package com.myliba.android;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/** Continues local thumbnail preprocessing while the activity is closed. */
public final class LocalThumbnailTaskService extends Service {
    static final String PREFS="local_thumbnail_tasks";
    static volatile boolean running;
    static volatile boolean stopRequested;
    static volatile String status="尚未运行";
    static boolean isEnabled(){return running&&!stopRequested;}
    private final ExecutorService worker=Executors.newSingleThreadExecutor();
    private volatile boolean stopped;

    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("local-thumbnails","本机缩略图任务",NotificationManager.IMPORTANCE_LOW));}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if("stop".equals(intent==null?null:intent.getAction())){stopped=true;stopRequested=true;status="正在停止…";getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("status",status).apply();return START_NOT_STICKY;}
        if(running)return START_STICKY;
        stopped=false;stopRequested=false;running=true;status="准备处理本机缩略图";
        Notification notification=notification(status);
        if(Build.VERSION.SDK_INT>=34)startForeground(4322,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4322,notification);
        getSharedPreferences(PREFS,MODE_PRIVATE).edit().putBoolean("running",true).putString("status",status).apply();
        worker.execute(this::process);return START_STICKY;
    }
    private Notification notification(String text){return new Notification.Builder(this,"local-thumbnails").setSmallIcon(android.R.drawable.ic_menu_gallery).setContentTitle("Myliba 本机缩略图").setContentText(text).setOngoing(true).build();}
    private void update(String value,int done,int total){status=value;getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("status",value).putInt("done",done).putInt("total",total).apply();getSystemService(NotificationManager.class).notify(4322,notification(value));}
    private void process(){int done=0,generated=0,failed=0,offset=0,total=0;LocalLibrary db=null;ThumbnailLoader loader=new ThumbnailLoader(this);try{
        db=new LocalLibrary(this);LibraryStore store=new LibraryStore(db,null);total=store.page(new LocalFilters(),0).total;
        while(!stopped){LibraryStore.Page page=store.page(new LocalFilters(),offset);if(page.items.isEmpty())break;
            for(LibraryStore.Entry entry:page.items){if(stopped)break;try{if(loader.generate(entry,false))generated++;}catch(Exception ex){failed++;AppLog.error("Local thumbnail task failed: "+entry.name,ex);}done++;if(done%10==0)update("处理中 "+done+" / "+total+" · 已生成 "+generated,done,total);}
            offset+=page.items.size();if(offset>=page.total)break;
        }
        update(stopped?"已停止 · 已生成 "+generated:"已完成 · 已生成 "+generated+(failed>0?" · 失败 "+failed:""),done,total);
    }catch(Exception ex){update("处理失败："+(ex.getMessage()==null?"未知错误":ex.getMessage()),done,total);AppLog.error("Local thumbnail task failed",ex);
    }finally{if(db!=null)db.close();running=false;stopRequested=false;getSharedPreferences(PREFS,MODE_PRIVATE).edit().putBoolean("running",false).putString("status",status).apply();stopForeground(STOP_FOREGROUND_REMOVE);stopSelf();}}
    @Override public void onDestroy(){stopped=true;running=false;stopRequested=false;getSharedPreferences(PREFS,MODE_PRIVATE).edit().putBoolean("running",false).putString("status",status).apply();worker.shutdownNow();super.onDestroy();}
    @Override public IBinder onBind(Intent intent){return null;}
}
