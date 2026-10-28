package com.myliba.android;

import android.content.SharedPreferences;
import android.content.Context;
import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;

/** One immutable server identity per client: requests never migrate between hosts. */
final class RemoteClient {
    final String base;
    private volatile String cookie;
    private final SharedPreferences prefs;
    private final Context context;
    private volatile RemoteRTC rtc;
    private volatile boolean rtcConfigured;
    volatile JSONArray nodes=new JSONArray();
    volatile String selectedNodeId="";
    volatile boolean owner;
    static final class Failure extends IOException {
        final int status;
        Failure(int status,String message){super(message);this.status=status;}
    }
    RemoteClient(String base,SharedPreferences prefs,Context context){this.base=base;this.prefs=prefs;this.context=context.getApplicationContext();
        String saved="";if(base.equals(prefs.getString("server_url","")))try{saved=CloudSecretStore.read(prefs,"server_cookie");}catch(Exception ignored){}
        this.cookie=saved;rtc=new RemoteRTC(this.context,base);
    }
    static String normalize(String input) throws Exception {
        URI u=new URI(input.trim());
        if(!"https".equalsIgnoreCase(u.getScheme())||u.getHost()==null||u.getUserInfo()!=null||u.getQuery()!=null||u.getFragment()!=null||!(u.getPath()==null||u.getPath().isEmpty()||"/".equals(u.getPath()))) throw new IllegalArgumentException("请填写 HTTPS 站点根地址，例如 https://library.example.com");
        return new URI("https",null,u.getHost(),u.getPort()==443?-1:u.getPort(),null,null,null).toString();
    }
    String cookie(){return cookie;}
    boolean paired(){return base.equals(prefs.getString("device_hub",""))&&!prefs.getString("device_id","").isEmpty();}
    void forget(){cookie="";rtcConfigured=false;rtc.close();rtc=new RemoteRTC(context,base);if(base.equals(prefs.getString("server_url","")))prefs.edit().remove("server_cookie").apply();}
    void unpair(){forget();if(base.equals(prefs.getString("device_hub","")))prefs.edit().remove("device_hub").remove("device_id").remove("device_token").apply();}
    void pairedCredential(String id,String token)throws Exception{
        prefs.edit().putString("device_id",id).putString("device_hub",base).putString("device_token",CloudSecretStore.protect(token)).apply();
    }
    private JSONObject deviceSession()throws Exception{
        if(!base.equals(prefs.getString("device_hub","")))throw new Failure(401,"此 Hub 尚未授权该设备");
        String id=prefs.getString("device_id",""),token=CloudSecretStore.read(prefs,"device_token");
        if(id.isEmpty()||token.isEmpty())throw new Failure(401,"设备尚未授权");
        rtcConfigured=false;rtc.close();rtc=new RemoteRTC(context,base);
        try{
            JSONObject session=json("POST","/api/session/device",object("id",id,"token",token));
            JSONObject status=new JSONObject(new String(bytes("GET","/api/session",null,8*1024*1024),StandardCharsets.UTF_8));
            configureSession(status);return session;
        }
        catch(Failure ex){if(ex.status==401)throw new Failure(401,"Hub 拒绝设备凭据。请检查 Hub 授权数据是否恢复，或用主人密码重新授权；本机设备凭据已保留。");throw ex;}
    }
    JSONObject json(String method,String path,JSONObject body)throws Exception{
        byte[] bytes;
        try{bytes=bytes(method,path,body,8*1024*1024);}
        catch(Failure ex){
            if(ex.status!=401||"/api/session/device".equals(path)||("/api/session".equals(path)&&"POST".equals(method)))throw ex;
            deviceSession();bytes=bytes(method,path,body,8*1024*1024);
        }
        JSONObject result=bytes.length==0?new JSONObject():new JSONObject(new String(bytes,StandardCharsets.UTF_8));
        if("/api/session".equals(path)&&("GET".equals(method)||"POST".equals(method)))configureSession(result);
        return result;
    }
    private void configureSession(JSONObject result){
        nodes=result.optJSONArray("nodes")==null?new JSONArray():result.optJSONArray("nodes");selectedNodeId=result.optString("selectedNodeId");owner=result.optBoolean("owner",false);
        rtc.configure(cookie,result.optJSONArray("iceServers"));rtcConfigured=true;if(result.optBoolean("nodeOnline"))rtc.connectAsync();
    }
    JSONArray array(String path)throws Exception{
        String s=new String(bytes("GET",path,null,8*1024*1024),StandardCharsets.UTF_8);
        if(s.trim().startsWith("["))return new JSONArray(s);
        JSONArray list=new JSONObject(s).optJSONArray("items");return list==null?new JSONArray():list;
    }
    byte[] bytes(String method,String path,JSONObject body,int max)throws Exception{
        if(!path.startsWith("/api/session")&&!path.startsWith("/api/hub/")&&(contentPath(path)||rtc.connected())){
            boolean directRequired=contentPath(path);
            if(directRequired&&!rtcConfigured)json("GET","/api/session",null);
            byte[] payload=body==null?new byte[0]:body.toString().getBytes(StandardCharsets.UTF_8);
            try{
                RemoteRTC.Reply reply=rtc.fetch(method,path,payload,max);
                if(reply.status<200||reply.status>=300)throw failure(reply.status,reply.body);
                return reply.body;
            }catch(Failure ex){if(!"GET".equals(method)||ex.status<500)throw ex;AppLog.info("Remote content falling back to limited Hub relay after RTC HTTP "+ex.status);}
            catch(Exception ex){if(!"GET".equals(method))throw ex;AppLog.info("Remote content falling back to limited Hub relay: "+ex.getClass().getSimpleName());}
        }
        HttpURLConnection c=(HttpURLConnection)new URL(base+path).openConnection();
        try{
            c.setRequestMethod(method);c.setConnectTimeout(12000);c.setReadTimeout(30000);c.setInstanceFollowRedirects(false);c.setUseCaches(false);
            c.setRequestProperty("Origin",base);c.setRequestProperty("Referer",base+"/");c.setRequestProperty("Accept","application/json, image/*");
            if(!cookie.isEmpty())c.setRequestProperty("Cookie",cookie);
            if(body!=null){c.setDoOutput(true);c.setRequestProperty("Content-Type","application/json; charset=utf-8");try(OutputStream out=c.getOutputStream()){out.write(body.toString().getBytes(StandardCharsets.UTF_8));}}
            int status=c.getResponseCode();String set=c.getHeaderField("Set-Cookie");
            if(set!=null&&!set.isEmpty()){cookie=set.split(";",2)[0];if(base.equals(prefs.getString("server_url","")))try{prefs.edit().putString("server_cookie",CloudSecretStore.protect(cookie)).apply();}catch(Exception ex){throw new IOException("无法安全保存登录会话",ex);}}
            InputStream stream=status>=400?c.getErrorStream():c.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream();
            if(stream!=null)try(InputStream in=stream){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){if(out.size()+n>max)throw new IOException("响应内容过大，请下载后查看");out.write(buffer,0,n);}}
            byte[] data=out.toByteArray();if(status<200||status>=300){if(status==401)forget();throw failure(status,data);}return data;
        }finally{c.disconnect();}
    }
    void download(String path,OutputStream out)throws Exception{
        streamFile(path,null,out,(status,headers)->{if(status<200||status>=300)throw new Failure(status,"文件传输失败（"+status+"）");});
    }
    void streamFile(String path,String range,OutputStream out,RemoteRTC.ResponseStart start)throws Exception{
        if(!rtcConfigured)json("GET","/api/session",null);
        try{rtc.ensureConnected();}catch(Exception ex){AppLog.info("Remote media falling back to limited Hub relay: "+ex.getClass().getSimpleName());streamFromHub(path,range,out,start);return;}
        JSONObject headers=new JSONObject();if(range!=null&&!range.isEmpty())headers.put("Range",new JSONArray().put(range));
        final int[] statusCode={0};
        final long[] forwarded={0};
        OutputStream counted=new OutputStream(){
            @Override public void write(int value)throws IOException{if(statusCode[0]<500){out.write(value);forwarded[0]++;}}
            @Override public void write(byte[] data,int offset,int length)throws IOException{if(statusCode[0]<500){out.write(data,offset,length);forwarded[0]+=length;}}
        };
        final boolean[] started={false};
        try{
            RemoteRTC.Reply reply=rtc.stream("GET",path,new byte[0],headers,counted,Long.MAX_VALUE,(status,responseHeadersValue)->{
                statusCode[0]=status;
                if(status<500){started[0]=true;if(start!=null)start.onStart(status,responseHeadersValue);}
            });
            if(reply.status>=500&&!started[0]){AppLog.info("Remote media falling back to limited Hub relay after RTC HTTP "+reply.status);streamFromHub(path,range,out,start);}
            else if(!started[0]&&start!=null)start.onStart(reply.status,reply.headers);
        }
        catch(Exception ex){if(forwarded[0]>0||started[0])throw ex;AppLog.info("Remote media falling back to limited Hub relay after RTC content failure: "+ex.getClass().getSimpleName());streamFromHub(path,range,out,start);}
    }
    private void streamFromHub(String path,String range,OutputStream out,RemoteRTC.ResponseStart start)throws Exception{
        HttpURLConnection c=(HttpURLConnection)new URL(base+path).openConnection();
        try{
            c.setRequestMethod("GET");c.setConnectTimeout(12000);c.setReadTimeout(30000);c.setInstanceFollowRedirects(false);c.setUseCaches(false);
            c.setRequestProperty("Origin",base);c.setRequestProperty("Referer",base+"/");if(!cookie.isEmpty())c.setRequestProperty("Cookie",cookie);
            if(range!=null&&!range.isEmpty())c.setRequestProperty("Range",range);
            int status=c.getResponseCode();if(status<200||status>=300){InputStream error=c.getErrorStream();byte[] data=new byte[0];if(error!=null){byte[] buffer=new byte[8192];int n=error.read(buffer);if(n>0)data=java.util.Arrays.copyOf(buffer,n);error.close();}if(status==401)forget();throw failure(status,data);}
            JSONObject headers=new JSONObject();for(String name:new String[]{"Content-Type","Content-Length","Content-Range","Accept-Ranges"}){String value=c.getHeaderField(name);if(value!=null)headers.put(name,new JSONArray().put(value));}
            if(start!=null)start.onStart(status,headers);
            try(InputStream in=c.getInputStream()){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1)out.write(buffer,0,n);}
        }finally{c.disconnect();}
    }
    JSONObject selectNode(String id)throws Exception{
        JSONObject result=json("PUT","/api/hub/nodes/active",object("id",id));
        rtc.close();rtc=new RemoteRTC(context,base);rtcConfigured=false;json("GET","/api/session",null);return result;
    }
    boolean directConnected(){return rtc.connected();}
    String connectionDetails(){return rtc.connectionSummary();}
    void close(){rtc.close();}
    private static boolean contentPath(String path){
        return path.matches("/api/resources/[^/]+/(file|thumbnail)(\\?.*)?")||path.matches("/api/resources/[^/]+/video/.*")||path.matches("/api/cloud/files/[^/]+/(file|assets/.*|video/.*|versions/.*/file)(\\?.*)?");
    }
    private static Failure failure(int status,byte[] data){String message="服务端请求失败（"+status+"）";try{message=new JSONObject(new String(data,StandardCharsets.UTF_8)).optString("error",message);}catch(JSONException ignored){}return new Failure(status,message);}
    static JSONObject object(Object... pairs){JSONObject o=new JSONObject();try{for(int i=0;i<pairs.length;i+=2)o.put((String)pairs[i],pairs[i+1]);}catch(JSONException e){throw new IllegalArgumentException(e);}return o;}
    static String enc(String s){try{return URLEncoder.encode(s==null?"":s,"UTF-8");}catch(Exception e){throw new IllegalArgumentException(e);}}
}
