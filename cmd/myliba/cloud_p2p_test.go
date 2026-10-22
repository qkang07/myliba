package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestCloudHubSignalsOnlyBetweenRegisteredNodes(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	t.Setenv("MYLIBA_ICE_SERVERS", `[{"urls":["stun:stun.example:3478","turn:relay.example:3478"]}]`)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	hub.routes(mux, token)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(method, path string, value any) *http.Response {
		var body bytes.Buffer
		if value != nil {
			if err := json.NewEncoder(&body).Encode(value); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, server.URL+path, &body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	source, target := "11111111111111111111111111111111", "22222222222222222222222222222222"
	for _, node := range []cloudNode{{ID: source, Role: "client"}, {ID: target, Role: "storage", Endpoint: "p2p://" + target}} {
		response := call(http.MethodPost, "/cloud/nodes/heartbeat", node)
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("heartbeat: %d", response.StatusCode)
		}
	}
	signal := cloudPeerSignal{From: source, To: target, Session: "33333333333333333333333333333333", Type: "offer", SDP: "v=0"}
	response := call(http.MethodPost, "/cloud/signal", signal)
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("signal: %d", response.StatusCode)
	}
	response = call(http.MethodGet, "/cloud/signal/"+target, nil)
	var inbox struct {
		Messages []cloudPeerSignal `json:"messages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inbox); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(inbox.Messages) != 1 || inbox.Messages[0].SDP != signal.SDP {
		t.Fatalf("inbox: %+v", inbox.Messages)
	}
	response = call(http.MethodGet, "/cloud/signal/"+target, nil)
	inbox.Messages = nil
	if err := json.NewDecoder(response.Body).Decode(&inbox); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(inbox.Messages) != 0 {
		t.Fatal("signal was delivered twice")
	}
	response = call(http.MethodGet, "/cloud/ice", nil)
	var ice struct {
		Servers []iceServerConfig `json:"servers"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ice); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(ice.Servers) != 1 || len(ice.Servers[0].URLs) != 1 || ice.Servers[0].URLs[0] != "stun:stun.example:3478" {
		t.Fatalf("relay ICE leaked into cloud transfer: %+v", ice)
	}
}

func TestCloudPeerDataChannelBypassesHubForBytes(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	t.Setenv("MYLIBA_ICE_SERVERS", `[{"urls":["stun:127.0.0.1:9"]}]`)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	hub.routes(mux, token)
	server := httptest.NewServer(mux)
	defer server.Close()
	runtime := &cloudRuntime{id: "11111111111111111111111111111111", hubURL: "ws" + strings.TrimPrefix(server.URL, "http") + "/node", token: token, client: &http.Client{Timeout: 10 * time.Second}}
	target := "22222222222222222222222222222222"
	block := bytes.Repeat([]byte("encrypted-chunk"), (4<<20)/len("encrypted-chunk"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, node := range []cloudNode{{ID: runtime.id, Role: "client"}, {ID: target, Endpoint: "p2p://" + target, Role: "storage"}} {
		if err := runtime.p2pHub(ctx, http.MethodPost, "/cloud/nodes/heartbeat", token, node, nil); err != nil {
			t.Fatal(err)
		}
	}
	answerDone := make(chan error, 1)
	go func() {
		for {
			var inbox struct {
				Messages []cloudPeerSignal `json:"messages"`
			}
			if err := runtime.p2pHub(ctx, http.MethodGet, "/cloud/signal/"+target, token, nil, &inbox); err != nil {
				answerDone <- err
				return
			}
			for _, offer := range inbox.Messages {
				peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
				if err != nil {
					answerDone <- err
					return
				}
				defer peer.Close()
				peer.OnDataChannel(func(channel *webrtc.DataChannel) {
					var uploaded bytes.Buffer
					channel.OnMessage(func(message webrtc.DataChannelMessage) {
						if !message.IsString {
							_, _ = uploaded.Write(message.Data)
							return
						}
						if message.IsString && string(message.Data) == `{"type":"end"}` {
							go func() {
								if !bytes.Equal(uploaded.Bytes(), block) {
									_ = channel.SendText(`{"type":"response","status":400}`)
									_ = channel.SendText(`{"type":"end"}`)
									return
								}
								_ = channel.SendText(`{"type":"response","status":200}`)
								for offset := 0; offset < len(block); offset += 16 << 10 {
									end := offset + (16 << 10)
									if end > len(block) {
										end = len(block)
									}
									for channel.BufferedAmount() > 512<<10 {
										time.Sleep(5 * time.Millisecond)
									}
									if channel.Send(block[offset:end]) != nil {
										return
									}
								}
								_ = channel.SendText(`{"type":"end"}`)
							}()
						}
					})
				})
				if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
					answerDone <- err
					return
				}
				answer, err := peer.CreateAnswer(nil)
				if err != nil {
					answerDone <- err
					return
				}
				gather := webrtc.GatheringCompletePromise(peer)
				if err = peer.SetLocalDescription(answer); err != nil {
					answerDone <- err
					return
				}
				select {
				case <-gather:
				case <-ctx.Done():
					answerDone <- ctx.Err()
					return
				}
				err = runtime.p2pHub(ctx, http.MethodPost, "/cloud/signal", token, cloudPeerSignal{From: target, To: runtime.id, Session: offer.Session, Type: "answer", SDP: peer.LocalDescription().SDP}, nil)
				if err != nil {
					answerDone <- err
					return
				}
				<-ctx.Done()
				answerDone <- nil
				return
			}
			select {
			case <-ctx.Done():
				answerDone <- ctx.Err()
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	response, err := runtime.p2pRequest(ctx, http.MethodPut, target, "/cloud/blocks/test", token, bytes.NewReader(block))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(data, block) {
		t.Fatalf("data channel: status %d, size %d, error %v", response.StatusCode, len(data), err)
	}
	cancel()
	<-answerDone
}
