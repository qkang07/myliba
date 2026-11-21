package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
)

type classificationDomainError struct {
	status  int
	code    string
	message string
}

func (e *classificationDomainError) Error() string { return e.message }

func classificationError(status int, code, message string) error {
	return &classificationDomainError{status: status, code: code, message: message}
}

func writeClassificationError(w http.ResponseWriter, err error) {
	var domain *classificationDomainError
	if errors.As(err, &domain) {
		writeJSON(w, domain.status, map[string]string{"error": domain.message, "code": domain.code})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "索引操作失败", "code": "classificationInternal"})
}

func validClassificationName(raw string) (string, bool) {
	name := normalizeClassificationName(raw)
	if name == "" || len([]rune(name)) > 255 {
		return "", false
	}
	for _, current := range name {
		if current == 0 || unicode.IsControl(current) {
			return "", false
		}
	}
	return name, true
}

func classificationNodeVersion(indexID, parentID, name string, deleted int, nameOpID, parentOpID string) string {
	return classificationSeed("classification-node-v1", indexID, parentID, name, fmt.Sprint(deleted), nameOpID, parentOpID)
}

func rebuildClassificationMemberships(tx *sql.Tx, libraryID string) error {
	if _, err := tx.Exec(`DELETE FROM classification_memberships WHERE library_id=?`, libraryID); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO classification_memberships(library_id,file_id,index_id,has_manual,has_automatic,updated_at)
		SELECT a.library_id,a.file_id,a.index_id,
			MAX(CASE WHEN a.origin IN ('manual','legacy-import') THEN 1 ELSE 0 END),
			MAX(CASE WHEN a.origin='automatic' THEN 1 ELSE 0 END),?
		FROM classification_adds a
		JOIN classification_nodes n ON n.library_id=a.library_id AND n.index_id=a.index_id AND n.deleted=0
		LEFT JOIN classification_removes r ON r.library_id=a.library_id AND r.file_id=a.file_id AND r.index_id=a.index_id AND r.add_token=a.add_token
		WHERE a.library_id=? AND r.add_token IS NULL
		GROUP BY a.library_id,a.file_id,a.index_id`, now(), libraryID)
	return err
}

func rebuildClassificationAncestors(tx *sql.Tx, libraryID string) error {
	rows, err := tx.Query(`SELECT index_id,COALESCE(parent_id,'') FROM classification_nodes WHERE library_id=? AND deleted=0 ORDER BY index_id`, libraryID)
	if err != nil {
		return err
	}
	parents := map[string]string{}
	ids := []string{}
	for rows.Next() {
		var id, parent string
		if err = rows.Scan(&id, &parent); err != nil {
			rows.Close()
			return err
		}
		parents[id] = parent
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err = tx.Exec(`DELETE FROM classification_ancestors WHERE library_id=?`, libraryID); err != nil {
		return err
	}
	for _, descendant := range ids {
		seen := map[string]bool{}
		current := descendant
		depth := 0
		for current != "" {
			if seen[current] || depth > 1024 {
				return classificationError(http.StatusConflict, "indexCycle", "索引层级存在循环或深度异常")
			}
			seen[current] = true
			if _, ok := parents[current]; !ok {
				return classificationError(http.StatusConflict, "indexParentMissing", "索引上级不存在")
			}
			if _, err = tx.Exec(`INSERT INTO classification_ancestors(library_id,ancestor_id,descendant_id,depth) VALUES(?,?,?,?)`, libraryID, current, descendant, depth); err != nil {
				return err
			}
			current = parents[current]
			depth++
		}
	}
	return nil
}

func classificationSnapshotGeneration(tx *sql.Tx, libraryID string) (string, error) {
	parts := []string{libraryID}
	rows, err := tx.Query(`SELECT index_id,COALESCE(parent_id,''),name,deleted,name_op_id,parent_op_id FROM classification_nodes WHERE library_id=? ORDER BY index_id`, libraryID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var id, parent, name, nameOp, parentOp string
		var deleted int
		if err = rows.Scan(&id, &parent, &name, &deleted, &nameOp, &parentOp); err != nil {
			rows.Close()
			return "", err
		}
		parts = append(parts, "node", id, parent, name, fmt.Sprint(deleted), nameOp, parentOp)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	rows, err = tx.Query(`SELECT file_id,index_id,has_manual,has_automatic FROM classification_memberships WHERE library_id=? ORDER BY file_id,index_id`, libraryID)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var fileID, indexID string
		var manual, automatic int
		if err = rows.Scan(&fileID, &indexID, &manual, &automatic); err != nil {
			rows.Close()
			return "", err
		}
		parts = append(parts, "membership", fileID, indexID, fmt.Sprint(manual), fmt.Sprint(automatic))
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	return classificationSeed(parts...), nil
}

func classificationRequestResult(tx *sql.Tx, libraryID, actor, requestID, requestHash string) ([]byte, bool, error) {
	if strings.TrimSpace(requestID) == "" || len(requestID) > 160 {
		return nil, false, classificationError(http.StatusBadRequest, "invalidRequestId", "requestId 无效")
	}
	var storedHash, result string
	err := tx.QueryRow(`SELECT request_hash,result_json FROM classification_requests WHERE library_id=? AND actor=? AND request_id=?`, libraryID, actor, requestID).Scan(&storedHash, &result)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if storedHash != requestHash {
		return nil, false, classificationError(http.StatusConflict, "requestIdConflict", "requestId 已用于不同操作")
	}
	return []byte(result), true, nil
}

func storeClassificationRequest(tx *sql.Tx, libraryID, actor, requestID, requestHash string, result any) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO classification_requests(library_id,actor,request_id,request_hash,result_json,created_at) VALUES(?,?,?,?,?,?)`, libraryID, actor, requestID, requestHash, string(raw), now())
	return raw, err
}

func writeStoredClassificationResult(w http.ResponseWriter, status int, raw []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func classificationNodeForResponse(tx *sql.Tx, libraryID, indexID string) (classificationIndexView, error) {
	var item classificationIndexView
	var parent sql.NullString
	var deleted int
	var nameOp, parentOp string
	err := tx.QueryRow(`SELECT index_id,parent_id,name,deleted,name_op_id,parent_op_id,
		EXISTS(SELECT 1 FROM classification_nodes c WHERE c.library_id=n.library_id AND c.parent_id=n.index_id AND c.deleted=0),
		(SELECT COUNT(DISTINCT m.file_id) FROM classification_ancestors a JOIN classification_memberships m ON m.library_id=a.library_id AND m.index_id=a.descendant_id WHERE a.library_id=n.library_id AND a.ancestor_id=n.index_id)
		FROM classification_nodes n WHERE n.library_id=? AND n.index_id=?`, libraryID, indexID).Scan(&item.ID, &parent, &item.Name, &deleted, &nameOp, &parentOp, &item.HasChildren, &item.Count)
	if err != nil {
		return item, err
	}
	if parent.Valid {
		value := parent.String
		item.ParentID = &value
	}
	item.Version = classificationNodeVersion(item.ID, parent.String, item.Name, deleted, nameOp, parentOp)
	item.Ancestors = []classificationIndexView{}
	rows, err := tx.Query(`SELECT n.index_id,n.name FROM classification_ancestors a JOIN classification_nodes n ON n.library_id=a.library_id AND n.index_id=a.ancestor_id WHERE a.library_id=? AND a.descendant_id=? AND a.depth>0 ORDER BY a.depth DESC`, libraryID, indexID)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	for rows.Next() {
		var ancestor classificationIndexView
		if err = rows.Scan(&ancestor.ID, &ancestor.Name); err != nil {
			return item, err
		}
		ancestor.Ancestors = []classificationIndexView{}
		item.Ancestors = append(item.Ancestors, ancestor)
	}
	return item, rows.Err()
}

func classificationNodeForVisibleResponse(tx *sql.Tx, libraryID, indexID string, protected map[string]bool) (classificationIndexView, error) {
	item, err := classificationNodeForResponse(tx, libraryID, indexID)
	if err != nil || len(protected) == 0 {
		return item, err
	}
	rows, err := tx.Query(`SELECT DISTINCT m.file_id FROM classification_ancestors a
		JOIN classification_memberships m ON m.library_id=a.library_id AND m.index_id=a.descendant_id
		WHERE a.library_id=? AND a.ancestor_id=?`, libraryID, indexID)
	if err != nil {
		return item, err
	}
	defer rows.Close()
	item.Count = 0
	for rows.Next() {
		var fileID string
		if err = rows.Scan(&fileID); err != nil {
			return item, err
		}
		if !protected[fileID] {
			item.Count++
		}
	}
	return item, rows.Err()
}

func classificationSiblingConflict(tx *sql.Tx, libraryID, excludeID string, parentID *string, nameKey string) (bool, error) {
	var count int
	if parentID == nil {
		err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id<>? AND parent_id IS NULL AND deleted=0 AND name_key=?`, libraryID, excludeID, nameKey).Scan(&count)
		return count != 0, err
	}
	err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id<>? AND parent_id=? AND deleted=0 AND name_key=?`, libraryID, excludeID, *parentID, nameKey).Scan(&count)
	return count != 0, err
}

func classificationParentExists(tx *sql.Tx, libraryID string, parentID *string) error {
	if parentID == nil {
		return nil
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, libraryID, *parentID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return classificationError(http.StatusNotFound, "indexNotFound", "上级索引不存在")
	}
	return nil
}

func decodeClassificationParent(raw json.RawMessage) (*string, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return nil, classificationError(http.StatusBadRequest, "invalidIndexParent", "上级索引无效")
	}
	value = strings.TrimSpace(value)
	return &value, nil
}

func (s *server) refreshClassificationBeforeWrite() (string, string, error) {
	libraryID, actor, err := s.classificationIdentity()
	if err != nil {
		return "", "", err
	}
	_, err = refreshClassificationProjection(s.db, libraryID, actor)
	return libraryID, actor, err
}

func (s *server) createClassificationIndex(w http.ResponseWriter, r *http.Request) {
	libraryID, actor, err := s.refreshClassificationBeforeWrite()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	var input struct {
		RequestID string  `json:"requestId"`
		ParentID  *string `json:"parentId"`
		Name      string  `json:"name"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input) != nil {
		writeClassificationError(w, classificationError(400, "invalidIndex", "索引内容无效"))
		return
	}
	name, valid := validClassificationName(input.Name)
	if !valid {
		writeClassificationError(w, classificationError(400, "invalidIndexName", "索引名称无效"))
		return
	}
	if input.ParentID != nil {
		trimmed := strings.TrimSpace(*input.ParentID)
		input.ParentID = &trimmed
	}
	requestHash := classificationSeed("index.create", name, parentValue(input.ParentID))
	tx, err := s.db.Begin()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	defer tx.Rollback()
	if raw, found, requestErr := classificationRequestResult(tx, libraryID, actor, input.RequestID, requestHash); requestErr != nil {
		writeClassificationError(w, requestErr)
		return
	} else if found {
		writeStoredClassificationResult(w, 200, raw)
		return
	}
	if err = classificationParentExists(tx, libraryID, input.ParentID); err != nil {
		writeClassificationError(w, err)
		return
	}
	if conflict, conflictErr := classificationSiblingConflict(tx, libraryID, "", input.ParentID, classificationNameKey(name)); conflictErr != nil {
		writeClassificationError(w, conflictErr)
		return
	} else if conflict {
		writeClassificationError(w, classificationError(409, "indexNameConflict", "同级已有同名索引"))
		return
	}
	operationID := newID()
	indexID := "index:" + operationID
	domain := map[string]any{"indexId": indexID, "name": name, "origin": "manual"}
	if input.ParentID != nil {
		domain["parentId"] = *input.ParentID
	}
	if _, err = recordClassificationOperation(tx, libraryID, "index.create", operationID, domain); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = replayClassificationTx(tx, libraryID, actor); err != nil {
		writeClassificationError(w, err)
		return
	}
	node, err := classificationNodeForResponse(tx, libraryID, indexID)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	result := map[string]any{"node": node, "operationId": operationID}
	raw, err := storeClassificationRequest(tx, libraryID, actor, input.RequestID, requestHash, result)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeClassificationError(w, err)
		return
	}
	writeStoredClassificationResult(w, http.StatusCreated, raw)
}

func parentValue(parent *string) string {
	if parent == nil {
		return "<root>"
	}
	return *parent
}

func (s *server) updateClassificationIndex(w http.ResponseWriter, r *http.Request) {
	libraryID, actor, err := s.refreshClassificationBeforeWrite()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	indexID := strings.TrimSpace(r.PathValue("id"))
	var input struct {
		RequestID   string                     `json:"requestId"`
		BaseVersion string                     `json:"baseVersion"`
		Patch       map[string]json.RawMessage `json:"patch"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input) != nil || len(input.Patch) == 0 || len(input.Patch) > 2 {
		writeClassificationError(w, classificationError(400, "invalidIndexPatch", "索引修改内容无效"))
		return
	}
	var newName *string
	var newParent *string
	parentSet := false
	for field, raw := range input.Patch {
		switch field {
		case "name":
			var candidate string
			if json.Unmarshal(raw, &candidate) != nil {
				writeClassificationError(w, classificationError(400, "invalidIndexName", "索引名称无效"))
				return
			}
			candidate, valid := validClassificationName(candidate)
			if !valid {
				writeClassificationError(w, classificationError(400, "invalidIndexName", "索引名称无效"))
				return
			}
			newName = &candidate
		case "parentId":
			parentSet = true
			newParent, err = decodeClassificationParent(raw)
			if err != nil {
				writeClassificationError(w, err)
				return
			}
		default:
			writeClassificationError(w, classificationError(400, "invalidIndexPatch", "索引修改字段无效"))
			return
		}
	}
	requestHash := classificationSeed("index.update", indexID, input.BaseVersion, valueOrMissing(newName), parentPatchValue(parentSet, newParent))
	tx, err := s.db.Begin()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	defer tx.Rollback()
	if raw, found, requestErr := classificationRequestResult(tx, libraryID, actor, input.RequestID, requestHash); requestErr != nil {
		writeClassificationError(w, requestErr)
		return
	} else if found {
		writeStoredClassificationResult(w, 200, raw)
		return
	}
	var currentParent sql.NullString
	var currentName, nameOp, parentOp string
	var deleted int
	if err = tx.QueryRow(`SELECT parent_id,name,deleted,name_op_id,parent_op_id FROM classification_nodes WHERE library_id=? AND index_id=?`, libraryID, indexID).Scan(&currentParent, &currentName, &deleted, &nameOp, &parentOp); err == sql.ErrNoRows || deleted != 0 {
		writeClassificationError(w, classificationError(404, "indexNotFound", "索引不存在"))
		return
	} else if err != nil {
		writeClassificationError(w, err)
		return
	}
	currentVersion := classificationNodeVersion(indexID, currentParent.String, currentName, deleted, nameOp, parentOp)
	if input.BaseVersion == "" || input.BaseVersion != currentVersion {
		writeJSON(w, 409, map[string]any{"error": "索引已发生变化", "code": "indexVersionChanged", "currentVersion": currentVersion})
		return
	}
	targetName := currentName
	if newName != nil {
		targetName = *newName
	}
	targetParent := (*string)(nil)
	if currentParent.Valid {
		value := currentParent.String
		targetParent = &value
	}
	if parentSet {
		targetParent = newParent
	}
	if targetParent != nil && *targetParent == indexID {
		writeClassificationError(w, classificationError(409, "indexCycle", "索引不能成为自己的上级"))
		return
	}
	if err = classificationParentExists(tx, libraryID, targetParent); err != nil {
		writeClassificationError(w, err)
		return
	}
	if targetParent != nil {
		var cycle int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_ancestors WHERE library_id=? AND ancestor_id=? AND descendant_id=?`, libraryID, indexID, *targetParent).Scan(&cycle); err != nil {
			writeClassificationError(w, err)
			return
		}
		if cycle != 0 {
			writeClassificationError(w, classificationError(409, "indexCycle", "索引不能移动到自己的下级"))
			return
		}
	}
	if conflict, conflictErr := classificationSiblingConflict(tx, libraryID, indexID, targetParent, classificationNameKey(targetName)); conflictErr != nil {
		writeClassificationError(w, conflictErr)
		return
	} else if conflict {
		writeClassificationError(w, classificationError(409, "indexNameConflict", "同级已有同名索引"))
		return
	}
	operationID := newID()
	if _, err = recordClassificationOperation(tx, libraryID, "index.update", operationID, map[string]any{"indexId": indexID, "baseVersion": input.BaseVersion, "patch": input.Patch}); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = replayClassificationTx(tx, libraryID, actor); err != nil {
		writeClassificationError(w, err)
		return
	}
	node, err := classificationNodeForResponse(tx, libraryID, indexID)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	result := map[string]any{"node": node, "operationId": operationID}
	raw, err := storeClassificationRequest(tx, libraryID, actor, input.RequestID, requestHash, result)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeClassificationError(w, err)
		return
	}
	writeStoredClassificationResult(w, 200, raw)
}

func valueOrMissing(value *string) string {
	if value == nil {
		return "<missing>"
	}
	return *value
}

func parentPatchValue(set bool, value *string) string {
	if !set {
		return "<missing>"
	}
	return parentValue(value)
}

func (s *server) deleteClassificationIndex(w http.ResponseWriter, r *http.Request) {
	libraryID, actor, err := s.refreshClassificationBeforeWrite()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	indexID := strings.TrimSpace(r.PathValue("id"))
	requestID, baseVersion := r.URL.Query().Get("requestId"), r.URL.Query().Get("baseVersion")
	requestHash := classificationSeed("index.delete", indexID, baseVersion)
	tx, err := s.db.Begin()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	defer tx.Rollback()
	if raw, found, requestErr := classificationRequestResult(tx, libraryID, actor, requestID, requestHash); requestErr != nil {
		writeClassificationError(w, requestErr)
		return
	} else if found {
		writeStoredClassificationResult(w, 200, raw)
		return
	}
	var parent sql.NullString
	var name, nameOp, parentOp string
	var deleted int
	if err = tx.QueryRow(`SELECT parent_id,name,deleted,name_op_id,parent_op_id FROM classification_nodes WHERE library_id=? AND index_id=?`, libraryID, indexID).Scan(&parent, &name, &deleted, &nameOp, &parentOp); err == sql.ErrNoRows || deleted != 0 {
		writeClassificationError(w, classificationError(404, "indexNotFound", "索引不存在"))
		return
	} else if err != nil {
		writeClassificationError(w, err)
		return
	}
	currentVersion := classificationNodeVersion(indexID, parent.String, name, deleted, nameOp, parentOp)
	if baseVersion == "" || baseVersion != currentVersion {
		writeJSON(w, 409, map[string]any{"error": "索引已发生变化", "code": "indexVersionChanged", "currentVersion": currentVersion})
		return
	}
	var children, memberships int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0`, libraryID, indexID).Scan(&children); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND index_id=?`, libraryID, indexID).Scan(&memberships); err != nil {
		writeClassificationError(w, err)
		return
	}
	if children != 0 || memberships != 0 {
		writeClassificationError(w, classificationError(409, "indexNotEmpty", "索引仍有下级或文件归属"))
		return
	}
	operationID := newID()
	if _, err = recordClassificationOperation(tx, libraryID, "index.delete", operationID, map[string]any{"indexId": indexID, "baseVersion": baseVersion}); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = replayClassificationTx(tx, libraryID, actor); err != nil {
		writeClassificationError(w, err)
		return
	}
	result := map[string]any{"operationId": operationID}
	raw, err := storeClassificationRequest(tx, libraryID, actor, requestID, requestHash, result)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeClassificationError(w, err)
		return
	}
	writeStoredClassificationResult(w, 200, raw)
}

func uniqueSortedClassificationValues(values []string, limit int) ([]string, bool) {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 200 {
			return nil, false
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 || len(result) > limit {
		return nil, false
	}
	sort.Strings(result)
	return result, true
}

func (s *server) changeClassificationMemberships(w http.ResponseWriter, r *http.Request) {
	libraryID, actor, err := s.refreshClassificationBeforeWrite()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	var input struct {
		RequestID      string   `json:"requestId"`
		FileIDs        []string `json:"fileIds"`
		AddIndexIDs    []string `json:"addIndexIds"`
		RemoveIndexIDs []string `json:"removeIndexIds"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&input) != nil {
		writeClassificationError(w, classificationError(400, "invalidMemberships", "索引归属内容无效"))
		return
	}
	fileIDs, valid := uniqueSortedClassificationValues(input.FileIDs, 200)
	if !valid {
		writeClassificationError(w, classificationError(400, "invalidMemberships", "文件列表无效"))
		return
	}
	addIDs, addValid := normalizeOptionalClassificationValues(input.AddIndexIDs, 20)
	removeIDs, removeValid := normalizeOptionalClassificationValues(input.RemoveIndexIDs, 20)
	if !addValid || !removeValid || len(addIDs)+len(removeIDs) == 0 {
		writeClassificationError(w, classificationError(400, "invalidMemberships", "索引列表无效"))
		return
	}
	protectedFiles, protectionErr := s.cloud.protectedLibraryFileIDs()
	if protectionErr != nil {
		writeClassificationError(w, classificationError(503, "protectionUnavailable", "无法确认文件保护状态"))
		return
	}
	for _, fileID := range fileIDs {
		if protectedFiles[fileID] {
			writeClassificationError(w, classificationError(404, "fileNotFound", "文件不存在或无权整理"))
			return
		}
	}
	removeSet := map[string]bool{}
	for _, id := range removeIDs {
		removeSet[id] = true
	}
	for _, id := range addIDs {
		if removeSet[id] {
			writeClassificationError(w, classificationError(400, "invalidMemberships", "同一索引不能同时加入和移除"))
			return
		}
	}
	requestHash := classificationSeed("index.memberships", strings.Join(fileIDs, "\x00"), strings.Join(addIDs, "\x00"), strings.Join(removeIDs, "\x00"))
	tx, err := s.db.Begin()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	defer tx.Rollback()
	if raw, found, requestErr := classificationRequestResult(tx, libraryID, actor, input.RequestID, requestHash); requestErr != nil {
		writeClassificationError(w, requestErr)
		return
	} else if found {
		writeStoredClassificationResult(w, 200, raw)
		return
	}
	for _, fileID := range fileIDs {
		var exists, protected int
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM library_files f WHERE f.file_id=? AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=f.file_id)),
			EXISTS(SELECT 1 FROM library_file_folders lf JOIN folders p ON p.id=lf.folder_id WHERE lf.file_id=? AND p.password_hash<>'')`, fileID, fileID).Scan(&exists, &protected); err != nil {
			writeClassificationError(w, err)
			return
		}
		if exists == 0 || protected != 0 {
			writeClassificationError(w, classificationError(404, "fileNotFound", "文件不存在或无权整理"))
			return
		}
	}
	for _, indexID := range append(append([]string{}, addIDs...), removeIDs...) {
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, libraryID, indexID).Scan(&exists); err != nil {
			writeClassificationError(w, err)
			return
		}
		if exists == 0 {
			writeClassificationError(w, classificationError(404, "indexNotFound", "索引不存在"))
			return
		}
	}
	before := map[string]string{}
	for _, fileID := range fileIDs {
		before[fileID], err = classificationFileMembershipDigest(tx, libraryID, fileID)
		if err != nil {
			writeClassificationError(w, err)
			return
		}
	}
	operationID := newID()
	files := make([]classificationFileMembership, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		entry := classificationFileMembership{FileID: fileID}
		for _, indexID := range addIDs {
			entry.Adds = append(entry.Adds, classificationMembershipAdd{IndexID: indexID, Token: "manual:" + classificationSeed(operationID, fileID, indexID), Origin: "manual"})
		}
		for _, indexID := range removeIDs {
			// Removals target exactly the add tokens visible right now, so a
			// concurrent add from another author is never silently removed.
			rows, queryErr := tx.Query(`SELECT add_token FROM classification_adds WHERE library_id=? AND file_id=? AND index_id=? ORDER BY add_token`, libraryID, fileID, indexID)
			if queryErr != nil {
				writeClassificationError(w, queryErr)
				return
			}
			remove := classificationMembershipRemove{IndexID: indexID, ObservedTokens: []string{}}
			for rows.Next() {
				var token string
				if scanErr := rows.Scan(&token); scanErr != nil {
					rows.Close()
					writeClassificationError(w, scanErr)
					return
				}
				remove.ObservedTokens = append(remove.ObservedTokens, token)
			}
			if rowsErr := rows.Err(); rowsErr != nil {
				rows.Close()
				writeClassificationError(w, rowsErr)
				return
			}
			rows.Close()
			entry.Removes = append(entry.Removes, remove)
		}
		files = append(files, entry)
	}
	domain := map[string]any{"files": files}
	if raw, marshalErr := json.Marshal(domain); marshalErr != nil {
		writeClassificationError(w, marshalErr)
		return
	} else if len(raw) > 900<<10 {
		writeClassificationError(w, classificationError(413, "membershipPayloadTooLarge", "单次归属变更过大，请分批操作"))
		return
	}
	if _, err = recordClassificationOperation(tx, libraryID, "index.memberships", operationID, domain); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = replayClassificationTx(tx, libraryID, actor); err != nil {
		writeClassificationError(w, err)
		return
	}
	changed := 0
	state := map[string][]string{}
	for _, fileID := range fileIDs {
		after, digestErr := classificationFileMembershipDigest(tx, libraryID, fileID)
		if digestErr != nil {
			writeClassificationError(w, digestErr)
			return
		}
		if after != before[fileID] {
			changed++
		}
		state[fileID] = splitMembershipDigest(after)
	}
	result := map[string]any{"changedFiles": changed, "operationId": operationID, "state": state}
	raw, err := storeClassificationRequest(tx, libraryID, actor, input.RequestID, requestHash, result)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeClassificationError(w, err)
		return
	}
	writeStoredClassificationResult(w, 200, raw)
}

func normalizeOptionalClassificationValues(values []string, limit int) ([]string, bool) {
	if len(values) == 0 {
		return []string{}, true
	}
	return uniqueSortedClassificationValues(values, limit)
}

func classificationFileMembershipDigest(tx *sql.Tx, libraryID, fileID string) (string, error) {
	rows, err := tx.Query(`SELECT index_id FROM classification_memberships WHERE library_id=? AND file_id=? ORDER BY index_id`, libraryID, fileID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, "\x00"), rows.Err()
}

func splitMembershipDigest(digest string) []string {
	if digest == "" {
		return []string{}
	}
	return strings.Split(digest, "\x00")
}

func classificationSourceDirectoryKey(relative string) string {
	key := filepath.ToSlash(filepath.Clean(relative))
	if key == "" {
		key = "."
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func classificationRootDisplayName(rootPath string) string {
	clean := filepath.Clean(rootPath)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = filepath.VolumeName(clean)
	}
	if name == "" {
		return "Root"
	}
	return name
}

func ensureClassificationSourceDirectory(tx *sql.Tx, libraryID, nodeID, rootID, sourceKey, name string, parentID *string, segments []string) (string, bool, error) {
	var indexID string
	var disabled int
	err := tx.QueryRow(`SELECT index_id,disabled FROM classification_source_dirs WHERE library_id=? AND node_id=? AND root_id=? AND source_dir_key=?`, libraryID, nodeID, rootID, sourceKey).Scan(&indexID, &disabled)
	if err == nil {
		var deleted int
		if nodeErr := tx.QueryRow(`SELECT deleted FROM classification_nodes WHERE library_id=? AND index_id=?`, libraryID, indexID).Scan(&deleted); nodeErr != nil && nodeErr != sql.ErrNoRows {
			return "", false, nodeErr
		} else if disabled != 0 || nodeErr == sql.ErrNoRows || deleted != 0 {
			return "", false, nil
		}
		return indexID, true, nil
	}
	if err != sql.ErrNoRows {
		return "", false, err
	}
	indexID = "source-index:" + classificationSeed(libraryID, nodeID, rootID, sourceKey)
	domain := map[string]any{"indexId": indexID, "name": name, "origin": "automatic"}
	if parentID != nil {
		domain["parentId"] = *parentID
	}
	if _, err = recordClassificationOperation(tx, libraryID, "index.create", newID(), domain); err != nil {
		return "", false, err
	}
	rawSegments, err := json.Marshal(segments)
	if err != nil {
		return "", false, err
	}
	if _, err = tx.Exec(`INSERT INTO classification_source_dirs(library_id,node_id,root_id,source_dir_key,index_id,display_segments_json,disabled) VALUES(?,?,?,?,?,?,0) ON CONFLICT DO NOTHING`, libraryID, nodeID, rootID, sourceKey, indexID, string(rawSegments)); err != nil {
		return "", false, err
	}
	return indexID, true, nil
}

// classifyScannedResource records the initial directory-derived membership in
// the same transaction as its per-entry progress marker. A later scan observes
// the marker and never resurrects a membership the user removed.
func (s *server) classifyScannedResource(libraryID, nodeID, rootID, rootPath, filePath, resourceID string) error {
	relative, err := filepath.Rel(rootPath, filePath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("scanned file escaped its authorized root")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var processed int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_imports WHERE library_id=? AND node_id=? AND root_id=? AND source_entry_id=?`, libraryID, nodeID, rootID, resourceID).Scan(&processed); err != nil {
		return err
	}
	if processed != 0 {
		return tx.Commit()
	}
	rootName := classificationRootDisplayName(rootPath)
	segments := []string{rootName}
	rootIndex, active, err := ensureClassificationSourceDirectory(tx, libraryID, nodeID, rootID, ".", rootName, nil, segments)
	if err != nil {
		return err
	}
	currentIndex := rootIndex
	parent := &currentIndex
	directory := filepath.Dir(relative)
	if directory != "." && directory != "" {
		partial := ""
		for _, segment := range strings.FieldsFunc(filepath.ToSlash(directory), func(current rune) bool { return current == '/' }) {
			if partial == "" {
				partial = segment
			} else {
				partial += "/" + segment
			}
			segments = append(segments, segment)
			if !active {
				break
			}
			currentIndex, active, err = ensureClassificationSourceDirectory(tx, libraryID, nodeID, rootID, classificationSourceDirectoryKey(partial), segment, parent, segments)
			if err != nil {
				return err
			}
			parent = &currentIndex
		}
	}
	state := "skipped"
	if active {
		var fileID string
		if err = tx.QueryRow(`SELECT file_id FROM library_resource_aliases WHERE resource_id=?`, resourceID).Scan(&fileID); err != nil {
			return err
		}
		token := "automatic:" + classificationSeed(libraryID, nodeID, rootID, resourceID, currentIndex)
		domain := map[string]any{"files": []classificationFileMembership{{FileID: fileID, Adds: []classificationMembershipAdd{{IndexID: currentIndex, Token: token, Origin: "automatic"}}}}}
		if _, err = recordClassificationOperation(tx, libraryID, "index.memberships", newID(), domain); err != nil {
			return err
		}
		state = "processed"
	}
	if _, err = tx.Exec(`INSERT INTO classification_imports(library_id,node_id,root_id,source_entry_id,state,index_id,updated_at) VALUES(?,?,?,?,?,?,?)`, libraryID, nodeID, rootID, resourceID, state, currentIndex, now()); err != nil {
		return err
	}
	if err = replayClassificationTx(tx, libraryID, nodeID); err != nil {
		return err
	}
	return tx.Commit()
}
