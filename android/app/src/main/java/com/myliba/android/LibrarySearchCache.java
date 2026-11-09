package com.myliba.android;

import android.content.Context;
import android.content.SharedPreferences;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.Map;
import org.json.JSONObject;

/** A small private cache of first-page query metadata for disconnected browsing. */
final class LibrarySearchCache {
    private static final String PREFS="library_search_cache";
    private static final int MAX_ENTRIES=8;
    private final SharedPreferences prefs;

    LibrarySearchCache(Context context){prefs=context.getApplicationContext().getSharedPreferences(PREFS,Context.MODE_PRIVATE);}

    private String key(String base,String node,String path){
        try{
            byte[] bytes=MessageDigest.getInstance("SHA-256").digest((base+'\n'+node+'\n'+path).getBytes(StandardCharsets.UTF_8));
            StringBuilder result=new StringBuilder("query_");
            for(byte value:bytes)result.append(Character.forDigit((value>>>4)&15,16)).append(Character.forDigit(value&15,16));
            return result.toString();
        }catch(Exception ex){throw new IllegalStateException(ex);}
    }

    void save(String base,String node,String path,JSONObject response){
        String id=key(base,node,path);
        JSONObject value=new JSONObject();
        try{
            value.put("cachedAt",Instant.now().toString());
            value.put("items",response.optJSONArray("items"));
            value.put("total",response.optInt("total"));
            value.put("facets",response.optJSONObject("facets"));
            value.put("coverage",response.optJSONObject("coverage"));
        }catch(Exception ex){return;}
        SharedPreferences.Editor edit=prefs.edit();
        Map<String,?> all=prefs.getAll();
        if(!all.containsKey(id)&&all.size()>=MAX_ENTRIES){
            String oldest=null;String when="";
            for(Map.Entry<String,?> entry:all.entrySet()){
                try{String at=new JSONObject(String.valueOf(entry.getValue())).optString("cachedAt");if(oldest==null||at.compareTo(when)<0){oldest=entry.getKey();when=at;}}
                catch(Exception ignored){}
            }
            if(oldest!=null)edit.remove(oldest);
        }
        edit.putString(id,value.toString()).apply();
    }

    JSONObject load(String base,String node,String path){
        String raw=prefs.getString(key(base,node,path),"");
        if(raw.isEmpty())return null;
        try{return new JSONObject(raw);}catch(Exception ex){return null;}
    }
}
