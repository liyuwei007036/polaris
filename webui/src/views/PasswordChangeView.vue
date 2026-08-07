<script setup>
import { reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, post } from '../api'
import BrandMark from '../components/BrandMark.vue'

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
      <div class="password-card__icon"><BrandMark :size="24" /></div>
      <h1>首次登录需要修改密码</h1>
      <p>当前账户：<strong>{{ username }}</strong></p>
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
        <el-button type="primary" size="large" :loading="loading" @click="save">保存</el-button>
      </el-form>
    </section>
  </div>
</template>

<style scoped>
.password-page { min-height: 100%; display: grid; place-items: center; padding: 24px; }
.password-card {
  width: min(460px, 100%);
  padding: 34px;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  box-shadow: var(--sb-shadow);
}
.password-card__icon {
  width: 46px;
  height: 46px;
  display: grid;
  place-items: center;
  margin-bottom: 18px;
  color: #04121f;
  background: linear-gradient(135deg, var(--sb-accent), var(--sb-accent-2));
  border-radius: 12px;
  font-size: 22px;
  box-shadow: 0 0 24px -6px rgba(56, 189, 248, .85);
}
.password-card h1 { margin: 0 0 8px; color: #fff; font-size: 24px; font-weight: 620; letter-spacing: .4px; }
.password-card p { margin: 0 0 22px; color: var(--sb-muted); }
.password-card .el-button { width: 100%; }
</style>
