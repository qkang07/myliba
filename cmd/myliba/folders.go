package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type virtualFolder struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Count             int    `json:"count"`
	CreatedAt         string `json:"createdAt"`
	PasswordProtected bool   `json:"passwordProtected"`
}

type folderAccessSession struct {
	FolderID string
	Expires  time.Time
}

// Empty/all preserve the existing unscoped API. "unfiled" is the built-in
// group for resources without a virtual folder.
func folderScopeSQL(folderID, resourceAlias string) (string, []any) {
	switch folderID {
	case "", "all":
		return "", nil
	case "unfiled":
		return `NOT EXISTS (SELECT 1 FROM resource_folders rf WHERE rf.resource_id=` + resourceAlias + `.id)`, nil
	default:
		return `EXISTS (SELECT 1 FROM resource_folders rf WHERE rf.resource_id=` + resourceAlias + `.id AND rf.folder_id=?)`, []any{folderID}
	}
}

func readFolderName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "文件夹名称无效")
		return "", false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "文件夹名称无效")
		return "", false
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || name == "未归组" || name == "全部资源" || utf8.RuneCountInString(name) > 80 {
		writeError(w, http.StatusBadRequest, "文件夹名称需为 1 到 80 个字符")
		return "", false
	}
	return name, true
}

func (s *server) listFolders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(`SELECT f.id,f.name,f.created_at,COUNT(rf.resource_id),f.password_hash<>''
		FROM folders f LEFT JOIN resource_folders rf ON rf.folder_id=f.id
		GROUP BY f.id ORDER BY f.name COLLATE NOCASE,f.id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	items := []virtualFolder{}
	for rows.Next() {
		var item virtualFolder
		if err := rows.Scan(&item.ID, &item.Name, &item.CreatedAt, &item.Count, &item.PasswordProtected); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "无法读取文件夹")
			return
		}
		if item.PasswordProtected && !s.folderCookieToken(r, item.ID) {
			item.Count = 0
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	rows.Close()
	var unfiledCount int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resources r WHERE NOT EXISTS
		(SELECT 1 FROM resource_folders rf WHERE rf.resource_id=r.id)`).Scan(&unfiledCount); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "unfiledCount": unfiledCount})
}

func (s *server) createFolder(w http.ResponseWriter, r *http.Request) {
	name, ok := readFolderName(w, r)
	if !ok {
		return
	}
	item := virtualFolder{ID: newID(), Name: name, CreatedAt: now()}
	if _, err := s.db.Exec(`INSERT INTO folders(id,name,created_at,password_hash) VALUES(?,?,?,'')`, item.ID, item.Name, item.CreatedAt); err != nil {
		writeFolderError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) renameFolder(w http.ResponseWriter, r *http.Request) {
	name, ok := readFolderName(w, r)
	if !ok {
		return
	}
	result, err := s.db.Exec(`UPDATE folders SET name=? WHERE id=?`, name, r.PathValue("id"))
	if err != nil {
		writeFolderError(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	var item virtualFolder
	if err := s.db.QueryRow(`SELECT f.id,f.name,f.created_at,COUNT(rf.resource_id),f.password_hash<>''
		FROM folders f LEFT JOIN resource_folders rf ON rf.folder_id=f.id
		WHERE f.id=? GROUP BY f.id`, r.PathValue("id")).Scan(&item.ID, &item.Name, &item.CreatedAt, &item.Count, &item.PasswordProtected); err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取文件夹")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	defer tx.Rollback()
	// Keep saved collections usable after their folder is removed.
	rows, err := tx.Query(`SELECT id,filters_json FROM smart_collections`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	updates := map[string]string{}
	for rows.Next() {
		var id, raw string
		var filters collectionFilters
		if err := rows.Scan(&id, &raw); err != nil || json.Unmarshal([]byte(raw), &filters) != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "无法更新关联集合")
			return
		}
		if filters.FolderID == r.PathValue("id") {
			filters.FolderID = "unfiled"
			data, _ := json.Marshal(filters)
			updates[id] = string(data)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, "无法更新关联集合")
		return
	}
	rows.Close()
	for id, raw := range updates {
		if _, err := tx.Exec(`UPDATE smart_collections SET filters_json=? WHERE id=?`, raw, id); err != nil {
			writeError(w, http.StatusInternalServerError, "无法更新关联集合")
			return
		}
	}
	result, err := tx.Exec(`DELETE FROM folders WHERE id=?`, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法删除文件夹")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) moveResourcesToFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs      []string `json:"ids"`
		FolderID string   `json:"folderId"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || len(input.IDs) == 0 || len(input.IDs) > 1000 {
		writeError(w, http.StatusBadRequest, "请选择 1 到 1000 个资源")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "移动请求无效")
		return
	}
	input.FolderID = strings.TrimSpace(input.FolderID)
	tx, err := s.db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法移动资源")
		return
	}
	defer tx.Rollback()
	if input.FolderID != "" {
		var id, passwordHash string
		err := tx.QueryRow(`SELECT id,password_hash FROM folders WHERE id=?`, input.FolderID).Scan(&id, &passwordHash)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "目标文件夹不存在")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取目标文件夹")
			return
		}
		if passwordHash != "" && !s.folderCookieToken(r, id) {
			writeError(w, http.StatusLocked, "目标文件夹需要密码")
			return
		}
	}
	seen := map[string]bool{}
	count := 0
	for _, rawID := range input.IDs {
		id := strings.TrimSpace(rawID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		var existing, sourceFolderID, sourcePasswordHash string
		if err := tx.QueryRow(`SELECT r.id,COALESCE(rf.folder_id,''),COALESCE(f.password_hash,'') FROM resources r LEFT JOIN resource_folders rf ON rf.resource_id=r.id LEFT JOIN folders f ON f.id=rf.folder_id WHERE r.id=?`, id).Scan(&existing, &sourceFolderID, &sourcePasswordHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "资源不存在")
			} else {
				writeError(w, http.StatusInternalServerError, "无法读取资源")
			}
			return
		}
		if sourcePasswordHash != "" && !s.folderCookieToken(r, sourceFolderID) {
			writeError(w, http.StatusLocked, "源文件夹需要密码")
			return
		}
		if input.FolderID == "" {
			_, err = tx.Exec(`DELETE FROM resource_folders WHERE resource_id=?`, id)
		} else {
			_, err = tx.Exec(`INSERT INTO resource_folders(resource_id,folder_id) VALUES(?,?)
				ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, id, input.FolderID)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法移动资源")
			return
		}
		count++
	}
	if count == 0 {
		writeError(w, http.StatusBadRequest, "请选择资源")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "无法移动资源")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": count})
}

func (s *server) assignRootToFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RootID string `json:"rootId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&input); err != nil || strings.TrimSpace(input.RootID) == "" {
		writeError(w, http.StatusBadRequest, "扫描目录无效")
		return
	}
	var folderID, rootID string
	if err := s.db.QueryRow(`SELECT id FROM folders WHERE id=?`, r.PathValue("id")).Scan(&folderID); err != nil {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	if err := s.db.QueryRow(`SELECT id FROM roots WHERE id=?`, input.RootID).Scan(&rootID); err != nil {
		writeError(w, http.StatusNotFound, "扫描目录不存在")
		return
	}
	result, err := s.db.Exec(`INSERT INTO resource_folders(resource_id,folder_id)
		SELECT id,? FROM resources WHERE root_id=? AND 1=1
		ON CONFLICT(resource_id) DO UPDATE SET folder_id=excluded.folder_id`, folderID, rootID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法添加扫描结果")
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取扫描结果")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": count})
}

func folderCookieName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "myliba_folder_" + hex.EncodeToString(sum[:8])
}

func (s *server) folderCookieToken(r *http.Request, folderID string) bool {
	cookie, err := r.Cookie(folderCookieName(folderID))
	values := []string{r.Header.Get("X-Myliba-Folder-Access")}
	if err == nil {
		values = append(values, cookie.Value)
	}
	s.folderAccessMu.Lock()
	defer s.folderAccessMu.Unlock()
	for _, value := range values {
		if session, ok := s.folderAccess[value]; ok && session.FolderID == folderID && time.Now().Before(session.Expires) {
			return true
		}
	}
	return false
}

func (s *server) unlockFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "密码请求无效")
		return
	}
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM folders WHERE id=?`, r.PathValue("id")).Scan(&hash); err != nil {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
		writeError(w, http.StatusForbidden, "密码不正确")
		return
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		writeError(w, http.StatusInternalServerError, "无法解锁文件夹")
		return
	}
	value := hex.EncodeToString(token[:])
	s.folderAccessMu.Lock()
	if s.folderAccess == nil {
		s.folderAccess = make(map[string]folderAccessSession)
	}
	expires := time.Now().Add(8 * time.Hour)
	s.folderAccess[value] = folderAccessSession{FolderID: r.PathValue("id"), Expires: expires}
	s.folderAccessMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: folderCookieName(r.PathValue("id")), Value: value, Path: "/api", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 8 * 60 * 60})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accessToken": value})
}

func (s *server) folderAccessStatus(w http.ResponseWriter, r *http.Request) {
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM folders WHERE id=?`, r.PathValue("id")).Scan(&hash); err != nil {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"protected": hash != "", "unlocked": hash == "" || s.folderCookieToken(r, r.PathValue("id"))})
}

func (s *server) setFolderPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "密码请求无效")
		return
	}
	password := strings.TrimSpace(input.Password)
	if password != "" && (utf8.RuneCountInString(password) < 4 || len(password) > 72) {
		writeError(w, http.StatusBadRequest, "密码需为 4 到 72 个字符")
		return
	}
	hash := ""
	if password != "" {
		encoded, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法保存密码")
			return
		}
		hash = string(encoded)
	}
	result, err := s.db.Exec(`UPDATE folders SET password_hash=? WHERE id=?`, hash, r.PathValue("id"))
	if err != nil {
		writeFolderError(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		writeError(w, http.StatusNotFound, "文件夹不存在")
		return
	}
	s.folderAccessMu.Lock()
	for token, session := range s.folderAccess {
		if session.FolderID == r.PathValue("id") {
			delete(s.folderAccess, token)
		}
	}
	s.folderAccessMu.Unlock()
	if password == "" {
		if cookie, err := r.Cookie(folderCookieName(r.PathValue("id"))); err == nil {
			s.folderAccessMu.Lock()
			delete(s.folderAccess, cookie.Value)
			s.folderAccessMu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: folderCookieName(r.PathValue("id")), Value: "", Path: "/api", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "passwordProtected": password != ""})
}

func (s *server) protectedFolderID(r *http.Request) string {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "folders" && parts[2] != "" {
		return parts[2]
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "resources" && parts[2] != "selection" && parts[2] != "folder" && parts[2] != "tags" {
		var folderID string
		_ = s.db.QueryRow(`SELECT folder_id FROM resource_folders WHERE resource_id=?`, parts[2]).Scan(&folderID)
		return folderID
	}
	switch parts[len(parts)-1] {
	case "tags", "attribute-tags", "tag-batch-suggestions":
		return r.URL.Query().Get("folderId")
	}
	if strings.HasSuffix(r.URL.Path, "/resources") || strings.HasSuffix(r.URL.Path, "/resources/selection") {
		return r.URL.Query().Get("folderId")
	}
	return ""
}

func (s *server) folderAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/unlock") || strings.HasSuffix(r.URL.Path, "/access") {
			next.ServeHTTP(w, r)
			return
		}
		folderID := s.protectedFolderID(r)
		if folderID != "" {
			var hash string
			err := s.db.QueryRow(`SELECT password_hash FROM folders WHERE id=?`, folderID).Scan(&hash)
			if err == nil && hash != "" && !s.folderCookieToken(r, folderID) {
				writeError(w, http.StatusLocked, "此文件夹需要密码")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeFolderError(w http.ResponseWriter, err error) {
	var sqliteErr sqlite3.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code == sqlite3.ErrConstraint {
		writeError(w, http.StatusConflict, "已有同名文件夹")
		return
	}
	writeError(w, http.StatusInternalServerError, "无法保存文件夹")
}
