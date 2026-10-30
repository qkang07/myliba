package com.myliba.android;

import android.content.Context;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.media.MediaMetadataRetriever;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.util.Base64;
import org.json.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.*;

/** Remote, allowlisted view of the phone's existing SAF/SQLite library. */
final class NodeResourceApi {
    static final class Result implements Closeable {
        final int status;final JSONObject headers;final InputStream body;
        Result(int status,String type,InputStream body,long length)throws Exception{
            this.status=status;this.body=body;headers=new JSONObject();
            headers.put("Content-Type",new JSONArray().put(type));
            if(length>=0)headers.put("Content-Length",new JSONArray().put(Long.toString(length)));
            headers.put("Cache-Control",new JSONArray().put("private, no-store"));
        }
        @Override public void close()throws IOException{body.close();}
    }
    private final Context context;private final LocalLibrary db;
    NodeResourceApi(Context context){this.context=context;db=new LocalLibrary(context);}
    void close(){db.close();}
    static String id(String uri){return Base64.encodeToString(uri.getBytes(StandardCharsets.UTF_8),Base64.URL_SAFE|Base64.NO_WRAP|Base64.NO_PADDING);}
    static String decodeID(String encoded){try{return new String(Base64.decode(encoded,Base64.URL_SAFE|Base64.NO_WRAP|Base64.NO_PADDING),StandardCharsets.UTF_8);}catch(Exception ex){return "";}}
    private LocalLibrary.Item item(String encoded){
        String uri=decodeID(encoded);return uri.isEmpty()?null:db.item(uri);
    }
    private static Result json(int status,Object value)throws Exception{byte[] bytes=value.toString().getBytes(StandardCharsets.UTF_8);return new Result(status,"application/json; charset=utf-8",new ByteArrayInputStream(bytes),bytes.length);}
    private static Result error(int status,String message)throws Exception{return json(status,new JSONObject().put("error",message));}
    private static int integer(String raw,int fallback,int min,int max){try{int value=Integer.parseInt(raw);return Math.max(min,Math.min(max,value));}catch(Exception ex){return fallback;}}
    private static String kind(LocalLibrary.Item item){return LibraryStore.kind(item.name,item.mime);}
    private JSONObject resource(LocalLibrary.Item item)throws Exception{
        return new JSONObject().put("id",id(item.uri)).put("name",item.name).put("path",item.path)
            .put("size",item.size).put("contentSize",item.size).put("modified",Instant.ofEpochMilli(item.modified).toString())
            .put("kind",kind(item)).put("available",true).put("versionId","").put("folderId",item.folderId==null?"":item.folderId)
            .put("tags",new JSONArray(db.tags(item.uri))).put("attributeTags",new JSONArray());
    }
    private static LocalFilters filters(Uri uri){
        LocalFilters f=new LocalFilters();f.query=uri.getQueryParameter("q")==null?"":uri.getQueryParameter("q");
        f.tags.addAll(uri.getQueryParameters("tag"));f.attributes.addAll(uri.getQueryParameters("attribute"));
        String root=uri.getQueryParameter("rootId"),folder=uri.getQueryParameter("folderId"),sort=uri.getQueryParameter("sort"),kind=uri.getQueryParameter("kind");
        if(root!=null)f.rootUri=root;if(folder!=null&&!folder.isEmpty())f.folderId=folder;if(sort!=null&&!sort.isEmpty())f.sort=sort;
        if(kind!=null&&!kind.isEmpty())f.attributes.add("type:"+kind);
        f.unorganized="1".equals(uri.getQueryParameter("unorganized"))||"true".equals(uri.getQueryParameter("unorganized"));
        return f;
    }
    private static JSONObject body(byte[] data)throws Exception{if(data.length>2<<20)throw new IOException("request too large");return data.length==0?new JSONObject():new JSONObject(new String(data,StandardCharsets.UTF_8));}
    private static List<String> ids(JSONArray array)throws Exception{List<String> result=new ArrayList<>();if(array!=null)for(int i=0;i<array.length();i++)result.add(array.getString(i));return result;}
    private static String header(JSONObject headers,String name){for(Iterator<String> keys=headers.keys();keys.hasNext();){String key=keys.next();if(key.equalsIgnoreCase(name)){Object value=headers.opt(key);return value instanceof JSONArray?((JSONArray)value).optString(0,""):value instanceof String?(String)value:"";}}return "";}
    private List<String> uris(JSONArray array)throws Exception{List<String> result=new ArrayList<>();for(String encoded:ids(array)){LocalLibrary.Item item=item(encoded);if(item!=null)result.add(item.uri);}return result;}

    Result handle(String method,String rawPath,byte[] requestBody,JSONObject requestHeaders)throws Exception{
        Uri uri=Uri.parse(rawPath);String path=uri.getPath();
        if(path==null||!path.startsWith("/api/")||path.contains("..")||path.contains("\\"))return error(403,"远程接口不允许访问");
        String[] parts=path.substring(1).split("/");
        if(parts.length==2&&"roots".equals(parts[1])&&"GET".equals(method)){
            JSONArray roots=new JSONArray();for(LocalLibrary.Root root:db.roots())roots.put(new JSONObject().put("id",root.uri).put("path",root.name).put("addedAt","").put("lastScanAt","").put("autoPreprocess",false).put("count",root.count));return json(200,roots);
        }
        if(parts.length==2&&"resources".equals(parts[1])&&"GET".equals(method))return page(uri,false);
        if(parts.length==3&&"resources".equals(parts[1])&&"selection".equals(parts[2])&&"GET".equals(method))return page(uri,true);
        if(parts.length==3&&"resources".equals(parts[1])&&"GET".equals(method)){
            LocalLibrary.Item found=item(parts[2]);return found==null?error(404,"资源不存在"):json(200,resource(found));
        }
        if(parts.length==4&&"resources".equals(parts[1])){
            LocalLibrary.Item found=item(parts[2]);if(found==null)return error(404,"资源不存在");
            if("file".equals(parts[3])&&"GET".equals(method))return file(found,requestHeaders,uri);
            if("thumbnail".equals(parts[3])&&"GET".equals(method))return thumbnail(found,false);
            if("thumbnail".equals(parts[3])&&"POST".equals(method)){
                try(Result generated=thumbnail(found,true)){return generated.status==200?json(200,new JSONObject().put("ok",true)):error(404,"暂无缩略图");}
            }
            if("tag-suggestions".equals(parts[3])&&"GET".equals(method))return json(200,new JSONObject().put("suggestions",new JSONArray()));
            if("video-info".equals(parts[3])&&"GET".equals(method))return videoInfo(found);
        }
        if(parts.length==2&&"folders".equals(parts[1])&&"GET".equals(method)){
            JSONArray folders=new JSONArray();int grouped=0,total=0;for(LocalLibrary.Root root:db.roots())total+=root.count;
            for(LocalLibrary.Folder folder:db.folders()){grouped+=folder.count;folders.put(new JSONObject().put("id",folder.id).put("name",folder.name).put("count",folder.count).put("createdAt",""));}
            return json(200,new JSONObject().put("items",folders).put("unfiledCount",Math.max(0,total-grouped)));
        }
        if(parts.length==2&&"collections".equals(parts[1])&&"GET".equals(method)){
            JSONArray collections=new JSONArray();for(LocalLibrary.Collection collection:db.collections()){
                JSONObject f=new JSONObject(collection.filters.serialize());f.put("rootId",collection.filters.rootUri);f.put("kind","");
                collections.put(new JSONObject().put("id",collection.id).put("name",collection.name).put("filters",f).put("createdAt",""));
            }return json(200,collections);
        }
        if(parts.length==2&&"tags".equals(parts[1])&&"GET".equals(method)){
            JSONArray tags=new JSONArray();String folder=uri.getQueryParameter("folderId");
            for(LocalLibrary.Attribute tag:db.tagCatalog(folder==null?"all":folder))tags.put(new JSONObject().put("id","tag-"+id(tag.key)).put("name",tag.label).put("count",tag.count));return json(200,tags);
        }
        if(parts.length==2&&"attribute-tags".equals(parts[1])&&"GET".equals(method)){
            JSONArray attributes=new JSONArray();for(LocalLibrary.Attribute attribute:db.attributeCatalog(filters(uri)))attributes.put(new JSONObject().put("key",attribute.key).put("category",attribute.key.split(":",2)[0]).put("label",attribute.label).put("count",attribute.count));return json(200,attributes);
        }
        if(parts.length==2&&"preprocess".equals(parts[1])&&"GET".equals(method))return json(200,new JSONObject().put("counts",new JSONObject().put("pending",0).put("running",0).put("done",0).put("failed",0)).put("jobs",new JSONArray()).put("visible",new JSONObject()));
        if(parts.length==2&&"tag-batch-suggestions".equals(parts[1])&&"GET".equals(method))return json(200,new JSONObject().put("suggestions",new JSONArray()).put("scanned",0));
        if(parts.length==2&&"cloud".equals(parts[1]))return error(404,"手机资源节点的云层功能独立配置");
        return mutate(method,parts,requestBody);
    }

    private Result page(Uri uri,boolean selection)throws Exception{
        int limit=integer(uri.getQueryParameter("limit"),48,1,1000),offset=integer(uri.getQueryParameter("offset"),0,0,Integer.MAX_VALUE);
        LocalLibrary.Page page=db.search(filters(uri),limit,offset);JSONArray items=new JSONArray();
        for(LocalLibrary.Item item:page.items)items.put(selection?new JSONObject().put("id",id(item.uri)).put("kind",kind(item)).put("available",true):resource(item));
        return json(200,new JSONObject().put("items",items).put("total",page.total).put("limit",limit).put("offset",offset));
    }
    private Result file(LocalLibrary.Item item,JSONObject headers,Uri uri)throws Exception{
        String range=header(headers,"Range");
        long start=0,end=item.size-1;int status=200;
        if(!range.isEmpty()){
            if(!range.matches("bytes=[0-9]+-[0-9]*"))return error(416,"文件范围无效");
            String[] bounds=range.substring(6).split("-",-1);start=Long.parseLong(bounds[0]);if(!bounds[1].isEmpty())end=Math.min(end,Long.parseLong(bounds[1]));
            if(start>=item.size||end<start)return error(416,"文件范围无效");status=206;
        }
        Uri fileUri=Uri.parse(item.uri);InputStream source=null;boolean positioned=false;
        if(start>0){
            try{ParcelFileDescriptor descriptor=context.getContentResolver().openFileDescriptor(fileUri,"r");
                if(descriptor!=null){ParcelFileDescriptor.AutoCloseInputStream seekable=new ParcelFileDescriptor.AutoCloseInputStream(descriptor);try{seekable.getChannel().position(start);source=seekable;positioned=true;}catch(IOException ex){seekable.close();}}
            }catch(IOException ignored){/* Some document providers only expose sequential streams. */}
        }
        if(source==null)source=context.getContentResolver().openInputStream(fileUri);if(source==null)return error(404,"文件不可访问");
        long skipped=0;while(!positioned&&skipped<start){long n=source.skip(start-skipped);if(n<=0){source.close();return error(404,"文件不可访问");}skipped+=n;}
        long length=item.size==0?0:end-start+1;
        String type=item.mime==null||item.mime.isEmpty()?"application/octet-stream":item.mime;
        if(type.equals("text/html")||type.equals("image/svg+xml")||type.contains("javascript"))type="application/octet-stream";
        Result result=new Result(status,type,new LimitedInputStream(source,length),length);
        result.headers.put("Accept-Ranges",new JSONArray().put("bytes"));
        if(status==206)result.headers.put("Content-Range",new JSONArray().put("bytes "+start+"-"+end+"/"+item.size));
        if("1".equals(uri.getQueryParameter("download"))||"application/octet-stream".equals(type))result.headers.put("Content-Disposition",new JSONArray().put("attachment; filename=\""+item.name.replace("\"","_")+"\""));
        result.headers.put("Content-Security-Policy",new JSONArray().put("sandbox"));
        return result;
    }
    private Result thumbnail(LocalLibrary.Item item,boolean force)throws Exception{
        String cachePath=ThumbnailLoader.cacheFile(context,item.uri,item.modified,item.size);
        if(!force)try{byte[] cached=StorageDirectories.read(context,cachePath,4*1024*1024);return new Result(200,"image/jpeg",new ByteArrayInputStream(cached),cached.length);}catch(IOException ignored){}
        Bitmap bitmap=null;Uri uri=Uri.parse(item.uri);
        try{
            if("video".equals(kind(item))){MediaMetadataRetriever retriever=new MediaMetadataRetriever();try{retriever.setDataSource(context,uri);bitmap=retriever.getFrameAtTime(0);}finally{retriever.release();}}
            else if(item.name.toLowerCase(java.util.Locale.ROOT).endsWith(".arw")){bitmap=ThumbnailLoader.arwPreview(context,uri);}
            else if("image".equals(kind(item))){BitmapFactory.Options options=new BitmapFactory.Options();options.inJustDecodeBounds=true;try(InputStream in=context.getContentResolver().openInputStream(uri)){BitmapFactory.decodeStream(in,null,options);}int sample=1;while(Math.max(options.outWidth,options.outHeight)/sample>1024)sample*=2;options.inJustDecodeBounds=false;options.inSampleSize=sample;try(InputStream in=context.getContentResolver().openInputStream(uri)){bitmap=BitmapFactory.decodeStream(in,null,options);}}
        }catch(Exception ignored){}
        if(bitmap==null)return error(404,item.name.toLowerCase(java.util.Locale.ROOT).endsWith(".arw")?"ARW 文件没有可用的内嵌 JPEG 预览":"暂无缩略图");
        ByteArrayOutputStream encoded=new ByteArrayOutputStream();try{if(!bitmap.compress(Bitmap.CompressFormat.JPEG,78,encoded))throw new IOException("无法生成缩略图");}finally{bitmap.recycle();}
        byte[] bytes=encoded.toByteArray();try{StorageDirectories.write(context,cachePath,bytes);}catch(IOException ignored){}
        return new Result(200,"image/jpeg",new ByteArrayInputStream(bytes),bytes.length);
    }
    private Result videoInfo(LocalLibrary.Item item)throws Exception{
        MediaMetadataRetriever retriever=new MediaMetadataRetriever();long duration=0;
        try{retriever.setDataSource(context,Uri.parse(item.uri));duration=Long.parseLong(retriever.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION));}catch(Exception ignored){}finally{retriever.release();}
        return json(200,new JSONObject().put("duration",duration/1000.0));
    }
    private static final class LimitedInputStream extends FilterInputStream{
        long left;LimitedInputStream(InputStream in,long length){super(in);left=length;}
        @Override public int read()throws IOException{if(left<=0)return -1;int value=super.read();if(value>=0)left--;return value;}
        @Override public int read(byte[] b,int off,int len)throws IOException{if(left<=0)return -1;int n=super.read(b,off,(int)Math.min(len,left));if(n>0)left-=n;return n;}
    }
    private Result mutate(String method,String[] parts,byte[] data)throws Exception{
        if(parts.length==3&&"resources".equals(parts[1])&&"tags".equals(parts[2])&&"POST".equals(method)){
            JSONObject input=body(data);List<String> uris=uris(input.optJSONArray("ids"));db.addTagToItems(uris,input.optString("name"));
            return json(200,new JSONObject().put("ok",true).put("updated",uris.size()).put("tag",input.optString("name")));
        }
        if(parts.length==3&&"resources".equals(parts[1])&&"folder".equals(parts[2])&&"PUT".equals(method)){
            JSONObject input=body(data);List<String> uris=uris(input.optJSONArray("ids"));String folder=input.optString("folderId","unfiled");
            db.moveItems(uris,folder.isEmpty()?"unfiled":folder);return json(200,new JSONObject().put("ok",true).put("updated",uris.size()));
        }
        if(parts.length==4&&"resources".equals(parts[1])&&"tags".equals(parts[3])&&"POST".equals(method)){
            LocalLibrary.Item found=item(parts[2]);if(found==null)return error(404,"资源不存在");db.addTag(found.uri,body(data).optString("name"));return json(200,new JSONObject().put("ok",true));
        }
        if(parts.length==5&&"resources".equals(parts[1])&&"tags".equals(parts[3])&&"DELETE".equals(method)){
            LocalLibrary.Item found=item(parts[2]);if(found==null)return error(404,"资源不存在");String tag=parts[4].startsWith("tag-")?decodeID(parts[4].substring(4)):parts[4];if(tag.isEmpty())return error(400,"标签 ID 无效");db.removeTag(found.uri,tag);return json(200,new JSONObject().put("ok",true));
        }
        if(parts.length==2&&"folders".equals(parts[1])&&"POST".equals(method)){
            String name=body(data).getString("name"),id=db.createFolder(name);return json(201,new JSONObject().put("id",id).put("name",name).put("count",0).put("createdAt",""));
        }
        if(parts.length==3&&"folders".equals(parts[1])&&"PUT".equals(method)){
            String name=body(data).getString("name");db.renameFolder(parts[2],name);return json(200,new JSONObject().put("id",parts[2]).put("name",name).put("count",0).put("createdAt",""));
        }
        if(parts.length==3&&"folders".equals(parts[1])&&"DELETE".equals(method)){db.removeFolder(parts[2]);return json(200,new JSONObject().put("ok",true));}
        if((parts.length==2||parts.length==3)&&"collections".equals(parts[1])&&("POST".equals(method)||"PUT".equals(method))){
            JSONObject input=body(data),f=input.getJSONObject("filters");f.put("rootUri",f.optString("rootId"));
            LocalFilters filters=LocalFilters.parse(f.toString());String name=input.getString("name"),id=db.saveCollection(parts.length==3?parts[2]:null,name,filters);
            return json(parts.length==2?201:200,new JSONObject().put("id",id).put("name",name).put("filters",input.getJSONObject("filters")).put("createdAt",""));
        }
        if(parts.length==3&&"collections".equals(parts[1])&&"DELETE".equals(method)){db.removeCollection(parts[2]);return json(200,new JSONObject().put("ok",true));}
        return error(404,"此节点不支持该操作");
    }
}
