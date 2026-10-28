package com.myliba.android;

import org.json.*;
import java.util.*;
import java.time.Instant;

/** UI-neutral adapter. Phone and Hub data share navigation and interaction behavior. */
final class LibraryStore {
    final LocalLibrary local; final RemoteClient client; final boolean remote;
    LibraryStore(LocalLibrary local,RemoteClient client){this.local=local;this.client=client;remote=client!=null;}
    static final class Entry {
        String id,name,path,mime,kind,folderId;long size,modified;boolean available=true;
        List<String> tags=new ArrayList<>();
    }
    static final class Group {
        final String id,name; final int count; LocalFilters filters;
        Group(String id,String name,int count){this.id=id;this.name=name;this.count=count;}
    }
    static final class Facet {
        final String key,name;final int count;
        Facet(String key,String name,int count){this.key=key;this.name=name;this.count=count;}
    }
    static final class Meta {
        List<Group> roots=new ArrayList<>(),folders=new ArrayList<>(),collections=new ArrayList<>();
        List<Facet> tags=new ArrayList<>(),attributes=new ArrayList<>();int total,unfiled;boolean online=true;
    }
    static final class Page { List<Entry> items=new ArrayList<>();int total; }
    Meta metadata(LocalFilters filters)throws Exception{
        Meta m=new Meta();
        String folder=filters.folderId;
        if(!remote){
            for(LocalLibrary.Root r:local.roots()){m.roots.add(new Group(r.uri,r.name,r.count));m.total+=r.count;}
            for(LocalLibrary.Folder f:local.folders())m.folders.add(new Group(f.id,f.name,f.count));
            for(LocalLibrary.Collection c:local.collections()){Group g=new Group(c.id,c.name,0);g.filters=c.filters;m.collections.add(g);}
            for(LocalLibrary.Attribute t:local.tagCatalog(folder))m.tags.add(new Facet(t.key,t.label,t.count));
            for(LocalLibrary.Attribute t:local.attributeCatalog(filters))m.attributes.add(new Facet(t.key,t.label,t.count));
            int grouped=0;for(Group g:m.folders)grouped+=g.count;m.unfiled=m.total-grouped;
        }else{
            JSONObject session=client.json("GET","/api/session",null);m.online=session.optBoolean("nodeOnline",false);
            JSONArray roots=client.array("/api/roots");for(int i=0;i<roots.length();i++){JSONObject r=roots.getJSONObject(i);int n=r.optInt("count");m.roots.add(new Group(r.getString("id"),r.optString("path"),n));m.total+=n;}
            JSONObject fs=client.json("GET","/api/folders",null);m.unfiled=fs.optInt("unfiledCount");JSONArray folders=fs.optJSONArray("items");if(folders!=null)for(int i=0;i<folders.length();i++){JSONObject f=folders.getJSONObject(i);m.folders.add(new Group(f.getString("id"),f.getString("name"),f.optInt("count")));}
            JSONArray cs=client.array("/api/collections");for(int i=0;i<cs.length();i++){JSONObject c=cs.getJSONObject(i);Group g=new Group(c.getString("id"),c.getString("name"),0);g.filters=fromRemote(c.getJSONObject("filters"));m.collections.add(g);}
            JSONArray ts=client.array("/api/tags?folderId="+RemoteClient.enc(folder));for(int i=0;i<ts.length();i++){JSONObject t=ts.getJSONObject(i);m.tags.add(new Facet(t.getString("name"),t.getString("name"),t.optInt("count")));}
            JSONArray as=client.array(path(filters,"/api/attribute-tags",0,0));for(int i=0;i<as.length();i++){JSONObject t=as.getJSONObject(i);m.attributes.add(new Facet(t.getString("key"),t.getString("label"),t.optInt("count")));}
        }return m;
    }
    Page page(LocalFilters f,int offset)throws Exception{
        Page page=new Page();if(!remote){LocalLibrary.Page p=local.search(f,48,offset);page.total=p.total;for(LocalLibrary.Item i:p.items){Entry e=new Entry();e.id=i.uri;e.name=i.name;e.path=i.path;e.mime=i.name.toLowerCase(Locale.ROOT).endsWith(".arw")?"image/x-sony-arw":i.mime;e.kind=kind(i.name,i.mime);e.size=i.size;e.modified=i.modified;e.folderId=i.folderId;e.tags=local.tags(i.uri);page.items.add(e);}}
        else{JSONObject p=client.json("GET",path(f,"/api/resources",48,offset),null);page.total=p.getInt("total");JSONArray items=p.getJSONArray("items");for(int i=0;i<items.length();i++)page.items.add(entry(items.getJSONObject(i)));}return page;
    }
    Entry detail(Entry value)throws Exception{
        if(remote)return entry(client.json("GET","/api/resources/"+RemoteClient.enc(value.id),null));
        value.tags=local.tags(value.id);return value;
    }
    List<String> matching(LocalFilters f)throws Exception{
        if(!remote)return local.matchingUris(f);List<String> ids=new ArrayList<>();int offset=0;
        while(true){JSONObject p=client.json("GET",path(f,"/api/resources/selection",1000,offset),null);JSONArray a=p.getJSONArray("items");for(int i=0;i<a.length();i++)ids.add(a.getJSONObject(i).getString("id"));offset+=a.length();if(offset>=p.getInt("total")||a.length()==0)break;}return ids;
    }
    void addTag(List<String> ids,String name)throws Exception{
        String clean=clean(name,remote?40:64);if(!remote){local.addTagToItems(ids,clean);return;}
        for(int i=0;i<ids.size();i+=5000)client.json("POST","/api/resources/tags",RemoteClient.object("ids",new JSONArray(ids.subList(i,Math.min(i+5000,ids.size()))),"name",clean));
    }
    void removeTag(List<String> ids,String name)throws Exception{
        if(!remote){local.removeTagFromItems(ids,name);return;}String tagId=null;JSONArray tags=client.array("/api/tags?folderId=all");for(int i=0;i<tags.length();i++){JSONObject t=tags.getJSONObject(i);if(name.equals(t.getString("name")))tagId=t.getString("id");}if(tagId==null)return;
        for(String id:ids)client.json("DELETE","/api/resources/"+RemoteClient.enc(id)+"/tags/"+RemoteClient.enc(tagId),null);
    }
    void move(List<String> ids,String folder)throws Exception{
        if(!remote){local.moveItems(ids,folder);return;}for(int i=0;i<ids.size();i+=1000)client.json("PUT","/api/resources/folder",RemoteClient.object("ids",new JSONArray(ids.subList(i,Math.min(i+1000,ids.size()))),"folderId",folder));
    }
    void saveFolder(String id,String name)throws Exception{
        if(!remote){if(id==null)local.createFolder(name);else local.renameFolder(id,name);return;}
        client.json(id==null?"POST":"PUT","/api/folders"+(id==null?"":"/"+RemoteClient.enc(id)),RemoteClient.object("name",clean(name,64)));
    }
    void deleteFolder(String id)throws Exception{if(!remote)local.removeFolder(id);else client.json("DELETE","/api/folders/"+RemoteClient.enc(id),null);}
    void saveCollection(String id,String name,LocalFilters f)throws Exception{
        if(!remote){local.saveCollection(id,name,f);return;}
        client.json(id==null?"POST":"PUT","/api/collections"+(id==null?"":"/"+RemoteClient.enc(id)),RemoteClient.object("name",clean(name,64),"filters",remoteFilters(f)));
    }
    void deleteCollection(String id)throws Exception{if(!remote)local.removeCollection(id);else client.json("DELETE","/api/collections/"+RemoteClient.enc(id),null);}
    static String clean(String s,int max){String v=s.trim();if(v.isEmpty()||v.length()>max)throw new IllegalArgumentException("请输入 1–"+max+" 个字符");return v;}
    static JSONObject remoteFilters(LocalFilters f){return RemoteClient.object("query",f.query,"tags",new JSONArray(f.tags),"attributes",new JSONArray(f.attributes),"rootId",f.rootUri,"folderId",f.folderId,"unorganized",f.unorganized,"sort",f.sort,"kind","");}
    static LocalFilters fromRemote(JSONObject value)throws Exception{JSONObject copy=new JSONObject(value.toString());copy.put("rootUri",copy.optString("rootId"));LocalFilters f=LocalFilters.parse(copy.toString());String kind=copy.optString("kind");if(!kind.isEmpty()&&!f.attributes.contains("type:"+kind))f.attributes.add("type:"+kind);return f;}
    static String path(LocalFilters f,String endpoint,int limit,int offset){StringBuilder s=new StringBuilder(endpoint+"?q="+RemoteClient.enc(f.query)+"&folderId="+RemoteClient.enc(f.folderId)+"&rootId="+RemoteClient.enc(f.rootUri)+"&sort="+RemoteClient.enc(f.sort)+"&unorganized="+(f.unorganized?"1":"")+"&limit="+limit+"&offset="+offset);for(String t:f.tags)s.append("&tag=").append(RemoteClient.enc(t));for(String t:f.attributes)s.append("&attribute=").append(RemoteClient.enc(t));return s.toString();}
    static Entry entry(JSONObject o)throws Exception{Entry e=new Entry();e.id=o.getString("id");e.name=o.getString("name");e.path=o.optString("path");e.kind=o.optString("kind","file");e.size=o.optLong("size");e.folderId=o.optString("folderId");e.available=o.optBoolean("available",true);try{e.modified=Instant.parse(o.optString("modified")).toEpochMilli();}catch(Exception ignored){}int dot=e.name.lastIndexOf('.');String ext=dot<0?"":e.name.substring(dot+1);e.mime=android.webkit.MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext.toLowerCase(Locale.ROOT));if(e.name.toLowerCase(Locale.ROOT).endsWith(".arw"))e.mime="image/x-sony-arw";else if(e.mime==null)e.mime="application/octet-stream";JSONArray ts=o.optJSONArray("tags");if(ts!=null)for(int i=0;i<ts.length();i++)e.tags.add(ts.getString(i));return e;}
    static String kind(String name,String mime){if(name.toLowerCase(Locale.ROOT).endsWith(".arw"))return "image";if(mime!=null){if(mime.startsWith("image/"))return "image";if(mime.startsWith("video/"))return "video";if(mime.startsWith("audio/"))return "audio";}return name.toLowerCase(Locale.ROOT).endsWith(".pdf")?"document":"file";}
}
