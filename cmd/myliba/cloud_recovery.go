package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// A recovery attestation proves a node other than the releasing device still
// holds a working library key. Release is blocked when the only key holder is
// the device about to delete its original (docs/unified-library-design.md §9).
const recoveryAttestationWindow = 30 * 24 * time.Hour

func (c *cloudClient) recoveryProof(nodeID, day string) []byte {
	c.keyMu.RLock()
	key := c.key
	c.keyMu.RUnlock()
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("myliba-recovery-attest|" + c.libraryID() + "|" + nodeID + "|" + day))
	return mac.Sum(nil)
}

func (c *cloudClient) recordRecoveryAttestation(nodeID, proofHash string) error {
	if c.runtime == nil || c.runtime.db == nil {
		return errors.New("recovery attestation needs a library database")
	}
	_, err := c.runtime.db.Exec(`INSERT INTO library_recovery_attestations(node_id,library_id,proof_hash,verified_at)
		VALUES(?,?,?,?) ON CONFLICT(node_id) DO UPDATE SET library_id=excluded.library_id,proof_hash=excluded.proof_hash,verified_at=excluded.verified_at`,
		nodeID, c.libraryID(), proofHash, now())
	return err
}

// selfAttestRecovery performs a local AEAD round-trip under the library key and
// records that this node can recover content. It is the recoverability check the
// cloud node runs before it may authorize releasing another device's original.
func (c *cloudClient) selfAttestRecovery(w http.ResponseWriter, r *http.Request) {
	if !localCloudMutation(w, r) {
		return
	}
	c.keyMu.RLock()
	key := c.key
	c.keyMu.RUnlock()
	block, err := aes.NewCipher(key[:])
	if err != nil {
		writeError(w, 500, "恢复密钥不可用")
		return
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		writeError(w, 500, "恢复密钥不可用")
		return
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		writeError(w, 500, "无法生成校验随机数")
		return
	}
	probe := []byte("myliba-recovery-probe|" + c.libraryID())
	aad := []byte(c.libraryID())
	sealed := aead.Seal(nil, nonce, probe, aad)
	opened, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil || string(opened) != string(probe) {
		writeError(w, 500, "恢复密钥读回校验失败")
		return
	}
	day := time.Now().UTC().Format("2006-01-02")
	proofHash := hex.EncodeToString(c.recoveryProof(c.runtime.id, day))
	if err := c.recordRecoveryAttestation(c.runtime.id, proofHash); err != nil {
		writeError(w, 500, "无法保存可恢复性证明")
		return
	}
	writeJSON(w, 200, map[string]any{"nodeId": c.runtime.id, "libraryId": c.libraryID(), "verifiedAt": now(), "recoverable": true})
}

// attestRecovery lets a remote key holder (for example the phone) prove it holds
// the same library key. The proof is an HMAC bound to library, node and UTC day;
// the local node recomputes it with its own key and accepts today or yesterday.
func (c *cloudClient) attestRecovery(w http.ResponseWriter, r *http.Request) {
	var input struct {
		NodeID string `json:"nodeId"`
		Proof  string `json:"proof"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input); err != nil {
		writeError(w, 400, "无效的恢复证明")
		return
	}
	nodeID := strings.TrimSpace(input.NodeID)
	proof, err := hex.DecodeString(strings.TrimSpace(input.Proof))
	if err != nil || nodeID == "" || len(nodeID) > 128 || len(proof) != sha256.Size {
		writeError(w, 400, "恢复证明应为节点标识与 64 位十六进制 HMAC")
		return
	}
	nowUTC := time.Now().UTC()
	match := false
	for _, offset := range []time.Duration{0, -24 * time.Hour} {
		day := nowUTC.Add(offset).Format("2006-01-02")
		if hmac.Equal(proof, c.recoveryProof(nodeID, day)) {
			match = true
			break
		}
	}
	if !match {
		writeError(w, 403, "恢复证明与本文集密钥不符")
		return
	}
	if err := c.recordRecoveryAttestation(nodeID, hex.EncodeToString(proof)); err != nil {
		writeError(w, 500, "无法保存可恢复性证明")
		return
	}
	writeJSON(w, 200, map[string]any{"nodeId": nodeID, "libraryId": c.libraryID(), "verifiedAt": now(), "recoverable": true})
}

// recoveryHolder reports a recent attestation from a node other than the
// excluded (releasing) device. The returned node ID is empty when no independent
// key holder has proven recoverability.
func (c *cloudClient) recoveryHolder(excludeNodeID string) (string, bool) {
	if c.runtime == nil || c.runtime.db == nil {
		return "", false
	}
	cutoff := time.Now().UTC().Add(-recoveryAttestationWindow).Format(time.RFC3339Nano)
	rows, err := c.runtime.db.Query(`SELECT node_id,verified_at FROM library_recovery_attestations WHERE library_id=? AND node_id<>? ORDER BY verified_at DESC`, c.libraryID(), excludeNodeID)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID, verifiedAt string
		if rows.Scan(&nodeID, &verifiedAt) != nil {
			continue
		}
		stamp, parseErr := time.Parse(time.RFC3339Nano, verifiedAt)
		if parseErr != nil || stamp.Format(time.RFC3339Nano) < cutoff {
			continue
		}
		return nodeID, true
	}
	return "", false
}

func (c *cloudClient) recoveryStatusAPI(w http.ResponseWriter, r *http.Request) {
	holder, ok := c.recoveryHolder(c.runtime.id)
	writeJSON(w, 200, map[string]any{"libraryId": c.libraryID(), "selfNodeId": c.runtime.id, "independentHolder": holder, "recoverable": ok})
}
