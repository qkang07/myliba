package com.myliba.android;

import android.content.Context;
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

/** Resource-node publisher; never enabled by the opaque storage-node service. */
final class LocalIndexPublisher {
    private static final Object PUBLISH_LOCK=new Object();
    static volatile String status="索引同步未启用";
    static volatile int pendingCount;
    private final Context context;

    private static final class Pending {
        final String id,token;
        final JSONObject index;
        Pending(String id,String token,JSONObject index){this.id=id;this.token=token;this.index=index;}
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
        String scope=LocalIndexCrypto.hash((hub+"|"+node+"|"+LocalIndexCrypto.hash(key)).getBytes(StandardCharsets.UTF_8));
        File directory=new File(context.getFilesDir(),"published-index/"+scope);
        if(!directory.isDirectory()&&!directory.mkdirs())throw new IOException("无法保存索引同步状态");

        Map<String,Pending> pending=new TreeMap<>();
        Set<String> liveIDs=new HashSet<>();
        int liveCount;
        try(LocalLibrary library=new LocalLibrary(context)){
            SQLiteDatabase db=library.getReadableDatabase();
            db.beginTransactionNonExclusive();
            try{
                try(Cursor all=db.rawQuery("SELECT file_id FROM items",null)){while(all.moveToNext())liveIDs.add(all.getString(0));}
                try(Cursor count=db.rawQuery("SELECT COUNT(*) FROM items",null)){count.moveToFirst();liveCount=count.getInt(0);}
                try(Cursor rows=db.rawQuery("SELECT q.file_id,q.token,i.uri,i.root_uri,i.name,i.mime,i.size,i.modified,i.revision_id,COALESCE(f.id,''),COALESCE(f.name,'') FROM index_outbox q LEFT JOIN items i ON i.file_id=q.file_id LEFT JOIN folders f ON f.id=i.folder_id ORDER BY q.file_id",null)){
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
                        pending.put(id,new Pending(id,queueToken,index));
                    }
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
                String digest=LocalIndexCrypto.hash(manifest.getBytes(StandardCharsets.UTF_8));
                File ack=new File(directory,item.id+".ack"),record=new File(directory,item.id+".record"),fingerprint=new File(directory,item.id+".digest");
                if(!digest.equals(read(ack))){
                    if(!digest.equals(read(fingerprint))||!record.isFile()){
                        String old=read(record),stamp=Instant.now().toString();
                        if(!old.isEmpty()){
                            Instant previousTime=Instant.parse(new JSONObject(old).getString("createdAt"));
                            if(!Instant.parse(stamp).isAfter(previousTime))stamp=previousTime.plusNanos(1).toString();
                        }
                        write(record,LocalIndexCrypto.seal(key,item.id,index.getString("revisionId"),node,stamp,manifest));
                        write(fingerprint,digest);
                    }
                    upload(hub,token,read(record).getBytes(StandardCharsets.UTF_8));
                    write(ack,digest);sent++;
                }
                ackOutbox(item.id,item.token);
            }catch(InterruptedException ex){throw ex;
            }catch(Exception ex){failed++;if(firstFailure.isEmpty())firstFailure=ex.getMessage()==null?ex.getClass().getSimpleName():ex.getMessage();}
        }
        pendingCount=outboxCount();
        status="索引已同步 · "+liveCount+" 个文件 · 本轮更新 "+sent+" 项 · 待发布 "+pendingCount+" 项"+(failed==0?"":" · "+failed+" 项失败，将重试："+firstFailure)+(discarded==0?"":" · 忽略 "+discarded+" 项未发布删除");
    }

    private JSONObject previousIndex(File directory,byte[] key,String id)throws Exception{
        String raw=read(new File(directory,id+".record"));
        if(raw.isEmpty())return null;
        JSONObject record=new JSONObject(raw);
        String manifest=LocalIndexCrypto.open(key,id,record.getString("revisionId"),record.getString("authorId"),record.getString("createdAt"),record.getString("nonce"),record.getString("sealed"));
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

    private static boolean safeID(String value){return value.length()>=16&&value.length()<=128&&value.matches("[A-Za-z0-9-]+");}
    private static String read(File file)throws IOException{
        if(!file.exists()&&!new File(file.getPath()+".bak").exists())return "";
        return new String(new AtomicFile(file).readFully(),StandardCharsets.UTF_8);
    }
    private static void write(File file,String value)throws IOException{
        AtomicFile atomic=new AtomicFile(file);FileOutputStream stream=atomic.startWrite();
        try{stream.write(value.getBytes(StandardCharsets.UTF_8));atomic.finishWrite(stream);}catch(IOException ex){atomic.failWrite(stream);throw ex;}
    }
}
