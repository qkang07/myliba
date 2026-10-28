package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed web/dist
var assets embed.FS

type server struct {
	db             *sql.DB
	cloud          *cloudClient
	thumbnailDir   string
	videoDir       string
	ffmpegPath     string
	ffprobePath    string
	thumbnailMu    sync.Mutex
	thumbnailLocks map[string]*sync.Mutex
	videoMu        sync.Mutex
	videoJobs      map[string]*videoJob
	videoSlots     chan struct{}
	jobsWake       chan struct{}
	jobsStop       chan struct{}
	folderAccessMu sync.Mutex
	folderAccess   map[string]folderAccessSession
}

type resource struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Path          string         `json:"path"`
	Size          int64          `json:"size"`
	ContentSize   int64          `json:"contentSize"`
	Modified      string         `json:"modified"`
	Kind          string         `json:"kind"`
	Available     bool           `json:"available"`
	VersionID     string         `json:"versionId"`
	FolderID      string         `json:"folderId"`
	Tags          []string       `json:"tags"`
	AttributeTags []attributeTag `json:"attributeTags"`
}

func main() {
	if os.Getenv("MYLIBA_MODE") == "hub" {
		if err := runHub(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if os.Getenv("MYLIBA_MODE") == "storage" {
		if err := runCloudStorage(); err != nil {
			log.Fatal(err)
		}
		return
	}
	dataDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal(err)
	}
	dataDir = filepath.Join(dataDir, "myliba")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dataDir, "library.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		log.Fatal(err)
	}
	if err := migrate(db); err != nil {
		log.Fatal(err)
	}
	thumbnailDir := filepath.Join(dataDir, "thumbnails")
	if err := os.MkdirAll(thumbnailDir, 0o700); err != nil {
		log.Fatal(err)
	}
	videoDir := filepath.Join(dataDir, "videos")
	if err := os.MkdirAll(videoDir, 0o700); err != nil {
		log.Fatal(err)
	}
	ffmpegPath := mediaToolPath("ffmpeg")
	ffprobePath := mediaToolPath("ffprobe")
	app := &server{db: db, thumbnailDir: thumbnailDir, videoDir: videoDir, ffmpegPath: ffmpegPath, ffprobePath: ffprobePath, thumbnailLocks: map[string]*sync.Mutex{}, videoJobs: map[string]*videoJob{}, videoSlots: make(chan struct{}, 1), jobsWake: make(chan struct{}, 1)}
	iceServers, err := loadICEServers()
	if err != nil {
		log.Fatal(err)
	}
	hubs, err := newHubProfiles(db, iceServers)
	if err != nil {
		log.Fatal(err)
	}
	shares := &shareService{db: db, hubs: hubs, app: app}
	cloudRuntime, err := newCloudRuntime(db, false)
	if err != nil {
		log.Fatal(err)
	}
	cloud, err := newCloudClient(cloudRuntime)
	if err != nil {
		log.Fatal(err)
	}
	app.cloud = cloud
	cloud.indexer = app
	if err := app.ensureDefaultMediaStore(dataDir); err != nil {
		log.Fatal(err)
	}
	if err := cloud.primeLocalDigests(); err != nil {
		log.Printf("local index startup: %v", err)
	}
	go cloudRuntime.run(context.Background())
	go cloud.assetSyncWorker(context.Background())
	if cloudRuntime.endpoint != "" && !strings.HasPrefix(cloudRuntime.endpoint, "p2p://") {
		go func() {
			if err := cloudRuntime.serveDirect(); err != nil {
				log.Fatal(err)
			}
		}()
	}
	if err := app.startPreprocessing(); err != nil {
		log.Fatal(err)
	}
	go app.backfillMediaAssets()
	go app.queueAllLocalIndexes()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", app.health)
	mux.HandleFunc("GET /api/cloud/files", cloud.list)
	mux.HandleFunc("GET /api/cloud/files/{id}/file", cloud.download)
	mux.HandleFunc("GET /api/cloud/files/{id}/assets/{asset}/parts/{part}", cloud.cloudAssetPart)
	mux.HandleFunc("GET /api/cloud/files/{id}/video/{quality}", cloud.cloudVideo)
	mux.HandleFunc("GET /api/cloud/files/{id}/video/{quality}/init", cloud.cloudVideoPart)
	mux.HandleFunc("GET /api/cloud/files/{id}/video/{quality}/segments/{segment}", cloud.cloudVideoPart)
	mux.HandleFunc("GET /api/cloud/files/{id}/versions", cloud.versions)
	mux.HandleFunc("GET /api/cloud/files/{id}/versions/{revision}/file", cloud.download)
	mux.HandleFunc("POST /api/cloud/resources/{id}/upload", cloud.upload)
	mux.HandleFunc("GET /api/cloud/recovery", cloud.recovery)
	mux.HandleFunc("POST /api/cloud/recovery", cloud.recovery)
	mux.HandleFunc("GET /api/session", localSession)
	mux.HandleFunc("GET /api/hubs", hubs.list)
	mux.HandleFunc("POST /api/hubs", hubs.save)
	mux.HandleFunc("PUT /api/hubs/active", hubs.selectActive)
	mux.HandleFunc("DELETE /api/hubs/{id}", hubs.remove)
	mux.HandleFunc("GET /api/directories", app.directories)
	mux.HandleFunc("GET /api/roots", app.roots)
	mux.HandleFunc("PUT /api/roots", app.addRoot)
	mux.HandleFunc("POST /api/roots/{id}/scan", app.scanRoot)
	mux.HandleFunc("GET /api/resources", app.listResources)
	mux.HandleFunc("GET /api/resources/selection", app.listResourceSelection)
	mux.HandleFunc("GET /api/resources/{id}", app.getResource)
	mux.HandleFunc("GET /api/resources/{id}/index", app.fileIndex)
	mux.HandleFunc("GET /api/media-stores", app.mediaStores)
	mux.HandleFunc("POST /api/media-stores", app.addMediaStore)
	mux.HandleFunc("PUT /api/media-stores/{id}/default", app.selectMediaStore)
	mux.HandleFunc("GET /api/resources/{id}/video-info", app.videoInfo)
	mux.HandleFunc("GET /api/resources/{id}/video-preview", app.videoPreview)
	mux.HandleFunc("POST /api/resources/{id}/video-preview", app.videoPreview)
	mux.HandleFunc("GET /api/resources/{id}/video/{quality}", app.videoVariant)
	mux.HandleFunc("POST /api/resources/{id}/video/{quality}", app.videoVariant)
	mux.HandleFunc("GET /api/resources/{id}/video/{quality}/init", app.videoInit)
	mux.HandleFunc("GET /api/resources/{id}/video/{quality}/segments/{segment}", app.videoSegment)
	mux.HandleFunc("POST /api/resources/tags", app.addTagBatch)
	mux.HandleFunc("GET /api/resources/{id}/tag-suggestions", app.tagSuggestions)
	mux.HandleFunc("GET /api/tag-batch-suggestions", app.tagBatchSuggestions)
	mux.HandleFunc("GET /api/resources/{id}/file", app.serveFile)
	mux.HandleFunc("GET /api/resources/{id}/shares", shares.list)
	mux.HandleFunc("POST /api/resources/{id}/shares", shares.create)
	mux.HandleFunc("DELETE /api/shares/{id}", shares.revoke)
	mux.HandleFunc("POST /api/shares/{id}/redeem", shares.redeem)
	mux.HandleFunc("GET /api/shares/{id}/file", shares.file)
	mux.HandleFunc("GET /api/shares/{id}/video/{quality}", shares.video)
	mux.HandleFunc("POST /api/shares/{id}/video/{quality}", shares.video)
	mux.HandleFunc("GET /api/shares/{id}/video/{quality}/init", shares.video)
	mux.HandleFunc("GET /api/shares/{id}/video/{quality}/segments/{segment}", shares.video)
	mux.HandleFunc("GET /api/resources/{id}/thumbnail", app.serveThumbnail)
	mux.HandleFunc("POST /api/resources/{id}/thumbnail", app.serveThumbnail)
	mux.HandleFunc("GET /api/preprocess", app.listPreprocessJobs)
	mux.HandleFunc("POST /api/preprocess/prioritize", app.prioritizePreprocessJobs)
	mux.HandleFunc("POST /api/preprocess/retry-failed", app.retryFailedPreprocessJobs)
	mux.HandleFunc("POST /api/preprocess/{id}/retry", app.retryPreprocessJob)
	mux.HandleFunc("POST /api/resources/{id}/tags", app.addTag)
	mux.HandleFunc("DELETE /api/resources/{id}/tags/{tagID}", app.removeTag)
	mux.HandleFunc("GET /api/tags", app.tags)
	mux.HandleFunc("GET /api/attribute-tags", app.attributeTags)
	mux.HandleFunc("GET /api/folders", app.listFolders)
	mux.HandleFunc("POST /api/folders", app.createFolder)
	mux.HandleFunc("POST /api/folders/{id}/unlock", app.unlockFolder)
	mux.HandleFunc("GET /api/folders/{id}/access", app.folderAccessStatus)
	mux.HandleFunc("PUT /api/folders/{id}/password", app.setFolderPassword)
	mux.HandleFunc("POST /api/folders/{id}/scan", app.assignRootToFolder)
	mux.HandleFunc("PUT /api/folders/{id}", app.renameFolder)
	mux.HandleFunc("DELETE /api/folders/{id}", app.deleteFolder)
	mux.HandleFunc("PUT /api/resources/folder", app.moveResourcesToFolder)
	mux.HandleFunc("GET /api/collections", app.listCollections)
	mux.HandleFunc("POST /api/collections", app.createCollection)
	mux.HandleFunc("PUT /api/collections/{id}", app.updateCollection)
	mux.HandleFunc("DELETE /api/collections/{id}", app.deleteCollection)
	static, err := fs.Sub(assets, "web/dist")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	addr := "127.0.0.1:4317"
	log.Printf("Myliba is ready at http://%s (data: %s)", addr, dataDir)
	httpServer := &http.Server{Addr: addr, Handler: securityHeaders(app.folderAccessMiddleware(mux))}
	if handled, err := runWindowsTray(httpServer, hubs); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	log.Fatal(httpServer.ListenAndServe())
}

func localSession(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"local": true})
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS roots (
	id TEXT PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	added_at TEXT NOT NULL,
	last_scan_at TEXT,
	auto_preprocess INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS resources (
	id TEXT PRIMARY KEY,
	root_id TEXT NOT NULL REFERENCES roots(id) ON DELETE CASCADE,
	path TEXT NOT NULL UNIQUE,
	file_id TEXT NOT NULL DEFAULT '',
	current_version_id TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL,
	size INTEGER NOT NULL,
	modified_at TEXT NOT NULL,
	kind TEXT NOT NULL,
	available INTEGER NOT NULL DEFAULT 1,
	first_seen_at TEXT NOT NULL,
	last_seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS versions (
	id TEXT PRIMARY KEY,
	resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
	content_id TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL,
	modified_at TEXT NOT NULL,
	discovered_at TEXT NOT NULL,
	UNIQUE(resource_id,size,modified_at)
);
CREATE TABLE IF NOT EXISTS tags (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS resource_tags (
	resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
	tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	PRIMARY KEY(resource_id, tag_id)
);
CREATE TABLE IF NOT EXISTS folders (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	created_at TEXT NOT NULL,
	password_hash TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS resource_folders (
	resource_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
	folder_id TEXT NOT NULL REFERENCES folders(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS smart_collections (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	filters_json TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS resource_fts USING fts5(resource_id UNINDEXED, name, path);
CREATE INDEX IF NOT EXISTS resources_root_idx ON resources(root_id);
CREATE INDEX IF NOT EXISTS resources_file_id_idx ON resources(file_id);
CREATE INDEX IF NOT EXISTS versions_resource_idx ON versions(resource_id, discovered_at);
CREATE INDEX IF NOT EXISTS resource_tags_tag_idx ON resource_tags(tag_id);
CREATE INDEX IF NOT EXISTS resource_folders_folder_idx ON resource_folders(folder_id);
CREATE TABLE IF NOT EXISTS preprocess_jobs (
    resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    operation TEXT NOT NULL,
    version_key TEXT NOT NULL,
    status TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    priority_at INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(resource_id, operation)
);
CREATE INDEX IF NOT EXISTS preprocess_jobs_queue_idx ON preprocess_jobs(status, priority DESC, priority_at DESC, created_at);
CREATE TABLE IF NOT EXISTS file_shares (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    size INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE INDEX IF NOT EXISTS file_shares_resource_idx ON file_shares(resource_id, created_at);
CREATE TABLE IF NOT EXISTS media_stores (
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    is_default INTEGER NOT NULL DEFAULT 0,
    added_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS file_assets (
    id TEXT PRIMARY KEY,
    resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    version_key TEXT NOT NULL,
    kind TEXT NOT NULL,
    spec TEXT NOT NULL,
    size INTEGER NOT NULL,
    codec TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS file_assets_resource_idx ON file_assets(resource_id, version_key);
CREATE TABLE IF NOT EXISTS file_asset_locations (
    asset_id TEXT NOT NULL REFERENCES file_assets(id) ON DELETE CASCADE,
    storage_id TEXT NOT NULL REFERENCES media_stores(id),
    path TEXT NOT NULL,
    PRIMARY KEY(asset_id,storage_id,path)
);
`)
	if err != nil {
		return err
	}
	columns, err := db.Query(`PRAGMA table_info(roots)`)
	if err != nil {
		return err
	}
	hasAutoPreprocess := false
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			return err
		}
		if name == "auto_preprocess" {
			hasAutoPreprocess = true
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		return err
	}
	columns.Close()
	if !hasAutoPreprocess {
		_, err = db.Exec(`ALTER TABLE roots ADD COLUMN auto_preprocess INTEGER NOT NULL DEFAULT 1`)
	}
	if err == nil {
		_, err = db.Exec(`ALTER TABLE folders ADD COLUMN password_hash TEXT NOT NULL DEFAULT ''`)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			err = nil
		}
	}
	if err == nil {
		_, err = db.Exec(`UPDATE resources SET kind='image' WHERE lower(name) LIKE '%.arw' AND kind <> 'image'`)
	}
	return err
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app": "myliba"})
}

func (s *server) roots(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT r.id, r.path, r.added_at, COALESCE(r.last_scan_at, ''), r.auto_preprocess, COUNT(x.id)
		FROM roots r LEFT JOIN resources x ON x.root_id=r.id GROUP BY r.id ORDER BY r.added_at`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, path, added, scanned string
		var count int
		var autoPreprocess bool
		if err := rows.Scan(&id, &path, &added, &scanned, &autoPreprocess, &count); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, map[string]any{"id": id, "path": path, "addedAt": added, "lastScanAt": scanned, "autoPreprocess": autoPreprocess, "count": count})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) addRoot(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Path           string `json:"path"`
		AutoPreprocess *bool  `json:"autoPreprocess"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请输入有效的目录路径")
		return
	}
	cleanInput := strings.TrimSpace(input.Path)
	if cleanInput == "" {
		writeError(w, http.StatusBadRequest, "请输入目录路径")
		return
	}
	path, err := filepath.Abs(cleanInput)
	if err != nil || path == "." {
		writeError(w, http.StatusBadRequest, "目录路径无效")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeError(w, http.StatusBadRequest, "该路径不是可访问的目录")
		return
	}
	var id string
	err = s.db.QueryRow(`SELECT id FROM roots WHERE path=?`, path).Scan(&id)
	if err == nil {
		if input.AutoPreprocess != nil {
			if _, err := s.db.Exec(`UPDATE roots SET auto_preprocess=? WHERE id=?`, *input.AutoPreprocess, id); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "path": path, "existing": true})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id = newID()
	autoPreprocess := true
	if input.AutoPreprocess != nil {
		autoPreprocess = *input.AutoPreprocess
	}
	_, err = s.db.Exec(`INSERT INTO roots(id,path,added_at,auto_preprocess) VALUES(?,?,?,?)`, id, path, now(), autoPreprocess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "path": path})
}

func (s *server) scanRoot(w http.ResponseWriter, r *http.Request) {
	rootID := r.PathValue("id")
	var rootPath string
	var autoPreprocess bool
	if err := s.db.QueryRow(`SELECT path,auto_preprocess FROM roots WHERE id=?`, rootID).Scan(&rootPath, &autoPreprocess); err != nil {
		writeError(w, http.StatusNotFound, "目录不存在")
		return
	}
	if info, err := os.Stat(rootPath); err != nil || !info.IsDir() {
		writeError(w, http.StatusGone, "目录当前不可访问")
		return
	}
	seen := map[string]struct{}{}
	var count int
	err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		path = filepath.Clean(path)
		seen[path] = struct{}{}
		kind := fileKind(path)
		fileID := localFileID(path)
		stamp := now()
		var id string
		err = s.db.QueryRow(`SELECT id FROM resources WHERE path=?`, path).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if fileID != "" {
				_ = s.db.QueryRow(`SELECT id FROM resources WHERE file_id=? ORDER BY last_seen_at DESC LIMIT 1`, fileID).Scan(&id)
			}
			if id != "" {
				_, err = s.db.Exec(`UPDATE resources SET root_id=?,path=?,name=?,size=?,modified_at=?,kind=?,available=1,last_seen_at=? WHERE id=?`,
					rootID, path, filepath.Base(path), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano), kind, stamp, id)
			} else {
				id = newID()
				_, err = s.db.Exec(`INSERT INTO resources(id,root_id,path,file_id,name,size,modified_at,kind,available,first_seen_at,last_seen_at)
					VALUES(?,?,?,?,?,?,?,?,1,?,?)`, id, rootID, path, fileID, filepath.Base(path), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano), kind, stamp, stamp)
			}
		} else if err == nil {
			_, err = s.db.Exec(`UPDATE resources SET root_id=?,file_id=?,name=?,size=?,modified_at=?,kind=?,available=1,last_seen_at=? WHERE id=?`,
				rootID, fileID, filepath.Base(path), info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano), kind, stamp, id)
		} else {
			return err
		}
		if err != nil {
			return err
		}
		modifiedAt := info.ModTime().UTC().Format(time.RFC3339Nano)
		versionID, err := findOrCreateVersion(s.db, id, info.Size(), modifiedAt)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE resources SET current_version_id=? WHERE id=?`, versionID, id); err != nil {
			return err
		}
		_, err = s.db.Exec(`DELETE FROM resource_fts WHERE resource_id=?`, id)
		if err == nil {
			_, err = s.db.Exec(`INSERT INTO resource_fts(resource_id,name,path) VALUES(?,?,?)`, id, filepath.Base(path), path)
		}
		if err != nil {
			return err
		}
		if autoPreprocess {
			if err := s.enqueuePreprocessing(id, kind, info); err != nil {
				return err
			}
		}
		if s.cloud != nil {
			s.cloud.queueLocalIndex(id)
		}
		count++
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "扫描失败："+err.Error())
		return
	}
	// Missing files are no longer locally available, but retain their resource and tags.
	rows, err := s.db.Query(`SELECT id,path FROM resources WHERE root_id=?`, rootID)
	if err == nil {
		missing := []string{}
		for rows.Next() {
			var id, path string
			if rows.Scan(&id, &path) == nil {
				if _, ok := seen[filepath.Clean(path)]; !ok {
					if _, statErr := os.Stat(path); statErr == nil {
						continue
					}
					missing = append(missing, id)
				}
			}
		}
		rows.Close()
		for _, id := range missing {
			_, _ = s.db.Exec(`UPDATE resources SET available=0 WHERE id=?`, id)
			_, _ = s.db.Exec(`DELETE FROM preprocess_jobs WHERE resource_id=? AND status IN ('pending','running')`, id)
			if s.cloud != nil {
				s.cloud.queueLocalIndex(id)
			}
		}
	}
	_, err = s.db.Exec(`UPDATE roots SET last_scan_at=? WHERE id=?`, now(), rootID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexed": count})
}

func resourceWhere(r *http.Request) ([]string, []any, error) {
	return resourceWhereWithoutAttributeCategory(r, "")
}

func resourceWhereWithoutAttributeCategory(r *http.Request, excludedCategory string) ([]string, []any, error) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	tags := r.URL.Query()["tag"]
	attributes, err := normalizeAttributeKeys(r.URL.Query()["attribute"])
	if err != nil {
		return nil, nil, err
	}
	unorganized := r.URL.Query().Get("unorganized") == "1"
	kind := r.URL.Query().Get("kind")
	rootID := r.URL.Query().Get("rootId")
	folderID := r.URL.Query().Get("folderId")
	if !validCollectionKind(kind) {
		return nil, nil, fmt.Errorf("筛选条件无效")
	}
	args := []any{}
	where := []string{"1=1"}
	if query != "" {
		match := ftsQuery(query)
		where = append(where, `(r.id IN (SELECT resource_id FROM resource_fts WHERE resource_fts MATCH ?) OR r.name LIKE ? OR r.path LIKE ? OR r.id IN (SELECT rt.resource_id FROM resource_tags rt JOIN tags t ON t.id=rt.tag_id WHERE t.name LIKE ?))`)
		args = append(args, match, "%"+query+"%", "%"+query+"%", "%"+query+"%")
	}
	for _, rawTag := range tags {
		tag := strings.TrimSpace(rawTag)
		if tag == "" {
			continue
		}
		where = append(where, `EXISTS (SELECT 1 FROM resource_tags rt JOIN tags t ON t.id=rt.tag_id WHERE rt.resource_id=r.id AND t.name=?)`)
		args = append(args, tag)
	}
	if unorganized {
		where = append(where, `NOT EXISTS (SELECT 1 FROM resource_tags rt WHERE rt.resource_id=r.id)`)
	}
	if kind != "" && excludedCategory != "type" {
		where = append(where, `r.kind=?`)
		args = append(args, kind)
	}
	if excludedCategory != "" {
		filtered := attributes[:0]
		for _, key := range attributes {
			category, _, _ := strings.Cut(key, ":")
			if category != excludedCategory {
				filtered = append(filtered, key)
			}
		}
		attributes = filtered
	}
	attributeClauses, attributeArgs := attributeFilterSQL(attributes)
	where = append(where, attributeClauses...)
	args = append(args, attributeArgs...)
	if rootID != "" {
		where = append(where, `r.root_id=?`)
		args = append(args, rootID)
	}
	if clause, folderArgs := folderScopeSQL(folderID, "r"); clause != "" {
		where = append(where, clause)
		args = append(args, folderArgs...)
	}
	return where, args, nil
}

func (s *server) listResourceSelection(w http.ResponseWriter, r *http.Request) {
	where, args, err := resourceWhere(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := 1000
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeError(w, http.StatusBadRequest, "分页大小无效")
			return
		}
		limit = parsed
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "分页位置无效")
			return
		}
		offset = parsed
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resources r WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.Query(`SELECT r.id,r.kind,r.available FROM resources r WHERE `+strings.Join(where, " AND ")+` ORDER BY r.id ASC LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Available bool   `json:"available"`
	}{}
	for rows.Next() {
		var item struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			Available bool   `json:"available"`
		}
		if err := rows.Scan(&item.ID, &item.Kind, &item.Available); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *server) listResources(w http.ResponseWriter, r *http.Request) {
	where, args, err := resourceWhere(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sort := r.URL.Query().Get("sort")
	if !validCollectionSort(sort) {
		writeError(w, http.StatusBadRequest, "筛选条件无效")
		return
	}
	limit := 48
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 || limit > 100 {
			limit = 48
		}
	}
	offset := 0
	_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
	if offset < 0 {
		offset = 0
	}
	orderBy := `r.modified_at DESC, r.id DESC`
	switch sort {
	case "modified_asc":
		orderBy = `r.modified_at ASC, r.id ASC`
	case "name_asc":
		orderBy = `r.name COLLATE NOCASE ASC, r.id ASC`
	case "name_desc":
		orderBy = `r.name COLLATE NOCASE DESC, r.id DESC`
	case "size_desc":
		orderBy = `r.size DESC, r.id DESC`
	}
	args = append(args, limit, offset)
	rows, err := s.db.Query(`SELECT r.id,r.name,r.path,r.size,r.modified_at,r.kind,r.available,r.current_version_id,COALESCE((SELECT rf.folder_id FROM resource_folders rf WHERE rf.resource_id=r.id),'')
		FROM resources r WHERE `+strings.Join(where, " AND ")+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []resource{}
	for rows.Next() {
		var item resource
		if err := rows.Scan(&item.ID, &item.Name, &item.Path, &item.Size, &item.Modified, &item.Kind, &item.Available, &item.VersionID, &item.FolderID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		item.Tags = []string{}
		item.AttributeTags = resourceAttributeTags(item.Name, item.Kind, item.Size)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range items {
		items[i].ContentSize = s.mediaContentSize(items[i].ID, items[i].Size, items[i].Modified)
		tagRows, err := s.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=? ORDER BY t.name COLLATE NOCASE`, items[i].ID)
		if err == nil {
			for tagRows.Next() {
				var name string
				if tagRows.Scan(&name) == nil {
					items[i].Tags = append(items[i].Tags, name)
				}
			}
			tagRows.Close()
		}
	}
	var total int
	totalQuery := `SELECT COUNT(*) FROM resources r WHERE ` + strings.Join(where, " AND ")
	countArgs := args[:len(args)-2]
	if err := s.db.QueryRow(totalQuery, countArgs...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func findOrCreateVersion(db *sql.DB, resourceID string, size int64, modifiedAt string) (string, error) {
	var versionID string
	err := db.QueryRow(`SELECT id FROM versions WHERE resource_id=? AND size=? AND modified_at=?`, resourceID, size, modifiedAt).Scan(&versionID)
	if err == nil {
		return versionID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	versionID = newID()
	_, err = db.Exec(`INSERT INTO versions(id,resource_id,size,modified_at,discovered_at) VALUES(?,?,?,?,?)`, versionID, resourceID, size, modifiedAt, now())
	return versionID, err
}

func (s *server) getResource(w http.ResponseWriter, r *http.Request) {
	var item resource
	err := s.db.QueryRow(`SELECT r.id,r.name,r.path,r.size,r.modified_at,r.kind,r.available,r.current_version_id,COALESCE((SELECT rf.folder_id FROM resource_folders rf WHERE rf.resource_id=r.id),'') FROM resources r WHERE r.id=?`, r.PathValue("id")).Scan(
		&item.ID, &item.Name, &item.Path, &item.Size, &item.Modified, &item.Kind, &item.Available, &item.VersionID, &item.FolderID)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取资源信息")
		return
	}
	item.Tags = []string{}
	item.ContentSize = s.mediaContentSize(item.ID, item.Size, item.Modified)
	item.AttributeTags = resourceAttributeTags(item.Name, item.Kind, item.Size)
	rows, err := s.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=? ORDER BY t.name COLLATE NOCASE`, item.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取标签")
		return
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取标签")
			return
		}
		item.Tags = append(item.Tags, name)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取标签")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) videoInfo(w http.ResponseWriter, r *http.Request) {
	var path, kind string
	if err := s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=?`, r.PathValue("id")).Scan(&path, &kind); err != nil {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if kind != "video" {
		writeError(w, http.StatusUnsupportedMediaType, "该资源不是视频")
		return
	}
	if s.ffprobePath == "" {
		writeError(w, http.StatusNotImplemented, "读取视频时长需要 FFprobe")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusGone, "文件当前不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, s.ffprobePath, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).Output()
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "无法读取视频时长")
		return
	}
	duration, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || duration <= 0 || duration > 86400 {
		writeError(w, http.StatusUnsupportedMediaType, "视频时长不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]float64{"duration": duration})
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request) {
	var path string
	if err := s.db.QueryRow(`SELECT path FROM resources WHERE id=?`, r.PathValue("id")).Scan(&path); err != nil {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusGone, "文件当前不可用，请重新扫描所在目录")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusGone, "无法读取文件")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	inline := safeInlineContentType(contentType)
	if inline {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if r.URL.Query().Get("download") == "1" || !inline {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	}
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
}

func (s *server) addTag(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请输入标签名称")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 40 {
		writeError(w, http.StatusBadRequest, "标签需为 1 到 40 个字符")
		return
	}
	resourceID := r.PathValue("id")
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resources WHERE id=?`, resourceID).Scan(&exists); err != nil || exists == 0 {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, newID(), name, now())
	if err == nil {
		var tagID string
		err = tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, resourceID, tagID, now())
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.cloud != nil {
		s.cloud.queueLocalIndex(resourceID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) addTagBatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs  []string `json:"ids"`
		Name string   `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式无效")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 40 || len(input.IDs) == 0 || len(input.IDs) > 20000 {
		writeError(w, http.StatusBadRequest, "请选择资源，并填写 1 到 40 个字符的标签")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO tags(id,name,created_at) VALUES(?,?,?) ON CONFLICT(name) DO NOTHING`, newID(), name, now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var tagID string
	if err = tx.QueryRow(`SELECT id FROM tags WHERE name=?`, name).Scan(&tagID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	statement, err := tx.Prepare(`INSERT INTO resource_tags(resource_id,tag_id,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer statement.Close()
	for _, id := range input.IDs {
		if _, err := statement.Exec(id, tagID, now()); err != nil {
			writeError(w, http.StatusBadRequest, "所选资源列表已变化，请刷新后重试")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.cloud != nil {
		for _, id := range input.IDs {
			s.cloud.queueLocalIndex(id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": len(input.IDs), "tag": name})
}

func (s *server) removeTag(w http.ResponseWriter, r *http.Request) {
	resourceID := r.PathValue("id")
	_, err := s.db.Exec(`DELETE FROM resource_tags WHERE resource_id=? AND tag_id=?`, resourceID, r.PathValue("tagID"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.cloud != nil {
		s.cloud.queueLocalIndex(resourceID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) tags(w http.ResponseWriter, r *http.Request) {
	folderID := r.URL.Query().Get("folderId")
	query := `SELECT t.id,t.name,COUNT(rt.resource_id) FROM tags t LEFT JOIN resource_tags rt ON rt.tag_id=t.id`
	args := []any{}
	if clause, folderArgs := folderScopeSQL(folderID, "res"); clause != "" {
		query = `SELECT t.id,t.name,COUNT(rt.resource_id) FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id JOIN resources res ON res.id=rt.resource_id WHERE ` + clause
		args = append(args, folderArgs...)
	}
	query += ` GROUP BY t.id ORDER BY t.name COLLATE NOCASE`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name string
		var count int
		if rows.Scan(&id, &name, &count) == nil {
			items = append(items, map[string]any{"id": id, "name": name, "count": count})
		}
	}
	writeJSON(w, http.StatusOK, items)
}

func ftsQuery(query string) string {
	parts := strings.Fields(query)
	for i, part := range parts {
		parts[i] = `"` + strings.ReplaceAll(part, `"`, `""`) + `"*`
	}
	return strings.Join(parts, " AND ")
}

func safeInlineContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	if mediaType == "application/pdf" || mediaType == "text/plain" {
		return true
	}
	return (strings.HasPrefix(mediaType, "image/") && mediaType != "image/svg+xml") ||
		strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "audio/")
}

func fileKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".avif", ".heic", ".arw":
		return "image"
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v":
		return "video"
	case ".mp3", ".wav", ".flac", ".m4a", ".aac", ".ogg":
		return "audio"
	case ".pdf":
		return "document"
	default:
		return "file"
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := make([]byte, 36)
	hex.Encode(encoded[0:8], b[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], b[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], b[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], b[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], b[10:16])
	return string(encoded)
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			origin := r.Header.Get("Origin")
			if origin != "" && origin != "http://127.0.0.1:4317" {
				writeError(w, http.StatusForbidden, "跨站请求已拒绝")
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
