package com.myliba.android;

import android.app.Application;

public final class MylibaApplication extends Application {
    @Override public void onCreate(){super.onCreate();AppLog.init(this);}
}
