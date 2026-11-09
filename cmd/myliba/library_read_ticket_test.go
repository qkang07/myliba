package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLibraryReadTicketScopeAndExpiry(t *testing.T) {
	client := &cloudClient{}
	copy(client.key[:], []byte("0123456789abcdef0123456789abcdef"))
	path := "/api/library/read/source-ref/file"
	issued, err := client.signLibraryReadTicket(libraryReadTicket{FileID: "file-1", RevisionID: "revision-1", Purpose: "original", NodeID: "source", ResourceRef: "source-ref", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.verifyLibraryReadTicket(issued, path, "original", "source", "source-ref"); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []struct{ path, purpose, node, resource string }{
		{path, "preview", "source", "source-ref"}, {path, "original", "other", "source-ref"}, {path, "original", "source", "other"}, {"/api/library/read/source-ref/thumbnail", "original", "source", "source-ref"},
	} {
		if _, err := client.verifyLibraryReadTicket(issued, changed.path, changed.purpose, changed.node, changed.resource); err == nil {
			t.Fatalf("accepted altered scope: %+v", changed)
		}
	}
	expired := libraryReadTicket{LibraryID: client.libraryID(), FileID: "file-1", RevisionID: "revision-1", Purpose: "original", NodeID: "source", ResourceRef: "source-ref", Path: path, Expires: time.Now().Add(-time.Minute).Unix()}
	payload, err := json.Marshal(expired)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, client.key[:])
	_, _ = mac.Write(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := client.verifyLibraryReadTicket(encoded, path, "original", "source", "source-ref"); err == nil {
		t.Fatal("accepted expired ticket")
	}
}

func TestCloudLibraryReadTicketPinsRevisionAndPurpose(t *testing.T) {
	client := &cloudClient{runtime: &cloudRuntime{id: "query"}}
	copy(client.key[:], []byte("0123456789abcdef0123456789abcdef"))
	path := "/api/cloud/files/object/versions/v1/file"
	token, err := client.signLibraryReadTicket(libraryReadTicket{FileID:"logical",RevisionID:"v1",Purpose:"original",NodeID:"query",ResourceRef:"object",Path:path})
	if err != nil { t.Fatal(err) }
	record := cloudRecord{ObjectID:"object",RevisionID:"v1"}
	manifest := cloudManifest{FileID:"logical"}
	request := httptest.NewRequest("GET",path+"?libraryRead=1",nil);request.SetPathValue("revision","v1");request.Header.Set("X-Myliba-Library-Read",token)
	if !client.authorizeCloudLibraryRead(httptest.NewRecorder(),request,record,manifest,"original") { t.Fatal("valid read rejected") }
	if client.authorizeCloudLibraryRead(httptest.NewRecorder(),request,record,manifest,"thumbnail") { t.Fatal("cross-purpose read accepted") }
	request.SetPathValue("revision","v2")
	if client.authorizeCloudLibraryRead(httptest.NewRecorder(),request,record,manifest,"original") { t.Fatal("cross-revision read accepted") }
}
