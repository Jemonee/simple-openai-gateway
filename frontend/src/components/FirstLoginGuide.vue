<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  ChatDotRound,
  Connection,
  Grid,
  Key,
  MagicStick,
  Setting,
  Tickets,
} from '@element-plus/icons-vue'
import type { Component } from 'vue'
import { isLocalBrowserAccess } from '@/utils/localAccess'

interface GuideStep {
  key: string
  title: string
  summary: string
  detail: string
  path: string
  action: string
  icon: Component
  localOnly?: boolean
}

const open = defineModel<boolean>({ required: true })
const emit = defineEmits<{ complete: []; skip: [] }>()
const router = useRouter()
const currentIndex = ref(0)
const localAccess = isLocalBrowserAccess()
const allSteps: GuideStep[] = [
  { key: 'channels', title: '配置渠道', summary: '连接上游服务', detail: '录入上游地址和密钥，获取可用模型，并启用需要参与调度的模型映射。', path: '/channels', action: '打开渠道管理', icon: Connection },
  { key: 'models', title: '查看模型路由', summary: '确认候选渠道', detail: '检查公开模型、路由策略、优先级、权重和每个模型当前可路由的渠道。', path: '/models', action: '打开模型路由', icon: Grid },
  { key: 'tokens', title: '配置访问令牌', summary: '控制客户端访问', detail: '签发客户端令牌并限定可访问模型。完整密钥只在创建或重置时显示一次。', path: '/tokens', action: '打开访问令牌', icon: Key },
  { key: 'local-config', title: '一键配置本地文件', summary: '连接本机 Codex', detail: '从运行总览写入本机 config.toml 和 auth.json。该入口仅在浏览器通过回环地址访问运行机时提供。', path: '/', action: '查看快速配置', icon: MagicStick, localOnly: true },
  { key: 'logs', title: '查看调用日志', summary: '追踪每次请求', detail: '按模型、渠道、令牌和时间筛选调用，查看请求结果、费用、用量、耗时和上游尝试。', path: '/logs', action: '打开调用日志', icon: Tickets },
  { key: 'active-sessions', title: '查看活跃会话', summary: '关注正在使用的会话', detail: '查看最近 30 分钟仍有用户调用的会话，并进入会话详情核对请求轨迹。', path: '/active-sessions', action: '打开活跃会话', icon: ChatDotRound },
  { key: 'settings', title: '调整系统设置', summary: '配置运行规则', detail: '调整路由决策占比、重试和超时、会话时长、日志记录细节及常用模型。', path: '/settings', action: '打开系统设置', icon: Setting },
]
const steps = computed(() => allSteps.filter((step) => !step.localOnly || localAccess))
const currentStep = computed(() => steps.value[currentIndex.value] ?? steps.value[0])
const isLastStep = computed(() => currentIndex.value === steps.value.length - 1)

watch(open, (value) => {
  if (value) currentIndex.value = 0
})

function skipGuide() {
  open.value = false
  emit('skip')
}

function finishGuide() {
  open.value = false
  emit('complete')
}

async function openCurrentFeature() {
  const step = currentStep.value
  if (!step) return
  finishGuide()
  await router.push(step.path)
}
</script>

<template>
  <el-dialog
    v-model="open"
    class="first-login-guide"
    width="min(860px, calc(100vw - 28px))"
    :show-close="false"
    :close-on-click-modal="false"
    :close-on-press-escape="false"
    align-center
  >
    <template #header>
      <div class="guide-heading">
        <div><span>首次使用</span><h2>快速熟悉网关控制台</h2><p>按推荐顺序了解主要功能，配置工作可稍后继续。</p></div>
        <b>{{ currentIndex + 1 }} / {{ steps.length }}</b>
      </div>
    </template>

    <div class="guide-layout">
      <nav class="guide-steps" aria-label="首次使用引导步骤">
        <button
          v-for="(step, index) in steps"
          :key="step.key"
          type="button"
          :class="{ 'is-active': index === currentIndex, 'is-visited': index < currentIndex }"
          :aria-current="index === currentIndex ? 'step' : undefined"
          @click="currentIndex = index"
        >
          <span>{{ index + 1 }}</span>
          <span><strong>{{ step.title }}</strong><small>{{ step.summary }}</small></span>
        </button>
      </nav>

      <section v-if="currentStep" class="guide-detail" aria-live="polite">
        <span class="guide-icon"><component :is="currentStep.icon" /></span>
        <div>
          <small>第 {{ currentIndex + 1 }} 步</small>
          <h3>{{ currentStep.title }}</h3>
          <p>{{ currentStep.detail }}</p>
        </div>
        <button class="guide-feature-link" type="button" @click="openCurrentFeature">{{ currentStep.action }}</button>
      </section>
    </div>

    <template #footer>
      <div class="guide-footer">
        <el-button text @click="skipGuide">跳过引导</el-button>
        <div>
          <el-button v-if="currentIndex > 0" @click="currentIndex -= 1">上一步</el-button>
          <el-button v-if="!isLastStep" type="primary" @click="currentIndex += 1">下一步</el-button>
          <el-button v-else type="primary" @click="finishGuide">开始使用</el-button>
        </div>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.guide-heading { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; }
.guide-heading span { color: var(--rose-primary); font-size: 11px; font-weight: 700; text-transform: uppercase; }
.guide-heading h2 { margin: 4px 0 0; color: var(--rose-text); font-size: 20px; font-weight: 650; }
.guide-heading p { margin: 5px 0 0; color: var(--rose-text-muted); font-size: 12px; }
.guide-heading b { flex: none; color: var(--rose-text-muted); font: 600 12px/1.5 var(--rose-font-mono); }
.guide-layout { display: grid; grid-template-columns: 260px minmax(0, 1fr); min-height: 390px; border: 1px solid var(--rose-border); }
.guide-steps { padding: 8px; border-right: 1px solid var(--rose-border); background: var(--rose-surface-muted); }
.guide-steps button { display: grid; width: 100%; grid-template-columns: 28px minmax(0, 1fr); align-items: center; gap: 10px; padding: 9px; border: 0; border-radius: var(--rose-radius-control); color: var(--rose-text-muted); background: transparent; text-align: left; cursor: pointer; }
.guide-steps button:hover { background: var(--rose-surface); }
.guide-steps button.is-active { color: var(--rose-primary); background: var(--rose-primary-soft); }
.guide-steps button > span:first-child { display: grid; width: 24px; height: 24px; place-items: center; border: 1px solid var(--rose-border-strong); border-radius: 50%; background: var(--rose-surface); font: 600 11px/1 var(--rose-font-mono); }
.guide-steps button.is-active > span:first-child { border-color: var(--rose-primary); color: var(--rose-surface); background: var(--rose-primary); }
.guide-steps button.is-visited > span:first-child { border-color: var(--rose-success); color: var(--rose-success); }
.guide-steps button > span:last-child { display: grid; min-width: 0; gap: 2px; }
.guide-steps strong { color: currentColor; font-size: 12px; font-weight: 650; }
.guide-steps small { overflow: hidden; color: var(--rose-text-subtle); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.guide-detail { display: grid; align-content: center; justify-items: start; gap: 20px; padding: 44px; }
.guide-icon { display: grid; width: 52px; height: 52px; place-items: center; border: 1px solid var(--rose-border); border-radius: var(--rose-radius-panel); color: var(--rose-primary); background: var(--rose-primary-soft); }
.guide-icon svg { width: 26px; height: 26px; }
.guide-detail small { color: var(--rose-text-subtle); font-size: 11px; }
.guide-detail h3 { margin: 5px 0 0; color: var(--rose-text); font-size: 21px; font-weight: 650; }
.guide-detail p { max-width: 470px; margin: 10px 0 0; color: var(--rose-text-muted); font-size: 13px; line-height: 1.75; }
.guide-feature-link { padding: 0; border: 0; color: var(--rose-primary); background: transparent; font-size: 12px; font-weight: 650; cursor: pointer; }
.guide-feature-link:hover { text-decoration: underline; }
.guide-footer { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.guide-footer > div { display: flex; gap: 8px; }
@media (max-width: 680px) {
  .guide-layout { grid-template-columns: 1fr; }
  .guide-steps { display: flex; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--rose-border); }
  .guide-steps button { width: auto; min-width: 52px; grid-template-columns: 1fr; justify-items: center; }
  .guide-steps button > span:last-child { display: none; }
  .guide-detail { min-height: 280px; padding: 28px; }
  .guide-footer { align-items: stretch; flex-direction: column-reverse; }
  .guide-footer > div { justify-content: flex-end; }
}
</style>
