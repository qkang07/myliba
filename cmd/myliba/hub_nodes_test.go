package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestHubRoutesSessionToSelectedResourceNode(t *testing.T) {
	hub := &remoteHub{
		nodes: map[string]*remoteNode{}, selectedNodes: map[string]string{},
		waiters: map[string]*remoteWaiter{}, sessions: map[string]time.Time{"test": time.Now().Add(time.Hour)},
		signalSessions: map[string]signalSession{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /node", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err == nil {
			hub.attachNode(conn, r.URL.Query().Get("id"), r.URL.Query().Get("name"), r.URL.Query().Get("kind"))
		}
	})
	mux.HandleFunc("GET /api/session", hub.sessionStatus)
	mux.Handle("PUT /api/hub/nodes/active", hub.requireSession(http.HandlerFunc(hub.selectNode)))
	mux.Handle("GET /api/resources", hub.requireSession(http.HandlerFunc(hub.proxy)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, node := range []string{"desktop", "phone"} {
		kind := "desktop"
		if node == "phone" {
			kind = "android"
		}
		conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http:", "ws:", 1)+"/node?id="+node+"&name="+node+"&kind="+kind, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.CloseNow()
		go func(name string) {
			for {
				_, payload, err := conn.Read(ctx)
				if err != nil {
					return
				}
				var request wireMessage
				if json.Unmarshal(payload, &request) != nil || request.Type != "request" {
					continue
				}
				_ = conn.Write(ctx, websocket.MessageText, mustWire(t, wireMessage{Type: "start", ID: request.ID, Status: 200}))
				_ = conn.Write(ctx, websocket.MessageText, mustWire(t, wireMessage{Type: "chunk", ID: request.ID, Data: []byte(name)}))
				_ = conn.Write(ctx, websocket.MessageText, mustWire(t, wireMessage{Type: "end", ID: request.ID}))
			}
		}(node)
	}
	client := server.Client()
	request := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: hubSessionCookie, Value: "test"})
		req.Header.Set("Origin", server.URL)
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	selected := request(http.MethodPut, "/api/hub/nodes/active", `{"id":"phone"}`)
	selected.Body.Close()
	if selected.StatusCode != 200 {
		t.Fatalf("select status = %d", selected.StatusCode)
	}
	response := request(http.MethodGet, "/api/resources", "")
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(data) != "phone" {
		t.Fatalf("selected route = %d %q", response.StatusCode, data)
	}
	session := request(http.MethodGet, "/api/session", "")
	var info struct {
		SelectedNodeID string        `json:"selectedNodeId"`
		Nodes          []hubNodeView `json:"nodes"`
	}
	if err := json.NewDecoder(session.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	session.Body.Close()
	if info.SelectedNodeID != "phone" || len(info.Nodes) != 2 {
		t.Fatalf("session selection = %#v", info)
	}
	for _, node := range info.Nodes {
		if node.ID == "phone" && (node.Kind != "android" || !node.Online) {
			t.Fatalf("phone node = %#v", node)
		}
	}
}

func TestRemoveOfflineResourceNode(t *testing.T) {
	hub := &remoteHub{
		nodes:         map[string]*remoteNode{"old-phone": {ID: "old-phone", Name: "旧手机", Kind: "android"}},
		selectedNodes: map[string]string{"test": "old-phone"},
		sessions:      map[string]time.Time{"test": time.Now().Add(time.Hour)},
	}
	mux := http.NewServeMux()
	mux.Handle("DELETE /api/hub/nodes/{id}", hub.requireSession(http.HandlerFunc(hub.removeOfflineNode)))
	request := httptest.NewRequest(http.MethodDelete, "/api/hub/nodes/old-phone", nil)
	request.Header.Set("Origin", "http://example.com")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated removal = %d", response.Code)
	}
	request.AddCookie(&http.Cookie{Name: hubSessionCookie, Value: "test"})
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || hub.nodes["old-phone"] != nil || hub.selectedNodes["test"] != "" {
		t.Fatalf("offline removal = %d, nodes = %+v, selections = %+v", response.Code, hub.nodes, hub.selectedNodes)
	}
}

func TestRTCWireRequestAcceptsBrowserAndNativeHeaders(t *testing.T) {
	for _, input := range []string{
		`{"type":"request","id":"1","headers":{"content-type":"application/json","range":"bytes=0-9"}}`,
		`{"type":"request","id":"1","headers":{"Content-Type":["application/json"],"Range":["bytes=0-9"]}}`,
	} {
		var message wireMessage
		if err := json.Unmarshal([]byte(input), &message); err != nil {
			t.Fatal(err)
		}
		if message.Headers.Get("Content-Type") != "application/json" || message.Headers.Get("Range") != "bytes=0-9" {
			t.Fatalf("headers = %#v", message.Headers)
		}
	}
}

func mustWire(t *testing.T, message wireMessage) []byte {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
