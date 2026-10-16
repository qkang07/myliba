package com.myliba.android;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.UriPermission;
import android.database.sqlite.SQLiteConstraintException;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.DocumentsContract;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.Gravity;
import android.view.View;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import java.util.List;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private static final int PICK_TREE = 1;
    private static final int INK = Color.rgb(32, 42, 36);
    private static final int MUTED = Color.rgb(101, 113, 104);
    private static final int GREEN = Color.rgb(47, 98, 72);
    private static final int GREEN_SOFT = Color.rgb(234, 242, 236);
    private static final int CANVAS = Color.rgb(245, 247, 245);

    private final ExecutorService scanner = Executors.newSingleThreadExecutor();
    private LocalLibrary library;
    private RemotePanel remote;
    private LinearLayout page;
    private LinearLayout rootList;
    private LinearLayout resultList;
    private EditText search;
    private TextView scanStatus;
    private TextView resultStatus;
    private Button folderButton;
    private Button collectionButton;
    private Button filterButton;
    private Button sortButton;
    private LinearLayout selectionBar;
    private LocalFilters filters = new LocalFilters();
    private final LinkedHashSet<String> selectedUris = new LinkedHashSet<>();
    private final ArrayList<String> shownUris = new ArrayList<>();
    private String activeCollectionId;
    private int shownCount;
    private volatile int searchGeneration;
    private Button localTab;
    private Button serverTab;
    private boolean showingServer;
    private boolean scanning;

    @Override public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().setStatusBarColor(CANVAS);
        getWindow().setNavigationBarColor(CANVAS);
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR | View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        library = new LocalLibrary(this);
        remote = new RemotePanel(this);

        LinearLayout shell = new LinearLayout(this);
        shell.setOrientation(LinearLayout.VERTICAL);
        shell.setBackgroundColor(CANVAS);
        setContentView(shell);
        if (Build.VERSION.SDK_INT >= 35) {
            getWindow().setDecorFitsSystemWindows(false);
            shell.setOnApplyWindowInsetsListener((view, insets) -> {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars());
                android.graphics.Insets ime = insets.getInsets(WindowInsets.Type.ime());
                shell.setPadding(bars.left, bars.top, bars.right, Math.max(bars.bottom, ime.bottom));
                return insets;
            });
        }

        LinearLayout header = column();
        header.setPadding(dp(20), dp(17), dp(20), dp(10));
        header.setBackgroundColor(Color.WHITE);
        TextView title = text("myliba", 24, GREEN, true);
        header.addView(title);
        TextView subtitle = text("PERSONAL LIBRARY", 9, MUTED, true);
        subtitle.setLetterSpacing(.12f);
        header.addView(subtitle);
        shell.addView(header);

        LinearLayout tabs = row();
        tabs.setPadding(dp(16), dp(8), dp(16), dp(8));
        tabs.setBackgroundColor(Color.WHITE);
        localTab = button("手机文件", () -> showLocal());
        serverTab = button("连接服务端", () -> showServer());
        tabs.addView(localTab, new LinearLayout.LayoutParams(0, dp(44), 1));
        tabs.addView(serverTab, new LinearLayout.LayoutParams(0, dp(44), 1));
        shell.addView(tabs);
        styleTab(localTab, true);
        styleTab(serverTab, false);

        page = column();
        shell.addView(page, new LinearLayout.LayoutParams(-1, 0, 1));
        showLocal();
    }

    private void showLocal() {
        showingServer = false;
        localTab.setEnabled(false);
        serverTab.setEnabled(true);
        styleTab(localTab, true);
        styleTab(serverTab, false);
        page.removeAllViews();
        ScrollView scroll = new ScrollView(this);
        page.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
        LinearLayout body = column();
        body.setPadding(dp(16), dp(18), dp(16), dp(30));
        scroll.addView(body);

        LinearLayout intro = column();
        intro.setPadding(dp(15), dp(15), dp(15), dp(15));
        intro.setBackground(rounded(Color.WHITE, dp(11), Color.rgb(231, 235, 231), dp(1)));
        intro.addView(text("个人资源空间", 10, GREEN, true));
        intro.addView(text("文件都在原来的位置", 20, INK, true), matchTop(5));
        intro.addView(text("索引、搜索与整理都在这台手机完成。", 12, MUTED, false), matchTop(5));
        body.addView(intro);

        addSectionHeading(body, "索引目录", "选择要纳入资源库的文件夹。", 20);
        Button add = primaryButton("＋ 选择目录", this::pickTree);
        body.addView(add, matchTop(12));
        rootList = column();
        body.addView(rootList, matchTop(12));
        scanStatus = text("", 13, MUTED, false);
        body.addView(scanStatus, matchTop(8));

        TextView heading = text("资源", 19, INK, true);
        body.addView(heading, matchTop(26));
        folderButton = button("全部资源", this::chooseFolder);
        body.addView(folderButton, matchTop(8));
        collectionButton = button("智能集合", this::chooseCollection);
        body.addView(collectionButton, matchTop(4));
        search = new EditText(this);
        search.setSingleLine(true);
        search.setTextSize(15);
        search.setHint("搜索文件名、路径或标签");
        search.setBackground(rounded(Color.WHITE, dp(9), Color.rgb(220, 227, 221), dp(1)));
        search.setPadding(dp(13), dp(9), dp(13), dp(9));
        body.addView(search, matchTop(8));
        search.setText(filters.query);
        LinearLayout controls = row();
        filterButton = button("筛选", this::chooseFilter);
        sortButton = button("排序", this::chooseSort);
        controls.addView(filterButton, new LinearLayout.LayoutParams(0, dp(48), 1));
        controls.addView(sortButton, new LinearLayout.LayoutParams(0, dp(48), 1));
        controls.addView(button("清除", this::clearFilters), new LinearLayout.LayoutParams(0, dp(48), 1));
        body.addView(controls, matchTop(6));
        resultStatus = text("", 13, MUTED, false);
        body.addView(resultStatus, matchTop(8));
        selectionBar = column();
        body.addView(selectionBar, matchTop(4));
        resultList = column();
        body.addView(resultList, matchTop(12));
        search.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            @Override public void onTextChanged(CharSequence s, int start, int before, int count) {
                filters.query = s.toString();
                filtersChanged();
            }
            @Override public void afterTextChanged(Editable s) {}
        });
        refreshRoots();
        refreshItems();
    }

    private void showServer() {
        searchGeneration++;
        showingServer = true;
        localTab.setEnabled(true);
        serverTab.setEnabled(false);
        styleTab(localTab, false);
        styleTab(serverTab, true);
        page.removeAllViews();
        remote.attach(page);
    }

    private void pickTree() {
        if (scanning) { toast("请等当前扫描结束"); return; }
        Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
        intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);
        try {
            startActivityForResult(intent, PICK_TREE);
        } catch (ActivityNotFoundException error) {
            toast("这台设备没有可用的目录选择器");
        }
    }

    @Override protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != PICK_TREE || resultCode != RESULT_OK || data == null || data.getData() == null) return;
        Uri uri = data.getData();
        try {
            getContentResolver().takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION);
            String name = DocumentsContract.getTreeDocumentId(uri);
            int slash = name.lastIndexOf(':');
            if (slash >= 0 && slash + 1 < name.length()) name = name.substring(slash + 1);
            if (name.isEmpty()) name = "所选目录";
            library.addRoot(uri, name);
            refreshRoots();
            scan(uri);
        } catch (SecurityException | IllegalArgumentException error) {
            toast("无法保存目录授权：" + error.getMessage());
        }
    }

    private void scan(Uri uri) {
        if (scanning) {
            toast("当前扫描结束后再试");
            return;
        }
        if (!hasGrant(uri)) {
            toast("目录授权已失效，请重新选择目录");
            return;
        }
        scanning = true;
        selectedUris.clear();
        searchGeneration++;
        scanStatus.setText("正在扫描…");
        refreshRoots();
        refreshItems();
        scanner.execute(() -> {
            try {
                int count = new TreeScanner(getContentResolver(), library).scan(uri);
                runOnUiThread(() -> {
                    scanning = false;
                    if (scanStatus != null) scanStatus.setText("已扫描 " + count + " 个文件");
                    if (!showingServer) { refreshRoots(); refreshItems(); }
                });
            } catch (Exception error) {
                runOnUiThread(() -> {
                    scanning = false;
                    if (scanStatus != null) scanStatus.setText("扫描失败，索引未修改：" + error.getMessage());
                    if (!showingServer) { refreshRoots(); refreshItems(); }
                });
            }
        });
    }

    private boolean hasGrant(Uri uri) {
        for (UriPermission grant : getContentResolver().getPersistedUriPermissions()) {
            if (grant.isReadPermission() && uri.equals(grant.getUri())) return true;
        }
        return false;
    }

    private void refreshRoots() {
        if (rootList == null || showingServer) return;
        rootList.removeAllViews();
        if (scanning) {
            rootList.addView(text("正在扫描目录…", 14, MUTED, false));
            return;
        }
        List<LocalLibrary.Root> roots = library.roots();
        if (roots.isEmpty()) {
            rootList.addView(text("还没有授权目录。可选择相册、下载或文档中的文件夹。", 14, MUTED, false));
            return;
        }
        for (LocalLibrary.Root root : roots) {
            LinearLayout line = row();
            line.setGravity(Gravity.CENTER_VERTICAL);
            line.setPadding(dp(9), dp(5), dp(5), dp(5));
            line.setBackground(rounded(Color.WHITE, dp(9), Color.rgb(231, 235, 231), dp(1)));
            TextView label = text(root.name + "  ·  " + root.count + " 个文件", 15, INK, true);
            label.setPadding(dp(4), 0, 0, 0);
            line.addView(label, new LinearLayout.LayoutParams(0, -2, 1));
            line.addView(button("扫描", () -> scan(Uri.parse(root.uri))));
            line.addView(button("移除", () -> removeRoot(root)));
            rootList.addView(line, matchTop(8));
        }
    }

    private void removeRoot(LocalLibrary.Root root) {
        if (scanning) { toast("请等当前扫描结束"); return; }
        new AlertDialog.Builder(this).setTitle("移除 " + root.name + "？")
                .setMessage("会删除此目录的本地索引和标签，原文件不受影响。")
                .setNegativeButton("取消", null)
                .setPositiveButton("移除", (dialog, which) -> {
                    library.removeRoot(root.uri);
                    try {
                        getContentResolver().releasePersistableUriPermission(Uri.parse(root.uri), Intent.FLAG_GRANT_READ_URI_PERMISSION);
                    } catch (SecurityException ignored) {}
                    refreshRoots();
                    selectedUris.clear();
                    if (root.uri.equals(filters.rootUri)) filters.rootUri = "";
                    refreshItems();
                }).show();
    }

    private void refreshItems() {
        if (resultList == null || search == null || showingServer) return;
        searchGeneration++;
        resultList.removeAllViews();
        selectionBar.removeAllViews();
        if (scanning) {
            resultStatus.setText("扫描中");
            resultList.addView(text("扫描完成后会更新结果", 14, MUTED, false));
            return;
        }
        updateControls();
        shownCount = 0;
        shownUris.clear();
        resultStatus.setText("正在检索…");
        loadMore();
    }

    private void loadMore() {
        if (resultList == null || showingServer || scanning) return;
        int generation = searchGeneration;
        int offset = shownCount;
        LocalFilters requested = filters.copy();
        if (offset > 0 && resultList.getChildCount() > 0) resultList.removeViewAt(resultList.getChildCount() - 1);
        scanner.execute(() -> {
            if (generation != searchGeneration) return;
            try {
                LocalLibrary.Page page = library.search(requested, 48, offset);
                runOnUiThread(() -> {
                    if (generation == searchGeneration && !showingServer && !scanning && offset == shownCount) showPage(page);
                });
            } catch (Exception error) {
                runOnUiThread(() -> {
                    if (generation == searchGeneration && !showingServer) resultStatus.setText("检索失败：" + error.getMessage());
                });
            }
        });
    }

    private void showPage(LocalLibrary.Page page) {
        if (shownCount == 0) resultList.removeAllViews();
        shownCount += page.items.size();
        resultStatus.setText("匹配 " + page.total + " 个文件 · 已显示 " + shownCount);
        if (page.items.isEmpty() && shownCount == 0) {
            selectionBar.removeAllViews();
            resultList.addView(text("没有匹配的文件", 14, MUTED, false));
            return;
        }
        for (LocalLibrary.Item item : page.items) {
            shownUris.add(item.uri);
            LinearLayout card = column();
            card.setPadding(dp(12), dp(10), dp(12), dp(10));
            card.setBackground(rounded(Color.WHITE, dp(10), Color.rgb(231, 235, 231), dp(1)));
            TextView name = text(item.name, 16, INK, true);
            name.setMaxLines(2);
            card.addView(name);
            TextView meta = text(item.path + (item.tags.isEmpty() ? "" : "\n" + item.tags), 12, MUTED, false);
            meta.setMaxLines(3);
            card.addView(meta);
            LinearLayout actions = row();
            actions.addView(button("打开", () -> open(item)));
            actions.addView(button("标签", () -> editTags(item)));
            actions.addView(button(selectedUris.contains(item.uri) ? "✓ 已选" : "选择", () -> {
                if (!selectedUris.add(item.uri)) selectedUris.remove(item.uri);
                refreshItems();
            }));
            card.addView(actions, matchTop(4));
            resultList.addView(card, matchTop(8));
        }
        if (shownCount < page.total) resultList.addView(button("加载更多", this::loadMore), matchTop(10));
        updateSelectionBar();
    }

    private void updateControls() {
        if (folderButton == null) return;
        String folderName = "全部资源";
        if ("unfiled".equals(filters.folderId)) folderName = "未归组";
        else if (!"all".equals(filters.folderId)) {
            for (LocalLibrary.Folder folder : library.folders()) if (folder.id.equals(filters.folderId)) folderName = folder.name;
        }
        folderButton.setText("文件夹：" + folderName + " ▾");
        String collectionName = "智能集合 ▾";
        if (activeCollectionId != null) {
            for (LocalLibrary.Collection collection : library.collections()) {
                if (collection.id.equals(activeCollectionId)) collectionName = "智能集合：" + collection.name +
                        (collection.filters.serialize().equals(filters.serialize()) ? "" : " · 已修改") + " ▾";
            }
        }
        collectionButton.setText(collectionName);
        int filterCount = filters.tags.size() + filters.attributes.size() +
                (filters.rootUri.isEmpty() ? 0 : 1) + (filters.unorganized ? 1 : 0);
        filterButton.setText(filterCount == 0 ? "筛选" : "筛选 · " + filterCount);
        String sortName;
        switch (filters.sort) {
            case "modified_asc": sortName = "最早"; break;
            case "name_asc": sortName = "名称 A–Z"; break;
            case "name_desc": sortName = "名称 Z–A"; break;
            case "size_desc": sortName = "大小"; break;
            default: sortName = "最近";
        }
        sortButton.setText(sortName + " ▾");
    }

    private void updateSelectionBar() {
        selectionBar.removeAllViews();
        LinearLayout choice = row();
        choice.addView(button("选本页", () -> {
            selectedUris.addAll(shownUris);
            refreshItems();
        }), new LinearLayout.LayoutParams(0, dp(44), 1));
        choice.addView(button("选全部匹配", () -> {
            int generation = searchGeneration;
            LocalFilters requested = filters.copy();
            resultStatus.setText("正在选择匹配文件…");
            scanner.execute(() -> {
                try {
                    List<String> matches = library.matchingUris(requested);
                    runOnUiThread(() -> {
                        if (generation != searchGeneration || showingServer) return;
                        selectedUris.addAll(matches);
                        refreshItems();
                    });
                } catch (Exception error) {
                    runOnUiThread(() -> {
                        if (generation == searchGeneration && !showingServer) resultStatus.setText("选择失败：" + error.getMessage());
                    });
                }
            });
        }), new LinearLayout.LayoutParams(0, dp(44), 1));
        if (!selectedUris.isEmpty()) choice.addView(button("取消选择", () -> {
            selectedUris.clear();
            refreshItems();
        }), new LinearLayout.LayoutParams(0, dp(44), 1));
        selectionBar.addView(choice);
        if (selectedUris.isEmpty()) return;
        TextView count = text("已选择 " + selectedUris.size() + " 个文件", 13, MUTED, false);
        selectionBar.addView(count);
        LinearLayout actions = row();
        actions.addView(button("批量加标签", this::batchTag), new LinearLayout.LayoutParams(0, dp(44), 1));
        actions.addView(button("移动到文件夹", this::batchMove), new LinearLayout.LayoutParams(0, dp(44), 1));
        selectionBar.addView(actions);
    }

    private void filtersChanged() {
        selectedUris.clear();
        refreshItems();
    }

    private void clearFilters() {
        String folderId = filters.folderId;
        filters = new LocalFilters();
        filters.folderId = folderId;
        search.setText("");
        filtersChanged();
    }

    private void chooseFolder() {
        List<LocalLibrary.Folder> folders = library.folders();
        String[] choices = new String[folders.size() + 3];
        choices[0] = "全部资源";
        choices[1] = "未归组";
        for (int i = 0; i < folders.size(); i++) choices[i + 2] = folders.get(i).name + " · " + folders.get(i).count;
        choices[choices.length - 1] = "管理文件夹…";
        new AlertDialog.Builder(this).setTitle("文件夹").setItems(choices, (dialog, which) -> {
            if (which == choices.length - 1) { manageFolders(); return; }
            filters = new LocalFilters();
            filters.folderId = which == 0 ? "all" : which == 1 ? "unfiled" : folders.get(which - 2).id;
            activeCollectionId = null;
            search.setText("");
            filtersChanged();
        }).setNegativeButton("关闭", null).show();
    }

    private void manageFolders() {
        List<LocalLibrary.Folder> folders = library.folders();
        String[] choices = new String[folders.size() + 1];
        choices[0] = "＋ 新建文件夹";
        for (int i = 0; i < folders.size(); i++) choices[i + 1] = folders.get(i).name;
        new AlertDialog.Builder(this).setTitle("管理文件夹").setItems(choices, (dialog, which) -> {
            if (which == 0) promptName("新建文件夹", "文件夹名称", "", value -> {
                String id = library.createFolder(value);
                filters.folderId = id;
                filtersChanged();
            });
            else {
                LocalLibrary.Folder folder = folders.get(which - 1);
                new AlertDialog.Builder(this).setTitle(folder.name).setItems(new String[]{"重命名", "删除文件夹"}, (d, action) -> {
                    if (action == 0) promptName("重命名文件夹", "文件夹名称", folder.name, value -> {
                        library.renameFolder(folder.id, value);
                        refreshItems();
                    });
                    else new AlertDialog.Builder(this).setTitle("删除 " + folder.name + "？")
                            .setMessage("其中的文件会移至未归组。原文件和标签不受影响。")
                            .setNegativeButton("取消", null)
                            .setPositiveButton("删除", (confirm, action2) -> {
                                library.removeFolder(folder.id);
                                if (folder.id.equals(filters.folderId)) filters.folderId = "unfiled";
                                filtersChanged();
                            }).show();
                }).show();
            }
        }).setNegativeButton("关闭", null).show();
    }

    private void chooseCollection() {
        List<LocalLibrary.Collection> collections = library.collections();
        int extras = activeCollectionId == null ? 1 : 4;
        String[] choices = new String[collections.size() + extras];
        for (int i = 0; i < collections.size(); i++) choices[i] = collections.get(i).name;
        int start = collections.size();
        choices[start] = "＋ 保存当前筛选";
        if (activeCollectionId != null) {
            choices[start + 1] = "更新当前集合";
            choices[start + 2] = "重命名当前集合";
            choices[start + 3] = "删除当前集合";
        }
        new AlertDialog.Builder(this).setTitle("智能集合").setItems(choices, (dialog, which) -> {
            if (which < collections.size()) {
                LocalLibrary.Collection selected = collections.get(which);
                filters = selected.filters.copy();
                selectedUris.clear();
                search.setText(filters.query);
                activeCollectionId = selected.id;
                refreshItems();
            } else if (which == start) {
                promptName("保存智能集合", "集合名称", "", value -> {
                    activeCollectionId = library.saveCollection(null, value, filters.copy());
                    refreshItems();
                });
            } else if (activeCollectionId != null) {
                LocalLibrary.Collection current = null;
                for (LocalLibrary.Collection item : collections) if (item.id.equals(activeCollectionId)) current = item;
                if (current == null) return;
                LocalLibrary.Collection target = current;
                if (which == start + 1) {
                    library.saveCollection(target.id, target.name, filters.copy());
                    toast("已更新智能集合");
                } else if (which == start + 2) {
                    promptName("重命名智能集合", "集合名称", target.name, value -> {
                        library.saveCollection(target.id, value, target.filters);
                        refreshItems();
                    });
                } else {
                    new AlertDialog.Builder(this).setTitle("删除 " + target.name + "？")
                            .setNegativeButton("取消", null)
                            .setPositiveButton("删除", (d, action) -> {
                                library.removeCollection(target.id);
                                activeCollectionId = null;
                                refreshItems();
                            }).show();
                }
            }
        }).setNegativeButton("关闭", null).show();
    }

    private interface NameAction { void apply(String value); }

    private void promptName(String title, String hint, String initial, NameAction action) {
        EditText input = new EditText(this);
        input.setSingleLine(true);
        input.setHint(hint);
        input.setText(initial);
        new AlertDialog.Builder(this).setTitle(title).setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("保存", (dialog, which) -> {
                    try { action.apply(input.getText().toString()); }
                    catch (IllegalArgumentException | SQLiteConstraintException error) { toast("名称无效或已存在"); }
                }).show();
    }

    private void chooseFilter() {
        String[] choices = {"手动标签", "文件类型", "文件大小", "文件格式", "来源目录", filters.unorganized ? "✓ 待整理（无标签）" : "待整理（无标签）"};
        new AlertDialog.Builder(this).setTitle("组合筛选").setItems(choices, (dialog, which) -> {
            if (which == 0) chooseTags();
            else if (which == 1) chooseAttributes("type:", "文件类型");
            else if (which == 2) chooseAttributes("size:", "文件大小");
            else if (which == 3) chooseAttributes("extension:", "文件格式");
            else if (which == 4) chooseRoot();
            else { filters.unorganized = !filters.unorganized; filtersChanged(); }
        }).setNegativeButton("关闭", null).show();
    }

    private void chooseTags() {
        String folderId = filters.folderId;
        scanner.execute(() -> {
            List<LocalLibrary.Attribute> catalog = library.tagCatalog(folderId);
            runOnUiThread(() -> {
                if (!showingServer && folderId.equals(filters.folderId)) showTagChoices(catalog);
            });
        });
    }

    private void showTagChoices(List<LocalLibrary.Attribute> catalog) {
        if (catalog.isEmpty()) { toast("当前文件夹还没有标签"); return; }
        String[] labels = new String[catalog.size()];
        boolean[] checked = new boolean[catalog.size()];
        for (int i = 0; i < catalog.size(); i++) {
            labels[i] = catalog.get(i).label + " · " + catalog.get(i).count;
            checked[i] = filters.tags.contains(catalog.get(i).key);
        }
        new AlertDialog.Builder(this).setTitle("手动标签（可多选）")
                .setMultiChoiceItems(labels, checked, (dialog, which, value) -> checked[which] = value)
                .setNegativeButton("取消", null)
                .setNeutralButton("清除", (dialog, which) -> { filters.tags.clear(); filtersChanged(); })
                .setPositiveButton("应用", (dialog, which) -> {
                    filters.tags.clear();
                    for (int i = 0; i < catalog.size(); i++) if (checked[i]) filters.tags.add(catalog.get(i).key);
                    filtersChanged();
                }).show();
    }

    private void chooseAttributes(String prefix, String title) {
        String folderId = filters.folderId;
        scanner.execute(() -> {
            List<LocalLibrary.Attribute> all = library.attributeCatalog(folderId);
            List<LocalLibrary.Attribute> catalog = new ArrayList<>();
            for (LocalLibrary.Attribute item : all) if (item.key.startsWith(prefix)) catalog.add(item);
            runOnUiThread(() -> {
                if (!showingServer && folderId.equals(filters.folderId)) showAttributeChoices(prefix, title, catalog);
            });
        });
    }

    private void showAttributeChoices(String prefix, String title, List<LocalLibrary.Attribute> catalog) {
        if (catalog.isEmpty()) { toast("当前文件夹没有可用的" + title); return; }
        String[] labels = new String[catalog.size()];
        boolean[] checked = new boolean[catalog.size()];
        for (int i = 0; i < catalog.size(); i++) {
            labels[i] = catalog.get(i).label + " · " + catalog.get(i).count;
            checked[i] = filters.attributes.contains(catalog.get(i).key);
        }
        new AlertDialog.Builder(this).setTitle(title + "（可多选）")
                .setMultiChoiceItems(labels, checked, (dialog, which, value) -> checked[which] = value)
                .setNegativeButton("取消", null)
                .setNeutralButton("清除", (dialog, which) -> {
                    filters.attributes.removeIf(key -> key.startsWith(prefix));
                    filtersChanged();
                })
                .setPositiveButton("应用", (dialog, which) -> {
                    filters.attributes.removeIf(key -> key.startsWith(prefix));
                    for (int i = 0; i < catalog.size(); i++) if (checked[i]) filters.attributes.add(catalog.get(i).key);
                    filtersChanged();
                }).show();
    }

    private void chooseRoot() {
        List<LocalLibrary.Root> roots = library.roots();
        String[] choices = new String[roots.size() + 1];
        choices[0] = "全部来源目录";
        for (int i = 0; i < roots.size(); i++) choices[i + 1] = roots.get(i).name + " · " + roots.get(i).count;
        new AlertDialog.Builder(this).setTitle("来源目录").setItems(choices, (dialog, which) -> {
            filters.rootUri = which == 0 ? "" : roots.get(which - 1).uri;
            filtersChanged();
        }).setNegativeButton("取消", null).show();
    }

    private void chooseSort() {
        String[] keys = {"modified_desc", "modified_asc", "name_asc", "name_desc", "size_desc"};
        String[] labels = {"最近修改", "最早修改", "名称 A–Z", "名称 Z–A", "文件大小"};
        int selected = 0;
        for (int i = 0; i < keys.length; i++) if (keys[i].equals(filters.sort)) selected = i;
        new AlertDialog.Builder(this).setTitle("排序").setSingleChoiceItems(labels, selected, (dialog, which) -> {
            filters.sort = keys[which];
            filtersChanged();
            dialog.dismiss();
        }).setNegativeButton("取消", null).show();
    }

    private void batchTag() {
        if (selectedUris.isEmpty()) return;
        new AlertDialog.Builder(this).setTitle("批量标签").setItems(new String[]{"添加标签", "移除标签"}, (dialog, which) -> {
            if (which == 0) promptName("给 " + selectedUris.size() + " 个文件添加标签", "标签名称", "", value -> {
                library.addTagToItems(new ArrayList<>(selectedUris), value);
                selectedUris.clear();
                refreshItems();
            });
            else {
                List<LocalLibrary.Attribute> tags = library.tagCatalog(filters.folderId);
                if (tags.isEmpty()) { toast("没有可移除的标签"); return; }
                String[] choices = new String[tags.size()];
                for (int i = 0; i < tags.size(); i++) choices[i] = tags.get(i).label;
                new AlertDialog.Builder(this).setTitle("从所选文件移除标签").setItems(choices, (d, choice) -> {
                    library.removeTagFromItems(new ArrayList<>(selectedUris), tags.get(choice).key);
                    selectedUris.clear();
                    refreshItems();
                }).setNegativeButton("取消", null).show();
            }
        }).setNegativeButton("取消", null).show();
    }

    private void batchMove() {
        if (selectedUris.isEmpty()) return;
        List<LocalLibrary.Folder> folders = library.folders();
        String[] choices = new String[folders.size() + 2];
        choices[0] = "未归组";
        for (int i = 0; i < folders.size(); i++) choices[i + 1] = folders.get(i).name;
        choices[choices.length - 1] = "＋ 新建文件夹并移动";
        new AlertDialog.Builder(this).setTitle("移动 " + selectedUris.size() + " 个文件到")
                .setItems(choices, (dialog, which) -> {
                    if (which == choices.length - 1) {
                        promptName("新建文件夹", "文件夹名称", "", value -> moveSelected(library.createFolder(value)));
                    } else moveSelected(which == 0 ? "unfiled" : folders.get(which - 1).id);
                }).setNegativeButton("取消", null).show();
    }

    private void moveSelected(String folderId) {
        library.moveItems(new ArrayList<>(selectedUris), folderId);
        selectedUris.clear();
        refreshItems();
    }

    private void open(LocalLibrary.Item item) {
        Intent intent = new Intent(this, PreviewActivity.class);
        intent.putExtra("uri", item.uri);
        intent.putExtra("name", item.name);
        intent.putExtra("mime", item.mime);
        try {
            startActivity(intent);
        } catch (SecurityException error) {
            toast("目录授权已失效，请重新选择目录");
        }
    }

    private void editTags(LocalLibrary.Item item) {
        if (scanning) { toast("请等当前扫描结束"); return; }
        List<String> tags = library.tags(item.uri);
        String[] choices = new String[tags.size() + 1];
        choices[0] = "＋ 添加标签";
        for (int i = 0; i < tags.size(); i++) choices[i + 1] = "移除 · " + tags.get(i);
        new AlertDialog.Builder(this).setTitle(item.name).setItems(choices, (dialog, which) -> {
            if (which == 0) addTag(item);
            else {
                library.removeTag(item.uri, tags.get(which - 1));
                selectedUris.clear();
                refreshItems();
            }
        }).setNegativeButton("关闭", null).show();
    }

    private void addTag(LocalLibrary.Item item) {
        EditText input = new EditText(this);
        input.setSingleLine(true);
        input.setHint("例如：旅行");
        new AlertDialog.Builder(this).setTitle("添加标签").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("添加", (dialog, which) -> {
                    try {
                        library.addTag(item.uri, input.getText().toString());
                        selectedUris.clear();
                        refreshItems();
                    } catch (IllegalArgumentException error) { toast(error.getMessage()); }
                }).show();
    }

    @Override public void onBackPressed() {
        if (showingServer && remote.canGoBack()) remote.goBack();
        else if (showingServer) showLocal();
        else super.onBackPressed();
    }

    @Override protected void onDestroy() {
        searchGeneration++;
        remote.destroy();
        scanner.execute(library::close);
        scanner.shutdown();
        super.onDestroy();
    }

    void toast(String message) { Toast.makeText(this, message, Toast.LENGTH_LONG).show(); }
    int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }
    LinearLayout column() { LinearLayout view = new LinearLayout(this); view.setOrientation(LinearLayout.VERTICAL); return view; }
    LinearLayout row() { LinearLayout view = new LinearLayout(this); view.setOrientation(LinearLayout.HORIZONTAL); return view; }
    LinearLayout.LayoutParams matchTop(int top) { LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2); params.topMargin = dp(top); return params; }
    TextView text(String value, int size, int color, boolean bold) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(size);
        view.setTextColor(color);
        if (bold) view.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        return view;
    }
    Button button(String value, Runnable action) {
        Button view = new Button(this);
        view.setText(value);
        view.setTextColor(GREEN);
        view.setAllCaps(false);
        view.setTextSize(12);
        view.setMinHeight(dp(42));
        view.setPadding(dp(10), 0, dp(10), 0);
        view.setBackground(rounded(GREEN_SOFT, dp(8), 0, 0));
        view.setElevation(0);
        view.setOnClickListener(v -> action.run());
        return view;
    }

    Button primaryButton(String value, Runnable action) {
        Button view = button(value, action);
        view.setTextColor(Color.WHITE);
        view.setBackground(rounded(GREEN, dp(8), 0, 0));
        return view;
    }

    private void styleTab(Button tab, boolean active) {
        tab.setBackground(rounded(active ? GREEN_SOFT : Color.WHITE, dp(9), active ? 0 : Color.rgb(231, 235, 231), active ? 0 : dp(1)));
        tab.setTextColor(active ? GREEN : MUTED);
        tab.setTypeface(Typeface.DEFAULT, active ? Typeface.BOLD : Typeface.NORMAL);
        tab.setElevation(0);
    }

    private void addSectionHeading(LinearLayout parent, String title, String subtitle, int top) {
        LinearLayout section = column();
        section.addView(text(title, 16, INK, true));
        section.addView(text(subtitle, 11, MUTED, false), matchTop(3));
        parent.addView(section, matchTop(top));
    }

    private GradientDrawable rounded(int fill, int radius, int stroke, int strokeWidth) {
        GradientDrawable drawable = new GradientDrawable();
        drawable.setColor(fill);
        drawable.setCornerRadius(radius);
        if (strokeWidth > 0) drawable.setStroke(strokeWidth, stroke);
        return drawable;
    }
}
