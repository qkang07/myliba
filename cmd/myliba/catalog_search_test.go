package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogOrganizationSyncAndDedup(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "report.txt")
	if err = os.WriteFile(source, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(source)
	_, err = db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?);
 INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at,current_version_id)
 VALUES('file','root',?,'report.txt',6,?,'document',1,?,?,'v1');
 INSERT INTO folders(id,name,created_at,password_hash) VALUES('folder','Before',?,'')`, dir, now(), source, info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), now(), now(), now())
	if err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, filepath.Join(dir, "cloud"), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, cloud: client}
	client.indexer = app
	if err = client.uploadFile(context.Background(), "file", source, false); err != nil {
		t.Fatal(err)
	}
	if err = client.publishLocalIndex("file"); err != nil {
		t.Fatal(err)
	}
	remoteRuntime := testCloudRuntime(t, filepath.Join(dir, "remote"), "")
	key, err := os.ReadFile(filepath.Join(runtime.disk.dir, "recovery.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(remoteRuntime.disk.dir, "recovery.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	remote, err := newCloudClient(remoteRuntime)
	if err != nil {
		t.Fatal(err)
	}
	token := "catalog-sync-test-token-0123456789"
	peer := serveCloudTestNode(t, runtime, token)
	syncList := func() []cloudFileView {
		t.Helper()
		if err := client.publishLocalIndex("file"); err != nil {
			t.Fatal(err)
		}
		if err := remoteRuntime.syncCatalog(context.Background(), peer.URL, token); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		remote.list(response, httptest.NewRequest("GET", "/api/cloud/files?scope=all", nil))
		if response.Code != 200 {
			t.Fatal(response.Body.String())
		}
		var view struct {
			Files []cloudFileView `json:"files"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view.Files
	}
	mutate := func(handler http.HandlerFunc, method, path, id, body string) {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.SetPathValue("id", id)
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != 200 {
			t.Fatalf("mutation %s: %d %s", path, response.Code, response.Body.String())
		}
		client.queueMu.Lock()
		_, queued := client.assetPending["file"]
		client.assetPending = map[string]bool{}
		client.queueMu.Unlock()
		if !queued {
			t.Fatal("classification mutation did not queue index sync")
		}
	}
	items := syncList()
	if len(items) != 1 || items[0].LocalOnly || len(items[0].Locations) != 1 {
		t.Fatalf("duplicate source/cloud: %+v", items)
	}
	mutate(app.addTag, "POST", "/api/resources/file/tags", "file", `{"name":"Updated"}`)
	mutate(app.moveResourcesToFolder, "PUT", "/api/resources/folder", "", `{"ids":["file"],"folderId":"folder"}`)
	mutate(app.renameFolder, "PUT", "/api/folders/folder", "folder", `{"name":"After"}`)
	items = syncList()
	if len(items) != 1 || items[0].FolderName != "After" || len(items[0].Tags) != 1 || items[0].Tags[0] != "Updated" {
		t.Fatalf("classification not synced: %+v", items)
	}
	var tagID string
	if err = db.QueryRow(`SELECT id FROM tags WHERE name='Updated'`).Scan(&tagID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("DELETE", "/api/resources/file/tags/"+tagID, nil)
	request.SetPathValue("id", "file")
	request.SetPathValue("tagID", tagID)
	response := httptest.NewRecorder()
	app.removeTag(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	mutate(app.deleteFolder, "DELETE", "/api/folders/folder", "folder", "")
	items = syncList()
	if len(items) != 1 || items[0].FolderID != "" || len(items[0].Tags) != 0 {
		t.Fatalf("removed classification resurrected: %+v", items)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE resources SET available=0 WHERE id='file'`); err != nil {
		t.Fatal(err)
	}
	items = syncList()
	if len(items) != 1 || items[0].LocalOnly || len(items[0].Locations) != 0 {
		t.Fatalf("offline source should retain cloud result: %+v", items)
	}
	// A password change publishes an exclusion snapshot; it must also hide
	// an older, previously public cloud record on a receiving device.
	if _, err = db.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES('private','Private',?,''); INSERT INTO resource_folders(resource_id,folder_id) VALUES('file','private')`, now()); err != nil {
		t.Fatal(err)
	}
	mutate(app.setFolderPassword, "PUT", "/api/folders/private/password", "private", `{"password":"test-password"}`)
	items = syncList()
	if len(items) != 0 {
		t.Fatalf("protected file exposed: %+v", items)
	}
}

func TestCatalogTimestampOrdering(t *testing.T) {
	if !catalogAfter(cloudRecord{CreatedAt: "2026-11-02T00:00:00.1Z"}, cloudRecord{CreatedAt: "2026-11-02T00:00:00Z"}) {
		t.Fatal("fractional time must sort after whole second")
	}
}

func TestCatalogMergeDevicesNamesAndVersions(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id, node, revision, modified, stamp string, available bool) {
		t.Helper()
		index := fileIndexView{FileID: id, RevisionID: revision, Name: "same-name.txt", Modified: modified, Original: indexedContent{Size: 4}, ContentSize: 4, OrganizationKnown: true}
		if available {
			index.Original.Local = []indexedLocation{{NodeID: node, Path: "/files/" + id}}
		}
		record := cloudRecord{Format: 3, LibraryID: client.libraryID(), ObjectID: id, RevisionID: revision, AuthorID: node, CreatedAt: stamp, BlockIDs: []string{}}
		record, err := client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err = runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	modified := "2026-11-02T00:00:00Z"
	publish("same-file", "device-a", "v1", modified, modified, true)
	publish("same-file", "device-b", "v1", modified, modified, true)
	publish("other-file", "device-c", "v1", modified, modified, true)
	publish("same-file", "device-d", "v2", "2026-11-02T01:00:00Z", modified, true)
	items, err := client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("must merge copies but retain homonyms and versions: %+v", items)
	}
	for _, item := range items {
		if item.ID == "same-file@v1" && len(item.Locations) != 2 {
			t.Fatalf("missing copy locations: %+v", item)
		}
	}
	publish("same-file", "device-a", "v1", modified, "2026-11-02T00:00:00.1Z", false)
	items, err = client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "same-file@v1" && (len(item.Locations) != 1 || item.Locations[0].NodeID != "device-b") {
			t.Fatalf("stale location resurrected: %+v", item)
		}
	}
	cloudOnly, err := client.mergeCatalogSearch(nil, false)
	if err != nil || len(cloudOnly) != 0 {
		t.Fatalf("local indexes leaked into legacy cloud list: %+v %v", cloudOnly, err)
	}
	newUpload, err := client.mergeCatalogSearch([]cloudFileView{{ID: "same-file", Name: "same-name.txt", OriginalSize: 4, Modified: modified, CatalogCreatedAt: "2026-11-02T02:00:00Z", Tags: []string{"new-upload"}, FolderName: "New folder"}}, false)
	if err != nil || len(newUpload) != 1 || len(newUpload[0].Tags) != 1 || newUpload[0].Tags[0] != "new-upload" || newUpload[0].FolderName != "New folder" {
		t.Fatalf("stale device snapshot replaced newer upload: %+v %v", newUpload, err)
	}
	protected, err := client.mergeCatalogSearch([]cloudFileView{{ID: "same-file", Protected: true, CatalogCreatedAt: "2026-11-02T02:00:00Z"}}, false)
	if err != nil || len(protected) != 0 {
		t.Fatalf("old public index exposed protected upload: %+v %v", protected, err)
	}
}
