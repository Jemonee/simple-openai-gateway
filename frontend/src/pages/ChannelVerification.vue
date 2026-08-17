<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Aim, CircleCheck, CloseBold, Refresh, Search, View } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import type {
  Channel,
  GatewayModel,
  VerificationAttempt,
  VerificationCheckResult,
  VerificationMode,
  VerificationReport,
  VerificationReportPage,
  VerificationSession,
  VerificationSessionDetail,
  VerificationSessionStatus,
  VerificationVerdict,
} from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration, formatUSD } from '@/utils/formatters'

interface ModeOption {
  /** Preset label shown in the segmented control. */
  label: string
  /** Backend v4.1.1 preset identifier. */
  value: VerificationMode
  /** Frozen logical request count for the preset. */
  requests: number
  /** Compact scope shown below the control. */
  scope: string
}

const modeOptions: ModeOption[] = [
  { label: '低', value: 'low', requests: 19, scope: 'Juice、确定性检查、三组轻量行为指纹' },
  { label: '中', value: 'medium', requests: 49, scope: '三组 10 次行为指纹与扩展 Juice' },
  { label: '高', value: 'high', requests: 158, scope: '四种请求画像、32K 上下文、完整指纹' },
]

const route = useRoute()
const loading = ref(true)
const errorMessage = ref('')
const channels = ref<Channel[]>([])
const models = ref<GatewayModel[]>([])
const reports = ref<VerificationReport[]>([])
const sessions = ref<VerificationSession[]>([])
const totalReports = ref(0)
const totalSessions = ref(0)
const selectedReport = ref<VerificationReport | null>(null)
const selectedSession = ref<VerificationSessionDetail | null>(null)
const drawerOpen = ref(false)
const sessionLoading = ref(false)
const starting = ref(false)
const stopping = ref(false)
const optionsReady = ref(false)
const filters = reactive({ channelId: 0, modelId: 0, mode: 'medium' as VerificationMode })
const pagination = reactive({ page: 1, pageSize: 20 })
let pollTimer: ReturnType<typeof setInterval> | undefined

const selectedChannel = computed(() => channels.value.find((channel) => channel.id === filters.channelId) ?? null)
const availableModels = computed(() => {
  const channel = selectedChannel.value
  if (!channel) return []
  return channel.models
    .filter((mapping) => mapping.enabled && !mapping.circuitDisabled)
    .map((mapping) => ({
      id: mapping.modelId,
      name: models.value.find((model) => model.id === mapping.modelId)?.name ?? `模型 #${mapping.modelId}`,
      upstreamModel: mapping.upstreamModel,
    }))
})
const selectedMode = computed(() => modeOptions.find((option) => option.value === filters.mode) ?? modeOptions[1])
const latestReport = computed(() => reports.value[0] ?? null)
const activeSession = computed(() => sessions.value.find((session) => session.status === 'running' || session.status === 'stopping') ?? null)
const selectedSessionProgress = computed(() => {
  const session = selectedSession.value?.session
  if (!session?.plannedCount) return 0
  return Math.min(100, Math.round(session.completedCount / session.plannedCount * 100))
})
const nativeGoFallbackActive = computed(() => selectedSession.value?.session.nativeTransportBackend === 'go_fallback'
  || selectedSession.value?.attempts.some((attempt) => attempt.transportBackend === 'go_fallback') === true)
const fingerprintRows = computed(() => {
  const values = selectedReport.value?.fingerprintMatch ?? {}
  return ['gpt-5.6-sol', 'gpt-5.6-terra', 'gpt-5.6-luna'].map((model) => ({ model, value: values[model] ?? 0 }))
})

function formatDate(value: string): string {
  if (!value) return '--'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function modeLabel(value: VerificationMode): string {
	if (value === 'quick') return '快速（旧）'
	if (value === 'standard') return '标准（旧）'
	if (value === 'full') return '完整（旧）'
  return modeOptions.find((option) => option.value === value)?.label ?? value
}

function logicalRequestCount(value: VerificationMode): number | null {
  return modeOptions.find((option) => option.value === value)?.requests ?? null
}

function modelLabel(value: string): string {
  if (value === 'gpt-5.6-sol') return 'Sol'
  if (value === 'gpt-5.6-terra') return 'Terra'
  if (value === 'gpt-5.6-luna') return 'Luna'
  return value
}

function verdictLabel(value: VerificationVerdict | ''): string {
  if (!value) return '检测中'
	if (value === 'passed') return '通过（旧）'
	if (value === 'marginal') return '临界（旧）'
	if (value === 'failed') return '失败（旧）'
  if (value === 'possible_non_gpt') return '可能非 GPT'
  const juice = value.includes('mismatch') ? 'Juice 不一致' : value.includes('insufficient') ? 'Juice 不足' : 'Juice 通过'
  return `${juice} / ${value.endsWith('strong') ? '指纹强指向' : '指纹不明确'}`
}

function verdictType(value: VerificationVerdict | ''): 'success' | 'warning' | 'danger' | 'info' {
  if (!value) return 'info'
	if (value === 'passed') return 'success'
	if (value === 'marginal') return 'warning'
	if (value === 'failed') return 'danger'
  if (value === 'possible_non_gpt' || value.includes('mismatch')) return 'danger'
  if (value.includes('insufficient') || value.endsWith('unclear')) return 'warning'
  return 'success'
}

function sessionStatusLabel(value: VerificationSessionStatus): string {
  return { running: '运行中', stopping: '停止中', completed: '已完成', stopped: '已停止', failed: '失败' }[value]
}

function sessionStatusType(value: VerificationSessionStatus): 'success' | 'warning' | 'danger' | 'info' {
  if (value === 'completed') return 'success'
  if (value === 'failed') return 'danger'
  if (value === 'stopped' || value === 'stopping') return 'warning'
  return 'info'
}

function checkStatusLabel(check: VerificationCheckResult): string {
  if (check.status === 'passed') return '通过'
  if (check.status === 'skipped') return '证据不明确'
  return '异常'
}

function checkStatusType(check: VerificationCheckResult): 'success' | 'warning' | 'danger' {
  if (check.status === 'passed') return 'success'
  if (check.status === 'skipped') return 'warning'
  return 'danger'
}

function attemptStatusType(attempt: VerificationAttempt): 'success' | 'danger' | 'warning' {
  if (attempt.outcome === 'success') return 'success'
  if (attempt.statusCode === 429 || attempt.statusCode === 408) return 'warning'
  return 'danger'
}

function attemptStatusLabel(attempt: VerificationAttempt): string {
  if (attempt.outcome === 'success') return `成功 · HTTP ${attempt.statusCode}`
  if (attempt.statusCode > 0) return `失败 · HTTP ${attempt.statusCode}`
  return '网络失败'
}

function detectorLabel(name: string): string {
  const labels: Record<string, string> = {
    juice_high: 'High Juice', juice_low: 'Low Juice', juice_medium: 'Medium Juice', juice_xhigh: 'XHigh Juice', juice_max: 'Max Juice',
    output_luna_48: '48 输出完整性', output_terra_32: '32 输出完整性', juice_coverage: 'Juice 显式覆盖',
    rand_country: '随机国家指纹', rand_bird: '随机鸟类指纹', b80_letter_count: 'B80 字符指纹',
  }
  return labels[name] ?? name
}

function requestProfile(attempt: VerificationAttempt): string {
  const format = attempt.requestFormat === 'native_codex'
    ? attempt.transportBackend === 'go_fallback' ? 'Native Codex（纯 Go 回退）' : 'Native Codex（Node.js）'
    : '普通 Responses'
  const context = attempt.contextMode === 'fixed_32k_history' ? '32K 历史' : '短上下文'
  return `${format} · ${context} · ${attempt.effort}`
}

function formatAttemptBody(body: string): string {
  if (!body) return '(空)'
  try { return JSON.stringify(JSON.parse(body), null, 2) } catch { return body }
}

function evidenceText(check: VerificationCheckResult): string {
  return check.evidence ? JSON.stringify(check.evidence, null, 2) : ''
}

function selectFirstModel() {
  if (!availableModels.value.some((model) => model.id === filters.modelId)) {
    filters.modelId = availableModels.value[0]?.id ?? 0
  }
}

watch(() => filters.channelId, () => {
  selectFirstModel()
  pagination.page = 1
  if (optionsReady.value) void loadVerificationData()
})

watch(() => filters.modelId, () => {
  pagination.page = 1
  if (optionsReady.value) void loadVerificationData()
})

async function loadVerificationData() {
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.channelId) query.set('channelId', String(filters.channelId))
  if (filters.modelId) query.set('modelId', String(filters.modelId))
  const [reportPage, sessionPage] = await Promise.all([
    request<VerificationReportPage>(`/admin/gateway/verifications?${query}`),
    request<{ items: VerificationSession[]; total: number }>(`/admin/gateway/verification-sessions?${query}`),
  ])
  reports.value = reportPage.items
  totalReports.value = reportPage.total
  sessions.value = sessionPage.items
  totalSessions.value = sessionPage.total
  if (selectedSession.value && ['running', 'stopping'].includes(selectedSession.value.session.status)) {
    await loadSession(selectedSession.value.session.id, false)
    if (selectedSession.value && !['running', 'stopping'].includes(selectedSession.value.session.status)) {
      const report = reports.value.find((item) => item.sessionId === selectedSession.value?.session.id)
      if (report) selectedReport.value = report
    }
  }
}

async function loadData() {
  loading.value = true
  optionsReady.value = false
  errorMessage.value = ''
  try {
    const [channelItems, modelItems] = await Promise.all([
      request<Channel[]>('/admin/gateway/channels'),
      request<GatewayModel[]>('/admin/gateway/models'),
    ])
    channels.value = channelItems
    models.value = modelItems
    const requestedChannelId = Number(route.query.channelId)
    if (Number.isInteger(requestedChannelId) && channels.value.some((channel) => channel.id === requestedChannelId)) filters.channelId = requestedChannelId
    if (!filters.channelId || !channels.value.some((channel) => channel.id === filters.channelId)) filters.channelId = channels.value.find((channel) => channel.enabled)?.id ?? channels.value[0]?.id ?? 0
    selectFirstModel()
    optionsReady.value = true
    await loadVerificationData()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '渠道验证数据加载失败'
  } finally {
    loading.value = false
  }
}

async function loadSession(sessionId: string, showLoading = true) {
  if (showLoading) sessionLoading.value = true
  try {
    selectedSession.value = await request<VerificationSessionDetail>(`/admin/gateway/verification-sessions/${encodeURIComponent(sessionId)}`)
  } catch (error) {
    if (showLoading) ElMessage.error(error instanceof Error ? error.message : '验证会话加载失败')
  } finally {
    if (showLoading) sessionLoading.value = false
  }
}

async function runVerification() {
  if (!filters.channelId || !filters.modelId) {
    ElMessage.warning('请选择渠道和已启用的 GPT-5.6 模型映射')
    return
  }
  starting.value = true
  try {
    const session = await request<VerificationSession>('/admin/gateway/verifications', {
      method: 'POST',
      body: JSON.stringify({ channelId: filters.channelId, modelId: filters.modelId, mode: filters.mode }),
    })
    selectedReport.value = null
    selectedSession.value = { session, attempts: [] }
    drawerOpen.value = true
    if (session.nativeTransportBackend === 'go_fallback') {
      ElMessage.warning(`${modeLabel(filters.mode)}档检测已启动；未检测到 Node.js，Native Codex 探针将使用纯 Go 回退`)
    } else {
      ElMessage.success(`${modeLabel(filters.mode)}档检测已启动，共 ${session.plannedCount} 个逻辑请求`)
    }
    await loadVerificationData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '渠道验证启动失败')
  } finally {
    starting.value = false
  }
}

async function stopSession() {
  const session = selectedSession.value?.session
  if (!session || !['running', 'stopping'].includes(session.status)) return
  stopping.value = true
  try {
    await request<VerificationSession>(`/admin/gateway/verification-sessions/${encodeURIComponent(session.id)}/stop`, { method: 'POST' })
    await loadSession(session.id, false)
    ElMessage.success('停止请求已提交')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '停止检测失败')
  } finally {
    stopping.value = false
  }
}

async function openSession(session: VerificationSession) {
  selectedReport.value = reports.value.find((report) => report.sessionId === session.id) ?? null
  selectedSession.value = null
  drawerOpen.value = true
  await loadSession(session.id)
}

async function openReport(report: VerificationReport) {
  selectedReport.value = report
  selectedSession.value = null
  drawerOpen.value = true
  await loadSession(report.sessionId)
}

function handlePageChange(page: number) {
  pagination.page = page
  void loadVerificationData()
}

onMounted(() => {
  void loadData()
  pollTimer = setInterval(() => {
    if (optionsReady.value && (activeSession.value || selectedSession.value?.session.status === 'running' || selectedSession.value?.session.status === 'stopping')) void loadVerificationData()
  }, 1500)
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<template>
  <div class="page-stack verification-page">
    <header class="page-heading">
      <div class="heading-copy"><div class="heading-icon"><Aim /></div><div><div class="eyebrow">渠道质量</div><h1>GPT-5.6 模型指纹验证</h1></div></div>
      <el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新验证数据" title="刷新" @click="loadData" />
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>验证数据加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadData">重试</el-button></div>

    <section class="verification-toolbar" aria-label="验证参数">
      <el-form inline @submit.prevent="runVerification">
        <el-form-item label="渠道"><el-select v-model="filters.channelId" filterable placeholder="选择渠道" :disabled="loading" style="width: 220px"><el-option v-for="channel in channels" :key="channel.id" :label="channel.name" :value="channel.id" /></el-select></el-form-item>
        <el-form-item label="申报模型"><el-select v-model="filters.modelId" filterable placeholder="选择 GPT-5.6 映射" :disabled="loading || !selectedChannel" style="width: 280px"><el-option v-for="model in availableModels" :key="model.id" :label="`${model.name} · ${model.upstreamModel}`" :value="model.id" /></el-select></el-form-item>
        <el-form-item label="档位"><el-radio-group v-model="filters.mode" size="small"><el-radio-button v-for="option in modeOptions" :key="option.value" :value="option.value">{{ option.label }}</el-radio-button></el-radio-group></el-form-item>
        <el-form-item><el-button type="primary" :icon="CircleCheck" :loading="starting" :disabled="!filters.channelId || !filters.modelId || Boolean(activeSession)" @click="runVerification">开始验证</el-button></el-form-item>
      </el-form>
      <div class="preset-line"><strong>{{ selectedMode.requests }} 个逻辑请求</strong><span>{{ selectedMode.scope }}</span><span>8 并发 · 每任务最多 3 次 HTTP 尝试</span></div>
    </section>

    <section v-if="activeSession" class="active-run" aria-label="运行中的验证">
      <div class="active-run-heading"><div><strong>{{ activeSession.channelName }} · {{ activeSession.publicModel }}</strong><span>{{ modeLabel(activeSession.mode) }}档 · {{ activeSession.completedCount }} / {{ activeSession.plannedCount }}</span></div><el-tag :type="sessionStatusType(activeSession.status)" effect="plain">{{ sessionStatusLabel(activeSession.status) }}</el-tag></div>
      <el-progress :percentage="activeSession.plannedCount ? Math.round(activeSession.completedCount / activeSession.plannedCount * 100) : 0" :stroke-width="8" />
      <div class="active-run-actions"><span>{{ activeSession.requestCount }} 次 HTTP 尝试 · {{ formatUSD(activeSession.upstreamCostMicros) }}</span><el-button text :icon="View" @click="openSession(activeSession)">查看进度</el-button></div>
    </section>

    <section class="verification-summary" aria-label="验证概览">
      <div class="summary-item"><span>检测会话</span><strong>{{ totalSessions }}</strong></div>
      <div class="summary-item"><span>最近结论</span><el-tag v-if="latestReport" :type="verdictType(latestReport.verdict)" effect="plain">{{ verdictLabel(latestReport.verdict) }}</el-tag><strong v-else>--</strong></div>
      <div class="summary-item"><span>逻辑 / HTTP</span><strong>{{ latestReport ? `${logicalRequestCount(latestReport.mode) ?? '--'} / ${latestReport.requestCount}` : '--' }}</strong></div>
      <div class="summary-item"><span>最近费用</span><strong>{{ latestReport ? formatUSD(latestReport.upstreamCostMicros) : '--' }}</strong></div>
    </section>

    <section class="table-surface">
      <div class="surface-heading"><div><h2>验证记录</h2><p class="surface-note">冻结基线 trusted-fingerprint-v3</p></div><el-button text :icon="Search" :disabled="loading" @click="loadVerificationData">刷新列表</el-button></div>
      <el-skeleton v-if="loading" :rows="6" animated />
      <el-table v-else-if="sessions.length" :data="sessions" row-key="id" table-layout="fixed">
        <el-table-column label="开始时间" min-width="168"><template #default="scope">{{ formatDate(scope.row.startedAt) }}</template></el-table-column>
        <el-table-column label="渠道 / 模型" min-width="220"><template #default="scope"><div class="model-cell"><strong>{{ scope.row.channelName }} · {{ scope.row.publicModel }}</strong><small>{{ scope.row.upstreamModel }}</small><el-tag v-if="scope.row.nativeTransportBackend === 'go_fallback'" type="warning" effect="plain" size="small">纯 Go 回退</el-tag></div></template></el-table-column>
        <el-table-column label="档位" width="72"><template #default="scope">{{ modeLabel(scope.row.mode) }}</template></el-table-column>
        <el-table-column label="任务进度" width="130" align="right"><template #default="scope"><div class="progress-cell"><strong>{{ scope.row.completedCount }} / {{ scope.row.plannedCount }}</strong><small>{{ scope.row.requestCount }} 次 HTTP</small></div></template></el-table-column>
        <el-table-column label="费用" width="120" align="right"><template #default="scope">{{ formatUSD(scope.row.upstreamCostMicros) }}</template></el-table-column>
        <el-table-column label="状态 / 结论" min-width="190"><template #default="scope"><el-tag v-if="scope.row.verdict" :type="verdictType(scope.row.verdict)" effect="plain">{{ verdictLabel(scope.row.verdict) }}</el-tag><el-tag v-else :type="sessionStatusType(scope.row.status)" effect="plain">{{ sessionStatusLabel(scope.row.status) }}</el-tag></template></el-table-column>
        <el-table-column label="操作" width="72" align="right"><template #default="scope"><el-button text :icon="View" aria-label="查看验证会话" title="查看" @click="openSession(scope.row)" /></template></el-table-column>
      </el-table>
      <el-empty v-else description="暂无验证记录" />
      <div v-if="totalSessions > pagination.pageSize" class="pagination-row"><el-pagination background layout="prev, pager, next" :current-page="pagination.page" :page-size="pagination.pageSize" :total="totalSessions" @current-change="handlePageChange" /></div>
    </section>

    <el-drawer v-model="drawerOpen" title="模型指纹验证详情" size="min(760px, 100vw)">
      <el-skeleton v-if="sessionLoading && !selectedSession" :rows="8" animated />
      <template v-else-if="selectedSession">
        <div class="report-header">
          <div><span class="report-channel">{{ selectedSession.session.channelName }}</span><h2>{{ selectedSession.session.publicModel }}</h2><p>{{ selectedSession.session.upstreamModel }} · {{ modeLabel(selectedSession.session.mode) }}档 · {{ formatDate(selectedSession.session.startedAt) }}</p></div>
          <div class="report-state"><el-tag :type="selectedReport ? verdictType(selectedReport.verdict) : sessionStatusType(selectedSession.session.status)" effect="plain">{{ selectedReport ? verdictLabel(selectedReport.verdict) : sessionStatusLabel(selectedSession.session.status) }}</el-tag></div>
        </div>

        <el-alert
          v-if="nativeGoFallbackActive"
          class="native-fallback-alert"
          title="Native Codex 正在使用纯 Go 回退传输"
          description="运行环境未检测到可用的 Node.js。TLS 传输指纹与默认 Node.js 路径存在差异；会话和每次 HTTP 尝试均已标记实际执行器。"
          type="warning"
          show-icon
          :closable="false"
        />

        <section v-if="['running', 'stopping'].includes(selectedSession.session.status)" class="run-progress-detail">
          <div><strong>{{ selectedSession.session.completedCount }} / {{ selectedSession.session.plannedCount }}</strong><span>逻辑任务</span></div>
          <el-progress :percentage="selectedSessionProgress" :stroke-width="9" />
          <div class="run-progress-footer"><span>{{ selectedSession.session.requestCount }} 次 HTTP 尝试 · {{ formatUSD(selectedSession.session.upstreamCostMicros) }}</span><el-button v-if="selectedSession.session.status === 'running'" type="danger" plain :icon="CloseBold" :loading="stopping" @click="stopSession">停止</el-button></div>
        </section>

        <template v-if="selectedReport">
          <div class="verdict-band"><strong>{{ selectedReport.title }}</strong><span>{{ selectedReport.summary }}</span></div>
          <div v-if="selectedReport.fingerprintClaimMismatch" class="fingerprint-mismatch" role="alert">
            <strong>模型指纹与申报不一致</strong>
            <span>申报 {{ modelLabel(selectedReport.claimedModel) }}，实际请求 {{ selectedReport.requestModel }}，行为指纹强烈指向 {{ modelLabel(selectedReport.fingerprintModel ?? '') }}。</span>
          </div>
          <section class="fingerprint-grid" aria-label="行为指纹匹配度">
            <div v-for="row in fingerprintRows" :key="row.model"><span>{{ modelLabel(row.model) }}</span><el-progress :percentage="Math.round(row.value * 1000) / 10" :show-text="false" :stroke-width="7" /><strong>{{ (row.value * 100).toFixed(3) }}%</strong></div>
          </section>
          <div class="check-list">
            <article v-for="check in selectedReport.checks" :key="check.name" class="check-row">
              <div class="check-heading"><div><strong>{{ check.label }}</strong><small>{{ check.category }}</small></div><el-tag :type="checkStatusType(check)" effect="plain">{{ checkStatusLabel(check) }}</el-tag></div>
              <p>{{ check.summary }}</p><details v-if="evidenceText(check)"><summary>技术证据</summary><pre class="evidence-block">{{ evidenceText(check) }}</pre></details>
            </article>
          </div>
        </template>

        <section class="session-overview" aria-label="验证会话汇总">
          <div><span>HTTP 尝试</span><strong>{{ selectedSession.session.requestCount }}</strong></div><div><span>成功响应</span><strong>{{ selectedSession.session.successCount }}</strong></div><div><span>Token</span><strong>{{ formatCompactNumber(selectedSession.session.inputTokens + selectedSession.session.outputTokens) }}</strong></div><div><span>上游费用</span><strong>{{ formatUSD(selectedSession.session.upstreamCostMicros) }}</strong></div>
        </section>

        <section class="session-calls" aria-label="验证调用记录">
          <div class="section-label"><strong>HTTP 尝试</strong><span>同一逻辑任务的重试共享 job ID</span></div>
          <el-empty v-if="!selectedSession.attempts.length" description="尚无调用记录" />
          <el-collapse v-else accordion>
            <el-collapse-item v-for="attempt in selectedSession.attempts" :key="attempt.id" :name="attempt.id">
              <template #title><div class="attempt-title-row"><span class="attempt-name">#{{ attempt.sequence }}.{{ attempt.attemptNo }} · {{ detectorLabel(attempt.probeId) }}</span><el-tag v-if="attempt.transportBackend === 'go_fallback'" type="warning" effect="plain" size="small">纯 Go 回退</el-tag><el-tag :type="attemptStatusType(attempt)" effect="plain" size="small">{{ attemptStatusLabel(attempt) }}</el-tag><strong>{{ formatUSD(attempt.upstreamCostMicros) }}</strong></div></template>
              <div class="attempt-meta"><span>{{ requestProfile(attempt) }}</span><span>{{ formatDuration(attempt.latencyMs) }}</span><span>{{ attempt.jobId }}</span></div>
              <div class="attempt-metrics"><span>输入 {{ formatCompactNumber(attempt.inputTokens) }}</span><span>输出 {{ formatCompactNumber(attempt.outputTokens) }}</span><span>缓存读 {{ formatCompactNumber(attempt.cachedTokens) }}</span><span>估算 {{ formatUSD(attempt.estimatedCostMicros) }}</span></div>
              <p v-if="attempt.errorMessage" class="attempt-error-text">{{ attempt.errorMessage }}</p>
              <div class="attempt-bodies"><details><summary>请求体<span v-if="attempt.requestBodyTruncated">（已截断）</span></summary><pre>{{ formatAttemptBody(attempt.requestBody) }}</pre></details><details><summary>原始 SSE<span v-if="attempt.responseBodyTruncated">（已截断）</span></summary><pre>{{ formatAttemptBody(attempt.responseBody) }}</pre></details></div>
            </el-collapse-item>
          </el-collapse>
        </section>
      </template>
    </el-drawer>
  </div>
</template>

<style scoped>
.verification-page { min-width: 0; }
.page-heading { display: flex; align-items: center; justify-content: space-between; gap: 20px; }
.heading-copy { display: flex; min-width: 0; align-items: center; gap: 11px; }
.heading-icon { display: grid; width: 34px; height: 34px; flex: 0 0 34px; place-items: center; border: 1px solid var(--hongfen-border); border-radius: 6px; background: var(--hongfen-primary-soft); color: var(--hongfen-primary); }
.heading-icon :deep(svg) { width: 17px; height: 17px; }
.eyebrow { display: flex; align-items: center; gap: 6px; color: var(--hongfen-primary); font-size: 12px; font-weight: 700; }
.page-heading h1 { margin: 2px 0 0; color: var(--hongfen-text); font-size: 24px; }
.verification-toolbar, .verification-summary, .table-surface, .active-run { border: 1px solid var(--hongfen-border); border-radius: 6px; background: var(--hongfen-surface); }
.verification-toolbar { padding: 14px 16px 10px; }
.verification-toolbar :deep(.el-form-item) { margin-bottom: 8px; }
.preset-line { display: flex; flex-wrap: wrap; gap: 7px 18px; color: var(--hongfen-text-muted); font-size: 11px; }
.preset-line strong { color: var(--hongfen-primary); }
.active-run { display: grid; gap: 10px; padding: 14px 16px; }
.active-run-heading, .active-run-actions, .run-progress-footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.active-run-heading > div { display: grid; gap: 3px; }
.active-run-heading span, .active-run-actions > span, .run-progress-footer > span { color: var(--hongfen-text-muted); font-size: 11px; }
.verification-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; overflow: hidden; background: var(--hongfen-border); }
.summary-item { display: grid; align-content: center; gap: 6px; min-height: 76px; padding: 12px 16px; background: var(--hongfen-surface); }
.summary-item > span { color: var(--hongfen-text-muted); font-size: 12px; }
.summary-item > strong { color: var(--hongfen-text); font-size: 20px; font-variant-numeric: tabular-nums; }
.table-surface { padding: 16px; }
.surface-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 12px; }
.surface-heading h2 { margin: 0; color: var(--hongfen-text); font-size: 16px; }
.surface-note { margin: 3px 0 0; color: var(--hongfen-text-muted); font-size: 11px; }
.model-cell, .progress-cell { display: grid; gap: 2px; }
.model-cell small, .progress-cell small { color: var(--hongfen-text-muted); font-size: 11px; }
.progress-cell { justify-items: end; font-variant-numeric: tabular-nums; }
.pagination-row { display: flex; justify-content: flex-end; padding-top: 16px; }
.report-header { display: flex; justify-content: space-between; gap: 16px; border-bottom: 1px solid var(--hongfen-border); padding-bottom: 15px; }
.report-channel { color: var(--hongfen-primary); font-size: 12px; font-weight: 700; }
.report-header h2 { margin: 4px 0; color: var(--hongfen-text); font-size: 21px; }
.report-header p { margin: 0; color: var(--hongfen-text-muted); font-size: 12px; }
.native-fallback-alert { margin: 14px 0; }
.run-progress-detail { display: grid; gap: 12px; margin: 16px 0; border-bottom: 1px solid var(--hongfen-border); padding-bottom: 16px; }
.run-progress-detail > div:first-child { display: flex; align-items: baseline; gap: 8px; }
.run-progress-detail > div:first-child strong { color: var(--hongfen-primary); font-size: 22px; font-variant-numeric: tabular-nums; }
.run-progress-detail > div:first-child span { color: var(--hongfen-text-muted); font-size: 11px; }
.verdict-band { display: grid; gap: 6px; margin: 16px 0 0; border-left: 3px solid var(--hongfen-primary); padding: 8px 12px; background: var(--hongfen-surface-muted); }
.verdict-band strong { color: var(--hongfen-text); font-size: 15px; }
.verdict-band span { color: var(--hongfen-text-muted); font-size: 12px; line-height: 1.5; }
.fingerprint-mismatch { display: grid; gap: 4px; margin-top: 10px; border: 1px solid var(--hongfen-danger-border); border-radius: 4px; padding: 9px 12px; background: var(--hongfen-danger-soft); }
.fingerprint-mismatch strong { color: var(--hongfen-danger); font-size: 13px; }
.fingerprint-mismatch span { color: var(--hongfen-text); font-size: 12px; line-height: 1.5; }
.fingerprint-grid { display: grid; gap: 8px; margin: 16px 0; }
.fingerprint-grid > div { display: grid; grid-template-columns: 48px minmax(0, 1fr) 76px; align-items: center; gap: 10px; }
.fingerprint-grid span { color: var(--hongfen-text); font-size: 12px; }
.fingerprint-grid strong { text-align: right; font: 11px var(--hongfen-font-mono); font-variant-numeric: tabular-nums; }
.check-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; margin-bottom: 18px; }
.check-row { min-width: 0; border: 1px solid var(--hongfen-border); border-radius: 6px; padding: 12px; background: var(--hongfen-surface-muted); }
.check-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 10px; }
.check-heading > div { display: grid; gap: 3px; }
.check-heading small { color: var(--hongfen-text-muted); font-size: 11px; }
.check-row p { margin: 9px 0; color: var(--hongfen-text-muted); font-size: 12px; line-height: 1.5; }
.check-row summary { cursor: pointer; color: var(--hongfen-primary); font-size: 11px; }
.evidence-block { max-height: 220px; overflow: auto; margin: 8px 0 0; padding: 9px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); color: var(--hongfen-text); font: 11px/1.5 var(--hongfen-font-mono); white-space: pre-wrap; word-break: break-word; }
.session-overview { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; margin-bottom: 18px; border: 1px solid var(--hongfen-border); background: var(--hongfen-border); }
.session-overview > div { display: grid; gap: 5px; padding: 10px 12px; background: var(--hongfen-surface-muted); }
.session-overview span { color: var(--hongfen-text-muted); font-size: 11px; }
.session-overview strong { color: var(--hongfen-text); font: 13px var(--hongfen-font-mono); font-variant-numeric: tabular-nums; }
.session-calls { margin-bottom: 20px; }
.section-label { display: flex; align-items: baseline; gap: 8px; margin-bottom: 8px; color: var(--hongfen-text); }
.section-label span { color: var(--hongfen-text-muted); font-size: 11px; }
.attempt-title-row { display: flex; align-items: center; gap: 9px; width: 100%; padding-right: 8px; color: var(--hongfen-text); font-size: 12px; }
.attempt-title-row > .attempt-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attempt-title-row strong { margin-left: auto; font: 11px var(--hongfen-font-mono); }
.attempt-meta, .attempt-metrics { display: flex; flex-wrap: wrap; gap: 5px 14px; color: var(--hongfen-text-muted); font-size: 11px; }
.attempt-meta span:last-child { overflow-wrap: anywhere; }
.attempt-metrics { margin-top: 8px; color: var(--hongfen-text); font-family: var(--hongfen-font-mono); }
.attempt-error-text { margin: 9px 0 0; color: var(--hongfen-danger); font-size: 11px; overflow-wrap: anywhere; }
.attempt-bodies { display: grid; gap: 8px; margin-top: 12px; }
.attempt-bodies details { border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.attempt-bodies summary { cursor: pointer; padding: 7px 9px; color: var(--hongfen-text); font-size: 11px; }
.attempt-bodies pre { max-height: 230px; overflow: auto; margin: 0; padding: 9px; border-top: 1px solid var(--hongfen-border); color: var(--hongfen-text); font: 11px/1.5 var(--hongfen-font-mono); white-space: pre-wrap; word-break: break-word; }
@media (max-width: 760px) {
  .page-heading { flex-direction: row; align-items: center; }
  .heading-copy > div:last-child { min-width: 0; }
  .page-heading h1 { font-size: 20px; line-height: 1.3; }
  .page-refresh-button { flex: 0 0 auto; }
  .verification-summary, .session-overview, .check-list { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .verification-toolbar :deep(.el-form-item) { width: 100%; }
  .verification-toolbar :deep(.el-select) { width: 100% !important; }
  .surface-heading, .report-header { align-items: flex-start; }
}
@media (max-width: 480px) {
  .check-list { grid-template-columns: 1fr; }
  .fingerprint-grid > div { grid-template-columns: 42px minmax(0, 1fr) 68px; }
}
</style>
