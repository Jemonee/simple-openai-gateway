import { readonly, ref } from 'vue'
import type { AdminSession } from '@/types/gateway'
import { request } from '@/utils/api'

const checking = ref(true)
const authenticated = ref(false)
const user = ref<AdminSession | null>(null)
let sessionPromise: Promise<void> | null = null

async function checkSession(): Promise<void> {
  try {
    user.value = await request<AdminSession>('/admin/auth/session')
    authenticated.value = true
  } catch {
    user.value = null
    authenticated.value = false
  } finally {
    checking.value = false
  }
}

export function useAdminAuth() {
  function ensureSession(): Promise<void> {
    if (!sessionPromise) {
      sessionPromise = checkSession()
    }
    return sessionPromise
  }

  async function login(username: string, password: string): Promise<void> {
    user.value = await request<AdminSession>('/admin/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    })
    authenticated.value = true
    checking.value = false
  }

  async function logout(): Promise<void> {
    try {
      await request<null>('/admin/auth/logout', { method: 'POST' })
    } finally {
      user.value = null
      authenticated.value = false
    }
  }

  async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
    await request<null>('/admin/auth/password', {
      method: 'PUT',
      body: JSON.stringify({ currentPassword, newPassword }),
    })
    user.value = null
    authenticated.value = false
  }

  return {
    checking: readonly(checking),
    authenticated: readonly(authenticated),
    user: readonly(user),
    ensureSession,
    login,
    logout,
    changePassword,
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('gateway:unauthorized', () => {
    user.value = null
    authenticated.value = false
    checking.value = false
  })
}
