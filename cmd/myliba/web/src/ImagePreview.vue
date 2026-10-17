<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { ChevronLeft, ChevronRight, Download, ExternalLink, LoaderCircle, Minus, Plus, RotateCcw, X } from "@lucide/vue";

const props = defineProps<{ src: string; name: string; position?: number; total?: number; canPrevious?: boolean; canNext?: boolean; loadingNext?: boolean }>();
const emit = defineEmits<{ close: []; download: []; details: []; previous: []; next: []; error: [] }>();
const dialog = ref<HTMLElement | null>(null);
const stage = ref<HTMLElement | null>(null);
const image = ref<HTMLImageElement | null>(null);
const closeButton = ref<HTMLButtonElement | null>(null);
const zoom = ref(1);
const panX = ref(0);
const panY = ref(0);
const dragging = ref(false);
const zoomLabel = computed(() => Math.round(zoom.value * 100) + "%");
const imageStyle = computed(() => ({
  transform: "translate3d(" + panX.value + "px, " + panY.value + "px, 0) scale(" + zoom.value + ")",
}));
let wheelDelta = 0;
let drag: { pointerId: number; x: number; y: number; panX: number; panY: number } | null = null;
let backdropPointerId: number | null = null;
let previousFocus: HTMLElement | null = null;

function clampPan(x: number, y: number, scale = zoom.value) {
  const viewport = stage.value;
  const picture = image.value;
  if (!viewport || !picture) return { x: 0, y: 0 };
  const maxX = Math.max(0, (picture.offsetWidth * scale - viewport.clientWidth) / 2);
  const maxY = Math.max(0, (picture.offsetHeight * scale - viewport.clientHeight) / 2);
  return {
    x: Math.max(-maxX, Math.min(maxX, x)),
    y: Math.max(-maxY, Math.min(maxY, y)),
  };
}
function zoomAt(value: number, clientX?: number, clientY?: number) {
  const next = Math.max(1, Math.min(4, value));
  if (next === zoom.value) return;
  const rect = stage.value?.getBoundingClientRect();
  if (!rect || next === 1) {
    zoom.value = next;
    panX.value = 0;
    panY.value = 0;
    return;
  }
  const x = (clientX ?? rect.left + rect.width / 2) - rect.left - rect.width / 2;
  const y = (clientY ?? rect.top + rect.height / 2) - rect.top - rect.height / 2;
  const ratio = next / zoom.value;
  const pan = clampPan(x - (x - panX.value) * ratio, y - (y - panY.value) * ratio, next);
  zoom.value = next;
  panX.value = pan.x;
  panY.value = pan.y;
}
function resetZoom() {
  zoom.value = 1;
  panX.value = 0;
  panY.value = 0;
  wheelDelta = 0;
}
function onWheel(event: WheelEvent) {
  if (event.ctrlKey || event.metaKey) return;
  event.preventDefault();
  const pixels = event.deltaY * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? stage.value?.clientHeight || 1 : 1);
  if (!pixels) return;
  const step = Math.sign(pixels) * Math.min(Math.abs(pixels), 50);
  wheelDelta = Math.sign(wheelDelta) === Math.sign(step) ? wheelDelta + step : step;
  if (Math.abs(wheelDelta) < 50) return;
  zoomAt(zoom.value + (wheelDelta < 0 ? 0.25 : -0.25), event.clientX, event.clientY);
  wheelDelta = 0;
}
function onDoubleClick(event: MouseEvent) {
  if (zoom.value > 1) resetZoom();
  else zoomAt(2, event.clientX, event.clientY);
}
function onPointerDown(event: PointerEvent) {
  if (event.button !== 0) return;
  if (event.target === stage.value) {
    backdropPointerId = event.pointerId;
    return;
  }
  if (zoom.value <= 1 || event.target !== image.value || !stage.value) return;
  drag = { pointerId: event.pointerId, x: event.clientX, y: event.clientY, panX: panX.value, panY: panY.value };
  dragging.value = true;
  stage.value.setPointerCapture(event.pointerId);
  event.preventDefault();
}
function onPointerMove(event: PointerEvent) {
  if (!drag || drag.pointerId !== event.pointerId) return;
  const pan = clampPan(drag.panX + event.clientX - drag.x, drag.panY + event.clientY - drag.y);
  panX.value = pan.x;
  panY.value = pan.y;
}
function onPointerUp(event: PointerEvent) {
  if (drag?.pointerId === event.pointerId) {
    drag = null;
    dragging.value = false;
    if (stage.value?.hasPointerCapture(event.pointerId)) stage.value.releasePointerCapture(event.pointerId);
    return;
  }
  if (backdropPointerId === event.pointerId) {
    backdropPointerId = null;
    if (event.target === stage.value) emit("close");
  }
}
function onPointerCancel(event: PointerEvent) {
  if (backdropPointerId === event.pointerId) backdropPointerId = null;
  if (drag?.pointerId === event.pointerId) {
    drag = null;
    dragging.value = false;
  }
}
function onKeyDown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    emit("close");
    return;
  }
  if (event.key !== "Tab" || !dialog.value) return;
  const buttons = Array.from(dialog.value.querySelectorAll<HTMLButtonElement>("button:not(:disabled)"));
  if (!buttons.length) return;
  const first = buttons[0];
  const last = buttons[buttons.length - 1];
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}
onMounted(() => {
  previousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  document.body.classList.add("image-preview-open");
  document.addEventListener("keydown", onKeyDown);
  void nextTick(() => closeButton.value?.focus());
});
onBeforeUnmount(() => {
  document.removeEventListener("keydown", onKeyDown);
  document.body.classList.remove("image-preview-open");
  if (previousFocus?.isConnected) previousFocus.focus();
});
</script>

<template>
  <Teleport to="body">
    <div ref="dialog" class="image-lightbox" role="dialog" aria-modal="true" :aria-label="'图片预览：' + props.name">
      <header class="image-lightbox-header">
        <span class="image-lightbox-name" :title="props.name">{{ props.name }}</span>
        <span v-if="props.total" class="image-lightbox-position">{{ props.position }} / {{ props.total }}</span>
        <button v-if="props.total" type="button" class="image-lightbox-details" @click="emit('details')"><ExternalLink :size="16" />详情</button>
        <button type="button" aria-label="下载原图" title="下载原图" @click="emit('download')"><Download :size="18" /></button>
        <button ref="closeButton" type="button" aria-label="关闭图片预览" title="关闭预览" @click="emit('close')"><X :size="20" /></button>
      </header>
      <div ref="stage" class="image-lightbox-stage" :class="{ 'is-pannable': zoom > 1, 'is-dragging': dragging }" @wheel="onWheel" @pointerdown="onPointerDown" @pointermove="onPointerMove" @pointerup="onPointerUp" @pointercancel="onPointerCancel">
        <button v-if="props.total" type="button" class="image-lightbox-navigation image-lightbox-previous" aria-label="上一个文件" :disabled="!props.canPrevious" @click="emit('previous')"><ChevronLeft :size="28" /></button>
        <img ref="image" :src="props.src" :alt="props.name" :style="imageStyle" draggable="false" @dblclick.stop="onDoubleClick" @error="emit('error')" />
        <button v-if="props.total" type="button" class="image-lightbox-navigation image-lightbox-next" aria-label="下一个文件" :disabled="!props.canNext || props.loadingNext" @click="emit('next')"><LoaderCircle v-if="props.loadingNext" class="spin" :size="22" /><ChevronRight v-else :size="28" /></button>
      </div>
      <footer class="image-lightbox-footer">
        <div class="image-lightbox-controls">
          <button type="button" aria-label="缩小图片" title="缩小" :disabled="zoom <= 1" @click="zoomAt(zoom - 0.25)"><Minus :size="17" /></button>
          <span aria-live="polite">{{ zoomLabel }}</span>
          <button type="button" aria-label="放大图片" title="放大" :disabled="zoom >= 4" @click="zoomAt(zoom + 0.25)"><Plus :size="17" /></button>
          <span class="image-lightbox-divider"></span>
          <button type="button" class="image-lightbox-reset" aria-label="适应窗口" title="适应窗口" @click="resetZoom"><RotateCcw :size="16" />适应窗口</button>
        </div>
        <small>滚轮缩放 · 放大后拖动 · 双击切换<span v-if="props.total"> · ← → 切换文件</span> · Esc 关闭</small>
      </footer>
    </div>
  </Teleport>
</template>

<style>
body.image-preview-open { overflow: hidden; }
.image-lightbox { position: fixed; z-index: 100; inset: 0; display: flex; flex-direction: column; background: rgb(12 16 14 / 96%); color: #fff; }
.image-lightbox-header { display: flex; min-height: 58px; flex: none; align-items: center; gap: 8px; padding: 0 18px; background: rgb(0 0 0 / 28%); }
.image-lightbox-name { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
.image-lightbox-position { flex: none; min-width: 56px; text-align: center; color: rgb(255 255 255 / 75%); font-size: 12px; font-variant-numeric: tabular-nums; }
.image-lightbox-header .image-lightbox-details { padding: 0 10px; font-size: 12px; }
.image-lightbox-header button, .image-lightbox-controls button { display: inline-flex; min-width: 36px; height: 36px; align-items: center; justify-content: center; gap: 7px; border: 0; border-radius: 7px; background: transparent; color: #fff; cursor: pointer; }
.image-lightbox-header button:hover, .image-lightbox-controls button:hover:not(:disabled) { background: rgb(255 255 255 / 15%); }
.image-lightbox-header button:focus-visible, .image-lightbox-controls button:focus-visible { outline: 2px solid #fff; outline-offset: 2px; }
.image-lightbox-controls button:disabled { opacity: .35; cursor: default; }
.image-lightbox-stage { position: relative; display: grid; min-height: 0; flex: 1; place-items: center; overflow: hidden; touch-action: none; }
.image-lightbox-stage .image-lightbox-navigation { position: absolute; z-index: 1; top: 50%; display: grid; width: 44px; height: 56px; place-items: center; transform: translateY(-50%); border: 0; border-radius: 8px; background: rgb(0 0 0 / 48%); color: #fff; cursor: pointer; }
.image-lightbox-stage .image-lightbox-navigation:hover:not(:disabled) { background: rgb(0 0 0 / 72%); }
.image-lightbox-stage .image-lightbox-navigation:focus-visible { outline: 2px solid #fff; outline-offset: 2px; }
.image-lightbox-stage .image-lightbox-navigation:disabled { opacity: .35; cursor: default; }
.image-lightbox-previous { left: 16px; }
.image-lightbox-next { right: 16px; }
.image-lightbox-stage img { display: block; max-width: calc(100vw - 48px); max-height: calc(100dvh - 166px); object-fit: contain; user-select: none; }
.image-lightbox-stage.is-pannable img { cursor: grab; }
.image-lightbox-stage.is-dragging img { cursor: grabbing; }
.image-lightbox-footer { display: flex; min-height: 72px; flex: none; flex-direction: column; align-items: center; justify-content: center; gap: 3px; padding: 8px 16px; background: rgb(0 0 0 / 28%); }
.image-lightbox-controls { display: flex; align-items: center; gap: 4px; }
.image-lightbox-controls > span:not(.image-lightbox-divider) { min-width: 48px; text-align: center; font-size: 12px; font-variant-numeric: tabular-nums; }
.image-lightbox-divider { width: 1px; height: 22px; margin: 0 5px; background: rgb(255 255 255 / 25%); }
.image-lightbox-controls .image-lightbox-reset { padding: 0 9px; font-size: 12px; }
.image-lightbox-footer small { color: rgb(255 255 255 / 58%); font-size: 10px; }
@media (max-width: 600px) {
  .image-lightbox-header { padding: 0 10px; }
  .image-lightbox-previous { left: 4px; }
  .image-lightbox-next { right: 4px; }
  .image-lightbox-stage .image-lightbox-navigation { width: 34px; height: 48px; }
  .image-lightbox-stage img { max-width: calc(100vw - 20px); max-height: calc(100dvh - 150px); }
  .image-lightbox-footer small { font-size: 9px; }
}
</style>

