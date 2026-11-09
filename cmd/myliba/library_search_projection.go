package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

func foldedTagKey(value string) string {
	var result strings.Builder
	for _, current := range value {
		lowest := current
		for next := unicode.SimpleFold(current); next != current; next = unicode.SimpleFold(next) {
			if next < lowest {
				lowest = next
			}
		}
		result.WriteRune(lowest)
	}
	return result.String()
}

type projectedLibraryFile struct {
	File             cloudFileView `json:"file"`
	CatalogCreatedAt string        `json:"catalogCreatedAt"`
	CatalogFormat    int           `json:"catalogFormat"`
	Protected        bool          `json:"protected"`
}

// libraryFiles reads a materialized SQLite projection when neither the
// encrypted catalog nor the local classification database has changed.
// Availability is overlaid on each read because peers and blocks can change
// without a catalog write. The projection is rebuilt after process startup.
func (c *cloudClient) libraryFiles(includeLocal bool) ([]cloudFileView, error) {
	if c.runtime.db == nil {
		return c.buildLibraryFiles(includeLocal)
	}
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if err := c.ensureSearchProjectionLocked(); err != nil {
		return nil, err
	}
	if !c.searchReady { // Minimal legacy fixtures have no unified-library migration.
		return c.buildLibraryFiles(includeLocal)
	}
	return c.readLibraryProjection(includeLocal)
}

// ensureSearchProjection refreshes the materialized projection without reading
// any rows back, so search can compute generation, facets and coverage in SQL.
func (c *cloudClient) ensureSearchProjection() error {
	if c.runtime.db == nil {
		return nil
	}
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	return c.ensureSearchProjectionLocked()
}

func (c *cloudClient) searchProjectionReady() bool {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	return c.runtime.db != nil && c.searchReady
}

func (c *cloudClient) ensureSearchProjectionLocked() error {
	db := c.runtime.db
	var hasTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='library_search_projection'`).Scan(&hasTable); err != nil {
		return err
	}
	if hasTable == 0 { // Minimal legacy fixtures have no unified-library migration.
		return nil
	}
	ids, err := c.runtime.disk.list("catalog")
	if err != nil {
		return err
	}
	stamp := cloudHash([]byte(c.libraryID() + "\x00" + strings.Join(ids, "\x00")))
	if c.indexer != nil {
		stamp += ":indexer"
	}
	var changes int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		return err
	}
	if c.searchReady && c.searchStamp == stamp && c.searchChanges == changes {
		return nil
	}
	cloud, err := c.buildLibraryFiles(false)
	if err != nil {
		return err
	}
	all, err := c.buildLibraryFiles(true)
	if err != nil {
		return err
	}
	// Keep the decrypted record headers only in process memory so replica
	// availability can be refreshed without decrypting the entire catalog.
	records, err := c.contentRecords()
	if err != nil {
		return err
	}
	lookup := make(map[string]cloudRecord, len(records))
	for _, record := range records {
		lookup[record.ObjectID+"\x00"+record.RevisionID] = record
	}
	var after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
		return err
	}
	if after != changes { // A concurrent local mutation needs another rebuild.
		c.searchReady = false
		return nil
	}
	latestIDs, err := c.runtime.disk.list("catalog")
	if err != nil {
		return err
	}
	if cloudHash([]byte(c.libraryID()+"\x00"+strings.Join(latestIDs, "\x00"))) != strings.TrimSuffix(stamp, ":indexer") {
		c.searchReady = false
		return nil
	}
	committedChanges, err := c.writeLibraryProjection(cloud, all)
	if err != nil {
		return err
	}
	c.searchChanges = committedChanges
	c.searchStamp, c.searchRecords, c.searchReady = stamp, lookup, true
	return nil
}

func (c *cloudClient) writeLibraryProjection(cloud, all []cloudFileView) (int64, error) {
	selections, err := c.currentRevisionSelections()
	if err != nil {
		return 0, err
	}
	tx, err := c.runtime.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM library_search_projection`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM library_search_fts`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM library_search_current`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM library_search_tags`); err != nil {
		return 0, err
	}
	statement, err := tx.Prepare(`INSERT INTO library_search_projection(scope,ordinal,file_id,revision_id,name,folder_id,search_text,file_json) VALUES(?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer statement.Close()
	for scope, files := range [][]cloudFileView{cloud, all} {
		for at, file := range files {
			encoded, marshalErr := json.Marshal(projectedLibraryFile{File: file, CatalogCreatedAt: file.CatalogCreatedAt, CatalogFormat: file.CatalogFormat, Protected: file.Protected})
			if marshalErr != nil {
				return 0, marshalErr
			}
			searchText := strings.ToLower(file.Name + "\x00" + file.FolderName + "\x00" + strings.Join(file.Tags, "\x00"))
			if _, err = statement.Exec(scope, at, file.FileID, file.RevisionID, file.Name, file.FolderID, searchText, string(encoded)); err != nil {
				return 0, err
			}
		}
		currentStatement, prepErr := tx.Prepare(`INSERT INTO library_search_current(scope,file_id,revision_id,name,folder_id,folder_name,modified,ingested_at,file_type,local_only,name_search,folder_search,tags_search,file_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if prepErr != nil {
			return 0, prepErr
		}
		for at := range files {
			files[at].CurrentRevision = selections[files[at].FileID] == files[at].RevisionID && files[at].RevisionID != ""
		}
		for _, file := range currentLibraryFiles(files) {
			encoded, marshalErr := json.Marshal(projectedLibraryFile{File: file, CatalogCreatedAt: file.CatalogCreatedAt, CatalogFormat: file.CatalogFormat, Protected: file.Protected})
			if marshalErr != nil {
				currentStatement.Close()
				return 0, marshalErr
			}
			localOnly := 0
			if file.LocalOnly {
				localOnly = 1
			}
			_, err = currentStatement.Exec(scope, file.FileID, file.RevisionID, file.Name, file.FolderID, file.FolderName, file.Modified, file.CatalogCreatedAt, catalogFileType(file.Name), localOnly, strings.ToLower(file.Name), strings.ToLower(file.FolderName), strings.ToLower(strings.Join(file.Tags, "\x1f")), string(encoded))
			if err != nil {
				currentStatement.Close()
				return 0, err
			}
			for _, tag := range file.Tags {
				if _, err = tx.Exec(`INSERT OR IGNORE INTO library_search_tags(scope,file_id,tag_key,tag_name) VALUES(?,?,?,?)`, scope, file.FileID, foldedTagKey(tag), tag); err != nil {
					currentStatement.Close()
					return 0, err
				}
			}
		}
		if err = currentStatement.Close(); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO library_search_fts(rowid,name_search,folder_search,tags_search) SELECT rowid,name_search,folder_search,tags_search FROM library_search_current`); err != nil {
		return 0, err
	}
	// The cursor generation is bound to the exact deduplicated revision set,
	// including the current-revision flags, so compute it inside the same
	// transaction that materializes the rows.
	if _, err = tx.Exec(`DELETE FROM library_search_meta`); err != nil {
		return 0, err
	}
	for scope, files := range [][]cloudFileView{cloud, all} {
		if _, err = tx.Exec(`INSERT INTO library_search_meta(scope,generation) VALUES(?,?)`, scope, catalogGeneration(files)); err != nil {
			return 0, err
		}
	}
	var changes int64
	if err := tx.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return changes, nil
}

func (c *cloudClient) readLibraryProjection(includeLocal bool) ([]cloudFileView, error) {
	scope := 0
	if includeLocal {
		scope = 1
	}
	rows, err := c.runtime.db.Query(`SELECT file_json FROM library_search_projection WHERE scope=? ORDER BY ordinal`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []cloudFileView{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var projected projectedLibraryFile
		if err := json.Unmarshal([]byte(raw), &projected); err != nil {
			return nil, err
		}
		file := projected.File
		file.CatalogCreatedAt, file.CatalogFormat, file.Protected = projected.CatalogCreatedAt, projected.CatalogFormat, projected.Protected
		if !file.LocalOnly {
			if record, ok := c.searchRecords[file.ID+"\x00"+file.RevisionID]; ok {
				file.Replicas, file.AvailableReplicas = c.replicaCounts(record)
				file.MinReplicas = file.Replicas
			}
		}
		result = append(result, file)
	}
	return result, rows.Err()
}

// readSearchCandidates uses the materialized current-file projection to apply
// stable filters before the live availability and facet passes. FTS5's trigram
// LIKE index handles substring queries of at least three characters.
func (c *cloudClient) readSearchCandidates(scope, needle, fileType, grouped, cloudCopy string) ([]cloudFileView, bool, error) {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.runtime.db == nil || !c.searchReady {
		return nil, false, nil
	}
	projectionScope := 1
	if scope == "cloud" {
		projectionScope = 0
	}
	where := []string{"p.scope=?"}
	args := []any{projectionScope}
	if fileType != "" {
		where = append(where, "p.file_type=?")
		args = append(args, fileType)
	}
	if grouped == "true" {
		where = append(where, "p.folder_id<>''")
	} else if grouped == "false" {
		where = append(where, "p.folder_id=''")
	}
	if cloudCopy == "true" {
		where = append(where, "p.local_only=0")
	} else if cloudCopy == "false" {
		where = append(where, "p.local_only=1")
	}
	if scope == "local" || strings.HasPrefix(scope, "device:") {
		device := strings.TrimPrefix(scope, "device:")
		if scope == "local" {
			device = c.runtime.id
		}
		where = append(where, `EXISTS (SELECT 1 FROM json_each(p.file_json,'$.file.locations') AS location WHERE json_extract(location.value,'$.nodeId')=?)`)
		args = append(args, device)
	}
	if needle != "" {
		pattern := "%" + needle + "%"
		if len([]rune(needle)) >= 3 {
			where = append(where, `p.rowid IN (SELECT rowid FROM library_search_fts WHERE name_search LIKE ? OR folder_search LIKE ? OR tags_search LIKE ?)`)
		} else {
			where = append(where, `(p.name_search LIKE ? OR p.folder_search LIKE ? OR p.tags_search LIKE ?)`)
		}
		args = append(args, pattern, pattern, pattern)
	}
	rows, err := c.runtime.db.Query(fmt.Sprintf(`SELECT p.file_json FROM library_search_current AS p WHERE %s ORDER BY p.name,p.file_id`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	result := []cloudFileView{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		var projected projectedLibraryFile
		if err = json.Unmarshal([]byte(raw), &projected); err != nil {
			return nil, false, err
		}
		file := projected.File
		file.CatalogCreatedAt, file.CatalogFormat, file.Protected = projected.CatalogCreatedAt, projected.CatalogFormat, projected.Protected
		if !file.LocalOnly {
			if record, ok := c.searchRecords[file.ID+"\x00"+file.RevisionID]; ok {
				file.Replicas, file.AvailableReplicas = c.replicaCounts(record)
				file.MinReplicas = file.Replicas
			}
		}
		result = append(result, file)
	}
	return result, true, rows.Err()
}

// searchProjectionPage performs the final stable filtering and page selection
// in SQLite when no live-readability predicate is requested. Facets and
// coverage are still computed separately from the revision projection.
func (c *cloudClient) searchProjectionPage(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy, order string, tags []string, limit, offset int) ([]cloudFileView, int, bool, error) {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.runtime.db == nil || !c.searchReady {
		return nil, 0, false, nil
	}
	condition, args := c.searchProjectionWhere(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy, tags, true, true)
	sortOrder := "p.name ASC,p.file_id ASC"
	switch order {
	case "", "name_asc":
	case "name_desc":
		sortOrder = "p.name DESC,p.file_id ASC"
	case "modified_desc":
		sortOrder = "p.modified DESC,p.name ASC,p.file_id ASC"
	case "modified_asc":
		sortOrder = "p.modified ASC,p.name ASC,p.file_id ASC"
	case "ingested_desc":
		sortOrder = "p.ingested_at DESC,p.name ASC,p.file_id ASC"
	case "ingested_asc":
		sortOrder = "p.ingested_at ASC,p.name ASC,p.file_id ASC"
	default:
		return nil, 0, false, nil
	}
	tx, err := c.runtime.db.Begin()
	if err != nil {
		return nil, 0, false, err
	}
	defer tx.Rollback()
	var total int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM library_search_current AS p WHERE `+condition, args...).Scan(&total); err != nil {
		return nil, 0, false, err
	}
	pageArgs := append(append([]any{}, args...), limit, offset)
	rows, err := tx.Query(`SELECT p.file_json FROM library_search_current AS p WHERE `+condition+` ORDER BY `+sortOrder+` LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, false, err
	}
	page := []cloudFileView{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, 0, false, err
		}
		var projected projectedLibraryFile
		if err = json.Unmarshal([]byte(raw), &projected); err != nil {
			rows.Close()
			return nil, 0, false, err
		}
		file := projected.File
		file.CatalogCreatedAt, file.CatalogFormat, file.Protected = projected.CatalogCreatedAt, projected.CatalogFormat, projected.Protected
		if !file.LocalOnly {
			if record, ok := c.searchRecords[file.ID+"\x00"+file.RevisionID]; ok {
				file.Replicas, file.AvailableReplicas = c.replicaCounts(record)
				file.MinReplicas = file.Replicas
			}
		}
		page = append(page, file)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, 0, false, err
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return nil, 0, false, err
	}
	return page, total, true, nil
}

// searchProjectionWhere builds the stable filter shared by page and facet
// queries. Folder facets exclude the folder condition (they count across all
// folders); tag facets exclude the tag condition (they count across all tags),
// matching the cross-filtered facet semantics of the Go fallback.
func (c *cloudClient) searchProjectionWhere(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy string, tags []string, includeFolder, includeTags bool) (string, []any) {
	projectionScope := 1
	if scope == "cloud" {
		projectionScope = 0
	}
	where := []string{"p.scope=?", `NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=p.file_id)`, `COALESCE((SELECT state FROM library_file_lifecycle l WHERE l.file_id=p.file_id),'active')=?`}
	args := []any{projectionScope, lifecycle}
	if fileType != "" {
		where = append(where, "p.file_type=?")
		args = append(args, fileType)
	}
	if grouped == "true" {
		where = append(where, "p.folder_id<>''")
	} else if grouped == "false" {
		where = append(where, "p.folder_id=''")
	}
	if cloudCopy == "true" {
		where = append(where, "p.local_only=0")
	} else if cloudCopy == "false" {
		where = append(where, "p.local_only=1")
	}
	if scope == "local" || strings.HasPrefix(scope, "device:") {
		device := strings.TrimPrefix(scope, "device:")
		if scope == "local" {
			device = c.runtime.id
		}
		where = append(where, `EXISTS (SELECT 1 FROM json_each(p.file_json,'$.file.locations') AS location WHERE json_extract(location.value,'$.nodeId')=?)`)
		args = append(args, device)
	}
	if needle != "" {
		pattern := "%" + needle + "%"
		if strings.ContainsAny(needle, "%_") {
			pattern = "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(needle) + "%"
			where = append(where, `(p.name_search LIKE ? ESCAPE '\' OR p.folder_search LIKE ? ESCAPE '\' OR p.tags_search LIKE ? ESCAPE '\')`)
		} else if len([]rune(needle)) >= 3 {
			where = append(where, `p.rowid IN (SELECT rowid FROM library_search_fts WHERE name_search LIKE ? OR folder_search LIKE ? OR tags_search LIKE ?)`)
		} else {
			where = append(where, `(p.name_search LIKE ? OR p.folder_search LIKE ? OR p.tags_search LIKE ?)`)
		}
		args = append(args, pattern, pattern, pattern)
	}
	if includeFolder {
		if folderID == "unfiled" {
			where = append(where, "p.folder_id=''")
		} else if strings.HasPrefix(folderID, "name:") {
			where = append(where, "p.folder_search=?")
			args = append(args, strings.ToLower(strings.TrimPrefix(folderID, "name:")))
		} else if folderID != "" {
			where = append(where, "p.folder_id=?")
			args = append(args, folderID)
		}
	}
	if includeTags {
		for _, tag := range tags {
			where = append(where, `EXISTS (SELECT 1 FROM library_search_tags t WHERE t.scope=p.scope AND t.file_id=p.file_id AND t.tag_key=?)`)
			args = append(args, foldedTagKey(tag))
		}
	}
	return strings.Join(where, " AND "), args
}

type libraryFolderFacet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type libraryTagFacet struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// searchProjectionGeneration reads the cursor generation bound to the exact
// revision set materialized in the projection, avoiding a full catalog reload.
func (c *cloudClient) searchProjectionGeneration(scope string) (string, bool, error) {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.runtime.db == nil || !c.searchReady {
		return "", false, nil
	}
	projectionScope := 1
	if scope == "cloud" {
		projectionScope = 0
	}
	var generation string
	if err := c.runtime.db.QueryRow(`SELECT generation FROM library_search_meta WHERE scope=?`, projectionScope).Scan(&generation); err != nil {
		return "", false, err
	}
	return generation, true, nil
}

// searchProjectionCoverage computes the catalog-freshness fields from the
// revision projection. Live peer availability is overlaid separately by the
// caller; these fields only describe which catalog records this node holds.
func (c *cloudClient) searchProjectionCoverage(scope string) (string, []string, map[string]string, bool, error) {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.runtime.db == nil || !c.searchReady {
		return "", nil, nil, false, nil
	}
	projectionScope := 1
	if scope == "cloud" {
		projectionScope = 0
	}
	var lastCatalogAt string
	if err := c.runtime.db.QueryRow(`SELECT COALESCE(MAX(json_extract(file_json,'$.catalogCreatedAt')),'') FROM library_search_projection WHERE scope=?`, projectionScope).Scan(&lastCatalogAt); err != nil {
		return "", nil, nil, false, err
	}
	rows, err := c.runtime.db.Query(`SELECT json_extract(location.value,'$.nodeId') AS node, MAX(json_extract(p.file_json,'$.catalogCreatedAt')) AS catalog_at FROM library_search_projection AS p, json_each(p.file_json,'$.file.locations') AS location WHERE p.scope=? AND COALESCE(json_extract(location.value,'$.nodeId'),'')<>'' GROUP BY node ORDER BY node`, projectionScope)
	if err != nil {
		return "", nil, nil, false, err
	}
	defer rows.Close()
	sources := []string{}
	sourceLastCatalogAt := map[string]string{}
	for rows.Next() {
		var node, catalogAt string
		if err := rows.Scan(&node, &catalogAt); err != nil {
			return "", nil, nil, false, err
		}
		sources = append(sources, node)
		if catalogAt != "" {
			sourceLastCatalogAt[node] = catalogAt
		}
	}
	return lastCatalogAt, sources, sourceLastCatalogAt, true, rows.Err()
}

// searchProjectionFacets counts folder and tag facets in SQLite with the same
// cross-filtering as the Go fallback: folder facets apply every filter except
// the folder selection, tag facets apply every filter except the tag selection.
func (c *cloudClient) searchProjectionFacets(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy string, tags []string) ([]libraryFolderFacet, []libraryTagFacet, bool, error) {
	c.searchMu.Lock()
	defer c.searchMu.Unlock()
	if c.runtime.db == nil || !c.searchReady {
		return nil, nil, false, nil
	}
	folderCond, folderArgs := c.searchProjectionWhere(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy, tags, false, true)
	folderRows, err := c.runtime.db.Query(`SELECT fid,fname,cnt FROM (SELECT CASE WHEN p.folder_id='' THEN 'unfiled' ELSE p.folder_id END AS fid, CASE WHEN p.folder_id='' THEN '未归组' ELSE p.folder_name END AS fname, COUNT(*) OVER (PARTITION BY CASE WHEN p.folder_id='' THEN 'unfiled' ELSE p.folder_id END) AS cnt, ROW_NUMBER() OVER (PARTITION BY CASE WHEN p.folder_id='' THEN 'unfiled' ELSE p.folder_id END ORDER BY p.name DESC,p.file_id DESC) AS rn FROM library_search_current AS p WHERE `+folderCond+`) WHERE rn=1 ORDER BY fname,fid`, folderArgs...)
	if err != nil {
		return nil, nil, false, err
	}
	folders := []libraryFolderFacet{}
	for folderRows.Next() {
		var facet libraryFolderFacet
		if err := folderRows.Scan(&facet.ID, &facet.Name, &facet.Count); err != nil {
			folderRows.Close()
			return nil, nil, false, err
		}
		folders = append(folders, facet)
	}
	if err := folderRows.Err(); err != nil {
		folderRows.Close()
		return nil, nil, false, err
	}
	folderRows.Close()
	tagCond, tagArgs := c.searchProjectionWhere(scope, lifecycle, needle, folderID, fileType, grouped, cloudCopy, tags, true, false)
	tagRows, err := c.runtime.db.Query(`SELECT t.tag_name,COUNT(*) FROM library_search_tags AS t JOIN library_search_current AS p ON p.scope=t.scope AND p.file_id=t.file_id WHERE `+tagCond+` GROUP BY t.tag_name ORDER BY t.tag_name`, tagArgs...)
	if err != nil {
		return nil, nil, false, err
	}
	defer tagRows.Close()
	tagItems := []libraryTagFacet{}
	for tagRows.Next() {
		var facet libraryTagFacet
		if err := tagRows.Scan(&facet.Name, &facet.Count); err != nil {
			return nil, nil, false, err
		}
		tagItems = append(tagItems, facet)
	}
	return folders, tagItems, true, tagRows.Err()
}
