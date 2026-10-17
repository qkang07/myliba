package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	thumbnailWidth  = 480
	thumbnailHeight = 320
	maxImagePixels  = 80_000_000
)

func (s *server) serveThumbnail(w http.ResponseWriter, r *http.Request) {
	var sourcePath, kind string
	if err := s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=?`, r.PathValue("id")).Scan(&sourcePath, &kind); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "资源不存在")
		} else {
			writeError(w, http.StatusInternalServerError, "无法读取资源信息")
		}
		return
	}
	if kind != "image" && kind != "video" {
		writeError(w, http.StatusUnsupportedMediaType, "该资源类型不支持缩略图")
		return
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		writeError(w, http.StatusGone, "文件当前不可用，请重新扫描所在目录")
		return
	}
	if !info.Mode().IsRegular() {
		writeError(w, http.StatusUnsupportedMediaType, "资源不是普通文件")
		return
	}
	width, height := thumbnailSize(r.URL.Query().Get("size"))
	at := 0.0
	if raw := r.URL.Query().Get("at"); raw != "" {
		if kind != "video" {
			writeError(w, http.StatusBadRequest, "仅视频支持指定截图时间")
			return
		}
		at, err = strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(at) || math.IsInf(at, 0) || at < 0 || at > 86400 {
			writeError(w, http.StatusBadRequest, "截图时间超出范围")
			return
		}
	}
	cachePath, etag := s.thumbnailCachePath(r.PathValue("id"), info, width, at)
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Type", "image/jpeg")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if _, err := os.Stat(cachePath); err == nil {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
		} else {
			http.ServeFile(w, r, cachePath)
		}
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Cache-Control", "no-store")
		writeError(w, http.StatusNotFound, "缩略图尚未生成")
		return
	}
	if err := s.ensureThumbnail(r.Context(), r.PathValue("id"), sourcePath, kind, info, width, height, at); err != nil {
		log.Printf("thumbnail generation failed for %s: %v", filepath.Base(sourcePath), err)
		if kind == "video" && s.ffmpegPath == "" {
			writeError(w, http.StatusNotImplemented, "提取视频首帧需要安装 FFmpeg 并将其加入 PATH")
		} else {
			writeError(w, http.StatusUnsupportedMediaType, "无法为该文件生成缩略图")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) thumbnailCachePath(id string, info os.FileInfo, width int, at float64) (string, string) {
	version := fmt.Sprintf("%s\x00%d\x00%d", id, info.Size(), info.ModTime().UnixNano())
	if width != thumbnailWidth || at != 0 {
		version += fmt.Sprintf("\x00%d\x00%.3f", width, at)
	}
	digest := sha256.Sum256([]byte(version))
	hexDigest := hex.EncodeToString(digest[:])
	return filepath.Join(s.thumbnailDir, hexDigest+".jpg"), fmt.Sprintf("%q", hexDigest)
}

func (s *server) ensureThumbnail(ctx context.Context, id, sourcePath, kind string, info os.FileInfo, width, height int, at float64) error {
	cachePath, _ := s.thumbnailCachePath(id, info, width, at)
	lock := s.thumbnailLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	content, err := s.generateThumbnail(ctx, sourcePath, kind, width, height, at)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(s.thumbnailDir, "thumbnail-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, cachePath)
}

func (s *server) thumbnailLock(id string) *sync.Mutex {
	s.thumbnailMu.Lock()
	defer s.thumbnailMu.Unlock()
	lock := s.thumbnailLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.thumbnailLocks[id] = lock
	}
	return lock
}

func thumbnailSize(level string) (int, int) {
	switch level {
	case "small":
		return 240, 160
	case "large":
		return 1280, 960
	default:
		return thumbnailWidth, thumbnailHeight
	}
}

func (s *server) generateThumbnail(parent context.Context, sourcePath, kind string, width, height int, at float64) ([]byte, error) {
	if s.ffmpegPath != "" {
		ctx, cancel := context.WithTimeout(parent, 30*time.Second)
		defer cancel()
		args := []string{"-v", "error", "-nostdin"}
		if kind == "video" && at > 0 {
			args = append(args, "-ss", strconv.FormatFloat(at, 'f', 3, 64))
		}
		args = append(args, "-i", sourcePath, "-frames:v", "1",
			"-vf", fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", width, height),
			"-f", "image2pipe", "-c:v", "mjpeg", "-q:v", "5", "pipe:1")
		cmd := exec.CommandContext(ctx, s.ffmpegPath, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		var output bytes.Buffer
		cmd.Stdout = &output
		if err := cmd.Run(); err == nil && output.Len() > 0 {
			return output.Bytes(), nil
		} else if kind == "video" {
			if err == nil {
				err = fmt.Errorf("ffmpeg produced no frame")
			}
			return nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
	}
	if kind == "video" {
		return nil, fmt.Errorf("FFmpeg is required to extract video frames")
	}
	return makeImageThumbnail(sourcePath, width, height)
}

func makeImageThumbnail(path string, maxWidth, maxHeight int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > maxImagePixels {
		return nil, fmt.Errorf("image dimensions are unsupported")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	source, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	width, height := config.Width, config.Height
	if width > maxWidth || height > maxHeight {
		ratio := min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
		width = max(1, int(float64(width)*ratio))
		height = max(1, int(float64(height)*ratio))
	}
	thumb := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(thumb, thumb.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	sourceBounds := source.Bounds()
	for y := 0; y < height; y++ {
		sy := sourceBounds.Min.Y + y*sourceBounds.Dy()/height
		for x := 0; x < width; x++ {
			sx := sourceBounds.Min.X + x*sourceBounds.Dx()/width
			r, g, b, a := source.At(sx, sy).RGBA()

			thumb.SetRGBA(x, y, color.RGBA{
				R: uint8((r + (0xffff - a)) >> 8),
				G: uint8((g + (0xffff - a)) >> 8),
				B: uint8((b + (0xffff - a)) >> 8),
				A: 0xff,
			})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, thumb, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
