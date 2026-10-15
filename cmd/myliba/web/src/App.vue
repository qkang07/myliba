<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { closeWebRTC, connectWebRTC } from "./webrtc";
import ResourceDetail from "./ResourceDetail.vue";
import {
  Check,
  ChevronDown,
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
  createCollection,
  deleteCollection,
  ApiError,
  fileURL,
  getResourceThumbnail,
  createThumbnail,
  getBatchTagSuggestions,
  getResources,
  getCollections,
  getRoots,
  getDirectories,
  getSession,
  getTagSuggestions,
  getTags,
  login,
  logout,
  scanRoot,
  updateCollection,
  type DirectoryListing,
  type BatchTagSuggestion,
  type CollectionFilters,
  type SmartCollection,
  type ResourceSort,
  type Resource,
  type RootSummary,
  type TagSummary,
  type TagSuggestion,
  type TrafficRate,
} from "./api";

const remoteMode = ref(false);
const authenticated = ref(true);
const nodeOnline = ref(false);
const nodeTransport = ref<"connecting" | "webrtc" | "turn" | "relay">("relay");
const webTraffic = ref<TrafficRate>({ downloadBps: 0, uploadBps: 0 });
const nodeTraffic = ref<TrafficRate>({ downloadBps: 0, uploadBps: 0 });
const loading = ref(false);
const initialized = ref(false);
const errorMessage = ref("");
const resources = ref<Resource[]>([]);
const failedThumbnailIDs = ref(new Set<string>());
const thumbnailURLs = ref(new Map<string, string>());
const thumbnailVersionSignatures = ref(new Map<string, string>());
const loadingThumbnailIDs = new Set<string>();
const generatingThumbnailIDs = ref(new Set<string>());
const generatingThumbnailBatch = ref(false);
const thumbnailRefreshTokens = ref(new Map<string, number>());
const thumbnailBatchProgress = ref({ done: 0, total: 0 });
const roots = ref<RootSummary[]>([]);
const tags = ref<TagSummary[]>([]);
const total = ref(0);
const query = ref("");
const selectedTags = ref<string[]>([]);
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
const detailResourceID = ref(new URLSearchParams(window.location.search).get("resource") || "");
const previewResource = ref<Resource | null>(null);
const previewMimeType = ref("");
const previewError = ref("");

let debounceTimer: ReturnType<typeof setTimeout> | undefined;
let toastTimer: ReturnType<typeof setTimeout> | undefined;
let statusTimer: ReturnType<typeof setInterval> | undefined;
let requestGeneration = 0;
let lastWebRTCAttempt = 0;
let retryingWebRTC = false;
let authGeneration = 0;
let suppressQueryWatch = false;

const onShortcut = (event: KeyboardEvent) => {
  if (event.key === "Escape" && previewResource.value) {
    closeResourcePreview();
    return;
  }
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
    document.querySelector<HTMLInputElement>("#resource-search")?.focus();
  }
};

const onTagEditorOutside = (event: MouseEvent) => {
  if (!editingTagID.value) return;
  const target = event.target;
  if (target instanceof Element && target.closest(".tag-editor-wrap")) return;
  closeTagEditor();
};

const onTagFilterOutside = (event: MouseEvent) => {
  if (tagFilterOpen.value && event.target instanceof Element && !event.target.closest(".tag-filter")) {
    tagFilterOpen.value = false;
  }
};

function currentFilters(): CollectionFilters {
  return {
    query: query.value.trim(), tags: [...selectedTags.value], kind: selectedKind.value,
    rootId: selectedRootID.value, unorganized: unorganizedOnly.value, sort: sortOrder.value,
  };
}

function sameFilters(left: CollectionFilters, right: CollectionFilters) {
  return left.query === right.query && left.kind === right.kind && left.rootId === right.rootId &&
    left.unorganized === right.unorganized && left.sort === right.sort &&
    left.tags.length === right.tags.length && left.tags.every((tag) => right.tags.includes(tag));
}

const selectedCollection = computed(() => collections.value.find((item) => item.id === selectedCollectionID.value));
const activeCollection = computed(() => selectedCollection.value && sameFilters(selectedCollection.value.filters, currentFilters()) ? selectedCollection.value : undefined);
const pageTitle = computed(() => {
  if (activeCollection.value) return activeCollection.value.name;
  if (selectedTags.value.length > 1) return `标签筛选（${selectedTags.value.length}）`;
  if (selectedTags.value.length === 1) return `标签：${selectedTags.value[0]}`;
  if (unorganizedOnly.value) return "待整理";
  return query.value.trim() || selectedKind.value || selectedRootID.value ? "筛选结果" : "全部资源";
});
const selectedCount = computed(() => selectedIDs.value.size);
const selectedMediaCount = computed(() => resources.value.filter((resource) => selectedIDs.value.has(resource.id) && resource.available && ["image", "video"].includes(resource.kind)).length);
const totalIndexed = computed(() => roots.value.reduce((sum, root) => sum + root.count, 0));
const hasMore = computed(() => resources.value.length < total.value);
const isFiltered = computed(() => Boolean(query.value.trim() || selectedTags.value.length || unorganizedOnly.value || selectedKind.value || selectedRootID.value || sortOrder.value !== "modified_desc"));
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

async function generateThumbnail(id: string, showFailure = true): Promise<boolean> {
  if (generatingThumbnailIDs.value.has(id)) return false;
  generatingThumbnailIDs.value = new Set(generatingThumbnailIDs.value).add(id);
  try {
    await createThumbnail(id);
    const revisions = new Map(thumbnailRefreshTokens.value);
    revisions.set(id, (revisions.get(id) || 0) + 1);
    thumbnailRefreshTokens.value = revisions;
    const failed = new Set(failedThumbnailIDs.value);
    failed.delete(id);
    failedThumbnailIDs.value = failed;
    const urls = new Map(thumbnailURLs.value);
    const previousURL = urls.get(id);
    if (previousURL) URL.revokeObjectURL(previousURL);
    urls.delete(id);
    thumbnailURLs.value = urls;
    loadingThumbnailIDs.delete(id);
    const resource = resources.value.find((item) => item.id === id);
    if (resource) await loadThumbnail(resource);
    return true;
  } catch (error) {
    if (showFailure) notify(error instanceof Error ? error.message : "缩略图生成失败");
    return false;
  } finally {
    const generating = new Set(generatingThumbnailIDs.value);
    generating.delete(id);
    generatingThumbnailIDs.value = generating;
  }
}

async function generateSelectedThumbnails() {
  if (generatingThumbnailBatch.value) return;
  const selected = resources.value.filter((resource) => selectedIDs.value.has(resource.id) && resource.available && ["image", "video"].includes(resource.kind));
  if (!selected.length) return;
  generatingThumbnailBatch.value = true;
  thumbnailBatchProgress.value = { done: 0, total: selected.length };
  let nextIndex = 0;
  let succeeded = 0;
  let failed = 0;
  const worker = async () => {
    while (nextIndex < selected.length) {
      const resource = selected[nextIndex++];
      if (await generateThumbnail(resource.id, false)) succeeded++;
      else failed++;
      thumbnailBatchProgress.value = { ...thumbnailBatchProgress.value, done: thumbnailBatchProgress.value.done + 1 };
    }
  };
  try {
    await Promise.all([worker(), worker()]);
    notify(failed ? `完成 ${succeeded} 个，${failed} 个失败` : `已生成 ${succeeded} 个缩略图`);
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
    mp4: "video/mp4", m4v: "video/mp4", mov: "video/quicktime", webm: "video/webm", ogv: "video/ogg",
    mp3: "audio/mpeg", m4a: "audio/mp4", aac: "audio/aac", oga: "audio/ogg", ogg: "audio/ogg", wav: "audio/wav", flac: "audio/flac",
    pdf: "application/pdf", txt: "text/plain",
  };
  const type = types[extension] || "";
  return canPreviewInline(type) ? type : "";
}

function openResourcePreview(resource: Resource) {
  previewResource.value = resource;
  previewMimeType.value = getPreviewMimeType(resource);
  previewError.value = "";
}

function closeResourcePreview() {
  previewResource.value = null;
  previewMimeType.value = "";
  previewError.value = "";
}

function startRelayDownload(resource: Resource) {
  const link = document.createElement("a");
  link.href = fileURL(resource.id, true);
  link.download = resource.name;
  document.body.append(link);
  link.click();
  link.remove();
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
      tags: selectedTags.value,
      unorganized: unorganizedOnly.value,
      kind: selectedKind.value,
      rootId: selectedRootID.value,
      sort: sortOrder.value,
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
  const generation = authGeneration;
  try {
    const result = await getRoots();
    if (generation === authGeneration) roots.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadTags() {
  const generation = authGeneration;
  try {
    const result = await getTags();
    if (generation === authGeneration) tags.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadCollections() {
  const generation = authGeneration;
  try {
    const result = await getCollections();
    if (generation === authGeneration) collections.value = result;
  } catch (error) { if (generation === authGeneration) showError(error); }
}

async function loadWorkspace() {
  await Promise.all([loadRoots(), loadTags(), loadCollections(), loadResources()]);
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
  detailResourceID.value = new URLSearchParams(window.location.search).get("resource") || "";
}

function chooseAll() {
  if (detailResourceID.value) closeDetail();
  selectedCollectionID.value = "";
  selectedTags.value = [];
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
  selectedKind.value = "";
  selectedRootID.value = "";
  sortOrder.value = "modified_desc";
  query.value = "";
  unorganizedOnly.value = true;
  void loadResources();
}

function toggleTag(name: string) {
  if (detailResourceID.value) closeDetail();
  selectedTags.value = selectedTags.value.includes(name)
    ? selectedTags.value.filter((tag) => tag !== name)
    : [...selectedTags.value, name];
  unorganizedOnly.value = false;
  void loadResources();
}

function clearSelectedTags() {
  if (!selectedTags.value.length) return;
  selectedTags.value = [];
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
  document.querySelector(".tag-filter")?.scrollIntoView({ behavior: "smooth", block: "center" });
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
  selectedRootID.value = item.filters.rootId;
  unorganizedOnly.value = item.filters.unorganized;
  sortOrder.value = item.filters.sort;
  await nextTick();
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
    notify(`已保存智能集合「${item.name}」`);
  } catch (error) { showError(error); }
  finally { collectionBusy.value = false; }
}

async function removeCollection() {
  const id = editingCollectionID.value;
  if (!id || collectionBusy.value || !window.confirm(`删除智能集合「${collectionName.value}」？`)) return;
  collectionBusy.value = true;
  try {
    await deleteCollection(id);
    if (selectedCollectionID.value === id) selectedCollectionID.value = "";
    await loadCollections();
    closeCollectionEditor();
    notify("智能集合已删除");
  } catch (error) { showError(error); }
  finally { collectionBusy.value = false; }
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
  const generation = authGeneration;
  loadingBatchSuggestions.value = true;
  try {
    const result = await getBatchTagSuggestions();
    if (generation !== authGeneration) return;
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

function applyTransportState(state: "connecting" | "webrtc" | "turn" | "relay") {
  nodeTransport.value = state;
  if (state === "relay") void refreshNodeStatus();
}

async function establishWebRTC(iceServers: RTCIceServer[] = []) {
  lastWebRTCAttempt = Date.now();
  await connectWebRTC(iceServers, applyTransportState, (rate) => { nodeTraffic.value = rate; });
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
    nodeOnline.value = session.nodeOnline;
    webTraffic.value = session.webTraffic ?? webTraffic.value;
    password.value = "";
    if (session.nodeOnline) await establishWebRTC(session.iceServers);
    else nodeTransport.value = "relay";
    await loadWorkspace();
    startStatusPolling();
  } catch (error) { loginError.value = error instanceof Error ? error.message : "登录失败"; }
  finally { loginBusy.value = false; }
}

function clearRemoteSession() {
  authGeneration++;
  requestGeneration++;
  authenticated.value = false;
  nodeOnline.value = false;
  password.value = "";
  resources.value = [];
  roots.value = [];
  tags.value = [];
  collections.value = [];
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
  selectedIDs.value = new Set();
  selectedTags.value = [];
  tagFilterOpen.value = false;
  closeResourcePreview();
  detailResourceID.value = "";
  editingTagID.value = "";
  errorMessage.value = "";
  toastMessage.value = "";
  stopStatusPolling();
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

async function refreshNodeStatus() {
  try {
    const session = await getSession();
    if (session) {
      if (!session.authenticated) {
        clearRemoteSession();
      }
      nodeOnline.value = session.nodeOnline;
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
  window.addEventListener("keydown", onShortcut);
  window.addEventListener("popstate", syncDetailRoute);
  document.addEventListener("click", onTagEditorOutside, true);
  document.addEventListener("click", onTagFilterOutside, true);
  try {
    const session = await getSession();
    if (!session) {
      remoteMode.value = false;
      authenticated.value = true;
    } else {
      remoteMode.value = true;
      authenticated.value = session.authenticated;
      nodeOnline.value = session.nodeOnline;
      webTraffic.value = session.webTraffic ?? webTraffic.value;
      nodeTraffic.value = session.nodeTraffic ?? nodeTraffic.value;
    }
    if (authenticated.value) {
      if (session?.nodeOnline) await establishWebRTC(session.iceServers);
      await loadWorkspace();
      startStatusPolling();
    }
  } catch (error) { showError(error); }
  finally { initialized.value = true; }

});

onBeforeUnmount(() => {
  window.removeEventListener("myliba:unauthorized", handleUnauthorized);
  window.removeEventListener("keydown", onShortcut);
  window.removeEventListener("popstate", syncDetailRoute);
  document.removeEventListener("click", onTagEditorOutside, true);
  document.removeEventListener("click", onTagFilterOutside, true);
  stopStatusPolling();
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
        <button class="nav-link" :class="{ active: !isFiltered && !activeCollection }" @click="chooseAll">
          <House :size="17" /><span>全部资源</span><span class="nav-count">{{ totalIndexed }}</span>
        </button>
        <button class="nav-link" :class="{ active: unorganizedOnly && !activeCollection }" @click="chooseUnorganized">
          <Clock3 :size="17" /><span>待整理</span>
        </button>
      </nav>

      <div class="sidebar-section-label collection-section-title">
        <span>智能集合</span><button class="sidebar-add" type="button" title="保存当前筛选为集合" aria-label="新建智能集合" @click="openCollectionEditor()"><Plus :size="14" /></button>
      </div>
      <nav class="collection-nav" aria-label="智能集合">
        <div v-for="item in collections" :key="item.id" class="collection-nav-row" :class="{ active: activeCollection?.id === item.id }">
          <button type="button" class="collection-nav-open" @click="chooseCollection(item)"><Bookmark :size="14" /><span>{{ item.name }}</span></button>
          <button type="button" class="collection-nav-edit" :aria-label="`编辑集合 ${item.name}`" @click="openCollectionEditor(item, true)"><Pencil :size="13" /></button>
        </div>
        <p v-if="!collections.length" class="sidebar-empty">保存常用筛选，快速回到这里。</p>
      </nav>

      <div class="sidebar-section-label tag-section-title">
        <span>标签</span><span class="label-count">{{ tags.length }}</span>
      </div>
      <nav class="tag-nav" aria-label="按标签筛选">
        <button v-for="tag in tags" :key="tag.id" class="tag-nav-link" :class="{ active: selectedTags.includes(tag.name) }" :aria-pressed="selectedTags.includes(tag.name)" @click="toggleTag(tag.name)">
          <Tag :size="15" /><span class="tag-nav-name">{{ tag.name }}</span><span class="tag-count">{{ tag.count }}</span><Check v-if="selectedTags.includes(tag.name)" :size="13" />
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
        <div class="breadcrumbs"><span>我的资源</span><span class="breadcrumb-divider">/</span><strong>{{ detailResourceID ? "资源详情" : pageTitle }}</strong></div>
        <div class="topbar-actions">
          <template v-if="remoteMode">
            <span class="connection-badge traffic-badge" title="浏览器到服务端的 HTTPS 应用层流量；↓ 服务端发往网页，↑ 网页发往服务端。">
              <span class="connection-dot"></span><span>Web · HTTPS 接入</span><small>↓ {{ formatRate(webTraffic.downloadBps) }}　↑ {{ formatRate(webTraffic.uploadBps) }}</small>
            </span>
            <span class="connection-badge traffic-badge" :class="{ offline: !nodeOnline }" :title="nodeTransport === 'webrtc' ? 'WebRTC DataChannel 直连；↓ 节点发往网页，↑ 网页发往节点。' : nodeTransport === 'turn' ? 'WebRTC DataChannel 通过 TURN 中继；↓ 节点发往网页，↑ 网页发往节点。' : nodeTransport === 'connecting' ? '正在协商 WebRTC；直连建立前 API 经 WSS 服务端转发。' : 'WebRTC 不可用，API 经 WSS 服务端转发；↓ 节点发往网页，↑ 网页发往节点。'">
              <span class="connection-dot"></span><span>{{ nodeTransport === 'webrtc' ? '节点 · WebRTC 直连' : nodeTransport === 'turn' ? '节点 · WebRTC / TURN' : nodeTransport === 'connecting' ? '节点 · WebRTC 连接中' : '节点 · WSS 转发' }}</span><small>↓ {{ formatRate(nodeTraffic.downloadBps) }}　↑ {{ formatRate(nodeTraffic.uploadBps) }}</small>
            </span>
          </template>
          <span v-else class="connection-badge"><span class="connection-dot"></span>本机模式</span>
          <button v-if="remoteMode && authenticated" class="quiet-button signout-button" title="退出登录" @click="signOut"><LogOut :size="15" /><span>退出</span></button>
        </div>
      </header>

      <main class="page-content">
        <ResourceDetail v-if="detailResourceID && authenticated" :id="detailResourceID" @back="closeDetail" @tags-changed="loadTags(); loadResources()" />
        <template v-else>
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

        <div v-if="collections.length" class="mobile-collections" aria-label="智能集合">
          <span>智能集合</span>
          <button v-for="item in collections" :key="item.id" type="button" :class="{ active: activeCollection?.id === item.id }" @click="chooseCollection(item)"><Bookmark :size="13" />{{ item.name }}</button>
        </div>

        <section class="collection-toolbar">
          <div class="collection-heading">
            <span class="collection-symbol"><Bookmark v-if="activeCollection" :size="17" /><LayoutGrid v-else :size="17" /></span>
            <div><h2>{{ pageTitle }}</h2><p>{{ activeCollection ? "按保存的条件动态更新" : selectedTags.length ? "同时包含所选标签的资源" : unorganizedOnly ? "还没有添加标签的资源" : "搜索和筛选你的资源" }}</p></div>
          </div>
          <div class="collection-actions">
            <label class="search-field" for="resource-search">
              <Search :size="17" />
              <input id="resource-search" v-model="query" type="search" placeholder="搜索文件名、路径或标签" autocomplete="off" />
              <kbd>⌘ K</kbd>
              <button v-if="query" class="search-clear" type="button" aria-label="清除搜索" @click="query = ''"><X :size="14" /></button>
            </label>
            <div class="tag-filter">
              <button class="button button-secondary tag-filter-toggle" type="button" :class="{ selected: selectedTags.length > 0 }" :aria-expanded="tagFilterOpen" aria-controls="tag-filter-options" @click="toggleTagFilter"><Tag :size="15" /><span>{{ selectedTags.length ? `已选 ${selectedTags.length} 个标签` : "按标签筛选" }}</span><ChevronDown :size="14" /></button>
              <div v-if="tagFilterOpen" id="tag-filter-options" class="tag-filter-popover">
                <label class="tag-filter-search" for="tag-filter-search"><Search :size="14" /><input id="tag-filter-search" v-model="tagFilterQuery" type="search" placeholder="查找标签" autocomplete="off" /></label>
                <p class="tag-filter-hint">可多选 · 结果同时包含所选标签</p>
                <div class="tag-filter-list">
                  <button v-for="tag in matchingTags" :key="tag.id" class="tag-filter-option" type="button" :class="{ active: selectedTags.includes(tag.name) }" :aria-pressed="selectedTags.includes(tag.name)" @click="toggleTag(tag.name)"><span>{{ tag.name }}</span><small>{{ tag.count }}</small><Check v-if="selectedTags.includes(tag.name)" :size="14" /></button>
                  <p v-if="!matchingTags.length" class="tag-filter-empty">{{ tags.length ? "没有匹配的标签" : "还没有标签" }}</p>
                </div>
                <button v-if="selectedTags.length" class="tag-filter-clear" type="button" @click="clearSelectedTags">清除已选标签</button>
              </div>
            </div>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'grid' }" title="网格视图" @click="listMode = 'grid'"><LayoutGrid :size="16" /></button>
            <button class="button button-secondary display-mode-button" :class="{ selected: listMode === 'list' }" title="列表视图" @click="listMode = 'list'"><List :size="16" /></button>
          </div>
        </section>

        <div class="resource-filter-controls" aria-label="更多筛选和排序">
          <label>类型<select v-model="selectedKind" @change="loadResources()"><option value="">全部类型</option><option value="image">图片</option><option value="video">视频</option><option value="audio">音频</option><option value="document">文档</option><option value="file">其他文件</option></select></label>
          <label>来源<select v-model="selectedRootID" @change="loadResources()"><option value="">全部目录</option><option v-for="root in roots" :key="root.id" :value="root.id">{{ root.path }}</option></select></label>
          <label>排序<select v-model="sortOrder" @change="loadResources()"><option value="modified_desc">最近修改</option><option value="modified_asc">最早修改</option><option value="name_asc">名称 A-Z</option><option value="name_desc">名称 Z-A</option><option value="size_desc">文件最大</option></select></label>
        </div>

        <form v-if="collectionEditorOpen" class="collection-editor" @submit.prevent="saveCollection">
          <div><strong>{{ editingCollectionID ? "编辑智能集合" : "保存当前筛选" }}</strong><p>集合记录当前搜索、标签、类型、目录和排序条件，资源会随索引变化自动更新。</p></div>
          <label class="sr-only" for="collection-name">集合名称</label>
          <input id="collection-name" v-model="collectionName" maxlength="80" placeholder="给集合起个名字" autocomplete="off" />
          <button class="button button-primary" type="submit" :disabled="collectionBusy || !collectionName.trim()">{{ editingCollectionID ? "保存更改" : "创建集合" }}</button>
          <button class="button button-secondary" type="button" @click="closeCollectionEditor">取消</button>
          <button v-if="editingCollectionID" class="quiet-button collection-delete" type="button" :disabled="collectionBusy" @click="removeCollection"><Trash2 :size="14" />删除</button>
        </form>

        <div v-if="selectedTags.length" class="selected-tag-filters" aria-label="已选标签">
          <span>同时包含</span>
          <button v-for="name in selectedTags" :key="name" type="button" :aria-label="`移除标签 ${name}`" @click="toggleTag(name)"><Tag :size="12" />{{ name }}<X :size="12" /></button>
        </div>

        <div class="results-toolbar">
          <div class="results-count"><span>{{ isFiltered ? "筛选结果" : "所有资源" }}</span><strong>{{ total.toLocaleString("zh-CN") }}</strong></div>
          <div class="results-toolbar-actions">
            <button class="quiet-button batch-suggestion-toggle" :aria-expanded="batchSuggestionsOpen" title="按目录和文件名特征分析整个资源库" @click="toggleBatchSuggestions"><Sparkles :size="14" />批量建议</button>
            <button class="quiet-button save-collection-toggle" @click="openCollectionEditor()"><BookmarkPlus :size="14" />保存集合</button>
            <button v-if="selectedCollection" class="quiet-button save-collection-toggle" @click="openCollectionEditor(selectedCollection)"><Pencil :size="14" />{{ activeCollection ? "编辑集合" : "更新集合" }}</button>
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
          <button v-if="selectedMediaCount" class="button button-secondary thumbnail-batch-button" :disabled="generatingThumbnailBatch" @click="generateSelectedThumbnails"><LoaderCircle v-if="generatingThumbnailBatch" class="spin" :size="14" /><Image v-else :size="14" />{{ generatingThumbnailBatch ? `正在生成 ${thumbnailBatchProgress.done}/${thumbnailBatchProgress.total}` : `生成缩略图（${selectedMediaCount}）` }}</button>
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
            <div class="resource-preview" @click="openDetail(resource)">
              <img v-if="thumbnailURLs.has(resource.id) && !failedThumbnailIDs.has(resource.id)" loading="lazy" :src="thumbnailURLs.get(resource.id)" :alt="resource.name" @error="markThumbnailFailed(resource.id)" />
              <div v-if="!resource.available || !['image', 'video'].includes(resource.kind) || failedThumbnailIDs.has(resource.id) || !thumbnailURLs.has(resource.id)" class="preview-placeholder"><component :is="kindIcon(resource.kind)" :size="29" :stroke-width="1.5" /></div>
              <button v-if="resource.available && ['image', 'video'].includes(resource.kind) && failedThumbnailIDs.has(resource.id)" class="thumbnail-generate-button" :disabled="isGeneratingThumbnail(resource.id)" @click.stop="generateThumbnail(resource.id)"><LoaderCircle v-if="isGeneratingThumbnail(resource.id)" class="spin" :size="13" />{{ isGeneratingThumbnail(resource.id) ? '正在生成' : '生成缩略图' }}</button>
              <span v-if="resource.available && resource.kind === 'video' && thumbnailURLs.has(resource.id) && !failedThumbnailIDs.has(resource.id)" class="video-preview-play" aria-hidden="true"><Play :size="17" fill="currentColor" /></span>
              <span class="type-pill">{{ kindLabel(resource.kind) }}</span>
              <span v-if="!resource.available" class="unavailable-pill">来源暂不可用</span>
            </div>
            <div class="resource-info">
              <p class="resource-name" :title="resource.path">{{ resource.name }}</p>
              <div class="resource-meta"><span>{{ formatBytes(resource.size) }}</span><span class="meta-dot"></span><span>{{ formatDate(resource.modified) }}</span></div>
              <div v-if="resource.available" class="resource-file-actions">
                <button class="resource-file-action" type="button" @click="openResourceFile(resource, false)"><ExternalLink :size="13" />详情</button>
                <button class="resource-file-action" type="button" @click="openResourceFile(resource, true)"><Download :size="13" />下载</button>
              </div>
              <p v-else class="resource-unavailable-note">源文件当前不可用</p>
              <div class="resource-tags">
                <button v-for="tagName in resource.tags" :key="tagName" class="tag-chip" :aria-pressed="selectedTags.includes(tagName)" @click="toggleTag(tagName)"><Tag :size="11" />{{ tagName }}</button>
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
        </template>
      </main>
    </div>

    <nav class="mobile-nav" aria-label="资源导航">
      <button :class="{ active: !isFiltered && !activeCollection }" @click="chooseAll"><House :size="17" /><span>全部</span></button>
      <button :class="{ active: unorganizedOnly && !activeCollection }" @click="chooseUnorganized"><Clock3 :size="17" /><span>待整理</span></button>
      <button :class="{ active: selectedTags.length > 0 }" @click="openTagFilterFromMobile"><Tag :size="17" /><span>标签{{ selectedTags.length ? ` ${selectedTags.length}` : "" }}</span></button>
    </nav>

    <div v-if="previewResource" class="resource-preview-backdrop" @click.self="closeResourcePreview">
      <section class="resource-preview-dialog" role="dialog" aria-modal="true" :aria-label="'预览 ' + previewResource.name">
        <header class="resource-preview-header">
          <div class="resource-preview-heading">
            <h2>{{ previewResource.name }}</h2>
            <p>{{ formatBytes(previewResource.size) }} · {{ kindLabel(previewResource.kind) }}</p>
          </div>
          <button class="resource-preview-download" type="button" @click="openResourceFile(previewResource, true)"><Download :size="14" />下载</button>
          <button class="quiet-button directory-close" aria-label="关闭预览" @click="closeResourcePreview"><X :size="18" /></button>
        </header>
        <div class="resource-preview-stage">
          <template v-if="previewMimeType && !previewError">
            <img v-if="previewMimeType.startsWith('image/')" :src="fileURL(previewResource.id)" :alt="previewResource.name" @error="previewError = '无法加载此图片。请检查文件是否仍可访问。'" />
            <video v-else-if="previewMimeType.startsWith('video/')" :src="fileURL(previewResource.id)" controls preload="metadata" @error="previewError = '浏览器无法播放此视频格式。你可以下载后打开。'"></video>
            <audio v-else-if="previewMimeType.startsWith('audio/')" :src="fileURL(previewResource.id)" controls preload="metadata" @error="previewError = '浏览器无法播放此音频格式。你可以下载后打开。'"></audio>
            <iframe v-else :src="fileURL(previewResource.id)" :title="'预览 ' + previewResource.name"></iframe>
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
