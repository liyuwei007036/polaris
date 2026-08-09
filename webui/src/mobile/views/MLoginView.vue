<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { api, setCsrfToken } from '../../api'
import { brandName, brandTagline } from '../../brand'
import BrandMark from '../../components/BrandMark.vue'

const emit = defineEmits(['authenticated'])
const step = ref('credentials')
const loading = ref(false)
const challengeID = ref('')
const credentials = ref({ username: '', password: '' })
const code = ref('')

async function submitCredentials() {
  if (loading.value || !credentials.value.username || !credentials.value.password) return
  loading.value = true
  try {
    const result = await api('/auth/login', { method: 'POST', body: credentials.value })
    if (result.requires_2fa) {
      challengeID.value = result.challenge_id
      step.value = 'mfa'
    } else {
      await finishAuthentication(result)
    }
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    loading.value = false
  }
}

async function submitMFA() {
  if (loading.value || code.value.length !== 6) return
  loading.value = true
  try {
    const result = await api('/auth/mfa', {
      method: 'POST',
      body: { challenge_id: challengeID.value, code: code.value },
      silentUnauthorized: true,
    })
    await finishAuthentication(result)
  } catch (error) {
    ElMessage.error(error.code === 'authentication failed'
      ? '验证码不正确或本次登录已超时，请重新输入验证器当前显示的 6 位动态码'
      : error.message)
  } finally {
    loading.value = false
  }
}

async function finishAuthentication(result) {
  setCsrfToken(result.csrf_token)
  emit('authenticated', await api('/auth/me'))
}
</script>

<template>
  <div class="m-login">
    <header class="m-login__brand">
      <span class="m-login__mark"><BrandMark :size="26" /></span>
      <strong>{{ brandName }}</strong>
      <small>{{ brandTagline }}</small>
    </header>

    <form v-if="step === 'credentials'" class="m-login__form" @submit.prevent="submitCredentials">
      <h1>登录</h1>
      <div class="m-field">
        <label class="m-field__label">用户名</label>
        <el-input v-model="credentials.username" size="large" aria-label="用户名" autocomplete="username" />
      </div>
      <div class="m-field">
        <label class="m-field__label">密码</label>
        <el-input
          v-model="credentials.password"
          size="large"
          type="password"
          show-password
          aria-label="密码"
          autocomplete="current-password"
          @keyup.enter="submitCredentials"
        />
      </div>
      <el-button type="primary" size="large" native-type="submit" :loading="loading">登录</el-button>
    </form>

    <form v-else class="m-login__form" @submit.prevent="submitMFA">
      <h1>两步验证</h1>
      <p class="m-login__sub">输入验证器当前显示的 6 位动态码</p>
      <div class="m-field">
        <label class="m-field__label">动态验证码</label>
        <el-input
          v-model="code"
          size="large"
          maxlength="6"
          inputmode="numeric"
          aria-label="动态验证码"
          placeholder="000000"
          class="m-login__code"
        />
      </div>
      <el-button type="primary" size="large" native-type="submit" :loading="loading">登录</el-button>
      <el-button link class="m-login__back" @click="step = 'credentials'; code = ''">返回</el-button>
    </form>
  </div>
</template>

<style scoped>
.m-login {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 26px;
  padding: 24px 20px calc(24px + var(--m-safe-bottom));
  overflow-y: auto;
}
.m-login__brand { display: flex; flex-direction: column; align-items: center; gap: 8px; }
.m-login__mark {
  width: 52px;
  height: 52px;
  display: grid;
  place-items: center;
  color: #04121f;
  background: linear-gradient(135deg, var(--sb-accent), var(--sb-accent-2));
  border-radius: 15px;
  box-shadow: 0 0 26px -6px rgba(56, 189, 248, .85);
}
.m-login__brand strong { color: #fff; font-size: 20px; letter-spacing: 1px; }
.m-login__brand small { color: var(--sb-muted); font-size: 11px; letter-spacing: 4px; }
.m-login__form {
  padding: 24px 20px 22px;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: 16px;
  box-shadow: var(--sb-shadow);
}
.m-login__form h1 { margin: 0 0 20px; color: #fff; font-size: 21px; font-weight: 620; }
.m-login__sub { margin: -14px 0 20px; color: var(--sb-muted); font-size: 13px; line-height: 1.6; }
.m-login__form :deep(.el-button--primary) { width: 100%; height: 46px; margin-top: 4px; }
.m-login__code :deep(.el-input__inner) { letter-spacing: 8px; text-align: center; font-size: 20px; }
.m-login__back { width: 100%; margin-top: 12px; }
</style>
