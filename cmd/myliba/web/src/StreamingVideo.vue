<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { createThumbnailVariant, createVideoVariant, fileURL, getResourceFile, getResourceThumbnailVariant, getCloudFile, getCloudVideoPart, getCloudVideoVariant, getVideoPart, getVideoVariant, type Resource, type VideoManifest, type VideoQuality } from "./api";
import { webRTCConnected, webRTCFetch } from "./webrtc";
import { readBlobWithProgress } from "./transferProgress";

const props = defineProps<{ id: string; name: string; remote: boolean; phoneNode?: boolean; shareKey?: string; cloud?: boolean; qualities?: VideoQuality[]; resource?: Pick<Resource, "id" | "size" | "modified">; poster?: string; fileSize?: number }>();
const emit = defineEmits<{ metadata: [duration: number] }>();
type Selection = VideoQuality | "original";
type NetworkInfo = { downlink?: number; effectiveType?: string };
const connection = (navigator as Navigator & { connection?: NetworkInfo }).connection;
const preferredQuality: VideoQuality = connection?.downlink && connection.downlink < 2 || connection?.effectiveType === "2g" ? "360p" : "720p";
const selected = ref<Selection>(props.phoneNode ? "original" : props.cloud ? (props.qualities?.includes(preferredQuality) ? preferredQuality : props.qualities?.[0] ?? preferredQuality) : preferredQuality);
const video = ref<HTMLVideoElement | null>(null);
const message = ref("");
const loading = ref(false);
const started = ref(false);
const posterURL = ref(props.poster || "");
const progress = ref<number | null>(null);
const transferLabel = ref("");
let generation = 0;
let posterObjectURL = "";
const controller = new AbortController();
let mediaSource: MediaSource | undefined;
let mediaURL = "";
let sourceBuffer: SourceBuffer | undefined;
let manifest: VideoManifest | undefined;
let starts: number[] = [];
let loaded = new Set<number>();
let pumping = false;
let initialized = false;
let restorePosition = 0;
let resumePlayback = false;

async function loadPoster() {
  if (!props.resource) return;
  const resourceID = props.id;
  message.value = "正在加载视频封面…";
  try {
    let response = await getResourceThumbnailVariant(props.resource, "large");
    if (response.status === 404) {
      await createThumbnailVariant(props.id, "large");
      response = await getResourceThumbnailVariant(props.resource, "large");
    }
    const blob = await readBlobWithProgress(response, (value, received, total) => { if (!started.value) { progress.value = value; transferLabel.value = total ? `${formatBytes(received || 0)} / ${formatBytes(total)}` : `${formatBytes(received || 0)} 已加载`; } }, 0, controller.signal);
    if (controller.signal.aborted || resourceID !== props.id) return;
    posterObjectURL = URL.createObjectURL(blob);
    posterURL.value = posterObjectURL;
  } catch { /* A video can still be played without a generated cover. */ }
  finally { if (!started.value) { message.value = ""; progress.value = null; } }
}

function playPreview() {
  started.value = true;
  void nextTick(() => start());
}

function playOriginal() {
  const changed = selected.value !== "original";
  selected.value = "original";
  if (!started.value) { started.value = true; if (!changed) void nextTick(() => start()); }
}

async function variant(quality: VideoQuality, create = false): Promise<VideoManifest> {
  if (props.cloud) return getCloudVideoVariant(props.id, quality);
  if (!props.shareKey) return create ? createVideoVariant(props.id, quality) : getVideoVariant(props.id, quality);
  const response = await webRTCFetch(`/api/shares/${encodeURIComponent(props.id)}/video/${quality}`, {
    method: create ? "POST" : "GET", headers: { "X-Myliba-Share-Key": props.shareKey },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok && response.status !== 202) throw new Error(body.error || `视频请求失败 (${response.status})`);
  return body as VideoManifest;
}

function part(quality: VideoQuality, segment: "init" | number) {
  if (props.cloud) return getCloudVideoPart(props.id, quality, segment);
  if (!props.shareKey) return getVideoPart(props.id, quality, segment);
  const suffix = segment === "init" ? "init" : `segments/${segment}`;
  return webRTCFetch(`/api/shares/${encodeURIComponent(props.id)}/video/${quality}/${suffix}`, { headers: { "X-Myliba-Share-Key": props.shareKey } });
}

function dispose() {
  generation++;
  if (video.value) {
    video.value.pause();
    video.value.removeAttribute("src");
    video.value.load();
  }
  if (mediaSource?.readyState === "open") {
    try { mediaSource.endOfStream(); } catch { /* A seek may still be appending. */ }
  }
  if (mediaURL) URL.revokeObjectURL(mediaURL);
  mediaSource = undefined;
  mediaURL = "";
  sourceBuffer = undefined;
  manifest = undefined;
  loaded = new Set();
  pumping = false;
  initialized = false;
}

async function start() {
  const currentVideo = video.value;
  dispose();
  const current = generation;
  message.value = "";
  loading.value = false;
  progress.value = null;
  if (!currentVideo) return;
  if (selected.value === "original") {
    if (props.remote || props.cloud || props.shareKey) {
      loading.value = true;
      message.value = "正在读取视频原片…";
      try {
        const response = props.cloud ? await getCloudFile(props.id) : props.shareKey
          ? await webRTCFetch(`/api/shares/${encodeURIComponent(props.id)}/file`, { headers: { "X-Myliba-Share-Key": props.shareKey } })
          : await getResourceFile(props.id);
        if (!response.ok) throw new Error(`读取视频失败 (${response.status})`);
        const maxPreviewBytes = 256 * 1024 * 1024;
        const length = Number(response.headers.get("Content-Length") || props.fileSize || 0);
        if (length > maxPreviewBytes) { await response.body?.cancel(); throw new Error("网页原片预览目前限 256 MB，请下载原片查看。"); }
        const reader = response.body?.getReader();
        if (!reader) throw new Error("无法读取视频流");
        const chunks: Uint8Array[] = [];
        let received = 0;
        progress.value = length > 0 ? 0 : null;
        transferLabel.value = length > 0 ? `0 B / ${formatBytes(length)}` : "文件大小未知";
        try {
          while (true) {
            const { done, value } = await reader.read();
            if (done) break;
            received += value.byteLength;
            if (received > maxPreviewBytes) throw new Error("网页原片预览目前限 256 MB，请下载原片查看。");
            chunks.push(value);
            if (length > 0 && current === generation) progress.value = Math.min(100, Math.round(received * 100 / length));
            if (current === generation) transferLabel.value = length > 0 ? `${formatBytes(received)} / ${formatBytes(length)}` : `${formatBytes(received)} 已加载`;
          }
        } catch (cause) {
          await reader.cancel().catch(() => {});
          throw cause;
        } finally { reader.releaseLock(); }
        const blob = new Blob(chunks as BlobPart[], { type: response.headers.get("Content-Type") || "video/mp4" });
        if (current !== generation) return;
        mediaURL = URL.createObjectURL(blob);
        currentVideo.src = mediaURL;
        currentVideo.load();
        loading.value = false;
        progress.value = null;
        transferLabel.value = "";
        message.value = "";
      } catch (cause) {
        if (current === generation) { loading.value = false; progress.value = null; transferLabel.value = ""; message.value = cause instanceof Error ? cause.message : "无法预览原片"; }
      }
      return;
    }
    currentVideo.src = fileURL(props.id);
    loading.value = true;
    message.value = "正在缓冲原片…";
    currentVideo.load();
    return;
  }
  loading.value = true;
  message.value = "正在准备流媒体播放…";
  const quality = selected.value;
  try {
    if (props.shareKey && !webRTCConnected()) throw new Error("分享直连不可用，暂时无法播放视频流");
    let result = await variant(quality);
    if (current !== generation) return;
    if (props.cloud && (result.status === "missing" || result.status === "failed")) throw new Error("云层尚无此清晰度，请在源节点生成后同步。");
    if (result.status === "missing" || result.status === "failed") result = await variant(quality, true);
    while (current === generation && result.status === "processing") {
      message.value = `正在生成 ${quality} 视频片段，首次播放需要等待转换完成…`;
      await new Promise<void>((resolve) => { setTimeout(resolve, 1500); });
      if (current !== generation) return;
      result = await variant(quality);
    }
    if (current !== generation) return;
    if (result.status !== "ready" || !result.segments?.length || !result.codec || !result.duration) throw new Error(result.error || "视频片段尚不可用");
    if (!window.MediaSource || !MediaSource.isTypeSupported(`video/mp4; codecs="${result.codec}"`)) throw new Error("当前浏览器不支持该视频流格式");
    manifest = result;
    starts = [];
    let offset = 0;
    for (const duration of result.segments) { starts.push(offset); offset += duration; }
    mediaSource = new MediaSource();
    mediaURL = URL.createObjectURL(mediaSource);
    currentVideo.src = mediaURL;
    mediaSource.addEventListener("sourceopen", () => { void attach(current, quality, result); }, { once: true });
  } catch (cause) {
    if (current === generation) {
      loading.value = false;
      message.value = cause instanceof Error ? cause.message : "无法播放视频";
    }
  }
}

async function append(bytes: ArrayBuffer, current: number) {
  const buffer = sourceBuffer;
  if (!buffer || current !== generation) return;
  await new Promise<void>((resolve, reject) => {
    const finished = () => { buffer.removeEventListener("updateend", finished); buffer.removeEventListener("error", failed); resolve(); };
    const failed = () => { buffer.removeEventListener("updateend", finished); buffer.removeEventListener("error", failed); reject(new Error("视频缓冲失败")); };
    buffer.addEventListener("updateend", finished, { once: true });
    buffer.addEventListener("error", failed, { once: true });
    try { buffer.appendBuffer(bytes); } catch (cause) { failed(); }
  });
}

async function attach(current: number, quality: VideoQuality, result: VideoManifest) {
  try {
    if (current !== generation || !mediaSource || !video.value) return;
    const buffer = mediaSource.addSourceBuffer(`video/mp4; codecs="${result.codec}"`);
    buffer.mode = "segments";
    sourceBuffer = buffer;
    mediaSource.duration = result.duration!;
    const response = await part(quality, "init");
    if (!response.ok) throw new Error("无法读取视频初始化片段");
    const bytes = await (await readBlobWithProgress(response, (value, received, total) => { if (current === generation) { progress.value = value; transferLabel.value = total ? `${formatBytes(received || 0)} / ${formatBytes(total)}` : `${formatBytes(received || 0)} 已加载`; } })).arrayBuffer();
    if (current !== generation) return;
    await append(bytes, current);
    if (current !== generation) return;
    initialized = true;
    if (restorePosition > 0) video.value.currentTime = Math.min(restorePosition, result.duration! - 0.1);
    loading.value = false;
    progress.value = null;
    message.value = "";
    emit("metadata", result.duration!);
    void pump();
    if (resumePlayback) void video.value.play().catch(() => {});
  } catch (cause) {
    if (current === generation) { loading.value = false; message.value = cause instanceof Error ? cause.message : "视频初始化失败"; }
  }
}

function segmentAt(time: number) {
  let index = 0;
  for (let i = 1; i < starts.length && starts[i] <= time; i++) index = i;
  return index;
}

async function trimBuffer(current: number) {
  const buffer = sourceBuffer;
  const time = video.value?.currentTime ?? 0;
  if (!buffer || time < 50 || buffer.buffered.length === 0 || buffer.buffered.start(0) >= time - 40) return;
  await new Promise<void>((resolve) => {
    buffer.addEventListener("updateend", () => resolve(), { once: true });
    buffer.remove(0, time - 30);
  });
  if (current !== generation) return;
  for (const index of loaded) if (starts[index] < time - 35) loaded.delete(index);
}

async function pump() {
  if (pumping || !initialized || !manifest?.segments || !sourceBuffer || !video.value || selected.value === "original") return;
  pumping = true;
  const current = generation;
  const quality = selected.value;
  let requestedIndex = -1;
  try {
    const first = segmentAt(video.value.currentTime);
    requestedIndex = first;
    for (let index = first; index <= Math.min(first + 2, manifest.segments.length - 1); index++) {
      if (current !== generation) return;
      if (loaded.has(index)) continue;
      const response = await part(quality, index);
      if (!response.ok) throw new Error("视频片段传输失败");
      const bytes = await (await readBlobWithProgress(response, (value, received, total) => { if (current === generation) { progress.value = value; transferLabel.value = total ? `${formatBytes(received || 0)} / ${formatBytes(total)}` : `${formatBytes(received || 0)} 已加载`; } })).arrayBuffer();
      if (current !== generation) return;
      await trimBuffer(current);
      await append(bytes, current);
      loaded.add(index);
    }
    if (current === generation) { message.value = ""; progress.value = null; }
  } catch (cause) {
    if (current === generation) message.value = cause instanceof Error ? cause.message : "视频播放中断";
  } finally {
    pumping = false;
    if (current === generation && video.value && requestedIndex >= 0 && segmentAt(video.value.currentTime) !== requestedIndex) void pump();
  }
}

function changeQuality(event: Event) {
  const target = event.target as HTMLSelectElement;
  restorePosition = video.value?.currentTime ?? 0;
  resumePlayback = !!video.value && !video.value.paused;
  selected.value = target.value as Selection;
}

function onMetadata(event: Event) {
  const element = event.target as HTMLVideoElement;
  if (Number.isFinite(element.duration) && element.duration > 0) emit("metadata", element.duration);
}
function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}
function onWaiting() {
  if (!started.value) return;
  loading.value = true;
  message.value = "正在缓冲视频…";
  void pump();
}
function onCanPlay() {
  loading.value = false;
  message.value = "";
  progress.value = null;
}
function onMediaProgress() {
  const element = video.value;
  if (!element || selected.value !== "original" || !Number.isFinite(element.duration) || element.duration <= 0 || !element.buffered.length) return;
  const buffered = element.buffered.end(element.buffered.length - 1);
  progress.value = Math.min(100, Math.round(buffered * 100 / element.duration));
}

watch(() => props.id, () => { restorePosition = 0; resumePlayback = false; started.value = false; dispose(); if (posterObjectURL) URL.revokeObjectURL(posterObjectURL); posterObjectURL = ""; posterURL.value = props.poster || ""; void loadPoster(); });
watch([() => props.id, selected], async () => { if (!started.value) return; await nextTick(); void start(); }, { flush: "post" });
onMounted(() => { void loadPoster(); });
onBeforeUnmount(() => { controller.abort(); dispose(); if (posterObjectURL) URL.revokeObjectURL(posterObjectURL); });
</script>

<template>
  <div class="stream-video">
    <div class="stream-video-controls">
      <label>清晰度
        <select :value="selected" @change="changeQuality">
          <option value="original">原片</option>
          <option v-if="!phoneNode && (!cloud || qualities?.includes('360p'))" value="360p">360p · 省流量</option>
          <option v-if="!phoneNode && (!cloud || qualities?.includes('720p'))" value="720p">720p · 标清</option>
          <option v-if="!phoneNode && (!cloud || qualities?.includes('1080p'))" value="1080p">1080p · 高清</option>
        </select>
      </label>
      <span v-if="loading || message || progress !== null" role="status">{{ message || (selected === 'original' ? '原片缓冲进度' : '视频片段读取进度') }}</span>
      <progress v-if="loading || progress !== null || (!started && message)" :value="progress === null ? undefined : progress" max="100"></progress><span v-if="progress !== null">{{ progress }}% · {{ transferLabel }}</span><span v-else-if="loading">{{ transferLabel }}</span>
    </div>
    <div v-if="!started" class="stream-video-cover"><img v-if="posterURL" :src="posterURL" :alt="name + '封面'" /><div class="stream-video-cover-actions"><button type="button" @click="playPreview">{{ phoneNode ? '播放原片' : '播放预览' }}</button><button v-if="!phoneNode" type="button" @click="playOriginal">查看原片</button></div></div>
    <video v-else ref="video" controls preload="metadata" playsinline @timeupdate="pump" @seeking="pump" @waiting="onWaiting" @canplay="onCanPlay" @progress="onMediaProgress" @loadedmetadata="onMetadata" @error="selected === 'original' && (message = '浏览器无法播放此原片格式，请下载查看。')"></video>
  </div>
</template>
