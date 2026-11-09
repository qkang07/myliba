package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLibrarySnapshotRoundTripAndCausalIntegrity(t *testing.T) {
	operations := []libraryOperationEnvelope{
		{OpID: "one", AuthorID: "phone", Counter: 1, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"Travel"}`)},
		{OpID: "two", AuthorID: "phone", Counter: 2, Kind: "tag.add", Payload: json.RawMessage(`{"fileIds":["file"],"tagName":"Trip"}`)},
	}
	snapshot, err := encodeLibrarySnapshot("phone", operations)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := decodeLibrarySnapshot(snapshot)
	if err != nil || len(replayed) != 2 || replayed[1].OpID != "two" {
		t.Fatalf("snapshot replay: %+v %v", replayed, err)
	}
	snapshot.Checksum = cloudHash([]byte("wrong"))
	if _, err := decodeLibrarySnapshot(snapshot); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
	snapshot.Checksum = cloudHash([]byte("restored"))
	snapshot.SchemaVersion = 2
	if _, err := decodeLibrarySnapshot(snapshot); err == nil {
		t.Fatal("accepted unknown schema")
	}
	operations[1].Counter = 3
	if _, err := encodeLibrarySnapshot("phone", operations); err == nil {
		t.Fatal("accepted causal gap")
	}
}

func TestLibrarySnapshotSegmentUsesAbsoluteCounters(t *testing.T) {
	operations := []libraryOperationEnvelope{
		{OpID: "hundred-one", AuthorID: "phone", Counter: 101, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"A"}`)},
		{OpID: "hundred-two", AuthorID: "phone", Counter: 102, Kind: "folder.create", Payload: json.RawMessage(`{"folderName":"B"}`)},
	}
	snapshot, err := encodeLibrarySnapshot("phone", operations)
	if err != nil || snapshot.FromCounter != 101 || snapshot.Frontier != 102 {
		t.Fatalf("segment: %+v %v", snapshot, err)
	}
	decoded, err := decodeLibrarySnapshot(snapshot)
	if err != nil || len(decoded) != 2 || decoded[0].Counter != 101 || decoded[1].Counter != 102 {
		t.Fatalf("segment replay: %+v %v", decoded, err)
	}
	snapshot.FromCounter = 100
	if _, err = decodeLibrarySnapshot(snapshot); err == nil {
		t.Fatal("accepted segment causal gap")
	}
}

func TestLibrarySnapshotPublisherSplitsLongAuthorHistory(t *testing.T) {
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
	for at := 0; at < 101; at++ {
		tx, beginErr := db.Begin()
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if err = recordLibraryOperation(tx, "folder.create", map[string]any{"folderName": "A"}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err = client.publishPendingOperations(); err != nil {
		t.Fatal(err)
	}
	if err = client.publishPendingOperations(); err != nil {
		t.Fatal(err)
	}
	if err = client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	if err = client.publishOperationSnapshots(); err != nil {
		t.Fatal(err)
	}
	ids, err := runtime.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	spans := map[int64]int64{}
	for _, id := range ids {
		file, openErr := runtime.disk.get("catalog", id)
		if openErr != nil {
			t.Fatal(openErr)
		}
		var record cloudRecord
		decodeErr := json.NewDecoder(file).Decode(&record)
		file.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if record.ObjectID[:min(len(record.ObjectID), len("library-snapshot/"))] != "library-snapshot/" {
			continue
		}
		manifest, openErr := client.openManifest(record)
		if openErr != nil || manifest.Snapshot == nil {
			t.Fatalf("snapshot decode: %v", openErr)
		}
		from := manifest.Snapshot.FromCounter
		if from == 0 {
			from = 1
		}
		spans[from] = manifest.Snapshot.Frontier
	}
	if spans[1] != 100 || spans[101] != 101 {
		var applied, published int64
		_ = db.QueryRow(`SELECT MAX(last_counter) FROM library_operation_frontiers`).Scan(&applied)
		_ = db.QueryRow(`SELECT MAX(frontier) FROM library_snapshot_publication`).Scan(&published)
		t.Fatalf("unexpected snapshot segments: %+v applied=%d published=%d", spans, applied, published)
	}
}

func TestLibrarySnapshotRestoresAfterOperationLogCompaction(t *testing.T) {
	sourceDB, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)
	if err := migrate(sourceDB); err != nil {
		t.Fatal(err)
	}
	source := testCloudRuntime(t, t.TempDir(), "")
	source.db = sourceDB
	client, err := newCloudClient(source)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := sourceDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordLibraryOperation(tx, "folder.create", map[string]any{"folderName": "Archive"}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := client.publishPendingOperations(); err != nil {
		t.Fatal(err)
	}
	if err := client.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	if err := client.publishOperationSnapshots(); err != nil {
		t.Fatal(err)
	}
	ids, err := source.disk.list("catalog")
	if err != nil {
		t.Fatal(err)
	}
	operationID, snapshotCount := "", 0
	for _, id := range ids {
		file, err := source.disk.get("catalog", id)
		if err != nil {
			t.Fatal(err)
		}
		var record cloudRecord
		if err := json.NewDecoder(file).Decode(&record); err != nil {
			file.Close()
			t.Fatal(err)
		}
		file.Close()
		if len(record.ObjectID) >= len("library-operation/") && record.ObjectID[:len("library-operation/")] == "library-operation/" {
			operationID = id
		}
		if len(record.ObjectID) >= len("library-snapshot/") && record.ObjectID[:len("library-snapshot/")] == "library-snapshot/" {
			snapshotCount++
		}
	}
	if operationID == "" || snapshotCount != 1 {
		t.Fatalf("snapshot publication: op=%q snapshots=%d", operationID, snapshotCount)
	}
	path, err := source.disk.existingPath("catalog", operationID)
	if err != nil || os.Remove(path) != nil {
		t.Fatalf("compact old operation: %v", err)
	}
	receiverDB, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "receiver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer receiverDB.Close()
	receiverDB.SetMaxOpenConns(1)
	if err := migrate(receiverDB); err != nil {
		t.Fatal(err)
	}
	receiver := testCloudRuntime(t, t.TempDir(), "")
	receiver.db = receiverDB
	if err := os.WriteFile(filepath.Join(receiver.disk.dir, "recovery.key"), client.key[:], 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := newCloudClient(receiver)
	if err != nil {
		t.Fatal(err)
	}
	tx, err = receiverDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := recordLibraryOperation(tx, "folder.create", map[string]any{"folderName": "Local pending"}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == operationID {
			continue
		}
		file, err := source.disk.get("catalog", id)
		if err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, cloudMaxRecordSize)
		n, err := file.Read(raw)
		file.Close()
		if err != nil && n == 0 {
			t.Fatal(err)
		}
		raw = raw[:n]
		if err := receiver.disk.put("catalog", id, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	if err := recovered.applyRemoteOperations(); err != nil {
		t.Fatal(err)
	}
	var folder, pending int
	if err := receiverDB.QueryRow(`SELECT COUNT(*) FROM folders WHERE name='Archive'`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if err := receiverDB.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE state='pending' AND kind='folder.create'`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if folder != 1 || pending != 1 {
		t.Fatalf("snapshot recovery lost state or local outbox: folder=%d pending=%d", folder, pending)
	}
}
