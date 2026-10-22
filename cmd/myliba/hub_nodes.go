package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/coder/websocket"
)

type hubNodeView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Online bool   `json:"online"`
}

func validRemoteNodeID(id string) bool {
	if len(id) < 1 || len(id) > 80 {
		return false
	}
	for _, ch := range id {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", ch) {
			return false
		}
	}
	return true
}

// The selected node belongs to the signed-in browser or app session. Keeping
// this choice on the Hub also binds HTTP fallback and WebRTC signaling to the
// same node without accepting a caller-supplied routing header on each request.
func (h *remoteHub) selectedIDLocked(r *http.Request) string {
	if cookie, err := r.Cookie(hubSessionCookie); err == nil {
		if id := h.selectedNodes[cookie.Value]; id != "" {
			return id
		}
	}
	if node := h.nodes["legacy"]; node != nil && node.Conn != nil {
		return "legacy"
	}
	ids := make([]string, 0, len(h.nodes))
	for id, node := range h.nodes {
		if node.Conn != nil && node.Kind == "desktop" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		for id, node := range h.nodes {
			if node.Conn != nil {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func (h *remoteHub) selectedNodeLocked(r *http.Request) *websocket.Conn {
	if node := h.nodes[h.selectedIDLocked(r)]; node != nil {
		return node.Conn
	}
	return nil
}

func (h *remoteHub) nodesViewLocked() []hubNodeView {
	result := make([]hubNodeView, 0, len(h.nodes))
	for _, node := range h.nodes {
		result = append(result, hubNodeView{ID: node.ID, Name: node.Name, Kind: node.Kind, Online: node.Conn != nil})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Online != result[j].Online {
			return result[i].Online
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind == "desktop"
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func (h *remoteHub) listNodes(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	nodes, selectedID := h.nodesViewLocked(), h.selectedIDLocked(r)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "selectedId": selectedID})
}

func (h *remoteHub) selectNode(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil || !validRemoteNodeID(input.ID) {
		writeError(w, http.StatusBadRequest, "节点 ID 无效")
		return
	}
	cookie, err := r.Cookie(hubSessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	h.mu.Lock()
	_, exists := h.nodes[input.ID]
	if exists {
		h.selectedNodes[cookie.Value] = input.ID
	}
	h.mu.Unlock()
	if !exists {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	h.listNodes(w, r)
}
