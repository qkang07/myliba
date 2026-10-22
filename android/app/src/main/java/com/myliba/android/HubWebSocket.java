package com.myliba.android;

import android.util.Base64;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;

/** Small authenticated WSS transport shared by resource-node and RTC signaling. */
final class HubWebSocket implements Closeable {
    private static final int LIMIT=4<<20;
    private final SecureRandom random=new SecureRandom();
    private final SSLSocket socket;
    private final InputStream in;
    private final OutputStream out;
    private volatile boolean closed;

    HubWebSocket(String url,String authorization,String cookie,String origin)throws Exception{
        URI uri=new URI(url);
        if(!"wss".equals(uri.getScheme())||uri.getHost()==null||uri.getUserInfo()!=null||uri.getFragment()!=null)throw new IOException("invalid WSS URL");
        int port=uri.getPort()<0?443:uri.getPort();
        Socket raw=new Socket();raw.connect(new InetSocketAddress(uri.getHost(),port),10000);
        socket=(SSLSocket)((SSLSocketFactory)SSLSocketFactory.getDefault()).createSocket(raw,uri.getHost(),port,true);
        SSLParameters parameters=socket.getSSLParameters();parameters.setEndpointIdentificationAlgorithm("HTTPS");socket.setSSLParameters(parameters);
        socket.setSoTimeout(12000);socket.startHandshake();
        in=new BufferedInputStream(socket.getInputStream());out=socket.getOutputStream();
        byte[] nonce=new byte[16];random.nextBytes(nonce);String key=Base64.encodeToString(nonce,Base64.NO_WRAP);
        String path=uri.getRawPath();if(path==null||path.isEmpty())path="/";if(uri.getRawQuery()!=null)path+="?"+uri.getRawQuery();
        String host=uri.getHost()+(uri.getPort()<0?"":":"+port);
        StringBuilder request=new StringBuilder("GET ").append(path).append(" HTTP/1.1\r\nHost: ").append(host)
            .append("\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: ").append(key).append("\r\n");
        if(authorization!=null&&!authorization.isEmpty())request.append("Authorization: Bearer ").append(authorization).append("\r\n");
        if(cookie!=null&&!cookie.isEmpty())request.append("Cookie: ").append(cookie).append("\r\n");
        if(origin!=null&&!origin.isEmpty())request.append("Origin: ").append(origin).append("\r\n");
        out.write(request.append("\r\n").toString().getBytes(StandardCharsets.US_ASCII));out.flush();
        String status=line(in);if(!status.startsWith("HTTP/1.1 101 ")&&!status.startsWith("HTTP/1.0 101 "))throw new IOException("Hub WebSocket HTTP "+status);
        String accept="";for(int i=0;i<64;i++){String header=line(in);if(header.isEmpty())break;int colon=header.indexOf(':');if(colon>0&&header.substring(0,colon).equalsIgnoreCase("Sec-WebSocket-Accept"))accept=header.substring(colon+1).trim();}
        String expected=Base64.encodeToString(MessageDigest.getInstance("SHA-1").digest((key+"258EAFA5-E914-47DA-95CA-C5AB0DC85B11").getBytes(StandardCharsets.US_ASCII)),Base64.NO_WRAP);
        if(!expected.equals(accept))throw new IOException("Hub WebSocket handshake mismatch");
        socket.setSoTimeout(0);
    }

    synchronized void send(String value)throws IOException{frame(1,value.getBytes(StandardCharsets.UTF_8));}
    synchronized void ping()throws IOException{frame(9,new byte[0]);}
    private synchronized void frame(int opcode,byte[] data)throws IOException{
        if(closed||data.length>LIMIT)throw new IOException("WebSocket closed or frame too large");
        out.write(0x80|opcode);
        if(data.length<126)out.write(0x80|data.length);else if(data.length<=65535){out.write(0x80|126);out.write(data.length>>8);out.write(data.length);}else{out.write(0x80|127);long size=data.length;for(int i=7;i>=0;i--)out.write((int)(size>>(i*8))&255);}
        byte[] mask=new byte[4];random.nextBytes(mask);out.write(mask);
        byte[] masked=new byte[data.length];for(int i=0;i<data.length;i++)masked[i]=(byte)(data[i]^mask[i&3]);out.write(masked);out.flush();
    }
    String read()throws IOException{
        ByteArrayOutputStream message=new ByteArrayOutputStream();boolean started=false;
        while(true){int first=in.read(),second=in.read();if(first<0||second<0)throw new EOFException("Hub WebSocket disconnected");
            int opcode=first&15;boolean fin=(first&128)!=0;if((second&128)!=0)throw new IOException("masked Hub frame");
            long length=second&127;if(length==126){byte[] n=exact(2);length=((n[0]&255)<<8)|(n[1]&255);}else if(length==127){byte[] n=exact(8);length=0;for(byte b:n)length=(length<<8)|(b&255);}
            if(length>LIMIT||message.size()+length>LIMIT)throw new IOException("Hub frame too large");byte[] data=exact((int)length);
            if(opcode==8)throw new EOFException("Hub WebSocket closed");
            if(opcode==9){frame(10,data);continue;}
            if(opcode==10)continue;
            if(opcode!=1&&!(opcode==0&&started))throw new IOException("unexpected Hub frame");
            started=true;message.write(data,0,data.length);if(fin)return message.toString("UTF-8");
        }
    }
    private byte[] exact(int length)throws IOException{byte[] data=new byte[length];int offset=0;while(offset<length){int n=in.read(data,offset,length-offset);if(n<0)throw new EOFException();offset+=n;}return data;}
    private static String line(InputStream in)throws IOException{ByteArrayOutputStream out=new ByteArrayOutputStream();int ch;while((ch=in.read())!=-1){if(ch=='\n')break;if(ch!='\r')out.write(ch);if(out.size()>8192)throw new IOException("header too long");}if(ch<0)throw new EOFException();return out.toString("UTF-8");}
    @Override public void close(){closed=true;try{socket.close();}catch(IOException ignored){}}
}
