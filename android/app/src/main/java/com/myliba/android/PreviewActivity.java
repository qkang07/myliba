package com.myliba.android;

import android.app.Activity;
import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Color;
import android.graphics.pdf.PdfRenderer;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.ParcelFileDescriptor;
import android.view.Gravity;
import android.view.WindowInsets;
import android.widget.Button;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.MediaController;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.VideoView;
import android.view.View;
import java.util.ArrayList;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class PreviewActivity extends Activity {
    private final ExecutorService worker = Executors.newSingleThreadExecutor();
    private Uri fileUri;
    private String mime;
    private LinearLayout content;
    private PdfRenderer pdf;
    private ParcelFileDescriptor pdfFile;
    private VideoView video;
    private Bitmap currentBitmap;
    private int pdfPage;
    private boolean closing;
    private MobileUi ui;
    private boolean remote;
    private File remoteCache;
    private LinearLayout toolbar, browserNav;
    private ArrayList<String> browseIds, browseNames, browseMimes;
    private int browseIndex;
    private boolean imageControlsVisible = true;

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        ui = new MobileUi(this);
        String value = getIntent().getStringExtra("uri");
        if (value == null) { finish(); return; }
        fileUri = Uri.parse(value);
        remote = getIntent().hasExtra("server");
        mime = getIntent().getStringExtra("mime");
        if (mime == null) mime = "application/octet-stream";
        String name = getIntent().getStringExtra("name");
        if (name == null) name = "文件预览";
        browseIds=getIntent().getStringArrayListExtra("browse_ids");browseNames=getIntent().getStringArrayListExtra("browse_names");browseMimes=getIntent().getStringArrayListExtra("browse_mimes");browseIndex=getIntent().getIntExtra("browse_index",-1);
        boolean media=mime.startsWith("image/")||mime.startsWith("video/");

        LinearLayout layout = new LinearLayout(this);
        layout.setOrientation(LinearLayout.VERTICAL);
        layout.setBackgroundColor(media?Color.BLACK:MobileUi.BG);
        getWindow().setStatusBarColor(media?Color.BLACK:MobileUi.BG);
        getWindow().setNavigationBarColor(media?Color.BLACK:MobileUi.PAPER);
        getWindow().getDecorView().setSystemUiVisibility(media?View.SYSTEM_UI_FLAG_FULLSCREEN|View.SYSTEM_UI_FLAG_HIDE_NAVIGATION|View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY:View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR|View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        setContentView(layout);
        if (Build.VERSION.SDK_INT >= 35 && !media) {
            getWindow().setDecorFitsSystemWindows(false);
            layout.setOnApplyWindowInsetsListener((view, insets) -> {
                android.graphics.Insets bars = insets.getInsets(WindowInsets.Type.systemBars());
                layout.setPadding(bars.left, bars.top, bars.right, bars.bottom);
                return insets;
            });
        }
        toolbar = new LinearLayout(this);
        toolbar.setGravity(Gravity.CENTER_VERTICAL);
        toolbar.setPadding(dp(8), dp(10), dp(8), dp(10));
        toolbar.setBackgroundColor(media?0xff171b19:Color.WHITE);
        ImageView back=ui.iconButton("back", "关闭预览", this::finish);
        if(media)back.setImageDrawable(new MobileUi.Icon("back",Color.WHITE));
        toolbar.addView(back);
        TextView title = ui.text(name, 16, media?Color.WHITE:MobileUi.INK, true);
        title.setPadding(dp(8), 0, dp(8), 0);
        title.setSingleLine(true);
        title.setEllipsize(android.text.TextUtils.TruncateAt.MIDDLE);
        toolbar.addView(title, new LinearLayout.LayoutParams(0, -2, 1));
        if (!remote){ImageView external=ui.iconButton("link", "用其他应用打开", this::openExternal);if(media)external.setImageDrawable(new MobileUi.Icon("link",Color.WHITE));toolbar.addView(external);}
        layout.addView(toolbar);
        content = new LinearLayout(this);
        content.setOrientation(LinearLayout.VERTICAL);
        layout.addView(content, new LinearLayout.LayoutParams(-1, 0, 1));
        if(media && browseIds!=null && browseNames!=null && browseMimes!=null && browseIds.size()==browseNames.size() && browseIds.size()==browseMimes.size() && browseIndex>=0 && browseIndex<browseIds.size()){
            browserNav=new LinearLayout(this);browserNav.setGravity(Gravity.CENTER_VERTICAL);browserNav.setPadding(dp(12),dp(8),dp(12),dp(8));browserNav.setBackgroundColor(0xff171b19);
            TextView previous=button("上一项",()->navigateMedia(-1));previous.setEnabled(browseIndex>0);browserNav.addView(previous);
            TextView position=ui.text((browseIndex+1)+" / "+browseIds.size(),13,Color.WHITE,false);position.setGravity(Gravity.CENTER);browserNav.addView(position,new LinearLayout.LayoutParams(0,-2,1));
            TextView next=button("下一项",()->navigateMedia(1));next.setEnabled(browseIndex+1<browseIds.size());browserNav.addView(next);layout.addView(browserNav);
        }
        showMessage("正在打开…");

        if (remote) loadRemote();
        else openPreview();
    }

    private void openPreview() {
        if (mime.startsWith("image/")) loadImage();
        else if (mime.equals("application/pdf")) loadPdf();
        else if (mime.startsWith("text/") || mime.equals("application/json") || mime.endsWith("+xml")) loadText();
        else if (mime.startsWith("video/")) showVideo();
        else showMessage(remote ? "此格式没有内置预览，请返回详情下载原文件。" : "此格式没有内置预览，可用其他应用打开。");
    }

    private void loadRemote() {
        RemoteClient client = new RemoteClient(getIntent().getStringExtra("server"), getSharedPreferences("MainActivity", MODE_PRIVATE));
        if (mime.startsWith("video/") || mime.startsWith("audio/")) {
            showVideo();
            return;
        }
        if (!(mime.startsWith("image/") || mime.equals("application/pdf") || mime.startsWith("text/") || mime.equals("application/json") || mime.endsWith("+xml"))) {
            showMessage("此格式没有内置预览，请返回详情下载原文件。");
            return;
        }
        showMessage("正在读取原文件…");
        worker.execute(() -> {
            try {
                byte[] data = client.bytes("GET", "/api/resources/" + RemoteClient.enc(getIntent().getStringExtra("resource")) + "/file", null, 24 * 1024 * 1024);
                remoteCache = File.createTempFile("preview-", ".bin", getCacheDir());
                try (FileOutputStream out = new FileOutputStream(remoteCache)) { out.write(data); }
                runOnUiThread(() -> { if (!closing) { fileUri = Uri.fromFile(remoteCache); openPreview(); } });
            } catch (Exception error) { fail(error); }
        });
    }

    private void loadImage() {
        worker.execute(() -> {
            try {
                BitmapFactory.Options bounds = new BitmapFactory.Options();
                bounds.inJustDecodeBounds = true;
                try (InputStream stream = getContentResolver().openInputStream(fileUri)) {
                    if (stream == null) throw new IOException("无法读取图片");
                    BitmapFactory.decodeStream(stream, null, bounds);
                }
                BitmapFactory.Options options = new BitmapFactory.Options();
                options.inPreferredConfig = Bitmap.Config.ARGB_8888;
                options.inSampleSize = 1;
                while (bounds.outWidth / options.inSampleSize > 2048 || bounds.outHeight / options.inSampleSize > 2048) {
                    options.inSampleSize *= 2;
                }
                Bitmap bitmap;
                try (InputStream stream = getContentResolver().openInputStream(fileUri)) {
                    if (stream == null) throw new IOException("无法读取图片");
                    bitmap = BitmapFactory.decodeStream(stream, null, options);
                }
                if (bitmap == null) throw new IOException("图片格式不受支持");
                runOnUiThread(() -> {
                    if (closing) { bitmap.recycle(); return; }
                    clearContent();
                    ImageView image = new ZoomImageView(this);
                    image.setOnClickListener(v->toggleImageControls());
                    image.setImageBitmap(bitmap);
                    currentBitmap = bitmap;
                    content.addView(image, new LinearLayout.LayoutParams(-1, -1));
                });
            } catch (Exception error) { fail(error); }
        });
    }

    private void loadText() {
        worker.execute(() -> {
            try (InputStream stream = getContentResolver().openInputStream(fileUri)) {
                if (stream == null) throw new IOException("无法读取文本");
                ByteArrayOutputStream bytes = new ByteArrayOutputStream();
                byte[] buffer = new byte[8192];
                int count;
                while (bytes.size() < 1024 * 1024 && (count = stream.read(buffer, 0, Math.min(buffer.length, 1024 * 1024 - bytes.size()))) != -1) {
                    bytes.write(buffer, 0, count);
                }
                String text = new String(bytes.toByteArray(), StandardCharsets.UTF_8);
                if (bytes.size() == 1024 * 1024) text += "\n\n仅显示前 1 MiB";
                String preview = text;
                runOnUiThread(() -> {
                    if (closing) return;
                    clearContent();
                    ScrollView scroll = new ScrollView(this);
                    TextView view = new TextView(this);
                    view.setText(preview);
                    view.setTextSize(15);
                    view.setTextColor(Color.rgb(29, 39, 49));
                    view.setPadding(dp(16), dp(16), dp(16), dp(24));
                    scroll.addView(view);
                    content.addView(scroll, new LinearLayout.LayoutParams(-1, -1));
                });
            } catch (Exception error) { fail(error); }
        });
    }

    private void loadPdf() {
        worker.execute(() -> {
            try {
                pdfFile = getContentResolver().openFileDescriptor(fileUri, "r");
                if (pdfFile == null) throw new IOException("无法读取 PDF");
                pdf = new PdfRenderer(pdfFile);
                if (pdf.getPageCount() == 0) throw new IOException("PDF 没有页面");
                runOnUiThread(() -> { if (!closing) renderPdfPage(0); });
            } catch (Exception error) { fail(error); }
        });
    }

    private void renderPdfPage(int index) {
        if (pdf == null || index < 0 || index >= pdf.getPageCount()) return;
        pdfPage = index;
        showMessage("正在渲染第 " + (index + 1) + " 页…");
        worker.execute(() -> {
            try (PdfRenderer.Page page = pdf.openPage(index)) {
                float scale = Math.min(1f, Math.min(1600f / page.getWidth(), 2048f / page.getHeight()));
                int width = Math.max(1, Math.round(page.getWidth() * scale));
                int height = Math.max(1, Math.round(page.getHeight() * scale));
                Bitmap bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888);
                bitmap.eraseColor(Color.WHITE);
                page.render(bitmap, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY);
                runOnUiThread(() -> {
                    if (closing || index != pdfPage) { bitmap.recycle(); return; }
                    clearContent();
                    LinearLayout pager = new LinearLayout(this);
                    pager.setGravity(Gravity.CENTER);
                    Button previous = button("上一页", () -> renderPdfPage(index - 1));
                    previous.setEnabled(index > 0);
                    pager.addView(previous);
                    TextView count = new TextView(this);
                    count.setText((index + 1) + " / " + pdf.getPageCount());
                    pager.addView(count);
                    Button next = button("下一页", () -> renderPdfPage(index + 1));
                    next.setEnabled(index + 1 < pdf.getPageCount());
                    pager.addView(next);
                    content.addView(pager);
                    ScrollView scroll = new ScrollView(this);
                    ImageView image = new ImageView(this);
                    image.setImageBitmap(bitmap);
                    currentBitmap = bitmap;
                    image.setAdjustViewBounds(true);
                    scroll.addView(image);
                    content.addView(scroll, new LinearLayout.LayoutParams(-1, 0, 1));
                });
            } catch (Exception error) { fail(error); }
        });
    }

    private void showVideo() {
        clearContent();
        video = new VideoView(this);
        MediaController controls = new MediaController(this);
        controls.setAnchorView(video);
        video.setMediaController(controls);
        if (remote) {
            RemoteClient client = new RemoteClient(getIntent().getStringExtra("server"), getSharedPreferences("MainActivity", MODE_PRIVATE));
            java.util.Map<String, String> headers = new java.util.HashMap<>();
            headers.put("Cookie", client.cookie());
            video.setVideoURI(fileUri, headers);
        } else video.setVideoURI(fileUri);
        content.addView(video, new LinearLayout.LayoutParams(-1, -1));
        video.setOnErrorListener((player, what, extra) -> { showMessage("视频无法播放，可用其他应用打开。"); return true; });
        video.start();
    }

    private void openExternal() {
        Intent intent = new Intent(Intent.ACTION_VIEW);
        intent.setDataAndType(fileUri, mime);
        intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
        try { startActivity(Intent.createChooser(intent, "打开文件")); }
        catch (RuntimeException error) { showMessage("没有可打开此文件的应用，或目录授权已失效。"); }
    }
    private void toggleImageControls(){if(!mime.startsWith("image/"))return;imageControlsVisible=!imageControlsVisible;toolbar.setVisibility(imageControlsVisible?View.VISIBLE:View.GONE);if(browserNav!=null)browserNav.setVisibility(imageControlsVisible?View.VISIBLE:View.GONE);}
    private void navigateMedia(int step){int target=browseIndex+step;if(browseIds==null||target<0||target>=browseIds.size())return;Intent next=new Intent(this,PreviewActivity.class);String id=browseIds.get(target);next.putExtra("uri",remote?getIntent().getStringExtra("server")+"/api/resources/"+RemoteClient.enc(id)+"/file":id);next.putExtra("name",browseNames.get(target));next.putExtra("mime",browseMimes.get(target));if(remote)next.putExtra("server",getIntent().getStringExtra("server")).putExtra("resource",id);next.putStringArrayListExtra("browse_ids",browseIds);next.putStringArrayListExtra("browse_names",browseNames);next.putStringArrayListExtra("browse_mimes",browseMimes);next.putExtra("browse_index",target);startActivity(next);finish();}

    private void fail(Exception error) { runOnUiThread(() -> { if (!closing) showMessage("预览失败：" + error.getMessage()); }); }

    private void showMessage(String message) {
        clearContent();
        TextView view = new TextView(this);
        view.setText(message);
        view.setTextSize(15);
        view.setPadding(dp(20), dp(24), dp(20), dp(20));
        content.addView(view);
    }

    private void clearContent() {
        content.removeAllViews();
        if (currentBitmap != null) {
            currentBitmap.recycle();
            currentBitmap = null;
        }
    }

    private Button button(String title, Runnable action) {
        Button button = new Button(this);
        button.setText(title);
        button.setAllCaps(false);
        button.setTextColor(MobileUi.GREEN);
        button.setTextSize(12);
        button.setBackground(ui.shape(MobileUi.SOFT, 12, 0));
        button.setPadding(dp(16), dp(8), dp(16), dp(8));
        button.setOnClickListener(view -> action.run());
        return button;
    }

    private int dp(int value) { return Math.round(value * getResources().getDisplayMetrics().density); }

    @Override protected void onDestroy() {
        closing = true;
        if (video != null) video.stopPlayback();
        clearContent();
        worker.execute(() -> {
            if (pdf != null) pdf.close();
            if (pdfFile != null) try { pdfFile.close(); } catch (IOException ignored) {}
            if (remoteCache != null) remoteCache.delete();
        });
        worker.shutdown();
        super.onDestroy();
    }
}
