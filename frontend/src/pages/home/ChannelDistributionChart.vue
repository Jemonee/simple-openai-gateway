<script setup lang="ts">
import { computed, ref } from 'vue'
import type { DashboardBreakdown } from '@/types/gateway'
import { formatCompactNumber } from '@/utils/formatters'

export type ChannelShareMetric = 'requests' | 'cost'

interface ChannelDistributionChartProps {
  /** Per-channel request and cost totals for the active dashboard range. */
  items: DashboardBreakdown[]
  /** Value used to calculate each channel's share. */
  metric: ChannelShareMetric
  /** Accessible description of the selected range and share metric. */
  ariaLabel: string
}

interface ChartItem {
  item: DashboardBreakdown
  value: number
  share: number
  color: string
  path: string
}

const { items, metric, ariaLabel } = defineProps<ChannelDistributionChartProps>()

const center = 130
const radius = 108
const activeIndex = ref<number | null>(null)
const sliceColors = [
  'var(--rose-primary)',
  'var(--rose-success)',
  'var(--rose-warning)',
  'var(--rose-danger)',
  'var(--rose-text-muted)',
  'var(--rose-primary-hover)',
  'var(--rose-border-strong)',
  'var(--rose-text)',
]

function metricValue(item: DashboardBreakdown): number {
  return metric === 'requests' ? item.requests : item.upstreamCostMicros
}

const total = computed(() => items.reduce((sum, item) => sum + Math.max(0, metricValue(item)), 0))
const metricLabel = computed(() => metric === 'requests' ? '请求次数' : '总费用')
const chartItems = computed<ChartItem[]>(() => {
  if (total.value <= 0) return []

  let startAngle = -90
  return items
    .map((item, originalIndex) => ({ item, originalIndex, value: Math.max(0, metricValue(item)) }))
    .filter(({ value }) => value > 0)
    .sort((left, right) => right.value - left.value || left.item.name.localeCompare(right.item.name))
    .map(({ item, originalIndex, value }) => {
      const share = value / total.value
      const endAngle = startAngle + share * 360
      const path = sectorPath(startAngle, endAngle)
      startAngle = endAngle
      return { item, value, share, path, color: sliceColors[originalIndex % sliceColors.length] }
    })
})

function polarPoint(angle: number): { x: number; y: number } {
  const radians = angle * Math.PI / 180
  return {
    x: center + radius * Math.cos(radians),
    y: center + radius * Math.sin(radians),
  }
}

function sectorPath(startAngle: number, endAngle: number): string {
  if (endAngle - startAngle >= 359.999) {
    return `M ${center} ${center - radius} A ${radius} ${radius} 0 1 1 ${center} ${center + radius} A ${radius} ${radius} 0 1 1 ${center} ${center - radius} Z`
  }
  const start = polarPoint(startAngle)
  const end = polarPoint(endAngle)
  const largeArc = endAngle - startAngle > 180 ? 1 : 0
  return `M ${center} ${center} L ${start.x} ${start.y} A ${radius} ${radius} 0 ${largeArc} 1 ${end.x} ${end.y} Z`
}

function formatShare(value: number): string {
  return new Intl.NumberFormat('zh-CN', { style: 'percent', maximumFractionDigits: 1 }).format(value)
}

function formatUSD(micros: number): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 }).format(micros / 1_000_000)
}

function formatValue(value: number): string {
  return metric === 'requests' ? formatCompactNumber(value) : formatUSD(value)
}
</script>

<template>
  <div v-if="chartItems.length" class="channel-pie-layout">
    <div class="channel-pie-figure">
      <svg viewBox="0 0 260 260" role="img" :aria-label="ariaLabel">
        <path
          v-for="(entry, index) in chartItems"
          :key="entry.item.name"
          class="channel-pie-slice"
          :class="{ 'is-active': activeIndex === index, 'is-muted': activeIndex !== null && activeIndex !== index }"
          :d="entry.path"
          :style="{ color: entry.color }"
          tabindex="0"
          :aria-label="`${entry.item.name}，${metricLabel} ${formatValue(entry.value)}，占比 ${formatShare(entry.share)}`"
          @focus="activeIndex = index"
          @blur="activeIndex = null"
          @mouseenter="activeIndex = index"
          @mouseleave="activeIndex = null"
        >
          <title>{{ entry.item.name }}：{{ formatValue(entry.value) }}，{{ formatShare(entry.share) }}</title>
        </path>
      </svg>
    </div>

    <ol class="channel-pie-legend" aria-label="渠道占比明细">
      <li
        v-for="(entry, index) in chartItems"
        :key="entry.item.name"
        :class="{ 'is-active': activeIndex === index }"
        tabindex="0"
        @focus="activeIndex = index"
        @blur="activeIndex = null"
        @mouseenter="activeIndex = index"
        @mouseleave="activeIndex = null"
      >
        <i :style="{ backgroundColor: entry.color }" aria-hidden="true"></i>
        <span>{{ entry.item.name }}</span>
        <strong>{{ formatValue(entry.value) }}</strong>
        <small>{{ formatShare(entry.share) }}</small>
      </li>
    </ol>

    <table class="channel-pie-data-table">
      <caption>{{ ariaLabel }}</caption>
      <thead><tr><th>渠道</th><th>{{ metricLabel }}</th><th>占比</th></tr></thead>
      <tbody><tr v-for="entry in chartItems" :key="entry.item.name"><th>{{ entry.item.name }}</th><td>{{ formatValue(entry.value) }}</td><td>{{ formatShare(entry.share) }}</td></tr></tbody>
    </table>
  </div>

  <div v-else class="channel-pie-empty">
    当前范围暂无可统计的渠道{{ metric === 'requests' ? '请求' : '费用' }}
  </div>
</template>

<style scoped>
.channel-pie-layout { display: grid; grid-template-columns: minmax(210px, .8fr) minmax(220px, 1.2fr); align-items: start; gap: 20px; min-height: 338px; padding: 24px; }
.channel-pie-figure { width: min(100%, 286px); margin-inline: auto; aspect-ratio: 1; }
.channel-pie-figure svg { display: block; width: 100%; height: 100%; overflow: visible; }
.channel-pie-slice { fill: currentColor; stroke: var(--rose-surface); stroke-width: 2; transform-box: fill-box; transform-origin: center; transition: opacity 140ms ease, transform 140ms ease; cursor: default; }
.channel-pie-slice.is-active { transform: scale(1.025); }
.channel-pie-slice.is-muted { opacity: .38; }
.channel-pie-slice:focus-visible { outline: none; stroke: var(--rose-text); stroke-width: 3; }
.channel-pie-legend { display: grid; align-content: start; max-height: 300px; margin: 0; padding: 4px 0; overflow-y: auto; list-style: none; scrollbar-gutter: stable; }
.channel-pie-legend li { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto auto; align-items: center; gap: 10px; min-height: 40px; padding: 7px 8px; border-bottom: 1px solid var(--rose-border); font-variant-numeric: tabular-nums; }
.channel-pie-legend li:last-child { border-bottom: 0; }
.channel-pie-legend li.is-active { background: var(--rose-surface-muted); }
.channel-pie-legend li:focus-visible { outline: 2px solid var(--rose-primary); outline-offset: -2px; }
.channel-pie-legend i { width: 10px; height: 10px; }
.channel-pie-legend span { min-width: 0; overflow: hidden; color: var(--rose-text); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.channel-pie-legend strong { color: var(--rose-text); font: 650 12px var(--rose-font-mono); white-space: nowrap; }
.channel-pie-legend small { min-width: 48px; color: var(--rose-text-muted); font: 10px var(--rose-font-mono); text-align: right; }
.channel-pie-empty { display: grid; min-height: 338px; place-items: center; padding: 24px; color: var(--rose-text-muted); font-size: 12px; }
.channel-pie-data-table { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }

@media (max-width: 620px) {
  .channel-pie-layout { grid-template-columns: 1fr; gap: 10px; min-height: 0; padding: 18px 14px; }
  .channel-pie-figure { width: min(72vw, 250px); }
  .channel-pie-legend { width: 100%; max-height: none; }
}
</style>
