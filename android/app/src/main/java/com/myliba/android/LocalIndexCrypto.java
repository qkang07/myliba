package com.myliba.android;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.SecureRandom;
import javax.crypto.Cipher;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.SecretKeySpec;

/** Format-3 catalog encoding shared with Go. No Android dependencies. */
final class LocalIndexCrypto {
    static byte[] key(String value) {
        if(!value.matches("[0-9a-fA-F]{64}"))throw new IllegalArgumentException("恢复密钥需为 64 位十六进制字符");
        byte[] result=new byte[32];for(int i=0;i<32;i++)result[i]=(byte)Integer.parseInt(value.substring(i*2,i*2+2),16);return result;
    }
    static String hex(byte[] data){StringBuilder out=new StringBuilder();for(byte b:data)out.append(String.format(java.util.Locale.ROOT,"%02x",b&255));return out.toString();}
    static String hash(byte[] data)throws Exception{return hex(MessageDigest.getInstance("SHA-256").digest(data));}
    static String seal(byte[] key,String object,String revision,String author,String stamp,String manifest)throws Exception{
        for(String value:new String[]{object,revision,author,stamp})if(!value.matches("[a-zA-Z0-9:._-]{1,128}"))throw new IllegalArgumentException("Invalid catalog identity");
        String aad="{\"format\":3,\"keepHistory\":false,\"libraryId\":\""+hash(key)+"\",\"objectId\":\""+object+"\",\"revisionId\":\""+revision+"\",\"authorId\":\""+author+"\",\"createdAt\":\""+stamp+"\",\"blockIds\":[]}";
        byte[] nonce=new byte[12];new SecureRandom().nextBytes(nonce);
        Cipher cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.ENCRYPT_MODE,new SecretKeySpec(key,"AES"),new GCMParameterSpec(128,nonce));cipher.updateAAD(aad.getBytes(StandardCharsets.UTF_8));
        byte[] sealed=cipher.doFinal(manifest.getBytes(StandardCharsets.UTF_8));
        return aad.substring(0,aad.length()-1)+",\"nonce\":\""+hex(nonce)+"\",\"sealed\":\""+hex(sealed)+"\"}";
    }
    static String open(byte[] key,String object,String revision,String author,String stamp,String nonceHex,String sealedHex)throws Exception{
        String aad="{\"format\":3,\"keepHistory\":false,\"libraryId\":\""+hash(key)+"\",\"objectId\":\""+object+"\",\"revisionId\":\""+revision+"\",\"authorId\":\""+author+"\",\"createdAt\":\""+stamp+"\",\"blockIds\":[]}";
        byte[] nonce=decode(nonceHex),sealed=decode(sealedHex);
        Cipher cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.DECRYPT_MODE,new SecretKeySpec(key,"AES"),new GCMParameterSpec(128,nonce));cipher.updateAAD(aad.getBytes(StandardCharsets.UTF_8));
        return new String(cipher.doFinal(sealed),StandardCharsets.UTF_8);
    }
    private static byte[] decode(String hex){if(hex.length()%2!=0||!hex.matches("[0-9a-f]+"))throw new IllegalArgumentException("Invalid hex");byte[] bytes=new byte[hex.length()/2];for(int i=0;i<bytes.length;i++)bytes[i]=(byte)Integer.parseInt(hex.substring(2*i,2*i+2),16);return bytes;}
}
