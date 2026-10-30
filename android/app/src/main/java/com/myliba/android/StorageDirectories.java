package com.myliba.android;

import android.content.ContentResolver;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.database.Cursor;
import android.net.Uri;
import android.provider.DocumentsContract;
import android.text.TextUtils;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.FileNotFoundException;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.security.MessageDigest;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/** Shared per-device storage pool for generated previews and encrypted cloud objects. */
final class StorageDirectories {
    private static final String PREFS = "cloud_storage";
    private static final String KEY = "storage_directories";
    private static final String[] MANAGED = {"resource-thumbnails", "cloud-node"};
    private static final String[] PROJECTION = {DocumentsContract.Document.COLUMN_DOCUMENT_ID,
            DocumentsContract.Document.COLUMN_DISPLAY_NAME, DocumentsContract.Document.COLUMN_MIME_TYPE};
    private static final Map<String,Long> USAGE_CACHE = new HashMap<>();

    private StorageDirectories() {}

    static synchronized JSONArray configured(Context context) {
        try{return new JSONArray(prefs(context).getString(KEY, "[]"));}catch(Exception ignored){return new JSONArray();}
    }

    static boolean overlapsSourceRoot(Uri candidate,LocalLibrary library){
        for(LocalLibrary.Root root:library.roots())if(overlaps(candidate,Uri.parse(root.uri)))return true;
        return false;
    }

    static boolean overlaps(Uri first,Uri second){
        try{
            if(!first.getAuthority().equals(second.getAuthority()))return false;
            String a=DocumentsContract.getTreeDocumentId(first),b=DocumentsContract.getTreeDocumentId(second);
            return a.equals(b)||a.startsWith(b+"/")||b.startsWith(a+"/");
        }catch(Exception ignored){return false;}
    }

    static boolean overlapsConfigured(Context context,Uri candidate){
        JSONArray dirs=configured(context);for(int i=0;i<dirs.length();i++)try{if(overlaps(candidate,Uri.parse(dirs.getJSONObject(i).getString("uri"))))return true;}catch(Exception ignored){}return false;
    }

    static synchronized void add(Context context, Uri tree, String label) throws Exception {
        JSONArray dirs = configured(context);
        String value = tree.toString();
        for (int i=0;i<dirs.length();i++) if (value.equals(dirs.getJSONObject(i).optString("uri"))) return;
        dirs.put(new JSONObject().put("uri",value).put("label",label).put("maxBytes",0));
        prefs(context).edit().putString(KEY,dirs.toString()).apply();USAGE_CACHE.clear();
    }

    static synchronized void remove(Context context, int index) {
        JSONArray source=configured(context), next=new JSONArray();
        JSONObject removed=source.optJSONObject(index);
        for(int i=0;i<source.length();i++) if(i!=index) next.put(source.optJSONObject(i));
        prefs(context).edit().putString(KEY,next.toString()).apply();USAGE_CACHE.clear();
        if(removed!=null)try{context.getContentResolver().releasePersistableUriPermission(Uri.parse(removed.getString("uri")),Intent.FLAG_GRANT_READ_URI_PERMISSION|Intent.FLAG_GRANT_WRITE_URI_PERMISSION);}catch(Exception ignored){}
    }

    private static final class MigrationItem { String relative; long size; byte[] digest; MigrationItem(String r,long s,byte[] d){relative=r;size=s;digest=d;} }

    /** Copies and verifies managed files first; only then removes their source and releases a tree. */
    static synchronized void migrateAndRemove(Context context,int sourceIndex,int targetIndex)throws Exception{
        if(sourceIndex==targetIndex||targetIndex< -1||sourceIndex< -1)throw new IOException("迁移位置无效");
        JSONArray dirs=configured(context);if(sourceIndex>=dirs.length()||targetIndex>=dirs.length())throw new IOException("存储目录已变更，请刷新后重试");
        Uri source=sourceIndex<0?null:Uri.parse(dirs.getJSONObject(sourceIndex).getString("uri"));
        Uri target=targetIndex<0?null:Uri.parse(dirs.getJSONObject(targetIndex).getString("uri"));
        if(source!=null&&target!=null&&overlaps(source,target))throw new IOException("源目录与目标目录不能重叠");
        long cap=limit(context,targetIndex),used=usage(context,targetIndex),delta=0;
        List<MigrationItem> files=new ArrayList<>();
        for(String managed:MANAGED)collectMigration(context,source,managed,managed,files);
        for(MigrationItem item:files){
            byte[] existing=readTarget(context,target,item.relative);
            if(existing!=null){if(existing.length!=item.size||!MessageDigest.isEqual(sha256(existing),item.digest))throw new IOException("目标存在同名不同内容的文件："+item.relative);item.size=-item.size;}
            else delta+=item.size;
        }
        if(cap>0&&used+delta>cap)throw new IOException("目标目录剩余容量不足，源目录未变更");
        for(MigrationItem item:files)if(item.size>0){byte[] bytes=readSource(context,source,item.relative);if(!MessageDigest.isEqual(sha256(bytes),item.digest))throw new IOException("源文件在迁移期间发生变化："+item.relative);writeTarget(context,target,item.relative,bytes);byte[] copied=readTarget(context,target,item.relative);if(copied==null||!MessageDigest.isEqual(sha256(copied),item.digest))throw new IOException("迁移校验失败："+item.relative);}
        // Keep source data until every target file has been verified.
        for(String managed:MANAGED)deleteManaged(context,source,managed);
        if(sourceIndex>=0)remove(context,sourceIndex);
        USAGE_CACHE.clear();
    }

    private static void collectMigration(Context context,Uri tree,String path,String relative,List<MigrationItem> out)throws Exception{
        if(tree==null){File root=new File(context.getFilesDir(),path);if(!root.exists())return;if(java.nio.file.Files.isSymbolicLink(root.toPath()))throw new IOException("拒绝迁移符号链接");if(root.isFile()){byte[] data=readFile(root,Integer.MAX_VALUE);out.add(new MigrationItem(relative,data.length,sha256(data)));return;}File[] children=root.listFiles();if(children==null)throw new IOException("无法读取源目录");for(File child:children){if(java.nio.file.Files.isSymbolicLink(child.toPath()))throw new IOException("拒绝迁移符号链接");if(child.isDirectory())collectMigration(context,null,path+"/"+child.getName(),relative+"/"+child.getName(),out);else{byte[] data=readFile(child,Integer.MAX_VALUE);out.add(new MigrationItem(relative+"/"+child.getName(),data.length,sha256(data)));}}return;}
        Uri doc=findPath(context,tree,"Myliba/"+path);if(doc==null)return;String id=DocumentsContract.getDocumentId(doc);
        try(Cursor cursor=context.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(tree,id),PROJECTION,null,null,null)){if(cursor==null)throw new IOException("无法读取源目录");while(cursor.moveToNext()){String name=cursor.getString(1),mime=cursor.getString(2),childPath=path+"/"+name,childRel=relative+"/"+name;if(DocumentsContract.Document.MIME_TYPE_DIR.equals(mime))collectMigration(context,tree,childPath,childRel,out);else{Uri file=DocumentsContract.buildDocumentUriUsingTree(tree,cursor.getString(0));try(InputStream in=context.getContentResolver().openInputStream(file)){byte[] data=readStream(in,Integer.MAX_VALUE);out.add(new MigrationItem(childRel,data.length,sha256(data)));}}}}
    }

    private static byte[] readSource(Context context,Uri tree,String relative)throws Exception{if(tree==null)return readFile(new File(context.getFilesDir(),relative),Integer.MAX_VALUE);Uri file=findPath(context,tree,"Myliba/"+relative);if(file==null)throw new FileNotFoundException(relative);try(InputStream in=context.getContentResolver().openInputStream(file)){return readStream(in,Integer.MAX_VALUE);}}
    private static byte[] readTarget(Context context,Uri tree,String relative)throws Exception{try{return readSource(context,tree,relative);}catch(FileNotFoundException missing){return null;}}
    private static void writeTarget(Context context,Uri tree,String relative,byte[] data)throws Exception{if(tree==null){writeInternal(context,relative,data);return;}writeTree(context,tree,relative,data);}
    private static void deleteManaged(Context context,Uri tree,String name)throws Exception{if(tree==null){deleteFileTree(new File(context.getFilesDir(),name));return;}Uri root=findPath(context,tree,"Myliba/"+name);if(root!=null&&!DocumentsContract.deleteDocument(context.getContentResolver(),root))throw new IOException("无法清理源目录中的 "+name);}
    private static void deleteFileTree(File file)throws IOException{if(!file.exists())return;if(file.isDirectory()){File[] children=file.listFiles();if(children==null)throw new IOException("无法清理源目录");for(File child:children)deleteFileTree(child);}if(!file.delete())throw new IOException("无法清理源文件："+file.getName());}
    private static byte[] sha256(byte[] data)throws Exception{return MessageDigest.getInstance("SHA-256").digest(data);}

    static synchronized void setLimit(Context context, int index, long bytes) throws Exception {
        if(bytes<0)throw new IllegalArgumentException("容量上限无效");
        if(index<0){long used=usage(context,"");if(bytes>0&&bytes<used)throw new IOException("容量上限不能小于当前占用");prefs(context).edit().putLong("default_max_bytes",bytes).apply();return;}
        JSONArray source=configured(context), next=new JSONArray();
        for(int i=0;i<source.length();i++){
            JSONObject item=new JSONObject(source.getJSONObject(i).toString());
            if(i==index){long used=usage(context,item.optString("uri"));if(bytes>0&&bytes<used)throw new IOException("容量上限不能小于当前占用");item.put("maxBytes",bytes);}
            next.put(item);
        }
        prefs(context).edit().putString(KEY,next.toString()).apply();
    }

    static long limit(Context context,int index){
        if(index<0)return prefs(context).getLong("default_max_bytes",0);
        return configured(context).optJSONObject(index)==null?0:configured(context).optJSONObject(index).optLong("maxBytes",0);
    }

    static long usage(Context context,int index){
        if(index<0)return usage(context,"");
        JSONObject item=configured(context).optJSONObject(index);return item==null?0:usage(context,item.optString("uri"));
    }

    static String label(Context context,int index){
        if(index<0)return context.getFilesDir().getAbsolutePath();
        JSONObject item=configured(context).optJSONObject(index);return item==null?"":item.optString("label",item.optString("uri"));
    }

    static long totalCapacity(Context context){long total=0;int count=configured(context).length();for(int i=-1;i<count;i++){long cap=limit(context,i);total+=cap>0?cap:usage(context,i)+freeBytes(context,i);}return total;}
    static long totalAvailable(Context context){long total=0;int count=configured(context).length();for(int i=-1;i<count;i++){long cap=limit(context,i);total+=cap>0?Math.max(0,cap-usage(context,i)):freeBytes(context,i);}return total;}
    private static long freeBytes(Context context,int index){
        if(index<0)return context.getFilesDir().getUsableSpace();
        return 0;
    }

    private static SharedPreferences prefs(Context context){return context.getSharedPreferences(PREFS,Context.MODE_PRIVATE);}

    static synchronized void write(Context context,String relative,byte[] data)throws IOException{
        Exception last=null;
        int count=configured(context).length();
        for(int i=-1;i<count;i++){
            long oldBytes=existingLength(context,i,relative),current=usage(context,i),cap=limit(context,i);if(cap>0&&current-oldBytes+data.length>cap)continue;
            try{
                if(i<0)writeInternal(context,relative,data);
                else writeTree(context,Uri.parse(configured(context).getJSONObject(i).getString("uri")),relative,data);
                String key=i<0?"internal":configured(context).getJSONObject(i).getString("uri");USAGE_CACHE.put(key,Math.max(0,usage(context,i)-oldBytes)+data.length);
                return;
            }catch(Exception ex){last=ex;}
        }
        throw new IOException(last==null?"所有存储目录均已达到容量上限":"没有可写入的存储目录",last);
    }

    private static long existingLength(Context context,int index,String relative){
        try{if(index<0){File file=new File(context.getFilesDir(),relative);return file.isFile()?file.length():0;}Uri tree=Uri.parse(configured(context).getJSONObject(index).getString("uri"));Uri file=findPath(context,tree,"Myliba/"+relative);if(file==null)return 0;try(Cursor cursor=context.getContentResolver().query(file,new String[]{DocumentsContract.Document.COLUMN_SIZE},null,null,null)){return cursor!=null&&cursor.moveToFirst()?cursor.getLong(0):0;}}catch(Exception ignored){return 0;}
    }

    static synchronized byte[] read(Context context,String relative,int max)throws IOException{
        try{File file=new File(context.getFilesDir(),relative);if(file.isFile())return readFile(file,max);}catch(Exception ignored){}
        JSONArray dirs=configured(context);
        for(int i=0;i<dirs.length();i++)try{
            Uri found=findPath(context,Uri.parse(dirs.getJSONObject(i).getString("uri")),relative);
            if(found!=null)try(InputStream in=context.getContentResolver().openInputStream(found)){return readStream(in,max);}
        }catch(Exception ignored){}
        throw new FileNotFoundException(relative);
    }

    static synchronized long length(Context context,String relative){
        File file=new File(context.getFilesDir(),relative);if(file.isFile())return file.length();JSONArray dirs=configured(context);
        for(int i=0;i<dirs.length();i++)try{Uri doc=findPath(context,Uri.parse(dirs.getJSONObject(i).getString("uri")),"Myliba/"+relative);if(doc!=null)try(Cursor cursor=context.getContentResolver().query(doc,new String[]{DocumentsContract.Document.COLUMN_SIZE},null,null,null)){if(cursor!=null&&cursor.moveToFirst())return cursor.getLong(0);}}catch(Exception ignored){}return 0;
    }

    static synchronized List<String> list(Context context,String relative)throws IOException{
        List<String> values=new ArrayList<>();File internal=new File(context.getFilesDir(),relative);File[] children=internal.listFiles();
        if(children!=null)for(File child:children)values.add(child.getName());
        JSONArray dirs=configured(context);for(int i=0;i<dirs.length();i++)try{listTree(context,Uri.parse(dirs.getJSONObject(i).getString("uri")),relative,values);}catch(Exception ignored){}
        Collections.sort(values);return values;
    }

    private static synchronized long usage(Context context,String tree){
        String key=TextUtils.isEmpty(tree)?"internal":tree;Long cached=USAGE_CACHE.get(key);if(cached!=null)return cached;
        long total=0;
        if(TextUtils.isEmpty(tree)){
            for(String dir:MANAGED)total+=fileUsage(new File(context.getFilesDir(),dir));
            USAGE_CACHE.put(key,total);return total;
        }
        for(String dir:MANAGED)try{Uri found=findPath(context,Uri.parse(tree),"Myliba/"+dir);if(found!=null)total+=treeUsage(context,Uri.parse(tree),DocumentsContract.getDocumentId(found));}catch(Exception ignored){}
        USAGE_CACHE.put(key,total);return total;
    }

    private static long fileUsage(File root){
        if(!root.exists())return 0;if(root.isFile())return root.length();long total=0;File[] files=root.listFiles();if(files!=null)for(File f:files)total+=fileUsage(f);return total;
    }

    private static long treeUsage(Context context,Uri tree,String parent){
        long total=0;try(Cursor cursor=context.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(tree,parent),PROJECTION,null,null,null)){
            if(cursor==null)return 0;while(cursor.moveToNext()){String id=cursor.getString(0),mime=cursor.getString(2);if(DocumentsContract.Document.MIME_TYPE_DIR.equals(mime))total+=treeUsage(context,tree,id);else{Uri doc=DocumentsContract.buildDocumentUriUsingTree(tree,id);try(Cursor size=context.getContentResolver().query(doc,new String[]{DocumentsContract.Document.COLUMN_SIZE},null,null,null)){if(size!=null&&size.moveToFirst())total+=size.getLong(0);}}}
        }catch(Exception ignored){}return total;
    }

    private static void writeInternal(Context context,String relative,byte[] data)throws IOException{
        File target=new File(context.getFilesDir(),relative),parent=target.getParentFile();if(parent==null||(!parent.isDirectory()&&!parent.mkdirs()))throw new IOException("无法创建存储目录");
        File temp=File.createTempFile(".myliba-",".tmp",parent);try{try(FileOutputStream out=new FileOutputStream(temp)){out.write(data);out.getFD().sync();}if(target.exists()&&!target.delete())throw new IOException("无法替换缓存文件");if(!temp.renameTo(target))throw new IOException("无法保存缓存文件");}finally{temp.delete();}
    }

    private static void writeTree(Context context,Uri tree,String relative,byte[] data)throws Exception{
        String path="Myliba/"+relative;int slash=path.lastIndexOf('/');Uri parent=ensurePath(context,tree,path.substring(0,slash),true);String name=path.substring(slash+1);
        Uri old=findChild(context,tree,DocumentsContract.getDocumentId(parent),name);
        String tempName=name+".myliba-tmp-"+System.nanoTime();Uri temp=DocumentsContract.createDocument(context.getContentResolver(),parent,"application/octet-stream",tempName);if(temp==null)throw new IOException("文件夹不允许写入");
        try(OutputStream out=context.getContentResolver().openOutputStream(temp,"w")){if(out==null)throw new IOException("无法写入文件");out.write(data);}
        if(old!=null)DocumentsContract.deleteDocument(context.getContentResolver(),old);
        Uri renamed=null;try{renamed=DocumentsContract.renameDocument(context.getContentResolver(),temp,name);}catch(Exception ignored){}
        if(renamed!=null)return;
        Uri target=DocumentsContract.createDocument(context.getContentResolver(),parent,"application/octet-stream",name);if(target==null){DocumentsContract.deleteDocument(context.getContentResolver(),temp);throw new IOException("无法创建目标文件");}
        try(InputStream in=context.getContentResolver().openInputStream(temp);OutputStream out=context.getContentResolver().openOutputStream(target,"w")){if(in==null||out==null)throw new IOException("无法复制临时文件");byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1)out.write(buffer,0,n);}
        DocumentsContract.deleteDocument(context.getContentResolver(),temp);
    }

    private static Uri findPath(Context context,Uri tree,String path)throws Exception{
        String current=DocumentsContract.getTreeDocumentId(tree);Uri currentUri=DocumentsContract.buildDocumentUriUsingTree(tree,current);
        for(String part:path.split("/")){if(part.isEmpty())continue;currentUri=findChild(context,tree,DocumentsContract.getDocumentId(currentUri),part);if(currentUri==null)return null;}
        return currentUri;
    }

    private static Uri ensurePath(Context context,Uri tree,String path,boolean create)throws Exception{
        String current=DocumentsContract.getTreeDocumentId(tree);Uri currentUri=DocumentsContract.buildDocumentUriUsingTree(tree,current);
        for(String part:path.split("/")){if(part.isEmpty())continue;Uri next=findChild(context,tree,DocumentsContract.getDocumentId(currentUri),part);if(next==null&&create)next=DocumentsContract.createDocument(context.getContentResolver(),currentUri,DocumentsContract.Document.MIME_TYPE_DIR,part);if(next==null)throw new IOException("无法创建目录："+part);currentUri=next;}
        return currentUri;
    }

    private static Uri findChild(Context context,Uri tree,String parent,String name){
        try(Cursor cursor=context.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(tree,parent),PROJECTION,null,null,null)){
            if(cursor!=null)while(cursor.moveToNext())if(name.equals(cursor.getString(1)))return DocumentsContract.buildDocumentUriUsingTree(tree,cursor.getString(0));
        }catch(Exception ignored){}return null;
    }

    private static void listTree(Context context,Uri tree,String relative,List<String> out)throws Exception{
        Uri dir=findPath(context,tree,"Myliba/"+relative);if(dir==null)return;String doc=DocumentsContract.getDocumentId(dir);
        try(Cursor cursor=context.getContentResolver().query(DocumentsContract.buildChildDocumentsUriUsingTree(tree,doc),PROJECTION,null,null,null)){
            if(cursor!=null)while(cursor.moveToNext())out.add(cursor.getString(1));
        }
    }

    private static byte[] readFile(File file,int max)throws IOException{if(file.length()>max)throw new IOException("文件过大");try(InputStream in=new FileInputStream(file)){return readStream(in,max);}}
    private static byte[] readStream(InputStream in,int max)throws IOException{if(in==null)throw new IOException("文件不可读");ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>max)throw new IOException("文件过大");}return out.toByteArray();}
}
