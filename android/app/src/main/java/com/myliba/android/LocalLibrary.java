package com.myliba.android;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;

import java.util.ArrayList;
import java.util.Collections;
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
        super(context, "local-library.db", null, 2);
    }

    @Override public void onConfigure(SQLiteDatabase db) {
        db.setForeignKeyConstraintsEnabled(true);
    }

    @Override public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE roots (uri TEXT PRIMARY KEY, name TEXT NOT NULL)");
        db.execSQL("CREATE TABLE items (uri TEXT PRIMARY KEY, root_uri TEXT NOT NULL REFERENCES roots(uri) ON DELETE CASCADE, name TEXT NOT NULL, path TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL, scan_token TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'file', folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL)");
        db.execSQL("CREATE INDEX items_root ON items(root_uri)");
        db.execSQL("CREATE TABLE tags (item_uri TEXT NOT NULL REFERENCES items(uri) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY(item_uri, name))");
        createOrganizationTables(db);
        createOrganizationIndexes(db);
    }

    @Override public void onUpgrade(SQLiteDatabase db, int oldVersion, int newVersion) {
        if (oldVersion != 1 || newVersion != 2) throw new IllegalStateException("Missing local library migration from " + oldVersion);
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
    }

    private static void createOrganizationTables(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE folders (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE)");
        db.execSQL("CREATE TABLE collections (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, filters_json TEXT NOT NULL)");
    }

    private static void createOrganizationIndexes(SQLiteDatabase db) {
        db.execSQL("CREATE INDEX items_folder ON items(folder_id)");
        db.execSQL("CREATE INDEX tags_name ON tags(name)");
    }

    private static String kindFor(String name) {
        int dot = name.lastIndexOf('.');
        if (dot < 0) return "file";
        String ext = name.substring(dot + 1).toLowerCase(java.util.Locale.ROOT);
        switch (ext) {
            case "jpg": case "jpeg": case "png": case "gif": case "webp": case "bmp": case "avif": case "heic": return "image";
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
                "SELECT r.uri, r.name, COUNT(i.uri) FROM roots r LEFT JOIN items i ON i.root_uri=r.uri GROUP BY r.uri ORDER BY r.name COLLATE NOCASE", null)) {
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
            case "name_asc": order = "i.name COLLATE NOCASE ASC, i.uri ASC"; break;
            case "name_desc": order = "i.name COLLATE NOCASE DESC, i.uri ASC"; break;
            case "size_desc": order = "i.size DESC, i.uri ASC"; break;
            default: order = "i.modified DESC, i.uri DESC";
        }
        args.add(String.valueOf(limit));
        args.add(String.valueOf(offset));
        List<Item> items = new ArrayList<>();
        try (Cursor cursor = db.rawQuery(
                "SELECT i.uri, i.root_uri, i.name, i.path, i.mime, i.size, i.modified, " +
                "COALESCE((SELECT group_concat(t.name, ' · ') FROM tags t WHERE t.item_uri=i.uri), ''), i.folder_id " +
                "FROM items i WHERE " + where + " ORDER BY " + order + " LIMIT ? OFFSET ?", args.toArray(new String[0]))) {
            while (cursor.moveToNext()) items.add(new Item(cursor));
        }
        return new Page(items, total);
    }

    private static String where(LocalFilters filters, ArrayList<String> args) {
        ArrayList<String> clauses = new ArrayList<>();
        String query = filters.query.trim();
        if (!query.isEmpty()) {
            String needle = "%" + escapeLike(query) + "%";
            clauses.add("(i.name LIKE ? ESCAPE '\\' OR i.path LIKE ? ESCAPE '\\' OR EXISTS (SELECT 1 FROM tags t WHERE t.item_uri=i.uri AND t.name LIKE ? ESCAPE '\\'))");
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
        String scope = "unfiled".equals(folderId) ? " WHERE i.folder_id IS NULL" : "all".equals(folderId) ? "" : " WHERE i.folder_id=?";
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
                "SELECT f.id, f.name, COUNT(i.uri) FROM folders f LEFT JOIN items i ON i.folder_id=f.id GROUP BY f.id ORDER BY f.name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Folder(cursor));
        }
        return result;
    }

    String createFolder(String name) {
        String cleaned = cleanName(name, "文件夹");
        String id = UUID.randomUUID().toString();
        ContentValues values = new ContentValues();
        values.put("id", id);
        values.put("name", cleaned);
        getWritableDatabase().insertOrThrow("folders", null, values);
        return id;
    }

    void renameFolder(String id, String name) {
        ContentValues values = new ContentValues();
        values.put("name", cleanName(name, "文件夹"));
        getWritableDatabase().update("folders", values, "id=?", new String[]{id});
    }

    void removeFolder(String id) {
        SQLiteDatabase db = getWritableDatabase();
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
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void moveItems(List<String> uris, String folderId) {
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            for (String uri : uris) {
                ContentValues values = new ContentValues();
                if ("unfiled".equals(folderId)) values.putNull("folder_id");
                else values.put("folder_id", folderId);
                db.update("items", values, "uri=?", new String[]{uri});
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
        values.put("name", cleanName(name, "智能集合"));
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

    void addTagToItems(List<String> uris, String name) {
        String cleaned = cleanName(name, "标签");
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            for (String uri : uris) {
                ContentValues values = new ContentValues();
                values.put("item_uri", uri);
                values.put("name", cleaned);
                db.insertWithOnConflict("tags", null, values, SQLiteDatabase.CONFLICT_IGNORE);
            }
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void removeTagFromItems(List<String> uris, String name) {
        SQLiteDatabase db = getWritableDatabase();
        db.beginTransaction();
        try {
            for (String uri : uris) db.delete("tags", "item_uri=? AND name=?", new String[]{uri, name});
            db.setTransactionSuccessful();
        } finally { db.endTransaction(); }
    }

    void addTag(String itemUri, String name) {
        String cleaned = name.trim();
        if (cleaned.isEmpty() || cleaned.length() > 64) throw new IllegalArgumentException("标签应为 1–64 个字符");
        ContentValues values = new ContentValues();
        values.put("item_uri", itemUri);
        values.put("name", cleaned);
        getWritableDatabase().insertWithOnConflict("tags", null, values, SQLiteDatabase.CONFLICT_IGNORE);
    }

    void removeTag(String itemUri, String name) {
        getWritableDatabase().delete("tags", "item_uri=? AND name=?", new String[]{itemUri, name});
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
        if (db.update("items", values, "uri=?", new String[]{uri}) == 0) {
            db.insertOrThrow("items", null, values);
        }
    }

    void removeMissing(SQLiteDatabase db, String rootUri, String scanToken) {
        db.delete("items", "root_uri=? AND scan_token<>?", new String[]{rootUri, scanToken});
    }
}
