<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ChatTranscript from '@/components/ChatTranscript.vue'
import JsonCodeViewer from '@/components/JsonCodeViewer.vue'
import RelayStepTimeline from '@/components/RelayStepTimeline.vue'
import type { PayloadLogDetail, RelayAttemptLog, RelayRequestLog } from '@/types/gateway'
import { conversation, deltaDescription, requestConversation } from '@/utils/conversation'

interface RequestPayloadDialogProps {
  /** Request whose retained payloads should be inspected. */
  request: RelayRequestLog | null
}

type DetailMode = 'chat' | 'source'

const { request } = defineProps<RequestPayloadDialogProps>()

function defaultActiveTab(): string {
  const attempts = request?.attempts ?? []
  const finalAttempt = attempts[attempts.length - 1]
  return finalAttempt ? `attempt-${finalAttempt.id}` : 'original'
}

const open = defineModel<boolean>({ required: true })
const activeTab = ref(defaultActiveTab())
const detailMode = ref<DetailMode>('chat')
const detailModeOptions: Array<{ label: string; value: DetailMode }> = [
  { label: '聊天', value: 'chat' },
  { label: '源码', value: 'source' },
]
const title = computed(() => request ? `调用详情 · ${request.id}` : '调用详情')
const originalMessages = computed(() => requestConversation(request?.requestBody ?? ''))
const requestPayloadLogDetail = computed<PayloadLogDetail>(() => request?.payloadLogDetail || 'default')

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
          <span><strong>接口路径</strong><code>{{ request.apiPath }}</code></span>
          <span><strong>模型</strong><code>{{ request.requestedModel }}</code></span>
          <span><strong>思考等级</strong><code>{{ request.reasoningEffort || '默认' }}</code></span>
          <span><strong>结果</strong>{{ outcomeLabel(request.outcome) }}</span>
          <span><strong>HTTP</strong>{{ request.statusCode }}</span>
          <span><strong>尝试</strong>{{ request.attemptCount }}</span>
          <span><strong>记录细节</strong><el-tag :type="payloadLogDetailType(request.payloadLogDetail)" effect="plain" size="small">{{ payloadLogDetailLabel(request.payloadLogDetail) }}</el-tag></span>
        </div>
        <el-segmented v-if="requestPayloadLogDetail !== 'none'" v-model="detailMode" :options="detailModeOptions" size="small" aria-label="详情展示方式" />
      </div>
      <el-alert v-if="retentionNotice(request.payloadLogDetail)" :title="retentionNotice(request.payloadLogDetail)" :type="request.payloadLogDetail === 'summary' ? 'warning' : 'info'" :closable="false" show-icon />

      <el-tabs v-model="activeTab" class="payload-tabs">
        <el-tab-pane label="阶段耗时" name="timings" lazy>
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
            <span><strong>接口路径</strong><code>{{ attempt.apiPath || request.apiPath }}</code></span>
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
.payload-tabs { display: flex; flex: 1; flex-direction: column; min-width: 0; min-height: 0; }
.payload-tabs :deep(.el-tabs__header) { flex: none; }
.payload-tabs :deep(.el-tabs__content) { flex: 1; min-height: 0; overflow-y: auto; padding-right: 6px; scrollbar-gutter: stable; }
.payload-tabs :deep(.el-alert) { margin-bottom: 12px; }
.payload-dialog > .el-alert { margin-bottom: 12px; }
.payload-section + .payload-section { margin-top: 20px; padding-top: 18px; border-top: 1px solid var(--hongfen-border); }
.payload-section h3 { margin: 0 0 10px; color: var(--hongfen-text); font-size: 13px; }
.payload-empty { padding: 40px 12px; color: var(--hongfen-text-muted); text-align: center; }
@media (max-width: 640px) {
  :global(.request-payload-modal.el-dialog) { max-height: calc(100dvh - 24px); margin: 12px auto; }
  .payload-toolbar { align-items: stretch; flex-direction: column; }
  .payload-toolbar .el-segmented { align-self: flex-end; }
}
</style>
