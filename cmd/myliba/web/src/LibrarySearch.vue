<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from "vue";
import { Bookmark, BookmarkPlus, Check, ChevronDown, Download, ExternalLink, File, FileText, FileVideo, FolderOpen, HardDrive, Image, LoaderCircle, Music2, Play, RefreshCw, Search, Tag, X } from "@lucide/vue";
import StreamingVideo from "./StreamingVideo.vue";
import CloudFileDetail from "./CloudFileDetail.vue";
import { previewKind, useCloudRead } from "./useCloudRead";
import { lockedOfflineIndex, offlineIndexStatus, saveOfflineIndex, searchOfflineIndex, unlockOfflineIndex } from "./libraryOffline";
import {
  ApiError, createLibrarySavedQuery, deleteLibrarySavedQuery, getCloudVersions, getLibrarySavedQueries, searchLibraryFiles, setLibraryFileLifecycle,
  type CloudFile, type CloudView, type HubResourceNode, type LibrarySavedQuery,
} from "./api";

const props = defineProps<{ remote: boolean; nodes?: HubResourceNode[] }>();
const emit = defineEmits<{ openResource: [id: string] }>();
const open = ref(false);
const loading = ref(false);
const error = ref("");
const cloudQuery = ref("");
const includeDevices = ref(true);
const view = ref<CloudView>({ files: [], jobs: {}, nodeId: "", libraryId: "", peers: [], directEndpoint: "" });
const versions = ref<Record<string, CloudFile[]>>({});
const searchTotal = ref(0);
const nextCursor = ref("");
const loadingMore = ref(false);
const selectedFolder = ref("");
const selectedTags = ref<string[]>([]);
const selectedDevice = ref("");
const selectedType = ref<"" | "image" | "video" | "audio" | "document" | "other">("");
const grouped = ref<"" | "true" | "false">("");
const cloudCopy = ref<"" | "true" | "false">("");
const readable = ref<"" | "true" | "false">("");
const lifecycle = ref<"active" | "trashed">("active");
const searchFacets = ref<{ folders: Array<{ id: string; name: string; count: number }>; tags: Array<{ name: string; count: number }> }>({ folders: [], tags: [] });
const sort = ref<"name_asc" | "name_desc" | "modified_desc" | "modified_asc" | "ingested_desc" | "ingested_asc">("name_asc");
const coverage = ref<{ scope: string; deviceIndexes: boolean; generation?: string; lastCatalogAt?: string; sources?: string[]; knownNodes?: string[]; unindexedSources?: string[]; sourceLastCatalogAt?: Record<string, string>; sourceFrontiers?: Record<string, Record<string, number>>; offlineSources?: string[] }>({ scope: "all", deviceIndexes: true });
const savedQueries = ref<LibrarySavedQuery[]>([]);
const savedQueriesOpen = ref(false);
const offlineMode = ref(false);
const offlineStatus = ref<{ nodeId: string; libraryId: string; syncedAt: string; count: number } | null>(null);
const detailFile = ref<CloudFile | null>(null);
const { activeDownload, activeVideo, activePreview, nodeLabel, readableLocation, closePreview, openDeviceFile, download, preview } = useCloudRead({
  remote: computed(() => props.remote),
  queryNodeId: computed(() => view.value.nodeId),
  nodes: computed(() => props.nodes ?? []),
  onError: (message) => { error.value = message; },
  onOpenResource: (id) => { open.value = false; emit("openResource", id); },
});
let timer: ReturnType<typeof setInterval> | undefined;
const tagPickerOpen = ref(false);
const tagPickerQuery = ref("");
const moreFiltersOpen = ref(false);
const coverageOpen = ref(false);
const offlinePanelOpen = ref(false);
const matchingFacetTags = computed(() => {
  const query = tagPickerQuery.value.trim().toLocaleLowerCase();
  return searchFacets.value.tags.filter((tag) => !query || tag.name.toLocaleLowerCase().includes(query));
});
const hasActiveFilters = computed(() => Boolean(cloudQuery.value || selectedFolder.value || selectedTags.value.length || selectedDevice.value || selectedType.value || grouped.value || cloudCopy.value || readable.value));
function toggleFacetTag(name: string) {
  selectedTags.value = selectedTags.value.includes(name) ? selectedTags.value.filter((tag) => tag !== name) : [...selectedTags.value, name];
  void refresh();
}
function fileIcon(file: CloudFile) {
  if (file.variants?.length) return FileVideo;
  const ext = file.name.split(".").pop()?.toLocaleLowerCase() || "";
  if (["jpg", "jpeg", "png", "gif", "webp", "bmp", "avif"].includes(ext)) return Image;
  if (["mp3", "m4a", "aac", "ogg", "oga", "wav", "flac"].includes(ext)) return Music2;
  if (["mp4", "m4v", "mov", "webm", "mkv"].includes(ext)) return FileVideo;
  if (["pdf", "txt", "md", "csv"].includes(ext)) return FileText;
  return File;
}

async function refresh(cursor = "", rethrow = false) {
  try {
    const scope = includeDevices.value ? (selectedDevice.value ? `device:${selectedDevice.value}` : "all") : "cloud";
    const input = { query: cloudQuery.value, scope, lifecycle: lifecycle.value, folderId: selectedFolder.value, tags: selectedTags.value, type: selectedType.value || undefined, grouped: grouped.value === "" ? undefined : grouped.value === "true", cloudCopy: cloudCopy.value === "" ? undefined : cloudCopy.value === "true", readable: readable.value === "" ? undefined : readable.value === "true", sort: sort.value, cursor, limit: 50 };
    let page;
    try { page = offlineMode.value && cursor.startsWith("offline:") ? searchOfflineIndex(input) : await searchLibraryFiles(input); if (!page) throw new Error("离线索引已变化"); offlineMode.value = cursor.startsWith("offline:"); }
    catch (cause) {
      if (cause instanceof ApiError && cause.status < 500 && cause.status !== 0 || cursor && !cursor.startsWith("offline:")) throw cause;
      page = searchOfflineIndex(input);
      if (!page || offlineStatus.value && view.value.nodeId && (offlineStatus.value.nodeId !== view.value.nodeId || view.value.libraryId && offlineStatus.value.libraryId !== view.value.libraryId)) throw cause;
      offlineMode.value = true;
    }
    if (!offlineMode.value && offlineStatus.value && page.libraryId !== offlineStatus.value.libraryId) lockLocalIndex();
    const files = cursor ? [...view.value.files, ...page.items] : page.items;
    view.value = { files, jobs: page.jobs, nodeId: page.nodeId, libraryId: page.libraryId, peers: page.peers ?? [], peerStatus: page.peerStatus, directEndpoint: page.directEndpoint, lan: page.lan };
    searchTotal.value = page.total;
    nextCursor.value = page.nextCursor;
    searchFacets.value = page.facets;
    coverage.value = page.coverage;
    error.value = "";
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取资源库索引"; if (rethrow) throw cause; }
}
async function enableOfflineIndex() {
  const passphrase = window.prompt("设置本地离线索引口令（仅用于此浏览器加密存储）");
  if (!passphrase) return;
  try {
    const count = await saveOfflineIndex(view.value.nodeId, passphrase);
    offlineStatus.value = offlineIndexStatus();
    error.value = `已加密保存 ${count} 条离线元数据`;
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "保存离线索引失败"; }
}
async function unlockLocalIndex() {
  const passphrase = window.prompt("输入本地离线索引口令");
  if (!passphrase) return;
  try { await unlockOfflineIndex(passphrase, view.value.nodeId || undefined); offlineStatus.value = offlineIndexStatus(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法解锁离线索引"; }
}
function lockLocalIndex() { lockedOfflineIndex(); offlineStatus.value = null; offlineMode.value = false; }
function clearSearchFilters() {
  cloudQuery.value = "";
  selectedFolder.value = "";
  selectedTags.value = [];
  selectedDevice.value = "";
  selectedType.value = "";
  grouped.value = "";
  cloudCopy.value = "";
  readable.value = "";
  sort.value = "name_asc";
  void refresh();
}
async function loadMore() {
  if (!nextCursor.value || loadingMore.value) return;
  loadingMore.value = true;
  try { await refresh(nextCursor.value, true); }
  catch (cause) {
    // A changed index invalidates the cursor (§6.1). Reload the first page
    // instead of silently dropping or repeating rows.
    if (cause instanceof ApiError && cause.status === 409) { nextCursor.value = ""; await refresh(""); }
  }
  finally { loadingMore.value = false; }
}
async function loadSavedQueries() {
  savedQueriesOpen.value = true;
  try { savedQueries.value = await getLibrarySavedQueries(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取常用查询"; }
}
async function saveCurrentQuery() {
  const name = window.prompt("为当前检索条件命名（保存条件而非结果）：");
  if (name === null) return;
  const trimmed = name.trim();
  if (!trimmed) return;
  try {
    await createLibrarySavedQuery(trimmed, { scope: includeDevices.value ? (selectedDevice.value ? `device:${selectedDevice.value}` : "all") : "cloud", lifecycle: lifecycle.value, query: cloudQuery.value.trim(), folderId: selectedFolder.value, tags: [...selectedTags.value], type: selectedType.value || undefined, grouped: grouped.value === "" ? undefined : grouped.value === "true", cloudCopy: cloudCopy.value === "" ? undefined : cloudCopy.value === "true", readable: readable.value === "" ? undefined : readable.value === "true", sort: sort.value });
    savedQueries.value = await getLibrarySavedQueries();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法保存常用查询"; }
}
function applySavedQuery(saved: LibrarySavedQuery) {
  cloudQuery.value = saved.filters.query;
  includeDevices.value = saved.filters.scope !== "cloud";
  lifecycle.value = saved.filters.lifecycle || "active";
  selectedDevice.value = saved.filters.scope.startsWith("device:") ? saved.filters.scope.slice(7) : "";
  selectedType.value = saved.filters.type || "";
  grouped.value = saved.filters.grouped === undefined ? "" : String(saved.filters.grouped) as "true" | "false";
  cloudCopy.value = saved.filters.cloudCopy === undefined ? "" : String(saved.filters.cloudCopy) as "true" | "false";
  readable.value = saved.filters.readable === undefined ? "" : String(saved.filters.readable) as "true" | "false";
  selectedFolder.value = saved.filters.folderId;
  selectedTags.value = [...saved.filters.tags];
  sort.value = saved.filters.sort;
  savedQueriesOpen.value = false;
  void refresh();
}
async function removeSavedQuery(saved: LibrarySavedQuery) {
  try { await deleteLibrarySavedQuery(saved.id); savedQueries.value = await getLibrarySavedQueries(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法删除常用查询"; }
}
async function show(initialQuery?: string) {
  if (initialQuery !== undefined) cloudQuery.value = initialQuery.trim();
  open.value = true;
  loading.value = true;
  await refresh();
  loading.value = false;
}
defineExpose({ open: show });
function openDetail(file: CloudFile) { detailFile.value = file; }
function openResourceFromDetail(id: string) { open.value = false; emit("openResource", id); }
async function changeLifecycle(file: CloudFile) {
  const state = lifecycle.value === "active" ? "trashed" : "active";
  if (state === "trashed" && !window.confirm(`将“${file.name}”移入回收站？文件原件和副本会保留。`)) return;
  const id = file.fileId || file.id;
  try { await setLibraryFileLifecycle(id, state); await refresh(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法更新回收状态"; }
}
async function deleteLibraryEntry(file: CloudFile) {
  if (!window.confirm(`永久删除“${file.name}”的资源库条目？此操作不能恢复；原件与云块仍等待安全回收。`)) return;
  try { await setLibraryFileLifecycle(file.fileId || file.id, "deleted"); await refresh(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法删除资源库条目"; }
}
async function toggleVersions(id: string) {
  if (versions.value[id]) { delete versions.value[id]; versions.value = { ...versions.value }; return; }
  try { versions.value = { ...versions.value, [id]: await getCloudVersions(id) }; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取历史"; }
}
function formatSize(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}
watch(open, (value) => {
  if (timer) clearInterval(timer);
  if (value) timer = setInterval(() => void refresh(), 3000);
  else { activeVideo.value = ""; closePreview(); }
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); closePreview(); });
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
      <section class="library-search-dialog" role="dialog" aria-modal="true" aria-label="统一检索">
        <header class="hub-settings-header">
          <div><strong>统一检索</strong><small>跨设备与云库查找文件，重复项自动合并。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
        </header>
        <div class="library-search-controls">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <label class="library-search-field">
            <Search :size="16" />
            <input v-model="cloudQuery" type="search" placeholder="搜索文件名、文件夹或标签" aria-label="统一检索文件" @change="refresh()" @keyup.enter="refresh()" />
            <button v-if="cloudQuery" class="search-clear" type="button" aria-label="清空搜索" @click="cloudQuery = ''; refresh()"><X :size="14" /></button>
          </label>
          <div class="library-scope-row">
            <div class="library-scope-switch" role="group" aria-label="检索范围">
              <button type="button" :class="{ active: includeDevices }" @click="includeDevices = true; refresh()">全部来源</button>
              <button type="button" :class="{ active: !includeDevices }" @click="includeDevices = false; refresh()">仅云库</button>
            </div>
            <span v-if="offlineMode" class="library-pill library-pill-amber">离线元数据</span>
            <span v-else-if="offlineStatus" class="library-pill">本地索引 {{ offlineStatus.count }} 条</span>
            <div class="library-toolbar">
              <button class="button button-secondary" type="button" @click="loadSavedQueries"><Bookmark :size="14" />常用查询</button>
              <button class="button button-secondary" type="button" @click="saveCurrentQuery"><BookmarkPlus :size="14" />保存条件</button>
              <button class="button button-secondary" type="button" :aria-expanded="offlinePanelOpen" @click="offlinePanelOpen = !offlinePanelOpen"><HardDrive :size="14" />离线索引</button>
              <button class="quiet-button" type="button" aria-label="刷新" @click="refresh()"><RefreshCw :size="15" /></button>
            </div>
          </div>
          <div class="library-filter-bar">
            <label>文件夹<select v-model="selectedFolder" @change="refresh()"><option value="">全部文件夹</option><option v-for="folder in searchFacets.folders" :key="folder.id" :value="folder.id">{{ folder.name }}（{{ folder.count }}）</option></select></label>
            <label>类型<select v-model="selectedType" @change="refresh()"><option value="">全部类型</option><option value="image">图片</option><option value="video">视频</option><option value="audio">音频</option><option value="document">文档</option><option value="other">其他</option></select></label>
            <button class="button button-secondary tag-filter-toggle" type="button" :class="{ selected: selectedTags.length }" :aria-expanded="tagPickerOpen" @click="tagPickerOpen = !tagPickerOpen"><Tag :size="13" /><span>标签{{ selectedTags.length ? ' · ' + selectedTags.length : '' }}</span><ChevronDown :size="13" /></button>
            <label>状态<select v-model="lifecycle" @change="refresh()"><option value="active">资源库</option><option value="trashed">回收站</option></select></label>
            <label>排序<select v-model="sort" @change="refresh()"><option value="name_asc">名称 A–Z</option><option value="name_desc">名称 Z–A</option><option value="modified_desc">最近修改</option><option value="modified_asc">最早修改</option><option value="ingested_desc">最近入库</option><option value="ingested_asc">最早入库</option></select></label>
            <button class="button button-secondary tag-filter-toggle" type="button" :class="{ selected: selectedDevice || grouped || cloudCopy || readable }" :aria-expanded="moreFiltersOpen" @click="moreFiltersOpen = !moreFiltersOpen"><span>更多筛选</span><ChevronDown :size="13" /></button>
            <button v-if="hasActiveFilters" class="quiet-button library-clear-filters" type="button" @click="clearSearchFilters">清除筛选</button>
          </div>
          <div v-if="moreFiltersOpen" class="library-filter-bar library-filter-more">
            <label v-if="includeDevices">来源设备<select v-model="selectedDevice" @change="refresh()"><option value="">所有设备</option><option v-for="node in coverage.knownNodes || coverage.sources || []" :key="node" :value="node">{{ nodeLabel(node) }}{{ coverage.unindexedSources?.includes(node) ? '（暂无文件）' : '' }}</option></select></label>
            <label>归组<select v-model="grouped" @change="refresh()"><option value="">全部</option><option value="true">已归组</option><option value="false">未归组</option></select></label>
            <label>云库副本<select v-model="cloudCopy" @change="refresh()"><option value="">全部</option><option value="true">有云库副本</option><option value="false">仅设备来源</option></select></label>
            <label>当前可读<select v-model="readable" @change="refresh()"><option value="">全部</option><option value="true">可读</option><option value="false">暂不可读</option></select></label>
          </div>
          <div v-if="tagPickerOpen" class="tag-filter-panel">
            <div class="tag-filter-panel-heading"><strong>按标签筛选</strong><button type="button" aria-label="收起标签筛选" @click="tagPickerOpen = false"><ChevronDown :size="15" /></button></div>
            <label class="tag-filter-search"><Search :size="14" /><input v-model="tagPickerQuery" type="search" placeholder="查找标签" autocomplete="off" /></label>
            <div class="tag-filter-list">
              <button v-for="tag in matchingFacetTags" :key="tag.name" class="tag-filter-option" type="button" :class="{ active: selectedTags.includes(tag.name) }" :aria-pressed="selectedTags.includes(tag.name)" @click="toggleFacetTag(tag.name)"><span>{{ tag.name }}</span><small>{{ tag.count }}</small><Check v-if="selectedTags.includes(tag.name)" :size="14" /></button>
              <p v-if="!matchingFacetTags.length" class="tag-filter-empty">{{ searchFacets.tags.length ? "没有匹配的标签" : "当前范围内还没有标签" }}</p>
            </div>
            <button v-if="selectedTags.length" class="tag-filter-clear" type="button" @click="selectedTags = []; refresh()">清除已选标签</button>
          </div>
          <div v-if="savedQueriesOpen" class="library-subpanel">
            <div class="library-subpanel-heading"><strong>常用查询</strong><button class="quiet-button" type="button" aria-label="收起常用查询" @click="savedQueriesOpen = false"><X :size="14" /></button></div>
            <p v-if="!savedQueries.length">还没有保存的查询。设置筛选条件后点击“保存条件”。</p>
            <div v-for="saved in savedQueries" :key="saved.id" class="hub-profile-row">
              <div><strong>{{ saved.name }}</strong><small>{{ saved.filters.scope === "all" ? "全部来源" : saved.filters.scope === "cloud" ? "仅云库" : saved.filters.scope.startsWith("device:") ? "设备 " + nodeLabel(saved.filters.scope.slice(7)) : "本机" }}<span v-if="saved.filters.query"> · “{{ saved.filters.query }}”</span><span v-if="saved.filters.tags.length"> · 标签 {{ saved.filters.tags.join("、") }}</span></small></div>
              <button class="button button-secondary" type="button" @click="applySavedQuery(saved)">应用</button>
              <button class="button button-secondary" type="button" @click="removeSavedQuery(saved)">删除</button>
            </div>
          </div>
          <div v-if="offlinePanelOpen" class="library-subpanel">
            <div class="library-subpanel-heading"><strong>加密离线索引</strong><button class="quiet-button" type="button" aria-label="收起离线索引" @click="offlinePanelOpen = false"><X :size="14" /></button></div>
            <p>在本浏览器加密保存目录信息，设备不可达时仍可检索；不保存文件内容。</p>
            <div class="library-subpanel-actions">
              <button class="button button-secondary" type="button" :disabled="!view.nodeId" @click="enableOfflineIndex">开启并更新</button>
              <button class="button button-secondary" type="button" @click="unlockLocalIndex">解锁本地索引</button>
              <button v-if="offlineStatus" class="button button-secondary" type="button" @click="lockLocalIndex">锁定</button>
            </div>
            <p v-if="offlineStatus">{{ offlineStatus.count }} 条 · {{ offlineStatus.syncedAt }} · 来源 {{ nodeLabel(offlineStatus.nodeId) }}</p>
          </div>
          <p v-if="offlineMode" class="library-notice">当前为离线元数据，连接设备后才能打开或修改文件。</p>
        </div>
        <div class="library-search-results">
          <div class="library-results-toolbar">
            <span v-if="loading">正在读取资源库索引…</span>
            <span v-else>找到 <strong>{{ searchTotal }}</strong> 个文件<span v-if="view.files.length && view.files.length < searchTotal"> · 已显示 {{ view.files.length }} 个</span></span>
            <button class="quiet-button" type="button" :aria-expanded="coverageOpen" @click="coverageOpen = !coverageOpen"><ChevronDown :size="14" />索引详情</button>
          </div>
          <div v-if="coverageOpen" class="library-subpanel">
            <div class="library-subpanel-heading"><strong>索引覆盖</strong><button class="quiet-button" type="button" aria-label="收起索引详情" @click="coverageOpen = false"><X :size="14" /></button></div>
            <p>未同步的设备文件不会出现在这里；受保护的文件请到来源设备解锁查看。</p>
            <p v-if="coverage.sources?.length" :title="coverage.generation ? `索引代次 ${coverage.generation}` : undefined">覆盖 {{ coverage.sources.length }} 个来源设备<span v-if="coverage.lastCatalogAt"> · 最近更新 {{ coverage.lastCatalogAt }}</span></p>
            <p v-for="source in coverage.sources || []" :key="source" :title="source">{{ nodeLabel(source) }} · {{ coverage.sourceLastCatalogAt?.[source] || "尚无更新时间" }}<span v-if="coverage.offlineSources?.includes(source)"> · 当前离线</span></p>
            <p v-for="source in coverage.unindexedSources || []" :key="source + '-unindexed'" :title="source">{{ nodeLabel(source) }} · 暂无已索引文件<span v-if="coverage.offlineSources?.includes(source)"> · 当前离线</span></p>
          </div>
          <div v-if="loading" class="library-loading"><LoaderCircle class="spin" :size="16" />正在读取资源库索引…</div>
          <div v-else-if="!view.files.length" class="library-empty">
            <span class="empty-icon"><FolderOpen :size="22" /></span>
            <h3>{{ hasActiveFilters ? "没有匹配的文件" : "尚无已同步的文件" }}</h3>
            <p>{{ hasActiveFilters ? "试试清除筛选条件，或更换关键词。" : "设备发布索引或文件加入云库后会显示在这里。" }}</p>
            <button v-if="hasActiveFilters" class="button button-secondary" type="button" @click="clearSearchFilters">清除筛选</button>
          </div>
          <div v-else class="library-result-list">
            <article v-for="file in view.files" :key="file.id" class="library-result">
              <div class="library-result-row">
                <span class="library-result-icon"><component :is="fileIcon(file)" :size="17" /></span>
                <div class="library-result-copy">
                  <strong :title="file.name">{{ file.name }}</strong>
                  <small v-if="offlineMode">{{ file.folderName || "未归组" }} · {{ formatSize(file.originalSize ?? file.size) }} · 离线元数据</small>
                  <small v-else-if="file.localOnly">未上传云库 · {{ formatSize(file.originalSize) }} · {{ file.locations?.length || 0 }} 处来源<span v-if="file.folderName"> · {{ file.folderName }}</span><span v-if="(file.revisionCount || 0) > 1"> · {{ file.revisionCount }} 个版本</span></small>
                  <small v-else>{{ formatSize(file.originalSize ?? file.size) }} · {{ file.availableReplicas ?? "—" }}/{{ file.replicas ?? file.minReplicas }} 份副本可用<span v-if="file.folderName"> · {{ file.folderName }}</span><span v-if="(file.revisionCount || 0) > 1"> · {{ file.revisionCount }} 个版本</span></small>
                  <div v-if="file.tags?.length" class="library-result-tags"><span v-for="tag in file.tags" :key="tag">{{ tag }}</span></div>
                </div>
                <div v-if="!offlineMode" class="library-result-actions">
                  <button v-if="previewKind(file) && ((file.revisionCount || 0) <= 1 || file.currentRevision)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="preview(file)"><LoaderCircle v-if="activeDownload === file.id" class="spin" :size="13" /><Play v-else :size="13" />{{ activeDownload === file.id ? "读取中" : activeVideo === file.id || activePreview?.fileID === file.id ? "收起预览" : "预览" }}</button>
                  <button v-if="(file.revisionCount || 0) <= 1 || file.currentRevision" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file, file.name)"><LoaderCircle v-if="activeDownload === file.id" class="spin" :size="13" /><Download v-else :size="13" />下载</button>
                  <button v-if="remote && file.localOnly && readableLocation(file) && ((file.revisionCount || 0) <= 1 || file.currentRevision)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="openDeviceFile(file)"><ExternalLink :size="13" />打开来源</button>
                  <button class="button button-secondary" type="button" @click="openDetail(file)">详情</button>
                  <button v-if="file.history" class="button button-ghost" type="button" @click="toggleVersions(file.id)">{{ versions[file.id] ? "收起历史" : "历史" }}</button>
                  <button class="button button-ghost" type="button" @click="changeLifecycle(file)">{{ lifecycle === "active" ? "移入回收站" : "恢复" }}</button>
                  <button v-if="lifecycle === 'trashed'" class="button button-ghost library-danger" type="button" @click="deleteLibraryEntry(file)">永久删除条目</button>
                </div>
              </div>
              <p v-if="!offlineMode && (file.revisionCount || 0) > 1 && !file.currentRevision" class="library-result-note">存在多个版本，请在详情中选择当前版后再预览或下载。</p>
              <p v-else-if="!offlineMode && file.locations?.some((location) => !location.resourceRef)" class="library-result-note muted">部分结果来自旧索引，设备重新同步后可用。</p>
              <div v-if="activeVideo === file.id" class="library-result-media"><div class="cloud-video-preview"><StreamingVideo :id="file.id" :name="file.name" :remote="remote" cloud :qualities="file.variants" :file-size="file.originalSize" :file-id="file.fileId || file.id" :revision-id="file.revisionId" /></div></div>
              <div v-if="activePreview?.fileID === file.id" class="library-result-media"><div class="cloud-file-preview"><button class="quiet-button" type="button" @click="closePreview">关闭预览<X :size="14" /></button><img v-if="activePreview.kind === 'image'" :src="activePreview.url" :alt="file.name" /><audio v-else-if="activePreview.kind === 'audio'" :src="activePreview.url" controls preload="metadata" /><video v-else-if="activePreview.kind === 'video'" :src="activePreview.url" controls preload="metadata" /><iframe v-else :src="activePreview.url" :title="file.name" /></div></div>
              <div v-if="versions[file.id]" class="library-result-history">
                <div v-for="version in versions[file.id]" :key="version.revisionId" class="hub-profile-row">
                  <div><strong>{{ version.name }}</strong><small>{{ version.modified }} · {{ formatSize(version.size) }}</small></div>
                  <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file, version.name, version.revisionId)"><Download :size="13" />下载此版</button>
                </div>
              </div>
            </article>
          </div>
          <div v-if="nextCursor" class="library-load-more">
            <button class="button button-secondary" type="button" :disabled="loadingMore" @click="loadMore"><LoaderCircle v-if="loadingMore" class="spin" :size="14" /><RefreshCw v-else :size="14" />{{ loadingMore ? "正在读取" : "加载更多" }}</button>
          </div>
        </div>
      </section>
    </div>
  </Teleport>
  <CloudFileDetail v-if="detailFile" :file="detailFile" :remote="remote" :nodes="nodes ?? []" :query-node-id="view.nodeId" @close="detailFile = null" @changed="refresh()" @open-resource="openResourceFromDetail" />
</template>