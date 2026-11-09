package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const cleanupLeaseTTL = 10 * time.Minute

// cleanupLease is the Hub's cross-node cleanup authorization. It is held only
// in memory: a Hub restart drops it and any in-flight storage deletion fails
// closed on the next verify. While a lease is active, catalog writes and
// prune requests are rejected so the authorized generation cannot change.
type cleanupLease struct {
	token      string
	generation string
	expiresAt  time.Time
}

type cleanupAuthorizeRequest struct {
	LibraryID         string            `json:"libraryId"`
	Generation        string            `json:"generation"`
	SnapshotFrontiers map[string]int64  `json:"snapshotFrontiers"`
	ActiveMembers     []string          `json:"activeMembers"`
	SnapshotHashes    map[string]string `json:"snapshotHashes"`
}

func (c *cloudHub) leaseActiveLocked() bool {
	if c.lease == nil {
		return false
	}
	if time.Now().Before(c.lease.expiresAt) {
		return true
	}
	c.lease = nil
	return false
}

func (c *cloudHub) visibleCatalogLocked() ([]string, error) {
	ids, err := c.disk.list("catalog")
	if err != nil {
		return nil, err
	}
	visible := make([]string, 0, len(ids))
	for _, id := range ids {
		if !c.pruned[id] {
			visible = append(visible, id)
		}
	}
	return visible, nil
}

// The query node proves its member gate with durable evidence the Hub can
// check without keys: persisted continuous snapshots and persisted member
// receipts for the exact catalog generation being cleaned.
func (c *cloudHub) cleanupLeaseRoutes(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("POST /cloud/cleanup/authorize", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input cleanupAuthorizeRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input) != nil ||
			!validCloudHash(input.LibraryID) || !validCloudHash(input.Generation) ||
			len(input.SnapshotFrontiers) == 0 || len(input.SnapshotFrontiers) > 1000 ||
			len(input.ActiveMembers) == 0 || len(input.ActiveMembers) > 1000 || len(input.SnapshotHashes) > 1000 {
			http.Error(w, "invalid cleanup authorization", 400)
			return
		}
		for author, frontier := range input.SnapshotFrontiers {
			if author == "" || len(author) > 128 || frontier < 1 || !validCloudHash(input.SnapshotHashes[author]) {
				http.Error(w, "invalid cleanup authorization", 400)
				return
			}
		}
		for _, member := range input.ActiveMembers {
			if member == "" || len(member) > 128 {
				http.Error(w, "invalid cleanup authorization", 400)
				return
			}
		}
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		if c.leaseActiveLocked() {
			http.Error(w, "cleanup already in progress", http.StatusConflict)
			return
		}
		visible, err := c.visibleCatalogLocked()
		if err != nil {
			http.Error(w, "catalog unavailable", 500)
			return
		}
		if cloudHash([]byte(strings.Join(visible, "\x00"))) != input.Generation {
			http.Error(w, "catalog changed", http.StatusConflict)
			return
		}
		receipts := map[string]bool{}
		for _, id := range visible {
			record, readErr := c.readCatalogRecord(id)
			if readErr != nil {
				http.Error(w, "catalog unreadable", http.StatusConflict)
				return
			}
			if record.Format >= 4 && record.LibraryID == input.LibraryID && strings.HasPrefix(record.ObjectID, "library-receipt/") {
				receipts[strings.TrimPrefix(record.ObjectID, "library-receipt/")] = true
			}
		}
		for author := range input.SnapshotFrontiers {
			snapshot, readErr := c.readCatalogRecord(input.SnapshotHashes[author])
			if readErr != nil || snapshot.LibraryID != input.LibraryID || snapshot.ObjectID != "library-snapshot/"+author || snapshot.Format < 4 || c.pruned[input.SnapshotHashes[author]] {
				http.Error(w, "snapshot not persisted", http.StatusConflict)
				return
			}
		}
		for _, member := range input.ActiveMembers {
			if !receipts[member] {
				http.Error(w, "member receipt not persisted", http.StatusConflict)
				return
			}
		}
		c.lease = &cleanupLease{token: cloudHash([]byte(newID())), generation: input.Generation, expiresAt: time.Now().Add(cleanupLeaseTTL)}
		writeJSON(w, 200, map[string]any{"token": c.lease.token, "generation": c.lease.generation, "expiresAt": c.lease.expiresAt.UTC().Format(time.RFC3339Nano)})
	})))
	mux.Handle("POST /cloud/cleanup/verify", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token      string `json:"token"`
			Generation string `json:"generation"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validCloudHash(input.Token) || !validCloudHash(input.Generation) {
			http.Error(w, "invalid cleanup verification", 400)
			return
		}
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		if !c.leaseActiveLocked() || c.lease.token != input.Token || c.lease.generation != input.Generation {
			http.Error(w, "cleanup authorization expired or superseded", http.StatusConflict)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})))
	mux.Handle("POST /cloud/cleanup/release", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token string `json:"token"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validCloudHash(input.Token) {
			http.Error(w, "invalid cleanup release", 400)
			return
		}
		c.pruneMu.Lock()
		if c.lease != nil && c.lease.token == input.Token {
			c.lease = nil
		}
		c.pruneMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})))
}