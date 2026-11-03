package com.myliba.android;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;

/** Regenerates public test vectors using the production Android encryption code. */
public final class LocalIndexInterop {
    public static void main(String[] args)throws Exception{
        byte[] key=LocalIndexCrypto.key("11".repeat(32));
        String node="phone-interop",id=LocalIndexCrypto.hash("phone-file".getBytes(StandardCharsets.UTF_8));
        ArrayList<String> records=new ArrayList<>();
        for(int phase=0;phase<4;phase++){
            String tags=phase==0?"[\"旅行\"]":phase==1?"[\"家庭\",\"收藏\"]":"[]";
            String folder=phase==0?"旅行照片":phase==1?"家庭照片":"";
            String locations=phase==3?"[]":"[{\"nodeId\":\"phone-interop\",\"storageId\":\"saf-root\",\"resourceRef\":\"Y29udGVudDovL3Rlc3QvcGhvdG8uanBn\"}]";
            String index="{\"fileId\":\""+id+"\",\"revisionId\":\"v1\",\"name\":\"照片.jpg\",\"kind\":\"image\",\"modified\":\"2026-11-03T00:00:00Z\",\"tags\":"+tags+",\"folderId\":\""+(phase<2?"folder":"")+"\",\"folderName\":\""+folder+"\",\"organizationKnown\":true,\"deleted\":"+(phase==3)+",\"original\":{\"size\":6,\"local\":"+locations+"},\"assets\":[],\"contentSize\":6}";
            String manifest="{\"indexOnly\":true,\"localIndex\":"+index+"}";
            String stamp="2026-11-03T00:00:0"+phase+"Z";
            String record=LocalIndexCrypto.seal(key,id,"v1",node,stamp,manifest);
            String nonce=record.split("\"nonce\":\"")[1].split("\"")[0],sealed=record.split("\"sealed\":\"")[1].split("\"")[0];
            if(!manifest.equals(LocalIndexCrypto.open(key,id,"v1",node,stamp,nonce,sealed)))throw new AssertionError("publisher cannot reopen its persisted record");
            records.add(record);
        }
        Files.write(Path.of(args[0]),records,StandardCharsets.UTF_8);
    }
}
