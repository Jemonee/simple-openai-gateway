<script setup lang="ts">
import { computed } from 'vue'
import type { CSSProperties } from 'vue'

interface RequestTrendPoint {
  /** Stable hour or date bucket identifier. */
  key: string
  /** Compact UTC+8 label shown on the horizontal axis. */
  label: string
  /** All requests received in this bucket. */
  requests: number
  /** Successfully completed requests in this bucket. */
  successes: number
}

interface RequestTrendChartProps {
  /** Chronological request buckets rendered by the chart. */
  points: RequestTrendPoint[]
  /** Accessible description of the selected range and dimension. */
  ariaLabel: string
}

type SeriesKey = 'requests' | 'successes'

const { points, ariaLabel } = defineProps<RequestTrendChartProps>()

const chartHeight = 236
const plotTop = 18
const plotBottom = 186
const plotLeft = 46
const plotRightPadding = 20
const chartWidth = computed(() => Math.max(960, plotLeft + plotRightPadding + Math.max(1, points.length - 1) * 14))
const plotRight = computed(() => chartWidth.value - plotRightPadding)

function niceCeiling(value: number): number {
  if (value <= 1) return 1
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const normalized = value / magnitude
  const step = normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return step * magnitude
}

const yMaximum = computed(() => niceCeiling(Math.max(0, ...points.map((point) => point.requests))))
const gridTicks = computed(() => Array.from({ length: 5 }, (_, index) => {
  const ratio = index / 4
  return {
    value: Math.round(yMaximum.value * (1 - ratio)),
    y: plotTop + (plotBottom - plotTop) * ratio,
  }
}))
const labelIndexes = computed(() => {
  const interval = Math.max(1, Math.ceil(points.length / 8))
  const indexes = new Set<number>()
  points.forEach((_, index) => {
    if (index % interval === 0 || index === points.length - 1) indexes.add(index)
  })
  return indexes
})
const chartStyle = computed<CSSProperties>(() => ({ '--trend-chart-width': `${chartWidth.value}px` }))

function xPosition(index: number): number {
  if (points.length <= 1) return (plotLeft + plotRight.value) / 2
  return plotLeft + (plotRight.value - plotLeft) * index / (points.length - 1)
}

function yPosition(value: number): number {
  return plotBottom - Math.max(0, value) / yMaximum.value * (plotBottom - plotTop)
}

function seriesPoints(series: SeriesKey) {
  return points.map((point, index) => ({
    ...point,
    value: point[series],
    x: xPosition(index),
    y: yPosition(point[series]),
  }))
}

function seriesPath(series: SeriesKey): string {
  return seriesPoints(series).map((point, index) => `${index === 0 ? 'M' : 'L'} ${point.x} ${point.y}`).join(' ')
}
</script>

<template>
  <div class="trend-chart-scroller" :style="chartStyle">
    <svg
      class="trend-chart"
      :viewBox="`0 0 ${chartWidth} ${chartHeight}`"
      :width="chartWidth"
      :height="chartHeight"
      role="img"
      :aria-label="ariaLabel"
      preserveAspectRatio="xMinYMin meet"
    >
      <g class="trend-grid" aria-hidden="true">
        <g v-for="tick in gridTicks" :key="tick.y">
          <line :x1="plotLeft" :x2="plotRight" :y1="tick.y" :y2="tick.y" />
          <text x="36" :y="tick.y + 4" text-anchor="end">{{ tick.value }}</text>
        </g>
      </g>

      <g v-if="points.length" class="trend-series trend-series-requests">
        <path :d="seriesPath('requests')" />
        <circle v-for="point in seriesPoints('requests')" :key="`request-${point.key}`" :cx="point.x" :cy="point.y" r="3">
          <title>{{ point.label }}，请求 {{ point.value }}</title>
        </circle>
      </g>
      <g v-if="points.length" class="trend-series trend-series-successes">
        <path :d="seriesPath('successes')" />
        <circle v-for="point in seriesPoints('successes')" :key="`success-${point.key}`" :cx="point.x" :cy="point.y" r="3">
          <title>{{ point.label }}，成功 {{ point.value }}</title>
        </circle>
      </g>

      <g class="trend-labels" aria-hidden="true">
        <template v-for="(point, index) in points" :key="point.key">
          <line v-if="labelIndexes.has(index)" :x1="xPosition(index)" :x2="xPosition(index)" :y1="plotBottom" :y2="plotBottom + 5" />
          <text v-if="labelIndexes.has(index)" :x="xPosition(index)" y="210" text-anchor="middle">{{ point.label }}</text>
        </template>
      </g>
    </svg>

    <table class="trend-data-table">
      <caption>{{ ariaLabel }}</caption>
      <thead><tr><th>时间</th><th>请求</th><th>成功</th></tr></thead>
      <tbody><tr v-for="point in points" :key="point.key"><th>{{ point.label }}</th><td>{{ point.requests }}</td><td>{{ point.successes }}</td></tr></tbody>
    </table>
  </div>
</template>

<style scoped>
.trend-chart-scroller { width: 100%; overflow-x: auto; scrollbar-gutter: stable; }
.trend-chart { display: block; width: max(100%, var(--trend-chart-width)); height: auto; }
.trend-grid line { stroke: var(--rose-border); stroke-width: 1; vector-effect: non-scaling-stroke; }
.trend-grid text, .trend-labels text { fill: var(--rose-text-muted); font: 10px var(--rose-font-mono); }
.trend-labels line { stroke: var(--rose-border-strong); stroke-width: 1; vector-effect: non-scaling-stroke; }
.trend-series path { fill: none; stroke-width: 2; vector-effect: non-scaling-stroke; }
.trend-series circle { stroke: var(--rose-surface); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.trend-series-requests path { stroke: var(--rose-primary); }
.trend-series-requests circle { fill: var(--rose-primary); }
.trend-series-successes path { stroke: var(--rose-success); }
.trend-series-successes circle { fill: var(--rose-success); }
.trend-data-table { position: absolute; width: 1px; height: 1px; padding: 0; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }
</style>
