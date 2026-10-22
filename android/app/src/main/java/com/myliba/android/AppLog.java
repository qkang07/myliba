package com.myliba.android;

import android.app.ActivityManager;
import android.app.ApplicationExitInfo;
import android.content.Context;
import android.content.SharedPreferences;
import android.os.Build;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.List;
import java.util.Locale;
import java.util.zip.ZipEntry;
import java.util.zip.ZipOutputStream;

/** Small, private, rotating diagnostic log. Never write credentials, SDP, URLs or file paths here. */
final class AppLog {
    private static final long MAX_BYTES=512L*1024;
    private static final int MAX_NATIVE_TRACE=16*1024*1024;
    private static Context context;

    private AppLog(){}

    static synchronized void init(Context app){
        if(context!=null)return;
        context=app.getApplicationContext();
        Thread.UncaughtExceptionHandler previous=Thread.getDefaultUncaughtExceptionHandler();
        Thread.setDefaultUncaughtExceptionHandler((thread,error)->{
            error("Uncaught exception on "+thread.getName(),error);
            if(previous!=null)previous.uncaughtException(thread,error);
        });
        String version="unknown";
        try{version=app.getPackageManager().getPackageInfo(app.getPackageName(),0).versionName;}catch(Exception ignored){}
        info("App started; Android "+Build.VERSION.SDK_INT+"; version "+version);
        new Thread(AppLog::importLastExit,"diagnostics-exit-import").start();
    }

    static synchronized void info(String event){write("INFO",event);}

    static synchronized void error(String event,Throwable failure){
        write("ERROR",event);
        for(Throwable cause=failure;cause!=null;cause=cause.getCause()){
            write("ERROR","  "+cause.getClass().getName());
            StackTraceElement[] stack=cause.getStackTrace();
            for(int i=0;i<Math.min(stack.length,24);i++)write("ERROR","    at "+stack[i]);
        }
    }

    private static void write(String level,String event){
        if(context==null)return;
        try{
            File dir=new File(context.getFilesDir(),"diagnostics");
            if(!dir.exists()&&!dir.mkdirs())return;
            File active=new File(dir,"app.log"),old=new File(dir,"app.old.log");
            if(active.length()>MAX_BYTES){if(old.exists())old.delete();active.renameTo(old);}
            String when=new SimpleDateFormat("yyyy-MM-dd HH:mm:ss.SSS Z",Locale.ROOT).format(new Date());
            String line=when+" ["+Thread.currentThread().getName()+"] "+level+" "+event.replace('\n',' ').replace('\r',' ')+"\n";
            try(FileOutputStream out=new FileOutputStream(active,true)){out.write(line.getBytes(StandardCharsets.UTF_8));}
        }catch(Exception ignored){}
    }

    private static synchronized void importLastExit(){
        if(Build.VERSION.SDK_INT<30)return;
        try{
            SharedPreferences prefs=context.getSharedPreferences("diagnostics",Context.MODE_PRIVATE);
            long last=prefs.getLong("last_exit",0),newest=last;
            ActivityManager manager=context.getSystemService(ActivityManager.class);
            List<ApplicationExitInfo> exits=manager.getHistoricalProcessExitReasons(context.getPackageName(),0,10);
            for(int i=exits.size()-1;i>=0;i--){
                ApplicationExitInfo exit=exits.get(i);
                if(exit.getTimestamp()<=last)continue;
                newest=Math.max(newest,exit.getTimestamp());
                int reason=exit.getReason();
                if(reason!=ApplicationExitInfo.REASON_CRASH_NATIVE&&reason!=ApplicationExitInfo.REASON_CRASH&&reason!=ApplicationExitInfo.REASON_ANR)continue;
                write("ERROR","Previous process exit: reason="+reason+" status="+exit.getStatus()+" time="+exit.getTimestamp());
                if(reason==ApplicationExitInfo.REASON_CRASH_NATIVE&&Build.VERSION.SDK_INT>=31)saveNativeTrace(exit);
            }
            if(newest>last)prefs.edit().putLong("last_exit",newest).apply();
        }catch(Exception failure){error("Could not read previous process exit",failure);}
    }

    private static void saveNativeTrace(ApplicationExitInfo exit){
        File target=new File(new File(context.getFilesDir(),"diagnostics"),"native-crash.pb");
        if(target.exists())target.delete();
        try(InputStream trace=exit.getTraceInputStream()){
            if(trace==null){write("INFO","Native tombstone unavailable from Android");return;}
            boolean truncated=false;
            try(OutputStream out=new FileOutputStream(target)){
                byte[] buffer=new byte[8192];int read,total=0;
                while((read=trace.read(buffer))!=-1){if(total+read>MAX_NATIVE_TRACE){truncated=true;break;}out.write(buffer,0,read);total+=read;}
            }
            if(truncated){target.delete();write("INFO","Native tombstone exceeds 16 MiB export limit");}
            else write("INFO","Native tombstone saved for diagnostic export");
        }catch(Exception failure){error("Could not save native tombstone",failure);}
    }

    static synchronized String recent(){
        if(context==null)return "日志尚未初始化";
        File dir=new File(context.getFilesDir(),"diagnostics");
        StringBuilder result=new StringBuilder();
        appendText(result,new File(dir,"app.old.log"));
        appendText(result,new File(dir,"app.log"));
        if(result.length()>40000)return "（只显示最近的日志；导出可获取完整文件）\n"+result.substring(result.length()-40000);
        return result.length()==0?"暂无日志":result.toString();
    }

    private static void appendText(StringBuilder result,File file){
        if(!file.isFile())return;
        try(FileInputStream input=new FileInputStream(file)){
            ByteArrayOutputStream output=new ByteArrayOutputStream();byte[] buffer=new byte[8192];int count;
            while((count=input.read(buffer))!=-1)output.write(buffer,0,count);
            result.append(output.toString("UTF-8"));
        }catch(IOException ignored){}
    }

    static synchronized void export(OutputStream destination)throws IOException{
        File dir=new File(context.getFilesDir(),"diagnostics");
        try(ZipOutputStream zip=new ZipOutputStream(destination)){
            add(zip,new File(dir,"app.old.log"));
            add(zip,new File(dir,"app.log"));
            add(zip,new File(dir,"native-crash.pb"));
        }
    }

    private static void add(ZipOutputStream zip,File file)throws IOException{
        if(!file.isFile())return;
        zip.putNextEntry(new ZipEntry(file.getName()));
        try(InputStream input=new FileInputStream(file)){
            byte[] buffer=new byte[8192];int count;
            while((count=input.read(buffer))!=-1)zip.write(buffer,0,count);
        }
        zip.closeEntry();
    }
}
