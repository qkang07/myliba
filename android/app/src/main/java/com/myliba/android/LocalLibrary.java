package com.myliba.android;

import android.icu.lang.UCharacter;
import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;
import org.json.JSONArray;
import org.json.JSONObject;

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.NoSuchAlgorithmException;
import java.text.Normalizer;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;
import java.util.UUID;

final class LocalLibrary extends SQLiteOpenHelper {
    static final class Root {
        final String uri;
        final String name;
        final int count;
        final boolean preserveStructure;

        Root(String uri, String name, int count, boolean preserveStructure) {
            this.uri = uri;
            this.name = name;
            this.count = count;
            this.preserveStructure = preserveStructure;
        }
    }

    static final class IndexNode {
        final String id;
        final String parentId;
        final String name;
        final String version;
        final int count;
        final boolean hasChildren;
        String path;

        IndexNode(String id, String parentId, String name, String version, int count, boolean hasChildren) {
            this.id = id;
            this.parentId = parentId;
            this.name = name;
            this.version = version;
            this.count = count;
            this.hasChildren = hasChildren;
            this.path = name;
        }
    }

    static final class Item {
        final String uri;
        final String rootUri;
        final String name;
        final String path;
        final String mime;
        final long size;
        final long modified;
        final String tags;
        final String folderId;
        final String fileId;
        final String logicalFileId;
        final String revisionId;

        Item(Cursor cursor) {
            uri = cursor.getString(0);
            rootUri = cursor.getString(1);
            name = cursor.getString(2);
            path = cursor.getString(3);
            mime = cursor.getString(4);
            size = cursor.getLong(5);
            modified = cursor.getLong(6);
            tags = cursor.getString(7);
            folderId = cursor.getString(8);
            fileId = cursor.getString(9);
            logicalFileId = cursor.getString(10);
            revisionId = cursor.getString(11);
        }
    }

    static final class Folder {
        final String id;
        final String name;
        final int count;

        Folder(Cursor cursor) {
            id = cursor.getString(0);
            name = cursor.getString(1);
            count = cursor.getInt(2);
        }
    }

    static final class FolderConflict {
        final String fileId;
        final String folderName;
        final String author;
        final long counter;

        FolderConflict(Cursor cursor) {
            fileId = cursor.getString(0);
            folderName = cursor.getString(1);
            author = cursor.getString(2);
            counter = cursor.getLong(3);
        }
    }

    static final class RenameConflict {
        final String anchor;
        final String folderName;
        final String appliedName;
        final String author;
        final long counter;

        RenameConflict(String[] values) {
            anchor = values[0];
            folderName = values[1];
            appliedName = values[2];
            author = values[3];
            counter = Long.parseLong(values[4]);
        }
    }

    static final class Collection {
        final String id;
        final String name;
        final LocalFilters filters;

        Collection(Cursor cursor) {
            id = cursor.getString(0);
            name = cursor.getString(1);
            filters = LocalFilters.parse(cursor.getString(2));
        }
    }

    static final class Page {
        final List<Item> items;
        final int total;

        Page(List<Item> items, int total) {
            this.items = items;
            this.total = total;
        }
    }

    static final class Attribute {
        final String key;
        final String label;
        final int count;

        Attribute(String key, String label, int count) {
            this.key = key;
            this.label = label;
            this.count = count;
        }
    }

    LocalLibrary(Context context) {
        super(context, "local-library.db", null, 19);
        verifyClassificationNameVectors();
    }

    @Override public void onConfigure(SQLiteDatabase db) {
        db.setForeignKeyConstraintsEnabled(true);
    }

    @Override public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE roots (uri TEXT PRIMARY KEY, name TEXT NOT NULL, preserve_structure INTEGER NOT NULL DEFAULT 1)");
        db.execSQL("CREATE TABLE items (uri TEXT PRIMARY KEY, root_uri TEXT NOT NULL REFERENCES roots(uri) ON DELETE CASCADE, name TEXT NOT NULL, path TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL, scan_token TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'file', folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL, file_id TEXT NOT NULL UNIQUE, logical_file_id TEXT NOT NULL, revision_id TEXT NOT NULL)");
        db.execSQL("CREATE INDEX items_root ON items(root_uri)");
        db.execSQL("CREATE TABLE tags (item_uri TEXT NOT NULL REFERENCES items(uri) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY(item_uri, name))");
        createOrganizationTables(db);
        createOrganizationIndexes(db);
        createAliasTables(db);
        createIndexSyncTables(db);
        createOperationSyncTables(db);
        createClassificationTables(db);
        createClassificationOperationTables(db);
        ensureClassificationIdentity(db);
        ensureActiveClassificationState(db);
    }

    @Override public void onUpgrade(SQLiteDatabase db, int oldVersion, int newVersion) {
        if (oldVersion == 1) {
            createOrganizationTables(db);
            db.execSQL("ALTER TABLE items ADD COLUMN kind TEXT NOT NULL DEFAULT 'file'");
            db.execSQL("ALTER TABLE items ADD COLUMN folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL");
            createOrganizationIndexes(db);
            List<String[]> existing = new ArrayList<>();
            try (Cursor cursor = db.rawQuery("SELECT uri, name FROM items", null)) {
                while (cursor.moveToNext()) existing.add(new String[]{cursor.getString(0), cursor.getString(1)});
            }
            for (String[] item : existing) {
                ContentValues values = new ContentValues();
                values.put("kind", kindFor(item[1]));
                db.update("items", values, "uri=?", new String[]{item[0]});
            }
            oldVersion = 2;
        }
        if (oldVersion == 2) {
            db.execSQL("UPDATE items SET kind='image' WHERE lower(name) LIKE '%.arw'");
            oldVersion = 3;
        }
        if (oldVersion == 3) {
            db.execSQL("ALTER TABLE items ADD COLUMN file_id TEXT");
            db.execSQL("ALTER TABLE items ADD COLUMN revision_id TEXT");
            List<String> uris = new ArrayList<>();
            try (Cursor cursor = db.rawQuery("SELECT uri FROM items", null)) {
                while (cursor.moveToNext()) uris.add(cursor.getString(0));
            }
            for (String uri : uris) {
                ContentValues values = new ContentValues();
                values.put("file_id", UUID.randomUUID().toString());
                values.put("revision_id", UUID.randomUUID().toString());
                db.update("items", values, "uri=?", new String[]{uri});
            }
            db.execSQL("CREATE UNIQUE INDEX items_file_id ON items(file_id)");
			db.execSQL("ALTER TABLE items ADD COLUMN logical_file_id TEXT NOT NULL DEFAULT ''");
			db.execSQL("UPDATE items SET logical_file_id=file_id WHERE logical_file_id='' ");
			createAliasTables(db);
            createIndexSyncTables(db);
			oldVersion = 6;
        }
        if (oldVersion == 4) {
            createAliasTables(db);
            oldVersion = 5;
        }
        if (oldVersion == 5) {
            db.execSQL("ALTER TABLE items ADD COLUMN logical_file_id TEXT NOT NULL DEFAULT ''");
            db.execSQL("UPDATE items SET logical_file_id=file_id WHERE logical_file_id='' ");
            createIndexSyncTables(db);
            oldVersion = 6;
        }
        if (oldVersion == 6) {
            createOperationSyncTables(db);
            oldVersion = 7;
        }
        if (oldVersion == 7) {
            createOperationSyncTables(db);
            oldVersion = 8;
        }
        if (oldVersion == 8) {
            createOperationSyncTables(db);
            oldVersion = 9;
        }
        if (oldVersion == 9) {
            createOperationSyncTables(db);
            oldVersion = 10;
        }
        if (oldVersion == 10) {
            createOperationSyncTables(db);
            oldVersion = 11;
        }
        if (oldVersion == 11) {
            createOperationSyncTables(db);
            oldVersion = 12;
        }
        if (oldVersion == 12) {
            createOperationSyncTables(db);
            oldVersion = 13;
        }
        if (oldVersion == 13) {
            createOperationSyncTables(db);
            oldVersion = 14;
        }
        if (oldVersion == 14) {
            createOperationSyncTables(db);
            oldVersion = 15;
        }
        if (oldVersion == 15) {
            createOperationSyncTables(db);
            oldVersion = 16;
        }
        if (oldVersion == 16) {
            createOperationSyncTables(db);
            oldVersion = 17;
        }
        if (oldVersion == 17) {
            db.execSQL("ALTER TABLE roots ADD COLUMN preserve_structure INTEGER NOT NULL DEFAULT 1");
            createClassificationTables(db);
            ensureClassificationIdentity(db);
            oldVersion = 18;
        }
        if (oldVersion == 18) {
            createClassificationOperationTables(db);
            backfillClassificationOperations(db);
            ensureActiveClassificationState(db);
            oldVersion = 19;
        }
        if (oldVersion != newVersion) throw new IllegalStateException("Missing local library migration to " + newVersion + " from " + oldVersion);
    }

    private static void createClassificationTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_library_identity (id INTEGER PRIMARY KEY CHECK(id=1), library_id TEXT NOT NULL, node_id TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_nodes (library_id TEXT NOT NULL, index_id TEXT NOT NULL, parent_id TEXT, name TEXT NOT NULL, name_key TEXT NOT NULL, origin TEXT NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, name_op_id TEXT NOT NULL DEFAULT '', parent_op_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(library_id,index_id), FOREIGN KEY(library_id,parent_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE RESTRICT)");
        db.execSQL("CREATE INDEX IF NOT EXISTS classification_nodes_browse ON classification_nodes(library_id,parent_id,deleted,name_key,index_id)");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_adds (library_id TEXT NOT NULL, logical_file_id TEXT NOT NULL, index_id TEXT NOT NULL, add_token TEXT NOT NULL, origin TEXT NOT NULL, operation_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, PRIMARY KEY(library_id,logical_file_id,index_id,add_token), FOREIGN KEY(library_id,index_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE)");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_removes (library_id TEXT NOT NULL, logical_file_id TEXT NOT NULL, index_id TEXT NOT NULL, add_token TEXT NOT NULL, operation_id TEXT NOT NULL, removed_at TEXT NOT NULL, PRIMARY KEY(library_id,logical_file_id,index_id,add_token))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_memberships (library_id TEXT NOT NULL, logical_file_id TEXT NOT NULL, index_id TEXT NOT NULL, has_manual INTEGER NOT NULL DEFAULT 0, has_automatic INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL, PRIMARY KEY(library_id,logical_file_id,index_id), FOREIGN KEY(library_id,index_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE)");
        db.execSQL("CREATE INDEX IF NOT EXISTS classification_memberships_index ON classification_memberships(library_id,index_id,logical_file_id)");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_ancestors (library_id TEXT NOT NULL, ancestor_id TEXT NOT NULL, descendant_id TEXT NOT NULL, depth INTEGER NOT NULL CHECK(depth>=0), PRIMARY KEY(library_id,ancestor_id,descendant_id), FOREIGN KEY(library_id,ancestor_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE, FOREIGN KEY(library_id,descendant_id) REFERENCES classification_nodes(library_id,index_id) ON DELETE CASCADE)");
        db.execSQL("CREATE INDEX IF NOT EXISTS classification_ancestors_desc ON classification_ancestors(library_id,descendant_id,depth,ancestor_id)");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_source_dirs (library_id TEXT NOT NULL, node_id TEXT NOT NULL, root_id TEXT NOT NULL, source_dir_key TEXT NOT NULL, index_id TEXT NOT NULL, display_segments_json TEXT NOT NULL, disabled INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(library_id,node_id,root_id,source_dir_key))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_imports (library_id TEXT NOT NULL, node_id TEXT NOT NULL, root_id TEXT NOT NULL, source_entry_id TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('processed','skipped')), index_id TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL, PRIMARY KEY(library_id,node_id,root_id,source_entry_id))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_requests (library_id TEXT NOT NULL, actor TEXT NOT NULL, request_id TEXT NOT NULL, request_hash TEXT NOT NULL, result_json TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(library_id,actor,request_id))");
    }

    private static void createClassificationOperationTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_operations (library_id TEXT NOT NULL, op_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, clock INTEGER NOT NULL, kind TEXT NOT NULL, payload_json TEXT NOT NULL, context_json TEXT NOT NULL, baseline_id TEXT NOT NULL DEFAULT '', origin TEXT NOT NULL CHECK(origin IN ('local','remote')), received_at TEXT NOT NULL, PRIMARY KEY(library_id,op_id), UNIQUE(library_id,author_id,counter))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_author_clocks (library_id TEXT NOT NULL, author_id TEXT NOT NULL, last_clock INTEGER NOT NULL, last_counter INTEGER NOT NULL, PRIMARY KEY(library_id,author_id))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_conflicts (library_id TEXT NOT NULL, op_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, kind TEXT NOT NULL, reason TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '', resolved INTEGER NOT NULL DEFAULT 0, seen_at TEXT NOT NULL, PRIMARY KEY(library_id,op_id))");
        db.execSQL("CREATE TABLE IF NOT EXISTS classification_library_state (library_id TEXT PRIMARY KEY, mode TEXT NOT NULL DEFAULT 'shadow' CHECK(mode IN ('shadow','active')), baseline_id TEXT NOT NULL DEFAULT '', activated_at TEXT NOT NULL DEFAULT '')");
        addClassificationColumnIfMissing(db, "classification_source_dirs", "parent_index_id", "TEXT NOT NULL DEFAULT ''");
        addClassificationColumnIfMissing(db, "classification_imports", "logical_file_id", "TEXT NOT NULL DEFAULT ''");
    }

    private static void addClassificationColumnIfMissing(SQLiteDatabase db, String table, String column, String definition) {
        try (Cursor cursor = db.rawQuery("PRAGMA table_info(" + table + ")", null)) {
            while (cursor.moveToNext()) if (column.equals(cursor.getString(1))) return;
        }
        db.execSQL("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition);
    }

    private static final class BackfillFile {
        final JSONArray adds = new JSONArray();
        final Map<String, JSONArray> removeTokens = new LinkedHashMap<>();
    }

    // Existing manual edits live only in the projection. Synthesize envelopes so
    // the deterministic replay reproduces them instead of erasing them, ordering
    // creates parents-first, memberships while indexes are alive, deletes
    // children-first.
    private static void backfillClassificationOperations(SQLiteDatabase db) {
        ensureClassificationIdentity(db);
        String libraryId, nodeId;
        try (Cursor cursor = db.rawQuery("SELECT library_id,node_id FROM classification_library_identity WHERE id=1", null)) {
            if (!cursor.moveToFirst()) return;
            libraryId = cursor.getString(0); nodeId = cursor.getString(1);
        }
        db.execSQL("UPDATE classification_source_dirs SET parent_index_id=COALESCE((SELECT n.parent_id FROM classification_nodes n WHERE n.library_id=classification_source_dirs.library_id AND n.index_id=classification_source_dirs.index_id),'') WHERE parent_index_id=''");
        backfillImportFileIds(db);
        try {
            List<String[]> manual = new ArrayList<>();
            Set<String> manualIds = new HashSet<>();
            try (Cursor cursor = db.rawQuery("SELECT index_id,COALESCE(parent_id,''),name FROM classification_nodes WHERE library_id=? AND origin='manual' ORDER BY index_id", new String[]{libraryId})) {
                while (cursor.moveToNext()) { manual.add(new String[]{cursor.getString(0), cursor.getString(1), cursor.getString(2)}); manualIds.add(cursor.getString(0)); }
            }
            for (String[] node : classificationTopoParentsFirst(manual, manualIds)) {
                JSONObject domain = new JSONObject().put("indexId", node[0]).put("name", node[2]).put("origin", "manual");
                if (!node[1].isEmpty()) domain.put("parentId", node[1]);
                ClassificationOperations.recordOperation(db, libraryId, "index.create", UUID.randomUUID().toString(), domain);
            }
            try (Cursor cursor = db.rawQuery("SELECT index_id,COALESCE(parent_id,''),name FROM classification_nodes WHERE library_id=? AND deleted=0 AND origin<>'manual' AND (name_op_id<>'' OR parent_op_id<>'') ORDER BY index_id", new String[]{libraryId})) {
                while (cursor.moveToNext()) {
                    JSONObject patch = new JSONObject().put("name", cursor.getString(2)).put("parentId", cursor.getString(1).isEmpty() ? JSONObject.NULL : cursor.getString(1));
                    JSONObject domain = new JSONObject().put("indexId", cursor.getString(0)).put("patch", patch);
                    ClassificationOperations.recordOperation(db, libraryId, "index.update", UUID.randomUUID().toString(), domain);
                }
            }
            backfillMembershipOperations(db, libraryId);
            List<String[]> deleted = new ArrayList<>();
            Set<String> deletedIds = new HashSet<>();
            try (Cursor cursor = db.rawQuery("SELECT index_id,COALESCE(parent_id,'') FROM classification_nodes WHERE library_id=? AND deleted=1 ORDER BY index_id", new String[]{libraryId})) {
                while (cursor.moveToNext()) { deleted.add(new String[]{cursor.getString(0), cursor.getString(1)}); deletedIds.add(cursor.getString(0)); }
            }
            List<String[]> deleteOrder = classificationTopoParentsFirst(deleted, deletedIds);
            Collections.reverse(deleteOrder);
            for (String[] node : deleteOrder) {
                ClassificationOperations.recordOperation(db, libraryId, "index.delete", UUID.randomUUID().toString(), new JSONObject().put("indexId", node[0]));
            }
        } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
        ClassificationOperations.replay(db, libraryId, nodeId);
    }

    private static void backfillImportFileIds(SQLiteDatabase db) {
        List<String[]> rows = new ArrayList<>();
        try (Cursor cursor = db.rawQuery("SELECT library_id,node_id,root_id,source_entry_id,index_id FROM classification_imports WHERE state='processed' AND index_id<>'' AND logical_file_id=''", null)) {
            while (cursor.moveToNext()) rows.add(new String[]{cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getString(3), cursor.getString(4)});
        }
        for (String[] row : rows) {
            String token = "automatic:" + classificationSeed(row[0], row[1], row[2], row[3], row[4]);
            String fileId = "";
            try (Cursor cursor = db.rawQuery("SELECT logical_file_id FROM classification_adds WHERE library_id=? AND add_token=? LIMIT 1", new String[]{row[0], token})) {
                if (cursor.moveToFirst()) fileId = cursor.getString(0);
            }
            if (!fileId.isEmpty()) db.execSQL("UPDATE classification_imports SET logical_file_id=? WHERE library_id=? AND node_id=? AND root_id=? AND source_entry_id=?",
                    new Object[]{fileId, row[0], row[1], row[2], row[3]});
        }
    }

    private static void backfillMembershipOperations(SQLiteDatabase db, String libraryId) throws org.json.JSONException {
        Map<String, Map<String, BackfillFile>> groups = new TreeMap<>();
        try (Cursor cursor = db.rawQuery("SELECT operation_id,logical_file_id,index_id,add_token FROM classification_adds WHERE library_id=? AND origin='manual' ORDER BY operation_id,logical_file_id,index_id,add_token", new String[]{libraryId})) {
            while (cursor.moveToNext()) {
                BackfillFile file = groups.computeIfAbsent(cursor.getString(0), k -> new LinkedHashMap<>()).computeIfAbsent(cursor.getString(1), k -> new BackfillFile());
                file.adds.put(new JSONObject().put("indexId", cursor.getString(2)).put("token", cursor.getString(3)).put("origin", "manual"));
            }
        }
        try (Cursor cursor = db.rawQuery("SELECT operation_id,logical_file_id,index_id,add_token FROM classification_removes WHERE library_id=? ORDER BY operation_id,logical_file_id,index_id,add_token", new String[]{libraryId})) {
            while (cursor.moveToNext()) {
                BackfillFile file = groups.computeIfAbsent(cursor.getString(0), k -> new LinkedHashMap<>()).computeIfAbsent(cursor.getString(1), k -> new BackfillFile());
                file.removeTokens.computeIfAbsent(cursor.getString(2), k -> new JSONArray()).put(cursor.getString(3));
            }
        }
        for (Map<String, BackfillFile> group : groups.values()) {
            List<String> fileIds = new ArrayList<>(group.keySet());
            for (int start = 0; start < fileIds.size(); start += 200) {
                JSONArray files = new JSONArray();
                for (int at = start; at < Math.min(start + 200, fileIds.size()); at++) {
                    BackfillFile file = group.get(fileIds.get(at));
                    JSONArray removes = new JSONArray();
                    for (Map.Entry<String, JSONArray> remove : file.removeTokens.entrySet()) removes.put(new JSONObject().put("indexId", remove.getKey()).put("observedTokens", remove.getValue()));
                    files.put(new JSONObject().put("fileId", fileIds.get(at)).put("adds", file.adds).put("removes", removes));
                }
                ClassificationOperations.recordOperation(db, libraryId, "index.memberships", UUID.randomUUID().toString(), new JSONObject().put("files", files));
            }
        }
    }

    private static List<String[]> classificationTopoParentsFirst(List<String[]> rows, Set<String> ids) {
        List<String[]> order = new ArrayList<>();
        Set<String> emitted = new HashSet<>();
        boolean progress = true;
        while (order.size() < rows.size() && progress) {
            progress = false;
            for (String[] row : rows) {
                if (emitted.contains(row[0])) continue;
                if (row[1].isEmpty() || !ids.contains(row[1]) || emitted.contains(row[1])) { order.add(row); emitted.add(row[0]); progress = true; }
            }
        }
        for (String[] row : rows) if (!emitted.contains(row[0])) { order.add(row); emitted.add(row[0]); }
        return order;
    }

    private static void ensureClassificationIdentity(SQLiteDatabase db) {
        try (Cursor cursor = db.rawQuery("SELECT 1 FROM classification_library_identity WHERE id=1", null)) {
            if (cursor.moveToFirst()) return;
        }
        ContentValues values = new ContentValues();
        values.put("id", 1);
        values.put("library_id", "local:" + UUID.randomUUID());
        values.put("node_id", "android:" + UUID.randomUUID());
        db.insertOrThrow("classification_library_identity", null, values);
    }

    private static void ensureActiveClassificationState(SQLiteDatabase db) {
        ensureClassificationIdentity(db);
        String libraryId;
        try (Cursor cursor = db.rawQuery("SELECT library_id FROM classification_library_identity WHERE id=1", null)) {
            if (!cursor.moveToFirst()) throw new IllegalStateException("分类索引身份不存在");
            libraryId = cursor.getString(0);
        }
        db.execSQL("INSERT OR IGNORE INTO classification_library_state(library_id,mode,baseline_id,activated_at) VALUES(?,'active','',?)",
                new Object[]{libraryId, Instant.now().toString()});
    }

    String[] classificationIdentity(SQLiteDatabase db) {
        ensureClassificationIdentity(db);
        try (Cursor cursor = db.rawQuery("SELECT library_id,node_id FROM classification_library_identity WHERE id=1", null)) {
            if (!cursor.moveToFirst()) throw new IllegalStateException("分类索引身份不存在");
            return new String[]{cursor.getString(0), cursor.getString(1)};
        }
    }

    String classificationMode() {
        SQLiteDatabase db = getReadableDatabase();
        String[] identity = classificationIdentity(db);
        try (Cursor cursor = db.rawQuery("SELECT mode FROM classification_library_state WHERE library_id=?", new String[]{identity[0]})) {
            return cursor.moveToFirst() ? cursor.getString(0) : "shadow";
        }
    }

    String classificationLibraryId() {
        return classificationIdentity(getReadableDatabase())[0];
    }

    /** Binds a new-model local library to the shared encrypted library. */
    void bindSharedClassification(String sharedLibraryId, String sharedNodeId) {
        if (!sharedLibraryId.matches("[0-9a-f]{64}") || sharedNodeId.isEmpty() || sharedNodeId.length() > 160) {
            throw new IllegalArgumentException("共享分类索引身份无效");
        }
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db);
            if (!identity[0].equals(sharedLibraryId)) {
                db.execSQL("PRAGMA defer_foreign_keys=ON");
                String[] tables = {"classification_ancestors", "classification_memberships", "classification_removes", "classification_adds",
                        "classification_nodes", "classification_source_dirs", "classification_imports",
                        "classification_requests", "classification_operations",
                        "classification_author_clocks", "classification_conflicts", "classification_library_state"};
                for (String table : tables) db.execSQL("UPDATE " + table + " SET library_id=? WHERE library_id=?", new Object[]{sharedLibraryId, identity[0]});
                db.execSQL("UPDATE classification_source_dirs SET node_id=? WHERE library_id=? AND node_id=?", new Object[]{sharedNodeId, sharedLibraryId, identity[1]});
                db.execSQL("UPDATE classification_imports SET node_id=? WHERE library_id=? AND node_id=?", new Object[]{sharedNodeId, sharedLibraryId, identity[1]});
                db.execSQL("UPDATE classification_library_identity SET library_id=?,node_id=? WHERE id=1", new Object[]{sharedLibraryId, sharedNodeId});
            } else if (!identity[1].equals(sharedNodeId)) {
                db.execSQL("UPDATE classification_source_dirs SET node_id=? WHERE library_id=? AND node_id=?", new Object[]{sharedNodeId, sharedLibraryId, identity[1]});
                db.execSQL("UPDATE classification_imports SET node_id=? WHERE library_id=? AND node_id=?", new Object[]{sharedNodeId, sharedLibraryId, identity[1]});
                db.execSQL("UPDATE classification_library_identity SET node_id=? WHERE id=1", new Object[]{sharedNodeId});
            }
            db.execSQL("INSERT INTO classification_library_state(library_id,mode,baseline_id,activated_at) VALUES(?,'active','',?) " +
                            "ON CONFLICT(library_id) DO UPDATE SET mode='active',baseline_id='',activated_at=excluded.activated_at",
                    new Object[]{sharedLibraryId, Instant.now().toString()});
            ClassificationOperations.replay(db, sharedLibraryId, sharedNodeId);
            db.setTransactionSuccessful();
        } finally {
            db.endTransaction();
        }
    }

    private static boolean legacyClassificationKind(String kind) {
        return kind.startsWith("folder.") || kind.startsWith("tag.");
    }

    private static boolean classificationActive(SQLiteDatabase db) {
        ensureClassificationIdentity(db);
        try (Cursor cursor = db.rawQuery("SELECT 1 FROM classification_library_state s JOIN classification_library_identity i ON i.library_id=s.library_id WHERE i.id=1 AND s.mode='active'", null)) {
            return cursor.moveToFirst();
        }
    }


    static String classificationSeed(String... parts) {
        try {
            MessageDigest digest = MessageDigest.getInstance("SHA-256");
            for (String part : parts) {
                byte[] bytes = part.getBytes(StandardCharsets.UTF_8);
                digest.update(ByteBuffer.allocate(4).putInt(bytes.length).array());
                digest.update(bytes);
            }
            StringBuilder result = new StringBuilder();
            for (byte value : digest.digest()) result.append(String.format("%02x", value & 0xff));
            return result.toString();
        } catch (NoSuchAlgorithmException impossible) {
            throw new IllegalStateException(impossible);
        }
    }

    String ensureClassificationSourceDirectory(SQLiteDatabase db, String[] identity, String rootId, String sourceKey,
                                                String name, String parentId, String displaySegmentsJson) {
        try (Cursor cursor = db.rawQuery("SELECT index_id,disabled FROM classification_source_dirs WHERE library_id=? AND node_id=? AND root_id=? AND source_dir_key=?",
                new String[]{identity[0], identity[1], rootId, sourceKey})) {
            if (cursor.moveToFirst()) {
                String indexId = cursor.getString(0);
                if (cursor.getInt(1) != 0) return null;
                try (Cursor node = db.rawQuery("SELECT deleted FROM classification_nodes WHERE library_id=? AND index_id=?", new String[]{identity[0], indexId})) {
                    return node.moveToFirst() && node.getInt(0) == 0 ? indexId : null;
                }
            }
        }
        String indexId = "source-index:" + classificationSeed(identity[0], identity[1], rootId, sourceKey);
        String nodeName = normalizeClassificationName(name);
        ContentValues node = new ContentValues();
        node.put("library_id", identity[0]);
        node.put("index_id", indexId);
        if (parentId == null) node.putNull("parent_id"); else node.put("parent_id", parentId);
        node.put("name", nodeName);
        node.put("name_key", classificationNameKey(nodeName));
        node.put("origin", "automatic");
        node.put("created_at", Instant.now().toString());
        node.put("updated_at", Instant.now().toString());
        db.insertWithOnConflict("classification_nodes", null, node, SQLiteDatabase.CONFLICT_IGNORE);
        db.execSQL("UPDATE classification_nodes SET name=?,name_key=?,updated_at=? WHERE library_id=? AND index_id=? AND name_op_id='' AND deleted=0",
                new Object[]{nodeName, classificationNameKey(nodeName), Instant.now().toString(), identity[0], indexId});
        ContentValues mapping = new ContentValues();
        mapping.put("library_id", identity[0]);
        mapping.put("node_id", identity[1]);
        mapping.put("root_id", rootId);
        mapping.put("source_dir_key", sourceKey);
        mapping.put("index_id", indexId);
        mapping.put("display_segments_json", displaySegmentsJson);
        mapping.put("parent_index_id", parentId == null ? "" : parentId);
        mapping.put("disabled", 0);
        db.insertWithOnConflict("classification_source_dirs", null, mapping, SQLiteDatabase.CONFLICT_IGNORE);
        db.execSQL("UPDATE classification_source_dirs SET display_segments_json=?,parent_index_id=? WHERE library_id=? AND node_id=? AND root_id=? AND source_dir_key=? AND EXISTS(SELECT 1 FROM classification_nodes n WHERE n.library_id=classification_source_dirs.library_id AND n.index_id=classification_source_dirs.index_id AND n.name_op_id='' AND n.deleted=0)",
                new Object[]{displaySegmentsJson, parentId == null ? "" : parentId, identity[0], identity[1], rootId, sourceKey});
        return indexId;
    }

    void classifyScannedItem(SQLiteDatabase db, String[] identity, String rootId, String sourceEntryId,
                             String itemUri, String indexId) {
        try (Cursor cursor = db.rawQuery("SELECT 1 FROM classification_imports WHERE library_id=? AND node_id=? AND root_id=? AND source_entry_id=?",
                new String[]{identity[0], identity[1], rootId, sourceEntryId})) {
            if (cursor.moveToFirst()) return;
        }
        String state = "skipped";
        String storedIndex = indexId == null ? "" : indexId;
        String logicalFileId = "";
        if (indexId != null) {
            try (Cursor cursor = db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items WHERE uri=?", new String[]{itemUri})) {
                if (!cursor.moveToFirst()) throw new IllegalStateException("收录文件身份不存在");
                logicalFileId = cursor.getString(0);
            }
            ContentValues add = new ContentValues();
            add.put("library_id", identity[0]);
            add.put("logical_file_id", logicalFileId);
            add.put("index_id", indexId);
            add.put("add_token", "automatic:" + classificationSeed(identity[0], identity[1], rootId, sourceEntryId, indexId));
            add.put("origin", "automatic");
            add.put("created_at", Instant.now().toString());
            db.insertWithOnConflict("classification_adds", null, add, SQLiteDatabase.CONFLICT_IGNORE);
            state = "processed";
        }
        ContentValues progress = new ContentValues();
        progress.put("library_id", identity[0]);
        progress.put("node_id", identity[1]);
        progress.put("root_id", rootId);
        progress.put("source_entry_id", sourceEntryId);
        progress.put("state", state);
        progress.put("index_id", storedIndex);
        progress.put("logical_file_id", logicalFileId);
        progress.put("updated_at", Instant.now().toString());
        db.insertOrThrow("classification_imports", null, progress);
        rebuildClassificationProjection(db, identity[0]);
    }

    private static void rebuildClassificationProjection(SQLiteDatabase db, String libraryId) {
        db.delete("classification_memberships", "library_id=?", new String[]{libraryId});
        db.execSQL("INSERT INTO classification_memberships(library_id,logical_file_id,index_id,has_manual,has_automatic,updated_at) " +
                        "SELECT a.library_id,a.logical_file_id,a.index_id,MAX(CASE WHEN a.origin IN ('manual','legacy-import') THEN 1 ELSE 0 END),MAX(CASE WHEN a.origin='automatic' THEN 1 ELSE 0 END),? " +
                        "FROM classification_adds a JOIN classification_nodes n ON n.library_id=a.library_id AND n.index_id=a.index_id AND n.deleted=0 " +
                        "LEFT JOIN classification_removes r ON r.library_id=a.library_id AND r.logical_file_id=a.logical_file_id AND r.index_id=a.index_id AND r.add_token=a.add_token " +
                        "WHERE a.library_id=? AND r.add_token IS NULL GROUP BY a.library_id,a.logical_file_id,a.index_id",
                new Object[]{Instant.now().toString(), libraryId});
        db.delete("classification_ancestors", "library_id=?", new String[]{libraryId});
        db.execSQL("WITH RECURSIVE paths(ancestor_id,descendant_id,depth) AS (" +
                        "SELECT index_id,index_id,0 FROM classification_nodes WHERE library_id=? AND deleted=0 UNION ALL " +
                        "SELECT n.parent_id,p.descendant_id,p.depth+1 FROM paths p JOIN classification_nodes n ON n.library_id=? AND n.index_id=p.ancestor_id WHERE n.parent_id IS NOT NULL AND p.depth<1024) " +
                        "INSERT INTO classification_ancestors(library_id,ancestor_id,descendant_id,depth) SELECT ?,ancestor_id,descendant_id,depth FROM paths",
                new Object[]{libraryId, libraryId, libraryId});
    }

    private static String classificationNodeVersion(String id, String parentId, String name, int deleted, String nameOp, String parentOp) {
        return classificationSeed("classification-node-v1", id, parentId == null ? "" : parentId, name,
                String.valueOf(deleted), nameOp == null ? "" : nameOp, parentOp == null ? "" : parentOp);
    }

    private static String classificationName(String raw) {
        String name = normalizeClassificationName(raw);
        if (name.isEmpty() || name.codePointCount(0, name.length()) > 255) throw new IllegalArgumentException("索引名称需为 1 到 255 个字符");
        for (int at = 0; at < name.length();) {
            int value = name.codePointAt(at);
            if (value == 0 || Character.isISOControl(value)) throw new IllegalArgumentException("索引名称不能包含控制字符");
            at += Character.charCount(value);
        }
        return name;
    }

    static String normalizeClassificationName(String value) {
        String raw = value == null ? "" : value;
        int start = 0, end = raw.length();
        while (start < end) {
            int current = raw.codePointAt(start);
            if (!classificationWhitespace(current)) break;
            start += Character.charCount(current);
        }
        while (start < end) {
            int current = raw.codePointBefore(end);
            if (!classificationWhitespace(current)) break;
            end -= Character.charCount(current);
        }
        return Normalizer.normalize(raw.substring(start, end), Normalizer.Form.NFC);
    }

    private static boolean classificationWhitespace(int value) {
        return (value >= 0x0009 && value <= 0x000d) || value == 0x0020 || value == 0x0085 || value == 0x00a0 ||
                value == 0x1680 || (value >= 0x2000 && value <= 0x200a) || value == 0x2028 || value == 0x2029 ||
                value == 0x202f || value == 0x205f || value == 0x3000;
    }

    static String classificationNameKey(String value) {
        String normalized = normalizeClassificationName(value);
        return Normalizer.normalize(UCharacter.foldCase(normalized, true), Normalizer.Form.NFC);
    }

    private static boolean classificationNameVectorsVerified;

    private static synchronized void verifyClassificationNameVectors() {
        if (classificationNameVectorsVerified) return;
        String[][] vectors = {
                {"  Cafe\u0301  ", "Café", "café"},
                {"\u212Bngstro\u0308m", "Ångström", "ångström"},
                {"Straße", "Straße", "strasse"},
                {"Σίσυφος", "Σίσυφος", "σίσυφοσ"},
                {"\u212Aelvin", "Kelvin", "kelvin"},
                {"İ", "İ", "i\u0307"},
                {"\u00a0项目\u3000", "项目", "项目"},
        };
        for (String[] vector : vectors) {
            if (!normalizeClassificationName(vector[0]).equals(vector[1]) || !classificationNameKey(vector[0]).equals(vector[2])) {
                throw new IllegalStateException("此 Android Unicode 运行时不支持分类名称规范版本 1");
            }
        }
        classificationNameVectorsVerified = true;
    }

    private static void refreshLegacyClassification(SQLiteDatabase db, String[] identity) {
        ClassificationOperations.replay(db, identity[0], identity[1]);
    }

    private static IndexNode readIndexNode(SQLiteDatabase db, String libraryId, String indexId) {
        try (Cursor cursor = db.rawQuery("SELECT n.index_id,n.parent_id,n.name,n.deleted,n.name_op_id,n.parent_op_id," +
                "EXISTS(SELECT 1 FROM classification_nodes c WHERE c.library_id=n.library_id AND c.parent_id=n.index_id AND c.deleted=0)," +
                "(SELECT COUNT(DISTINCT m.logical_file_id) FROM classification_ancestors a JOIN classification_memberships m ON m.library_id=a.library_id AND m.index_id=a.descendant_id WHERE a.library_id=n.library_id AND a.ancestor_id=n.index_id) " +
                "FROM classification_nodes n WHERE n.library_id=? AND n.index_id=?", new String[]{libraryId, indexId})) {
            if (!cursor.moveToFirst() || cursor.getInt(3) != 0) return null;
            String parent = cursor.isNull(1) ? null : cursor.getString(1);
            return new IndexNode(cursor.getString(0), parent, cursor.getString(2),
                    classificationNodeVersion(cursor.getString(0), parent, cursor.getString(2), cursor.getInt(3), cursor.getString(4), cursor.getString(5)),
                    cursor.getInt(7), cursor.getInt(6) != 0);
        }
    }

    private static String readIndexPath(SQLiteDatabase db, String libraryId, IndexNode node) {
        ArrayList<String> parts = new ArrayList<>();
        try (Cursor cursor = db.rawQuery("SELECT n.name FROM classification_ancestors a JOIN classification_nodes n ON n.library_id=a.library_id AND n.index_id=a.ancestor_id WHERE a.library_id=? AND a.descendant_id=? AND a.depth>0 ORDER BY a.depth DESC", new String[]{libraryId, node.id})) {
            while (cursor.moveToNext()) parts.add(cursor.getString(0));
        }
        parts.add(node.name);
        return String.join(" / ", parts);
    }

    List<IndexNode> indexChildren(String parentId) {
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db);
            refreshLegacyClassification(db, identity);
            ArrayList<String> ids = new ArrayList<>();
            String sql = parentId == null ?
                    "SELECT index_id FROM classification_nodes WHERE library_id=? AND parent_id IS NULL AND deleted=0 ORDER BY name_key,index_id" :
                    "SELECT index_id FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0 ORDER BY name_key,index_id";
            String[] args = parentId == null ? new String[]{identity[0]} : new String[]{identity[0], parentId};
            try (Cursor cursor = db.rawQuery(sql, args)) { while (cursor.moveToNext()) ids.add(cursor.getString(0)); }
            ArrayList<IndexNode> result = new ArrayList<>();
            for (String id : ids) { IndexNode node = readIndexNode(db, identity[0], id); if (node != null) { node.path=readIndexPath(db,identity[0],node); result.add(node); } }
            db.setTransactionSuccessful();
            return result;
        } finally { db.endTransaction(); }
    }

    List<IndexNode> allIndexes() {
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try {
            String[] identity=classificationIdentity(db);refreshLegacyClassification(db,identity);ArrayList<IndexNode> result=new ArrayList<>();
            try(Cursor cursor=db.rawQuery("SELECT index_id FROM classification_nodes WHERE library_id=? AND deleted=0 ORDER BY name_key,index_id",new String[]{identity[0]})){
                while(cursor.moveToNext()){IndexNode node=readIndexNode(db,identity[0],cursor.getString(0));if(node!=null){node.path=readIndexPath(db,identity[0],node);result.add(node);}}
            }
            Collections.sort(result,(a,b)->a.path.compareToIgnoreCase(b.path));db.setTransactionSuccessful();return result;
        } finally {db.endTransaction();}
    }

    List<IndexNode> indexAncestors(String indexId) {
        SQLiteDatabase db=getReadableDatabase();String[] identity=classificationIdentity(db);ArrayList<IndexNode> result=new ArrayList<>();
        try(Cursor cursor=db.rawQuery("SELECT ancestor_id FROM classification_ancestors WHERE library_id=? AND descendant_id=? AND depth>0 ORDER BY depth DESC",new String[]{identity[0],indexId})){
            while(cursor.moveToNext()){IndexNode node=readIndexNode(db,identity[0],cursor.getString(0));if(node!=null)result.add(node);}
        }
        return result;
    }

    IndexNode createIndex(String parentId, String rawName) {
        String name = classificationName(rawName);
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db);
            refreshLegacyClassification(db, identity);
            if (parentId != null && readIndexNode(db, identity[0], parentId) == null) throw new IllegalStateException("上级索引不存在");
            String[] duplicateArgs = parentId == null ? new String[]{identity[0], classificationNameKey(name)} : new String[]{identity[0], parentId, classificationNameKey(name)};
            String duplicateSql = parentId == null ? "SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND parent_id IS NULL AND deleted=0 AND name_key=?" : "SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0 AND name_key=?";
            try (Cursor cursor = db.rawQuery(duplicateSql, duplicateArgs)) { cursor.moveToFirst(); if (cursor.getInt(0) != 0) throw new IllegalStateException("同级已有同名索引"); }
            String operationId = UUID.randomUUID().toString();
            String indexId = "index:" + operationId;
            JSONObject domain = new JSONObject();
            try {
                domain.put("indexId", indexId);
                domain.put("name", name);
                domain.put("origin", "manual");
                if (parentId != null) domain.put("parentId", parentId);
            } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            ClassificationOperations.recordOperation(db, identity[0], "index.create", operationId, domain);
            ClassificationOperations.replay(db, identity[0], identity[1]);
            IndexNode result = readIndexNode(db, identity[0], indexId);
            db.setTransactionSuccessful();
            return result;
        } finally { db.endTransaction(); }
    }

    IndexNode updateIndex(String indexId, String rawName, String parentId, String baseVersion) {
        String name = classificationName(rawName);
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db);
            refreshLegacyClassification(db, identity);
            IndexNode current = readIndexNode(db, identity[0], indexId);
            if (current == null) throw new IllegalStateException("索引不存在");
            if (!current.version.equals(baseVersion)) throw new IllegalStateException("索引已发生变化，请刷新后重试");
            if (indexId.equals(parentId)) throw new IllegalStateException("索引不能成为自己的上级");
            if (parentId != null) {
                if (readIndexNode(db, identity[0], parentId) == null) throw new IllegalStateException("上级索引不存在");
                try (Cursor cursor = db.rawQuery("SELECT COUNT(*) FROM classification_ancestors WHERE library_id=? AND ancestor_id=? AND descendant_id=?", new String[]{identity[0], indexId, parentId})) { cursor.moveToFirst(); if (cursor.getInt(0) != 0) throw new IllegalStateException("索引不能移动到自己的下级"); }
            }
            String duplicateSql = parentId == null ? "SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id<>? AND parent_id IS NULL AND deleted=0 AND name_key=?" : "SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id<>? AND parent_id=? AND deleted=0 AND name_key=?";
            String[] duplicateArgs = parentId == null ? new String[]{identity[0], indexId, classificationNameKey(name)} : new String[]{identity[0], indexId, parentId, classificationNameKey(name)};
            try (Cursor cursor = db.rawQuery(duplicateSql, duplicateArgs)) { cursor.moveToFirst(); if (cursor.getInt(0) != 0) throw new IllegalStateException("同级已有同名索引"); }
            String operationId = UUID.randomUUID().toString();
            JSONObject patch = new JSONObject(), domain = new JSONObject();
            try {
                patch.put("name", name);
                patch.put("parentId", parentId == null ? JSONObject.NULL : parentId);
                domain.put("indexId", indexId);
                domain.put("baseVersion", baseVersion);
                domain.put("patch", patch);
            } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            ClassificationOperations.recordOperation(db, identity[0], "index.update", operationId, domain);
            ClassificationOperations.replay(db, identity[0], identity[1]);
            IndexNode result = readIndexNode(db, identity[0], indexId);
            db.setTransactionSuccessful();
            return result;
        } finally { db.endTransaction(); }
    }

    void deleteIndex(String indexId, String baseVersion) {
        SQLiteDatabase db = getWritableDatabase(); db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db); refreshLegacyClassification(db, identity);
            IndexNode current = readIndexNode(db, identity[0], indexId);
            if (current == null) throw new IllegalStateException("索引不存在");
            if (!current.version.equals(baseVersion)) throw new IllegalStateException("索引已发生变化，请刷新后重试");
            if (current.hasChildren || current.count != 0) throw new IllegalStateException("索引仍有下级或文件归属");
            String operationId = UUID.randomUUID().toString();
            JSONObject domain = new JSONObject();
            try { domain.put("indexId", indexId); domain.put("baseVersion", baseVersion); } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            ClassificationOperations.recordOperation(db, identity[0], "index.delete", operationId, domain);
            ClassificationOperations.replay(db, identity[0], identity[1]);
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void changeIndexMemberships(List<String> itemUris, List<String> addIndexIds, List<String> removeIndexIds) {
        changeIndexMemberships(UUID.randomUUID().toString(), itemUris, addIndexIds, removeIndexIds);
    }

    private List<String> logicalFileIdsForItemUris(List<String> itemUris) {
        SQLiteDatabase db=getReadableDatabase();LinkedHashSet<String> result=new LinkedHashSet<>();
        for(String uri:itemUris)try(Cursor cursor=db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items WHERE uri=?",new String[]{uri})){
            if(!cursor.moveToFirst())throw new IllegalStateException("文件不存在");result.add(cursor.getString(0));
        }
        return new ArrayList<>(result);
    }

    private static String membershipDigest(SQLiteDatabase db, String libraryId, String fileId) {
        try (Cursor cursor = db.rawQuery("SELECT GROUP_CONCAT(index_id,char(31)) FROM (SELECT index_id FROM classification_memberships WHERE library_id=? AND logical_file_id=? ORDER BY index_id)", new String[]{libraryId, fileId})) {
            return cursor.moveToFirst() && !cursor.isNull(0) ? cursor.getString(0) : "";
        }
    }

    String changeIndexMemberships(String requestId, List<String> itemUris, List<String> addIndexIds, List<String> removeIndexIds) {
        return changeIndexMembershipsByFileIds(requestId,logicalFileIdsForItemUris(itemUris),addIndexIds,removeIndexIds);
    }

    String changeIndexMembershipsByFileIds(String requestId, List<String> requestedFileIds, List<String> addIndexIds, List<String> removeIndexIds) {
        if (requestId == null || requestId.trim().isEmpty() || requestId.length() > 160) throw new IllegalArgumentException("requestId 无效");
        if (requestedFileIds.isEmpty() || requestedFileIds.size() > 200 || addIndexIds.size() + removeIndexIds.size() == 0 || addIndexIds.size() > 20 || removeIndexIds.size() > 20) throw new IllegalArgumentException("索引归属批次无效");
        SQLiteDatabase db = getWritableDatabase(); db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db); refreshLegacyClassification(db, identity);
            LinkedHashSet<String> fileIds = new LinkedHashSet<>(requestedFileIds);
            LinkedHashSet<String> adds = new LinkedHashSet<>(addIndexIds), removes = new LinkedHashSet<>(removeIndexIds);
            if (fileIds.contains("") || fileIds.contains(null) || adds.contains("") || removes.contains("")) throw new IllegalArgumentException("索引归属批次无效");
            for (String id : adds) if (removes.contains(id)) throw new IllegalArgumentException("同一索引不能同时加入和移除");
            ArrayList<String> canonicalFiles = new ArrayList<>(fileIds), canonicalAdds = new ArrayList<>(adds), canonicalRemoves = new ArrayList<>(removes);
            Collections.sort(canonicalFiles); Collections.sort(canonicalAdds); Collections.sort(canonicalRemoves);
            String requestHash = classificationSeed("index.memberships", String.join("\u0000", canonicalFiles), String.join("\u0000", canonicalAdds), String.join("\u0000", canonicalRemoves));
            try (Cursor cursor = db.rawQuery("SELECT request_hash,result_json FROM classification_requests WHERE library_id=? AND actor=? AND request_id=?", new String[]{identity[0], identity[1], requestId})) {
                if (cursor.moveToFirst()) {
                    if (!requestHash.equals(cursor.getString(0))) throw new IllegalStateException("requestIdConflict");
                    String cached = cursor.getString(1); db.setTransactionSuccessful(); return cached;
                }
            }
            for(String fileId:fileIds)try(Cursor cursor=db.rawQuery("SELECT 1 FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=? LIMIT 1",new String[]{fileId})){
                if(!cursor.moveToFirst())throw new IllegalStateException("文件不存在");
            }
            for (String id : adds) if (readIndexNode(db, identity[0], id) == null) throw new IllegalStateException("索引不存在");
            for (String id : removes) if (readIndexNode(db, identity[0], id) == null) throw new IllegalStateException("索引不存在");
            java.util.HashMap<String,String> before = new java.util.HashMap<>();
            for (String fileId : fileIds) before.put(fileId, membershipDigest(db, identity[0], fileId));
            String operationId = UUID.randomUUID().toString();
            JSONArray files = new JSONArray();
            try {
                for (String fileId : fileIds) {
                    JSONObject file = new JSONObject().put("fileId", fileId);
                    JSONArray addArray = new JSONArray();
                    for (String indexId : adds) addArray.put(new JSONObject().put("indexId", indexId).put("token", "manual:" + classificationSeed(operationId, fileId, indexId)).put("origin", "manual"));
                    file.put("adds", addArray);
                    JSONArray removeArray = new JSONArray();
                    for (String indexId : removes) {
                        JSONArray tokens = new JSONArray();
                        try (Cursor cursor = db.rawQuery("SELECT add_token FROM classification_adds WHERE library_id=? AND logical_file_id=? AND index_id=? ORDER BY add_token", new String[]{identity[0], fileId, indexId})) {
                            while (cursor.moveToNext()) tokens.put(cursor.getString(0));
                        }
                        removeArray.put(new JSONObject().put("indexId", indexId).put("observedTokens", tokens));
                    }
                    file.put("removes", removeArray);
                    files.put(file);
                }
            } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            JSONObject domain = new JSONObject();
            try { domain.put("files", files); } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            if (domain.toString().getBytes(StandardCharsets.UTF_8).length > 900 << 10) throw new IllegalStateException("单次归属变更过大，请分批操作");
            ClassificationOperations.recordOperation(db, identity[0], "index.memberships", operationId, domain);
            ClassificationOperations.replay(db, identity[0], identity[1]);
            int changed = 0; JSONObject state = new JSONObject(); String result;
            try {
                for (String fileId : fileIds) {
                    String after = membershipDigest(db, identity[0], fileId); if (!after.equals(before.get(fileId))) changed++;
                    JSONArray memberships = new JSONArray(); if (!after.isEmpty()) for (String indexId : after.split("\u001f", -1)) memberships.put(indexId);
                    state.put(fileId, memberships);
                }
                result = new JSONObject().put("changedFiles", changed).put("operationId", operationId).put("state", state).toString();
            } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
            ContentValues request = new ContentValues(); request.put("library_id", identity[0]); request.put("actor", identity[1]); request.put("request_id", requestId); request.put("request_hash", requestHash); request.put("result_json", result); request.put("created_at", Instant.now().toString());
            db.insertOrThrow("classification_requests", null, request);
            db.setTransactionSuccessful(); return result;
        } finally { db.endTransaction(); }
    }

    List<IndexNode> directIndexes(String itemUri) {
        SQLiteDatabase db = getWritableDatabase(); db.beginTransaction();
        try {
            String[] identity = classificationIdentity(db); refreshLegacyClassification(db, identity);
            String fileId; try (Cursor cursor = db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items WHERE uri=?", new String[]{itemUri})) { if (!cursor.moveToFirst()) return Collections.emptyList(); fileId = cursor.getString(0); }
            ArrayList<IndexNode> result = new ArrayList<>();
            try (Cursor cursor = db.rawQuery("SELECT index_id FROM classification_memberships WHERE library_id=? AND logical_file_id=? ORDER BY index_id", new String[]{identity[0], fileId})) { while (cursor.moveToNext()) { IndexNode node = readIndexNode(db, identity[0], cursor.getString(0)); if (node != null) { node.path=readIndexPath(db,identity[0],node); result.add(node); } } }
            db.setTransactionSuccessful(); return result;
        } finally { db.endTransaction(); }
    }

    private static void createOrganizationTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE folders (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE)");
        db.execSQL("CREATE TABLE collections (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, filters_json TEXT NOT NULL)");
    }

    private static void createOrganizationIndexes(SQLiteDatabase db) {
        db.execSQL("CREATE INDEX items_folder ON items(folder_id)");
        db.execSQL("CREATE INDEX tags_name ON tags(name)");
    }

    private static void createIndexSyncTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE IF NOT EXISTS index_outbox (file_id TEXT PRIMARY KEY, item_uri TEXT, token TEXT NOT NULL)");
        for(String trigger:new String[]{"index_items_insert","index_items_update","index_items_delete","index_tags_insert","index_tags_delete","index_folders_rename"})db.execSQL("DROP TRIGGER IF EXISTS "+trigger);
        String id="COALESCE(NULLIF(%s.logical_file_id,''),%s.file_id)";
        db.execSQL("CREATE TRIGGER index_items_insert AFTER INSERT ON items BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) VALUES("+String.format(id,"NEW","NEW")+",NEW.uri,lower(hex(randomblob(16)))); END");
        db.execSQL("CREATE TRIGGER index_items_update AFTER UPDATE OF name,path,mime,size,modified,folder_id,logical_file_id,revision_id ON items WHEN OLD.name IS NOT NEW.name OR OLD.path IS NOT NEW.path OR OLD.mime IS NOT NEW.mime OR OLD.size IS NOT NEW.size OR OLD.modified IS NOT NEW.modified OR OLD.folder_id IS NOT NEW.folder_id OR OLD.logical_file_id IS NOT NEW.logical_file_id OR OLD.revision_id IS NOT NEW.revision_id BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) VALUES("+String.format(id,"NEW","NEW")+",NEW.uri,lower(hex(randomblob(16)))); END");
        db.execSQL("CREATE TRIGGER index_items_delete AFTER DELETE ON items BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) VALUES("+String.format(id,"OLD","OLD")+",(SELECT uri FROM items WHERE "+String.format(id,"items","items")+"="+String.format(id,"OLD","OLD")+" LIMIT 1),lower(hex(randomblob(16)))); END");
        db.execSQL("CREATE TRIGGER index_tags_insert AFTER INSERT ON tags BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT "+String.format(id,"items","items")+",uri,lower(hex(randomblob(16))) FROM items WHERE uri=NEW.item_uri; END");
        db.execSQL("CREATE TRIGGER index_tags_delete AFTER DELETE ON tags BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT "+String.format(id,"items","items")+",uri,lower(hex(randomblob(16))) FROM items WHERE uri=OLD.item_uri; END");
        db.execSQL("CREATE TRIGGER index_folders_rename AFTER UPDATE OF name ON folders BEGIN INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT "+String.format(id,"items","items")+",uri,lower(hex(randomblob(16))) FROM items WHERE folder_id=NEW.id; END");
        db.execSQL("INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT COALESCE(NULLIF(logical_file_id,''),file_id),uri,lower(hex(randomblob(16))) FROM items");
    }

    private static void createOperationSyncTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operation_identity (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operation_authors (author_id TEXT PRIMARY KEY, next_counter INTEGER NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operations (op_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, kind TEXT NOT NULL, payload_json TEXT NOT NULL, created_at TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending', UNIQUE(author_id,counter))");
        db.execSQL("CREATE INDEX IF NOT EXISTS library_operations_pending ON library_operations(state,author_id,counter)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_tag_add_tokens (file_id TEXT NOT NULL, tag_name TEXT NOT NULL, token TEXT NOT NULL, PRIMARY KEY(file_id,tag_name,token))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_tag_remove_tokens (file_id TEXT NOT NULL, tag_name TEXT NOT NULL, token TEXT NOT NULL, PRIMARY KEY(file_id,tag_name,token))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operation_inbox (op_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, received_at TEXT NOT NULL, applied_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operation_quarantine (op_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, kind TEXT NOT NULL, reason TEXT NOT NULL, seen_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_operation_frontiers (author_id TEXT PRIMARY KEY, last_counter INTEGER NOT NULL, updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_receipts (node_id TEXT PRIMARY KEY, frontiers_json TEXT NOT NULL, catalog_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_members (node_id TEXT PRIMARY KEY, epoch INTEGER NOT NULL, state TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_folder_state (file_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, folder_id TEXT NOT NULL, updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_name_state (file_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, current_name TEXT NOT NULL, context_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_name_conflicts (file_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, file_name TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(file_id,author_id,counter))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_current_revision (file_id TEXT PRIMARY KEY, revision_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, context_json TEXT NOT NULL DEFAULT '{}', updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_revision_conflicts (file_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, revision_id TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(file_id,author_id,counter))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_folder_conflicts (file_id TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, folder_name TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(file_id,author_id,counter,op_id))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_folder_tombstones (name TEXT PRIMARY KEY COLLATE NOCASE, author_id TEXT NOT NULL, counter INTEGER NOT NULL, deleted_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_folder_rename_state (anchor TEXT PRIMARY KEY COLLATE NOCASE, author_id TEXT NOT NULL, counter INTEGER NOT NULL, current_name TEXT NOT NULL, updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_folder_rename_conflicts (anchor TEXT NOT NULL COLLATE NOCASE, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, folder_name TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(anchor,author_id,counter))");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_lifecycle (file_id TEXT PRIMARY KEY, state TEXT NOT NULL, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, context_json TEXT NOT NULL, updated_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE IF NOT EXISTS library_file_deletions (file_id TEXT PRIMARY KEY, author_id TEXT NOT NULL, counter INTEGER NOT NULL, op_id TEXT NOT NULL, deleted_at TEXT NOT NULL)");
    }

    private static void createAliasTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE item_aliases (uri TEXT PRIMARY KEY, file_id TEXT NOT NULL)");
        db.execSQL("CREATE INDEX item_aliases_file_id ON item_aliases(file_id)");
        db.execSQL("CREATE TRIGGER item_aliases_insert AFTER INSERT ON items BEGIN INSERT OR REPLACE INTO item_aliases(uri,file_id) VALUES(NEW.uri,NEW.file_id); END");
        db.execSQL("INSERT OR REPLACE INTO item_aliases(uri,file_id) SELECT uri,file_id FROM items");
    }

    private static String kindFor(String name) {
        int dot = name.lastIndexOf('.');
        if (dot < 0) return "file";
        String ext = name.substring(dot + 1).toLowerCase(java.util.Locale.ROOT);
        switch (ext) {
            case "jpg": case "jpeg": case "png": case "gif": case "webp": case "bmp": case "avif": case "heic": case "arw": return "image";
            case "mp4": case "mov": case "mkv": case "avi": case "webm": case "m4v": return "video";
            case "mp3": case "wav": case "flac": case "m4a": case "aac": case "ogg": return "audio";
            case "pdf": return "document";
            default: return "file";
        }
    }

    void addRoot(Uri uri, String name) {
        ContentValues values = new ContentValues();
        values.put("uri", uri.toString());
        values.put("name", name);
        getWritableDatabase().insertWithOnConflict("roots", null, values, SQLiteDatabase.CONFLICT_IGNORE);
    }

    void setRootPreserveStructure(String uri, boolean enabled) {
        ContentValues values = new ContentValues();
        values.put("preserve_structure", enabled ? 1 : 0);
        getWritableDatabase().update("roots", values, "uri=?", new String[]{uri});
    }

    boolean rootPreservesStructure(SQLiteDatabase db, String uri) {
        try (Cursor cursor = db.rawQuery("SELECT preserve_structure FROM roots WHERE uri=?", new String[]{uri})) {
            return cursor.moveToFirst() && cursor.getInt(0) != 0;
        }
    }

    void removeRoot(String uri) {
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            db.delete("roots", "uri=?", new String[]{uri});
            try (Cursor cursor = db.rawQuery("SELECT id, filters_json FROM collections", null)) {
                while (cursor.moveToNext()) {
                    LocalFilters filters = LocalFilters.parse(cursor.getString(1));
                    if (!uri.equals(filters.rootUri)) continue;
                    filters.rootUri = "";
                    ContentValues values = new ContentValues();
                    values.put("filters_json", filters.serialize());
                    db.update("collections", values, "id=?", new String[]{cursor.getString(0)});
                }
            }
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    List<Root> roots() {
        List<Root> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT r.uri, r.name, COUNT(i.uri),r.preserve_structure FROM roots r LEFT JOIN items i ON i.root_uri=r.uri AND NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)) GROUP BY r.uri ORDER BY r.name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Root(cursor.getString(0), cursor.getString(1), cursor.getInt(2), cursor.getInt(3) != 0));
        }
        return result;
    }

    Page search(LocalFilters filters, int limit, int offset) {
        ArrayList<String> args = new ArrayList<>();
        String where = where(filters, args);
        SQLiteDatabase db = getReadableDatabase();
        int total;
        try (Cursor cursor = db.rawQuery("SELECT COUNT(*) FROM items i WHERE " + where, args.toArray(new String[0]))) {
            cursor.moveToFirst();
            total = cursor.getInt(0);
        }
        String order;
        switch (filters.sort) {
            case "modified_asc": order = "i.modified ASC, i.uri ASC"; break;
            case "name_asc": order = "COALESCE((SELECT current_name FROM library_file_name_state n WHERE n.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)),i.name) COLLATE NOCASE ASC, i.uri ASC"; break;
            case "name_desc": order = "COALESCE((SELECT current_name FROM library_file_name_state n WHERE n.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)),i.name) COLLATE NOCASE DESC, i.uri ASC"; break;
            case "size_desc": order = "i.size DESC, i.uri ASC"; break;
            default: order = "i.modified DESC, i.uri DESC";
        }
        args.add(String.valueOf(limit));
        args.add(String.valueOf(offset));
        List<Item> items = new ArrayList<>();
        try (Cursor cursor = db.rawQuery(
                "SELECT i.uri, i.root_uri, COALESCE((SELECT current_name FROM library_file_name_state n WHERE n.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)),i.name), i.path, i.mime, i.size, i.modified, " +
                "COALESCE((SELECT group_concat(t.name, ' · ') FROM tags t WHERE t.item_uri=i.uri), ''), i.folder_id, " +
                "i.file_id, COALESCE(NULLIF(i.logical_file_id,''),i.file_id), i.revision_id FROM items i WHERE " + where + " ORDER BY " + order + " LIMIT ? OFFSET ?", args.toArray(new String[0]))) {
            while (cursor.moveToNext()) items.add(new Item(cursor));
        }
        return new Page(items, total);
    }

    Item item(String uri) {
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT i.uri, i.root_uri, COALESCE((SELECT current_name FROM library_file_name_state n WHERE n.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)),i.name), i.path, i.mime, i.size, i.modified, " +
                "COALESCE((SELECT group_concat(t.name, ' · ') FROM tags t WHERE t.item_uri=i.uri), ''), i.folder_id, " +
                "i.file_id, COALESCE(NULLIF(i.logical_file_id,''),i.file_id), i.revision_id FROM items i WHERE i.uri=? AND NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id))", new String[]{uri})) {
            return cursor.moveToFirst() ? new Item(cursor) : null;
        }
    }

    private static String where(LocalFilters filters, ArrayList<String> args) {
        ArrayList<String> clauses = new ArrayList<>();
        clauses.add("NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id))");
        String query = filters.query.trim();
        if (!query.isEmpty()) {
            String needle = "%" + escapeLike(query) + "%";
            clauses.add("(COALESCE((SELECT current_name FROM library_file_name_state n WHERE n.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)),i.name) LIKE ? ESCAPE '\\' OR i.path LIKE ? ESCAPE '\\' OR EXISTS (SELECT 1 FROM tags t WHERE t.item_uri=i.uri AND t.name LIKE ? ESCAPE '\\'))");
            Collections.addAll(args, needle, needle, needle);
        }
        for (String tag : filters.tags) {
            clauses.add("EXISTS (SELECT 1 FROM tags t WHERE t.item_uri=i.uri AND t.name=?)");
            args.add(tag);
        }
        if (filters.unorganized) clauses.add("NOT EXISTS (SELECT 1 FROM tags t WHERE t.item_uri=i.uri)");
        if (!filters.rootUri.isEmpty()) { clauses.add("i.root_uri=?"); args.add(filters.rootUri); }
        if ("unfiled".equals(filters.folderId)) clauses.add("i.folder_id IS NULL");
        else if (!"all".equals(filters.folderId)) { clauses.add("i.folder_id=?"); args.add(filters.folderId); }
        ArrayList<String> types = new ArrayList<>();
        ArrayList<String> sizes = new ArrayList<>();
        ArrayList<String> extensions = new ArrayList<>();
        for (String key : filters.attributes) {
            if (key.startsWith("type:")) types.add(key.substring(5));
            else if (key.startsWith("size:")) sizes.add(key.substring(5));
            else if (key.startsWith("extension:")) extensions.add(key.substring(10));
        }
        if (!types.isEmpty()) {
            clauses.add("i.kind IN (" + placeholders(types.size()) + ")");
            args.addAll(types);
        }
        if (!sizes.isEmpty()) {
            ArrayList<String> parts = new ArrayList<>();
            for (String size : sizes) {
                switch (size) {
                    case "lt1m": parts.add("i.size < 1048576"); break;
                    case "1to10m": parts.add("i.size >= 1048576 AND i.size < 10485760"); break;
                    case "10to100m": parts.add("i.size >= 10485760 AND i.size < 104857600"); break;
                    case "gte100m": parts.add("i.size >= 104857600"); break;
                }
            }
            if (!parts.isEmpty()) clauses.add("((" + String.join(") OR (", parts) + "))");
        }
        if (!extensions.isEmpty()) {
            ArrayList<String> parts = new ArrayList<>();
            for (String extension : extensions) {
                parts.add("LOWER(i.name) LIKE ?");
                args.add("%." + extension);
            }
            clauses.add("(" + String.join(" OR ", parts) + ")");
        }
        return clauses.isEmpty() ? "1=1" : String.join(" AND ", clauses);
    }

    private static String escapeLike(String value) {
        return value.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_");
    }

    private static String placeholders(int count) {
        return String.join(",", Collections.nCopies(count, "?"));
    }

    List<String> tags(String itemUri) {
        List<String> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT name FROM tags WHERE item_uri=? ORDER BY name COLLATE NOCASE", new String[]{itemUri})) {
            while (cursor.moveToNext()) result.add(cursor.getString(0));
        }
        return result;
    }

    List<Attribute> tagCatalog(String folderId) {
        List<Attribute> result = new ArrayList<>();
        String active = " NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id))";
        String scope = "unfiled".equals(folderId) ? " WHERE"+active+" AND i.folder_id IS NULL" : "all".equals(folderId) ? " WHERE"+active : " WHERE"+active+" AND i.folder_id=?";
        String[] args = scope.endsWith("=?") ? new String[]{folderId} : null;
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT t.name, COUNT(*) FROM tags t JOIN items i ON i.uri=t.item_uri" + scope +
                " GROUP BY t.name ORDER BY t.name COLLATE NOCASE", args)) {
            while (cursor.moveToNext()) result.add(new Attribute(cursor.getString(0), cursor.getString(0), cursor.getInt(1)));
        }
        return result;
    }

    List<Attribute> attributeCatalog(LocalFilters filters) {
        java.util.LinkedHashMap<String, Integer> counts = new java.util.LinkedHashMap<>();
        for (String category : new String[]{"type", "size", "extension"}) {
            LocalFilters scope = filters.copy();
            scope.attributes.removeIf(key -> key.startsWith(category + ":"));
            ArrayList<String> args = new ArrayList<>();
            String clause = where(scope, args);
            try (Cursor cursor = getReadableDatabase().rawQuery("SELECT i.name, i.kind, i.size FROM items i WHERE " + clause, args.toArray(new String[0]))) {
                while (cursor.moveToNext()) {
                    if (category.equals("type")) addCount(counts, "type:" + cursor.getString(1));
                    else if (category.equals("size")) {
                        long size = cursor.getLong(2);
                        addCount(counts, "size:" + (size < 1048576 ? "lt1m" : size < 10485760 ? "1to10m" : size < 104857600 ? "10to100m" : "gte100m"));
                    } else {
                        String name = cursor.getString(0);
                        int dot = name.lastIndexOf('.');
                        if (dot > 0 && dot < name.length() - 1) {
                            String ext = name.substring(dot + 1).toLowerCase(java.util.Locale.ROOT);
                            if (ext.length() <= 16 && ext.matches("[a-z0-9]+")) addCount(counts, "extension:" + ext);
                        }
                    }
                }
            }
        }
        for (String key : filters.attributes) if (!counts.containsKey(key)) counts.put(key, 0);
        List<Attribute> result = new ArrayList<>();
        for (String key : new String[]{"type:image", "type:video", "type:audio", "type:document", "type:file", "size:lt1m", "size:1to10m", "size:10to100m", "size:gte100m"}) {
            if (counts.containsKey(key)) result.add(new Attribute(key, attributeLabel(key), counts.get(key)));
        }
        ArrayList<String> formats = new ArrayList<>();
        for (String key : counts.keySet()) if (key.startsWith("extension:")) formats.add(key);
        Collections.sort(formats);
        for (String key : formats) result.add(new Attribute(key, attributeLabel(key), counts.get(key)));
        return result;
    }

    private static void addCount(java.util.Map<String, Integer> counts, String key) {
        counts.put(key, counts.containsKey(key) ? counts.get(key) + 1 : 1);
    }

    static String attributeLabel(String key) {
        switch (key) {
            case "type:image": return "图片";
            case "type:video": return "视频";
            case "type:audio": return "音频";
            case "type:document": return "文档";
            case "type:file": return "其他文件";
            case "size:lt1m": return "小于 1 MB";
            case "size:1to10m": return "1–10 MB";
            case "size:10to100m": return "10–100 MB";
            case "size:gte100m": return "100 MB 以上";
            default: return key.startsWith("extension:") ? key.substring(10).toUpperCase(java.util.Locale.ROOT) : key;
        }
    }

    List<Folder> folders() {
        List<Folder> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT f.id, f.name, COUNT(i.uri) FROM folders f LEFT JOIN items i ON i.folder_id=f.id AND NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)) GROUP BY f.id ORDER BY f.name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Folder(cursor));
        }
        return result;
    }

    int folderConflictCount() {
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT COUNT(*) FROM library_folder_conflicts", null)) {
            return cursor.moveToFirst() ? cursor.getInt(0) : 0;
        }
    }

    List<FolderConflict> folderConflicts() {
        List<FolderConflict> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT file_id,folder_name,author_id,counter FROM library_folder_conflicts ORDER BY created_at,file_id", null)) {
            while (cursor.moveToNext()) result.add(new FolderConflict(cursor));
        }
        return result;
    }

    int renameConflictCount() {
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT COUNT(DISTINCT anchor) FROM library_folder_rename_conflicts", null)) {
            return cursor.moveToFirst() ? cursor.getInt(0) : 0;
        }
    }

    int fileNameConflictCount(){
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT COUNT(DISTINCT file_id) FROM library_file_name_conflicts",null)){return cursor.moveToFirst()?cursor.getInt(0):0;}
    }

    int revisionConflictCount(){try(Cursor cursor=getReadableDatabase().rawQuery("SELECT COUNT(DISTINCT file_id) FROM library_file_revision_conflicts",null)){return cursor.moveToFirst()?cursor.getInt(0):0;}}
    List<String[]> revisionConflicts(){List<String[]> result=new ArrayList<>();try(Cursor cursor=getReadableDatabase().rawQuery("SELECT c.file_id,c.revision_id,COALESCE(s.revision_id,''),c.author_id,c.counter FROM library_file_revision_conflicts c LEFT JOIN library_file_current_revision s ON s.file_id=c.file_id ORDER BY c.file_id,c.created_at,c.author_id,c.counter",null)){while(cursor.moveToNext())result.add(new String[]{cursor.getString(0),cursor.getString(1),cursor.getString(2),cursor.getString(3),Long.toString(cursor.getLong(4))});}return result;}

    void selectCurrentRevision(String fileId,String revisionId,boolean resolve){
        if(fileId.isEmpty()||fileId.length()>160||revisionId.isEmpty()||revisionId.length()>160)throw new IllegalArgumentException("版本选择无效");
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            if(!resolve)try(Cursor conflicts=db.rawQuery("SELECT 1 FROM library_file_revision_conflicts WHERE file_id=? LIMIT 1",new String[]{fileId})){if(conflicts.moveToFirst())throw new IllegalStateException("请先处理并发版本选择");}
            recordLibraryOperation(db,"file.version.select","{\"fileIds\":["+JSONObject.quote(fileId)+"],\"revisionId\":"+JSONObject.quote(revisionId)+",\"resolve\":"+resolve+"}");
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    List<String[]> fileNameConflicts(){
        List<String[]> result=new ArrayList<>();
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT c.file_id,c.file_name,COALESCE(s.current_name,''),c.author_id,c.counter FROM library_file_name_conflicts c LEFT JOIN library_file_name_state s ON s.file_id=c.file_id ORDER BY c.file_id,c.created_at,c.author_id,c.counter",null)){
            while(cursor.moveToNext())result.add(new String[]{cursor.getString(0),cursor.getString(1),cursor.getString(2),cursor.getString(3),Long.toString(cursor.getLong(4))});
        }
        return result;
    }

    void renameLogicalFile(String fileId,String name,boolean resolve){
        String cleaned=name.trim();
        if(fileId.isEmpty()||fileId.length()>160||cleaned.isEmpty()||cleaned.length()>255||cleaned.equals(".")||cleaned.equals("..")||cleaned.indexOf('/')>=0||cleaned.indexOf('\\')>=0||cleaned.indexOf('\n')>=0||cleaned.indexOf('\r')>=0||cleaned.indexOf('\0')>=0)throw new IllegalArgumentException("文件名称无效");
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            if(!resolve)try(Cursor conflicts=db.rawQuery("SELECT 1 FROM library_file_name_conflicts WHERE file_id=? LIMIT 1",new String[]{fileId})){if(conflicts.moveToFirst())throw new IllegalStateException("请先处理并发改名");}
            recordLibraryOperation(db,"file.rename","{\"fileIds\":["+JSONObject.quote(fileId)+"],\"fileName\":"+JSONObject.quote(cleaned)+",\"resolve\":"+resolve+"}");
            db.execSQL("INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT ?,uri,lower(hex(randomblob(16))) FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=? LIMIT 1",new Object[]{fileId,fileId});
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    // appliedFrontierCount reports how many operations THIS device has actually
    // materialized (§5.1). It is a local receipt only; it never implies other
    // nodes applied the same operations, so callers must not render it as
    // "所有设备已同步".
    int appliedFrontierCount() {
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT COUNT(*) FROM library_operation_inbox", null)) {
            return cursor.moveToFirst() ? cursor.getInt(0) : 0;
        }
    }

    JSONObject appliedFrontiers() {
        JSONObject result = new JSONObject();
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT author_id,last_counter FROM library_operation_frontiers", null)) {
            while (cursor.moveToNext()) try { result.put(cursor.getString(0), cursor.getLong(1)); } catch (org.json.JSONException failure) { throw new IllegalStateException(failure); }
        }
        return result;
    }

    static final class RemoteReceipt {
        final String node, frontiers, catalogAt;
        RemoteReceipt(String node,String frontiers,String catalogAt){this.node=node;this.frontiers=frontiers;this.catalogAt=catalogAt;}
    }

    void applyReceipts(List<RemoteReceipt> receipts) {
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            for(RemoteReceipt receipt:receipts){
                if(receipt.node.isEmpty()||receipt.frontiers.length()>1<<20)continue;
                Instant incoming;try{incoming=Instant.parse(receipt.catalogAt);}catch(Exception invalid){continue;}
                try{
                    JSONObject merged=new JSONObject(receipt.frontiers);
                    String stamp=receipt.catalogAt;
                    try(Cursor previous=db.rawQuery("SELECT frontiers_json,catalog_at FROM library_receipts WHERE node_id=?",new String[]{receipt.node})){
                        if(previous.moveToFirst()){
                            JSONObject old=new JSONObject(previous.getString(0));java.util.Iterator<String> names=old.keys();
                            while(names.hasNext()){String author=names.next();long count=old.optLong(author,0);if(count>merged.optLong(author,0))merged.put(author,count);}
                            try{if(Instant.parse(previous.getString(1)).isAfter(incoming))stamp=previous.getString(1);}catch(Exception ignored){}
                        }
                    }
                    db.execSQL("INSERT INTO library_receipts(node_id,frontiers_json,catalog_at) VALUES(?,?,?) ON CONFLICT(node_id) DO UPDATE SET frontiers_json=excluded.frontiers_json,catalog_at=excluded.catalog_at",new Object[]{receipt.node,merged.toString(),stamp});
                    db.execSQL("INSERT OR IGNORE INTO library_members(node_id,epoch,state,author_id,counter,updated_at) VALUES(?,1,'active','',0,?)",new Object[]{receipt.node,Instant.now().toString()});
                }catch(Exception invalid){/* Malformed receipts cannot change applied frontiers. */}
            }
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    int remoteReceiptCount() {
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT COUNT(*) FROM library_receipts",null)){return cursor.moveToFirst()?cursor.getInt(0):0;}
    }

    boolean retiredMember(String node){
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT state FROM library_members WHERE node_id=?",new String[]{node})){
            return cursor.moveToFirst()&&"retired".equals(cursor.getString(0));
        }
    }

    long memberEpoch(String node){
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT epoch FROM library_members WHERE node_id=? AND state='active'",new String[]{node})){
            return cursor.moveToFirst()?cursor.getLong(0):0;
        }
    }

    long rejoinEpoch(String node){
        SQLiteDatabase db=getReadableDatabase();long epoch=0;String state="";
        try(Cursor row=db.rawQuery("SELECT epoch,state FROM library_members WHERE node_id=?",new String[]{node})){if(row.moveToFirst()){epoch=row.getLong(0);state=row.getString(1);}}
        if(!"retired".equals(state))throw new IllegalStateException("当前节点未退役");
        try(Cursor row=db.rawQuery("SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed')",null)){if(row.moveToFirst()&&row.getInt(0)>0)throw new IllegalStateException("旧作者仍有待发布操作，请先处理");}
        return epoch+1;
    }

    void rejoinMember(String node,long expectedEpoch){
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            long epoch=0;String state="";
            try(Cursor row=db.rawQuery("SELECT epoch,state FROM library_members WHERE node_id=?",new String[]{node})){if(row.moveToFirst()){epoch=row.getLong(0);state=row.getString(1);}}
            if(!"retired".equals(state))throw new IllegalStateException("当前节点未退役");
            if(expectedEpoch!=epoch+1)throw new IllegalStateException("成员 epoch 已变化，请重试");
            try(Cursor row=db.rawQuery("SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed')",null)){if(row.moveToFirst()&&row.getInt(0)>0)throw new IllegalStateException("旧作者仍有待发布操作，请先处理");}
            db.execSQL("INSERT INTO library_operation_identity(key,value) VALUES('author',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",new Object[]{"android-"+UUID.randomUUID()});
            recordLibraryOperation(db,"member.join","{\"nodeId\":"+JSONObject.quote(node)+",\"epoch\":"+expectedEpoch+"}");
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    List<String[]> remoteReceipts() {
        List<String[]> result=new ArrayList<>();
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT node_id,frontiers_json,catalog_at FROM library_receipts ORDER BY node_id",null)){
            while(cursor.moveToNext())result.add(new String[]{cursor.getString(0),cursor.getString(1),cursor.getString(2)});
        }
        return result;
    }

    List<RenameConflict> renameConflicts() {
        List<RenameConflict> result = new ArrayList<>();
        SQLiteDatabase db = getReadableDatabase();
        try (Cursor cursor = db.rawQuery("SELECT anchor,folder_name,author_id,counter FROM library_folder_rename_conflicts ORDER BY anchor,created_at,author_id,counter", null)) {
            while (cursor.moveToNext()) {
                String anchor = cursor.getString(0);
                String applied = anchor;
                try (Cursor state = db.rawQuery("SELECT current_name FROM library_folder_rename_state WHERE anchor=?", new String[]{anchor})) {
                    if (state.moveToFirst()) applied = state.getString(0);
                }
                result.add(new RenameConflict(new String[]{anchor, cursor.getString(1), applied, cursor.getString(2), Long.toString(cursor.getLong(3))}));
            }
        }
        return result;
    }

    private static List<String> logicalFileIds(SQLiteDatabase db, List<String> uris) {
        LinkedHashSet<String> ids = new LinkedHashSet<>();
        for (String uri : uris) {
            try (Cursor cursor = db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items WHERE uri=?", new String[]{uri})) {
                if (cursor.moveToFirst()) ids.add(cursor.getString(0));
            }
        }
        return new ArrayList<>(ids);
    }

    private static String jsonArray(List<String> values) {
        StringBuilder out = new StringBuilder("[");
        for (String value : values) {
            if (out.length() > 1) out.append(',');
            out.append(JSONObject.quote(value));
        }
        return out.append(']').toString();
    }

    private static String operationAuthor(SQLiteDatabase db) {
        try (Cursor cursor = db.rawQuery("SELECT value FROM library_operation_identity WHERE key='author'", null)) {
            if (cursor.moveToFirst()) return cursor.getString(0);
        }
        String author = "android-" + UUID.randomUUID();
        ContentValues value = new ContentValues();
        value.put("key", "author");
        value.put("value", author);
        db.insertOrThrow("library_operation_identity", null, value);
        return author;
    }

    // Caller owns the surrounding transaction, so the visible classification
    // update and its durable cross-device operation cannot be separated.
    private static void recordLibraryOperation(SQLiteDatabase db, String kind, String fields) {
        if (legacyClassificationKind(kind) && classificationActive(db)) throw new IllegalStateException("classificationUpgradeRequired");
        String author = operationAuthor(db);
        long counter = 1;
        try (Cursor cursor = db.rawQuery("SELECT next_counter FROM library_operation_authors WHERE author_id=?", new String[]{author})) {
            if (cursor.moveToFirst()) counter = cursor.getLong(0);
        }
        ContentValues sequence = new ContentValues();
        sequence.put("author_id", author);
        sequence.put("next_counter", counter + 1);
        db.insertWithOnConflict("library_operation_authors", null, sequence, SQLiteDatabase.CONFLICT_REPLACE);
        String payload = "{\"schemaVersion\":1,\"author\":" + JSONObject.quote(author) + ",\"counter\":" + counter + ",\"context\":" + observedFrontiers(db) + fields.substring(1);
        ContentValues operation = new ContentValues();
        operation.put("op_id", UUID.randomUUID().toString());
        operation.put("author_id", author);
        operation.put("counter", counter);
        operation.put("kind", kind);
        operation.put("payload_json", payload);
        operation.put("created_at", Instant.now().toString());
        operation.put("state", "pending");
        db.insertOrThrow("library_operations", null, operation);
        if("file.trash".equals(kind)||"file.restore".equals(kind)||"file.delete".equals(kind)||"file.rename".equals(kind)||"file.version.select".equals(kind)||"member.retire".equals(kind)||"member.join".equals(kind)){
            try{materializeRemoteOperation(db,new RemoteOperation(operation.getAsString("op_id"),author,counter,kind,payload));}
            catch(Exception failure){throw new IllegalStateException(failure);}
        }
        if("folder.delete".equals(kind)||"folder.create".equals(kind)){
            try{
                String name=new JSONObject(fields).optString("folderName","").trim();
                if(!name.isEmpty()){
                    if("folder.delete".equals(kind))db.execSQL("INSERT INTO library_folder_tombstones(name,author_id,counter,deleted_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,deleted_at=excluded.deleted_at",new Object[]{name,author,counter,Instant.now().toString()});
                    else db.delete("library_folder_tombstones","name=? COLLATE NOCASE",new String[]{name});
                }
            }catch(org.json.JSONException failure){throw new IllegalStateException(failure);}
        }
    }

    // observedFrontiers records the per-author counters this device has already
    // applied, so a receiving node can tell a later move from a concurrent one
    // instead of treating every Android operation as uncontextualized legacy.
    private static String observedFrontiers(SQLiteDatabase db) {
        StringBuilder context = new StringBuilder("{");
        try (Cursor cursor = db.rawQuery("SELECT author_id,last_counter FROM library_operation_frontiers", null)) {
            while (cursor.moveToNext()) {
                if (context.length() > 1) context.append(',');
                context.append(JSONObject.quote(cursor.getString(0))).append(':').append(cursor.getLong(1));
            }
        }
        return context.append('}').toString();
    }

    private static List<String> observedTagTokens(SQLiteDatabase db, List<String> fileIds, String tag) {
        LinkedHashSet<String> tokens = new LinkedHashSet<>();
        for (String fileId : fileIds) {
            try (Cursor cursor = db.rawQuery("SELECT token FROM library_tag_add_tokens WHERE file_id=? AND tag_name=?", new String[]{fileId, tag})) {
                while (cursor.moveToNext()) tokens.add(cursor.getString(0));
            }
        }
        return new ArrayList<>(tokens);
    }

    static final class RemoteOperation {
        final String id, author, kind, payload;
        final long counter;
        RemoteOperation(String id, String author, long counter, String kind, String payload) {
            this.id = id; this.author = author; this.counter = counter; this.kind = kind; this.payload = payload;
        }
    }

    private static void recordLocalFolderMoveState(SQLiteDatabase db, List<String> fileIds, String folderId) {
        String author = operationAuthor(db);
        long counter = 0;
        try (Cursor cursor = db.rawQuery("SELECT next_counter-1 FROM library_operation_authors WHERE author_id=?", new String[]{author})) {
            if (cursor.moveToFirst()) counter = cursor.getLong(0);
        }
        if (counter < 1) return;
        for (String fileId : fileIds) {
            ContentValues state = new ContentValues();
            state.put("file_id", fileId); state.put("author_id", author); state.put("counter", counter);
            state.put("folder_id", folderId); state.put("updated_at", Instant.now().toString());
            db.insertWithOnConflict("library_file_folder_state", null, state, SQLiteDatabase.CONFLICT_REPLACE);
        }
    }

    private static void recordLocalFolderRenameState(SQLiteDatabase db, String previous, String name) {
        String author = operationAuthor(db);
        long counter = 0;
        try (Cursor cursor = db.rawQuery("SELECT next_counter-1 FROM library_operation_authors WHERE author_id=?", new String[]{author})) {
            if (cursor.moveToFirst()) counter = cursor.getLong(0);
        }
        if (counter < 1 || previous.isEmpty() || name.isEmpty()) return;
        ContentValues state = new ContentValues();
        state.put("anchor", previous); state.put("author_id", author); state.put("counter", counter);
        state.put("current_name", name); state.put("updated_at", Instant.now().toString());
        db.insertWithOnConflict("library_folder_rename_state", null, state, SQLiteDatabase.CONFLICT_REPLACE);
    }

    // Operations are applied in author-counter order. A missing predecessor is
    // retained for a later catalog sync instead of being applied out of order.
    void applyRemoteOperations(List<RemoteOperation> operations) throws Exception {
        Collections.sort(operations, (left, right) -> left.author.equals(right.author) ? Long.compare(left.counter, right.counter) : left.author.compareTo(right.author));
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String localAuthor = operationAuthor(db);
            String localClassificationAuthor = ClassificationOperations.classificationAuthor(db);
            String[] classificationIdentity = classificationIdentity(db);
            boolean classificationActive = classificationActive(db);
            for (RemoteOperation operation : operations) {
                if (operation.id.isEmpty() || operation.author.isEmpty() || operation.kind.isEmpty() || operation.counter < 1 || operation.payload.length() > 1 << 20) continue;
                boolean classificationOperation = ClassificationOperations.knownKind(operation.kind);
                if(!knownOperationKind(operation.kind) && !classificationOperation){
                    ContentValues quarantine=new ContentValues();quarantine.put("op_id",operation.id);quarantine.put("author_id",operation.author);quarantine.put("counter",operation.counter);quarantine.put("kind",operation.kind);quarantine.put("reason","不支持的整理操作，请升级客户端");quarantine.put("seen_at",Instant.now().toString());
                    db.insertWithOnConflict("library_operation_quarantine",null,quarantine,SQLiteDatabase.CONFLICT_IGNORE);
                    continue;
                }
                db.delete("library_operation_quarantine","op_id=?",new String[]{operation.id});
                long frontier = 0;
                try (Cursor cursor = db.rawQuery("SELECT last_counter FROM library_operation_frontiers WHERE author_id=?", new String[]{operation.author})) {
                    if (cursor.moveToFirst()) frontier = cursor.getLong(0);
                }
                if(operation.counter<=frontier){
                    String accepted="";
                    try(Cursor cursor=db.rawQuery("SELECT op_id FROM library_operation_inbox WHERE author_id=? AND counter=? LIMIT 1",new String[]{operation.author,Long.toString(operation.counter)})){if(cursor.moveToFirst())accepted=cursor.getString(0);}
                    if(accepted.isEmpty())try(Cursor cursor=db.rawQuery("SELECT op_id FROM library_operations WHERE author_id=? AND counter=? LIMIT 1",new String[]{operation.author,Long.toString(operation.counter)})){if(cursor.moveToFirst())accepted=cursor.getString(0);}
                    if(!accepted.isEmpty()&&!accepted.equals(operation.id)){
                        ContentValues quarantine=new ContentValues();quarantine.put("op_id",operation.id);quarantine.put("author_id",operation.author);quarantine.put("counter",operation.counter);quarantine.put("kind",operation.kind);quarantine.put("reason","作者序号对应不同操作");quarantine.put("seen_at",Instant.now().toString());
                        db.insertWithOnConflict("library_operation_quarantine",null,quarantine,SQLiteDatabase.CONFLICT_IGNORE);
                        continue;
                    }
                }
                if (operation.counter > frontier + 1) continue;
                if (operation.counter == frontier + 1) {
                    if (classificationOperation) {
                        if (!classificationActive) continue;
                        try {
                            ClassificationOperations.receiveOperation(db, classificationIdentity[0], classificationIdentity[1], operation.id, operation.author, operation.counter, operation.kind, operation.payload);
                        } catch (ClassificationOperations.PendingException pending) {
                            continue;
                        } catch (ClassificationOperations.QuarantineException rejected) {
                            ContentValues quarantine=new ContentValues();quarantine.put("op_id",operation.id);quarantine.put("author_id",operation.author);quarantine.put("counter",operation.counter);quarantine.put("kind",operation.kind);quarantine.put("reason",rejected.getMessage());quarantine.put("seen_at",Instant.now().toString());
                            db.insertWithOnConflict("library_operation_quarantine",null,quarantine,SQLiteDatabase.CONFLICT_IGNORE);
                            continue;
                        }
                    } else if (classificationActive && legacyClassificationKind(operation.kind)) {
                        ContentValues quarantine=new ContentValues();quarantine.put("op_id",operation.id);quarantine.put("author_id",operation.author);quarantine.put("counter",operation.counter);quarantine.put("kind",operation.kind);quarantine.put("reason","旧分类操作不受支持");quarantine.put("seen_at",Instant.now().toString());
                        db.insertWithOnConflict("library_operation_quarantine",null,quarantine,SQLiteDatabase.CONFLICT_IGNORE);
                        continue;
                    } else {
                        materializeRemoteOperation(db, operation);
                    }
                    ContentValues inbox = new ContentValues();
                    inbox.put("op_id", operation.id); inbox.put("author_id", operation.author); inbox.put("counter", operation.counter);
                    inbox.put("received_at", Instant.now().toString()); inbox.put("applied_at", Instant.now().toString());
                    db.insertWithOnConflict("library_operation_inbox", null, inbox, SQLiteDatabase.CONFLICT_IGNORE);
                    ContentValues next = new ContentValues();
                    next.put("author_id", operation.author); next.put("last_counter", operation.counter); next.put("updated_at", Instant.now().toString());
                    db.insertWithOnConflict("library_operation_frontiers", null, next, SQLiteDatabase.CONFLICT_REPLACE);
                }
                if (localAuthor.equals(operation.author) || localClassificationAuthor.equals(operation.author)) {
                    ContentValues acknowledged = new ContentValues(); acknowledged.put("state", "acknowledged");
                    db.update("library_operations", acknowledged, "op_id=?", new String[]{operation.id});
                }
            }
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    private static boolean knownOperationKind(String kind){
        return "folder.create".equals(kind)||"folder.rename".equals(kind)||"folder.delete".equals(kind)||"folder.move".equals(kind)||"tag.add".equals(kind)||"tag.remove".equals(kind)||"file.trash".equals(kind)||"file.restore".equals(kind)||"file.delete".equals(kind)||"file.rename".equals(kind)||"file.version.select".equals(kind)||"member.retire".equals(kind)||"member.join".equals(kind);
    }

    int quarantinedOperationCount(){try(Cursor cursor=getReadableDatabase().rawQuery("SELECT COUNT(*) FROM library_operation_quarantine",null)){return cursor.moveToFirst()?cursor.getInt(0):0;}}

    private static List<String> operationFileIds(org.json.JSONArray values) {
        LinkedHashSet<String> ids = new LinkedHashSet<>();
        if (values != null) for (int index = 0; index < values.length(); index++) {
            String fileId = values.optString(index, "").trim();
            if (!fileId.isEmpty() && fileId.length() <= 160) ids.add(fileId);
        }
        return new ArrayList<>(ids);
    }

    private static boolean operationBefore(String author,long counter,String id,String otherAuthor,long otherCounter,String otherId){
        int compared=author.compareTo(otherAuthor);if(compared!=0)return compared<0;
        if(counter!=otherCounter)return counter<otherCounter;
        return id.compareTo(otherId)<0;
    }

    private static String ensureFolder(SQLiteDatabase db, String name) {
        String cleaned = name.trim();
        if (cleaned.isEmpty() || cleaned.length() > 64) return "";
        try (Cursor cursor = db.rawQuery("SELECT id FROM folders WHERE name=?", new String[]{cleaned})) {
            if (cursor.moveToFirst()) return cursor.getString(0);
        }
        ContentValues folder = new ContentValues(); folder.put("id", UUID.randomUUID().toString()); folder.put("name", cleaned);
        db.insertWithOnConflict("folders", null, folder, SQLiteDatabase.CONFLICT_IGNORE);
        try (Cursor cursor = db.rawQuery("SELECT id FROM folders WHERE name=?", new String[]{cleaned})) {
            return cursor.moveToFirst() ? cursor.getString(0) : "";
        }
    }

    private static void materializeRemoteOperation(SQLiteDatabase db, RemoteOperation operation) throws Exception {
        org.json.JSONObject payload = new org.json.JSONObject(operation.payload);
        if("member.retire".equals(operation.kind)||"member.join".equals(operation.kind)){
            String node=payload.optString("nodeId");long epoch=payload.optLong("epoch",0);if(node.isEmpty()||node.length()>160||epoch<1)return;
            long current=0;String state="";try(Cursor row=db.rawQuery("SELECT epoch,state FROM library_members WHERE node_id=?",new String[]{node})){if(row.moveToFirst()){current=row.getLong(0);state=row.getString(1);}}
            if("member.retire".equals(operation.kind)){if(current!=epoch||"retired".equals(state))return;
                db.execSQL("UPDATE library_members SET state='retired',author_id=?,counter=?,updated_at=? WHERE node_id=?",new Object[]{operation.author,operation.counter,Instant.now().toString(),node});return;}
            if(epoch<=current)return;
            db.execSQL("INSERT INTO library_members(node_id,epoch,state,author_id,counter,updated_at) VALUES(?,?,'active',?,?,?) ON CONFLICT(node_id) DO UPDATE SET epoch=excluded.epoch,state='active',author_id=excluded.author_id,counter=excluded.counter,updated_at=excluded.updated_at",new Object[]{node,epoch,operation.author,operation.counter,Instant.now().toString()});return;
        }
        if ("folder.create".equals(operation.kind)) {
            String name=payload.optString("folderName","").trim();if(name.isEmpty())return;
            try(Cursor previous=db.rawQuery("SELECT author_id,counter FROM library_folder_tombstones WHERE name=?",new String[]{name})){
                if(previous.moveToFirst()){
                    String author=previous.getString(0);long counter=previous.getLong(1);JSONObject context=payload.optJSONObject("context");
                    if(!author.equals(operation.author)&&(context==null||context.optLong(author,0)<counter))return;
                }
            }
            db.delete("library_folder_tombstones","name=? COLLATE NOCASE",new String[]{name});ensureFolder(db,name);return;
        }
        if ("folder.rename".equals(operation.kind)) {
            String previous = payload.optString("previousName", "").trim(); String name = payload.optString("folderName", "").trim();
            if (!previous.isEmpty() && !name.isEmpty() && name.length() <= 64) {
                String priorAuthor = ""; long priorCounter = 0;
                try (Cursor cursor = db.rawQuery("SELECT author_id,counter FROM library_folder_rename_state WHERE anchor=?", new String[]{previous})) {
                    if (cursor.moveToFirst()) { priorAuthor = cursor.getString(0); priorCounter = cursor.getLong(1); }
                }
                org.json.JSONObject renameContext = payload.optJSONObject("context");
                long observed = renameContext == null ? 0 : renameContext.optLong(priorAuthor, 0);
                if (!priorAuthor.isEmpty() && !priorAuthor.equals(operation.author) && observed < priorCounter) {
                    ContentValues conflict = new ContentValues(); conflict.put("anchor", previous); conflict.put("author_id", operation.author); conflict.put("counter", operation.counter); conflict.put("op_id", operation.id); conflict.put("folder_name", name); conflict.put("created_at", Instant.now().toString());
                    db.insertWithOnConflict("library_folder_rename_conflicts", null, conflict, SQLiteDatabase.CONFLICT_IGNORE);
                    return;
                }
                ContentValues values = new ContentValues(); values.put("name", name); db.update("folders", values, "name=? COLLATE NOCASE", new String[]{previous});
                ContentValues state = new ContentValues(); state.put("anchor", previous); state.put("author_id", operation.author); state.put("counter", operation.counter); state.put("current_name", name); state.put("updated_at", Instant.now().toString());
                db.insertWithOnConflict("library_folder_rename_state", null, state, SQLiteDatabase.CONFLICT_REPLACE);
            }
            return;
        }
        if ("folder.delete".equals(operation.kind)) { String name = payload.optString("folderName", "").trim(); if (!name.isEmpty()) {db.execSQL("INSERT INTO library_folder_tombstones(name,author_id,counter,deleted_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET author_id=excluded.author_id,counter=excluded.counter,deleted_at=excluded.deleted_at",new Object[]{name,operation.author,operation.counter,Instant.now().toString()});db.delete("folders", "name=? COLLATE NOCASE", new String[]{name});} return; }
        List<String> fileIds = operationFileIds(payload.optJSONArray("fileIds"));
        if (fileIds.isEmpty()) return;
        if("file.version.select".equals(operation.kind)){
            if(fileIds.size()!=1)return;
            String fileId=fileIds.get(0),revisionId=payload.optString("revisionId","");
            if(revisionId.isEmpty()||revisionId.length()>160)return;
            String priorAuthor="",priorId="",priorRevision="",priorContext="{}";long priorCounter=0;
            try(Cursor prior=db.rawQuery("SELECT author_id,counter,op_id,revision_id,context_json FROM library_file_current_revision WHERE file_id=?",new String[]{fileId})){if(prior.moveToFirst()){priorAuthor=prior.getString(0);priorCounter=prior.getLong(1);priorId=prior.getString(2);priorRevision=prior.getString(3);priorContext=prior.getString(4);}}
            if(new JSONObject(priorContext).optLong(operation.author,0)>=operation.counter||priorAuthor.equals(operation.author)&&priorCounter>=operation.counter)return;
            JSONObject context=payload.optJSONObject("context");
            boolean concurrent=!priorAuthor.isEmpty()&&!priorAuthor.equals(operation.author)&&(context==null||context.optLong(priorAuthor,0)<priorCounter);
            if(concurrent){
                ContentValues conflict=new ContentValues();conflict.put("file_id",fileId);conflict.put("author_id",operation.author);conflict.put("counter",operation.counter);conflict.put("op_id",operation.id);conflict.put("revision_id",revisionId);conflict.put("created_at",Instant.now().toString());
                if(!operationBefore(operation.author,operation.counter,operation.id,priorAuthor,priorCounter,priorId)){db.insertWithOnConflict("library_file_revision_conflicts",null,conflict,SQLiteDatabase.CONFLICT_IGNORE);return;}
                conflict.put("author_id",priorAuthor);conflict.put("counter",priorCounter);conflict.put("op_id",priorId);conflict.put("revision_id",priorRevision);db.insertWithOnConflict("library_file_revision_conflicts",null,conflict,SQLiteDatabase.CONFLICT_IGNORE);
            }
            if(payload.optBoolean("resolve",false)&&!concurrent)db.delete("library_file_revision_conflicts","file_id=?",new String[]{fileId});
            ContentValues value=new ContentValues();value.put("file_id",fileId);value.put("revision_id",revisionId);value.put("author_id",operation.author);value.put("counter",operation.counter);value.put("op_id",operation.id);value.put("context_json",context==null?"{}":context.toString());value.put("updated_at",Instant.now().toString());db.insertWithOnConflict("library_file_current_revision",null,value,SQLiteDatabase.CONFLICT_REPLACE);
            return;
        }
        if("file.rename".equals(operation.kind)){
            if(fileIds.size()!=1)return;
            String fileId=fileIds.get(0),name=payload.optString("fileName","").trim();
            if(name.isEmpty()||name.length()>255||name.equals(".")||name.equals("..")||name.indexOf('/')>=0||name.indexOf('\\')>=0||name.indexOf('\n')>=0||name.indexOf('\r')>=0||name.indexOf('\0')>=0)return;
            boolean resolve=payload.optBoolean("resolve",false);String priorAuthor="",priorId="",priorName="",priorContext="{}";long priorCounter=0;
            try(Cursor prior=db.rawQuery("SELECT author_id,counter,op_id,current_name,context_json FROM library_file_name_state WHERE file_id=?",new String[]{fileId})){if(prior.moveToFirst()){priorAuthor=prior.getString(0);priorCounter=prior.getLong(1);priorId=prior.getString(2);priorName=prior.getString(3);priorContext=prior.getString(4);}}
            if(new JSONObject(priorContext).optLong(operation.author,0)>=operation.counter)return;
            if(priorAuthor.equals(operation.author)&&priorCounter>=operation.counter)return;
            JSONObject context=payload.optJSONObject("context");
            boolean concurrent=!priorAuthor.isEmpty()&&!priorAuthor.equals(operation.author)&&(context==null||context.optLong(priorAuthor,0)<priorCounter);
            if(concurrent){
                ContentValues conflict=new ContentValues();conflict.put("file_id",fileId);conflict.put("author_id",operation.author);conflict.put("counter",operation.counter);conflict.put("op_id",operation.id);conflict.put("file_name",name);conflict.put("created_at",Instant.now().toString());
                if(!operationBefore(operation.author,operation.counter,operation.id,priorAuthor,priorCounter,priorId)){db.insertWithOnConflict("library_file_name_conflicts",null,conflict,SQLiteDatabase.CONFLICT_IGNORE);return;}
                conflict.put("author_id",priorAuthor);conflict.put("counter",priorCounter);conflict.put("op_id",priorId);conflict.put("file_name",priorName);db.insertWithOnConflict("library_file_name_conflicts",null,conflict,SQLiteDatabase.CONFLICT_IGNORE);
            }
            if(resolve&&!concurrent)db.delete("library_file_name_conflicts","file_id=?",new String[]{fileId});
            ContentValues value=new ContentValues();value.put("file_id",fileId);value.put("author_id",operation.author);value.put("counter",operation.counter);value.put("op_id",operation.id);value.put("current_name",name);value.put("context_json",context==null?"{}":context.toString());value.put("updated_at",Instant.now().toString());db.insertWithOnConflict("library_file_name_state",null,value,SQLiteDatabase.CONFLICT_REPLACE);
            db.execSQL("INSERT OR REPLACE INTO index_outbox(file_id,item_uri,token) SELECT ?,uri,lower(hex(randomblob(16))) FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=? LIMIT 1",new Object[]{fileId,fileId});
            return;
        }
        if ("file.trash".equals(operation.kind) || "file.restore".equals(operation.kind) || "file.delete".equals(operation.kind)) {
            String state="file.trash".equals(operation.kind)?"trashed":"active";JSONObject context=payload.optJSONObject("context");
            for(String fileId:fileIds){
                if("file.delete".equals(operation.kind)){
                    ContentValues deletion=new ContentValues();deletion.put("file_id",fileId);deletion.put("author_id",operation.author);deletion.put("counter",operation.counter);deletion.put("op_id",operation.id);deletion.put("deleted_at",Instant.now().toString());db.insertWithOnConflict("library_file_deletions",null,deletion,SQLiteDatabase.CONFLICT_IGNORE);continue;
                }
                try(Cursor deletion=db.rawQuery("SELECT 1 FROM library_file_deletions WHERE file_id=?",new String[]{fileId})){if(deletion.moveToFirst())continue;}
                boolean apply=true;
                try(Cursor previous=db.rawQuery("SELECT state,author_id,counter,op_id,context_json FROM library_file_lifecycle WHERE file_id=?",new String[]{fileId})){
                    if(previous.moveToFirst()){
                        String priorState=previous.getString(0),priorAuthor=previous.getString(1),priorId=previous.getString(3);long priorCounter=previous.getLong(2);
                        JSONObject priorContext=new JSONObject(previous.getString(4));
                        if(priorContext.optLong(operation.author,0)>=operation.counter||priorAuthor.equals(operation.author)&&priorCounter>=operation.counter)apply=false;
                        else if(!priorAuthor.equals(operation.author)&&(context==null||context.optLong(priorAuthor,0)<priorCounter)){
                            if("active".equals(state)&&"trashed".equals(priorState)||state.equals(priorState)&&operation.id.compareTo(priorId)<=0)apply=false;
                        }
                    }
                }
                if(apply){ContentValues value=new ContentValues();value.put("file_id",fileId);value.put("state",state);value.put("author_id",operation.author);value.put("counter",operation.counter);value.put("op_id",operation.id);value.put("context_json",context==null?"{}":context.toString());value.put("updated_at",Instant.now().toString());db.insertWithOnConflict("library_file_lifecycle",null,value,SQLiteDatabase.CONFLICT_REPLACE);}
            }
            return;
        }
        if ("tag.add".equals(operation.kind) || "tag.remove".equals(operation.kind)) {
            String tag = payload.optString("tag", "").trim(); if (tag.isEmpty() || tag.length() > 64) return;
            for (String fileId : fileIds) {
                if ("tag.add".equals(operation.kind)) {
                    String token = payload.optString("tagToken", operation.id);
                    ContentValues tokenValues = new ContentValues(); tokenValues.put("file_id", fileId); tokenValues.put("tag_name", tag); tokenValues.put("token", token);
                    db.insertWithOnConflict("library_tag_add_tokens", null, tokenValues, SQLiteDatabase.CONFLICT_IGNORE);
                    db.execSQL("INSERT OR IGNORE INTO tags(item_uri,name) SELECT uri,? FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=?", new Object[]{tag, fileId});
                } else {
                    org.json.JSONArray removed = payload.optJSONArray("removeTokens");
                    if (removed != null) for (int index = 0; index < removed.length(); index++) {
                        String token = removed.optString(index, ""); if (token.isEmpty()) continue;
                        ContentValues removedToken = new ContentValues(); removedToken.put("file_id", fileId); removedToken.put("tag_name", tag); removedToken.put("token", token);
                        db.insertWithOnConflict("library_tag_remove_tokens", null, removedToken, SQLiteDatabase.CONFLICT_IGNORE);
                    }
                    int remaining = 0;
                    try (Cursor cursor = db.rawQuery("SELECT COUNT(*) FROM library_tag_add_tokens a WHERE a.file_id=? AND a.tag_name=? AND NOT EXISTS (SELECT 1 FROM library_tag_remove_tokens r WHERE r.file_id=a.file_id AND r.tag_name=a.tag_name AND r.token=a.token)", new String[]{fileId, tag})) { if (cursor.moveToFirst()) remaining = cursor.getInt(0); }
                    if (remaining == 0) db.delete("tags", "name=? AND item_uri IN (SELECT uri FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=?)", new String[]{tag, fileId});
                }
            }
            return;
        }
        if (!"folder.move".equals(operation.kind)) return;
        String folderName = payload.optString("folderName", "").trim();
        boolean deleted=false;
        if(!folderName.isEmpty())try(Cursor tombstone=db.rawQuery("SELECT 1 FROM library_folder_tombstones WHERE name=?",new String[]{folderName})){deleted=tombstone.moveToFirst();}
        String folderId = folderName.isEmpty()||deleted ? "" : ensureFolder(db, folderName);
        org.json.JSONObject context = payload.optJSONObject("context");
        for (String fileId : fileIds) {
            if(deleted){ContentValues conflict=new ContentValues();conflict.put("file_id",fileId);conflict.put("author_id",operation.author);conflict.put("counter",operation.counter);conflict.put("op_id",operation.id);conflict.put("folder_name",folderName);conflict.put("created_at",Instant.now().toString());db.insertWithOnConflict("library_folder_conflicts",null,conflict,SQLiteDatabase.CONFLICT_IGNORE);continue;}
            String priorAuthor = ""; long priorCounter = 0;
            try (Cursor cursor = db.rawQuery("SELECT author_id,counter FROM library_file_folder_state WHERE file_id=?", new String[]{fileId})) {
                if (cursor.moveToFirst()) { priorAuthor = cursor.getString(0); priorCounter = cursor.getLong(1); }
            }
            long observed = context == null ? 0 : context.optLong(priorAuthor, 0);
            if (!priorAuthor.isEmpty() && !priorAuthor.equals(operation.author) && observed < priorCounter) {
                ContentValues conflict = new ContentValues(); conflict.put("file_id", fileId); conflict.put("author_id", operation.author); conflict.put("counter", operation.counter); conflict.put("op_id", operation.id); conflict.put("folder_name", folderName); conflict.put("created_at", Instant.now().toString());
                db.insertWithOnConflict("library_folder_conflicts", null, conflict, SQLiteDatabase.CONFLICT_IGNORE);
                continue;
            }
            ContentValues item = new ContentValues(); if (folderId.isEmpty()) item.putNull("folder_id"); else item.put("folder_id", folderId);
            db.update("items", item, "COALESCE(NULLIF(logical_file_id,''),file_id)=?", new String[]{fileId});
            ContentValues state = new ContentValues(); state.put("file_id", fileId); state.put("author_id", operation.author); state.put("counter", operation.counter); state.put("folder_id", folderId); state.put("updated_at", Instant.now().toString());
            db.insertWithOnConflict("library_file_folder_state", null, state, SQLiteDatabase.CONFLICT_REPLACE);
        }
    }

    void resolveFolderConflict(String fileId, String folderName) {
        String name = folderName == null ? "" : folderName.trim();
        if (fileId == null || fileId.trim().isEmpty() || fileId.length() > 160 || name.length() > 64) throw new IllegalArgumentException("归组选择无效");
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            boolean deleted=false;if(!name.isEmpty())try(Cursor tombstone=db.rawQuery("SELECT 1 FROM library_folder_tombstones WHERE name=?",new String[]{name})){deleted=tombstone.moveToFirst();}
            String folderId = name.isEmpty() ? "" : ensureFolder(db, name);
            if(deleted)recordLibraryOperation(db,"folder.create","{\"folderName\":"+JSONObject.quote(name)+"}");
            ContentValues item = new ContentValues(); if (folderId.isEmpty()) item.putNull("folder_id"); else item.put("folder_id", folderId);
            db.update("items", item, "COALESCE(NULLIF(logical_file_id,''),file_id)=?", new String[]{fileId});
            db.delete("library_folder_conflicts", "file_id=?", new String[]{fileId});
            recordLibraryOperation(db, "folder.move", "{\"fileIds\":" + jsonArray(Collections.singletonList(fileId)) + ",\"folderId\":" + JSONObject.quote(folderId) + ",\"folderName\":" + JSONObject.quote(name) + "}");
            recordLocalFolderMoveState(db, Collections.singletonList(fileId), folderId);
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void resolveFolderRenameConflict(String anchor, String folderName) {
        String name = folderName == null ? "" : folderName.trim();
        String key = anchor == null ? "" : anchor.trim();
        if (key.isEmpty() || key.length() > 64 || name.isEmpty() || name.length() > 64) throw new IllegalArgumentException("改名选择无效");
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            String current = key;
            try (Cursor cursor = db.rawQuery("SELECT current_name FROM library_folder_rename_state WHERE anchor=?", new String[]{key})) {
                if (cursor.moveToFirst()) current = cursor.getString(0);
            }
            ContentValues values = new ContentValues(); values.put("name", name);
            db.update("folders", values, "name=? COLLATE NOCASE", new String[]{current});
            db.delete("library_folder_rename_conflicts", "anchor=?", new String[]{key});
            db.delete("library_folder_rename_state", "anchor=?", new String[]{key});
            recordLibraryOperation(db, "folder.rename", "{\"folderName\":" + JSONObject.quote(name) + ",\"previousName\":" + JSONObject.quote(current) + "}");
            recordLocalFolderRenameState(db, current, name);
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    String createFolder(String name) {
        String cleaned = cleanName(name, "文件夹");
        String id = UUID.randomUUID().toString();
        ContentValues values = new ContentValues();
        values.put("id", id);
        values.put("name", cleaned);
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            db.insertOrThrow("folders", null, values);
            recordLibraryOperation(db, "folder.create", "{\"folderName\":" + JSONObject.quote(cleaned) + "}");
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
        return id;
    }

    void renameFolder(String id, String name) {
        SQLiteDatabase db = getWritableDatabase();
        String previous = "";
        try (Cursor cursor = db.rawQuery("SELECT name FROM folders WHERE id=?", new String[]{id})) {
            if (cursor.moveToFirst()) previous = cursor.getString(0);
        }
        if (previous.isEmpty()) return;
        ContentValues values = new ContentValues();
        String cleaned = cleanName(name, "文件夹");
        values.put("name", cleaned);
        db.beginTransaction();
        try {
            db.update("folders", values, "id=?", new String[]{id});
            recordLibraryOperation(db, "folder.rename", "{\"folderName\":" + JSONObject.quote(cleaned) + ",\"previousName\":" + JSONObject.quote(previous) + "}");
            recordLocalFolderRenameState(db, previous, cleaned);
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void removeFolder(String id) {
        SQLiteDatabase db = getWritableDatabase();
        String name = "";
        try (Cursor cursor = db.rawQuery("SELECT name FROM folders WHERE id=?", new String[]{id})) {
            if (cursor.moveToFirst()) name = cursor.getString(0);
        }
        if (name.isEmpty()) return;
        db.beginTransaction();
        try {
            db.delete("folders", "id=?", new String[]{id});
            try (Cursor cursor = db.rawQuery("SELECT id, filters_json FROM collections", null)) {
                while (cursor.moveToNext()) {
                    LocalFilters filters = LocalFilters.parse(cursor.getString(1));
                    if (!id.equals(filters.folderId)) continue;
                    filters.folderId = "unfiled";
                    ContentValues values = new ContentValues();
                    values.put("filters_json", filters.serialize());
                    db.update("collections", values, "id=?", new String[]{cursor.getString(0)});
                }
            }
            recordLibraryOperation(db, "folder.delete", "{\"folderName\":" + JSONObject.quote(name) + "}");
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void moveItems(List<String> uris, String folderId) {
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            List<String> fileIds = logicalFileIds(db, uris);
            String folderName = "";
            if (!"unfiled".equals(folderId)) {
                try (Cursor cursor = db.rawQuery("SELECT name FROM folders WHERE id=?", new String[]{folderId})) {
                    if (cursor.moveToFirst()) folderName = cursor.getString(0);
                }
            }
            for (String fileId : fileIds) {
                ContentValues values = new ContentValues();
                if ("unfiled".equals(folderId)) values.putNull("folder_id");
                else values.put("folder_id", folderId);
                db.update("items", values, "COALESCE(NULLIF(logical_file_id,''),file_id)=?", new String[]{fileId});
            }
            if (!fileIds.isEmpty()) {
                recordLibraryOperation(db, "folder.move", "{\"fileIds\":" + jsonArray(fileIds) + ",\"folderId\":" + JSONObject.quote("unfiled".equals(folderId) ? "" : folderId) + ",\"folderName\":" + JSONObject.quote(folderName) + "}");
                recordLocalFolderMoveState(db, fileIds, "unfiled".equals(folderId) ? "" : folderId);
            }
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    List<Collection> collections() {
        List<Collection> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT id, name, filters_json FROM collections ORDER BY name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Collection(cursor));
        }
        return result;
    }

    String saveCollection(String id, String name, LocalFilters filters) {
        ContentValues values = new ContentValues();
        String actualId = id == null ? UUID.randomUUID().toString() : id;
        values.put("id", actualId);
        values.put("name", cleanName(name, "常用查询"));
        values.put("filters_json", filters.serialize());
        if (id == null) getWritableDatabase().insertOrThrow("collections", null, values);
        else getWritableDatabase().update("collections", values, "id=?", new String[]{id});
        return actualId;
    }

    void removeCollection(String id) {
        getWritableDatabase().delete("collections", "id=?", new String[]{id});
    }

    private static String cleanName(String name, String label) {
        String cleaned = name.trim();
        if (cleaned.isEmpty() || cleaned.length() > 64) throw new IllegalArgumentException(label + "名称应为 1–64 个字符");
        if ("文件夹".equals(label) && ("未归组".equals(cleaned) || "全部资源".equals(cleaned))) {
            throw new IllegalArgumentException("文件夹名称已保留");
        }
        return cleaned;
    }

    List<String> matchingUris(LocalFilters filters) {
        ArrayList<String> args = new ArrayList<>();
        String where = where(filters, args);
        List<String> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery("SELECT i.uri FROM items i WHERE " + where, args.toArray(new String[0]))) {
            while (cursor.moveToNext()) result.add(cursor.getString(0));
        }
        return result;
    }

    List<String[]> trashedFiles() {
        List<String[]> result=new ArrayList<>();
        try(Cursor cursor=getReadableDatabase().rawQuery("SELECT l.file_id,COALESCE((SELECT name FROM items i WHERE COALESCE(NULLIF(i.logical_file_id,''),i.file_id)=l.file_id LIMIT 1),l.file_id) FROM library_file_lifecycle l WHERE l.state='trashed' AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=l.file_id) ORDER BY l.updated_at DESC,l.file_id",null)){
            while(cursor.moveToNext())result.add(new String[]{cursor.getString(0),cursor.getString(1)});
        }
        return result;
    }

    void trashUris(List<String> uris) {
        if(uris==null||uris.isEmpty())return;
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            LinkedHashSet<String> fileIds=new LinkedHashSet<>();
            for(String uri:uris)try(Cursor cursor=db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items WHERE uri=?",new String[]{uri})){
                if(cursor.moveToFirst())fileIds.add(cursor.getString(0));
            }
            if(!fileIds.isEmpty())recordLibraryOperation(db,"file.trash","{\"fileIds\":"+jsonArray(new ArrayList<>(fileIds))+"}");
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    void restoreFile(String fileId) {
        if(fileId==null||fileId.isEmpty()||fileId.length()>160)throw new IllegalArgumentException("文件标识无效");
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            int known=0;try(Cursor cursor=db.rawQuery("SELECT COUNT(*) FROM library_file_lifecycle WHERE file_id=? AND state='trashed' AND NOT EXISTS (SELECT 1 FROM library_file_deletions WHERE file_id=?)",new String[]{fileId,fileId})){if(cursor.moveToFirst())known=cursor.getInt(0);}
            if(known!=0)recordLibraryOperation(db,"file.restore","{\"fileIds\":["+JSONObject.quote(fileId)+"]}");
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    void deleteFileRecord(String fileId) {
        if(fileId==null||fileId.isEmpty()||fileId.length()>160)throw new IllegalArgumentException("文件标识无效");
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            int trashed=0;try(Cursor cursor=db.rawQuery("SELECT COUNT(*) FROM library_file_lifecycle WHERE file_id=? AND state='trashed'",new String[]{fileId})){if(cursor.moveToFirst())trashed=cursor.getInt(0);}
            if(trashed==0)throw new IllegalStateException("请先移入回收站");
            recordLibraryOperation(db,"file.delete","{\"fileIds\":["+JSONObject.quote(fileId)+"]}");
            db.setTransactionSuccessful();
        }finally{db.endTransaction();}
    }

    void addTagToItems(List<String> uris, String name) {
        String cleaned = cleanName(name, "标签");
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            List<String> changed = new ArrayList<>();
            for (String fileId : logicalFileIds(db, uris)) {
                boolean inserted = false;
                try (Cursor items = db.rawQuery("SELECT uri FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=?", new String[]{fileId})) {
                    while (items.moveToNext()) {
                        ContentValues values = new ContentValues();
                        values.put("item_uri", items.getString(0));
                        values.put("name", cleaned);
                        inserted |= db.insertWithOnConflict("tags", null, values, SQLiteDatabase.CONFLICT_IGNORE) != -1;
                    }
                }
                if (inserted) changed.add(fileId);
            }
            if (!changed.isEmpty()) {
                String token = UUID.randomUUID().toString();
                for (String fileId : changed) {
                    ContentValues tokenValues = new ContentValues();
                    tokenValues.put("file_id", fileId);
                    tokenValues.put("tag_name", cleaned);
                    tokenValues.put("token", token);
                    db.insertOrThrow("library_tag_add_tokens", null, tokenValues);
                }
                recordLibraryOperation(db, "tag.add", "{\"fileIds\":" + jsonArray(changed) + ",\"tag\":" + JSONObject.quote(cleaned) + ",\"tagToken\":" + JSONObject.quote(token) + "}");
            }
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void removeTagFromItems(List<String> uris, String name) {
        String cleaned = name.trim();
        if (cleaned.isEmpty()) return;
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            List<String> changed = new ArrayList<>();
            List<String> fileIds = logicalFileIds(db, uris);
            List<String> tokens = observedTagTokens(db, fileIds, cleaned);
            for (String fileId : fileIds) {
                int removed = db.delete("tags", "name=? AND item_uri IN (SELECT uri FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=?)", new String[]{cleaned, fileId});
                if (removed > 0) changed.add(fileId);
            }
            if (!changed.isEmpty()) recordLibraryOperation(db, "tag.remove", "{\"fileIds\":" + jsonArray(changed) + ",\"tag\":" + JSONObject.quote(cleaned) + ",\"removeTokens\":" + jsonArray(tokens) + "}");
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void addTag(String itemUri, String name) {
        addTagToItems(Collections.singletonList(itemUri), name);
    }

    void removeTag(String itemUri, String name) {
        removeTagFromItems(Collections.singletonList(itemUri), name);
    }

    void upsertItem(SQLiteDatabase db, String uri, String rootUri, String name, String path,
                    String mime, long size, long modified, String scanToken) {
        ContentValues values = new ContentValues();
        values.put("uri", uri);
        values.put("root_uri", rootUri);
        values.put("name", name);
        values.put("path", path);
        values.put("mime", mime);
        values.put("size", size);
        values.put("modified", modified);
        values.put("scan_token", scanToken);
        values.put("kind", kindFor(name));
        boolean changed = true;
        try (Cursor cursor = db.rawQuery("SELECT size,modified,mime FROM items WHERE uri=?", new String[]{uri})) {
            if (cursor.moveToFirst()) changed = cursor.getLong(0) != size || cursor.getLong(1) != modified || !cursor.getString(2).equals(mime);
        }
        if (changed) values.put("revision_id", UUID.randomUUID().toString());
        if (db.update("items", values, "uri=?", new String[]{uri}) == 0) {
            String fileID = aliasFileID(db, uri);
            values.put("file_id", fileID == null ? UUID.randomUUID().toString() : fileID);
			values.put("logical_file_id",fileID == null ? values.getAsString("file_id") : fileID);
            values.put("revision_id", UUID.randomUUID().toString());
            db.insertOrThrow("items", null, values);
        }
    }

    private static String aliasFileID(SQLiteDatabase db, String uri) {
        try (Cursor cursor = db.rawQuery("SELECT file_id FROM item_aliases WHERE uri=?", new String[]{uri})) {
            return cursor.moveToFirst() ? cursor.getString(0) : null;
        }
    }

    // A received SAF document has its own physical row/URI, while logicalFileID
    // joins it to the source file for unified publication and search.
    void importTransferredItem(String uri,String rootUri,String name,String mime,long size,long modified,String logicalFileID,String revisionID,String folderName,List<String> tags) {
        if(logicalFileID==null||logicalFileID.isEmpty()||revisionID==null||revisionID.isEmpty())return;
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try {
            int known=0;try(Cursor cursor=db.rawQuery("SELECT COUNT(*) FROM items WHERE COALESCE(NULLIF(logical_file_id,''),file_id)=?",new String[]{logicalFileID})){if(cursor.moveToFirst())known=cursor.getInt(0);}
            ContentValues values=new ContentValues();values.put("uri",uri);values.put("root_uri",rootUri);values.put("name",name);values.put("path",name);values.put("mime",mime);values.put("size",size);values.put("modified",modified);values.put("scan_token","transfer:"+UUID.randomUUID());values.put("kind",kindFor(name));values.put("file_id",UUID.randomUUID().toString());values.put("logical_file_id",logicalFileID);values.put("revision_id",revisionID);
            db.insertOrThrow("items",null,values);
            if(known==0){
                String folder=folderName==null?"":folderName.trim();if(!folder.isEmpty()&&folder.length()<=64){String folderID=UUID.randomUUID().toString();ContentValues item=new ContentValues();item.put("id",folderID);item.put("name",folder);db.insertWithOnConflict("folders",null,item,SQLiteDatabase.CONFLICT_IGNORE);try(Cursor cursor=db.rawQuery("SELECT id FROM folders WHERE name=?",new String[]{folder})){if(cursor.moveToFirst()){ContentValues assigned=new ContentValues();assigned.put("folder_id",cursor.getString(0));db.update("items",assigned,"uri=?",new String[]{uri});}}}
                if(tags!=null)for(String tag:tags){if(tag==null||(tag=tag.trim()).isEmpty()||tag.length()>64)continue;ContentValues value=new ContentValues();value.put("item_uri",uri);value.put("name",tag);db.insertWithOnConflict("tags",null,value,SQLiteDatabase.CONFLICT_IGNORE);}
            }
            db.setTransactionSuccessful();
        } finally {db.endTransaction();}
    }

    void removeMissing(SQLiteDatabase db, String rootUri, String scanToken) {
        db.delete("items", "root_uri=? AND scan_token<>?", new String[]{rootUri, scanToken});
    }
}
