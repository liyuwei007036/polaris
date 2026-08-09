<script setup>
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, post } from '../../api'
import BrandMark from '../../components/BrandMark.vue'

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
    await post('/auth/password', { current_password: form.current_password, new_password: form.new_password })
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
  <div class="m-first">
    <div class="m-first__icon"><BrandMark :size="24" /></div>
    <h1>首次登录需要修改密码</h1>
    <p>当前账户：<strong>{{ username }}</strong></p>
    <form @submit.prevent="save">
      <div class="m-field">
        <label class="m-field__label">当前密码</label>
        <el-input v-model="form.current_password" type="password" show-password aria-label="当前密码" autocomplete="current-password" placeholder="请输入当前密码" />
      </div>
      <div class="m-field">
        <label class="m-field__label">新密码</label>
        <el-input v-model="form.new_password" type="password" show-password aria-label="新密码" autocomplete="new-password" placeholder="至少 12 位，不能与当前密码相同" />
      </div>
      <div class="m-field">
        <label class="m-field__label">再次输入新密码</label>
        <el-input v-model="form.confirm_password" type="password" show-password aria-label="再次输入新密码" autocomplete="new-password" placeholder="请再次输入新密码" @keyup.enter="save" />
      </div>
      <el-button type="primary" size="large" native-type="submit" :loading="loading">保存</el-button>
    </form>
  </div>
</template>

<style scoped>
.m-first {
  width: 100%;
  height: 100%;
  padding: 28px 20px calc(28px + var(--m-safe-bottom));
  padding-top: calc(28px + var(--m-safe-top));
  overflow-y: auto;
}
.m-first__icon {
  width: 46px;
  height: 46px;
  display: grid;
  place-items: center;
  margin-bottom: 16px;
  color: #04121f;
  background: linear-gradient(135deg, var(--sb-accent), var(--sb-accent-2));
  border-radius: 12px;
  box-shadow: 0 0 24px -6px rgba(56, 189, 248, .85);
}
.m-first h1 { margin: 0 0 8px; color: #fff; font-size: 21px; font-weight: 620; line-height: 1.35; }
.m-first p { margin: 0 0 22px; color: var(--sb-muted); font-size: 13.5px; }
.m-first :deep(.el-button--primary) { width: 100%; height: 46px; margin-top: 4px; }
</style>
