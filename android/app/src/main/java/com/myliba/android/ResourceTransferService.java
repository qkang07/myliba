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
    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("resource-transfer","Myliba 文件传输",NotificationManager.IMPORTANCE_LOW));recoverInterrupted();File[] abandoned=stagingDirectory().listFiles((dir,name)->name.startsWith("myliba-transfer-")&&name.endsWith(".part"));if(abandoned!=null)for(File file:abandoned)file.delete();}
    private File stagingDirectory(){File external=getExternalFilesDir(null);return external!=null&&external.isDirectory()?external:getCacheDir();}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(intent==null)return START_NOT_STICKY;
        if("cancel".equals(intent.getAction())){String id=intent.getStringExtra("id");AtomicBoolean flag=cancelled.get(id);if(flag!=null)flag.set(true);return START_NOT_STICKY;}
        String id=UUID.randomUUID().toString(),name=intent.getStringExtra("name"),resource=intent.getStringExtra("resource"),base=intent.getStringExtra("base"),node=intent.getStringExtra("node");
        long total=intent.getLongExtra("total",-1);boolean cloud=intent.getBooleanExtra("cloud",false);
        String revision=intent.getStringExtra("revision"),locations=intent.getStringExtra("locations");
        String fileId=intent.getStringExtra("fileId");
        if("retry".equals(intent.getAction())){
            id=intent.getStringExtra("id");JSONObject previous=find(id);if(previous==null||cancelled.containsKey(id)||!("失败".equals(previous.optString("status"))||"已取消".equals(previous.optString("status")))){if(active.get()==0)stopSelf(startId);return START_NOT_STICKY;}
            name=previous.optString("name");resource=previous.optString("resource");base=previous.optString("base");node=previous.optString("node");total=previous.optLong("total",-1);cloud=previous.optBoolean("cloud",false);
            revision=previous.optString("revision");locations=previous.optString("locations","[]");
            fileId=previous.optString("fileId",resource);
        }
        if(id==null||name==null||name.isEmpty()||resource==null||resource.isEmpty()||base==null||base.isEmpty()){if(active.get()==0)stopSelf(startId);return START_NOT_STICKY;}
        AtomicBoolean flag=new AtomicBoolean();cancelled.put(id,flag);record(id,name,total,0,"等待下载","","",base,resource,node,cloud,revision,locations,fileId);
        active.incrementAndGet();if(Build.VERSION.SDK_INT>=34)startForeground(4321,notification("下载中 · "+name),ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4321,notification("下载中 · "+name));
        final String taskId=id,taskName=name,taskResource=resource,taskBase=base,taskNode=node,taskRevision=revision,taskLocations=locations,taskFileId=fileId;final long taskTotal=total;final boolean taskCloud=cloud;
        work.execute(()->{download(taskId,taskName,taskResource,taskBase,taskNode,taskTotal,taskCloud,taskRevision,taskLocations,taskFileId,flag);cancelled.remove(taskId);if(active.decrementAndGet()<=0)stopSelf();});
        return START_STICKY;
    }
    private Notification notification(String text){Intent open=new Intent(this,MainActivity.class);PendingIntent click=PendingIntent.getActivity(this,2,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this,"resource-transfer").setSmallIcon(android.R.drawable.stat_sys_download).setContentTitle("Myliba 文件传输").setContentText(text).setContentIntent(click).setOngoing(true).build();}
    private void download(String id,String name,String resource,String base,String node,long total,boolean cloud,String revision,String locations,String fileId,AtomicBoolean flag){
        Uri destination=null;File fallback=null,temp=null;RemoteClient client=null;long[] count={0};
        try{
            client=new RemoteClient(base,getSharedPreferences("MainActivity",MODE_PRIVATE),this);
            JSONObject session=client.json("GET","/api/session",null);if(node!=null&&!node.isEmpty()&&!node.equals(session.optString("selectedNodeId")))throw new IOException("当前 Hub 资源节点已切换，请重新下载");
            JSONArray resolvedCandidates=null;
            if(revision!=null&&!revision.isEmpty()&&fileId!=null&&!fileId.isEmpty())try{
                JSONObject resolved=client.json("POST","/api/library/files/"+RemoteClient.enc(fileId==null||fileId.isEmpty()?resource:fileId)+"/resolve",new JSONObject().put("revisionId",revision).put("purpose","original"));
                resolvedCandidates=resolved.optJSONArray("candidates");
            }catch(Exception ignored){/* Saved search locations remain the fallback if resolution is temporarily unavailable. */}
            temp=File.createTempFile("myliba-transfer-",".part",stagingDirectory());record(id,name,total,0,"下载中","","");
            Exception failure=null;boolean complete=false;
            if(resolvedCandidates!=null)for(int i=0;i<resolvedCandidates.length()&&!complete&&!flag.get();i++){
                JSONObject candidate=resolvedCandidates.optJSONObject(i);if(candidate==null)continue;
                String path=candidate.optString("readPath"),ticket=candidate.optString("readTicket"),sourceNode=candidate.optString("nodeId"),ref=candidate.optString("resourceRef");
                if(!path.startsWith("/api/")||path.contains("..")||!revision.equals(candidate.optString("revisionId")))continue;
                try{
                    JSONObject before=null;long expected=-1;String sourceVersion="";
                    if("device".equals(candidate.optString("kind"))){if(sourceNode.isEmpty()||ref.isEmpty())continue;client.selectNode(sourceNode);before=client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);expected=before.optLong("size",-1);sourceVersion=before.optString("versionId");if(!before.optBoolean("available",true)||!revision.equals(sourceVersion)||total>=0&&expected>=0&&total!=expected)continue;}
                    else if(node!=null&&!node.isEmpty()&&!node.equals(client.selectedNodeId))client.selectNode(node);
                    fetchCandidate(client,path,temp,id,name,total,flag,count,candidate.optString("plainHash"),ticket);
                    if(before!=null){JSONObject after=client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);if(!after.optBoolean("available",true)||!sourceVersion.equals(after.optString("versionId"))||expected>=0&&after.optLong("size",-1)!=expected)throw new IOException("来源在下载期间发生变化");}
                    complete=true;
                }catch(Exception ex){failure=ex;}
            }
            if(!complete&&resolvedCandidates==null){
                String cloudPath="/api/cloud/files/"+RemoteClient.enc(resource)+(revision==null||revision.isEmpty()?"/file":"/versions/"+RemoteClient.enc(revision)+"/file")+"?download=1";
                try{fetchCandidate(client,cloud?cloudPath:"/api/resources/"+RemoteClient.enc(resource)+"/file?download=1",temp,id,name,total,flag,count,"","");complete=true;}
                catch(Exception ex){failure=ex;}
            }
            if(!complete&&resolvedCandidates==null&&cloud&&!flag.get()&&locations!=null){
                JSONArray candidates=new JSONArray(locations);
                for(int i=0;i<candidates.length()&&!complete;i++){
                    JSONObject candidate=candidates.optJSONObject(i);if(candidate==null)continue;
                    String sourceNode=candidate.optString("nodeId"),ref=candidate.optString("resourceRef");if(sourceNode.isEmpty()||ref.isEmpty())continue;
                    try{
                        client.selectNode(sourceNode);
                        JSONObject before=client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);
                        long expected=before.optLong("size",-1);String sourceVersion=before.optString("versionId");
                        if(!before.optBoolean("available",true)||revision!=null&&!revision.isEmpty()&&!revision.equals(sourceVersion)||total>=0&&expected>=0&&total!=expected)continue;
                        fetchCandidate(client,"/api/resources/"+RemoteClient.enc(ref)+"/file?download=1",temp,id,name,total,flag,count,"","");
                        JSONObject after=client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);
                        if(!after.optBoolean("available",true)||!sourceVersion.equals(after.optString("versionId"))||expected>=0&&after.optLong("size",-1)!=expected)throw new IOException("来源在下载期间发生变化");
                        complete=true;
                    }catch(Exception ex){failure=ex;}
                }
            }
            if(!complete)throw failure==null?new IOException("没有可读取的来源"):failure;
            if(flag.get())throw new IOException("已取消");
            OutputStream output;
            if(Build.VERSION.SDK_INT>=29){
                ContentValues values=new ContentValues();values.put(MediaStore.Downloads.DISPLAY_NAME,name.replace('/','_').replace('\\','_'));values.put(MediaStore.Downloads.MIME_TYPE,"application/octet-stream");values.put(MediaStore.Downloads.RELATIVE_PATH,Environment.DIRECTORY_DOWNLOADS+"/Myliba");values.put(MediaStore.Downloads.IS_PENDING,1);
                destination=getContentResolver().insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI,values);if(destination==null)throw new IOException("无法创建下载文件");output=getContentResolver().openOutputStream(destination);
            }else{File folder=new File(getFilesDir(),"rtc-downloads");if(!folder.exists()&&!folder.mkdirs())throw new IOException("无法创建下载目录");fallback=new File(folder,id);output=new FileOutputStream(fallback);}
            if(output==null)throw new IOException("无法写入下载文件");
            try(InputStream input=new FileInputStream(temp);OutputStream target=output){byte[] buffer=new byte[64<<10];int n;while((n=input.read(buffer))!=-1){if(flag.get())throw new IOException("已取消");target.write(buffer,0,n);}}
            if(Build.VERSION.SDK_INT>=29){ContentValues done=new ContentValues();done.put(MediaStore.Downloads.IS_PENDING,0);getContentResolver().update(destination,done,null,null);}
            record(id,name,total,count[0],"已完成",destination!=null?destination.toString():ResourceDownloadProvider.uri(id,name).toString(),"");
        }catch(Exception ex){if(destination!=null)getContentResolver().delete(destination,null,null);if(fallback!=null)fallback.delete();record(id,name,total,count[0],flag.get()?"已取消":"失败","",ex.getMessage()==null?"传输中断":ex.getMessage());}
        finally{if(client!=null){try{if(node!=null&&!node.isEmpty())client.selectNode(node);}catch(Exception ignored){}client.close();}if(temp!=null)temp.delete();}
    }

    private void fetchCandidate(RemoteClient client,String path,File temp,String id,String name,long total,AtomicBoolean flag,long[] count,String plainHash,String readTicket)throws Exception{
        count[0]=0;long[] last={0};
        try(OutputStream target=new FileOutputStream(temp,false)){
            OutputStream progress=new FilterOutputStream(target){
                @Override public void write(int value)throws IOException{check();out.write(value);update(1);}
                @Override public void write(byte[] bytes,int off,int len)throws IOException{check();out.write(bytes,off,len);update(len);}
                private void check()throws IOException{if(flag.get())throw new IOException("已取消");}
                private void update(int n){count[0]+=n;long now=System.currentTimeMillis();if(now-last[0]>350){record(id,name,total,count[0],"下载中","","");last[0]=now;}}
            };
            client.download(path,progress,readTicket);progress.flush();
        }
        if(total>=0&&count[0]!=total)throw new IOException("下载大小与索引版本不一致");
        if(plainHash!=null&&plainHash.matches("[0-9a-f]{64}")){
            java.security.MessageDigest digest=java.security.MessageDigest.getInstance("SHA-256");
            try(InputStream input=new FileInputStream(temp)){byte[] buffer=new byte[64<<10];int n;while((n=input.read(buffer))!=-1){if(flag.get())throw new IOException("已取消");digest.update(buffer,0,n);}}
            if(!LocalIndexCrypto.hex(digest.digest()).equals(plainHash))throw new IOException("下载内容校验失败");
        }
    }
    static JSONArray records(Context context){synchronized(RECORDS){try{return new JSONArray(context.getSharedPreferences(PREFS,MODE_PRIVATE).getString("items","[]"));}catch(Exception ex){return new JSONArray();}}}
    private JSONObject find(String id){if(id==null)return null;JSONArray items=records(this);for(int i=0;i<items.length();i++){JSONObject item=items.optJSONObject(i);if(item!=null&&id.equals(item.optString("id")))return item;}return null;}
    private void recoverInterrupted(){synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean changed=false;for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;String state=item.optString("status");if("下载中".equals(state)||"等待下载".equals(state)){try{item.put("status","失败").put("error","传输中断，请重新下载");changed=true;}catch(JSONException ignored){}}next.put(item);}if(changed)getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error){record(id,name,total,downloaded,state,uri,error,null,null,null,null);}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error,String base,String resource,String node){record(id,name,total,downloaded,state,uri,error,base,resource,node,null);}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error,String base,String resource,String node,Boolean cloud){record(id,name,total,downloaded,state,uri,error,base,resource,node,cloud,null,null);}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error,String base,String resource,String node,Boolean cloud,String revision,String locations){record(id,name,total,downloaded,state,uri,error,base,resource,node,cloud,revision,locations,null);}
    private void record(String id,String name,long total,long downloaded,String state,String uri,String error,String base,String resource,String node,Boolean cloud,String revision,String locations,String fileId){synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean found=false;for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;if(id.equals(item.optString("id"))){found=true;next.put(entry(id,name,total,downloaded,state,uri,error,base==null?item.optString("base"):base,resource==null?item.optString("resource"):resource,node==null?item.optString("node"):node,cloud==null?item.optBoolean("cloud"):cloud,revision==null?item.optString("revision"):revision,locations==null?item.optString("locations","[]"):locations,fileId==null?item.optString("fileId"):fileId));}else next.put(item);}if(!found)next.put(entry(id,name,total,downloaded,state,uri,error,base,resource,node,cloud!=null&&cloud,revision,locations,fileId));getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    private static JSONObject entry(String id,String name,long total,long downloaded,String state,String uri,String error,String base,String resource,String node,boolean cloud,String revision,String locations,String fileId){return RemoteClient.object("id",id,"name",name,"total",total,"downloaded",downloaded,"status",state,"uri",uri,"error",error,"base",base==null?"":base,"resource",resource==null?"":resource,"node",node==null?"":node,"cloud",cloud,"revision",revision==null?"":revision,"locations",locations==null?"[]":locations,"fileId",fileId==null?"":fileId);}
    static void remove(Context context,String id){synchronized(RECORDS){JSONArray old=records(context),next=new JSONArray();for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item!=null&&!id.equals(item.optString("id")))next.put(item);}context.getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    @Override public IBinder onBind(Intent intent){return null;}
    @Override public void onDestroy(){work.shutdownNow();super.onDestroy();}
}
