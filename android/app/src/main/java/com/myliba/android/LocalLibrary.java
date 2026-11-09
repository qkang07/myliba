package com.myliba.android;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;
import org.json.JSONObject;

import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.UUID;

final class LocalLibrary extends SQLiteOpenHelper {
    static final class Root {
        final String uri;
        final String name;
        final int count;

        Root(String uri, String name, int count) {
            this.uri = uri;
            this.name = name;
            this.count = count;
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
        super(context, "local-library.db", null, 17);
    }

    @Override public void onConfigure(SQLiteDatabase db) {
        db.setForeignKeyConstraintsEnabled(true);
    }

    @Override public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE roots (uri TEXT PRIMARY KEY, name TEXT NOT NULL)");
        db.execSQL("CREATE TABLE items (uri TEXT PRIMARY KEY, root_uri TEXT NOT NULL REFERENCES roots(uri) ON DELETE CASCADE, name TEXT NOT NULL, path TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL, scan_token TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'file', folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL, file_id TEXT NOT NULL UNIQUE, logical_file_id TEXT NOT NULL, revision_id TEXT NOT NULL)");
        db.execSQL("CREATE INDEX items_root ON items(root_uri)");
        db.execSQL("CREATE TABLE tags (item_uri TEXT NOT NULL REFERENCES items(uri) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY(item_uri, name))");
        createOrganizationTables(db);
        createOrganizationIndexes(db);
        createAliasTables(db);
        createIndexSyncTables(db);
        createOperationSyncTables(db);
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
        if (oldVersion != newVersion) throw new IllegalStateException("Missing local library migration to " + newVersion + " from " + oldVersion);
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
                "SELECT r.uri, r.name, COUNT(i.uri) FROM roots r LEFT JOIN items i ON i.root_uri=r.uri AND NOT EXISTS (SELECT 1 FROM library_file_lifecycle l WHERE l.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id) AND l.state='trashed') AND NOT EXISTS (SELECT 1 FROM library_file_deletions d WHERE d.file_id=COALESCE(NULLIF(i.logical_file_id,''),i.file_id)) GROUP BY r.uri ORDER BY r.name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Root(cursor.getString(0), cursor.getString(1), cursor.getInt(2)));
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

    void rejoinMember(String node){
        SQLiteDatabase db=getWritableDatabase();db.beginTransaction();
        try{
            long epoch=0;String state="";
            try(Cursor row=db.rawQuery("SELECT epoch,state FROM library_members WHERE node_id=?",new String[]{node})){if(row.moveToFirst()){epoch=row.getLong(0);state=row.getString(1);}}
            if(!"retired".equals(state))throw new IllegalStateException("当前节点未退役");
            try(Cursor row=db.rawQuery("SELECT COUNT(*) FROM library_operations WHERE state IN ('pending','failed')",null)){if(row.moveToFirst()&&row.getInt(0)>0)throw new IllegalStateException("旧作者仍有待发布操作，请先处理");}
            db.execSQL("INSERT INTO library_operation_identity(key,value) VALUES('author',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",new Object[]{"android-"+UUID.randomUUID()});
            recordLibraryOperation(db,"member.join","{\"nodeId\":"+JSONObject.quote(node)+",\"epoch\":"+(epoch+1)+"}");
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
            for (RemoteOperation operation : operations) {
                if (operation.id.isEmpty() || operation.author.isEmpty() || operation.kind.isEmpty() || operation.counter < 1 || operation.payload.length() > 1 << 20) continue;
                if(!knownOperationKind(operation.kind)){
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
                    materializeRemoteOperation(db, operation);
                    ContentValues inbox = new ContentValues();
                    inbox.put("op_id", operation.id); inbox.put("author_id", operation.author); inbox.put("counter", operation.counter);
                    inbox.put("received_at", Instant.now().toString()); inbox.put("applied_at", Instant.now().toString());
                    db.insertWithOnConflict("library_operation_inbox", null, inbox, SQLiteDatabase.CONFLICT_IGNORE);
                    ContentValues next = new ContentValues();
                    next.put("author_id", operation.author); next.put("last_counter", operation.counter); next.put("updated_at", Instant.now().toString());
                    db.insertWithOnConflict("library_operation_frontiers", null, next, SQLiteDatabase.CONFLICT_REPLACE);
                }
                if (localAuthor.equals(operation.author)) {
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
