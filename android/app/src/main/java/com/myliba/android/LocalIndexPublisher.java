package com.myliba.android;

import android.content.Context;
import android.content.ContentValues;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.util.AtomicFile;
import org.json.JSONArray;
import org.json.JSONObject;
import java.io.*;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.*;
import java.util.zip.GZIPInputStream;

/** Resource-node publisher; never enabled by the opaque storage-node service. */
final class LocalIndexPublisher {
    private static final Object PUBLISH_LOCK=new Object();
    static volatile String status="索引同步未启用";
    static volatile int pendingCount;
    static volatile int folderConflictCount;
    static volatile int renameConflictCount;
    static volatile int appliedCount;
    static volatile int receiptCount;
    static volatile int quarantinedCount;
    private final Context context;

    private static final class Pending {
        final String id,token;
        final JSONObject index;
        Pending(String id,String token,JSONObject index){this.id=id;this.token=token;this.index=index;}
    }

    private static final class PendingOperation {
        final String id,author,kind,payload,createdAt;
        final long counter;
        PendingOperation(String id,String author,long counter,String kind,String payload,String createdAt){this.id=id;this.author=author;this.counter=counter;this.kind=kind;this.payload=payload;this.createdAt=createdAt;}
    }

    LocalIndexPublisher(Context context){this.context=context;}

    void sync(String hub,String token,String node)throws Exception{
        synchronized(PUBLISH_LOCK){
            if(Thread.currentThread().isInterrupted())throw new InterruptedException();
            syncLocked(hub,token,node);
        }
    }

    private void syncLocked(String hub,String token,String node)throws Exception{
        SharedPreferences settings=context.getSharedPreferences(ResourceNodeService.PREFS,Context.MODE_PRIVATE);
        if(!settings.getBoolean("index_enabled",false)){pendingCount=0;status="索引同步未启用";return;}
        byte[] key=LocalIndexCrypto.key(CloudSecretStore.read(settings,"index_key"));
        requireFormatFour(hub,token);
        // Read membership and operations before publishing. A retired device
        // cannot emit another record under its old author epoch.
        folderConflictCount=applyRemoteOperations(hub,token,key);
        long authorEpoch=0;
        try(LocalLibrary membership=new LocalLibrary(context)){
            if(membership.retiredMember(node)){status="此资源节点已退役。完整同步后请在设置中重新加入，并使用新作者 epoch 发布。";return;}
            authorEpoch=membership.memberEpoch(node);
        }
        String scope=LocalIndexCrypto.hash((hub+"|"+node+"|"+LocalIndexCrypto.hash(key)).getBytes(StandardCharsets.UTF_8));
        File directory=new File(context.getFilesDir(),"published-index/"+scope);
        if(!directory.isDirectory()&&!directory.mkdirs())throw new IOException("无法保存索引同步状态");

        Map<String,Pending> pending=new TreeMap<>();
        List<PendingOperation> operations=new ArrayList<>();
        Set<String> liveIDs=new HashSet<>();
        int liveCount;
        File formatFourAck=new File(directory,"format4.ack");
        try(LocalLibrary library=new LocalLibrary(context)){
            SQLiteDatabase db=library.getWritableDatabase();
            if(!formatFourAck.isFile())db.execSQL("INSERT OR IGNORE INTO index_outbox(file_id,item_uri,token) SELECT COALESCE(NULLIF(logical_file_id,''),file_id),uri,lower(hex(randomblob(16))) FROM items");
            db.beginTransactionNonExclusive();
            try{
                try(Cursor all=db.rawQuery("SELECT COALESCE(NULLIF(logical_file_id,''),file_id) FROM items",null)){while(all.moveToNext())liveIDs.add(all.getString(0));}
                try(Cursor count=db.rawQuery("SELECT COUNT(*) FROM items",null)){count.moveToFirst();liveCount=count.getInt(0);}
                try(Cursor rows=db.rawQuery("SELECT q.file_id,q.token,i.uri,i.root_uri,COALESCE(n.current_name,i.name),i.mime,i.size,i.modified,i.revision_id,COALESCE(f.id,''),COALESCE(f.name,'') FROM index_outbox q LEFT JOIN items i ON COALESCE(NULLIF(i.logical_file_id,''),i.file_id)=q.file_id LEFT JOIN folders f ON f.id=i.folder_id LEFT JOIN library_file_name_state n ON n.file_id=q.file_id ORDER BY q.file_id,i.uri",null)){
                    while(rows.moveToNext()){
                        String id=rows.getString(0),queueToken=rows.getString(1);
                        if(rows.isNull(2)){pending.put(id,new Pending(id,queueToken,null));continue;}
                        String uri=rows.getString(2);
                        JSONArray tags=new JSONArray();
                        try(Cursor tagRows=db.rawQuery("SELECT name FROM tags WHERE item_uri=? ORDER BY name",new String[]{uri})){while(tagRows.moveToNext())tags.put(tagRows.getString(0));}
                        JSONObject location=new JSONObject().put("nodeId",node).put("storageId",rows.getString(3)).put("resourceRef",NodeResourceApi.id(uri));
                        JSONObject index=new JSONObject().put("fileId",id).put("revisionId",rows.getString(8)).put("name",rows.getString(4))
                            .put("kind",LibraryStore.kind(rows.getString(4),rows.getString(5))).put("modified",Instant.ofEpochMilli(rows.getLong(7)).toString())
                            .put("tags",tags).put("folderId",rows.getString(9)).put("folderName",rows.getString(10)).put("organizationKnown",true)
                            .put("original",new JSONObject().put("size",rows.getLong(6)).put("local",new JSONArray().put(location)))
                            .put("assets",new JSONArray()).put("contentSize",rows.getLong(6));
						Pending previous=pending.get(id);if(previous!=null&&previous.index!=null){previous.index.getJSONObject("original").getJSONArray("local").put(location);continue;}
                        pending.put(id,new Pending(id,queueToken,index));
                    }
                }
                try(Cursor rows=db.rawQuery("SELECT op_id,author_id,counter,kind,payload_json,created_at FROM library_operations WHERE state='pending' ORDER BY author_id,counter",null)){
                    while(rows.moveToNext())operations.add(new PendingOperation(rows.getString(0),rows.getString(1),rows.getLong(2),rows.getString(3),rows.getString(4),rows.getString(5)));
                }
                db.setTransactionSuccessful();
            }finally{db.endTransaction();}
        }

        // Records made by the former URI-derived identity have no database row.
        // Replace them once with tombstones so migration cannot leave duplicates.
        File[] stored=directory.listFiles((dir,name)->name.endsWith(".record")||name.endsWith(".record.bak"));
        Set<File> records=new HashSet<>();
        if(stored!=null)for(File file:stored)records.add(new File(directory,file.getName().replaceFirst("\\.bak$","")));
        for(File record:records){
            String name=record.getName();
            if(!name.endsWith(".record"))continue;
            String id=name.substring(0,name.length()-7);
            if(!safeID(id)||liveIDs.contains(id)||pending.containsKey(id))continue;
            pending.put(id,new Pending(id,"",null));
        }

        int sent=0,discarded=0,failed=0;
        String firstFailure="";
        for(Pending item:pending.values()){
            if(Thread.currentThread().isInterrupted())throw new InterruptedException();
            try{
                JSONObject index=item.index;
                if(index==null){
                    index=previousIndex(directory,key,item.id);
                    if(index==null){ackOutbox(item.id,item.token);discarded++;continue;}
                    index.put("deleted",true);
                    index.getJSONObject("original").put("local",new JSONArray());
                }
                String manifest=new JSONObject().put("indexOnly",true).put("localIndex",index).toString();
                String digest=LocalIndexCrypto.hash(("format4\n"+manifest).getBytes(StandardCharsets.UTF_8));
                File ack=new File(directory,item.id+".ack"),record=new File(directory,item.id+".record"),fingerprint=new File(directory,item.id+".digest");
                if(!digest.equals(read(ack))){
                    if(!digest.equals(read(fingerprint))||!record.isFile()){
                        String old=read(record),stamp=Instant.now().toString();
                        if(!old.isEmpty()){
                            try{Instant previousTime=Instant.parse(new JSONObject(old).getString("createdAt"));
                                if(!Instant.parse(stamp).isAfter(previousTime))stamp=previousTime.plusNanos(1).toString();}
                            catch(Exception ignored){/* A damaged pending record is rebuilt from the durable outbox. */}
                        }
                        write(record,LocalIndexCrypto.seal(key,item.id,index.getString("revisionId"),node,authorEpoch,stamp,manifest));
                        write(fingerprint,digest);
                    }
                    upload(hub,token,read(record).getBytes(StandardCharsets.UTF_8));
                    write(ack,digest);sent++;
                }
                ackOutbox(item.id,item.token);
            }catch(InterruptedException ex){throw ex;
            }catch(Exception ex){failed++;if(firstFailure.isEmpty())firstFailure=ex.getMessage()==null?ex.getClass().getSimpleName():ex.getMessage();}
        }
        if(failed==0&&!formatFourAck.isFile())write(formatFourAck,"1");
        File operationDirectory=new File(directory,"operations");
        if(!operationDirectory.isDirectory()&&!operationDirectory.mkdirs())throw new IOException("无法保存整理操作同步状态");
        for(PendingOperation operation:operations){
            if(Thread.currentThread().isInterrupted())throw new InterruptedException();
            try{
                File record=new File(operationDirectory,operation.id+".record");
                if(needsFormatFourRecord(record)){
                    JSONObject envelope=new JSONObject().put("opId",operation.id).put("authorId",operation.author).put("counter",operation.counter).put("kind",operation.kind).put("payload",new JSONObject(operation.payload));
                    String manifest=new JSONObject().put("operation",envelope).toString();
                    write(record,LocalIndexCrypto.seal(key,"operation-"+operation.id,operation.id,node,authorEpoch,Instant.now().toString(),manifest));
                }
                upload(hub,token,read(record).getBytes(StandardCharsets.UTF_8));
                acknowledgeOperation(operation.id);sent++;
            }catch(InterruptedException ex){throw ex;
            }catch(Exception ex){failed++;if(firstFailure.isEmpty())firstFailure=ex.getMessage()==null?ex.getClass().getSimpleName():ex.getMessage();}
        }
        String receiveError="";
        try{publishReceipt(directory,hub,token,key,node,authorEpoch);}catch(Exception ex){receiveError=" · 应用回执未同步："+(ex.getMessage()==null?ex.getClass().getSimpleName():ex.getMessage());}
        pendingCount=outboxCount()+operationOutboxCount();
        status="索引与整理操作已交接 Hub · "+liveCount+" 个文件 · 本轮更新 "+sent+" 项 · 待发布 "+pendingCount+" 项 · 本端已应用 "+appliedCount+" 项 · 其他节点回执 "+receiptCount+" 份"+(folderConflictCount==0?"":" · 归组待处理 "+folderConflictCount+" 项")+(renameConflictCount==0?"":" · 改名待处理 "+renameConflictCount+" 项")+(quarantinedCount==0?"":" · "+quarantinedCount+" 项操作需升级客户端")+receiveError+(failed==0?"":" · "+failed+" 项失败，将重试："+firstFailure)+(discarded==0?"":" · 忽略 "+discarded+" 项未发布删除");
    }

    private void publishReceipt(File directory,String hub,String token,byte[] key,String node,long authorEpoch)throws Exception{
        JSONObject frontiers;
        try(LocalLibrary local=new LocalLibrary(context)){frontiers=local.appliedFrontiers();}
        if(frontiers.length()==0)return;
        String manifest=new JSONObject().put("receipt",new JSONObject().put("nodeId",node).put("frontiers",frontiers)).toString();
        String digest=LocalIndexCrypto.hash(("format4\n"+manifest).getBytes(StandardCharsets.UTF_8));
        File ack=new File(directory,"receipt.ack"),record=new File(directory,"receipt.record"),fingerprint=new File(directory,"receipt.digest");
        if(digest.equals(read(ack)))return;
        if(!digest.equals(read(fingerprint))||!record.isFile()){
            write(record,LocalIndexCrypto.seal(key,"library-receipt-"+node,java.util.UUID.randomUUID().toString(),node,authorEpoch,Instant.now().toString(),manifest));
            write(fingerprint,digest);
        }
        upload(hub,token,read(record).getBytes(StandardCharsets.UTF_8));
        write(ack,digest);
    }

    private JSONObject previousIndex(File directory,byte[] key,String id)throws Exception{
        String raw=read(new File(directory,id+".record"));
        if(raw.isEmpty())return null;
        JSONObject record=new JSONObject(raw);
        String manifest=LocalIndexCrypto.open(key,record.optInt("format",3),record.optBoolean("keepHistory",false),id,record.getString("revisionId"),record.getString("authorId"),record.getString("createdAt"),record.getString("nonce"),record.getString("sealed"));
        return new JSONObject(manifest).getJSONObject("localIndex");
    }

    private void ackOutbox(String id,String token){
        if(token.isEmpty())return;
        try(LocalLibrary library=new LocalLibrary(context)){
            library.getWritableDatabase().delete("index_outbox","file_id=? AND token=?",new String[]{id,token});
        }
    }

    private int outboxCount(){
        try(LocalLibrary library=new LocalLibrary(context);Cursor rows=library.getReadableDatabase().rawQuery("SELECT COUNT(*) FROM index_outbox",null)){
            return rows.moveToFirst()?rows.getInt(0):0;
        }
    }

    private int operationOutboxCount(){
        try(LocalLibrary library=new LocalLibrary(context);Cursor rows=library.getReadableDatabase().rawQuery("SELECT COUNT(*) FROM library_operations WHERE state='pending'",null)){
            return rows.moveToFirst()?rows.getInt(0):0;
        }
    }

    private void acknowledgeOperation(String id){
        try(LocalLibrary library=new LocalLibrary(context)){
            ContentValues values=new ContentValues();values.put("state","sent");
            library.getWritableDatabase().update("library_operations",values,"op_id=? AND state='pending'",new String[]{id});
        }
    }

    private int applyRemoteOperations(String hub,String token,byte[] key)throws Exception{
        JSONArray records;
        HttpURLConnection listing=(HttpURLConnection)new URL(hub+"/cloud/catalog").openConnection();
        try{
            listing.setRequestMethod("GET");listing.setConnectTimeout(10000);listing.setReadTimeout(30000);listing.setRequestProperty("Authorization","Bearer "+token);
            if(listing.getResponseCode()!=200)throw new IOException("整理操作目录 HTTP "+listing.getResponseCode());
            records=new JSONObject(new String(readResponse(listing,16<<20),StandardCharsets.UTF_8)).optJSONArray("records");
        }finally{listing.disconnect();}
        if(records==null)return 0;
        List<LocalLibrary.RemoteOperation> operations=new ArrayList<>();List<LocalLibrary.RemoteReceipt> receipts=new ArrayList<>();
        String library=LocalIndexCrypto.hash(key);
        for(int index=0;index<records.length();index++){
            String id=records.optString(index,"");if(!id.matches("[0-9a-f]{64}"))continue;
            try{
                HttpURLConnection get=(HttpURLConnection)new URL(hub+"/cloud/catalog/"+id).openConnection();byte[] raw;
                try{get.setRequestMethod("GET");get.setConnectTimeout(10000);get.setReadTimeout(30000);get.setRequestProperty("Authorization","Bearer "+token);if(get.getResponseCode()!=200)continue;raw=readResponse(get,1<<20);}finally{get.disconnect();}
                JSONObject record=new JSONObject(new String(raw,StandardCharsets.UTF_8));int format=record.optInt("format",0);
                if(format<3||!library.equals(record.optString("libraryId")))continue;
                String manifest=LocalIndexCrypto.open(key,format,record.optBoolean("keepHistory",false),record.optString("objectId"),record.optString("revisionId"),record.optString("authorId"),record.optString("createdAt"),record.optString("nonce"),record.optString("sealed"));
                JSONObject opened=new JSONObject(manifest);JSONObject receipt=opened.optJSONObject("receipt");
                if(receipt!=null){String receiptNode=receipt.optString("nodeId");JSONObject frontiers=receipt.optJSONObject("frontiers");if(receiptNode.equals(record.optString("authorId"))&&frontiers!=null&&frontiers.length()>0)receipts.add(new LocalLibrary.RemoteReceipt(receiptNode,frontiers.toString(),record.optString("createdAt")));continue;}
                JSONObject snapshot=opened.optJSONObject("snapshot");
                if(snapshot!=null){
                    String author=snapshot.optString("authorId"),object=record.optString("objectId");long frontier=snapshot.optLong("frontier",0),from=snapshot.optLong("fromCounter",1);
                    if(author.isEmpty()||!object.equals("library-snapshot/"+author)||from<1||frontier<from||frontier>100000)continue;
                    if(snapshot.optInt("schemaVersion")!=1){operations.add(new LocalLibrary.RemoteOperation(record.optString("revisionId"),author,frontier,"snapshot.unsupported","{}"));continue;}
                    byte[] compressed=Base64.getDecoder().decode(snapshot.optString("operationsB64"));
                    byte[] expanded;try(GZIPInputStream zip=new GZIPInputStream(new ByteArrayInputStream(compressed));ByteArrayOutputStream out=new ByteArrayOutputStream()){
                        byte[] buffer=new byte[8192];int length;while((length=zip.read(buffer))!=-1){if(out.size()+length>64<<20)throw new IOException("snapshot too large");out.write(buffer,0,length);}expanded=out.toByteArray();
                    }
                    if(!LocalIndexCrypto.hash(expanded).equals(snapshot.optString("checksum")))continue;
                    JSONArray packed=new JSONArray(new String(expanded,StandardCharsets.UTF_8));if(packed.length()!=frontier-from+1)continue;
                    List<LocalLibrary.RemoteOperation> decoded=new ArrayList<>();boolean valid=true;
                    for(int at=0;at<packed.length();at++){JSONObject item=packed.optJSONObject(at);if(item==null||!author.equals(item.optString("authorId"))||item.optLong("counter")!=from+at||item.optJSONObject("payload")==null){valid=false;break;}
                        decoded.add(new LocalLibrary.RemoteOperation(item.optString("opId"),author,from+at,item.optString("kind"),item.optJSONObject("payload").toString()));}
                    if(valid)operations.addAll(decoded);
                    continue;
                }
                JSONObject operation=opened.optJSONObject("operation");if(operation==null)continue;
                JSONObject payload=operation.optJSONObject("payload");
                String opId=operation.optString("opId"),author=operation.optString("authorId"),kind=operation.optString("kind");long counter=operation.optLong("counter",0);
                if(payload==null||opId.isEmpty()||author.isEmpty()||kind.isEmpty()||counter<1||!opId.equals(record.optString("revisionId")))continue;
                String objectId=record.optString("objectId");
                if(!objectId.equals("operation-"+opId)&&!objectId.equals("library-operation/"+author+"/"+counter))continue;
                operations.add(new LocalLibrary.RemoteOperation(opId,author,counter,kind,payload.toString()));
            }catch(Exception ignored){/* Other libraries and malformed catalog entries are not actionable here. */}
        }
        try(LocalLibrary local=new LocalLibrary(context)){if(!operations.isEmpty())local.applyRemoteOperations(operations);if(!receipts.isEmpty())local.applyReceipts(receipts);renameConflictCount=local.renameConflictCount();appliedCount=local.appliedFrontierCount();receiptCount=local.remoteReceiptCount();quarantinedCount=local.quarantinedOperationCount();return local.folderConflictCount();}
    }

    private static byte[] readResponse(HttpURLConnection connection,int limit)throws IOException{
        try(InputStream input=connection.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream()){
            byte[] buffer=new byte[8192];int count;while((count=input.read(buffer))!=-1){out.write(buffer,0,count);if(out.size()>limit)throw new IOException("catalog response too large");}return out.toByteArray();
        }
    }

    private static void upload(String hub,String token,byte[] record)throws Exception{
        if(record.length>1<<20)throw new IOException("单个文件索引过大");
        HttpURLConnection connection=(HttpURLConnection)new URL(hub+"/cloud/catalog/"+LocalIndexCrypto.hash(record)).openConnection();
        try{
            connection.setRequestMethod("PUT");connection.setInstanceFollowRedirects(false);connection.setConnectTimeout(10000);connection.setReadTimeout(30000);
            connection.setRequestProperty("Authorization","Bearer "+token);connection.setRequestProperty("Content-Type","application/json");
            connection.setDoOutput(true);connection.setFixedLengthStreamingMode(record.length);
            try(OutputStream out=connection.getOutputStream()){out.write(record);}
            if(connection.getResponseCode()!=204)throw new IOException("索引发布 HTTP "+connection.getResponseCode());
        }finally{connection.disconnect();}
    }
    private static void requireFormatFour(String hub,String token)throws Exception{
        HttpURLConnection connection=(HttpURLConnection)new URL(hub+"/cloud/capabilities").openConnection();
        try{
            connection.setRequestMethod("GET");connection.setInstanceFollowRedirects(false);connection.setConnectTimeout(10000);connection.setReadTimeout(10000);
            connection.setRequestProperty("Authorization","Bearer "+token);
            if(connection.getResponseCode()!=200)throw new IOException("Hub 不支持新版目录协议，请更新 Hub（HTTP "+connection.getResponseCode()+"）");
            try(InputStream input=connection.getInputStream()){
                ByteArrayOutputStream output=new ByteArrayOutputStream();byte[] buffer=new byte[1024];int count;
                while((count=input.read(buffer))!=-1){if(output.size()+count>4096)throw new IOException("Hub 目录能力响应过大");output.write(buffer,0,count);}
                if(new JSONObject(new String(output.toByteArray(),StandardCharsets.UTF_8)).optInt("maxCatalogFormat",0)<4)throw new IOException("Hub 不支持 format 4 目录记录，请更新 Hub");
            }
        }finally{connection.disconnect();}
    }

    private static boolean safeID(String value){return value.length()>=16&&value.length()<=128&&value.matches("[A-Za-z0-9-]+");}
    private static boolean needsFormatFourRecord(File record){try{return !record.isFile()||new JSONObject(read(record)).optInt("format",3)<4;}catch(Exception ex){return true;}}
    private static String read(File file)throws IOException{
        if(!file.exists()&&!new File(file.getPath()+".bak").exists())return "";
        return new String(new AtomicFile(file).readFully(),StandardCharsets.UTF_8);
    }
    private static void write(File file,String value)throws IOException{
        AtomicFile atomic=new AtomicFile(file);FileOutputStream stream=atomic.startWrite();
        try{stream.write(value.getBytes(StandardCharsets.UTF_8));atomic.finishWrite(stream);}catch(IOException ex){atomic.failWrite(stream);throw ex;}
    }
}
