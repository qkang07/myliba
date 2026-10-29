package com.myliba.android;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.Network;
import android.util.Base64;
import org.json.*;
import org.webrtc.*;
import java.io.*;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;

/** Native counterpart of the Hub browser's authenticated RTC API channel. */
final class RemoteRTC implements AutoCloseable {
    interface ResponseStart {void onStart(int status,JSONObject headers)throws Exception;}
    private static final class ConsumerFailure extends IOException {
        ConsumerFailure(Exception cause){super("媒体读取方已关闭",cause);}
    }
    static final class Reply {final int status;final JSONObject headers;final byte[] body;Reply(int status,JSONObject headers,byte[] body){this.status=status;this.headers=headers;this.body=body;}}
    private final Context context;private final String base;
    private volatile String cookie;
    private volatile JSONArray servers=new JSONArray();
    private final Object lock=new Object();
    private final Map<String,BlockingQueue<JSONObject>> pending=new ConcurrentHashMap<>();
    private final List<IceCandidate> earlyCandidates=new CopyOnWriteArrayList<>();
    private volatile PeerConnection peer;private volatile DataChannel channel;private volatile HubWebSocket socket;
    private volatile boolean remoteReady;private volatile boolean closed;
    private volatile String pathSummary="尚未建立 ICE 路径";
    private volatile long lastSpeedBps;
    private final ConnectivityManager connectivity;
    private final ConnectivityManager.NetworkCallback networkCallback;
    private final AtomicBoolean reconnecting=new AtomicBoolean();
    private volatile Network defaultNetwork;
    private volatile long networkEpoch,connectionEpoch=-1;
    private volatile boolean watchingNetwork,wantsConnection;
    private volatile CountDownLatch connectingLatch;
    private PeerConnectionFactory factory;
    RemoteRTC(Context context,String base){this.context=context.getApplicationContext();this.base=base;
        connectivity=(ConnectivityManager)this.context.getSystemService(Context.CONNECTIVITY_SERVICE);
        networkCallback=new ConnectivityManager.NetworkCallback(){
            @Override public void onAvailable(Network network){
                if(network.equals(defaultNetwork))return;
                defaultNetwork=network;networkEpoch++;AppLog.info("Remote RTC default network available; "+WebRtcDiagnostics.network(RemoteRTC.this.context));networkChanged();
            }
            @Override public void onLost(Network network){
                if(!network.equals(defaultNetwork))return;
                defaultNetwork=null;networkEpoch++;AppLog.info("Remote RTC default network lost; "+WebRtcDiagnostics.network(RemoteRTC.this.context));networkChanged();
            }
        };
    }
    void configure(String cookie,JSONArray servers){this.cookie=cookie;this.servers=servers==null?new JSONArray():servers;}
    private boolean channelOpen(){DataChannel current=channel;return current!=null&&current.state()==DataChannel.State.OPEN;}
    boolean connected(){return !closed&&channelOpen()&&connectionEpoch==networkEpoch;}
    String connectionSummary(){if(!connected())return "WebRTC 数据通道未连接 · 内容请求回退到 Hub HTTPS";String summary="WebRTC DataChannel · "+pathSummary;if(lastSpeedBps>0)summary+=" · 最近响应 "+(lastSpeedBps>=1L<<20?String.format(Locale.ROOT,"%.1f MB/s",lastSpeedBps/(double)(1<<20)):String.format(Locale.ROOT,"%.0f KB/s",lastSpeedBps/(double)(1<<10)));return summary;}
    private void networkChanged(){
        AppLog.info("Remote RTC default network changed");
        CountDownLatch connecting=connectingLatch;if(connecting!=null)connecting.countDown();
        new Thread(()->{
            synchronized(lock){if(closed)return;if(connectionEpoch!=networkEpoch)closeConnection();}
            if(defaultNetwork!=null&&wantsConnection)connectAsync();
        },"resource-rtc-network").start();
    }
    private void connectionBroken(Object source){
        if(source==null)return;
        new Thread(()->{
            synchronized(lock){if(closed||(peer!=source&&channel!=source))return;closeConnection();}
            if(wantsConnection&&defaultNetwork!=null)connectAsync();
        },"resource-rtc-reconnect").start();
    }
    void connectAsync(){
        if(closed)return;
        wantsConnection=true;
        if(!reconnecting.compareAndSet(false,true))return;
        new Thread(()->{
            try{
                for(int attempt=0;attempt<4&&!closed&&wantsConnection;attempt++){
                    if(watchingNetwork&&defaultNetwork==null)return;
                    try{ensureConnected();return;}
                    catch(Exception failure){if(attempt==3){AppLog.error("Remote RTC background connection failed",failure);return;}}
                    try{Thread.sleep((attempt+1)*1500L);}catch(InterruptedException ex){Thread.currentThread().interrupt();return;}
                }
            }finally{reconnecting.set(false);}
        },"resource-rtc-connect").start();
    }
    void ensureConnected()throws Exception{
        if(connected())return;
        synchronized(lock){
            if(connected())return;
            if(closed)throw new IOException("RTC 客户端已关闭");
            wantsConnection=true;
            if(!watchingNetwork&&connectivity!=null){defaultNetwork=connectivity.getActiveNetwork();connectivity.registerDefaultNetworkCallback(networkCallback);watchingNetwork=true;}
            closeConnection();long connectingEpoch=networkEpoch;
            AppLog.info("Remote RTC connecting; "+WebRtcDiagnostics.network(context));
            if(cookie==null||cookie.isEmpty())throw new IOException("请先登录 Hub");
            WebRtcRuntime.ensureInitialized(context);
            if(factory==null)factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
            List<PeerConnection.IceServer> ice=WebRtcIceServers.parse(servers);
            AppLog.info("Remote RTC "+WebRtcDiagnostics.servers(servers));
            socket=new HubWebSocket(base.replaceFirst("^https:","wss:")+"/signal",null,cookie,base);
            AppLog.info("Remote RTC signaling connected");
            CountDownLatch open=new CountDownLatch(1);connectingLatch=open;
            PeerConnection[] created=new PeerConnection[1];AtomicBoolean iceDisconnected=new AtomicBoolean();
            peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){
                    AppLog.info("Remote RTC ICE "+state+"; "+WebRtcDiagnostics.network(context));
                    if(state==PeerConnection.IceConnectionState.CONNECTED||state==PeerConnection.IceConnectionState.COMPLETED)WebRtcDiagnostics.selectedPair(created[0],"Remote RTC");
                    if(state==PeerConnection.IceConnectionState.DISCONNECTED){iceDisconnected.set(true);new Thread(()->{try{Thread.sleep(5000);}catch(InterruptedException ex){Thread.currentThread().interrupt();return;}if(iceDisconnected.get())connectionBroken(created[0]);},"resource-rtc-ice-check").start();}
                    else{iceDisconnected.set(false);if(state==PeerConnection.IceConnectionState.FAILED||state==PeerConnection.IceConnectionState.CLOSED){open.countDown();connectionBroken(created[0]);}}
                }
                @Override public void onIceConnectionReceivingChange(boolean value){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){AppLog.info("Remote RTC ICE gathering "+state);}
                @Override public void onIceCandidate(IceCandidate candidate){AppLog.info("Remote RTC local "+WebRtcDiagnostics.candidate(candidate.sdp));try{sendSignal(new JSONObject().put("type","candidate").put("candidate",new JSONObject().put("candidate",candidate.sdp).put("sdpMid",candidate.sdpMid).put("sdpMLineIndex",candidate.sdpMLineIndex)));}catch(Exception failure){AppLog.error("Remote RTC local ICE candidate signaling failed",failure);}}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel data){}
                @Override public void onRenegotiationNeeded(){}
            });
            created[0]=peer;
            if(peer==null)throw new IOException("RTC peer unavailable");
            DataChannel.Init init=new DataChannel.Init();init.ordered=true;channel=peer.createDataChannel("myliba-api",init);
            if(channel==null)throw new IOException("RTC data channel unavailable");
            DataChannel localChannel=channel;
            channel.registerObserver(new DataChannel.Observer(){
                @Override public void onBufferedAmountChange(long amount){}
                @Override public void onStateChange(){AppLog.info("Remote RTC data channel "+localChannel.state()+"; "+WebRtcDiagnostics.network(context));if(channelOpen()){open.countDown();WebRtcDiagnostics.selectedPair(peer,"Remote RTC",value->pathSummary=value);}else if(localChannel.state()==DataChannel.State.CLOSED){open.countDown();connectionBroken(localChannel);}}
                @Override public void onMessage(DataChannel.Buffer buffer){if(buffer.binary)return;ByteBuffer data=buffer.data;byte[] raw=new byte[data.remaining()];data.get(raw);
                    try{JSONObject message=new JSONObject(new String(raw,StandardCharsets.UTF_8));BlockingQueue<JSONObject> queue=pending.get(message.optString("id"));if(queue!=null)queue.offer(message);}catch(Exception ignored){}
                }
            });
            HubWebSocket signaling=socket;
            new Thread(()->readSignals(signaling),"resource-rtc-signaling").start();
            SessionDescription offer=createOffer(peer);ResourceNodeRTC.setDescription(peer,offer,true);
            AppLog.info("Remote RTC local offer set");
            sendSignal(new JSONObject().put("type","offer").put("description",new JSONObject().put("type","offer").put("sdp",offer.description)));
            AppLog.info("Remote RTC offer sent");
            boolean opened=open.await(16,TimeUnit.SECONDS);connectingLatch=null;
            if(!opened||closed||!channelOpen()||connectingEpoch!=networkEpoch){closeConnection();throw new IOException("无法与资源节点建立 WebRTC 直连");}
            connectionEpoch=connectingEpoch;
            AppLog.info("Remote RTC data channel open");
        }
    }
    private void readSignals(HubWebSocket signaling){
        try{while(!closed&&socket==signaling){JSONObject signal=new JSONObject(signaling.read());String type=signal.optString("type");
            if("answer".equals(type)){JSONObject description=signal.optJSONObject("description");if(description!=null){String sdp=description.optString("sdp");WebRtcDiagnostics.sdpCandidates("Remote RTC remote SDP",sdp);ResourceNodeRTC.setDescription(peer,new SessionDescription(SessionDescription.Type.ANSWER,sdp),false);AppLog.info("Remote RTC answer set");remoteReady=true;for(IceCandidate candidate:earlyCandidates)peer.addIceCandidate(candidate);earlyCandidates.clear();}}
            else if("candidate".equals(type)){JSONObject candidate=signal.optJSONObject("candidate");if(candidate!=null){String value=candidate.optString("candidate");AppLog.info("Remote RTC remote "+WebRtcDiagnostics.candidate(value));IceCandidate ice=new IceCandidate(candidate.optString("sdpMid"),candidate.optInt("sdpMLineIndex"),value);if(remoteReady)peer.addIceCandidate(ice);else earlyCandidates.add(ice);}}
            else if("error".equals(type))throw new IOException("Hub 拒绝 RTC 信令");
        }}catch(Exception failure){if(!closed)AppLog.error("Remote RTC signaling ended",failure);}finally{if(socket==signaling){closeConnection();if(!closed&&wantsConnection&&defaultNetwork!=null)connectAsync();}}
    }
    private void sendSignal(JSONObject signal)throws Exception{HubWebSocket current=socket;if(current==null)throw new IOException("RTC signaling disconnected");current.send(signal.toString());}
    private static SessionDescription createOffer(PeerConnection peer)throws Exception{
        CountDownLatch done=new CountDownLatch(1);SessionDescription[] offer=new SessionDescription[1];String[] failure=new String[1];
        peer.createOffer(new SdpObserver(){
            @Override public void onCreateSuccess(SessionDescription value){offer[0]=value;done.countDown();}
            @Override public void onCreateFailure(String error){failure[0]=error;done.countDown();}
            @Override public void onSetSuccess(){}
            @Override public void onSetFailure(String error){}
        },new MediaConstraints());
        if(!done.await(15,TimeUnit.SECONDS)||offer[0]==null)throw new IOException("RTC offer failed: "+failure[0]);return offer[0];
    }
    Reply fetch(String method,String path,byte[] body,int max)throws Exception{
        ByteArrayOutputStream output=new ByteArrayOutputStream();Reply response=stream(method,path,body,new JSONObject(),output,max);
        return new Reply(response.status,response.headers,output.toByteArray());
    }
    Reply stream(String method,String path,byte[] body,JSONObject headers,OutputStream output,long max)throws Exception{
        return stream(method,path,body,headers,output,max,null);
    }
    Reply stream(String method,String path,byte[] body,JSONObject headers,OutputStream output,long max,ResponseStart startHandler)throws Exception{
        return stream(method,path,body,headers,output,max,startHandler,null);
    }
    Reply stream(String method,String path,byte[] body,JSONObject headers,OutputStream output,long max,ResponseStart startHandler,AtomicBoolean cancelled)throws Exception{
        ensureConnected();long transferStarted=android.os.SystemClock.elapsedRealtime();DataChannel requestChannel=channel;String id=UUID.randomUUID().toString();BlockingQueue<JSONObject> queue=new LinkedBlockingQueue<>();pending.put(id,queue);
        try{
            send(new JSONObject().put("type","request").put("id",id).put("method",method).put("path",path).put("headers",headers));
            if(body!=null)for(int offset=0;offset<body.length;offset+=24<<10)send(new JSONObject().put("type","request-chunk").put("id",id).put("data",Base64.encodeToString(body,offset,Math.min(24<<10,body.length-offset),Base64.NO_WRAP)));
            send(new JSONObject().put("type","request-end").put("id",id));
            int status=0;JSONObject responseHeaders=new JSONObject();long total=0;
            long deadline=android.os.SystemClock.elapsedRealtime()+(path.contains("mode=ondemand")?120000:30000);
            while(true){if(cancelled!=null&&cancelled.get())throw new ConsumerFailure(new IOException("视频请求已取消"));JSONObject message=queue.poll(1,TimeUnit.SECONDS);if(!connected()||android.os.SystemClock.elapsedRealtime()>deadline)throw new IOException("RTC 文件传输中断");if(message==null)continue;deadline=android.os.SystemClock.elapsedRealtime()+30000;
                String type=message.optString("type");
                if("start".equals(type)){status=message.optInt("status",502);responseHeaders=message.optJSONObject("headers");if(responseHeaders==null)responseHeaders=new JSONObject();if(startHandler!=null)try{startHandler.onStart(status,responseHeaders);}catch(Exception ex){throw new ConsumerFailure(ex);}}
                else if("chunk".equals(type)){byte[] chunk=Base64.decode(message.optString("data",""),Base64.DEFAULT);total+=chunk.length;if(total>max)throw new IOException("响应内容过大");try{output.write(chunk);}catch(IOException ex){throw new ConsumerFailure(ex);}}
                else if("error".equals(type))throw new IOException(message.optString("error","RTC 文件传输中断"));
                else if("end".equals(type)){if(status==0)throw new IOException("RTC 响应无状态");JSONArray lengths=responseHeaders.optJSONArray("Content-Length");
                    if(lengths!=null&&lengths.length()>0){try{long expected=Long.parseLong(lengths.optString(0));if(expected!=total)throw new IOException("RTC 传输不完整");}catch(NumberFormatException ignored){}}
                    AppLog.info("Remote RTC response status "+status+" bytes "+total);
                    long elapsed=android.os.SystemClock.elapsedRealtime()-transferStarted;if(total>0&&elapsed>0)lastSpeedBps=total*1000/elapsed;
                    return new Reply(status,responseHeaders,new byte[0]);}
            }
        }catch(ConsumerFailure failure){
            AppLog.info("Remote RTC response consumer closed; cancelling request");
            try{if(channel==requestChannel&&connected())send(new JSONObject().put("type","request-cancel").put("id",id));}catch(Exception ignored){}
            throw failure;
        }catch(Exception failure){AppLog.error("Remote RTC request failed",failure);if(channel==requestChannel)closeConnection();throw failure;}finally{pending.remove(id);}
    }
    private synchronized void send(JSONObject value)throws Exception{
        DataChannel current=channel;if(current==null)throw new IOException("RTC channel unavailable");
        while(current.bufferedAmount()>512<<10){if(current.state()!=DataChannel.State.OPEN)throw new IOException("RTC channel closed");Thread.sleep(5);}
        if(!current.send(new DataChannel.Buffer(ByteBuffer.wrap(value.toString().getBytes(StandardCharsets.UTF_8)),false)))throw new IOException("RTC send failed");
    }
    private void closeConnection(){synchronized(lock){
        HubWebSocket old=socket;socket=null;if(old!=null)old.close();DataChannel data=channel;channel=null;
        PeerConnection connection=peer;peer=null;
        if(data!=null)data.unregisterObserver();
        if(connection!=null)connection.close();
        if(data!=null)data.dispose();
        if(connection!=null)connection.dispose();
        remoteReady=false;earlyCandidates.clear();connectionEpoch=-1;
        for(BlockingQueue<JSONObject> queue:pending.values())queue.offer(new JSONObject());
    }}
    @Override public void close(){closed=true;CountDownLatch connecting=connectingLatch;if(connecting!=null)connecting.countDown();synchronized(lock){wantsConnection=false;if(watchingNetwork&&connectivity!=null){connectivity.unregisterNetworkCallback(networkCallback);watchingNetwork=false;}closeConnection();if(factory!=null){factory.dispose();factory=null;}}}
}
