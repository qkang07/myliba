package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestUnifiedLibraryBaselineAndLegacyClassificationProjection(t *testing.T) {
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
	for _, statement := range []string{
		`INSERT INTO roots(id,path,added_at) VALUES('root','C:/photos','2026-01-01T00:00:00Z')`,
		`INSERT INTO resources(id,root_id,path,file_id,current_version_id,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES('legacy','root','C:/photos/a.jpg','stable-file','revision','a.jpg',42,'2026-01-01T00:00:00Z','image',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO versions(id,resource_id,size,modified_at,discovered_at) VALUES('revision','legacy',42,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO folders(id,name,created_at) VALUES('travel','Travel','2026-01-01T00:00:00Z')`,
		`INSERT INTO tags(id,name,created_at) VALUES('family','Family','2026-01-01T00:00:00Z')`,
		`INSERT INTO resource_folders(resource_id,folder_id) VALUES('legacy','travel')`,
		`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES('legacy','family','2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// The migration is intentionally safe to rerun against an existing desktop DB.
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var fileID, locationRef, state, folderID, tagID string
	if err := db.QueryRow(`SELECT file_id FROM library_resource_aliases WHERE resource_id='legacy'`).Scan(&fileID); err != nil || fileID != "stable-file" {
		t.Fatalf("alias = %q, %v", fileID, err)
	}
	if err := db.QueryRow(`SELECT resource_ref,state FROM library_locations WHERE resource_id='legacy'`).Scan(&locationRef, &state); err != nil || locationRef != "C:/photos/a.jpg" || state != "available" {
		t.Fatalf("location = %q/%q, %v", locationRef, state, err)
	}
	if err := db.QueryRow(`SELECT folder_id FROM library_file_folders WHERE file_id='stable-file'`).Scan(&folderID); err != nil || folderID != "travel" {
		t.Fatalf("folder = %q, %v", folderID, err)
	}
	if err := db.QueryRow(`SELECT tag_id FROM library_file_tags WHERE file_id='stable-file'`).Scan(&tagID); err != nil || tagID != "family" {
		t.Fatalf("tag = %q, %v", tagID, err)
	}
	if _, err := db.Exec(`DELETE FROM resource_tags WHERE resource_id='legacy' AND tag_id='family'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT tag_id FROM library_file_tags WHERE file_id='stable-file'`).Scan(&tagID); err != sql.ErrNoRows {
		t.Fatalf("removed legacy tag stayed projected: %q, %v", tagID, err)
	}
	app := &server{db: db}
	if err := app.syncUnifiedResource("legacy"); err != nil {
		t.Fatal(err)
	}
}

func TestTransferredFileKeepsLogicalIdentityAndInitialClassification(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(root, "library.db"))
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
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?)`, root, now()); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first.txt")
	if err := os.WriteFile(first, []byte("copy"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db}
	if err := app.registerTransferredFile(first, "logical-file", "logical-revision", "Travel", []string{"Family", "2026"}); err != nil {
		t.Fatal(err)
	}
	var fileID, revisionID, folderName string
	var tagCount, locationCount int
	if err := db.QueryRow(`SELECT a.file_id,l.revision_id FROM library_resource_aliases a JOIN library_locations l ON l.resource_id=a.resource_id WHERE l.resource_ref=?`, first).Scan(&fileID, &revisionID); err != nil || fileID != "logical-file" || revisionID != "logical-revision" {
		t.Fatalf("transferred identity = %q/%q, %v", fileID, revisionID, err)
	}
	if err := db.QueryRow(`SELECT f.name FROM library_file_folders lf JOIN folders f ON f.id=lf.folder_id WHERE lf.file_id='logical-file'`).Scan(&folderName); err != nil || folderName != "Travel" {
		t.Fatalf("transferred folder = %q, %v", folderName, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_file_tags WHERE file_id='logical-file'`).Scan(&tagCount); err != nil || tagCount != 2 {
		t.Fatalf("transferred tags = %d, %v", tagCount, err)
	}
	second := filepath.Join(root, "second.txt")
	if err := os.WriteFile(second, []byte("copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.registerTransferredFile(second, "logical-file", "logical-revision", "Should not replace", []string{"Should not replace"}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM library_locations WHERE file_id='logical-file' AND revision_id='logical-revision'`).Scan(&locationCount); err != nil || locationCount != 2 {
		t.Fatalf("transferred locations = %d, %v", locationCount, err)
	}
	if err := db.QueryRow(`SELECT f.name FROM library_file_folders lf JOIN folders f ON f.id=lf.folder_id WHERE lf.file_id='logical-file'`).Scan(&folderName); err != nil || folderName != "Travel" {
		t.Fatalf("existing classification was overwritten: %q, %v", folderName, err)
	}
}
