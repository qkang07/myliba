package com.myliba.android;

import android.content.Context;
import org.webrtc.PeerConnectionFactory;

/** WebRTC global initialization must finish before any factory exists. */
final class WebRtcRuntime {
    private static boolean initialized;

    private WebRtcRuntime(){}

    static synchronized void ensureInitialized(Context context){
        if(initialized)return;
        AppLog.info("WebRTC global initialization starting");
        PeerConnectionFactory.initialize(PeerConnectionFactory.InitializationOptions.builder(context.getApplicationContext()).createInitializationOptions());
        initialized=true;
        AppLog.info("WebRTC global initialization complete");
    }
}
