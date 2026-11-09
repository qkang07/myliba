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
 INSERT INTO resources(id,root_id,path,file_id,name,size,modified_at,kind,available,first_seen_at,last_seen_at,current_version_id)
 VALUES('file','root',?,'logical-file','report.txt',6,?,'document',1,?,?,'v1');
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
	if record, manifest, findErr := client.findRecord("file"); findErr != nil || record.Format != 4 || record.RevisionID != "v1" || manifest.FileID != "logical-file" {
		t.Fatalf("P2 upload identity = record:%+v manifest:%+v err:%v", record, manifest, findErr)
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
	resolveRequest := httptest.NewRequest("POST", "/api/library/files/logical-file/resolve", strings.NewReader(`{"revisionId":"v1","purpose":"original"}`))
	resolveRequest.SetPathValue("fileId", "logical-file")
	resolveResponse := httptest.NewRecorder()
	client.resolveLibraryFile(resolveResponse, resolveRequest)
	if resolveResponse.Code != 200 {
		t.Fatalf("resolve failed: %d %s", resolveResponse.Code, resolveResponse.Body.String())
	}
	var resolved struct {
		Candidates []struct {
			Kind       string `json:"kind"`
			RevisionID string `json:"revisionId"`
			PlainHash  string `json:"plainHash"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(resolveResponse.Body.Bytes(), &resolved); err != nil {
		t.Fatal(err)
	}
	if len(resolved.Candidates) == 0 || resolved.Candidates[0].RevisionID != "v1" {
		t.Fatalf("version-pinned candidates missing: %+v", resolved)
	}
	if len(resolved.Candidates[0].PlainHash) != 64 {
		t.Fatalf("verified hash missing from read candidate: %+v", resolved)
	}
	thumbnailRequest := httptest.NewRequest("POST", "/api/library/files/logical-file/resolve", strings.NewReader(`{"revisionId":"v1","purpose":"thumbnail"}`))
	thumbnailRequest.SetPathValue("fileId", "logical-file")
	thumbnailResponse := httptest.NewRecorder()
	client.resolveLibraryFile(thumbnailResponse, thumbnailRequest)
	var thumbnail struct {
		Candidates []struct {
			Kind     string `json:"kind"`
			ReadPath string `json:"readPath"`
		} `json:"candidates"`
	}
	if thumbnailResponse.Code != 200 || json.Unmarshal(thumbnailResponse.Body.Bytes(), &thumbnail) != nil {
		t.Fatalf("thumbnail resolve: %d %s", thumbnailResponse.Code, thumbnailResponse.Body.String())
	}
	for _, candidate := range thumbnail.Candidates {
		if candidate.Kind == "cloud" || !strings.HasSuffix(candidate.ReadPath, "/thumbnail") {
			t.Fatalf("original advertised as thumbnail: %+v", thumbnail.Candidates)
		}
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

func TestLibrarySearchDeviceAndTypeFilters(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, name, node string }{{"photo", "photo.jpg", "phone"}, {"notes", "notes.txt", "laptop"}} {
		index := fileIndexView{FileID: item.id, RevisionID: "v1", Name: item.name, Modified: "2026-11-02T00:00:00Z", Original: indexedContent{Size: 4, Local: []indexedLocation{{NodeID: item.node, ResourceRef: item.id}}}, ContentSize: 4, FolderID: "folder-" + item.node, FolderName: "同名文件夹", OrganizationKnown: true}
		record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: item.id, RevisionID: "v1", AuthorID: item.node, CreatedAt: "2026-11-02T00:00:00Z", BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err := runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search?scope=device:phone&type=image&cloudCopy=false&limit=1", nil))
	if response.Code != 200 {
		t.Fatalf("search status: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Total int             `json:"total"`
		Items []cloudFileView `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || len(result.Items) != 1 || result.Items[0].FileID != "photo" {
		t.Fatalf("filtered search: %+v", result)
	}
	response = httptest.NewRecorder()
	client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search?scope=all&type=document&grouped=true", nil))
	if response.Code != 200 {
		t.Fatalf("document search: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].FileID != "notes" {
		t.Fatalf("document filter: %+v", result)
	}
	response = httptest.NewRecorder()
	client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search?scope=all&folderId=folder-phone", nil))
	if response.Code != 200 {
		t.Fatalf("folder search: %d %s", response.Code, response.Body.String())
	}
	var byFolder struct {
		Total  int             `json:"total"`
		Items  []cloudFileView `json:"items"`
		Facets struct {
			Folders []struct {
				ID string `json:"id"`
			} `json:"folders"`
		} `json:"facets"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &byFolder); err != nil {
		t.Fatal(err)
	}
	if byFolder.Total != 1 || len(byFolder.Items) != 1 || byFolder.Items[0].FileID != "photo" || len(byFolder.Facets.Folders) != 2 {
		t.Fatalf("same-name folders merged: %+v", byFolder)
	}
}

func TestLibraryFileDetailMergesRevisionsAndLocations(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(node, revision, modified string) {
		t.Helper()
		index := fileIndexView{FileID: "logical-detail", RevisionID: revision, Name: "detail.txt", Modified: modified, Original: indexedContent{Size: 4}, ContentSize: 4, OrganizationKnown: true}
		index.Original.Local = []indexedLocation{{NodeID: node, Path: "/files/" + revision}}
		record := cloudRecord{Format: 3, LibraryID: client.libraryID(), ObjectID: "logical-detail", RevisionID: revision, AuthorID: node, CreatedAt: modified, BlockIDs: []string{}}
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
	publish("device-a", "v1", "2026-11-02T00:00:00Z")
	publish("device-b", "v2", "2026-11-02T01:00:00Z")

	request := httptest.NewRequest("GET", "/api/library/files/logical-detail", nil)
	request.SetPathValue("fileId", "logical-detail")
	response := httptest.NewRecorder()
	client.libraryFileDetail(response, request)
	if response.Code != 200 {
		t.Fatalf("detail status %d: %s", response.Code, response.Body.String())
	}
	var detail struct {
		FileID    string `json:"fileId"`
		Name      string `json:"name"`
		Revisions []struct {
			RevisionID string `json:"revisionId"`
			Modified   string `json:"modified"`
		} `json:"revisions"`
		Locations          []indexedLocation `json:"locations"`
		ContentStatus      string            `json:"contentStatus"`
		OrganizationStatus string            `json:"organizationStatus"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.FileID != "logical-detail" || detail.Name != "detail.txt" {
		t.Fatalf("detail identity = %+v", detail)
	}
	if len(detail.Revisions) != 2 || detail.Revisions[0].RevisionID != "v2" || detail.Revisions[1].RevisionID != "v1" {
		t.Fatalf("revisions must merge and sort newest first: %+v", detail.Revisions)
	}
	if len(detail.Locations) != 2 {
		t.Fatalf("locations must merge across devices: %+v", detail.Locations)
	}
	if detail.OrganizationStatus != "synced" {
		t.Fatalf("organization status = %s", detail.OrganizationStatus)
	}
	resolveWithoutRevision := httptest.NewRequest("POST", "/api/library/files/logical-detail/resolve", strings.NewReader(`{"purpose":"original"}`))
	resolveWithoutRevision.SetPathValue("fileId", "logical-detail")
	choiceRequired := httptest.NewRecorder()
	client.resolveLibraryFile(choiceRequired, resolveWithoutRevision)
	if choiceRequired.Code != http.StatusConflict || !strings.Contains(choiceRequired.Body.String(), "revisionChoiceRequired") {
		t.Fatalf("multiple versions resolved without a choice: %d %s", choiceRequired.Code, choiceRequired.Body.String())
	}
	searchResponse := httptest.NewRecorder()
	client.searchLibrary(searchResponse, httptest.NewRequest("GET", "/api/library/search?scope=all", nil))
	if searchResponse.Code != 200 {
		t.Fatalf("search status %d: %s", searchResponse.Code, searchResponse.Body.String())
	}
	var search struct {
		Total int             `json:"total"`
		Items []cloudFileView `json:"items"`
	}
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &search); err != nil {
		t.Fatal(err)
	}
	if search.Total != 1 || len(search.Items) != 1 || search.Items[0].RevisionID != "v2" || search.Items[0].RevisionCount != 2 {
		t.Fatalf("search did not group revisions: %+v", search)
	}
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "selected.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	rawSelection, _ := json.Marshal(map[string]any{"fileIds": []string{"logical-detail"}, "revisionId": "v1", "context": map[string]int64{}})
	applyRename(t, db, libraryOperationEnvelope{OpID: "select-v1", AuthorID: "chooser", Counter: 1, Kind: "file.version.select", Payload: rawSelection})
	selectedSearch := httptest.NewRecorder()
	client.searchLibrary(selectedSearch, httptest.NewRequest("GET", "/api/library/search?scope=all", nil))
	if selectedSearch.Code != 200 || json.Unmarshal(selectedSearch.Body.Bytes(), &search) != nil || len(search.Items) != 1 || search.Items[0].RevisionID != "v1" || !search.Items[0].CurrentRevision {
		t.Fatalf("selected revision missing from search: %d %s", selectedSearch.Code, selectedSearch.Body.String())
	}
	selectedResolve := httptest.NewRequest("POST", "/api/library/files/logical-detail/resolve", strings.NewReader(`{"purpose":"original"}`))
	selectedResolve.SetPathValue("fileId", "logical-detail")
	selectedResponse := httptest.NewRecorder()
	client.resolveLibraryFile(selectedResponse, selectedResolve)
	if selectedResponse.Code != 200 || !strings.Contains(selectedResponse.Body.String(), `"revisionId":"v1"`) {
		t.Fatalf("shared revision choice not resolved: %d %s", selectedResponse.Code, selectedResponse.Body.String())
	}

	missing := httptest.NewRequest("GET", "/api/library/files/unknown", nil)
	missing.SetPathValue("fileId", "unknown")
	missingResponse := httptest.NewRecorder()
	client.libraryFileDetail(missingResponse, missing)
	if missingResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown file status = %d", missingResponse.Code)
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
	// Distinct revisions must not collapse just because size and mtime match.
	publish("same-file", "device-e", "v3", modified, modified, true)
	items, err := client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
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

func TestAppliedClassificationOverridesOldManifestIncludingEmpty(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "classification.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES('logical',?,?);
		INSERT INTO library_classification_authoritative(file_id) VALUES('logical')`, now(), now()); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	files, err := client.mergeCatalogSearch([]cloudFileView{{FileID: "logical", ID: "legacy", Name: "file.txt", RevisionID: "v1", Tags: []string{"stale"}, FolderID: "old", FolderName: "Old"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].FolderID != "" || len(files[0].Tags) != 0 {
		t.Fatalf("old manifest restored cleared classification: %+v", files)
	}
}
