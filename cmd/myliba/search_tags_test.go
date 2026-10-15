package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestListResourcesMultipleTags(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "tags.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO roots(id,path,added_at) VALUES('root','/library','2026-01-01')`,
		`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES
			('both','root','/library/shared.jpg','shared.jpg',1,'2026-01-04','image',1,'2026-01-04','2026-01-04'),
			('alpha','root','/library/alpha.jpg','alpha.jpg',1,'2026-01-03','image',1,'2026-01-03','2026-01-03'),
			('beta','root','/library/beta.jpg','beta.jpg',1,'2026-01-02','image',1,'2026-01-02','2026-01-02'),
			('none','root','/library/none.jpg','none.jpg',1,'2026-01-01','image',1,'2026-01-01','2026-01-01')`,
		`INSERT INTO tags(id,name,created_at) VALUES('a','Alpha','2026-01-01'),('b','Beta','2026-01-01')`,
		`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES
			('both','a','2026-01-01'),('both','b','2026-01-01'),
			('alpha','a','2026-01-01'),('beta','b','2026-01-01')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	app := &server{db: db}
	for _, tc := range []struct {
		name, query string
		total       int
		ids         []string
	}{
		{"single tag", "tag=Alpha", 2, []string{"both", "alpha"}},
		{"two tags intersect", "tag=Alpha&tag=Beta", 1, []string{"both"}},
		{"reverse order", "tag=Beta&tag=Alpha", 1, []string{"both"}},
		{"missing tag", "tag=Alpha&tag=Missing", 0, []string{}},
		{"keyword with tags", "q=shared&tag=Alpha&tag=Beta", 1, []string{"both"}},
		{"pagination count", "tag=Alpha&limit=1", 2, []string{"both"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/resources?"+tc.query, nil)
			recorder := httptest.NewRecorder()
			app.listResources(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
			}
			var page struct {
				Items []resource `json:"items"`
				Total int        `json:"total"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page.Total != tc.total || len(page.Items) != len(tc.ids) {
				t.Fatalf("got total=%d ids=%v, want total=%d ids=%v", page.Total, resourceIDs(page.Items), tc.total, tc.ids)
			}
			for i, id := range tc.ids {
				if page.Items[i].ID != id {
					t.Fatalf("got ids=%v, want %v", resourceIDs(page.Items), tc.ids)
				}
			}
		})
	}
}

func resourceIDs(items []resource) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}
