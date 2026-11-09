package main

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
)

func (c *cloudClient) librarySnapshotStatus(w http.ResponseWriter, _ *http.Request) {
	if c.runtime.db == nil {
		writeJSON(w, 200, map[string]any{"items": []any{}})
		return
	}
	rows, err := c.runtime.db.Query(`SELECT f.author_id,f.last_counter,COALESCE(p.frontier,0),COALESCE(p.record_hash,''),COALESCE(p.published_at,'') FROM library_operation_frontiers f LEFT JOIN library_snapshot_publication p ON p.author_id=f.author_id ORDER BY f.author_id`)
	if err != nil {
		writeError(w, 500, "无法读取快照状态")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var author, hash, stamp string
		var applied, covered int64
		if err = rows.Scan(&author, &applied, &covered, &hash, &stamp); err != nil {
			writeError(w, 500, "无法读取快照状态")
			return
		}
		items = append(items, map[string]any{"authorId": author, "appliedFrontier": applied, "snapshotFrontier": covered, "recordHash": hash, "publishedAt": stamp, "locallyPersisted": hash != "" && c.runtime.disk.valid("catalog", hash)})
	}
	if rows.Err() != nil {
		writeError(w, 500, "无法读取快照状态")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

type librarySnapshotEnvelope struct {
	SchemaVersion int    `json:"schemaVersion"`
	AuthorID      string `json:"authorId"`
	FromCounter   int64  `json:"fromCounter,omitempty"`
	Frontier      int64  `json:"frontier"`
	Checksum      string `json:"checksum"`
	OperationsB64 string `json:"operationsB64"`
}

func encodeLibrarySnapshot(author string, operations []libraryOperationEnvelope) (librarySnapshotEnvelope, error) {
	if author == "" || len(operations) == 0 {
		return librarySnapshotEnvelope{}, errors.New("empty snapshot")
	}
	start := operations[0].Counter
	if start < 1 {
		return librarySnapshotEnvelope{}, errors.New("invalid snapshot start")
	}
	for at, op := range operations {
		if op.AuthorID != author || op.Counter != start+int64(at) || op.OpID == "" || !knownLibraryOperationKind(op.Kind) {
			return librarySnapshotEnvelope{}, errors.New("snapshot contains a causal gap")
		}
	}
	raw, err := json.Marshal(operations)
	if err != nil {
		return librarySnapshotEnvelope{}, err
	}
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	if _, err = zip.Write(raw); err != nil {
		return librarySnapshotEnvelope{}, err
	}
	if err = zip.Close(); err != nil {
		return librarySnapshotEnvelope{}, err
	}
	from := start
	if from == 1 {
		from = 0
	}
	return librarySnapshotEnvelope{SchemaVersion: 1, AuthorID: author, FromCounter: from, Frontier: operations[len(operations)-1].Counter, Checksum: cloudHash(raw), OperationsB64: base64.StdEncoding.EncodeToString(compressed.Bytes())}, nil
}

func decodeLibrarySnapshot(snapshot librarySnapshotEnvelope) ([]libraryOperationEnvelope, error) {
	if snapshot.SchemaVersion != 1 {
		return nil, errors.New("不支持的快照 schema，请升级客户端")
	}
	start := snapshot.FromCounter
	if start == 0 {
		start = 1
	}
	if snapshot.AuthorID == "" || start < 1 || snapshot.Frontier < start || snapshot.Frontier > 100000 || !validCloudHash(snapshot.Checksum) || len(snapshot.OperationsB64) > cloudMaxRecordSize {
		return nil, errors.New("invalid snapshot header")
	}
	compressed, err := base64.StdEncoding.DecodeString(snapshot.OperationsB64)
	if err != nil {
		return nil, err
	}
	zip, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer zip.Close()
	raw, err := io.ReadAll(io.LimitReader(zip, 64<<20+1))
	if err != nil || len(raw) > 64<<20 || cloudHash(raw) != snapshot.Checksum {
		return nil, errors.New("snapshot checksum mismatch")
	}
	var operations []libraryOperationEnvelope
	if err = json.Unmarshal(raw, &operations); err != nil {
		return nil, err
	}
	if int64(len(operations)) != snapshot.Frontier-start+1 {
		return nil, errors.New("snapshot frontier mismatch")
	}
	for at, op := range operations {
		if op.AuthorID != snapshot.AuthorID || op.Counter != start+int64(at) || op.OpID == "" {
			return nil, errors.New("snapshot contains a causal gap")
		}
	}
	return operations, nil
}

// Snapshots are immutable encrypted catalog records. Their contents remain
// replayable from counter one, so a fresh receiver can rebuild state even
// after individual operation records are compacted. Local outbox rows are not
// replaced by an incoming snapshot.
func (c *cloudClient) publishOperationSnapshots() error {
	if c == nil || c.runtime == nil || c.runtime.db == nil {
		return nil
	}
	records, err := c.validRecords()
	if err != nil {
		return err
	}
	byAuthor := map[string]map[int64]libraryOperationEnvelope{}
	for _, record := range records {
		manifest, openErr := c.openManifest(record)
		if openErr != nil {
			continue
		}
		if manifest.Snapshot != nil {
			operations, decodeErr := decodeLibrarySnapshot(*manifest.Snapshot)
			if decodeErr != nil {
				continue
			}
			for _, op := range operations {
				if byAuthor[op.AuthorID] == nil {
					byAuthor[op.AuthorID] = map[int64]libraryOperationEnvelope{}
				}
				if byAuthor[op.AuthorID][op.Counter].OpID == "" {
					byAuthor[op.AuthorID][op.Counter] = op
				}
			}
		}
		if manifest.Operation != nil {
			op := *manifest.Operation
			if byAuthor[op.AuthorID] == nil {
				byAuthor[op.AuthorID] = map[int64]libraryOperationEnvelope{}
			}
			byAuthor[op.AuthorID][op.Counter] = op
		}
	}
	authors := make([]string, 0, len(byAuthor))
	for author := range byAuthor {
		authors = append(authors, author)
	}
	sort.Strings(authors)
	for _, author := range authors {
		var frontier, published int64
		if err := c.runtime.db.QueryRow(`SELECT last_counter FROM library_operation_frontiers WHERE author_id=?`, author).Scan(&frontier); err != nil {
			continue
		}
		var latestHash string
		_ = c.runtime.db.QueryRow(`SELECT frontier,record_hash FROM library_snapshot_publication WHERE author_id=?`, author).Scan(&published, &latestHash)
		if published > 0 && !c.runtime.disk.valid("catalog", latestHash) {
			published = 0
		}
		if frontier < 1 || frontier <= published || frontier > 100000 {
			continue
		}
		operations := make([]libraryOperationEnvelope, 0, frontier)
		for counter := int64(1); counter <= frontier; counter++ {
			op := byAuthor[author][counter]
			if op.OpID == "" {
				break
			}
			operations = append(operations, op)
		}
		if int64(len(operations)) != frontier {
			continue
		}
		for start := published + 1; start <= frontier; {
			end := min(frontier, start+99)
			var raw []byte
			for {
				snapshot, encodeErr := encodeLibrarySnapshot(author, operations[start-1:end])
				if encodeErr != nil {
					return encodeErr
				}
				record := cloudRecord{Format: 4, LibraryID: c.libraryID(), ObjectID: "library-snapshot/" + author, RevisionID: newID(), AuthorID: c.runtime.id, AuthorEpoch: c.authorEpoch(), CreatedAt: now(), BlockIDs: []string{}}
				record, err = c.sealManifest(record, cloudManifest{Snapshot: &snapshot})
				if err != nil {
					return err
				}
				raw, err = json.Marshal(record)
				if err != nil {
					return err
				}
				if len(raw) <= cloudMaxRecordSize {
					break
				}
				if end == start {
					return fmt.Errorf("snapshot operation %s/%d exceeds catalog limit", author, start)
				}
				end = start + (end-start)/2
			}
			hash := cloudHash(raw)
			if err = c.runtime.disk.put("catalog", hash, bytes.NewReader(raw), cloudMaxRecordSize); err != nil {
				return fmt.Errorf("snapshot %s: %w", author, err)
			}
			if _, err = c.runtime.db.Exec(`INSERT INTO library_snapshot_publication(author_id,frontier,record_hash,published_at) VALUES(?,?,?,?) ON CONFLICT(author_id) DO UPDATE SET frontier=excluded.frontier,record_hash=excluded.record_hash,published_at=excluded.published_at`, author, end, hash, now()); err != nil {
				return err
			}
			start = end + 1
		}
	}
	return nil
}
