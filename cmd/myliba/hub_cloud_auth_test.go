package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudDeviceCredentialsAreScopedAndRevocable(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	store, err := newHubAuthStore(strings.Repeat("owner-password", 2))
	if err != nil {
		t.Fatal(err)
	}
	storageToken, browserToken := strings.Repeat("s", 64), strings.Repeat("b", 64)
	if err := store.update(func(data *hubAuthData) error {
		data.Devices["storage"] = hubAuthDevice{ID: "storage", Kind: "storage", TokenHash: hubAuthKey(storageToken), CanCloud: true}
		data.Devices["browser"] = hubAuthDevice{ID: "browser", Kind: "desktop", TokenHash: hubAuthKey(browserToken), CanBrowse: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store}
	mux := http.NewServeMux()
	mux.Handle("GET /cloud/probe", hub.cloudBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })))
	mux.HandleFunc("POST /cloud/auth/verify", hub.verifyCloudPeer)
	mux.HandleFunc("POST /cloud/auth/grant", hub.issueCloudGrant)
	grant := func(token string) string {
		r := httptest.NewRequest("POST", "/cloud/auth/grant", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("issue grant: %d", w.Code)
		}
		var result struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result.Token
	}
	request := func(method, path, token, body string) int {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if got := request("GET", "/cloud/probe", storageToken, ""); got != 204 {
		t.Fatalf("cloud device: %d", got)
	}
	if got := request("GET", "/cloud/probe", browserToken, ""); got != 401 {
		t.Fatalf("browser-only device: %d", got)
	}
	if got := request("POST", "/cloud/auth/verify", storageToken, `{"token":"`+browserToken+`"}`); got != 401 {
		t.Fatalf("peer without cloud grant: %d", got)
	}
	if err := store.update(func(data *hubAuthData) error {
		d := data.Devices["browser"]
		d.CanCloud = true
		data.Devices["browser"] = d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := request("POST", "/cloud/auth/verify", storageToken, `{"token":"`+browserToken+`"}`); got != 401 {
		t.Fatalf("raw peer credential leaked as grant: %d", got)
	}
	access := grant(browserToken)
	if got := request("POST", "/cloud/auth/verify", storageToken, `{"token":"`+access+`"}`); got != 204 {
		t.Fatalf("authorized peer: %d", got)
	}
	if got := request("POST", "/cloud/auth/verify", storageToken, `{"token":"`+access+`"}`); got != 401 {
		t.Fatalf("replayed grant: %d", got)
	}
	access = grant(browserToken)
	if err := store.update(func(data *hubAuthData) error {
		d := data.Devices["browser"]
		d.RevokedAt = time.Now()
		data.Devices["browser"] = d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := request("POST", "/cloud/auth/verify", storageToken, `{"token":"`+access+`"}`); got != 401 {
		t.Fatalf("revoked peer: %d", got)
	}
}

func TestCloudStoragePairsWithOwnerPassword(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	password := strings.Repeat("owner-secret", 2)
	store, err := newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store, password: password, secure: false}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", hub.login)
	mux.HandleFunc("DELETE /api/session", hub.logout)
	mux.Handle("POST /api/hub/devices", hub.requireOwner(http.HandlerFunc(hub.pairDevice)))
	server := httptest.NewServer(mux)
	defer server.Close()
	token, err := pairCloudStorage(server.URL, "storage-1", password)
	if err != nil {
		t.Fatal(err)
	}
	if id, allowed := hub.cloudDeviceID(token); !allowed || id != "storage-1" {
		t.Fatalf("storage credential not authorized: %q %v", id, allowed)
	}
	if d := store.data.Devices["storage-1"]; d.Kind != "storage" || !d.CanCloud || d.CanBrowse || d.CanServe {
		t.Fatalf("wrong storage grant: %+v", d)
	}
}

func TestCloudDirectRequestUsesOneTimeGrant(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	store, err := newHubAuthStore(strings.Repeat("owner-password", 2))
	if err != nil {
		t.Fatal(err)
	}
	callerToken, storageToken := strings.Repeat("c", 64), strings.Repeat("s", 64)
	if err := store.update(func(data *hubAuthData) error {
		data.Devices["caller"] = hubAuthDevice{ID: "caller", Kind: "desktop", TokenHash: hubAuthKey(callerToken), CanCloud: true}
		data.Devices["storage"] = hubAuthDevice{ID: "storage", Kind: "storage", TokenHash: hubAuthKey(storageToken), CanCloud: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store}
	hubMux := http.NewServeMux()
	hubMux.HandleFunc("POST /cloud/auth/grant", hub.issueCloudGrant)
	hubMux.HandleFunc("POST /cloud/auth/verify", hub.verifyCloudPeer)
	hubServer := httptest.NewServer(hubMux)
	defer hubServer.Close()
	storage := testCloudRuntime(t, t.TempDir(), storageToken)
	storage.grantForPeer = nil
	storage.hubURL = "ws" + strings.TrimPrefix(hubServer.URL, "http") + "/node"
	storageMux := http.NewServeMux()
	storage.disk.routesWithAuth(storageMux, storage.authorizeCloudPeer)
	storageServer := httptest.NewServer(storageMux)
	defer storageServer.Close()
	caller := testCloudRuntime(t, t.TempDir(), callerToken)
	caller.grantForPeer = nil
	caller.hubURL = storage.hubURL
	response, err := caller.request(context.Background(), http.MethodGet, storageServer.URL, "/cloud/inventory", callerToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("direct inventory: %d", response.StatusCode)
	}
	if err := store.update(func(data *hubAuthData) error {
		d := data.Devices["caller"]
		d.RevokedAt = time.Now()
		data.Devices["caller"] = d
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := caller.request(context.Background(), http.MethodGet, storageServer.URL, "/cloud/inventory", callerToken, nil); err == nil {
		t.Fatal("revoked caller obtained a new grant")
	}
}
