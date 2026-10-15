package com.myliba.android;

import android.app.AlertDialog;
import android.app.DownloadManager;
import android.content.Context;
import android.content.Intent;
import android.net.Uri;
import android.os.Environment;
import android.webkit.CookieManager;
import android.webkit.DownloadListener;
import android.webkit.URLUtil;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.net.URI;
import java.net.URISyntaxException;

final class RemotePanel {
    private final MainActivity activity;
    private final LinearLayout container;
    private String serverUrl;
    private WebView webView;
    private TextView status;

    RemotePanel(MainActivity activity) {
        this.activity = activity;
        this.container = activity.column();
        this.serverUrl = activity.getPreferences(Context.MODE_PRIVATE).getString("server_url", "");
    }

    void attach(LinearLayout page) {
        if (container.getParent() instanceof LinearLayout) ((LinearLayout) container.getParent()).removeView(container);
        page.addView(container, new LinearLayout.LayoutParams(-1, -1));
        render();
    }

    private void render() {
        container.removeAllViews();
        LinearLayout toolbar = activity.row();
        toolbar.setPadding(activity.dp(12), activity.dp(8), activity.dp(12), activity.dp(8));
        if (serverUrl.isEmpty()) {
            LinearLayout setup = activity.column();
            setup.setPadding(activity.dp(20), activity.dp(28), activity.dp(20), activity.dp(20));
            setup.addView(activity.text("连接 Myliba 服务端", 22, 0xff1d2731, true));
            setup.addView(activity.text("输入自建 Hub 的 HTTPS 地址。登录与远程资源操作沿用现有网页版。", 14, 0xff5e6b77, false), activity.matchTop(8));
            EditText address = new EditText(activity);
            address.setSingleLine(true);
            address.setHint("https://library.example.com");
            address.setInputType(android.text.InputType.TYPE_CLASS_TEXT | android.text.InputType.TYPE_TEXT_VARIATION_URI);
            setup.addView(address, activity.matchTop(20));
            setup.addView(activity.button("连接", () -> connect(address.getText().toString())), activity.matchTop(8));
            container.addView(setup);
            return;
        }

        status = activity.text(serverUrl, 12, 0xff5e6b77, false);
        status.setSingleLine(true);
        status.setEllipsize(android.text.TextUtils.TruncateAt.MIDDLE);
        toolbar.addView(status, new LinearLayout.LayoutParams(0, -2, 1));
        toolbar.addView(activity.button("浏览器", this::openBrowser));
        toolbar.addView(activity.button("修改", this::editServer));
        container.addView(toolbar);

        if (webView == null) createWebView();
        if (webView.getParent() instanceof LinearLayout) ((LinearLayout) webView.getParent()).removeView(webView);
        container.addView(webView, new LinearLayout.LayoutParams(-1, 0, 1));
    }

    private void connect(String input) {
        try {
            URI parsed = new URI(input.trim());
            if (!"https".equalsIgnoreCase(parsed.getScheme()) || parsed.getHost() == null ||
                    parsed.getRawUserInfo() != null || parsed.getRawQuery() != null || parsed.getRawFragment() != null ||
                    (parsed.getRawPath() != null && !parsed.getRawPath().isEmpty() && !"/".equals(parsed.getRawPath()))) {
                throw new URISyntaxException(input, "请输入 HTTPS 站点根地址");
            }
            String host = parsed.getHost().toLowerCase();
            if (host.contains(":")) host = "[" + host + "]";
            int port = parsed.getPort();
            serverUrl = "https://" + host + (port > 0 && port != 443 ? ":" + port : "");
            activity.getPreferences(Context.MODE_PRIVATE).edit().putString("server_url", serverUrl).apply();
            if (webView != null) {
                if (webView.getParent() instanceof LinearLayout) ((LinearLayout) webView.getParent()).removeView(webView);
                webView.destroy();
                webView = null;
            }
            render();
        } catch (URISyntaxException error) {
            activity.toast("请输入有效的 HTTPS 站点根地址");
        }
    }

    private void editServer() {
        EditText input = new EditText(activity);
        input.setSingleLine(true);
        input.setText(serverUrl);
        new AlertDialog.Builder(activity).setTitle("服务端地址").setView(input)
                .setNegativeButton("取消", null)
                .setPositiveButton("连接", (dialog, which) -> connect(input.getText().toString()))
                .show();
    }

    private void createWebView() {
        webView = new WebView(activity);
        webView.getSettings().setJavaScriptEnabled(true);
        webView.getSettings().setDomStorageEnabled(true);
        webView.getSettings().setAllowFileAccess(false);
        webView.getSettings().setAllowContentAccess(false);
        webView.getSettings().setMixedContentMode(android.webkit.WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        CookieManager.getInstance().setAcceptCookie(true);
        webView.setWebViewClient(new WebViewClient() {
            @Override public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
                if (!request.isForMainFrame()) return false;
                Uri url = request.getUrl();
                if (sameOrigin(url)) return false;
                if ("https".equalsIgnoreCase(url.getScheme())) openExternal(url);
                return true;
            }
        });
        webView.setWebChromeClient(new WebChromeClient() {
            @Override public void onProgressChanged(WebView view, int progress) {
                if (status != null) status.setText(progress < 100 ? "正在连接 · " + progress + "%" : serverUrl);
            }
        });
        webView.setDownloadListener(downloadListener());
        webView.loadUrl(serverUrl);
    }

    private boolean sameOrigin(Uri uri) {
        Uri base = Uri.parse(serverUrl);
        return "https".equalsIgnoreCase(uri.getScheme()) &&
                base.getHost() != null && base.getHost().equalsIgnoreCase(uri.getHost()) &&
                base.getPort() == uri.getPort();
    }

    private DownloadListener downloadListener() {
        return (url, userAgent, contentDisposition, mimeType, contentLength) -> {
            Uri uri = Uri.parse(url);
            if (!sameOrigin(uri)) {
                activity.toast("此下载链接不属于当前服务端");
                return;
            }
            try {
                DownloadManager.Request request = new DownloadManager.Request(uri);
                String cookie = CookieManager.getInstance().getCookie(url);
                if (cookie != null) request.addRequestHeader("Cookie", cookie);
                if (userAgent != null) request.addRequestHeader("User-Agent", userAgent);
                String filename = URLUtil.guessFileName(url, contentDisposition, mimeType);
                filename = filename.replace('/', '_').replace('\\', '_');
                request.setTitle(filename);
                request.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
                request.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, filename);
                DownloadManager manager = (DownloadManager) activity.getSystemService(Context.DOWNLOAD_SERVICE);
                manager.enqueue(request);
                activity.toast("已加入下载任务");
            } catch (RuntimeException error) {
                activity.toast("无法下载：" + error.getMessage());
            }
        };
    }

    private void openBrowser() { openExternal(Uri.parse(serverUrl)); }

    private void openExternal(Uri uri) {
        try { activity.startActivity(new Intent(Intent.ACTION_VIEW, uri)); }
        catch (RuntimeException error) { activity.toast("无法打开浏览器"); }
    }

    boolean canGoBack() { return webView != null && webView.canGoBack(); }
    void goBack() { if (webView != null) webView.goBack(); }
    void destroy() {
        if (webView != null) {
            if (webView.getParent() instanceof LinearLayout) ((LinearLayout) webView.getParent()).removeView(webView);
            webView.destroy();
            webView = null;
        }
    }
}
