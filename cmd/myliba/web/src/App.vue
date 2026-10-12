<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  Check,
  ChevronDown,
  CircleAlert,
  Clock3,
  Download,
  ExternalLink,
  File,
  FileText,
  FileVideo,
  FolderOpen,
  FolderSync,
  HardDrive,
  House,
  Image,
  LayoutGrid,
  LibraryBig,
  List,
  LoaderCircle,
  LogOut,
  Music2,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Tag,
  Wifi,
  WifiOff,
  X,
} from "@lucide/vue";
import {
  addBatchTag,
  addResourceTag,
  addRoot,
  ApiError,
  fileURL,
  getBatchTagSuggestions,
  getResources,
  getRoots,
  getDirectories,
  getSession,
  getTagSuggestions,
  getTags,
  login,
  logout,
  scanRoot,
  type DirectoryListing,
  type BatchTagSuggestion,
  type Resource,
  type RootSummary,
  type TagSummary,
  type TagSuggestion,
} from "./api";

const remoteMode = ref(false);
const authenticated = ref(true);
const nodeOnline = ref(false);
const loading = ref(false);
const initialized = ref(false);
const errorMessage = ref("");
const resources = ref<Resource[]>([]);
const roots = ref<RootSummary[]>([]);
const tags = ref<TagSummary[]>([]);
const total = ref(0);
const query = ref("");
const selectedTag = ref("");
const unorganizedOnly = ref(false);
const pageOffset = ref(0);
const pageSize = 48;
const selectedIDs = ref(new Set<string>());
const bulkTagName = ref("");
const batchSuggestions = ref<BatchTagSuggestion[]>([]);
const batchSuggestionsOpen = ref(false);
const loadingBatchSuggestions = ref(false);
const batchSuggestionsScanned = ref(0);
const applyingBatchSuggestion = ref("");
const rootPath = ref("");
const rootBusy = ref(false);
const pickerBusy = ref(false);
const directoryPickerOpen = ref(false);
const directoryListing = ref<DirectoryListing | null>(null);
const directoryError = ref("");
const scanningRootID = ref("");
const editingTagID = ref("");
const editingTagName = ref("");
const addingTagID = ref("");
const tagSuggestions = ref<TagSuggestion[]>([]);
const loadingTagSuggestions = ref(false);
const password = ref("");
const loginBusy = ref(false);
const loginError = ref("");
const toastMessage = ref("");
const toastVisible = ref(false);
const listMode = ref<"grid" | "list">("grid");

let debounceTimer: ReturnType<typeof setTimeout> | undefined;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let statusTimer: ReturnType<typeof setInterval> | undefined;
let requestGeneration = 0;

const onShortcut = (event: KeyboardEvent) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    document.querySelector<HTMLInputElement>("#resource-search")?.focus();
  }
};

const onTagEditorOutside = (event: MouseEvent) => {
  if (!editingTagID.value) return;
  const target = event.target;
  if (target instanceof Element && target.closest(".tag-editor-wrap")) return;
  closeTagEditor();
};

const pageTitle = computed(() => selectedTag.value
  ? `标签：${selectedTag.value}`
  : unorganizedOnly.value
    ? "待整理"
    : query.value.trim()
      ? "搜索结果"
      : "全部资源");
const selectedCount = computed(() => selectedIDs.value.size);
const totalIndexed = computed(() => roots.value.reduce((sum, root) => sum + root.count, 0));
const hasMore = computed(() => resources.value.length < total.value);
const isFiltered = computed(() => Boolean(query.value.trim() || selectedTag.value || unorganizedOnly.value));

function formatBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

function formatDate(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "时间未知";
  return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "short", day: "numeric" }).format(date);
}

function kindLabel(kind: string): string {
  const labels: Record<string, string> = { image: "图片", video: "视频", audio: "音频", document: "文档", file: "文件" };
  return labels[kind] || "文件";
}

function kindIcon(kind: string) {
  if (kind === "image") return Image;
  if (kind === "video") return FileVideo;
  if (kind === "audio") return Music2;
  if (kind === "document") return FileText;
  return File;
}

function notify(message: string) {
  toastMessage.value = message;
  toastVisible.value = true;
  if (toastTimer) clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toastVisible.value = false; }, 2400);
}

function showError(error: unknown) {
  if (error instanceof ApiError && error.status === 401 && remoteMode.value) return;
  errorMessage.value = error instanceof Error ? error.message : "发生了意外错误";
}

async function loadResources(append = false) {
  const generation = ++requestGeneration;
  if (!append) {
    pageOffset.value = 0;
    loading.value = true;
  }
  errorMessage.value = "";
  try {
    const result = await getResources({
      query: query.value.trim(),
      tag: selectedTag.value,
      unorganized: unorganizedOnly.value,
      limit: pageSize,
      offset: append ? pageOffset.value : 0,
    });
    if (generation !== requestGeneration) return;
    resources.value = append ? [...resources.value, ...result.items] : result.items;
    total.value = result.total;
    selectedIDs.value = new Set();
  } catch (error) {
    if (generation === requestGeneration) showError(error);
  } finally {
    if (generation === requestGeneration) loading.value = false;
  }
}

async function loadRoots() {
  try { roots.value = await getRoots(); }
  catch (error) { showError(error); }
}

async function loadTags() {
  try { tags.value = await getTags(); }
  catch (error) { showError(error); }
}

async function loadWorkspace() {
  await Promise.all([loadRoots(), loadTags(), loadResources()]);
}

function chooseAll() {
  selectedTag.value = "";
  unorganizedOnly.value = false;
  query.value = "";
  void loadResources();
}

function chooseUnorganized() {
  selectedTag.value = "";
  unorganizedOnly.value = true;
  void loadResources();
}

function chooseTag(name: string) {
  selectedTag.value = selectedTag.value === name ? "" : name;
  unorganizedOnly.value = false;
  void loadResources();
}

function toggleSelection(id: string) {
  const next = new Set(selectedIDs.value);
  if (next.has(id)) next.delete(id);
  else next.add(id);
  selectedIDs.value = next;
}

async function addTagToResource(id: string, suggestedName = "") {
  const name = (suggestedName || editingTagName.value).trim();
  if (!name || addingTagID.value) return;
  addingTagID.value = id;
  try {
    await addResourceTag(id, name);
    await Promise.all([loadTags(), loadResources()]);
    notify(`已添加标签「${name}」`);
    if (editingTagID.value === id) {
      editingTagName.value = "";
      await refreshTagSuggestions(id);
    }
  } catch (error) { showError(error); }
  finally { if (addingTagID.value === id) addingTagID.value = ""; }
}

async function openTagEditor(resource: Resource) {
  editingTagID.value = resource.id;
  editingTagName.value = "";
  await refreshTagSuggestions(resource.id);
}

function closeTagEditor() {
  editingTagID.value = "";
  editingTagName.value = "";
  tagSuggestions.value = [];
  loadingTagSuggestions.value = false;
}

async function refreshTagSuggestions(resourceID: string) {
  tagSuggestions.value = [];
  loadingTagSuggestions.value = true;
  try {
    const suggestions = await getTagSuggestions(resourceID);
    if (editingTagID.value === resourceID) tagSuggestions.value = suggestions;
  } catch {
    if (editingTagID.value === resourceID) tagSuggestions.value = [];
  } finally {
    if (editingTagID.value === resourceID) loadingTagSuggestions.value = false;
  }
}

async function applyBatchTag() {
  const name = bulkTagName.value.trim();
  if (!name || !selectedIDs.value.size) {
    notify("先选择资源并输入标签");
    return;
  }
  try {
    const result = await addBatchTag([...selectedIDs.value], name);
    bulkTagName.value = "";
    await Promise.all([loadResources(), loadTags()]);
    notify(`已为 ${result.updated} 项添加「${result.tag}」`);
  } catch (error) { showError(error); }
}

async function toggleBatchSuggestions() {
  if (batchSuggestionsOpen.value) {
    batchSuggestionsOpen.value = false;
    return;
  }
  batchSuggestionsOpen.value = true;
  await loadBatchSuggestions();
}

async function loadBatchSuggestions() {
  loadingBatchSuggestions.value = true;
  try {
    const result = await getBatchTagSuggestions();
    batchSuggestions.value = result.suggestions;
    batchSuggestionsScanned.value = result.scanned;
  } catch (error) { showError(error); }
  finally { loadingBatchSuggestions.value = false; }
}

async function applyBatchSuggestion(suggestion: BatchTagSuggestion) {
  const key = suggestion.name.toLocaleLowerCase();
  if (applyingBatchSuggestion.value || !suggestion.ids.length) return;
  applyingBatchSuggestion.value = key;
  try {
    const result = await addBatchTag(suggestion.ids, suggestion.name);
    await Promise.all([loadTags(), loadResources()]);
    notify(`已为 ${result.updated} 项添加标签「${result.tag}」`);
    await loadBatchSuggestions();
  } catch (error) { showError(error); }
  finally { applyingBatchSuggestion.value = ""; }
}

async function submitRoot() {
  const path = rootPath.value.trim();
  if (!path || rootBusy.value) return;
  rootBusy.value = true;
  errorMessage.value = "";
  try {
    const root = await addRoot(path);
    const result = await scanRoot(root.id);
    rootPath.value = "";
    notify(`扫描完成，收录 ${result.indexed} 个文件`);
    await loadWorkspace();
  } catch (error) { showError(error); }
  finally { rootBusy.value = false; }
}

async function openFolderPicker() {
  if (rootBusy.value || directoryPickerOpen.value) return;
  directoryPickerOpen.value = true;
  directoryListing.value = null;
  directoryError.value = "";
  await browseDirectories("");
}

async function browseDirectories(path: string) {
  if (pickerBusy.value) return;
  pickerBusy.value = true;
  try {
    directoryListing.value = await getDirectories(path);
    directoryError.value = "";
  } catch (error) {
    directoryError.value = error instanceof Error ? error.message : "无法读取此目录";
  }
  finally { pickerBusy.value = false; }
}

async function selectCurrentDirectory() {
  const path = directoryListing.value?.current;
  if (!path) return;
  directoryPickerOpen.value = false;
  rootPath.value = path;
  await submitRoot();
}

async function rescanRoot(root: RootSummary) {
  scanningRootID.value = root.id;
  try {
    const result = await scanRoot(root.id);
    notify(`扫描完成，收录 ${result.indexed} 个文件`);
    await loadWorkspace();
  } catch (error) { showError(error); }
  finally { scanningRootID.value = ""; }
}

async function submitLogin() {
  if (loginBusy.value) return;
  loginBusy.value = true;
  loginError.value = "";
  try {
    const session = await login(password.value);
    authenticated.value = session.authenticated;
    nodeOnline.value = session.nodeOnline;
    password.value = "";
    await loadWorkspace();
    startStatusPolling();
  } catch (error) { loginError.value = error instanceof Error ? error.message : "登录失败"; }
  finally { loginBusy.value = false; }
}

async function signOut() {
  try { await logout(); }
  catch (error) { showError(error); }
  authenticated.value = false;
  password.value = "";
  stopStatusPolling();
}

function handleUnauthorized() {
  if (!remoteMode.value) return;
  authenticated.value = false;
  stopStatusPolling();
}

async function refreshNodeStatus() {
  try {
    const session = await getSession();
    if (session) {
      if (!session.authenticated) {
        authenticated.value = false;
        stopStatusPolling();
      }
      nodeOnline.value = session.nodeOnline;
    }
  } catch { nodeOnline.value = false; }
}

function startStatusPolling() {
  stopStatusPolling();
  if (remoteMode.value) statusTimer = setInterval(() => void refreshNodeStatus(), 30000);
}

function stopStatusPolling() {
  if (statusTimer) clearInterval(statusTimer);
  statusTimer = undefined;
}

watch(query, () => {
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => { void loadResources(); }, 220);
});

onMounted(async () => {
  window.addEventListener("myliba:unauthorized", handleUnauthorized);
  window.addEventListener("keydown", onShortcut);
  document.addEventListener("click", onTagEditorOutside, true);
  try {
    const session = await getSession();
    if (!session) {
      remoteMode.value = false;
      authenticated.value = true;
    } else {
      remoteMode.value = true;
      authenticated.value = session.authenticated;
      nodeOnline.value = session.nodeOnline;
    }
    if (authenticated.value) {
      await loadWorkspace();
      startStatusPolling();
    }
  } catch (error) { showError(error); }
  finally { initialized.value = true; }

});

onBeforeUnmount(() => {
  window.removeEventListener("myliba:unauthorized", handleUnauthorized);
  window.removeEventListener("keydown", onShortcut);
  document.removeEventListener("click", onTagEditorOutside, true);
  stopStatusPolling();
  if (debounceTimer) clearTimeout(debounceTimer);
  if (toastTimer) clearTimeout(toastTimer);
});
</script>

<template>
  <div class="app-frame">
    <aside class="sidebar">
      <a class="brand" href="/" aria-label="Myliba 首页">
        <span class="brand-mark"><LibraryBig :size="19" :stroke-width="1.8" /></span>
        <span class="brand-copy"><strong>myliba</strong><small>PERSONAL LIBRARY</small></span>
      </a>

      <div class="sidebar-section-label">资源空间</div>
      <nav class="primary-nav" aria-label="资源空间">
        <button class="nav-link" :class="{ active: !selectedTag && !unorganizedOnly }" @click="chooseAll">
          <House :size="17" /><span>全部资源</span><span class="nav-count">{{ totalIndexed }}</span>
        </button>
        <button class="nav-link" :class="{ active: unorganizedOnly }" @click="chooseUnorganized">
          <Clock3 :size="17" /><span>待整理</span>
        </button>
      </nav>

      <div class="sidebar-section-label tag-section-title">
        <span>标签</span><span class="label-count">{{ tags.length }}</span>
      </div>
      <nav class="tag-nav" aria-label="按标签筛选">
        <button v-for="tag in tags" :key="tag.id" class="tag-nav-link" :class="{ active: selectedTag === tag.name }" @click="chooseTag(tag.name)">
          <Tag :size="15" /><span class="tag-nav-name">{{ tag.name }}</span><span class="tag-count">{{ tag.count }}</span>
        </button>
        <p v-if="!tags.length" class="sidebar-empty">给资源添加标签后，会显示在这里。</p>
      </nav>

      <div class="sidebar-bottom">
        <div class="node-status-card">
          <span class="node-status-icon" :class="{ offline: remoteMode && !nodeOnline }">
            <component :is="remoteMode && !nodeOnline ? WifiOff : remoteMode ? Wifi : HardDrive" :size="17" />
          </span>
          <span class="node-status-copy"><strong>{{ remoteMode ? "家中节点" : "本机节点" }}</strong><small>{{ remoteMode ? (nodeOnline ? "在线 · 可访问资源" : "离线 · 暂不可访问") : "索引保存在此设备" }}</small></span>
          <span class="status-dot" :class="{ offline: remoteMode && !nodeOnline }"></span>
        </div>
        <p class="sidebar-note"><span class="privacy-mark">◆</span> 原文件保留在原位置</p>
      </div>
    </aside>

    <div class="main-column">
      <header class="topbar">
        <div class="breadcrumbs"><span>我的资源</span><span class="breadcrumb-divider">/</span><strong>{{ pageTitle }}</strong></div>
        <div class="topbar-actions">
          <span class="connection-badge" :class="{ offline: remoteMode && !nodeOnline }">
            <span class="connection-dot"></span>{{ remoteMode ? (nodeOnline ? "家中节点在线" : "家中节点离线") : "本机模式" }}
          </span>
          <button v-if="remoteMode && authenticated" class="quiet-button signout-button" title="退出登录" @click="signOut"><LogOut :size="15" /><span>退出</span></button>
        </div>
      </header>

      <main class="page-content">
        <section class="page-intro">
          <div>
            <div class="eyebrow"><Sparkles :size="13" />个人资源空间</div>
            <h1>让每份资源，都有迹可循。</h1>
            <p class="intro-copy">统一搜索和整理你的文件，原文件仍留在原来的位置。</p>
          </div>
          <div class="library-stat">
            <span class="stat-icon"><LibraryBig :size="18" /></span>
            <span><strong>{{ totalIndexed.toLocaleString("zh-CN") }}</strong><small>已收录资源</small></span>
          </div>
        </section>

        <section v-if="!remoteMode" class="roots-panel">
          <div class="panel-heading">
            <span class="panel-icon"><FolderOpen :size="18" /></span>
            <span class="panel-heading-copy"><strong>索引目录</strong><small>选择要纳入资源库的文件夹，不会移动或修改原文件。</small></span>
          </div>
          <form class="root-form" @submit.prevent="submitRoot">
            <label class="sr-only" for="root-path">文件夹路径</label>
            <input id="root-path" v-model="rootPath" type="text" placeholder="文件夹路径" autocomplete="off" />
            <button class="button button-secondary picker-button" type="button" :disabled="rootBusy" @click="openFolderPicker"><FolderOpen :size="16" /><span>浏览文件夹</span></button>
            <button class="button button-primary" type="submit" :disabled="rootBusy || !rootPath.trim()">
              <LoaderCircle v-if="rootBusy" class="spin" :size="16" /><Plus v-else :size="16" /><span>{{ rootBusy ? "正在扫描" : "添加目录" }}</span>
            </button>
          </form>
          <div v-if="roots.length" class="root-list">
            <div v-for="root in roots" :key="root.id" class="root-row">
              <span class="root-folder-icon"><FolderOpen :size="15" /></span>
              <span class="root-path" :title="root.path">{{ root.path }}</span>
              <span class="root-count">{{ root.count.toLocaleString("zh-CN") }} 个文件</span>
              <button class="button button-ghost rescan-button" :disabled="scanningRootID === root.id" @click="rescanRoot(root)">
                <LoaderCircle v-if="scanningRootID === root.id" class="spin" :size="15" /><RefreshCw v-else :size="15" /><span>重新扫描</span>
              </button>
            </div>
          </div>
        </section>

        <section v-else class="roots-panel remote-roots-panel">
          <div class="panel-heading">
            <span class="panel-icon"><HardDrive :size="18" /></span>
            <span class="panel-heading-copy"><strong>家中索引目录</strong><small>目录保存在家中节点。远程访问时可以更新索引。</small></span>
            <span class="remote-root-count">{{ roots.length }} 个目录</span>
          </div>
          <div v-if="roots.length" class="root-list">
            <div v-for="root in roots" :key="root.id" class="root-row">
              <span class="root-folder-icon"><FolderOpen :size="15" /></span>
              <span class="root-path" :title="root.path">{{ root.path }}</span>
              <span class="root-count">{{ root.count.toLocaleString("zh-CN") }} 个文件</span>
              <button class="button button-ghost rescan-button" :disabled="scanningRootID === root.id || !nodeOnline" @click="rescanRoot(root)">
                <LoaderCircle v-if="scanningRootID === root.id" class="spin" :size="15" /><FolderSync v-else :size="15" /><span>重新扫描</span>
              </button>
            </div>
          </div>
          <div v-else-if="nodeOnline" class="remote-roots-empty">家中节点还没有索引目录。</div>
          <div v-else class="remote-roots-empty"><WifiOff :size="15" />家中节点离线时无法读取目录列表。</div>
        </section>

        <section class="collection-toolbar">
          <div class="collection-heading">
            <span class="collection-symbol"><LayoutGrid :size="17" /></span>
            <div><h2>{{ pageTitle }}</h2><p>{{ selectedTag ? "按标签筛选资源" : unorganizedOnly ? "还没有添加标签的资源" : "按最近修改时间排序" }}</p></div>
          </div>
          <div class="collection-actions">
            <label class="search-field" for="resource-search">
              <Search :size="17" />
              <input id="resource-search" v-model="query" type="search" placeholder="搜索文件名、路径或标签" autocomplete="off" />
              <kbd>⌘ K</kbd>
              <button v-if="query" class="search-clear" type="button" aria-label="清除搜索" @click="query = ''"><X :size="14" /></button>
            </label>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'grid' }" title="网格视图" @click="listMode = 'grid'"><LayoutGrid :size="16" /></button>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'list' }" title="列表视图" @click="listMode = 'list'"><List :size="16" /></button>
          </div>
        </section>

        <div class="results-toolbar">
          <div class="results-count"><span>{{ isFiltered ? "筛选结果" : "所有资源" }}</span><strong>{{ total.toLocaleString("zh-CN") }}</strong></div>
          <div class="results-toolbar-actions">
            <button class="quiet-button batch-suggestion-toggle" :aria-expanded="batchSuggestionsOpen" title="按目录和文件名特征分析整个资源库" @click="toggleBatchSuggestions"><Sparkles :size="14" />批量建议</button>
            <button v-if="isFiltered" class="quiet-button clear-filters" @click="chooseAll"><X :size="14" />清除筛选</button>
            <span v-else class="sort-label"><Clock3 :size="14" />最近修改</span>
          </div>
        </div>

        <section v-if="batchSuggestionsOpen" class="batch-suggestions-panel" aria-label="批量标签建议">
          <header class="batch-suggestions-header">
            <div><strong>批量整理建议</strong><p>{{ loadingBatchSuggestions ? `正在扫描全库 ${totalIndexed} 项资源` : `已扫描 ${batchSuggestionsScanned} 项资源` }}；建议根据文件夹和文件名特征生成。</p></div>
            <button class="quiet-button batch-suggestions-close" aria-label="收起批量建议" title="收起" @click="batchSuggestionsOpen = false"><X :size="16" /></button>
          </header>
          <div v-if="loadingBatchSuggestions" class="batch-suggestions-state"><LoaderCircle class="spin" :size="16" />正在分析 {{ batchSuggestionsScanned || totalIndexed }} 项资源…</div>
          <div v-else-if="batchSuggestions.length" class="batch-suggestion-list">
            <article v-for="suggestion in batchSuggestions" :key="suggestion.name" class="batch-suggestion-card">
              <div class="batch-suggestion-copy">
                <strong><Tag :size="13" />{{ suggestion.name }}</strong>
                <span>{{ suggestion.reason }} · {{ suggestion.count }} 项</span>
                <small :title="suggestion.examples.join('、')">{{ suggestion.examples.join("、") }}</small>
              </div>
              <button class="button button-secondary batch-suggestion-apply" :disabled="Boolean(applyingBatchSuggestion)" @click="applyBatchSuggestion(suggestion)">
                <LoaderCircle v-if="applyingBatchSuggestion === suggestion.name.toLocaleLowerCase()" class="spin" :size="14" />
                <Plus v-else :size="14" />
                <span>{{ applyingBatchSuggestion === suggestion.name.toLocaleLowerCase() ? "正在添加" : `应用到 ${suggestion.count} 项` }}</span>
              </button>
            </article>
          </div>
          <div v-else class="batch-suggestions-state">已扫描 {{ batchSuggestionsScanned }} 项资源，暂时没有足够集中的标签建议。</div>
        </section>

        <div v-if="selectedCount" class="bulk-toolbar">
          <div class="selection-summary"><span class="selection-check"><Check :size="14" /></span><strong>{{ selectedCount }}</strong><span>项已选</span></div>
          <span class="bulk-divider"></span>
          <input v-model="bulkTagName" maxlength="40" type="text" placeholder="输入要添加的标签" @keydown.enter.prevent="applyBatchTag" />
          <button class="button button-primary" :disabled="!bulkTagName.trim()" @click="applyBatchTag"><Plus :size="15" />批量添加标签</button>
          <button class="quiet-button cancel-selection" @click="selectedIDs = new Set()">取消选择</button>
        </div>

        <div v-if="errorMessage" class="notice notice-error" role="alert"><CircleAlert :size="17" /><span>{{ errorMessage }}</span><button class="notice-close" aria-label="关闭提示" @click="errorMessage = ''"><X :size="15" /></button></div>

        <div v-if="loading && !resources.length" class="resource-grid skeleton-grid" :class="{ 'list-mode': listMode === 'list' }" aria-label="正在加载资源">
          <div v-for="index in 8" :key="index" class="skeleton-card"><div class="skeleton-preview"></div><div class="skeleton-line"></div><div class="skeleton-line short"></div></div>
        </div>

        <section v-else-if="resources.length" class="resource-grid" :class="{ 'list-mode': listMode === 'list' }" aria-label="资源列表">
          <article v-for="resource in resources" :key="resource.id" class="resource-card" :class="{ unavailable: !resource.available, selected: selectedIDs.has(resource.id) }">
            <label class="resource-select" :aria-label="`选择${resource.name}`">
              <input type="checkbox" :checked="selectedIDs.has(resource.id)" @change="toggleSelection(resource.id)" />
              <span class="check-indicator"><Check :size="13" /></span>
            </label>
            <div class="resource-preview">
              <img v-if="resource.available && resource.kind === 'image'" loading="lazy" :src="fileURL(resource.id)" :alt="resource.name" />
              <video v-else-if="resource.available && resource.kind === 'video'" preload="metadata" muted :src="`${fileURL(resource.id)}#t=0.1`"></video>
              <div v-else class="preview-placeholder"><component :is="kindIcon(resource.kind)" :size="29" :stroke-width="1.5" /></div>
              <span class="type-pill">{{ kindLabel(resource.kind) }}</span>
              <span v-if="!resource.available" class="unavailable-pill">来源暂不可用</span>
            </div>
            <div class="resource-info">
              <p class="resource-name" :title="resource.path">{{ resource.name }}</p>
              <div class="resource-meta"><span>{{ formatBytes(resource.size) }}</span><span class="meta-dot"></span><span>{{ formatDate(resource.modified) }}</span></div>
              <div v-if="resource.available" class="resource-file-actions">
                <a class="resource-file-action" :href="fileURL(resource.id)" target="_blank" rel="noopener noreferrer"><ExternalLink :size="13" />打开</a>
                <a class="resource-file-action" :href="fileURL(resource.id, true)" :download="resource.name"><Download :size="13" />下载</a>
              </div>
              <p v-else class="resource-unavailable-note">源文件当前不可用</p>
              <div class="resource-tags">
                <button v-for="tagName in resource.tags" :key="tagName" class="tag-chip" @click="chooseTag(tagName)"><Tag :size="11" />{{ tagName }}</button>
                <span v-if="!resource.tags.length && editingTagID !== resource.id" class="no-tags">未添加标签</span>
                <div v-if="editingTagID === resource.id" class="tag-editor-wrap">
                  <div class="tag-editor">
                    <input v-model="editingTagName" type="text" maxlength="40" placeholder="输入标签后回车，可连续添加" :disabled="addingTagID === resource.id" @keydown.enter.prevent="addTagToResource(resource.id)" @keydown.esc="closeTagEditor" />
                    <button :disabled="addingTagID === resource.id || !editingTagName.trim()" aria-label="添加标签" title="添加标签" @click="addTagToResource(resource.id)"><LoaderCircle v-if="addingTagID === resource.id" class="spin" :size="13" /><Check v-else :size="14" /></button>
                    <button class="tag-editor-close" aria-label="收起标签编辑" title="收起" @click="closeTagEditor"><X :size="14" /></button>
                  </div>
                  <div v-if="loadingTagSuggestions" class="tag-suggestion-hint"><LoaderCircle class="spin" :size="11" />正在识别可用标签…</div>
                  <div v-else-if="tagSuggestions.length" class="tag-suggestions" aria-label="标签建议">
                    <button v-for="suggestion in tagSuggestions.slice(0, 5)" :key="suggestion.name" class="tag-suggestion" :disabled="addingTagID === resource.id" :title="suggestion.reason" @click="addTagToResource(resource.id, suggestion.name)"><Tag :size="10" /><span>{{ suggestion.name }}</span><small>{{ suggestion.reason }}</small></button>
                  </div>
                </div>
                <button v-else class="add-tag-button" :aria-label="`给${resource.name}添加标签`" title="添加标签" @click="openTagEditor(resource)"><Plus :size="15" /></button>
              </div>
            </div>
          </article>
        </section>

        <section v-else-if="initialized && !loading" class="empty-state">
          <span class="empty-icon"><FolderOpen :size="25" /></span>
          <h2>{{ isFiltered ? "没有找到匹配资源" : unorganizedOnly ? "整理完成" : "资源库还是空的" }}</h2>
          <p>{{ isFiltered ? "试试其他关键词，或清除筛选条件。" : unorganizedOnly ? "当前没有未添加标签的资源。" : "选择一个文件夹，Myliba 就会为其中的文件建立索引。" }}</p>
          <button v-if="!remoteMode && !isFiltered && !unorganizedOnly" class="button button-primary" @click="openFolderPicker"><FolderOpen :size="16" />选择第一个文件夹</button>
          <button v-else-if="isFiltered" class="button button-secondary" @click="chooseAll">清除筛选</button>
        </section>

        <div v-if="hasMore && !loading" class="load-more-wrap"><button class="button button-secondary" @click="pageOffset += pageSize; loadResources(true)">加载更多</button></div>
        <div v-if="loading && resources.length" class="loading-more"><LoaderCircle class="spin" :size="16" />正在加载…</div>

        <footer class="page-footer"><span>MYLIBA · 个人资源管理器</span><span><span class="footer-dot"></span>{{ remoteMode ? (nodeOnline ? "家中节点在线" : "等待节点连接") : "本机索引已就绪" }}</span></footer>
      </main>
    </div>

    <nav class="mobile-nav" aria-label="资源导航">
      <button :class="{ active: !selectedTag && !unorganizedOnly }" @click="chooseAll"><House :size="17" /><span>全部</span></button>
      <button :class="{ active: unorganizedOnly }" @click="chooseUnorganized"><Clock3 :size="17" /><span>待整理</span></button>
      <label class="mobile-tag-select"><Tag :size="16" /><select :value="selectedTag" aria-label="按标签筛选" @change="chooseTag(($event.target as HTMLSelectElement).value)"><option value="">标签</option><option v-for="tag in tags" :key="tag.id" :value="tag.name">{{ tag.name }} · {{ tag.count }}</option></select><ChevronDown :size="14" /></label>
    </nav>

    <div v-if="directoryPickerOpen" class="directory-picker-backdrop" @click.self="directoryPickerOpen = false">
      <section class="directory-picker" role="dialog" aria-modal="true" aria-labelledby="directory-picker-title">
        <header class="directory-picker-header">
          <span class="panel-icon"><FolderOpen :size="18" /></span>
          <div><h2 id="directory-picker-title">选择索引目录</h2><p>浏览本机文件夹，原文件不会移动或复制。</p></div>
          <button class="quiet-button directory-close" aria-label="关闭" @click="directoryPickerOpen = false"><X :size="18" /></button>
        </header>
        <div class="directory-picker-toolbar">
          <button class="button button-secondary directory-up" :disabled="pickerBusy || !directoryListing?.parent" @click="browseDirectories(directoryListing?.parent || '')"><ChevronDown class="directory-up-icon" :size="15" />上一级</button>
          <span class="directory-current" :title="directoryListing?.current || '此电脑'">{{ directoryListing?.current || "选择磁盘" }}</span>
          <button class="button button-primary" :disabled="pickerBusy || !directoryListing?.current" @click="selectCurrentDirectory"><Check :size="15" />选择此目录</button>
        </div>
        <div class="directory-list" aria-live="polite">
          <div v-if="pickerBusy" class="directory-empty"><LoaderCircle class="spin" :size="18" />正在读取目录…</div>
          <div v-else-if="directoryError" class="directory-empty directory-error"><CircleAlert :size="17" />{{ directoryError }}</div>
          <template v-else-if="directoryListing">
            <button v-for="root in directoryListing.roots" :key="root.path" class="directory-item" @click="browseDirectories(root.path)"><HardDrive :size="17" /><span>{{ root.name }}</span><ChevronDown class="directory-next" :size="15" /></button>
            <button v-for="directory in directoryListing.directories" :key="directory.path" class="directory-item" @click="browseDirectories(directory.path)"><FolderOpen :size="17" /><span>{{ directory.name }}</span><ChevronDown class="directory-next" :size="15" /></button>
            <div v-if="!directoryListing.roots.length && !directoryListing.directories.length" class="directory-empty">此目录下没有子文件夹</div>
          </template>
        </div>
        <footer class="directory-picker-footer">{{ directoryListing?.current ? "选择当前目录后，Myliba 会开始扫描其中的文件。" : "先选择一个磁盘，再逐层打开目标文件夹。" }}</footer>
      </section>
    </div>

    <div v-if="remoteMode && !authenticated" class="login-screen">
      <form class="login-card" @submit.prevent="submitLogin">
        <span class="login-brand"><LibraryBig :size="21" /></span>
        <p class="login-eyebrow">PERSONAL LIBRARY</p>
        <h1>登录你的资源空间</h1>
        <p class="login-copy">使用你在 Myliba 公网节点设置的访问密码。</p>
        <label for="login-password">访问密码</label>
        <input id="login-password" v-model="password" type="password" autocomplete="current-password" required />
        <p class="login-error" role="alert">{{ loginError }}</p>
        <button class="button button-primary login-submit" type="submit" :disabled="loginBusy || !password">
          <LoaderCircle v-if="loginBusy" class="spin" :size="16" /><span>{{ loginBusy ? "正在登录" : "登录" }}</span>
        </button>
      </form>
    </div>

    <Transition name="toast"><div v-if="toastVisible" class="toast-message" role="status"><Check :size="16" />{{ toastMessage }}</div></Transition>
  </div>
</template>
