<script setup lang="ts">
import { reactive, ref } from 'vue'
import { Key, User } from '@element-plus/icons-vue'
import { useRoute, useRouter } from 'vue-router'
import { useAdminAuth } from '@/composables/useAdminAuth'
import projectMeta from '@/config/project.generated.js'
import { preserveHeadlessQuery } from '@/utils/headlessMode'

const auth = useAdminAuth()
const route = useRoute()
const router = useRouter()
const loginType = ref<'admin' | 'access_token'>('admin')
const adminForm = reactive({ username: '', password: '' })
const tokenForm = reactive({ accessToken: '' })
const loading = ref(false)
const errorMessage = ref('')

async function submit() {
  errorMessage.value = ''
  if (loginType.value === 'admin' && (!adminForm.username.trim() || !adminForm.password)) {
    errorMessage.value = '请输入用户名和密码'
    return
  }
  if (loginType.value === 'access_token' && !tokenForm.accessToken.trim()) {
    errorMessage.value = '请输入访问令牌'
    return
  }
  loading.value = true
  try {
    if (loginType.value === 'admin') {
      await auth.login(adminForm.username, adminForm.password)
    } else {
      await auth.loginWithAccessToken(tokenForm.accessToken.trim())
      if (route.meta.adminOnly) {
        await router.replace({ path: '/', query: preserveHeadlessQuery(route.query.headless) })
      }
    }
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <main class="login-page">
    <section class="login-panel" aria-labelledby="login-title">
      <div class="login-brand">
        <span class="brand-mark" aria-hidden="true">O</span>
        <div>
          <strong>{{ projectMeta.displayName }}</strong>
          <span>OpenAI Gateway</span>
        </div>
      </div>
      <header class="login-heading"><h1 id="login-title">控制台登录</h1></header>
      <el-form label-position="top" @submit.prevent="submit">
        <el-tabs v-model="loginType" stretch>
          <el-tab-pane label="管理员登录" name="admin">
            <el-form-item label="用户名">
              <el-input v-model="adminForm.username" autocomplete="username" size="large" @keyup.enter="submit">
                <template #prefix><User /></template>
              </el-input>
            </el-form-item>
            <el-form-item label="密码">
              <el-input v-model="adminForm.password" type="password" autocomplete="current-password" show-password size="large" @keyup.enter="submit">
                <template #prefix><Key /></template>
              </el-input>
            </el-form-item>
          </el-tab-pane>
          <el-tab-pane label="访问令牌登录" name="access_token">
            <el-form-item label="访问令牌">
              <el-input v-model="tokenForm.accessToken" type="password" autocomplete="off" show-password size="large" @keyup.enter="submit">
                <template #prefix><Key /></template>
              </el-input>
            </el-form-item>
          </el-tab-pane>
        </el-tabs>
        <div v-if="errorMessage" class="inline-error" role="alert">{{ errorMessage }}</div>
        <el-button class="login-submit" type="primary" native-type="submit" :loading="loading">登录</el-button>
      </el-form>
    </section>
  </main>
</template>
