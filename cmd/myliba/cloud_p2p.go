package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// Cloud WebRTC carries ciphertext only. The Hub exchanges SDP and never sees data-channel frames.
type cloudChannelHeader struct {
	Type   string `json:"type"`
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	Token  string `json:"token,omitempty"`
	Status int    `json:"status,omitempty"`
}

func (c *cloudRuntime) p2pHub(ctx context.Context, method, path, token string, input any, output any) error {
	wsURL, _, err := c.credentials()
	if err != nil {
		return err
	}
	base, err := hubHTTPURL(wsURL)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return e
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("Hub cloud signaling: HTTP %d", resp.StatusCode)
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(output)
	}
	return nil
}

func (c *cloudRuntime) p2pRequest(parent context.Context, method, target, path, token string, body io.Reader) (*http.Response, error) {
	if !validCloudNodeID(target) {
		return nil, errors.New("invalid peer ID")
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	c.startSignalInbox(parent)
	var payload []byte
	var err error
	if body != nil {
		payload, err = io.ReadAll(io.LimitReader(body, cloudMaxBlobSize+1))
		if err != nil {
			return nil, err
		}
		if len(payload) > cloudMaxBlobSize {
			return nil, errors.New("cloud request too large")
		}
	}
	var ice struct {
		Servers []iceServerConfig `json:"servers"`
	}
	if err := c.p2pHub(ctx, http.MethodGet, "/cloud/ice", token, nil, &ice); err != nil {
		return nil, err
	}
	config := pionICEConfiguration(ice.Servers)
	peer, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}
	defer peer.Close()
	channel, err := peer.CreateDataChannel("cloud", nil)
	if err != nil {
		return nil, err
	}
	type result struct {
		response *http.Response
		err      error
	}
	finished := make(chan result, 1)
	var received bytes.Buffer
	status := 0
	var payloadStarted time.Time
	var payloadElapsed time.Duration
	channel.OnMessage(func(msg webrtc.DataChannelMessage) {
		if msg.IsString {
			var header cloudChannelHeader
			if json.Unmarshal(msg.Data, &header) != nil {
				return
			}
			switch header.Type {
			case "response":
				status = header.Status
			case "end":
				if status < 100 || status > 599 {
					select {
					case finished <- result{err: errors.New("invalid cloud peer response")}:
					default:
					}
					return
				}
				if !payloadStarted.IsZero() {
					payloadElapsed = time.Since(payloadStarted)
				}
				data := append([]byte(nil), received.Bytes()...)
				header := make(http.Header)
				if payloadElapsed > 0 {
					header.Set("X-Myliba-Payload-Duration-Millis", fmt.Sprint(payloadElapsed.Milliseconds()))
				}
				select {
				case finished <- result{response: &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data))}}:
				default:
				}
			}
		} else if received.Len()+len(msg.Data) <= 16<<20 {
			if received.Len() == 0 {
				payloadStarted = time.Now()
			}
			_, _ = received.Write(msg.Data)
		} else {
			select {
			case finished <- result{err: errors.New("cloud peer response too large")}:
			default:
			}
		}
	})
	channel.OnOpen(func() {
		go func() {
			grant, grantErr := c.peerGrant(ctx, token)
			if grantErr != nil {
				select {
				case finished <- result{nil, grantErr}:
				default:
				}
				return
			}
			header, _ := json.Marshal(cloudChannelHeader{Type: "request", Method: method, Path: path, Token: grant})
			if err := channel.SendText(string(header)); err == nil {
				for offset := 0; offset < len(payload); offset += 16 << 10 {
					end := offset + 16<<10
					if end > len(payload) {
						end = len(payload)
					}
					for channel.BufferedAmount() > 512<<10 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
					if err = channel.Send(payload[offset:end]); err != nil {
						break
					}
				}
			}
			if err == nil {
				err = channel.SendText(`{"type":"end"}`)
			}
			if err != nil {
				select {
				case finished <- result{err: err}:
				default:
				}
			}
		}()
	})
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		return nil, err
	}
	gather := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		return nil, err
	}
	select {
	case <-gather:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	session := hex.EncodeToString(raw)
	answerChannel := make(chan cloudPeerSignal, 1)
	c.signalMu.Lock()
	if c.answers == nil {
		c.answers = map[string]chan cloudPeerSignal{}
	}
	c.answers[session] = answerChannel
	c.signalMu.Unlock()
	defer func() { c.signalMu.Lock(); delete(c.answers, session); c.signalMu.Unlock() }()
	signal := cloudPeerSignal{From: c.id, To: target, Session: session, Type: "offer", SDP: peer.LocalDescription().SDP}
	if err := c.p2pHub(ctx, http.MethodPost, "/cloud/signal", token, signal, nil); err != nil {
		return nil, err
	}
	select {
	case answer := <-answerChannel:
		if answer.From != target {
			return nil, errors.New("unexpected cloud peer answer")
		}
		if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
			return nil, err
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-finished:
		if result.err == nil && result.response != nil {
			if result.response.Header == nil {
				result.response.Header = make(http.Header)
			}
			result.response.Header.Set("X-Myliba-Selected-ICE-Pair", cloudSelectedICEPair(peer))
		}
		return result.response, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func cloudSelectedICEPair(peer *webrtc.PeerConnection) string {
	if peer == nil {
		return ""
	}
	stats := peer.GetStats()
	for _, stat := range stats {
		transport, ok := stat.(webrtc.TransportStats)
		if !ok || transport.SelectedCandidatePairID == "" {
			continue
		}
		pair, ok := stats[transport.SelectedCandidatePairID].(webrtc.ICECandidatePairStats)
		if !ok {
			continue
		}
		local, localOK := stats[pair.LocalCandidateID].(webrtc.ICECandidateStats)
		remote, remoteOK := stats[pair.RemoteCandidateID].(webrtc.ICECandidateStats)
		if !localOK || !remoteOK {
			continue
		}
		localType := string(local.CandidateType) + "/" + strings.ToLower(local.Protocol) + "/" + cloudIPFamily(local.IP)
		if local.NetworkType != "" {
			localType += "/" + strings.ToLower(local.NetworkType)
		}
		remoteType := string(remote.CandidateType) + "/" + strings.ToLower(remote.Protocol) + "/" + cloudIPFamily(remote.IP)
		return localType + " ↔ " + remoteType
	}
	return ""
}

func cloudIPFamily(ip string) string {
	if strings.Contains(ip, ":") {
		return "ipv6"
	}
	if ip != "" {
		return "ipv4"
	}
	return "unknown"
}
