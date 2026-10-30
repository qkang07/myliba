package com.myliba.android;

import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import org.json.JSONArray;
import java.io.*;
import java.math.BigInteger;
import java.net.*;
import java.security.*;
import java.security.cert.Certificate;
import java.security.cert.X509Certificate;
import java.util.*;
import javax.net.ssl.*;
import javax.security.auth.x500.X500Principal;

/** A private-address TLS endpoint with a persistent, Hub-advertised certificate pin. */
final class CloudLan {
    final SSLServerSocket listener;
    final String fingerprint;

    CloudLan(String nodeId)throws Exception {
        String alias="myliba-cloud-lan-"+nodeId;
        KeyStore store=KeyStore.getInstance("AndroidKeyStore");store.load(null);
        if(!store.containsAlias(alias)){
            KeyPairGenerator generator=KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_RSA,"AndroidKeyStore");
            Calendar start=Calendar.getInstance(),end=Calendar.getInstance();start.add(Calendar.HOUR,-1);end.add(Calendar.YEAR,20);
            generator.initialize(new KeyGenParameterSpec.Builder(alias,KeyProperties.PURPOSE_SIGN|KeyProperties.PURPOSE_VERIFY)
                .setKeySize(2048).setDigests(KeyProperties.DIGEST_SHA256,KeyProperties.DIGEST_NONE)
                .setSignaturePaddings(KeyProperties.SIGNATURE_PADDING_RSA_PKCS1)
                .setCertificateSubject(new X500Principal("CN=Myliba cloud LAN node"))
                .setCertificateSerialNumber(BigInteger.valueOf(1))
                .setCertificateNotBefore(start.getTime()).setCertificateNotAfter(end.getTime()).build());
            generator.generateKeyPair();
            store.load(null);
        }
        Certificate certificate=store.getCertificate(alias);
        if(certificate==null)throw new IOException("LAN certificate unavailable");
        fingerprint=hex(MessageDigest.getInstance("SHA-256").digest(certificate.getEncoded()));
        KeyManagerFactory keys=KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());keys.init(store,null);
		X509KeyManager delegate=null;for(KeyManager candidate:keys.getKeyManagers())if(candidate instanceof X509KeyManager){delegate=(X509KeyManager)candidate;break;}
		if(delegate==null)throw new IOException("LAN key manager unavailable");
		final X509KeyManager selected=delegate;
		X509ExtendedKeyManager fixed=new X509ExtendedKeyManager(){
			@Override public String[] getClientAliases(String type,java.security.Principal[] issuers){return selected.getClientAliases(type,issuers);}
			@Override public String chooseClientAlias(String[] types,java.security.Principal[] issuers,Socket socket){return selected.chooseClientAlias(types,issuers,socket);}
			@Override public String[] getServerAliases(String type,java.security.Principal[] issuers){return new String[]{alias};}
			@Override public String chooseServerAlias(String type,java.security.Principal[] issuers,Socket socket){return alias;}
			@Override public String chooseEngineServerAlias(String type,java.security.Principal[] issuers,SSLEngine engine){return alias;}
			@Override public X509Certificate[] getCertificateChain(String name){return selected.getCertificateChain(name);}
			@Override public PrivateKey getPrivateKey(String name){return selected.getPrivateKey(name);}
		};
		SSLContext tls=SSLContext.getInstance("TLS");tls.init(new KeyManager[]{fixed},null,null);
        listener=(SSLServerSocket)tls.getServerSocketFactory().createServerSocket(0,32,InetAddress.getByName("0.0.0.0"));
	}
	JSONArray addresses()throws Exception{
		JSONArray addresses=new JSONArray();
        Enumeration<NetworkInterface> interfaces=NetworkInterface.getNetworkInterfaces();
        while(interfaces!=null&&interfaces.hasMoreElements()&&addresses.length()<8){
            NetworkInterface iface=interfaces.nextElement();if(!iface.isUp()||iface.isLoopback())continue;
            Enumeration<InetAddress> ips=iface.getInetAddresses();
            while(ips.hasMoreElements()&&addresses.length()<8){
                InetAddress ip=ips.nextElement();
                if(ip instanceof Inet4Address&&ip.isSiteLocalAddress())addresses.put("https://"+ip.getHostAddress()+":"+listener.getLocalPort());
            }
        }
		return addresses;
    }

    private static String hex(byte[] data){StringBuilder out=new StringBuilder();for(byte value:data)out.append(String.format(Locale.ROOT,"%02x",value&255));return out.toString();}

    static CloudStorageService.PeerResult request(JSONArray addresses,String fingerprint,String method,String path,String grant,byte[] data)throws Exception {
        if(addresses==null||addresses.length()==0||!fingerprint.matches("[0-9a-f]{64}"))throw new IOException("LAN identity unavailable");
        SSLContext tls=SSLContext.getInstance("TLS");
        tls.init(null,new TrustManager[]{new X509TrustManager(){
            @Override public void checkClientTrusted(X509Certificate[] chain,String authType)throws java.security.cert.CertificateException{throw new java.security.cert.CertificateException("client certificate not accepted");}
            @Override public void checkServerTrusted(X509Certificate[] chain,String authType)throws java.security.cert.CertificateException{
                try{if(chain==null||chain.length==0||!MessageDigest.isEqual(MessageDigest.getInstance("SHA-256").digest(chain[0].getEncoded()),hexBytes(fingerprint)))throw new java.security.cert.CertificateException("LAN node identity mismatch");}
                catch(NoSuchAlgorithmException ex){throw new java.security.cert.CertificateException(ex);}
            }
            @Override public X509Certificate[] getAcceptedIssuers(){return new X509Certificate[0];}
        }},new SecureRandom());
        Exception last=null;
		long deadline=android.os.SystemClock.elapsedRealtime()+8000;
        for(int i=0;i<addresses.length();i++){
			if(android.os.SystemClock.elapsedRealtime()>=deadline)break;
            String address=addresses.optString(i);
			URI uri=new URI(address);String host=uri.getHost();
			if(host==null||!host.matches("[0-9.]+"))continue;
			InetAddress ip=InetAddress.getByName(host);
			if(!"https".equals(uri.getScheme())||!(ip instanceof Inet4Address)||!ip.isSiteLocalAddress()||uri.getPort()<1||uri.getPort()>65535||!uri.getPath().isEmpty())continue;
            HttpsURLConnection connection=null;
            try{
                connection=(HttpsURLConnection)new URL(address+path).openConnection();connection.setSSLSocketFactory(tls.getSocketFactory());connection.setHostnameVerifier((verifiedHost,session)->true);
				connection.setInstanceFollowRedirects(false);connection.setConnectTimeout(1500);connection.setReadTimeout(path.startsWith("/cloud/diagnostic-payload")?90000:3000);connection.setRequestMethod(method);
                connection.setRequestProperty("Authorization","Bearer "+grant);
                if(data!=null&&data.length>0){connection.setDoOutput(true);connection.setFixedLengthStreamingMode(data.length);try(OutputStream out=connection.getOutputStream()){out.write(data);}}
                int status=connection.getResponseCode();long payloadStart=android.os.SystemClock.elapsedRealtime();byte[] body=new byte[0];
                InputStream input=status>=400?connection.getErrorStream():connection.getInputStream();
                if(input!=null)try(InputStream in=input;ByteArrayOutputStream out=new ByteArrayOutputStream()){byte[] buffer=new byte[8192];int n;while((n=in.read(buffer))!=-1){out.write(buffer,0,n);if(out.size()>16<<20)throw new IOException("LAN response too large");}body=out.toByteArray();}
                return new CloudStorageService.PeerResult(status,body,Math.max(1,android.os.SystemClock.elapsedRealtime()-payloadStart));
            }catch(Exception ex){last=ex;}finally{if(connection!=null)connection.disconnect();}
        }
        throw new IOException("LAN node unreachable",last);
    }

    private static byte[] hexBytes(String text){byte[] bytes=new byte[text.length()/2];for(int i=0;i<bytes.length;i++)bytes[i]=(byte)Integer.parseInt(text.substring(i*2,i*2+2),16);return bytes;}
}
