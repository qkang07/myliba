package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	cloudChunkSize     = 4 << 20
	cloudMaxBlobSize   = cloudChunkSize + 64
	cloudMaxRecordSize = 1 << 20
)

// cloudDisk stores immutable encrypted blocks and catalog records. The catalog
// is opaque to storage peers and survives loss of the originating client.
type cloudDisk struct {
	dir     string
	backend cloudObjectBackend
	db      *sql.DB
	mu      sync.RWMutex
	roots   []cloudDiskRoot
}

type cloudDiskRoot struct {
	path      string
	cloudDir  string
	maxBytes  int64
	usedBytes int64
	readOnly  bool
}

type cloudFile struct {
	*os.File
	remove string
}

func (f *cloudFile) Close() error {
	err := f.File.Close()
	if f.remove != "" {
		_ = os.Remove(f.remove)
	}
	return err
}

func newCloudDisk(dir string) (*cloudDisk, error) {
	for _, child := range []string{"blocks", "catalog"} {
		if err := os.MkdirAll(filepath.Join(dir, child), 0700); err != nil {
			return nil, err
		}
	}
	return &cloudDisk{dir: dir, roots: []cloudDiskRoot{{path: filepath.Dir(dir), cloudDir: dir}}}, nil
}

func (d *cloudDisk) configureMediaStores(db *sql.DB) error {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`SELECT path,max_bytes FROM media_stores ORDER BY is_default DESC,(id='appdata') DESC,added_at,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	roots := make([]cloudDiskRoot, 0, 4)
	previous := make(map[string]cloudDiskRoot, len(d.roots))
	for _, root := range d.roots {
		previous[root.path] = root
	}
	seen := map[string]bool{}
	for rows.Next() {
		var root cloudDiskRoot
		if err := rows.Scan(&root.path, &root.maxBytes); err != nil {
			return err
		}
		root.path = filepath.Clean(root.path)
		if seen[root.path] {
			continue
		}
		seen[root.path] = true
		root.cloudDir = filepath.Join(root.path, "cloud")
		if err := os.MkdirAll(filepath.Join(root.cloudDir, "blocks"), 0700); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(root.cloudDir, "catalog"), 0700); err != nil {
			return err
		}
		root.readOnly = false
		if old, ok := previous[root.path]; ok && old.usedBytes > 0 {
			root.usedBytes = old.usedBytes
		} else {
			root.usedBytes = cloudDiskRootUsage(root.path)
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	legacyRoot := filepath.Clean(filepath.Dir(d.dir))
	if !seen[legacyRoot] {
		root := cloudDiskRoot{path: legacyRoot, cloudDir: d.dir, readOnly: true}
		if old, ok := previous[root.path]; ok && old.usedBytes > 0 {
			root.usedBytes = old.usedBytes
		} else {
			root.usedBytes = cloudDiskRootUsage(root.path)
		}
		roots = append(roots, root)
	}
	if len(roots) > 0 {
		d.roots = roots
	}
	d.db = db
	return nil
}

func cloudHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validCloudHash(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

func (d *cloudDisk) path(kind, id string) (string, error) {
	if !validCloudHash(id) {
		return "", errors.New("invalid content id")
	}
	if kind != "blocks" && kind != "catalog" {
		return "", errors.New("invalid content kind")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	base := d.dir
	if len(d.roots) > 0 {
		base = d.roots[0].cloudDir
	}
	return filepath.Join(base, kind, id[:2], id), nil
}

func (d *cloudDisk) existingPath(kind, id string) (string, error) {
	path, err := d.path(kind, id)
	if err != nil {
		return "", err
	}
	if d.backend != nil {
		return path, nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, root := range d.roots {
		candidate := filepath.Join(root.cloudDir, kind, id[:2], id)
		if info, e := os.Stat(candidate); e == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return path, nil
}

func (d *cloudDisk) has(kind, id string) bool {
	if d.backend != nil {
		return d.valid(kind, id)
	}
	path, err := d.existingPath(kind, id)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (d *cloudDisk) valid(kind, id string) bool {
	file, err := d.get(kind, id)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return hex.EncodeToString(hash.Sum(nil)) == id
}

func (d *cloudDisk) put(kind, id string, body io.Reader, limit int64) error {
	if _, err := d.path(kind, id); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return err
	}
	if len(data) == 0 || int64(len(data)) > limit || cloudHash(data) != id {
		return errors.New("invalid cloud object")
	}
	if kind == "catalog" {
		if err := validateCloudRecord(data); err != nil {
			return err
		}
	}
	if d.backend != nil {
		return d.backend.Put(kind, id, data)
	}
	if d.valid(kind, id) {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.db != nil {
		if err := d.configureMediaStores(d.db); err != nil {
			return err
		}
	}
	for i := range d.roots {
		root := d.roots[i]
		if root.readOnly {
			continue
		}
		existing := filepath.Join(root.cloudDir, kind, id[:2], id)
		if existingData, e := os.ReadFile(existing); e == nil && cloudHash(existingData) == id {
			return nil
		} else if e == nil {
			_ = os.Remove(existing)
			root.usedBytes = max(int64(0), root.usedBytes-int64(len(existingData)))
		}
		used := root.usedBytes
		if root.maxBytes > 0 && used+int64(len(data)) > root.maxBytes {
			continue
		}
		target := filepath.Join(root.cloudDir, kind, id[:2], id)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			continue
		}
		if info, e := os.Stat(target); e == nil && info.Mode().IsRegular() {
			_ = os.Remove(target)
		}
		temp, err := os.CreateTemp(filepath.Dir(target), ".cloud-*")
		if err != nil {
			continue
		}
		if _, err = temp.Write(data); err == nil {
			err = temp.Sync()
		}
		if closeErr := temp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temp.Name(), target)
		}
		if err != nil {
			_ = os.Remove(temp.Name())
			continue
		}
		d.roots[i].usedBytes = used + int64(len(data))
		return nil
	}
	return errors.New("所有本机存储目录均已达到容量上限或不可写")
}

func cloudDiskRootUsage(root string) int64 {
	var total int64
	for _, child := range []string{"thumbnails", "videos", "cloud"} {
		_ = filepath.WalkDir(filepath.Join(root, child), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if info, e := entry.Info(); e == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
	}
	return total
}

func (d *cloudDisk) setRootUsage(root string, used int64) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.roots {
		if filepath.Clean(d.roots[i].path) == filepath.Clean(root) {
			d.roots[i].usedBytes = used
		}
	}
}

func (d *cloudDisk) rootUsage(root string) (int64, bool) {
	if d == nil {
		return 0, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, item := range d.roots {
		if filepath.Clean(item.path) == filepath.Clean(root) {
			return item.usedBytes, true
		}
	}
	return 0, false
}

func validateCloudRecord(data []byte) error {
	var record cloudRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	if !validCloudHash(record.LibraryID) || record.ObjectID == "" || len(record.ObjectID) > 128 ||
		record.RevisionID == "" || len(record.RevisionID) > 128 || record.AuthorID == "" || len(record.AuthorID) > 128 ||
		record.Format > 3 || record.CreatedAt == "" || len(record.BlockIDs) > 10000 {
		return errors.New("invalid catalog record")
	}
	if _, err := time.Parse(time.RFC3339Nano, record.CreatedAt); err != nil {
		return err
	}
	nonce, err := hex.DecodeString(record.Nonce)
	if err != nil || len(nonce) != 12 {
		return errors.New("invalid catalog nonce")
	}
	sealed, err := hex.DecodeString(record.Sealed)
	if err != nil || len(sealed) < 16 {
		return errors.New("invalid encrypted manifest")
	}
	for _, id := range record.BlockIDs {
		if !validCloudHash(id) {
			return errors.New("invalid block ID")
		}
	}
	return nil
}

func (d *cloudDisk) get(kind, id string) (*cloudFile, error) {
	path, err := d.existingPath(kind, id)
	if err != nil {
		return nil, err
	}
	if d.backend == nil {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		return &cloudFile{File: file}, nil
	}
	data, err := d.backend.Get(kind, id)
	if err != nil {
		return nil, err
	}
	if cloudHash(data) != id {
		return nil, errors.New("cloud backend hash mismatch")
	}
	file, err := os.CreateTemp(d.dir, ".cloud-read-*")
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(data); err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	return &cloudFile{File: file, remove: file.Name()}, nil
}

func (d *cloudDisk) list(kind string) ([]string, error) {
	if kind != "blocks" && kind != "catalog" {
		return nil, errors.New("invalid content kind")
	}
	if d.backend != nil {
		return d.backend.List(kind)
	}
	d.mu.RLock()
	roots := append([]cloudDiskRoot(nil), d.roots...)
	d.mu.RUnlock()
	seen := map[string]bool{}
	ids := []string{}
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(root.cloudDir, kind), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.Type().IsRegular() && validCloudHash(entry.Name()) && !seen[entry.Name()] {
				if kind != "catalog" || d.valid(kind, entry.Name()) {
					seen[entry.Name()] = true
					ids = append(ids, entry.Name())
				}
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func cloudBearer(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if !strings.HasPrefix(authorization, "Bearer ") || len(token) < 32 ||
			!safeEqual(strings.TrimPrefix(authorization, "Bearer "), token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (d *cloudDisk) routes(mux *http.ServeMux, token string) {
	d.routesWithAuth(mux, func(next http.Handler) http.Handler { return cloudBearer(next, token) })
}

func (d *cloudDisk) routesWithAuth(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("GET /cloud/ping", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /cloud/diagnostic-payload", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, err := strconv.Atoi(r.URL.Query().Get("bytes"))
		if err != nil || size < 1 || size > 4<<20 {
			http.Error(w, "invalid diagnostic size", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(make([]byte, size))
	})))
	mux.Handle("GET /cloud/inventory", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		if after != "" && !validCloudHash(after) {
			http.Error(w, "invalid cursor", 400)
			return
		}
		ids, err := d.list("blocks")
		if err != nil {
			http.Error(w, "inventory unavailable", 500)
			return
		}
		start := sort.SearchStrings(ids, after)
		if after != "" && start < len(ids) && ids[start] == after {
			start++
		}
		end := start + 1000
		if end > len(ids) {
			end = len(ids)
		}
		next := ""
		if end < len(ids) {
			next = ids[end-1]
		}
		writeJSON(w, 200, map[string]any{"blocks": ids[start:end], "next": next})
	})))
	mux.Handle("GET /cloud/catalog", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids, err := d.list("catalog")
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"records": ids})
	})))
	for _, kind := range []string{"blocks", "catalog"} {
		kind := kind
		base := "/cloud/" + kind + "/{id}"
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPut} {
			method := method
			mux.Handle(method+" "+base, authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.PathValue("id")
				if !validCloudHash(id) {
					http.Error(w, "invalid id", 400)
					return
				}
				if method == http.MethodPut {
					limit := int64(cloudMaxBlobSize)
					if kind == "catalog" {
						limit = cloudMaxRecordSize
					}
					if err := d.put(kind, id, r.Body, limit); err != nil {
						http.Error(w, fmt.Sprintf("store object: %v", err), 400)
						return
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if method == http.MethodHead && !d.valid(kind, id) {
					http.Error(w, "not found", 404)
					return
				}
				file, err := d.get(kind, id)
				if err != nil {
					http.Error(w, "not found", 404)
					return
				}
				defer file.Close()
				info, err := file.Stat()
				if err != nil {
					http.Error(w, "read failed", 500)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
				w.Header().Set("Cache-Control", "private, immutable")
				if method == http.MethodGet {
					_, _ = io.Copy(w, file)
				}
			})))
		}
	}
}

func cloudJSON(body io.Reader, max int64, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, max+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing JSON")
	}
	return nil
}
