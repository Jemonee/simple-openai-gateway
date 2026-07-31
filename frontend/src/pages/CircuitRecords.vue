<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ArrowRight, Refresh, RefreshLeft, Search, Unlock, WarningFilled } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { Channel, ChannelModel, CircuitRecord, CircuitRecordPage, CircuitResolution } from '@/types/gateway'
import { request } from '@/utils/api'

interface CircuitModelGroup {
  key: string
  modelId: number
  modelName: string
  upstreamModels: string[]
  records: CircuitRecord[]
  pendingCount: number
  mapping: ChannelModel | null
}

interface CircuitChannelGroup {
  key: string
  channelId: number
  channelName: string
  models: CircuitModelGroup[]
  recordCount: number
  pendingCount: number
  channel: Channel | null
}

const loading = ref(true)
const errorMessage = ref('')
const records = ref<CircuitRecord[]>([])
const channels = ref<Channel[]>([])
const total = ref(0)
const pendingManual = ref(0)
const reopeningRecordId = ref<number | null>(null)
const expandedChannelKeys = ref<Set<string>>(new Set())
const expandedModelKeys = ref<Set<string>>(new Set())
const updatingChannelIds = ref<Set<number>>(new Set())
const updatingModelKeys = ref<Set<string>>(new Set())
const knownChannelKeys = new Set<string>()
const filters = reactive({ channelId: '', level: '', status: '' })
const pagination = reactive({ page: 1, pageSize: 50 })
const groupedRecords = computed<CircuitChannelGroup[]>(() => {
  const channelGroups = new Map<number, CircuitChannelGroup>()
  const modelGroups = new Map<string, CircuitModelGroup>()

  records.value.forEach((record) => {
    let channel = channelGroups.get(record.channelId)
    if (!channel) {
      const channelConfiguration = channels.value.find((item) => item.id === record.channelId) ?? null
      channel = {
        key: `channel-${record.channelId}`,
        channelId: record.channelId,
        channelName: record.channelName,
        models: [],
        recordCount: 0,
        pendingCount: 0,
        channel: channelConfiguration,
      }
      channelGroups.set(record.channelId, channel)
    }
    const modelKey = `${record.channelId}:${record.modelId}`
    let model = modelGroups.get(modelKey)
    if (!model) {
      model = {
        key: modelKey,
        modelId: record.modelId,
        modelName: record.modelName,
        upstreamModels: [],
        records: [],
        pendingCount: 0,
        mapping: channel.channel?.models.find((item) => item.modelId === record.modelId) ?? null,
      }
      modelGroups.set(modelKey, model)
      channel.models.push(model)
    }
    if (!model.upstreamModels.includes(record.upstreamModel)) model.upstreamModels.push(record.upstreamModel)
    model.records.push(record)
    channel.recordCount += 1
    if (!record.resolvedAt) {
      model.pendingCount += 1
      channel.pendingCount += 1
    }
  })

  return [...channelGroups.values()]
})

const resolutionLabels: Record<CircuitResolution, string> = {
  '': '',
  automatic_recovery: '自动恢复',
  escalated: '已升级',
  manual_reopen: '人工开启',
  mapping_removed: '映射已删除',
  manual_reset: '人工重置',
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function levelLabel(level: number): string {
  return `${['', '一', '二', '三'][level] ?? level}级`
}

function levelType(level: number): 'warning' | 'danger' {
  return level >= 3 ? 'danger' : 'warning'
}

function recordStatus(record: CircuitRecord): string {
  if (record.resolvedAt) return resolutionLabels[record.resolution] || '已恢复'
  if (record.level === 3) return '待人工开启'
  if (record.openUntil && Date.parse(record.openUntil) > Date.now()) return '熔断中'
  return '恢复探测中'
}

function statusType(record: CircuitRecord): 'success' | 'warning' | 'danger' | 'info' {
  if (record.resolvedAt) return record.resolution === 'escalated' ? 'warning' : 'success'
  return record.level === 3 ? 'danger' : 'warning'
}

function canReopen(record: CircuitRecord): boolean {
  return record.level === 3 && !record.resolvedAt && record.mappingExists && record.mappingCircuitDisabled
}

function buildQuery(): URLSearchParams {
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.channelId) query.set('channelId', filters.channelId)
  if (filters.level) query.set('level', filters.level)
  if (filters.status) query.set('status', filters.status)
  return query
}

function applyCircuitPage(page: CircuitRecordPage) {
  records.value = page.items
  total.value = page.total
  pendingManual.value = page.pendingManual
  const nextExpandedChannels = new Set(expandedChannelKeys.value)
  page.items.forEach((record) => {
    const key = `channel-${record.channelId}`
    if (!knownChannelKeys.has(key)) nextExpandedChannels.add(key)
    knownChannelKeys.add(key)
  })
  expandedChannelKeys.value = nextExpandedChannels
}

async function loadRecords(showLoading = true) {
  if (showLoading) loading.value = true
  errorMessage.value = ''
  try {
    const page = await request<CircuitRecordPage>(`/admin/gateway/circuit-records?${buildQuery()}`)
    applyCircuitPage(page)
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '熔断记录加载失败'
  } finally {
    if (showLoading) loading.value = false
  }
}

function toggleExpandedChannel(key: string) {
  const next = new Set(expandedChannelKeys.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedChannelKeys.value = next
}

function toggleExpandedModel(key: string) {
  const next = new Set(expandedModelKeys.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedModelKeys.value = next
}

function pendingSet<T extends number | string>(current: Set<T>, key: T, pending: boolean): Set<T> {
  const next = new Set(current)
  if (pending) next.add(key)
  else next.delete(key)
  return next
}

async function refreshAfterToggle() {
  const [channelResult, recordResult] = await Promise.allSettled([
    request<Channel[]>('/admin/gateway/channels'),
    request<CircuitRecordPage>(`/admin/gateway/circuit-records?${buildQuery()}`),
  ])
  if (channelResult.status === 'fulfilled') channels.value = channelResult.value
  if (recordResult.status === 'fulfilled') applyCircuitPage(recordResult.value)
  if (channelResult.status === 'rejected' || recordResult.status === 'rejected') {
    ElMessage.warning('状态已保存，但最新数据刷新失败，请手动刷新')
  }
}

async function updateChannelEnabled(channel: Channel, enabled: boolean) {
  if (updatingChannelIds.value.has(channel.id)) return
  const previousEnabled = channel.enabled
  channel.enabled = enabled
  updatingChannelIds.value = pendingSet(updatingChannelIds.value, channel.id, true)
  try {
    await request<Channel>(`/admin/gateway/channels/${channel.id}`, {
      method: 'PUT',
      body: JSON.stringify({
        name: channel.name,
        baseUrl: channel.baseUrl,
        apiKey: '',
        enabled,
        supportsStreamUsage: channel.supportsStreamUsage,
        priceMultiplierBasisPoints: channel.priceMultiplierBasisPoints,
      }),
    })
    ElMessage.success(enabled ? '渠道已开启' : '渠道已关闭')
    await refreshAfterToggle()
  } catch (error) {
    channel.enabled = previousEnabled
    ElMessage.error(error instanceof Error ? error.message : '渠道状态更新失败')
  } finally {
    updatingChannelIds.value = pendingSet(updatingChannelIds.value, channel.id, false)
  }
}

async function updateMappingEnabled(channel: Channel, mapping: ChannelModel, enabled: boolean) {
  const key = `${channel.id}:${mapping.modelId}`
  if (updatingModelKeys.value.has(key)) return
  const previousEnabled = mapping.enabled
  mapping.enabled = enabled
  updatingModelKeys.value = pendingSet(updatingModelKeys.value, key, true)
  try {
    await request<ChannelModel[]>(`/admin/gateway/channels/${channel.id}/models`, {
      method: 'PUT',
      body: JSON.stringify(channel.models.map((item) => ({
        modelId: item.modelId,
        upstreamModel: item.upstreamModel,
        priority: item.priority,
        weight: item.weight,
        inputPriceMicros: item.inputPriceMicros,
        outputPriceMicros: item.outputPriceMicros,
        cachedInputPriceMicros: item.cachedInputPriceMicros,
        cacheWritePriceMicros: item.cacheWritePriceMicros,
        priceMultiplierBasisPoints: item.priceMultiplierBasisPoints,
        enabled: item.modelId === mapping.modelId ? enabled : item.enabled,
      }))),
    })
    ElMessage.success(enabled ? '模型映射已开启' : '模型映射已关闭')
    await refreshAfterToggle()
  } catch (error) {
    mapping.enabled = previousEnabled
    ElMessage.error(error instanceof Error ? error.message : '模型映射状态更新失败')
  } finally {
    updatingModelKeys.value = pendingSet(updatingModelKeys.value, key, false)
  }
}

function searchRecords() {
  pagination.page = 1
  void loadRecords()
}

function resetRecords() {
  Object.assign(filters, { channelId: '', level: '', status: '' })
  pagination.page = 1
  void loadRecords()
}

async function reopenMapping(record: CircuitRecord) {
  try {
    await ElMessageBox.confirm(
      `人工开启“${record.channelName} / ${record.modelName}”映射？`,
      '开启熔断映射',
      { type: 'warning', confirmButtonText: '开启映射', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  reopeningRecordId.value = record.id
  try {
    await request<null>(`/admin/gateway/circuit-records/${record.id}/reopen-mapping`, { method: 'POST' })
    ElMessage.success('映射已开启并重新参与调度')
    await loadRecords()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '映射开启失败')
  } finally {
    reopeningRecordId.value = null
  }
}

onMounted(async () => {
  try {
    channels.value = await request<Channel[]>('/admin/gateway/channels')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '渠道筛选项加载失败'
  }
  await loadRecords()
})
</script>

<template>
  <div class="page-stack circuit-record-page">
    <header class="page-heading">
      <div><h1>熔断记录</h1><p>渠道与模型映射的分级熔断事件</p></div>
      <div class="page-actions"><el-tooltip content="刷新熔断记录" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新熔断记录" @click="loadRecords" /></el-tooltip></div>
    </header>

    <section class="filter-bar circuit-filter-bar" aria-label="熔断记录筛选">
      <el-select v-model="filters.channelId" clearable placeholder="全部渠道"><el-option v-for="channel in channels" :key="channel.id" :label="channel.name" :value="String(channel.id)" /></el-select>
      <el-select v-model="filters.level" clearable placeholder="全部等级"><el-option label="一级熔断" value="1" /><el-option label="二级熔断" value="2" /><el-option label="三级熔断" value="3" /></el-select>
      <el-select v-model="filters.status" clearable placeholder="全部状态"><el-option label="待恢复" value="pending" /><el-option label="已结束" value="resolved" /></el-select>
      <div class="filter-actions"><el-button :icon="RefreshLeft" :disabled="loading" @click="resetRecords">重置</el-button><el-button type="primary" :icon="Search" :loading="loading" @click="searchRecords">查询</el-button></div>
    </section>

    <section v-if="!errorMessage" class="circuit-summary" aria-label="熔断记录汇总">
      <div><span><WarningFilled />当前筛选记录</span><strong>{{ total }}</strong></div>
      <div :class="{ 'has-pending': pendingManual > 0 }"><span><Unlock />待人工开启映射</span><strong>{{ pendingManual }}</strong></div>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>熔断记录加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadRecords">重试</el-button></div>
    <section v-else v-loading="loading" class="surface-panel circuit-tree-panel" aria-live="polite">
      <div v-if="!loading && groupedRecords.length === 0" class="circuit-empty">
        <WarningFilled />
        <strong>没有熔断记录</strong>
        <span>当前筛选条件下没有渠道或模型触发熔断。</span>
      </div>
      <div v-else class="circuit-tree-scroll">
        <article v-for="channel in groupedRecords" :key="channel.key" class="channel-group">
          <header class="channel-group-heading">
            <button
              class="fold-button"
              type="button"
              :aria-expanded="expandedChannelKeys.has(channel.key)"
              :aria-label="`${expandedChannelKeys.has(channel.key) ? '折叠' : '展开'}渠道 ${channel.channelName}`"
              @click="toggleExpandedChannel(channel.key)"
            >
              <ArrowRight :class="{ 'is-expanded': expandedChannelKeys.has(channel.key) }" />
            </button>
            <span class="hierarchy-index">渠道</span>
            <div><h2>{{ channel.channelName }}</h2><p>渠道 #{{ channel.channelId }} · {{ channel.models.length }} 个模型</p></div>
            <div class="group-counts"><strong>{{ channel.recordCount }}</strong><span>条记录</span><b v-if="channel.pendingCount">{{ channel.pendingCount }} 条待恢复</b></div>
            <div class="state-toggle" @click.stop>
              <template v-if="channel.channel">
                <span>{{ channel.channel.enabled ? '已开启' : '已关闭' }}</span>
                <el-switch
                  :model-value="channel.channel.enabled"
                  :loading="updatingChannelIds.has(channel.channel.id)"
                  :disabled="updatingChannelIds.has(channel.channel.id)"
                  :aria-label="`${channel.channel.enabled ? '关闭' : '开启'}渠道 ${channel.channelName}`"
                  @change="updateChannelEnabled(channel.channel, Boolean($event))"
                />
              </template>
              <el-tag v-else type="info" effect="plain">渠道已删除</el-tag>
            </div>
          </header>

          <div v-show="expandedChannelKeys.has(channel.key)" class="channel-group-body">
          <div class="model-groups">
            <section v-for="model in channel.models" :key="model.key" class="model-group">
              <header class="model-group-heading">
                <span class="hierarchy-connector" aria-hidden="true" />
                <button
                  class="fold-button"
                  type="button"
                  :aria-expanded="expandedModelKeys.has(model.key)"
                  :aria-label="`${expandedModelKeys.has(model.key) ? '折叠' : '展开'}模型 ${model.modelName} 的熔断事件`"
                  @click="toggleExpandedModel(model.key)"
                >
                  <ArrowRight :class="{ 'is-expanded': expandedModelKeys.has(model.key) }" />
                </button>
                <span class="hierarchy-index">模型</span>
                <div>
                  <h3><code>{{ model.modelName }}</code></h3>
                  <p>上游 <code>{{ model.upstreamModels.join('、') }}</code></p>
                </div>
                <div class="model-count"><strong>{{ model.records.length }}</strong><span>条事件</span><b v-if="model.pendingCount">{{ model.pendingCount }} 条处理中</b></div>
                <div class="state-toggle" @click.stop>
                  <template v-if="channel.channel && model.mapping">
                    <span>{{ model.mapping.enabled ? '已开启' : '已关闭' }}</span>
                    <el-switch
                      :model-value="model.mapping.enabled"
                      :loading="updatingModelKeys.has(model.key)"
                      :disabled="updatingModelKeys.has(model.key)"
                      :aria-label="`${model.mapping.enabled ? '关闭' : '开启'}模型映射 ${model.modelName}`"
                      @change="updateMappingEnabled(channel.channel, model.mapping, Boolean($event))"
                    />
                  </template>
                  <el-tag v-else type="info" effect="plain">映射已删除</el-tag>
                </div>
              </header>

              <ol v-show="expandedModelKeys.has(model.key)" class="record-list" aria-label="熔断事件列表">
                <li v-for="record in model.records" :key="record.id" class="record-row">
                  <div class="record-marker" :class="`is-level-${record.level}`"><span>{{ record.level }}</span></div>
                  <div class="record-time"><strong>{{ formatDate(record.createdAt) }}</strong><small>记录 #{{ record.id }}</small></div>
                  <div class="record-trigger"><el-tag :type="levelType(record.level)" effect="plain">{{ levelLabel(record.level) }}熔断</el-tag><small>{{ record.immediate ? '严重错误立即升级' : `${record.failureCount} 次连续失败` }}</small></div>
                  <div class="record-status"><el-tag :type="statusType(record)" effect="plain">{{ recordStatus(record) }}</el-tag><small v-if="record.resolvedAt">{{ formatDate(record.resolvedAt) }}</small><small v-else-if="record.openUntil">截止 {{ formatDate(record.openUntil) }}</small></div>
                  <el-tooltip :content="record.message" placement="top"><p class="failure-message">{{ record.message }}</p></el-tooltip>
                  <div class="record-action">
                    <el-tooltip v-if="canReopen(record)" content="人工开启模型映射" placement="top"><el-button class="table-action-button" text type="warning" :icon="Unlock" :loading="reopeningRecordId === record.id" aria-label="人工开启模型映射" @click="reopenMapping(record)" /></el-tooltip>
                  </div>
                </li>
              </ol>
            </section>
          </div>
          </div>
        </article>
      </div>
      <footer class="table-pagination"><el-pagination v-model:current-page="pagination.page" v-model:page-size="pagination.pageSize" :disabled="loading" :total="total" :page-sizes="[25, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadRecords" /></footer>
    </section>
  </div>
</template>

<style scoped>
.circuit-record-page { padding-bottom: 16px; }
.circuit-filter-bar { grid-template-columns: repeat(3, minmax(160px, 1fr)) auto; }
.circuit-summary { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); border: 1px solid var(--rose-border); background: var(--rose-surface); }
.circuit-summary > div { display: grid; grid-template-columns: 1fr auto; align-items: center; gap: 4px 16px; min-height: 66px; padding: 10px 16px; }
.circuit-summary > div + div { border-left: 1px solid var(--rose-border); }
.circuit-summary span { display: flex; align-items: center; gap: 7px; color: var(--rose-text-muted); font-size: 12px; }
.circuit-summary span svg { width: 15px; }
.circuit-summary strong { grid-row: 1 / span 2; grid-column: 2; color: var(--rose-text); font: 650 22px/1 var(--rose-font-mono); }
.circuit-summary .has-pending strong { color: var(--rose-danger); }
.circuit-tree-panel { display: flex; min-height: 260px; flex-direction: column; }
.circuit-tree-scroll { min-height: 0; flex: 1 1 auto; overflow: auto; }
.circuit-tree-panel .table-pagination { flex: none; min-height: 56px; align-items: center; background: var(--rose-surface); }
.circuit-empty { display: grid; min-height: 240px; flex: 1; place-content: center; justify-items: center; gap: 8px; color: var(--rose-text-muted); }
.circuit-empty svg { width: 30px; color: var(--rose-text-subtle); }
.circuit-empty strong { color: var(--rose-text); font-size: 14px; }
.circuit-empty span { font-size: 12px; }
.channel-group + .channel-group { border-top: 8px solid var(--rose-surface-muted); }
.channel-group-heading { display: grid; grid-template-columns: auto auto minmax(0, 1fr) auto auto; align-items: center; gap: 12px; min-height: 64px; padding: 10px 16px; border-bottom: 1px solid var(--rose-border); background: var(--rose-surface-muted); }
.fold-button { display: inline-grid; width: 30px; height: 30px; place-items: center; padding: 0; border: 0; border-radius: var(--rose-radius-control); color: var(--rose-text-muted); background: transparent; cursor: pointer; }
.fold-button:hover { color: var(--rose-primary-hover); background: var(--rose-primary-soft); }
.fold-button:focus-visible { outline: 2px solid var(--rose-primary); outline-offset: 1px; }
.fold-button svg { width: 15px; transition: transform 140ms ease; }
.fold-button svg.is-expanded { transform: rotate(90deg); }
.hierarchy-index { display: inline-grid; min-width: 42px; height: 22px; place-items: center; border: 1px solid var(--rose-border-strong); border-radius: var(--rose-radius-control); color: var(--rose-text-muted); background: var(--rose-surface); font-size: 10px; font-weight: 650; }
.channel-group-heading h2, .model-group-heading h3 { margin: 0; color: var(--rose-text); font-size: 14px; font-weight: 650; }
.channel-group-heading p, .model-group-heading p { margin: 3px 0 0; color: var(--rose-text-muted); font-size: 10px; }
.group-counts, .model-count { display: grid; grid-template-columns: auto auto; align-items: baseline; justify-content: end; gap: 2px 6px; color: var(--rose-text-muted); }
.group-counts strong, .model-count strong { color: var(--rose-text); font: 650 17px/1 var(--rose-font-mono); }
.group-counts span, .model-count span { font-size: 10px; }
.group-counts b, .model-count b { grid-column: 1 / -1; color: var(--rose-danger); font-size: 10px; font-weight: 600; text-align: right; }
.state-toggle { display: flex; min-width: 92px; align-items: center; justify-content: flex-end; gap: 8px; }
.state-toggle > span { color: var(--rose-text-muted); font-size: 10px; white-space: nowrap; }
.channel-group-body { max-height: min(520px, 55dvh); overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.model-groups { padding-left: 28px; }
.model-group { position: relative; border-left: 1px solid var(--rose-border-strong); }
.model-group + .model-group { border-top: 1px solid var(--rose-border); }
.model-group-heading { position: relative; display: grid; grid-template-columns: auto auto minmax(0, 1fr) auto auto; align-items: center; gap: 12px; min-height: 58px; padding: 9px 16px 9px 26px; background: color-mix(in srgb, var(--rose-surface-muted) 64%, var(--rose-surface)); }
.hierarchy-connector { position: absolute; top: 50%; left: 0; width: 18px; border-top: 1px solid var(--rose-border-strong); }
.model-group-heading code { font-size: 12px; }
.record-list { max-height: 260px; margin: 0; padding: 0 16px 8px 26px; overflow-y: auto; overscroll-behavior: contain; list-style: none; scrollbar-gutter: stable; }
.record-row { position: relative; display: grid; grid-template-columns: 142px 138px 142px minmax(220px, 1fr) 38px; align-items: center; gap: 12px; min-height: 70px; padding: 10px 0 10px 34px; border-top: 1px solid var(--rose-border); }
.record-marker { position: absolute; top: 50%; left: -12px; display: grid; width: 24px; height: 24px; place-items: center; border: 3px solid var(--rose-surface); border-radius: 50%; color: var(--rose-surface); background: var(--rose-warning); box-shadow: 0 0 0 1px var(--rose-border-strong); transform: translateY(-50%); }
.record-marker.is-level-3 { background: var(--rose-danger); }
.record-marker span { font: 700 10px/1 var(--rose-font-mono); }
.record-time, .record-trigger, .record-status { display: grid; min-width: 0; justify-items: start; gap: 4px; }
.record-time strong { color: var(--rose-text); font-size: 11px; font-weight: 600; white-space: nowrap; }
.record-time small, .record-trigger small, .record-status small { color: var(--rose-text-subtle); font-size: 10px; }
.failure-message { display: -webkit-box; min-width: 0; margin: 0; overflow: hidden; color: var(--rose-text-muted); font-size: 11px; line-height: 1.5; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 2; }
.record-action { display: flex; justify-content: flex-end; }
@media (min-width: 961px) {
  .circuit-record-page { height: 100%; min-height: 0; grid-template-rows: auto auto auto minmax(0, 1fr); overflow: hidden; padding-bottom: 0; }
  .circuit-tree-panel { min-height: 0; }
}
@media (max-width: 1080px) {
  .record-row { grid-template-columns: 130px 132px minmax(180px, 1fr) 38px; }
  .record-status { grid-column: 2; grid-row: 2; }
  .failure-message { grid-column: 3; grid-row: 1 / span 2; }
  .record-action { grid-column: 4; grid-row: 1 / span 2; }
}
@media (max-width: 720px) {
  .circuit-filter-bar { grid-template-columns: 1fr; }
  .circuit-summary { grid-template-columns: 1fr; }
  .circuit-summary > div + div { border-top: 1px solid var(--rose-border); border-left: 0; }
  .model-groups { padding-left: 16px; }
  .channel-group-heading, .model-group-heading { grid-template-columns: auto auto minmax(0, 1fr); }
  .group-counts, .model-count { grid-column: 3; justify-content: start; }
  .state-toggle { grid-column: 2 / -1; justify-content: flex-start; }
  .record-list { padding-right: 10px; padding-left: 18px; }
  .record-row { grid-template-columns: minmax(0, 1fr) auto; gap: 8px 12px; padding: 12px 0 12px 26px; }
  .record-time { grid-column: 1; grid-row: 1; }
  .record-trigger { grid-column: 1; grid-row: 2; }
  .record-status { grid-column: 2; grid-row: 1 / span 2; }
  .failure-message { grid-column: 1 / -1; grid-row: 3; }
  .record-action { grid-column: 2; grid-row: 3; }
}
</style>
