package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type catalogPruneRequest struct {
	Generation string            `json:"generation"`
	LibraryID  string            `json:"libraryId,omitempty"`
	IDs        []string          `json:"ids"`
	ContentIDs []string          `json:"contentIds,omitempty"`
	Snapshots  map[string]string `json:"snapshots"`
}

func (c *cloudHub) savePrunedLocked() error {
	ids := make([]string, 0, len(c.pruned))
	for id := range c.pruned {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(c.prunePath), ".catalog-prune-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), c.prunePath)
}

func (c *cloudHub) readCatalogRecord(id string) (cloudRecord, error) {
	var record cloudRecord
	file, err := c.disk.get("catalog", id)
	if err != nil {
		return record, err
	}
	defer file.Close()
	if err = json.NewDecoder(file).Decode(&record); err != nil {
		return record, err
	}
	return record, nil
}

// Prune markers are durable before any catalog deletion. They prevent a late
// storage node or old client from restoring a record after compaction.
func (c *cloudHub) pruneRoutes(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("GET /cloud/pruned", authorize(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		ids := make([]string, 0, len(c.pruned))
		for id := range c.pruned {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		writeJSON(w, 200, map[string]any{"records": ids})
	})))
	mux.Handle("POST /cloud/catalog/prune", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input catalogPruneRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input) != nil || !validCloudHash(input.Generation) || (len(input.IDs) == 0 && len(input.ContentIDs) == 0) || len(input.IDs) > 1000 || len(input.ContentIDs) > 1000 || len(input.Snapshots) == 0 {
			http.Error(w, "invalid prune request", 400)
			return
		}
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		if c.leaseActiveLocked() {
			http.Error(w, "cleanup in progress", http.StatusLocked)
			return
		}
		ids, err := c.disk.list("catalog")
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		visible := make([]string, 0, len(ids))
		for _, id := range ids {
			if !c.pruned[id] {
				visible = append(visible, id)
			}
		}
		if cloudHash([]byte(strings.Join(visible, "\x00"))) != input.Generation {
			http.Error(w, "catalog changed", 409)
			return
		}
		writerRecords := map[string]int{}
		for _, id := range visible {
			record, readErr := c.readCatalogRecord(id)
			if readErr != nil {
				http.Error(w, "catalog unreadable", 409)
				return
			}
			if record.Format >= 4 {
				writerRecords[record.LibraryID+"\x00"+record.AuthorID]++
			}
		}
		seen := map[string]bool{}
		snapshotEvidence := map[string]bool{}
		for _, hash := range input.Snapshots {
			snapshotEvidence[hash] = true
		}
		if len(input.ContentIDs) > 0 {
			// Content records of permanently deleted files are proposed only by a
			// query node that passed the member gate. The Hub still requires a
			// persisted snapshot for the same library plus the retention period.
			if !validCloudHash(input.LibraryID) {
				http.Error(w, "invalid prune library", 400)
				return
			}
			persisted := 0
			for author, hash := range input.Snapshots {
				if !validCloudHash(hash) || author == "" || len(author) > 128 {
					http.Error(w, "invalid prune snapshot", 400)
					return
				}
				snapshot, readErr := c.readCatalogRecord(hash)
				if readErr == nil && !c.pruned[hash] && snapshot.LibraryID == input.LibraryID && snapshot.ObjectID == "library-snapshot/"+author && snapshot.Format >= 4 {
					persisted++
				}
			}
			if persisted == 0 {
				http.Error(w, "snapshot not persisted", 409)
				return
			}
			for _, id := range input.ContentIDs {
				if !validCloudHash(id) || seen[id] || c.pruned[id] || snapshotEvidence[id] {
					http.Error(w, "invalid content prune target", 400)
					return
				}
				seen[id] = true
				record, openErr := c.readCatalogRecord(id)
				if openErr != nil {
					http.Error(w, "content prune target missing", 409)
					return
				}
				if record.LibraryID != input.LibraryID || strings.HasPrefix(record.ObjectID, "library-operation/") || strings.HasPrefix(record.ObjectID, "library-snapshot/") || strings.HasPrefix(record.ObjectID, "library-receipt/") {
					http.Error(w, "only content records of deleted files may be recycled", 400)
					return
				}
				stamp, parseErr := time.Parse(time.RFC3339Nano, record.CreatedAt)
				if parseErr != nil || time.Since(stamp) < libraryCleanupRetention {
					http.Error(w, "retention period not met", 409)
					return
				}
			}
		}
		for _, id := range input.IDs {
			if !validCloudHash(id) || seen[id] || c.pruned[id] {
				http.Error(w, "invalid prune target", 400)
				return
			}
			seen[id] = true
			record, openErr := c.readCatalogRecord(id)
			if openErr != nil {
				http.Error(w, "prune target missing", 409)
				return
			}
			if !strings.HasPrefix(record.ObjectID, "library-operation/") || len(record.BlockIDs) != 0 {
				http.Error(w, "only operation records may be compacted", 400)
				return
			}
			writerKey := record.LibraryID + "\x00" + record.AuthorID
			if writerRecords[writerKey] <= 1 {
				http.Error(w, "writer format gate would be lost", 409)
				return
			}
			writerRecords[writerKey]--
			stamp, parseErr := time.Parse(time.RFC3339Nano, record.CreatedAt)
			if parseErr != nil || time.Since(stamp) < libraryCleanupRetention {
				http.Error(w, "retention period not met", 409)
				return
			}
			parts := strings.Split(record.ObjectID, "/")
			if len(parts) != 3 {
				http.Error(w, "invalid operation identity", 400)
				return
			}
			snapshotHash := input.Snapshots[parts[1]]
			if !validCloudHash(snapshotHash) || seen[snapshotHash] || c.pruned[snapshotHash] {
				http.Error(w, "snapshot missing", 409)
				return
			}
			snapshot, openErr := c.readCatalogRecord(snapshotHash)
			if openErr != nil || snapshot.LibraryID != record.LibraryID || snapshot.ObjectID != "library-snapshot/"+parts[1] || snapshot.Format < 4 {
				http.Error(w, "snapshot not persisted", 409)
				return
			}
		}
		targets := append(append([]string{}, input.IDs...), input.ContentIDs...)
		for _, id := range targets {
			c.pruned[id] = true
		}
		if err = c.savePrunedLocked(); err != nil {
			for _, id := range targets {
				delete(c.pruned, id)
			}
			http.Error(w, "cannot persist prune markers", 500)
			return
		}
		for _, id := range targets {
			if err = c.disk.remove("catalog", id); err != nil {
				http.Error(w, "prune marker persisted but object removal failed", 500)
				return
			}
		}
		writeJSON(w, 200, map[string]any{"pruned": input.IDs, "recycledContent": input.ContentIDs})
	})))
}

func (c *cloudHub) finishPrunedOnStartup() error {
	for id := range c.pruned {
		if err := c.disk.remove("catalog", id); err != nil {
			return errors.New("cannot resume catalog prune: " + err.Error())
		}
	}
	return nil
}
