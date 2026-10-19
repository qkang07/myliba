package com.myliba.android;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.ArrayList;

final class LocalFilters {
    String query = "";
    final ArrayList<String> tags = new ArrayList<>();
    final ArrayList<String> attributes = new ArrayList<>();
    String rootUri = "";
    String folderId = "all";
    boolean unorganized;
    String sort = "modified_desc";

    LocalFilters copy() {
        LocalFilters result = new LocalFilters();
        result.query = query;
        result.tags.addAll(tags);
        result.attributes.addAll(attributes);
        result.rootUri = rootUri;
        result.folderId = folderId;
        result.unorganized = unorganized;
        result.sort = sort;
        return result;
    }

    String serialize() {
        JSONObject json = new JSONObject();
        try {
            json.put("query", query);
            json.put("tags", new JSONArray(tags));
            json.put("attributes", new JSONArray(attributes));
            json.put("rootUri", rootUri);
            json.put("folderId", folderId);
            json.put("unorganized", unorganized);
            json.put("sort", sort);
        } catch (JSONException error) {
            throw new IllegalStateException(error);
        }
        return json.toString();
    }

    static LocalFilters parse(String value) {
        LocalFilters filters = new LocalFilters();
        try {
            JSONObject json = new JSONObject(value);
            filters.query = json.optString("query", "");
            JSONArray tags = json.optJSONArray("tags");
            if (tags != null) for (int i = 0; i < tags.length(); i++) filters.tags.add(tags.getString(i));
            JSONArray attributes = json.optJSONArray("attributes");
            if (attributes != null) for (int i = 0; i < attributes.length(); i++) filters.attributes.add(attributes.getString(i));
            filters.rootUri = json.optString("rootUri", "");
            filters.folderId = json.optString("folderId", "all");
            filters.unorganized = json.optBoolean("unorganized", false);
            filters.sort = json.optString("sort", "modified_desc");
        } catch (JSONException error) {
            throw new IllegalArgumentException("常用查询筛选条件已损坏", error);
        }
        return filters;
    }
}
