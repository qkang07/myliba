package com.myliba.android;

import android.content.Context;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import java.io.*;
import java.nio.*;
import java.nio.channels.FileChannel;
import java.util.*;

/** Extracts JPEG previews referenced from the TIFF directories of a Sony ARW. */
final class ArwPreview {
    private static byte[] read(FileChannel channel,long offset,int length,long size)throws IOException{
        if(offset<0||length<0||offset>size||length>size-offset)throw new EOFException();
        ByteBuffer buffer=ByteBuffer.allocate(length);channel.position(offset);
        while(buffer.hasRemaining())if(channel.read(buffer)<0)throw new EOFException();
        return buffer.array();
    }
    private static long uint(ByteBuffer buffer){return Integer.toUnsignedLong(buffer.getInt());}
    static Bitmap decode(Context context,Uri uri)throws IOException{
        try(ParcelFileDescriptor fd=context.getContentResolver().openFileDescriptor(uri,"r")){
            if(fd==null)throw new IOException("无法读取 ARW 文件");
            try(FileInputStream stream=new FileInputStream(fd.getFileDescriptor())){
                FileChannel channel=stream.getChannel();long size=channel.size();byte[] header=read(channel,0,8,size);
                ByteOrder order;if(header[0]=='I'&&header[1]=='I')order=ByteOrder.LITTLE_ENDIAN;else if(header[0]=='M'&&header[1]=='M')order=ByteOrder.BIG_ENDIAN;else throw new IOException("ARW 格式无效");
                ByteBuffer h=ByteBuffer.wrap(header).order(order);if(Short.toUnsignedInt(h.getShort(2))!=42)throw new IOException("ARW 格式无效");
                ArrayDeque<Long> queue=new ArrayDeque<>();queue.add(Integer.toUnsignedLong(h.getInt(4)));Set<Long> seen=new HashSet<>();byte[] best=null;long bestPixels=0;
                while(!queue.isEmpty()&&seen.size()<128){long offset=queue.remove();if(offset==0||!seen.add(offset))continue;
                    try{
                        int count=Short.toUnsignedInt(ByteBuffer.wrap(read(channel,offset,2,size)).order(order).getShort());if(count>4096)continue;
                        ByteBuffer entries=ByteBuffer.wrap(read(channel,offset+2,count*12+4,size)).order(order);long jpegOffset=0,jpegLength=0;
                        for(int i=0;i<count;i++){int pos=i*12;int tag=Short.toUnsignedInt(entries.getShort(pos));int type=Short.toUnsignedInt(entries.getShort(pos+2));long n=Integer.toUnsignedLong(entries.getInt(pos+4));long value=Integer.toUnsignedLong(entries.getInt(pos+8));if(type!=4||n==0)continue;
                            if(tag==0x201&&n==1)jpegOffset=value;else if(tag==0x202&&n==1)jpegLength=value;else if(tag==0x14a||tag==0x8769||tag==0x8825){if(n==1)queue.add(value);else if(n<=128){ByteBuffer sub=ByteBuffer.wrap(read(channel,value,(int)n*4,size)).order(order);while(sub.hasRemaining())queue.add(uint(sub));}}
                        }
                        queue.add(Integer.toUnsignedLong(entries.getInt(count*12)));if(jpegLength<4||jpegLength>32L*1024*1024)continue;
                        byte[] data=read(channel,jpegOffset,(int)jpegLength,size);if((data[0]&255)!=255||(data[1]&255)!=216)continue;
                        BitmapFactory.Options bounds=new BitmapFactory.Options();bounds.inJustDecodeBounds=true;BitmapFactory.decodeByteArray(data,0,data.length,bounds);long pixels=(long)bounds.outWidth*bounds.outHeight;
                        if(bounds.outWidth>0&&bounds.outHeight>0&&pixels<=80000000L&&pixels>bestPixels){best=data;bestPixels=pixels;}
                    }catch(EOFException ignored){}
                }
                if(best==null)throw new IOException("ARW 文件不含可用的 JPEG 预览");
                BitmapFactory.Options bounds=new BitmapFactory.Options();bounds.inJustDecodeBounds=true;BitmapFactory.decodeByteArray(best,0,best.length,bounds);
                BitmapFactory.Options options=new BitmapFactory.Options();options.inSampleSize=1;while(Math.max(bounds.outWidth,bounds.outHeight)/options.inSampleSize>1280)options.inSampleSize*=2;
                Bitmap result=BitmapFactory.decodeByteArray(best,0,best.length,options);if(result==null)throw new IOException("ARW JPEG 预览无法解码");return result;
            }
        }
    }
}
