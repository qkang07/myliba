package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The phone reaches a query node only over the P2P RTC data channel, whose
// request body is capped at 2 MiB (rtc.go). A whole original cannot fit, so the
// phone pushes <=1 MiB plaintext chunks here and the node stages them on its own
// disk, then runs the shared encrypt -> store -> read-back-verify -> commit
// pipeline. The Hub never sees plaintext: it only relays RTC signaling
// (docs/unified-library-design.md §10.1). The node is a trusted key holder, so
// transient plaintext staging on its disk is within the same trust domain as the
// ciphertext blocks it already stores.
const originalChunkLimit = transferChunkLimit

func sourceLocationID(nodeID, resourceRef string) string {
	if nodeID == "" || resourceRef == "" {
		return ""
	}
	return "source:" + nodeID + ":" + resourceRef
}

// registerSourceLocation records the phone's SAF source as a releasable local
// location of the immutable revision. Classification lives elsewhere and is never
// touched here. The revision row is inserted first because library_locations has
// an enforced foreign key to library_revisions.
func (c *cloudClient) registerSourceLocation(fileID, revisionID, nodeID, storageID, resourceRef string, size int64, modified string) error {
	locationID := sourceLocationID(nodeID, resourceRef)
	if locationID == "" {
		return nil
	}
	if c.runtime == nil || c.runtime.db == nil {
		return errors.New("资源库数据库不可用")
	}
	if _, err := c.runtime.db.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO NOTHING`, fileID, now(), now()); err != nil {
		return err
	}
	if _, err := c.runtime.db.Exec(`INSERT INTO library_revisions(revision_id,file_id,size,modified_at,created_at) VALUES(?,?,?,?,?) ON CONFLICT(revision_id) DO NOTHING`, revisionID, fileID, size, modified, now()); err != nil {
		return err
	}
	_, err := c.runtime.db.Exec(`INSERT INTO library_locations(location_id,file_id,revision_id,node_id,storage_id,resource_ref,state,updated_at)
		VALUES(?,?,?,?,?,?,'available',?)
		ON CONFLICT(location_id) DO UPDATE SET file_id=excluded.file_id,revision_id=excluded.revision_id,node_id=excluded.node_id,storage_id=excluded.storage_id,resource_ref=excluded.resource_ref,state='available',updated_at=excluded.updated_at`,
		locationID, fileID, revisionID, nodeID, storageID, resourceRef, now())
	return err
}

func (c *cloudClient) uploadOriginalChunk(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	if fileID == "" || !validTransferIdentity(fileID) {
		writeError(w, 400, "文件标识无效")
		return
	}
	if r.ContentLength < 0 || r.ContentLength > originalChunkLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "单次上传分块不能超过 1 MiB")
		return
	}
	if c.runtime == nil || c.runtime.db == nil {
		writeError(w, 409, "资源库数据库不可用")
		return
	}
	query := r.URL.Query()
	uploadID := query.Get("uploadId")
	if len(uploadID) < 16 || len(uploadID) > 64 || strings.Trim(uploadID, "0123456789abcdef") != "" {
		writeError(w, 400, "上传 ID 无效")
		return
	}
	offset, err := strconv.ParseInt(query.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, 400, "文件偏移无效")
		return
	}
	total, err := strconv.ParseInt(query.Get("total"), 10, 64)
	if err != nil || total < 0 || offset+r.ContentLength > total {
		writeError(w, 400, "文件大小无效")
		return
	}
	dir := filepath.Join(c.runtime.disk.dir, "original-uploads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeError(w, 500, "无法创建上传暂存目录")
		return
	}
	temp := filepath.Join(dir, uploadID+".part")
	flags := os.O_WRONLY
	if offset == 0 {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(temp, flags, 0600)
	if err != nil {
		writeError(w, http.StatusConflict, "无法创建上传暂存文件")
		return
	}
	if _, err = file.Seek(offset, io.SeekStart); err == nil {
		_, err = io.CopyN(file, r.Body, r.ContentLength)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		writeError(w, 500, "写入上传分块失败")
		return
	}
	if query.Get("final") != "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	written, statErr := os.Stat(temp)
	if statErr != nil || written.Size() != total {
		_ = os.Remove(temp)
		writeError(w, http.StatusConflict, "上传文件大小不完整")
		return
	}
	revisionID := strings.TrimSpace(query.Get("revisionId"))
	name := strings.TrimSpace(query.Get("name"))
	expectedHash := strings.ToLower(strings.TrimSpace(query.Get("plainHash")))
	modified := strings.TrimSpace(query.Get("modified"))
	history := query.Get("history") == "1"
	objectID := strings.TrimSpace(query.Get("objectId"))
	if objectID == "" {
		objectID = fileID
	}
	nodeID := strings.TrimSpace(query.Get("nodeId"))
	storageID := strings.TrimSpace(query.Get("storageId"))
	resourceRef := strings.TrimSpace(query.Get("resourceRef"))
	if revisionID == "" || name == "" || !validTransferIdentity(revisionID) || !validCloudHash(expectedHash) {
		_ = os.Remove(temp)
		writeError(w, 400, "完成上传需要 revisionId、name 与有效的 plainHash")
		return
	}
	if modified == "" {
		modified = now()
	}
	if err := c.registerSourceLocation(fileID, revisionID, nodeID, storageID, resourceRef, total, modified); err != nil {
		_ = os.Remove(temp)
		writeError(w, 500, "无法登记来源位置："+err.Error())
		return
	}
	taskID := c.createTransferTask("upload", fileID, revisionID, objectID, sourceLocationID(nodeID, resourceRef), total)
	c.jobsMu.Lock()
	if c.jobs[objectID] == "上传中" {
		c.jobsMu.Unlock()
		c.setTransferState(taskID, transferBlocked, "同一文件正在上传")
		_ = os.Remove(temp)
		writeError(w, 409, "文件正在上传")
		return
	}
	c.jobs[objectID] = "上传中"
	c.jobsMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		defer os.Remove(temp)
		staged, openErr := os.Open(temp)
		var err error
		if openErr != nil {
			err = openErr
			c.setTransferState(taskID, transferRetryableFailed, err.Error())
		} else {
			err = c.uploadOriginalStream(ctx, taskID, fileID, revisionID, objectID, name, modified, expectedHash, total, history, staged)
			staged.Close()
		}
		c.jobsMu.Lock()
		if err != nil {
			c.jobs[objectID] = "失败：" + err.Error()
		} else {
			c.jobs[objectID] = "已加入云层"
		}
		c.jobsMu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "上传中", "taskId": taskID, "objectId": objectID, "locationId": sourceLocationID(nodeID, resourceRef),
	})
}
