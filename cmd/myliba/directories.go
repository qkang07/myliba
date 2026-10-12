package main

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

type directoryItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type directoryListing struct {
	Current     string          `json:"current"`
	Parent      string          `json:"parent"`
	Roots       []directoryItem `json:"roots"`
	Directories []directoryItem `json:"directories"`
}

func (s *server) directories(w http.ResponseWriter, r *http.Request) {
	requested := r.URL.Query().Get("path")
	if requested == "" {
		roots, err := localDirectoryRoots()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取本机磁盘列表："+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, directoryListing{Roots: roots, Directories: []directoryItem{}})
		return
	}
	if !filepath.IsAbs(requested) {
		writeError(w, http.StatusBadRequest, "目录路径必须是绝对路径")
		return
	}
	path := filepath.Clean(requested)
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusNotFound, "目录不存在或无法访问")
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeError(w, http.StatusForbidden, "无法读取该目录，请返回上一级目录")
		return
	}
	listing := directoryListing{
		Current:     path,
		Parent:      filepath.Dir(path),
		Roots:       []directoryItem{},
		Directories: []directoryItem{},
	}
	if listing.Parent == path {
		listing.Parent = ""
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		childPath := filepath.Join(path, entry.Name())
		listing.Directories = append(listing.Directories, directoryItem{Name: entry.Name(), Path: childPath})
	}
	writeJSON(w, http.StatusOK, listing)
}

func localDirectoryRoots() ([]directoryItem, error) {
	if runtime.GOOS != "windows" {
		return []directoryItem{{Name: string(filepath.Separator), Path: string(filepath.Separator)}}, nil
	}
	roots := make([]directoryItem, 0, 4)
	for letter := 'A'; letter <= 'Z'; letter++ {
		path := string(letter) + ":\\"
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			roots = append(roots, directoryItem{Name: path, Path: path})
		}
	}
	return roots, nil
}
