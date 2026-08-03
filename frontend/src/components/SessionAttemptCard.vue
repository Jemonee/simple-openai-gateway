<script setup lang="ts">
import { WarningFilled } from '@element-plus/icons-vue'
import type { RelayAttemptLog } from '@/types/gateway'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'

interface SessionAttemptCardProps {
  /** Upstream attempt displayed within a session request timeline. */
  attempt: RelayAttemptLog
  /** One-based attempt number within the current request. */
  attemptNumber: number
  /** Actual upstream API path, relative from and including /v1. */
  apiPath: string
  /** Reasoning effort requested by the client, or empty when omitted. */
  reasoningEffort: string
}

const { attempt, attemptNumber, apiPath, reasoningEffort } = defineProps<SessionAttemptCardProps>()

function formatTiming(value: number): string {
  return value > 0 ? formatDuration(value) : '--'
}

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 6 }).format(micros / 1_000_000)
}

function channelLabel(): string {
  return attempt.channelName || (attempt.channelId > 0 ? `渠道 #${attempt.channelId}` : '未知渠道')
}

function statusType(): 'success' | 'warning' | 'danger' | 'info' {
  if (attempt.outcome === 'canceled') return 'warning'
  if (attempt.outcome === 'failed' || (!attempt.outcome && !attempt.success) || attempt.statusCode >= 500 || attempt.statusCode === 0) return 'danger'
  if (attempt.statusCode === 408 || attempt.statusCode === 429) return 'warning'
  if (attempt.statusCode >= 200 && attempt.statusCode < 300) return 'success'
  return 'info'
}

function statusLabel(): string {
  if (attempt.outcome === 'canceled') return '客户端取消'
  if (attempt.outcome === 'success' || (!attempt.outcome && attempt.success)) return `成功 · HTTP ${attempt.statusCode}`
  if (!attempt.success && attempt.statusCode >= 200 && attempt.statusCode < 300) return `业务中断 · HTTP ${attempt.statusCode}`
  if (attempt.statusCode > 0) return `HTTP ${attempt.statusCode}`
  if (attempt.errorMessage.startsWith('gateway preparation failed:')) return '准备失败'
  return '网络错误'
}

function costSourceLabel(): string {
  if (attempt.costSource === 'upstream') return '上游返回'
  if (attempt.costSource === 'estimated_fallback') return '估算回退'
  if (attempt.costSource === 'mixed') return '混合费用'
  return attempt.outcome === 'canceled' ? '取消时费用未知' : '失败为零'
}

function costSourceType(): 'success' | 'warning' | 'info' {
  if (attempt.costSource === 'upstream') return 'success'
  if (attempt.costSource === 'estimated_fallback' || attempt.costSource === 'mixed' || attempt.outcome === 'canceled') return 'warning'
  return 'info'
}
</script>

<template>
  <article class="attempt-card" :class="{ 'is-failed': attempt.outcome === 'failed' || (!attempt.outcome && !attempt.success), 'is-canceled': attempt.outcome === 'canceled' }">
    <header class="attempt-header">
      <div class="attempt-identity">
        <div class="attempt-title"><span>尝试 {{ attemptNumber }}</span><strong>{{ channelLabel() }}</strong></div>
        <div class="attempt-route">
          <span><small>接口路径</small><code>{{ apiPath }}</code></span>
          <span><small>上游模型</small><code>{{ attempt.upstreamModel }}</code></span>
          <span><small>思考等级</small><code>{{ reasoningEffort || '默认' }}</code></span>
          <span v-if="attempt.channelBaseUrl"><small>Base URL</small><code class="attempt-url">{{ attempt.channelBaseUrl }}</code></span>
        </div>
      </div>
      <div class="attempt-badges">
        <el-tag :type="statusType()" effect="plain">{{ statusLabel() }}</el-tag>
        <el-tag :type="costSourceType()" effect="plain" size="small">{{ costSourceLabel() }}</el-tag>
      </div>
    </header>

    <div v-if="attempt.errorMessage" class="attempt-error" :class="{ 'is-canceled': attempt.outcome === 'canceled' }" role="note">
      <el-icon><WarningFilled /></el-icon>
      <div><strong>{{ attempt.outcome === 'canceled' ? '取消原因' : '失败原因' }}</strong><code>{{ attempt.errorMessage }}</code></div>
    </div>

    <div class="attempt-metric-groups">
      <section class="attempt-metric-group performance-metrics" aria-label="性能指标">
        <span class="metric-group-label">性能</span>
        <dl>
          <div><dt>首 Token</dt><dd>{{ formatTiming(attempt.firstTokenMs) }}</dd></div>
          <div><dt>请求延迟</dt><dd>{{ formatTiming(attempt.latencyMs) }}</dd></div>
          <div><dt>请求耗时</dt><dd>{{ formatTiming(attempt.durationMs) }}</dd></div>
        </dl>
      </section>
      <section class="attempt-metric-group usage-metrics" aria-label="Token 用量">
        <span class="metric-group-label">用量</span>
        <dl>
          <div><dt>普通输入</dt><dd>{{ formatCompactNumber(attempt.normalInputTokens) }}</dd></div>
          <div><dt>输出</dt><dd>{{ formatCompactNumber(attempt.outputTokens) }}</dd></div>
          <div><dt>缓存读</dt><dd>{{ formatCompactNumber(attempt.cachedTokens) }}</dd></div>
          <div><dt>缓存写</dt><dd>{{ formatCompactNumber(attempt.cacheWriteTokens) }}</dd></div>
          <div><dt>真实发送</dt><dd>{{ formatCompactNumber(attempt.sentTokens) }}</dd></div>
        </dl>
      </section>
      <section class="attempt-metric-group cost-metrics" aria-label="费用">
        <span class="metric-group-label">费用</span>
        <dl>
          <div><dt>上游金额</dt><dd>{{ formatUSD(attempt.upstreamCostMicros) }}</dd></div>
          <div><dt>自行估算</dt><dd>{{ formatUSD(attempt.estimatedCostMicros) }}</dd></div>
        </dl>
      </section>
    </div>
  </article>
</template>

<style scoped>
.attempt-card { container-type: inline-size; display: grid; min-width: 0; border: 1px solid var(--hongfen-border); border-left: 3px solid var(--hongfen-border-strong); background: var(--hongfen-surface); }
.attempt-card.is-failed { border-left-color: var(--hongfen-danger); }
.attempt-card.is-canceled { border-left-color: var(--hongfen-warning); }
.attempt-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; padding: 13px 14px 12px; }
.attempt-identity { display: grid; gap: 5px; min-width: 0; }
.attempt-title { display: flex; align-items: baseline; flex-wrap: wrap; gap: 8px 12px; min-width: 0; }
.attempt-title > span { color: var(--hongfen-text-muted); font-size: 10px; font-variant-numeric: tabular-nums; }
.attempt-title > strong { overflow: hidden; color: var(--hongfen-text); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.attempt-route { display: flex; align-items: center; flex-wrap: wrap; gap: 5px 16px; min-width: 0; color: var(--hongfen-text-muted); font-size: 10px; }
.attempt-route > span { display: flex; align-items: baseline; gap: 5px; min-width: 0; }
.attempt-route small { color: var(--hongfen-text-subtle); font-size: 9px; white-space: nowrap; }
.attempt-route code { color: var(--hongfen-text-muted); overflow-wrap: anywhere; }
.attempt-url { color: var(--hongfen-text-muted); }
.attempt-badges { display: flex; align-items: center; justify-content: flex-end; flex: 0 0 auto; flex-wrap: wrap; gap: 7px; }
.attempt-error { display: grid; grid-template-columns: 16px minmax(0, 1fr); align-items: start; gap: 8px; margin: 0 14px 12px; padding: 8px 10px; color: var(--hongfen-danger); background: var(--hongfen-danger-soft); }
.attempt-error .el-icon { margin-top: 2px; }
.attempt-error > div { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; min-width: 0; }
.attempt-error strong { flex: 0 0 auto; font-size: 10px; }
.attempt-error code { color: var(--hongfen-danger); font-size: 11px; overflow-wrap: anywhere; }
.attempt-error.is-canceled { color: var(--hongfen-warning); background: var(--hongfen-warning-soft); }
.attempt-error.is-canceled code { color: var(--hongfen-warning); }
.attempt-metric-groups { display: grid; grid-template-columns: minmax(250px, .9fr) minmax(400px, 1.5fr) minmax(190px, .7fr); border-top: 1px solid var(--hongfen-border); }
.attempt-metric-group { min-width: 0; padding: 11px 14px 13px; }
.attempt-metric-group + .attempt-metric-group { border-left: 1px solid var(--hongfen-border); }
.metric-group-label { display: block; margin-bottom: 8px; color: var(--hongfen-text-subtle); font-size: 9px; font-weight: 650; }
.attempt-metric-group dl { display: grid; gap: 9px 12px; margin: 0; font-variant-numeric: tabular-nums; }
.performance-metrics dl { grid-template-columns: repeat(3, minmax(68px, 1fr)); }
.usage-metrics dl { grid-template-columns: repeat(5, minmax(64px, 1fr)); }
.cost-metrics dl { grid-template-columns: repeat(2, minmax(78px, 1fr)); }
.attempt-metric-group dl > div { display: grid; gap: 2px; min-width: 0; }
.attempt-metric-group dt { color: var(--hongfen-text-muted); font-size: 10px; white-space: nowrap; }
.attempt-metric-group dd { margin: 0; color: var(--hongfen-text); font-size: 12px; font-weight: 600; white-space: nowrap; }
@container (max-width: 900px) {
  .attempt-metric-groups { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .cost-metrics { grid-column: 1 / -1; border-top: 1px solid var(--hongfen-border); border-left: 0 !important; }
}
@container (max-width: 620px) {
  .attempt-header { align-items: stretch; flex-direction: column; gap: 10px; }
  .attempt-badges { justify-content: flex-start; }
  .attempt-metric-groups { grid-template-columns: 1fr; }
  .attempt-metric-group + .attempt-metric-group { border-top: 1px solid var(--hongfen-border); border-left: 0; }
  .cost-metrics { grid-column: auto; }
  .usage-metrics dl { grid-template-columns: repeat(3, minmax(64px, 1fr)); }
}
@container (max-width: 400px) {
  .performance-metrics dl, .usage-metrics dl, .cost-metrics dl { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
