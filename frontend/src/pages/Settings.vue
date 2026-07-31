<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { Check, Delete, Link, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import projectMeta from '@/config/project.generated.js'
import RoutingWeightEditor from '@/components/RoutingWeightEditor.vue'
import type { ApplicationSettings, LogPayloadCleanupResult, LogStorageUsage, PayloadLogDetail } from '@/types/gateway'
import type { RoutingWeightValues } from '@/types/routing'
import { request } from '@/utils/api'

const repositoryUrl = 'https://github.com/Jemonee/simple-openai-gateway'
const buildBranch = __BUILD_BRANCH__
const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const logStorage = ref<LogStorageUsage | null>(null)
const logStorageLoading = ref(false)
const clearingPayloads = ref(false)
let storageTimer: ReturnType<typeof setInterval> | undefined
const payloadLogDetailOptions: Array<{ label: string; value: PayloadLogDetail }> = [
  { label: '默认', value: 'default' },
  { label: '摘要', value: 'summary' },
  { label: '无', value: 'none' },
]
const payloadLogDetailDescriptions: Record<PayloadLogDetail, string> = {
  default: '沿用当前记录规则，正文按会话去重并以单段 4 MiB 为上限。',
  summary: '保留 JSON 结构、短文本预览及首尾数组项，单段不超过 64 KiB。',
  none: '不保存请求参数和响应正文，仅保留状态、用量、耗时与路由结果。',
}
const defaultRoutingPriceWeightPercent = 40
const defaultRoutingEfficiencyWeightPercent = 35
const defaultRoutingQualityWeightPercent = 15
const defaultRoutingBalanceWeightPercent = 10
const minimumRoutingQualityWeightPercent = 5
type RoutingWeightKey = 'routingPriceWeightPercent' | 'routingEfficiencyWeightPercent' | 'routingQualityWeightPercent' | 'routingBalanceWeightPercent'
const routingWeightKeys: RoutingWeightKey[] = ['routingPriceWeightPercent', 'routingEfficiencyWeightPercent', 'routingQualityWeightPercent', 'routingBalanceWeightPercent']
const defaultCommonModelNames = ['gpt-image-2', 'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4-mini', 'codex-auto-review']
const form = reactive<ApplicationSettings>({
  webConfig: { host: '', port: '' },
  nodeConfig: { sharedToken: '' },
  gatewayConfig: {
    maxAttempts: 3,
    requestBodyLimitMB: 32,
    responseHeaderTimeoutSeconds: 120,
    streamIdleTimeoutSeconds: 300,
    routingPriceWeightPercent: defaultRoutingPriceWeightPercent,
    routingEfficiencyWeightPercent: defaultRoutingEfficiencyWeightPercent,
    routingQualityWeightPercent: defaultRoutingQualityWeightPercent,
    routingBalanceWeightPercent: defaultRoutingBalanceWeightPercent,
    sessionTTLHours: 12,
    secureCookie: false,
    payloadLogDetail: 'default',
    commonModelNames: [...defaultCommonModelNames],
  },
})
const routingWeights = computed<RoutingWeightValues>(() => ({
  price: form.gatewayConfig.routingPriceWeightPercent,
  efficiency: form.gatewayConfig.routingEfficiencyWeightPercent,
  quality: form.gatewayConfig.routingQualityWeightPercent,
  balance: form.gatewayConfig.routingBalanceWeightPercent,
}))

function clampRoutingWeight(value: number, minimum = 0): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(Math.max(Math.round(value), minimum), 100)
}

function applyRoutingWeights(weights: RoutingWeightValues) {
  form.gatewayConfig.routingPriceWeightPercent = weights.price
  form.gatewayConfig.routingEfficiencyWeightPercent = weights.efficiency
  form.gatewayConfig.routingQualityWeightPercent = weights.quality
  form.gatewayConfig.routingBalanceWeightPercent = weights.balance
}

function normalizedRoutingWeights(payload: ApplicationSettings['gatewayConfig']) {
  const price = clampRoutingWeight(payload.routingPriceWeightPercent ?? defaultRoutingPriceWeightPercent)
  const efficiency = clampRoutingWeight(payload.routingEfficiencyWeightPercent ?? defaultRoutingEfficiencyWeightPercent)
  const remaining = Math.max(100 - price - efficiency, minimumRoutingQualityWeightPercent)
  const balance = payload.routingBalanceWeightPercent === undefined
    ? Math.min(defaultRoutingBalanceWeightPercent, remaining - minimumRoutingQualityWeightPercent)
    : clampRoutingWeight(payload.routingBalanceWeightPercent)
  const quality = payload.routingQualityWeightPercent === undefined
    ? remaining - balance
    : clampRoutingWeight(payload.routingQualityWeightPercent, minimumRoutingQualityWeightPercent)
  if (price + efficiency + quality + balance === 100) return { price, efficiency, quality, balance }
  return { price: defaultRoutingPriceWeightPercent, efficiency: defaultRoutingEfficiencyWeightPercent, quality: defaultRoutingQualityWeightPercent, balance: defaultRoutingBalanceWeightPercent }
}

function applySettings(payload: ApplicationSettings) {
  const routingWeights = normalizedRoutingWeights(payload.gatewayConfig)
  form.webConfig = { ...payload.webConfig }
  form.nodeConfig = { ...payload.nodeConfig }
  form.gatewayConfig = {
    ...payload.gatewayConfig,
    routingPriceWeightPercent: routingWeights.price,
    routingEfficiencyWeightPercent: routingWeights.efficiency,
    routingQualityWeightPercent: routingWeights.quality,
    routingBalanceWeightPercent: routingWeights.balance,
    commonModelNames: payload.gatewayConfig.commonModelNames?.length ? [...payload.gatewayConfig.commonModelNames] : [...defaultCommonModelNames],
  }
}

async function loadSettings() {
  loading.value = true
  errorMessage.value = ''
  try {
    applySettings(await request<ApplicationSettings>('/settings'))
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '系统设置加载失败'
  } finally {
    loading.value = false
  }
}

function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let size = value
  let unit = 0
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024
    unit += 1
  }
  return `${size.toFixed(unit === 0 ? 0 : size >= 10 ? 1 : 2)} ${units[unit]}`
}

function formatCutoff(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

async function loadLogStorage() {
  logStorageLoading.value = true
  try {
    logStorage.value = await request<LogStorageUsage>('/admin/gateway/logs/storage')
  } catch {
    logStorage.value = null
  } finally {
    logStorageLoading.value = false
  }
}

async function clearHistoricalPayloads() {
  const cutoff = logStorage.value?.cutoffAt ? formatCutoff(logStorage.value.cutoffAt) : '当前时间前 30 分钟'
  try {
    await ElMessageBox.confirm(
      `将永久清空 ${cutoff} 之前调用日志中的请求参数、请求正文和响应正文。状态、Token、费用和耗时统计会保留。`,
      '清理调用日志参数',
      { type: 'warning', confirmButtonText: '确认清理', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  clearingPayloads.value = true
  try {
    const result = await request<LogPayloadCleanupResult>('/admin/gateway/logs/clear-payloads', { method: 'POST' })
    ElMessage.success(`已清理 ${result.requestLogsCleared} 条调用和 ${result.attemptLogsCleared} 条上游尝试的参数与明细`)
    await loadLogStorage()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '调用日志参数清理失败')
  } finally {
    clearingPayloads.value = false
  }
}

async function saveSettings() {
  const routingWeightTotal = routingWeightKeys.reduce((total, key) => total + form.gatewayConfig[key], 0)
  if (routingWeightTotal !== 100 || form.gatewayConfig.routingQualityWeightPercent < minimumRoutingQualityWeightPercent) {
    ElMessage.error(`四项路由占比之和必须为 100%，且质量占比不能低于 ${minimumRoutingQualityWeightPercent}%`)
    return
  }
  saving.value = true
  try {
    applySettings(await request<ApplicationSettings>('/settings', { method: 'PUT', body: JSON.stringify(form) }))
    ElMessage.success('系统设置已保存')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '系统设置保存失败')
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void loadSettings()
  void loadLogStorage()
  storageTimer = setInterval(() => { void loadLogStorage() }, 10_000)
})

onUnmounted(() => {
  if (storageTimer) clearInterval(storageTimer)
})
</script>

<template>
  <div class="page-stack settings-page">
    <header class="page-heading">
      <div><h1>系统设置</h1><p>调整监听地址、请求限制、路由决策、超时和管理会话</p></div>
      <el-button :icon="Refresh" :loading="loading" @click="loadSettings">重新加载</el-button>
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>系统设置加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadSettings">重试</el-button></div>
    <el-skeleton v-else-if="loading" :rows="10" animated />
    <el-form v-else label-position="top" class="settings-form" @submit.prevent="saveSettings">
      <section class="surface-panel settings-section">
        <header class="panel-heading"><div><h2>服务监听</h2><p>监听地址变更后需重启进程</p></div></header>
        <div class="settings-fields form-columns">
          <el-form-item label="监听主机"><el-input v-model="form.webConfig.host" placeholder="0.0.0.0" /></el-form-item>
          <el-form-item label="监听端口"><el-input v-model="form.webConfig.port" placeholder="8888" /></el-form-item>
        </div>
      </section>

      <section class="surface-panel settings-section routing-settings-section">
        <header class="panel-heading"><div><h2>路由决策</h2><p>保存后立即作用于新进入的非固定渠道请求</p></div></header>
        <RoutingWeightEditor
          :weights="routingWeights"
          :minimum-quality="minimumRoutingQualityWeightPercent"
          :disabled="saving"
          @update:weights="applyRoutingWeights"
        />
        <dl class="routing-formula">
          <div><dt>价格</dt><dd>按本次预计费用相对最低费用归一化，价格越低得分越高。</dd></div>
          <div><dt>效率</dt><dd>首 token 45% + 响应头延迟 20% + 输出吞吐 35%，使用近 30 分钟成功样本。</dd></div>
          <div><dt>质量</dt><dd>成功率 70% + 缓存命中 18% + 缓存 Token 率 12%，至少保留 {{ minimumRoutingQualityWeightPercent }}%。</dd></div>
          <div><dt>均衡</dt><dd>比较当前模型各候选渠道的实际占比与基础目标占比，按样本置信度纠偏；候选越多，窗口会从 100 条动态扩展到最多 1000 条。</dd></div>
        </dl>
      </section>

      <section class="surface-panel settings-section">
        <header class="panel-heading"><div><h2>请求与重试</h2><p>作用于公开的 /v1 Chat Completions 与 Responses 接口</p></div></header>
        <div class="settings-fields settings-grid">
          <el-form-item label="单请求最大尝试次数"><el-input-number v-model="form.gatewayConfig.maxAttempts" :min="1" :max="3" controls-position="right" /></el-form-item>
          <el-form-item label="请求体上限（MiB）"><el-input-number v-model="form.gatewayConfig.requestBodyLimitMB" :min="1" :max="256" controls-position="right" /></el-form-item>
          <el-form-item label="响应头等待（秒）"><el-input-number v-model="form.gatewayConfig.responseHeaderTimeoutSeconds" :min="1" :max="600" controls-position="right" /></el-form-item>
          <el-form-item label="流式空闲超时（秒）"><el-input-number v-model="form.gatewayConfig.streamIdleTimeoutSeconds" :min="10" :max="3600" controls-position="right" /></el-form-item>
        </div>
      </section>

      <section class="surface-panel settings-section">
        <header class="panel-heading"><div><h2>会话与数据</h2><p>调用明细固定保留 5 天，更早数据仅保留按令牌汇总的每日统计</p></div></header>
        <div class="settings-fields settings-grid">
          <el-form-item label="管理会话时长（小时）"><el-input-number v-model="form.gatewayConfig.sessionTTLHours" :min="1" :max="168" controls-position="right" /></el-form-item>
          <el-form-item label="Cookie 安全属性"><el-switch v-model="form.gatewayConfig.secureCookie" active-text="仅 HTTPS 发送" inactive-text="允许本地 HTTP" /></el-form-item>
          <el-form-item label="调用日志参数 / 返回记录细节" class="payload-detail-field">
            <div class="payload-detail-control">
              <el-segmented v-model="form.gatewayConfig.payloadLogDetail" :options="payloadLogDetailOptions" aria-label="调用日志参数和返回记录细节" />
              <small>{{ payloadLogDetailDescriptions[form.gatewayConfig.payloadLogDetail] }} 保存后立即作用于新进入的调用。</small>
              <div class="payload-storage-row">
                <span class="payload-storage-metric"><strong>当前占用空间</strong><b v-if="!logStorageLoading && logStorage">{{ formatBytes(logStorage.payloadBytes) }}</b><b v-else-if="!logStorageLoading">--</b><el-skeleton v-else :rows="1" animated /></span>
                <el-button type="danger" plain size="small" :icon="Delete" :loading="clearingPayloads" @click="clearHistoricalPayloads">清理</el-button>
              </div>
              <small v-if="logStorage">清理范围：{{ formatCutoff(logStorage.cutoffAt) }} 之前的参数与正文；每 10 秒刷新。</small>
            </div>
          </el-form-item>
          <el-form-item label="首次渠道配置自动启用模型" class="payload-detail-field">
            <div class="common-model-control">
              <el-select v-model="form.gatewayConfig.commonModelNames" multiple filterable allow-create default-first-option placeholder="输入模型名称后回车" aria-label="首次配置自动启用的常用模型">
                <el-option v-for="modelName in form.gatewayConfig.commonModelNames" :key="modelName" :label="modelName" :value="modelName" />
              </el-select>
              <small>新建渠道首次获取上游模型时，精确匹配这些名称的新增映射会自动启用。</small>
            </div>
          </el-form-item>
        </div>
      </section>

      <div class="settings-actions"><el-button type="primary" :icon="Check" :loading="saving" native-type="submit">保存设置</el-button></div>
    </el-form>

    <section class="surface-panel settings-section project-section">
      <header class="panel-heading"><div><h2>项目与责任说明</h2><p>当前构建来源和使用边界</p></div></header>
      <dl class="project-metadata">
        <div><dt>项目名称</dt><dd>{{ projectMeta.displayName }}</dd></div>
        <div><dt>构建分支</dt><dd><code>{{ buildBranch }}</code></dd></div>
        <div class="repository-row">
          <dt>GitHub 仓库</dt>
          <dd><el-link :icon="Link" :href="repositoryUrl" target="_blank" rel="noreferrer">{{ repositoryUrl }}</el-link></dd>
        </div>
      </dl>
      <div class="disclaimer" role="note" aria-label="免责声明">
        <strong>免责声明</strong>
        <p>本项目按“现状”提供，不附带任何明示或暗示保证。使用者应自行评估适用性、安全性与合规性，并承担部署、配置、数据处理及第三方服务调用产生的风险与责任。</p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.settings-form { display: grid; gap: 16px; }
.settings-fields { padding: 20px 22px 8px; }
.settings-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0 18px; }
.settings-actions { display: flex; justify-content: flex-end; position: sticky; bottom: 12px; padding: 10px; border: 1px solid var(--rose-border); background: var(--rose-surface); }
.routing-formula { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); margin: 0; padding: 4px 22px 20px; }
.routing-formula > div { min-width: 0; padding: 10px 14px; border-left: 2px solid var(--rose-border-strong); }
.routing-formula dt { color: var(--rose-text); font-size: 11px; font-weight: 650; }
.routing-formula dd { margin: 5px 0 0; color: var(--rose-text-muted); font-size: 11px; line-height: 1.6; }
.payload-detail-field { grid-column: 1 / -1; }
.payload-detail-control { display: grid; justify-items: start; gap: 8px; }
.payload-detail-control small { color: var(--rose-text-muted); font-size: 11px; line-height: 1.6; }
.payload-storage-row { display: flex; width: 100%; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 12px; border: 1px solid var(--rose-border); background: var(--rose-surface-muted); }
.payload-storage-metric { display: flex; align-items: baseline; gap: 10px; color: var(--rose-text-muted); font-size: 11px; }
.payload-storage-metric b { color: var(--rose-text); font-family: var(--rose-font-mono); font-size: 16px; }
.payload-storage-metric :deep(.el-skeleton) { width: 70px; }
.common-model-control { display: grid; gap: 8px; width: 100%; }
.common-model-control .el-select { width: 100%; }
.common-model-control small { color: var(--rose-text-muted); font-size: 11px; line-height: 1.6; }
.project-section { margin-top: 16px; }
.project-metadata { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; margin: 0; padding: 20px 22px; }
.project-metadata > div { min-width: 0; }
.project-metadata dt { margin-bottom: 6px; color: var(--rose-text-subtle); font-size: 11px; }
.project-metadata dd { min-width: 0; margin: 0; color: var(--rose-text); font-size: 13px; font-weight: 650; overflow-wrap: anywhere; }
.project-metadata code { font-family: var(--rose-font-mono); font-size: 12px; font-weight: 500; }
.repository-row { grid-column: 1 / -1; }
.repository-row :deep(.el-link) { max-width: 100%; font-size: 12px; vertical-align: top; }
.repository-row :deep(.el-link__inner) { min-width: 0; overflow-wrap: anywhere; text-align: left; }
.disclaimer { margin: 0 22px 22px; padding: 14px 16px; border-left: 3px solid var(--rose-warning); background: var(--rose-surface-muted); color: var(--rose-text-muted); }
.disclaimer strong { display: block; margin-bottom: 5px; color: var(--rose-text); font-size: 12px; }
.disclaimer p { margin: 0; font-size: 12px; line-height: 1.7; }
@media (max-width: 800px) {
  .settings-grid { grid-template-columns: 1fr 1fr; }
  .routing-formula { grid-template-columns: 1fr; gap: 8px; }
}
@media (max-width: 520px) {
  .settings-grid, .project-metadata { grid-template-columns: 1fr; }
  .repository-row { grid-column: auto; }
  .project-metadata { gap: 14px; padding: 18px; }
  .disclaimer { margin: 0 18px 18px; }
}
</style>
