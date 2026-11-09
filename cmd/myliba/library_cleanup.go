package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const libraryCleanupRetention = 30 * 24 * time.Hour

type libraryCleanupItem struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

type libraryCleanupPlan struct {
	PlanID            string               `json:"planId"`
	GeneratedAt       string               `json:"generatedAt"`
	RetentionDays     int                  `json:"retentionDays"`
	Eligible          []libraryCleanupItem `json:"eligible"`
	Blocked           []string             `json:"blocked"`
	CatalogGeneration string               `json:"catalogGeneration"`
	SnapshotFrontiers map[string]int64     `json:"snapshotFrontiers"`
	ActiveMembers     []string             `json:"activeMembers"`
	SnapshotHashes    map[string]string    `json:"snapshotHashes"`
}

// All catalog records, including records from other libraries, keep their
// ciphertext blocks alive. Unknown or unreadable records fail the plan closed.
func (c *cloudClient) cleanupReferences(ids []string) (map[string]bool, error) {
	referenced := map[string]bool{}
	for _, id := range ids {
		file, err := c.runtime.disk.get("catalog", id)
		if err != nil {
			return nil, err
		}
		var record cloudRecord
		err = json.NewDecoder(file).Decode(&record)
		file.Close()
		if err != nil {
			return nil, err
		}
		for _, block := range record.BlockIDs {
			if !validCloudHash(block) {
				return nil, errors.New("invalid catalog block")
			}
			referenced[block] = true
		}
	}
	return referenced, nil
}

func (c *cloudClient) cleanupGate(ids []string) (map[string]int64, []string, []string, error) {
	frontiers := map[string]int64{}
	blocked := []string{}
	members := []string{}
	if c.runtime.db == nil {
		return frontiers, members, []string{"查询节点数据库不可用"}, nil
	}
	rows, err := c.runtime.db.Query(`SELECT author_id,last_counter FROM library_operation_frontiers`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var author string
		var counter int64
		if err = rows.Scan(&author, &counter); err != nil {
			break
		}
		frontiers[author] = counter
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	if len(frontiers) == 0 {
		blocked = append(blocked, "尚无可恢复操作前沿")
	}
	remote := map[string]bool{}
	for _, id := range ids {
		remote[id] = true
	}
	type snapshotSpan struct{ from, to int64 }
	segments := map[string][]snapshotSpan{}
	for _, id := range ids {
		file, openErr := c.runtime.disk.get("catalog", id)
		if openErr != nil {
			continue
		}
		var record cloudRecord
		decodeErr := json.NewDecoder(file).Decode(&record)
		file.Close()
		if decodeErr != nil || !strings.HasPrefix(record.ObjectID, "library-snapshot/") {
			continue
		}
		manifest, openErr := c.openManifest(record)
		if openErr != nil || manifest.Snapshot == nil {
			continue
		}
		snapshot := manifest.Snapshot
		if record.ObjectID != "library-snapshot/"+snapshot.AuthorID {
			continue
		}
		if _, openErr = decodeLibrarySnapshot(*snapshot); openErr != nil {
			continue
		}
		from := snapshot.FromCounter
		if from == 0 {
			from = 1
		}
		segments[snapshot.AuthorID] = append(segments[snapshot.AuthorID], snapshotSpan{from, snapshot.Frontier})
	}
	for author, counter := range frontiers {
		var published int64
		var hash string
		if c.runtime.db.QueryRow(`SELECT frontier,record_hash FROM library_snapshot_publication WHERE author_id=?`, author).Scan(&published, &hash) != nil || published < counter || !remote[hash] || !c.runtime.disk.valid("catalog", hash) {
			blocked = append(blocked, "作者 "+author+" 的持久快照未覆盖前沿")
			continue
		}
		covered := int64(0)
		for {
			next := covered
			for _, part := range segments[author] {
				if part.from <= covered+1 && part.to > next {
					next = part.to
				}
			}
			if next == covered {
				break
			}
			covered = next
			if covered >= counter {
				break
			}
		}
		if covered < counter {
			blocked = append(blocked, "作者 "+author+" 的快照分片不连续")
		}
	}
	rows, err = c.runtime.db.Query(`SELECT node_id FROM library_members WHERE state='active' ORDER BY node_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var member string
		if err = rows.Scan(&member); err != nil {
			break
		}
		members = append(members, member)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	if len(members) == 0 {
		blocked = append(blocked, "尚无活跃成员确认集合")
	}
	for _, member := range members {
		var raw string
		if c.runtime.db.QueryRow(`SELECT frontiers_json FROM library_receipts WHERE node_id=?`, member).Scan(&raw) != nil {
			blocked = append(blocked, "成员 "+member+" 尚未回执")
			continue
		}
		var seen map[string]int64
		if json.Unmarshal([]byte(raw), &seen) != nil {
			blocked = append(blocked, "成员 "+member+" 回执无效")
			continue
		}
		for author, counter := range frontiers {
			if seen[author] < counter {
				blocked = append(blocked, "成员 "+member+" 尚未覆盖作者 "+author)
			}
		}
	}
	sort.Strings(blocked)
	return frontiers, members, blocked, nil
}

// Content records of permanently deleted files keep their blocks alive until
// the same gate passes: the deletion is covered by a persisted snapshot, every
// active member receipt covers the author frontier, and retention has elapsed.
func (c *cloudClient) appendDeletedContent(plan *libraryCleanupPlan, local []string, allRecords map[string]cloudRecord) error {
	if c.runtime.db == nil {
		return nil
	}
	type fileDeletion struct {
		author    string
		counter   int64
		deletedAt string
	}
	deletions := map[string]fileDeletion{}
	rows, err := c.runtime.db.Query(`SELECT file_id,author_id,counter,deleted_at FROM library_file_deletions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var fileID string
		var deletion fileDeletion
		if err = rows.Scan(&fileID, &deletion.author, &deletion.counter, &deletion.deletedAt); err != nil {
			break
		}
		deletions[fileID] = deletion
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	contentCount := 0
	for _, id := range local {
		record := allRecords[id]
		if record.LibraryID != c.libraryID() || strings.HasPrefix(record.ObjectID, "library-") || contentCount >= 1000 {
			continue
		}
		manifest, openErr := c.openManifest(record)
		if openErr != nil || manifest.IndexOnly || manifest.Operation != nil || manifest.Receipt != nil || manifest.Snapshot != nil {
			continue
		}
		fileID := manifest.FileID
		if fileID == "" {
			fileID = record.ObjectID
		}
		deletion, deleted := deletions[fileID]
		if !deleted || deletion.counter > plan.SnapshotFrontiers[deletion.author] {
			continue
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, deletion.deletedAt)
		if parseErr != nil || time.Since(stamp) < libraryCleanupRetention {
			continue
		}
		plan.Eligible = append(plan.Eligible, libraryCleanupItem{Kind: "content", ID: id})
		contentCount++
	}
	return nil
}

func (c *cloudClient) makeLibraryCleanupPlan(ctx context.Context) (libraryCleanupPlan, error) {
	plan := libraryCleanupPlan{GeneratedAt: now(), RetentionDays: 30, Eligible: []libraryCleanupItem{}, Blocked: []string{}, SnapshotFrontiers: map[string]int64{}, ActiveMembers: []string{}, SnapshotHashes: map[string]string{}}
	ws, token, err := c.runtime.credentials()
	if err != nil || ws == "" || len(token) < 32 {
		plan.Blocked = append(plan.Blocked, "Hub 不可达或未配置")
		return plan, nil
	}
	hub, err := hubHTTPURL(ws)
	if err != nil {
		plan.Blocked = append(plan.Blocked, "Hub 地址无效")
		return plan, nil
	}
	if err = c.runtime.syncCatalog(ctx, hub, token); err != nil {
		plan.Blocked = append(plan.Blocked, "无法同步 Hub 目录")
		return plan, nil
	}
	remote, err := c.runtime.remoteIDs(ctx, hub, token)
	if err != nil {
		plan.Blocked = append(plan.Blocked, "无法读取 Hub 目录")
		return plan, nil
	}
	local, err := c.runtime.disk.list("catalog")
	if err != nil {
		return plan, err
	}
	if strings.Join(remote, "\x00") != strings.Join(local, "\x00") {
		plan.Blocked = append(plan.Blocked, "Hub 与本机目录不一致")
		return plan, nil
	}
	plan.CatalogGeneration = cloudHash([]byte(strings.Join(remote, "\x00")))
	plan.SnapshotFrontiers, plan.ActiveMembers, plan.Blocked, err = c.cleanupGate(remote)
	if err != nil {
		return plan, err
	}
	if len(plan.Blocked) > 0 {
		return plan, nil
	}
	for author := range plan.SnapshotFrontiers {
		var hash string
		if c.runtime.db.QueryRow(`SELECT record_hash FROM library_snapshot_publication WHERE author_id=?`, author).Scan(&hash) == nil {
			plan.SnapshotHashes[author] = hash
		}
	}
	writerRecords := map[string]int{}
	allRecords := map[string]cloudRecord{}
	for _, id := range local {
		file, openErr := c.runtime.disk.get("catalog", id)
		if openErr != nil {
			return plan, openErr
		}
		var record cloudRecord
		decodeErr := json.NewDecoder(file).Decode(&record)
		file.Close()
		if decodeErr != nil {
			return plan, decodeErr
		}
		allRecords[id] = record
		if record.Format >= 4 {
			writerRecords[record.LibraryID+"\x00"+record.AuthorID]++
		}
	}
	for _, id := range local {
		record := allRecords[id]
		if record.LibraryID != c.libraryID() || !strings.HasPrefix(record.ObjectID, "library-operation/") || len(record.BlockIDs) != 0 {
			continue
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, record.CreatedAt)
		writerKey := record.LibraryID + "\x00" + record.AuthorID
		if parseErr == nil && time.Since(stamp) >= libraryCleanupRetention && len(plan.Eligible) < 1000 && writerRecords[writerKey] > 1 {
			plan.Eligible = append(plan.Eligible, libraryCleanupItem{Kind: "catalog", ID: id})
			writerRecords[writerKey]--
		}
	}
	if err = c.appendDeletedContent(&plan, local, allRecords); err != nil {
		return plan, err
	}
	referenced, err := c.cleanupReferences(local)
	if err != nil {
		plan.Blocked = append(plan.Blocked, "目录记录不可解析")
		return plan, nil
	}
	blocks, err := c.runtime.disk.list("blocks")
	if err != nil {
		return plan, err
	}
	for _, id := range blocks {
		if referenced[id] {
			continue
		}
		modified, size, statErr := c.runtime.disk.objectStat("blocks", id)
		if statErr != nil || time.Since(modified) < libraryCleanupRetention {
			continue
		}
		plan.Eligible = append(plan.Eligible, libraryCleanupItem{Kind: "block", ID: id, Bytes: size})
	}
	raw, _ := json.Marshal(struct {
		Catalog   string
		Items     []libraryCleanupItem
		Frontiers map[string]int64
		Members   []string
		Snapshots map[string]string
	}{plan.CatalogGeneration, plan.Eligible, plan.SnapshotFrontiers, plan.ActiveMembers, plan.SnapshotHashes})
	plan.PlanID = cloudHash(raw)
	return plan, nil
}

func (c *cloudClient) libraryCleanupPlanAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	plan, err := c.makeLibraryCleanupPlan(ctx)
	if err != nil {
		writeError(w, 500, "无法计算清理计划")
		return
	}
	writeJSON(w, 200, plan)
}

func (c *cloudClient) executeLibraryCleanup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PlanID string `json:"planId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validCloudHash(input.PlanID) {
		writeError(w, 400, "清理计划无效")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	plan, err := c.makeLibraryCleanupPlan(ctx)
	if err != nil {
		writeError(w, 500, "无法重新校验清理门槛")
		return
	}
	if len(plan.Blocked) > 0 || plan.PlanID != input.PlanID {
		writeJSON(w, 409, map[string]any{"error": "清理门槛或目录已变化，请重新查看计划", "plan": plan})
		return
	}
	// Recheck the local catalog and member gate before asking the Hub to mark
	// immutable operation records pruned. The Hub independently compares its
	// generation and retains durable prune markers against late re-uploads.
	c.runtime.disk.catalogMu.Lock()
	ids, err := c.runtime.disk.list("catalog")
	if err != nil || cloudHash([]byte(strings.Join(ids, "\x00"))) != plan.CatalogGeneration {
		c.runtime.disk.catalogMu.Unlock()
		writeError(w, 409, "目录已变化")
		return
	}
	_, _, blocked, gateErr := c.cleanupGate(ids)
	c.runtime.disk.catalogMu.Unlock()
	if gateErr != nil || len(blocked) != 0 {
		writeError(w, 409, "快照或成员回执门槛已变化")
		return
	}
	ws, token, credErr := c.runtime.credentials()
	if credErr != nil {
		writeError(w, 503, "Hub 凭据不可用")
		return
	}
	hub, hubErr := hubHTTPURL(ws)
	if hubErr != nil {
		writeError(w, 503, "Hub 地址无效")
		return
	}
	pruned := []string{}
	recycled := []string{}
	for _, item := range plan.Eligible {
		switch item.Kind {
		case "catalog":
			pruned = append(pruned, item.ID)
		case "content":
			recycled = append(recycled, item.ID)
		}
	}
	if len(pruned) > 0 || len(recycled) > 0 {
		payload, _ := json.Marshal(catalogPruneRequest{Generation: plan.CatalogGeneration, LibraryID: c.libraryID(), IDs: pruned, ContentIDs: recycled, Snapshots: plan.SnapshotHashes})
		response, requestErr := c.runtime.request(ctx, http.MethodPost, hub, "/cloud/catalog/prune", token, bytes.NewReader(payload))
		if requestErr != nil {
			writeError(w, 503, "Hub 目录压缩不可用")
			return
		}
		status := response.StatusCode
		response.Body.Close()
		if status != http.StatusOK {
			writeError(w, 409, "Hub 目录压缩门槛或目录已变化")
			return
		}
		if err = c.runtime.syncCatalog(ctx, hub, token); err != nil {
			writeError(w, 503, "Hub 已记录压缩，但本机尚未同步删除标记")
			return
		}
	}
	storagePeers := []cloudNode{}
	for _, peer := range c.runtime.activePeers() {
		if peer.Role == "storage" && peer.ID != c.runtime.id && peer.Endpoint != "" && validCloudEndpoint(peer.Endpoint) {
			storagePeers = append(storagePeers, peer)
		}
	}
	physical := len(storagePeers) > 0
	for _, item := range plan.Eligible {
		if item.Kind == "block" {
			physical = true
		}
	}
	leaseToken := ""
	if physical {
		// Physical deletion runs under a Hub lease: the member gate evidence is
		// re-verified against the post-prune generation and catalog writes are
		// blocked until every node finishes or the lease expires.
		remote, remoteErr := c.runtime.remoteIDs(ctx, hub, token)
		if remoteErr != nil {
			writeError(w, 503, "无法读取 Hub 目录，拒绝物理删除")
			return
		}
		leaseToken, err = c.authorizeCleanup(ctx, hub, token, plan, cloudHash([]byte(strings.Join(remote, "\x00"))))
		if err != nil {
			writeError(w, 409, "无法取得跨节点清理授权，拒绝物理删除")
			return
		}
		defer c.releaseCleanupLease(ctx, hub, token, leaseToken)
	}
	c.runtime.disk.catalogMu.Lock()
	ids, err = c.runtime.disk.list("catalog")
	if err != nil {
		c.runtime.disk.catalogMu.Unlock()
		writeError(w, 500, "无法重新读取目录")
		return
	}
	frontiers, _, blocked, gateErr := c.cleanupGate(ids)
	if gateErr != nil || len(blocked) > 0 {
		c.runtime.disk.catalogMu.Unlock()
		writeError(w, 409, "清理回执门槛已变化")
		return
	}
	referenced, err := c.cleanupReferences(ids)
	if err != nil {
		c.runtime.disk.catalogMu.Unlock()
		writeError(w, 409, "目录记录已变化")
		return
	}
	removed := []string{}
	for _, item := range plan.Eligible {
		if item.Kind != "block" || referenced[item.ID] {
			continue
		}
		modified, size, statErr := c.runtime.disk.objectStat("blocks", item.ID)
		if statErr != nil || size != item.Bytes || time.Since(modified) < libraryCleanupRetention {
			continue
		}
		if c.runtime.disk.remove("blocks", item.ID) == nil {
			removed = append(removed, item.ID)
		}
	}
	c.runtime.disk.catalogMu.Unlock()
	compacted, compactErr := c.compactFolderTombstones(frontiers)
	if compactErr != nil {
		writeError(w, 500, "文件夹墓碑压缩失败")
		return
	}
	storageResults := c.executeStorageCleanup(ctx, token, leaseToken, storagePeers)
	writeJSON(w, 200, map[string]any{"removedBlocks": removed, "removedCatalogRecords": pruned, "removedContentRecords": recycled, "compactedTombstones": compacted, "storageNodes": storageResults, "sourceOriginalsRemoved": 0})
}

func (c *cloudClient) authorizeCleanup(ctx context.Context, hub, token string, plan libraryCleanupPlan, generation string) (string, error) {
	payload, _ := json.Marshal(cleanupAuthorizeRequest{LibraryID: c.libraryID(), Generation: generation, SnapshotFrontiers: plan.SnapshotFrontiers, ActiveMembers: plan.ActiveMembers, SnapshotHashes: plan.SnapshotHashes})
	response, err := c.runtime.request(ctx, http.MethodPost, hub, "/cloud/cleanup/authorize", token, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New("Hub cleanup authorization: HTTP " + response.Status)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&grant) != nil || !validCloudHash(grant.Token) {
		return "", errors.New("invalid Hub cleanup authorization")
	}
	return grant.Token, nil
}

func (c *cloudClient) releaseCleanupLease(ctx context.Context, hub, token, leaseToken string) {
	payload, _ := json.Marshal(map[string]string{"token": leaseToken})
	if response, err := c.runtime.request(ctx, http.MethodPost, hub, "/cloud/cleanup/release", token, bytes.NewReader(payload)); err == nil {
		response.Body.Close()
	}
}

// Folder tombstones are compactable only after every active member receipt and
// a persisted snapshot cover the deleting author's frontier, so no future
// operation can arrive without having observed the delete.
func (c *cloudClient) compactFolderTombstones(frontiers map[string]int64) (int, error) {
	if c.runtime.db == nil {
		return 0, nil
	}
	rows, err := c.runtime.db.Query(`SELECT name,author_id,counter,deleted_at FROM library_folder_tombstones`)
	if err != nil {
		return 0, err
	}
	names := []string{}
	for rows.Next() {
		var name, author, deletedAt string
		var counter int64
		if err = rows.Scan(&name, &author, &counter, &deletedAt); err != nil {
			break
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, deletedAt)
		if frontier, ok := frontiers[author]; ok && parseErr == nil && counter <= frontier && time.Since(stamp) >= libraryCleanupRetention {
			names = append(names, name)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	compacted := 0
	for _, name := range names {
		if _, err = c.runtime.db.Exec(`DELETE FROM library_folder_tombstones WHERE name=?`, name); err != nil {
			return compacted, err
		}
		compacted++
	}
	return compacted, nil
}

func (c *cloudClient) executeStorageCleanup(ctx context.Context, token, leaseToken string, peers []cloudNode) []map[string]any {
	results := []map[string]any{}
	if leaseToken == "" {
		return results
	}
	for _, peer := range peers {
		result := map[string]any{"nodeId": peer.ID}
		response, err := c.runtime.request(ctx, http.MethodGet, peer.Endpoint, "/cloud/cleanup/plan", token, nil)
		if err != nil {
			result["error"] = "存储节点不可达"
			results = append(results, result)
			continue
		}
		var peerPlan cloudBlockCleanupPlan
		decodeErr := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&peerPlan)
		response.Body.Close()
		if decodeErr != nil {
			result["error"] = "存储节点清理计划无效"
			results = append(results, result)
			continue
		}
		if len(peerPlan.Blocked) > 0 {
			result["blocked"] = peerPlan.Blocked
			results = append(results, result)
			continue
		}
		if len(peerPlan.Eligible) == 0 {
			result["removedBlocks"] = []string{}
			results = append(results, result)
			continue
		}
		payload, _ := json.Marshal(map[string]string{"planId": peerPlan.PlanID, "cleanupToken": leaseToken})
		response, err = c.runtime.request(ctx, http.MethodPost, peer.Endpoint, "/cloud/cleanup/execute", token, bytes.NewReader(payload))
		if err != nil {
			result["error"] = "存储节点清理执行不可达"
			results = append(results, result)
			continue
		}
		var outcome struct {
			RemovedBlocks []string `json:"removedBlocks"`
			Error         string   `json:"error"`
		}
		decodeErr = json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&outcome)
		status := response.StatusCode
		response.Body.Close()
		if decodeErr != nil || status != http.StatusOK {
			if outcome.Error != "" {
				result["error"] = outcome.Error
			} else {
				result["error"] = "存储节点清理被拒绝"
			}
			if outcome.RemovedBlocks != nil {
				result["removedBlocks"] = outcome.RemovedBlocks
			}
			results = append(results, result)
			continue
		}
		result["removedBlocks"] = outcome.RemovedBlocks
		results = append(results, result)
	}
	return results
}
