package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func fileRenameEnvelope(id, author string, counter int64, name string, context map[string]int64, resolve bool) libraryOperationEnvelope {
	raw, _ := json.Marshal(map[string]any{"fileIds": []string{"logical-file"}, "fileName": name, "context": context, "resolve": resolve})
	return libraryOperationEnvelope{OpID: id, AuthorID: author, Counter: counter, Kind: "file.rename", Payload: raw}
}

func TestFileRenameConflictResolutionSuppressesLateObservedOperation(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	applyRename(t, db, fileRenameEnvelope("a1", "a", 1, "one.jpg", nil, false))
	applyRename(t, db, fileRenameEnvelope("b1", "b", 1, "two.jpg", nil, false))
	var name string
	var conflicts int
	if err = db.QueryRow(`SELECT current_name FROM library_file_name_state WHERE file_id='logical-file'`).Scan(&name); err != nil || name != "one.jpg" {
		t.Fatalf("initial name %q: %v", name, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM library_file_name_conflicts WHERE file_id='logical-file'`).Scan(&conflicts); err != nil || conflicts != 1 {
		t.Fatalf("conflicts %d: %v", conflicts, err)
	}
	applyRename(t, db, fileRenameEnvelope("a2", "a", 2, "final.jpg", map[string]int64{"a": 1, "b": 1}, true))
	// The resolved operation has observed b1, which may arrive later on a
	// different replica. It cannot resurrect a conflict or change the name.
	applyRename(t, db, fileRenameEnvelope("b1", "b", 1, "two.jpg", nil, false))
	if err = db.QueryRow(`SELECT current_name FROM library_file_name_state WHERE file_id='logical-file'`).Scan(&name); err != nil || name != "final.jpg" {
		t.Fatalf("resolved name %q: %v", name, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM library_file_name_conflicts WHERE file_id='logical-file'`).Scan(&conflicts); err != nil || conflicts != 0 {
		t.Fatalf("resolved conflicts %d: %v", conflicts, err)
	}
}

func TestConcurrentCurrentRevisionSelectionRetainsCandidate(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	makeSelection := func(id, author, revision string, counter int64, context map[string]int64, resolve bool) libraryOperationEnvelope {
		raw, _ := json.Marshal(map[string]any{"fileIds": []string{"logical-file"}, "revisionId": revision, "context": context, "resolve": resolve})
		return libraryOperationEnvelope{OpID: id, AuthorID: author, Counter: counter, Kind: "file.version.select", Payload: raw}
	}
	applyRename(t, db, makeSelection("a1", "a", "revision-one", 1, nil, false))
	applyRename(t, db, makeSelection("b1", "b", "revision-two", 1, nil, false))
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM library_file_revision_conflicts WHERE file_id='logical-file'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("version conflicts = %d: %v", count, err)
	}
	applyRename(t, db, makeSelection("a2", "a", "revision-two", 2, map[string]int64{"a": 1, "b": 1}, true))
	applyRename(t, db, makeSelection("b1", "b", "revision-two", 1, nil, false))
	var selected string
	if err = db.QueryRow(`SELECT revision_id FROM library_file_current_revision WHERE file_id='logical-file'`).Scan(&selected); err != nil || selected != "revision-two" {
		t.Fatalf("selected revision = %q: %v", selected, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM library_file_revision_conflicts WHERE file_id='logical-file'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("resolved version conflicts = %d: %v", count, err)
	}
}

func TestConcurrentFileChoicesConvergeAcrossDeliveryOrder(t *testing.T) {
	for _, kind := range []string{"file.rename", "file.version.select"} {
		var winners []string
		for _, order := range []string{"ab", "ba"} {
			db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			if err = migrate(db); err != nil {
				t.Fatal(err)
			}
			for _, author := range order {
				payload := map[string]any{"fileIds": []string{"logical-file"}, "context": map[string]int64{}}
				if kind == "file.rename" {
					payload["fileName"] = string(author) + ".jpg"
				} else {
					payload["revisionId"] = string(author) + "-revision"
				}
				raw, _ := json.Marshal(payload)
				applyRename(t, db, libraryOperationEnvelope{OpID: string(author) + "1", AuthorID: string(author), Counter: 1, Kind: kind, Payload: raw})
			}
			var winner string
			table, column := "library_file_name_state", "current_name"
			if kind == "file.version.select" {
				table, column = "library_file_current_revision", "revision_id"
			}
			if err = db.QueryRow(`SELECT ` + column + ` FROM ` + table + ` WHERE file_id='logical-file'`).Scan(&winner); err != nil {
				t.Fatal(err)
			}
			winners = append(winners, winner)
			db.Close()
		}
		if winners[0] != winners[1] {
			t.Fatalf("%s differs by delivery order: %v", kind, winners)
		}
	}
}
