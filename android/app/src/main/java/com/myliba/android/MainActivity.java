package com.myliba.android;

import android.app.*;
import android.content.*;
import android.graphics.Bitmap;
import android.graphics.Color;
import android.media.MediaMetadataRetriever;
import android.media.MediaExtractor;
import android.media.MediaFormat;
import android.media.MediaPlayer;
import android.net.Uri;
import android.os.*;
import android.provider.DocumentsContract;
import android.text.*;
import android.view.*;
import android.view.inputmethod.InputMethodManager;
import android.widget.*;
import java.text.SimpleDateFormat;
import java.util.*;
import java.util.concurrent.*;
import org.json.JSONArray;
import org.json.JSONObject;

import static com.myliba.android.MobileUi.*;

/** A single mobile library experience, backed by either SAF/SQLite or the Hub API. */
public final class MainActivity extends Activity {
    private static final int PICK_TREE=41;
    private final ExecutorService io=Executors.newSingleThreadExecutor();
    private final Handler handler=new Handler(Looper.getMainLooper());
    private final Set<String> selected=new LinkedHashSet<>();
    private final List<LibraryStore.Entry> entries=new ArrayList<>();
    private MobileUi ui; private LocalLibrary db; private LibraryStore localStore,hubStore;
    private ThumbnailLoader thumbs; private SharedPreferences prefs;
    private LocalFilters localFilters=new LocalFilters(),hubFilters=new LocalFilters();
    private LibraryStore.Meta meta=new LibraryStore.Meta();
    private LinearLayout shell,header,content,navigation,batchDock;
    private ListView files;private ResourceAdapter adapter;private FrameLayout resultArea;
    private TextView resultLabel,footerLabel;private EditText search;
    private TextView nodeTitle,nodeStatus,nodeCounts,nodeCapacity,nodeAccess,nodeConfigure,nodeStop;
    private boolean displayedNodeRunning;
    private boolean home,grid,loading,selecting,busy,scanning,destroyed,needsLogin,storageOnly;
    private int tab,generation,total; private String error="",scanMessage="",activeCollection;
    private final Runnable searchTask=()->reload(false,false);
    private final Runnable transferRefresh=new Runnable(){@Override public void run(){if(!destroyed&&tab==3){renderTransfers();handler.postDelayed(this,1000);}}};
    private final Runnable nodeRefresh=new Runnable(){@Override public void run(){if(!destroyed&&storageOnly){if(displayedNodeRunning!=CloudStorageService.running)render();else updateStorageOnlyStatus();handler.postDelayed(this,3000);}}};

    @Override public void onCreate(Bundle saved){
        super.onCreate(saved);ui=new MobileUi(this);db=new LocalLibrary(this);localStore=new LibraryStore(db,null);
        prefs=getPreferences(MODE_PRIVATE);thumbs=new ThumbnailLoader(this);grid=prefs.getBoolean("view_grid",false);
        storageOnly=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE).getBoolean("storage_only",false);
        String server=prefs.getString("server_url","");if(!server.isEmpty())hubStore=new LibraryStore(db,new RemoteClient(server,prefs));
        if(saved!=null){home=saved.getBoolean("home");tab=saved.getInt("tab");try{localFilters=LocalFilters.parse(saved.getString("local","{}"));hubFilters=LocalFilters.parse(saved.getString("hub","{}"));}catch(Exception ignored){}activeCollection=saved.getString("collection");}
        getWindow().setStatusBarColor(BG);getWindow().setNavigationBarColor(PAPER);
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR|View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        shell=ui.col();shell.setBackgroundColor(BG);setContentView(shell);
        header=ui.col();shell.addView(header);content=ui.col();shell.addView(content,new LinearLayout.LayoutParams(-1,0,1));batchDock=ui.col();shell.addView(batchDock);navigation=ui.row();shell.addView(navigation);
        if(Build.VERSION.SDK_INT>=30){getWindow().setDecorFitsSystemWindows(false);shell.setOnApplyWindowInsetsListener((v,insets)->{
            android.graphics.Insets bars=insets.getInsets(WindowInsets.Type.systemBars()),ime=insets.getInsets(WindowInsets.Type.ime());
            shell.setPadding(bars.left,bars.top,bars.right,Math.max(bars.bottom,ime.bottom));navigation.setVisibility(storageOnly||ime.bottom>bars.bottom?View.GONE:View.VISIBLE);return insets;});}
        if(Build.VERSION.SDK_INT>=33)getOnBackInvokedDispatcher().registerOnBackInvokedCallback(android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT,this::handleBack);
        render();if(storageOnly)handler.postDelayed(nodeRefresh,3000);else if(tab==3)handler.postDelayed(transferRefresh,1000);else reload(false,true);
    }
    private LibraryStore store(){return home?hubStore:localStore;}
    private LocalFilters f(){return home?hubFilters:localFilters;}
    private void setFilters(LocalFilters next){if(home)hubFilters=next;else localFilters=next;}
    private void switchSource(boolean remote){
        if(home==remote)return;hideKeyboard();handler.removeCallbacks(searchTask);generation++;home=remote;meta=new LibraryStore.Meta();entries.clear();total=0;error="";needsLogin=false;selected.clear();selecting=false;activeCollection=null;
        render();reload(false,true);
    }
    private void navigate(int page){if(tab==page)return;hideKeyboard();handler.removeCallbacks(searchTask);handler.removeCallbacks(transferRefresh);generation++;loading=false;tab=page;selected.clear();selecting=false;error="";render();if(tab==3)handler.postDelayed(transferRefresh,1000);else reload(false,true);}
    private void render(){
        header.removeAllViews();content.removeAllViews();files=null;adapter=null;search=null;resultArea=null;resultLabel=null;
        if(storageOnly){renderStorageOnly();return;}
        navigation.setVisibility(View.VISIBLE);batchDock.setVisibility(View.VISIBLE);
        header.setPadding(ui.d(22),ui.d(14),ui.d(18),ui.d(14));
        TextView brand=ui.text("MYLIBA",10,GREEN,true);brand.setLetterSpacing(.18f);header.addView(brand);
        LinearLayout title=ui.row();String[] titles={"资源库","文件夹","来源","传输列表"};title.addView(ui.text(titles[tab],28,INK,true),ui.weight());
        LinearLayout source=ui.row();ui.pad(source,12,8);source.addView(ui.icon(home?"server":"phone",17,GREEN));TextView label=ui.text(home?"家中":"本机",12,GREEN,true);label.setPadding(ui.d(7),0,ui.d(5),0);source.addView(label);source.addView(ui.icon("down",15,GREEN));ui.touch(source,SOFT,20,this::sourceSheet);source.setContentDescription("切换资源库，当前"+(home?"家中":"本机"));title.addView(source);
        header.addView(title,ui.top(7));
        if(tab==0)renderLibrary();else if(tab==1)renderFolders();else if(tab==2)renderSources();else renderTransfers();
        renderNav();renderBatch();
    }
    private void enterStorageMode(){
        storageOnly=true;generation++;handler.removeCallbacks(transferRefresh);getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE).edit().putBoolean("storage_only",true).apply();
        render();handler.removeCallbacks(nodeRefresh);handler.postDelayed(nodeRefresh,3000);
    }
    private void leaveStorageMode(){
        storageOnly=false;handler.removeCallbacks(nodeRefresh);getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE).edit().putBoolean("storage_only",false).apply();
        home=false;tab=2;render();reload(false,true);
    }
    private void renderStorageOnly(){
        navigation.setVisibility(View.GONE);batchDock.setVisibility(View.GONE);
        displayedNodeRunning=CloudStorageService.running;
        header.setPadding(ui.d(22),ui.d(18),ui.d(18),ui.d(14));
        header.addView(ui.text("MYLIBA",10,GREEN,true));header.addView(ui.text("存储节点",28,INK,true),ui.top(8));
        ScrollView scroll=new ScrollView(this);scroll.setVerticalScrollBarEnabled(false);LinearLayout body=ui.col();body.setPadding(ui.d(20),ui.d(8),ui.d(20),ui.d(28));scroll.addView(body);content.addView(scroll,new LinearLayout.LayoutParams(-1,-1));
        android.content.SharedPreferences settings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
        boolean openlist="openlist".equals(settings.getString("backend","disk"));
        LinearLayout card=ui.card();nodeTitle=ui.text("",20,INK,true);card.addView(nodeTitle);
        nodeStatus=ui.text("",13,GREEN,false);card.addView(nodeStatus,ui.top(8));
        card.addView(ui.text("云层块存储："+(openlist?"OpenList · "+settings.getString("openlist_mount",""):"本手机的应用存储空间"),12,MUTED,false),ui.top(15));
        String access=settings.getString("url","").isEmpty()?"存储节点中继"+(CloudStorageService.relayConnected?"已连接":"待连接"):"公网直连";
        nodeAccess=ui.text("访问："+access,12,MUTED,false);card.addView(nodeAccess,ui.top(6));
        nodeCounts=ui.text("",13,INK,false);card.addView(nodeCounts,ui.top(15));
        nodeCapacity=ui.text("",12,MUTED,false);card.addView(nodeCapacity,ui.top(5));
        body.addView(card);
        nodeConfigure=ui.button("",true,this::cloudStorageSheet);body.addView(nodeConfigure,ui.top(18));
        nodeStop=ui.button("停止存储节点",false,()->{stopService(new Intent(this,CloudStorageService.class));handler.postDelayed(this::render,350);});body.addView(nodeStop,ui.top(9));
        body.addView(ui.button("切换到完整客户端",false,this::leaveStorageMode),ui.top(22));
        body.addView(ui.text("这台手机作为云层存储节点，供其他节点备份和读取加密块。关闭界面后，前台服务仍会继续运行。",12,MUTED,false),ui.top(17));
        updateStorageOnlyStatus();
    }
    private void updateStorageOnlyStatus(){
        if(nodeTitle==null)return;
        boolean active=CloudStorageService.running;
        nodeTitle.setText(active?"节点运行中":"节点未运行");nodeStatus.setText(CloudStorageService.status);nodeStatus.setTextColor(active?GREEN:MUTED);
        nodeCounts.setText("加密块 "+CloudStorageService.blockCount+" · 索引 "+CloudStorageService.catalogCount);
        nodeCapacity.setVisibility(CloudStorageService.capacityBytes>0?View.VISIBLE:View.GONE);
        if(CloudStorageService.capacityBytes>0)nodeCapacity.setText("可用 "+size(CloudStorageService.availableBytes)+" / "+size(CloudStorageService.capacityBytes));
        android.content.SharedPreferences settings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
        nodeAccess.setText("访问："+(settings.getString("url","").isEmpty()?"存储节点中继"+(CloudStorageService.relayConnected?"已连接":"待连接"):"公网直连"));
        nodeConfigure.setText(active?"配置并重启":"配置并启动");nodeStop.setVisibility(active?View.VISIBLE:View.GONE);
    }
    private void renderNav(){
        navigation.removeAllViews();navigation.setBackgroundColor(PAPER);navigation.setPadding(ui.d(14),ui.d(8),ui.d(14),ui.d(8));
        String[] icons={"library","folder","sources","download"},labels={"资源","文件夹","来源","传输"};
        for(int i=0;i<4;i++){final int index=i;LinearLayout item=ui.col();item.setGravity(Gravity.CENTER);ui.pad(item,0,4);
            FrameLayout bubble=new FrameLayout(this);bubble.setBackground(ui.shape(tab==i?SOFT:Color.TRANSPARENT,17,0));ImageView icon=ui.icon(icons[i],23,tab==i?GREEN:MUTED);FrameLayout.LayoutParams ip=new FrameLayout.LayoutParams(ui.d(23),ui.d(23),Gravity.CENTER);bubble.addView(icon,ip);item.addView(bubble,ui.lp(58,31));TextView t=ui.text(labels[i],11,tab==i?GREEN:MUTED,tab==i);t.setGravity(Gravity.CENTER);item.addView(t,ui.top(4));ui.touch(item,Color.TRANSPARENT,18,()->navigate(index));item.setContentDescription(labels[i]);item.setSelected(tab==i);navigation.addView(item,new LinearLayout.LayoutParams(0,ui.d(60),1));}
    }
    private void renderLibrary(){
        LinearLayout controls=ui.col();controls.setPadding(ui.d(20),0,ui.d(20),0);content.addView(controls);
        LinearLayout searchBox=ui.row();searchBox.setBackground(ui.shape(PAPER,16,LINE));searchBox.setPadding(ui.d(14),0,ui.d(3),0);searchBox.addView(ui.icon("search",21,MUTED));
        search=new EditText(this);search.setSingleLine(true);search.setTextSize(14);search.setTextColor(INK);search.setHintTextColor(MUTED);search.setHint("搜索文件、路径或标签");search.setBackgroundColor(Color.TRANSPARENT);search.setPadding(ui.d(10),0,ui.d(4),0);search.setText(f().query);search.setImeOptions(android.view.inputmethod.EditorInfo.IME_ACTION_SEARCH);searchBox.addView(search,new LinearLayout.LayoutParams(0,ui.d(52),1));
        ImageView clear=ui.iconButton("x","清空搜索",()->search.setText(""));clear.setVisibility(f().query.isEmpty()?View.GONE:View.VISIBLE);searchBox.addView(clear);
        ImageView filter=ui.iconButton("filter","筛选"+(filterCount()>0?"，已应用 "+filterCount()+" 项":""),this::filterSheet);if(filterCount()>0)filter.setBackground(ui.shape(SOFT,12,0));searchBox.addView(filter);controls.addView(searchBox);
        search.addTextChangedListener(new TextWatcher(){public void beforeTextChanged(CharSequence s,int st,int c,int a){}public void afterTextChanged(Editable e){}public void onTextChanged(CharSequence s,int st,int before,int count){f().query=s.toString();clear.setVisibility(s.length()==0?View.GONE:View.VISIBLE);selected.clear();selecting=false;handler.removeCallbacks(searchTask);handler.postDelayed(searchTask,300);}});
        search.setOnEditorActionListener((v,id,event)->{handler.removeCallbacks(searchTask);hideKeyboard();reload(false,false);return true;});
        ArrayList<View> types=new ArrayList<>();String[] keys={"","image","video","document","audio"};String[] names={"全部","图片","视频","文档","音频"};
        for(int i=0;i<keys.length;i++){String key=keys[i];boolean active=key.isEmpty()?f().attributes.stream().noneMatch(x->x.startsWith("type:")):f().attributes.contains("type:"+key);types.add(ui.chip(names[i],active,()->{LocalFilters next=f().copy();next.attributes.removeIf(x->x.startsWith("type:"));if(!key.isEmpty())next.attributes.add("type:"+key);applyFilters(next,false);}));}
        controls.addView(ui.rail(types),ui.top(12));
        ArrayList<View> applied=new ArrayList<>();
        if(!f().folderId.equals("all"))applied.add(ui.chip(folderName(f().folderId)+"  ×",true,()->{f().folderId="all";activeCollection=null;refreshScreen(true);}));
        if(activeCollection!=null){LibraryStore.Group g=find(meta.collections,activeCollection);if(g!=null)applied.add(ui.chip(g.name+"  ×",true,()->{activeCollection=null;refreshScreen(false);}));}
        for(String tag:f().tags)applied.add(ui.chip(tag+"  ×",true,()->{f().tags.remove(tag);refreshScreen(false);}));
        if(f().unorganized)applied.add(ui.chip("待整理  ×",true,()->{f().unorganized=false;refreshScreen(false);}));
        for(String key:f().attributes)if(!key.startsWith("type:"))applied.add(ui.chip(LocalLibrary.attributeLabel(key)+"  ×",true,()->{f().attributes.remove(key);refreshScreen(false);}));
        if(!f().rootUri.isEmpty())applied.add(ui.chip("已选来源  ×",true,()->{f().rootUri="";refreshScreen(false);}));
        if(!applied.isEmpty())controls.addView(ui.rail(applied),ui.top(8));
        LinearLayout tools=ui.row();resultLabel=ui.text("最近文件",13,INK,true);tools.addView(resultLabel,ui.weight());tools.addView(ui.iconButton("sort","排序",this::sortSheet));tools.addView(ui.iconButton(grid?"list":"grid",grid?"切换列表":"切换网格",()->{grid=!grid;prefs.edit().putBoolean("view_grid",grid).apply();render();}));tools.addView(ui.iconButton("more","资源操作",this::libraryMenu));controls.addView(tools,ui.top(8));
        resultArea=new FrameLayout(this);content.addView(resultArea,new LinearLayout.LayoutParams(-1,0,1));
        files=new ListView(this);files.setDivider(null);files.setSelector(android.R.color.transparent);files.setClipToPadding(false);files.setPadding(ui.d(20),0,ui.d(20),ui.d(12));files.setVerticalScrollBarEnabled(false);
        footerLabel=ui.text("加载更多",13,GREEN,true);footerLabel.setGravity(Gravity.CENTER);ui.pad(footerLabel,12,18);footerLabel.setOnClickListener(v->reload(true,false));files.addFooterView(footerLabel);
        adapter=new ResourceAdapter();files.setAdapter(adapter);resultArea.addView(files,new FrameLayout.LayoutParams(-1,-1));updateResults();
    }
    private void updateResults(){
        if(adapter==null||tab!=0)return;adapter.notifyDataSetChanged();resultLabel.setText((selecting?"选择文件":f().query.isEmpty()?"最近文件":"搜索结果")+(total>0?"  ·  "+total:""));
        footerLabel.setText(loading?"正在加载…":entries.size()<total?"加载更多（"+entries.size()+" / "+total+"）":"已显示全部文件");footerLabel.setVisibility(entries.isEmpty()?View.GONE:View.VISIBLE);footerLabel.setEnabled(!loading&&entries.size()<total);
        while(resultArea.getChildCount()>1)resultArea.removeViewAt(1);
        if(entries.isEmpty()){
            ScrollView sc=new ScrollView(this);sc.setFillViewport(true);LinearLayout empty=ui.col();empty.setGravity(Gravity.CENTER);ui.pad(empty,28,20);sc.addView(empty);
            if(loading){ProgressBar p=new ProgressBar(this);p.setIndeterminateTintList(android.content.res.ColorStateList.valueOf(GREEN));empty.addView(p,ui.lp(28,28));empty.addView(centerText("正在读取资源库",13,MUTED),ui.top(16));}
            else if(!error.isEmpty()) {empty.addView(ui.icon(needsLogin?"shield":"link",44,GREEN));empty.addView(centerText(needsLogin?"登录家中资源库":"暂时无法连接",20,INK),ui.top(18));empty.addView(centerText(error,13,MUTED),ui.top(9));empty.addView(ui.button(needsLogin?"登录":"重新连接",true,needsLogin?this::connectSheet:()->reload(false,true)),ui.top(22));}
            else if(home&&hubStore==null){empty.addView(ui.emptyArt(),ui.lp(-1,172));empty.addView(centerText("家中的文件，也能随身浏览",20,INK),ui.top(10));empty.addView(centerText("连接你的 Myliba 服务端，搜索和整理家中设备上的资源。",13,MUTED),ui.top(10));empty.addView(ui.button("连接服务端",true,this::connectSheet),ui.top(24));}
            else if(meta.roots.isEmpty()&&!home){empty.addView(ui.emptyArt(),ui.lp(-1,172));empty.addView(centerText("给文件一个好找的地方",21,INK),ui.top(12));empty.addView(centerText("选一个手机文件夹开始。\n照片、文档和灵感，都留在原来的位置。",13,MUTED),ui.top(10));empty.addView(ui.button("选择手机文件夹",true,this::pickTree),ui.top(24));empty.addView(centerText("仅索引你授权的目录",11,MUTED),ui.top(14));}
            else{empty.addView(ui.icon("search",44,GREEN));empty.addView(centerText("没有找到匹配文件",20,INK),ui.top(20));empty.addView(centerText("换个关键词，或减少一些筛选条件。",13,MUTED),ui.top(9));empty.addView(ui.button("清除筛选",false,()->{String folder=f().folderId;LocalFilters next=new LocalFilters();next.folderId=folder;applyFilters(next,false);}),ui.top(22));}
            resultArea.addView(sc,new FrameLayout.LayoutParams(-1,-1));
        }else if(!error.isEmpty())toast(error);
        renderBatch();
    }
    private TextView centerText(String s,int size,int color){TextView v=ui.text(s,size,color,size>=19);v.setGravity(Gravity.CENTER);return v;}
    private final class ResourceAdapter extends BaseAdapter {
        public int getCount(){return grid?(entries.size()+1)/2:entries.size();}public Object getItem(int p){return entries.get(grid?p*2:p);}public long getItemId(int p){return p;}
        public View getView(int pos,View recycled,android.view.ViewGroup parent){
            LinearLayout row;Cell[] cells;
            if(recycled==null){row=ui.row();row.setGravity(Gravity.TOP);row.setPadding(0,0,0,ui.d(10));cells=new Cell[grid?2:1];for(int i=0;i<cells.length;i++){cells[i]=new Cell(grid);LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,-2,1);if(i==1)p.leftMargin=ui.d(10);row.addView(cells[i].root,p);}row.setTag(cells);}else{row=(LinearLayout)recycled;cells=(Cell[])row.getTag();}
            for(int i=0;i<cells.length;i++){int index=grid?pos*2+i:pos;cells[i].root.setVisibility(index<entries.size()?View.VISIBLE:View.INVISIBLE);if(index<entries.size())cells[i].bind(entries.get(index));}return row;
        }
    }
    private final class Cell {
        final LinearLayout root,copy; final FrameLayout preview; final ImageView image,placeholder,more;final TextView name,info,tags;final boolean tile;
        Cell(boolean tile){this.tile=tile;root=tile?ui.col():ui.row();root.setBackground(ui.shape(PAPER,17,0));ui.pad(root,tile?0:12,tile?0:11);
            preview=new FrameLayout(MainActivity.this);preview.setClipToOutline(true);preview.setBackground(ui.shape(SOFT,tile?16:13,0));image=new ImageView(MainActivity.this);image.setScaleType(ImageView.ScaleType.CENTER_CROP);preview.addView(image,new FrameLayout.LayoutParams(-1,-1));placeholder=ui.icon("file",tile?38:27,GREEN);preview.addView(placeholder,new FrameLayout.LayoutParams(ui.d(tile?38:27),ui.d(tile?38:27),Gravity.CENTER));root.addView(preview,ui.lp(tile?-1:58,tile?122:58));
            copy=ui.col();if(tile)ui.pad(copy,12,12);else copy.setPadding(ui.d(12),0,0,0);
            name=ui.text("",tile?13:14,INK,true);name.setMaxLines(tile?2:1);name.setEllipsize(TextUtils.TruncateAt.END);if(tile)name.setMinHeight(ui.d(36));copy.addView(name);
            info=ui.text("",11,MUTED,false);info.setSingleLine();info.setEllipsize(TextUtils.TruncateAt.END);copy.addView(info,ui.top(5));
            tags=ui.text("",10,GREEN,false);tags.setSingleLine();tags.setEllipsize(TextUtils.TruncateAt.END);copy.addView(tags,ui.top(6));
            if(tile)root.addView(copy,ui.lp(-1,-2));else root.addView(copy,ui.weight());
            more=ui.iconButton("more","文件操作",()->{});if(tile){FrameLayout.LayoutParams p=new FrameLayout.LayoutParams(ui.d(36),ui.d(36),Gravity.END|Gravity.TOP);p.setMargins(0,ui.d(6),ui.d(6),0);more.setPadding(ui.d(9),ui.d(9),ui.d(9),ui.d(9));preview.addView(more,p);}else{ui.pad(more,10,10);root.addView(more,ui.lp(40,40));}
        }
        void bind(LibraryStore.Entry e){
            boolean checked=selected.contains(e.id);name.setText(e.name);info.setText(size(e.size)+"  ·  "+date(e.modified));tags.setText(e.tags.isEmpty()?typeName(e.kind):TextUtils.join(" · ",e.tags));tags.setTextColor(e.tags.isEmpty()?MUTED:GREEN);
            root.setBackground(ui.shape(checked?SOFT:PAPER,17,checked?0xffa6c0aa:0));root.setAlpha(e.available?1:.6f);
            image.setTag(e.id);image.setImageDrawable(null);placeholder.setImageDrawable(new MobileUi.Icon(e.kind,typeColor(e.kind)));placeholder.setVisibility(View.VISIBLE);preview.setBackground(ui.shape(typeBg(e.kind),tile?16:13,0));
            LibraryStore s=store();if(s!=null)thumbs.load(s,e,image,placeholder);
            more.setImageDrawable(new MobileUi.Icon(selecting?(checked?"check":"plus"):"more",selecting?GREEN:INK));more.setContentDescription(selecting?(checked?"取消选择 ":"选择 ")+e.name:e.name+"的操作");more.setOnClickListener(v->{if(selecting)toggleSelection(e.id);else entryMenu(e);});
            if(tile)more.setBackground(ui.shape(0xeaffffff,12,0));
            preview.setOnClickListener(v->{if(selecting)toggleSelection(e.id);else openPreview(e);});
            root.setOnClickListener(v->{if(selecting)toggleSelection(e.id);else showDetail(e);});root.setOnLongClickListener(v->{selecting=true;toggleSelection(e.id);return true;});root.setContentDescription(e.name+"，"+typeName(e.kind)+"，"+size(e.size));
        }
    }
    private void reload(boolean append,boolean metadata){
        if(destroyed||scanning)return;if(append&&loading)return;LibraryStore s=store();if(s==null){loading=false;updateResults();return;}
        LocalFilters query=f().copy();int token=++generation,offset=append?entries.size():0;int targetTab=tab;loading=true;error="";if(!append){selected.clear();selecting=false;}updateResults();
        io.execute(()->{try{LibraryStore.Meta next=(metadata||targetTab==0&&!append)?s.metadata(query):null;LibraryStore.Page p=targetTab==0?s.page(query,offset):null;
            runOnUiThread(()->{if(destroyed||token!=generation||s!=store())return;loading=false;needsLogin=false;if(next!=null)meta=next;if(p!=null){if(!append)entries.clear();entries.addAll(p.items);total=p.total;}if(tab==0)updateResults();else render();});
        }catch(Exception e){runOnUiThread(()->{if(destroyed||token!=generation||s!=store())return;loading=false;error=message(e);needsLogin=e instanceof RemoteClient.Failure&&((RemoteClient.Failure)e).status==401;if(needsLogin)entries.clear();if(tab==0)updateResults();else render();});}});
    }
    private void refreshScreen(boolean metadata){hideKeyboard();handler.removeCallbacks(searchTask);selected.clear();selecting=false;entries.clear();render();reload(false,metadata);}
    private void applyFilters(LocalFilters value,boolean metadata){setFilters(value);refreshScreen(metadata);}
    private int filterCount(){return f().tags.size()+f().attributes.size()+(f().rootUri.isEmpty()?0:1)+(f().unorganized?1:0);}
    private void sourceSheet(){
        MobileUi.Sheet s=ui.sheet("选择资源库","本机与家中的文件分别管理，使用同一套浏览方式。");
        s.body.addView(ui.item("phone","本机文件",!home?"当前资源库 · 可离线使用":"读取已授权的手机文件夹",()->{s.close();switchSource(false);}));
        s.body.addView(ui.item("server","家中资源库",hubStore==null?"连接你的 Myliba 服务端":Uri.parse(hubStore.client.base).getHost(),()->{s.close();switchSource(true);if(hubStore==null)connectSheet();}),ui.top(10));s.show();
    }
    private void libraryMenu(){MobileUi.Sheet s=ui.sheet("资源操作",null);s.body.addView(ui.item("check","选择文件","长按任意文件也可开始选择",()->{s.close();selecting=true;updateResults();}));s.body.addView(ui.item("spark","保存为常用查询","保存当前搜索和筛选条件",()->{s.close();saveCollection(null);}));if(home)s.body.addView(ui.item("refresh","后台预处理","查看缩略图与文件指纹任务",()->{s.close();preprocessSheet();}));s.body.addView(ui.item("refresh","刷新资源库","重新读取索引中的文件",()->{s.close();reload(false,true);}));s.show();}
    private void preprocessSheet(){LibraryStore s=store();if(s==null||!s.remote)return;MobileUi.Sheet sheet=ui.sheet("后台预处理","家中资源库的缩略图和文件指纹任务");TextView loading=ui.text("正在读取任务…",13,MUTED,false);sheet.body.addView(loading);sheet.show();loadPreprocess(sheet,s);}
    private void loadPreprocess(MobileUi.Sheet sheet,LibraryStore s){io.execute(()->{try{JSONObject queue=s.client.json("GET","/api/preprocess",null);runOnUiThread(()->{if(destroyed||!sheet.dialog.isShowing())return;sheet.body.removeAllViews();sheet.footer.removeAllViews();JSONObject counts=queue.optJSONObject("counts");if(counts!=null)sheet.body.addView(ui.text("排队 "+counts.optInt("pending")+" · 处理中 "+counts.optInt("running")+" · 失败 "+counts.optInt("failed")+" · 完成 "+counts.optInt("done"),13,INK,true));if(counts!=null&&counts.optInt("failed")>0)sheet.body.addView(ui.button("重试全部失败任务",false,()->{sheet.close();mutate(s,()->s.client.json("POST","/api/preprocess/retry-failed",null),"已重新排队",()->preprocessSheet());}),ui.top(14));JSONArray jobs=queue.optJSONArray("jobs");if(jobs==null||jobs.length()==0)sheet.body.addView(ui.text("当前没有任务",13,MUTED,false),ui.top(16));else for(int i=0;i<jobs.length();i++){JSONObject job=jobs.optJSONObject(i);if(job==null)continue;String status=job.optString("status");String label=status.equals("pending")?"排队中":status.equals("running")?"处理中":status.equals("failed")?"失败":"已完成";String op=job.optString("operation").equals("thumbnail")?"缩略图":"文件指纹";String detail=op+" · "+label+(job.optInt("priority")>0&&status.equals("pending")?" · 当前列表优先":"");if(!job.optString("error").isEmpty())detail+=" · "+job.optString("error");String id=job.optString("resourceId");LinearLayout row=ui.item(op.equals("缩略图")?"image":"file",job.optString("name"),detail,()->{if(!status.equals("failed"))return;sheet.close();mutate(s,()->s.client.json("POST","/api/preprocess/"+RemoteClient.enc(id)+"/retry",null),"任务已重新排队",()->preprocessSheet());});sheet.body.addView(row,ui.top(8));}sheet.footer.addView(ui.button("刷新状态",false,()->loadPreprocess(sheet,s)),ui.top(8));});}catch(Exception ex){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing()){sheet.body.removeAllViews();sheet.body.addView(ui.text("读取失败："+message(ex),13,RED,false));}});}});}
    private void toggleSelection(String id){selecting=true;if(!selected.add(id))selected.remove(id);updateResults();}
    private void renderBatch(){
        if(batchDock==null)return;batchDock.removeAllViews();batchDock.setVisibility(selecting?View.VISIBLE:View.GONE);if(!selecting)return;
        batchDock.setBackgroundColor(PAPER);batchDock.setPadding(ui.d(16),ui.d(9),ui.d(16),ui.d(10));
        LinearLayout top=ui.row();top.addView(ui.iconButton("x","退出选择",()->{selecting=false;selected.clear();updateResults();}));top.addView(ui.text("已选 "+selected.size()+" 项",14,INK,true),ui.weight());TextView all=ui.chip("全选结果",false,this::selectAll);top.addView(all);batchDock.addView(top);
        LinearLayout actions=ui.row();TextView tags=ui.button("编辑标签",true,this::batchTags);TextView move=ui.button("移动到文件夹",false,()->moveSheet(new ArrayList<>(selected)));tags.setEnabled(!selected.isEmpty()&&!busy);move.setEnabled(!selected.isEmpty()&&!busy);tags.setAlpha(selected.isEmpty()?.45f:1);move.setAlpha(selected.isEmpty()?.45f:1);actions.addView(tags,new LinearLayout.LayoutParams(0,ui.d(46),1));LinearLayout.LayoutParams mp=new LinearLayout.LayoutParams(0,ui.d(46),1);mp.leftMargin=ui.d(10);actions.addView(move,mp);batchDock.addView(actions,ui.top(6));
    }
    private void batchTags(){
        List<String> ids=new ArrayList<>(selected);if(ids.isEmpty())return;LibraryStore target=store();
        MobileUi.Sheet sheet=ui.sheet("编辑标签","已选择 "+ids.size()+" 个文件");
        sheet.body.addView(ui.item("plus","添加标签","为选中的文件添加同一个标签",()->{sheet.close();tagPrompt(ids,null);}));
        section(sheet.body,"移除标签",null,null);
        if(meta.tags.isEmpty())sheet.body.addView(ui.text("当前资源库还没有手动标签。",12,MUTED,false),ui.top(12));
        for(LibraryStore.Facet tag:meta.tags)sheet.body.addView(ui.item("tag",tag.name,"从选中文件中移除此标签",()->{sheet.close();confirm("移除标签“"+tag.name+"”？","仅影响选中的 "+ids.size()+" 个文件。",()->mutate(target,()->target.removeTag(ids,tag.name),"标签已移除",null));}),ui.top(8));
        sheet.show();
    }
    private void selectAll(){if(busy)return;LibraryStore s=store();if(s==null)return;LocalFilters snapshot=f().copy();int token=generation;busy=true;io.execute(()->{try{List<String> ids=s.matching(snapshot);runOnUiThread(()->{busy=false;if(destroyed||s!=store()||token!=generation)return;selected.clear();selected.addAll(ids);selecting=true;updateResults();});}catch(Exception e){runOnUiThread(()->{busy=false;if(!destroyed)toast(message(e));});}});}
    private void filterSheet(){
        MobileUi.Sheet sheet=ui.sheet("筛选资源","多个手动标签需同时满足；同类属性可多选。");LocalFilters draft=f().copy();
        fillFilter(sheet,draft);sheet.show();
    }
    private void fillFilter(MobileUi.Sheet sheet,LocalFilters draft){
        sheet.body.removeAllViews();sheet.footer.removeAllViews();
        filterSection(sheet.body,"手动标签",meta.tags,draft.tags,null);
        ArrayList<LibraryStore.Facet> types=new ArrayList<>(),sizes=new ArrayList<>(),formats=new ArrayList<>();
        for(LibraryStore.Facet a:meta.attributes){if(a.key.startsWith("type:"))types.add(a);else if(a.key.startsWith("size:"))sizes.add(a);else formats.add(a);}
        filterSection(sheet.body,"文件类型",types,draft.attributes,"type:");filterSection(sheet.body,"文件大小",sizes,draft.attributes,"size:");filterSection(sheet.body,"文件格式",formats,draft.attributes,"extension:");
        section(sheet.body,"来源目录",null,null);MobileUi.Flow roots=ui.new Flow();
        TextView all=ui.chip("全部目录",draft.rootUri.isEmpty(),()->{draft.rootUri="";fillFilter(sheet,draft);});roots.addView(all);
        for(LibraryStore.Group r:meta.roots)roots.addView(ui.chip(shortPath(r.name),draft.rootUri.equals(r.id),()->{draft.rootUri=r.id;fillFilter(sheet,draft);}));sheet.body.addView(roots,ui.top(10));
        LinearLayout untagged=ui.row();untagged.addView(ui.text("只看待整理文件",14,INK,true),ui.weight());Switch toggle=new Switch(this);toggle.setChecked(draft.unorganized);toggle.setThumbTintList(android.content.res.ColorStateList.valueOf(GREEN));toggle.setOnCheckedChangeListener((v,checked)->draft.unorganized=checked);untagged.addView(toggle);sheet.body.addView(untagged,ui.top(24));sheet.body.addView(ui.text("还没有添加手动标签的文件",11,MUTED,false),ui.top(5));
        LinearLayout buttons=ui.row();buttons.addView(ui.button("重置",false,()->{String folder=draft.folderId;LocalFilters reset=new LocalFilters();reset.folderId=folder;fillFilter(sheet,reset);}),new LinearLayout.LayoutParams(0,ui.d(48),1));LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,ui.d(48),2);p.leftMargin=ui.d(10);buttons.addView(ui.button("应用筛选",true,()->{sheet.close();applyFilters(draft,false);}),p);sheet.footer.addView(buttons,ui.top(10));
    }
    private void filterSection(LinearLayout parent,String title,List<LibraryStore.Facet> choices,List<String> selected,String prefix){
        section(parent,title,null,null);if(choices.isEmpty()){parent.addView(ui.text("当前资源库暂无"+title,12,MUTED,false),ui.top(8));return;}MobileUi.Flow flow=ui.new Flow();
        for(LibraryStore.Facet c:choices){TextView chip=ui.chip(c.name+"  "+c.count,selected.contains(c.key),()->{});chip.setOnClickListener(v->{if(selected.contains(c.key))selected.remove(c.key);else selected.add(c.key);boolean active=selected.contains(c.key);chip.setTextColor(active?GREEN:MUTED);chip.setBackground(ui.shape(active?SOFT:PAPER,20,active?0:LINE));chip.setSelected(active);});if(!selected.contains(c.key))chip.setBackground(ui.shape(PAPER,20,LINE));flow.addView(chip);}parent.addView(flow,ui.top(10));
    }
    private void sortSheet(){MobileUi.Sheet s=ui.sheet("排序方式",null);String[] keys={"modified_desc","modified_asc","name_asc","name_desc","size_desc"},names={"最近修改","最早修改","名称 A–Z","名称 Z–A","文件从大到小"};for(int i=0;i<keys.length;i++){String key=keys[i];s.body.addView(ui.item(key.equals(f().sort)?"check":"sort",names[i],null,()->{s.close();f().sort=key;reload(false,false);}));}s.show();}
    private void renderFolders(){
        LinearLayout body=scrollBody();body.addView(ui.text("按主题收纳，让下次查找更轻松。",13,MUTED,false));
        if(home&&(hubStore==null||needsLogin)){body.addView(connectionCard(),ui.top(18));return;}
        if(!error.isEmpty())body.addView(errorCard(),ui.top(12));
        LinearLayout quick=ui.row();quick.addView(folderTile("all","全部文件",meta.total,false),new LinearLayout.LayoutParams(0,-2,1));LinearLayout.LayoutParams qp=new LinearLayout.LayoutParams(0,-2,1);qp.leftMargin=ui.d(12);quick.addView(folderTile("unfiled","未归组",meta.unfiled,true),qp);body.addView(quick,ui.top(20));
        section(body,"我的文件夹","新建",()->folderPrompt(null));
        if(meta.folders.isEmpty()){LinearLayout empty=ui.card();empty.addView(ui.text("整理成自己的收藏",15,INK,true));empty.addView(ui.text("创建“工作资料”或“旅行”，收纳已经索引的文件。",12,MUTED,false),ui.top(7));empty.addView(ui.button("创建文件夹",false,()->folderPrompt(null)),ui.top(16));body.addView(empty,ui.top(12));}
        for(LibraryStore.Group g:meta.folders){LinearLayout r=ui.item("folder",g.name,g.count+" 个文件",()->openFolder(g.id));r.removeViewAt(r.getChildCount()-1);r.addView(ui.iconButton("more",g.name+"的操作",()->folderMenu(g)));body.addView(r,ui.top(8));}
        section(body,"常用查询","新建查询",()->saveCollection(null));
        body.addView(ui.text("保存搜索和筛选条件，下次打开时按最新文件重新查询。",12,MUTED,false),ui.top(7));
        if(meta.collections.isEmpty())body.addView(ui.item("spark","创建第一个常用查询","例如：旅行标签下的全部图片",()->saveCollection(null)),ui.top(12));
        for(LibraryStore.Group g:meta.collections){LinearLayout r=ui.item("spark",g.name,collectionSummary(g.filters),()->{setFilters(g.filters.copy());activeCollection=g.id;tab=0;refreshScreen(true);});r.removeViewAt(r.getChildCount()-1);r.addView(ui.iconButton("more",g.name+"的操作",()->collectionMenu(g)));body.addView(r,ui.top(8));}
    }
    private View folderTile(String id,String name,int count,boolean inbox){LinearLayout card=ui.card();card.addView(ui.icon(inbox?"download":"library",27,GREEN));card.addView(ui.text(name,15,INK,true),ui.top(16));card.addView(ui.text(count+" 个文件",11,MUTED,false),ui.top(6));ui.touch(card,inbox?SOFT:PAPER,20,()->openFolder(id));return card;}
    private void openFolder(String id){LocalFilters next=new LocalFilters();next.folderId=id;setFilters(next);activeCollection=null;tab=0;refreshScreen(true);}
    private void folderPrompt(LibraryStore.Group g){prompt(g==null?"新建文件夹":"重命名文件夹","文件夹名称",g==null?"":g.name,value->{LibraryStore s=store();mutate(s,()->s.saveFolder(g==null?null:g.id,value),"文件夹已保存",null);});}
    private void folderMenu(LibraryStore.Group g){MobileUi.Sheet s=ui.sheet(g.name,g.count+" 个文件");s.body.addView(ui.item("edit","重命名",null,()->{s.close();folderPrompt(g);}));s.body.addView(ui.item("trash","删除文件夹","其中的文件会回到未归组，原文件不受影响",()->{s.close();confirm("删除“"+g.name+"”？","只移除虚拟文件夹，文件和标签会保留。",()->{LibraryStore st=store();mutate(st,()->st.deleteFolder(g.id),"文件夹已删除",()->{if(f().folderId.equals(g.id))f().folderId="unfiled";});});}));s.show();}
    private void saveCollection(LibraryStore.Group g){LocalFilters snapshot=g==null?f().copy():g.filters.copy();prompt(g==null?"保存为常用查询":"重命名查询","例如：旅行图片",g==null?"":g.name,name->{LibraryStore s=store();mutate(s,()->s.saveCollection(g==null?null:g.id,name,snapshot),"常用查询已保存",null);});}
    private void collectionMenu(LibraryStore.Group g){MobileUi.Sheet s=ui.sheet(g.name,collectionSummary(g.filters));s.body.addView(ui.item("edit","重命名",null,()->{s.close();saveCollection(g);}));s.body.addView(ui.item("refresh","用当前筛选更新",collectionSummary(f()),()->{s.close();LibraryStore st=store();LocalFilters copy=f().copy();mutate(st,()->st.saveCollection(g.id,g.name,copy),"查询条件已更新",null);}));s.body.addView(ui.item("trash","删除常用查询","文件不会被删除",()->{s.close();confirm("删除这个常用查询？",g.name,()->{LibraryStore st=store();mutate(st,()->st.deleteCollection(g.id),"常用查询已删除",()->{if(g.id.equals(activeCollection))activeCollection=null;});});}));s.show();}
    private void renderSources(){
        LinearLayout body=scrollBody();body.addView(ui.text("连接文件所在的地方。",13,MUTED,false));
        LinearLayout current=ui.card();LinearLayout head=ui.row();ImageView symbol=ui.icon(home?"server":"phone",44,GREEN);ui.pad(symbol,10,10);symbol.setBackground(ui.shape(SOFT,14,0));head.addView(symbol);LinearLayout copy=ui.col();copy.setPadding(ui.d(12),0,0,0);copy.addView(ui.text(home?"家中资源库":"这台手机",17,INK,true));copy.addView(ui.text(home?(hubStore==null?"尚未连接":needsLogin?"需要登录":error.isEmpty()?(meta.online?"节点在线":"等待家中节点"):"连接中断"):"本地索引 · 离线可用",11,MUTED,false),ui.top(5));head.addView(copy,ui.weight());current.addView(head);current.addView(ui.text(home?(hubStore==null?"添加服务端地址，在手机上浏览家中资源。":hubStore.client.base):"只访问你授权的文件夹，不移动原文件。",12,MUTED,false),ui.top(15));
        if(home){current.addView(ui.button(hubStore==null?"连接服务端":"连接设置",true,this::connectSheet),ui.top(16));if(hubStore!=null)current.addView(ui.button("退出登录",false,this::logout),ui.top(8));}else current.addView(ui.button(scanning?"正在扫描…":"添加索引目录",true,this::pickTree),ui.top(16));body.addView(current,ui.top(20));
        if(!home){section(body,"云层存储节点",null,null);body.addView(ui.item("server",CloudStorageService.running?"存储节点运行中":"用这台手机存储加密块",CloudStorageService.running?"端口 4319 · "+CloudStorageService.status:"可用公网 HTTPS 地址或存储中继",this::cloudStorageSheet),ui.top(10));body.addView(ui.item("shield","切换为仅存储模式","保留同一 APK，只显示节点运行与配置",this::enterStorageMode),ui.top(9));}
        if(!scanMessage.isEmpty()&&!home)body.addView(ui.text(scanMessage,12,GREEN,false),ui.top(12));if(!error.isEmpty())body.addView(errorCard(),ui.top(12));
        section(body,"索引目录",meta.roots.size()+" 个",null);
        if(meta.roots.isEmpty())body.addView(ui.text(home?"连接后可查看服务端已配置的目录。":"在这里添加照片、下载或工作资料文件夹。",12,MUTED,false),ui.top(10));
        for(LibraryStore.Group r:meta.roots){LinearLayout row=ui.item("folder",shortPath(r.name),r.count+" 个文件"+(home?" · "+r.name:""),()->{LocalFilters next=new LocalFilters();next.rootUri=r.id;setFilters(next);activeCollection=null;tab=0;refreshScreen(false);});if(!home){row.removeViewAt(row.getChildCount()-1);row.addView(ui.iconButton("more",r.name+"的操作",()->rootMenu(r)));}body.addView(row,ui.top(9));}
        if(!home){section(body,"家中资源库",null,null);body.addView(connectionCard(),ui.top(12));}
        LinearLayout note=ui.row();note.addView(ui.icon("shield",18,MUTED));TextView message=ui.text("本机与服务端的标签、文件夹分别保存。",11,MUTED,false);message.setPadding(ui.d(8),0,0,0);note.addView(message,ui.weight());body.addView(note,ui.top(24));
    }
    private View connectionCard(){return ui.item("server",hubStore==null?"连接家中资源库":"打开家中资源库",hubStore==null?"通过你的 Myliba 服务端访问家中文件":Uri.parse(hubStore.client.base).getHost(),()->{if(hubStore==null||needsLogin)connectSheet();else if(home)navigate(0);else{tab=0;switchSource(true);}});}
    private void cloudStorageSheet(){
        android.content.SharedPreferences settings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
        String storedToken,storedPassword;try{storedToken=CloudSecretStore.read(settings,"token");storedPassword=CloudSecretStore.read(settings,"openlist_password");}catch(Exception ex){toast("云层凭据读取失败，请重新配置节点");storedToken="";storedPassword="";}
        MobileUi.Sheet sheet=ui.sheet("手机云层存储节点","同一应用可浏览文件或只运行存储节点。节点只处理已加密的文件块。");
        sheet.body.addView(ui.text("存储位置",12,INK,true));
        Spinner backendChoice=new Spinner(this);ArrayAdapter<String> choices=new ArrayAdapter<>(this,android.R.layout.simple_spinner_item,new String[]{"本手机存储（云层节点）","OpenList 云盘（可选后端）"});choices.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);backendChoice.setAdapter(choices);
        backendChoice.setSelection("openlist".equals(settings.getString("backend","disk"))?1:0);sheet.body.addView(backendChoice,ui.top(7));
        LinearLayout openlistFields=ui.col();
        openlistFields.addView(ui.text("OpenList HTTPS 地址",12,INK,true));EditText openlistURL=ui.input("https://openlist.example.com",settings.getString("openlist_url",""));openlistFields.addView(openlistURL,ui.top(7));
        openlistFields.addView(ui.text("可写挂载路径",12,INK,true),ui.top(16));EditText mount=ui.input("例如 aliyun 或 quark",settings.getString("openlist_mount",""));openlistFields.addView(mount,ui.top(7));
        openlistFields.addView(ui.text("OpenList 用户名",12,INK,true),ui.top(16));EditText user=ui.input("有 WebDAV 管理权限的用户",settings.getString("openlist_user",""));openlistFields.addView(user,ui.top(7));
        openlistFields.addView(ui.text("OpenList 密码",12,INK,true),ui.top(16));EditText password=ui.input("OpenList 用户密码",storedPassword);password.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);openlistFields.addView(password,ui.top(7));
        openlistFields.addView(ui.text("云盘账号由 OpenList 管理；这里通过 /dav/挂载路径 存取加密块。请在 OpenList 中选择可写驱动，并启用 WebDAV 本机代理。",12,MUTED,false),ui.top(9));
        sheet.body.addView(openlistFields,ui.top(17));openlistFields.setVisibility(backendChoice.getSelectedItemPosition()==1?View.VISIBLE:View.GONE);
        backendChoice.setOnItemSelectedListener(new android.widget.AdapterView.OnItemSelectedListener(){public void onNothingSelected(android.widget.AdapterView<?> parent){}public void onItemSelected(android.widget.AdapterView<?> parent,View view,int position,long id){openlistFields.setVisibility(position==1?View.VISIBLE:View.GONE);}});
        sheet.body.addView(ui.text("Hub HTTPS 地址",12,INK,true));EditText hub=ui.input("https://hub.example.com",settings.getString("hub",""));sheet.body.addView(hub,ui.top(7));
        sheet.body.addView(ui.text("手机的直连 HTTPS 地址",12,INK,true),ui.top(16));EditText endpoint=ui.input("https://phone.example.com",settings.getString("url",""));sheet.body.addView(endpoint,ui.top(7));
        sheet.body.addView(ui.text("存储中继 HTTPS 地址（直连不可用时填写）",12,INK,true),ui.top(16));EditText relay=ui.input("https://storage.example.com",settings.getString("relay",""));sheet.body.addView(relay,ui.top(7));
        sheet.body.addView(ui.text("云层令牌",12,INK,true),ui.top(16));EditText secret=ui.input("与 Hub 的 MYLIBA_CLOUD_TOKEN 相同",storedToken);secret.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);sheet.body.addView(secret,ui.top(7));
        TextView validation=ui.text("没有手机公网地址时，只填存储中继地址。中继节点需启用 MYLIBA_CLOUD_RELAY_ENABLED=1。",12,MUTED,false);sheet.body.addView(validation,ui.top(14));
        sheet.footer.addView(ui.button(CloudStorageService.running?"保存并重启节点":"启动存储节点",true,()->{
            String h=hub.getText().toString().trim(),u=endpoint.getText().toString().trim(),r=relay.getText().toString().trim(),t=secret.getText().toString();
            if(!validCloudAddress(h)||(!u.isEmpty()&&!validCloudAddress(u))||(!r.isEmpty()&&!validCloudAddress(r))||(u.isEmpty()&&r.isEmpty())||t.length()<32||t.contains("\r")||t.contains("\n")){validation.setText("需要有效的 Hub、直连或中继 HTTPS 地址，以及至少 32 字符的云层令牌");validation.setTextColor(RED);return;}
            String backend=backendChoice.getSelectedItemPosition()==1?"openlist":"disk";
            String address=openlistURL.getText().toString().trim(),mountPath=mount.getText().toString().trim(),username=user.getText().toString().trim(),pass=password.getText().toString();
            if(backend.equals("openlist"))try{new CloudOpenListBackend(address,mountPath,username,pass);}catch(Exception ex){validation.setText(message(ex));validation.setTextColor(RED);return;}
            String protectedToken,protectedPassword;try{protectedToken=CloudSecretStore.protect(t);protectedPassword=CloudSecretStore.protect(pass);}catch(Exception ex){validation.setText("无法安全保存云层凭据："+message(ex));validation.setTextColor(RED);return;}
            settings.edit().putString("hub",h.replaceAll("/+$","")).putString("url",u.replaceAll("/+$","")).putString("relay",r.replaceAll("/+$","")).putString("token",protectedToken)
                .putString("backend",backend).putString("openlist_url",address.replaceAll("/+$","")).putString("openlist_mount",mountPath).putString("openlist_user",username).putString("openlist_password",protectedPassword).apply();
            stopService(new Intent(this,CloudStorageService.class));
            handler.postDelayed(()->{startForegroundService(new Intent(this,CloudStorageService.class));if(storageOnly||tab==2)handler.postDelayed(this::render,500);},400);
            sheet.close();
        }));
        if(CloudStorageService.running)sheet.footer.addView(ui.button("停止存储节点",false,()->{stopService(new Intent(this,CloudStorageService.class));sheet.close();handler.postDelayed(this::render,300);}),ui.top(9));
        sheet.show();
    }
    private static boolean validCloudAddress(String address){
        try{java.net.URI uri=new java.net.URI(address);return "https".equalsIgnoreCase(uri.getScheme())&&uri.getHost()!=null&&uri.getUserInfo()==null&&uri.getRawQuery()==null&&uri.getRawFragment()==null&&(uri.getPath().isEmpty()||uri.getPath().equals("/"));}catch(Exception ex){return false;}
    }
    private View errorCard(){LinearLayout card=ui.card();card.addView(ui.text(error,13,RED,false));card.addView(ui.button(needsLogin?"登录":"重试",false,needsLogin?this::connectSheet:()->reload(false,true)),ui.top(12));return card;}
    private LinearLayout scrollBody(){ScrollView scroll=new ScrollView(this);scroll.setVerticalScrollBarEnabled(false);LinearLayout body=ui.col();body.setPadding(ui.d(20),0,ui.d(20),ui.d(24));scroll.addView(body);content.addView(scroll,new LinearLayout.LayoutParams(-1,-1));return body;}
    private void section(LinearLayout parent,String label,String action,Runnable run){LinearLayout row=ui.row();row.addView(ui.text(label,15,INK,true),ui.weight());if(action!=null){TextView t=ui.text(action,12,GREEN,true);ui.pad(t,4,8);if(run!=null)t.setOnClickListener(v->run.run());row.addView(t);}parent.addView(row,ui.top(24));}
    private void rootMenu(LibraryStore.Group g){MobileUi.Sheet s=ui.sheet(g.name,g.count+" 个已索引文件");s.body.addView(ui.item("refresh","重新扫描","读取新增或已修改的文件",()->{s.close();scan(Uri.parse(g.id));}));s.body.addView(ui.item("trash","移除索引目录","移除本地索引和标签，原文件仍保留",()->{s.close();confirm("移除这个目录？","此目录的本地索引和标签将删除，原文件不受影响。",()->mutate(localStore,()->{db.removeRoot(g.id);try{getContentResolver().releasePersistableUriPermission(Uri.parse(g.id),Intent.FLAG_GRANT_READ_URI_PERMISSION);}catch(SecurityException ignored){}},"已移除目录",()->{if(localFilters.rootUri.equals(g.id))localFilters.rootUri="";}));}));s.show();}
    private void connectSheet(){
        MobileUi.Sheet s=ui.sheet("连接家中资源库","填写 Myliba 服务端的 HTTPS 地址与访问密码。");s.body.addView(ui.icon("server",42,GREEN));s.body.addView(ui.text("服务端地址",12,INK,true),ui.top(22));EditText address=ui.input("https://library.example.com",hubStore==null?"":hubStore.client.base);address.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_URI);s.body.addView(address,ui.top(9));s.body.addView(ui.text("访问密码",12,INK,true),ui.top(18));EditText password=ui.input("与 Web 端使用相同的密码","");password.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);s.body.addView(password,ui.top(9));TextView status=ui.text("密码不会保存在手机上。",12,MUTED,false);s.body.addView(status,ui.top(14));TextView connect=ui.button("连接资源库",true,()->{});s.footer.addView(connect);
        connect.setOnClickListener(v->{if(busy)return;final String base;try{base=RemoteClient.normalize(address.getText().toString());}catch(Exception e){status.setText(message(e));status.setTextColor(RED);return;}String pass=password.getText().toString();RemoteClient candidate=new RemoteClient(base,prefs);connect.setEnabled(false);connect.setText("正在连接…");status.setText("正在验证连接");status.setTextColor(MUTED);busy=true;
            io.execute(()->{try{if(!pass.isEmpty())candidate.json("POST","/api/session",RemoteClient.object("password",pass));else candidate.json("GET","/api/session",null);runOnUiThread(()->{busy=false;if(destroyed)return;prefs.edit().putString("server_url",base).putString("server_cookie",candidate.cookie()).apply();hubStore=new LibraryStore(db,candidate);home=true;tab=0;needsLogin=false;error="";meta=new LibraryStore.Meta();entries.clear();generation++;s.close();render();reload(false,true);});}catch(Exception e){runOnUiThread(()->{busy=false;if(destroyed)return;connect.setEnabled(true);connect.setText("连接资源库");status.setText(e instanceof RemoteClient.Failure&&((RemoteClient.Failure)e).status==401?"密码不正确或登录已过期，请重新输入。":message(e));status.setTextColor(RED);});}});
        });s.show();
    }
    private void logout(){if(hubStore==null)return;confirm("退出家中资源库？","本机文件和索引会保留。",()->{LibraryStore old=hubStore;mutate(old,()->{try{old.client.json("DELETE","/api/session",null);}catch(java.io.IOException ignored){}finally{old.client.forget();}},"已退出登录",()->{needsLogin=true;entries.clear();home=false;tab=2;meta=new LibraryStore.Meta();});});}
    private void pickTree(){if(scanning){toast("当前目录扫描完成后再添加");return;}Intent i=new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION|Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);try{startActivityForResult(i,PICK_TREE);}catch(ActivityNotFoundException e){toast("设备没有可用的文件夹选择器");}}
    @Override protected void onActivityResult(int request,int result,Intent data){super.onActivityResult(request,result,data);if(request!=PICK_TREE||result!=RESULT_OK||data==null||data.getData()==null)return;Uri uri=data.getData();try{getContentResolver().takePersistableUriPermission(uri,Intent.FLAG_GRANT_READ_URI_PERMISSION);String name=DocumentsContract.getTreeDocumentId(uri);int slash=Math.max(name.lastIndexOf('/'),name.lastIndexOf(':'));if(slash>=0)name=name.substring(slash+1);if(name.isEmpty())name="手机存储";db.addRoot(uri,name);home=false;tab=0;meta=new LibraryStore.Meta();entries.clear();render();scan(uri);}catch(Exception e){toast("无法授权目录："+message(e));}}
    private void scan(Uri uri){if(scanning)return;scanning=true;scanMessage="正在索引文件…";generation++;loading=true;updateResults();toast(scanMessage);io.execute(()->{try{int count=new TreeScanner(getContentResolver(),db).scan(uri);runOnUiThread(()->{if(destroyed)return;scanning=false;loading=false;scanMessage="已索引 "+count+" 个文件";thumbs.clear();toast(scanMessage);if(!home)reload(false,true);});}catch(Exception e){runOnUiThread(()->{if(destroyed)return;scanning=false;loading=false;scanMessage="扫描失败："+message(e);error=scanMessage;toast(scanMessage);if(tab==0)updateResults();else render();});}});}
    private void showDetail(LibraryStore.Entry e){
        LibraryStore s=store();if(s==null)return;MobileUi.Sheet sheet=ui.sheet(e.name,typeName(e.kind)+" · "+size(e.size));detailBody(sheet,s,e);sheet.show();
        io.execute(()->{try{LibraryStore.Entry fresh=s.detail(e);runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing())detailBody(sheet,s,fresh);});}catch(Exception failure){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing())toast(message(failure));});}});
    }
    private void detailBody(MobileUi.Sheet sheet,LibraryStore s,LibraryStore.Entry e){
        sheet.body.removeAllViews();sheet.footer.removeAllViews();FrameLayout frame=new FrameLayout(this);frame.setBackground(ui.shape(typeBg(e.kind),18,0));frame.setClipToOutline(true);ImageView img=new ImageView(this);img.setScaleType(ImageView.ScaleType.FIT_CENTER);frame.addView(img,new FrameLayout.LayoutParams(-1,-1));ImageView icon=ui.icon(e.kind,54,typeColor(e.kind));frame.addView(icon,new FrameLayout.LayoutParams(ui.d(54),ui.d(54),Gravity.CENTER));sheet.body.addView(frame,ui.lp(-1,190));thumbs.load(s,e,img,icon);
        if(s.remote&&(e.kind.equals("image")||e.kind.equals("video"))&&e.available){TextView generate=ui.chip("重新生成移动端封面",false,()->mutate(s,()->s.client.json("POST","/api/resources/"+RemoteClient.enc(e.id)+"/thumbnail?size=mobile",null),"移动端封面已生成",()->{thumbs.clear();detailBody(sheet,s,e);}));sheet.body.addView(generate,ui.top(8));}
        if(e.kind.equals("video")&&e.available)sheet.body.addView(ui.item("video","分段截图","按视频时长分为 6 段，生成适合手机的截图",()->{sheet.close();showVideoCaptures(s,e);}),ui.top(12));
        section(sheet.body,"文件信息",null,null);detailLine(sheet.body,"大小",size(e.size));detailLine(sheet.body,"修改时间",dateTime(e.modified));detailLine(sheet.body,"文件路径",e.path);if(!e.available)detailLine(sheet.body,"状态","来源暂不可用");
        section(sheet.body,"手动标签","添加",()->tagPrompt(Collections.singletonList(e.id),()->refreshDetail(sheet,s,e)));
        if(e.tags.isEmpty())sheet.body.addView(ui.text("加一个标签，下次更容易找到。",12,MUTED,false),ui.top(9));else{MobileUi.Flow flow=ui.new Flow();for(String t:e.tags)flow.addView(ui.chip(t+"  ×",true,()->confirm("移除标签“"+t+"”？",e.name,()->mutate(s,()->s.removeTag(Collections.singletonList(e.id),t),"标签已移除",()->refreshDetail(sheet,s,e)))));sheet.body.addView(flow,ui.top(10));}
        sheet.body.addView(ui.item("folder","移动到文件夹",folderName(e.folderId),()->{sheet.close();moveSheet(Collections.singletonList(e.id));}),ui.top(20));
        if(s.remote){sheet.body.addView(ui.item("spark","获取建议标签","根据服务端的资源信息获取建议",()->suggestTags(sheet,s,e)),ui.top(10));}
        TextView open=ui.button("全屏预览",true,()->{sheet.close();openPreview(e);});open.setEnabled(e.available);open.setAlpha(e.available?1:.45f);sheet.footer.addView(open,ui.top(8));
        if(s.remote)sheet.footer.addView(ui.button("下载原文件",false,()->download(s,e)),ui.top(8));
    }
    private void showVideoCaptures(LibraryStore store,LibraryStore.Entry entry){
        MobileUi.Sheet sheet=ui.sheet("分段截图",entry.name);
        sheet.body.addView(ui.text("正在读取视频时长…",13,MUTED,false));sheet.show();
        io.execute(()->{
            try{
                double duration;
                if(store.remote)duration=store.client.json("GET","/api/resources/"+RemoteClient.enc(entry.id)+"/video-info",null).getDouble("duration");
                else duration=localVideoDuration(Uri.parse(entry.id));
                if(!Double.isFinite(duration)||duration<=0)throw new IllegalArgumentException("视频时长不可用");
                runOnUiThread(()->{
                    if(destroyed||!sheet.dialog.isShowing())return;
                    sheet.body.removeAllViews();sheet.footer.removeAllViews();
                    sheet.body.addView(ui.text("每段中点截取一帧 · 移动端最长边不超过 960 像素",12,MUTED,false));
                    ArrayList<Bitmap> generated=new ArrayList<>();
                    sheet.dialog.setOnDismissListener(ignored->{for(Bitmap bitmap:generated)bitmap.recycle();});
                    ArrayList<TextView> actions=new ArrayList<>();
                    for(int index=0;index<6;index++){
                        final int part=index;double at=Math.max(0,Math.min(duration-0.05,(index+0.5)*duration/6));
                        LinearLayout card=ui.card();card.addView(ui.text("第 "+(index+1)+" 段 · "+String.format(Locale.ROOT,"%.1f",at)+" 秒",14,INK,true));
                        ImageView image=new ImageView(this);image.setScaleType(ImageView.ScaleType.FIT_CENTER);image.setBackground(ui.shape(SOFT,12,0));card.addView(image,ui.top(8));image.getLayoutParams().height=ui.d(150);
                        TextView state=ui.text("尚未生成",12,MUTED,false);card.addView(state,ui.top(6));
                        TextView action=ui.button("生成截图",false,()->{
                            captureVideoFrame(sheet,store,entry,at,image,state,generated,actions.get(part));
                        });actions.add(action);card.addView(action,ui.top(10));sheet.body.addView(card,ui.top(10));
                    }
                    sheet.footer.addView(ui.button("生成全部截图",true,()->{for(TextView action:actions)if(action.isEnabled())action.performClick();}),ui.top(8));
                });
            }catch(Exception error){runOnUiThread(()->{
                if(destroyed||!sheet.dialog.isShowing())return;
                sheet.body.removeAllViews();sheet.footer.removeAllViews();
                sheet.body.addView(ui.text("无法读取视频时长，请输入截图位置（秒）。",13,MUTED,false));
                EditText input=ui.input("例如 1.5 秒","0.5");
                input.setInputType(android.text.InputType.TYPE_CLASS_NUMBER|android.text.InputType.TYPE_NUMBER_FLAG_DECIMAL);
                sheet.body.addView(input,ui.top(12));
                ImageView image=new ImageView(this);image.setScaleType(ImageView.ScaleType.FIT_CENTER);image.setBackground(ui.shape(SOFT,12,0));sheet.body.addView(image,ui.top(12));image.getLayoutParams().height=ui.d(200);
                TextView state=ui.text("输入时间后生成适合手机的截图",12,MUTED,false);sheet.body.addView(state,ui.top(8));
                ArrayList<Bitmap> generated=new ArrayList<>();
                sheet.dialog.setOnDismissListener(ignored->{for(Bitmap bitmap:generated)bitmap.recycle();});
                TextView action=ui.button("生成截图",true,()->{});
                action.setOnClickListener(view->{
                    try{
                        double at=Double.parseDouble(input.getText().toString());
                        if(!Double.isFinite(at)||at<0||at>86400)throw new NumberFormatException();
                        captureVideoFrame(sheet,store,entry,at,image,state,generated,action);
                    }catch(NumberFormatException invalid){state.setText("请输入 0–86400 秒之间的时间");}
                });
                sheet.footer.addView(action,ui.top(8));
            });}
        });
    }
    private double localVideoDuration(Uri uri)throws Exception{
        double seconds=0;
        MediaMetadataRetriever retriever=new MediaMetadataRetriever();
        try{
            retriever.setDataSource(this,uri);
            String value=retriever.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION);
            if(value!=null&&!value.isEmpty())seconds=Long.parseLong(value)/1000.0;
        }finally{retriever.release();}
        if(seconds>0)return seconds;
        MediaExtractor extractor=new MediaExtractor();
        try{
            extractor.setDataSource(this,uri,null);
            for(int index=0;index<extractor.getTrackCount();index++){
                MediaFormat format=extractor.getTrackFormat(index);
                if(format.containsKey(MediaFormat.KEY_DURATION))seconds=Math.max(seconds,format.getLong(MediaFormat.KEY_DURATION)/1_000_000.0);
            }
        }finally{extractor.release();}
        if(seconds>0)return seconds;
        MediaPlayer player=MediaPlayer.create(this,uri);
        if(player!=null)try{seconds=player.getDuration()/1000.0;}finally{player.release();}
        return seconds;
    }
    private void captureVideoFrame(MobileUi.Sheet sheet,LibraryStore store,LibraryStore.Entry entry,double at,ImageView image,TextView state,ArrayList<Bitmap> generated,TextView action){
        action.setEnabled(false);state.setText("正在生成…");
        io.execute(()->{
            try{
                Bitmap bitmap;
                if(store.remote){
                    String path="/api/resources/"+RemoteClient.enc(entry.id)+"/thumbnail?size=mobile&at="+String.format(Locale.ROOT,"%.3f",at);
                    store.client.bytes("POST",path,null,1024);
                    byte[] bytes=store.client.bytes("GET",path,null,3*1024*1024);
                    bitmap=android.graphics.BitmapFactory.decodeByteArray(bytes,0,bytes.length);
                }else bitmap=ThumbnailLoader.videoFrame(this,Uri.parse(entry.id),(long)(at*1_000_000));
                if(bitmap==null)throw new IllegalStateException("视频帧不可用");
                runOnUiThread(()->{
                    if(destroyed||!sheet.dialog.isShowing()){bitmap.recycle();return;}
                    generated.add(bitmap);image.setImageBitmap(bitmap);state.setText(bitmap.getWidth()+" × "+bitmap.getHeight());action.setText("重新生成截图");action.setEnabled(true);
                });
            }catch(Exception error){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing()){state.setText("生成失败："+message(error));action.setEnabled(true);}});}
        });
    }
    private void suggestTags(MobileUi.Sheet detail,LibraryStore s,LibraryStore.Entry e){
        MobileUi.Sheet suggestions=ui.sheet("建议标签","选择一个建议，将它添加到这个文件。");TextView state=ui.text("正在读取建议…",13,MUTED,false);suggestions.body.addView(state);suggestions.show();
        io.execute(()->{try{org.json.JSONArray list=s.client.json("GET","/api/resources/"+RemoteClient.enc(e.id)+"/tag-suggestions",null).getJSONArray("suggestions");runOnUiThread(()->{if(destroyed||!suggestions.dialog.isShowing())return;suggestions.body.removeAllViews();int count=0;for(int n=0;n<list.length();n++){org.json.JSONObject item=list.optJSONObject(n);if(item==null)continue;String tag=item.optString("name");if(tag.isEmpty()||e.tags.contains(tag))continue;count++;suggestions.body.addView(ui.item("plus",tag,item.optString("reason"),()->{suggestions.close();mutate(s,()->s.addTag(Collections.singletonList(e.id),tag),"标签已添加",()->refreshDetail(detail,s,e));}),ui.top(8));}if(count==0)suggestions.body.addView(ui.text("暂时没有新的建议标签。",13,MUTED,false));});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)state.setText(message(ex));});}});
    }
    private void refreshDetail(MobileUi.Sheet sheet,LibraryStore s,LibraryStore.Entry e){io.execute(()->{try{LibraryStore.Entry fresh=s.detail(e);runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing())detailBody(sheet,s,fresh);});}catch(Exception ex){runOnUiThread(()->toast(message(ex)));}});}
    private void detailLine(LinearLayout parent,String label,String value){LinearLayout row=ui.col();row.addView(ui.text(label,11,MUTED,false));TextView t=ui.text(value==null?"—":value,13,INK,false);t.setTextIsSelectable(true);row.addView(t,ui.top(5));parent.addView(row,ui.top(14));}
    private void openPreview(LibraryStore.Entry e){
        if(!e.available){toast("来源暂不可用");return;}
        LibraryStore s=store();if(s==null)return;
        Intent intent=new Intent(this,PreviewActivity.class);
        intent.putExtra("uri",s.remote?s.client.base+"/api/resources/"+RemoteClient.enc(e.id)+"/file":e.id);
        intent.putExtra("name",e.name).putExtra("mime",e.mime);
        if(s.remote)intent.putExtra("server",s.client.base).putExtra("resource",e.id);
        ArrayList<String> ids=new ArrayList<>(),names=new ArrayList<>(),mimes=new ArrayList<>();
        for(LibraryStore.Entry item:entries)if(item.available&&(item.kind.equals("image")||item.kind.equals("video"))){ids.add(item.id);names.add(item.name);mimes.add(item.mime);}
        if(ids.contains(e.id)){intent.putStringArrayListExtra("browse_ids",ids);intent.putStringArrayListExtra("browse_names",names);intent.putStringArrayListExtra("browse_mimes",mimes);intent.putExtra("browse_index",ids.indexOf(e.id));}
        startActivity(intent);
    }
    private void entryMenu(LibraryStore.Entry e){MobileUi.Sheet s=ui.sheet(e.name,typeName(e.kind));s.body.addView(ui.item("file","文件详情",null,()->{s.close();showDetail(e);}));s.body.addView(ui.item("tag","添加标签",null,()->{s.close();tagPrompt(Collections.singletonList(e.id),null);}));s.body.addView(ui.item("folder","移动到文件夹",null,()->{s.close();moveSheet(Collections.singletonList(e.id));}));s.body.addView(ui.item("check","选择文件",null,()->{s.close();selecting=true;toggleSelection(e.id);}));s.show();}
    private void tagPrompt(List<String> ids,Runnable after){if(ids.isEmpty())return;LibraryStore s=store();prompt("添加标签",ids.size()==1?"例如：旅行、工作":"给 "+ids.size()+" 个文件添加标签","",name->{String clean=LibraryStore.clean(name,s.remote?40:64);mutate(s,()->s.addTag(ids,clean),"标签已添加",after);});}
    private void moveSheet(List<String> ids){if(ids.isEmpty())return;LibraryStore st=store();MobileUi.Sheet s=ui.sheet("移动到文件夹",ids.size()+" 个文件 · 原文件位置不变");s.body.addView(ui.item("download","未归组",null,()->{s.close();mutate(st,()->st.move(ids,"unfiled"),"文件已移至未归组",null);}));for(LibraryStore.Group g:meta.folders)s.body.addView(ui.item("folder",g.name,g.count+" 个文件",()->{s.close();mutate(st,()->st.move(ids,g.id),"文件已移动",null);}),ui.top(6));if(meta.folders.isEmpty())s.body.addView(ui.text("还没有自定义文件夹，可以在“文件夹”页创建。",12,MUTED,false),ui.top(20));s.show();}
    private void download(LibraryStore s,LibraryStore.Entry e){try{DownloadManager.Request r=new DownloadManager.Request(Uri.parse(s.client.base+"/api/resources/"+RemoteClient.enc(e.id)+"/file?download=1"));r.addRequestHeader("Cookie",s.client.cookie());r.setTitle(e.name);r.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);r.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS,e.name.replace('/','_').replace('\\','_'));long id=((DownloadManager)getSystemService(DOWNLOAD_SERVICE)).enqueue(r);JSONArray saved=transfers();JSONObject item=new JSONObject();item.put("id",id);item.put("name",e.name);saved.put(item);prefs.edit().putString("transfers",saved.toString()).apply();toast("已加入传输列表");if(tab==3)renderTransfers();}catch(Exception ex){toast("下载失败："+message(ex));}}
    private JSONArray transfers(){try{return new JSONArray(prefs.getString("transfers","[]"));}catch(Exception ignored){return new JSONArray();}}
    private void renderTransfers(){
        if(tab!=3)return;int scrollPosition=content.getChildCount()>0&&content.getChildAt(0) instanceof ScrollView?content.getChildAt(0).getScrollY():0;content.removeAllViews();ScrollView scroll=new ScrollView(this);scroll.setFillViewport(true);LinearLayout body=ui.col();body.setPadding(ui.d(20),0,ui.d(20),ui.d(24));scroll.addView(body);content.addView(scroll,new LinearLayout.LayoutParams(-1,-1));scroll.post(()->scroll.scrollTo(0,scrollPosition));
        body.addView(ui.text("下载到手机的文件",13,MUTED,false),ui.top(6));JSONArray saved=transfers();
        if(saved.length()==0){LinearLayout empty=ui.card();empty.addView(ui.icon("download",36,GREEN));empty.addView(ui.text("暂无传输任务",18,INK,true),ui.top(16));empty.addView(ui.text("从家中资源库打开文件详情，即可下载原文件。",13,MUTED,false),ui.top(8));body.addView(empty,ui.top(20));return;}
        DownloadManager manager=(DownloadManager)getSystemService(DOWNLOAD_SERVICE);
        for(int i=saved.length()-1;i>=0;i--){JSONObject item=saved.optJSONObject(i);if(item==null)continue;long id=item.optLong("id",-1);if(id<0)continue;String name=item.optString("name","下载文件");
            int status=0,reason=0;long downloaded=0,totalBytes=-1;try(android.database.Cursor cursor=manager.query(new DownloadManager.Query().setFilterById(id))){if(cursor!=null&&cursor.moveToFirst()){status=cursor.getInt(cursor.getColumnIndexOrThrow(DownloadManager.COLUMN_STATUS));reason=cursor.getInt(cursor.getColumnIndexOrThrow(DownloadManager.COLUMN_REASON));downloaded=cursor.getLong(cursor.getColumnIndexOrThrow(DownloadManager.COLUMN_BYTES_DOWNLOADED_SO_FAR));totalBytes=cursor.getLong(cursor.getColumnIndexOrThrow(DownloadManager.COLUMN_TOTAL_SIZE_BYTES));}}catch(Exception ignored){}
            LinearLayout card=ui.card();TextView title=ui.text(name,15,INK,true);title.setMaxLines(2);card.addView(title);String state=status==DownloadManager.STATUS_SUCCESSFUL?"已完成":status==DownloadManager.STATUS_FAILED?"下载失败（"+reason+"）":status==DownloadManager.STATUS_RUNNING?"下载中":status==DownloadManager.STATUS_PAUSED?"已暂停":status==DownloadManager.STATUS_PENDING?"等待下载":"任务已不存在";
            card.addView(ui.text(state+" · "+size(downloaded)+(totalBytes>0?" / "+size(totalBytes):""),12,MUTED,false),ui.top(7));
            if(status==DownloadManager.STATUS_RUNNING||status==DownloadManager.STATUS_PAUSED||status==DownloadManager.STATUS_PENDING){ProgressBar bar=new ProgressBar(this,null,android.R.attr.progressBarStyleHorizontal);bar.setMax(100);bar.setIndeterminate(totalBytes<=0);if(totalBytes>0)bar.setProgress((int)Math.min(100,downloaded*100/totalBytes));card.addView(bar,ui.top(12));}
            final int stateValue=status;final long downloadId=id;if(status==DownloadManager.STATUS_SUCCESSFUL)card.addView(ui.button("打开文件",false,()->{Uri uri=manager.getUriForDownloadedFile(downloadId);if(uri==null){toast("文件已移走或不可用");return;}Intent open=new Intent(Intent.ACTION_VIEW);String mime=manager.getMimeTypeForDownloadedFile(downloadId);open.setDataAndType(uri,mime==null?"*/*":mime);open.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);try{startActivity(Intent.createChooser(open,"打开下载文件"));}catch(Exception ex){toast("没有可用的应用打开此文件");}}),ui.top(12));
            else if(stateValue==DownloadManager.STATUS_RUNNING||stateValue==DownloadManager.STATUS_PAUSED||stateValue==DownloadManager.STATUS_PENDING)card.addView(ui.button("取消下载",false,()->{manager.remove(downloadId);removeTransfer(downloadId);renderTransfers();}),ui.top(12));
            else card.addView(ui.button("移出列表",false,()->{removeTransfer(downloadId);renderTransfers();}),ui.top(12));body.addView(card,ui.top(12));
        }
    }
    private void removeTransfer(long id){JSONArray old=transfers(),next=new JSONArray();for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item!=null&&item.optLong("id")!=id)next.put(item);}prefs.edit().putString("transfers",next.toString()).apply();}
    private interface Work{void run()throws Exception;}
    private void mutate(LibraryStore target,Work work,String success,Runnable after){if(target==null||busy||scanning){toast("请等待当前操作完成");return;}busy=true;renderBatch();io.execute(()->{try{work.run();runOnUiThread(()->{busy=false;if(destroyed)return;toast(success);if(target==store()){selected.clear();selecting=false;if(after!=null)after.run();reload(false,true);}});}catch(Exception e){runOnUiThread(()->{busy=false;if(destroyed)return;toast(message(e));renderBatch();});}});}
    private interface TextAction{void run(String s);}
    private void prompt(String title,String hint,String initial,TextAction action){MobileUi.Sheet s=ui.sheet(title,null);EditText input=ui.input(hint,initial);s.body.addView(input);TextView error=ui.text("",12,RED,false);s.body.addView(error,ui.top(8));s.footer.addView(ui.button("保存",true,()->{try{String value=LibraryStore.clean(input.getText().toString(),64);action.run(value);s.close();hideKeyboard();}catch(Exception e){error.setText(message(e));}}));s.show();input.requestFocus();}
    private void confirm(String title,String description,Runnable action){MobileUi.Sheet s=ui.sheet(title,description);s.body.addView(ui.icon("shield",42,GREEN));s.footer.addView(ui.button("确认",true,()->{s.close();action.run();}));s.footer.addView(ui.button("取消",false,s::close),ui.top(8));s.show();}
    private String folderName(String id){if(id==null||id.isEmpty()||id.equals("unfiled"))return "未归组";if(id.equals("all"))return "全部文件";LibraryStore.Group g=find(meta.folders,id);return g==null?"文件夹":g.name;}
    private static LibraryStore.Group find(List<LibraryStore.Group> groups,String id){for(LibraryStore.Group g:groups)if(g.id.equals(id))return g;return null;}
    private String collectionSummary(LocalFilters value){ArrayList<String> parts=new ArrayList<>();if(!value.query.isEmpty())parts.add("“"+value.query+"”");parts.addAll(value.tags);for(String key:value.attributes)parts.add(LocalLibrary.attributeLabel(key));if(value.unorganized)parts.add("待整理");return parts.isEmpty()?"全部文件 · 动态更新":TextUtils.join(" · ",parts);}
    private String shortPath(String name){String v=name.replace('\\','/');int i=v.lastIndexOf('/');return i>=0&&i<v.length()-1?v.substring(i+1):v;}
    static String size(long bytes){if(bytes<0)return "大小未知";if(bytes<1024)return bytes+" B";if(bytes<1024*1024)return String.format(Locale.ROOT,"%.0f KB",bytes/1024.0);if(bytes<1024L*1024*1024)return String.format(Locale.ROOT,"%.1f MB",bytes/(1024.0*1024));return String.format(Locale.ROOT,"%.1f GB",bytes/(1024.0*1024*1024));}
    private static String date(long n){return n<=0?"时间未知":new SimpleDateFormat("MM月dd日",Locale.CHINA).format(new Date(n));}
    private static String dateTime(long n){return n<=0?"未知":new SimpleDateFormat("yyyy年MM月dd日 HH:mm",Locale.CHINA).format(new Date(n));}
    private static String typeName(String kind){switch(kind){case "image":return "图片";case "video":return "视频";case "audio":return "音频";case "document":return "文档";default:return "文件";}}
    private static int typeColor(String k){switch(k){case "image":return 0xff5b8565;case "video":return 0xff8a735b;case "audio":return 0xff8675a0;case "document":return 0xff64849e;default:return MUTED;}}
    private static int typeBg(String k){switch(k){case "image":return 0xffe8efe3;case "video":return 0xfff1eade;case "audio":return 0xffede8f2;case "document":return 0xffe7eef4;default:return 0xffedf0ea;}}
    private static String message(Exception e){if(e instanceof java.net.SocketTimeoutException)return "连接超时，请检查网络后重试。";if(e instanceof java.net.UnknownHostException)return "找不到服务端，请检查地址。";String s=e.getMessage();return s==null||s.isEmpty()?"操作失败，请稍后重试":s;}
    private void toast(String s){if(!destroyed)Toast.makeText(this,s,Toast.LENGTH_SHORT).show();}
    private void hideKeyboard(){View v=getCurrentFocus();if(v!=null){((InputMethodManager)getSystemService(INPUT_METHOD_SERVICE)).hideSoftInputFromWindow(v.getWindowToken(),0);v.clearFocus();}}
    private void handleBack(){if(storageOnly){finish();return;}if(selecting){selected.clear();selecting=false;updateResults();}else if(tab!=0)navigate(0);else if(!f().folderId.equals("all"))openFolder("all");else finish();}
    // API 33+ uses the platform back dispatcher registered in onCreate; this is the older-device fallback.
    @android.annotation.SuppressLint("GestureBackNavigation")
    @Override public void onBackPressed(){handleBack();}
    @Override protected void onSaveInstanceState(Bundle b){b.putBoolean("home",home);b.putInt("tab",tab);b.putString("local",localFilters.serialize());b.putString("hub",hubFilters.serialize());b.putString("collection",activeCollection);super.onSaveInstanceState(b);}
    @Override protected void onDestroy(){destroyed=true;generation++;handler.removeCallbacksAndMessages(null);thumbs.close();io.execute(db::close);io.shutdown();super.onDestroy();}
}
