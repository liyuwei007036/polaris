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
    <div class="m-first__card">
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
  </div>
</template>

<style scoped>
.m-first {
  width: 100%;
  height: 100%;
  padding: 24px 18px calc(24px + var(--m-safe-bottom));
  padding-top: calc(24px + var(--m-safe-top));
  background: var(--sb-bg);
  display: flex;
  flex-direction: column;
  justify-content: center;
  overflow-y: auto;
}
.m-first__card {
  padding: 26px 20px 24px;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line-strong);
  border-radius: var(--m-radius);
  box-shadow: var(--sb-shadow);
}
.m-first__icon {
  width: 48px;
  height: 48px;
  display: grid;
  place-items: center;
  margin-bottom: 16px;
  border-radius: 13px;
  transition: all 0.2s ease;
}
html.dark .m-first__icon {
  color: #0E1015;
  background: linear-gradient(135deg, #F8FAFC, #CBD5E1);
  box-shadow: 0 0 22px -4px rgba(248, 250, 252, 0.4);
}
html.light .m-first__icon,
html:not(.dark) .m-first__icon {
  color: #FFFFFF;
  background: linear-gradient(135deg, #0F172A, #334155);
  box-shadow: 0 6px 18px -4px rgba(15, 23, 42, 0.3);
}
.m-first h1 {
  margin: 0 0 8px;
  color: var(--sb-text);
  font-size: 20px;
  font-weight: 650;
  line-height: 1.35;
  letter-spacing: .2px;
}
.m-first p {
  margin: 0 0 20px;
  color: var(--sb-muted);
  font-size: 13px;
}
.m-first p strong {
  color: var(--sb-text);
}
.m-first :deep(.el-button--primary) {
  width: 100%;
  height: var(--m-tap);
  margin-top: 8px;
  font-weight: 600;
}
</style>
