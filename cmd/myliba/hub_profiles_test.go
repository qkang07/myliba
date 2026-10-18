package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubProfilesRuntimeSwitchAndSecretIsolation(t *testing.T) {
	if allowedRemoteRequest(http.MethodGet, "/api/hubs") || allowedRemoteRequest(http.MethodPost, "/api/hubs") {
		t.Fatal("Hub profiles must not be accessible through remote proxy")
	}
	t.Setenv("MYLIBA_HUB_URL", "")
	t.Setenv("MYLIBA_NODE_TOKEN", "")
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	h, err := newHubProfiles(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, origin string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:4317"+path, strings.NewReader(body))
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler(response, req)
		return response
	}
	token := strings.Repeat("x", 32)
	response := request("POST", "/api/hubs", `{"name":"A","url":"wss://127.0.0.1:1/node","token":"`+token+`"}`, "http://127.0.0.1:4317", h.save)
	if response.Code != 200 {
		t.Fatalf("save: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), token) {
		t.Fatal("node token leaked in response")
	}
	var view hubProfilesView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Profiles) != 1 || view.ActiveID != view.Profiles[0].ID {
		t.Fatalf("unexpected view: %+v", view)
	}
	firstID := view.ActiveID
	response = request("POST", "/api/hubs", `{"name":"B","url":"wss://127.0.0.1:2/node","token":"`+token+`"}`, "http://127.0.0.1:4317", h.save)
	if response.Code != 200 {
		t.Fatalf("second save: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ActiveID == firstID || len(view.Profiles) != 2 {
		t.Fatalf("new profile did not become active: %+v", view)
	}
	response = request("PUT", "/api/hubs/active", `{"id":"`+firstID+`"}`, "http://127.0.0.1:4317", h.selectActive)
	if response.Code != 200 {
		t.Fatalf("switch: %d %s", response.Code, response.Body.String())
	}
	response = request("PUT", "/api/hubs/active", `{"id":""}`, "http://127.0.0.1:4317", h.selectActive)
	if response.Code != 200 {
		t.Fatalf("disconnect: %d %s", response.Code, response.Body.String())
	}
	response = request("POST", "/api/hubs", `{"name":"C","url":"wss://127.0.0.1:3/node","token":"`+token+`"}`, "https://evil.example", h.save)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin save: %d", response.Code)
	}
	h.mu.Lock()
	if h.cancel != nil {
		h.cancel()
	}
	h.mu.Unlock()
}
