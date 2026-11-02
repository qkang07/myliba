package com.myliba.android;

import android.content.Context;
import android.util.Base64;
import org.json.JSONArray;
import org.json.JSONObject;
import org.webrtc.*;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.UUID;
import java.util.concurrent.BlockingQueue;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.LinkedBlockingQueue;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

/** Authenticated WebRTC channel to one selected Hub resource node. */
final class NodeTransferConnection implements AutoCloseable {
    static final class Reply {
        final int status;
        final JSONObject headers;
        final byte[] body;
        Reply(int status, JSONObject headers, byte[] body) { this.status=status; this.headers=headers; this.body=body; }
    }

    private final PeerConnectionFactory factory;
    private final HubWebSocket socket;
    private final PeerConnection peer;
    private final DataChannel channel;
    private final BlockingQueue<JSONObject> incoming=new LinkedBlockingQueue<>();
    private final CountDownLatch ready=new CountDownLatch(1);
    private final AtomicBoolean closed=new AtomicBoolean();
    private final List<IceCandidate> earlyCandidates=new ArrayList<>();
    private volatile boolean remoteReady;

    static NodeTransferConnection open(Context context, RemoteClient client, String nodeId) throws Exception {
        client.selectNode(nodeId);
        JSONObject session=client.json("GET","/api/session",null);
        return new NodeTransferConnection(context,client,session.optJSONArray("iceServers"));
    }

    private NodeTransferConnection(Context context, RemoteClient client, JSONArray iceServers) throws Exception {
        WebRtcRuntime.ensureInitialized(context.getApplicationContext());
        factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
        HubWebSocket openedSocket=null;
        PeerConnection openedPeer=null;
        DataChannel openedChannel=null;
        try {
            String ws=client.base.replaceFirst("^https:","wss:")+"/signal";
            openedSocket=new HubWebSocket(ws,null,client.cookie(),client.base);
            socket=openedSocket;
            openedPeer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(WebRtcIceServers.parse(iceServers)),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){if(state==PeerConnection.IceConnectionState.FAILED||state==PeerConnection.IceConnectionState.CLOSED)ready.countDown();}
                @Override public void onIceConnectionReceivingChange(boolean value){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){}
                @Override public void onIceCandidate(IceCandidate candidate){sendSignal(candidateSignal(candidate));}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel remote){remote.registerObserver(observer(remote));}
                @Override public void onRenegotiationNeeded(){}
            });
            if(openedPeer==null)throw new IOException("WebRTC 连接初始化失败");
            DataChannel.Init init=new DataChannel.Init();init.ordered=true;
            openedChannel=openedPeer.createDataChannel("myliba-transfer",init);
            if(openedChannel==null)throw new IOException("无法创建 WebRTC 数据通道");
            openedChannel.registerObserver(observer(openedChannel));
            peer=openedPeer;channel=openedChannel;
            PeerConnection activePeer=openedPeer;
            Thread reader=new Thread(()->readSignals(activePeer),"node-transfer-signaling");reader.setDaemon(true);reader.start();
            SessionDescription offer=createOffer(openedPeer);
            ResourceNodeRTC.setDescription(openedPeer,offer,true);
            sendSignal(new JSONObject().put("type","offer").put("description",new JSONObject().put("type","offer").put("sdp",offer.description)));
            if(!ready.await(25,TimeUnit.SECONDS)||channel.state()!=DataChannel.State.OPEN)throw new IOException("无法与所选资源节点建立 WebRTC 连接");
        } catch(Exception failure) {
            if(openedChannel!=null){openedChannel.unregisterObserver();openedChannel.dispose();}
            if(openedPeer!=null){openedPeer.close();openedPeer.dispose();}
            if(openedSocket!=null)openedSocket.close();
            factory.dispose();
            throw failure;
        }
    }

    private DataChannel.Observer observer(DataChannel target){
        return new DataChannel.Observer(){
            @Override public void onBufferedAmountChange(long amount){}
            @Override public void onStateChange(){if(target.state()==DataChannel.State.OPEN)ready.countDown();else if(target.state()==DataChannel.State.CLOSED){incoming.offer(RemoteClient.object("type","fatal","error","节点数据通道已关闭"));ready.countDown();}}
            @Override public void onMessage(DataChannel.Buffer buffer){if(buffer.binary)return;ByteBuffer bytes=buffer.data;byte[] raw=new byte[bytes.remaining()];bytes.get(raw);try{incoming.offer(new JSONObject(new String(raw,StandardCharsets.UTF_8)));}catch(Exception ignored){}}
        };
    }

    private void readSignals(PeerConnection activePeer){
        try {
            while(!closed.get()){
                JSONObject signal=new JSONObject(socket.read());String type=signal.optString("type");
                if("answer".equals(type)){
                    JSONObject description=signal.optJSONObject("description");if(description==null)continue;
                    ResourceNodeRTC.setDescription(activePeer,new SessionDescription(SessionDescription.Type.ANSWER,description.optString("sdp")),false);
                    synchronized(earlyCandidates){remoteReady=true;for(IceCandidate candidate:earlyCandidates)activePeer.addIceCandidate(candidate);earlyCandidates.clear();}
                }else if("candidate".equals(type)){
                    JSONObject candidate=signal.optJSONObject("candidate");if(candidate==null)continue;
                    IceCandidate value=new IceCandidate(candidate.optString("sdpMid"),candidate.optInt("sdpMLineIndex"),candidate.optString("candidate"));
                    synchronized(earlyCandidates){if(remoteReady)activePeer.addIceCandidate(value);else earlyCandidates.add(value);}
                }else if("error".equals(type))throw new IOException("Hub 拒绝了节点连接");
            }
        }catch(Exception failure){if(!closed.get())incoming.offer(RemoteClient.object("type","fatal","error",failure.getMessage()==null?"节点信令中断":failure.getMessage()));ready.countDown();}
    }

    private void sendSignal(JSONObject value){try{if(!closed.get())socket.send(value.toString());}catch(Exception failure){incoming.offer(RemoteClient.object("type","fatal","error","节点信令发送失败"));ready.countDown();}}

    private static JSONObject candidateSignal(IceCandidate candidate){return RemoteClient.object("type","candidate","candidate",RemoteClient.object("candidate",candidate.sdp,"sdpMid",candidate.sdpMid,"sdpMLineIndex",candidate.sdpMLineIndex));}

    private static SessionDescription createOffer(PeerConnection peer)throws Exception{
        CountDownLatch done=new CountDownLatch(1);SessionDescription[] value=new SessionDescription[1];String[] error=new String[1];
        peer.createOffer(new SdpObserver(){
            @Override public void onCreateSuccess(SessionDescription offer){value[0]=offer;done.countDown();}
            @Override public void onCreateFailure(String failure){error[0]=failure;done.countDown();}
            @Override public void onSetSuccess(){}
            @Override public void onSetFailure(String failure){}
        },new MediaConstraints());
        if(!done.await(15,TimeUnit.SECONDS)||value[0]==null)throw new IOException("WebRTC offer 创建失败："+error[0]);return value[0];
    }

    Reply request(String method,String path,byte[] body,AtomicBoolean cancelled)throws Exception{
        if(closed.get()||channel.state()!=DataChannel.State.OPEN)throw new IOException("节点连接已关闭");
        String id=UUID.randomUUID().toString();
        send(RemoteClient.object("type","request","id",id,"method",method,"path",path,"headers",new JSONObject()));
        for(int offset=0;offset<body.length;offset+=24<<10){checkCancelled(cancelled);int length=Math.min(24<<10,body.length-offset);send(RemoteClient.object("type","request-chunk","id",id,"data",Base64.encodeToString(body,offset,length,Base64.NO_WRAP)));}
        send(RemoteClient.object("type","request-end","id",id));
        int status=0;JSONObject headers=new JSONObject();ByteArrayOutputStream output=new ByteArrayOutputStream();long deadline=System.currentTimeMillis()+120000;
        try{
            while(System.currentTimeMillis()<deadline){checkCancelled(cancelled);if(closed.get()||channel.state()!=DataChannel.State.OPEN)throw new IOException("节点数据通道已关闭");JSONObject message=incoming.poll(250,TimeUnit.MILLISECONDS);if(message==null)continue;
                if("fatal".equals(message.optString("type")))throw new IOException(message.optString("error","节点连接中断"));
                if(!id.equals(message.optString("id")))continue;
                switch(message.optString("type")){
                    case "start":status=message.optInt("status",502);headers=message.optJSONObject("headers");if(headers==null)headers=new JSONObject();break;
                    case "chunk":{byte[] chunk=Base64.decode(message.optString("data",""),Base64.DEFAULT);if(output.size()+chunk.length>1<<20)throw new IOException("传输分块超过 1 MiB");output.write(chunk);break;}
                    case "error":throw new IOException(message.optString("error","节点文件读取失败"));
                    case "end":if(status==0)throw new IOException("节点未返回传输状态");return new Reply(status,headers,output.toByteArray());
                }
            }
            throw new IOException("节点传输超时");
        }catch(Exception failure){if(cancelled!=null&&cancelled.get())try{send(RemoteClient.object("type","request-cancel","id",id));}catch(Exception ignored){}throw failure;}
    }

    private synchronized void send(JSONObject message)throws Exception{
        byte[] raw=message.toString().getBytes(StandardCharsets.UTF_8);while(channel.bufferedAmount()>512<<10){if(closed.get()||channel.state()!=DataChannel.State.OPEN)throw new IOException("节点数据通道已关闭");Thread.sleep(5);}
        if(!channel.send(new DataChannel.Buffer(ByteBuffer.wrap(raw),false)))throw new IOException("节点数据发送失败");
    }

    private static void checkCancelled(AtomicBoolean cancelled)throws IOException{if(cancelled!=null&&cancelled.get())throw new IOException("传输已取消");}

    @Override public void close(){if(!closed.compareAndSet(false,true))return;try{socket.send(new JSONObject().put("type","close").toString());}catch(Exception ignored){}socket.close();channel.unregisterObserver();channel.close();peer.close();channel.dispose();peer.dispose();factory.dispose();}
}
