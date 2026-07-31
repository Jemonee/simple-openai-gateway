<script setup lang="ts">
import { ref } from 'vue'
import MarkdownIt from 'markdown-it'
import manualSource from '@/manual/pc-user-guide.md?raw'

interface ManualHeading {
  id: string
  level: number
  title: string
}

const headings: ManualHeading[] = []
const markdown = new MarkdownIt({ html: false, linkify: true, typographer: false })
const renderEnvironment = {}
const tokens = markdown.parse(manualSource, renderEnvironment)
let headingIndex = 0

tokens.forEach((token, index) => {
  if (token.type !== 'heading_open') return
  const level = Number(token.tag.slice(1))
  const title = tokens[index + 1]?.content ?? ''
  const id = `manual-section-${headingIndex}`
  token.attrSet('id', id)
  token.attrSet('tabindex', '-1')
  headings.push({ id, level, title })
  headingIndex += 1
})

const firstSectionIndex = tokens.findIndex((token) => token.type === 'heading_open' && token.tag === 'h2')
const headerTokens = firstSectionIndex < 0 ? tokens : tokens.slice(0, firstSectionIndex)
const bodyTokens = firstSectionIndex < 0 ? [] : tokens.slice(firstSectionIndex)
const renderedHeader = markdown.renderer.render(headerTokens, markdown.options, renderEnvironment)
const renderedBody = markdown.renderer.render(bodyTokens, markdown.options, renderEnvironment)
const tableOfContents = headings.filter((heading) => heading.level === 2)
const manualDocument = ref<HTMLElement | null>(null)

function scrollToHeading(id: string) {
  const target = manualDocument.value?.querySelector<HTMLElement>(`#${id}`)
  if (!target) return
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  target.scrollIntoView({ behavior: reduceMotion ? 'auto' : 'smooth', block: 'start' })
  target.focus({ preventScroll: true })
  window.history.replaceState(null, '', `#${id}`)
}
</script>

<template>
  <div class="manual-page">
    <aside class="manual-toc" aria-label="使用手册目录">
      <strong>目录</strong>
      <nav>
        <a v-for="heading in tableOfContents" :key="heading.id" :href="`#${heading.id}`" @click.prevent="scrollToHeading(heading.id)">{{ heading.title }}</a>
      </nav>
    </aside>
    <main class="manual-reading">
      <header class="manual-header manual-markdown" v-html="renderedHeader" />
      <div ref="manualDocument" class="manual-document">
        <article class="manual-body manual-markdown" v-html="renderedBody" />
      </div>
    </main>
  </div>
</template>

<style scoped>
.manual-page { display: grid; height: 100%; min-height: 0; grid-template-columns: 232px minmax(0, 820px); align-items: stretch; justify-content: center; gap: 36px; }
.manual-toc { min-height: 0; overflow: auto; border-left: 2px solid var(--rose-border-strong); padding-left: 14px; scrollbar-gutter: stable; }
.manual-toc > strong { color: var(--rose-text); font-size: 12px; font-weight: 650; }
.manual-toc nav { display: grid; gap: 2px; margin-top: 10px; }
.manual-toc a { padding: 5px 7px; border-radius: var(--rose-radius-control); color: var(--rose-text-muted); font-size: 11px; line-height: 1.35; }
.manual-toc a:hover, .manual-toc a:focus-visible { color: var(--rose-primary); background: var(--rose-primary-soft); }
.manual-reading { display: grid; min-width: 0; min-height: 0; grid-template-rows: auto minmax(0, 1fr); overflow: hidden; border: 1px solid var(--rose-border); background: var(--rose-surface); }
.manual-header { padding: 26px 42px 22px; border-bottom: 1px solid var(--rose-border); background: var(--rose-surface); }
.manual-document { min-width: 0; min-height: 0; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.manual-body { padding: 8px 42px 48px; }
.manual-markdown { color: var(--rose-text-muted); font-size: 13px; line-height: 1.8; }
.manual-markdown :deep(h1) { margin: 0 0 8px; color: var(--rose-text); font-size: 26px; font-weight: 650; line-height: 1.3; }
.manual-markdown :deep(h2) { margin: 26px 0 12px; padding-top: 14px; border-top: 1px solid var(--rose-border); color: var(--rose-text); font-size: 18px; font-weight: 650; line-height: 1.4; scroll-margin-top: 14px; }
.manual-markdown :deep(h3) { margin: 24px 0 8px; color: var(--rose-text); font-size: 14px; font-weight: 650; scroll-margin-top: 14px; }
.manual-markdown :deep(p) { margin: 10px 0; }
.manual-markdown :deep(blockquote) { margin: 16px 0 14px; padding: 10px 14px; border-left: 3px solid var(--rose-primary); color: var(--rose-text-muted); background: var(--rose-surface-muted); }
.manual-markdown :deep(blockquote p) { margin: 0; }
.manual-markdown :deep(ul), .manual-markdown :deep(ol) { margin: 9px 0; padding-left: 24px; }
.manual-markdown :deep(li + li) { margin-top: 4px; }
.manual-markdown :deep(code) { padding: 2px 4px; border: 1px solid var(--rose-border); border-radius: 2px; color: var(--rose-text); background: var(--rose-surface-muted); }
.manual-markdown :deep(strong) { color: var(--rose-text); font-weight: 650; }
.manual-markdown :deep(a) { color: var(--rose-primary); text-decoration: underline; text-underline-offset: 2px; }
@media (max-width: 960px) {
  .manual-page { grid-template-columns: 1fr; grid-template-rows: auto minmax(0, 1fr); justify-content: stretch; gap: 12px; }
  .manual-toc { overflow: hidden; border-left: 0; }
  .manual-toc nav { display: flex; gap: 4px; margin-top: 7px; overflow-x: auto; padding-bottom: 4px; scrollbar-gutter: stable; }
  .manual-toc a { flex: none; border: 1px solid var(--rose-border); background: var(--rose-surface); }
}
@media (max-width: 620px) {
  .manual-header { padding: 20px 18px 16px; }
  .manual-body { padding: 4px 18px 36px; }
  .manual-markdown :deep(h1) { font-size: 22px; }
}
</style>
