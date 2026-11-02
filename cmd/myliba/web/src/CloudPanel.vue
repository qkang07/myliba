<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Cloud, Download, LoaderCircle, Play, RefreshCw, Upload, X } from "@lucide/vue";
import StreamingVideo from "./StreamingVideo.vue";
import {
  diagnoseCloudPeer, getCloudFile, getCloudFiles, getCloudRecovery, getCloudVersions, getResource, importCloudRecovery,
  speedTestCloudPeer, uploadResourceToCloud, type CloudFile, type CloudPeerDiagnosis, type CloudSpeedTest, type CloudView, type Resource,
} from "./api";

const props = defineProps<{ resources: Resource[]; remote: boolean }>();
const emit = defineEmits<{ openResource: [id: string] }>();
const open = ref(false);
const loading = ref(false);
const error = ref("");
const cloudQuery = ref("");
const includeDevices = ref(true);
const view = ref<CloudView>({ files: [], jobs: {}, nodeId: "", libraryId: "", peers: [], directEndpoint: "" });
const recoveryKey = ref("");
const importKey = ref("");
const activeDownload = ref("");
const activeVideo = ref("");
const activePreview = ref<{ fileID: string; url: string; mime: string; kind: "image" | "audio" | "document" } | null>(null);
const historyChoices = ref<Record<string, boolean>>({});
const versions = ref<Record<string, CloudFile[]>>({});
const diagnosingPeer = ref("");
const testingPeer = ref("");
const selectedSpeedPath = ref<Record<string, CloudSpeedTest["path"]>>({});
const diagnoses = ref<Record<string, CloudPeerDiagnosis>>({});
const speedResults = ref<Record<string, CloudSpeedTest>>({});
const matchingFiles = computed(() => {
  const query = cloudQuery.value.trim().toLocaleLowerCase();
  return view.value.files.filter((file) => !query || [file.name, file.folderName || "", ...(file.tags || [])].some((value) => value.toLocaleLowerCase().includes(query)));
});
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
  try {
    view.value = await getCloudFiles(includeDevices.value);
    for (const file of view.value.files) {
      if (file.sourceId && historyChoices.value[file.sourceId] === undefined) historyChoices.value[file.sourceId] = file.history;
    }
    error.value = "";
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取云层"; }
}
async function show(initialQuery?: string) {
  if (initialQuery !== undefined) cloudQuery.value = initialQuery.trim();
  open.value = true;
  loading.value = true;
  await refresh();
  loading.value = false;
}
defineExpose({ open: show });
async function upload(resource: Resource) {
  try { await uploadResourceToCloud(resource.id, historyChoices.value[resource.id] ?? false); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法上传文件"; }
}
function cloudMembership(resource: Resource) {
  return view.value.files.find((file) => !file.localOnly && (file.id === resource.id || file.sourceId === resource.id));
}
async function download(id: string, name: string, revisionId?: string) {
  if (activeDownload.value) return;
  activeDownload.value = id;
  try {
    const response = await getCloudFile(id, revisionId);
    if (!response.ok) throw new Error(`下载失败 (HTTP ${response.status})`);
    const url = URL.createObjectURL(await response.blob());
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法下载文件"; }
  finally { activeDownload.value = ""; }
}
function previewKind(file: CloudFile): "image" | "audio" | "document" | "video" | null {
  if (file.localOnly) return null;
  if (file.variants?.length) return "video";
  const ext = file.name.split(".").pop()?.toLocaleLowerCase() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(ext)) return "image";
  if (["mp3", "m4a", "aac", "ogg", "oga", "wav", "flac"].includes(ext)) return "audio";
  if (["mp4", "m4v", "mov", "webm", "mkv"].includes(ext)) return "video";
  if (["pdf", "txt", "md", "csv"].includes(ext)) return "document";
  return null;
}
async function preview(file: CloudFile) {
  const kind = previewKind(file);
  if (!kind) return;
  if (file.sourceId) {
    try {
      const source = await getResource(file.sourceId);
      if (source.available && source.modified === file.modified && (source.contentSize ?? source.size) === (file.originalSize ?? file.size)) {
        closePreview();
        activeVideo.value = "";
        open.value = false;
        emit("openResource", source.id);
        return;
      }
    } catch { /* The source may only exist in the cloud catalog. */ }
  }
  if (kind === "video") {
    closePreview();
    activeVideo.value = activeVideo.value === file.id ? "" : file.id;
    return;
  }
  activeVideo.value = "";
  if (activePreview.value) URL.revokeObjectURL(activePreview.value.url);
  activePreview.value = null;
  activeDownload.value = file.id;
  try {
    const response = await getCloudFile(file.id);
    if (!response.ok) throw new Error(`预览失败 (HTTP ${response.status})`);
    const url = URL.createObjectURL(await response.blob());
    activePreview.value = { fileID: file.id, url, mime: response.headers.get("Content-Type") || "application/octet-stream", kind };
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法预览云层文件"; }
  finally { activeDownload.value = ""; }
}
function closePreview() {
  if (activePreview.value) URL.revokeObjectURL(activePreview.value.url);
  activePreview.value = null;
}
async function toggleVersions(id: string) {
  if (versions.value[id]) { delete versions.value[id]; versions.value = { ...versions.value }; return; }
  try { versions.value = { ...versions.value, [id]: await getCloudVersions(id) }; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取历史"; }
}
async function revealKey() {
  try { recoveryKey.value = (await getCloudRecovery()).recoveryKey; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取恢复密钥"; }
}
async function importKeyNow() {
  try {
    await importCloudRecovery(importKey.value);
    importKey.value = "";
    await refresh();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "恢复密钥无效"; }
}
async function diagnosePeer(id: string) {
  diagnosingPeer.value = id;
  try {
    const result = await diagnoseCloudPeer(id);
    diagnoses.value = { ...diagnoses.value, [id]: result };
    view.value = { ...view.value, peerStatus: { ...view.value.peerStatus, [id]: result.diagnostics } };
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "节点诊断失败"; }
  finally { diagnosingPeer.value = ""; }
}
function availablePaths(id: string): CloudSpeedTest["path"][] {
  const checks = view.value.peerStatus?.[id]?.checks || {};
  return (["lan", "webrtc", "https", "relay"] as CloudSpeedTest["path"][]).filter((path) => checks[path]?.state === "connected");
}
async function speedTest(id: string) {
  const paths = availablePaths(id);
  const chosenPath = selectedSpeedPath.value[id];
  const path = chosenPath && paths.includes(chosenPath) ? chosenPath : paths[0];
  if (!path) { error.value = "当前没有可测速的节点连接路径，请先运行连接诊断"; return; }
  selectedSpeedPath.value = { ...selectedSpeedPath.value, [id]: path };
  testingPeer.value = id;
  try {
    const result = await speedTestCloudPeer(id, path);
    speedResults.value = { ...speedResults.value, [id]: result };
    error.value = result.error || "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "节点测速失败"; }
  finally { testingPeer.value = ""; }
}
function formatSize(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}
function pathLabel(path?: string) { return ({ lan: "局域网 TLS", webrtc: "WebRTC", https: "HTTPS", relay: "存储节点中继", direct: "HTTPS" } as Record<string, string>)[path || ""] || "节点连接"; }
function formatRate(value?: number) { if (!value) return "吞吐未测出"; return value >= 1024 ** 2 ? `${(value / 1024 ** 2).toFixed(1)} MB/s` : `${(value / 1024).toFixed(0)} KB/s`; }
function peerCheckDetail(path: string, check: { state: string; rttMillis?: number; connectMillis?: number; speedBps?: number; bytes?: number; durationMillis?: number; httpStatus?: number; selectedPair?: string; error?: string }) { if (check.state !== "connected") return check.error || "不可达"; const latency = path === "webrtc" ? `建链/请求 ${check.connectMillis ?? "?"} ms` : `RTT ${check.rttMillis ?? "?"} ms`; const pair = check.selectedPair ? ` · ICE ${check.selectedPair}` : ""; const status = check.httpStatus ? ` · HTTP ${check.httpStatus}` : ""; return `${latency} · ${formatRate(check.speedBps)} · ${formatSize(check.bytes ?? 0)} / ${check.durationMillis ?? "?"} ms${status}${pair}${check.error ? ` · ${check.error}` : ""}`; }
watch(open, (value) => {
  if (timer) clearInterval(timer);
  if (value) timer = setInterval(() => void refresh(), 3000);
  else { recoveryKey.value = ""; activeVideo.value = ""; closePreview(); }
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); closePreview(); });
</script>

<template>
  <button class="button button-secondary" type="button" @click="show()"><Cloud :size="15" />云层</button>
  <Teleport to="body">
    <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
      <section class="hub-settings-dialog cloud-dialog" role="dialog" aria-modal="true" aria-label="云层">
        <header class="hub-settings-header">
          <div><strong>云层</strong><small>文件分块加密后保存在多个节点；文件内容只走直连。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p class="hub-current">当前节点 {{ view.nodeId || "未连接" }} · 已发现 {{ view.peers.length }} 个其他在线节点</p>
          <p v-if="view.peers.length">连接诊断会分别显示每条路径的 RTT、建链/请求时间、诊断吞吐、候选类型和失败原因。ICE 的 host ↔ host 通常表示主机候选直连，出现 relay 表示经过 TURN。纯测速只收发合成数据，每条路径 3 轮、每轮 4 MiB；速度按数据传输时间计算，不读写云层文件。</p>
          <p v-if="!remote && view.peers.length === 0">当前是单节点云层：文件仍会在本机分块、加密、保存索引并可随时读取。只有一份副本时会提示保护不足，不影响使用。</p>
		  <p v-if="!remote && !view.directEndpoint && !view.lan?.length && view.peers.length > 0">本客户端通过 Hub 协商 WebRTC 节点直连；若直连失败，请检查网络或配置可访问的地址。</p>
		  <div v-for="peer in view.peers" :key="peer.id" class="hub-profile-row cloud-peer-row">
		    <div><strong>{{ peer.role === "storage" ? "存储节点" : "客户端" }} · {{ peer.id.slice(0, 12) }}</strong><small>{{ peer.lan?.length ? "已公布局域网地址 · " : "" }}{{ peer.endpoint.startsWith("p2p://") ? "WebRTC 地址" : peer.endpoint ? "HTTPS 地址" : "等待直连地址" }}</small></div>
		    <span :class="['hub-cloud-status', { offline: view.peerStatus?.[peer.id]?.state !== 'connected' }]">{{ view.peerStatus?.[peer.id]?.state === "connected" ? `${pathLabel(view.peerStatus[peer.id].path)} 直连` : view.peerStatus?.[peer.id]?.state === "unreachable" ? "当前不可达" : "检测中" }}</span>
		    <small v-if="view.peerStatus?.[peer.id]?.checks" class="cloud-peer-checks">{{ Object.entries(view.peerStatus[peer.id].checks || {}).map(([path, check]) => `${pathLabel(path)}：${peerCheckDetail(path, check)}`).join("　/　") }}</small>
		    <div class="cloud-peer-actions">
		      <select v-model="selectedSpeedPath[peer.id]" :disabled="!!testingPeer || !availablePaths(peer.id).length" aria-label="测速路径">
		        <option v-for="path in availablePaths(peer.id)" :key="path" :value="path">{{ pathLabel(path) }}</option>
		      </select>
		      <button class="button button-secondary" type="button" :disabled="!!diagnosingPeer || !!testingPeer" @click="diagnosePeer(peer.id)">{{ diagnosingPeer === peer.id ? "诊断中…" : "连接诊断" }}</button>
	      <button class="button button-secondary" type="button" :disabled="!!diagnosingPeer || !!testingPeer || !availablePaths(peer.id).length" @click="speedTest(peer.id)">{{ testingPeer === peer.id ? "测速中…" : "纯测速" }}</button>
		    </div>
		    <div v-if="diagnoses[peer.id]" class="cloud-peer-diagnostics">
		      <strong>诊断详情</strong>
		      <small>角色 {{ diagnoses[peer.id].peer.role }} · 地址类型 {{ diagnoses[peer.id].peer.endpointType }} · 局域网地址 {{ diagnoses[peer.id].peer.lanAddresses }} 个 · 最近心跳 {{ diagnoses[peer.id].peer.seenAt || "未知" }} · 检测时间 {{ diagnoses[peer.id].diagnostics.checkedAt || "未知" }}</small>
	      <small v-for="(check, path) in diagnoses[peer.id].diagnostics.checks" :key="path">{{ pathLabel(path) }} · {{ check.state === "connected" ? "可达" : "不可达" }} · {{ peerCheckDetail(path, check) }}</small>
		    </div>
		    <div v-if="speedResults[peer.id]" class="cloud-peer-diagnostics">
		      <strong>{{ pathLabel(speedResults[peer.id].path) }}纯测速：{{ speedResults[peer.id].state === "complete" ? formatRate(speedResults[peer.id].speedBps) : "失败" }}</strong>
	      <small>{{ speedResults[peer.id].rounds ?? 0 }} 轮 · {{ formatSize(speedResults[peer.id].bytes ?? 0) }} · 总耗时 {{ speedResults[peer.id].durationMillis ?? 0 }} ms · 平均建链/请求 {{ speedResults[peer.id].connectMillis ?? 0 }} ms · {{ speedResults[peer.id].startedAt }}</small>
		      <small v-if="speedResults[peer.id].selectedPair">已选 ICE 路径：{{ speedResults[peer.id].selectedPair }}</small>
	      <small v-for="(sample, index) in speedResults[peer.id].samples" :key="index">第 {{ index + 1 }} 轮：{{ formatRate(sample.speedBps) }} · 数据 {{ sample.durationMillis }} ms · 建链/请求 {{ sample.connectMillis }} ms</small>
		      <small v-if="speedResults[peer.id].error" class="form-error">{{ speedResults[peer.id].error }}</small>
		    </div>
		  </div>
          <div class="cloud-heading"><strong>跨设备与云层文件</strong><button class="quiet-button" type="button" @click="refresh"><RefreshCw :size="15" />刷新</button></div>
          <label class="cloud-file-search"><Cloud :size="15" /><input v-model="cloudQuery" type="search" placeholder="搜索文件名、文件夹或标签" aria-label="查找云层文件" /></label>
          <label><input v-model="includeDevices" type="checkbox" @change="refresh" />同时检索设备已同步的文件索引</label>
          <p>文件夹和标签随来源设备的索引同步更新。未同步的设备文件不会出现在这里；受保护的文件请回到来源设备解锁查看。</p>
          <p v-if="loading">正在读取云层索引…</p>
          <p v-else-if="!view.files.length">尚无已同步的文件。设备发布索引或文件加入云层后会显示在这里。</p>
          <p v-else-if="!matchingFiles.length">没有匹配的已同步文件。</p>
          <template v-for="file in matchingFiles" :key="file.id">
          <p v-if="file.localOnly">仅设备索引 · 未存入云层。请切换到来源设备读取原件。</p>
          <p v-for="location in file.locations || []" :key="location.nodeId + location.path">来源设备 {{ location.nodeId }} · {{ location.path }}</p>
          <p v-if="file.folderName || file.tags?.length">{{ file.folderName ? '文件夹：' + file.folderName : '未归组' }}<span v-if="file.tags?.length"> · 标签：{{ file.tags.join('、') }}</span></p>
          <div class="hub-profile-row">
            <div><strong>{{ file.name }}</strong><small v-if="!file.localOnly">文件总大小 {{ formatSize(file.size) }} · 原件 {{ formatSize(file.originalSize ?? file.size) }} · {{ file.chunks }} 块 · {{ file.replicas ?? file.minReplicas }} 份副本 · {{ file.availableReplicas ?? "—" }} 份可用（60 秒内端点探测） · {{ file.history ? "保留历史" : "无历史" }}</small><small v-else>文件大小 {{ formatSize(file.originalSize) }} · {{ file.locations?.length || 0 }} 处已记录的原件位置</small></div>
            <button v-if="previewKind(file)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="preview(file)"><LoaderCircle v-if="activeDownload === file.id" class="spin" :size="14" /><Play v-else :size="14" />{{ activeDownload === file.id ? "读取中" : activeVideo === file.id || activePreview?.fileID === file.id ? "收起预览" : "预览" }}</button>
            <button v-if="file.history" class="button button-secondary" type="button" @click="toggleVersions(file.id)">历史</button>
            <button v-if="!file.localOnly" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file.id, file.name)">
              <LoaderCircle v-if="activeDownload === file.id" class="spin" :size="14" /><Download v-else :size="14" />下载
            </button>
          </div>
          <div v-if="activeVideo === file.id" class="cloud-video-preview"><StreamingVideo :id="file.id" :name="file.name" :remote="remote" cloud :qualities="file.variants" :file-size="file.originalSize" /></div>
          <div v-if="activePreview?.fileID === file.id" class="cloud-file-preview"><button class="quiet-button" type="button" @click="closePreview">关闭预览<X :size="14" /></button><img v-if="activePreview.kind === 'image'" :src="activePreview.url" :alt="file.name" /><audio v-else-if="activePreview.kind === 'audio'" :src="activePreview.url" controls preload="metadata" /><iframe v-else :src="activePreview.url" :title="file.name" /></div>
          </template>
          <div v-for="file in view.files.filter((item) => versions[item.id])" :key="file.id + '-history'" class="cloud-history">
            <div v-for="version in versions[file.id]" :key="version.revisionId" class="hub-profile-row">
              <div><strong>{{ version.name }}</strong><small>{{ version.modified }} · {{ formatSize(version.size) }}</small></div>
              <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file.id, version.name, version.revisionId)"><Download :size="14" />下载此版</button>
            </div>
          </div>
          <template v-if="!remote">
            <div class="cloud-heading"><strong>将当前页面的本地文件加入云层</strong></div>
            <p v-if="!resources.length">进入资源列表后，可从这里选择文件上传。</p>
            <div v-for="resource in resources" :key="resource.id" class="hub-profile-row">
              <div><strong>{{ resource.name }}</strong><small>{{ formatSize(resource.contentSize ?? resource.size) }} · {{ cloudMembership(resource) ? `已在云层，${cloudMembership(resource)?.replicas ?? cloudMembership(resource)?.minReplicas ?? 0} 份副本，${cloudMembership(resource)?.availableReplicas ?? "—"} 份可用` : view.jobs[resource.id] || "仅本地" }}</small><label class="cloud-history-choice"><input v-model="historyChoices[resource.id]" type="checkbox" />保留后续历史版本</label></div>
              <button v-if="!cloudMembership(resource)" class="button button-secondary" type="button" :disabled="!resource.available || view.jobs[resource.id] === '上传中'" @click="upload(resource)">
                <Upload :size="14" />{{ view.jobs[resource.id] === "上传中" ? "上传中" : "加入云层" }}
              </button>
            </div>
            <div class="cloud-heading"><strong>恢复密钥</strong></div>
            <p>请将恢复密钥另存于安全位置；原客户端丢失后，使用它解锁其他节点上的加密索引。</p>
            <button class="button button-secondary" type="button" @click="revealKey">显示恢复密钥</button>
            <code v-if="recoveryKey" class="cloud-recovery-key">{{ recoveryKey }}</code>
            <form class="hub-settings-form" @submit.prevent="importKeyNow">
              <label>在空文集设备上导入恢复密钥<input v-model="importKey" minlength="64" maxlength="64" autocomplete="off" /></label>
              <button class="button button-secondary" type="submit" :disabled="importKey.length !== 64">导入</button>
            </form>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
</template>
