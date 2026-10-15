package com.myliba.android;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.content.UriPermission;
import android.graphics.Color;
import android.graphics.Typeface;
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
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private static final int PICK_TREE = 1;
    private static final int INK = Color.rgb(29, 39, 49);
    private static final int MUTED = Color.rgb(94, 107, 119);
    private static final int BLUE = Color.rgb(38, 89, 173);

    private final ExecutorService scanner = Executors.newSingleThreadExecutor();
    private LocalLibrary library;
    private RemotePanel remote;
    private LinearLayout page;
    private LinearLayout rootList;
    private LinearLayout resultList;
    private EditText search;
    private TextView scanStatus;
    private Button localTab;
    private Button serverTab;
    private boolean showingServer;
    private boolean scanning;

    @Override public void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().setStatusBarColor(Color.rgb(245, 248, 252));
        getWindow().setNavigationBarColor(Color.rgb(245, 248, 252));
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR | View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        library = new LocalLibrary(this);
        remote = new RemotePanel(this);

        LinearLayout shell = new LinearLayout(this);
        shell.setOrientation(LinearLayout.VERTICAL);
        shell.setBackgroundColor(Color.rgb(250, 251, 253));
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
        header.setPadding(dp(20), dp(20), dp(20), dp(12));
        header.setBackgroundColor(Color.WHITE);
        TextView title = text("Myliba", 26, INK, true);
        header.addView(title);
        TextView subtitle = text("你的本地与远程资源库", 13, MUTED, false);
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

        page = column();
        shell.addView(page, new LinearLayout.LayoutParams(-1, 0, 1));
        showLocal();
    }

    private void showLocal() {
        showingServer = false;
        localTab.setEnabled(false);
        serverTab.setEnabled(true);
        page.removeAllViews();
        ScrollView scroll = new ScrollView(this);
        page.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
        LinearLayout body = column();
        body.setPadding(dp(18), dp(20), dp(18), dp(32));
        scroll.addView(body);

        body.addView(text("本地目录", 21, INK, true));
        body.addView(text("授权一个或多个目录，原文件留在手机原位。", 13, MUTED, false));
        Button add = button("＋ 选择目录", this::pickTree);
        body.addView(add, matchTop(12));
        rootList = column();
        body.addView(rootList, matchTop(12));
        scanStatus = text("", 13, MUTED, false);
        body.addView(scanStatus, matchTop(8));

        TextView heading = text("资源", 21, INK, true);
        body.addView(heading, matchTop(28));
        search = new EditText(this);
        search.setSingleLine(true);
        search.setTextSize(15);
        search.setHint("搜索文件名、路径或标签");
        search.setPadding(dp(12), dp(8), dp(12), dp(8));
        body.addView(search, matchTop(8));
        resultList = column();
        body.addView(resultList, matchTop(12));
        search.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            @Override public void onTextChanged(CharSequence s, int start, int before, int count) { refreshItems(); }
            @Override public void afterTextChanged(Editable s) {}
        });
        refreshRoots();
        refreshItems();
    }

    private void showServer() {
        showingServer = true;
        localTab.setEnabled(true);
        serverTab.setEnabled(false);
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
        scanStatus.setText("正在扫描…");
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
            TextView label = text(root.name + "  ·  " + root.count + " 个文件", 15, INK, true);
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
                    refreshItems();
                }).show();
    }

    private void refreshItems() {
        if (resultList == null || search == null || showingServer) return;
        resultList.removeAllViews();
        if (scanning) {
            resultList.addView(text("扫描完成后会更新结果", 14, MUTED, false));
            return;
        }
        List<LocalLibrary.Item> items = library.search(search.getText().toString());
        if (items.isEmpty()) {
            resultList.addView(text("没有匹配的文件", 14, MUTED, false));
            return;
        }
        for (LocalLibrary.Item item : items) {
            LinearLayout card = column();
            card.setPadding(dp(12), dp(10), dp(12), dp(10));
            card.setBackgroundColor(Color.WHITE);
            TextView name = text(item.name, 16, INK, true);
            name.setMaxLines(2);
            card.addView(name);
            TextView meta = text(item.path + (item.tags.isEmpty() ? "" : "\n" + item.tags), 12, MUTED, false);
            meta.setMaxLines(3);
            card.addView(meta);
            LinearLayout actions = row();
            actions.addView(button("打开", () -> open(item)));
            actions.addView(button("标签", () -> editTags(item)));
            card.addView(actions, matchTop(4));
            resultList.addView(card, matchTop(8));
        }
        if (items.size() == 200) resultList.addView(text("仅显示前 200 项，可输入关键词缩小范围。", 12, MUTED, false));
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
        view.setTextColor(BLUE);
        view.setAllCaps(false);
        view.setOnClickListener(v -> action.run());
        return view;
    }
}
