package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

const rtcChunkSize = 24 << 10

type nodeRTCManager struct {
	mu       sync.Mutex
	sessions map[string]*nodeRTCSession
	config   webrtc.Configuration
	conn     *websocket.Conn
	writeMu  *sync.Mutex
}

type nodeRTCSession struct {
	shareID    string
	peer       *webrtc.PeerConnection
	channel    *webrtc.DataChannel
	signalMu   sync.Mutex
	dataSendMu sync.Mutex
	requestMu  sync.Mutex
	requests   map[string]*rtcRequest
	localQueue []rtcSignal
	answerSent bool
	closeOnce  sync.Once
}

type rtcRequest struct {
	request  wireMessage
	body     bytes.Buffer
	tooLarge bool
}

func newNodeRTCManager(conn *websocket.Conn, writeMu *sync.Mutex, servers []iceServerConfig) *nodeRTCManager {
	direct := make([]iceServerConfig, 0, len(servers))
	for _, server := range servers {
		filtered := server
		filtered.URLs = nil
		for _, endpoint := range server.URLs {
			if !strings.HasPrefix(strings.ToLower(endpoint), "turn:") && !strings.HasPrefix(strings.ToLower(endpoint), "turns:") {
				filtered.URLs = append(filtered.URLs, endpoint)
			}
		}
		if len(filtered.URLs) > 0 {
			direct = append(direct, filtered)
		}
	}
	return &nodeRTCManager{sessions: map[string]*nodeRTCSession{}, config: pionICEConfiguration(direct), conn: conn, writeMu: writeMu}
}

func (m *nodeRTCManager) handle(sessionID, shareID string, payload json.RawMessage) {
	if sessionID == "" {
		return
	}
	var signal rtcSignal
	if json.Unmarshal(payload, &signal) != nil {
		return
	}
	switch signal.Type {
	case "offer":
		if signal.Description != nil && signal.Description.Type == webrtc.SDPTypeOffer {
			m.acceptOffer(sessionID, shareID, *signal.Description)
		}
	case "candidate":
		if signal.Candidate == nil {
			return
		}
		m.mu.Lock()
		session := m.sessions[sessionID]
		m.mu.Unlock()
		if session != nil {
			_ = session.peer.AddICECandidate(*signal.Candidate)
		}
	case "close":
		m.remove(sessionID, nil)
	}
}

func (m *nodeRTCManager) acceptOffer(sessionID, shareID string, offer webrtc.SessionDescription) {
	peer, err := webrtc.NewPeerConnection(m.config)
	if err != nil {
		m.sendSignal(sessionID, rtcSignal{Type: "error"})
		return
	}
	session := &nodeRTCSession{peer: peer, shareID: shareID, requests: map[string]*rtcRequest{}}
	m.mu.Lock()
	previous := m.sessions[sessionID]
	m.sessions[sessionID] = session
	m.mu.Unlock()
	if previous != nil {
		previous.close()
	}

	peer.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			m.queueOrSendCandidate(sessionID, session, candidate.ToJSON())
		}
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		session.channel = channel
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			m.handleDataMessage(session, channel, message.Data)
		})
	})
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.remove(sessionID, session)
		}
	})
	if err := peer.SetRemoteDescription(offer); err != nil {
		m.remove(sessionID, session)
		m.sendSignal(sessionID, rtcSignal{Type: "error"})
		return
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		m.remove(sessionID, session)
		m.sendSignal(sessionID, rtcSignal{Type: "error"})
		return
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		m.remove(sessionID, session)
		m.sendSignal(sessionID, rtcSignal{Type: "error"})
		return
	}
	if err := m.sendSignal(sessionID, rtcSignal{Type: "answer", Description: peer.LocalDescription()}); err != nil {
		m.remove(sessionID, session)
		return
	}
	session.signalMu.Lock()
	session.answerSent = true
	pending := session.localQueue
	session.localQueue = nil
	session.signalMu.Unlock()
	for _, queued := range pending {
		m.sendSignal(sessionID, queued)
	}
}

func (m *nodeRTCManager) queueOrSendCandidate(sessionID string, session *nodeRTCSession, candidate webrtc.ICECandidateInit) {
	signal := rtcSignal{Type: "candidate", Candidate: &candidate}
	session.signalMu.Lock()
	if !session.answerSent {
		session.localQueue = append(session.localQueue, signal)
		session.signalMu.Unlock()
		return
	}
	session.signalMu.Unlock()
	m.sendSignal(sessionID, signal)
}

func (m *nodeRTCManager) sendSignal(sessionID string, signal rtcSignal) error {
	payload, err := json.Marshal(signal)
	if err != nil {
		return err
	}
	return writeWire(m.conn, context.Background(), m.writeMu, wireMessage{Type: "rtc-signal", SessionID: sessionID, Signal: payload})
}

func (m *nodeRTCManager) handleDataMessage(session *nodeRTCSession, channel *webrtc.DataChannel, payload []byte) {
	var message wireMessage
	if json.Unmarshal(payload, &message) != nil || message.ID == "" {
		return
	}
	session.requestMu.Lock()
	switch message.Type {
	case "request":
		if _, exists := session.requests[message.ID]; !exists {
			session.requests[message.ID] = &rtcRequest{request: message}
		}
	case "request-chunk":
		request := session.requests[message.ID]
		if request != nil && !request.tooLarge {
			if request.body.Len()+len(message.Data) > 2<<20 {
				request.tooLarge = true
			} else {
				_, _ = request.body.Write(message.Data)
			}
		}
	case "request-end":
		request := session.requests[message.ID]
		delete(session.requests, message.ID)
		session.requestMu.Unlock()
		if request != nil {
			if request.tooLarge {
				go m.sendRTCError(session, channel, request.request.ID, http.StatusRequestEntityTooLarge, "request body too large")
			} else {
				request.request.Body = request.body.Bytes()
				go m.serveRTCRequest(session, channel, request.request)
			}
		}
		return
	}
	session.requestMu.Unlock()
}

func (m *nodeRTCManager) sendRTCError(session *nodeRTCSession, channel *webrtc.DataChannel, id string, status int, message string) {
	data, _ := json.Marshal(wireMessage{Type: "start", ID: id, Status: status})
	session.dataSendMu.Lock()
	defer session.dataSendMu.Unlock()
	_ = channel.SendText(string(data))
	data, _ = json.Marshal(wireMessage{Type: "chunk", ID: id, Data: []byte(message)})
	_ = channel.SendText(string(data))
	data, _ = json.Marshal(wireMessage{Type: "end", ID: id})
	_ = channel.SendText(string(data))
}

func (m *nodeRTCManager) serveRTCRequest(session *nodeRTCSession, channel *webrtc.DataChannel, input wireMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	write := func(message wireMessage) error {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		session.dataSendMu.Lock()
		defer session.dataSendMu.Unlock()
		for channel.BufferedAmount() > 1<<20 {
			if channel.ReadyState() != webrtc.DataChannelStateOpen {
				return errors.New("WebRTC data channel is closed")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return channel.SendText(string(data))
	}
	allowed := allowedRemoteRequest(input.Method, input.Path)
	if session.shareID != "" {
		allowed = allowedShareRTCRequest(session.shareID, input)
	}
	if !allowed {
		_ = write(wireMessage{Type: "start", ID: input.ID, Status: http.StatusForbidden})
		_ = write(wireMessage{Type: "chunk", ID: input.ID, Data: []byte("接口不允许通过远程节点连接")})
		_ = write(wireMessage{Type: "end", ID: input.ID})
		return
	}
	request, err := http.NewRequestWithContext(ctx, input.Method, "http://127.0.0.1:4317"+input.Path, bytes.NewReader(input.Body))
	if err != nil {
		_ = write(wireMessage{Type: "start", ID: input.ID, Status: http.StatusBadRequest})
		_ = write(wireMessage{Type: "end", ID: input.ID})
		return
	}
	copyAllowedHeaders(request.Header, input.Headers, false)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		_ = write(wireMessage{Type: "start", ID: input.ID, Status: http.StatusBadGateway})
		_ = write(wireMessage{Type: "chunk", ID: input.ID, Data: []byte("本机资源服务暂不可用")})
		_ = write(wireMessage{Type: "end", ID: input.ID})
		return
	}
	defer response.Body.Close()
	if err := write(wireMessage{Type: "start", ID: input.ID, Status: response.StatusCode, Headers: filteredHeaders(response.Header, true)}); err != nil {
		return
	}
	buffer := make([]byte, rtcChunkSize)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			if err := write(wireMessage{Type: "chunk", ID: input.ID, Data: buffer[:count]}); err != nil {
				return
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				_ = write(wireMessage{Type: "chunk", ID: input.ID, Data: []byte(readErr.Error())})
			}
			break
		}
	}
	_ = write(wireMessage{Type: "end", ID: input.ID})
}

func allowedShareRTCRequest(shareID string, input wireMessage) bool {
	parsed, err := url.ParseRequestURI(input.Path)
	if !validShareID(shareID) || err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || filteredHeaders(input.Headers, false).Get("X-Myliba-Share-Key") == "" {
		return false
	}
	base := "/api/shares/" + shareID
	if input.Method == http.MethodGet && parsed.Path == base+"/file" {
		return true
	}
	if !strings.HasPrefix(parsed.Path, base+"/video/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, base+"/video/"), "/")
	if len(parts) == 1 {
		return (input.Method == http.MethodGet || input.Method == http.MethodPost) && videoQualities[parts[0]].width != 0
	}
	if videoQualities[parts[0]].width == 0 || input.Method != http.MethodGet {
		return false
	}
	if len(parts) == 2 && parts[1] == "init" {
		return true
	}
	if len(parts) == 3 && parts[1] == "segments" {
		index, err := strconv.Atoi(parts[2])
		return err == nil && index >= 0 && index <= 99999
	}
	return false
}

func (m *nodeRTCManager) remove(sessionID string, expected *nodeRTCSession) {
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil && (expected == nil || expected == session) {
		delete(m.sessions, sessionID)
	} else {
		session = nil
	}
	m.mu.Unlock()
	if session != nil {
		session.close()
	}
}

func (m *nodeRTCManager) closeAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = map[string]*nodeRTCSession{}
	m.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (s *nodeRTCSession) close() {
	s.closeOnce.Do(func() {
		if s.peer != nil {
			_ = s.peer.Close()
		}
	})
}
