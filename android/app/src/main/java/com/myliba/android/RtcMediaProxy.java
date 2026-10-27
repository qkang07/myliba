package com.myliba.android;

import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.*;

/** Loopback Range bridge so Android's media player can seek through RTC. */
final class RtcMediaProxy implements AutoCloseable {
    private final RemoteClient client;private final String path,key;
    private final ServerSocket listener;private final ExecutorService workers=Executors.newCachedThreadPool();
    private volatile boolean closed;
    RtcMediaProxy(RemoteClient client,String resourceID)throws Exception{
        this.client=client;path="/api/resources/"+RemoteClient.enc(resourceID)+"/file";key=UUID.randomUUID().toString();
        listener=new ServerSocket(0,8,InetAddress.getByName("127.0.0.1"));
        workers.execute(()->{while(!closed)try{Socket socket=listener.accept();workers.execute(()->serve(socket));}catch(IOException ex){if(!closed)break;}});
    }
    String url(){return "http://127.0.0.1:"+listener.getLocalPort()+"/"+key;}
    private static String line(InputStream in)throws IOException{ByteArrayOutputStream out=new ByteArrayOutputStream();int c;while((c=in.read())!=-1){if(c=='\n')break;if(c!='\r')out.write(c);if(out.size()>8192)throw new IOException("request too long");}if(c<0)throw new EOFException();return out.toString("UTF-8");}
    private static String header(JSONObject headers,String name){JSONArray values=headers.optJSONArray(name);return values==null?"":values.optString(0,"");}
    private void serve(Socket socket){
        try(Socket connection=socket){connection.setSoTimeout(30000);InputStream in=new BufferedInputStream(connection.getInputStream());OutputStream out=connection.getOutputStream();
            String first=line(in);String[] request=first.split(" ");if(request.length<2||!("GET".equals(request[0])||"HEAD".equals(request[0]))||!request[1].equals("/"+key)){out.write("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".getBytes(StandardCharsets.US_ASCII));return;}
            boolean head="HEAD".equals(request[0]);
            String range="";for(int i=0;i<64;i++){String value=line(in);if(value.isEmpty())break;int colon=value.indexOf(':');if(colon>0&&value.substring(0,colon).equalsIgnoreCase("Range"))range=value.substring(colon+1).trim();}
            client.streamFile(path,head?"bytes=0-0":range,head?new ByteArrayOutputStream():out,(status,headers)->{
                StringBuilder reply=new StringBuilder("HTTP/1.1 ").append(head?200:status).append(!head&&status==206?" Partial Content":" OK").append("\r\nConnection: close\r\n");
                for(String name:new String[]{"Content-Type","Content-Length","Content-Range","Accept-Ranges"}){if(head&&"Content-Range".equals(name))continue;String value=header(headers,name);
                    if(head&&"Content-Length".equals(name)){String contentRange=header(headers,"Content-Range");int slash=contentRange.lastIndexOf('/');if(slash>=0)value=contentRange.substring(slash+1);}
                    if(!value.isEmpty()&&!value.contains("\r")&&!value.contains("\n"))reply.append(name).append(": ").append(value).append("\r\n");}
                out.write(reply.append("\r\n").toString().getBytes(StandardCharsets.US_ASCII));out.flush();
            });
            out.flush();
        }catch(Exception ignored){}
    }
    @Override public void close(){
        closed=true;
        try{listener.close();}catch(IOException ignored){}
        workers.shutdownNow();
        // Activity teardown runs on the UI thread; closing the RTC WebSocket can
        // perform a blocking TLS socket write, so dispose the client off-thread.
        new Thread(client::close,"resource-media-rtc-close").start();
    }
}
