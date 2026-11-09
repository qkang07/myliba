package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"time"
)

// Local indexes are encrypted catalog records with no file blocks. They let
// another node discover a source and its derived files without uploading them.
func (c *cloudClient) publishLocalIndex(objectID string) error {
	if c.indexer == nil {
		return nil
	}
	index, err := c.indexer.buildFileIndex(objectID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	plain, err := json.Marshal(index)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(plain)
	digestText := hex.EncodeToString(digest[:])
	c.localDigestMu.Lock()
	defer c.localDigestMu.Unlock()
	if c.localDigests[objectID] == digestText {
		return nil
	}
	revision := index.RevisionID
	if revision == "" {
		revision = newID()
	}
	// ObjectID remains the old resource lookup key; FileID inside the encrypted
	// index is the stable logical identity introduced by P2.
	record := cloudRecord{Format: 4, LibraryID: c.libraryID(), ObjectID: objectID,
		RevisionID: revision, AuthorID: c.runtime.id, AuthorEpoch: c.authorEpoch(), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), BlockIDs: []string{}}
	record, err = c.sealManifest(record, cloudManifest{IndexOnly: true, LocalIndex: &index})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(raw) > cloudMaxRecordSize {
		return errors.New("local file index is too large")
	}
	if err := c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		return err
	}
	c.localDigests[objectID] = digestText
	return nil
}

func (c *cloudClient) primeLocalDigests() error {
	records, err := c.validRecords()
	if err != nil {
		return err
	}
	latest := map[string]cloudRecord{}
	for _, record := range records {
		if record.AuthorID != c.runtime.id {
			continue
		}
		manifest, err := c.openManifest(record)
		if err != nil || !manifest.IndexOnly {
			continue
		}
		previous, exists := latest[record.ObjectID]
		if !exists || catalogAfter(record, previous) {
			latest[record.ObjectID] = record
		}
	}
	c.localDigestMu.Lock()
	defer c.localDigestMu.Unlock()
	for objectID, record := range latest {
		manifest, err := c.openManifest(record)
		if err != nil {
			continue
		}
		plain, err := json.Marshal(manifest.LocalIndex)
		if err != nil {
			continue
		}
		digest := sha256.Sum256(plain)
		c.localDigests[objectID] = hex.EncodeToString(digest[:])
	}
	return nil
}

func (s *server) queueAllLocalIndexes() {
	if s.cloud == nil {
		return
	}
	started := time.Now()
	log.Printf("local index startup: resource scan starting")
	rows, err := s.db.Query(`SELECT id FROM resources`)
	if err != nil {
		log.Printf("local index startup: resource scan failed after %s: %v", time.Since(started), err)
		return
	}
	log.Printf("local index startup: resource scan acquired after %s", time.Since(started))
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		s.cloud.queueLocalIndex(id)
	}
	log.Printf("local index startup: queued %d resources in %s", len(ids), time.Since(started))
}

func (c *cloudClient) localIndexes(objectID string) ([]fileIndexView, error) {
	records, err := c.validRecords()
	if err != nil {
		return nil, err
	}
	latest := map[string]cloudRecord{}
	for _, record := range records {
		if record.ObjectID != objectID {
			continue
		}
		manifest, err := c.openManifest(record)
		if err != nil || !manifest.IndexOnly {
			continue
		}
		previous, ok := latest[record.AuthorID]
		if !ok || catalogAfter(record, previous) {
			latest[record.AuthorID] = record
		}
	}
	ordered := make([]cloudRecord, 0, len(latest))
	for _, record := range latest {
		ordered = append(ordered, record)
	}
	sort.Slice(ordered, func(i, j int) bool { return catalogAfter(ordered[i], ordered[j]) })
	indexes := make([]fileIndexView, 0, len(ordered))
	for _, record := range ordered {
		manifest, err := c.openManifest(record)
		if err == nil {
			indexes = append(indexes, *manifest.LocalIndex)
		}
	}
	return indexes, nil
}
