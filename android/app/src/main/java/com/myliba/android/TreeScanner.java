package com.myliba.android;

import android.content.ContentResolver;
import android.content.UriPermission;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.net.Uri;
import android.provider.DocumentsContract;
import org.json.JSONArray;

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.UUID;

final class TreeScanner {
    private static final class PendingDirectory {
        final String documentId;
        final String path;
        final String indexId;
        final List<String> segments;

        PendingDirectory(String documentId, String path, String indexId, List<String> segments) {
            this.documentId = documentId;
            this.path = path;
            this.indexId = indexId;
            this.segments = segments;
        }
    }

    private final ContentResolver resolver;
    private final LocalLibrary library;

    TreeScanner(ContentResolver resolver, LocalLibrary library) {
        this.resolver = resolver;
        this.library = library;
    }

    int scan(Uri treeUri) {
        String rootId = DocumentsContract.getTreeDocumentId(treeUri);
        boolean authorized=false;
        for(UriPermission grant:resolver.getPersistedUriPermissions())if(treeUri.equals(grant.getUri())&&grant.isReadPermission()){authorized=true;break;}
        if(!authorized)throw new SecurityException("索引目录读取授权已失效，请重新授权");
        Uri rootDocument=DocumentsContract.buildDocumentUriUsingTree(treeUri,rootId);
        String rootName;
        try(Cursor root=resolver.query(rootDocument,new String[]{DocumentsContract.Document.COLUMN_DOCUMENT_ID,DocumentsContract.Document.COLUMN_DISPLAY_NAME},null,null,null)){
            if(root==null||!root.moveToFirst())throw new IllegalStateException("索引目录暂不可用，保留已收录文件身份");
            rootName=root.getString(1);if(rootName==null||rootName.trim().isEmpty())rootName="Root";
        }
        String rootUri = treeUri.toString();
        String token = UUID.randomUUID().toString();
        ArrayDeque<PendingDirectory> pending = new ArrayDeque<>();
        Set<String> visited = new HashSet<>();
        int indexed = 0;
        SQLiteDatabase db = library.getWritableDatabase();
        db.beginTransaction();
        try {
            boolean preserveStructure=library.rootPreservesStructure(db,rootUri);
            String[] classificationIdentity=preserveStructure?library.classificationIdentity(db):null;
            List<String> rootSegments=new ArrayList<>();rootSegments.add(rootName);
            String rootIndex=preserveStructure?library.ensureClassificationSourceDirectory(db,classificationIdentity,rootUri,rootId,rootName,null,new JSONArray(rootSegments).toString()):null;
            pending.add(new PendingDirectory(rootId,"",rootIndex,rootSegments));
            while (!pending.isEmpty()) {
                PendingDirectory folder = pending.removeFirst();
                if (!visited.add(folder.documentId)) continue;
                Uri children = DocumentsContract.buildChildDocumentsUriUsingTree(treeUri, folder.documentId);
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
                        String path = folder.path.isEmpty() ? name : folder.path + "/" + name;
                        if (DocumentsContract.Document.MIME_TYPE_DIR.equals(mime)) {
                            List<String> segments=new ArrayList<>(folder.segments);segments.add(name);
                            String childIndex=folder.indexId==null?null:library.ensureClassificationSourceDirectory(db,classificationIdentity,rootUri,id,name,folder.indexId,new JSONArray(segments).toString());
                            pending.addLast(new PendingDirectory(id,path,childIndex,segments));
                            continue;
                        }
                        Uri document = DocumentsContract.buildDocumentUriUsingTree(treeUri, id);
                        long size = cursor.isNull(3) ? -1 : cursor.getLong(3);
                        long modified = cursor.isNull(4) ? 0 : cursor.getLong(4);
                        library.upsertItem(db, document.toString(), rootUri, name, path, mime, size, modified, token);
                        if(preserveStructure)library.classifyScannedItem(db,classificationIdentity,rootUri,id,document.toString(),folder.indexId);
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
