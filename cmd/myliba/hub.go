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
	if len(nodeToken) < 32 {
		return errors.New("MYLIBA_NODE_TOKEN must be at least 32 characters")
	}
	addr := strings.TrimSpace(os.Getenv("MYLIBA_HUB_LISTEN"))
	if addr == "" {
		addr = "127.0.0.1:4318"
	}
	static, err := fs.Sub(assets, "web/dist")
	if err != nil {
		return err
	}
	hub := &remoteHub{waiters: map[string]*remoteWaiter{}, password: password, secure: os.Getenv("MYLIBA_HUB_COOKIE_SECURE") != "0", sessions: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /node", func(w http.ResponseWriter, r *http.Request) {
		if !validBearer(r, nodeToken) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn.SetReadLimit(4 << 20)
		hub.attach(conn)
	})
	mux.HandleFunc("GET /api/session", hub.sessionStatus)
	mux.HandleFunc("POST /api/session", hub.login)
	mux.HandleFunc("DELETE /api/session", hub.logout)
	mux.Handle("GET /api/webrtc/config", hub.requireSession(http.HandlerFunc(hub.webrtcConfig)))
	mux.Handle("/api/", hub.requireSession(http.HandlerFunc(hub.proxy)))
	mux.Handle("/", http.FileServer(http.FS(static)))
	server := &http.Server{Addr: addr, Handler: hubHeaders(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
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
	online := h.online
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "nodeOnline": online})
}

func (h *remoteHub) webrtcConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"iceServers": configuredICEServers()})
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
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法创建登录会话")
		return
	}
	h.mu.Lock()
	h.sessions[token] = time.Now().Add(30 * 24 * time.Hour)
	online := h.online
	h.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: hubSessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(30 * 24 * time.Hour)})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "nodeOnline": online})
}

func (h *remoteHub) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	if cookie, err := r.Cookie(hubSessionCookie); err == nil {
		h.mu.Lock()
		delete(h.sessions, cookie.Value)
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
	expires, ok := h.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(h.sessions, cookie.Value)
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
