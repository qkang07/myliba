package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileShareCapabilityAndRevocation(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE hub_profiles(id TEXT PRIMARY KEY,url TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hub_profiles(id,url) VALUES('hub','wss://hub.example/node')`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.txt")
	if err := os.WriteFile(path, []byte("shared bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?)`, dir, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES('resource','root',?,'photo.txt',12,?,'document',1,?,?)`, path, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	service := &shareService{db: db, hubs: &hubProfiles{db: db, activeID: "hub"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/resources/{id}/shares", service.create)
	mux.HandleFunc("GET /api/resources/{id}/shares", service.list)
	mux.HandleFunc("POST /api/shares/{id}/redeem", service.redeem)
	mux.HandleFunc("GET /api/shares/{id}/file", service.file)
	mux.HandleFunc("DELETE /api/shares/{id}", service.revoke)
	call := func(method, path string, body []byte, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if token != "" {
			req.Header.Set("X-Myliba-Share-Key", token)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	created := call(http.MethodPost, "/api/resources/resource/shares", []byte(`{"expiresInHours":24}`), "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var result struct {
		Share fileShare `json:"share"`
		URL   string    `json:"url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(result.URL)
	if err != nil || link.Host != "hub.example" || link.Path != "/s/"+result.Share.ID || !validShareID(result.Share.ID) {
		t.Fatalf("invalid share URL: %s", result.URL)
	}
	token := strings.TrimPrefix(link.Fragment, "key=")
	if len(token) != 64 {
		t.Fatal("share key was not generated")
	}
	listed := call(http.MethodGet, "/api/resources/resource/shares", nil, "")
	if listed.Code != 200 || strings.Contains(listed.Body.String(), token) {
		t.Fatalf("list exposed token: %d %s", listed.Code, listed.Body.String())
	}
	fileURL := "/api/shares/" + result.Share.ID + "/file"
	if got := call(http.MethodGet, fileURL, nil, "").Code; got != http.StatusUnauthorized {
		t.Fatalf("file without key: %d", got)
	}
	if got := call(http.MethodGet, fileURL, nil, strings.Repeat("0", 64)).Code; got != http.StatusUnauthorized {
		t.Fatalf("file with wrong key: %d", got)
	}
	redeemURL := "/api/shares/" + result.Share.ID + "/redeem"
	if got := call(http.MethodPost, redeemURL, []byte(`{"token":"bad"}`), "").Code; got != http.StatusUnauthorized {
		t.Fatalf("redeem with wrong key: %d", got)
	}
	validBody, _ := json.Marshal(map[string]string{"token": token})
	if got := call(http.MethodPost, redeemURL, validBody, "").Code; got != http.StatusOK {
		t.Fatalf("redeem with key: %d", got)
	}
	file := call(http.MethodGet, fileURL, nil, token)
	if file.Code != http.StatusOK || file.Body.String() != "shared bytes" {
		t.Fatalf("authorized file: %d %s", file.Code, file.Body.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodGet, fileURL, nil, token).Code; got != http.StatusGone {
		t.Fatalf("file after source change: %d", got)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodDelete, "/api/shares/"+result.Share.ID, nil, "").Code; got != http.StatusOK {
		t.Fatalf("revoke: %d", got)
	}
	if got := call(http.MethodGet, fileURL, nil, token).Code; got != http.StatusGone {
		t.Fatalf("file after revocation: %d", got)
	}
	if got := call(http.MethodPost, redeemURL, validBody, "").Code; got != http.StatusGone {
		t.Fatalf("redeem after revocation: %d", got)
	}
}

func TestShareRemoteRouteBoundaries(t *testing.T) {
	id := newID()
	if !allowedRemoteRequest(http.MethodPost, "/api/shares/"+id+"/redeem") || !allowedRemoteRequest(http.MethodGet, "/api/shares/"+id+"/file") {
		t.Fatal("share protocol routes missing from node allowlist")
	}
	if allowedHubRelayRequest(http.MethodGet, "/api/shares/"+id+"/file") {
		t.Fatal("share file bytes must not go through Hub relay")
	}
	if allowedRemoteRequest(http.MethodGet, "/api/shares/"+id+"/redeem") || allowedRemoteRequest(http.MethodPost, "/api/shares/"+id+"/file") {
		t.Fatal("share protocol allowed an unsupported method")
	}
	key := http.Header{"X-Myliba-Share-Key": []string{strings.Repeat("a", 64)}}
	if !allowedShareRTCRequest(id, wireMessage{Method: http.MethodGet, Path: "/api/shares/" + id + "/file", Headers: key}) {
		t.Fatal("share session cannot request its own file")
	}
	for _, path := range []string{"/api/resources/other/file", "/api/shares/" + newID() + "/file", "/api/shares/" + id + "/file?download=1"} {
		if allowedShareRTCRequest(id, wireMessage{Method: http.MethodGet, Path: path, Headers: key}) {
			t.Fatalf("share session escaped its file: %s", path)
		}
	}
	if allowedShareRTCRequest(id, wireMessage{Method: http.MethodGet, Path: "/api/shares/" + id + "/file"}) {
		t.Fatal("share session accepted a file request without the key")
	}
	forwarded := filteredHeaders(http.Header{"x-myliba-share-key": []string{strings.Repeat("b", 64)}}, false)
	if forwarded.Get("X-Myliba-Share-Key") != strings.Repeat("b", 64) {
		t.Fatal("browser-lowercase share key was dropped before reaching node")
	}
	if !allowedShareRTCRequest(id, wireMessage{Method: http.MethodGet, Path: "/api/shares/" + id + "/file", Headers: http.Header{"x-myliba-share-key": []string{strings.Repeat("b", 64)}}}) {
		t.Fatal("share request rejected the browser's lowercase header")
	}
}

func TestPublicSharePageServesEmbeddedApp(t *testing.T) {
	static, err := fs.Sub(assets, "web/dist")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /s/{id}", shareIndex(http.FS(static)))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/s/"+newID(), nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `id="app"`) || !strings.Contains(response.Body.String(), "/assets/index-") {
		t.Fatalf("share page: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/s/bad", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("invalid share page ID: %d", response.Code)
	}
}
