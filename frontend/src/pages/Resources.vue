<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, reactive, ref } from 'vue'
import { Collection, Document, DocumentCopy, Link, MagicStick, Picture, Refresh, RefreshLeft, Search, Tickets, View } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import RequestPayloadDialog from '@/components/RequestPayloadDialog.vue'
import SessionLogDrawer from '@/components/SessionLogDrawer.vue'
import type { CodexSessionDetail, CodexSessionSummary, RelayRequestLog, ResourceCatalogItem, ResourceDetail, ResourcePage, ResourceRequestRef, ResourceSessionRef } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber } from '@/utils/formatters'
import { resourceExternalHref, resourceFallbackLabel, resourcePreviewMode, resourcePreviewSource, resourceReferenceLabel } from '@/utils/resourcePreview'
import { sourceCodePreview } from '@/utils/codeLanguage'
import ImagePreviewDialog from '@/components/ImagePreviewDialog.vue'
import ResourceFallbackPreview from '@/components/ResourceFallbackPreview.vue'

const SourceCodePreview = defineAsyncComponent(() => import('@/components/SourceCodePreview.vue'))

const loading = ref(true)
const errorMessage = ref('')
const resources = ref<ResourceCatalogItem[]>([])
const total = ref(0)
const pagination = reactive({ page: 1, pageSize: 50 })
const filters = reactive({ search: '', kind: '', sensitive: '', lastSeenBeforeDays: '' })
const customLastSeenDays = ref<number | undefined>()
const detailOpen = ref(false)
const detailLoading = ref(false)
const detail = ref<ResourceDetail | null>(null)
const detailSourcePreview = computed(() => detail.value ? sourceCodePreview(detail.value.resource) : null)
const sessionDrawerOpen = ref(false)
const selectedSession = ref<CodexSessionSummary | null>(null)
const sessionDetailLoadingKey = ref('')
const requestDetailOpen = ref(false)
const selectedRequest = ref<RelayRequestLog | null>(null)
const requestDetailLoadingId = ref('')

const kindOptions = [
  { value: 'image', label: '图片', icon: Picture },
  { value: 'file', label: '文件', icon: Document },
  { value: 'code', label: '代码', icon: DocumentCopy },
  { value: 'skill', label: 'Skill', icon: MagicStick },
  { value: 'url', label: '链接', icon: Link },
  { value: 'data', label: '内嵌资源', icon: Collection },
  { value: 'reference', label: '其他', icon: Tickets },
]
const lastSeenBeforeOptions = [7, 15, 30, 90, 180, 365]

const detailTitle = computed(() => {
  if (!detail.value) return '资源详情'
  const resource = detail.value.resource
  if (resource.name) return resource.name
  if (resource.previewData) return resource.mimeType ? `${resource.mimeType} 图片` : '图片资源'
  return resourceReferenceLabel(resource.uri) || resource.preview || `资源 #${resource.id}`
})

function kindLabel(kind: string): string {
  return kindOptions.find((option) => option.value === kind)?.label || '其他'
}

function kindType(kind: string): 'success' | 'warning' | 'info' | 'danger' {
  if (kind === 'image') return 'success'
  if (kind === 'file' || kind === 'data' || kind === 'skill') return 'warning'
  if (kind === 'url') return 'info'
  return 'danger'
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

function buildQuery(): URLSearchParams {
  const query = new URLSearchParams({ page: String(pagination.page), pageSize: String(pagination.pageSize) })
  if (filters.search.trim()) query.set('search', filters.search.trim())
  if (filters.kind) query.set('kind', filters.kind)
  if (filters.sensitive) query.set('sensitive', filters.sensitive)
  if (filters.lastSeenBeforeDays && filters.lastSeenBeforeDays !== 'custom') query.set('lastSeenBeforeDays', filters.lastSeenBeforeDays)
  if (filters.lastSeenBeforeDays === 'custom' && customLastSeenDays.value) query.set('lastSeenBeforeDays', String(customLastSeenDays.value))
  return query
}

async function loadResources() {
  loading.value = true
  errorMessage.value = ''
  try {
    const page = await request<ResourcePage>(`/admin/gateway/resources?${buildQuery()}`)
    resources.value = page.items
    total.value = page.total
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '资源目录加载失败'
  } finally {
    loading.value = false
  }
}

function searchResources() {
  if (filters.lastSeenBeforeDays === 'custom' && (!Number.isInteger(customLastSeenDays.value) || (customLastSeenDays.value ?? 0) <= 0)) {
    ElMessage.error('最后发现时间天数必须为正整数')
    return
  }
  pagination.page = 1
  void loadResources()
}

function resetResources() {
  Object.assign(filters, { search: '', kind: '', sensitive: '', lastSeenBeforeDays: '' })
  customLastSeenDays.value = undefined
  pagination.page = 1
  void loadResources()
}

async function openDetail(row: ResourceCatalogItem) {
  detailOpen.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await request<ResourceDetail>(`/admin/gateway/resources/${row.id}`)
  } catch (error) {
    detailOpen.value = false
    ElMessage.error(error instanceof Error ? error.message : '资源详情加载失败')
  } finally {
    detailLoading.value = false
  }
}

function resourceSessionKey(session: ResourceSessionRef): string {
  return session.sessionId ? `${session.tokenId}:${session.sessionId}` : `request:${session.fallbackRequestId}`
}

async function openReferencedSession(session: ResourceSessionRef) {
  if (sessionDetailLoadingKey.value) return
  const key = resourceSessionKey(session)
  if (!session.sessionId && !session.fallbackRequestId) {
    ElMessage.error('该引用缺少可用的会话标识')
    return
  }
  sessionDetailLoadingKey.value = key
  const query = new URLSearchParams({ page: '1', pageSize: '25' })
  if (session.sessionId) {
    query.set('sessionId', session.sessionId)
    query.set('tokenId', String(session.tokenId))
  } else {
    query.set('requestId', session.fallbackRequestId)
  }
  try {
    const sessionDetail = await request<CodexSessionDetail>(`/admin/gateway/sessions/detail?${query}`)
    selectedSession.value = sessionDetail.summary
    sessionDrawerOpen.value = true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '会话详情加载失败')
  } finally {
    sessionDetailLoadingKey.value = ''
  }
}

async function openReferencedRequest(requestRef: ResourceRequestRef) {
  if (requestDetailLoadingId.value) return
  requestDetailLoadingId.value = requestRef.id
  try {
    selectedRequest.value = await request<RelayRequestLog>(`/admin/gateway/logs/${encodeURIComponent(requestRef.id)}`)
    requestDetailOpen.value = true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '调用详情加载失败')
  } finally {
    requestDetailLoadingId.value = ''
  }
}

async function toggleSensitive(row: ResourceCatalogItem, value: boolean) {
  try {
    await request<null>(`/admin/gateway/resources/${row.id}/sensitive`, {
      method: 'PUT',
      body: JSON.stringify({ sensitive: value }),
    })
    row.sensitive = value
    if (detail.value?.resource.id === row.id) detail.value.resource.sensitive = value
    ElMessage.success(value ? '已设为全局敏感资源' : '已取消全局敏感标记')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '敏感标记保存失败')
  }
}

onMounted(loadResources)
</script>

<template>
  <div class="page-stack resources-page">
    <header class="page-heading">
      <div><h1>资源中心</h1><p>汇总所有已识别资源，并反查引用它的会话与请求</p></div>
      <div class="page-actions"><el-tooltip content="刷新资源目录" placement="bottom"><el-button class="page-refresh-button" :icon="Refresh" :loading="loading" aria-label="刷新资源目录" @click="loadResources" /></el-tooltip></div>
    </header>

    <section class="filter-bar resources-filter-bar" aria-label="资源筛选">
      <el-input v-model="filters.search" clearable placeholder="名称、引用地址、SHA-256" @keyup.enter="searchResources" />
      <el-select v-model="filters.kind" clearable placeholder="全部类型"><el-option v-for="option in kindOptions" :key="option.value" :label="option.label" :value="option.value" /></el-select>
      <el-select v-model="filters.sensitive" clearable placeholder="全部敏感状态"><el-option label="仅敏感资源" value="true" /><el-option label="仅普通资源" value="false" /></el-select>
      <div class="last-seen-filter"><el-select v-model="filters.lastSeenBeforeDays" placeholder="最后发现时间"><el-option label="全部时间" value="" /><el-option v-for="days in lastSeenBeforeOptions" :key="days" :label="`${days} 天之前`" :value="String(days)" /><el-option label="自定义天数" value="custom" /></el-select><el-input-number v-if="filters.lastSeenBeforeDays === 'custom'" v-model="customLastSeenDays" :min="1" :max="36500" controls-position="right" placeholder="天数" /></div>
      <div class="filter-actions"><el-button :icon="RefreshLeft" :disabled="loading" @click="resetResources">重置</el-button><el-button type="primary" :icon="Search" :loading="loading" @click="searchResources">查询</el-button></div>
    </section>

    <div v-if="errorMessage" class="state-panel state-error" role="alert"><strong>资源目录加载失败</strong><span>{{ errorMessage }}</span><el-button :loading="loading" @click="loadResources">重试</el-button></div>
    <section v-else v-loading="loading" class="surface-panel resource-table-panel">
      <header class="resource-table-toolbar"><strong>全部资源</strong><span>{{ formatCompactNumber(total) }} 个资源 · 点击行查看引用关系</span></header>
      <el-table :data="resources" row-key="id" height="100%" empty-text="当前筛选条件下没有资源" @row-click="openDetail">
        <el-table-column label="资源" min-width="260">
          <template #default="scope"><div class="primary-cell resource-primary"><ImagePreviewDialog v-if="scope.row.previewData" :src="scope.row.previewData" :alt="scope.row.name || '图片资源'" :title="scope.row.name || '图片资源'" @click.stop><img :src="scope.row.previewData" alt="" class="resource-thumbnail" /></ImagePreviewDialog><div class="resource-primary-content"><el-tooltip :content="scope.row.name || (scope.row.previewData ? '图片资源' : resourceReferenceLabel(scope.row.uri)) || `资源 #${scope.row.id}`" placement="top-start" :show-after="250"><strong>{{ scope.row.name || (scope.row.previewData ? '图片资源' : resourceReferenceLabel(scope.row.uri)) || `资源 #${scope.row.id}` }}</strong></el-tooltip><el-tooltip :content="scope.row.previewData ? '已保存图片预览' : resourceReferenceLabel(scope.row.uri || scope.row.sha256)" placement="top-start" :show-after="250"><small class="resource-uri">{{ scope.row.previewData ? '已保存图片预览' : resourceReferenceLabel(scope.row.uri || scope.row.sha256) }}</small></el-tooltip></div></div></template>
        </el-table-column>
        <el-table-column label="类型" width="112"><template #default="scope"><el-tag :type="kindType(scope.row.kind)" effect="plain">{{ kindLabel(scope.row.kind) }}</el-tag></template></el-table-column>
        <el-table-column label="引用统计" width="190"><template #default="scope"><div class="resource-counts"><strong>{{ formatCompactNumber(scope.row.useCount) }} 次引用</strong><small>{{ formatCompactNumber(scope.row.requestCount) }} 个请求 · {{ formatCompactNumber(scope.row.sessionCount) }} 个会话</small></div></template></el-table-column>
        <el-table-column label="敏感状态" width="170"><template #default="scope"><div class="sensitive-control"><el-tag v-if="scope.row.sensitive" type="danger" effect="plain">敏感资源</el-tag><span v-else class="muted-text">普通资源</span><el-switch :model-value="scope.row.sensitive" :aria-label="`${scope.row.sensitive ? '取消' : '设为'}敏感资源`" @click.stop @change="toggleSensitive(scope.row, Boolean($event))" /></div></template></el-table-column>
        <el-table-column label="最近发现" width="170"><template #default="scope">{{ formatDate(scope.row.lastSeenAt) }}</template></el-table-column>
        <el-table-column label="操作" width="62" fixed="right" align="right"><template #default="scope"><el-tooltip content="查看引用关系" placement="top"><el-button class="table-action-button" text :icon="View" aria-label="查看引用关系" @click.stop="openDetail(scope.row)" /></el-tooltip></template></el-table-column>
      </el-table>
      <footer class="table-pagination"><el-pagination v-model:current-page="pagination.page" v-model:page-size="pagination.pageSize" :disabled="loading" :total="total" :page-sizes="[25, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadResources" /></footer>
    </section>

    <el-dialog v-model="detailOpen" class="resource-detail-dialog" width="min(980px, 94vw)" append-to-body destroy-on-close>
      <template #header="{ titleId, titleClass }"><el-tooltip :content="detailTitle" placement="top-start" :show-after="250"><span :id="titleId" :class="[titleClass, 'resource-dialog-title']">{{ detailTitle }}</span></el-tooltip></template>
      <el-skeleton v-if="detailLoading" :rows="8" animated />
      <div v-else-if="detail" class="resource-detail">
        <section class="resource-detail-summary">
          <div><span>类型</span><strong>{{ kindLabel(detail.resource.kind) }}</strong></div>
          <div><span>引用次数</span><strong>{{ detail.requests.length }}</strong></div>
          <div><span>涉及会话</span><strong>{{ detail.sessions.length }}</strong></div>
          <div><span>敏感状态</span><el-tag :type="detail.resource.sensitive ? 'danger' : 'info'" effect="plain">{{ detail.resource.sensitive ? '敏感资源' : '普通资源' }}</el-tag></div>
        </section>
        <section class="resource-detail-meta"><div class="catalog-browser-preview"><SourceCodePreview v-if="detailSourcePreview" :code="detailSourcePreview.code" :language="detailSourcePreview.language" :label="`${detailTitle} 源码预览`" /><ImagePreviewDialog v-else-if="resourcePreviewMode(detail.resource) === 'image'" :src="resourcePreviewSource(detail.resource)" :alt="detailTitle" :title="detailTitle"><img :src="resourcePreviewSource(detail.resource)" alt="资源图片预览" /></ImagePreviewDialog><iframe v-else-if="resourcePreviewMode(detail.resource) === 'pdf'" :src="resourcePreviewSource(detail.resource)" :title="`${detailTitle} 预览`" /><iframe v-else-if="resourcePreviewMode(detail.resource) === 'text'" :src="resourcePreviewSource(detail.resource)" :title="`${detailTitle} 预览`" sandbox="" /><audio v-else-if="resourcePreviewMode(detail.resource) === 'audio'" :src="resourcePreviewSource(detail.resource)" controls /><video v-else-if="resourcePreviewMode(detail.resource) === 'video'" :src="resourcePreviewSource(detail.resource)" controls /><ResourceFallbackPreview v-else :message="resourceFallbackLabel(detail.resource)" :reference="resourceReferenceLabel(detail.resource.uri || detail.resource.preview)" :href="resourceExternalHref(detail.resource.uri)" /></div><div v-if="detail.resource.sha256"><span>SHA-256</span><el-tooltip :content="detail.resource.sha256" placement="top-start" :show-after="250"><code class="resource-truncated">{{ detail.resource.sha256 }}</code></el-tooltip></div><div><span>敏感标记</span><el-switch :model-value="detail.resource.sensitive" aria-label="切换资源敏感标记" @change="toggleSensitive(resources.find((item) => item.id === detail?.resource.id) ?? ({ ...detail.resource, requestCount: detail.requests.length, useCount: detail.requests.length, sessionCount: detail.sessions.length } as ResourceCatalogItem), Boolean($event))" /></div></section>
        <el-tabs>
          <el-tab-pane label="引用会话"><el-table :data="detail.sessions" max-height="340" empty-text="暂无可归属会话"><el-table-column label="会话" min-width="240"><template #default="scope"><div class="primary-cell"><el-tooltip class="resource-cell-tooltip" :content="scope.row.sessionName || '未命名会话'" placement="top-start" :show-after="250"><strong>{{ scope.row.sessionName || '未命名会话' }}</strong></el-tooltip><el-tooltip class="resource-cell-tooltip" :content="scope.row.sessionId || scope.row.fallbackRequestId || '未识别会话'" placement="top-start" :show-after="250"><small>{{ scope.row.sessionId || scope.row.fallbackRequestId || '未识别会话' }}</small></el-tooltip></div></template></el-table-column><el-table-column label="请求数" width="120" align="right"><template #default="scope">{{ scope.row.requestCount }}</template></el-table-column><el-table-column label="令牌" width="100" align="right"><template #default="scope">#{{ scope.row.tokenId }}</template></el-table-column><el-table-column label="详情" width="62" fixed="right" align="right"><template #default="scope"><el-tooltip content="查看会话详情" placement="top"><el-button class="table-action-button" text :icon="View" :loading="sessionDetailLoadingKey === resourceSessionKey(scope.row)" aria-label="查看会话详情" @click.stop="openReferencedSession(scope.row)" /></el-tooltip></template></el-table-column></el-table></el-tab-pane>
          <el-tab-pane label="引用请求"><el-table :data="detail.requests" max-height="340" empty-text="暂无引用请求"><el-table-column label="时间" width="170"><template #default="scope">{{ formatDate(scope.row.createdAt) }}</template></el-table-column><el-table-column label="请求 ID" min-width="250"><template #default="scope"><el-tooltip class="resource-cell-tooltip" :content="scope.row.id" placement="top-start" :show-after="250"><code>{{ scope.row.id }}</code></el-tooltip></template></el-table-column><el-table-column label="会话" min-width="180"><template #default="scope"><el-tooltip class="resource-cell-tooltip" :content="scope.row.sessionName || scope.row.sessionId || '未命名会话'" placement="top-start" :show-after="250"><span>{{ scope.row.sessionName || scope.row.sessionId || '未命名会话' }}</span></el-tooltip></template></el-table-column><el-table-column label="模型" min-width="140"><template #default="scope"><el-tooltip class="resource-cell-tooltip" :content="scope.row.requestedModel" placement="top-start" :show-after="250"><code>{{ scope.row.requestedModel }}</code></el-tooltip></template></el-table-column><el-table-column label="位置" min-width="180"><template #default="scope"><el-tooltip class="resource-cell-tooltip" :content="scope.row.location" placement="top-start" :show-after="250"><code>{{ scope.row.location }}</code></el-tooltip></template></el-table-column><el-table-column label="详情" width="62" fixed="right" align="right"><template #default="scope"><el-tooltip content="查看调用详情" placement="top"><el-button class="table-action-button" text :icon="View" :loading="requestDetailLoadingId === scope.row.id" aria-label="查看调用详情" @click.stop="openReferencedRequest(scope.row)" /></el-tooltip></template></el-table-column></el-table></el-tab-pane>
        </el-tabs>
      </div>
      <template #footer><el-button @click="detailOpen = false">关闭</el-button></template>
    </el-dialog>
    <SessionLogDrawer v-model="sessionDrawerOpen" :summary="selectedSession" />
    <RequestPayloadDialog v-model="requestDetailOpen" :request="selectedRequest" />
  </div>
</template>

<style scoped>
.resources-page { height: 100%; min-height: 0; grid-template-rows: auto auto minmax(320px, 1fr); overflow: hidden; padding-bottom: 16px; }
.resources-filter-bar { grid-template-columns: minmax(220px, 1.4fr) repeat(2, minmax(150px, .7fr)) minmax(180px, .9fr) auto; }
.last-seen-filter { display: flex; min-width: 0; gap: 8px; }
.last-seen-filter .el-select { min-width: 0; flex: 1 1 auto; }
.last-seen-filter .el-input-number { width: 112px; flex: 0 0 112px; }
.resource-table-panel { display: flex; min-height: 320px; flex-direction: column; }
.resource-table-toolbar { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; min-height: 48px; padding: 12px 16px; border-bottom: 1px solid var(--hongfen-border); }
.resource-table-toolbar span { color: var(--hongfen-text-muted); font-size: 11px; }
.resource-table-panel > .el-table { min-height: 0; flex: 1 1 0; }
.resource-uri { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-primary { display: flex; align-items: center; gap: 9px; }
.resource-primary-content { min-width: 0; }
.resource-primary-content > .el-tooltip { display: block; min-width: 0; }
.resource-primary-content strong, .resource-primary-content small { display: block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-thumbnail { width: 38px; height: 38px; flex: 0 0 auto; border: 1px solid var(--hongfen-border); object-fit: contain; background: var(--hongfen-surface-muted); }
.resource-counts { display: grid; gap: 3px; }
.resource-counts strong { color: var(--hongfen-text); font: 600 12px/1.3 var(--hongfen-font-mono); }
.resource-counts small { color: var(--hongfen-text-muted); font-size: 10px; }
.sensitive-control { display: flex; align-items: center; gap: 9px; }
.resource-detail-summary { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }
.resource-detail-summary > div { display: grid; min-height: 70px; place-content: center; gap: 6px; border-right: 1px solid var(--hongfen-border); text-align: center; }
.resource-detail-summary > div:last-child { border-right: 0; }
.resource-detail-summary span, .resource-detail-meta span { color: var(--hongfen-text-muted); font-size: 11px; }
.resource-detail-summary strong { color: var(--hongfen-text); font: 650 18px/1 var(--hongfen-font-mono); }
.resource-detail-meta { display: grid; gap: 7px; margin: 12px 0; padding: 10px 12px; border: 1px solid var(--hongfen-border); }
.resource-detail-meta > div:not(.catalog-browser-preview) { display: grid; grid-template-columns: 68px minmax(0, 1fr); gap: 9px; min-width: 0; }
.resource-detail-meta > div > .el-tooltip { min-width: 0; }
.resource-detail-meta code { min-width: 0; }
.resource-dialog-title { display: block; max-width: min(720px, 76vw); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-detail-item dd > .el-tooltip, .resource-detail-meta > div > .el-tooltip { display: block; max-width: 100%; }
.resource-detail-dialog :global(.el-tooltip) { min-width: 0; max-width: 100%; }
.resource-cell-tooltip { display: block; min-width: 0; max-width: 100%; overflow: hidden; }
.resource-cell-tooltip > strong, .resource-cell-tooltip > small, .resource-cell-tooltip > span, .resource-cell-tooltip > code { display: block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.resource-detail-meta > .catalog-browser-preview { display: flex; width: 100%; min-width: 0; max-width: 100%; min-height: 100px; max-height: 300px; grid-column: 1 / -1; padding: 8px; overflow: hidden; border-bottom: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); box-sizing: border-box; }
.catalog-browser-preview img, .catalog-browser-preview video { max-width: 100%; max-height: 280px; margin: auto; object-fit: contain; }
.catalog-browser-preview iframe { width: 100%; min-height: 270px; border: 0; background: var(--hongfen-surface); }
.catalog-browser-preview audio { width: 100%; margin: auto; }
@media (max-width: 800px) { .resources-page { height: auto; min-height: 100%; overflow: visible; } .resources-filter-bar { grid-template-columns: 1fr; } .last-seen-filter .el-input-number { width: 120px; flex-basis: 120px; } .resource-detail-summary { grid-template-columns: repeat(2, 1fr); } .resource-detail-summary > div:nth-child(2) { border-right: 0; } .resource-detail-summary > div:nth-child(-n + 2) { border-bottom: 1px solid var(--hongfen-border); } }
</style>
