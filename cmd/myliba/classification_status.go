package main

import (
	"context"
	"net/http"
	"time"
)

// classificationStatus reports the always-on new classification model. It is
// deliberately read-only: there is no legacy upgrade, prepare, or cancel
// action in the product flow.
func (s *server) classificationStatus(w http.ResponseWriter, r *http.Request) {
	libraryID, nodeID, err := s.classificationIdentity()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "索引尚未初始化")
		return
	}
	var nodes, memberships, pending int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND deleted=0`, libraryID).Scan(&nodes); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分类索引状态")
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=?`, libraryID).Scan(&memberships); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分类索引状态")
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed') AND kind LIKE 'index.%'`).Scan(&pending); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分类索引状态")
		return
	}
	payload := map[string]any{
		"libraryId":         libraryID,
		"nodeId":            nodeID,
		"mode":              classificationMode(s.db, libraryID),
		"catalogFormat":     s.cloud.catalogWriterFormat(),
		"nodeCount":         nodes,
		"membershipCount":   memberships,
		"pendingOperations": pending,
	}
	hub := map[string]any{"connected": false}
	if s.cloud != nil && s.cloud.runtime != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if caps, capsErr := s.cloud.hubClassificationCapabilities(ctx); capsErr == nil {
			hub = map[string]any{
				"connected":              true,
				"classificationMode":     caps.ClassificationMode,
				"maxCatalogFormat":       caps.MaxCatalogFormat,
				"libraryMinWriterFormat": caps.LibraryMinWriterFormat,
			}
		}
	}
	payload["hub"] = hub
	writeJSON(w, http.StatusOK, payload)
}
