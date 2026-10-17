package com.myliba.android;

import android.content.SharedPreferences;
import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;

/** One immutable server identity per client: requests never migrate between hosts. */
final class RemoteClient {
    final String base;
    private volatile String cookie;
    private final SharedPreferences prefs;
    static final class Failure extends IOException {
        final int status;
        Failure(int status,String message){super(message);this.status=status;}
    }
    RemoteClient(String base,SharedPreferences prefs){this.base=base;this.prefs=prefs;this.cookie=base.equals(prefs.getString("server_url",""))?prefs.getString("server_cookie",""):"";}
    static String normalize(String input) throws Exception {
        URI u=new URI(input.trim());
        if(!"https".equalsIgnoreCase(u.getScheme())||u.getHost()==null||u.getUserInfo()!=null||u.getQuery()!=null||u.getFragment()!=null||!(u.getPath()==null||u.getPath().isEmpty()||"/".equals(u.getPath()))) throw new IllegalArgumentException("请填写 HTTPS 站点根地址，例如 https://library.example.com");
        return new URI("https",null,u.getHost(),u.getPort()==443?-1:u.getPort(),null,null,null).toString();
    }
    String cookie(){return cookie;}
    void forget(){cookie="";if(base.equals(prefs.getString("server_url","")))prefs.edit().remove("server_cookie").apply();}
    JSONObject json(String method,String path,JSONObject body)throws Exception{
        byte[] bytes=bytes(method,path,body,8*1024*1024);return bytes.length==0?new JSONObject():new JSONObject(new String(bytes,StandardCharsets.UTF_8));
    }
    JSONArray array(String path)throws Exception{
        String s=new String(bytes("GET",path,null,8*1024*1024),StandardCharsets.UTF_8);
        if(s.trim().startsWith("["))return new JSONArray(s);
        JSONArray list=new JSONObject(s).optJSONArray("items");return list==null?new JSONArray():list;
    }
    byte[] bytes(String method,String path,JSONObject body,int max)throws Exception{
        HttpURLConnection c=(HttpURLConnection)new URL(base+path).openConnection();
        try{
            c.setRequestMethod(method);c.setConnectTimeout(12000);c.setReadTimeout(30000);c.setInstanceFollowRedirects(false);c.setUseCaches(false);
            c.setRequestProperty("Origin",base);c.setRequestProperty("Referer",base+"/");c.setRequestProperty("Accept","application/json, image/*");
            if(!cookie.isEmpty())c.setRequestProperty("Cookie",cookie);
            if(body!=null){c.setDoOutput(true);c.setRequestProperty("Content-Type","application/json; charset=utf-8");try(OutputStream out=c.getOutputStream()){out.write(body.toString().getBytes(StandardCharsets.UTF_8));}}
            int status=c.getResponseCode();String set=c.getHeaderField("Set-Cookie");
            if(set!=null&&!set.isEmpty()){cookie=set.split(";",2)[0];if(base.equals(prefs.getString("server_url","")))prefs.edit().putString("server_cookie",cookie).apply();}
            InputStream stream=status>=400?c.getErrorStream():c.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream();
            if(stream!=null)try(InputStream in=stream){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){if(out.size()+n>max)throw new IOException("响应内容过大，请下载后查看");out.write(buffer,0,n);}}
            byte[] data=out.toByteArray();if(status<200||status>=300){String message="服务端请求失败（"+status+"）";try{message=new JSONObject(new String(data,StandardCharsets.UTF_8)).optString("error",message);}catch(JSONException ignored){}if(status==401)forget();throw new Failure(status,message);}return data;
        }finally{c.disconnect();}
    }
    static JSONObject object(Object... pairs){JSONObject o=new JSONObject();try{for(int i=0;i<pairs.length;i+=2)o.put((String)pairs[i],pairs[i+1]);}catch(JSONException e){throw new IllegalArgumentException(e);}return o;}
    static String enc(String s){try{return URLEncoder.encode(s==null?"":s,"UTF-8");}catch(Exception e){throw new IllegalArgumentException(e);}}
}
