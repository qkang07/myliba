package com.myliba.android;

import android.app.Activity;
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
import java.util.concurrent.*;

final class ThumbnailLoader {
    private final Activity activity;
    private final ExecutorService pool=Executors.newFixedThreadPool(3);
    private final LruCache<String,Bitmap> cache=new LruCache<String,Bitmap>(12*1024*1024){@Override protected int sizeOf(String k,Bitmap b){return b.getAllocationByteCount();}};
    ThumbnailLoader(Activity a){activity=a;}
    void load(LibraryStore store,LibraryStore.Entry e,ImageView target,View fallback){
        if(!e.available||!(e.kind.equals("image")||e.kind.equals("video")||(!store.remote&&e.mime.equals("application/pdf"))))return;
        String key=(store.remote?store.client.base:"local")+e.id+e.modified+e.size;target.setTag(key);target.setImageDrawable(null);
        Bitmap hit=cache.get(key);if(hit!=null){target.setImageBitmap(hit);fallback.setVisibility(View.GONE);return;}
        pool.execute(()->{try{Bitmap b=read(store,e);if(b==null)return;cache.put(key,b);activity.runOnUiThread(()->{if(!activity.isFinishing()&&key.equals(target.getTag())){target.setImageBitmap(b);fallback.setVisibility(View.GONE);}});}catch(Exception ignored){}});
    }
    private Bitmap read(LibraryStore s,LibraryStore.Entry e)throws Exception{
        if(s.remote){byte[] data=s.client.bytes("GET","/api/resources/"+RemoteClient.enc(e.id)+"/thumbnail",null,3*1024*1024);return BitmapFactory.decodeByteArray(data,0,data.length);}
        Uri uri=Uri.parse(e.id);
        if(Build.VERSION.SDK_INT>=29){try{return activity.getContentResolver().loadThumbnail(uri,new Size(320,240),null);}catch(Exception ignored){}}
        if(e.kind.equals("video")){if(Build.VERSION.SDK_INT<27)return null;MediaMetadataRetriever r=new MediaMetadataRetriever();try{r.setDataSource(activity,uri);return r.getScaledFrameAtTime(0,MediaMetadataRetriever.OPTION_CLOSEST_SYNC,320,240);}finally{r.release();}}
        if(e.mime.equals("application/pdf")){try(ParcelFileDescriptor fd=activity.getContentResolver().openFileDescriptor(uri,"r");PdfRenderer r=new PdfRenderer(fd);PdfRenderer.Page p=r.openPage(0)){Bitmap b=Bitmap.createBitmap(240,320,Bitmap.Config.ARGB_8888);b.eraseColor(Color.WHITE);p.render(b,null,null,PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY);return b;}}
        BitmapFactory.Options options=new BitmapFactory.Options();options.inJustDecodeBounds=true;try(InputStream in=activity.getContentResolver().openInputStream(uri)){BitmapFactory.decodeStream(in,null,options);}options.inJustDecodeBounds=false;options.inSampleSize=1;while(options.outWidth/options.inSampleSize>640||options.outHeight/options.inSampleSize>640)options.inSampleSize*=2;options.inPreferredConfig=Bitmap.Config.RGB_565;try(InputStream in=activity.getContentResolver().openInputStream(uri)){return BitmapFactory.decodeStream(in,null,options);}
    }
    void clear(){cache.evictAll();}
    void close(){pool.shutdownNow();cache.evictAll();}
}
