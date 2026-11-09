package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	releaseCredentialTTL = 10 * time.Minute
	releasePolicy        = "two-independent-domains"
)

// A release credential authorizes deleting one specific local location of one
// immutable revision. It is bound to location, version, policy, plaintext hash
// and issue time, and is short lived; the release endpoint re-verifies the key
// safety state before honoring it so an expired or stale check cannot delete a
// changed original (docs/unified-library-design.md §9, T19).
type releaseCredential struct {
	CredentialID string `json:"id"`
	LibraryID    string `json:"l"`
	FileID       string `json:"f"`
	RevisionID   string `json:"v"`
	LocationID   string `json:"loc"`
	NodeID       string `json:"n"`
	Policy       string `json:"policy"`
	PlainHash    string `json:"hash"`
	IssuedAt     int64  `json:"iat"`
	Expires      int64  `json:"exp"`
}

func (c *cloudClient) signReleaseCredential(cred releaseCredential) (string, error) {
	cred.LibraryID = c.libraryID()
	cred.CredentialID = newID()
	cred.IssuedAt = time.Now().Unix()
	cred.Expires = time.Now().Add(releaseCredentialTTL).Unix()
	payload, err := json.Marshal(cred)
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

func (c *cloudClient) verifyReleaseCredential(raw, fileID, locationID string) (releaseCredential, error) {
	var cred releaseCredential
	parts := strings.Split(raw, ".")
	if len(parts) != 2 || len(raw) > 4096 {
		return cred, errors.New("缺少释放凭据")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cred, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cred, err
	}
	c.keyMu.RLock()
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	valid := hmac.Equal(signature, mac.Sum(nil))
	c.keyMu.RUnlock()
	if !valid || json.Unmarshal(payload, &cred) != nil {
		return releaseCredential{}, errors.New("释放凭据签名无效")
	}
	nowUnix := time.Now().Unix()
	if cred.LibraryID != c.libraryID() || cred.FileID != fileID || cred.LocationID != locationID ||
		cred.Policy != releasePolicy || cred.CredentialID == "" || cred.RevisionID == "" || cred.NodeID == "" ||
		!validCloudHash(cred.PlainHash) || cred.Expires <= nowUnix || cred.Expires > nowUnix+int64(releaseCredentialTTL.Seconds()) || cred.IssuedAt > nowUnix {
		return releaseCredential{}, errors.New("释放凭据无效、绑定不符或已过期")
	}
	return cred, nil
}

func (c *cloudClient) recordForRevision(fileID, revisionID string) (cloudRecord, cloudManifest, error) {
	records, err := c.contentRecords()
	if err != nil {
		return cloudRecord{}, cloudManifest{}, err
	}
	for _, record := range records {
		if record.RevisionID != revisionID {
			continue
		}
		manifest, err := c.openManifest(record)
		if err != nil {
			continue
		}
		matchesFile := manifest.FileID == fileID || (manifest.FileID == "" && record.ObjectID == fileID)
		if matchesFile {
			return record, manifest, nil
		}
	}
	return cloudRecord{}, cloudManifest{}, errors.New("云层尚无此版本的可验证原件")
}

// domainRepresentatives picks one complete-copy node per independent fault
// domain so persistence can be confirmed against each domain exactly once.
func (c *cloudClient) domainRepresentatives(record cloudRecord) map[string]cloudNode {
	complete := c.completeCopyNodes(record)
	domains := c.runtime.nodeFaultDomains()
	peers := map[string]cloudNode{}
	for _, peer := range c.runtime.activePeers() {
		peers[peer.ID] = peer
	}
	representatives := map[string]cloudNode{}
	for nodeID := range complete {
		domain := domains[nodeID]
		if domain == "" {
			continue
		}
		if _, exists := representatives[domain]; exists {
			continue
		}
		if nodeID == c.runtime.id {
			representatives[domain] = cloudNode{ID: nodeID, FaultDomain: domain}
			continue
		}
		if peer, ok := peers[nodeID]; ok {
			representatives[domain] = peer
		}
	}
	return representatives
}

// verifyDomainPersistence confirms every block is addressable and ciphertext
// hash valid on each independent domain. The local domain additionally carries a
// full plaintext read-back (library_verified_content); remote domains get block
// persistence and integrity confirmation here.
func (c *cloudClient) verifyDomainPersistence(ctx context.Context, record cloudRecord) error {
	representatives := c.domainRepresentatives(record)
	_, token, err := c.runtime.credentials()
	if err != nil {
		return err
	}
	for domain, node := range representatives {
		for _, blockID := range record.BlockIDs {
			if node.ID == c.runtime.id {
				file, getErr := c.runtime.disk.get("blocks", blockID)
				if getErr != nil {
					return errors.New("故障域 " + domain + " 块丢失")
				}
				data, readErr := io.ReadAll(io.LimitReader(file, cloudMaxBlobSize+1))
				file.Close()
				if readErr != nil || cloudHash(data) != blockID {
					return errors.New("故障域 " + domain + " 块校验失败")
				}
				continue
			}
			if node.Endpoint == "" {
				return errors.New("故障域 " + domain + " 节点不可达")
			}
			if _, getErr := c.runtime.get(ctx, node.Endpoint, "blocks", blockID, token, cloudMaxBlobSize); getErr != nil {
				return errors.New("故障域 " + domain + " 块持久性校验失败")
			}
		}
	}
	return nil
}

type releaseEvaluation struct {
	Eligible   bool     `json:"eligible"`
	Reason     string   `json:"reason"`
	FileID     string   `json:"fileId"`
	RevisionID string   `json:"revisionId"`
	LocationID string   `json:"locationId"`
	NodeID     string   `json:"nodeId"`
	PlainHash  string   `json:"plainHash"`
	Domains    []string `json:"domains"`
	Holder     string   `json:"recoveryHolder"`
	record     cloudRecord
	manifest   cloudManifest
}

// evaluateRelease is the shared safety gate used by both release-check and the
// final release call. It never deletes anything; it only decides eligibility.
func (c *cloudClient) evaluateRelease(ctx context.Context, fileID, locationID, revisionID string) releaseEvaluation {
	eval := releaseEvaluation{FileID: fileID, LocationID: locationID, RevisionID: revisionID, Domains: []string{}}
	if c.runtime == nil || c.runtime.db == nil {
		eval.Reason = "资源库数据库不可用"
		return eval
	}
	var locNode, locRevision, locState string
	err := c.runtime.db.QueryRow(`SELECT node_id,COALESCE(revision_id,''),state FROM library_locations WHERE location_id=? AND file_id=?`, locationID, fileID).Scan(&locNode, &locRevision, &locState)
	if err != nil {
		eval.Reason = "本机位置不存在或不属于该文件"
		return eval
	}
	eval.NodeID = locNode
	if revisionID != "" && locRevision != revisionID {
		eval.Reason = "位置绑定的版本与请求不符"
		return eval
	}
	revisionID = locRevision
	eval.RevisionID = revisionID
	if locState != "available" {
		eval.Reason = "该位置已不可用，无需释放"
		return eval
	}
	record, manifest, err := c.recordForRevision(fileID, revisionID)
	if err != nil {
		eval.Reason = err.Error()
		return eval
	}
	eval.record, eval.manifest = record, manifest
	var plainHash string
	if err := c.runtime.db.QueryRow(`SELECT plain_hash FROM library_verified_content WHERE revision_id=?`, revisionID).Scan(&plainHash); err != nil {
		eval.Reason = "原件尚未完成读回验证，不能释放"
		return eval
	}
	if plainHash != manifest.PlainHash || !validCloudHash(plainHash) {
		eval.Reason = "读回摘要与清单不一致"
		return eval
	}
	eval.PlainHash = plainHash
	domains, _ := c.replicaDomains(record)
	eval.Domains = domains
	if len(domains) < 2 {
		eval.Reason = "独立故障域的可验证保存不足两份（同一磁盘的多网关只算一份）"
		return eval
	}
	if err := c.verifyDomainPersistence(ctx, record); err != nil {
		eval.Reason = err.Error()
		return eval
	}
	holder, ok := c.recoveryHolder(locNode)
	if !ok {
		eval.Reason = "恢复密钥尚无独立于本机的可恢复性证明"
		return eval
	}
	eval.Holder = holder
	eval.Eligible = true
	eval.Reason = "满足两份独立故障域保存与恢复密钥证明，可安全释放"
	return eval
}

func (c *cloudClient) releaseCheck(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	var input struct {
		LocationID string `json:"locationId"`
		RevisionID string `json:"revisionId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "无效的释放检查请求")
		return
	}
	if fileID == "" || strings.TrimSpace(input.LocationID) == "" {
		writeError(w, 400, "释放检查需要 fileId 与 locationId")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	eval := c.evaluateRelease(ctx, fileID, strings.TrimSpace(input.LocationID), strings.TrimSpace(input.RevisionID))
	response := map[string]any{
		"eligible": eval.Eligible, "reason": eval.Reason, "fileId": eval.FileID, "revisionId": eval.RevisionID,
		"locationId": eval.LocationID, "nodeId": eval.NodeID, "plainHash": eval.PlainHash,
		"domains": eval.Domains, "recoveryHolder": eval.Holder, "policy": releasePolicy,
	}
	if eval.Eligible {
		token, err := c.signReleaseCredential(releaseCredential{
			FileID: fileID, RevisionID: eval.RevisionID, LocationID: eval.LocationID, NodeID: eval.NodeID,
			Policy: releasePolicy, PlainHash: eval.PlainHash,
		})
		if err != nil {
			writeError(w, 500, "无法签发释放凭据")
			return
		}
		response["credential"] = token
		response["expires"] = time.Now().Add(releaseCredentialTTL).Unix()
	}
	writeJSON(w, 200, response)
}

func (c *cloudClient) releaseOriginal(w http.ResponseWriter, r *http.Request) {
	fileID := r.PathValue("fileId")
	var input struct {
		LocationID string `json:"locationId"`
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&input); err != nil {
		writeError(w, 400, "无效的释放请求")
		return
	}
	locationID := strings.TrimSpace(input.LocationID)
	if fileID == "" || locationID == "" {
		writeError(w, 400, "释放需要 fileId 与 locationId")
		return
	}
	cred, err := c.verifyReleaseCredential(input.Credential, fileID, locationID)
	if err != nil {
		writeError(w, 403, err.Error()+"，请重新执行释放检查")
		return
	}
	// Idempotent finish: a crashed client may retry after the location was already
	// removed. The release log makes the second call a no-op success.
	var logged string
	if c.runtime.db != nil {
		_ = c.runtime.db.QueryRow(`SELECT credential_id FROM library_release_log WHERE location_id=?`, locationID).Scan(&logged)
	}
	if logged != "" {
		writeJSON(w, 200, map[string]any{"released": true, "locationId": locationID, "idempotent": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	eval := c.evaluateRelease(ctx, fileID, locationID, cred.RevisionID)
	if !eval.Eligible {
		writeError(w, 409, "释放前复核未通过："+eval.Reason)
		return
	}
	if eval.PlainHash != cred.PlainHash || eval.RevisionID != cred.RevisionID {
		writeError(w, 409, "原件版本或摘要已变化，请重新收录后再释放")
		return
	}
	taskID := c.createTransferTask("release", fileID, cred.RevisionID, "", locationID, eval.manifest.Size)
	c.setTransferState(taskID, "deletingLocal", "")
	if _, err := c.runtime.db.Exec(`UPDATE library_locations SET state='missing',updated_at=? WHERE location_id=? AND file_id=?`, now(), locationID, fileID); err != nil {
		c.setTransferState(taskID, transferRetryableFailed, err.Error())
		writeError(w, 500, "无法移除本机位置")
		return
	}
	if _, err := c.runtime.db.Exec(`INSERT INTO library_release_log(location_id,file_id,revision_id,credential_id,released_at) VALUES(?,?,?,?,?)
		ON CONFLICT(location_id) DO NOTHING`, locationID, fileID, cred.RevisionID, cred.CredentialID, now()); err != nil {
		c.setTransferState(taskID, transferRetryableFailed, err.Error())
		writeError(w, 500, "无法记录释放结果")
		return
	}
	c.setTransferState(taskID, transferCommitted, "")
	writeJSON(w, 200, map[string]any{"released": true, "locationId": locationID, "revisionId": cred.RevisionID, "taskId": taskID})
}
