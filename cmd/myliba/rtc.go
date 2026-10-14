package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
	return &nodeRTCManager{sessions: map[string]*nodeRTCSession{}, config: pionICEConfiguration(servers), conn: conn, writeMu: writeMu}
}

func (m *nodeRTCManager) handle(sessionID string, payload json.RawMessage) {
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
			m.acceptOffer(sessionID, *signal.Description)
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

func (m *nodeRTCManager) acceptOffer(sessionID string, offer webrtc.SessionDescription) {
	peer, err := webrtc.NewPeerConnection(m.config)
	if err != nil {
		m.sendSignal(sessionID, rtcSignal{Type: "error"})
		return
	}
	session := &nodeRTCSession{peer: peer, requests: map[string]*rtcRequest{}}
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
	if !strings.HasPrefix(input.Path, "/api/") || strings.HasPrefix(input.Path, "/api/session") || strings.HasPrefix(input.Path, "/api/directories") {
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
