package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVideoStreamRoutesStayDirect(t *testing.T) {
	status := "/api/resources/item/video/720p"
	init := status + "/init"
	segment := status + "/segments/0"
	for _, path := range []string{status, init, segment} {
		if !allowedRemoteRequest(http.MethodGet, path) {
			t.Errorf("remote GET denied: %s", path)
		}
	}
	if !allowedRemoteRequest(http.MethodPost, status) || !allowedHubRelayRequest(http.MethodPost, status) {
		t.Fatal("video conversion control request should be available")
	}
	for _, path := range []string{init, segment} {
		if allowedHubRelayRequest(http.MethodGet, path) {
			t.Errorf("Hub must not relay video bytes: %s", path)
		}
	}
	for _, path := range []string{status + "/segments/../file", status + "/segments/0/extra"} {
		if allowedRemoteRequest(http.MethodGet, path) {
			t.Errorf("unexpected route allowed: %s", path)
		}
	}
	shareID := newID()
	shareStatus := "/api/shares/" + shareID + "/video/720p"
	shareSegment := shareStatus + "/segments/0"
	if !allowedRemoteRequest(http.MethodPost, shareStatus) || !allowedRemoteRequest(http.MethodGet, shareSegment) || allowedHubRelayRequest(http.MethodGet, shareSegment) {
		t.Fatal("share video routes violate control/content boundary")
	}
	key := http.Header{"X-Myliba-Share-Key": []string{strings.Repeat("a", 64)}}
	for _, path := range []string{shareStatus, shareStatus + "/init", shareSegment} {
		if !allowedShareRTCRequest(shareID, wireMessage{Method: http.MethodGet, Path: path, Headers: key}) {
			t.Errorf("share direct request denied: %s", path)
		}
	}
	if !allowedShareRTCRequest(shareID, wireMessage{Method: http.MethodPost, Path: shareStatus, Headers: key}) {
		t.Fatal("share video conversion denied")
	}
	if allowedShareRTCRequest(shareID, wireMessage{Method: http.MethodGet, Path: shareSegment}) ||
		allowedShareRTCRequest(shareID, wireMessage{Method: http.MethodGet, Path: shareStatus + "/segments/../file", Headers: key}) {
		t.Fatal("share video route accepted an unauthorized request")
	}
}

func TestVideoConversionAndRangePlayback(t *testing.T) {
	ffmpeg := mediaToolPath("ffmpeg")
	if ffmpeg == "" {
		t.Skip("FFmpeg unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	create := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-t", "5", "-c:v", "mpeg4", "-an", source)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create test video: %v: %s", err, output)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,'2026-01-01')`, dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES('item','root',?,'source.mp4',?,?,'video',1,'2026-01-01','2026-01-01')`, source, info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := &server{db: db, videoDir: filepath.Join(dir, "videos"), ffmpegPath: ffmpeg}
	_, _, outputDir, err := app.videoSource("item", "360p")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := app.transcodeVideo(ctx, outputDir, source, info, videoQualities["360p"]); err != nil {
		t.Fatal(err)
	}
	manifest, err := readVideoManifest(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Segments) < 2 || manifest.Duration < 4.5 || !strings.HasPrefix(manifest.Codec, "avc1.") {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/resources/item/video/360p/segments/0", nil)
	request.SetPathValue("id", "item")
	request.SetPathValue("quality", "360p")
	request.SetPathValue("segment", "0")
	request.Header.Set("Range", "bytes=0-99")
	response := httptest.NewRecorder()
	app.videoSegment(response, request)
	if response.Code != http.StatusPartialContent || response.Body.Len() != 100 || !strings.HasPrefix(response.Header().Get("Content-Range"), "bytes 0-99/") {
		t.Fatal(fmt.Sprintf("segment Range response: status=%d size=%d range=%s", response.Code, response.Body.Len(), response.Header().Get("Content-Range")))
	}
}

func TestShareVideoRequiresLiveCapability(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(source, []byte("video test source"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?)`, dir, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resources(id,root_id,path,name,size,modified_at,kind,available,first_seen_at,last_seen_at) VALUES('item','root',?,'clip.mp4',?,?,'video',1,?,?)`, source, info.Size(), stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	shareID := newID()
	key := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO file_shares(id,resource_id,token_hash,size,modified_ns,created_at,expires_at,revoked_at) VALUES(?,?,?,?,?,?,?,NULL)`, shareID, "item", shareTokenHash(key), info.Size(), info.ModTime().UnixNano(), stamp, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	service := &shareService{db: db, app: &server{db: db, videoDir: filepath.Join(dir, "videos")}}
	call := func(token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/shares/"+shareID+"/video/360p", nil)
		request.SetPathValue("id", shareID)
		request.SetPathValue("quality", "360p")
		request.Header.Set("X-Myliba-Share-Key", token)
		response := httptest.NewRecorder()
		service.video(response, request)
		return response
	}
	if response := call(""); response.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: %d", response.Code)
	}
	if response := call(key); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"missing"`) {
		t.Fatalf("valid key: %d %s", response.Code, response.Body.String())
	}
	if _, err := db.Exec(`UPDATE file_shares SET revoked_at=? WHERE id=?`, now(), shareID); err != nil {
		t.Fatal(err)
	}
	if response := call(key); response.Code != http.StatusGone {
		t.Fatalf("revoked key: %d", response.Code)
	}
}
