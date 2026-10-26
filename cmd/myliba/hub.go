package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const hubSessionCookie = "myliba_hub_session"

func runHub() error {
	password := os.Getenv("MYLIBA_WEB_PASSWORD")
	nodeToken := os.Getenv("MYLIBA_NODE_TOKEN")
	if len(password) < 16 {
		return errors.New("MYLIBA_WEB_PASSWORD must be at least 16 characters")
	}
	auth, err := newHubAuthStore(password)
	if err != nil {
		return err
	}
	if (nodeToken != "" && len(nodeToken) < 32) || (auth.data.LegacyNodeAllowed && len(nodeToken) < 32) {
		return errors.New("MYLIBA_NODE_TOKEN must be at least 32 characters while legacy nodes are enabled")
	}
	addr := strings.TrimSpace(os.Getenv("MYLIBA_HUB_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:4318"
	}
	static, err := fs.Sub(assets, "web/dist")
	if err != nil {
		return err
	}
	iceServers, err := loadICEServers()
	if err != nil {
		return err
	}
	relayBps, err := hubRelayBytesPerSecond()
	if err != nil {
		return err
	}
	hub := &remoteHub{waiters: map[string]*remoteWaiter{}, nodes: map[string]*remoteNode{}, selectedNodes: map[string]string{}, password: password, secure: os.Getenv("MYLIBA_HUB_COOKIE_SECURE") != "0", sessions: map[string]time.Time{}, auth: auth, iceServers: iceServers, relayLimiter: relayRateLimiter{bps: relayBps}, relaySlots: make(chan struct{}, 8), signalSessions: map[string]signalSession{}}
	mux := http.NewServeMux()
	cloud, err := newCloudHub()
	if err != nil {
		return err
	}
	cloud.routesWithAuth(mux, hub.cloudBearer)
	mux.HandleFunc("POST /cloud/auth/grant", hub.issueCloudGrant)
	mux.HandleFunc("POST /cloud/auth/verify", hub.verifyCloudPeer)
	mux.HandleFunc("GET /node", func(w http.ResponseWriter, r *http.Request) {
		id, name, kind := r.URL.Query().Get("id"), strings.TrimSpace(r.URL.Query().Get("name")), r.URL.Query().Get("kind")
		if id == "" {
			id, name, kind = "legacy", "家中客户端", "desktop"
		}
		if !validRemoteNodeID(id) || len([]rune(name)) > 80 || (kind != "desktop" && kind != "android") {
			http.Error(w, "invalid node identity", http.StatusBadRequest)
			return
		}
		// Registered devices must prove their own credential. Unregistered legacy
		// nodes retain the existing shared bearer until the owner disables it.
		if !hub.authorizedNode(r, id, kind, nodeToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if name == "" {
			name = id
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(4 << 20)
		if !hub.attachNodeChecked(conn, id, name, kind, r, nodeToken) {
			_ = conn.Close(websocket.StatusPolicyViolation, "device authorization revoked")
		}
	})
	mux.HandleFunc("GET /signal", hub.signal)
	mux.HandleFunc("GET /share/signal", hub.shareSignal)
	mux.HandleFunc("POST /api/shares/{id}/redeem", hub.shareRedeem)
	mux.HandleFunc("GET /s/{id}", shareIndex(http.FS(static)))
	mux.HandleFunc("GET /api/session", hub.sessionStatus)
	mux.HandleFunc("POST /api/session", hub.login)
	mux.HandleFunc("POST /api/session/device", hub.deviceLogin)
	mux.HandleFunc("DELETE /api/session/device", hub.deviceLogout)
	mux.HandleFunc("DELETE /api/session", hub.logout)
	mux.Handle("POST /api/hub/devices", hub.requireOwner(http.HandlerFunc(hub.pairDevice)))
	mux.Handle("GET /api/hub/devices", hub.requireOwner(http.HandlerFunc(hub.listDevices)))
	mux.Handle("DELETE /api/hub/devices/{id}", hub.requireOwner(http.HandlerFunc(hub.revokeDevice)))
	mux.Handle("PUT /api/hub/devices/legacy", hub.requireOwner(http.HandlerFunc(hub.setLegacyNodes)))
	mux.Handle("GET /api/hub/nodes", hub.requireSession(http.HandlerFunc(hub.listNodes)))
	mux.Handle("PUT /api/hub/nodes/active", hub.requireSession(http.HandlerFunc(hub.selectNode)))
	mux.Handle("DELETE /api/hub/nodes/{id}", hub.requireOwner(http.HandlerFunc(hub.removeOfflineNode)))
	mux.Handle("GET /api/hub/cloud/overview", hub.requireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cloud == nil {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "nodes": []cloudNode{}})
			return
		}
		cloud.overview(w, r)
	})))
	mux.Handle("DELETE /api/hub/cloud/nodes/{id}", hub.requireOwner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cloud == nil {
			writeError(w, http.StatusNotFound, "云层未启用")
			return
		}
		cloud.removeOfflineNode(w, r)
	})))
	mux.Handle("/api/", hub.requireSession(http.HandlerFunc(hub.proxy)))
	mux.Handle("/", http.FileServer(http.FS(static)))
	server := &http.Server{Addr: addr, Handler: hubHeaders(hub.trackWebTraffic(mux)), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("Myliba remote hub listening on %s; put it behind an HTTPS reverse proxy", addr)
	return server.ListenAndServe()
}

func validBearer(r *http.Request, token string) bool {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return false
	}
	return safeEqual(strings.TrimPrefix(value, "Bearer "), token)
}

func (h *remoteHub) sessionStatus(w http.ResponseWriter, r *http.Request) {
	if !h.hasSession(r) {
		writeError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	h.mu.Lock()
	selectedID := h.selectedIDLocked(r)
	owner := true
	if h.auth != nil {
		cookie, _ := r.Cookie(hubSessionCookie)
		owner = h.auth.data.Sessions[hubAuthKey(cookie.Value)].DeviceID == ""
	}
	node := h.nodes[selectedID]
	online := node != nil && node.Conn != nil
	nodes := h.nodesViewLocked()
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "owner": owner, "nodeOnline": online, "selectedNodeId": selectedID, "nodes": nodes, "webTraffic": h.webTraffic.rate(), "nodeTraffic": h.nodeTraffic.rate(), "iceServers": h.iceServers})
}

func (h *remoteHub) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	h.mu.Lock()
	blockedUntil := h.loginBlockedUntil
	h.mu.Unlock()
	if time.Now().Before(blockedUntil) {
		writeError(w, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input)
	if err != nil || !safeEqual(input.Password, h.password) {
		h.mu.Lock()
		h.loginTries++
		if h.loginTries >= 5 {
			h.loginTries = 0
			h.loginBlockedUntil = time.Now().Add(time.Minute)
		}
		h.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "密码不正确")
		return
	}
	h.mu.Lock()
	h.loginTries = 0
	h.loginBlockedUntil = time.Time{}
	h.mu.Unlock()
	h.issueSession(w, r, "", "")
}

func (h *remoteHub) issueSession(w http.ResponseWriter, r *http.Request, deviceID, deviceToken string) {
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}
	h.mu.Lock()
	if deviceID != "" {
		device, exists := h.auth.data.Devices[deviceID]
		if !exists || !device.RevokedAt.IsZero() || !device.CanBrowse || !safeEqual(device.TokenHash, hubAuthKey(deviceToken)) {
			h.mu.Unlock()
			writeError(w, http.StatusUnauthorized, "设备未获授权或已撤销")
			return
		}
	}
	expires := time.Now().Add(30 * 24 * time.Hour)
	selectedID := h.selectedIDLocked(r)
	if h.auth != nil {
		if err := h.auth.update(func(data *hubAuthData) error {
			data.Sessions[hubAuthKey(token)] = hubAuthSession{DeviceID: deviceID, SelectedNode: selectedID, Expires: expires}
			return nil
		}); err != nil {
			h.mu.Unlock()
			writeError(w, http.StatusInternalServerError, "无法保存登录会话")
			return
		}
	} else {
		h.sessions[token] = expires
		h.selectedNodes[token] = selectedID
	}
	node := h.nodes[selectedID]
	online := node != nil && node.Conn != nil
	nodes := h.nodesViewLocked()
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: hubSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(30 * 24 * time.Hour)})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "owner": deviceID == "", "nodeOnline": online, "selectedNodeId": selectedID, "nodes": nodes, "iceServers": h.iceServers})
}

func (h *remoteHub) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if cookie, err := r.Cookie(hubSessionCookie); err == nil {
		h.mu.Lock()
		if h.auth != nil {
			if err := h.auth.update(func(data *hubAuthData) error {
				delete(data.Sessions, hubAuthKey(cookie.Value))
				return nil
			}); err != nil {
				h.mu.Unlock()
				writeError(w, http.StatusInternalServerError, "无法撤销登录会话")
				return
			}
		} else {
			delete(h.sessions, cookie.Value)
			delete(h.selectedNodes, cookie.Value)
		}
		h.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: hubSessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (h *remoteHub) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.hasSession(r) {
			writeError(w, http.StatusUnauthorized, "请先登录")
			return
		}
		if r.Method != http.MethodGet && !sameOrigin(r, h.secure) {
			writeError(w, http.StatusForbidden, "请求来源无效")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *remoteHub) hasSession(r *http.Request) bool {
	cookie, err := r.Cookie(hubSessionCookie)
	if err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.auth != nil {
		session, ok := h.auth.data.Sessions[hubAuthKey(cookie.Value)]
		if !ok || time.Now().After(session.Expires) {
			return false
		}
		if session.DeviceID != "" {
			device, found := h.auth.data.Devices[session.DeviceID]
			return found && device.RevokedAt.IsZero() && device.CanBrowse
		}
		return true
	}
	expires, ok := h.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(h.sessions, cookie.Value)
		delete(h.selectedNodes, cookie.Value)
		return false
	}
	return true
}

func sameOrigin(r *http.Request, secure bool) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	if secure {
		return parsed.Scheme == "https"
	}
	return parsed.Scheme == "http"
}

func hubHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' stun: stuns: turn: turns:; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; webrtc 'allow'")
		next.ServeHTTP(w, r)
	})
}
