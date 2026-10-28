package com.myliba.android;

import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.*;

/** Loopback Range bridge so Android's media player can seek through RTC. */
final class RtcMediaProxy implements AutoCloseable {
    private static final long CHUNK_SIZE=512L*1024;
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
            JSONObject[] probe={null};
            client.streamFile(path,"bytes=0-0",new ByteArrayOutputStream(),(status,headers)->{
                if(status!=206||!header(headers,"Content-Range").matches("bytes 0-0/[0-9]+"))throw new IOException("资源节点不支持视频分段读取");
                probe[0]=headers;
            });
            if(probe[0]==null)throw new IOException("无法读取视频信息");
            String contentRange=header(probe[0],"Content-Range");long total=Long.parseLong(contentRange.substring(contentRange.lastIndexOf('/')+1));
            if(total<=0)throw new IOException("视频长度无效");
            long start=0,end=total-1;
            if(!range.isEmpty()){
                if(!range.matches("bytes=[0-9]*-[0-9]*"))throw new IOException("视频范围无效");
                String[] bounds=range.substring(6).split("-",-1);
                if(bounds[0].isEmpty()){if(bounds[1].isEmpty())throw new IOException("视频范围无效");long suffix=Long.parseLong(bounds[1]);if(suffix<=0)throw new IOException("视频范围无效");start=Math.max(0,total-suffix);}
                else{start=Long.parseLong(bounds[0]);if(!bounds[1].isEmpty())end=Math.min(end,Long.parseLong(bounds[1]));}
                if(start>=total||end<start){out.write(("HTTP/1.1 416 Range Not Satisfiable\r\nContent-Range: bytes */"+total+"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));return;}
            }
            String type=header(probe[0],"Content-Type");if(type.isEmpty()||type.contains("\r")||type.contains("\n"))type="application/octet-stream";
            StringBuilder reply=new StringBuilder("HTTP/1.1 ").append(range.isEmpty()?"200 OK":"206 Partial Content").append("\r\nConnection: close\r\nAccept-Ranges: bytes\r\nContent-Type: ").append(type).append("\r\nContent-Length: ").append(end-start+1).append("\r\n");
            if(!range.isEmpty())reply.append("Content-Range: bytes ").append(start).append('-').append(end).append('/').append(total).append("\r\n");
            out.write(reply.append("\r\n").toString().getBytes(StandardCharsets.US_ASCII));out.flush();
            if(!head)for(long offset=start;offset<=end&&!closed;){
                long last=Math.min(end,offset+CHUNK_SIZE-1);String expected="bytes "+offset+"-"+last+"/"+total;
                client.streamFile(path,"bytes="+offset+"-"+last,out,(status,headers)->{if(status!=206||!expected.equals(header(headers,"Content-Range")))throw new IOException("视频分段内容发生变化");});
                offset=last+1;
            }
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
