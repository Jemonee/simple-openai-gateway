import { execFileSync } from 'node:child_process'
import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import vueDevTools from 'vite-plugin-vue-devtools'
import projectMeta from './src/config/project.generated.js'

function escapeHtmlText(value) {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

function projectMetadataPlugin() {
  return {
    name: 'project-metadata',
    transformIndexHtml: {
      order: 'pre',
      handler(html) {
        return html.replace('__PROJECT_DISPLAY_NAME__', escapeHtmlText(projectMeta.displayName))
      },
    },
  }
}

function resolveBuildBranch() {
  const environmentBranch = (
    process.env.GATEWAY_BUILD_BRANCH
    || process.env.GITHUB_HEAD_REF
    || process.env.GITHUB_REF_NAME
    || ''
  ).trim()
  if (environmentBranch) return environmentBranch

  try {
    return execFileSync('git', ['branch', '--show-current'], { encoding: 'utf8' }).trim() || 'detached HEAD'
  } catch {
    return 'unknown'
  }
}

// https://vite.dev/config/
export default defineConfig({
  base: '/static/',
  define: {
    __BUILD_BRANCH__: JSON.stringify(resolveBuildBranch()),
  },
  plugins: [
    projectMetadataPlugin(),
    vue(),
    vueDevTools(),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    },
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8888',
    },
  },
})
