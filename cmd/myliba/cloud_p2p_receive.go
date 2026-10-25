package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

func (c *cloudRuntime) startSignalInbox(ctx context.Context) {
	c.signalOnce.Do(func() { go c.signalInbox(ctx) })
}

func (c *cloudRuntime) signalInbox(ctx context.Context) {
	for ctx.Err() == nil {
		_, token, err := c.credentials()
		if err == nil && len(token) >= 32 {
			var inbox struct {
				Messages []cloudPeerSignal `json:"messages"`
			}
			err = c.p2pHub(ctx, http.MethodGet, "/cloud/signal/"+c.id+"?wait=1", token, nil, &inbox)
			if err == nil {
				for _, message := range inbox.Messages {
					if message.To != c.id {
						continue
					}
					switch message.Type {
					case "answer":
						c.signalMu.Lock()
						answer := c.answers[message.Session]
						c.signalMu.Unlock()
						if answer != nil {
							select {
							case answer <- message:
							default:
							}
						}
					case "offer":
						go c.acceptCloudOffer(ctx, message, token)
					}
				}
				continue
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("Cloud peer signaling: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *cloudRuntime) acceptCloudOffer(parent context.Context, offer cloudPeerSignal, token string) {
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	var ice struct {
		Servers []iceServerConfig `json:"servers"`
	}
	if err := c.p2pHub(ctx, http.MethodGet, "/cloud/ice", token, nil, &ice); err != nil {
		return
	}
	peer, err := webrtc.NewPeerConnection(pionICEConfiguration(ice.Servers))
	if err != nil {
		return
	}
	defer peer.Close()
	done := make(chan struct{}, 1)
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		var header cloudChannelHeader
		var body bytes.Buffer
		started, ended := false, false
		channel.OnMessage(func(message webrtc.DataChannelMessage) {
			if ended {
				return
			}
			if !message.IsString {
				if !started || body.Len()+len(message.Data) > cloudMaxBlobSize {
					ended = true
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
				_, _ = body.Write(message.Data)
				return
			}
			var control cloudChannelHeader
			if json.Unmarshal(message.Data, &control) != nil {
				ended = true
				select {
				case done <- struct{}{}:
				default:
				}
				return
			}
			switch control.Type {
			case "request":
				if started {
					ended = true
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
				header, started = control, true
			case "end":
				if !started {
					ended = true
					select {
					case done <- struct{}{}:
					default:
					}
					return
				}
				ended = true
				payload := append([]byte(nil), body.Bytes()...)
				go func() {
					c.serveCloudChannel(ctx, channel, header, payload)
					select {
					case done <- struct{}{}:
					default:
					}
				}()
			}
		})
	})
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		return
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		return
	}
	gather := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		return
	}
	select {
	case <-gather:
	case <-ctx.Done():
		return
	}
	if err := c.p2pHub(ctx, http.MethodPost, "/cloud/signal", token, cloudPeerSignal{From: c.id, To: offer.From, Session: offer.Session, Type: "answer", SDP: peer.LocalDescription().SDP}, nil); err != nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (c *cloudRuntime) serveCloudChannel(ctx context.Context, channel *webrtc.DataChannel, header cloudChannelHeader, payload []byte) {
	status, data := http.StatusBadRequest, []byte(nil)
	if strings.HasPrefix(header.Path, "/cloud/") && (header.Method == http.MethodGet || header.Method == http.MethodHead || header.Method == http.MethodPut) && len(header.Token) >= 32 && len(header.Token) <= 4096 {
		request, err := http.NewRequestWithContext(ctx, header.Method, "http://cloud.invalid"+header.Path, bytes.NewReader(payload))
		if err == nil {
			request.Header.Set("Authorization", "Bearer "+header.Token)
			recorder := httptest.NewRecorder()
			c.directHandler().ServeHTTP(recorder, request)
			response := recorder.Result()
			status = response.StatusCode
			data, _ = io.ReadAll(io.LimitReader(response.Body, cloudMaxBlobSize+1))
			response.Body.Close()
			if len(data) > cloudMaxBlobSize {
				status, data = http.StatusInternalServerError, nil
			}
		}
	}
	response, _ := json.Marshal(cloudChannelHeader{Type: "response", Status: status})
	if err := channel.SendText(string(response)); err != nil {
		return
	}
	for offset := 0; offset < len(data); offset += 16 << 10 {
		end := offset + 16<<10
		if end > len(data) {
			end = len(data)
		}
		for channel.BufferedAmount() > 512<<10 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
		if err := channel.Send(data[offset:end]); err != nil {
			return
		}
	}
	if err := channel.SendText(`{"type":"end"}`); err != nil {
		return
	}
	for channel.BufferedAmount() > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}
