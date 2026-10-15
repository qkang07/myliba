package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAttributeTagsAndCombinedFilters(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "attributes.db"))
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
		`INSERT INTO folders(id,name,created_at) VALUES('folder','Work','2026-01-01')`,
		`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES
            ('a','root','/library/a.jpg','a.jpg',500000,'2026-01-04','image',1,'2026-01-04','2026-01-04'),
            ('b','root','/library/b.png','b.png',2097152,'2026-01-03','image',1,'2026-01-03','2026-01-03'),
            ('c','root','/library/c.pdf','c.pdf',20971520,'2026-01-02','document',1,'2026-01-02','2026-01-02'),
            ('d','root','/library/d.jpg','d.jpg',209715200,'2026-01-01','image',1,'2026-01-01','2026-01-01')`,
		`INSERT INTO resource_folders(resource_id,folder_id) VALUES('a','folder'),('b','folder'),('c','folder')`,
		`INSERT INTO tags(id,name,created_at) VALUES('red','Red','2026-01-01')`,
		`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES('a','red','2026-01-01'),('c','red','2026-01-01')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	app := &server{db: db}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/resources", app.listResources)
	mux.HandleFunc("GET /api/resources/selection", app.listResourceSelection)
	mux.HandleFunc("GET /api/attribute-tags", app.attributeTags)
	mux.HandleFunc("POST /api/collections", app.createCollection)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
		return response
	}
	catalog := request(http.MethodGet, "/api/attribute-tags?folderId=folder", "")
	var attributes []attributeTag
	if err := json.Unmarshal(catalog.Body.Bytes(), &attributes); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, item := range attributes {
		counts[item.Key] = item.Count
	}
	if catalog.Code != http.StatusOK || counts["type:image"] != 2 || counts["extension:jpg"] != 1 || counts["size:10to100m"] != 1 || counts["size:gte100m"] != 0 {
		t.Fatalf("folder attribute catalog: %d %s", catalog.Code, catalog.Body.String())
	}
	for _, tc := range []struct {
		name, query string
		want        []string
	}{
		{"same category OR and cross category AND", "folderId=folder&attribute=type:image&attribute=type:document&attribute=size:lt1m&attribute=size:10to100m", []string{"a", "c"}},
		{"type extension size and manual tag", "folderId=folder&attribute=type:image&attribute=extension:jpg&attribute=size:lt1m&tag=Red", []string{"a"}},
		{"unfiled scope", "folderId=unfiled&attribute=extension:jpg", []string{"d"}},
		{"legacy kind", "folderId=folder&kind=document", []string{"c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(http.MethodGet, "/api/resources?"+tc.query, "")
			var page struct {
				Items []resource `json:"items"`
				Total int        `json:"total"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || len(page.Items) != len(tc.want) || page.Total != len(tc.want) {
				t.Fatalf("filter: %d %s", response.Code, response.Body.String())
			}
			for i, id := range tc.want {
				if page.Items[i].ID != id || len(page.Items[i].AttributeTags) < 2 {
					t.Fatalf("resource %d: %+v", i, page.Items[i])
				}
			}
		})
	}
	selectionQuery := "folderId=folder&attribute=type:image&attribute=type:document&attribute=size:lt1m&attribute=size:10to100m"
	for pageNumber, expectedID := range []string{"a", "c"} {
		response := request(http.MethodGet, "/api/resources/selection?"+selectionQuery+"&limit=1&offset="+strconv.Itoa(pageNumber), "")
		var selection struct {
			Items []struct {
				ID        string `json:"id"`
				Kind      string `json:"kind"`
				Available bool   `json:"available"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &selection); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || selection.Total != 2 || len(selection.Items) != 1 || selection.Items[0].ID != expectedID || !selection.Items[0].Available {
			t.Fatalf("selection page %d: %d %s", pageNumber, response.Code, response.Body.String())
		}
	}
	response := request(http.MethodGet, "/api/resources/selection?folderId=folder&attribute=type:image&attribute=extension:jpg&attribute=size:lt1m&tag=Red&q=a.jpg", "")
	var selected struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &selected); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || selected.Total != 1 || len(selected.Items) != 1 || selected.Items[0].ID != "a" {
		t.Fatalf("combined selection: %d %s", response.Code, response.Body.String())
	}
	if response := request(http.MethodGet, "/api/resources/selection?limit=1001", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selection limit: %d %s", response.Code, response.Body.String())
	}
	if !allowedRemoteRequest(http.MethodGet, "/api/resources/selection?folderId=folder") || allowedRemoteRequest(http.MethodPost, "/api/resources/selection") {
		t.Fatal("remote selection route permissions are incorrect")
	}
	if response := request(http.MethodGet, "/api/resources?attribute=extension:%25", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid attribute: %d %s", response.Code, response.Body.String())
	}
	saved := request(http.MethodPost, "/api/collections", `{"name":"Small images","filters":{"query":"","tags":[],"attributes":["type:image","size:lt1m","TYPE:image"],"kind":"","rootId":"","folderId":"folder","unorganized":false,"sort":"modified_desc"}}`)
	var collection smartCollection
	if err := json.Unmarshal(saved.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	if saved.Code != http.StatusCreated || len(collection.Filters.Attributes) != 2 {
		t.Fatalf("collection attributes: %d %s", saved.Code, saved.Body.String())
	}
	if !allowedRemoteRequest(http.MethodGet, "/api/attribute-tags?folderId=folder") {
		t.Fatal("remote attribute catalog blocked")
	}
}

func TestResourceSizeBucketBoundaries(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want string
	}{
		{mebibyte - 1, "lt1m"}, {mebibyte, "1to10m"},
		{10*mebibyte - 1, "1to10m"}, {10 * mebibyte, "10to100m"},
		{100*mebibyte - 1, "10to100m"}, {100 * mebibyte, "gte100m"},
	} {
		if got := resourceSizeBucket(tc.size); got != tc.want {
			t.Errorf("size %d: got %s, want %s", tc.size, got, tc.want)
		}
	}
}
