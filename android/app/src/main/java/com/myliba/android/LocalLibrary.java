package com.myliba.android;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;

import java.util.ArrayList;
import java.util.List;

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

        Item(Cursor cursor) {
            uri = cursor.getString(0);
            rootUri = cursor.getString(1);
            name = cursor.getString(2);
            path = cursor.getString(3);
            mime = cursor.getString(4);
            size = cursor.getLong(5);
            modified = cursor.getLong(6);
            tags = cursor.getString(7);
        }
    }

    LocalLibrary(Context context) {
        super(context, "local-library.db", null, 1);
    }

    @Override public void onConfigure(SQLiteDatabase db) {
        db.setForeignKeyConstraintsEnabled(true);
    }

    @Override public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE roots (uri TEXT PRIMARY KEY, name TEXT NOT NULL)");
        db.execSQL("CREATE TABLE items (uri TEXT PRIMARY KEY, root_uri TEXT NOT NULL REFERENCES roots(uri) ON DELETE CASCADE, name TEXT NOT NULL, path TEXT NOT NULL, mime TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL, scan_token TEXT NOT NULL)");
        db.execSQL("CREATE INDEX items_root ON items(root_uri)");
        db.execSQL("CREATE TABLE tags (item_uri TEXT NOT NULL REFERENCES items(uri) ON DELETE CASCADE, name TEXT NOT NULL, PRIMARY KEY(item_uri, name))");
    }

    @Override public void onUpgrade(SQLiteDatabase db, int oldVersion, int newVersion) {
        throw new IllegalStateException("Missing local library migration from " + oldVersion);
    }

    void addRoot(Uri uri, String name) {
        ContentValues values = new ContentValues();
        values.put("uri", uri.toString());
        values.put("name", name);
        getWritableDatabase().insertWithOnConflict("roots", null, values, SQLiteDatabase.CONFLICT_IGNORE);
    }

    void removeRoot(String uri) {
        getWritableDatabase().delete("roots", "uri=?", new String[]{uri});
    }

    List<Root> roots() {
        List<Root> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT r.uri, r.name, COUNT(i.uri) FROM roots r LEFT JOIN items i ON i.root_uri=r.uri GROUP BY r.uri ORDER BY r.name COLLATE NOCASE", null)) {
            while (cursor.moveToNext()) result.add(new Root(cursor.getString(0), cursor.getString(1), cursor.getInt(2)));
        }
        return result;
    }

    List<Item> search(String text) {
        List<Item> result = new ArrayList<>();
        String needle = "%" + text.trim() + "%";
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT i.uri, i.root_uri, i.name, i.path, i.mime, i.size, i.modified, " +
                "COALESCE((SELECT group_concat(t.name, ' · ') FROM tags t WHERE t.item_uri=i.uri), '') " +
                "FROM items i WHERE i.name LIKE ? OR i.path LIKE ? OR EXISTS " +
                "(SELECT 1 FROM tags t WHERE t.item_uri=i.uri AND t.name LIKE ?) " +
                "ORDER BY i.name COLLATE NOCASE LIMIT 200", new String[]{needle, needle, needle})) {
            while (cursor.moveToNext()) result.add(new Item(cursor));
        }
        return result;
    }

    List<String> tags(String itemUri) {
        List<String> result = new ArrayList<>();
        try (Cursor cursor = getReadableDatabase().rawQuery(
                "SELECT name FROM tags WHERE item_uri=? ORDER BY name COLLATE NOCASE", new String[]{itemUri})) {
            while (cursor.moveToNext()) result.add(cursor.getString(0));
        }
        return result;
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
        if (db.update("items", values, "uri=?", new String[]{uri}) == 0) {
            db.insertOrThrow("items", null, values);
        }
    }

    void removeMissing(SQLiteDatabase db, String rootUri, String scanToken) {
        db.delete("items", "root_uri=? AND scan_token<>?", new String[]{rootUri, scanToken});
    }
}
