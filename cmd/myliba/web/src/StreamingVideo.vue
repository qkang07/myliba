<script setup lang="ts">
import { nextTick, onBeforeUnmount, ref, watch } from "vue";
import { createVideoVariant, fileURL, getCloudVideoPart, getCloudVideoVariant, getVideoPart, getVideoVariant, type VideoManifest, type VideoQuality } from "./api";
import { webRTCConnected, webRTCFetch } from "./webrtc";

const props = defineProps<{ id: string; name: string; remote: boolean; shareKey?: string; cloud?: boolean; qualities?: VideoQuality[] }>();
const emit = defineEmits<{ metadata: [duration: number] }>();
type Selection = VideoQuality | "original";
type NetworkInfo = { downlink?: number; effectiveType?: string };
const connection = (navigator as Navigator & { connection?: NetworkInfo }).connection;
const preferredQuality: VideoQuality = connection?.downlink && connection.downlink < 2 || connection?.effectiveType === "2g" ? "360p" : "720p";
const selected = ref<Selection>(props.cloud ? (props.qualities?.includes(preferredQuality) ? preferredQuality : props.qualities?.[0] ?? preferredQuality)
  : props.remote || !!props.shareKey || !/\.(mp4|m4v|webm)$/i.test(props.name) ? preferredQuality : "original");
const video = ref<HTMLVideoElement | null>(null);
const message = ref("");
const loading = ref(false);
let generation = 0;
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
  if (!currentVideo) return;
  if (selected.value === "original") {
    currentVideo.src = fileURL(props.id);
    currentVideo.load();
    return;
  }
  loading.value = true;
  message.value = "正在准备流媒体播放…";
  const quality = selected.value;
  try {
    if (props.remote && !webRTCConnected()) throw new Error("节点直连不可用，暂时无法播放视频流");
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
    const bytes = await response.arrayBuffer();
    if (current !== generation) return;
    await append(bytes, current);
    if (current !== generation) return;
    initialized = true;
    if (restorePosition > 0) video.value.currentTime = Math.min(restorePosition, result.duration! - 0.1);
    loading.value = false;
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
      const bytes = await response.arrayBuffer();
      if (current !== generation) return;
      await trimBuffer(current);
      await append(bytes, current);
      loaded.add(index);
    }
    if (current === generation) message.value = "";
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

watch(() => props.id, () => { restorePosition = 0; resumePlayback = false; });
watch([() => props.id, selected], async () => { await nextTick(); void start(); }, { immediate: true, flush: "post" });
watch(() => props.remote, () => { if (props.remote && selected.value === "original") selected.value = "720p"; });
onBeforeUnmount(dispose);
</script>

<template>
  <div class="stream-video">
    <div class="stream-video-controls">
      <label>清晰度
        <select :value="selected" @change="changeQuality">
          <option v-if="!remote && !shareKey && !cloud" value="original">原片</option>
          <option v-if="!cloud || qualities?.includes('360p')" value="360p">360p · 省流量</option>
          <option v-if="!cloud || qualities?.includes('720p')" value="720p">720p · 标清</option>
          <option v-if="!cloud || qualities?.includes('1080p')" value="1080p">1080p · 高清</option>
        </select>
      </label>
      <span v-if="loading || message" role="status">{{ message }}</span>
    </div>
    <video ref="video" controls preload="metadata" playsinline @timeupdate="pump" @seeking="pump" @waiting="pump" @loadedmetadata="onMetadata" @error="selected === 'original' && (message = '原片格式无法播放，请选择压缩清晰度。')"></video>
  </div>
</template>
