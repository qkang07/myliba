package main

import (
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestThumbnailVariantsAndResourceDetail(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "sample.jpg")
	file, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 1600, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 1600; x++ {
			picture.Set(x, y, color.RGBA{R: 80, G: 120, B: 160, A: 255})
		}
	}
	if err := jpeg.Encode(file, picture, nil); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,'2026-01-01')`, dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES('item','root',?,'sample.jpg',100,'2026-01-01','image',1,'2026-01-01','2026-01-01')`, source)
	if err != nil {
		t.Fatal(err)
	}
	thumbDir := filepath.Join(dir, "thumbnails")
	if err := os.Mkdir(thumbDir, 0700); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, thumbnailDir: thumbDir, thumbnailLocks: map[string]*sync.Mutex{}}
	for _, tc := range []struct {
		size                string
		maxWidth, maxHeight int
	}{
		{"small", 240, 160}, {"medium", 480, 320}, {"large", 1280, 960},
	} {
		t.Run(tc.size, func(t *testing.T) {
			path := "/api/resources/item/thumbnail?size=" + tc.size
			post := httptest.NewRequest(http.MethodPost, path, nil)
			post.SetPathValue("id", "item")
			writer := httptest.NewRecorder()
			app.serveThumbnail(writer, post)
			if writer.Code != http.StatusNoContent {
				t.Fatalf("POST: %d %s", writer.Code, writer.Body.String())
			}
			get := httptest.NewRequest(http.MethodGet, path, nil)
			get.SetPathValue("id", "item")
			writer = httptest.NewRecorder()
			app.serveThumbnail(writer, get)
			if writer.Code != http.StatusOK {
				t.Fatalf("GET: %d %s", writer.Code, writer.Body.String())
			}
			config, _, err := image.DecodeConfig(writer.Body)
			if err != nil {
				t.Fatal(err)
			}
			if config.Width > tc.maxWidth || config.Height > tc.maxHeight || config.Width == 0 || config.Height == 0 {
				t.Fatalf("unexpected dimensions: %dx%d", config.Width, config.Height)
			}
		})
	}
	bad := httptest.NewRequest(http.MethodPost, "/api/resources/item/thumbnail?at=1", nil)
	bad.SetPathValue("id", "item")
	writer := httptest.NewRecorder()
	app.serveThumbnail(writer, bad)
	if writer.Code != http.StatusBadRequest {
		t.Fatalf("image time accepted: %d", writer.Code)
	}
	detail := httptest.NewRequest(http.MethodGet, "/api/resources/item", nil)
	detail.SetPathValue("id", "item")
	writer = httptest.NewRecorder()
	app.getResource(writer, detail)
	if writer.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", writer.Code, writer.Body.String())
	}
	if !allowedRemoteRequest(http.MethodGet, "/api/resources/item") || !allowedRemoteRequest(http.MethodGet, "/api/resources/item/video-info") ||
		!allowedRemoteRequest(http.MethodGet, "/api/resources/item/thumbnail?size=small") ||
		!allowedRemoteRequest(http.MethodPost, "/api/resources/item/thumbnail?size=small") {
		t.Fatal("remote detail routes rejected")
	}
}
