package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Upload task states follow docs/unified-library-design.md §9. A task reaches
// committed only after the full manifest and read-back content verification are
// persisted; any phase can fall into retryableFailed, blocked or cancelled.
const (
	transferQueued          = "queued"
	transferReading         = "reading"
	transferUploading       = "uploading"
	transferVerifying       = "verifying"
	transferCommitted       = "committed"
	transferRetryableFailed = "retryableFailed"
	transferBlocked         = "blocked"
	transferCancelled       = "cancelled"
)

type transferTask struct {
	TaskID     string `json:"taskId"`
	Kind       string `json:"kind"`
	FileID     string `json:"fileId"`
	RevisionID string `json:"revisionId"`
	ObjectID   string `json:"objectId"`
	LocationID string `json:"locationId"`
	State      string `json:"state"`
	Detail     string `json:"detail"`
	PlainHash  string `json:"plainHash"`
	Size       int64  `json:"size"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

func (c *cloudClient) createTransferTask(kind, fileID, revisionID, objectID, locationID string, size int64) string {
	if c.runtime == nil || c.runtime.db == nil {
		return ""
	}
	taskID := newID()
	_, err := c.runtime.db.Exec(`INSERT INTO library_transfer_tasks(task_id,kind,file_id,revision_id,object_id,location_id,state,detail,plain_hash,size,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, taskID, kind, fileID, revisionID, objectID, locationID, transferQueued, "", "", size, now(), now())
	if err != nil {
		return ""
	}
	return taskID
}

func (c *cloudClient) setTransferState(taskID, state, detail string) {
	if taskID == "" || c.runtime == nil || c.runtime.db == nil {
		return
	}
	_, _ = c.runtime.db.Exec(`UPDATE library_transfer_tasks SET state=?,detail=?,updated_at=? WHERE task_id=?`, state, detail, now(), taskID)
}

func (c *cloudClient) setTransferHash(taskID, plainHash string) {
	if taskID == "" || c.runtime == nil || c.runtime.db == nil {
		return
	}
	_, _ = c.runtime.db.Exec(`UPDATE library_transfer_tasks SET plain_hash=?,updated_at=? WHERE task_id=?`, plainHash, now(), taskID)
}

func (c *cloudClient) transferTaskByID(taskID string) (transferTask, error) {
	var task transferTask
	if c.runtime == nil || c.runtime.db == nil {
		return task, sql.ErrNoRows
	}
	err := c.runtime.db.QueryRow(`SELECT task_id,kind,file_id,revision_id,object_id,location_id,state,detail,plain_hash,size,created_at,updated_at
		FROM library_transfer_tasks WHERE task_id=?`, taskID).
		Scan(&task.TaskID, &task.Kind, &task.FileID, &task.RevisionID, &task.ObjectID, &task.LocationID, &task.State, &task.Detail, &task.PlainHash, &task.Size, &task.CreatedAt, &task.UpdatedAt)
	return task, err
}

func (c *cloudClient) listTransferTasks(kind string) []transferTask {
	tasks := []transferTask{}
	if c.runtime == nil || c.runtime.db == nil {
		return tasks
	}
	query := `SELECT task_id,kind,file_id,revision_id,object_id,location_id,state,detail,plain_hash,size,created_at,updated_at FROM library_transfer_tasks`
	args := []any{}
	if kind != "" {
		query += ` WHERE kind=?`
		args = append(args, kind)
	}
	query += ` ORDER BY created_at DESC LIMIT 200`
	rows, err := c.runtime.db.Query(query, args...)
	if err != nil {
		return tasks
	}
	defer rows.Close()
	for rows.Next() {
		var task transferTask
		if rows.Scan(&task.TaskID, &task.Kind, &task.FileID, &task.RevisionID, &task.ObjectID, &task.LocationID, &task.State, &task.Detail, &task.PlainHash, &task.Size, &task.CreatedAt, &task.UpdatedAt) == nil {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// encryptBlocks streams plaintext through AEAD, storing one immutable ciphertext
// block per chunk and returning the plaintext SHA-256. It is shared by the local
// path upload and the phone original-stream upload so both use identical crypto.
func (c *cloudClient) encryptBlocks(record *cloudRecord, manifest *cloudManifest, reader io.Reader, aead cipher.AEAD) (string, error) {
	plainHash := sha256.New()
	buffer := make([]byte, cloudChunkSize)
	for index := 0; ; index++ {
		n, readErr := io.ReadFull(reader, buffer)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return "", readErr
		}
		if n == 0 {
			break
		}
		plainHash.Write(buffer[:n])
		ciphertext := aead.Seal(nil, cloudNonce(index), buffer[:n], cloudAAD(*record, index, n))
		id := cloudHash(ciphertext)
		if err := c.runtime.disk.put("blocks", id, bytes.NewReader(ciphertext), cloudMaxBlobSize); err != nil {
			return "", err
		}
		manifest.Chunks = append(manifest.Chunks, cloudChunk{ID: id, Size: n})
		record.BlockIDs = append(record.BlockIDs, id)
		if readErr != nil {
			break
		}
	}
	return hex.EncodeToString(plainHash.Sum(nil)), nil
}

// verifyStoredBlocks reads every stored block back from disk, checks its
// ciphertext hash, AEAD-decrypts it and re-hashes the plaintext. This is the
// read-back recovery path required before content counts as a verifiable save;
// hashing only what was read during upload would not prove the blocks survive.
func (c *cloudClient) verifyStoredBlocks(record cloudRecord, manifest cloudManifest, aead cipher.AEAD) error {
	hash := sha256.New()
	for index, chunk := range manifest.Chunks {
		file, err := c.runtime.disk.get("blocks", chunk.ID)
		if err != nil {
			return fmt.Errorf("块丢失或不可读：%w", err)
		}
		data, err := io.ReadAll(io.LimitReader(file, cloudMaxBlobSize+1))
		file.Close()
		if err != nil {
			return err
		}
		if cloudHash(data) != chunk.ID {
			return errors.New("块密文校验失败")
		}
		plain, err := aead.Open(nil, cloudNonce(index), data, cloudAAD(record, index, chunk.Size))
		if err != nil {
			return errors.New("块解密失败，内容不可恢复")
		}
		if len(plain) != chunk.Size {
			return errors.New("块长度与清单不符")
		}
		hash.Write(plain)
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.PlainHash {
		return errors.New("读回明文摘要与原件不一致")
	}
	return nil
}

// uploadOriginal accepts a phone's original byte stream and runs the same
// encrypt → store → read-back-verify → commit pipeline as a local upload. The
// caller binds the stream to a stable fileId/revisionId and the expected
// plaintext hash; a mismatch blocks the commit rather than binding a manifest
// to different content.
func (c *cloudClient) uploadOriginal(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	if fileID == "" {
		writeError(w, 400, "缺少文件标识")
		return
	}
	query := r.URL.Query()
	revisionID := strings.TrimSpace(query.Get("revisionId"))
	name := strings.TrimSpace(query.Get("name"))
	expectedHash := strings.ToLower(strings.TrimSpace(query.Get("plainHash")))
	size, sizeErr := strconv.ParseInt(query.Get("size"), 10, 64)
	history := query.Get("history") == "1"
	if revisionID == "" || name == "" || sizeErr != nil || size < 0 || !validCloudHash(expectedHash) {
		writeError(w, 400, "上传需要 revisionId、name、size 与有效的 plainHash")
		return
	}
	modified := strings.TrimSpace(query.Get("modified"))
	if modified == "" {
		modified = now()
	}
	objectID := strings.TrimSpace(query.Get("objectId"))
	if objectID == "" {
		objectID = fileID
	}
	taskID := c.createTransferTask("upload", fileID, revisionID, objectID, "", size)
	c.jobsMu.Lock()
	if c.jobs[objectID] == "上传中" {
		c.jobsMu.Unlock()
		c.setTransferState(taskID, transferBlocked, "同一文件正在上传")
		writeError(w, 409, "文件正在上传")
		return
	}
	c.jobs[objectID] = "上传中"
	c.jobsMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
		defer cancel()
		err := c.uploadOriginalStream(ctx, taskID, fileID, revisionID, objectID, name, modified, expectedHash, size, history, r.Body)
		c.jobsMu.Lock()
		if err != nil {
			c.jobs[objectID] = "失败：" + err.Error()
		} else {
			c.jobs[objectID] = "已加入云层"
		}
		c.jobsMu.Unlock()
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "上传中", "taskId": taskID, "objectId": objectID})
}

func (c *cloudClient) uploadOriginalStream(ctx context.Context, taskID, fileID, revisionID, objectID, name, modified, expectedHash string, size int64, history bool, body io.Reader) error {
	fail := func(state, detail string) error {
		c.setTransferState(taskID, state, detail)
		return errors.New(detail)
	}
	c.setTransferState(taskID, transferReading, "")
	var dataKey [32]byte
	if _, err := rand.Read(dataKey[:]); err != nil {
		return fail(transferRetryableFailed, "无法生成内容密钥")
	}
	block, err := aes.NewCipher(dataKey[:])
	if err != nil {
		return fail(transferBlocked, "无法初始化加密")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fail(transferBlocked, "无法初始化加密")
	}
	record := cloudRecord{
		Format: 4, KeepHistory: history, LibraryID: c.libraryID(), ObjectID: objectID, RevisionID: revisionID,
		AuthorID: c.runtime.id, AuthorEpoch: c.authorEpoch(), CreatedAt: now(), BlockIDs: []string{},
	}
	manifest := cloudManifest{
		FileID: fileID, Name: name, Size: size, Modified: modified, History: history,
		SourceID: objectID, DataKey: hex.EncodeToString(dataKey[:]), Chunks: []cloudChunk{},
		Kind: fileKind(name), ContentSize: size,
	}
	c.setTransferState(taskID, transferUploading, "")
	plainHash, err := c.encryptBlocks(&record, &manifest, io.LimitReader(body, size+1), aead)
	if err != nil {
		return fail(transferRetryableFailed, "上传中断："+err.Error())
	}
	if int64(len(manifest.Chunks)) == 0 && size != 0 {
		return fail(transferRetryableFailed, "原件字节不足")
	}
	manifest.PlainHash = plainHash
	if plainHash != expectedHash {
		return fail(transferBlocked, "上传内容与原件摘要不一致，已拒绝绑定清单")
	}
	c.setTransferHash(taskID, plainHash)
	c.setTransferState(taskID, transferVerifying, "")
	if err := c.verifyStoredBlocks(record, manifest, aead); err != nil {
		return fail(transferRetryableFailed, "读回验证失败："+err.Error())
	}
	if err := c.commitUploadedContent(ctx, &record, &manifest, fileID, objectID, taskID); err != nil {
		return fail(transferRetryableFailed, err.Error())
	}
	c.setTransferState(taskID, transferCommitted, "")
	return nil
}

// commitUploadedContent persists verified content, seals the manifest, writes the
// catalog record and kicks off background replication. Shared by stream uploads.
func (c *cloudClient) commitUploadedContent(ctx context.Context, record *cloudRecord, manifest *cloudManifest, fileID, objectID, taskID string) error {
	if c.runtime.db != nil {
		if _, err := c.runtime.db.Exec(`INSERT INTO library_files(file_id,created_at,updated_at) VALUES(?,?,?) ON CONFLICT(file_id) DO NOTHING`, fileID, now(), now()); err != nil {
			return err
		}
		if _, err := c.runtime.db.Exec(`INSERT INTO library_revisions(revision_id,file_id,size,modified_at,created_at) VALUES(?,?,?,?,?) ON CONFLICT(revision_id) DO NOTHING`, record.RevisionID, fileID, manifest.Size, manifest.Modified, now()); err != nil {
			return err
		}
		if _, err := c.runtime.db.Exec(`INSERT INTO library_verified_content(revision_id,plain_hash,verified_at) VALUES(?,?,?)
			ON CONFLICT(revision_id) DO UPDATE SET plain_hash=excluded.plain_hash,verified_at=excluded.verified_at`, record.RevisionID, manifest.PlainHash, now()); err != nil {
			return err
		}
	}
	sealed, err := c.sealManifest(*record, *manifest)
	if err != nil {
		return err
	}
	*record = sealed
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(raw) > cloudMaxRecordSize {
		return errors.New("文件块清单过大")
	}
	if err := c.runtime.disk.put("catalog", cloudHash(raw), bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
		return err
	}
	c.replicateRecord(ctx, *record)
	return nil
}

func (c *cloudClient) transferTasksAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"tasks": c.listTransferTasks(r.URL.Query().Get("kind"))})
}

func (c *cloudClient) transferTaskAPI(w http.ResponseWriter, r *http.Request) {
	task, err := c.transferTaskByID(r.PathValue("taskId"))
	if err != nil {
		writeError(w, 404, "传输任务不存在")
		return
	}
	writeJSON(w, 200, task)
}
