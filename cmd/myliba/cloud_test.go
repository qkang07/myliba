package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCloudHubOverviewRetainsOfflineNodes(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("t", 32)
	controller := &remoteHub{sessions: map[string]time.Time{"session": time.Now().Add(time.Hour)}}
	mux := http.NewServeMux()
	hub.routes(mux, token)
	mux.Handle("GET /api/hub/cloud/overview", controller.requireSession(http.HandlerFunc(hub.overview)))
	blockBytes := int64(125)
	capacity, available := uint64(1000), uint64(700)
	input, _ := json.Marshal(cloudNode{ID: "node-1", Endpoint: "https://node.example", Role: "storage", cloudStorageStats: cloudStorageStats{
		Backend: "disk", StatsReady: true, BlockCount: 2, BlockBytes: &blockBytes, CatalogCount: 1, CapacityBytes: &capacity, AvailableBytes: &available,
	}})
	request := httptest.NewRequest(http.MethodPost, "/cloud/nodes/heartbeat", bytes.NewReader(input))
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("heartbeat: %d %s", response.Code, response.Body.String())
	}
	readOverview := func(auth bool) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/hub/cloud/overview", nil)
		if auth {
			request.AddCookie(&http.Cookie{Name: hubSessionCookie, Value: "session"})
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		var body map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		return response.Code, body
	}
	if code, _ := readOverview(false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated overview: %d", code)
	}
	if code, data := readOverview(true); code != 200 || data["online"] != float64(1) || data["reportedBlockBytes"] != float64(125) || data["knownAvailableBytes"] != float64(700) {
		t.Fatalf("online overview: %d %+v", code, data)
	}
	hub.mu.Lock()
	node := hub.nodes["node-1"]
	node.SeenAt = time.Now().Add(-time.Minute)
	hub.nodes[node.ID] = node
	if err := hub.saveNodesLocked(); err != nil {
		t.Fatal(err)
	}
	hub.mu.Unlock()
	request = httptest.NewRequest(http.MethodGet, "/cloud/nodes", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var discovery struct {
		Nodes []cloudNode `json:"nodes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &discovery); err != nil || len(discovery.Nodes) != 0 {
		t.Fatalf("offline discovery: %s %v", response.Body.String(), err)
	}
	if code, data := readOverview(true); code != 200 || data["offline"] != float64(1) || data["reportedBlockBytes"] != float64(0) {
		t.Fatalf("offline overview: %d %+v", code, data)
	}
	restored, err := newCloudHub()
	if err != nil || len(restored.nodes) != 1 || restored.nodes["node-1"].BlockBytes == nil || *restored.nodes["node-1"].BlockBytes != 125 {
		t.Fatalf("restored nodes: %+v %v", restored, err)
	}
	response = httptest.NewRecorder()
	restored.overview(response, httptest.NewRequest(http.MethodGet, "/api/hub/cloud/overview", nil))
	var persisted map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &persisted); err != nil || persisted["offline"] != float64(1) {
		t.Fatalf("persisted node must restart offline: %+v %v", persisted, err)
	}
}

func TestCloudHubRemoveOfflineNode(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	cloud, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	cloud.mu.Lock()
	cloud.nodes[id] = cloudNode{ID: id, Role: "storage", SeenAt: time.Now().UTC()}
	cloud.heartbeatSeen[id] = true
	if err := cloud.saveNodesLocked(); err != nil {
		t.Fatal(err)
	}
	cloud.mu.Unlock()
	hub := &remoteHub{sessions: map[string]time.Time{"session": time.Now().Add(time.Hour)}}
	mux := http.NewServeMux()
	mux.Handle("DELETE /api/hub/cloud/nodes/{id}", hub.requireSession(http.HandlerFunc(cloud.removeOfflineNode)))
	remove := func(auth bool) int {
		t.Helper()
		request := httptest.NewRequest(http.MethodDelete, "/api/hub/cloud/nodes/"+id, nil)
		request.Header.Set("Origin", "http://example.com")
		if auth {
			request.AddCookie(&http.Cookie{Name: hubSessionCookie, Value: "session"})
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response.Code
	}
	if code := remove(false); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated removal: %d", code)
	}
	if code := remove(true); code != http.StatusConflict {
		t.Fatalf("online removal: %d", code)
	}
	cloud.mu.Lock()
	node := cloud.nodes[id]
	node.SeenAt = time.Now().Add(-time.Minute)
	cloud.nodes[id] = node
	cloud.mu.Unlock()
	if code := remove(true); code != http.StatusNoContent {
		t.Fatalf("offline removal: %d", code)
	}
	restored, err := newCloudHub()
	if err != nil || len(restored.nodes) != 0 {
		t.Fatalf("node registry still contains removed node: %+v %v", restored, err)
	}
}

func testCloudRuntime(t *testing.T, dir, token string) *cloudRuntime {
	t.Helper()
	disk, err := newCloudDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &cloudRuntime{disk: disk, id: newID(), role: "storage", token: token, client: &http.Client{}, locations: map[string][]cloudNode{}, grantForPeer: func(_ context.Context, presented string) (string, error) { return presented, nil }}
}

func serveCloudTestNode(t *testing.T, runtime *cloudRuntime, token string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	runtime.disk.routes(mux, token)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	runtime.endpoint = server.URL
	return server
}

func TestCloudSingleLocalNodeWithoutHubOrToken(t *testing.T) {
	t.Setenv("MYLIBA_CLOUD_TOKEN", "")
	dir := t.TempDir()
	t.Setenv("MYLIBA_CLOUD_DATA_DIR", filepath.Join(dir, "node"))
	t.Setenv("MYLIBA_CLOUD_PUBLIC_URL", "")
	t.Setenv("MYLIBA_CLOUD_RELAY_URL", "")
	runtime, err := newCloudRuntime(nil, false)
	if err != nil || runtime.endpoint != "" {
		t.Fatalf("local runtime: %+v, %v", runtime, err)
	}
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("one local node is a complete encrypted cloud")
	path := filepath.Join(dir, "single.txt")
	if err := os.WriteFile(path, plain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.uploadFile(context.Background(), "single-file", path, false); err != nil {
		t.Fatal(err)
	}
	if err := runtime.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err := client.validRecords()
	if err != nil || len(records) != 1 || len(records[0].BlockIDs) != 1 {
		t.Fatalf("local catalog: %+v, %v", records, err)
	}
	blockID := records[0].BlockIDs[0]
	if !runtime.disk.valid("blocks", blockID) || len(runtime.blockLocations(blockID)) != 1 || len(runtime.activePeers()) != 0 {
		t.Fatal("local block must be indexed as one replica without peers")
	}
	listResponse := httptest.NewRecorder()
	client.list(listResponse, httptest.NewRequest(http.MethodGet, "/api/cloud/files", nil))
	var listing map[string]json.RawMessage
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listing); listResponse.Code != http.StatusOK || err != nil || string(listing["peers"]) != "[]" {
		t.Fatalf("single-node cloud listing must return an empty peers array: status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}
	reopenedRuntime, err := newCloudRuntime(nil, false)
	if err != nil || reopenedRuntime.id != runtime.id {
		t.Fatalf("reopened node: %+v, %v", reopenedRuntime, err)
	}
	reopened, err := newCloudClient(reopenedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/cloud/files/single-file/file", nil)
	request.SetPathValue("id", "single-file")
	response := httptest.NewRecorder()
	reopened.download(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), plain) {
		t.Fatalf("local download: status=%d bytes=%d", response.Code, response.Body.Len())
	}
}

func TestCloudEncryptedUploadReplicasAndRecovery(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	dir := t.TempDir()
	source := testCloudRuntime(t, filepath.Join(dir, "source"), token)
	peerA := testCloudRuntime(t, filepath.Join(dir, "a"), token)
	peerB := testCloudRuntime(t, filepath.Join(dir, "b"), token)
	serverA := serveCloudTestNode(t, peerA, token)
	serverB := serveCloudTestNode(t, peerB, token)
	source.peers = []cloudNode{{ID: peerA.id, Endpoint: serverA.URL}, {ID: peerB.id, Endpoint: serverB.URL}}
	client, err := newCloudClient(source)
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, cloudChunkSize+341)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "example.bin")
	if err := os.WriteFile(path, plain, 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.uploadFile(context.Background(), "resource-1", path, false); err != nil {
		t.Fatal(err)
	}
	records, err := client.validRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("records: %v, %v", records, err)
	}
	manifest, err := client.openManifest(records[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) != 2 {
		t.Fatalf("chunks=%d", len(manifest.Chunks))
	}
	for _, chunk := range manifest.Chunks {
		if !peerA.disk.has("blocks", chunk.ID) || !peerB.disk.has("blocks", chunk.ID) {
			t.Fatal("encrypted chunk not replicated to both peers")
		}
		raw, err := os.ReadFile(filepath.Join(source.disk.dir, "blocks", chunk.ID[:2], chunk.ID))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(raw, plain[:len(raw)]) {
			t.Fatal("stored plaintext")
		}
	}
	// Simulate loss of the original client: retain only a copied catalog and
	// the exported recovery key; fetch content from the two storage nodes.
	recovered := testCloudRuntime(t, filepath.Join(dir, "recovered"), token)
	recovered.peers = source.peers
	// A stale index entry must not hide a live replica discovered from peers.
	recovered.locations = map[string][]cloudNode{}
	for _, chunk := range manifest.Chunks {
		recovered.locations[chunk.ID] = []cloudNode{{ID: "stale", Endpoint: "http://127.0.0.1:1"}}
	}
	if err := os.WriteFile(filepath.Join(recovered.disk.dir, "recovery.key"), client.key[:], 0600); err != nil {
		t.Fatal(err)
	}
	recordIDs, err := source.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range recordIDs {
		file, err := source.disk.get("catalog", id)
		if err != nil {
			t.Fatal(err)
		}
		if err := recovered.disk.put("catalog", id, file, cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	recoveredClient, err := newCloudClient(recovered)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/cloud/files/resource-1/file", nil)
	request.SetPathValue("id", "resource-1")
	response := httptest.NewRecorder()
	recoveredClient.download(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), plain) {
		t.Fatalf("recovery failed: status=%d bytes=%d", response.Code, response.Body.Len())
	}
}

func TestCloudOpaqueCatalogAndRelayBoundary(t *testing.T) {
	disk, err := newCloudDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	disk.routes(mux, token)
	data, err := json.Marshal(cloudRecord{
		Format: 3, LibraryID: cloudHash([]byte("library")), ObjectID: "file-1",
		RevisionID: "revision-1", AuthorID: "node-1", CreatedAt: now(),
		Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := cloudHash(data)
	request := httptest.NewRequest(http.MethodPut, "/cloud/catalog/"+id, bytes.NewReader(data))
	request.SetPathValue("id", id)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("store catalog: %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/cloud/catalog", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var listing struct {
		Records []string `json:"records"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Records) != 1 || listing.Records[0] != id {
		t.Fatalf("catalog list: %+v", listing)
	}
	request = httptest.NewRequest(http.MethodGet, "/cloud/catalog/"+id, nil)
	request.SetPathValue("id", id)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("catalog accepted unauthenticated access")
	}
	for _, route := range []string{"/api/resources/abc/file", "/api/resources/abc/thumbnail", "/api/cloud/files/abc/file"} {
		if !allowedHubRelayRequest(http.MethodGet, route) || !hubRelayContentPath(route) {
			t.Fatalf("limited Hub fallback missing: %s", route)
		}
		if !allowedRemoteRequest(http.MethodGet, route) {
			t.Fatalf("direct route blocked: %s", route)
		}
	}
}

func TestCloudInventoryPaginationAndHashValidation(t *testing.T) {
	disk, err := newCloudDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	token := "0123456789abcdef0123456789abcdef"
	mux := http.NewServeMux()
	disk.routes(mux, token)
	data := []byte("ciphertext")
	id := cloudHash(data)
	if err := disk.put("blocks", id, bytes.NewReader(data), cloudMaxBlobSize); err != nil {
		t.Fatal(err)
	}
	if err := disk.put("blocks", id, bytes.NewReader([]byte("wrong")), cloudMaxBlobSize); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/cloud/inventory", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var page struct {
		Blocks []string `json:"blocks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Blocks) != 1 || page.Blocks[0] != id {
		t.Fatalf("inventory: %+v", page)
	}
	file, err := disk.get("blocks", id)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	roundtrip, err := io.ReadAll(file)
	if err != nil || !bytes.Equal(roundtrip, data) {
		t.Fatal("immutable block changed")
	}
}

func TestCloudEmptyFile(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.uploadFile(context.Background(), "empty", path, false); err != nil {
		t.Fatal(err)
	}
	rec, manifest, err := client.findRecord("empty")
	if err != nil || rec.ObjectID != "empty" || len(manifest.Chunks) != 0 {
		t.Fatalf("empty file: %+v %+v %v", rec, manifest, err)
	}
}

func TestCloudHubDiscoveryIndexSyncAndRepair(t *testing.T) {
	token := "0123456789abcdef0123456789abcdef"
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	hubMux := http.NewServeMux()
	hub.routes(hubMux, token)
	hubServer := httptest.NewServer(hubMux)
	defer hubServer.Close()
	source := testCloudRuntime(t, t.TempDir(), token)
	storage := testCloudRuntime(t, t.TempDir(), token)
	sourceServer := serveCloudTestNode(t, source, token)
	storageServer := serveCloudTestNode(t, storage, token)
	source.hubURL = "ws" + strings.TrimPrefix(hubServer.URL, "http") + "/node"
	storage.hubURL = source.hubURL
	source.endpoint = sourceServer.URL
	storage.endpoint = storageServer.URL
	if err := source.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := storage.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := source.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	client, err := newCloudClient(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "one.txt")
	if err := os.WriteFile(path, []byte("cloud test"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := client.uploadFile(context.Background(), "file-1", path, false); err != nil {
		t.Fatal(err)
	}
	if err := source.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := storage.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids, err := storage.disk.list("catalog")
	if err != nil || len(ids) != 1 {
		t.Fatalf("storage catalog: %v %v", ids, err)
	}
	if ids, err = hub.disk.list("catalog"); err != nil || len(ids) != 1 {
		t.Fatalf("Hub catalog: %v %v", ids, err)
	}
	records, err := client.validRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || !storage.disk.has("blocks", records[0].BlockIDs[0]) {
		t.Fatal("storage failed to obtain direct block copy")
	}
	recovered := testCloudRuntime(t, t.TempDir(), token)
	recovered.bootstrap = []cloudNode{{ID: storage.id, Endpoint: storageServer.URL, Role: "storage"}}
	recovered.peers = recovered.bootstrap
	if err := os.WriteFile(filepath.Join(recovered.disk.dir, "recovery.key"), client.key[:], 0600); err != nil {
		t.Fatal(err)
	}
	if err := recovered.syncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	recoveredClient, err := newCloudClient(recovered)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/cloud/files/file-1/file", nil)
	request.SetPathValue("id", "file-1")
	response := httptest.NewRecorder()
	recoveredClient.download(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "cloud test" {
		t.Fatalf("bootstrap recovery: %d %q", response.Code, response.Body.String())
	}
}

func TestCloudRecoveryKeyImportAfterOpaqueCatalogSync(t *testing.T) {
	ownerRuntime := testCloudRuntime(t, t.TempDir(), "")
	owner, err := newCloudClient(ownerRuntime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("recovered note"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := owner.uploadFile(context.Background(), "note", path, false); err != nil {
		t.Fatal(err)
	}

	newRuntime := testCloudRuntime(t, t.TempDir(), "")
	newClient, err := newCloudClient(newRuntime)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := ownerRuntime.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		file, err := ownerRuntime.disk.get("catalog", id)
		if err != nil {
			t.Fatal(err)
		}
		err = newRuntime.disk.put("catalog", id, file, cloudMaxRecordSize)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if records, err := newClient.validRecords(); err != nil || len(records) != 0 {
		t.Fatalf("opaque records before import: %d, %v", len(records), err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:4317/api/cloud/recovery",
		strings.NewReader(`{"recoveryKey":"`+hex.EncodeToString(owner.key[:])+`"}`))
	request.Header.Set("X-Myliba-Local", "1")
	request.Header.Set("Origin", "http://127.0.0.1:4317")
	response := httptest.NewRecorder()
	newClient.recovery(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("import after sync: %d %s", response.Code, response.Body.String())
	}
	if records, err := newClient.validRecords(); err != nil || len(records) != 1 {
		t.Fatalf("restored records: %d, %v", len(records), err)
	}
}

func TestCloudHistoryOnlyIncludesCurrentEnabledRun(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "draft.txt")
	for index, history := range []bool{false, true, true, false, true} {
		if err := os.WriteFile(path, []byte{byte(index)}, 0600); err != nil {
			t.Fatal(err)
		}
		if err := client.uploadFile(context.Background(), "draft", path, history); err != nil {
			t.Fatal(err)
		}
		versions, err := client.historyRecords("draft")
		if err != nil {
			t.Fatal(err)
		}
		want := []int{0, 1, 2, 0, 1}[index]
		if len(versions) != want {
			t.Fatalf("revision %d: history=%d, want %d", index, len(versions), want)
		}
	}
}
