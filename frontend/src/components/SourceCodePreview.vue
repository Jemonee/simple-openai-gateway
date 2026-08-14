<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { CopyDocument } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import * as monaco from 'monaco-editor/esm/vs/editor/editor.api'
import 'monaco-editor/esm/vs/basic-languages/cpp/cpp.contribution'
import 'monaco-editor/esm/vs/basic-languages/csharp/csharp.contribution'
import 'monaco-editor/esm/vs/basic-languages/css/css.contribution'
import 'monaco-editor/esm/vs/basic-languages/dart/dart.contribution'
import 'monaco-editor/esm/vs/basic-languages/dockerfile/dockerfile.contribution'
import 'monaco-editor/esm/vs/basic-languages/go/go.contribution'
import 'monaco-editor/esm/vs/basic-languages/html/html.contribution'
import 'monaco-editor/esm/vs/basic-languages/java/java.contribution'
import 'monaco-editor/esm/vs/basic-languages/javascript/javascript.contribution'
import 'monaco-editor/esm/vs/basic-languages/kotlin/kotlin.contribution'
import 'monaco-editor/esm/vs/basic-languages/less/less.contribution'
import 'monaco-editor/esm/vs/basic-languages/lua/lua.contribution'
import 'monaco-editor/esm/vs/basic-languages/markdown/markdown.contribution'
import 'monaco-editor/esm/vs/basic-languages/php/php.contribution'
import 'monaco-editor/esm/vs/basic-languages/python/python.contribution'
import 'monaco-editor/esm/vs/basic-languages/ruby/ruby.contribution'
import 'monaco-editor/esm/vs/basic-languages/rust/rust.contribution'
import 'monaco-editor/esm/vs/basic-languages/scala/scala.contribution'
import 'monaco-editor/esm/vs/basic-languages/scss/scss.contribution'
import 'monaco-editor/esm/vs/basic-languages/shell/shell.contribution'
import 'monaco-editor/esm/vs/basic-languages/sql/sql.contribution'
import 'monaco-editor/esm/vs/basic-languages/swift/swift.contribution'
import 'monaco-editor/esm/vs/basic-languages/typescript/typescript.contribution'
import 'monaco-editor/esm/vs/basic-languages/xml/xml.contribution'
import 'monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution'
import { sourceCodeLanguageLabel } from '@/utils/codeLanguage'

interface SourceCodePreviewProps {
  /** Bounded source text rendered in the preview. */
  code: string
  /** Monaco language identifier used for syntax tokenization. */
  language: string
  /** Accessible label describing the source resource. */
  label?: string
}

const { code, language, label = '源码预览' } = defineProps<SourceCodePreviewProps>()
const highlightedHTML = ref('')
const isDarkMode = ref(document.documentElement.classList.contains('dark'))
let renderVersion = 0
let colorSchemeQuery: MediaQueryList | undefined

const languageLabel = computed(() => sourceCodeLanguageLabel(language))
const sourceSummary = computed(() => `${code.split(/\r?\n/).length} 行 · ${code.length.toLocaleString('zh-CN')} 字符`)
const tokenizerLanguage = computed(() => language === 'json' ? 'javascript' : language)

function syncColorScheme(event: MediaQueryListEvent) {
  isDarkMode.value = event.matches
}

watch([() => code, tokenizerLanguage, isDarkMode], async ([nextCode, nextLanguage, nextIsDarkMode]) => {
  const version = ++renderVersion
  highlightedHTML.value = ''
  try {
    monaco.editor.setTheme(nextIsDarkMode ? 'vs-dark' : 'vs')
    const html = await monaco.editor.colorize(nextCode, nextLanguage || 'plaintext', { tabSize: 2 })
    if (version === renderVersion) highlightedHTML.value = html
  } catch {
    if (version === renderVersion) highlightedHTML.value = ''
  }
}, { immediate: true })

onMounted(() => {
  colorSchemeQuery = window.matchMedia('(prefers-color-scheme: dark)')
  isDarkMode.value = colorSchemeQuery.matches
  colorSchemeQuery.addEventListener('change', syncColorScheme)
})

onBeforeUnmount(() => colorSchemeQuery?.removeEventListener('change', syncColorScheme))

async function copySource() {
  try {
    await navigator.clipboard.writeText(code)
    ElMessage.success('源码预览已复制')
  } catch {
    ElMessage.error('无法访问剪贴板')
  }
}
</script>

<template>
  <section class="source-preview" :aria-label="label">
    <header class="source-preview-toolbar">
      <div><strong>{{ languageLabel }}</strong><span>{{ sourceSummary }}</span></div>
      <el-tooltip content="复制源码预览" placement="top">
        <el-button :icon="CopyDocument" aria-label="复制源码预览" @click="copySource" />
      </el-tooltip>
    </header>
    <pre class="source-preview-code"><code v-if="highlightedHTML" v-html="highlightedHTML"></code><code v-else>{{ code }}</code></pre>
  </section>
</template>

<style scoped>
.source-preview { display: flex; width: 100%; min-width: 0; max-width: 100%; max-height: 100%; flex: 1 1 auto; flex-direction: column; overflow: hidden; border: 1px solid var(--hongfen-border); background: var(--hongfen-surface); box-sizing: border-box; }
.source-preview-toolbar { display: flex; width: 100%; min-width: 0; min-height: 36px; flex: 0 0 auto; align-items: center; justify-content: space-between; gap: 10px; padding: 4px 5px 4px 10px; border-bottom: 1px solid var(--hongfen-border); background: var(--hongfen-surface-muted); box-sizing: border-box; }
.source-preview-toolbar > div { display: flex; min-width: 0; align-items: baseline; gap: 8px; }
.source-preview-toolbar strong { color: var(--hongfen-text); font: 650 11px/1 var(--hongfen-font-mono); }
.source-preview-toolbar span { color: var(--hongfen-text-subtle); font-size: 10px; white-space: nowrap; }
.source-preview-toolbar .el-button { width: 28px; height: 28px; flex: 0 0 auto; padding: 0; }
.source-preview-code { width: 100%; min-width: 0; max-width: 100%; min-height: 90px; margin: 0; padding: 10px 12px; overflow: auto; color: var(--hongfen-text); font: 12px/1.55 var(--hongfen-font-mono); tab-size: 2; white-space: pre; box-sizing: border-box; }
.source-preview-code code { display: block; width: max-content; min-width: 100%; font: inherit; }
@media (max-width: 640px) {
  .source-preview-toolbar > div { align-items: flex-start; flex-direction: column; gap: 3px; }
}
</style>
