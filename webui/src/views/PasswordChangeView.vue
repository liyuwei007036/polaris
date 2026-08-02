<script setup>
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Key } from '@element-plus/icons-vue'
import { api, post } from '../api'

defineProps({ username: { type: String, required: true } })
const emit = defineEmits(['changed'])
const loading = ref(false)
const form = reactive({ current_password: '', new_password: '', confirm_password: '' })

async function save() {
  if (!form.current_password) return ElMessage.error('请输入当前密码')
  if (form.new_password.length < 12) return ElMessage.error('新密码至少需要 12 位')
  if (form.new_password === '123456') return ElMessage.error('新密码不能继续使用初始密码')
  if (form.new_password !== form.confirm_password) return ElMessage.error('两次输入的新密码不一致')
  loading.value = true
  try {
    await post('/auth/password', {
      current_password: form.current_password,
      new_password: form.new_password,
    })
    const session = await api('/auth/me')
    ElMessage.success('密码已修改')
    emit('changed', session)
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="password-page">
    <section class="password-card">
      <div class="password-card__icon"><Key /></div>
      <h1>首次登录需要修改密码</h1>
      <p>当前账户：<strong>{{ username }}</strong></p>
      <el-alert title="修改完成前，不能进入管理平台或调用其他管理功能。" type="warning" show-icon :closable="false" />
      <el-form label-position="top" @submit.prevent="save">
        <el-form-item label="当前密码">
          <el-input v-model="form.current_password" type="password" show-password autocomplete="current-password" placeholder="请输入当前密码" />
        </el-form-item>
        <el-form-item label="新密码">
          <el-input v-model="form.new_password" type="password" show-password autocomplete="new-password" placeholder="至少 12 位，不能与当前密码相同" />
        </el-form-item>
        <el-form-item label="再次输入新密码">
          <el-input v-model="form.confirm_password" type="password" show-password autocomplete="new-password" placeholder="请再次输入新密码" @keyup.enter="save" />
        </el-form-item>
        <el-button type="primary" size="large" :loading="loading" @click="save">保存新密码并进入平台</el-button>
      </el-form>
    </section>
  </div>
</template>

<style scoped>
.password-page { min-height: 100%; display: grid; place-items: center; padding: 24px; background: #f4f6f9; }
.password-card { width: min(460px, 100%); padding: 34px; background: #fff; border: 1px solid var(--sb-border); border-radius: 12px; box-shadow: 0 18px 48px rgba(15,23,42,.08); }
.password-card__icon { width: 46px; height: 46px; display: grid; place-items: center; margin-bottom: 18px; color: #fff; background: var(--sb-accent); border-radius: 10px; font-size: 22px; }
.password-card h1 { margin: 0 0 8px; color: var(--sb-text); font-size: 24px; }
.password-card p { margin: 0 0 20px; color: var(--sb-muted); }
.password-card .el-alert { margin-bottom: 22px; }
.password-card .el-button { width: 100%; }
</style>
