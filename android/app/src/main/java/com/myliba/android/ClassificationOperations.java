package com.myliba.android;

import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.content.ContentValues;
import org.json.JSONArray;
import org.json.JSONObject;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

/**
 * Deterministic classification operation log and replay, mirroring the Go
 * node's semantics: every grouping edit is an envelope in the outbox, and the
 * projection is rebuilt from the operation log in (clock, author, counter,
 * opId) order, then swapped atomically.
 */
final class ClassificationOperations {
    static final String REPLAY_SUFFIX = "\u0000replay";

    static final class PendingException extends Exception {
        PendingException() { super("classification predecessors missing"); }
    }

    static final class QuarantineException extends Exception {
        QuarantineException(String reason) { super(reason); }
    }

    private ClassificationOperations() {}

    private static String[] jsonNames(JSONObject object) {
        if (object == null) return new String[0];
        List<String> names = new ArrayList<>();
        for (java.util.Iterator<String> keys = object.keys(); keys.hasNext();) names.add(keys.next());
        return names.toArray(new String[0]);
    }

    static boolean knownKind(String kind) {
        return "index.create".equals(kind) || "index.update".equals(kind) || "index.delete".equals(kind)
                || "index.memberships".equals(kind) || "index.resolve".equals(kind);
    }

    static String classificationAuthor(SQLiteDatabase db) {
        try (Cursor cursor = db.rawQuery("SELECT value FROM library_operation_identity WHERE key='classification-author'", null)) {
            if (cursor.moveToFirst()) return cursor.getString(0);
        }
        String author = "index-" + UUID.randomUUID();
        db.execSQL("INSERT INTO library_operation_identity(key,value) VALUES('classification-author',?)", new Object[]{author});
        return author;
    }

    private static long nextCounter(SQLiteDatabase db, String author) {
        try (Cursor cursor = db.rawQuery("SELECT next_counter FROM library_operation_authors WHERE author_id=?", new String[]{author})) {
            if (!cursor.moveToFirst()) {
                db.execSQL("INSERT INTO library_operation_authors(author_id,next_counter) VALUES(?,2)", new Object[]{author});
                return 1;
            }
            long counter = cursor.getLong(0);
            db.execSQL("UPDATE library_operation_authors SET next_counter=? WHERE author_id=?", new Object[]{counter + 1, author});
            return counter;
        }
    }

    static JSONObject operationContext(SQLiteDatabase db, String libraryId) {
        JSONObject context = new JSONObject();
        try (Cursor cursor = db.rawQuery("SELECT author_id,last_counter FROM library_operation_frontiers", null)) {
            while (cursor.moveToNext()) {
                try { context.put(cursor.getString(0), cursor.getLong(1)); } catch (org.json.JSONException impossible) { throw new IllegalStateException(impossible); }
            }
        }
        try (Cursor cursor = db.rawQuery("SELECT author_id,last_counter FROM classification_author_clocks WHERE library_id=?", new String[]{libraryId})) {
            while (cursor.moveToNext()) {
                try {
                    String author = cursor.getString(0);
                    context.put(author, Math.max(context.optLong(author, 0), cursor.getLong(1)));
                } catch (org.json.JSONException impossible) { throw new IllegalStateException(impossible); }
            }
        }
        return context;
    }

    private static long authorClock(SQLiteDatabase db, String libraryId, String author) {
        try (Cursor cursor = db.rawQuery("SELECT last_clock FROM classification_author_clocks WHERE library_id=? AND author_id=?", new String[]{libraryId, author})) {
            return cursor.moveToFirst() ? cursor.getLong(0) : 0;
        }
    }

    private static void bumpAuthorClock(SQLiteDatabase db, String libraryId, String author, long clock, long counter) {
        db.execSQL("INSERT INTO classification_author_clocks(library_id,author_id,last_clock,last_counter) VALUES(?,?,?,?) " +
                "ON CONFLICT(library_id,author_id) DO UPDATE SET last_clock=MAX(excluded.last_clock,last_clock),last_counter=MAX(excluded.last_counter,last_counter)",
                new Object[]{libraryId, author, clock, counter});
    }

    /** Records the domain envelope into the shared outbox and the replay log in the caller's transaction. */
    static String recordOperation(SQLiteDatabase db, String libraryId, String kind, String opId, JSONObject domain) {
        try {
            String author = classificationAuthor(db);
            long counter = nextCounter(db, author);
            JSONObject context = operationContext(db, libraryId);
            long clock = authorClock(db, libraryId, author);
            for (String predecessor : jsonNames(context)) {
                clock = Math.max(clock, authorClock(db, libraryId, predecessor));
            }
            clock++;
			String baselineId = "";
            domain.put("schemaVersion", 2);
            domain.put("author", author);
            domain.put("counter", counter);
            domain.put("clock", clock);
            domain.put("baselineId", baselineId);
            domain.put("context", context);
            String payload = domain.toString();
            String stamp = Instant.now().toString();
            db.execSQL("INSERT INTO library_operations(op_id,author_id,counter,kind,payload_json,created_at,state) VALUES(?,?,?,?,?,?,'pending')",
                    new Object[]{opId, author, counter, kind, payload, stamp});
            db.execSQL("INSERT INTO classification_operations(library_id,op_id,author_id,counter,clock,kind,payload_json,context_json,baseline_id,origin,received_at) VALUES(?,?,?,?,?,?,?,?,?,'local',?)",
                    new Object[]{libraryId, opId, author, counter, clock, kind, payload, context.toString(), baselineId, stamp});
            bumpAuthorClock(db, libraryId, author, clock, counter);
            return payload;
        } catch (org.json.JSONException failure) {
            throw new IllegalStateException(failure);
        }
    }

    private static void recordConflict(SQLiteDatabase db, String libraryId, String opId, String author, long counter, String kind, String reason, JSONObject detail) {
        db.execSQL("INSERT INTO classification_conflicts(library_id,op_id,author_id,counter,kind,reason,detail_json,seen_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(library_id,op_id) DO NOTHING",
                new Object[]{libraryId, opId, author, counter, kind, reason, detail == null ? "{}" : detail.toString(), Instant.now().toString()});
    }

    // ---- replay ----

    static void replay(SQLiteDatabase db, String libraryId, String nodeId) {
        if (libraryId == null || libraryId.isEmpty() || nodeId == null || nodeId.isEmpty()) {
            throw new IllegalStateException("分类索引身份不存在");
        }
        db.execSQL("PRAGMA defer_foreign_keys=ON");
        String replayLibrary = libraryId + REPLAY_SUFFIX;
        for (String table : new String[]{"classification_ancestors", "classification_memberships", "classification_removes", "classification_adds", "classification_nodes"}) {
            db.delete(table, "library_id=?", new String[]{replayLibrary});
        }
        List<String[]> operations = new ArrayList<>();
        try (Cursor cursor = db.rawQuery("SELECT op_id,author_id,counter,kind,payload_json FROM classification_operations WHERE library_id=? ORDER BY clock,author_id,counter,op_id", new String[]{libraryId})) {
            while (cursor.moveToNext()) operations.add(new String[]{cursor.getString(0), cursor.getString(1), String.valueOf(cursor.getLong(2)), cursor.getString(3), cursor.getString(4)});
        }
        for (String[] op : operations) {
            applyOp(db, libraryId, replayLibrary, op[0], op[1], Long.parseLong(op[2]), op[3], op[4]);
        }
        rebuildMemberships(db, replayLibrary);
        rebuildAncestors(db, replayLibrary);
        swapProjection(db, libraryId, replayLibrary);
    }

    private static void swapProjection(SQLiteDatabase db, String libraryId, String replayLibrary) {
        // Children before parents: ON DELETE CASCADE would otherwise wipe the
        // rows still needed for the copy.
        String[] tables = {"classification_ancestors", "classification_memberships", "classification_removes", "classification_adds", "classification_nodes"};
        for (String table : tables) db.delete(table, "library_id=?", new String[]{libraryId});
        String[][] swaps = {
                {"classification_nodes", "index_id,parent_id,name,name_key,origin,deleted,name_op_id,parent_op_id,created_at,updated_at"},
                {"classification_adds", "logical_file_id,index_id,add_token,origin,operation_id,created_at"},
                {"classification_removes", "logical_file_id,index_id,add_token,operation_id,removed_at"},
                {"classification_memberships", "logical_file_id,index_id,has_manual,has_automatic,updated_at"},
                {"classification_ancestors", "ancestor_id,descendant_id,depth"},
        };
        for (String[] swap : swaps) {
            db.execSQL("INSERT INTO " + swap[0] + "(library_id," + swap[1] + ") SELECT ?," + swap[1] + " FROM " + swap[0] + " WHERE library_id=?", new Object[]{libraryId, replayLibrary});
        }
        for (String table : tables) db.delete(table, "library_id=?", new String[]{replayLibrary});
    }

    static void rebuildMemberships(SQLiteDatabase db, String libraryId) {
        db.delete("classification_memberships", "library_id=?", new String[]{libraryId});
        db.execSQL("INSERT INTO classification_memberships(library_id,logical_file_id,index_id,has_manual,has_automatic,updated_at) " +
                        "SELECT a.library_id,a.logical_file_id,a.index_id,MAX(CASE WHEN a.origin IN ('manual','legacy-import') THEN 1 ELSE 0 END),MAX(CASE WHEN a.origin='automatic' THEN 1 ELSE 0 END),? " +
                        "FROM classification_adds a JOIN classification_nodes n ON n.library_id=a.library_id AND n.index_id=a.index_id AND n.deleted=0 " +
                        "LEFT JOIN classification_removes r ON r.library_id=a.library_id AND r.logical_file_id=a.logical_file_id AND r.index_id=a.index_id AND r.add_token=a.add_token " +
                        "WHERE a.library_id=? AND r.add_token IS NULL GROUP BY a.library_id,a.logical_file_id,a.index_id",
                new Object[]{Instant.now().toString(), libraryId});
    }

    static void rebuildAncestors(SQLiteDatabase db, String libraryId) {
        Map<String, String> parents = new LinkedHashMap<>();
        try (Cursor cursor = db.rawQuery("SELECT index_id,COALESCE(parent_id,'') FROM classification_nodes WHERE library_id=? AND deleted=0 ORDER BY index_id", new String[]{libraryId})) {
            while (cursor.moveToNext()) parents.put(cursor.getString(0), cursor.getString(1));
        }
        db.delete("classification_ancestors", "library_id=?", new String[]{libraryId});
        for (String descendant : parents.keySet()) {
            Map<String, Boolean> seen = new HashMap<>();
            String current = descendant;
            int depth = 0;
            while (!current.isEmpty()) {
                if (seen.containsKey(current) || depth > 1024) throw new IllegalStateException("索引层级存在循环或深度异常");
                seen.put(current, true);
                if (!parents.containsKey(current)) throw new IllegalStateException("索引上级不存在");
                db.execSQL("INSERT INTO classification_ancestors(library_id,ancestor_id,descendant_id,depth) VALUES(?,?,?,?)", new Object[]{libraryId, current, descendant, depth});
                current = parents.get(current);
                depth++;
            }
        }
    }

    // ---- operation materialization ----

    private static String[] replayNode(SQLiteDatabase db, String replayLibrary, String indexId) {
        try (Cursor cursor = db.rawQuery("SELECT COALESCE(parent_id,''),name,name_op_id,parent_op_id,deleted FROM classification_nodes WHERE library_id=? AND index_id=?", new String[]{replayLibrary, indexId})) {
            if (!cursor.moveToFirst()) return null;
            return new String[]{cursor.getString(0), cursor.getString(1), cursor.getString(2), cursor.getString(3), String.valueOf(cursor.getInt(4))};
        }
    }

    private static boolean nodeAlive(SQLiteDatabase db, String replayLibrary, String indexId) {
        try (Cursor cursor = db.rawQuery("SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0", new String[]{replayLibrary, indexId})) {
            cursor.moveToFirst();
            return cursor.getInt(0) != 0;
        }
    }

    private static boolean nodeExists(SQLiteDatabase db, String replayLibrary, String indexId) {
        try (Cursor cursor = db.rawQuery("SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=?", new String[]{replayLibrary, indexId})) {
            cursor.moveToFirst();
            return cursor.getInt(0) != 0;
        }
    }

    private static long count(SQLiteDatabase db, String sql, String... args) {
        try (Cursor cursor = db.rawQuery(sql, args)) {
            cursor.moveToFirst();
            return cursor.getLong(0);
        }
    }

    private static void applyOp(SQLiteDatabase db, String libraryId, String replayLibrary, String opId, String author, long counter, String kind, String payloadRaw) {
        JSONObject payload;
        try { payload = new JSONObject(payloadRaw); } catch (org.json.JSONException failure) {
            recordConflict(db, libraryId, opId, author, counter, kind, "invalidPayload", null);
            return;
        }
        JSONObject context = payload.optJSONObject("context");
        if (context == null) context = new JSONObject();
        switch (kind) {
            case "index.create": applyCreate(db, libraryId, replayLibrary, opId, author, counter, kind, payload); break;
            case "index.update":
            case "index.resolve": applyUpdate(db, libraryId, replayLibrary, opId, author, counter, kind, payload, context, kind.equals("index.resolve")); break;
            case "index.delete": applyDelete(db, libraryId, replayLibrary, opId, author, counter, kind, payload); break;
            case "index.memberships": applyMemberships(db, libraryId, replayLibrary, opId, author, counter, kind, payload); break;
            default: recordConflict(db, libraryId, opId, author, counter, kind, "unsupportedKind", null);
        }
    }

    private static void applyCreate(SQLiteDatabase db, String libraryId, String replayLibrary, String opId, String author, long counter, String kind, JSONObject payload) {
        String indexId = payload.optString("indexId"), name = LocalLibrary.normalizeClassificationName(payload.optString("name"));
        if (indexId.isEmpty() || !validName(name)) {
            recordConflict(db, libraryId, opId, author, counter, kind, "invalidPayload", null);
            return;
        }
        if (nodeExists(db, replayLibrary, indexId)) return;
        String parentId = payload.isNull("parentId") ? null : payload.optString("parentId", null);
        if (parentId != null && !nodeAlive(db, replayLibrary, parentId)) {
            JSONObject detail = new JSONObject();
            try { detail.put("parentId", parentId); } catch (org.json.JSONException ignored) {}
            recordConflict(db, libraryId, opId, author, counter, kind, "parentMissing", detail);
            return;
        }
        String origin = "automatic".equals(payload.optString("origin")) ? "automatic" : "manual";
        ContentValues values = new ContentValues();
        values.put("library_id", replayLibrary);
        values.put("index_id", indexId);
        if (parentId == null) values.putNull("parent_id"); else values.put("parent_id", parentId);
        values.put("name", name);
        values.put("name_key", LocalLibrary.classificationNameKey(name));
        values.put("origin", origin);
        values.put("name_op_id", opId);
        values.put("parent_op_id", opId);
        values.put("created_at", Instant.now().toString());
        values.put("updated_at", Instant.now().toString());
        db.insertOrThrow("classification_nodes", null, values);
        rebuildAncestors(db, replayLibrary);
    }

    /** A concurrent same-field edit by another author that this operation never observed wins. */
    private static boolean fieldWinner(SQLiteDatabase db, String libraryId, String opId, String author, JSONObject context, String priorOpId) {
        if (priorOpId.isEmpty() || priorOpId.equals(opId)) return false;
        try (Cursor cursor = db.rawQuery("SELECT author_id,counter FROM classification_operations WHERE library_id=? AND op_id=?", new String[]{libraryId, priorOpId})) {
            if (!cursor.moveToFirst()) return false;
            String priorAuthor = cursor.getString(0);
            long priorCounter = cursor.getLong(1);
            return !priorAuthor.equals(author) && context.optLong(priorAuthor, 0) < priorCounter;
        }
    }

    private static void applyUpdate(SQLiteDatabase db, String libraryId, String replayLibrary, String opId, String author, long counter, String kind, JSONObject payload, JSONObject context, boolean resolve) {
        String indexId = payload.optString("indexId");
        if (resolve && !payload.optString("conflictOpId").isEmpty()) {
            db.execSQL("UPDATE classification_conflicts SET resolved=1 WHERE library_id=? AND op_id=?", new Object[]{libraryId, payload.optString("conflictOpId")});
        }
        String[] node = replayNode(db, replayLibrary, indexId);
        if (node == null || !"0".equals(node[4])) {
            JSONObject detail = new JSONObject();
            try { detail.put("indexId", indexId); } catch (org.json.JSONException ignored) {}
            recordConflict(db, libraryId, opId, author, counter, kind, "indexNotFound", detail);
            return;
        }
        JSONObject patch = payload.optJSONObject("patch");
        if (patch == null || patch.length() == 0 || patch.length() > 2) {
            recordConflict(db, libraryId, opId, author, counter, kind, "invalidPatch", null);
            return;
        }
        String targetName = node[1], nameOp = node[2], parentOp = node[3];
        String targetParent = node[0].isEmpty() ? null : node[0];
        boolean rename = false, move = false;
        for (String field : jsonNames(patch)) {
            if (field.equals("name")) {
                String candidate = LocalLibrary.normalizeClassificationName(patch.optString("name", ""));
                if (!validName(candidate)) {
                    recordConflict(db, libraryId, opId, author, counter, kind, "invalidPatch", null);
                    return;
                }
                targetName = candidate;
                rename = true;
            } else if (field.equals("parentId")) {
                if (patch.isNull("parentId")) {
                    targetParent = null;
                } else {
                    String value = patch.optString("parentId", "").trim();
                    if (value.isEmpty()) {
                        recordConflict(db, libraryId, opId, author, counter, kind, "invalidPatch", null);
                        return;
                    }
                    targetParent = value;
                }
                move = true;
            } else {
                recordConflict(db, libraryId, opId, author, counter, kind, "invalidPatch", null);
                return;
            }
        }
        if (rename && fieldWinner(db, libraryId, opId, author, context, nameOp) && !resolve) {
            JSONObject detail = new JSONObject();
            try { detail.put("indexId", indexId); detail.put("name", targetName); } catch (org.json.JSONException ignored) {}
            recordConflict(db, libraryId, opId, author, counter, kind, "concurrentName", detail);
            rename = false;
        }
        if (move) {
            if (targetParent != null && targetParent.equals(indexId)) {
                JSONObject detail = new JSONObject();
                try { detail.put("indexId", indexId); } catch (org.json.JSONException ignored) {}
                recordConflict(db, libraryId, opId, author, counter, kind, "indexCycle", detail);
                return;
            }
            if (targetParent != null) {
                if (!nodeAlive(db, replayLibrary, targetParent)) {
                    JSONObject detail = new JSONObject();
                    try { detail.put("indexId", indexId); detail.put("parentId", targetParent); } catch (org.json.JSONException ignored) {}
                    recordConflict(db, libraryId, opId, author, counter, kind, "parentMissing", detail);
                    return;
                }
                if (count(db, "SELECT COUNT(*) FROM classification_ancestors WHERE library_id=? AND ancestor_id=? AND descendant_id=?", replayLibrary, indexId, targetParent) != 0) {
                    JSONObject detail = new JSONObject();
                    try { detail.put("indexId", indexId); detail.put("parentId", targetParent); } catch (org.json.JSONException ignored) {}
                    recordConflict(db, libraryId, opId, author, counter, kind, "indexCycle", detail);
                    return;
                }
            }
            if (fieldWinner(db, libraryId, opId, author, context, parentOp) && !resolve) {
                JSONObject detail = new JSONObject();
                try { detail.put("indexId", indexId); } catch (org.json.JSONException ignored) {}
                recordConflict(db, libraryId, opId, author, counter, kind, "concurrentMove", detail);
                move = false;
            }
        }
        if (!rename && !move) return;
        if (rename) nameOp = opId;
        if (move) parentOp = opId;
        ContentValues values = new ContentValues();
        if (targetParent == null) values.putNull("parent_id"); else values.put("parent_id", targetParent);
        values.put("name", targetName);
        values.put("name_key", LocalLibrary.classificationNameKey(targetName));
        values.put("name_op_id", nameOp);
        values.put("parent_op_id", parentOp);
        values.put("updated_at", Instant.now().toString());
        db.update("classification_nodes", values, "library_id=? AND index_id=?", new String[]{replayLibrary, indexId});
        rebuildAncestors(db, replayLibrary);
    }

    private static void applyDelete(SQLiteDatabase db, String libraryId, String replayLibrary, String opId, String author, long counter, String kind, JSONObject payload) {
        String indexId = payload.optString("indexId");
        String[] node = replayNode(db, replayLibrary, indexId);
        if (node == null || !"0".equals(node[4])) {
            JSONObject detail = new JSONObject();
            try { detail.put("indexId", indexId); } catch (org.json.JSONException ignored) {}
            recordConflict(db, libraryId, opId, author, counter, kind, "indexNotFound", detail);
            return;
        }
        long children = count(db, "SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0", replayLibrary, indexId);
        long memberships = count(db, "SELECT COUNT(*) FROM classification_memberships WHERE library_id=? AND index_id=?", replayLibrary, indexId);
        if (children != 0 || memberships != 0) {
            JSONObject detail = new JSONObject();
            try { detail.put("indexId", indexId); } catch (org.json.JSONException ignored) {}
            recordConflict(db, libraryId, opId, author, counter, kind, "indexNotEmpty", detail);
            return;
        }
        db.execSQL("UPDATE classification_nodes SET deleted=1,name_op_id=?,parent_op_id=?,updated_at=? WHERE library_id=? AND index_id=?",
                new Object[]{opId, opId, Instant.now().toString(), replayLibrary, indexId});
        rebuildAncestors(db, replayLibrary);
    }

    private static void applyMemberships(SQLiteDatabase db, String libraryId, String replayLibrary, String opId, String author, long counter, String kind, JSONObject payload) {
        JSONArray files = payload.optJSONArray("files");
        if (files == null || files.length() == 0 || files.length() > 200) {
            recordConflict(db, libraryId, opId, author, counter, kind, "invalidPayload", null);
            return;
        }
        try {
            for (int at = 0; at < files.length(); at++) {
                JSONObject file = files.getJSONObject(at);
                JSONArray adds = file.optJSONArray("adds"), removes = file.optJSONArray("removes");
                for (int i = 0; adds != null && i < adds.length(); i++) {
                    String indexId = adds.getJSONObject(i).optString("indexId");
                    if (!nodeAlive(db, replayLibrary, indexId)) {
                        JSONObject detail = new JSONObject();
                        detail.put("indexId", indexId); detail.put("fileId", file.optString("fileId"));
                        recordConflict(db, libraryId, opId, author, counter, kind, "indexNotFound", detail);
                        return;
                    }
                }
                for (int i = 0; removes != null && i < removes.length(); i++) {
                    String indexId = removes.getJSONObject(i).optString("indexId");
                    if (!nodeAlive(db, replayLibrary, indexId)) {
                        JSONObject detail = new JSONObject();
                        detail.put("indexId", indexId); detail.put("fileId", file.optString("fileId"));
                        recordConflict(db, libraryId, opId, author, counter, kind, "indexNotFound", detail);
                        return;
                    }
                }
            }
            String stamp = Instant.now().toString();
            for (int at = 0; at < files.length(); at++) {
                JSONObject file = files.getJSONObject(at);
                String fileId = file.optString("fileId");
                if (fileId.isEmpty() || fileId.length() > 160) continue;
                if (count(db, "SELECT COUNT(*) FROM library_file_deletions WHERE file_id=?", fileId) != 0) continue;
                JSONArray adds = file.optJSONArray("adds"), removes = file.optJSONArray("removes");
                for (int i = 0; adds != null && i < adds.length(); i++) {
                    JSONObject add = adds.getJSONObject(i);
                    String token = add.optString("token");
                    if (token.isEmpty() || token.length() > 200) continue;
                    String origin = add.optString("origin");
                    if (!origin.equals("automatic") && !origin.equals("legacy-import")) origin = "manual";
                    ContentValues values = new ContentValues();
                    values.put("library_id", replayLibrary);
                    values.put("logical_file_id", fileId);
                    values.put("index_id", add.optString("indexId"));
                    values.put("add_token", token);
                    values.put("origin", origin);
                    values.put("operation_id", opId);
                    values.put("created_at", stamp);
                    db.insertWithOnConflict("classification_adds", null, values, SQLiteDatabase.CONFLICT_IGNORE);
                }
                for (int i = 0; removes != null && i < removes.length(); i++) {
                    JSONObject remove = removes.getJSONObject(i);
                    String indexId = remove.optString("indexId");
                    JSONObject observedFrontier = remove.optJSONObject("observedFrontier");
                    if (observedFrontier != null) {
                        db.execSQL("INSERT INTO classification_removes(library_id,logical_file_id,index_id,add_token,operation_id,removed_at) " +
                                        "SELECT ?,?,?,a.add_token,?,? FROM classification_adds a JOIN classification_operations o ON o.library_id=? AND o.op_id=a.operation_id " +
                                        "WHERE a.library_id=? AND a.logical_file_id=? AND a.index_id=? AND o.author_id=? AND o.counter<=? ON CONFLICT DO NOTHING",
                                new Object[]{replayLibrary, fileId, indexId, opId, stamp, libraryId, replayLibrary, fileId, indexId, observedFrontier.optString("author"), observedFrontier.optLong("counter")});
                    }
                    JSONArray tokens = remove.optJSONArray("observedTokens");
                    for (int t = 0; tokens != null && t < tokens.length(); t++) {
                        String token = tokens.optString(t);
                        if (token.isEmpty() || token.length() > 200) continue;
                        ContentValues values = new ContentValues();
                        values.put("library_id", replayLibrary);
                        values.put("logical_file_id", fileId);
                        values.put("index_id", indexId);
                        values.put("add_token", token);
                        values.put("operation_id", opId);
                        values.put("removed_at", stamp);
                        db.insertWithOnConflict("classification_removes", null, values, SQLiteDatabase.CONFLICT_IGNORE);
                    }
                }
            }
        } catch (org.json.JSONException failure) {
            recordConflict(db, libraryId, opId, author, counter, kind, "invalidPayload", null);
            return;
        }
        rebuildMemberships(db, replayLibrary);
    }

    static boolean validName(String raw) {
        String name = LocalLibrary.normalizeClassificationName(raw);
        if (name.isEmpty() || name.codePointCount(0, name.length()) > 255) return false;
        for (int at = 0; at < name.length();) {
            int value = name.codePointAt(at);
            if (value == 0 || Character.isISOControl(value)) return false;
            at += Character.charCount(value);
        }
        return true;
    }

    // ---- receive ----

    /** Validates causality and clock, persists the envelope, and replays in the caller's transaction. */
    static void receiveOperation(SQLiteDatabase db, String libraryId, String nodeId, String opId, String author, long counter, String kind, String payloadRaw) throws PendingException, QuarantineException {
        if (!knownKind(kind)) throw new QuarantineException("unsupportedKind");
        JSONObject payload;
        try { payload = new JSONObject(payloadRaw); } catch (org.json.JSONException failure) { throw new QuarantineException("unsupportedSchema"); }
        if (payload.optInt("schemaVersion") != 2 || payload.optLong("clock", 0) < 1) throw new QuarantineException("unsupportedSchema");
        long clock = payload.optLong("clock");
        JSONObject context = payload.optJSONObject("context");
        if (context == null) context = new JSONObject();
        String[] authors = jsonNames(context);
        for (String contextAuthor : authors) {
            long needed = context.optLong(contextAuthor, 0);
            if (needed <= 0) continue;
            long frontier = 0;
            try (Cursor cursor = db.rawQuery("SELECT last_counter FROM library_operation_frontiers WHERE author_id=?", new String[]{contextAuthor})) {
                if (cursor.moveToFirst()) frontier = cursor.getLong(0);
            }
            if (frontier >= needed) continue;
            long present = count(db, "SELECT (SELECT COUNT(*) FROM library_operations WHERE author_id=? AND counter=?) + (SELECT COUNT(*) FROM classification_operations WHERE library_id=? AND author_id=? AND counter=?)",
                    contextAuthor, String.valueOf(needed), libraryId, contextAuthor, String.valueOf(needed));
            if (present == 0) throw new PendingException();
        }
        for (String contextAuthor : authors) {
            long needed = context.optLong(contextAuthor, 0);
            if (needed <= 0) continue;
            try (Cursor cursor = db.rawQuery("SELECT clock FROM classification_operations WHERE library_id=? AND author_id=? AND counter=?", new String[]{libraryId, contextAuthor, String.valueOf(needed)})) {
                if (!cursor.moveToFirst()) continue;
                if (clock <= cursor.getLong(0)) throw new QuarantineException("invalidClock");
            }
        }
        if (count(db, "SELECT COUNT(*) FROM classification_operations WHERE library_id=? AND op_id=?", libraryId, opId) == 0) {
            db.execSQL("INSERT INTO classification_operations(library_id,op_id,author_id,counter,clock,kind,payload_json,context_json,baseline_id,origin,received_at) VALUES(?,?,?,?,?,?,?,?,?,'remote',?)",
                    new Object[]{libraryId, opId, author, counter, clock, kind, payloadRaw, context.toString(), payload.optString("baselineId"), Instant.now().toString()});
        }
        bumpAuthorClock(db, libraryId, author, clock, counter);
        replay(db, libraryId, nodeId);
    }
}
