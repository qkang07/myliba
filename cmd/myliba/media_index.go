package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	MaxBytes  int64  `json:"maxBytes"`
	UsedBytes int64  `json:"usedBytes"`
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

func (s *server) writableMediaStore() (mediaStore, error) {
	rows, err := s.db.Query(`SELECT id,path,is_default,max_bytes FROM media_stores ORDER BY is_default DESC,(id='appdata') DESC,added_at,id`)
	if err != nil {
		return mediaStore{}, err
	}
	defer rows.Close()
	var first mediaStore
	for rows.Next() {
		var item mediaStore
		if err := rows.Scan(&item.ID, &item.Path, &item.IsDefault, &item.MaxBytes); err != nil {
			return mediaStore{}, err
		}
		if first.ID == "" {
			first = item
		}
		if item.MaxBytes == 0 || s.mediaStoreUsed(item.Path) < item.MaxBytes {
			return item, nil
		}
	}
	if err := rows.Err(); err != nil {
		return mediaStore{}, err
	}
	if first.ID != "" {
		return first, nil
	}
	return mediaStore{}, sql.ErrNoRows
}

func (s *server) mediaStoreUsed(path string) int64 {
	if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
		if used, ok := s.cloud.runtime.disk.rootUsage(path); ok {
			return used
		}
	}
	return mediaStoreUsage(path)
}

func (s *server) mediaStores(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	rows, err := s.db.Query(`SELECT id,path,is_default,max_bytes FROM media_stores ORDER BY (id='appdata') DESC,is_default DESC,added_at,id`)
	if err != nil {
		writeError(w, 500, "无法读取本机存储位置")
		return
	}
	defer rows.Close()
	stores := []mediaStore{}
	for rows.Next() {
		var item mediaStore
		if err := rows.Scan(&item.ID, &item.Path, &item.IsDefault, &item.MaxBytes); err != nil {
			writeError(w, 500, "无法读取本机存储位置")
			return
		}
		item.UsedBytes = s.mediaStoreUsed(item.Path)
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
		Path     string `json:"path"`
		MaxBytes int64  `json:"maxBytes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "本机存储位置无效")
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(input.Path))
	if err != nil || strings.TrimSpace(input.Path) == "" || input.MaxBytes < 0 {
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
	stores, err := s.db.Query(`SELECT path FROM media_stores`)
	if err != nil {
		writeError(w, 500, "无法验证存储目录")
		return
	}
	for stores.Next() {
		var root string
		if err := stores.Scan(&root); err != nil {
			stores.Close()
			writeError(w, 500, "无法验证存储目录")
			return
		}
		if pathWithin(path, root) && pathWithin(root, path) {
			continue
		}
		if pathWithin(path, root) || pathWithin(root, path) {
			stores.Close()
			writeError(w, 400, "存储目录不能互相包含")
			return
		}
	}
	if err := stores.Err(); err != nil {
		stores.Close()
		writeError(w, 500, "无法验证存储目录")
		return
	}
	stores.Close()
	for _, subdir := range []string{"thumbnails", "videos"} {
		if err := os.MkdirAll(filepath.Join(path, subdir), 0700); err != nil {
			writeError(w, 400, "无法在该目录保存媒体文件")
			return
		}
	}
	var id string
	if err := s.db.QueryRow(`SELECT id FROM media_stores WHERE path=?`, path).Scan(&id); err == nil {
		if input.MaxBytes > 0 {
			if input.MaxBytes < s.mediaStoreUsed(path) {
				writeError(w, 409, "容量上限不能小于当前占用")
				return
			}
			if _, updateErr := s.db.Exec(`UPDATE media_stores SET max_bytes=? WHERE id=?`, input.MaxBytes, id); updateErr != nil {
				writeError(w, 500, "无法保存容量上限")
				return
			}
		}
		writeJSON(w, 200, map[string]any{"id": id, "path": path, "existing": true})
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeError(w, 500, "无法读取本机存储位置")
		return
	}
	id = newID()
	if _, err := s.db.Exec(`INSERT INTO media_stores(id,path,is_default,added_at,max_bytes) VALUES(?,?,0,?,?)`, id, path, now(), input.MaxBytes); err != nil {
		writeError(w, 500, "无法保存本机存储位置")
		return
	}
	writeJSON(w, 201, map[string]string{"id": id, "path": path})
}

func (s *server) updateMediaStoreCapacity(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	var input struct {
		MaxBytes int64 `json:"maxBytes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || input.MaxBytes < 0 {
		writeError(w, 400, "容量上限无效")
		return
	}
	id := r.PathValue("id")
	var path string
	if err := s.db.QueryRow(`SELECT path FROM media_stores WHERE id=?`, id).Scan(&path); err != nil {
		writeError(w, 404, "本机存储位置不存在")
		return
	}
	used := mediaStoreUsage(path)
	if input.MaxBytes > 0 && input.MaxBytes < used {
		writeError(w, 409, "容量上限不能小于当前占用")
		return
	}
	if _, err := s.db.Exec(`UPDATE media_stores SET max_bytes=? WHERE id=?`, input.MaxBytes, id); err != nil {
		writeError(w, 500, "无法保存容量上限")
		return
	}
	if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
		s.cloud.runtime.disk.setRootUsage(path, used)
	}
	w.WriteHeader(http.StatusNoContent)
}

func mediaStoreUsage(root string) int64 {
	var total int64
	for _, child := range []string{"thumbnails", "videos", "cloud"} {
		_ = filepath.WalkDir(filepath.Join(root, child), func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if info, e := entry.Info(); e == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
	}
	return total
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

type migrationFile struct {
	source, target string
	size           int64
	hash           [32]byte
	exists         bool
}

func hashRegularFile(path string) (int64, [32]byte, error) {
	var zero [32]byte
	info, err := os.Lstat(path)
	if err != nil {
		return 0, zero, err
	}
	if !info.Mode().IsRegular() {
		return 0, zero, fmt.Errorf("拒绝迁移非普通文件: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, zero, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, zero, err
	}
	copy(zero[:], h.Sum(nil))
	return n, zero, nil
}

func (s *server) migrateMediaStore(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	var disk *cloudDisk
	if s.cloud != nil && s.cloud.runtime != nil {
		disk = s.cloud.runtime.disk
	}
	if disk != nil {
		disk.mu.Lock()
		defer disk.mu.Unlock()
	}
	sourceID := r.PathValue("id")
	var input struct {
		TargetID string `json:"targetId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil || input.TargetID == "" || input.TargetID == sourceID {
		writeError(w, 400, "迁移目标无效")
		return
	}
	var sourcePath, targetPath string
	var isDefault bool
	if err := s.db.QueryRow(`SELECT path,is_default FROM media_stores WHERE id=?`, sourceID).Scan(&sourcePath, &isDefault); err != nil {
		writeError(w, 404, "源存储位置不存在")
		return
	}
	if isDefault || sourceID == defaultMediaStoreID {
		writeError(w, 409, "默认应用目录不能移除")
		return
	}
	var targetLimit int64
	if err := s.db.QueryRow(`SELECT path,max_bytes FROM media_stores WHERE id=?`, input.TargetID).Scan(&targetPath, &targetLimit); err != nil {
		writeError(w, 404, "目标存储位置不存在")
		return
	}
	if _, err := os.Stat(sourcePath); err != nil {
		writeError(w, 409, "源目录当前不可用")
		return
	}
	if _, err := os.Stat(targetPath); err != nil {
		writeError(w, 409, "目标目录当前不可用")
		return
	}
	if pathWithin(sourcePath, targetPath) || pathWithin(targetPath, sourcePath) {
		writeError(w, 409, "源目录与目标目录不能互相包含")
		return
	}
	files := make([]migrationFile, 0)
	for _, child := range []string{"thumbnails", "videos", "cloud"} {
		root := filepath.Join(sourcePath, child)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("目录中含有符号链接: %s", path)
			}
			if info.IsDir() {
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("目录中含有特殊文件: %s", path)
			}
			rel, err := filepath.Rel(sourcePath, path)
			if err != nil {
				return err
			}
			size, hash, err := hashRegularFile(path)
			if err != nil {
				return err
			}
			target := filepath.Join(targetPath, rel)
			f := migrationFile{source: path, target: target, size: size, hash: hash}
			if targetInfo, e := os.Lstat(target); e == nil {
				if !targetInfo.Mode().IsRegular() {
					return fmt.Errorf("目标存在同名非普通文件: %s", target)
				}
				n, h, e := hashRegularFile(target)
				if e != nil {
					return e
				}
				if n != size || h != hash {
					return fmt.Errorf("目标已存在不同内容的同名文件: %s", target)
				}
				f.exists = true
			} else if !os.IsNotExist(e) {
				return e
			}
			files = append(files, f)
			return nil
		})
		if err != nil {
			writeError(w, 409, "无法安全读取源目录: "+err.Error())
			return
		}
	}
	var delta int64
	for _, f := range files {
		delta += f.size
	}
	used := mediaStoreUsage(targetPath)
	if targetLimit > 0 && used+delta > targetLimit {
		writeError(w, 409, "目标目录剩余容量不足，源目录未变更")
		return
	}
	for _, f := range files {
		if f.exists {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.target), 0700); err != nil {
			writeError(w, 500, "无法创建目标目录")
			return
		}
		in, err := os.Open(f.source)
		if err != nil {
			writeError(w, 500, "无法读取源文件")
			return
		}
		tmp, err := os.CreateTemp(filepath.Dir(f.target), ".myliba-migrate-*")
		if err != nil {
			in.Close()
			writeError(w, 500, "无法创建迁移临时文件")
			return
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(tmp, h), in)
		in.Close()
		if copyErr == nil {
			copyErr = tmp.Sync()
		}
		closeErr := tmp.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		var got [32]byte
		copy(got[:], h.Sum(nil))
		if copyErr == nil && (n != f.size || got != f.hash) {
			copyErr = fmt.Errorf("迁移校验失败")
		}
		if copyErr == nil {
			copyErr = os.Rename(tmp.Name(), f.target)
		}
		if copyErr != nil {
			_ = os.Remove(tmp.Name())
			writeError(w, 500, "复制或校验失败，源目录仍保留: "+copyErr.Error())
			return
		}
	}
	locations, err := s.db.Query(`SELECT asset_id,path FROM file_asset_locations WHERE storage_id=?`, sourceID)
	if err != nil {
		writeError(w, 500, "无法更新附加内容记录")
		return
	}
	type location struct{ asset, path string }
	locs := []location{}
	for locations.Next() {
		var l location
		if err := locations.Scan(&l.asset, &l.path); err != nil {
			locations.Close()
			writeError(w, 500, "无法读取附加内容记录")
			return
		}
		locs = append(locs, l)
	}
	if err := locations.Err(); err != nil {
		locations.Close()
		writeError(w, 500, "无法读取附加内容记录")
		return
	}
	locations.Close()
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, 500, "无法更新存储位置")
		return
	}
	defer tx.Rollback()
	for _, l := range locs {
		rel, err := filepath.Rel(sourcePath, l.path)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			writeError(w, 409, "附加内容记录超出源目录")
			return
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO file_asset_locations(asset_id,storage_id,path) VALUES(?,?,?)`, l.asset, input.TargetID, filepath.Join(targetPath, rel)); err != nil {
			writeError(w, 500, "无法迁移附加内容记录")
			return
		}
	}
	if _, err := tx.Exec(`DELETE FROM file_asset_locations WHERE storage_id=?`, sourceID); err != nil {
		writeError(w, 500, "无法迁移附加内容记录")
		return
	}
	if _, err := tx.Exec(`DELETE FROM media_stores WHERE id=?`, sourceID); err != nil {
		writeError(w, 500, "无法移除源存储位置")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, 500, "无法完成存储迁移")
		return
	}
	for _, child := range []string{"thumbnails", "videos", "cloud"} {
		if err := os.RemoveAll(filepath.Join(sourcePath, child)); err != nil {
			log.Printf("media store migration cleanup %s: %v", child, err)
		}
	}
	if disk != nil {
		_ = disk.configureMediaStores(s.db)
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
		if store, err := s.writableMediaStore(); err == nil {
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
	var maxBytes int64
	var storageRoot string
	if err := s.db.QueryRow(`SELECT path,max_bytes FROM media_stores WHERE id=?`, storageID).Scan(&storageRoot, &maxBytes); err == nil {
		_, projected := mediaStoreUsageForReservation(storageRoot, path, size)
		if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
			s.cloud.runtime.disk.setRootUsage(storageRoot, mediaStoreUsage(storageRoot))
		}
		if maxBytes > 0 && projected > maxBytes {
			storeKind := "thumbnails"
			if kind == "video" {
				storeKind = "videos"
			}
			nextID, nextPath, moveErr := s.moveMediaAssetToAvailableStore(path, storeKind, size)
			if moveErr != nil {
				_ = os.RemoveAll(path)
				if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
					s.cloud.runtime.disk.setRootUsage(storageRoot, mediaStoreUsage(storageRoot))
				}
				return fmt.Errorf("本机存储目录已达到容量上限，且后续目录没有足够空间: %w", moveErr)
			}
			storageID, path = nextID, nextPath
			if err := s.db.QueryRow(`SELECT path,max_bytes FROM media_stores WHERE id=?`, storageID).Scan(&storageRoot, &maxBytes); err != nil {
				return err
			}
			_, projected = mediaStoreUsageForReservation(storageRoot, path, size)
		}
		if maxBytes > 0 && projected > maxBytes {
			_ = os.RemoveAll(path)
			if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
				s.cloud.runtime.disk.setRootUsage(storageRoot, mediaStoreUsage(storageRoot))
			}
			return fmt.Errorf("本机存储目录已达到容量上限")
		}
		if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
			s.cloud.runtime.disk.setRootUsage(storageRoot, mediaStoreUsage(storageRoot))
		}
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

func mediaStoreUsageForReservation(root, assetPath string, incoming int64) (int64, int64) {
	// The just-written asset is already included by the walk, so avoid counting it twice.
	used := mediaStoreUsage(root)
	if assetInfo, err := os.Stat(assetPath); err == nil {
		if assetInfo.IsDir() {
			var current int64
			_ = filepath.WalkDir(assetPath, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr == nil && !entry.IsDir() {
					if info, e := entry.Info(); e == nil && info.Mode().IsRegular() {
						current += info.Size()
					}
				}
				return nil
			})
			used -= current
		} else if assetInfo.Mode().IsRegular() {
			used -= assetInfo.Size()
		}
	}
	return used, used + incoming
}

func (s *server) moveMediaAssetToAvailableStore(assetPath, kind string, size int64) (string, string, error) {
	if kind != "thumbnails" && kind != "videos" {
		return "", "", errors.New("invalid media store kind")
	}
	currentID := s.mediaStoreIDForPath(assetPath)
	var currentRoot string
	if err := s.db.QueryRow(`SELECT path FROM media_stores WHERE id=?`, currentID).Scan(&currentRoot); err != nil {
		return "", "", err
	}
	relative, err := filepath.Rel(currentRoot, assetPath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("media asset is outside its storage directory")
	}
	rows, err := s.db.Query(`SELECT id,path,max_bytes FROM media_stores ORDER BY is_default DESC,(id='appdata') DESC,added_at,id`)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, root string
		var maxBytes int64
		if err := rows.Scan(&id, &root, &maxBytes); err != nil {
			return "", "", err
		}
		if id == currentID {
			continue
		}
		target := filepath.Join(root, relative)
		if _, statErr := os.Lstat(target); statErr == nil {
			continue
		}
		_, projected := mediaStoreUsageForReservation(root, target, size)
		if maxBytes > 0 && projected > maxBytes {
			continue
		}
		if err := moveMediaAssetPath(assetPath, target); err != nil {
			continue
		}
		if s.cloud != nil && s.cloud.runtime != nil && s.cloud.runtime.disk != nil {
			s.cloud.runtime.disk.setRootUsage(currentRoot, mediaStoreUsage(currentRoot))
			s.cloud.runtime.disk.setRootUsage(root, mediaStoreUsage(root))
		}
		return id, target, nil
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	return "", "", errors.New("所有存储目录都没有足够的剩余容量")
}

func moveMediaAssetPath(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return errors.New("媒体文件不可移动")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	tempRoot, err := os.MkdirTemp(filepath.Dir(target), ".media-move-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempRoot)
	tempPath := filepath.Join(tempRoot, filepath.Base(target))
	if info.IsDir() {
		if err := os.MkdirAll(tempPath, info.Mode().Perm()); err != nil {
			return err
		}
		err = filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			dst := filepath.Join(tempPath, rel)
			mode := entry.Type()
			if mode&os.ModeSymlink != 0 {
				return errors.New("媒体目录包含符号链接")
			}
			if entry.IsDir() {
				return os.MkdirAll(dst, 0700)
			}
			return copyMediaFile(path, dst)
		})
		if err != nil {
			return err
		}
	} else if err := copyMediaFile(source, tempPath); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	if err := os.RemoveAll(source); err != nil {
		_ = os.RemoveAll(target)
		return err
	}
	return nil
}

func copyMediaFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("媒体文件类型无效")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
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
