package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const demandSegmentSeconds = 4.0
const demandMaxBytes = 16 << 20

var demandSlots = make(chan struct{}, 2)
var demandCache = struct {
	sync.Mutex
	entries map[string][]byte
	order   []string
	size    int
}{entries: map[string][]byte{}}

type limitedVideoBuffer struct{ bytes.Buffer }

func (b *limitedVideoBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > demandMaxBytes {
		return 0, errors.New("video segment exceeds limit")
	}
	return b.Buffer.Write(p)
}

func demandKey(sourceKey, quality string, index int) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", sourceKey, quality, index)))
	return hex.EncodeToString(digest[:])
}

func demandEncode(ctx context.Context, s *server, input, key, quality string, index int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheKey := demandKey(key, quality, index)
	demandCache.Lock()
	cached := demandCache.entries[cacheKey]
	demandCache.Unlock()
	if cached != nil {
		return cached, nil
	}
	select {
	case demandSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-demandSlots }()
	demandCache.Lock()
	cached = demandCache.entries[cacheKey]
	demandCache.Unlock()
	if cached != nil {
		return cached, nil
	}
	q := videoQualities[quality]
	if q.width == 0 || s.ffmpegPath == "" {
		return nil, errors.New("视频转码不可用")
	}
	filter := fmt.Sprintf("fps=30,scale=w='min(iw,%d)':h='min(ih,%d)':force_original_aspect_ratio=decrease:force_divisible_by=2", q.width, q.height)
	encodeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(encodeCtx, s.ffmpegPath, "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(float64(index)*demandSegmentSeconds, 'f', 3, 64), "-i", input,
		"-t", "4", "-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-vf", filter,
		"-c:v", "libx264", "-threads", "2", "-preset", "veryfast", "-profile:v", "baseline", "-level:v", "4.1", "-pix_fmt", "yuv420p",
		"-b:v", q.bitrate, "-maxrate", q.bitrate, "-bufsize", q.bitrate, "-g", "120", "-keyint_min", "120", "-sc_threshold", "0",
		"-c:a", "aac", "-b:a", q.audioBitrate, "-ac", "2", "-movflags", "empty_moov+default_base_moof+frag_keyframe", "-f", "mp4", "pipe:1")
	var out limitedVideoBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("视频片段转码失败")
	}
	data := append([]byte(nil), out.Bytes()...)
	if _, _, err := splitDemandMP4(data); err != nil {
		return nil, err
	}
	demandCache.Lock()
	if demandCache.entries[cacheKey] == nil {
		for demandCache.size+len(data) > 64<<20 && len(demandCache.order) > 0 {
			oldest := demandCache.order[0]
			demandCache.order = demandCache.order[1:]
			demandCache.size -= len(demandCache.entries[oldest])
			delete(demandCache.entries, oldest)
		}
		demandCache.entries[cacheKey] = data
		demandCache.order = append(demandCache.order, cacheKey)
		demandCache.size += len(data)
	}
	demandCache.Unlock()
	return data, nil
}

func splitDemandMP4(data []byte) ([]byte, []byte, error) {
	firstFragment := -1
	hasData := false
	for offset := 0; offset+8 <= len(data); {
		size := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		header := 8
		if size == 1 {
			if offset+16 > len(data) {
				break
			}
			n := binary.BigEndian.Uint64(data[offset+8 : offset+16])
			if n > uint64(len(data)) {
				break
			}
			size = int64(n)
			header = 16
		}
		if size == 0 {
			size = int64(len(data) - offset)
		}
		if size < int64(header) || size > int64(len(data)-offset) {
			break
		}
		kind := string(data[offset+4 : offset+8])
		if kind == "moof" && firstFragment < 0 {
			firstFragment = offset
		}
		if kind == "mdat" && firstFragment >= 0 {
			hasData = true
		}
		if kind == "mfra" && firstFragment > 0 && hasData {
			return data[:firstFragment], data[firstFragment:offset], nil
		}
		offset += int(size)
		if offset == len(data) && firstFragment > 0 && hasData {
			return data[:firstFragment], data[firstFragment:offset], nil
		}
	}
	return nil, nil, errors.New("无效的视频片段")
}

func demandManifest(ctx context.Context, s *server, input, key, quality string) (videoManifest, error) {
	if s.ffprobePath == "" || s.ffmpegPath == "" {
		return videoManifest{}, errors.New("视频分段转码需要 FFmpeg 和 FFprobe")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case demandSlots <- struct{}{}:
	case <-probeCtx.Done():
		return videoManifest{}, probeCtx.Err()
	}
	output, err := exec.CommandContext(probeCtx, s.ffprobePath, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", input).Output()
	<-demandSlots
	duration, parseErr := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil || parseErr != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 86400 {
		return videoManifest{}, errors.New("无法读取视频时长")
	}
	data, err := demandEncode(ctx, s, input, key, quality, 0)
	if err != nil {
		return videoManifest{}, err
	}
	init, _, err := splitDemandMP4(data)
	if err != nil {
		return videoManifest{}, err
	}
	manifest := videoManifest{Status: "ready", Mode: "ondemand", Codec: mp4Codec(init), Duration: duration, Segments: []float64{}}
	for position := 0.0; position < duration; position += demandSegmentSeconds {
		manifest.Segments = append(manifest.Segments, math.Min(demandSegmentSeconds, duration-position))
	}
	return manifest, nil
}

func serveDemandPart(w http.ResponseWriter, r *http.Request, s *server, input, key, quality string, index int, init bool) {
	if index < 0 || index > 21600 {
		writeError(w, 400, "视频片段编号无效")
		return
	}
	data, err := demandEncode(r.Context(), s, input, key, quality, index)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 503, err.Error())
		}
		return
	}
	head, part, err := splitDemandMP4(data)
	if err != nil {
		writeError(w, 500, "视频片段无效")
		return
	}
	name := "segment.m4s"
	contentType := "video/iso.segment"
	if init {
		part = head
		name = "init.mp4"
		contentType = "video/mp4"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(part))
}

// Expose cloud plaintext only over an ephemeral, capability-protected loopback
// endpoint, so FFmpeg can seek encrypted blocks without materializing a file.
func (c *cloudClient) demandInput(ctx context.Context, id string) (string, string, func(), error) {
	record, manifest, err := c.findRecord(id)
	if err != nil {
		return "", "", nil, err
	}
	key := record.LibraryID + ":" + record.ObjectID + ":" + record.RevisionID
	if len(manifest.Chunks) == 0 {
		if file, _ := c.localOriginal(record, manifest); file != nil {
			path := file.Name()
			file.Close()
			return path, key, func() {}, nil
		}
		return "", "", nil, errors.New("云层原片不可用")
	}
	dataKey, err := hex.DecodeString(manifest.DataKey)
	if err != nil || len(dataKey) != 32 {
		return "", "", nil, errors.New("文件密钥无效")
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return "", "", nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", nil, err
	}
	if _, err = newCloudRangeReader(ctx, c, record, manifest, aead); err != nil {
		return "", "", nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}
	token, err := randomToken()
	if err != nil {
		listener.Close()
		return "", "", nil, err
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+token || (r.Method != "GET" && r.Method != "HEAD") {
			http.NotFound(w, r)
			return
		}
		reader, e := newCloudRangeReader(r.Context(), c, record, manifest, aead)
		if e != nil {
			http.Error(w, "invalid file", 500)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, manifest.Name, time.Time{}, reader)
	})}
	go func() { _ = server.Serve(listener) }()
	return "http://" + listener.Addr().String() + "/" + token, key, func() { _ = server.Close() }, nil
}

var _ io.Writer = (*limitedVideoBuffer)(nil)
