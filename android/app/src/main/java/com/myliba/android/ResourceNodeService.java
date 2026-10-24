package com.myliba.android;

import android.app.*;
import android.content.*;
import android.content.pm.ServiceInfo;
import android.os.*;
import android.provider.Settings;
import android.util.Base64;
import org.json.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.*;

/** User-started foreground resource node, separate from encrypted cloud storage. */
public final class ResourceNodeService extends Service {
    static final String PREFS="resource_node";
    static volatile boolean running;
    static volatile String status="未启动";
    private final ExecutorService workers=Executors.newCachedThreadPool();
    private volatile boolean stopped;
    private volatile HubWebSocket socket;
    private NodeResourceApi api;
    private ResourceNodeRTC rtc;
    private String hub,token,id,name;

    static String ensureID(Context context){
        SharedPreferences p=context.getSharedPreferences(PREFS,MODE_PRIVATE);
        String id=p.getString("id","");if(!id.isEmpty())return id;
        String device=Settings.Secure.getString(context.getContentResolver(),Settings.Secure.ANDROID_ID);
        id=device==null||device.isEmpty()?UUID.randomUUID().toString():UUID.nameUUIDFromBytes(("myliba-resource-node:"+device).getBytes(StandardCharsets.UTF_8)).toString();
        p.edit().putString("id",id).apply();return id;
    }

    static void savePaired(Context context,String hub,String id,String token)throws Exception{
        context.getSharedPreferences(PREFS,MODE_PRIVATE).edit().putString("hub",hub).putString("id",id)
            .putString("token",CloudSecretStore.protect(token)).putBoolean("paired",true).apply();
    }

    static boolean clearPaired(Context context,String hub){
        SharedPreferences p=context.getSharedPreferences(PREFS,MODE_PRIVATE);
        if(p.getBoolean("paired",false)&&hub.equals(p.getString("hub",""))){p.edit().remove("token").remove("paired").apply();return true;}
        return false;
    }

    @Override public void onCreate(){super.onCreate();getSystemService(NotificationManager.class).createNotificationChannel(new NotificationChannel("resource-node","Myliba 资源节点",NotificationManager.IMPORTANCE_LOW));}
    @Override public int onStartCommand(Intent intent,int flags,int startId){
        if(running)return START_STICKY;
        SharedPreferences p=getSharedPreferences(PREFS,MODE_PRIVATE);
        hub=p.getString("hub","");name=p.getString("name",android.os.Build.MODEL);
        try{token=CloudSecretStore.read(p,"token");}catch(Exception ex){status="节点密钥读取失败";stopSelf();return START_NOT_STICKY;}
        if(!hub.startsWith("https://")||token.length()<32||name.trim().isEmpty()){status="资源节点配置无效";stopSelf();return START_NOT_STICKY;}
        id=ensureID(this);
        Intent open=new Intent(this,MainActivity.class);PendingIntent click=PendingIntent.getActivity(this,1,open,PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        Notification notification=new Notification.Builder(this,"resource-node").setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle("Myliba 手机资源节点运行中").setContentText("经 Hub 向其他客户端开放已索引资源")
            .setContentIntent(click).setOngoing(true).build();
        if(Build.VERSION.SDK_INT>=34)startForeground(4320,notification,ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);else startForeground(4320,notification);
        api=new NodeResourceApi(this);stopped=false;running=true;status="正在连接 Hub";
        AppLog.info("Resource node starting");
        try{rtc=new ResourceNodeRTC(this,api,workers);}catch(Exception ex){AppLog.error("Resource node WebRTC startup failed",ex);status="WebRTC 启动失败："+ex.getMessage();running=false;api.close();stopSelf();return START_NOT_STICKY;}
        workers.execute(this::run);
        return START_STICKY;
    }

    private void run(){
        while(!stopped){
            try{
                String wss=hub.replaceFirst("^https:","wss:")+"/node?id="+id+"&name="+java.net.URLEncoder.encode(name,"UTF-8")+"&kind=android";
                HubWebSocket current=new HubWebSocket(wss,token,null,null);socket=current;rtc.setSocket(current);status="已向 Hub 注册 · 等待客户端直连";
                AppLog.info("Resource node registered with Hub");
                workers.execute(()->{while(!stopped&&socket==current){try{Thread.sleep(25000);if(!stopped&&socket==current)current.ping();}catch(Exception ex){current.close();return;}}});
                while(!stopped){JSONObject frame=new JSONObject(current.read());String type=frame.optString("type");
                    if("request".equals(type))workers.execute(()->respond(current,frame));
                    else if("rtc-signal".equals(type))rtc.signal(frame);
                    else if("node-config".equals(type))rtc.setICEServers(frame.optJSONArray("iceServers"));
                }
            }catch(Exception ex){if(!stopped){AppLog.error("Resource node Hub connection interrupted",ex);status="Hub 连接中断，正在重试："+ex.getMessage();}}
            HubWebSocket old=socket;socket=null;if(old!=null)old.close();rtc.closeSessions();
            if(!stopped)try{Thread.sleep(3000);}catch(InterruptedException ex){Thread.currentThread().interrupt();break;}
        }
    }

    private void respond(HubWebSocket current,JSONObject request){
        String requestID=request.optString("id");if(requestID.isEmpty())return;
        boolean started=false;
        try{
            byte[] body=Base64.decode(request.optString("body",""),Base64.DEFAULT);
            try(NodeResourceApi.Result result=api.handle(request.optString("method"),request.optString("path"),body,request.optJSONObject("headers")==null?new JSONObject():request.getJSONObject("headers"))){
                current.send(new JSONObject().put("type","start").put("id",requestID).put("status",result.status).put("headers",result.headers).toString());
                started=true;
                byte[] buffer=new byte[24<<10];int count;while((count=result.body.read(buffer))!=-1){
                    byte[] chunk=java.util.Arrays.copyOf(buffer,count);
                    current.send(new JSONObject().put("type","chunk").put("id",requestID).put("data",Base64.encodeToString(chunk,Base64.NO_WRAP)).toString());
                }
            }
        }catch(Exception ex){if(!started)try{current.send(new JSONObject().put("type","start").put("id",requestID).put("status",500).toString());}catch(Exception ignored){}}
        finally{try{current.send(new JSONObject().put("type","end").put("id",requestID).toString());}catch(Exception ignored){}}
    }

    @Override public void onDestroy(){AppLog.info("Resource node stopping");stopped=true;running=false;status="已停止";HubWebSocket current=socket;if(current!=null)current.close();if(rtc!=null)rtc.close();if(api!=null)api.close();workers.shutdownNow();super.onDestroy();}
    @Override public IBinder onBind(Intent intent){return null;}
}
