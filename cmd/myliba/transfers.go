package main

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const transferChunkLimit = 1 << 20

type transferEntry struct {
	Name       string   `json:"name"`
	Path       string   `json:"path"`
	Dir        bool     `json:"dir"`
	Size       int64    `json:"size"`
	FileID     string   `json:"fileId,omitempty"`
	RevisionID string   `json:"revisionId,omitempty"`
	FolderName string   `json:"folderName,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

func (s *server) transferBrowse(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("path")
	if requested == "" {
		roots, err := s.transferRoots()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取传输目录")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"current": "", "parent": "", "roots": roots, "entries": []transferEntry{}})
		return
	}
	path, err := s.authorizedTransferPath(requested, false)
	if err != nil {
		writeError(w, http.StatusForbidden, "传输只能访问已添加的资源根目录")
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusNotFound, "目录不存在或无法访问")
		return
	}
	items, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusForbidden, "无法读取该目录")
		return
	}
	entries := make([]transferEntry, 0, len(items))
	for _, item := range items {
		if item.Type()&os.ModeSymlink != 0 {
			continue
		}
		child, err := item.Info()
		if err != nil || (!child.IsDir() && !child.Mode().IsRegular()) {
			continue
		}
		entry := transferEntry{Name: item.Name(), Path: filepath.Join(path, item.Name()), Dir: child.IsDir(), Size: child.Size()}
		if !entry.Dir {
			s.transferEntryIdentity(&entry)
		}
		entries = append(entries, entry)
	}
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	} else if _, err := s.authorizedTransferPath(parent, false); err != nil {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": path, "parent": parent, "roots": []any{}, "entries": entries})
}

// transferEntryIdentity exposes no path beyond the already-authorized browse
// response. It supplies an optional P2 envelope for a peer that understands
// logical identities; unindexed files remain transferable as ordinary files.
func (s *server) transferEntryIdentity(entry *transferEntry) {
	var resourceID string
	if err := s.db.QueryRow(`SELECT id FROM resources WHERE path=?`, entry.Path).Scan(&resourceID); err != nil {
		return
	}
	fileID, err := s.unifiedFileID(resourceID)
	if err != nil {
		return
	}
	entry.FileID = fileID
	_ = s.db.QueryRow(`SELECT current_version_id FROM resources WHERE id=?`, resourceID).Scan(&entry.RevisionID)
	_ = s.db.QueryRow(`SELECT f.name FROM resource_folders rf JOIN folders f ON f.id=rf.folder_id WHERE rf.resource_id=?`, resourceID).Scan(&entry.FolderName)
	rows, err := s.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=? ORDER BY t.name COLLATE NOCASE`, resourceID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		if rows.Scan(&tag) == nil {
			entry.Tags = append(entry.Tags, tag)
		}
	}
}

func (s *server) transferFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	path, err := s.authorizedTransferPath(path, r.Method == http.MethodPut || r.Method == http.MethodDelete)
	if err != nil {
		writeError(w, http.StatusForbidden, "传输只能访问已添加的资源根目录")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.transferReadChunk(w, r, path)
	case http.MethodPut:
		s.transferWriteChunk(w, r, path)
	case http.MethodDelete:
		s.transferCancel(w, r, path)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *server) transferReadChunk(w http.ResponseWriter, r *http.Request, path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusNotFound, "文件不存在或无法读取")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 || offset > info.Size() {
		writeError(w, http.StatusBadRequest, "文件偏移无效")
		return
	}
	length, err := strconv.ParseInt(r.URL.Query().Get("length"), 10, 64)
	if err != nil || length < 0 || length > transferChunkLimit || length > info.Size()-offset {
		writeError(w, http.StatusBadRequest, "分块长度无效")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "无法打开文件")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件")
		return
	}
	if _, err := io.CopyN(w, f, length); err != nil {
		return
	}
}

func (s *server) transferWriteChunk(w http.ResponseWriter, r *http.Request, path string) {
	if r.ContentLength < 0 || r.ContentLength > transferChunkLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "单次传输分块不能超过 1 MiB")
		return
	}
	_, err := os.Lstat(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusConflict, "目标文件已存在或不可用")
		return
	}
	parent := filepath.Dir(path)
	dir, err := os.Lstat(parent)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		writeError(w, http.StatusBadRequest, "目标目录不存在或无法访问")
		return
	}
	id := r.URL.Query().Get("id")
	if len(id) < 16 || len(id) > 64 || strings.Trim(id, "0123456789abcdef") != "" {
		writeError(w, http.StatusBadRequest, "传输 ID 无效")
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "文件偏移无效")
		return
	}
	total, err := strconv.ParseInt(r.URL.Query().Get("total"), 10, 64)
	if err != nil || total < 0 || offset+r.ContentLength > total {
		writeError(w, http.StatusBadRequest, "文件大小无效")
		return
	}
	temp := path + ".myliba-transfer-" + id + ".part"
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(temp, flags, 0600)
	if err != nil {
		writeError(w, http.StatusConflict, "无法创建传输临时文件")
		return
	}
	if _, err = f.Seek(offset, io.SeekStart); err == nil {
		_, err = io.CopyN(f, r.Body, r.ContentLength)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "写入传输分块失败")
		return
	}
	if r.URL.Query().Get("final") == "1" {
		written, statErr := os.Stat(temp)
		if statErr != nil || written.Size() != total {
			writeError(w, http.StatusConflict, "传输文件大小不完整")
			return
		}
		if err := os.Link(temp, path); err != nil {
			writeError(w, http.StatusConflict, "无法完成文件写入；目标可能已存在")
			return
		}
		if err := os.Remove(temp); err != nil {
			writeError(w, http.StatusInternalServerError, "文件已写入，但临时文件清理失败")
			return
		}
		if fileID := strings.TrimSpace(r.URL.Query().Get("fileId")); fileID != "" {
			if err := s.registerTransferredFile(path, fileID, strings.TrimSpace(r.URL.Query().Get("revisionId")), strings.TrimSpace(r.URL.Query().Get("folderName")), r.URL.Query()["tag"]); err != nil {
				writeError(w, http.StatusInternalServerError, "文件已写入，但无法登记统一索引："+err.Error())
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// registerTransferredFile creates a new local location for an existing logical
// file. The incoming folder/tag values are an initial snapshot only: if this
// logical file is already known locally, its local classification wins until
// P3's causal operation protocol is available.
func (s *server) registerTransferredFile(path, fileID, revisionID, folderName string, tags []string) error {
	if !validTransferIdentity(fileID) || !validTransferIdentity(revisionID) {
		return errors.New("传输身份无效")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("目标文件不可用")
	}
	var rootID string
	rows, err := s.db.Query(`SELECT id,path FROM roots`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err == nil && pathWithin(path, root) {
			rootID = id
			break
		}
	}
	rows.Close()
	if rootID == "" {
		return errors.New("目标目录不属于资源根目录")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	resourceID := newID()
	resourceVersionID := revisionID
	var existingVersion string
	if err = tx.QueryRow(`SELECT id FROM versions WHERE id=?`, resourceVersionID).Scan(&existingVersion); err == nil {
		resourceVersionID = newID()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	stamp := now()
	modified := info.ModTime().UTC().Format(time.RFC3339Nano)
	physicalID := localFileID(path)
	if _, err = tx.Exec(`INSERT INTO resources(id,root_id,path,file_id,current_version_id,name,size,modified_at,kind,available,first_seen_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,1,?,?)`, resourceID, rootID, path, physicalID, resourceVersionID, filepath.Base(path), info.Size(), modified, fileKind(path), stamp, stamp); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO versions(id,resource_id,size,modified_at,discovered_at) VALUES(?,?,?,?,?)`, resourceVersionID, resourceID, info.Size(), modified, stamp); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET updated_at=excluded.updated_at`, fileID, stamp, stamp); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE library_resource_aliases SET file_id=? WHERE resource_id=?`, fileID, resourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_revisions(revision_id,file_id,resource_version_id,size,modified_at,created_at) VALUES(?,?,?,?,?,?)`, revisionID, fileID, resourceVersionID, info.Size(), modified, stamp); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO library_locations(location_id,file_id,revision_id,resource_id,storage_id,resource_ref,state,updated_at)
		VALUES(?,?,?,?,?,?, 'available',?)`, "legacy-resource:"+resourceID, fileID, revisionID, resourceID, rootID, path, stamp); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM resource_fts WHERE resource_id=?`, resourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO resource_fts(resource_id,name,path) VALUES(?,?,?)`, resourceID, filepath.Base(path), path); err != nil {
		return err
	}
	var classified int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM library_file_folders WHERE file_id=?`, fileID).Scan(&classified); err != nil {
		return err
	}
	if classified == 0 {
		var tagCount int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM library_file_tags WHERE file_id=?`, fileID).Scan(&tagCount); err != nil {
			return err
		}
		classified = tagCount
	}
	if classified == 0 {
		if err = applyTransferredClassification(tx, resourceID, folderName, tags, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validTransferIdentity(value string) bool {
	return value != "" && len(value) <= 160 && !strings.ContainsRune(value, '\x00')
}

func applyTransferredClassification(tx *sql.Tx, resourceID, folderName string, tags []string, stamp string) error {
	folderName = strings.TrimSpace(folderName)
	if folderName != "" && len([]rune(folderName)) <= 80 {
		folderID := newID()
		if _, err := tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, folderID, folderName, stamp); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT id FROM folders WHERE name=?`, folderName).Scan(&folderID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) VALUES(?,?)`, resourceID, folderID); err != nil {
			return err
		}
	}
	for _, name := range tags {
		name = strings.TrimSpace(name)
		if name == "" || len([]rune(name)) > 40 {
			continue
		}
		tagID := newID()
		if _, err := tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, tagID, name, stamp); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, resourceID, tagID, stamp); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) transferCancel(w http.ResponseWriter, r *http.Request, path string) {
	id := r.URL.Query().Get("id")
	if len(id) < 16 || len(id) > 64 || strings.Trim(id, "0123456789abcdef") != "" {
		writeError(w, http.StatusBadRequest, "传输 ID 无效")
		return
	}
	err := os.Remove(path + ".myliba-transfer-" + id + ".part")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, "无法清理未完成文件")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) transferRoots() ([]directoryItem, error) {
	rows, err := s.db.Query(`SELECT id,path FROM roots ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := make([]directoryItem, 0)
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			return nil, err
		}
		roots = append(roots, directoryItem{Name: filepath.Base(filepath.Clean(path)), Path: filepath.Clean(path)})
	}
	return roots, rows.Err()
}

func (s *server) authorizedTransferPath(requested string, allowMissingLeaf bool) (string, error) {
	if requested == "" || !filepath.IsAbs(requested) || filepath.Clean(requested) != requested {
		return "", errors.New("invalid path")
	}
	roots, err := s.transferRoots()
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		rel, err := filepath.Rel(root.Path, requested)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		current := root.Path
		if rel != "." {
			for i, part := range strings.Split(rel, string(filepath.Separator)) {
				current = filepath.Join(current, part)
				info, statErr := os.Lstat(current)
				if errors.Is(statErr, os.ErrNotExist) && allowMissingLeaf && i == len(strings.Split(rel, string(filepath.Separator)))-1 {
					return current, nil
				}
				if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("unsafe path")
				}
			}
		}
		return filepath.Clean(requested), nil
	}
	return "", errors.New("outside transfer roots")
}
