<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { ArrowDown, ArrowLeft, ArrowRight, Check, File, Folder, LoaderCircle, RefreshCw, WifiOff, X } from "@lucide/vue";
import {
  browseTransferDirectory,
  cancelTransferFile,
  selectResourceNode,
  readTransferChunk,
  writeTransferChunk,
  type HubResourceNode,
  type TransferDirectory,
  type TransferEntry,
} from "./api";

const props = defineProps<{ nodes: HubResourceNode[]; selectedNodeId: string }>();
const emit = defineEmits<{ close: [] }>();
const availableNodes = computed(() => props.nodes.filter((node) => node.online && node.kind === "desktop"));
const sourceNodeId = ref("");
const targetNodeId = ref("");
const sourceDir = ref<TransferDirectory>({ current: "", parent: "", roots: [], entries: [] });
const targetDir = ref<TransferDirectory>({ current: "", parent: "", roots: [], entries: [] });
const sourceSelected = ref(new Set<string>());
const busy = ref(false);
const listing = ref(false);
const error = ref("");
const progress = ref({ name: "", transferred: 0, total: 0 });
const controller = ref<AbortController | null>(null);
const originalNodeId = props.selectedNodeId;
const chunkSize = 1024 * 1024;

const sourceFiles = computed(() => sourceDir.value.entries.filter((entry) => !entry.dir));
const selectedFiles = computed(() => sourceFiles.value.filter((entry) => sourceSelected.value.has(entry.path)));
const percent = computed(() => progress.value.total ? Math.min(100, Math.floor(progress.value.transferred * 100 / progress.value.total)) : busy.value ? 0 : 100);

async function loadDirectory(side: "source" | "target", path = "") {
  const nodeId = side === "source" ? sourceNodeId.value : targetNodeId.value;
  if (!nodeId) return;
  listing.value = true;
  error.value = "";
  try {
    await selectResourceNode(nodeId);
    const data = await browseTransferDirectory(path);
    if (side === "source") { sourceDir.value = data; sourceSelected.value = new Set(); }
    else targetDir.value = data;
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : "无法读取目录";
  } finally { listing.value = false; }
}

async function switchNode(side: "source" | "target", id: string) {
  if (side === "source") {
    sourceNodeId.value = id;
    if (targetNodeId.value === id) targetNodeId.value = availableNodes.value.find((node) => node.id !== id)?.id || "";
    await loadDirectory("source");
    if (targetNodeId.value && targetDir.value.current === "") await loadDirectory("target");
  } else {
    targetNodeId.value = id;
    if (sourceNodeId.value === id) sourceNodeId.value = availableNodes.value.find((node) => node.id !== id)?.id || "";
    await loadDirectory("target");
    if (sourceNodeId.value && sourceDir.value.current === "") await loadDirectory("source");
  }
}

function toggleFile(path: string) {
  const next = new Set(sourceSelected.value);
  if (next.has(path)) next.delete(path); else next.add(path);
  sourceSelected.value = next;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = value / 1024, index = 0;
  while (size >= 1024 && index < units.length - 1) { size /= 1024; index++; }
  return `${size.toFixed(size >= 100 ? 0 : 1)} ${units[index]}`;
}

async function transfer() {
  if (!selectedFiles.value.length || !sourceNodeId.value || !targetNodeId.value || sourceNodeId.value === targetNodeId.value) return;
  busy.value = true;
  error.value = "";
  const signalController = new AbortController();
  controller.value = signalController;
  try {
    for (const file of selectedFiles.value) {
      const id = crypto.randomUUID().replaceAll("-", "");
      const targetPath = `${targetDir.value.current.replace(/[\\/]$/, "")}${targetDir.value.current.includes("\\") ? "\\" : "/"}${file.name}`;
      let offset = 0;
      progress.value = { name: file.name, transferred: 0, total: file.size };
      try {
        await selectResourceNode(sourceNodeId.value);
        if (file.size === 0) {
          await selectResourceNode(targetNodeId.value);
          await writeTransferChunk(targetPath, id, 0, 0, new Blob([]), true, signalController.signal);
        }
        while (offset < file.size) {
          const length = Math.min(chunkSize, file.size - offset);
          const response = await readTransferChunk(file.path, offset, length, signalController.signal);
          const chunk = await response.blob();
          await selectResourceNode(targetNodeId.value);
          await writeTransferChunk(targetPath, id, offset, file.size, chunk, offset + length === file.size, signalController.signal);
          offset += length;
          progress.value = { name: file.name, transferred: offset, total: file.size };
          if (offset < file.size) await selectResourceNode(sourceNodeId.value);
        }
      } catch (cause) {
        try { await selectResourceNode(targetNodeId.value); await cancelTransferFile(targetPath, id); } catch { /* no partial file may have been created */ }
        throw cause;
      }
    }
    sourceSelected.value = new Set();
    error.value = "文件已传输完成";
    await loadDirectory("target", targetDir.value.current);
  } catch (cause) {
    if (!signalController.signal.aborted) error.value = cause instanceof Error ? cause.message : "传输失败";
    else error.value = "传输已取消，未完成的临时文件已清理";
  } finally {
    controller.value = null;
    busy.value = false;
  }
}

function directoryTitle(dir: TransferDirectory) {
  return dir.current || "选择已添加的资源根目录";
}

onMounted(async () => {
  const online = availableNodes.value;
  sourceNodeId.value = online.find((node) => node.id === originalNodeId)?.id || online[0]?.id || "";
  targetNodeId.value = online.find((node) => node.id !== sourceNodeId.value)?.id || "";
  await loadDirectory("source");
  await loadDirectory("target");
});

onBeforeUnmount(() => {
  controller.value?.abort();
  if (originalNodeId && props.nodes.some((node) => node.id === originalNodeId)) void selectResourceNode(originalNodeId).catch(() => undefined);
});
</script>

<template>
  <section class="node-transfer-page">
    <header class="transfer-heading">
      <div><span class="transfer-eyebrow">NODE TO NODE</span><h1>文件传输</h1><p>在两个在线节点的已添加资源目录之间复制文件。传输不会扫描或建立索引。</p></div>
      <button type="button" class="transfer-close" @click="emit('close')"><X :size="16" />返回资源空间</button>
    </header>

    <div v-if="availableNodes.length < 2" class="transfer-notice"><WifiOff :size="18" />目前仅支持桌面资源节点；需要至少两个在线桌面节点才能开始传输。</div>
    <template v-else>
      <div class="transfer-columns">
        <section class="transfer-pane">
          <header class="transfer-pane-heading"><span class="transfer-step">01</span><div><strong>来源节点</strong><small>选择要发送的文件</small></div>
            <select :value="sourceNodeId" :disabled="busy" aria-label="来源节点" @change="switchNode('source', ($event.target as HTMLSelectElement).value)"><option v-for="node in availableNodes" :key="node.id" :value="node.id">{{ node.name }}</option></select>
          </header>
          <div class="transfer-path"><button v-if="sourceDir.parent" type="button" :disabled="busy || listing" aria-label="上一级" @click="loadDirectory('source', sourceDir.parent)"><ArrowLeft :size="15" /></button><span :title="directoryTitle(sourceDir)">{{ directoryTitle(sourceDir) }}</span><button type="button" :disabled="busy || listing" aria-label="刷新目录" @click="loadDirectory('source', sourceDir.current)"><RefreshCw :size="14" /></button></div>
          <div class="transfer-file-list">
            <button v-for="root in sourceDir.roots" :key="root.path" type="button" class="transfer-entry transfer-folder" :disabled="busy || listing" @click="loadDirectory('source', root.path)"><Folder :size="16" /><span>{{ root.name }}</span><ArrowRight :size="14" /></button>
            <button v-for="entry in sourceDir.entries.filter((item: TransferEntry) => item.dir)" :key="entry.path" type="button" class="transfer-entry transfer-folder" :disabled="busy || listing" @click="loadDirectory('source', entry.path)"><Folder :size="16" /><span>{{ entry.name }}</span><ArrowRight :size="14" /></button>
            <label v-for="entry in sourceFiles" :key="entry.path" class="transfer-entry transfer-file" :class="{ selected: sourceSelected.has(entry.path) }"><input type="checkbox" :checked="sourceSelected.has(entry.path)" :disabled="busy" @change="toggleFile(entry.path)" /><File :size="15" /><span>{{ entry.name }}</span><small>{{ formatBytes(entry.size) }}</small></label>
            <div v-if="!sourceDir.roots.length && !sourceDir.entries.length" class="transfer-empty">{{ sourceDir.current ? "此目录没有可传输的文件或子目录。" : "该节点尚未添加资源根目录。" }}</div>
          </div>
          <footer class="transfer-pane-footer">{{ selectedFiles.length }} 个文件 · {{ formatBytes(selectedFiles.reduce((sum, item) => sum + item.size, 0)) }}</footer>
        </section>

        <div class="transfer-direction"><ArrowDown :size="18" /><span>复制</span></div>

        <section class="transfer-pane">
          <header class="transfer-pane-heading"><span class="transfer-step">02</span><div><strong>目标节点</strong><small>选择文件保存位置</small></div>
            <select :value="targetNodeId" :disabled="busy" aria-label="目标节点" @change="switchNode('target', ($event.target as HTMLSelectElement).value)"><option v-for="node in availableNodes.filter((node) => node.id !== sourceNodeId)" :key="node.id" :value="node.id">{{ node.name }}</option></select>
          </header>
          <div class="transfer-path"><button v-if="targetDir.parent" type="button" :disabled="busy || listing" aria-label="上一级" @click="loadDirectory('target', targetDir.parent)"><ArrowLeft :size="15" /></button><span :title="directoryTitle(targetDir)">{{ directoryTitle(targetDir) }}</span><button type="button" :disabled="busy || listing" aria-label="刷新目录" @click="loadDirectory('target', targetDir.current)"><RefreshCw :size="14" /></button></div>
          <div class="transfer-file-list">
            <button v-for="root in targetDir.roots" :key="root.path" type="button" class="transfer-entry transfer-folder" :disabled="busy || listing" @click="loadDirectory('target', root.path)"><Folder :size="16" /><span>{{ root.name }}</span><ArrowRight :size="14" /></button>
            <button v-for="entry in targetDir.entries.filter((item: TransferEntry) => item.dir)" :key="entry.path" type="button" class="transfer-entry transfer-folder" :disabled="busy || listing" @click="loadDirectory('target', entry.path)"><Folder :size="16" /><span>{{ entry.name }}</span><ArrowRight :size="14" /></button>
            <div v-if="!targetDir.roots.length && !targetDir.entries.length" class="transfer-empty">{{ targetDir.current ? "此目录没有子目录，可将文件保存到当前目录。" : "该节点尚未添加资源根目录。" }}</div>
          </div>
          <footer class="transfer-pane-footer">文件将使用原文件名写入此目录，不会覆盖同名文件。</footer>
        </section>
      </div>

      <div class="transfer-actions">
        <p v-if="error" class="transfer-message" :class="{ success: error === '文件已传输完成' || error.includes('已取消') }" role="status">{{ error }}</p>
        <div v-if="busy" class="transfer-progress"><div><span>{{ progress.name }}</span><strong>{{ formatBytes(progress.transferred) }} / {{ formatBytes(progress.total) }}</strong></div><progress :value="percent" max="100" /></div>
        <button v-if="busy" type="button" class="button button-secondary" @click="controller?.abort()"><X :size="15" />取消传输</button>
        <button v-else type="button" class="transfer-start" :disabled="!selectedFiles.length || !targetDir.current || sourceNodeId === targetNodeId || listing" @click="transfer"><LoaderCircle v-if="listing" :size="16" class="transfer-spin" /><Check v-else :size="16" />传输 {{ selectedFiles.length }} 个文件</button>
      </div>
      <p class="transfer-footnote"><File :size="13" />仅复制原文件内容。目标文件不会加入资源索引，也不会触发缩略图或媒体处理。</p>
    </template>
  </section>
</template>

<style scoped>
.node-transfer-page { width: min(1240px, 100%); margin: 0 auto; padding: 20px 0 36px; color: var(--text, #24332a); }
.transfer-heading { display:flex; align-items:flex-start; justify-content:space-between; gap:20px; margin-bottom:26px; }
.transfer-eyebrow { color:#758579; font-size:10px; font-weight:700; letter-spacing:.14em; }
.transfer-heading h1 { margin:7px 0 5px; font-size:26px; letter-spacing:-.04em; }
.transfer-heading p { margin:0; color:#77837a; font-size:13px; }
.transfer-close { display:flex; align-items:center; gap:7px; padding:9px 11px; border:1px solid #e1e7e0; border-radius:8px; background:#fff; color:#536156; cursor:pointer; font-size:12px; }
.transfer-columns { display:grid; grid-template-columns:minmax(0,1fr) 46px minmax(0,1fr); align-items:stretch; gap:16px; }
.transfer-pane { display:flex; min-width:0; min-height:410px; flex-direction:column; border:1px solid #e2e8e1; border-radius:12px; background:#fff; box-shadow:0 4px 18px #21351f08; overflow:hidden; }
.transfer-pane-heading { display:flex; align-items:center; gap:11px; padding:17px 17px 14px; border-bottom:1px solid #edf0ec; }
.transfer-step { display:grid; width:31px; height:31px; place-items:center; border-radius:9px; background:#edf4ed; color:#48734e; font-size:10px; font-weight:700; }
.transfer-pane-heading div { display:grid; flex:1; gap:3px; }
.transfer-pane-heading strong { font-size:13px; }
.transfer-pane-heading small { color:#859087; font-size:11px; }
.transfer-pane-heading select { max-width:170px; padding:7px 25px 7px 9px; border:1px solid #e3e8e2; border-radius:7px; background:white; color:#435248; font-size:11px; }
.transfer-path { display:flex; min-width:0; align-items:center; gap:7px; min-height:40px; padding:0 13px; border-bottom:1px solid #f0f2ef; color:#79847b; font-size:10px; }
.transfer-path span { flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.transfer-path button { display:grid; width:26px; height:26px; place-items:center; border:0; border-radius:6px; background:transparent; color:#718076; cursor:pointer; }
.transfer-path button:hover { background:#f2f5f1; }
.transfer-file-list { flex:1; max-height:420px; min-height:250px; overflow:auto; padding:6px; }
.transfer-entry { display:flex; width:100%; min-height:39px; align-items:center; gap:9px; padding:0 9px; border:0; border-radius:7px; background:transparent; color:#57645a; text-align:left; font-size:12px; }
.transfer-entry span { flex:1; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.transfer-folder { cursor:pointer; }
.transfer-folder svg:first-child { color:#9b8b5a; }
.transfer-folder svg:last-child { color:#a7afa8; }
.transfer-folder:hover { background:#f5f7f4; }
.transfer-file { cursor:pointer; }
.transfer-file:hover,.transfer-file.selected { background:#f0f5ef; }
.transfer-file input { accent-color:#56815d; }
.transfer-file svg { color:#78867b; }
.transfer-file small { color:#929b94; font-size:10px; }
.transfer-empty { padding:35px 10px; color:#929a93; text-align:center; font-size:11px; }
.transfer-pane-footer { min-height:39px; padding:11px 14px; border-top:1px solid #edf0ec; color:#879189; font-size:10px; }
.transfer-direction { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:6px; color:#859287; font-size:10px; }
.transfer-direction svg { display:grid; width:36px; height:36px; box-sizing:content-box; padding:9px; border-radius:50%; background:#edf3ec; color:#57805b; }
.transfer-actions { display:flex; align-items:center; gap:14px; min-height:62px; margin-top:16px; padding:10px 14px; border:1px solid #e5eae3; border-radius:10px; background:#fff; }
.transfer-message { flex:1; margin:0; color:#a0443b; font-size:12px; }
.transfer-message.success { color:#4d7951; }
.transfer-progress { flex:1; display:grid; gap:6px; min-width:0; }
.transfer-progress div { display:flex; justify-content:space-between; gap:12px; font-size:11px; }
.transfer-progress span { overflow:hidden; color:#536156; text-overflow:ellipsis; white-space:nowrap; }
.transfer-progress strong { flex-shrink:0; color:#79847b; font-size:10px; }
.transfer-progress progress { width:100%; height:6px; accent-color:#56815d; }
.transfer-start { display:flex; align-items:center; gap:8px; min-height:39px; margin-left:auto; padding:0 15px; border:0; border-radius:8px; background:#47764e; color:white; cursor:pointer; font-size:12px; font-weight:600; }
.transfer-start:hover:not(:disabled) { background:#3b6842; }
.transfer-start:disabled { background:#b9c4ba; cursor:not-allowed; }
.transfer-footnote { display:flex; align-items:center; gap:7px; margin:12px 2px 0; color:#849086; font-size:10px; }
.transfer-notice { display:flex; min-height:190px; align-items:center; justify-content:center; gap:9px; border:1px dashed #dce3db; border-radius:12px; color:#77837a; font-size:13px; }
.transfer-spin { animation:spin .9s linear infinite; }
@keyframes spin { to { transform:rotate(360deg); } }
@media (max-width: 760px) { .node-transfer-page { padding-top:12px; } .transfer-heading h1 { font-size:22px; } .transfer-columns { grid-template-columns:1fr; gap:10px; } .transfer-pane { min-height:300px; } .transfer-file-list { min-height:200px; max-height:310px; } .transfer-direction { flex-direction:row; min-height:22px; } .transfer-direction svg { width:20px; height:20px; padding:5px; transform:rotate(-90deg); } .transfer-actions { flex-wrap:wrap; } .transfer-message,.transfer-progress { flex-basis:100%; } .transfer-close { font-size:0; } }
</style>
