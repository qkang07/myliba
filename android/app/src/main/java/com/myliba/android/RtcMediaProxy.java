package com.myliba.android;

import org.json.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;

/** Loopback Range bridge so Android's media player can seek through RTC. */
final class RtcMediaProxy implements AutoCloseable {
    private static final long CHUNK_SIZE=512L*1024;
    private final RemoteClient client;private final String path,key,videoBase,quality;
    private final ConcurrentHashMap<Integer,AtomicBoolean> activeSegments=new ConcurrentHashMap<>();
    private final ServerSocket listener;private final ExecutorService workers=Executors.newCachedThreadPool();
    private volatile boolean closed;
    RtcMediaProxy(RemoteClient client,String resourceID)throws Exception{
        this(client,resourceID,"original");
    }
    RtcMediaProxy(RemoteClient client,String resourceID,String quality)throws Exception{
        if(!quality.matches("original|360p|720p|1080p"))throw new IOException("不支持的清晰度");
        this.client=client;videoBase="/api/resources/"+RemoteClient.enc(resourceID)+"/video/"+quality;this.quality=quality;path="/api/resources/"+RemoteClient.enc(resourceID)+"/file";key=UUID.randomUUID().toString();
        listener=new ServerSocket(0,8,InetAddress.getByName("127.0.0.1"));
        workers.execute(()->{while(!closed)try{Socket socket=listener.accept();workers.execute(()->serve(socket));}catch(IOException ex){if(!closed)break;}});
    }
    String url(){return "http://127.0.0.1:"+listener.getLocalPort()+"/"+key+(quality.equals("original")?"":"/index.m3u8");}
    private static String line(InputStream in)throws IOException{ByteArrayOutputStream out=new ByteArrayOutputStream();int c;while((c=in.read())!=-1){if(c=='\n')break;if(c!='\r')out.write(c);if(out.size()>8192)throw new IOException("request too long");}if(c<0)throw new EOFException();return out.toString("UTF-8");}
    private static String header(JSONObject headers,String name){JSONArray values=headers.optJSONArray(name);return values==null?"":values.optString(0,"");}
    private void serve(Socket socket){
        try(Socket connection=socket){connection.setSoTimeout(30000);InputStream in=new BufferedInputStream(connection.getInputStream());OutputStream out=connection.getOutputStream();
            String first=line(in);String[] request=first.split(" ");if(request.length<2||!("GET".equals(request[0])||"HEAD".equals(request[0]))||!(quality.equals("original")?request[1].equals("/"+key):request[1].startsWith("/"+key+"/"))){out.write("HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n".getBytes(StandardCharsets.US_ASCII));return;}
            boolean head="HEAD".equals(request[0]);
            String range="";for(int i=0;i<64;i++){String value=line(in);if(value.isEmpty())break;int colon=value.indexOf(':');if(colon>0&&value.substring(0,colon).equalsIgnoreCase("Range"))range=value.substring(colon+1).trim();}
            if(!quality.equals("original")){serveAdaptive(request[1].substring(("/"+key+"/").length()),head,range,out);return;}
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
    private void serveAdaptive(String part,boolean head,String range,OutputStream out)throws Exception{
        if(part.equals("index.m3u8")){
            ByteArrayOutputStream body=new ByteArrayOutputStream();
            OutputStream limited=new OutputStream(){@Override public void write(int value)throws IOException{if(body.size()>=2*1024*1024)throw new IOException("视频索引过大");body.write(value);}@Override public void write(byte[] data,int offset,int length)throws IOException{if(length>2*1024*1024-body.size())throw new IOException("视频索引过大");body.write(data,offset,length);}};
            client.streamFile(videoBase+"?mode=ondemand",null,limited,(status,headers)->{if(status!=200)throw new IOException("无法准备视频流");});
            JSONObject manifest=new JSONObject(body.toString("UTF-8"));JSONArray segments=manifest.optJSONArray("segments");
            if(!"ondemand".equals(manifest.optString("mode"))||segments==null||segments.length()==0||segments.length()>21600)throw new IOException("节点不支持按需转码");
            StringBuilder playlist=new StringBuilder("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-MAP:URI=\"init.mp4\"\n");
            for(int i=0;i<segments.length();i++){double duration=segments.optDouble(i);if(!Double.isFinite(duration)||duration<=0||duration>4.001)throw new IOException("视频索引无效");if(i>0)playlist.append("#EXT-X-DISCONTINUITY\n");playlist.append("#EXTINF:").append(String.format(java.util.Locale.ROOT,"%.6f",duration)).append(",\nsegment-").append(i).append(".m4s\n");}
            byte[] bytes=playlist.append("#EXT-X-ENDLIST\n").toString().getBytes(StandardCharsets.UTF_8);
            out.write(("HTTP/1.1 200 OK\r\nContent-Type: application/vnd.apple.mpegurl\r\nContent-Length: "+bytes.length+"\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.US_ASCII));if(!head)out.write(bytes);out.flush();return;
        }
        String suffix;AtomicBoolean cancel=null;int segmentIndex=-1;
        if(part.equals("init.mp4"))suffix="init";
        else if(part.matches("segment-[0-9]{1,5}\\.m4s")){
            int index=Integer.parseInt(part.substring(8,part.length()-4));if(index>21600)throw new IOException("视频分段无效");suffix="segments/"+index;segmentIndex=index;
            cancel=new AtomicBoolean();for(java.util.Map.Entry<Integer,AtomicBoolean> entry:activeSegments.entrySet())if(Math.abs(entry.getKey()-index)>1)entry.getValue().set(true);
            AtomicBoolean previous=activeSegments.put(index,cancel);if(previous!=null)previous.set(true);
        }else throw new IOException("视频分段无效");
        AtomicBoolean current=cancel;
        String requestRange=head?"bytes=0-0":range;
        try{client.streamFile(videoBase+"/"+suffix+"?mode=ondemand",requestRange,head?new ByteArrayOutputStream():out,(status,headers)->{
            if(status!=200&&status!=206)throw new IOException("视频分段不可用");
            String type=header(headers,"Content-Type"),length=header(headers,"Content-Length"),contentRange=header(headers,"Content-Range");
            if(head&&status==206){int slash=contentRange.lastIndexOf('/');if(slash>=0)length=contentRange.substring(slash+1);}
            if(type.contains("\r")||type.contains("\n")||!length.matches("[0-9]+")||contentRange.contains("\r")||contentRange.contains("\n"))throw new IOException("视频分段响应无效");
            StringBuilder reply=new StringBuilder("HTTP/1.1 ").append(!head&&status==206?"206 Partial Content":"200 OK").append("\r\nContent-Type: ").append(type).append("\r\nContent-Length: ").append(length).append("\r\nAccept-Ranges: bytes\r\nCache-Control: no-store\r\nConnection: close\r\n");
            if(!head&&status==206&&!contentRange.isEmpty())reply.append("Content-Range: ").append(contentRange).append("\r\n");
            out.write(reply.append("\r\n").toString().getBytes(StandardCharsets.US_ASCII));out.flush();
        },current);}finally{if(current!=null)activeSegments.remove(segmentIndex,current);}
    }
    @Override public void close(){
        closed=true;
        for(AtomicBoolean active:activeSegments.values())active.set(true);activeSegments.clear();
        try{listener.close();}catch(IOException ignored){}
        workers.shutdownNow();
        // Activity teardown runs on the UI thread; closing the RTC WebSocket can
        // perform a blocking TLS socket write, so dispose the client off-thread.
        new Thread(client::close,"resource-media-rtc-close").start();
    }
}
