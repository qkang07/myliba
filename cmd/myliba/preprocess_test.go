package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestScanQueuesThumbnailsAndVisibleItemsTakePriority(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jpg", "b.jpg"} {
		f, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 20, 20))
		img.Set(0, 0, color.RGBA{R: 200, A: 255})
		if err := jpeg.Encode(f, img, nil); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,'2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.Mkdir(thumbDir, 0700); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, thumbnailDir: thumbDir, thumbnailLocks: map[string]*sync.Mutex{}}
	scan := httptest.NewRequest(http.MethodPost, "/api/roots/root/scan", nil)
	scan.SetPathValue("id", "root")
	response := httptest.NewRecorder()
	app.scanRoot(response, scan)
	if response.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", response.Code, response.Body.String())
	}

	ids := map[string]string{}
	rows, err := db.Query(`SELECT r.name,r.id FROM resources r JOIN preprocess_jobs j ON j.resource_id=r.id WHERE j.status='pending'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("queued jobs: %v", ids)
	}

	body, _ := json.Marshal(map[string]any{"ids": []string{ids["b.jpg"], ids["a.jpg"]}})
	response = httptest.NewRecorder()
	app.prioritizePreprocessJobs(response, httptest.NewRequest(http.MethodPost, "/api/preprocess/prioritize", bytes.NewReader(body)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("prioritize: %d %s", response.Code, response.Body.String())
	}
	first, operation, _, err := app.claimPreprocessJob()
	if err != nil || first != ids["b.jpg"] || operation != thumbnailOperation {
		t.Fatalf("first job = %s, %v", first, err)
	}

	preview := httptest.NewRequest(http.MethodGet, "/api/resources/"+first+"/file", nil)
	preview.SetPathValue("id", first)
	response = httptest.NewRecorder()
	app.serveFile(response, preview)
	if response.Code != http.StatusOK || response.Body.Len() == 0 {
		t.Fatalf("original preview before thumbnail: %d", response.Code)
	}

	info, err := os.Stat(filepath.Join(root, "b.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ensureThumbnail(preview.Context(), first, filepath.Join(root, "b.jpg"), "image", info, thumbnailWidth, thumbnailHeight, 0); err != nil {
		t.Fatal(err)
	}
	thumbnail := httptest.NewRequest(http.MethodGet, "/api/resources/"+first+"/thumbnail", nil)
	thumbnail.SetPathValue("id", first)
	response = httptest.NewRecorder()
	app.serveThumbnail(response, thumbnail)
	if response.Code != http.StatusOK {
		t.Fatalf("generated thumbnail: %d %s", response.Code, response.Body.String())
	}
}

func TestPreprocessRemoteRoutesAreScoped(t *testing.T) {
	for _, tc := range []struct {
		method, uri string
		allowed     bool
	}{
		{http.MethodGet, "/api/preprocess", true},
		{http.MethodPost, "/api/preprocess/prioritize", true},
		{http.MethodPost, "/api/preprocess/retry-failed", true},
		{http.MethodGet, "/api/preprocess/retry-failed", false},
		{http.MethodPost, "/api/preprocess/item/retry", true},
		{http.MethodDelete, "/api/preprocess/item/retry", false},
		{http.MethodGet, "/api/preprocess/item/retry", false},
	} {
		if got := allowedRemoteRequest(tc.method, tc.uri); got != tc.allowed {
			t.Fatalf("%s %s: allowed=%v", tc.method, tc.uri, got)
		}
	}
}

func TestPreprocessWorkerCompletesQueuedThumbnail(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "item.jpg")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 24, 24)), nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,'2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.Mkdir(thumbDir, 0700); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, thumbnailDir: thumbDir, thumbnailLocks: map[string]*sync.Mutex{}, jobsWake: make(chan struct{}, 1)}
	if err := app.startPreprocessing(); err != nil {
		t.Fatal(err)
	}
	defer close(app.jobsStop)
	scan := httptest.NewRequest(http.MethodPost, "/api/roots/root/scan", nil)
	scan.SetPathValue("id", "root")
	response := httptest.NewRecorder()
	app.scanRoot(response, scan)
	if response.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var id, status string
		err := db.QueryRow(`SELECT resource_id,status FROM preprocess_jobs LIMIT 1`).Scan(&id, &status)
		if err == nil && status == "done" {
			thumbnail := httptest.NewRequest(http.MethodGet, "/api/resources/"+id+"/thumbnail", nil)
			thumbnail.SetPathValue("id", id)
			response = httptest.NewRecorder()
			app.serveThumbnail(response, thumbnail)
			if response.Code != http.StatusOK {
				t.Fatalf("worker thumbnail: %d", response.Code)
			}
			mobile := httptest.NewRequest(http.MethodGet, "/api/resources/"+id+"/thumbnail?size=mobile", nil)
			mobile.SetPathValue("id", id)
			response = httptest.NewRecorder()
			app.serveThumbnail(response, mobile)
			if response.Code != http.StatusOK {
				t.Fatalf("worker mobile thumbnail: %d", response.Code)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background thumbnail did not complete")
}

func TestOtherFilesReceiveFingerprintTask(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "note.txt")
	if err := os.WriteFile(source, []byte("library document"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,'2026-01-01')`, root); err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.Mkdir(thumbDir, 0700); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, thumbnailDir: thumbDir, thumbnailLocks: map[string]*sync.Mutex{}, jobsWake: make(chan struct{}, 1)}
	if err := app.startPreprocessing(); err != nil {
		t.Fatal(err)
	}
	defer close(app.jobsStop)
	scan := httptest.NewRequest(http.MethodPost, "/api/roots/root/scan", nil)
	scan.SetPathValue("id", "root")
	response := httptest.NewRecorder()
	app.scanRoot(response, scan)
	if response.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var operation, status, contentID string
		err := db.QueryRow(`SELECT j.operation,j.status,v.content_id FROM preprocess_jobs j
            JOIN resources r ON r.id=j.resource_id JOIN versions v ON v.id=r.current_version_id LIMIT 1`).Scan(&operation, &status, &contentID)
		if err == nil && status == "done" {
			expected := sha256.Sum256([]byte("library document"))
			if operation != fingerprintOperation || contentID != hex.EncodeToString(expected[:]) {
				t.Fatalf("operation=%s contentID=%s", operation, contentID)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background fingerprint did not complete")
}

func TestScanCanSkipAutomaticPreprocessingAndRetryFailedJobs(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "library")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		if err := os.WriteFile(filepath.Join(root, "note-"+strconv.Itoa(i)+".txt"), []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, jobsWake: make(chan struct{}, 1)}
	body, _ := json.Marshal(map[string]any{"path": root, "autoPreprocess": false})
	response := httptest.NewRecorder()
	app.addRoot(response, httptest.NewRequest(http.MethodPut, "/api/roots", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("add root: %d %s", response.Code, response.Body.String())
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	scan := httptest.NewRequest(http.MethodPost, "/api/roots/"+added.ID+"/scan", nil)
	scan.SetPathValue("id", added.ID)
	response = httptest.NewRecorder()
	app.scanRoot(response, scan)
	if response.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", response.Code, response.Body.String())
	}
	var resourceID string
	if err := db.QueryRow(`SELECT id FROM resources WHERE root_id=?`, added.ID).Scan(&resourceID); err != nil {
		t.Fatal(err)
	}
	app.backfillPreprocessing()
	priorityBody, _ := json.Marshal(map[string]any{"ids": []string{resourceID}})
	response = httptest.NewRecorder()
	app.prioritizePreprocessJobs(response, httptest.NewRequest(http.MethodPost, "/api/preprocess/prioritize", bytes.NewReader(priorityBody)))
	if response.Code != http.StatusNoContent {
		t.Fatalf("prioritize: %d %s", response.Code, response.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM preprocess_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled root queued %d jobs: %v", count, err)
	}
	rows, err := db.Query(`SELECT id FROM resources WHERE root_id=?`, added.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := db.Exec(`INSERT INTO preprocess_jobs(resource_id,operation,version_key,status,created_at,updated_at,error)
            VALUES(?, 'fingerprint', '1', 'failed', '2026-01-01', '2026-01-01', 'previous error')`, id); err != nil {
			t.Fatal(err)
		}
	}
	response = httptest.NewRecorder()
	app.retryFailedPreprocessJobs(response, httptest.NewRequest(http.MethodPost, "/api/preprocess/retry-failed", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"retried":105`)) {
		t.Fatalf("bulk retry: %d %s", response.Code, response.Body.String())
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM preprocess_jobs WHERE status='pending' AND error=''`).Scan(&pending); err != nil || pending != 105 {
		t.Fatalf("retried jobs: %d, %v", pending, err)
	}
	var status, jobError string
	if err := db.QueryRow(`SELECT status,error FROM preprocess_jobs WHERE resource_id=?`, resourceID).Scan(&status, &jobError); err != nil || status != "pending" || jobError != "" {
		t.Fatalf("retried job: status=%s error=%s query=%v", status, jobError, err)
	}
}

func TestMigratePreservesExistingRootsPreprocessDefault(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE roots (id TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE, added_at TEXT NOT NULL, last_scan_at TEXT);
        INSERT INTO roots(id,path,added_at) VALUES('old','C:/old','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := db.QueryRow(`SELECT auto_preprocess FROM roots WHERE id='old'`).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("legacy root auto preprocess = %v, %v", enabled, err)
	}
}
