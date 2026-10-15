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

func TestVirtualFolderScopeAndDeletion(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "folders.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO roots(id,path,added_at) VALUES('root','/library','2026-01-01')`,
		`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES
            ('a','root','/library/a.jpg','a.jpg',1,'2026-01-03','image',1,'2026-01-03','2026-01-03'),
            ('b','root','/library/b.jpg','b.jpg',1,'2026-01-02','image',1,'2026-01-02','2026-01-02'),
            ('c','root','/library/c.jpg','c.jpg',1,'2026-01-01','image',1,'2026-01-01','2026-01-01')`,
		`INSERT INTO tags(id,name,created_at) VALUES('red','Red','2026-01-01'),('blue','Blue','2026-01-01'),('green','Green','2026-01-01')`,
		`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES('a','red','2026-01-01'),('b','blue','2026-01-01'),('c','green','2026-01-01')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	app := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/folders", app.listFolders)
	mux.HandleFunc("POST /api/folders", app.createFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", app.deleteFolder)
	mux.HandleFunc("PUT /api/resources/folder", app.moveResourcesToFolder)
	mux.HandleFunc("GET /api/resources", app.listResources)
	mux.HandleFunc("GET /api/tags", app.tags)
	mux.HandleFunc("POST /api/collections", app.createCollection)
	mux.HandleFunc("GET /api/collections", app.listCollections)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	created := request(http.MethodPost, "/api/folders", `{"name":"Projects"}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var folder virtualFolder
	if err := json.Unmarshal(created.Body.Bytes(), &folder); err != nil {
		t.Fatal(err)
	}
	if folder.ID == "" {
		t.Fatal("missing folder id")
	}
	if response := request(http.MethodPost, "/api/folders", `{"name":"projects"}`); response.Code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %s", response.Code, response.Body.String())
	}
	move := request(http.MethodPut, "/api/resources/folder", `{"ids":["a","b"],"folderId":"`+folder.ID+`"}`)
	if move.Code != http.StatusOK {
		t.Fatalf("move: %d %s", move.Code, move.Body.String())
	}
	var page struct {
		Items []resource `json:"items"`
		Total int        `json:"total"`
	}
	scoped := request(http.MethodGet, "/api/resources?folderId="+folder.ID+"&tag=Red", "")
	if err := json.Unmarshal(scoped.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if scoped.Code != http.StatusOK || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "a" || page.Items[0].FolderID != folder.ID {
		t.Fatalf("scoped resources: %d %s", scoped.Code, scoped.Body.String())
	}
	tags := request(http.MethodGet, "/api/tags?folderId="+folder.ID, "")
	var folderTags []map[string]any
	if err := json.Unmarshal(tags.Body.Bytes(), &folderTags); err != nil {
		t.Fatal(err)
	}
	if len(folderTags) != 2 || folderTags[0]["name"] == "Green" || folderTags[1]["name"] == "Green" {
		t.Fatalf("folder tags: %s", tags.Body.String())
	}
	unfiled := request(http.MethodGet, "/api/resources?folderId=unfiled", "")
	page = struct {
		Items []resource `json:"items"`
		Total int        `json:"total"`
	}{}
	if err := json.Unmarshal(unfiled.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].ID != "c" {
		t.Fatalf("unfiled: %s", unfiled.Body.String())
	}
	saved := request(http.MethodPost, "/api/collections", `{"name":"Projects Red","filters":{"folderId":"`+folder.ID+`","query":"","tags":["Red"],"kind":"","rootId":"","unorganized":false,"sort":"modified_desc"}}`)
	if saved.Code != http.StatusCreated {
		t.Fatalf("collection: %d %s", saved.Code, saved.Body.String())
	}
	if response := request(http.MethodDelete, "/api/folders/"+folder.ID, ""); response.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	unfiled = request(http.MethodGet, "/api/resources?folderId=unfiled", "")
	if err := json.Unmarshal(unfiled.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 {
		t.Fatalf("delete did not release resources: %s", unfiled.Body.String())
	}
	var collections []smartCollection
	if err := json.Unmarshal(request(http.MethodGet, "/api/collections", "").Body.Bytes(), &collections); err != nil {
		t.Fatal(err)
	}
	if len(collections) != 1 || collections[0].Filters.FolderID != "unfiled" {
		t.Fatalf("collection after delete: %+v", collections)
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/folders"}, {"POST", "/api/folders"}, {"PUT", "/api/folders/" + folder.ID},
		{"DELETE", "/api/folders/" + folder.ID}, {"PUT", "/api/resources/folder"},
	} {
		if !allowedRemoteRequest(tc.method, tc.path) {
			t.Fatalf("remote request blocked: %s %s", tc.method, tc.path)
		}
	}
}
