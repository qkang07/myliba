<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { ArrowLeft, Check, CircleAlert, Copy, Download, Expand, File, FileText, Image, Link2, LoaderCircle, Minimize, Music2, Play, Plus, RefreshCw, Tag, Trash2 } from "@lucide/vue";
import ImagePreview from "./ImagePreview.vue";
import StreamingVideo from "./StreamingVideo.vue";
import {
  addResourceTag, createFileShare, createThumbnailVariant, fileURL, getResource, getResourceFile, getResourceThumbnailVariant, listFileShares, revokeFileShare, uploadResourceToCloud,
  getTagSuggestions, getTags, getVideoInfo, moveResourcesToFolder, removeResourceTag,
  type FileShare, type Resource, type TagSuggestion, type TagSummary, type VirtualFolder,
} from "./api";

const props = defineProps<{ id: string; folders: VirtualFolder[]; remote: boolean; phoneNode?: boolean }>();
const emit = defineEmits<{ back: []; tagsChanged: []; folderChanged: [] }>();
type Size = "small" | "medium" | "large";
type Capture = { key: string; at: number; label: string };
const resource = ref<Resource | null>(null);
const loading = ref(true);
const error = ref("");
const previewError = ref("");
const previewURL = ref("");
const cloudStatus = ref("");
const cloudBusy = ref(false);
const shares = ref<FileShare[]>([]);
const shareHours = ref(168);
const shareBusy = ref(false);
const shareURL = ref("");
const shareError = ref("");
const tagInput = ref("");
const tagBusy = ref(false);
const folderBusy = ref(false);
const tagSuggestions = ref<TagSuggestion[]>([]);
const tagCatalog = ref<TagSummary[]>([]);
const duration = ref(0);
const durationError = ref("");
const images = ref(new Map<string, string>());
const busy = ref(new Set<string>());
const captureBusy = ref(false);
const imageViewerOpen = ref(false);
const expandedPreview = ref(false);
let generation = 0;

const mimeType = computed(() => {
  const ext = resource.value?.name.split(".").pop()?.toLowerCase() || "";
  const types: Record<string, string> = {
    avif: "image/avif", bmp: "image/bmp", gif: "image/gif", jpeg: "image/jpeg", jpg: "image/jpeg",
    png: "image/png", tif: "image/tiff", tiff: "image/tiff", webp: "image/webp",
    mp4: "video/mp4", m4v: "video/mp4", mov: "video/quicktime", mkv: "video/x-matroska", avi: "video/x-msvideo", webm: "video/webm", ogv: "video/ogg",
    mp3: "audio/mpeg", m4a: "audio/mp4", aac: "audio/aac", oga: "audio/ogg", ogg: "audio/ogg",
    wav: "audio/wav", flac: "audio/flac", pdf: "application/pdf", txt: "text/plain",
  };
  return types[ext] || "";
});
const sizes: { key: Size; label: string; dimensions: string }[] = [
  { key: "small", label: "小", dimensions: "240 × 160" },
  { key: "medium", label: "中", dimensions: "480 × 320" },
  { key: "large", label: "大", dimensions: "1280 × 960" },
];
const captures = computed<Capture[]>(() => {
  if (!duration.value) return [];
  const segment = duration.value / 6;
  return Array.from({ length: 6 }, (_, i) => ({
    key: `capture-${i}`,
    at: Math.max(0, Math.min(duration.value - 0.05, (i + 0.5) * segment)),
    label: `${formatTime(i * segment)}–${formatTime((i + 1) * segment)}`,
  }));
});
const kindLabel = computed(() => ({ image: "图片", video: "视频", audio: "音频", document: "文档", file: "文件" }[resource.value?.kind || "file"] || "文件"));
const fileIcon = computed(() => resource.value?.kind === "image" ? Image : resource.value?.kind === "video" ? Play : resource.value?.kind === "audio" ? Music2 : resource.value?.kind === "document" ? FileText : File);
function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}
function formatTime(seconds: number) {
  const whole = Math.floor(Math.max(0, seconds));
  const hh = Math.floor(whole / 3600);
  const mm = Math.floor(whole % 3600 / 60);
  const ss = whole % 60;
  return hh ? `${hh}:${String(mm).padStart(2, "0")}:${String(ss).padStart(2, "0")}` : `${mm}:${String(ss).padStart(2, "0")}`;
}
function setImage(key: string, url: string) {
  const next = new Map(images.value);
  const old = next.get(key);
  if (old) URL.revokeObjectURL(old);
  next.set(key, url);
  images.value = next;
}
function clearImages() {
  for (const url of images.value.values()) URL.revokeObjectURL(url);
  images.value = new Map();
}
async function readVariant(key: string, size: Size, at = 0, current = generation) {
  if (!resource.value) return;
  try {
    const response = await getResourceThumbnailVariant(resource.value, size, at);
    if (!response.ok) return;
    const url = URL.createObjectURL(await response.blob());
    if (current !== generation) URL.revokeObjectURL(url);
    else setImage(key, url);
  } catch { /* An absent cached variant is generated on demand. */ }
}
async function generateVariant(key: string, size: Size, at = 0) {
  if (!resource.value || busy.value.has(key)) return false;
  const current = generation;
  error.value = "";
  busy.value = new Set(busy.value).add(key);
  try {
    await createThumbnailVariant(resource.value.id, size, at);
    if (current !== generation) return false;
    await readVariant(key, size, at, current);
    return images.value.has(key);
  } catch (cause) {
    if (current === generation) error.value = cause instanceof Error ? cause.message : "缩略图生成失败";
    return false;
  } finally {
    const next = new Set(busy.value);
    next.delete(key);
    busy.value = next;
  }
}
async function generateAllCaptures() {
  if (captureBusy.value) return;
  captureBusy.value = true;
  try {
    for (const capture of captures.value) await generateVariant(capture.key, "medium", capture.at);
  } finally { captureBusy.value = false; }
}
async function refreshResource() {
  if (previewURL.value) URL.revokeObjectURL(previewURL.value);
  previewURL.value = "";
  closeImageViewer();
  closeExpandedPreview();
  const current = ++generation;
  loading.value = true;
  error.value = "";
  previewError.value = "";
  duration.value = 0;
  durationError.value = "";
  resource.value = null;
  shares.value = [];
  shareError.value = "";
  clearImages();
  try {
    const item = await getResource(props.id);
    if (current !== generation) return;
    resource.value = item;
    loading.value = false;
    shareURL.value = "";
    if (!props.phoneNode) void listFileShares(item.id).then((items) => { if (current === generation) shares.value = items; }).catch(() => { if (current === generation) shares.value = []; });
    if (props.remote && item.available && item.kind !== "video") {
      if (item.size > 128 * 1024 * 1024) {
        previewError.value = "此文件超过直连网页预览的大小限制，请下载后打开。";
      } else {
        void getResourceFile(item.id).then(async (response) => {
          if (!response.ok) throw new Error("直连读取失败");
          const url = URL.createObjectURL(await response.blob());
          if (generation !== current) URL.revokeObjectURL(url);
          else previewURL.value = url;
        }).catch((cause) => {
          if (generation === current) previewError.value = cause instanceof Error ? cause.message : "直连不可用";
        });
      }
    }
    void getTags().then((result) => { if (current === generation) tagCatalog.value = result; }).catch(() => {});
    void getTagSuggestions(item.id).then((result) => { if (current === generation) tagSuggestions.value = result; }).catch(() => {});
    if (item.available && item.kind === "image") {
      for (const size of sizes) void readVariant(size.key, size.key, 0, current);
    }
    if (item.available && item.kind === "video") {
      void getVideoInfo(item.id).then(({ duration: seconds }) => {
        if (current !== generation) return;
        duration.value = seconds;
        for (const capture of captures.value) void readVariant(capture.key, "medium", capture.at, current);
      }).catch((cause) => {
        if (current === generation && !duration.value) durationError.value = cause instanceof Error ? cause.message : "无法读取视频时长";
      });
    }
  } catch (cause) {
    if (current === generation) {
      error.value = cause instanceof Error ? cause.message : "无法读取资源";
      loading.value = false;
    }
  }
}
async function updateTags() {
  if (!resource.value) return;
  const current = generation;
  const item = await getResource(resource.value.id);
  if (current !== generation) return;
  resource.value = item;
  tagCatalog.value = await getTags();
  if (current !== generation) return;
  tagSuggestions.value = await getTagSuggestions(item.id).catch(() => []);
  emit("tagsChanged");
}
async function addTag(name = tagInput.value) {
  if (!resource.value || !name.trim() || tagBusy.value) return;
  tagBusy.value = true;
  error.value = "";
  try {
    await addResourceTag(resource.value.id, name.trim());
    tagInput.value = "";
    await updateTags();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "添加标签失败"; }
  finally { tagBusy.value = false; }
}
async function removeTag(name: string) {
  if (!resource.value || tagBusy.value) return;
  const tag = tagCatalog.value.find((item) => item.name.toLocaleLowerCase() === name.toLocaleLowerCase());
  if (!tag) { error.value = "无法定位标签，请刷新详情页后重试"; return; }
  tagBusy.value = true;
  error.value = "";
  try {
    await removeResourceTag(resource.value.id, tag.id);
    await updateTags();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "移除标签失败"; }
  finally { tagBusy.value = false; }
}
async function changeFolder(event: Event) {
  const id = resource.value?.id;
  const target = event.target;
  if (!id || !(target instanceof HTMLSelectElement) || folderBusy.value) return;
  folderBusy.value = true;
  error.value = "";
  try {
    await moveResourcesToFolder([id], target.value);
    resource.value = await getResource(id);
    emit("folderChanged");
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "移动资源失败";
    target.value = resource.value?.folderId || "";
  } finally { folderBusy.value = false; }
}
function onVideoMetadata(seconds: number) {
  if (!Number.isFinite(seconds) || seconds <= 0 || duration.value) return;
  duration.value = seconds;
  durationError.value = "";
  const current = generation;
  for (const capture of captures.value) void readVariant(capture.key, "medium", capture.at, current);
}
async function download() {
  if (!resource.value) return;
  if (props.remote) {
    try {
      const response = await getResourceFile(resource.value.id, true);
      if (!response.ok) throw new Error(`下载失败 (HTTP ${response.status})`);
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = resource.value.name;
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (cause) { error.value = cause instanceof Error ? cause.message : "直连下载失败"; }
    return;
  }
  const link = document.createElement("a");
  link.href = fileURL(resource.value.id, true);
  link.download = resource.value.name;
  document.body.append(link);
  link.click();
  link.remove();
}
async function uploadToCloud() {
  if (!resource.value || cloudBusy.value) return;
  cloudBusy.value = true;
  try {
    await uploadResourceToCloud(resource.value.id);
    cloudStatus.value = "已加入上传队列，可在云层查看进度";
  } catch (cause) { cloudStatus.value = cause instanceof Error ? cause.message : "加入云层失败"; }
  finally { cloudBusy.value = false; }
}
async function createShare() {
  if (!resource.value || shareBusy.value) return;
  shareBusy.value = true;
  shareError.value = "";
  try {
    const result = await createFileShare(resource.value.id, shareHours.value);
    shareURL.value = result.url;
    shares.value = [result.share, ...shares.value];
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(result.url).catch(() => { /* The visible URL can be copied manually. */ });
  } catch (cause) { shareError.value = cause instanceof Error ? cause.message : "创建分享失败"; }
  finally { shareBusy.value = false; }
}
async function copyShare() {
  if (!shareURL.value) return;
  try {
    if (!navigator.clipboard?.writeText) throw new Error("clipboard unavailable");
    await navigator.clipboard.writeText(shareURL.value);
  }
  catch { shareError.value = "无法自动复制，请手动复制上方链接"; }
}
async function revokeShare(id: string) {
  if (shareBusy.value) return;
  shareBusy.value = true;
  shareError.value = "";
  try {
    await revokeFileShare(id);
    shares.value = shares.value.map((item) => item.id === id ? { ...item, revokedAt: new Date().toISOString() } : item);
    if (shareURL.value.includes(`/s/${id}#`)) shareURL.value = "";
  } catch (cause) { shareError.value = cause instanceof Error ? cause.message : "撤销分享失败"; }
  finally { shareBusy.value = false; }
}
function openImageViewer() {
  if (!resource.value?.available || !mimeType.value.startsWith("image/") || previewError.value) return;
  closeExpandedPreview();
  imageViewerOpen.value = true;
}
function closeImageViewer() {
  imageViewerOpen.value = false;
}
function closeExpandedPreview() {
  expandedPreview.value = false;
  document.body.classList.remove("detail-preview-open");
}
function toggleExpandedPreview() {
  expandedPreview.value = !expandedPreview.value;
  document.body.classList.toggle("detail-preview-open", expandedPreview.value);
}
function onPreviewKeydown(event: KeyboardEvent) {
  if (event.key === "Escape" && expandedPreview.value) closeExpandedPreview();
}
document.addEventListener("keydown", onPreviewKeydown);
watch(() => props.id, () => void refreshResource(), { immediate: true });
onBeforeUnmount(() => {
  if (previewURL.value) URL.revokeObjectURL(previewURL.value);
  generation++;
  clearImages();
  closeImageViewer();
  closeExpandedPreview();
  document.removeEventListener("keydown", onPreviewKeydown);
});
</script>

<template>
  <section class="detail-page" aria-label="资源详情">
    <button class="detail-back quiet-button" @click="emit('back')"><ArrowLeft :size="16" />返回资源列表</button>
    <div v-if="loading" class="detail-state"><LoaderCircle class="spin" :size="23" />正在读取资源…</div>
    <div v-else-if="!resource" class="detail-state"><CircleAlert :size="25" /><p>{{ error }}</p><button class="button button-secondary" @click="refreshResource"><RefreshCw :size="14" />重试</button></div>
    <template v-else>
      <header class="detail-heading">
        <span class="detail-kind-icon"><component :is="fileIcon" :size="25" /></span>
        <div><p class="eyebrow">资源详情 · {{ kindLabel }}</p><h1>{{ resource.name }}</h1><p>{{ resource.available ? "源文件可用" : "源文件暂不可用" }} · 文件总大小 {{ formatBytes(resource.contentSize ?? resource.size) }}</p></div>
        <button class="button button-secondary" :disabled="!resource.available" @click="download"><Download :size="15" />下载原文件</button>
        <button v-if="!remote" class="button button-secondary" :disabled="!resource.available || cloudBusy" @click="uploadToCloud">加入云层</button>
      </header>
      <p v-if="cloudStatus" class="detail-error" role="status">{{ cloudStatus }}</p>
      <section v-if="!phoneNode" class="detail-panel share-panel" aria-label="外链分享">
        <div class="detail-panel-heading"><h2><Link2 :size="17" />外链分享</h2><span>仅此文件 · 节点在线时直传</span></div>
        <div class="share-create"><label>有效期 <select v-model.number="shareHours"><option :value="24">1 天</option><option :value="168">7 天</option><option :value="720">30 天</option></select></label><button class="button button-secondary" :disabled="!resource.available || shareBusy" @click="createShare">创建分享链接</button></div>
        <div v-if="shareURL" class="share-created"><p>链接已创建，密钥仅显示这一次。可直接粘贴发送给接收者。</p><div><input :value="shareURL" readonly aria-label="分享链接" @focus="($event.target as HTMLInputElement).select()" /><button class="button button-secondary" @click="copyShare"><Copy :size="14" />复制</button></div></div>
        <p v-if="shareError" class="detail-error" role="alert">{{ shareError }}</p>
        <div v-if="shares.length" class="share-list"><div v-for="share in shares" :key="share.id" class="share-row"><span>{{ share.revokedAt ? '已撤销' : new Date(share.expiresAt) <= new Date() ? '已过期' : '有效至 ' + new Date(share.expiresAt).toLocaleString() }}</span><button v-if="!share.revokedAt && new Date(share.expiresAt) > new Date()" class="quiet-button" :disabled="shareBusy" @click="revokeShare(share.id)"><Trash2 :size="14" />撤销</button></div></div>
      </section>
      <p v-if="error" class="detail-error" role="alert"><CircleAlert :size="15" />{{ error }}</p>
      <div class="detail-layout">
        <div class="detail-main">
          <section class="detail-panel detail-preview-panel" :class="{ 'is-preview-expanded': expandedPreview }">
            <div class="detail-panel-heading detail-preview-heading"><h2>文件预览</h2><div class="detail-preview-tools"><span>{{ mimeType || "无法在网页中预览" }}</span><button v-if="resource.available && mimeType.startsWith('image/') && !previewError" class="detail-fullscreen-button" aria-label="查看大图" @click="openImageViewer"><Expand :size="14" />查看大图</button><button v-else-if="resource.available && mimeType && !previewError" class="detail-fullscreen-button" :aria-label="expandedPreview ? '退出窗口预览' : '铺满窗口预览'" @click="toggleExpandedPreview"><Minimize v-if="expandedPreview" :size="14" /><Expand v-else :size="14" />{{ expandedPreview ? '退出窗口预览' : '铺满窗口' }}</button></div></div>
            <div class="detail-preview">
              <template v-if="resource.available && mimeType && !previewError && (resource.kind === 'video' || !remote || previewURL)">
                <button v-if="mimeType.startsWith('image/')" type="button" class="detail-image-trigger" :aria-label="'预览图片 ' + resource.name" @click="openImageViewer"><img :src="remote ? previewURL : fileURL(resource.id)" :alt="resource.name" @error="previewError = '图片无法加载'" /><span class="detail-image-trigger-hint"><Expand :size="14" />点击查看大图</span></button>
                <StreamingVideo v-else-if="mimeType.startsWith('video/')" :id="resource.id" :name="resource.name" :remote="remote" :phone-node="phoneNode" @metadata="onVideoMetadata" />
                <audio v-else-if="mimeType.startsWith('audio/')" :src="remote ? previewURL : fileURL(resource.id)" controls preload="metadata" @error="previewError = '浏览器无法播放此音频格式'"></audio>
                <iframe v-else sandbox="" :src="remote ? previewURL : fileURL(resource.id)" :title="'预览 ' + resource.name"></iframe>
              </template>
              <div v-else class="detail-preview-fallback"><component :is="fileIcon" :size="30" /><p>{{ !resource.available ? "源文件当前不可用，请重新扫描所在目录" : previewError || "该格式暂不支持网页预览" }}</p><button v-if="resource.available" class="button button-secondary" @click="download"><Download :size="14" />下载查看</button></div>
            </div>
          </section>
          <section v-if="resource.available && resource.kind === 'image'" class="detail-panel">
            <div class="detail-panel-heading"><div><h2>多级缩略图</h2><p>按用途生成不同尺寸，结果保存在本机缓存。</p></div></div>
            <div class="detail-variants">
              <article v-for="size in sizes" :key="size.key" class="detail-variant">
                <div class="detail-variant-image"><img v-if="images.has(size.key)" :src="images.get(size.key)" :alt="size.label + '缩略图'" /><Image v-else :size="24" /></div>
                <strong>{{ size.label }} · {{ size.dimensions }}</strong>
                <button class="button button-secondary" :disabled="busy.has(size.key)" @click="generateVariant(size.key, size.key)"><LoaderCircle v-if="busy.has(size.key)" class="spin" :size="14" /><Plus v-else :size="14" />{{ images.has(size.key) ? "重新生成" : "生成缩略图" }}</button>
              </article>
            </div>
          </section>
          <section v-if="resource.available && resource.kind === 'video' && !phoneNode" class="detail-panel">
            <div class="detail-panel-heading"><div><h2>分段截图</h2><p>{{ duration ? `视频时长 ${formatTime(duration)}，均分为 6 段并截取每段中点。` : durationError || "正在读取视频时长…" }}</p></div><button v-if="captures.length" class="button button-secondary" :disabled="captureBusy" @click="generateAllCaptures"><LoaderCircle v-if="captureBusy" class="spin" :size="14" /><Image v-else :size="14" />生成全部</button></div>
            <div v-if="captures.length" class="detail-captures">
              <article v-for="capture in captures" :key="capture.key" class="detail-variant">
                <div class="detail-variant-image"><img v-if="images.has(capture.key)" :src="images.get(capture.key)" :alt="capture.label + '截图'" /><Play v-else :size="23" /></div>
                <strong>{{ capture.label }}</strong><small>截图位置 {{ formatTime(capture.at) }}</small>
                <button class="button button-secondary" :disabled="busy.has(capture.key) || captureBusy" @click="generateVariant(capture.key, 'medium', capture.at)"><LoaderCircle v-if="busy.has(capture.key)" class="spin" :size="14" /><Plus v-else :size="14" />{{ images.has(capture.key) ? "重新生成" : "生成截图" }}</button>
              </article>
            </div>
          </section>
        </div>
        <aside class="detail-side">
          <section class="detail-panel">
            <div class="detail-panel-heading"><h2>文件信息</h2></div>
            <dl class="detail-facts"><div><dt>名称</dt><dd>{{ resource.name }}</dd></div><div><dt>类型</dt><dd>{{ kindLabel }}</dd></div><div><dt>文件总大小</dt><dd>{{ formatBytes(resource.contentSize ?? resource.size) }}</dd></div><div><dt>原件大小</dt><dd>{{ formatBytes(resource.size) }}</dd></div><div><dt>修改时间</dt><dd>{{ new Date(resource.modified).toLocaleString('zh-CN') }}</dd></div><div><dt>原始路径</dt><dd class="detail-path">{{ resource.path }}</dd></div><div><dt>资源 ID</dt><dd class="detail-path">{{ resource.id }}</dd></div></dl>
          </section>
          <section class="detail-panel">
            <div class="detail-panel-heading"><h2>所属文件夹</h2></div>
            <label class="detail-folder-field">文件夹<select :value="resource.folderId" :disabled="folderBusy" @change="changeFolder"><option value="">未归组</option><option v-for="folder in folders" :key="folder.id" :value="folder.id">{{ folder.name }}</option></select></label>
            <p class="detail-folder-hint">移动只调整资源分组，不改变原文件位置。</p>
          </section>
          <section class="detail-panel">
            <div class="detail-panel-heading"><h2>属性标签</h2><span>随文件属性自动更新</span></div>
            <div class="detail-attribute-tags"><span v-for="attribute in resource.attributeTags" :key="attribute.key">{{ attribute.category === 'type' ? '类型' : attribute.category === 'size' ? '大小' : '格式' }}：{{ attribute.label }}</span></div>
          </section>
          <section class="detail-panel">
            <div class="detail-panel-heading"><h2>手动标签</h2><span>{{ resource.tags.length }} 个</span></div>
            <div class="detail-tags"><span v-for="name in resource.tags" :key="name" class="detail-tag"><Tag :size="12" />{{ name }}<button :aria-label="'移除标签 ' + name" :disabled="tagBusy" @click="removeTag(name)"><Trash2 :size="12" /></button></span><p v-if="!resource.tags.length">尚未添加手动标签</p></div>
            <form class="detail-tag-form" @submit.prevent="addTag()"><input v-model="tagInput" maxlength="40" placeholder="输入新标签" :disabled="tagBusy" /><button class="button button-primary" :disabled="tagBusy || !tagInput.trim()"><LoaderCircle v-if="tagBusy" class="spin" :size="14" /><Check v-else :size="14" />添加</button></form>
            <div v-if="tagSuggestions.length" class="detail-suggestions"><p>建议标签</p><button v-for="suggestion in tagSuggestions.filter((item) => !resource?.tags.some((name) => name.toLocaleLowerCase() === item.name.toLocaleLowerCase())).slice(0, 6)" :key="suggestion.name" :disabled="tagBusy" :title="suggestion.reason" @click="addTag(suggestion.name)"><Plus :size="12" />{{ suggestion.name }}</button></div>
          </section>
        </aside>
      </div>
    </template>
    <ImagePreview v-if="imageViewerOpen && resource" :src="remote ? previewURL : fileURL(resource.id)" :name="resource.name" @close="closeImageViewer" @download="download" />
  </section>
</template>
