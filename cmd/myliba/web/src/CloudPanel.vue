<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { Cloud, Download, ExternalLink, LoaderCircle, Play, RefreshCw, Upload, X } from "@lucide/vue";
import StreamingVideo from "./StreamingVideo.vue";
import { lockedOfflineIndex, offlineIndexStatus, saveOfflineIndex, searchOfflineIndex, unlockOfflineIndex } from "./libraryOffline";
import {
  ApiError, addLibraryFileTag, createLibrarySavedQuery, deleteLibrarySavedQuery, diagnoseCloudPeer, executeLibraryCleanup, getCloudFile, getCloudRecovery, getCloudRecoveryStatus, getCloudTransfers, getCloudVersions, getFileNameConflicts, getFolderConflicts, getLegacyFolders, getLibraryCleanupPlan, getLibraryFileDetail, getLibraryMembers, getLibraryReadCandidate, getLibrarySavedQueries, getLibrarySyncStatus, getRenameConflicts, getResource, getResourceFile, getRevisionConflicts, importCloudRecovery, mergeLegacyFolders, moveLibraryFile, rejoinLibraryMember, releaseCheck, releaseOriginal, removeLibraryFileTag, renameLibraryFile, resolveFolderConflict, resolveLibraryFile, resolveRenameConflict, retireLibraryMember, searchLibraryFiles, selectLibraryCurrentRevision, selectResourceNode, selfAttestCloudRecovery,
  setLibraryFileLifecycle, speedTestCloudPeer, uploadResourceToCloud, type CloudFile, type CloudPeerDiagnosis, type CloudRecoveryStatus, type CloudSpeedTest, type CloudTransferTask, type CloudView, type FileNameConflict, type FolderConflict, type LegacyFolderGroup, type LibraryCleanupPlan, type LibraryFileDetail, type LibraryMember, type LibrarySavedQuery, type LibrarySyncStatus, type ReleaseCheck, type ReleaseLocation, type RenameConflict, type Resource, type RevisionConflict,
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
const activePreview = ref<{ fileID: string; url: string; mime: string; kind: "image" | "audio" | "document" | "video" } | null>(null);
const historyChoices = ref<Record<string, boolean>>({});
const versions = ref<Record<string, CloudFile[]>>({});
const diagnosingPeer = ref("");
const testingPeer = ref("");
const selectedSpeedPath = ref<Record<string, CloudSpeedTest["path"]>>({});
const diagnoses = ref<Record<string, CloudPeerDiagnosis>>({});
const speedResults = ref<Record<string, CloudSpeedTest>>({});
const syncOpen = ref(false);
const syncLoading = ref(false);
const syncStatus = ref<LibrarySyncStatus | null>(null);
const cleanupPlan = ref<LibraryCleanupPlan | null>(null);
const cleanupBusy = ref(false);
const folderConflicts = ref<FolderConflict[]>([]);
const renameConflicts = ref<RenameConflict[]>([]);
const fileNameConflicts = ref<FileNameConflict[]>([]);
const revisionConflicts = ref<RevisionConflict[]>([]);
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
const legacyFoldersOpen = ref(false);
const legacyFolders = ref<LegacyFolderGroup[]>([]);
const selectedLegacyFolders = ref<string[]>([]);
const membersOpen = ref(false);
const members = ref<LibraryMember[]>([]);
const offlineMode = ref(false);
const offlineStatus = ref<{ nodeId: string; libraryId: string; syncedAt: string; count: number } | null>(null);
const detail = ref<LibraryFileDetail | null>(null);
const detailLoading = ref("");
const detailEditing = ref(false);
const transfersOpen = ref(false);
const transfersLoading = ref(false);
const transfers = ref<CloudTransferTask[]>([]);
const recoveryStatus = ref<CloudRecoveryStatus | null>(null);
const recoveryBusy = ref(false);
const releaseChecks = ref<Record<string, ReleaseCheck>>({});
const releaseBusy = ref("");
let timer: ReturnType<typeof setInterval> | undefined;

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
    for (const file of files) {
      if (file.sourceId && historyChoices.value[file.sourceId] === undefined) historyChoices.value[file.sourceId] = file.history;
    }
    error.value = "";
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取云层"; if (rethrow) throw cause; }
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
async function loadLegacyFolders() {
  legacyFoldersOpen.value = true;
  try { legacyFolders.value = (await getLegacyFolders()).items; error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取旧文件夹"; }
}
async function loadMembers() {
  membersOpen.value = true;
  try { members.value = (await getLibraryMembers()).items; error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取成员"; }
}
async function retireMember(nodeId: string) {
  if (!window.confirm(`退役成员 ${nodeId}？其旧作者 epoch 不再计入回收门槛。`)) return;
  try { await retireLibraryMember(nodeId); await loadMembers(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法退役成员"; }
}
async function rejoinMember() {
  try { await rejoinLibraryMember(); await loadMembers(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法重新加入"; }
}
async function mergeSelectedLegacyFolders() {
  const selected = legacyFolders.value.filter(folder => selectedLegacyFolders.value.includes(folder.id));
  if (selected.length < 2) return;
  const name = window.prompt("合并后的文件夹名称", selected[0].name)?.trim();
  if (!name) return;
  try {
    await mergeLegacyFolders(selected.map(folder => folder.id), name);
    selectedLegacyFolders.value = [];
    await Promise.all([loadLegacyFolders(), refresh()]);
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法合并旧文件夹"; }
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
  void loadRecoveryStatus();
  if (transfersOpen.value) void loadTransfers();
}
defineExpose({ open: show });
async function showSync() {
  syncOpen.value = true;
  syncLoading.value = true;
  try {
    const [status, conflicts, renames, fileNames, revisions] = await Promise.all([getLibrarySyncStatus(), getFolderConflicts(), getRenameConflicts(), getFileNameConflicts(), getRevisionConflicts()]);
    syncStatus.value = status;
    folderConflicts.value = conflicts.items;
    renameConflicts.value = renames.items;
    fileNameConflicts.value = fileNames.items;
    revisionConflicts.value = revisions.items;
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取同步状态"; }
  finally { syncLoading.value = false; }
}
async function inspectCleanup() {
  cleanupBusy.value = true;
  try { cleanupPlan.value = await getLibraryCleanupPlan(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法计算清理计划"; }
  finally { cleanupBusy.value = false; }
}
async function runCleanup() {
  const plan = cleanupPlan.value;
  if (!plan?.planId || plan.blocked.length) return;
  if (!window.confirm(`执行当前计划，清理 ${plan.eligible.filter(item => item.kind === "catalog").length} 条已压缩目录记录、${plan.eligible.filter(item => item.kind === "content").length} 条已删除文件的内容记录和 ${plan.eligible.filter(item => item.kind === "block").length} 个无引用云块？存储节点将在 Hub 写入保护下同步清理，来源原件不会删除。`)) return;
  cleanupBusy.value = true;
  try {
    const result = await executeLibraryCleanup(plan.planId);
    cleanupPlan.value = null;
    const storageRemoved = result.storageNodes.reduce((total, node) => total + (node.removedBlocks?.length ?? 0), 0);
    const storageFailed = result.storageNodes.filter(node => node.error || node.blocked?.length).length;
    error.value = `已清理 ${result.removedCatalogRecords.length} 条目录记录、${result.removedContentRecords.length} 条内容记录、${result.removedBlocks.length} 个本机云块、${storageRemoved} 个存储节点云块，压缩 ${result.compactedTombstones} 个文件夹墓碑` + (storageFailed ? `；${storageFailed} 个存储节点未完成，可稍后重试` : "");
    await showSync();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "清理未完成，请重新查看计划"; }
  finally { cleanupBusy.value = false; }
}
async function resolveConflict(conflict: FolderConflict) {
  const nextFolder = window.prompt(`为文件 ${conflict.fileId} 选择最终文件夹名称；留空表示未归组。`, conflict.folderName);
  if (nextFolder === null) return;
  syncLoading.value = true;
  try {
    await resolveFolderConflict(conflict.fileId, nextFolder.trim());
    await showSync();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法解决文件夹冲突"; }
  finally { syncLoading.value = false; }
}
async function resolveRename(conflict: RenameConflict) {
  const nextName = window.prompt(`原名“${conflict.anchor}”当前显示为“${conflict.appliedName}”。选择最终名称：`, conflict.folderName);
  if (nextName === null) return;
  const trimmed = nextName.trim();
  if (!trimmed) { error.value = "改名结果不能为空"; return; }
  syncLoading.value = true;
  try {
    await resolveRenameConflict(conflict.anchor, trimmed);
    await showSync();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法解决改名冲突"; }
  finally { syncLoading.value = false; }
}
async function resolveFileName(conflict: FileNameConflict) {
  const name = window.prompt(`文件 ${conflict.fileId} 当前显示为“${conflict.appliedName}”。选择最终显示名称：`, conflict.fileName);
  if (name === null || !name.trim()) return;
  syncLoading.value = true;
  try { await renameLibraryFile(conflict.fileId, name.trim(), true); await showSync(); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法解决文件改名冲突"; }
  finally { syncLoading.value = false; }
}
async function chooseCurrentRevision(fileID: string, revisionID: string, resolve = false) {
  if (detailEditing.value) return;
  detailEditing.value = true;
  try {
    await selectLibraryCurrentRevision(fileID, revisionID, resolve);
    if (detail.value?.fileId === fileID) detail.value = await getLibraryFileDetail(fileID);
    await refresh();
    if (resolve) await showSync();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法选择当前版本"; }
  finally { detailEditing.value = false; }
}
async function openDetail(file: CloudFile) {
  const id = file.fileId || file.id;
  detailLoading.value = id;
  releaseChecks.value = {};
  try { detail.value = await getLibraryFileDetail(id); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取文件详情"; }
  finally { detailLoading.value = ""; }
}
async function editDetail(action: "addTag" | "removeTag" | "folder" | "name", value?: string) {
  if (!detail.value || detailEditing.value) return;
  const fileID = detail.value.fileId;
  let input = value;
  if (input === undefined) input = window.prompt(action === "addTag" ? "添加标签" : action === "name" ? "文件显示名称" : "文件夹名称（留空为未归组）", action === "folder" ? detail.value.folderName : action === "name" ? detail.value.name : "") ?? undefined;
  if (input === undefined) return;
  input = input.trim();
  if (action !== "folder" && !input) return;
  detailEditing.value = true;
  try {
    if (action === "addTag") await addLibraryFileTag(fileID, input);
    else if (action === "removeTag") await removeLibraryFileTag(fileID, input);
    else if (action === "name") await renameLibraryFile(fileID, input);
    else await moveLibraryFile(fileID, input);
    detail.value = await getLibraryFileDetail(fileID);
    await refresh();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法修改分类"; }
  finally { detailEditing.value = false; }
}
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
function contentStatusLabel(status: LibraryFileDetail["contentStatus"]) {
  switch (status) {
    case "readable": return "可读取";
    case "cloudMissing": return "云层副本缺失，需可达来源设备";
    case "sourceOffline": return "来源设备离线";
    default: return "当前不可用";
  }
}
async function upload(resource: Resource) {
  try { await uploadResourceToCloud(resource.id, historyChoices.value[resource.id] ?? false); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法上传文件"; }
}
function cloudMembership(resource: Resource) {
  return view.value.files.find((file) => !file.localOnly && (file.id === resource.id || file.sourceId === resource.id));
}
function readableLocation(file: CloudFile) {
  return file.locations?.find((location) => location.nodeId && location.resourceRef);
}
async function openDeviceFile(file: CloudFile) {
  const locations = file.locations?.filter((location) => location.nodeId && location.resourceRef) ?? [];
  if (!locations.length) return;
  if (!props.remote) {
    error.value = "请从 Hub 资源库打开此文件；来源设备路径不会暴露给本机索引。";
    return;
  }
  activeDownload.value = file.id;
  let failure = "没有可读取的来源";
  for (const location of locations) {
    try {
      await selectResourceNode(location.nodeId);
      const resource = await getResource(location.resourceRef!);
      if (!resource.available) throw new Error("来源暂不可用");
      if (file.revisionId && resource.versionId !== file.revisionId) throw new Error("来源版本与检索结果不一致");
      try { window.localStorage.setItem("myliba:selected-resource-node", location.nodeId); } catch { /* Session state remains authoritative. */ }
      const target = new URL(window.location.href);
      target.searchParams.delete("folder");
      target.searchParams.set("resource", location.resourceRef!);
      window.location.replace(target.href);
      return;
    } catch (cause) {
      failure = cause instanceof Error ? cause.message : "无法连接来源设备";
    }
  }
  error.value = `无法打开来源：${failure}`;
  activeDownload.value = "";
}
async function download(id: string, name: string, revisionId?: string) {
  if (activeDownload.value) return;
  activeDownload.value = id;
  const queryNode = view.value.nodeId;
  let switched = false;
  try {
    let data: Blob | undefined;
    const file = view.value.files.find((item) => item.id === id);
    const wantedRevision = revisionId || file?.revisionId || "";
    const resolved = await resolveLibraryFile(file?.fileId || file?.id || id, wantedRevision);
    let failure = resolved.reason || "没有可读取的候选位置";
    for (const candidate of resolved.candidates) {
      try {
        if (candidate.kind === "cloud" && candidate.objectId) {
          if (switched && queryNode) { await selectResourceNode(queryNode); switched = false; }
          const response = await getLibraryReadCandidate(candidate);
          if (!response.ok) throw new Error(`云层下载失败 (HTTP ${response.status})`);
          data = await response.blob();
        } else if (candidate.kind === "device" && candidate.nodeId && candidate.resourceRef && (props.remote || candidate.nodeId === queryNode)) {
          if (props.remote && candidate.nodeId !== queryNode) { await selectResourceNode(candidate.nodeId); switched = true; }
          const source = await getResource(candidate.resourceRef);
          if (!source.available || source.versionId !== candidate.revisionId) throw new Error("来源版本与检索结果不一致");
          const response = await getLibraryReadCandidate(candidate);
          if (!response.ok) throw new Error(`来源下载失败 (HTTP ${response.status})`);
          const bytes = await response.blob();
          const after = await getResource(candidate.resourceRef);
          if (source.size !== bytes.size || !after.available || after.versionId !== source.versionId || after.size !== source.size) throw new Error("来源在下载期间发生变化，已丢弃此候选");
          data = bytes;
        }
        if (data) break;
      } catch (cause) {
        failure = cause instanceof Error ? cause.message : failure;
      }
    }
    if (!data) throw new Error(failure);
    const url = URL.createObjectURL(data);
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法下载文件"; }
  finally {
    if (switched && queryNode) {
      try { await selectResourceNode(queryNode); } catch { /* A later refresh will report query-node availability. */ }
    }
    activeDownload.value = "";
  }
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
      if (source.available && source.versionId === file.revisionId && source.modified === file.modified && (source.contentSize ?? source.size) === (file.originalSize ?? file.size)) {
        closePreview();
        activeVideo.value = "";
        open.value = false;
        emit("openResource", source.id);
        return;
      }
    } catch { /* The source may only exist in the cloud catalog. */ }
  }
  if (kind === "video") {
    if (activePreview.value?.fileID === file.id) { closePreview(); return; }
    if (await previewVideoClip(file)) { activeVideo.value = ""; return; }
    closePreview();
    activeVideo.value = activeVideo.value === file.id ? "" : file.id;
    return;
  }
  activeVideo.value = "";
  if (activePreview.value) URL.revokeObjectURL(activePreview.value.url);
  activePreview.value = null;
  activeDownload.value = file.id;
  const queryNode = view.value.nodeId;
  let selectedNode = queryNode;
  try {
    const resolved = await resolveLibraryFile(file.fileId || file.id, file.revisionId, "preview");
    let failure = resolved.reason || "没有可读取的预览候选";
    for (const candidate of resolved.candidates) {
      try {
        if (candidate.kind === "device") {
          if (!candidate.nodeId || !candidate.resourceRef || !props.remote && candidate.nodeId !== view.value.nodeId) continue;
          if (props.remote && candidate.nodeId !== selectedNode) { await selectResourceNode(candidate.nodeId); selectedNode = candidate.nodeId; }
          const source = await getResource(candidate.resourceRef);
          if (!source.available || source.versionId !== candidate.revisionId) throw new Error("来源版本已变化");
        } else if (selectedNode !== queryNode && queryNode) { await selectResourceNode(queryNode); selectedNode = queryNode; }
        const response = await getLibraryReadCandidate(candidate);
        if (!response.ok) throw new Error(`预览候选不可用 (HTTP ${response.status})`);
        const url = URL.createObjectURL(await response.blob());
        activePreview.value = { fileID: file.id, url, mime: response.headers.get("Content-Type") || "application/octet-stream", kind };
        break;
      } catch (cause) { failure = cause instanceof Error ? cause.message : failure; }
    }
    if (!activePreview.value) throw new Error(failure);
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法预览云层文件"; }
  finally { if (selectedNode !== queryNode && queryNode) { try { await selectResourceNode(queryNode); } catch { /* A later refresh reports the active node. */ } } activeDownload.value = ""; }
}
async function previewVideoClip(file: CloudFile): Promise<boolean> {
  const queryNode = view.value.nodeId;
  let selectedNode = queryNode;
  try {
    const resolved = await resolveLibraryFile(file.fileId || file.id, file.revisionId, "preview");
    for (const candidate of resolved.candidates) {
      if (candidate.assetType !== "videoPreview" || !candidate.readPath || !candidate.nodeId || !candidate.resourceRef) continue;
      if (!props.remote && candidate.nodeId !== queryNode) continue;
      try {
        if (props.remote && candidate.nodeId !== selectedNode) { await selectResourceNode(candidate.nodeId); selectedNode = candidate.nodeId; }
        const source = await getResource(candidate.resourceRef);
        if (!source.available || source.versionId !== candidate.revisionId) continue;
        const response = await getLibraryReadCandidate(candidate);
        if (!response.ok || !(response.headers.get("Content-Type") || "").startsWith("video/")) continue;
        closePreview();
        activePreview.value = { fileID: file.id, url: URL.createObjectURL(await response.blob()), mime: "video/mp4", kind: "video" };
        return true;
      } catch { /* Another candidate or the ordinary video stream may work. */ }
    }
  } catch { /* Fall back to the original video stream. */ }
  finally { if (selectedNode !== queryNode && queryNode) { try { await selectResourceNode(queryNode); } catch { /* Refresh reports node availability. */ } } }
  return false;
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
function transferStateLabel(state: string) {
  switch (state) {
    case "queued": return "待上传";
    case "reading": return "读取原件";
    case "uploading": return "传输中";
    case "verifying": return "读回验证";
    case "committed": return "安全保存";
    case "deletingLocal": return "移除本机位置";
    case "retryableFailed": return "失败（可重试）";
    case "blocked": return "已阻塞";
    case "cancelled": return "已取消";
    default: return state;
  }
}
async function loadTransfers() {
  transfersLoading.value = true;
  try { transfers.value = (await getCloudTransfers()).tasks; error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取传输任务"; }
  finally { transfersLoading.value = false; }
}
async function showTransfers() {
  transfersOpen.value = !transfersOpen.value;
  if (transfersOpen.value) await loadTransfers();
}
async function loadRecoveryStatus() {
  try { recoveryStatus.value = await getCloudRecoveryStatus(); }
  catch { recoveryStatus.value = null; }
}
async function attestRecovery() {
  recoveryBusy.value = true;
  try {
    await selfAttestCloudRecovery();
    await loadRecoveryStatus();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法证明本机可恢复"; }
  finally { recoveryBusy.value = false; }
}
async function checkRelease(location: ReleaseLocation) {
  if (!detail.value) return;
  releaseBusy.value = location.locationId;
  try {
    releaseChecks.value = { ...releaseChecks.value, [location.locationId]: await releaseCheck(detail.value.fileId, location.locationId, location.revisionId) };
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "释放检查失败"; }
  finally { releaseBusy.value = ""; }
}
async function doRelease(location: ReleaseLocation) {
  if (!detail.value) return;
  const fileID = detail.value.fileId;
  if (!window.confirm(`释放位置 ${location.nodeId || location.locationId} 的本机原件记录？请先确认该设备上的原件已安全删除（手机须先完成 SAF 删除）。释放只移除本机位置记录，保留分类与云层副本。`)) return;
  releaseBusy.value = location.locationId;
  try {
    const check = await releaseCheck(fileID, location.locationId, location.revisionId);
    releaseChecks.value = { ...releaseChecks.value, [location.locationId]: check };
    if (!check.eligible || !check.credential) { error.value = check.reason || "暂不满足安全释放条件"; return; }
    await releaseOriginal(fileID, location.locationId, check.credential);
    detail.value = await getLibraryFileDetail(fileID);
    await Promise.all([refresh(), loadTransfers()]);
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "释放未完成"; }
  finally { releaseBusy.value = ""; }
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
  <button v-if="!remote" class="button button-secondary" type="button" @click="showSync"><RefreshCw :size="15" />同步</button>
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
          <div class="cloud-heading"><strong>跨设备与云层文件</strong><button class="quiet-button" type="button" @click="refresh()"><RefreshCw :size="15" />刷新</button></div>
          <label class="cloud-file-search"><Cloud :size="15" /><input v-model="cloudQuery" type="search" placeholder="搜索文件名、文件夹或标签" aria-label="查找云层文件" @change="refresh()" @keyup.enter="refresh()" /></label>
          <label><input v-model="includeDevices" type="checkbox" @change="refresh()" />同时检索设备已同步的文件索引</label>
          <div class="cloud-search-filters">
            <label>文件状态<select v-model="lifecycle" @change="refresh()"><option value="active">资源库</option><option value="trashed">回收站</option></select></label>
            <label v-if="includeDevices">来源设备<select v-model="selectedDevice" @change="refresh()"><option value="">所有设备</option><option v-for="node in coverage.knownNodes || coverage.sources || []" :key="node" :value="node">{{ node }}{{ coverage.unindexedSources?.includes(node) ? '（无已索引位置）' : '' }}</option></select></label>
            <label>文件夹<select v-model="selectedFolder" @change="refresh()"><option value="">全部文件夹</option><option v-for="folder in searchFacets.folders" :key="folder.id" :value="folder.id">{{ folder.name }}（{{ folder.count }}）</option></select></label>
            <label>标签<select v-model="selectedTags" multiple size="4" aria-label="按标签筛选" @change="refresh()"><option v-for="tag in searchFacets.tags" :key="tag.name" :value="tag.name">{{ tag.name }}（{{ tag.count }}）</option></select></label>
            <label>类型<select v-model="selectedType" @change="refresh()"><option value="">全部类型</option><option value="image">图片</option><option value="video">视频</option><option value="audio">音频</option><option value="document">文档</option><option value="other">其他</option></select></label>
            <label>归组<select v-model="grouped" @change="refresh()"><option value="">全部</option><option value="true">已归组</option><option value="false">未归组</option></select></label>
            <label>云层副本<select v-model="cloudCopy" @change="refresh()"><option value="">全部</option><option value="true">有云层副本</option><option value="false">仅设备来源</option></select></label>
            <label>当前可读<select v-model="readable" @change="refresh()"><option value="">全部</option><option value="true">可读</option><option value="false">暂不可读</option></select></label>
            <label>排序<select v-model="sort" @change="refresh()"><option value="name_asc">名称 A–Z</option><option value="name_desc">名称 Z–A</option><option value="modified_desc">最近修改</option><option value="modified_asc">最早修改</option><option value="ingested_desc">最近入库</option><option value="ingested_asc">最早入库</option></select></label>
            <button v-if="cloudQuery || selectedFolder || selectedTags.length || selectedDevice || selectedType || grouped || cloudCopy || readable" class="quiet-button" type="button" @click="clearSearchFilters">清除筛选</button>
          </div>
          <div class="cloud-search-filters">
            <button class="button button-secondary" type="button" @click="loadSavedQueries">常用查询</button>
            <button class="button button-secondary" type="button" @click="saveCurrentQuery">保存当前条件</button>
            <button class="button button-secondary" type="button" @click="loadLegacyFolders">旧文件夹身份</button>
            <button class="button button-secondary" type="button" @click="loadMembers">资源库成员</button>
            <button class="button button-secondary" type="button" @click="showTransfers">传输任务</button>
            <button class="button button-secondary" type="button" :disabled="!view.nodeId" @click="enableOfflineIndex">开启并更新加密离线索引</button>
            <button class="button button-secondary" type="button" @click="unlockLocalIndex">解锁本地索引</button>
            <button v-if="offlineStatus" class="button button-secondary" type="button" @click="lockLocalIndex">锁定</button>
          </div>
          <p v-if="offlineStatus" class="hub-current">本地索引：{{ offlineStatus.count }} 条 · {{ offlineStatus.syncedAt }} · 查询节点 {{ offlineStatus.nodeId }}</p>
          <p v-if="offlineMode" class="hub-current">当前显示离线元数据。来源和云块可读性未知；可读性筛选暂不生效，连接查询节点后才能打开或修改文件。</p>
          <div v-if="legacyFoldersOpen" class="cloud-saved-queries">
            <p>同名旧文件夹按来源保持独立。选择两个或更多实体后手动合并。</p>
            <p v-if="!legacyFolders.length">没有待合并的旧文件夹。</p>
            <label v-for="folder in legacyFolders" :key="folder.id" class="hub-profile-row">
              <input v-model="selectedLegacyFolders" type="checkbox" :value="folder.id" />
              <span>{{ folder.name }} · {{ folder.count }} 个文件 · 来源 {{ folder.sources.join("、") }} · {{ folder.id }}</span>
            </label>
            <button class="button button-secondary" type="button" :disabled="selectedLegacyFolders.length < 2" @click="mergeSelectedLegacyFolders">合并选中项</button>
          </div>
          <div v-if="membersOpen" class="cloud-saved-queries">
            <p>退役设备重新加入时须先从 Hub 完整引导，并改用新作者 epoch。</p>
            <div v-for="member in members" :key="member.nodeId" class="hub-profile-row">
              <span>{{ member.nodeId }} · epoch {{ member.epoch }} · {{ member.state === 'active' ? '活跃' : '已退役' }} · 回执 {{ member.lastReceiptAt || '暂无' }}</span>
              <button v-if="member.state === 'active' && member.nodeId !== view.nodeId" class="button button-secondary" type="button" @click="retireMember(member.nodeId)">退役</button>
              <button v-if="member.state === 'retired' && member.nodeId === view.nodeId" class="button button-secondary" type="button" @click="rejoinMember">重新加入</button>
            </div>
          </div>
          <div v-if="transfersOpen" class="cloud-saved-queries">
            <div class="cloud-heading"><strong>上传与释放任务</strong><button class="quiet-button" type="button" :disabled="transfersLoading" @click="loadTransfers"><RefreshCw :size="15" />刷新</button></div>
            <p>上传任务依次经过待上传、读取、传输、读回验证，完成后才标记为安全保存；释放任务在移除本机位置后完成。失败或阻塞会保留原因，不会伪造成功。</p>
            <p v-if="transfersLoading">正在读取传输任务…</p>
            <p v-else-if="!transfers.length">暂无上传或释放任务。</p>
            <div v-for="task in transfers" :key="task.taskId" class="hub-profile-row">
              <div><strong>{{ task.kind === "release" ? "释放" : "上传" }} · {{ transferStateLabel(task.state) }}</strong><small>文件 {{ task.fileId }} · 版本 {{ task.revisionId || "—" }} · {{ formatSize(task.size) }} · {{ task.updatedAt }}</small><small v-if="task.locationId">位置 {{ task.locationId }}</small><small v-if="task.detail" :class="{ 'form-error': task.state === 'retryableFailed' || task.state === 'blocked' }">{{ task.detail }}</small></div>
            </div>
          </div>
          <div v-if="savedQueriesOpen" class="cloud-saved-queries">
            <p v-if="!savedQueries.length">还没有保存的查询。设置筛选条件后点击“保存当前条件”。</p>
            <div v-for="saved in savedQueries" :key="saved.id" class="hub-profile-row">
              <div><strong>{{ saved.name }}</strong><small>{{ saved.filters.scope === "all" ? "全部来源" : saved.filters.scope === "cloud" ? "仅云层" : saved.filters.scope.startsWith("device:") ? "设备 " + saved.filters.scope.slice(7) : "本机" }}<span v-if="saved.filters.query"> · “{{ saved.filters.query }}”</span><span v-if="saved.filters.folderId"> · 文件夹 {{ saved.filters.folderId }}</span><span v-if="saved.filters.tags.length"> · 标签 {{ saved.filters.tags.join("、") }}</span></small></div>
              <button class="button button-secondary" type="button" @click="applySavedQuery(saved)">应用</button>
              <button class="button button-secondary" type="button" @click="removeSavedQuery(saved)">删除</button>
            </div>
          </div>
          <p>文件夹和标签随来源设备的索引同步更新。检索在后端按逻辑文件去重并分页；未同步的设备文件不会出现在这里，受保护的文件请回到来源设备解锁查看。</p>
          <p v-if="coverage.sources?.length" class="hub-current">覆盖 {{ coverage.sources.length }} 个来源节点<span v-if="coverage.lastCatalogAt"> · 最近目录更新 {{ coverage.lastCatalogAt }}</span> · 索引代次 {{ coverage.generation?.slice(0, 8) || "—" }}</p>
          <p v-for="source in coverage.sources || []" :key="source" class="hub-current">来源 {{ source }} · {{ coverage.sourceLastCatalogAt?.[source] || "尚无目录时间" }}<span v-if="coverage.offlineSources?.includes(source)"> · 当前离线</span><span v-for="(counter, author) in coverage.sourceFrontiers?.[source] || {}" :key="author"> · {{ author }} 已应用至 {{ counter }}</span></p>
          <p v-for="source in coverage.unindexedSources || []" :key="source + '-unindexed'" class="hub-current">已知节点 {{ source }} · 暂无已索引文件位置<span v-if="coverage.offlineSources?.includes(source)"> · 当前离线</span><span v-for="(counter, author) in coverage.sourceFrontiers?.[source] || {}" :key="author"> · {{ author }} 已应用至 {{ counter }}</span></p>
          <p v-if="loading">正在读取云层索引…</p>
          <p v-else-if="!view.files.length">尚无已同步的文件。设备发布索引或文件加入云层后会显示在这里。</p>
          <p v-else>找到 {{ searchTotal }} 个逻辑文件，当前显示 {{ view.files.length }} 个。</p>
          <div v-for="file in offlineMode ? view.files : []" :key="'offline-' + file.id" class="hub-profile-row"><div><strong>{{ file.name }}</strong><small>{{ file.folderName || '未归组' }} · {{ formatSize(file.originalSize ?? file.size) }} · 离线元数据 · 可读性未知</small></div></div>
          <template v-for="file in offlineMode ? [] : view.files" :key="file.id">
          <p v-if="file.localOnly">仅设备索引 · 未存入云层。{{ remote ? '来源设备在线时可直接打开。' : '请从 Hub 资源库连接来源设备。' }}</p>
          <p v-for="location in file.locations || []" :key="location.nodeId + (location.resourceRef || location.path || '')">来源设备 {{ location.nodeId }}{{ location.resourceRef ? ' · 可解析' : ' · 旧索引需重新同步' }}</p>
          <p v-if="file.folderName || file.tags?.length">{{ file.folderName ? '文件夹：' + file.folderName : '未归组' }}<span v-if="file.tags?.length"> · 标签：{{ file.tags.join('、') }}</span></p>
          <div class="hub-profile-row">
            <div><strong>{{ file.name }}</strong><small v-if="!file.localOnly">文件总大小 {{ formatSize(file.size) }} · 原件 {{ formatSize(file.originalSize ?? file.size) }} · {{ file.chunks }} 块 · {{ file.replicas ?? file.minReplicas }} 份副本 · {{ file.availableReplicas ?? "—" }} 份可用（60 秒内端点探测） · {{ file.history ? "保留历史" : "无历史" }}<span v-if="(file.revisionCount || 0) > 1"> · {{ file.revisionCount }} 个版本</span></small><small v-else>文件大小 {{ formatSize(file.originalSize) }} · {{ file.locations?.length || 0 }} 处已记录的原件位置<span v-if="(file.revisionCount || 0) > 1"> · {{ file.revisionCount }} 个版本</span></small></div>
            <button v-if="previewKind(file) && ((file.revisionCount || 0) <= 1 || file.currentRevision)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="preview(file)"><LoaderCircle v-if="activeDownload === file.id" class="spin" :size="14" /><Play v-else :size="14" />{{ activeDownload === file.id ? "读取中" : activeVideo === file.id || activePreview?.fileID === file.id ? "收起预览" : "预览" }}</button>
            <button v-if="file.history" class="button button-secondary" type="button" @click="toggleVersions(file.id)">历史</button>
            <button v-if="remote && file.localOnly && readableLocation(file) && ((file.revisionCount || 0) <= 1 || file.currentRevision)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="openDeviceFile(file)"><ExternalLink :size="14" />打开来源</button>
            <button v-if="!file.localOnly && ((file.revisionCount || 0) <= 1 || file.currentRevision)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file.id, file.name)">
              <LoaderCircle v-if="activeDownload === file.id" class="spin" :size="14" /><Download v-else :size="14" />下载
            </button>
            <button class="button button-secondary" type="button" :disabled="detailLoading === (file.fileId || file.id)" @click="openDetail(file)"><LoaderCircle v-if="detailLoading === (file.fileId || file.id)" class="spin" :size="14" />详情</button>
            <span v-if="(file.revisionCount || 0) > 1 && !file.currentRevision" class="hub-current">多个版本，请在详情中选择当前版</span>
            <button class="button button-secondary" type="button" @click="changeLifecycle(file)">{{ lifecycle === "active" ? "移入回收站" : "恢复" }}</button>
            <button v-if="lifecycle === 'trashed'" class="button button-secondary" type="button" @click="deleteLibraryEntry(file)">永久删除条目</button>
          </div>
          <div v-if="activeVideo === file.id" class="cloud-video-preview"><StreamingVideo :id="file.id" :name="file.name" :remote="remote" cloud :qualities="file.variants" :file-size="file.originalSize" :file-id="file.fileId || file.id" :revision-id="file.revisionId" /></div>
          <div v-if="activePreview?.fileID === file.id" class="cloud-file-preview"><button class="quiet-button" type="button" @click="closePreview">关闭预览<X :size="14" /></button><img v-if="activePreview.kind === 'image'" :src="activePreview.url" :alt="file.name" /><audio v-else-if="activePreview.kind === 'audio'" :src="activePreview.url" controls preload="metadata" /><video v-else-if="activePreview.kind === 'video'" :src="activePreview.url" controls preload="metadata" /><iframe v-else :src="activePreview.url" :title="file.name" /></div>
          </template>
          <div v-for="file in view.files.filter((item) => versions[item.id])" :key="file.id + '-history'" class="cloud-history">
            <div v-for="version in versions[file.id]" :key="version.revisionId" class="hub-profile-row">
              <div><strong>{{ version.name }}</strong><small>{{ version.modified }} · {{ formatSize(version.size) }}</small></div>
              <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file.id, version.name, version.revisionId)"><Download :size="14" />下载此版</button>
            </div>
          </div>
          <button v-if="nextCursor" class="button button-secondary" type="button" :disabled="loadingMore" @click="loadMore"><LoaderCircle v-if="loadingMore" class="spin" :size="14" /><RefreshCw v-else :size="14" />{{ loadingMore ? "正在读取" : "加载更多" }}</button>
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
            <p v-if="recoveryStatus" class="hub-current">可恢复性：{{ recoveryStatus.recoverable ? "已有独立于本机的密钥持有者证明可恢复" : "尚无独立密钥持有者证明，暂不满足安全释放" }}<span v-if="recoveryStatus.independentHolder"> · 持有者 {{ recoveryStatus.independentHolder }}</span></p>
            <button class="button button-secondary" type="button" :disabled="recoveryBusy" @click="attestRecovery">{{ recoveryBusy ? "正在证明…" : "证明本机可恢复" }}</button>
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
  <Teleport to="body">
    <div v-if="syncOpen" class="resource-preview-backdrop" @click.self="syncOpen = false">
      <section class="hub-settings-dialog cloud-dialog" role="dialog" aria-modal="true" aria-label="跨设备同步">
        <header class="hub-settings-header">
          <div><strong>跨设备同步</strong><small>文件夹、标签修改会先写入本机操作队列，再由云层索引传播。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="syncOpen = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p v-if="syncLoading">正在读取同步状态…</p>
          <template v-else>
            <p class="hub-current">本机作者 {{ syncStatus?.author || "尚未产生修改" }}</p>
            <p>本机已保存并待发布 {{ syncStatus?.outbox?.pending ?? 0 }} 项 · 已交接 Hub 待确认 {{ syncStatus?.outbox?.sent ?? 0 }} 项 · 本查询端已应用 {{ syncStatus?.outbox?.acknowledged ?? 0 }} 项<span v-if="(syncStatus?.outbox?.failed ?? 0) > 0"> · 失败 {{ syncStatus?.outbox?.failed }} 项</span></p>
            <p class="hub-current">确认阶段说明：Hub 接收仅表示交接已持久化；只有某查询端应用或读取到该操作后，才算在该端可见。下方前沿只代表本端已应用进度，不代表所有设备已同步。</p>
            <div v-if="syncStatus?.frontiers && Object.keys(syncStatus.frontiers).length" class="cloud-heading"><strong>本端已应用前沿</strong></div>
            <p v-for="(counter, author) in syncStatus?.frontiers || {}" :key="author" class="hub-current">作者 {{ author }}：本端已应用至第 {{ counter }} 项</p>
            <div class="cloud-heading"><strong>安全清理</strong><button class="quiet-button" type="button" :disabled="cleanupBusy" @click="inspectCleanup">{{ cleanupBusy ? "正在核对" : "查看清理计划" }}</button></div>
            <template v-if="cleanupPlan">
              <p v-for="reason in cleanupPlan.blocked" :key="reason" class="hub-current">暂不可清理：{{ reason }}</p>
              <p v-if="!cleanupPlan.blocked.length">保留期 {{ cleanupPlan.retentionDays }} 天；已核对 {{ cleanupPlan.activeMembers.length }} 个活跃成员。可压缩目录记录 {{ cleanupPlan.eligible.filter(item => item.kind === "catalog").length }} 条，已删除文件内容记录 {{ cleanupPlan.eligible.filter(item => item.kind === "content").length }} 条，无引用本机云块 {{ cleanupPlan.eligible.filter(item => item.kind === "block").length }} 个。</p>
              <button v-if="!cleanupPlan.blocked.length" class="button button-secondary" type="button" :disabled="cleanupBusy" @click="runCleanup">执行此计划</button>
            </template>
            <div v-if="syncStatus?.receipts && Object.keys(syncStatus.receipts).length" class="cloud-heading"><strong>节点应用回执</strong></div>
            <div v-for="(frontiers, node) in syncStatus?.receipts || {}" :key="node" class="hub-profile-row">
              <div><strong>节点 {{ node }}</strong><small v-for="(counter, author) in frontiers" :key="author">作者 {{ author }}：该节点已应用至第 {{ counter }} 项</small></div>
            </div>
            <div v-if="syncStatus?.legacyImports && Object.keys(syncStatus.legacyImports).length" class="cloud-heading"><strong>旧目录导入前沿</strong></div>
            <p v-for="(progress, node) in syncStatus?.legacyImports || {}" :key="node" class="hub-current">来源 {{ node }}：已登记 {{ progress.count }} 条旧快照 · 最近目录 {{ progress.lastCatalogAt }}</p>
            <template v-if="syncStatus?.failures?.length">
              <div class="cloud-heading"><strong>失败记录（{{ syncStatus.failures.length }}）</strong></div>
              <div v-for="failure in syncStatus.failures" :key="failure.opId" class="hub-profile-row">
                <div><strong>{{ failure.kind }}</strong><small>作者 {{ failure.author }} 第 {{ failure.counter }} 项 · {{ failure.createdAt }}</small><small class="form-error">{{ failure.error || "未记录原因" }}</small></div>
              </div>
            </template>
            <div class="cloud-heading"><strong>文件夹冲突（{{ folderConflicts.length }}）</strong><button class="quiet-button" type="button" @click="showSync"><RefreshCw :size="15" />刷新</button></div>
            <p v-if="!folderConflicts.length">没有待处理的文件夹冲突。</p>
            <div v-for="conflict in folderConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.folderName || "未归组" }}</strong><small>文件 {{ conflict.fileId }} · 来自 {{ conflict.author }} 的第 {{ conflict.counter }} 项修改 · {{ conflict.createdAt }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveConflict(conflict)">选择归属</button>
            </div>
            <div class="cloud-heading"><strong>改名冲突（{{ renameConflicts.length }}）</strong></div>
            <p v-if="!renameConflicts.length">没有待处理的改名冲突。</p>
            <div v-for="conflict in renameConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.folderName }}</strong><small>原名 {{ conflict.anchor }} · 当前 {{ conflict.appliedName }} · 来自 {{ conflict.author }} 的第 {{ conflict.counter }} 项修改 · {{ conflict.createdAt }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveRename(conflict)">选择名称</button>
            </div>
            <div class="cloud-heading"><strong>文件改名冲突（{{ fileNameConflicts.length }}）</strong></div>
            <p v-if="!fileNameConflicts.length">没有待处理的文件改名冲突。</p>
            <div v-for="conflict in fileNameConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.fileName }}</strong><small>文件 {{ conflict.fileId }} · 当前 {{ conflict.appliedName }} · 来自 {{ conflict.author }} 的第 {{ conflict.counter }} 项修改</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveFileName(conflict)">选择名称</button>
            </div>
            <div class="cloud-heading"><strong>版本选择冲突（{{ revisionConflicts.length }}）</strong></div>
            <p v-if="!revisionConflicts.length">没有待处理的版本选择冲突。</p>
            <div v-for="conflict in revisionConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>版本 {{ conflict.revisionId }}</strong><small>文件 {{ conflict.fileId }} · 当前 {{ conflict.appliedRevisionId }} · 来自 {{ conflict.author }} 的第 {{ conflict.counter }} 项修改</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading || detailEditing" @click="chooseCurrentRevision(conflict.fileId, conflict.revisionId, true)">选择当前版</button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
  <Teleport to="body">
    <div v-if="detail" class="resource-preview-backdrop" @click.self="detail = null">
      <section class="hub-settings-dialog cloud-dialog" role="dialog" aria-modal="true" aria-label="文件详情">
        <header class="hub-settings-header">
          <div><strong>{{ detail.name }}</strong><small>统一详情页：跨设备去重后的版本、来源位置与可读取状态。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="detail = null"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p class="hub-current">内容状态 {{ contentStatusLabel(detail.contentStatus) }} · 归组状态 {{ detail.organizationStatus === "conflict" ? "存在待处理归属冲突" : "已同步" }}</p>
          <p>文件标识 {{ detail.fileId }} · {{ detail.folderName ? "文件夹 " + detail.folderName : "未归组" }}<span v-if="detail.tags.length"> · 标签 {{ detail.tags.join("、") }}</span></p>
          <div class="cloud-search-filters">
            <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('name')">修改显示名称</button>
            <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('folder')">修改文件夹</button>
            <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('addTag')">添加标签</button>
            <button v-for="tag in detail.tags" :key="tag" class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('removeTag', tag)">移除 {{ tag }}</button>
          </div>
          <div class="cloud-heading"><strong>版本（{{ detail.revisions.length }}）</strong></div>
          <div v-for="revision in detail.revisions" :key="revision.revisionId" class="hub-profile-row">
            <div><strong>{{ revision.modified || "未知时间" }}{{ detail.currentRevisionId === revision.revisionId ? " · 当前版" : "" }}</strong><small>版本 {{ revision.revisionId }} · {{ formatSize(revision.size) }} · 原件 {{ formatSize(revision.originalSize) }} · {{ revision.chunks }} 块 · {{ revision.replicas }} 份副本 · {{ revision.availableReplicas }} 份可用 · {{ revision.localOnly ? "仅设备索引" : "已入云层" }}{{ revision.history ? " · 保留历史" : "" }}</small></div>
            <button v-if="detail.currentRevisionId !== revision.revisionId" class="button button-secondary" type="button" :disabled="detailEditing" @click="chooseCurrentRevision(detail.fileId, revision.revisionId)">设为当前版</button>
            <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(detail.fileId, detail.name, revision.revisionId)"><Download :size="14" />下载此版</button>
          </div>
          <div class="cloud-heading"><strong>来源位置（{{ detail.locations.length }}）</strong></div>
          <p v-if="!detail.locations.length">没有已记录的原件位置；内容仅存在于云层分块中。</p>
          <div v-for="location in detail.locations" :key="location.nodeId + (location.resourceRef || location.path || '')" class="hub-profile-row">
            <div><strong>节点 {{ location.nodeId }}</strong><small>{{ location.resourceRef ? "可解析资源 " + location.resourceRef : location.path ? "路径 " + location.path : "旧索引需重新同步" }}</small></div>
          </div>
          <div class="cloud-heading"><strong>安全释放（{{ detail.releaseLocations?.length || 0 }}）</strong></div>
          <p>释放前需满足两份独立故障域的可验证保存与恢复密钥证明。手机来源须先在手机上完成 SAF 删除，再在此确认移除本机位置记录；分类与云层副本始终保留。</p>
          <p v-if="!detail.releaseLocations?.length">没有已登记的可释放来源位置。</p>
          <div v-for="location in detail.releaseLocations || []" :key="location.locationId" class="hub-profile-row">
            <div>
              <strong>节点 {{ location.nodeId || "本机" }} · {{ location.state === "available" ? "原件仍在" : "已释放" }}</strong>
              <small>位置 {{ location.locationId }} · 版本 {{ location.revisionId || "—" }}</small>
              <template v-if="releaseChecks[location.locationId]">
                <small :class="{ 'form-error': !releaseChecks[location.locationId].eligible }">{{ releaseChecks[location.locationId].eligible ? "可安全释放" : "暂不可释放" }} · {{ releaseChecks[location.locationId].reason }}</small>
                <small>独立故障域 {{ releaseChecks[location.locationId].domains.length }} 个<span v-if="releaseChecks[location.locationId].recoveryHolder"> · 恢复密钥持有者 {{ releaseChecks[location.locationId].recoveryHolder }}</span></small>
              </template>
            </div>
            <button v-if="location.state === 'available'" class="button button-secondary" type="button" :disabled="releaseBusy === location.locationId" @click="checkRelease(location)">{{ releaseBusy === location.locationId ? "检查中…" : "释放检查" }}</button>
            <button v-if="location.state === 'available' && releaseChecks[location.locationId]?.eligible" class="button button-secondary" type="button" :disabled="releaseBusy === location.locationId" @click="doRelease(location)">确认释放</button>
          </div>
          <p v-if="detail.folderConflicts">此文件有 {{ detail.folderConflicts }} 项待处理的归属冲突，请在“同步”面板中选择归属。</p>
        </div>
      </section>
    </div>
  </Teleport>
</template>
