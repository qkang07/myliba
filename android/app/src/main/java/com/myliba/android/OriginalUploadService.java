package com.myliba.android;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.database.Cursor;
import android.net.Uri;
import android.os.*;
import android.provider.DocumentsContract;
import org.json.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.time.Instant;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Foreground phone-original upload and safe release (docs/unified-library-design.md
 * §9, P5). The original is pushed to the selected query node in <=1 MiB plaintext
 * chunks strictly over the P2P RTC channel so the Hub never sees content; the node
 * encrypts, stores, read-back-verifies and commits. Release re-checks the cloud
 * safety gate, confirms the original is unchanged, deletes the SAF document and
 * only then asks the node to remove the local location. Tasks persist across
 * process death and interrupted tasks are marked failed rather than silently lost.
 */
public final class OriginalUploadService extends Service {
    static final String PREFS="original_uploads";
    private static final int CHUNK=1<<20;
    private static final Object RECORDS=new Object();
    private final ExecutorService work=Executors.newSingleThreadExecutor();
    private final Map<String,AtomicBoolean> cancelled=new ConcurrentHashMap<>();
    private final AtomicInteger active=new AtomicInteger();
    private boolean recoveryStarted;

    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("original-upload","Myliba 原件入云",NotificationManager.IMPORTANCE_LOW));recoverInterrupted();}

    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(intent==null){resumePendingReleases();if(active.get()==0)stopSelf(startId);return active.get()>0?START_STICKY:START_NOT_STICKY;}
        String action=intent.getAction();
        if("retry-release".equals(action)){retryRelease(intent.getStringExtra("id"));resumePendingReleases();if(active.get()==0)stopSelf(startId);return active.get()>0?START_STICKY:START_NOT_STICKY;}
        resumePendingReleases();
        if("resume-releases".equals(action)){if(active.get()==0)stopSelf(startId);return active.get()>0?START_STICKY:START_NOT_STICKY;}
        if("cancel".equals(action)){String id=intent.getStringExtra("id");AtomicBoolean flag=cancelled.get(id);if(flag!=null)flag.set(true);return START_NOT_STICKY;}
        if("release".equals(action)){startRelease(intent,startId);return START_STICKY;}
        String id=UUID.randomUUID().toString();
        if("retry".equals(action)){
            id=intent.getStringExtra("id");JSONObject previous=find(id);
            if(previous==null||cancelled.containsKey(id)||!("失败".equals(previous.optString("status"))||"已取消".equals(previous.optString("status")))){if(active.get()==0)stopSelf(startId);return START_NOT_STICKY;}
            startUpload(previous.optString("id"),previous.optString("uri"),previous.optString("name"),previous.optString("fileId"),previous.optString("revisionId"),
                previous.optString("base"),previous.optString("node"),previous.optString("nodeId"),previous.optString("storageId"),previous.optString("resourceRef"),
                previous.optLong("total",-1),previous.optString("modified"),startId);
            return START_STICKY;
        }
        String uri=intent.getStringExtra("uri"),name=intent.getStringExtra("name"),fileId=intent.getStringExtra("fileId"),revisionId=intent.getStringExtra("revisionId"),
            base=intent.getStringExtra("base"),node=intent.getStringExtra("node"),nodeId=intent.getStringExtra("nodeId"),storageId=intent.getStringExtra("storageId"),
            resourceRef=intent.getStringExtra("resourceRef"),modified=intent.getStringExtra("modified");
        long total=intent.getLongExtra("total",-1);
        if(uri==null||fileId==null||fileId.isEmpty()||revisionId==null||revisionId.isEmpty()||base==null||base.isEmpty()||total<0){if(active.get()==0)stopSelf(startId);return START_NOT_STICKY;}
        startUpload(id,uri,name==null?"":name,fileId,revisionId,base,node==null?"":node,nodeId==null?"":nodeId,storageId==null?"":storageId,resourceRef==null?"":resourceRef,total,modified==null?"":modified,startId);
        return START_STICKY;
    }

    private void startUpload(String id,String uri,String name,String fileId,String revisionId,String base,String node,String nodeId,String storageId,String resourceRef,long total,String modified,int startId){
        AtomicBoolean flag=new AtomicBoolean();cancelled.put(id,flag);
        record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,0,"等待上传","","",modified);
        active.incrementAndGet();
        if(Build.VERSION.SDK_INT>=34)startForeground(4323,notification("原件入云 · "+name),ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4323,notification("原件入云 · "+name));
        work.execute(()->{upload(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,modified,flag);cancelled.remove(id);if(active.decrementAndGet()<=0)stopSelf();});
    }

    private void startRelease(Intent intent,int startId){
        String id=UUID.randomUUID().toString(),uri=intent.getStringExtra("uri"),name=intent.getStringExtra("name"),fileId=intent.getStringExtra("fileId"),
            revisionId=intent.getStringExtra("revisionId"),base=intent.getStringExtra("base"),node=intent.getStringExtra("node"),locationId=intent.getStringExtra("locationId");
        if(uri==null||fileId==null||fileId.isEmpty()||base==null||base.isEmpty()||locationId==null||locationId.isEmpty()){if(active.get()==0)stopSelf(startId);return;}
        startReleaseTask(id,uri,name,fileId,revisionId,base,node,locationId);
    }

    private void startReleaseTask(String id,String uri,String name,String fileId,String revisionId,String base,String node,String locationId){
        AtomicBoolean flag=new AtomicBoolean();cancelled.put(id,flag);
        if(!releaseRecord(id,uri,name==null?"":name,fileId,revisionId==null?"":revisionId,base,node==null?"":node,locationId,"检查中","","checking")){cancelled.remove(id);if(active.get()==0)stopSelf();return;}
        active.incrementAndGet();
        if(Build.VERSION.SDK_INT>=34)startForeground(4323,notification("安全释放 · "+name),ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4323,notification("安全释放 · "+name));
        work.execute(()->{release(id,uri,fileId,revisionId==null?"":revisionId,base,node==null?"":node,locationId,flag);cancelled.remove(id);if(active.decrementAndGet()<=0)stopSelf();});
    }

    private void retryRelease(String id){
        JSONObject item=findRelease(id);
        if(item==null||cancelled.containsKey(id))return;
        String phase=item.optString("phase");
        if("deleting".equals(phase)||"deleted".equals(phase)){startReleaseRecovery(item);return;}
        if("failed".equals(phase))startReleaseTask(id,item.optString("uri"),item.optString("name"),item.optString("fileId"),item.optString("revisionId"),item.optString("base"),item.optString("node"),item.optString("locationId"));
    }

    private void resumePendingReleases(){
        if(recoveryStarted)return;
        recoveryStarted=true;
        JSONArray items=releaseRecords(this);
        for(int i=0;i<items.length();i++){
            JSONObject item=items.optJSONObject(i);
            if(item!=null&&("deleting".equals(item.optString("phase"))||"deleted".equals(item.optString("phase"))))startReleaseRecovery(item);
        }
    }

    private void startReleaseRecovery(JSONObject item){
        String id=item.optString("id");
        if(id.isEmpty()||cancelled.containsKey(id))return;
        cancelled.put(id,new AtomicBoolean());
        active.incrementAndGet();
        if(Build.VERSION.SDK_INT>=34)startForeground(4323,notification("核实安全释放"),ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4323,notification("核实安全释放"));
        work.execute(()->{finishInterruptedRelease(item);cancelled.remove(id);if(active.decrementAndGet()<=0)stopSelf();});
    }

    private Notification notification(String text){Intent open=new Intent(this,MainActivity.class);PendingIntent click=PendingIntent.getActivity(this,3,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this,"original-upload").setSmallIcon(android.R.drawable.stat_sys_upload).setContentTitle("Myliba 原件入云").setContentText(text).setContentIntent(click).setOngoing(true).build();}

    private void upload(String id,String uri,String name,String fileId,String revisionId,String base,String node,String nodeId,String storageId,String resourceRef,long total,String modified,AtomicBoolean flag){
        RemoteClient client=null;
        try{
            client=new RemoteClient(base,getSharedPreferences("MainActivity",MODE_PRIVATE),this);
            client.json("GET","/api/session",null);
            if(!node.isEmpty()&&!node.equals(client.selectedNodeId))client.selectNode(node);
            String uploadId=randomHex();
            MessageDigest digest=MessageDigest.getInstance("SHA-256");
            String taskId="",locationId="";
            long offset=0;boolean sentFinal=false;
            try(InputStream input=getContentResolver().openInputStream(Uri.parse(uri))){
                if(input==null)throw new IOException("无法读取手机原件");
                byte[] buffer=new byte[CHUNK];
                while(!sentFinal){
                    if(flag.get())throw new IOException("已取消");
                    int filled=0;while(filled<CHUNK){int read=input.read(buffer,filled,CHUNK-filled);if(read<0)break;filled+=read;}
                    digest.update(buffer,0,filled);
                    long next=offset+filled;boolean last=next>=total;
                    StringBuilder path=new StringBuilder("/api/cloud/files/").append(RemoteClient.enc(fileId)).append("/original/chunk?uploadId=").append(uploadId)
                        .append("&offset=").append(offset).append("&total=").append(total);
                    if(last){
                        path.append("&final=1&revisionId=").append(RemoteClient.enc(revisionId)).append("&name=").append(RemoteClient.enc(name))
                            .append("&plainHash=").append(LocalIndexCrypto.hex(digest.digest())).append("&objectId=").append(RemoteClient.enc(fileId))
                            .append("&modified=").append(RemoteClient.enc(modified)).append("&nodeId=").append(RemoteClient.enc(nodeId))
                            .append("&storageId=").append(RemoteClient.enc(storageId)).append("&resourceRef=").append(RemoteClient.enc(resourceRef));
                    }
                    byte[] body=Arrays.copyOf(buffer,filled);
                    byte[] response=client.rtcRaw("PUT",path.toString(),body,1<<20);
                    offset=next;record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,offset,"上传中","","",modified);
                    if(last){JSONObject out=new JSONObject(new String(response,StandardCharsets.UTF_8));taskId=out.optString("taskId");locationId=out.optString("locationId");sentFinal=true;}
                    if(filled==0&&!last)break;
                }
            }
            if(offset!=total)throw new IOException("原件大小与索引版本不一致，已拒绝上传");
            if(taskId.isEmpty())throw new IOException("节点未返回上传任务");
            record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,offset,"验证中",taskId,locationId,modified);
            String state=waitForNodeTask(client,taskId,flag);
            if(!"committed".equals(state))throw new IOException("节点上传未成功："+state);
            record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,total,"已完成",taskId,locationId,modified);
        }catch(Exception ex){
            String status=flag.get()?"已取消":"失败";
            record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,0,status,"","",modified,ex.getMessage()==null?"上传中断":ex.getMessage());
        }finally{if(client!=null){try{if(!node.isEmpty())client.selectNode(node);}catch(Exception ignored){}client.close();}}
    }

    private String waitForNodeTask(RemoteClient client,String taskId,AtomicBoolean flag)throws Exception{
        long deadline=System.currentTimeMillis()+3600_000L;
        while(System.currentTimeMillis()<deadline){
            if(flag.get())throw new IOException("已取消");
            JSONObject task=client.json("GET","/api/cloud/transfers/"+RemoteClient.enc(taskId),null);
            String state=task.optString("state");
            if("committed".equals(state)||"retryableFailed".equals(state)||"blocked".equals(state)||"cancelled".equals(state)){
                if(!"committed".equals(state))throw new IOException(task.optString("detail",state));
                return state;
            }
            Thread.sleep(1000);
        }
        throw new IOException("节点验证超时");
    }

    private void release(String id,String uri,String fileId,String revisionId,String base,String node,String locationId,AtomicBoolean flag){
        RemoteClient client=null;
        String phase="checking";
        try{
            client=new RemoteClient(base,getSharedPreferences("MainActivity",MODE_PRIVATE),this);
            client.json("GET","/api/session",null);
            if(!node.isEmpty()&&!node.equals(client.selectedNodeId))client.selectNode(node);
            JSONObject check=client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/release-check",new JSONObject().put("locationId",locationId).put("revisionId",revisionId));
            if(check.optBoolean("released",false))throw new IOException("查询节点已移除该位置，请刷新本机索引后核实原件");
            if(!check.optBoolean("eligible",false))throw new IOException(check.optString("reason","不满足安全释放条件"));
            String credential=check.optString("credential"),plainHash=check.optString("plainHash");
            if(credential.isEmpty())throw new IOException("节点未签发释放凭据");
            String currentHash=hashUri(uri,flag);
            if(!currentHash.equals(plainHash))throw new IOException("原件在检查后发生变化，已取消删除，请重新收录新版本");
            if(flag.get())throw new IOException("已取消");
            if(!releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"删除本机原件","","deleting"))throw new IOException("无法持久记录释放进度，已取消删除");
            phase="deleting";
            boolean deleted=DocumentsContract.deleteDocument(getContentResolver(),Uri.parse(uri));
            if(!deleted){phase="failed";throw new IOException("系统拒绝删除本机原件，位置已保留");}
            phase="deleted";
            if(!releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"移除本机位置","","deleted"))throw new IOException("原件已删除，但无法记录收尾进度");
            finishRemoteRelease(client,fileId,revisionId,locationId);
            releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"已释放","","completed");
        }catch(Exception ex){
            boolean pending="deleting".equals(phase)||"deleted".equals(phase);
            releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,pending?"待核实":flag.get()?"已取消":"失败",ex.getMessage()==null?"释放中断":ex.getMessage(),pending?phase:"failed");
        }finally{if(client!=null){try{if(!node.isEmpty())client.selectNode(node);}catch(Exception ignored){}client.close();}}
    }

    private void finishInterruptedRelease(JSONObject item){
        String id=item.optString("id"),uri=item.optString("uri"),fileId=item.optString("fileId"),revisionId=item.optString("revisionId"),base=item.optString("base"),node=item.optString("node"),locationId=item.optString("locationId");
        String phase=item.optString("phase");
        RemoteClient client=null;
        try{
            int state=documentState(uri);
            if(state==1){releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"失败","手机原件仍存在；请重新执行安全释放检查","failed");return;}
            if(state!=0)throw new IOException("无法确认 SAF 原件是否已删除；请恢复目录授权或存储卷后重试收尾");
            phase="deleted";
            if(!releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"移除本机位置","","deleted"))throw new IOException("无法持久记录收尾进度");
            client=new RemoteClient(base,getSharedPreferences("MainActivity",MODE_PRIVATE),this);
            client.json("GET","/api/session",null);
            if(!node.isEmpty()&&!node.equals(client.selectedNodeId))client.selectNode(node);
            finishRemoteRelease(client,fileId,revisionId,locationId);
            releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"已释放","","completed");
        }catch(Exception ex){releaseRecord(id,uri,"",fileId,revisionId,base,node,locationId,"待核实",ex.getMessage()==null?"释放收尾中断":ex.getMessage(),phase);
        }finally{if(client!=null){try{if(!node.isEmpty())client.selectNode(node);}catch(Exception ignored){}client.close();}}
    }

    private void finishRemoteRelease(RemoteClient client,String fileId,String revisionId,String locationId)throws Exception{
        JSONObject check=client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/release-check",new JSONObject().put("locationId",locationId).put("revisionId",revisionId));
        if(check.optBoolean("released",false))return;
        if(!check.optBoolean("eligible",false))throw new IOException(check.optString("reason","查询节点暂不能完成释放收尾"));
        String credential=check.optString("credential");
        if(credential.isEmpty())throw new IOException("查询节点未签发释放凭据");
        JSONObject result=client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/release",new JSONObject().put("locationId",locationId).put("credential",credential));
        if(!result.optBoolean("released",false))throw new IOException("查询节点未确认位置移除");
    }

    // Only a reachable, authorized tree can prove that a child is absent. A
    // revoked grant or an unavailable volume must never be treated as deletion.
    private int documentState(String raw){
        try{
            Uri document=Uri.parse(raw);
            if(!DocumentsContract.isTreeUri(document))return -1;
            String rootId=DocumentsContract.getTreeDocumentId(document);
            Uri tree=DocumentsContract.buildTreeDocumentUri(document.getAuthority(),rootId);
            boolean authorized=false;
            for(UriPermission grant:getContentResolver().getPersistedUriPermissions())if(tree.equals(grant.getUri())&&grant.isReadPermission()){authorized=true;break;}
            if(!authorized)return -1;
            Uri root=DocumentsContract.buildDocumentUriUsingTree(tree,rootId);
            try(Cursor cursor=getContentResolver().query(root,new String[]{DocumentsContract.Document.COLUMN_DOCUMENT_ID},null,null,null)){
                if(cursor==null||!cursor.moveToFirst())return -1;
            }
            String targetId=DocumentsContract.getDocumentId(document);
            if(rootId.equals(targetId))return 1;
            try(Cursor cursor=getContentResolver().query(document,new String[]{DocumentsContract.Document.COLUMN_DOCUMENT_ID},null,null,null)){
                if(cursor!=null&&cursor.moveToFirst())return 1;
            }catch(Exception ignored){}
            // Some providers return null or throw for a deleted document. Walk
            // the readable tree before concluding that it is really absent.
            ArrayDeque<String> pending=new ArrayDeque<>();Set<String> visited=new HashSet<>();pending.add(rootId);
            String[] columns={DocumentsContract.Document.COLUMN_DOCUMENT_ID,DocumentsContract.Document.COLUMN_MIME_TYPE};
            while(!pending.isEmpty()){
                String parentId=pending.removeFirst();if(!visited.add(parentId))continue;
                Uri children=DocumentsContract.buildChildDocumentsUriUsingTree(tree,parentId);
                try(Cursor cursor=getContentResolver().query(children,columns,null,null,null)){
                    if(cursor==null)return -1;
                    while(cursor.moveToNext()){
                        String childId=cursor.getString(0);
                        if(targetId.equals(childId))return 1;
                        if(DocumentsContract.Document.MIME_TYPE_DIR.equals(cursor.getString(1)))pending.add(childId);
                    }
                }
            }
            return 0;
        }catch(Exception ex){return -1;}
    }

    private String hashUri(String uri,AtomicBoolean flag)throws Exception{
        MessageDigest digest=MessageDigest.getInstance("SHA-256");
        try(InputStream input=getContentResolver().openInputStream(Uri.parse(uri))){
            if(input==null)throw new IOException("无法读取手机原件以复核版本");
            byte[] buffer=new byte[64<<10];int read;while((read=input.read(buffer))!=-1){if(flag.get())throw new IOException("已取消");digest.update(buffer,0,read);}
        }
        return LocalIndexCrypto.hex(digest.digest());
    }

    private static String randomHex(){byte[] bytes=new byte[16];new SecureRandom().nextBytes(bytes);return LocalIndexCrypto.hex(bytes);}

    static JSONArray records(Context context){synchronized(RECORDS){try{return new JSONArray(context.getSharedPreferences(PREFS,MODE_PRIVATE).getString("items","[]"));}catch(Exception ex){return new JSONArray();}}}
    static JSONArray releaseRecords(Context context){synchronized(RECORDS){try{return new JSONArray(context.getSharedPreferences(PREFS,MODE_PRIVATE).getString("releases","[]"));}catch(Exception ex){return new JSONArray();}}}
    static boolean hasInterruptedRelease(Context context){JSONArray items=releaseRecords(context);for(int i=0;i<items.length();i++){JSONObject item=items.optJSONObject(i);if(item==null)continue;String phase=item.optString("phase"),status=item.optString("status");if("deleting".equals(phase)||"deleted".equals(phase)||(phase.isEmpty()&&("删除本机原件".equals(status)||"移除本机位置".equals(status)||"失败".equals(status))))return true;}return false;}
    private JSONObject find(String id){if(id==null)return null;JSONArray items=records(this);for(int i=0;i<items.length();i++){JSONObject item=items.optJSONObject(i);if(item!=null&&id.equals(item.optString("id")))return item;}return null;}
    private JSONObject findRelease(String id){if(id==null)return null;JSONArray items=releaseRecords(this);for(int i=0;i<items.length();i++){JSONObject item=items.optJSONObject(i);if(item!=null&&id.equals(item.optString("id")))return item;}return null;}
    private void recoverInterrupted(){synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean changed=false;for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;String state=item.optString("status");if("上传中".equals(state)||"等待上传".equals(state)||"验证中".equals(state)){try{item.put("status","失败").put("error","上传中断，请重试");changed=true;}catch(JSONException ignored){}}next.put(item);}if(changed)getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();
        old=releaseRecords(this);next=new JSONArray();changed=false;
        for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;String phase=item.optString("phase"),status=item.optString("status");
            try{
                if(phase.isEmpty()){
                    phase="已释放".equals(status)?"completed":"检查中".equals(status)?"failed":"deleting";
                    item.put("phase",phase);changed=true;
                }
                if("checking".equals(phase)){item.put("phase","failed").put("status","失败").put("error","释放检查中断，请重试");changed=true;}
            }catch(JSONException ignored){}
            next.put(item);
        }
        if(changed)getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("releases",next.toString()).commit();}}

    private void record(String id,String uri,String name,String fileId,String revisionId,String base,String node,String nodeId,String storageId,String resourceRef,long total,long uploaded,String status,String taskId,String locationId,String modified){record(id,uri,name,fileId,revisionId,base,node,nodeId,storageId,resourceRef,total,uploaded,status,taskId,locationId,modified,null);}
    private void record(String id,String uri,String name,String fileId,String revisionId,String base,String node,String nodeId,String storageId,String resourceRef,long total,long uploaded,String status,String taskId,String locationId,String modified,String error){
        synchronized(RECORDS){JSONArray old=records(this),next=new JSONArray();boolean found=false;
            JSONObject entry=RemoteClient.object("id",id,"uri",uri,"name",name,"fileId",fileId,"revisionId",revisionId,"base",base,"node",node,"nodeId",nodeId,"storageId",storageId,"resourceRef",resourceRef,"total",total,"uploaded",uploaded,"status",status,"taskId",taskId,"locationId",locationId,"modified",modified,"error",error==null?"":error);
            for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;if(id.equals(item.optString("id"))){found=true;next.put(entry);}else next.put(item);}
            if(!found)next.put(entry);
            getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}
    }
    private boolean releaseRecord(String id,String uri,String name,String fileId,String revisionId,String base,String node,String locationId,String status,String error,String phase){
        synchronized(RECORDS){JSONArray old=releaseRecords(this),next=new JSONArray();boolean found=false;
            for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;if(id.equals(item.optString("id"))){found=true;if(name==null||name.isEmpty())name=item.optString("name");} }
            JSONObject entry=RemoteClient.object("id",id,"uri",uri,"name",name,"fileId",fileId,"revisionId",revisionId,"base",base,"node",node,"locationId",locationId,"status",status,"error",error==null?"":error,"phase",phase);
            for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item==null)continue;if(id.equals(item.optString("id")))next.put(entry);else next.put(item);}
            if(!found)next.put(entry);
            return getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("releases",next.toString()).commit();}
    }
    static void remove(Context context,String id){synchronized(RECORDS){JSONArray old=records(context),next=new JSONArray();for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item!=null&&!id.equals(item.optString("id")))next.put(item);}context.getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("items",next.toString()).apply();}}
    @Override public IBinder onBind(Intent intent){return null;}
    @Override public void onDestroy(){work.shutdownNow();super.onDestroy();}
}
