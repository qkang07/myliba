package com.myliba.android;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.net.Uri;
import android.os.*;
import android.provider.MediaStore;
import org.json.*;
import java.io.*;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;

/** Foreground RTC downloads with persisted progress and failure state. */
public final class ResourceTransferService extends Service {
    static final String PREFS="resource_transfers";
    private static final Object RECORDS=new Object();
    private final ExecutorService work=Executors.newSingleThreadExecutor();
    private final Map<String,AtomicBoolean> cancelled=new ConcurrentHashMap<>();
    private final AtomicInteger active=new AtomicInteger();
    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("resource-transfer","Myliba 文件传输",NotificationManager.IMPORTANCE_LOW));recoverInterrupted();}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(intent==null)return START_NOT_STICKY;
        if("cancel".equals(intent.getAction())){String id=intent.getStringExtra("id");AtomicBoolean flag=cancelled.get(id);if(flag!=null)flag.set(true);return START_NOT_STICKY;}
        String id=UUID.randomUUID().toString(),name=intent.getStringExtra("name"),resource=intent.getStringExtra("resource"),base=intent.getStringExtra("base"),node=intent.getStringExtra("node");
        long total=intent.getLongExtra("total",-1);if(name==null||resource==null||base==null)return START_NOT_STICKY;
        AtomicBoolean flag=new AtomicBoolean();cancelled.put(id,flag);record(id,name,total,0,"等待下载","","");
        active.incrementAndGet();if(Build.VERSION.SDK_INT>=34)startForeground(4321,notification("下载中 · "+name),ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4321,notification("下载中 · "+name));
        work.execute(()->{download(id,name,resource,base,node,total,flag);cancelled.remove(id);if(active.decrementAndGet()<=0)stopSelf();});
        return START_STICKY;
    }
    private Notification notification(String text){Intent open=new Intent(this,MainActivity.class);PendingIntent click=PendingIntent.getActivity(this,2,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this,"resource-transfer").setSmallIcon(android.R.drawable.stat_sys_download).setContentTitle("Myliba 文件传输").setContentText(text).setContentIntent(click).setOngoing(true).build();}
    private void download(String id,String name,String resource,String base,String node,long total,AtomicBoolean flag){
        Uri destination=null;File fallback=null;RemoteClient client=null;long[] count={0};long[] last={0};
        try{
            client=new RemoteClient(base,getSharedPreferences("MainActivity",MODE_PRIVATE),this);
            JSONObject session=client.json("GET","/api/session",null);if(node!=null&&!node.isEmpty()&&!node.equals(session.optString("selectedNodeId")))throw new IOException("当前 Hub 资源节点已切换，请重新下载");
            OutputStream output;
            if(Build.VERSION.SDK_INT>=29){
                ContentValues values=new ContentValues();values.put(MediaStore.Downloads.DISPLAY_NAME,name.replace('/','_').replace('\\','_'));values.put(MediaStore.Downloads.MIME_TYPE,"application/octet-stream");values.put(MediaStore.Downloads.RELATIVE_PATH,Environment.DIRECTORY_DOWNLOADS+"/Myliba");values.put(MediaStore.Downloads.IS_PENDING,1);
                destination=getContentResolver().insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI,values);if(destination==null)throw new IOException("无法创建下载文件");output=getContentResolver().openOutputStream(destination);
            }else{File folder=new File(getFilesDir(),"rtc-downloads");if(!folder.exists()&&!folder.mkdirs())throw new IOException("无法创建下载目录");fallback=new File(folder,id);output=new FileOutputStream(fallback);}
            if(output==null)throw new IOException("无法写入下载文件");record(id,name,total,0,"下载中","","");
            try(OutputStream target=output){OutputStream progress=new FilterOutputStream(target){
                @Override public void write(int value)throws IOException{check();out.write(value);update(1);}
                @Override public void write(byte[] bytes,int off,int len)throws IOException{check();out.write(bytes,off,len);update(len);}
                private void check()throws IOException{if(flag.get())throw new IOException("已取消");}
                private void update(int n){count[0]+=n;long now=System.currentTimeMillis();if(now-last[0]>350){record(id,name,total,count[0],"下载中","","");last[0]=now;}}
            };client.download("/api/resources/"+RemoteClient.enc(resource)+"/file?download=1",progress);progress.flush();}
            if(Build.VERSION.SDK_INT>=29){ContentValues done=new ContentValues();done.put(MediaStore.Downloads.IS_PENDING,0);getContentResolver().update(destination,done,null,null);}
            record(id,name,total,count[0],"已完成",destination!=null?destination.toString():ResourceDownloadProvider.uri(id,name).toString(),"");
        }catch(Exception ex){if(destination!=null)getContentResolver().delete(destination,null,null);if(fallback!=null)fallback.delete();record(id,name,total,count[0],flag.get()?"已取消":"失败","",ex.getMessage()==null?"传输中断":ex.getMessage());}
        finally{if(client!=null)client.close();}
    }
    static JSONArray records(Context context){synchronized(RECORDS){try{return new JSONArray(context.getSharedPreferences(PREFS,MODE_PRIVATE).getString("items","[]"));}catch(Exception ex){return new JSONArray();}}}
    private void recoverInterrupted(){synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean changed=false;for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;String state=item.optString("status");if("下载中".equals(state)||"等待下载".equals(state)){try{item.put("status","失败").put("error","传输中断，请重新下载");changed=true;}catch(JSONException ignored){}}next.put(item);}if(changed)getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error){synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean found=false;for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;if(id.equals(item.optString("id"))){found=true;next.put(entry(id,name,total,downloaded,state,uri,error));}else next.put(item);}if(!found)next.put(entry(id,name,total,downloaded,state,uri,error));getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    private static JSONObject entry(String id,String name,long total,long downloaded,String state,String uri,String error){return RemoteClient.object("id",id,"name",name,"total",total,"downloaded",downloaded,"status",state,"uri",uri,"error",error);}
    static void remove(Context context,String id){synchronized(RECORDS){JSONArray old=records(context),next=new JSONArray();for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item!=null&&!id.equals(item.optString("id")))next.put(item);}context.getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    @Override public IBinder onBind(Intent intent){return null;}
    @Override public void onDestroy(){work.shutdownNow();super.onDestroy();}
}
