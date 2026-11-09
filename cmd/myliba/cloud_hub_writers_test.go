package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func hubTestRecord(format int, library, author, revision, at string, epoch int64) ([]byte, string) {
	record := cloudRecord{Format: format, LibraryID: library, ObjectID: "file-" + revision, RevisionID: revision, AuthorID: author, AuthorEpoch: epoch, CreatedAt: at, Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
	data, _ := json.Marshal(record)
	return data, cloudHash(data)
}

func TestHubWriterGateFormatAndEpoch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYLIBA_HUB_DATA_DIR", dir)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	hub.routes(mux, token)
	library := cloudHash([]byte("test-library"))

	put := func(data []byte, id string) int {
		req := httptest.NewRequest(http.MethodPut, "/cloud/catalog/"+id, bytes.NewReader(data))
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer "+token)
		resp := httptest.NewRecorder()
		mux.ServeHTTP(resp, req)
		return resp.Code
	}

	// Format 3 from a new author is accepted.
	data, id := hubTestRecord(3, library, "node-a", "v1", "2026-11-01T00:00:00Z", 0)
	if code := put(data, id); code != 204 {
		t.Fatalf("format 3 initial write: %d", code)
	}
	// Format 4 upgrades the author.
	data, id = hubTestRecord(4, library, "node-a", "v2", "2026-11-02T00:00:00Z", 1)
	if code := put(data, id); code != 204 {
		t.Fatalf("format 4 upgrade: %d", code)
	}
	// Late format 3 history (before upgrade) is still accepted.
	data, id = hubTestRecord(3, library, "node-a", "v0", "2026-10-31T00:00:00Z", 0)
	if code := put(data, id); code != 204 {
		t.Fatalf("late history: %d", code)
	}
	// New format 3 after upgrade is rejected.
	data, id = hubTestRecord(3, library, "node-a", "v3", "2026-11-03T00:00:00Z", 0)
	if code := put(data, id); code != 403 {
		t.Fatalf("format downgrade should be 403, got %d", code)
	}
	// Another author is unaffected.
	data, id = hubTestRecord(3, library, "node-b", "v1", "2026-11-03T00:00:00Z", 0)
	if code := put(data, id); code != 204 {
		t.Fatalf("other author format 3: %d", code)
	}
	// Stale epoch is rejected with 409 (conflict).
	data, id = hubTestRecord(4, library, "node-a", "v4", "2026-11-04T00:00:00Z", 0)
	if code := put(data, id); code != 409 {
		t.Fatalf("epoch 0 after epoch 1 should be rejected: %d", code)
	}
	// Higher epoch is accepted.
	data, id = hubTestRecord(4, library, "node-a", "v5", "2026-11-05T00:00:00Z", 2)
	if code := put(data, id); code != 204 {
		t.Fatalf("epoch upgrade: %d", code)
	}
	// Retire node-a via the writer endpoint.
	retireBody, _ := json.Marshal(map[string]any{"libraryId": library, "authorId": "node-a", "epoch": int64(2)})
	req := httptest.NewRequest(http.MethodPost, "/cloud/writers/retire", bytes.NewReader(retireBody))
	req.Header.Set("Authorization", "Bearer "+token)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != 200 {
		t.Fatalf("retire: %d %s", resp.Code, resp.Body.String())
	}
	// Retired author cannot write.
	data, id = hubTestRecord(4, library, "node-a", "v6", "2026-11-06T00:00:00Z", 2)
	if code := put(data, id); code != 403 {
		t.Fatalf("retired writer should be 403, got %d", code)
	}
	// Rejoin with higher epoch.
	rejoinBody, _ := json.Marshal(map[string]any{"libraryId": library, "authorId": "node-a", "epoch": int64(3)})
	req = httptest.NewRequest(http.MethodPost, "/cloud/writers/rejoin", bytes.NewReader(rejoinBody))
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	if resp.Code != 200 {
		t.Fatalf("rejoin: %d %s", resp.Code, resp.Body.String())
	}
	// After rejoin, writing with new epoch succeeds.
	data, id = hubTestRecord(4, library, "node-a", "v7", "2026-11-07T00:00:00Z", 3)
	if code := put(data, id); code != 204 {
		t.Fatalf("post-rejoin write: %d", code)
	}
	// Restart preserves the gate.
	hub2, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	mux2 := http.NewServeMux()
	hub2.routes(mux2, token)
	data, id = hubTestRecord(3, library, "node-a", "v8", "2026-11-08T00:00:00Z", 3)
	req = httptest.NewRequest(http.MethodPut, "/cloud/catalog/"+id, bytes.NewReader(data))
	req.SetPathValue("id", id)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	mux2.ServeHTTP(resp, req)
	if resp.Code != 403 {
		t.Fatalf("restart lost format gate: %d", resp.Code)
	}
	// Capabilities reflect the gate.
	req = httptest.NewRequest(http.MethodGet, "/cloud/capabilities?libraryId="+library+"&authorId=node-a", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp = httptest.NewRecorder()
	mux2.ServeHTTP(resp, req)
	var caps struct {
		MinWriterFormat int   `json:"minWriterFormat"`
		WriterEpoch     int64 `json:"writerEpoch"`
		HubWriterGate   bool  `json:"hubWriterGate"`
	}
	if resp.Code != 200 || json.Unmarshal(resp.Body.Bytes(), &caps) != nil {
		t.Fatalf("capabilities: %d %s", resp.Code, resp.Body.String())
	}
	if caps.MinWriterFormat != 4 || caps.WriterEpoch != 3 || !caps.HubWriterGate {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

func TestHubWriterGateRebuildFromCatalog(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MYLIBA_HUB_DATA_DIR", dir)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	library := cloudHash([]byte("rebuild-library"))
	// Simulate pre-existing catalog records without a writer registry file.
	data, id := hubTestRecord(4, library, "legacy-node", "r1", "2026-11-01T00:00:00Z", 1)
	if err := hub.disk.put("catalog", id, bytes.NewReader(data), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	// Rebuild should pick up the format gate.
	if err := hub.writers.rebuildFromCatalog(hub.disk); err != nil {
		t.Fatal(err)
	}
	state, ok := hub.writers.writerState(library, "legacy-node")
	if !ok || state.MinFormat != 4 || state.Epoch != 1 {
		t.Fatalf("rebuild did not recover writer state: %+v", state)
	}
}