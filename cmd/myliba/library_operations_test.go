package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func renameEnvelope(opID, author string, counter int64, previous, name string, context map[string]int64) libraryOperationEnvelope {
	payload := map[string]any{"folderName": name, "previousName": previous, "context": context}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return libraryOperationEnvelope{OpID: opID, AuthorID: author, Counter: counter, Kind: "folder.rename", Payload: raw}
}

func TestLegacyFolderIdentityKeepsSourcesSeparate(t *testing.T) {
	first := legacyFolderIdentity("phone-a", "", "Travel")
	if first == "" || first != legacyFolderIdentity("phone-a", "", " travel ") {
		t.Fatal("name-only legacy folder identity is not stable")
	}
	if first == legacyFolderIdentity("phone-b", "", "Travel") || first == legacyFolderIdentity("phone-a", "source-folder", "Travel") {
		t.Fatal("legacy folder identities merged different sources")
	}
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	if err := migrate(db); err != nil { t.Fatal(err) }
	snapshots := []legacySnapshot{
		{hash: cloudHash([]byte("a")), author: "phone-a", objectID: "file-a", fileID: "file-a", revisionID: "v1", folderID: first, folderName: "Travel", tagsJSON: "[]", catalogAt: "2026-01-01T00:00:00Z", known: true},
		{hash: cloudHash([]byte("b")), author: "phone-b", objectID: "file-b", fileID: "file-b", revisionID: "v1", folderID: legacyFolderIdentity("phone-b", "", "Travel"), folderName: "Travel", tagsJSON: "[]", catalogAt: "2026-01-01T00:00:00Z", known: true},
	}
	for i := 0; i < 2; i++ { if err := importLegacySnapshots(db, snapshots); err != nil { t.Fatal(err) } }
	var imported, identities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_legacy_snapshots`).Scan(&imported); err != nil { t.Fatal(err) }
	if err := db.QueryRow(`SELECT COUNT(DISTINCT folder_id) FROM library_legacy_folder_identities`).Scan(&identities); err != nil { t.Fatal(err) }
	if imported != 2 || identities != 2 { t.Fatalf("repeat import merged or duplicated folders: snapshots=%d identities=%d", imported, identities) }
	app := &server{db: db}
	listing := httptest.NewRecorder()
	app.listLegacyFolders(listing, httptest.NewRequest("GET", "/api/library/legacy-folders", nil))
	if listing.Code != 200 || !strings.Contains(listing.Body.String(), `"count":1`) { t.Fatalf("legacy folder list: %d %s", listing.Code, listing.Body.String()) }
	request := httptest.NewRequest("POST", "/api/library/legacy-folders/merge", strings.NewReader(`{"folderIds":["`+snapshots[0].folderID+`","`+snapshots[1].folderID+`"],"name":"Travel"}`))
	merged := httptest.NewRecorder()
	app.mergeLegacyFolders(merged, request)
	if merged.Code != 200 { t.Fatalf("legacy merge: %d %s", merged.Code, merged.Body.String()) }
	var moved, operations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_file_folder_state WHERE file_id IN ('file-a','file-b')`).Scan(&moved); err != nil { t.Fatal(err) }
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE kind='folder.move'`).Scan(&operations); err != nil { t.Fatal(err) }
	if moved != 2 || operations != 1 { t.Fatalf("merge lacked durable operation: moved=%d operations=%d", moved, operations) }
	listing = httptest.NewRecorder();app.listLegacyFolders(listing, httptest.NewRequest("GET", "/api/library/legacy-folders", nil))
	if strings.Contains(listing.Body.String(), snapshots[0].folderID) || strings.Contains(listing.Body.String(), snapshots[1].folderID) { t.Fatal("merged identities still require confirmation") }
}

func applyRename(t *testing.T, db *sql.DB, op libraryOperationEnvelope) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeLibraryOperation(tx, op); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentFolderRenameKeepsConflictCandidate(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES('travel','Travel',?,'')`, now()); err != nil {
		t.Fatal(err)
	}

	// Author A renames Travel -> Trip and observes nobody.
	applyRename(t, db, renameEnvelope("op-a", "author-a", 1, "Travel", "Trip", map[string]int64{}))
	var name string
	if err := db.QueryRow(`SELECT name FROM folders WHERE id='travel'`).Scan(&name); err != nil || name != "Trip" {
		t.Fatalf("first rename not applied: %q, %v", name, err)
	}

	// Author B concurrently renames Travel -> Journey without observing A.
	// The anchor "Travel" is already claimed by A, so B must be retained as a
	// conflict candidate rather than silently no-op'ing (§5.2).
	applyRename(t, db, renameEnvelope("op-b", "author-b", 1, "Travel", "Journey", map[string]int64{}))
	if err := db.QueryRow(`SELECT name FROM folders WHERE id='travel'`).Scan(&name); err != nil || name != "Trip" {
		t.Fatalf("concurrent rename overwrote winner: %q, %v", name, err)
	}
	var conflictName string
	if err := db.QueryRow(`SELECT folder_name FROM library_folder_rename_conflicts WHERE anchor='Travel' AND author_id='author-b'`).Scan(&conflictName); err != nil || conflictName != "Journey" {
		t.Fatalf("concurrent rename candidate not retained: %q, %v", conflictName, err)
	}
	response := httptest.NewRecorder()
	(&server{db: db}).libraryFolderRenameConflicts(response, httptest.NewRequest("GET", "/api/library/conflicts/renames", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"appliedName":"Trip"`) {
		t.Fatalf("rename conflict endpoint: %d %s", response.Code, response.Body.String())
	}

	// A later rename by B that observes A applies cleanly and is not a conflict.
	applyRename(t, db, renameEnvelope("op-b2", "author-b", 2, "Trip", "Voyage", map[string]int64{"author-a": 1}))
	if err := db.QueryRow(`SELECT name FROM folders WHERE id='travel'`).Scan(&name); err != nil || name != "Voyage" {
		t.Fatalf("observed rename not applied: %q, %v", name, err)
	}
	var extra int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_folder_rename_conflicts WHERE author_id='author-b' AND counter=2`).Scan(&extra); err != nil || extra != 0 {
		t.Fatalf("observed rename wrongly marked conflict: %d, %v", extra, err)
	}
}

func TestResolveFolderRenameConflictConverges(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES('travel','Travel',?,'')`, now()); err != nil {
		t.Fatal(err)
	}
	applyRename(t, db, renameEnvelope("op-a", "author-a", 1, "Travel", "Trip", map[string]int64{}))
	applyRename(t, db, renameEnvelope("op-b", "author-b", 1, "Travel", "Journey", map[string]int64{}))

	app := &server{db: db}
	request := httptest.NewRequest("POST", "/api/library/conflicts/renames/Travel/resolve", strings.NewReader(`{"folderName":"Journey"}`))
	request.SetPathValue("anchor", "Travel")
	response := httptest.NewRecorder()
	app.libraryResolveFolderRenameConflict(response, request)
	if response.Code != 200 {
		t.Fatalf("resolve status %d: %s", response.Code, response.Body.String())
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM folders WHERE id='travel'`).Scan(&name); err != nil || name != "Journey" {
		t.Fatalf("resolved name = %q, %v", name, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_folder_rename_conflicts WHERE anchor='Travel'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("conflict not cleared: %d, %v", remaining, err)
	}
	// Resolution emits a new rename operation from the applied name so peers converge.
	var kind, payload string
	if err := db.QueryRow(`SELECT kind,payload_json FROM library_operations ORDER BY counter DESC LIMIT 1`).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != "folder.rename" {
		t.Fatalf("resolution did not emit rename op: %q", kind)
	}
	var decoded struct {
		FolderName   string `json:"folderName"`
		PreviousName string `json:"previousName"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.FolderName != "Journey" || decoded.PreviousName != "Trip" {
		t.Fatalf("resolution op payload = %+v", decoded)
	}
}

func TestLibraryReceiptPublishesAndRemainsOutOfFileList(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "receipt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library_operation_frontiers(author_id,last_counter,updated_at) VALUES('author-a',2,?)`, now()); err != nil {
		t.Fatal(err)
	}
	if err := client.publishLibraryReceipt(); err != nil {
		t.Fatal(err)
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT frontiers_json FROM library_receipts WHERE node_id=?`, runtime.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var frontiers map[string]int64
	if err := json.Unmarshal([]byte(raw), &frontiers); err != nil || frontiers["author-a"] != 2 {
		t.Fatalf("receipt: %s, %v", raw, err)
	}
	files, err := client.contentRecords()
	if err != nil || len(files) != 0 {
		t.Fatalf("receipt appeared as content: %+v, %v", files, err)
	}
	response := httptest.NewRecorder()
	(&server{db: db}).librarySyncStatus(response, httptest.NewRequest("GET", "/api/library/sync/status", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"receipts"`) {
		t.Fatalf("receipt status: %d %s", response.Code, response.Body.String())
	}
}

func TestDeletedFolderDoesNotResurrectFromConcurrentMove(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	apply := func(id, author string, counter int64, kind string, payload string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := materializeLibraryOperation(tx, libraryOperationEnvelope{OpID: id, AuthorID: author, Counter: counter, Kind: kind, Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	apply("delete", "author-a", 1, "folder.delete", `{"folderName":"Travel"}`)
	apply("move", "author-b", 1, "folder.move", `{"folderName":"Travel","folderId":"foreign-folder","fileIds":["file-1"]}`)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Travel'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted folder resurrected: %d, %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_folder_conflicts WHERE file_id='file-1' AND op_id='move'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("move conflict lost: %d, %v", count, err)
	}
	apply("recreate", "author-b", 2, "folder.create", `{"folderName":"Travel","context":{"author-a":1}}`)
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Travel'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("observed recreation failed: %d, %v", count, err)
	}
}

func TestLibraryOperationCounterCollisionRejected(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO library_operation_frontiers(author_id,last_counter,updated_at) VALUES('author-a',1,?); INSERT INTO library_operation_inbox(op_id,author_id,counter,received_at,applied_at) VALUES('original','author-a',1,?,?)`, now(), now(), now()); err != nil {
		t.Fatal(err)
	}
	op := libraryOperationEnvelope{OpID: "different", AuthorID: "author-a", Counter: 1, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"Unexpected"}`)}
	record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-operation/author-a/1", RevisionID: op.OpID, AuthorID: "node-a", CreatedAt: now(), BlockIDs: []string{}}
	record, err = client.sealManifest(record, cloudManifest{Operation: &op})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	other := libraryOperationEnvelope{OpID: "other", AuthorID: "author-b", Counter: 1, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"Other"}`)}
	otherRecord := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-operation/author-b/1", RevisionID: other.OpID, AuthorID: "node-b", CreatedAt: now(), BlockIDs: []string{}}
	otherRecord, err = client.sealManifest(otherRecord, cloudManifest{Operation: &other})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(otherRecord)
	if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_operation_quarantine WHERE op_id='different'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("collision not quarantined: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Unexpected'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("colliding operation applied: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Other'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("other author blocked: %d %v", count, err)
	}
}

func TestUnknownLibraryOperationIsQuarantinedWithoutBlockingOtherAuthor(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []libraryOperationEnvelope{
		{OpID: "future", AuthorID: "author-a", Counter: 1, Kind: "future.operation", Payload: json.RawMessage(`{}`)},
		{OpID: "known", AuthorID: "author-b", Counter: 1, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"Known"}`)},
	} {
		record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-operation/" + op.AuthorID + "/1", RevisionID: op.OpID, AuthorID: "node-a", CreatedAt: now(), BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{Operation: &op})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_operation_quarantine WHERE op_id='future'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unknown operation not quarantined: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Known'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("other author blocked: %d %v", count, err)
	}
}

func TestLibraryReceiptsNeverRegressFrontiers(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		at, revision string
		frontiers    map[string]int64
	}{
		{"2026-11-01T00:00:00Z", "r1", map[string]int64{"author-a": 3}},
		{"2026-11-02T00:00:00Z", "r2", map[string]int64{"author-a": 1, "author-b": 2}},
	} {
		receipt := libraryReceiptEnvelope{NodeID: "node-a", Frontiers: item.frontiers}
		record := cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-receipt-node-a", RevisionID: item.revision, AuthorID: "node-a", CreatedAt: item.at, BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{Receipt: &receipt})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow(`SELECT frontiers_json FROM library_receipts WHERE node_id='node-a'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var applied map[string]int64
	if err := json.Unmarshal([]byte(raw), &applied); err != nil {
		t.Fatal(err)
	}
	if applied["author-a"] != 3 || applied["author-b"] != 2 {
		t.Fatalf("receipt frontier regressed: %+v", applied)
	}
}

func TestLegacySnapshotImportFrontierIsIdempotent(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, at string
		known  bool
	}{{"file-a", "2026-11-01T00:00:00Z", false}, {"file-b", "2026-11-02T00:00:00Z", false}} {
		index := fileIndexView{FileID: item.id, RevisionID: "v1", Name: item.id + ".txt", Modified: item.at, Original: indexedContent{Size: 0}, OrganizationKnown: item.known}
		if item.id == "file-a" {
			index.Tags = []string{"legacy"}
			index.FolderName = "Travel"
			index.FolderID = "old-folder"
		}
		record := cloudRecord{Format: 3, LibraryID: client.libraryID(), ObjectID: item.id, RevisionID: "v1", AuthorID: "old-node", CreatedAt: item.at, BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(record)
		if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := client.applyRemoteOperations(); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var latest string
	if err := db.QueryRow(`SELECT imported_count,last_catalog_at FROM library_legacy_import_frontiers WHERE author_id='old-node'`).Scan(&count, &latest); err != nil || count != 2 || latest != "2026-11-02T00:00:00Z" {
		t.Fatalf("legacy frontier: %d %q %v", count, latest, err)
	}
	var known int
	if err := db.QueryRow(`SELECT classification_known FROM library_legacy_snapshots WHERE object_id='file-b'`).Scan(&known); err != nil || known != 0 {
		t.Fatalf("missing classification treated as empty: %d %v", known, err)
	}
	if err := db.QueryRow(`SELECT classification_known FROM library_legacy_snapshots WHERE object_id='file-a' AND revision_id='v1'`).Scan(&known); err != nil || known != 1 {
		t.Fatalf("legacy classification without known flag ignored: %d %v", known, err)
	}
	newer := fileIndexView{FileID: "file-a", RevisionID: "v2", Name: "file-a.txt", Modified: "2026-11-03T00:00:00Z", Original: indexedContent{Size: 0}, OrganizationKnown: false}
	record := cloudRecord{Format: 3, LibraryID: client.libraryID(), ObjectID: "file-a", RevisionID: "v2", AuthorID: "old-node", CreatedAt: "2026-11-03T00:00:00Z", BlockIDs: []string{}}
	record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &newer})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(record)
	if err := runtime.disk.put("catalog", cloudHash(raw), strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	items, err := client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range items {
		if file.FileID == "file-a" && file.RevisionID == "v2" {
			found = true
			if len(file.Tags) != 1 || file.Tags[0] != "legacy" || file.FolderName != "Travel" {
				t.Fatalf("known legacy baseline lost after unknown snapshot: %+v", file)
			}
		}
	}
	if !found {
		t.Fatalf("newer device index missing: %+v", items)
	}
	apply := func(id, kind, payload string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := materializeLibraryOperation(tx, libraryOperationEnvelope{OpID: id, AuthorID: "new-node", Counter: 1, Kind: kind, Payload: json.RawMessage(payload)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	apply("add-new", "tag.add", `{"fileIds":["file-a"],"tag":"fresh"}`)
	items, err = client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range items {
		if file.FileID == "file-a" && file.RevisionID == "v2" && (len(file.Tags) != 2 || file.FolderName != "Travel") {
			t.Fatalf("first new operation discarded legacy classification: %+v", file)
		}
	}
	apply("remove-old", "tag.remove", `{"fileIds":["file-a"],"tag":"legacy"}`)
	items, err = client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range items {
		if file.FileID == "file-a" && file.RevisionID == "v2" && (len(file.Tags) != 1 || file.Tags[0] != "fresh" || file.FolderName != "Travel") {
			t.Fatalf("legacy tag removal lost new classification: %+v", file)
		}
	}
}

func TestFileLifecycleTrashWinsConcurrentRestoreAndLateReplay(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	apply := func(id, author string, counter int64, kind string, context map[string]int64) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := materializeFileLifecycle(tx, libraryOperationEnvelope{OpID: id, AuthorID: author, Counter: counter, Kind: kind}, []string{"file-a"}, context); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	state := func() string {
		var value string
		if err := db.QueryRow(`SELECT state FROM library_file_lifecycle WHERE file_id='file-a'`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	apply("trash-a", "a", 1, "file.trash", nil)
	apply("restore-b", "b", 1, "file.restore", nil)
	if got := state(); got != "trashed" {
		t.Fatalf("concurrent restore exposed trashed file: %s", got)
	}
	apply("restore-b2", "b", 2, "file.restore", map[string]int64{"a": 1})
	if got := state(); got != "active" {
		t.Fatalf("observed restore failed: %s", got)
	}
	apply("trash-a", "a", 1, "file.trash", nil)
	if got := state(); got != "active" {
		t.Fatalf("late trash replay hid restored file: %s", got)
	}
	apply("trash-c", "c", 1, "file.trash", nil)
	if got := state(); got != "trashed" {
		t.Fatalf("concurrent trash did not hide file: %s", got)
	}
	apply("delete-d", "d", 1, "file.delete", map[string]int64{"c": 1})
	apply("restore-c", "c", 2, "file.restore", map[string]int64{"d": 1})
	var tombstone int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id='file-a'`).Scan(&tombstone); err != nil || tombstone != 1 {
		t.Fatalf("permanent deletion tombstone lost: %d %v", tombstone, err)
	}
	if got := state(); got != "trashed" {
		t.Fatalf("restore changed deleted file state: %s", got)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	states, err := client.libraryLifecycleStates()
	if err != nil || states["file-a"] != "deleted" {
		t.Fatalf("deleted state not projected: %+v %v", states, err)
	}
}
