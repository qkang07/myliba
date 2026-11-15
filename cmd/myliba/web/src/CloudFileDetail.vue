<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { Download, ExternalLink, LoaderCircle, Play, X } from "@lucide/vue";
import StreamingVideo from "./StreamingVideo.vue";
import { previewKind, useCloudRead } from "./useCloudRead";
import {
  addLibraryFileTag, getLibraryFileDetail, moveLibraryFile, releaseCheck, releaseOriginal, removeLibraryFileTag, renameLibraryFile, selectLibraryCurrentRevision, setLibraryFileLifecycle,
  type CloudFile, type HubResourceNode, type LibraryFileDetail, type ReleaseCheck, type ReleaseLocation,
} from "./api";

const props = defineProps<{ file: CloudFile; remote: boolean; nodes: HubResourceNode[]; queryNodeId: string }>();
const emit = defineEmits<{ close: []; changed: []; openResource: [id: string] }>();

const detail = ref<LibraryFileDetail | null>(null);
const detailLoading = ref(false);
const detailEditing = ref(false);
const error = ref("");
const releaseChecks = ref<Record<string, ReleaseCheck>>({});
const releaseBusy = ref("");

const { activeDownload, activeVideo, activePreview, nodeLabel, readableLocation, closePreview, openDeviceFile, download, preview } = useCloudRead({
  remote: computed(() => props.remote),
  queryNodeId: computed(() => props.queryNodeId),
  nodes: computed(() => props.nodes),
  onError: (message) => { error.value = message; },
  onOpenResource: (id) => { emit("openResource", id); emit("close"); },
});

const activeFile = computed<CloudFile>(() => detail.value ? { ...props.file, name: detail.value.name, revisionId: detail.value.currentRevisionId } : props.file);
const canPreview = computed(() => Boolean(previewKind(activeFile.value)) && ((activeFile.value.revisionCount || 0) <= 1 || activeFile.value.currentRevision));

function formatSize(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}

function contentStatusLabel(status: LibraryFileDetail["contentStatus"]) {
  switch (status) {
    case "readable": return "可读取";
    case "cloudMissing": return "云库副本缺失，需可达来源设备";
    case "sourceOffline": return "来源设备离线";
    default: return "当前不可用";
  }
}

async function loadDetail() {
  const id = props.file.fileId || props.file.id;
  detailLoading.value = true;
  releaseChecks.value = {};
  try { detail.value = await getLibraryFileDetail(id); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取文件详情"; }
  finally { detailLoading.value = false; }
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
    emit("changed");
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法修改分类"; }
  finally { detailEditing.value = false; }
}

async function chooseCurrentRevision(fileID: string, revisionID: string) {
  if (detailEditing.value) return;
  detailEditing.value = true;
  try {
    await selectLibraryCurrentRevision(fileID, revisionID);
    detail.value = await getLibraryFileDetail(fileID);
    emit("changed");
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法选择当前版本"; }
  finally { detailEditing.value = false; }
}

async function changeLifecycle() {
  if (!detail.value || detailEditing.value) return;
  const state = detail.value.lifecycle === "active" ? "trashed" : "active";
  if (state === "trashed" && !window.confirm(`将“${detail.value.name}”移入回收站？文件原件和副本会保留。`)) return;
  const fileID = detail.value.fileId;
  detailEditing.value = true;
  try { await setLibraryFileLifecycle(fileID, state); detail.value = await getLibraryFileDetail(fileID); emit("changed"); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法更新回收状态"; }
  finally { detailEditing.value = false; }
}

async function deleteEntry() {
  if (!detail.value || detailEditing.value) return;
  if (!window.confirm(`永久删除“${detail.value.name}”的资源库条目？此操作不能恢复；原件与云块仍等待安全回收。`)) return;
  detailEditing.value = true;
  try { await setLibraryFileLifecycle(detail.value.fileId, "deleted"); emit("changed"); emit("close"); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法删除资源库条目"; }
  finally { detailEditing.value = false; }
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
  if (!window.confirm(`释放位置 ${location.nodeId || location.locationId} 的本机原件记录？请先确认该设备上的原件已安全删除（手机须先完成 SAF 删除）。释放只移除本机位置记录，保留分类与云库副本。`)) return;
  releaseBusy.value = location.locationId;
  try {
    const check = await releaseCheck(fileID, location.locationId, location.revisionId);
    releaseChecks.value = { ...releaseChecks.value, [location.locationId]: check };
    if (!check.eligible || !check.credential) { error.value = check.reason || "暂不满足安全释放条件"; return; }
    await releaseOriginal(fileID, location.locationId, check.credential);
    detail.value = await getLibraryFileDetail(fileID);
    emit("changed");
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "释放未完成"; }
  finally { releaseBusy.value = ""; }
}

function close() {
  closePreview();
  activeVideo.value = "";
  emit("close");
}

watch(() => props.file, () => { closePreview(); activeVideo.value = ""; void loadDetail(); });
onMounted(() => void loadDetail());
</script>

<template>
  <Teleport to="body">
    <div class="resource-preview-backdrop" @click.self="close">
      <section class="hub-settings-dialog cloud-dialog" role="dialog" aria-modal="true" aria-label="文件详情">
        <header class="hub-settings-header">
          <div><strong>{{ detail?.name || file.name }}</strong><small>版本、来源位置与可读状态</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="close"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <div v-if="detailLoading && !detail" class="library-loading"><LoaderCircle class="spin" :size="16" />正在读取文件详情…</div>
          <template v-if="detail">
            <p class="hub-current">{{ contentStatusLabel(detail.contentStatus) }} · {{ detail.organizationStatus === "conflict" ? "归属待处理" : "分类已同步" }}</p>
            <p class="library-detail-meta" :title="`文件标识 ${detail.fileId}`">{{ detail.folderName ? "文件夹 " + detail.folderName : "未归组" }}<span v-if="detail.tags.length"> · 标签 {{ detail.tags.join("、") }}</span></p>
            <div class="library-detail-actions">
              <button v-if="canPreview" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="preview(activeFile)"><LoaderCircle v-if="activeDownload === activeFile.id" class="spin" :size="14" /><Play v-else :size="14" />{{ activeDownload === activeFile.id ? "读取中" : activeVideo === activeFile.id || activePreview?.fileID === activeFile.id ? "收起预览" : "预览" }}</button>
              <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(activeFile, detail.name)"><LoaderCircle v-if="activeDownload === activeFile.id" class="spin" :size="14" /><Download v-else :size="14" />下载</button>
              <button v-if="remote && activeFile.localOnly && readableLocation(activeFile)" class="button button-secondary" type="button" :disabled="!!activeDownload" @click="openDeviceFile(activeFile)"><ExternalLink :size="14" />打开来源</button>
            </div>
            <div v-if="activeVideo === activeFile.id" class="library-result-media"><div class="cloud-video-preview"><StreamingVideo :id="activeFile.id" :name="activeFile.name" :remote="remote" cloud :qualities="activeFile.variants" :file-size="activeFile.originalSize" :file-id="activeFile.fileId || activeFile.id" :revision-id="activeFile.revisionId" /></div></div>
            <div v-if="activePreview?.fileID === activeFile.id" class="library-result-media"><div class="cloud-file-preview"><button class="quiet-button" type="button" @click="closePreview">关闭预览<X :size="14" /></button><img v-if="activePreview.kind === 'image'" :src="activePreview.url" :alt="activeFile.name" /><audio v-else-if="activePreview.kind === 'audio'" :src="activePreview.url" controls preload="metadata" /><video v-else-if="activePreview.kind === 'video'" :src="activePreview.url" controls preload="metadata" /><iframe v-else :src="activePreview.url" :title="activeFile.name" /></div></div>
            <div class="library-detail-actions">
              <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('name')">修改显示名称</button>
              <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('folder')">修改文件夹</button>
              <button class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('addTag')">添加标签</button>
              <button v-for="tag in detail.tags" :key="tag" class="button button-secondary" type="button" :disabled="detailEditing" @click="editDetail('removeTag', tag)">移除 {{ tag }}</button>
            </div>
            <div class="library-detail-actions">
              <button class="button button-secondary" type="button" :disabled="detailEditing" @click="changeLifecycle">{{ detail.lifecycle === "active" ? "移入回收站" : "恢复" }}</button>
              <button v-if="detail.lifecycle === 'trashed'" class="button button-ghost library-danger" type="button" :disabled="detailEditing" @click="deleteEntry">永久删除条目</button>
            </div>
            <div class="cloud-heading"><strong>版本（{{ detail.revisions.length }}）</strong></div>
            <div v-for="revision in detail.revisions" :key="revision.revisionId" class="hub-profile-row">
              <div><strong>{{ revision.modified || "未知时间" }}{{ detail.currentRevisionId === revision.revisionId ? " · 当前版" : "" }}</strong><small :title="`版本 ${revision.revisionId}`">{{ formatSize(revision.size) }} · {{ revision.availableReplicas }}/{{ revision.replicas }} 份副本可用 · {{ revision.localOnly ? "未上传云库" : "已入云库" }}{{ revision.history ? " · 保留历史" : "" }}</small></div>
              <button v-if="detail.currentRevisionId !== revision.revisionId" class="button button-secondary" type="button" :disabled="detailEditing" @click="chooseCurrentRevision(detail.fileId, revision.revisionId)">设为当前版</button>
              <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(activeFile, detail.name, revision.revisionId)"><Download :size="14" />下载此版</button>
            </div>
            <div class="cloud-heading"><strong>来源位置（{{ detail.locations.length }}）</strong></div>
            <p v-if="!detail.locations.length" class="library-detail-meta">没有已记录的原件位置；内容保存在云库中。</p>
            <div v-for="location in detail.locations" :key="location.nodeId + (location.resourceRef || location.path || '')" class="hub-profile-row">
              <div><strong :title="location.nodeId">{{ nodeLabel(location.nodeId) }}</strong><small :title="location.resourceRef || location.path">{{ location.resourceRef ? "可查看原件" : location.path ? location.path : "等待设备重新同步" }}</small></div>
            </div>
            <div class="cloud-heading"><strong>安全释放（{{ detail.releaseLocations?.length || 0 }}）</strong></div>
            <p class="library-detail-meta" title="释放前需满足两份独立故障域的可验证保存与恢复密钥证明。手机来源须先在手机上完成 SAF 删除，再在此确认移除本机位置记录。">确认已有安全副本后才能移除本机原件记录；分类与云库副本始终保留。</p>
            <p v-if="!detail.releaseLocations?.length" class="library-detail-meta">没有已登记的可释放来源位置。</p>
            <div v-for="location in detail.releaseLocations || []" :key="location.locationId" class="hub-profile-row">
              <div>
                <strong :title="location.nodeId">{{ nodeLabel(location.nodeId || undefined) }} · {{ location.state === "available" ? "原件仍在" : "已释放" }}</strong>
                <small :title="`位置 ${location.locationId} · 版本 ${location.revisionId || '—'}`">{{ location.state === "available" ? "可检查是否满足释放条件" : "本机位置记录已移除" }}</small>
                <template v-if="releaseChecks[location.locationId]">
                  <small :class="{ 'form-error': !releaseChecks[location.locationId].eligible }">{{ releaseChecks[location.locationId].eligible ? "可安全释放" : "暂不可释放" }} · {{ releaseChecks[location.locationId].reason }}</small>
                  <small :title="`独立故障域 ${releaseChecks[location.locationId].domains.length} 个${releaseChecks[location.locationId].recoveryHolder ? ' · 恢复密钥持有者 ' + releaseChecks[location.locationId].recoveryHolder : ''}`">副本保护 {{ releaseChecks[location.locationId].domains.length }} 重{{ releaseChecks[location.locationId].recoveryHolder ? " · 已有恢复证明" : "" }}</small>
                </template>
              </div>
              <button v-if="location.state === 'available'" class="button button-secondary" type="button" :disabled="releaseBusy === location.locationId" @click="checkRelease(location)">{{ releaseBusy === location.locationId ? "检查中…" : "释放检查" }}</button>
              <button v-if="location.state === 'available' && releaseChecks[location.locationId]?.eligible" class="button button-secondary" type="button" :disabled="releaseBusy === location.locationId" @click="doRelease(location)">确认释放</button>
            </div>
            <p v-if="detail.folderConflicts" class="library-notice">此文件有 {{ detail.folderConflicts }} 项待处理的归属冲突，请在“同步”面板中选择归属。</p>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
</template>