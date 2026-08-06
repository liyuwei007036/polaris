<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, Delete, Edit, Loading, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, friendlyError, post, put } from '../api'
import { formatDateTime, includesText, parseServerTime } from '../format'
import { waitForTask } from '../live'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const rows = ref([])
const dialogOpen = ref(false)
const editing = ref(null)
const testDialog = ref(false)
const testing = ref(false)
const testTarget = ref(null)
const testResults = ref([])
const form = reactive({ name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true, expires_at: '' })
const keyword = ref('')
const selectedType = ref('')
const selectedStatus = ref('')
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedType.value && row.type !== selectedType.value) return false
  if (selectedStatus.value && String(row.enabled) !== selectedStatus.value) return false
  return includesText([row.name, row.type, row.server, row.server_port, row.username], keyword.value)
}))

async function load() {
  loading.value = true
  try { rows.value = (await api('/outbounds')).outbounds || [] } finally { loading.value = false }
}
// The date is recorded for the operator's benefit only: nothing stops using an
// expired outbound, so it is marked rather than hidden.
function isExpired(row) {
  const expiry = parseServerTime(row.expires_at)
  return Boolean(expiry) && expiry.getTime() < Date.now()
}
function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true, expires_at: '' })
  dialogOpen.value = true
}
function openEdit(row) {
  editing.value = row
  // The picker works in the visitor's own time zone; the value sent back is
  // the UTC instant that time represents.
  Object.assign(form, { ...row, password: '', expires_at: row.expires_at ? parseServerTime(row.expires_at) : '' })
  dialogOpen.value = true
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
    dialogOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '上网出口保存失败')
  } finally { saving.value = false }
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
const onlineNodes = computed(() => appState.nodes.filter((node) => node.online))

async function openTest(row) {
  await loadNodes()
  testTarget.value = row
  // Every server reaches the internet differently, so one server passing says
  // nothing about the rest. All of them are tested together.
  testResults.value = onlineNodes.value.map((node) => ({ id: node.id, name: node.name, state: 'pending', detail: '等待开始' }))
  testDialog.value = true
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

// Closing mid-run would leave the operator with no way back to the results,
// so the dialog stays put until every server has reported.
function requestCloseTest(done) {
  if (testing.value) {
    ElMessage.warning('检测正在进行，请等待全部服务器返回结果')
    return
  }
  done()
}

// The agent writes its own summaries in Chinese; anything else is an internal
// message that would mean nothing to an operator.
function hasChinese(value) {
  return /[㐀-鿿]/.test(value || '')
}

const testStates = {
  pending: ['等待中', 'info'],
  running: ['检测中', 'warning'],
  passed: ['可用', 'success'],
  failed: ['不可用', 'danger'],
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="上网出口">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建</el-button>
    </PageHeader>
    <main class="page-content">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或用户" style="width: 260px" />
        <el-select v-model="selectedType" clearable placeholder="全部类型" style="width: 140px"><el-option label="SOCKS5" value="socks" /><el-option label="HTTP" value="http" /><el-option label="直连" value="direct" /></el-select>
        <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredRows" :loading="loading" empty-text="还没有上网出口">
          <el-table-column label="名称" prop="name" min-width="180" />
          <el-table-column label="类型" width="110"><template #default="{ row }"><el-tag>{{ row.type.toUpperCase() }}</el-tag></template></el-table-column>
          <el-table-column label="服务器" min-width="220"><template #default="{ row }"><span class="mono">{{ row.type === 'direct' ? '服务器直连' : `${row.server}:${row.server_port}` }}</span></template></el-table-column>
          <el-table-column label="登录用户名" prop="username" min-width="140"><template #default="{ row }">{{ row.username || '—' }}</template></el-table-column>
          <el-table-column label="过期时间" width="180">
            <template #default="{ row }">
              <span :class="{ 'expired-text': isExpired(row) }">{{ formatDateTime(row.expires_at, '未设置') }}</span>
            </template>
          </el-table-column>
          <el-table-column label="状态" width="90"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
          <el-table-column label="操作" width="250" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <template v-if="row.type !== 'direct'">
                <el-button link type="primary" :icon="Connection" @click="openTest(row)">测试</el-button>
                <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
                <el-button v-if="canWrite" link @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button>
                <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
              </template>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="dialogOpen" :title="editing ? '编辑上网出口' : '新建上网出口'" width="560px">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="14"><el-form-item label="名称" required><el-input v-model="form.name" /></el-form-item></el-col>
          <el-col :span="10"><el-form-item label="类型" required><el-select v-model="form.type" style="width: 100%"><el-option label="SOCKS5" value="socks" /><el-option label="HTTP" value="http" /></el-select></el-form-item></el-col>
          <el-col :span="16"><el-form-item label="服务器地址" required><el-input v-model="form.server" placeholder="127.0.0.1" /></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="端口" required><el-input-number v-model="form.server_port" :min="1" :max="65535" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="用户名"><el-input v-model="form.username" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item :label="editing ? '新密码（留空则不变）' : '密码'"><el-input v-model="form.password" type="password" show-password /></el-form-item></el-col>
          <el-col :span="24">
            <el-form-item label="过期时间">
              <el-date-picker v-model="form.expires_at" type="datetime" placeholder="选填，仅用于提醒" style="width: 100%" />
              <div class="subtle" style="margin-top: 6px">仅作展示提醒，到期后系统不会自动停用该出口。</div>
            </el-form-item>
          </el-col>
        </el-row>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!form.name || !form.server" @click="save">保存</el-button></template>
    </el-dialog>

    <el-dialog
      v-model="testDialog"
      :title="`检测上网出口：${testTarget?.name || ''}`"
      width="640px"
      :before-close="requestCloseTest"
      :close-on-click-modal="!testing"
      :close-on-press-escape="!testing"
      :show-close="!testing"
    >
      <el-alert
        v-if="!testResults.length"
        title="没有在线的服务器可用于检测，请先确认至少有一台服务器已接入并在线。"
        type="warning" show-icon :closable="false" />
      <template v-else>
        <el-alert
          :title="testing ? '正在让每一台在线服务器通过该出口访问测试地址，请等待全部结果返回。' : '每台服务器的网络环境不同，以下是各自通过该出口上网的检测结果。'"
          :type="testing ? 'info' : 'success'" show-icon :closable="false" style="margin-bottom: 14px" />
        <el-table :data="testResults" max-height="340">
          <el-table-column label="服务器" prop="name" min-width="160" />
          <el-table-column label="结果" width="110">
            <template #default="{ row }">
              <el-tag :type="testStates[row.state][1]">{{ testStates[row.state][0] }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="详情" min-width="260" show-overflow-tooltip>
            <template #default="{ row }">
              <el-icon v-if="row.state === 'running'" class="is-loading"><Loading /></el-icon>
              <span>{{ row.detail }}</span>
            </template>
          </el-table-column>
        </el-table>
      </template>
      <template #footer>
        <el-button :disabled="testing" @click="testDialog = false">关闭</el-button>
        <el-button type="primary" :loading="testing" :disabled="!testResults.length" @click="runTest">重新检测</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.expired-text { color: var(--el-color-danger); }
</style>
