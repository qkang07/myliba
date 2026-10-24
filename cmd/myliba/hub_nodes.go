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
		id := h.selectedNodes[cookie.Value]
		if h.auth != nil {
			id = h.auth.data.Sessions[hubAuthKey(cookie.Value)].SelectedNode
		}
		if id != "" {
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
		if h.auth != nil {
			err = h.auth.update(func(data *hubAuthData) error {
				session := data.Sessions[hubAuthKey(cookie.Value)]
				session.SelectedNode = input.ID
				data.Sessions[hubAuthKey(cookie.Value)] = session
				return nil
			})
		} else {
			h.selectedNodes[cookie.Value] = input.ID
		}
	}
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存当前节点")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	h.listNodes(w, r)
}

func (h *remoteHub) removeOfflineNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRemoteNodeID(id) {
		writeError(w, http.StatusBadRequest, "节点 ID 无效")
		return
	}
	h.mu.Lock()
	node := h.nodes[id]
	if node == nil {
		h.mu.Unlock()
		writeError(w, http.StatusNotFound, "节点不存在")
		return
	}
	if node.Conn != nil {
		h.mu.Unlock()
		writeError(w, http.StatusConflict, "节点仍在线，不能移除")
		return
	}
	if h.auth != nil {
		if err := h.auth.update(func(data *hubAuthData) error {
			for key, session := range data.Sessions {
				if session.SelectedNode == id {
					session.SelectedNode = ""
					data.Sessions[key] = session
				}
			}
			return nil
		}); err != nil {
			h.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "无法清除节点选择")
			return
		}
	}
	delete(h.nodes, id)
	for session, selected := range h.selectedNodes {
		if selected == id {
			delete(h.selectedNodes, session)
		}
	}
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
