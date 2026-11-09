package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatFourAuthorRejectsLaterLegacyIndex(t *testing.T) {
	runtime := testCloudRuntime(t, t.TempDir(), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		format                     int
		revision, at, name, author string
	}{
		{3, "v1", "2026-11-01T00:00:00Z", "earlier.txt", "node-a"},
		{4, "v2", "2026-11-02T00:00:00Z", "current.txt", "node-a"},
		{3, "v3", "2026-11-03T00:00:00Z", "stale.txt", "node-a"},
		{3, "v1", "2026-11-03T00:00:00Z", "other.txt", "node-b"},
	} {
		fileID := "file-a"
		if item.author == "node-b" {
			fileID = "file-b"
		}
		index := fileIndexView{FileID: fileID, RevisionID: item.revision, Name: item.name, Modified: item.at, Original: indexedContent{Size: 0}, OrganizationKnown: true}
		record := cloudRecord{Format: item.format, LibraryID: client.libraryID(), ObjectID: fileID, RevisionID: item.revision, AuthorID: item.author, CreatedAt: item.at, BlockIDs: []string{}}
		record, err = client.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		id := cloudHash(raw)
		if item.format == 3 && item.revision == "v3" {
			// Simulate a record accepted by an older, unsynchronized storage node.
			// The reader must isolate it even though this node's writer gate rejects it.
			path, pathErr := runtime.disk.path("catalog", id)
			if pathErr != nil {
				t.Fatal(pathErr)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
		} else if err := runtime.disk.put("catalog", id, strings.NewReader(string(raw)), cloudMaxRecordSize); err != nil {
			t.Fatal(err)
		}
	}
	records, err := client.validRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("downgraded record was accepted: %+v", records)
	}
	files, err := client.mergeCatalogSearch(nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("unexpected library projection: %+v", files)
	}
	for _, file := range files {
		if file.FileID == "file-a" && file.Name != "current.txt" {
			t.Fatalf("legacy downgrade replaced format-4 index: %+v", file)
		}
	}
}
