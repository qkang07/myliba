package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *server) hubWriterChange(ctx context.Context, kind, libraryID, authorID string, epoch int64) error {
	if s.cloud == nil || s.cloud.runtime == nil {
		return fmt.Errorf("云库未启用")
	}
	wsURL, token, err := s.cloud.runtime.credentials()
	if err != nil || wsURL == "" || len(token) < 32 {
		return fmt.Errorf("Hub 不可用")
	}
	hub, err := hubHTTPURL(wsURL)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"libraryId": libraryID, "authorId": authorID, "epoch": epoch})
	resp, err := s.cloud.runtime.request(ctx, http.MethodPost, hub, "/cloud/writers/"+kind, token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Hub writer %s: HTTP %d", kind, resp.StatusCode)
	}
	return nil
}

func (s *server) notifyHubWriterChange(kind, libraryID, authorID string, epoch int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.hubWriterChange(ctx, kind, libraryID, authorID, epoch)
}

func (s *server) listLibraryMembers(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT m.node_id,m.epoch,m.state,m.updated_at,COALESCE(r.catalog_at,'') FROM library_members m LEFT JOIN library_receipts r ON r.node_id=m.node_id ORDER BY m.node_id`)
	if err != nil {
		writeError(w, 500, "无法读取成员")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, state, updated, receipt string
		var epoch int64
		if err := rows.Scan(&id, &epoch, &state, &updated, &receipt); err != nil {
			writeError(w, 500, "无法读取成员")
			return
		}
		items = append(items, map[string]any{"nodeId": id, "epoch": epoch, "state": state, "updatedAt": updated, "lastReceiptAt": receipt})
	}
	if rows.Err() != nil {
		writeError(w, 500, "无法读取成员")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *server) retireLibraryMember(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("nodeId"))
	if id == "" || len(id) > 160 || id == s.cloud.runtime.id {
		writeError(w, 400, "不能退役当前查询节点")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法退役成员")
		return
	}
	defer tx.Rollback()
	var epoch int64
	var state string
	if err = tx.QueryRow(`SELECT epoch,state FROM library_members WHERE node_id=?`, id).Scan(&epoch, &state); err == sql.ErrNoRows {
		writeError(w, 404, "成员不存在")
		return
	} else if err != nil {
		writeError(w, 500, "无法退役成员")
		return
	}
	if state == "retired" {
		writeError(w, 409, "成员已退役")
		return
	}
	var op libraryOperationEnvelope
	if err = recordLibraryOperation(tx, "member.retire", map[string]any{"nodeId": id, "epoch": epoch}, &op); err == nil {
		err = materializeLibraryOperation(tx, op)
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, 500, "无法退役成员")
		return
	}
	s.notifyHubWriterChange("retire", s.cloud.libraryID(), id, epoch)
	writeJSON(w, 200, map[string]any{"nodeId": id, "epoch": epoch, "state": "retired", "operationId": op.OpID})
}

func (s *server) rejoinLibraryMember(w http.ResponseWriter, r *http.Request) {
	if s.cloud == nil || s.cloud.runtime == nil {
		writeError(w, 503, "云库未启用")
		return
	}
	wsURL, token, err := s.cloud.runtime.credentials()
	if err != nil || wsURL == "" || len(token) < 32 {
		writeError(w, 503, "重新加入前需连接 Hub")
		return
	}
	hub, err := hubHTTPURL(wsURL)
	if err != nil {
		writeError(w, 503, "Hub 地址无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err = s.cloud.runtime.syncCatalog(ctx, hub, token); err != nil {
		writeError(w, 503, "无法从 Hub 完整引导")
		return
	}
	if err = s.cloud.applyRemoteOperations(); err != nil {
		writeError(w, 503, "无法应用引导操作")
		return
	}
	id := s.cloud.runtime.id
	var epoch int64
	var state string
	if err = s.db.QueryRow(`SELECT epoch,state FROM library_members WHERE node_id=?`, id).Scan(&epoch, &state); err != nil || state != "retired" {
		writeError(w, 409, "当前节点未退役")
		return
	}
	var pending int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed')`).Scan(&pending); err != nil || pending != 0 {
		writeError(w, 409, "旧作者仍有待发布操作，请先处理")
		return
	}
	rejoinCtx, rejoinCancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer rejoinCancel()
	if err = s.hubWriterChange(rejoinCtx, "rejoin", s.cloud.libraryID(), id, epoch+1); err != nil {
		writeError(w, 503, "无法在 Hub 注册新作者 epoch")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法重新加入")
		return
	}
	defer tx.Rollback()
	var currentEpoch int64
	var currentState string
	if err = tx.QueryRow(`SELECT epoch,state FROM library_members WHERE node_id=?`, id).Scan(&currentEpoch, &currentState); err != nil || currentState != "retired" || currentEpoch != epoch {
		writeError(w, 409, "成员状态已变化，请重试")
		return
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed')`).Scan(&pending); err != nil || pending != 0 {
		writeError(w, 409, "旧作者仍有待发布操作，请先处理")
		return
	}
	newAuthor := "desktop-" + newID()
	if _, err = tx.Exec(`INSERT INTO library_operation_identity(key,value) VALUES('author',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, newAuthor); err != nil {
		writeError(w, 500, "无法创建新作者 epoch")
		return
	}
	var op libraryOperationEnvelope
	if err = recordLibraryOperation(tx, "member.join", map[string]any{"nodeId": id, "epoch": epoch + 1}, &op); err == nil {
		err = materializeLibraryOperation(tx, op)
	}
	if err != nil || tx.Commit() != nil {
		writeError(w, 500, "无法重新加入")
		return
	}
	writeJSON(w, 200, map[string]any{"nodeId": id, "epoch": epoch + 1, "state": "active", "operationId": op.OpID})
}
