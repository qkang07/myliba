package com.myliba.android;

import android.app.job.JobInfo;
import android.app.job.JobParameters;
import android.app.job.JobScheduler;
import android.app.job.JobService;
import android.content.ComponentName;
import android.content.Context;
import android.content.SharedPreferences;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;

/** Persistent, network-aware fallback for index and operation sync. */
public final class LocalIndexJobService extends JobService {
    private static final int PERIODIC_JOB=4318, STARTUP_JOB=4319;
    private final ExecutorService worker=Executors.newSingleThreadExecutor();
    private Future<?> active;

    static void schedule(Context context,boolean immediate){
        JobScheduler jobs=context.getSystemService(JobScheduler.class);
        if(jobs==null)return;
        SharedPreferences settings=context.getSharedPreferences(ResourceNodeService.PREFS,Context.MODE_PRIVATE);
        if(!settings.getBoolean("paired",false)||!settings.getBoolean("index_enabled",false)){
            jobs.cancel(PERIODIC_JOB);jobs.cancel(STARTUP_JOB);return;
        }
        ComponentName service=new ComponentName(context,LocalIndexJobService.class);
        jobs.schedule(new JobInfo.Builder(PERIODIC_JOB,service)
            .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
            .setPersisted(true)
            .setPeriodic(15*60*1000L).build());
        if(immediate)jobs.schedule(new JobInfo.Builder(STARTUP_JOB,service)
            .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
            .setMinimumLatency(1000L).build());
    }

    @Override public boolean onStartJob(JobParameters params){
        active=worker.submit(()->{
            boolean retry=false;
            try{
                SharedPreferences settings=getSharedPreferences(ResourceNodeService.PREFS,MODE_PRIVATE);
                if(settings.getBoolean("paired",false)&&settings.getBoolean("index_enabled",false)){
                    String hub=settings.getString("hub","");
                    String token=CloudSecretStore.read(settings,"token");
                    String node=ResourceNodeService.ensureID(this);
                    if(!hub.startsWith("https://")||token.length()<32)throw new IllegalStateException("索引同步配置无效");
                    new LocalIndexPublisher(this).sync(hub,token,node);
                }
            }catch(InterruptedException stopped){Thread.currentThread().interrupt();retry=true;}
            catch(Exception failure){LocalIndexPublisher.status="后台索引同步失败，将重试："+failure.getMessage();AppLog.error("Background index sync failed",failure);retry=true;}
            if(!Thread.currentThread().isInterrupted())jobFinished(params,retry);
        });
        return true;
    }

    @Override public boolean onStopJob(JobParameters params){
        if(active!=null)active.cancel(true);
        return true;
    }

    @Override public void onDestroy(){worker.shutdownNow();super.onDestroy();}
}
