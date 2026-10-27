<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { closeWebRTC, connectWebRTC } from "./webrtc";
import { singleFilters, toggleFilter } from "./filterSelection";
import ResourceDetail from "./ResourceDetail.vue";
import ImagePreview from "./ImagePreview.vue";
import StreamingVideo from "./StreamingVideo.vue";
import HubSettings from "./HubSettings.vue";
import CloudPanel from "./CloudPanel.vue";
import HubCloudDashboard from "./HubCloudDashboard.vue";
import DeviceManager from "./DeviceManager.vue";
import {
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Clock3,
  Bookmark,
  BookmarkPlus,
  Pencil,
  Trash2,
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
  Play,
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
  addMediaStore,
  createCollection,
  deleteCollection,
  ApiError,
  fileURL,
  getResourceThumbnail,
  getResourceFile,
  getPreprocessQueue,
  prioritizePreprocessJobs,
  retryPreprocessJob,
  retryFailedPreprocessJobs,
  type PreprocessQueue,
  getBatchTagSuggestions,
  getResources,
  getResourceSelection,
  getCollections,
  getFolders,
  createFolder,
  renameFolder,
  deleteFolder,
  moveResourcesToFolder,
  getRoots,
  getMediaStores,
  getDirectories,
  getSession,
  getTagSuggestions,
  getTags,
  getAttributeTags,
  login,
  logout,
  scanRoot,
  selectResourceNode,
  removeResourceNode,
  selectMediaStore,
  updateCollection,
  type DirectoryListing,
  type BatchTagSuggestion,
  type CollectionFilters,
  type SmartCollection,
  type ResourceSort,
  type Resource,
  type RootSummary,
  type MediaStore,
  type TagSummary,
  type AttributeTag,
  type TagSuggestion,
  type VirtualFolder,
  type TrafficRate,
  type HubResourceNode,
} from "./api";

const remoteMode = ref(false);
const authenticated = ref(true);
const ownerSession = ref(true);
const nodeOnline = ref(false);
const resourceNodes = ref<HubResourceNode[]>([]);
const selectedResourceNodeID = ref("");
const selectedResourceNodeKind = computed(() => resourceNodes.value.find((node) => node.id === selectedResourceNodeID.value)?.kind);
const nodeTransport = ref<"connecting" | "webrtc" | "turn" | "relay">("relay");
const webTraffic = ref<TrafficRate>({ downloadBps: 0, uploadBps: 0 });
const nodeTraffic = ref<TrafficRate>({ downloadBps: 0, uploadBps: 0 });
const loading = ref(false);
const initialized = ref(false);
const errorMessage = ref("");
const resources = ref<Resource[]>([]);
const failedThumbnailIDs = ref(new Set<string>());
const preprocessOpen = ref(false);
const retryingFailedJobs = ref(false);
const preprocessQueue = ref<PreprocessQueue>({ counts: { pending: 0, running: 0, done: 0, failed: 0 }, jobs: [], visible: {} });
const thumbnailURLs = ref(new Map<string, string>());
const thumbnailVersionSignatures = ref(new Map<string, string>());
const loadingThumbnailIDs = new Set<string>();
const generatingThumbnailIDs = ref(new Set<string>());
const generatingThumbnailBatch = ref(false);
const thumbnailRefreshTokens = ref(new Map<string, number>());
const thumbnailBatchProgress = ref({ done: 0, total: 0 });
const roots = ref<RootSummary[]>([]);
const mediaStores = ref<MediaStore[]>([]);
const mediaStorePath = ref("");
const mediaStoreBusy = ref(false);
const tags = ref<TagSummary[]>([]);
const attributeCatalog = ref<AttributeTag[]>([]);
const attributeCatalogLoading = ref(false);
const selectedAttributes = ref<string[]>([]);
const attributeFormatQuery = ref("");
const folders = ref<VirtualFolder[]>([]);
const unfiledCount = ref(0);
const activeFolderID = ref<string | null>(new URLSearchParams(window.location.search).get("folder"));
const newFolderName = ref("");
const folderBusy = ref(false);
const bulkFolderID = ref("");
const folderEditorOpen = ref(false);
const sidebarFolderCreateOpen = ref(false);
const editingFolderName = ref("");
const total = ref(0);
const query = ref("");
const selectedTags = ref<string[]>([]);
const multipleFilters = ref(false);
const selectedKind = ref("");
const selectedRootID = ref("");
const sortOrder = ref<ResourceSort>("modified_desc");
const collections = ref<SmartCollection[]>([]);
const selectedCollectionID = ref("");
const collectionEditorOpen = ref(false);
const editingCollectionID = ref("");
const collectionName = ref("");
const collectionBusy = ref(false);
const tagFilterOpen = ref(false);
const tagFilterQuery = ref("");
const unorganizedOnly = ref(false);
const pageOffset = ref(0);
const pageSize = 48;
const selectedIDs = ref(new Set<string>());
const selectedResources = ref(new Map<string, Pick<Resource, "id" | "kind" | "available">>());
const selectingAll = ref(false);
const selectionProgress = ref({ loaded: 0, total: 0 });
const bulkBusy = ref<"" | "tag" | "folder">("");
const bulkProgress = ref({ done: 0, total: 0 });
const bulkTagName = ref("");
const batchSuggestions = ref<BatchTagSuggestion[]>([]);
const batchSuggestionsOpen = ref(false);
const loadingBatchSuggestions = ref(false);
const batchSuggestionsScanned = ref(0);
const applyingBatchSuggestion = ref("");
const rootPath = ref("");
const rootAutoPreprocess = ref(true);
const rootBusy = ref(false);
const rootSettingBusyID = ref("");
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
const detailResourceID = ref(new URLSearchParams(window.location.search).get("resource") || "");
const previewResource = ref<Resource | null>(null);
const previewURL = ref("");
const previewMimeType = ref("");
const previewError = ref("");
const previewLoadingNext = ref(false);

let debounceTimer: ReturnType<typeof setTimeout> | undefined;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let statusTimer: ReturnType<typeof setInterval> | undefined;
let jobsTimer: ReturnType<typeof setInterval> | undefined;
let requestGeneration = 0;
let attributeCatalogGeneration = 0;
let attributeCatalogScope = "";
let selectionGeneration = 0;
let selectionScopeKey = "";
let lastWebRTCAttempt = 0;
let retryingWebRTC = false;
let authGeneration = 0;
let suppressQueryWatch = false;

const onShortcut = (event: KeyboardEvent) => {
  if (event.key === "Escape" && preprocessOpen.value) { preprocessOpen.value = false; return; }
  if (event.key === "Escape" && previewResource.value) {
    closeResourcePreview();
    return;
  }
  if (previewResource.value && !event.altKey && !event.ctrlKey && !event.metaKey &&
    (event.key === "ArrowLeft" || event.key === "ArrowRight") &&
    !(event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement ||
      event.target instanceof HTMLVideoElement || event.target instanceof HTMLAudioElement)) {
    event.preventDefault();
    void moveResourcePreview(event.key === "ArrowLeft" ? -1 : 1);
    return;
  }
  if (event.key === "Escape" && sidebarFolderCreateOpen.value) { sidebarFolderCreateOpen.value = false; return; }
  if (event.key === "Escape" && tagFilterOpen.value) {
    tagFilterOpen.value = false;
    return;
  }
  if (event.key === "Escape" && collectionEditorOpen.value) {
    closeCollectionEditor();
    return;
  }
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    tagFilterOpen.value = false;
    if (activeFolderID.value === null) openFolder("all");
    void nextTick(() => document.querySelector<HTMLInputElement>("#resource-search")?.focus());
  }
};

const onTagEditorOutside = (event: MouseEvent) => {
  if (!editingTagID.value) return;
  const target = event.target;
  if (target instanceof Element && target.closest(".tag-editor-wrap")) return;
  closeTagEditor();
};

function currentFilters(): CollectionFilters {
  return {
    query: query.value.trim(), tags: [...selectedTags.value], attributes: [...selectedAttributes.value], kind: selectedKind.value,
    rootId: selectedRootID.value, folderId: activeFolderID.value || "all", unorganized: unorganizedOnly.value, sort: sortOrder.value,
  };
}

function effectiveAttributes(filters: CollectionFilters): string[] {
  const attributes = [...(filters.attributes || [])];
  if (filters.kind && !attributes.includes("type:" + filters.kind)) attributes.push("type:" + filters.kind);
  return attributes;
}

function sameFilters(left: CollectionFilters, right: CollectionFilters) {
  const leftAttributes = effectiveAttributes(left);
  const rightAttributes = effectiveAttributes(right);
  return left.query === right.query && left.rootId === right.rootId &&
    left.unorganized === right.unorganized && left.sort === right.sort && (left.folderId || "all") === (right.folderId || "all") &&
    left.tags.length === right.tags.length && left.tags.every((tag) => right.tags.includes(tag)) &&
    leftAttributes.length === rightAttributes.length && leftAttributes.every((key) => rightAttributes.includes(key));
}

const selectedCollection = computed(() => collections.value.find((item) => item.id === selectedCollectionID.value));
const activeAttributeKeys = computed(() => effectiveAttributes(currentFilters()));
const attributeTypes = computed(() => attributeCatalog.value.filter((tag) => tag.category === "type"));
const attributeSizes = computed(() => attributeCatalog.value.filter((tag) => tag.category === "size"));
const attributeFormats = computed(() => attributeCatalog.value.filter((tag) => tag.category === "extension" && tag.label.toLocaleLowerCase().includes(attributeFormatQuery.value.trim().toLocaleLowerCase())));
const visibleCollections = computed(() => collections.value.filter((item) => (item.filters.folderId || "all") === (activeFolderID.value || "all")));
const folderTitle = computed(() => activeFolderID.value === "all" ? "全部资源" : activeFolderID.value === "unfiled" ? "未归组" : folders.value.find((item) => item.id === activeFolderID.value)?.name || "文件夹");
const activeCollection = computed(() => selectedCollection.value && sameFilters(selectedCollection.value.filters, currentFilters()) ? selectedCollection.value : undefined);
const pageTitle = computed(() => {
  if (activeCollection.value) return activeCollection.value.name;
  if (selectedTags.value.length > 1) return `标签筛选（${selectedTags.value.length}）`;
  if (selectedTags.value.length === 1) return `标签：${selectedTags.value[0]}`;
  if (unorganizedOnly.value) return "待整理";
  return query.value.trim() || activeAttributeKeys.value.length || selectedRootID.value ? "筛选结果" : activeFolderID.value === null ? "文件夹" : folderTitle.value;
});
const selectedCount = computed(() => selectedIDs.value.size);
const selectedMediaCount = computed(() => [...selectedResources.value.values()].filter((resource) => resource.available && ["image", "video"].includes(resource.kind)).length);
const totalIndexed = computed(() => roots.value.reduce((sum, root) => sum + root.count, 0));
const hasMore = computed(() => resources.value.length < total.value);
const previewIndex = computed(() => previewResource.value ? resources.value.findIndex((item) => item.id === previewResource.value?.id) : -1);
const previewPosition = computed(() => previewIndex.value < 0 ? 1 : previewIndex.value + 1);
const previewTotal = computed(() => Math.max(total.value, resources.value.length));
const canPreviewPrevious = computed(() => previewIndex.value > 0);
const canPreviewNext = computed(() => previewIndex.value >= 0 && (previewIndex.value < resources.value.length - 1 || hasMore.value));
const isFiltered = computed(() => Boolean(query.value.trim() || selectedTags.value.length || unorganizedOnly.value || activeAttributeKeys.value.length || selectedRootID.value || sortOrder.value !== "modified_desc"));
const matchingTags = computed(() => {
  const needle = tagFilterQuery.value.trim().toLocaleLowerCase();
  return tags.value.filter((tag) => tag.count > 0 && tag.name.toLocaleLowerCase().includes(needle));
});

function formatRate(value: number): string {
  if (value < 1024) return `${value} B/s`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB/s`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB/s`;
}

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

function markThumbnailFailed(id: string) {
  failedThumbnailIDs.value = new Set(failedThumbnailIDs.value).add(id);
}

async function loadThumbnail(resource: Resource) {
  if (!resource.available || !["image", "video"].includes(resource.kind) || thumbnailURLs.value.has(resource.id) || loadingThumbnailIDs.has(resource.id)) return;
  loadingThumbnailIDs.add(resource.id);
  const requestedRevision = thumbnailRefreshTokens.value.get(resource.id) || 0;
  const requestedSignature = `${resource.size}-${resource.modified}`;
  let retryWithCurrentRevision = false;
  try {
    const response = await getResourceThumbnail(resource, requestedRevision);
    if (!response.ok) throw new Error("thumbnail unavailable");
    const url = URL.createObjectURL(await response.blob());
    const currentResource = resources.value.find((item) => item.id === resource.id);
    if ((thumbnailRefreshTokens.value.get(resource.id) || 0) !== requestedRevision || (currentResource && `${currentResource.size}-${currentResource.modified}` !== requestedSignature)) {
      URL.revokeObjectURL(url);
      retryWithCurrentRevision = true;
      return;
    }
    const urls = new Map(thumbnailURLs.value);
    const previous = urls.get(resource.id);
    if (previous) URL.revokeObjectURL(previous);
    urls.set(resource.id, url);
    thumbnailURLs.value = urls;
    const failed = new Set(failedThumbnailIDs.value);
    failed.delete(resource.id);
    failedThumbnailIDs.value = failed;
  } catch {
    markThumbnailFailed(resource.id);
  } finally {
    loadingThumbnailIDs.delete(resource.id);
    if (retryWithCurrentRevision) void loadThumbnail(resource);
  }
}

function isGeneratingThumbnail(id: string) {
  return generatingThumbnailIDs.value.has(id);
}

async function generateThumbnail(id: string, showFailure = true, refresh = true): Promise<boolean> {
  if (generatingThumbnailIDs.value.has(id)) return false;
  generatingThumbnailIDs.value = new Set(generatingThumbnailIDs.value).add(id);
  try {
    await retryPreprocessJob(id);
    if (refresh) await refreshPreprocessJobs();
    return true;
  } catch (error) {
    if (showFailure) notify(error instanceof Error ? error.message : "无法安排缩略图任务");
    return false;
  } finally {
    const next = new Set(generatingThumbnailIDs.value);
    next.delete(id);
    generatingThumbnailIDs.value = next;
  }
}

async function retryAllFailedJobs() {
  if (retryingFailedJobs.value || !preprocessQueue.value.counts.failed) return;
  retryingFailedJobs.value = true;
  try {
    const result = await retryFailedPreprocessJobs();
    await refreshPreprocessJobs();
    notify(result.retried ? `已将 ${result.retried} 个失败任务重新加入队列` : "没有可重试的失败任务");
  } catch (error) {
    notify(error instanceof Error ? error.message : "无法批量重试任务");
  } finally {
    retryingFailedJobs.value = false;
  }
}

async function generateSelectedThumbnails() {
  if (generatingThumbnailBatch.value) return;
  const selected = [...selectedResources.value.values()].filter((resource) => resource.available && ["image", "video"].includes(resource.kind));
  if (!selected.length) return;
  generatingThumbnailBatch.value = true;
  thumbnailBatchProgress.value = { done: 0, total: selected.length };
  let nextIndex = 0;
  let succeeded = 0;
  let failed = 0;
  const worker = async () => {
    while (nextIndex < selected.length) {
      const resource = selected[nextIndex++];
      if (await generateThumbnail(resource.id, false, false)) succeeded++;
      else failed++;
      thumbnailBatchProgress.value = { ...thumbnailBatchProgress.value, done: thumbnailBatchProgress.value.done + 1 };
    }
  };
  try {
    await Promise.all([worker(), worker()]);
    await refreshPreprocessJobs();
    notify(failed ? `已排队 ${succeeded} 个，${failed} 个失败` : `已将 ${succeeded} 个任务加入队列`);
  } finally {
    generatingThumbnailBatch.value = false;
  }
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

function canPreviewInline(contentType: string) {
  const type = contentType.split(";")[0].trim().toLowerCase();
  return type === "application/pdf" || type === "text/plain" ||
    (type.startsWith("image/") && type !== "image/svg+xml") ||
    type.startsWith("video/") || type.startsWith("audio/");
}

function getPreviewMimeType(resource: Resource) {
  const extension = resource.name.split(".").pop()?.toLowerCase() || "";
  const types: Record<string, string> = {
    avif: "image/avif", bmp: "image/bmp", gif: "image/gif", heic: "image/heic", heif: "image/heif",
    jpeg: "image/jpeg", jpg: "image/jpeg", png: "image/png", tif: "image/tiff", tiff: "image/tiff", webp: "image/webp",
    mp4: "video/mp4", m4v: "video/mp4", mov: "video/quicktime", mkv: "video/x-matroska", avi: "video/x-msvideo", webm: "video/webm", ogv: "video/ogg",
    mp3: "audio/mpeg", m4a: "audio/mp4", aac: "audio/aac", oga: "audio/ogg", ogg: "audio/ogg", wav: "audio/wav", flac: "audio/flac",
    pdf: "application/pdf", txt: "text/plain",
  };
  const type = types[extension] || "";
  return canPreviewInline(type) ? type : "";
}

function openResourcePreview(resource: Resource) {
  if (previewURL.value) URL.revokeObjectURL(previewURL.value);
  previewURL.value = "";
  previewResource.value = resource;
  previewMimeType.value = getPreviewMimeType(resource);
  previewError.value = resource.available ? "" : "源文件当前不可用。";
  if (remoteMode.value && resource.available && resource.kind !== "video" && resource.kind !== "image") {
    if (resource.size > 128 * 1024 * 1024) {
      previewError.value = "此文件超过直连网页预览的大小限制，请下载后打开。";
    } else {
      const id = resource.id;
      void getResourceFile(id).then(async (response) => {
        if (!response.ok) throw new Error("文件读取失败");
        const url = URL.createObjectURL(await response.blob());
        if (previewResource.value?.id !== id) URL.revokeObjectURL(url);
        else { previewURL.value = url; previewError.value = ""; }
      }).catch((cause) => {
        if (previewResource.value?.id === id) previewError.value = cause instanceof Error ? cause.message : "文件暂时不可用";
      });
    }
  }
}

async function moveResourcePreview(direction: -1 | 1) {
  const index = previewIndex.value;
  if (index < 0 || previewLoadingNext.value) return;
  if (direction < 0) {
    if (index > 0) openResourcePreview(resources.value[index - 1]);
    return;
  }
  if (index + 1 < resources.value.length) {
    openResourcePreview(resources.value[index + 1]);
    return;
  }
  if (!hasMore.value) return;
  previewLoadingNext.value = true;
  const currentID = previewResource.value?.id;
  const previousLength = resources.value.length;
  pageOffset.value = previousLength;
  try {
    await loadResources(true);
    if (previewResource.value?.id === currentID && resources.value.length > previousLength) {
      openResourcePreview(resources.value[previousLength]);
    }
  } finally {
    previewLoadingNext.value = false;
  }
}

function closeResourcePreview() {
  if (previewURL.value) URL.revokeObjectURL(previewURL.value);
  previewURL.value = "";
  previewResource.value = null;
  previewMimeType.value = "";
  previewError.value = "";
}

async function startRelayDownload(resource: Resource) {
  if (remoteMode.value) {
    try {
      const response = await getResourceFile(resource.id, true);
      if (!response.ok) throw new Error(`下载失败 (HTTP ${response.status})`);
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = resource.name;
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch (cause) { showError(cause); }
    return;
  }
  const link = document.createElement("a");
  link.href = fileURL(resource.id, true);
  link.download = resource.name;
  document.body.append(link);
  link.click();
  link.remove();
}

async function loadResources(append = false) {
  const generation = ++requestGeneration;
  const scopeKey = JSON.stringify(currentFilters());
  if (scopeKey !== selectionScopeKey || activeFolderID.value === null) {
    clearSelection();
    selectionScopeKey = scopeKey;
  }
  if (!append) void loadAttributeCatalog();
  if (activeFolderID.value === null) {
    resources.value = [];
    total.value = 0;
    loading.value = false;
    return;
  }
  if (!append) {
    pageOffset.value = 0;
    loading.value = true;
  }
  errorMessage.value = "";
  try {
    const result = await getResources({
      query: query.value.trim(),
      tags: selectedTags.value,
      attributes: selectedAttributes.value,
      unorganized: unorganizedOnly.value,
      kind: selectedKind.value,
      rootId: selectedRootID.value,
      folderId: activeFolderID.value,
      sort: sortOrder.value,
      limit: pageSize,
      offset: append ? pageOffset.value : 0,
    });
    if (generation !== requestGeneration) return;
    resources.value = append ? [...resources.value, ...result.items] : result.items;
    total.value = result.total;
  } catch (error) {
    if (generation === requestGeneration) showError(error);
  } finally {
    if (generation === requestGeneration) loading.value = false;
  }
}

async function loadRoots() {
  const generation = authGeneration;
  try {
    const result = await getRoots();
    if (generation === authGeneration) roots.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadMediaStores() {
  if (remoteMode.value) return;
  const generation = authGeneration;
  try {
    const result = await getMediaStores();
    if (generation === authGeneration) mediaStores.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function submitMediaStore() {
  const path = mediaStorePath.value.trim();
  if (!path || mediaStoreBusy.value) return;
  mediaStoreBusy.value = true;
  try {
    await addMediaStore(path);
    mediaStorePath.value = "";
    await loadMediaStores();
  } catch (error) { showError(error); }
  finally { mediaStoreBusy.value = false; }
}

async function makeDefaultMediaStore(id: string) {
  if (mediaStoreBusy.value) return;
  mediaStoreBusy.value = true;
  try {
    await selectMediaStore(id);
    await loadMediaStores();
  } catch (error) { showError(error); }
  finally { mediaStoreBusy.value = false; }
}

async function loadTags() {
  const generation = authGeneration;
  const folderID = activeFolderID.value;
  if (folderID === null) { tags.value = []; return; }
  try {
    const result = await getTags(folderID);
    if (generation === authGeneration && folderID === activeFolderID.value) tags.value = result;
  } catch (error) { if (generation === authGeneration && folderID === activeFolderID.value) showError(error); }
}

async function loadFolders() {
  const generation = authGeneration;
  try {
    const result = await getFolders();
    if (generation === authGeneration) {
      folders.value = result.items;
      unfiledCount.value = result.unfiledCount;
      if (activeFolderID.value && !["all", "unfiled"].includes(activeFolderID.value) && !result.items.some((item) => item.id === activeFolderID.value)) {
        openFolder(null);
      }
    }
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadAttributeCatalog() {
  const generation = ++attributeCatalogGeneration;
  const sessionGeneration = authGeneration;
  const filters = currentFilters();
  const scope = JSON.stringify({ ...filters, sort: "" });
  if (scope !== attributeCatalogScope) {
    attributeCatalog.value = [];
    attributeCatalogScope = scope;
  }
  if (activeFolderID.value === null) {
    attributeCatalogLoading.value = false;
    return;
  }
  attributeCatalogLoading.value = true;
  try {
    const result = await getAttributeTags(filters);
    if (generation === attributeCatalogGeneration && sessionGeneration === authGeneration && scope === JSON.stringify({ ...currentFilters(), sort: "" })) attributeCatalog.value = result;
  } catch (error) {
    if (generation === attributeCatalogGeneration && sessionGeneration === authGeneration) {
      attributeCatalog.value = [];
      showError(error);
    }
  } finally {
    if (generation === attributeCatalogGeneration) attributeCatalogLoading.value = false;
  }
}

async function loadCollections() {
  const generation = authGeneration;
  try {
    const result = await getCollections();
    if (generation === authGeneration) collections.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadWorkspace() {
  await Promise.all([loadRoots(), loadMediaStores(), loadFolders(), loadTags(), loadCollections(), loadResources()]);
}

function setDetailRoute(id: string) {
  detailResourceID.value = id;
  const url = new URL(window.location.href);
  if (id) url.searchParams.set("resource", id);
  else url.searchParams.delete("resource");
  window.history.pushState({}, "", url.pathname + url.search + url.hash);
}

function openDetail(resource: Resource) {
  setDetailRoute(resource.id);
  window.scrollTo(0, 0);
}

function closeDetail() {
  setDetailRoute("");
}

function syncDetailRoute() {
  const params = new URLSearchParams(window.location.search);
  detailResourceID.value = params.get("resource") || "";
  const nextFolder = params.get("folder");
  if (nextFolder !== activeFolderID.value) {
    activeFolderID.value = nextFolder;
    resetFolderFilters();
    void Promise.all([loadTags(), loadResources()]);
  }
}

function resetFolderFilters() {
  if (debounceTimer) clearTimeout(debounceTimer);
  suppressQueryWatch = true;
  selectedCollectionID.value = "";
  selectedTags.value = [];
  selectedAttributes.value = [];
  selectedKind.value = "";
  selectedRootID.value = "";
  sortOrder.value = "modified_desc";
  unorganizedOnly.value = false;
  query.value = "";
  void nextTick(() => { suppressQueryWatch = false; });
  tagFilterOpen.value = false;
  batchSuggestionsOpen.value = false;
  batchSuggestions.value = [];
  attributeFormatQuery.value = "";
  folderEditorOpen.value = false;
  bulkFolderID.value = "";
  clearSelection();
}

function openFolder(id: string | null) {
  if (detailResourceID.value) detailResourceID.value = "";
  activeFolderID.value = id;
  resetFolderFilters();
  const url = new URL(window.location.href);
  url.searchParams.delete("resource");
  if (id === null) url.searchParams.delete("folder");
  else url.searchParams.set("folder", id);
  window.history.pushState({}, "", url.pathname + url.search + url.hash);
  void Promise.all([loadTags(), loadResources()]);
  window.scrollTo(0, 0);
}

async function toggleSidebarFolderCreate() {
  sidebarFolderCreateOpen.value = !sidebarFolderCreateOpen.value;
  if (!sidebarFolderCreateOpen.value) return;
  await nextTick();
  document.querySelector<HTMLInputElement>("#sidebar-new-folder-name")?.focus();
}

async function submitFolder() {
  const name = newFolderName.value.trim();
  if (!name || folderBusy.value) return;
  folderBusy.value = true;
  try {
    const item = await createFolder(name);
    newFolderName.value = "";
    sidebarFolderCreateOpen.value = false;
    await loadFolders();
    openFolder(item.id);
    notify("文件夹已创建");
  } catch (error) { showError(error); }
  finally { folderBusy.value = false; }
}

async function saveFolderName() {
  const id = activeFolderID.value;
  const name = editingFolderName.value.trim();
  if (!id || ["all", "unfiled"].includes(id) || !name || folderBusy.value) return;
  folderBusy.value = true;
  try {
    await renameFolder(id, name);
    await loadFolders();
    folderEditorOpen.value = false;
    notify("文件夹已重命名");
  } catch (error) { showError(error); }
  finally { folderBusy.value = false; }
}

async function removeFolder() {
  const id = activeFolderID.value;
  if (!id || ["all", "unfiled"].includes(id) || folderBusy.value) return;
  if (!window.confirm("删除文件夹？其中的文件会移至未归组，原文件和标签不会删除。")) return;
  folderBusy.value = true;
  try {
    await deleteFolder(id);
    folderEditorOpen.value = false;
    openFolder(null);
    await Promise.all([loadFolders(), loadCollections()]);
    notify("文件夹已删除，文件已移至未归组");
  } catch (error) { showError(error); }
  finally { folderBusy.value = false; }
}

async function moveSelectedToFolder() {
  if (!selectedIDs.value.size || bulkBusy.value || selectingAll.value) return;
  const ids = [...selectedIDs.value];
  bulkBusy.value = "folder";
  bulkProgress.value = { done: 0, total: ids.length };
  try {
    for (let offset = 0; offset < ids.length; offset += 1000) {
      const batch = ids.slice(offset, offset + 1000);
      await moveResourcesToFolder(batch, bulkFolderID.value);
      removeSelected(batch);
      bulkProgress.value = { done: offset + batch.length, total: ids.length };
    }
    await Promise.all([loadFolders(), loadTags(), loadResources()]);
    notify("已移动 " + ids.length + " 项资源");
  } catch (error) {
    await Promise.all([loadFolders(), loadTags(), loadResources()]);
    showError(error);
    if (bulkProgress.value.done && errorMessage.value) errorMessage.value += `；已移动 ${bulkProgress.value.done} 项，剩余 ${selectedCount.value} 项仍已选`;
  } finally { bulkBusy.value = ""; }
}

async function onDetailFolderChanged() {
  closeDetail();
  await Promise.all([loadFolders(), loadTags(), loadResources()]);
  notify("文件夹归属已更新");
}

function chooseAll() {
  if (detailResourceID.value) closeDetail();
  selectedCollectionID.value = "";
  selectedTags.value = [];
  selectedAttributes.value = [];
  selectedKind.value = "";
  selectedRootID.value = "";
  sortOrder.value = "modified_desc";
  unorganizedOnly.value = false;
  query.value = "";
  void loadResources();
}

function chooseUnorganized() {
  if (detailResourceID.value) closeDetail();
  selectedCollectionID.value = "";
  selectedTags.value = [];
  selectedAttributes.value = [];
  selectedKind.value = "";
  selectedRootID.value = "";
  sortOrder.value = "modified_desc";
  query.value = "";
  unorganizedOnly.value = true;
  void loadResources();
}

function toggleTag(name: string) {
  if (detailResourceID.value) closeDetail();
  selectedTags.value = toggleFilter(selectedTags.value, name, multipleFilters.value);
  unorganizedOnly.value = false;
  void loadResources();
}

function chooseSidebarTag(name: string) {
  toggleTag(name);
}

function changeFilterMode() {
  if (!multipleFilters.value) {
    selectedTags.value = singleFilters(selectedTags.value);
    selectedAttributes.value = singleFilters(activeAttributeKeys.value, true);
    selectedKind.value = "";
    void loadResources();
  }
}

function clearSelectedTags() {
  if (!selectedTags.value.length) return;
  selectedTags.value = [];
  void loadResources();
}

function attributeLabel(key: string): string {
  const found = attributeCatalog.value.find((item) => item.key === key);
  if (found) return found.label;
  const [category, value] = key.split(":");
  if (category === "type") return { image: "图片", video: "视频", audio: "音频", document: "文档", file: "其他文件" }[value] || value;
  if (category === "size") return { lt1m: "小于 1 MB", "1to10m": "1–10 MB", "10to100m": "10–100 MB", gte100m: "100 MB 以上" }[value] || value;
  return category === "extension" ? value.toUpperCase() : value;
}

function toggleAttribute(key: string) {
  if (detailResourceID.value) closeDetail();
  const selected = toggleFilter(activeAttributeKeys.value, key, multipleFilters.value, key.split(":", 1)[0] + ":");
  selectedKind.value = "";
  selectedAttributes.value = [...selected];
  void loadResources();
}

function clearAttributes() {
  if (!activeAttributeKeys.value.length) return;
  selectedKind.value = "";
  selectedAttributes.value = [];
  void loadResources();
}

async function toggleTagFilter() {
  tagFilterOpen.value = !tagFilterOpen.value;
  if (!tagFilterOpen.value) return;
  tagFilterQuery.value = "";
  await nextTick();
  document.querySelector<HTMLInputElement>("#tag-filter-search")?.focus();
}

async function openTagFilterFromMobile() {
  if (detailResourceID.value) closeDetail();
  tagFilterOpen.value = true;
  await nextTick();
  document.querySelector(".tag-filter-panel")?.scrollIntoView({ behavior: "smooth", block: "center" });
  document.querySelector<HTMLInputElement>("#tag-filter-search")?.focus({ preventScroll: true });
}

async function chooseCollection(item: SmartCollection) {
  if (detailResourceID.value) closeDetail();
  closeCollectionEditor();
  if (debounceTimer) clearTimeout(debounceTimer);
  suppressQueryWatch = true;
  selectedCollectionID.value = item.id;
  query.value = item.filters.query;
  selectedTags.value = [...item.filters.tags];
  selectedKind.value = item.filters.kind;
  selectedAttributes.value = [...(item.filters.attributes || [])];
  multipleFilters.value = selectedTags.value.length > 1 || singleFilters(activeAttributeKeys.value, true).length < activeAttributeKeys.value.length;
  selectedRootID.value = item.filters.rootId;
  unorganizedOnly.value = item.filters.unorganized;
  sortOrder.value = item.filters.sort;
  activeFolderID.value = item.filters.folderId || "all";
  const url = new URL(window.location.href);
  url.searchParams.set("folder", activeFolderID.value);
  window.history.pushState({}, "", url.pathname + url.search + url.hash);
  await nextTick();
  void loadTags();
  suppressQueryWatch = false;
  void loadResources();
}

function openCollectionEditor(item?: SmartCollection, restoreFilters = false) {
  if (detailResourceID.value) closeDetail();
  if (item) {
    if (restoreFilters) void chooseCollection(item);
    editingCollectionID.value = item.id;
    collectionName.value = item.name;
  } else {
    editingCollectionID.value = "";
    collectionName.value = "";
  }
  collectionEditorOpen.value = true;
  void nextTick(() => document.querySelector<HTMLInputElement>("#collection-name")?.focus());
}

function closeCollectionEditor() {
  collectionEditorOpen.value = false;
  editingCollectionID.value = "";
  collectionName.value = "";
}

async function saveCollection() {
  const name = collectionName.value.trim();
  if (!name || collectionBusy.value) return;
  collectionBusy.value = true;
  try {
    const item = editingCollectionID.value
      ? await updateCollection(editingCollectionID.value, name, currentFilters())
      : await createCollection(name, currentFilters());
    await loadCollections();
    selectedCollectionID.value = item.id;
    closeCollectionEditor();
    notify(`已保存常用查询「${item.name}」`);
  } catch (error) { showError(error); }
  finally { collectionBusy.value = false; }
}

async function removeCollection() {
  const id = editingCollectionID.value;
  if (!id || collectionBusy.value || !window.confirm(`删除常用查询「${collectionName.value}」？`)) return;
  collectionBusy.value = true;
  try {
    await deleteCollection(id);
    if (selectedCollectionID.value === id) selectedCollectionID.value = "";
    await loadCollections();
    closeCollectionEditor();
    notify("常用查询已删除");
  } catch (error) { showError(error); }
  finally { collectionBusy.value = false; }
}

function clearSelection() {
  selectionGeneration++;
  selectingAll.value = false;
  selectionProgress.value = { loaded: 0, total: 0 };
  selectedIDs.value = new Set();
  selectedResources.value = new Map();
}

function removeSelected(ids: string[]) {
  const nextIDs = new Set(selectedIDs.value);
  const nextResources = new Map(selectedResources.value);
  for (const id of ids) {
    nextIDs.delete(id);
    nextResources.delete(id);
  }
  selectedIDs.value = nextIDs;
  selectedResources.value = nextResources;
}

function toggleSelection(id: string) {
  if (selectingAll.value || bulkBusy.value) return;
  const nextIDs = new Set(selectedIDs.value);
  const nextResources = new Map(selectedResources.value);
  if (nextIDs.has(id)) {
    nextIDs.delete(id);
    nextResources.delete(id);
  } else {
    const resource = resources.value.find((item) => item.id === id);
    if (!resource) return;
    nextIDs.add(id);
    nextResources.set(id, { id, kind: resource.kind, available: resource.available });
  }
  selectedIDs.value = nextIDs;
  selectedResources.value = nextResources;
}

async function selectAllMatching() {
  if (activeFolderID.value === null || selectingAll.value || bulkBusy.value || !total.value) return;
  const filters = currentFilters();
  const scopeKey = JSON.stringify(filters);
  const generation = ++selectionGeneration;
  selectingAll.value = true;
  selectionProgress.value = { loaded: 0, total: total.value };
  const matches = new Map<string, Pick<Resource, "id" | "kind" | "available">>();
  try {
    for (let offset = 0; ; offset += 1000) {
      const result = await getResourceSelection({ ...filters, limit: 1000, offset });
      if (generation !== selectionGeneration || scopeKey !== JSON.stringify(currentFilters())) return;
      for (const item of result.items) matches.set(item.id, { id: item.id, kind: item.kind, available: item.available });
      selectionProgress.value = { loaded: matches.size, total: result.total };
      if (!result.items.length || offset + result.items.length >= result.total) break;
    }
    selectedResources.value = matches;
    selectedIDs.value = new Set(matches.keys());
  } catch (error) {
    if (generation === selectionGeneration) showError(error);
  } finally {
    if (generation === selectionGeneration) selectingAll.value = false;
  }
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
  if (!name || !selectedIDs.value.size || bulkBusy.value || selectingAll.value) return;
  const ids = [...selectedIDs.value];
  bulkBusy.value = "tag";
  bulkProgress.value = { done: 0, total: ids.length };
  try {
    for (let offset = 0; offset < ids.length; offset += 1000) {
      const batch = ids.slice(offset, offset + 1000);
      await addBatchTag(batch, name);
      removeSelected(batch);
      bulkProgress.value = { done: offset + batch.length, total: ids.length };
    }
    bulkTagName.value = "";
    await Promise.all([loadResources(), loadTags()]);
    notify(`已为 ${ids.length} 项添加「${name}」`);
  } catch (error) {
    await Promise.all([loadResources(), loadTags()]);
    showError(error);
    if (bulkProgress.value.done && errorMessage.value) errorMessage.value += `；已处理 ${bulkProgress.value.done} 项，剩余 ${selectedCount.value} 项仍已选`;
  } finally { bulkBusy.value = ""; }
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
  const generation = authGeneration;
  const folderID = activeFolderID.value || "all";
  loadingBatchSuggestions.value = true;
  try {
    const result = await getBatchTagSuggestions(folderID);
    if (generation !== authGeneration || folderID !== (activeFolderID.value || "all")) return;
    batchSuggestions.value = result.suggestions;
    batchSuggestionsScanned.value = result.scanned;
  } catch (error) { if (generation === authGeneration) showError(error); }
  finally { if (generation === authGeneration) loadingBatchSuggestions.value = false; }
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
    const root = await addRoot(path, rootAutoPreprocess.value);
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

async function toggleRootPreprocessing(root: RootSummary) {
  if (rootSettingBusyID.value || scanningRootID.value === root.id) return;
  rootSettingBusyID.value = root.id;
  try {
    const enabled = !root.autoPreprocess;
    await addRoot(root.path, enabled);
    root.autoPreprocess = enabled;
    notify(enabled ? "已启用自动预处理" : "后续扫描将仅索引文件");
  } catch (error) { showError(error); }
  finally { rootSettingBusyID.value = ""; }
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

function applyTransportState(state: "connecting" | "webrtc" | "turn" | "relay") {
  nodeTransport.value = state;
  if (state === "relay") void refreshNodeStatus();
}

async function establishWebRTC(iceServers: RTCIceServer[] = []) {
  lastWebRTCAttempt = Date.now();
  await connectWebRTC(iceServers, applyTransportState, (rate) => { nodeTraffic.value = rate; });
}

async function changeResourceNode(event: Event) {
  const id = (event.target as HTMLSelectElement).value;
  if (!id || id === selectedResourceNodeID.value) return;
  try {
    await selectResourceNode(id);
    closeWebRTC();
    try { window.localStorage.setItem("myliba:selected-resource-node", id); } catch { /* Session polling still detects the change. */ }
    window.location.reload();
  } catch (error) {
    (event.target as HTMLSelectElement).value = selectedResourceNodeID.value;
    showError(error);
  }
}

async function removeOfflineResourceNode(node: HubResourceNode) {
  if (!window.confirm(`从 Hub 列表移除离线节点“${node.name}”？节点重新连接时会再次出现。`)) return;
  try {
    await removeResourceNode(node.id);
    window.location.reload();
  } catch (error) { showError(error); }
}

function openResourceFile(resource: Resource, download: boolean) {
  if (!download) {
    openDetail(resource);
    return;
  }
  startRelayDownload(resource);
}

async function submitLogin() {
  if (loginBusy.value) return;
  loginBusy.value = true;
  loginError.value = "";
  try {
    const session = await login(password.value);
    authGeneration++;
    authenticated.value = session.authenticated;
    ownerSession.value = session.owner ?? true;
    nodeOnline.value = session.nodeOnline;
    resourceNodes.value = session.nodes ?? [];
    selectedResourceNodeID.value = session.selectedNodeId ?? "";
    webTraffic.value = session.webTraffic ?? webTraffic.value;
    password.value = "";
    if (session.nodeOnline) await establishWebRTC(session.iceServers);
    else nodeTransport.value = "relay";
    await loadWorkspace();
    startStatusPolling();
    startPreprocessPolling();
  } catch (error) { loginError.value = error instanceof Error ? error.message : "登录失败"; }
  finally { loginBusy.value = false; }
}

function clearRemoteSession() {
  authGeneration++;
  requestGeneration++;
  authenticated.value = false;
  ownerSession.value = false;
  nodeOnline.value = false;
  resourceNodes.value = [];
  selectedResourceNodeID.value = "";
  password.value = "";
  resources.value = [];
  roots.value = [];
  tags.value = [];
  attributeCatalog.value = [];
  selectedAttributes.value = [];
  collections.value = [];
  folders.value = [];
  unfiledCount.value = 0;
  activeFolderID.value = null;
  selectedCollectionID.value = "";
  collectionEditorOpen.value = false;
  editingCollectionID.value = "";
  selectedKind.value = "";
  selectedRootID.value = "";
  sortOrder.value = "modified_desc";
  query.value = "";
  unorganizedOnly.value = false;
  batchSuggestions.value = [];
  tagSuggestions.value = [];
  total.value = 0;
  clearSelection();
  selectedTags.value = [];
  tagFilterOpen.value = false;
  closeResourcePreview();
  detailResourceID.value = "";
  editingTagID.value = "";
  errorMessage.value = "";
  toastMessage.value = "";
  stopStatusPolling();
  stopPreprocessPolling();
  closeWebRTC();
}

async function signOut() {
  try { await logout(); }
  catch (error) { showError(error); return; }
  clearRemoteSession();
}

function handleUnauthorized() {
  if (!remoteMode.value) return;
  clearRemoteSession();
}

function reloadForSelectedNode() {
  clearRemoteSession();
  const url = new URL(window.location.href);
  url.searchParams.delete("resource");
  url.searchParams.delete("folder");
  window.location.replace(url.href);
}

function handleNodeStorageChange(event: StorageEvent) {
  if (event.key === "myliba:selected-resource-node" && remoteMode.value && authenticated.value && event.newValue !== selectedResourceNodeID.value) reloadForSelectedNode();
}

async function refreshNodeStatus() {
  const generation = authGeneration;
  try {
    const session = await getSession();
    if (generation !== authGeneration) return;
    if (session) {
      if (!session.authenticated) {
        clearRemoteSession();
        return;
      }
      if ((session.selectedNodeId ?? "") !== selectedResourceNodeID.value) {
        reloadForSelectedNode();
        return;
      }
      nodeOnline.value = session.nodeOnline;
      ownerSession.value = session.owner ?? true;
      resourceNodes.value = session.nodes ?? [];
      selectedResourceNodeID.value = session.selectedNodeId ?? "";
      webTraffic.value = session.webTraffic ?? { downloadBps: 0, uploadBps: 0 };
      if (nodeTransport.value !== "webrtc" && nodeTransport.value !== "turn") {
        nodeTraffic.value = { downloadBps: session.nodeTraffic?.uploadBps ?? 0, uploadBps: session.nodeTraffic?.downloadBps ?? 0 };
      }
      if (session.nodeOnline && nodeTransport.value === "relay" && !retryingWebRTC && Date.now() - lastWebRTCAttempt >= 30_000) {
        retryingWebRTC = true;
        void establishWebRTC(session.iceServers).finally(() => { retryingWebRTC = false; });
      }
    }
  } catch { nodeOnline.value = false; }
}

async function refreshPreprocessJobs() {
  if (remoteMode.value && (!authenticated.value || !nodeOnline.value)) return;
  try {
    const ids = resources.value.filter((item) => item.available).map((item) => item.id).slice(-200);
    const queue = await getPreprocessQueue(ids);
    preprocessQueue.value = queue;
    for (const resource of resources.value) {
      if (["image", "video"].includes(resource.kind) && queue.visible[resource.id] === "done" && !thumbnailURLs.value.has(resource.id) && !loadingThumbnailIDs.has(resource.id)) {
        void loadThumbnail(resource);
      }
    }
  } catch { /* Keep the last queue snapshot during brief disconnects. */ }
}

function startPreprocessPolling() {
  if (jobsTimer) clearInterval(jobsTimer);
  void refreshPreprocessJobs();
  jobsTimer = setInterval(() => void refreshPreprocessJobs(), 2000);
}

function stopPreprocessPolling() {
  if (jobsTimer) clearInterval(jobsTimer);
  jobsTimer = undefined;
}

function preprocessStatusLabel(status: string) {
  return ({ pending: "排队中", running: "处理中", done: "已完成", failed: "失败" } as Record<string, string>)[status] || status;
}

function startStatusPolling() {
  stopStatusPolling();
  if (remoteMode.value) statusTimer = setInterval(() => void refreshNodeStatus(), 2000);
}

function stopStatusPolling() {
  if (statusTimer) clearInterval(statusTimer);
  statusTimer = undefined;
}

watch(resources, (items) => {
  const activeVersions = new Map(items.map((item) => [item.id, `${item.size}-${item.modified}`]));
  const urls = new Map(thumbnailURLs.value);
  const signatures = new Map(thumbnailVersionSignatures.value);
  for (const [id, url] of urls) {
    const resource = items.find((item) => item.id === id);
    const nextSignature = activeVersions.get(id);
    const priorSignature = signatures.get(id);
    if (!resource || (priorSignature !== undefined && priorSignature !== nextSignature)) {
      URL.revokeObjectURL(url);
      urls.delete(id);
    }
  }
  thumbnailURLs.value = urls;
  for (const resource of items) signatures.set(resource.id, `${resource.size}-${resource.modified}`);
  for (const id of signatures.keys()) if (!activeVersions.has(id)) signatures.delete(id);
  thumbnailVersionSignatures.value = signatures;
  for (const resource of items) void loadThumbnail(resource);
  const visibleIDs = items.filter((item) => item.available).map((item) => item.id).slice(-200);
  if (visibleIDs.length && (!remoteMode.value || (authenticated.value && nodeOnline.value))) {
    void prioritizePreprocessJobs(visibleIDs).then(() => refreshPreprocessJobs()).catch(() => {});
  }
}, { flush: "post" });

watch(query, () => {
  if (suppressQueryWatch) return;
  if (debounceTimer) clearTimeout(debounceTimer);
  debounceTimer = setTimeout(() => {
    if (!remoteMode.value || authenticated.value) void loadResources();
  }, 220);
});

onMounted(async () => {
  window.addEventListener("myliba:unauthorized", handleUnauthorized);
  window.addEventListener("storage", handleNodeStorageChange);
  window.addEventListener("keydown", onShortcut);
  window.addEventListener("popstate", syncDetailRoute);
  document.addEventListener("click", onTagEditorOutside, true);
  try {
    const session = await getSession();
    if (!session) {
      remoteMode.value = false;
      authenticated.value = true;
    } else {
      remoteMode.value = true;
      authenticated.value = session.authenticated;
      ownerSession.value = session.owner ?? true;
      nodeOnline.value = session.nodeOnline;
      resourceNodes.value = session.nodes ?? [];
      selectedResourceNodeID.value = session.selectedNodeId ?? "";
      webTraffic.value = session.webTraffic ?? webTraffic.value;
      nodeTraffic.value = session.nodeTraffic ?? nodeTraffic.value;
    }
    if (authenticated.value) {
      if (session?.nodeOnline) await establishWebRTC(session.iceServers);
      await loadWorkspace();
      startStatusPolling();
      startPreprocessPolling();
    }
  } catch (error) { showError(error); }
  finally { initialized.value = true; }

});

onBeforeUnmount(() => {
  if (previewURL.value) URL.revokeObjectURL(previewURL.value);
  window.removeEventListener("myliba:unauthorized", handleUnauthorized);
  window.removeEventListener("storage", handleNodeStorageChange);
  window.removeEventListener("keydown", onShortcut);
  window.removeEventListener("popstate", syncDetailRoute);
  document.removeEventListener("click", onTagEditorOutside, true);
  stopStatusPolling();
  stopPreprocessPolling();
  closeWebRTC();
  for (const url of thumbnailURLs.value.values()) URL.revokeObjectURL(url);
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
        <button class="nav-link" :class="{ active: activeFolderID === null }" @click="openFolder(null)">
          <House :size="17" /><span>文件夹首页</span>
        </button>
      </nav>
      <div class="sidebar-section-label folder-section-title"><span>文件夹</span><span class="sidebar-heading-actions"><span class="label-count">{{ folders.length }}</span><button class="sidebar-add" type="button" title="新建文件夹" aria-label="新建文件夹" :aria-expanded="sidebarFolderCreateOpen" @click="toggleSidebarFolderCreate"><Plus :size="14" /></button></span></div>
      <form v-if="sidebarFolderCreateOpen" class="sidebar-folder-create" @submit.prevent="submitFolder"><label class="sr-only" for="sidebar-new-folder-name">文件夹名称</label><input id="sidebar-new-folder-name" v-model="newFolderName" maxlength="80" placeholder="文件夹名称" autocomplete="off" /><button type="submit" :disabled="folderBusy || !newFolderName.trim()" aria-label="创建文件夹"><Plus :size="15" /></button></form>
      <nav class="folder-nav" aria-label="文件夹">
        <button class="nav-link" :class="{ active: activeFolderID === 'all' }" @click="openFolder('all')"><LibraryBig :size="16" /><span>全部资源</span><span class="nav-count">{{ totalIndexed }}</span></button>
        <button v-for="folder in folders" :key="folder.id" class="nav-link" :class="{ active: activeFolderID === folder.id }" @click="openFolder(folder.id)"><FolderOpen :size="16" /><span>{{ folder.name }}</span><span class="nav-count">{{ folder.count }}</span></button>
        <button class="nav-link" title="未归组：尚未放入虚拟文件夹，与手动标签无关" :class="{ active: activeFolderID === 'unfiled' }" @click="openFolder('unfiled')"><FolderOpen :size="16" /><span>未归组</span><span class="nav-count">{{ unfiledCount }}</span></button>
      </nav>
      <div v-if="activeFolderID !== null" class="folder-children" :aria-label="folderTitle + '的内容'">
        <div class="folder-children-title">{{ folderTitle }} · 内容</div>
        <nav class="primary-nav" aria-label="当前文件夹">
          <button class="nav-link" :class="{ active: unorganizedOnly && !activeCollection }" @click="chooseUnorganized"><Clock3 :size="17" /><span>待整理</span></button>
        </nav>

        <div class="sidebar-section-label collection-section-title">
          <span>常用查询</span><button class="sidebar-add" type="button" title="保存当前查询条件" aria-label="新建常用查询" @click="openCollectionEditor()"><Plus :size="14" /></button>
        </div>
        <nav class="collection-nav" aria-label="常用查询">
          <div v-for="item in visibleCollections" :key="item.id" class="collection-nav-row" :class="{ active: activeCollection?.id === item.id }">
            <button type="button" class="collection-nav-open" @click="chooseCollection(item)"><Bookmark :size="14" /><span>{{ item.name }}</span></button>
            <button type="button" class="collection-nav-edit" :aria-label="`编辑常用查询 ${item.name}`" @click="openCollectionEditor(item, true)"><Pencil :size="13" /></button>
          </div>
          <p v-if="!visibleCollections.length" class="sidebar-empty">保存当前搜索和筛选条件。</p>
        </nav>

        <div v-if="activeFolderID !== 'all'" class="sidebar-section-label tag-section-title">
          <span>手动标签</span><span class="label-count">{{ tags.length }}</span>
        </div>
        <nav v-if="activeFolderID !== 'all'" class="tag-nav" aria-label="手动标签筛选导航">
          <button v-for="tag in tags" :key="tag.id" class="tag-nav-link" :class="{ active: selectedTags.includes(tag.name) }" :aria-pressed="selectedTags.includes(tag.name)" @click="chooseSidebarTag(tag.name)">
            <Tag :size="15" /><span class="tag-nav-name">{{ tag.name }}</span><span class="tag-count">{{ tag.count }}</span><Check v-if="selectedTags.includes(tag.name)" :size="13" />
          </button>
          <p v-if="!tags.length" class="sidebar-empty">给资源添加标签后，会显示在这里。</p>
        </nav>
      </div>

      <div class="sidebar-bottom">
        <div class="node-status-card">
          <span class="node-status-icon" :class="{ offline: remoteMode && !nodeOnline }">
            <component :is="remoteMode && !nodeOnline ? WifiOff : remoteMode ? Wifi : HardDrive" :size="17" />
          </span>
          <span class="node-status-copy"><strong>{{ remoteMode ? (resourceNodes.find((node) => node.id === selectedResourceNodeID)?.name || "资源节点") : "本机节点" }}</strong><small>{{ remoteMode ? (nodeOnline ? "在线 · 可访问资源" : "离线 · 暂不可访问") : "索引保存在此设备" }}</small></span>
          <span class="status-dot" :class="{ offline: remoteMode && !nodeOnline }"></span>
        </div>
        <p class="sidebar-note"><span class="privacy-mark">◆</span> 原文件保留在原位置</p>
      </div>
    </aside>

    <div class="main-column">
      <header class="topbar">
        <div class="breadcrumbs"><button type="button" class="breadcrumb-home" @click="openFolder(null)">我的资源</button><span class="breadcrumb-divider">/</span><strong>{{ detailResourceID ? "资源详情" : activeFolderID === null ? "文件夹" : pageTitle }}</strong></div>
        <div class="topbar-actions">
          <template v-if="remoteMode">
            <label v-if="authenticated" class="resource-node-picker">资源节点
              <select :value="selectedResourceNodeID" aria-label="选择资源节点" @change="changeResourceNode">
                <option v-if="!resourceNodes.length" value="">暂无节点</option>
                <option v-for="node in resourceNodes" :key="node.id" :value="node.id">{{ node.name }}{{ node.kind === "android" ? " · 手机" : " · 桌面" }}{{ node.online ? "" : " · 离线" }}</option>
              </select>
            </label>
            <details v-if="authenticated && resourceNodes.some((node) => !node.online)" class="resource-node-cleanup">
              <summary>离线节点</summary>
              <div class="resource-node-cleanup-list">
                <div v-for="node in resourceNodes.filter((item) => !item.online)" :key="node.id">
                  <span>{{ node.name }}</span><button type="button" @click="removeOfflineResourceNode(node)">移除</button>
                </div>
              </div>
            </details>
            <span class="connection-badge traffic-badge" title="浏览器到服务端的 HTTPS 应用层流量；↓ 服务端发往网页，↑ 网页发往服务端。">
              <span class="connection-dot"></span><span>Web · HTTPS 接入</span><small>↓ {{ formatRate(webTraffic.downloadBps) }}　↑ {{ formatRate(webTraffic.uploadBps) }}</small>
            </span>
            <span class="connection-badge traffic-badge" :class="{ offline: !nodeOnline }" :title="nodeTransport === 'webrtc' ? 'WebRTC DataChannel 直连；↓ 节点发往网页，↑ 网页发往节点。' : nodeTransport === 'turn' ? 'WebRTC DataChannel 通过 TURN 中继；↓ 节点发往网页，↑ 网页发往节点。' : nodeTransport === 'connecting' ? '正在协商 WebRTC；连接失败后文件内容经限速 Hub 中转。' : 'WebRTC 不可用；文件内容经限速 Hub 中转。'">
              <span class="connection-dot"></span><span>{{ nodeTransport === 'webrtc' ? '节点 · WebRTC 直连' : nodeTransport === 'turn' ? '节点 · WebRTC / TURN' : nodeTransport === 'connecting' ? '节点 · WebRTC 连接中' : '节点 · Hub 限速中转' }}</span><small>↓ {{ formatRate(nodeTraffic.downloadBps) }}　↑ {{ formatRate(nodeTraffic.uploadBps) }}</small>
            </span>
          </template>
          <span v-else class="connection-badge"><span class="connection-dot"></span>本机模式</span>
          <HubSettings v-if="!remoteMode && authenticated" />
          <HubCloudDashboard v-if="remoteMode && authenticated && ownerSession" />
          <DeviceManager v-if="remoteMode && authenticated && ownerSession" />
          <CloudPanel v-if="authenticated" :resources="resources" :remote="remoteMode" />
          <button v-if="authenticated" class="button button-secondary preprocess-toggle" type="button" @click="preprocessOpen = true"><Clock3 :size="14" />后台任务 <span v-if="preprocessQueue.counts.pending + preprocessQueue.counts.running">({{ preprocessQueue.counts.pending + preprocessQueue.counts.running }})</span></button>
          <button v-if="remoteMode && authenticated" class="quiet-button signout-button" title="退出登录" @click="signOut"><LogOut :size="15" /><span>退出</span></button>
        </div>
      </header>

      <main class="page-content">
        <ResourceDetail v-if="detailResourceID && authenticated" :id="detailResourceID" :folders="folders" :remote="remoteMode" :phone-node="selectedResourceNodeKind === 'android'" @back="closeDetail" @tags-changed="loadTags(); loadResources()" @folder-changed="onDetailFolderChanged" />
        <template v-else>
        <section v-if="activeFolderID === null" class="page-intro">
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

        <section v-if="activeFolderID === null" class="virtual-folders-panel" aria-label="文件夹">
          <div class="virtual-folders-heading"><div><p class="eyebrow">顶层分组</p><h2>我的文件夹</h2><p>先按文件夹整理资源，进入后再用标签细分。文件保留在原位置。</p></div></div>
          <form class="virtual-folder-create" @submit.prevent="submitFolder"><label class="sr-only" for="new-folder-name">文件夹名称</label><input id="new-folder-name" v-model="newFolderName" maxlength="80" placeholder="新文件夹名称" /><button class="button button-primary" type="submit" :disabled="folderBusy || !newFolderName.trim()"><Plus :size="15" />新建文件夹</button></form>
          <div class="virtual-folder-grid">
            <button v-for="folder in folders" :key="folder.id" class="virtual-folder-card" type="button" @click="openFolder(folder.id)"><span class="virtual-folder-icon"><FolderOpen :size="22" /></span><strong>{{ folder.name }}</strong><small>{{ folder.count }} 项资源</small></button>
            <button class="virtual-folder-card virtual-folder-unfiled" type="button" @click="openFolder('unfiled')"><span class="virtual-folder-icon"><FolderOpen :size="22" /></span><strong>未归组</strong><small>{{ unfiledCount }} 项资源</small></button>
          </div>
          <p v-if="!folders.length" class="virtual-folder-hint">已有资源都在“未归组”。新建文件夹后，可批量选择文件并移动。</p>
        </section>

        <section v-if="activeFolderID === null && !remoteMode" class="roots-panel">
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
            <label class="root-preprocess-option"><input v-model="rootAutoPreprocess" type="checkbox" :disabled="rootBusy" /><span>扫描后自动执行预处理（生成缩略图和文件指纹）</span></label>
          </form>
          <div v-if="roots.length" class="root-list">
            <div v-for="root in roots" :key="root.id" class="root-row">
              <span class="root-folder-icon"><FolderOpen :size="15" /></span>
              <span class="root-path" :title="root.path">{{ root.path }}</span>
              <span class="root-count">{{ root.count.toLocaleString("zh-CN") }} 个文件 · {{ root.autoPreprocess ? "自动预处理" : "仅扫描" }}</span>
              <button class="button button-ghost root-preprocess-switch" type="button" :disabled="!!rootSettingBusyID || scanningRootID === root.id" @click="toggleRootPreprocessing(root)">{{ root.autoPreprocess ? "关闭预处理" : "开启预处理" }}</button>
              <button class="button button-ghost rescan-button" :disabled="scanningRootID === root.id" @click="rescanRoot(root)">
                <LoaderCircle v-if="scanningRootID === root.id" class="spin" :size="15" /><RefreshCw v-else :size="15" /><span>重新扫描</span>
              </button>
            </div>
          </div>
          <div class="panel-heading">
            <span class="panel-icon"><HardDrive :size="18" /></span>
            <span class="panel-heading-copy"><strong>生成内容存储位置</strong><small>缩略图和多尺寸视频可保存到多个本机目录；默认位置用于新生成的内容，旧文件保持原位。</small></span>
          </div>
          <form class="root-form" @submit.prevent="submitMediaStore">
            <label class="sr-only" for="media-store-path">媒体存储目录</label>
            <input id="media-store-path" v-model="mediaStorePath" type="text" placeholder="已有的本机目录路径" autocomplete="off" />
            <button class="button button-primary" type="submit" :disabled="mediaStoreBusy || !mediaStorePath.trim()"><Plus :size="16" />添加位置</button>
          </form>
          <div v-if="mediaStores.length" class="root-list">
            <div v-for="store in mediaStores" :key="store.id" class="root-row">
              <span class="root-folder-icon"><HardDrive :size="15" /></span>
              <span class="root-path" :title="store.path">{{ store.path }}</span>
              <span class="root-count">{{ store.isDefault ? "新内容默认位置" : "已配置" }}</span>
              <button v-if="!store.isDefault" class="button button-ghost" type="button" :disabled="mediaStoreBusy" @click="makeDefaultMediaStore(store.id)">设为默认</button>
            </div>
          </div>
        </section>

        <section v-else-if="activeFolderID === null" class="roots-panel remote-roots-panel">
          <div class="panel-heading">
            <span class="panel-icon"><HardDrive :size="18" /></span>
            <span class="panel-heading-copy"><strong>{{ selectedResourceNodeKind === "android" ? "手机索引目录" : "家中索引目录" }}</strong><small>{{ selectedResourceNodeKind === "android" ? "目录由手机 App 管理，可在手机上重新扫描。" : "目录保存在家中节点。远程访问时可以更新索引。" }}</small></span>
            <span class="remote-root-count">{{ roots.length }} 个目录</span>
          </div>
          <div v-if="roots.length" class="root-list">
            <div v-for="root in roots" :key="root.id" class="root-row">
              <span class="root-folder-icon"><FolderOpen :size="15" /></span>
              <span class="root-path" :title="root.path">{{ root.path }}</span>
              <span class="root-count">{{ root.count.toLocaleString("zh-CN") }} 个文件</span>
              <button v-if="selectedResourceNodeKind !== 'android'" class="button button-ghost rescan-button" :disabled="scanningRootID === root.id || !nodeOnline" @click="rescanRoot(root)">
                <LoaderCircle v-if="scanningRootID === root.id" class="spin" :size="15" /><FolderSync v-else :size="15" /><span>重新扫描</span>
              </button>
            </div>
          </div>
          <div v-else-if="nodeOnline" class="remote-roots-empty">家中节点还没有索引目录。</div>
          <div v-else class="remote-roots-empty"><WifiOff :size="15" />家中节点离线时无法读取目录列表。</div>
        </section>

        <template v-else>
        <div class="folder-context-bar"><button class="quiet-button" @click="openFolder(null)"><House :size="14" />文件夹首页</button><span>/</span><strong>{{ folderTitle }}</strong><div v-if="activeFolderID !== 'all' && activeFolderID !== 'unfiled'" class="folder-context-actions"><button class="quiet-button" @click="editingFolderName = folderTitle; folderEditorOpen = !folderEditorOpen"><Pencil :size="14" />重命名</button><button class="quiet-button" @click="removeFolder"><Trash2 :size="14" />删除</button></div></div>
        <p v-if="activeFolderID === 'unfiled'" class="folder-scope-hint">尚未放入虚拟文件夹的资源会显示在这里；“待整理”指没有手动标签的资源。</p>
        <form v-if="folderEditorOpen && activeFolderID !== 'all' && activeFolderID !== 'unfiled'" class="folder-rename-form" @submit.prevent="saveFolderName"><input v-model="editingFolderName" maxlength="80" aria-label="文件夹名称" /><button class="button button-primary" :disabled="folderBusy || !editingFolderName.trim()">保存</button><button type="button" class="button button-secondary" @click="folderEditorOpen = false">取消</button></form>
        <div v-if="visibleCollections.length" class="mobile-collections" aria-label="常用查询">
          <span>常用查询</span>
          <button v-for="item in visibleCollections" :key="item.id" type="button" :class="{ active: activeCollection?.id === item.id }" @click="chooseCollection(item)"><Bookmark :size="13" />{{ item.name }}</button>
        </div>

        <section class="collection-toolbar">
          <div class="collection-heading">
            <span class="collection-symbol"><Bookmark v-if="activeCollection" :size="17" /><LayoutGrid v-else :size="17" /></span>
            <div><h2>{{ pageTitle }}</h2><p>{{ activeCollection ? "按保存的条件动态更新" : selectedTags.length ? "同时包含所选标签的资源" : unorganizedOnly ? "还没有添加手动标签的资源" : "搜索和筛选你的资源" }}</p></div>
          </div>
          <div class="collection-actions">
            <label class="search-field" for="resource-search">
              <Search :size="17" />
              <input id="resource-search" v-model="query" type="search" placeholder="搜索文件名、路径或手动标签" autocomplete="off" />
              <kbd>⌘ K</kbd>
              <button v-if="query" class="search-clear" type="button" aria-label="清除搜索" @click="query = ''"><X :size="14" /></button>
            </label>
            <button class="button button-secondary tag-filter-toggle" type="button" :class="{ selected: selectedTags.length || activeAttributeKeys.length }" :aria-expanded="tagFilterOpen" aria-controls="tag-filter-options" @click="toggleTagFilter"><Tag :size="15" /><span>标签筛选{{ selectedTags.length + activeAttributeKeys.length ? ' · ' + (selectedTags.length + activeAttributeKeys.length) : '' }}</span><ChevronDown :size="14" /></button>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'grid' }" title="网格视图" @click="listMode = 'grid'"><LayoutGrid :size="16" /></button>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'list' }" title="列表视图" @click="listMode = 'list'"><List :size="16" /></button>
          </div>
        </section>

        <section v-if="tagFilterOpen" id="tag-filter-options" class="tag-filter-panel" aria-label="标签筛选">
          <div class="tag-filter-panel-heading"><strong>标签筛选</strong><button type="button" aria-label="收起标签筛选" @click="tagFilterOpen = false"><ChevronDown :size="15" /></button></div>
          <div class="tag-filter-group">
            <div class="tag-filter-group-heading"><strong>手动标签</strong><label class="tag-filter-mode"><input v-model="multipleFilters" type="checkbox" @change="changeFilterMode" />多选</label><span>{{ multipleFilters ? "同时满足所选手动标签；同类属性可多选" : "单选；点击新标签替换旧标签，同类属性只选一项" }}</span></div>
            <label class="tag-filter-search" for="tag-filter-search"><Search :size="14" /><input id="tag-filter-search" v-model="tagFilterQuery" type="search" placeholder="查找手动标签" autocomplete="off" /></label>
            <div class="tag-filter-list">
              <button v-for="tag in matchingTags" :key="tag.id" class="tag-filter-option" type="button" :class="{ active: selectedTags.includes(tag.name) }" :aria-pressed="selectedTags.includes(tag.name)" @click="toggleTag(tag.name)"><span>{{ tag.name }}</span><small>{{ tag.count }}</small><Check v-if="selectedTags.includes(tag.name)" :size="14" /></button>
              <p v-if="!matchingTags.length" class="tag-filter-empty">{{ tags.length ? "没有匹配的标签" : "还没有标签" }}</p>
            </div>
            <button v-if="selectedTags.length" class="tag-filter-clear" type="button" @click="clearSelectedTags">清除已选手动标签</button>
          </div>
          <div class="tag-filter-group">
            <div class="tag-filter-group-heading"><strong>属性标签</strong><span>按文件属性自动更新</span></div>
            <div v-if="attributeCatalogLoading && !attributeCatalog.length" class="attribute-filter-loading"><LoaderCircle class="spin" :size="14" />正在统计当前条件…</div>
            <div v-if="attributeTypes.length" class="attribute-filter-row"><span class="attribute-filter-label">文件类型</span><div class="attribute-filter-chips"><button v-for="tag in attributeTypes" :key="tag.key" type="button" class="attribute-filter-chip" :disabled="attributeCatalogLoading" :class="{ active: activeAttributeKeys.includes(tag.key) }" :aria-pressed="activeAttributeKeys.includes(tag.key)" @click="toggleAttribute(tag.key)">{{ tag.label }}<small>{{ attributeCatalogLoading ? '…' : (tag.count ?? 0) }}</small><Check v-if="activeAttributeKeys.includes(tag.key)" :size="12" /></button></div></div>
            <div v-if="attributeSizes.length" class="attribute-filter-row"><span class="attribute-filter-label">文件大小</span><div class="attribute-filter-chips"><button v-for="tag in attributeSizes" :key="tag.key" type="button" class="attribute-filter-chip" :disabled="attributeCatalogLoading" :class="{ active: activeAttributeKeys.includes(tag.key) }" :aria-pressed="activeAttributeKeys.includes(tag.key)" @click="toggleAttribute(tag.key)">{{ tag.label }}<small>{{ attributeCatalogLoading ? '…' : (tag.count ?? 0) }}</small><Check v-if="activeAttributeKeys.includes(tag.key)" :size="12" /></button></div></div>
            <div v-if="attributeCatalog.some((tag) => tag.category === 'extension')" class="attribute-filter-row"><span class="attribute-filter-label">文件格式</span><div class="attribute-format-area"><input v-if="attributeCatalog.filter((tag) => tag.category === 'extension').length > 12" v-model="attributeFormatQuery" type="search" placeholder="查找格式，如 PDF" aria-label="查找文件格式" /><div class="attribute-filter-chips attribute-format-chips"><button v-for="tag in attributeFormats" :key="tag.key" type="button" class="attribute-filter-chip" :disabled="attributeCatalogLoading" :class="{ active: activeAttributeKeys.includes(tag.key) }" :aria-pressed="activeAttributeKeys.includes(tag.key)" @click="toggleAttribute(tag.key)">{{ tag.label }}<small>{{ attributeCatalogLoading ? '…' : (tag.count ?? 0) }}</small><Check v-if="activeAttributeKeys.includes(tag.key)" :size="12" /></button><span v-if="!attributeFormats.length" class="attribute-format-empty">没有匹配的格式</span></div></div></div>
            <p v-if="!attributeCatalog.length && !attributeCatalogLoading" class="tag-filter-empty">当前没有可用的属性标签</p>
          </div>
        </section>

        <div class="resource-filter-controls" aria-label="更多筛选和排序">
          <label>来源<select v-model="selectedRootID" @change="loadResources()"><option value="">全部目录</option><option v-for="root in roots" :key="root.id" :value="root.id">{{ root.path }}</option></select></label>
          <label>排序<select v-model="sortOrder" @change="loadResources()"><option value="modified_desc">最近修改</option><option value="modified_asc">最早修改</option><option value="name_asc">名称 A-Z</option><option value="name_desc">名称 Z-A</option><option value="size_desc">文件最大</option></select></label>
        </div>

        <form v-if="collectionEditorOpen" class="collection-editor" @submit.prevent="saveCollection">
          <div><strong>{{ editingCollectionID ? "编辑常用查询" : "保存为常用查询" }}</strong><p>保存当前搜索、手动标签、属性标签、目录和排序条件；下次打开时按最新资源重新查询。</p></div>
          <label class="sr-only" for="collection-name">查询名称</label>
          <input id="collection-name" v-model="collectionName" maxlength="80" placeholder="给查询起个名字" autocomplete="off" />
          <button class="button button-primary" type="submit" :disabled="collectionBusy || !collectionName.trim()">{{ editingCollectionID ? "保存更改" : "保存查询" }}</button>
          <button class="button button-secondary" type="button" @click="closeCollectionEditor">取消</button>
          <button v-if="editingCollectionID" class="quiet-button collection-delete" type="button" :disabled="collectionBusy" @click="removeCollection"><Trash2 :size="14" />删除</button>
        </form>

        <div v-if="selectedTags.length" class="selected-tag-filters" aria-label="已选标签">
          <span>{{ selectedTags.length > 1 ? "同时包含" : "手动标签" }}</span>
          <button v-for="name in selectedTags" :key="name" type="button" :aria-label="`移除标签 ${name}`" @click="toggleTag(name)"><Tag :size="12" />{{ name }}<X :size="12" /></button>
        </div>

        <div v-if="activeAttributeKeys.length" class="selected-tag-filters selected-attribute-filters" aria-label="已选属性标签">
          <span>属性条件</span>
          <button v-for="key in activeAttributeKeys" :key="key" type="button" :aria-label="'移除属性 ' + attributeLabel(key)" @click="toggleAttribute(key)">{{ attributeLabel(key) }}<X :size="12" /></button>
          <button type="button" class="clear-attribute-filters" @click="clearAttributes">清除属性</button>
        </div>

        <div class="results-toolbar">
          <div class="results-count"><span>{{ isFiltered ? "筛选结果" : "所有资源" }}</span><strong>{{ total.toLocaleString("zh-CN") }}</strong></div>
          <div class="results-toolbar-actions">
            <button v-if="total" class="button button-secondary select-all-results" :disabled="selectingAll || !!bulkBusy || (selectedCount === total && !loading)" @click="selectAllMatching"><LoaderCircle v-if="selectingAll" class="spin" :size="14" /><Check v-else :size="14" />{{ selectingAll ? `选择中 ${selectionProgress.loaded}/${selectionProgress.total}` : selectedCount === total && !loading ? `已全选 ${total} 项` : `全选结果（${total}）` }}</button>
            <button class="quiet-button batch-suggestion-toggle" :aria-expanded="batchSuggestionsOpen" title="按目录和文件名特征分析当前文件夹" @click="toggleBatchSuggestions"><Sparkles :size="14" />批量建议</button>
            <button class="quiet-button save-collection-toggle" @click="openCollectionEditor()"><BookmarkPlus :size="14" />保存集合</button>
            <button v-if="selectedCollection" class="quiet-button save-collection-toggle" @click="openCollectionEditor(selectedCollection)"><Pencil :size="14" />{{ activeCollection ? "编辑集合" : "更新集合" }}</button>
            <button v-if="isFiltered" class="quiet-button clear-filters" @click="chooseAll"><X :size="14" />清除筛选</button>
            <span v-else class="sort-label"><Clock3 :size="14" />最近修改</span>
          </div>
        </div>

        <section v-if="batchSuggestionsOpen" class="batch-suggestions-panel" aria-label="批量标签建议">
          <header class="batch-suggestions-header">
            <div><strong>批量整理建议</strong><p>{{ loadingBatchSuggestions ? `正在扫描当前分组资源` : `已扫描 ${batchSuggestionsScanned} 项资源` }}；建议根据文件夹和文件名特征生成。</p></div>
            <button class="quiet-button batch-suggestions-close" aria-label="收起批量建议" title="收起" @click="batchSuggestionsOpen = false"><X :size="16" /></button>
          </header>
          <div v-if="loadingBatchSuggestions" class="batch-suggestions-state"><LoaderCircle class="spin" :size="16" />正在分析当前分组资源…</div>
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

        <div v-if="selectedCount || selectingAll" class="bulk-toolbar">
          <div class="selection-summary"><span class="selection-check"><Check :size="14" /></span><strong>{{ selectingAll ? selectionProgress.loaded : selectedCount }}</strong><span>{{ selectingAll ? `/${selectionProgress.total} 项选择中` : '项已选' }}</span></div>
          <button v-if="selectedMediaCount" class="button button-secondary thumbnail-batch-button" :disabled="generatingThumbnailBatch || !!bulkBusy || selectingAll" @click="generateSelectedThumbnails"><LoaderCircle v-if="generatingThumbnailBatch" class="spin" :size="14" /><Image v-else :size="14" />{{ generatingThumbnailBatch ? `正在排队 ${thumbnailBatchProgress.done}/${thumbnailBatchProgress.total}` : `重新排队缩略图（${selectedMediaCount}）` }}</button>
          <span class="bulk-divider"></span>
          <input v-model="bulkTagName" maxlength="40" type="text" placeholder="输入要添加的标签" :disabled="!!bulkBusy || selectingAll" @keydown.enter.prevent="applyBatchTag" />
          <button class="button button-primary" :disabled="!bulkTagName.trim() || !!bulkBusy || selectingAll" @click="applyBatchTag"><LoaderCircle v-if="bulkBusy === 'tag'" class="spin" :size="15" /><Plus v-else :size="15" />{{ bulkBusy === 'tag' ? `添加中 ${bulkProgress.done}/${bulkProgress.total}` : '批量添加标签' }}</button>
          <span class="bulk-divider"></span>
          <label class="bulk-folder-picker">移动到<select v-model="bulkFolderID" :disabled="!!bulkBusy || selectingAll"><option value="">未归组</option><option v-for="folder in folders" :key="folder.id" :value="folder.id">{{ folder.name }}</option></select></label>
          <button class="button button-secondary" :disabled="!!bulkBusy || selectingAll" @click="moveSelectedToFolder"><LoaderCircle v-if="bulkBusy === 'folder'" class="spin" :size="15" /><FolderOpen v-else :size="15" />{{ bulkBusy === 'folder' ? `移动中 ${bulkProgress.done}/${bulkProgress.total}` : '移动' }}</button>
          <button class="quiet-button cancel-selection" :disabled="!!bulkBusy" @click="clearSelection">取消选择</button>
        </div>

        <div v-if="errorMessage" class="notice notice-error" role="alert"><CircleAlert :size="17" /><span>{{ errorMessage }}</span><button class="notice-close" aria-label="关闭提示" @click="errorMessage = ''"><X :size="15" /></button></div>

        <div v-if="loading && !resources.length" class="resource-grid skeleton-grid" :class="{ 'list-mode': listMode === 'list' }" aria-label="正在加载资源">
          <div v-for="index in 8" :key="index" class="skeleton-card"><div class="skeleton-preview"></div><div class="skeleton-line"></div><div class="skeleton-line short"></div></div>
        </div>

        <section v-else-if="resources.length" class="resource-grid" :class="{ 'list-mode': listMode === 'list' }" aria-label="资源列表">
          <article v-for="resource in resources" :key="resource.id" class="resource-card" :class="{ unavailable: !resource.available, selected: selectedIDs.has(resource.id) }">
            <label class="resource-select" :aria-label="`选择${resource.name}`">
              <input type="checkbox" :checked="selectedIDs.has(resource.id)" :disabled="selectingAll || !!bulkBusy" @change="toggleSelection(resource.id)" />
              <span class="check-indicator"><Check :size="13" /></span>
            </label>
            <div class="resource-preview" role="button" tabindex="0" :aria-label="'预览 ' + resource.name" @click="openResourcePreview(resource)" @keydown.enter.self.prevent="openResourcePreview(resource)" @keydown.space.self.prevent="openResourcePreview(resource)">
              <img v-if="thumbnailURLs.has(resource.id) && !failedThumbnailIDs.has(resource.id)" loading="lazy" :src="thumbnailURLs.get(resource.id)" :alt="resource.name" @error="markThumbnailFailed(resource.id)" />
              <div v-if="!resource.available || !['image', 'video'].includes(resource.kind) || failedThumbnailIDs.has(resource.id) || !thumbnailURLs.has(resource.id)" class="preview-placeholder"><component :is="kindIcon(resource.kind)" :size="29" :stroke-width="1.5" /></div>
              <button v-if="resource.available && ['image', 'video'].includes(resource.kind) && failedThumbnailIDs.has(resource.id) && preprocessQueue.visible[resource.id] === 'failed'" class="thumbnail-generate-button" :disabled="isGeneratingThumbnail(resource.id)" @click.stop="generateThumbnail(resource.id)"><LoaderCircle v-if="isGeneratingThumbnail(resource.id)" class="spin" :size="13" />{{ isGeneratingThumbnail(resource.id) ? '正在排队' : '重试缩略图' }}</button>
              <span v-else-if="resource.available && ['image', 'video'].includes(resource.kind) && !thumbnailURLs.has(resource.id) && ['pending', 'running'].includes(preprocessQueue.visible[resource.id] || '')" class="thumbnail-generate-button thumbnail-waiting">{{ preprocessStatusLabel(preprocessQueue.visible[resource.id]) }}</span>
              <span v-if="resource.available && resource.kind === 'video' && thumbnailURLs.has(resource.id) && !failedThumbnailIDs.has(resource.id)" class="video-preview-play" aria-hidden="true"><Play :size="17" fill="currentColor" /></span>
              <span class="type-pill">{{ kindLabel(resource.kind) }}</span>
              <span v-if="!resource.available" class="unavailable-pill">来源暂不可用</span>
            </div>
            <div class="resource-info">
              <p class="resource-name" :title="resource.path"><button type="button" class="resource-name-button" @click="openResourcePreview(resource)">{{ resource.name }}</button></p>
              <div class="resource-meta"><span>{{ formatBytes(resource.contentSize ?? resource.size) }}</span><span class="meta-dot"></span><span>{{ formatDate(resource.modified) }}</span></div>
              <div v-if="resource.available" class="resource-file-actions">
                <button class="resource-file-action" type="button" @click="openResourcePreview(resource)"><Image :size="13" />预览</button>
                <button class="resource-file-action" type="button" @click="openResourceFile(resource, false)"><ExternalLink :size="13" />详情</button>
                <button class="resource-file-action" type="button" @click="openResourceFile(resource, true)"><Download :size="13" />下载</button>
              </div>
              <p v-else class="resource-unavailable-note">源文件当前不可用</p>
              <div class="resource-tags">
                <button v-for="tagName in resource.tags" :key="tagName" class="tag-chip" :aria-pressed="selectedTags.includes(tagName)" @click="toggleTag(tagName)"><Tag :size="11" />{{ tagName }}</button>
                <span v-if="!resource.tags.length && editingTagID !== resource.id" class="no-tags">未添加手动标签</span>
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
              <div v-if="resource.attributeTags?.length" class="resource-attributes" aria-label="文件属性标签"><button v-for="attribute in resource.attributeTags" :key="attribute.key" type="button" class="resource-attribute-chip" :class="{ active: activeAttributeKeys.includes(attribute.key) }" :aria-pressed="activeAttributeKeys.includes(attribute.key)" @click="toggleAttribute(attribute.key)">{{ attribute.category === 'type' ? '类型' : attribute.category === 'size' ? '大小' : '格式' }}：{{ attribute.label }}</button></div>
            </div>
          </article>
        </section>

        <section v-else-if="initialized && !loading" class="empty-state">
          <span class="empty-icon"><FolderOpen :size="25" /></span>
          <h2>{{ isFiltered ? "没有找到匹配资源" : unorganizedOnly ? "整理完成" : "这里还没有资源" }}</h2>
          <p>{{ isFiltered ? "试试其他关键词，或清除筛选条件。" : unorganizedOnly ? "当前没有未添加手动标签的资源。" : activeFolderID === "unfiled" ? "新索引的文件会先出现在这里。" : "从未归组选择资源，再移动到这个文件夹。" }}</p>
          <button v-if="!isFiltered && !unorganizedOnly && activeFolderID !== 'unfiled'" class="button button-primary" @click="openFolder('unfiled')"><FolderOpen :size="16" />前往未归组</button>
          <button v-else-if="isFiltered" class="button button-secondary" @click="chooseAll">清除筛选</button>
        </section>

        <div v-if="hasMore && !loading" class="load-more-wrap"><button class="button button-secondary" @click="pageOffset = resources.length; loadResources(true)">加载更多</button></div>
        <div v-if="loading && resources.length" class="loading-more"><LoaderCircle class="spin" :size="16" />正在加载…</div>
        </template>

        <footer class="page-footer"><span>MYLIBA · 个人资源管理器</span><span><span class="footer-dot"></span>{{ remoteMode ? (nodeOnline ? "家中节点在线" : "等待节点连接") : "本机索引已就绪" }}</span></footer>
        </template>
      </main>
    </div>

    <nav class="mobile-nav" aria-label="资源导航">
      <button :class="{ active: activeFolderID === null }" @click="openFolder(null)"><House :size="17" /><span>文件夹</span></button>
      <button :class="{ active: activeFolderID === 'unfiled' }" @click="openFolder('unfiled')"><FolderOpen :size="17" /><span>未归组</span></button>
      <button v-if="activeFolderID !== null" :class="{ active: selectedTags.length + activeAttributeKeys.length > 0 }" @click="openTagFilterFromMobile"><Tag :size="17" /><span>标签{{ selectedTags.length + activeAttributeKeys.length ? ' ' + (selectedTags.length + activeAttributeKeys.length) : '' }}</span></button>
    </nav>

    <div v-if="preprocessOpen" class="resource-preview-backdrop" @click.self="preprocessOpen = false">
      <section class="preprocess-dialog" role="dialog" aria-modal="true" aria-label="后台任务">
        <header class="resource-preview-header">
          <div class="resource-preview-heading"><h2>后台任务</h2><p>已启用自动预处理的目录在扫描后入队；当前浏览的资源优先</p></div>
          <button class="quiet-button directory-close" aria-label="关闭后台任务" @click="preprocessOpen = false"><X :size="18" /></button>
        </header>
        <div class="preprocess-summary">
          <span>排队 {{ preprocessQueue.counts.pending }}</span><span>处理中 {{ preprocessQueue.counts.running }}</span><span>失败 {{ preprocessQueue.counts.failed }}</span><span>完成 {{ preprocessQueue.counts.done }}</span>
          <button class="button button-secondary preprocess-retry-all" type="button" :disabled="retryingFailedJobs || !preprocessQueue.counts.failed" @click="retryAllFailedJobs"><LoaderCircle v-if="retryingFailedJobs" class="spin" :size="13" /><RefreshCw v-else :size="13" />{{ retryingFailedJobs ? "正在重试" : "重试全部失败任务" }}</button>
        </div>
        <div class="preprocess-list">
          <div v-for="job in preprocessQueue.jobs" :key="job.resourceId + job.operation" class="preprocess-row">
            <component :is="job.operation === 'thumbnail' ? Image : File" :size="16" /><div><strong :title="job.name">{{ job.name }}</strong><small>{{ job.operation === 'thumbnail' ? '缩略图 / 封面' : '文件指纹' }} · {{ preprocessStatusLabel(job.status) }}<span v-if="job.priority && job.status === 'pending'"> · 当前列表优先</span></small><small v-if="job.error" class="preprocess-error" :title="job.error">{{ job.error }}</small></div>
            <button v-if="job.status === 'failed'" class="button button-secondary" @click="generateThumbnail(job.resourceId)">重试</button>
          </div>
          <p v-if="!preprocessQueue.jobs.length" class="directory-empty">任务队列为空</p>
        </div>
      </section>
    </div>

    <ImagePreview v-if="previewResource && previewMimeType.startsWith('image/') && !previewError" :key="previewResource.id" :src="thumbnailURLs.get(previewResource.id)" :resource="previewResource" :name="previewResource.name" :position="previewPosition" :total="previewTotal" :can-previous="canPreviewPrevious" :can-next="canPreviewNext" :loading-next="previewLoadingNext" @close="closeResourcePreview" @download="startRelayDownload(previewResource)" @details="openDetail(previewResource); closeResourcePreview()" @previous="moveResourcePreview(-1)" @next="moveResourcePreview(1)" />
    <div v-else-if="previewResource" class="resource-preview-backdrop resource-browser-preview" @click.self="closeResourcePreview">
      <section class="resource-preview-dialog" role="dialog" aria-modal="true" :aria-label="'预览 ' + previewResource.name">
        <header class="resource-preview-header">
          <div class="resource-preview-heading">
            <h2>{{ previewResource.name }}</h2>
            <p>{{ formatBytes(previewResource.size) }} · {{ kindLabel(previewResource.kind) }}</p>
          </div>
          <div class="resource-preview-navigation">
            <button type="button" aria-label="上一个文件" :disabled="!canPreviewPrevious" @click="moveResourcePreview(-1)"><ChevronLeft :size="20" /></button>
            <span>{{ previewPosition }} / {{ previewTotal }}</span>
            <button type="button" aria-label="下一个文件" :disabled="!canPreviewNext || previewLoadingNext" @click="moveResourcePreview(1)"><LoaderCircle v-if="previewLoadingNext" class="spin" :size="18" /><ChevronRight v-else :size="20" /></button>
          </div>
          <button class="resource-preview-download" type="button" @click="openDetail(previewResource); closeResourcePreview()"><ExternalLink :size="14" />详情</button>
          <button class="resource-preview-download" type="button" @click="openResourceFile(previewResource, true)"><Download :size="14" />下载</button>
          <button class="quiet-button directory-close" aria-label="关闭预览" @click="closeResourcePreview"><X :size="18" /></button>
        </header>
        <div class="resource-preview-stage">
          <template v-if="previewMimeType && !previewError">
              <StreamingVideo v-if="previewMimeType.startsWith('video/')" :key="previewResource.id" :id="previewResource.id" :name="previewResource.name" :remote="remoteMode" :phone-node="selectedResourceNodeKind === 'android'" :resource="previewResource" :poster="thumbnailURLs.get(previewResource.id)" :file-size="previewResource.size" />
              <audio v-else-if="previewMimeType.startsWith('audio/') && (!remoteMode || previewURL)" :src="remoteMode ? previewURL : fileURL(previewResource.id)" controls preload="metadata" @error="previewError = '浏览器无法播放此音频格式。你可以下载后打开。'"></audio>
              <iframe v-else-if="!remoteMode || previewURL" :src="remoteMode ? previewURL : fileURL(previewResource.id)" :title="'预览 ' + previewResource.name"></iframe>
          </template>
          <div v-else-if="previewError" class="resource-preview-message" role="alert"><CircleAlert :size="22" /><p>{{ previewError }}</p><button class="button button-secondary" @click="openResourceFile(previewResource, true)"><Download :size="14" />下载文件</button></div>
          <div v-else class="resource-preview-message"><File :size="24" /><p>此文件格式暂不支持网页预览，可以下载后用本地应用打开。</p><button class="button button-primary" @click="openResourceFile(previewResource, true)"><Download :size="14" />下载文件</button></div>
        </div>
      </section>
    </div>

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
