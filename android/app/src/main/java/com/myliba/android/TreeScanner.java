package com.myliba.android;

import android.content.ContentResolver;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.net.Uri;
import android.provider.DocumentsContract;

import java.util.ArrayDeque;
import java.util.HashSet;
import java.util.Set;
import java.util.UUID;

final class TreeScanner {
    private final ContentResolver resolver;
    private final LocalLibrary library;

    TreeScanner(ContentResolver resolver, LocalLibrary library) {
        this.resolver = resolver;
        this.library = library;
    }

    int scan(Uri treeUri) {
        String rootId = DocumentsContract.getTreeDocumentId(treeUri);
        String rootUri = treeUri.toString();
        String token = UUID.randomUUID().toString();
        ArrayDeque<String[]> pending = new ArrayDeque<>();
        Set<String> visited = new HashSet<>();
        pending.add(new String[]{rootId, ""});
        int indexed = 0;
        SQLiteDatabase db = library.getWritableDatabase();
        db.beginTransaction();
        try {
            while (!pending.isEmpty()) {
                String[] folder = pending.removeFirst();
                if (!visited.add(folder[0])) continue;
                Uri children = DocumentsContract.buildChildDocumentsUriUsingTree(treeUri, folder[0]);
                String[] columns = {
                        DocumentsContract.Document.COLUMN_DOCUMENT_ID,
                        DocumentsContract.Document.COLUMN_DISPLAY_NAME,
                        DocumentsContract.Document.COLUMN_MIME_TYPE,
                        DocumentsContract.Document.COLUMN_SIZE,
                        DocumentsContract.Document.COLUMN_LAST_MODIFIED
                };
                try (Cursor cursor = resolver.query(children, columns, null, null, null)) {
                    if (cursor == null) throw new IllegalStateException("目录提供方未返回文件列表");
                    while (cursor.moveToNext()) {
                        String id = cursor.getString(0);
                        String name = cursor.getString(1);
                        String mime = cursor.getString(2);
                        if (id == null || name == null) continue;
                        if (mime == null) mime = "application/octet-stream";
                        String path = folder[1].isEmpty() ? name : folder[1] + "/" + name;
                        if (DocumentsContract.Document.MIME_TYPE_DIR.equals(mime)) {
                            pending.addLast(new String[]{id, path});
                            continue;
                        }
                        Uri document = DocumentsContract.buildDocumentUriUsingTree(treeUri, id);
                        long size = cursor.isNull(3) ? -1 : cursor.getLong(3);
                        long modified = cursor.isNull(4) ? 0 : cursor.getLong(4);
                        library.upsertItem(db, document.toString(), rootUri, name, path, mime, size, modified, token);
                        indexed++;
                    }
                }
            }
            library.removeMissing(db, rootUri, token);
            db.setTransactionSuccessful();
        } finally {
            db.endTransaction();
        }
        return indexed;
    }
}
