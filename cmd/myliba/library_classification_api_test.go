package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogicalClassificationEditsPreserveLegacyBaseline(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "logical.db"))
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
	app := &server{db: db, cloud: client}
	client.indexer = app
	index := fileIndexView{FileID: "file-a", RevisionID: "v1", Name: "example.txt", Modified: now(), Original: indexedContent{Size: 0}, OrganizationKnown: true, FolderName: "Travel", Tags: []string{"legacy"}}
	record := cloudRecord{Format: 3, LibraryID: client.libraryID(), ObjectID: "file-a", RevisionID: "v1", AuthorID: "old", CreatedAt: now(), BlockIDs: []string{}}
	record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
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
	call := func(method, tag, body string, handler http.HandlerFunc) {
		t.Helper()
		r := httptest.NewRequest(method, "/api/library/files/file-a", strings.NewReader(body))
		r.SetPathValue("fileId", "file-a")
		if tag != "" {
			r.SetPathValue("tag", tag)
		}
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, tag, w.Code, w.Body.String())
		}
	}
	call("POST", "", `{"name":"fresh"}`, app.addLogicalFileTag)
	items, err := client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Tags) != 2 || items[0].FolderName != "Travel" {
		t.Fatalf("legacy classification lost: %+v", items)
	}
	call("DELETE", "legacy", "", app.removeLogicalFileTag)
	call("PUT", "", `{"folderName":"New"}`, app.moveLogicalFile)
	items, err = client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Tags) != 1 || items[0].Tags[0] != "fresh" || items[0].FolderName != "New" {
		t.Fatalf("logical classification not applied: %+v", items)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_operations WHERE state='pending'`).Scan(&pending); err != nil || pending != 3 {
		t.Fatalf("outbox count %d: %v", pending, err)
	}
}
