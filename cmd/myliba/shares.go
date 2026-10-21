package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type shareService struct {
	db   *sql.DB
	hubs *hubProfiles
	app  *server
}

func (s *shareService) video(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.PathValue("id"), r.Header.Get("X-Myliba-Share-Key")) {
		writeError(w, http.StatusUnauthorized, "分享密钥无效")
		return
	}
	share, _, _, err := s.lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusGone, err.Error())
		return
	}
	var kind string
	if err := s.db.QueryRow(`SELECT kind FROM resources WHERE id=?`, share.ResourceID).Scan(&kind); err != nil || kind != "video" {
		writeError(w, http.StatusUnsupportedMediaType, "分享文件不是视频")
		return
	}
	r.SetPathValue("id", share.ResourceID)
	if r.PathValue("segment") != "" {
		s.app.videoSegment(w, r)
	} else if strings.HasSuffix(r.URL.Path, "/init") {
		s.app.videoInit(w, r)
	} else {
		s.app.videoVariant(w, r)
	}
}

type fileShare struct {
	ID         string `json:"id"`
	ResourceID string `json:"resourceId"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	CreatedAt  string `json:"createdAt"`
	ExpiresAt  string `json:"expiresAt"`
	RevokedAt  string `json:"revokedAt,omitempty"`
}

func shareTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (h *hubProfiles) publicOrigin() (string, error) {
	h.mu.Lock()
	id := h.activeID
	h.mu.Unlock()
	if id == "" {
		return "", errors.New("请先连接公网 Hub")
	}
	var raw string
	if err := h.db.QueryRow(`SELECT url FROM hub_profiles WHERE id=?`, id).Scan(&raw); err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" {
		return "", errors.New("公网 Hub 地址无效")
	}
	return "https://" + parsed.Host, nil
}

func (s *shareService) create(w http.ResponseWriter, r *http.Request) {
	origin, err := s.hubs.publicOrigin()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var input struct {
		ExpiresInHours int `json:"expiresInHours"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil || input.ExpiresInHours < 1 || input.ExpiresInHours > 720 {
		writeError(w, http.StatusBadRequest, "有效期需为 1 到 720 小时")
		return
	}
	resourceID := r.PathValue("id")
	var path, name string
	var available bool
	if err := s.db.QueryRow(`SELECT path,name,available FROM resources WHERE id=?`, resourceID).Scan(&path, &name, &available); err != nil || !available {
		writeError(w, http.StatusNotFound, "文件不存在或不可用")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusGone, "原文件当前不可用")
		return
	}
	token, err := randomToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法生成分享密钥")
		return
	}
	created := time.Now().UTC()
	share := fileShare{ID: newID(), ResourceID: resourceID, Name: name, Size: info.Size(), CreatedAt: created.Format(time.RFC3339Nano), ExpiresAt: created.Add(time.Duration(input.ExpiresInHours) * time.Hour).Format(time.RFC3339Nano)}
	_, err = s.db.Exec(`INSERT INTO file_shares(id,resource_id,token_hash,size,modified_ns,created_at,expires_at,revoked_at) VALUES(?,?,?,?,?,?,?,NULL)`, share.ID, resourceID, shareTokenHash(token), info.Size(), info.ModTime().UnixNano(), share.CreatedAt, share.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法保存分享")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"share": share, "url": origin + "/s/" + share.ID + "#key=" + token})
}

func (s *shareService) list(w http.ResponseWriter, r *http.Request) {
	resourceID := r.PathValue("id")
	rows, err := s.db.Query(`SELECT id,created_at,expires_at,COALESCE(revoked_at,'') FROM file_shares WHERE resource_id=? ORDER BY created_at DESC`, resourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分享")
		return
	}
	defer rows.Close()
	shares := make([]fileShare, 0)
	for rows.Next() {
		share := fileShare{ResourceID: resourceID}
		if err := rows.Scan(&share.ID, &share.CreatedAt, &share.ExpiresAt, &share.RevokedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取分享")
			return
		}
		shares = append(shares, share)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "无法读取分享")
		return
	}
	writeJSON(w, http.StatusOK, shares)
}

func (s *shareService) revoke(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Exec(`UPDATE file_shares SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法撤销分享")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeError(w, http.StatusNotFound, "分享不存在或已撤销")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *shareService) lookup(id string) (fileShare, string, int64, error) {
	var share fileShare
	var path, revokedAt string
	var modifiedNS int64
	err := s.db.QueryRow(`SELECT f.id,f.resource_id,r.name,r.path,f.size,f.modified_ns,f.created_at,f.expires_at,COALESCE(f.revoked_at,'')
		FROM file_shares f JOIN resources r ON r.id=f.resource_id WHERE f.id=? AND r.available=1`, id).Scan(
		&share.ID, &share.ResourceID, &share.Name, &path, &share.Size, &modifiedNS, &share.CreatedAt, &share.ExpiresAt, &revokedAt)
	if err != nil || revokedAt != "" {
		return fileShare{}, "", 0, errors.New("分享不存在或已撤销")
	}
	expires, err := time.Parse(time.RFC3339Nano, share.ExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return fileShare{}, "", 0, errors.New("分享已过期")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != share.Size || info.ModTime().UnixNano() != modifiedNS {
		return fileShare{}, "", 0, errors.New("原文件已变更或不可用，请重新分享")
	}
	return share, path, modifiedNS, nil
}

func (s *shareService) validToken(id, token string) bool {
	if len(token) != 64 {
		return false
	}
	if _, err := hex.DecodeString(token); err != nil {
		return false
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM file_shares WHERE id=?`, id).Scan(&stored); err != nil {
		return false
	}
	return safeEqual(shareTokenHash(token), stored)
}

func (s *shareService) redeem(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil || len(input.Token) != 64 {
		writeError(w, http.StatusUnauthorized, "分享密钥无效")
		return
	}
	share, path, _, err := s.lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusGone, err.Error())
		return
	}
	if !s.validToken(share.ID, input.Token) {
		writeError(w, http.StatusUnauthorized, "分享密钥无效")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" || !safeInlineContentType(contentType) {
		contentType = "application/octet-stream"
	}
	var kind string
	_ = s.db.QueryRow(`SELECT kind FROM resources WHERE id=?`, share.ResourceID).Scan(&kind)
	writeJSON(w, http.StatusOK, map[string]any{"id": share.ID, "name": share.Name, "size": share.Size, "expiresAt": share.ExpiresAt, "contentType": contentType, "kind": kind})
}

func (s *shareService) file(w http.ResponseWriter, r *http.Request) {
	if !s.validToken(r.PathValue("id"), r.Header.Get("X-Myliba-Share-Key")) {
		writeError(w, http.StatusUnauthorized, "分享密钥无效")
		return
	}
	share, path, modifiedNS, err := s.lookup(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusGone, err.Error())
		return
	}
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusGone, "原文件当前不可用")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != share.Size || info.ModTime().UnixNano() != modifiedNS {
		writeError(w, http.StatusGone, "原文件已变更")
		return
	}
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" || !safeInlineContentType(contentType) {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": strings.TrimSpace(share.Name)}))
	http.ServeContent(w, r, share.Name, info.ModTime(), file)
}
