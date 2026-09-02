<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { api, post, put } from '../../api'
import { formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('account')
const operators = ref([])
const operatorSheet = ref(false)
const totpSetup = reactive({ open: false, loading: false, secret: '', qr: '', code: '' })
const operator = reactive({ username: '', password: '', role: 'operator' })
const operatorKeyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const roleSheet = ref(false)
const roleTarget = ref(null)
const roleDraft = ref('operator')

const roles = [
  { value: 'admin', label: '管理员' },
  { value: 'operator', label: '运维人员' },
  { value: 'viewer', label: '只读用户' },
]
const roleNames = Object.fromEntries(roles.map((role) => [role.value, role.label]))
const filteredOperators = computed(() => operators.value.filter((row) => includesText([row.username, roleNames[row.role]], operatorKeyword.value)))
const tabs = computed(() => (isAdmin.value
  ? [{ value: 'account', label: '登录安全' }, { value: 'operators', label: '管理账户' }]
  : [{ value: 'account', label: '登录安全' }]))

async function load() {
  loading.value = true
  try {
    const operatorResult = await (isAdmin.value ? api('/operators') : Promise.resolve({ operators: [] }))
    operators.value = operatorResult.operators || []
  } finally { loading.value = false }
}

function openOperator() {
  Object.assign(operator, { username: '', password: '', role: 'operator' })
  operatorSheet.value = true
}

async function saveOperator() {
  await post('/operators', operator)
  operatorSheet.value = false
  ElMessage.success('账户已创建，用户首次登录时需要修改初始密码')
  await load()
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '权限', value: roleNames[row.role] || row.role },
    { label: '状态', value: row.enabled ? '启用' : '停用' },
    { label: '登录方式', value: row.totp_enabled ? '密码 + 两步验证' : '仅密码' },
    { label: '密码', value: row.must_change_password ? '等待本人修改' : '已设置' },
    { label: '最后登录', value: formatDateTime(row.last_login_at, '从未登录') },
  ]
})

function openActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const actions = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  const list = [
    { key: 'role', label: '修改权限', hint: `当前：${roleNames[row.role]}` },
    { key: 'state', label: row.enabled ? '停用账户' : '启用账户' },
    { key: 'password', label: '重置密码' },
  ]
  if (row.totp_enabled) list.push({ key: 'totp', label: '关闭两步验证', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'role') return changeRole(row)
  if (key === 'state') return setOperatorState(row)
  if (key === 'password') return resetPassword(row)
  if (key === 'totp') return disableOperatorTOTP(row)
}

function changeRole(row) {
  roleTarget.value = row
  roleDraft.value = row.role
  roleSheet.value = true
}

async function saveRole() {
  await put(`/operators/${roleTarget.value.id}`, { role: roleDraft.value, enabled: roleTarget.value.enabled })
  roleSheet.value = false
  ElMessage.success('权限已修改')
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
    totpSetup.qr = await QRCode.toDataURL(result.otpauth_uri, { width: 440, margin: 1 })
    totpSetup.code = ''
    totpSetup.open = true
  } finally { totpSetup.loading = false }
}

async function enableTOTP() {
  if (totpSetup.loading) return
  if (!/^\d{6}$/.test(totpSetup.code)) return ElMessage.error('请输入验证器显示的 6 位数字')
  totpSetup.loading = true
  try {
    await api('/auth/2fa/enable', { method: 'POST', body: { code: totpSetup.code }, silentUnauthorized: true })
    appState.totp_enabled = true
    totpSetup.open = false
    ElMessage.success('两步验证已启用，后续登录需要输入动态验证码')
    await load()
  } catch (error) {
    ElMessage.error(error.code === 'authentication failed'
      ? '验证码不正确，请输入验证器当前显示的 6 位动态码'
      : error.message)
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

onMounted(load)
</script>

<template>
  <MPage title="系统设置" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
    </template>

    <MSegmented v-model="tab" :options="tabs" />

    <template v-if="tab === 'account'">
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">两步验证</span>
          <span class="m-pill" :class="appState.totp_enabled ? 'm-pill--success' : 'm-pill--info'">{{ appState.totp_enabled ? '已启用' : '未启用' }}</span>
        </div>
        <div class="m-card__note">{{ appState.totp_enabled ? '登录时需要输入验证器生成的动态验证码。' : '为账户增加一层动态验证码保护。' }}</div>
        <el-button v-if="appState.totp_enabled" type="danger" plain class="wide" @click="disableOwnTOTP">关闭两步验证</el-button>
        <el-button v-else type="primary" class="wide" :loading="totpSetup.loading" @click="beginTOTPSetup">启用两步验证</el-button>
      </section>
    </template>

    <template v-else>
      <el-input v-model="operatorKeyword" clearable :prefix-icon="Search" placeholder="搜索用户名或权限" />
      <div class="m-count">共 {{ filteredOperators.length }} 个账户</div>
      <article v-for="row in filteredOperators" :key="row.id" class="m-item" :class="{ 'is-off': !row.enabled }">
        <button type="button" class="m-item__hit" @click="openActions(row)">
          <div class="m-item__head">
            <span class="m-item__title">{{ row.username }}</span>
            <span v-if="row.must_change_password" class="m-pill m-pill--warning">待改密码</span>
            <span v-if="!row.enabled" class="m-pill m-pill--info">停用</span>
            <i class="m-item__chevron" aria-hidden="true">›</i>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ roleNames[row.role] || row.role }}</b><small>权限</small></span>
            <span class="m-stat"><b>{{ row.totp_enabled ? '两步验证' : '仅密码' }}</b><small>登录方式</small></span>
          </div>
          <div class="m-item__meta">最后登录 {{ formatDateTime(row.last_login_at, '从未登录') }}</div>
        </button>
      </article>
      <div v-if="!filteredOperators.length && !loading" class="m-empty">还没有管理账户</div>
    </template>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.username" :details="details" :actions="actions" @select="runAction" />

    <MSheet v-model="roleSheet" :title="`修改 ${roleTarget?.username || ''} 的权限`">
      <div class="m-field">
        <label class="m-field__label">权限</label>
        <MPicker v-model="roleDraft" :options="roles" title="选择权限" />
      </div>
      <template #footer>
        <el-button @click="roleSheet = false">取消</el-button>
        <el-button type="primary" @click="saveRole">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="totpSetup.open" title="启用两步验证">
      <div class="totp">
        <p>用验证器扫描下面的二维码，或复制密钥手动添加，然后输入 6 位动态码完成绑定。</p>
        <div class="totp__frame"><img :src="totpSetup.qr" alt="两步验证绑定二维码" data-testid="totp-qr" /></div>
        <div class="m-field">
          <label class="m-field__label">无法扫码时，可手动输入此密钥</label>
          <el-input :model-value="totpSetup.secret" readonly class="m-mono" aria-label="无法扫码时，可手动输入此密钥" data-testid="totp-secret" />
        </div>
        <div class="m-field">
          <label class="m-field__label">动态验证码</label>
          <el-input v-model="totpSetup.code" maxlength="6" inputmode="numeric" aria-label="动态验证码" placeholder="000000" />
        </div>
      </div>
      <template #footer>
        <el-button @click="totpSetup.open = false">取消</el-button>
        <el-button type="primary" :loading="totpSetup.loading" @click="enableTOTP">启用</el-button>
      </template>
    </MSheet>

    <MSheet v-model="operatorSheet" title="新建管理账户">
      <div class="m-field">
        <label class="m-field__label">用户名</label>
        <el-input v-model="operator.username" aria-label="用户名" placeholder="3 至 64 位，可用字母、数字、点、下划线和短横线" />
      </div>
      <div class="m-field">
        <label class="m-field__label">初始密码</label>
        <el-input v-model="operator.password" type="password" show-password aria-label="初始密码" />
        <div class="m-field__hint">用户首次登录时必须修改此密码，至少 12 位。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">权限</label>
        <MPicker v-model="operator.role" :options="roles" title="选择权限" />
      </div>
      <template #footer>
        <el-button @click="operatorSheet = false">取消</el-button>
        <el-button type="primary" :disabled="!operator.username || operator.password.length < 12" @click="saveOperator">创建</el-button>
      </template>
    </MSheet>
    <template v-if="isAdmin && tab === 'operators'" #fab>
      <button type="button" class="m-fab" aria-label="新建管理账户" @click="openOperator">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
.wide { width: 100%; height: var(--m-tap); margin: 12px 0 0; }
.totp { text-align: center; }
.totp > p { margin: 0 0 16px; color: var(--sb-muted); font-size: 13px; line-height: 1.7; text-align: left; }
/* 二维码必须黑白，深色底会让验证器扫不出来。 */
.totp__frame { display: inline-block; padding: 10px; margin-bottom: 18px; background: #fff; border-radius: var(--m-radius); line-height: 0; }
.totp__frame img { display: block; width: min(220px, 60vw); height: min(220px, 60vw); }
.totp .m-field { text-align: left; }
</style>
