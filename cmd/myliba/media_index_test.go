package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMultipleMediaStoresUnifiedIndexAndCloudAssets(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Join(dir, "sources")
	appData := filepath.Join(dir, "appdata")
	secondStore := filepath.Join(dir, "second-store")
	for _, path := range []string{sourceRoot, appData, secondStore} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(sourceRoot, "clip.mp4")
	original := []byte("the original video bytes")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?)`, sourceRoot, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at,current_version_id)
		VALUES('clip','root',?,?,?,?,?,1,?,?,?)`, source, "clip.mp4", len(original), modified, "video", now(), now(), "revision-local"); err != nil {
		t.Fatal(err)
	}
	runtime := testCloudRuntime(t, filepath.Join(dir, "cloud"), "")
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, cloud: client, thumbnailDir: filepath.Join(appData, "thumbnails"), videoDir: filepath.Join(appData, "videos")}
	if err := app.ensureDefaultMediaStore(appData); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/media-stores", strings.NewReader(`{"path":`+jsonString(secondStore)+`}`))
	request.Host = "127.0.0.1:4317"
	request.Header.Set("Origin", "http://127.0.0.1:4317")
	request.Header.Set("X-Myliba-Local", "1")
	response := httptest.NewRecorder()
	app.addMediaStore(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("add store: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created store: %s %v", response.Body.String(), err)
	}
	request = httptest.NewRequest(http.MethodPut, "/api/media-stores/"+created.ID+"/default", strings.NewReader("{}"))
	request.Host = "127.0.0.1:4317"
	request.Header.Set("Origin", "http://127.0.0.1:4317")
	request.Header.Set("X-Myliba-Local", "1")
	request.SetPathValue("id", created.ID)
	response = httptest.NewRecorder()
	app.selectMediaStore(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("select store: %d %s", response.Code, response.Body.String())
	}
	thumbnailPath, _ := app.thumbnailCachePath("clip", info, thumbnailWidth, 0)
	if !pathWithin(thumbnailPath, secondStore) {
		t.Fatalf("new thumbnail is outside selected store: %s", thumbnailPath)
	}
	if err := os.MkdirAll(filepath.Dir(thumbnailPath), 0700); err != nil {
		t.Fatal(err)
	}
	thumbnail := []byte("jpeg thumbnail content")
	if err := os.WriteFile(thumbnailPath, thumbnail, 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.recordThumbnail("clip", info, thumbnailWidth, 0, thumbnailPath); err != nil {
		t.Fatal(err)
	}
	client.indexer = app
	if err := client.publishLocalIndex("clip"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.findRecord("clip"); err == nil {
		t.Fatal("a local-only index must not appear as cloud content")
	}
	remoteDB, err := sql.Open("sqlite3", filepath.Join(dir, "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteDB.Close() })
	if err := migrate(remoteDB); err != nil {
		t.Fatal(err)
	}
	remoteRuntime := testCloudRuntime(t, filepath.Join(dir, "remote-cloud"), "")
	remoteRuntime.db = remoteDB
	key, err := os.ReadFile(filepath.Join(runtime.disk.dir, "recovery.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRuntime.disk.dir, "recovery.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	remoteClient, err := newCloudClient(remoteRuntime)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := runtime.disk.list("catalog")
	if err != nil || len(ids) != 1 {
		t.Fatalf("local-only catalog: %v %v", ids, err)
	}
	const catalogToken = "local-index-test-token-0123456789"
	peer := serveCloudTestNode(t, runtime, catalogToken)
	if err := remoteRuntime.syncCatalog(context.Background(), peer.URL, catalogToken); err != nil {
		t.Fatal(err)
	}
	remoteApp := &server{db: remoteDB, cloud: remoteClient}
	remoteIndex, err := remoteApp.buildFileIndex("clip", true)
	if err != nil || len(remoteIndex.Original.Local) != 1 || remoteIndex.Original.Cloud != nil ||
		len(remoteIndex.Assets) != 1 || remoteIndex.ContentSize != int64(len(original)+len(thumbnail)) {
		t.Fatalf("synced local-only index: %+v %v", remoteIndex, err)
	}
	if err := client.uploadFile(context.Background(), "clip", source, true); err != nil {
		t.Fatal(err)
	}
	record, manifest, err := client.findRecord("clip")
	if err != nil || len(manifest.Assets) != 1 || manifest.ContentSize != int64(len(original)+len(thumbnail)) ||
		len(record.BlockIDs) != 2 || manifest.Size != int64(len(original)) {
		t.Fatalf("cloud index: %+v %+v %v", record, manifest, err)
	}
	firstOriginalBlock := record.BlockIDs[0]
	if err := os.Remove(thumbnailPath); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/cloud/files/clip/assets", nil)
	request.SetPathValue("id", "clip")
	response = httptest.NewRecorder()
	client.serveCloudPart(response, request, manifest.Assets[0].ID, "thumbnail.jpg")
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), thumbnail) {
		t.Fatalf("thumbnail cloud fallback: %d %q", response.Code, response.Body.String())
	}
	videoDir := filepath.Join(secondStore, "videos", cloudHash([]byte("360p variant")))
	if err := os.MkdirAll(videoDir, 0700); err != nil {
		t.Fatal(err)
	}
	video := videoManifest{Status: "ready", Codec: "avc1.42E029, mp4a.40.2", Duration: 4, Segments: []float64{4}}
	raw, _ := json.Marshal(video)
	for name, data := range map[string][]byte{"manifest.json": raw, "init.mp4": []byte("init"), "segment-00000.m4s": []byte("segment")} {
		if err := os.WriteFile(filepath.Join(videoDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.recordVideoAsset("clip", info, "360p", videoDir, video); err != nil {
		t.Fatal(err)
	}
	if err := client.syncNewAssets(context.Background(), "clip"); err != nil {
		t.Fatal(err)
	}
	updated, updatedManifest, err := client.findRecord("clip")
	if err != nil || len(updatedManifest.Assets) != 2 || updated.BlockIDs[0] != firstOriginalBlock ||
		updated.RevisionID != record.RevisionID || updatedManifest.ContentSize != int64(len(original)+len(thumbnail)+len("init")+len("segment")) {
		t.Fatalf("updated cloud index: %+v %+v %v", updated, updatedManifest, err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/cloud/files/clip/video/360p/segments/0", nil)
	request.SetPathValue("id", "clip")
	request.SetPathValue("quality", "360p")
	request.SetPathValue("segment", "0")
	response = httptest.NewRecorder()
	client.cloudVideoPart(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "segment" {
		t.Fatalf("video part: %d %q", response.Code, response.Body.String())
	}
	versions, err := client.historyRecords("clip")
	if err != nil || len(versions) != 1 {
		t.Fatalf("asset update must not create a source version: %d %v", len(versions), err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/resources/clip/index", nil)
	request.SetPathValue("id", "clip")
	response = httptest.NewRecorder()
	app.fileIndex(response, request)
	var index fileIndexView
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &index) != nil || len(index.Assets) != 2 ||
		index.ContentSize != updatedManifest.ContentSize || len(index.Original.Local) == 0 || index.Original.Cloud == nil {
		t.Fatalf("unified index: %d %s", response.Code, response.Body.String())
	}
	changed := []byte("a different original video")
	if err := os.WriteFile(source, changed, 0600); err != nil {
		t.Fatal(err)
	}
	changedInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE resources SET size=?,modified_at=? WHERE id='clip'`, len(changed),
		changedInfo.ModTime().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	currentIndex, err := app.buildFileIndex("clip", true)
	if err != nil || currentIndex.Original.Cloud != nil || len(currentIndex.Assets) != 0 || currentIndex.ContentSize != int64(len(changed)) {
		t.Fatalf("changed local revision must not inherit old cloud bytes: %+v %v", currentIndex, err)
	}
}

func jsonString(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
