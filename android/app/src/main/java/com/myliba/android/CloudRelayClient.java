package com.myliba.android;

import android.util.Base64;
import org.json.JSONObject;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import java.util.function.BooleanSupplier;
import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

/** Outbound WebSocket from a private phone to a public storage relay. */
final class CloudRelayClient {
    private static final int LIMIT=18<<20;
    private final String url,id,token;
    private final int localPort;
    private final SecureRandom random=new SecureRandom();
    private volatile SSLSocket socket;
    private volatile boolean connected;
    CloudRelayClient(String url,String id,String token,int localPort){this.url=url;this.id=id;this.token=token;this.localPort=localPort;}
    void close(){SSLSocket current=socket;if(current!=null)try{current.close();}catch(IOException ignored){}}
    boolean isConnected(){return connected;}

    void run(BooleanSupplier stopped){
        while(!stopped.getAsBoolean()){
            try{connectAndServe(stopped);}catch(Exception ex){if(!stopped.getAsBoolean())CloudStorageService.status="中继断开，正在重连";}
            connected=false;CloudStorageService.relayConnected=false;close();
            if(stopped.getAsBoolean())return;
            try{Thread.sleep(5000);}catch(InterruptedException ex){Thread.currentThread().interrupt();return;}
        }
    }

    private void connectAndServe(BooleanSupplier stopped)throws Exception{
        URI uri=new URI(url);
        if(!"https".equals(uri.getScheme())||uri.getHost()==null||uri.getUserInfo()!=null||uri.getRawQuery()!=null||uri.getRawFragment()!=null||!(uri.getPath().isEmpty()||"/".equals(uri.getPath())))throw new IOException("invalid relay URL");
        int port=uri.getPort()<0?443:uri.getPort();
        Socket raw=new Socket();
        try{raw.connect(new InetSocketAddress(uri.getHost(),port),10000);}catch(IOException ex){raw.close();throw ex;}
        SSLSocket tls=(SSLSocket)((SSLSocketFactory)SSLSocketFactory.getDefault()).createSocket(raw,uri.getHost(),port,true);
        socket=tls;
        SSLParameters parameters=tls.getSSLParameters();parameters.setEndpointIdentificationAlgorithm("HTTPS");tls.setSSLParameters(parameters);
        tls.setSoTimeout(30000);tls.startHandshake();
        InputStream in=new BufferedInputStream(tls.getInputStream());OutputStream out=tls.getOutputStream();
        byte[] nonce=new byte[16];random.nextBytes(nonce);String key=Base64.encodeToString(nonce,Base64.NO_WRAP);
        String host=uri.getHost()+(uri.getPort()<0?"":":"+port);
        String request="GET /cloud/tunnel/"+id+" HTTP/1.1\r\nHost: "+host+"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: "+key+"\r\nAuthorization: Bearer "+token+"\r\n\r\n";
        out.write(request.getBytes(StandardCharsets.US_ASCII));out.flush();
        String status=line(in);if(!status.startsWith("HTTP/1.1 101 ")&&!status.startsWith("HTTP/1.0 101 "))throw new IOException("relay handshake failed");
        String accept="";for(int i=0;i<64;i++){String header=line(in);if(header.isEmpty())break;int colon=header.indexOf(':');if(colon>0&&header.substring(0,colon).equalsIgnoreCase("Sec-WebSocket-Accept"))accept=header.substring(colon+1).trim();}
        String expected=Base64.encodeToString(MessageDigest.getInstance("SHA-1").digest((key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes(StandardCharsets.US_ASCII)),Base64.NO_WRAP);
        if(!expected.equals(accept))throw new IOException("relay handshake mismatch");
        tls.setSoTimeout(0);connected=true;CloudStorageService.relayConnected=true;CloudStorageService.status="中继已连接";
        while(!stopped.getAsBoolean()){
            byte[] message=readMessage(in,out);
            if(message==null)return;
            JSONObject frame=new JSONObject(new String(message,StandardCharsets.UTF_8));
            String requestId=frame.optString("id"),method=frame.optString("method"),path=frame.optString("path");
            if(requestId.isEmpty()||!validRequest(method,path))throw new IOException("invalid relay request");
            byte[] body=Base64.decode(frame.optString("body",""),Base64.DEFAULT);
            if(body.length>(4<<20)+64)throw new IOException("relay request too large");
            LocalResponse response=localRequest(method,path,body);
            JSONObject reply=new JSONObject().put("id",requestId).put("status",response.status).put("body",Base64.encodeToString(response.body,Base64.NO_WRAP));
            byte[] data=reply.toString().getBytes(StandardCharsets.UTF_8);
            if(data.length>LIMIT)throw new IOException("relay response too large");
            writeFrame(out,2,data);
        }
    }

    private static boolean validRequest(String method,String path){
        if(path.contains("\r")||path.contains("\n"))return false;
        if("GET".equals(method)&&("/cloud/catalog".equals(path)||path.matches("/cloud/inventory(\\?after=[0-9a-f]{64})?")))return true;
        return ("GET".equals(method)||"HEAD".equals(method)||"PUT".equals(method))&&path.matches("/cloud/(blocks|catalog)/[0-9a-f]{64}");
    }
    private static String line(InputStream in)throws IOException{
        ByteArrayOutputStream out=new ByteArrayOutputStream();int ch;
        while((ch=in.read())!=-1){if(ch=='\n')break;if(ch!='\r')out.write(ch);if(out.size()>8192)throw new IOException("header too large");}
        if(ch<0)throw new EOFException();return out.toString("UTF-8");
    }
    private static byte[] exact(InputStream in,int length)throws IOException{
        byte[] data=new byte[length];int offset=0;while(offset<length){int n=in.read(data,offset,length-offset);if(n<0)throw new EOFException();offset+=n;}return data;
    }
    private LocalResponse localRequest(String method,String path,byte[] body)throws Exception{
        try(Socket local=new Socket()){local.connect(new InetSocketAddress("127.0.0.1",localPort),5000);local.setSoTimeout(90000);OutputStream out=local.getOutputStream();InputStream in=new BufferedInputStream(local.getInputStream());
            String request=method+" "+path+" HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer "+token+"\r\nContent-Length: "+body.length+"\r\nConnection: close\r\n\r\n";
            out.write(request.getBytes(StandardCharsets.US_ASCII));out.write(body);out.flush();
            String first=line(in);String[] parts=first.split(" ");if(parts.length<2)throw new IOException("invalid local response");int status=Integer.parseInt(parts[1]);
            int length=-1;for(int i=0;i<64;i++){String header=line(in);if(header.isEmpty())break;int colon=header.indexOf(':');if(colon>0&&header.substring(0,colon).equalsIgnoreCase("Content-Length"))length=Integer.parseInt(header.substring(colon+1).trim());}
            if(length<0||length>LIMIT/2)throw new IOException("local response too large");
            return new LocalResponse(status,"HEAD".equals(method)?new byte[0]:exact(in,length));
        }
    }
    private static final class LocalResponse{final int status;final byte[] body;LocalResponse(int status,byte[] body){this.status=status;this.body=body;}}

    private byte[] readMessage(InputStream in,OutputStream out)throws IOException{
        ByteArrayOutputStream message=new ByteArrayOutputStream();boolean started=false;
        while(true){int first=in.read(),second=in.read();if(first<0||second<0)return null;
            int opcode=first&15;boolean fin=(first&128)!=0;if((second&128)!=0)throw new IOException("masked server frame");
            long length=second&127;if(length==126){byte[] n=exact(in,2);length=((n[0]&255)<<8)|(n[1]&255);}else if(length==127){byte[] n=exact(in,8);length=0;for(byte b:n)length=(length<<8)|(b&255);}
            if(length>LIMIT||message.size()+length>LIMIT)throw new IOException("relay frame too large");byte[] data=exact(in,(int)length);
            if(opcode==8)return null;
            if(opcode==9){writeFrame(out,10,data);continue;}
            if(opcode==10)continue;
            if(opcode!=2&&!(opcode==0&&started))throw new IOException("unexpected relay frame");
            started=true;message.write(data);if(fin)return message.toByteArray();
        }
    }
    private void writeFrame(OutputStream out,int opcode,byte[] data)throws IOException{
        if(data.length>LIMIT)throw new IOException("relay frame too large");out.write(0x80|opcode);
        if(data.length<126)out.write(0x80|data.length);else if(data.length<=65535){out.write(0x80|126);out.write(data.length>>8);out.write(data.length);}else{out.write(0x80|127);long size=data.length;for(int i=7;i>=0;i--)out.write((int)(size>>(i*8))&255);}
        byte[] mask=new byte[4];random.nextBytes(mask);out.write(mask);
        byte[] masked=new byte[data.length];for(int i=0;i<data.length;i++)masked[i]=(byte)(data[i]^mask[i&3]);out.write(masked);out.flush();
    }
}
