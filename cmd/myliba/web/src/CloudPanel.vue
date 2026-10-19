<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { Cloud, Download, LoaderCircle, RefreshCw, Upload, X } from "@lucide/vue";
import {
  getCloudFile, getCloudFiles, getCloudRecovery, getCloudVersions, importCloudRecovery,
  uploadResourceToCloud, type CloudFile, type CloudView, type Resource,
} from "./api";

const props = defineProps<{ resources: Resource[]; remote: boolean }>();
const open = ref(false);
const loading = ref(false);
const error = ref("");
const view = ref<CloudView>({ files: [], jobs: {}, nodeId: "", libraryId: "", peers: [], directEndpoint: "" });
const recoveryKey = ref("");
const importKey = ref("");
const activeDownload = ref("");
const historyChoices = ref<Record<string, boolean>>({});
const versions = ref<Record<string, CloudFile[]>>({});
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
  try {
    view.value = await getCloudFiles();
    for (const file of view.value.files) {
      if (file.sourceId && historyChoices.value[file.sourceId] === undefined) historyChoices.value[file.sourceId] = file.history;
    }
    error.value = "";
  }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取云层"; }
}
async function show() {
  open.value = true;
  loading.value = true;
  await refresh();
  loading.value = false;
}
async function upload(resource: Resource) {
  try { await uploadResourceToCloud(resource.id, historyChoices.value[resource.id] ?? false); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法上传文件"; }
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
function formatSize(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 ** 2) return `${Math.round(value / 1024)} KB`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MB`;
  return `${(value / 1024 ** 3).toFixed(1)} GB`;
}
watch(open, (value) => {
  if (timer) clearInterval(timer);
  if (value) timer = setInterval(() => void refresh(), 3000);
  else recoveryKey.value = "";
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>

<template>
  <button class="button button-secondary" type="button" @click="show"><Cloud :size="15" />云层</button>
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
          <p v-if="!remote && !view.directEndpoint">本客户端尚未配置直连公开地址；可主动上传到其他节点，但其他设备不能直接从本客户端取块。</p>
          <div class="cloud-heading"><strong>云层文件</strong><button class="quiet-button" type="button" @click="refresh"><RefreshCw :size="15" />刷新</button></div>
          <p v-if="loading">正在读取云层索引…</p>
          <p v-else-if="!view.files.length">云层里还没有可用文件。</p>
          <div v-for="file in view.files" :key="file.id" class="hub-profile-row">
            <div><strong>{{ file.name }}</strong><small>{{ formatSize(file.size) }} · {{ file.chunks }} 块 · 最少 {{ file.minReplicas }}/3 份在线副本 · {{ file.history ? "保留历史" : "无历史" }}</small></div>
            <button v-if="file.history" class="button button-secondary" type="button" @click="toggleVersions(file.id)">历史</button>
            <button class="button button-secondary" type="button" :disabled="!!activeDownload" @click="download(file.id, file.name)">
              <LoaderCircle v-if="activeDownload === file.id" class="spin" :size="14" /><Download v-else :size="14" />下载
            </button>
          </div>
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
              <div><strong>{{ resource.name }}</strong><small>{{ formatSize(resource.size) }} · {{ view.jobs[resource.id] || "仅本地" }}</small><label class="cloud-history-choice"><input v-model="historyChoices[resource.id]" type="checkbox" />保留后续历史版本</label></div>
              <button class="button button-secondary" type="button" :disabled="!resource.available || view.jobs[resource.id] === '上传中'" @click="upload(resource)">
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
