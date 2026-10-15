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

func TestSmartCollectionCRUD(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "collections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root','/library','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/collections", app.listCollections)
	mux.HandleFunc("POST /api/collections", app.createCollection)
	mux.HandleFunc("PUT /api/collections/{id}", app.updateCollection)
	mux.HandleFunc("DELETE /api/collections/{id}", app.deleteCollection)

	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	body := `{"name":"Work","filters":{"query":"report","tags":["Important","Important"],"kind":"document","rootId":"root","unorganized":false,"sort":"name_asc"}}`
	created := request(http.MethodPost, "/api/collections", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var collection smartCollection
	if err := json.Unmarshal(created.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if collection.ID == "" || collection.Name != "Work" || len(collection.Filters.Tags) != 1 || collection.Filters.Sort != "name_asc" {
		t.Fatalf("unexpected created collection: %+v", collection)
	}
	listed := request(http.MethodGet, "/api/collections", "")
	var collections []smartCollection
	if err := json.Unmarshal(listed.Body.Bytes(), &collections); err != nil || len(collections) != 1 || collections[0].ID != collection.ID {
		t.Fatalf("list: %d %s: %v", listed.Code, listed.Body.String(), err)
	}
	if response := request(http.MethodPost, "/api/collections", body); response.Code != http.StatusConflict {
		t.Fatalf("duplicate name: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodPost, "/api/collections", `{"name":"Bad","filters":{"kind":"not-a-kind"}}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid kind: %d %s", response.Code, response.Body.String())
	}
	updated := request(http.MethodPut, "/api/collections/"+collection.ID, `{"name":"Projects","filters":{"query":"","tags":[],"kind":"image","rootId":"","unorganized":false,"sort":"size_desc"}}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d %s", updated.Code, updated.Body.String())
	}
	if response := request(http.MethodDelete, "/api/collections/"+collection.ID, ""); response.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/collections", ""); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("list after delete: %d %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if !allowedRemoteRequest(method, "/api/collections") {
			t.Fatalf("remote collection request blocked: %s", method)
		}
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		if !allowedRemoteRequest(method, "/api/collections/"+collection.ID) {
			t.Fatalf("remote collection item request blocked: %s", method)
		}
	}
}

func TestListResourcesKindRootAndSort(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "filters.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO roots(id,path,added_at) VALUES('one','/one','2026-01-01'),('two','/two','2026-01-01')`,
		`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES
		('small','one','/one/a.jpg','A.jpg',1,'2026-01-01','image',1,'2026-01-01','2026-01-01'),
		('large','one','/one/b.jpg','B.jpg',10,'2026-01-02','image',1,'2026-01-02','2026-01-02'),
		('other','two','/two/c.jpg','C.jpg',20,'2026-01-03','image',1,'2026-01-03','2026-01-03'),
		('document','one','/one/d.pdf','D.pdf',30,'2026-01-04','document',1,'2026-01-04','2026-01-04')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	app := &server{db: db}
	response := httptest.NewRecorder()
	app.listResources(response, httptest.NewRequest(http.MethodGet, "/api/resources?kind=image&rootId=one&sort=size_desc", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	var page struct {
		Items []resource `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != "large" || page.Items[1].ID != "small" {
		t.Fatalf("unexpected filtered order: %+v", page)
	}
	response = httptest.NewRecorder()
	app.listResources(response, httptest.NewRequest(http.MethodGet, "/api/resources?sort=random", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid sort: %d %s", response.Code, response.Body.String())
	}
}
