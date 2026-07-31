<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowDown, Expand, Fold, Menu, Search } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import AdminLogin from '@/components/AdminLogin.vue'
import ChangePasswordDialog from '@/components/ChangePasswordDialog.vue'
import FirstLoginGuide from '@/components/FirstLoginGuide.vue'
import SidebarMenuItem from '@/components/SidebarMenuItem.vue'
import { useAdminAuth } from '@/composables/useAdminAuth'
import { useRequestActivity } from '@/composables/useRequestActivity'
import projectMeta from '@/config/project.generated.js'
import { navigationGroups, type NavigationItem } from '@/navigation'

const route = useRoute()
const auth = useAdminAuth()
const { isRequestActive } = useRequestActivity()
const { checking, authenticated, user } = auth
const sidebarCollapsed = ref(false)
const mobileOpen = ref(false)
const isMobileViewport = ref(false)
const searchQuery = ref('')
const passwordDialogOpen = ref(false)
const firstLoginGuideOpen = ref(false)
const logoutLoading = ref(false)
const workspaceScroll = ref<HTMLElement | null>(null)
const firstLoginGuideStorageKey = `${projectMeta.appName}:first-login-guide-complete`
let firstLoginGuideChecked = false
let mobileMediaQuery: MediaQueryList | null = null

function filterNavigationItems(items: NavigationItem[], query: string): NavigationItem[] {
  return items.flatMap((item) => {
    if (item.label.toLowerCase().includes(query)) return [{ ...item }]
    const children = item.children?.length ? filterNavigationItems(item.children, query) : []
    return children.length ? [{ ...item, children }] : []
  })
}

const filteredGroups = computed(() => {
  const query = searchQuery.value.trim().toLowerCase()
  if (!query) return navigationGroups
  return navigationGroups
    .map((group) => ({ ...group, items: filterNavigationItems(group.items, query) }))
    .filter((group) => group.items.length > 0)
})

function collectOpenMenuKeys(items: NavigationItem[], path: string, openAll: boolean, keys: Set<string>): boolean {
  let containsActivePath = false
  items.forEach((item) => {
    const childActive = item.children?.length
      ? collectOpenMenuKeys(item.children, path, openAll, keys)
      : false
    if (item.children?.length && (openAll || childActive)) keys.add(item.key)
    containsActivePath ||= item.path === path || childActive
  })
  return containsActivePath
}

const defaultOpenMenuKeys = computed(() => {
  const keys = new Set<string>()
  const openAll = searchQuery.value.trim().length > 0
  filteredGroups.value.forEach((group) => collectOpenMenuKeys(group.items, route.path, openAll, keys))
  return [...keys]
})

const menuCollapsed = computed(() => sidebarCollapsed.value && !isMobileViewport.value)
const breadcrumbs = computed(() => route.meta.breadcrumbs ?? ['网关控制台'])
const workspaceContained = computed(() => route.meta.workspaceMode === 'contained')

function syncMobileViewport(event: MediaQueryList | MediaQueryListEvent) {
  isMobileViewport.value = event.matches
}

function toggleSidebar() {
  if (isMobileViewport.value) {
    mobileOpen.value = !mobileOpen.value
  } else {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }
}

async function handleAccountCommand(command: string) {
  if (command === 'password') {
    passwordDialogOpen.value = true
    return
  }
	if (command === 'logout') {
		logoutLoading.value = true
		try {
			await auth.logout()
    } catch (error) {
      ElMessage.error(error instanceof Error ? error.message : '退出登录失败')
		} finally {
			logoutLoading.value = false
		}
  }
}

function completeFirstLoginGuide() {
  try {
    window.localStorage.setItem(firstLoginGuideStorageKey, '1')
  } catch {}
}

onMounted(() => {
  void auth.ensureSession()
  mobileMediaQuery = window.matchMedia('(max-width: 960px)')
  syncMobileViewport(mobileMediaQuery)
  mobileMediaQuery.addEventListener('change', syncMobileViewport)
})

onBeforeUnmount(() => {
  mobileMediaQuery?.removeEventListener('change', syncMobileViewport)
})

watch(() => route.path, async () => {
  mobileOpen.value = false
  await nextTick()
  workspaceScroll.value?.scrollTo({ top: 0 })
})

watch(authenticated, (isAuthenticated) => {
  if (!isAuthenticated || firstLoginGuideChecked) return
  firstLoginGuideChecked = true
  try {
    firstLoginGuideOpen.value = window.localStorage.getItem(firstLoginGuideStorageKey) !== '1'
  } catch {
    firstLoginGuideOpen.value = true
  }
}, { immediate: true })
</script>

<template>
  <div v-if="checking" class="auth-loading" aria-live="polite">
    <span class="rose-spinner" aria-hidden="true"></span>
    <span>正在检查管理会话</span>
  </div>

  <AdminLogin v-else-if="!authenticated" />

	<div
    v-else
    class="app-shell"
    :class="{ 'is-sidebar-collapsed': sidebarCollapsed, 'is-mobile-open': mobileOpen }"
	>
		<div v-show="isRequestActive" class="request-progress" role="progressbar" aria-label="接口请求处理中"><span></span></div>
    <a class="skip-link" href="#workspace-content">跳到主内容</a>
    <header class="app-header">
      <div class="header-brand">
        <button class="icon-button header-menu-button" type="button" title="切换导航" @click="toggleSidebar">
          <Menu />
        </button>
        <div class="brand-mark" aria-hidden="true">O</div>
        <div class="header-brand-copy">
          <strong>{{ projectMeta.displayName }}</strong>
          <span>OpenAI Gateway</span>
        </div>
      </div>

      <div class="header-actions">
        <span class="header-status" title="运行状态：网关就绪"><i aria-hidden="true"></i>{{ projectMeta.displayName }}</span>
        <el-dropdown trigger="click" @command="handleAccountCommand">
			<button class="account-button" type="button" :disabled="logoutLoading">
            <span class="account-avatar" aria-hidden="true">{{ user?.username.slice(0, 1).toUpperCase() }}</span>
            <span>{{ user?.username }}</span>
            <ArrowDown />
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="password">修改密码</el-dropdown-item>
				<el-dropdown-item command="logout" divided :disabled="logoutLoading">{{ logoutLoading ? '正在退出' : '退出登录' }}</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </header>

    <aside class="app-sidebar">
      <div class="sidebar-toolbar">
        <label class="sidebar-search">
          <Search />
          <input v-model="searchQuery" aria-label="搜索菜单" placeholder="搜索菜单" />
        </label>
        <button
          class="icon-button sidebar-toggle"
          type="button"
          :title="sidebarCollapsed ? '展开导航' : '收起导航'"
          @click="toggleSidebar"
        >
          <Expand v-if="sidebarCollapsed" />
          <Fold v-else />
        </button>
      </div>

      <nav class="sidebar-nav" aria-label="主导航">
        <div v-if="filteredGroups.length === 0" class="sidebar-empty">未找到菜单</div>
        <section v-for="group in filteredGroups" :key="group.label" class="nav-group">
          <div class="nav-group-label">{{ group.label }}</div>
          <el-menu
            :key="`${group.label}:${searchQuery}:${route.path}`"
            class="sidebar-menu"
            :default-active="route.path"
            :default-openeds="defaultOpenMenuKeys"
            :collapse="menuCollapsed"
            :collapse-transition="false"
            router
          >
            <SidebarMenuItem v-for="item in group.items" :key="item.key" :item="item" />
          </el-menu>
        </section>
      </nav>

      <div class="sidebar-footer">
        <span class="connection-indicator"><i aria-hidden="true"></i><span>单实例 SQLite</span></span>
        <span class="sidebar-footer-version">v{{ projectMeta.version }}</span>
      </div>
    </aside>

    <main class="app-main">
      <div class="workspace-toolbar">
        <div class="breadcrumbs">
          <template v-for="(item, index) in breadcrumbs" :key="`${item}-${index}`">
            <b v-if="index > 0">/</b>
            <strong v-if="index === breadcrumbs.length - 1">{{ item }}</strong>
            <span v-else>{{ item }}</span>
          </template>
        </div>
      </div>
      <section
        ref="workspaceScroll"
        class="workspace-scroll"
        :class="{ 'is-contained': workspaceContained }"
      >
        <div id="workspace-content" class="workspace-content" tabindex="-1">
          <slot />
        </div>
      </section>
    </main>

    <button v-if="mobileOpen" class="mobile-scrim" type="button" aria-label="关闭导航" @click="mobileOpen = false"></button>
    <ChangePasswordDialog v-model="passwordDialogOpen" />
    <FirstLoginGuide
      v-model="firstLoginGuideOpen"
      @complete="completeFirstLoginGuide"
      @skip="completeFirstLoginGuide"
    />
  </div>
</template>

<style scoped>
.request-progress { position: fixed; z-index: 3000; top: 0; right: 0; left: 0; height: 2px; overflow: hidden; pointer-events: none; }
.request-progress span { display: block; width: 38%; height: 100%; background: var(--rose-primary); animation: request-progress-slide 1s ease-in-out infinite; }
.account-button:disabled { cursor: wait; opacity: 0.72; }
@keyframes request-progress-slide { from { transform: translateX(-110%); } to { transform: translateX(360%); } }
@media (prefers-reduced-motion: reduce) { .request-progress span { width: 100%; animation: none; } }
</style>
