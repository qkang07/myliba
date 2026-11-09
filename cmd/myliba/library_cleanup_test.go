package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLibraryCleanupRequiresSnapshotAndEveryActiveReceipt(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library_operation_frontiers(author_id,last_counter,updated_at) VALUES('writer',1,?)`, now()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library_members(node_id,epoch,state,author_id,counter,updated_at) VALUES('phone',1,'active','',0,'2026-01-01T00:00:00Z'),('old',1,'retired','',0,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	_, _, blocked, err := client.cleanupGate(nil)
	if err != nil || len(blocked) == 0 {
		t.Fatalf("missing snapshot should block: %v %v", blocked, err)
	}
	op := libraryOperationEnvelope{OpID: "one", AuthorID: "writer", Counter: 1, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"A"}`)}
	snapshot, err := encodeLibrarySnapshot("writer", []libraryOperationEnvelope{op})
	if err != nil {
		t.Fatal(err)
	}
	record, err := client.sealManifest(cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-snapshot/writer", RevisionID: newID(), AuthorID: "writer", CreatedAt: now(), BlockIDs: []string{}}, cloudManifest{Snapshot: &snapshot})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	hash := cloudHash(raw)
	if err = runtime.disk.put("catalog", hash, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO library_snapshot_publication(author_id,frontier,record_hash,published_at) VALUES('writer',1,?,?)`, hash, now()); err != nil {
		t.Fatal(err)
	}
	_, _, blocked, err = client.cleanupGate([]string{hash})
	if err != nil || !strings.Contains(strings.Join(blocked, " "), "phone") {
		t.Fatalf("missing active receipt should block: %v %v", blocked, err)
	}
	if _, err = db.Exec(`INSERT INTO library_receipts(node_id,frontiers_json,catalog_at) VALUES('phone','{"writer":1}',?)`, now()); err != nil {
		t.Fatal(err)
	}
	frontiers, members, blocked, err := client.cleanupGate([]string{hash})
	if err != nil || len(blocked) != 0 || frontiers["writer"] != 1 || len(members) != 1 || members[0] != "phone" {
		t.Fatalf("retired member must not block: %v %v %v %v", frontiers, members, blocked, err)
	}
	if _, err = db.Exec(`UPDATE library_receipts SET frontiers_json='{"writer":0}' WHERE node_id='phone'`); err != nil {
		t.Fatal(err)
	}
	_, _, blocked, err = client.cleanupGate([]string{hash})
	if err != nil || len(blocked) == 0 {
		t.Fatalf("stale receipt should block: %v %v", blocked, err)
	}
	second := libraryOperationEnvelope{OpID: "two", AuthorID: "writer", Counter: 2, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"B"}`)}
	segment, err := encodeLibrarySnapshot("writer", []libraryOperationEnvelope{second})
	if err != nil {
		t.Fatal(err)
	}
	nextRecord, err := client.sealManifest(cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: "library-snapshot/writer", RevisionID: newID(), AuthorID: "writer", CreatedAt: now(), BlockIDs: []string{}}, cloudManifest{Snapshot: &segment})
	if err != nil {
		t.Fatal(err)
	}
	nextRaw, err := json.Marshal(nextRecord)
	if err != nil {
		t.Fatal(err)
	}
	nextHash := cloudHash(nextRaw)
	if err = runtime.disk.put("catalog", nextHash, bytes.NewReader(nextRaw), cloudMaxRecordSize); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE library_operation_frontiers SET last_counter=2 WHERE author_id='writer'`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE library_snapshot_publication SET frontier=2,record_hash=? WHERE author_id='writer'`, nextHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE library_receipts SET frontiers_json='{"writer":2}' WHERE node_id='phone'`); err != nil {
		t.Fatal(err)
	}
	_, _, blocked, err = client.cleanupGate([]string{nextHash})
	if err != nil || len(blocked) == 0 {
		t.Fatalf("missing first snapshot segment should block: %v %v", blocked, err)
	}
	_, _, blocked, err = client.cleanupGate([]string{hash, nextHash})
	if err != nil || len(blocked) != 0 {
		t.Fatalf("continuous segments should pass: %v %v", blocked, err)
	}
}

func TestCleanupRecyclesOnlyCoveredDeletedContent(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-libraryCleanupRetention - time.Hour).UTC().Format(time.RFC3339Nano)
	for _, deletion := range []struct {
		fileID  string
		counter int64
		stamp   string
	}{
		{"file-covered", 3, old},
		{"file-recent", 3, now()},
		{"file-uncovered", 9, old},
	} {
		if _, err = db.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES(?,?,?)`, deletion.fileID, deletion.stamp, deletion.stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO library_file_deletions(file_id,author_id,counter,op_id,deleted_at) VALUES(?,'writer',?, ?, ?)`, deletion.fileID, deletion.counter, deletion.fileID, deletion.stamp); err != nil {
			t.Fatal(err)
		}
	}
	allRecords := map[string]cloudRecord{}
	local := []string{}
	addContent := func(object, fileID string) {
		record, sealErr := client.sealManifest(cloudRecord{Format: 4, LibraryID: client.libraryID(), ObjectID: object, RevisionID: object, AuthorID: "writer", CreatedAt: now(), BlockIDs: []string{}}, cloudManifest{FileID: fileID, Name: object})
		if sealErr != nil {
			t.Fatal(sealErr)
		}
		raw, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		id := cloudHash(raw)
		if err := runtime.disk.put("catalog", id, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
		allRecords[id] = record
		local = append(local, id)
	}
	addContent("object-covered", "file-covered")
	addContent("object-recent", "file-recent")
	addContent("object-uncovered", "file-uncovered")
	addContent("object-live", "file-live")
	plan := libraryCleanupPlan{SnapshotFrontiers: map[string]int64{"writer": 5}}
	if err = client.appendDeletedContent(&plan, local, allRecords); err != nil {
		t.Fatal(err)
	}
	if len(plan.Eligible) != 1 || plan.Eligible[0].Kind != "content" || allRecords[plan.Eligible[0].ID].ObjectID != "object-covered" {
		t.Fatalf("only covered, retained deletions may be recycled: %+v", plan.Eligible)
	}
}

func TestCleanupCompactsOnlyCoveredFolderTombstones(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-libraryCleanupRetention - time.Hour).UTC().Format(time.RFC3339Nano)
	for _, tombstone := range []struct {
		name    string
		author  string
		counter int64
		stamp   string
	}{
		{"covered", "writer", 3, old},
		{"recent", "writer", 3, now()},
		{"uncovered", "writer", 9, old},
		{"unknown-author", "ghost", 1, old},
	} {
		if _, err = db.Exec(`INSERT INTO library_folder_tombstones(name,author_id,counter,deleted_at) VALUES(?,?,?,?)`, tombstone.name, tombstone.author, tombstone.counter, tombstone.stamp); err != nil {
			t.Fatal(err)
		}
	}
	compacted, err := client.compactFolderTombstones(map[string]int64{"writer": 5})
	if err != nil || compacted != 1 {
		t.Fatalf("only covered retained tombstones may compact: %d %v", compacted, err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_folder_tombstones WHERE name='covered'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("covered tombstone not compacted: %d %v", remaining, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_folder_tombstones`).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("uncovered tombstones must remain: %d %v", remaining, err)
	}
}
