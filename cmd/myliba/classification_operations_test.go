package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// classificationPeerServers builds two nodes of one library: they share the
// catalog disk (and therefore the recovery key) but keep separate databases.
func classificationPeerServers(t *testing.T) (*server, *sql.DB, *server, *sql.DB) {
	t.Helper()
	sharedDir := t.TempDir()
	build := func() (*server, *sql.DB) {
		db := classificationTestDB(t)
		runtime := testCloudRuntime(t, sharedDir, "")
		runtime.db = db
		client, err := newCloudClient(runtime)
		if err != nil {
			t.Fatal(err)
		}
		return &server{db: db, cloud: client}, db
	}
	appA, dbA := build()
	appB, dbB := build()
	if appA.cloud.libraryID() != appB.cloud.libraryID() {
		t.Fatal("peer servers must share one library identity")
	}
	return appA, dbA, appB, dbB
}

func classificationOperationsOf(t *testing.T, db *sql.DB, libraryID string) []libraryOperationEnvelope {
	t.Helper()
	rows, err := db.Query(`SELECT op_id,author_id,counter,kind,payload_json FROM classification_operations WHERE library_id=? ORDER BY clock,author_id,counter,op_id`, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	operations := []libraryOperationEnvelope{}
	for rows.Next() {
		var op libraryOperationEnvelope
		var payload string
		if err = rows.Scan(&op.OpID, &op.AuthorID, &op.Counter, &op.Kind, &payload); err != nil {
			t.Fatal(err)
		}
		op.Payload = json.RawMessage(payload)
		operations = append(operations, op)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return operations
}

// deliverClassificationOperation mirrors the transport receiver: causality
// validation and replay commit together with the inbox and frontier advance.
func deliverClassificationOperation(t *testing.T, db *sql.DB, libraryID, nodeID string, op libraryOperationEnvelope) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = receiveClassificationOperation(tx, libraryID, nodeID, op); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO library_operation_inbox(op_id,author_id,counter,received_at,applied_at) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, op.OpID, op.AuthorID, op.Counter, now(), now()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO library_operation_frontiers(author_id,last_counter,updated_at) VALUES(?,?,?) ON CONFLICT(author_id) DO UPDATE SET last_counter=MAX(excluded.last_counter,last_counter),updated_at=excluded.updated_at`, op.AuthorID, op.Counter, now()); err != nil {
		t.Fatal(err)
	}
	return tx.Commit()
}

func createIndexViaAPI(t *testing.T, app *server, requestID, name string, parent any) classificationIndexView {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"requestId": requestID, "name": name, "parentId": parent})
	response := callClassificationHandler(t, http.MethodPost, "/api/library/indexes", string(raw), nil, app.createClassificationIndex)
	if response.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, response.Code, response.Body.String())
	}
	return decodeClassificationResponse[struct {
		Node classificationIndexView `json:"node"`
	}](t, response).Node
}

func updateIndexViaAPI(t *testing.T, app *server, db *sql.DB, requestID, indexID string, patch map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var parent sql.NullString
	var name, nameOp, parentOp string
	var deleted int
	if err := db.QueryRow(`SELECT parent_id,name,deleted,name_op_id,parent_op_id FROM classification_nodes WHERE library_id=? AND index_id=?`, app.cloud.libraryID(), indexID).Scan(&parent, &name, &deleted, &nameOp, &parentOp); err != nil {
		t.Fatal(err)
	}
	baseVersion := classificationNodeVersion(indexID, parent.String, name, deleted, nameOp, parentOp)
	raw, _ := json.Marshal(map[string]any{"requestId": requestID, "baseVersion": baseVersion, "patch": patch})
	return callClassificationHandler(t, http.MethodPut, "/api/library/indexes/"+indexID, string(raw), map[string]string{"id": indexID}, app.updateClassificationIndex)
}

func classificationGenerationOf(t *testing.T, db *sql.DB, libraryID string) string {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	generation, err := classificationSnapshotGeneration(tx, libraryID)
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestClassificationReplayConvergesOnMutualMoveCycle(t *testing.T) {
	appA, dbA, appB, dbB := classificationPeerServers(t)
	libraryID := appA.cloud.libraryID()
	root := createIndexViaAPI(t, appA, "root", "P", nil)
	y25 := createIndexViaAPI(t, appA, "y25", "Y25", root.ID)
	y26 := createIndexViaAPI(t, appA, "y26", "Y26", root.ID)
	for _, op := range classificationOperationsOf(t, dbA, libraryID) {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, op); err != nil {
			t.Fatalf("deliver create: %v", err)
		}
	}
	// Both sides move one child under the other without seeing the peer move.
	if response := updateIndexViaAPI(t, appB, dbB, "b-move", y25.ID, map[string]any{"parentId": y26.ID}); response.Code != http.StatusOK {
		t.Fatalf("B move: %d %s", response.Code, response.Body.String())
	}
	if response := updateIndexViaAPI(t, appA, dbA, "a-move", y26.ID, map[string]any{"parentId": y25.ID}); response.Code != http.StatusOK {
		t.Fatalf("A move: %d %s", response.Code, response.Body.String())
	}
	opsA := classificationOperationsOf(t, dbA, libraryID)
	opsB := classificationOperationsOf(t, dbB, libraryID)
	if err := deliverClassificationOperation(t, dbA, libraryID, appA.cloud.runtime.id, opsB[len(opsB)-1]); err != nil {
		t.Fatalf("deliver B move to A: %v", err)
	}
	if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, opsA[len(opsA)-1]); err != nil {
		t.Fatalf("deliver A move to B: %v", err)
	}
	if generationA, generationB := classificationGenerationOf(t, dbA, libraryID), classificationGenerationOf(t, dbB, libraryID); generationA != generationB {
		t.Fatalf("mutual move diverged: %s != %s", generationA, generationB)
	}
	for _, db := range []*sql.DB{dbA, dbB} {
		var cycles int
		if err := db.QueryRow(`SELECT COUNT(*) FROM classification_conflicts WHERE library_id=? AND reason='indexCycle'`, libraryID).Scan(&cycles); err != nil || cycles != 1 {
			t.Fatalf("expected one cycle conflict, got %d (%v)", cycles, err)
		}
	}
	// Duplicate and out-of-order redelivery must not change the projection.
	generation := classificationGenerationOf(t, dbA, libraryID)
	for i := len(opsA) - 1; i >= 0; i-- {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, opsA[i]); err != nil {
			t.Fatalf("redelivery %d: %v", i, err)
		}
	}
	if after := classificationGenerationOf(t, dbB, libraryID); after != generation {
		t.Fatalf("redelivery changed projection: %s != %s", after, generation)
	}
}

func TestClassificationConcurrentRenameAndExplicitResolve(t *testing.T) {
	appA, dbA, appB, dbB := classificationPeerServers(t)
	libraryID := appA.cloud.libraryID()
	node := createIndexViaAPI(t, appA, "create", "Original", nil)
	for _, op := range classificationOperationsOf(t, dbA, libraryID) {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, op); err != nil {
			t.Fatal(err)
		}
	}
	if response := updateIndexViaAPI(t, appA, dbA, "a-rename", node.ID, map[string]any{"name": "Alpha"}); response.Code != http.StatusOK {
		t.Fatalf("A rename: %d %s", response.Code, response.Body.String())
	}
	if response := updateIndexViaAPI(t, appB, dbB, "b-rename", node.ID, map[string]any{"name": "Beta"}); response.Code != http.StatusOK {
		t.Fatalf("B rename: %d %s", response.Code, response.Body.String())
	}
	opsA := classificationOperationsOf(t, dbA, libraryID)
	opsB := classificationOperationsOf(t, dbB, libraryID)
	if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, opsA[len(opsA)-1]); err != nil {
		t.Fatal(err)
	}
	if err := deliverClassificationOperation(t, dbA, libraryID, appA.cloud.runtime.id, opsB[len(opsB)-1]); err != nil {
		t.Fatal(err)
	}
	if generationA, generationB := classificationGenerationOf(t, dbA, libraryID), classificationGenerationOf(t, dbB, libraryID); generationA != generationB {
		t.Fatalf("concurrent rename diverged: %s != %s", generationA, generationB)
	}
	var loserOpID string
	if err := dbA.QueryRow(`SELECT op_id FROM classification_conflicts WHERE library_id=? AND reason='concurrentName'`, libraryID).Scan(&loserOpID); err != nil {
		t.Fatalf("expected concurrentName conflict: %v", err)
	}
	resolveBody, _ := json.Marshal(map[string]any{"requestId": "resolve", "indexId": node.ID, "patch": map[string]any{"name": "Chosen"}})
	response := callClassificationHandler(t, http.MethodPost, "/api/library/index-conflicts/"+loserOpID+"/resolve", string(resolveBody), map[string]string{"opId": loserOpID}, appA.resolveLibraryIndexConflict)
	if response.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", response.Code, response.Body.String())
	}
	opsA = classificationOperationsOf(t, dbA, libraryID)
	if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, opsA[len(opsA)-1]); err != nil {
		t.Fatal(err)
	}
	for _, db := range []*sql.DB{dbA, dbB} {
		var name string
		var resolved int
		if err := db.QueryRow(`SELECT name FROM classification_nodes WHERE library_id=? AND index_id=?`, libraryID, node.ID).Scan(&name); err != nil || name != "Chosen" {
			t.Fatalf("resolve did not converge: name=%q err=%v", name, err)
		}
		if err := db.QueryRow(`SELECT resolved FROM classification_conflicts WHERE library_id=? AND op_id=?`, libraryID, loserOpID).Scan(&resolved); err != nil || resolved != 1 {
			t.Fatalf("conflict not marked resolved: %d %v", resolved, err)
		}
	}
	if generationA, generationB := classificationGenerationOf(t, dbA, libraryID), classificationGenerationOf(t, dbB, libraryID); generationA != generationB {
		t.Fatalf("resolve diverged: %s != %s", generationA, generationB)
	}
}

func TestClassificationRemoveKeepsConcurrentAddToken(t *testing.T) {
	appA, dbA, appB, dbB := classificationPeerServers(t)
	libraryID := appA.cloud.libraryID()
	insertClassificationResource(t, dbA, "root", "file-a", "a.jpg")
	insertClassificationResource(t, dbB, "root", "file-a", "a.jpg")
	node := createIndexViaAPI(t, appA, "create", "X", nil)
	for _, op := range classificationOperationsOf(t, dbA, libraryID) {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, op); err != nil {
			t.Fatal(err)
		}
	}
	membership := func(app *server, requestID string, add, remove []string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"requestId": requestID, "fileIds": []string{"file-a"}, "addIndexIds": add, "removeIndexIds": remove})
		return callClassificationHandler(t, http.MethodPost, "/api/library/index-memberships", string(raw), nil, app.changeClassificationMemberships)
	}
	// B adds concurrently before observing A's add; A then removes only what it saw.
	if response := membership(appB, "b-add", []string{node.ID}, nil); response.Code != http.StatusOK {
		t.Fatalf("B add: %d %s", response.Code, response.Body.String())
	}
	if response := membership(appA, "a-add", []string{node.ID}, nil); response.Code != http.StatusOK {
		t.Fatalf("A add: %d %s", response.Code, response.Body.String())
	}
	if response := membership(appA, "a-remove", nil, []string{node.ID}); response.Code != http.StatusOK {
		t.Fatalf("A remove: %d %s", response.Code, response.Body.String())
	}
	opsA := classificationOperationsOf(t, dbA, libraryID)
	for _, op := range opsA[1:] {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, op); err != nil {
			t.Fatalf("deliver A membership op: %v", err)
		}
	}
	opsB := classificationOperationsOf(t, dbB, libraryID)
	for _, op := range opsB {
		if op.AuthorID == opsA[0].AuthorID {
			continue
		}
		if err := deliverClassificationOperation(t, dbA, libraryID, appA.cloud.runtime.id, op); err != nil {
			t.Fatalf("deliver B add: %v", err)
		}
	}
	for _, db := range []*sql.DB{dbA, dbB} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-a' AND index_id=?`, libraryID, node.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("concurrent add token lost: count=%d err=%v", count, err)
		}
	}
	if generationA, generationB := classificationGenerationOf(t, dbA, libraryID), classificationGenerationOf(t, dbB, libraryID); generationA != generationB {
		t.Fatalf("observed-remove diverged: %s != %s", generationA, generationB)
	}
}

func TestClassificationFrontierCompressedRemove(t *testing.T) {
	appA, dbA, appB, dbB := classificationPeerServers(t)
	libraryID := appA.cloud.libraryID()
	insertClassificationResource(t, dbA, "root", "file-a", "a.jpg")
	insertClassificationResource(t, dbB, "root", "file-a", "a.jpg")
	node := createIndexViaAPI(t, appA, "create", "X", nil)
	membership := func(requestID string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"requestId": requestID, "fileIds": []string{"file-a"}, "addIndexIds": []string{node.ID}, "removeIndexIds": []string{}})
		return callClassificationHandler(t, http.MethodPost, "/api/library/index-memberships", string(raw), nil, appA.changeClassificationMemberships)
	}
	if response := membership("add-1"); response.Code != http.StatusOK {
		t.Fatalf("add 1: %d %s", response.Code, response.Body.String())
	}
	if response := membership("add-2"); response.Code != http.StatusOK {
		t.Fatalf("add 2: %d %s", response.Code, response.Body.String())
	}
	opsA := classificationOperationsOf(t, dbA, libraryID)
	for _, op := range opsA {
		if err := deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, op); err != nil {
			t.Fatal(err)
		}
	}
	// B compresses its remove into a frontier instead of listing every token.
	tx, err := dbB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	authorB, err := classificationOperationAuthor(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var clockA int64
	if err = dbB.QueryRow(`SELECT last_clock FROM classification_author_clocks WHERE library_id=? AND author_id=?`, libraryID, opsA[0].AuthorID).Scan(&clockA); err != nil {
		t.Fatal(err)
	}
	domain := map[string]any{
		"schemaVersion": 2, "author": authorB, "counter": 1, "clock": clockA + 1,
		"context": map[string]int64{opsA[0].AuthorID: opsA[len(opsA)-1].Counter},
		"files":   []classificationFileMembership{{FileID: "file-a", Removes: []classificationMembershipRemove{{IndexID: node.ID, ObservedFrontier: &classificationObservedFrontier{Author: opsA[0].AuthorID, Counter: opsA[len(opsA)-1].Counter}}}}},
	}
	payload, _ := json.Marshal(domain)
	compressed := libraryOperationEnvelope{OpID: newID(), AuthorID: authorB, Counter: 1, Kind: "index.memberships", Payload: payload}
	if err = deliverClassificationOperation(t, dbB, libraryID, appB.cloud.runtime.id, compressed); err != nil {
		t.Fatalf("compressed remove: %v", err)
	}
	if err = deliverClassificationOperation(t, dbA, libraryID, appA.cloud.runtime.id, compressed); err != nil {
		t.Fatalf("deliver compressed remove: %v", err)
	}
	for _, db := range []*sql.DB{dbA, dbB} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-a'`, libraryID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("frontier remove left memberships: %d %v", count, err)
		}
	}
	if generationA, generationB := classificationGenerationOf(t, dbA, libraryID), classificationGenerationOf(t, dbB, libraryID); generationA != generationB {
		t.Fatalf("frontier remove diverged: %s != %s", generationA, generationB)
	}
}

func TestClassificationReplaySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, cloud: client}
	libraryID := client.libraryID()
	node := createIndexViaAPI(t, app, "create", "Persistent", nil)
	child := createIndexViaAPI(t, app, "child", "Child", node.ID)
	generation := classificationGenerationOf(t, db, libraryID)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetMaxOpenConns(1)
	if _, err = reopened.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = refreshClassificationProjection(reopened, libraryID, "desktop-restart"); err != nil {
		t.Fatal(err)
	}
	var name, parent string
	if err = reopened.QueryRow(`SELECT name,parent_id FROM classification_nodes WHERE library_id=? AND index_id=?`, libraryID, child.ID).Scan(&name, &parent); err != nil || name != "Child" || parent != node.ID {
		t.Fatalf("restart lost edits: name=%q parent=%q err=%v", name, parent, err)
	}
	if after := classificationGenerationOf(t, reopened, libraryID); after != generation {
		t.Fatalf("restart changed projection: %s != %s", after, generation)
	}
}

func TestClassificationReceiverRejectsBadClockAndSchema(t *testing.T) {
	app, db := classificationTestServer(t)
	libraryID := app.cloud.libraryID()
	createIndexViaAPI(t, app, "create", "Base", nil)
	original := classificationOperationsOf(t, db, libraryID)[0]

	badSchema := original
	badSchema.OpID = newID()
	badSchema.AuthorID = "index-peer"
	badSchema.Payload = json.RawMessage(`{"schemaVersion":99,"clock":5}`)
	if err := deliverClassificationOperation(t, db, libraryID, app.cloud.runtime.id, badSchema); !errors.Is(err, errClassificationQuarantine) {
		t.Fatalf("bad schema accepted: %v", err)
	}

	var clock int64
	if err := db.QueryRow(`SELECT clock FROM classification_operations WHERE library_id=? AND op_id=?`, libraryID, original.OpID).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	badClock := original
	badClock.OpID = newID()
	badClock.AuthorID = "index-peer"
	badClock.Counter = 1
	var header classificationOpHeader
	if err := json.Unmarshal(original.Payload, &header); err != nil {
		t.Fatal(err)
	}
	header.Author = "index-peer"
	header.Counter = 1
	header.Clock = clock // Must strictly exceed the observed predecessor.
	header.Context = map[string]int64{original.AuthorID: original.Counter}
	badClock.Payload, _ = json.Marshal(header)
	if err := deliverClassificationOperation(t, db, libraryID, app.cloud.runtime.id, badClock); !errors.Is(err, errClassificationQuarantine) {
		t.Fatalf("bad clock accepted: %v", err)
	}

	pending := badClock
	pending.OpID = newID()
	header.Clock = clock + 1
	header.Context = map[string]int64{"index-unknown-author": 3}
	pending.Payload, _ = json.Marshal(header)
	if err := deliverClassificationOperation(t, db, libraryID, app.cloud.runtime.id, pending); !errors.Is(err, errClassificationPending) {
		t.Fatalf("missing predecessor accepted: %v", err)
	}
}
