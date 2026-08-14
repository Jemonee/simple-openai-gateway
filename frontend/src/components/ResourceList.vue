<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import type { Component } from 'vue'
import { Collection, Document, DocumentCopy, Link, MagicStick, Picture, Tickets } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import type { ResourceSummary, ResourceView } from '@/types/gateway'
import { request } from '@/utils/api'
import { resourceExternalHref, resourceFallbackLabel, resourcePreviewMode, resourcePreviewSource, resourceReferenceLabel } from '@/utils/resourcePreview'
import { sourceCodePreview } from '@/utils/codeLanguage'
import ImagePreviewDialog from '@/components/ImagePreviewDialog.vue'
import ResourceFallbackPreview from '@/components/ResourceFallbackPreview.vue'

const SourceCodePreview = defineAsyncComponent(() => import('@/components/SourceCodePreview.vue'))

interface ResourceListProps {
  /** Resources detected in one request or aggregated for a session. */
  resources: Array<ResourceView | ResourceSummary>
  /** Use the tighter layout used inside a request timeline. */
  compact?: boolean
  /** Allow administrators to mark resources as globally sensitive in the detail dialog. */
  manageSensitive?: boolean
}

type ResourceItem = ResourceView | ResourceSummary
type ResourceKind = 'image' | 'file' | 'code' | 'skill' | 'url' | 'data' | 'other'

interface ResourceCategory {
  /** Stable resource category used for grouping and filtering. */
  kind: ResourceKind
  /** Chinese label shown in the statistic button and dialog. */
  label: string
  /** Category icon. */
  icon: Component
}

const { resources, compact = false, manageSensitive = true } = defineProps<ResourceListProps>()
const dialogOpen = ref(false)
const activeCategory = ref<ResourceCategory | null>(null)

const categories: ResourceCategory[] = [
  { kind: 'image', label: '图片', icon: Picture },
  { kind: 'file', label: '文件', icon: Document },
  { kind: 'code', label: '代码', icon: DocumentCopy },
  { kind: 'skill', label: 'Skill', icon: MagicStick },
  { kind: 'url', label: '链接', icon: Link },
  { kind: 'data', label: '内嵌资源', icon: Collection },
  { kind: 'other', label: '其他', icon: Tickets },
]

function categoryOf(resource: ResourceItem): ResourceKind {
  if (resource.kind === 'image' || resource.kind === 'file' || resource.kind === 'code' || resource.kind === 'skill' || resource.kind === 'url' || resource.kind === 'data') return resource.kind
  return 'other'
}

const categoryStats = computed(() => categories
  .map((category) => ({
    ...category,
    count: resources.filter((resource) => categoryOf(resource) === category.kind).length,
  }))
  .filter((category) => category.count > 0))

const activeResources = computed(() => activeCategory.value
  ? resources.filter((resource) => categoryOf(resource) === activeCategory.value?.kind)
  : [])
const highlightedPreviews = computed(() => new Map(resources.flatMap((resource) => {
  const preview = sourceCodePreview(resource)
  return preview ? [[resource.fingerprint, preview] as const] : []
})))

function openCategory(category: ResourceCategory) {
  activeCategory.value = category
  dialogOpen.value = true
}

function resourceLabel(resource: ResourceItem): string {
	if (resource.name) return resource.name
	if (resource.previewData) return resource.mimeType ? `${resource.mimeType} 图片` : '图片资源'
	return resourceReferenceLabel(resource.uri) || `资源 #${resource.id}`
}

function kindLabel(resource: ResourceItem): string {
  return resource.mimeType || resource.kind || '未知类型'
}

function usageLabel(resource: ResourceItem): string {
  if ('useCount' in resource) return `${resource.useCount} 次使用 · ${resource.requestCount} 个请求`
  return resource.locations?.length ? `${resource.locations.length} 个位置` : '当前请求'
}

async function toggleSensitive(resource: ResourceItem, value: boolean) {
  try {
    await request<null>(`/admin/gateway/resources/${resource.id}/sensitive`, {
      method: 'PUT',
      body: JSON.stringify({ sensitive: value }),
    })
    resource.sensitive = value
    ElMessage.success(value ? '已设为全局敏感资源' : '已取消全局敏感标记')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '敏感资源标记保存失败')
  }
}
</script>

<template>
  <div v-if="categoryStats.length" class="resource-stats" :class="{ 'is-compact': compact }" aria-label="资源分类统计">
    <button
      v-for="category in categoryStats"
      :key="category.kind"
      type="button"
      class="resource-stat"
      :title="`查看${category.label}资源`"
      @click="openCategory(category)"
    >
      <el-icon><component :is="category.icon" /></el-icon>
      <span>{{ category.label }}</span>
      <strong>{{ category.count }}</strong>
    </button>
  </div>

  <el-dialog v-model="dialogOpen" class="resource-list-dialog" :title="`${activeCategory?.label ?? '资源'} · ${activeResources.length} 项`" :width="activeCategory?.kind === 'url' ? 'min(960px, 94vw)' : 'min(720px, 92vw)'" append-to-body destroy-on-close>
    <el-table v-if="activeResources.length && activeCategory?.kind === 'url'" class="resource-link-table" :data="activeResources" row-key="fingerprint" max-height="min(62vh, 620px)" empty-text="暂无链接">
      <el-table-column label="链接" min-width="420">
        <template #default="scope">
          <div class="resource-link-cell">
            <el-tooltip :content="resourceLabel(scope.row)" placement="top-start" :show-after="250"><strong>{{ resourceLabel(scope.row) }}</strong></el-tooltip>
            <el-tooltip :content="scope.row.uri || scope.row.preview" placement="top-start" :show-after="250">
              <a v-if="resourceExternalHref(scope.row.uri)" class="resource-link-uri" :href="resourceExternalHref(scope.row.uri)" target="_blank" rel="noopener noreferrer"><code>{{ scope.row.uri || scope.row.preview }}</code></a>
              <code v-else class="resource-link-uri">{{ scope.row.uri || scope.row.preview }}</code>
            </el-tooltip>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="引用" width="170"><template #default="scope"><span class="resource-link-usage">{{ usageLabel(scope.row) }}</span></template></el-table-column>
      <el-table-column label="敏感状态" width="150">
        <template #default="scope"><div class="resource-link-sensitive"><el-tag v-if="scope.row.sensitive" type="danger" effect="plain" size="small">敏感</el-tag><span v-else>普通</span><el-switch v-if="manageSensitive" :model-value="scope.row.sensitive" :aria-label="`${scope.row.sensitive ? '取消' : '设为'}敏感资源`" @change="toggleSensitive(scope.row, Boolean($event))" /></div></template>
      </el-table-column>
      <el-table-column label="打开" width="62" fixed="right" align="right">
        <template #default="scope"><el-tooltip content="新窗口打开" placement="top"><el-button class="table-action-button" text type="primary" :icon="Link" tag="a" :href="resourceExternalHref(scope.row.uri) || undefined" target="_blank" rel="noopener noreferrer" :disabled="!resourceExternalHref(scope.row.uri)" aria-label="新窗口打开链接" /></el-tooltip></template>
      </el-table-column>
    </el-table>
    <div v-else-if="activeResources.length" class="resource-detail-list">
      <article v-for="resource in activeResources" :key="`${resource.id}-${resource.fingerprint}-${resource.uri}`" class="resource-detail-item">
        <div class="resource-detail-heading">
          <el-icon><component :is="activeCategory?.icon ?? Tickets" /></el-icon>
          <el-tooltip class="resource-title-tooltip" :content="resourceLabel(resource)" placement="top-start" :show-after="250">
            <strong class="resource-truncated">{{ resourceLabel(resource) }}</strong>
          </el-tooltip>
          <el-tag v-if="resource.sensitive" type="danger" effect="plain" size="small">敏感</el-tag>
        </div>
        <div class="resource-browser-preview">
          <SourceCodePreview v-if="highlightedPreviews.get(resource.fingerprint)" :code="highlightedPreviews.get(resource.fingerprint)?.code ?? ''" :language="highlightedPreviews.get(resource.fingerprint)?.language ?? 'plaintext'" :label="`${resourceLabel(resource)} 源码预览`" />
          <ImagePreviewDialog v-else-if="resourcePreviewMode(resource) === 'image'" :src="resourcePreviewSource(resource)" :alt="resourceLabel(resource)" :title="resourceLabel(resource)"><img :src="resourcePreviewSource(resource)" alt="资源图片预览" /></ImagePreviewDialog>
          <iframe v-else-if="resourcePreviewMode(resource) === 'pdf'" :src="resourcePreviewSource(resource)" :title="`${resourceLabel(resource)} 预览`" />
          <iframe v-else-if="resourcePreviewMode(resource) === 'text'" :src="resourcePreviewSource(resource)" :title="`${resourceLabel(resource)} 预览`" sandbox="" />
          <audio v-else-if="resourcePreviewMode(resource) === 'audio'" :src="resourcePreviewSource(resource)" controls />
          <video v-else-if="resourcePreviewMode(resource) === 'video'" :src="resourcePreviewSource(resource)" controls />
          <ResourceFallbackPreview v-else :message="resourceFallbackLabel(resource)" :reference="resourceReferenceLabel(resource.uri || resource.preview)" :href="resourceExternalHref(resource.uri)" />
        </div>
        <dl>
          <div><dt>类型</dt><dd>{{ kindLabel(resource) }}</dd></div>
          <div v-if="resource.sizeBytes > 0"><dt>大小</dt><dd>{{ resource.sizeBytes.toLocaleString() }} bytes</dd></div>
          <div v-if="resource.sha256"><dt>SHA-256</dt><dd><el-tooltip :content="resource.sha256" placement="top-start" :show-after="250"><code class="resource-truncated">{{ resource.sha256 }}</code></el-tooltip></dd></div>
          <div><dt>使用</dt><dd>{{ usageLabel(resource) }}</dd></div>
        </dl>
        <div v-if="manageSensitive" class="resource-sensitive-control">
          <span><strong>全局敏感资源</strong><small>引用后标记请求与会话为敏感资源泄露</small></span>
          <el-switch :model-value="resource.sensitive" :aria-label="`${resource.sensitive ? '取消' : '设为'}敏感资源`" @change="toggleSensitive(resource, Boolean($event))" />
        </div>
      </article>
    </div>
    <el-empty v-else description="暂无资源" />
    <template #footer><el-button @click="dialogOpen = false">关闭</el-button></template>
  </el-dialog>
</template>

<style scoped>
.resource-stats { display: flex; flex-wrap: wrap; gap: 8px; }
.resource-stat { display: inline-flex; align-items: center; gap: 7px; min-height: 34px; padding: 6px 11px; border: 1px solid var(--hongfen-border); border-radius: 4px; background: var(--hongfen-surface); color: var(--hongfen-text); cursor: pointer; }
.resource-stat:hover { border-color: var(--hongfen-primary); background: var(--hongfen-primary-selection); color: var(--hongfen-primary-hover); }
.resource-stat span { color: var(--hongfen-text-muted); font-size: 12px; }
.resource-stat strong { min-width: 1.2em; color: var(--hongfen-text); font: 700 14px/1 var(--hongfen-font-mono); text-align: center; }
.is-compact { gap: 5px; }
.is-compact .resource-stat { min-height: 26px; padding: 3px 7px; gap: 4px; }
.is-compact .resource-stat span { font-size: 10px; }
.is-compact .resource-stat strong { font-size: 12px; }
.resource-link-table { width: 100%; }
.resource-link-table :deep(.el-table__inner-wrapper::before) { display: none; }
.resource-link-cell { display: grid; min-width: 0; gap: 4px; }
.resource-link-cell > .el-tooltip { min-width: 0; }
.resource-link-cell strong, .resource-link-uri, .resource-link-uri code { display: block; min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-link-cell strong { color: var(--hongfen-text); }
.resource-link-uri { color: var(--hongfen-primary-hover); text-decoration: none; }
.resource-link-uri:hover { text-decoration: underline; }
.resource-link-uri code { font-family: var(--hongfen-font-mono); font-size: 11px; }
.resource-link-usage { color: var(--hongfen-text-muted); font-size: 12px; white-space: nowrap; }
.resource-link-sensitive { display: flex; align-items: center; justify-content: space-between; gap: 8px; color: var(--hongfen-text-muted); font-size: 12px; }
.resource-detail-list { display: grid; min-width: 0; max-width: 100%; gap: 8px; max-height: min(56vh, 520px); overflow-x: hidden; overflow-y: auto; }
.resource-detail-item { min-width: 0; max-width: 100%; padding: 12px 14px; overflow: hidden; border: 1px solid var(--hongfen-border); border-radius: 6px; background: var(--hongfen-surface); box-sizing: border-box; }
.resource-detail-heading { display: flex; align-items: center; gap: 7px; min-width: 0; color: var(--hongfen-text); }
.resource-title-tooltip { min-width: 0; flex: 1; overflow: hidden; }
.resource-title-tooltip :deep(.el-tooltip__trigger) { display: block; min-width: 0; max-width: 100%; overflow: hidden; }
.resource-truncated { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-detail-heading strong { color: var(--hongfen-text); }
.resource-detail-item dl { display: grid; gap: 4px; margin: 8px 0 0 25px; color: var(--hongfen-text-muted); font-size: 12px; }
.resource-detail-item dl > div { display: grid; grid-template-columns: 54px minmax(0, 1fr); gap: 8px; }
.resource-detail-item dt { color: var(--hongfen-text-subtle); }
.resource-detail-item dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
.resource-detail-item code { font-family: var(--hongfen-font-mono); }
.resource-uri { max-width: 100%; }
.resource-browser-preview { display: flex; width: 100%; min-width: 0; max-width: 100%; min-height: 80px; max-height: 280px; margin-top: 12px; padding: 8px; overflow: hidden; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); box-sizing: border-box; }
.resource-browser-preview img, .resource-browser-preview video { display: block; max-width: 100%; max-height: 260px; margin: auto; object-fit: contain; }
.resource-browser-preview iframe { width: 100%; min-height: 250px; border: 0; background: var(--hongfen-surface); }
.resource-browser-preview audio { width: 100%; margin: auto; }
.resource-sensitive-control { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 12px; padding-top: 10px; border-top: 1px solid var(--hongfen-border); }
.resource-sensitive-control span { display: grid; gap: 2px; }
.resource-sensitive-control strong { color: var(--hongfen-text); font-size: 12px; }
.resource-sensitive-control small { color: var(--hongfen-text-muted); font-size: 10px; }
:global(.resource-list-dialog .el-dialog__body) { min-width: 0; overflow: hidden; }
</style>
