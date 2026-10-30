package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type previewClip struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}
type previewSpec struct {
	Quality string        `json:"quality"`
	Clips   []previewClip `json:"clips"`
	File    string        `json:"file,omitempty"`
}

func (s *server) videoPreview(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var source, kind string
	if s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=? AND available=1`, id).Scan(&source, &kind) != nil || kind != "video" {
		writeError(w, 404, "视频不存在或不可用")
		return
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, 410, "源文件当前不可用")
		return
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("preview\x00%s\x00%d\x00%d", id, info.Size(), info.ModTime().UnixNano())))
	dir := s.videoPreviewDirectory(hex.EncodeToString(hash[:]), r.Method)
	manifest := filepath.Join(dir, "preview.json")
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("file") == "1" {
			data, e := os.ReadFile(manifest)
			var spec previewSpec
			if e != nil || json.Unmarshal(data, &spec) != nil || filepath.Base(spec.File) != spec.File || !strings.HasSuffix(spec.File, ".mp4") {
				writeError(w, 404, "预览短视频尚未生成")
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			http.ServeFile(w, r, filepath.Join(dir, spec.File))
			return
		}
		result := map[string]any{"status": "missing"}
		if data, e := os.ReadFile(manifest); e == nil {
			var spec previewSpec
			if json.Unmarshal(data, &spec) == nil {
				if _, e = os.Stat(filepath.Join(dir, filepath.Base(spec.File))); e == nil {
					result["status"] = "ready"
					result["spec"] = spec
				}
			}
		}
		s.videoMu.Lock()
		if job := s.videoJobs[dir]; job != nil {
			result["status"] = job.status
			result["error"] = job.err
		}
		s.videoMu.Unlock()
		writeJSON(w, 200, result)
		return
	}
	if s.ffmpegPath == "" || s.ffprobePath == "" {
		writeError(w, 501, "生成预览需要 FFmpeg 和 FFprobe")
		return
	}
	var spec previewSpec
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&spec); err != nil || videoQualities[spec.Quality].width == 0 || len(spec.Clips) == 0 || len(spec.Clips) > 10 {
		writeError(w, 400, "请选择分辨率和 1 至 10 个片段")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, s.ffprobePath, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", source).Output()
	duration, e := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || e != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 {
		writeError(w, 400, "无法读取视频时长")
		return
	}
	total := 0.0
	for _, c := range spec.Clips {
		if math.IsNaN(c.Start) || math.IsNaN(c.End) || math.IsInf(c.Start, 0) || math.IsInf(c.End, 0) || c.Start < 0 || c.End <= c.Start || c.End > duration+0.01 {
			writeError(w, 400, "片段时间无效或超出视频时长")
			return
		}
		total += c.End - c.Start
	}
	if total > 120 {
		writeError(w, 400, "预览总时长不能超过 120 秒")
		return
	}
	s.videoMu.Lock()
	if s.videoJobs == nil {
		s.videoJobs = map[string]*videoJob{}
	}
	if job := s.videoJobs[dir]; job != nil && job.status == "processing" {
		s.videoMu.Unlock()
		writeError(w, 409, "预览正在生成")
		return
	}
	s.videoJobs[dir] = &videoJob{status: "processing"}
	if s.videoSlots == nil {
		s.videoSlots = make(chan struct{}, 1)
	}
	s.videoMu.Unlock()
	go s.buildVideoPreview(dir, source, info, spec)
	writeJSON(w, 202, map[string]string{"status": "processing"})
}

func (s *server) videoPreviewDirectory(key, method string) string {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT path FROM media_stores ORDER BY is_default DESC,added_at,id`)
		if err == nil {
			defer rows.Close()
			var first string
			for rows.Next() {
				var root string
				if rows.Scan(&root) != nil {
					continue
				}
				dir := filepath.Join(root, "videos", "previews", key)
				if first == "" {
					first = dir
				}
				if _, e := os.Stat(dir); e == nil {
					return dir
				}
			}
			if method == http.MethodPost {
				if store, e := s.writableMediaStore(); e == nil {
					return filepath.Join(store.Path, "videos", "previews", key)
				}
			}
			if first != "" {
				return first
			}
		}
	}
	return filepath.Join(s.videoDir, "previews", key)
}
func (s *server) buildVideoPreview(dir, source string, info os.FileInfo, spec previewSpec) {
	s.videoSlots <- struct{}{}
	defer func() { <-s.videoSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err := os.MkdirAll(dir, 0700)
	if err == nil {
		var filters []string
		var inputs strings.Builder
		q := videoQualities[spec.Quality]
		for i, c := range spec.Clips {
			filters = append(filters, fmt.Sprintf("[0:v:0]trim=start=%.3f:end=%.3f,setpts=PTS-STARTPTS,scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease:force_divisible_by=2,setsar=1[v%d]", c.Start, c.End, q.width, q.height, i))
			fmt.Fprintf(&inputs, "[v%d]", i)
		}
		filters = append(filters, fmt.Sprintf("%sconcat=n=%d:v=1:a=0[out]", inputs.String(), len(spec.Clips)))
		f, e := os.CreateTemp(dir, "preview-*.mp4")
		if e != nil {
			err = e
		} else {
			name := f.Name()
			f.Close()
			defer func() {
				if err != nil {
					os.Remove(name)
				}
			}()
			cmd := exec.CommandContext(ctx, s.ffmpegPath, "-hide_banner", "-loglevel", "error", "-y", "-i", source, "-filter_complex", strings.Join(filters, ";"), "-map", "[out]", "-an", "-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-movflags", "+faststart", name)
			if out, e := cmd.CombinedOutput(); e != nil {
				err = fmt.Errorf("ffmpeg: %w: %s", e, string(out))
			} else if now, e := os.Stat(source); e != nil || preprocessVersion(now) != preprocessVersion(info) {
				err = fmt.Errorf("source changed")
			} else {
				spec.File = filepath.Base(name)
				data, e := json.Marshal(spec)
				if e != nil {
					err = e
				} else {
					tmp, e := os.CreateTemp(dir, "manifest-*.json")
					if e != nil {
						err = e
					} else {
						_, err = tmp.Write(data)
						if e = tmp.Close(); err == nil {
							err = e
						}
						if err == nil {
							_ = os.Remove(filepath.Join(dir, "preview.json"))
							err = os.Rename(tmp.Name(), filepath.Join(dir, "preview.json"))
						}
						if err != nil {
							os.Remove(tmp.Name())
						}
					}
				}
			}
		}
	}
	if err == nil {
		storeID := s.mediaStoreIDForPath(dir)
		var maxBytes int64
		var root string
		if s.db != nil && s.db.QueryRow(`SELECT path,max_bytes FROM media_stores WHERE id=?`, storeID).Scan(&root, &maxBytes) == nil {
			used := mediaStoreUsage(root)
			if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
				s.cloud.runtime.disk.setRootUsage(root, used)
			}
			if maxBytes > 0 && used > maxBytes {
				withoutAsset, _ := mediaStoreUsageForReservation(root, dir, 0)
				assetBytes := max(int64(0), used-withoutAsset)
				if _, _, moveErr := s.moveMediaAssetToAvailableStore(dir, "videos", assetBytes); moveErr != nil {
					_ = os.RemoveAll(dir)
					if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
						s.cloud.runtime.disk.setRootUsage(root, mediaStoreUsage(root))
					}
					err = fmt.Errorf("media storage capacity exceeded: %w", moveErr)
				}
			}
		}
	}
	s.videoMu.Lock()
	if err != nil {
		s.videoJobs[dir] = &videoJob{status: "failed", err: "预览生成失败，请检查源文件或 FFmpeg"}
	} else {
		delete(s.videoJobs, dir)
	}
	s.videoMu.Unlock()
}
