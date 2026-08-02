<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { Key } from '@element-plus/icons-vue'
import { api, setCsrfToken } from '../api'

const emit = defineEmits(['authenticated'])
const step = ref('credentials')
const loading = ref(false)
const challengeID = ref('')
const credentials = ref({ email: '', password: '' })
const code = ref('')

async function submitCredentials() {
  if (!credentials.value.email || !credentials.value.password) return
  loading.value = true
  try {
    const result = await api('/auth/login', { method: 'POST', body: credentials.value })
    challengeID.value = result.challenge_id
    step.value = 'mfa'
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    loading.value = false
  }
}

async function submitMFA() {
  if (code.value.length !== 6) return
  loading.value = true
  try {
    const result = await api('/auth/mfa', {
      method: 'POST',
      body: { challenge_id: challengeID.value, code: code.value },
    })
    setCsrfToken(result.csrf_token)
    const session = await api('/auth/me')
    emit('authenticated', session)
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <section class="login-brand">
      <div class="login-brand__logo">
        <span class="login-brand__mark"><Key /></span>
        <span>sb-control</span>
      </div>
      <div class="login-brand__copy">
        <h1>统一管理服务器和客户端连接</h1>
        <p>在一个平台中完成服务器接入、用户配置和访问规则设置。每次修改都会先检查，再自动应用并保留记录。</p>
      </div>
    </section>

    <section class="login-form-wrap">
      <el-form v-if="step === 'credentials'" class="login-form" label-position="top" @submit.prevent="submitCredentials">
        <h2>登录管理平台</h2>
        <div class="login-form__sub">请输入您的管理账户</div>
        <el-form-item label="邮箱">
          <el-input v-model="credentials.email" size="large" autocomplete="username" placeholder="admin@example.com" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="credentials.password"
            size="large"
            type="password"
            show-password
            autocomplete="current-password"
            placeholder="请输入密码"
            @keyup.enter="submitCredentials"
          />
        </el-form-item>
        <el-button type="primary" size="large" :loading="loading" @click="submitCredentials">继续</el-button>
      </el-form>

      <el-form v-else class="login-form" label-position="top" @submit.prevent="submitMFA">
        <h2>两步验证</h2>
        <div class="login-form__sub">请输入验证器应用当前显示的 6 位数字</div>
        <el-form-item label="动态验证码">
          <el-input
            v-model="code"
            size="large"
            maxlength="6"
            inputmode="numeric"
            placeholder="000000"
            @keyup.enter="submitMFA"
          />
        </el-form-item>
        <el-button type="primary" size="large" :loading="loading" @click="submitMFA">登录</el-button>
        <div class="login-form__back">
          <el-button link @click="step = 'credentials'; code = ''">返回重新输入账户</el-button>
        </div>
      </el-form>
    </section>
  </div>
</template>
