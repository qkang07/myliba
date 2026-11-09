package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

type legacyFolderGroup struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
	Count   int      `json:"count"`
	Files   []string `json:"-"`
}

func (s *server) legacyFolderGroups() ([]legacyFolderGroup, error) {
	rows, err := s.db.Query(`SELECT i.folder_id,b.folder_name,b.author_id,b.file_id
		FROM library_legacy_baselines b JOIN library_legacy_folder_identities i ON i.record_hash=b.record_hash
		WHERE i.folder_id<>'' AND NOT EXISTS (SELECT 1 FROM library_file_folder_state m WHERE m.file_id=b.file_id)
		ORDER BY i.folder_id,b.file_id`)
	if err != nil { return nil, err }
	defer rows.Close()
	groups := map[string]*legacyFolderGroup{}
	sources := map[string]map[string]bool{}
	for rows.Next() {
		var id, name, author, fileID string
		if err := rows.Scan(&id, &name, &author, &fileID); err != nil { return nil, err }
		group := groups[id]
		if group == nil { group = &legacyFolderGroup{ID: id, Name: name, Sources: []string{}, Files: []string{}}; groups[id] = group; sources[id] = map[string]bool{} }
		group.Files = append(group.Files, fileID)
		group.Count++
		sources[id][author] = true
	}
	if err := rows.Err(); err != nil { return nil, err }
	result := make([]legacyFolderGroup, 0, len(groups))
	for id, group := range groups {
		for author := range sources[id] { group.Sources = append(group.Sources, author) }
		sort.Strings(group.Sources)
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool { if result[i].Name == result[j].Name { return result[i].ID < result[j].ID }; return result[i].Name < result[j].Name })
	return result, nil
}

func (s *server) listLegacyFolders(w http.ResponseWriter, r *http.Request) {
	groups, err := s.legacyFolderGroups()
	if err != nil { writeError(w, 500, "无法读取旧文件夹身份"); return }
	writeJSON(w, 200, map[string]any{"items": groups})
}

func (s *server) mergeLegacyFolders(w http.ResponseWriter, r *http.Request) {
	var input struct { IDs []string `json:"folderIds"`; Name string `json:"name"` }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input) != nil || len(input.IDs) < 2 || len(input.IDs) > 100 {
		writeError(w, 400, "请选择至少两个旧文件夹"); return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 80 { writeError(w, 400, "目标文件夹名称无效"); return }
	selected := map[string]bool{}
	for _, id := range input.IDs {
		if !strings.HasPrefix(id, "legacy:") || selected[id] { writeError(w, 400, "旧文件夹身份无效"); return }
		selected[id] = true
	}
	groups, err := s.legacyFolderGroups()
	if err != nil { writeError(w, 500, "无法读取旧文件夹身份"); return }
	fileSet := map[string]bool{}
	for _, group := range groups {
		if selected[group.ID] { delete(selected, group.ID); for _, id := range group.Files { fileSet[id] = true } }
	}
	if len(selected) != 0 || len(fileSet) == 0 || len(fileSet) > 20000 { writeError(w, 409, "旧文件夹已变化，请刷新后重试"); return }
	fileIDs := make([]string, 0, len(fileSet))
	for id := range fileSet { fileIDs = append(fileIDs, id) }
	sort.Strings(fileIDs)
	tx, err := s.db.Begin()
	if err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
	defer tx.Rollback()
	var deleted int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM library_folder_tombstones WHERE name=?`, name).Scan(&deleted); err != nil || deleted != 0 { writeError(w, 409, "目标文件夹当前不可用"); return }
	if _, err = tx.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'') ON CONFLICT(name) DO NOTHING`, newID(), name, now()); err != nil { writeFolderError(w, err); return }
	var folderID, password string
	if err = tx.QueryRow(`SELECT id,password_hash FROM folders WHERE name=? COLLATE NOCASE`, name).Scan(&folderID, &password); err != nil || password != "" { writeError(w, 403, "目标文件夹不可用或受保护"); return }
	for _, fileID := range fileIDs {
		if err = ensureOperationFile(tx, fileID); err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
		if err = seedLegacyClassification(tx, fileID); err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
		if _, err = tx.Exec(`INSERT INTO library_file_folders(file_id,folder_id,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO UPDATE SET folder_id=excluded.folder_id,updated_at=excluded.updated_at`, fileID, folderID, now()); err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
		if _, err = tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) SELECT resource_id,? FROM library_resource_aliases WHERE file_id=? ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, folderID, fileID); err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
	}
	if err = recordLibraryOperation(tx, "folder.move", map[string]any{"fileIds": fileIDs, "folderId": folderID, "folderName": name}); err != nil { writeError(w, 500, "无法记录文件夹合并"); return }
	if err = tx.Commit(); err != nil { writeError(w, 500, "无法合并旧文件夹"); return }
	writeJSON(w, 200, map[string]any{"folderId": folderID, "name": name, "files": len(fileIDs)})
}

