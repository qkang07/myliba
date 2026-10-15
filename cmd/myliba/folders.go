package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type virtualFolder struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
	CreatedAt string `json:"createdAt"`
}

// Empty/all preserve the existing unscoped API. "unfiled" is the built-in
// group for resources without a virtual folder.
func folderScopeSQL(folderID, resourceAlias string) (string, []any) {
	switch folderID {
	case "", "all":
		return "", nil
	case "unfiled":
		return `NOT EXISTS (SELECT 1 FROM resource_folders rf WHERE rf.resource_id=` + resourceAlias + `.id)`, nil
	default:
		return `EXISTS (SELECT 1 FROM resource_folders rf WHERE rf.resource_id=` + resourceAlias + `.id AND rf.folder_id=?)`, []any{folderID}
	}
}

func readFolderName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "文件夹名称无效")
		return "", false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "文件夹名称无效")
		return "", false
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || name == "未归组" || name == "全部资源" || utf8.RuneCountInString(name) > 80 {
		writeError(w, http.StatusBadRequest, "文件夹名称需为 1 到 80 个字符")
		return "", false
	}
	return name, true
}

func (s *server) listFolders(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT f.id,f.name,f.created_at,COUNT(rf.resource_id)
		FROM folders f LEFT JOIN resource_folders rf ON rf.folder_id=f.id
		GROUP BY f.id ORDER BY f.name COLLATE NOCASE,f.id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	items := []virtualFolder{}
	for rows.Next() {
		var item virtualFolder
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt, &item.Count); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "无法读取文件夹")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	rows.Close()
	var unfiledCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resources r WHERE NOT EXISTS
		(SELECT 1 FROM resource_folders rf WHERE rf.resource_id=r.id)`).Scan(&unfiledCount); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unfiledCount": unfiledCount})
}

func (s *server) createFolder(w http.ResponseWriter, r *http.Request) {
	name, ok := readFolderName(w, r)
	if !ok {
		return
	}
	item := virtualFolder{ID: newID(), Name: name, CreatedAt: now()}
	if _, err := s.db.Exec(`INSERT INTO folders(id,name,created_at) VALUES(?,?,?)`, item.ID, item.Name, item.CreatedAt); err != nil {
		writeFolderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) renameFolder(w http.ResponseWriter, r *http.Request) {
	name, ok := readFolderName(w, r)
	if !ok {
		return
	}
	result, err := s.db.Exec(`UPDATE folders SET name=? WHERE id=?`, name, r.PathValue("id"))
	if err != nil {
		writeFolderError(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	var item virtualFolder
	if err := s.db.QueryRow(`SELECT f.id,f.name,f.created_at,COUNT(rf.resource_id)
		FROM folders f LEFT JOIN resource_folders rf ON rf.folder_id=f.id
		WHERE f.id=? GROUP BY f.id`, r.PathValue("id")).Scan(&item.ID, &item.Name, &item.CreatedAt, &item.Count); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	defer tx.Rollback()
	// Keep saved collections usable after their folder is removed.
	rows, err := tx.Query(`SELECT id,filters_json FROM smart_collections`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	updates := map[string]string{}
	for rows.Next() {
		var id, raw string
		var filters collectionFilters
		if err := rows.Scan(&id, &raw); err != nil || json.Unmarshal([]byte(raw), &filters) != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "无法更新关联集合")
			return
		}
		if filters.FolderID == r.PathValue("id") {
			filters.FolderID = "unfiled"
			data, _ := json.Marshal(filters)
			updates[id] = string(data)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "无法更新关联集合")
		return
	}
	rows.Close()
	for id, raw := range updates {
		if _, err := tx.Exec(`UPDATE smart_collections SET filters_json=? WHERE id=?`, raw, id); err != nil {
			writeError(w, http.StatusInternalServerError, "无法更新关联集合")
			return
		}
	}
	result, err := tx.Exec(`DELETE FROM folders WHERE id=?`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) moveResourcesToFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs      []string `json:"ids"`
		FolderID string   `json:"folderId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.IDs) == 0 || len(input.IDs) > 1000 {
		writeError(w, http.StatusBadRequest, "请选择 1 到 1000 个资源")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "移动请求无效")
		return
	}
	input.FolderID = strings.TrimSpace(input.FolderID)
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法移动资源")
		return
	}
	defer tx.Rollback()
	if input.FolderID != "" {
		var id string
		err := tx.QueryRow(`SELECT id FROM folders WHERE id=?`, input.FolderID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "目标文件夹不存在")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取目标文件夹")
			return
		}
	}
	seen := map[string]bool{}
	count := 0
	for _, rawID := range input.IDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var existing string
		if err := tx.QueryRow(`SELECT id FROM resources WHERE id=?`, id).Scan(&existing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "资源不存在")
			} else {
				writeError(w, http.StatusInternalServerError, "无法读取资源")
			}
			return
		}
		if input.FolderID == "" {
			_, err = tx.Exec(`DELETE FROM resource_folders WHERE resource_id=?`, id)
		} else {
			_, err = tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) VALUES(?,?)
				ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, id, input.FolderID)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法移动资源")
			return
		}
		count++
	}
	if count == 0 {
		writeError(w, http.StatusBadRequest, "请选择资源")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法移动资源")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": count})
}

func writeFolderError(w http.ResponseWriter, err error) {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		writeError(w, http.StatusConflict, "已有同名文件夹")
		return
	}
	writeError(w, http.StatusInternalServerError, "无法保存文件夹")
}
