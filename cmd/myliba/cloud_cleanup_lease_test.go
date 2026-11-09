package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHubCleanupLeaseAuthorizesAndProtectsWrites(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	token := strings.Repeat("l", 32)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	library := cloudHash([]byte("library"))
	putRecord := func(object, stamp string) string {
		record := cloudRecord{Format: 4, LibraryID: library, ObjectID: object, RevisionID: object, AuthorID: "writer", CreatedAt: stamp, Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		id := cloudHash(raw)
		if err := hub.disk.put("catalog", id, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
		return id
	}
	snapshotID := putRecord("library-snapshot/writer", "2020-02-01T00:00:00Z")
	putRecord("library-receipt/phone", "2020-02-01T00:00:00Z")
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
	authorize := func(request cleanupAuthorizeRequest) *httptest.ResponseRecorder {
		payload, _ := json.Marshal(request)
		return serve("POST", "/cloud/cleanup/authorize", payload)
	}
	request := cleanupAuthorizeRequest{LibraryID: library, Generation: generation, SnapshotFrontiers: map[string]int64{"writer": 3}, ActiveMembers: []string{"phone"}, SnapshotHashes: map[string]string{"writer": snapshotID}}
	missingReceipt := request
	missingReceipt.ActiveMembers = []string{"ghost"}
	if response := authorize(missingReceipt); response.Code != http.StatusConflict {
		t.Fatalf("missing persisted receipt must block authorization: %d", response.Code)
	}
	missingSnapshot := request
	missingSnapshot.SnapshotHashes = map[string]string{"writer": cloudHash([]byte("absent"))}
	if response := authorize(missingSnapshot); response.Code != http.StatusConflict {
		t.Fatalf("missing persisted snapshot must block authorization: %d", response.Code)
	}
	stale := request
	stale.Generation = cloudHash([]byte("stale"))
	if response := authorize(stale); response.Code != http.StatusConflict {
		t.Fatalf("stale generation must block authorization: %d", response.Code)
	}
	response := authorize(request)
	if response.Code != http.StatusOK {
		t.Fatalf("authorize: %d %s", response.Code, response.Body.String())
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &grant); err != nil || !validCloudHash(grant.Token) {
		t.Fatalf("invalid lease grant: %v %s", err, response.Body.String())
	}
	if response := authorize(request); response.Code != http.StatusConflict {
		t.Fatalf("concurrent cleanup must be rejected: %d", response.Code)
	}
	content := cloudRecord{Format: 4, LibraryID: library, ObjectID: "file-new", RevisionID: "revision", AuthorID: "writer", CreatedAt: now(), Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
	contentRaw, _ := json.Marshal(content)
	contentID := cloudHash(contentRaw)
	if response := serve("PUT", "/cloud/catalog/"+contentID, contentRaw); response.Code != http.StatusLocked {
		t.Fatalf("catalog write during cleanup lease: %d", response.Code)
	}
	prunePayload, _ := json.Marshal(catalogPruneRequest{Generation: generation, IDs: []string{snapshotID}, Snapshots: map[string]string{"writer": snapshotID}})
	if response := serve("POST", "/cloud/catalog/prune", prunePayload); response.Code != http.StatusLocked {
		t.Fatalf("prune during cleanup lease: %d", response.Code)
	}
	verify := func(leaseToken, leaseGeneration string) int {
		payload, _ := json.Marshal(map[string]string{"token": leaseToken, "generation": leaseGeneration})
		return serve("POST", "/cloud/cleanup/verify", payload).Code
	}
	if code := verify(grant.Token, generation); code != http.StatusOK {
		t.Fatalf("verify active lease: %d", code)
	}
	if code := verify(grant.Token, cloudHash([]byte("other"))); code != http.StatusConflict {
		t.Fatalf("verify wrong generation: %d", code)
	}
	if code := verify(cloudHash([]byte("wrong")), generation); code != http.StatusConflict {
		t.Fatalf("verify wrong token: %d", code)
	}
	releasePayload, _ := json.Marshal(map[string]string{"token": grant.Token})
	if response := serve("POST", "/cloud/cleanup/release", releasePayload); response.Code != http.StatusNoContent {
		t.Fatalf("release: %d", response.Code)
	}
	if code := verify(grant.Token, generation); code != http.StatusConflict {
		t.Fatalf("verify released lease: %d", code)
	}
	if response := serve("PUT", "/cloud/catalog/"+contentID, contentRaw); response.Code != http.StatusNoContent {
		t.Fatalf("catalog write after release: %d %s", response.Code, response.Body.String())
	}
}

func TestHubCleanupLeaseExpiresAndRestartFailsClosed(t *testing.T) {
	t.Setenv("MYLIBA_HUB_DATA_DIR", t.TempDir())
	token := strings.Repeat("e", 32)
	hub, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	library := cloudHash([]byte("library"))
	record := cloudRecord{Format: 4, LibraryID: library, ObjectID: "library-snapshot/writer", RevisionID: "snapshot", AuthorID: "writer", CreatedAt: "2020-02-01T00:00:00Z", Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
	raw, _ := json.Marshal(record)
	snapshotID := cloudHash(raw)
	if err := hub.disk.put("catalog", snapshotID, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	receipt := record
	receipt.ObjectID = "library-receipt/phone"
	receiptRaw, _ := json.Marshal(receipt)
	if err := hub.disk.put("catalog", cloudHash(receiptRaw), bytes.NewReader(receiptRaw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
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
	payload, _ := json.Marshal(cleanupAuthorizeRequest{LibraryID: library, Generation: generation, SnapshotFrontiers: map[string]int64{"writer": 1}, ActiveMembers: []string{"phone"}, SnapshotHashes: map[string]string{"writer": snapshotID}})
	response := serve("POST", "/cloud/cleanup/authorize", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("authorize: %d %s", response.Code, response.Body.String())
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}
	hub.pruneMu.Lock()
	hub.lease.expiresAt = time.Now().Add(-time.Second)
	hub.pruneMu.Unlock()
	verifyPayload, _ := json.Marshal(map[string]string{"token": grant.Token, "generation": generation})
	if response := serve("POST", "/cloud/cleanup/verify", verifyPayload); response.Code != http.StatusConflict {
		t.Fatalf("expired lease must fail closed: %d", response.Code)
	}
	fresh := cloudRecord{Format: 4, LibraryID: library, ObjectID: "file-fresh", RevisionID: "revision", AuthorID: "writer", CreatedAt: now(), Nonce: strings.Repeat("0", 24), Sealed: strings.Repeat("a", 32), BlockIDs: []string{}}
	freshRaw, _ := json.Marshal(fresh)
	if response := serve("PUT", "/cloud/catalog/"+cloudHash(freshRaw), freshRaw); response.Code != http.StatusNoContent {
		t.Fatalf("expired lease must not block writes: %d", response.Code)
	}
	ids, err = hub.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(cleanupAuthorizeRequest{LibraryID: library, Generation: cloudHash([]byte(strings.Join(ids, "\x00"))), SnapshotFrontiers: map[string]int64{"writer": 1}, ActiveMembers: []string{"phone"}, SnapshotHashes: map[string]string{"writer": snapshotID}})
	response = serve("POST", "/cloud/cleanup/authorize", payload)
	if response.Code != http.StatusOK {
		t.Fatalf("re-authorize after expiry: %d %s", response.Code, response.Body.String())
	}
	restarted, err := newCloudHub()
	if err != nil {
		t.Fatal(err)
	}
	if restarted.lease != nil {
		t.Fatal("lease must not survive a Hub restart")
	}
}