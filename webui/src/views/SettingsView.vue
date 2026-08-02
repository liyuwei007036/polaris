<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Refresh } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('account')
const operators = ref([])
const certificates = ref([])
const dialog = ref('')
const totpSetup = reactive({ open: false, loading: false, secret: '', qr: '', code: '' })
const operator = reactive({ username: '', password: '', role: 'operator' })
const certificate = reactive({ name: '', certificate_pem: '', private_key_pem: '', enabled: true })

async function load() {
  loading.value = true
  try {
    const [operatorResult, certificateResult] = await Promise.all([
      isAdmin.value ? api('/operators') : Promise.resolve({ operators: [] }),
      api('/certificates'),
    ])
    operators.value = operatorResult.operators || []
    certificates.value = certificateResult.certificates || []
  } finally { loading.value = false }
}

function open(kind) {
  dialog.value = kind
  if (kind === 'operator') Object.assign(operator, { username: '', password: '', role: 'operator' })
  if (kind === 'certificate') Object.assign(certificate, { name: '', certificate_pem: '', private_key_pem: '', enabled: true })
}

async function saveOperator() {
  await post('/operators', operator)
  dialog.value = ''
  ElMessage.success('账户已创建，用户首次登录时需要修改初始密码')
  await load()
}

async function setOperatorState(row) {
  await put(`/operators/${row.id}`, { role: row.role, enabled: !row.enabled })
  await load()
}

async function resetPassword(row) {
  const result = await ElMessageBox.prompt(`为 ${row.username} 设置新的初始密码`, '重置密码', {
    inputType: 'password',
    inputValidator: (value) => value.length >= 12 || '密码至少 12 位',
  })
  await post(`/operators/${row.id}/password`, { password: result.value })
  ElMessage.success('密码已重置，用户下次登录时需要修改密码')
  await load()
}

async function disableOperatorTOTP(row) {
  await ElMessageBox.confirm(`关闭 ${row.username} 的两步验证？`, '关闭两步验证', { type: 'warning' })
  await post(`/operators/${row.id}/totp/reset`, {})
  ElMessage.success('两步验证已关闭')
  await load()
}

async function beginTOTPSetup() {
  totpSetup.loading = true
  try {
    const result = await post('/auth/2fa/setup', {})
    totpSetup.secret = result.secret
    totpSetup.qr = await QRCode.toDataURL(result.otpauth_uri, { width: 220, margin: 1 })
    totpSetup.code = ''
    totpSetup.open = true
  } finally { totpSetup.loading = false }
}

async function enableTOTP() {
  if (!/^\d{6}$/.test(totpSetup.code)) return ElMessage.error('请输入验证器显示的 6 位数字')
  totpSetup.loading = true
  try {
    await post('/auth/2fa/enable', { code: totpSetup.code })
    appState.totp_enabled = true
    totpSetup.open = false
    ElMessage.success('两步验证已启用，后续登录需要输入动态验证码')
    await load()
  } finally { totpSetup.loading = false }
}

async function disableOwnTOTP() {
  const result = await ElMessageBox.prompt('请输入当前登录密码以确认关闭', '关闭两步验证', {
    inputType: 'password',
    inputValidator: (value) => Boolean(value) || '请输入当前密码',
  })
  await post('/auth/2fa/disable', { password: result.value })
  appState.totp_enabled = false
  ElMessage.success('两步验证已关闭')
  await load()
}

async function saveCertificate() { await post('/certificates', certificate); dialog.value = ''; await load() }
async function removeCertificate(row) { await ElMessageBox.confirm(`确认删除证书“${row.name}”？`, '删除证书', { type: 'warning' }); await del(`/certificates/${row.id}`); await load() }
onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="系统设置" description="管理登录安全、平台账户和加密证书；Reality 密钥由接入服务自动生成">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main class="page-content">
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="登录安全" name="account">
            <section class="security-card">
              <div>
                <h3>两步验证</h3>
                <p v-if="appState.totp_enabled">已启用。后续登录除密码外，还需要输入验证器应用生成的动态验证码。</p>
                <p v-else>未启用。可以使用验证器应用扫描二维码，提高账户安全性。</p>
              </div>
              <el-tag :type="appState.totp_enabled ? 'success' : 'info'">{{ appState.totp_enabled ? '已启用' : '未启用' }}</el-tag>
              <el-button v-if="appState.totp_enabled" type="danger" plain @click="disableOwnTOTP">关闭两步验证</el-button>
              <el-button v-else type="primary" :loading="totpSetup.loading" @click="beginTOTPSetup">启用两步验证</el-button>
            </section>
          </el-tab-pane>

          <el-tab-pane v-if="isAdmin" label="管理账户" name="operators">
            <div class="tab-actions"><el-button type="primary" :icon="Plus" @click="open('operator')">新建账户</el-button></div>
            <el-table v-loading="loading" :data="operators">
              <el-table-column label="用户名" prop="username" min-width="180" />
              <el-table-column label="权限" width="130"><template #default="{ row }"><el-select v-model="row.role" size="small" @change="put(`/operators/${row.id}`, { role: row.role, enabled: row.enabled })"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></template></el-table-column>
              <el-table-column label="登录安全" width="150"><template #default="{ row }"><el-tag v-if="row.must_change_password" type="warning">等待修改密码</el-tag><el-tag v-else-if="row.totp_enabled" type="success">两步验证已启用</el-tag><el-tag v-else type="info">密码登录</el-tag></template></el-table-column>
              <el-table-column label="状态" width="100"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
              <el-table-column label="最近登录" prop="last_login_at" min-width="190"><template #default="{ row }">{{ row.last_login_at || '—' }}</template></el-table-column>
              <el-table-column label="操作" width="280"><template #default="{ row }"><el-button link @click="setOperatorState(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button link @click="resetPassword(row)">重置密码</el-button><el-button v-if="row.totp_enabled" link @click="disableOperatorTOTP(row)">关闭两步验证</el-button></template></el-table-column>
            </el-table>
          </el-tab-pane>

          <el-tab-pane label="加密证书" name="certificates">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" @click="open('certificate')">导入证书</el-button></div>
            <el-table v-loading="loading" :data="certificates"><el-table-column label="名称" prop="name" min-width="220" /><el-table-column label="状态" width="100"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column><el-table-column label="更新时间" prop="updated_at" min-width="190" /><el-table-column label="操作" width="100"><template #default="{ row }"><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeCertificate(row)">删除</el-button></template></el-table-column></el-table>
          </el-tab-pane>

        </el-tabs>
      </div>
    </main>

    <el-dialog v-model="totpSetup.open" title="启用两步验证" width="500px" :close-on-click-modal="false">
      <div class="totp-setup">
        <p>使用验证器应用扫描二维码，然后输入应用显示的 6 位数字完成绑定。</p>
        <img :src="totpSetup.qr" alt="两步验证绑定二维码" data-testid="totp-qr" />
        <el-form label-position="top">
          <el-form-item label="无法扫码时，可手动输入此密钥"><el-input :model-value="totpSetup.secret" readonly class="mono" data-testid="totp-secret" /></el-form-item>
          <el-form-item label="动态验证码"><el-input v-model="totpSetup.code" maxlength="6" inputmode="numeric" placeholder="000000" @keyup.enter="enableTOTP" /></el-form-item>
        </el-form>
      </div>
      <template #footer><el-button @click="totpSetup.open = false">取消</el-button><el-button type="primary" :loading="totpSetup.loading" @click="enableTOTP">确认启用</el-button></template>
    </el-dialog>
    <el-dialog :model-value="dialog === 'operator'" title="新建管理账户" width="540px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="用户名"><el-input v-model="operator.username" placeholder="3 至 64 位，可使用字母、数字、点、下划线和短横线" /></el-form-item><el-form-item label="初始密码"><el-input v-model="operator.password" type="password" show-password /><div class="form-tip">用户首次登录时必须修改此密码。</div></el-form-item><el-form-item label="权限"><el-select v-model="operator.role" style="width: 100%"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!operator.username || operator.password.length < 12" @click="saveOperator">创建账户</el-button></template></el-dialog>
    <el-dialog :model-value="dialog === 'certificate'" title="导入加密证书" width="700px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="证书名称"><el-input v-model="certificate.name" /></el-form-item><el-form-item label="证书内容（PEM 格式）"><el-input v-model="certificate.certificate_pem" type="textarea" :rows="6" class="mono" /></el-form-item><el-form-item label="证书私钥（PEM 格式）"><el-input v-model="certificate.private_key_pem" type="textarea" :rows="6" class="mono" /></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!certificate.name || !certificate.certificate_pem || !certificate.private_key_pem" @click="saveCertificate">导入证书</el-button></template></el-dialog>
  </div>
</template>

<style scoped>
.security-card { display: flex; align-items: center; gap: 18px; padding: 24px; }
.security-card > div { flex: 1; }
.security-card h3 { margin: 0 0 7px; color: var(--sb-text); font-size: 16px; }
.security-card p { margin: 0; color: var(--sb-muted); line-height: 1.7; }
.totp-setup { text-align: center; }
.totp-setup > p { margin: 0 0 16px; color: var(--sb-muted); line-height: 1.7; }
.totp-setup img { width: 220px; height: 220px; margin-bottom: 14px; border: 1px solid var(--sb-border); border-radius: 8px; }
.totp-setup :deep(.el-form-item) { text-align: left; }
.form-tip { margin-top: 6px; color: var(--sb-muted); font-size: 12px; }
@media (max-width: 700px) {
  .security-card { align-items: flex-start; flex-wrap: wrap; }
  .security-card > div { flex-basis: 100%; }
}
</style>
