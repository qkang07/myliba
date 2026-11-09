package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

func materializeRevisionSelection(tx *sql.Tx, op libraryOperationEnvelope, fileID, revisionID string, context map[string]int64, resolve bool) error {
	if err := ensureOperationFile(tx, fileID); err != nil {
		return err
	}
	var priorAuthor, priorID, priorRevision, priorContext string
	var priorCounter int64
	err := tx.QueryRow(`SELECT author_id,counter,op_id,revision_id,context_json FROM library_file_current_revision WHERE file_id=?`, fileID).Scan(&priorAuthor, &priorCounter, &priorID, &priorRevision, &priorContext)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	concurrent := false
	if err == nil {
		var seen map[string]int64
		if json.Unmarshal([]byte(priorContext), &seen) == nil && seen[op.AuthorID] >= op.Counter {
			return nil
		}
		if priorAuthor == op.AuthorID && priorCounter >= op.Counter {
			return nil
		}
		if priorAuthor != op.AuthorID && context[priorAuthor] < priorCounter {
			concurrent = true
			if !operationBefore(op.AuthorID, op.Counter, op.OpID, priorAuthor, priorCounter, priorID) {
				_, err = tx.Exec(`INSERT INTO library_file_revision_conflicts(file_id,author_id,counter,op_id,revision_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, op.AuthorID, op.Counter, op.OpID, revisionID, now())
				return err
			}
			if _, err = tx.Exec(`INSERT INTO library_file_revision_conflicts(file_id,author_id,counter,op_id,revision_id,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, priorAuthor, priorCounter, priorID, priorRevision, now()); err != nil {
				return err
			}
		}
	}
	if resolve && !concurrent {
		if _, err = tx.Exec(`DELETE FROM library_file_revision_conflicts WHERE file_id=?`, fileID); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(context)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO library_file_current_revision(file_id,revision_id,author_id,counter,op_id,context_json,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET revision_id=excluded.revision_id,author_id=excluded.author_id,counter=excluded.counter,op_id=excluded.op_id,context_json=excluded.context_json,updated_at=excluded.updated_at`, fileID, revisionID, op.AuthorID, op.Counter, op.OpID, string(raw), now())
	return err
}

func (c *cloudClient) currentRevisionSelections() (map[string]string, error) {
	selected := map[string]string{}
	if c.runtime == nil || c.runtime.db == nil {
		return selected, nil
	}
	rows, err := c.runtime.db.Query(`SELECT file_id,revision_id FROM library_file_current_revision`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var fileID, revisionID string
		if err = rows.Scan(&fileID, &revisionID); err != nil {
			return nil, err
		}
		selected[fileID] = revisionID
	}
	return selected, rows.Err()
}

func (s *server) selectCurrentRevision(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		RevisionID string `json:"revisionId"`
		Resolve    bool   `json:"resolve"`
	}
	if fileID == "" || len(fileID) > 160 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || input.RevisionID == "" || len(input.RevisionID) > 160 {
		writeError(w, 400, "版本选择无效")
		return
	}
	if !s.logicalFileForEdit(w, fileID) {
		return
	}
	files, err := s.cloud.libraryFiles(true)
	if err != nil {
		writeError(w, 500, "无法读取文件版本")
		return
	}
	found := false
	for _, file := range files {
		if file.FileID == fileID && file.RevisionID == input.RevisionID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, 404, "版本不存在")
		return
	}
	if !input.Resolve {
		var conflicts int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM library_file_revision_conflicts WHERE file_id=?`, fileID).Scan(&conflicts); err != nil {
			writeError(w, 500, "无法读取版本冲突")
			return
		} else if conflicts > 0 {
			writeError(w, 409, "请先处理此文件的并发版本选择")
			return
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法选择当前版本")
		return
	}
	defer tx.Rollback()
	var op libraryOperationEnvelope
	if err = recordLibraryOperation(tx, "file.version.select", map[string]any{"fileIds": []string{fileID}, "revisionId": input.RevisionID, "resolve": input.Resolve}, &op); err == nil {
		err = materializeLibraryOperation(tx, op)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, 500, "无法选择当前版本")
		return
	}
	writeJSON(w, 200, map[string]any{"fileId": fileID, "revisionId": input.RevisionID, "operationId": op.OpID, "state": "pending"})
}

func (s *server) libraryRevisionConflicts(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT c.file_id,c.author_id,c.counter,c.op_id,c.revision_id,c.created_at,COALESCE(st.revision_id,'') FROM library_file_revision_conflicts c LEFT JOIN library_file_current_revision st ON st.file_id=c.file_id ORDER BY c.file_id,c.created_at,c.author_id,c.counter`)
	if err != nil {
		writeError(w, 500, "无法读取版本选择冲突")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var fileID, author, opID, revisionID, created, applied string
		var counter int64
		if err = rows.Scan(&fileID, &author, &counter, &opID, &revisionID, &created, &applied); err != nil {
			writeError(w, 500, "无法读取版本选择冲突")
			return
		}
		items = append(items, map[string]any{"fileId": fileID, "author": author, "counter": counter, "opId": opID, "revisionId": revisionID, "appliedRevisionId": applied, "createdAt": created})
	}
	if err = rows.Err(); err != nil {
		writeError(w, 500, "无法读取版本选择冲突")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
