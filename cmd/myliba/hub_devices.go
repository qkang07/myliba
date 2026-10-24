package main

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type hubDeviceView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	CanBrowse bool      `json:"canBrowse"`
	CanServe  bool      `json:"canServe"`
	CanCloud  bool      `json:"canCloud"`
	CreatedAt time.Time `json:"createdAt"`
	RevokedAt time.Time `json:"revokedAt,omitempty"`
}

func (h *remoteHub) authorizedNode(r *http.Request, id, kind, legacyToken string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authorizedNodeLocked(r, id, kind, legacyToken)
}

// authorizedNodeLocked must be called with h.mu held. This lets attachment
// commit under the same lock used by device revocation.
func (h *remoteHub) authorizedNodeLocked(r *http.Request, id, kind, legacyToken string) bool {
	if h.auth == nil {
		return validBearer(r, legacyToken)
	}
	if device, exists := h.auth.data.Devices[id]; exists {
		return device.RevokedAt.IsZero() && device.CanServe && device.Kind == kind &&
			safeEqual(device.TokenHash, hubAuthKey(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))) &&
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return h.auth.data.LegacyNodeAllowed && validBearer(r, legacyToken)
}

func (h *remoteHub) requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.hasSession(r) {
			writeError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r, h.secure) {
			writeError(w, http.StatusForbidden, "请求来源无效")
			return
		}
		cookie, _ := r.Cookie(hubSessionCookie)
		h.mu.Lock()
		owner := h.auth == nil
		if h.auth != nil && cookie != nil {
			session, exists := h.auth.data.Sessions[hubAuthKey(cookie.Value)]
			owner = exists && time.Now().Before(session.Expires) && session.DeviceID == ""
		}
		h.mu.Unlock()
		if !owner {
			writeError(w, http.StatusForbidden, "只有主人可以管理设备")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *remoteHub) pairDevice(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "设备授权不可用")
		return
	}
	var input struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		CanBrowse bool   `json:"canBrowse"`
		CanServe  bool   `json:"canServe"`
		CanCloud  bool   `json:"canCloud"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil ||
		!validRemoteNodeID(input.ID) || len([]rune(strings.TrimSpace(input.Name))) == 0 || len([]rune(input.Name)) > 80 ||
		(input.Kind != "android" && input.Kind != "desktop" && input.Kind != "storage") ||
		(!input.CanBrowse && !input.CanServe && !input.CanCloud) ||
		(input.Kind == "storage" && (input.CanBrowse || input.CanServe || !input.CanCloud)) {
		writeError(w, http.StatusBadRequest, "设备信息无效")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建设备凭据")
		return
	}
	now := time.Now().UTC()
	device := hubAuthDevice{ID: input.ID, Name: strings.TrimSpace(input.Name), Kind: input.Kind, TokenHash: hubAuthKey(token), CanBrowse: input.CanBrowse, CanServe: input.CanServe, CanCloud: input.CanCloud, CreatedAt: now}
	h.mu.Lock()
	err = h.auth.update(func(data *hubAuthData) error {
		data.Devices[device.ID] = device
		for key, session := range data.Sessions {
			if session.DeviceID == device.ID {
				delete(data.Sessions, key)
			}
		}
		return nil
	})
	if err == nil {
		for key, grant := range h.cloudGrants {
			if grant.DeviceID == device.ID {
				delete(h.cloudGrants, key)
			}
		}
	}
	node := h.nodes[device.ID]
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存设备授权")
		return
	}
	if node != nil && node.Conn != nil {
		_ = node.Conn.Close(1008, "device credentials rotated")
	}
	writeJSON(w, http.StatusCreated, map[string]any{"device": hubDeviceView{ID: device.ID, Name: device.Name, Kind: device.Kind, CanBrowse: device.CanBrowse, CanServe: device.CanServe, CanCloud: device.CanCloud, CreatedAt: device.CreatedAt}, "token": token})
}

func (h *remoteHub) deviceLogin(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "设备授权不可用")
		return
	}
	if !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	var input struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validRemoteNodeID(input.ID) || len(input.Token) < 32 {
		writeError(w, http.StatusBadRequest, "设备凭据无效")
		return
	}
	h.issueSession(w, r, input.ID, input.Token)
}

func (h *remoteHub) deviceLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, h.secure) || !h.hasSession(r) {
		writeError(w, http.StatusUnauthorized, "设备会话无效")
		return
	}
	cookie, _ := r.Cookie(hubSessionCookie)
	h.mu.Lock()
	deviceID := h.auth.data.Sessions[hubAuthKey(cookie.Value)].DeviceID
	h.mu.Unlock()
	if deviceID == "" {
		writeError(w, http.StatusForbidden, "当前不是设备会话")
		return
	}
	r.SetPathValue("id", deviceID)
	h.revokeDevice(w, r)
}

func (h *remoteHub) listDevices(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	devices := make([]hubDeviceView, 0, len(h.auth.data.Devices))
	for _, d := range h.auth.data.Devices {
		devices = append(devices, hubDeviceView{ID: d.ID, Name: d.Name, Kind: d.Kind, CanBrowse: d.CanBrowse, CanServe: d.CanServe, CanCloud: d.CanCloud, CreatedAt: d.CreatedAt, RevokedAt: d.RevokedAt})
	}
	legacy := h.auth.data.LegacyNodeAllowed
	h.mu.Unlock()
	sort.Slice(devices, func(i, j int) bool { return devices[i].CreatedAt.Before(devices[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices, "legacyNodeAllowed": legacy})
}

func (h *remoteHub) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validRemoteNodeID(id) {
		writeError(w, http.StatusBadRequest, "设备 ID 无效")
		return
	}
	h.mu.Lock()
	device, exists := h.auth.data.Devices[id]
	if !exists || !device.RevokedAt.IsZero() {
		h.mu.Unlock()
		writeError(w, http.StatusNotFound, "设备不存在或已撤销")
		return
	}
	err := h.auth.update(func(data *hubAuthData) error {
		device := data.Devices[id]
		device.RevokedAt = time.Now().UTC()
		data.Devices[id] = device
		for key, session := range data.Sessions {
			if session.DeviceID == id {
				delete(data.Sessions, key)
			}
		}
		return nil
	})
	if err == nil {
		for key, grant := range h.cloudGrants {
			if grant.DeviceID == id {
				delete(h.cloudGrants, key)
			}
		}
	}
	node := h.nodes[id]
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法撤销设备")
		return
	}
	if node != nil && node.Conn != nil {
		_ = node.Conn.Close(1008, "device revoked")
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *remoteHub) setLegacyNodes(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "设置格式错误")
		return
	}
	if input.Enabled && len(strings.TrimSpace(os.Getenv("MYLIBA_NODE_TOKEN"))) < 32 {
		writeError(w, http.StatusConflict, "Hub 未配置旧共享节点密钥")
		return
	}
	h.mu.Lock()
	err := h.auth.update(func(data *hubAuthData) error { data.LegacyNodeAllowed = input.Enabled; return nil })
	var disconnect []*websocket.Conn
	if err == nil && !input.Enabled {
		for id, node := range h.nodes {
			if _, paired := h.auth.data.Devices[id]; !paired && node.Conn != nil {
				disconnect = append(disconnect, node.Conn)
			}
		}
	}
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存旧密钥设置")
		return
	}
	for _, conn := range disconnect {
		_ = conn.Close(websocket.StatusPolicyViolation, "legacy node credentials disabled")
	}
	h.listDevices(w, r)
}
