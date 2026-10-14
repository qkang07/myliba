package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

type iceServerConfig struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type rtcSignal struct {
	Type        string                     `json:"type"`
	Description *webrtc.SessionDescription `json:"description,omitempty"`
	Candidate   *webrtc.ICECandidateInit   `json:"candidate,omitempty"`
}

func loadICEServers() ([]iceServerConfig, error) {
	raw := strings.TrimSpace(os.Getenv("MYLIBA_ICE_SERVERS"))
	if raw == "" {
		return []iceServerConfig{{URLs: []string{"stun:stun.l.google.com:19302"}}}, nil
	}
	var servers []iceServerConfig
	if err := json.Unmarshal([]byte(raw), &servers); err != nil || len(servers) == 0 {
		return nil, errors.New("MYLIBA_ICE_SERVERS must be a non-empty JSON array of ICE server objects")
	}
	for _, server := range servers {
		if len(server.URLs) == 0 {
			return nil, errors.New("each MYLIBA_ICE_SERVERS entry needs at least one URL")
		}
	}
	return servers, nil
}

func pionICEConfiguration(servers []iceServerConfig) webrtc.Configuration {
	result := webrtc.Configuration{ICEServers: make([]webrtc.ICEServer, 0, len(servers))}
	for _, server := range servers {
		result.ICEServers = append(result.ICEServers, webrtc.ICEServer{
			URLs: server.URLs, Username: server.Username, Credential: server.Credential,
			CredentialType: webrtc.ICECredentialTypePassword,
		})
	}
	return result
}

func (h *remoteHub) signal(w http.ResponseWriter, r *http.Request) {
	if !h.hasSession(r) || !sameOrigin(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(1 << 20)
	h.mu.Lock()
	node := h.conn
	h.mu.Unlock()
	if node == nil {
		_ = conn.Close(websocket.StatusTryAgainLater, "remote node is offline")
		return
	}
	sessionID := newID()
	h.signalMu.Lock()
	if h.signalSessions == nil {
		h.signalSessions = map[string]*websocket.Conn{}
	}
	h.signalSessions[sessionID] = conn
	h.signalMu.Unlock()
	defer func() {
		h.signalMu.Lock()
		delete(h.signalSessions, sessionID)
		h.signalMu.Unlock()
		payload, _ := json.Marshal(rtcSignal{Type: "close"})
		_ = h.send(node, wireMessage{Type: "rtc-signal", SessionID: sessionID, Signal: payload})
		_ = conn.Close(websocket.StatusNormalClosure, "signaling session ended")
	}()

	for {
		_, payload, err := conn.Read(context.Background())
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
		if err := h.send(node, wireMessage{Type: "rtc-signal", SessionID: sessionID, Signal: payload}); err != nil {
			return
		}
	}
}

func (h *remoteHub) forwardRTCSignal(sessionID string, payload json.RawMessage) {
	h.signalMu.Lock()
	conn := h.signalSessions[sessionID]
	h.signalMu.Unlock()
	if conn == nil {
		return
	}
	ctx := context.Background()
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		_ = conn.CloseNow()
	}
}
