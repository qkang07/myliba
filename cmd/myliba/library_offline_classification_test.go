package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func classificationOfflineClient(t *testing.T) (*cloudClient, *server) {
	t.Helper()
	db := classificationTestDB(t)
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id, name string) {
		t.Helper()
		index := fileIndexView{FileID: id, RevisionID: "v1", Name: name, Modified: "2026-11-19T00:00:00Z", Original: indexedContent{Size: 4, Local: []indexedLocation{{NodeID: "phone", ResourceRef: id}}}, ContentSize: 4, OrganizationKnown: true}
		record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: id, RevisionID: "v1", AuthorID: "phone", CreatedAt: "2026-11-19T00:00:00Z", BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err = runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	publish("f1", "a.txt")
	publish("f2", "b.txt")
	libraryID, stamp := client.libraryID(), now()
	if _, err = db.Exec(`INSERT INTO classification_nodes(library_id,index_id,parent_id,name,name_key,origin,created_at,updated_at) VALUES
		(?, 'root', NULL, '资料', '资料', 'manual', ?, ?),
		(?, 'child', 'root', '项目', '项目', 'manual', ?, ?);
		INSERT INTO classification_memberships(library_id,file_id,index_id,has_manual,has_automatic,updated_at) VALUES
		(?, 'f1', 'root', 1, 0, ?),
		(?, 'f2', 'child', 0, 1, ?)`, libraryID, stamp, stamp, libraryID, stamp, stamp, libraryID, stamp, libraryID, stamp); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = rebuildClassificationAncestors(tx, libraryID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return client, &server{db: db, cloud: client}
}

func TestOfflineIndexCarriesClassificationAndExpiresOnMembershipChange(t *testing.T) {
	client, app := classificationOfflineClient(t)
	request := httptest.NewRequest(http.MethodGet, "/api/library/offline-index?scope=all&limit=1", nil)
	response := httptest.NewRecorder()
	client.offlineLibraryIndex(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("first offline page: %d %s", response.Code, response.Body.String())
	}
	var first struct {
		Items                   []offlineLibraryFile `json:"items"`
		ClassificationVersion   int                  `json:"classificationVersion"`
		ClassificationNodeTotal int                  `json:"classificationNodeTotal"`
		Generation              string               `json:"generation"`
		Next                    string               `json:"nextCursor"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.ClassificationVersion != 2 || first.ClassificationNodeTotal != 2 || len(first.Items) != 1 || len(first.Items[0].Indexes) != 1 || first.Items[0].Indexes[0].IndexID != "root" || !first.Items[0].Indexes[0].Manual || first.Next == "" {
		t.Fatalf("offline classification payload: %+v", first)
	}
	nodesResponse := httptest.NewRecorder()
	client.offlineLibraryIndex(nodesResponse, httptest.NewRequest(http.MethodGet, "/api/library/offline-index?scope=all&section=classification&limit=1&generation="+first.Generation, nil))
	var nodePage struct {
		Payload  string `json:"classificationPayload"`
		Encoding string `json:"classificationEncoding"`
		Next     string `json:"nextCursor"`
	}
	if nodesResponse.Code != http.StatusOK || json.Unmarshal(nodesResponse.Body.Bytes(), &nodePage) != nil || nodePage.Encoding != "gzip+base64+json" || nodePage.Next == "" {
		t.Fatalf("classification dictionary page: %d %s", nodesResponse.Code, nodesResponse.Body.String())
	}
	compressed, err := base64.StdEncoding.DecodeString(nodePage.Payload)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	rawNodes, err := io.ReadAll(reader)
	reader.Close()
	var nodes []offlineClassificationNode
	if err != nil || json.Unmarshal(rawNodes, &nodes) != nil || len(nodes) != 1 || nodes[0].ID != "child" {
		t.Fatalf("compressed classification nodes: %s %v", rawNodes, err)
	}
	secondResponse := httptest.NewRecorder()
	client.offlineLibraryIndex(secondResponse, httptest.NewRequest(http.MethodGet, "/api/library/offline-index?scope=all&limit=1&cursor="+first.Next, nil))
	var second struct {
		Items []offlineLibraryFile `json:"items"`
	}
	if secondResponse.Code != http.StatusOK || json.Unmarshal(secondResponse.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Items[0].Indexes[0].IndexID != "child" || second.Items[0].Indexes[0].Manual {
		t.Fatalf("second offline page: %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	if _, err := app.db.Exec(`INSERT INTO classification_memberships(library_id,file_id,index_id,has_manual,has_automatic,updated_at) VALUES(?, 'f2', 'root', 1, 0, ?)`, client.libraryID(), now()); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	client.offlineLibraryIndex(stale, httptest.NewRequest(http.MethodGet, "/api/library/offline-index?scope=all&limit=1&cursor="+first.Next, nil))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "cursorExpired") {
		t.Fatalf("classification change did not expire offline cursor: %d %s", stale.Code, stale.Body.String())
	}
}

func TestLibrarySavedQueryPersistsClassificationIDs(t *testing.T) {
	_, app := classificationOfflineClient(t)
	body := `{"name":"项目待整理","filters":{"scope":"all","query":"","folderId":"","tags":[],"sort":"name_asc","browseId":"root","indexFilters":[{"mode":"subtree","id":"root"},{"mode":"direct","id":"child"}],"needsOrganizing":true}}`
	response := httptest.NewRecorder()
	app.createLibrarySavedQuery(response, httptest.NewRequest(http.MethodPost, "/api/library/queries", strings.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create saved classification query: %d %s", response.Code, response.Body.String())
	}
	var saved librarySavedQuery
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Filters.BrowseID != "root" || saved.Filters.BrowseMode != "subtree" || saved.Filters.NeedsOrganizing == nil || !*saved.Filters.NeedsOrganizing || len(saved.Filters.IndexFilters) != 2 {
		t.Fatalf("saved classification filters: %+v", saved.Filters)
	}
	missing := httptest.NewRecorder()
	app.createLibrarySavedQuery(missing, httptest.NewRequest(http.MethodPost, "/api/library/queries", strings.NewReader(`{"name":"missing","filters":{"scope":"all","query":"","folderId":"","tags":[],"sort":"name_asc","browseId":"missing"}}`)))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing saved index status: %d %s", missing.Code, missing.Body.String())
	}
	if !allowedRemoteRequest(http.MethodGet, "/api/library/indexes") || !allowedRemoteRequest(http.MethodGet, "/api/library/indexes/root") || allowedRemoteRequest(http.MethodPost, "/api/library/indexes/root") {
		t.Fatal("classification tree remote read whitelist mismatch")
	}
}
