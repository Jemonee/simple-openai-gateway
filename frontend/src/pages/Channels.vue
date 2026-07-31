<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import type { CSSProperties } from 'vue'
import { Connection, Delete, Edit, Plus, Refresh, RefreshLeft, RefreshRight, Search, Unlock } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import ChannelLatencySparkline from '@/components/ChannelLatencySparkline.vue'
import type {
  ApplicationSettings,
  Channel,
  ChannelModel,
  ChannelModelDiscovery,
  ChannelModelDiscoveryRequest,
  GatewayModel,
  UpstreamModel,
} from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'

interface MappingDraft {
  clientKey: string
  id?: number
  modelId: number | null
  upstreamModel: string
  priority: number
  weight: number
  inputPrice: number
  outputPrice: number
  cachedInputPrice: number | null
  cacheWritePrice: number | null
  adjustmentMultiplier: number
  enabled: boolean
  circuitDisabled: boolean
  recentAttemptCount: number
}

interface MappingGroup {
  key: 'enabled' | 'disabled'
  label: string
  emptyText: string
  items: MappingDraft[]
}

const maxVisibleChannelModels = 3
const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const channels = ref<Channel[]>([])
const models = ref<GatewayModel[]>([])
const channelSearchQuery = ref('')
const drawerOpen = ref(false)
const quickDialogOpen = ref(false)
const quickProcessing = ref(false)
const quickStep = ref(0)
const quickError = ref('')
const editingId = ref<number | null>(null)
const form = reactive({ name: '', baseUrl: '', apiKey: '', enabled: true, supportsStreamUsage: true })
const mappings = ref<MappingDraft[]>([])
const discoveringModels = ref(false)
const discoveryError = ref('')
const discoveredModels = ref<UpstreamModel[]>([])
const discoverySummary = ref<ChannelModelDiscovery | null>(null)
const commonModelNames = ref<string[]>(['gpt-image-2', 'gpt-5.6-terra', 'gpt-5.6-sol', 'gpt-5.6-luna', 'gpt-5.5', 'gpt-5.4-mini', 'codex-auto-review'])
const mappingSearch = ref('')
const priceMultiplier = ref(1)
const testingChannelId = ref<number | null>(null)
const deletingChannelId = ref<number | null>(null)
const resettingCircuitChannelId = ref<number | null>(null)
const currentTime = ref(Date.now())
const drawerTitle = computed(() => editingId.value ? '编辑渠道' : '新增渠道')
const quickDialogTitle = computed(() => editingId.value ? '快速设置渠道' : '快速新增渠道')
const sortedChannels = computed(() => [...channels.value].sort(compareChannels))
const filteredChannels = computed(() => {
  const query = channelSearchQuery.value.trim().toLocaleLowerCase()
  if (!query) return sortedChannels.value
  return sortedChannels.value.filter((channel) => (
    channel.name.toLocaleLowerCase().includes(query)
    || channel.baseUrl.toLocaleLowerCase().includes(query)
  ))
})
const channelTableEmptyText = computed(() => channelSearchQuery.value.trim() ? '未找到匹配的渠道' : '还没有渠道')
const createdPublicModelCount = computed(() => discoveredModels.value.filter((model) => model.publicModelCreated).length)
const sortedPublicModels = computed(() => [...models.value].sort((left, right) => compareModelsByUsage(left.id, left.name, right.id, right.name)))
const sortedDiscoveredModels = computed(() => [...discoveredModels.value].sort((left, right) => (
  Number(Boolean(right.officialPrice)) - Number(Boolean(left.officialPrice))
  || compareModelsByUsage(left.publicModelId, left.id, right.publicModelId, right.id)
)))
const mappingGroups = computed<MappingGroup[]>(() => [
  { key: 'enabled', label: '已启用', emptyText: '没有匹配的已启用映射', items: filteredMappings(true) },
  { key: 'disabled', label: '未启用', emptyText: '没有匹配的未启用映射', items: filteredMappings(false) },
])
const modelHueByName = computed(() => {
  const usedHues = new Set<number>()
  const hues = new Map<string, number>()
  const names = [...new Set(models.value.map((model) => model.name))].sort((left, right) => left.localeCompare(right))
  for (const name of names) {
    let hue = hashModelName(name) % 360
    while (usedHues.has(hue)) hue = (hue + 47) % 360
    usedHues.add(hue)
    hues.set(name, hue)
  }
  return hues
})
let mappingDraftSequence = 0
let discoveryRequestVersion = 0
let clockTimer: ReturnType<typeof setInterval> | undefined

function channelSortLatency(channel: Channel): number {
  if (channel.metrics.latencySampleCount > 0) return channel.metrics.averageLatencyMs
  if (channel.latencyEwmaMs > 0) return channel.latencyEwmaMs
  return Number.POSITIVE_INFINITY
}

function compareChannels(left: Channel, right: Channel): number {
  return Number(right.enabled) - Number(left.enabled)
    || right.metrics.recentAttemptCount - left.metrics.recentAttemptCount
    || channelSortLatency(left) - channelSortLatency(right)
    || left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: 'base' })
}

function compareModelNamesDescending(left: string, right: string): number {
  return right.localeCompare(left, undefined, { numeric: true, sensitivity: 'base' })
}

function modelUsageCount(modelId: number): number {
  return channels.value.reduce((total, channel) => total + channel.models
    .filter((mapping) => mapping.modelId === modelId)
    .reduce((modelTotal, mapping) => modelTotal + mapping.recentAttemptCount, 0), 0)
}

function compareModelsByUsage(leftId: number, leftName: string, rightId: number, rightName: string): number {
  return modelUsageCount(rightId) - modelUsageCount(leftId) || compareModelNamesDescending(leftName, rightName)
}

function hashModelName(value: string): number {
  let hash = 2166136261
  for (const character of value) {
    hash ^= character.codePointAt(0) ?? 0
    hash = Math.imul(hash, 16777619)
  }
  return hash >>> 0
}

function nextMappingClientKey(id?: number): string {
  return id ? `saved-${id}` : `draft-${++mappingDraftSequence}`
}

function fromMicros(value: number | null): number | null {
  return value === null ? null : value / 1_000_000
}

function mappingDraft(mapping: ChannelModel): MappingDraft {
  return {
    clientKey: nextMappingClientKey(mapping.id),
    id: mapping.id,
    modelId: mapping.modelId,
    upstreamModel: mapping.upstreamModel,
    priority: mapping.priority,
    weight: mapping.weight,
    inputPrice: fromMicros(mapping.inputPriceMicros) ?? 0,
    outputPrice: fromMicros(mapping.outputPriceMicros) ?? 0,
    cachedInputPrice: fromMicros(mapping.cachedInputPriceMicros),
    cacheWritePrice: fromMicros(mapping.cacheWritePriceMicros),
    adjustmentMultiplier: Number.isFinite(mapping.priceMultiplierBasisPoints) ? mapping.priceMultiplierBasisPoints / 10_000 : 1,
    enabled: mapping.enabled,
    circuitDisabled: mapping.circuitDisabled,
    recentAttemptCount: mapping.recentAttemptCount,
  }
}

function discoveredMappingDraft(model: UpstreamModel, enabled = false): MappingDraft {
  const price = model.officialPrice
  return {
    clientKey: nextMappingClientKey(),
    modelId: model.publicModelId || null,
    upstreamModel: model.id,
    priority: 0,
    weight: 100,
    inputPrice: fromMicros(price?.inputPriceMicros ?? 0) ?? 0,
    outputPrice: fromMicros(price?.outputPriceMicros ?? 0) ?? 0,
    cachedInputPrice: fromMicros(price?.cachedInputPriceMicros ?? null),
    cacheWritePrice: fromMicros(price?.cacheWritePriceMicros ?? null),
    adjustmentMultiplier: 1,
    enabled,
    circuitDisabled: false,
    recentAttemptCount: 0,
  }
}

function mergeDiscoveredMappings(discovered: UpstreamModel[], enableCommon = false) {
  const configured = new Set(mappings.value.map((mapping) => mapping.upstreamModel.trim()))
  const common = new Set(commonModelNames.value.map((name) => name.trim().toLowerCase()).filter(Boolean))
  for (const model of discovered) {
    if (!configured.has(model.id)) {
      mappings.value.push(discoveredMappingDraft(model, enableCommon && common.has(model.id.toLowerCase())))
      configured.add(model.id)
    }
  }
}

function resetForm(channel?: Channel) {
  discoveryRequestVersion += 1
  editingId.value = channel?.id ?? null
  form.name = channel?.name ?? ''
  form.baseUrl = channel?.baseUrl ?? ''
  form.apiKey = ''
  form.enabled = channel?.enabled ?? true
  form.supportsStreamUsage = channel?.supportsStreamUsage ?? true
  mappings.value = channel?.models.map(mappingDraft) ?? []
  discoveringModels.value = false
  discoveryError.value = ''
  discoveredModels.value = []
  discoverySummary.value = null
  mappingSearch.value = ''
  priceMultiplier.value = channel && Number.isFinite(channel.priceMultiplierBasisPoints)
    ? channel.priceMultiplierBasisPoints / 10_000
    : 1
  drawerOpen.value = false
  quickDialogOpen.value = true
  quickProcessing.value = false
  quickStep.value = 0
  quickError.value = ''
}

function openAdvanced() {
  quickDialogOpen.value = false
  drawerOpen.value = true
}

function openQuick() {
  drawerOpen.value = false
  quickDialogOpen.value = true
  quickError.value = ''
}

function inferredChannelName() {
  if (form.name.trim()) return form.name.trim()
  try {
    const host = new URL(form.baseUrl.trim()).hostname
    return host || '新渠道'
  } catch {
    return '新渠道'
  }
}

function filterMappingsByUpstreamModel(model: UpstreamModel) {
  mappingSearch.value = mappingSearch.value.trim() === model.id ? '' : model.id
}

function supportedModelRowClassName({ row }: { row: UpstreamModel }): string {
  return mappingSearch.value.trim() === row.id ? 'is-filtering-mappings' : ''
}

function addMapping() {
  if (discoveredModels.value.length === 0) {
    ElMessage.warning('当前没有可选择的上游模型')
    return
  }
  const upstreamModel = sortedDiscoveredModels.value.find((model) => !mappings.value.some((mapping) => mapping.upstreamModel === model.id)) ?? sortedDiscoveredModels.value[0]
  mappings.value.push(discoveredMappingDraft(upstreamModel))
}

function modelOptionsForMapping(mapping: MappingDraft): UpstreamModel[] {
  const current = mapping.upstreamModel.trim()
  if (!current || discoveredModels.value.some((model) => model.id === current)) return sortedDiscoveredModels.value
  return [{ id: current, ownedBy: '已配置', created: 0, publicModelId: mapping.modelId ?? 0, publicModelCreated: false, officialPrice: null }, ...sortedDiscoveredModels.value]
}

function selectUpstreamModel(mapping: MappingDraft, upstreamModelId: string) {
  const upstreamModel = discoveredModels.value.find((model) => model.id === upstreamModelId)
  if (upstreamModel?.publicModelId) mapping.modelId = upstreamModel.publicModelId
  if (mapping.id === undefined) {
    if (upstreamModel?.officialPrice) {
      assignOfficialPrice(mapping, upstreamModel.officialPrice)
    } else {
      mapping.inputPrice = 0
      mapping.outputPrice = 0
      mapping.cachedInputPrice = null
      mapping.cacheWritePrice = null
    }
    mapping.adjustmentMultiplier = 1
  }
}

function formatOfficialPrice(model: UpstreamModel): string {
  const price = model.officialPrice
  if (!price) return '未收录'
  const cachedInput = fromMicros(price.cachedInputPriceMicros)
  const cacheWrite = fromMicros(price.cacheWritePriceMicros)
  return `输入 $${fromMicros(price.inputPriceMicros)?.toFixed(4)} · 输出 $${fromMicros(price.outputPriceMicros)?.toFixed(4)} · 缓存读 ${cachedInput === null ? '未提供' : `$${cachedInput.toFixed(4)}`} · 缓存写 ${cacheWrite === null ? '未提供' : `$${cacheWrite.toFixed(4)}`}`
}

function officialPriceForMapping(mapping: MappingDraft) {
  return discoveredModels.value.find((model) => model.id === mapping.upstreamModel)?.officialPrice ?? null
}

function assignOfficialPrice(mapping: MappingDraft, official: NonNullable<UpstreamModel['officialPrice']>) {
  mapping.inputPrice = fromMicros(official.inputPriceMicros) ?? 0
  mapping.outputPrice = fromMicros(official.outputPriceMicros) ?? 0
  mapping.cachedInputPrice = fromMicros(official.cachedInputPriceMicros)
  mapping.cacheWritePrice = fromMicros(official.cacheWritePriceMicros)
}

function roundedPrice(value: number): number {
  return Math.round(value * 1_000_000) / 1_000_000
}

function applyMappingPriceMultiplier(mapping: MappingDraft) {
  const multiplier = mapping.adjustmentMultiplier
  if (!Number.isFinite(multiplier) || multiplier < 0) {
    ElMessage.error('请输入有效的价格倍率')
    return
  }
  mapping.inputPrice = roundedPrice(mapping.inputPrice * multiplier)
  mapping.outputPrice = roundedPrice(mapping.outputPrice * multiplier)
  mapping.cachedInputPrice = mapping.cachedInputPrice === null ? null : roundedPrice(mapping.cachedInputPrice * multiplier)
  mapping.cacheWritePrice = mapping.cacheWritePrice === null ? null : roundedPrice(mapping.cacheWritePrice * multiplier)
  ElMessage.success(`已按当前价格的 ${multiplier} 倍调整`)
}

function restoreMappingOfficialPrice(mapping: MappingDraft) {
  const official = officialPriceForMapping(mapping)
  if (!official) {
    ElMessage.warning('该模型未收录官方默认价格')
    return
  }
  assignOfficialPrice(mapping, official)
  mapping.adjustmentMultiplier = 1
  ElMessage.success('已恢复官方默认价格')
}

function applyOfficialPriceMultiplier() {
  const multiplier = priceMultiplier.value
  if (!Number.isFinite(multiplier) || multiplier < 0) {
    ElMessage.error('请输入有效的价格倍率')
    return
  }
  let updated = 0
  for (const mapping of mappings.value) {
    const official = discoveredModels.value.find((model) => model.id === mapping.upstreamModel)?.officialPrice
    if (!official) continue
    mapping.inputPrice = Math.round(official.inputPriceMicros * multiplier) / 1_000_000
    mapping.outputPrice = Math.round(official.outputPriceMicros * multiplier) / 1_000_000
    mapping.cachedInputPrice = official.cachedInputPriceMicros === null ? null : Math.round(official.cachedInputPriceMicros * multiplier) / 1_000_000
    mapping.cacheWritePrice = official.cacheWritePriceMicros === null ? null : Math.round(official.cacheWritePriceMicros * multiplier) / 1_000_000
    mapping.adjustmentMultiplier = multiplier
    updated += 1
  }
  if (updated === 0) {
    ElMessage.warning('当前映射没有可匹配的官方价格')
    return
  }
  ElMessage.success(`已按官方基准价重算 ${updated} 条映射`)
}

function restoreAllOfficialPrices() {
  let updated = 0
  for (const mapping of mappings.value) {
    const official = officialPriceForMapping(mapping)
    if (!official) continue
    assignOfficialPrice(mapping, official)
    mapping.adjustmentMultiplier = 1
    updated += 1
  }
  priceMultiplier.value = 1
  if (updated === 0) {
    ElMessage.warning('当前映射没有可恢复的官方默认价格')
    return
  }
  ElMessage.success(`已恢复 ${updated} 条映射的官方默认价格`)
}

function formatDiscoveryTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

async function discoverChannelModels(showSuccess = false): Promise<boolean> {
  if (!form.baseUrl.trim() || (!editingId.value && !form.apiKey.trim())) {
    discoveryError.value = 'Base URL 和 API key 不完整'
    return false
  }
  const requestVersion = ++discoveryRequestVersion
  discoveringModels.value = true
  discoveryError.value = ''
  const payload: ChannelModelDiscoveryRequest = {
    channelId: editingId.value ?? 0,
    baseUrl: form.baseUrl.trim(),
    apiKey: form.apiKey.trim(),
  }
  try {
    const result = await request<ChannelModelDiscovery>('/admin/gateway/channels/discover-models', {
      method: 'POST',
      body: JSON.stringify(payload),
    })
    if (requestVersion !== discoveryRequestVersion) return false
    const refreshedModels = await request<GatewayModel[]>('/admin/gateway/models')
    if (requestVersion !== discoveryRequestVersion) return false
    discoverySummary.value = result
    discoveredModels.value = result.models
    models.value = refreshedModels
    mergeDiscoveredMappings(result.models, editingId.value === null)
    if (showSuccess) {
      const createdCount = result.models.filter((model) => model.publicModelCreated).length
      ElMessage.success(createdCount > 0 ? `已获取 ${result.models.length} 个模型，自动新增 ${createdCount} 个公共模型` : `已获取 ${result.models.length} 个模型，公共模型均已存在`)
    }
    return true
  } catch (error) {
    if (requestVersion !== discoveryRequestVersion) return false
    discoverySummary.value = null
    discoveredModels.value = []
    discoveryError.value = error instanceof Error ? error.message : '上游模型获取失败'
    return false
  } finally {
    if (requestVersion === discoveryRequestVersion) discoveringModels.value = false
  }
}

async function quickSetup() {
  if (!form.baseUrl.trim() || (!editingId.value && !form.apiKey.trim())) {
    quickError.value = '请填写 Base URL 和 API key'
    return
  }
  if (!Number.isFinite(priceMultiplier.value) || priceMultiplier.value < 0 || priceMultiplier.value > 100) {
    quickError.value = '倍率必须在 0 到 100 之间'
    return
  }
  form.name = inferredChannelName()
  quickProcessing.value = true
  quickError.value = ''
  quickStep.value = 1
  const discovered = await discoverChannelModels(false)
  if (!discovered) {
    quickProcessing.value = false
    quickError.value = discoveryError.value || '模型获取失败，请检查连接配置'
    return
  }
  quickStep.value = 2
  const configuredUpstreams = new Set(mappings.value.map((mapping) => mapping.upstreamModel.trim()))
  mergeDiscoveredMappings(discoveredModels.value, true)
  for (const mapping of mappings.value) {
    if (!configuredUpstreams.has(mapping.upstreamModel.trim()) && mapping.modelId) mapping.enabled = true
  }
  applyOfficialPriceMultiplier()
  quickStep.value = 3
  const enabledCount = mappings.value.filter((mapping) => mapping.enabled && !configuredUpstreams.has(mapping.upstreamModel.trim())).length
  const saved = await saveChannel(false)
  if (saved) {
    quickStep.value = 4
    quickDialogOpen.value = false
    ElMessage.success(`操作成功，新增启用模型 ${enabledCount} 个`)
  } else {
    quickError.value = '保存失败，请检查配置后重试'
  }
  quickProcessing.value = false
}

function modelName(modelId: number): string {
  return models.value.find((model) => model.id === modelId)?.name ?? `#${modelId}`
}

function sortedMappings(enabled: boolean): MappingDraft[] {
  return mappings.value
    .filter((mapping) => mapping.enabled === enabled)
    .sort((left, right) => Number(Boolean(officialPriceForMapping(right))) - Number(Boolean(officialPriceForMapping(left)))
      || right.recentAttemptCount - left.recentAttemptCount
      || compareModelNamesDescending(modelName(left.modelId ?? 0), modelName(right.modelId ?? 0))
      || compareModelNamesDescending(left.upstreamModel, right.upstreamModel))
}

function filteredMappings(enabled: boolean): MappingDraft[] {
  const query = mappingSearch.value.trim().toLowerCase()
  if (!query) return sortedMappings(enabled)
  return sortedMappings(enabled).filter((mapping) => (
    mapping.upstreamModel.toLowerCase().includes(query)
    || modelName(mapping.modelId ?? 0).toLowerCase().includes(query)
  ))
}

function sortedChannelModels(channel: Channel): ChannelModel[] {
  return [...channel.models].sort((left, right) => right.recentAttemptCount - left.recentAttemptCount
    || compareModelNamesDescending(modelName(left.modelId), modelName(right.modelId)))
}

function visibleChannelModels(channel: Channel): ChannelModel[] {
  return sortedChannelModels(channel).slice(0, maxVisibleChannelModels)
}

function hiddenChannelModels(channel: Channel): ChannelModel[] {
  return sortedChannelModels(channel).slice(maxVisibleChannelModels)
}

function modelTagStyle(modelId: number): CSSProperties {
  const name = modelName(modelId)
  const hue = modelHueByName.value.get(name) ?? hashModelName(name) % 360
  return {
    '--el-tag-bg-color': `hsl(${hue} 70% 96%)`,
    '--el-tag-border-color': `hsl(${hue} 48% 76%)`,
    '--el-tag-text-color': `hsl(${hue} 48% 30%)`,
  }
}

function removeMapping(mapping: MappingDraft) {
  const index = mappings.value.indexOf(mapping)
  if (index >= 0) mappings.value.splice(index, 1)
}

function updateMappingEnabled(mapping: MappingDraft, enabled: boolean) {
  mapping.enabled = enabled
  if (enabled) mapping.circuitDisabled = false
}

function formatPercent(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function formatPriceMultiplier(value: number): string {
  const multiplier = Number.isFinite(value) ? value / 10_000 : 1
  return `${new Intl.NumberFormat('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(multiplier)}x`
}

function formatTiming(value: number, samples: number): string {
  return samples > 0 ? formatDuration(value) : '--'
}

function timingSampleLabel(channel: Channel): string {
  return `样本 首 Token ${formatCompactNumber(channel.metrics.firstTokenSampleCount)} · 延迟 ${formatCompactNumber(channel.metrics.latencySampleCount)} · 耗时 ${formatCompactNumber(channel.metrics.durationSampleCount)}`
}

function isCircuitOpen(channel: Channel): boolean {
  return channel.circuitOpenUntil !== null && Date.parse(channel.circuitOpenUntil) > currentTime.value
}

function hasCircuitMark(channel: Channel): boolean {
  return channel.circuitLevel > 0
}

function channelState(channel: Channel): { label: string; type: 'success' | 'warning' | 'danger' | 'info' } {
  if (channel.circuitLevel >= 3) return { label: '三级熔断', type: 'danger' }
  if (isCircuitOpen(channel)) return { label: `${channel.circuitLevel === 2 ? '二' : '一'}级熔断`, type: 'danger' }
  if (channel.circuitLevel > 0) return { label: `${channel.circuitLevel === 2 ? '二' : '一'}级恢复中`, type: 'warning' }
  if (!channel.enabled) return { label: '已停用', type: 'info' }
  if (channel.consecutiveFailures > 0) return { label: `${channel.consecutiveFailures} 次失败`, type: 'warning' }
  return { label: '可调度', type: 'success' }
}

function circuitRemaining(channel: Channel): string {
  if (channel.circuitLevel >= 3) return '需人工恢复'
  if (isCircuitOpen(channel) && channel.circuitOpenUntil) {
    const seconds = Math.max(0, Math.ceil((Date.parse(channel.circuitOpenUntil) - currentTime.value) / 1000))
    return `${seconds} 秒后自动探测`
  }
  if (channel.circuitLevel > 0) return `需 ${channel.circuitLevel} 次成功探测完全恢复`
  return ''
}

function channelRowClassName({ row }: { row: Channel }): string {
  return hasCircuitMark(row) ? 'channel-row--circuit-open' : ''
}

async function loadData() {
  loading.value = true
  errorMessage.value = ''
  try {
    const [channelItems, modelItems, settings] = await Promise.all([
      request<Channel[]>('/admin/gateway/channels'),
      request<GatewayModel[]>('/admin/gateway/models'),
      request<ApplicationSettings>('/settings'),
    ])
    channels.value = channelItems
    models.value = modelItems
    if (settings.gatewayConfig.commonModelNames?.length) commonModelNames.value = settings.gatewayConfig.commonModelNames
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '渠道数据加载失败'
  } finally {
    loading.value = false
  }
}

async function saveChannel(showSuccess = true): Promise<boolean> {
  if (!form.name.trim() || !form.baseUrl.trim() || (!editingId.value && !form.apiKey.trim())) {
    ElMessage.error('请完整填写渠道名称、Base URL 和 API key')
    return false
  }
  if (mappings.value.some((item) => !item.modelId || !item.upstreamModel.trim())) {
    ElMessage.error('模型映射需要选择公开模型和上游模型')
    return false
  }
  if (mappings.value.some((item) => !Number.isFinite(item.adjustmentMultiplier) || item.adjustmentMultiplier < 0 || item.adjustmentMultiplier > 100)) {
    ElMessage.error('价格倍率必须在 0 到 100 之间')
    return false
  }
  if (!Number.isFinite(priceMultiplier.value) || priceMultiplier.value < 0 || priceMultiplier.value > 100) {
    ElMessage.error('渠道官方价倍率必须在 0 到 100 之间')
    return false
  }
  saving.value = true
  try {
    await request<Channel>('/admin/gateway/channels/configuration', {
      method: 'POST',
      body: JSON.stringify({
        id: editingId.value ?? 0,
        channel: {
          ...form,
          priceMultiplierBasisPoints: Math.round(priceMultiplier.value * 10_000),
        },
        models: mappings.value.map((item) => ({
          modelId: item.modelId,
          upstreamModel: item.upstreamModel,
          priority: item.priority,
          weight: item.weight,
          inputPriceMicros: Math.round(item.inputPrice * 1_000_000),
          outputPriceMicros: Math.round(item.outputPrice * 1_000_000),
          cachedInputPriceMicros: item.cachedInputPrice === null ? null : Math.round(item.cachedInputPrice * 1_000_000),
          cacheWritePriceMicros: item.cacheWritePrice === null ? null : Math.round(item.cacheWritePrice * 1_000_000),
          priceMultiplierBasisPoints: Math.round(item.adjustmentMultiplier * 10_000),
          enabled: item.enabled,
        })),
      }),
    })
    drawerOpen.value = false
    if (showSuccess) ElMessage.success('渠道配置已保存')
    await loadData()
    return true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '渠道保存失败')
    return false
  } finally {
    saving.value = false
  }
}

async function testChannel(channel: Channel) {
  testingChannelId.value = channel.id
  try {
    const result = await request<{ latencyMs: number; status: number }>(`/admin/gateway/channels/${channel.id}/test`, { method: 'POST' })
    ElMessage.success(`连接成功，HTTP ${result.status}，${formatDuration(result.latencyMs)}`)
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '连接测试失败')
    await loadData()
  } finally {
    testingChannelId.value = null
  }
}

async function resetChannelCircuit(channel: Channel) {
  try {
    await ElMessageBox.confirm(
      `人工恢复渠道“${channel.name}”？恢复后该渠道会立即重新参与请求调度。`,
      '恢复三级熔断渠道',
      { type: 'warning', confirmButtonText: '恢复并启用', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  resettingCircuitChannelId.value = channel.id
  try {
    await request<null>(`/admin/gateway/channels/${channel.id}/reset-circuit`, { method: 'POST' })
    ElMessage.success('渠道已恢复并重新启用')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '解除熔断失败')
  } finally {
    resettingCircuitChannelId.value = null
  }
}

async function deleteChannel(channel: Channel) {
  await ElMessageBox.confirm(`删除渠道“${channel.name}”及其全部模型映射？`, '删除渠道', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  deletingChannelId.value = channel.id
  try {
    await request<null>(`/admin/gateway/channels/${channel.id}`, { method: 'DELETE' })
    ElMessage.success('渠道已删除')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '渠道删除失败')
  } finally {
    deletingChannelId.value = null
  }
}

onMounted(() => {
  void loadData()
  clockTimer = setInterval(() => {
    currentTime.value = Date.now()
  }, 1000)
})

onUnmounted(() => {
  if (clockTimer) clearInterval(clockTimer)
})
</script>

<template>
  <div class="page-stack channel-page">
    <header class="page-heading">
      <div><h1>渠道管理</h1><p>维护上游连接、模型映射与每百万 Token 价格</p></div>
      <div class="page-actions">
        <el-tooltip content="刷新渠道列表" placement="bottom">
          <el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新渠道列表" @click="loadData" />
        </el-tooltip>
        <el-button type="primary" :icon="Plus" @click="resetForm()">新增渠道</el-button>
      </div>
    </header>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>渠道加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadData">重试</el-button></div>
    <section v-else class="surface-panel table-panel">
      <header class="channel-list-toolbar">
        <div><strong>渠道列表</strong><span>{{ filteredChannels.length }} / {{ channels.length }} 个渠道</span></div>
        <el-input v-model="channelSearchQuery" class="channel-search" clearable :prefix-icon="Search" aria-label="按渠道名称或 Base URL 筛选" placeholder="筛选名称或 Base URL" />
      </header>
      <el-table v-loading="loading" :data="filteredChannels" row-key="id" height="var(--channel-table-height)" :empty-text="channelTableEmptyText" :row-class-name="channelRowClassName">
        <el-table-column label="渠道" width="228" fixed="left">
          <template #default="scope"><div class="primary-cell"><strong>{{ scope.row.name }}</strong><small>{{ scope.row.baseUrl }}</small></div></template>
        </el-table-column>
        <el-table-column label="状态" width="116">
          <template #default="scope">
            <div class="channel-state-cell" :class="{ 'is-circuit-open': hasCircuitMark(scope.row) }">
              <div class="channel-state-heading">
                <el-tag :type="channelState(scope.row).type" effect="plain">{{ channelState(scope.row).label }}</el-tag>
                <strong v-if="hasCircuitMark(scope.row)">{{ circuitRemaining(scope.row) }}</strong>
              </div>
              <el-tooltip v-if="hasCircuitMark(scope.row) && scope.row.lastError" :content="scope.row.lastError" placement="top" :show-after="250">
                <small tabindex="0">{{ scope.row.lastError }}</small>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="价格倍率" width="88" align="right">
          <template #default="scope"><span class="price-multiplier">{{ formatPriceMultiplier(scope.row.priceMultiplierBasisPoints) }}</span></template>
        </el-table-column>
        <el-table-column label="近 30 分钟成功率" width="154" align="right">
          <template #default="scope">
            <div class="metric-copy success-metric">
              <strong>{{ formatPercent(scope.row.metrics.recentSuccessRate) }}</strong>
              <small v-if="scope.row.metrics.recentAttemptCount">{{ formatCompactNumber(scope.row.metrics.recentSuccessCount) }} / {{ formatCompactNumber(scope.row.metrics.recentAttemptCount) }} 次尝试</small>
              <small v-else>暂无调用，按 100%</small>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="模型" min-width="280">
          <template #default="scope">
            <div v-if="scope.row.models.length" class="channel-model-tags">
              <el-tooltip v-for="item in visibleChannelModels(scope.row)" :key="item.id" :content="modelName(item.modelId)" placement="top" :show-after="300">
                <el-tag class="channel-model-tag" effect="plain" :style="modelTagStyle(item.modelId)" tabindex="0">
                  <span class="channel-model-tag-label">{{ modelName(item.modelId) }}</span>
                </el-tag>
              </el-tooltip>
              <el-tooltip v-if="hiddenChannelModels(scope.row).length" placement="top" :show-after="200" popper-class="channel-model-overflow-popper">
                <template #content>
                  <div class="channel-model-overflow-content">
                    <el-tag v-for="item in hiddenChannelModels(scope.row)" :key="item.id" effect="plain" :style="modelTagStyle(item.modelId)" size="small">{{ modelName(item.modelId) }}</el-tag>
                  </div>
                </template>
                <el-tag
                  class="channel-model-more-tag"
                  effect="plain"
                  type="info"
                  tabindex="0"
                  :aria-label="`还有 ${hiddenChannelModels(scope.row).length} 个模型，悬停查看`"
                >+{{ hiddenChannelModels(scope.row).length }}</el-tag>
              </el-tooltip>
            </div>
            <span v-else class="muted-text">未映射</span>
          </template>
        </el-table-column>
        <el-table-column label="近 5 天性能" min-width="390">
          <template #default="scope">
            <div v-if="scope.row.metrics.latencySeries.length" class="latency-metric-cell">
              <ChannelLatencySparkline :points="scope.row.metrics.latencySeries" :channel-name="scope.row.name" />
              <div class="metric-copy">
                <strong>首 Token {{ formatTiming(scope.row.metrics.averageFirstTokenMs, scope.row.metrics.firstTokenSampleCount) }} · 延迟 {{ formatTiming(scope.row.metrics.averageLatencyMs, scope.row.metrics.latencySampleCount) }}</strong>
                <small>请求耗时 {{ formatTiming(scope.row.metrics.averageDurationMs, scope.row.metrics.durationSampleCount) }}</small>
                <small>{{ timingSampleLabel(scope.row) }}</small>
                <small v-if="scope.row.latencyEwmaMs > 0">EWMA {{ formatDuration(scope.row.latencyEwmaMs) }}</small>
              </div>
            </div>
            <span v-else class="muted-text">近 5 天无成功采样</span>
          </template>
        </el-table-column>
        <el-table-column label="缓存读取" min-width="170">
          <template #default="scope">
            <div v-if="scope.row.metrics.inputTokens > 0" class="metric-copy cache-metric">
              <strong>{{ formatPercent(scope.row.metrics.cacheHitRate) }}</strong>
              <div class="cache-meter" role="meter" aria-label="缓存读取占比" aria-valuemin="0" aria-valuemax="1" :aria-valuenow="Math.min(Math.max(scope.row.metrics.cacheHitRate, 0), 1)">
                <span :style="{ width: `${Math.min(Math.max(scope.row.metrics.cacheHitRate, 0), 1) * 100}%` }" />
              </div>
              <small>{{ formatCompactNumber(scope.row.metrics.cachedTokens) }} / {{ formatCompactNumber(scope.row.metrics.inputTokens) }} Token</small>
            </div>
            <span v-else class="muted-text">暂无 usage 数据</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="164" fixed="right" align="right">
          <template #default="scope">
            <div class="table-actions">
              <el-tooltip v-if="scope.row.circuitLevel >= 3" content="人工恢复并重新启用渠道" placement="top"><el-button class="table-action-button reset-circuit-button" text type="warning" :icon="Unlock" :loading="resettingCircuitChannelId === scope.row.id" :disabled="deletingChannelId === scope.row.id || testingChannelId === scope.row.id" aria-label="人工恢复三级熔断渠道" @click="resetChannelCircuit(scope.row)" /></el-tooltip>
              <el-tooltip content="测试渠道连接" placement="top"><el-button class="table-action-button" text :icon="Connection" :loading="testingChannelId === scope.row.id" :disabled="deletingChannelId === scope.row.id" aria-label="测试渠道连接" @click="testChannel(scope.row)" /></el-tooltip>
              <el-tooltip content="编辑渠道" placement="top"><el-button class="table-action-button" text :icon="Edit" aria-label="编辑渠道" @click="resetForm(scope.row)" /></el-tooltip>
              <el-tooltip content="删除渠道" placement="top"><el-button class="table-action-button" text type="danger" :icon="Delete" :loading="deletingChannelId === scope.row.id" :disabled="testingChannelId === scope.row.id" aria-label="删除渠道" @click="deleteChannel(scope.row)" /></el-tooltip>
            </div>
          </template>
        </el-table-column>
      </el-table>
      <div v-if="!loading && channels.length === 0" class="table-empty-action"><el-button type="primary" :icon="Plus" @click="resetForm()">添加第一个渠道</el-button></div>
    </section>

    <el-dialog v-model="quickDialogOpen" :title="quickDialogTitle" width="min(520px, calc(100vw - 32px))" destroy-on-close :close-on-click-modal="false" :close-on-press-escape="!quickProcessing" :show-close="!quickProcessing">
      <div v-if="quickProcessing" class="quick-setup-progress" aria-live="polite">
        <p>正在连接上游并准备模型映射，请稍候…</p>
        <el-steps direction="vertical" :active="quickStep" finish-status="success">
          <el-step title="验证 Base URL 和 API key" />
          <el-step title="获取上游模型列表" />
          <el-step title="自动映射并按倍率计算价格" />
          <el-step title="保存渠道并刷新列表" />
        </el-steps>
      </div>
      <el-form v-else label-position="top" class="quick-setup-form" @submit.prevent="quickSetup">
        <el-alert title="快速设置只需要三项信息，模型会自动发现并映射。" type="info" :closable="false" show-icon />
        <el-form-item label="Base URL" required><el-input v-model="form.baseUrl" placeholder="https://api.openai.com/v1" /></el-form-item>
        <el-form-item :label="editingId ? '替换 API key（可留空）' : 'API key'" required><el-input v-model="form.apiKey" type="password" show-password autocomplete="new-password" :placeholder="editingId ? '留空则保持当前密钥' : '上游供应商密钥'" /></el-form-item>
        <el-form-item label="价格倍率"><el-input-number v-model="priceMultiplier" :min="0" :max="100" :precision="2" :step="0.1" controls-position="right" /><small class="field-note">将按官方目录价格乘以该倍率写入模型映射，默认 1 倍。</small></el-form-item>
        <div v-if="quickError" class="inline-error" role="alert">{{ quickError }}</div>
      </el-form>
      <template #footer>
        <div class="quick-dialog-actions">
          <el-button :disabled="quickProcessing" @click="openAdvanced">高级设置</el-button>
          <span />
          <el-button :disabled="quickProcessing" @click="quickDialogOpen = false">取消</el-button>
          <el-button v-if="!quickProcessing" type="primary" :loading="saving" @click="quickSetup">设置</el-button>
        </div>
      </template>
    </el-dialog>

    <el-drawer v-model="drawerOpen" :title="drawerTitle" size="min(820px, 100vw)" destroy-on-close>
      <el-form label-position="top" class="drawer-form">
        <div class="form-columns">
          <el-form-item label="渠道名称"><el-input v-model="form.name" placeholder="例如 OpenAI 主渠道" /></el-form-item>
          <el-form-item label="Base URL"><el-input v-model="form.baseUrl" placeholder="https://api.openai.com/v1" /></el-form-item>
        </div>
        <el-form-item :label="editingId ? '替换 API key' : 'API key'">
          <el-input v-model="form.apiKey" type="password" show-password autocomplete="new-password" :placeholder="editingId ? '留空则保持当前密钥' : '上游供应商密钥'" />
        </el-form-item>
        <div class="toggle-row"><el-checkbox v-model="form.enabled">启用渠道</el-checkbox><el-checkbox v-model="form.supportsStreamUsage">支持流式 usage 参数</el-checkbox></div>

        <div class="subsection-heading model-discovery-heading">
          <div>
            <h3>上游可用模型</h3>
            <p v-if="discoverySummary">HTTP {{ discoverySummary.status }} · {{ formatDuration(discoverySummary.latencyMs) }} · {{ formatDiscoveryTime(discoverySummary.fetchedAt) }}</p>
            <p v-else>尚未获取</p>
          </div>
          <div class="model-discovery-actions">
            <el-tag v-if="createdPublicModelCount" type="warning" effect="plain">自动新增 {{ createdPublicModelCount }} 个公共模型</el-tag>
            <el-tag v-if="discoverySummary" type="success" effect="plain">{{ discoveredModels.length }} 个模型</el-tag>
            <el-button :icon="Refresh" :loading="discoveringModels" @click="discoverChannelModels(true)">获取模型</el-button>
          </div>
        </div>
        <el-skeleton v-if="discoveringModels" :rows="3" animated />
        <div v-else-if="discoveryError" class="model-discovery-error inline-error" role="alert"><span>{{ discoveryError }}</span><el-button text @click="discoverChannelModels()">重试</el-button></div>
        <div v-else-if="discoveredModels.length === 0" class="mapping-empty">{{ discoverySummary ? '上游未返回可用模型' : '尚未获取上游模型' }}</div>
        <el-table v-else :data="sortedDiscoveredModels" row-key="id" max-height="240" class="supported-model-table" :row-class-name="supportedModelRowClassName" @row-click="filterMappingsByUpstreamModel">
          <el-table-column label="模型 ID" min-width="200">
            <template #default="scope">
              <button class="upstream-model-filter" type="button" :aria-pressed="mappingSearch.trim() === scope.row.id" :aria-label="`筛选 ${scope.row.id} 的模型映射`" @click.stop="filterMappingsByUpstreamModel(scope.row)"><code>{{ scope.row.id }}</code></button>
            </template>
          </el-table-column>
          <el-table-column label="所属方" min-width="100"><template #default="scope">{{ scope.row.ownedBy || '未提供' }}</template></el-table-column>
          <el-table-column label="官方短上下文价（USD / 百万 Token）" min-width="430"><template #default="scope"><span class="official-price" :class="{ 'muted-text': !scope.row.officialPrice }">{{ formatOfficialPrice(scope.row) }}</span></template></el-table-column>
          <el-table-column label="本站公共模型" min-width="190">
            <template #default="scope">
              <div class="public-model-cell">
                <code>{{ modelName(scope.row.publicModelId) }}</code>
                <el-tag :type="scope.row.publicModelCreated ? 'warning' : 'success'" effect="plain" size="small">{{ scope.row.publicModelCreated ? '本次自动新增' : '已存在' }}</el-tag>
              </div>
            </template>
          </el-table-column>
        </el-table>

        <div class="subsection-heading mapping-heading">
          <div><h3>模型映射与价格</h3><p>按模型名称筛选配置；有官方价格的模型优先，首次配置会自动启用常用模型</p></div>
          <el-button :icon="Plus" :disabled="discoveredModels.length === 0" @click="addMapping">添加映射</el-button>
        </div>
        <div class="mapping-toolbar" aria-label="模型映射筛选与批量价格操作">
          <el-input v-model="mappingSearch" clearable :prefix-icon="Search" placeholder="搜索本站或上游模型" aria-label="搜索模型映射" />
          <div class="mapping-bulk-actions">
            <label class="multiplier-control"><span>渠道官方价倍率</span><el-input-number v-model="priceMultiplier" :min="0" :max="100" :precision="2" :step="0.1" controls-position="right" /></label>
            <el-button :icon="RefreshRight" :disabled="discoveredModels.length === 0" @click="applyOfficialPriceMultiplier">应用倍率</el-button>
            <el-button :icon="RefreshLeft" :disabled="discoveredModels.length === 0" @click="restoreAllOfficialPrices">全部恢复默认</el-button>
          </div>
        </div>
        <div v-if="mappings.length === 0" class="mapping-empty">当前渠道未配置模型映射</div>
        <template v-else>
        <section v-for="group in mappingGroups" :key="group.key" class="mapping-group" :aria-label="`${group.label}模型映射`">
          <header class="mapping-group-heading">
            <h4>{{ group.label }}</h4>
            <span>{{ group.items.length }} 条映射</span>
          </header>
          <div v-if="group.items.length === 0" class="mapping-group-empty">{{ group.emptyText }}</div>
          <article v-for="(mapping, index) in group.items" :key="mapping.clientKey" class="mapping-editor">
          <header class="mapping-editor-header">
            <div><strong>{{ group.label }}映射 {{ index + 1 }}</strong><el-tag v-if="mapping.circuitDisabled" type="danger" effect="plain" size="small">熔断关闭</el-tag><span>{{ mapping.upstreamModel || '未选择上游模型' }}</span></div>
            <div class="mapping-editor-actions"><el-checkbox :model-value="mapping.enabled" @change="updateMappingEnabled(mapping, Boolean($event))">启用该映射</el-checkbox><el-button :icon="Delete" title="删除映射" circle @click="removeMapping(mapping)" /></div>
          </header>

          <div class="mapping-model-grid">
            <el-form-item label="上游模型">
              <el-select v-model="mapping.upstreamModel" filterable placeholder="选择供应商返回的模型" @change="selectUpstreamModel(mapping, $event)">
                <el-option v-for="model in modelOptionsForMapping(mapping)" :key="model.id" :label="model.id" :value="model.id">
                  <div class="upstream-model-option"><span>{{ model.id }}</span><small v-if="model.ownedBy">{{ model.ownedBy }}</small></div>
                </el-option>
              </el-select>
              <small class="field-note">实际发送到该供应商接口的模型 ID</small>
            </el-form-item>
            <el-form-item label="本站公开模型">
              <el-select v-model="mapping.modelId" filterable placeholder="客户端请求使用的模型名"><el-option v-for="model in sortedPublicModels" :key="model.id" :label="model.name" :value="model.id" /></el-select>
              <small class="field-note">Codex 和 OpenAI 客户端请求时使用的模型名称</small>
            </el-form-item>
          </div>

          <div class="mapping-routing-grid">
            <el-form-item label="调度优先级">
              <el-input-number v-model="mapping.priority" :min="-1000" :max="1000" controls-position="right" />
              <small class="field-note">数值越大越优先；只有最高优先级组参与选择</small>
            </el-form-item>
            <el-form-item label="同级选择权重">
              <el-input-number v-model="mapping.weight" :min="1" :max="10000" controls-position="right" />
              <small class="field-note">仅优先级相同时生效，数值越大被选中概率越高</small>
            </el-form-item>
          </div>

          <section class="mapping-price-section" aria-label="Token 计费单价">
            <div class="mapping-price-heading">
              <div class="mapping-price-title"><strong>Token 计费单价</strong><span>USD / 百万 Token</span></div>
              <div class="mapping-price-actions">
                <label class="multiplier-control"><span>当前价倍率</span><el-input-number v-model="mapping.adjustmentMultiplier" :min="0" :max="100" :precision="2" :step="0.1" controls-position="right" /></label>
                <el-button :icon="RefreshRight" @click="applyMappingPriceMultiplier(mapping)">按倍率调整</el-button>
                <el-button :icon="RefreshLeft" :disabled="!officialPriceForMapping(mapping)" @click="restoreMappingOfficialPrice(mapping)">恢复默认</el-button>
              </div>
            </div>
            <div class="mapping-price-grid">
              <el-form-item label="输入价格">
                <el-input-number v-model="mapping.inputPrice" :min="0" :precision="4" :step="0.1" controls-position="right" />
                <small class="field-note">未使用缓存读写的普通输入 Token</small>
              </el-form-item>
              <el-form-item label="输出价格">
                <el-input-number v-model="mapping.outputPrice" :min="0" :precision="4" :step="0.1" controls-position="right" />
                <small class="field-note">供应商模型生成的输出 Token</small>
              </el-form-item>
              <el-form-item label="缓存读取价格">
                <el-input-number v-model="mapping.cachedInputPrice" :min="0" :precision="4" :step="0.1" controls-position="right" placeholder="留空则同输入" />
                <small class="field-note">从供应商上下文缓存读取的输入 Token；留空沿用输入价</small>
              </el-form-item>
              <el-form-item label="缓存写入价格">
                <el-input-number v-model="mapping.cacheWritePrice" :min="0" :precision="4" :step="0.1" controls-position="right" placeholder="留空则同输入" />
                <small class="field-note">写入供应商上下文缓存的输入 Token；留空沿用输入价</small>
              </el-form-item>
            </div>
          </section>
          </article>
        </section>
        </template>
      </el-form>
      <template #footer><div class="drawer-actions"><el-button @click="openQuick">切换简要设置</el-button><span /><el-button @click="drawerOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveChannel">保存渠道</el-button></div></template>
    </el-drawer>
  </div>
</template>

<style scoped>
:deep(.el-table__body tr.channel-row--circuit-open > td.el-table__cell),
:deep(.el-table__body tr.channel-row--circuit-open:hover > td.el-table__cell) { background: var(--rose-danger-soft); }
:deep(.el-table__body tr.channel-row--circuit-open > td.el-table__cell:first-child) { box-shadow: inset 3px 0 0 var(--rose-danger); }
.channel-state-cell { display: grid; min-width: 0; gap: 3px; }
.channel-state-cell :deep(.el-tag) { max-width: 100%; }
.channel-state-heading { display: flex; align-items: center; gap: 5px; min-width: 0; }
.channel-state-heading strong { color: var(--rose-danger); font-family: var(--rose-font-mono); font-size: 11px; font-weight: 600; font-variant-numeric: tabular-nums; white-space: nowrap; }
.channel-state-cell small { display: block; max-width: 100%; overflow: hidden; color: var(--rose-text-muted); font-size: 11px; line-height: 1.3; text-overflow: ellipsis; white-space: nowrap; }
.channel-state-cell.is-circuit-open small { color: var(--rose-danger); cursor: help; }
.reset-circuit-button { color: var(--rose-danger); }
.channel-page { --channel-table-height: min(660px, max(360px, calc(100dvh - var(--rose-header-height) - 220px))); }
@media (min-width: 961px) {
  .channel-page { height: 100%; min-height: 0; grid-template-rows: auto minmax(0, 1fr); overflow: hidden; --channel-table-height: calc(100% - 59px); }
  .channel-page > .table-panel { min-height: 0; }
}
.quick-setup-form { display: grid; gap: 8px; }
.quick-setup-form :deep(.el-input-number) { width: 100%; }
.quick-setup-progress { min-height: 260px; padding: 8px 12px; }
.quick-setup-progress p { margin: 0 0 18px; color: var(--rose-text-muted); font-size: 12px; }
.quick-dialog-actions, .drawer-actions { display: flex; align-items: center; gap: 8px; }
.quick-dialog-actions > span, .drawer-actions > span { flex: 1; }
.channel-list-toolbar { display: flex; min-height: 58px; align-items: center; justify-content: space-between; gap: 16px; padding: 10px 16px; border-bottom: 1px solid var(--rose-border); background: var(--rose-surface-muted); }
.channel-list-toolbar > div { display: grid; min-width: 0; gap: 2px; }
.channel-list-toolbar strong { color: var(--rose-text); font-size: 14px; font-weight: 650; }
.channel-list-toolbar span { color: var(--rose-text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.channel-search { width: min(360px, 44vw); }
.model-discovery-heading { margin-top: 0; }
.model-discovery-actions { display: flex; align-items: center; gap: 8px; }
.model-discovery-error { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 0; }
.supported-model-table { margin-bottom: 4px; border: 1px solid var(--rose-border); }
.supported-model-table :deep(.el-table__row) { cursor: pointer; }
.supported-model-table :deep(.el-table__row.is-filtering-mappings > td.el-table__cell) { background: var(--rose-primary-soft); }
.upstream-model-filter { max-width: 100%; padding: 0; overflow: hidden; border: 0; color: var(--rose-primary-hover); background: transparent; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.upstream-model-filter:focus-visible { border-radius: 2px; outline: 2px solid var(--rose-primary); outline-offset: 2px; }
.public-model-cell { display: flex; align-items: center; justify-content: space-between; gap: 8px; min-width: 0; }
.public-model-cell code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.official-price { font-family: var(--rose-font-mono); font-size: 11px; white-space: nowrap; }
.upstream-model-option { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-width: 0; }
.upstream-model-option span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upstream-model-option small { flex-shrink: 0; color: var(--rose-text-subtle); font-size: 11px; }
.channel-model-tags { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; max-width: 100%; }
.channel-model-tag { max-width: 126px; }
.channel-model-tag :deep(.el-tag__content) { min-width: 0; }
.channel-model-tag-label { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.channel-model-more-tag { flex-shrink: 0; cursor: help; font-variant-numeric: tabular-nums; }
.channel-model-overflow-content { display: flex; flex-wrap: wrap; gap: 6px; max-width: 360px; max-height: 220px; overflow-y: auto; padding: 2px; }
.price-multiplier { color: var(--rose-text); font-family: var(--rose-font-mono); font-size: 12px; font-variant-numeric: tabular-nums; }
.latency-metric-cell { display: flex; align-items: center; gap: 8px; min-height: 48px; }
.metric-copy { display: grid; min-width: 0; gap: 2px; font-variant-numeric: tabular-nums; }
.metric-copy strong { color: var(--rose-text); font-size: 13px; font-weight: 650; }
.metric-copy small { color: var(--rose-text-muted); font-size: 11px; line-height: 1.35; white-space: nowrap; }
.success-metric { justify-items: end; }
.cache-metric { width: 132px; }
.cache-meter { width: 100%; height: 4px; overflow: hidden; border-radius: 2px; background: var(--rose-border); }
.cache-meter span { display: block; height: 100%; background: var(--rose-amber); }
.mapping-group { display: grid; gap: 10px; margin-top: 14px; }
.mapping-group-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-bottom: 8px; border-bottom: 1px solid var(--rose-border); }
.mapping-group-heading h4 { color: var(--rose-text); font-size: 13px; font-weight: 650; }
.mapping-group-heading span { color: var(--rose-text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.mapping-group-empty { padding: 14px; border: 1px dashed var(--rose-border-strong); color: var(--rose-text-muted); font-size: 12px; text-align: center; }
.mapping-editor { margin-bottom: 12px; padding: 14px; border: 1px solid var(--rose-border); border-radius: 6px; background: var(--rose-surface); }
.mapping-editor-header { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 12px; padding-bottom: 10px; border-bottom: 1px solid var(--rose-border); }
.mapping-editor-header > div:first-child { display: grid; min-width: 0; gap: 2px; }
.mapping-editor-header strong { color: var(--rose-text); font-size: 13px; }
.mapping-editor-header span { overflow: hidden; color: var(--rose-text-muted); font-family: var(--rose-font-mono); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
.mapping-editor-actions { display: flex; flex-shrink: 0; align-items: center; gap: 10px; }
.mapping-toolbar { display: grid; grid-template-columns: minmax(220px, 1fr) auto; align-items: center; gap: 10px 16px; margin-top: 10px; padding: 10px 0; border-block: 1px solid var(--rose-border); }
.mapping-bulk-actions, .mapping-price-actions, .multiplier-control { display: flex; align-items: center; gap: 8px; }
.mapping-bulk-actions { flex-wrap: wrap; justify-content: flex-end; }
.mapping-price-actions { flex-wrap: wrap; justify-content: flex-end; }
.multiplier-control span { color: var(--rose-text-muted); font-size: 11px; white-space: nowrap; }
.multiplier-control :deep(.el-input-number) { width: 116px; }
.mapping-model-grid, .mapping-routing-grid, .mapping-price-grid { display: grid; gap: 12px; }
.mapping-model-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.mapping-routing-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.mapping-price-section { margin-top: 2px; padding-top: 12px; border-top: 1px dashed var(--rose-border-strong); }
.mapping-price-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 10px; }
.mapping-price-title { display: grid; gap: 2px; }
.mapping-price-title strong { color: var(--rose-text); font-size: 12px; }
.mapping-price-title span { color: var(--rose-text-muted); font-size: 11px; }
.mapping-price-grid { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.mapping-editor :deep(.el-form-item) { margin-bottom: 12px; }
.mapping-editor :deep(.el-select), .mapping-editor :deep(.el-input-number) { width: 100%; }
.field-note { display: block; min-height: 30px; margin-top: 5px; color: var(--rose-text-muted); font-size: 11px; line-height: 1.35; }
.mapping-empty { padding: 24px; border: 1px dashed var(--rose-border-strong); color: var(--rose-text-muted); text-align: center; }
@media (max-width: 860px) { .mapping-toolbar { grid-template-columns: 1fr; } .mapping-bulk-actions { justify-content: flex-start; } }
@media (max-width: 720px) { .channel-page { --channel-table-height: 480px; } .channel-list-toolbar, .model-discovery-heading, .mapping-editor-header, .mapping-heading, .mapping-price-heading { align-items: flex-start; flex-direction: column; } .channel-search { width: 100%; } .model-discovery-actions, .mapping-bulk-actions, .mapping-price-actions { width: 100%; justify-content: flex-start; } .mapping-model-grid, .mapping-routing-grid, .mapping-price-grid { grid-template-columns: 1fr; } .mapping-editor-header { flex-direction: column; } .mapping-editor-actions { width: 100%; justify-content: space-between; } .field-note { min-height: 0; } }
</style>
