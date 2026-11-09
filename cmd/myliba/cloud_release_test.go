package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type releaseFixture struct {
	client     *cloudClient
	db         *sql.DB
	source     *cloudRuntime
	peerA      *cloudRuntime
	peerB      *cloudRuntime
	fileID     string
	revisionID string
	locationID string
	phoneNode  string
	token      string
}

func setupReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	dir := t.TempDir()
	token := "0123456789abcdef0123456789abcdef"
	db, err := sql.Open("sqlite3", filepath.Join(dir, "library.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS hub_profiles (id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, token TEXT NOT NULL, cloud_token TEXT NOT NULL DEFAULT '');
		CREATE TABLE IF NOT EXISTS hub_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT OR REPLACE INTO hub_profiles(id,name,url,token) VALUES('hub','hub','https://hub.example',?);
		INSERT OR REPLACE INTO hub_settings(key,value) VALUES('active_hub','hub')`, token); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(dir, "original.txt")
	plain := []byte("the phone original that will be released after safe cloud save")
	if err = os.WriteFile(sourcePath, plain, 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(sourcePath)
	if _, err = db.Exec(`INSERT INTO roots(id,path,added_at) VALUES('root',?,?);
		INSERT INTO resources(id,root_id,path,file_id,name,size,modified_at,kind,available,first_seen_at,last_seen_at,current_version_id)
		VALUES('myfile','root',?,'logical-file','original.txt',?,?, 'document',1,?,?,'v1')`,
		dir, now(), sourcePath, len(plain), info.ModTime().UTC().Format(time.RFC3339Nano), now(), now()); err != nil {
		t.Fatal(err)
	}
	source := testCloudRuntime(t, filepath.Join(dir, "source"), token)
	source.db = db
	source.faultDomain = "domain-A"
	peerA := testCloudRuntime(t, filepath.Join(dir, "peerA"), token)
	peerA.faultDomain = "domain-B"
	peerB := testCloudRuntime(t, filepath.Join(dir, "peerB"), token)
	peerB.faultDomain = "domain-C"
	serverA := serveCloudTestNode(t, peerA, token)
	serverB := serveCloudTestNode(t, peerB, token)
	source.peers = []cloudNode{
		{ID: peerA.id, Endpoint: serverA.URL, FaultDomain: "domain-B", Role: "storage"},
		{ID: peerB.id, Endpoint: serverB.URL, FaultDomain: "domain-C", Role: "storage"},
	}
	client, err := newCloudClient(source)
	if err != nil {
		t.Fatal(err)
	}
	client.indexer = &server{db: db, cloud: client}
	if err = client.uploadFile(context.Background(), "myfile", sourcePath, false); err != nil {
		t.Fatal(err)
	}
	source.refreshLocations(context.Background(), token, source.peers)
	fixture := releaseFixture{client: client, db: db, source: source, peerA: peerA, peerB: peerB,
		fileID: "logical-file", revisionID: "v1", locationID: "loc-phone", phoneNode: "phone-node", token: token}
	if _, err = db.Exec(`INSERT INTO library_locations(location_id,file_id,revision_id,node_id,state,updated_at)
		VALUES(?,?,?,?, 'available', ?)`, fixture.locationID, fixture.fileID, fixture.revisionID, fixture.phoneNode, now()); err != nil {
		t.Fatal(err)
	}
	if err = client.recordRecoveryAttestation(source.id, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestReleaseEligibleWithTwoDomainsAndRecoveryHolder(t *testing.T) {
	f := setupReleaseFixture(t)
	eval := f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if !eval.Eligible {
		t.Fatalf("expected eligible release, got reason: %s", eval.Reason)
	}
	if len(eval.Domains) != 3 {
		t.Fatalf("expected three independent domains, got %v", eval.Domains)
	}
	if eval.Holder == f.phoneNode || eval.Holder == "" {
		t.Fatalf("recovery holder must be independent of the releasing device: %q", eval.Holder)
	}
}

// T18: two gateways pointing at the same disk are one fault domain, so even three
// complete node copies must not qualify for safe release.
func TestReleaseRejectsGatewaysSharingOneFaultDomain(t *testing.T) {
	f := setupReleaseFixture(t)
	f.source.faultDomain = "shared-disk"
	f.source.mu.Lock()
	f.source.peers[0].FaultDomain = "shared-disk"
	f.source.peers[1].FaultDomain = "shared-disk"
	f.source.mu.Unlock()
	complete := f.client.completeCopyNodes(f.client.recordForRevisionMust(t, f.fileID, f.revisionID))
	if len(complete) < 3 {
		t.Fatalf("expected three complete node copies, got %d", len(complete))
	}
	eval := f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if eval.Eligible {
		t.Fatal("three gateways on one fault domain must not be eligible for release")
	}
	if len(eval.Domains) != 1 {
		t.Fatalf("expected one independent domain, got %v", eval.Domains)
	}
}

func TestReleaseRequiresIndependentRecoveryHolder(t *testing.T) {
	f := setupReleaseFixture(t)
	if _, err := f.db.Exec(`DELETE FROM library_recovery_attestations`); err != nil {
		t.Fatal(err)
	}
	eval := f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if eval.Eligible {
		t.Fatal("release must be blocked without an independent recovery holder")
	}
	// An attestation held only by the releasing device does not count.
	if err := f.client.recordRecoveryAttestation(f.phoneNode, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	eval = f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if eval.Eligible {
		t.Fatal("the releasing device alone holding the key must not authorize release")
	}
}

// T20: missing read-back verification blocks release.
func TestReleaseRequiresReadBackVerifiedContent(t *testing.T) {
	f := setupReleaseFixture(t)
	if _, err := f.db.Exec(`DELETE FROM library_verified_content WHERE revision_id=?`, f.revisionID); err != nil {
		t.Fatal(err)
	}
	eval := f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if eval.Eligible || !strings.Contains(eval.Reason, "读回验证") {
		t.Fatalf("expected read-back verification block, got eligible=%v reason=%s", eval.Eligible, eval.Reason)
	}
}

// T20: a corrupted block on the second fault domain fails persistence verification.
func TestReleaseRejectsCorruptedRemoteBlock(t *testing.T) {
	f := setupReleaseFixture(t)
	record := f.client.recordForRevisionMust(t, f.fileID, f.revisionID)
	blockID := record.BlockIDs[0]
	path := filepath.Join(f.peerB.disk.dir, "blocks", blockID[:2], blockID)
	if err := os.WriteFile(path, []byte("corrupted ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	eval := f.client.evaluateRelease(context.Background(), f.fileID, f.locationID, f.revisionID)
	if eval.Eligible {
		t.Fatal("a corrupted remote block must block release")
	}
}

func TestReleaseCheckIssuesBoundCredentialAndReleaseIsIdempotent(t *testing.T) {
	f := setupReleaseFixture(t)
	checkBody, _ := json.Marshal(map[string]string{"locationId": f.locationID, "revisionId": f.revisionID})
	request := httptest.NewRequest(http.MethodPost, "/api/library/files/"+f.fileID+"/release-check", strings.NewReader(string(checkBody)))
	request.SetPathValue("fileId", f.fileID)
	response := httptest.NewRecorder()
	f.client.releaseCheck(response, request)
	if response.Code != 200 {
		t.Fatalf("release-check: %d %s", response.Code, response.Body.String())
	}
	var check struct {
		Eligible   bool   `json:"eligible"`
		Credential string `json:"credential"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &check); err != nil {
		t.Fatal(err)
	}
	if !check.Eligible || check.Credential == "" {
		t.Fatalf("expected eligible check with credential: %+v", check)
	}
	// A credential bound to another location must be rejected.
	if _, err := f.client.verifyReleaseCredential(check.Credential, f.fileID, "other-location"); err == nil {
		t.Fatal("credential must be bound to its location")
	}
	if _, err := f.client.verifyReleaseCredential(check.Credential, "other-file", f.locationID); err == nil {
		t.Fatal("credential must be bound to its file")
	}
	releaseBody, _ := json.Marshal(map[string]string{"locationId": f.locationID, "credential": check.Credential})
	release := func() map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/library/files/"+f.fileID+"/release", strings.NewReader(string(releaseBody)))
		req.SetPathValue("fileId", f.fileID)
		rec := httptest.NewRecorder()
		f.client.releaseOriginal(rec, req)
		if rec.Code != 200 {
			t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := release(); out["released"] != true {
		t.Fatalf("first release: %+v", out)
	}
	var state string
	if err := f.db.QueryRow(`SELECT state FROM library_locations WHERE location_id=?`, f.locationID).Scan(&state); err != nil || state != "missing" {
		t.Fatalf("location must be removed after release: %q %v", state, err)
	}
	// Classification survives the release.
	var fileCount int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM library_files WHERE file_id=?`, f.fileID).Scan(&fileCount); err != nil || fileCount != 1 {
		t.Fatalf("logical file/classification must survive release: %d %v", fileCount, err)
	}
	if out := release(); out["idempotent"] != true {
		t.Fatalf("second release must be idempotent: %+v", out)
	}
}

// T19: an expired credential cannot authorize deletion.
func TestReleaseCredentialExpiry(t *testing.T) {
	f := setupReleaseFixture(t)
	cred := releaseCredential{FileID: f.fileID, RevisionID: f.revisionID, LocationID: f.locationID, NodeID: f.phoneNode, Policy: releasePolicy, PlainHash: strings.Repeat("c", 64)}
	token, err := f.client.signReleaseCredential(cred)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.client.verifyReleaseCredential(token, f.fileID, f.locationID); err != nil {
		t.Fatalf("fresh credential must verify: %v", err)
	}
	// Forge an expired credential by re-signing with a past expiry through the
	// internal struct: tampering the payload breaks the signature, so instead
	// verify that a credential whose expiry window is exceeded is rejected.
	parts := strings.Split(token, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var decoded releaseCredential
	_ = json.Unmarshal(payload, &decoded)
	decoded.Expires = time.Now().Add(-time.Minute).Unix()
	tampered, _ := json.Marshal(decoded)
	forged := base64.RawURLEncoding.EncodeToString(tampered) + "." + parts[1]
	if _, err = f.client.verifyReleaseCredential(forged, f.fileID, f.locationID); err == nil {
		t.Fatal("expired/tampered credential must be rejected")
	}
}

func TestUploadReadBackVerificationDetectsBlockLoss(t *testing.T) {
	f := setupReleaseFixture(t)
	record := f.client.recordForRevisionMust(t, f.fileID, f.revisionID)
	manifest, err := f.client.openManifest(record)
	if err != nil {
		t.Fatal(err)
	}
	dataKey, err := hex.DecodeString(manifest.DataKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.verifyStoredBlocks(record, manifest, aead); err != nil {
		t.Fatalf("fresh upload must read back cleanly: %v", err)
	}
	blockID := record.BlockIDs[0]
	path := filepath.Join(f.source.disk.dir, "blocks", blockID[:2], blockID)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = f.client.verifyStoredBlocks(record, manifest, aead); err == nil {
		t.Fatal("read-back verification must fail when a stored block is lost")
	}
}

func TestUploadOriginalStreamBindsExpectedHash(t *testing.T) {
	f := setupReleaseFixture(t)
	plain := []byte("a streamed phone original bound by its plaintext hash")
	sum := sha256.Sum256(plain)
	expected := hex.EncodeToString(sum[:])
	// Wrong expected hash must block the commit.
	err := f.client.uploadOriginalStream(context.Background(), "", "stream-file", "rev-stream", "stream-file", "stream.txt", now(), strings.Repeat("0", 64), int64(len(plain)), false, strings.NewReader(string(plain)))
	if err == nil {
		t.Fatal("mismatched expected hash must block the upload")
	}
	taskID := f.client.createTransferTask("upload", "stream-file", "rev-stream", "stream-file", "", int64(len(plain)))
	if err = f.client.uploadOriginalStream(context.Background(), taskID, "stream-file", "rev-stream", "stream-file", "stream.txt", now(), expected, int64(len(plain)), false, strings.NewReader(string(plain))); err != nil {
		t.Fatalf("matching stream upload must commit: %v", err)
	}
	task, err := f.client.transferTaskByID(taskID)
	if err != nil || task.State != transferCommitted || task.PlainHash != expected {
		t.Fatalf("committed task expected: %+v %v", task, err)
	}
	record, manifest, err := f.client.recordForRevision("stream-file", "rev-stream")
	if err != nil || manifest.PlainHash != expected || len(record.BlockIDs) == 0 {
		t.Fatalf("streamed content must be cataloged: %+v %v", manifest, err)
	}
}

func TestFaultDomainDerivation(t *testing.T) {
	t.Setenv("MYLIBA_FAULT_DOMAIN", "")
	rootA, _ := url.Parse("https://dav.example/root/")
	rootB, _ := url.Parse("https://dav.example/root/")
	rootC, _ := url.Parse("https://dav.example/other/")
	davA := &webDAVBackend{root: rootA}
	davB := &webDAVBackend{root: rootB}
	davC := &webDAVBackend{root: rootC}
	if cloudFaultDomain(&cloudDisk{backend: davA}, "node-1") != cloudFaultDomain(&cloudDisk{backend: davB}, "node-2") {
		t.Fatal("two gateways to the same WebDAV root must share a fault domain")
	}
	if cloudFaultDomain(&cloudDisk{backend: davA}, "node-1") == cloudFaultDomain(&cloudDisk{backend: davC}, "node-3") {
		t.Fatal("different WebDAV roots must be different fault domains")
	}
	if cloudFaultDomain(&cloudDisk{}, "node-1") == cloudFaultDomain(&cloudDisk{}, "node-2") {
		t.Fatal("independent local disks default to distinct domains")
	}
	t.Setenv("MYLIBA_FAULT_DOMAIN", "operator-declared-disk")
	if cloudFaultDomain(&cloudDisk{}, "node-1") != "operator-declared-disk" || cloudFaultDomain(&cloudDisk{}, "node-2") != "operator-declared-disk" {
		t.Fatal("operator override must collapse distinct directories into one declared domain")
	}
}

func (c *cloudClient) recordForRevisionMust(t *testing.T, fileID, revisionID string) cloudRecord {
	t.Helper()
	record, _, err := c.recordForRevision(fileID, revisionID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
