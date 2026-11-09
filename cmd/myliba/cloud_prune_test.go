package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHubPruneMarkerStopsLateCatalogResurrection(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	token := strings.Repeat("p", 32)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	library := cloudHash([]byte("library"))
	makeRecord := func(object, revision, stamp string) ([]byte, string) {
		record := cloudRecord{Format: 4, LibraryID: library, ObjectID: object, RevisionID: revision, AuthorID: "writer", CreatedAt: stamp, Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return raw, cloudHash(raw)
	}
	operation, operationID := makeRecord("library-operation/writer/1", "operation-one", "2020-01-01T00:00:00Z")
	snapshot, snapshotID := makeRecord("library-snapshot/writer", "snapshot-one", "2020-02-01T00:00:00Z")
	for _, item := range []struct {
		raw []byte
		id  string
	}{{operation, operationID}, {snapshot, snapshotID}} {
		if err = hub.disk.put("catalog", item.id, bytes.NewReader(item.raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	hub.routes(mux, token)
	serve := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	ids, err := hub.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	request := catalogPruneRequest{Generation: cloudHash([]byte(strings.Join(ids, "\x00"))), IDs: []string{operationID}, Snapshots: map[string]string{"writer": snapshotID}}
	payload, _ := json.Marshal(request)
	if response := serve("POST", "/cloud/catalog/prune", payload); response.Code != 200 {
		t.Fatalf("prune: %d %s", response.Code, response.Body.String())
	}
	if response := serve("GET", "/cloud/catalog/"+operationID, nil); response.Code != http.StatusGone {
		t.Fatalf("pruned GET: %d", response.Code)
	}
	if response := serve("PUT", "/cloud/catalog/"+operationID, operation); response.Code != http.StatusGone {
		t.Fatalf("late old PUT: %d", response.Code)
	}
	if hub.disk.has("catalog", operationID) {
		t.Fatal("pruned record still on Hub disk")
	}
	hub, err = newCloudHub()
	if err != nil || !hub.pruned[operationID] {
		t.Fatalf("prune marker lost after restart: %v", err)
	}
	mux = http.NewServeMux()
	hub.routes(mux, token)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := testCloudRuntime(t, t.TempDir(), token)
	client.hubURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/node"
	if err = client.disk.put("catalog", operationID, bytes.NewReader(operation), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if err = client.syncCatalog(context.Background(), server.URL, token); err != nil {
		t.Fatal(err)
	}
	if client.disk.has("catalog", operationID) || !client.disk.has("catalog", snapshotID) {
		t.Fatal("client did not remove pruned record and retain snapshot")
	}
}

func TestHubPruneRecyclesDeletedFileContent(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	token := strings.Repeat("r", 32)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	library := cloudHash([]byte("library"))
	makeRecord := func(object, stamp string, blocks []string) ([]byte, string) {
		record := cloudRecord{Format: 4, LibraryID: library, ObjectID: object, RevisionID: object, AuthorID: "writer", CreatedAt: stamp, Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: blocks}
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return raw, cloudHash(raw)
	}
	snapshot, snapshotID := makeRecord("library-snapshot/writer", "2020-02-01T00:00:00Z", []string{})
	content, contentID := makeRecord("file-deleted", "2020-01-01T00:00:00Z", []string{cloudHash([]byte("block"))})
	recent, recentID := makeRecord("file-recent", now(), []string{})
	for _, item := range []struct {
		raw []byte
		id  string
	}{{snapshot, snapshotID}, {content, contentID}, {recent, recentID}} {
		if err = hub.disk.put("catalog", item.id, bytes.NewReader(item.raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	hub.routes(mux, token)
	serve := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	ids, err := hub.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	generation := cloudHash([]byte(strings.Join(ids, "\x00")))
	prune := func(request catalogPruneRequest) *httptest.ResponseRecorder {
		request.Generation = generation
		request.Snapshots = map[string]string{"writer": snapshotID}
		payload, _ := json.Marshal(request)
		return serve("POST", "/cloud/catalog/prune", payload)
	}
	if response := prune(catalogPruneRequest{ContentIDs: []string{contentID}}); response.Code != http.StatusBadRequest {
		t.Fatalf("content recycle without library identity: %d", response.Code)
	}
	if response := prune(catalogPruneRequest{LibraryID: library, ContentIDs: []string{recentID}}); response.Code != http.StatusConflict {
		t.Fatalf("content recycle before retention: %d", response.Code)
	}
	if response := prune(catalogPruneRequest{LibraryID: library, ContentIDs: []string{snapshotID}}); response.Code != http.StatusBadRequest {
		t.Fatalf("snapshot must not be recyclable as content: %d", response.Code)
	}
	if response := prune(catalogPruneRequest{LibraryID: cloudHash([]byte("other")), ContentIDs: []string{contentID}}); response.Code != http.StatusConflict {
		t.Fatalf("content recycle without persisted library snapshot: %d", response.Code)
	}
	if response := prune(catalogPruneRequest{LibraryID: library, ContentIDs: []string{contentID}}); response.Code != http.StatusOK {
		t.Fatalf("content recycle: %d %s", response.Code, response.Body.String())
	}
	if response := serve("GET", "/cloud/catalog/"+contentID, nil); response.Code != http.StatusGone {
		t.Fatalf("recycled content GET: %d", response.Code)
	}
	if response := serve("PUT", "/cloud/catalog/"+contentID, content); response.Code != http.StatusGone {
		t.Fatalf("late content re-upload must be rejected: %d", response.Code)
	}
	if hub.disk.has("catalog", contentID) || !hub.disk.has("catalog", snapshotID) || !hub.disk.has("catalog", recentID) {
		t.Fatal("content recycle removed the wrong records")
	}
	hub, err = newCloudHub()
	if err != nil || !hub.pruned[contentID] {
		t.Fatalf("content prune marker lost after restart: %v", err)
	}
}
