<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { ArrowRight, ChatDotRound, Setting, Tools, User } from '@element-plus/icons-vue'
import MarkdownIt from 'markdown-it'
import type { ConversationMessage } from '@/utils/conversation'
import { request } from '@/utils/api'
import { formatCompactNumber } from '@/utils/formatters'

interface ChatTranscriptProps {
  /** Ordered user, system, tool, and assistant messages to render. */
  messages: ConversationMessage[]
}

interface TokenCountResponse {
  /** O200k token count for each submitted message, preserving request order. */
  counts: number[]
}

const { messages } = defineProps<ChatTranscriptProps>()
const markdown = new MarkdownIt({ html: false, breaks: true, linkify: true, typographer: false })
const transcriptId = useId()
const expandedMessageKeys = ref<Set<string>>(new Set())
const messageTokenCounts = ref<number[]>([])
let tokenCountRequest = 0
markdown.renderer.rules.link_open = (tokens, index, options, _env, self) => {
  tokens[index].attrSet('target', '_blank')
  tokens[index].attrSet('rel', 'noreferrer noopener')
  return self.renderToken(tokens, index, options)
}

const renderedMessages = computed(() => messages.map((message, index) => ({
  ...message,
  html: markdown.render(message.content),
  preview: messagePreview(message.content),
  tokenCount: messageTokenCounts.value[index],
})))

function messageKey(message: ConversationMessage, index: number): string {
  return `${message.id}-${index}`
}

function messageRegionId(index: number): string {
  return `${transcriptId}-message-${index}`
}

function messagePreview(content: string): string {
  const compact = content
    .replace(/```[\s\S]*?```/g, ' [代码] ')
    .replace(/`([^`]+)`/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}(?:#{1,6}|>|[-*+]\s|\d+[.)]\s)/gm, '')
    .replace(/[\r\n\t]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
  const characters = [...compact]
  return characters.length > 140 ? `${characters.slice(0, 140).join('')}...` : compact
}

function isMessageExpanded(message: ConversationMessage, index: number): boolean {
  return expandedMessageKeys.value.has(messageKey(message, index))
}

function toggleMessage(message: ConversationMessage, index: number) {
  const key = messageKey(message, index)
  const next = new Set(expandedMessageKeys.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedMessageKeys.value = next
}

function roleIcon(role: ConversationMessage['role']) {
  if (role === 'user') return User
  if (role === 'tool') return Tools
  if (role === 'system' || role === 'developer') return Setting
  return ChatDotRound
}

watch(() => messages, async (currentMessages) => {
  expandedMessageKeys.value = new Set()
  messageTokenCounts.value = []
  const currentRequest = ++tokenCountRequest
  if (currentMessages.length === 0) return
  try {
    const result = await request<TokenCountResponse>('/admin/gateway/token-counts', {
      method: 'POST',
      body: JSON.stringify({ texts: currentMessages.map((message) => message.content) }),
    })
    if (currentRequest === tokenCountRequest) messageTokenCounts.value = result.counts
  } catch {
    if (currentRequest === tokenCountRequest) messageTokenCounts.value = []
  }
}, { immediate: true })
</script>

<template>
  <div v-if="renderedMessages.length" class="chat-transcript">
    <article
      v-for="(message, index) in renderedMessages"
      :key="messageKey(message, index)"
      class="chat-message"
      :class="[`is-${message.role}`, { 'is-expanded': isMessageExpanded(message, index) }]"
    >
      <button
        type="button"
        class="message-summary"
        :aria-expanded="isMessageExpanded(message, index)"
        :aria-controls="messageRegionId(index)"
        @click="toggleMessage(message, index)"
      >
        <el-icon class="message-expand-icon"><ArrowRight /></el-icon>
        <el-icon class="message-role-icon"><component :is="roleIcon(message.role)" /></el-icon>
        <strong>{{ message.label }}</strong>
        <span>{{ message.preview || '无文本内容' }}</span>
        <small>{{ message.tokenCount === undefined ? '--' : formatCompactNumber(message.tokenCount) }} Token</small>
      </button>
      <div
        v-if="isMessageExpanded(message, index)"
        :id="messageRegionId(index)"
        class="markdown-body"
        role="region"
        :aria-label="`${message.label}消息内容`"
        tabindex="0"
        v-html="message.html"
      />
    </article>
  </div>
  <div v-else class="chat-empty">正文中没有可解析的对话消息，可切换到源码查看完整内容</div>
</template>

<style scoped>
.chat-transcript { display: grid; border-block: 1px solid var(--rose-border); }
.chat-message { min-width: 0; background: var(--rose-surface); }
.chat-message + .chat-message { border-top: 1px solid var(--rose-border); }
.chat-message.is-assistant { background: var(--rose-surface-muted); }
.chat-message.is-error { border-left: 3px solid var(--rose-danger); background: var(--rose-danger-soft); }
.message-summary { display: grid; grid-template-columns: 18px 20px 90px minmax(0, 1fr) auto; align-items: center; gap: 8px; width: 100%; min-width: 0; padding: 12px 14px; border: 0; background: transparent; color: var(--rose-text-muted); font: inherit; text-align: left; cursor: pointer; }
.message-summary:hover { background: color-mix(in srgb, var(--rose-surface) 92%, var(--rose-primary)); }
.message-summary:focus-visible { position: relative; z-index: 1; outline: 2px solid var(--rose-primary); outline-offset: -2px; }
.message-summary strong { overflow: hidden; color: var(--rose-text); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.message-summary > span { overflow: hidden; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.message-summary small { color: var(--rose-text-subtle); font: 10px var(--rose-font-mono); white-space: nowrap; }
.message-summary .el-icon { font-size: 15px; }
.message-expand-icon { color: var(--rose-text-subtle); transition: transform 160ms ease; }
.chat-message.is-expanded .message-expand-icon { transform: rotate(90deg); }
.message-role-icon { color: var(--rose-primary); }
.chat-message.is-error .message-summary, .chat-message.is-error .message-role-icon { color: var(--rose-danger); }
.markdown-body { min-width: 0; max-height: 360px; margin: 0 14px 16px 50px; padding: 12px 14px; overflow: auto; border-left: 2px solid var(--rose-border-strong); background: var(--rose-surface); color: var(--rose-text); font-size: 13px; line-height: 1.72; overflow-wrap: anywhere; scrollbar-gutter: stable; }
.markdown-body:focus-visible { outline: 2px solid var(--rose-primary); outline-offset: 2px; }
.markdown-body :deep(> :first-child) { margin-top: 0; }
.markdown-body :deep(> :last-child) { margin-bottom: 0; }
.markdown-body :deep(p), .markdown-body :deep(ul), .markdown-body :deep(ol), .markdown-body :deep(blockquote), .markdown-body :deep(pre) { margin: 0 0 10px; }
.markdown-body :deep(ul), .markdown-body :deep(ol) { padding-left: 22px; }
.markdown-body :deep(blockquote) { padding: 6px 12px; border-left: 3px solid var(--rose-border-strong); color: var(--rose-text-muted); }
.markdown-body :deep(code) { padding: 2px 5px; border: 1px solid var(--rose-border); background: var(--rose-surface); font-family: var(--rose-font-mono); font-size: .92em; }
.markdown-body :deep(pre) { max-width: 100%; padding: 12px 14px; overflow: auto; border: 1px solid var(--rose-border); background: var(--rose-surface-muted); color: var(--rose-text); }
.markdown-body :deep(pre code) { padding: 0; border: 0; color: inherit; background: transparent; }
.markdown-body :deep(table) { width: 100%; border-collapse: collapse; }
.markdown-body :deep(th), .markdown-body :deep(td) { padding: 7px 9px; border: 1px solid var(--rose-border); text-align: left; }
.markdown-body :deep(a) { color: var(--rose-primary-hover); }
.chat-empty { padding: 44px 16px; color: var(--rose-text-muted); text-align: center; }
@media (max-width: 640px) {
  .message-summary { grid-template-columns: 18px 20px minmax(0, 1fr) auto; padding: 11px 10px; }
  .message-summary > span { grid-column: 3 / -1; }
  .markdown-body { margin: 0 10px 14px 48px; padding: 10px 12px; }
}
</style>
