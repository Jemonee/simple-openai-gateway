<script setup lang="ts">
import { reactive, ref } from 'vue'
import { Key, User } from '@element-plus/icons-vue'
import { useAdminAuth } from '@/composables/useAdminAuth'
import projectMeta from '@/config/project.generated.js'

const auth = useAdminAuth()
const form = reactive({ username: '', password: '' })
const loading = ref(false)
const errorMessage = ref('')

async function submit() {
  errorMessage.value = ''
  if (!form.username.trim() || !form.password) {
    errorMessage.value = '请输入用户名和密码'
    return
  }
  loading.value = true
  try {
    await auth.login(form.username, form.password)
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
      <header class="login-heading">
        <h1 id="login-title">管理员登录</h1>
        <p>访问渠道、路由、令牌和调用统计</p>
      </header>
      <el-form label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="form.username" autocomplete="username" size="large" @keyup.enter="submit">
            <template #prefix><User /></template>
          </el-input>
        </el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" autocomplete="current-password" show-password size="large" @keyup.enter="submit">
            <template #prefix><Key /></template>
          </el-input>
        </el-form-item>
        <div v-if="errorMessage" class="inline-error" role="alert">{{ errorMessage }}</div>
        <el-button class="login-submit" type="primary" native-type="submit" :loading="loading">登录</el-button>
      </el-form>
    </section>
  </main>
</template>
