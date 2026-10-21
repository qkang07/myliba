package com.myliba.android;

import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import java.nio.charset.StandardCharsets;
import java.security.KeyStore;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/** App-private credentials protected by a non-exportable Android Keystore key. */
final class CloudSecretStore {
    private static final String ALIAS="myliba-cloud-secrets-v1",PREFIX="v1:";
    private CloudSecretStore(){}
    private static synchronized SecretKey key()throws Exception{
        KeyStore store=KeyStore.getInstance("AndroidKeyStore");store.load(null);
        if(store.containsAlias(ALIAS))return (SecretKey)store.getKey(ALIAS,null);
        KeyGenerator generator=KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES,"AndroidKeyStore");
        generator.init(new KeyGenParameterSpec.Builder(ALIAS,KeyProperties.PURPOSE_ENCRYPT|KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());
        return generator.generateKey();
    }
    static String protect(String value)throws Exception{
        Cipher cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.ENCRYPT_MODE,key());
        byte[] iv=cipher.getIV(),encrypted=cipher.doFinal(value.getBytes(StandardCharsets.UTF_8));
        byte[] payload=new byte[iv.length+encrypted.length];System.arraycopy(iv,0,payload,0,iv.length);System.arraycopy(encrypted,0,payload,iv.length,encrypted.length);
        return PREFIX+Base64.encodeToString(payload,Base64.NO_WRAP);
    }
    static String read(SharedPreferences settings,String name)throws Exception{
        String saved=settings.getString(name,"");if(saved.isEmpty())return "";
        if(!saved.startsWith(PREFIX)){settings.edit().putString(name,protect(saved)).apply();return saved;}
        byte[] payload=Base64.decode(saved.substring(PREFIX.length()),Base64.DEFAULT);
        if(payload.length<29)throw new IllegalStateException("云层凭据已损坏");
        byte[] iv=new byte[12];System.arraycopy(payload,0,iv,0,12);
        Cipher cipher=Cipher.getInstance("AES/GCM/NoPadding");cipher.init(Cipher.DECRYPT_MODE,key(),new GCMParameterSpec(128,iv));
        return new String(cipher.doFinal(payload,12,payload.length-12),StandardCharsets.UTF_8);
    }
}
