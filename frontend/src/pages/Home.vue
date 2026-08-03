<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowDown, Clock, Coin, Connection, CopyDocument, DataLine, Key, List, MagicStick, Odometer, PieChart, Refresh, Tickets, Timer } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import ChannelDistributionChart from '@/pages/home/ChannelDistributionChart.vue'
import type { ChannelShareMetric } from '@/pages/home/ChannelDistributionChart.vue'
import RequestTrendChart from '@/pages/home/RequestTrendChart.vue'
import type {
  Channel,
  ClientToken,
  CodexConfigurationSaveRequest,
  CodexLocalConfiguration,
  CodexProviderMode,
  CodexTokenMode,
  DashboardSummary,
  GatewayModel,
} from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'
import { isLocalBrowserAccess } from '@/utils/localAccess'

const router = useRouter()
const loading = ref(true)
const errorMessage = ref('')
const dashboard = ref<DashboardSummary | null>(null)
const channels = ref<Channel[]>([])
const models = ref<GatewayModel[]>([])
const tokens = ref<ClientToken[]>([])
const defaultCodexModel = 'gpt-5.6-sol'
const selectedModel = ref('')
const serviceBaseUrl = ref(typeof window === 'undefined' ? '/v1' : `${window.location.origin}/v1`)
const copyingConfig = ref(false)
const quickStartExpanded = ref(false)
const codexDialogOpen = ref(false)
const codexConfigLoading = ref(false)
const codexConfigSaving = ref(false)
const codexConfigError = ref('')
const localCodexConfig = ref<CodexLocalConfiguration | null>(null)
const providerMode = ref<CodexProviderMode>('existing')
const existingProviderName = ref('')
const newProviderName = ref('gateway')
const tokenMode = ref<CodexTokenMode>('new')
const selectedTokenId = ref<number | null>(null)
const newTokenName = ref('本机 Codex')
const localBrowserAccess = isLocalBrowserAccess()

type DashboardRangeDays = 1 | 2 | 3 | 5
type TrendDimension = 'hour' | 'day'
type ChannelDistributionView = 'list' | 'pie'

const selectedRangeDays = ref<DashboardRangeDays>(1)
const trendDimension = ref<TrendDimension>('hour')
const channelDistributionView = ref<ChannelDistributionView>('list')
const channelShareMetric = ref<ChannelShareMetric>('requests')
const distributionPageSize = 5
const channelDistributionPage = ref(1)
const modelDistributionPage = ref(1)
const timeRangeOptions: Array<{ label: string; value: DashboardRangeDays }> = [
  { label: '当前', value: 1 },
  { label: '最近两天', value: 2 },
  { label: '最近三天', value: 3 },
  { label: '最近五天', value: 5 },
]

const totalTokens = computed(() => (dashboard.value?.inputTokens ?? 0) + (dashboard.value?.outputTokens ?? 0))
const topTokenModels = computed(() => [...(dashboard.value?.models ?? [])]
  .sort((left, right) => (right.inputTokens + right.outputTokens) - (left.inputTokens + left.outputTokens)
    || right.requests - left.requests
    || left.name.localeCompare(right.name))
  .slice(0, 4))
const topCacheChannels = computed(() => [...(dashboard.value?.channels ?? [])]
  .filter((channel) => channel.inputTokens > 0 && channel.cachedTokens > 0)
  .sort((left, right) => right.cacheHitRate - left.cacheHitRate
    || right.cachedTokens - left.cachedTokens
    || left.name.localeCompare(right.name))
  .slice(0, 3))
const topCostModels = computed(() => (dashboard.value?.models ?? []).slice(0, 5))
const topCostRatios = computed(() => dashboard.value?.costRatios ?? [])
const selectedRangeLabel = computed(() => timeRangeOptions.find((option) => option.value === selectedRangeDays.value)?.label ?? '当前')
const availableChannels = computed(() => channels.value.filter((channel) => (
  channel.enabled && (!channel.circuitOpenUntil || Date.parse(channel.circuitOpenUntil) <= Date.now())
)).length)
const trendDimensionOptions: Array<{ label: string; value: TrendDimension }> = [
  { label: '小时', value: 'hour' },
  { label: '天', value: 'day' },
]
const channelShareMetricOptions: Array<{ label: string; value: ChannelShareMetric }> = [
  { label: '请求次数', value: 'requests' },
  { label: '总费用', value: 'cost' },
]
const requestTrendPoints = computed(() => trendDimension.value === 'hour'
  ? (dashboard.value?.hourly ?? []).map((hour) => ({
      key: hour.hour,
      label: formatHour(hour.hour),
      requests: hour.requests,
      successes: hour.successes,
    }))
  : (dashboard.value?.daily ?? []).map((day) => ({
      key: day.date,
      label: formatDate(day.date),
      requests: day.requests,
      successes: day.successes,
    })))
const requestTrendAriaLabel = computed(() => `${selectedRangeLabel.value}请求${trendDimension.value === 'hour' ? '小时' : '天'}维度折线图`)
const channelDistributionAriaLabel = computed(() => `${selectedRangeLabel.value}渠道${channelShareMetric.value === 'requests' ? '请求次数' : '总费用'}占比饼图`)
const channelDistributionTotal = computed(() => dashboard.value?.channels.length ?? 0)
const modelDistributionTotal = computed(() => dashboard.value?.models.length ?? 0)
const paginatedChannels = computed(() => {
  const start = (channelDistributionPage.value - 1) * distributionPageSize
  return (dashboard.value?.channels ?? []).slice(start, start + distributionPageSize)
})
const paginatedModels = computed(() => {
  const start = (modelDistributionPage.value - 1) * distributionPageSize
  return (dashboard.value?.models ?? []).slice(start, start + distributionPageSize)
})
const readyChannels = computed(() => channels.value.filter((channel) => channel.enabled && (!channel.circuitOpenUntil || Date.parse(channel.circuitOpenUntil) <= Date.now())))
const readyModels = computed(() => models.value.filter((model) => model.enabled && readyChannels.value.some((channel) => channel.models.some((mapping) => mapping.enabled && mapping.modelId === model.id))))
const readyTokens = computed(() => tokens.value.filter((token) => token.enabled))
const activeProviderName = computed(() => providerMode.value === 'existing' ? existingProviderName.value : newProviderName.value.trim())
const currentCodexProvider = computed(() => localCodexConfig.value?.providers.find((provider) => provider.name === localCodexConfig.value?.modelProvider) ?? null)
const canReuseCurrentToken = computed(() => Boolean(
  localCodexConfig.value?.authTokenId
  && tokens.value.some((token) => token.id === localCodexConfig.value?.authTokenId && token.enabled),
))
const codexConfig = computed(() => `model_provider = "custom"
model = "${selectedModel.value || defaultCodexModel}"
network_access = "enabled"
windows_wsl_setup_acknowledged = true
model_reasoning_effort = "xhigh"
disable_response_storage = true
preferred_auth_method = "apikey"
personality = "pragmatic"

[model_providers]
[model_providers.custom]
name = "custom"
wire_api = "responses"
requires_openai_auth = true
base_url = "${serviceBaseUrl.value.trim() || '/v1'}"`)

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(micros / 1_000_000)
}

function formatPercent(value: number): string {
  return `${(value * 100).toFixed(1)}%`
}

function formatRatio(value: number): string {
  return `${value.toFixed(2)}x`
}

function formatDate(value: string): string {
  const date = new Date(`${value}T00:00:00Z`)
  return `${date.getUTCMonth() + 1}/${date.getUTCDate()}`
}

function formatHour(value: string): string {
  const hour = value.slice(11, 13)
  if (selectedRangeDays.value === 1) return `${hour}:00`
  return `${Number(value.slice(5, 7))}/${Number(value.slice(8, 10))} ${hour}:00`
}

async function loadDashboard() {
  loading.value = true
  errorMessage.value = ''
  channelDistributionPage.value = 1
  modelDistributionPage.value = 1
  try {
    const [summary, channelItems, modelItems, tokenItems] = await Promise.all([
      request<DashboardSummary>(`/admin/gateway/dashboard?days=${selectedRangeDays.value}`),
      request<Channel[]>('/admin/gateway/channels'),
      request<GatewayModel[]>('/admin/gateway/models'),
      request<ClientToken[]>('/admin/gateway/tokens'),
    ])
    dashboard.value = summary
    channels.value = channelItems
    models.value = modelItems
    tokens.value = tokenItems
    if (!readyModels.value.some((model) => model.name === selectedModel.value)) {
      selectedModel.value = readyModels.value.find((model) => model.name === defaultCodexModel)?.name
        ?? readyModels.value[0]?.name
        ?? modelItems.find((model) => model.enabled)?.name
        ?? ''
    }
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '仪表盘加载失败'
  } finally {
    loading.value = false
  }
}

function handleTimeRangeChange() {
  trendDimension.value = selectedRangeDays.value === 1 ? 'hour' : 'day'
  void loadDashboard()
}

async function copyCodexConfig() {
  copyingConfig.value = true
  try {
    await navigator.clipboard.writeText(codexConfig.value)
    ElMessage.success('Codex 配置已复制')
  } catch {
    ElMessage.error('无法访问剪贴板')
  } finally {
    copyingConfig.value = false
  }
}

function applyExistingProvider(name: string) {
  const provider = localCodexConfig.value?.providers.find((item) => item.name === name)
  if (provider?.baseUrl) serviceBaseUrl.value = provider.baseUrl
}

function handleProviderModeChange() {
  if (providerMode.value === 'existing') applyExistingProvider(existingProviderName.value)
}

function handleTokenModeChange() {
  if (tokenMode.value === 'existing' && canReuseCurrentToken.value) {
    selectedTokenId.value = localCodexConfig.value?.authTokenId ?? null
  }
}

function applyLocalCodexConfiguration(configuration: CodexLocalConfiguration) {
  localCodexConfig.value = configuration
  const configuredProvider = configuration.providers.find((provider) => provider.name === configuration.modelProvider)
  const fallbackProvider = configuredProvider ?? configuration.providers[0]
  if (fallbackProvider) {
    providerMode.value = 'existing'
    existingProviderName.value = fallbackProvider.name
    serviceBaseUrl.value = fallbackProvider.baseUrl || serviceBaseUrl.value
  } else {
    providerMode.value = 'new'
    existingProviderName.value = ''
  }
  if (configuration.model && readyModels.value.some((model) => model.name === configuration.model)) {
    selectedModel.value = configuration.model
  }
  if (configuration.authTokenId && tokens.value.some((token) => token.id === configuration.authTokenId && token.enabled)) {
    tokenMode.value = 'existing'
    selectedTokenId.value = configuration.authTokenId
  } else {
    tokenMode.value = 'new'
    selectedTokenId.value = null
  }
}

async function loadLocalCodexConfiguration() {
  codexConfigLoading.value = true
  codexConfigError.value = ''
  localCodexConfig.value = null
  try {
    const configuration = await request<CodexLocalConfiguration>('/admin/gateway/codex-config')
    applyLocalCodexConfiguration(configuration)
  } catch (error) {
    codexConfigError.value = error instanceof Error ? error.message : '本机 Codex 配置读取失败'
  } finally {
    codexConfigLoading.value = false
  }
}

function openCodexConfigurator() {
  if (!localBrowserAccess) {
    ElMessage.warning('一键配置仅支持在网关运行机上通过 localhost 或回环地址访问')
    return
  }
  codexDialogOpen.value = true
  void loadLocalCodexConfiguration()
}

async function saveLocalCodexConfiguration() {
  if (!activeProviderName.value || !selectedModel.value || !serviceBaseUrl.value.trim()) {
    ElMessage.error('请完整选择供应商、模型和服务地址')
    return
  }
  if (tokenMode.value === 'existing' && !selectedTokenId.value) {
    ElMessage.error('请选择可复用的现有访问令牌')
    return
  }
  if (tokenMode.value === 'new' && !newTokenName.value.trim()) {
    ElMessage.error('请输入新访问令牌名称')
    return
  }
  const payload: CodexConfigurationSaveRequest = {
    providerMode: providerMode.value,
    providerName: activeProviderName.value,
    model: selectedModel.value,
    baseUrl: serviceBaseUrl.value.trim(),
    tokenMode: tokenMode.value,
    tokenId: selectedTokenId.value ?? 0,
    newTokenName: newTokenName.value.trim(),
  }
  codexConfigSaving.value = true
  codexConfigError.value = ''
  try {
    const configuration = await request<CodexLocalConfiguration>('/admin/gateway/codex-config', {
      method: 'PUT',
      body: JSON.stringify(payload),
    })
    applyLocalCodexConfiguration(configuration)
    codexDialogOpen.value = false
    quickStartExpanded.value = true
    ElMessage.success('本机 Codex 配置已保存')
    await loadDashboard()
  } catch (error) {
    codexConfigError.value = error instanceof Error ? error.message : '本机 Codex 配置保存失败'
  } finally {
    codexConfigSaving.value = false
  }
}

onMounted(loadDashboard)
</script>

<template>
  <div class="page-stack dashboard-page">
    <header class="page-heading">
      <div>
        <h1>运行总览</h1>
        <p>按所选自然日范围汇总请求、Token、费用与运行质量</p>
      </div>
      <div class="page-actions">
        <el-segmented
          v-model="selectedRangeDays"
          :options="timeRangeOptions"
          :disabled="loading"
          size="small"
          aria-label="统计时间范围"
          @change="handleTimeRangeChange"
        />
        <el-tooltip content="刷新运行总览" placement="bottom">
          <el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新运行总览" @click="loadDashboard" />
        </el-tooltip>
      </div>
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert">
      <strong>无法读取网关统计</strong>
      <span>{{ errorMessage }}</span>
      <el-button @click="loadDashboard">重试</el-button>
    </div>

    <template v-else>
      <section class="metric-strip" aria-label="网关指标">
        <article class="metric-cell">
          <span><Tickets />请求量</span>
          <strong v-if="!loading">{{ formatCompactNumber(dashboard?.requests ?? 0) }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>{{ selectedRangeLabel }}聚合统计</small>
        </article>
        <article class="metric-cell">
          <span><DataLine />成功率</span>
          <strong v-if="!loading">{{ formatPercent(dashboard?.successRate ?? 0) }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>最终状态为 2xx</small>
        </article>
        <el-tooltip placement="bottom" :disabled="loading || topTokenModels.length === 0" popper-class="dashboard-metric-popper">
          <template #content>
            <div class="metric-tooltip" aria-label="Token 用量最高的四个模型">
              <header><strong>模型 Token 用量</strong><span>按输入与输出合计降序</span></header>
              <ol>
                <li v-for="(model, index) in topTokenModels" :key="model.name">
                  <span class="metric-tooltip-rank">{{ index + 1 }}</span>
                  <code>{{ model.name }}</code>
                  <div>
                    <strong>{{ formatCompactNumber(model.inputTokens + model.outputTokens) }}</strong>
                    <small>输入 {{ formatCompactNumber(model.inputTokens) }} · 输出 {{ formatCompactNumber(model.outputTokens) }}</small>
                  </div>
                </li>
              </ol>
            </div>
          </template>
          <article class="metric-cell metric-cell-tooltip" tabindex="0">
            <span><Coin />Token</span>
            <strong v-if="!loading">{{ formatCompactNumber(totalTokens) }}</strong>
            <el-skeleton v-else :rows="1" animated />
            <small>悬浮查看模型用量前四</small>
          </article>
        </el-tooltip>
        <el-tooltip placement="bottom" :disabled="loading || topCacheChannels.length === 0" popper-class="dashboard-metric-popper">
          <template #content>
            <div class="metric-tooltip" aria-label="缓存命中率最高的三个渠道">
              <header><strong>渠道缓存命中率</strong><span>{{ selectedRangeLabel }} · 按缓存命中率降序</span></header>
              <ol>
                <li v-for="(channel, index) in topCacheChannels" :key="channel.name">
                  <span class="metric-tooltip-rank">{{ index + 1 }}</span>
                  <code>{{ channel.name }}</code>
                  <div>
                    <strong>{{ formatPercent(channel.cacheHitRate) }}</strong>
                    <small>缓存读 {{ formatCompactNumber(channel.cachedTokens) }} / 输入 {{ formatCompactNumber(channel.inputTokens) }} Token</small>
                  </div>
                </li>
              </ol>
            </div>
          </template>
          <article class="metric-cell metric-cell-tooltip" tabindex="0">
            <span><DataLine />缓存命中率</span>
            <strong v-if="!loading">{{ formatPercent(dashboard?.cacheHitRate ?? 0) }}</strong>
            <el-skeleton v-else :rows="1" animated />
            <small>缓存读 {{ formatCompactNumber(dashboard?.cachedTokens ?? 0) }} · 写 {{ formatCompactNumber(dashboard?.cacheWriteTokens ?? 0) }}</small>
          </article>
        </el-tooltip>
        <el-tooltip placement="bottom" :disabled="loading || topCostModels.length === 0" popper-class="dashboard-metric-popper">
          <template #content>
            <div class="cost-tooltip-list" role="list" aria-label="费用最高的五个模型">
              <div v-for="model in topCostModels" :key="model.name" role="listitem"><code>{{ model.name }}</code><strong>{{ formatUSD(model.upstreamCostMicros) }}</strong></div>
            </div>
          </template>
          <article class="metric-cell metric-cell-tooltip" tabindex="0">
            <span><Coin />上游费用</span>
            <strong v-if="!loading">{{ formatUSD(dashboard?.upstreamCostMicros ?? 0) }}</strong>
            <el-skeleton v-else :rows="1" animated />
            <small>悬浮查看费用最高的模型</small>
          </article>
        </el-tooltip>
        <el-tooltip placement="bottom" :disabled="loading || topCostRatios.length === 0" popper-class="dashboard-metric-popper">
          <template #content>
            <div class="metric-tooltip cost-ratio-tooltip" aria-label="使用最多的五个费用倍率">
              <header><strong>费用倍率分布</strong><span>按可计算官方基准的请求数排序</span></header>
              <ol>
                <li v-for="(item, index) in topCostRatios" :key="item.ratio">
                  <span class="metric-tooltip-rank">{{ index + 1 }}</span>
                  <code>{{ formatRatio(item.ratio) }}</code>
                  <div>
                    <strong>{{ formatPercent(item.share) }}</strong>
                    <small>{{ formatCompactNumber(item.requests) }} 个请求</small>
                  </div>
                </li>
              </ol>
            </div>
          </template>
          <article class="metric-cell metric-cell-tooltip" tabindex="0">
            <span><Coin />费用倍率</span>
            <strong v-if="!loading">{{ formatRatio(dashboard?.upstreamCostRatio ?? 0) }}</strong>
            <el-skeleton v-else :rows="1" animated />
            <small>悬浮查看倍率占比前五</small>
          </article>
        </el-tooltip>
        <article class="metric-cell">
          <span><Timer />平均首 Token</span>
          <strong v-if="!loading">{{ dashboard?.firstTokenSampleCount ? formatDuration(dashboard.averageFirstTokenMs) : '--' }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>{{ formatCompactNumber(dashboard?.firstTokenSampleCount ?? 0) }} 个流式样本</small>
        </article>
        <article class="metric-cell">
          <span><Odometer />平均请求延迟</span>
          <strong v-if="!loading">{{ dashboard?.latencySampleCount ? formatDuration(dashboard.averageLatencyMs) : '--' }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>{{ formatCompactNumber(dashboard?.latencySampleCount ?? 0) }} 个响应头样本</small>
        </article>
        <article class="metric-cell">
          <span><Clock />平均请求耗时</span>
          <strong v-if="!loading">{{ formatDuration(dashboard?.averageDurationMs ?? 0) }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>{{ formatCompactNumber(dashboard?.durationSampleCount ?? 0) }} 个完整请求</small>
        </article>
        <article class="metric-cell">
          <span><Connection />可用渠道</span>
          <strong v-if="!loading">{{ availableChannels }} / {{ channels.length }}</strong>
          <el-skeleton v-else :rows="1" animated />
          <small>{{ availableChannels ? '至少一个渠道可调度' : '当前无可用渠道' }}</small>
        </article>
      </section>
      <p class="historical-cost-note">{{ selectedRangeLabel }}按东八区自然日和请求级记录统计；重试不会重复累计 Token 与费用，费用优先采用最终上游返回值。</p>

      <section class="surface-panel quick-start-panel">
        <header class="panel-heading quick-start-heading">
          <button
            class="quick-start-toggle"
            type="button"
            :aria-expanded="quickStartExpanded"
            aria-controls="codex-quick-start-content"
            @click="quickStartExpanded = !quickStartExpanded"
          >
            <el-icon :class="{ 'is-expanded': quickStartExpanded }"><ArrowDown /></el-icon>
            <span><strong>Codex 快速开始</strong><small>使用 Responses 接口连接当前网关</small></span>
          </button>
          <div class="quick-start-actions">
            <el-button v-if="localBrowserAccess" class="one-click-config-button" type="primary" :icon="MagicStick" @click="openCodexConfigurator">一键配置本机 Codex</el-button>
            <span v-else class="local-config-note">一键配置仅在运行机本地访问时可用</span>
            <el-button :icon="CopyDocument" :loading="copyingConfig" :disabled="!selectedModel" @click="copyCodexConfig">复制配置</el-button>
          </div>
        </header>
        <el-collapse-transition>
          <div v-show="quickStartExpanded" id="codex-quick-start-content">
            <div class="readiness-strip" aria-label="Codex 接入状态">
              <div><span>渠道</span><strong>{{ readyChannels.length }} / {{ channels.length }}</strong><el-tag :type="readyChannels.length ? 'success' : 'warning'" effect="plain">{{ readyChannels.length ? '就绪' : '待配置' }}</el-tag></div>
              <div><span>模型</span><strong>{{ readyModels.length }} / {{ models.length }}</strong><el-tag :type="readyModels.length ? 'success' : 'warning'" effect="plain">{{ readyModels.length ? '就绪' : '待启用映射' }}</el-tag></div>
              <div><span>令牌</span><strong>{{ readyTokens.length }} / {{ tokens.length }}</strong><el-tag :type="readyTokens.length ? 'success' : 'warning'" effect="plain">{{ readyTokens.length ? '就绪' : '待签发' }}</el-tag></div>
            </div>
            <div class="quick-start-body">
              <div class="quick-start-fields">
                <label><span>Codex 模型</span><el-select v-model="selectedModel" filterable placeholder="选择已就绪模型"><el-option v-for="model in readyModels" :key="model.id" :label="model.name" :value="model.name" /></el-select></label>
                <label><span>服务地址</span><el-input v-model="serviceBaseUrl" /></label>
                <div class="token-safety"><strong>auth.json</strong><code>OPENAI_API_KEY</code><span>一键配置直接写入本机认证文件，不在页面显示完整令牌。</span></div>
              </div>
              <pre>{{ codexConfig }}</pre>
            </div>
          </div>
        </el-collapse-transition>
      </section>

      <el-dialog
        v-model="codexDialogOpen"
        class="codex-config-dialog"
        width="min(760px, calc(100vw - 28px))"
        :close-on-click-modal="!codexConfigSaving"
        :close-on-press-escape="!codexConfigSaving"
      >
        <template #header>
          <div class="codex-dialog-heading">
            <span class="codex-dialog-icon"><MagicStick /></span>
            <div><strong>一键配置本机 Codex</strong><small>config.toml + auth.json</small></div>
          </div>
        </template>

        <div v-if="codexConfigLoading" class="codex-config-loading" aria-live="polite">
          <el-skeleton :rows="6" animated />
        </div>
        <div v-else-if="codexConfigError && !localCodexConfig" class="state-panel state-error" role="alert">
          <strong>本机配置读取失败</strong>
          <span>{{ codexConfigError }}</span>
          <el-button :loading="codexConfigLoading" @click="loadLocalCodexConfiguration">重新读取</el-button>
        </div>
        <div v-else-if="localCodexConfig" class="codex-config-content">
          <section class="codex-current-state" aria-label="当前本机 Codex 配置">
            <div><span>供应商</span><strong>{{ localCodexConfig.modelProvider || '未配置' }}</strong><small>{{ currentCodexProvider?.baseUrl || '无服务地址' }}</small></div>
            <div><span>模型</span><strong>{{ localCodexConfig.model || '未配置' }}</strong><small><code :title="localCodexConfig.configPath">{{ localCodexConfig.configPath }}</code></small></div>
            <div><span>认证</span><strong>{{ localCodexConfig.authConfigured ? '已配置' : '未配置' }}</strong><small><code :title="localCodexConfig.authKeyPrefix ? undefined : localCodexConfig.authPath">{{ localCodexConfig.authKeyPrefix || localCodexConfig.authPath }}</code></small></div>
          </section>

          <p v-if="codexConfigError" class="codex-inline-error" role="alert">{{ codexConfigError }}</p>

          <el-form class="codex-config-form" label-position="top" @submit.prevent="saveLocalCodexConfiguration">
            <section class="codex-form-section">
              <header><span>1</span><div><strong>模型供应商</strong><small>当前 {{ localCodexConfig.providers.length }} 个</small></div></header>
              <el-form-item label="配置方式">
                <el-radio-group v-model="providerMode" @change="handleProviderModeChange">
                  <el-radio-button value="existing" :disabled="localCodexConfig.providers.length === 0">修改现有供应商</el-radio-button>
                  <el-radio-button value="new">新增供应商</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="providerMode === 'existing'" label="现有供应商">
                <el-select v-model="existingProviderName" filterable placeholder="选择供应商" @change="applyExistingProvider">
                  <el-option v-for="provider in localCodexConfig.providers" :key="provider.name" :label="provider.displayName || provider.name" :value="provider.name">
                    <span>{{ provider.displayName || provider.name }}</span>
                    <small class="provider-option-url">{{ provider.baseUrl || '未设置地址' }}</small>
                  </el-option>
                </el-select>
              </el-form-item>
              <el-form-item v-else label="供应商标识">
                <el-input v-model="newProviderName" maxlength="64" placeholder="例如 gateway" />
              </el-form-item>
              <div class="codex-form-grid">
                <el-form-item label="Codex 模型">
                  <el-select v-model="selectedModel" filterable placeholder="选择已就绪模型">
                    <el-option v-for="model in readyModels" :key="model.id" :label="model.name" :value="model.name" />
                  </el-select>
                </el-form-item>
                <el-form-item label="服务地址">
                  <el-input v-model="serviceBaseUrl" />
                </el-form-item>
              </div>
            </section>

            <section class="codex-form-section">
              <header><span>2</span><div><strong>访问令牌</strong><small>写入 auth.json</small></div></header>
              <el-form-item label="令牌方式">
                <el-radio-group v-model="tokenMode" @change="handleTokenModeChange">
                  <el-radio-button value="existing" :disabled="!canReuseCurrentToken">使用现有令牌</el-radio-button>
                  <el-radio-button value="new">新增令牌</el-radio-button>
                </el-radio-group>
              </el-form-item>
              <el-form-item v-if="tokenMode === 'existing'" label="现有访问令牌">
                <el-select v-model="selectedTokenId" placeholder="选择访问令牌">
                  <el-option
                    v-for="token in readyTokens"
                    :key="token.id"
                    :value="token.id"
                    :label="`${token.name} · ${token.keyPrefix}`"
                    :disabled="token.id !== localCodexConfig.authTokenId"
                  />
                </el-select>
              </el-form-item>
              <div v-else class="new-token-row">
                <el-form-item label="新令牌名称"><el-input v-model="newTokenName" maxlength="120" /></el-form-item>
                <div class="new-token-policy"><Key /><span>所选模型 · 60 RPM · 10 并发</span></div>
              </div>
              <p v-if="!canReuseCurrentToken" class="token-reuse-note">现有令牌只保存不可逆摘要；当前 auth.json 没有可匹配的明文令牌，请新增令牌。</p>
            </section>
          </el-form>
        </div>

        <template #footer>
          <div class="dialog-actions">
            <el-button :disabled="codexConfigSaving" @click="codexDialogOpen = false">取消</el-button>
            <el-button
              type="primary"
              :icon="MagicStick"
              :loading="codexConfigSaving"
              :disabled="codexConfigLoading || !localCodexConfig"
              @click="saveLocalCodexConfiguration"
            >保存并配置</el-button>
          </div>
        </template>
      </el-dialog>

      <div v-if="!loading && dashboard?.requests === 0" class="state-panel state-empty">
        <Connection />
        <strong>还没有网关调用</strong>
        <span>配置渠道、公开模型和访问令牌后，请求统计会显示在这里。</span>
        <el-button type="primary" @click="router.push('/channels')">配置渠道</el-button>
      </div>

      <template v-else>
        <section class="surface-panel usage-panel">
          <header class="panel-heading">
            <div>
              <h2>{{ selectedRangeLabel }}请求</h2>
              <p>{{ trendDimension === 'hour' ? '每小时' : '每日' }}请求量与成功量</p>
            </div>
            <el-segmented v-model="trendDimension" :options="trendDimensionOptions" size="small" aria-label="请求趋势统计维度" />
          </header>
          <div v-if="loading" class="chart-skeleton"><el-skeleton :rows="5" animated /></div>
          <RequestTrendChart v-else :points="requestTrendPoints" :aria-label="requestTrendAriaLabel" />
          <div class="chart-legend"><span><i class="legend-total"></i>请求</span><span><i class="legend-success"></i>成功</span></div>
        </section>

        <div class="dashboard-tables">
          <section class="surface-panel distribution-panel channel-distribution-panel">
            <header class="panel-heading channel-distribution-heading">
              <div><h2>渠道分布</h2><p>{{ selectedRangeLabel }}，每个请求按最终渠道统计一次</p></div>
              <div class="channel-distribution-actions">
                <el-segmented
                  v-if="channelDistributionView === 'pie'"
                  v-model="channelShareMetric"
                  :options="channelShareMetricOptions"
                  size="small"
                  aria-label="渠道占比统计方式"
                />
                <el-tooltip :content="channelDistributionView === 'list' ? '切换为饼图' : '切换为列表'" placement="top">
                  <el-button
                    class="distribution-view-button"
                    :icon="channelDistributionView === 'list' ? PieChart : List"
                    :aria-label="channelDistributionView === 'list' ? '切换为饼图' : '切换为列表'"
                    @click="channelDistributionView = channelDistributionView === 'list' ? 'pie' : 'list'"
                  />
                </el-tooltip>
              </div>
            </header>
            <el-table v-if="channelDistributionView === 'list'" class="distribution-table" :data="paginatedChannels" height="338" empty-text="暂无渠道调用">
              <el-table-column prop="name" label="渠道" min-width="140" />
              <el-table-column label="请求" width="78" align="right"><template #default="scope">{{ formatCompactNumber(scope.row.requests) }}</template></el-table-column>
              <el-table-column label="Token" min-width="142" align="right">
                <template #default="scope"><div class="usage-cell"><strong>{{ formatCompactNumber(scope.row.inputTokens + scope.row.outputTokens) }}</strong><small>输入 {{ formatCompactNumber(scope.row.inputTokens) }} · 输出 {{ formatCompactNumber(scope.row.outputTokens) }}</small></div></template>
              </el-table-column>
              <el-table-column label="成功率" width="112" align="right">
                <template #default="scope"><div class="success-cell"><strong>{{ formatPercent(scope.row.successRate) }}</strong><small>{{ formatCompactNumber(scope.row.successes) }} / {{ formatCompactNumber(Math.max(0, scope.row.requests - scope.row.canceledCount)) }} 完成</small></div></template>
              </el-table-column>
              <el-table-column label="费用" width="150" align="right">
                <template #default="scope"><div class="cost-cell"><strong>{{ formatUSD(scope.row.upstreamCostMicros) }}</strong><small>估算 {{ formatUSD(scope.row.estimatedCostMicros) }}</small></div></template>
              </el-table-column>
            </el-table>
            <footer v-if="channelDistributionView === 'list'" class="table-pagination distribution-pagination">
              <el-pagination
                v-if="channelDistributionTotal > distributionPageSize"
                v-model:current-page="channelDistributionPage"
                small
                background
                :page-size="distributionPageSize"
                :total="channelDistributionTotal"
                layout="total, prev, pager, next"
                aria-label="渠道分布分页"
              />
            </footer>
            <ChannelDistributionChart
              v-else
              class="distribution-chart"
              :items="dashboard?.channels ?? []"
              :metric="channelShareMetric"
              :aria-label="channelDistributionAriaLabel"
            />
          </section>
          <section class="surface-panel distribution-panel">
            <header class="panel-heading"><div><h2>模型分布</h2><p>{{ selectedRangeLabel }}，按公开模型统计</p></div></header>
            <el-table class="distribution-table" :data="paginatedModels" height="338" empty-text="暂无模型调用">
              <el-table-column prop="name" label="模型" min-width="140" />
              <el-table-column label="请求" width="78" align="right"><template #default="scope">{{ formatCompactNumber(scope.row.requests) }}</template></el-table-column>
              <el-table-column label="Token" min-width="142" align="right">
                <template #default="scope"><div class="usage-cell"><strong>{{ formatCompactNumber(scope.row.inputTokens + scope.row.outputTokens) }}</strong><small>输入 {{ formatCompactNumber(scope.row.inputTokens) }} · 输出 {{ formatCompactNumber(scope.row.outputTokens) }}</small></div></template>
              </el-table-column>
              <el-table-column label="成功率" width="112" align="right">
                <template #default="scope"><div class="success-cell"><strong>{{ formatPercent(scope.row.successRate) }}</strong><small>{{ formatCompactNumber(scope.row.successes) }} / {{ formatCompactNumber(Math.max(0, scope.row.requests - scope.row.canceledCount)) }} 完成</small></div></template>
              </el-table-column>
              <el-table-column label="费用" width="150" align="right">
                <template #default="scope"><div class="cost-cell"><strong>{{ formatUSD(scope.row.upstreamCostMicros) }}</strong><small>估算 {{ formatUSD(scope.row.estimatedCostMicros) }}</small></div></template>
              </el-table-column>
            </el-table>
            <footer class="table-pagination distribution-pagination">
              <el-pagination
                v-if="modelDistributionTotal > distributionPageSize"
                v-model:current-page="modelDistributionPage"
                small
                background
                :page-size="distributionPageSize"
                :total="modelDistributionTotal"
                layout="total, prev, pager, next"
                aria-label="模型分布分页"
              />
            </footer>
          </section>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.dashboard-page { min-width: 0; }
.usage-panel { min-height: 300px; }
.historical-cost-note { margin: -8px 0 0; color: var(--hongfen-text-subtle); font-size: 11px; }
.metric-cell-tooltip { cursor: help; }
:global(.dashboard-metric-popper) { max-width: min(440px, 90vw); }
.metric-tooltip { width: min(360px, 82vw); }
.metric-tooltip > header { display: grid; gap: 2px; padding: 3px 4px 8px; border-bottom: 1px solid rgb(255 255 255 / 16%); }
.metric-tooltip > header strong { font-size: 12px; }
.metric-tooltip > header span { color: rgb(255 255 255 / 70%); font-size: 10px; }
.metric-tooltip ol { display: grid; max-height: 244px; margin: 0; padding: 0; overflow-y: auto; list-style: none; }
.metric-tooltip li { display: grid; grid-template-columns: 20px minmax(0, 1fr) auto; align-items: center; gap: 9px; padding: 8px 4px; border-bottom: 1px solid rgb(255 255 255 / 16%); }
.metric-tooltip li:last-child { border-bottom: 0; }
.metric-tooltip-rank { display: grid; width: 18px; height: 18px; place-items: center; border: 1px solid rgb(255 255 255 / 24%); border-radius: 3px; font: 600 10px/1 var(--hongfen-font-mono); }
.metric-tooltip code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.metric-tooltip li > div { display: grid; justify-items: end; gap: 1px; font-variant-numeric: tabular-nums; }
.metric-tooltip li small { color: rgb(255 255 255 / 70%); font-size: 9px; white-space: nowrap; }
.cost-tooltip-list { display: grid; min-width: 260px; max-height: 220px; overflow-y: auto; }
.cost-tooltip-list > div { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 18px; padding: 8px 4px; border-bottom: 1px solid rgb(255 255 255 / 16%); }
.cost-tooltip-list > div:last-child { border-bottom: 0; }
.cost-tooltip-list code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cost-tooltip-list strong { font-variant-numeric: tabular-nums; }
.quick-start-panel { overflow: hidden; }
.quick-start-heading { padding-left: 10px; }
.quick-start-actions { display: flex; flex: none; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
.one-click-config-button { min-height: 38px; padding-inline: 16px; font-weight: 700; }
.one-click-config-button :deep(.el-icon) { font-size: 17px; }
.local-config-note { max-width: 220px; color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.5; text-align: right; }
.quick-start-toggle { display: flex; flex: 1; align-items: center; gap: 10px; min-width: 0; padding: 6px 8px; border: 0; border-radius: var(--hongfen-radius-control); background: transparent; color: inherit; text-align: left; cursor: pointer; }
.quick-start-toggle:focus-visible { outline: 2px solid var(--hongfen-primary); outline-offset: 1px; }
.quick-start-toggle .el-icon { flex: none; color: var(--hongfen-text-muted); transition: transform 160ms ease; }
.quick-start-toggle .el-icon.is-expanded { transform: rotate(180deg); }
.quick-start-toggle > span { display: grid; min-width: 0; gap: 2px; }
.quick-start-toggle strong { color: var(--hongfen-text); font-size: 14px; font-weight: 650; }
.quick-start-toggle small { color: var(--hongfen-text-muted); font-size: 11px; }
.readiness-strip { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border-block: 1px solid var(--hongfen-border); }
.readiness-strip > div { display: grid; grid-template-columns: 1fr auto; align-items: center; gap: 4px 12px; padding: 12px 16px; border-right: 1px solid var(--hongfen-border); }
.readiness-strip > div:last-child { border-right: 0; }
.readiness-strip span { color: var(--hongfen-text-muted); font-size: 11px; }
.readiness-strip strong { color: var(--hongfen-text); font-family: var(--hongfen-font-mono); }
.readiness-strip .el-tag { grid-column: 2; grid-row: 1 / span 2; }
.quick-start-body { display: grid; grid-template-columns: minmax(280px, .75fr) minmax(420px, 1.25fr); gap: 18px; padding: 18px; }
.quick-start-fields { display: grid; align-content: start; gap: 14px; }
.quick-start-fields label { display: grid; gap: 6px; }
.quick-start-fields label > span { color: var(--hongfen-text-muted); font-size: 11px; font-weight: 600; }
.quick-start-fields .el-select { width: 100%; }
.token-safety { display: grid; grid-template-columns: auto 1fr; gap: 4px 10px; padding: 12px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.token-safety strong, .token-safety span { color: var(--hongfen-text-muted); font-size: 11px; }
.token-safety code { color: var(--hongfen-text); }
.token-safety span { grid-column: 1 / -1; }
.quick-start-body pre { min-height: 220px; margin: 0; padding: 15px; overflow: auto; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); color: var(--hongfen-text); font: 12px/1.65 var(--hongfen-font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
.codex-dialog-heading { display: flex; align-items: center; gap: 10px; }
.codex-dialog-heading > div { display: grid; gap: 2px; }
.codex-dialog-heading strong { color: var(--hongfen-text); font-size: 16px; }
.codex-dialog-heading small { color: var(--hongfen-text-muted); font-family: var(--hongfen-font-mono); font-size: 11px; }
.codex-dialog-icon { display: grid; width: 34px; height: 34px; place-items: center; border: 1px solid var(--hongfen-primary); border-radius: var(--hongfen-radius-control); color: var(--hongfen-surface); background: var(--hongfen-primary); }
.codex-dialog-icon svg { width: 18px; height: 18px; }
.codex-config-loading { min-height: 320px; padding: 8px 0; }
.codex-config-content { display: grid; gap: 20px; }
.codex-current-state { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.codex-current-state > div { display: grid; min-width: 0; gap: 4px; padding: 12px 14px; border-right: 1px solid var(--hongfen-border); }
.codex-current-state > div:last-child { border-right: 0; }
.codex-current-state span, .codex-current-state small { min-width: 0; color: var(--hongfen-text-muted); font-size: 10px; }
.codex-current-state small { display: block; overflow: hidden; }
.codex-current-state strong { overflow: hidden; color: var(--hongfen-text); font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.codex-current-state code { display: block; width: 100%; max-width: 100%; overflow: hidden; font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.codex-inline-error { padding: 9px 11px; border-left: 3px solid var(--hongfen-danger); color: var(--hongfen-danger); background: var(--hongfen-danger-soft); font-size: 12px; }
.codex-config-form { display: grid; gap: 22px; }
.codex-form-section { display: grid; gap: 12px; }
.codex-form-section + .codex-form-section { padding-top: 20px; border-top: 1px solid var(--hongfen-border); }
.codex-form-section > header { display: flex; align-items: center; gap: 9px; }
.codex-form-section > header > span { display: grid; width: 24px; height: 24px; flex: none; place-items: center; border-radius: 50%; color: var(--hongfen-surface); background: var(--hongfen-primary); font-family: var(--hongfen-font-mono); font-size: 11px; font-weight: 700; }
.codex-form-section > header > div { display: flex; min-width: 0; align-items: baseline; gap: 8px; }
.codex-form-section > header strong { color: var(--hongfen-text); font-size: 13px; }
.codex-form-section > header small { color: var(--hongfen-text-muted); font-size: 10px; }
.codex-config-form :deep(.el-form-item) { margin-bottom: 0; }
.codex-config-form :deep(.el-select) { width: 100%; }
.codex-form-grid { display: grid; grid-template-columns: minmax(210px, .8fr) minmax(260px, 1.2fr); gap: 12px; }
.provider-option-url { float: right; max-width: 320px; overflow: hidden; color: var(--hongfen-text-muted); text-overflow: ellipsis; white-space: nowrap; }
.new-token-row { display: grid; grid-template-columns: minmax(220px, 1fr) auto; align-items: end; gap: 12px; }
.new-token-policy { display: flex; min-height: 32px; align-items: center; gap: 6px; padding: 0 10px; border: 1px solid var(--hongfen-border); color: var(--hongfen-text-muted); background: var(--hongfen-surface-muted); font-size: 11px; white-space: nowrap; }
.new-token-policy svg { width: 14px; height: 14px; color: var(--hongfen-success); }
.token-reuse-note { color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.55; }
.cost-cell, .usage-cell, .success-cell { display: grid; gap: 2px; font-variant-numeric: tabular-nums; }
.cost-cell strong, .usage-cell strong, .success-cell strong { color: var(--hongfen-text); font-weight: 650; }
.cost-cell small, .usage-cell small, .success-cell small { color: var(--hongfen-text-muted); font-size: 10px; white-space: nowrap; }
.chart-skeleton { padding: 24px; }
.chart-legend { display: flex; justify-content: flex-end; gap: 18px; padding: 0 22px 18px; color: var(--hongfen-text-muted); font-size: 12px; }
.chart-legend span { display: inline-flex; align-items: center; gap: 6px; }
.chart-legend i { width: 10px; height: 10px; }
.legend-total { background: var(--hongfen-primary); }
.legend-success { background: var(--hongfen-success); }
.channel-distribution-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
.distribution-view-button { width: 32px; height: 32px; padding: 0; }
.dashboard-tables { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.distribution-panel { display: flex; min-height: 452px; flex-direction: column; }
.distribution-table { flex: none; }
.distribution-pagination { min-height: 56px; align-items: center; margin-top: auto; }
.distribution-chart { flex: 1; }
@media (max-width: 860px) {
  .dashboard-tables { grid-template-columns: 1fr; }
  .quick-start-body { grid-template-columns: 1fr; }
  .quick-start-heading { align-items: flex-start; flex-direction: column; padding: 10px; }
  .quick-start-actions { width: 100%; justify-content: flex-start; }
}
@media (max-width: 560px) {
  .readiness-strip, .codex-current-state { grid-template-columns: 1fr; }
  .readiness-strip > div, .codex-current-state > div { border-right: 0; border-bottom: 1px solid var(--hongfen-border); }
  .readiness-strip > div:last-child, .codex-current-state > div:last-child { border-bottom: 0; }
  .quick-start-body { padding: 12px; }
  .quick-start-actions, .quick-start-actions .el-button, .one-click-config-button { width: 100%; margin-left: 0; }
  .local-config-note { max-width: none; width: 100%; text-align: left; }
  .codex-form-grid, .new-token-row { grid-template-columns: 1fr; }
  .codex-form-section :deep(.el-radio-group) { display: grid; grid-template-columns: 1fr; width: 100%; }
  .codex-form-section :deep(.el-radio-button__inner) { width: 100%; }
  .new-token-policy { white-space: normal; }
}
@media (max-width: 480px) { .channel-distribution-heading { align-items: flex-start; flex-direction: column; } .channel-distribution-actions { width: 100%; justify-content: space-between; } }
</style>
