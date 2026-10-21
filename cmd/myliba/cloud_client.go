package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type cloudChunk struct {
	ID   string `json:"id"`
	Size int    `json:"size"`
}

type cloudAssetPart struct {
	Name      string       `json:"name"`
	Duration  float64      `json:"duration,omitempty"`
	Size      int64        `json:"size"`
	PlainHash string       `json:"plainHash"`
	Chunks    []cloudChunk `json:"chunks"`
}

type cloudAsset struct {
	ID      string            `json:"assetId"`
	Kind    string            `json:"kind"`
	Spec    string            `json:"spec"`
	Codec   string            `json:"codec,omitempty"`
	DataKey string            `json:"dataKey"`
	Size    int64             `json:"size"`
	Parts   []cloudAssetPart  `json:"parts"`
	Local   []indexedLocation `json:"local,omitempty"`
}

type cloudManifest struct {
	Name        string            `json:"name"`
	Size        int64             `json:"size"`
	Modified    string            `json:"modified"`
	History     bool              `json:"history"`
	SourceID    string            `json:"sourceId,omitempty"`
	PlainHash   string            `json:"plainHash"`
	DataKey     string            `json:"dataKey"`
	Chunks      []cloudChunk      `json:"chunks"`
	Kind        string            `json:"kind,omitempty"`
	Tags        []string          `json:"tags,omitempty"`
	Local       []indexedLocation `json:"local,omitempty"`
	Assets      []cloudAsset      `json:"assets,omitempty"`
	ContentSize int64             `json:"contentSize,omitempty"`
	IndexOnly   bool              `json:"indexOnly,omitempty"`
	LocalIndex  *fileIndexView    `json:"localIndex,omitempty"`
}

type cloudFileView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	OriginalSize int64    `json:"originalSize"`
	Modified     string   `json:"modified"`
	History      bool     `json:"history"`
	SourceID     string   `json:"sourceId,omitempty"`
	Chunks       int      `json:"chunks"`
	RevisionID   string   `json:"revisionId"`
	MinReplicas  int      `json:"minReplicas"`
	Variants     []string `json:"variants"`
}

type cloudClient struct {
	runtime       *cloudRuntime
	indexer       *server
	assetMu       sync.Mutex
	localDigestMu sync.Mutex
	localDigests  map[string]string
	queueMu       sync.Mutex
	assetPending  map[string]bool
	assetWake     chan struct{}
	keyMu         sync.RWMutex
	key           [32]byte
	jobsMu        sync.Mutex
	jobs          map[string]string
}

func newCloudClient(runtime *cloudRuntime) (*cloudClient, error) {
	keyPath := filepath.Join(runtime.disk.dir, "recovery.key")
	var key [32]byte
	data, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		if _, err = rand.Read(key[:]); err != nil {
			return nil, err
		}
		file, createErr := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, createErr
		}
		_, err = file.Write(key[:])
		closeErr := file.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	} else if len(data) != 32 {
		return nil, errors.New("invalid cloud recovery key")
	} else {
		copy(key[:], data)
	}
	return &cloudClient{runtime: runtime, key: key, jobs: map[string]string{},
		localDigests: map[string]string{},
		assetPending: map[string]bool{}, assetWake: make(chan struct{}, 1)}, nil
}

func (c *cloudClient) libraryID() string {
	c.keyMu.RLock()
	defer c.keyMu.RUnlock()
	return cloudHash(c.key[:])
}

func (c *cloudClient) recovery(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	if r.Method == http.MethodGet {
		c.keyMu.RLock()
		key := hex.EncodeToString(c.key[:])
		c.keyMu.RUnlock()
		writeJSON(w, 200, map[string]string{"recoveryKey": key, "libraryId": c.libraryID()})
		return
	}
	var input struct {
		RecoveryKey string `json:"recoveryKey"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "无效的恢复密钥")
		return
	}
	raw, err := hex.DecodeString(strings.TrimSpace(input.RecoveryKey))
	if err != nil || len(raw) != 32 {
		writeError(w, 400, "恢复密钥应为 64 位十六进制")
		return
	}
	// A fresh client may have already synchronized opaque records from the Hub
	// before the owner imports the key. Only records readable with the current
	// key and referencing content blocks prove that this client has an existing
	// cloud library to protect. Local-only indexes can be republished with the
	// imported key.
	records, err := c.contentRecords()
	if err != nil {
		writeError(w, 500, "无法读取云层索引")
		return
	}
	if len(records) != 0 {
		writeError(w, 409, "此设备已有此文集的云层索引，不能直接替换密钥")
		return
	}
	if err := os.WriteFile(filepath.Join(c.runtime.disk.dir, "recovery.key"), raw, 0600); err != nil {
		writeError(w, 500, "无法保存恢复密钥")
		return
	}
	c.keyMu.Lock()
	copy(c.key[:], raw)
	c.keyMu.Unlock()
	c.localDigestMu.Lock()
	c.localDigests = map[string]string{}
	c.localDigestMu.Unlock()
	if c.indexer != nil {
		go c.indexer.queueAllLocalIndexes()
	}
	writeJSON(w, 200, map[string]string{"libraryId": c.libraryID()})
}

func localCloudMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Host != "127.0.0.1:4317" || r.Header.Get("X-Myliba-Local") != "1" ||
		(r.Method != http.MethodGet && r.Header.Get("Origin") != "http://127.0.0.1:4317") {
		writeError(w, http.StatusForbidden, "仅允许本机页面执行云层操作")
		return false
	}
	return true
}

func cloudAAD(record cloudRecord, index, size int) []byte {
	return []byte(fmt.Sprintf("myliba-cloud-v1|%s|%s|%s|%d|%d", record.LibraryID, record.ObjectID, record.RevisionID, index, size))
}

func cloudNonce(index int) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], uint64(index))
	return nonce
}

func cloudRecordAAD(record cloudRecord) []byte {
	if record.Format < 2 {
		return []byte(record.LibraryID + "|" + record.ObjectID + "|" + record.RevisionID)
	}
	if record.Format == 2 {
		raw, _ := json.Marshal(struct {
			Format     int      `json:"format"`
			LibraryID  string   `json:"libraryId"`
			ObjectID   string   `json:"objectId"`
			RevisionID string   `json:"revisionId"`
			AuthorID   string   `json:"authorId"`
			CreatedAt  string   `json:"createdAt"`
			BlockIDs   []string `json:"blockIds"`
		}{record.Format, record.LibraryID, record.ObjectID, record.RevisionID, record.AuthorID, record.CreatedAt, record.BlockIDs})
		return raw
	}
	raw, _ := json.Marshal(struct {
		Format      int      `json:"format"`
		KeepHistory bool     `json:"keepHistory"`
		LibraryID   string   `json:"libraryId"`
		ObjectID    string   `json:"objectId"`
		RevisionID  string   `json:"revisionId"`
		AuthorID    string   `json:"authorId"`
		CreatedAt   string   `json:"createdAt"`
		BlockIDs    []string `json:"blockIds"`
	}{record.Format, record.KeepHistory, record.LibraryID, record.ObjectID, record.RevisionID, record.AuthorID, record.CreatedAt, record.BlockIDs})
	return raw
}

func (c *cloudClient) sealManifest(record cloudRecord, manifest cloudManifest) (cloudRecord, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return record, err
	}
	c.keyMu.RLock()
	block, err := aes.NewCipher(c.key[:])
	c.keyMu.RUnlock()
	if err != nil {
		return record, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return record, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return record, err
	}
	aad := cloudRecordAAD(record)
	record.Nonce = hex.EncodeToString(nonce)
	record.Sealed = hex.EncodeToString(aead.Seal(nil, nonce, raw, aad))
	return record, nil
}

func (c *cloudClient) openManifest(record cloudRecord) (cloudManifest, error) {
	var manifest cloudManifest
	if record.LibraryID != c.libraryID() {
		return manifest, errors.New("different library")
	}
	nonce, err := hex.DecodeString(record.Nonce)
	if err != nil {
		return manifest, err
	}
	sealed, err := hex.DecodeString(record.Sealed)
	if err != nil {
		return manifest, err
	}
	c.keyMu.RLock()
	block, err := aes.NewCipher(c.key[:])
	c.keyMu.RUnlock()
	if err != nil {
		return manifest, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return manifest, err
	}
	aad := cloudRecordAAD(record)
	raw, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return manifest, err
	}
	err = json.Unmarshal(raw, &manifest)
	if err != nil {
		return manifest, err
	}
	if manifest.IndexOnly {
		if len(record.BlockIDs) != 0 || manifest.LocalIndex == nil || manifest.LocalIndex.FileID != record.ObjectID ||
			manifest.LocalIndex.Original.Cloud != nil || manifest.LocalIndex.Original.Size < 0 ||
			(manifest.LocalIndex.RevisionID != "" && manifest.LocalIndex.RevisionID != record.RevisionID) {
			return manifest, errors.New("invalid local file index")
		}
		index := manifest.LocalIndex
		total := index.Original.Size
		for _, location := range index.Original.Local {
			if location.NodeID != record.AuthorID || location.Path == "" {
				return manifest, errors.New("invalid local source location")
			}
		}
		seen := map[string]bool{}
		for _, asset := range index.Assets {
			if !validCloudHash(asset.ID) || seen[asset.ID] || asset.Content.Cloud != nil || asset.Content.Size < 0 ||
				total > (1<<63-1)-asset.Content.Size {
				return manifest, errors.New("invalid local media asset")
			}
			seen[asset.ID] = true
			total += asset.Content.Size
			for _, location := range asset.Content.Local {
				if location.NodeID != record.AuthorID || location.Path == "" {
					return manifest, errors.New("invalid local media location")
				}
			}
		}
		if total != index.ContentSize {
			return manifest, errors.New("invalid local file size")
		}
		return manifest, nil
	}
	position := 0
	validateChunks := func(chunks []cloudChunk, expectedSize int64) bool {
		var size int64
		for _, chunk := range chunks {
			if position >= len(record.BlockIDs) || chunk.ID != record.BlockIDs[position] ||
				!validCloudHash(chunk.ID) || chunk.Size <= 0 || chunk.Size > cloudChunkSize {
				return false
			}
			position++
			size += int64(chunk.Size)
		}
		return size == expectedSize
	}
	if manifest.Size < 0 || !validateChunks(manifest.Chunks, manifest.Size) {
		return manifest, errors.New("invalid original chunk index")
	}
	total := manifest.Size
	seenAssets := map[string]bool{}
	for _, asset := range manifest.Assets {
		assetKey, keyErr := hex.DecodeString(asset.DataKey)
		if !validCloudHash(asset.ID) || keyErr != nil || len(assetKey) != 32 || seenAssets[asset.ID] || asset.Size < 0 || len(asset.Parts) == 0 ||
			(asset.Kind != "thumbnail" && asset.Kind != "video") {
			return manifest, errors.New("invalid asset index")
		}
		seenAssets[asset.ID] = true
		var assetSize int64
		seenParts := map[string]bool{}
		for _, part := range asset.Parts {
			if part.Name == "" || part.Name == "." || part.Name == ".." || filepath.Base(part.Name) != part.Name ||
				!validCloudHash(part.PlainHash) || seenParts[part.Name] || part.Size < 0 ||
				!validateChunks(part.Chunks, part.Size) || assetSize > (1<<63-1)-part.Size {
				return manifest, errors.New("invalid asset part index")
			}
			seenParts[part.Name] = true
			assetSize += part.Size
		}
		if assetSize != asset.Size || total > (1<<63-1)-asset.Size {
			return manifest, errors.New("invalid asset size")
		}
		total += asset.Size
	}
	if position != len(record.BlockIDs) {
		return manifest, errors.New("invalid chunk index")
	}
	if manifest.ContentSize == 0 && len(manifest.Assets) == 0 {
		manifest.ContentSize = manifest.Size // older catalogs only contain an original
	} else if manifest.ContentSize != total {
		return manifest, errors.New("invalid content size")
	}
	return manifest, nil
}

func (c *cloudClient) loadRecords() ([]cloudRecord, error) {
	ids, err := c.runtime.disk.list("catalog")
	if err != nil {
		return nil, err
	}
	records := []cloudRecord{}
	for _, id := range ids {
		if !c.runtime.disk.valid("catalog", id) {
			continue
		}
		file, err := c.runtime.disk.get("catalog", id)
		if err != nil {
			return nil, err
		}
		var record cloudRecord
		err = json.NewDecoder(io.LimitReader(file, cloudMaxRecordSize)).Decode(&record)
		file.Close()
		if err != nil {
			return nil, err
		}
		if record.LibraryID == c.libraryID() {
			records = append(records, record)
		}
	}
	return records, nil
}

func (c *cloudClient) validRecords() ([]cloudRecord, error) {
	records, err := c.loadRecords()
	if err != nil {
		return nil, err
	}
	valid := make([]cloudRecord, 0, len(records))
	for _, record := range records {
		if _, err := c.openManifest(record); err == nil {
			valid = append(valid, record)
		}
	}
	return valid, nil
}

func latestCloudRecords(records []cloudRecord) []cloudRecord {
	byObject := map[string]cloudRecord{}
	for _, record := range records {
		previous, ok := byObject[record.ObjectID]
		if !ok || record.CreatedAt > previous.CreatedAt ||
			(record.CreatedAt == previous.CreatedAt && record.RevisionID > previous.RevisionID) {
			byObject[record.ObjectID] = record
		}
	}
	result := make([]cloudRecord, 0, len(byObject))
	for _, record := range byObject {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt > result[j].CreatedAt })
	return result
}

func (c *cloudClient) contentRecords() ([]cloudRecord, error) {
	records, err := c.validRecords()
	if err != nil {
		return nil, err
	}
	result := make([]cloudRecord, 0, len(records))
	for _, record := range records {
		manifest, err := c.openManifest(record)
		if err == nil && !manifest.IndexOnly {
			result = append(result, record)
		}
	}
	return result, nil
}

func (c *cloudClient) list(w http.ResponseWriter, r *http.Request) {
	records, err := c.contentRecords()
	if err != nil {
		writeError(w, 500, "无法读取云层索引")
		return
	}
	items := []cloudFileView{}
	for _, record := range latestCloudRecords(records) {
		manifest, err := c.openManifest(record)
		if err != nil {
			continue
		}
		minReplicas := 0
		variants := []string{}
		for _, asset := range manifest.Assets {
			if asset.Kind == "video" {
				variants = append(variants, asset.Spec)
			}
		}
		sort.Strings(variants)
		if len(record.BlockIDs) == 0 {
			minReplicas = 1
		}
		for index, id := range record.BlockIDs {
			count := len(c.runtime.blockLocations(id))
			if c.runtime.disk.has("blocks", id) && count == 0 {
				count = 1
			}
			if index == 0 || count < minReplicas {
				minReplicas = count
			}
		}
		items = append(items, cloudFileView{
			ID: record.ObjectID, Name: manifest.Name, Size: manifest.ContentSize, OriginalSize: manifest.Size,
			Modified: manifest.Modified, History: manifest.History,
			SourceID: manifest.SourceID, Chunks: len(record.BlockIDs),
			RevisionID: record.RevisionID, MinReplicas: minReplicas,
			Variants: variants,
		})
	}
	c.jobsMu.Lock()
	jobs := make(map[string]string, len(c.jobs))
	for id, status := range c.jobs {
		jobs[id] = status
	}
	c.jobsMu.Unlock()
	writeJSON(w, 200, map[string]any{"files": items, "jobs": jobs, "nodeId": c.runtime.id, "libraryId": c.libraryID(), "peers": c.runtime.activePeers(), "directEndpoint": c.runtime.endpoint})
}

func (c *cloudClient) upload(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	id := r.PathValue("id")
	var input struct {
		History *bool `json:"history"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		writeError(w, 400, "上传选项无效")
		return
	}
	history := false
	if _, previous, err := c.findRecord(id); err == nil {
		history = previous.History
	}
	if input.History != nil {
		history = *input.History
	}
	var path string
	if err := c.runtime.db.QueryRow(`SELECT path FROM resources WHERE id=? AND available=1`, id).Scan(&path); err != nil {
		writeError(w, 404, "本地资源不存在")
		return
	}
	c.jobsMu.Lock()
	if c.jobs[id] == "上传中" {
		c.jobsMu.Unlock()
		writeError(w, 409, "文件正在上传")
		return
	}
	c.jobs[id] = "上传中"
	c.jobsMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		err := c.uploadFile(ctx, id, path, history)
		c.jobsMu.Lock()
		if err != nil {
			c.jobs[id] = "失败：" + err.Error()
		} else {
			c.jobs[id] = "已加入云层"
		}
		c.jobsMu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "上传中"})
}

func (c *cloudClient) uploadFile(ctx context.Context, objectID, path string, history bool) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	initial, err := file.Stat()
	if err != nil || !initial.Mode().IsRegular() {
		return errors.New("文件不可用")
	}
	var dataKey [32]byte
	if _, err := rand.Read(dataKey[:]); err != nil {
		return err
	}
	block, err := aes.NewCipher(dataKey[:])
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	record := cloudRecord{
		Format: 3, KeepHistory: history, LibraryID: c.libraryID(), ObjectID: objectID, RevisionID: newID(),
		AuthorID: c.runtime.id, CreatedAt: now(), BlockIDs: []string{},
	}
	manifest := cloudManifest{
		Name: initial.Name(), Size: initial.Size(), Modified: initial.ModTime().UTC().Format(time.RFC3339Nano),
		SourceID: objectID, DataKey: hex.EncodeToString(dataKey[:]), Chunks: []cloudChunk{}, History: history,
		Kind: fileKind(path), ContentSize: initial.Size(),
	}
	if c.runtime.db != nil {
		var rootID string
		if err := c.runtime.db.QueryRow(`SELECT root_id FROM resources WHERE id=? AND path=?`, objectID, path).Scan(&rootID); err == nil {
			manifest.Local = []indexedLocation{{NodeID: c.runtime.id, StorageID: rootID, Path: path}}
		}
		tags, err := c.runtime.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=? ORDER BY t.name`, objectID)
		if err == nil {
			for tags.Next() {
				var tag string
				if tags.Scan(&tag) == nil {
					manifest.Tags = append(manifest.Tags, tag)
				}
			}
			tags.Close()
		}
	}
	plainHash := sha256.New()
	buffer := make([]byte, cloudChunkSize)
	for index := 0; ; index++ {
		n, readErr := io.ReadFull(file, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return readErr
		}
		if n == 0 {
			break
		}
		plainHash.Write(buffer[:n])
		ciphertext := aead.Seal(nil, cloudNonce(index), buffer[:n], cloudAAD(record, index, n))
		id := cloudHash(ciphertext)
		if err := c.runtime.disk.put("blocks", id, bytes.NewReader(ciphertext), cloudMaxBlobSize); err != nil {
			return err
		}
		manifest.Chunks = append(manifest.Chunks, cloudChunk{ID: id, Size: n})
		record.BlockIDs = append(record.BlockIDs, id)
		if readErr != nil {
			break
		}
	}
	final, err := os.Stat(path)
	if err != nil || final.Size() != initial.Size() || !final.ModTime().Equal(initial.ModTime()) {
		return errors.New("上传期间文件发生变化，请重试")
	}
	manifest.PlainHash = hex.EncodeToString(plainHash.Sum(nil))
	if err := c.appendGeneratedAssets(ctx, &record, &manifest, preprocessVersion(initial)); err != nil {
		return err
	}
	record, err = c.sealManifest(record, manifest)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(raw) > cloudMaxRecordSize {
		return errors.New("文件块清单过大")
	}
	if err := c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		return err
	}
	// The local write commits immediately. Unreachable peers remain background
	// replication work and never prevent the user from using the file.
	c.replicateRecord(ctx, record)
	if err := c.syncNewAssets(ctx, objectID); err != nil {
		return err
	}
	return nil
}

func (c *cloudClient) replicateRecord(ctx context.Context, record cloudRecord) {
	c.replicateBlocks(ctx, record.BlockIDs)
}

func (c *cloudClient) replicateBlocks(ctx context.Context, blockIDs []string) {
	_, token, err := c.runtime.credentials()
	if err != nil || token == "" {
		return
	}
	peers := c.runtime.activePeers()
	for _, id := range blockIDs {
		file, err := c.runtime.disk.get("blocks", id)
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(file, cloudMaxBlobSize+1))
		file.Close()
		if err != nil {
			continue
		}
		copies := 1
		for _, peer := range rankedCloudPeers(id, peers) {
			if copies >= 3 {
				break
			}
			if err := c.runtime.put(ctx, peer.Endpoint, "blocks", id, token, data); err == nil {
				copies++
			}
		}
	}
}

func (c *cloudClient) findRecord(objectID string) (cloudRecord, cloudManifest, error) {
	records, err := c.contentRecords()
	if err != nil {
		return cloudRecord{}, cloudManifest{}, err
	}
	for _, record := range latestCloudRecords(records) {
		if record.ObjectID != objectID {
			continue
		}
		manifest, err := c.openManifest(record)
		return record, manifest, err
	}
	return cloudRecord{}, cloudManifest{}, sql.ErrNoRows
}

func (c *cloudClient) versions(w http.ResponseWriter, r *http.Request) {
	objectID := r.PathValue("id")
	records, err := c.historyRecords(objectID)
	if err != nil {
		writeError(w, 404, "云层文件不存在")
		return
	}
	items := []cloudFileView{}
	for _, record := range records {
		manifest, err := c.openManifest(record)
		if err != nil {
			continue
		}
		items = append(items, cloudFileView{ID: objectID, Name: manifest.Name, Size: manifest.ContentSize,
			OriginalSize: manifest.Size, Modified: manifest.Modified, History: true,
			Chunks: len(record.BlockIDs), RevisionID: record.RevisionID})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Modified > items[j].Modified })
	writeJSON(w, 200, items)
}

func (c *cloudClient) versionRecord(objectID, revisionID string) (cloudRecord, cloudManifest, error) {
	records, err := c.historyRecords(objectID)
	if err != nil {
		return cloudRecord{}, cloudManifest{}, err
	}
	for _, record := range records {
		if record.RevisionID == revisionID {
			manifest, err := c.openManifest(record)
			return record, manifest, err
		}
	}
	return cloudRecord{}, cloudManifest{}, sql.ErrNoRows
}

func (c *cloudClient) historyRecords(objectID string) ([]cloudRecord, error) {
	records, err := c.contentRecords()
	if err != nil {
		return nil, err
	}
	ordered := make([]cloudRecord, 0)
	for _, record := range records {
		if record.ObjectID == objectID {
			ordered = append(ordered, record)
		}
	}
	if len(ordered) == 0 {
		return nil, sql.ErrNoRows
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt == ordered[j].CreatedAt {
			return ordered[i].RevisionID > ordered[j].RevisionID
		}
		return ordered[i].CreatedAt > ordered[j].CreatedAt
	})
	result := make([]cloudRecord, 0, len(ordered))
	seenRevisions := map[string]bool{}
	for _, record := range ordered {
		if seenRevisions[record.RevisionID] {
			continue
		}
		seenRevisions[record.RevisionID] = true
		manifest, err := c.openManifest(record)
		if err != nil || !manifest.History {
			break
		}
		result = append(result, record)
	}
	return result, nil
}

func (c *cloudClient) fetchBlock(ctx context.Context, id string, index int) ([]byte, error) {
	if file, err := c.runtime.disk.get("blocks", id); err == nil {
		data, readErr := io.ReadAll(io.LimitReader(file, cloudMaxBlobSize+1))
		file.Close()
		if readErr == nil && cloudHash(data) == id {
			return data, nil
		}
	}
	_, token, err := c.runtime.credentials()
	if err != nil {
		return nil, err
	}
	peers := c.runtime.blockLocations(id)
	filtered := make([]cloudNode, 0, len(peers))
	seen := map[string]bool{}
	for _, peer := range peers {
		if peer.ID != c.runtime.id && peer.Endpoint != "" && !seen[peer.Endpoint] {
			filtered = append(filtered, peer)
			seen[peer.Endpoint] = true
		}
	}
	// Inventories are hints and can be stale. A node that recently came back
	// online may hold the block even before its inventory is refreshed.
	for _, peer := range rankedCloudPeers(id, c.runtime.activePeers()) {
		if peer.ID != c.runtime.id && peer.Endpoint != "" && !seen[peer.Endpoint] {
			filtered = append(filtered, peer)
			seen[peer.Endpoint] = true
		}
	}
	peers = filtered
	if len(peers) > 0 {
		rotate := index % len(peers)
		peers = append(peers[rotate:], peers[:rotate]...)
	}
	for _, peer := range peers {
		data, err := c.runtime.get(ctx, peer.Endpoint, "blocks", id, token, cloudMaxBlobSize)
		if err == nil {
			return data, nil
		}
	}
	return nil, errors.New("没有可用的文件块副本")
}

// A cloud location and a local path may describe the same logical revision.
// Check the source bytes before choosing the local path, since a file can change
// without the library having been rescanned yet.
func (c *cloudClient) localOriginal(record cloudRecord, manifest cloudManifest) (*os.File, os.FileInfo) {
	if c.runtime.db == nil || manifest.SourceID == "" || manifest.SourceID != record.ObjectID || !validCloudHash(manifest.PlainHash) {
		return nil, nil
	}
	var path, modified string
	var size int64
	var available bool
	if err := c.runtime.db.QueryRow(`SELECT path,size,modified_at,available FROM resources WHERE id=?`, manifest.SourceID).
		Scan(&path, &size, &modified, &available); err != nil || !available || size != manifest.Size || modified != manifest.Modified {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != manifest.Size ||
		info.ModTime().UTC().Format(time.RFC3339Nano) != manifest.Modified {
		file.Close()
		return nil, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil || hex.EncodeToString(hash.Sum(nil)) != manifest.PlainHash {
		file.Close()
		return nil, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, nil
	}
	return file, info
}

func (c *cloudClient) download(w http.ResponseWriter, r *http.Request) {
	var record cloudRecord
	var manifest cloudManifest
	var err error
	if revisionID := r.PathValue("revision"); revisionID != "" {
		record, manifest, err = c.versionRecord(r.PathValue("id"), revisionID)
	} else {
		record, manifest, err = c.findRecord(r.PathValue("id"))
	}
	if err != nil {
		writeError(w, 404, "云层文件不存在")
		return
	}
	name := manifest.Name
	contentType := mime.TypeByExtension(filepath.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if file, info := c.localOriginal(record, manifest); file != nil {
		defer file.Close()
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		http.ServeContent(w, r, manifest.Name, info.ModTime(), file)
		return
	}
	dataKey, err := hex.DecodeString(manifest.DataKey)
	if err != nil || len(dataKey) != 32 {
		writeError(w, 500, "文件密钥无效")
		return
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		writeError(w, 500, "文件密钥无效")
		return
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		writeError(w, 500, "文件密钥无效")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Length", fmt.Sprint(manifest.Size))
	written := false
	for start := 0; start < len(manifest.Chunks); start += 4 {
		end := start + 4
		if end > len(manifest.Chunks) {
			end = len(manifest.Chunks)
		}
		type result struct {
			plain []byte
			err   error
		}
		results := make([]result, end-start)
		var group sync.WaitGroup
		for index := start; index < end; index++ {
			index := index
			chunk := manifest.Chunks[index]
			group.Add(1)
			go func() {
				defer group.Done()
				data, err := c.fetchBlock(r.Context(), chunk.ID, index)
				if err != nil {
					results[index-start].err = err
					return
				}
				plain, err := aead.Open(nil, cloudNonce(index), data, cloudAAD(record, index, chunk.Size))
				results[index-start] = result{plain: plain, err: err}
			}()
		}
		group.Wait()
		for _, item := range results {
			if item.err != nil {
				if !written {
					w.Header().Del("Content-Length")
					w.Header().Del("Content-Disposition")
					writeError(w, http.StatusServiceUnavailable, "文件块暂不可用或校验失败")
				}
				return
			}
			if _, err := w.Write(item.plain); err != nil {
				return
			}
			written = true
		}
	}
	if !written {
		w.WriteHeader(http.StatusOK)
	}
}
