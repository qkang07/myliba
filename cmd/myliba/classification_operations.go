package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Classification operations use a dedicated author identity so their ordered
// replay does not share counters with unrelated library operations.
const classificationReplaySuffix = "\x00replay"

var (
	errClassificationPending    = errors.New("classification predecessors missing")
	errClassificationQuarantine = errors.New("classification operation quarantined")
)

type classificationOpHeader struct {
	SchemaVersion int              `json:"schemaVersion"`
	Author        string           `json:"author"`
	Counter       int64            `json:"counter"`
	Clock         int64            `json:"clock"`
	BaselineID    string           `json:"baselineId"`
	Context       map[string]int64 `json:"context"`
}

type classificationMembershipAdd struct {
	IndexID string `json:"indexId"`
	Token   string `json:"token"`
	Origin  string `json:"origin"`
}

type classificationObservedFrontier struct {
	Author  string `json:"author"`
	Counter int64  `json:"counter"`
}

type classificationMembershipRemove struct {
	IndexID          string                          `json:"indexId"`
	ObservedTokens   []string                        `json:"observedTokens,omitempty"`
	ObservedFrontier *classificationObservedFrontier `json:"observedFrontier,omitempty"`
}

type classificationFileMembership struct {
	FileID  string                           `json:"fileId"`
	Adds    []classificationMembershipAdd    `json:"adds,omitempty"`
	Removes []classificationMembershipRemove `json:"removes,omitempty"`
}

type classificationOpPayload struct {
	IndexID      string                         `json:"indexId,omitempty"`
	ParentID     *string                        `json:"parentId,omitempty"`
	Name         string                         `json:"name,omitempty"`
	Origin       string                         `json:"origin,omitempty"`
	BaseVersion  string                         `json:"baseVersion,omitempty"`
	Patch        map[string]json.RawMessage     `json:"patch,omitempty"`
	Files        []classificationFileMembership `json:"files,omitempty"`
	ConflictOpID string                         `json:"conflictOpId,omitempty"`
}

func migrateClassificationOperationsSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS classification_operations (
	library_id TEXT NOT NULL,
	op_id TEXT NOT NULL,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	clock INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	context_json TEXT NOT NULL,
	baseline_id TEXT NOT NULL DEFAULT '',
	origin TEXT NOT NULL CHECK(origin IN ('local','remote')),
	received_at TEXT NOT NULL,
	PRIMARY KEY(library_id,op_id),
	UNIQUE(library_id,author_id,counter)
);
CREATE TABLE IF NOT EXISTS classification_author_clocks (
	library_id TEXT NOT NULL,
	author_id TEXT NOT NULL,
	last_clock INTEGER NOT NULL,
	last_counter INTEGER NOT NULL,
	PRIMARY KEY(library_id,author_id)
);
CREATE TABLE IF NOT EXISTS classification_conflicts (
	library_id TEXT NOT NULL,
	op_id TEXT NOT NULL,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	kind TEXT NOT NULL,
	reason TEXT NOT NULL,
	detail_json TEXT NOT NULL DEFAULT '',
	resolved INTEGER NOT NULL DEFAULT 0,
	seen_at TEXT NOT NULL,
	PRIMARY KEY(library_id,op_id)
);
CREATE TABLE IF NOT EXISTS classification_library_state (
	library_id TEXT PRIMARY KEY,
	mode TEXT NOT NULL DEFAULT 'shadow' CHECK(mode IN ('shadow','active')),
	baseline_id TEXT NOT NULL DEFAULT '',
	activated_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS library_legacy_held_operations (
	op_id TEXT PRIMARY KEY,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	seen_at TEXT NOT NULL
);
`)
	return err
}

func classificationMode(db *sql.DB, libraryID string) string {
	var mode string
	if err := db.QueryRow(`SELECT mode FROM classification_library_state WHERE library_id=?`, libraryID).Scan(&mode); err != nil {
		return "shadow"
	}
	return mode
}

// initializeClassificationLibrary creates the format-5 classification state
// for a new library. MyLibA has not shipped yet, so a new runtime starts from
// an empty classification projection instead of importing folders, tags, or
// any other development-era classification state.
func initializeClassificationLibrary(db *sql.DB, libraryID string) error {
	if db == nil || libraryID == "" {
		return errors.New("classification library identity unavailable")
	}
	_, err := db.Exec(`INSERT INTO classification_library_state(library_id,mode,baseline_id,activated_at)
		VALUES(?,'active','',?) ON CONFLICT(library_id) DO NOTHING`, libraryID, now())
	return err
}

func classificationModeTx(tx *sql.Tx) string {
	var mode string
	if err := tx.QueryRow(`SELECT mode FROM classification_library_state LIMIT 1`).Scan(&mode); err != nil {
		return "shadow"
	}
	return mode
}

// rejectIfClassificationActive guards every legacy classification write entry
// (§9.2 item 4). After the switch, grouping changes must go through index
// operations so all devices replay one deterministic projection.
func (s *server) rejectIfClassificationActive(w http.ResponseWriter) bool {
	if s.cloud == nil {
		return false
	}
	if classificationMode(s.db, s.cloud.libraryID()) == "active" {
		writeError(w, http.StatusConflict, "classificationUpgradeRequired")
		return true
	}
	return false
}

func classificationOperationAuthor(tx *sql.Tx) (string, error) {
	var author string
	err := tx.QueryRow(`SELECT value FROM library_operation_identity WHERE key='classification-author'`).Scan(&author)
	if err == nil {
		return author, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	author = "index-" + newID()
	_, err = tx.Exec(`INSERT INTO library_operation_identity(key,value) VALUES('classification-author',?)`, author)
	return author, err
}

func nextOperationCounter(tx *sql.Tx, author string) (int64, error) {
	var counter int64
	err := tx.QueryRow(`SELECT next_counter FROM library_operation_authors WHERE author_id=?`, author).Scan(&counter)
	if err == sql.ErrNoRows {
		if _, err = tx.Exec(`INSERT INTO library_operation_authors(author_id,next_counter) VALUES(?,2)`, author); err != nil {
			return 0, err
		}
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE library_operation_authors SET next_counter=? WHERE author_id=?`, counter+1, author); err != nil {
		return 0, err
	}
	return counter, nil
}

func classificationAuthorClock(tx *sql.Tx, libraryID, author string) (int64, error) {
	var clock int64
	err := tx.QueryRow(`SELECT last_clock FROM classification_author_clocks WHERE library_id=? AND author_id=?`, libraryID, author).Scan(&clock)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return clock, err
}

func bumpClassificationAuthorClock(tx *sql.Tx, libraryID, author string, clock, counter int64) error {
	_, err := tx.Exec(`INSERT INTO classification_author_clocks(library_id,author_id,last_clock,last_counter) VALUES(?,?,?,?)
		ON CONFLICT(library_id,author_id) DO UPDATE SET last_clock=MAX(excluded.last_clock,last_clock),last_counter=MAX(excluded.last_counter,last_counter)`, libraryID, author, clock, counter)
	return err
}

// recordClassificationOperation writes the domain operation into the shared
// outbox and the persistent replay log in the caller's transaction. The logical
// clock covers every observed predecessor plus this author's own prior work,
// including operations the network has not read back yet.
func recordClassificationOperation(tx *sql.Tx, libraryID, kind, opID string, domain map[string]any) (libraryOperationEnvelope, error) {
	author, err := classificationOperationAuthor(tx)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	counter, err := nextOperationCounter(tx, author)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	context, err := operationContext(tx)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	clock, err := classificationAuthorClock(tx, libraryID, author)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	for predecessor := range context {
		observed, clockErr := classificationAuthorClock(tx, libraryID, predecessor)
		if clockErr != nil {
			return libraryOperationEnvelope{}, clockErr
		}
		if observed > clock {
			clock = observed
		}
	}
	clock++
	domain["schemaVersion"] = 2
	domain["author"] = author
	domain["counter"] = counter
	domain["clock"] = clock
	domain["baselineId"] = ""
	domain["context"] = context
	raw, err := json.Marshal(domain)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	if _, err = tx.Exec(`INSERT INTO library_operations(op_id,author_id,counter,kind,payload_json,created_at,state) VALUES(?,?,?,?,?,?, 'pending')`, opID, author, counter, kind, string(raw), now()); err != nil {
		return libraryOperationEnvelope{}, err
	}
	contextJSON, err := json.Marshal(context)
	if err != nil {
		return libraryOperationEnvelope{}, err
	}
	if _, err = tx.Exec(`INSERT INTO classification_operations(library_id,op_id,author_id,counter,clock,kind,payload_json,context_json,baseline_id,origin,received_at) VALUES(?,?,?,?,?,?,?,?,?,'local',?)`, libraryID, opID, author, counter, clock, kind, string(raw), string(contextJSON), "", now()); err != nil {
		return libraryOperationEnvelope{}, err
	}
	if err = bumpClassificationAuthorClock(tx, libraryID, author, clock, counter); err != nil {
		return libraryOperationEnvelope{}, err
	}
	return libraryOperationEnvelope{OpID: opID, AuthorID: author, Counter: counter, Kind: kind, Payload: raw}, nil
}

func knownClassificationOperationKind(kind string) bool {
	switch kind {
	case "index.create", "index.update", "index.delete", "index.memberships", "index.resolve":
		return true
	default:
		return false
	}
}

func recordClassificationConflict(tx *sql.Tx, libraryID string, op libraryOperationEnvelope, reason string, detail any) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO classification_conflicts(library_id,op_id,author_id,counter,kind,reason,detail_json,seen_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(library_id,op_id) DO NOTHING`, libraryID, op.OpID, op.AuthorID, op.Counter, op.Kind, reason, string(raw), now())
	return err
}

// replayClassificationTx deterministically rebuilds the projection from the
// format-5 operation log and swaps it into the live tables atomically.
func replayClassificationTx(tx *sql.Tx, libraryID, localNodeID string) error {
	if libraryID == "" || localNodeID == "" {
		return errors.New("classification replay identity unavailable")
	}
	if _, err := tx.Exec(`PRAGMA defer_foreign_keys=ON`); err != nil {
		return err
	}
	replayLibrary := libraryID + classificationReplaySuffix
	for _, table := range []string{"classification_ancestors", "classification_memberships", "classification_removes", "classification_adds", "classification_nodes"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE library_id=?`, replayLibrary); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT op_id,author_id,counter,kind,payload_json FROM classification_operations WHERE library_id=? ORDER BY clock,author_id,counter,op_id`, libraryID)
	if err != nil {
		return err
	}
	operations := []libraryOperationEnvelope{}
	for rows.Next() {
		var op libraryOperationEnvelope
		var payload string
		if err = rows.Scan(&op.OpID, &op.AuthorID, &op.Counter, &op.Kind, &payload); err != nil {
			rows.Close()
			return err
		}
		op.Payload = json.RawMessage(payload)
		operations = append(operations, op)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, op := range operations {
		if err = applyClassificationOp(tx, libraryID, replayLibrary, op); err != nil {
			return err
		}
	}
	if err = rebuildClassificationMemberships(tx, replayLibrary); err != nil {
		return err
	}
	if err = rebuildClassificationAncestors(tx, replayLibrary); err != nil {
		return err
	}
	return swapClassificationProjection(tx, libraryID, replayLibrary)
}

func swapClassificationProjection(tx *sql.Tx, libraryID, replayLibrary string) error {
	// Delete and copy children before parents: ON DELETE CASCADE fires even
	// with deferred foreign keys, so removing nodes first would wipe the rows
	// that still need to be copied.
	tables := []string{"classification_ancestors", "classification_memberships", "classification_removes", "classification_adds", "classification_nodes"}
	for _, table := range tables {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE library_id=?`, libraryID); err != nil {
			return err
		}
	}
	swaps := []struct{ table, columns string }{
		{"classification_nodes", "index_id,parent_id,name,name_key,origin,deleted,name_op_id,parent_op_id,created_at,updated_at"},
		{"classification_adds", "file_id,index_id,add_token,origin,operation_id,created_at"},
		{"classification_removes", "file_id,index_id,add_token,operation_id,removed_at"},
		{"classification_memberships", "file_id,index_id,has_manual,has_automatic,updated_at"},
		{"classification_ancestors", "ancestor_id,descendant_id,depth"},
	}
	for _, swap := range swaps {
		if _, err := tx.Exec(`INSERT INTO `+swap.table+`(library_id,`+swap.columns+`) SELECT ?,`+swap.columns+` FROM `+swap.table+` WHERE library_id=?`, libraryID, replayLibrary); err != nil {
			return err
		}
	}
	for _, table := range tables {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE library_id=?`, replayLibrary); err != nil {
			return err
		}
	}
	return nil
}

func applyClassificationOp(tx *sql.Tx, libraryID, replayLibrary string, op libraryOperationEnvelope) error {
	var payload classificationOpPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		return recordClassificationConflict(tx, libraryID, op, "invalidPayload", nil)
	}
	var header classificationOpHeader
	if err := json.Unmarshal(op.Payload, &header); err != nil {
		return recordClassificationConflict(tx, libraryID, op, "invalidPayload", nil)
	}
	switch op.Kind {
	case "index.create":
		return applyClassificationCreate(tx, libraryID, replayLibrary, op, payload)
	case "index.update", "index.resolve":
		return applyClassificationUpdate(tx, libraryID, replayLibrary, op, payload, header.Context, op.Kind == "index.resolve")
	case "index.delete":
		return applyClassificationDelete(tx, libraryID, replayLibrary, op, payload)
	case "index.memberships":
		return applyClassificationMemberships(tx, libraryID, replayLibrary, op, payload)
	default:
		return recordClassificationConflict(tx, libraryID, op, "unsupportedKind", nil)
	}
}

func classificationReplayNode(tx *sql.Tx, replayLibrary, indexID string) (parent sql.NullString, name, nameOp, parentOp string, deleted int, err error) {
	err = tx.QueryRow(`SELECT parent_id,name,name_op_id,parent_op_id,deleted FROM classification_nodes WHERE library_id=? AND index_id=?`, replayLibrary, indexID).Scan(&parent, &name, &nameOp, &parentOp, &deleted)
	return
}

func applyClassificationCreate(tx *sql.Tx, libraryID, replayLibrary string, op libraryOperationEnvelope, payload classificationOpPayload) error {
	name, valid := validClassificationName(payload.Name)
	if payload.IndexID == "" || !valid {
		return recordClassificationConflict(tx, libraryID, op, "invalidPayload", nil)
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=?`, replayLibrary, payload.IndexID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	if payload.ParentID != nil {
		var alive int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, replayLibrary, *payload.ParentID).Scan(&alive); err != nil {
			return err
		}
		if alive == 0 {
			return recordClassificationConflict(tx, libraryID, op, "parentMissing", map[string]string{"parentId": *payload.ParentID})
		}
	}
	origin := payload.Origin
	if origin != "automatic" {
		origin = "manual"
	}
	var parent any
	if payload.ParentID != nil {
		parent = *payload.ParentID
	}
	if _, err := tx.Exec(`INSERT INTO classification_nodes(library_id,index_id,parent_id,name,name_key,origin,name_op_id,parent_op_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, replayLibrary, payload.IndexID, parent, name, classificationNameKey(name), origin, op.OpID, op.OpID, now(), now()); err != nil {
		return err
	}
	return rebuildClassificationAncestors(tx, replayLibrary)
}

// classificationFieldWinner reports whether a concurrent edit of the same
// field by another author was not observed by this operation's context.
func classificationFieldWinner(tx *sql.Tx, libraryID string, op libraryOperationEnvelope, context map[string]int64, priorOpID string) (bool, error) {
	if priorOpID == "" || priorOpID == op.OpID {
		return false, nil
	}
	var priorAuthor string
	var priorCounter int64
	err := tx.QueryRow(`SELECT author_id,counter FROM classification_operations WHERE library_id=? AND op_id=?`, libraryID, priorOpID).Scan(&priorAuthor, &priorCounter)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return priorAuthor != op.AuthorID && context[priorAuthor] < priorCounter, nil
}

func applyClassificationUpdate(tx *sql.Tx, libraryID, replayLibrary string, op libraryOperationEnvelope, payload classificationOpPayload, context map[string]int64, resolve bool) error {
	if resolve && payload.ConflictOpID != "" {
		if _, err := tx.Exec(`UPDATE classification_conflicts SET resolved=1 WHERE library_id=? AND op_id=?`, libraryID, payload.ConflictOpID); err != nil {
			return err
		}
	}
	parent, name, nameOp, parentOp, deleted, err := classificationReplayNode(tx, replayLibrary, payload.IndexID)
	if err == sql.ErrNoRows || deleted != 0 {
		return recordClassificationConflict(tx, libraryID, op, "indexNotFound", map[string]string{"indexId": payload.IndexID})
	}
	if err != nil {
		return err
	}
	if len(payload.Patch) == 0 || len(payload.Patch) > 2 {
		return recordClassificationConflict(tx, libraryID, op, "invalidPatch", nil)
	}
	targetName, targetParent := name, (*string)(nil)
	if parent.Valid {
		value := parent.String
		targetParent = &value
	}
	rename, move := false, false
	for field, raw := range payload.Patch {
		switch field {
		case "name":
			var candidate string
			if json.Unmarshal(raw, &candidate) != nil {
				return recordClassificationConflict(tx, libraryID, op, "invalidPatch", nil)
			}
			candidate, valid := validClassificationName(candidate)
			if !valid {
				return recordClassificationConflict(tx, libraryID, op, "invalidPatch", nil)
			}
			targetName, rename = candidate, true
		case "parentId":
			value, decodeErr := decodeClassificationParent(raw)
			if decodeErr != nil {
				return recordClassificationConflict(tx, libraryID, op, "invalidPatch", nil)
			}
			targetParent, move = value, true
		default:
			return recordClassificationConflict(tx, libraryID, op, "invalidPatch", nil)
		}
	}
	if rename {
		concurrent, winnerErr := classificationFieldWinner(tx, libraryID, op, context, nameOp)
		if winnerErr != nil {
			return winnerErr
		}
		if concurrent && !resolve {
			if err = recordClassificationConflict(tx, libraryID, op, "concurrentName", map[string]string{"indexId": payload.IndexID, "name": targetName}); err != nil {
				return err
			}
			rename = false
		}
	}
	if move {
		if targetParent != nil && *targetParent == payload.IndexID {
			return recordClassificationConflict(tx, libraryID, op, "indexCycle", map[string]string{"indexId": payload.IndexID})
		}
		if targetParent != nil {
			var alive, cycle int
			if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, replayLibrary, *targetParent).Scan(&alive); err != nil {
				return err
			}
			if alive == 0 {
				return recordClassificationConflict(tx, libraryID, op, "parentMissing", map[string]string{"indexId": payload.IndexID, "parentId": *targetParent})
			}
			if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_ancestors WHERE library_id=? AND ancestor_id=? AND descendant_id=?`, replayLibrary, payload.IndexID, *targetParent).Scan(&cycle); err != nil {
				return err
			}
			if cycle != 0 {
				return recordClassificationConflict(tx, libraryID, op, "indexCycle", map[string]string{"indexId": payload.IndexID, "parentId": *targetParent})
			}
		}
		concurrent, winnerErr := classificationFieldWinner(tx, libraryID, op, context, parentOp)
		if winnerErr != nil {
			return winnerErr
		}
		if concurrent && !resolve {
			if err = recordClassificationConflict(tx, libraryID, op, "concurrentMove", map[string]string{"indexId": payload.IndexID}); err != nil {
				return err
			}
			move = false
		}
	}
	if !rename && !move {
		return nil
	}
	if rename {
		nameOp = op.OpID
	}
	if move {
		parentOp = op.OpID
	}
	var parentArg any
	if targetParent != nil {
		parentArg = *targetParent
	}
	if _, err = tx.Exec(`UPDATE classification_nodes SET parent_id=?,name=?,name_key=?,name_op_id=?,parent_op_id=?,updated_at=? WHERE library_id=? AND index_id=?`, parentArg, targetName, classificationNameKey(targetName), nameOp, parentOp, now(), replayLibrary, payload.IndexID); err != nil {
		return err
	}
	return rebuildClassificationAncestors(tx, replayLibrary)
}

func applyClassificationDelete(tx *sql.Tx, libraryID, replayLibrary string, op libraryOperationEnvelope, payload classificationOpPayload) error {
	_, _, _, _, deleted, err := classificationReplayNode(tx, replayLibrary, payload.IndexID)
	if err == sql.ErrNoRows || deleted != 0 {
		return recordClassificationConflict(tx, libraryID, op, "indexNotFound", map[string]string{"indexId": payload.IndexID})
	}
	if err != nil {
		return err
	}
	var children, memberships int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0`, replayLibrary, payload.IndexID).Scan(&children); err != nil {
		return err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND index_id=?`, replayLibrary, payload.IndexID).Scan(&memberships); err != nil {
		return err
	}
	if children != 0 || memberships != 0 {
		return recordClassificationConflict(tx, libraryID, op, "indexNotEmpty", map[string]string{"indexId": payload.IndexID})
	}
	if _, err = tx.Exec(`UPDATE classification_nodes SET deleted=1,name_op_id=?,parent_op_id=?,updated_at=? WHERE library_id=? AND index_id=?`, op.OpID, op.OpID, now(), replayLibrary, payload.IndexID); err != nil {
		return err
	}
	return rebuildClassificationAncestors(tx, replayLibrary)
}

func applyClassificationMemberships(tx *sql.Tx, libraryID, replayLibrary string, op libraryOperationEnvelope, payload classificationOpPayload) error {
	if len(payload.Files) == 0 || len(payload.Files) > 200 {
		return recordClassificationConflict(tx, libraryID, op, "invalidPayload", nil)
	}
	for _, file := range payload.Files {
		for _, add := range file.Adds {
			var alive int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, replayLibrary, add.IndexID).Scan(&alive); err != nil {
				return err
			}
			if alive == 0 {
				return recordClassificationConflict(tx, libraryID, op, "indexNotFound", map[string]string{"indexId": add.IndexID, "fileId": file.FileID})
			}
		}
		for _, remove := range file.Removes {
			var alive int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, replayLibrary, remove.IndexID).Scan(&alive); err != nil {
				return err
			}
			if alive == 0 {
				return recordClassificationConflict(tx, libraryID, op, "indexNotFound", map[string]string{"indexId": remove.IndexID, "fileId": file.FileID})
			}
		}
	}
	for _, file := range payload.Files {
		if file.FileID == "" || len(file.FileID) > 160 {
			continue
		}
		var deleted int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?`, file.FileID).Scan(&deleted); err != nil {
			return err
		}
		if deleted != 0 {
			continue
		}
		for _, add := range file.Adds {
			origin := add.Origin
			if origin != "automatic" && origin != "legacy-import" {
				origin = "manual"
			}
			if add.Token == "" || len(add.Token) > 200 {
				continue
			}
			if _, err := tx.Exec(`INSERT INTO classification_adds(library_id,file_id,index_id,add_token,origin,operation_id,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, replayLibrary, file.FileID, add.IndexID, add.Token, origin, op.OpID, now()); err != nil {
				return err
			}
		}
		for _, remove := range file.Removes {
			if remove.ObservedFrontier != nil {
				if _, err := tx.Exec(`INSERT INTO classification_removes(library_id,file_id,index_id,add_token,operation_id,removed_at)
					SELECT ?,?,?,a.add_token,?,? FROM classification_adds a JOIN classification_operations o ON o.library_id=? AND o.op_id=a.operation_id
					WHERE a.library_id=? AND a.file_id=? AND a.index_id=? AND o.author_id=? AND o.counter<=? ON CONFLICT DO NOTHING`,
					replayLibrary, file.FileID, remove.IndexID, op.OpID, now(), libraryID, replayLibrary, file.FileID, remove.IndexID, remove.ObservedFrontier.Author, remove.ObservedFrontier.Counter); err != nil {
					return err
				}
			}
			for _, token := range remove.ObservedTokens {
				if token == "" || len(token) > 200 {
					continue
				}
				if _, err := tx.Exec(`INSERT INTO classification_removes(library_id,file_id,index_id,add_token,operation_id,removed_at) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, replayLibrary, file.FileID, remove.IndexID, token, op.OpID, now()); err != nil {
					return err
				}
			}
		}
	}
	return rebuildClassificationMemberships(tx, replayLibrary)
}

// receiveClassificationOperation validates causality and the logical clock,
// persists the full envelope, and replays the projection in the same
// transaction as the inbox/frontier advance performed by the caller.
func receiveClassificationOperation(tx *sql.Tx, libraryID, localNodeID string, op libraryOperationEnvelope) error {
	if !knownClassificationOperationKind(op.Kind) {
		return fmt.Errorf("%w: %s", errClassificationQuarantine, "unsupportedKind")
	}
	var header classificationOpHeader
	if err := json.Unmarshal(op.Payload, &header); err != nil || header.SchemaVersion != 2 || header.Clock < 1 {
		return fmt.Errorf("%w: %s", errClassificationQuarantine, "unsupportedSchema")
	}
	for author, counter := range header.Context {
		if counter <= 0 {
			continue
		}
		var frontier int64
		err := tx.QueryRow(`SELECT last_counter FROM library_operation_frontiers WHERE author_id=?`, author).Scan(&frontier)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if frontier >= counter {
			continue
		}
		var present int
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM library_operations WHERE author_id=? AND counter=?) OR EXISTS(SELECT 1 FROM classification_operations WHERE library_id=? AND author_id=? AND counter=?)`, author, counter, libraryID, author, counter).Scan(&present); err != nil {
			return err
		}
		if present == 0 {
			return errClassificationPending
		}
	}
	// The clock must exceed the clocks of the exact predecessors the sender
	// claims to have observed. Concurrent operations the sender never saw do
	// not constrain it, so the receiver's own clock is deliberately excluded.
	for author, counter := range header.Context {
		if counter <= 0 {
			continue
		}
		var observedClock int64
		err := tx.QueryRow(`SELECT clock FROM classification_operations WHERE library_id=? AND author_id=? AND counter=?`, libraryID, author, counter).Scan(&observedClock)
		if err == sql.ErrNoRows {
			continue // Legacy schema-1 operations carry no logical clock.
		}
		if err != nil {
			return err
		}
		if header.Clock <= observedClock {
			return fmt.Errorf("%w: %s", errClassificationQuarantine, "invalidClock")
		}
	}
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM classification_operations WHERE library_id=? AND op_id=?`, libraryID, op.OpID).Scan(&exists); err != nil {
		return err
	} else if exists == 0 {
		contextJSON, err := json.Marshal(header.Context)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO classification_operations(library_id,op_id,author_id,counter,clock,kind,payload_json,context_json,baseline_id,origin,received_at) VALUES(?,?,?,?,?,?,?,?,?,'remote',?)`, libraryID, op.OpID, op.AuthorID, op.Counter, header.Clock, op.Kind, string(op.Payload), string(contextJSON), header.BaselineID, now()); err != nil {
			return err
		}
	}
	if err := bumpClassificationAuthorClock(tx, libraryID, op.AuthorID, header.Clock, op.Counter); err != nil {
		return err
	}
	return replayClassificationTx(tx, libraryID, localNodeID)
}

func (s *server) libraryIndexConflicts(w http.ResponseWriter, _ *http.Request) {
	libraryID, _, err := s.classificationIdentity()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "索引尚未初始化")
		return
	}
	rows, err := s.db.Query(`SELECT op_id,author_id,counter,kind,reason,detail_json,resolved,seen_at FROM classification_conflicts WHERE library_id=? ORDER BY seen_at,author_id,counter LIMIT 200`, libraryID)
	if err != nil {
		writeError(w, 500, "无法读取索引冲突")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var opID, author, kind, reason, detail, seen string
		var counter int64
		var resolved int
		if err = rows.Scan(&opID, &author, &counter, &kind, &reason, &detail, &resolved, &seen); err != nil {
			writeError(w, 500, "无法读取索引冲突")
			return
		}
		items = append(items, map[string]any{"opId": opID, "author": author, "counter": counter, "kind": kind, "reason": reason, "detail": json.RawMessage(detail), "resolved": resolved != 0, "seenAt": seen})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *server) resolveLibraryIndexConflict(w http.ResponseWriter, r *http.Request) {
	libraryID, actor, err := s.refreshClassificationBeforeWrite()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	conflictOpID := strings.TrimSpace(r.PathValue("opId"))
	var input struct {
		RequestID string                     `json:"requestId"`
		IndexID   string                     `json:"indexId"`
		Patch     map[string]json.RawMessage `json:"patch"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&input) != nil || conflictOpID == "" || strings.TrimSpace(input.IndexID) == "" || len(input.Patch) == 0 || len(input.Patch) > 2 {
		writeClassificationError(w, classificationError(400, "invalidResolve", "冲突解决内容无效"))
		return
	}
	requestHash := classificationSeed("index.resolve", conflictOpID, input.IndexID, fmt.Sprint(input.Patch))
	tx, err := s.db.Begin()
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	defer tx.Rollback()
	if raw, found, requestErr := classificationRequestResult(tx, libraryID, actor, input.RequestID, requestHash); requestErr != nil {
		writeClassificationError(w, requestErr)
		return
	} else if found {
		writeStoredClassificationResult(w, 200, raw)
		return
	}
	var resolved int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM classification_conflicts WHERE library_id=? AND op_id=?`, libraryID, conflictOpID).Scan(&resolved); err != nil || resolved == 0 {
		writeClassificationError(w, classificationError(404, "conflictNotFound", "冲突不存在"))
		return
	}
	opID := newID()
	domain := map[string]any{"indexId": strings.TrimSpace(input.IndexID), "patch": input.Patch, "conflictOpId": conflictOpID}
	if _, err = recordClassificationOperation(tx, libraryID, "index.resolve", opID, domain); err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = replayClassificationTx(tx, libraryID, actor); err != nil {
		writeClassificationError(w, err)
		return
	}
	result := map[string]any{"operationId": opID}
	raw, err := storeClassificationRequest(tx, libraryID, actor, input.RequestID, requestHash, result)
	if err != nil {
		writeClassificationError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeClassificationError(w, err)
		return
	}
	writeStoredClassificationResult(w, 200, raw)
}
