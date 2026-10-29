package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDemandVideoSegmentsCanSeekAndDecode(t *testing.T) {
	ffmpeg, ffprobe := mediaToolPath("ffmpeg"), mediaToolPath("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg and FFprobe unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "video.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "12", "-c:v", "mpeg4", "-c:a", "aac", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create source: %v %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	s := &server{ffmpegPath: ffmpeg, ffprobePath: ffprobe}
	manifest, err := demandManifest(ctx, s, source, source, "360p")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Mode != "ondemand" || len(manifest.Segments) != 3 || manifest.Duration < 11.9 {
		t.Fatalf("invalid manifest: %+v", manifest)
	}
	first, err := demandEncode(ctx, s, source, source, "360p", 0)
	if err != nil {
		t.Fatal(err)
	}
	init, _, err := splitDemandMP4(first)
	if err != nil {
		t.Fatal(err)
	}
	later, err := demandEncode(ctx, s, source, source, "360p", 2)
	if err != nil {
		t.Fatal(err)
	}
	_, media, err := splitDemandMP4(later)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "seek.mp4")
	if err = os.WriteFile(output, append(append([]byte{}, init...), media...), 0600); err != nil {
		t.Fatal(err)
	}
	decode := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", output, "-f", "null", "-")
	if out, err := decode.CombinedOutput(); err != nil {
		t.Fatalf("decode seek segment: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "init.mp4"), init, 0600); err != nil {
		t.Fatal(err)
	}
	playlist := "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n"
	for i := range manifest.Segments {
		segment, err := demandEncode(ctx, s, source, source, "360p", i)
		if err != nil {
			t.Fatal(err)
		}
		_, fragment, err := splitDemandMP4(segment)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("segment-%d.m4s", i)), fragment, 0600); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			playlist += "#EXT-X-DISCONTINUITY\n"
		}
		playlist += fmt.Sprintf("#EXTINF:%.6f,\nsegment-%d.m4s\n", manifest.Segments[i], i)
	}
	playlist += "#EXT-X-ENDLIST\n"
	playlistPath := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(playlistPath, []byte(playlist), 0600); err != nil {
		t.Fatal(err)
	}
	decodeHLS := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-i", playlistPath, "-f", "null", "-")
	if output, err := decodeHLS.CombinedOutput(); err != nil {
		t.Fatalf("decode HLS segments: %v %s", err, output)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := demandEncode(cancelled, s, source, source+"-cancel", "360p", 1); err == nil {
		t.Fatal("cancelled request started a conversion")
	}
}

func TestDemandCloudVideoWithoutLocalOriginal(t *testing.T) {
	ffmpeg, ffprobe := mediaToolPath("ffmpeg"), mediaToolPath("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		t.Skip("FFmpeg and FFprobe unavailable")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "cloud.mp4")
	create := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-t", "9", "-c:v", "mpeg4", "-an", source)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create cloud source: %v %s", err, output)
	}
	runtime := testCloudRuntime(t, filepath.Join(dir, "cloud"), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.uploadFile(context.Background(), "cloud-video", source, false); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	input, key, closeInput, err := client.demandInput(ctx, "cloud-video")
	if err != nil {
		t.Fatal(err)
	}
	defer closeInput()
	s := &server{ffmpegPath: ffmpeg, ffprobePath: ffprobe}
	manifest, err := demandManifest(ctx, s, input, key, "360p")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Mode != "ondemand" || len(manifest.Segments) != 3 {
		t.Fatalf("unexpected cloud manifest: %+v", manifest)
	}
	if _, err = demandEncode(ctx, s, input, key, "360p", 2); err != nil {
		t.Fatal(err)
	}
}

func TestDemandQueuedSegmentCancelsOnSeek(t *testing.T) {
	demandSlots <- struct{}{}
	demandSlots <- struct{}{}
	defer func() { <-demandSlots; <-demandSlots }()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := demandEncode(ctx, &server{}, "unused", "queued-seek-test", "360p", 12)
		result <- err
	}()
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled {
			t.Fatalf("expected cancelled request, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled queued segment did not release promptly")
	}
}
