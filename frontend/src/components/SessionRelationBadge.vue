<script setup lang="ts">
import type { CodexSessionSummary } from '@/types/gateway'

interface SessionRelationBadgeProps {
  /** Session summary containing direct parent and child relationship metadata. */
  session: CodexSessionSummary
  /** Opens a parent session detail view. */
  onOpenParent?: (sessionId: string, tokenId: number) => void
}

defineProps<SessionRelationBadgeProps>()

function shortSessionID(value: string): string {
  const trimmed = value.trim()
  return trimmed.length > 16 ? `${trimmed.slice(0, 8)}…${trimmed.slice(-6)}` : trimmed
}
</script>

<template>
  <div v-if="session.parentSessionId || session.childCount > 0" class="session-relation" aria-label="会话父子关系">
    <span v-if="session.childCount > 0" class="relation-label">主会话 · {{ session.childCount }} 个子会话</span>
    <template v-if="session.parentSessionId">
      <span v-if="session.childCount > 0" class="relation-separator">·</span>
      <span class="relation-label">子会话 · 父</span>
      <button
        v-if="onOpenParent"
        class="relation-parent-link"
        type="button"
        :title="`打开父会话 ${session.parentSessionId}`"
        @click.stop="onOpenParent(session.parentSessionId, session.tokenId)"
      >{{ shortSessionID(session.parentSessionId) }}</button>
      <code v-else>{{ shortSessionID(session.parentSessionId) }}</code>
    </template>
  </div>
</template>

<style scoped>
.session-relation { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: 5px; color: var(--hongfen-text-muted); font-size: 10px; line-height: 1.3; }
.relation-label { white-space: nowrap; }
.relation-separator { color: var(--hongfen-text-subtle); }
.relation-parent-link { max-width: 150px; overflow: hidden; padding: 0; border: 0; color: var(--hongfen-primary); background: transparent; cursor: pointer; font: 600 10px/1.3 var(--hongfen-font-mono); text-decoration: underline; text-overflow: ellipsis; white-space: nowrap; }
.relation-parent-link:hover { color: var(--hongfen-primary-hover); }
code { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
