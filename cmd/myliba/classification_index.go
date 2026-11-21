package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// The classification index is the authoritative grouping model for every new library.
func migrateClassificationIndexSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS classification_nodes (
	library_id TEXT NOT NULL,
	index_id TEXT NOT NULL,
	parent_id TEXT,
	name TEXT NOT NULL,
	name_key TEXT NOT NULL,
	origin TEXT NOT NULL,
	deleted INTEGER NOT NULL DEFAULT 0,
	name_op_id TEXT NOT NULL DEFAULT '',
	parent_op_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(library_id,index_id),
	FOREIGN KEY(library_id,parent_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS classification_nodes_browse_idx ON classification_nodes(library_id,parent_id,deleted,name_key,index_id);
CREATE TABLE IF NOT EXISTS classification_adds (
	library_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	index_id TEXT NOT NULL,
	add_token TEXT NOT NULL,
	origin TEXT NOT NULL,
	operation_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	PRIMARY KEY(library_id,file_id,index_id,add_token),
	FOREIGN KEY(library_id,index_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS classification_removes (
	library_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	index_id TEXT NOT NULL,
	add_token TEXT NOT NULL,
	operation_id TEXT NOT NULL,
	removed_at TEXT NOT NULL,
	PRIMARY KEY(library_id,file_id,index_id,add_token)
);
CREATE TABLE IF NOT EXISTS classification_memberships (
	library_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	index_id TEXT NOT NULL,
	has_manual INTEGER NOT NULL DEFAULT 0,
	has_automatic INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(library_id,file_id,index_id),
	FOREIGN KEY(library_id,index_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS classification_memberships_index_idx ON classification_memberships(library_id,index_id,file_id);
CREATE TABLE IF NOT EXISTS classification_ancestors (
	library_id TEXT NOT NULL,
	ancestor_id TEXT NOT NULL,
	descendant_id TEXT NOT NULL,
	depth INTEGER NOT NULL CHECK(depth>=0),
	PRIMARY KEY(library_id,ancestor_id,descendant_id),
	FOREIGN KEY(library_id,ancestor_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE,
	FOREIGN KEY(library_id,descendant_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS classification_ancestors_desc_idx ON classification_ancestors(library_id,descendant_id,depth,ancestor_id);
CREATE TABLE IF NOT EXISTS classification_source_dirs (
	library_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	root_id TEXT NOT NULL,
	source_dir_key TEXT NOT NULL,
	index_id TEXT NOT NULL,
	display_segments_json TEXT NOT NULL,
	disabled INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(library_id,node_id,root_id,source_dir_key)
);
CREATE TABLE IF NOT EXISTS classification_imports (
	library_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	root_id TEXT NOT NULL,
	source_entry_id TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('processed','skipped')),
	index_id TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	PRIMARY KEY(library_id,node_id,root_id,source_entry_id)
);
CREATE TABLE IF NOT EXISTS classification_requests (
	library_id TEXT NOT NULL,
	actor TEXT NOT NULL,
	request_id TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(library_id,actor,request_id)
);
`)
	return err
}

type classificationProjectionReport struct {
	LibraryID             string `json:"libraryId"`
	Generation            string `json:"generation"`
	NodeCount             int    `json:"nodeCount"`
	MembershipCount       int    `json:"membershipCount"`
	UnclassifiedFileCount int    `json:"unclassifiedFileCount"`
	BuiltAt               string `json:"builtAt"`
}

func classificationSeed(parts ...string) string {
	h := sha256.New()
	var size [4]byte
	for _, part := range parts {
		binary.BigEndian.PutUint32(size[:], uint32(len([]byte(part))))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func normalizeClassificationName(name string) string {
	return norm.NFC.String(strings.TrimSpace(name))
}

func classificationNameKey(name string) string {
	normalized := normalizeClassificationName(name)
	return norm.NFC.String(cases.Fold().String(normalized))
}

func refreshClassificationProjection(db *sql.DB, libraryID, localNodeID string) (classificationProjectionReport, error) {
	report := classificationProjectionReport{LibraryID: libraryID, BuiltAt: now()}
	if libraryID == "" || localNodeID == "" {
		return report, errors.New("classification projection requires library and node identity")
	}
	tx, err := db.Begin()
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if err = replayClassificationTx(tx, libraryID, localNodeID); err != nil {
		return report, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND deleted=0`, libraryID).Scan(&report.NodeCount); err != nil {
		return report, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=?`, libraryID).Scan(&report.MembershipCount); err != nil {
		return report, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM library_files f
		WHERE NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=f.file_id)
		AND NOT EXISTS (SELECT 1 FROM classification_memberships m WHERE m.library_id=? AND m.file_id=f.file_id)`, libraryID).Scan(&report.UnclassifiedFileCount); err != nil {
		return report, err
	}
	report.Generation, err = classificationSnapshotGeneration(tx, libraryID)
	if err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

type classificationIndexView struct {
	ID          string                    `json:"id"`
	ParentID    *string                   `json:"parentId"`
	Name        string                    `json:"name"`
	Ancestors   []classificationIndexView `json:"ancestors"`
	HasChildren bool                      `json:"hasChildren"`
	Version     string                    `json:"version"`
	Count       int                       `json:"count"`
}

func visibleClassificationNodeIDs(db *sql.DB, libraryID string, protected map[string]bool) (map[string]bool, error) {
	keep := map[string]bool{}
	rows, err := db.Query(`SELECT index_id,origin FROM classification_nodes WHERE library_id=? AND deleted=0`, libraryID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, origin string
		if err = rows.Scan(&id, &origin); err != nil {
			rows.Close()
			return nil, err
		}
		if origin != "automatic" {
			keep[id] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = db.Query(`SELECT a.ancestor_id,a.descendant_id,m.file_id FROM classification_ancestors a LEFT JOIN classification_memberships m ON m.library_id=a.library_id AND m.index_id=a.descendant_id WHERE a.library_id=?`, libraryID)
	if err != nil {
		return nil, err
	}
	type pair struct{ ancestor, descendant string }
	pairs := []pair{}
	for rows.Next() {
		var ancestor, descendant string
		var fileID sql.NullString
		if err = rows.Scan(&ancestor, &descendant, &fileID); err != nil {
			rows.Close()
			return nil, err
		}
		pairs = append(pairs, pair{ancestor, descendant})
		if fileID.Valid && !protected[fileID.String] {
			keep[ancestor] = true
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for changed := true; changed; {
		changed = false
		for _, item := range pairs {
			if keep[item.descendant] && !keep[item.ancestor] {
				keep[item.ancestor] = true
				changed = true
			}
		}
	}
	return keep, nil
}

func (s *server) classificationIdentity() (string, string, error) {
	if s.cloud == nil || s.cloud.runtime == nil {
		return "", "", errors.New("classification cloud identity unavailable")
	}
	return s.cloud.libraryID(), s.cloud.runtime.id, nil
}

func (s *server) listClassificationIndexes(w http.ResponseWriter, r *http.Request) {
	libraryID, nodeID, err := s.classificationIdentity()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "索引尚未初始化")
		return
	}
	report, err := refreshClassificationProjection(s.db, libraryID, nodeID)
	if err != nil {
		writeError(w, 500, "无法刷新索引投影")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 200 {
			writeError(w, 400, "索引分页大小无效")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 0 {
			writeError(w, 400, "索引游标无效")
			return
		}
		offset = parsed
	}
	query := classificationNameKey(r.URL.Query().Get("q"))
	parentID := strings.TrimSpace(r.URL.Query().Get("parentId"))
	protectedFiles, err := s.cloud.protectedLibraryFileIDs()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法确认文件保护状态")
		return
	}
	visibleNodes, err := visibleClassificationNodeIDs(s.db, libraryID, protectedFiles)
	if err != nil {
		writeError(w, 500, "无法计算受保护索引可见性")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	defer tx.Rollback()
	var rows *sql.Rows
	if query != "" {
		rows, err = tx.Query(`SELECT index_id FROM classification_nodes WHERE library_id=? AND deleted=0 AND name_key LIKE '%'||?||'%' ORDER BY name_key,index_id`, libraryID, query)
	} else if parentID == "" {
		rows, err = tx.Query(`SELECT index_id FROM classification_nodes WHERE library_id=? AND parent_id IS NULL AND deleted=0 ORDER BY name_key,index_id`, libraryID)
	} else {
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, libraryID, parentID).Scan(&exists); err != nil || exists == 0 || !visibleNodes[parentID] {
			if err == nil {
				writeClassificationError(w, classificationError(404, "indexNotFound", "索引不存在"))
			} else {
				writeError(w, 500, "无法读取索引")
			}
			return
		}
		rows, err = tx.Query(`SELECT index_id FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0 ORDER BY name_key,index_id`, libraryID, parentID)
	}
	if err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			writeError(w, 500, "无法读取索引")
			return
		}
		if visibleNodes[id] {
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "无法读取索引")
		return
	}
	rows.Close()
	next := ""
	if offset > len(ids) {
		offset = len(ids)
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}
	if end < len(ids) {
		next = strconv.Itoa(offset + limit)
	}
	ids = ids[offset:end]
	items := make([]classificationIndexView, 0, len(ids))
	for _, id := range ids {
		item, itemErr := classificationNodeForVisibleResponse(tx, libraryID, id, protectedFiles)
		if itemErr != nil {
			writeError(w, 500, "无法读取索引")
			return
		}
		items = append(items, item)
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "nextCursor": next, "generation": report.Generation})
}

func (s *server) getClassificationIndex(w http.ResponseWriter, r *http.Request) {
	libraryID, nodeID, err := s.classificationIdentity()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "索引尚未初始化")
		return
	}
	if _, err = refreshClassificationProjection(s.db, libraryID, nodeID); err != nil {
		writeError(w, 500, "无法刷新索引投影")
		return
	}
	protectedFiles, err := s.cloud.protectedLibraryFileIDs()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法确认文件保护状态")
		return
	}
	visibleNodes, err := visibleClassificationNodeIDs(s.db, libraryID, protectedFiles)
	if err != nil {
		writeError(w, 500, "无法计算受保护索引可见性")
		return
	}
	requestedID := strings.TrimSpace(r.PathValue("id"))
	if !visibleNodes[requestedID] {
		writeClassificationError(w, classificationError(http.StatusNotFound, "indexNotFound", "索引不存在"))
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	defer tx.Rollback()
	item, err := classificationNodeForVisibleResponse(tx, libraryID, requestedID, protectedFiles)
	if errors.Is(err, sql.ErrNoRows) {
		writeClassificationError(w, classificationError(http.StatusNotFound, "indexNotFound", "索引不存在"))
		return
	}
	if err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "无法读取索引")
		return
	}
	writeJSON(w, 200, item)
}
