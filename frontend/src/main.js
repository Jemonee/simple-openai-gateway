import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'
import './assets/main.css'

import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import App from './App.vue'
import router from './router/index.ts'
import projectMeta from './config/project.generated.js'

const colorSchemeQuery = window.matchMedia('(prefers-color-scheme: dark)')
function syncColorScheme(event) {
  document.documentElement.classList.toggle('dark', event.matches)
}

syncColorScheme(colorSchemeQuery)
colorSchemeQuery.addEventListener('change', syncColorScheme)

document.title = projectMeta.displayName
const faviconPath = projectMeta.faviconPath.replace(/^\/+/, '')
const faviconUrl = `${import.meta.env.BASE_URL}${faviconPath}`
document.querySelectorAll('[data-project-favicon]').forEach((link) => {
  link.setAttribute('href', faviconUrl)
})

createApp(App)
    .use(ElementPlus, { locale: zhCn })
    .use(router)
    .mount('#app')
