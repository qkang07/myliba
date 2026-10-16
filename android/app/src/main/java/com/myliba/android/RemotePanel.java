package com.myliba.android;

import android.app.AlertDialog;
import android.app.DownloadManager;
import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.BitmapFactory;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.os.Environment;
import android.os.Handler;
import android.os.Looper;
import android.text.Editable;
import android.text.TextWatcher;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.View;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URISyntaxException;
import java.net.URL;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

final class RemotePanel {
    private static final int CANVAS = Color.rgb(245, 247, 245);
    private static final int SURFACE = Color.WHITE;
    private static final int LINE = Color.rgb(231, 235, 231);
    private static final int INK = Color.rgb(32, 42, 36);
    private static final int MUTED = Color.rgb(101, 113, 104);
    private static final int GREEN = Color.rgb(47, 98, 72);
    private static final int GREEN_SOFT = Color.rgb(234, 242, 236);
    private static final int AMBER = Color.rgb(138, 99, 49);
    private static final int PAGE_SIZE = 48;

    private final MainActivity activity;
    private final LinearLayout container;
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final Set<String> selectedIds = new LinkedHashSet<>();
    private final Runnable searchRunnable = () -> loadResources(false);
    private String serverUrl;
    private String sessionCookie;
    private boolean authenticated;
    private boolean loading;
    private int generation;
    private int offset;
    private int total;
    private String selectedCollectionId;
    private JSONArray resources = new JSONArray();
    private JSONArray folders = new JSONArray();
    private JSONArray collections = new JSONArray();
    private JSONArray roots = new JSONArray();
    private JSONArray tags = new JSONArray();
    private JSONArray attributes = new JSONArray();
    private RemoteFilters filters = new RemoteFilters();

    private LinearLayout results;
    private TextView resultStatus;
    private TextView connectionStatus;
    private TextView activeFolderLabel;
    private LinearLayout filterSummary;
    private LinearLayout selectionBar;
    private EditText searchInput;
    private LinearLayout pageRoot;

    private static final class RemoteFilters {
        String query = "";
        final ArrayList<String> tags = new ArrayList<>();
        final ArrayList<String> attributes = new ArrayList<>();
        String kind = "";
        String rootId = "";
        String folderId = "all";
        boolean unorganized;
        String sort = "modified_desc";

        JSONObject json() throws JSONException {
            JSONObject result = new JSONObject();
            result.put("query", query);
            result.put("tags", new JSONArray(tags));
            result.put("attributes", new JSONArray(attributes));
            result.put("kind", kind);
            result.put("rootId", rootId);
            result.put("folderId", folderId);
            result.put("unorganized", unorganized);
            result.put("sort", sort);
            return result;
        }

        RemoteFilters copy() {
            RemoteFilters copy = new RemoteFilters();
            copy.query = query;
            copy.tags.addAll(tags);
            copy.attributes.addAll(attributes);
            copy.kind = kind;
            copy.rootId = rootId;
            copy.folderId = folderId;
            copy.unorganized = unorganized;
            copy.sort = sort;
            return copy;
        }

        static RemoteFilters from(JSONObject json) {
            RemoteFilters result = new RemoteFilters();
            result.query = json.optString("query", "");
            readArray(json.optJSONArray("tags"), result.tags);
            readArray(json.optJSONArray("attributes"), result.attributes);
            result.kind = json.optString("kind", "");
            result.rootId = json.optString("rootId", "");
            result.folderId = json.optString("folderId", "all");
            result.unorganized = json.optBoolean("unorganized", false);
            result.sort = json.optString("sort", "modified_desc");
            return result;
        }

        private static void readArray(JSONArray source, List<String> target) {
            if (source == null) return;
            for (int i = 0; i < source.length(); i++) {
                String value = source.optString(i, "");
                if (!value.isEmpty()) target.add(value);
            }
        }
    }

    private static final class ApiFailure extends Exception {
        final int status;
        ApiFailure(int status, String message) { super(message); this.status = status; }
    }

    RemotePanel(MainActivity activity) {
        this.activity = activity;
        container = activity.column();
        serverUrl = activity.getPreferences(Context.MODE_PRIVATE).getString("server_url", "");
        sessionCookie = activity.getPreferences(Context.MODE_PRIVATE).getString("server_cookie", "");
    }

    void attach(LinearLayout page) {
        if (container.getParent() instanceof LinearLayout) ((LinearLayout) container.getParent()).removeView(container);
        page.addView(container, new LinearLayout.LayoutParams(-1, -1));
        if (serverUrl.isEmpty()) showAddressForm();
        else if (!authenticated && sessionCookie.isEmpty()) showLoginForm();
        else if (!authenticated) checkSession();
        else if (pageRoot == null) loadLibrary();
        else container.addView(pageRoot, new LinearLayout.LayoutParams(-1, -1));
    }

    private void showAddressForm() {
        container.removeAllViews();
        ScrollView scroll = new ScrollView(activity);
        container.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
        LinearLayout body = activity.column();
        body.setPadding(activity.dp(20), activity.dp(30), activity.dp(20), activity.dp(30));
        scroll.addView(body);
        body.addView(brand());
        TextView eyebrow = label("PRIVATE LIBRARY · REMOTE ACCESS");
        body.addView(eyebrow, top(30));
        body.addView(activity.text("连接你的资源空间", 26, INK, true), top(8));
        body.addView(activity.text("通过 HTTPS 安全访问已配置的 Myliba Hub。登录后可搜索、筛选和整理服务端资源。", 14, MUTED, false), top(8));
        LinearLayout card = card();
        card.setPadding(activity.dp(16), activity.dp(17), activity.dp(16), activity.dp(16));
        card.addView(activity.text("服务端地址", 14, INK, true));
        EditText address = input("https://library.example.com");
        address.setInputType(android.text.InputType.TYPE_CLASS_TEXT | android.text.InputType.TYPE_TEXT_VARIATION_URI);
        card.addView(address, top(10));
        card.addView(primaryButton("连接服务端", () -> connect(address.getText().toString())), top(12));
        body.addView(card, top(22));
        body.addView(activity.text("地址只需填写站点根路径，例如 https://你的域名。", 12, MUTED, false), top(11));
    }

    private void showLoginForm() {
        container.removeAllViews();
        ScrollView scroll = new ScrollView(activity);
        container.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
        LinearLayout body = activity.column();
        body.setPadding(activity.dp(20), activity.dp(28), activity.dp(20), activity.dp(28));
        scroll.addView(body);
        LinearLayout top = activity.row();
        top.setGravity(Gravity.CENTER_VERTICAL);
        top.addView(brand(), new LinearLayout.LayoutParams(0, -2, 1));
        top.addView(quietButton("修改地址", this::editAddress));
        body.addView(top);
        body.addView(label("REMOTE LIBRARY"), top(32));
        body.addView(activity.text("欢迎回来", 25, INK, true), top(8));
        body.addView(activity.text("登录到你的 Myliba Hub，文件仍保存在家中设备。", 13, MUTED, false), top(7));
        LinearLayout card = card();
        card.setPadding(activity.dp(16), activity.dp(16), activity.dp(16), activity.dp(16));
        card.addView(activity.text("服务端", 11, MUTED, false));
        TextView host = activity.text(serverUrl, 13, INK, true);
        host.setMaxLines(1);
        host.setEllipsize(TextUtils.TruncateAt.MIDDLE);
        card.addView(host, top(4));
        EditText password = input("Web 密码");
        password.setInputType(android.text.InputType.TYPE_CLASS_TEXT | android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);
        card.addView(password, top(14));
        TextView error = activity.text("", 12, 0xffa24f45, false);
        card.addView(error, top(6));
        card.addView(primaryButton("登录并打开资源库", () -> login(password.getText().toString(), error)), top(8));
        body.addView(card, top(20));
    }

    private void connect(String input) {
        try {
            URI parsed = new URI(input.trim());
            if (!"https".equalsIgnoreCase(parsed.getScheme()) || parsed.getHost() == null ||
                    parsed.getRawUserInfo() != null || parsed.getRawQuery() != null || parsed.getRawFragment() != null ||
                    (parsed.getRawPath() != null && !parsed.getRawPath().isEmpty() && !"/".equals(parsed.getRawPath()))) {
                throw new URISyntaxException(input, "invalid server URL");
            }
            String host = parsed.getHost().toLowerCase(java.util.Locale.ROOT);
            if (host.contains(":")) host = "[" + host + "]";
            serverUrl = "https://" + host + (parsed.getPort() > 0 && parsed.getPort() != 443 ? ":" + parsed.getPort() : "");
            sessionCookie = "";
            activity.getPreferences(Context.MODE_PRIVATE).edit().putString("server_url", serverUrl).remove("server_cookie").apply();
            checkSession();
        } catch (URISyntaxException error) {
            activity.toast("请输入有效的 HTTPS 站点根地址");
        }
    }

    private void editAddress() {
        EditText address = input(serverUrl);
        address.setText(serverUrl);
        new AlertDialog.Builder(activity).setTitle("服务端地址").setView(address)
                .setNegativeButton("取消", null)
                .setPositiveButton("连接", (dialog, which) -> connect(address.getText().toString()))
                .show();
    }

    private void checkSession() {
        showLoading("正在连接服务端…");
        worker.execute(() -> {
            try {
                request("GET", "/api/session", null);
                authenticated = true;
                loadLibraryData();
            } catch (ApiFailure failure) {
                if (failure.status == 401) {
                    activity.runOnUiThread(() -> showLoginForm());
                } else showError(failure.getMessage());
            } catch (Exception error) { showError(message(error)); }
        });
    }

    private void login(String password, TextView errorView) {
        if (password.trim().isEmpty()) { errorView.setText("请输入服务端密码"); return; }
        errorView.setText("正在验证…");
        worker.execute(() -> {
            try {
                JSONObject body = new JSONObject().put("password", password);
                request("POST", "/api/session", body);
                authenticated = true;
                activity.getPreferences(Context.MODE_PRIVATE).edit().putString("server_cookie", sessionCookie).apply();
                loadLibraryData();
            } catch (Exception exception) {
                activity.runOnUiThread(() -> errorView.setText(message(exception)));
            }
        });
    }

    private void loadLibrary() {
        showLoading("正在读取资源库…");
        worker.execute(this::loadLibraryData);
    }

    private void loadLibraryData() {
        try {
            JSONObject folderResult = request("GET", "/api/folders", null);
            JSONArray nextFolders = folderResult.optJSONArray("items");
            JSONArray nextCollections = requestArray("GET", "/api/collections", null);
            JSONArray nextRoots = requestArray("GET", "/api/roots", null);
            JSONObject session = request("GET", "/api/session", null);
            activity.runOnUiThread(() -> {
                folders = nextFolders == null ? new JSONArray() : nextFolders;
                collections = nextCollections;
                roots = nextRoots;
                authenticated = true;
                renderLibrary();
                connectionStatus.setText(session.optBoolean("nodeOnline", false) ? "家中节点在线" : "等待节点连接");
                loadResources(false);
                loadTagsAndAttributes();
            });
        } catch (Exception error) {
            if (error instanceof ApiFailure && ((ApiFailure) error).status == 401) {
                authenticated = false;
                sessionCookie = "";
                activity.getPreferences(Context.MODE_PRIVATE).edit().remove("server_cookie").apply();
                activity.runOnUiThread(this::showLoginForm);
            } else showError(message(error));
        }
    }

    private void renderLibrary() {
        container.removeAllViews();
        ScrollView scroll = new ScrollView(activity);
        container.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
        pageRoot = activity.column();
        pageRoot.setPadding(activity.dp(16), activity.dp(17), activity.dp(16), activity.dp(26));
        pageRoot.setBackgroundColor(CANVAS);
        scroll.addView(pageRoot);

        LinearLayout brandRow = activity.row();
        brandRow.setGravity(Gravity.CENTER_VERTICAL);
        brandRow.addView(brand(), new LinearLayout.LayoutParams(0, -2, 1));
        LinearLayout connection = activity.column();
        connection.setGravity(Gravity.RIGHT);
        connectionStatus = activity.text("正在连接…", 10, MUTED, false);
        connection.addView(connectionStatus);
        LinearLayout accountActions = activity.row();
        accountActions.addView(quietButton("地址", this::editAddress));
        accountActions.addView(quietButton("退出", this::logout));
        connection.addView(accountActions);
        brandRow.addView(connection);
        pageRoot.addView(brandRow);

        LinearLayout hero = card();
        hero.setPadding(activity.dp(15), activity.dp(15), activity.dp(15), activity.dp(14));
        hero.addView(label("个人资源空间"));
        hero.addView(activity.text("家中资源库", 21, INK, true), top(5));
        TextView host = activity.text(Uri.parse(serverUrl).getHost() + "  ·  " + roots.length() + " 个索引目录", 12, MUTED, false);
        host.setMaxLines(1);
        host.setEllipsize(TextUtils.TruncateAt.MIDDLE);
        hero.addView(host, top(4));
        pageRoot.addView(hero, top(17));

        pageRoot.addView(sectionTitle("文件夹", "管理"), top(22));
        LinearLayout folderRail = chipRail();
        addChip(folderRail, "全部资源", "all".equals(filters.folderId), () -> setFolder("all"));
        addChip(folderRail, "未归组", "unfiled".equals(filters.folderId), () -> setFolder("unfiled"));
        for (int i = 0; i < folders.length(); i++) {
            JSONObject folder = folders.optJSONObject(i);
            if (folder != null) addChip(folderRail, folder.optString("name"), folder.optString("id").equals(filters.folderId), () -> setFolder(folder.optString("id")));
        }
        pageRoot.addView(folderRail, top(8));
        pageRoot.addView(quietButton("＋ 新建文件夹", this::createFolder), top(4));

        pageRoot.addView(sectionTitle("智能集合", "＋ 保存"), top(18));
        LinearLayout collectionRail = chipRail();
        for (int i = 0; i < collections.length(); i++) {
            JSONObject collection = collections.optJSONObject(i);
            if (collection == null) continue;
            JSONObject savedFilters = collection.optJSONObject("filters");
            if (savedFilters != null && !filters.folderId.equals(savedFilters.optString("folderId", "all"))) continue;
            String id = collection.optString("id");
            String modified = id.equals(selectedCollectionId) && !matchesCurrentFilters(savedFilters) ? " · 已修改" : "";
            TextView chip = addChip(collectionRail, "◇ " + collection.optString("name") + modified, id.equals(selectedCollectionId), () -> chooseCollection(collection));
            chip.setOnLongClickListener(view -> { collectionActions(collection); return true; });
        }
        pageRoot.addView(collectionRail, top(7));

        LinearLayout heading = activity.row();
        heading.setGravity(Gravity.CENTER_VERTICAL);
        heading.addView(activity.text("资源", 18, INK, true), new LinearLayout.LayoutParams(0, -2, 1));
        resultStatus = activity.text("正在读取…", 11, MUTED, false);
        heading.addView(resultStatus);
        pageRoot.addView(heading, top(22));

        LinearLayout searchCard = card();
        searchCard.setOrientation(LinearLayout.HORIZONTAL);
        searchCard.setGravity(Gravity.CENTER_VERTICAL);
        searchCard.setPadding(activity.dp(12), 0, activity.dp(12), 0);
        TextView searchIcon = activity.text("⌕", 22, GREEN, true);
        searchCard.addView(searchIcon);
        searchInput = new EditText(activity);
        searchInput.setSingleLine(true);
        searchInput.setTextSize(13);
        searchInput.setTextColor(INK);
        searchInput.setHintTextColor(Color.rgb(154, 164, 156));
        searchInput.setHint("搜索文件名、路径或标签");
        searchInput.setBackgroundColor(Color.TRANSPARENT);
        searchInput.setPadding(activity.dp(9), activity.dp(8), activity.dp(8), activity.dp(8));
        searchInput.setText(filters.query);
        searchCard.addView(searchInput, new LinearLayout.LayoutParams(0, activity.dp(48), 1));
        searchInput.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            @Override public void onTextChanged(CharSequence s, int start, int before, int count) {
                filters.query = s.toString();
                handler.removeCallbacks(searchRunnable);
                handler.postDelayed(searchRunnable, 320);
            }
            @Override public void afterTextChanged(Editable s) {}
        });
        pageRoot.addView(searchCard, top(10));

        LinearLayout filterRail = chipRail();
        addChip(filterRail, "标签" + badge(filters.tags.size()), !filters.tags.isEmpty(), this::chooseTags);
        addChip(filterRail, "属性" + badge(filters.attributes.size()), !filters.attributes.isEmpty(), this::chooseAttributes);
        addChip(filterRail, "来源" + (filters.rootId.isEmpty() ? "" : " · 已选"), !filters.rootId.isEmpty(), this::chooseRoot);
        addChip(filterRail, filters.unorganized ? "✓ 待整理" : "待整理", filters.unorganized, () -> {
            filters.unorganized = !filters.unorganized;
            loadResources(false);
        });
        addChip(filterRail, sortLabel(filters.sort) + " ▾", false, this::chooseSort);
        addChip(filterRail, "清除", false, this::clearFilters);
        pageRoot.addView(filterRail, top(9));

        filterSummary = activity.column();
        pageRoot.addView(filterSummary, top(5));
        selectionBar = activity.column();
        pageRoot.addView(selectionBar, top(6));
        results = activity.column();
        pageRoot.addView(results, top(10));
        updateFilterSummary();
        renderSelectionBar();
    }

    private void loadResources(boolean append) {
        if (!authenticated || loading && append) return;
        loading = true;
        int requestGeneration = ++generation;
        int requestOffset = append ? offset : 0;
        RemoteFilters snapshot = filters.copy();
        if (resultStatus != null) resultStatus.setText("正在检索…");
        String path = resourcePath(snapshot, requestOffset);
        worker.execute(() -> {
            try {
                JSONObject response = request("GET", path, null);
                JSONArray items = response.optJSONArray("items");
                int count = response.optInt("total", 0);
                activity.runOnUiThread(() -> {
                    if (requestGeneration != generation || !authenticated || results == null) return;
                    loading = false;
                    total = count;
                    if (!append) {
                        resources = items == null ? new JSONArray() : items;
                        offset = resources.length();
                        selectedIds.clear();
                    } else {
                        if (items != null) for (int i = 0; i < items.length(); i++) resources.put(items.optJSONObject(i));
                        offset = resources.length();
                    }
                    renderResults();
                });
            } catch (Exception error) {
                activity.runOnUiThread(() -> {
                    if (requestGeneration != generation || results == null) return;
                    loading = false;
                    if (error instanceof ApiFailure && ((ApiFailure) error).status == 401) {
                        authenticated = false;
                        showLoginForm();
                    } else {
                        resultStatus.setText("读取失败 · " + message(error));
                    }
                });
            }
        });
    }

    private String resourcePath(RemoteFilters value, int pageOffset) {
        StringBuilder query = new StringBuilder("/api/resources?q=").append(enc(value.query))
                .append("&unorganized=").append(value.unorganized ? "1" : "")
                .append("&kind=").append(enc(value.kind))
                .append("&rootId=").append(enc(value.rootId))
                .append("&folderId=").append(enc(value.folderId))
                .append("&sort=").append(enc(value.sort))
                .append("&limit=").append(PAGE_SIZE).append("&offset=").append(pageOffset);
        for (String tag : value.tags) query.append("&tag=").append(enc(tag));
        for (String key : value.attributes) query.append("&attribute=").append(enc(key));
        return query.toString();
    }

    private void renderResults() {
        if (results == null) return;
        results.removeAllViews();
        resultStatus.setText("共 " + total + " 项");
        renderSelectionBar();
        if (resources.length() == 0) {
            LinearLayout empty = card();
            empty.setPadding(activity.dp(18), activity.dp(20), activity.dp(18), activity.dp(20));
            empty.addView(activity.text("没有找到匹配的资源", 15, INK, true));
            empty.addView(activity.text("调整关键词或筛选条件，再试一次。", 12, MUTED, false), top(5));
            results.addView(empty);
            return;
        }
        for (int i = 0; i < resources.length(); i++) {
            JSONObject resource = resources.optJSONObject(i);
            if (resource == null) continue;
            results.addView(resourceCard(resource), top(9));
        }
        if (offset < total) results.addView(primaryButton("加载更多资源", () -> loadResources(true)), top(12));
    }

    private View resourceCard(JSONObject resource) {
        LinearLayout card = card();
        card.setPadding(activity.dp(13), activity.dp(12), activity.dp(13), activity.dp(11));
        String id = resource.optString("id");
        String kind = resource.optString("kind");
        if ("image".equals(kind) || "video".equals(kind)) {
            ImageView preview = new ImageView(activity);
            preview.setScaleType(ImageView.ScaleType.CENTER_CROP);
            preview.setBackground(round(GREEN_SOFT, activity.dp(9), 0, 0));
            TextView placeholder = activity.text(typeSymbol(kind) + "  ·  轻触生成预览", 13, GREEN, true);
            placeholder.setGravity(Gravity.CENTER);
            LinearLayout previewBox = activity.column();
            previewBox.setGravity(Gravity.CENTER);
            previewBox.addView(preview, new LinearLayout.LayoutParams(-1, activity.dp(132)));
            previewBox.addView(placeholder, new LinearLayout.LayoutParams(-1, activity.dp(28)));
            previewBox.setOnClickListener(view -> generateThumbnail(id, preview, placeholder));
            card.addView(previewBox, top(1));
            loadThumbnail(resource, preview, placeholder, previewBox);
        }
        LinearLayout titleRow = activity.row();
        titleRow.setGravity(Gravity.CENTER_VERTICAL);
        TextView marker = activity.text(typeSymbol(kind), 18, GREEN, true);
        marker.setGravity(Gravity.CENTER);
        titleRow.addView(marker, new LinearLayout.LayoutParams(activity.dp(32), activity.dp(34)));
        LinearLayout copy = activity.column();
        copy.setPadding(activity.dp(9), 0, 0, 0);
        TextView name = activity.text(resource.optString("name", "未命名资源"), 14, INK, true);
        name.setMaxLines(2);
        name.setEllipsize(TextUtils.TruncateAt.END);
        copy.addView(name);
        TextView path = activity.text(resource.optString("path"), 10, MUTED, false);
        path.setSingleLine(true);
        path.setEllipsize(TextUtils.TruncateAt.MIDDLE);
        copy.addView(path, top(3));
        titleRow.addView(copy, new LinearLayout.LayoutParams(0, -2, 1));
        String status = resource.optBoolean("available", true) ? "可用" : "离线";
        TextView availability = activity.text(status, 10, resource.optBoolean("available", true) ? GREEN : AMBER, false);
        titleRow.addView(availability);
        card.addView(titleRow);
        LinearLayout metadata = activity.row();
        metadata.setGravity(Gravity.CENTER_VERTICAL);
        metadata.addView(pill(kindLabel(resource.optString("kind")), false));
        metadata.addView(activity.text("  " + formatSize(resource.optLong("size")) + "  ·  " + shortDate(resource.optString("modified")), 10, MUTED, false), new LinearLayout.LayoutParams(0, -2, 1));
        card.addView(metadata, top(8));
        JSONArray resourceTags = resource.optJSONArray("tags");
        if (resourceTags != null && resourceTags.length() > 0) {
            LinearLayout tagLine = activity.row();
            tagLine.setPadding(0, activity.dp(7), 0, 0);
            for (int i = 0; i < Math.min(resourceTags.length(), 4); i++) tagLine.addView(pill(resourceTags.optString(i), true));
            card.addView(tagLine);
        }
        LinearLayout actions = activity.row();
        actions.addView(quietButton("详情", () -> showResource(resource)));
        actions.addView(quietButton(selectedIds.contains(id) ? "✓ 已选择" : "选择", () -> {
            if (!selectedIds.add(id)) selectedIds.remove(id);
            renderResults();
        }));
        card.addView(actions, top(3));
        return card;
    }

    private void showResource(JSONObject item) {
        String id = item.optString("id");
        worker.execute(() -> {
            try {
                JSONObject detail = request("GET", "/api/resources/" + enc(id), null);
                activity.runOnUiThread(() -> showResourceDialog(detail));
            } catch (Exception error) { activity.runOnUiThread(() -> activity.toast(message(error))); }
        });
    }

    private void loadThumbnail(JSONObject resource, ImageView preview, TextView placeholder, View clickable) {
        String kind = resource.optString("kind");
        if (!"image".equals(kind) && !"video".equals(kind)) return;
        String id = resource.optString("id");
        String version = enc(resource.optLong("size") + "-" + resource.optString("modified"));
        worker.execute(() -> {
            try {
                byte[] bytes = requestBytes("GET", "/api/resources/" + enc(id) + "/thumbnail?v=" + version, null);
                android.graphics.Bitmap bitmap = BitmapFactory.decodeByteArray(bytes, 0, bytes.length);
                if (bitmap != null) activity.runOnUiThread(() -> {
                    preview.setImageBitmap(bitmap);
                    if (placeholder.getParent() == clickable) ((LinearLayout) clickable).removeView(placeholder);
                });
            } catch (Exception ignored) { }
        });
    }

    private void generateThumbnail(String id, ImageView preview, TextView placeholder) {
        placeholder.setText("正在生成预览…");
        worker.execute(() -> {
            try {
                request("POST", "/api/resources/" + enc(id) + "/thumbnail", null);
                byte[] bytes = requestBytes("GET", "/api/resources/" + enc(id) + "/thumbnail", null);
                android.graphics.Bitmap bitmap = BitmapFactory.decodeByteArray(bytes, 0, bytes.length);
                activity.runOnUiThread(() -> {
                    if (bitmap != null) {
                        preview.setImageBitmap(bitmap);
                        if (placeholder.getParent() instanceof LinearLayout) ((LinearLayout) placeholder.getParent()).removeView(placeholder);
                    } else placeholder.setText("无法读取预览");
                });
            } catch (Exception error) {
                activity.runOnUiThread(() -> placeholder.setText(message(error)));
            }
        });
    }

    private void showResourceDialog(JSONObject item) {
        String id = item.optString("id");
        LinearLayout body = activity.column();
        body.setPadding(activity.dp(18), activity.dp(4), activity.dp(18), activity.dp(8));
        body.addView(activity.text(item.optString("path"), 12, MUTED, false));
        body.addView(activity.text(kindLabel(item.optString("kind")) + "  ·  " + formatSize(item.optLong("size")), 12, MUTED, false), top(7));
        body.addView(activity.text(shortDate(item.optString("modified")), 11, MUTED, false), top(4));
        JSONArray itemTags = item.optJSONArray("tags");
        body.addView(activity.text(itemTags == null || itemTags.length() == 0 ? "暂无手动标签" : "手动标签", 12, INK, true), top(13));
        if (itemTags != null) {
            for (int i = 0; i < itemTags.length(); i++) {
                String tagName = itemTags.optString(i);
                body.addView(quietButton("标签 · " + tagName + "    移除", () -> removeTag(id, tagName)), top(3));
            }
        }
        body.addView(quietButton("＋ 添加标签", () -> addTag(id)), top(6));
        body.addView(quietButton("移动到文件夹", () -> moveResource(id)), top(5));
        body.addView(primaryButton("下载原文件", () -> download(id, item.optString("name"))), top(10));
        new AlertDialog.Builder(activity).setTitle(item.optString("name", "资源详情")).setView(body)
                .setPositiveButton("完成", null).show();
    }

    private void addTag(String id) {
        EditText input = input("例如：旅行");
        new AlertDialog.Builder(activity).setTitle("添加手动标签").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("添加", (dialog, which) -> mutate("POST", "/api/resources/" + enc(id) + "/tags", json("name", input.getText().toString())))
                .show();
    }

    private void removeTag(String id, String name) {
        worker.execute(() -> {
            try {
                JSONArray allTags = requestArray("GET", "/api/tags?folderId=" + enc(filters.folderId), null);
                String tagId = "";
                for (int i = 0; i < allTags.length(); i++) {
                    JSONObject tag = allTags.optJSONObject(i);
                    if (tag != null && name.equals(tag.optString("name"))) { tagId = tag.optString("id"); break; }
                }
                if (tagId.isEmpty()) throw new IllegalStateException("找不到此标签");
                request("DELETE", "/api/resources/" + enc(id) + "/tags/" + enc(tagId), null);
                activity.runOnUiThread(() -> loadResources(false));
            } catch (Exception error) { activity.runOnUiThread(() -> activity.toast(message(error))); }
        });
    }

    private void createFolder() {
        EditText input = input("文件夹名称");
        new AlertDialog.Builder(activity).setTitle("新建文件夹").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("创建", (dialog, which) -> mutate("POST", "/api/folders", json("name", input.getText().toString())))
                .show();
    }

    private void moveResource(String id) {
        String[] names = new String[folders.length() + 1];
        names[0] = "未归组";
        for (int i = 0; i < folders.length(); i++) names[i + 1] = folders.optJSONObject(i).optString("name");
        new AlertDialog.Builder(activity).setTitle("移动到文件夹").setItems(names, (dialog, which) -> {
            String folderId = which == 0 ? "unfiled" : folders.optJSONObject(which - 1).optString("id");
            try {
                JSONObject body = new JSONObject().put("ids", new JSONArray().put(id)).put("folderId", folderId);
                mutate("PUT", "/api/resources/folder", body);
            } catch (JSONException error) { activity.toast("无法准备移动请求"); }
        }).setNegativeButton("取消", null).show();
    }

    private void download(String id, String name) {
        try {
            Uri uri = Uri.parse(serverUrl + "/api/resources/" + enc(id) + "/file?download=1");
            DownloadManager.Request request = new DownloadManager.Request(uri);
            request.addRequestHeader("Cookie", sessionCookie);
            request.setTitle(name);
            request.setDescription("从 Myliba 服务端下载");
            request.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
            request.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name.replace('/', '_').replace('\\', '_'));
            ((DownloadManager) activity.getSystemService(Context.DOWNLOAD_SERVICE)).enqueue(request);
            activity.toast("已加入下载任务");
        } catch (RuntimeException error) { activity.toast("无法创建下载任务：" + message(error)); }
    }

    private void mutate(String method, String path, JSONObject body) {
        worker.execute(() -> {
            try {
                request(method, path, body);
                refreshMetadataThenList();
                activity.runOnUiThread(() -> activity.toast("已完成"));
            } catch (Exception error) { activity.runOnUiThread(() -> activity.toast(message(error))); }
        });
    }

    private void refreshMetadataThenList() {
        try {
            JSONObject folderResult = request("GET", "/api/folders", null);
            JSONArray nextFolders = folderResult.optJSONArray("items");
            JSONArray nextCollections = requestArray("GET", "/api/collections", null);
            activity.runOnUiThread(() -> {
                folders = nextFolders == null ? new JSONArray() : nextFolders;
                collections = nextCollections;
                if (pageRoot != null && authenticated) { renderLibrary(); loadResources(false); }
            });
        } catch (Exception error) { activity.runOnUiThread(() -> activity.toast(message(error))); }
    }

    private void chooseCollection(JSONObject collection) {
        JSONObject saved = collection.optJSONObject("filters");
        if (saved == null) return;
        filters = RemoteFilters.from(saved);
        selectedCollectionId = collection.optString("id");
        selectedIds.clear();
        if (searchInput != null) searchInput.setText(filters.query);
        handler.removeCallbacks(searchRunnable);
        renderLibrary();
        loadResources(false);
    }

    private void saveCollection() {
        EditText input = input("例如：最近的 PDF");
        new AlertDialog.Builder(activity).setTitle("保存为智能集合").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("保存", (dialog, which) -> {
                    try {
                        JSONObject body = new JSONObject().put("name", input.getText().toString().trim()).put("filters", filters.json());
                        mutate("POST", "/api/collections", body);
                    } catch (JSONException error) { activity.toast("无法保存筛选条件"); }
                }).show();
    }

    private void setFolder(String folderId) {
        filters = new RemoteFilters();
        filters.folderId = folderId;
        selectedCollectionId = null;
        selectedIds.clear();
        renderLibrary();
        loadResources(false);
        loadTagsAndAttributes();
    }

    private void loadTagsAndAttributes() {
        String folderId = filters.folderId;
        worker.execute(() -> {
            try {
                JSONArray nextTags = requestArray("GET", "/api/tags?folderId=" + enc(folderId), null);
                JSONArray nextAttributes = requestArray("GET", "/api/attribute-tags?folderId=" + enc(folderId), null);
                activity.runOnUiThread(() -> {
                    if (folderId.equals(filters.folderId)) { tags = nextTags; attributes = nextAttributes; }
                });
            } catch (Exception error) { activity.runOnUiThread(() -> activity.toast(message(error))); }
        });
    }

    private void chooseTags() {
        if (tags.length() == 0) { loadTagsAndAttributes(); activity.toast("正在读取标签，请稍后再试"); return; }
        showMultiChoice("选择标签（多个条件同时满足）", tags, "name", "count", filters.tags, values -> {
            filters.tags.clear(); filters.tags.addAll(values); loadResources(false);
        });
    }

    private void chooseAttributes() {
        if (attributes.length() == 0) { loadTagsAndAttributes(); activity.toast("正在读取属性，请稍后再试"); return; }
        ArrayList<JSONObject> choices = new ArrayList<>();
        for (int i = 0; i < attributes.length(); i++) {
            JSONObject item = attributes.optJSONObject(i);
            if (item != null) choices.add(item);
        }
        String[] groups = {"文件类型", "大小范围", "格式"};
        new AlertDialog.Builder(activity).setTitle("属性筛选").setItems(groups, (dialog, which) -> {
            String category = which == 0 ? "type" : which == 1 ? "size" : "extension";
            ArrayList<JSONObject> filtered = new ArrayList<>();
            for (JSONObject choice : choices) if (category.equals(choice.optString("category"))) filtered.add(choice);
            showAttributeChoices(category, filtered);
        }).setNegativeButton("关闭", null).show();
    }

    private void showAttributeChoices(String category, List<JSONObject> choices) {
        if (choices.isEmpty()) { activity.toast("没有可用的筛选项"); return; }
        String[] labels = new String[choices.size()];
        boolean[] checked = new boolean[choices.size()];
        for (int i = 0; i < choices.size(); i++) {
            JSONObject item = choices.get(i);
            String key = item.optString("key");
            labels[i] = item.optString("label") + " · " + item.optInt("count");
            checked[i] = filters.attributes.contains(key);
        }
        new AlertDialog.Builder(activity).setTitle("选择" + ("type".equals(category) ? "类型" : "size".equals(category) ? "大小" : "格式"))
                .setMultiChoiceItems(labels, checked, (dialog, which, value) -> checked[which] = value)
                .setNeutralButton("清除此类", (dialog, which) -> {
                    removeAttributes(category); renderLibrary(); loadResources(false);
                })
                .setNegativeButton("取消", null)
                .setPositiveButton("应用", (dialog, which) -> {
                    removeAttributes(category);
                    for (int i = 0; i < choices.size(); i++) if (checked[i]) filters.attributes.add(choices.get(i).optString("key"));
                    renderLibrary(); loadResources(false);
                }).show();
    }

    private void removeAttributes(String category) {
        String prefix = category + ":";
        for (int i = filters.attributes.size() - 1; i >= 0; i--) if (filters.attributes.get(i).startsWith(prefix)) filters.attributes.remove(i);
    }

    private interface MultiChoiceAction { void apply(List<String> values); }

    private void showMultiChoice(String title, JSONArray items, String valueKey, String countKey, List<String> selected, MultiChoiceAction action) {
        String[] labels = new String[items.length()];
        boolean[] checked = new boolean[items.length()];
        for (int i = 0; i < items.length(); i++) {
            JSONObject item = items.optJSONObject(i);
            if (item == null) continue;
            String value = item.optString(valueKey);
            labels[i] = value + " · " + item.optInt(countKey);
            checked[i] = selected.contains(value);
        }
        new AlertDialog.Builder(activity).setTitle(title)
                .setMultiChoiceItems(labels, checked, (dialog, which, value) -> checked[which] = value)
                .setNeutralButton("清除", (dialog, which) -> action.apply(new ArrayList<>()))
                .setNegativeButton("取消", null)
                .setPositiveButton("应用", (dialog, which) -> {
                    ArrayList<String> values = new ArrayList<>();
                    for (int i = 0; i < items.length(); i++) if (checked[i]) values.add(items.optJSONObject(i).optString(valueKey));
                    action.apply(values);
                }).show();
    }

    private void chooseSort() {
        String[] keys = {"modified_desc", "modified_asc", "name_asc", "name_desc", "size_desc"};
        String[] labels = {"最近修改", "最早修改", "名称 A–Z", "名称 Z–A", "文件大小"};
        int selected = 0;
        for (int i = 0; i < keys.length; i++) if (keys[i].equals(filters.sort)) selected = i;
        new AlertDialog.Builder(activity).setTitle("排序").setSingleChoiceItems(labels, selected, (dialog, which) -> {
            filters.sort = keys[which]; renderLibrary(); loadResources(false); dialog.dismiss();
        }).setNegativeButton("取消", null).show();
    }

    private void chooseRoot() {
        String[] names = new String[roots.length() + 1];
        names[0] = "全部索引目录";
        for (int i = 0; i < roots.length(); i++) {
            JSONObject root = roots.optJSONObject(i);
            names[i + 1] = root.optString("path") + " · " + root.optInt("count");
        }
        new AlertDialog.Builder(activity).setTitle("来源目录").setItems(names, (dialog, which) -> {
            filters.rootId = which == 0 ? "" : roots.optJSONObject(which - 1).optString("id");
            renderLibrary();
            loadResources(false);
        }).setNegativeButton("取消", null).show();
    }

    private void clearFilters() {
        String folder = filters.folderId;
        filters = new RemoteFilters();
        filters.folderId = folder;
        selectedIds.clear();
        if (searchInput != null) searchInput.setText("");
        handler.removeCallbacks(searchRunnable);
        renderLibrary();
        loadResources(false);
    }

    private void renderSelectionBar() {
        if (selectionBar == null) return;
        selectionBar.removeAllViews();
        if (total == 0) return;
        LinearLayout row = activity.row();
        row.addView(quietButton("选择本页", () -> {
            for (int i = 0; i < resources.length(); i++) selectedIds.add(resources.optJSONObject(i).optString("id"));
            renderResults();
        }));
        row.addView(quietButton("全选匹配结果 " + total, this::selectAllMatching));
        if (!selectedIds.isEmpty()) row.addView(quietButton("取消选择", () -> { selectedIds.clear(); renderResults(); }));
        selectionBar.addView(row);
        if (!selectedIds.isEmpty()) {
            selectionBar.addView(activity.text("已选择 " + selectedIds.size() + " 项", 11, GREEN, true));
            LinearLayout actions = activity.row();
            actions.addView(quietButton("批量加标签", this::batchTag));
            actions.addView(quietButton("移动文件夹", this::batchMove));
            selectionBar.addView(actions);
        }
    }

    private void selectAllMatching() {
        RemoteFilters snapshot = filters.copy();
        int requestGeneration = generation;
        worker.execute(() -> {
            try {
                LinkedHashSet<String> all = new LinkedHashSet<>();
                int start = 0;
                while (true) {
                    String path = resourcePath(snapshot, start).replace("/api/resources?", "/api/resources/selection?").replace("&limit=" + PAGE_SIZE, "&limit=100");
                    JSONObject page = request("GET", path, null);
                    JSONArray pageItems = page.optJSONArray("items");
                    if (pageItems == null || pageItems.length() == 0) break;
                    for (int i = 0; i < pageItems.length(); i++) all.add(pageItems.optJSONObject(i).optString("id"));
                    start += pageItems.length();
                    if (start >= page.optInt("total", 0) || start >= 20000) break;
                }
                activity.runOnUiThread(() -> {
                    if (requestGeneration != generation || !authenticated) return;
                    selectedIds.clear(); selectedIds.addAll(all); renderResults();
                });
            } catch (Exception error) { activity.runOnUiThread(() -> activity.toast("全选失败：" + message(error))); }
        });
    }

    private void batchTag() {
        EditText input = input("标签名称");
        new AlertDialog.Builder(activity).setTitle("为 " + selectedIds.size() + " 项添加标签").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("添加", (dialog, which) -> {
                    ArrayList<String> ids = new ArrayList<>(selectedIds);
                    String tagName = input.getText().toString();
                    selectedIds.clear();
                    worker.execute(() -> {
                        try {
                            for (int start = 0; start < ids.size(); start += 5000) {
                                JSONArray chunk = new JSONArray();
                                for (int i = start; i < Math.min(start + 5000, ids.size()); i++) chunk.put(ids.get(i));
                                request("POST", "/api/resources/tags", new JSONObject().put("ids", chunk).put("name", tagName));
                            }
                            refreshMetadataThenList();
                            activity.runOnUiThread(() -> activity.toast("批量标签已完成"));
                        } catch (Exception error) { activity.runOnUiThread(() -> { activity.toast("批量添加未全部完成：" + message(error)); loadResources(false); }); }
                    });
                }).show();
    }

    private void batchMove() {
        String[] names = new String[folders.length() + 1];
        names[0] = "未归组";
        for (int i = 0; i < folders.length(); i++) names[i + 1] = folders.optJSONObject(i).optString("name");
        new AlertDialog.Builder(activity).setTitle("移动 " + selectedIds.size() + " 项").setItems(names, (dialog, which) -> {
            ArrayList<String> ids = new ArrayList<>(selectedIds);
            selectedIds.clear();
            String folderId = which == 0 ? "unfiled" : folders.optJSONObject(which - 1).optString("id");
            worker.execute(() -> {
                try {
                    for (int start = 0; start < ids.size(); start += 1000) {
                        JSONArray chunk = new JSONArray();
                        for (int i = start; i < Math.min(start + 1000, ids.size()); i++) chunk.put(ids.get(i));
                        request("PUT", "/api/resources/folder", new JSONObject().put("ids", chunk).put("folderId", folderId));
                    }
                    refreshMetadataThenList();
                    activity.runOnUiThread(() -> activity.toast("批量移动已完成"));
                } catch (Exception error) { activity.runOnUiThread(() -> { activity.toast("批量移动未全部完成：" + message(error)); loadResources(false); }); }
            });
        }).setNegativeButton("取消", null).show();
    }

    private void updateFilterSummary() {
        if (filterSummary == null) return;
        filterSummary.removeAllViews();
        if (!filters.tags.isEmpty()) filterSummary.addView(activity.text("标签需同时匹配：" + TextUtils.join("、", filters.tags), 10, GREEN, false));
        if (!filters.attributes.isEmpty()) {
            ArrayList<String> labels = new ArrayList<>();
            for (String key : filters.attributes) labels.add(LocalLibrary.attributeLabel(key));
            filterSummary.addView(activity.text("属性：" + TextUtils.join("、", labels), 10, GREEN, false), top(3));
        }
        if (!filters.rootId.isEmpty()) filterSummary.addView(activity.text("来源：已筛选目录", 10, GREEN, false), top(3));
    }

    private boolean matchesCurrentFilters(JSONObject savedFilters) {
        if (savedFilters == null) return false;
        try { return filters.json().toString().equals(RemoteFilters.from(savedFilters).json().toString()); }
        catch (JSONException error) { return false; }
    }

    private void logout() {
        worker.execute(() -> {
            try { request("DELETE", "/api/session", null); } catch (Exception ignored) {}
            authenticated = false;
            sessionCookie = "";
            pageRoot = null;
            activity.getPreferences(Context.MODE_PRIVATE).edit().remove("server_cookie").apply();
            activity.runOnUiThread(this::showLoginForm);
        });
    }

    private JSONObject request(String method, String path, JSONObject body) throws Exception {
        String content = requestText(method, path, body);
        try { return content.isEmpty() ? new JSONObject() : new JSONObject(content); }
        catch (JSONException error) { throw new IllegalStateException("服务端响应格式无效", error); }
    }

    private String requestText(String method, String path, JSONObject body) throws Exception {
        URL url = new URL(serverUrl + path);
        HttpURLConnection connection = (HttpURLConnection) url.openConnection();
        connection.setRequestMethod(method);
        connection.setConnectTimeout(12000);
        connection.setReadTimeout(30000);
        connection.setInstanceFollowRedirects(false);
        connection.setUseCaches(false);
        connection.setRequestProperty("Accept", "application/json");
        connection.setRequestProperty("Origin", serverUrl);
        connection.setRequestProperty("Referer", serverUrl + "/");
        if (!sessionCookie.isEmpty()) connection.setRequestProperty("Cookie", sessionCookie);
        if (body != null) {
            connection.setDoOutput(true);
            connection.setRequestProperty("Content-Type", "application/json; charset=utf-8");
            try (OutputStream output = connection.getOutputStream()) { output.write(body.toString().getBytes(StandardCharsets.UTF_8)); }
        }
        int status = connection.getResponseCode();
        String setCookie = connection.getHeaderField("Set-Cookie");
        if (setCookie != null && !setCookie.isEmpty()) {
            sessionCookie = setCookie.split(";", 2)[0];
            activity.getPreferences(Context.MODE_PRIVATE).edit().putString("server_cookie", sessionCookie).apply();
        }
        InputStream stream = status >= 400 ? connection.getErrorStream() : connection.getInputStream();
        byte[] bytes = new byte[0];
        if (stream != null) {
            try (InputStream input = stream; ByteArrayOutputStream output = new ByteArrayOutputStream()) {
                byte[] buffer = new byte[8192];
                int read;
                while ((read = input.read(buffer)) != -1) output.write(buffer, 0, read);
                bytes = output.toByteArray();
            }
        }
        connection.disconnect();
        String responseBody = new String(bytes, StandardCharsets.UTF_8);
        if (status < 200 || status >= 300) {
            String errorMessage = "请求失败（" + status + "）";
            try { errorMessage = new JSONObject(responseBody).optString("error", errorMessage); }
            catch (JSONException ignored) {}
            throw new ApiFailure(status, errorMessage);
        }
        return responseBody;
    }

    private byte[] requestBytes(String method, String path, JSONObject body) throws Exception {
        URL url = new URL(serverUrl + path);
        HttpURLConnection connection = (HttpURLConnection) url.openConnection();
        try {
            connection.setRequestMethod(method);
            connection.setConnectTimeout(12000);
            connection.setReadTimeout(30000);
            connection.setInstanceFollowRedirects(false);
            connection.setUseCaches(false);
            connection.setRequestProperty("Accept", "image/jpeg");
            connection.setRequestProperty("Origin", serverUrl);
            connection.setRequestProperty("Referer", serverUrl + "/");
            if (!sessionCookie.isEmpty()) connection.setRequestProperty("Cookie", sessionCookie);
            int status = connection.getResponseCode();
            InputStream stream = status >= 400 ? connection.getErrorStream() : connection.getInputStream();
            byte[] bytes = new byte[0];
            if (stream != null) {
                try (InputStream input = stream; ByteArrayOutputStream output = new ByteArrayOutputStream()) {
                    byte[] buffer = new byte[8192];
                    int read;
                    while ((read = input.read(buffer)) != -1) output.write(buffer, 0, read);
                    bytes = output.toByteArray();
                }
            }
            if (status < 200 || status >= 300) {
                String errorMessage = "请求失败（" + status + "）";
                try { errorMessage = new JSONObject(new String(bytes, StandardCharsets.UTF_8)).optString("error", errorMessage); }
                catch (JSONException ignored) { }
                throw new ApiFailure(status, errorMessage);
            }
            return bytes;
        } finally { connection.disconnect(); }
    }

    private JSONArray requestArray(String method, String path, JSONObject body) throws Exception {
        String content = requestText(method, path, body);
        try {
            if (content.trim().startsWith("[")) return new JSONArray(content);
            return new JSONObject(content).optJSONArray("items");
        } catch (JSONException error) { throw new IllegalStateException("服务端列表格式无效", error); }
    }

    private void showLoading(String message) {
        container.removeAllViews();
        LinearLayout body = activity.column();
        body.setGravity(Gravity.CENTER);
        body.addView(activity.text(message, 14, MUTED, false));
        container.addView(body, new LinearLayout.LayoutParams(-1, -1));
    }

    private void showError(String message) {
        activity.runOnUiThread(() -> {
            container.removeAllViews();
            LinearLayout body = activity.column();
            body.setPadding(activity.dp(20), activity.dp(30), activity.dp(20), activity.dp(24));
            body.addView(brand());
            body.addView(activity.text("连接暂不可用", 22, INK, true), top(24));
            body.addView(activity.text(message, 13, MUTED, false), top(7));
            body.addView(primaryButton("重试", this::checkSession), top(14));
            body.addView(quietButton("修改服务端地址", this::editAddress), top(8));
            container.addView(body);
        });
    }

    private JSONObject json(String key, String value) {
        try { return new JSONObject().put(key, value); }
        catch (JSONException error) { return new JSONObject(); }
    }

    private static String enc(String value) {
        try { return URLEncoder.encode(value == null ? "" : value, "UTF-8"); }
        catch (Exception error) { return ""; }
    }

    private static String message(Exception error) {
        if (error instanceof ApiFailure) return error.getMessage();
        String value = error.getMessage();
        return value == null || value.trim().isEmpty() ? "网络连接失败" : value;
    }

    private LinearLayout brand() {
        LinearLayout row = activity.row();
        row.setGravity(Gravity.CENTER_VERTICAL);
        TextView mark = activity.text("M", 17, Color.WHITE, true);
        mark.setGravity(Gravity.CENTER);
        mark.setBackground(round(GREEN, activity.dp(11), 0, 0));
        row.addView(mark, new LinearLayout.LayoutParams(activity.dp(38), activity.dp(38)));
        LinearLayout copy = activity.column();
        copy.setPadding(activity.dp(10), 0, 0, 0);
        TextView name = activity.text("myliba", 16, INK, true);
        copy.addView(name);
        TextView sub = activity.text("PERSONAL LIBRARY", 9, MUTED, true);
        sub.setLetterSpacing(.08f);
        copy.addView(sub, top(2));
        row.addView(copy);
        return row;
    }

    private TextView label(String value) {
        TextView text = activity.text(value.toUpperCase(java.util.Locale.ROOT), 10, GREEN, true);
        text.setLetterSpacing(.05f);
        return text;
    }

    private LinearLayout sectionTitle(String title, String action) {
        LinearLayout row = activity.row();
        row.setGravity(Gravity.CENTER_VERTICAL);
        TextView label = activity.text(title, 13, INK, true);
        row.addView(label, new LinearLayout.LayoutParams(0, -2, 1));
        TextView actionView = quietButton(action, "＋ 保存".equals(action) ? this::saveCollection : this::manageFolders);
        row.addView(actionView);
        return row;
    }

    private void manageFolders() {
        if (folders.length() == 0) { createFolder(); return; }
        String[] names = new String[folders.length() + 1];
        names[0] = "＋ 新建文件夹";
        for (int i = 0; i < folders.length(); i++) names[i + 1] = folders.optJSONObject(i).optString("name");
        new AlertDialog.Builder(activity).setTitle("管理文件夹").setItems(names, (dialog, which) -> {
            if (which == 0) { createFolder(); return; }
            JSONObject folder = folders.optJSONObject(which - 1);
            String id = folder.optString("id");
            new AlertDialog.Builder(activity).setTitle(folder.optString("name"))
                    .setItems(new String[]{"重命名", "删除文件夹"}, (d, action) -> {
                        if (action == 0) renameFolder(folder);
                        else new AlertDialog.Builder(activity).setTitle("删除此文件夹？")
                                .setMessage("其中的资源会移至未归组，原文件和标签保留。")
                                .setNegativeButton("取消", null)
                                .setPositiveButton("删除", (confirm, choice) -> {
                                    if (id.equals(filters.folderId)) filters.folderId = "unfiled";
                                    mutate("DELETE", "/api/folders/" + enc(id), null);
                                }).show();
                    }).show();
        }).setNegativeButton("关闭", null).show();
    }

    private void renameFolder(JSONObject folder) {
        EditText input = input("文件夹名称");
        input.setText(folder.optString("name"));
        new AlertDialog.Builder(activity).setTitle("重命名文件夹").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("保存", (dialog, which) -> mutate("PUT", "/api/folders/" + enc(folder.optString("id")), json("name", input.getText().toString())))
                .show();
    }

    private void collectionActions(JSONObject collection) {
        String id = collection.optString("id");
        String[] actions = id.equals(selectedCollectionId)
                ? new String[]{"打开", "用当前筛选更新", "重命名", "删除"}
                : new String[]{"打开", "重命名", "删除"};
        new AlertDialog.Builder(activity).setTitle(collection.optString("name")).setItems(actions, (dialog, which) -> {
            if ("打开".equals(actions[which])) chooseCollection(collection);
            else if (actions[which].contains("更新")) {
                try {
                    JSONObject body = new JSONObject().put("name", collection.optString("name")).put("filters", filters.json());
                    mutate("PUT", "/api/collections/" + enc(id), body);
                } catch (JSONException error) { activity.toast("无法准备集合筛选条件"); }
            } else if (actions[which].contains("重命名")) renameCollection(collection);
            else new AlertDialog.Builder(activity).setTitle("删除此智能集合？")
                    .setNegativeButton("取消", null)
                    .setPositiveButton("删除", (d, choice) -> {
                        if (id.equals(selectedCollectionId)) selectedCollectionId = null;
                        mutate("DELETE", "/api/collections/" + enc(id), null);
                    }).show();
        }).show();
    }

    private void renameCollection(JSONObject collection) {
        EditText input = input("集合名称");
        input.setText(collection.optString("name"));
        new AlertDialog.Builder(activity).setTitle("重命名智能集合").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("保存", (dialog, which) -> {
                    try {
                        JSONObject body = new JSONObject().put("name", input.getText().toString()).put("filters", collection.optJSONObject("filters"));
                        mutate("PUT", "/api/collections/" + enc(collection.optString("id")), body);
                    } catch (JSONException error) { activity.toast("无法保存集合"); }
                }).show();
    }

    private LinearLayout chipRail() {
        HorizontalScrollView scroll = new HorizontalScrollView(activity);
        scroll.setHorizontalScrollBarEnabled(false);
        LinearLayout rail = activity.row();
        rail.setGravity(Gravity.CENTER_VERTICAL);
        rail.setPadding(0, 0, activity.dp(4), 0);
        scroll.addView(rail);
        LinearLayout wrapper = activity.row();
        wrapper.addView(scroll, new LinearLayout.LayoutParams(-1, activity.dp(38)));
        wrapper.setTag(rail);
        return wrapper;
    }

    private TextView addChip(LinearLayout wrapper, String title, boolean active, Runnable action) {
        Object tag = wrapper.getTag();
        LinearLayout rail = tag instanceof LinearLayout ? (LinearLayout) tag : wrapper;
        TextView chip = activity.text(title, 11, active ? GREEN : MUTED, active);
        chip.setGravity(Gravity.CENTER);
        chip.setPadding(activity.dp(11), 0, activity.dp(11), 0);
        chip.setBackground(round(active ? GREEN_SOFT : SURFACE, activity.dp(8), LINE, activity.dp(1)));
        chip.setOnClickListener(view -> action.run());
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-2, activity.dp(32));
        params.rightMargin = activity.dp(7);
        rail.addView(chip, params);
        return chip;
    }

    private TextView pill(String title, boolean tag) {
        TextView view = activity.text(title, 9, tag ? GREEN : MUTED, tag);
        view.setGravity(Gravity.CENTER);
        view.setPadding(activity.dp(7), 0, activity.dp(7), 0);
        view.setBackground(round(tag ? GREEN_SOFT : CANVAS, activity.dp(5), tag ? 0 : LINE, tag ? 0 : activity.dp(1)));
        return view;
    }

    private TextView quietButton(String title, Runnable action) {
        TextView button = activity.text(title, 11, GREEN, true);
        button.setGravity(Gravity.CENTER);
        button.setPadding(activity.dp(8), activity.dp(7), activity.dp(8), activity.dp(7));
        button.setOnClickListener(view -> action.run());
        return button;
    }

    private TextView primaryButton(String title, Runnable action) {
        TextView button = activity.text(title, 13, Color.WHITE, true);
        button.setGravity(Gravity.CENTER);
        button.setMinHeight(activity.dp(42));
        button.setPadding(activity.dp(13), activity.dp(9), activity.dp(13), activity.dp(9));
        button.setBackground(round(GREEN, activity.dp(8), 0, 0));
        button.setOnClickListener(view -> action.run());
        return button;
    }

    private EditText input(String hint) {
        EditText input = new EditText(activity);
        input.setSingleLine(true);
        input.setTextSize(14);
        input.setTextColor(INK);
        input.setHintTextColor(Color.rgb(154, 164, 156));
        input.setHint(hint);
        input.setPadding(activity.dp(12), activity.dp(9), activity.dp(12), activity.dp(9));
        input.setBackground(round(SURFACE, activity.dp(8), Color.rgb(220, 227, 221), activity.dp(1)));
        return input;
    }

    private LinearLayout card() {
        LinearLayout card = activity.column();
        card.setBackground(round(SURFACE, activity.dp(11), LINE, activity.dp(1)));
        return card;
    }

    private GradientDrawable round(int fill, int radius, int stroke, int strokeWidth) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        drawable.setCornerRadius(radius);
        if (strokeWidth > 0) drawable.setStroke(strokeWidth, stroke);
        return drawable;
    }

    private LinearLayout.LayoutParams top(int size) {
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2);
        params.topMargin = activity.dp(size);
        return params;
    }

    private String badge(int count) { return count == 0 ? "" : " · " + count; }

    private String sortLabel(String sort) {
        switch (sort) {
            case "modified_asc": return "最早修改";
            case "name_asc": return "名称 A–Z";
            case "name_desc": return "名称 Z–A";
            case "size_desc": return "文件大小";
            default: return "最近修改";
        }
    }

    private static String kindLabel(String kind) {
        switch (kind) {
            case "image": return "图片";
            case "video": return "视频";
            case "audio": return "音频";
            case "document": return "文档";
            default: return "文件";
        }
    }

    private static String typeSymbol(String kind) {
        switch (kind) {
            case "image": return "▧";
            case "video": return "▷";
            case "audio": return "♫";
            case "document": return "▤";
            default: return "▱";
        }
    }

    private static String formatSize(long size) {
        if (size < 0) return "大小未知";
        if (size < 1024) return size + " B";
        if (size < 1024 * 1024) return String.format(java.util.Locale.ROOT, "%.1f KB", size / 1024.0);
        if (size < 1024L * 1024 * 1024) return String.format(java.util.Locale.ROOT, "%.1f MB", size / (1024.0 * 1024));
        return String.format(java.util.Locale.ROOT, "%.1f GB", size / (1024.0 * 1024 * 1024));
    }

    private static String shortDate(String value) {
        if (value == null || value.length() < 10) return "修改时间未知";
        return value.substring(0, 10);
    }

    boolean canGoBack() { return false; }
    void goBack() {}

    void destroy() {
        generation++;
        handler.removeCallbacksAndMessages(null);
        worker.shutdown();
        pageRoot = null;
    }
}
