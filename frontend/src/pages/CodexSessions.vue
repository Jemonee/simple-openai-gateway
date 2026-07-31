<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import type { CSSProperties } from 'vue'
import { Coin, Connection, DataLine, EditPen, Refresh, RefreshLeft, Search, Tickets, Timer, View } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import SessionLogDrawer from '@/components/SessionLogDrawer.vue'
import type { Channel, ClientToken, CodexSessionPage, CodexSessionSummary, GatewayModel, LogAggregateSummary } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'
import { logDateDefaultTimes, logDateRangeShortcuts, toEastEightISOString, todayLogRange } from '@/utils/logDateRanges'

const loading = ref(true)
const errorMessage = ref('')
const sessions = ref<CodexSessionSummary[]>([])
const summary = ref<LogAggregateSummary | null>(null)
const total = ref(0)
const models = ref<GatewayModel[]>([])
const channels = ref<Channel[]>([])
const tokens = ref<ClientToken[]>([])
const filters = reactive({ session: '', model: '', channelId: '', tokenId: '', range: todayLogRange() as Date[] | null })
const pagination = reactive({ page: 1, pageSize: 25 })
const drawerOpen = ref(false)
const selectedSession = ref<CodexSessionSummary | null>(null)

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function formatPercent(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 6 }).format(micros / 1_000_000)
}

function formatTiming(value: number, samples: number): string {
  return samples > 0 ? formatDuration(value) : '--'
}

interface SessionHealth {
  score: number | null
  label: '健康' | '关注' | '风险' | '待观察'
  kind: 'healthy' | 'attention' | 'risk' | 'unknown'
}

function sessionHealth(row: CodexSessionSummary): SessionHealth {
  const completedRequests = Math.max(0, row.requestCount - row.canceledCount - row.processingCount)
  if (completedRequests === 0) return { score: null, label: '待观察', kind: 'unknown' }

  const successScore = Math.min(1, Math.max(0, row.successRate))
  const compactionCount = Math.max(0, row.compactionCount)
  const compactionScore = 1 / (1 + compactionCount * 0.35)
  const score = Math.round((successScore * 0.7 + compactionScore * 0.3) * 100)
  if (score >= 85) return { score, label: '健康', kind: 'healthy' }
  if (score >= 65) return { score, label: '关注', kind: 'attention' }
  return { score, label: '风险', kind: 'risk' }
}

function sessionRowStyle({ row }: { row: CodexSessionSummary }): CSSProperties {
  const health = sessionHealth(row)
  const score = health.score ?? 50
  const risk = 1 - score / 100
  let tone = 'var(--rose-text-subtle)'
  if (health.score !== null && score >= 50) {
    const successWeight = Math.round((score - 50) * 2)
    tone = `color-mix(in srgb, var(--rose-warning) ${100 - successWeight}%, var(--rose-success))`
  } else if (health.score !== null) {
    const warningWeight = Math.round(score * 2)
    tone = `color-mix(in srgb, var(--rose-danger) ${100 - warningWeight}%, var(--rose-warning))`
  }
  const tint = health.kind === 'unknown' ? 3 : Math.round(5 + risk * 5)
  return {
    '--session-row-tone': tone,
    '--session-row-fill': `color-mix(in srgb, var(--rose-surface) ${100 - tint}%, ${tone})`,
    '--session-row-fill-hover': `color-mix(in srgb, var(--rose-surface) ${Math.max(0, 96 - tint)}%, ${tone})`,
  } as CSSProperties
}

function compactionStyle(count: number): CSSProperties {
  const normalizedCount = Math.max(0, count)
  const emphasis = normalizedCount === 0 ? 0 : Math.min(1, Math.log1p(normalizedCount) / Math.log1p(8))
  const dangerWeight = Math.round(emphasis * 100)
  const tone = normalizedCount === 0
    ? 'var(--rose-text-subtle)'
    : `color-mix(in srgb, var(--rose-warning) ${100 - dangerWeight}%, var(--rose-danger))`
  return {
    '--compaction-tone': tone,
    '--compaction-size': `${Math.round(14 + emphasis * 4)}px`,
    '--compaction-width': `${Math.round(12 + emphasis * 44)}px`,
  } as CSSProperties
}

function sessionClientSource(session: CodexSessionSummary): { label: string; kind: 'codex' | 'copilot' | 'other' } {
  const clientKind = session.clientKind?.trim().toLowerCase() ?? ''
  const sessionSource = session.sessionSource?.trim().toLowerCase() ?? ''
  if (clientKind === 'copilot' || sessionSource.startsWith('copilot')) return { label: 'Copilot', kind: 'copilot' }
  if (clientKind === 'codex' || sessionSource === 'prompt_cache_key' || sessionSource.startsWith('client_metadata.') || sessionSource.startsWith('metadata.')) {
    return { label: 'Codex', kind: 'codex' }
  }
  return { label: '其他', kind: 'other' }
}

function sessionOrigin(value: string): { label: string; kind: 'user' | 'system' | 'assistant' | 'developer' | 'other' } {
  const source = value?.trim().toLowerCase() ?? ''
  if (source === 'user') return { label: '用户', kind: 'user' }
  if (source === 'system' || source === 'ambient_suggestions') return { label: 'System', kind: 'system' }
  if (source === 'assistant') return { label: 'Assistant', kind: 'assistant' }
  if (source === 'developer') return { label: 'Developer', kind: 'developer' }
  return { label: '其他', kind: 'other' }
}

function channelState(session: CodexSessionSummary): { label: string; type: 'success' | 'warning' | 'danger' | 'info' } {
  const channel = session.currentChannel
  if (!channel) return { label: '未分配', type: 'info' }
  if (!channel.enabled || !channel.mappingEnabled) return { label: '已停用', type: 'warning' }
  if (channel.circuitOpenUntil && Date.parse(channel.circuitOpenUntil) > Date.now()) return { label: '熔断中', type: 'danger' }
  return { label: '当前渠道', type: 'success' }
}

async function loadOptions() {
  [models.value, channels.value, tokens.value] = await Promise.all([
    request<GatewayModel[]>('/admin/gateway/models'),
    request<Channel[]>('/admin/gateway/channels'),
    request<ClientToken[]>('/admin/gateway/tokens'),
  ])
}

async function loadSessions() {
  loading.value = true
  errorMessage.value = ''
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.session.trim()) query.set('session', filters.session.trim())
  if (filters.model) query.set('model', filters.model)
  if (filters.channelId) query.set('channelId', filters.channelId)
  if (filters.tokenId) query.set('tokenId', filters.tokenId)
  if (filters.range?.length === 2) {
    query.set('from', toEastEightISOString(filters.range[0]))
    query.set('to', toEastEightISOString(filters.range[1]))
  }
  try {
    const page = await request<CodexSessionPage>(`/admin/gateway/sessions?${query}`)
    sessions.value = page.items
    summary.value = page.summary
    total.value = page.total
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '会话日志加载失败'
  } finally {
    loading.value = false
  }
}

function searchSessions() {
  pagination.page = 1
  void loadSessions()
}

function resetSessions() {
  Object.assign(filters, { session: '', model: '', channelId: '', tokenId: '', range: todayLogRange() })
  pagination.page = 1
  void loadSessions()
}

function openSession(session: CodexSessionSummary) {
  selectedSession.value = session
  drawerOpen.value = true
}

async function renameSession(session: CodexSessionSummary) {
  try {
    const result = await ElMessageBox.prompt('输入新的会话名称，最多 80 个字符', '修改会话名称', {
      confirmButtonText: '保存',
      cancelButtonText: '取消',
      inputValue: session.sessionName,
      inputValidator: (value) => {
        const title = value.trim()
        if (!title) return '会话名称不能为空'
        if ([...title].length > 80) return '会话名称最多 80 个字符'
        return true
      },
    })
    await request<null>('/admin/gateway/sessions/title', {
      method: 'PUT',
      body: JSON.stringify({
        sessionId: session.identified ? session.sessionId : '',
        requestId: session.identified ? '' : session.fallbackRequestId,
        tokenId: session.tokenId,
        title: result.value,
      }),
    })
    const normalized = result.value.trim().replace(/\s+/g, ' ')
    session.sessionName = normalized
    if (selectedSession.value && sessionRowKey(selectedSession.value) === sessionRowKey(session)) {
      selectedSession.value.sessionName = normalized
    }
    ElMessage.success('会话名称已保存')
  } catch (error) {
    if (error === 'cancel' || error === 'close') return
    ElMessage.error(error instanceof Error ? error.message : '会话名称保存失败')
  }
}

function sessionRowKey(session: CodexSessionSummary): string {
  return session.identified ? `${session.tokenId}:${session.sessionId}` : session.fallbackRequestId
}

onMounted(async () => {
  try {
    await loadOptions()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '筛选项加载失败'
  }
  await loadSessions()
})
</script>

<template>
  <div class="page-stack log-page">
    <header class="page-heading">
      <div><h1>会话日志</h1><p>默认按东八区显示今天，可查询 5 天内的会话、渠道、模型、令牌与用量</p></div>
      <div class="page-actions"><el-tooltip content="刷新会话日志" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新会话日志" @click="loadSessions" /></el-tooltip></div>
    </header>

    <section class="filter-bar" aria-label="会话日志筛选">
      <el-input v-model="filters.session" clearable placeholder="会话名称、会话 ID 或请求 ID" @keyup.enter="searchSessions" />
      <el-select v-model="filters.model" clearable placeholder="全部模型"><el-option v-for="model in models" :key="model.id" :label="model.name" :value="model.name" /></el-select>
      <el-select v-model="filters.channelId" clearable placeholder="全部渠道"><el-option v-for="channel in channels" :key="channel.id" :label="channel.name" :value="String(channel.id)" /></el-select>
      <el-select v-model="filters.tokenId" clearable placeholder="全部令牌"><el-option v-for="token in tokens" :key="token.id" :label="token.name" :value="String(token.id)" /></el-select>
      <el-date-picker v-model="filters.range" type="datetimerange" format="YYYY-MM-DD HH:mm:ss" :default-time="logDateDefaultTimes" :shortcuts="logDateRangeShortcuts" range-separator="至" start-placeholder="开始时间" end-placeholder="结束时间" />
      <div class="filter-actions"><el-button :icon="RefreshLeft" :disabled="loading" @click="resetSessions">重置</el-button><el-button type="primary" :icon="Search" :loading="loading" @click="searchSessions">查询</el-button></div>
    </section>

    <section v-if="!errorMessage" class="metric-strip" aria-label="会话日志汇总">
      <article class="metric-cell">
        <span><Tickets />会话数</span>
        <strong v-if="!loading">{{ formatCompactNumber(total) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>当前筛选范围</small>
      </article>
      <article class="metric-cell">
        <span><Connection />请求量</span>
        <strong v-if="!loading">{{ formatCompactNumber(summary?.requestCount ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>{{ formatCompactNumber(summary?.attemptCount ?? 0) }} 次上游尝试</small>
      </article>
      <article class="metric-cell">
        <span><DataLine />成功率</span>
        <strong v-if="!loading">{{ formatPercent(summary?.successRate ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>成功 {{ formatCompactNumber(summary?.successCount ?? 0) }} · 取消 {{ formatCompactNumber(summary?.canceledCount ?? 0) }} · 失败 {{ formatCompactNumber((summary?.requestCount ?? 0) - (summary?.successCount ?? 0) - (summary?.canceledCount ?? 0)) }}</small>
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
        <strong v-if="!loading">{{ formatTiming(summary?.averageDurationMs ?? 0, summary?.durationSampleCount ?? 0) }}</strong><el-skeleton v-else :rows="1" animated />
        <small>首 Token {{ formatTiming(summary?.averageFirstTokenMs ?? 0, summary?.firstTokenSampleCount ?? 0) }} · 延迟 {{ formatTiming(summary?.averageLatencyMs ?? 0, summary?.latencySampleCount ?? 0) }}</small>
      </article>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>会话日志加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadSessions">重试</el-button></div>
    <section v-else class="surface-panel table-panel log-table-panel">
      <el-table v-loading="loading" class="session-table" :data="sessions" :row-key="sessionRowKey" :row-style="sessionRowStyle" height="100%" empty-text="当前筛选条件下没有会话记录" @row-click="openSession">
        <el-table-column label="会话" min-width="300">
          <template #default="scope">
            <div class="session-identity" :class="`is-${sessionClientSource(scope.row).kind}`" :aria-label="`来源 ${sessionClientSource(scope.row).label}`">
              <div class="session-title-line">
                <strong class="session-name" :title="scope.row.sessionName || '未命名会话'">{{ scope.row.sessionName || '未命名会话' }}</strong>
                <span class="session-client"><i aria-hidden="true" />{{ sessionClientSource(scope.row).label }}</span>
              </div>
              <div class="session-meta">
                <small><code>{{ scope.row.identified ? scope.row.sessionId : scope.row.fallbackRequestId }}</code></small>
                <span class="session-origin" :class="`is-${sessionOrigin(scope.row.threadSource).kind}`" :title="`原始来源：${scope.row.threadSource || 'unavailable'}`"><i aria-hidden="true" />{{ sessionOrigin(scope.row.threadSource).label }}</span>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="路由 / 调用身份" min-width="270">
          <template #default="scope">
            <div class="route-cell">
              <div v-if="scope.row.currentChannel" class="channel-title"><strong>{{ scope.row.currentChannel.channelName }}</strong><el-tag :type="channelState(scope.row).type" effect="plain">{{ channelState(scope.row).label }}</el-tag></div>
              <div v-else class="channel-title"><strong class="muted-text">未进入上游渠道</strong><el-tag type="info" effect="plain">未分配</el-tag></div>
              <small><span>模型</span><code>{{ scope.row.latestModel }}</code><span v-if="scope.row.currentChannel">上游</span><code v-if="scope.row.currentChannel">{{ scope.row.currentChannel.upstreamModel }}</code></small>
              <small><span>令牌</span>{{ scope.row.tokenName || `令牌 #${scope.row.tokenId}` }}<code>{{ scope.row.tokenKeyPrefix || '无历史前缀' }}</code></small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="会话健康度" width="156">
          <template #default="scope">
            <div class="health-cell" :class="`is-${sessionHealth(scope.row).kind}`">
              <div><strong>{{ sessionHealth(scope.row).score ?? '--' }}<small v-if="sessionHealth(scope.row).score !== null">分</small></strong><span><i aria-hidden="true" />{{ sessionHealth(scope.row).label }}</span></div>
              <small>成功率 {{ formatPercent(scope.row.successRate) }}</small>
              <small>{{ formatCompactNumber(scope.row.requestCount) }} 请求 · {{ formatCompactNumber(scope.row.attemptCount) }} 尝试</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="压缩次数" width="104" align="right"><template #default="scope"><div class="compaction-cell" :style="compactionStyle(scope.row.compactionCount)"><strong>{{ formatCompactNumber(scope.row.compactionCount) }}</strong><small>次压缩</small><i aria-hidden="true" /></div></template></el-table-column>
        <el-table-column label="Token / 费用" min-width="260">
          <template #default="scope">
            <div class="usage-cell">
              <div><strong>{{ formatCompactNumber(scope.row.inputTokens + scope.row.outputTokens) }} Token</strong><span>{{ formatUSD(scope.row.upstreamCostMicros) }}</span></div>
              <small>普通输入 {{ formatCompactNumber(scope.row.normalInputTokens) }} · 输出 {{ formatCompactNumber(scope.row.outputTokens) }} · 缓存读 {{ formatCompactNumber(scope.row.cachedTokens) }}</small>
              <small>缓存写 {{ formatCompactNumber(scope.row.cacheWriteTokens) }} · 真实发送 {{ formatCompactNumber(scope.row.sentTokens) }} · 估算 {{ formatUSD(scope.row.estimatedCostMicros) }}</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="性能 / 时间" min-width="270"><template #default="scope"><div class="performance-cell"><strong>首 Token {{ formatTiming(scope.row.averageFirstTokenMs, scope.row.firstTokenSampleCount) }} · 延迟 {{ formatTiming(scope.row.averageLatencyMs, scope.row.latencySampleCount) }}</strong><small>请求耗时 {{ formatTiming(scope.row.averageDurationMs, scope.row.durationSampleCount) }}</small><small>首次 {{ formatDate(scope.row.firstSeenAt) }} · 最近 {{ formatDate(scope.row.lastSeenAt) }}</small></div></template></el-table-column>
        <el-table-column label="操作" width="96" fixed="right" align="right"><template #default="scope"><div class="table-actions"><el-tooltip content="修改会话名称" placement="top"><el-button class="table-action-button" text :icon="EditPen" aria-label="修改会话名称" @click.stop="renameSession(scope.row)" /></el-tooltip><el-tooltip content="查看会话详情" placement="top"><el-button class="table-action-button" text :icon="View" aria-label="查看会话详情" @click.stop="openSession(scope.row)" /></el-tooltip></div></template></el-table-column>
      </el-table>
      <footer class="table-pagination"><el-pagination v-model:current-page="pagination.page" v-model:page-size="pagination.pageSize" :disabled="loading" :total="total" :page-sizes="[25, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadSessions" /></footer>
    </section>

    <SessionLogDrawer v-model="drawerOpen" :summary="selectedSession" @closed="loadSessions" />
  </div>
</template>

<style scoped>
.log-page { padding-bottom: 16px; }
.log-table-panel { display: flex; min-width: 0; flex-direction: column; }
.log-table-panel :deep(.el-table__inner-wrapper::before) { display: none; }
.log-table-panel .table-pagination { flex: none; min-height: 56px; align-items: center; background: var(--rose-surface); }
.session-identity { display: grid; min-width: 0; gap: 6px; padding: 3px 4px; }
.session-title-line { display: flex; min-width: 0; align-items: flex-start; gap: 8px; }
.session-name { display: -webkit-box; min-width: 0; flex: 1; overflow: hidden; color: var(--rose-text); line-height: 1.4; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.session-client { display: inline-flex; flex: none; align-items: center; gap: 5px; color: var(--rose-text-muted); font-size: 10px; line-height: 18px; }
.session-client i { width: 6px; height: 6px; border-radius: 50%; background: var(--rose-text-subtle); }
.session-identity.is-codex .session-client i { background: var(--rose-success); }
.session-identity.is-copilot .session-client i { background: var(--rose-primary); }
.session-meta { display: flex; min-width: 0; align-items: center; gap: 8px; }
.session-meta small { min-width: 0; flex: 1; }
.session-origin { display: inline-flex; flex: 0 0 auto; align-items: center; gap: 5px; padding: 2px 6px; border: 1px solid var(--rose-border); color: var(--rose-text-muted); background: color-mix(in srgb, var(--rose-surface) 88%, transparent); font-size: 10px; line-height: 1.2; }
.session-origin i { width: 5px; height: 5px; border-radius: 50%; background: var(--rose-text-subtle); }
.session-origin.is-user { border-color: color-mix(in srgb, var(--rose-primary) 40%, var(--rose-border)); color: var(--rose-primary-hover); }
.session-origin.is-user i { background: var(--rose-primary); }
.session-origin.is-system, .session-origin.is-developer { border-color: color-mix(in srgb, var(--rose-warning) 46%, var(--rose-border)); color: var(--rose-warning); }
.session-origin.is-system i, .session-origin.is-developer i { background: var(--rose-warning); }
.session-origin.is-assistant { border-color: color-mix(in srgb, var(--rose-success) 42%, var(--rose-border)); color: var(--rose-success); }
.session-origin.is-assistant i { background: var(--rose-success); }
.channel-title { display: flex; align-items: center; gap: 8px; min-width: 0; }
.channel-title strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-identity small { overflow: hidden; color: var(--rose-text-muted); text-overflow: ellipsis; white-space: nowrap; }
.route-cell, .usage-cell, .performance-cell { display: grid; min-width: 0; gap: 4px; font-variant-numeric: tabular-nums; }
.route-cell > small, .usage-cell > small, .performance-cell > small { overflow: hidden; color: var(--rose-text-muted); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.route-cell > small { display: flex; align-items: center; gap: 6px; }
.route-cell > small span { color: var(--rose-text-subtle); }
.route-cell > small code { overflow: hidden; text-overflow: ellipsis; }
.usage-cell > div { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.usage-cell > div strong, .performance-cell > strong { color: var(--rose-text); font-size: 12px; }
.usage-cell > div span { color: var(--rose-text); font: 600 12px/1.3 var(--rose-font-mono); white-space: nowrap; }
.health-cell { display: grid; gap: 3px; font-variant-numeric: tabular-nums; }
.health-cell > div { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.health-cell > div > strong { color: var(--session-row-tone); font: 700 19px/1.2 var(--rose-font-mono); }
.health-cell > div > strong small { margin-left: 2px; color: inherit; font: 500 10px/1 var(--rose-font-sans); }
.health-cell > div > span { display: inline-flex; align-items: center; gap: 5px; color: var(--session-row-tone); font-size: 11px; font-weight: 600; }
.health-cell > div > span i { width: 6px; height: 6px; border-radius: 50%; background: currentColor; }
.health-cell > small { color: var(--rose-text-muted); font-size: 10px; }
.compaction-cell { position: relative; display: grid; justify-items: end; gap: 2px; padding-right: 7px; font-variant-numeric: tabular-nums; }
.compaction-cell strong { color: var(--compaction-tone); font: 700 var(--compaction-size)/1.2 var(--rose-font-mono); }
.compaction-cell small { color: var(--rose-text-muted); font-size: 10px; }
.compaction-cell > i { width: var(--compaction-width); height: 2px; margin-top: 3px; background: var(--compaction-tone); content: ''; }
.session-table :deep(.el-table__body tr > td.el-table__cell) { background-color: var(--session-row-fill); transition: background-color 140ms ease; }
.session-table :deep(.el-table__body tr:hover > td.el-table__cell) { background-color: var(--session-row-fill-hover) !important; }
.session-table :deep(.el-table__body tr > td.el-table__cell:first-child) { box-shadow: inset 3px 0 0 var(--session-row-tone); }
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
</style>
