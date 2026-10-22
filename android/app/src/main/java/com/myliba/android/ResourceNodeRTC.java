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
import java.util.concurrent.atomic.AtomicBoolean;

/** Answers authenticated Hub signaling and serves the local library over RTC. */
final class ResourceNodeRTC {
    private final NodeResourceApi api;private final ExecutorService workers;
    private final PeerConnectionFactory factory;
    private final Map<String,Session> sessions=new ConcurrentHashMap<>();
    private final Map<String,List<IceCandidate>> earlyCandidates=new ConcurrentHashMap<>();
    private volatile JSONArray servers=new JSONArray();
    private volatile HubWebSocket socket;
    ResourceNodeRTC(Context context,NodeResourceApi api,ExecutorService workers){
        this.api=api;this.workers=workers;
        PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(context).createInitializationOptions());
        factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
    }
    void setSocket(HubWebSocket value){socket=value;}
    void setICEServers(JSONArray value){servers=value==null?new JSONArray():value;}
    void closeSessions(){for(Session session:sessions.values())session.close();sessions.clear();earlyCandidates.clear();}
    void close(){closeSessions();factory.dispose();}
    void signal(JSONObject frame){
        String id=frame.optString("sessionId"),share=frame.optString("shareId");JSONObject signal=frame.optJSONObject("signal");
        if(id.isEmpty()||signal==null||!share.isEmpty())return;
        String type=signal.optString("type");
        if("offer".equals(type)){JSONObject description=signal.optJSONObject("description");if(description!=null&&"offer".equals(description.optString("type")))workers.execute(()->accept(id,description.optString("sdp")));}
        else if("candidate".equals(type)){
            JSONObject candidate=signal.optJSONObject("candidate");if(candidate==null)return;
            IceCandidate ice=new IceCandidate(candidate.optString("sdpMid"),candidate.optInt("sdpMLineIndex"),candidate.optString("candidate"));
            Session session=sessions.get(id);if(session==null)earlyCandidates.computeIfAbsent(id,key->new CopyOnWriteArrayList<>()).add(ice);else session.addCandidate(ice);
        }else if("close".equals(type)){Session gone=sessions.remove(id);if(gone!=null)gone.close();}
    }
    private void accept(String id,String sdp){
        Session session=null;
        try{
            List<PeerConnection.IceServer> ice=new ArrayList<>();
            JSONArray configured=servers;
            for(int i=0;i<configured.length();i++){JSONObject server= configured.optJSONObject(i);if(server==null)continue;JSONArray urls=server.optJSONArray("urls");if(urls==null)continue;
                for(int j=0;j<urls.length();j++){String url=urls.optString(j);if(url.startsWith("stun:")||url.startsWith("stuns:"))ice.add(PeerConnection.IceServer.builder(url).createIceServer());}
            }
            if(ice.isEmpty())ice.add(PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer());
            session=new Session(id);
            Session active=session;
            PeerConnection peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){if(state==PeerConnection.IceConnectionState.FAILED||state==PeerConnection.IceConnectionState.CLOSED){sessions.remove(id,active);active.close();}}
                @Override public void onIceConnectionReceivingChange(boolean value){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){}
                @Override public void onIceCandidate(IceCandidate candidate){try{sendSignal(id,new JSONObject().put("type","candidate").put("candidate",new JSONObject().put("candidate",candidate.sdp).put("sdpMid",candidate.sdpMid).put("sdpMLineIndex",candidate.sdpMLineIndex)));}catch(Exception ignored){}}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel channel){active.channel=channel;channel.registerObserver(active);}
                @Override public void onRenegotiationNeeded(){}
            });
            if(peer==null)throw new IOException("RTC peer unavailable");session.peer=peer;
            Session previous=sessions.put(id,session);if(previous!=null)previous.close();
            setDescription(peer,new SessionDescription(SessionDescription.Type.OFFER,sdp),false);
            session.remoteReady=true;List<IceCandidate> pending=earlyCandidates.remove(id);if(pending!=null)for(IceCandidate candidate:pending)session.addCandidate(candidate);session.flushCandidates();
            SessionDescription answer=createAnswer(peer);setDescription(peer,answer,true);
            sendSignal(id,new JSONObject().put("type","answer").put("description",new JSONObject().put("type","answer").put("sdp",peer.getLocalDescription().description)));
        }catch(Exception ex){if(session!=null){sessions.remove(id,session);session.close();}try{sendSignal(id,new JSONObject().put("type","error"));}catch(Exception ignored){}}
    }
    private void sendSignal(String id,JSONObject signal)throws Exception{
        HubWebSocket current=socket;if(current==null)throw new IOException("Hub disconnected");
        current.send(new JSONObject().put("type","rtc-signal").put("sessionId",id).put("signal",signal).toString());
    }
    private static SessionDescription createAnswer(PeerConnection peer)throws Exception{
        CountDownLatch done=new CountDownLatch(1);SessionDescription[] answer=new SessionDescription[1];String[] failure=new String[1];
        peer.createAnswer(new SdpObserver(){
            @Override public void onCreateSuccess(SessionDescription value){answer[0]=value;done.countDown();}
            @Override public void onCreateFailure(String error){failure[0]=error;done.countDown();}
            @Override public void onSetSuccess(){}
            @Override public void onSetFailure(String error){}
        },new MediaConstraints());
        if(!done.await(15,TimeUnit.SECONDS)||answer[0]==null)throw new IOException("RTC answer failed: "+failure[0]);return answer[0];
    }
    static void setDescription(PeerConnection peer,SessionDescription value,boolean local)throws Exception{
        CountDownLatch done=new CountDownLatch(1);String[] failure=new String[1];
        SdpObserver observer=new SdpObserver(){
            @Override public void onCreateSuccess(SessionDescription result){}
            @Override public void onCreateFailure(String error){failure[0]=error;done.countDown();}
            @Override public void onSetSuccess(){done.countDown();}
            @Override public void onSetFailure(String error){failure[0]=error;done.countDown();}
        };
        if(local)peer.setLocalDescription(observer,value);else peer.setRemoteDescription(observer,value);
        if(!done.await(15,TimeUnit.SECONDS)||failure[0]!=null)throw new IOException("RTC SDP failed: "+failure[0]);
    }
    private final class Session implements DataChannel.Observer {
        final String id;final Map<String,Request> requests=new HashMap<>();final List<IceCandidate> candidates=new ArrayList<>();
        final AtomicBoolean closed=new AtomicBoolean();
        volatile PeerConnection peer;volatile DataChannel channel;volatile boolean remoteReady;
        Session(String id){this.id=id;}
        synchronized void addCandidate(IceCandidate candidate){if(remoteReady&&peer!=null)peer.addIceCandidate(candidate);else candidates.add(candidate);}
        synchronized void flushCandidates(){if(peer!=null)for(IceCandidate candidate:candidates)peer.addIceCandidate(candidate);candidates.clear();}
        void close(){if(!closed.compareAndSet(false,true))return;DataChannel current=channel;if(current!=null){current.close();current.dispose();}PeerConnection connection=peer;if(connection!=null){connection.close();connection.dispose();}}
        @Override public void onBufferedAmountChange(long amount){}
        @Override public void onStateChange(){}
        @Override public synchronized void onMessage(DataChannel.Buffer buffer){
            if(buffer.binary)return;ByteBuffer data=buffer.data;byte[] raw=new byte[data.remaining()];data.get(raw);
            try{JSONObject message=new JSONObject(new String(raw,StandardCharsets.UTF_8));String type=message.optString("type"),requestID=message.optString("id");if(requestID.isEmpty())return;
                if("request".equals(type))requests.put(requestID,new Request(message));
                else if("request-chunk".equals(type)){Request request=requests.get(requestID);if(request!=null)request.add(Base64.decode(message.optString("data",""),Base64.DEFAULT));}
                else if("request-end".equals(type)){Request request=requests.remove(requestID);if(request!=null)workers.execute(()->serve(request));}
            }catch(Exception ignored){}
        }
        private void serve(Request request){
            boolean started=false;
            try(NodeResourceApi.Result result=api.handle(request.method,request.path,request.body.toByteArray(),request.headers)){
                send(new JSONObject().put("type","start").put("id",request.id).put("status",result.status).put("headers",result.headers));
                started=true;
                byte[] buffer=new byte[24<<10];int count;while((count=result.body.read(buffer))!=-1){
                    send(new JSONObject().put("type","chunk").put("id",request.id).put("data",Base64.encodeToString(buffer,0,count,Base64.NO_WRAP)));
                }
            }catch(Exception ex){try{if(!started)send(new JSONObject().put("type","start").put("id",request.id).put("status",500));else send(new JSONObject().put("type","error").put("id",request.id).put("error","手机文件读取中断"));}catch(Exception ignored){}}
            finally{try{send(new JSONObject().put("type","end").put("id",request.id));}catch(Exception ignored){}}
        }
        private synchronized void send(JSONObject value)throws Exception{
            DataChannel current=channel;if(current==null)throw new IOException("RTC data channel unavailable");
            while(current.bufferedAmount()>512<<10){if(current.state()!=DataChannel.State.OPEN)throw new IOException("RTC data channel closed");Thread.sleep(5);}
            if(!current.send(new DataChannel.Buffer(ByteBuffer.wrap(value.toString().getBytes(StandardCharsets.UTF_8)),false)))throw new IOException("RTC send failed");
        }
    }
    private static final class Request {
        final String id,method,path;final JSONObject headers;final ByteArrayOutputStream body=new ByteArrayOutputStream();
        Request(JSONObject value)throws Exception{id=value.getString("id");method=value.getString("method");path=value.getString("path");headers=value.optJSONObject("headers")==null?new JSONObject():value.getJSONObject("headers");
            String initial=value.optString("body","");if(!initial.isEmpty())add(Base64.decode(initial,Base64.DEFAULT));}
        void add(byte[] chunk)throws IOException{if(body.size()+chunk.length>2<<20)throw new IOException("request too large");body.write(chunk);}
    }
}
