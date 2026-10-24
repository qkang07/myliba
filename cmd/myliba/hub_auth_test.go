package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHubAuthStorePersistsHashedCredentials(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	password := strings.Repeat("owner secret ", 3)
	store, err := newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	sessionToken := strings.Repeat("s", 64)
	deviceToken := strings.Repeat("d", 64)
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	err = store.update(func(data *hubAuthData) error {
		data.Sessions[hubAuthKey(sessionToken)] = hubAuthSession{DeviceID: "phone", Expires: expires}
		data.Devices["phone"] = hubAuthDevice{ID: "phone", Name: "手机", Kind: "android", TokenHash: hubAuthKey(deviceToken), CanBrowse: true, CanServe: true, CreatedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("MYLIBA_HUB_DATA_DIR"), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), sessionToken) || strings.Contains(string(raw), deviceToken) {
		t.Fatal("raw credential persisted")
	}
	var decoded hubAuthData
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Sessions[hubAuthKey(sessionToken)].DeviceID != "phone" {
		t.Fatal("session was not persisted")
	}
	reloaded, err := newHubAuthStore(password)
	if err != nil || reloaded.data.Devices["phone"].TokenHash != hubAuthKey(deviceToken) {
		t.Fatalf("reload failed: %v", err)
	}
	if _, err := newHubAuthStore(strings.Repeat("different ", 3)); err == nil {
		t.Fatal("password change must not silently take over existing Hub auth data")
	}
}

func TestHubAuthStoreRejectsDamagedData(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYLIBA_HUB_DATA_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newHubAuthStore(strings.Repeat("owner", 5)); err == nil {
		t.Fatal("damaged auth data must stop startup")
	}
}

func TestNewHubWithoutSharedNodeTokenRequiresPairing(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	t.Setenv("MYLIBA_NODE_TOKEN", "")
	store, err := newHubAuthStore(strings.Repeat("owner", 5))
	if err != nil {
		t.Fatal(err)
	}
	if store.data.LegacyNodeAllowed {
		t.Fatal("new Hub unexpectedly enabled legacy shared node access")
	}
	hub := &remoteHub{auth: store}
	request := httptest.NewRequest(http.MethodGet, "https://hub.example/node", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
	if hub.authorizedNode(request, "unknown", "desktop", "") {
		t.Fatal("unpaired node was authorized")
	}
}

func TestHubSessionSurvivesRestartAndLogoutRevokesIt(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	password := strings.Repeat("owner password ", 2)
	store, err := newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store, password: password, nodes: map[string]*remoteNode{"home": {ID: "home", Name: "Home", Kind: "desktop"}}}
	request := httptest.NewRequest(http.MethodPost, "http://hub.example/api/session", strings.NewReader(`{"password":"`+password+`"}`))
	request.Header.Set("Origin", "http://hub.example")
	response := httptest.NewRecorder()
	hub.login(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	store, err = newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &remoteHub{auth: store, password: password, nodes: hub.nodes}
	status := httptest.NewRequest(http.MethodGet, "http://hub.example/api/session", nil)
	status.AddCookie(cookie)
	response = httptest.NewRecorder()
	restarted.sessionStatus(response, status)
	if response.Code != http.StatusOK {
		t.Fatalf("session lost after restart: %d %s", response.Code, response.Body.String())
	}
	logout := httptest.NewRequest(http.MethodDelete, "http://hub.example/api/session", nil)
	logout.Header.Set("Origin", "http://hub.example")
	logout.AddCookie(cookie)
	response = httptest.NewRecorder()
	restarted.logout(response, logout)
	if response.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", response.Code, response.Body.String())
	}
	store, err = newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	restarted.auth = store
	response = httptest.NewRecorder()
	restarted.sessionStatus(response, status)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session survived restart: %d", response.Code)
	}
}

func TestHubDevicePairingScopesAndRevocation(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	t.Setenv("MYLIBA_NODE_TOKEN", strings.Repeat("l", 64))
	password := strings.Repeat("owner password ", 2)
	store, err := newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store, password: password, nodes: map[string]*remoteNode{}}
	ownerRequest := httptest.NewRequest(http.MethodPost, "http://hub.example/api/session", strings.NewReader(`{"password":"`+password+`"}`))
	ownerRequest.Header.Set("Origin", "http://hub.example")
	response := httptest.NewRecorder()
	hub.login(response, ownerRequest)
	if response.Code != http.StatusOK {
		t.Fatalf("owner login: %d", response.Code)
	}
	ownerCookie := response.Result().Cookies()[0]
	pair := httptest.NewRequest(http.MethodPost, "http://hub.example/api/hub/devices", strings.NewReader(`{"id":"phone-1","name":"手机","kind":"android","canBrowse":true,"canServe":true}`))
	pair.Header.Set("Origin", "http://hub.example")
	pair.AddCookie(ownerCookie)
	response = httptest.NewRecorder()
	hub.requireOwner(http.HandlerFunc(hub.pairDevice)).ServeHTTP(response, pair)
	if response.Code != http.StatusCreated {
		t.Fatalf("pair: %d %s", response.Code, response.Body.String())
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &paired); err != nil || len(paired.Token) < 32 {
		t.Fatalf("pair token: %v", err)
	}
	requestNode := func(id, token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://hub.example/node", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}
	legacy := strings.Repeat("l", 64)
	if !hub.authorizedNode(requestNode("phone-1", paired.Token), "phone-1", "android", legacy) ||
		hub.authorizedNode(requestNode("other", paired.Token), "other", "android", legacy) ||
		hub.authorizedNode(requestNode("phone-1", legacy), "phone-1", "android", legacy) {
		t.Fatal("device bearer scope was not enforced")
	}
	deviceLogin := httptest.NewRequest(http.MethodPost, "http://hub.example/api/session/device", strings.NewReader(`{"id":"phone-1","token":"`+paired.Token+`"}`))
	deviceLogin.Header.Set("Origin", "http://hub.example")
	response = httptest.NewRecorder()
	hub.deviceLogin(response, deviceLogin)
	if response.Code != http.StatusOK {
		t.Fatalf("device login: %d %s", response.Code, response.Body.String())
	}
	deviceCookie := response.Result().Cookies()[0]
	manage := httptest.NewRequest(http.MethodGet, "http://hub.example/api/hub/devices", nil)
	manage.AddCookie(deviceCookie)
	response = httptest.NewRecorder()
	hub.requireOwner(http.HandlerFunc(hub.listDevices)).ServeHTTP(response, manage)
	if response.Code != http.StatusForbidden {
		t.Fatalf("device could manage devices: %d", response.Code)
	}
	revoke := httptest.NewRequest(http.MethodDelete, "http://hub.example/api/hub/devices/phone-1", nil)
	revoke.SetPathValue("id", "phone-1")
	revoke.Header.Set("Origin", "http://hub.example")
	revoke.AddCookie(ownerCookie)
	response = httptest.NewRecorder()
	hub.requireOwner(http.HandlerFunc(hub.revokeDevice)).ServeHTTP(response, revoke)
	if response.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", response.Code, response.Body.String())
	}
	if hub.authorizedNode(requestNode("phone-1", paired.Token), "phone-1", "android", legacy) {
		t.Fatal("revoked device could serve")
	}
	if hub.attachNodeChecked(nil, "phone-1", "手机", "android", requestNode("phone-1", paired.Token), legacy) {
		t.Fatal("revoked device attached after websocket authorization")
	}
	if node := hub.nodes["phone-1"]; node != nil && node.Conn != nil {
		t.Fatal("revoked device remained online")
	}
	status := httptest.NewRequest(http.MethodGet, "http://hub.example/api/session", nil)
	status.AddCookie(deviceCookie)
	response = httptest.NewRecorder()
	hub.sessionStatus(response, status)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked device could browse: %d", response.Code)
	}
	if !hub.authorizedNode(requestNode("old-node", legacy), "old-node", "desktop", legacy) {
		t.Fatal("legacy compatibility failed before shutdown")
	}
	settings := httptest.NewRequest(http.MethodPut, "http://hub.example/api/hub/devices/legacy", strings.NewReader(`{"enabled":false}`))
	settings.Header.Set("Origin", "http://hub.example")
	settings.AddCookie(ownerCookie)
	response = httptest.NewRecorder()
	hub.requireOwner(http.HandlerFunc(hub.setLegacyNodes)).ServeHTTP(response, settings)
	if response.Code != http.StatusOK || hub.authorizedNode(requestNode("old-node", legacy), "old-node", "desktop", legacy) {
		t.Fatalf("legacy token was not disabled: %d", response.Code)
	}
	pair = httptest.NewRequest(http.MethodPost, "http://hub.example/api/hub/devices", strings.NewReader(`{"id":"phone-1","name":"手机","kind":"android","canBrowse":true,"canServe":true}`))
	pair.Header.Set("Origin", "http://hub.example")
	pair.AddCookie(ownerCookie)
	response = httptest.NewRecorder()
	hub.requireOwner(http.HandlerFunc(hub.pairDevice)).ServeHTTP(response, pair)
	if response.Code != http.StatusCreated {
		t.Fatalf("re-pair: %d", response.Code)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &paired); err != nil {
		t.Fatal(err)
	}
	deviceLogin = httptest.NewRequest(http.MethodPost, "http://hub.example/api/session/device", strings.NewReader(`{"id":"phone-1","token":"`+paired.Token+`"}`))
	deviceLogin.Header.Set("Origin", "http://hub.example")
	response = httptest.NewRecorder()
	hub.deviceLogin(response, deviceLogin)
	if response.Code != http.StatusOK {
		t.Fatalf("device re-login: %d", response.Code)
	}
	selfLogout := httptest.NewRequest(http.MethodDelete, "http://hub.example/api/session/device", nil)
	selfLogout.Header.Set("Origin", "http://hub.example")
	selfLogout.AddCookie(response.Result().Cookies()[0])
	response = httptest.NewRecorder()
	hub.deviceLogout(response, selfLogout)
	if response.Code != http.StatusNoContent || hub.authorizedNode(requestNode("phone-1", paired.Token), "phone-1", "android", legacy) {
		t.Fatalf("device self-revocation failed: %d", response.Code)
	}
}

func TestDesktopPairingUsesOwnerPasswordOnce(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	password := strings.Repeat("owner password ", 2)
	store, err := newHubAuthStore(password)
	if err != nil {
		t.Fatal(err)
	}
	hub := &remoteHub{auth: store, password: password, secure: true, nodes: map[string]*remoteNode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", hub.login)
	mux.HandleFunc("DELETE /api/session", hub.logout)
	mux.Handle("POST /api/hub/devices", hub.requireOwner(http.HandlerFunc(hub.pairDevice)))
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	client := server.Client()
	client.Jar, _ = cookiejar.New(nil)
	hubURL := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/node"
	if _, err := pairDesktopHubWithClient(client, hubURL, "desktop-1", "电脑", "wrong password"); err == nil {
		t.Fatal("wrong owner password paired a device")
	}
	token, err := pairDesktopHubWithClient(client, hubURL, "desktop-1", "电脑", password)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://hub.example/node", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	if !hub.authorizedNode(request, "desktop-1", "desktop", strings.Repeat("legacy", 6)) {
		t.Fatal("desktop credential did not authorize its node")
	}
}
