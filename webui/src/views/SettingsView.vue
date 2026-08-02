<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Delete, Plus, Refresh } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('operators')
const operators = ref([])
const certificates = ref([])
const realityKeys = ref([])
const dialog = ref('')
const secretOpen = ref(false)
const oneTimeSecret = ref('')
const secretTitle = ref('密钥')
const secretNotice = ref('此密钥只显示一次，请立即复制并妥善保存。')
const operator = reactive({ email: '', password: '', role: 'operator' })
const certificate = reactive({ name: '', certificate_pem: '', private_key_pem: '', enabled: true })
const reality = reactive({ name: '' })

async function load() {
  loading.value = true
  try {
    const [operatorResult, certificateResult, realityResult] = await Promise.all([
      isAdmin.value ? api('/operators') : Promise.resolve({ operators: [] }),
      api('/certificates'), api('/reality-keys'),
    ])
    operators.value = operatorResult.operators || []
    certificates.value = certificateResult.certificates || []
    realityKeys.value = realityResult.reality_keys || []
  } finally { loading.value = false }
}
function open(kind) {
  dialog.value = kind
  if (kind === 'operator') Object.assign(operator, { email: '', password: '', role: 'operator' })
  if (kind === 'certificate') Object.assign(certificate, { name: '', certificate_pem: '', private_key_pem: '', enabled: true })
  if (kind === 'reality') Object.assign(reality, { name: '' })
}
async function saveOperator() {
  const result = await post('/operators', operator)
  dialog.value = ''
  oneTimeSecret.value = result.totp_secret
  secretTitle.value = '两步验证绑定密钥'
  secretNotice.value = '请立即将此密钥添加到验证器应用。关闭后将不再显示。'
  secretOpen.value = true
  await load()
}
async function setOperatorState(row) { await put(`/operators/${row.id}`, { role: row.role, enabled: !row.enabled }); await load() }
async function resetPassword(row) {
  const result = await ElMessageBox.prompt(`为 ${row.email} 输入新密码`, '重置密码', { inputType: 'password', inputValidator: (value) => value.length >= 12 || '密码至少 12 位' })
  await post(`/operators/${row.id}/password`, { password: result.value }); ElMessage.success('密码已重置')
}
async function resetMFA(row) {
  await ElMessageBox.confirm(`重置 ${row.email} 的两步验证？`, '重置两步验证', { type: 'warning' })
  oneTimeSecret.value = (await post(`/operators/${row.id}/totp/reset`, {})).totp_secret
  secretTitle.value = '新的两步验证绑定密钥'
  secretNotice.value = '原绑定已失效。请立即将此密钥重新添加到验证器应用。关闭后将不再显示。'
  secretOpen.value = true
}
async function saveCertificate() { await post('/certificates', certificate); dialog.value = ''; await load() }
async function removeCertificate(row) { await ElMessageBox.confirm(`确认删除证书“${row.name}”？`, '删除证书', { type: 'warning' }); await del(`/certificates/${row.id}`); await load() }
async function saveReality() {
  const result = await post('/reality-keys', reality)
  dialog.value = ''
  oneTimeSecret.value = result.private_key
  secretTitle.value = 'Reality 连接密钥'
  secretNotice.value = '此密钥只显示一次，请立即复制并妥善保存。'
  secretOpen.value = true
  await load()
}
async function toggleReality(row) { await post(`/reality-keys/${row.id}/enabled`, { enabled: !row.enabled }); await load() }
async function copySecret() { await navigator.clipboard.writeText(oneTimeSecret.value); ElMessage.success('密钥已复制') }
onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="系统设置" description="管理平台账户、加密证书和 Reality 连接密钥"><el-button :icon="Refresh" @click="load">刷新</el-button></PageHeader>
    <main class="page-content"><div class="table-panel"><el-tabs v-model="tab" class="panel-tabs">
      <el-tab-pane v-if="isAdmin" label="管理账户" name="operators"><div class="tab-actions"><el-button type="primary" :icon="Plus" @click="open('operator')">新建账户</el-button></div><el-table v-loading="loading" :data="operators"><el-table-column label="邮箱" prop="email" min-width="210" /><el-table-column label="权限" width="130"><template #default="{ row }"><el-select v-model="row.role" size="small" @change="put(`/operators/${row.id}`, { role: row.role, enabled: row.enabled })"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></template></el-table-column><el-table-column label="状态" width="100"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column><el-table-column label="最近登录" prop="last_login_at" min-width="190"><template #default="{ row }">{{ row.last_login_at || '—' }}</template></el-table-column><el-table-column label="操作" width="250"><template #default="{ row }"><el-button link @click="setOperatorState(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button link @click="resetPassword(row)">重置密码</el-button><el-button link @click="resetMFA(row)">重置两步验证</el-button></template></el-table-column></el-table></el-tab-pane>
      <el-tab-pane label="加密证书" name="certificates"><div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" @click="open('certificate')">导入证书</el-button></div><el-table v-loading="loading" :data="certificates"><el-table-column label="名称" prop="name" min-width="220" /><el-table-column label="状态" width="100"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column><el-table-column label="更新时间" prop="updated_at" min-width="190" /><el-table-column label="操作" width="100"><template #default="{ row }"><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeCertificate(row)">删除</el-button></template></el-table-column></el-table></el-tab-pane>
      <el-tab-pane label="Reality 密钥" name="reality"><div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" @click="open('reality')">生成密钥</el-button></div><el-table v-loading="loading" :data="realityKeys"><el-table-column label="名称" prop="name" min-width="180" /><el-table-column label="公钥" prop="public_key" min-width="360"><template #default="{ row }"><span class="mono">{{ row.public_key }}</span></template></el-table-column><el-table-column label="状态" width="100"><template #default="{ row }">{{ row.enabled ? '启用' : '停用' }}</template></el-table-column><el-table-column label="操作" width="100"><template #default="{ row }"><el-button v-if="isAdmin" link @click="toggleReality(row)">{{ row.enabled ? '停用' : '启用' }}</el-button></template></el-table-column></el-table></el-tab-pane>
    </el-tabs></div></main>

    <el-dialog :model-value="dialog === 'operator'" title="新建管理账户" width="540px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="邮箱"><el-input v-model="operator.email" /></el-form-item><el-form-item label="初始密码"><el-input v-model="operator.password" type="password" show-password /></el-form-item><el-form-item label="权限"><el-select v-model="operator.role" style="width: 100%"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!operator.email || operator.password.length < 12" @click="saveOperator">创建账户</el-button></template></el-dialog>
    <el-dialog :model-value="dialog === 'certificate'" title="导入加密证书" width="700px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="证书名称"><el-input v-model="certificate.name" /></el-form-item><el-form-item label="证书内容（PEM 格式）"><el-input v-model="certificate.certificate_pem" type="textarea" :rows="6" class="mono" /></el-form-item><el-form-item label="证书私钥（PEM 格式）"><el-input v-model="certificate.private_key_pem" type="textarea" :rows="6" class="mono" /></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!certificate.name || !certificate.certificate_pem || !certificate.private_key_pem" @click="saveCertificate">导入证书</el-button></template></el-dialog>
    <el-dialog :model-value="dialog === 'reality'" title="生成 Reality 连接密钥" width="500px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="密钥名称"><el-input v-model="reality.name" placeholder="例如：主服务器 Reality" /></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!reality.name" @click="saveReality">生成密钥</el-button></template></el-dialog>
    <el-dialog v-model="secretOpen" :title="secretTitle" width="620px"><el-alert :title="secretNotice" type="warning" show-icon :closable="false" /><el-input v-model="oneTimeSecret" readonly class="mono" style="margin-top: 16px" /><template #footer><el-button @click="secretOpen = false">完成</el-button><el-button type="primary" :icon="CopyDocument" @click="copySecret">复制密钥</el-button></template></el-dialog>
  </div>
</template>
