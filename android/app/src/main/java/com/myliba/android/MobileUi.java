package com.myliba.android;

import android.app.Activity;
import android.app.Dialog;
import android.content.res.ColorStateList;
import android.graphics.*;
import android.graphics.drawable.*;
import android.view.*;
import android.widget.*;
import java.util.*;

/** Shared native visual language for every library and every screen. */
final class MobileUi {
    static final int BG = 0xfff5f7f3, PAPER = Color.WHITE, INK = 0xff20372d,
            GREEN = 0xff28634c, SOFT = 0xffe8f0e8, MUTED = 0xff758179,
            LINE = 0xffe5eae3, AMBER = 0xff9c743f, RED = 0xffa75348;
    final Activity a;
    MobileUi(Activity a) { this.a = a; }
    int d(float v) { return Math.round(v * a.getResources().getDisplayMetrics().density); }
    LinearLayout col() { LinearLayout v = new LinearLayout(a); v.setOrientation(LinearLayout.VERTICAL); return v; }
    LinearLayout row() { LinearLayout v = new LinearLayout(a); v.setGravity(Gravity.CENTER_VERTICAL); return v; }
    LinearLayout.LayoutParams lp(int w, int h) { return new LinearLayout.LayoutParams(w < 0 ? w : d(w), h < 0 ? h : d(h)); }
    LinearLayout.LayoutParams top(int margin) { LinearLayout.LayoutParams p = lp(-1,-2); p.topMargin=d(margin); return p; }
    LinearLayout.LayoutParams weight() { return new LinearLayout.LayoutParams(0,-2,1); }
    void pad(View v, int x, int y) { v.setPadding(d(x),d(y),d(x),d(y)); }
    TextView text(String s, int size, int color, boolean bold) {
        TextView v=new TextView(a); v.setText(s); v.setTextSize(size); v.setTextColor(color);
        v.setTypeface(Typeface.create(bold ? "sans-serif-medium" : "sans-serif", Typeface.NORMAL));
        v.setIncludeFontPadding(false); v.setLineSpacing(d(3),1); return v;
    }
    GradientDrawable shape(int color, int radius, int border) {
        GradientDrawable g=new GradientDrawable(); g.setColor(color); g.setCornerRadius(d(radius));
        if(border!=0)g.setStroke(d(1),border); return g;
    }
    void touch(View v, int color, int radius, Runnable action) {
        v.setBackground(new RippleDrawable(ColorStateList.valueOf(0x1828634c),shape(color,radius,0),shape(Color.WHITE,radius,0)));
        v.setOnClickListener(w->action.run()); v.setFocusable(true);
    }
    TextView button(String label, boolean primary, Runnable action) {
        TextView v=text(label,14,primary?PAPER:GREEN,true); v.setGravity(Gravity.CENTER); v.setMinHeight(d(48));
        pad(v,16,12); touch(v,primary?GREEN:SOFT,14,action); return v;
    }
    ImageView icon(String name,int size,int color) {
        ImageView v=new ImageView(a); v.setImageDrawable(new Icon(name,color)); v.setLayoutParams(lp(size,size)); return v;
    }
    ImageView iconButton(String name,String description,Runnable action) {
        ImageView v=icon(name,44,INK); pad(v,11,11); v.setContentDescription(description);
        touch(v,Color.TRANSPARENT,14,action); return v;
    }
    TextView chip(String name,boolean active,Runnable action) {
        TextView v=text(name,12,active?GREEN:MUTED,active); v.setGravity(Gravity.CENTER); pad(v,14,10);
        v.setMinHeight(d(40)); touch(v,active?SOFT:PAPER,20,action); v.setSelected(active); return v;
    }
    LinearLayout card() { LinearLayout v=col(); v.setBackground(shape(PAPER,20,LINE)); pad(v,18,18); return v; }
    View line() { View v=new View(a); v.setBackgroundColor(LINE); v.setLayoutParams(lp(-1,1)); return v; }
    View spacer(int h) { View v=new View(a); v.setLayoutParams(lp(1,h)); return v; }
    View rail(List<View> children) {
        HorizontalScrollView s=new HorizontalScrollView(a); s.setHorizontalScrollBarEnabled(false); s.setClipToPadding(false);
        LinearLayout r=row();
        for(View child:children) { LinearLayout.LayoutParams p=lp(-2,-2); p.rightMargin=d(8); r.addView(child,p); }
        s.addView(r); return s;
    }
    EditText input(String hint,String initial) {
        EditText e=new EditText(a); e.setTextSize(15); e.setTextColor(INK); e.setHintTextColor(MUTED);
        e.setSingleLine(); e.setHint(hint); e.setText(initial); pad(e,14,12); e.setMinHeight(d(50));
        e.setBackground(shape(BG,14,LINE)); return e;
    }
    LinearLayout item(String icon,String title,String subtitle,Runnable action) {
        LinearLayout r=row(); pad(r,12,12); touch(r,PAPER,14,action);
        ImageView mark=icon(icon,40,GREEN); pad(mark,9,9); mark.setBackground(shape(SOFT,12,0)); r.addView(mark);
        LinearLayout copy=col(); copy.setPadding(d(12),0,d(8),0);
        TextView t=text(title,14,INK,true); t.setMaxLines(2); t.setEllipsize(android.text.TextUtils.TruncateAt.END); copy.addView(t);
        if(subtitle!=null&&!subtitle.isEmpty()) { TextView sub=text(subtitle,11,MUTED,false); sub.setMaxLines(2); sub.setEllipsize(android.text.TextUtils.TruncateAt.END); copy.addView(sub,top(5)); }
        r.addView(copy,weight()); r.addView(icon("chevron",18,MUTED)); return r;
    }
    Sheet sheet(String title,String subtitle) { return new Sheet(title,subtitle); }
    final class Sheet {
        final Dialog dialog; final LinearLayout body,footer,frame;
        Sheet(String title,String subtitle) {
            dialog=new Dialog(a); frame=new LinearLayout(a){
                @Override protected void onMeasure(int widthSpec,int heightSpec){
                    int cap=(int)(a.getResources().getDisplayMetrics().heightPixels*.86f);
                    if(MeasureSpec.getMode(heightSpec)!=MeasureSpec.UNSPECIFIED)cap=Math.min(cap,MeasureSpec.getSize(heightSpec));
                    super.onMeasure(widthSpec,MeasureSpec.makeMeasureSpec(cap,MeasureSpec.AT_MOST));
                }
            }; frame.setOrientation(LinearLayout.VERTICAL); frame.setBackground(shape(PAPER,26,0)); pad(frame,20,10);
            View handle=new View(a); handle.setBackground(shape(0xffd7ded7,3,0)); LinearLayout.LayoutParams hp=lp(36,4); hp.gravity=Gravity.CENTER; frame.addView(handle,hp);
            LinearLayout heading=row(); TextView headingText=text(title,21,INK,true); headingText.setMaxLines(2); headingText.setEllipsize(android.text.TextUtils.TruncateAt.END); heading.addView(headingText,weight()); heading.addView(iconButton("x","关闭",dialog::dismiss)); frame.addView(heading,top(12));
            if(subtitle!=null&&!subtitle.isEmpty()) frame.addView(text(subtitle,12,MUTED,false),top(2));
            ScrollView scroll=new ScrollView(a); scroll.setFillViewport(false); scroll.setVerticalScrollBarEnabled(false);
            body=col(); body.setPadding(0,d(16),0,d(12)); scroll.addView(body);
            frame.addView(scroll,new LinearLayout.LayoutParams(-1,-2,1)); footer=col(); frame.addView(footer);
            dialog.setContentView(frame); Window w=dialog.getWindow(); if(w!=null) { w.setBackgroundDrawableResource(android.R.color.transparent); w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND); w.setDimAmount(.30f); w.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE); }
        }
        void show() { dialog.show(); Window w=dialog.getWindow(); if(w!=null) { w.setGravity(Gravity.BOTTOM); w.setLayout(-1,-2); }
            frame.setOnApplyWindowInsetsListener((view,insets)->{ int b=insets.getSystemWindowInsetBottom(); frame.setPadding(d(20),d(10),d(20),Math.max(d(16),b)); return insets; });
        }
        void close() { dialog.dismiss(); }
    }
    final class Flow extends ViewGroup {
        Flow() { super(a); }
        @Override protected void onMeasure(int ws,int hs) {
            int max=MeasureSpec.getSize(ws), x=0,y=0,row=0;
            for(int i=0;i<getChildCount();i++){ View v=getChildAt(i); measureChild(v,ws,hs); int w=v.getMeasuredWidth(),h=v.getMeasuredHeight(); if(x>0&&x+w>max){x=0;y+=row+d(8);row=0;} x+=w+d(8);row=Math.max(row,h); }
            setMeasuredDimension(max,resolveSize(y+row,hs));
        }
        @Override protected void onLayout(boolean changed,int l,int t,int r,int b){ int x=0,y=0,row=0; for(int i=0;i<getChildCount();i++){ View v=getChildAt(i); int w=v.getMeasuredWidth(),h=v.getMeasuredHeight();if(x>0&&x+w>r-l){x=0;y+=row+d(8);row=0;}v.layout(x,y,x+w,y+h);x+=w+d(8);row=Math.max(row,h); } }
        @Override protected LayoutParams generateDefaultLayoutParams(){return new LayoutParams(-2,-2);}
    }
    /** Resolution-independent line icons, independent of device emoji fonts. */
    static final class Icon extends Drawable {
        final String name; final Paint p=new Paint(Paint.ANTI_ALIAS_FLAG); int color;
        Icon(String name,int color){this.name=name;this.color=color;}
        private void lines(Canvas c,float... v){Path path=new Path();path.moveTo(v[0],v[1]);for(int i=2;i<v.length;i+=2)path.lineTo(v[i],v[i+1]);c.drawPath(path,p);}
        private void rect(Canvas c,float x,float y,float w,float h){c.drawRoundRect(x,y,x+w,y+h,2,2,p);}
        @Override public void draw(Canvas c){c.save();android.graphics.Rect b=getBounds();c.translate(b.left,b.top);c.scale(b.width()/24f,b.height()/24f);p.setColor(color);p.setStyle(Paint.Style.STROKE);p.setStrokeWidth(1.7f);p.setStrokeCap(Paint.Cap.ROUND);p.setStrokeJoin(Paint.Join.ROUND);
            switch(name){
                case "search":c.drawCircle(10.5f,10.5f,6.5f,p);lines(c,15.5f,15.5f,21,21);break;
                case "folder":lines(c,3,7,3,19,21,19,21,7,12,7,10,4,3,4,3,7);break;
                case "library":rect(c,3,3,7,7);rect(c,14,3,7,7);rect(c,3,14,7,7);rect(c,14,14,7,7);break;
                case "grid":rect(c,4,4,6,6);rect(c,14,4,6,6);rect(c,4,14,6,6);rect(c,14,14,6,6);break;
                case "list":lines(c,9,6,21,6);lines(c,9,12,21,12);lines(c,9,18,21,18);c.drawCircle(4,6,.6f,p);c.drawCircle(4,12,.6f,p);c.drawCircle(4,18,.6f,p);break;
                case "phone":rect(c,6,2,12,20);lines(c,10,18,14,18);break;
                case "server":rect(c,3,3,18,7);rect(c,3,14,18,7);c.drawCircle(7,6.5f,.6f,p);c.drawCircle(7,17.5f,.6f,p);lines(c,12,6.5f,17,6.5f);lines(c,12,17.5f,17,17.5f);break;
                case "sources":rect(c,2,3,20,8);rect(c,6,15,12,6);lines(c,12,11,12,15);break;
                case "filter":lines(c,4,6,20,6);lines(c,4,12,20,12);lines(c,4,18,20,18);p.setStyle(Paint.Style.FILL);c.drawCircle(8,6,2.3f,p);c.drawCircle(16,12,2.3f,p);c.drawCircle(10,18,2.3f,p);break;
                case "sort":lines(c,5,4,5,20,2,17);lines(c,5,20,8,17);lines(c,12,6,21,6);lines(c,12,12,18,12);lines(c,12,18,15,18);break;
                case "plus":lines(c,12,5,12,19);lines(c,5,12,19,12);break;
                case "x":lines(c,6,6,18,18);lines(c,18,6,6,18);break;
                case "check":lines(c,5,12,10,17,20,6);break;
                case "chevron":lines(c,9,5,16,12,9,19);break;
                case "down":lines(c,7,10,12,15,17,10);break;
                case "back":lines(c,14,5,7,12,14,19);break;
                case "more":c.drawCircle(5,12,1,p);c.drawCircle(12,12,1,p);c.drawCircle(19,12,1,p);break;
                case "image":rect(c,3,3,18,18);c.drawCircle(8,8,1.5f,p);lines(c,3,17,10,11,15,16,18,13,21,16);break;
                case "video":rect(c,3,3,18,18);lines(c,10,8,16,12,10,16,10,8);break;
                case "audio":lines(c,9,17,9,5,19,3,19,15);lines(c,9,8,19,6);c.drawOval(3,15,9,20,p);c.drawOval(13,13,19,18,p);break;
                case "document":case "file":lines(c,5,3,14,3,19,8,19,21,5,21,5,3);lines(c,14,3,14,8,19,8);lines(c,8,12,16,12);lines(c,8,16,14,16);break;
                case "tag":lines(c,3,3,12,3,22,13,13,22,3,12,3,3);c.drawCircle(8,8,1.4f,p);break;
                case "download":lines(c,12,3,12,15,7,10);lines(c,12,15,17,10);lines(c,4,16,4,21,20,21,20,16);break;
                case "refresh":c.drawArc(4,4,20,20,40,280,false,p);lines(c,20,3,20,9,14,9);break;
                case "spark":lines(c,12,2,15,9,22,12,15,15,12,22,9,15,2,12,9,9,12,2);break;
                case "shield":lines(c,12,2,21,6,20,15,16,20,12,22,8,20,4,15,3,6,12,2);lines(c,8,12,11,15,17,9);break;
                case "link":c.drawArc(3,9,15,21,40,280,false,p);c.drawArc(9,3,21,15,220,280,false,p);lines(c,8,16,16,8);break;
                case "trash":lines(c,3,6,21,6);lines(c,6,6,7,21,17,21,18,6);lines(c,9,6,9,3,15,3,15,6);lines(c,10,10,10,17);lines(c,14,10,14,17);break;
                case "edit":lines(c,4,16,16,4,20,8,8,20,3,21,4,16);lines(c,13,7,17,11);break;
                default:rect(c,4,4,16,16);
            } c.restore(); }
        @Override public void setAlpha(int alpha){p.setAlpha(alpha);}
        @Override public void setColorFilter(ColorFilter f){p.setColorFilter(f);}
        @Override public int getOpacity(){return PixelFormat.TRANSLUCENT;}
    }
    View emptyArt() {
        return new View(a){ final Paint p=new Paint(3);
            @Override protected void onDraw(Canvas c){super.onDraw(c);c.save();c.translate(getWidth()/2f,getHeight()/2f);float s=d(1);c.scale(s,s);
                p.setColor(0xffeaf0e5);c.drawCircle(0,0,73,p);p.setColor(0xffd7e5d4);c.drawCircle(61,-38,8,p);c.drawCircle(-66,31,5,p);
                c.save();c.rotate(-10);p.setColor(0xffd1dfce);c.drawRoundRect(-49,-34,39,41,11,11,p);c.restore();
                c.save();c.rotate(8);p.setColor(Color.WHITE);c.drawRoundRect(-29,-51,32,31,7,7,p);p.setColor(0xffb8cebb);c.drawRoundRect(-17,-35,19,-7,4,4,p);p.setColor(0xffdfe8dc);c.drawRect(-17,2,18,5,p);c.drawRect(-17,11,9,14,p);c.restore();
                p.setColor(GREEN);c.drawRoundRect(-51,-6,52,51,10,10,p);p.setColor(0xff377657);c.drawRoundRect(-51,-12,-5,7,8,8,p);
                p.setColor(0xffb9d6bf);c.drawRoundRect(-14,13,15,17,2,2,p);c.drawRoundRect(-14,24,6,28,2,2,p);c.restore();}
        };
    }
}
