<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { FullScreen, RefreshLeft, RefreshRight, ZoomIn, ZoomOut } from '@element-plus/icons-vue'

interface ImagePreviewDialogProps {
  /** Complete image URL or data URI passed to the viewer. */
  src: string
  /** Accessible image description. */
  alt?: string
  /** Dialog title shown above the image. */
  title?: string
  /** Optional thumbnail class applied to the default trigger image. */
  thumbnailClass?: string
}

const { src, alt = '图片预览', title = '图片预览', thumbnailClass = '' } = defineProps<ImagePreviewDialogProps>()
const visible = ref(false)
const scale = ref(1)
const rotation = ref(0)
const offset = ref({ x: 0, y: 0 })
const dragging = ref(false)
const dragStart = ref({ x: 0, y: 0 })
const dragOrigin = ref({ x: 0, y: 0 })

const imageStyle = computed(() => ({
  transform: `translate(${offset.value.x}px, ${offset.value.y}px) rotate(${rotation.value}deg) scale(${scale.value})`,
}))

function open() {
  visible.value = true
  reset()
}

function reset() {
  scale.value = 1
  rotation.value = 0
  offset.value = { x: 0, y: 0 }
}

function zoom(delta: number) {
  scale.value = Math.min(8, Math.max(0.2, Number((scale.value + delta).toFixed(2))))
}

function rotate(delta: number) {
  rotation.value = (rotation.value + delta + 360) % 360
}

function startDrag(event: PointerEvent) {
  if (event.button !== 0) return
  dragging.value = true
  dragStart.value = { x: event.clientX, y: event.clientY }
  dragOrigin.value = { ...offset.value }
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
}

function moveDrag(event: PointerEvent) {
  if (!dragging.value) return
  offset.value = {
    x: dragOrigin.value.x + event.clientX - dragStart.value.x,
    y: dragOrigin.value.y + event.clientY - dragStart.value.y,
  }
}

function stopDrag() {
  dragging.value = false
}

function handleWheel(event: WheelEvent) {
  zoom(event.deltaY < 0 ? 0.15 : -0.15)
}

function handleKeydown(event: KeyboardEvent) {
  if (!visible.value) return
  if (event.key === 'Escape') visible.value = false
  if (event.key === '+' || event.key === '=') zoom(0.15)
  if (event.key === '-') zoom(-0.15)
  if (event.key.toLowerCase() === 'r') rotate(90)
  if (event.key.toLowerCase() === 'f') reset()
}

watch(visible, async (isVisible) => {
  if (isVisible) {
    await nextTick()
    window.addEventListener('keydown', handleKeydown)
  } else {
    window.removeEventListener('keydown', handleKeydown)
  }
})

onBeforeUnmount(() => window.removeEventListener('keydown', handleKeydown))
</script>

<template>
  <span class="image-preview-trigger" role="button" tabindex="0" :aria-label="`查看${alt}`" @click="open" @keydown.enter.prevent="open" @keydown.space.prevent="open">
    <slot><img :src="src" :alt="alt" :class="thumbnailClass" /></slot>
  </span>

  <el-dialog v-model="visible" class="image-preview-dialog" width="min(1100px, 94vw)" top="4vh" append-to-body destroy-on-close :show-close="true">
    <template #header><div class="image-preview-header"><strong>{{ title }}</strong><span>{{ Math.round(scale * 100) }}%</span></div></template>
    <div class="image-preview-stage" @wheel.prevent="handleWheel" @pointerdown="startDrag" @pointermove="moveDrag" @pointerup="stopDrag" @pointercancel="stopDrag" @pointerleave="stopDrag">
      <img :src="src" :alt="alt" :style="imageStyle" :class="{ 'is-dragging': dragging }" draggable="false" />
    </div>
    <template #footer>
      <div class="image-preview-toolbar">
        <el-tooltip content="缩小"><el-button :icon="ZoomOut" aria-label="缩小" @click="zoom(-0.2)" /></el-tooltip>
        <el-tooltip content="放大"><el-button :icon="ZoomIn" aria-label="放大" @click="zoom(0.2)" /></el-tooltip>
        <el-tooltip content="顺时针旋转"><el-button :icon="RefreshRight" aria-label="顺时针旋转" @click="rotate(90)" /></el-tooltip>
        <el-tooltip content="逆时针旋转"><el-button :icon="RefreshLeft" aria-label="逆时针旋转" @click="rotate(-90)" /></el-tooltip>
        <el-tooltip content="适应窗口"><el-button :icon="FullScreen" aria-label="适应窗口" @click="reset" /></el-tooltip>
        <el-button @click="visible = false">关闭</el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.image-preview-trigger { display: inline-flex; max-width: 100%; cursor: zoom-in; outline: none; }
.image-preview-trigger:focus-visible { box-shadow: 0 0 0 2px var(--hongfen-primary-selection); }
.image-preview-dialog :deep(.el-dialog__body) { padding: 0 14px; }
.image-preview-header { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.image-preview-header span { color: var(--hongfen-text-muted); font: 600 12px/1 var(--hongfen-font-mono); }
.image-preview-stage { display: flex; width: 100%; height: min(72vh, 720px); align-items: center; justify-content: center; overflow: hidden; background: var(--hongfen-surface-muted); cursor: grab; touch-action: none; }
.image-preview-stage:active { cursor: grabbing; }
.image-preview-stage img { display: block; max-width: 100%; max-height: 100%; user-select: none; transform-origin: center; transition: transform 120ms ease-out; }
.image-preview-stage img.is-dragging { transition: none; }
.image-preview-toolbar { display: flex; align-items: center; justify-content: flex-end; gap: 6px; }
@media (max-width: 640px) { .image-preview-stage { height: 62vh; } .image-preview-toolbar { flex-wrap: wrap; } }
</style>
