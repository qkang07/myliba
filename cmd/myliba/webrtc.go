package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

const (
	maxWebRTCPeers      = 32
	webRTCTransferChunk = 32 << 10
	webRTCBufferedLimit = 1 << 20
)

type webRTCOfferRequest struct {
	SDP        string `json:"sdp"`
	ResourceID string `json:"resourceId"`
}

type webRTCTransferMessage struct {
	Type        string "json:\"type\""
	ResourceID  string "json:\"resourceId,omitempty\""
	Name        string "json:\"name,omitempty\""
	ContentType string "json:\"contentType,omitempty\""
	Size        int64  "json:\"size\""
	Message     string "json:\"message,omitempty\""
}

func configuredICEServers() []webrtc.ICEServer {
	raw := strings.TrimSpace(os.Getenv("MYLIBA_STUN_URLS"))
	if raw == "" {
		raw = "stun:stun.l.google.com:19302"
	}
	servers := make([]webrtc.ICEServer, 0)
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "stun:") || strings.HasPrefix(value, "stuns:") {
			servers = append(servers, webrtc.ICEServer{URLs: []string{value}})
		}
	}
	return servers
}

func (s *server) createWebRTCAnswer(w http.ResponseWriter, r *http.Request) {
	var input webRTCOfferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil || input.SDP == "" || input.ResourceID == "" {
		writeError(w, http.StatusBadRequest, "invalid WebRTC offer")
		return
	}

	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: configuredICEServers()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create WebRTC peer")
		return
	}
	if !s.registerWebRTCPeer(peer) {
		_ = peer.Close()
		writeError(w, http.StatusServiceUnavailable, "too many WebRTC peers")
		return
	}
	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			s.removeWebRTCPeer(peer)
			_ = peer.Close()
		}
	})
	var transferStarted atomic.Bool
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		s.handleWebRTCFileChannel(channel, input.ResourceID, &transferStarted)
	})
	time.AfterFunc(2*time.Minute, func() {
		if !transferStarted.Load() {
			_ = peer.Close()
		}
	})

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: input.SDP}
	if err := peer.SetRemoteDescription(offer); err != nil {
		s.removeWebRTCPeer(peer)
		_ = peer.Close()
		writeError(w, http.StatusBadRequest, "invalid WebRTC offer")
		return
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		s.removeWebRTCPeer(peer)
		_ = peer.Close()
		writeError(w, http.StatusInternalServerError, "could not create WebRTC answer")
		return
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		s.removeWebRTCPeer(peer)
		_ = peer.Close()
		writeError(w, http.StatusInternalServerError, "could not start ICE gathering")
		return
	}
	select {
	case <-gathered:
	case <-time.After(12 * time.Second):
		s.removeWebRTCPeer(peer)
		_ = peer.Close()
		writeError(w, http.StatusGatewayTimeout, "ICE gathering timed out")
		return
	}
	local := peer.LocalDescription()
	if local == nil {
		s.removeWebRTCPeer(peer)
		_ = peer.Close()
		writeError(w, http.StatusInternalServerError, "WebRTC answer is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"type": "answer", "sdp": local.SDP})
	time.AfterFunc(30*time.Minute, func() {
		_ = peer.Close()
	})
}

func (s *server) registerWebRTCPeer(peer *webrtc.PeerConnection) bool {
	s.webrtcMu.Lock()
	defer s.webrtcMu.Unlock()
	if len(s.webrtcPeers) >= maxWebRTCPeers {
		return false
	}
	s.webrtcPeers[peer] = struct{}{}
	return true
}

func (s *server) removeWebRTCPeer(peer *webrtc.PeerConnection) {
	s.webrtcMu.Lock()
	delete(s.webrtcPeers, peer)
	s.webrtcMu.Unlock()
}

func (s *server) handleWebRTCFileChannel(channel *webrtc.DataChannel, authorizedResourceID string, requested *atomic.Bool) {
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		if !message.IsString {
			return
		}
		var input webRTCTransferMessage
		if err := json.Unmarshal(message.Data, &input); err != nil || input.Type != "get" || input.ResourceID != authorizedResourceID {
			_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "invalid file request"})
			return
		}
		if !requested.CompareAndSwap(false, true) {
			_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "only one file can be requested per connection"})
			return
		}
		go s.sendFileOverWebRTC(channel, input.ResourceID)
	})
}

func (s *server) sendFileOverWebRTC(channel *webrtc.DataChannel, resourceID string) {
	defer channel.Close()
	var path string
	if err := s.db.QueryRow("SELECT path FROM resources WHERE id=?", resourceID).Scan(&path); err != nil {
		_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "resource not found"})
		return
	}
	file, err := os.Open(path)
	if err != nil {
		_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "file is unavailable"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "file is unavailable"})
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := sendWebRTCControl(channel, webRTCTransferMessage{
		Type:        "start",
		Name:        filepath.Base(path),
		ContentType: contentType,
		Size:        info.Size(),
	}); err != nil {
		return
	}

	buffer := make([]byte, webRTCTransferChunk)
	for {
		if err := waitForWebRTCBuffer(channel); err != nil {
			return
		}
		count, readErr := file.Read(buffer)
		if count > 0 {
			chunk := append([]byte(nil), buffer[:count]...)
			if err := channel.Send(chunk); err != nil {
				return
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "error", Message: "file read failed"})
				return
			}
			break
		}
	}
	_ = sendWebRTCControl(channel, webRTCTransferMessage{Type: "done"})
}

func waitForWebRTCBuffer(channel *webrtc.DataChannel) error {
	for channel.BufferedAmount() > webRTCBufferedLimit {
		if channel.ReadyState() != webrtc.DataChannelStateOpen {
			return errors.New("data channel closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if channel.ReadyState() != webrtc.DataChannelStateOpen {
		return errors.New("data channel closed")
	}
	return nil
}

func sendWebRTCControl(channel *webrtc.DataChannel, message webRTCTransferMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return channel.SendText(string(data))
}
