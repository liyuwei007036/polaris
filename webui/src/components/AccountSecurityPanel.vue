<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import QRCode from 'qrcode'
import { api, post, setCsrfToken } from '../api'

defineProps({
  // 手机端的抽屉里字号和二维码尺寸都要收紧一点。
  mobile: { type: Boolean, default: false },
})

const appState = inject('appState')
const loading = ref(false)
const enabling = ref(false)
const setup = reactive({ open: false, secret: '', qr: '', code: '' })

async function refresh() {
  // 两步验证绑定在当前登录账户上，打开面板时重新读取一次，避免沿用登录时的旧状态。
  const session = await api('/auth/me').catch(() => null)
  if (!session) return
  setCsrfToken(session.csrf_token)
  appState.totp_enabled = Boolean(session.totp_enabled)
}

async function beginSetup() {
  loading.value = true
  try {
    const result = await post('/auth/2fa/setup', {})
    setup.secret = result.secret
    setup.qr = await QRCode.toDataURL(result.otpauth_uri, { width: 220, margin: 1 })
    setup.code = ''
    setup.open = true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '生成验证器密钥失败')
  } finally {
    loading.value = false
  }
}

async function enable() {
  if (enabling.value) return
  if (!/^\d{6}$/.test(setup.code)) return ElMessage.error('请输入验证器显示的 6 位数字')
  enabling.value = true
  try {
    await api('/auth/2fa/enable', { method: 'POST', body: { code: setup.code }, silentUnauthorized: true })
    appState.totp_enabled = true
    setup.open = false
    ElMessage.success('两步验证已启用，后续登录需要输入动态验证码')
  } catch (error) {
    ElMessage.error(error.code === 'authentication failed'
      ? '验证码不正确，请输入验证器当前显示的 6 位动态码'
      : error.message)
  } finally {
    enabling.value = false
  }
}

async function disable() {
  let result
  try {
    result = await ElMessageBox.prompt('请输入当前登录密码以确认关闭', '关闭两步验证', {
      inputType: 'password',
      inputValidator: (value) => Boolean(value) || '请输入当前密码',
    })
  } catch {
    return
  }
  try {
    await post('/auth/2fa/disable', { password: result.value })
    appState.totp_enabled = false
    setup.open = false
    ElMessage.success('两步验证已关闭')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '关闭两步验证失败')
  }
}

onMounted(refresh)
</script>

<template>
  <div class="account-security" :class="{ 'is-mobile': mobile }">
    <header class="account-security__head">
      <div>
        <h3>两步验证</h3>
        <p v-if="appState.totp_enabled">当前账户 <strong>{{ appState.username }}</strong> 登录时需要输入验证器生成的动态验证码。</p>
        <p v-else>为当前账户 <strong>{{ appState.username }}</strong> 增加一层动态验证码保护。</p>
      </div>
      <el-tag :type="appState.totp_enabled ? 'success' : 'info'">{{ appState.totp_enabled ? '已启用' : '未启用' }}</el-tag>
    </header>

    <div class="account-security__actions">
      <el-button v-if="appState.totp_enabled" type="danger" plain @click="disable">关闭两步验证</el-button>
      <el-button v-else type="primary" :loading="loading" @click="beginSetup">启用两步验证</el-button>
    </div>

    <section v-if="setup.open" class="account-security__setup">
      <p>扫描二维码后，输入验证器显示的 6 位动态码完成绑定。</p>
      <div class="account-security__qr"><img :src="setup.qr" alt="两步验证绑定二维码" data-testid="totp-qr" /></div>
      <el-form label-position="top" @submit.prevent="enable">
        <el-form-item label="无法扫码时，可手动输入此密钥">
          <el-input :model-value="setup.secret" readonly class="mono" data-testid="totp-secret" />
        </el-form-item>
        <el-form-item label="动态验证码">
          <el-input v-model="setup.code" maxlength="6" inputmode="numeric" placeholder="000000" />
        </el-form-item>
      </el-form>
      <div class="account-security__setup-actions">
        <el-button @click="setup.open = false">取消</el-button>
        <el-button type="primary" :loading="enabling" @click="enable">启用</el-button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.account-security { display: flex; flex-direction: column; gap: 16px; }
.account-security__head { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
.account-security__head > div { min-width: 0; }
.account-security__head h3 { margin: 0 0 7px; color: var(--sb-text); font-size: 15px; font-weight: 620; }
.account-security__head p { margin: 0; color: var(--sb-muted); line-height: 1.7; }
.account-security__head strong { color: var(--sb-text); }
.account-security__actions { display: flex; justify-content: flex-end; }
.account-security__setup { padding-top: 16px; border-top: 1px solid var(--sb-line); text-align: center; }
.account-security__setup > p { margin: 0 0 14px; color: var(--sb-muted); line-height: 1.7; text-align: left; }
.account-security__qr { display: inline-block; padding: 8px; margin-bottom: 14px; background: #fff; border-radius: 10px; line-height: 0; }
.account-security__qr img { display: block; width: 200px; height: 200px; }
.account-security__setup :deep(.el-form-item) { text-align: left; }
.account-security__setup-actions { display: flex; justify-content: flex-end; gap: 10px; margin-top: 4px; }

.account-security.is-mobile { gap: 14px; }
.account-security.is-mobile .account-security__head { flex-direction: column; gap: 8px; }
.account-security.is-mobile .account-security__actions { display: block; }
.account-security.is-mobile .account-security__actions :deep(.el-button) { width: 100%; height: var(--m-tap); margin: 0; }
.account-security.is-mobile .account-security__qr img { width: min(220px, 60vw); height: min(220px, 60vw); }
.account-security.is-mobile .account-security__setup-actions :deep(.el-button) { flex: 1; height: var(--m-tap); }
</style>
