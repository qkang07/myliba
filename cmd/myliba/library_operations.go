package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const localOperationAuthor = "desktop"

type libraryOperationEnvelope struct {
	OpID     string          `json:"opId"`
	AuthorID string          `json:"authorId"`
	Counter  int64           `json:"counter"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

type libraryReceiptEnvelope struct {
	NodeID    string           `json:"nodeId"`
	Frontiers map[string]int64 `json:"frontiers"`
}

func (c *cloudClient) publishLibraryReceipt() error {
	if c == nil || c.runtime == nil || c.runtime.db == nil {
		return nil
	}
	rows, err := c.runtime.db.Query(`SELECT author_id,last_counter FROM library_operation_frontiers`)
	if err != nil {
		return err
	}
	frontiers := map[string]int64{}
	for rows.Next() {
		var author string
		var counter int64
		if err = rows.Scan(&author, &counter); err != nil {
			rows.Close()
			return err
		}
		frontiers[author] = counter
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(frontiers) == 0 {
		return nil
	}
	receipt := libraryReceiptEnvelope{NodeID: c.runtime.id, Frontiers: frontiers}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	digest := cloudHash(payload)
	var previous string
	_ = c.runtime.db.QueryRow(`SELECT digest FROM library_receipt_publication WHERE id=1`).Scan(&previous)
	if previous == digest {
		return nil
	}
	record := cloudRecord{Format: 4, LibraryID: c.libraryID(), ObjectID: "library-receipt/" + c.runtime.id, RevisionID: newID(), AuthorID: c.runtime.id, AuthorEpoch: c.authorEpoch(), CreatedAt: now(), BlockIDs: []string{}}
	record, err = c.sealManifest(record, cloudManifest{Receipt: &receipt})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > cloudMaxRecordSize {
		return errors.New("library receipt is too large")
	}
	if err = c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		return err
	}
	_, err = c.runtime.db.Exec(`INSERT INTO library_receipt_publication(id,digest) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET digest=excluded.digest`, digest)
	return err
}

// recordLibraryOperation is deliberately called inside the same transaction as
// the compatibility mutation. P3 transport can retry this immutable outbox
// without ever claiming the remote side applied it.
func recordLibraryOperation(tx *sql.Tx, kind string, payload map[string]any, captured ...*libraryOperationEnvelope) error {
	author, err := operationAuthor(tx)
	if err != nil {
		return err
	}
	var counter int64
	err = tx.QueryRow(`SELECT next_counter FROM library_operation_authors WHERE author_id=?`, author).Scan(&counter)
	if err == sql.ErrNoRows {
		counter = 1
		if _, err = tx.Exec(`INSERT INTO library_operation_authors(author_id,next_counter) VALUES(?,?)`, author, 2); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if _, err = tx.Exec(`UPDATE library_operation_authors SET next_counter=? WHERE author_id=?`, counter+1, author); err != nil {
		return err
	}
	payload["schemaVersion"] = 1
	payload["author"] = author
	payload["counter"] = counter
	context, err := operationContext(tx)
	if err != nil {
		return err
	}
	payload["context"] = context
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	opID := newID()
	if _, err = tx.Exec(`INSERT INTO library_operations(op_id,author_id,counter,kind,payload_json,created_at,state) VALUES(?,?,?,?,?,?, 'pending')`, opID, author, counter, kind, string(raw), now()); err != nil {
		return err
	}
	if len(captured) > 0 && captured[0] != nil {
		*captured[0] = libraryOperationEnvelope{OpID: opID, AuthorID: author, Counter: counter, Kind: kind, Payload: raw}
	}
	if kind == "file.trash" || kind == "file.restore" || kind == "file.delete" {
		return materializeFileLifecycle(tx, libraryOperationEnvelope{OpID: opID, AuthorID: author, Counter: counter, Kind: kind}, payload["fileIds"], context)
	}
	if name, ok := payload["folderName"].(string); ok && strings.TrimSpace(name) != "" {
		switch kind {
		case "folder.delete":
			_, err = tx.Exec(`INSERT INTO library_folder_tombstones(name,author_id,counter,deleted_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,deleted_at=excluded.deleted_at`, name, author, counter, now())
		case "folder.create":
			_, err = tx.Exec(`DELETE FROM library_folder_tombstones WHERE name=? COLLATE NOCASE`, name)
		}
		if err != nil {
			return err
		}
	}
	if fileIDs, ok := payload["fileIds"].([]string); ok {
		for _, fileID := range uniqueOperationFileIDs(fileIDs) {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) SELECT file_id FROM library_files WHERE file_id=?`, fileID); err != nil {
				return err
			}
		}
	}
	if kind == "folder.rename" {
		if previous, ok := payload["previousName"].(string); ok {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) SELECT lf.file_id FROM library_file_folders lf JOIN folders f ON f.id=lf.folder_id WHERE f.name=? COLLATE NOCASE`, previous); err != nil {
				return err
			}
		}
	}
	if kind == "folder.move" {
		return recordLocalFolderMoveState(tx, payload, author, counter)
	}
	if kind == "folder.rename" {
		return recordLocalFolderRenameState(tx, payload, author, counter)
	}
	return nil
}

func (s *server) setLibraryFileLifecycle(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		State string `json:"state"`
	}
	if fileID == "" || len(fileID) > 160 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || (input.State != "active" && input.State != "trashed" && input.State != "deleted") {
		writeError(w, http.StatusBadRequest, "文件回收状态无效")
		return
	}
	files, err := s.cloud.libraryFiles(true)
	if err != nil {
		writeError(w, 500, "无法读取统一资源库")
		return
	}
	found := false
	for _, file := range files {
		if file.FileID == fileID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	var deleted int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?`, fileID).Scan(&deleted); err != nil {
		writeError(w, 500, "无法读取文件回收状态")
		return
	}
	if deleted != 0 {
		writeError(w, http.StatusConflict, "文件已永久删除，不能恢复或再次修改")
		return
	}
	if input.State == "deleted" {
		var state string
		err := s.db.QueryRow(`SELECT state FROM library_file_lifecycle WHERE file_id=?`, fileID).Scan(&state)
		if err != nil && err != sql.ErrNoRows {
			writeError(w, 500, "无法读取文件回收状态")
			return
		}
		if state != "trashed" {
			writeError(w, http.StatusConflict, "请先将文件移入回收站")
			return
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法更新文件回收状态")
		return
	}
	defer tx.Rollback()
	if err := ensureOperationFile(tx, fileID); err != nil {
		writeError(w, 500, "无法更新文件回收状态")
		return
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?`, fileID).Scan(&deleted); err != nil {
		writeError(w, 500, "无法读取文件回收状态")
		return
	}
	if deleted != 0 {
		writeError(w, http.StatusConflict, "文件已永久删除")
		return
	}
	if input.State == "deleted" {
		var current string
		if err := tx.QueryRow(`SELECT state FROM library_file_lifecycle WHERE file_id=?`, fileID).Scan(&current); err != nil || current != "trashed" {
			writeError(w, http.StatusConflict, "请先将文件移入回收站")
			return
		}
	}
	kind := "file.restore"
	if input.State == "trashed" {
		kind = "file.trash"
	} else if input.State == "deleted" {
		kind = "file.delete"
	}
	if err := recordLibraryOperation(tx, kind, map[string]any{"fileIds": []string{fileID}}); err != nil {
		writeError(w, 500, "无法更新文件回收状态")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "无法更新文件回收状态")
		return
	}
	writeJSON(w, 200, map[string]any{"fileId": fileID, "state": input.State})
}

func operationAuthor(tx *sql.Tx) (string, error) {
	var author string
	err := tx.QueryRow(`SELECT value FROM library_operation_identity WHERE key='author'`).Scan(&author)
	if err == nil {
		return author, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	author = localOperationAuthor + "-" + newID()
	_, err = tx.Exec(`INSERT INTO library_operation_identity(key,value) VALUES('author',?)`, author)
	return author, err
}

// operationContext records the frontiers that the author has already observed.
// It lets a receiving node tell a later move from a genuinely concurrent one.
func operationContext(tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.Query(`SELECT author_id,last_counter FROM library_operation_frontiers`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	context := map[string]int64{}
	for rows.Next() {
		var author string
		var counter int64
		if err := rows.Scan(&author, &counter); err != nil {
			return nil, err
		}
		context[author] = counter
	}
	return context, rows.Err()
}

// The compatibility handler has already applied the folder change locally.
// Persisting its writer state now prevents an incoming concurrent operation from
// silently replacing it before our own encrypted operation is read back.
func recordLocalFolderMoveState(tx *sql.Tx, payload map[string]any, author string, counter int64) error {
	fileIDs, ok := payload["fileIds"].([]string)
	if !ok {
		return nil
	}
	folderID, _ := payload["folderId"].(string)
	for _, fileID := range uniqueOperationFileIDs(fileIDs) {
		if fileID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO library_file_folder_state(file_id,author_id,counter,folder_id,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,folder_id=excluded.folder_id,updated_at=excluded.updated_at`, fileID, author, counter, folderID, now()); err != nil {
			return err
		}
	}
	return nil
}

// The compatibility rename handler has already renamed the folder locally.
// Recording the writer state keyed by the pre-rename anchor lets a receiving
// node tell a later rename of the same folder from a genuinely concurrent one,
// instead of silently dropping the second rename (§5.2 row "文件夹或文件并发改名").
func recordLocalFolderRenameState(tx *sql.Tx, payload map[string]any, author string, counter int64) error {
	previous, _ := payload["previousName"].(string)
	name, _ := payload["folderName"].(string)
	previous, name = strings.TrimSpace(previous), strings.TrimSpace(name)
	if previous == "" || name == "" {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO library_folder_rename_state(anchor,author_id,counter,current_name,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(anchor) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,current_name=excluded.current_name,updated_at=excluded.updated_at`, previous, author, counter, name, now())
	return err
}

func logicalFileIDs(tx *sql.Tx, resourceIDs []string) ([]string, error) {
	ids := make([]string, 0, len(resourceIDs))
	seen := map[string]bool{}
	for _, resourceID := range resourceIDs {
		var fileID string
		if err := tx.QueryRow(`SELECT file_id FROM library_resource_aliases WHERE resource_id=?`, resourceID).Scan(&fileID); err != nil {
			return nil, err
		}
		if !seen[fileID] {
			seen[fileID] = true
			ids = append(ids, fileID)
		}
	}
	return ids, nil
}

func (s *server) librarySyncStatus(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT state,COUNT(*) FROM library_operations GROUP BY state`)
	if err != nil {
		writeError(w, 500, "无法读取同步状态")
		return
	}
	counts := map[string]int{"pending": 0, "sent": 0, "acknowledged": 0, "failed": 0}
	for rows.Next() {
		var state string
		var count int
		if rows.Scan(&state, &count) == nil {
			counts[state] = count
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, 500, "无法读取同步状态")
		return
	}
	rows.Close()
	var author string
	_ = s.db.QueryRow(`SELECT value FROM library_operation_identity WHERE key='author'`).Scan(&author)
	var conflicts int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM library_folder_conflicts`).Scan(&conflicts)
	var renameConflicts int
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT anchor) FROM library_folder_rename_conflicts`).Scan(&renameConflicts)
	var fileNameConflicts int
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT file_id) FROM library_file_name_conflicts`).Scan(&fileNameConflicts)
	var revisionConflicts int
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT file_id) FROM library_file_revision_conflicts`).Scan(&revisionConflicts)
	// Applied frontiers report what THIS query node has materialized per author
	// (§5.1). They never imply another node applied the same operations, so the
	// UI must not render "所有设备已同步" from them.
	frontiers := map[string]int64{}
	if rows, err := s.db.Query(`SELECT author_id,last_counter FROM library_operation_frontiers`); err == nil {
		for rows.Next() {
			var authorID string
			var counter int64
			if rows.Scan(&authorID, &counter) == nil {
				frontiers[authorID] = counter
			}
		}
		rows.Close()
	}
	// Failed records carry the transport error so the UI can show why an
	// operation is stuck instead of only counting it.
	failures := []map[string]any{}
	if rows, err := s.db.Query(`SELECT op_id,author_id,counter,kind,error,created_at FROM library_operations WHERE state='failed' ORDER BY created_at,author_id,counter LIMIT 50`); err == nil {
		for rows.Next() {
			var opID, authorID, kind, opError, created string
			var counter int64
			if rows.Scan(&opID, &authorID, &counter, &kind, &opError, &created) == nil {
				failures = append(failures, map[string]any{"opId": opID, "author": authorID, "counter": counter, "kind": kind, "error": opError, "createdAt": created})
			}
		}
		rows.Close()
	}
	if rows, err := s.db.Query(`SELECT op_id,author_id,counter,kind,reason,seen_at FROM library_operation_quarantine ORDER BY seen_at,author_id,counter LIMIT 50`); err == nil {
		for rows.Next() {
			var opID, authorID, kind, reason, seen string
			var counter int64
			if rows.Scan(&opID, &authorID, &counter, &kind, &reason, &seen) == nil {
				failures = append(failures, map[string]any{"opId": opID, "author": authorID, "counter": counter, "kind": kind, "error": reason, "createdAt": seen})
			}
		}
		rows.Close()
	}
	receipts := map[string]map[string]int64{}
	if rows, err := s.db.Query(`SELECT node_id,frontiers_json FROM library_receipts`); err == nil {
		for rows.Next() {
			var node, raw string
			if rows.Scan(&node, &raw) == nil {
				var applied map[string]int64
				if json.Unmarshal([]byte(raw), &applied) == nil {
					receipts[node] = applied
				}
			}
		}
		rows.Close()
	}
	legacyImports := map[string]map[string]any{}
	if rows, err := s.db.Query(`SELECT author_id,last_catalog_at,imported_count FROM library_legacy_import_frontiers`); err == nil {
		for rows.Next() {
			var node, last string
			var count int
			if rows.Scan(&node, &last, &count) == nil {
				legacyImports[node] = map[string]any{"lastCatalogAt": last, "count": count}
			}
		}
		rows.Close()
	}
	writeJSON(w, 200, map[string]any{"author": author, "outbox": counts, "folderConflicts": conflicts, "renameConflicts": renameConflicts, "fileNameConflicts": fileNameConflicts, "revisionConflicts": revisionConflicts, "frontiers": frontiers, "receipts": receipts, "legacyImports": legacyImports, "failures": failures})
}

func (s *server) libraryFolderConflicts(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT file_id,author_id,counter,op_id,folder_name,created_at FROM library_folder_conflicts ORDER BY created_at,file_id`)
	if err != nil {
		writeError(w, 500, "无法读取归组冲突")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var fileID, author, opID, name, created string
		var counter int64
		if err := rows.Scan(&fileID, &author, &counter, &opID, &name, &created); err != nil {
			writeError(w, 500, "无法读取归组冲突")
			return
		}
		items = append(items, map[string]any{"fileId": fileID, "author": author, "counter": counter, "opId": opID, "folderName": name, "createdAt": created})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// libraryResolveFolderConflict is deliberately an explicit user action. The
// selected folder becomes a new operation; it does not silently pick the last
// received candidate.
func (s *server) libraryResolveFolderConflict(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		FolderName string `json:"folderName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input); err != nil {
		writeError(w, 400, "归组选择无效")
		return
	}
	name := strings.TrimSpace(input.FolderName)
	if fileID == "" || len(fileID) > 160 || len([]rune(name)) > 80 {
		writeError(w, 400, "归组选择无效")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法处理归组冲突")
		return
	}
	defer tx.Rollback()
	if err = ensureOperationFile(tx, fileID); err != nil {
		writeError(w, 500, "无法处理归组冲突")
		return
	}
	folderID := ""
	if name != "" {
		var deleted int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM library_folder_tombstones WHERE name=?`, name).Scan(&deleted); err != nil {
			writeError(w, 500, "无法处理归组冲突")
			return
		}
		if _, err = tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, newID(), name, now()); err != nil {
			writeFolderError(w, err)
			return
		}
		if deleted != 0 {
			if err = recordLibraryOperation(tx, "folder.create", map[string]any{"folderName": name}); err != nil {
				writeError(w, 500, "无法处理归组冲突")
				return
			}
		}
		if err = tx.QueryRow(`SELECT id FROM folders WHERE name=?`, name).Scan(&folderID); err != nil {
			writeError(w, 500, "无法处理归组冲突")
			return
		}
		_, err = tx.Exec(`INSERT INTO library_file_folders(file_id,folder_id,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET folder_id=excluded.folder_id,updated_at=excluded.updated_at`, fileID, folderID, now())
		if err == nil {
			_, err = tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) SELECT resource_id,? FROM library_resource_aliases WHERE file_id=? ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, folderID, fileID)
		}
	} else {
		_, err = tx.Exec(`DELETE FROM library_file_folders WHERE file_id=?`, fileID)
		if err == nil {
			_, err = tx.Exec(`DELETE FROM resource_folders WHERE resource_id IN (SELECT resource_id FROM library_resource_aliases WHERE file_id=?)`, fileID)
		}
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM library_folder_conflicts WHERE file_id=?`, fileID)
	}
	if err == nil {
		err = recordLibraryOperation(tx, "folder.move", map[string]any{"fileIds": []string{fileID}, "folderId": "resolved", "folderName": name})
	}
	if err != nil {
		writeError(w, 500, "无法处理归组冲突")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "无法处理归组冲突")
		return
	}
	if s.cloud != nil {
		s.cloud.queueLocalIndexByFileID(fileID)
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// libraryFolderRenameConflicts lists concurrent-rename candidates grouped by the
// pre-rename anchor. The applied winner is reported alongside the retained
// losers so the UI can offer a repeatable tentative order (§5.2, §6.2).
func (s *server) libraryFolderRenameConflicts(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT c.anchor,c.author_id,c.counter,c.op_id,c.folder_name,c.created_at,COALESCE(st.current_name,'') FROM library_folder_rename_conflicts c LEFT JOIN library_folder_rename_state st ON st.anchor=c.anchor ORDER BY c.anchor,c.created_at,c.author_id,c.counter`)
	if err != nil {
		writeError(w, 500, "无法读取改名冲突")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var anchor, author, opID, name, created, applied string
		var counter int64
		if err := rows.Scan(&anchor, &author, &counter, &opID, &name, &created, &applied); err != nil {
			writeError(w, 500, "无法读取改名冲突")
			return
		}
		items = append(items, map[string]any{"anchor": anchor, "author": author, "counter": counter, "opId": opID, "folderName": name, "appliedName": applied, "createdAt": created})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// libraryResolveFolderRenameConflict is an explicit user action: the chosen name
// becomes a new rename operation from the currently applied name, so every node
// converges instead of keeping divergent candidates.
func (s *server) libraryResolveFolderRenameConflict(w http.ResponseWriter, r *http.Request) {
	anchor := strings.TrimSpace(r.PathValue("anchor"))
	var input struct {
		FolderName string `json:"folderName"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input); err != nil {
		writeError(w, 400, "改名选择无效")
		return
	}
	name := strings.TrimSpace(input.FolderName)
	if anchor == "" || len([]rune(anchor)) > 80 || name == "" || len([]rune(name)) > 80 {
		writeError(w, 400, "改名选择无效")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法处理改名冲突")
		return
	}
	defer tx.Rollback()
	current := anchor
	_ = tx.QueryRow(`SELECT current_name FROM library_folder_rename_state WHERE anchor=?`, anchor).Scan(&current)
	if current == "" {
		current = anchor
	}
	if _, err = tx.Exec(`UPDATE folders SET name=? WHERE name=? COLLATE NOCASE`, name, current); err != nil {
		writeFolderError(w, err)
		return
	}
	if _, err = tx.Exec(`DELETE FROM library_folder_rename_conflicts WHERE anchor=?`, anchor); err != nil {
		writeError(w, 500, "无法处理改名冲突")
		return
	}
	if _, err = tx.Exec(`DELETE FROM library_folder_rename_state WHERE anchor=?`, anchor); err != nil {
		writeError(w, 500, "无法处理改名冲突")
		return
	}
	if err = recordLibraryOperation(tx, "folder.rename", map[string]any{"folderName": name, "previousName": current}); err != nil {
		writeError(w, 500, "无法处理改名冲突")
		return
	}
	if err = tx.Commit(); err != nil {
		writeError(w, 500, "无法处理改名冲突")
		return
	}
	s.queueAllLocalIndexes()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// publishPendingOperations writes operation records into the same encrypted
// catalog transport as file indexes, but their manifest type keeps them out of
// content listing and replication accounting. A later P3 receiver consumes
// these records into library_operation_inbox.
func (c *cloudClient) publishPendingOperations() error {
	if c == nil || c.runtime == nil || c.runtime.db == nil {
		return nil
	}
	rows, err := c.runtime.db.Query(`SELECT op_id,author_id,counter,kind,payload_json FROM library_operations WHERE state IN ('pending','failed') ORDER BY author_id,counter LIMIT 100`)
	if err != nil {
		return err
	}
	pending := []libraryOperationEnvelope{}
	for rows.Next() {
		var op libraryOperationEnvelope
		var payload string
		if err := rows.Scan(&op.OpID, &op.AuthorID, &op.Counter, &op.Kind, &payload); err != nil {
			rows.Close()
			return err
		}
		op.Payload = json.RawMessage(payload)
		pending = append(pending, op)
	}
	if err := rows.Err(); err != nil { rows.Close(); return err }
	rows.Close()
	for _, op := range pending {
		record := cloudRecord{Format: 4, LibraryID: c.libraryID(), ObjectID: "library-operation/" + op.AuthorID + "/" + fmt.Sprint(op.Counter), RevisionID: op.OpID, AuthorID: c.runtime.id, AuthorEpoch: c.authorEpoch(), CreatedAt: now(), BlockIDs: []string{}}
		record, err = c.sealManifest(record, cloudManifest{Operation: &op})
		if err != nil {
			return err
		}
		raw, err := json.Marshal(record)
		if err != nil || len(raw) > cloudMaxRecordSize {
			if err == nil {
				err = errors.New("library operation is too large")
			}
			return err
		}
		if err = c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			return err
		}
		if _, err = c.runtime.db.Exec(`UPDATE library_operations SET state='sent',error='' WHERE op_id=?`, op.OpID); err != nil {
			return err
		}
	}
	return nil
}

// applyRemoteOperations materializes the small P3 operation subset supported
// today.  Inbox insertion happens in the same transaction as the projection,
// so a crash or duplicate catalog delivery cannot apply a mutation twice.
func (c *cloudClient) applyRemoteOperations() error {
	if c == nil || c.runtime == nil || c.runtime.db == nil {
		return nil
	}
	records, err := c.validRecords()
	if err != nil {
		return err
	}
	type receivedOperation struct {
		record    cloudRecord
		operation libraryOperationEnvelope
	}
	received := make([]receivedOperation, 0)
	legacy := make([]legacySnapshot, 0)
	receipts := map[string]map[string]int64{}
	receiptTimes := map[string]string{}
	for _, record := range records {
		manifest, err := c.openManifest(record)
		if err != nil {
			continue
		}
		if record.Format == 3 && manifest.Operation == nil && manifest.Receipt == nil && manifest.Snapshot == nil {
			if snapshot, ok := legacySnapshotFromRecord(record, manifest); ok {
				legacy = append(legacy, snapshot)
			}
		}
		if manifest.Receipt != nil {
			if manifest.Receipt.NodeID == record.AuthorID && len(manifest.Receipt.Frontiers) > 0 {
				if receipts[record.AuthorID] == nil {
					receipts[record.AuthorID] = map[string]int64{}
				}
				for author, counter := range manifest.Receipt.Frontiers {
					if author != "" && counter > receipts[record.AuthorID][author] {
						receipts[record.AuthorID][author] = counter
					}
				}
				if receiptTimes[record.AuthorID] == "" || catalogAfter(record, cloudRecord{CreatedAt: receiptTimes[record.AuthorID]}) {
					receiptTimes[record.AuthorID] = record.CreatedAt
				}
			}
			continue
		}
		if manifest.Snapshot != nil {
			operations, snapshotErr := decodeLibrarySnapshot(*manifest.Snapshot)
			if snapshotErr != nil {
				_, _ = c.runtime.db.Exec(`INSERT INTO library_operation_quarantine(op_id,author_id,counter,kind,reason,seen_at) VALUES(?,?,?,?,?,?) ON CONFLICT(op_id) DO NOTHING`, record.RevisionID, manifest.Snapshot.AuthorID, manifest.Snapshot.Frontier, "snapshot", snapshotErr.Error(), now())
				continue
			}
			for _, op := range operations { received = append(received, receivedOperation{record: record, operation: op}) }
			continue
		}
		if manifest.Operation == nil {
			continue
		}
		op := manifest.Operation
		if op.OpID == "" || op.AuthorID == "" || op.Counter < 1 || op.OpID != record.RevisionID || (record.ObjectID != "library-operation/"+op.AuthorID+"/"+fmt.Sprint(op.Counter) && record.ObjectID != "operation-"+op.OpID) {
			continue
		}
		if !knownLibraryOperationKind(op.Kind) {
			_, _ = c.runtime.db.Exec(`INSERT INTO library_operation_quarantine(op_id,author_id,counter,kind,reason,seen_at) VALUES(?,?,?,?,?,?) ON CONFLICT(op_id) DO NOTHING`, op.OpID, op.AuthorID, op.Counter, op.Kind, "不支持的整理操作，请升级客户端", now())
			continue
		}
		_, _ = c.runtime.db.Exec(`DELETE FROM library_operation_quarantine WHERE op_id=?`, op.OpID)
		received = append(received, receivedOperation{record: record, operation: *manifest.Operation})
	}
	if err := importLegacySnapshots(c.runtime.db, legacy); err != nil {
		return err
	}
	for node, frontiers := range receipts {
		var priorRaw, priorAt string
		if err := c.runtime.db.QueryRow(`SELECT frontiers_json,catalog_at FROM library_receipts WHERE node_id=?`, node).Scan(&priorRaw, &priorAt); err != nil && err != sql.ErrNoRows {
			return err
		}
		var prior map[string]int64
		if json.Unmarshal([]byte(priorRaw), &prior) == nil {
			for author, counter := range prior {
				if counter > frontiers[author] {
					frontiers[author] = counter
				}
			}
		}
		stamp := receiptTimes[node]
		if priorAt != "" && (stamp == "" || catalogAfter(cloudRecord{CreatedAt: priorAt}, cloudRecord{CreatedAt: stamp})) {
			stamp = priorAt
		}
		raw, err := json.Marshal(frontiers)
		if err != nil {
			return err
		}
		if _, err = c.runtime.db.Exec(`INSERT INTO library_receipts(node_id,frontiers_json,catalog_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET frontiers_json=excluded.frontiers_json,catalog_at=excluded.catalog_at`, node, string(raw), stamp); err != nil {
			return err
		}
		if _, err = c.runtime.db.Exec(`INSERT INTO library_members(node_id,epoch,state,author_id,counter,updated_at) VALUES(?,1,'active','',0,?) ON CONFLICT(node_id) DO NOTHING`, node, now()); err != nil { return err }
	}
	sort.Slice(received, func(i, j int) bool {
		if received[i].operation.AuthorID == received[j].operation.AuthorID {
			return received[i].operation.Counter < received[j].operation.Counter
		}
		return received[i].operation.AuthorID < received[j].operation.AuthorID
	})
	for _, incoming := range received {
		op := incoming.operation
		tx, err := c.runtime.db.Begin()
		if err != nil {
			return err
		}
		var frontier int64
		err = tx.QueryRow(`SELECT last_counter FROM library_operation_frontiers WHERE author_id=?`, op.AuthorID).Scan(&frontier)
		if err == sql.ErrNoRows {
			err = nil
			frontier = 0
		}
		collision := false
		if err == nil && op.Counter <= frontier {
			var accepted string
			claimErr := tx.QueryRow(`SELECT op_id FROM library_operation_inbox WHERE author_id=? AND counter=? LIMIT 1`, op.AuthorID, op.Counter).Scan(&accepted)
			if claimErr == sql.ErrNoRows {
				claimErr = tx.QueryRow(`SELECT op_id FROM library_operations WHERE author_id=? AND counter=? LIMIT 1`, op.AuthorID, op.Counter).Scan(&accepted)
			}
			if claimErr != nil && claimErr != sql.ErrNoRows {
				err = claimErr
			}
			if err == nil && accepted != "" && accepted != op.OpID {
				collision = true
			}
		}
		if collision {
			tx.Rollback()
			if _, err := c.runtime.db.Exec(`INSERT INTO library_operation_quarantine(op_id,author_id,counter,kind,reason,seen_at) VALUES(?,?,?,?,?,?) ON CONFLICT(op_id) DO NOTHING`, op.OpID, op.AuthorID, op.Counter, op.Kind, "作者序号对应不同操作", now()); err != nil {
				return err
			}
			continue
		}
		if err == nil && op.Counter > frontier+1 {
			tx.Rollback() // Causal gap: retain the encrypted record and retry later.
			continue
		}
		if err == nil && op.Counter == frontier+1 {
			err = materializeLibraryOperation(tx, op)
			if err == nil {
				_, err = tx.Exec(`INSERT INTO library_operation_inbox(op_id,author_id,counter,received_at,applied_at) VALUES(?,?,?,?,?)`, op.OpID, op.AuthorID, op.Counter, now(), now())
			}
			if err == nil {
				_, err = tx.Exec(`INSERT INTO library_operation_frontiers(author_id,last_counter,updated_at) VALUES(?,?,?) ON CONFLICT(author_id) DO UPDATE SET last_counter=excluded.last_counter,updated_at=excluded.updated_at`, op.AuthorID, op.Counter, now())
			}
		}
		if err == nil {
			var localAuthor string
			_ = tx.QueryRow(`SELECT value FROM library_operation_identity WHERE key='author'`).Scan(&localAuthor)
			if localAuthor == op.AuthorID {
				_, err = tx.Exec(`UPDATE library_operations SET state='acknowledged' WHERE op_id=?`, op.OpID)
			}
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type legacySnapshot struct {
	hash, author, objectID, fileID, revisionID, folderID, folderName, tagsJSON, catalogAt string
	known                                                                       bool
}

func legacySnapshotFromRecord(record cloudRecord, manifest cloudManifest) (legacySnapshot, bool) {
	if record.AuthorID == "" || record.ObjectID == "" || record.RevisionID == "" {
		return legacySnapshot{}, false
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return legacySnapshot{}, false
	}
	item := legacySnapshot{hash: cloudHash(raw), author: record.AuthorID, objectID: record.ObjectID, fileID: record.ObjectID, revisionID: record.RevisionID, catalogAt: record.CreatedAt}
	var tags []string
	if manifest.IndexOnly {
		if manifest.LocalIndex == nil {
			return legacySnapshot{}, false
		}
		if manifest.LocalIndex.FileID != "" {
			item.fileID = manifest.LocalIndex.FileID
		}
		// Older device indexes can contain classification without the explicit
		// known flag. Nonempty values are evidence; absent values remain unknown.
		item.known = manifest.LocalIndex.OrganizationKnown || manifest.LocalIndex.FolderID != "" || manifest.LocalIndex.FolderName != "" || len(manifest.LocalIndex.Tags) > 0
		if item.known {
			item.folderID = legacyFolderIdentity(record.AuthorID, manifest.LocalIndex.FolderID, manifest.LocalIndex.FolderName)
			item.folderName = manifest.LocalIndex.FolderName
			tags = manifest.LocalIndex.Tags
		}
	} else {
		if manifest.FileID != "" {
			item.fileID = manifest.FileID
		}
		// Format 3 content records did not distinguish omitted classification
		// from an explicit empty set. Keep the snapshot but mark empties unknown.
		item.known = manifest.FolderID != "" || manifest.FolderName != "" || len(manifest.Tags) > 0
		if item.known {
			item.folderID = legacyFolderIdentity(record.AuthorID, manifest.FolderID, manifest.FolderName)
			item.folderName = manifest.FolderName
			tags = manifest.Tags
		}
	}
	if tags == nil {
		tags = []string{}
	}
	tagBytes, err := json.Marshal(tags)
	if err != nil {
		return legacySnapshot{}, false
	}
	item.tagsJSON = string(tagBytes)
	return item, true
}

// Old names have no global identity. Names from different authors remain
// distinct until a user explicitly moves their files into one folder.
func legacyFolderIdentity(author, sourceID, name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	key := sourceID
	if key == "" {
		key = "name:" + strings.ToLower(strings.TrimSpace(name))
	} else {
		key = "id:" + key
	}
	return "legacy:" + cloudHash([]byte(author+"\x00"+key))[:24]
}

func importLegacySnapshots(db *sql.DB, snapshots []legacySnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range snapshots {
		if item.folderID != "" {
			if _, err := tx.Exec(`INSERT INTO library_legacy_folder_identities(record_hash,folder_id) VALUES(?,?) ON CONFLICT(record_hash) DO UPDATE SET folder_id=excluded.folder_id`, item.hash, item.folderID); err != nil {
				return err
			}
		}
		known := 0
		if item.known {
			known = 1
		}
		result, err := tx.Exec(`INSERT OR IGNORE INTO library_legacy_snapshots(record_hash,author_id,object_id,file_id,revision_id,classification_known,folder_name,tags_json,catalog_at,imported_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, item.hash, item.author, item.objectID, item.fileID, item.revisionID, known, item.folderName, item.tagsJSON, item.catalogAt, now())
		if err != nil {
			return err
		}
		added, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if added == 0 {
			continue
		}
		var previousAt, previousHash string
		err = tx.QueryRow(`SELECT last_catalog_at,last_record_hash FROM library_legacy_import_frontiers WHERE author_id=?`, item.author).Scan(&previousAt, &previousHash)
		if err == sql.ErrNoRows {
			_, err = tx.Exec(`INSERT INTO library_legacy_import_frontiers(author_id,last_catalog_at,last_record_hash,imported_count) VALUES(?,?,?,1)`, item.author, item.catalogAt, item.hash)
		} else if err == nil {
			if previousAt == "" || catalogAfter(cloudRecord{CreatedAt: item.catalogAt, RevisionID: item.hash}, cloudRecord{CreatedAt: previousAt, RevisionID: previousHash}) {
				previousAt, previousHash = item.catalogAt, item.hash
			}
			_, err = tx.Exec(`UPDATE library_legacy_import_frontiers SET last_catalog_at=?,last_record_hash=?,imported_count=imported_count+1 WHERE author_id=?`, previousAt, previousHash, item.author)
		}
		if err != nil {
			return err
		}
		if item.known && item.fileID != "" && len(item.fileID) <= 160 {
			var baselineAt, baselineHash string
			baselineErr := tx.QueryRow(`SELECT catalog_at,record_hash FROM library_legacy_baselines WHERE file_id=?`, item.fileID).Scan(&baselineAt, &baselineHash)
			if baselineErr != nil && baselineErr != sql.ErrNoRows {
				return baselineErr
			}
			if baselineErr == sql.ErrNoRows || catalogAfter(cloudRecord{CreatedAt: item.catalogAt, RevisionID: item.hash}, cloudRecord{CreatedAt: baselineAt, RevisionID: baselineHash}) {
				if err := ensureOperationFile(tx, item.fileID); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO library_legacy_baselines(file_id,author_id,record_hash,catalog_at,folder_name,tags_json) VALUES(?,?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET author_id=excluded.author_id,record_hash=excluded.record_hash,catalog_at=excluded.catalog_at,folder_name=excluded.folder_name,tags_json=excluded.tags_json`, item.fileID, item.author, item.hash, item.catalogAt, item.folderName, item.tagsJSON); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func knownLibraryOperationKind(kind string) bool {
	switch kind {
	case "folder.create", "folder.rename", "folder.delete", "folder.move", "tag.add", "tag.remove", "file.trash", "file.restore", "file.delete", "file.rename", "file.version.select", "member.retire", "member.join":
		return true
	default:
		return false
	}
}

func materializeLibraryOperation(tx *sql.Tx, op libraryOperationEnvelope) error {
	var payload struct {
		FileIDs      []string         `json:"fileIds"`
		FileName     string           `json:"fileName"`
		RevisionID   string           `json:"revisionId"`
		Resolve      bool             `json:"resolve"`
		Tag          string           `json:"tag"`
		TagToken     string           `json:"tagToken"`
		RemoveTokens []string         `json:"removeTokens"`
		FolderID     string           `json:"folderId"`
		FolderName   string           `json:"folderName"`
		PreviousName string           `json:"previousName"`
		Context      map[string]int64 `json:"context"`
		NodeID       string           `json:"nodeId"`
		Epoch        int64            `json:"epoch"`
	}
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return err
	}
	switch op.Kind {
	case "member.retire", "member.join":
		if payload.NodeID == "" || len(payload.NodeID) > 160 || payload.Epoch < 1 { return nil }
		var currentEpoch int64
		var currentState string
		err := tx.QueryRow(`SELECT epoch,state FROM library_members WHERE node_id=?`, payload.NodeID).Scan(&currentEpoch, &currentState)
		if err != nil && err != sql.ErrNoRows { return err }
		if op.Kind == "member.retire" {
			if err == sql.ErrNoRows || currentEpoch != payload.Epoch || currentState == "retired" { return nil }
			_, err = tx.Exec(`UPDATE library_members SET state='retired',author_id=?,counter=?,updated_at=? WHERE node_id=?`, op.AuthorID, op.Counter, now(), payload.NodeID)
			return err
		}
		if err == nil && payload.Epoch <= currentEpoch { return nil }
		_, err = tx.Exec(`INSERT INTO library_members(node_id,epoch,state,author_id,counter,updated_at) VALUES(?,?,'active',?,?,?) ON CONFLICT(node_id) DO UPDATE SET epoch=excluded.epoch,state='active',author_id=excluded.author_id,counter=excluded.counter,updated_at=excluded.updated_at`, payload.NodeID, payload.Epoch, op.AuthorID, op.Counter, now())
		return err
	case "file.version.select":
		if len(payload.FileIDs) != 1 || payload.FileIDs[0] == "" || len(payload.FileIDs[0]) > 160 || payload.RevisionID == "" || len(payload.RevisionID) > 160 {
			return nil
		}
		return materializeRevisionSelection(tx, op, payload.FileIDs[0], payload.RevisionID, payload.Context, payload.Resolve)
	case "file.rename":
		if len(payload.FileIDs) != 1 || payload.FileIDs[0] == "" || len(payload.FileIDs[0]) > 160 || !validLogicalFileName(payload.FileName) {
			return nil
		}
		return materializeFileRename(tx, op, payload.FileIDs[0], strings.TrimSpace(payload.FileName), payload.Context, payload.Resolve)
	case "file.trash", "file.restore", "file.delete":
		return materializeFileLifecycle(tx, op, payload.FileIDs, payload.Context)
	case "folder.create", "folder.rename", "folder.delete":
		name := strings.TrimSpace(payload.FolderName)
		previous := strings.TrimSpace(payload.PreviousName)
		if op.Kind == "folder.rename" && previous != "" && name != "" && len([]rune(name)) <= 80 {
			return materializeFolderRename(tx, op, previous, name, payload.Context)
		}
		if op.Kind == "folder.create" && name != "" && len([]rune(name)) <= 80 {
			var deletedAuthor string
			var deletedCounter int64
			err := tx.QueryRow(`SELECT author_id,counter FROM library_folder_tombstones WHERE name=?`, name).Scan(&deletedAuthor, &deletedCounter)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil && deletedAuthor != op.AuthorID && payload.Context[deletedAuthor] < deletedCounter {
				return nil
			}
			if _, err := tx.Exec(`DELETE FROM library_folder_tombstones WHERE name=?`, name); err != nil {
				return err
			}
			_, err = tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, newID(), name, now())
			return err
		}
		if op.Kind == "folder.delete" && name != "" {
			if _, err := tx.Exec(`INSERT INTO library_folder_tombstones(name,author_id,counter,deleted_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,deleted_at=excluded.deleted_at`, name, op.AuthorID, op.Counter, now()); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) SELECT lf.file_id FROM library_file_folders lf JOIN folders f ON f.id=lf.folder_id WHERE f.name=? COLLATE NOCASE`, name); err != nil {
				return err
			}
			_, err := tx.Exec(`DELETE FROM folders WHERE name=? COLLATE NOCASE`, name)
			return err
		}
		return nil
	case "tag.add", "tag.remove":
		if len(payload.FileIDs) == 0 || len(payload.FileIDs) > 20000 {
			return nil
		}
		name := strings.TrimSpace(payload.Tag)
		if name == "" || len([]rune(name)) > 40 {
			return nil
		}
		var tagID string
		if op.Kind == "tag.add" {
			if _, err := tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, newID(), name, now()); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID); err != nil {
				return err
			}
		} else {
			_ = tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID)
		}
		for _, fileID := range uniqueOperationFileIDs(payload.FileIDs) {
			if err := ensureOperationFile(tx, fileID); err != nil {
				return err
			}
			if err := seedLegacyClassification(tx, fileID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) VALUES(?)`, fileID); err != nil {
				return err
			}
			if op.Kind == "tag.add" {
				token := payload.TagToken
				if token == "" {
					token = op.OpID
				}
				if _, err := tx.Exec(`INSERT INTO library_tag_add_tokens(file_id,tag_name,token,author_id,counter,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, name, token, op.AuthorID, op.Counter, now()); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO library_file_tags(file_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, fileID, tagID, now()); err != nil {
					return err
				}
				_, err := tx.Exec(`INSERT INTO resource_tags(resource_id,tag_id,created_at) SELECT resource_id,?,? FROM library_resource_aliases WHERE file_id=? ON CONFLICT DO NOTHING`, tagID, now(), fileID)
				if err != nil {
					return err
				}
			} else if tagID != "" {
				// A legacy baseline predates the operation log. Its token is
				// observed by every new remove, while concurrent new add tokens
				// remain protected by the ordinary observed-token rule.
				if _, err := tx.Exec(`INSERT INTO library_tag_remove_tokens(file_id,tag_name,token,removed_at) SELECT ?,?,token,? FROM library_tag_add_tokens WHERE file_id=? AND tag_name=? AND token LIKE 'legacy:%' ON CONFLICT DO NOTHING`, fileID, name, now(), fileID, name); err != nil {
					return err
				}
				for _, token := range payload.RemoveTokens {
					if token != "" {
						if _, err := tx.Exec(`INSERT INTO library_tag_remove_tokens(file_id,tag_name,token,removed_at) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`, fileID, name, token, now()); err != nil {
							return err
						}
					}
				}
				var remaining int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM library_tag_add_tokens a WHERE a.file_id=? AND a.tag_name=? AND NOT EXISTS (SELECT 1 FROM library_tag_remove_tokens r WHERE r.file_id=a.file_id AND r.tag_name=a.tag_name AND r.token=a.token)`, fileID, name).Scan(&remaining); err != nil {
					return err
				}
				if remaining == 0 {
					if _, err := tx.Exec(`DELETE FROM library_file_tags WHERE file_id=? AND tag_id=?`, fileID, tagID); err != nil {
						return err
					}
					if _, err := tx.Exec(`DELETE FROM resource_tags WHERE tag_id=? AND resource_id IN (SELECT resource_id FROM library_resource_aliases WHERE file_id=?)`, tagID, fileID); err != nil {
						return err
					}
				}
			}
		}
	case "folder.move":
		if len(payload.FileIDs) == 0 || len(payload.FileIDs) > 20000 {
			return nil
		}
		name := strings.TrimSpace(payload.FolderName)
		folderID := ""
		var deleted int
		if name != "" {
			if err := tx.QueryRow(`SELECT COUNT(*) FROM library_folder_tombstones WHERE name=?`, name).Scan(&deleted); err != nil {
				return err
			}
		}
		if deleted == 0 && payload.FolderID != "" && name != "" && len([]rune(name)) <= 80 {
			if _, err := tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, newID(), name, now()); err != nil {
				return err
			}
			if err := tx.QueryRow(`SELECT id FROM folders WHERE name=?`, name).Scan(&folderID); err != nil {
				return err
			}
		}
		for _, fileID := range uniqueOperationFileIDs(payload.FileIDs) {
			if err := ensureOperationFile(tx, fileID); err != nil {
				return err
			}
			if err := seedLegacyClassification(tx, fileID); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) VALUES(?)`, fileID); err != nil {
				return err
			}
			if deleted != 0 {
				if _, err := tx.Exec(`INSERT INTO library_folder_conflicts(file_id,author_id,counter,op_id,folder_name,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, op.AuthorID, op.Counter, op.OpID, name, now()); err != nil {
					return err
				}
				continue
			}
			var priorAuthor string
			var priorCounter int64
			err := tx.QueryRow(`SELECT author_id,counter FROM library_file_folder_state WHERE file_id=?`, fileID).Scan(&priorAuthor, &priorCounter)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			// A different writer only conflicts when this operation did not
			// observe the stored writer state. Operations without context are
			// legacy/conservative and therefore treated as concurrent.
			if err == nil && priorAuthor != op.AuthorID && payload.Context[priorAuthor] < priorCounter {
				_, err = tx.Exec(`INSERT INTO library_folder_conflicts(file_id,author_id,counter,op_id,folder_name,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, op.AuthorID, op.Counter, op.OpID, name, now())
				if err != nil {
					return err
				}
				continue
			}
			if folderID == "" {
				if _, err := tx.Exec(`DELETE FROM library_file_folders WHERE file_id=?`, fileID); err != nil {
					return err
				}
				if _, err := tx.Exec(`DELETE FROM resource_folders WHERE resource_id IN (SELECT resource_id FROM library_resource_aliases WHERE file_id=?)`, fileID); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(`INSERT INTO library_file_folders(file_id,folder_id,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET folder_id=excluded.folder_id,updated_at=excluded.updated_at`, fileID, folderID, now()); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) SELECT resource_id,? FROM library_resource_aliases WHERE file_id=? ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, folderID, fileID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(`INSERT INTO library_file_folder_state(file_id,author_id,counter,folder_id,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,folder_id=excluded.folder_id,updated_at=excluded.updated_at`, fileID, op.AuthorID, op.Counter, folderID, now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// materializeFolderRename applies a folder rename keyed by its pre-rename
// anchor. A different writer only conflicts when this operation did not
// observe the stored rename; the loser is kept as a candidate instead of
// silently no-op'ing, so no concurrent rename is lost (§5.2).
func materializeFolderRename(tx *sql.Tx, op libraryOperationEnvelope, previous, name string, context map[string]int64) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO library_classification_authoritative(file_id) SELECT lf.file_id FROM library_file_folders lf JOIN folders f ON f.id=lf.folder_id WHERE f.name=? COLLATE NOCASE`, previous); err != nil {
		return err
	}
	var priorAuthor string
	var priorCounter int64
	err := tx.QueryRow(`SELECT author_id,counter FROM library_folder_rename_state WHERE anchor=?`, previous).Scan(&priorAuthor, &priorCounter)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && priorAuthor != op.AuthorID && context[priorAuthor] < priorCounter {
		_, err = tx.Exec(`INSERT INTO library_folder_rename_conflicts(anchor,author_id,counter,op_id,folder_name,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, previous, op.AuthorID, op.Counter, op.OpID, name, now())
		return err
	}
	if _, err := tx.Exec(`UPDATE folders SET name=? WHERE name=? COLLATE NOCASE`, name, previous); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO library_folder_rename_state(anchor,author_id,counter,current_name,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(anchor) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,current_name=excluded.current_name,updated_at=excluded.updated_at`, previous, op.AuthorID, op.Counter, name, now())
	return err
}

func ensureOperationFile(tx *sql.Tx, fileID string) error {
	if fileID == "" || len(fileID) > 160 {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO NOTHING`, fileID, now(), now())
	return err
}

func seedLegacyClassification(tx *sql.Tx, fileID string) error {
	var authoritative int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM library_classification_authoritative WHERE file_id=?`, fileID).Scan(&authoritative); err != nil || authoritative != 0 {
		return err
	}
	var author, recordHash, folderName, rawTags string
	err := tx.QueryRow(`SELECT author_id,record_hash,folder_name,tags_json FROM library_legacy_baselines WHERE file_id=?`, fileID).Scan(&author, &recordHash, &folderName, &rawTags)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if folderName != "" && len([]rune(folderName)) <= 80 {
		if _, err := tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, newID(), folderName, now()); err != nil {
			return err
		}
		var folderID string
		if err := tx.QueryRow(`SELECT id FROM folders WHERE name=?`, folderName).Scan(&folderID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO library_file_folders(file_id,folder_id,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO NOTHING`, fileID, folderID, now()); err != nil {
			return err
		}
	}
	var tags []string
	if err := json.Unmarshal([]byte(rawTags), &tags); err != nil {
		return err
	}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || len([]rune(tag)) > 40 {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, newID(), tag, now()); err != nil {
			return err
		}
		var tagID string
		if err := tx.QueryRow(`SELECT id FROM tags WHERE name=?`, tag).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO library_file_tags(file_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, fileID, tagID, now()); err != nil {
			return err
		}
		token := "legacy:" + recordHash + ":" + strings.ToLower(tag)
		if _, err := tx.Exec(`INSERT INTO library_tag_add_tokens(file_id,tag_name,token,author_id,counter,created_at) VALUES(?,?,?,?,0,?) ON CONFLICT DO NOTHING`, fileID, tag, token, "legacy:"+author, now()); err != nil {
			return err
		}
	}
	return nil
}

func materializeFileLifecycle(tx *sql.Tx, op libraryOperationEnvelope, rawFileIDs any, context map[string]int64) error {
	fileIDs, ok := rawFileIDs.([]string)
	if !ok || len(fileIDs) == 0 || len(fileIDs) > 20000 {
		return nil
	}
	state := "active"
	if op.Kind == "file.trash" {
		state = "trashed"
	}
	if context == nil {
		context = map[string]int64{}
	}
	contextJSON, err := json.Marshal(context)
	if err != nil {
		return err
	}
	for _, fileID := range uniqueOperationFileIDs(fileIDs) {
		if len(fileID) > 160 {
			continue
		}
		if err := ensureOperationFile(tx, fileID); err != nil {
			return err
		}
		if op.Kind == "file.delete" {
			if _, err := tx.Exec(`INSERT INTO library_file_deletions(file_id,author_id,counter,op_id,deleted_at) VALUES(?,?,?,?,?) ON CONFLICT(file_id) DO NOTHING`, fileID, op.AuthorID, op.Counter, op.OpID, now()); err != nil {
				return err
			}
			continue
		}
		var deleted int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?`, fileID).Scan(&deleted); err != nil {
			return err
		}
		if deleted != 0 {
			continue
		}
		var priorState, priorAuthor, priorID, priorContextJSON string
		var priorCounter int64
		err := tx.QueryRow(`SELECT state,author_id,counter,op_id,context_json FROM library_file_lifecycle WHERE file_id=?`, fileID).Scan(&priorState, &priorAuthor, &priorCounter, &priorID, &priorContextJSON)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			var priorContext map[string]int64
			if err := json.Unmarshal([]byte(priorContextJSON), &priorContext); err != nil {
				return err
			}
			if priorContext[op.AuthorID] >= op.Counter {
				continue
			}
			if priorAuthor == op.AuthorID && priorCounter >= op.Counter {
				continue
			}
			if priorAuthor != op.AuthorID && context[priorAuthor] < priorCounter {
				// Concurrent trash hides the file. A restore must first observe
				// the trash; concurrent restores cannot make it visible.
				if state == "active" && priorState == "trashed" {
					continue
				}
				if state == priorState && op.OpID <= priorID {
					continue
				}
			}
		}
		if _, err := tx.Exec(`INSERT INTO library_file_lifecycle(file_id,state,author_id,counter,op_id,context_json,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET state=excluded.state,author_id=excluded.author_id,counter=excluded.counter,op_id=excluded.op_id,context_json=excluded.context_json,updated_at=excluded.updated_at`, fileID, state, op.AuthorID, op.Counter, op.OpID, string(contextJSON), now()); err != nil {
			return err
		}
	}
	return nil
}

func uniqueOperationFileIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func observedTagTokens(tx *sql.Tx, fileIDs []string, name string) ([]string, error) {
	seen := map[string]bool{}
	result := []string{}
	for _, fileID := range fileIDs {
		rows, err := tx.Query(`SELECT a.token FROM library_tag_add_tokens a WHERE a.file_id=? AND a.tag_name=? AND NOT EXISTS (SELECT 1 FROM library_tag_remove_tokens r WHERE r.file_id=a.file_id AND r.tag_name=a.tag_name AND r.token=a.token)`, fileID, name)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var token string
			if rows.Scan(&token) == nil && !seen[token] {
				seen[token] = true
				result = append(result, token)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}
