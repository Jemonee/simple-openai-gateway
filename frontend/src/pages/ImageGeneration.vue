<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Connection, Picture, Promotion } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { useAdminAuth } from '@/composables/useAdminAuth'
import type { AvailableGatewayModel, Channel, GatewayModel, ImageGenerationRequest, ImageGenerationResponse } from '@/types/gateway'
import { request } from '@/utils/api'
import { formatCompactNumber, formatUSD } from '@/utils/formatters'

const supportedModels = new Set(['gpt-image-2.5-flare', 'gpt-image-2.5-sunburst'])
const { user } = useAdminAuth()
const isAccessTokenLogin = computed(() => user.value?.loginType === 'access_token')
const loadingOptions = ref(true)
const generating = ref(false)
const optionsError = ref('')
const models = ref<GatewayModel[]>([])
const tokenModels = ref<AvailableGatewayModel[]>([])
const channels = ref<Channel[]>([])
const model = ref('')
const channelId = ref(0)
const prompt = ref('')
const size = ref<ImageGenerationRequest['size']>('1K')
const quality = ref<ImageGenerationRequest['quality']>('medium')
const result = ref<ImageGenerationResponse | null>(null)

const imageModels = computed(() => (isAccessTokenLogin.value ? tokenModels.value : models.value)
  .filter(item => supportedModels.has(item.name)))
const readyChannels = computed(() => channels.value.filter(channel => (
  channel.enabled && (!channel.circuitOpenUntil || Date.parse(channel.circuitOpenUntil) <= Date.now())
)))
const channelsForModel = computed(() => {
  const selected = imageModels.value.find(item => item.name === model.value)
  if (!selected) return []
  return readyChannels.value.filter(channel => channel.models.some(mapping => mapping.enabled && mapping.modelId === selected.id))
})
const canGenerate = computed(() => Boolean(model.value && prompt.value.trim() && !generating.value))

async function loadOptions() {
  loadingOptions.value = true
  optionsError.value = ''
  try {
    if (isAccessTokenLogin.value) {
      tokenModels.value = await request<AvailableGatewayModel[]>('/admin/gateway/models/available')
      models.value = []
      channels.value = []
    } else {
      const [modelItems, channelItems] = await Promise.all([
        request<GatewayModel[]>('/admin/gateway/models'),
        request<Channel[]>('/admin/gateway/channels'),
      ])
      models.value = modelItems
      channels.value = channelItems
    }
    if (!imageModels.value.some(item => item.name === model.value)) model.value = imageModels.value[0]?.name ?? ''
    if (!channelsForModel.value.some(channel => channel.id === channelId.value)) channelId.value = 0
  } catch (error) {
    optionsError.value = error instanceof Error ? error.message : '生图模型和渠道加载失败'
  } finally {
    loadingOptions.value = false
  }
}

function onModelChange() {
  channelId.value = 0
}

async function generate() {
  if (!canGenerate.value) return
  generating.value = true
  result.value = null
  try {
    result.value = await request<ImageGenerationResponse>('/admin/gateway/web-chat/images/generations', {
      method: 'POST',
      body: JSON.stringify({ model: model.value, channelId: isAccessTokenLogin.value ? 0 : channelId.value, prompt: prompt.value.trim(), size: size.value, quality: quality.value }),
    })
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '图片生成失败')
  } finally {
    generating.value = false
  }
}

onMounted(() => { void loadOptions() })
</script>

<template>
  <div class="image-generation-page">
    <section class="generation-panel" aria-labelledby="image-generation-title">
      <header class="panel-heading">
        <span class="heading-icon"><el-icon><Picture /></el-icon></span>
        <div><h1 id="image-generation-title">AI 生图</h1><p>使用 gpt-image-2.5 系列模型生成图片。</p></div>
      </header>

      <div v-if="optionsError" class="state-error" role="alert"><strong>加载失败</strong><span>{{ optionsError }}</span><el-button @click="loadOptions">重试</el-button></div>
      <div v-else-if="!loadingOptions && imageModels.length === 0" class="state-empty"><strong>没有可用的生图模型</strong><span>请在模型路由和渠道管理中配置 gpt-image-2.5-flare 或 gpt-image-2.5-sunburst。</span></div>
      <form v-else class="generation-form" @submit.prevent="generate">
        <label class="field field-wide"><span>提示词</span><el-input v-model="prompt" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" maxlength="200000" show-word-limit resize="none" placeholder="描述想生成的图像内容、构图、风格和光线。" aria-label="图片生成提示词" /></label>
        <label class="field"><span>模型</span><el-select v-model="model" :loading="loadingOptions" placeholder="选择生图模型" @change="onModelChange"><el-option v-for="item in imageModels" :key="item.id" :label="item.name" :value="item.name" /></el-select></label>
        <label v-if="!isAccessTokenLogin" class="field"><span><el-icon><Connection /></el-icon> 渠道</span><el-select v-model="channelId" :disabled="!model" placeholder="自动路由"><el-option label="自动路由选择" :value="0" /><el-option v-for="channel in channelsForModel" :key="channel.id" :label="`置顶 · ${channel.name}`" :value="channel.id" /></el-select></label>
        <label class="field"><span>分辨率</span><el-segmented v-model="size" :options="[{ label: '1K', value: '1K' }, { label: '2K', value: '2K' }, { label: '4K', value: '4K' }]" aria-label="图片分辨率" /></label>
        <label class="field"><span>质量</span><el-segmented v-model="quality" :options="[{ label: '低', value: 'low' }, { label: '中', value: 'medium' }, { label: '高', value: 'high' }]" aria-label="图片质量" /></label>
        <div class="form-actions"><span>将{{ channelId ? '固定使用所选渠道' : '按当前路由策略自动选择渠道' }}。</span><el-button native-type="submit" type="primary" :icon="Promotion" :loading="generating" :disabled="!canGenerate">生成图片</el-button></div>
      </form>
    </section>

    <section v-if="result" class="result-panel" aria-live="polite">
      <header><div><h2>生成结果</h2><p>{{ result.model }} · {{ size }} · {{ quality === 'low' ? '低质量' : quality === 'medium' ? '中质量' : '高质量' }}</p></div><code>{{ result.requestId }}</code></header>
      <div class="image-grid"><a v-for="(image, index) in result.images" :key="image" :href="image" target="_blank" rel="noopener noreferrer" :aria-label="`在新标签页打开生成图片 ${index + 1}`"><img :src="image" :alt="`生成图片 ${index + 1}`" /></a></div>
      <footer><span>{{ result.channelName || '自动路由' }}</span><span>{{ result.upstreamModel || result.model }}</span><span>输入 {{ formatCompactNumber(result.inputTokens) }}</span><span>输出 {{ formatCompactNumber(result.outputTokens) }}</span><span>{{ formatUSD(result.estimatedCostMicros) }}</span></footer>
    </section>
  </div>
</template>

<style scoped>
.image-generation-page { display: grid; grid-template-columns: minmax(360px, 460px) minmax(0, 1fr); height: 100%; min-height: 0; border: 1px solid var(--hongfen-border); background: var(--hongfen-page); }
.generation-panel, .result-panel { min-width: 0; padding: 20px; background: var(--hongfen-surface); }
.generation-panel { border-right: 1px solid var(--hongfen-border); overflow-y: auto; }
.panel-heading { display: flex; align-items: center; gap: 10px; margin-bottom: 20px; }
.heading-icon { display: grid; width: 34px; height: 34px; place-items: center; border: 1px solid var(--hongfen-primary); border-radius: 4px; color: var(--hongfen-primary); background: var(--hongfen-primary-soft); }
.panel-heading h1, .result-panel h2 { margin: 0; color: var(--hongfen-text); font-size: 16px; }
.panel-heading p, .result-panel header p { margin: 4px 0 0; color: var(--hongfen-text-muted); font-size: 11px; }
.generation-form { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.field { display: grid; gap: 5px; min-width: 0; color: var(--hongfen-text-muted); font-size: 11px; }
.field-wide, .form-actions { grid-column: 1 / -1; }
.field > span { display: flex; align-items: center; gap: 4px; }
.field :deep(.el-select), .field :deep(.el-segmented) { width: 100%; }
.form-actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-top: 4px; color: var(--hongfen-text-muted); font-size: 10px; }
.state-error, .state-empty { display: grid; gap: 7px; color: var(--hongfen-text-muted); font-size: 12px; }
.state-error strong { color: var(--hongfen-danger); }.state-empty strong { color: var(--hongfen-text); }
.result-panel { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; gap: 16px; overflow: auto; }
.result-panel > header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }.result-panel code { color: var(--hongfen-text-subtle); font-family: var(--hongfen-font-mono); font-size: 10px; overflow-wrap: anywhere; text-align: right; }
.image-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); align-content: start; gap: 12px; }.image-grid a { display: block; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); }.image-grid img { display: block; width: 100%; max-height: 680px; object-fit: contain; }
.result-panel > footer { display: flex; flex-wrap: wrap; gap: 6px 12px; color: var(--hongfen-text-subtle); font-size: 10px; }.result-panel > footer span:first-child { color: var(--hongfen-success); }
@media (max-width: 960px) { .image-generation-page { grid-template-columns: 1fr; height: auto; }.generation-panel { border-right: 0; border-bottom: 1px solid var(--hongfen-border); }.result-panel { min-height: 560px; } }
@media (max-width: 520px) { .generation-panel, .result-panel { padding: 14px; }.generation-form { grid-template-columns: 1fr; }.field-wide, .form-actions { grid-column: auto; }.form-actions { align-items: flex-start; flex-direction: column; }.form-actions .el-button { align-self: stretch; } }
</style>
