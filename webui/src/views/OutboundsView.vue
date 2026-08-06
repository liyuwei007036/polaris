<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, friendlyError, post, put } from '../api'
import { includesText } from '../format'
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
const testNodeID = ref('')
const form = reactive({ name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true })
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
function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', type: 'socks', server: '', server_port: 1080, username: '', password: '', enabled: true })
  dialogOpen.value = true
}
function openEdit(row) {
  editing.value = row
  Object.assign(form, { ...row, password: '' })
  dialogOpen.value = true
}
async function save() {
  saving.value = true
  try {
    const payload = { ...form, server_port: Number(form.server_port) }
    if (editing.value) await put(`/outbounds/${editing.value.id}`, payload)
    else await post('/outbounds', payload)
    ElMessage.success(editing.value ? '上网出口已保存' : '上网出口已创建')
    dialogOpen.value = false
    await load()
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
async function openTest(row) {
  await loadNodes()
  testTarget.value = row
  testNodeID.value = appState.nodes.find((node) => node.online)?.id || ''
  testDialog.value = true
}
async function runTest() {
  testing.value = true
  try {
    const task = await post(`/outbounds/${testTarget.value.id}/test`, { node_id: testNodeID.value })
    const result = await waitForTask(task.id, 30000)
    if (result.status !== 'succeeded') throw new Error(result.result_summary ? friendlyError(result.result_summary) : '上网出口检测未通过')
    ElMessage.success(/[\u3400-\u9fff]/.test(result.result_summary || '') ? result.result_summary : '上网出口可用，连接检测已通过')
    testDialog.value = false
  } finally { testing.value = false }
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
        </el-row>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!form.name || !form.server" @click="save">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="testDialog" title="检测上网出口" width="520px">
      <el-form label-position="top">
        <el-form-item label="上网出口"><el-input :model-value="testTarget?.name" disabled /></el-form-item>
        <el-form-item label="测试服务器" required>
          <el-select v-model="testNodeID" style="width: 100%" placeholder="请选择在线服务器">
            <el-option v-for="node in appState.nodes.filter((item) => item.online)" :key="node.id" :value="node.id" :label="node.name" />
          </el-select>
        </el-form-item>
        <el-alert title="所选服务器会通过此出口访问测试地址，以确认能否正常上网并测量响应时间。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="testDialog = false">取消</el-button>
        <el-button type="primary" :loading="testing" :disabled="!testNodeID" @click="runTest">检测</el-button>
      </template>
    </el-dialog>
  </div>
</template>
