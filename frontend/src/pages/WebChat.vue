<script setup lang="ts">
import { computed, nextTick, onMounted, ref, useTemplateRef, watch } from 'vue'
import { ChatDotRound, CirclePlus, Connection, Delete, Grid, Promotion, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import MarkdownIt from 'markdown-it'
import { useAdminAuth } from '@/composables/useAdminAuth'
import type {
  AvailableGatewayModel,
  Channel,
  GatewayModel,
  WebChatCompletionMessage,
  WebChatCompletionRequest,
  WebChatCompletionResponse,
} from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatUSD } from '@/utils/formatters'

const markdown = new MarkdownIt({ html: false, breaks: true, linkify: true })

interface WebChatMessage extends WebChatCompletionMessage {
  /** Browser-local message identifier used for rendering. */
  id: string
  /** Browser-local creation time in RFC 3339 format. */
  createdAt: string
  /** Gateway request identifier for assistant replies. */
  requestId?: string
  /** Public model used for this assistant reply. */
  model?: string
  /** Upstream channel selected for this assistant reply. */
  channelName?: string
  /** Upstream model selected for this assistant reply. */
  upstreamModel?: string
  /** Input token count captured by the gateway. */
  inputTokens?: number
  /** Output token count captured by the gateway. */
  outputTokens?: number
  /** Estimated turn cost in micro-USD. */
  estimatedCostMicros?: number
  /** Whether this browser-only assistant entry represents a failed turn. */
  error?: boolean
}

interface WebChatConversation {
  /** Stable session identifier shared with gateway session logs. */
  id: string
  /** Browser-local conversation title. */
  title: string
  /** Public gateway model selected for subsequent turns. */
  model: string
  /** Explicit channel identifier, or zero for automatic routing. */
  channelId: number
  /** Browser-local creation time in RFC 3339 format. */
  createdAt: string
  /** Browser-local last update time in RFC 3339 format. */
  updatedAt: string
  /** Conversation history sent to Chat Completions. */
  messages: WebChatMessage[]
}

const loadingOptions = ref(true)
const { user } = useAdminAuth()
const isAccessTokenLogin = computed(() => user.value?.loginType === 'access_token')
const storageKey = computed(() => isAccessTokenLogin.value
  ? `simple-openai-gateway.web-chat.v1.token-${user.value?.tokenId ?? 0}`
  : 'simple-openai-gateway.web-chat.v1')
const optionsError = ref('')
const models = ref<GatewayModel[]>([])
const tokenModels = ref<AvailableGatewayModel[]>([])
const channels = ref<Channel[]>([])
const conversations = ref<WebChatConversation[]>([])
const activeConversationId = ref('')
const composer = ref('')
const sendingConversationIds = ref(new Set<string>())
const messageViewport = useTemplateRef<HTMLElement>('messageViewport')

const readyChannels = computed(() => channels.value.filter(channel => (
  channel.enabled && (!channel.circuitOpenUntil || Date.parse(channel.circuitOpenUntil) <= Date.now())
)))
const readyModels = computed<AvailableGatewayModel[]>(() => isAccessTokenLogin.value
  ? tokenModels.value
  : models.value.filter(model => (
      model.enabled && readyChannels.value.some(channel => channel.models.some(mapping => mapping.enabled && mapping.modelId === model.id))
    )))
const activeConversation = computed(() => conversations.value.find(item => item.id === activeConversationId.value) ?? null)
const channelsForActiveModel = computed(() => {
  const model = readyModels.value.find(item => item.name === activeConversation.value?.model)
  if (!model) return []
  return readyChannels.value.filter(channel => channel.models.some(mapping => mapping.enabled && mapping.modelId === model.id))
})
const activeChannel = computed(() => channels.value.find(channel => channel.id === activeConversation.value?.channelId) ?? null)
const sendingActiveConversation = computed(() => Boolean(activeConversation.value && sendingConversationIds.value.has(activeConversation.value.id)))
const canSend = computed(() => Boolean(
  activeConversation.value
  && activeConversation.value.model
  && composer.value.trim()
  && !sendingActiveConversation.value,
))

function newID(prefix: string): string {
  const suffix = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(16).slice(2)}`
  return `${prefix}-${suffix}`
}

function createConversation(): WebChatConversation {
  const now = new Date().toISOString()
  return {
    id: newID('web-chat'),
    title: '新对话',
    model: readyModels.value[0]?.name ?? '',
    channelId: 0,
    createdAt: now,
    updatedAt: now,
    messages: [],
  }
}

function isStoredConversation(value: unknown): value is WebChatConversation {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Partial<WebChatConversation>
  return typeof candidate.id === 'string'
    && typeof candidate.title === 'string'
    && typeof candidate.model === 'string'
    && typeof candidate.channelId === 'number'
    && Array.isArray(candidate.messages)
}

function loadStoredConversations() {
  if (typeof window === 'undefined') return
  try {
    const stored = JSON.parse(window.localStorage.getItem(storageKey.value) ?? '[]') as unknown
    if (Array.isArray(stored)) conversations.value = stored.filter(isStoredConversation)
  } catch {
    conversations.value = []
  }
  if (conversations.value.length === 0) conversations.value = [createConversation()]
  activeConversationId.value = conversations.value[0].id
}

function persistConversations() {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(storageKey.value, JSON.stringify(conversations.value))
}

function reconcileConversationRoutes() {
  const fallbackModel = readyModels.value[0]?.name ?? ''
  for (const conversation of conversations.value) {
    if (!readyModels.value.some(model => model.name === conversation.model)) conversation.model = fallbackModel
    const selectedModel = readyModels.value.find(model => model.name === conversation.model)
    const channelValid = selectedModel && readyChannels.value.some(channel => (
      channel.id === conversation.channelId
      && channel.models.some(mapping => mapping.enabled && mapping.modelId === selectedModel.id)
    ))
    if (!channelValid) conversation.channelId = 0
  }
}

async function loadOptions() {
  loadingOptions.value = true
  optionsError.value = ''
  try {
    if (isAccessTokenLogin.value) {
      tokenModels.value = await request<AvailableGatewayModel[]>('/admin/gateway/models/available')
      models.value = []
      channels.value = []
      reconcileConversationRoutes()
      return
    }
    const [modelItems, channelItems] = await Promise.all([
      request<GatewayModel[]>('/admin/gateway/models'),
      request<Channel[]>('/admin/gateway/channels'),
    ])
    models.value = modelItems
    channels.value = channelItems
    reconcileConversationRoutes()
  } catch (error) {
    optionsError.value = error instanceof Error ? error.message : '模型和渠道加载失败'
  } finally {
    loadingOptions.value = false
  }
}

function addConversation() {
  const conversation = createConversation()
  conversations.value.unshift(conversation)
  activeConversationId.value = conversation.id
  composer.value = ''
  void scrollToBottom()
}

function selectConversation(id: string) {
  activeConversationId.value = id
  composer.value = ''
  void scrollToBottom()
}

async function deleteConversation(conversation: WebChatConversation) {
  if (sendingConversationIds.value.has(conversation.id)) {
    ElMessage.warning('当前对话正在等待回复')
    return
  }
  try {
    await ElMessageBox.confirm(
      `删除“${conversation.title}”的浏览器聊天记录？网关调用日志和会话日志不会删除。`,
      '删除网页对话',
      { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  conversations.value = conversations.value.filter(item => item.id !== conversation.id)
  if (conversations.value.length === 0) conversations.value = [createConversation()]
  if (activeConversationId.value === conversation.id) activeConversationId.value = conversations.value[0].id
}

function updateConversationRoute() {
  const conversation = activeConversation.value
  if (!conversation) return
  conversation.channelId = 0
  conversation.updatedAt = new Date().toISOString()
}

function touchActiveConversation() {
  if (activeConversation.value) activeConversation.value.updatedAt = new Date().toISOString()
}

function conversationRouteLabel(conversation: WebChatConversation): string {
  if (conversation.channelId === 0) return '自动路由'
  return channels.value.find(channel => channel.id === conversation.channelId)?.name ?? '置顶渠道不可用'
}

function conversationPreview(conversation: WebChatConversation): string {
  const latest = conversation.messages[conversation.messages.length - 1]
  return latest?.content.trim().replace(/\s+/g, ' ').slice(0, 42) || '还没有消息'
}

function conversationTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(date)
}

function renderMarkdown(content: string): string {
  return markdown.render(content)
}

function titleFromMessage(content: string): string {
  const normalized = content.trim().replace(/\s+/g, ' ')
  return normalized.length > 20 ? `${normalized.slice(0, 20)}…` : normalized
}

async function scrollToBottom() {
  await nextTick()
  if (messageViewport.value) messageViewport.value.scrollTop = messageViewport.value.scrollHeight
}

async function sendMessage() {
  const conversation = activeConversation.value
  const content = composer.value.trim()
  if (!conversation || !content || sendingConversationIds.value.has(conversation.id)) return
  if (!conversation.model) {
    ElMessage.warning('请先选择可用模型')
    return
  }

  const now = new Date().toISOString()
  const userMessage: WebChatMessage = { id: newID('message'), role: 'user', content, createdAt: now }
  conversation.messages.push(userMessage)
  if (conversation.title === '新对话') conversation.title = titleFromMessage(content)
  conversation.updatedAt = now
  composer.value = ''
  sendingConversationIds.value.add(conversation.id)
  await scrollToBottom()

  const payload: WebChatCompletionRequest = {
    sessionId: conversation.id,
    model: conversation.model,
    channelId: isAccessTokenLogin.value ? 0 : conversation.channelId,
    messages: conversation.messages
      .filter(message => !message.error)
      .map(message => ({ role: message.role, content: message.content })),
  }
  try {
    const response = await request<WebChatCompletionResponse>('/admin/gateway/web-chat/completions', {
      method: 'POST',
      body: JSON.stringify(payload),
    })
    conversation.messages.push({
      id: newID('message'),
      role: 'assistant',
      content: response.content,
      createdAt: new Date().toISOString(),
      requestId: response.requestId,
      model: response.model,
      channelName: response.channelName,
      upstreamModel: response.upstreamModel,
      inputTokens: response.inputTokens,
      outputTokens: response.outputTokens,
      estimatedCostMicros: response.estimatedCostMicros,
    })
  } catch (error) {
    conversation.messages.push({
      id: newID('message'),
      role: 'assistant',
      content: error instanceof Error ? error.message : '网页对话请求失败',
      createdAt: new Date().toISOString(),
      error: true,
    })
  } finally {
    conversation.updatedAt = new Date().toISOString()
    sendingConversationIds.value.delete(conversation.id)
    if (activeConversationId.value === conversation.id) await scrollToBottom()
  }
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.key !== 'Enter' || event.shiftKey || event.isComposing) return
  event.preventDefault()
  void sendMessage()
}

watch(conversations, persistConversations, { deep: true })
watch(activeConversationId, () => { void scrollToBottom() })

onMounted(() => {
  loadStoredConversations()
  void loadOptions()
})
</script>

<template>
  <div class="web-chat-page">
    <aside class="conversation-sidebar" aria-label="网页对话列表">
      <header class="conversation-sidebar-heading">
        <div><h1>网页对话</h1><p>{{ conversations.length }} 个浏览器会话</p></div>
        <el-button type="primary" :icon="CirclePlus" @click="addConversation">新对话</el-button>
      </header>

      <div class="conversation-list">
        <button
          v-for="conversation in conversations"
          :key="conversation.id"
          type="button"
          class="conversation-item"
          :class="{ active: conversation.id === activeConversationId }"
          @click="selectConversation(conversation.id)"
        >
          <span class="conversation-item-main">
            <strong>{{ conversation.title }}</strong>
            <small>{{ conversationPreview(conversation) }}</small>
          </span>
          <span class="conversation-item-meta">
            <span>{{ conversation.model || '未选择模型' }}</span>
            <span v-if="!isAccessTokenLogin">{{ conversationRouteLabel(conversation) }}</span>
            <time>{{ conversationTime(conversation.updatedAt) }}</time>
          </span>
          <el-button
            class="conversation-delete"
            text
            :icon="Delete"
            title="删除网页对话"
            aria-label="删除网页对话"
            @click.stop="deleteConversation(conversation)"
          />
        </button>
      </div>
    </aside>

    <main class="chat-workspace">
      <div v-if="optionsError" class="chat-state state-error" role="alert">
        <strong>{{ isAccessTokenLogin ? '模型加载失败' : '模型和渠道加载失败' }}</strong><span>{{ optionsError }}</span><el-button :icon="Refresh" @click="loadOptions">重试</el-button>
      </div>

      <template v-else-if="activeConversation">
        <header class="chat-toolbar">
          <div class="chat-title-block">
            <span class="chat-title-icon"><el-icon><ChatDotRound /></el-icon></span>
            <div><h2>{{ activeConversation.title }}</h2><p>会话 ID：{{ activeConversation.id }}</p></div>
          </div>
          <div class="chat-route-controls">
            <label>
              <span><el-icon><Grid /></el-icon>模型</span>
              <el-select
                v-model="activeConversation.model"
                :loading="loadingOptions"
                filterable
                placeholder="选择模型"
                @change="updateConversationRoute"
              >
                <el-option v-for="model in readyModels" :key="model.id" :label="model.name" :value="model.name" />
              </el-select>
            </label>
            <label v-if="!isAccessTokenLogin">
              <span><el-icon><Connection /></el-icon>渠道</span>
              <el-select v-model="activeConversation.channelId" :disabled="!activeConversation.model" placeholder="自动路由" @change="touchActiveConversation">
                <el-option label="自动路由选择" :value="0" />
                <el-option v-for="channel in channelsForActiveModel" :key="channel.id" :label="`置顶 · ${channel.name}`" :value="channel.id" />
              </el-select>
            </label>
            <el-tag v-if="!isAccessTokenLogin && activeConversation.channelId" type="warning" effect="plain">置顶渠道</el-tag>
            <el-tag v-else type="success" effect="plain">自动路由</el-tag>
          </div>
        </header>

        <section ref="messageViewport" class="message-viewport" aria-live="polite">
          <div v-if="activeConversation.messages.length === 0" class="empty-conversation">
            <el-icon><ChatDotRound /></el-icon>
            <strong>开始一个网页内对话</strong>
            <p>{{ isAccessTokenLogin ? '选择授权模型后由网关自动路由。每次回复都会进入当前令牌的调用日志、会话日志和活跃会话。' : '选择模型后可交给网关自动路由，或置顶一个支持该模型的渠道。每次回复都会进入调用日志、会话日志和活跃会话。' }}</p>
          </div>

          <article
            v-for="message in activeConversation.messages"
            :key="message.id"
            class="chat-message"
            :class="[`message-${message.role}`, { 'message-error': message.error }]"
          >
            <header>
              <strong>{{ message.role === 'user' ? '你' : message.error ? '请求失败' : '助手' }}</strong>
              <time>{{ conversationTime(message.createdAt) }}</time>
            </header>
            <div v-if="message.role === 'assistant' && !message.error" class="message-content markdown-content" v-html="renderMarkdown(message.content)" />
            <div v-else class="message-content">{{ message.content }}</div>
            <footer v-if="message.requestId" class="message-telemetry">
              <span>{{ message.channelName || '未知渠道' }}</span>
              <span>{{ message.upstreamModel || message.model }}</span>
              <span>输入 {{ formatCompactNumber(message.inputTokens ?? 0) }}</span>
              <span>输出 {{ formatCompactNumber(message.outputTokens ?? 0) }}</span>
              <span>{{ formatUSD(message.estimatedCostMicros ?? 0) }}</span>
              <code>{{ message.requestId }}</code>
            </footer>
          </article>

          <article v-if="sendingActiveConversation" class="chat-message message-assistant message-pending">
            <header><strong>助手</strong><span>正在等待上游回复</span></header>
            <div class="typing-indicator" aria-label="正在生成"><i /><i /><i /></div>
          </article>
        </section>

        <footer class="composer-panel">
          <el-input
            v-model="composer"
            type="textarea"
            :autosize="{ minRows: 3, maxRows: 8 }"
            maxlength="200000"
            show-word-limit
            resize="none"
            placeholder="输入消息。Enter 发送，Shift + Enter 换行"
            aria-label="网页对话消息"
            @keydown="handleComposerKeydown"
          />
          <div class="composer-actions">
            <span>{{ activeChannel ? `将固定使用 ${activeChannel.name}` : '将由网关按当前路由策略自动选择渠道' }}</span>
            <el-button type="primary" :icon="Promotion" :loading="sendingActiveConversation" :disabled="!canSend" @click="sendMessage">发送</el-button>
          </div>
        </footer>
      </template>
    </main>
  </div>
</template>

<style scoped>
.web-chat-page { display: grid; grid-template-columns: 286px minmax(0, 1fr); height: 100%; min-height: 0; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); overflow: hidden; }
.conversation-sidebar { display: grid; grid-template-rows: auto minmax(0, 1fr); min-width: 0; border-right: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.conversation-sidebar-heading { display: grid; gap: 12px; padding: 16px; border-bottom: 1px solid var(--hongfen-border); }
.conversation-sidebar-heading > div { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.conversation-sidebar-heading h1 { margin: 0; color: var(--hongfen-text); font-size: 18px; }
.conversation-sidebar-heading p { margin: 0; color: var(--hongfen-text-muted); font-size: 11px; }
.conversation-list { min-height: 0; padding: 8px; overflow-y: auto; }
.conversation-item { position: relative; display: grid; width: 100%; gap: 7px; margin: 0 0 6px; padding: 12px 34px 12px 12px; border: 1px solid transparent; border-radius: 4px; background: transparent; color: inherit; text-align: left; cursor: pointer; }
.conversation-item:hover { border-color: var(--hongfen-border); background: var(--hongfen-surface); }
.conversation-item.active { border-color: var(--hongfen-primary); background: var(--hongfen-primary-soft); }
.conversation-item-main { display: grid; gap: 4px; min-width: 0; }
.conversation-item-main strong, .conversation-item-main small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conversation-item-main strong { color: var(--hongfen-text); font-size: 12px; }
.conversation-item-main small { color: var(--hongfen-text-muted); font-size: 11px; }
.conversation-item-meta { display: flex; flex-wrap: wrap; gap: 4px 8px; color: var(--hongfen-text-subtle); font-size: 10px; }
.conversation-delete { position: absolute; top: 6px; right: 4px; opacity: 0; }
.conversation-item:hover .conversation-delete, .conversation-item.active .conversation-delete { opacity: 1; }
.chat-workspace { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; min-width: 0; min-height: 0; }
.chat-toolbar { display: flex; align-items: center; justify-content: space-between; gap: 18px; min-height: 72px; padding: 12px 18px; border-bottom: 1px solid var(--hongfen-border); }
.chat-title-block { display: flex; align-items: center; gap: 10px; min-width: 0; }
.chat-title-icon { display: grid; width: 34px; height: 34px; place-items: center; border: 1px solid var(--hongfen-primary); border-radius: 4px; background: var(--hongfen-primary-soft); color: var(--hongfen-primary); }
.chat-title-block h2 { margin: 0; color: var(--hongfen-text); font-size: 15px; }
.chat-title-block p { max-width: 360px; margin: 4px 0 0; color: var(--hongfen-text-subtle); font-family: var(--hongfen-font-mono); font-size: 9px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.chat-route-controls { display: flex; align-items: flex-end; justify-content: flex-end; gap: 8px; }
.chat-route-controls label { display: grid; gap: 4px; }
.chat-route-controls label > span { display: flex; align-items: center; gap: 4px; color: var(--hongfen-text-muted); font-size: 10px; }
.chat-route-controls .el-select { width: 190px; }
.message-viewport { min-height: 0; padding: 22px clamp(18px, 4vw, 64px); overflow-y: auto; background: var(--hongfen-page); }
.empty-conversation { display: grid; max-width: 520px; margin: 12vh auto 0; justify-items: center; gap: 10px; color: var(--hongfen-text-muted); text-align: center; }
.empty-conversation .el-icon { font-size: 36px; color: var(--hongfen-primary); }
.empty-conversation strong { color: var(--hongfen-text); font-size: 16px; }
.empty-conversation p { margin: 0; font-size: 12px; line-height: 1.7; }
.chat-message { display: grid; max-width: 860px; gap: 7px; margin: 0 auto 18px; }
.chat-message > header { display: flex; align-items: center; gap: 10px; color: var(--hongfen-text-muted); font-size: 10px; }
.chat-message > header strong { color: var(--hongfen-text); font-size: 11px; }
.message-user { justify-items: end; }
.message-user > header { flex-direction: row-reverse; }
.message-content { max-width: min(760px, 88%); padding: 12px 14px; border: 1px solid var(--hongfen-border); border-radius: 4px; background: var(--hongfen-surface); color: var(--hongfen-text); font-size: 13px; line-height: 1.75; overflow-wrap: anywhere; white-space: pre-wrap; }
.message-user .message-content { border-color: var(--hongfen-primary); background: var(--hongfen-primary-soft); }
.message-error .message-content { border-color: color-mix(in srgb, var(--hongfen-danger) 42%, var(--hongfen-border)); background: color-mix(in srgb, var(--hongfen-surface) 92%, var(--hongfen-danger)); color: var(--hongfen-danger); }
.markdown-content { white-space: normal; }
.markdown-content :deep(p) { margin: 0 0 10px; }
.markdown-content :deep(p:last-child) { margin-bottom: 0; }
.markdown-content :deep(pre) { padding: 10px 12px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); overflow-x: auto; }
.markdown-content :deep(code) { font-family: var(--hongfen-font-mono); font-size: 12px; }
.markdown-content :deep(ul), .markdown-content :deep(ol) { padding-left: 22px; }
.markdown-content :deep(a) { color: var(--hongfen-primary); }
.message-telemetry { display: flex; flex-wrap: wrap; gap: 5px 10px; max-width: min(760px, 88%); color: var(--hongfen-text-subtle); font-size: 9px; }
.message-telemetry code { font-family: var(--hongfen-font-mono); }
.message-pending { opacity: .82; }
.typing-indicator { display: flex; gap: 4px; padding: 13px 15px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.typing-indicator i { width: 5px; height: 5px; border-radius: 50%; background: var(--hongfen-primary); animation: typing 1s infinite ease-in-out; }
.typing-indicator i:nth-child(2) { animation-delay: .14s; }
.typing-indicator i:nth-child(3) { animation-delay: .28s; }
.composer-panel { display: grid; gap: 8px; padding: 12px 18px; border-top: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.composer-panel :deep(.el-textarea__inner) { box-shadow: none; }
.composer-actions { display: flex; align-items: center; justify-content: space-between; gap: 14px; }
.composer-actions > span { color: var(--hongfen-text-muted); font-size: 10px; }
.chat-state { align-self: start; margin: 18px; }
@keyframes typing { 0%, 70%, 100% { transform: translateY(0); opacity: .45; } 35% { transform: translateY(-3px); opacity: 1; } }
@media (prefers-reduced-motion: reduce) { .typing-indicator i { animation: none; } }
@media (max-width: 960px) {
  .web-chat-page { grid-template-columns: 230px minmax(0, 1fr); }
  .chat-toolbar { align-items: flex-start; flex-direction: column; }
  .chat-route-controls { width: 100%; justify-content: flex-start; flex-wrap: wrap; }
}
@media (max-width: 680px) {
  .web-chat-page { display: flex; flex-direction: column; overflow: auto; }
  .conversation-sidebar { display: block; border-right: 0; border-bottom: 1px solid var(--hongfen-border); }
  .conversation-sidebar-heading { display: flex; align-items: center; justify-content: space-between; }
  .conversation-list { display: flex; padding: 8px; overflow-x: auto; }
  .conversation-item { min-width: 220px; margin: 0 6px 0 0; }
  .chat-workspace { min-height: 660px; }
  .chat-route-controls label { width: calc(50% - 4px); }
  .chat-route-controls .el-select { width: 100%; }
  .message-viewport { padding: 18px 14px; }
  .message-content, .message-telemetry { max-width: 94%; }
  .composer-actions { align-items: stretch; flex-direction: column; }
}
</style>
