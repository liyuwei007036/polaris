<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Loading, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, friendlyError, post, put } from '../../api'
import { formatDateTime, includesText, parseServerTime } from '../../format'
import { waitForTask } from '../../live'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const rows = ref([])
const sheetOpen = ref(false)
const editing = ref(null)
const testSheet = ref(false)
const testing = ref(false)
const testTarget = ref(null)
const testResults = ref([])
const form = reactive({ name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true, expires_at: '' })
const keyword = ref('')
const selectedType = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)

const typeOptions = [{ value: 'socks', label: 'SOCKS5' }, { value: 'http', label: 'HTTP' }]
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedType.value && row.type !== selectedType.value) return false
  return includesText([row.name, row.type, row.server, row.server_port, row.username], keyword.value)
}))
const onlineNodes = computed(() => appState.nodes.filter((node) => node.online))

async function load() {
  loading.value = true
  try { rows.value = (await api('/outbounds')).outbounds || [] } finally { loading.value = false }
}

// 过期时间只是给操作者的提醒：过期不会自动停用，所以标出来而不是藏起来。
function isExpired(row) {
  const expiry = parseServerTime(row.expires_at)
  return Boolean(expiry) && expiry.getTime() < Date.now()
}

// 原生 datetime-local 用的是本地时间字符串，服务端收到的仍是 UTC 时刻。
function toLocalInput(value) {
  const date = parseServerTime(value)
  if (!date) return ''
  const pad = (part) => String(part).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true, expires_at: '' })
  sheetOpen.value = true
}

function openEdit(row) {
  editing.value = row
  Object.assign(form, { ...row, password: '', expires_at: toLocalInput(row.expires_at) })
  sheetOpen.value = true
}

async function save() {
  saving.value = true
  try {
    const expiry = form.expires_at ? new Date(form.expires_at) : null
    const payload = {
      ...form,
      server_port: Number(form.server_port),
      expires_at: expiry && !Number.isNaN(expiry.getTime()) ? expiry.toISOString() : '',
    }
    if (editing.value) await put(`/outbounds/${editing.value.id}`, payload)
    else await post('/outbounds', payload)
    ElMessage.success(editing.value ? '上网出口已保存' : '上网出口已创建')
    sheetOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '上网出口保存失败')
  } finally { saving.value = false }
}

function openActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const actions = computed(() => {
  const row = actionTarget.value
  if (!row || row.type === 'direct') return []
  const list = [{ key: 'test', label: '检测连通性', hint: '逐台在线服务器检测' }]
  if (canWrite.value) {
    list.push({ key: 'edit', label: '编辑' })
    list.push({ key: 'toggle', label: row.enabled ? '停用' : '启用' })
  }
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'test') return openTest(row)
  if (key === 'edit') return openEdit(row)
  if (key === 'toggle') return toggle(row)
  if (key === 'delete') return remove(row)
}

async function toggle(row) {
  await post(`/outbounds/${row.id}/enabled`, { enabled: !row.enabled })
  await load()
}

async function remove(row) {
  await ElMessageBox.confirm(`确认删除上网出口“${row.name}”？`, '删除上网出口', { type: 'warning' })
  await del(`/outbounds/${row.id}`)
  await load()
}

async function openTest(row) {
  await loadNodes()
  testTarget.value = row
  // 每台服务器出网路径不同，一台通不代表其它台通，所以全部一起测。
  testResults.value = onlineNodes.value.map((node) => ({ id: node.id, name: node.name, state: 'pending', detail: '等待开始' }))
  testSheet.value = true
  if (testResults.value.length) runTest()
}

function updateResult(nodeID, patch) {
  testResults.value = testResults.value.map((row) => (row.id === nodeID ? { ...row, ...patch } : row))
}

async function runTest() {
  if (testing.value) return
  testing.value = true
  testResults.value = testResults.value.map((row) => ({ ...row, state: 'running', detail: '正在检测' }))
  try {
    await Promise.all(testResults.value.map(async (row) => {
      try {
        const task = await post(`/outbounds/${testTarget.value.id}/test`, { node_id: row.id })
        const result = await waitForTask(task.id, 40000)
        const summary = result.result_summary || ''
        if (result.status !== 'succeeded') {
          updateResult(row.id, { state: 'failed', detail: summary ? friendlyError(summary) : '检测未通过' })
          return
        }
        updateResult(row.id, { state: 'passed', detail: hasChinese(summary) ? summary : '可正常上网' })
      } catch (error) {
        updateResult(row.id, { state: 'failed', detail: error instanceof Error ? error.message : '检测未完成' })
      }
    }))
    const passed = testResults.value.filter((row) => row.state === 'passed').length
    ElMessage[passed === testResults.value.length ? 'success' : 'warning'](`检测完成：${passed}/${testResults.value.length} 台服务器可通过该出口上网`)
  } finally { testing.value = false }
}

// agent 用中文写结果摘要；不是中文的都是内部消息，对操作者没有意义。
function hasChinese(value) {
  return /[㐀-鿿]/.test(value || '')
}

const testStates = {
  pending: ['等待中', 'm-pill--info'],
  running: ['检测中', 'm-pill--warning'],
  passed: ['可用', 'm-pill--success'],
  failed: ['不可用', 'm-pill--danger'],
}

onMounted(load)
</script>

<template>
  <MPage title="上网出口" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
      <el-button v-if="canWrite" type="primary" :icon="Plus" circle aria-label="新建上网出口" @click="openCreate" />
    </template>

    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或用户" />
    <MSegmented
      v-model="selectedType"
      class="filter"
      :options="[{ value: '', label: '全部' }, { value: 'socks', label: 'SOCKS5' }, { value: 'http', label: 'HTTP' }, { value: 'direct', label: '直连' }]"
    />

    <article v-for="row in filteredRows" :key="row.id" class="m-card">
      <div class="m-card__top">
        <span class="m-card__title">{{ row.name }}</span>
        <span class="m-pill m-pill--accent">{{ row.type.toUpperCase() }}</span>
        <span class="m-pill" :class="row.enabled ? 'm-pill--success' : 'm-pill--info'">{{ row.enabled ? '启用' : '停用' }}</span>
        <button v-if="row.type !== 'direct'" type="button" class="m-more-btn" :aria-label="`${row.name} 的操作`" @click="openActions(row)">⋯</button>
      </div>
      <div class="m-card__row m-mono">
        <span>{{ row.type === 'direct' ? '服务器直连' : `${row.server}:${row.server_port}` }}</span>
      </div>
      <div class="m-card__row">
        <span>登录用户 {{ row.username || '—' }}</span>
        <span class="m-card__spacer" />
        <span :class="{ 'm-danger': isExpired(row) }">{{ formatDateTime(row.expires_at, '未设置过期') }}</span>
      </div>
    </article>

    <div v-if="!filteredRows.length && !loading" class="m-empty">还没有上网出口</div>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name" :actions="actions" @select="runAction" />

    <MSheet v-model="sheetOpen" :title="editing ? '编辑上网出口' : '新建上网出口'" full>
      <div class="m-field">
        <label class="m-field__label">名称 <em>*</em></label>
        <el-input v-model="form.name" aria-label="名称" />
      </div>
      <div class="m-field">
        <label class="m-field__label">类型 <em>*</em></label>
        <MPicker v-model="form.type" :options="typeOptions" title="选择类型" />
      </div>
      <div class="m-field">
        <label class="m-field__label">服务器地址 <em>*</em></label>
        <el-input v-model="form.server" aria-label="服务器地址" placeholder="127.0.0.1" />
      </div>
      <div class="m-field">
        <label class="m-field__label">端口 <em>*</em></label>
        <el-input-number v-model="form.server_port" :min="1" :max="65535" controls-position="right" aria-label="端口" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">用户名</label>
        <el-input v-model="form.username" aria-label="用户名" />
      </div>
      <div class="m-field">
        <label class="m-field__label">{{ editing ? '新密码（留空则不变）' : '密码' }}</label>
        <el-input v-model="form.password" type="password" show-password aria-label="密码" />
      </div>
      <div class="m-field">
        <label class="m-field__label">过期时间</label>
        <!-- 用系统自带的日期选择：手机上的滚轮比浮层日历好用得多。 -->
        <input v-model="form.expires_at" type="datetime-local" class="datetime" aria-label="过期时间" />
        <div class="m-field__hint">选填，仅作提醒，到期不会自动停用。</div>
      </div>
      <template #footer>
        <el-button @click="sheetOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!form.name || !form.server" @click="save">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="testSheet" :title="`检测：${testTarget?.name || ''}`" :dismissible="!testing">
      <div v-if="!testResults.length" class="m-notice m-notice--warning">没有在线的服务器可用于检测。</div>
      <template v-else>
        <div class="m-notice" :class="testing ? 'm-notice--info' : 'm-notice--info'">
          {{ testing ? '正在通过该出口检测各服务器的连通性…' : '各服务器通过该出口的连通性结果。' }}
        </div>
        <article v-for="row in testResults" :key="row.id" class="m-card">
          <div class="m-card__top">
            <span class="m-card__title">{{ row.name }}</span>
            <span class="m-pill" :class="testStates[row.state][1]">{{ testStates[row.state][0] }}</span>
          </div>
          <div class="m-card__note">
            <el-icon v-if="row.state === 'running'" class="is-loading"><Loading /></el-icon>
            {{ row.detail }}
          </div>
        </article>
      </template>
      <template #footer>
        <el-button :disabled="testing" @click="testSheet = false">关闭</el-button>
        <el-button type="primary" :loading="testing" :disabled="!testResults.length" @click="runTest">重新检测</el-button>
      </template>
    </MSheet>
  </MPage>
</template>

<style scoped>
.filter { margin-top: 10px; }
.datetime {
  width: 100%;
  min-height: var(--m-tap);
  padding: 9px 12px;
  color: var(--sb-text);
  background: rgba(148, 163, 184, .07);
  border: 1px solid var(--sb-line-strong);
  border-radius: 8px;
  font: inherit;
  font-size: 16px;
}
</style>
