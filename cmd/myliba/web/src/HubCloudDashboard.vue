<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { HardDrive, RefreshCw, X } from "@lucide/vue";
import { getHubCloudOverview, removeHubCloudNode, type HubCloudOverview, type HubCloudNode } from "./api";

const open = ref(false);
const loading = ref(false);
const error = ref("");
const overview = ref<HubCloudOverview>({ enabled: false, nodes: [] });
let timer: ReturnType<typeof setInterval> | undefined;

async function refresh() {
  try { overview.value = await getHubCloudOverview(); error.value = ""; }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取 Hub 节点状态"; }
}
async function show() {
  open.value = true;
  loading.value = true;
  await refresh();
  loading.value = false;
}
async function removeNode(node: HubCloudNode) {
  if (!window.confirm(`移除离线节点 ${node.id} 的 Hub 登记？若它还有唯一的文件块副本，今后需要它重新上线才能恢复。此操作不会删除节点上的文件。`)) return;
  try { await removeHubCloudNode(node.id); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "移除节点失败"; }
}
function size(value?: number) {
  if (value === undefined) return "未知";
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let unit = -1;
  do { value /= 1024; unit++; } while (value >= 1024 && unit < units.length - 1);
  return `${value.toFixed(1)} ${units[unit]}`;
}
function time(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "未知" : date.toLocaleString();
}
watch(open, (value) => {
  if (timer) clearInterval(timer);
  timer = value ? setInterval(() => void refresh(), 10000) : undefined;
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>

<template>
  <button class="button button-secondary hub-cloud-toggle" type="button" title="Hub 节点管理" aria-label="Hub 节点管理" @click="show"><HardDrive :size="15" /><span>节点管理</span></button>
  <Teleport to="body">
    <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
      <section class="hub-settings-dialog hub-cloud-dialog" role="dialog" aria-modal="true" aria-label="Hub 节点管理">
        <header class="hub-settings-header">
          <div><strong>Hub 节点管理</strong><small>查看已登记节点及它们上报的存储状态</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body hub-cloud-body">
          <p v-if="error" class="form-error" role="alert">{{ error }}</p>
          <p v-if="loading">正在读取节点…</p>
          <p v-else-if="!overview.enabled">Hub 尚未启用云层。请在 Hub 配置 MYLIBA_CLOUD_TOKEN。</p>
          <template v-else>
            <div class="hub-cloud-summary">
              <div><strong>{{ overview.online ?? 0 }} / {{ overview.nodes.length }}</strong><span>在线 / 已登记节点</span></div>
              <div><strong>{{ size(overview.reportedBlockBytes ?? 0) }}</strong><span>在线节点副本占用 <small v-if="overview.unknownBlockBytesNodes">· {{ overview.unknownBlockBytesNodes }} 节点未知</small></span></div>
              <div><strong>{{ size(overview.knownAvailableBytes ?? 0) }}</strong><span>已知可用空间 <small v-if="overview.unknownCapacityNodes">· {{ overview.unknownCapacityNodes }} 节点未知</small></span></div>
              <div><strong>{{ size(overview.knownCapacityBytes ?? 0) }}</strong><span>已知总容量</span></div>
              <div><strong>{{ overview.reportedBlockCount ?? 0 }}</strong><span>在线块副本数</span></div>
              <div><strong>{{ overview.catalogRecords ?? 0 }}</strong><span>Hub 索引记录数</span></div>
            </div>
            <p class="hub-cloud-note">空间与块数按在线节点上报相加；同一块的多个备份会分别计入。S3/WebDAV/OpenList 的容量和占用暂不统计，离线节点也不计入空间汇总。</p>
            <div class="cloud-heading"><strong>节点（{{ overview.nodes.length }}）</strong><button class="quiet-button" type="button" @click="refresh"><RefreshCw :size="15" />刷新</button></div>
            <p v-if="!overview.nodes.length">还没有节点向 Hub 注册。</p>
            <div v-for="node in overview.nodes" :key="node.id" class="hub-cloud-node">
              <div class="hub-cloud-node-heading"><strong>{{ node.role === "client" ? "客户端" : "存储节点" }} · {{ node.id.slice(0, 12) }}</strong><span :class="['hub-cloud-status', { offline: !node.online }]">{{ node.online ? "在线" : "离线" }}</span></div>
              <small>{{ node.backend === "s3" ? "S3 / OSS" : node.backend === "openlist" ? "OpenList / 云盘" : node.backend === "webdav" ? "WebDAV / 云盘" : "本地磁盘" }} · {{ node.relayed ? (node.endpoint ? "经存储节点中继" : "中继未连接") : "直连" }} · 最近上报 {{ time(node.seenAt) }}</small>
              <small class="hub-cloud-endpoint">{{ node.endpoint || "当前无访问地址" }}</small>
              <div class="hub-cloud-node-metrics"><span>{{ node.statsReady ? node.blockCount : "未知" }} 块</span><span>副本占用 {{ size(node.blockBytes) }}</span><span>可用 {{ size(node.availableBytes) }} / {{ size(node.capacityBytes) }}</span><span>{{ node.statsReady ? node.catalogCount : "未知" }} 条索引</span></div>
              <button v-if="!node.online" class="quiet-button" type="button" :aria-label="`移除离线节点 ${node.id}`" @click="removeNode(node)">移除离线记录</button>
            </div>
          </template>
        </div>
      </section>
    </div>
  </Teleport>
</template>
