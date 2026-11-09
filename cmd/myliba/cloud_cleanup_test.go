package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloudObjectStatUsesNewestReplica(t *testing.T) {
	disk, err := newCloudDisk(filepath.Join(t.TempDir(), "one", "cloud"))
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("duplicate block")
	id := cloudHash(data)
	if err := disk.put("blocks", id, bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	first, _ := disk.existingPath("blocks", id)
	old := time.Now().Add(-libraryCleanupRetention - time.Hour)
	if err := os.Chtimes(first, old, old); err != nil {
		t.Fatal(err)
	}
	secondRoot := filepath.Join(t.TempDir(), "two", "cloud")
	second := filepath.Join(secondRoot, "blocks", id[:2], id)
	if err := os.MkdirAll(filepath.Dir(second), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, data, 0600); err != nil {
		t.Fatal(err)
	}
	disk.roots = append(disk.roots, cloudDiskRoot{path: filepath.Dir(secondRoot), cloudDir: secondRoot})
	modified, size, err := disk.objectStat("blocks", id)
	if err != nil || time.Since(modified) >= libraryCleanupRetention || size != 2*int64(len(data)) {
		t.Fatalf("new replica must block cleanup: %v %d %v", modified, size, err)
	}
}

func TestStorageCleanupRechecksHubReferences(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	token := strings.Repeat("c", 32)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	hubMux := http.NewServeMux()
	hub.routes(hubMux, token)
	hubServer := httptest.NewServer(hubMux)
	defer hubServer.Close()
	storage := testCloudRuntime(t, t.TempDir(), token)
	storage.hubURL = "ws" + strings.TrimPrefix(hubServer.URL, "http") + "/node"
	block := []byte("old orphan block")
	id := cloudHash(block)
	if err := storage.disk.put("blocks", id, bytes.NewReader(block), int64(len(block))); err != nil {
		t.Fatal(err)
	}
	path, err := storage.disk.existingPath("blocks", id)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-libraryCleanupRetention - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	plan, err := storage.makeBlockCleanupPlan(context.Background())
	if err != nil || len(plan.Eligible) != 1 || plan.Eligible[0].ID != id {
		t.Fatalf("old orphan plan: %+v, %v", plan, err)
	}
	mux := http.NewServeMux()
	storage.cleanupRoutes(mux, func(next http.Handler) http.Handler { return cloudBearer(next, token) })
	leaseToken := strings.Repeat("d", 64)
	execute := func(planID, cleanupToken string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"planId": planID, "cleanupToken": cleanupToken})
		req := httptest.NewRequest("POST", "/cloud/cleanup/execute", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	if response := execute(plan.PlanID, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("execute without cleanup authorization: %d", response.Code)
	}
	record := cloudRecord{Format: 4, LibraryID: cloudHash([]byte("library")), ObjectID: "file", RevisionID: "revision", AuthorID: "author", CreatedAt: now(), Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{id}}
	raw, _ := json.Marshal(record)
	if err := hub.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if response := execute(plan.PlanID, leaseToken); response.Code != http.StatusConflict || !storage.disk.has("blocks", id) {
		t.Fatalf("stale plan deleted referenced block: %d %s", response.Code, response.Body.String())
	}
	second := []byte("second old orphan")
	secondID := cloudHash(second)
	if err := storage.disk.put("blocks", secondID, bytes.NewReader(second), int64(len(second))); err != nil {
		t.Fatal(err)
	}
	path, _ = storage.disk.existingPath("blocks", secondID)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	plan, err = storage.makeBlockCleanupPlan(context.Background())
	if err != nil || len(plan.Eligible) != 1 || plan.Eligible[0].ID != secondID {
		t.Fatalf("revised plan: %+v, %v", plan, err)
	}
	if response := execute(plan.PlanID, leaseToken); response.Code != http.StatusConflict || !storage.disk.has("blocks", secondID) {
		t.Fatalf("execute without Hub lease must fail closed: %d %s", response.Code, response.Body.String())
	}
	hub.pruneMu.Lock()
	hub.lease = &cleanupLease{token: leaseToken, generation: plan.CatalogGeneration, expiresAt: time.Now().Add(time.Minute)}
	hub.pruneMu.Unlock()
	if response := execute(plan.PlanID, leaseToken); response.Code != http.StatusOK || storage.disk.has("blocks", secondID) || !storage.disk.has("blocks", id) {
		t.Fatalf("orphan cleanup: %d %s", response.Code, response.Body.String())
	}
}
