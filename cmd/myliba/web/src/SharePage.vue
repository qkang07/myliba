<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { CircleAlert, Download, File, Link2, LoaderCircle } from "@lucide/vue";
import { closeWebRTC, connectWebRTC, webRTCConnected, webRTCFetch } from "./webrtc";
import StreamingVideo from "./StreamingVideo.vue";

type ShareInfo = { id: string; name: string; size: number; expiresAt: string; contentType: string; kind?: string; iceServers: RTCIceServer[] };
const shareID = window.location.pathname.split("/").pop() || "";
const key = new URLSearchParams(window.location.hash.slice(1)).get("key") || "";
const info = ref<ShareInfo | null>(null);
const loading = ref(true);
const error = ref("");
const transport = ref<"connecting" | "webrtc" | "turn" | "relay">("connecting");
const previewURL = ref("");
const downloading = ref(false);

function formatBytes(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 ** 2) return `${(size / 1024).toFixed(1)} KB`;
  if (size < 1024 ** 3) return `${(size / 1024 ** 2).toFixed(1)} MB`;
  return `${(size / 1024 ** 3).toFixed(1)} GB`;
}

async function fileResponse() {
  if (!webRTCConnected()) throw new Error("节点直连不可用，请稍后重试");
  const response = await webRTCFetch(`/api/shares/${encodeURIComponent(shareID)}/file`, { headers: { "X-Myliba-Share-Key": key } });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `文件读取失败 (${response.status})`);
  }
  return response;
}

async function openShare() {
  if (!key) { error.value = "分享链接缺少密钥"; loading.value = false; return; }
  loading.value = true;
  error.value = "";
  try {
    const response = await fetch(`/api/shares/${encodeURIComponent(shareID)}/redeem`, {
      method: "POST", credentials: "omit", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ token: key }),
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `无法打开分享 (${response.status})`);
    info.value = body as ShareInfo;
    await connectWebRTC(body.iceServers || [], (state) => { transport.value = state; }, () => {}, { id: shareID, token: key });
    if (!webRTCConnected()) throw new Error("节点直连不可用，请稍后重试");
    if (body.size <= 128 * 1024 * 1024 && /^(image|audio)\//.test(body.contentType)) {
      const file = await fileResponse();
      previewURL.value = URL.createObjectURL(await file.blob());
    }
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法打开分享"; }
  finally { loading.value = false; }
}

async function download() {
  if (!info.value || downloading.value) return;
  downloading.value = true;
  error.value = "";
  try {
    const response = await fileResponse();
    const url = URL.createObjectURL(await response.blob());
    const link = document.createElement("a");
    link.href = url;
    link.download = info.value.name;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "下载失败"; }
  finally { downloading.value = false; }
}

onMounted(() => { void openShare(); });
onBeforeUnmount(() => { if (previewURL.value) URL.revokeObjectURL(previewURL.value); closeWebRTC(); });
</script>

<template>
  <main class="share-page">
    <section class="share-card">
      <div class="share-mark"><Link2 :size="24" /></div>
      <p class="eyebrow">MYLIBA · 文件分享</p>
      <h1>{{ info?.name || '正在打开分享' }}</h1>
      <p v-if="info" class="share-meta">{{ formatBytes(info.size) }} · 有效至 {{ new Date(info.expiresAt).toLocaleString() }}</p>
      <p v-if="loading" class="share-status"><LoaderCircle class="spin" :size="17" />正在验证分享并连接文件节点…</p>
      <p v-else-if="error" class="detail-error" role="alert"><CircleAlert :size="17" />{{ error }}</p>
      <button v-if="error && !webRTCConnected()" class="button button-secondary" @click="openShare">重试连接</button>
      <template v-if="info && !loading">
        <div v-if="previewURL || info.kind === 'video' || info.contentType.startsWith('video/')" class="share-preview">
          <StreamingVideo v-if="info.kind === 'video' || info.contentType.startsWith('video/')" :id="shareID" :name="info.name" :remote="true" :share-key="key" />
          <img v-else-if="info.contentType.startsWith('image/')" :src="previewURL" :alt="info.name" />
          <audio v-else-if="info.contentType.startsWith('audio/')" :src="previewURL" controls />
        </div>
        <div v-else class="share-file-icon"><File :size="38" /></div>
        <button class="button button-primary share-download" :disabled="!webRTCConnected() || downloading" @click="download"><Download :size="16" />{{ downloading ? '正在获取文件…' : '下载文件' }}</button>
        <p class="share-transport">{{ transport === 'webrtc' ? '已连接文件节点 · WebRTC 直传' : '文件节点直连不可用' }}</p>
      </template>
    </section>
  </main>
</template>
