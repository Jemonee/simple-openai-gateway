<script setup lang="ts">
import { computed, ref, type CSSProperties } from 'vue'
import { ArrowRight } from '@element-plus/icons-vue'
import type { RelayStepCategory, RelayStepLog } from '@/types/gateway'

interface RelayStepTimelineProps {
  /** Fine-grained stages captured for one relayed request. */
  steps?: RelayStepLog[]
  /** Whether the stage list is open when this timeline is first rendered. */
  defaultExpanded?: boolean
}

const props = withDefaults(defineProps<RelayStepTimelineProps>(), {
  steps: () => [],
  defaultExpanded: false,
})
const expanded = ref(props.defaultExpanded)

const orderedSteps = computed(() => [...props.steps].sort((left, right) =>
  left.startedOffsetUs - right.startedOffsetUs || left.id - right.id,
))
const observedSpanUs = computed(() => orderedSteps.value.reduce(
  (maximum, step) => Math.max(maximum, step.startedOffsetUs + step.durationUs),
  0,
))

const stageLabels: Record<string, string> = {
  access_control: '访问鉴权与限流',
  request_body_read: '读取请求体',
  payload_parse: '解析协议参数',
  request_log_start: '创建请求日志',
  session_resolution: '识别客户端会话',
  token_estimation: '估算输入 Token',
  route_planning: '规划路由策略',
  retry_policy: '确定重试策略',
  payload_transform: '转换上游请求',
  credential_decrypt: '解密渠道凭据',
  upstream_request_build: '构建上游请求',
  upstream_wait_headers: '等待上游响应头',
  response_body_read: '读取上游响应体',
  stream_response: '处理流式响应',
  response_analysis: '解析用量与异常',
  response_write: '写回客户端响应',
  attempt_log_prepare: '整理尝试日志',
  affinity_update: '更新路由亲和性',
  request_finalize: '汇总请求结果',
  request_log_persist: '事务落库',
}

const categoryLabels: Record<RelayStepCategory, string> = {
  gateway: '网关',
  upstream: '上游',
  downstream: '下游',
  storage: '存储',
}

function stageLabel(stage: string): string {
  return stageLabels[stage] ?? stage
}

function categoryLabel(category: RelayStepCategory): string {
  return categoryLabels[category] ?? category
}

function formatMicroseconds(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 1 : 2)} s`
  if (value >= 1_000) return `${(value / 1_000).toFixed(value >= 100_000 ? 0 : value >= 10_000 ? 1 : 2)} ms`
  return `${Math.max(0, Math.round(value))} us`
}

function barStyle(step: RelayStepLog): CSSProperties {
  const span = Math.max(observedSpanUs.value, 1)
  const left = Math.min(100, Math.max(0, step.startedOffsetUs / span * 100))
  const width = Math.max(0.35, Math.min(100 - left, step.durationUs / span * 100))
  return { left: `${left}%`, width: `${width}%` }
}
</script>

<template>
  <section v-if="orderedSteps.length" class="relay-step-timeline" aria-label="请求处理阶段耗时">
    <header>
      <button
        type="button"
        class="relay-step-toggle"
        :aria-expanded="expanded"
        @click="expanded = !expanded"
      >
        <el-icon class="relay-step-expand-icon" :class="{ 'is-expanded': expanded }"><ArrowRight /></el-icon>
        <strong>处理阶段</strong>
        <span>{{ orderedSteps.length }} 个阶段 · 观测跨度 {{ formatMicroseconds(observedSpanUs) }}</span>
      </button>
    </header>
    <ol v-show="expanded">
      <li v-for="(step, index) in orderedSteps" :key="step.id || `${step.stage}-${step.attempt}-${index}`" :class="`is-${step.outcome}`">
        <span class="step-index">{{ index + 1 }}</span>
        <div class="step-identity">
          <div>
            <strong>{{ stageLabel(step.stage) }}</strong>
            <span class="step-category" :class="`is-${step.category}`">{{ categoryLabel(step.category) }}</span>
            <span v-if="step.attempt > 0" class="step-attempt">尝试 {{ step.attempt }}</span>
          </div>
          <small v-if="step.detail">{{ step.detail }}</small>
        </div>
        <div class="step-waterfall" aria-hidden="true">
          <span :class="`is-${step.category}`" :style="barStyle(step)" />
        </div>
        <time>{{ formatMicroseconds(step.durationUs) }}</time>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.relay-step-timeline { margin-top: 12px; border-block: 1px solid var(--rose-border); background: var(--rose-surface); }
.relay-step-timeline > header { min-height: 38px; background: var(--rose-surface-muted); }
.relay-step-toggle { display: grid; width: 100%; min-height: 38px; grid-template-columns: 18px auto minmax(0, 1fr); align-items: center; gap: 8px; padding: 0 10px; border: 0; color: inherit; background: transparent; text-align: left; cursor: pointer; }
.relay-step-toggle:hover { background: color-mix(in srgb, var(--rose-surface-muted) 88%, var(--rose-primary)); }
.relay-step-toggle:focus-visible { position: relative; z-index: 1; outline: 2px solid var(--rose-primary); outline-offset: -2px; }
.relay-step-toggle strong { color: var(--rose-text); font-size: 12px; }
.relay-step-toggle span { justify-self: end; overflow: hidden; color: var(--rose-text-muted); font-size: 10px; font-variant-numeric: tabular-nums; text-overflow: ellipsis; white-space: nowrap; }
.relay-step-expand-icon { color: var(--rose-text-subtle); transition: transform 150ms ease; }
.relay-step-expand-icon.is-expanded { transform: rotate(90deg); }
.relay-step-timeline ol { display: grid; max-height: min(42dvh, 288px); margin: 0; padding: 0; overflow-y: auto; overscroll-behavior: contain; list-style: none; scrollbar-color: var(--rose-border-strong) var(--rose-surface-muted); scrollbar-gutter: stable; scrollbar-width: thin; }
.relay-step-timeline ol::-webkit-scrollbar { width: 8px; }
.relay-step-timeline ol::-webkit-scrollbar-track { background: var(--rose-surface-muted); }
.relay-step-timeline ol::-webkit-scrollbar-thumb { border: 2px solid var(--rose-surface-muted); background: var(--rose-border-strong); }
.relay-step-timeline li { display: grid; min-width: 0; grid-template-columns: 24px minmax(210px, 1fr) minmax(180px, 1.35fr) 76px; align-items: center; gap: 10px; min-height: 40px; padding: 5px 10px; border-top: 1px solid var(--rose-border); }
.relay-step-timeline li:first-child { border-top: 0; }
.relay-step-timeline li.is-failed { background: var(--rose-danger-soft); }
.relay-step-timeline li.is-canceled { background: var(--rose-warning-soft); }
.step-index { display: grid; width: 20px; height: 20px; place-items: center; border: 1px solid var(--rose-border-strong); border-radius: 50%; color: var(--rose-text-muted); font: 600 9px/1 var(--rose-font-mono); }
.step-identity { display: grid; min-width: 0; gap: 2px; }
.step-identity > div { display: flex; min-width: 0; align-items: center; flex-wrap: wrap; gap: 5px 8px; }
.step-identity strong { color: var(--rose-text); font-size: 11px; }
.step-identity small { overflow: hidden; color: var(--rose-text-muted); font-size: 9px; text-overflow: ellipsis; white-space: nowrap; }
.step-category, .step-attempt { padding: 2px 5px; border: 1px solid var(--rose-border); color: var(--rose-text-muted); background: var(--rose-surface); font-size: 8px; line-height: 1; }
.step-category.is-upstream { border-color: var(--rose-primary); color: var(--rose-primary-hover); }
.step-category.is-downstream { border-color: var(--rose-success); color: var(--rose-success); }
.step-category.is-storage { border-color: var(--rose-warning); color: var(--rose-warning); }
.step-waterfall { position: relative; height: 8px; overflow: hidden; background: var(--rose-surface-muted); }
.step-waterfall span { position: absolute; top: 0; bottom: 0; min-width: 2px; background: var(--rose-text-subtle); }
.step-waterfall span.is-upstream { background: var(--rose-primary); }
.step-waterfall span.is-downstream { background: var(--rose-success); }
.step-waterfall span.is-storage { background: var(--rose-warning); }
.relay-step-timeline time { color: var(--rose-text); font: 600 10px/1 var(--rose-font-mono); text-align: right; white-space: nowrap; }
@media (max-width: 760px) {
  .relay-step-timeline li { grid-template-columns: 22px minmax(0, 1fr) 68px; gap: 8px; }
  .step-waterfall { grid-column: 2 / -1; grid-row: 2; }
  .step-identity small { white-space: normal; overflow-wrap: anywhere; }
}
@media (max-width: 480px) {
  .relay-step-toggle { grid-template-columns: 18px minmax(0, 1fr); padding-block: 8px; }
  .relay-step-toggle span { grid-column: 2; justify-self: start; white-space: normal; }
  .relay-step-timeline ol { max-height: min(50dvh, 288px); }
}
</style>
