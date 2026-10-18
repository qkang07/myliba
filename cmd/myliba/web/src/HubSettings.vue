<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";
import { Check, LoaderCircle, Pencil, Plus, Trash2, Wifi, WifiOff, X } from "@lucide/vue";
import { deleteHubProfile, getHubProfiles, saveHubProfile, selectHubProfile, type HubProfile, type HubProfilesView } from "./api";

const open = ref(false);
const busy = ref(false);
const error = ref("");
const view = ref<HubProfilesView>({ profiles: [], activeId: "", status: "disabled" });
const editingID = ref("");
const name = ref("");
const url = ref("");
const token = ref("");
let timer: ReturnType<typeof setInterval> | undefined;

const activeName = () => view.value.profiles.find(profile => profile.id === view.value.activeId)?.name || "未连接";
const statusText = () => ({ disabled: "未连接", connecting: "连接中", connected: "已连接", disconnected: "连接失败，正在重试" }[view.value.status]);

async function refresh() {
  try { view.value = await getHubProfiles(); }
  catch (cause) { if (open.value) error.value = cause instanceof Error ? cause.message : "读取服务端配置失败"; }
}

function create() {
  editingID.value = "";
  name.value = "";
  url.value = "";
  token.value = "";
  error.value = "";
}

function edit(profile: HubProfile) {
  editingID.value = profile.id;
  name.value = profile.name;
  url.value = profile.url;
  token.value = "";
  error.value = "";
}

async function save() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    view.value = await saveHubProfile({ id: editingID.value || undefined, name: name.value.trim(), url: url.value.trim(), token: token.value.trim() });
    create();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "保存失败"; }
  finally { busy.value = false; }
}

async function select(id: string) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try { view.value = await selectHubProfile(id); }
  catch (cause) { error.value = cause instanceof Error ? cause.message : "切换失败"; }
  finally { busy.value = false; }
}

async function remove(id: string) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    view.value = await deleteHubProfile(id);
    if (editingID.value === id) create();
  } catch (cause) { error.value = cause instanceof Error ? cause.message : "删除失败"; }
  finally { busy.value = false; }
}

onMounted(() => {
  void refresh();
  timer = setInterval(() => void refresh(), 3000);
});
onBeforeUnmount(() => { if (timer) clearInterval(timer); });
</script>

<template>
  <button class="button button-secondary hub-settings-toggle" type="button" @click="open = true; refresh()">
    <Wifi v-if="view.status === 'connected'" :size="14" /><WifiOff v-else :size="14" />服务端 · {{ activeName() }}
  </button>
  <Teleport to="body">
  <div v-if="open" class="resource-preview-backdrop" @click.self="open = false">
    <section class="hub-settings-dialog" role="dialog" aria-modal="true" aria-label="服务端连接">
      <header class="hub-settings-header">
        <div><strong>服务端连接</strong><small>本机索引保存在此设备。选择一个 Hub 供远程访问。</small></div>
        <button class="quiet-button directory-close" type="button" aria-label="关闭" @click="open = false"><X :size="18" /></button>
      </header>
      <div class="hub-settings-body">
        <p class="hub-current">当前：<strong>{{ activeName() }}</strong> · {{ statusText() }}</p>
        <div v-for="profile in view.profiles" :key="profile.id" class="hub-profile-row">
          <div><strong>{{ profile.name }}</strong><small>{{ profile.url }}</small></div>
          <button class="button button-secondary" type="button" :disabled="busy || profile.id === view.activeId" @click="select(profile.id)"><Check v-if="profile.id === view.activeId" :size="13" />{{ profile.id === view.activeId ? "当前" : "连接" }}</button>
          <button class="quiet-button" type="button" :aria-label="`编辑 ${profile.name}`" @click="edit(profile)"><Pencil :size="15" /></button>
          <button class="quiet-button" type="button" :aria-label="`删除 ${profile.name}`" :disabled="busy" @click="remove(profile.id)"><Trash2 :size="15" /></button>
        </div>
        <button v-if="view.activeId" class="hub-disconnect" type="button" :disabled="busy" @click="select('')">断开服务端</button>
        <form class="hub-settings-form" @submit.prevent="save">
          <h3>{{ editingID ? "编辑服务端" : "添加服务端" }}</h3>
          <label>名称<input v-model="name" maxlength="80" required placeholder="例如：我的 VPS" /></label>
          <label>服务端地址<input v-model="url" type="url" maxlength="2048" required placeholder="wss://example.com/node" autocomplete="url" /></label>
          <label>节点密钥<input v-model="token" type="password" maxlength="4096" :required="!editingID" :placeholder="editingID ? '留空则保持原密钥' : '输入 VPS 上配置的节点密钥'" autocomplete="new-password" /></label>
          <p>密钥保存在本机资源库，不会在页面中回显。新服务端保存后会立即连接。</p>
          <div class="hub-form-actions"><button v-if="editingID" class="button button-secondary" type="button" @click="create">取消编辑</button><button class="button button-primary" type="submit" :disabled="busy"><LoaderCircle v-if="busy" class="spin" :size="14" /><Plus v-else :size="14" />{{ editingID ? "保存" : "添加并连接" }}</button></div>
        </form>
        <p v-if="error" class="hub-settings-error" role="alert">{{ error }}</p>
      </div>
    </section>
  </div>
  </Teleport>
</template>
