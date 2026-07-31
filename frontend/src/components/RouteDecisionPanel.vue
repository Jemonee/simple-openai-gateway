<script setup lang="ts">
import { computed, ref } from 'vue'
import { Right } from '@element-plus/icons-vue'
import type { RouteDecision, RouteDecisionCandidate } from '@/types/gateway'
import { formatDuration } from '@/utils/formatters'

interface RouteDecisionPanelProps {
  /** Immutable routing calculation snapshot attached to an upstream attempt. */
  decision: RouteDecision
}

const { decision } = defineProps<RouteDecisionPanelProps>()
const expanded = ref(false)
const sortedCandidates = computed(() => [...decision.candidates].sort((left, right) => right.probability - left.probability))
const isAffinityDecision = computed(() => decision.mode === 'session_affinity' || decision.mode === 'response_affinity')
const selectedCandidate = computed(() => sortedCandidates.value.find((candidate) => candidate.selected) ?? null)
const candidates = computed(() => {
  if (!isAffinityDecision.value) return sortedCandidates.value
  return sortedCandidates.value.filter((candidate) => candidate.selected)
})
const hasScoringWeights = computed(() => !isAffinityDecision.value && decision.weights && (decision.weights.price + decision.weights.efficiency + decision.weights.quality + (decision.weights.balance ?? 0) > 0))

function formatPercent(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', minimumFractionDigits: 1, maximumFractionDigits: 2 }).format(value)
}

function formatExpectation(value: number): string {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 5 }).format(value)
}

function formatScore(value: number): string {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 3 }).format(value)
}

function formatCost(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 6 }).format(micros / 1_000_000)
}

function formatLatency(candidate: RouteDecisionCandidate): string {
  return candidate.latencyMs > 0 ? formatDuration(candidate.latencyMs) : '无样本'
}

function formatFirstToken(candidate: RouteDecisionCandidate): string {
  return candidate.firstTokenMs > 0 ? formatDuration(candidate.firstTokenMs) : '无样本'
}

function formatThroughput(candidate: RouteDecisionCandidate): string {
  return candidate.tokensPerSecond > 0 ? `${candidate.tokensPerSecond.toFixed(1)} token/s` : '无样本'
}

function formatRecentShare(candidate: RouteDecisionCandidate): string {
  if (candidate.routeSampleSize <= 0) return '无样本'
  return `${formatPercent(candidate.recentRouteShare)} (${candidate.recentRouteCount}/${candidate.routeSampleSize})`
}

function formatBalance(candidate: RouteDecisionCandidate): string {
  return `${formatPercent(candidate.targetRouteShare ?? 0)} · ×${(candidate.balanceMultiplier || 1).toFixed(2)}`
}

function formatCacheHitRate(candidate: RouteDecisionCandidate): string {
  return candidate.cacheSampleCount > 0 ? formatPercent(candidate.cacheHitRate) : '无样本'
}

function formatCacheRate(candidate: RouteDecisionCandidate): string {
  return candidate.cacheTokenCount > 0 ? formatPercent(candidate.cacheRate) : '无样本'
}

function strategyLabel(): string {
  if (decision.strategy === 'lowest_cost') return '成本优先'
  if (decision.strategy === 'lowest_latency') return '效率优先'
  return '优先级加权'
}

function modeLabel(): string {
  if (decision.mode === 'session_affinity') return '会话固定，沿用当前渠道'
  if (decision.mode === 'response_affinity') return '响应关联固定，沿用当前渠道'
  return '期望度归一化后随机抽取'
}
</script>

<template>
  <section class="route-decision" :class="{ affinity: isAffinityDecision }" aria-label="路由决策参数">
    <header
      class="decision-header"
      role="button"
      tabindex="0"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
      @keydown.enter.prevent="expanded = !expanded"
      @keydown.space.prevent="expanded = !expanded"
    >
      <div class="decision-title">
        <el-icon class="decision-expand-icon" :class="{ 'is-expanded': expanded }"><Right /></el-icon>
        <div><h4>路由策略</h4><p v-show="expanded">{{ strategyLabel() }} · {{ modeLabel() }}</p></div>
      </div>
      <div class="decision-summary">
        <strong v-if="selectedCandidate"><span>命中模型</span><code>{{ selectedCandidate.upstreamModel }}</code></strong>
        <span v-if="selectedCandidate">{{ selectedCandidate.channelName }} · {{ isAffinityDecision ? '沿用' : '已命中' }}</span>
        <span v-else>未记录命中模型</span>
        <span v-if="expanded && hasScoringWeights">价格 {{ formatPercent(decision.weights.price) }} · 效率 {{ formatPercent(decision.weights.efficiency) }} · 质量 {{ formatPercent(decision.weights.quality) }} · 均衡 {{ formatPercent(decision.weights.balance ?? 0) }}</span>
        <span v-if="expanded">{{ candidates.length }} 个候选渠道</span>
      </div>
    </header>
    <div v-show="expanded" class="decision-table-wrap">
      <table>
        <colgroup>
          <col class="channel-column">
          <col class="priority-column">
          <col class="price-column">
          <col class="efficiency-column">
          <col class="quality-column">
          <col class="share-column">
          <col class="expectation-column">
          <col class="probability-column">
        </colgroup>
        <thead><tr><th>渠道 / 上游模型</th><th>优先级 × 权重</th><th>价格 / 得分</th><th>效率 / 得分</th><th>质量</th><th>实际 / 目标占比</th><th>期望度</th><th>命中概率</th></tr></thead>
        <tbody>
          <tr v-for="candidate in candidates" :key="candidate.channelModelId" :class="{ selected: candidate.selected }">
            <td><span class="channel-name"><i aria-hidden="true" />{{ candidate.channelName }}<em v-if="candidate.selected">{{ isAffinityDecision ? '沿用' : '已命中' }}</em></span><code>{{ candidate.upstreamModel }}</code></td>
            <td><strong>{{ candidate.priority }}</strong><span>× {{ candidate.weight }}</span></td>
            <td><span>{{ formatCost(candidate.expectedCostMicros) }}</span><small>价格分 {{ formatScore(candidate.priceScore) }}</small></td>
            <td class="efficiency-cell"><span>首 token {{ formatFirstToken(candidate) }}</span><span>响应 {{ formatLatency(candidate) }}</span><span>{{ formatThroughput(candidate) }}</span><small>效率分 {{ formatScore(candidate.efficiencyScore) }}</small></td>
            <td><span>成功 {{ formatPercent(candidate.successRate) }}</span><small>质量分 {{ formatScore(candidate.qualityScore) }} · 缓存命中 {{ formatCacheHitRate(candidate) }} · 缓存率 {{ formatCacheRate(candidate) }}</small></td>
            <td><span>{{ formatRecentShare(candidate) }}</span><small>目标 / 修正 {{ formatBalance(candidate) }}</small></td>
            <td><code>{{ formatExpectation(candidate.expectation) }}</code></td>
            <td><strong class="probability">{{ formatPercent(candidate.probability) }}</strong></td>
          </tr>
        </tbody>
      </table>
    </div>
    <p v-show="expanded" class="decision-note">效率分使用近 30 分钟成功样本；均衡按当前模型候选的实际占比相对基础目标占比纠偏，窗口随候选数从 100 条扩展到最多 1000 条。历史快照不会随后续配置变化。</p>
  </section>
</template>

<style scoped>
.route-decision { min-width: 0; border: 1px solid var(--rose-border); background: var(--rose-surface); }
.decision-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding: 12px 14px; cursor: pointer; }
.decision-header:hover, .decision-header:focus-visible { background: var(--rose-surface-muted); outline: none; }
.decision-header[aria-expanded="true"] { border-bottom: 1px solid var(--rose-border); }
.decision-header:focus-visible { box-shadow: inset 0 0 0 2px var(--rose-primary); }
.decision-title { display: flex; align-items: flex-start; gap: 9px; min-width: 0; }
.decision-expand-icon { flex: 0 0 auto; margin-top: 2px; color: var(--rose-text-subtle); transition: transform 150ms ease; }
.decision-expand-icon.is-expanded { transform: rotate(90deg); }
.route-decision h4 { color: var(--rose-text); font-size: 13px; }
.route-decision header p, .decision-note { margin-top: 3px; color: var(--rose-text-muted); font-size: 10px; }
.decision-summary { display: grid; justify-items: end; gap: 4px; color: var(--rose-text-muted); font-size: 10px; }
.decision-summary strong { display: flex; align-items: baseline; gap: 7px; color: var(--rose-text); font-size: 11px; }
.decision-summary strong span { color: var(--rose-text-subtle); font-size: 9px; font-weight: 500; }
.decision-summary code { color: var(--rose-primary-hover); overflow-wrap: anywhere; }
.decision-table-wrap { overflow-x: auto; }
table { width: 100%; min-width: 1180px; table-layout: fixed; border-collapse: collapse; font-size: 11px; font-variant-numeric: tabular-nums; }
.channel-column { width: 190px; }
.priority-column { width: 104px; }
.price-column { width: 122px; }
.efficiency-column { width: 210px; }
.quality-column { width: 248px; }
.share-column { width: 130px; }
.expectation-column { width: 86px; }
.probability-column { width: 90px; }
th { padding: 8px 10px; color: var(--rose-text-subtle); background: var(--rose-surface-muted); font-size: 9px; font-weight: 650; text-align: left; white-space: nowrap; }
td { padding: 9px 10px; border-top: 1px solid var(--rose-border); color: var(--rose-text-muted); white-space: nowrap; }
tbody tr:first-child td { border-top: 0; }
tbody tr.selected { background: var(--rose-primary-soft); }
.affinity tbody tr.selected { background: var(--rose-success-soft); }
td:first-child { display: grid; gap: 3px; min-width: 180px; white-space: normal; }
.channel-name { display: flex; align-items: center; gap: 6px; color: var(--rose-text); font-weight: 650; }
.channel-name i { width: 6px; height: 6px; border-radius: 50%; background: var(--rose-border-strong); }
.selected .channel-name i { background: var(--rose-primary); }
.affinity .selected .channel-name i { background: var(--rose-success); }
.channel-name em { padding: 1px 5px; border: 1px solid var(--rose-primary); color: var(--rose-primary-hover); font-size: 8px; font-style: normal; font-weight: 600; }
.affinity .channel-name em { border-color: var(--rose-success); color: var(--rose-success); }
td:nth-child(2) { display: table-cell; }
td:nth-child(2) strong { margin-right: 3px; color: var(--rose-text); }
td code { color: var(--rose-text-muted); overflow-wrap: anywhere; }
td > small { display: block; margin-top: 4px; color: var(--rose-text-subtle); font-size: 9px; }
.efficiency-cell { min-width: 190px; }
.efficiency-cell > span { display: inline-block; margin: 0 8px 3px 0; }
.probability { color: var(--rose-primary-hover); font-size: 12px; }
.affinity .selected .probability { color: var(--rose-success); }
.decision-note { margin: 0; padding: 9px 14px; border-top: 1px solid var(--rose-border); line-height: 1.5; }
@media (max-width: 560px) {
  .decision-header { flex-direction: column; gap: 8px; }
  .decision-summary { justify-items: start; }
}
</style>
