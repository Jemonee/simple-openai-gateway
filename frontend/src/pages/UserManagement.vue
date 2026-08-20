<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh } from '@element-plus/icons-vue'
import { request } from '@/utils/api'
import type { AdminUserView } from '@/types/gateway'

const users = ref<AdminUserView[]>([])
const loading = ref(false)
const dialogOpen = ref(false)
const saving = ref(false)
const form = reactive({ username: '', password: '', role: 'user' as AdminUserView['role'] })

function formatDate(value: string): string {
  if (!value) return '--'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(new Date(value))
}

async function loadUsers() {
  loading.value = true
  try {
    users.value = await request<AdminUserView[]>('/admin/auth/users')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '账号列表加载失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  Object.assign(form, { username: '', password: '', role: 'user' })
  dialogOpen.value = true
}

async function createUser() {
  if (!form.username.trim() || form.password.length < 12) {
    ElMessage.warning('请输入用户名和至少 12 位密码')
    return
  }
  saving.value = true
  try {
    await request<AdminUserView>('/admin/auth/users', {
      method: 'POST',
      body: JSON.stringify(form),
    })
    dialogOpen.value = false
    ElMessage.success('账号已创建')
    await loadUsers()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '账号创建失败')
  } finally {
    saving.value = false
  }
}

async function resetPassword(user: AdminUserView) {
  const result = await ElMessageBox.prompt(`为“${user.username}”设置新的登录密码`, '重置密码', {
    inputType: 'password',
    inputPlaceholder: '至少 12 位',
    inputValidator: (value: string) => value.length >= 12 || '密码至少需要 12 位',
    confirmButtonText: '确认重置',
    cancelButtonText: '取消',
  }).catch(() => null)
  if (!result) return
  try {
    await request<null>(`/admin/auth/users/${user.id}/reset-password`, {
      method: 'POST',
      body: JSON.stringify({ newPassword: result.value }),
    })
    ElMessage.success('密码已重置，原登录会话已失效')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '密码重置失败')
  }
}

onMounted(() => { void loadUsers() })
</script>

<template>
  <section class="page-section user-management-page">
    <div class="page-toolbar">
      <div>
        <h1>账号管理</h1>
        <p>创建控制台账号并在需要时重置登录密码。</p>
      </div>
      <div class="toolbar-actions">
        <el-button :icon="Refresh" :loading="loading" @click="loadUsers">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">创建账号</el-button>
      </div>
    </div>

    <div class="table-surface">
      <el-table v-loading="loading" :data="users" stripe row-key="id">
        <el-table-column prop="username" label="用户名" min-width="180" />
        <el-table-column label="角色" width="120">
          <template #default="scope">
            <el-tag :type="scope.row.role === 'admin' ? 'danger' : 'info'">{{ scope.row.role === 'admin' ? '管理员' : '普通账号' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="scope"><el-tag :type="scope.row.enabled ? 'success' : 'info'">{{ scope.row.enabled ? '启用' : '停用' }}</el-tag></template>
        </el-table-column>
        <el-table-column label="创建时间" min-width="180"><template #default="scope">{{ formatDate(scope.row.createdAt) }}</template></el-table-column>
        <el-table-column label="操作" width="140" fixed="right">
          <template #default="scope"><el-button link type="primary" @click="resetPassword(scope.row)">重置密码</el-button></template>
        </el-table-column>
        <template #empty><el-empty description="暂无控制台账号" /></template>
      </el-table>
    </div>

    <el-dialog v-model="dialogOpen" title="创建账号" width="460px" destroy-on-close>
      <el-form label-position="top" @submit.prevent="createUser">
        <el-form-item label="用户名" required><el-input v-model="form.username" autocomplete="username" /></el-form-item>
        <el-form-item label="初始密码" required><el-input v-model="form.password" type="password" show-password autocomplete="new-password" /></el-form-item>
        <el-form-item label="角色"><el-radio-group v-model="form.role"><el-radio value="user">普通账号</el-radio><el-radio value="admin">管理员</el-radio></el-radio-group></el-form-item>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="createUser">创建</el-button></template>
    </el-dialog>
  </section>
</template>

<style scoped>
.user-management-page { display: grid; gap: 16px; }
.page-toolbar { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.page-toolbar h1 { margin: 0; color: var(--hongfen-text); font-size: 22px; }
.page-toolbar p { margin: 6px 0 0; color: var(--hongfen-text-muted); font-size: 13px; }
.toolbar-actions { display: flex; gap: 8px; }
.table-surface { border: 1px solid var(--hongfen-border); border-radius: 6px; background: var(--hongfen-surface); overflow: hidden; }
@media (max-width: 640px) { .page-toolbar { flex-direction: column; } .toolbar-actions { width: 100%; } .toolbar-actions .el-button { flex: 1; } }
</style>
