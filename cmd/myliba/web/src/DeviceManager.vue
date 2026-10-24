<script setup lang="ts">
import { ref } from "vue";
import { ShieldCheck, Trash2, X } from "@lucide/vue";
import { getHubDevices, revokeHubDevice, setLegacyHubNodes, type HubDevicesView } from "./api";

const open = ref(false);
const busy = ref(false);
const error = ref("");
const view = ref<HubDevicesView>({ devices: [], legacyNodeAllowed: true });

async function show() {
  open.value = true;
  error.value = "";
  await refresh();
}

async function refresh() {
  try { view.value = await getHubDevices(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法读取设备授权"; }
}

async function revoke(id: string) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try { await revokeHubDevice(id); await refresh(); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法撤销设备"; }
  finally { busy.value = false; }
}

async function toggleLegacy() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try { view.value = await setLegacyHubNodes(!view.value.legacyNodeAllowed); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "无法更新旧密钥设置"; }
  finally { busy.value = false; }
}
</script>

<template>
  <button class="button button-secondary" type="button" @click="show"><ShieldCheck :size="14" />设备授权</button>
  <Teleport to="body">
    <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
      <section class="hub-settings-dialog" role="dialog" aria-modal="true" aria-label="设备授权">
        <header class="hub-settings-header">
          <div><strong>设备授权</strong><small>每台手机和电脑使用自己的凭据。撤销后，该设备的浏览会话和资源节点连接都会失效。</small></div>
          <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
        </header>
        <div class="hub-settings-body">
          <div v-for="device in view.devices" :key="device.id" class="hub-profile-row">
            <div><strong>{{ device.name }}</strong><small>{{ device.kind === "android" ? "手机" : device.kind === "storage" ? "存储节点" : "电脑" }} · {{ device.canBrowse ? "可浏览" : "不可浏览" }} · {{ device.canServe ? "可提供资源" : "不可提供资源" }} · {{ device.canCloud ? "可访问云层" : "无云层权限" }}{{ device.revokedAt ? " · 已撤销" : "" }}</small></div>
            <button v-if="!device.revokedAt" class="quiet-button" type="button" :disabled="busy" :aria-label="`撤销 ${device.name}`" @click="revoke(device.id)"><Trash2 :size="16" /></button>
          </div>
          <p v-if="!view.devices.length">尚无逐台授权的设备。在手机或桌面客户端连接 Hub 时输入一次主人密码即可添加。</p>
          <p v-if="view.legacyNodeAllowed">旧共享节点密钥仍可连接。所有旧设备完成授权后，请关闭它，确保逐台撤销生效。</p>
          <button v-if="view.legacyNodeAllowed" class="button button-secondary" type="button" :disabled="busy" @click="toggleLegacy">关闭旧共享密钥</button>
          <p v-if="error" class="hub-settings-error" role="alert">{{ error }}</p>
        </div>
      </section>
    </div>
  </Teleport>
</template>
