package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type generatedAssetLocation struct {
	id, kind, spec, codec, storageID, path string
}

func (c *cloudClient) generatedAssetLocations(objectID, versionKey string) ([]generatedAssetLocation, error) {
	if c.runtime.db == nil {
		return nil, nil
	}
	rows, err := c.runtime.db.Query(`SELECT a.id,a.kind,a.spec,a.codec,l.storage_id,l.path
		FROM file_assets a JOIN file_asset_locations l ON l.asset_id=a.id
		WHERE a.resource_id=? AND a.version_key=? ORDER BY a.id,l.storage_id`, objectID, versionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []generatedAssetLocation{}
	for rows.Next() {
		var item generatedAssetLocation
		if err := rows.Scan(&item.id, &item.kind, &item.spec, &item.codec, &item.storageID, &item.path); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (c *cloudClient) appendCloudPart(ctx context.Context, record *cloudRecord, aead cipher.AEAD, path, name string, duration float64) (cloudAssetPart, error) {
	part := cloudAssetPart{Name: name, Duration: duration, Chunks: []cloudChunk{}}
	file, err := os.Open(path)
	if err != nil {
		return part, err
	}
	defer file.Close()
	initial, err := file.Stat()
	if err != nil || !initial.Mode().IsRegular() {
		return part, errors.New("generated media is unavailable")
	}
	buffer := make([]byte, cloudChunkSize)
	plainHash := sha256.New()
	for {
		if err := ctx.Err(); err != nil {
			return part, err
		}
		n, readErr := io.ReadFull(file, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return part, readErr
		}
		if n == 0 {
			break
		}
		plainHash.Write(buffer[:n])
		index := len(record.BlockIDs)
		if index >= 10000 {
			return part, errors.New("too many cloud content blocks")
		}
		ciphertext := aead.Seal(nil, cloudNonce(index), buffer[:n], cloudAAD(*record, index, n))
		id := cloudHash(ciphertext)
		if err := c.runtime.disk.put("blocks", id, bytes.NewReader(ciphertext), cloudMaxBlobSize); err != nil {
			return part, err
		}
		part.Chunks = append(part.Chunks, cloudChunk{ID: id, Size: n})
		record.BlockIDs = append(record.BlockIDs, id)
		part.Size += int64(n)
		if readErr != nil {
			break
		}
	}
	final, err := os.Stat(path)
	if err != nil || final.Size() != initial.Size() || !final.ModTime().Equal(initial.ModTime()) || part.Size != initial.Size() {
		return part, errors.New("generated media changed during upload")
	}
	part.PlainHash = hex.EncodeToString(plainHash.Sum(nil))
	return part, nil
}

func (c *cloudClient) appendGeneratedAssets(ctx context.Context, record *cloudRecord, manifest *cloudManifest, versionKey string) error {
	locations, err := c.generatedAssetLocations(record.ObjectID, versionKey)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, asset := range manifest.Assets {
		seen[asset.ID] = true
	}
	for _, location := range locations {
		if seen[location.id] {
			continue
		}
		if _, err := os.Stat(location.path); err != nil {
			continue
		}
		asset := cloudAsset{ID: location.id, Kind: location.kind, Spec: location.spec,
			Codec: location.codec, Parts: []cloudAssetPart{},
			Local: []indexedLocation{{NodeID: c.runtime.id, StorageID: location.storageID, Path: location.path}}}
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return err
		}
		asset.DataKey = hex.EncodeToString(key[:])
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return err
		}
		if asset.Kind == "thumbnail" {
			part, err := c.appendCloudPart(ctx, record, aead, location.path, "thumbnail.jpg", 0)
			if err != nil {
				return err
			}
			asset.Parts = append(asset.Parts, part)
			asset.Size = part.Size
		} else if asset.Kind == "video" {
			video, err := readVideoManifest(location.path)
			if err != nil {
				continue
			}
			asset.Codec = video.Codec
			part, err := c.appendCloudPart(ctx, record, aead, filepath.Join(location.path, "init.mp4"), "init.mp4", 0)
			if err != nil {
				return err
			}
			asset.Parts = append(asset.Parts, part)
			asset.Size += part.Size
			for index, duration := range video.Segments {
				name := fmt.Sprintf("segment-%05d.m4s", index)
				part, err := c.appendCloudPart(ctx, record, aead, filepath.Join(location.path, name), name, duration)
				if err != nil {
					return err
				}
				asset.Parts = append(asset.Parts, part)
				asset.Size += part.Size
			}
		} else {
			continue
		}
		manifest.Assets = append(manifest.Assets, asset)
		manifest.ContentSize += asset.Size
		seen[asset.ID] = true
	}
	return nil
}

func (c *cloudClient) syncNewAssets(ctx context.Context, objectID string) error {
	c.assetMu.Lock()
	defer c.assetMu.Unlock()
	record, manifest, err := c.findRecord(objectID)
	if err != nil {
		return nil // local-only files have no cloud blocks to extend
	}
	local, _ := c.localOriginal(record, manifest)
	if local == nil {
		return nil // do not attach assets to an older or different source revision
	}
	local.Close()
	updated := record
	updated.BlockIDs = append([]string(nil), record.BlockIDs...)
	updated.AuthorID = c.runtime.id
	stamp := time.Now().UTC()
	if previous, err := time.Parse(time.RFC3339Nano, record.CreatedAt); err == nil && !stamp.After(previous) {
		stamp = previous.Add(time.Nanosecond)
	}
	updated.CreatedAt = stamp.Format(time.RFC3339Nano)
	before := len(updated.BlockIDs)
	beforeAssets := len(manifest.Assets)
	if err := c.appendGeneratedAssets(ctx, &updated, &manifest, versionKeyFromMetadata(manifest.Size, manifest.Modified)); err != nil {
		return err
	}
	if len(updated.BlockIDs) == before && len(manifest.Assets) == beforeAssets {
		return nil
	}
	updated, err = c.sealManifest(updated, manifest)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	if len(raw) > cloudMaxRecordSize {
		return errors.New("file index is too large")
	}
	if err := c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		return err
	}
	c.replicateBlocks(ctx, updated.BlockIDs[before:])
	return nil
}

func (c *cloudClient) queueAssetSync(objectID string) {
	if c.assetWake == nil {
		return
	}
	c.queueMu.Lock()
	c.assetPending[objectID] = true
	c.queueMu.Unlock()
	c.wakeAssetWorker()
}

func (c *cloudClient) queueLocalIndex(objectID string) {
	if c.assetWake == nil {
		return
	}
	c.queueMu.Lock()
	if _, queued := c.assetPending[objectID]; !queued {
		c.assetPending[objectID] = false
	}
	c.queueMu.Unlock()
	c.wakeAssetWorker()
}

func (c *cloudClient) wakeAssetWorker() {
	select {
	case c.assetWake <- struct{}{}:
	default:
	}
}

func (c *cloudClient) assetSyncWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.assetWake:
		case <-ticker.C:
		}
		c.queueMu.Lock()
		pending := c.assetPending
		c.assetPending = map[string]bool{}
		c.queueMu.Unlock()
		for objectID, withCloudAssets := range pending {
			attempt, cancel := context.WithTimeout(ctx, 2*time.Hour)
			err := c.publishLocalIndex(objectID)
			if err == nil && withCloudAssets {
				err = c.syncNewAssets(attempt, objectID)
			}
			cancel()
			if err != nil {
				log.Printf("cloud asset sync %s: %v", objectID, err)
				c.queueMu.Lock()
				c.assetPending[objectID] = c.assetPending[objectID] || withCloudAssets
				c.queueMu.Unlock()
			}
		}
	}
}

func cloudPart(manifest cloudManifest, assetID, name string) (cloudAsset, cloudAssetPart, int, bool) {
	index := len(manifest.Chunks)
	for _, asset := range manifest.Assets {
		for _, part := range asset.Parts {
			if asset.ID == assetID && part.Name == name {
				return asset, part, index, true
			}
			index += len(part.Chunks)
		}
	}
	return cloudAsset{}, cloudAssetPart{}, 0, false
}

func cloudPartType(asset cloudAsset, part cloudAssetPart) string {
	if asset.Kind == "thumbnail" {
		return "image/jpeg"
	}
	if part.Name == "init.mp4" {
		return "video/mp4"
	}
	return "video/iso.segment"
}

func (c *cloudClient) serveCloudPart(w http.ResponseWriter, r *http.Request, assetID, partName string) {
	record, manifest, err := c.findRecord(r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "文件不存在")
		return
	}
	asset, part, start, found := cloudPart(manifest, assetID, partName)
	if !found {
		writeError(w, 404, "文件内容不存在")
		return
	}
	w.Header().Set("Content-Type", cloudPartType(asset, part))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	for _, location := range asset.Local {
		if location.NodeID != c.runtime.id {
			continue
		}
		path := location.Path
		if asset.Kind == "video" {
			path = filepath.Join(path, partName)
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		info, statErr := file.Stat()
		if statErr == nil && info.Mode().IsRegular() && info.Size() == part.Size {
			hash := sha256.New()
			_, hashErr := io.Copy(hash, file)
			if hashErr == nil && hex.EncodeToString(hash.Sum(nil)) == part.PlainHash {
				if _, err := file.Seek(0, io.SeekStart); err == nil {
					http.ServeContent(w, r, partName, info.ModTime(), file)
					file.Close()
					return
				}
			}
		}
		file.Close()
	}
	dataKey, err := hex.DecodeString(asset.DataKey)
	if err != nil || len(dataKey) != 32 {
		writeError(w, 500, "内容密钥无效")
		return
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		writeError(w, 500, "内容密钥无效")
		return
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		writeError(w, 500, "内容密钥无效")
		return
	}
	var content bytes.Buffer
	for offset, chunk := range part.Chunks {
		ciphertext, err := c.fetchBlock(r.Context(), chunk.ID, start+offset)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "内容块暂不可用")
			return
		}
		plain, err := aead.Open(nil, cloudNonce(start+offset), ciphertext, cloudAAD(record, start+offset, chunk.Size))
		if err != nil || len(plain) != chunk.Size {
			writeError(w, http.StatusServiceUnavailable, "内容块校验失败")
			return
		}
		content.Write(plain)
	}
	plain := content.Bytes()
	hash := sha256.Sum256(plain)
	if int64(len(plain)) != part.Size || hex.EncodeToString(hash[:]) != part.PlainHash {
		writeError(w, http.StatusServiceUnavailable, "内容校验失败")
		return
	}
	http.ServeContent(w, r, partName, time.Time{}, bytes.NewReader(plain))
}

func (c *cloudClient) cloudAssetPart(w http.ResponseWriter, r *http.Request) {
	c.serveCloudPart(w, r, r.PathValue("asset"), r.PathValue("part"))
}

func (c *cloudClient) cloudVideo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "ondemand" {
		if c.indexer == nil || videoQualities[r.PathValue("quality")].width == 0 {
			writeError(w, 503, "云视频转码不可用")
			return
		}
		input, key, closeInput, err := c.demandInput(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 503, "云层原片不可用")
			return
		}
		defer closeInput()
		manifest, err := demandManifest(r.Context(), c.indexer, input, key, r.PathValue("quality"))
		if err != nil {
			if r.Context().Err() == nil {
				writeError(w, 503, err.Error())
			}
			return
		}
		writeJSON(w, 200, manifest)
		return
	}
	_, manifest, err := c.findRecord(r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "文件不存在")
		return
	}
	quality := r.PathValue("quality")
	for _, asset := range manifest.Assets {
		if asset.Kind != "video" || asset.Spec != quality || len(asset.Parts) < 2 {
			continue
		}
		video := videoManifest{Status: "ready", Codec: asset.Codec, Segments: []float64{}}
		for _, part := range asset.Parts[1:] {
			video.Segments = append(video.Segments, part.Duration)
			video.Duration += part.Duration
		}
		writeJSON(w, 200, video)
		return
	}
	writeJSON(w, 200, videoManifest{Status: "missing"})
}

func (c *cloudClient) cloudVideoPart(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("mode") == "ondemand" {
		if c.indexer == nil || videoQualities[r.PathValue("quality")].width == 0 {
			writeError(w, 503, "云视频转码不可用")
			return
		}
		index := 0
		raw := r.PathValue("segment")
		if raw != "" {
			var err error
			index, err = strconv.Atoi(raw)
			if err != nil || index < 0 || index > 21600 {
				writeError(w, 400, "视频片段编号无效")
				return
			}
		}
		input, key, closeInput, err := c.demandInput(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, 503, "云层原片不可用")
			return
		}
		defer closeInput()
		serveDemandPart(w, r, c.indexer, input, key, r.PathValue("quality"), index, raw == "")
		return
	}
	_, manifest, err := c.findRecord(r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "文件不存在")
		return
	}
	quality := r.PathValue("quality")
	partName := "init.mp4"
	if raw := r.PathValue("segment"); raw != "" {
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 || index > 99999 {
			writeError(w, 400, "视频分段编号无效")
			return
		}
		partName = fmt.Sprintf("segment-%05d.m4s", index)
	}
	for _, asset := range manifest.Assets {
		if asset.Kind == "video" && asset.Spec == quality {
			c.serveCloudPart(w, r, asset.ID, partName)
			return
		}
	}
	writeError(w, 404, "视频清晰度不存在")
}
