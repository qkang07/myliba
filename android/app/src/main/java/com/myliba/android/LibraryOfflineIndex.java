package com.myliba.android;

import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import org.json.JSONArray;
import org.json.JSONObject;

/** Complete metadata generations keyed by server and query node. */
final class LibraryOfflineIndex extends SQLiteOpenHelper {
    LibraryOfflineIndex(Context context){super(context.getApplicationContext(),"library_offline_index.db",null,2);}
    @Override public void onCreate(SQLiteDatabase db){
        db.execSQL("CREATE TABLE metadata(key TEXT PRIMARY KEY,generation TEXT NOT NULL,library_id TEXT NOT NULL,synced_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE entries(key TEXT NOT NULL,file_id TEXT NOT NULL,json TEXT NOT NULL,PRIMARY KEY(key,file_id))");
        db.execSQL("CREATE TABLE staging(key TEXT NOT NULL,file_id TEXT NOT NULL,json TEXT NOT NULL,PRIMARY KEY(key,file_id))");
        db.execSQL("CREATE TABLE active(scope TEXT PRIMARY KEY,library_id TEXT NOT NULL)");
    }
    @Override public void onUpgrade(SQLiteDatabase db,int oldVersion,int newVersion){
        if(oldVersion<2){db.execSQL("CREATE TABLE active(scope TEXT PRIMARY KEY,library_id TEXT NOT NULL)");
            List<String[]> previous=new ArrayList<>();try(Cursor rows=db.rawQuery("SELECT key,library_id FROM metadata",null)){while(rows.moveToNext())previous.add(new String[]{rows.getString(0),rows.getString(1)});}
            for(String[] item:previous){String scoped=item[0]+'\n'+item[1];db.execSQL("UPDATE metadata SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("UPDATE entries SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("UPDATE staging SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{item[0],item[1]});}}
    }
    private static String scope(String base,String node){return base+'\n'+node;}
    private static String key(String scope,String library){return scope+'\n'+library;}
    void sync(RemoteClient client,String node)throws Exception{
        String scope=scope(client.base,node);SQLiteDatabase db=getWritableDatabase();
        JSONObject first=client.json("GET","/api/library/offline-index?scope=all&limit=200",null);
        String library=first.optString("libraryId"),generation=first.optString("generation");
        if(library.isEmpty()||generation.isEmpty()||!node.equals(first.optString("nodeId")))throw new IllegalStateException("离线索引库或查询节点不一致");
        String key=key(scope,library);
        try(Cursor row=db.rawQuery("SELECT generation,synced_at FROM metadata WHERE key=?",new String[]{key})){
            if(row.moveToFirst()&&generation.equals(row.getString(0))&&Instant.parse(row.getString(1)).isAfter(Instant.now().minusSeconds(300))){db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{scope,library});return;}
        }catch(Exception ignored){}
        db.delete("staging","key=?",new String[]{key});String next="";int count=0;JSONObject page=first;
        try{
            do{
                if(!generation.equals(page.optString("generation"))||!library.equals(page.optString("libraryId"))||!node.equals(page.optString("nodeId")))throw new IllegalStateException("离线索引代次或范围变化");
                JSONArray items=page.optJSONArray("items");if(items==null)throw new IllegalStateException("离线索引缺少文件页");
                db.beginTransaction();try{for(int i=0;i<items.length();i++){JSONObject file=items.optJSONObject(i);if(file==null)continue;String id=file.optString("fileId",file.optString("id"));if(id.isEmpty())continue;
                    db.execSQL("INSERT OR REPLACE INTO staging(key,file_id,json) VALUES(?,?,?)",new Object[]{key,id,file.toString()});count++;}
                    db.setTransactionSuccessful();}finally{db.endTransaction();}
                next=page.optString("nextCursor");if(count>1000000)throw new IllegalStateException("离线索引过大");
                if(!next.isEmpty())page=client.json("GET","/api/library/offline-index?scope=all&limit=200&cursor="+Uri.encode(next),null);
            }while(!next.isEmpty());
            db.beginTransaction();try{
                db.execSQL("DELETE FROM entries WHERE key=?",new Object[]{key});
                db.execSQL("INSERT INTO entries(key,file_id,json) SELECT key,file_id,json FROM staging WHERE key=?",new Object[]{key});
                db.execSQL("INSERT OR REPLACE INTO metadata(key,generation,library_id,synced_at) VALUES(?,?,?,?)",new Object[]{key,generation,library,Instant.now().toString()});
                db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{scope,library});
                db.execSQL("DELETE FROM staging WHERE key=?",new Object[]{key});db.setTransactionSuccessful();
            }finally{db.endTransaction();}
        }catch(Exception ex){db.delete("staging","key=?",new String[]{key});throw ex;}
    }
    JSONObject search(String base,String node,String path){
        String scopeKey=scope(base,node),library,generation,synced;SQLiteDatabase db=getReadableDatabase();
        try(Cursor row=db.rawQuery("SELECT library_id FROM active WHERE scope=?",new String[]{scopeKey})){if(!row.moveToFirst())return null;library=row.getString(0);}
        String key=key(scopeKey,library);
        try(Cursor row=db.rawQuery("SELECT generation,synced_at FROM metadata WHERE key=?",new String[]{key})){
            if(!row.moveToFirst())return null;generation=row.getString(0);synced=row.getString(1);
        }
        Uri uri=Uri.parse(path);String scope=uri.getQueryParameter("scope"),lifecycle=uri.getQueryParameter("lifecycle"),query=lower(uri.getQueryParameter("query")),folder=uri.getQueryParameter("folderId"),type=uri.getQueryParameter("type"),grouped=uri.getQueryParameter("grouped"),cloudCopy=uri.getQueryParameter("cloudCopy"),sort=uri.getQueryParameter("sort");
        if(scope==null)scope="all";if(lifecycle==null)lifecycle="active";List<String> tags=uri.getQueryParameters("tag");List<JSONObject> files=new ArrayList<>();
        try(Cursor rows=db.rawQuery("SELECT json FROM entries WHERE key=?",new String[]{key})){
            while(rows.moveToNext()){
                JSONObject file=new JSONObject(rows.getString(0));if(!lifecycle.equals(file.optString("lifecycle","active")))continue;
                if("cloud".equals(scope)&&file.optBoolean("localOnly"))continue;
                if(scope.startsWith("device:")||"local".equals(scope)){String wanted=scope.startsWith("device:")?scope.substring(7):node;boolean found=false;JSONArray locations=file.optJSONArray("locations");
                    if(locations!=null)for(int i=0;i<locations.length();i++){JSONObject location=locations.optJSONObject(i);if(location!=null&&wanted.equals(location.optString("nodeId")))found=true;}if(!found)continue;}
                if(type!=null&&!type.isEmpty()&&!type.equals(fileType(file.optString("name"))))continue;
                boolean hasFolder=!file.optString("folderId").isEmpty();if("true".equals(grouped)&&!hasFolder||"false".equals(grouped)&&hasFolder)continue;
                if("true".equals(cloudCopy)&&file.optBoolean("localOnly")||"false".equals(cloudCopy)&&!file.optBoolean("localOnly"))continue;
                JSONArray fileTags=file.optJSONArray("tags");boolean text=query.isEmpty()||lower(file.optString("name")).contains(query)||lower(file.optString("folderName")).contains(query);
                if(fileTags!=null)for(int i=0;i<fileTags.length();i++)if(lower(fileTags.optString(i)).contains(query))text=true;if(!text)continue;
                if(folder!=null&&!folder.isEmpty()&&!folder.equals(file.optString("folderId"))&&!("unfiled".equals(folder)&&!hasFolder)&&!(folder.startsWith("name:")&&lower(folder.substring(5)).equals(lower(file.optString("folderName")))))continue;
                boolean allTags=true;for(String tag:tags){boolean found=false;if(fileTags!=null)for(int i=0;i<fileTags.length();i++)if(lower(tag).equals(lower(fileTags.optString(i))))found=true;if(!found){allTags=false;break;}}if(allTags)files.add(file);
            }
        }catch(Exception ex){return null;}
        final String order=sort==null?"name_asc":sort;Collections.sort(files,(a,b)->{int result=0;
            if("name_desc".equals(order))result=b.optString("name").compareTo(a.optString("name"));
            else if("modified_desc".equals(order))result=b.optString("modified").compareTo(a.optString("modified"));
            else if("modified_asc".equals(order))result=a.optString("modified").compareTo(b.optString("modified"));
            else if("ingested_desc".equals(order))result=b.optString("ingestedAt").compareTo(a.optString("ingestedAt"));
            else if("ingested_asc".equals(order))result=a.optString("ingestedAt").compareTo(b.optString("ingestedAt"));
            else result=a.optString("name").compareTo(b.optString("name"));
            return result!=0?result:a.optString("fileId").compareTo(b.optString("fileId"));});
        int offset=0;String cursor=uri.getQueryParameter("cursor"),prefix="offline:"+generation+":";if(cursor!=null&&!cursor.isEmpty()){
            if(!cursor.startsWith(prefix))return null;try{offset=Integer.parseInt(cursor.substring(prefix.length()));}catch(Exception ex){return null;}}
        if(offset<0||offset>files.size())return null;int end=Math.min(files.size(),offset+50);JSONArray page=new JSONArray();for(int i=offset;i<end;i++)page.put(files.get(i));
        try{return new JSONObject().put("items",page).put("total",files.size()).put("nextCursor",end<files.size()?prefix+end:"").put("cachedAt",synced)
            .put("coverage",new JSONObject().put("scope",scope).put("generation",generation).put("offline",true));}catch(Exception ex){return null;}
    }
    private static String lower(String value){return value==null?"":value.toLowerCase(Locale.ROOT);}
    private static String fileType(String name){String value=lower(name);if(value.matches(".*\\.(jpg|jpeg|png|gif|webp|heic|arw|dng)$"))return "image";if(value.matches(".*\\.(mp4|mov|mkv|avi|webm)$"))return "video";if(value.matches(".*\\.(mp3|wav|flac|m4a|ogg)$"))return "audio";if(value.matches(".*\\.(pdf|txt|doc|docx|xls|xlsx|ppt|pptx)$"))return "document";return "other";}
}
