package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	return &cloudDisk{dir: dir}, nil
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
	return filepath.Join(d.dir, kind, id[:2], id), nil
}

func (d *cloudDisk) has(kind, id string) bool {
	if d.backend != nil {
		return d.valid(kind, id)
	}
	path, err := d.path(kind, id)
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
	path, err := d.path(kind, id)
	if err != nil {
		return err
	}
	if d.backend != nil {
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
		return d.backend.Put(kind, id, data)
	}
	if d.valid(kind, id) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cloud-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, limit+1))
	if err != nil {
		return err
	}
	if n == 0 || n > limit {
		return errors.New("invalid object size")
	}
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(hash.Sum(nil))), []byte(id)) != 1 {
		return errors.New("content hash mismatch")
	}
	if kind == "catalog" {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			return err
		}
		if err := validateCloudRecord(data); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Immutable IDs make duplicate concurrent writes harmless.
	if d.valid(kind, id) {
		return nil
	}
	if d.has(kind, id) {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return os.Rename(file.Name(), path)
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
	path, err := d.path(kind, id)
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
	ids := []string{}
	err := filepath.WalkDir(filepath.Join(d.dir, kind), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() && validCloudHash(entry.Name()) && (kind != "catalog" || d.valid(kind, entry.Name())) {
			ids = append(ids, entry.Name())
		}
		return nil
	})
	sort.Strings(ids)
	return ids, err
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
