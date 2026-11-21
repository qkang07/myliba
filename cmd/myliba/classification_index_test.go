package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func classificationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "classification.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = migrate(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func classificationTestServer(t *testing.T) (*server, *sql.DB) {
	t.Helper()
	app, client := classificationTestNode(t, "test-"+newID())
	if err := initializeClassificationLibrary(app.db, client.libraryID()); err != nil {
		t.Fatal(err)
	}
	return app, app.db
}

func callClassificationHandler(t *testing.T, method, target, body string, pathValues map[string]string, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	for key, value := range pathValues {
		request.SetPathValue(key, value)
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func decodeClassificationResponse[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func seedClassificationLibrary(t *testing.T, db *sql.DB, folderID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'')`, folderID, name, now()); err != nil {
		t.Fatal(err)
	}
}

func insertClassificationResource(t *testing.T, db *sql.DB, rootID, fileID, name string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO roots(id,path,added_at) VALUES(?,?,?)`, rootID, `C:\`+rootID, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources(id,root_id,path,file_id,name,size,modified_at,kind,first_seen_at,last_seen_at)
		VALUES(?,?,?,?,?,1,?,'image',?,?)`, fileID, rootID, `C:\`+rootID+`\`+name, fileID, name, now(), now(), now()); err != nil {
		t.Fatal(err)
	}
}

func TestClassificationTreeEditingAndMembershipIdempotence(t *testing.T) {
	app, db := classificationTestServer(t)
	insertClassificationResource(t, db, "root", "file-a", "a.jpg")
	type nodeResult struct {
		Node        classificationIndexView `json:"node"`
		OperationID string                  `json:"operationId"`
	}
	create := func(requestID, name string, parent any) (*httptest.ResponseRecorder, nodeResult) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"requestId": requestID, "name": name, "parentId": parent})
		response := callClassificationHandler(t, http.MethodPost, "/api/library/indexes", string(raw), nil, app.createClassificationIndex)
		var result nodeResult
		if response.Code == http.StatusCreated || response.Code == http.StatusOK {
			result = decodeClassificationResponse[nodeResult](t, response)
		}
		return response, result
	}
	rootResponse, root := create("create-root", "Root", nil)
	if rootResponse.Code != http.StatusCreated || root.Node.ID == "" || root.Node.ParentID != nil {
		t.Fatalf("create root: %d %s", rootResponse.Code, rootResponse.Body.String())
	}
	repeated, repeatedRoot := create("create-root", "Root", nil)
	if repeated.Code != http.StatusOK || repeatedRoot.OperationID != root.OperationID || repeatedRoot.Node.ID != root.Node.ID {
		t.Fatalf("idempotent create: %d %s", repeated.Code, repeated.Body.String())
	}
	conflict, _ := create("create-root", "Different", nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("request id conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	childResponse, child := create("create-child", "Child", root.Node.ID)
	if childResponse.Code != http.StatusCreated || child.Node.ParentID == nil || *child.Node.ParentID != root.Node.ID || len(child.Node.Ancestors) != 1 || child.Node.Ancestors[0].ID != root.Node.ID {
		t.Fatalf("create child: %d %+v %s", childResponse.Code, child, childResponse.Body.String())
	}
	duplicate, _ := create("create-duplicate", "Root", nil)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate sibling: %d %s", duplicate.Code, duplicate.Body.String())
	}

	cycleBody, _ := json.Marshal(map[string]any{"requestId": "move-cycle", "baseVersion": root.Node.Version, "patch": map[string]any{"parentId": child.Node.ID}})
	cycle := callClassificationHandler(t, http.MethodPut, "/api/library/indexes/"+root.Node.ID, string(cycleBody), map[string]string{"id": root.Node.ID}, app.updateClassificationIndex)
	if cycle.Code != http.StatusConflict {
		t.Fatalf("cycle move: %d %s", cycle.Code, cycle.Body.String())
	}

	updateBody, _ := json.Marshal(map[string]any{"requestId": "update-child", "baseVersion": child.Node.Version, "patch": map[string]any{"name": "Renamed", "parentId": nil}})
	updatedResponse := callClassificationHandler(t, http.MethodPut, "/api/library/indexes/"+child.Node.ID, string(updateBody), map[string]string{"id": child.Node.ID}, app.updateClassificationIndex)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update child: %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	updated := decodeClassificationResponse[nodeResult](t, updatedResponse)
	if updated.Node.Name != "Renamed" || updated.Node.ParentID != nil || len(updated.Node.Ancestors) != 0 || updated.Node.Version == child.Node.Version {
		t.Fatalf("updated child: %+v", updated)
	}
	staleBody, _ := json.Marshal(map[string]any{"requestId": "stale-child", "baseVersion": child.Node.Version, "patch": map[string]any{"name": "Stale"}})
	stale := callClassificationHandler(t, http.MethodPut, "/api/library/indexes/"+child.Node.ID, string(staleBody), map[string]string{"id": child.Node.ID}, app.updateClassificationIndex)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update: %d %s", stale.Code, stale.Body.String())
	}

	membership := func(requestID string, add, remove []string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"requestId": requestID, "fileIds": []string{"file-a"}, "addIndexIds": add, "removeIndexIds": remove})
		return callClassificationHandler(t, http.MethodPost, "/api/library/index-memberships", string(raw), nil, app.changeClassificationMemberships)
	}
	added := membership("membership-add", []string{root.Node.ID, child.Node.ID}, nil)
	if added.Code != http.StatusOK {
		t.Fatalf("add memberships: %d %s", added.Code, added.Body.String())
	}
	addedResult := decodeClassificationResponse[struct {
		ChangedFiles int                 `json:"changedFiles"`
		OperationID  string              `json:"operationId"`
		State        map[string][]string `json:"state"`
	}](t, added)
	if addedResult.ChangedFiles != 1 || len(addedResult.State["file-a"]) != 2 {
		t.Fatalf("add result: %+v", addedResult)
	}
	repeatedAdd := membership("membership-add", []string{root.Node.ID, child.Node.ID}, nil)
	repeatedAddResult := decodeClassificationResponse[struct {
		OperationID string `json:"operationId"`
	}](t, repeatedAdd)
	if repeatedAdd.Code != http.StatusOK || repeatedAddResult.OperationID != addedResult.OperationID {
		t.Fatalf("idempotent membership: %d %s", repeatedAdd.Code, repeatedAdd.Body.String())
	}
	noChange := membership("membership-add-again", []string{root.Node.ID}, nil)
	if result := decodeClassificationResponse[struct {
		ChangedFiles int `json:"changedFiles"`
	}](t, noChange); noChange.Code != http.StatusOK || result.ChangedFiles != 0 {
		t.Fatalf("duplicate logical membership: %d %+v", noChange.Code, result)
	}
	removedRoot := membership("membership-remove-root", nil, []string{root.Node.ID})
	if removedRoot.Code != http.StatusOK {
		t.Fatalf("remove root: %d %s", removedRoot.Code, removedRoot.Body.String())
	}

	deleteTarget := "/api/library/indexes/" + child.Node.ID + "?requestId=delete-nonempty&baseVersion=" + updated.Node.Version
	nonempty := callClassificationHandler(t, http.MethodDelete, deleteTarget, "", map[string]string{"id": child.Node.ID}, app.deleteClassificationIndex)
	if nonempty.Code != http.StatusConflict {
		t.Fatalf("delete nonempty: %d %s", nonempty.Code, nonempty.Body.String())
	}
	if response := membership("membership-remove-child", nil, []string{child.Node.ID}); response.Code != http.StatusOK {
		t.Fatalf("remove child: %d %s", response.Code, response.Body.String())
	}
	deleteTarget = "/api/library/indexes/" + child.Node.ID + "?requestId=delete-child&baseVersion=" + updated.Node.Version
	deleted := callClassificationHandler(t, http.MethodDelete, deleteTarget, "", map[string]string{"id": child.Node.ID}, app.deleteClassificationIndex)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete child: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := refreshClassificationProjection(db, app.cloud.libraryID(), app.cloud.runtime.id); err != nil {
		t.Fatal(err)
	}
	var activeManual, deletedChild int
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND origin='manual' AND deleted=0`, app.cloud.libraryID()).Scan(&activeManual); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT deleted FROM classification_nodes WHERE library_id=? AND index_id=?`, app.cloud.libraryID(), child.Node.ID).Scan(&deletedChild); err != nil {
		t.Fatal(err)
	}
	if activeManual != 1 || deletedChild != 1 {
		t.Fatalf("refresh overwrote edits: active=%d deleted=%d", activeManual, deletedChild)
	}
}

func TestClassificationDirectoryImportKeepsUserEditsAndProgress(t *testing.T) {
	app, db := classificationTestServer(t)
	insertClassificationResource(t, db, "root", "file-a", "a.jpg")
	rootPath := t.TempDir()
	firstPath := filepath.Join(rootPath, "2025", "Hangzhou", "a.jpg")
	if err := app.classifyScannedResource(app.cloud.libraryID(), app.cloud.runtime.id, "root", rootPath, firstPath, "file-a"); err != nil {
		t.Fatal(err)
	}
	var leafID, rootIndexID string
	if err := db.QueryRow(`SELECT index_id FROM classification_source_dirs WHERE library_id=? AND root_id='root' AND source_dir_key=?`, app.cloud.libraryID(), classificationSourceDirectoryKey("2025/Hangzhou")).Scan(&leafID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT index_id FROM classification_source_dirs WHERE library_id=? AND root_id='root' AND source_dir_key='.'`, app.cloud.libraryID()).Scan(&rootIndexID); err != nil {
		t.Fatal(err)
	}
	var direct int
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-a' AND index_id=?`, app.cloud.libraryID(), leafID).Scan(&direct); err != nil || direct != 1 {
		t.Fatalf("initial directory membership: %d %v", direct, err)
	}

	var leafParent sql.NullString
	var leafName, leafNameOp, leafParentOp string
	var leafDeleted int
	if err := db.QueryRow(`SELECT parent_id,name,deleted,name_op_id,parent_op_id FROM classification_nodes WHERE library_id=? AND index_id=?`, app.cloud.libraryID(), leafID).Scan(&leafParent, &leafName, &leafDeleted, &leafNameOp, &leafParentOp); err != nil {
		t.Fatal(err)
	}
	baseVersion := classificationNodeVersion(leafID, leafParent.String, leafName, leafDeleted, leafNameOp, leafParentOp)
	editBody, _ := json.Marshal(map[string]any{"requestId": "user-edit", "baseVersion": baseVersion, "patch": map[string]any{"name": "Jiangnan", "parentId": rootIndexID}})
	editResponse := callClassificationHandler(t, http.MethodPut, "/api/library/indexes/"+leafID, string(editBody), map[string]string{"id": leafID}, app.updateClassificationIndex)
	if editResponse.Code != http.StatusOK {
		t.Fatalf("user edit: %d %s", editResponse.Code, editResponse.Body.String())
	}

	insertClassificationResource(t, db, "root", "file-b", "b.jpg")
	secondPath := filepath.Join(rootPath, "2025", "Hangzhou", "b.jpg")
	if err := app.classifyScannedResource(app.cloud.libraryID(), app.cloud.runtime.id, "root", rootPath, secondPath, "file-b"); err != nil {
		t.Fatal(err)
	}
	var name, parent string
	if err := db.QueryRow(`SELECT name,parent_id FROM classification_nodes WHERE library_id=? AND index_id=?`, app.cloud.libraryID(), leafID).Scan(&name, &parent); err != nil {
		t.Fatal(err)
	}
	if name != "Jiangnan" || parent != rootIndexID {
		t.Fatalf("directory mapping overwrote user edit: name=%q parent=%q", name, parent)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-b' AND index_id=?`, app.cloud.libraryID(), leafID).Scan(&direct); err != nil || direct != 1 {
		t.Fatalf("new file did not reuse edited node: %d %v", direct, err)
	}

	body, _ := json.Marshal(map[string]any{"requestId": "remove-auto", "fileIds": []string{"file-a"}, "addIndexIds": []string{}, "removeIndexIds": []string{leafID}})
	response := callClassificationHandler(t, http.MethodPost, "/api/library/index-memberships", string(body), nil, app.changeClassificationMemberships)
	if response.Code != http.StatusOK {
		t.Fatalf("remove automatic membership: %d %s", response.Code, response.Body.String())
	}
	if err := app.classifyScannedResource(app.cloud.libraryID(), app.cloud.runtime.id, "root", rootPath, firstPath, "file-a"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-a'`, app.cloud.libraryID()).Scan(&direct); err != nil || direct != 0 {
		t.Fatalf("rescan resurrected removed automatic membership: %d %v", direct, err)
	}
}

func TestClassificationDirectoryImportRetriesAfterProgressCommitFailure(t *testing.T) {
	app, db := classificationTestServer(t)
	insertClassificationResource(t, db, "root", "file-a", "a.jpg")
	rootPath := t.TempDir()
	filePath := filepath.Join(rootPath, "2025", "a.jpg")
	if _, err := db.Exec(`CREATE TRIGGER classification_import_fail BEFORE INSERT ON classification_imports BEGIN SELECT RAISE(ABORT,'injected progress failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := app.classifyScannedResource(app.cloud.libraryID(), app.cloud.runtime.id, "root", rootPath, filePath, "file-a"); err == nil {
		t.Fatal("classification import unexpectedly survived injected progress failure")
	}
	var adds, progress int
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_adds WHERE library_id=? AND origin='automatic'`, app.cloud.libraryID()).Scan(&adds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_imports WHERE library_id=?`, app.cloud.libraryID()).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if adds != 0 || progress != 0 {
		t.Fatalf("failed import committed partial state: adds=%d progress=%d", adds, progress)
	}
	if _, err := db.Exec(`DROP TRIGGER classification_import_fail`); err != nil {
		t.Fatal(err)
	}
	if err := app.classifyScannedResource(app.cloud.libraryID(), app.cloud.runtime.id, "root", rootPath, filePath, "file-a"); err != nil {
		t.Fatal(err)
	}
	var memberships int
	if err := db.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND file_id='file-a'`, app.cloud.libraryID()).Scan(&memberships); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Fatalf("retry did not complete initial membership: %d", memberships)
	}
}
