<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from "vue";
import { createVideoPreview, getVideoPreview, getVideoPreviewFile, type PreviewClip, type PreviewSpec } from "./api";
const props = defineProps<{ id: string; duration: number }>();
const clips = ref<PreviewClip[]>([{ start: 0, end: 5 }]);
const quality = ref<PreviewSpec["quality"]>("720p");
const status = ref("missing");
const message = ref("");
const url = ref("");
let revision = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
function clearURL() { if (url.value) URL.revokeObjectURL(url.value); url.value = ""; }
async function refresh(current = revision, loadSpec = false) {
  try {
    const result = await getVideoPreview(props.id);
    if (current !== revision) return;
    status.value = result.status;
    message.value = result.error || "";
    if (result.spec && loadSpec) { clips.value = result.spec.clips; quality.value = result.spec.quality; }
    if (result.status === "processing") timer = setTimeout(() => void refresh(current, true), 2000);
    else if (result.status === "ready") {
      const response = await getVideoPreviewFile(props.id);
      if (!response.ok) throw new Error("无法读取预览短视频");
      const next = URL.createObjectURL(await response.blob());
      if (current !== revision) URL.revokeObjectURL(next); else { clearURL(); url.value = next; }
    }
  } catch (e) { if (current === revision) message.value = e instanceof Error ? e.message : "无法读取预览"; }
}
async function generate() {
  const total = clips.value.reduce((sum, c) => sum + c.end - c.start, 0);
  if (!props.duration || clips.value.some(c => !Number.isFinite(c.start) || !Number.isFinite(c.end) || c.start < 0 || c.end <= c.start || c.end > props.duration) || total > 120) { message.value = "请检查片段时间：结束时间须大于开始时间且不超过视频时长，总时长不超过 120 秒。"; return; }
  message.value = "";
  try { await createVideoPreview(props.id, { quality: quality.value, clips: clips.value }); status.value = "processing"; void refresh(revision, true); }
  catch (e) { message.value = e instanceof Error ? e.message : "生成失败"; }
}
function addClip() { clips.value.push({ start: 0, end: Math.min(5, props.duration) }); }
function move(index: number, offset: number) { const other = index + offset; if (other < 0 || other >= clips.value.length) return; [clips.value[index], clips.value[other]] = [clips.value[other]!, clips.value[index]!]; }
watch(() => props.id, () => { revision++; clearTimeout(timer); clearURL(); clips.value = [{ start: 0, end: 5 }]; quality.value = "720p"; status.value = "missing"; message.value = ""; void refresh(revision, true); }, { immediate: true });
watch(() => props.duration, value => { if (value > 0 && status.value === "missing" && clips.value.length === 1 && clips.value[0]?.end === 5) clips.value[0].end = Math.min(5, value); });
onBeforeUnmount(() => { revision++; clearTimeout(timer); clearURL(); });
</script>
<template>
  <section class="detail-panel preview-editor">
    <div class="detail-panel-heading"><div><h2>预览短视频</h2><p>按顺序拼接片段，生成不含声音的预览；总时长最多 120 秒。</p></div></div>
    <video v-if="url" :src="url" controls playsinline preload="metadata" class="preview-editor-video"></video>
    <p v-else>{{ status === 'processing' ? '正在生成预览…' : '尚未生成预览短视频' }}</p>
    <div class="preview-editor-options"><label>输出分辨率 <select v-model="quality" :disabled="status === 'processing'"><option value="360p">360p</option><option value="720p">720p</option><option value="1080p">1080p</option></select></label><span>保持原始画面比例，不放大原视频</span></div>
    <div v-for="(clip, i) in clips" :key="i" class="preview-editor-row">
      <strong>片段 {{ i + 1 }}</strong><label>开始（秒）<input v-model.number="clip.start" type="number" min="0" :max="duration" step="0.1" :disabled="status === 'processing'" /></label><label>结束（秒）<input v-model.number="clip.end" type="number" min="0" :max="duration" step="0.1" :disabled="status === 'processing'" /></label>
      <button type="button" class="quiet-button" :disabled="i === 0 || status === 'processing'" @click="move(i, -1)">上移</button><button type="button" class="quiet-button" :disabled="i === clips.length - 1 || status === 'processing'" @click="move(i, 1)">下移</button><button type="button" class="quiet-button" :disabled="clips.length === 1 || status === 'processing'" @click="clips.splice(i, 1)">删除</button>
    </div>
    <div class="preview-editor-actions"><button type="button" class="button button-secondary" :disabled="clips.length >= 10 || !duration || status === 'processing'" @click="addClip">添加片段</button><button type="button" class="button button-primary" :disabled="!duration || status === 'processing'" @click="generate">{{ status === 'processing' ? '生成中…' : url ? '重新生成' : '生成预览' }}</button></div>
    <p v-if="message" class="detail-error" role="alert">{{ message }}</p>
  </section>
</template>
<style scoped>
.preview-editor-video { width: 100%; max-height: 360px; background: #111; margin: 12px 0; }
.preview-editor-options,.preview-editor-row,.preview-editor-actions { display:flex; flex-wrap:wrap; align-items:end; gap:12px; margin-top:12px; }
.preview-editor-options label,.preview-editor-row label { display:flex; flex-direction:column; gap:4px; }
.preview-editor-row input { width:100px; }
</style>
