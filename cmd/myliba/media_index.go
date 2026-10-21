package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultMediaStoreID = "appdata"

type mediaStore struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	IsDefault bool   `json:"isDefault"`
}

func (s *server) ensureDefaultMediaStore(dataDir string) error {
	if _, err := s.db.Exec(`INSERT INTO media_stores(id,path,is_default,added_at) VALUES(?,?,1,?)
		ON CONFLICT(id) DO NOTHING`, defaultMediaStoreID, dataDir, now()); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE media_stores SET is_default=1 WHERE id=? AND NOT EXISTS
		(SELECT 1 FROM media_stores WHERE is_default=1)`, defaultMediaStoreID)
	return err
}

func (s *server) selectedMediaStore() (mediaStore, error) {
	var store mediaStore
	err := s.db.QueryRow(`SELECT id,path,is_default FROM media_stores ORDER BY is_default DESC,added_at,id LIMIT 1`).
		Scan(&store.ID, &store.Path, &store.IsDefault)
	return store, err
}

func (s *server) mediaStores(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	rows, err := s.db.Query(`SELECT id,path,is_default FROM media_stores ORDER BY is_default DESC,added_at,id`)
	if err != nil {
		writeError(w, 500, "无法读取本机存储位置")
		return
	}
	defer rows.Close()
	stores := []mediaStore{}
	for rows.Next() {
		var item mediaStore
		if err := rows.Scan(&item.ID, &item.Path, &item.IsDefault); err != nil {
			writeError(w, 500, "无法读取本机存储位置")
			return
		}
		stores = append(stores, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, 500, "无法读取本机存储位置")
		return
	}
	writeJSON(w, 200, stores)
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (s *server) addMediaStore(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	var input struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "本机存储位置无效")
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(input.Path))
	if err != nil || strings.TrimSpace(input.Path) == "" {
		writeError(w, 400, "本机存储位置无效")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		writeError(w, 400, "请选择已存在的目录")
		return
	}
	roots, err := s.db.Query(`SELECT path FROM roots`)
	if err != nil {
		writeError(w, 500, "无法验证目录")
		return
	}
	for roots.Next() {
		var root string
		if err := roots.Scan(&root); err != nil {
			roots.Close()
			writeError(w, 500, "无法验证目录")
			return
		}
		if pathWithin(path, root) || pathWithin(root, path) {
			roots.Close()
			writeError(w, 400, "媒体存储位置不能与原件扫描目录重叠")
			return
		}
	}
	if err := roots.Err(); err != nil {
		roots.Close()
		writeError(w, 500, "无法验证目录")
		return
	}
	roots.Close()
	for _, subdir := range []string{"thumbnails", "videos"} {
		if err := os.MkdirAll(filepath.Join(path, subdir), 0700); err != nil {
			writeError(w, 400, "无法在该目录保存媒体文件")
			return
		}
	}
	var id string
	if err := s.db.QueryRow(`SELECT id FROM media_stores WHERE path=?`, path).Scan(&id); err == nil {
		writeJSON(w, 200, map[string]any{"id": id, "path": path, "existing": true})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 500, "无法读取本机存储位置")
		return
	}
	id = newID()
	if _, err := s.db.Exec(`INSERT INTO media_stores(id,path,is_default,added_at) VALUES(?,?,0,?)`, id, path, now()); err != nil {
		writeError(w, 500, "无法保存本机存储位置")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "path": path})
}

func (s *server) selectMediaStore(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	id := r.PathValue("id")
	var path string
	if err := s.db.QueryRow(`SELECT path FROM media_stores WHERE id=?`, id).Scan(&path); err != nil {
		writeError(w, 404, "本机存储位置不存在")
		return
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		writeError(w, 409, "本机存储位置当前不可用")
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法选择本机存储位置")
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE media_stores SET is_default=0 WHERE is_default=1`); err != nil {
		writeError(w, 500, "无法选择本机存储位置")
		return
	}
	if _, err := tx.Exec(`UPDATE media_stores SET is_default=1 WHERE id=?`, id); err != nil {
		writeError(w, 500, "无法选择本机存储位置")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "无法选择本机存储位置")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) mediaAssetPath(id, kind string) (string, string) {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT l.storage_id,l.path FROM file_asset_locations l
			WHERE l.asset_id=? ORDER BY (SELECT is_default FROM media_stores WHERE id=l.storage_id) DESC,l.storage_id`, id)
		if err == nil {
			for rows.Next() {
				var storageID, path string
				if rows.Scan(&storageID, &path) == nil {
					if _, err := os.Stat(path); err == nil {
						rows.Close()
						return storageID, path
					}
				}
			}
			rows.Close()
		}
		legacy := filepath.Join(s.thumbnailDir, id+".jpg")
		if kind == "video" {
			legacy = filepath.Join(s.videoDir, id)
		}
		if _, err := os.Stat(legacy); err == nil {
			return defaultMediaStoreID, legacy
		}
		if store, err := s.selectedMediaStore(); err == nil {
			if kind == "video" {
				return store.ID, filepath.Join(store.Path, "videos", id)
			}
			return store.ID, filepath.Join(store.Path, "thumbnails", id+".jpg")
		}
	}
	if kind == "video" {
		return defaultMediaStoreID, filepath.Join(s.videoDir, id)
	}
	return defaultMediaStoreID, filepath.Join(s.thumbnailDir, id+".jpg")
}

func (s *server) recordThumbnail(id string, info os.FileInfo, width int, at float64, path string) error {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	assetID := strings.TrimSuffix(filepath.Base(path), ".jpg")
	storageID := s.mediaStoreIDForPath(path)
	return s.recordMediaAsset(assetID, id, preprocessVersion(info), "thumbnail",
		fmt.Sprintf("%d@%.3f", width, at), "image/jpeg", storageID, path, fileInfo.Size())
}

func (s *server) recordVideoAsset(id string, sourceInfo os.FileInfo, quality, dir string, manifest videoManifest) error {
	parts := []string{"init.mp4"}
	for index := range manifest.Segments {
		parts = append(parts, fmt.Sprintf("segment-%05d.m4s", index))
	}
	var size int64
	for _, part := range parts {
		info, err := os.Stat(filepath.Join(dir, part))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing video part %s", part)
		}
		size += info.Size()
	}
	return s.recordMediaAsset(filepath.Base(dir), id, preprocessVersion(sourceInfo), "video", quality,
		manifest.Codec, s.mediaStoreIDForPath(dir), dir, size)
}

func (s *server) mediaStoreIDForPath(path string) string {
	if s.db != nil {
		rows, err := s.db.Query(`SELECT id,path FROM media_stores`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, root string
				if rows.Scan(&id, &root) == nil && pathWithin(path, root) {
					return id
				}
			}
		}
	}
	return defaultMediaStoreID
}

func (s *server) recordMediaAsset(id, resourceID, versionKey, kind, spec, codec, storageID, path string, size int64) error {
	if s.db == nil {
		return nil
	}
	if _, err := s.selectedMediaStore(); errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if size < 0 {
		return fmt.Errorf("invalid asset size")
	}
	var existingSize int64
	if err := s.db.QueryRow(`SELECT a.size FROM file_assets a JOIN file_asset_locations l ON l.asset_id=a.id
		WHERE a.id=? AND l.storage_id=? AND l.path=?`, id, storageID, path).Scan(&existingSize); err == nil && existingSize == size {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO file_assets(id,resource_id,version_key,kind,spec,size,codec,created_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET size=excluded.size,codec=excluded.codec`,
		id, resourceID, versionKey, kind, spec, size, codec, now()); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO file_asset_locations(asset_id,storage_id,path) VALUES(?,?,?)`, id, storageID, path); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.cloud != nil {
		s.cloud.queueAssetSync(resourceID)
	}
	return nil
}

func (s *server) backfillMediaAssets() {
	rows, err := s.db.Query(`SELECT id,path,kind,size,modified_at FROM resources WHERE available=1 AND kind IN ('image','video')`)
	if err != nil {
		log.Printf("media index backfill: %v", err)
		return
	}
	type source struct {
		id, path, kind, modified string
		size                     int64
	}
	var sources []source
	for rows.Next() {
		var item source
		if err := rows.Scan(&item.id, &item.path, &item.kind, &item.size, &item.modified); err != nil {
			rows.Close()
			log.Printf("media index backfill: %v", err)
			return
		}
		sources = append(sources, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("media index backfill: %v", err)
	}
	rows.Close()
	for _, item := range sources {
		info, err := os.Stat(item.path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != item.size ||
			info.ModTime().UTC().Format(time.RFC3339Nano) != item.modified {
			continue
		}
		for _, width := range []int{240, thumbnailWidth, mobileThumbnailWidth, 1280} {
			path, _ := s.thumbnailCachePath(item.id, info, width, 0)
			if _, err := os.Stat(path); err == nil {
				if err := s.recordThumbnail(item.id, info, width, 0, path); err != nil {
					log.Printf("media index thumbnail %s: %v", item.id, err)
				}
			}
		}
		if item.kind == "video" {
			for quality := range videoQualities {
				_, _, dir, err := s.videoSource(item.id, quality)
				if err != nil {
					continue
				}
				if manifest, err := readVideoManifest(dir); err == nil {
					if err := s.recordVideoAsset(item.id, info, quality, dir, manifest); err != nil {
						log.Printf("media index video %s %s: %v", item.id, quality, err)
					}
				}
			}
		}
	}
}

type indexedLocation struct {
	NodeID    string `json:"nodeId"`
	StorageID string `json:"storageId"`
	Path      string `json:"path"`
}

type indexedCloudContent struct {
	RevisionID string           `json:"revisionId"`
	Chunks     []cloudChunk     `json:"chunks"`
	Parts      []cloudAssetPart `json:"parts,omitempty"`
}

type indexedContent struct {
	Size  int64                `json:"size"`
	Local []indexedLocation    `json:"local"`
	Cloud *indexedCloudContent `json:"cloud,omitempty"`
}

type indexedAsset struct {
	ID      string         `json:"assetId"`
	Kind    string         `json:"kind"`
	Spec    string         `json:"spec"`
	Codec   string         `json:"codec,omitempty"`
	Content indexedContent `json:"content"`
}

type fileIndexView struct {
	FileID      string         `json:"fileId"`
	RevisionID  string         `json:"revisionId"`
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Modified    string         `json:"modified"`
	Tags        []string       `json:"tags"`
	Original    indexedContent `json:"original"`
	Assets      []indexedAsset `json:"assets"`
	ContentSize int64          `json:"contentSize"`
}

func versionKeyFromMetadata(size int64, modified string) string {
	stamp, err := time.Parse(time.RFC3339Nano, modified)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", size, stamp.UnixNano())
}

func (s *server) mediaContentSize(resourceID string, originalSize int64, modified string) int64 {
	key := versionKeyFromMetadata(originalSize, modified)
	var assetSize sql.NullInt64
	if key == "" || s.db.QueryRow(`SELECT SUM(size) FROM file_assets WHERE resource_id=? AND version_key=?`,
		resourceID, key).Scan(&assetSize) != nil {
		return originalSize
	}
	return originalSize + assetSize.Int64
}

func (s *server) fileIndex(w http.ResponseWriter, r *http.Request) {
	index, err := s.buildFileIndex(r.PathValue("id"), true)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "文件不存在")
		return
	}
	if err != nil {
		writeError(w, 500, "无法读取文件索引")
		return
	}
	writeJSON(w, 200, index)
}

func (s *server) buildFileIndex(id string, includeCloud bool) (fileIndexView, error) {
	index := fileIndexView{FileID: id, Tags: []string{}, Original: indexedContent{Local: []indexedLocation{}}, Assets: []indexedAsset{}}
	var path, rootID string
	var available bool
	localErr := s.db.QueryRow(`SELECT current_version_id,name,kind,modified_at,size,path,root_id,available
		FROM resources WHERE id=?`, id).Scan(&index.RevisionID, &index.Name, &index.Kind, &index.Modified,
		&index.Original.Size, &path, &rootID, &available)
	if localErr != nil && !errors.Is(localErr, sql.ErrNoRows) {
		return index, localErr
	}
	if localErr == nil {
		nodeID := ""
		if s.cloud != nil {
			nodeID = s.cloud.runtime.id
		}
		if available {
			index.Original.Local = append(index.Original.Local, indexedLocation{NodeID: nodeID, StorageID: rootID, Path: path})
		}
		index.ContentSize = s.mediaContentSize(id, index.Original.Size, index.Modified)
		rows, err := s.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=? ORDER BY t.name`, id)
		if err != nil {
			return index, err
		}
		for rows.Next() {
			var tag string
			if rows.Scan(&tag) == nil {
				index.Tags = append(index.Tags, tag)
			}
		}
		rows.Close()
		assetRows, err := s.db.Query(`SELECT a.id,a.kind,a.spec,a.codec,a.size,l.storage_id,l.path
			FROM file_assets a JOIN file_asset_locations l ON l.asset_id=a.id
			WHERE a.resource_id=? AND a.version_key=? ORDER BY a.id,l.storage_id`, id,
			versionKeyFromMetadata(index.Original.Size, index.Modified))
		if err != nil {
			return index, err
		}
		positions := map[string]int{}
		for assetRows.Next() {
			var assetID, kind, spec, codec, storageID, assetPath string
			var size int64
			if err := assetRows.Scan(&assetID, &kind, &spec, &codec, &size, &storageID, &assetPath); err != nil {
				assetRows.Close()
				return index, err
			}
			position, found := positions[assetID]
			if !found {
				position = len(index.Assets)
				positions[assetID] = position
				index.Assets = append(index.Assets, indexedAsset{ID: assetID, Kind: kind, Spec: spec, Codec: codec,
					Content: indexedContent{Size: size, Local: []indexedLocation{}}})
			}
			index.Assets[position].Content.Local = append(index.Assets[position].Content.Local,
				indexedLocation{NodeID: nodeID, StorageID: storageID, Path: assetPath})
		}
		if err := assetRows.Err(); err != nil {
			assetRows.Close()
			return index, err
		}
		assetRows.Close()
	}
	if includeCloud && s.cloud != nil {
		hasLocalSnapshots := false
		if snapshots, err := s.cloud.localIndexes(id); err == nil {
			hasLocalSnapshots = len(snapshots) > 0
			for _, snapshot := range snapshots {
				if localErr != nil && index.Name == "" {
					index = snapshot
				}
				if snapshot.Original.Size != index.Original.Size || snapshot.Modified != index.Modified {
					continue
				}
				index.Original.Local = mergeIndexedLocations(index.Original.Local, snapshot.Original.Local)
				positions := map[string]int{}
				for i, asset := range index.Assets {
					positions[asset.ID] = i
				}
				for _, asset := range snapshot.Assets {
					if at, ok := positions[asset.ID]; ok {
						index.Assets[at].Content.Local = mergeIndexedLocations(index.Assets[at].Content.Local, asset.Content.Local)
					} else {
						index.Assets = append(index.Assets, asset)
					}
				}
			}
			index.ContentSize = index.Original.Size
			for _, asset := range index.Assets {
				index.ContentSize += asset.Content.Size
			}
		}
		record, manifest, err := s.cloud.findRecord(id)
		if err == nil && (localErr != nil || index.Original.Size == manifest.Size && index.Modified == manifest.Modified) {
			if localErr != nil && index.Name != "" && (index.Original.Size != manifest.Size || index.Modified != manifest.Modified) {
				index = fileIndexView{FileID: id, Tags: []string{}, Original: indexedContent{Local: []indexedLocation{}}, Assets: []indexedAsset{}}
			}
			index.Original.Cloud = &indexedCloudContent{RevisionID: record.RevisionID, Chunks: manifest.Chunks}
			if localErr != nil && !hasLocalSnapshots {
				index.Original.Local = mergeIndexedLocations(index.Original.Local, manifest.Local)
			}
			if localErr != nil && index.Name == "" {
				index.RevisionID = record.RevisionID
				index.Name = manifest.Name
				index.Kind = manifest.Kind
				index.Tags = append([]string(nil), manifest.Tags...)
				index.Modified = manifest.Modified
				index.Original.Size = manifest.Size
			}
			{
				assetPositions := map[string]int{}
				for i, asset := range index.Assets {
					assetPositions[asset.ID] = i
				}
				for _, asset := range manifest.Assets {
					position, found := assetPositions[asset.ID]
					if !found {
						position = len(index.Assets)
						assetPositions[asset.ID] = position
						index.Assets = append(index.Assets, indexedAsset{ID: asset.ID, Kind: asset.Kind,
							Spec: asset.Spec, Codec: asset.Codec, Content: indexedContent{Size: asset.Size, Local: []indexedLocation{}}})
					}
					index.Assets[position].Content.Cloud = &indexedCloudContent{RevisionID: record.RevisionID, Parts: asset.Parts}
					if localErr != nil && !hasLocalSnapshots {
						index.Assets[position].Content.Local = mergeIndexedLocations(index.Assets[position].Content.Local, asset.Local)
					}
				}
				index.ContentSize = index.Original.Size
				for _, asset := range index.Assets {
					index.ContentSize += asset.Content.Size
				}
			}
		}
	}
	if localErr != nil && index.Name == "" {
		return index, sql.ErrNoRows
	}
	return index, nil
}

func mergeIndexedLocations(existing, more []indexedLocation) []indexedLocation {
	for _, location := range more {
		found := false
		for _, current := range existing {
			if current.NodeID == location.NodeID && current.StorageID == location.StorageID && current.Path == location.Path {
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, location)
		}
	}
	return existing
}
