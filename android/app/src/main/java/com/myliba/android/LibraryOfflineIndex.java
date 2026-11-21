package com.myliba.android;

import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;
import android.net.Uri;
import android.util.Base64;
import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Set;
import java.util.zip.GZIPInputStream;
import org.json.JSONArray;
import org.json.JSONObject;

/** Complete metadata generations keyed by server and query node. */
final class LibraryOfflineIndex extends SQLiteOpenHelper {
    LibraryOfflineIndex(Context context){super(context.getApplicationContext(),"library_offline_index.db",null,4);}
    @Override public void onCreate(SQLiteDatabase db){
        db.execSQL("CREATE TABLE metadata(key TEXT PRIMARY KEY,generation TEXT NOT NULL,library_id TEXT NOT NULL,synced_at TEXT NOT NULL)");
        db.execSQL("CREATE TABLE entries(key TEXT NOT NULL,file_id TEXT NOT NULL,json TEXT NOT NULL,PRIMARY KEY(key,file_id))");
        db.execSQL("CREATE TABLE staging(key TEXT NOT NULL,file_id TEXT NOT NULL,json TEXT NOT NULL,PRIMARY KEY(key,file_id))");
        db.execSQL("CREATE TABLE active(scope TEXT PRIMARY KEY,library_id TEXT NOT NULL)");
        db.execSQL("CREATE TABLE classification_nodes(key TEXT NOT NULL,index_id TEXT NOT NULL,parent_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(key,index_id))");
        db.execSQL("CREATE TABLE classification_nodes_staging(key TEXT NOT NULL,index_id TEXT NOT NULL,parent_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(key,index_id))");
    }
    @Override public void onUpgrade(SQLiteDatabase db,int oldVersion,int newVersion){
        if(oldVersion<2){db.execSQL("CREATE TABLE active(scope TEXT PRIMARY KEY,library_id TEXT NOT NULL)");
            List<String[]> previous=new ArrayList<>();try(Cursor rows=db.rawQuery("SELECT key,library_id FROM metadata",null)){while(rows.moveToNext())previous.add(new String[]{rows.getString(0),rows.getString(1)});}
            for(String[] item:previous){String scoped=item[0]+'\n'+item[1];db.execSQL("UPDATE metadata SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("UPDATE entries SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("UPDATE staging SET key=? WHERE key=?",new Object[]{scoped,item[0]});db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{item[0],item[1]});}}
        if(oldVersion<3){db.execSQL("CREATE TABLE classification_nodes(key TEXT NOT NULL,index_id TEXT NOT NULL,parent_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(key,index_id))");db.execSQL("CREATE TABLE classification_nodes_staging(key TEXT NOT NULL,index_id TEXT NOT NULL,parent_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(key,index_id))");db.execSQL("DELETE FROM metadata");}
        if(oldVersion<4)db.execSQL("DELETE FROM metadata");
    }
    private static String scope(String base,String node){return base+'\n'+node;}
    private static String key(String scope,String library){return scope+'\n'+library;}
    void sync(RemoteClient client,String node)throws Exception{
        String scope=scope(client.base,node);SQLiteDatabase db=getWritableDatabase();
        JSONObject first=client.json("GET","/api/library/offline-index?scope=all&limit=200",null);
        String library=first.optString("libraryId"),generation=first.optString("generation");
        if(library.isEmpty()||generation.isEmpty()||first.optInt("classificationVersion")!=2||!node.equals(first.optString("nodeId")))throw new IllegalStateException("离线索引库、分类格式或查询节点不一致");
        String key=key(scope,library);
        try(Cursor row=db.rawQuery("SELECT generation,synced_at FROM metadata WHERE key=?",new String[]{key})){
            if(row.moveToFirst()&&generation.equals(row.getString(0))&&Instant.parse(row.getString(1)).isAfter(Instant.now().minusSeconds(300))){db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{scope,library});return;}
        }catch(Exception ignored){}
        db.delete("staging","key=?",new String[]{key});db.delete("classification_nodes_staging","key=?",new String[]{key});String nodeCursor="";int nodeCount=0,expectedNodes=first.optInt("classificationNodeTotal",-1);if(expectedNodes<0)throw new IllegalStateException("离线索引缺少分类字典计数");
        try{
            do{
                JSONObject nodePage=client.json("GET","/api/library/offline-index?scope=all&section=classification&limit=500&generation="+Uri.encode(generation)+(nodeCursor.isEmpty()?"":"&cursor="+Uri.encode(nodeCursor)),null);
                if(!generation.equals(nodePage.optString("generation"))||!library.equals(nodePage.optString("libraryId"))||!node.equals(nodePage.optString("nodeId"))||nodePage.optInt("classificationVersion")!=2||!"gzip+base64+json".equals(nodePage.optString("classificationEncoding")))throw new IllegalStateException("离线分类字典代次或格式变化");
                JSONArray nodeItems=decodeNodes(nodePage);db.beginTransaction();try{for(int i=0;i<nodeItems.length();i++){JSONObject item=nodeItems.optJSONObject(i);if(item!=null){db.execSQL("INSERT OR REPLACE INTO classification_nodes_staging(key,index_id,parent_id,name) VALUES(?,?,?,?)",new Object[]{key,item.optString("id"),item.optString("parentId"),item.optString("name")});nodeCount++;}}db.setTransactionSuccessful();}finally{db.endTransaction();}
                nodeCursor=nodePage.optString("nextCursor");if(nodeCount>1000000)throw new IllegalStateException("离线分类字典过大");
            }while(!nodeCursor.isEmpty());
            if(nodeCount!=expectedNodes)throw new IllegalStateException("离线分类字典不完整");
            String next="";int count=0;JSONObject page=first;
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
                db.execSQL("DELETE FROM classification_nodes WHERE key=?",new Object[]{key});
                db.execSQL("INSERT INTO classification_nodes(key,index_id,parent_id,name) SELECT key,index_id,parent_id,name FROM classification_nodes_staging WHERE key=?",new Object[]{key});
                db.execSQL("INSERT OR REPLACE INTO metadata(key,generation,library_id,synced_at) VALUES(?,?,?,?)",new Object[]{key,generation,library,Instant.now().toString()});
                db.execSQL("INSERT OR REPLACE INTO active(scope,library_id) VALUES(?,?)",new Object[]{scope,library});
                db.execSQL("DELETE FROM staging WHERE key=?",new Object[]{key});db.execSQL("DELETE FROM classification_nodes_staging WHERE key=?",new Object[]{key});db.setTransactionSuccessful();
            }finally{db.endTransaction();}
        }catch(Exception ex){db.delete("staging","key=?",new String[]{key});db.delete("classification_nodes_staging","key=?",new String[]{key});throw ex;}
    }
    private static JSONArray decodeNodes(JSONObject page)throws Exception{
        byte[] compressed;try{compressed=Base64.decode(page.getString("classificationPayload"),Base64.DEFAULT);}catch(Exception ex){throw new IOException("离线分类字典编码无效",ex);}
        ByteArrayOutputStream output=new ByteArrayOutputStream();try(GZIPInputStream input=new GZIPInputStream(new ByteArrayInputStream(compressed))){byte[] buffer=new byte[8192];int count,total=0;while((count=input.read(buffer))!=-1){total+=count;if(total>8*1024*1024)throw new IOException("离线分类字典分页过大");output.write(buffer,0,count);}}
        return new JSONArray(new String(output.toByteArray(),StandardCharsets.UTF_8));
    }
    JSONObject search(String base,String node,String path){
        String scopeKey=scope(base,node),library,generation,synced;SQLiteDatabase db=getReadableDatabase();
        try(Cursor row=db.rawQuery("SELECT library_id FROM active WHERE scope=?",new String[]{scopeKey})){if(!row.moveToFirst())return null;library=row.getString(0);}
        String key=key(scopeKey,library);
        try(Cursor row=db.rawQuery("SELECT generation,synced_at FROM metadata WHERE key=?",new String[]{key})){
            if(!row.moveToFirst())return null;generation=row.getString(0);synced=row.getString(1);
        }
        Uri uri=Uri.parse(path);String scope=uri.getQueryParameter("scope"),lifecycle=uri.getQueryParameter("lifecycle"),query=lower(uri.getQueryParameter("query")),folder=uri.getQueryParameter("folderId"),type=uri.getQueryParameter("type"),grouped=uri.getQueryParameter("grouped"),cloudCopy=uri.getQueryParameter("cloudCopy"),readable=uri.getQueryParameter("readable"),sort=uri.getQueryParameter("sort"),browseId=uri.getQueryParameter("browseId"),browseMode=uri.getQueryParameter("browseMode"),needs=uri.getQueryParameter("needsOrganizing"),facetParent=uri.getQueryParameter("facetParentId");
        if(readable!=null&&!readable.isEmpty())return null;
        if(scope==null)scope="all";if(lifecycle==null)lifecycle="active";if(browseId==null)browseId="";if(browseMode==null||browseMode.isEmpty())browseMode="subtree";List<String> tags=uri.getQueryParameters("tag"),indexConditions=uri.getQueryParameters("indexFilter");List<JSONObject> files=new ArrayList<>(),folderFacetFiles=new ArrayList<>(),tagFacetFiles=new ArrayList<>(),indexFacetFiles=new ArrayList<>();Map<String,String> parents=new HashMap<>(),names=new HashMap<>();
        try(Cursor nodeRows=db.rawQuery("SELECT index_id,parent_id,name FROM classification_nodes WHERE key=?",new String[]{key})){while(nodeRows.moveToNext()){parents.put(nodeRows.getString(0),nodeRows.getString(1));names.put(nodeRows.getString(0),nodeRows.getString(2));}}
        if((!browseId.isEmpty()&&!parents.containsKey(browseId))||(facetParent!=null&&!facetParent.isEmpty()&&!parents.containsKey(facetParent)))return null;for(String condition:indexConditions){int split=condition.indexOf(':');if(split<1||!parents.containsKey(condition.substring(split+1)))return null;}
        try(Cursor rows=db.rawQuery("SELECT json FROM entries WHERE key=?",new String[]{key})){
            while(rows.moveToNext()){
                JSONObject file=new JSONObject(rows.getString(0));if(!lifecycle.equals(file.optString("lifecycle","active")))continue;
                if("cloud".equals(scope)&&file.optBoolean("localOnly"))continue;
                if(scope.startsWith("device:")||"local".equals(scope)){String wanted=scope.startsWith("device:")?scope.substring(7):node;boolean found=false;JSONArray locations=file.optJSONArray("locations");
                    if(locations!=null)for(int i=0;i<locations.length();i++){JSONObject location=locations.optJSONObject(i);if(location!=null&&wanted.equals(location.optString("nodeId")))found=true;}if(!found)continue;}
                if(type!=null&&!type.isEmpty()&&!type.equals(fileType(file.optString("name"))))continue;
                boolean hasFolder=!file.optString("folderId").isEmpty();if("true".equals(grouped)&&!hasFolder||"false".equals(grouped)&&hasFolder)continue;
                if("true".equals(cloudCopy)&&file.optBoolean("localOnly")||"false".equals(cloudCopy)&&!file.optBoolean("localOnly"))continue;
                JSONArray fileTags=file.optJSONArray("tags");boolean text=query.isEmpty()||lower(file.optString("name")).contains(query)||lower(file.optString("folderName")).contains(query)||indexText(file,parents,names).contains(query);
                if(fileTags!=null)for(int i=0;i<fileTags.length();i++)if(lower(fileTags.optString(i)).contains(query))text=true;if(!text)continue;
                boolean folderMatch=folder==null||folder.isEmpty()||folder.equals(file.optString("folderId"))||"unfiled".equals(folder)&&!hasFolder||folder.startsWith("name:")&&lower(folder.substring(5)).equals(lower(file.optString("folderName")));
                boolean tagsMatch=true;for(String tag:tags){boolean found=false;if(fileTags!=null)for(int i=0;i<fileTags.length();i++)if(lower(tag).equals(lower(fileTags.optString(i))))found=true;if(!found){tagsMatch=false;break;}}
                boolean indexesMatch=true;for(String condition:indexConditions){int split=condition.indexOf(':');if(!inIndex(file,condition.substring(split+1),condition.substring(0,split),parents)){indexesMatch=false;break;}}
                boolean organized=organized(file),needsMatch=needs==null||needs.isEmpty()||"true".equals(needs)!=organized,browseMatch=browseId.isEmpty()||inIndex(file,browseId,browseMode,parents);
                if(tagsMatch&&indexesMatch&&needsMatch&&browseMatch)folderFacetFiles.add(file);
                if(folderMatch&&indexesMatch&&needsMatch&&browseMatch)tagFacetFiles.add(file);
                if(folderMatch&&tagsMatch&&indexesMatch&&needsMatch)indexFacetFiles.add(file);
                if(folderMatch&&tagsMatch&&indexesMatch&&needsMatch&&browseMatch)files.add(file);
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
        try{Map<String,JSONObject> folderCounts=new HashMap<>();for(JSONObject file:folderFacetFiles){String id=file.optString("folderId"),name=file.optString("folderName");if(id.isEmpty()){id="unfiled";name="未归组";}JSONObject item=folderCounts.get(id);if(item==null){item=new JSONObject().put("id",id).put("name",name).put("count",0);folderCounts.put(id,item);}item.put("count",item.optInt("count")+1);}if(folder!=null&&!folder.isEmpty()&&!folderCounts.containsKey(folder))folderCounts.put(folder,new JSONObject().put("id",folder).put("name","unfiled".equals(folder)?"未归组":folder).put("count",0));List<JSONObject> sortedFolders=new ArrayList<>(folderCounts.values());Collections.sort(sortedFolders,(a,b)->{int compared=a.optString("name").compareTo(b.optString("name"));return compared!=0?compared:a.optString("id").compareTo(b.optString("id"));});JSONArray folderFacets=new JSONArray();for(JSONObject item:sortedFolders)folderFacets.put(item);
            Map<String,JSONObject> tagCounts=new HashMap<>();for(JSONObject file:tagFacetFiles){JSONArray fileTags=file.optJSONArray("tags");Set<String> seen=new HashSet<>();if(fileTags!=null)for(int i=0;i<fileTags.length();i++){String name=fileTags.optString(i),tagKey=lower(name);if(tagKey.isEmpty()||!seen.add(tagKey))continue;JSONObject item=tagCounts.get(tagKey);if(item==null){item=new JSONObject().put("name",name).put("count",0);tagCounts.put(tagKey,item);}item.put("count",item.optInt("count")+1);}}for(String tag:tags)if(!tagCounts.containsKey(lower(tag)))tagCounts.put(lower(tag),new JSONObject().put("name",tag).put("count",0));List<JSONObject> sortedTags=new ArrayList<>(tagCounts.values());Collections.sort(sortedTags,(a,b)->a.optString("name").compareTo(b.optString("name")));JSONArray tagFacets=new JSONArray();for(JSONObject item:sortedTags)tagFacets.put(item);
            JSONArray indexFacets=new JSONArray();if(facetParent!=null)for(Map.Entry<String,String> indexNode:names.entrySet())if(facetParent.equals(parents.get(indexNode.getKey()))){int count=0;for(JSONObject file:indexFacetFiles)if(inIndex(file,indexNode.getKey(),"subtree",parents))count++;indexFacets.put(new JSONObject().put("id",indexNode.getKey()).put("name",indexNode.getValue()).put("mode","subtree").put("count",count));}
            return new JSONObject().put("items",page).put("total",files.size()).put("nextCursor",end<files.size()?prefix+end:"").put("cachedAt",synced)
            .put("facets",new JSONObject().put("folders",folderFacets).put("tags",tagFacets).put("indexes",indexFacets)).put("coverage",new JSONObject().put("scope",scope).put("generation",generation).put("offline",true));}catch(Exception ex){return null;}
    }
    private static boolean organized(JSONObject file){JSONArray indexes=file.optJSONArray("indexes");if(indexes!=null)for(int i=0;i<indexes.length();i++){JSONObject item=indexes.optJSONObject(i);if(item!=null&&item.optBoolean("manual"))return true;}return false;}
    private static boolean inIndex(JSONObject file,String wanted,String mode,Map<String,String> parents){JSONArray indexes=file.optJSONArray("indexes");if(indexes==null)return false;for(int i=0;i<indexes.length();i++){JSONObject item=indexes.optJSONObject(i);String current=item==null?"":item.optString("indexId");if("direct".equals(mode)){if(wanted.equals(current))return true;continue;}Set<String> seen=new HashSet<>();while(!current.isEmpty()&&seen.add(current)){if(wanted.equals(current))return true;current=parents.get(current);if(current==null)current="";}}return false;}
    private static String indexText(JSONObject file,Map<String,String> parents,Map<String,String> names){StringBuilder text=new StringBuilder();JSONArray indexes=file.optJSONArray("indexes");if(indexes!=null)for(int i=0;i<indexes.length();i++){JSONObject item=indexes.optJSONObject(i);String current=item==null?"":item.optString("indexId");Set<String> seen=new HashSet<>();while(!current.isEmpty()&&seen.add(current)){text.append(' ').append(lower(names.get(current)));current=parents.get(current);if(current==null)current="";}}return text.toString();}
    private static String lower(String value){return value==null?"":value.toLowerCase(Locale.ROOT);}
    private static String fileType(String name){String value=lower(name);if(value.matches(".*\\.(jpg|jpeg|png|gif|webp|heic|arw|dng)$"))return "image";if(value.matches(".*\\.(mp4|mov|mkv|avi|webm)$"))return "video";if(value.matches(".*\\.(mp3|wav|flac|m4a|ogg)$"))return "audio";if(value.matches(".*\\.(pdf|txt|doc|docx|xls|xlsx|ppt|pptx)$"))return "document";return "other";}
}
