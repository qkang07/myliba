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
import java.io.IOException;
import java.util.*;
import java.util.concurrent.*;
import java.util.concurrent.atomic.AtomicBoolean;
import org.json.JSONArray;
import org.json.JSONObject;

import static com.myliba.android.MobileUi.*;

/** A single mobile library experience, backed by either SAF/SQLite or the Hub API. */
public final class MainActivity extends Activity {
    private static final int PICK_TREE=41,EXPORT_LOG=42,PICK_STORAGE_TREE=43;
    private final ExecutorService io=Executors.newSingleThreadExecutor();
    private final Handler handler=new Handler(Looper.getMainLooper());
    private final Set<String> selected=new LinkedHashSet<>();
    private final List<LibraryStore.Entry> entries=new ArrayList<>();
    private MobileUi ui; private LocalLibrary db; private LibraryStore localStore,hubStore;
    private ThumbnailLoader thumbs; private SharedPreferences prefs;
    private LocalFilters localFilters=new LocalFilters(),hubFilters=new LocalFilters();
    private LibraryStore.Meta meta=new LibraryStore.Meta();
    private LinearLayout shell,header,content,navigation,batchDock,tagChoicesContainer;
    private ListView files;private ResourceAdapter adapter;private FrameLayout resultArea;
    private TextView resultLabel,footerLabel;private EditText search;
    private TextView nodeTitle,nodeStatus,nodeCounts,nodeCapacity,nodeAccess,nodePeers,nodeConfigure,nodeStop,resourceNodeStatus;
    private boolean displayedNodeRunning;
    private boolean settingsMetadataLoading;
    private boolean home,grid,loading,selecting,busy,scanning,destroyed,needsLogin,storageOnly,multipleTagFilters;
    private TextView networkSpeedStatus;
    private TextView thumbnailTaskStatus;
    private Switch thumbnailTaskSwitch;
    private volatile NativeTransferJob activeNativeTransfer;
    private long trafficSampleRx=-1,trafficSampleTx=-1,trafficSampleAt;
    private int tab,generation,total,renderedTab=-1; private String error="",scanMessage="",activeCollection;
    private final Runnable searchTask=()->reload(false,false);
    private final Runnable hubRetry=()->{if(!destroyed&&home&&!loading&&hubStore!=null)reload(false,true);};
    private final Runnable transferRefresh=new Runnable(){@Override public void run(){if(!destroyed&&tab==3){renderTransfers();handler.postDelayed(this,1000);}}};
    private final Runnable nodeRefresh=new Runnable(){@Override public void run(){if(!destroyed&&storageOnly){if(displayedNodeRunning!=CloudStorageService.running)render();else updateStorageOnlyStatus();handler.postDelayed(this,3000);}}};
    private final Runnable resourceNodeRefresh=new Runnable(){@Override public void run(){if(!destroyed&&!storageOnly&&tab==2){if(resourceNodeStatus!=null)resourceNodeStatus.setText(ResourceNodeService.status+"\n"+LocalIndexPublisher.status);if(thumbnailTaskStatus!=null)thumbnailTaskStatus.setText(getSharedPreferences(LocalThumbnailTaskService.PREFS,MODE_PRIVATE).getString("status",LocalThumbnailTaskService.status));if(thumbnailTaskSwitch!=null&&thumbnailTaskSwitch.isChecked()!=LocalThumbnailTaskService.isEnabled())setThumbnailTaskChecked(LocalThumbnailTaskService.isEnabled());handler.postDelayed(this,3000);}}};
    private final Runnable networkSpeedRefresh=new Runnable(){@Override public void run(){if(destroyed||tab!=2)return;long now=SystemClock.elapsedRealtime(),rx=android.net.TrafficStats.getUidRxBytes(getApplicationInfo().uid),tx=android.net.TrafficStats.getUidTxBytes(getApplicationInfo().uid);if(networkSpeedStatus!=null&&rx>=0&&tx>=0){String rate="当前网速：采样中";if(trafficSampleAt>0&&now>trafficSampleAt){long bps=((rx-trafficSampleRx)+(tx-trafficSampleTx))*1000/(now-trafficSampleAt);rate="当前网速："+size(Math.max(0,bps))+"/秒";}networkSpeedStatus.setText(rate);}trafficSampleRx=rx;trafficSampleTx=tx;trafficSampleAt=now;handler.postDelayed(this,1000);}};

    @Override public void onCreate(Bundle saved){
        super.onCreate(saved);ui=new MobileUi(this);db=new LocalLibrary(this);localStore=new LibraryStore(db,null);
        prefs=getPreferences(MODE_PRIVATE);thumbs=new ThumbnailLoader(this);grid=prefs.getBoolean("view_grid",false);multipleTagFilters=prefs.getBoolean("multiple_tag_filters",false);
        LocalIndexJobService.schedule(this,true);
        storageOnly=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE).getBoolean("storage_only",false);
        String server=prefs.getString("server_url","");if(!server.isEmpty())hubStore=new LibraryStore(db,new RemoteClient(server,prefs,this));
        if(saved!=null){home=saved.getBoolean("home");tab=saved.getInt("tab");try{localFilters=LocalFilters.parse(saved.getString("local","{}"));hubFilters=LocalFilters.parse(saved.getString("hub","{}"));}catch(Exception ignored){}activeCollection=saved.getString("collection");}
        getWindow().setStatusBarColor(BG);getWindow().setNavigationBarColor(PAPER);
        getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR|View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR);
        shell=ui.col();shell.setBackgroundColor(BG);setContentView(shell);
        header=ui.col();shell.addView(header);content=ui.col();shell.addView(content,new LinearLayout.LayoutParams(-1,0,1));batchDock=ui.col();shell.addView(batchDock);navigation=ui.row();shell.addView(navigation);
        if(Build.VERSION.SDK_INT>=30){getWindow().setDecorFitsSystemWindows(false);shell.setOnApplyWindowInsetsListener((v,insets)->{
            android.graphics.Insets bars=insets.getInsets(WindowInsets.Type.systemBars()),ime=insets.getInsets(WindowInsets.Type.ime());
            shell.setPadding(bars.left,bars.top,bars.right,Math.max(bars.bottom,ime.bottom));navigation.setVisibility(storageOnly||ime.bottom>bars.bottom?View.GONE:View.VISIBLE);return insets;});}
        if(Build.VERSION.SDK_INT>=33)getOnBackInvokedDispatcher().registerOnBackInvokedCallback(android.window.OnBackInvokedDispatcher.PRIORITY_DEFAULT,this::handleBack);
        render();if(storageOnly)handler.postDelayed(nodeRefresh,3000);else if(tab==3)handler.postDelayed(transferRefresh,1000);else if(tab==2){handler.postDelayed(resourceNodeRefresh,3000);handler.postDelayed(networkSpeedRefresh,1000);loadSettingsMetadata();}else reload(false,true);
    }
    private LibraryStore store(){return home?hubStore:localStore;}
    private LocalFilters f(){return home?hubFilters:localFilters;}
    private void setFilters(LocalFilters next){if(home)hubFilters=next;else localFilters=next;}
    private void switchSource(boolean remote){
        if(home==remote)return;hideKeyboard();handler.removeCallbacks(searchTask);handler.removeCallbacks(hubRetry);generation++;home=remote;meta=new LibraryStore.Meta();entries.clear();total=0;error="";needsLogin=false;selected.clear();selecting=false;activeCollection=null;
        render();reload(false,true);
    }
    private void navigate(int page){if(tab==page)return;hideKeyboard();handler.removeCallbacks(searchTask);handler.removeCallbacks(transferRefresh);handler.removeCallbacks(resourceNodeRefresh);handler.removeCallbacks(networkSpeedRefresh);generation++;loading=false;tab=page;selected.clear();selecting=false;error="";render();if(tab==3)handler.postDelayed(transferRefresh,1000);else if(tab==2){handler.postDelayed(resourceNodeRefresh,3000);handler.postDelayed(networkSpeedRefresh,1000);loadSettingsMetadata();}else reload(false,true);}
    private void loadSettingsMetadata(){if(settingsMetadataLoading||localStore==null)return;settingsMetadataLoading=true;int token=generation;io.execute(()->{try{LibraryStore.Meta local=localStore.metadata(localFilters.copy());runOnUiThread(()->{settingsMetadataLoading=false;if(!destroyed&&tab==2&&token==generation){meta=local;render();}});}catch(Exception ex){runOnUiThread(()->settingsMetadataLoading=false);}});}
    private void render(){
        int restoreScroll=0;if(renderedTab==tab&&content.getChildCount()>0&&content.getChildAt(0) instanceof ScrollView)restoreScroll=content.getChildAt(0).getScrollY();
        header.removeAllViews();content.removeAllViews();files=null;adapter=null;search=null;resultArea=null;resultLabel=null;tagChoicesContainer=null;
        if(storageOnly){renderStorageOnly();return;}
        navigation.setVisibility(View.VISIBLE);batchDock.setVisibility(View.VISIBLE);
        header.setPadding(ui.d(20),ui.d(10),ui.d(18),ui.d(9));
        TextView brand=ui.text("MYLIBA",10,GREEN,true);brand.setLetterSpacing(.18f);header.addView(brand);
        LinearLayout title=ui.row();String[] titles={"资源库","文件夹","设置","传输列表"};title.addView(ui.text(titles[tab],28,INK,true),ui.weight());
        if(tab!=2){LinearLayout source=ui.row();ui.pad(source,10,7);source.addView(ui.icon(home?"server":"phone",17,GREEN));TextView label=ui.text(home?selectedNodeName():localNodeName(),12,GREEN,true);label.setPadding(ui.d(6),0,ui.d(4),0);label.setSingleLine();label.setEllipsize(TextUtils.TruncateAt.END);label.setMaxWidth(ui.d(96));source.addView(label);source.addView(ui.icon("down",15,GREEN));ui.touch(source,SOFT,20,this::sourceSheet);source.setContentDescription("切换资源库，当前"+(home?selectedNodeName():"本节点 "+localNodeName()));title.addView(source);}
        header.addView(title,ui.top(7));
        if(tab==0)renderLibrary();else if(tab==1)renderFolders();else if(tab==2)renderSources();else renderTransfers();
        renderNav();renderBatch();renderedTab=tab;if(restoreScroll>0&&content.getChildCount()>0&&content.getChildAt(0) instanceof ScrollView){int y=restoreScroll;content.getChildAt(0).post(()->{if(!destroyed&&renderedTab==tab&&content.getChildCount()>0&&content.getChildAt(0) instanceof ScrollView)content.getChildAt(0).scrollTo(0,y);});}
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
        String access=!settings.getString("url","").isEmpty()?"公网直连":!settings.getString("relay","").isEmpty()?"存储节点中继"+(CloudStorageService.relayConnected?"已连接":"待连接"):"Hub 协助节点直连";
        nodeAccess=ui.text("访问："+access,12,MUTED,false);card.addView(nodeAccess,ui.top(6));
		nodePeers=ui.text("其他节点：等待 Hub 目录",12,MUTED,false);card.addView(nodePeers,ui.top(9));
        nodeCounts=ui.text("",13,INK,false);card.addView(nodeCounts,ui.top(15));
        nodeCapacity=ui.text("",12,MUTED,false);card.addView(nodeCapacity,ui.top(5));
        body.addView(card);
        nodeConfigure=ui.button("",true,this::cloudStorageSheet);body.addView(nodeConfigure,ui.top(18));
        renderStorageDirectories(body);
        nodeStop=ui.button("停止存储节点",false,()->{stopService(new Intent(this,CloudStorageService.class));handler.postDelayed(this::render,350);});body.addView(nodeStop,ui.top(9));
        body.addView(ui.button("切换到完整客户端",false,this::leaveStorageMode),ui.top(22));
        body.addView(ui.button("查看诊断日志",false,this::diagnosticsSheet),ui.top(9));
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
        nodeAccess.setText("访问："+(!settings.getString("url","").isEmpty()?"公网直连":!settings.getString("relay","").isEmpty()?"存储节点中继"+(CloudStorageService.relayConnected?"已连接":"待连接"):"Hub 协助节点直连"));
		if(nodePeers!=null){List<String> links=new ArrayList<>();for(Map.Entry<String,JSONObject> entry:CloudStorageService.peerLinks.entrySet())links.add(entry.getKey().substring(0,Math.min(12,entry.getKey().length()))+" · "+formatPeerLink(entry.getValue()));Collections.sort(links);nodePeers.setText(links.isEmpty()?"其他节点：尚未发现":"其他节点：\n"+String.join("\n",links));}
        nodeConfigure.setText(active?"配置并重启":"配置并启动");nodeStop.setVisibility(active?View.VISIBLE:View.GONE);
    }
    private void renderNav(){
        navigation.removeAllViews();navigation.setBackgroundColor(PAPER);navigation.setPadding(ui.d(14),ui.d(8),ui.d(14),ui.d(8));
        if(tab==3){navigation.setVisibility(View.GONE);return;}
        String[] icons={"library","folder","sources"},labels={"资源","文件夹","设置"};
        for(int i=0;i<3;i++){final int index=i;LinearLayout item=ui.col();item.setGravity(Gravity.CENTER);ui.pad(item,0,4);
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
        if(store()!=null&&store().remote)types.add(ui.chip("统一检索",false,this::cloudFilesSheet));
        controls.addView(ui.rail(types),ui.top(7));
        tagChoicesContainer=ui.col();controls.addView(tagChoicesContainer);renderTagChoices();
        ArrayList<View> applied=new ArrayList<>();
        if(!f().folderId.equals("all"))applied.add(ui.chip(folderName(f().folderId)+"  ×",true,()->{f().folderId="all";activeCollection=null;refreshScreen(true);}));
        if(activeCollection!=null){LibraryStore.Group g=find(meta.collections,activeCollection);if(g!=null)applied.add(ui.chip(g.name+"  ×",true,()->{activeCollection=null;refreshScreen(false);}));}
        if(f().unorganized)applied.add(ui.chip("未添加标签  ×",true,()->{f().unorganized=false;refreshScreen(false);}));
        for(String key:f().attributes)if(!key.startsWith("type:"))applied.add(ui.chip(LocalLibrary.attributeLabel(key)+"  ×",true,()->{f().attributes.remove(key);refreshScreen(false);}));
        if(!f().rootUri.isEmpty())applied.add(ui.chip("已选来源  ×",true,()->{f().rootUri="";refreshScreen(false);}));
        if(!applied.isEmpty())controls.addView(ui.rail(applied),ui.top(5));
        LinearLayout tools=ui.row();resultLabel=ui.text("最近文件",13,INK,true);tools.addView(resultLabel,ui.weight());tools.addView(ui.iconButton("sort","排序",this::sortSheet));tools.addView(ui.iconButton(grid?"list":"grid",grid?"切换列表":"切换网格",()->{grid=!grid;prefs.edit().putBoolean("view_grid",grid).apply();render();}));tools.addView(ui.iconButton("more","资源操作",this::libraryMenu));controls.addView(tools,ui.top(3));
        resultArea=new FrameLayout(this);content.addView(resultArea,new LinearLayout.LayoutParams(-1,0,1));
        files=new ListView(this);files.setDivider(null);files.setSelector(android.R.color.transparent);files.setClipToPadding(false);files.setPadding(ui.d(20),0,ui.d(20),ui.d(12));files.setVerticalScrollBarEnabled(false);
        footerLabel=ui.text("加载更多",13,GREEN,true);footerLabel.setGravity(Gravity.CENTER);ui.pad(footerLabel,12,18);footerLabel.setOnClickListener(v->reload(true,false));files.addFooterView(footerLabel);
        adapter=new ResourceAdapter();files.setAdapter(adapter);resultArea.addView(files,new FrameLayout.LayoutParams(-1,-1));updateResults();
    }
    private void renderTagChoices(){
        if(tagChoicesContainer==null)return;tagChoicesContainer.removeAllViews();
        ArrayList<View> choices=new ArrayList<>();
        for(LibraryStore.Facet tag:meta.tags){String key=tag.key;choices.add(ui.chip(tag.name,f().tags.contains(key),()->{LocalFilters next=f().copy();toggleFilterSelection(next.tags,key,null);applyFilters(next,false);}));}
        choices.add(ui.chip(multipleTagFilters?"多选：开":"多选：关",multipleTagFilters,()->setMultipleTagFilters(!multipleTagFilters)));
        if(!choices.isEmpty())tagChoicesContainer.addView(ui.rail(choices),ui.top(5));
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
            preview=new FrameLayout(MainActivity.this);preview.setClipToOutline(true);preview.setBackground(ui.shape(SOFT,tile?16:13,0));image=new ImageView(MainActivity.this);image.setScaleType(ImageView.ScaleType.CENTER_CROP);preview.addView(image,new FrameLayout.LayoutParams(-1,-1));placeholder=ui.icon("file",tile?38:27,GREEN);preview.addView(placeholder,new FrameLayout.LayoutParams(ui.d(tile?38:27),ui.d(tile?38:27),Gravity.CENTER));root.addView(preview,ui.lp(tile?-1:58,tile?102:58));
            copy=ui.col();if(tile)ui.pad(copy,10,9);else copy.setPadding(ui.d(12),0,0,0);
            name=ui.text("",tile?13:14,INK,true);name.setMaxLines(tile?2:1);name.setEllipsize(TextUtils.TruncateAt.END);copy.addView(name);
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
            runOnUiThread(()->{if(destroyed||token!=generation||s!=store())return;handler.removeCallbacks(hubRetry);if(home&&s.remote&&s.client.selectedNodeId.equals(ResourceNodeService.ensureID(this))){switchSource(false);return;}loading=false;needsLogin=false;if(next!=null){meta=next;renderTagChoices();}if(p!=null){if(!append)entries.clear();entries.addAll(p.items);total=p.total;}if(tab==0)updateResults();else render();});
        }catch(Exception e){runOnUiThread(()->{if(destroyed||token!=generation||s!=store())return;loading=false;error=message(e);needsLogin=e instanceof RemoteClient.Failure&&((RemoteClient.Failure)e).status==401;if(needsLogin)entries.clear();if(home){handler.removeCallbacks(hubRetry);handler.postDelayed(hubRetry,needsLogin?30000:10000);}if(tab==0)updateResults();else render();});}});
    }
    private void refreshScreen(boolean metadata){hideKeyboard();handler.removeCallbacks(searchTask);selected.clear();selecting=false;entries.clear();render();reload(false,metadata);}
    private void applyFilters(LocalFilters value,boolean metadata){setFilters(value);refreshScreen(metadata);}
    private int filterCount(){return f().tags.size()+f().attributes.size()+(f().rootUri.isEmpty()?0:1)+(f().unorganized?1:0);}
    private void sourceSheet(){
        MobileUi.Sheet s=ui.sheet("选择资源库","这台手机是一个资源节点。连接 Hub 后，可切换到其他设备的资源库。");
        populateSourceSheet(s);
        s.show();
        LibraryStore current=hubStore;
        if(current!=null)io.execute(()->{try{current.client.json("GET","/api/session",null);runOnUiThread(()->{if(!destroyed&&s.dialog.isShowing()&&current==hubStore)populateSourceSheet(s);});}catch(Exception ex){runOnUiThread(()->{if(!destroyed&&s.dialog.isShowing()&&current==hubStore)s.body.addView(ui.text("Hub 节点列表暂不可用："+message(ex),11,RED,false),ui.top(10));});}});
    }
    private void populateSourceSheet(MobileUi.Sheet s){
        s.body.removeAllViews();
        s.body.addView(ui.item("server",hubStore==null?"连接 Hub":"Hub 连接设置",hubStore==null?"连接你的 Myliba Hub":Uri.parse(hubStore.client.base).getHost(),()->{s.close();connectSheet();}));
        s.body.addView(ui.item("phone","本节点 · "+localNodeName(),!home?"当前资源库 · 可离线使用":"读取这台手机已授权的文件夹",()->{s.close();switchSource(false);}),ui.top(10));
        if(hubStore!=null){JSONArray nodes=hubStore.client.nodes;for(int i=0;i<nodes.length();i++){JSONObject node=nodes.optJSONObject(i);if(node==null)continue;String id=node.optString("id");
            if(id.equals(ResourceNodeService.ensureID(this)))continue;
            s.body.addView(ui.item("android".equals(node.optString("kind"))?"phone":"server",node.optString("name",id),node.optBoolean("online")?(id.equals(hubStore.client.selectedNodeId)?"当前节点 · 在线":"在线 · 点击切换"):"离线",()->{
                s.close();LibraryStore current=hubStore;io.execute(()->{try{current.client.selectNode(id);runOnUiThread(()->{if(destroyed||hubStore!=current)return;home=true;tab=0;hubFilters=new LocalFilters();activeCollection=null;generation++;entries.clear();meta=new LibraryStore.Meta();render();reload(false,true);});}catch(Exception ex){runOnUiThread(()->toast("切换节点失败："+message(ex)));}});
            }),ui.top(8));
            if(hubStore.client.owner&&!node.optBoolean("online"))s.body.addView(ui.button("移除离线节点 · "+node.optString("name",id),false,()->{
                s.close();confirm("移除离线节点？","仅从 Hub 列表移除；节点重新连接后会再次出现。",()->{
                    LibraryStore current=hubStore;io.execute(()->{try{current.client.json("DELETE","/api/hub/nodes/"+RemoteClient.enc(id),null);current.client.json("GET","/api/session",null);runOnUiThread(()->{if(destroyed||hubStore!=current)return;toast("已移除离线节点");render();if(home)reload(false,true);});}catch(Exception ex){runOnUiThread(()->toast("移除节点失败："+message(ex)));}});
                });
            }),ui.top(4));}}
    }
    private String localNodeName(){return getSharedPreferences(ResourceNodeService.PREFS,MODE_PRIVATE).getString("name",Build.MODEL);}
    private String selectedNodeName(){if(hubStore==null)return "Hub";if(hubStore.client.selectedNodeId.equals(ResourceNodeService.ensureID(this)))return "本节点";JSONArray nodes=hubStore.client.nodes;for(int i=0;i<nodes.length();i++){JSONObject node=nodes.optJSONObject(i);if(node!=null&&hubStore.client.selectedNodeId.equals(node.optString("id")))return node.optString("name","Hub");}return "Hub";}
    private void libraryMenu(){MobileUi.Sheet s=ui.sheet("资源操作",null);s.body.addView(ui.item("check","选择文件","长按任意文件也可开始选择",()->{s.close();selecting=true;updateResults();}));s.body.addView(ui.item("spark","保存为常用查询","保存当前搜索和筛选条件",()->{s.close();saveCollection(null);}));if(home)s.body.addView(ui.item("refresh","后台预处理","查看缩略图与文件指纹任务",()->{s.close();preprocessSheet();}));else {s.body.addView(ui.item("image","生成本机缩略图","预先为已索引的图片、视频和 PDF 生成封面",()->{s.close();generateLocalThumbnails();}));s.body.addView(ui.item("trash","本机回收站","恢复离线回收的逻辑文件，原件仍在原位置",()->{s.close();localTrashSheet();}),ui.top(8));}s.body.addView(ui.item("refresh","刷新资源库","重新读取索引中的文件",()->{s.close();reload(false,true);}));s.show();}
    private void localTrashSheet(){MobileUi.Sheet sheet=ui.sheet("本机回收站","逻辑回收只隐藏文件；原件和分类仍保留。");sheet.body.addView(ui.text("正在读取…",12,MUTED,false));sheet.show();io.execute(()->{try{List<String[]> files=db.trashedFiles();runOnUiThread(()->{if(destroyed||!sheet.dialog.isShowing())return;sheet.body.removeAllViews();if(files.isEmpty())sheet.body.addView(ui.text("回收站为空",12,MUTED,false));for(String[] file:files){sheet.body.addView(ui.item("file",file[1],"恢复到资源库",()->mutate(localStore,()->db.restoreFile(file[0]),"已恢复文件",()->{sheet.close();reload(false,true);})),ui.top(7));sheet.body.addView(ui.button("永久删除条目 · "+file[1],false,()->confirm("永久删除资源库条目？","此操作不能恢复；原件与云块仍等待安全回收。",()->mutate(localStore,()->db.deleteFileRecord(file[0]),"资源库条目已删除",()->{sheet.close();reload(false,true);}))),ui.top(4));}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing()){sheet.body.removeAllViews();sheet.body.addView(ui.text("读取失败："+message(ex),12,RED,false));}});}});}
    private void preprocessSheet(){LibraryStore s=store();if(s==null||!s.remote)return;MobileUi.Sheet sheet=ui.sheet("后台预处理","家中资源库的缩略图和文件指纹任务");TextView loading=ui.text("正在读取任务…",13,MUTED,false);sheet.body.addView(loading);sheet.show();loadPreprocess(sheet,s);}
    private void loadPreprocess(MobileUi.Sheet sheet,LibraryStore s){io.execute(()->{try{JSONObject queue=s.client.json("GET","/api/preprocess",null);runOnUiThread(()->{if(destroyed||!sheet.dialog.isShowing())return;sheet.body.removeAllViews();sheet.footer.removeAllViews();JSONObject counts=queue.optJSONObject("counts");if(counts!=null)sheet.body.addView(ui.text("排队 "+counts.optInt("pending")+" · 处理中 "+counts.optInt("running")+" · 失败 "+counts.optInt("failed")+" · 完成 "+counts.optInt("done"),13,INK,true));if(counts!=null&&counts.optInt("failed")>0)sheet.body.addView(ui.button("重试全部失败任务",false,()->{sheet.close();mutate(s,()->s.client.json("POST","/api/preprocess/retry-failed",null),"已重新排队",()->preprocessSheet());}),ui.top(14));JSONArray jobs=queue.optJSONArray("jobs");if(jobs==null||jobs.length()==0)sheet.body.addView(ui.text("当前没有任务",13,MUTED,false),ui.top(16));else for(int i=0;i<jobs.length();i++){JSONObject job=jobs.optJSONObject(i);if(job==null)continue;String status=job.optString("status");String label=status.equals("pending")?"排队中":status.equals("running")?"处理中":status.equals("failed")?"失败":"已完成";String op=job.optString("operation").equals("thumbnail")?"缩略图":"文件指纹";String detail=op+" · "+label+(job.optInt("priority")>0&&status.equals("pending")?" · 当前列表优先":"");if(!job.optString("error").isEmpty())detail+=" · "+job.optString("error");String id=job.optString("resourceId");LinearLayout row=ui.item(op.equals("缩略图")?"image":"file",job.optString("name"),detail,()->{if(!status.equals("failed"))return;sheet.close();mutate(s,()->s.client.json("POST","/api/preprocess/"+RemoteClient.enc(id)+"/retry",null),"任务已重新排队",()->preprocessSheet());});sheet.body.addView(row,ui.top(8));}sheet.footer.addView(ui.button("刷新状态",false,()->loadPreprocess(sheet,s)),ui.top(8));});}catch(Exception ex){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing()){sheet.body.removeAllViews();sheet.body.addView(ui.text("读取失败："+message(ex),13,RED,false));}});}});}
    private void toggleSelection(String id){selecting=true;if(!selected.add(id))selected.remove(id);updateResults();}
    private void renderBatch(){
        if(batchDock==null)return;batchDock.removeAllViews();batchDock.setVisibility(selecting?View.VISIBLE:View.GONE);if(!selecting)return;
        batchDock.setBackgroundColor(PAPER);batchDock.setPadding(ui.d(16),ui.d(9),ui.d(16),ui.d(10));
        LinearLayout top=ui.row();top.addView(ui.iconButton("x","退出选择",()->{selecting=false;selected.clear();updateResults();}));top.addView(ui.text("已选 "+selected.size()+" 项",14,INK,true),ui.weight());TextView all=ui.chip("全选结果",false,this::selectAll);top.addView(all);batchDock.addView(top);
        LinearLayout actions=ui.row();TextView tags=ui.button("编辑标签",true,this::batchTags);TextView move=ui.button("归入文件夹",false,()->moveSheet(new ArrayList<>(selected)));tags.setEnabled(!selected.isEmpty()&&!busy);move.setEnabled(!selected.isEmpty()&&!busy);tags.setAlpha(selected.isEmpty()?.45f:1);move.setAlpha(selected.isEmpty()?.45f:1);actions.addView(tags,new LinearLayout.LayoutParams(0,ui.d(46),1));LinearLayout.LayoutParams mp=new LinearLayout.LayoutParams(0,ui.d(46),1);mp.leftMargin=ui.d(10);actions.addView(move,mp);batchDock.addView(actions,ui.top(6));
        if(!home){TextView trash=ui.button("移入回收站",false,()->{List<String> ids=new ArrayList<>(selected);confirm("回收选中的文件？","将隐藏这 "+ids.size()+" 个文件；原件和分类保留，可从本机回收站恢复。",()->mutate(localStore,()->db.trashUris(ids),"已移入回收站",null));});trash.setEnabled(!selected.isEmpty()&&!busy);trash.setAlpha(selected.isEmpty()?.45f:1);batchDock.addView(trash,ui.top(6));}
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
        MobileUi.Sheet sheet=ui.sheet("筛选资源","默认单选；开启多选后，多个手动标签需同时满足。");LocalFilters draft=f().copy();
        fillFilter(sheet,draft);sheet.show();
    }
    private void cloudFilesSheet(){
        LibraryStore current=store();if(current==null||!current.remote)return;
        MobileUi.Sheet sheet=ui.sheet("统一检索","搜索设备已同步的索引和云层文件；分类修改随来源设备同步。受保护文件请回到来源设备查看。");
        EditText searchFiles=new EditText(this);searchFiles.setSingleLine(true);searchFiles.setTextSize(14);searchFiles.setHint("搜索文件名、文件夹或标签");sheet.body.addView(searchFiles,ui.top(8));
        final boolean[] allSources={true},trashed={false};final int[] sortIndex={0};final String[] sortKeys={"name_asc","name_desc","modified_desc","modified_asc","ingested_desc","ingested_asc"};final String[] sortLabels={"名称顺序","名称倒序","最近修改","最早修改","最近入库","最早入库"};
        final String[] folderId={""};final LinkedHashSet<String> tags=new LinkedHashSet<>();final JSONObject[] facets={null};
        final String[] deviceId={""};final JSONArray[] sources={null};final JSONObject[] sourceTimes={null},sourceFrontiers={null};final JSONArray[] offlineSources={null},unindexedSources={null};
        final int[] typeIndex={0},groupIndex={0},copyIndex={0},readIndex={0};
        final String[] typeKeys={"","image","video","audio","document","other"},typeLabels={"全部","图片","视频","音频","文档","其他"};
        final String[] stateKeys={"","true","false"},groupLabels={"全部","已归组","未归组"},copyLabels={"全部","有云层副本","仅设备来源"},readLabels={"全部","可读","暂不可读"};
        Runnable[] reset={null};
        TextView scopeButton=ui.button("范围：全部来源",false,()->{allSources[0]=!allSources[0];reset[0].run();});sheet.body.addView(scopeButton,ui.top(8));
        TextView lifecycleButton=ui.button("资源库",false,()->{trashed[0]=!trashed[0];reset[0].run();});sheet.body.addView(lifecycleButton,ui.top(5));
        TextView deviceButton=ui.button("来源设备：全部",false,()->{MobileUi.Sheet picker=ui.sheet("来源设备","显示已有文件位置或应用回执的节点");picker.body.addView(ui.item("cloud","所有设备",null,()->{deviceId[0]="";picker.close();reset[0].run();}));JSONArray options=sources[0];if(options!=null)for(int i=0;i<options.length();i++){String node=options.optString(i);if(node.isEmpty())continue;picker.body.addView(ui.item("cloud",node,sourceCoverageLabel(node,sourceTimes[0],sourceFrontiers[0],offlineSources[0],unindexedSources[0]),()->{deviceId[0]=node;allSources[0]=true;picker.close();reset[0].run();}),ui.top(5));}picker.show();});sheet.body.addView(deviceButton,ui.top(5));
        TextView sortButton=ui.button("排序：名称顺序",false,()->{sortIndex[0]=(sortIndex[0]+1)%sortKeys.length;reset[0].run();});sheet.body.addView(sortButton,ui.top(5));
        TextView typeButton=ui.button("类型：全部",false,()->{typeIndex[0]=(typeIndex[0]+1)%typeKeys.length;reset[0].run();});sheet.body.addView(typeButton,ui.top(5));
        TextView groupButton=ui.button("归组：全部",false,()->{groupIndex[0]=(groupIndex[0]+1)%stateKeys.length;reset[0].run();});sheet.body.addView(groupButton,ui.top(5));
        TextView copyButton=ui.button("云层副本：全部",false,()->{copyIndex[0]=(copyIndex[0]+1)%stateKeys.length;reset[0].run();});sheet.body.addView(copyButton,ui.top(5));
        TextView readButton=ui.button("当前可读：全部",false,()->{readIndex[0]=(readIndex[0]+1)%stateKeys.length;reset[0].run();});sheet.body.addView(readButton,ui.top(5));
        TextView folderButton=ui.button("文件夹：全部",false,()->{MobileUi.Sheet picker=ui.sheet("筛选文件夹","按去重后的逻辑文件统计");picker.body.addView(ui.item("folder","全部文件夹","清除文件夹筛选",()->{folderId[0]="";picker.close();reset[0].run();}));JSONArray options=facets[0]==null?null:facets[0].optJSONArray("folders");if(options!=null)for(int i=0;i<options.length();i++){JSONObject option=options.optJSONObject(i);if(option==null)continue;picker.body.addView(ui.item("folder",option.optString("name"),option.optInt("count")+" 个文件",()->{folderId[0]=option.optString("id");picker.close();reset[0].run();}),ui.top(5));}picker.show();});sheet.body.addView(folderButton,ui.top(5));
        TextView tagButton=ui.button("标签：全部",false,()->{MobileUi.Sheet picker=ui.sheet("筛选标签","选择多个标签时，文件必须同时包含这些标签");picker.body.addView(ui.item("tag","清除标签筛选","显示全部标签",()->{tags.clear();picker.close();reset[0].run();}));JSONArray options=facets[0]==null?null:facets[0].optJSONArray("tags");if(options!=null)for(int i=0;i<options.length();i++){JSONObject option=options.optJSONObject(i);if(option==null)continue;String name=option.optString("name");picker.body.addView(ui.item("tag",name+(tags.contains(name)?" ✓":""),option.optInt("count")+" 个文件",()->{if(!tags.add(name))tags.remove(name);picker.close();reset[0].run();}),ui.top(5));}picker.show();});sheet.body.addView(tagButton,ui.top(5));
        sheet.body.addView(ui.button("常用查询",false,()->{MobileUi.Sheet picker=ui.sheet("常用查询","只保存条件，应用时重新检索当前索引");TextView loadingQueries=ui.text("正在读取…",12,MUTED,false);picker.body.addView(loadingQueries);picker.show();io.execute(()->{try{JSONArray saved=current.client.array("/api/library/queries");runOnUiThread(()->{if(destroyed||!picker.dialog.isShowing())return;picker.body.removeAllViews();if(saved.length()==0)picker.body.addView(ui.text("还没有保存的查询",12,MUTED,false));for(int i=0;i<saved.length();i++){JSONObject item=saved.optJSONObject(i);if(item==null)continue;JSONObject filters=item.optJSONObject("filters");if(filters==null)continue;String savedId=item.optString("id"),savedName=item.optString("name");LinearLayout row=ui.row();row.addView(ui.item("search",savedName,null,()->{String savedScope=filters.optString("scope","all");allSources[0]=!"cloud".equals(savedScope);trashed[0]="trashed".equals(filters.optString("lifecycle"));deviceId[0]=savedScope.startsWith("device:")?savedScope.substring(7):"";folderId[0]=filters.optString("folderId");tags.clear();JSONArray savedTags=filters.optJSONArray("tags");if(savedTags!=null)for(int j=0;j<savedTags.length();j++)tags.add(savedTags.optString(j));sortIndex[0]=Math.max(0,Arrays.asList(sortKeys).indexOf(filters.optString("sort")));typeIndex[0]=Math.max(0,Arrays.asList(typeKeys).indexOf(filters.optString("type")));groupIndex[0]=filters.has("grouped")?(filters.optBoolean("grouped")?1:2):0;copyIndex[0]=filters.has("cloudCopy")?(filters.optBoolean("cloudCopy")?1:2):0;readIndex[0]=filters.has("readable")?(filters.optBoolean("readable")?1:2):0;picker.close();searchFiles.setText(filters.optString("query"));reset[0].run();}),new LinearLayout.LayoutParams(0,-2,1));row.addView(ui.button("删除",false,()->{io.execute(()->{try{current.client.json("DELETE","/api/library/queries/"+RemoteClient.enc(savedId),null);runOnUiThread(()->{if(!destroyed){picker.close();toast("已删除常用查询");}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("删除失败："+message(ex));});}});}),ui.top(2));picker.body.addView(row,ui.top(6));}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed&&picker.dialog.isShowing())loadingQueries.setText("读取失败："+message(ex));});}});}),ui.top(8));
        sheet.body.addView(ui.button("保存当前条件",false,()->prompt("保存常用查询","查询名称","",name->{
            String scope=allSources[0]?(deviceId[0].isEmpty()?"all":"device:"+deviceId[0]):"cloud";
            io.execute(()->{try{
                JSONObject filters=new JSONObject().put("scope",scope).put("lifecycle",trashed[0]?"trashed":"active").put("query",searchFiles.getText().toString().trim()).put("folderId",folderId[0]).put("tags",new JSONArray(tags)).put("sort",sortKeys[sortIndex[0]]);
                if(typeIndex[0]>0)filters.put("type",typeKeys[typeIndex[0]]);
                if(groupIndex[0]>0)filters.put("grouped",groupIndex[0]==1);
                if(copyIndex[0]>0)filters.put("cloudCopy",copyIndex[0]==1);
                if(readIndex[0]>0)filters.put("readable",readIndex[0]==1);
                current.client.json("POST","/api/library/queries",new JSONObject().put("name",name).put("filters",filters));
                runOnUiThread(()->{if(!destroyed)toast("已保存常用查询");});
            }catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("保存失败："+message(ex));});}});
        })),ui.top(5));
        sheet.body.addView(ui.button("旧文件夹身份",false,()->{
            MobileUi.Sheet picker=ui.sheet("旧文件夹身份","同名旧文件夹按来源保持独立，请明确选择要合并的实体");
            TextView status=ui.text("正在读取…",12,MUTED,false);picker.body.addView(status);picker.show();
            io.execute(()->{try{
                if(!current.client.selectedNodeId.isEmpty())current.client.selectNode(current.client.selectedNodeId);
                JSONArray groups=current.client.json("GET","/api/library/legacy-folders",null).optJSONArray("items");
                runOnUiThread(()->{if(destroyed||!picker.dialog.isShowing())return;picker.body.removeAllViews();
                    if(groups==null||groups.length()==0){picker.body.addView(ui.text("没有待合并的旧文件夹",12,MUTED,false));return;}
                    HashSet<String> selected=new HashSet<>();HashMap<String,String> names=new HashMap<>();
                    TextView chosen=ui.text("已选 0 项",12,MUTED,false);picker.body.addView(chosen);
                    for(int i=0;i<groups.length();i++){JSONObject group=groups.optJSONObject(i);if(group==null)continue;
                        String id=group.optString("id"),name=group.optString("name");names.put(id,name);
                        picker.body.addView(ui.item("folder",name+" · "+group.optInt("count")+" 个文件",group.optJSONArray("sources")+" · "+id,()->{if(!selected.add(id))selected.remove(id);chosen.setText("已选 "+selected.size()+" 项："+android.text.TextUtils.join("、",selected));}),ui.top(5));
                    }
                    picker.body.addView(ui.button("合并选中项",false,()->{if(selected.size()<2){toast("请至少选择两个旧文件夹");return;}
                        String proposed=names.get(selected.iterator().next());prompt("合并旧文件夹","目标文件夹名称",proposed==null?"":proposed,name->{if(name.trim().isEmpty())return;
                            io.execute(()->{try{current.client.json("POST","/api/library/legacy-folders/merge",new JSONObject().put("folderIds",new JSONArray(selected)).put("name",name.trim()));
                                runOnUiThread(()->{if(!destroyed){picker.close();toast("已合并旧文件夹");reset[0].run();}});
                            }catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("合并失败："+message(ex));});}});
                        });}),ui.top(8));
                });
            }catch(Exception ex){runOnUiThread(()->{if(!destroyed&&picker.dialog.isShowing())status.setText("读取失败："+message(ex));});}});
        }),ui.top(5));
        TextView state=ui.text("正在检索…",12,MUTED,false);sheet.body.addView(state,ui.top(10));
        LinearLayout rows=ui.col();sheet.body.addView(rows,ui.top(6));
        final String queryNode=current.client.selectedNodeId;
        final String[] cursor={""};final int[] request={0};final boolean[] loading={false};final TextView[] more={null};
        Runnable[] load={null};
        load[0]=()->{
            if(loading[0]||destroyed||!sheet.dialog.isShowing())return;
            loading[0]=true;int version=request[0];String next=cursor[0],query=searchFiles.getText().toString().trim();String wantedFolder=folderId[0],sort=sortKeys[sortIndex[0]],scope=allSources[0]?(deviceId[0].isEmpty()?"all":"device:"+deviceId[0]):"cloud";String wantedType=typeKeys[typeIndex[0]],wantedGroup=stateKeys[groupIndex[0]],wantedCopy=stateKeys[copyIndex[0]],wantedRead=stateKeys[readIndex[0]];List<String> wantedTags=new ArrayList<>(tags);state.setText("正在检索…");
            io.execute(()->{try{
                String path="/api/library/search?scope="+Uri.encode(scope)+"&lifecycle="+(trashed[0]?"trashed":"active")+"&limit=50&query="+Uri.encode(query)+"&sort="+sort+(wantedFolder.isEmpty()?"":"&folderId="+Uri.encode(wantedFolder))+(wantedType.isEmpty()?"":"&type="+wantedType)+(wantedGroup.isEmpty()?"":"&grouped="+wantedGroup)+(wantedCopy.isEmpty()?"":"&cloudCopy="+wantedCopy)+(wantedRead.isEmpty()?"":"&readable="+wantedRead);
                for(String tag:wantedTags)path+="&tag="+Uri.encode(tag);
                if(!next.isEmpty())path+="&cursor="+Uri.encode(next);
                LibrarySearchCache cache=new LibrarySearchCache(this);LibraryOfflineIndex offlineIndex=new LibraryOfflineIndex(this);JSONObject result;boolean usedCache=false;
                try{if(next.startsWith("offline:"))throw new IOException("offline index page");if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);result=current.client.json("GET",path,null);if(next.isEmpty()){cache.save(current.client.base,queryNode,path,result);io.execute(()->{try{if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);offlineIndex.sync(current.client,queryNode);}catch(Exception ex){AppLog.info("Offline index refresh: "+ex.getMessage());}finally{offlineIndex.close();}});}}
                catch(Exception network){if(!(network instanceof IOException)||network instanceof RemoteClient.Failure&&!next.startsWith("offline:"))throw network;result=offlineIndex.search(current.client.base,queryNode,path);if(result==null&&next.isEmpty())result=cache.load(current.client.base,queryNode,path);if(result==null)throw network;usedCache=true;}
                final JSONObject response=result;final boolean cached=usedCache;JSONArray files=response.optJSONArray("items");String following=response.optString("nextCursor","");int total=response.optInt("total",0);JSONObject coverage=response.optJSONObject("coverage");
                runOnUiThread(()->{
                    if(destroyed||!sheet.dialog.isShowing()||version!=request[0])return;
                    loading[0]=false;cursor[0]=following;
                    if(next.isEmpty()){facets[0]=response.optJSONObject("facets");sources[0]=coverage==null?null:coverage.optJSONArray("knownNodes");if(sources[0]==null&&coverage!=null)sources[0]=coverage.optJSONArray("sources");sourceTimes[0]=coverage==null?null:coverage.optJSONObject("sourceLastCatalogAt");sourceFrontiers[0]=coverage==null?null:coverage.optJSONObject("sourceFrontiers");offlineSources[0]=coverage==null?null:coverage.optJSONArray("offlineSources");unindexedSources[0]=coverage==null?null:coverage.optJSONArray("unindexedSources");}
                    if(more[0]!=null){rows.removeView(more[0]);more[0]=null;}
                    if(files!=null)for(int i=0;i<files.length();i++){
                        JSONObject file=files.optJSONObject(i);if(file==null)continue;
                        String name=file.optString("name"),folder=file.optString("folderName");JSONArray fileTags=file.optJSONArray("tags");ArrayList<String> labels=new ArrayList<>();
                        if(fileTags!=null)for(int j=0;j<fileTags.length();j++)labels.add(fileTags.optString(j));
                        String classification=(folder.isEmpty()?"未归组":folder)+(labels.isEmpty()?"":" · "+TextUtils.join("、",labels));boolean localOnly=file.optBoolean("localOnly");
                        String copies=localOnly?"仅设备索引":file.optInt("replicas",file.optInt("minReplicas"))+" 份副本 · "+file.optString("availableReplicas","—")+" 份可用";if(file.optInt("revisionCount",1)>1)copies+=" · "+file.optInt("revisionCount")+" 个版本";
                        if(cached){rows.addView(ui.item("cloud",name,size(file.optLong("originalSize",file.optLong("size")))+" · 离线元数据 · 可读性未知\n"+classification,()->toast("离线索引仅供查找，连接来源后再打开")),ui.top(7));continue;}
                        boolean chooseVersion=file.optInt("revisionCount",1)>1&&!file.optBoolean("currentRevision",false);
                        LinearLayout row=ui.row();row.addView(ui.item("cloud",name,size(file.optLong("originalSize",file.optLong("size")))+" · "+copies+"\n"+classification,()->{if(chooseVersion)libraryVersionsSheet(current,queryNode,file);else if(localOnly)openIndexedSource(current,file);else previewCloudFile(current,file);}),new LinearLayout.LayoutParams(0,-2,1));
                        if(!localOnly&&!trashed[0])row.addView(ui.button(chooseVersion?"选择版本":"下载",false,()->{if(chooseVersion)libraryVersionsSheet(current,queryNode,file);else enqueueCloudDownload(current,file);}),ui.top(2));
                        if(localOnly&&chooseVersion&&!trashed[0])row.addView(ui.button("选择版本",false,()->libraryVersionsSheet(current,queryNode,file)),ui.top(2));
                        if(file.optInt("revisionCount",1)>1&&!chooseVersion)row.addView(ui.button("版本",false,()->libraryVersionsSheet(current,queryNode,file)),ui.top(2));
                        row.addView(ui.button("分类",false,()->cloudClassificationSheet(current,queryNode,file,reset[0])),ui.top(2));
                        row.addView(ui.button(trashed[0]?"恢复":"回收",false,()->{
                            String fileId=file.optString("fileId",file.optString("id"));String nextState=trashed[0]?"active":"trashed";
                            io.execute(()->{try{current.client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/lifecycle",new JSONObject().put("state",nextState));runOnUiThread(()->{if(!destroyed){toast("trashed".equals(nextState)?"已移入回收站":"已恢复");reset[0].run();}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("更新失败："+message(ex));});}});
                        }),ui.top(2));
                        if(trashed[0])row.addView(ui.button("永久删除条目",false,()->confirm("永久删除资源库条目？","此操作不能恢复；原件与云块仍等待安全回收。",()->{String fileId=file.optString("fileId",file.optString("id"));io.execute(()->{try{if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);current.client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/lifecycle",new JSONObject().put("state","deleted"));runOnUiThread(()->{if(!destroyed){toast("资源库条目已删除");reset[0].run();}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("删除失败："+message(ex));});}});})),ui.top(2));
                        rows.addView(row,ui.top(7));
                    }
                    state.setText((cached?"离线缓存 · "+response.optString("cachedAt")+" · 仅供识别位置 · ":"")+"找到 "+total+" 个文件"+(coverage==null||coverage.optString("lastCatalogAt").isEmpty()?"":" · 目录更新 "+coverage.optString("lastCatalogAt")));
                    if(!following.isEmpty()){more[0]=ui.button("加载更多",false,()->load[0].run());rows.addView(more[0],ui.top(8));}
                    else if(total==0)rows.addView(ui.text("没有匹配的已同步文件。",12,MUTED,false),ui.top(8));
                });
            }catch(Exception ex){runOnUiThread(()->{if(destroyed||!sheet.dialog.isShowing()||version!=request[0])return;loading[0]=false;if(!next.isEmpty()&&ex instanceof RemoteClient.Failure&&((RemoteClient.Failure)ex).status==409){reset[0].run();return;}state.setText("检索失败："+message(ex));});}});
        };
        reset[0]=()->{request[0]++;cursor[0]="";loading[0]=false;more[0]=null;rows.removeAllViews();scopeButton.setText(allSources[0]?"范围：全部来源":"范围：仅云层");lifecycleButton.setText(trashed[0]?"回收站":"资源库");deviceButton.setText("来源设备："+(deviceId[0].isEmpty()?"全部":deviceId[0]));sortButton.setText("排序："+sortLabels[sortIndex[0]]);typeButton.setText("类型："+typeLabels[typeIndex[0]]);groupButton.setText("归组："+groupLabels[groupIndex[0]]);copyButton.setText("云层副本："+copyLabels[copyIndex[0]]);readButton.setText("当前可读："+readLabels[readIndex[0]]);folderButton.setText("文件夹："+(folderId[0].isEmpty()?"全部":folderId[0]));tagButton.setText("标签："+(tags.isEmpty()?"全部":TextUtils.join("、",tags)));load[0].run();};
        searchFiles.addTextChangedListener(new TextWatcher(){public void beforeTextChanged(CharSequence s,int st,int c,int a){}public void afterTextChanged(Editable e){}public void onTextChanged(CharSequence s,int st,int before,int count){handler.removeCallbacks(reset[0]);handler.postDelayed(reset[0],250);}});
        sheet.show();reset[0].run();
    }
    private void cloudClassificationSheet(LibraryStore current,String queryNode,JSONObject file,Runnable refresh){
        String fileId=file.optString("fileId",file.optString("id"));MobileUi.Sheet sheet=ui.sheet("整理文件",file.optString("name"));
        sheet.body.addView(ui.item("cloud","修改显示名称",file.optString("name"),()->prompt("修改显示名称","不会修改来源设备上的文件名",file.optString("name"),name->{if(name.trim().isEmpty())return;sheet.close();editCloudClassification(current,queryNode,fileId,"PUT","name",new JSONObjectValue("fileName",name.trim()),refresh);})),ui.top(7));
        sheet.body.addView(ui.item("folder","修改文件夹",file.optString("folderName","未归组"),()->prompt("修改文件夹","留空为未归组",file.optString("folderName"),name->{sheet.close();editCloudClassification(current,queryNode,fileId,"PUT","folder",new JSONObjectValue("folderName",name),refresh);})),ui.top(7));
        sheet.body.addView(ui.item("tag","添加标签",null,()->prompt("添加标签","标签名称","",name->{if(name.trim().isEmpty())return;sheet.close();editCloudClassification(current,queryNode,fileId,"POST","tags",new JSONObjectValue("name",name.trim()),refresh);})),ui.top(7));
        JSONArray tags=file.optJSONArray("tags");if(tags!=null)for(int i=0;i<tags.length();i++){String tag=tags.optString(i);if(tag.isEmpty())continue;sheet.body.addView(ui.item("tag","移除 "+tag,null,()->{sheet.close();editCloudClassification(current,queryNode,fileId,"DELETE","tags/"+RemoteClient.enc(tag),null,refresh);}),ui.top(7));}
        sheet.show();
    }
    private static String sourceCoverageLabel(String node,JSONObject times,JSONObject frontiers,JSONArray offline,JSONArray unindexed){
        StringBuilder label=new StringBuilder(times==null?"目录时间未知":times.optString(node,"目录时间未知"));
        if(unindexed!=null)for(int i=0;i<unindexed.length();i++)if(node.equals(unindexed.optString(i))){label.append(" · 尚无已索引位置");break;}
        if(offline!=null)for(int i=0;i<offline.length();i++)if(node.equals(offline.optString(i))){label.append(" · 当前离线");break;}
        JSONObject applied=frontiers==null?null:frontiers.optJSONObject(node);
        if(applied!=null)label.append(" · 已回执 ").append(applied.length()).append(" 位作者前沿");
        return label.toString();
    }
    private static final class JSONObjectValue {final String key,value;JSONObjectValue(String key,String value){this.key=key;this.value=value;}}
    private void editCloudClassification(LibraryStore current,String queryNode,String fileId,String method,String action,JSONObjectValue field,Runnable refresh){
        io.execute(()->{try{if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);JSONObject body=field==null?null:new JSONObject().put(field.key,field.value);current.client.json(method,"/api/library/files/"+RemoteClient.enc(fileId)+"/"+action,body);runOnUiThread(()->{if(!destroyed){toast("已保存分类，等待同步");refresh.run();}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("分类修改失败："+message(ex));});}});
    }
    private void openIndexedSource(LibraryStore current,JSONObject file){
        JSONArray locations=file.optJSONArray("locations");if(locations==null){toast("没有可读取的来源位置");return;}
        String revision=file.optString("revisionId"),queryNode=current.client.selectedNodeId;
        io.execute(()->{
            String failure="没有可读取的来源位置";
            for(int i=0;i<locations.length();i++){
                JSONObject location=locations.optJSONObject(i);if(location==null)continue;String node=location.optString("nodeId"),ref=location.optString("resourceRef");if(node.isEmpty()||ref.isEmpty())continue;
                try{
                    current.client.selectNode(node);
                    JSONObject resource=current.client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);
                    if(!resource.optBoolean("available",true)){failure="来源暂不可用";continue;}
                    if(!revision.isEmpty()&&!revision.equals(resource.optString("versionId"))){failure="来源版本与检索结果不一致";continue;}
                    LibraryStore.Entry entry=LibraryStore.entry(resource);runOnUiThread(()->{if(!destroyed)openPreview(entry);});return;
                }catch(Exception ex){failure=message(ex);}
            }
            try{if(!queryNode.isEmpty())current.client.selectNode(queryNode);}catch(Exception ignored){}
            String reason=failure;runOnUiThread(()->{if(!destroyed)toast("无法打开来源："+reason);});
        });
    }
    private void enqueueCloudDownload(LibraryStore current,JSONObject file){String id=file.optString("id"),name=file.optString("name");Intent transfer=new Intent(this,ResourceTransferService.class);transfer.putExtra("base",current.client.base).putExtra("resource",id).putExtra("fileId",file.optString("fileId",id)).putExtra("name",name).putExtra("total",file.optLong("originalSize",file.optLong("size"))).putExtra("node",current.client.selectedNodeId).putExtra("cloud",true).putExtra("revision",file.optString("revisionId")).putExtra("locations",file.optJSONArray("locations")==null?"[]":file.optJSONArray("locations").toString());try{startForegroundService(transfer);toast("已加入传输列表");}catch(Exception ex){toast("下载失败："+message(ex));}}
    private void libraryVersionsSheet(LibraryStore current,String queryNode,JSONObject file){
        String fileId=file.optString("fileId",file.optString("id"));MobileUi.Sheet sheet=ui.sheet("文件版本",file.optString("name"));sheet.body.addView(ui.text("正在读取版本…",12,MUTED,false));sheet.show();
        io.execute(()->{try{
            if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);
            JSONObject detail=current.client.json("GET","/api/library/files/"+RemoteClient.enc(fileId),null);JSONArray revisions=detail.optJSONArray("revisions");
            runOnUiThread(()->{if(destroyed||!sheet.dialog.isShowing())return;sheet.body.removeAllViews();if(revisions==null||revisions.length()==0){sheet.body.addView(ui.text("没有可用版本",12,MUTED,false));return;}
                for(int i=0;i<revisions.length();i++){JSONObject revision=revisions.optJSONObject(i);if(revision==null)continue;String id=revision.optString("revisionId");sheet.body.addView(ui.item("file",revision.optString("modified","未知时间"),"版本 "+id+" · 原件 "+size(revision.optLong("originalSize"))+(id.equals(detail.optString("currentRevisionId"))?" · 当前版":""),()->{try{JSONObject chosen=new JSONObject(file.toString());chosen.put("revisionId",id);chosen.put("originalSize",revision.optLong("originalSize"));chosen.put("fileId",fileId);chosen.put("locations",revision.optJSONArray("locations"));sheet.close();enqueueCloudDownload(current,chosen);}catch(Exception ex){toast("无法读取版本："+message(ex));}}),ui.top(8));if(!id.equals(detail.optString("currentRevisionId")))sheet.body.addView(ui.button("设为当前版",false,()->io.execute(()->{try{current.client.json("PUT","/api/library/files/"+RemoteClient.enc(fileId)+"/current-revision",new JSONObject().put("revisionId",id));runOnUiThread(()->{if(!destroyed){sheet.close();toast("已选择当前版本，等待同步");}});}catch(Exception ex){runOnUiThread(()->{if(!destroyed)toast("版本选择失败："+message(ex));});}})),ui.top(3));}
            });
        }catch(Exception ex){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing()){sheet.body.removeAllViews();sheet.body.addView(ui.text("读取版本失败："+message(ex),12,RED,false));}});}});
    }
    private void previewCloudFile(LibraryStore current,JSONObject file){
        io.execute(()->{
            LibraryStore.Entry local=null;String source=file.optString("sourceId"),queryNode=current.client.selectedNodeId,revision=file.optString("revisionId");
            String lowerName=file.optString("name").toLowerCase(Locale.ROOT);
            if(lowerName.matches(".*\\.(jpg|jpeg|png|gif|webp|heic|arw|dng)$")&&previewImageCandidates(current,file,queryNode))return;
            if(lowerName.matches(".*\\.(mp4|m4v|mov|webm|mkv)$")&&previewVideoCandidates(current,file,queryNode))return;
            if(!source.isEmpty())try{JSONObject value=current.client.json("GET","/api/resources/"+RemoteClient.enc(source),null);long localSize=value.optLong("contentSize",value.optLong("size")),cloudSize=file.optLong("originalSize",file.optLong("size"));if(value.optBoolean("available")&&value.optString("modified").equals(file.optString("modified"))&&localSize==cloudSize&&revision.equals(value.optString("versionId")))local=LibraryStore.entry(value);}catch(Exception ignored){}
            if(local==null){JSONArray locations=file.optJSONArray("locations");if(locations!=null)for(int i=0;i<locations.length();i++){JSONObject location=locations.optJSONObject(i);if(location==null)continue;String node=location.optString("nodeId"),ref=location.optString("resourceRef");if(node.isEmpty()||ref.isEmpty())continue;try{current.client.selectNode(node);JSONObject value=current.client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);if(value.optBoolean("available")&&revision.equals(value.optString("versionId"))){local=LibraryStore.entry(value);break;}}catch(Exception ignored){}}}
            if(local==null)try{if(!queryNode.isEmpty())current.client.selectNode(queryNode);}catch(Exception ignored){}
            LibraryStore.Entry localEntry=local;
            runOnUiThread(()->{if(destroyed)return;if(localEntry!=null){openPreview(localEntry);return;}openCloudPreview(current,file);});
        });
    }
    private boolean previewImageCandidates(LibraryStore current,JSONObject file,String queryNode){
        try{
            String fileId=file.optString("fileId",file.optString("id")),revision=file.optString("revisionId");
            JSONObject resolved=current.client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/resolve",new JSONObject().put("revisionId",revision).put("purpose","preview"));
            JSONArray candidates=resolved.optJSONArray("candidates");if(candidates==null)return false;
            for(int i=0;i<candidates.length();i++){
                JSONObject candidate=candidates.optJSONObject(i);if(candidate==null)continue;
                String path=candidate.optString("readPath"),node=candidate.optString("nodeId"),ref=candidate.optString("resourceRef");
                if(!path.startsWith("/api/")||path.contains(".."))continue;
                try{
                    if("device".equals(candidate.optString("kind"))){if(node.isEmpty()||ref.isEmpty())continue;current.client.selectNode(node);
                        JSONObject source=current.client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);
                        if(!source.optBoolean("available")||!revision.equals(source.optString("versionId")))continue;
                    }else if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);
                    byte[] content=current.client.bytes("GET",path,null,32<<20,candidate.optString("readTicket"));
                    java.io.File temp=java.io.File.createTempFile("library-preview-",".bin",getCacheDir());
                    try(java.io.FileOutputStream out=new java.io.FileOutputStream(temp)){out.write(content);}
                    String mime="thumbnail".equals(candidate.optString("assetType"))?"image/jpeg":android.webkit.MimeTypeMap.getSingleton().getMimeTypeFromExtension(file.optString("name").replaceFirst("^.*\\.","").toLowerCase(Locale.ROOT));
                    if(mime==null)mime="image/jpeg";
                    Intent intent=new Intent(this,PreviewActivity.class);intent.putExtra("uri",Uri.fromFile(temp).toString()).putExtra("name",file.optString("name")).putExtra("mime",mime).putExtra("size",content.length).putExtra("delete_preview_path",temp.getAbsolutePath());
                    runOnUiThread(()->{if(!destroyed)startActivity(intent);else temp.delete();});return true;
                }catch(Exception ignored){/* Try the next version-pinned candidate. */}
            }
        }catch(Exception ignored){/* Fall back to the existing source/original path. */}
        finally{try{if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);}catch(Exception ignored){}}
        return false;
    }
    private boolean previewVideoCandidates(LibraryStore current,JSONObject file,String queryNode){
        try{
            String fileId=file.optString("fileId",file.optString("id")),revision=file.optString("revisionId");
            JSONObject resolved=current.client.json("POST","/api/library/files/"+RemoteClient.enc(fileId)+"/resolve",new JSONObject().put("revisionId",revision).put("purpose","preview"));
            JSONArray candidates=resolved.optJSONArray("candidates");if(candidates==null)return false;
            for(int i=0;i<candidates.length();i++){
                JSONObject candidate=candidates.optJSONObject(i);if(candidate==null||!"videoPreview".equals(candidate.optString("assetType")))continue;
                String path=candidate.optString("readPath"),node=candidate.optString("nodeId"),ref=candidate.optString("resourceRef");
                if(!path.startsWith("/api/")||path.contains("..")||node.isEmpty()||ref.isEmpty())continue;
                try{
                    current.client.selectNode(node);
                    JSONObject source=current.client.json("GET","/api/resources/"+RemoteClient.enc(ref),null);
                    if(!source.optBoolean("available")||!revision.equals(source.optString("versionId")))continue;
                    byte[] content=current.client.bytes("GET",path,null,32<<20,candidate.optString("readTicket"));
                    if(content.length==0)continue;
                    java.io.File temp=java.io.File.createTempFile("library-video-preview-",".mp4",getCacheDir());
                    try(java.io.FileOutputStream out=new java.io.FileOutputStream(temp)){out.write(content);}
                    Intent intent=new Intent(this,PreviewActivity.class);intent.putExtra("uri",Uri.fromFile(temp).toString()).putExtra("name",file.optString("name")).putExtra("mime","video/mp4").putExtra("size",content.length).putExtra("delete_preview_path",temp.getAbsolutePath());
                    runOnUiThread(()->{if(!destroyed)startActivity(intent);else temp.delete();});return true;
                }catch(Exception ignored){/* Try the next version-pinned preview candidate. */}
            }
        }catch(Exception ignored){/* Fall back to the existing cloud stream. */}
        finally{try{if(!queryNode.isEmpty()&&!queryNode.equals(current.client.selectedNodeId))current.client.selectNode(queryNode);}catch(Exception ignored){}}
        return false;
    }
    private void openCloudPreview(LibraryStore current,JSONObject file){
        String id=file.optString("id"),name=file.optString("name");int dot=name.lastIndexOf('.');String extension=dot<0?"":name.substring(dot+1).toLowerCase(Locale.ROOT);String mime=android.webkit.MimeTypeMap.getSingleton().getMimeTypeFromExtension(extension);if(name.toLowerCase(Locale.ROOT).endsWith(".arw"))mime="image/x-sony-arw";if(mime==null)mime="application/octet-stream";
        Intent intent=new Intent(this,PreviewActivity.class);intent.putExtra("uri",current.client.base+"/api/cloud/files/"+RemoteClient.enc(id)+"/file").putExtra("name",name).putExtra("mime",mime).putExtra("size",file.optLong("originalSize",file.optLong("size"))).putExtra("server",current.client.base).putExtra("resource",id).putExtra("cloud",true).putExtra("cloud_variants",file.optJSONArray("variants")!=null&&file.optJSONArray("variants").length()>0);
        for(int i=0;i<current.client.nodes.length();i++){JSONObject node=current.client.nodes.optJSONObject(i);if(node!=null&&current.client.selectedNodeId.equals(node.optString("id"))){intent.putExtra("source_node_kind",node.optString("kind"));break;}}
        startActivity(intent);
    }
    private void fillFilter(MobileUi.Sheet sheet,LocalFilters draft){
        sheet.body.removeAllViews();sheet.footer.removeAllViews();
        sheet.body.addView(ui.button(multipleTagFilters?"多选：开":"多选：关",false,()->{setMultipleTagFilters(!multipleTagFilters);if(!multipleTagFilters){keepSingle(draft.tags,null);keepSingle(draft.attributes,"type:");keepSingle(draft.attributes,"size:");keepSingle(draft.attributes,"extension:");}fillFilter(sheet,draft);}),ui.top(8));
        filterSection(sheet.body,"手动标签",meta.tags,draft.tags,null);
        ArrayList<LibraryStore.Facet> types=new ArrayList<>(),sizes=new ArrayList<>(),formats=new ArrayList<>();
        for(LibraryStore.Facet a:meta.attributes){if(a.key.startsWith("type:"))types.add(a);else if(a.key.startsWith("size:"))sizes.add(a);else formats.add(a);}
        filterSection(sheet.body,"文件类型",types,draft.attributes,"type:");filterSection(sheet.body,"文件大小",sizes,draft.attributes,"size:");filterSection(sheet.body,"文件格式",formats,draft.attributes,"extension:");
        section(sheet.body,"来源目录",null,null);MobileUi.Flow roots=ui.new Flow();
        TextView all=ui.chip("全部目录",draft.rootUri.isEmpty(),()->{draft.rootUri="";fillFilter(sheet,draft);});roots.addView(all);
        for(LibraryStore.Group r:meta.roots)roots.addView(ui.chip(shortPath(r.name),draft.rootUri.equals(r.id),()->{draft.rootUri=r.id;fillFilter(sheet,draft);}));sheet.body.addView(roots,ui.top(10));
        LinearLayout untagged=ui.row();untagged.addView(ui.text("只看未添加标签文件",14,INK,true),ui.weight());Switch toggle=new Switch(this);toggle.setChecked(draft.unorganized);toggle.setThumbTintList(android.content.res.ColorStateList.valueOf(GREEN));toggle.setOnCheckedChangeListener((v,checked)->draft.unorganized=checked);untagged.addView(toggle);sheet.body.addView(untagged,ui.top(24));sheet.body.addView(ui.text("还没有添加手动标签的文件",11,MUTED,false),ui.top(5));
        LinearLayout buttons=ui.row();buttons.addView(ui.button("重置",false,()->{String folder=draft.folderId;LocalFilters reset=new LocalFilters();reset.folderId=folder;fillFilter(sheet,reset);}),new LinearLayout.LayoutParams(0,ui.d(48),1));LinearLayout.LayoutParams p=new LinearLayout.LayoutParams(0,ui.d(48),2);p.leftMargin=ui.d(10);buttons.addView(ui.button("应用筛选",true,()->{sheet.close();applyFilters(draft,false);}),p);sheet.footer.addView(buttons,ui.top(10));
    }
    private void filterSection(LinearLayout parent,String title,List<LibraryStore.Facet> choices,List<String> selected,String prefix){
        section(parent,title,null,null);if(choices.isEmpty()){parent.addView(ui.text("当前资源库暂无"+title,12,MUTED,false),ui.top(8));return;}MobileUi.Flow flow=ui.new Flow();
        for(LibraryStore.Facet c:choices){TextView chip=ui.chip(c.name+"  "+c.count,selected.contains(c.key),()->{});chip.setOnClickListener(v->{toggleFilterSelection(selected,c.key,prefix);for(int i=0;i<flow.getChildCount();i++){View child=flow.getChildAt(i);if(!(child instanceof TextView))continue;TextView choice=(TextView)child;String key=choices.get(i).key;boolean active=selected.contains(key);choice.setTextColor(active?GREEN:MUTED);choice.setBackground(ui.shape(active?SOFT:PAPER,20,active?0:LINE));choice.setSelected(active);}});if(!selected.contains(c.key))chip.setBackground(ui.shape(PAPER,20,LINE));flow.addView(chip);}parent.addView(flow,ui.top(10));
    }
    private void toggleFilterSelection(List<String> selected,String key,String prefix){
        if(selected.contains(key)){selected.remove(key);return;}
        if(!multipleTagFilters){if(prefix==null)selected.clear();else selected.removeIf(value->value.startsWith(prefix));}
        selected.add(key);
    }
    private static boolean hasMultipleAttributes(List<String> attributes){
        Set<String> seen=new HashSet<>();for(String key:attributes)if(!seen.add(key.split(":",2)[0]))return true;return false;
    }
    private static void keepSingle(List<String> selected,String prefix){
        boolean found=false;for(int i=selected.size()-1;i>=0;i--){String key=selected.get(i);if(prefix==null||key.startsWith(prefix)){if(found)selected.remove(i);else found=true;}}
    }
    private void setMultipleTagFilters(boolean enabled){
        multipleTagFilters=enabled;prefs.edit().putBoolean("multiple_tag_filters",enabled).apply();
        if(!enabled){LocalFilters next=f().copy();keepSingle(next.tags,null);keepSingle(next.attributes,"type:");keepSingle(next.attributes,"size:");keepSingle(next.attributes,"extension:");applyFilters(next,false);}
        else renderTagChoices();
    }
    private void sortSheet(){MobileUi.Sheet s=ui.sheet("排序方式",null);String[] keys={"modified_desc","modified_asc","name_asc","name_desc","size_desc"},names={"最近修改","最早修改","名称 A–Z","名称 Z–A","文件从大到小"};for(int i=0;i<keys.length;i++){String key=keys[i];s.body.addView(ui.item(key.equals(f().sort)?"check":"sort",names[i],null,()->{s.close();f().sort=key;reload(false,false);}));}s.show();}
    private void renderFolders(){
        LinearLayout body=scrollBody();body.addView(ui.text("按主题收纳，让下次查找更轻松。",13,MUTED,false));
        if(home&&(hubStore==null||needsLogin)){body.addView(connectionCard(),ui.top(18));return;}
        if(!error.isEmpty())body.addView(errorCard(),ui.top(12));
        LinearLayout quick=ui.row();quick.addView(folderTile("all","全部文件",meta.total,false),new LinearLayout.LayoutParams(0,-2,1));LinearLayout.LayoutParams qp=new LinearLayout.LayoutParams(0,-2,1);qp.leftMargin=ui.d(12);quick.addView(folderTile("unfiled","未归组",meta.unfiled,true),qp);body.addView(quick,ui.top(20));
        section(body,"我的文件夹","新建",()->folderPrompt(null));
        if(meta.folders.isEmpty()){LinearLayout empty=ui.card();empty.addView(ui.text("整理成自己的收藏",15,INK,true));empty.addView(ui.text("创建“工作资料”或“旅行”，收纳已经索引的文件。",12,MUTED,false),ui.top(7));empty.addView(ui.button("创建文件夹",false,()->folderPrompt(null)),ui.top(16));body.addView(empty,ui.top(12));}
        for(LibraryStore.Group g:meta.folders){LinearLayout r=ui.item(g.passwordProtected?"shield":"folder",g.name,g.passwordProtected?"受密码保护 · "+g.count+" 个文件":g.count+" 个文件",()->openFolder(g.id));r.removeViewAt(r.getChildCount()-1);r.addView(ui.iconButton("more",g.name+"的操作",()->folderMenu(g)));body.addView(r,ui.top(8));}
        section(body,"常用查询","新建查询",()->saveCollection(null));
        body.addView(ui.text("保存搜索和筛选条件，下次打开时按最新文件重新查询。",12,MUTED,false),ui.top(7));
        if(meta.collections.isEmpty())body.addView(ui.item("spark","创建第一个常用查询","例如：旅行标签下的全部图片",()->saveCollection(null)),ui.top(12));
        for(LibraryStore.Group g:meta.collections){LinearLayout r=ui.item("spark",g.name,collectionSummary(g.filters),()->{LocalFilters filters=g.filters.copy();if(filters.tags.size()>1||hasMultipleAttributes(filters.attributes)){multipleTagFilters=true;prefs.edit().putBoolean("multiple_tag_filters",true).apply();}setFilters(filters);activeCollection=g.id;tab=0;refreshScreen(true);});r.removeViewAt(r.getChildCount()-1);r.addView(ui.iconButton("more",g.name+"的操作",()->collectionMenu(g)));body.addView(r,ui.top(8));}
    }
    private View folderTile(String id,String name,int count,boolean inbox){LinearLayout card=ui.card();card.addView(ui.icon(inbox?"download":"library",27,GREEN));card.addView(ui.text(name,15,INK,true),ui.top(16));card.addView(ui.text(count+" 个文件",11,MUTED,false),ui.top(6));ui.touch(card,inbox?SOFT:PAPER,20,()->openFolder(id));return card;}
    private void openFolder(String id){LibraryStore target=store();if(target==null){toast("请先连接资源库");return;}if(target.remote){busy=true;io.execute(()->{try{boolean unlocked=target.folderUnlocked(id);runOnUiThread(()->{busy=false;if(destroyed)return;if(unlocked)showFolder(id);else folderPasswordPrompt(id,false);});}catch(Exception e){runOnUiThread(()->{busy=false;if(!destroyed)toast(message(e));});}});return;}showFolder(id);}
    private void showFolder(String id){LocalFilters next=new LocalFilters();next.folderId=id;setFilters(next);activeCollection=null;tab=0;refreshScreen(true);}
    private void folderPasswordPrompt(String id,boolean setting){
        LibraryStore target=store();LibraryStore.Group folder=find(meta.folders,id);if(target==null||!target.remote)return;
        MobileUi.Sheet sheet=ui.sheet(setting?(folder!=null&&folder.passwordProtected?"修改文件夹密码":"设置文件夹密码"):"解锁文件夹",folder==null?null:folder.name);
        EditText input=ui.input("4 到 72 个字符","");input.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);sheet.body.addView(input);
        TextView error=ui.text("",12,RED,false);sheet.body.addView(error,ui.top(8));
        sheet.footer.addView(ui.button(setting?"保存密码":"解锁",true,()->{
            String password=input.getText().toString().trim();if(password.length()<4||password.getBytes(java.nio.charset.StandardCharsets.UTF_8).length>72){error.setText("密码需为 4 到 72 个字符");return;}
            busy=true;io.execute(()->{try{
                if(setting)target.setFolderPassword(id,password);else target.unlockFolder(id,password);
                runOnUiThread(()->{busy=false;if(destroyed)return;sheet.close();toast(setting?"文件夹密码已保存":"文件夹已解锁");if(setting){if(f().folderId.equals(id))f().folderId="all";reload(false,true);}else showFolder(id);});
            }catch(Exception e){runOnUiThread(()->{busy=false;if(!destroyed)error.setText(message(e));});}});
        }));sheet.show();input.requestFocus();
    }
    private void folderPrompt(LibraryStore.Group g){prompt(g==null?"新建文件夹":"重命名文件夹","文件夹名称",g==null?"":g.name,value->{LibraryStore s=store();mutate(s,()->s.saveFolder(g==null?null:g.id,value),"文件夹已保存",null);});}
    private void folderMenu(LibraryStore.Group g){LibraryStore target=store();if(target!=null&&target.remote&&g.passwordProtected){busy=true;io.execute(()->{try{boolean unlocked=target.folderUnlocked(g.id);runOnUiThread(()->{busy=false;if(destroyed)return;if(unlocked)showFolderMenu(g,target);else folderPasswordPrompt(g.id,false);});}catch(Exception e){runOnUiThread(()->{busy=false;if(!destroyed)toast(message(e));});}});return;}showFolderMenu(g,target);}
    private void showFolderMenu(LibraryStore.Group g,LibraryStore target){MobileUi.Sheet s=ui.sheet(g.name,g.count+" 个文件");s.body.addView(ui.item("edit","重命名",null,()->{s.close();folderPrompt(g);}));if(target!=null&&target.remote)s.body.addView(ui.item("shield",g.passwordProtected?"修改文件夹密码":"设置文件夹密码","限制远程资源访问",()->{s.close();folderPasswordPrompt(g.id,true);}));if(target!=null&&target.remote&&g.passwordProtected)s.body.addView(ui.item("shield","取消文件夹密码",null,()->{s.close();confirm("取消文件夹密码？",g.name,()->mutate(target,()->target.setFolderPassword(g.id,""),"文件夹密码已取消",null));}));s.body.addView(ui.item("trash","删除文件夹","其中的文件会回到未归组，原文件不受影响",()->{s.close();confirm("删除“"+g.name+"”？","只移除虚拟文件夹，文件和标签会保留。",()->{LibraryStore st=store();mutate(st,()->st.deleteFolder(g.id),"文件夹已删除",()->{if(f().folderId.equals(g.id))f().folderId="unfiled";});});}));s.show();}
    private void saveCollection(LibraryStore.Group g){LocalFilters snapshot=g==null?f().copy():g.filters.copy();prompt(g==null?"保存为常用查询":"重命名查询","例如：旅行图片",g==null?"":g.name,name->{LibraryStore s=store();mutate(s,()->s.saveCollection(g==null?null:g.id,name,snapshot),"常用查询已保存",null);});}
    private void collectionMenu(LibraryStore.Group g){MobileUi.Sheet s=ui.sheet(g.name,collectionSummary(g.filters));s.body.addView(ui.item("edit","重命名",null,()->{s.close();saveCollection(g);}));s.body.addView(ui.item("refresh","用当前筛选更新",collectionSummary(f()),()->{s.close();LibraryStore st=store();LocalFilters copy=f().copy();mutate(st,()->st.saveCollection(g.id,g.name,copy),"查询条件已更新",null);}));s.body.addView(ui.item("trash","删除常用查询","文件不会被删除",()->{s.close();confirm("删除这个常用查询？",g.name,()->{LibraryStore st=store();mutate(st,()->st.deleteCollection(g.id),"常用查询已删除",()->{if(g.id.equals(activeCollection))activeCollection=null;});});}));s.show();}
    private void renderSources(){
        LinearLayout body=scrollBody();body.addView(ui.text("本机配置、连接状态与任务",13,MUTED,false));
        networkSpeedStatus=ui.text("当前网速：正在采样",12,GREEN,false);body.addView(networkSpeedStatus,ui.top(8));
        section(body,"Hub 连接",null,null);
        body.addView(ui.item("server",hubStore==null?"尚未连接 Hub":Uri.parse(hubStore.client.base).getHost(),hubStore==null?"连接后可查看节点状态和访问远程资源":needsLogin?"需要重新登录":error.isEmpty()?"Hub 会话已建立":"连接异常："+error,this::connectSheet),ui.top(10));
        section(body,"节点连接状态",null,null);body.addView(ui.item("server","节点连接总览",nodeOverviewSummary(),this::nodeStatusSheet),ui.top(10));
        section(body,"本机资源库",null,null);body.addView(ui.text("索引目录 "+meta.roots.size()+" 个 · 文件 "+meta.total+" 个",13,MUTED,false),ui.top(8));body.addView(ui.button(scanning?"正在扫描…":"添加索引目录",true,this::pickTree),ui.top(10));for(LibraryStore.Group r:meta.roots)body.addView(ui.item("folder",shortPath(r.name),r.count+" 个文件",()->rootMenu(r)),ui.top(7));
        section(body,"本机资源节点",null,null);resourceNodeStatus=ui.text(ResourceNodeService.status,12,MUTED,false);body.addView(ui.item("server",ResourceNodeService.running?"运行中 · "+localNodeName():"未运行 · "+localNodeName(),ResourceNodeService.status,this::resourceNodeSheet),ui.top(10));
        section(body,"本机缩略图",null,null);LinearLayout autoRow=ui.row();autoRow.addView(ui.text("索引后自动加入缩略图任务",14,INK,true),ui.weight());Switch autoThumb=new Switch(this);autoThumb.setChecked(prefs.getBoolean("auto_local_thumbnails",true));autoThumb.setOnCheckedChangeListener((v,checked)->prefs.edit().putBoolean("auto_local_thumbnails",checked).apply());autoRow.addView(autoThumb);body.addView(autoRow,ui.top(10));LinearLayout taskRow=ui.row();taskRow.addView(ui.text("缩略图后台任务",14,INK,true),ui.weight());thumbnailTaskSwitch=new Switch(this);thumbnailTaskSwitch.setChecked(LocalThumbnailTaskService.isEnabled());thumbnailTaskSwitch.setOnCheckedChangeListener((v,checked)->toggleThumbnailTask(checked));taskRow.addView(thumbnailTaskSwitch);body.addView(taskRow,ui.top(14));thumbnailTaskStatus=ui.text(getSharedPreferences(LocalThumbnailTaskService.PREFS,MODE_PRIVATE).getString("status",LocalThumbnailTaskService.status),12,MUTED,false);body.addView(thumbnailTaskStatus,ui.top(5));
        renderStorageDirectories(body);
        section(body,"云层存储",null,null);body.addView(ui.item("server",CloudStorageService.running?"存储节点运行中":"存储节点配置",CloudStorageService.running?CloudStorageService.status:"本机存储或 OpenList 后端",this::cloudStorageSheet),ui.top(10));
        section(body,"传输",null,null);body.addView(ui.item("download","传输列表",transferSummary(),()->navigate(3)),ui.top(10));section(body,"诊断",null,null);body.addView(ui.item("file","诊断日志","查看连接诊断并导出诊断包",this::diagnosticsSheet),ui.top(10));
        if(!scanMessage.isEmpty())body.addView(ui.text(scanMessage,12,GREEN,false),ui.top(12));
    }
    private String transferSummary(){JSONArray a=ResourceTransferService.records(this);int running=0;for(int i=0;i<a.length();i++){JSONObject x=a.optJSONObject(i);if(x!=null&&(x.optString("status").equals("下载中")||x.optString("status").equals("等待下载")))running++;}return running==0?"查看下载历史":running+" 个正在传输";}
    private String nodeOverviewSummary(){if(hubStore==null)return "连接 Hub 后查看各节点在线状态";int online=0,total=hubStore.client.nodes.length();for(int i=0;i<total;i++){JSONObject n=hubStore.client.nodes.optJSONObject(i);if(n!=null&&n.optBoolean("online"))online++;}return online+" 个在线 · "+(total-online)+" 个离线 · 点击查看节点";}
    private String formatPeerLink(JSONObject link){String state=link.optString("state","unknown");if("checking".equals(state))return "检测中";JSONObject checks=link.optJSONObject("checks");if(checks==null)return "connected".equals(state)?"已连接":"不可达";List<String> details=new ArrayList<>();String[] paths={"lan","webrtc","https","relay"};for(String path:paths){JSONObject check=checks.optJSONObject(path);if(check==null)continue;String label=peerPathLabel(path);if(!"connected".equals(check.optString("state"))){details.add(label+"不可达"+(check.optString("error").isEmpty()?"":" · "+check.optString("error")));continue;}long latency=check.optLong("webrtc".equals(path)?"connectMillis":"rttMillis",0);String value=label+("webrtc".equals(path)?" 建链/请求 ":" RTT ")+latency+" ms";long bps=check.optLong("speedBps",0);if(bps>0)value+=" · "+formatRate(bps);if(check.optLong("bytes")>0)value+=" · "+size(check.optLong("bytes"))+" / "+check.optLong("durationMillis")+" ms";if(check.optInt("httpStatus")>0)value+=" · HTTP "+check.optInt("httpStatus");if(!check.optString("selectedPair").isEmpty())value+=" · ICE "+check.optString("selectedPair");if(!check.optString("error").isEmpty())value+=" · "+check.optString("error");details.add(value);}String preferred=peerPathLabel(link.optString("path",""));return details.isEmpty()?preferred:("connected".equals(state)?"首选 "+preferred:"不可达")+" · "+String.join("；",details);}
    private String formatRate(long bytesPerSecond){if(bytesPerSecond>=1L<<20)return String.format(Locale.ROOT,"%.1f MB/s",bytesPerSecond/(double)(1<<20));if(bytesPerSecond>=1<<10)return String.format(Locale.ROOT,"%.0f KB/s",bytesPerSecond/(double)(1<<10));return bytesPerSecond+" B/s";}
    private void setThumbnailTaskChecked(boolean checked){if(thumbnailTaskSwitch==null)return;thumbnailTaskSwitch.setOnCheckedChangeListener(null);thumbnailTaskSwitch.setChecked(checked);thumbnailTaskSwitch.setOnCheckedChangeListener((button,value)->toggleThumbnailTask(value));}
    private void toggleThumbnailTask(boolean enabled){if(enabled)startLocalThumbnailTask();else{Intent stop=new Intent(this,LocalThumbnailTaskService.class);stop.setAction("stop");startService(stop);}}
    private void nodeStatusSheet(){MobileUi.Sheet sheet=ui.sheet("节点连接状态",hubStore==null?"连接 Hub 后可查看节点状态。":"Hub · "+Uri.parse(hubStore.client.base).getHost());if(hubStore==null){sheet.body.addView(ui.button("连接 Hub",true,()->{sheet.close();connectSheet();}));sheet.show();return;}Set<String> shown=new HashSet<>();JSONArray nodes=hubStore.client.nodes;if(nodes.length()==0)sheet.body.addView(ui.text("Hub 尚未返回资源节点。",13,MUTED,false));for(int i=0;i<nodes.length();i++){JSONObject node=nodes.optJSONObject(i);if(node==null)continue;String id=node.optString("id"),name=node.optString("name",id),state=node.optBoolean("online")?"在线":"离线";shown.add(id);sheet.body.addView(ui.item("android".equals(node.optString("kind"))?"phone":"server",name,state+" · "+node.optString("kind","node"),()->{sheet.close();nodeDetailSheet(node);}),ui.top(8));}if(CloudStorageService.running&&!CloudStorageService.peerNodes.isEmpty()){section(sheet.body,"云层节点",null,null);for(Map.Entry<String,JSONObject> entry:CloudStorageService.peerNodes.entrySet()){String id=entry.getKey();if(shown.contains(id))continue;JSONObject peer=entry.getValue();String name=peer.optString("name",id.substring(0,Math.min(12,id.length())));String state=CloudStorageService.peerLinks.containsKey(id)?formatPeerLink(CloudStorageService.peerLinks.get(id)):"等待连接检测";sheet.body.addView(ui.item("server",name,state,()->{sheet.close();nodeDetailSheet(peer);}),ui.top(8));}}sheet.show();}
    private void nodeDetailSheet(JSONObject node){MobileUi.Sheet sheet=ui.sheet(node.optString("name","节点"),node.optBoolean("online",true)?"当前在线":"当前离线");String id=node.optString("id");detailLine(sheet.body,"节点 ID",id);detailLine(sheet.body,"类型",node.optString("kind",node.optString("role","node")));detailLine(sheet.body,"连接状态",node.optBoolean("online",true)?"在线":"当前云层心跳可达");boolean selected=hubStore!=null&&id.equals(hubStore.client.selectedNodeId);detailLine(sheet.body,"当前数据路径",selected?hubStore.client.connectionDetails():"此资源节点当前未选中，尚未建立数据通道");if(node.has("canBrowse"))detailLine(sheet.body,"浏览资源",node.optBoolean("canBrowse")?"已授权":"未授权");if(node.has("canServe"))detailLine(sheet.body,"提供资源",node.optBoolean("canServe")?"已启用":"未启用");if(CloudStorageService.peerNodes.containsKey(id)){sheet.body.addView(ui.item("speed","连接诊断与纯测速",CloudStorageService.peerLinks.containsKey(id)?formatPeerLink(CloudStorageService.peerLinks.get(id)):"手动测试到此节点的连接与吞吐",()->{sheet.close();cloudPeerTestSheet(id);}),ui.top(14));}sheet.show();}
    private void cloudPeerTestSheet(String id){JSONObject node=CloudStorageService.peerNodes.get(id);if(node==null){toast("该云层节点已离线或不再可用");return;}JSONObject status=CloudStorageService.peerLinks.get(id);if(status==null)status=new JSONObject();MobileUi.Sheet sheet=ui.sheet(node.optString("name",id.substring(0,Math.min(12,id.length()))),"手动连接诊断与纯测速");TextView detail=ui.text(formatPeerLink(status),11,MUTED,false);sheet.body.addView(detail);JSONObject peer=status.optJSONObject("peer");if(peer!=null)sheet.body.addView(ui.text("地址类型 "+peer.optString("endpointType","未知")+" · 局域网地址 "+peer.optInt("lanAddresses")+" 个 · 最近心跳 "+peer.optString("seenAt","未知"),10,MUTED,false),ui.top(4));List<String> routes=new ArrayList<>();JSONObject checks=status.optJSONObject("checks");for(String route:new String[]{"lan","webrtc","https","relay"}){JSONObject check=checks==null?null:checks.optJSONObject(route);if(check!=null&&"connected".equals(check.optString("state")))routes.add(route);}if(routes.isEmpty()){if(node.optJSONArray("lan")!=null&&node.optJSONArray("lan").length()>0)routes.add("lan");String endpoint=node.optString("endpoint");if(endpoint.startsWith("p2p://"))routes.add("webrtc");else if(endpoint.contains("/cloud/relay/"))routes.add("relay");else if(endpoint.startsWith("https://"))routes.add("https");}Spinner routeChoice=new Spinner(this);List<String> labels=new ArrayList<>();for(String route:routes)labels.add(peerPathLabel(route));ArrayAdapter<String> adapter=new ArrayAdapter<>(this,android.R.layout.simple_spinner_item,labels);adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);routeChoice.setAdapter(adapter);if(!routes.isEmpty())sheet.body.addView(routeChoice,ui.top(10));TextView result=ui.text("点击“开始测速”后进行 3 轮独立测试，每轮传输 4 MiB 合成数据。",12,MUTED,false);sheet.body.addView(result,ui.top(9));sheet.body.addView(ui.button("重新连接诊断",false,()->{result.setText("正在检测可用路径…");CloudStorageService.diagnosePeer(id,value->handler.post(()->{if(value==null)return;CloudStorageService.peerLinks.put(id,value);detail.setText(formatPeerLink(value));result.setText(value.optString("state","unreachable").equals("connected")?"诊断完成，可选择已连通路径测速。":value.optString("error","没有可用连接路径"));}));}),ui.top(10));TextView speed=ui.button("开始纯测速",true,()->{});speed.setOnClickListener(view->{if(routes.isEmpty()){result.setText("没有可用路径；请先运行连接诊断。若诊断成功，请重新打开此节点详情。 ");return;}String route=routes.get(Math.max(0,routeChoice.getSelectedItemPosition()));speed.setEnabled(false);speed.setText("测速中…");result.setText("正在对"+peerPathLabel(route)+"进行 3 轮独立测速，请稍候…");CloudStorageService.speedTestPeer(id,route,value->handler.post(()->{speed.setEnabled(true);speed.setText("重新测速");if(value==null||!"complete".equals(value.optString("state"))){result.setText("测速失败："+(value==null?"无结果":value.optString("error","未知错误")));return;}StringBuilder summary=new StringBuilder(peerPathLabel(route)+"平均 "+formatRate(value.optLong("speedBps"))+" · "+size(value.optLong("bytes"))+" · 建链/请求 "+value.optLong("connectMillis")+" ms · 总耗时 "+value.optLong("durationMillis")+" ms");if(!value.optString("selectedPair").isEmpty())summary.append("\n已选 ICE 路径：").append(value.optString("selectedPair"));JSONArray samples=value.optJSONArray("samples");if(samples!=null)for(int i=0;i<samples.length();i++){JSONObject sample=samples.optJSONObject(i);if(sample!=null)summary.append("\n第 ").append(i+1).append(" 轮：").append(formatRate(sample.optLong("speedBps"))).append(" · 数据 ").append(sample.optLong("durationMillis")).append(" ms · 建链/请求 ").append(sample.optLong("connectMillis")).append(" ms");}result.setText(summary.toString());}));});sheet.body.addView(speed,ui.top(10));sheet.show();}
    private void startLocalThumbnailTask(){try{startForegroundService(new Intent(this,LocalThumbnailTaskService.class));toast("已启动本机缩略图后台任务");}catch(Exception ex){setThumbnailTaskChecked(false);toast("无法启动后台任务："+message(ex));}}
    private void diagnosticsSheet(){
        MobileUi.Sheet sheet=ui.sheet("诊断日志","日志保存在本机；导出时可选择手机上的保存位置。原生崩溃记录会在下次启动后尝试收集。");
        TextView content=ui.text(AppLog.recent(),11,INK,false);content.setTypeface(android.graphics.Typeface.MONOSPACE);content.setTextIsSelectable(true);
        sheet.body.addView(content);
        sheet.footer.addView(ui.button("导出诊断包",true,()->{sheet.close();exportDiagnostics();}));
        sheet.show();
    }
    private void exportDiagnostics(){
        AppLog.info("Diagnostic export requested");
        Intent intent=new Intent(Intent.ACTION_CREATE_DOCUMENT);intent.addCategory(Intent.CATEGORY_OPENABLE);
        intent.setType("application/zip");
        String stamp=new SimpleDateFormat("yyyyMMdd-HHmmss",Locale.ROOT).format(new Date());
        intent.putExtra(Intent.EXTRA_TITLE,"myliba-diagnostics-"+stamp+".zip");
        try{startActivityForResult(intent,EXPORT_LOG);}catch(ActivityNotFoundException ex){toast("设备没有可用的文件保存器");}
    }
    private View connectionCard(){return ui.item("server",hubStore==null?"连接 Hub":"选择 Hub 资源节点",hubStore==null?"连接一次，即可访问获授权的其他节点":Uri.parse(hubStore.client.base).getHost(),()->{if(hubStore==null||needsLogin)connectSheet();else sourceSheet();});}
    private void folderConflictSheet(){
        List<LocalLibrary.FolderConflict> conflicts;
        try(LocalLibrary library=new LocalLibrary(this)){conflicts=library.folderConflicts();}
        if(conflicts.isEmpty()){toast("没有待处理的归组冲突");return;}
        MobileUi.Sheet sheet=ui.sheet("归组待处理","这些移动在不同设备上互相未观察到。选择最终文件夹会生成一条新的同步操作。");
        for(LocalLibrary.FolderConflict conflict:conflicts){
            String title=conflict.folderName.isEmpty()?"未归组":conflict.folderName;
            sheet.body.addView(ui.item("folder",title,"文件 "+conflict.fileId+" · 来自 "+conflict.author+" 的第 "+conflict.counter+" 项修改",()->resolveFolderConflictSheet(conflict,sheet)),ui.top(9));
        }
        sheet.show();
    }
    private void resolveFolderConflictSheet(LocalLibrary.FolderConflict conflict,MobileUi.Sheet parent){
        EditText input=ui.input("留空表示未归组",conflict.folderName);
        new AlertDialog.Builder(this).setTitle("选择最终文件夹").setMessage("文件 "+conflict.fileId).setView(input).setNegativeButton("取消",null).setPositiveButton("确定",(dialog,which)->{
            String name=input.getText().toString().trim();
            io.execute(()->{try(LocalLibrary library=new LocalLibrary(this)){library.resolveFolderConflict(conflict.fileId,name);runOnUiThread(()->{parent.close();toast("已记录最终归属，等待同步");});}catch(Exception ex){runOnUiThread(()->toast("无法处理归组冲突："+message(ex)));}});
        }).show();
    }
    private void renameConflictSheet(){
        List<LocalLibrary.RenameConflict> conflicts;
        try(LocalLibrary library=new LocalLibrary(this)){conflicts=library.renameConflicts();}
        if(conflicts.isEmpty()){toast("没有待处理的改名冲突");return;}
        MobileUi.Sheet sheet=ui.sheet("改名待处理","这些改名在不同设备上互相未观察到。选择最终名称会生成一条新的同步操作。");
        for(LocalLibrary.RenameConflict conflict:conflicts){
            sheet.body.addView(ui.item("folder",conflict.folderName,"原名 "+conflict.anchor+" · 当前 "+conflict.appliedName+" · 来自 "+conflict.author+" 的第 "+conflict.counter+" 项修改",()->resolveRenameConflictSheet(conflict,sheet)),ui.top(9));
        }
        sheet.show();
    }
    private void resolveRenameConflictSheet(LocalLibrary.RenameConflict conflict,MobileUi.Sheet parent){
        EditText input=ui.input("最终文件夹名称",conflict.folderName);
        new AlertDialog.Builder(this).setTitle("选择最终名称").setMessage("原名 "+conflict.anchor).setView(input).setNegativeButton("取消",null).setPositiveButton("确定",(dialog,which)->{
            String name=input.getText().toString().trim();
            io.execute(()->{try(LocalLibrary library=new LocalLibrary(this)){library.resolveFolderRenameConflict(conflict.anchor,name);runOnUiThread(()->{parent.close();toast("已记录最终名称，等待同步");});}catch(Exception ex){runOnUiThread(()->toast("无法处理改名冲突："+message(ex)));}});
        }).show();
    }
    private void fileNameConflictSheet(){
        List<String[]> conflicts;try(LocalLibrary library=new LocalLibrary(this)){conflicts=library.fileNameConflicts();}
        if(conflicts.isEmpty()){toast("没有待处理的文件改名冲突");return;}
        MobileUi.Sheet sheet=ui.sheet("文件改名待处理","选择最终显示名称会生成一条新的同步操作，不修改物理文件名。");
        for(String[] conflict:conflicts)sheet.body.addView(ui.item("file",conflict[1],"文件 "+conflict[0]+" · 当前 "+conflict[2]+" · 来自 "+conflict[3]+" 的第 "+conflict[4]+" 项修改",()->prompt("选择最终显示名称","文件 "+conflict[0],conflict[1],name->{if(name.trim().isEmpty())return;io.execute(()->{try(LocalLibrary library=new LocalLibrary(this)){library.renameLogicalFile(conflict[0],name,true);runOnUiThread(()->{sheet.close();toast("已记录最终名称，等待同步");});}catch(Exception ex){runOnUiThread(()->toast("无法处理文件改名冲突："+message(ex)));}});})),ui.top(9));
        sheet.show();
    }
    private void revisionConflictSheet(){
        List<String[]> conflicts;try(LocalLibrary library=new LocalLibrary(this)){conflicts=library.revisionConflicts();}
        if(conflicts.isEmpty()){toast("没有待处理的版本选择冲突");return;}
        MobileUi.Sheet sheet=ui.sheet("版本选择待处理","选择当前版会生成新的同步操作，其他版本仍保留。");
        for(String[] conflict:conflicts)sheet.body.addView(ui.item("file","版本 "+conflict[1],"文件 "+conflict[0]+" · 当前 "+conflict[2]+" · 来自 "+conflict[3]+" 的第 "+conflict[4]+" 项修改",()->io.execute(()->{try(LocalLibrary library=new LocalLibrary(this)){library.selectCurrentRevision(conflict[0],conflict[1],true);runOnUiThread(()->{sheet.close();toast("已记录当前版本，等待同步");});}catch(Exception ex){runOnUiThread(()->toast("无法处理版本冲突："+message(ex)));}})),ui.top(9));
        sheet.show();
    }
    private void receiptSheet(){
        List<String[]> receipts;try(LocalLibrary library=new LocalLibrary(this)){receipts=library.remoteReceipts();}
        MobileUi.Sheet sheet=ui.sheet("节点应用回执","仅表示列出的节点已应用到相应操作，不代表所有设备均已同步。");
        for(String[] receipt:receipts){
            StringBuilder progress=new StringBuilder();
            try{JSONObject frontiers=new JSONObject(receipt[1]);Iterator<String> authors=frontiers.keys();while(authors.hasNext()){String author=authors.next();if(progress.length()>0)progress.append(" · ");progress.append(author).append(" 第 ").append(frontiers.optLong(author)).append(" 项");}}catch(Exception ignored){}
            sheet.body.addView(ui.item("sync","节点 "+receipt[0],progress+"\n回执时间 "+receipt[2],()->{}),ui.top(7));
        }
        if(receipts.isEmpty())sheet.body.addView(ui.text("还没有收到其他节点的应用回执。",12,MUTED,false));
        sheet.show();
    }
    private void resourceNodeSheet(){
        SharedPreferences settings=getSharedPreferences(ResourceNodeService.PREFS,MODE_PRIVATE);
        String tokenValue;try{tokenValue=CloudSecretStore.read(settings,"token");}catch(Exception ex){tokenValue="";}
        final String savedToken=tokenValue;
        boolean paired=settings.getBoolean("paired",false);
        if(!paired){connectSheet();return;}
        MobileUi.Sheet sheet=ui.sheet("手机资源节点","共享这台手机已索引的资源库。其他客户端优先通过 WebRTC 直连读取文件，连接失败时使用限速的 Hub 中转。");
        sheet.body.addView(ui.text("节点名称",12,INK,true));EditText name=ui.input("例如：我的手机",settings.getString("name",android.os.Build.MODEL));sheet.body.addView(name,ui.top(7));
        sheet.body.addView(ui.text("Hub HTTPS 地址",12,INK,true),ui.top(16));EditText hub=ui.input("https://hub.example.com",settings.getString("hub",prefs.getString("server_url","")));sheet.body.addView(hub,ui.top(7));
        sheet.body.addView(ui.text("这台手机已由主人授权，启动节点无需再输入密钥。",12,GREEN,false),ui.top(16));
        Switch indexSync=new Switch(this);indexSync.setText("将手机索引加入统一检索");indexSync.setChecked(settings.getBoolean("index_enabled",false));sheet.body.addView(indexSync,ui.top(16));
        Switch originalUpload=new Switch(this);originalUpload.setText("上传原件到云层（独立于索引发布）");originalUpload.setChecked(settings.getBoolean("original_upload_enabled",false));sheet.body.addView(originalUpload,ui.top(12));
        sheet.body.addView(ui.text("启用后可在本地文件详情中手动“上传原件”。原件经 WebRTC 直连分块送到所选查询节点加密保存，Hub 不经手明文；上传完成并经读回验证后才可在另一端释放本机空间。释放仅作用于你明确选中的位置，删除前会再次复核版本与安全保存状态。",12,MUTED,false),ui.top(7));
        EditText indexKey=ui.input("已有云库的恢复密钥（64 位；留空保留已存密钥）","");indexKey.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);sheet.body.addView(indexKey,ui.top(7));
        sheet.body.addView(ui.text("系统网络任务会发布待同步变更并拉取整理操作，启动应用或资源节点时也会触发。只交换加密目录，不上传原件。扫描、标签和归组修改会写入可恢复队列。请使用电脑云层中的恢复密钥；密钥由 Android Keystore 保护。停用不会撤回其他设备已收到的目录。",12,MUTED,false),ui.top(9));
        TextView state=ui.text(ResourceNodeService.status+"\n"+LocalIndexPublisher.status,12,MUTED,false);sheet.body.addView(state,ui.top(13));
        sheet.footer.addView(ui.button(ResourceNodeService.running?"保存并重启资源节点":"启动资源节点",true,()->{
            String h=hub.getText().toString().trim(),n=name.getText().toString().trim(),t=savedToken;
            if(paired&&!h.replaceAll("/+$","").equals(settings.getString("hub",""))){state.setText("更换 Hub 请先在家中资源库为这台手机重新授权");state.setTextColor(RED);return;}
            if(!validCloudAddress(h)||n.isEmpty()||n.length()>80||t.length()<32||t.contains("\r")||t.contains("\n")){state.setText("请先通过主人密码授权手机，再检查 Hub 地址和节点名称");state.setTextColor(RED);return;}
            String protectedToken;try{protectedToken=CloudSecretStore.protect(t);}catch(Exception ex){state.setText("无法保存节点密钥："+message(ex));state.setTextColor(RED);return;}
            String protectedIndexKey=settings.getString("index_key","");
            try{String supplied=indexKey.getText().toString().trim();if(!supplied.isEmpty()){LocalIndexCrypto.key(supplied);protectedIndexKey=CloudSecretStore.protect(supplied);}else if(indexSync.isChecked())LocalIndexCrypto.key(CloudSecretStore.read(settings,"index_key"));}
            catch(Exception ex){state.setText("无法启用索引同步："+message(ex));state.setTextColor(RED);return;}
            settings.edit().putString("hub",h.replaceAll("/+$","")).putString("name",n).putString("token",protectedToken).putBoolean("index_enabled",indexSync.isChecked()).putBoolean("original_upload_enabled",originalUpload.isChecked()).putString("index_key",protectedIndexKey).apply();
            LocalIndexJobService.schedule(this,indexSync.isChecked());
            stopService(new Intent(this,ResourceNodeService.class));
            handler.postDelayed(()->{startForegroundService(new Intent(this,ResourceNodeService.class));if(tab==2)handler.postDelayed(this::render,500);},400);sheet.close();
        }));
        int conflicts;try(LocalLibrary library=new LocalLibrary(this)){conflicts=library.folderConflictCount();}
        if(conflicts>0)sheet.footer.addView(ui.button("处理归组冲突（"+conflicts+"）",false,()->{sheet.close();folderConflictSheet();}),ui.top(9));
        int renames;try(LocalLibrary library=new LocalLibrary(this)){renames=library.renameConflictCount();}
        if(renames>0)sheet.footer.addView(ui.button("处理改名冲突（"+renames+"）",false,()->{sheet.close();renameConflictSheet();}),ui.top(9));
        int nameConflicts;try(LocalLibrary library=new LocalLibrary(this)){nameConflicts=library.fileNameConflictCount();}
        if(nameConflicts>0)sheet.footer.addView(ui.button("处理文件改名冲突（"+nameConflicts+"）",false,()->{sheet.close();fileNameConflictSheet();}),ui.top(9));
        int revisionConflicts;try(LocalLibrary library=new LocalLibrary(this)){revisionConflicts=library.revisionConflictCount();}
        if(revisionConflicts>0)sheet.footer.addView(ui.button("处理版本选择冲突（"+revisionConflicts+"）",false,()->{sheet.close();revisionConflictSheet();}),ui.top(9));
        int receipts;try(LocalLibrary library=new LocalLibrary(this)){receipts=library.remoteReceiptCount();}
        if(receipts>0)sheet.footer.addView(ui.button("查看节点回执（"+receipts+"）",false,()->{sheet.close();receiptSheet();}),ui.top(9));
        boolean retired;String memberNode=ResourceNodeService.ensureID(this);try(LocalLibrary library=new LocalLibrary(this)){retired=library.retiredMember(memberNode);}
        if(retired)sheet.footer.addView(ui.button("重新加入资源库（新作者 epoch）",false,()->{state.setText("正在从 Hub 完整引导…");io.execute(()->{try{
            new LocalIndexPublisher(this).sync(settings.getString("hub",""),savedToken,memberNode);
            try(LocalLibrary library=new LocalLibrary(this)){library.rejoinMember(memberNode);}
            LocalIndexJobService.schedule(this,true);runOnUiThread(()->{if(!destroyed){sheet.close();toast("已创建新作者 epoch，等待同步");}});
        }catch(Exception ex){runOnUiThread(()->{if(!destroyed)state.setText("重新加入失败："+message(ex));});}});}),ui.top(9));
        if(indexSync.isChecked())sheet.footer.addView(ui.button("立即同步索引",false,()->{LocalIndexJobService.schedule(this,true);toast("已请求系统执行索引同步");}),ui.top(9));
        if(ResourceNodeService.running)sheet.footer.addView(ui.button("停止资源节点",false,()->{stopService(new Intent(this,ResourceNodeService.class));sheet.close();handler.postDelayed(this::render,350);}),ui.top(9));
        sheet.show();
    }
    private void cloudStorageSheet(){
        android.content.SharedPreferences settings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
        String storedPassword;try{storedPassword=CloudSecretStore.read(settings,"openlist_password");}catch(Exception ex){toast("OpenList 凭据读取失败，请重新配置节点");storedPassword="";}
        MobileUi.Sheet sheet=ui.sheet("手机云层存储节点","同一应用可浏览文件或只运行存储节点。节点只处理已加密的文件块。");
		if(CloudStorageService.running)renderCloudPeerControls(sheet);
        sheet.body.addView(ui.text("存储位置",12,INK,true));
        Spinner backendChoice=new Spinner(this);ArrayAdapter<String> choices=new ArrayAdapter<>(this,android.R.layout.simple_spinner_item,new String[]{"本手机存储（云层节点）","OpenList 云盘（可选后端）"});choices.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);backendChoice.setAdapter(choices);
        backendChoice.setSelection("openlist".equals(settings.getString("backend","disk"))?1:0);sheet.body.addView(backendChoice,ui.top(7));
        LinearLayout openlistFields=ui.col();
        openlistFields.addView(ui.text("OpenList HTTPS 地址",12,INK,true));EditText openlistURL=ui.input("https://openlist.example.com",settings.getString("openlist_url",""));openlistFields.addView(openlistURL,ui.top(7));
        openlistFields.addView(ui.text("可写挂载路径",12,INK,true),ui.top(16));EditText mount=ui.input("例如 aliyun 或 quark",settings.getString("openlist_mount",""));openlistFields.addView(mount,ui.top(7));
        openlistFields.addView(ui.text("OpenList 用户名",12,INK,true),ui.top(16));EditText user=ui.input("有 WebDAV 管理权限的用户",settings.getString("openlist_user",""));openlistFields.addView(user,ui.top(7));
        openlistFields.addView(ui.text("OpenList 密码",12,INK,true),ui.top(16));EditText password=ui.input("OpenList 用户密码",storedPassword);password.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);addClearablePassword(openlistFields,password,7);
        openlistFields.addView(ui.text("云盘账号由 OpenList 管理；这里通过 /dav/挂载路径 存取加密块。请在 OpenList 中选择可写驱动，并启用 WebDAV 本机代理。",12,MUTED,false),ui.top(9));
        sheet.body.addView(openlistFields,ui.top(17));openlistFields.setVisibility(backendChoice.getSelectedItemPosition()==1?View.VISIBLE:View.GONE);
        backendChoice.setOnItemSelectedListener(new android.widget.AdapterView.OnItemSelectedListener(){public void onNothingSelected(android.widget.AdapterView<?> parent){}public void onItemSelected(android.widget.AdapterView<?> parent,View view,int position,long id){openlistFields.setVisibility(position==1?View.VISIBLE:View.GONE);}});
        sheet.body.addView(ui.text("Hub HTTPS 地址",12,INK,true));EditText hub=ui.input("https://hub.example.com",settings.getString("hub",""));sheet.body.addView(hub,ui.top(7));
        sheet.body.addView(ui.text("手机的直连 HTTPS 地址（可选）",12,INK,true),ui.top(16));EditText endpoint=ui.input("https://phone.example.com",settings.getString("url",""));sheet.body.addView(endpoint,ui.top(7));
        sheet.body.addView(ui.text("存储中继 HTTPS 地址（可选）",12,INK,true),ui.top(16));EditText relay=ui.input("https://storage.example.com",settings.getString("relay",""));sheet.body.addView(relay,ui.top(7));
        sheet.body.addView(ui.text("主人密码（首次授权或重新授权时填写）",12,INK,true),ui.top(16));EditText ownerPassword=ui.input("已授权这台手机可留空","");ownerPassword.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);addClearablePassword(sheet.body,ownerPassword,7);
        TextView validation=ui.text("两个可选地址都留空时，Hub 只负责信令，文件块由节点 WebRTC 直连传输。若网络无法建立直连，可另配存储中继。",12,MUTED,false);sheet.body.addView(validation,ui.top(14));
        sheet.footer.addView(ui.button(CloudStorageService.running?"保存并重启节点":"启动存储节点",true,()->{
            String h=hub.getText().toString().trim(),u=endpoint.getText().toString().trim(),r=relay.getText().toString().trim(),ownerPass=ownerPassword.getText().toString();
            if(!validCloudAddress(h)||(!u.isEmpty()&&!validCloudAddress(u))||(!r.isEmpty()&&!validCloudAddress(r))){validation.setText("请输入有效的 Hub HTTPS 地址");validation.setTextColor(RED);return;}
            String backend=backendChoice.getSelectedItemPosition()==1?"openlist":"disk";
            String address=openlistURL.getText().toString().trim(),mountPath=mount.getText().toString().trim(),username=user.getText().toString().trim(),pass=password.getText().toString();
            if(backend.equals("openlist"))try{new CloudOpenListBackend(address,mountPath,username,pass);}catch(Exception ex){validation.setText(message(ex));validation.setTextColor(RED);return;}
            if(busy)return;busy=true;validation.setText("正在验证设备授权…");validation.setTextColor(MUTED);
            io.execute(()->{try{
                String base=h.replaceAll("/+$",""),deviceToken="";
                if(ownerPass.isEmpty()&&base.equals(prefs.getString("device_hub","")))deviceToken=CloudSecretStore.read(prefs,"device_token");
                if(deviceToken.length()<32){
                    if(ownerPass.isEmpty())throw new java.io.IOException("请填写主人密码，先为这台手机授权");
                    RemoteClient candidate=new RemoteClient(base,prefs,this);
                    candidate.json("POST","/api/session",RemoteClient.object("password",ownerPass));
                    String id=ResourceNodeService.ensureID(this);
                    JSONObject result=candidate.json("POST","/api/hub/devices",RemoteClient.object("id",id,"name",android.os.Build.MODEL,"kind","android","canBrowse",true,"canServe",true,"canCloud",true));
                    deviceToken=result.getString("token");candidate.pairedCredential(id,deviceToken);ResourceNodeService.savePaired(this,base,id,deviceToken);
                    candidate.json("DELETE","/api/session",null);candidate.forget();
                }
                String protectedToken=CloudSecretStore.protect(deviceToken),protectedPassword=CloudSecretStore.protect(pass);
                settings.edit().putString("hub",base).putString("url",u.replaceAll("/+$","")).putString("relay",r.replaceAll("/+$","")).putString("token",protectedToken)
                    .putString("backend",backend).putString("openlist_url",address.replaceAll("/+$","")).putString("openlist_mount",mountPath).putString("openlist_user",username).putString("openlist_password",protectedPassword).apply();
                runOnUiThread(()->{busy=false;stopService(new Intent(this,CloudStorageService.class));if(ResourceNodeService.running){stopService(new Intent(this,ResourceNodeService.class));handler.postDelayed(()->startForegroundService(new Intent(this,ResourceNodeService.class)),700);}handler.postDelayed(()->{startForegroundService(new Intent(this,CloudStorageService.class));if(storageOnly||tab==2)handler.postDelayed(this::render,500);},400);sheet.close();});
            }catch(Exception ex){runOnUiThread(()->{busy=false;validation.setText("授权或保存失败："+message(ex));validation.setTextColor(RED);});}});
        }));
        if(CloudStorageService.running)sheet.footer.addView(ui.button("停止存储节点",false,()->{stopService(new Intent(this,CloudStorageService.class));sheet.close();handler.postDelayed(this::render,300);}),ui.top(9));
        sheet.show();
    }
    private void renderCloudPeerControls(MobileUi.Sheet sheet){
        section(sheet.body,"节点连接诊断与纯测速",null,null);
        sheet.body.addView(ui.text("诊断会逐条检查可用路径、RTT、建链耗时和 ICE 候选类型。纯测速只收发合成数据，每条路径 3 轮、每轮 4 MiB，不读写云层文件。",11,MUTED,false),ui.top(5));
        if(CloudStorageService.peerNodes.isEmpty()){sheet.body.addView(ui.text("其他节点：尚未发现",12,MUTED,false),ui.top(8));return;}
        List<String> ids=new ArrayList<>(CloudStorageService.peerNodes.keySet());Collections.sort(ids);
        for(String id:ids){JSONObject node=CloudStorageService.peerNodes.get(id);if(node==null)continue;JSONObject status=CloudStorageService.peerLinks.get(id);if(status==null)status=new JSONObject();
            TextView heading=ui.text(("storage".equals(node.optString("role"))?"存储节点":"客户端")+" · "+id.substring(0,Math.min(12,id.length())),13,INK,true);sheet.body.addView(heading,ui.top(10));
            TextView detail=ui.text(formatPeerLink(status),11,MUTED,false);sheet.body.addView(detail,ui.top(4));
            JSONObject peerDetail=status.optJSONObject("peer");if(peerDetail!=null)sheet.body.addView(ui.text("地址类型 "+peerDetail.optString("endpointType","未知")+" · 局域网地址 "+peerDetail.optInt("lanAddresses")+" 个 · 最近心跳 "+peerDetail.optString("seenAt","未知")+" · 检测 "+status.optString("checkedAt","未知"),10,MUTED,false),ui.top(3));
            List<String> routes=new ArrayList<>();JSONObject checks=status.optJSONObject("checks");for(String route:new String[]{"lan","webrtc","https","relay"})if(checks!=null&&"connected".equals(checks.optJSONObject(route)==null?"":checks.optJSONObject(route).optString("state")))routes.add(route);
            if(routes.isEmpty()){if(node.optJSONArray("lan")!=null&&node.optJSONArray("lan").length()>0)routes.add("lan");String endpoint=node.optString("endpoint");if(endpoint.startsWith("p2p://"))routes.add("webrtc");else if(endpoint.contains("/cloud/relay/"))routes.add("relay");else if(endpoint.startsWith("https://"))routes.add("https");}
            Spinner routeChoice=new Spinner(this);List<String> labels=new ArrayList<>();for(String route:routes)labels.add(peerPathLabel(route));ArrayAdapter<String> adapter=new ArrayAdapter<>(this,android.R.layout.simple_spinner_item,labels);adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);routeChoice.setAdapter(adapter);if(!routes.isEmpty())sheet.body.addView(routeChoice,ui.top(5));
            TextView resultText=ui.text("测速结果将在这里显示",10,MUTED,false);sheet.body.addView(resultText,ui.top(4));
            LinearLayout actions=ui.row();actions.addView(ui.button("连接诊断",false,()->{resultText.setText("正在检测局域网、WebRTC、HTTPS 与存储中继…");CloudStorageService.diagnosePeer(id,result->{handler.post(()->{if(result==null)return;CloudStorageService.peerLinks.put(id,result);detail.setText(formatPeerLink(result));JSONObject peer=result.optJSONObject("peer");if(peer!=null)resultText.setText("地址类型 "+peer.optString("endpointType","未知")+" · 局域网地址 "+peer.optInt("lanAddresses")+" 个 · 检测时间 "+result.optString("checkedAt","未知"));else resultText.setText(result.optString("error","诊断失败"));});});}),new LinearLayout.LayoutParams(0,-2,1));
            actions.addView(ui.button("纯测速",false,()->{if(routes.isEmpty()){resultText.setText("当前没有可用的测速路径");return;}String route=routes.get(Math.max(0,routeChoice.getSelectedItemPosition()));resultText.setText("正在对"+peerPathLabel(route)+"进行 3 轮独立测速…");CloudStorageService.speedTestPeer(id,route,result->{handler.post(()->{if(result==null)return;if("complete".equals(result.optString("state"))){StringBuilder summary=new StringBuilder(peerPathLabel(route)+"平均 "+formatRate(result.optLong("speedBps"))+" · "+size(result.optLong("bytes"))+" · 建链/请求 "+result.optLong("connectMillis")+" ms · 总耗时 "+result.optLong("durationMillis")+" ms");if(!result.optString("selectedPair").isEmpty())summary.append("\n已选 ICE 路径：").append(result.optString("selectedPair"));JSONArray samples=result.optJSONArray("samples");if(samples!=null)for(int i=0;i<samples.length();i++){JSONObject sample=samples.optJSONObject(i);if(sample!=null)summary.append("\n第 ").append(i+1).append(" 轮：").append(formatRate(sample.optLong("speedBps"))).append(" · 数据 ").append(sample.optLong("durationMillis")).append(" ms · 建链/请求 ").append(sample.optLong("connectMillis")).append(" ms");}resultText.setText(summary.toString());}else resultText.setText("测速失败："+result.optString("error","未知错误"));});});}),new LinearLayout.LayoutParams(0,-2,1));
            sheet.body.addView(actions,ui.top(5));
        }
    }
    private String peerPathLabel(String path){return "lan".equals(path)?"局域网 TLS":"webrtc".equals(path)?"WebRTC":"relay".equals(path)?"存储中继":"HTTPS";}
    private static boolean validCloudAddress(String address){
        try{java.net.URI uri=new java.net.URI(address);return "https".equalsIgnoreCase(uri.getScheme())&&uri.getHost()!=null&&uri.getUserInfo()==null&&uri.getRawQuery()==null&&uri.getRawFragment()==null&&(uri.getPath().isEmpty()||uri.getPath().equals("/"));}catch(Exception ex){return false;}
    }
    private View errorCard(){LinearLayout card=ui.card();card.addView(ui.text(error,13,RED,false));card.addView(ui.button(needsLogin?"登录":"重试",false,needsLogin?this::connectSheet:()->reload(false,true)),ui.top(12));return card;}
    private LinearLayout scrollBody(){ScrollView scroll=new ScrollView(this);scroll.setVerticalScrollBarEnabled(false);LinearLayout body=ui.col();body.setPadding(ui.d(20),0,ui.d(20),ui.d(24));scroll.addView(body);content.addView(scroll,new LinearLayout.LayoutParams(-1,-1));return body;}
    private void section(LinearLayout parent,String label,String action,Runnable run){LinearLayout row=ui.row();row.addView(ui.text(label,15,INK,true),ui.weight());if(action!=null){TextView t=ui.text(action,12,GREEN,true);ui.pad(t,4,8);if(run!=null)t.setOnClickListener(v->run.run());row.addView(t);}parent.addView(row,ui.top(24));}
    private void rootMenu(LibraryStore.Group g){MobileUi.Sheet s=ui.sheet(g.name,g.count+" 个已索引文件");s.body.addView(ui.item("refresh","重新扫描","读取新增或已修改的文件",()->{s.close();scan(Uri.parse(g.id));}));s.body.addView(ui.item("trash","移除索引目录","移除本地索引和标签，原文件仍保留",()->{s.close();confirm("移除这个目录？","此目录的本地索引和标签将删除，原文件不受影响。",()->mutate(localStore,()->{db.removeRoot(g.id);try{getContentResolver().releasePersistableUriPermission(Uri.parse(g.id),Intent.FLAG_GRANT_READ_URI_PERMISSION);}catch(SecurityException ignored){}},"已移除目录",()->{if(localFilters.rootUri.equals(g.id))localFilters.rootUri="";}));}));s.show();}
    private void addClearablePassword(LinearLayout parent,EditText field,int top){
        LinearLayout row=ui.row();row.setGravity(Gravity.CENTER_VERTICAL);
        row.addView(field,new LinearLayout.LayoutParams(0,-2,1));
        TextView clear=ui.button("清空",false,()->{field.setText("");field.requestFocus();});
        clear.setContentDescription("清空密码输入框");
        LinearLayout.LayoutParams action=new LinearLayout.LayoutParams(-2,-2);action.leftMargin=ui.d(8);row.addView(clear,action);
        parent.addView(row,ui.top(top));
    }
    private void connectSheet(){
        MobileUi.Sheet s=ui.sheet("连接 Hub","首次输入主人密码，为这台手机授权。以后由设备凭据自动连接。");s.body.addView(ui.icon("server",42,GREEN));s.body.addView(ui.text("Hub 地址",12,INK,true),ui.top(22));EditText address=ui.input("https://library.example.com",hubStore==null?"":hubStore.client.base);address.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_URI);s.body.addView(address,ui.top(9));s.body.addView(ui.text("主人密码（已授权设备可留空）",12,INK,true),ui.top(18));EditText password=ui.input("与 Web 端使用相同的密码","");password.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD);addClearablePassword(s.body,password,9);
        s.body.addView(ui.text("恢复 Hub 旧手机设备 ID（可选）",12,INK,true),ui.top(18));EditText oldID=ui.input("从 Hub 的设备授权列表复制完整 ID","");s.body.addView(oldID,ui.top(9));s.body.addView(ui.text("仅在卸载重装后恢复旧设备时填写；需要主人密码。此操作会更新旧设备的授权凭据。",12,MUTED,false),ui.top(7));
        TextView status=ui.text("主人密码不会保存在手机上；设备凭据由系统安全存储。",12,MUTED,false);s.body.addView(status,ui.top(14));TextView connect=ui.button("连接 Hub",true,()->{});s.footer.addView(connect);
        connect.setOnClickListener(v->{if(busy)return;final String base;try{base=RemoteClient.normalize(address.getText().toString());}catch(Exception e){status.setText(message(e));status.setTextColor(RED);return;}String pass=password.getText().toString(),restoreID=oldID.getText().toString().trim();if(!restoreID.isEmpty()&&pass.isEmpty()){status.setText("恢复旧设备 ID 需要主人密码");status.setTextColor(RED);return;}RemoteClient candidate=new RemoteClient(base,prefs,this);connect.setEnabled(false);connect.setText("正在连接…");status.setText("正在验证连接");status.setTextColor(MUTED);busy=true;
            io.execute(()->{try{
                boolean paired=false,legacyHub=false;
                if(!pass.isEmpty()){
                    candidate.json("POST","/api/session",RemoteClient.object("password",pass));
                    String id=restoreID.isEmpty()?ResourceNodeService.ensureID(this):verifiedOldAndroidID(candidate,restoreID);
                    try{
                        JSONObject result=candidate.json("POST","/api/hub/devices",RemoteClient.object("id",id,"name",android.os.Build.MODEL,"kind","android","canBrowse",true,"canServe",true,"canCloud",true));
                        String token=result.getString("token");candidate.pairedCredential(id,token);ResourceNodeService.savePaired(this,base,id,token);
                        android.content.SharedPreferences cloudSettings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
                        if(base.equals(cloudSettings.getString("hub","")))cloudSettings.edit().putString("token",CloudSecretStore.protect(token)).apply();
                        candidate.json("DELETE","/api/session",null);
                        candidate.forget();
                        candidate.json("POST","/api/session/device",RemoteClient.object("id",id,"token",token));
                        candidate.json("GET","/api/session",null);paired=true;
                    }catch(RemoteClient.Failure ex){if(ex.status!=404)throw ex;legacyHub=true;}
                }else candidate.json("GET","/api/session",null);
                String savedCookie=CloudSecretStore.protect(candidate.cookie());
                final boolean restartNode=paired&&ResourceNodeService.running,restartCloud=paired&&CloudStorageService.running&&base.equals(getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE).getString("hub","")),oldHub=legacyHub;
                runOnUiThread(()->{busy=false;if(destroyed){new Thread(candidate::close,"resource-rtc-dispose").start();return;}if(hubStore!=null){RemoteClient previous=hubStore.client;new Thread(previous::close,"resource-rtc-dispose").start();}prefs.edit().putString("server_url",base).putString("server_cookie",savedCookie).apply();hubStore=new LibraryStore(db,candidate);home=!candidate.selectedNodeId.equals(ResourceNodeService.ensureID(this));tab=0;needsLogin=false;error="";meta=new LibraryStore.Meta();entries.clear();generation++;s.close();render();reload(false,true);if(oldHub)toast("Hub 尚未支持设备授权，暂用旧登录");if(restartNode){stopService(new Intent(this,ResourceNodeService.class));handler.postDelayed(()->startForegroundService(new Intent(this,ResourceNodeService.class)),700);}if(restartCloud){stopService(new Intent(this,CloudStorageService.class));handler.postDelayed(()->startForegroundService(new Intent(this,CloudStorageService.class)),700);}});
            }catch(Exception e){candidate.close();runOnUiThread(()->{busy=false;if(destroyed)return;connect.setEnabled(true);connect.setText("连接 Hub");status.setText(e instanceof RemoteClient.Failure&&((RemoteClient.Failure)e).status==401&&!pass.isEmpty()?"密码不正确或登录已过期，请重新输入。":message(e));status.setTextColor(RED);});}});
        });s.show();
    }
    private static String verifiedOldAndroidID(RemoteClient client,String id)throws Exception{
        if(!id.matches("[A-Za-z0-9_-]{1,80}"))throw new java.io.IOException("旧设备 ID 格式无效");
        JSONArray devices=client.json("GET","/api/hub/devices",null).optJSONArray("devices");
        if(devices!=null)for(int i=0;i<devices.length();i++){
            JSONObject device=devices.getJSONObject(i);
            if(id.equals(device.optString("id"))){
                if(!"android".equals(device.optString("kind")))throw new java.io.IOException("所选 ID 不是手机设备");
                return id;
            }
        }
        throw new java.io.IOException("Hub 中找不到该手机设备 ID，请从设备授权列表复制完整 ID");
    }
    private void logout(){if(hubStore==null)return;confirm("退出并撤销这台手机？","将清除本机设备凭据并停止资源节点；本机文件和索引会保留。",()->{LibraryStore old=hubStore;mutate(old,()->{
        if(old.client.paired()){
            try{old.client.json("DELETE","/api/session/device",null);}
            catch(RemoteClient.Failure ex){if(ex.status!=401)throw ex;}
        }else old.client.json("DELETE","/api/session",null);
        old.client.unpair();if(ResourceNodeService.clearPaired(this,old.client.base))stopService(new Intent(this,ResourceNodeService.class));
        android.content.SharedPreferences cloudSettings=getSharedPreferences(CloudStorageService.PREFS,MODE_PRIVATE);
        if(old.client.base.equals(cloudSettings.getString("hub",""))){cloudSettings.edit().remove("token").apply();stopService(new Intent(this,CloudStorageService.class));}
    },"已清除本机授权",()->{needsLogin=true;entries.clear();home=false;tab=2;meta=new LibraryStore.Meta();});});}
    private void pickTree(){if(scanning){toast("当前目录扫描完成后再添加");return;}Intent i=new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION|Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);try{startActivityForResult(i,PICK_TREE);}catch(ActivityNotFoundException e){toast("设备没有可用的文件夹选择器");}}
    private void pickStorageTree(){Intent i=new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);i.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION|Intent.FLAG_GRANT_WRITE_URI_PERMISSION|Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION);try{startActivityForResult(i,PICK_STORAGE_TREE);}catch(ActivityNotFoundException e){toast("设备没有可用的文件夹选择器");}}
    private void renderStorageDirectories(LinearLayout body){
        section(body,"附加内容与云层存储",null,null);
        body.addView(ui.text("默认应用目录排在第一位；缩略图和云层加密块按顺序写入有剩余配额的目录。0 GB 表示不限制。",12,MUTED,false),ui.top(7));
        int count=StorageDirectories.configured(this).length();
        for(int i=-1;i<count;i++){
            final int index=i;long used=StorageDirectories.usage(this,i),limit=StorageDirectories.limit(this,i);
            String title=i<0?"默认应用目录":StorageDirectories.label(this,i);
            String detail=size(used)+" / "+(limit>0?size(limit):"不限")+(i<0?" · 第一存储位置":" · 第"+(i+2)+"存储位置");
            body.addView(ui.item("harddrive",title,detail,()->storageDirectorySheet(index)),ui.top(7));
        }
        body.addView(ui.button("添加本地存储目录",false,this::pickStorageTree),ui.top(10));
    }
    private void storageDirectorySheet(int index){
        MobileUi.Sheet sheet=ui.sheet(index<0?"默认应用目录":StorageDirectories.label(this,index),"已用 "+size(StorageDirectories.usage(this,index)));
        sheet.body.addView(ui.item("edit","设置容量上限","单位 GB，输入 0 表示不限",()->{sheet.close();prompt("容量上限（GB）","例如 10",StorageDirectories.limit(this,index)>0?String.valueOf((double)StorageDirectories.limit(this,index)/(1L<<30)):"0",value->{double gb=Double.parseDouble(value);if(!Double.isFinite(gb)||gb<0)throw new IllegalArgumentException("请输入大于等于 0 的 GB 数值");try{StorageDirectories.setLimit(this,index,(long)(gb*(1L<<30)));}catch(Exception ex){throw new IllegalArgumentException(message(ex));}render();});}));
        if(index>=0)sheet.body.addView(ui.item("trash","移除此目录","不会删除目录内已经保存的文件",()->{sheet.close();StorageDirectories.remove(this,index);render();}));
        if(index>=0)sheet.body.addView(ui.item("folder","迁移并移除","先复制并校验附加内容，再清理此目录中的 Myliba 内容",()->{sheet.close();chooseStorageMigrationTarget(index);}));
        sheet.show();
    }
    private void chooseStorageMigrationTarget(int sourceIndex){
        JSONArray dirs=StorageDirectories.configured(this);ArrayList<Integer> indexes=new ArrayList<>();ArrayList<String> labels=new ArrayList<>();
        indexes.add(-1);labels.add("默认应用目录 · "+StorageDirectories.label(this,-1));
        for(int i=0;i<dirs.length();i++)if(i!=sourceIndex){indexes.add(i);labels.add(StorageDirectories.label(this,i));}
        new AlertDialog.Builder(this).setTitle("选择迁移目标").setItems(labels.toArray(new String[0]),(dialog,which)->{
            int targetIndex=indexes.get(which);String source=StorageDirectories.label(this,sourceIndex),target=StorageDirectories.label(this,targetIndex);
            confirm("迁移并移除目录？","将把“"+source+"”中的缩略图、视频预览和云层文件迁移到“"+target+"”。校验成功后只清理源目录中 Myliba 管理的内容，其他文件会保留。",()->{
                toast("正在迁移存储内容…");io.execute(()->{try{StorageDirectories.migrateAndRemove(this,sourceIndex,targetIndex);runOnUiThread(()->{if(!destroyed){render();toast("迁移完成，原存储目录已移除");}});}catch(Exception error){runOnUiThread(()->{if(!destroyed)toast("迁移失败，源目录保留："+message(error));});}});
            });
        }).show();
    }
    @Override protected void onActivityResult(int request,int result,Intent data){super.onActivityResult(request,result,data);if(result!=RESULT_OK||data==null||data.getData()==null)return;Uri uri=data.getData();
        if(request==EXPORT_LOG){io.execute(()->{try(java.io.OutputStream output=getContentResolver().openOutputStream(uri,"w")){if(output==null)throw new java.io.IOException("无法打开目标文件");AppLog.export(output);runOnUiThread(()->toast("诊断包已保存"));}catch(Exception ex){AppLog.error("Diagnostic export failed",ex);runOnUiThread(()->toast("导出失败："+message(ex)));}});return;}
        if(request==PICK_STORAGE_TREE){if(StorageDirectories.overlapsSourceRoot(uri,db)){toast("存储目录不能与资源索引目录重叠");return;}try{getContentResolver().takePersistableUriPermission(uri,Intent.FLAG_GRANT_READ_URI_PERMISSION|Intent.FLAG_GRANT_WRITE_URI_PERMISSION);String name=DocumentsContract.getTreeDocumentId(uri);int slash=Math.max(name.lastIndexOf('/'),name.lastIndexOf(':'));if(slash>=0)name=name.substring(slash+1);if(name.isEmpty())name="本地目录";StorageDirectories.add(this,uri,name);render();toast("存储目录已添加");}catch(Exception e){toast("无法授权存储目录："+message(e));}return;}
        if(request!=PICK_TREE)return;if(StorageDirectories.overlapsConfigured(this,uri)){toast("索引目录不能与存储目录重叠");return;}try{getContentResolver().takePersistableUriPermission(uri,Intent.FLAG_GRANT_READ_URI_PERMISSION);String name=DocumentsContract.getTreeDocumentId(uri);int slash=Math.max(name.lastIndexOf('/'),name.lastIndexOf(':'));if(slash>=0)name=name.substring(slash+1);if(name.isEmpty())name="手机存储";db.addRoot(uri,name);home=false;tab=0;meta=new LibraryStore.Meta();entries.clear();render();scan(uri);}catch(Exception e){toast("无法授权目录："+message(e));}}
    private void scan(Uri uri){if(scanning)return;scanning=true;scanMessage="正在索引文件…";generation++;loading=true;updateResults();toast(scanMessage);io.execute(()->{try{int count=new TreeScanner(getContentResolver(),db).scan(uri);LocalIndexJobService.schedule(this,true);runOnUiThread(()->{if(destroyed)return;scanning=false;loading=false;scanMessage="已索引 "+count+" 个文件";thumbs.clear();toast(scanMessage);if(!home)reload(false,true);if(prefs.getBoolean("auto_local_thumbnails",true))handler.postDelayed(this::generateLocalThumbnails,250);});}catch(Exception e){runOnUiThread(()->{if(destroyed)return;scanning=false;loading=false;scanMessage="扫描失败："+message(e);error=scanMessage;toast(scanMessage);if(tab==0)updateResults();else render();});}});}
    private void generateLocalThumbnails(){startLocalThumbnailTask();}
    private void showDetail(LibraryStore.Entry e){
        LibraryStore s=store();if(s==null)return;MobileUi.Sheet sheet=ui.sheet(e.name,typeName(e.kind)+" · "+size(e.size));detailBody(sheet,s,e);sheet.show();
        io.execute(()->{try{LibraryStore.Entry fresh=s.detail(e);runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing())detailBody(sheet,s,fresh);});}catch(Exception failure){runOnUiThread(()->{if(!destroyed&&sheet.dialog.isShowing())toast(message(failure));});}});
    }
    private void detailBody(MobileUi.Sheet sheet,LibraryStore s,LibraryStore.Entry e){
        sheet.body.removeAllViews();sheet.footer.removeAllViews();FrameLayout frame=new FrameLayout(this);frame.setBackground(ui.shape(typeBg(e.kind),18,0));frame.setClipToOutline(true);ImageView img=new ImageView(this);img.setScaleType(ImageView.ScaleType.FIT_CENTER);frame.addView(img,new FrameLayout.LayoutParams(-1,-1));ImageView icon=ui.icon(e.kind,54,typeColor(e.kind));frame.addView(icon,new FrameLayout.LayoutParams(ui.d(54),ui.d(54),Gravity.CENTER));if(e.available&&(e.kind.equals("image")||e.kind.equals("video")))ui.touch(frame,Color.TRANSPARENT,18,()->openPreview(e));sheet.body.addView(frame,ui.lp(-1,190));thumbs.load(s,e,img,icon);
        if(s.remote&&(e.kind.equals("image")||e.kind.equals("video"))&&e.available){TextView generate=ui.chip("重新生成移动端封面",false,()->mutate(s,()->s.client.json("POST","/api/resources/"+RemoteClient.enc(e.id)+"/thumbnail?size=mobile",null),"移动端封面已生成",()->{thumbs.clear();detailBody(sheet,s,e);}));sheet.body.addView(generate,ui.top(8));}
        else if(!s.remote&&(e.kind.equals("image")||e.kind.equals("video")||e.mime.equals("application/pdf"))){sheet.body.addView(ui.chip("重新生成本机缩略图",false,()->{io.execute(()->{try{thumbs.generate(e,true);runOnUiThread(()->{if(destroyed)return;thumbs.clear();toast("缩略图已生成");if(sheet.dialog.isShowing())detailBody(sheet,s,e);});}catch(Exception ex){runOnUiThread(()->toast("生成失败："+message(ex)));}});}),ui.top(8));}
        if(e.kind.equals("video")&&e.available)sheet.body.addView(ui.item("video","分段截图","按视频时长分为 6 段，生成适合手机的截图",()->{sheet.close();showVideoCaptures(s,e);}),ui.top(12));
        section(sheet.body,"文件信息",null,null);detailLine(sheet.body,"大小",size(e.size));detailLine(sheet.body,"修改时间",dateTime(e.modified));detailLine(sheet.body,"文件路径",e.path);if(!e.available)detailLine(sheet.body,"状态","来源暂不可用");
        if(!s.remote){
            android.content.SharedPreferences nodeSettings=getSharedPreferences(ResourceNodeService.PREFS,MODE_PRIVATE);
            if(nodeSettings.getBoolean("original_upload_enabled",false)&&hubStore!=null){
                sheet.body.addView(ui.item("cloud","上传原件到云层","经直连查询节点加密保存，读回验证后可释放本机空间",()->{sheet.close();startOriginalUpload(e);}),ui.top(12));
                JSONObject upload=completedUpload(e.id);
                if(upload!=null)sheet.body.addView(ui.item("spark","释放本机空间","两份独立故障域保存且恢复密钥可恢复后，删除本机原件并保留分类",()->{sheet.close();confirmRelease(e,upload);}),ui.top(10));
            }
            sheet.body.addView(ui.item("cloud","原件入云任务","查看上传与释放进度及失败原因",()->{sheet.close();originalTasksSheet();}),ui.top(10));
        }
        if(s.remote){section(sheet.body,"云层副本",null,null);TextView cloudState=ui.text("正在读取云层状态…",12,MUTED,false);sheet.body.addView(cloudState,ui.top(8));loadCloudReplicaStatus(s,e,cloudState);}
        section(sheet.body,"手动标签","添加",()->tagPrompt(Collections.singletonList(e.id),()->refreshDetail(sheet,s,e)));
        if(e.tags.isEmpty())sheet.body.addView(ui.text("加一个标签，下次更容易找到。",12,MUTED,false),ui.top(9));else{MobileUi.Flow flow=ui.new Flow();for(String t:e.tags)flow.addView(ui.chip(t+"  ×",true,()->confirm("移除标签“"+t+"”？",e.name,()->mutate(s,()->s.removeTag(Collections.singletonList(e.id),t),"标签已移除",()->refreshDetail(sheet,s,e)))));sheet.body.addView(flow,ui.top(10));}
        sheet.body.addView(ui.item("folder","归入文件夹",folderName(e.folderId),()->{sheet.close();moveSheet(Collections.singletonList(e.id));}),ui.top(20));
        if(s.remote){sheet.body.addView(ui.item("spark","获取建议标签","根据服务端的资源信息获取建议",()->suggestTags(sheet,s,e)),ui.top(10));}
        TextView open=ui.button("全屏预览",true,()->{sheet.close();openPreview(e);});open.setEnabled(e.available);open.setAlpha(e.available?1:.45f);sheet.footer.addView(open,ui.top(8));
        if(s.remote)sheet.footer.addView(ui.button("下载原文件",false,()->download(s,e)),ui.top(8));
    }
    private void loadCloudReplicaStatus(LibraryStore s,LibraryStore.Entry e,TextView target){io.execute(()->{String status;try{JSONArray files=s.client.json("GET","/api/cloud/files",null).optJSONArray("files");JSONObject match=null;if(files!=null)for(int i=0;i<files.length();i++){JSONObject file=files.optJSONObject(i);if(file!=null&&(e.id.equals(file.optString("id"))||e.id.equals(file.optString("sourceId")))){match=file;break;}}status=match==null?"未加入云层":match.optInt("replicas",match.optInt("minReplicas"))+" 份副本 · "+match.optString("availableReplicas","—")+" 份可用（端点探测）";}catch(Exception ex){status="云层状态暂不可用";}String result=status;runOnUiThread(()->{if(!destroyed)target.setText(result);});});}
    private void startOriginalUpload(LibraryStore.Entry e){
        if(hubStore==null){toast("请先连接 Hub 并选择查询节点");return;}
        String node=hubStore.client.selectedNodeId;
        if(node.isEmpty()){toast("请先选择一个在线查询节点");return;}
        LocalLibrary.Item item;try(LocalLibrary library=new LocalLibrary(this)){item=library.item(e.id);}
        if(item==null||item.logicalFileId==null||item.logicalFileId.isEmpty()||item.revisionId==null||item.revisionId.isEmpty()){toast("该文件尚未建立稳定身份，请先完成索引同步");return;}
        Intent intent=new Intent(this,OriginalUploadService.class);
        intent.putExtra("uri",e.id).putExtra("name",e.name).putExtra("fileId",item.logicalFileId).putExtra("revisionId",item.revisionId)
            .putExtra("base",hubStore.client.base).putExtra("node",node).putExtra("nodeId",ResourceNodeService.ensureID(this))
            .putExtra("storageId",item.rootUri==null?"":item.rootUri).putExtra("resourceRef",NodeResourceApi.id(e.id))
            .putExtra("total",item.size).putExtra("modified",java.time.Instant.ofEpochMilli(item.modified).toString());
        try{startForegroundService(intent);toast("已加入原件入云任务");}catch(Exception ex){toast("无法启动上传："+message(ex));}
    }
    private JSONObject completedUpload(String uri){JSONArray items=OriginalUploadService.records(this);for(int i=0;i<items.length();i++){JSONObject task=items.optJSONObject(i);if(task!=null&&uri.equals(task.optString("uri"))&&"已完成".equals(task.optString("status"))&&!task.optString("locationId").isEmpty())return task;}return null;}
    private void confirmRelease(LibraryStore.Entry e,JSONObject upload){
        confirm("释放本机空间？","将删除这台手机上的原件“"+e.name+"”，分类与离线元数据保留。需要两份独立故障域的安全保存与独立恢复密钥证明；删除前会再次复核原件版本，系统拒绝删除时保留位置。",()->{
            if(hubStore==null){toast("请先连接 Hub");return;}
            Intent intent=new Intent(this,OriginalUploadService.class);intent.setAction("release");
            intent.putExtra("uri",e.id).putExtra("name",e.name).putExtra("fileId",upload.optString("fileId")).putExtra("revisionId",upload.optString("revisionId"))
                .putExtra("base",hubStore.client.base).putExtra("node",hubStore.client.selectedNodeId).putExtra("locationId",upload.optString("locationId"));
            try{startForegroundService(intent);toast("已开始安全释放检查");}catch(Exception ex){toast("无法启动释放："+message(ex));}
        });
    }
    private void originalTasksSheet(){
        MobileUi.Sheet sheet=ui.sheet("原件入云任务","上传经 WebRTC 直连分块送到查询节点加密保存；释放先复核安全保存与版本，再删除本机原件。");
        JSONArray uploads=OriginalUploadService.records(this),releases=OriginalUploadService.releaseRecords(this);
        if(uploads.length()==0&&releases.length()==0)sheet.body.addView(ui.text("暂无任务。在本地文件详情中启用并点击“上传原件到云层”。",12,MUTED,false),ui.top(10));
        for(int i=0;i<uploads.length();i++){JSONObject task=uploads.optJSONObject(i);if(task==null)continue;String id=task.optString("id"),status=task.optString("status"),error=task.optString("error");long total=task.optLong("total",0),uploaded=task.optLong("uploaded",0);
            LinearLayout card=ui.card();card.addView(ui.text(task.optString("name",id),14,INK,true));card.addView(ui.text("上传 · "+status+(total>0?" · "+size(uploaded)+" / "+size(total):"")+(error.isEmpty()?"":"\n"+error),12,"失败".equals(status)||"已取消".equals(status)?RED:MUTED,false),ui.top(6));
            if("等待上传".equals(status)||"上传中".equals(status)||"验证中".equals(status))card.addView(ui.button("取消",false,()->{Intent cancel=new Intent(this,OriginalUploadService.class);cancel.setAction("cancel");cancel.putExtra("id",id);startService(cancel);sheet.close();handler.postDelayed(this::originalTasksSheet,300);}),ui.top(8));
            else if("失败".equals(status)||"已取消".equals(status))card.addView(ui.button("重试",false,()->{Intent retry=new Intent(this,OriginalUploadService.class);retry.setAction("retry");retry.putExtra("id",id);try{startForegroundService(retry);sheet.close();handler.postDelayed(this::originalTasksSheet,300);}catch(Exception ex){toast("重试失败："+message(ex));}}),ui.top(8));
            sheet.body.addView(card,ui.top(10));}
        for(int i=0;i<releases.length();i++){JSONObject task=releases.optJSONObject(i);if(task==null)continue;String status=task.optString("status"),error=task.optString("error");
            LinearLayout card=ui.card();card.addView(ui.text(task.optString("name",task.optString("fileId")),14,INK,true));card.addView(ui.text("释放 · "+status+(error.isEmpty()?"":"\n"+error),12,"失败".equals(status)||"已取消".equals(status)?RED:MUTED,false),ui.top(6));sheet.body.addView(card,ui.top(10));}
        sheet.footer.addView(ui.button("刷新",false,()->{sheet.close();originalTasksSheet();}),ui.top(8));sheet.show();
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
        boolean selfNode=s.remote&&s.client.selectedNodeId.equals(ResourceNodeService.ensureID(this));
        String localUri=selfNode?indexedSelfNodeUri(e.id):"";
        boolean remotePreview=s.remote&&localUri.isEmpty();
        Intent intent=new Intent(this,PreviewActivity.class);
        intent.putExtra("uri",remotePreview?s.client.base+"/api/resources/"+RemoteClient.enc(e.id)+"/file":s.remote?localUri:e.id);
        intent.putExtra("name",e.name).putExtra("mime",e.mime).putExtra("size",e.size);
        if(remotePreview){intent.putExtra("server",s.client.base).putExtra("resource",e.id);for(int i=0;i<s.client.nodes.length();i++){JSONObject node=s.client.nodes.optJSONObject(i);if(node!=null&&s.client.selectedNodeId.equals(node.optString("id"))){intent.putExtra("source_node_kind",node.optString("kind"));break;}}}
        ArrayList<String> ids=new ArrayList<>(),names=new ArrayList<>(),mimes=new ArrayList<>();ArrayList<Long> sizes=new ArrayList<>();
        for(LibraryStore.Entry item:entries)if(item.available&&item.kind.equals(e.kind)&&(item.kind.equals("image")||item.kind.equals("video"))){String id=remotePreview?item.id:selfNode?indexedSelfNodeUri(item.id):item.id;if(!id.isEmpty()){ids.add(id);names.add(item.name);mimes.add(item.mime);sizes.add(item.size);}}
        int browseAt=ids.indexOf(remotePreview?e.id:selfNode?localUri:e.id);
        if(browseAt>=0){intent.putStringArrayListExtra("browse_ids",ids);intent.putStringArrayListExtra("browse_names",names);intent.putStringArrayListExtra("browse_mimes",mimes);long[] browseSizes=new long[sizes.size()];for(int i=0;i<sizes.size();i++)browseSizes[i]=sizes.get(i);intent.putExtra("browse_sizes",browseSizes);intent.putExtra("browse_index",browseAt);}
        startActivity(intent);
    }
    private String indexedSelfNodeUri(String encodedId){String uri=NodeResourceApi.decodeID(encodedId);return !uri.isEmpty()&&db.item(uri)!=null?uri:"";}
    private void entryMenu(LibraryStore.Entry e){MobileUi.Sheet s=ui.sheet(e.name,typeName(e.kind));s.body.addView(ui.item("file","文件详情",null,()->{s.close();showDetail(e);}));if(!store().remote)s.body.addView(ui.item("file","修改显示名称","不修改物理文件名",()->prompt("修改显示名称","不修改物理文件名",e.name,name->{if(name.trim().isEmpty())return;s.close();mutate(localStore,()->{LocalLibrary.Item item=db.item(e.id);if(item==null)throw new IllegalStateException("文件不存在");db.renameLogicalFile(item.logicalFileId,name,false);},"已修改显示名称，等待同步",()->reload(false,true));})),ui.top(7));s.body.addView(ui.item("tag","添加标签",null,()->{s.close();tagPrompt(Collections.singletonList(e.id),null);}));s.body.addView(ui.item("folder","归入文件夹",null,()->{s.close();moveSheet(Collections.singletonList(e.id));}));s.body.addView(ui.item("check","选择文件",null,()->{s.close();selecting=true;toggleSelection(e.id);}));s.show();}
    private void tagPrompt(List<String> ids,Runnable after){if(ids.isEmpty())return;LibraryStore s=store();prompt("添加标签",ids.size()==1?"例如：旅行、工作":"给 "+ids.size()+" 个文件添加标签","",name->{String clean=LibraryStore.clean(name,s.remote?40:64);mutate(s,()->s.addTag(ids,clean),"标签已添加",after);});}
    private void moveSheet(List<String> ids){if(ids.isEmpty())return;LibraryStore st=store();MobileUi.Sheet s=ui.sheet("归入文件夹",ids.size()+" 个文件 · 原文件位置不变");s.body.addView(ui.item("download","未归组",null,()->{s.close();mutate(st,()->st.move(ids,"unfiled"),"已取消文件归组，原文件位置不变",null);}));for(LibraryStore.Group g:meta.folders)s.body.addView(ui.item("folder",g.name,g.count+" 个文件",()->{s.close();mutate(st,()->st.move(ids,g.id),"文件已归组，原文件位置不变",null);}),ui.top(6));if(meta.folders.isEmpty())s.body.addView(ui.text("还没有自定义文件夹，可以在“文件夹”页创建。",12,MUTED,false),ui.top(20));s.show();}
    private void download(LibraryStore s,LibraryStore.Entry e){
        if(s==null||!s.remote)return;
        Intent transfer=new Intent(this,ResourceTransferService.class);transfer.putExtra("base",s.client.base).putExtra("resource",e.id).putExtra("name",e.name).putExtra("total",e.size).putExtra("node",s.client.selectedNodeId);
        try{startForegroundService(transfer);toast("已加入传输列表");if(tab==3)handler.postDelayed(this::renderTransfers,400);}catch(Exception ex){toast("下载失败："+message(ex));}
    }
    private JSONArray transfers(){try{return new JSONArray(prefs.getString("transfers","[]"));}catch(Exception ignored){return new JSONArray();}}
    private void renderTransfers(){
        if(tab!=3)return;int scrollPosition=content.getChildCount()>0&&content.getChildAt(0) instanceof ScrollView?content.getChildAt(0).getScrollY():0;content.removeAllViews();ScrollView scroll=new ScrollView(this);scroll.setFillViewport(true);LinearLayout body=ui.col();body.setPadding(ui.d(20),0,ui.d(20),ui.d(24));scroll.addView(body);content.addView(scroll,new LinearLayout.LayoutParams(-1,-1));scroll.post(()->scroll.scrollTo(0,scrollPosition));
        if(hubStore!=null&&hubStore.remote){body.addView(ui.button("节点间互传",true,this::nodeTransferSheet),ui.top(8));body.addView(ui.text("在手机和其他在线资源节点之间直接复制文件，不加入资源索引。",12,MUTED,false),ui.top(6));}
        body.addView(ui.text("下载到手机的文件",13,MUTED,false),ui.top(6));JSONArray saved=transfers();
        JSONArray direct=ResourceTransferService.records(this);
        if(saved.length()==0&&direct.length()==0){LinearLayout empty=ui.card();empty.addView(ui.icon("download",36,GREEN));empty.addView(ui.text("暂无传输任务",18,INK,true),ui.top(16));empty.addView(ui.text("从 Hub 资源节点打开文件详情，即可下载原文件。",13,MUTED,false),ui.top(8));body.addView(empty,ui.top(20));return;}
        for(int i=direct.length()-1;i>=0;i--){JSONObject item=direct.optJSONObject(i);if(item==null)continue;String id=item.optString("id"),name=item.optString("name"),state=item.optString("status");long downloaded=item.optLong("downloaded"),totalBytes=item.optLong("total",-1);
            LinearLayout card=ui.card();TextView title=ui.text(name,15,INK,true);title.setMaxLines(2);card.addView(title);String detail=state+" · "+size(downloaded)+(totalBytes>0?" / "+size(totalBytes):"");if(state.equals("失败")&&!item.optString("error").isEmpty())detail+=" · "+item.optString("error");card.addView(ui.text(detail,12,MUTED,false),ui.top(7));
            if(state.equals("等待下载")||state.equals("下载中")){ProgressBar bar=new ProgressBar(this,null,android.R.attr.progressBarStyleHorizontal);bar.setMax(100);bar.setIndeterminate(totalBytes<=0);if(totalBytes>0)bar.setProgress((int)Math.min(100,downloaded*100/totalBytes));card.addView(bar,ui.top(12));card.addView(ui.button("取消下载",false,()->{Intent cancel=new Intent(this,ResourceTransferService.class);cancel.setAction("cancel");cancel.putExtra("id",id);startService(cancel);}),ui.top(12));}
            else if(state.equals("已完成")&&item.optString("uri").startsWith("content:"))card.addView(ui.button("打开文件",false,()->{Intent open=new Intent(Intent.ACTION_VIEW);open.setDataAndType(Uri.parse(item.optString("uri")),"*/*");open.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);try{startActivity(Intent.createChooser(open,"打开下载文件"));}catch(Exception ex){toast("没有可用的应用打开此文件");}}),ui.top(12));
            else if((state.equals("失败")||state.equals("已取消"))&&!item.optString("resource").isEmpty()){
                LinearLayout actions=ui.row();actions.addView(ui.button("重试",true,()->{Intent retry=new Intent(this,ResourceTransferService.class);retry.setAction("retry");retry.putExtra("id",id);try{startForegroundService(retry);handler.postDelayed(this::renderTransfers,250);}catch(Exception ex){toast("重试失败："+message(ex));}}),new LinearLayout.LayoutParams(0,ui.d(48),1));
                LinearLayout.LayoutParams remove=new LinearLayout.LayoutParams(0,ui.d(48),1);remove.leftMargin=ui.d(8);actions.addView(ui.button("删除记录",false,()->{ResourceTransferService.remove(this,id);renderTransfers();}),remove);card.addView(actions,ui.top(12));
            }else{if(state.equals("失败")||state.equals("已取消"))card.addView(ui.text("旧版记录缺少下载来源，请从文件详情重新发起。",11,MUTED,false),ui.top(10));card.addView(ui.button("移出列表",false,()->{ResourceTransferService.remove(this,id);renderTransfers();}),ui.top(12));}body.addView(card,ui.top(12));
        }
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
    private static final class NativeTransferFile {
        final String name,path,fileId,revisionId,folderName;final long size;final List<String> tags=new ArrayList<>();
        NativeTransferFile(JSONObject value){name=value.optString("name");path=value.optString("path");size=value.optLong("size");fileId=value.optString("fileId");revisionId=value.optString("revisionId");folderName=value.optString("folderName");JSONArray values=value.optJSONArray("tags");if(values!=null)for(int i=0;i<values.length();i++){String tag=values.optString(i);if(!tag.isEmpty())tags.add(tag);}}
    }
    private final class NativeTransferJob {
        final RemoteClient client;final String originalNode;final AtomicBoolean cancelled=new AtomicBoolean(),released=new AtomicBoolean();
        final List<JSONObject> nodes=new ArrayList<>();final List<NativeTransferFile> files=new ArrayList<>();
        NodeTransferConnection source,target;String sourceId="",sourceName="",targetId="",targetName="",targetDirectory="";volatile boolean running,transitioning;
        NativeTransferJob(RemoteClient client,String originalNode){this.client=client;this.originalNode=originalNode;}
    }
    private void nodeTransferSheet(){
        if(hubStore==null||!hubStore.remote){toast("请先连接 Hub");return;}
        NativeTransferJob job=new NativeTransferJob(hubStore.client,hubStore.client.selectedNodeId);activeNativeTransfer=job;
        MobileUi.Sheet loading=ui.sheet("节点间互传","读取在线资源节点中…");loading.footer.addView(ui.button("取消",false,()->{job.cancelled.set(true);loading.close();releaseNativeTransfer(job);}));loading.show();
        io.execute(()->{try{JSONObject session=job.client.json("GET","/api/session",null);JSONArray nodes=session.optJSONArray("nodes");if(nodes!=null)for(int i=0;i<nodes.length();i++){JSONObject node=nodes.optJSONObject(i);if(node!=null&&node.optBoolean("online"))job.nodes.add(node);}runOnUiThread(()->{if(destroyed||job.cancelled.get())return;loading.close();if(job.nodes.size()<2)showNativeTransferMessage("至少需要两个在线资源节点才能互传。",job);else chooseNativeTransferNode(job,true);});}catch(Exception failure){runOnUiThread(()->{loading.close();if(!job.cancelled.get())showNativeTransferMessage("无法读取在线节点："+message(failure),job);});}});
    }
    private void chooseNativeTransferNode(NativeTransferJob job,boolean source){
        MobileUi.Sheet sheet=ui.sheet(source?"选择来源节点":"选择目标节点",source?"选择要发送文件的在线节点":"选择接收文件的在线节点");
        for(JSONObject node:job.nodes){String id=node.optString("id");if(!source&&id.equals(job.sourceId))continue;String name=node.optString("name",id),kind=node.optString("kind");sheet.body.addView(ui.item("android".equals(kind)?"phone":"server",name,"android".equals(kind)?"手机资源节点":"桌面资源节点",()->{job.transitioning=true;sheet.close();job.transitioning=false;if(source){job.sourceId=id;job.sourceName=name;browseNativeTransfer(job,true,"");}else{job.targetId=id;job.targetName=name;browseNativeTransfer(job,false,"");}}),ui.top(8));}
        sheet.footer.addView(ui.button("取消互传",false,()->{sheet.close();cancelNativeTransfer(job);}));setNativeTransferDismiss(job,sheet);sheet.show();
    }
    private void browseNativeTransfer(NativeTransferJob job,boolean source,String path){
        MobileUi.Sheet loading=ui.sheet("读取节点目录",source?job.sourceName:job.targetName);loading.body.addView(ui.text("正在连接并读取目录…",13,MUTED,false));loading.footer.addView(ui.button("取消互传",false,()->{loading.close();cancelNativeTransfer(job);}));loading.show();
        io.execute(()->{try{NodeTransferConnection connection=source?job.source:job.target;if(connection==null){connection=NodeTransferConnection.open(this,job.client,source?job.sourceId:job.targetId);if(job.cancelled.get()){connection.close();return;}if(source)job.source=connection;else job.target=connection;}String route="/api/transfers/browse"+(path.isEmpty()?"":"?path="+Uri.encode(path));JSONObject dir=transferJson(connection.request("GET",route,new byte[0],job.cancelled),"读取目录失败");runOnUiThread(()->{if(destroyed||job.cancelled.get()){releaseNativeTransfer(job);return;}loading.close();showNativeTransferBrowser(job,source,dir);});}catch(Exception failure){runOnUiThread(()->{loading.close();if(!job.cancelled.get())showNativeTransferMessage("无法读取节点目录："+message(failure),job);else releaseNativeTransfer(job);});}});
    }
    private JSONObject transferJson(NodeTransferConnection.Reply reply,String fallback)throws Exception{if(reply.status<200||reply.status>=300){String error=fallback+"（"+reply.status+"）";try{error=new JSONObject(new String(reply.body,java.nio.charset.StandardCharsets.UTF_8)).optString("error",error);}catch(Exception ignored){}throw new java.io.IOException(error);}return new JSONObject(new String(reply.body,java.nio.charset.StandardCharsets.UTF_8));}
    private void showNativeTransferBrowser(NativeTransferJob job,boolean source,JSONObject directory){
        String current=directory.optString("current"),parent=directory.optString("parent");MobileUi.Sheet sheet=ui.sheet(source?"选择要传输的文件":"选择保存目录",(source?job.sourceName:job.targetName)+(current.isEmpty()?" · 选择资源根目录":" · "+directory.optString("currentName",current)));
        if(!parent.isEmpty())sheet.body.addView(ui.button("上一级",false,()->{job.transitioning=true;sheet.close();job.transitioning=false;browseNativeTransfer(job,source,parent);}),ui.top(2));
        JSONArray roots=directory.optJSONArray("roots");if(roots!=null)for(int i=0;i<roots.length();i++){JSONObject root=roots.optJSONObject(i);if(root!=null)addNativeTransferFolder(job,sheet,source,root);}
        JSONArray entries=directory.optJSONArray("entries");int visible=0;if(entries!=null)for(int i=0;i<entries.length();i++){JSONObject entry=entries.optJSONObject(i);if(entry==null)continue;if(entry.optBoolean("dir")){visible++;addNativeTransferFolder(job,sheet,source,entry);}else if(source){visible++;addNativeTransferFile(job,sheet,entry);}}
        if((roots==null||roots.length()==0)&&visible==0)sheet.body.addView(ui.text(current.isEmpty()?"该节点尚未添加资源根目录。":source?"此目录没有可传输的文件或子目录。":"此目录没有子目录，可保存到当前目录。",12,MUTED,false),ui.top(10));
        if(source)sheet.footer.addView(ui.button("继续 · 已选 "+job.files.size()+" 个文件",true,()->{if(job.files.isEmpty()){toast("请至少选择一个文件");return;}job.transitioning=true;sheet.close();job.transitioning=false;chooseNativeTransferNode(job,false);}),ui.top(4));
        else if(!current.isEmpty())sheet.footer.addView(ui.button("保存到此目录",true,()->{job.targetDirectory=current;job.transitioning=true;sheet.close();job.transitioning=false;startNativeTransfer(job);}),ui.top(4));
        sheet.footer.addView(ui.button("取消互传",false,()->{sheet.close();cancelNativeTransfer(job);}));setNativeTransferDismiss(job,sheet);sheet.show();
    }
    private void addNativeTransferFolder(NativeTransferJob job,MobileUi.Sheet sheet,boolean source,JSONObject folder){String path=folder.optString("path");if(path.isEmpty())return;sheet.body.addView(ui.item("folder",folder.optString("name","资源目录"),"打开目录",()->{job.transitioning=true;sheet.close();job.transitioning=false;browseNativeTransfer(job,source,path);}),ui.top(7));}
    private void addNativeTransferFile(NativeTransferJob job,MobileUi.Sheet sheet,JSONObject value){NativeTransferFile file=new NativeTransferFile(value);if(file.path.isEmpty())return;CheckBox check=new CheckBox(this);check.setText(file.name+" · "+size(file.size));check.setTextColor(INK);check.setTextSize(13);check.setButtonTintList(android.content.res.ColorStateList.valueOf(GREEN));check.setChecked(job.files.stream().anyMatch(item->item.path.equals(file.path)));check.setOnCheckedChangeListener((button,checked)->{job.files.removeIf(item->item.path.equals(file.path));if(checked)job.files.add(file);});sheet.body.addView(check,ui.top(3));}
    private void startNativeTransfer(NativeTransferJob job){
        MobileUi.Sheet progress=ui.sheet("正在进行节点互传","Hub 负责连接协调，文件内容通过节点间 WebRTC 传输");TextView state=ui.text("准备传输…",13,MUTED,false);progress.body.addView(state);ProgressBar bar=new ProgressBar(this,null,android.R.attr.progressBarStyleHorizontal);bar.setMax(100);progress.body.addView(bar,ui.top(12));TextView cancel=ui.button("取消传输",false,()->job.cancelled.set(true));progress.footer.addView(cancel);progress.dialog.setCancelable(false);progress.show();job.running=true;long total=0;for(NativeTransferFile file:job.files)total+=file.size;final long totalBytes=total;
        io.execute(()->{String failure="";long completed=0;try{if(job.source==null){NodeTransferConnection connection=NodeTransferConnection.open(this,job.client,job.sourceId);if(job.cancelled.get()){connection.close();throw new java.io.IOException("传输已取消");}job.source=connection;}checkNativeTransfer(job);if(job.target==null){NodeTransferConnection connection=NodeTransferConnection.open(this,job.client,job.targetId);if(job.cancelled.get()){connection.close();throw new java.io.IOException("传输已取消");}job.target=connection;}checkNativeTransfer(job);
            for(NativeTransferFile file:job.files){checkNativeTransfer(job);String targetPath=nativeTransferTargetPath(job.targetDirectory,file.name),transferId=UUID.randomUUID().toString().replace("-","");long offset=0;try{
                if(file.size==0){NodeTransferConnection.Reply written=job.target.request("PUT",nativeTransferFileQuery(targetPath,transferId,0,0,true,file),new byte[0],job.cancelled);if(written.status<200||written.status>=300)throw new java.io.IOException("创建目标文件失败（"+written.status+"）");}
                while(offset<file.size){checkNativeTransfer(job);int length=(int)Math.min(1<<20,file.size-offset);String readPath="/api/transfers/file?path="+Uri.encode(file.path)+"&offset="+offset+"&length="+length;NodeTransferConnection.Reply chunk=job.source.request("GET",readPath,new byte[0],job.cancelled);if(chunk.status!=200)throw new java.io.IOException("读取来源文件失败（"+chunk.status+"）");if(chunk.body.length!=length)throw new java.io.IOException("来源文件在传输时发生变化，已停止以避免文件不完整");boolean last=offset+length==file.size;NodeTransferConnection.Reply written=job.target.request("PUT",nativeTransferFileQuery(targetPath,transferId,offset,file.size,last,file),chunk.body,job.cancelled);if(written.status<200||written.status>=300)throw new java.io.IOException("写入目标文件失败（"+written.status+"）");offset+=length;long current=completed+offset;String label=file.name+" · "+size(current)+" / "+size(totalBytes);runOnUiThread(()->{if(!destroyed){state.setText(label);bar.setProgress(totalBytes==0?100:(int)Math.min(100,current*100/totalBytes));}});}
            }catch(Exception transferFailure){try{job.target.request("DELETE","/api/transfers/file?path="+Uri.encode(targetPath)+"&id="+transferId,new byte[0],null);}catch(Exception ignored){}throw transferFailure;}completed+=file.size;}
        }catch(Exception ex){failure=ex.getMessage()==null?"节点互传失败":ex.getMessage();}finally{job.running=false;releaseNativeTransfer(job);}
            String result=job.cancelled.get()?(failure.isEmpty()?"传输已取消，未完成的临时文件已清理":failure):failure.isEmpty()?"文件已传输完成":"传输失败："+failure;String finalFailure=failure;runOnUiThread(()->{if(destroyed)return;job.transitioning=true;progress.close();job.transitioning=false;MobileUi.Sheet done=ui.sheet(job.cancelled.get()?"传输已取消":finalFailure.isEmpty()?"传输完成":"传输失败",result);done.footer.addView(ui.button("完成",true,done::close));done.show();});
        });
    }
    private static String nativeTransferFileQuery(String path,String id,long offset,long total,boolean last,NativeTransferFile file){String query="/api/transfers/file?path="+Uri.encode(path)+"&id="+id+"&offset="+offset+"&total="+total+(last?"&final=1":"");if(!last||file.fileId.isEmpty()||file.revisionId.isEmpty())return query;query+="&fileId="+Uri.encode(file.fileId)+"&revisionId="+Uri.encode(file.revisionId);if(!file.folderName.isEmpty())query+="&folderName="+Uri.encode(file.folderName);for(String tag:file.tags)query+="&tag="+Uri.encode(tag);return query;}
    private static String nativeTransferTargetPath(String directory,String name){if(directory.startsWith("safdir:")){String token=android.util.Base64.encodeToString(name.getBytes(java.nio.charset.StandardCharsets.UTF_8),android.util.Base64.URL_SAFE|android.util.Base64.NO_WRAP|android.util.Base64.NO_PADDING);return "safout:"+directory.substring(7)+":"+token;}String separator=directory.contains("\\")?"\\":"/";return directory.endsWith("/")||directory.endsWith("\\")?directory+name:directory+separator+name;}
    private void checkNativeTransfer(NativeTransferJob job)throws java.io.IOException{if(job.cancelled.get())throw new java.io.IOException("传输已取消");}
    private void showNativeTransferMessage(String message,NativeTransferJob job){MobileUi.Sheet sheet=ui.sheet("节点间互传",message);sheet.footer.addView(ui.button("关闭",true,()->{sheet.close();cancelNativeTransfer(job);}));setNativeTransferDismiss(job,sheet);sheet.show();}
    private void setNativeTransferDismiss(NativeTransferJob job,MobileUi.Sheet sheet){sheet.dialog.setOnDismissListener(dialog->{if(!job.transitioning&&!job.running)cancelNativeTransfer(job);});}
    private void cancelNativeTransfer(NativeTransferJob job){job.cancelled.set(true);releaseNativeTransfer(job);}
    private void releaseNativeTransfer(NativeTransferJob job){if(job==null)return;if(job.source!=null)job.source.close();if(job.target!=null)job.target.close();if(job.released.compareAndSet(false,true)){if(activeNativeTransfer==job)activeNativeTransfer=null;if(!job.originalNode.isEmpty())io.execute(()->{try{job.client.selectNode(job.originalNode);}catch(Exception ignored){}});}}
    private void removeTransfer(long id){JSONArray old=transfers(),next=new JSONArray();for(int i=0;i<old.length();i++){JSONObject item=old.optJSONObject(i);if(item!=null&&item.optLong("id")!=id)next.put(item);}prefs.edit().putString("transfers",next.toString()).apply();}
    private interface Work{void run()throws Exception;}
    private void mutate(LibraryStore target,Work work,String success,Runnable after){if(target==null||busy||scanning){toast("请等待当前操作完成");return;}busy=true;renderBatch();io.execute(()->{try{work.run();if(!target.remote)LocalIndexJobService.schedule(this,true);runOnUiThread(()->{busy=false;if(destroyed)return;toast(success);if(target==store()){selected.clear();selecting=false;if(after!=null)after.run();reload(false,true);}});}catch(Exception e){runOnUiThread(()->{busy=false;if(destroyed)return;toast(message(e));renderBatch();});}});}
    private interface TextAction{void run(String s);}
    private void prompt(String title,String hint,String initial,TextAction action){MobileUi.Sheet s=ui.sheet(title,null);EditText input=ui.input(hint,initial);s.body.addView(input);TextView error=ui.text("",12,RED,false);s.body.addView(error,ui.top(8));s.footer.addView(ui.button("保存",true,()->{try{String value=LibraryStore.clean(input.getText().toString(),64);action.run(value);s.close();hideKeyboard();}catch(Exception e){error.setText(message(e));}}));s.show();input.requestFocus();}
    private void confirm(String title,String description,Runnable action){MobileUi.Sheet s=ui.sheet(title,description);s.body.addView(ui.icon("shield",42,GREEN));s.footer.addView(ui.button("确认",true,()->{s.close();action.run();}));s.footer.addView(ui.button("取消",false,s::close),ui.top(8));s.show();}
    private String folderName(String id){if(id==null||id.isEmpty()||id.equals("unfiled"))return "未归组";if(id.equals("all"))return "全部文件";LibraryStore.Group g=find(meta.folders,id);return g==null?"文件夹":g.name;}
    private static LibraryStore.Group find(List<LibraryStore.Group> groups,String id){for(LibraryStore.Group g:groups)if(g.id.equals(id))return g;return null;}
    private String collectionSummary(LocalFilters value){ArrayList<String> parts=new ArrayList<>();if(!value.query.isEmpty())parts.add("“"+value.query+"”");parts.addAll(value.tags);for(String key:value.attributes)parts.add(LocalLibrary.attributeLabel(key));if(value.unorganized)parts.add("未添加标签");return parts.isEmpty()?"全部文件 · 动态更新":TextUtils.join(" · ",parts);}
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
    private void handleBack(){if(storageOnly){finish();return;}if(selecting){selected.clear();selecting=false;updateResults();}else if(tab==3)navigate(2);else if(tab!=0)navigate(0);else if(!f().folderId.equals("all"))openFolder("all");else finish();}
    // API 33+ uses the platform back dispatcher registered in onCreate; this is the older-device fallback.
    @android.annotation.SuppressLint("GestureBackNavigation")
    @Override public void onBackPressed(){handleBack();}
    @Override protected void onSaveInstanceState(Bundle b){b.putBoolean("home",home);b.putInt("tab",tab);b.putString("local",localFilters.serialize());b.putString("hub",hubFilters.serialize());b.putString("collection",activeCollection);super.onSaveInstanceState(b);}
    @Override protected void onDestroy(){destroyed=true;generation++;handler.removeCallbacksAndMessages(null);NativeTransferJob transfer=activeNativeTransfer;if(transfer!=null)cancelNativeTransfer(transfer);thumbs.close();if(hubStore!=null){RemoteClient previous=hubStore.client;new Thread(previous::close,"resource-rtc-dispose").start();}io.execute(db::close);io.shutdown();super.onDestroy();}
}
