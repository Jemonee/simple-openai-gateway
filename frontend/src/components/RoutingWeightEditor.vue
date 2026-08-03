<script setup lang="ts">
import { computed, ref, useTemplateRef } from 'vue'
import type { RoutingWeightKey, RoutingWeightValues } from '@/types/routing'

interface RoutingWeightEditorProps {
  /** Four route-scoring percentages whose sum is always 100. */
  weights: RoutingWeightValues
  /** Minimum quality share retained while a divider or number field changes. */
  minimumQuality?: number
  /** Prevents pointer, keyboard, and number-field changes. */
  disabled?: boolean
}

const { weights, minimumQuality = 5, disabled = false } = defineProps<RoutingWeightEditorProps>()
const emit = defineEmits<{ 'update:weights': [value: RoutingWeightValues] }>()
const track = useTemplateRef<HTMLDivElement>('track')
const draggingBoundary = ref<number | null>(null)

const weightKeys: RoutingWeightKey[] = ['price', 'efficiency', 'quality', 'balance']
const labels: Record<RoutingWeightKey, string> = {
  price: '价格',
  efficiency: '效率',
  quality: '质量',
  balance: '均衡',
}
const boundaries = computed(() => [
  weights.price,
  weights.price + weights.efficiency,
  weights.price + weights.efficiency + weights.quality,
])

function minimumFor(key: RoutingWeightKey): number {
  return key === 'quality' ? minimumQuality : 0
}

function normalized(values: RoutingWeightValues): RoutingWeightValues {
  const next = { ...values }
  const total = weightKeys.reduce((sum, key) => sum + next[key], 0)
  if (total !== 100) next.balance += 100 - total
  return next
}

function updateField(key: RoutingWeightKey, value: number | undefined) {
  if (disabled || value === undefined || !Number.isFinite(value)) return
  const otherKeys = weightKeys.filter((item) => item !== key)
  const maximum = 100 - otherKeys.reduce((sum, item) => sum + minimumFor(item), 0)
  const selected = Math.min(Math.max(Math.round(value), minimumFor(key)), maximum)
  const next = { ...weights, [key]: selected }
  const available = 100 - selected - otherKeys.reduce((sum, item) => sum + minimumFor(item), 0)
  const currentExcess = otherKeys.map((item) => Math.max(weights[item] - minimumFor(item), 0))
  const excessTotal = currentExcess.reduce((sum, item) => sum + item, 0)

  otherKeys.forEach((item, index) => {
    const share = excessTotal > 0 ? currentExcess[index] / excessTotal : 1 / otherKeys.length
    next[item] = minimumFor(item) + Math.floor(available * share)
  })
  emit('update:weights', normalized(next))
}

function boundaryRange(index: number): { min: number; max: number } {
  if (index === 0) return { min: 0, max: boundaries.value[1] }
  if (index === 1) return { min: boundaries.value[0], max: boundaries.value[2] - minimumQuality }
  return { min: boundaries.value[1] + minimumQuality, max: 100 }
}

function updateBoundary(index: number, value: number) {
  if (disabled) return
  const range = boundaryRange(index)
  const boundary = Math.min(Math.max(Math.round(value), range.min), range.max)
  const next: RoutingWeightValues = { ...weights }
  if (index === 0) {
    next.price = boundary
    next.efficiency = boundaries.value[1] - boundary
  } else if (index === 1) {
    next.efficiency = boundary - boundaries.value[0]
    next.quality = boundaries.value[2] - boundary
  } else {
    next.quality = boundary - boundaries.value[1]
    next.balance = 100 - boundary
  }
  emit('update:weights', normalized(next))
}

function updateBoundaryFromPointer(index: number, event: PointerEvent) {
  const rect = track.value?.getBoundingClientRect()
  if (!rect?.width) return
  updateBoundary(index, ((event.clientX - rect.left) / rect.width) * 100)
}

function startDragging(index: number, event: PointerEvent) {
  if (disabled) return
  draggingBoundary.value = index
  ;(event.currentTarget as HTMLElement).setPointerCapture(event.pointerId)
  updateBoundaryFromPointer(index, event)
}

function continueDragging(index: number, event: PointerEvent) {
  if (draggingBoundary.value !== index) return
  updateBoundaryFromPointer(index, event)
}

function stopDragging(index: number, event: PointerEvent) {
  if (draggingBoundary.value !== index) return
  const target = event.currentTarget as HTMLElement
  if (target.hasPointerCapture(event.pointerId)) target.releasePointerCapture(event.pointerId)
  draggingBoundary.value = null
}

function handleBoundaryKey(index: number, event: KeyboardEvent) {
  if (disabled) return
  const range = boundaryRange(index)
  let next = boundaries.value[index]
  if (event.key === 'ArrowLeft' || event.key === 'ArrowDown') next -= 1
  else if (event.key === 'ArrowRight' || event.key === 'ArrowUp') next += 1
  else if (event.key === 'Home') next = range.min
  else if (event.key === 'End') next = range.max
  else return
  event.preventDefault()
  updateBoundary(index, next)
}

function boundaryLabel(index: number): string {
  const left = labels[weightKeys[index]]
  const right = labels[weightKeys[index + 1]]
  return `调整${left}与${right}占比，当前分隔位置 ${boundaries.value[index]}%`
}
</script>

<template>
  <div class="routing-weight-editor" :class="{ 'is-disabled': disabled }">
    <div ref="track" class="weight-track" aria-label="路由决策占比分段直线">
      <span class="weight-segment is-price" :style="{ width: `${weights.price}%` }" />
      <span class="weight-segment is-efficiency" :style="{ width: `${weights.efficiency}%` }" />
      <span class="weight-segment is-quality" :style="{ width: `${weights.quality}%` }" />
      <span class="weight-segment is-balance" :style="{ width: `${weights.balance}%` }" />
      <button
        v-for="(boundary, index) in boundaries"
        :key="index"
        class="divider-handle"
        type="button"
        role="slider"
        :style="{ left: `${boundary}%` }"
        :disabled="disabled"
        :title="boundaryLabel(index)"
        :aria-label="boundaryLabel(index)"
        :aria-valuemin="boundaryRange(index).min"
        :aria-valuemax="boundaryRange(index).max"
        :aria-valuenow="boundary"
        @pointerdown="startDragging(index, $event)"
        @pointermove="continueDragging(index, $event)"
        @pointerup="stopDragging(index, $event)"
        @pointercancel="stopDragging(index, $event)"
        @keydown="handleBoundaryKey(index, $event)"
      />
    </div>

    <div class="weight-fields">
      <label v-for="key in weightKeys" :key="key" :class="`is-${key}`">
        <span><i aria-hidden="true" />{{ labels[key] }}</span>
        <el-input-number
          :model-value="weights[key]"
          :min="minimumFor(key)"
          :max="100"
          :disabled="disabled"
          :precision="0"
          :step="1"
          controls-position="right"
          :aria-label="`${labels[key]}决策占比百分比`"
          @update:model-value="updateField(key, $event)"
        />
      </label>
    </div>
    <p class="weight-hint">拖动直线上的分隔点调整相邻占比，四项始终合计 100%。质量占比最低 {{ minimumQuality }}%。</p>
  </div>
</template>

<style scoped>
.routing-weight-editor { padding: 28px 22px 12px; }
.weight-track { position: relative; display: flex; height: 10px; margin: 10px 10px 30px; border-radius: 2px; background: var(--hongfen-surface-muted); }
.weight-segment { min-width: 0; height: 100%; transition: width 120ms ease; }
.weight-segment:first-child { border-radius: 2px 0 0 2px; }
.weight-segment:last-of-type { border-radius: 0 2px 2px 0; }
.is-price { --weight-color: var(--hongfen-primary); }
.is-efficiency { --weight-color: var(--hongfen-success); }
.is-quality { --weight-color: var(--hongfen-warning); }
.is-balance { --weight-color: var(--hongfen-danger); }
.weight-segment.is-price, .weight-fields .is-price i { background: var(--hongfen-primary); }
.weight-segment.is-efficiency, .weight-fields .is-efficiency i { background: var(--hongfen-success); }
.weight-segment.is-quality, .weight-fields .is-quality i { background: var(--hongfen-warning); }
.weight-segment.is-balance, .weight-fields .is-balance i { background: var(--hongfen-danger); }
.divider-handle { position: absolute; z-index: 1; top: 50%; width: 22px; height: 22px; padding: 0; border: 3px solid var(--hongfen-surface); border-radius: 50%; background: var(--hongfen-text); box-shadow: 0 0 0 1px var(--hongfen-border-strong); cursor: ew-resize; touch-action: none; transform: translate(-50%, -50%); }
.divider-handle:hover { background: var(--hongfen-primary); }
.divider-handle:focus-visible { outline: 3px solid color-mix(in srgb, var(--hongfen-primary) 28%, transparent); outline-offset: 3px; }
.weight-fields { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 16px; }
.weight-fields label { display: grid; min-width: 0; grid-template-columns: minmax(0, 1fr) 112px; align-items: center; gap: 10px; }
.weight-fields label > span { display: flex; align-items: center; gap: 7px; color: var(--hongfen-text); font-size: 12px; font-weight: 650; }
.weight-fields label i { width: 8px; height: 8px; flex: none; border-radius: 50%; }
.weight-fields :deep(.el-input-number) { width: 112px; }
.weight-hint { margin: 14px 0 0; color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.6; }
.is-disabled { opacity: 0.7; }
@media (prefers-reduced-motion: reduce) { .weight-segment { transition: none; } }
@media (max-width: 1080px) {
  .weight-fields { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 620px) {
  .routing-weight-editor { padding: 22px 16px 8px; }
  .weight-fields { grid-template-columns: 1fr; }
}
</style>
