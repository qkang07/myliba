package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// Names are library metadata. A rename never changes a source path or a
// revision's content identity.
func validLogicalFileName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && len([]rune(name)) <= 255 && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func operationBefore(author string, counter int64, opID, otherAuthor string, otherCounter int64, otherID string) bool {
	if author != otherAuthor {
		return author < otherAuthor
	}
	if counter != otherCounter {
		return counter < otherCounter
	}
	return opID < otherID
}

func materializeFileRename(tx *sql.Tx, op libraryOperationEnvelope, fileID, name string, context map[string]int64, resolve bool) error {
	if err := ensureOperationFile(tx, fileID); err != nil {
		return err
	}
	var priorAuthor, priorID, priorName, priorContext string
	var priorCounter int64
	err := tx.QueryRow(`SELECT author_id,counter,op_id,current_name,context_json FROM library_file_name_state WHERE file_id=?`, fileID).Scan(&priorAuthor, &priorCounter, &priorID, &priorName, &priorContext)
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
				_, err = tx.Exec(`INSERT INTO library_file_name_conflicts(file_id,author_id,counter,op_id,file_name,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, op.AuthorID, op.Counter, op.OpID, name, now())
				return err
			}
			if _, err = tx.Exec(`INSERT INTO library_file_name_conflicts(file_id,author_id,counter,op_id,file_name,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, fileID, priorAuthor, priorCounter, priorID, priorName, now()); err != nil {
				return err
			}
		}
	}
	if resolve && !concurrent {
		if _, err = tx.Exec(`DELETE FROM library_file_name_conflicts WHERE file_id=?`, fileID); err != nil {
			return err
		}
	}
	rawContext, err := json.Marshal(context)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO library_file_name_state(file_id,author_id,counter,op_id,current_name,context_json,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(file_id) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,op_id=excluded.op_id,current_name=excluded.current_name,context_json=excluded.context_json,updated_at=excluded.updated_at`, fileID, op.AuthorID, op.Counter, op.OpID, name, string(rawContext), now())
	return err
}

func (s *server) renameLogicalFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		FileName string `json:"fileName"`
		Resolve  bool   `json:"resolve"`
	}
	if fileID == "" || len(fileID) > 160 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validLogicalFileName(input.FileName) {
		writeError(w, 400, "文件名称无效")
		return
	}
	if !s.logicalFileForEdit(w, fileID) {
		return
	}
	if !input.Resolve {
		var conflicts int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM library_file_name_conflicts WHERE file_id=?`, fileID).Scan(&conflicts); err != nil {
			writeError(w, 500, "无法读取改名冲突")
			return
		} else if conflicts > 0 {
			writeError(w, 409, "请先处理此文件的并发改名")
			return
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法改名")
		return
	}
	defer tx.Rollback()
	var op libraryOperationEnvelope
	if err = recordLibraryOperation(tx, "file.rename", map[string]any{"fileIds": []string{fileID}, "fileName": strings.TrimSpace(input.FileName), "resolve": input.Resolve}, &op); err == nil {
		err = materializeLibraryOperation(tx, op)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		writeError(w, 500, "无法改名")
		return
	}
	if s.cloud != nil {
		s.cloud.queueLocalIndexByFileID(fileID)
	}
	writeJSON(w, 200, map[string]any{"fileId": fileID, "operationId": op.OpID, "state": "pending"})
}

func (s *server) libraryFileNameConflicts(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT c.file_id,c.author_id,c.counter,c.op_id,c.file_name,c.created_at,COALESCE(st.current_name,'') FROM library_file_name_conflicts c LEFT JOIN library_file_name_state st ON st.file_id=c.file_id ORDER BY c.file_id,c.created_at,c.author_id,c.counter`)
	if err != nil {
		writeError(w, 500, "无法读取文件改名冲突")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var fileID, author, opID, name, created, applied string
		var counter int64
		if err = rows.Scan(&fileID, &author, &counter, &opID, &name, &created, &applied); err != nil {
			writeError(w, 500, "无法读取文件改名冲突")
			return
		}
		items = append(items, map[string]any{"fileId": fileID, "author": author, "counter": counter, "opId": opID, "fileName": name, "appliedName": applied, "createdAt": created})
	}
	if err = rows.Err(); err != nil {
		writeError(w, 500, "无法读取文件改名冲突")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
