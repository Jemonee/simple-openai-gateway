<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { Coin, Connection, DataLine, Refresh, RefreshLeft, Search, Tickets, Timer, View } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import RequestPayloadDialog from '@/components/RequestPayloadDialog.vue'
import SessionAttemptCard from '@/components/SessionAttemptCard.vue'
import type { Channel, ClientToken, GatewayModel, LogAggregateSummary, LogPage, RelayAttemptLog, RelayRequestLog } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'
import { logDateDefaultTimes, logDateRangeShortcuts, toEastEightISOString, todayLogRange } from '@/utils/logDateRanges'

const loading = ref(true)
const errorMessage = ref('')
const logs = ref<RelayRequestLog[]>([])
const summary = ref<LogAggregateSummary | null>(null)
const total = ref(0)
const models = ref<GatewayModel[]>([])
const channels = ref<Channel[]>([])
const tokens = ref<ClientToken[]>([])
const filters = reactive({ model: '', channelId: '', tokenId: '', outcome: '', range: todayLogRange() as Date[] | null })
const pagination = reactive({ page: 1, pageSize: 50 })
const payloadDialogOpen = ref(false)
const selectedRequest = ref<RelayRequestLog | null>(null)
const payloadLoadingId = ref('')

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 6 }).format(micros / 1_000_000)
}

function formatPercent(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function formatTiming(value: number): string {
  return value > 0 ? formatDuration(value) : '--'
}

function formatAverageTiming(value: number, samples: number): string {
  return samples > 0 ? formatDuration(value) : '--'
}

function tokenName(log: RelayRequestLog): string {
  return log.tokenName || tokens.value.find((token) => token.id === log.tokenId)?.name || `令牌 #${log.tokenId}`
}

function finalAttempt(log: RelayRequestLog): RelayAttemptLog | undefined {
  return log.attempts[log.attempts.length - 1]
}

function finalChannelName(log: RelayRequestLog): string {
  const attempt = finalAttempt(log)
  if (!attempt) return ''
  return attempt.channelName || channels.value.find((channel) => channel.id === attempt.channelId)?.name || `渠道 #${attempt.channelId}`
}

function finalChannelModel(log: RelayRequestLog): string {
  return finalAttempt(log)?.upstreamModel || '未知上游模型'
}

function usageSource(value: string): string {
  if (value === 'upstream') return '上游 usage'
  if (value === 'estimated_tiktoken') return '本地 tiktoken 估算'
  if (value === 'mixed') return '混合来源'
  return '未知'
}

function costSource(log: RelayRequestLog): string {
  if (log.costSource === 'upstream') return '上游返回'
  if (log.costSource === 'estimated_fallback') return '估算回退'
  if (log.costSource === 'mixed') return '混合'
  if (log.outcome === 'canceled') return '取消时费用未知'
  return '失败为零'
}

function costSourceType(log: RelayRequestLog): 'success' | 'warning' | 'danger' | 'info' {
  if (log.costSource === 'upstream') return 'success'
  if (log.costSource === 'estimated_fallback' || log.costSource === 'mixed' || log.outcome === 'canceled') return 'warning'
  return 'info'
}

function statusType(status: number): 'success' | 'warning' | 'danger' | 'info' {
  if (status >= 200 && status < 300) return 'success'
  if (status === 408 || status === 429) return 'warning'
  if (status >= 500 || status === 0) return 'danger'
  return 'info'
}

function outcomeLabel(log: RelayRequestLog): string {
  if (log.outcome === 'success' || (log.outcome === '' && log.statusCode >= 200 && log.statusCode < 300)) return `HTTP ${log.statusCode}`
  if (log.outcome === 'canceled' || log.statusCode === 499) return '客户端取消'
  return log.statusCode > 0 ? `HTTP ${log.statusCode}` : '网络错误'
}

function outcomeType(log: RelayRequestLog): 'success' | 'warning' | 'danger' | 'info' {
  if (log.outcome === 'success' || (log.outcome === '' && log.statusCode >= 200 && log.statusCode < 300)) return 'success'
  if (log.outcome === 'canceled' || log.statusCode === 499) return 'warning'
  if (log.outcome === 'failed') return 'danger'
  return statusType(log.statusCode)
}

async function showPayloads(log: RelayRequestLog) {
  payloadLoadingId.value = log.id
  try {
    selectedRequest.value = await request<RelayRequestLog>(`/admin/gateway/logs/${encodeURIComponent(log.id)}`)
    payloadDialogOpen.value = true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '调用详情加载失败')
  } finally {
    payloadLoadingId.value = ''
  }
}

async function loadOptions() {
  [models.value, channels.value, tokens.value] = await Promise.all([
    request<GatewayModel[]>('/admin/gateway/models'),
    request<Channel[]>('/admin/gateway/channels'),
    request<ClientToken[]>('/admin/gateway/tokens'),
  ])
}

async function loadLogs() {
  loading.value = true
  errorMessage.value = ''
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.model) query.set('model', filters.model)
  if (filters.channelId) query.set('channelId', filters.channelId)
  if (filters.tokenId) query.set('tokenId', filters.tokenId)
  if (filters.outcome) query.set('outcome', filters.outcome)
  if (filters.range?.length === 2) {
    query.set('from', toEastEightISOString(filters.range[0]))
    query.set('to', toEastEightISOString(filters.range[1]))
  }
  try {
    const page = await request<LogPage>(`/admin/gateway/logs?${query}`)
    logs.value = page.items
    summary.value = page.summary
    total.value = page.total
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '调用日志加载失败'
  } finally {
    loading.value = false
  }
}

function searchLogs() {
  pagination.page = 1
  void loadLogs()
}

function resetLogs() {
  Object.assign(filters, { model: '', channelId: '', tokenId: '', outcome: '', range: todayLogRange() })
  pagination.page = 1
  void loadLogs()
}

onMounted(async () => {
  try {
    await loadOptions()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '筛选项加载失败'
  }
  await loadLogs()
})
</script>

<template>
  <div class="page-stack log-page">
    <header class="page-heading">
      <div><h1>调用日志</h1><p>默认按东八区显示今天，可查询 5 天内的请求状态、重试尝试与费用</p></div>
      <div class="page-actions">
        <el-tooltip content="刷新调用日志" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新调用日志" @click="loadLogs" /></el-tooltip>
      </div>
    </header>

    <section class="filter-bar" aria-label="日志筛选">
      <el-select v-model="filters.model" clearable placeholder="全部模型"><el-option v-for="model in models" :key="model.id" :label="model.name" :value="model.name" /></el-select>
      <el-select v-model="filters.channelId" clearable placeholder="全部渠道"><el-option v-for="channel in channels" :key="channel.id" :label="channel.name" :value="String(channel.id)" /></el-select>
      <el-select v-model="filters.tokenId" clearable placeholder="全部令牌"><el-option v-for="token in tokens" :key="token.id" :label="token.name" :value="String(token.id)" /></el-select>
      <el-select v-model="filters.outcome" clearable placeholder="全部结果"><el-option label="成功" value="success" /><el-option label="客户端取消" value="canceled" /><el-option label="失败" value="failed" /></el-select>
      <el-date-picker v-model="filters.range" type="datetimerange" format="YYYY-MM-DD HH:mm:ss" :default-time="logDateDefaultTimes" :shortcuts="logDateRangeShortcuts" range-separator="至" start-placeholder="开始时间" end-placeholder="结束时间" />
      <div class="filter-actions"><el-button :icon="RefreshLeft" :disabled="loading" @click="resetLogs">重置</el-button><el-button type="primary" :icon="Search" :loading="loading" @click="searchLogs">查询</el-button></div>
    </section>

    <section v-if="!errorMessage" class="metric-strip" aria-label="调用日志汇总">
      <article class="metric-cell">
        <span><Tickets />请求量</span>
        <strong v-if="!loading">{{ formatCompactNumber(summary?.requestCount ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>当前筛选范围</small>
      </article>
      <article class="metric-cell">
        <span><DataLine />成功率</span>
        <strong v-if="!loading">{{ formatPercent(summary?.successRate ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>成功 {{ formatCompactNumber(summary?.successCount ?? 0) }} · 取消 {{ formatCompactNumber(summary?.canceledCount ?? 0) }} · 失败 {{ formatCompactNumber((summary?.requestCount ?? 0) - (summary?.successCount ?? 0) - (summary?.canceledCount ?? 0)) }}</small>
      </article>
      <article class="metric-cell">
        <span><Connection />上游尝试</span>
        <strong v-if="!loading">{{ formatCompactNumber(summary?.attemptCount ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>包含重试调用</small>
      </article>
      <article class="metric-cell">
        <span><Coin />Token</span>
        <strong v-if="!loading">{{ formatCompactNumber((summary?.inputTokens ?? 0) + (summary?.outputTokens ?? 0)) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>输入 {{ formatCompactNumber(summary?.inputTokens ?? 0) }} · 输出 {{ formatCompactNumber(summary?.outputTokens ?? 0) }}</small>
      </article>
      <article class="metric-cell">
        <span><Coin />上游费用</span>
        <strong v-if="!loading">{{ formatUSD(summary?.upstreamCostMicros ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>自行估算 {{ formatUSD(summary?.estimatedCostMicros ?? 0) }}</small>
      </article>
      <article class="metric-cell">
        <span><Timer />平均请求耗时</span>
        <strong v-if="!loading">{{ formatAverageTiming(summary?.averageDurationMs ?? 0, summary?.durationSampleCount ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>首 Token {{ formatAverageTiming(summary?.averageFirstTokenMs ?? 0, summary?.firstTokenSampleCount ?? 0) }} · 延迟 {{ formatAverageTiming(summary?.averageLatencyMs ?? 0, summary?.latencySampleCount ?? 0) }}</small>
      </article>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>调用日志加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadLogs">重试</el-button></div>
    <section v-else class="surface-panel table-panel log-table-panel">
      <el-table v-loading="loading" class="compact-log-table" :data="logs" row-key="id" height="100%" empty-text="当前筛选条件下没有调用日志">
        <el-table-column type="expand">
          <template #default="scope">
            <div class="attempt-list">
              <header><strong>上游尝试</strong><span>共 {{ scope.row.attempts.length }} 次</span></header>
              <div v-if="scope.row.attempts.length === 0" class="muted-text">请求未进入上游调度</div>
              <SessionAttemptCard
                v-for="(attempt, index) in scope.row.attempts"
                :key="attempt.id"
                :attempt="attempt"
                :attempt-number="index + 1"
                :api-path="attempt.apiPath || scope.row.apiPath"
                :reasoning-effort="scope.row.reasoningEffort"
              />
            </div>
          </template>
        </el-table-column>
        <el-table-column label="时间 / 请求 ID" min-width="220"><template #default="scope"><div class="primary-cell"><strong>{{ formatDate(scope.row.createdAt) }}</strong><small><code>{{ scope.row.id }}</code></small></div></template></el-table-column>
        <el-table-column label="会话 / 调用令牌" min-width="210"><template #default="scope"><div class="primary-cell"><strong>{{ scope.row.sessionName || scope.row.codexSessionId || '未命名会话' }}</strong><small>{{ tokenName(scope.row) }} · <code>{{ scope.row.tokenKeyPrefix || '无历史前缀' }}</code></small></div></template></el-table-column>
        <el-table-column label="接口 / 模型" min-width="200"><template #default="scope"><div class="primary-cell"><strong><code>{{ scope.row.apiPath }}</code></strong><small>{{ scope.row.endpoint === 'chat' ? 'Chat Completions' : 'Responses' }} · <code>{{ scope.row.requestedModel }}</code></small></div></template></el-table-column>
        <el-table-column label="渠道" min-width="160"><template #default="scope"><div v-if="finalAttempt(scope.row)" class="primary-cell"><strong>{{ finalChannelName(scope.row) }}</strong><small><code>{{ finalChannelModel(scope.row) }}</code></small></div><span v-else class="muted-text">未进入上游渠道</span></template></el-table-column>
        <el-table-column label="状态" width="108"><template #default="scope"><el-tag :type="outcomeType(scope.row)" effect="plain">{{ outcomeLabel(scope.row) }}</el-tag></template></el-table-column>
        <el-table-column label="Token / 费用" min-width="310">
          <template #default="scope">
            <div class="usage-cost-cell">
              <div><strong>普通输入 {{ formatCompactNumber(scope.row.normalInputTokens) }} · 输出 {{ formatCompactNumber(scope.row.outputTokens) }}</strong><span>{{ formatUSD(scope.row.upstreamCostMicros) }}</span></div>
              <small>缓存读 {{ formatCompactNumber(scope.row.cachedTokens) }} · 缓存写 {{ formatCompactNumber(scope.row.cacheWriteTokens) }} · 真实发送（本地分词）{{ formatCompactNumber(scope.row.sentTokens) }}</small>
              <small>自行估算 {{ formatUSD(scope.row.estimatedCostMicros) }}</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="来源" width="142"><template #default="scope"><div class="source-breakdown"><el-tag :type="costSourceType(scope.row)" effect="plain" size="small">{{ costSource(scope.row) }}</el-tag><small>{{ usageSource(scope.row.usageSource) }}</small></div></template></el-table-column>
        <el-table-column label="性能 / 尝试" min-width="220"><template #default="scope"><div class="performance-cell"><strong>首 Token {{ formatTiming(scope.row.firstTokenMs) }} · 延迟 {{ formatTiming(scope.row.latencyMs) }}</strong><small>请求耗时 {{ formatTiming(scope.row.durationMs) }} · {{ scope.row.attemptCount }} 次尝试</small></div></template></el-table-column>
        <el-table-column label="详情" width="62" fixed="right" align="right"><template #default="scope"><el-tooltip content="查看调用记录" placement="top"><el-button class="table-action-button" text :icon="View" :loading="payloadLoadingId === scope.row.id" aria-label="查看调用记录" @click="showPayloads(scope.row)" /></el-tooltip></template></el-table-column>
      </el-table>
      <footer class="table-pagination"><el-pagination v-model:current-page="pagination.page" v-model:page-size="pagination.pageSize" :disabled="loading" :total="total" :page-sizes="[25, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadLogs" /></footer>
    </section>
    <RequestPayloadDialog v-model="payloadDialogOpen" :request="selectedRequest" />
  </div>
</template>

<style scoped>
.log-page { padding-bottom: 16px; }
.log-table-panel { display: flex; min-width: 0; flex-direction: column; }
.log-table-panel :deep(.el-table__inner-wrapper::before) { display: none; }
.log-table-panel .table-pagination { flex: none; min-height: 56px; align-items: center; background: var(--rose-surface); }
.compact-log-table :deep(.el-table__cell) { padding-block: 6px; }
.attempt-list { display: grid; gap: 7px; padding: 12px 20px 14px 48px; background: var(--rose-surface-muted); }
.attempt-list > header { display: flex; justify-content: space-between; color: var(--rose-text-muted); font-size: 12px; }
.attempt-list > header strong { color: var(--rose-text); }
.usage-cost-cell, .performance-cell, .source-breakdown { display: grid; min-width: 0; gap: 3px; font-variant-numeric: tabular-nums; }
.usage-cost-cell > div { display: flex; min-width: 0; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: 3px 12px; }
.usage-cost-cell > div strong, .performance-cell strong { color: var(--rose-text); font-size: 11px; line-height: 1.35; }
.usage-cost-cell > div span { flex: none; color: var(--rose-text); font: 600 12px/1.3 var(--rose-font-mono); }
.usage-cost-cell small, .performance-cell small, .source-breakdown small { color: var(--rose-text-muted); font-size: 10px; line-height: 1.35; overflow-wrap: anywhere; }
.source-breakdown { justify-items: start; }
@media (min-width: 961px) {
  .log-page { height: 100%; min-height: 0; grid-template-rows: auto auto auto minmax(0, 1fr); overflow: hidden; padding-bottom: 0; }
  .log-table-panel { min-height: 0; }
  .log-table-panel > .el-table { min-height: 0; flex: 1 1 0; }
  .log-page .metric-strip { grid-template-columns: repeat(6, minmax(0, 1fr)); }
  .log-page .metric-cell { min-height: 80px; padding-block: 10px; border-right: 1px solid var(--rose-border); border-bottom: 0; }
  .log-page .metric-cell:nth-child(3) { border-right: 1px solid var(--rose-border); }
  .log-page .metric-cell:nth-child(-n + 3) { border-bottom: 0; }
  .log-page .metric-cell:last-child { border-right: 0; }
  .log-page .metric-cell strong { margin-top: 5px; font-size: 17px; }
  .log-page .metric-cell small { margin-top: 3px; }
}
@media (min-width: 961px) and (max-width: 1360px) {
  .log-page .filter-bar { grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .log-page .filter-bar .el-date-editor { grid-column: span 3; }
}
@media (max-width: 720px) { .attempt-list { padding: 10px; overflow-x: auto; } }
</style>
