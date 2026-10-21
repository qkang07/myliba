package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func validShareID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, char := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if char != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

// validateShare asks the home node, which owns the share database, to check a
// visitor's capability. The Hub never accepts a share ID alone as authority.
func (h *remoteHub) validateShare(ctx context.Context, shareID, token string) (json.RawMessage, int, error) {
	if !validShareID(shareID) || len(token) != 64 {
		return nil, http.StatusUnauthorized, nil
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	h.mu.Lock()
	if h.shareChecks >= 16 {
		h.mu.Unlock()
		return nil, http.StatusTooManyRequests, nil
	}
	conn := h.conn
	if conn == nil {
		h.mu.Unlock()
		return nil, http.StatusServiceUnavailable, nil
	}
	id := newID()
	waiter := &remoteWaiter{channel: make(chan wireMessage, 8), done: make(chan struct{})}
	h.waiters[id] = waiter
	h.shareChecks++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiters, id)
		h.shareChecks--
		h.mu.Unlock()
		waiter.finish()
	}()
	if err := h.send(conn, wireMessage{Type: "request", ID: id, Method: http.MethodPost, Path: "/api/shares/" + shareID + "/redeem", Body: body}); err != nil {
		return nil, http.StatusBadGateway, err
	}
	status := 0
	var response bytes.Buffer
	for {
		select {
		case message := <-waiter.channel:
			switch message.Type {
			case "start":
				status = message.Status
			case "chunk":
				if response.Len()+len(message.Data) > 8<<10 {
					return nil, http.StatusBadGateway, errors.New("share response too large")
				}
				_, _ = response.Write(message.Data)
			case "end":
				if status == 0 {
					return nil, http.StatusBadGateway, errors.New("missing share response")
				}
				return response.Bytes(), status, nil
			}
		case <-waiter.done:
			return nil, http.StatusServiceUnavailable, errors.New("node disconnected")
		case <-ctx.Done():
			return nil, http.StatusGatewayTimeout, ctx.Err()
		}
	}
}

func (h *remoteHub) shareRedeem(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, h.secure) {
		writeError(w, http.StatusForbidden, "请求来源无效")
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "分享密钥格式错误")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	metadata, status, err := h.validateShare(ctx, r.PathValue("id"), input.Token)
	if err != nil {
		writeError(w, status, "无法联系文件节点")
		return
	}
	if status != http.StatusOK {
		writeError(w, status, "分享不可用、已过期或密钥无效")
		return
	}
	var result map[string]any
	if err := json.Unmarshal(metadata, &result); err != nil {
		writeError(w, http.StatusBadGateway, "文件节点响应无效")
		return
	}
	result["iceServers"] = h.directICEServers()
	writeJSON(w, http.StatusOK, result)
}

func (h *remoteHub) directICEServers() []iceServerConfig {
	servers := make([]iceServerConfig, 0, len(h.iceServers))
	for _, server := range h.iceServers {
		urls := make([]string, 0, len(server.URLs))
		for _, endpoint := range server.URLs {
			if strings.HasPrefix(strings.ToLower(endpoint), "stun:") || strings.HasPrefix(strings.ToLower(endpoint), "stuns:") {
				urls = append(urls, endpoint)
			}
		}
		if len(urls) > 0 {
			servers = append(servers, iceServerConfig{URLs: urls})
		}
	}
	return servers
}

func (h *remoteHub) shareSignal(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(r, h.secure) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "share signaling ended")
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	_, first, err := conn.Read(ctx)
	cancel()
	if err != nil {
		return
	}
	var auth struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if json.Unmarshal(first, &auth) != nil || auth.Type != "authorize" {
		_ = conn.Close(websocket.StatusPolicyViolation, "share authorization required")
		return
	}
	ctx, cancel = context.WithTimeout(r.Context(), 10*time.Second)
	_, status, err := h.validateShare(ctx, auth.ID, auth.Token)
	cancel()
	if err != nil || status != http.StatusOK {
		_ = conn.Close(websocket.StatusPolicyViolation, "share authorization failed")
		return
	}
	h.mu.Lock()
	node := h.conn
	h.mu.Unlock()
	if node == nil {
		_ = conn.Close(websocket.StatusTryAgainLater, "node offline")
		return
	}
	sessionID := newID()
	h.signalMu.Lock()
	h.signalSessions[sessionID] = conn
	h.signalMu.Unlock()
	defer func() {
		h.signalMu.Lock()
		delete(h.signalSessions, sessionID)
		h.signalMu.Unlock()
		payload, _ := json.Marshal(rtcSignal{Type: "close"})
		_ = h.send(node, wireMessage{Type: "rtc-signal", SessionID: sessionID, ShareID: auth.ID, Signal: payload})
	}()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"authorized"}`)); err != nil {
		return
	}
	for {
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var signal rtcSignal
		if json.Unmarshal(payload, &signal) != nil {
			continue
		}
		switch signal.Type {
		case "offer":
			if signal.Description == nil || signal.Description.Type != webrtc.SDPTypeOffer {
				continue
			}
		case "candidate":
			if signal.Candidate == nil {
				continue
			}
		case "close":
		default:
			continue
		}
		if err := h.send(node, wireMessage{Type: "rtc-signal", SessionID: sessionID, ShareID: auth.ID, Signal: payload}); err != nil {
			return
		}
	}
}

func shareIndex(static http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validShareID(r.PathValue("id")) {
			http.NotFound(w, r)
			return
		}
		file, err := static.Open("/index.html")
		if err != nil {
			http.Error(w, "share page unavailable", http.StatusInternalServerError)
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, file)
	}
}
