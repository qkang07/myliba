package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildVideoPreview(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=10:duration=4", "-c:v", "libx264", "-pix_fmt", "yuv420p", source).CombinedOutput(); err != nil {
		t.Fatalf("create source: %v %s", err, out)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "preview")
	s := &server{ffmpegPath: ffmpeg, videoSlots: make(chan struct{}, 1), videoJobs: map[string]*videoJob{output: {status: "processing"}}}
	s.buildVideoPreview(output, source, info, previewSpec{Quality: "360p", Clips: []previewClip{{Start: 0, End: 1}, {Start: 2, End: 3}}})
	if j := s.videoJobs[output]; j != nil {
		t.Fatalf("job failed: %+v", j)
	}
	data, err := os.ReadFile(filepath.Join(output, "preview.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec previewSpec
	if err = json.Unmarshal(data, &spec); err != nil || len(spec.Clips) != 2 {
		t.Fatalf("bad manifest: %s %v", data, err)
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", filepath.Join(output, spec.File)).Output()
	if err != nil {
		t.Fatalf("preview invalid: %v", err)
	}
	t.Logf("preview duration: %s", out)
}
func TestVideoPreviewRemotePermissions(t *testing.T) {
	path := "/api/resources/example/video-preview"
	if !allowedRemoteRequest("GET", path+"?file=1") || !allowedRemoteRequest("POST", path) || !allowedHubRelayRequest("GET", path+"?file=1") || !hubRelayContentPath(path) {
		t.Fatal("preview remote permissions missing")
	}
	if allowedRemoteRequest("DELETE", path) {
		t.Fatal("unexpected delete permission")
	}
}
