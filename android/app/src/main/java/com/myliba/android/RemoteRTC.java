package com.myliba.android;

import android.content.Context;
import android.util.Base64;
import org.json.*;
import org.webrtc.*;
import java.io.*;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;

/** Native counterpart of the Hub browser's authenticated RTC API channel. */
final class RemoteRTC implements AutoCloseable {
    interface ResponseStart {void onStart(int status,JSONObject headers)throws Exception;}
    static final class Reply {final int status;final JSONObject headers;final byte[] body;Reply(int status,JSONObject headers,byte[] body){this.status=status;this.headers=headers;this.body=body;}}
    private final Context context;private final String base;
    private volatile String cookie;
    private volatile JSONArray servers=new JSONArray();
    private final Object lock=new Object();
    private final Map<String,BlockingQueue<JSONObject>> pending=new ConcurrentHashMap<>();
    private final List<IceCandidate> earlyCandidates=new CopyOnWriteArrayList<>();
    private volatile PeerConnection peer;private volatile DataChannel channel;private volatile HubWebSocket socket;
    private volatile boolean remoteReady;private volatile boolean closed;
    private PeerConnectionFactory factory;
    RemoteRTC(Context context,String base){this.context=context.getApplicationContext();this.base=base;}
    void configure(String cookie,JSONArray servers){this.cookie=cookie;this.servers=servers==null?new JSONArray():servers;}
    boolean connected(){DataChannel current=channel;return current!=null&&current.state()==DataChannel.State.OPEN;}
    void connectAsync(){new Thread(()->{try{ensureConnected();}catch(Exception failure){AppLog.error("Remote RTC background connection failed",failure);}},"resource-rtc-connect").start();}
    void ensureConnected()throws Exception{
        if(connected())return;
        synchronized(lock){
            if(connected())return;
            closeConnection();closed=false;
            AppLog.info("Remote RTC connecting");
            if(cookie==null||cookie.isEmpty())throw new IOException("请先登录 Hub");
            WebRtcRuntime.ensureInitialized(context);
            if(factory==null)factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
            List<PeerConnection.IceServer> ice=new ArrayList<>();
            for(int i=0;i<servers.length();i++){JSONObject server=servers.optJSONObject(i);if(server==null)continue;JSONArray urls=server.optJSONArray("urls");if(urls==null)continue;
                for(int j=0;j<urls.length();j++){String url=urls.optString(j);if(url.startsWith("stun:")||url.startsWith("stuns:"))ice.add(PeerConnection.IceServer.builder(url).createIceServer());}
            }
            if(ice.isEmpty())ice.add(PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer());
            socket=new HubWebSocket(base.replaceFirst("^https:","wss:")+"/signal",null,cookie,base);
            AppLog.info("Remote RTC signaling connected");
            CountDownLatch open=new CountDownLatch(1);
            peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){AppLog.info("Remote RTC ICE "+state);if(state==PeerConnection.IceConnectionState.FAILED||state==PeerConnection.IceConnectionState.CLOSED)open.countDown();}
                @Override public void onIceConnectionReceivingChange(boolean value){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){}
                @Override public void onIceCandidate(IceCandidate candidate){try{sendSignal(new JSONObject().put("type","candidate").put("candidate",new JSONObject().put("candidate",candidate.sdp).put("sdpMid",candidate.sdpMid).put("sdpMLineIndex",candidate.sdpMLineIndex)));}catch(Exception ignored){}}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel data){}
                @Override public void onRenegotiationNeeded(){}
            });
            if(peer==null)throw new IOException("RTC peer unavailable");
            DataChannel.Init init=new DataChannel.Init();init.ordered=true;channel=peer.createDataChannel("myliba-api",init);
            if(channel==null)throw new IOException("RTC data channel unavailable");
            channel.registerObserver(new DataChannel.Observer(){
                @Override public void onBufferedAmountChange(long amount){}
                @Override public void onStateChange(){AppLog.info("Remote RTC data channel state changed");if(connected())open.countDown();}
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
            if(!open.await(16,TimeUnit.SECONDS)||!connected()){closeConnection();throw new IOException("无法与资源节点建立 WebRTC 直连");}
            AppLog.info("Remote RTC data channel open");
        }
    }
    private void readSignals(HubWebSocket signaling){
        try{while(!closed&&socket==signaling){JSONObject signal=new JSONObject(signaling.read());String type=signal.optString("type");
            if("answer".equals(type)){JSONObject description=signal.optJSONObject("description");if(description!=null){ResourceNodeRTC.setDescription(peer,new SessionDescription(SessionDescription.Type.ANSWER,description.optString("sdp")),false);AppLog.info("Remote RTC answer set");remoteReady=true;for(IceCandidate candidate:earlyCandidates)peer.addIceCandidate(candidate);earlyCandidates.clear();}}
            else if("candidate".equals(type)){JSONObject candidate=signal.optJSONObject("candidate");if(candidate!=null){IceCandidate ice=new IceCandidate(candidate.optString("sdpMid"),candidate.optInt("sdpMLineIndex"),candidate.optString("candidate"));if(remoteReady)peer.addIceCandidate(ice);else earlyCandidates.add(ice);}}
            else if("error".equals(type))throw new IOException("Hub 拒绝 RTC 信令");
        }}catch(Exception failure){if(!closed)AppLog.error("Remote RTC signaling ended",failure);}finally{if(socket==signaling)closeConnection();}
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
        ensureConnected();String id=UUID.randomUUID().toString();BlockingQueue<JSONObject> queue=new LinkedBlockingQueue<>();pending.put(id,queue);
        try{
            send(new JSONObject().put("type","request").put("id",id).put("method",method).put("path",path).put("headers",headers));
            if(body!=null)for(int offset=0;offset<body.length;offset+=24<<10)send(new JSONObject().put("type","request-chunk").put("id",id).put("data",Base64.encodeToString(body,offset,Math.min(24<<10,body.length-offset),Base64.NO_WRAP)));
            send(new JSONObject().put("type","request-end").put("id",id));
            int status=0;JSONObject responseHeaders=new JSONObject();long total=0;
            while(true){JSONObject message=queue.poll(30,TimeUnit.SECONDS);if(message==null||!connected())throw new IOException("RTC 文件传输中断");
                String type=message.optString("type");
                if("start".equals(type)){status=message.optInt("status",502);responseHeaders=message.optJSONObject("headers");if(responseHeaders==null)responseHeaders=new JSONObject();if(startHandler!=null)startHandler.onStart(status,responseHeaders);}
                else if("chunk".equals(type)){byte[] chunk=Base64.decode(message.optString("data",""),Base64.DEFAULT);total+=chunk.length;if(total>max)throw new IOException("响应内容过大");output.write(chunk);}
                else if("error".equals(type))throw new IOException(message.optString("error","RTC 文件传输中断"));
                else if("end".equals(type)){if(status==0)throw new IOException("RTC 响应无状态");JSONArray lengths=responseHeaders.optJSONArray("Content-Length");
                    if(lengths!=null&&lengths.length()>0){try{long expected=Long.parseLong(lengths.optString(0));if(expected!=total)throw new IOException("RTC 传输不完整");}catch(NumberFormatException ignored){}}
                    AppLog.info("Remote RTC response status "+status+" bytes "+total);
                    return new Reply(status,responseHeaders,new byte[0]);}
            }
        }catch(Exception failure){AppLog.error("Remote RTC request failed",failure);throw failure;}finally{pending.remove(id);}
    }
    private synchronized void send(JSONObject value)throws Exception{
        DataChannel current=channel;if(current==null)throw new IOException("RTC channel unavailable");
        while(current.bufferedAmount()>512<<10){if(current.state()!=DataChannel.State.OPEN)throw new IOException("RTC channel closed");Thread.sleep(5);}
        if(!current.send(new DataChannel.Buffer(ByteBuffer.wrap(value.toString().getBytes(StandardCharsets.UTF_8)),false)))throw new IOException("RTC send failed");
    }
    private synchronized void closeConnection(){
        HubWebSocket old=socket;socket=null;if(old!=null)old.close();DataChannel data=channel;channel=null;if(data!=null){data.close();data.dispose();}
        PeerConnection connection=peer;peer=null;if(connection!=null){connection.close();connection.dispose();}remoteReady=false;earlyCandidates.clear();
        for(BlockingQueue<JSONObject> queue:pending.values())queue.offer(new JSONObject());
    }
    @Override public void close(){closed=true;closeConnection();if(factory!=null){factory.dispose();factory=null;}}
}
