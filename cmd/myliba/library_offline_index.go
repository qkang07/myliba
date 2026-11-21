package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const offlineClassificationVersion = 2

type offlineLibraryFile struct {
	cloudFileView
	Lifecycle  string                            `json:"lifecycle"`
	IngestedAt string                            `json:"ingestedAt"`
	Indexes    []offlineClassificationMembership `json:"indexes"`
}

type offlineClassificationMembership struct {
	IndexID string `json:"indexId"`
	Manual  bool   `json:"manual"`
}

type offlineClassificationNode struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

func offlineClassificationSnapshot(db *sql.DB, libraryID string, protected map[string]bool) (string, []offlineClassificationNode, map[string][]offlineClassificationMembership, error) {
	if db == nil {
		return "", []offlineClassificationNode{}, map[string][]offlineClassificationMembership{}, nil
	}
	tx, err := db.Begin()
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()
	generation, err := classificationSnapshotGeneration(tx, libraryID)
	if err != nil {
		return "", nil, nil, err
	}
	type snapshotNode struct {
		offlineClassificationNode
		origin string
	}
	nodeRows, err := tx.Query(`SELECT index_id,COALESCE(parent_id,''),name,origin FROM classification_nodes WHERE library_id=? AND deleted=0 ORDER BY index_id`, libraryID)
	if err != nil {
		return "", nil, nil, err
	}
	rawNodes := []snapshotNode{}
	for nodeRows.Next() {
		var node snapshotNode
		if err = nodeRows.Scan(&node.ID, &node.ParentID, &node.Name, &node.origin); err != nil {
			nodeRows.Close()
			return "", nil, nil, err
		}
		rawNodes = append(rawNodes, node)
	}
	if err = nodeRows.Err(); err != nil {
		nodeRows.Close()
		return "", nil, nil, err
	}
	nodeRows.Close()
	membershipRows, err := tx.Query(`SELECT file_id,index_id,has_manual FROM classification_memberships WHERE library_id=? ORDER BY file_id,index_id`, libraryID)
	if err != nil {
		return "", nil, nil, err
	}
	memberships := map[string][]offlineClassificationMembership{}
	for membershipRows.Next() {
		var fileID, indexID string
		var manual int
		if err = membershipRows.Scan(&fileID, &indexID, &manual); err != nil {
			membershipRows.Close()
			return "", nil, nil, err
		}
		if !protected[fileID] {
			memberships[fileID] = append(memberships[fileID], offlineClassificationMembership{IndexID: indexID, Manual: manual != 0})
		}
	}
	if err = membershipRows.Err(); err != nil {
		membershipRows.Close()
		return "", nil, nil, err
	}
	membershipRows.Close()
	keep := map[string]bool{}
	for _, node := range rawNodes {
		if node.origin != "automatic" {
			keep[node.ID] = true
		}
	}
	ancestorRows, err := tx.Query(`SELECT a.ancestor_id,a.descendant_id,m.file_id FROM classification_ancestors a LEFT JOIN classification_memberships m ON m.library_id=a.library_id AND m.index_id=a.descendant_id WHERE a.library_id=?`, libraryID)
	if err != nil {
		return "", nil, nil, err
	}
	type ancestorPair struct{ ancestor, descendant string }
	pairs := []ancestorPair{}
	for ancestorRows.Next() {
		var ancestor, descendant string
		var fileID sql.NullString
		if err = ancestorRows.Scan(&ancestor, &descendant, &fileID); err != nil {
			ancestorRows.Close()
			return "", nil, nil, err
		}
		pairs = append(pairs, ancestorPair{ancestor: ancestor, descendant: descendant})
		if fileID.Valid && !protected[fileID.String] {
			keep[ancestor] = true
		}
	}
	if err = ancestorRows.Err(); err != nil {
		ancestorRows.Close()
		return "", nil, nil, err
	}
	ancestorRows.Close()
	for changed := true; changed; {
		changed = false
		for _, pair := range pairs {
			if keep[pair.descendant] && !keep[pair.ancestor] {
				keep[pair.ancestor] = true
				changed = true
			}
		}
	}
	nodes := []offlineClassificationNode{}
	for _, node := range rawNodes {
		if keep[node.ID] {
			nodes = append(nodes, node.offlineClassificationNode)
		}
	}
	if err = tx.Commit(); err != nil {
		return "", nil, nil, err
	}
	return generation, nodes, memberships, nil
}

func encodeOfflineClassificationNodes(nodes []offlineClassificationNode) (string, error) {
	raw, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return "", err
	}
	if _, err = writer.Write(raw); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(compressed.Bytes()), nil
}

// A complete, generation-bound metadata feed for clients that explicitly
// maintain an offline index. Live replica counts are cleared: cached entries
// are never evidence that a source or a cloud block is currently readable.
func (c *cloudClient) offlineLibraryIndex(w http.ResponseWriter, r *http.Request) {
	section := strings.TrimSpace(r.URL.Query().Get("section"))
	if section != "" && section != "classification" {
		writeError(w, 400, "离线索引分区无效")
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "cloud" {
		writeError(w, 400, "离线索引范围无效")
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		max := 200
		if section == "classification" {
			max = 500
		}
		if err != nil || value < 1 || value > max {
			writeError(w, 400, "limit 超出离线索引分页范围")
			return
		}
		limit = value
	}
	files, err := c.libraryFiles(scope == "all")
	if err != nil {
		writeError(w, 500, "无法读取离线索引")
		return
	}
	selections, err := c.currentRevisionSelections()
	if err != nil {
		writeError(w, 500, "无法读取当前版本")
		return
	}
	for at := range files {
		files[at].CurrentRevision = selections[files[at].FileID] == files[at].RevisionID && files[at].RevisionID != ""
	}
	states, err := c.libraryLifecycleStates()
	if err != nil {
		writeError(w, 500, "无法读取文件状态")
		return
	}
	stateKeys := make([]string, 0, len(states))
	for id, state := range states {
		stateKeys = append(stateKeys, id+":"+state)
	}
	sort.Strings(stateKeys)
	protectedFiles, err := c.protectedLibraryFileIDs()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "无法确认文件保护状态")
		return
	}
	classificationGeneration, classificationNodes, classificationMemberships, err := offlineClassificationSnapshot(c.runtime.db, c.libraryID(), protectedFiles)
	if err != nil {
		writeError(w, 500, "无法读取离线分类索引")
		return
	}
	generation := cloudHash([]byte(catalogGeneration(files) + "\x00" + strings.Join(stateKeys, "\x00") + "\x00" + classificationGeneration + "\x00" + scope))[:24]
	current := currentLibraryFiles(files)
	items := make([]offlineLibraryFile, 0, len(current))
	for _, file := range current {
		state := states[file.FileID]
		if state == "deleted" {
			continue
		}
		if state == "" {
			state = "active"
		}
		file.AvailableReplicas = 0
		indexes := append([]offlineClassificationMembership(nil), classificationMemberships[file.FileID]...)
		items = append(items, offlineLibraryFile{cloudFileView: file, Lifecycle: state, IngestedAt: file.CatalogCreatedAt, Indexes: indexes})
	}
	if section == "classification" {
		if expected := strings.TrimSpace(r.URL.Query().Get("generation")); expected == "" || expected != generation {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "离线索引已变化，请重新下载", "code": "cursorExpired"})
			return
		}
		offset := 0
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			value, parseErr := strconv.Atoi(raw)
			if parseErr != nil || value < 0 {
				writeError(w, 400, "分类字典游标无效")
				return
			}
			offset = value
		}
		if offset > len(classificationNodes) {
			offset = len(classificationNodes)
		}
		end := offset + limit
		if end > len(classificationNodes) {
			end = len(classificationNodes)
		}
		payload, encodeErr := encodeOfflineClassificationNodes(classificationNodes[offset:end])
		if encodeErr != nil {
			writeError(w, 500, "无法压缩离线分类字典")
			return
		}
		next := ""
		if end < len(classificationNodes) {
			next = strconv.Itoa(end)
		}
		writeJSON(w, 200, map[string]any{"items": []any{}, "total": len(classificationNodes), "nextCursor": next, "generation": generation, "classificationVersion": offlineClassificationVersion, "classificationEncoding": "gzip+base64+json", "classificationPayload": payload, "libraryId": c.libraryID(), "nodeId": c.runtime.id})
		return
	}
	offset := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		prefix, raw, ok := strings.Cut(cursor, ":")
		value, parseErr := strconv.Atoi(raw)
		if !ok || prefix != generation || parseErr != nil || value < 0 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "离线索引已变化，请重新下载", "code": "cursorExpired"})
			return
		}
		offset = value
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = generation + ":" + strconv.Itoa(end)
	}
	response := map[string]any{"items": items[offset:end], "total": len(items), "nextCursor": next, "generation": generation, "classificationVersion": offlineClassificationVersion, "classificationNodeTotal": len(classificationNodes), "libraryId": c.libraryID(), "nodeId": c.runtime.id, "syncedAt": time.Now().UTC().Format(time.RFC3339Nano)}
	writeJSON(w, 200, response)
}
