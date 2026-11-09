package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

func (s *server) logicalFileForEdit(w http.ResponseWriter, fileID string) bool {
	if fileID == "" || len(fileID) > 160 {
		writeError(w, http.StatusBadRequest, "文件标识无效")
		return false
	}
	var deleted int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?`, fileID).Scan(&deleted); err != nil {
		writeError(w, 500, "无法读取文件状态")
		return false
	}
	if deleted != 0 {
		writeError(w, http.StatusConflict, "文件已永久删除")
		return false
	}
	files, err := s.cloud.libraryFiles(true)
	if err != nil {
		writeError(w, 500, "无法读取统一资源库")
		return false
	}
	for _, file := range files {
		if file.FileID == fileID {
			return true
		}
	}
	writeError(w, http.StatusNotFound, "文件不存在")
	return false
}

func (s *server) hiddenLogicalResource(w http.ResponseWriter, resourceID string) bool {
	var deleted, trashed int
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM library_resource_aliases a JOIN library_file_deletions d ON d.file_id=a.file_id WHERE a.resource_id=?), EXISTS(SELECT 1 FROM library_resource_aliases a JOIN library_file_lifecycle l ON l.file_id=a.file_id WHERE a.resource_id=? AND l.state='trashed')`, resourceID, resourceID).Scan(&deleted, &trashed)
	if err != nil {
		writeError(w, 500, "无法读取文件状态")
		return true
	}
	if deleted != 0 {
		writeError(w, http.StatusGone, "文件条目已永久删除")
		return true
	}
	if trashed != 0 {
		writeError(w, http.StatusNotFound, "文件已移入回收站")
		return true
	}
	return false
}

func (s *server) applyLogicalClassification(w http.ResponseWriter, fileID, kind string, payload map[string]any, tagName string) {
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法保存分类")
		return
	}
	defer tx.Rollback()
	if err = ensureOperationFile(tx, fileID); err == nil {
		err = seedLegacyClassification(tx, fileID)
	}
	if err != nil {
		writeError(w, 500, "无法读取原有分类")
		return
	}
	if kind == "tag.remove" {
		payload["removeTokens"], err = observedTagTokens(tx, []string{fileID}, tagName)
		if err != nil {
			writeError(w, 500, "无法读取标签状态")
			return
		}
	}
	var op libraryOperationEnvelope
	if err = recordLibraryOperation(tx, kind, payload, &op); err == nil {
		err = materializeLibraryOperation(tx, op)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, 500, "无法保存分类")
		return
	}
	writeJSON(w, 200, map[string]any{"fileId": fileID, "operationId": op.OpID, "state": "pending"})
}

func (s *server) addLogicalFileTag(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "标签无效")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 40 || !s.logicalFileForEdit(w, fileID) {
		if name == "" || len([]rune(name)) > 40 {
			writeError(w, 400, "标签无效")
		}
		return
	}
	s.applyLogicalClassification(w, fileID, "tag.add", map[string]any{"fileIds": []string{fileID}, "tag": name}, name)
}

func (s *server) removeLogicalFileTag(w http.ResponseWriter, r *http.Request) {
	fileID, name := strings.TrimSpace(r.PathValue("fileId")), strings.TrimSpace(r.PathValue("tag"))
	if name == "" || len([]rune(name)) > 40 {
		writeError(w, 400, "标签无效")
		return
	}
	if !s.logicalFileForEdit(w, fileID) {
		return
	}
	s.applyLogicalClassification(w, fileID, "tag.remove", map[string]any{"fileIds": []string{fileID}, "tag": name}, name)
}

func (s *server) moveLogicalFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		FolderName string `json:"folderName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "文件夹无效")
		return
	}
	name := strings.TrimSpace(input.FolderName)
	if len([]rune(name)) > 80 {
		writeError(w, 400, "文件夹无效")
		return
	}
	if !s.logicalFileForEdit(w, fileID) {
		return
	}
	if name != "" {
		var password string
		err := s.db.QueryRow(`SELECT password_hash FROM folders WHERE name=? COLLATE NOCASE`, name).Scan(&password)
		if err != nil && err != sql.ErrNoRows {
			writeError(w, 500, "无法读取文件夹")
			return
		}
		if password != "" {
			writeError(w, http.StatusForbidden, "受保护文件夹只能在来源节点整理")
			return
		}
	}
	folderID := ""
	if name != "" {
		folderID = "logical"
	}
	s.applyLogicalClassification(w, fileID, "folder.move", map[string]any{"fileIds": []string{fileID}, "folderId": folderID, "folderName": name}, "")
}
