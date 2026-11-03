package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Compare parsed timestamps: RFC3339 with optional fractional seconds cannot
// safely be ordered as strings.
func catalogAfter(a, b cloudRecord) bool {
	x, _ := time.Parse(time.RFC3339Nano, a.CreatedAt)
	y, _ := time.Parse(time.RFC3339Nano, b.CreatedAt)
	if x.Equal(y) {
		return a.AuthorID+a.RevisionID > b.AuthorID+b.RevisionID
	}
	return x.After(y)
}

func (c *cloudClient) mergeCatalogSearch(files []cloudFileView, includeLocal bool) ([]cloudFileView, error) {
	records, err := c.validRecords()
	if err != nil {
		return nil, err
	}
	latest := map[string]cloudRecord{}
	indexes := map[string]fileIndexView{}
	// Each device publishes replacement snapshots, including empty tags and
	// ungrouping. Older snapshots must never resurrect removed classifications.
	for _, record := range records {
		manifest, err := c.openManifest(record)
		if err != nil || !manifest.IndexOnly || manifest.LocalIndex == nil {
			continue
		}
		key := record.ObjectID + "/" + record.AuthorID
		old, found := latest[key]
		if !found || catalogAfter(record, old) {
			latest[key] = record
			indexes[key] = *manifest.LocalIndex
		}
	}
	organization := map[string]fileIndexView{}
	organizationRecords := map[string]cloudRecord{}
	localOrganization := map[string]bool{}
	for key, index := range indexes {
		if !index.OrganizationKnown {
			continue
		}
		record := latest[key]
		old, found := organizationRecords[record.ObjectID]
		if !found || catalogAfter(record, old) {
			organizationRecords[record.ObjectID] = record
			organization[record.ObjectID] = index
		}
	}
	// Read the local database too, so a just-committed protection or metadata
	// change is reflected before the asynchronous publisher runs.
	if c.indexer != nil {
		ids := map[string]bool{}
		for _, file := range files {
			ids[file.ID] = true
		}
		for _, record := range latest {
			ids[record.ObjectID] = true
		}
		for id := range ids {
			index, err := c.indexer.buildFileIndex(id, false)
			if err == nil {
				organization[id] = index
				localOrganization[id] = true
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
	}
	result := []cloudFileView{}
	positions := map[string]int{}
	keyFor := func(id, modified string, size int64) string { return fmt.Sprintf("%s/%d/%s", id, size, modified) }
	add := func(file cloudFileView, id string) {
		if _, known := organization[id]; !known && file.Protected {
			return
		}
		if info, ok := organization[id]; ok {
			useIndex := localOrganization[id] || file.CatalogCreatedAt == "" || !catalogAfter(cloudRecord{CreatedAt: file.CatalogCreatedAt}, organizationRecords[id])
			if !useIndex && file.Protected {
				return
			}
			if info.Protected {
				return
			}
			// A stale index must not overwrite a newer upload's classification.
			if useIndex {
				file.Tags = append([]string{}, info.Tags...)
				file.FolderID = info.FolderID
				file.FolderName = info.FolderName
			}
		}
		key := keyFor(id, file.Modified, file.OriginalSize)
		if at, ok := positions[key]; ok {
			result[at].Locations = mergeIndexedLocations(result[at].Locations, file.Locations)
			return
		}
		positions[key] = len(result)
		result = append(result, file)
	}
	for _, file := range files {
		add(file, file.ID)
	}
	if includeLocal {
		keys := make([]string, 0, len(indexes))
		for key := range indexes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			index := indexes[key]
			if index.Deleted {
				continue
			}
			add(cloudFileView{ID: index.FileID + "@" + index.RevisionID, SourceID: index.FileID, Name: index.Name, Modified: index.Modified,
				OriginalSize: index.Original.Size, Size: index.ContentSize, Tags: index.Tags, FolderID: index.FolderID, FolderName: index.FolderName,
				RevisionID: index.RevisionID, LocalOnly: true, Locations: index.Original.Local, Variants: []string{}}, index.FileID)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
