<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { Cloud, RefreshCw, Upload, X } from "@lucide/vue";
import {
  diagnoseCloudPeer, executeLibraryCleanup, getCloudFiles, getCloudRecovery, getCloudRecoveryStatus, getCloudTransfers, getFileNameConflicts, getFolderConflicts, getLegacyFolders, getLibraryCleanupPlan, getLibraryMembers, getLibrarySyncStatus, getRenameConflicts, getRevisionConflicts, importCloudRecovery, mergeLegacyFolders, rejoinLibraryMember, renameLibraryFile, resolveFolderConflict, resolveRenameConflict, retireLibraryMember, selectLibraryCurrentRevision, selfAttestCloudRecovery, speedTestCloudPeer, uploadResourceToCloud,
  type CloudPeerDiagnosis, type CloudRecoveryStatus, type CloudSpeedTest, type CloudTransferTask, type CloudView, type FileNameConflict, type FolderConflict, type LegacyFolderGroup, type LibraryCleanupPlan, type LibraryMember, type LibrarySyncStatus, type RenameConflict, type Resource, type RevisionConflict,
} from "./api";

defineProps<{ resources: Resource[]; remote: boolean }>();
const open = ref(false);
const loading = ref(false);
const error = ref("");
const view = ref<CloudView>({ files: [], jobs: {}, nodeId: "", libraryId: "", peers: [], directEndpoint: "" });
const recoveryKey = ref("");
const importKey = ref("");
const historyChoices = ref<Record<string, boolean>>({});
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
const legacyFoldersOpen = ref(false);
const legacyFolders = ref<LegacyFolderGroup[]>([]);
const selectedLegacyFolders = ref<string[]>([]);
const membersOpen = ref(false);
const members = ref<LibraryMember[]>([]);
const transfersOpen = ref(false);
const transfersLoading = ref(false);
const transfers = ref<CloudTransferTask[]>([]);
const recoveryStatus = ref<CloudRecoveryStatus | null>(null);
const recoveryBusy = ref(false);
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
  try {
    view.value = await getCloudFiles();
    for (const file of view.value.files) {
      if (file.sourceId && historyChoices.value[file.sourceId] === undefined) historyChoices.value[file.sourceId] = file.history;
    }
    error.value = "";
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取云库"; }
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
async function show() {
  open.value = true;
  loading.value = true;
  await refresh();
  loading.value = false;
  void loadRecoveryStatus();
  if (transfersOpen.value) void loadTransfers();
}
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
async function chooseCurrentRevision(fileID: string, revisionID: string) {
  if (syncLoading.value) return;
  syncLoading.value = true;
  try {
    await selectLibraryCurrentRevision(fileID, revisionID, true);
    await refresh();
    await showSync();
    error.value = "";
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "无法选择当前版本"; }
  finally { syncLoading.value = false; }
}
async function upload(resource: Resource) {
  try { await uploadResourceToCloud(resource.id, historyChoices.value[resource.id] ?? false); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法上传文件"; }
}
function cloudMembership(resource: Resource) {
  return view.value.files.find((file) => !file.localOnly && (file.id === resource.id || file.sourceId === resource.id));
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
  else recoveryKey.value = "";
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>

<template>
  <button class="button button-secondary" type="button" @click="show()"><Cloud :size="15" />云库</button>
  <button v-if="!remote" class="button button-secondary" type="button" @click="showSync"><RefreshCw :size="15" />同步</button>
  <Teleport to="body">
    <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
      <section class="hub-settings-dialog cloud-dialog" role="dialog" aria-modal="true" aria-label="云库">
        <header class="hub-settings-header">
          <div><strong>云库</strong><small>文件加密保存在多个节点；查找文件请用统一检索。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p v-if="loading">正在读取云库状态…</p>
          <p class="hub-current"><span :title="view.nodeId">{{ view.nodeId ? (remote ? "查询节点已连接" : "本机节点") : "未连接" }}</span> · 已发现 {{ view.peers.length }} 个其他在线节点</p>
          <details v-if="view.peers.length" class="cloud-advanced-note"><summary>关于连接诊断与测速</summary><p>连接诊断会分别显示每条路径的 RTT、建链/请求时间、诊断吞吐、候选类型和失败原因。ICE 的 host ↔ host 通常表示主机候选直连，出现 relay 表示经过 TURN。纯测速只收发合成数据，每条路径 3 轮、每轮 4 MiB；速度按数据传输时间计算，不读写云库文件。</p></details>
          <p v-if="!remote && view.peers.length === 0">当前只有本机一个节点：文件仍会加密保存、随时可读；副本不足时会提示。</p>
		  <p v-if="!remote && !view.directEndpoint && !view.lan?.length && view.peers.length > 0">若节点直连失败，请检查网络或配置可访问的地址。</p>
		  <div v-for="peer in view.peers" :key="peer.id" class="hub-profile-row cloud-peer-row">
		    <div><strong :title="peer.id">{{ peer.role === "storage" ? "存储节点" : "客户端" }} · {{ peer.id.slice(0, 8) }}</strong><small>{{ peer.lan?.length ? "局域网可达 · " : "" }}{{ peer.endpoint ? "直连地址已就绪" : "等待直连地址" }}</small></div>
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
          <div class="cloud-heading"><strong>资源库管理</strong></div>
          <div class="cloud-search-filters">
            <button class="button button-secondary" type="button" @click="loadLegacyFolders">旧文件夹身份</button>
            <button class="button button-secondary" type="button" @click="loadMembers">资源库成员</button>
            <button class="button button-secondary" type="button" @click="showTransfers">传输任务</button>
          </div>
          <div v-if="legacyFoldersOpen" class="cloud-saved-queries">
            <p>同名旧文件夹按来源保持独立，选择两个或更多可合并。</p>
            <p v-if="!legacyFolders.length">没有待合并的旧文件夹。</p>
            <label v-for="folder in legacyFolders" :key="folder.id" class="hub-profile-row">
              <input v-model="selectedLegacyFolders" type="checkbox" :value="folder.id" />
              <span :title="folder.id">{{ folder.name }} · {{ folder.count }} 个文件 · 来源 {{ folder.sources.join("、") }}</span>
            </label>
            <button class="button button-secondary" type="button" :disabled="selectedLegacyFolders.length < 2" @click="mergeSelectedLegacyFolders">合并选中项</button>
          </div>
          <div v-if="membersOpen" class="cloud-saved-queries">
            <p>退役设备重新加入时按新成员处理。</p>
            <div v-for="member in members" :key="member.nodeId" class="hub-profile-row">
              <span :title="`节点 ${member.nodeId} · 作者 epoch ${member.epoch}`">{{ member.nodeId === view.nodeId ? "本机" : member.nodeId.slice(0, 8) }} · {{ member.state === 'active' ? '活跃' : '已退役' }} · 回执 {{ member.lastReceiptAt || '暂无' }}</span>
              <button v-if="member.state === 'active' && member.nodeId !== view.nodeId" class="button button-secondary" type="button" @click="retireMember(member.nodeId)">退役</button>
              <button v-if="member.state === 'retired' && member.nodeId === view.nodeId" class="button button-secondary" type="button" @click="rejoinMember">重新加入</button>
            </div>
          </div>
          <div v-if="transfersOpen" class="cloud-saved-queries">
            <div class="cloud-heading"><strong>上传与释放任务</strong><button class="quiet-button" type="button" :disabled="transfersLoading" @click="loadTransfers"><RefreshCw :size="15" />刷新</button></div>
            <p>上传完成并通过读回验证后才算安全保存；失败会保留原因。</p>
            <p v-if="transfersLoading">正在读取传输任务…</p>
            <p v-else-if="!transfers.length">暂无上传或释放任务。</p>
            <div v-for="task in transfers" :key="task.taskId" class="hub-profile-row">
              <div><strong>{{ task.kind === "release" ? "释放" : "上传" }} · {{ transferStateLabel(task.state) }}</strong><small :title="`文件 ${task.fileId} · 版本 ${task.revisionId || '—'}${task.locationId ? ' · 位置 ' + task.locationId : ''}`">{{ formatSize(task.size) }} · {{ task.updatedAt }}</small><small v-if="task.detail" :class="{ 'form-error': task.state === 'retryableFailed' || task.state === 'blocked' }">{{ task.detail }}</small></div>
            </div>
          </div>
          <template v-if="!remote">
            <div class="cloud-heading"><strong>将当前页面的本地文件加入云库</strong></div>
            <p v-if="!resources.length">进入资源列表后，可从这里选择文件上传。</p>
            <div v-for="resource in resources" :key="resource.id" class="hub-profile-row">
              <div><strong>{{ resource.name }}</strong><small>{{ formatSize(resource.contentSize ?? resource.size) }} · {{ cloudMembership(resource) ? `已在云库，${cloudMembership(resource)?.replicas ?? cloudMembership(resource)?.minReplicas ?? 0} 份副本，${cloudMembership(resource)?.availableReplicas ?? "—"} 份可用` : view.jobs[resource.id] || "仅本地" }}</small><label class="cloud-history-choice"><input v-model="historyChoices[resource.id]" type="checkbox" />保留后续历史版本</label></div>
              <button v-if="!cloudMembership(resource)" class="button button-secondary" type="button" :disabled="!resource.available || view.jobs[resource.id] === '上传中'" @click="upload(resource)">
                <Upload :size="14" />{{ view.jobs[resource.id] === "上传中" ? "上传中" : "加入云库" }}
              </button>
            </div>
            <div class="cloud-heading"><strong>恢复密钥</strong></div>
            <p>请把恢复密钥另存到安全位置，更换设备时用它解锁加密索引。</p>
            <p v-if="recoveryStatus" class="hub-current" :title="`${recoveryStatus.recoverable ? '已有独立于本机的密钥持有者证明可恢复' : '尚无独立密钥持有者证明，暂不满足安全释放'}${recoveryStatus.independentHolder ? ' · 持有者 ' + recoveryStatus.independentHolder : ''}`">可恢复性：{{ recoveryStatus.recoverable ? "已确认可恢复" : "尚未确认" }}</p>
            <button class="button button-secondary" type="button" :disabled="recoveryBusy" @click="attestRecovery">{{ recoveryBusy ? "正在证明…" : "证明本机可恢复" }}</button>
            <button class="button button-secondary" type="button" @click="revealKey">显示恢复密钥</button>
            <code v-if="recoveryKey" class="cloud-recovery-key">{{ recoveryKey }}</code>
            <form class="hub-settings-form" @submit.prevent="importKeyNow">
              <label>在新设备上导入恢复密钥<input v-model="importKey" minlength="64" maxlength="64" autocomplete="off" /></label>
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
          <div><strong>跨设备同步</strong><small>文件夹与标签的修改会自动同步到其他设备。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="syncOpen = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body cloud-panel-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p v-if="syncLoading">正在读取同步状态…</p>
          <template v-else>
            <p>待同步 {{ syncStatus?.outbox?.pending ?? 0 }} 项 · 已发送待确认 {{ syncStatus?.outbox?.sent ?? 0 }} 项 · 已应用 {{ syncStatus?.outbox?.acknowledged ?? 0 }} 项<span v-if="(syncStatus?.outbox?.failed ?? 0) > 0"> · 失败 {{ syncStatus?.outbox?.failed }} 项</span></p>
            <details class="cloud-advanced-note">
              <summary>同步技术详情</summary>
              <p class="hub-current" :title="syncStatus?.author">本机作者 {{ syncStatus?.author || "尚未产生修改" }}</p>
              <p>Hub 接收仅表示交接已持久化；只有某查询端应用或读取到该操作后，才算在该端可见。下方前沿只代表本端已应用进度，不代表所有设备已同步。</p>
              <template v-if="syncStatus?.frontiers && Object.keys(syncStatus.frontiers).length">
                <div class="cloud-heading"><strong>本端已应用前沿</strong></div>
                <p v-for="(counter, author) in syncStatus?.frontiers || {}" :key="author" class="hub-current">作者 {{ author }}：本端已应用至第 {{ counter }} 项</p>
              </template>
              <template v-if="syncStatus?.receipts && Object.keys(syncStatus.receipts).length">
                <div class="cloud-heading"><strong>节点应用回执</strong></div>
                <div v-for="(frontiers, node) in syncStatus?.receipts || {}" :key="node" class="hub-profile-row">
                  <div><strong>节点 {{ node }}</strong><small v-for="(counter, author) in frontiers" :key="author">作者 {{ author }}：该节点已应用至第 {{ counter }} 项</small></div>
                </div>
              </template>
              <template v-if="syncStatus?.legacyImports && Object.keys(syncStatus.legacyImports).length">
                <div class="cloud-heading"><strong>旧目录导入前沿</strong></div>
                <p v-for="(progress, node) in syncStatus?.legacyImports || {}" :key="node" class="hub-current">来源 {{ node }}：已登记 {{ progress.count }} 条旧快照 · 最近目录 {{ progress.lastCatalogAt }}</p>
              </template>
            </details>
            <div class="cloud-heading"><strong>安全清理</strong><button class="quiet-button" type="button" :disabled="cleanupBusy" @click="inspectCleanup">{{ cleanupBusy ? "正在核对" : "查看清理计划" }}</button></div>
            <template v-if="cleanupPlan">
              <p v-for="reason in cleanupPlan.blocked" :key="reason" class="hub-current">暂不可清理：{{ reason }}</p>
              <p v-if="!cleanupPlan.blocked.length">保留期 {{ cleanupPlan.retentionDays }} 天；已核对 {{ cleanupPlan.activeMembers.length }} 个活跃成员。可压缩目录记录 {{ cleanupPlan.eligible.filter(item => item.kind === "catalog").length }} 条，已删除文件内容记录 {{ cleanupPlan.eligible.filter(item => item.kind === "content").length }} 条，无引用本机云块 {{ cleanupPlan.eligible.filter(item => item.kind === "block").length }} 个。</p>
              <button v-if="!cleanupPlan.blocked.length" class="button button-secondary" type="button" :disabled="cleanupBusy" @click="runCleanup">执行此计划</button>
            </template>
            <template v-if="syncStatus?.failures?.length">
              <div class="cloud-heading"><strong>失败记录（{{ syncStatus.failures.length }}）</strong></div>
              <div v-for="failure in syncStatus.failures" :key="failure.opId" class="hub-profile-row">
                <div><strong>{{ failure.kind }}</strong><small>作者 {{ failure.author }} 第 {{ failure.counter }} 项 · {{ failure.createdAt }}</small><small class="form-error">{{ failure.error || "未记录原因" }}</small></div>
              </div>
            </template>
            <div class="cloud-heading"><strong>文件夹冲突（{{ folderConflicts.length }}）</strong><button class="quiet-button" type="button" @click="showSync"><RefreshCw :size="15" />刷新</button></div>
            <p v-if="!folderConflicts.length">没有待处理的文件夹冲突。</p>
            <div v-for="conflict in folderConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.folderName || "未归组" }}</strong><small :title="`文件 ${conflict.fileId} · 来自 ${conflict.author} 的第 ${conflict.counter} 项修改`">{{ conflict.createdAt }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveConflict(conflict)">选择归属</button>
            </div>
            <div class="cloud-heading"><strong>改名冲突（{{ renameConflicts.length }}）</strong></div>
            <p v-if="!renameConflicts.length">没有待处理的改名冲突。</p>
            <div v-for="conflict in renameConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.folderName }}</strong><small :title="`来自 ${conflict.author} 的第 ${conflict.counter} 项修改`">原名 {{ conflict.anchor }} · 当前 {{ conflict.appliedName }} · {{ conflict.createdAt }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveRename(conflict)">选择名称</button>
            </div>
            <div class="cloud-heading"><strong>文件改名冲突（{{ fileNameConflicts.length }}）</strong></div>
            <p v-if="!fileNameConflicts.length">没有待处理的文件改名冲突。</p>
            <div v-for="conflict in fileNameConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong>{{ conflict.fileName }}</strong><small :title="`文件 ${conflict.fileId} · 来自 ${conflict.author} 的第 ${conflict.counter} 项修改`">当前 {{ conflict.appliedName }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="resolveFileName(conflict)">选择名称</button>
            </div>
            <div class="cloud-heading"><strong>版本选择冲突（{{ revisionConflicts.length }}）</strong></div>
            <p v-if="!revisionConflicts.length">没有待处理的版本选择冲突。</p>
            <div v-for="conflict in revisionConflicts" :key="conflict.opId" class="hub-profile-row">
              <div><strong :title="conflict.revisionId">版本 {{ conflict.revisionId.slice(0, 8) }}</strong><small :title="`文件 ${conflict.fileId} · 当前 ${conflict.appliedRevisionId} · 来自 ${conflict.author} 的第 ${conflict.counter} 项修改`">当前版 {{ conflict.appliedRevisionId.slice(0, 8) }}</small></div>
              <button class="button button-secondary" type="button" :disabled="syncLoading" @click="chooseCurrentRevision(conflict.fileId, conflict.revisionId)">选择当前版</button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
</template>
