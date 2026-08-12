<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Aim, CircleCheck, Refresh, Search, View } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import type {
  Channel,
  GatewayModel,
  VerificationCheckResult,
  VerificationMode,
  VerificationReport,
  VerificationReportPage,
  VerificationAttempt,
  VerificationSessionDetail,
  VerificationVerdict,
} from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration, formatUSD } from '@/utils/formatters'

const loading = ref(true)
const route = useRoute()
const errorMessage = ref('')
const verifying = ref(false)
const channels = ref<Channel[]>([])
const models = ref<GatewayModel[]>([])
const reports = ref<VerificationReport[]>([])
const totalReports = ref(0)
const selectedReport = ref<VerificationReport | null>(null)
const drawerOpen = ref(false)
const sessionLoading = ref(false)
const selectedSession = ref<VerificationSessionDetail | null>(null)
const optionsReady = ref(false)
const filters = reactive({ channelId: 0, modelId: 0, mode: 'standard' as VerificationMode })
const pagination = reactive({ page: 1, pageSize: 20 })

const modeOptions: Array<{ label: string; value: VerificationMode }> = [
  { label: '快速', value: 'quick' },
  { label: '标准', value: 'standard' },
  { label: '完整', value: 'full' },
]

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
const latestReport = computed(() => reports.value[0] ?? null)
const passedChecks = computed(() => latestReport.value?.checks.filter((check) => check.status === 'passed').length ?? 0)
const latestScoreLabel = computed(() => latestReport.value ? `${latestReport.value.score.toFixed(1)} 分` : '--')

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function verdictLabel(value: VerificationVerdict): string {
  if (value === 'passed') return '通过'
  if (value === 'marginal') return '基本合格'
  return '未通过'
}

function verdictType(value: VerificationVerdict): 'success' | 'warning' | 'danger' {
  if (value === 'passed') return 'success'
  if (value === 'marginal') return 'warning'
  return 'danger'
}

function checkStatusLabel(check: VerificationCheckResult): string {
  if (check.status === 'passed') return '通过'
  if (check.status === 'skipped') return '跳过'
  return '未通过'
}

function checkStatusType(check: VerificationCheckResult): 'success' | 'warning' | 'danger' {
  if (check.status === 'passed') return 'success'
  if (check.status === 'skipped') return 'warning'
  return 'danger'
}

function evidenceText(check: VerificationCheckResult): string {
  return check.evidence ? JSON.stringify(check.evidence, null, 2) : ''
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
    basic_request: '基础请求',
    model_consistency: 'model 字段一致性',
    stream_integrity: '流式协议完整性',
    usage_billing: 'usage 计费合理性',
    tool_calls: 'Tool Calls',
    structured_output: 'Structured Output',
  }
  return labels[name] ?? name
}

function formatAttemptBody(body: string): string {
  if (!body) return '(空响应)'
  try { return JSON.stringify(JSON.parse(body), null, 2) } catch { return body }
}

function selectFirstModel() {
  if (!availableModels.value.some((model) => model.id === filters.modelId)) {
    filters.modelId = availableModels.value[0]?.id ?? 0
  }
}

watch(() => filters.channelId, () => {
  selectFirstModel()
  pagination.page = 1
  if (optionsReady.value) void loadReports()
})

watch(() => filters.modelId, () => {
  pagination.page = 1
  if (optionsReady.value) void loadReports()
})

async function loadReports() {
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.channelId) query.set('channelId', String(filters.channelId))
  if (filters.modelId) query.set('modelId', String(filters.modelId))
  const page = await request<VerificationReportPage>(`/admin/gateway/verifications?${query}`)
  reports.value = page.items
  totalReports.value = page.total
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
    if (Number.isInteger(requestedChannelId) && requestedChannelId > 0 && channels.value.some((channel) => channel.id === requestedChannelId)) {
      filters.channelId = requestedChannelId
    }
    if (!filters.channelId || !channels.value.some((channel) => channel.id === filters.channelId)) {
      filters.channelId = channels.value.find((channel) => channel.enabled)?.id ?? channels.value[0]?.id ?? 0
    }
    selectFirstModel()
    optionsReady.value = true
    await loadReports()
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '渠道验证数据加载失败'
  } finally {
    loading.value = false
  }
}

async function loadSession(sessionId: string) {
  sessionLoading.value = true
  try {
    selectedSession.value = await request<VerificationSessionDetail>(`/admin/gateway/verification-sessions/${encodeURIComponent(sessionId)}`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '验证调用记录加载失败')
  } finally {
    sessionLoading.value = false
  }
}

async function runVerification() {
  if (!filters.channelId || !filters.modelId) {
    ElMessage.warning('请选择渠道和已启用的模型映射')
    return
  }
  verifying.value = true
  try {
    const report = await request<VerificationReport>('/admin/gateway/verifications', {
      method: 'POST',
      body: JSON.stringify({ channelId: filters.channelId, modelId: filters.modelId, mode: filters.mode }),
    })
    selectedReport.value = report
    selectedSession.value = null
    drawerOpen.value = true
    if (report.sessionId) await loadSession(report.sessionId)
    ElMessage.success(`验证完成，${report.score.toFixed(1)} 分，${verdictLabel(report.verdict)}`)
    await loadReports()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '渠道验证失败')
  } finally {
    verifying.value = false
  }
}

async function openReport(report: VerificationReport) {
  selectedReport.value = report
  selectedSession.value = null
  drawerOpen.value = true
  if (report.sessionId) await loadSession(report.sessionId)
}

function handlePageChange(page: number) {
  pagination.page = page
  void loadReports()
}

onMounted(() => {
  void loadData()
})
</script>

<template>
  <div class="page-stack verification-page">
    <header class="page-heading">
      <div>
        <div class="eyebrow"><Aim :size="14" /> 渠道质量</div>
        <h1>渠道验证</h1>
      </div>
      <el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新验证数据" @click="loadData" />
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert">
      <strong>验证数据加载失败</strong><span>{{ errorMessage }}</span>
      <el-button :loading="loading" @click="loadData">重试</el-button>
    </div>

    <section class="verification-toolbar" aria-label="验证参数">
      <el-form inline @submit.prevent="runVerification">
        <el-form-item label="渠道">
          <el-select v-model="filters.channelId" filterable placeholder="选择渠道" :disabled="loading" style="width: 220px">
            <el-option v-for="channel in channels" :key="channel.id" :label="channel.name" :value="channel.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="模型">
          <el-select v-model="filters.modelId" filterable placeholder="选择模型映射" :disabled="loading || !selectedChannel" style="width: 260px">
            <el-option v-for="model in availableModels" :key="model.id" :label="`${model.name} · ${model.upstreamModel}`" :value="model.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="模式">
          <el-radio-group v-model="filters.mode" size="small">
            <el-radio-button v-for="option in modeOptions" :key="option.value" :value="option.value">{{ option.label }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :icon="CircleCheck" :loading="verifying" :disabled="!filters.channelId || !filters.modelId" @click="runVerification">开始验证</el-button>
        </el-form-item>
      </el-form>
    </section>

    <section class="verification-summary" aria-label="最近验证概览">
      <div class="summary-item"><span>报告总数</span><strong>{{ totalReports }}</strong></div>
      <div class="summary-item"><span>最近得分</span><strong>{{ latestScoreLabel }}</strong></div>
      <div class="summary-item"><span>最近结果</span><el-tag v-if="latestReport" :type="verdictType(latestReport.verdict)" effect="plain">{{ verdictLabel(latestReport.verdict) }}</el-tag><strong v-else>--</strong></div>
      <div class="summary-item"><span>通过检测</span><strong>{{ latestReport ? `${passedChecks} / ${latestReport.checks.length}` : '--' }}</strong></div>
    </section>

    <section class="table-surface">
      <div class="surface-heading">
        <div><h2>验证会话与报告</h2><p class="surface-note">每次验证都会独立记录调用明细和费用</p></div>
        <el-button text :icon="Search" :disabled="loading" @click="loadReports">刷新列表</el-button>
      </div>
      <el-skeleton v-if="loading" :rows="6" animated />
      <el-table v-else-if="reports.length" :data="reports" row-key="id" table-layout="fixed">
        <el-table-column label="时间" min-width="168"><template #default="scope">{{ formatDate(scope.row.finishedAt) }}</template></el-table-column>
        <el-table-column label="渠道" min-width="140" prop="channelName" />
        <el-table-column label="模型" min-width="220"><template #default="scope"><div class="model-cell"><strong>{{ scope.row.publicModel }}</strong><small>{{ scope.row.upstreamModel }}</small></div></template></el-table-column>
        <el-table-column label="模式" width="86"><template #default="scope">{{ modeOptions.find((option) => option.value === scope.row.mode)?.label ?? scope.row.mode }}</template></el-table-column>
        <el-table-column label="得分" width="100" align="right"><template #default="scope"><strong class="score-value">{{ scope.row.score.toFixed(1) }}</strong></template></el-table-column>
        <el-table-column label="调用 / 费用" width="150" align="right"><template #default="scope"><div class="cost-cell"><strong>{{ scope.row.requestCount }} 次</strong><small>{{ formatUSD(scope.row.upstreamCostMicros) }}</small></div></template></el-table-column>
        <el-table-column label="结果" width="112"><template #default="scope"><el-tag :type="verdictType(scope.row.verdict)" effect="plain">{{ verdictLabel(scope.row.verdict) }}</el-tag></template></el-table-column>
        <el-table-column label="操作" width="90" align="right"><template #default="scope"><el-button text :icon="View" aria-label="查看验证报告" title="查看验证报告" @click="openReport(scope.row)" /></template></el-table-column>
      </el-table>
      <el-empty v-else description="暂无验证报告" />
      <div v-if="totalReports > pagination.pageSize" class="pagination-row"><el-pagination background layout="prev, pager, next" :current-page="pagination.page" :page-size="pagination.pageSize" :total="totalReports" @current-change="handlePageChange" /></div>
    </section>

    <el-drawer v-model="drawerOpen" title="验证报告详情" size="min(700px, 100vw)">
      <template v-if="selectedReport">
        <div class="report-header">
          <div><span class="report-channel">{{ selectedReport.channelName }}</span><h2>{{ selectedReport.publicModel }}</h2><p>{{ selectedReport.upstreamModel }} · {{ formatDate(selectedReport.finishedAt) }}</p></div>
          <div class="report-score"><strong>{{ selectedReport.score.toFixed(1) }}</strong><el-tag :type="verdictType(selectedReport.verdict)" effect="plain">{{ verdictLabel(selectedReport.verdict) }}</el-tag></div>
        </div>
        <p class="report-summary">{{ selectedReport.summary }}</p>
        <section class="session-overview" aria-label="验证会话汇总">
          <div><span>会话调用</span><strong>{{ selectedSession?.session.requestCount ?? selectedReport.requestCount }} 次</strong></div>
          <div><span>Token</span><strong>{{ formatCompactNumber((selectedSession?.session.inputTokens ?? selectedReport.inputTokens) + (selectedSession?.session.outputTokens ?? selectedReport.outputTokens)) }}</strong></div>
          <div><span>上游费用</span><strong>{{ formatUSD(selectedSession?.session.upstreamCostMicros ?? selectedReport.upstreamCostMicros) }}</strong></div>
          <div><span>本地估算</span><strong>{{ formatUSD(selectedSession?.session.estimatedCostMicros ?? selectedReport.estimatedCostMicros) }}</strong></div>
        </section>
        <section class="session-calls" aria-label="验证调用记录">
          <div class="section-label"><strong>调用记录</strong><span v-if="selectedSession">按检测器顺序保留请求、响应和费用证据</span><el-skeleton v-else-if="sessionLoading" :rows="1" animated /></div>
          <el-empty v-if="selectedSession && !selectedSession.attempts.length" description="暂无调用记录" />
          <el-collapse v-else-if="selectedSession" accordion>
            <el-collapse-item v-for="attempt in selectedSession.attempts" :key="attempt.id" :name="attempt.id">
              <template #title>
                <div class="attempt-title-row"><span>#{{ attempt.sequence }} · {{ detectorLabel(attempt.detectorName) }}</span><el-tag :type="attemptStatusType(attempt)" effect="plain" size="small">{{ attemptStatusLabel(attempt) }}</el-tag><strong>{{ formatUSD(attempt.upstreamCostMicros) }}</strong></div>
              </template>
              <div class="attempt-meta"><span>{{ attempt.stream ? '流式 SSE' : '非流式' }} · {{ attempt.path }}</span><span>{{ formatDuration(attempt.latencyMs) }}</span><span>usage {{ attempt.usageSource || '未知' }}</span></div>
              <div class="attempt-metrics"><span>输入 {{ formatCompactNumber(attempt.inputTokens) }}</span><span>输出 {{ formatCompactNumber(attempt.outputTokens) }}</span><span>缓存读 {{ formatCompactNumber(attempt.cachedTokens) }}</span><span>估算 {{ formatUSD(attempt.estimatedCostMicros) }}</span></div>
              <p v-if="attempt.errorMessage" class="attempt-error-text">{{ attempt.errorMessage }}</p>
              <div class="attempt-bodies">
                <details open><summary>请求体<span v-if="attempt.requestBodyTruncated">（已截断）</span></summary><pre>{{ formatAttemptBody(attempt.requestBody) }}</pre></details>
                <details><summary>响应体<span v-if="attempt.responseBodyTruncated">（已截断）</span></summary><pre>{{ formatAttemptBody(attempt.responseBody) }}</pre></details>
              </div>
            </el-collapse-item>
          </el-collapse>
        </section>
        <div class="check-list">
          <article v-for="check in selectedReport.checks" :key="check.name" class="check-row">
            <div class="check-heading"><div><strong>{{ check.label }}</strong><small>{{ check.category }} · 权重 {{ check.weight }}</small></div><el-tag :type="checkStatusType(check)" effect="plain">{{ checkStatusLabel(check) }} {{ check.score.toFixed(0) }}</el-tag></div>
            <p>{{ check.summary }}</p>
            <pre v-if="evidenceText(check)" class="evidence-block">{{ evidenceText(check) }}</pre>
          </article>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<style scoped>
.verification-page { min-width: 0; }
.page-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; }
.eyebrow { display: flex; align-items: center; gap: 6px; color: var(--hongfen-primary); font-size: 12px; font-weight: 700; }
.page-heading h1 { margin: 4px 0 4px; color: var(--hongfen-text); font-size: 24px; }
.verification-toolbar, .verification-summary, .table-surface { border: 1px solid var(--hongfen-border); border-radius: 6px; background: var(--hongfen-surface); }
.verification-toolbar { padding: 14px 16px 8px; }
.verification-toolbar :deep(.el-form-item) { margin-bottom: 8px; }
.verification-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; overflow: hidden; background: var(--hongfen-border); }
.summary-item { display: grid; gap: 5px; min-height: 76px; padding: 14px 16px; background: var(--hongfen-surface); }
.summary-item span { color: var(--hongfen-text-muted); font-size: 12px; }
.summary-item strong { color: var(--hongfen-text); font-size: 20px; font-variant-numeric: tabular-nums; }
.table-surface { padding: 16px; }
.surface-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 12px; }
.surface-heading h2 { margin: 0; color: var(--hongfen-text); font-size: 16px; }
.surface-note { margin: 3px 0 0; color: var(--hongfen-text-muted); font-size: 11px; }
.cost-cell { display: grid; gap: 2px; justify-items: end; font-variant-numeric: tabular-nums; }
.cost-cell strong { color: var(--hongfen-text); font-size: 11px; }
.cost-cell small { color: var(--hongfen-text-muted); font: 11px var(--hongfen-font-mono); }
.model-cell { display: grid; gap: 2px; }
.model-cell small { color: var(--hongfen-text-muted); font-size: 11px; }
.score-value { color: var(--hongfen-primary); font-variant-numeric: tabular-nums; }
.pagination-row { display: flex; justify-content: flex-end; padding-top: 16px; }
.report-header { display: flex; justify-content: space-between; gap: 16px; border-bottom: 1px solid var(--hongfen-border); padding-bottom: 16px; }
.report-channel { color: var(--hongfen-primary); font-size: 12px; font-weight: 700; }
.report-header h2 { margin: 4px 0; color: var(--hongfen-text); font-size: 21px; }
.report-header p { margin: 0; color: var(--hongfen-text-muted); font-size: 12px; }
.report-score { display: grid; justify-items: end; align-content: start; gap: 8px; }
.report-score strong { color: var(--hongfen-primary); font-size: 30px; font-variant-numeric: tabular-nums; }
.report-summary { margin: 16px 0; color: var(--hongfen-text); font-size: 13px; }
.session-overview { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1px; margin: 0 0 18px; border: 1px solid var(--hongfen-border); background: var(--hongfen-border); }
.session-overview > div { display: grid; gap: 5px; padding: 10px 12px; background: var(--hongfen-surface-muted); }
.session-overview span { color: var(--hongfen-text-muted); font-size: 11px; }
.session-overview strong { color: var(--hongfen-text); font-family: var(--hongfen-font-mono); font-size: 13px; font-variant-numeric: tabular-nums; }
.session-calls { margin: 0 0 20px; }
.section-label { display: flex; align-items: baseline; gap: 8px; min-height: 25px; margin-bottom: 8px; color: var(--hongfen-text); }
.section-label span { color: var(--hongfen-text-muted); font-size: 11px; }
.attempt-title-row { display: flex; align-items: center; gap: 9px; width: 100%; padding-right: 8px; color: var(--hongfen-text); font-size: 12px; }
.attempt-title-row > span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.attempt-title-row strong { margin-left: auto; color: var(--hongfen-text); font-family: var(--hongfen-font-mono); font-size: 11px; font-variant-numeric: tabular-nums; }
.attempt-meta, .attempt-metrics { display: flex; flex-wrap: wrap; gap: 5px 14px; color: var(--hongfen-text-muted); font-size: 11px; }
.attempt-metrics { margin-top: 8px; color: var(--hongfen-text); font-family: var(--hongfen-font-mono); font-variant-numeric: tabular-nums; }
.attempt-error-text { margin: 9px 0 0; color: var(--hongfen-danger); font-size: 11px; overflow-wrap: anywhere; }
.attempt-bodies { display: grid; gap: 8px; margin-top: 12px; }
.attempt-bodies details { border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.attempt-bodies summary { cursor: pointer; padding: 7px 9px; color: var(--hongfen-text); font-size: 11px; }
.attempt-bodies summary span { color: var(--hongfen-warning); }
.attempt-bodies pre { max-height: 230px; overflow: auto; margin: 0; padding: 9px; border-top: 1px solid var(--hongfen-border); color: var(--hongfen-text); font: 11px/1.5 var(--hongfen-font-mono); white-space: pre-wrap; word-break: break-word; }
.check-list { display: grid; gap: 10px; }
.check-row { border: 1px solid var(--hongfen-border); border-radius: 6px; padding: 12px; background: var(--hongfen-surface-muted); }
.check-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.check-heading div { display: grid; gap: 3px; }
.check-heading small { color: var(--hongfen-text-muted); font-size: 11px; }
.check-row p { margin: 10px 0 0; color: var(--hongfen-text-muted); font-size: 12px; line-height: 1.55; }
.evidence-block { max-height: 220px; overflow: auto; margin: 10px 0 0; padding: 10px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); color: var(--hongfen-text); font-family: var(--hongfen-font-mono); font-size: 11px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; }
@media (max-width: 760px) {
  .verification-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .session-overview { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .verification-toolbar :deep(.el-form-item) { width: 100%; }
  .verification-toolbar :deep(.el-select) { width: 100% !important; }
  .surface-heading { align-items: flex-start; }
  .report-header { align-items: flex-start; }
}
</style>
