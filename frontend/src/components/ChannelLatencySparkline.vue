<script setup lang="ts">
import { computed } from 'vue'
import type { ChannelLatencyPoint } from '@/types/gateway'
import { formatDuration } from '@/utils/formatters'

interface ChannelLatencySparklineProps {
  /** Chronological successful-attempt latency samples rendered by the chart. */
  points: ChannelLatencyPoint[]
  /** Accessible channel label used to describe the latency curve. */
  channelName: string
}

const { points, channelName } = defineProps<ChannelLatencySparklineProps>()

const width = 144
const height = 40
const padding = 4

const latencyValues = computed(() => points.map((point) => point.latencyMs))
const minimumLatency = computed(() => Math.min(...latencyValues.value))
const maximumLatency = computed(() => Math.max(...latencyValues.value))
const polylinePoints = computed(() => {
  const range = maximumLatency.value - minimumLatency.value
  return points.map((point, index) => {
    const x = points.length === 1
      ? width / 2
      : padding + index * ((width - padding * 2) / (points.length - 1))
    const y = range === 0
      ? height / 2
      : padding + ((maximumLatency.value - point.latencyMs) / range) * (height - padding * 2)
    return `${x.toFixed(2)},${y.toFixed(2)}`
  }).join(' ')
})
const latestPoint = computed(() => {
  const renderedPoints = polylinePoints.value.split(' ')
  return renderedPoints[renderedPoints.length - 1]?.split(',') ?? []
})
const chartLabel = computed(() => `${channelName} 最近延迟曲线，最低 ${formatDuration(minimumLatency.value)}，最高 ${formatDuration(maximumLatency.value)}，最新 ${formatDuration(points[points.length - 1]?.latencyMs ?? 0)}`)
</script>

<template>
  <svg
    class="latency-sparkline"
    :viewBox="`0 0 ${width} ${height}`"
    role="img"
    :aria-label="chartLabel"
  >
    <line :x1="padding" :x2="width - padding" :y1="height / 2" :y2="height / 2" class="sparkline-guide" />
    <polyline :points="polylinePoints" class="sparkline-line" />
    <circle
      v-if="latestPoint.length === 2"
      :cx="latestPoint[0]"
      :cy="latestPoint[1]"
      r="2.5"
      class="sparkline-latest"
    />
  </svg>
</template>

<style scoped>
.latency-sparkline { display: block; width: 144px; height: 40px; overflow: visible; }
.sparkline-guide { stroke: var(--rose-border); stroke-width: 1; }
.sparkline-line { fill: none; stroke: var(--rose-teal); stroke-linecap: round; stroke-linejoin: round; stroke-width: 2; vector-effect: non-scaling-stroke; }
.sparkline-latest { fill: var(--rose-surface); stroke: var(--rose-teal); stroke-width: 2; vector-effect: non-scaling-stroke; }
</style>
