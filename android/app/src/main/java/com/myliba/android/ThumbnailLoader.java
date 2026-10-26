package com.myliba.android;

import android.app.Activity;
import android.content.Context;
import android.graphics.*;
import android.graphics.pdf.PdfRenderer;
import android.media.MediaMetadataRetriever;
import android.net.Uri;
import android.os.Build;
import android.os.ParcelFileDescriptor;
import android.util.LruCache;
import android.util.Size;
import android.view.View;
import android.widget.ImageView;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.concurrent.*;

final class ThumbnailLoader {
    private final Activity activity;
    private final Context context;
    private final ExecutorService pool=Executors.newFixedThreadPool(3);
    private final LruCache<String,Bitmap> cache=new LruCache<String,Bitmap>(12*1024*1024){@Override protected int sizeOf(String k,Bitmap b){return b.getAllocationByteCount();}};
    ThumbnailLoader(Activity a){activity=a;context=a;}
    ThumbnailLoader(Context c){activity=null;context=c.getApplicationContext();}
    void load(LibraryStore store,LibraryStore.Entry e,ImageView target,View fallback){
        if(!e.available||!(e.kind.equals("image")||e.kind.equals("video")||(!store.remote&&e.mime.equals("application/pdf"))))return;
        String key=(store.remote?store.client.base:"local")+e.id+e.modified+e.size;target.setTag(key);target.setImageDrawable(null);
        Bitmap hit=cache.get(key);if(hit!=null){target.setImageBitmap(hit);fallback.setVisibility(View.GONE);return;}
        pool.execute(()->{try{Bitmap b=store.remote?read(store,e):localThumbnail(store,e,false);if(b==null)return;cache.put(key,b);activity.runOnUiThread(()->{if(!activity.isFinishing()&&key.equals(target.getTag())){target.setImageBitmap(b);fallback.setVisibility(View.GONE);}});}catch(Exception ignored){}});
    }
    boolean generate(LibraryStore.Entry entry,boolean force)throws Exception{
        if(!(entry.kind.equals("image")||entry.kind.equals("video")||entry.mime.equals("application/pdf")))return false;
        Bitmap bitmap=localThumbnail(null,entry,force);if(bitmap==null)return false;
        File web=cacheFile(context,entry.id,entry.modified,entry.size,"web");if(force||!web.isFile()){Bitmap variant=fit(bitmap,480,320);try{saveLocalThumbnail(web,variant);}finally{if(variant!=bitmap)variant.recycle();}}
        cache.put("local"+entry.id+entry.modified+entry.size,bitmap);return true;
    }
    private Bitmap localThumbnail(LibraryStore store,LibraryStore.Entry entry,boolean force)throws Exception{
        File file=cacheFile(context,entry.id,entry.modified,entry.size,"mobile");
        if(!force&&file.isFile()){Bitmap cached=BitmapFactory.decodeFile(file.getAbsolutePath());if(cached!=null){file.setLastModified(System.currentTimeMillis());return cached;}}
        Bitmap bitmap=read(store,entry);if(bitmap==null)return null;
        saveLocalThumbnail(file,bitmap);
        return bitmap;
    }
    static File cacheFile(Context context,String id,long modified,long size)throws Exception{
        return cacheFile(context,id,modified,size,"mobile");
    }
    private static File cacheFile(Context context,String id,long modified,long size,String variant)throws Exception{
        File directory=new File(context.getCacheDir(),"resource-thumbnails");
        MessageDigest digest=MessageDigest.getInstance("SHA-256");byte[] hash=digest.digest((id+"|"+modified+"|"+size).getBytes(StandardCharsets.UTF_8));
        StringBuilder name=new StringBuilder();for(byte b:hash)name.append(String.format(java.util.Locale.ROOT,"%02x",b&255));
        return new File(directory,name+"-"+variant+".jpg");
    }
    static void saveLocalThumbnail(File file,Bitmap bitmap)throws IOException{
        File directory=file.getParentFile();
        if(!directory.isDirectory()&&!directory.mkdirs())throw new IOException("无法创建缩略图目录");
        File temporary=File.createTempFile("thumb-",".tmp",directory);
        try{
            try(FileOutputStream out=new FileOutputStream(temporary)){if(!bitmap.compress(Bitmap.CompressFormat.JPEG,78,out))throw new IOException("无法保存缩略图");}
            if(!temporary.renameTo(file)){file.delete();if(!temporary.renameTo(file))throw new IOException("无法保存缩略图");}
        }finally{temporary.delete();}
    }
    private Bitmap read(LibraryStore s,LibraryStore.Entry e)throws Exception{
        Uri uri;
        if(s!=null&&s.remote){
            String localUri=s.client.selectedNodeId.equals(ResourceNodeService.ensureID(context))?NodeResourceApi.decodeID(e.id):"";
            if(localUri.isEmpty()||s.local.item(localUri)==null){
                String endpoint="/api/resources/"+RemoteClient.enc(e.id)+"/thumbnail";
                byte[] data;
                try { data=s.client.bytes("GET",endpoint+"?size=mobile",null,3*1024*1024); }
                catch(RemoteClient.Failure error){if(error.status!=404)throw error;data=s.client.bytes("GET",endpoint,null,3*1024*1024);}
                return BitmapFactory.decodeByteArray(data,0,data.length);
            }
            uri=Uri.parse(localUri);
        }else uri=Uri.parse(e.id);
        if(Build.VERSION.SDK_INT>=29){try{return fitMobile(context.getContentResolver().loadThumbnail(uri,new Size(720,960),null));}catch(Exception ignored){}}
        if(e.kind.equals("video"))return videoFrame(context,uri,0);
        if(e.mime.equals("application/pdf")){try(ParcelFileDescriptor fd=context.getContentResolver().openFileDescriptor(uri,"r");PdfRenderer r=new PdfRenderer(fd);PdfRenderer.Page p=r.openPage(0)){float scale=Math.min(1f,Math.min(720f/p.getWidth(),960f/p.getHeight()));Bitmap b=Bitmap.createBitmap(Math.max(1,Math.round(p.getWidth()*scale)),Math.max(1,Math.round(p.getHeight()*scale)),Bitmap.Config.ARGB_8888);b.eraseColor(Color.WHITE);p.render(b,null,null,PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY);return b;}}
        BitmapFactory.Options options=new BitmapFactory.Options();options.inJustDecodeBounds=true;try(InputStream in=context.getContentResolver().openInputStream(uri)){BitmapFactory.decodeStream(in,null,options);}options.inJustDecodeBounds=false;options.inSampleSize=1;while(options.outWidth/options.inSampleSize>720||options.outHeight/options.inSampleSize>960)options.inSampleSize*=2;options.inPreferredConfig=Bitmap.Config.RGB_565;try(InputStream in=context.getContentResolver().openInputStream(uri)){return fitMobile(BitmapFactory.decodeStream(in,null,options));}
    }
    private static Bitmap fitMobile(Bitmap source){
        return fit(source,720,960);
    }
    private static Bitmap fit(Bitmap source,int maxWidth,int maxHeight){
        if(source==null||source.getWidth()<=maxWidth&&source.getHeight()<=maxHeight)return source;
        double scale=Math.min((double)maxWidth/source.getWidth(),(double)maxHeight/source.getHeight());
        Bitmap fitted=Bitmap.createScaledBitmap(source,Math.max(1,(int)Math.round(source.getWidth()*scale)),Math.max(1,(int)Math.round(source.getHeight()*scale)),true);
        if(fitted!=source)source.recycle();
        return fitted;
    }
    static Bitmap videoFrame(Context activity,Uri uri,long atMicros)throws Exception{
        MediaMetadataRetriever retriever=new MediaMetadataRetriever();
        try{
            retriever.setDataSource(activity,uri);
            int width=Integer.parseInt(retriever.extractMetadata(MediaMetadataRetriever.METADATA_KEY_VIDEO_WIDTH));
            int height=Integer.parseInt(retriever.extractMetadata(MediaMetadataRetriever.METADATA_KEY_VIDEO_HEIGHT));
            if(width<1||height<1)throw new IOException("无法读取视频尺寸");
            double scale=Math.min(1.0,Math.min(720.0/width,960.0/height));
            int targetWidth=Math.max(1,(int)Math.round(width*scale));
            int targetHeight=Math.max(1,(int)Math.round(height*scale));
            if(Build.VERSION.SDK_INT>=27)return retriever.getScaledFrameAtTime(atMicros,MediaMetadataRetriever.OPTION_CLOSEST_SYNC,targetWidth,targetHeight);
            Bitmap full=retriever.getFrameAtTime(atMicros,MediaMetadataRetriever.OPTION_CLOSEST_SYNC);
            if(full==null)return null;
            Bitmap scaled=Bitmap.createScaledBitmap(full,targetWidth,targetHeight,true);
            if(scaled!=full)full.recycle();
            return scaled;
        }finally{retriever.release();}
    }
    void clear(){cache.evictAll();}
    void close(){pool.shutdownNow();cache.evictAll();}
}
