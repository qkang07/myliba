package com.myliba.android;

import android.content.Context;
import org.json.*;
import org.webrtc.*;
import java.io.*;
import java.net.*;
import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.concurrent.*;
import java.util.function.BooleanSupplier;

/** Hub-signaled WebRTC receiver. Only SDP uses Hub; encrypted blocks use the DataChannel. */
final class CloudPeerClient {
    private final CloudStorageService storage;
    private final String hub,id,token;
    private final Map<String,PeerConnection> sessions=new ConcurrentHashMap<>();
    private PeerConnectionFactory factory;
    private volatile boolean closed;
    CloudPeerClient(CloudStorageService storage,String hub,String id,String token){this.storage=storage;this.hub=hub;this.id=id;this.token=token;}
    void close(){closed=true;for(PeerConnection peer:sessions.values())peer.close();sessions.clear();if(factory!=null)factory.dispose();}

    void run(BooleanSupplier stopped){
        try{
            PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(storage).createInitializationOptions());
            factory=PeerConnectionFactory.builder().createPeerConnectionFactory();
            while(!closed&&!stopped.getAsBoolean()){
                try{
                    JSONObject inbox=request("GET","/cloud/signal/"+id+"?wait=1",null);
                    JSONArray messages=inbox.optJSONArray("messages");
                    if(messages!=null)for(int i=0;i<messages.length();i++){
                        JSONObject signal=messages.optJSONObject(i);
                        if(signal!=null&&"offer".equals(signal.optString("type"))&&signal.optString("session").matches("[0-9a-f]{32}")&&signal.optString("from").matches("[0-9a-f]{32}")){
                            new Thread(()->accept(signal),"cloud-peer-offer").start();
                        }
                    }
                    CloudStorageService.status="Hub 信令在线 · 等待节点直连";
                }catch(Exception ex){if(!closed)CloudStorageService.status="直连信令失败："+ex.getMessage();}
                if(CloudStorageService.status.startsWith("直连信令失败"))try{Thread.sleep(3000);}catch(InterruptedException ex){Thread.currentThread().interrupt();return;}
            }
        }catch(Exception ex){CloudStorageService.status="WebRTC 启动失败："+ex.getMessage();}
    }

    private JSONObject request(String method,String path,JSONObject body)throws Exception{
        HttpURLConnection conn=(HttpURLConnection)new URL(hub+path).openConnection();
        conn.setRequestMethod(method);conn.setConnectTimeout(10000);conn.setReadTimeout(30000);conn.setInstanceFollowRedirects(false);
        conn.setRequestProperty("Authorization","Bearer "+token);
        if(body!=null){byte[] data=body.toString().getBytes(StandardCharsets.UTF_8);conn.setDoOutput(true);conn.setRequestProperty("Content-Type","application/json");conn.setFixedLengthStreamingMode(data.length);try(OutputStream out=conn.getOutputStream()){out.write(data);}}
        try{int status=conn.getResponseCode();if(status!=200&&status!=204)throw new IOException("Hub HTTP "+status);
            if(status==204)return new JSONObject();
            try(InputStream in=conn.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream()){
                byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>128<<10)throw new IOException("signaling too large");}
                return new JSONObject(out.toString("UTF-8"));
            }
        }finally{conn.disconnect();}
    }

    private void accept(JSONObject offer){
        String session=offer.optString("session"),from=offer.optString("from");PeerConnection peer=null;
        try{
            JSONArray serverList=request("GET","/cloud/ice",null).optJSONArray("servers");
            List<PeerConnection.IceServer> ice=new ArrayList<>();
            if(serverList!=null)for(int i=0;i<serverList.length();i++){
                JSONObject entry=serverList.optJSONObject(i);if(entry==null)continue;JSONArray urls=entry.optJSONArray("urls");
                if(urls!=null)for(int j=0;j<urls.length();j++){String url=urls.optString(j);if(url.startsWith("stun:")||url.startsWith("stuns:"))ice.add(PeerConnection.IceServer.builder(url).createIceServer());}
            }
            CountDownLatch gathered=new CountDownLatch(1);
            peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){
                    if(state==PeerConnection.IceConnectionState.CLOSED||state==PeerConnection.IceConnectionState.FAILED){PeerConnection gone=sessions.remove(session);if(gone!=null)gone.close();}
                }
                @Override public void onIceConnectionReceivingChange(boolean receiving){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){if(state==PeerConnection.IceGatheringState.COMPLETE)gathered.countDown();}
                @Override public void onIceCandidate(IceCandidate candidate){}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel channel){channel.registerObserver(new CloudChannel(channel));}
                @Override public void onRenegotiationNeeded(){}
            });
            if(peer==null)throw new IOException("WebRTC peer unavailable");sessions.put(session,peer);
            setDescription(peer,new SessionDescription(SessionDescription.Type.OFFER,offer.getString("sdp")),false);
            CountDownLatch answered=new CountDownLatch(1);SessionDescription[] answer=new SessionDescription[1];
            peer.createAnswer(new SdpObserver(){
                @Override public void onCreateSuccess(SessionDescription value){answer[0]=value;answered.countDown();}
                @Override public void onSetSuccess(){}
                @Override public void onCreateFailure(String error){answered.countDown();}
                @Override public void onSetFailure(String error){answered.countDown();}
            },new MediaConstraints());
            if(!answered.await(15,TimeUnit.SECONDS)||answer[0]==null)throw new IOException("WebRTC answer failed");
            setDescription(peer,answer[0],true);
            if(!gathered.await(15,TimeUnit.SECONDS))throw new IOException("ICE gathering timed out");
            JSONObject signal=new JSONObject().put("from",id).put("to",from).put("session",session).put("type","answer").put("sdp",peer.getLocalDescription().description);
            request("POST","/cloud/signal",signal);
            CloudStorageService.status="节点直连信令已建立";
        }catch(Exception ex){sessions.remove(session);if(peer!=null)peer.close();CloudStorageService.status="直连协商失败："+ex.getMessage();}
    }

    private static void setDescription(PeerConnection peer,SessionDescription value,boolean local)throws Exception{
        CountDownLatch done=new CountDownLatch(1);String[] error=new String[1];
        SdpObserver observer=new SdpObserver(){
            @Override public void onCreateSuccess(SessionDescription value){}
            @Override public void onSetSuccess(){done.countDown();}
            @Override public void onCreateFailure(String message){error[0]=message;done.countDown();}
            @Override public void onSetFailure(String message){error[0]=message;done.countDown();}
        };
        if(local)peer.setLocalDescription(observer,value);else peer.setRemoteDescription(observer,value);
        if(!done.await(15,TimeUnit.SECONDS)||error[0]!=null)throw new IOException("WebRTC SDP failed: "+error[0]);
    }

    private final class CloudChannel implements DataChannel.Observer {
        private final DataChannel channel;private final ByteArrayOutputStream body=new ByteArrayOutputStream();
        private String method,path,providedToken;private boolean done;
        CloudChannel(DataChannel channel){this.channel=channel;}
        @Override public void onBufferedAmountChange(long previousAmount){}
        @Override public void onStateChange(){}
        @Override public synchronized void onMessage(DataChannel.Buffer message){
            if(done)return;
            ByteBuffer buffer=message.data;byte[] data=new byte[buffer.remaining()];buffer.get(data);
            if(message.binary){if(method==null||body.size()+data.length>(4<<20)+64){done=true;channel.close();return;}body.write(data,0,data.length);return;}
            try{
                JSONObject control=new JSONObject(new String(data,StandardCharsets.UTF_8));
                if("request".equals(control.optString("type"))&&method==null){method=control.getString("method");path=control.getString("path");providedToken=control.getString("token");}
                else if("end".equals(control.optString("type"))&&method!=null){done=true;new Thread(this::respond,"cloud-peer-response").start();}
            }catch(Exception ex){done=true;channel.close();}
        }
        private void respond(){
            CloudStorageService.PeerResult result=storage.peerRequest(method,path,providedToken,body.toByteArray());
            try{
                send(new JSONObject().put("type","response").put("status",result.status).toString().getBytes(StandardCharsets.UTF_8),false);
                for(int offset=0;offset<result.data.length;offset+=16<<10){int end=Math.min(offset+(16<<10),result.data.length);send(Arrays.copyOfRange(result.data,offset,end),true);}
                send("{\"type\":\"end\"}".getBytes(StandardCharsets.UTF_8),false);
                long deadline=System.currentTimeMillis()+15000;
                while(channel.bufferedAmount()>0&&System.currentTimeMillis()<deadline&&channel.state()==DataChannel.State.OPEN)Thread.sleep(10);
            }catch(Exception ignored){}finally{channel.close();}
        }
        private void send(byte[] data,boolean binary)throws Exception{
            while(channel.bufferedAmount()>512<<10){if(channel.state()!=DataChannel.State.OPEN)throw new IOException("channel closed");Thread.sleep(5);}
            if(!channel.send(new DataChannel.Buffer(ByteBuffer.wrap(data),binary)))throw new IOException("channel send failed");
        }
    }
}
