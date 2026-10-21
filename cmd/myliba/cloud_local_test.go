package main

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCloudDownloadPrefersMatchingLocalOriginalAndFallsBackForChangedFile(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE resources (id TEXT PRIMARY KEY, path TEXT, size INTEGER, modified_at TEXT, available INTEGER);
		CREATE TABLE file_assets (id TEXT PRIMARY KEY, resource_id TEXT, version_key TEXT, kind TEXT, spec TEXT, codec TEXT);
		CREATE TABLE file_asset_locations (asset_id TEXT, storage_id TEXT, path TEXT)`); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, filepath.Join(dir, "cloud"), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("original file bytes")
	path := filepath.Join(dir, "clip.bin")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO resources(id,path,size,modified_at,available) VALUES(?,?,?,?,1)`, "clip", path, len(original), modified); err != nil {
		t.Fatal(err)
	}
	if err := client.uploadFile(context.Background(), "clip", path, false); err != nil {
		t.Fatal(err)
	}
	record, _, err := client.findRecord("clip")
	if err != nil || len(record.BlockIDs) != 1 {
		t.Fatalf("cloud record: %+v %v", record, err)
	}
	blockPath, err := runtime.disk.path("blocks", record.BlockIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	block, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blockPath); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/cloud/files/clip/file", nil)
	request.SetPathValue("id", "clip")
	response := httptest.NewRecorder()
	client.download(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) {
		t.Fatalf("local original: status=%d body=%q", response.Code, response.Body.String())
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "/api/cloud/files/clip/file", nil)
	rangeRequest.SetPathValue("id", "clip")
	rangeRequest.Header.Set("Range", "bytes=0-7")
	rangeResponse := httptest.NewRecorder()
	client.download(rangeResponse, rangeRequest)
	if rangeResponse.Code != http.StatusPartialContent || !bytes.Equal(rangeResponse.Body.Bytes(), original[:8]) {
		t.Fatalf("local range: status=%d body=%q", rangeResponse.Code, rangeResponse.Body.String())
	}
	if err := os.WriteFile(blockPath, block, 0600); err != nil {
		t.Fatal(err)
	}
	// Keep the size and modification time unchanged. The hash must still detect
	// the new local bytes and select the immutable cloud revision.
	changed := []byte("different file data")
	if len(changed) != len(original) {
		t.Fatal("test fixture sizes differ")
	}
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	client.download(response, request)
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), original) {
		t.Fatalf("cloud fallback: status=%d body=%q", response.Code, response.Body.String())
	}
}
