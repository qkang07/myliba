package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The phone pushes a >1 MiB original in two RTC-sized chunks; the node assembles,
// encrypts, read-back-verifies and commits, then registers the SAF source as a
// releasable location. This exercises the full P5 phone closed loop on the node.
func TestChunkedOriginalUploadRegistersReleasableSource(t *testing.T) {
	f := setupReleaseFixture(t)
	plain := make([]byte, (1<<20)+4096)
	for i := range plain {
		plain[i] = byte(i % 251)
	}
	sum := sha256.Sum256(plain)
	plainHash := hex.EncodeToString(sum[:])
	fileID, revisionID := "chunk-file", "rev-chunk"
	nodeID, storageID, resourceRef := "phone-node", "content://root", "ref-abc"
	uploadID := strings.Repeat("a", 32)
	total := len(plain)

	send := func(offset int, body []byte, final bool, finalize bool) *httptest.ResponseRecorder {
		t.Helper()
		target := "/api/cloud/files/" + fileID + "/original/chunk?uploadId=" + uploadID +
			"&offset=" + itoa(offset) + "&total=" + itoa(total)
		if final {
			target += "&final=1"
		}
		if finalize {
			target += "&revisionId=" + revisionID + "&name=chunk.bin&plainHash=" + plainHash +
				"&objectId=" + fileID + "&nodeId=" + nodeID + "&storageId=" + storageID + "&resourceRef=" + resourceRef
		}
		req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(string(body)))
		req.SetPathValue("fileId", fileID)
		rec := httptest.NewRecorder()
		f.client.uploadOriginalChunk(rec, req)
		return rec
	}

	if rec := send(0, plain[:1<<20], false, false); rec.Code != http.StatusNoContent {
		t.Fatalf("first chunk: %d %s", rec.Code, rec.Body.String())
	}
	rec := send(1<<20, plain[1<<20:], true, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("final chunk: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TaskID     string `json:"taskId"`
		LocationID string `json:"locationId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	wantLocation := sourceLocationID(nodeID, resourceRef)
	if out.LocationID != wantLocation || out.TaskID == "" {
		t.Fatalf("expected task and source location, got %+v", out)
	}

	task := waitForTransfer(t, f.client, out.TaskID, transferCommitted)
	if task.PlainHash != plainHash {
		t.Fatalf("committed plain hash mismatch: %s", task.PlainHash)
	}
	var locNode, locState string
	if err := f.db.QueryRow(`SELECT node_id,state FROM library_locations WHERE location_id=? AND file_id=?`, wantLocation, fileID).Scan(&locNode, &locState); err != nil {
		t.Fatalf("source location must be registered: %v", err)
	}
	if locNode != nodeID || locState != "available" {
		t.Fatalf("source location binding wrong: %q %q", locNode, locState)
	}
	record, manifest, err := f.client.recordForRevision(fileID, revisionID)
	if err != nil || manifest.PlainHash != plainHash || len(record.BlockIDs) == 0 {
		t.Fatalf("chunked content must be cataloged: %+v %v", manifest, err)
	}
	f.source.refreshLocations(context.Background(), f.token, f.source.peers)
	eval := f.client.evaluateRelease(context.Background(), fileID, wantLocation, revisionID)
	if !eval.Eligible {
		t.Fatalf("uploaded original with two independent domains must be releasable: %s", eval.Reason)
	}
}

// A chunk larger than the 1 MiB limit is rejected before staging.
func TestChunkedOriginalUploadRejectsOversizedChunk(t *testing.T) {
	f := setupReleaseFixture(t)
	body := strings.NewReader(strings.Repeat("x", (1<<20)+1))
	req := httptest.NewRequest(http.MethodPut, "/api/cloud/files/chunk-file/original/chunk?uploadId="+strings.Repeat("b", 32)+"&offset=0&total="+itoa((1<<20)+1), body)
	req.SetPathValue("fileId", "chunk-file")
	rec := httptest.NewRecorder()
	f.client.uploadOriginalChunk(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunk must be rejected, got %d", rec.Code)
	}
}

// Finalizing with a wrong plaintext hash must not bind a manifest to different
// content; the task ends blocked rather than committed (T19 version safety).
func TestChunkedOriginalUploadRejectsHashMismatch(t *testing.T) {
	f := setupReleaseFixture(t)
	plain := []byte("phone original bytes")
	uploadID := strings.Repeat("c", 32)
	target := "/api/cloud/files/hash-file/original/chunk?uploadId=" + uploadID + "&offset=0&total=" + itoa(len(plain)) +
		"&final=1&revisionId=rev-hash&name=hash.bin&plainHash=" + strings.Repeat("0", 64) + "&nodeId=phone-node&resourceRef=ref-hash"
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(string(plain)))
	req.SetPathValue("fileId", "hash-file")
	rec := httptest.NewRecorder()
	f.client.uploadOriginalChunk(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("final chunk accept: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		TaskID string `json:"taskId"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	task := waitForTransfer(t, f.client, out.TaskID, transferBlocked)
	if !strings.Contains(task.Detail, "摘要") {
		t.Fatalf("expected hash mismatch block, got %+v", task)
	}
}

func waitForTransfer(t *testing.T, c *cloudClient, taskID, want string) transferTask {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		task, err := c.transferTaskByID(taskID)
		if err == nil && (task.State == want || task.State == transferRetryableFailed || task.State == transferBlocked || task.State == transferCancelled) {
			if task.State != want {
				t.Fatalf("transfer ended in %s, wanted %s: %s", task.State, want, task.Detail)
			}
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transfer %s did not reach %s in time", taskID, want)
	return transferTask{}
}
