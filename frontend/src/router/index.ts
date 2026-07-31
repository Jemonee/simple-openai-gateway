import { createRouter, createWebHistory } from 'vue-router'
import { navigationRoutes } from '@/navigation'

const router = createRouter({
  history: createWebHistory('/static/'),
  routes: navigationRoutes,
})

export default router
