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

type collectionFilters struct {
	Query       string   `json:"query"`
	Tags        []string `json:"tags"`
	Attributes  []string `json:"attributes"`
	Kind        string   `json:"kind"`
	RootID      string   `json:"rootId"`
	FolderID    string   `json:"folderId"`
	Unorganized bool     `json:"unorganized"`
	Sort        string   `json:"sort"`
}

type smartCollection struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Filters   collectionFilters `json:"filters"`
	CreatedAt string            `json:"createdAt"`
}

type collectionInput struct {
	Name    string            `json:"name"`
	Filters collectionFilters `json:"filters"`
}

func validCollectionKind(kind string) bool {
	switch kind {
	case "", "image", "video", "audio", "document", "file":
		return true
	default:
		return false
	}
}

func validCollectionSort(sort string) bool {
	switch sort {
	case "", "modified_desc", "modified_asc", "name_asc", "name_desc", "size_desc":
		return true
	default:
		return false
	}
}

func (s *server) readCollectionInput(w http.ResponseWriter, r *http.Request) (collectionInput, bool) {
	var input collectionInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "集合内容无效")
		return input, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "集合内容无效")
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Filters.Query = strings.TrimSpace(input.Filters.Query)
	input.Filters.RootID = strings.TrimSpace(input.Filters.RootID)
	input.Filters.FolderID = strings.TrimSpace(input.Filters.FolderID)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 || utf8.RuneCountInString(input.Filters.Query) > 200 ||
		!validCollectionKind(input.Filters.Kind) || !validCollectionSort(input.Filters.Sort) || len(input.Filters.Tags) > 20 {
		writeError(w, http.StatusBadRequest, "集合名称或筛选条件无效")
		return input, false
	}
	if input.Filters.Sort == "" {
		input.Filters.Sort = "modified_desc"
	}
	seen := map[string]bool{}
	tags := []string{}
	for _, raw := range input.Filters.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || utf8.RuneCountInString(tag) > 80 {
			writeError(w, http.StatusBadRequest, "标签筛选条件无效")
			return input, false
		}
		key := strings.ToLower(tag)
		if !seen[key] {
			seen[key] = true
			tags = append(tags, tag)
		}
	}
	input.Filters.Tags = tags
	attributes, err := normalizeAttributeKeys(input.Filters.Attributes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return input, false
	}
	input.Filters.Attributes = attributes
	if input.Filters.Unorganized && len(tags) > 0 {
		writeError(w, http.StatusBadRequest, "待整理条件不能同时包含标签")
		return input, false
	}
	if input.Filters.RootID != "" {
		var id string
		err := s.db.QueryRow(`SELECT id FROM roots WHERE id=?`, input.Filters.RootID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "索引目录不存在")
			return input, false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取索引目录")
			return input, false
		}
	}
	if input.Filters.FolderID != "" && input.Filters.FolderID != "all" && input.Filters.FolderID != "unfiled" {
		var id string
		err := s.db.QueryRow(`SELECT id FROM folders WHERE id=?`, input.Filters.FolderID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "文件夹不存在")
			return input, false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取文件夹")
			return input, false
		}
	}
	return input, true
}

func (s *server) listCollections(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT id,name,filters_json,created_at FROM smart_collections ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取智能集合")
		return
	}
	defer rows.Close()
	items := []smartCollection{}
	for rows.Next() {
		var item smartCollection
		var filtersJSON string
		if err := rows.Scan(&item.ID, &item.Name, &filtersJSON, &item.CreatedAt); err != nil || json.Unmarshal([]byte(filtersJSON), &item.Filters) != nil {
			writeError(w, http.StatusInternalServerError, "无法读取智能集合")
			return
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "无法读取智能集合")
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) createCollection(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readCollectionInput(w, r)
	if !ok {
		return
	}
	filtersJSON, _ := json.Marshal(input.Filters)
	item := smartCollection{ID: newID(), Name: input.Name, Filters: input.Filters, CreatedAt: now()}
	_, err := s.db.Exec(`INSERT INTO smart_collections(id,name,filters_json,created_at) VALUES(?,?,?,?)`, item.ID, item.Name, string(filtersJSON), item.CreatedAt)
	if err != nil {
		writeCollectionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) updateCollection(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readCollectionInput(w, r)
	if !ok {
		return
	}
	filtersJSON, _ := json.Marshal(input.Filters)
	result, err := s.db.Exec(`UPDATE smart_collections SET name=?,filters_json=? WHERE id=?`, input.Name, string(filtersJSON), r.PathValue("id"))
	if err != nil {
		writeCollectionError(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "智能集合不存在")
		return
	}
	var item smartCollection
	item.ID = r.PathValue("id")
	item.Name = input.Name
	item.Filters = input.Filters
	if err := s.db.QueryRow(`SELECT created_at FROM smart_collections WHERE id=?`, item.ID).Scan(&item.CreatedAt); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取智能集合")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) deleteCollection(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Exec(`DELETE FROM smart_collections WHERE id=?`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除智能集合")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "智能集合不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeCollectionError(w http.ResponseWriter, err error) {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		writeError(w, http.StatusConflict, "已有同名智能集合")
		return
	}
	writeError(w, http.StatusInternalServerError, "无法保存智能集合")
}
