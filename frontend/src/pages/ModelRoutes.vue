<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ArrowDown, ArrowUp, Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { Channel, ChannelModel, GatewayModel, RoutingStrategy } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatDuration } from '@/utils/formatters'

interface CandidateRow {
  channel: Channel
  mapping: ChannelModel
}

interface CandidateState {
  label: string
  type: 'success' | 'warning' | 'info'
}

const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')
const models = ref<GatewayModel[]>([])
const channels = ref<Channel[]>([])
const dialogOpen = ref(false)
const editingId = ref<number | null>(null)
const deletingModelId = ref<number | null>(null)
const togglingMappingId = ref<number | null>(null)
const modelSearchQuery = ref('')
const showAllModels = ref(false)
const expandedStrategySections = ref<string[]>([])
const strategyDetailsOpen = computed(() => expandedStrategySections.value.includes('strategy-guide'))
const form = reactive<{ name: string; routingStrategy: RoutingStrategy; enabled: boolean }>({ name: '', routingStrategy: 'priority_weighted', enabled: true })
const dialogTitle = computed(() => editingId.value ? '编辑公开模型' : '新增公开模型')
const maxVisibleModels = 5
const sortedModels = computed(() => [...models.value].sort((left, right) => right.requestCount - left.requestCount
  || right.name.localeCompare(left.name, undefined, { numeric: true, sensitivity: 'base' })))
const filteredModels = computed(() => {
  const query = modelSearchQuery.value.trim().toLocaleLowerCase()
  if (!query) return sortedModels.value
  return sortedModels.value.filter((model) => model.name.toLocaleLowerCase().includes(query))
})
const visibleModels = computed(() => {
  if (modelSearchQuery.value.trim() || showAllModels.value) return filteredModels.value
  return filteredModels.value.slice(0, maxVisibleModels)
})
const hiddenModelCount = computed(() => Math.max(0, models.value.length - maxVisibleModels))
const modelTableEmptyText = computed(() => modelSearchQuery.value.trim() ? '未找到匹配的公开模型' : '还没有公开模型')
const strategies: Array<{ value: RoutingStrategy; label: string; note: string }> = [
  { value: 'priority_weighted', label: '优先级加权', note: '先限定最高优先级，再按价格、效率、质量与近期均衡占比抽样' },
  { value: 'lowest_cost', label: '成本优先', note: '在系统价格占比基础上放大渠道间的价格优势' },
  { value: 'lowest_latency', label: '效率优先', note: '在系统效率占比基础上放大首 token、延迟和吞吐优势' },
]

function strategyLabel(value: RoutingStrategy): string {
  return strategies.find((item) => item.value === value)?.label ?? value
}

function candidates(modelId: number): CandidateRow[] {
  const rows = channels.value.flatMap((channel) => channel.models
    .filter((mapping) => mapping.modelId === modelId)
    .map((mapping) => ({ channel, mapping })))
  return rows.sort((left, right) => {
    if (left.mapping.enabled !== right.mapping.enabled) return left.mapping.enabled ? -1 : 1
    const leftChannelAvailable = left.channel.enabled && !isCircuitOpen(left.channel)
    const rightChannelAvailable = right.channel.enabled && !isCircuitOpen(right.channel)
    if (leftChannelAvailable !== rightChannelAvailable) return leftChannelAvailable ? -1 : 1
    if (left.mapping.priority !== right.mapping.priority) return right.mapping.priority - left.mapping.priority
    return left.channel.name.localeCompare(right.channel.name, 'zh-CN')
  })
}

function isCircuitOpen(channel: Channel): boolean {
  return channel.circuitOpenUntil !== null && Date.parse(channel.circuitOpenUntil) > Date.now()
}

function channelState(channel: Channel): CandidateState {
  if (!channel.enabled) return { label: '渠道停用', type: 'info' }
  if (isCircuitOpen(channel)) return { label: '熔断中', type: 'warning' }
  return { label: '可用', type: 'success' }
}

function routableCandidateCount(modelId: number): number {
  return candidates(modelId).filter(({ channel, mapping }) => mapping.enabled && channel.enabled && !isCircuitOpen(channel)).length
}

function candidateSummary(modelId: number): string {
  const rows = candidates(modelId)
  if (rows.length === 0) return '无渠道映射'
  return `${routableCandidateCount(modelId)} 个可路由 / ${rows.length} 个映射`
}

function formatPrice(micros: number | null): string {
  if (micros === null) return '同输入价'
  return `$${(micros / 1_000_000).toFixed(4)}`
}

function formatMultiplier(basisPoints: number): string {
  return `${(Number.isFinite(basisPoints) ? basisPoints / 10_000 : 1).toFixed(2)}x`
}

function formatPercent(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function mappingPayload(mapping: ChannelModel) {
  return {
    modelId: mapping.modelId,
    upstreamModel: mapping.upstreamModel,
    priority: mapping.priority,
    weight: mapping.weight,
    inputPriceMicros: mapping.inputPriceMicros,
    outputPriceMicros: mapping.outputPriceMicros,
    cachedInputPriceMicros: mapping.cachedInputPriceMicros,
    cacheWritePriceMicros: mapping.cacheWritePriceMicros,
    priceMultiplierBasisPoints: mapping.priceMultiplierBasisPoints,
    enabled: mapping.enabled,
  }
}

async function setMappingEnabled(candidate: CandidateRow, enabled: string | number | boolean) {
  const nextEnabled = Boolean(enabled)
  if (candidate.mapping.enabled === nextEnabled || togglingMappingId.value !== null) return
  togglingMappingId.value = candidate.mapping.id
  try {
    const updatedMappings = await request<ChannelModel[]>(`/admin/gateway/channels/${candidate.channel.id}/models`, {
      method: 'PUT',
      body: JSON.stringify(candidate.channel.models.map((mapping) => ({
        ...mappingPayload(mapping),
        enabled: mapping.id === candidate.mapping.id ? nextEnabled : mapping.enabled,
      }))),
    })
    candidate.channel.models = updatedMappings
    ElMessage.success(nextEnabled ? '模型映射已启用' : '模型映射已停用')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '模型映射状态更新失败')
  } finally {
    togglingMappingId.value = null
  }
}

function openEditor(model?: GatewayModel) {
  editingId.value = model?.id ?? null
  form.name = model?.name ?? ''
  form.routingStrategy = model?.routingStrategy ?? 'priority_weighted'
  form.enabled = model?.enabled ?? true
  dialogOpen.value = true
}

async function loadData() {
  loading.value = true
  errorMessage.value = ''
  try {
    [models.value, channels.value] = await Promise.all([
      request<GatewayModel[]>('/admin/gateway/models'),
      request<Channel[]>('/admin/gateway/channels'),
    ])
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '模型路由加载失败'
  } finally {
    loading.value = false
  }
}

async function saveModel() {
  if (!form.name.trim()) {
    ElMessage.error('请输入公开模型名称')
    return
  }
  saving.value = true
  try {
    await request<GatewayModel>(editingId.value ? `/admin/gateway/models/${editingId.value}` : '/admin/gateway/models', {
      method: editingId.value ? 'PUT' : 'POST',
      body: JSON.stringify(form),
    })
    dialogOpen.value = false
    ElMessage.success('公开模型已保存')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '模型保存失败')
  } finally {
    saving.value = false
  }
}

async function deleteModel(model: GatewayModel) {
  await ElMessageBox.confirm(`删除公开模型“${model.name}”及相关渠道映射？`, '删除模型', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' })
  deletingModelId.value = model.id
  try {
    await request<null>(`/admin/gateway/models/${model.id}`, { method: 'DELETE' })
    ElMessage.success('公开模型已删除')
    await loadData()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '模型删除失败')
  } finally {
    deletingModelId.value = null
  }
}

onMounted(loadData)
</script>

<template>
  <div class="page-stack model-route-page" :class="{ 'is-strategy-open': strategyDetailsOpen }">
    <header class="page-heading">
      <div><h1>模型路由</h1><p>管理公开模型名称、候选渠道矩阵和动态调度策略</p></div>
      <div class="page-actions"><el-tooltip content="刷新模型列表" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新模型列表" @click="loadData" /></el-tooltip><el-button type="primary" :icon="Plus" @click="openEditor()">新增模型</el-button></div>
    </header>

    <section class="surface-panel strategy-model-panel" aria-labelledby="strategy-model-title">
      <el-collapse v-model="expandedStrategySections" class="strategy-collapse">
        <el-collapse-item name="strategy-guide">
          <template #title>
            <div class="strategy-model-heading">
              <div>
                <span class="section-eyebrow">ROUTING MODEL</span>
                <h2 id="strategy-model-title">三种路由策略如何选择渠道</h2>
                <p>候选池会先排除已停用、已熔断的渠道。展开查看评分、加权和抽样规则。</p>
              </div>
              <div class="probability-equation" aria-label="统一概率公式">
                <span>统一抽样模型</span>
                <code>P<sub>i</sub> = E<sub>i</sub> / &Sigma;E<sub>j</sub></code>
                <small>分数越高，被选中的概率越大</small>
              </div>
            </div>
          </template>

          <div class="strategy-model-grid">
            <article class="strategy-model-card is-priority">
          <header>
            <span class="strategy-index">01</span>
            <div><h3>优先级加权</h3><p>先锁定最高优先级，再在组内按综合期望值抽样。</p></div>
          </header>
          <div class="strategy-formula">
            <span>候选基础分</span>
            <code>B<sub>i</sub> = 1[p<sub>i</sub> = p<sub>max</sub>] &times; w<sub>i</sub> &times; (&alpha;C<sub>i</sub> + &beta;F<sub>i</sub> + &gamma;Q<sub>i</sub>)</code>
          </div>
          <ol class="strategy-steps">
            <li><strong>分组</strong><span>只保留优先级 <code>p<sub>max</sub></code> 的渠道，低优先级本轮概率为 0。</span></li>
            <li><strong>加权</strong><span>同级渠道先按价格、效率、质量和渠道权重形成基础目标占比，再按近期实际占比纠偏。</span></li>
            <li><strong>选择</strong><span>同级渠道按 <code>P<sub>i</sub></code> 随机抽样，不是固定轮询第一名。</span></li>
          </ol>
          <p class="strategy-conclusion"><strong>适合：</strong>有明确主备层级，同时希望同级渠道自动均衡。</p>
            </article>

            <article class="strategy-model-card is-cost">
          <header>
            <span class="strategy-index">02</span>
            <div><h3>最低成本</h3><p>价格越接近当前最低成本，获得的抽样加成越高。</p></div>
          </header>
          <div class="strategy-formula">
            <span>成本优势与期望值</span>
            <code>A<sub>i</sub> = (c<sub>min</sub> + 1) / (c<sub>i</sub> + 1)</code>
            <code>C<sub>i</sub> = A<sub>i</sub><sup>2</sup></code>
          </div>
          <ol class="strategy-steps">
            <li><strong>估价</strong><span><code>c<sub>i</sub></code> 按本次输入、预计输出及历史缓存率估算实际费用。</span></li>
            <li><strong>比较</strong><span>最便宜渠道的 <code>A<sub>i</sub> = 1</code>，成本越高，优势系数越低。</span></li>
            <li><strong>选择</strong><span>平方价格分放大价差，再按系统配置的价格占比进入综合期望值。</span></li>
          </ol>
          <p class="strategy-conclusion"><strong>适合：</strong>控制总体费用，但不希望低价渠道垄断或牺牲可用性。</p>
            </article>

            <article class="strategy-model-card is-latency">
          <header>
            <span class="strategy-index">03</span>
            <div><h3>效率优先</h3><p>首 token 更快、响应延迟更低、输出吞吐更高的渠道获得更高效率分。</p></div>
          </header>
          <div class="strategy-formula">
            <span>效率分</span>
            <code>F<sub>i</sub> = (0.45T<sub>first</sub> + 0.20T<sub>header</sub> + 0.35V<sub>token</sub>)<sup>2</sup></code>
          </div>
          <ol class="strategy-steps">
            <li><strong>采样</strong><span>只使用近 30 分钟成功尝试；响应延迟无样本时回退到渠道 EWMA。</span></li>
            <li><strong>归一</strong><span>首 token 和响应延迟越低越好，每秒输出 token 越高越好；无样本按中性分参与。</span></li>
            <li><strong>选择</strong><span>平方效率分放大实际差异，再按系统配置的效率占比进入综合期望值。</span></li>
          </ol>
          <p class="strategy-conclusion"><strong>适合：</strong>交互式请求、首响应敏感场景，并允许持续探测新渠道。</p>
            </article>
          </div>

          <footer class="factor-legend">
            <div class="base-equation">
              <span>三种策略共享的基础分与最终概率</span>
              <code>B<sub>i</sub> = max(w<sub>i</sub>, 1) / 100 &times; (&alpha;C<sub>i</sub> + &beta;F<sub>i</sub> + &gamma;Q<sub>i</sub>)</code>
              <code>P<sub>i</sub> = (1 - &delta;)T<sub>i</sub> + &delta;R<sub>i</sub></code>
            </div>
            <dl>
              <div><dt>&alpha; / &beta; / &gamma;</dt><dd>系统设置中的价格、效率与质量占比，保存后实时生效</dd></div>
              <div><dt>&delta;</dt><dd>均衡占比；将基础目标 <code>T</code> 与按近期实际偏差修正后的 <code>R</code> 混合</dd></div>
              <div><dt>C</dt><dd>本次缓存修正后预计费用相对最低费用的归一化价格分</dd></div>
              <div><dt>F</dt><dd>首 token 45%、响应头延迟 20%、输出吞吐 35%</dd></div>
              <div><dt>Q</dt><dd>成功率 70%、缓存命中 18%、缓存 Token 率 12%</dd></div>
              <div><dt>w</dt><dd>渠道模型映射的配置权重，最小按 1 计算</dd></div>
            </dl>
            <p>近期均衡只统计当前模型可路由候选，样本随候选数从 100 条扩展到最多 1000 条；修正乘数限制在 0.5～1.5，新渠道继续保留 20% 的有界探索流量。</p>
          </footer>
        </el-collapse-item>
      </el-collapse>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>模型路由加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadData">重试</el-button></div>
    <section v-else class="surface-panel table-panel">
      <header class="model-list-toolbar">
        <div><strong>公开模型列表</strong><span>按本站累计调用量排序 · 当前显示 {{ visibleModels.length }} / {{ models.length }} 个模型</span></div>
        <el-input v-model="modelSearchQuery" class="model-search" clearable :prefix-icon="Search" aria-label="按模型名称搜索" placeholder="搜索模型名称" />
      </header>
      <el-table v-loading="loading" class="model-list-table" :data="visibleModels" row-key="id" height="100%" scrollbar-always-on :empty-text="modelTableEmptyText">
        <el-table-column type="expand">
          <template #default="scope">
            <div class="candidate-matrix">
              <div class="matrix-heading"><strong>候选渠道</strong><span>{{ routableCandidateCount(scope.row.id) }} 个可路由 / {{ candidates(scope.row.id).length }} 个映射</span></div>
              <el-table class="candidate-table" :data="candidates(scope.row.id)" max-height="300" scrollbar-always-on empty-text="请在渠道管理中添加模型映射">
                <el-table-column label="渠道" min-width="140"><template #default="candidate">{{ candidate.row.channel.name }}</template></el-table-column>
                <el-table-column label="映射状态" width="132">
                  <template #default="candidate">
                    <div class="mapping-state-control">
                      <el-switch
                        :model-value="candidate.row.mapping.enabled"
                        inline-prompt
                        active-text="启"
                        inactive-text="停"
                        :loading="togglingMappingId === candidate.row.mapping.id"
                        :disabled="togglingMappingId !== null && togglingMappingId !== candidate.row.mapping.id"
                        :aria-label="`${candidate.row.channel.name} 的 ${candidate.row.mapping.upstreamModel} 映射`"
                        @change="setMappingEnabled(candidate.row, $event)"
                      />
                      <span>{{ candidate.row.mapping.enabled ? '已启用' : '已停用' }}</span>
                    </div>
                  </template>
                </el-table-column>
                <el-table-column label="渠道状态" width="104"><template #default="candidate"><el-tag :type="channelState(candidate.row.channel).type" effect="plain" size="small">{{ channelState(candidate.row.channel).label }}</el-tag></template></el-table-column>
                <el-table-column label="上游模型" min-width="160"><template #default="candidate"><code>{{ candidate.row.mapping.upstreamModel }}</code></template></el-table-column>
                <el-table-column label="近 30 分钟成功率" width="160" align="right">
                  <template #default="candidate">
                    <div class="success-rate-cell">
                      <strong>{{ formatPercent(candidate.row.mapping.recentSuccessRate) }}</strong>
                      <small v-if="candidate.row.mapping.recentAttemptCount">{{ formatCompactNumber(candidate.row.mapping.recentSuccessCount) }} / {{ formatCompactNumber(candidate.row.mapping.recentAttemptCount) }} 次尝试</small>
                      <small v-else>暂无调用，按 100%</small>
                    </div>
                  </template>
                </el-table-column>
                <el-table-column label="优先级 / 权重" width="132" align="right"><template #default="candidate">{{ candidate.row.mapping.priority }} / {{ candidate.row.mapping.weight }}</template></el-table-column>
                <el-table-column label="价格倍率" width="96" align="right"><template #default="candidate"><code>{{ formatMultiplier(candidate.row.mapping.priceMultiplierBasisPoints) }}</code></template></el-table-column>
                <el-table-column label="输入" width="104" align="right"><template #default="candidate">{{ formatPrice(candidate.row.mapping.inputPriceMicros) }}</template></el-table-column>
                <el-table-column label="输出" width="104" align="right"><template #default="candidate">{{ formatPrice(candidate.row.mapping.outputPriceMicros) }}</template></el-table-column>
                <el-table-column label="缓存读" width="104" align="right"><template #default="candidate">{{ formatPrice(candidate.row.mapping.cachedInputPriceMicros) }}</template></el-table-column>
                <el-table-column label="缓存写" width="104" align="right"><template #default="candidate">{{ formatPrice(candidate.row.mapping.cacheWritePriceMicros) }}</template></el-table-column>
                <el-table-column label="延迟" width="96" align="right"><template #default="candidate">{{ candidate.row.channel.latencyEwmaMs ? formatDuration(candidate.row.channel.latencyEwmaMs) : '待采样' }}</template></el-table-column>
              </el-table>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="公开模型" min-width="210"><template #default="scope"><div class="primary-cell"><strong><code>{{ scope.row.name }}</code></strong><small>{{ candidateSummary(scope.row.id) }}</small></div></template></el-table-column>
        <el-table-column label="累计调用量" width="128" align="right"><template #default="scope"><strong class="model-usage-count">{{ formatCompactNumber(scope.row.requestCount) }}</strong></template></el-table-column>
        <el-table-column label="调度策略" min-width="150"><template #default="scope">{{ strategyLabel(scope.row.routingStrategy) }}</template></el-table-column>
        <el-table-column label="状态" width="106"><template #default="scope"><el-tag :type="scope.row.enabled ? 'success' : 'info'" effect="plain">{{ scope.row.enabled ? '已公开' : '已停用' }}</el-tag></template></el-table-column>
        <el-table-column label="操作" width="92" fixed="right" align="right"><template #default="scope"><div class="table-actions"><el-tooltip content="编辑公开模型" placement="top"><el-button class="table-action-button" text :icon="Edit" :disabled="deletingModelId === scope.row.id" aria-label="编辑公开模型" @click="openEditor(scope.row)" /></el-tooltip><el-tooltip content="删除公开模型" placement="top"><el-button class="table-action-button" text type="danger" :icon="Delete" :loading="deletingModelId === scope.row.id" aria-label="删除公开模型" @click="deleteModel(scope.row)" /></el-tooltip></div></template></el-table-column>
      </el-table>
      <div v-if="!loading && !modelSearchQuery.trim() && hiddenModelCount" class="model-list-expand-bar">
        <span>{{ showAllModels ? `已显示全部 ${models.length} 个模型` : `已显示调用量前 ${visibleModels.length} 个模型` }}</span>
        <el-button text :icon="showAllModels ? ArrowUp : ArrowDown" :aria-expanded="showAllModels" @click="showAllModels = !showAllModels">
          {{ showAllModels ? '收起，仅显示调用量前五' : `显示其余 ${hiddenModelCount} 个模型` }}
        </el-button>
      </div>
      <div v-if="!loading && models.length === 0" class="table-empty-action"><el-button type="primary" :icon="Plus" @click="openEditor()">添加第一个公开模型</el-button></div>
    </section>

    <el-dialog v-model="dialogOpen" :title="dialogTitle" width="min(520px, calc(100vw - 32px))">
      <el-form label-position="top" @submit.prevent="saveModel">
        <el-form-item label="公开模型名称"><el-input v-model="form.name" placeholder="例如 gpt-4.1" /></el-form-item>
        <el-form-item label="调度策略">
          <el-select v-model="form.routingStrategy" class="full-width"><el-option v-for="strategy in strategies" :key="strategy.value" :label="strategy.label" :value="strategy.value"><div class="select-option"><strong>{{ strategy.label }}</strong><small>{{ strategy.note }}</small></div></el-option></el-select>
        </el-form-item>
        <el-checkbox v-model="form.enabled">公开并允许调用</el-checkbox>
      </el-form>
      <template #footer><div class="dialog-actions"><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveModel">保存模型</el-button></div></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.strategy-model-panel { flex: none; overflow: hidden; }
.strategy-collapse { border: 0; }
.strategy-collapse :deep(.el-collapse-item__header) { height: auto; min-height: 72px; padding: 0 18px 0 0; border: 0; background: var(--hongfen-surface-muted); line-height: normal; }
.strategy-collapse :deep(.el-collapse-item__arrow) { flex: none; margin-left: 14px; color: var(--hongfen-text-muted); font-size: 16px; }
.strategy-collapse :deep(.el-collapse-item__wrap) { border: 0; }
.strategy-collapse :deep(.el-collapse-item__content) { padding: 0; }
.strategy-model-heading { display: flex; flex: 1; align-items: center; justify-content: space-between; gap: 24px; min-width: 0; padding: 14px 0 14px 20px; }
.strategy-model-heading > div:first-child { min-width: 0; }
.section-eyebrow { display: block; margin-bottom: 5px; color: var(--hongfen-primary-hover); font: 650 10px/1 var(--hongfen-font-mono); letter-spacing: .12em; }
.strategy-model-heading h2 { color: var(--hongfen-text); font-size: 16px; font-weight: 650; }
.strategy-model-heading p { max-width: 760px; margin-top: 4px; color: var(--hongfen-text-muted); font-size: 12px; line-height: 1.55; }
.probability-equation { flex: 0 0 auto; display: grid; grid-template-columns: auto auto; align-items: center; gap: 3px 12px; min-width: 270px; padding-left: 20px; border-left: 1px solid var(--hongfen-border-strong); }
.probability-equation span { color: var(--hongfen-text-muted); font-size: 10px; }
.probability-equation code { grid-row: span 2; color: var(--hongfen-primary-hover); font-size: 15px; font-weight: 650; white-space: nowrap; }
.probability-equation small { color: var(--hongfen-text-subtle); font-size: 10px; }
.strategy-model-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); }
.strategy-model-card { min-width: 0; padding: 18px 20px 16px; }
.strategy-model-card + .strategy-model-card { border-left: 1px solid var(--hongfen-border); }
.strategy-model-card > header { display: grid; grid-template-columns: 30px minmax(0, 1fr); gap: 10px; min-height: 54px; }
.strategy-index { display: grid; width: 28px; height: 28px; place-items: center; border: 1px solid var(--hongfen-border-strong); border-radius: var(--hongfen-radius-control); color: var(--hongfen-text-muted); font: 650 10px/1 var(--hongfen-font-mono); }
.strategy-model-card h3 { color: var(--hongfen-text); font-size: 14px; font-weight: 650; }
.strategy-model-card header p { margin-top: 3px; color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.45; }
.strategy-formula { display: grid; align-content: start; gap: 6px; min-height: 88px; margin: 14px 0; padding: 11px 12px; border-left: 3px solid var(--hongfen-primary); background: var(--hongfen-surface-muted); }
.is-cost .strategy-formula { border-left-color: var(--hongfen-amber); }
.is-latency .strategy-formula { border-left-color: var(--hongfen-teal); }
.strategy-formula span { color: var(--hongfen-text-subtle); font-size: 9px; font-weight: 650; letter-spacing: .04em; }
.strategy-formula code { color: var(--hongfen-text); font-size: 11px; line-height: 1.45; overflow-wrap: anywhere; }
.strategy-steps { display: grid; gap: 9px; margin: 0; padding: 0; list-style: none; }
.strategy-steps li { display: grid; grid-template-columns: 42px minmax(0, 1fr); gap: 8px; color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.5; }
.strategy-steps strong { color: var(--hongfen-text); font-weight: 650; }
.strategy-steps code { color: var(--hongfen-primary-hover); }
.strategy-conclusion { min-height: 48px; margin: 14px 0 0; padding-top: 12px; border-top: 1px dashed var(--hongfen-border-strong); color: var(--hongfen-text-muted); font-size: 11px; line-height: 1.5; }
.strategy-conclusion strong { color: var(--hongfen-text); }
.factor-legend { padding: 14px 20px 16px; border-top: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.base-equation { display: flex; align-items: baseline; flex-wrap: wrap; gap: 7px 14px; }
.base-equation span { color: var(--hongfen-text-muted); font-size: 10px; font-weight: 650; }
.base-equation code { color: var(--hongfen-text); font-size: 11px; }
.factor-legend dl { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 0; margin: 12px 0 0; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.factor-legend dl > div { min-width: 0; padding: 9px 10px; }
.factor-legend dl > div + div { border-left: 1px solid var(--hongfen-border); }
.factor-legend dt { color: var(--hongfen-primary-hover); font: 650 11px/1 var(--hongfen-font-mono); }
.factor-legend dd { margin: 5px 0 0; color: var(--hongfen-text-muted); font-size: 10px; line-height: 1.35; }
.factor-legend > p { margin: 9px 0 0; color: var(--hongfen-text-subtle); font-size: 10px; }
.model-list-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 16px; min-height: 58px; padding: 10px 16px; border-bottom: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.model-list-toolbar > div { display: grid; gap: 2px; min-width: 0; }
.model-list-toolbar strong { color: var(--hongfen-text); font-size: 14px; font-weight: 650; }
.model-list-toolbar span { color: var(--hongfen-text-muted); font-size: 11px; font-variant-numeric: tabular-nums; }
.model-search { width: min(320px, 42vw); }
.model-usage-count { color: var(--hongfen-text); font-family: var(--hongfen-font-mono); font-variant-numeric: tabular-nums; }
.model-list-expand-bar { display: flex; min-height: 42px; align-items: center; justify-content: space-between; gap: 12px; padding: 5px 10px 5px 16px; border-top: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.model-list-expand-bar > span { color: var(--hongfen-text-subtle); font-size: 10px; font-variant-numeric: tabular-nums; }
.candidate-matrix { padding: 12px 24px 20px 54px; background: var(--hongfen-surface-muted); }
.matrix-heading { display: flex; justify-content: space-between; align-items: center; padding: 0 0 10px; color: var(--hongfen-text-muted); font-size: 12px; }
.matrix-heading strong { color: var(--hongfen-text); }
.success-rate-cell { display: grid; justify-items: end; gap: 2px; font-variant-numeric: tabular-nums; }
.success-rate-cell strong { color: var(--hongfen-text); font-size: 13px; }
.success-rate-cell small { color: var(--hongfen-text-muted); font-size: 11px; white-space: nowrap; }
.mapping-state-control { display: flex; align-items: center; gap: 7px; }
.mapping-state-control span { color: var(--hongfen-text-muted); font-size: 11px; white-space: nowrap; }
.select-option { display: grid; line-height: 1.3; }
.select-option small { color: var(--hongfen-text-muted); font-size: 11px; }
@media (min-width: 961px) {
  .model-route-page { height: 100%; min-height: 0; grid-template-rows: auto auto minmax(0, 1fr); overflow: hidden; }
  .model-route-page > .table-panel { display: flex; align-self: stretch; width: 100%; min-height: 0; flex-direction: column; }
  .model-route-page > .table-panel > .model-list-table { flex: 1; min-height: 0; }
  .model-list-expand-bar, .table-empty-action { flex: none; }
  .model-route-page.is-strategy-open { display: block; overflow-y: auto; scrollbar-gutter: stable; }
  .model-route-page.is-strategy-open > * + * { margin-top: 16px; }
  .model-route-page.is-strategy-open > .table-panel { height: 520px; }
}
@media (max-width: 980px) {
  .strategy-model-grid { grid-template-columns: 1fr; }
  .strategy-model-card + .strategy-model-card { border-top: 1px solid var(--hongfen-border); border-left: 0; }
  .strategy-model-card > header, .strategy-formula, .strategy-conclusion { min-height: 0; }
  .factor-legend dl { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .factor-legend dl > div:nth-child(4) { border-left: 0; }
  .factor-legend dl > div:nth-child(n + 4) { border-top: 1px solid var(--hongfen-border); }
}
@media (max-width: 720px) {
  .strategy-model-heading { align-items: flex-start; flex-direction: column; padding: 16px; }
  .probability-equation { width: 100%; min-width: 0; padding: 12px 0 0; border-top: 1px solid var(--hongfen-border-strong); border-left: 0; }
  .strategy-model-card { padding: 16px; }
  .factor-legend { padding: 14px 16px; }
  .factor-legend dl { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .factor-legend dl > div:nth-child(odd) { border-left: 0; }
  .factor-legend dl > div:nth-child(even) { border-left: 1px solid var(--hongfen-border); }
  .factor-legend dl > div:nth-child(n + 3) { border-top: 1px solid var(--hongfen-border); }
}
@media (max-width: 640px) {
  .model-list-toolbar { align-items: stretch; flex-direction: column; gap: 8px; }
  .model-search { width: 100%; }
  .model-list-expand-bar { align-items: flex-start; flex-direction: column; padding: 8px 10px 8px 16px; }
  .candidate-matrix { padding: 10px; }
}
@media (max-width: 460px) {
  .probability-equation { grid-template-columns: 1fr; }
  .probability-equation code { grid-row: auto; white-space: normal; }
  .factor-legend dl { grid-template-columns: 1fr; }
  .factor-legend dl > div + div { border-top: 1px solid var(--hongfen-border); border-left: 0; }
}
</style>
