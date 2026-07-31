<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import type { CSSProperties } from 'vue'
import { ChatDotRound, Refresh, Search, View } from '@element-plus/icons-vue'
import SessionLogDrawer from '@/components/SessionLogDrawer.vue'
import type { ActiveSessionPage, CodexSessionSummary } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'

const ACTIVE_WINDOW_MINUTES = 30

const loading = ref(true)
const errorMessage = ref('')
const sessions = ref<CodexSessionSummary[]>([])
const total = ref(0)
const searchText = ref('')
const pagination = reactive({ page: 1, pageSize: 25 })
const drawerOpen = ref(false)
const selectedSession = ref<CodexSessionSummary | null>(null)

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'medium',
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))
}

function formatRelativeActivity(value: string): string {
  const elapsedSeconds = Math.max(0, Math.floor((Date.now() - Date.parse(value)) / 1000))
  if (elapsedSeconds < 60) return '刚刚活跃'
  return `${Math.min(ACTIVE_WINDOW_MINUTES, Math.floor(elapsedSeconds / 60))} 分钟前`
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

function sessionRowKey(session: CodexSessionSummary): string {
  return session.identified ? `${session.tokenId}:${session.sessionId}` : `request:${session.fallbackRequestId}`
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

async function loadSessions() {
  loading.value = true
  errorMessage.value = ''
  const query = new URLSearchParams({
    page: String(pagination.page),
    pageSize: String(pagination.pageSize),
  })
  if (searchText.value.trim()) query.set('session', searchText.value.trim())

  try {
    const page = await request<ActiveSessionPage>(`/admin/gateway/active-sessions?${query}`)
    sessions.value = page.items
    total.value = page.total
  } catch (error) {
    sessions.value = []
    total.value = 0
    errorMessage.value = error instanceof Error ? error.message : '活跃会话加载失败'
  } finally {
    loading.value = false
  }
}

function searchSessions() {
  pagination.page = 1
  void loadSessions()
}

function clearSearch() {
  searchText.value = ''
  pagination.page = 1
  void loadSessions()
}

function openSession(session: CodexSessionSummary) {
  selectedSession.value = session
  drawerOpen.value = true
}

onMounted(() => {
  void loadSessions()
})
</script>

<template>
  <div class="page-stack active-session-page">
    <header class="page-heading">
      <div>
        <h1>活跃会话</h1>
        <p>仅展示用户发起且最近 30 分钟内仍有调用的会话，按最近活跃时间排序</p>
      </div>
      <div class="page-actions">
        <span class="active-rule"><i aria-hidden="true" />用户会话 · 30 分钟</span>
        <el-tooltip content="刷新活跃会话" placement="bottom">
          <el-button
            class="page-refresh-button"
            :icon="Refresh"
            :loading="loading"
            aria-label="刷新活跃会话"
            @click="loadSessions"
          />
        </el-tooltip>
      </div>
    </header>

    <section class="active-query-bar" aria-label="活跃会话查询">
      <el-input
        v-model="searchText"
        clearable
        :prefix-icon="Search"
        placeholder="会话名称或会话 ID"
        aria-label="会话名称或会话 ID"
        @clear="clearSearch"
        @keyup.enter="searchSessions"
      />
      <el-button type="primary" :icon="Search" :loading="loading" @click="searchSessions">查询</el-button>
      <span class="active-count"><ChatDotRound />{{ loading ? '正在查询' : `${formatCompactNumber(total)} 个活跃会话` }}</span>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert">
      <strong>活跃会话加载失败</strong>
      <span>{{ errorMessage }}</span>
      <el-button :loading="loading" @click="loadSessions">重试</el-button>
    </div>

    <section v-else class="surface-panel table-panel active-table-panel" aria-live="polite">
      <el-table
        v-loading="loading"
        class="active-session-table"
        :data="sessions"
        :row-key="sessionRowKey"
        :row-style="sessionRowStyle"
        height="100%"
        empty-text="最近 30 分钟内没有活跃的用户会话"
        @row-click="openSession"
      >
        <el-table-column label="会话" min-width="300">
          <template #default="scope">
            <div class="session-identity" :class="`is-${sessionClientSource(scope.row).kind}`" :aria-label="`来源 ${sessionClientSource(scope.row).label}`">
              <div class="session-title-line">
                <strong class="session-name" :title="scope.row.sessionName || '未命名会话'">{{ scope.row.sessionName || '未命名会话' }}</strong>
                <span class="session-client"><i aria-hidden="true" />{{ sessionClientSource(scope.row).label }}</span>
              </div>
              <div class="session-meta">
                <small><code :title="scope.row.sessionId">{{ scope.row.identified ? scope.row.sessionId : scope.row.fallbackRequestId }}</code></small>
                <span class="session-origin" :class="`is-${sessionOrigin(scope.row.threadSource).kind}`" :title="`原始来源：${scope.row.threadSource || 'unavailable'}`"><i aria-hidden="true" />{{ sessionOrigin(scope.row.threadSource).label }}</span>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="路由 / 调用身份" min-width="290">
          <template #default="scope">
            <div class="route-cell">
              <div v-if="scope.row.currentChannel" class="channel-title"><strong>{{ scope.row.currentChannel.channelName }}</strong><el-tag :type="channelState(scope.row).type" effect="plain">{{ channelState(scope.row).label }}</el-tag></div>
              <div v-else class="channel-title"><strong class="muted-text">未进入上游渠道</strong><el-tag type="info" effect="plain">未分配</el-tag></div>
              <small><span>接口</span><code>{{ scope.row.latestEndpoint || '未知接口' }}</code><span>模型</span><code>{{ scope.row.latestModel || '未知模型' }}</code><span v-if="scope.row.currentChannel">上游</span><code v-if="scope.row.currentChannel">{{ scope.row.currentChannel.upstreamModel }}</code></small>
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
        <el-table-column label="性能 / 时间" min-width="290">
          <template #default="scope">
            <div class="performance-cell">
              <strong>首 Token {{ formatTiming(scope.row.averageFirstTokenMs, scope.row.firstTokenSampleCount) }} · 延迟 {{ formatTiming(scope.row.averageLatencyMs, scope.row.latencySampleCount) }}</strong>
              <small>请求耗时 {{ formatTiming(scope.row.averageDurationMs, scope.row.durationSampleCount) }}</small>
              <small>首次 {{ formatDate(scope.row.firstSeenAt) }}</small>
              <small class="activity-line"><i aria-hidden="true" />最近 {{ formatDate(scope.row.lastSeenAt) }} · {{ formatRelativeActivity(scope.row.lastSeenAt) }}</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="64" fixed="right" align="right">
          <template #default="scope">
            <el-tooltip content="查看会话详情" placement="top">
              <el-button
                class="table-action-button"
                text
                :icon="View"
                aria-label="查看会话详情"
                @click.stop="openSession(scope.row)"
              />
            </el-tooltip>
          </template>
        </el-table-column>
      </el-table>
      <footer class="table-pagination">
        <el-pagination
          v-model:current-page="pagination.page"
          v-model:page-size="pagination.pageSize"
          :disabled="loading"
          :total="total"
          :page-sizes="[25, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @change="loadSessions"
        />
      </footer>
    </section>

    <SessionLogDrawer v-model="drawerOpen" :summary="selectedSession" />
  </div>
</template>

<style scoped>
.active-session-page { padding-bottom: 16px; }
.active-rule { display: inline-flex; min-height: 34px; align-items: center; gap: 7px; padding: 0 10px; border: 1px solid var(--rose-border); border-radius: var(--rose-radius-control); color: var(--rose-text-muted); background: var(--rose-surface); font-size: 12px; }
.active-rule i { width: 7px; height: 7px; flex: none; border-radius: 50%; background: var(--rose-success); }
.active-query-bar { display: grid; grid-template-columns: minmax(240px, 520px) auto minmax(140px, 1fr); align-items: center; justify-content: start; gap: 8px; padding: 12px; border: 1px solid var(--rose-border); border-radius: var(--rose-radius-panel); background: var(--rose-surface); }
.active-query-bar .el-button { min-width: 88px; }
.active-count { display: inline-flex; align-items: center; justify-content: flex-end; gap: 6px; color: var(--rose-text-muted); font-size: 12px; font-variant-numeric: tabular-nums; }
.active-count svg { width: 15px; height: 15px; color: var(--rose-success); }
.active-table-panel { display: flex; min-width: 0; flex-direction: column; }
.active-table-panel :deep(.el-table__inner-wrapper::before) { display: none; }
.active-table-panel .table-pagination { flex: none; min-height: 56px; align-items: center; background: var(--rose-surface); }
.session-identity { display: grid; min-width: 0; gap: 6px; padding: 3px 4px; }
.session-title-line { display: flex; min-width: 0; align-items: flex-start; gap: 8px; }
.session-name { display: -webkit-box; min-width: 0; flex: 1; overflow: hidden; color: var(--rose-text); line-height: 1.4; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.session-client { display: inline-flex; flex: none; align-items: center; gap: 5px; color: var(--rose-text-muted); font-size: 10px; line-height: 18px; }
.session-client i { width: 6px; height: 6px; border-radius: 50%; background: var(--rose-text-subtle); }
.session-identity.is-codex .session-client i { background: var(--rose-success); }
.session-identity.is-copilot .session-client i { background: var(--rose-primary); }
.session-meta { display: flex; min-width: 0; align-items: center; gap: 8px; }
.session-meta small { min-width: 0; flex: 1; }
.session-origin { display: inline-flex; flex: none; align-items: center; gap: 5px; padding: 2px 6px; border: 1px solid var(--rose-border); color: var(--rose-text-muted); background: color-mix(in srgb, var(--rose-surface) 88%, transparent); font-size: 10px; line-height: 1.2; }
.session-origin i { width: 5px; height: 5px; border-radius: 50%; background: var(--rose-text-subtle); }
.session-origin.is-user { border-color: color-mix(in srgb, var(--rose-primary) 40%, var(--rose-border)); color: var(--rose-primary-hover); }
.session-origin.is-user i { background: var(--rose-primary); }
.session-origin.is-system, .session-origin.is-developer { border-color: color-mix(in srgb, var(--rose-warning) 46%, var(--rose-border)); color: var(--rose-warning); }
.session-origin.is-system i, .session-origin.is-developer i { background: var(--rose-warning); }
.session-origin.is-assistant { border-color: color-mix(in srgb, var(--rose-success) 42%, var(--rose-border)); color: var(--rose-success); }
.session-origin.is-assistant i { background: var(--rose-success); }
.channel-title { display: flex; min-width: 0; align-items: center; gap: 8px; }
.channel-title strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.session-identity small { overflow: hidden; color: var(--rose-text-muted); text-overflow: ellipsis; white-space: nowrap; }
.route-cell, .usage-cell, .performance-cell { display: grid; min-width: 0; gap: 4px; font-variant-numeric: tabular-nums; }
.route-cell > small { display: flex; min-width: 0; align-items: center; gap: 6px; overflow: hidden; color: var(--rose-text-muted); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.route-cell > small span { color: var(--rose-text-subtle); }
.route-cell > small code { overflow: hidden; text-overflow: ellipsis; }
.usage-cell > div { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; }
.usage-cell > div strong, .performance-cell > strong { color: var(--rose-text); font-size: 12px; }
.usage-cell > div span { color: var(--rose-text); font: 600 12px/1.3 var(--rose-font-mono); white-space: nowrap; }
.route-cell > small, .usage-cell > small, .performance-cell > small { overflow: hidden; color: var(--rose-text-muted); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
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
.performance-cell .activity-line { display: flex; align-items: center; gap: 5px; color: var(--rose-success); }
.performance-cell .activity-line i { width: 6px; height: 6px; flex: none; border-radius: 50%; background: currentColor; }
.active-session-table :deep(.el-table__body tr > td.el-table__cell) { background-color: var(--session-row-fill); transition: background-color 140ms ease; }
.active-session-table :deep(.el-table__body tr:hover > td.el-table__cell) { background-color: var(--session-row-fill-hover) !important; }
.active-session-table :deep(.el-table__body tr > td.el-table__cell:first-child) { box-shadow: inset 3px 0 0 var(--session-row-tone); }

@media (min-width: 961px) {
  .active-session-page { height: 100%; min-height: 0; grid-template-rows: auto auto minmax(0, 1fr); overflow: hidden; padding-bottom: 0; }
  .active-table-panel { min-height: 0; }
  .active-table-panel > .el-table { min-height: 0; flex: 1 1 0; }
}

@media (max-width: 720px) {
  .active-query-bar { grid-template-columns: minmax(0, 1fr) auto; }
  .active-count { grid-column: 1 / -1; justify-content: flex-start; }
}

@media (max-width: 460px) {
  .active-query-bar { grid-template-columns: 1fr; }
  .active-query-bar .el-button { width: 100%; }
}
</style>
