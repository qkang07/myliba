package main

import (
	"database/sql"
	"errors"
)

// migrateUnifiedLibrary adds the first, deliberately additive, part of the
// library model.  resources remains the compatibility surface for the desktop
// UI and old cloud records; these tables give it a stable logical file identity
// and keep classifications independent from a particular local location.
//
// Do not fold this into a destructive resources migration. Existing desktop
// databases are the source of truth for this baseline import.
func migrateUnifiedLibrary(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS library_files (
	file_id TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_classification_authoritative (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS library_resource_aliases (
	resource_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE RESTRICT,
	imported_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_resource_aliases_file_idx ON library_resource_aliases(file_id);
CREATE TABLE IF NOT EXISTS library_revisions (
	revision_id TEXT PRIMARY KEY,
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	resource_version_id TEXT UNIQUE REFERENCES versions(id) ON DELETE SET NULL,
	size INTEGER NOT NULL,
	modified_at TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_revisions_file_idx ON library_revisions(file_id, modified_at);
CREATE TABLE IF NOT EXISTS library_verified_content (
	revision_id TEXT PRIMARY KEY REFERENCES library_revisions(revision_id) ON DELETE CASCADE,
	plain_hash TEXT NOT NULL,
	verified_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_verified_content_hash_idx ON library_verified_content(plain_hash);
CREATE TABLE IF NOT EXISTS library_operation_authors (
	author_id TEXT PRIMARY KEY,
	next_counter INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS library_operation_identity (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_operations (
	op_id TEXT PRIMARY KEY,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('pending','sent','acknowledged','failed')),
	error TEXT NOT NULL DEFAULT '',
	UNIQUE(author_id,counter)
);
CREATE INDEX IF NOT EXISTS library_operations_state_idx ON library_operations(state,created_at);
CREATE TABLE IF NOT EXISTS library_saved_queries (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE COLLATE NOCASE,
	filters_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_search_projection (
	scope INTEGER NOT NULL,
	ordinal INTEGER NOT NULL,
	file_id TEXT NOT NULL,
	revision_id TEXT NOT NULL,
	name TEXT NOT NULL,
	folder_id TEXT NOT NULL,
	search_text TEXT NOT NULL,
	file_json TEXT NOT NULL,
	PRIMARY KEY(scope,ordinal)
);
CREATE INDEX IF NOT EXISTS library_search_projection_file_idx ON library_search_projection(scope,file_id,revision_id);
CREATE INDEX IF NOT EXISTS library_search_projection_folder_idx ON library_search_projection(scope,folder_id);
CREATE TABLE IF NOT EXISTS library_search_current (
	scope INTEGER NOT NULL,
	file_id TEXT NOT NULL,
	revision_id TEXT NOT NULL,
	name TEXT NOT NULL,
	folder_id TEXT NOT NULL,
	folder_name TEXT NOT NULL,
	modified TEXT NOT NULL,
	ingested_at TEXT NOT NULL,
	file_type TEXT NOT NULL,
	local_only INTEGER NOT NULL,
	name_search TEXT NOT NULL,
	folder_search TEXT NOT NULL,
	tags_search TEXT NOT NULL,
	file_json TEXT NOT NULL,
	PRIMARY KEY(scope,file_id)
);
CREATE INDEX IF NOT EXISTS library_search_current_order ON library_search_current(scope,name,file_id);
CREATE INDEX IF NOT EXISTS library_search_current_folder ON library_search_current(scope,folder_id);
CREATE INDEX IF NOT EXISTS library_search_current_type ON library_search_current(scope,file_type);
CREATE VIRTUAL TABLE IF NOT EXISTS library_search_fts USING fts5(name_search,folder_search,tags_search,tokenize='trigram');
CREATE TABLE IF NOT EXISTS library_search_tags (
	scope INTEGER NOT NULL,
	file_id TEXT NOT NULL,
	tag_key TEXT NOT NULL,
	tag_name TEXT NOT NULL,
	PRIMARY KEY(scope,file_id,tag_key)
);
CREATE INDEX IF NOT EXISTS library_search_tags_key ON library_search_tags(scope,tag_key,file_id);
CREATE TABLE IF NOT EXISTS library_search_meta (
	scope INTEGER PRIMARY KEY,
	generation TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_operation_inbox (
	op_id TEXT PRIMARY KEY,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	received_at TEXT NOT NULL,
	applied_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_operation_quarantine (
	op_id TEXT PRIMARY KEY,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	kind TEXT NOT NULL,
	reason TEXT NOT NULL,
	seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_operation_frontiers (
	author_id TEXT PRIMARY KEY,
	last_counter INTEGER NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_receipts (
	node_id TEXT PRIMARY KEY,
	frontiers_json TEXT NOT NULL,
	catalog_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_members (
	node_id TEXT PRIMARY KEY,
	epoch INTEGER NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('active','retired')),
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_receipt_publication (
	id INTEGER PRIMARY KEY CHECK(id=1),
	digest TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_snapshot_publication (
	author_id TEXT PRIMARY KEY,
	frontier INTEGER NOT NULL,
	record_hash TEXT NOT NULL,
	published_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_legacy_snapshots (
	record_hash TEXT PRIMARY KEY,
	author_id TEXT NOT NULL,
	object_id TEXT NOT NULL,
	file_id TEXT NOT NULL,
	revision_id TEXT NOT NULL,
	classification_known INTEGER NOT NULL,
	folder_name TEXT NOT NULL,
	tags_json TEXT NOT NULL,
	catalog_at TEXT NOT NULL,
	imported_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_legacy_snapshots_file_idx ON library_legacy_snapshots(file_id,catalog_at);
CREATE TABLE IF NOT EXISTS library_legacy_folder_identities (
	record_hash TEXT PRIMARY KEY,
	folder_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_legacy_import_frontiers (
	author_id TEXT PRIMARY KEY,
	last_catalog_at TEXT NOT NULL,
	last_record_hash TEXT NOT NULL,
	imported_count INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS library_legacy_baselines (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	record_hash TEXT NOT NULL,
	catalog_at TEXT NOT NULL,
	folder_name TEXT NOT NULL,
	tags_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_lifecycle (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	state TEXT NOT NULL CHECK(state IN ('active','trashed')),
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	context_json TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_deletions (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE RESTRICT,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	deleted_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_folder_state (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	folder_id TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_name_state (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	current_name TEXT NOT NULL,
	context_json TEXT NOT NULL DEFAULT '{}',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_name_conflicts (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	file_name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(file_id,author_id,counter)
);
CREATE TABLE IF NOT EXISTS library_file_current_revision (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	revision_id TEXT NOT NULL,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	context_json TEXT NOT NULL DEFAULT '{}',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_file_revision_conflicts (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	revision_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(file_id,author_id,counter)
);
CREATE TABLE IF NOT EXISTS library_folder_conflicts (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	folder_name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(file_id,author_id,counter)
);
CREATE TABLE IF NOT EXISTS library_folder_tombstones (
	name TEXT PRIMARY KEY COLLATE NOCASE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	deleted_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_folder_rename_state (
	anchor TEXT PRIMARY KEY COLLATE NOCASE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	current_name TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_folder_rename_conflicts (
	anchor TEXT NOT NULL COLLATE NOCASE,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	folder_name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(anchor,author_id,counter)
);
CREATE TABLE IF NOT EXISTS library_tag_add_tokens (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	tag_name TEXT NOT NULL COLLATE NOCASE,
	token TEXT NOT NULL,
	author_id TEXT NOT NULL,
	counter INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(file_id,tag_name,token)
);
CREATE TABLE IF NOT EXISTS library_tag_remove_tokens (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	tag_name TEXT NOT NULL COLLATE NOCASE,
	token TEXT NOT NULL,
	removed_at TEXT NOT NULL,
	PRIMARY KEY(file_id,tag_name,token)
);
CREATE TABLE IF NOT EXISTS library_locations (
	location_id TEXT PRIMARY KEY,
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	revision_id TEXT REFERENCES library_revisions(revision_id) ON DELETE SET NULL,
	resource_id TEXT UNIQUE REFERENCES resources(id) ON DELETE CASCADE,
	node_id TEXT NOT NULL DEFAULT '',
	storage_id TEXT NOT NULL DEFAULT '',
	resource_ref TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL CHECK(state IN ('available','missing')),
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_locations_file_idx ON library_locations(file_id, state);
CREATE TABLE IF NOT EXISTS library_file_folders (
	file_id TEXT PRIMARY KEY REFERENCES library_files(file_id) ON DELETE CASCADE,
	folder_id TEXT NOT NULL REFERENCES folders(id) ON DELETE CASCADE,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_file_folders_folder_idx ON library_file_folders(folder_id);
CREATE TABLE IF NOT EXISTS library_file_tags (
	file_id TEXT NOT NULL REFERENCES library_files(file_id) ON DELETE CASCADE,
	tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
	created_at TEXT NOT NULL,
	PRIMARY KEY(file_id, tag_id)
);
CREATE INDEX IF NOT EXISTS library_file_tags_tag_idx ON library_file_tags(tag_id);
CREATE TABLE IF NOT EXISTS library_transfer_tasks (
	task_id TEXT PRIMARY KEY,
	kind TEXT NOT NULL CHECK(kind IN ('upload','release')),
	file_id TEXT NOT NULL,
	revision_id TEXT NOT NULL DEFAULT '',
	object_id TEXT NOT NULL DEFAULT '',
	location_id TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	detail TEXT NOT NULL DEFAULT '',
	plain_hash TEXT NOT NULL DEFAULT '',
	size INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS library_transfer_tasks_state_idx ON library_transfer_tasks(kind,state);
CREATE INDEX IF NOT EXISTS library_transfer_tasks_file_idx ON library_transfer_tasks(file_id);
CREATE TABLE IF NOT EXISTS library_recovery_attestations (
	node_id TEXT PRIMARY KEY,
	library_id TEXT NOT NULL,
	proof_hash TEXT NOT NULL,
	verified_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_release_log (
	location_id TEXT PRIMARY KEY,
	file_id TEXT NOT NULL,
	revision_id TEXT NOT NULL,
	credential_id TEXT NOT NULL,
	released_at TEXT NOT NULL
);

-- Legacy mutations remain the only writable classification API until P3.
-- The triggers materialize the same classification against logical file IDs.
CREATE TRIGGER IF NOT EXISTS library_alias_after_resource_insert
AFTER INSERT ON resources BEGIN
	INSERT OR IGNORE INTO library_files(file_id,created_at,updated_at)
	VALUES(CASE WHEN NEW.file_id<>'' THEN NEW.file_id ELSE NEW.id END,NEW.first_seen_at,NEW.last_seen_at);
	INSERT OR IGNORE INTO library_resource_aliases(resource_id,file_id,imported_at)
	VALUES(NEW.id,CASE WHEN NEW.file_id<>'' THEN NEW.file_id ELSE NEW.id END,NEW.first_seen_at);
END;
CREATE TRIGGER IF NOT EXISTS library_folder_after_insert
AFTER INSERT ON resource_folders BEGIN
	INSERT INTO library_file_folders(file_id,folder_id,updated_at)
	SELECT a.file_id,NEW.folder_id,datetime('now') FROM library_resource_aliases a WHERE a.resource_id=NEW.resource_id
	ON CONFLICT(file_id) DO UPDATE SET folder_id=excluded.folder_id,updated_at=excluded.updated_at;
END;
CREATE TRIGGER IF NOT EXISTS library_folder_before_delete
BEFORE DELETE ON resource_folders BEGIN
	DELETE FROM library_file_folders WHERE file_id=(SELECT file_id FROM library_resource_aliases WHERE resource_id=OLD.resource_id);
END;
CREATE TRIGGER IF NOT EXISTS library_tag_after_insert
AFTER INSERT ON resource_tags BEGIN
	INSERT OR IGNORE INTO library_file_tags(file_id,tag_id,created_at)
	SELECT a.file_id,NEW.tag_id,NEW.created_at FROM library_resource_aliases a WHERE a.resource_id=NEW.resource_id;
END;
CREATE TRIGGER IF NOT EXISTS library_tag_before_delete
BEFORE DELETE ON resource_tags BEGIN
	DELETE FROM library_file_tags WHERE file_id=(SELECT file_id FROM library_resource_aliases WHERE resource_id=OLD.resource_id) AND tag_id=OLD.tag_id;
END;
`)
	if err != nil {
		return err
	}
	return importUnifiedLibraryBaseline(db)
}

// importUnifiedLibraryBaseline is idempotent. It intentionally never changes
// an existing alias: a later scan must not replace an established logical ID
// merely because the OS began reporting a file identifier.
func importUnifiedLibraryBaseline(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_files(file_id,created_at,updated_at)
		SELECT CASE WHEN file_id<>'' THEN file_id ELSE id END,first_seen_at,last_seen_at FROM resources`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_resource_aliases(resource_id,file_id,imported_at)
		SELECT id,CASE WHEN file_id<>'' THEN file_id ELSE id END,first_seen_at FROM resources`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_revisions(revision_id,file_id,resource_version_id,size,modified_at,created_at)
		SELECT v.id,a.file_id,v.id,v.size,v.modified_at,v.discovered_at FROM versions v JOIN library_resource_aliases a ON a.resource_id=v.resource_id`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_locations(location_id,file_id,revision_id,resource_id,storage_id,resource_ref,state,updated_at)
		SELECT 'legacy-resource:'||r.id,a.file_id,NULLIF(r.current_version_id,''),r.id,r.root_id,r.path,
		CASE WHEN r.available<>0 THEN 'available' ELSE 'missing' END,r.last_seen_at
		FROM resources r JOIN library_resource_aliases a ON a.resource_id=r.id`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR REPLACE INTO library_file_folders(file_id,folder_id,updated_at)
		SELECT a.file_id,rf.folder_id,datetime('now') FROM resource_folders rf JOIN library_resource_aliases a ON a.resource_id=rf.resource_id`); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_file_tags(file_id,tag_id,created_at)
		SELECT a.file_id,rt.tag_id,rt.created_at FROM resource_tags rt JOIN library_resource_aliases a ON a.resource_id=rt.resource_id`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *server) unifiedFileID(resourceID string) (string, error) {
	var fileID string
	err := s.db.QueryRow(`SELECT file_id FROM library_resource_aliases WHERE resource_id=?`, resourceID).Scan(&fileID)
	if errors.Is(err, sql.ErrNoRows) {
		return resourceID, nil // Compatibility for a database opened before migration completes.
	}
	return fileID, err
}

// syncUnifiedResource projects one compatibility resource into the additive
// model after a scan changes its revision or availability. Classification is
// deliberately not derived from the filesystem here.
func (s *server) syncUnifiedResource(resourceID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_files(file_id,created_at,updated_at)
		SELECT CASE WHEN file_id<>'' THEN file_id ELSE id END,first_seen_at,last_seen_at FROM resources WHERE id=?`, resourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_resource_aliases(resource_id,file_id,imported_at)
		SELECT id,CASE WHEN file_id<>'' THEN file_id ELSE id END,first_seen_at FROM resources WHERE id=?`, resourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_revisions(revision_id,file_id,resource_version_id,size,modified_at,created_at)
		SELECT v.id,a.file_id,v.id,v.size,v.modified_at,v.discovered_at FROM versions v JOIN library_resource_aliases a ON a.resource_id=v.resource_id WHERE v.resource_id=?`, resourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO library_locations(location_id,file_id,revision_id,resource_id,storage_id,resource_ref,state,updated_at)
		SELECT 'legacy-resource:'||r.id,a.file_id,NULLIF(r.current_version_id,''),r.id,r.root_id,r.path,
		CASE WHEN r.available<>0 THEN 'available' ELSE 'missing' END,r.last_seen_at
		FROM resources r JOIN library_resource_aliases a ON a.resource_id=r.id WHERE r.id=?`, resourceID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE library_locations SET
		revision_id=(SELECT NULLIF(current_version_id,'') FROM resources WHERE id=?),
		storage_id=(SELECT root_id FROM resources WHERE id=?),
		resource_ref=(SELECT path FROM resources WHERE id=?),
		state=CASE WHEN (SELECT available FROM resources WHERE id=?)<>0 THEN 'available' ELSE 'missing' END,
		updated_at=(SELECT last_seen_at FROM resources WHERE id=?)
		WHERE resource_id=?`, resourceID, resourceID, resourceID, resourceID, resourceID, resourceID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
