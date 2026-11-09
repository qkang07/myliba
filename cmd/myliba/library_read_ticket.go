package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	libraryStreamCookieName = "myliba_library_stream"
	libraryStreamTTL        = 20 * time.Minute
)

// A library read ticket is scoped to one immutable candidate and one route.
// Ordinary resource routes remain available for older authenticated clients;
// resolve only advertises the ticketed route to new clients.
type libraryReadTicket struct {
	LibraryID   string `json:"l"`
	FileID      string `json:"f"`
	RevisionID  string `json:"v"`
	Purpose     string `json:"p"`
	NodeID      string `json:"n"`
	ResourceRef string `json:"r"`
	Path        string `json:"u"`
	Expires     int64  `json:"e"`
}

func (c *cloudClient) signLibraryReadTicket(t libraryReadTicket) (string, error) {
	t.LibraryID = c.libraryID()
	t.Expires = time.Now().Add(2 * time.Minute).Unix()
	payload, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	c.keyMu.RLock()
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	signature := mac.Sum(nil)
	c.keyMu.RUnlock()
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *cloudClient) verifyLibraryReadTicket(raw, path, purpose, node, resource string) (libraryReadTicket, error) {
	var ticket libraryReadTicket
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return ticket, errors.New("missing read ticket")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ticket, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ticket, err
	}
	c.keyMu.RLock()
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	valid := hmac.Equal(signature, mac.Sum(nil))
	c.keyMu.RUnlock()
	if !valid || json.Unmarshal(payload, &ticket) != nil || ticket.LibraryID != c.libraryID() || ticket.Path != path || ticket.NodeID != node || ticket.ResourceRef != resource || ticket.FileID == "" || ticket.RevisionID == "" || ticket.Purpose != purpose || ticket.Expires <= time.Now().Unix() || ticket.Expires > time.Now().Add(2*time.Minute).Unix() {
		return libraryReadTicket{}, errors.New("invalid or expired read ticket")
	}
	return ticket, nil
}

func (s *server) serveLibraryRead(w http.ResponseWriter, r *http.Request) {
	resource, asset := r.PathValue("id"), r.PathValue("asset")
	if asset != "file" && asset != "thumbnail" && asset != "video-preview" {
		writeError(w, 404, "读取资产不存在")
		return
	}
	purpose := "original"
	if asset == "thumbnail" {
		purpose = "thumbnail"
	} else if asset == "video-preview" {
		purpose = "preview"
	}
	path := r.URL.Path
	streamScope := "preview"
	if asset == "file" {
		streamScope = "original"
	}
	if raw := libraryStreamToken(r); raw != "" {
		session, streamErr := s.cloud.verifyLibraryStreamSession(raw, path, s.cloud.runtime.id, resource, streamScope)
		if streamErr != nil || session.FileID == "" {
			writeError(w, 403, "流媒体凭据无效、已过期或用途不符")
			return
		}
		var currentFile, currentRevision string
		queryErr := s.db.QueryRow(`SELECT COALESCE(NULLIF(file_id,''),id),current_version_id FROM resources WHERE id=?`, resource).Scan(&currentFile, &currentRevision)
		if queryErr != nil || currentFile != session.FileID || currentRevision != session.RevisionID {
			writeError(w, 409, "来源文件版本已变化")
			return
		}
		r.SetPathValue("id", resource)
		switch asset {
		case "file":
			s.serveFile(w, r)
		case "thumbnail":
			s.serveThumbnail(w, r)
		case "video-preview":
			s.videoPreview(w, r)
		}
		return
	}
	ticket, err := s.cloud.verifyLibraryReadTicket(r.Header.Get("X-Myliba-Library-Read"), path, purpose, s.cloud.runtime.id, resource)
	if err != nil && asset == "thumbnail" {
		ticket, err = s.cloud.verifyLibraryReadTicket(r.Header.Get("X-Myliba-Library-Read"), path, "preview", s.cloud.runtime.id, resource)
	}
	if err != nil {
		writeError(w, 403, "读取凭据无效或已过期")
		return
	}
	var currentFile, currentRevision string
	err = s.db.QueryRow(`SELECT COALESCE(NULLIF(file_id,''),id),current_version_id FROM resources WHERE id=?`, resource).Scan(&currentFile, &currentRevision)
	if err != nil || currentFile != ticket.FileID || currentRevision != ticket.RevisionID {
		writeError(w, 409, "来源文件版本已变化")
		return
	}
	r.SetPathValue("id", resource)
	switch asset {
	case "file":
		s.serveFile(w, r)
	case "thumbnail":
		s.serveThumbnail(w, r)
	case "video-preview":
		s.videoPreview(w, r)
	}
}

func (c *cloudClient) authorizeCloudLibraryRead(w http.ResponseWriter, r *http.Request, record cloudRecord, manifest cloudManifest, purpose string) bool {
	if r.URL.Query().Get("libraryRead") != "1" {
		return true
	}
	fileID := manifest.FileID
	if fileID == "" {
		fileID = record.ObjectID
	}
	ticket, err := c.verifyLibraryReadTicket(r.Header.Get("X-Myliba-Library-Read"), r.URL.Path, purpose, c.runtime.id, record.ObjectID)
	if err != nil && purpose == "thumbnail" {
		ticket, err = c.verifyLibraryReadTicket(r.Header.Get("X-Myliba-Library-Read"), r.URL.Path, "preview", c.runtime.id, record.ObjectID)
	}
	if err != nil || ticket.FileID != fileID || ticket.RevisionID != record.RevisionID || r.PathValue("revision") == "" || r.PathValue("revision") != record.RevisionID {
		writeError(w, 403, "读取凭据无效或版本已变化")
		return false
	}
	return true
}

// A library stream session is exchanged from a verified resolve read ticket so a
// long video stream outlives the two-minute ticket. It stays bound to one
// fileId/revisionId/node/resourceRef and a route prefix; Scope records whether
// the originating ticket authorized the original or only a preview, so a preview
// session can never authorize the original download route.
type libraryStreamSession struct {
	LibraryID   string `json:"l"`
	FileID      string `json:"f"`
	RevisionID  string `json:"v"`
	NodeID      string `json:"n"`
	ResourceRef string `json:"r"`
	Scope       string `json:"s"`
	PathPrefix  string `json:"u"`
	Expires     int64  `json:"e"`
}

func (c *cloudClient) signLibraryStreamSession(s libraryStreamSession) (string, error) {
	s.LibraryID = c.libraryID()
	s.Expires = time.Now().Add(libraryStreamTTL).Unix()
	payload, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	c.keyMu.RLock()
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	signature := mac.Sum(nil)
	c.keyMu.RUnlock()
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c *cloudClient) verifyLibraryStreamSession(raw, path, node, resource, requiredScope string) (libraryStreamSession, error) {
	var session libraryStreamSession
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return session, errors.New("missing stream session")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return session, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return session, err
	}
	c.keyMu.RLock()
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	valid := hmac.Equal(signature, mac.Sum(nil))
	c.keyMu.RUnlock()
	if !valid || json.Unmarshal(payload, &session) != nil {
		return libraryStreamSession{}, errors.New("invalid or expired stream session")
	}
	scopeOK := session.Scope == "original" || requiredScope == "preview" && session.Scope == "preview"
	if session.LibraryID != c.libraryID() || session.NodeID != node || session.ResourceRef != resource || session.FileID == "" || session.RevisionID == "" || session.PathPrefix == "" || !strings.HasPrefix(path, session.PathPrefix) || !scopeOK || session.Expires <= time.Now().Unix() || session.Expires > time.Now().Add(libraryStreamTTL).Unix() {
		return libraryStreamSession{}, errors.New("invalid or expired stream session")
	}
	return session, nil
}

// libraryStreamToken reads the HttpOnly cookie set by the exchange endpoint for
// direct browser access, falling back to the relay-forwarded read header used by
// remote WebRTC/Hub clients whose synthetic responses cannot store a cookie.
func libraryStreamToken(r *http.Request) string {
	if cookie, err := r.Cookie(libraryStreamCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	return r.Header.Get("X-Myliba-Library-Read")
}

// authorizeLibraryStreamCredential validates a presented stream session against
// the serving node and the requested route. handled reports whether a credential
// was present at all: when false the caller keeps its legacy session behavior;
// when true a credential was checked and either accepted (ok) or rejected (403),
// so an expired or cross-purpose credential is never silently downgraded.
func (c *cloudClient) authorizeLibraryStreamCredential(w http.ResponseWriter, r *http.Request, fileID, revisionID, resource, requiredScope string) (handled, ok bool) {
	raw := libraryStreamToken(r)
	if raw == "" {
		return false, false
	}
	session, err := c.verifyLibraryStreamSession(raw, r.URL.Path, c.runtime.id, resource, requiredScope)
	if err != nil || session.FileID != fileID || session.RevisionID != revisionID {
		writeError(w, 403, "流媒体凭据无效、已过期或用途不符")
		return true, false
	}
	return true, true
}

// openLibraryStreamSession exchanges a verified resolve read ticket for a
// longer-lived stream session. The ticket already pins fileId/revisionId/node/
// resourceRef/purpose; the session inherits those bounds and the candidate route
// prefix, and is delivered as an HttpOnly cookie plus a JSON token for remote
// header use.
func (c *cloudClient) openLibraryStreamSession(w http.ResponseWriter, r *http.Request) {
	fileID := strings.TrimSpace(r.PathValue("fileId"))
	var input struct {
		ReadPath string `json:"readPath"`
		Scope    string `json:"scope"`
	}
	if fileID == "" || len(fileID) > 160 || json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, "流媒体会话请求无效")
		return
	}
	if input.Scope != "original" && input.Scope != "preview" {
		writeError(w, http.StatusBadRequest, "流媒体用途无效")
		return
	}
	if !strings.HasPrefix(input.ReadPath, "/api/") || strings.Contains(input.ReadPath, "..") {
		writeError(w, http.StatusBadRequest, "读取路径无效")
		return
	}
	ticketPath, _, _ := strings.Cut(input.ReadPath, "?")
	var prefix string
	switch {
	case strings.HasPrefix(ticketPath, "/api/cloud/files/"):
		rest := strings.TrimPrefix(ticketPath, "/api/cloud/files/")
		objectID, _, found := strings.Cut(rest, "/")
		if !found || objectID == "" {
			writeError(w, http.StatusBadRequest, "读取路径无效")
			return
		}
		prefix = "/api/cloud/files/" + objectID + "/"
	case strings.HasPrefix(ticketPath, "/api/library/read/"):
		rest := strings.TrimPrefix(ticketPath, "/api/library/read/")
		resourceRef, _, found := strings.Cut(rest, "/")
		if !found || resourceRef == "" {
			writeError(w, http.StatusBadRequest, "读取路径无效")
			return
		}
		prefix = "/api/library/read/" + resourceRef + "/"
	default:
		writeError(w, http.StatusBadRequest, "读取路径无效")
		return
	}
	raw := r.Header.Get("X-Myliba-Library-Read")
	parsed, _ := parseLibraryReadTicketPayload(raw)
	ticket, err := c.verifyLibraryReadTicket(raw, ticketPath, input.Scope, parsed.NodeID, ticketResourceForPrefix(prefix))
	if err != nil || ticket.FileID != fileID {
		writeError(w, http.StatusForbidden, "读取凭据无效或已过期")
		return
	}
	token, err := c.signLibraryStreamSession(libraryStreamSession{FileID: ticket.FileID, RevisionID: ticket.RevisionID, NodeID: ticket.NodeID, ResourceRef: ticket.ResourceRef, Scope: input.Scope, PathPrefix: prefix})
	if err != nil {
		writeError(w, 500, "无法签发流媒体会话")
		return
	}
	expires := time.Now().Add(libraryStreamTTL)
	http.SetCookie(w, &http.Cookie{Name: libraryStreamCookieName, Value: token, Path: "/api", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(libraryStreamTTL.Seconds())})
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "scope": input.Scope, "expires": expires.Unix()})
}

func ticketResourceForPrefix(prefix string) string {
	trimmed := strings.TrimSuffix(prefix, "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return ""
}

// parseLibraryReadTicketPayload decodes the unverified ticket body only to learn
// the node it was minted for; verifyLibraryReadTicket still authenticates the
// signature and every bound field before the exchange trusts it.
func parseLibraryReadTicketPayload(raw string) (libraryReadTicket, error) {
	var ticket libraryReadTicket
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return ticket, errors.New("missing read ticket")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ticket, err
	}
	if err := json.Unmarshal(payload, &ticket); err != nil {
		return libraryReadTicket{}, err
	}
	return ticket, nil
}
