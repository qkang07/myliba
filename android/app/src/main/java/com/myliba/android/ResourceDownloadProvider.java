package com.myliba.android;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.provider.OpenableColumns;
import android.webkit.MimeTypeMap;
import java.io.File;
import java.io.FileNotFoundException;
import java.util.List;
import java.util.Locale;
import java.util.UUID;

/** Grants read-only access to completed RTC downloads on Android 8 and 9. */
public final class ResourceDownloadProvider extends ContentProvider {
    static final String AUTHORITY="com.myliba.android.downloads";
    static Uri uri(String id,String name){return new Uri.Builder().scheme("content").authority(AUTHORITY).appendPath(id).appendPath(name).build();}
    @Override public boolean onCreate(){return true;}
    private File file(Uri uri)throws FileNotFoundException{
        List<String> parts=uri.getPathSegments();if(parts.size()!=2)throw new FileNotFoundException();
        try{UUID.fromString(parts.get(0));}catch(IllegalArgumentException ex){throw new FileNotFoundException();}
        return new File(new File(getContext().getFilesDir(),"rtc-downloads"),parts.get(0));
    }
    @Override public ParcelFileDescriptor openFile(Uri uri,String mode)throws FileNotFoundException{
        if(!"r".equals(mode))throw new FileNotFoundException();File source=file(uri);if(!source.isFile())throw new FileNotFoundException();return ParcelFileDescriptor.open(source,ParcelFileDescriptor.MODE_READ_ONLY);
    }
    @Override public Cursor query(Uri uri,String[] projection,String selection,String[] selectionArgs,String sortOrder){
        File source;try{source=file(uri);}catch(FileNotFoundException ex){return null;}
        if(!source.isFile())return null;
        String[] columns=projection==null?new String[]{OpenableColumns.DISPLAY_NAME,OpenableColumns.SIZE}:projection;
        MatrixCursor cursor=new MatrixCursor(columns);Object[] row=new Object[columns.length];
        for(int i=0;i<columns.length;i++)row[i]=OpenableColumns.DISPLAY_NAME.equals(columns[i])?uri.getLastPathSegment():OpenableColumns.SIZE.equals(columns[i])?source.length():null;
        cursor.addRow(row);return cursor;
    }
    @Override public String getType(Uri uri){String name=uri.getLastPathSegment();if(name==null)return "application/octet-stream";int dot=name.lastIndexOf('.');String extension=dot<0?"":name.substring(dot+1).toLowerCase(Locale.ROOT);String type=MimeTypeMap.getSingleton().getMimeTypeFromExtension(extension);return type==null?"application/octet-stream":type;}
    @Override public Uri insert(Uri uri,ContentValues values){throw new UnsupportedOperationException();}
    @Override public int delete(Uri uri,String selection,String[] args){throw new UnsupportedOperationException();}
    @Override public int update(Uri uri,ContentValues values,String selection,String[] args){throw new UnsupportedOperationException();}
}
