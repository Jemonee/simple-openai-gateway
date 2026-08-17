import { createRouter, createWebHistory } from 'vue-router'
import { useAdminAuth } from '@/composables/useAdminAuth'
import { navigationRoutes } from '@/navigation'
import { preserveHeadlessQuery } from '@/utils/headlessMode'

const router = createRouter({
  history: createWebHistory('/static/'),
  routes: navigationRoutes,
})

router.beforeEach(async (to) => {
  const auth = useAdminAuth()
  await auth.ensureSession()
  if (auth.user.value?.loginType === 'access_token' && to.meta.adminOnly) {
    return { path: '/', query: preserveHeadlessQuery(to.query.headless) }
  }
  return true
})

export default router
