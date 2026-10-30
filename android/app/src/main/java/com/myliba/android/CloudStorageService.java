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
import java.util.function.Consumer;

/** User-started, app-private encrypted block node. TLS terminates at its configured HTTPS gateway. */
public final class CloudStorageService extends Service {
    static final String PREFS="cloud_storage";
    static volatile boolean running;
    static volatile String status="未启动";
    static volatile long blockCount,catalogCount,capacityBytes,availableBytes;
    static volatile String activeBackend="disk";
    static volatile boolean relayConnected;
	static final ConcurrentHashMap<String,JSONObject> peerLinks=new ConcurrentHashMap<>();
	static final ConcurrentHashMap<String,JSONObject> peerNodes=new ConcurrentHashMap<>();
	static final ConcurrentHashMap<String,String> peerPairs=new ConcurrentHashMap<>();
	private static volatile CloudStorageService instance;
	private final Set<String> probingPeers=ConcurrentHashMap.newKeySet();
    private final ExecutorService workers=Executors.newFixedThreadPool(12);
    private ServerSocket listener;
	private CloudLan lan;
	private final ExecutorService probes=Executors.newFixedThreadPool(4);
    private volatile boolean stopped;
    private String token,hub,publicUrl,relayUrl,nodeId,backend;
    private CloudRelayClient relayClient;
    private CloudPeerClient peerClient;
    private CloudOpenListBackend openlist;
    private File root;
    private int port;

    @Override public void onCreate(){super.onCreate();instance=this;getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("cloud-storage","Myliba 云层存储",NotificationManager.IMPORTANCE_LOW));}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(running)return START_STICKY;
        android.content.SharedPreferences p=getSharedPreferences(PREFS,MODE_PRIVATE);
        try{token=CloudSecretStore.read(p,"token");}catch(Exception ex){status="设备凭据读取失败";stopSelf();return START_NOT_STICKY;}
        hub=p.getString("hub","");publicUrl=p.getString("url","");relayUrl=p.getString("relay","");backend=p.getString("backend","disk");port=p.getInt("port",4319);
        activeBackend=backend;
        if(token.length()<32||token.contains("\r")||token.contains("\n")||!hub.startsWith("https://")||(!publicUrl.isEmpty()&&!publicUrl.startsWith("https://"))||(!relayUrl.isEmpty()&&!relayUrl.startsWith("https://"))||port<1||port>65535||!(backend.equals("disk")||backend.equals("openlist"))){status="配置无效";stopSelf();return START_NOT_STICKY;}
        root=new File(getFilesDir(),"cloud-node");new File(root,"blocks").mkdirs();new File(root,"catalog").mkdirs();
        try{
            if(backend.equals("openlist"))openlist=new CloudOpenListBackend(p.getString("openlist_url",""),p.getString("openlist_mount",""),p.getString("openlist_user",""),CloudSecretStore.read(p,"openlist_password"));
            String identityFile=backend.equals("disk")?"node-id":"node-id-openlist-"+hash((p.getString("openlist_url","")+"/"+p.getString("openlist_mount","")).getBytes(StandardCharsets.UTF_8)).substring(0,16);
            File idFile=new File(root,identityFile);
            if(idFile.exists())nodeId=new String(read(idFile,128),StandardCharsets.US_ASCII).trim();
            else{
                String device=android.provider.Settings.Secure.getString(getContentResolver(),android.provider.Settings.Secure.ANDROID_ID);
                if(device==null||device.isEmpty()){byte[] random=new byte[16];new java.security.SecureRandom().nextBytes(random);nodeId=hex(random);}
                else nodeId=hash(("myliba-cloud-node:"+device+":"+identityFile).getBytes(StandardCharsets.UTF_8)).substring(0,32);
                write(idFile,nodeId.getBytes(StandardCharsets.US_ASCII));
            }
            if(!nodeId.matches("[0-9a-f]{32}"))throw new IOException("节点 ID 无效");
			try{lan=new CloudLan(nodeId);}catch(Exception ex){AppLog.error("Cloud LAN unavailable",ex);}
            if(publicUrl.isEmpty())publicUrl=relayUrl.isEmpty()?"p2p://"+nodeId:relayUrl+"/cloud/relay/"+nodeId;
            if(!publicUrl.startsWith("p2p://")){listener=new ServerSocket();listener.setReuseAddress(true);listener.bind(new InetSocketAddress(publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)?"127.0.0.1":"0.0.0.0",port));}
        }catch(Exception ex){status="存储节点启动失败："+ex.getMessage();stopSelf();return START_NOT_STICKY;}
        Intent open=new Intent(this,MainActivity.class);
        PendingIntent click=PendingIntent.getActivity(this,0,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        Notification notification=new Notification.Builder(this,"cloud-storage").setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle("Myliba 云层存储节点运行中").setContentText((openlist==null?"本机磁盘":"OpenList 云盘")+" · 只存储加密文件块").setContentIntent(click).setOngoing(true).build();
        if(Build.VERSION.SDK_INT>=34)startForeground(4319,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4319,notification);
        stopped=false;running=true;status="等待 Hub 注册";
        AppLog.info("Cloud storage node starting; transport "+(publicUrl.startsWith("p2p://")?"p2p":publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)?"relay":"direct"));
        if(listener!=null)workers.execute(()->{while(!stopped){try{Socket socket=listener.accept();workers.execute(()->serve(socket));}catch(IOException ex){if(!stopped)break;}}});
		if(lan!=null)workers.execute(()->{while(!stopped){try{Socket socket=lan.listener.accept();workers.execute(()->serve(socket));}catch(IOException ex){if(!stopped)break;}}});
        workers.execute(()->{while(!stopped){try{syncHub();status="已向 Hub 注册";AppLog.info("Cloud storage node registered with Hub");}catch(Exception ex){AppLog.error("Cloud storage Hub sync failed",ex);status="Hub 同步失败："+ex.getMessage();}try{Thread.sleep(20000);}catch(InterruptedException ex){break;}}});
        if(publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)){relayClient=new CloudRelayClient(relayUrl,nodeId,token,port,()->peerGrant());new Thread(()->relayClient.run(()->stopped),"cloud-relay").start();}
		peerClient=new CloudPeerClient(this,hub,nodeId,token);new Thread(()->peerClient.run(()->stopped),"cloud-peer").start();
        return START_STICKY;
    }

    @Override public void onDestroy(){AppLog.info("Cloud storage node stopping");stopped=true;running=false;relayConnected=false;status="已停止";peerLinks.clear();peerNodes.clear();peerPairs.clear();if(instance==this)instance=null;if(relayClient!=null)relayClient.close();if(peerClient!=null)peerClient.close();try{if(listener!=null)listener.close();if(lan!=null)lan.listener.close();}catch(IOException ignored){}probes.shutdownNow();workers.shutdownNow();super.onDestroy();}
    @Override public IBinder onBind(Intent intent){return null;}

    private static String hex(byte[] data){StringBuilder b=new StringBuilder(data.length*2);for(byte value:data)b.append(String.format(Locale.ROOT,"%02x",value&255));return b.toString();}
    private static String hash(byte[] data)throws Exception{return hex(MessageDigest.getInstance("SHA-256").digest(data));}
    private static boolean validId(String value){return value!=null&&value.matches("[0-9a-f]{64}");}
    private String objectPath(String kind,String id)throws Exception{if(!("blocks".equals(kind)||"catalog".equals(kind))||!validId(id))throw new IOException("invalid id");return "cloud-node/"+kind+"/"+id.substring(0,2)+"/"+id;}
    private static byte[] read(File file,int max)throws IOException{if(file.length()>max)throw new IOException("too large");try(FileInputStream in=new FileInputStream(file);ByteArrayOutputStream out=new ByteArrayOutputStream()){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>max)throw new IOException("too large");}return out.toByteArray();}}
    private static void write(File file,byte[] data)throws IOException{file.getParentFile().mkdirs();File tmp=File.createTempFile(".cloud-",".tmp",file.getParentFile());try(FileOutputStream out=new FileOutputStream(tmp)){out.write(data);out.getFD().sync();}if(!tmp.renameTo(file)){tmp.delete();throw new IOException("rename failed");}}
    private void put(String kind,String id,byte[] data)throws Exception{
        int max="blocks".equals(kind)?(4<<20)+64:1<<20;
        if(data.length==0||data.length>max||!hash(data).equals(id))throw new IOException("hash mismatch");
        if("catalog".equals(kind)){JSONObject record=new JSONObject(new String(data,StandardCharsets.UTF_8));if(!validId(record.optString("libraryId"))||!record.has("sealed")||!record.has("blockIds"))throw new IOException("invalid catalog");}
        if(openlist!=null){openlist.put(kind,id,data);return;}
        try{if(hash(StorageDirectories.read(this,objectPath(kind,id),max)).equals(id))return;}catch(IOException ignored){}
        StorageDirectories.write(this,objectPath(kind,id),data);
    }
    private byte[] get(String kind,String id)throws Exception{
        int max="blocks".equals(kind)?(4<<20)+64:1<<20;
        byte[] data=openlist!=null?openlist.get(kind,id,max):StorageDirectories.read(this,objectPath(kind,id),max);
        if(!hash(data).equals(id))throw new IOException("cloud object hash mismatch");
        return data;
    }
    private List<String> list(String kind)throws Exception{if(openlist!=null)return openlist.list(kind);Set<String> unique=new TreeSet<>();String base="cloud-node/"+kind;for(String prefix:StorageDirectories.list(this,base)){if(!prefix.matches("[0-9a-f]{2}"))continue;for(String id:StorageDirectories.list(this,base+"/"+prefix))if(validId(id))unique.add(id);}return new ArrayList<>(unique);}
    private static String line(InputStream in)throws IOException{ByteArrayOutputStream out=new ByteArrayOutputStream();int ch;while((ch=in.read())!=-1){if(ch=='\n')break;if(ch!='\r')out.write(ch);if(out.size()>8192)throw new IOException("header too long");}if(ch==-1&&out.size()==0)throw new EOFException();return out.toString("UTF-8");}
    private static byte[] exact(InputStream in,int length)throws IOException{byte[] data=new byte[length];int offset=0;while(offset<length){int n=in.read(data,offset,length-offset);if(n<0)throw new EOFException();offset+=n;}return data;}
    private static void respond(OutputStream out,int status,String type,byte[] body,boolean head)throws IOException{
        String reason=status==200?"OK":status==204?"No Content":status==400?"Bad Request":status==401?"Unauthorized":status==404?"Not Found":"Error";
        out.write(("HTTP/1.1 "+status+" "+reason+"\r\nContent-Type: "+type+"\r\nContent-Length: "+body.length+"\r\nCache-Control: private\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        if(!head)out.write(body);out.flush();
    }
    private static byte[] json(JSONObject value){return value.toString().getBytes(StandardCharsets.UTF_8);}
    static final class PeerResult {final int status;final byte[] data;final long payloadMillis;PeerResult(int status,byte[] data){this(status,data,0);}PeerResult(int status,byte[] data,long payloadMillis){this.status=status;this.data=data;this.payloadMillis=payloadMillis;}}
    String peerGrant(){
        HttpURLConnection request=null;
        try{
            request=hubRequest("POST","/cloud/auth/grant",new byte[0]);
            if(request.getResponseCode()!=200)return "";
            byte[] raw;try(InputStream in=request.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream()){byte[] buffer=new byte[1024];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>8192)return "";}raw=out.toByteArray();}
            String grant=new JSONObject(new String(raw,StandardCharsets.UTF_8)).optString("token","");
            return grant.length()>=32&&!grant.contains("\r")&&!grant.contains("\n")?grant:"";
        }catch(Exception ex){AppLog.error("Cloud grant failed",ex);return "";}
        finally{if(request!=null)request.disconnect();}
    }
    private boolean verifyPeer(String suppliedToken){
        if(suppliedToken==null||suppliedToken.length()<32||suppliedToken.length()>4096)return false;
        HttpURLConnection check=null;
        try{
            check=hubRequest("POST","/cloud/auth/verify",json(new JSONObject().put("token",suppliedToken)));
            return check.getResponseCode()==204;
        }catch(Exception ex){AppLog.error("Cloud peer authorization failed",ex);return false;}
        finally{if(check!=null)check.disconnect();}
    }
    PeerResult peerRequest(String method,String path,String suppliedToken,byte[] data){
        try{
            if(!verifyPeer(suppliedToken))return new PeerResult(401,new byte[0]);
			if("GET".equals(method)&&"/cloud/ping".equals(path))return new PeerResult(204,new byte[0]);
            URI uri=new URI(path);String route=uri.getPath();
			if("GET".equals(method)&&"/cloud/diagnostic-payload".equals(route)){int size=diagnosticSize(uri);return size<1?new PeerResult(400,new byte[0]):new PeerResult(200,new byte[size]);}
            if("GET".equals(method)&&"/cloud/catalog".equals(route))return new PeerResult(200,json(new JSONObject().put("records",new JSONArray(list("catalog")))));
            if("GET".equals(method)&&"/cloud/inventory".equals(route)){
                String after=uri.getRawQuery()==null?"":uri.getRawQuery().replaceFirst("^after=","");if(!after.isEmpty()&&!validId(after))return new PeerResult(400,new byte[0]);
                List<String> ids=list("blocks");int start=0;while(start<ids.size()&&ids.get(start).compareTo(after)<=0)start++;int end=Math.min(start+1000,ids.size());
                JSONArray page=new JSONArray();for(int i=start;i<end;i++)page.put(ids.get(i));String next=end<ids.size()?ids.get(end-1):"";
                return new PeerResult(200,json(new JSONObject().put("blocks",page).put("next",next)));
            }
            String[] parts=route.split("/");if(parts.length!=4||!"cloud".equals(parts[1])||!("blocks".equals(parts[2])||"catalog".equals(parts[2]))||!validId(parts[3]))return new PeerResult(404,new byte[0]);
            if("PUT".equals(method)){put(parts[2],parts[3],data);return new PeerResult(204,new byte[0]);}
            if("GET".equals(method)||"HEAD".equals(method)){byte[] value=get(parts[2],parts[3]);return new PeerResult(200,"HEAD".equals(method)?new byte[0]:value);}
            return new PeerResult(400,new byte[0]);
        }catch(FileNotFoundException ex){return new PeerResult(404,new byte[0]);}catch(Exception ex){return new PeerResult(503,new byte[0]);}
    }
    private void serve(Socket socket){
        try(Socket client=socket){client.setSoTimeout(30000);InputStream in=new BufferedInputStream(client.getInputStream());OutputStream out=client.getOutputStream();
            String[] first=line(in).split(" ");if(first.length<2){respond(out,400,"text/plain",new byte[0],false);return;}
            String method=first[0];URI uri=new URI(first[1]);String route=uri.getPath();Map<String,String> headers=new HashMap<>();String header;int total=0;
            while(!(header=line(in)).isEmpty()){total+=header.length();if(total>16384)throw new IOException("headers too large");int colon=header.indexOf(':');if(colon>0)headers.put(header.substring(0,colon).toLowerCase(Locale.ROOT),header.substring(colon+1).trim());}
            String authorization=headers.getOrDefault("authorization","");
            if(!authorization.startsWith("Bearer ")||!verifyPeer(authorization.substring(7))){respond(out,401,"text/plain",new byte[0],false);return;}
			if("GET".equals(method)&&"/cloud/ping".equals(route)){respond(out,204,"text/plain",new byte[0],false);return;}
			if("GET".equals(method)&&"/cloud/diagnostic-payload".equals(route)){int size=diagnosticSize(uri);respond(out,size<1?400:200,"application/octet-stream",size<1?new byte[0]:new byte[size],false);return;}
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
    private static int diagnosticSize(URI uri){try{String query=uri.getRawQuery();if(query==null||!query.startsWith("bytes=")||query.indexOf('&')>=0)return -1;int size=Integer.parseInt(query.substring(6));return size>0&&size<=(4<<20)?size:-1;}catch(Exception ex){return -1;}}
    private HttpURLConnection hubRequest(String method,String path,byte[] data)throws Exception{
        String base=hub.replaceFirst("^wss:","https:").replaceFirst("^ws:","http:").replaceAll("/node/?$","");
        HttpURLConnection c=(HttpURLConnection)new URL(base+path).openConnection();c.setRequestMethod(method);c.setConnectTimeout(10000);c.setReadTimeout(30000);c.setInstanceFollowRedirects(false);c.setRequestProperty("Authorization","Bearer "+token);
        if(data!=null){c.setDoOutput(true);c.setRequestProperty("Content-Type","application/json");c.setFixedLengthStreamingMode(data.length);try(OutputStream out=c.getOutputStream()){out.write(data);}}return c;
    }
	private void probeNodes(JSONArray nodes){
		Set<String> active=new HashSet<>();
		for(int i=0;i<nodes.length();i++){
			JSONObject node=nodes.optJSONObject(i);if(node==null)continue;
			String id=node.optString("id"),endpoint=node.optString("endpoint");
			if(id.equals(nodeId)||!id.matches("[0-9a-f]{32}"))continue;
			peerNodes.put(id,node);
			active.add(id);peerLinks.putIfAbsent(id,checkingPeerLink());
			if(!probingPeers.add(id))continue;
			try{probes.execute(()->{
				try{
				JSONObject result=diagnosticsForNode(node);
				if(!stopped)peerLinks.put(id,result);
				}catch(Exception ex){AppLog.error("Cloud peer diagnostics failed for "+id,ex);
				}finally{probingPeers.remove(id);}
			});}catch(RejectedExecutionException ignored){probingPeers.remove(id);}
		}
		peerLinks.keySet().retainAll(active);
		peerNodes.keySet().retainAll(active);
	}
	private JSONObject diagnosticsForNode(JSONObject node)throws Exception{
		String id=node.optString("id"),endpoint=node.optString("endpoint");JSONObject checks=new JSONObject();JSONArray addresses=node.optJSONArray("lan");String key=node.optString("lanKey");
		if(addresses!=null&&addresses.length()>0)try{checks.put("lan",probePath(path->{String grant=peerGrant();if(grant.isEmpty())throw new IOException("cloud grant unavailable");return CloudLan.request(addresses,key,"GET",path,grant,null);},false));}catch(Exception ex){checks.put("lan",failedCheck(ex));AppLog.info("Cloud LAN probe "+id+": "+ex.getMessage());}
		if(endpoint.equals("p2p://"+id)&&peerClient!=null)try{peerPairs.remove(id);JSONObject check=probePath(path->peerClient.request(id,"GET",path,null),true);String pair=peerPairs.get(id);if(pair!=null)check.put("selectedPair",pair);checks.put("webrtc",check);}catch(Exception ex){checks.put("webrtc",failedCheck(ex));AppLog.info("Cloud WebRTC probe "+id+": "+ex.getMessage());}
		else if(endpoint.startsWith("https://"))try{String path=endpoint.contains("/cloud/relay/")?"relay":"https";checks.put(path,probePath(route->{String grant=peerGrant();if(grant.isEmpty())throw new IOException("cloud grant unavailable");return httpsPeerRequest(endpoint,"GET",route,grant,null);},false));}catch(Exception ex){String path=endpoint.contains("/cloud/relay/")?"relay":"https";checks.put(path,failedCheck(ex));AppLog.info("Cloud HTTPS probe "+id+": "+ex.getMessage());}
		JSONObject result=new JSONObject().put("state","unreachable").put("checkedAt",peerCheckTime()).put("checks",checks).put("peer",new JSONObject().put("id",id).put("role",node.optString("role")).put("endpointType",endpoint.startsWith("p2p://")?"webrtc":endpoint.contains("/cloud/relay/")?"relay":endpoint.startsWith("https://")?"https":"waiting").put("lanAddresses",addresses==null?0:addresses.length()).put("seenAt",node.optString("seenAt")));
		for(String path:new String[]{"lan","webrtc","https","relay"}){JSONObject check=checks.optJSONObject(path);if(check!=null&&"connected".equals(check.optString("state"))){result.put("state","connected").put("path",path);break;}}
		return result;
	}
	private static JSONObject failedCheck(Exception error){JSONObject result=new JSONObject();try{String message=error.getMessage()==null?error.getClass().getSimpleName():error.getMessage();if(message.length()>240)message=message.substring(0,240);result.put("state","unreachable").put("error",message);}catch(Exception ignored){}return result;}
	static void diagnosePeer(String id,Consumer<JSONObject> callback){CloudStorageService service=instance;JSONObject node=peerNodes.get(id);if(service==null||node==null){callback.accept(failedCheck(new IOException("节点当前不可用")));return;}try{service.probes.execute(()->{try{JSONObject result=service.diagnosticsForNode(node);peerLinks.put(id,result);callback.accept(result);}catch(Exception ex){callback.accept(failedCheck(ex));}});}catch(RejectedExecutionException ex){callback.accept(failedCheck(ex));}}
	static void speedTestPeer(String id,String path,Consumer<JSONObject> callback){CloudStorageService service=instance;JSONObject node=peerNodes.get(id);if(service==null||node==null){callback.accept(failedCheck(new IOException("节点当前不可用")));return;}try{service.probes.execute(()->callback.accept(service.measurePeer(node,path)));}catch(RejectedExecutionException ex){callback.accept(failedCheck(ex));}}
	private JSONObject measurePeer(JSONObject node,String path){long started=SystemClock.elapsedRealtime();int bytes=4<<20,rounds=3;long totalBytes=0,totalMillis=0,totalConnect=0;JSONArray samples=new JSONArray();String error="";String id=node.optString("id");
		if(!Arrays.asList("lan","webrtc","https","relay").contains(path))return failedCheck(new IOException("测速路径无效"));
		try{for(int i=0;i<rounds;i++){long sampleStart=SystemClock.elapsedRealtime();PeerResult result=requestPeerPath(node,path,"/cloud/diagnostic-payload?bytes="+bytes);long elapsed=Math.max(1,SystemClock.elapsedRealtime()-sampleStart);if(result==null||result.status!=200||result.data.length!=bytes){error="第 "+(i+1)+" 轮测速数据不完整";break;}long transfer=Math.max(1,result.payloadMillis>0?result.payloadMillis:elapsed);long setup=Math.max(0,elapsed-transfer);long bps=result.data.length*1000L/transfer;totalBytes+=result.data.length;totalMillis+=transfer;totalConnect+=setup;samples.put(new JSONObject().put("bytes",result.data.length).put("durationMillis",transfer).put("connectMillis",setup).put("speedBps",bps));}}
		catch(Exception ex){error=ex.getMessage()==null?ex.getClass().getSimpleName():ex.getMessage();}
		long duration=Math.max(1,SystemClock.elapsedRealtime()-started);try{JSONObject result=new JSONObject().put("peerId",id).put("path",path).put("state",error.isEmpty()&&samples.length()==rounds?"complete":"failed").put("rounds",rounds).put("bytes",totalBytes).put("durationMillis",duration).put("speedBps",totalMillis>0?totalBytes*1000L/totalMillis:0).put("connectMillis",samples.length()>0?totalConnect/samples.length():0).put("samples",samples).put("startedAt",peerCheckTime());String pair=peerPairs.get(id);if(pair!=null)result.put("selectedPair",pair);if(error.isEmpty()&&samples.length()!=rounds)error="测速未完成";if(!error.isEmpty()){if(error.length()>240)error=error.substring(0,240);result.put("error",error);}return result;}catch(Exception ex){return failedCheck(ex);}
	}
	private PeerResult requestPeerPath(JSONObject node,String path,String route)throws Exception{
		String id=node.optString("id"),endpoint=node.optString("endpoint");
		if("lan".equals(path)){JSONArray addresses=node.optJSONArray("lan");String grant=peerGrant();if(addresses==null||addresses.length()==0||grant.isEmpty())throw new IOException("局域网地址或节点授权不可用");return CloudLan.request(addresses,node.optString("lanKey"),"GET",route,grant,null);}
		if("webrtc".equals(path)){if(peerClient==null||!endpoint.equals("p2p://"+id))throw new IOException("WebRTC 节点地址不可用");return peerClient.request(id,"GET",route,null);}
		if(!endpoint.startsWith("https://")||("relay".equals(path))!=endpoint.contains("/cloud/relay/"))throw new IOException("HTTPS 节点地址不可用");String grant=peerGrant();if(grant.isEmpty())throw new IOException("节点授权不可用");return httpsPeerRequest(endpoint,"GET",route,grant,null);
	}
	private interface PeerProbe {PeerResult request(String path)throws Exception;}
	private static JSONObject checkingPeerLink(){try{return new JSONObject().put("state","checking");}catch(Exception ignored){return new JSONObject();}}
	private static String peerCheckTime(){java.text.SimpleDateFormat format=new java.text.SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'",Locale.ROOT);format.setTimeZone(TimeZone.getTimeZone("UTC"));return format.format(new Date());}
	private JSONObject probePath(PeerProbe probe,boolean negotiated)throws Exception{
		if(negotiated){long start=SystemClock.elapsedRealtime();PeerResult sample=probe.request("/cloud/diagnostic-payload?bytes=262144");long elapsed=SystemClock.elapsedRealtime()-start;JSONObject check=new JSONObject().put("state",sample!=null&&sample.status==200&&sample.data.length>0?"connected":"unreachable").put("connectMillis",elapsed).put("durationMillis",elapsed);if(sample!=null){check.put("httpStatus",sample.status).put("bytes",sample.data.length);if(sample.status!=200)check.put("error","HTTP "+sample.status);if(sample.status==200&&sample.data.length>0&&elapsed>0)check.put("speedBps",sample.data.length*1000L/elapsed);}return check;}
		long total=0;
		for(int i=0;i<3;i++){long start=SystemClock.elapsedRealtime();PeerResult result=probe.request("/cloud/ping");if(result==null||result.status!=204)return new JSONObject().put("state","unreachable").put("error",result==null?"no response":"ping HTTP "+result.status);total+=SystemClock.elapsedRealtime()-start;}
		JSONObject check=new JSONObject().put("state","connected").put("rttMillis",total/3);
		long start=SystemClock.elapsedRealtime();PeerResult sample=probe.request("/cloud/diagnostic-payload?bytes=262144");long elapsed=SystemClock.elapsedRealtime()-start;
		if(sample!=null){check.put("httpStatus",sample.status).put("bytes",sample.data.length).put("durationMillis",elapsed);if(sample.status==200&&sample.data.length>0&&elapsed>0)check.put("speedBps",sample.data.length*1000L/elapsed);else check.put("error","diagnostic HTTP "+sample.status);}
		return check;
	}
	private static PeerResult httpsPeerRequest(String endpoint,String method,String path,String grant,byte[] data)throws Exception{
		HttpURLConnection connection=(HttpURLConnection)new URL(endpoint+path).openConnection();
		connection.setRequestMethod(method);connection.setConnectTimeout(3000);connection.setReadTimeout(path.startsWith("/cloud/diagnostic-payload")?90000:8000);connection.setInstanceFollowRedirects(false);connection.setRequestProperty("Authorization","Bearer "+grant);
		try{
			if(data!=null&&data.length>0){connection.setDoOutput(true);connection.setFixedLengthStreamingMode(data.length);try(OutputStream out=connection.getOutputStream()){out.write(data);}}
			int status=connection.getResponseCode();long payloadStart=SystemClock.elapsedRealtime();InputStream input=status>=400?connection.getErrorStream():connection.getInputStream();
			if(input==null)return new PeerResult(status,new byte[0]);
			try(InputStream in=input;ByteArrayOutputStream out=new ByteArrayOutputStream()){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>16<<20)throw new IOException("peer response too large");}return new PeerResult(status,out.toByteArray(),Math.max(1,SystemClock.elapsedRealtime()-payloadStart));}
		}finally{connection.disconnect();}
	}
    private void syncHub()throws Exception{
        String advertised=publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)&&(relayClient==null||!relayClient.isConnected())?"":publicUrl;
        JSONObject node=new JSONObject().put("id",nodeId).put("endpoint",advertised).put("relayed",publicUrl.equals(relayUrl+"/cloud/relay/"+nodeId)).put("role","storage");
		if(lan!=null){JSONArray addresses=lan.addresses();if(addresses.length()>0)node.put("lan",addresses).put("lanKey",lan.fingerprint);}
		JSONObject links=new JSONObject();int reported=0;for(Map.Entry<String,JSONObject> entry:peerLinks.entrySet()){if(reported++>=64)break;links.put(entry.getKey(),entry.getValue());}node.put("links",links);
        List<String> blocks=list("blocks"),catalog=list("catalog");
        blockCount=blocks.size();catalogCount=catalog.size();capacityBytes=openlist==null?StorageDirectories.totalCapacity(this):0;availableBytes=openlist==null?StorageDirectories.totalAvailable(this):0;
        node.put("backend",backend).put("statsReady",true).put("blockCount",blocks.size()).put("catalogCount",catalog.size());
        if(openlist==null){long blockBytes=0;for(String id:blocks)blockBytes+=StorageDirectories.length(this,objectPath("blocks",id));node.put("blockBytes",blockBytes).put("capacityBytes",capacityBytes).put("availableBytes",availableBytes);}
        HttpURLConnection heartbeat=hubRequest("POST","/cloud/nodes/heartbeat",json(node));try{if(heartbeat.getResponseCode()!=204)throw new IOException("注册 HTTP "+heartbeat.getResponseCode());}finally{heartbeat.disconnect();}
		HttpURLConnection listingNodes=hubRequest("GET","/cloud/nodes",null);
		try{if(listingNodes.getResponseCode()!=200)throw new IOException("节点目录 HTTP "+listingNodes.getResponseCode());
			ByteArrayOutputStream out=new ByteArrayOutputStream();try(InputStream in=listingNodes.getInputStream()){byte[] buffer=new byte[4096];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>1<<20)throw new IOException("节点目录过大");}}
			JSONArray nodes=new JSONObject(out.toString("UTF-8")).optJSONArray("nodes");if(nodes!=null)probeNodes(nodes);
		}finally{listingNodes.disconnect();}
        HttpURLConnection listing=hubRequest("GET","/cloud/catalog",null);JSONArray remote;
        try{if(listing.getResponseCode()!=200)throw new IOException("索引 HTTP "+listing.getResponseCode());ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[4096];int n;try(InputStream in=listing.getInputStream()){while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>16<<20)throw new IOException("catalog too large");}}remote=new JSONObject(out.toString("UTF-8")).getJSONArray("records");}finally{listing.disconnect();}
        Set<String> known=new HashSet<>();Set<String> localCatalog=new HashSet<>(catalog);for(int i=0;i<remote.length();i++){String id=remote.optString(i);if(!validId(id))continue;known.add(id);if(localCatalog.contains(id))continue;
            HttpURLConnection get=hubRequest("GET","/cloud/catalog/"+id,null);try{if(get.getResponseCode()!=200)continue;ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[4096];int n;try(InputStream in=get.getInputStream()){while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>1<<20)throw new IOException("catalog too large");}}put("catalog",id,out.toByteArray());}finally{get.disconnect();}
        }
        for(String id:catalog){if(known.contains(id))continue;byte[] data=get("catalog",id);HttpURLConnection put=hubRequest("PUT","/cloud/catalog/"+id,data);try{if(put.getResponseCode()!=204)throw new IOException("索引上传失败：HTTP "+put.getResponseCode());}finally{put.disconnect();}}
    }
}
