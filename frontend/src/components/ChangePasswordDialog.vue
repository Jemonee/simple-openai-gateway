<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { useAdminAuth } from '@/composables/useAdminAuth'

interface ChangePasswordDialogProps {
  /** Whether the administrator password dialog is visible. */
  modelValue: boolean
}

const props = defineProps<ChangePasswordDialogProps>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const auth = useAdminAuth()
const saving = ref(false)
const errorMessage = ref('')
const form = reactive({ currentPassword: '', newPassword: '', confirmPassword: '' })

watch(() => props.modelValue, (visible) => {
  if (!visible) return
  form.currentPassword = ''
  form.newPassword = ''
  form.confirmPassword = ''
  errorMessage.value = ''
})

async function save() {
  errorMessage.value = ''
  if (form.newPassword.length < 12) {
    errorMessage.value = '新密码至少需要 12 个字符'
    return
  }
  if (form.newPassword !== form.confirmPassword) {
    errorMessage.value = '两次输入的新密码不一致'
    return
  }
  saving.value = true
  try {
    await auth.changePassword(form.currentPassword, form.newPassword)
    emit('update:modelValue', false)
    ElMessage.success('密码已修改，请重新登录')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : '密码修改失败'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="修改管理员密码"
    width="min(460px, calc(100vw - 32px))"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <el-form label-position="top" @submit.prevent="save">
      <el-form-item label="当前密码">
        <el-input v-model="form.currentPassword" type="password" autocomplete="current-password" show-password />
      </el-form-item>
      <el-form-item label="新密码">
        <el-input v-model="form.newPassword" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <el-form-item label="确认新密码">
        <el-input v-model="form.confirmPassword" type="password" autocomplete="new-password" show-password />
      </el-form-item>
      <div v-if="errorMessage" class="inline-error" role="alert">{{ errorMessage }}</div>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">保存并退出</el-button>
    </template>
  </el-dialog>
</template>
