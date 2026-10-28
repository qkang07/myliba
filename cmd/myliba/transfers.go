package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const transferChunkLimit = 1 << 20

type transferEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
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
		entries = append(entries, transferEntry{Name: item.Name(), Path: filepath.Join(path, item.Name()), Dir: child.IsDir(), Size: child.Size()})
	}
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	} else if _, err := s.authorizedTransferPath(parent, false); err != nil {
		parent = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"current": path, "parent": parent, "roots": []any{}, "entries": entries})
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
	}
	w.WriteHeader(http.StatusNoContent)
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
