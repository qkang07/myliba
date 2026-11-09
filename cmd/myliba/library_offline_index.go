package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type offlineLibraryFile struct {
	cloudFileView
	Lifecycle  string `json:"lifecycle"`
	IngestedAt string `json:"ingestedAt"`
}

// A complete, generation-bound metadata feed for clients that explicitly
// maintain an offline index. Live replica counts are cleared: cached entries
// are never evidence that a source or a cloud block is currently readable.
func (c *cloudClient) offlineLibraryIndex(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "cloud" {
		writeError(w, 400, "离线索引范围无效")
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			writeError(w, 400, "limit 应在 1 到 200 之间")
			return
		}
		limit = value
	}
	files, err := c.libraryFiles(scope == "all")
	if err != nil {
		writeError(w, 500, "无法读取离线索引")
		return
	}
	selections, err := c.currentRevisionSelections()
	if err != nil {
		writeError(w, 500, "无法读取当前版本")
		return
	}
	for at := range files {
		files[at].CurrentRevision = selections[files[at].FileID] == files[at].RevisionID && files[at].RevisionID != ""
	}
	states, err := c.libraryLifecycleStates()
	if err != nil {
		writeError(w, 500, "无法读取文件状态")
		return
	}
	stateKeys := make([]string, 0, len(states))
	for id, state := range states {
		stateKeys = append(stateKeys, id+":"+state)
	}
	sort.Strings(stateKeys)
	generation := cloudHash([]byte(catalogGeneration(files) + "\x00" + strings.Join(stateKeys, "\x00") + "\x00" + scope))[:24]
	current := currentLibraryFiles(files)
	items := make([]offlineLibraryFile, 0, len(current))
	for _, file := range current {
		state := states[file.FileID]
		if state == "deleted" {
			continue
		}
		if state == "" {
			state = "active"
		}
		file.AvailableReplicas = 0
		items = append(items, offlineLibraryFile{cloudFileView: file, Lifecycle: state, IngestedAt: file.CatalogCreatedAt})
	}
	offset := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		prefix, raw, ok := strings.Cut(cursor, ":")
		value, parseErr := strconv.Atoi(raw)
		if !ok || prefix != generation || parseErr != nil || value < 0 {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "离线索引已变化，请重新下载", "code": "cursorExpired"})
			return
		}
		offset = value
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	next := ""
	if end < len(items) {
		next = generation + ":" + strconv.Itoa(end)
	}
	writeJSON(w, 200, map[string]any{"items": items[offset:end], "total": len(items), "nextCursor": next, "generation": generation, "libraryId": c.libraryID(), "nodeId": c.runtime.id, "syncedAt": time.Now().UTC().Format(time.RFC3339Nano)})
}
