<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, useTemplateRef, watch } from 'vue'
import { CopyDocument } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/language/json/monaco.contribution'
import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'

interface JsonCodeViewerProps {
  /** JSON or plain-text payload rendered in the read-only source viewer. */
  value: string
  /** Stable viewer height used inside the payload dialog. */
  height?: string
}

interface MonacoEnvironmentConfig {
  getWorker: (_moduleId: string, label: string) => Worker
}

type ViewerMode = 'formatted' | 'raw'

interface PreparedPayload {
  formatted: string
  formatLabel: string
  language: 'json' | 'plaintext'
  summary: string
}

const { value, height = '420px' } = defineProps<JsonCodeViewerProps>()
const container = useTemplateRef<HTMLDivElement>('container')
const viewerMode = ref<ViewerMode>('formatted')
const wrapLines = ref(true)
const viewerModeOptions: Array<{ label: string; value: ViewerMode }> = [
  { label: '格式化', value: 'formatted' },
  { label: '原文', value: 'raw' },
]
let editor: monaco.editor.IStandaloneCodeEditor | undefined

function tryFormatJSON(source: string): string | null {
  try {
    return JSON.stringify(JSON.parse(source), null, 2)
  } catch {
    return null
  }
}

function formatSSE(source: string): { value: string; eventCount: number } {
  let namedEventCount = 0
  let dataEventCount = 0
  const formattedLines = source.split(/\r?\n/).flatMap((line) => {
    if (line.startsWith('event:')) namedEventCount += 1
    if (!line.startsWith('data:')) return [line]
    dataEventCount += 1
    const data = line.slice(5).trim()
    if (!data || data === '[DONE]') return [line]
    const formatted = tryFormatJSON(data)
    if (!formatted) return [line]
    return ['data:', ...formatted.split('\n').map((jsonLine) => `  ${jsonLine}`)]
  })
  return { value: formattedLines.join('\n'), eventCount: Math.max(namedEventCount, dataEventCount) }
}

function formatJSONLines(source: string): { value: string; recordCount: number } | null {
  const lines = source.split(/\r?\n/).filter((line) => line.trim())
  if (lines.length < 2) return null
  const formatted = lines.map((line) => tryFormatJSON(line.trim()))
  if (formatted.some((line) => line === null)) return null
  return { value: formatted.join('\n\n'), recordCount: lines.length }
}

function preparePayload(source: string): PreparedPayload {
  const trimmed = source.trim()
  if (!trimmed) return { formatted: '', formatLabel: '空内容', language: 'plaintext', summary: '0 字符' }
  const json = tryFormatJSON(trimmed)
  if (json) {
    return { formatted: json, formatLabel: 'JSON', language: 'json', summary: `${source.length.toLocaleString('zh-CN')} 字符` }
  }
  if (/^\s*(?:event:|data:)/m.test(source)) {
    const sse = formatSSE(source)
    return { formatted: sse.value, formatLabel: 'SSE 流', language: 'plaintext', summary: `${sse.eventCount} 个事件` }
  }
  const jsonLines = formatJSONLines(source)
  if (jsonLines) {
    return { formatted: jsonLines.value, formatLabel: 'JSON Lines', language: 'plaintext', summary: `${jsonLines.recordCount} 条记录` }
  }
  const lineCount = source.split(/\r?\n/).length
  return { formatted: source, formatLabel: '纯文本', language: 'plaintext', summary: `${lineCount} 行` }
}

const preparedPayload = computed(() => preparePayload(value))
const editorValue = computed(() => viewerMode.value === 'raw' ? value : preparedPayload.value.formatted)
const editorLanguage = computed(() => viewerMode.value === 'raw' && preparedPayload.value.language !== 'json' ? 'plaintext' : preparedPayload.value.language)

const monacoGlobal = globalThis as typeof globalThis & { MonacoEnvironment?: MonacoEnvironmentConfig }
monacoGlobal.MonacoEnvironment ??= {
  getWorker: (_moduleId, label) => label === 'json' ? new JsonWorker() : new EditorWorker(),
}

onMounted(() => {
  if (!container.value) return
  editor = monaco.editor.create(container.value, {
    value: editorValue.value,
    language: editorLanguage.value,
    theme: 'vs',
    readOnly: true,
    automaticLayout: true,
    folding: true,
    foldingHighlight: true,
    showFoldingControls: 'always',
    bracketPairColorization: { enabled: true },
    guides: { bracketPairs: true, indentation: true },
    minimap: { enabled: false },
    scrollBeyondLastLine: false,
    wordWrap: wrapLines.value ? 'on' : 'off',
    lineNumbersMinChars: 3,
    renderLineHighlight: 'none',
    padding: { top: 10, bottom: 10 },
    fontSize: 12,
    tabSize: 2,
  })
})

watch([editorValue, editorLanguage], ([nextValue, nextLanguage]) => {
  if (!editor) return
  if (editor.getValue() !== nextValue) editor.setValue(nextValue)
  const model = editor.getModel()
  if (model && model.getLanguageId() !== nextLanguage) monaco.editor.setModelLanguage(model, nextLanguage)
})

watch(wrapLines, (enabled) => editor?.updateOptions({ wordWrap: enabled ? 'on' : 'off' }))

async function copyCurrentValue() {
  try {
    await navigator.clipboard.writeText(editorValue.value)
    ElMessage.success('响应内容已复制')
  } catch {
    ElMessage.error('无法访问剪贴板')
  }
}

onBeforeUnmount(() => editor?.dispose())
</script>

<template>
  <section class="code-viewer-shell" :aria-label="`${preparedPayload.formatLabel} 响应内容`">
    <header class="code-viewer-toolbar">
      <div class="payload-format">
        <strong>{{ preparedPayload.formatLabel }}</strong>
        <span>{{ preparedPayload.summary }}</span>
      </div>
      <div class="viewer-actions">
        <el-checkbox v-model="wrapLines" size="small">自动换行</el-checkbox>
        <el-segmented v-model="viewerMode" :options="viewerModeOptions" size="small" aria-label="响应内容显示方式" />
        <el-tooltip content="复制当前内容" placement="top">
          <el-button :icon="CopyDocument" aria-label="复制当前响应内容" @click="copyCurrentValue" />
        </el-tooltip>
      </div>
    </header>
    <div ref="container" class="json-code-viewer" :style="{ height }" />
  </section>
</template>

<style scoped>
.code-viewer-shell { width: 100%; min-width: 0; overflow: hidden; border: 1px solid var(--rose-border); background: var(--rose-surface); }
.code-viewer-toolbar { min-height: 42px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 6px 8px 6px 12px; border-bottom: 1px solid var(--rose-border); background: var(--rose-surface-muted); }
.payload-format { display: flex; align-items: baseline; gap: 9px; min-width: 0; }
.payload-format strong { color: var(--rose-text); font: 650 11px/1 var(--rose-font-mono); }
.payload-format span { color: var(--rose-text-subtle); font-size: 10px; white-space: nowrap; }
.viewer-actions { display: flex; align-items: center; gap: 8px; }
.viewer-actions .el-button { width: 32px; height: 32px; padding: 0; }
.json-code-viewer { width: 100%; min-height: 220px; overflow: hidden; background: var(--rose-surface-muted); }
@media (max-width: 640px) {
  .code-viewer-toolbar { align-items: stretch; flex-direction: column; }
  .viewer-actions { justify-content: flex-end; }
}
</style>
