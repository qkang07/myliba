package main

import (
	"os"
	"path/filepath"
)

type cloudStorageStats struct {
	Backend        string  `json:"backend"`
	StatsReady     bool    `json:"statsReady"`
	BlockCount     int64   `json:"blockCount"`
	BlockBytes     *int64  `json:"blockBytes,omitempty"`
	CatalogCount   int64   `json:"catalogCount"`
	CapacityBytes  *uint64 `json:"capacityBytes,omitempty"`
	AvailableBytes *uint64 `json:"availableBytes,omitempty"`
}

func (d *cloudDisk) stats() (cloudStorageStats, error) {
	result := cloudStorageStats{Backend: "disk"}
	if d.backend != nil {
		switch d.backend.(type) {
		case *s3Backend:
			result.Backend = "s3"
		case *webDAVBackend:
			result.Backend = "webdav"
		case *openListBackend:
			result.Backend = "openlist"
		}
		blocks, err := d.backend.List("blocks")
		if err != nil {
			return result, err
		}
		catalog, err := d.backend.List("catalog")
		if err != nil {
			return result, err
		}
		result.BlockCount = int64(len(blocks))
		result.CatalogCount = int64(len(catalog))
		result.StatsReady = true
		return result, nil
	}
	var bytes int64
	for _, kind := range []string{"blocks", "catalog"} {
		err := filepath.WalkDir(filepath.Join(d.dir, kind), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.Type().IsRegular() || !validCloudHash(entry.Name()) {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if kind == "blocks" {
				result.BlockCount++
				bytes += info.Size()
			} else {
				result.CatalogCount++
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	result.BlockBytes = &bytes
	if capacity, available, err := cloudVolumeSpace(d.dir); err == nil {
		result.CapacityBytes, result.AvailableBytes = &capacity, &available
	}
	result.StatsReady = true
	return result, nil
}
