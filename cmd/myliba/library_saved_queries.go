package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// librarySavedQuery stores conditions rather than a result snapshot, so a
// query always reflects the current cloud and device index projection.
type librarySavedQueryFilters struct {
	Scope           string                  `json:"scope"`
	Lifecycle       string                  `json:"lifecycle,omitempty"`
	Query           string                  `json:"query"`
	FolderID        string                  `json:"folderId"`
	Tags            []string                `json:"tags"`
	Sort            string                  `json:"sort"`
	Type            string                  `json:"type,omitempty"`
	Grouped         *bool                   `json:"grouped,omitempty"`
	CloudCopy       *bool                   `json:"cloudCopy,omitempty"`
	Readable        *bool                   `json:"readable,omitempty"`
	BrowseID        string                  `json:"browseId,omitempty"`
	BrowseMode      string                  `json:"browseMode,omitempty"`
	IndexFilters    []libraryIndexCondition `json:"indexFilters,omitempty"`
	NeedsOrganizing *bool                   `json:"needsOrganizing,omitempty"`
}

type librarySavedQuery struct {
	ID        string                   `json:"id"`
	Name      string                   `json:"name"`
	Filters   librarySavedQueryFilters `json:"filters"`
	CreatedAt string                   `json:"createdAt"`
	UpdatedAt string                   `json:"updatedAt"`
}

type librarySavedQueryInput struct {
	Name    string                   `json:"name"`
	Filters librarySavedQueryFilters `json:"filters"`
}

func savedQueryClassificationFilter(filters librarySavedQueryFilters) libraryClassificationSearch {
	result := libraryClassificationSearch{Filters: append([]libraryIndexCondition(nil), filters.IndexFilters...)}
	if filters.BrowseID != "" {
		result.Browse = &libraryIndexCondition{ID: filters.BrowseID, Mode: filters.BrowseMode}
	}
	if filters.NeedsOrganizing != nil {
		result.NeedsOrganizing = strconv.FormatBool(*filters.NeedsOrganizing)
	}
	return result
}

func normalizeLibrarySavedQuery(input *librarySavedQueryInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.Filters.Scope = strings.TrimSpace(input.Filters.Scope)
	input.Filters.Lifecycle = strings.TrimSpace(input.Filters.Lifecycle)
	input.Filters.Query = strings.TrimSpace(input.Filters.Query)
	input.Filters.FolderID = strings.TrimSpace(input.Filters.FolderID)
	input.Filters.BrowseID = strings.TrimSpace(input.Filters.BrowseID)
	input.Filters.BrowseMode = strings.TrimSpace(input.Filters.BrowseMode)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 || utf8.RuneCountInString(input.Filters.Query) > 200 || len(input.Filters.Tags) > 20 {
		return errors.New("查询名称或筛选条件无效")
	}
	if input.Filters.Scope == "" {
		input.Filters.Scope = "all"
	}
	if input.Filters.Scope != "cloud" && input.Filters.Scope != "all" && input.Filters.Scope != "local" && !strings.HasPrefix(input.Filters.Scope, "device:") {
		return errors.New("检索范围无效")
	}
	if input.Filters.Lifecycle != "" && input.Filters.Lifecycle != "active" && input.Filters.Lifecycle != "trashed" {
		return errors.New("文件状态无效")
	}
	if strings.HasPrefix(input.Filters.Scope, "device:") && (len(input.Filters.Scope) <= len("device:") || len(input.Filters.Scope) > len("device:")+160) {
		return errors.New("设备范围无效")
	}
	if input.Filters.Type != "" && input.Filters.Type != "image" && input.Filters.Type != "video" && input.Filters.Type != "audio" && input.Filters.Type != "document" && input.Filters.Type != "other" {
		return errors.New("文件类型无效")
	}
	switch input.Filters.Sort {
	case "", "name_asc", "name_desc", "modified_desc", "modified_asc", "ingested_desc", "ingested_asc":
		if input.Filters.Sort == "" {
			input.Filters.Sort = "name_asc"
		}
	default:
		return errors.New("排序方式无效")
	}
	seen := map[string]bool{}
	tags := make([]string, 0, len(input.Filters.Tags))
	for _, raw := range input.Filters.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" || utf8.RuneCountInString(tag) > 80 {
			return errors.New("标签筛选条件无效")
		}
		key := strings.ToLower(tag)
		if !seen[key] {
			seen[key] = true
			tags = append(tags, tag)
		}
	}
	input.Filters.Tags = tags
	classificationValues := url.Values{}
	if input.Filters.BrowseID != "" {
		classificationValues.Set("browseId", input.Filters.BrowseID)
		classificationValues.Set("browseMode", input.Filters.BrowseMode)
	} else if input.Filters.BrowseMode != "" {
		return errors.New("浏览范围缺少索引")
	}
	for _, condition := range input.Filters.IndexFilters {
		classificationValues.Add("indexFilter", condition.Mode+":"+condition.ID)
	}
	if input.Filters.NeedsOrganizing != nil {
		classificationValues.Set("needsOrganizing", strconv.FormatBool(*input.Filters.NeedsOrganizing))
	}
	classification, err := parseClassificationSearch(classificationValues)
	if err != nil {
		return err
	}
	input.Filters.BrowseID = ""
	input.Filters.BrowseMode = ""
	if classification.Browse != nil {
		input.Filters.BrowseID = classification.Browse.ID
		input.Filters.BrowseMode = classification.Browse.Mode
	}
	input.Filters.IndexFilters = classification.Filters
	return nil
}

func (s *server) readLibrarySavedQuery(w http.ResponseWriter, r *http.Request) (librarySavedQueryInput, bool) {
	var input librarySavedQueryInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || normalizeLibrarySavedQuery(&input) != nil {
		writeError(w, http.StatusBadRequest, "查询名称或筛选条件无效")
		return input, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "查询内容无效")
		return input, false
	}
	classificationFilter := savedQueryClassificationFilter(input.Filters)
	if (classificationFilter.Browse != nil || len(classificationFilter.Filters) != 0) && (s.cloud == nil || s.cloud.runtime == nil) {
		writeError(w, http.StatusServiceUnavailable, "分类索引尚未初始化")
		return input, false
	}
	if classificationFilter.Browse != nil || len(classificationFilter.Filters) != 0 {
		err := validateClassificationSearch(s.db, s.cloud.libraryID(), classificationFilter)
		if err == nil {
			return input, true
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "分类索引不存在")
		} else {
			writeError(w, 500, "无法校验分类索引筛选")
		}
		return input, false
	}
	return input, true
}

func (s *server) listLibrarySavedQueries(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT id,name,filters_json,created_at,updated_at FROM library_saved_queries ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		writeError(w, 500, "无法读取常用查询")
		return
	}
	defer rows.Close()
	items := []librarySavedQuery{}
	for rows.Next() {
		var item librarySavedQuery
		var raw string
		if err := rows.Scan(&item.ID, &item.Name, &raw, &item.CreatedAt, &item.UpdatedAt); err != nil || json.Unmarshal([]byte(raw), &item.Filters) != nil {
			writeError(w, 500, "无法读取常用查询")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "无法读取常用查询")
		return
	}
	writeJSON(w, 200, items)
}

func (s *server) createLibrarySavedQuery(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readLibrarySavedQuery(w, r)
	if !ok {
		return
	}
	raw, _ := json.Marshal(input.Filters)
	stamp := now()
	item := librarySavedQuery{ID: newID(), Name: input.Name, Filters: input.Filters, CreatedAt: stamp, UpdatedAt: stamp}
	if _, err := s.db.Exec(`INSERT INTO library_saved_queries(id,name,filters_json,created_at,updated_at) VALUES(?,?,?,?,?)`, item.ID, item.Name, string(raw), item.CreatedAt, item.UpdatedAt); err != nil {
		writeError(w, http.StatusConflict, "已有同名常用查询")
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) updateLibrarySavedQuery(w http.ResponseWriter, r *http.Request) {
	input, ok := s.readLibrarySavedQuery(w, r)
	if !ok {
		return
	}
	raw, _ := json.Marshal(input.Filters)
	stamp := now()
	result, err := s.db.Exec(`UPDATE library_saved_queries SET name=?,filters_json=?,updated_at=? WHERE id=?`, input.Name, string(raw), stamp, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusConflict, "已有同名常用查询")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "常用查询不存在")
		return
	}
	var created string
	if err := s.db.QueryRow(`SELECT created_at FROM library_saved_queries WHERE id=?`, r.PathValue("id")).Scan(&created); err != nil {
		writeError(w, 500, "无法读取常用查询")
		return
	}
	writeJSON(w, 200, librarySavedQuery{ID: r.PathValue("id"), Name: input.Name, Filters: input.Filters, CreatedAt: created, UpdatedAt: stamp})
}

func (s *server) deleteLibrarySavedQuery(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Exec(`DELETE FROM library_saved_queries WHERE id=?`, r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "无法删除常用查询")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "常用查询不存在")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
