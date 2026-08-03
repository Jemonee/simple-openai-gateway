<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { CircleClose, CopyDocument, Edit, Plus, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { ClientToken, GatewayModel, IssuedClientToken } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'

const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const tokens = ref<ClientToken[]>([])
const models = ref<GatewayModel[]>([])
const dialogOpen = ref(false)
const secretDialogOpen = ref(false)
const issuedSecret = ref('')
const editingId = ref<number | null>(null)
const rotatingTokenId = ref<number | null>(null)
const revokingTokenId = ref<number | null>(null)
const copyingSecret = ref(false)
const form = reactive({ name: '', enabled: true, allowAllModels: true, rpm: 60, maxConcurrency: 10, modelIds: [] as number[] })
const dialogTitle = computed(() => editingId.value ? '编辑访问令牌' : '签发访问令牌')

function modelNames(token: ClientToken): string {
  if (token.allowAllModels) return '全部公开模型'
  return token.modelIds.map((id) => models.value.find((model) => model.id === id)?.name ?? `#${id}`).join('、')
}

function openEditor(token?: ClientToken) {
  editingId.value = token?.id ?? null
  form.name = token?.name ?? ''
  form.enabled = token?.enabled ?? true
  form.allowAllModels = token?.allowAllModels ?? true
  form.rpm = token?.rpm ?? 60
  form.maxConcurrency = token?.maxConcurrency ?? 10
  form.modelIds = [...(token?.modelIds ?? [])]
  dialogOpen.value = true
}

async function loadData() {
  loading.value = true
  errorMessage.value = ''
  try {
    [tokens.value, models.value] = await Promise.all([
      request<ClientToken[]>('/admin/gateway/tokens'),
      request<GatewayModel[]>('/admin/gateway/models'),
    ])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '访问令牌加载失败'
  } finally {
    loading.value = false
  }
}

function tokenPayload() {
  return {
    name: form.name,
    enabled: form.enabled,
    allowAllModels: form.allowAllModels,
    rpm: form.rpm,
    maxConcurrency: form.maxConcurrency,
    modelIds: form.allowAllModels ? [] : form.modelIds,
  }
}

async function saveToken() {
  if (!form.name.trim() || (!form.allowAllModels && form.modelIds.length === 0)) {
    ElMessage.error('请输入令牌名称并配置模型权限')
    return
  }
  saving.value = true
  try {
    if (editingId.value) {
      await request<ClientToken>(`/admin/gateway/tokens/${editingId.value}`, { method: 'PUT', body: JSON.stringify(tokenPayload()) })
      ElMessage.success('令牌策略已保存')
    } else {
      const issued = await request<IssuedClientToken>('/admin/gateway/tokens', { method: 'POST', body: JSON.stringify(tokenPayload()) })
      showSecret(issued.secret)
    }
    dialogOpen.value = false
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '令牌保存失败')
  } finally {
    saving.value = false
  }
}

function showSecret(secret: string) {
  issuedSecret.value = secret
  secretDialogOpen.value = true
}

async function copySecret() {
  copyingSecret.value = true
  try {
    await navigator.clipboard.writeText(issuedSecret.value)
    ElMessage.success('令牌已复制')
  } catch {
    ElMessage.error('无法访问剪贴板，请手动选择令牌')
  } finally {
    copyingSecret.value = false
  }
}

async function rotateToken(token: ClientToken) {
  await ElMessageBox.confirm(`轮换“${token.name}”后，旧令牌会立即失效。`, '轮换令牌', { type: 'warning', confirmButtonText: '轮换', cancelButtonText: '取消' })
  rotatingTokenId.value = token.id
  try {
    const issued = await request<IssuedClientToken>(`/admin/gateway/tokens/${token.id}/rotate`, { method: 'POST' })
    showSecret(issued.secret)
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '令牌轮换失败')
  } finally {
    rotatingTokenId.value = null
  }
}

async function revokeToken(token: ClientToken) {
  await ElMessageBox.confirm(`吊销“${token.name}”？现有客户端将无法继续调用。`, '吊销令牌', { type: 'warning', confirmButtonText: '吊销', cancelButtonText: '取消' })
  revokingTokenId.value = token.id
  try {
    await request<ClientToken>(`/admin/gateway/tokens/${token.id}`, {
      method: 'PUT',
      body: JSON.stringify({ name: token.name, enabled: false, allowAllModels: token.allowAllModels, rpm: token.rpm, maxConcurrency: token.maxConcurrency, modelIds: token.modelIds }),
    })
    ElMessage.success('令牌已吊销')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '令牌吊销失败')
  } finally {
    revokingTokenId.value = null
  }
}

function formatDate(value: string | null): string {
  return value ? new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(new Date(value)) : '从未使用'
}

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(micros / 1_000_000)
}

function formatTiming(value: number, samples: number): string {
  return samples > 0 ? formatDuration(value) : '--'
}

onMounted(loadData)
</script>

<template>
  <div class="page-stack">
    <header class="page-heading">
      <div><h1>访问令牌</h1><p>签发客户端 sk- Token，并限制模型、RPM 与并发数</p></div>
      <div class="page-actions"><el-tooltip content="刷新令牌列表" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新令牌列表" @click="loadData" /></el-tooltip><el-button type="primary" :icon="Plus" @click="openEditor()">签发令牌</el-button></div>
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>访问令牌加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadData">重试</el-button></div>
    <section v-else class="surface-panel table-panel">
      <el-table v-loading="loading" :data="tokens" row-key="id" empty-text="还没有访问令牌">
        <el-table-column label="令牌" min-width="190"><template #default="scope"><div class="primary-cell"><strong>{{ scope.row.name }}</strong><small><code>{{ scope.row.keyPrefix }}</code></small></div></template></el-table-column>
        <el-table-column label="状态" width="100"><template #default="scope"><el-tag :type="scope.row.enabled ? 'success' : 'info'" effect="plain">{{ scope.row.enabled ? '有效' : '已吊销' }}</el-tag></template></el-table-column>
        <el-table-column label="模型权限" min-width="190"><template #default="scope"><span class="clamped-text">{{ modelNames(scope.row) }}</span></template></el-table-column>
        <el-table-column label="RPM" width="88" align="right" prop="rpm" />
        <el-table-column label="并发" width="88" align="right" prop="maxConcurrency" />
        <el-table-column label="累计统计" min-width="220"><template #default="scope"><div class="primary-cell"><strong>{{ formatCompactNumber(scope.row.statistics.requests) }} 次 · {{ formatUSD(scope.row.statistics.upstreamCostMicros) }}</strong><small>估算 {{ formatUSD(scope.row.statistics.estimatedCostMicros) }} · {{ formatCompactNumber(scope.row.statistics.inputTokens + scope.row.statistics.outputTokens) }} Tokens</small></div></template></el-table-column>
        <el-table-column label="性能统计" min-width="250"><template #default="scope"><div class="primary-cell"><strong>首 Token {{ formatTiming(scope.row.statistics.averageFirstTokenMs, scope.row.statistics.firstTokenSampleCount) }} · 延迟 {{ formatTiming(scope.row.statistics.averageLatencyMs, scope.row.statistics.latencySampleCount) }}</strong><small>请求耗时 {{ formatTiming(scope.row.statistics.averageDurationMs, scope.row.statistics.durationSampleCount) }}</small></div></template></el-table-column>
        <el-table-column label="最近使用" width="154"><template #default="scope">{{ formatDate(scope.row.lastUsedAt) }}</template></el-table-column>
        <el-table-column label="操作" width="126" fixed="right" align="right"><template #default="scope"><div class="table-actions"><el-tooltip content="编辑令牌策略" placement="top"><el-button class="table-action-button" text :icon="Edit" :disabled="rotatingTokenId === scope.row.id || revokingTokenId === scope.row.id" aria-label="编辑令牌策略" @click="openEditor(scope.row)" /></el-tooltip><el-tooltip content="轮换令牌" placement="top"><el-button class="table-action-button" text :icon="RefreshRight" :loading="rotatingTokenId === scope.row.id" :disabled="revokingTokenId === scope.row.id" aria-label="轮换令牌" @click="rotateToken(scope.row)" /></el-tooltip><el-tooltip v-if="scope.row.enabled" content="吊销令牌" placement="top"><el-button class="table-action-button" text type="danger" :icon="CircleClose" :loading="revokingTokenId === scope.row.id" :disabled="rotatingTokenId === scope.row.id" aria-label="吊销令牌" @click="revokeToken(scope.row)" /></el-tooltip></div></template></el-table-column>
      </el-table>
      <div v-if="!loading && tokens.length === 0" class="table-empty-action"><el-button type="primary" :icon="Plus" @click="openEditor()">签发第一个令牌</el-button></div>
    </section>

    <el-dialog v-model="dialogOpen" :title="dialogTitle" width="min(560px, calc(100vw - 32px))">
      <el-form label-position="top" @submit.prevent="saveToken">
        <el-form-item label="令牌名称"><el-input v-model="form.name" placeholder="例如 数据分析客户端" /></el-form-item>
        <div class="form-columns"><el-form-item label="每分钟请求数"><el-input-number v-model="form.rpm" :min="1" :max="100000" controls-position="right" /></el-form-item><el-form-item label="最大并发数"><el-input-number v-model="form.maxConcurrency" :min="1" :max="10000" controls-position="right" /></el-form-item></div>
        <el-form-item label="模型权限">
          <el-radio-group v-model="form.allowAllModels"><el-radio-button :value="true">全部模型</el-radio-button><el-radio-button :value="false">指定模型</el-radio-button></el-radio-group>
        </el-form-item>
        <el-checkbox-group v-if="!form.allowAllModels" v-model="form.modelIds" class="model-checks"><el-checkbox v-for="model in models" :key="model.id" :value="model.id">{{ model.name }}</el-checkbox></el-checkbox-group>
        <el-checkbox v-model="form.enabled">令牌立即生效</el-checkbox>
      </el-form>
      <template #footer><div class="dialog-actions"><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveToken">{{ editingId ? '保存策略' : '签发令牌' }}</el-button></div></template>
    </el-dialog>

    <el-dialog v-model="secretDialogOpen" title="令牌已签发" width="min(600px, calc(100vw - 32px))" :close-on-click-modal="false">
      <div class="secret-once"><strong>完整令牌仅显示一次</strong><p>关闭后只能轮换，无法再次查看。</p><code>{{ issuedSecret }}</code><el-button type="primary" :icon="CopyDocument" :loading="copyingSecret" @click="copySecret">复制令牌</el-button></div>
      <template #footer><div class="dialog-actions"><el-button type="primary" @click="secretDialogOpen = false">我已保存</el-button></div></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.model-checks { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin-bottom: 18px; }
.secret-once { display: grid; gap: 10px; }
.secret-once p { color: var(--hongfen-text-muted); }
.secret-once code { padding: 14px; border: 1px solid var(--hongfen-border-strong); background: var(--hongfen-surface-muted); color: var(--hongfen-text); overflow-wrap: anywhere; user-select: all; }
.secret-once .el-button { justify-self: start; }
@media (max-width: 520px) { .model-checks { grid-template-columns: 1fr; } }
</style>
