package com.myliba.android;

import android.content.Context;
import android.graphics.Matrix;
import android.view.GestureDetector;
import android.view.MotionEvent;
import android.view.ScaleGestureDetector;
import android.widget.ImageView;

/** Pinch and double-tap zoom with bounded panning, independent of source location. */
final class ZoomImageView extends ImageView {
    private final Matrix transform = new Matrix();
    private final float[] values = new float[9];
    private final ScaleGestureDetector scaleGesture;
    private final GestureDetector gesture;
    private float fitScale = 1f;
    private final java.util.function.IntConsumer onSwipe;

    ZoomImageView(Context context, java.util.function.IntConsumer onSwipe) {
        super(context);
        this.onSwipe = onSwipe;
        setScaleType(ScaleType.MATRIX);
        setContentDescription("图片预览，双指缩放，双击放大或还原");
        scaleGesture = new ScaleGestureDetector(context, new ScaleGestureDetector.SimpleOnScaleGestureListener() {
            @Override public boolean onScale(ScaleGestureDetector detector) {
                scale(detector.getScaleFactor(), detector.getFocusX(), detector.getFocusY());
                return true;
            }
        });
        gesture = new GestureDetector(context, new GestureDetector.SimpleOnGestureListener() {
            @Override public boolean onDown(MotionEvent e) { return true; }
            @Override public boolean onScroll(MotionEvent first, MotionEvent last, float dx, float dy) {
                if (!scaleGesture.isInProgress()) { transform.postTranslate(-dx, -dy); constrain(); }
                return true;
            }
            @Override public boolean onDoubleTap(MotionEvent e) {
                transform.getValues(values);
                if (values[Matrix.MSCALE_X] > fitScale * 1.1f) fit();
                else scale(2.5f, e.getX(), e.getY());
                return true;
            }
            @Override public boolean onSingleTapConfirmed(MotionEvent e) { performClick(); return true; }
            @Override public boolean onFling(MotionEvent first, MotionEvent last, float velocityX, float velocityY) {
                if(first==null||last==null||onSwipe==null)return false;
                transform.getValues(values);
                float density=getResources().getDisplayMetrics().density;
                float dx=last.getX()-first.getX(),dy=last.getY()-first.getY();
                if(values[Matrix.MSCALE_X]>fitScale*1.05f||Math.abs(dx)<70*density||Math.abs(dx)<Math.abs(dy)*1.5f||Math.abs(velocityX)<250*density)return false;
                onSwipe.accept(dx<0?1:-1);
                return true;
            }
        });
    }

    @Override protected void onSizeChanged(int w, int h, int oldW, int oldH) { super.onSizeChanged(w,h,oldW,oldH); fit(); }
    private void fit() {
        if (getDrawable() == null || getWidth() == 0 || getHeight() == 0) return;
        fitScale = Math.min((float)getWidth()/getDrawable().getIntrinsicWidth(), (float)getHeight()/getDrawable().getIntrinsicHeight());
        transform.setScale(fitScale, fitScale);
        constrain();
    }
    private void scale(float factor, float x, float y) {
        transform.getValues(values);
        float current = values[Matrix.MSCALE_X];
        float target = Math.max(fitScale, Math.min(fitScale * 6, current * factor));
        transform.postScale(target/current, target/current, x, y);
        constrain();
    }
    private void constrain() {
        if (getDrawable() == null) return;
        transform.getValues(values);
        float width = getDrawable().getIntrinsicWidth()*values[Matrix.MSCALE_X];
        float height = getDrawable().getIntrinsicHeight()*values[Matrix.MSCALE_Y];
        float x = values[Matrix.MTRANS_X], y = values[Matrix.MTRANS_Y];
        float targetX = width < getWidth() ? (getWidth()-width)/2 : Math.min(0,Math.max(getWidth()-width,x));
        float targetY = height < getHeight() ? (getHeight()-height)/2 : Math.min(0,Math.max(getHeight()-height,y));
        transform.postTranslate(targetX-x,targetY-y);
        setImageMatrix(transform);
    }
    @Override public boolean onTouchEvent(MotionEvent event) {
        scaleGesture.onTouchEvent(event);
        gesture.onTouchEvent(event);
        return true;
    }
    @Override public boolean performClick() { super.performClick(); return true; }
}
