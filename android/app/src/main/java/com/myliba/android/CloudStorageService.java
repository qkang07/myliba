package com.myliba.android;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.os.*;
import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.*;
import java.util.concurrent.*;

/** User-started, app-private encrypted block node. TLS terminates at its configured HTTPS gateway. */
public final class CloudStorageService extends Service {
    static final String PREFS="cloud_storage";
    static volatile boolean running;
    static volatile String status="未启动";
    static volatile long blockCount,catalogCount,capacityBytes,availableBytes;
    static volatile String activeBackend="disk";
    static volatile boolean relayConnected;
    private final ExecutorService workers=Executors.newFixedThreadPool(4);
    private ServerSocket listener;
    private volatile boolean stopped;
    private String token,hub,publicUrl,relayUrl,nodeId,backend;
    private CloudRelayClient relayClient;
    private CloudOpenListBackend openlist;
    private File root;
    private int port;

    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("cloud-storage","Myliba 云层存储",NotificationManager.IMPORTANCE_LOW));}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(running)return START_STICKY;
        android.content.SharedPreferences p=getSharedPreferences(PREFS,MODE_PRIVATE);
        try{token=CloudSecretStore.read(p,"token");}catch(Exception ex){status="云层令牌读取失败";stopSelf();return START_NOT_STICKY;}
        hub=p.getString("hub","");publicUrl=p.getString("url","");relayUrl=p.getString("relay","");backend=p.getString("backend","disk");port=p.getInt("port",4319);
        activeBackend=backend;
        if(token.length()<32||token.contains("\r")||token.contains("\n")||!hub.startsWith("https://")||(!publicUrl.startsWith("https://")&&!relayUrl.startsWith("https://"))||port<1||port>65535||!(backend.equals("disk")||backend.equals("openlist"))){status="配置无效";stopSelf();return START_NOT_STICKY;}
        root=new File(getFilesDir(),"cloud-node");new File(root,"blocks").mkdirs();new File(root,"catalog").mkdirs();
        try{
            if(backend.equals("openlist"))openlist=new CloudOpenListBackend(p.getString("openlist_url",""),p.getString("openlist_mount",""),p.getString("openlist_user",""),CloudSecretStore.read(p,"openlist_password"));
            String identityFile=backend.equals("disk")?"node-id":"node-id-openlist-"+hash((p.getString("openlist_url","")+"/"+p.getString("openlist_mount","")).getBytes(StandardCharsets.UTF_8)).substring(0,16);
            File idFile=new File(root,identityFile);
            if(idFile.exists())nodeId=new String(read(idFile,128),StandardCharsets.US_ASCII).trim();
            else{byte[] random=new byte[16];new java.security.SecureRandom().nextBytes(random);nodeId=hex(random);write(idFile,nodeId.getBytes(StandardCharsets.US_ASCII));}
            if(!nodeId.matches("[0-9a-f]{32}"))throw new IOException("节点 ID 无效");
            if(publicUrl.isEmpty())publicUrl=relayUrl+"/cloud/relay/"+nodeId;
            listener=new ServerSocket();listener.setReuseAddress(true);listener.bind(new InetSocketAddress(publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)?"127.0.0.1":"0.0.0.0",port));
        }catch(Exception ex){status="存储节点启动失败："+ex.getMessage();stopSelf();return START_NOT_STICKY;}
        Intent open=new Intent(this,MainActivity.class);
        PendingIntent click=PendingIntent.getActivity(this,0,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        Notification notification=new Notification.Builder(this,"cloud-storage").setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle("Myliba 云层存储节点运行中").setContentText((openlist==null?"本机磁盘":"OpenList 云盘")+" · 只存储加密文件块").setContentIntent(click).setOngoing(true).build();
        if(Build.VERSION.SDK_INT>=34)startForeground(4319,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4319,notification);
        stopped=false;running=true;status="等待 Hub 注册";
        workers.execute(()->{while(!stopped){try{Socket socket=listener.accept();workers.execute(()->serve(socket));}catch(IOException ex){if(!stopped)break;}}});
        workers.execute(()->{while(!stopped){try{syncHub();status="已向 Hub 注册";}catch(Exception ex){status="Hub 同步失败："+ex.getMessage();}try{Thread.sleep(20000);}catch(InterruptedException ex){break;}}});
        if(publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)){relayClient=new CloudRelayClient(relayUrl,nodeId,token,port);new Thread(()->relayClient.run(()->stopped),"cloud-relay").start();}
        return START_STICKY;
    }

    @Override public void onDestroy(){stopped=true;running=false;relayConnected=false;status="已停止";if(relayClient!=null)relayClient.close();try{if(listener!=null)listener.close();}catch(IOException ignored){}workers.shutdownNow();super.onDestroy();}
    @Override public IBinder onBind(Intent intent){return null;}

    private static String hex(byte[] data){StringBuilder b=new StringBuilder(data.length*2);for(byte value:data)b.append(String.format(Locale.ROOT,"%02x",value&255));return b.toString();}
    private static String hash(byte[] data)throws Exception{return hex(MessageDigest.getInstance("SHA-256").digest(data));}
    private static boolean validId(String value){return value!=null&&value.matches("[0-9a-f]{64}");}
    private File object(String kind,String id)throws Exception{if(!("blocks".equals(kind)||"catalog".equals(kind))||!validId(id))throw new IOException("invalid id");File dir=new File(new File(root,kind),id.substring(0,2));return new File(dir,id);}
    private static byte[] read(File file,int max)throws IOException{if(file.length()>max)throw new IOException("too large");try(FileInputStream in=new FileInputStream(file);ByteArrayOutputStream out=new ByteArrayOutputStream()){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>max)throw new IOException("too large");}return out.toByteArray();}}
    private static void write(File file,byte[] data)throws IOException{file.getParentFile().mkdirs();File tmp=File.createTempFile(".cloud-",".tmp",file.getParentFile());try(FileOutputStream out=new FileOutputStream(tmp)){out.write(data);out.getFD().sync();}if(!tmp.renameTo(file)){tmp.delete();throw new IOException("rename failed");}}
    private void put(String kind,String id,byte[] data)throws Exception{
        int max="blocks".equals(kind)?(4<<20)+64:1<<20;
        if(data.length==0||data.length>max||!hash(data).equals(id))throw new IOException("hash mismatch");
        if("catalog".equals(kind)){JSONObject record=new JSONObject(new String(data,StandardCharsets.UTF_8));if(!validId(record.optString("libraryId"))||!record.has("sealed")||!record.has("blockIds"))throw new IOException("invalid catalog");}
        if(openlist!=null){openlist.put(kind,id,data);return;}
        File file=object(kind,id);if(file.exists()&&hash(read(file,max)).equals(id))return;write(file,data);
    }
    private byte[] get(String kind,String id)throws Exception{
        int max="blocks".equals(kind)?(4<<20)+64:1<<20;
        byte[] data=openlist!=null?openlist.get(kind,id,max):read(object(kind,id),max);
        if(!hash(data).equals(id))throw new IOException("cloud object hash mismatch");
        return data;
    }
    private List<String> list(String kind)throws Exception{if(openlist!=null)return openlist.list(kind);List<String> ids=new ArrayList<>();File[] dirs=new File(root,kind).listFiles();if(dirs!=null)for(File dir:dirs){File[] files=dir.listFiles();if(files!=null)for(File file:files)if(validId(file.getName()))ids.add(file.getName());}Collections.sort(ids);return ids;}
    private static String line(InputStream in)throws IOException{ByteArrayOutputStream out=new ByteArrayOutputStream();int ch;while((ch=in.read())!=-1){if(ch=='\n')break;if(ch!='\r')out.write(ch);if(out.size()>8192)throw new IOException("header too long");}if(ch==-1&&out.size()==0)throw new EOFException();return out.toString("UTF-8");}
    private static byte[] exact(InputStream in,int length)throws IOException{byte[] data=new byte[length];int offset=0;while(offset<length){int n=in.read(data,offset,length-offset);if(n<0)throw new EOFException();offset+=n;}return data;}
    private static void respond(OutputStream out,int status,String type,byte[] body,boolean head)throws IOException{
        String reason=status==200?"OK":status==204?"No Content":status==400?"Bad Request":status==401?"Unauthorized":status==404?"Not Found":"Error";
        out.write(("HTTP/1.1 "+status+" "+reason+"\r\nContent-Type: "+type+"\r\nContent-Length: "+body.length+"\r\nCache-Control: private\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        if(!head)out.write(body);out.flush();
    }
    private static byte[] json(JSONObject value){return value.toString().getBytes(StandardCharsets.UTF_8);}
    private void serve(Socket socket){
        try(Socket client=socket){client.setSoTimeout(30000);InputStream in=new BufferedInputStream(client.getInputStream());OutputStream out=client.getOutputStream();
            String[] first=line(in).split(" ");if(first.length<2){respond(out,400,"text/plain",new byte[0],false);return;}
            String method=first[0];URI uri=new URI(first[1]);String route=uri.getPath();Map<String,String> headers=new HashMap<>();String header;int total=0;
            while(!(header=line(in)).isEmpty()){total+=header.length();if(total>16384)throw new IOException("headers too large");int colon=header.indexOf(':');if(colon>0)headers.put(header.substring(0,colon).toLowerCase(Locale.ROOT),header.substring(colon+1).trim());}
            if(!MessageDigest.isEqual(("Bearer "+token).getBytes(StandardCharsets.UTF_8),headers.getOrDefault("authorization","").getBytes(StandardCharsets.UTF_8))){respond(out,401,"text/plain",new byte[0],false);return;}
            if("GET".equals(method)&&"/cloud/catalog".equals(route)){try{respond(out,200,"application/json",json(new JSONObject().put("records",new JSONArray(list("catalog")))),false);}catch(Exception ex){respond(out,503,"text/plain",new byte[0],false);}return;}
            if("GET".equals(method)&&"/cloud/inventory".equals(route)){
                String after="";if(uri.getRawQuery()!=null)for(String p:uri.getRawQuery().split("&"))if(p.startsWith("after="))after=URLDecoder.decode(p.substring(6),"UTF-8");
                if(!after.isEmpty()&&!validId(after)){respond(out,400,"text/plain",new byte[0],false);return;}
                List<String> ids;try{ids=list("blocks");}catch(Exception ex){respond(out,503,"text/plain",new byte[0],false);return;}int start=0;while(start<ids.size()&&ids.get(start).compareTo(after)<=0)start++;int end=Math.min(start+1000,ids.size());
                JSONArray page=new JSONArray();for(int i=start;i<end;i++)page.put(ids.get(i));String next=end<ids.size()?ids.get(end-1):"";
                respond(out,200,"application/json",json(new JSONObject().put("blocks",page).put("next",next)),false);return;
            }
            String[] parts=route.split("/");if(parts.length!=4||!"cloud".equals(parts[1])||!validId(parts[3])){respond(out,404,"text/plain",new byte[0],false);return;}
            String kind=parts[2],id=parts[3];if(!("blocks".equals(kind)||"catalog".equals(kind))){respond(out,404,"text/plain",new byte[0],false);return;}int max="blocks".equals(kind)?(4<<20)+64:1<<20;
            if("PUT".equals(method)){int length=Integer.parseInt(headers.getOrDefault("content-length","-1"));if(length<1||length>max){respond(out,400,"text/plain",new byte[0],false);return;}byte[] data=exact(in,length);if(!hash(data).equals(id)){respond(out,400,"text/plain",new byte[0],false);return;}try{put(kind,id,data);respond(out,204,"text/plain",new byte[0],false);}catch(Exception ex){respond(out,503,"text/plain",new byte[0],false);}return;}
            if("GET".equals(method)||"HEAD".equals(method)){try{respond(out,200,"application/octet-stream",get(kind,id),"HEAD".equals(method));}catch(FileNotFoundException ex){respond(out,404,"text/plain",new byte[0],false);}catch(Exception ex){respond(out,503,"text/plain",new byte[0],false);}return;}
            respond(out,400,"text/plain",new byte[0],false);
        }catch(Exception ignored){}
    }
    private HttpURLConnection hubRequest(String method,String path,byte[] data)throws Exception{
        String base=hub.replaceFirst("^wss:","https:").replaceFirst("^ws:","http:").replaceAll("/node/?$","");
        HttpURLConnection c=(HttpURLConnection)new URL(base+path).openConnection();c.setRequestMethod(method);c.setConnectTimeout(10000);c.setReadTimeout(30000);c.setInstanceFollowRedirects(false);c.setRequestProperty("Authorization","Bearer "+token);
        if(data!=null){c.setDoOutput(true);c.setRequestProperty("Content-Type","application/json");c.setFixedLengthStreamingMode(data.length);try(OutputStream out=c.getOutputStream()){out.write(data);}}return c;
    }
    private void syncHub()throws Exception{
        String advertised=publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)&&(relayClient==null||!relayClient.isConnected())?"":publicUrl;
        JSONObject node=new JSONObject().put("id",nodeId).put("endpoint",advertised).put("relayed",publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)).put("role","storage");
        List<String> blocks=list("blocks"),catalog=list("catalog");
        blockCount=blocks.size();catalogCount=catalog.size();capacityBytes=openlist==null?root.getTotalSpace():0;availableBytes=openlist==null?root.getUsableSpace():0;
        node.put("backend",backend).put("statsReady",true).put("blockCount",blocks.size()).put("catalogCount",catalog.size());
        if(openlist==null){long blockBytes=0;for(String id:blocks)blockBytes+=object("blocks",id).length();node.put("blockBytes",blockBytes).put("capacityBytes",root.getTotalSpace()).put("availableBytes",root.getUsableSpace());}
        HttpURLConnection heartbeat=hubRequest("POST","/cloud/nodes/heartbeat",json(node));try{if(heartbeat.getResponseCode()!=204)throw new IOException("注册 HTTP "+heartbeat.getResponseCode());}finally{heartbeat.disconnect();}
        HttpURLConnection listing=hubRequest("GET","/cloud/catalog",null);JSONArray remote;
        try{if(listing.getResponseCode()!=200)throw new IOException("索引 HTTP "+listing.getResponseCode());ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[4096];int n;try(InputStream in=listing.getInputStream()){while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>16<<20)throw new IOException("catalog too large");}}remote=new JSONObject(out.toString("UTF-8")).getJSONArray("records");}finally{listing.disconnect();}
        Set<String> known=new HashSet<>();Set<String> localCatalog=new HashSet<>(catalog);for(int i=0;i<remote.length();i++){String id=remote.optString(i);if(!validId(id))continue;known.add(id);if(localCatalog.contains(id))continue;
            HttpURLConnection get=hubRequest("GET","/cloud/catalog/"+id,null);try{if(get.getResponseCode()!=200)continue;ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[4096];int n;try(InputStream in=get.getInputStream()){while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>1<<20)throw new IOException("catalog too large");}}put("catalog",id,out.toByteArray());}finally{get.disconnect();}
        }
        for(String id:catalog){if(known.contains(id))continue;byte[] data=get("catalog",id);HttpURLConnection put=hubRequest("PUT","/cloud/catalog/"+id,data);try{if(put.getResponseCode()!=204)throw new IOException("索引上传失败：HTTP "+put.getResponseCode());}finally{put.disconnect();}}
    }
}
