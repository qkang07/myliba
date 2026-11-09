package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
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

func (c *cloudClient) libraryLifecycleStates() (map[string]string, error) {
	states := map[string]string{}
	if c.runtime.db == nil {
		return states, nil
	}
	rows, err := c.runtime.db.Query(`SELECT file_id,state FROM library_file_lifecycle`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			rows.Close()
			return nil, err
		}
		states[id] = state
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	deleted, err := c.runtime.db.Query(`SELECT file_id FROM library_file_deletions`)
	if err != nil {
		return nil, err
	}
	defer deleted.Close()
	for deleted.Next() {
		var id string
		if err := deleted.Scan(&id); err != nil {
			return nil, err
		}
		states[id] = "deleted"
	}
	return states, deleted.Err()
}

// searchLibrary is the paginated unified-search contract. The legacy
// /api/cloud/files response remains for compatibility, but callers that need
// a library view should filter after cloud/device deduplication, never before.
func (c *cloudClient) searchLibrary(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	scope := query.Get("scope")
	if scope == "" {
		scope = "all"
	}
	if scope != "cloud" && scope != "all" && scope != "local" && !strings.HasPrefix(scope, "device:") {
		writeError(w, http.StatusBadRequest, "检索范围无效")
		return
	}
	deviceID := strings.TrimPrefix(scope, "device:")
	if strings.HasPrefix(scope, "device:") && (deviceID == "" || len(deviceID) > 160) {
		writeError(w, http.StatusBadRequest, "设备范围无效")
		return
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeError(w, http.StatusBadRequest, "limit 应在 1 到 200 之间")
			return
		}
		limit = value
	}
	lifecycle := query.Get("lifecycle")
	if lifecycle != "" && lifecycle != "active" && lifecycle != "trashed" {
		writeError(w, http.StatusBadRequest, "回收状态无效")
		return
	}
	needle := strings.ToLower(strings.TrimSpace(query.Get("query")))
	folderID := strings.TrimSpace(query.Get("folderId"))
	fileType := strings.TrimSpace(query.Get("type"))
	grouped := strings.TrimSpace(query.Get("grouped"))
	cloudCopy := strings.TrimSpace(query.Get("cloudCopy"))
	readable := strings.TrimSpace(query.Get("readable"))
	if fileType != "" && fileType != "image" && fileType != "video" && fileType != "audio" && fileType != "document" && fileType != "other" || grouped != "" && grouped != "true" && grouped != "false" || cloudCopy != "" && cloudCopy != "true" && cloudCopy != "false" || readable != "" && readable != "true" && readable != "false" {
		writeError(w, http.StatusBadRequest, "筛选条件无效")
		return
	}
	order := query.Get("sort")
	switch order {
	case "", "name_asc", "name_desc", "modified_desc", "modified_asc", "ingested_desc", "ingested_asc":
	default:
		writeError(w, http.StatusBadRequest, "排序方式无效")
		return
	}
	wantedTags := []string{}
	seenTags := map[string]bool{}
	for _, tag := range query["tag"] {
		tag = strings.TrimSpace(tag)
		if tag != "" && !seenTags[strings.ToLower(tag)] {
			seenTags[strings.ToLower(tag)] = true
			wantedTags = append(wantedTags, tag)
		}
	}
	sort.Strings(wantedTags)
	peerStatuses := c.runtime.peerStatuses()
	cutoff := time.Now().Add(-time.Minute)
	availability := ""
	if readable != "" {
		peers := make([]string, 0, len(peerStatuses))
		for id, peer := range peerStatuses {
			if peer.State == "connected" && peer.CheckedAt.After(cutoff) {
				peers = append(peers, id)
			}
		}
		sort.Strings(peers)
		availability = strings.Join(peers, "\x00")
	}
	states, err := c.libraryLifecycleStates()
	if err != nil {
		writeError(w, 500, "无法读取文件回收状态")
		return
	}
	stateKeys := make([]string, 0, len(states))
	for id, state := range states {
		stateKeys = append(stateKeys, id+":"+state)
	}
	sort.Strings(stateKeys)
	lifecycleGeneration := cloudHash([]byte(strings.Join(stateKeys, "\x00")))[:16]
	wantedLifecycle := lifecycle
	if wantedLifecycle == "" {
		wantedLifecycle = "active"
	}
	if err := c.ensureSearchProjection(); err != nil {
		writeError(w, 500, "无法刷新检索投影")
		return
	}
	projectionReady := c.searchProjectionReady()
	// The generation binds a cursor to the exact index projection it paged
	// through. Any catalog or device-index change rewrites it, so a stale cursor
	// is rejected instead of silently skipping or duplicating rows (§6.1).
	var catalogGen, lastCatalogAt string
	var sourceList []string
	var sourceLastCatalogAt map[string]string
	var allRevisions []cloudFileView
	if projectionReady {
		gen, used, genErr := c.searchProjectionGeneration(scope)
		if genErr != nil {
			writeError(w, 500, "无法读取检索代号")
			return
		}
		if !used {
			projectionReady = false
		} else {
			catalogGen = gen
		}
	}
	if projectionReady {
		last, sources, lastMap, used, covErr := c.searchProjectionCoverage(scope)
		if covErr != nil {
			writeError(w, 500, "无法读取检索覆盖")
			return
		}
		if !used {
			projectionReady = false
		} else {
			lastCatalogAt, sourceList, sourceLastCatalogAt = last, sources, lastMap
		}
	}
	if !projectionReady {
		// Legacy fixtures without the unified-library migration merge the
		// catalog in Go; generation and coverage then come from the same set.
		allRevisions, err = c.libraryFiles(scope != "cloud")
		if err != nil {
			writeError(w, 500, "无法读取统一资源库")
			return
		}
		selections, selErr := c.currentRevisionSelections()
		if selErr != nil {
			writeError(w, 500, "无法读取当前版本")
			return
		}
		for at := range allRevisions {
			allRevisions[at].CurrentRevision = selections[allRevisions[at].FileID] == allRevisions[at].RevisionID && allRevisions[at].RevisionID != ""
		}
		catalogGen = catalogGeneration(allRevisions)
		sourceSet := map[string]bool{}
		sourceLastCatalogAt = map[string]string{}
		for _, file := range allRevisions {
			if file.CatalogCreatedAt > lastCatalogAt {
				lastCatalogAt = file.CatalogCreatedAt
			}
			for _, location := range file.Locations {
				if location.NodeID == "" {
					continue
				}
				sourceSet[location.NodeID] = true
				if file.CatalogCreatedAt != "" && (sourceLastCatalogAt[location.NodeID] == "" || catalogAfter(cloudRecord{CreatedAt: file.CatalogCreatedAt}, cloudRecord{CreatedAt: sourceLastCatalogAt[location.NodeID]})) {
					sourceLastCatalogAt[location.NodeID] = file.CatalogCreatedAt
				}
			}
		}
		for node := range sourceSet {
			sourceList = append(sourceList, node)
		}
		sort.Strings(sourceList)
	}
	generation := cloudHash([]byte(catalogGen + ":" + lifecycleGeneration + ":" + wantedLifecycle))[:16]
	filterGeneration := cloudHash([]byte(generation + "\x00" + scope + "\x00" + needle + "\x00" + folderID + "\x00" + fileType + "\x00" + grouped + "\x00" + cloudCopy + "\x00" + readable + "\x00" + availability + "\x00" + strings.Join(wantedTags, "\x00") + "\x00" + order + "\x00" + strconv.Itoa(limit)))[:16]
	offset := 0
	if raw := query.Get("cursor"); raw != "" {
		cursorGen, cursorOffset, ok := strings.Cut(raw, ":")
		value, convErr := strconv.Atoi(cursorOffset)
		if !ok || cursorGen != filterGeneration || convErr != nil || value < 0 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "索引或检索条件已更新，请刷新后重新检索", "code": "cursorExpired"})
			return
		}
		offset = value
	}
	matchesText := func(file cloudFileView) bool {
		if needle == "" {
			return true
		}
		if strings.Contains(strings.ToLower(file.Name), needle) || strings.Contains(strings.ToLower(file.FolderName), needle) {
			return true
		}
		for _, tag := range file.Tags {
			if strings.Contains(strings.ToLower(tag), needle) {
				return true
			}
		}
		return false
	}
	matchesFolder := func(file cloudFileView) bool {
		if folderID == "" {
			return true
		}
		if folderID == "unfiled" {
			return file.FolderID == ""
		}
		if strings.HasPrefix(folderID, "name:") {
			return strings.EqualFold(strings.TrimPrefix(folderID, "name:"), file.FolderName)
		}
		return folderID == file.FolderID
	}
	matchesTags := func(file cloudFileView) bool {
		for _, wanted := range wantedTags {
			found := false
			for _, tag := range file.Tags {
				if strings.EqualFold(tag, wanted) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	folders := map[string]libraryFolderFacet{}
	tagCounts := map[string]int{}
	var pageItems []cloudFileView
	total := 0
	sqlDone := false
	// Without a live-readability predicate the whole page, count and facets come
	// from SQLite; replica availability is still overlaid per row on read.
	if projectionReady && readable == "" {
		projectedFolders, projectedTags, used, facetErr := c.searchProjectionFacets(scope, wantedLifecycle, needle, folderID, fileType, grouped, cloudCopy, wantedTags)
		if facetErr != nil {
			writeError(w, 500, "无法读取检索 facet")
			return
		}
		if used {
			projectedPage, projectedTotal, pageUsed, pageErr := c.searchProjectionPage(scope, wantedLifecycle, needle, folderID, fileType, grouped, cloudCopy, order, wantedTags, limit, offset)
			if pageErr != nil {
				writeError(w, 500, "无法分页读取检索投影")
				return
			}
			if pageUsed {
				for _, facet := range projectedFolders {
					folders[facet.ID] = facet
				}
				for _, facet := range projectedTags {
					tagCounts[facet.Name] = facet.Count
				}
				pageItems, total, sqlDone = projectedPage, projectedTotal, true
			}
		}
	}
	if !sqlDone {
		files := []cloudFileView{}
		candidatesLoaded := false
		if projectionReady {
			projected, used, searchErr := c.readSearchCandidates(scope, needle, fileType, grouped, cloudCopy)
			if searchErr != nil {
				writeError(w, 500, "无法读取检索投影")
				return
			}
			if used {
				candidatesLoaded = true
				for _, file := range projected {
					state := states[file.FileID]
					if state == "" {
						state = "active"
					}
					if state == wantedLifecycle {
						files = append(files, file)
					}
				}
			}
		}
		if !candidatesLoaded {
			if allRevisions == nil {
				allRevisions, err = c.libraryFiles(scope != "cloud")
				if err != nil {
					writeError(w, 500, "无法读取统一资源库")
					return
				}
				selections, selErr := c.currentRevisionSelections()
				if selErr != nil {
					writeError(w, 500, "无法读取当前版本")
					return
				}
				for at := range allRevisions {
					allRevisions[at].CurrentRevision = selections[allRevisions[at].FileID] == allRevisions[at].RevisionID && allRevisions[at].RevisionID != ""
				}
			}
			visible := make([]cloudFileView, 0, len(allRevisions))
			for _, file := range allRevisions {
				state := states[file.FileID]
				if state == "" {
					state = "active"
				}
				if state == wantedLifecycle {
					visible = append(visible, file)
				}
			}
			files = currentLibraryFiles(visible)
		}
		matched := make([]cloudFileView, 0, len(files))
		isReadable := func(file cloudFileView) bool {
			if !file.LocalOnly && (file.AvailableReplicas > 0 || file.Chunks == 0) {
				return true
			}
			for _, location := range file.Locations {
				if location.NodeID == c.runtime.id {
					return true
				}
				if peer, ok := peerStatuses[location.NodeID]; ok && peer.State == "connected" && peer.CheckedAt.After(cutoff) {
					return true
				}
			}
			return false
		}
		for _, file := range files {
			if scope == "local" || strings.HasPrefix(scope, "device:") {
				wanted := deviceID
				if scope == "local" {
					wanted = c.runtime.id
				}
				found := false
				for _, location := range file.Locations {
					if location.NodeID == wanted {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			if fileType != "" && catalogFileType(file.Name) != fileType {
				continue
			}
			if grouped == "true" && file.FolderID == "" || grouped == "false" && file.FolderID != "" {
				continue
			}
			if cloudCopy == "true" && file.LocalOnly || cloudCopy == "false" && !file.LocalOnly {
				continue
			}
			if readable == "true" && !isReadable(file) || readable == "false" && isReadable(file) {
				continue
			}
			if !matchesText(file) {
				continue
			}
			if matchesTags(file) {
				id, name := file.FolderID, file.FolderName
				if id == "" {
					id, name = "unfiled", "未归组"
				}
				facet := folders[id]
				facet.ID, facet.Name, facet.Count = id, name, facet.Count+1
				folders[id] = facet
			}
			if matchesFolder(file) {
				seen := map[string]bool{}
				for _, tag := range file.Tags {
					key := strings.ToLower(tag)
					if !seen[key] {
						seen[key] = true
						tagCounts[tag]++
					}
				}
			}
			if matchesFolder(file) && matchesTags(file) {
				matched = append(matched, file)
			}
		}
		switch order {
		case "name_desc":
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].Name > matched[j].Name })
		case "modified_desc":
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].Modified > matched[j].Modified })
		case "modified_asc":
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].Modified < matched[j].Modified })
		case "ingested_desc":
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].CatalogCreatedAt > matched[j].CatalogCreatedAt })
		case "ingested_asc":
			sort.SliceStable(matched, func(i, j int) bool { return matched[i].CatalogCreatedAt < matched[j].CatalogCreatedAt })
		}
		total = len(matched)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		pageItems = matched[offset:end]
	}
	if folderID != "" {
		if _, found := folders[folderID]; !found {
			name := "未归组"
			if folderID != "unfiled" {
				name = folderID
			}
			folders[folderID] = libraryFolderFacet{ID: folderID, Name: name}
		}
	}
	for _, tag := range wantedTags {
		found := false
		for existing := range tagCounts {
			if strings.EqualFold(existing, tag) {
				found = true
				break
			}
		}
		if !found {
			tagCounts[tag] = 0
		}
	}
	folderItems := make([]libraryFolderFacet, 0, len(folders))
	for _, facet := range folders {
		folderItems = append(folderItems, facet)
	}
	sort.Slice(folderItems, func(i, j int) bool {
		if folderItems[i].Name == folderItems[j].Name {
			return folderItems[i].ID < folderItems[j].ID
		}
		return folderItems[i].Name < folderItems[j].Name
	})
	tagItems := make([]libraryTagFacet, 0, len(tagCounts))
	for name, count := range tagCounts {
		tagItems = append(tagItems, libraryTagFacet{Name: name, Count: count})
	}
	sort.Slice(tagItems, func(i, j int) bool { return tagItems[i].Name < tagItems[j].Name })
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	next := ""
	if end < total {
		next = filterGeneration + ":" + strconv.Itoa(end)
	}
	sources := map[string]bool{}
	for _, node := range sourceList {
		sources[node] = true
	}
	knownNodes := map[string]bool{}
	for node := range sources {
		knownNodes[node] = true
	}
	sourceFrontiers := map[string]map[string]int64{}
	if c.runtime.db != nil {
		rows, err := c.runtime.db.Query(`SELECT node_id,frontiers_json FROM library_receipts`)
		if err != nil {
			writeError(w, 500, "无法读取来源应用前沿")
			return
		}
		for rows.Next() {
			var node, raw string
			if err := rows.Scan(&node, &raw); err != nil {
				rows.Close()
				writeError(w, 500, "无法读取来源应用前沿")
				return
			}
			knownNodes[node] = true
			var frontiers map[string]int64
			if json.Unmarshal([]byte(raw), &frontiers) == nil {
				sourceFrontiers[node] = frontiers
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(w, 500, "无法读取来源应用前沿")
			return
		}
		rows.Close()
	}
	knownList := make([]string, 0, len(knownNodes))
	unindexedSources := []string{}
	for node := range knownNodes {
		knownList = append(knownList, node)
		if !sources[node] {
			unindexedSources = append(unindexedSources, node)
		}
	}
	sort.Strings(knownList)
	sort.Strings(unindexedSources)
	offlineSources := []string{}
	for _, node := range knownList {
		if node == c.runtime.id {
			continue
		}
		status, ok := peerStatuses[node]
		if !ok || status.State != "connected" || !status.CheckedAt.After(cutoff) {
			offlineSources = append(offlineSources, node)
		}
	}
	c.jobsMu.Lock()
	jobs := make(map[string]string, len(c.jobs))
	for id, status := range c.jobs {
		jobs[id] = status
	}
	c.jobsMu.Unlock()
	c.runtime.mu.RLock()
	lan := append([]string(nil), c.runtime.lan...)
	c.runtime.mu.RUnlock()
	writeJSON(w, 200, map[string]any{
		"items": pageItems, "total": total, "nextCursor": next,
		"generation": generation,
		"coverage": map[string]any{
			"scope": scope, "deviceIndexes": scope != "cloud", "generation": generation,
			"lastCatalogAt": lastCatalogAt, "sources": sourceList, "knownNodes": knownList, "unindexedSources": unindexedSources, "sourceLastCatalogAt": sourceLastCatalogAt, "sourceFrontiers": sourceFrontiers, "offlineSources": offlineSources,
		},
		"facets": map[string]any{"folders": folderItems, "tags": tagItems},
		"jobs":   jobs, "nodeId": c.runtime.id, "libraryId": c.libraryID(), "peers": c.runtime.activePeers(),
		"peerStatus": c.runtime.peerStatuses(), "directEndpoint": c.runtime.endpoint, "lan": lan,
	})
}

func currentLibraryFiles(all []cloudFileView) []cloudFileView {
	chosen := map[string]int{}
	result := make([]cloudFileView, 0, len(all))
	for _, file := range all {
		id := file.FileID
		if id == "" {
			id = file.ID
		}
		if at, ok := chosen[id]; ok {
			result[at].RevisionCount++
			if libraryRevisionAfter(file, result[at]) {
				count := result[at].RevisionCount
				result[at] = file
				result[at].RevisionCount = count
			}
			continue
		}
		chosen[id] = len(result)
		file.RevisionCount = 1
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].FileID < result[j].FileID
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func libraryRevisionAfter(a, b cloudFileView) bool {
	if a.CurrentRevision != b.CurrentRevision {
		return a.CurrentRevision
	}
	aTime, aErr := time.Parse(time.RFC3339Nano, a.Modified)
	bTime, bErr := time.Parse(time.RFC3339Nano, b.Modified)
	if aErr == nil && bErr == nil && !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	if a.Modified != b.Modified {
		return a.Modified > b.Modified
	}
	if a.CatalogCreatedAt != b.CatalogCreatedAt {
		return catalogAfter(cloudRecord{CreatedAt: a.CatalogCreatedAt}, cloudRecord{CreatedAt: b.CatalogCreatedAt})
	}
	return a.RevisionID > b.RevisionID
}

func catalogFileType(name string) string {
	ext := strings.ToLower(name)
	if at := strings.LastIndexByte(ext, '.'); at >= 0 {
		ext = ext[at:]
	} else {
		return "other"
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".arw", ".dng", ".bmp", ".tif", ".tiff":
		return "image"
	case ".mp4", ".mov", ".mkv", ".avi", ".webm", ".m4v":
		return "video"
	case ".mp3", ".m4a", ".flac", ".wav", ".ogg", ".aac":
		return "audio"
	case ".pdf", ".txt", ".md", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx":
		return "document"
	default:
		return "other"
	}
}

// catalogGeneration derives an index generation from the deduplicated logical
// projection. It changes whenever a file, revision, classification, or location
// in the merged catalog changes, which is exactly what must invalidate a cursor.
func catalogGeneration(files []cloudFileView) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(files)))
	for _, file := range files {
		b.WriteByte('\n')
		b.WriteString(file.ID)
		b.WriteByte('\x1f')
		b.WriteString(file.FileID)
		b.WriteByte('\x1f')
		b.WriteString(file.Name)
		b.WriteByte('\x1f')
		b.WriteString(file.RevisionID)
		if file.CurrentRevision {
			b.WriteByte('!')
		}
		b.WriteByte('\x1f')
		b.WriteString(file.Modified)
		b.WriteByte('\x1f')
		b.WriteString(file.CatalogCreatedAt)
		b.WriteByte('\x1f')
		b.WriteString(strconv.FormatInt(file.OriginalSize, 10))
		b.WriteByte('\x1f')
		b.WriteString(file.FolderID)
		b.WriteByte('\x1f')
		b.WriteString(file.FolderName)
		tags := append([]string(nil), file.Tags...)
		sort.Strings(tags)
		for _, tag := range tags {
			b.WriteByte('\x1f')
			b.WriteString(tag)
		}
		locations := make([]string, 0, len(file.Locations))
		for _, location := range file.Locations {
			locations = append(locations, location.NodeID+"\x00"+location.StorageID+"\x00"+location.ResourceRef+"\x00"+location.Path)
		}
		sort.Strings(locations)
		for _, location := range locations {
			b.WriteByte('\x1e')
			b.WriteString(location)
		}
	}
	return cloudHash([]byte(b.String()))[:16]
}

// libraryFileDetail is the unified detail contract (§6.2, §7
// GET /api/library/files/{fileId}). It aggregates every revision and location of
// one logical file after cloud/device deduplication, and reports classification,
// content readability, and unresolved organization conflicts for that file.
func (c *cloudClient) libraryFileDetail(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	if fileID == "" || len(fileID) > 160 {
		writeError(w, http.StatusBadRequest, "文件标识无效")
		return
	}
	states, err := c.libraryLifecycleStates()
	if err != nil {
		writeError(w, 500, "无法读取文件状态")
		return
	}
	if states[fileID] == "deleted" {
		writeError(w, http.StatusGone, "文件条目已永久删除")
		return
	}
	files, err := c.libraryFiles(true)
	if err != nil {
		writeError(w, 500, "无法读取统一资源库")
		return
	}
	matched := make([]cloudFileView, 0, 4)
	for _, file := range files {
		logical := file.FileID
		if logical == "" {
			logical = file.ID
		}
		if logical == fileID || file.ID == fileID || file.SourceID == fileID {
			matched = append(matched, file)
		}
	}
	if len(matched) == 0 {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	// A revision is one deduplicated entry; locations merge across devices.
	type revisionView struct {
		RevisionID        string            `json:"revisionId"`
		Modified          string            `json:"modified"`
		Size              int64             `json:"size"`
		OriginalSize      int64             `json:"originalSize"`
		Chunks            int               `json:"chunks"`
		Replicas          int               `json:"replicas"`
		AvailableReplicas int               `json:"availableReplicas"`
		LocalOnly         bool              `json:"localOnly"`
		History           bool              `json:"history"`
		Variants          []string          `json:"variants"`
		Locations         []indexedLocation `json:"locations"`
	}
	revisions := make([]revisionView, 0, len(matched))
	locations := []indexedLocation{}
	name, folderID, folderName := "", "", ""
	tags := []string{}
	hasCloud, availableCloud := false, false
	for _, file := range matched {
		if name == "" {
			name = file.Name
		}
		if folderID == "" && file.FolderID != "" {
			folderID, folderName = file.FolderID, file.FolderName
		}
		if len(tags) == 0 && len(file.Tags) > 0 {
			tags = append([]string{}, file.Tags...)
		}
		if !file.LocalOnly {
			hasCloud = true
			if file.AvailableReplicas > 0 || file.Chunks == 0 {
				availableCloud = true
			}
		}
		locations = mergeIndexedLocations(locations, file.Locations)
		revisions = append(revisions, revisionView{
			RevisionID: file.RevisionID, Modified: file.Modified, Size: file.Size, OriginalSize: file.OriginalSize,
			Chunks: file.Chunks, Replicas: file.Replicas, AvailableReplicas: file.AvailableReplicas,
			LocalOnly: file.LocalOnly, History: file.History, Variants: file.Variants, Locations: file.Locations,
		})
	}
	seenRevisions := map[string]bool{}
	for _, revision := range revisions {
		seenRevisions[revision.RevisionID] = true
	}
	if records, recordErr := c.contentRecords(); recordErr == nil {
		for _, record := range records {
			manifest, openErr := c.openManifest(record)
			if openErr != nil || manifest.Protected {
				continue
			}
			logicalID := manifest.FileID
			if logicalID == "" {
				logicalID = record.ObjectID
			}
			if logicalID != fileID || seenRevisions[record.RevisionID] {
				continue
			}
			seenRevisions[record.RevisionID] = true
			replicas, available := c.replicaCounts(record)
			variants := []string{}
			for _, asset := range manifest.Assets {
				if asset.Kind == "video" {
					variants = append(variants, asset.Spec)
				}
			}
			sort.Strings(variants)
			revisions = append(revisions, revisionView{RevisionID: record.RevisionID, Modified: manifest.Modified,
				Size: manifest.ContentSize, OriginalSize: manifest.Size, Chunks: len(record.BlockIDs),
				Replicas: replicas, AvailableReplicas: available, History: manifest.History, Variants: variants, Locations: manifest.Local})
		}
	} else {
		writeError(w, 500, "无法读取文件版本")
		return
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Modified > revisions[j].Modified })

	// Content status mirrors §6.2: prefer a reachable cloud copy, then a reachable
	// source device, and name the concrete blocker otherwise.
	statuses := c.runtime.peerStatuses()
	checkedAt := time.Now().Add(-time.Minute)
	deviceReachable := false
	for _, location := range locations {
		if location.NodeID == "" || location.NodeID == c.runtime.id {
			deviceReachable = true
			continue
		}
		if status, ok := statuses[location.NodeID]; ok && status.State == "connected" && status.CheckedAt.After(checkedAt) {
			deviceReachable = true
		}
	}
	content := "unavailable"
	switch {
	case availableCloud:
		content = "readable"
	case deviceReachable && len(locations) > 0:
		content = "readable"
	case hasCloud:
		content = "cloudMissing"
	case len(locations) > 0:
		content = "sourceOffline"
	}

	folderConflicts := 0
	if c.indexer != nil && c.indexer.db != nil {
		_ = c.indexer.db.QueryRow(`SELECT COUNT(*) FROM library_folder_conflicts WHERE file_id=?`, fileID).Scan(&folderConflicts)
	}
	organization := "synced"
	if folderConflicts > 0 {
		organization = "conflict"
	}
	lifecycle := states[fileID]
	if lifecycle == "" {
		lifecycle = "active"
	}
	selected, err := c.currentRevisionSelections()
	if err != nil {
		writeError(w, 500, "无法读取当前版本")
		return
	}
	currentRevision := selected[fileID]
	if !seenRevisions[currentRevision] {
		currentRevision = ""
	}
	// Registered source locations carry the stable locationId the release
	// endpoints bind to; the catalog manifest locations above do not expose it.
	type releaseLocationView struct {
		LocationID  string `json:"locationId"`
		NodeID      string `json:"nodeId"`
		RevisionID  string `json:"revisionId"`
		State       string `json:"state"`
		ResourceRef string `json:"resourceRef,omitempty"`
	}
	releaseLocations := []releaseLocationView{}
	if c.runtime.db != nil {
		rows, locErr := c.runtime.db.Query(`SELECT location_id,node_id,COALESCE(revision_id,''),state,resource_ref FROM library_locations WHERE file_id=? ORDER BY updated_at DESC`, fileID)
		if locErr == nil {
			for rows.Next() {
				var loc releaseLocationView
				if rows.Scan(&loc.LocationID, &loc.NodeID, &loc.RevisionID, &loc.State, &loc.ResourceRef) == nil {
					releaseLocations = append(releaseLocations, loc)
				}
			}
			rows.Close()
		}
	}
	writeJSON(w, 200, map[string]any{
		"fileId": fileID, "name": name,
		"currentRevisionId": currentRevision,
		"lifecycle":         lifecycle,
		"folderId":          folderID, "folderName": folderName, "tags": tags,
		"revisions": revisions, "locations": locations, "releaseLocations": releaseLocations,
		"contentStatus": content, "organizationStatus": organization, "folderConflicts": folderConflicts,
	})
}

// resolveLibraryFile returns version-pinned read candidates. The existing
// authenticated local/Hub session authorizes the subsequent read route; a
// source candidate must still be selected and checked by its resource node.
func (c *cloudClient) resolveLibraryFile(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		RevisionID string `json:"revisionId"`
		Purpose    string `json:"purpose"`
	}
	if fileID == "" || len(fileID) > 160 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input) != nil || len(input.RevisionID) > 160 {
		writeError(w, http.StatusBadRequest, "读取请求无效")
		return
	}
	if input.Purpose == "" {
		input.Purpose = "original"
	}
	if input.Purpose != "original" && input.Purpose != "preview" && input.Purpose != "thumbnail" {
		writeError(w, http.StatusBadRequest, "读取用途无效")
		return
	}
	states, err := c.libraryLifecycleStates()
	if err != nil {
		writeError(w, 500, "无法读取文件状态")
		return
	}
	if states[fileID] == "deleted" {
		writeError(w, http.StatusGone, "文件条目已永久删除")
		return
	}
	files, err := c.libraryFiles(true)
	if err != nil {
		writeError(w, 500, "无法解析文件位置")
		return
	}
	matched := make([]cloudFileView, 0, 4)
	for _, file := range files {
		if file.FileID == fileID || file.ID == fileID || file.SourceID == fileID {
			matched = append(matched, file)
		}
	}
	if len(matched) == 0 {
		writeError(w, http.StatusNotFound, "文件不存在")
		return
	}
	resolvedFileID := matched[0].FileID
	if resolvedFileID == "" {
		resolvedFileID = fileID
	}
	if input.RevisionID == "" {
		selected, selectErr := c.currentRevisionSelections()
		if selectErr != nil {
			writeError(w, 500, "无法读取当前版本")
			return
		}
		for _, file := range matched {
			if file.RevisionID != "" && selected[file.FileID] == file.RevisionID {
				input.RevisionID = file.RevisionID
				break
			}
		}
	}
	if input.RevisionID == "" {
		versions := map[string]bool{}
		for _, file := range matched {
			versions[file.RevisionID] = true
		}
		if len(versions) > 1 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "此文件有多个版本，请先选择版本", "code": "revisionChoiceRequired"})
			return
		}
		matched = currentLibraryFiles(matched)
		input.RevisionID = matched[0].RevisionID
	}
	type candidate struct {
		Kind        string `json:"kind"`
		NodeID      string `json:"nodeId,omitempty"`
		ResourceRef string `json:"resourceRef,omitempty"`
		ObjectID    string `json:"objectId,omitempty"`
		RevisionID  string `json:"revisionId"`
		Available   bool   `json:"available"`
		Reason      string `json:"reason,omitempty"`
		PlainHash   string `json:"plainHash,omitempty"`
		ReadPath    string `json:"readPath,omitempty"`
		AssetType   string `json:"assetType,omitempty"`
		ReadTicket  string `json:"readTicket,omitempty"`
	}
	candidates := []candidate{}
	seen := map[string]bool{}
	statuses := c.runtime.peerStatuses()
	cutoff := time.Now().Add(-time.Minute)
	addFile := func(file cloudFileView, manifest *cloudManifest) {
		plainHash := ""
		if manifest != nil && input.Purpose == "original" {
			plainHash = manifest.PlainHash
		}
		for _, location := range file.Locations {
			if location.NodeID == "" || location.ResourceRef == "" {
				continue
			}
			key := "source/" + location.NodeID + "/" + location.ResourceRef
			if seen[key] {
				continue
			}
			seen[key] = true
			available := location.NodeID == c.runtime.id
			if status, ok := statuses[location.NodeID]; ok && status.State == "connected" && status.CheckedAt.After(cutoff) {
				available = true
			}
			reason := ""
			if !available {
				reason = "sourceOffline"
			}
			readPath := "/api/library/read/" + url.PathEscape(location.ResourceRef) + "/file"
			if input.Purpose == "thumbnail" || input.Purpose == "preview" && catalogFileType(file.Name) == "image" {
				candidates = append(candidates, candidate{Kind: "device", NodeID: location.NodeID, ResourceRef: location.ResourceRef, RevisionID: input.RevisionID, Available: false, Reason: "assetUnknown", AssetType: "thumbnail", ReadPath: "/api/library/read/" + url.PathEscape(location.ResourceRef) + "/thumbnail"})
			}
			if input.Purpose == "preview" && catalogFileType(file.Name) == "video" {
				candidates = append(candidates, candidate{Kind: "device", NodeID: location.NodeID, ResourceRef: location.ResourceRef, RevisionID: input.RevisionID, Available: false, Reason: "assetUnknown", AssetType: "videoPreview", ReadPath: "/api/library/read/" + url.PathEscape(location.ResourceRef) + "/video-preview?file=1"})
			}
			if input.Purpose != "thumbnail" {
				candidates = append(candidates, candidate{Kind: "device", NodeID: location.NodeID, ResourceRef: location.ResourceRef, RevisionID: input.RevisionID, Available: available, Reason: reason, PlainHash: plainHash, ReadPath: readPath})
			}
		}
		if !file.LocalOnly {
			key := "cloud/" + file.ID
			if !seen[key] {
				seen[key] = true
				if (input.Purpose == "thumbnail" || input.Purpose == "preview" && catalogFileType(file.Name) == "image") && manifest != nil {
					for _, asset := range manifest.Assets {
						if asset.Kind != "thumbnail" || len(asset.Parts) == 0 {
							continue
						}
						part := asset.Parts[0]
						available := true
						for _, chunk := range part.Chunks {
							if c.runtime.disk.has("blocks", chunk.ID) {
								continue
							}
							found := false
							for _, node := range c.runtime.blockLocations(chunk.ID) {
								if status, ok := statuses[node.ID]; ok && status.State == "connected" && status.CheckedAt.After(cutoff) {
									found = true
									break
								}
							}
							if !found {
								available = false
								break
							}
						}
						reason := ""
						if !available {
							reason = "cloudMissing"
						}
						candidates = append(candidates, candidate{Kind: "cloud", ObjectID: file.ID, RevisionID: input.RevisionID, Available: available, Reason: reason, PlainHash: part.PlainHash, AssetType: "thumbnail", ReadPath: "/api/cloud/files/" + url.PathEscape(file.ID) + "/versions/" + url.PathEscape(input.RevisionID) + "/assets/" + url.PathEscape(asset.ID) + "/parts/" + url.PathEscape(part.Name) + "?libraryRead=1"})
						break
					}
				}
				available := file.AvailableReplicas > 0 || file.Chunks == 0
				reason := ""
				if !available {
					reason = "cloudMissing"
				}
				if input.Purpose != "thumbnail" {
					candidates = append(candidates, candidate{Kind: "cloud", ObjectID: file.ID, RevisionID: input.RevisionID, Available: available, Reason: reason, PlainHash: plainHash, ReadPath: "/api/cloud/files/" + url.PathEscape(file.ID) + "/versions/" + url.PathEscape(input.RevisionID) + "/file?libraryRead=1"})
				}
			}
		}
	}
	for _, file := range matched {
		if file.RevisionID == input.RevisionID {
			var manifest *cloudManifest
			if !file.LocalOnly {
				if _, found, lookupErr := c.versionRecord(file.ID, input.RevisionID); lookupErr == nil {
					manifest = &found
				}
			}
			addFile(file, manifest)
		}
	}
	if len(candidates) == 0 {
		records, recordErr := c.contentRecords()
		if recordErr != nil {
			writeError(w, 500, "无法解析文件版本")
			return
		}
		for _, record := range records {
			if record.RevisionID != input.RevisionID {
				continue
			}
			manifest, openErr := c.openManifest(record)
			if openErr != nil || manifest.Protected {
				continue
			}
			logicalID := manifest.FileID
			if logicalID == "" {
				logicalID = record.ObjectID
			}
			if logicalID != fileID {
				continue
			}
			_, available := c.replicaCounts(record)
			addFile(cloudFileView{ID: record.ObjectID, RevisionID: record.RevisionID, Locations: manifest.Local, Chunks: len(record.BlockIDs), AvailableReplicas: available}, &manifest)
		}
	}
	if len(candidates) == 0 {
		writeJSON(w, 200, map[string]any{"fileId": fileID, "revisionId": input.RevisionID, "purpose": input.Purpose, "candidates": candidates, "reason": "revisionChanged"})
		return
	}
	for at := range candidates {
		item := &candidates[at]
		if item.ReadPath == "" {
			continue
		}
		purpose := input.Purpose
		if item.AssetType == "" {
			purpose = "original"
		}
		node, resource := item.NodeID, item.ResourceRef
		if item.Kind == "cloud" {
			node, resource = c.runtime.id, item.ObjectID
		}
		ticketPath, _, _ := strings.Cut(item.ReadPath, "?")
		ticket, signErr := c.signLibraryReadTicket(libraryReadTicket{FileID: resolvedFileID, RevisionID: input.RevisionID, Purpose: purpose, NodeID: node, ResourceRef: resource, Path: ticketPath})
		if signErr != nil {
			writeError(w, 500, "无法签发读取凭据")
			return
		}
		item.ReadTicket = ticket
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		rank := func(item candidate) int {
			if item.AssetType != "" {
				if item.Available || item.Kind == "device" && (item.NodeID == c.runtime.id || statuses[item.NodeID].State == "connected") {
					return 0
				}
				return 5
			}
			if item.Kind == "device" && item.NodeID == c.runtime.id {
				return 1
			}
			if item.Kind == "device" && item.Available {
				return 1
			}
			if item.Kind == "cloud" && item.Available {
				return 2
			}
			if item.Kind == "device" {
				return 3
			}
			return 4
		}
		return rank(candidates[i]) < rank(candidates[j])
	})
	writeJSON(w, 200, map[string]any{"fileId": fileID, "revisionId": input.RevisionID, "purpose": input.Purpose, "candidates": candidates})
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
	keyFor := func(id, revision, modified string, size int64) string {
		if revision != "" {
			return id + "/revision/" + revision
		}
		// Legacy records without a revision retain the compatibility heuristic.
		return fmt.Sprintf("%s/legacy/%d/%s", id, size, modified)
	}
	add := func(file cloudFileView, id string) {
		// Catalog records keep their legacy object ID for backwards-compatible
		// retrieval.  When an index is available, use its logical file ID solely
		// as the result-deduplication key so an upload and its source location
		// remain one result.
		logicalID := id
		if file.FileID != "" {
			logicalID = file.FileID
		}
		if _, known := organization[id]; !known && file.Protected {
			return
		}
		if info, ok := organization[id]; ok {
			if info.FileID != "" {
				logicalID = info.FileID
			}
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
		file.FileID = logicalID
		key := keyFor(logicalID, file.RevisionID, file.Modified, file.OriginalSize)
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
			if index.Deleted || index.Protected {
				continue
			}
			record := latest[key]
			add(cloudFileView{FileID: index.FileID, ID: index.FileID + "@" + index.RevisionID, SourceID: record.ObjectID, Name: index.Name, Modified: index.Modified, CatalogCreatedAt: record.CreatedAt, CatalogFormat: record.Format,
				OriginalSize: index.Original.Size, Size: index.ContentSize, Tags: index.Tags, FolderID: index.FolderID, FolderName: index.FolderName,
				RevisionID: index.RevisionID, LocalOnly: true, Locations: index.Original.Local, Variants: []string{}}, index.FileID)
		}
	}
	// An applied classification operation outranks the classification copied
	// into an older cloud manifest or device index. The marker also represents
	// explicit empty tags and an explicit move to the unfiled state.
	if c.runtime != nil && c.runtime.db != nil {
		type classification struct {
			folderID, folderName string
			tags                 []string
			protected            bool
		}
		current := map[string]*classification{}
		rows, err := c.runtime.db.Query(`SELECT a.file_id,COALESCE(f.id,''),COALESCE(f.name,''),COALESCE(f.password_hash,'') FROM library_classification_authoritative a LEFT JOIN library_file_folders lf ON lf.file_id=a.file_id LEFT JOIN folders f ON f.id=lf.folder_id`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, folderID, name, password string
			if err = rows.Scan(&id, &folderID, &name, &password); err != nil {
				rows.Close()
				return nil, err
			}
			current[id] = &classification{folderID: folderID, folderName: name, tags: []string{}, protected: password != ""}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		rows, err = c.runtime.db.Query(`SELECT ft.file_id,t.name FROM library_file_tags ft JOIN library_classification_authoritative a ON a.file_id=ft.file_id JOIN tags t ON t.id=ft.tag_id ORDER BY t.name`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			if err = rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			if item := current[id]; item != nil {
				item.tags = append(item.tags, name)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		legacy := map[string]*classification{}
		rows, err = c.runtime.db.Query(`SELECT b.file_id,b.folder_name,b.tags_json,COALESCE(i.folder_id,'') FROM library_legacy_baselines b LEFT JOIN library_legacy_folder_identities i ON i.record_hash=b.record_hash`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name, raw, stableID string
			if err = rows.Scan(&id, &name, &raw, &stableID); err != nil {
				rows.Close()
				return nil, err
			}
			var tags []string
			if err = json.Unmarshal([]byte(raw), &tags); err != nil {
				rows.Close()
				return nil, err
			}
			if tags == nil {
				tags = []string{}
			}
			folderID := stableID
			if folderID == "" && name != "" {
				folderID = "name:" + strings.ToLower(name) // pre-migration fallback
			}
			legacy[id] = &classification{folderID: folderID, folderName: name, tags: tags}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		localAliases := map[string]bool{}
		explicitMoves := map[string]bool{}
		if moves, moveErr := c.runtime.db.Query(`SELECT file_id FROM library_file_folder_state`); moveErr == nil {
			for moves.Next() {
				var id string
				if err := moves.Scan(&id); err != nil {
					moves.Close()
					return nil, err
				}
				explicitMoves[id] = true
			}
			err = moves.Err()
			moves.Close()
			if err != nil {
				return nil, err
			}
		} else {
			return nil, moveErr
		}
		rows, err = c.runtime.db.Query(`SELECT DISTINCT file_id FROM library_resource_aliases`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			localAliases[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		filtered := result[:0]
		fileNames := map[string]string{}
		if rows, nameErr := c.runtime.db.Query(`SELECT file_id,current_name FROM library_file_name_state`); nameErr == nil {
			for rows.Next() {
				var id, name string
				if err = rows.Scan(&id, &name); err != nil {
					rows.Close()
					return nil, err
				}
				fileNames[id] = name
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		} else if !strings.Contains(nameErr.Error(), "no such table") {
			return nil, nameErr
		}
		for _, file := range result {
			if name := fileNames[file.FileID]; name != "" {
				file.Name = name
			}
			if file.CatalogFormat > 0 && file.CatalogFormat < 4 && !localAliases[file.FileID] && current[file.FileID] == nil {
				if baseline := legacy[file.FileID]; baseline != nil {
					file.FolderID, file.FolderName, file.Tags = baseline.folderID, baseline.folderName, append([]string{}, baseline.tags...)
				}
			}
			if item := current[file.FileID]; item != nil {
				if item.protected {
					continue
				}
				file.FolderID, file.FolderName, file.Tags = item.folderID, item.folderName, item.tags
			}
			if file.CatalogFormat > 0 && file.CatalogFormat < 4 && !localAliases[file.FileID] && !explicitMoves[file.FileID] {
				if baseline := legacy[file.FileID]; baseline != nil {
					file.FolderID, file.FolderName = baseline.folderID, baseline.folderName
				}
			}
			filtered = append(filtered, file)
		}
		result = filtered
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].ID < result[j].ID
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}
