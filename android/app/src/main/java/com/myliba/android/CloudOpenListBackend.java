package com.myliba.android;

import android.net.Uri;
import android.util.Base64;
import android.util.Xml;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.*;
import javax.net.ssl.SSLParameters;
import javax.net.ssl.SSLSocket;
import javax.net.ssl.SSLSocketFactory;
import org.xmlpull.v1.XmlPullParser;

/** Stores opaque cloud objects in one writable OpenList mount over WebDAV. */
final class CloudOpenListBackend {
    private static final int MAX_LIST=16<<20;
    private final URI server;
    private final String rootPath,authorization;
    private volatile boolean directoriesReady;

    CloudOpenListBackend(String address,String mount,String username,String password)throws Exception{
        server=new URI(address.trim());
        if(!"https".equalsIgnoreCase(server.getScheme())||server.getHost()==null||server.getUserInfo()!=null||server.getRawQuery()!=null||server.getRawFragment()!=null||server.getPort()==0||server.getPort()>65535)
            throw new IOException("OpenList 需要有效的 HTTPS 服务地址");
        if(username.isEmpty()||username.contains(":")||username.contains("\r")||username.contains("\n"))throw new IOException("OpenList 用户名无效");
        if(password.contains("\r")||password.contains("\n"))throw new IOException("OpenList 密码无效");
        String[] segments=mount.trim().replaceAll("^/+|/+$","").split("/",-1);
        if(segments.length==0||segments[0].isEmpty())throw new IOException("需要 OpenList 可写挂载路径");
        StringBuilder path=new StringBuilder(server.getRawPath()==null?"":server.getRawPath().replaceAll("/+$",""));
        path.append("/dav");
        for(String segment:segments){if(segment.isEmpty()||segment.equals(".")||segment.equals("..")||segment.contains("\\"))throw new IOException("OpenList 挂载路径无效");path.append('/').append(Uri.encode(segment));}
        rootPath=path.toString();
        authorization="Basic "+Base64.encodeToString((username+":"+password).getBytes(StandardCharsets.UTF_8),Base64.NO_WRAP);
    }

    private static boolean valid(String kind,String id){return ("blocks".equals(kind)||"catalog".equals(kind))&&id!=null&&id.matches("[0-9a-f]{64}");}
    private synchronized void ensureDirectories()throws Exception{
        if(directoriesReady)return;
        for(String kind:new String[]{"blocks","catalog"}){
            Response response=request("MKCOL",kind+"/",null,null,4096);
            if(response.status!=201&&response.status!=405)throw new IOException("OpenList 创建 "+kind+" 失败：HTTP "+response.status);
        }
        directoriesReady=true;
    }
    void put(String kind,String id,byte[] data)throws Exception{
        if(!valid(kind,id))throw new IOException("无效的云层对象");
        ensureDirectories();
        Response response=request("PUT",kind+"/"+id,data,null,4096);
        if(response.status!=200&&response.status!=201&&response.status!=204)throw new IOException("OpenList 写入失败：HTTP "+response.status);
    }
    byte[] get(String kind,String id,int limit)throws Exception{
        if(!valid(kind,id))throw new IOException("无效的云层对象");
        ensureDirectories();
        Response response=request("GET",kind+"/"+id,null,null,limit);
        if(response.status==404)throw new FileNotFoundException(id);
        if(response.status!=200)throw new IOException("OpenList 读取失败：HTTP "+response.status);
        return response.body;
    }
    List<String> list(String kind)throws Exception{
        if(!"blocks".equals(kind)&&!"catalog".equals(kind))throw new IOException("无效的云层目录");
        ensureDirectories();
        byte[] xml="<?xml version=\"1.0\"?><d:propfind xmlns:d=\"DAV:\"><d:prop><d:resourcetype/></d:prop></d:propfind>".getBytes(StandardCharsets.UTF_8);
        Response response=request("PROPFIND",kind+"/",xml,"Depth: 1\r\nContent-Type: application/xml\r\n",MAX_LIST);
        if(response.status!=207)throw new IOException("OpenList 列表失败：HTTP "+response.status);
        Set<String> ids=new TreeSet<>();
        XmlPullParser parser=Xml.newPullParser();parser.setInput(new InputStreamReader(new ByteArrayInputStream(response.body),StandardCharsets.UTF_8));
        for(int event=parser.getEventType();event!=XmlPullParser.END_DOCUMENT;event=parser.next()){
            if(event!=XmlPullParser.START_TAG||!"href".equals(parser.getName()))continue;
            String href=parser.nextText();
            try{String path=new URI(href).getPath();String name=path.substring(path.lastIndexOf('/')+1);if(name.matches("[0-9a-f]{64}"))ids.add(name);}catch(Exception ignored){}
        }
        return new ArrayList<>(ids);
    }

    private Response request(String method,String key,byte[] body,String extraHeaders,int limit)throws Exception{
        int port=server.getPort()<0?443:server.getPort();
        Socket raw=new Socket();
        try{raw.connect(new InetSocketAddress(server.getHost(),port),10000);}catch(IOException ex){raw.close();throw ex;}
        try(SSLSocket tls=(SSLSocket)((SSLSocketFactory)SSLSocketFactory.getDefault()).createSocket(raw,server.getHost(),port,true)){
            SSLParameters parameters=tls.getSSLParameters();parameters.setEndpointIdentificationAlgorithm("HTTPS");tls.setSSLParameters(parameters);
            tls.setSoTimeout(60000);tls.startHandshake();
            String host=server.getHost().contains(":")?"["+server.getHost()+"]":server.getHost();if(server.getPort()>0)host+=":"+port;
            String path=rootPath+"/"+key;
            String headers=method+" "+path+" HTTP/1.1\r\nHost: "+host+"\r\nAuthorization: "+authorization+"\r\nAccept-Encoding: identity\r\nConnection: close\r\n"+
                (body==null?"":"Content-Length: "+body.length+"\r\n")+("PUT".equals(method)?"Content-Type: application/octet-stream\r\n":"")+(extraHeaders==null?"":extraHeaders)+"\r\n";
            OutputStream out=tls.getOutputStream();out.write(headers.getBytes(StandardCharsets.US_ASCII));if(body!=null)out.write(body);out.flush();
            InputStream in=new BufferedInputStream(tls.getInputStream());String first=line(in);String[] parts=first.split(" ");if(parts.length<2)throw new IOException("OpenList 响应无效");
            int status=Integer.parseInt(parts[1]),contentLength=-1;boolean chunked=false;int headerBytes=0;String header;
            while(!(header=line(in)).isEmpty()){
                headerBytes+=header.length();if(headerBytes>32768)throw new IOException("OpenList 响应头过大");int colon=header.indexOf(':');if(colon<0)continue;
                String name=header.substring(0,colon).trim().toLowerCase(Locale.ROOT),value=header.substring(colon+1).trim();
                if("content-length".equals(name))contentLength=Integer.parseInt(value);
                if("transfer-encoding".equals(name)&&value.toLowerCase(Locale.ROOT).contains("chunked"))chunked=true;
                if("content-encoding".equals(name)&&!"identity".equalsIgnoreCase(value))throw new IOException("OpenList 返回了未请求的压缩内容");
            }
            if(status==204||status==304)return new Response(status,new byte[0]);
            byte[] data=chunked?readChunked(in,limit):contentLength>=0?readExact(in,contentLength,limit):readUntilEOF(in,limit);
            return new Response(status,data);
        }
    }
    private static String line(InputStream in)throws IOException{
        ByteArrayOutputStream out=new ByteArrayOutputStream();int ch;
        while((ch=in.read())!=-1){if(ch=='\n')break;if(ch!='\r')out.write(ch);if(out.size()>8192)throw new IOException("OpenList 响应头过大");}
        if(ch<0)throw new EOFException();return out.toString("UTF-8");
    }
    private static byte[] readExact(InputStream in,int length,int limit)throws IOException{
        if(length<0||length>limit)throw new IOException("OpenList 对象过大");byte[] data=new byte[length];int offset=0;
        while(offset<length){int n=in.read(data,offset,length-offset);if(n<0)throw new EOFException();offset+=n;}return data;
    }
    private static byte[] readUntilEOF(InputStream in,int limit)throws IOException{
        ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] buffer=new byte[8192];int n;
        while((n=in.read(buffer))!=-1){if(out.size()+n>limit)throw new IOException("OpenList 对象过大");out.write(buffer,0,n);}return out.toByteArray();
    }
    private static byte[] readChunked(InputStream in,int limit)throws IOException{
        ByteArrayOutputStream out=new ByteArrayOutputStream();
        while(true){String size=line(in).split(";",2)[0].trim();long count=Long.parseLong(size,16);
            if(count==0){while(!line(in).isEmpty()){}return out.toByteArray();}
            if(count<0||count>limit-out.size())throw new IOException("OpenList 对象过大");
            out.write(readExact(in,(int)count,limit));if(in.read()!='\r'||in.read()!='\n')throw new IOException("OpenList 分块响应无效");
        }
    }
    private static final class Response{final int status;final byte[] body;Response(int status,byte[] body){this.status=status;this.body=body;}}
}
