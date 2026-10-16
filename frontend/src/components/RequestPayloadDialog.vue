<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ChatTranscript from '@/components/ChatTranscript.vue'
import JsonCodeViewer from '@/components/JsonCodeViewer.vue'
import RelayStepTimeline from '@/components/RelayStepTimeline.vue'
import ResourceList from '@/components/ResourceList.vue'
import { useAdminAuth } from '@/composables/useAdminAuth'
import type { PayloadLogDetail, RelayAttemptLog, RelayRequestLog } from '@/types/gateway'
import { conversation, deltaDescription, requestConversation } from '@/utils/conversation'
import { formatCompactNumber, formatDuration, formatUSD } from '@/utils/formatters'

interface RequestPayloadDialogProps {
  /** Request whose retained payloads should be inspected. */
  request: RelayRequestLog | null
}

type DetailMode = 'chat' | 'source'

const { request } = defineProps<RequestPayloadDialogProps>()

function defaultActiveTab(): string {
  const attempts = request?.attempts ?? []
  const failedAttempt = [...attempts].reverse().find((attempt) => attempt.outcome === 'failed' || attempt.statusCode >= 400 || attempt.statusCode === 0)
  const finalAttempt = attempts[attempts.length - 1]
  const target = failedAttempt ?? finalAttempt
  return target ? `attempt-${target.id}` : 'original'
}

const open = defineModel<boolean>({ required: true })
const { user } = useAdminAuth()
const isAccessTokenLogin = computed(() => user.value?.loginType === 'access_token')
const activeTab = ref(defaultActiveTab())
const detailMode = ref<DetailMode>('chat')
const detailModeOptions: Array<{ label: string; value: DetailMode }> = [
  { label: '聊天', value: 'chat' },
  { label: '源码', value: 'source' },
]
const title = computed(() => request ? `调用详情 · ${request.id}` : '调用详情')
const originalMessages = computed(() => requestConversation(request?.requestBody ?? ''))
const requestPayloadLogDetail = computed<PayloadLogDetail>(() => request?.payloadLogDetail || 'default')
const finalAttempt = computed(() => request?.attempts.at(-1) ?? null)
const diagnosticTone = computed<'success' | 'warning' | 'danger' | 'info'>(() => {
  if (!request) return 'info'
  if (request.outcome === 'success') return 'success'
  if (request.outcome === 'canceled' || request.outcome === 'processing') return 'warning'
  return 'danger'
})
const diagnosticTitle = computed(() => {
  if (!request) return '正在读取调用详情'
  if (request.outcome === 'success') return `调用完成 · ${request.attemptCount} 次上游尝试`
  if (request.outcome === 'canceled') return '调用已由客户端取消'
  if (request.outcome === 'processing') return '调用仍在处理中'
  return `调用失败 · ${request.attemptCount} 次上游尝试后未完成`
})

function attemptMessages(attempt: RelayAttemptLog) {
  return conversation(request?.requestBody ?? '', attempt.responseBody)
}

function outcomeLabel(outcome: RelayRequestLog['outcome'] | RelayAttemptLog['outcome']): string {
  if (outcome === 'success') return '成功'
  if (outcome === 'canceled') return '客户端取消'
  if (outcome === 'processing') return '处理中'
  return '失败'
}

function payloadLogDetailLabel(value: PayloadLogDetail | undefined): string {
  if (value === 'summary') return '摘要'
  if (value === 'none') return '无'
  return '默认'
}

function payloadLogDetailType(value: PayloadLogDetail | undefined): 'success' | 'warning' | 'info' {
  if (value === 'summary') return 'warning'
  if (value === 'none') return 'info'
  return 'success'
}

function retentionNotice(value: PayloadLogDetail | undefined): string {
  if (value === 'summary') return '本次调用按摘要档保存：保留结构、短文本预览和大数组首尾项，单段上限 64 KiB。'
  if (value === 'none') return '本次调用按无档保存：请求参数和响应正文未写入日志。'
  return ''
}

function emptyPayloadText(value: PayloadLogDetail | undefined, fallback: string): string {
  return value === 'none' ? '本次调用未保存请求参数和响应正文' : fallback
}

watch(
  () => [open.value, request?.id],
  ([isOpen]) => {
    if (!isOpen) return
    activeTab.value = defaultActiveTab()
    detailMode.value = requestPayloadLogDetail.value === 'default' ? 'chat' : 'source'
  },
)
</script>

<template>
  <el-dialog
    v-model="open"
    :title="title"
    class="request-payload-modal"
    width="min(1120px, 96vw)"
    append-to-body
    destroy-on-close
    draggable
  >
    <div v-if="request" class="payload-dialog">
      <div class="payload-toolbar">
        <div class="payload-meta">
          <span><strong>端点</strong>{{ request.endpoint === 'chat' ? 'Chat Completions' : 'Responses' }}</span>
          <span v-if="!isAccessTokenLogin"><strong>接口路径</strong><code>{{ request.apiPath }}</code></span>
          <span><strong>模型</strong><code>{{ request.requestedModel }}</code></span>
          <span><strong>思考等级</strong><code>{{ request.reasoningEffort || '默认' }}</code></span>
          <span><strong>结果</strong>{{ outcomeLabel(request.outcome) }}</span>
          <el-tag v-if="!isAccessTokenLogin && request.sensitiveResourceLeak" type="danger" effect="plain" size="small">敏感资源泄露</el-tag>
          <span><strong>HTTP</strong>{{ request.statusCode }}</span>
          <span><strong>尝试</strong>{{ request.attemptCount }}</span>
          <span><strong>记录细节</strong><el-tag :type="payloadLogDetailType(request.payloadLogDetail)" effect="plain" size="small">{{ payloadLogDetailLabel(request.payloadLogDetail) }}</el-tag></span>
        </div>
        <el-segmented v-if="requestPayloadLogDetail !== 'none'" v-model="detailMode" :options="detailModeOptions" size="small" aria-label="详情展示方式" />
      </div>
      <section class="request-diagnostic" :class="`is-${diagnosticTone}`" aria-label="调用诊断摘要">
        <div class="request-diagnostic-status"><i aria-hidden="true" /><span>最终结果</span><strong>{{ diagnosticTitle }}</strong></div>
        <div><span>最终渠道</span><strong>{{ finalAttempt?.channelName || (finalAttempt?.channelId ? `渠道 #${finalAttempt.channelId}` : '未进入上游') }}</strong><code v-if="finalAttempt?.upstreamModel">{{ finalAttempt.upstreamModel }}</code></div>
        <div><span>总耗时</span><strong>{{ request.durationMs > 0 ? formatDuration(request.durationMs) : '--' }}</strong><small>首 Token {{ request.firstTokenMs > 0 ? formatDuration(request.firstTokenMs) : '--' }}</small></div>
        <div><span>Token / 费用</span><strong>{{ formatCompactNumber(request.inputTokens + request.outputTokens) }} / {{ formatUSD(request.upstreamCostMicros) }}</strong><small>缓存读 {{ formatCompactNumber(request.cachedTokens) }}</small></div>
      </section>
      <el-alert v-if="retentionNotice(request.payloadLogDetail)" :title="retentionNotice(request.payloadLogDetail)" :type="request.payloadLogDetail === 'summary' ? 'warning' : 'info'" :closable="false" show-icon />
      <section v-if="!isAccessTokenLogin && request.resources?.length" class="resource-section"><h3>识别到的资源</h3><ResourceList :resources="request.resources" /></section>

      <el-tabs v-model="activeTab" class="payload-tabs">
        <el-tab-pane v-if="!isAccessTokenLogin" label="阶段耗时" name="timings" lazy>
          <RelayStepTimeline :steps="request.steps ?? []" default-expanded />
          <div v-if="!(request.steps?.length ?? 0)" class="payload-empty">该请求没有阶段耗时记录</div>
        </el-tab-pane>
        <el-tab-pane label="原始请求 / 增量上下文" name="original" lazy>
          <el-alert v-if="requestPayloadLogDetail === 'default' && request.requestBodyTruncated" title="原始正文超过 4 MiB，留存内容已截断" type="warning" :closable="false" show-icon />
          <el-alert v-if="deltaDescription(request.requestBody)" :title="deltaDescription(request.requestBody)" type="info" :closable="false" show-icon />
          <ChatTranscript v-if="detailMode === 'chat'" :messages="originalMessages" />
          <JsonCodeViewer v-else-if="request.requestBody" :value="request.requestBody" />
          <div v-else class="payload-empty">{{ emptyPayloadText(request.payloadLogDetail, '没有留存原始请求正文') }}</div>
        </el-tab-pane>

        <el-tab-pane v-for="(attempt, index) in request.attempts" :key="attempt.id" :label="`尝试 ${index + 1}`" :name="`attempt-${attempt.id}`" lazy>
          <div class="attempt-meta">
            <span><strong>渠道</strong>{{ attempt.channelName || `渠道 #${attempt.channelId}` }}</span>
            <span v-if="!isAccessTokenLogin"><strong>接口路径</strong><code>{{ attempt.apiPath || request.apiPath }}</code></span>
            <span><strong>上游模型</strong><code>{{ attempt.upstreamModel }}</code></span>
            <span><strong>HTTP</strong>{{ attempt.statusCode || '网络错误' }}</span>
            <span><strong>结果</strong>{{ outcomeLabel(attempt.outcome) }}</span>
            <span><strong>记录细节</strong>{{ payloadLogDetailLabel(attempt.payloadLogDetail) }}</span>
          </div>
          <template v-if="detailMode === 'chat'">
            <el-alert v-if="deltaDescription(request.requestBody)" :title="deltaDescription(request.requestBody)" type="info" :closable="false" show-icon />
            <ChatTranscript :messages="attemptMessages(attempt)" />
          </template>
          <template v-else>
            <section class="payload-section">
              <h3>发送到上游的请求</h3>
              <el-alert v-if="attempt.payloadLogDetail !== 'summary' && attempt.requestBodyTruncated" title="原始正文超过 4 MiB，留存内容已截断" type="warning" :closable="false" show-icon />
              <el-alert v-if="deltaDescription(attempt.requestBody)" :title="deltaDescription(attempt.requestBody)" type="info" :closable="false" show-icon />
              <JsonCodeViewer v-if="attempt.requestBody" :value="attempt.requestBody" />
              <div v-else class="payload-empty">{{ emptyPayloadText(attempt.payloadLogDetail, '本次尝试没有可展示的请求正文') }}</div>
            </section>
            <section class="payload-section">
              <h3>上游返回</h3>
              <el-alert v-if="attempt.payloadLogDetail !== 'summary' && attempt.responseBodyTruncated" title="正文超过 4 MiB，当前内容已截断" type="warning" :closable="false" show-icon />
              <JsonCodeViewer v-if="attempt.responseBody" :value="attempt.responseBody" />
              <div v-else class="payload-empty">{{ emptyPayloadText(attempt.payloadLogDetail, '本次尝试没有收到响应正文') }}</div>
            </section>
          </template>
        </el-tab-pane>

      </el-tabs>
    </div>
    <template #footer><el-button @click="open = false">关闭</el-button></template>
  </el-dialog>
</template>

<style scoped>
:global(.request-payload-modal.el-dialog) { display: flex; flex-direction: column; max-height: calc(100dvh - 48px); margin: 24px auto; overflow: hidden; }
:global(.request-payload-modal .el-dialog__header), :global(.request-payload-modal .el-dialog__footer) { flex: none; }
:global(.request-payload-modal .el-dialog__header) { cursor: move; user-select: none; }
:global(.request-payload-modal .el-dialog__body) { display: flex; flex: 1; min-height: 0; overflow: hidden; }
.payload-dialog { display: flex; flex: 1; flex-direction: column; min-width: 0; min-height: 0; }
.payload-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; }
.payload-meta, .attempt-meta { display: flex; flex-wrap: wrap; gap: 10px 24px; padding-bottom: 14px; color: var(--hongfen-text-muted); font-size: 12px; }
.payload-meta span, .attempt-meta span { display: flex; align-items: baseline; gap: 7px; min-width: 0; }
.payload-meta strong, .attempt-meta strong { color: var(--hongfen-text); }
.payload-meta code, .attempt-meta code { overflow-wrap: anywhere; }
.request-diagnostic { display: grid; grid-template-columns: minmax(220px, 1.35fr) repeat(3, minmax(150px, 1fr)); margin-bottom: 14px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); font-variant-numeric: tabular-nums; }
.request-diagnostic > div { display: grid; min-width: 0; gap: 4px; padding: 10px 12px; border-right: 1px solid var(--hongfen-border); }
.request-diagnostic > div:last-child { border-right: 0; }
.request-diagnostic span { color: var(--hongfen-text-subtle); font-size: 10px; }
.request-diagnostic strong { overflow: hidden; color: var(--hongfen-text); font-size: 12px; font-weight: 700; text-overflow: ellipsis; white-space: nowrap; }
.request-diagnostic code, .request-diagnostic small { overflow: hidden; color: var(--hongfen-text-muted); font: 10px/1.35 var(--hongfen-font-mono); text-overflow: ellipsis; white-space: nowrap; }
.request-diagnostic-status { grid-template-columns: auto minmax(0, 1fr); align-items: center; column-gap: 8px; }
.request-diagnostic-status i { grid-row: span 2; width: 8px; height: 8px; border-radius: 50%; background: var(--hongfen-text-subtle); }
.request-diagnostic.is-success .request-diagnostic-status i { background: var(--hongfen-success); }
.request-diagnostic.is-warning .request-diagnostic-status i { background: var(--hongfen-warning); }
.request-diagnostic.is-danger { border-color: var(--hongfen-danger-border); }
.request-diagnostic.is-danger .request-diagnostic-status i { background: var(--hongfen-danger); }
.request-diagnostic.is-danger .request-diagnostic-status strong { color: var(--hongfen-danger); }
.payload-tabs { display: flex; flex: 1; flex-direction: column; min-width: 0; min-height: 0; }
.payload-tabs :deep(.el-tabs__header) { flex: none; }
.payload-tabs :deep(.el-tabs__content) { flex: 1; min-height: 0; overflow-y: auto; padding-right: 6px; scrollbar-gutter: stable; }
.payload-tabs :deep(.el-alert) { margin-bottom: 12px; }
.payload-dialog > .el-alert { margin-bottom: 12px; }
.payload-section + .payload-section { margin-top: 20px; padding-top: 18px; border-top: 1px solid var(--hongfen-border); }
.payload-section h3 { margin: 0 0 10px; color: var(--hongfen-text); font-size: 13px; }
.payload-empty { padding: 40px 12px; color: var(--hongfen-text-muted); text-align: center; }
.resource-section { margin: 0 0 12px; padding: 10px 12px; border: 1px solid var(--hongfen-border); border-radius: 4px; background: var(--hongfen-surface-muted); }
.resource-section h3 { margin: 0 0 8px; color: var(--hongfen-text); font-size: 12px; }
@media (max-width: 640px) {
  :global(.request-payload-modal.el-dialog) { max-height: calc(100dvh - 24px); margin: 12px auto; }
  .payload-toolbar { align-items: stretch; flex-direction: column; }
  .payload-toolbar .el-segmented { align-self: flex-end; }
  .request-diagnostic { grid-template-columns: 1fr 1fr; }
  .request-diagnostic > div:nth-child(2) { border-right: 0; }
  .request-diagnostic > div:nth-child(-n + 2) { border-bottom: 1px solid var(--hongfen-border); }
}
</style>
