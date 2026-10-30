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
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.function.BooleanSupplier;

/** Hub-signaled WebRTC peer. Only SDP uses Hub; encrypted blocks use the DataChannel. */
final class CloudPeerClient {
    private final CloudStorageService storage;
    private final String hub,id,token;
    private final Map<String,CloudSession> sessions=new ConcurrentHashMap<>();
	private final Map<String,CompletableFuture<JSONObject>> pendingAnswers=new ConcurrentHashMap<>();
    private final ExecutorService offers=Executors.newCachedThreadPool();
    private final ExecutorService cleanup=Executors.newSingleThreadExecutor();
	private volatile PeerConnectionFactory factory;
    private volatile boolean closed;
    CloudPeerClient(CloudStorageService storage,String hub,String id,String token){this.storage=storage;this.hub=hub;this.id=id;this.token=token;}
    synchronized void close(){
        if(closed)return;
        AppLog.info("Cloud peer client stopping");closed=true;
        offers.shutdownNow();cleanup.shutdown();
		for(CompletableFuture<JSONObject> answer:pendingAnswers.values())answer.completeExceptionally(new IOException("cloud peer stopped"));pendingAnswers.clear();
        new Thread(()->{
            awaitStopped(offers);awaitStopped(cleanup);
            for(CloudSession session:sessions.values())session.dispose();
            sessions.clear();
            PeerConnectionFactory old;
            synchronized(this){old=factory;factory=null;}
            if(old!=null)old.dispose();
            AppLog.info("Cloud peer client stopped");
        },"cloud-peer-shutdown").start();
    }

    private static void awaitStopped(ExecutorService executor){
        boolean interrupted=false;
        while(true)try{if(executor.awaitTermination(5,TimeUnit.SECONDS))break;}
        catch(InterruptedException ex){interrupted=true;}
        if(interrupted)Thread.currentThread().interrupt();
    }

    void run(BooleanSupplier stopped){
        try{
            WebRtcRuntime.ensureInitialized(storage);
            PeerConnectionFactory created=PeerConnectionFactory.builder().createPeerConnectionFactory();
            synchronized(this){if(closed){created.dispose();return;}factory=created;}
            AppLog.info("Cloud peer factory ready");
            while(!closed&&!stopped.getAsBoolean()){
                try{
                    JSONObject inbox=request("GET","/cloud/signal/"+id+"?wait=1",null);
                    if(closed)break;
                    JSONArray messages=inbox.optJSONArray("messages");
                    if(messages!=null)for(int i=0;i<messages.length();i++){
                        JSONObject signal=messages.optJSONObject(i);
						if(signal!=null&&"answer".equals(signal.optString("type"))){CompletableFuture<JSONObject> waiting=pendingAnswers.get(signal.optString("session"));if(waiting!=null)waiting.complete(signal);}
						else if(signal!=null&&"offer".equals(signal.optString("type"))&&signal.optString("session").matches("[0-9a-f]{32}")&&signal.optString("from").matches("[0-9a-f]{32}")){
                            AppLog.info("Cloud peer offer received");
                            offers.execute(()->accept(signal));
                        }
                    }
                    CloudStorageService.status="Hub 信令在线 · 等待节点直连";
                }catch(Exception ex){if(!closed){AppLog.error("Cloud peer signaling failed",ex);CloudStorageService.status="直连信令失败："+ex.getMessage();}}
                if(CloudStorageService.status.startsWith("直连信令失败"))try{Thread.sleep(3000);}catch(InterruptedException ex){Thread.currentThread().interrupt();return;}
            }
        }catch(Exception ex){AppLog.error("Cloud peer startup failed",ex);CloudStorageService.status="WebRTC 启动失败："+ex.getMessage();}
    }

    private JSONObject request(String method,String path,JSONObject body)throws Exception{
        HttpURLConnection conn=(HttpURLConnection)new URL(hub+path).openConnection();
        conn.setRequestMethod(method);conn.setConnectTimeout(10000);conn.setReadTimeout(30000);conn.setInstanceFollowRedirects(false);
        conn.setRequestProperty("Authorization","Bearer "+token);
        if(body!=null){byte[] data=body.toString().getBytes(StandardCharsets.UTF_8);conn.setDoOutput(true);conn.setRequestProperty("Content-Type","application/json");conn.setFixedLengthStreamingMode(data.length);try(OutputStream out=conn.getOutputStream()){out.write(data);}}
        try{int status=conn.getResponseCode();if(status!=200&&status!=204){AppLog.info("Cloud peer Hub HTTP "+status);throw new IOException("Hub HTTP "+status);}
            if(status==204)return new JSONObject();
            try(InputStream in=conn.getInputStream();ByteArrayOutputStream out=new ByteArrayOutputStream()){
                byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>128<<10)throw new IOException("signaling too large");}
                return new JSONObject(out.toString("UTF-8"));
            }
        }finally{conn.disconnect();}
    }

	CloudStorageService.PeerResult request(String target,String method,String path,byte[] payload)throws Exception{
		if(closed||factory==null||!target.matches("[0-9a-f]{32}")||target.equals(id))throw new IOException("peer unavailable");
		if(payload!=null&&payload.length>(4<<20)+64)throw new IOException("cloud request too large");
		String grant=storage.peerGrant();if(grant.isEmpty())throw new IOException("peer grant unavailable");
		byte[] random=new byte[16];new java.security.SecureRandom().nextBytes(random);StringBuilder sessionBuilder=new StringBuilder();for(byte value:random)sessionBuilder.append(String.format(Locale.ROOT,"%02x",value&255));String session=sessionBuilder.toString();
		CompletableFuture<JSONObject> answer=new CompletableFuture<>();pendingAnswers.put(session,answer);
		PeerConnection peer=null;
		try{
			JSONArray serverList=request("GET","/cloud/ice",null).optJSONArray("servers");List<PeerConnection.IceServer> ice=WebRtcIceServers.parse(serverList);AppLog.info("Cloud peer outbound connecting; "+WebRtcDiagnostics.network(storage)+"; "+WebRtcDiagnostics.servers(serverList));
			CountDownLatch gathered=new CountDownLatch(1);List<String> candidates=Collections.synchronizedList(new ArrayList<>());PeerConnection[] peerRef=new PeerConnection[1];
			peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
				@Override public void onSignalingChange(PeerConnection.SignalingState state){}
				@Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){AppLog.info("Cloud peer outbound ICE "+state+"; "+WebRtcDiagnostics.network(storage));if(state==PeerConnection.IceConnectionState.CONNECTED||state==PeerConnection.IceConnectionState.COMPLETED)WebRtcDiagnostics.selectedPair(peerRef[0],"Cloud peer outbound",summary->{if(summary!=null&&!summary.isEmpty())CloudStorageService.peerPairs.put(target,summary);});}
				@Override public void onIceConnectionReceivingChange(boolean receiving){}
				@Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){AppLog.info("Cloud peer outbound ICE gathering "+state);if(state==PeerConnection.IceGatheringState.COMPLETE)gathered.countDown();}
				@Override public void onIceCandidate(IceCandidate candidate){AppLog.info("Cloud peer outbound local "+WebRtcDiagnostics.candidate(candidate.sdp));candidates.add(candidate.sdp);}
				@Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
				@Override public void onAddStream(MediaStream stream){}
				@Override public void onRemoveStream(MediaStream stream){}
				@Override public void onDataChannel(DataChannel channel){}
				@Override public void onRenegotiationNeeded(){}
			});
			if(peer==null)throw new IOException("peer connection unavailable");
			peerRef[0]=peer;
			Outbound outbound=new Outbound(method,path,grant,payload==null?new byte[0]:payload);
			DataChannel channel=peer.createDataChannel("cloud",new DataChannel.Init());if(channel==null)throw new IOException("data channel unavailable");channel.registerObserver(outbound);outbound.channel=channel;
			CountDownLatch created=new CountDownLatch(1);SessionDescription[] offer=new SessionDescription[1];
			peer.createOffer(new SdpObserver(){
				@Override public void onCreateSuccess(SessionDescription value){offer[0]=value;created.countDown();}
				@Override public void onSetSuccess(){}
				@Override public void onCreateFailure(String error){created.countDown();}
				@Override public void onSetFailure(String error){created.countDown();}
			},new MediaConstraints());
			if(!created.await(10,TimeUnit.SECONDS)||offer[0]==null)throw new IOException("peer offer failed");
			setDescription(peer,offer[0],true);
			if(!gathered.await(12,TimeUnit.SECONDS))throw new IOException("ICE gathering timed out");
			request("POST","/cloud/signal",new JSONObject().put("from",id).put("to",target).put("session",session).put("type","offer").put("sdp",withCandidates(offer[0].description,candidates)));
			JSONObject remote=answer.get(15,TimeUnit.SECONDS);if(!target.equals(remote.optString("from")))throw new IOException("unexpected peer answer");
			WebRtcDiagnostics.sdpCandidates("Cloud peer outbound remote SDP",remote.optString("sdp"));setDescription(peer,new SessionDescription(SessionDescription.Type.ANSWER,remote.getString("sdp")),false);
			if(!outbound.done.await(path.startsWith("/cloud/diagnostic-payload")?90:20,TimeUnit.SECONDS))throw new IOException("peer response timed out");
			if(outbound.failure!=null)throw outbound.failure;
			return new CloudStorageService.PeerResult(outbound.status,outbound.response.toByteArray(),outbound.payloadDurationMillis);
		}finally{pendingAnswers.remove(session);if(peer!=null){peer.close();peer.dispose();}}
	}
	private static final class Outbound implements DataChannel.Observer {
		final String method,path,grant;final byte[] payload;final CountDownLatch done=new CountDownLatch(1);final ByteArrayOutputStream response=new ByteArrayOutputStream();
		volatile DataChannel channel;volatile IOException failure;volatile int status;volatile long payloadStarted,payloadDurationMillis;
		Outbound(String method,String path,String grant,byte[] payload){this.method=method;this.path=path;this.grant=grant;this.payload=payload;}
		@Override public void onBufferedAmountChange(long previousAmount){}
		@Override public void onStateChange(){if(channel==null)return;if(channel.state()==DataChannel.State.OPEN){new Thread(()->{
			try{send(new JSONObject().put("type","request").put("method",method).put("path",path).put("token",grant).toString().getBytes(StandardCharsets.UTF_8),false);
				for(int offset=0;offset<payload.length;offset+=16<<10)send(Arrays.copyOfRange(payload,offset,Math.min(offset+(16<<10),payload.length)),true);
				send("{\"type\":\"end\"}".getBytes(StandardCharsets.UTF_8),false);
			}catch(Exception ex){failure=new IOException("cloud peer send failed",ex);done.countDown();}
		},"cloud-peer-send").start();}else if(channel.state()==DataChannel.State.CLOSED&&done.getCount()>0){failure=new IOException("cloud peer channel closed");done.countDown();}}
		private void send(byte[] bytes,boolean binary)throws Exception{while(channel.bufferedAmount()>512<<10){if(channel.state()!=DataChannel.State.OPEN)throw new IOException("channel closed");Thread.sleep(5);}if(!channel.send(new DataChannel.Buffer(ByteBuffer.wrap(bytes),binary)))throw new IOException("send failed");}
		@Override public synchronized void onMessage(DataChannel.Buffer message){byte[] bytes=new byte[message.data.remaining()];message.data.get(bytes);
			if(message.binary){if(payloadStarted==0)payloadStarted=android.os.SystemClock.elapsedRealtime();if(response.size()+bytes.length>16<<20){failure=new IOException("peer response too large");done.countDown();}else response.write(bytes,0,bytes.length);return;}
			try{JSONObject control=new JSONObject(new String(bytes,StandardCharsets.UTF_8));if("response".equals(control.optString("type")))status=control.optInt("status");else if("end".equals(control.optString("type"))){if(status<100||status>599)failure=new IOException("invalid peer response");if(payloadStarted>0)payloadDurationMillis=Math.max(1,android.os.SystemClock.elapsedRealtime()-payloadStarted);done.countDown();}}
			catch(Exception ex){failure=new IOException("invalid peer response",ex);done.countDown();}
		}
	}

    private void accept(JSONObject offer){
        String session=offer.optString("session"),from=offer.optString("from");
        if(closed)return;
        CloudSession active=new CloudSession(session);
        if(sessions.putIfAbsent(session,active)!=null)return;
        boolean answeredSuccessfully=false;
        try{
            AppLog.info("Cloud peer preparing answer");
			JSONArray serverList=request("GET","/cloud/ice",null).optJSONArray("servers");AppLog.info("Cloud peer inbound connecting; "+WebRtcDiagnostics.network(storage)+"; "+WebRtcDiagnostics.servers(serverList));
            List<PeerConnection.IceServer> ice=WebRtcIceServers.parse(serverList);
            CountDownLatch gathered=new CountDownLatch(1);List<String> candidates=Collections.synchronizedList(new ArrayList<>());
            if(closed||factory==null)throw new IOException("Cloud peer is stopping");
            PeerConnection peer=factory.createPeerConnection(new PeerConnection.RTCConfiguration(ice),new PeerConnection.Observer(){
                @Override public void onSignalingChange(PeerConnection.SignalingState state){}
                @Override public void onIceConnectionChange(PeerConnection.IceConnectionState state){
                    AppLog.info("Cloud peer inbound ICE "+state+"; "+WebRtcDiagnostics.network(storage));
					if(state==PeerConnection.IceConnectionState.CONNECTED||state==PeerConnection.IceConnectionState.COMPLETED)WebRtcDiagnostics.selectedPair(active.peer,"Cloud peer inbound",summary->{if(summary!=null&&!summary.isEmpty())CloudStorageService.peerPairs.put(from,summary);});
                    if(state==PeerConnection.IceConnectionState.CLOSED||state==PeerConnection.IceConnectionState.FAILED)active.scheduleDispose();
                }
                @Override public void onIceConnectionReceivingChange(boolean receiving){}
                @Override public void onIceGatheringChange(PeerConnection.IceGatheringState state){AppLog.info("Cloud peer inbound ICE gathering "+state);if(state==PeerConnection.IceGatheringState.COMPLETE)gathered.countDown();}
                @Override public void onIceCandidate(IceCandidate candidate){AppLog.info("Cloud peer inbound local "+WebRtcDiagnostics.candidate(candidate.sdp));candidates.add(candidate.sdp);}
                @Override public void onIceCandidatesRemoved(IceCandidate[] candidates){}
                @Override public void onAddStream(MediaStream stream){}
                @Override public void onRemoveStream(MediaStream stream){}
                @Override public void onDataChannel(DataChannel channel){AppLog.info("Cloud peer data channel received");channel.registerObserver(new CloudChannel(channel,active));}
                @Override public void onRenegotiationNeeded(){}
            });
            if(peer==null)throw new IOException("WebRTC peer unavailable");active.peer=peer;
            AppLog.info("Cloud peer connection created");
            WebRtcDiagnostics.sdpCandidates("Cloud peer inbound remote SDP",offer.optString("sdp"));setDescription(peer,new SessionDescription(SessionDescription.Type.OFFER,offer.getString("sdp")),false);
            AppLog.info("Cloud peer remote offer set");
            CountDownLatch answered=new CountDownLatch(1);SessionDescription[] answer=new SessionDescription[1];
            peer.createAnswer(new SdpObserver(){
                @Override public void onCreateSuccess(SessionDescription value){answer[0]=value;answered.countDown();}
                @Override public void onSetSuccess(){}
                @Override public void onCreateFailure(String error){answered.countDown();}
                @Override public void onSetFailure(String error){answered.countDown();}
            },new MediaConstraints());
            if(!answered.await(15,TimeUnit.SECONDS)||answer[0]==null)throw new IOException("WebRTC answer failed");
            setDescription(peer,answer[0],true);
            AppLog.info("Cloud peer local answer set");
            if(!gathered.await(15,TimeUnit.SECONDS))throw new IOException("ICE gathering timed out");
            AppLog.info("Cloud peer ICE gathering complete");
            JSONObject signal=new JSONObject().put("from",id).put("to",from).put("session",session).put("type","answer").put("sdp",withCandidates(answer[0].description,candidates));
            request("POST","/cloud/signal",signal);
            AppLog.info("Cloud peer answer sent");
            CloudStorageService.status="节点直连信令已建立";
            answeredSuccessfully=true;
        }catch(Exception ex){AppLog.error("Cloud peer answer failed",ex);CloudStorageService.status="直连协商失败："+ex.getMessage();}
        finally{
            active.acceptDone.countDown();
            if(!answeredSuccessfully){active.dispose();sessions.remove(session,active);}
        }
    }

    private final class CloudSession {
        final String id;
        final CountDownLatch acceptDone=new CountDownLatch(1);
        final AtomicBoolean cleanupQueued=new AtomicBoolean();
        volatile PeerConnection peer;
        private boolean disposed;
        CloudSession(String id){this.id=id;}
        void scheduleDispose(){
            if(closed||!cleanupQueued.compareAndSet(false,true))return;
            try{cleanup.execute(()->{
                try{acceptDone.await();}catch(InterruptedException ex){Thread.currentThread().interrupt();}
                dispose();sessions.remove(id,this);
            });}catch(RejectedExecutionException ignored){}
        }
        synchronized void dispose(){
            if(disposed)return;
            disposed=true;
            PeerConnection current=peer;
            if(current!=null){current.dispose();AppLog.info("Cloud peer connection disposed");}
        }
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
	private static String withCandidates(String sdp,List<String> candidates){
		StringBuilder result=new StringBuilder(sdp);
		if(!sdp.endsWith("\r\n"))result.append("\r\n");
		synchronized(candidates){for(String candidate:candidates)if(candidate!=null&&!candidate.isEmpty()&&candidate.indexOf('\r')<0&&candidate.indexOf('\n')<0)result.append("a=").append(candidate.startsWith("a=")?candidate.substring(2):candidate).append("\r\n");}
		result.append("a=end-of-candidates\r\n");return result.toString();
	}

    private final class CloudChannel implements DataChannel.Observer {
        private final DataChannel channel;private final CloudSession owner;private final ByteArrayOutputStream body=new ByteArrayOutputStream();
        private String method,path,providedToken;private boolean done;
        CloudChannel(DataChannel channel,CloudSession owner){this.channel=channel;this.owner=owner;}
        @Override public void onBufferedAmountChange(long previousAmount){}
        @Override public void onStateChange(){}
        @Override public synchronized void onMessage(DataChannel.Buffer message){
            if(done)return;
            ByteBuffer buffer=message.data;byte[] data=new byte[buffer.remaining()];buffer.get(data);
            if(message.binary){if(method==null||body.size()+data.length>(4<<20)+64){done=true;owner.scheduleDispose();return;}body.write(data,0,data.length);return;}
            try{
                JSONObject control=new JSONObject(new String(data,StandardCharsets.UTF_8));
                if("request".equals(control.optString("type"))&&method==null){method=control.getString("method");path=control.getString("path");providedToken=control.getString("token");}
                else if("end".equals(control.optString("type"))&&method!=null){done=true;try{offers.execute(this::respond);}catch(RejectedExecutionException ignored){owner.scheduleDispose();}}
            }catch(Exception ex){done=true;owner.scheduleDispose();}
        }
        private void respond(){
            CloudStorageService.PeerResult result=storage.peerRequest(method,path,providedToken,body.toByteArray());
            AppLog.info("Cloud peer request status "+result.status+" bytes "+result.data.length);
            try{
                send(new JSONObject().put("type","response").put("status",result.status).toString().getBytes(StandardCharsets.UTF_8),false);
                for(int offset=0;offset<result.data.length;offset+=16<<10){int end=Math.min(offset+(16<<10),result.data.length);send(Arrays.copyOfRange(result.data,offset,end),true);}
                send("{\"type\":\"end\"}".getBytes(StandardCharsets.UTF_8),false);
                long deadline=System.currentTimeMillis()+15000;
                while(channel.bufferedAmount()>0&&System.currentTimeMillis()<deadline&&channel.state()==DataChannel.State.OPEN)Thread.sleep(10);
            }catch(Exception failure){AppLog.error("Cloud peer response failed",failure);}finally{try{channel.close();}catch(Exception failure){AppLog.error("Cloud peer channel close failed",failure);}owner.scheduleDispose();}
        }
        private void send(byte[] data,boolean binary)throws Exception{
            while(channel.bufferedAmount()>512<<10){if(channel.state()!=DataChannel.State.OPEN)throw new IOException("channel closed");Thread.sleep(5);}
            if(!channel.send(new DataChannel.Buffer(ByteBuffer.wrap(data),binary)))throw new IOException("channel send failed");
        }
    }
}
