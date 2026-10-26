package com.myliba.android;

import org.json.JSONArray;
import org.json.JSONObject;
import org.webrtc.PeerConnection;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

final class WebRtcIceServers {
    private WebRtcIceServers() {}

    static List<PeerConnection.IceServer> parse(JSONArray configured) {
        List<PeerConnection.IceServer> result = new ArrayList<>();
        if (configured != null) for (int i = 0; i < configured.length(); i++) {
            JSONObject server = configured.optJSONObject(i);
            if (server == null) continue;
            Object rawUrls = server.opt("urls");
            JSONArray urls = rawUrls instanceof JSONArray ? (JSONArray) rawUrls : null;
            String username = server.optString("username", "");
            String credential = server.optString("credential", "");
            int count = urls == null ? (rawUrls instanceof String ? 1 : 0) : urls.length();
            for (int j = 0; j < count; j++) {
                String url = urls == null ? (String) rawUrls : urls.optString(j, "");
                String normalized = url.toLowerCase(Locale.ROOT);
                if (!(normalized.startsWith("stun:") || normalized.startsWith("stuns:") ||
                        normalized.startsWith("turn:") || normalized.startsWith("turns:"))) continue;
                PeerConnection.IceServer.Builder builder = PeerConnection.IceServer.builder(url);
                if (!username.isEmpty()) builder.setUsername(username);
                if (!credential.isEmpty()) builder.setPassword(credential);
                result.add(builder.createIceServer());
            }
        }
        if (result.isEmpty()) result.add(PeerConnection.IceServer.builder("stun:stun.l.google.com:19302").createIceServer());
        return result;
    }
}
