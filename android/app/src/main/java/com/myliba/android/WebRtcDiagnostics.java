package com.myliba.android;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import org.json.JSONArray;
import org.json.JSONObject;
import org.webrtc.PeerConnection;
import org.webrtc.RTCStats;
import org.webrtc.RTCStatsReport;
import java.net.Inet4Address;
import java.net.Inet6Address;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;

/** Privacy-safe WebRTC diagnostics: transport, candidate type and address family, never addresses or credentials. */
final class WebRtcDiagnostics {
    private WebRtcDiagnostics(){}

    static String network(Context context){
        try{
            ConnectivityManager manager=context.getSystemService(ConnectivityManager.class);Network active=manager==null?null:manager.getActiveNetwork();
            NetworkCapabilities caps=active==null||manager==null?null:manager.getNetworkCapabilities(active);if(caps==null)return "network=unknown";
            StringBuilder type=new StringBuilder();append(type,caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR),"cellular");append(type,caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI),"wifi");append(type,caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET),"ethernet");append(type,caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN),"vpn");if(type.length()==0)type.append("other");
            boolean ipv4=false,ipv6=false;LinkProperties links=manager.getLinkProperties(active);
            if(links!=null)for(android.net.LinkAddress address:links.getLinkAddresses()){ipv4|=address.getAddress() instanceof Inet4Address;ipv6|=address.getAddress() instanceof Inet6Address;}
            return "network="+type+" validated="+caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED)+" internet="+caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)+" metered="+manager.isActiveNetworkMetered()+" ipv4="+ipv4+" ipv6="+ipv6;
        }catch(Exception ex){return "network=unavailable "+ex.getClass().getSimpleName();}
    }
    private static void append(StringBuilder out,boolean enabled,String value){if(enabled){if(out.length()>0)out.append('+');out.append(value);}}

    static String servers(JSONArray servers){int stun=0,turn=0,turns=0;boolean tcp=false;
        if(servers!=null)for(int i=0;i<servers.length();i++){JSONObject server=servers.optJSONObject(i);if(server==null)continue;Object raw=server.opt("urls");if(raw instanceof JSONArray){JSONArray urls=(JSONArray)raw;for(int j=0;j<urls.length();j++){String u=urls.optString(j).toLowerCase(Locale.ROOT);if(u.startsWith("stun:")||u.startsWith("stuns:"))stun++;if(u.startsWith("turn:")||u.startsWith("turns:")){turn++;if(u.startsWith("turns:"))turns++;if(u.contains("transport=tcp"))tcp=true;}}}else if(raw instanceof String){String u=((String)raw).toLowerCase(Locale.ROOT);if(u.startsWith("stun:")||u.startsWith("stuns:"))stun++;if(u.startsWith("turn:")||u.startsWith("turns:")){turn++;if(u.startsWith("turns:"))turns++;if(u.contains("transport=tcp"))tcp=true;}}}
        return "ICE servers stun="+stun+" turn="+turn+" turns="+turns+" turnTcp="+tcp+(stun+turn==0?" fallback=Google-STUN-only; no-TURN-relay":"");
    }

    static String candidate(String sdp){if(sdp==null)return "candidate=unknown";String[] fields=sdp.trim().split("\\s+");String protocol=fields.length>2?fields[2].toLowerCase(Locale.ROOT):"unknown",type="unknown";for(int i=0;i+1<fields.length;i++)if("typ".equals(fields[i])){type=fields[i+1].toLowerCase(Locale.ROOT);break;}return "candidate type="+type+" protocol="+protocol+" family="+family(fields.length>4?fields[4]:"");}
    static void sdpCandidates(String label,String sdp){if(sdp==null)return;int count=0;for(String line:sdp.split("\\r?\\n"))if(line.startsWith("a=candidate:")||line.startsWith("candidate:")){AppLog.info(label+" "+candidate(line.startsWith("a=")?line.substring(2):line));count++;}AppLog.info(label+" candidate count="+count);}

    static void selectedPair(PeerConnection peer,String label){
        if(peer==null)return;
        try{peer.getStats(report->logSelectedPair(report,label));}catch(Exception ex){AppLog.info(label+" selected ICE pair stats failed: "+ex.getClass().getSimpleName());}
    }
    private static void logSelectedPair(RTCStatsReport report,String label){
        Map<String,RTCStats> stats=report.getStatsMap();Map<String,RTCStats> candidates=new HashMap<>();for(RTCStats stat:stats.values())if("local-candidate".equals(stat.getType())||"remote-candidate".equals(stat.getType()))candidates.put(stat.getId(),stat);
        for(RTCStats stat:stats.values())if("candidate-pair".equals(stat.getType())){Map<String,Object> m=stat.getMembers();boolean selected=truth(m.get("selected"))||truth(m.get("nominated"))&&"succeeded".equals(String.valueOf(m.get("state")));if(!selected)continue;RTCStats local=candidates.get(String.valueOf(m.get("localCandidateId"))),remote=candidates.get(String.valueOf(m.get("remoteCandidateId")));AppLog.info(label+" selected ICE pair local="+candidateType(local)+" remote="+candidateType(remote)+" state="+m.get("state"));return;}
        AppLog.info(label+" selected ICE pair unavailable in stats");
    }
    private static boolean truth(Object value){return Boolean.TRUE.equals(value)||"true".equalsIgnoreCase(String.valueOf(value));}
    private static String candidateType(RTCStats stat){if(stat==null)return "unknown";Map<String,Object> m=stat.getMembers();return String.valueOf(m.getOrDefault("candidateType","unknown"))+"/"+String.valueOf(m.getOrDefault("protocol","unknown"))+"/"+family(String.valueOf(m.containsKey("address")?m.get("address"):m.getOrDefault("ip","")));}
    private static String family(String address){if(address.contains(":"))return "ipv6";if(address.matches("[0-9.]+"))return "ipv4";return "unknown";}
}
