<script setup lang="ts">
import { Link } from '@element-plus/icons-vue'

interface ResourceFallbackPreviewProps {
  /** Explanation shown when the browser cannot render the resource inline. */
  message: string
  /** Bounded resource reference shown inside the fallback panel. */
  reference: string
  /** Optional external URL that can be opened in a separate browser tab. */
  href?: string
}

defineProps<ResourceFallbackPreviewProps>()
</script>

<template>
  <div class="resource-fallback-preview">
    <div class="resource-fallback-icon" aria-hidden="true"><el-icon><Link /></el-icon></div>
    <div class="resource-fallback-content">
      <strong>{{ message }}</strong>
      <el-tooltip :content="reference" placement="top-start" :show-after="250">
        <span class="resource-fallback-reference">{{ reference }}</span>
      </el-tooltip>
    </div>
    <el-button v-if="href" class="resource-fallback-action" text type="primary" :icon="Link" tag="a" :href="href" target="_blank" rel="noopener noreferrer">新窗口打开</el-button>
  </div>
</template>

<style scoped>
.resource-fallback-preview { display: flex; width: 100%; min-width: 0; align-items: center; gap: 12px; padding: 14px 16px; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); }
.resource-fallback-icon { display: grid; width: 34px; height: 34px; flex: 0 0 auto; place-items: center; border: 1px solid var(--hongfen-border); border-radius: 4px; background: var(--hongfen-primary-selection); color: var(--hongfen-primary-hover); }
.resource-fallback-content { display: grid; min-width: 0; flex: 1; gap: 4px; }
.resource-fallback-content strong { color: var(--hongfen-text); font-size: 13px; font-weight: 600; }
.resource-fallback-reference { display: block; max-width: 100%; overflow: hidden; color: var(--hongfen-text-muted); font: 12px/1.4 var(--hongfen-font-mono); text-overflow: ellipsis; white-space: nowrap; }
.resource-fallback-content > .el-tooltip { min-width: 0; max-width: 100%; }
.resource-fallback-action { flex: 0 0 auto; }
@media (max-width: 640px) { .resource-fallback-preview { align-items: flex-start; flex-wrap: wrap; } .resource-fallback-action { margin-left: 46px; } }
</style>
