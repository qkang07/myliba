package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// searchResponse mirrors the unified-search fields this test asserts on.
type searchResponse struct {
	Total      int             `json:"total"`
	NextCursor string          `json:"nextCursor"`
	Generation string          `json:"generation"`
	Items      []cloudFileView `json:"items"`
	Facets     struct {
		Folders []libraryFolderFacet `json:"folders"`
		Tags    []libraryTagFacet    `json:"tags"`
	} `json:"facets"`
	Coverage struct {
		LastCatalogAt       string            `json:"lastCatalogAt"`
		Sources             []string          `json:"sources"`
		SourceLastCatalogAt map[string]string `json:"sourceLastCatalogAt"`
	} `json:"coverage"`
}

func TestLibrarySearchSQLFacetsCoverageAndPagination(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(id, name, node, folderID, folderName, created string, tags []string) {
		t.Helper()
		index := fileIndexView{FileID: id, RevisionID: "v1", Name: name, Modified: created, Tags: tags, FolderID: folderID, FolderName: folderName, Original: indexedContent{Size: 4, Local: []indexedLocation{{NodeID: node, ResourceRef: id}}}, ContentSize: 4, OrganizationKnown: true}
		record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: id, RevisionID: "v1", AuthorID: node, CreatedAt: created, BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err := runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	publish("f1", "alpha.txt", "phone", "folder-a", "Folder A", "2026-11-01T00:00:00Z", []string{"x", "y"})
	publish("f2", "beta.txt", "phone", "folder-a", "Folder A", "2026-11-02T00:00:00Z", []string{"x"})
	publish("f3", "gamma.txt", "laptop", "folder-b", "Folder B", "2026-11-03T00:00:00Z", []string{"y"})
	publish("f4", "delta.txt", "phone", "", "", "2026-11-04T00:00:00Z", nil)
	search := func(query string) searchResponse {
		t.Helper()
		response := httptest.NewRecorder()
		client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search?"+query, nil))
		if response.Code != 200 {
			t.Fatalf("search %s: %d %s", query, response.Code, response.Body.String())
		}
		var result searchResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	folderCount := func(result searchResponse, id string) (int, bool) {
		for _, facet := range result.Facets.Folders {
			if facet.ID == id {
				return facet.Count, true
			}
		}
		return 0, false
	}
	tagCount := func(result searchResponse, name string) (int, bool) {
		for _, facet := range result.Facets.Tags {
			if facet.Name == name {
				return facet.Count, true
			}
		}
		return 0, false
	}
	all := search("scope=all")
	if all.Total != 4 || all.Coverage.LastCatalogAt != "2026-11-04T00:00:00Z" {
		t.Fatalf("SQL coverage total/lastCatalogAt: %+v", all)
	}
	if len(all.Coverage.Sources) != 2 || all.Coverage.SourceLastCatalogAt["phone"] != "2026-11-04T00:00:00Z" || all.Coverage.SourceLastCatalogAt["laptop"] != "2026-11-03T00:00:00Z" {
		t.Fatalf("SQL source coverage: %+v", all.Coverage)
	}
	if count, ok := folderCount(all, "folder-a"); !ok || count != 2 {
		t.Fatalf("folder-a facet: %+v", all.Facets.Folders)
	}
	if count, ok := folderCount(all, "unfiled"); !ok || count != 1 {
		t.Fatalf("unfiled facet: %+v", all.Facets.Folders)
	}
	if count, ok := tagCount(all, "x"); !ok || count != 2 {
		t.Fatalf("tag x facet: %+v", all.Facets.Tags)
	}
	// Folder facets ignore the folder selection; tag facets honor it.
	byFolder := search("scope=all&folderId=folder-a")
	if byFolder.Total != 2 {
		t.Fatalf("folder filter total: %+v", byFolder)
	}
	if count, ok := folderCount(byFolder, "folder-b"); !ok || count != 1 {
		t.Fatalf("folder facets must cross-filter (folder-b still counted): %+v", byFolder.Facets.Folders)
	}
	if count, ok := tagCount(byFolder, "y"); !ok || count != 1 {
		t.Fatalf("tag facets must honor folder selection (y=1 within folder-a): %+v", byFolder.Facets.Tags)
	}
	// Tag facets ignore the tag selection; folder facets honor it.
	byTag := search("scope=all&tag=x")
	if byTag.Total != 2 {
		t.Fatalf("tag filter total: %+v", byTag)
	}
	if count, ok := tagCount(byTag, "y"); !ok || count != 2 {
		t.Fatalf("tag facets must cross-filter (y still counted): %+v", byTag.Facets.Tags)
	}
	if count, ok := folderCount(byTag, "folder-a"); !ok || count != 2 {
		t.Fatalf("folder facets must honor tag selection: %+v", byTag.Facets.Folders)
	}
	if _, ok := folderCount(byTag, "folder-b"); ok {
		t.Fatalf("folder-b has no x-tagged file: %+v", byTag.Facets.Folders)
	}
	// Selected zero-count conditions are retained.
	emptyFolder := search("scope=all&folderId=missing")
	if count, ok := folderCount(emptyFolder, "missing"); !ok || count != 0 || emptyFolder.Total != 0 {
		t.Fatalf("zero-count folder facet: %+v", emptyFolder.Facets.Folders)
	}
	emptyTag := search("scope=all&tag=z")
	if count, ok := tagCount(emptyTag, "z"); !ok || count != 0 || emptyTag.Total != 0 {
		t.Fatalf("zero-count tag facet: %+v", emptyTag.Facets.Tags)
	}
	// Pagination walks every row exactly once in stable name order.
	seen := map[string]bool{}
	order := []string{}
	cursor := ""
	for page := 0; page < 4; page++ {
		query := "scope=all&limit=1&sort=name_asc"
		if cursor != "" {
			query += "&cursor=" + cursor
		}
		result := search(query)
		if result.Total != 4 || len(result.Items) != 1 {
			t.Fatalf("page %d: %+v", page, result)
		}
		id := result.Items[0].FileID
		if seen[id] {
			t.Fatalf("duplicate row %s across pages", id)
		}
		seen[id] = true
		order = append(order, id)
		cursor = result.NextCursor
	}
	if cursor != "" || len(seen) != 4 || order[0] != "f1" || order[1] != "f2" || order[2] != "f4" || order[3] != "f3" {
		t.Fatalf("pagination lost/duplicated rows or wrong order: %v cursor=%q", order, cursor)
	}
	// A catalog change rewrites the generation and invalidates the old cursor.
	stale := search("scope=all&limit=1")
	publish("f5", "epsilon.txt", "phone", "", "", "2026-11-05T00:00:00Z", nil)
	response := httptest.NewRecorder()
	client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search?scope=all&limit=1&cursor="+stale.NextCursor, nil))
	if response.Code != 409 {
		t.Fatalf("stale cursor after catalog change: %d %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte("cursorExpired")) {
		t.Fatalf("stale cursor code missing: %s", response.Body.String())
	}
}

func TestLibraryProjectionRebuildsForCatalogAndLocalChanges(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if files, err := client.libraryFiles(true); err != nil || len(files) != 0 || !client.searchReady {
		t.Fatalf("initial projection: %+v %v", files, err)
	}
	initialStamp := client.searchStamp
	index := fileIndexView{FileID: "logical", RevisionID: "v1", Name: "新文件.txt", Modified: now(),
		Original: indexedContent{Size: 3, Local: []indexedLocation{{NodeID: "phone", ResourceRef: "source"}}}, ContentSize: 3, OrganizationKnown: true}
	record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "logical", RevisionID: "v1", AuthorID: "phone", CreatedAt: now(), BlockIDs: []string{}}
	record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	files, err := client.libraryFiles(true)
	if err != nil || len(files) != 1 || files[0].Name != index.Name || files[0].CatalogCreatedAt != record.CreatedAt || client.searchStamp == initialStamp {
		t.Fatalf("catalog change did not rebuild projection: %+v %v", files, err)
	}
	files, err = client.libraryFiles(true)
	if err != nil || len(files) != 1 || files[0].CatalogCreatedAt != record.CreatedAt || files[0].CatalogFormat != 4 {
		t.Fatalf("cache hit lost internal sort metadata: %+v %v", files, err)
	}
	if _, err := db.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES('other','2026-01-01','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	before := client.searchChanges
	files, err = client.libraryFiles(true)
	if err != nil || len(files) != 1 || client.searchChanges == before {
		t.Fatalf("local change did not rebuild projection: %+v %v", files, err)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_search_projection WHERE scope=1`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("materialized rows = %d, %v", rows, err)
	}
	if _, err := db.Exec(`INSERT INTO library_receipts(node_id,frontiers_json,catalog_at) VALUES('empty-phone','{"writer":2}','2026-11-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	client.searchLibrary(response, httptest.NewRequest("GET", "/api/library/search", nil))
	if response.Code != 200 {
		t.Fatalf("coverage status %d: %s", response.Code, response.Body.String())
	}
	var coverage struct {
		Coverage struct {
			KnownNodes       []string `json:"knownNodes"`
			UnindexedSources []string `json:"unindexedSources"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage.Coverage.UnindexedSources) != 1 || coverage.Coverage.UnindexedSources[0] != "empty-phone" {
		t.Fatalf("unindexed node missing: %+v", coverage.Coverage)
	}
	for _, path := range []string{"/api/library/search?query=%E6%96%B0%E6%96%87%E4%BB%B6", "/api/library/search?scope=device:phone&query=%E6%96%B0%E6%96%87%E4%BB%B6"} {
		filtered := httptest.NewRecorder()
		client.searchLibrary(filtered, httptest.NewRequest("GET", path, nil))
		var page struct {
			Total int             `json:"total"`
			Items []cloudFileView `json:"items"`
		}
		if filtered.Code != 200 || json.Unmarshal(filtered.Body.Bytes(), &page) != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].FileID != "logical" {
			t.Fatalf("FTS/current-file query %s: %d %s", path, filtered.Code, filtered.Body.String())
		}
	}
	second := fileIndexView{FileID: "logical-two", RevisionID: "v2", Name: "新文件-二.txt", Modified: now(), Tags: []string{"旅行"}, FolderID: "trip-folder", FolderName: "旅行", Original: indexedContent{Size: 4, Local: []indexedLocation{{NodeID: "phone", ResourceRef: "second"}}}, ContentSize: 4, OrganizationKnown: true}
	secondRecord := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "logical-two", RevisionID: "v2", AuthorID: "phone", CreatedAt: now(), BlockIDs: []string{}}
	secondRecord, err = client.sealManifest(secondRecord, cloudManifest{IndexOnly: true, LocalIndex: &second})
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, _ := json.Marshal(secondRecord)
	if err = runtime.disk.put("catalog", cloudHash(secondRaw), bytes.NewReader(secondRaw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	firstPage := httptest.NewRecorder()
	client.searchLibrary(firstPage, httptest.NewRequest("GET", "/api/library/search?query=%E6%96%B0%E6%96%87%E4%BB%B6&limit=1", nil))
	var paged struct {
		Total      int             `json:"total"`
		NextCursor string          `json:"nextCursor"`
		Items      []cloudFileView `json:"items"`
	}
	if firstPage.Code != 200 || json.Unmarshal(firstPage.Body.Bytes(), &paged) != nil || paged.Total != 2 || len(paged.Items) != 1 || paged.NextCursor == "" {
		t.Fatalf("SQLite first page: %d %s", firstPage.Code, firstPage.Body.String())
	}
	firstID := paged.Items[0].FileID
	secondPage := httptest.NewRecorder()
	client.searchLibrary(secondPage, httptest.NewRequest("GET", "/api/library/search?query=%E6%96%B0%E6%96%87%E4%BB%B6&limit=1&cursor="+paged.NextCursor, nil))
	if secondPage.Code != 200 || json.Unmarshal(secondPage.Body.Bytes(), &paged) != nil || paged.Total != 2 || len(paged.Items) != 1 || paged.Items[0].FileID == firstID || paged.NextCursor != "" {
		t.Fatalf("SQLite second page: %d %s", secondPage.Code, secondPage.Body.String())
	}
	tagged := httptest.NewRecorder()
	client.searchLibrary(tagged, httptest.NewRequest("GET", "/api/library/search?query=%E6%96%B0%E6%96%87%E4%BB%B6&tag=%E6%97%85%E8%A1%8C&folderId=trip-folder", nil))
	if tagged.Code != 200 || json.Unmarshal(tagged.Body.Bytes(), &paged) != nil || paged.Total != 1 || len(paged.Items) != 1 || paged.Items[0].FileID != "logical-two" {
		t.Fatalf("SQLite tag/folder page: %d %s", tagged.Code, tagged.Body.String())
	}
}
