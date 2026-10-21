package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type videoQuality struct {
	width, height int
	bitrate       string
	audioBitrate  string
}

var videoQualities = map[string]videoQuality{
	"360p":  {640, 360, "600k", "96k"},
	"720p":  {1280, 720, "2000k", "128k"},
	"1080p": {1920, 1080, "4000k", "128k"},
}

type videoManifest struct {
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Codec    string    `json:"codec,omitempty"`
	Duration float64   `json:"duration,omitempty"`
	Segments []float64 `json:"segments,omitempty"`
}

type videoJob struct {
	status string
	err    string
}

func (s *server) videoSource(id, quality string) (string, os.FileInfo, string, error) {
	if _, ok := videoQualities[quality]; !ok {
		return "", nil, "", errors.New("不支持的视频清晰度")
	}
	var path, kind string
	if err := s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=? AND available=1`, id).Scan(&path, &kind); err != nil || kind != "video" {
		return "", nil, "", errors.New("视频不存在")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, "", errors.New("源文件当前不可用")
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", id, info.Size(), info.ModTime().UnixNano(), quality)))
	_, dir := s.mediaAssetPath(hex.EncodeToString(digest[:]), "video")
	return path, info, dir, nil
}

func readVideoManifest(dir string) (videoManifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return videoManifest{}, err
	}
	var manifest videoManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return videoManifest{}, err
	}
	if manifest.Status != "ready" || len(manifest.Segments) == 0 || manifest.Duration <= 0 {
		return videoManifest{}, errors.New("视频索引无效")
	}
	return manifest, nil
}

func (s *server) videoVariant(w http.ResponseWriter, r *http.Request) {
	quality := r.PathValue("quality")
	path, info, dir, err := s.videoSource(r.PathValue("id"), quality)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if manifest, err := readVideoManifest(dir); err == nil {
		if err := s.recordVideoAsset(r.PathValue("id"), info, quality, dir, manifest); err != nil {
			log.Printf("video index %s: %v", filepath.Base(dir), err)
		}
		writeJSON(w, http.StatusOK, manifest)
		return
	}
	if r.Method == http.MethodPost && s.ffmpegPath == "" {
		writeError(w, http.StatusNotImplemented, "视频转码需要 FFmpeg")
		return
	}
	s.videoMu.Lock()
	if s.videoJobs == nil {
		s.videoJobs = make(map[string]*videoJob)
	}
	job := s.videoJobs[dir]
	if r.Method == http.MethodPost && (job == nil || job.status == "failed") {
		job = &videoJob{status: "processing"}
		s.videoJobs[dir] = job
		if s.videoSlots == nil {
			s.videoSlots = make(chan struct{}, 1)
		}
		go s.runVideoJob(r.PathValue("id"), dir, path, info, quality)
	}
	status := "missing"
	message := ""
	if job != nil {
		status, message = job.status, job.err
	}
	s.videoMu.Unlock()
	code := http.StatusOK
	if status == "processing" {
		code = http.StatusAccepted
	}
	writeJSON(w, code, videoManifest{Status: status, Error: message})
}

func (s *server) runVideoJob(id, dir, source string, sourceInfo os.FileInfo, quality string) {
	s.videoSlots <- struct{}{}
	defer func() { <-s.videoSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	err := s.transcodeVideo(ctx, dir, source, sourceInfo, videoQualities[quality])
	if err == nil {
		manifest, readErr := readVideoManifest(dir)
		if readErr == nil {
			readErr = s.recordVideoAsset(id, sourceInfo, quality, dir, manifest)
		}
		err = readErr
	}
	s.videoMu.Lock()
	if err != nil {
		log.Printf("video conversion %s: %v", filepath.Base(source), err)
		s.videoJobs[dir] = &videoJob{status: "failed", err: "视频转换失败，请检查文件格式后重试"}
	} else {
		delete(s.videoJobs, dir)
	}
	s.videoMu.Unlock()
}

func (s *server) transcodeVideo(ctx context.Context, dir, source string, sourceInfo os.FileInfo, quality videoQuality) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	filter := fmt.Sprintf("fps=30,scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease:force_divisible_by=2", quality.width, quality.height)
	cmd := exec.CommandContext(ctx, s.ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y", "-i", source,
		"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn",
		"-vf", filter, "-c:v", "libx264", "-preset", "veryfast", "-profile:v", "baseline", "-level:v", "4.1", "-pix_fmt", "yuv420p",
		"-b:v", quality.bitrate, "-maxrate", quality.bitrate, "-bufsize", quality.bitrate,
		"-g", "120", "-keyint_min", "120", "-sc_threshold", "0", "-force_key_frames", "expr:gte(t,n_forced*4)",
		"-c:a", "aac", "-b:a", quality.audioBitrate, "-ac", "2",
		"-f", "hls", "-hls_segment_type", "fmp4", "-hls_time", "4", "-hls_playlist_type", "vod",
		"-hls_flags", "independent_segments", "-hls_fmp4_init_filename", "init.mp4",
		"-hls_segment_filename", filepath.Join(tmp, "segment-%05d.m4s"), filepath.Join(tmp, "index.m3u8"))
	cmd.Dir = tmp
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(output)))
	}
	manifest, err := parseVideoPlaylist(filepath.Join(tmp, "index.m3u8"))
	if err != nil {
		return err
	}
	init, err := os.ReadFile(filepath.Join(tmp, "init.mp4"))
	if err != nil {
		return err
	}
	manifest.Codec = mp4Codec(init)
	if current, err := os.Stat(source); err != nil || preprocessVersion(current) != preprocessVersion(sourceInfo) {
		return errors.New("源视频在转换期间发生变化")
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "manifest.json"), data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return nil
}

func parseVideoPlaylist(path string) (videoManifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return videoManifest{}, err
	}
	defer file.Close()
	manifest := videoManifest{Status: "ready", Segments: []float64{}}
	scanner := bufio.NewScanner(file)
	var segmentDuration float64
	ended := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#EXTINF:") {
			segmentDuration, err = strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ","), 64)
			if err != nil || segmentDuration <= 0 {
				return videoManifest{}, errors.New("视频分段时长无效")
			}
		} else if strings.HasPrefix(line, "segment-") && strings.HasSuffix(line, ".m4s") {
			expected := fmt.Sprintf("segment-%05d.m4s", len(manifest.Segments))
			if line != expected || segmentDuration <= 0 {
				return videoManifest{}, errors.New("视频分段索引无效")
			}
			manifest.Segments = append(manifest.Segments, segmentDuration)
			manifest.Duration += segmentDuration
			segmentDuration = 0
		} else if line == "#EXT-X-ENDLIST" {
			ended = true
		}
	}
	if err := scanner.Err(); err != nil {
		return videoManifest{}, err
	}
	if !ended || len(manifest.Segments) == 0 || segmentDuration != 0 {
		return videoManifest{}, errors.New("视频分段列表不完整")
	}
	return manifest, nil
}

func mp4Codec(init []byte) string {
	codec := "avc1.42E029"
	if offset := strings.Index(string(init), "avcC"); offset >= 0 && offset+8 <= len(init) {
		codec = fmt.Sprintf("avc1.%02X%02X%02X", init[offset+5], init[offset+6], init[offset+7])
	}
	if strings.Contains(string(init), "mp4a") {
		codec += ", mp4a.40.2"
	}
	return codec
}

func (s *server) serveVideoPart(w http.ResponseWriter, r *http.Request, name, contentType string) {
	_, _, dir, err := s.videoSource(r.PathValue("id"), r.PathValue("quality"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if _, err := readVideoManifest(dir); err != nil {
		writeError(w, http.StatusNotFound, "视频尚未完成转换")
		return
	}
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		writeError(w, http.StatusNotFound, "视频分段不存在")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取视频分段")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *server) videoInit(w http.ResponseWriter, r *http.Request) {
	s.serveVideoPart(w, r, "init.mp4", "video/mp4")
}

func (s *server) videoSegment(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("segment"))
	if err != nil || index < 0 || index > 99999 {
		writeError(w, http.StatusBadRequest, "视频分段编号无效")
		return
	}
	_, _, dir, err := s.videoSource(r.PathValue("id"), r.PathValue("quality"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	manifest, err := readVideoManifest(dir)
	if err != nil || index >= len(manifest.Segments) {
		writeError(w, http.StatusNotFound, "视频分段不存在")
		return
	}
	s.serveVideoPart(w, r, fmt.Sprintf("segment-%05d.m4s", index), "video/iso.segment")
}
