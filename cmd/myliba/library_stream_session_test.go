package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func streamTestClient(node string) *cloudClient {
	var key [32]byte
	copy(key[:], []byte("stream-session-test-key-0123456789ab"))
	return &cloudClient{runtime: &cloudRuntime{id: node}, key: key}
}

// signRawStreamSession signs a session with the caller-supplied expiry so tests
// can exercise expired and over-long windows that the public signer never emits.
func signRawStreamSession(c *cloudClient, s libraryStreamSession) string {
	s.LibraryID = c.libraryID()
	payload, _ := json.Marshal(s)
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestLibraryStreamSessionScopeAndBinding(t *testing.T) {
	c := streamTestClient("node-a")
	prefix := "/api/cloud/files/obj1/"
	path := prefix + "versions/v1/file"
	base := libraryStreamSession{FileID: "file-1", RevisionID: "v1", NodeID: "node-a", ResourceRef: "obj1", Scope: "original", PathPrefix: prefix}

	token, err := c.signLibraryStreamSession(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.verifyLibraryStreamSession(token, path, "node-a", "obj1", "original"); err != nil {
		t.Fatalf("original session for original route: %v", err)
	}
	if _, err := c.verifyLibraryStreamSession(token, prefix+"video/720p/segments/0", "node-a", "obj1", "preview"); err != nil {
		t.Fatalf("original session should cover preview streaming: %v", err)
	}
	if _, err := c.verifyLibraryStreamSession(token, path, "node-b", "obj1", "original"); err == nil {
		t.Fatal("cross-node stream session accepted")
	}
	if _, err := c.verifyLibraryStreamSession(token, path, "node-a", "obj2", "original"); err == nil {
		t.Fatal("cross-resource stream session accepted")
	}
	if _, err := c.verifyLibraryStreamSession(token, "/api/cloud/files/obj2/versions/v1/file", "node-a", "obj1", "original"); err == nil {
		t.Fatal("path outside bound prefix accepted")
	}

	preview := base
	preview.Scope = "preview"
	preview.Expires = time.Now().Add(libraryStreamTTL).Unix()
	previewToken := signRawStreamSession(c, preview)
	if _, err := c.verifyLibraryStreamSession(previewToken, prefix+"video/720p/segments/0", "node-a", "obj1", "preview"); err != nil {
		t.Fatalf("preview session for preview route: %v", err)
	}
	if _, err := c.verifyLibraryStreamSession(previewToken, path, "node-a", "obj1", "original"); err == nil {
		t.Fatal("preview stream session authorized the original download route")
	}
}

func TestLibraryStreamSessionExpiryAndCrossType(t *testing.T) {
	c := streamTestClient("node-a")
	prefix := "/api/library/read/ref1/"
	path := prefix + "file"
	base := libraryStreamSession{FileID: "file-1", RevisionID: "v1", NodeID: "node-a", ResourceRef: "ref1", Scope: "original", PathPrefix: prefix}

	expired := base
	expired.Expires = time.Now().Add(-time.Minute).Unix()
	if _, err := c.verifyLibraryStreamSession(signRawStreamSession(c, expired), path, "node-a", "ref1", "original"); err == nil {
		t.Fatal("expired stream session accepted")
	}
	tooLong := base
	tooLong.Expires = time.Now().Add(libraryStreamTTL + time.Hour).Unix()
	if _, err := c.verifyLibraryStreamSession(signRawStreamSession(c, tooLong), path, "node-a", "ref1", "original"); err == nil {
		t.Fatal("stream session beyond the allowed window accepted")
	}

	// A two-minute read ticket must not be usable as a stream session, and a
	// stream session must not satisfy the read-ticket verifier.
	ticket, err := c.signLibraryReadTicket(libraryReadTicket{FileID: "file-1", RevisionID: "v1", Purpose: "original", NodeID: "node-a", ResourceRef: "ref1", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.verifyLibraryStreamSession(ticket, path, "node-a", "ref1", "original"); err == nil {
		t.Fatal("read ticket reused as a stream session")
	}
	streamToken, err := c.signLibraryStreamSession(base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.verifyLibraryReadTicket(streamToken, path, "original", "node-a", "ref1"); err == nil {
		t.Fatal("stream session reused as a read ticket")
	}
}

func TestLibraryStreamCredentialHandling(t *testing.T) {
	c := streamTestClient("node-a")
	prefix := "/api/cloud/files/obj1/"
	path := prefix + "versions/v1/file"
	token, err := c.signLibraryStreamSession(libraryStreamSession{FileID: "file-1", RevisionID: "v1", NodeID: "node-a", ResourceRef: "obj1", Scope: "original", PathPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	if handled, _ := c.authorizeLibraryStreamCredential(response, request, "file-1", "v1", "obj1", "original"); handled {
		t.Fatal("absent credential reported as handled")
	}

	request.Header.Set("X-Myliba-Library-Read", token)
	response = httptest.NewRecorder()
	if handled, ok := c.authorizeLibraryStreamCredential(response, request, "file-1", "v1", "obj1", "original"); !handled || !ok || response.Code != http.StatusOK {
		t.Fatalf("valid credential: handled=%t ok=%t code=%d", handled, ok, response.Code)
	}

	response = httptest.NewRecorder()
	if handled, ok := c.authorizeLibraryStreamCredential(response, request, "file-1", "v2", "obj1", "original"); !handled || ok || response.Code != http.StatusForbidden {
		t.Fatalf("revision mismatch must reject without fallback: handled=%t ok=%t code=%d", handled, ok, response.Code)
	}

	requestWithCookie := httptest.NewRequest(http.MethodGet, path, nil)
	requestWithCookie.AddCookie(&http.Cookie{Name: libraryStreamCookieName, Value: token})
	response = httptest.NewRecorder()
	if handled, ok := c.authorizeLibraryStreamCredential(response, requestWithCookie, "file-1", "v1", "obj1", "original"); !handled || !ok {
		t.Fatalf("HttpOnly cookie credential: handled=%t ok=%t code=%d", handled, ok, response.Code)
	}
}

func TestLibraryStreamSessionExchange(t *testing.T) {
	c := streamTestClient("node-a")
	cloudPath := "/api/cloud/files/obj1/versions/v1/file"
	originalTicket, err := c.signLibraryReadTicket(libraryReadTicket{FileID: "file-1", RevisionID: "v1", Purpose: "original", NodeID: "node-a", ResourceRef: "obj1", Path: cloudPath})
	if err != nil {
		t.Fatal(err)
	}

	exchange := func(scope, readPath, ticket, fileId string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"readPath": readPath, "scope": scope})
		request := httptest.NewRequest(http.MethodPost, "/api/library/files/"+fileId+"/stream-session", strings.NewReader(string(body)))
		request.SetPathValue("fileId", fileId)
		request.Header.Set("X-Myliba-Library-Read", ticket)
		response := httptest.NewRecorder()
		c.openLibraryStreamSession(response, request)
		return response
	}

	response := exchange("original", cloudPath, originalTicket, "file-1")
	if response.Code != http.StatusOK {
		t.Fatalf("exchange original: %d %s", response.Code, response.Body.String())
	}
	var issued struct {
		Token   string `json:"token"`
		Scope   string `json:"scope"`
		Expires int64  `json:"expires"`
	}
	if json.Unmarshal(response.Body.Bytes(), &issued) != nil || issued.Token == "" || issued.Scope != "original" {
		t.Fatalf("issued session: %+v %s", issued, response.Body.String())
	}
	cookieSeen := false
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == libraryStreamCookieName && cookie.HttpOnly && cookie.Value == issued.Token && cookie.Path == "/api" {
			cookieSeen = true
		}
	}
	if !cookieSeen {
		t.Fatalf("missing HttpOnly stream cookie: %v", response.Result().Cookies())
	}
	if _, err := c.verifyLibraryStreamSession(issued.Token, cloudPath, "node-a", "obj1", "original"); err != nil {
		t.Fatalf("issued token verifies: %v", err)
	}

	// A preview ticket cannot be escalated into an original-scoped session.
	previewPath := "/api/library/read/ref1/video-preview"
	previewTicket, err := c.signLibraryReadTicket(libraryReadTicket{FileID: "file-1", RevisionID: "v1", Purpose: "preview", NodeID: "node-a", ResourceRef: "ref1", Path: previewPath})
	if err != nil {
		t.Fatal(err)
	}
	if response := exchange("original", previewPath, previewTicket, "file-1"); response.Code != http.StatusForbidden {
		t.Fatalf("preview ticket escalated to original: %d %s", response.Code, response.Body.String())
	}
	if response := exchange("preview", previewPath, previewTicket, "file-1"); response.Code != http.StatusOK {
		t.Fatalf("preview ticket exchanged for preview session: %d %s", response.Code, response.Body.String())
	}
	// The ticket is bound to its fileId; a different logical file is rejected.
	if response := exchange("original", cloudPath, originalTicket, "file-2"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-file exchange: %d %s", response.Code, response.Body.String())
	}
	// An expired read ticket cannot mint a session.
	expiredTicket := signRawReadTicket(c, libraryReadTicket{FileID: "file-1", RevisionID: "v1", Purpose: "original", NodeID: "node-a", ResourceRef: "obj1", Path: cloudPath, Expires: time.Now().Add(-time.Minute).Unix()})
	if response := exchange("original", cloudPath, expiredTicket, "file-1"); response.Code != http.StatusForbidden {
		t.Fatalf("expired ticket exchange: %d %s", response.Code, response.Body.String())
	}
}

func signRawReadTicket(c *cloudClient, ticket libraryReadTicket) string {
	ticket.LibraryID = c.libraryID()
	payload, _ := json.Marshal(ticket)
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}