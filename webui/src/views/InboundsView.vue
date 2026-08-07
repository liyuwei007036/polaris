<script setup>
import { computed, inject, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, DocumentCopy, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import { protocolMap } from '../protocols'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'
import ListenerFormDialog from '../components/ListenerFormDialog.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const listeners = ref([])
const outbounds = ref([])
const endpointMap = ref({})
const formOpen = ref(false)
const editing = ref(null)
const copying = ref(null)
const copySourceID = ref('')
const keyword = ref('')
const selectedNode = ref('')
const selectedStatus = ref('')

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((item) => [item.id, item.name])))
const formEndpoints = computed(() => {
  const source = editing.value?.id || copySourceID.value
  return source ? endpointMap.value[source] || [] : []
})
const filteredListeners = computed(() => listeners.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedStatus.value && String(row.enabled) !== selectedStatus.value) return false
  return includesText([nodeNames.value[row.node_id], row.name, row.connection_domain, row.port, row.spec?.protocol, row.spec?.transport?.type, securityLabel(row)], keyword.value)
}))

// The listener list already reports how many users each service has, so the
// user lists themselves are fetched only for the service being edited rather
// than for every row on every refresh.
async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [listenerResult, outboundResult] = await Promise.all([
      api('/listeners'),
      api('/outbounds').catch(() => ({ outbounds: [] })),
    ])
    listeners.value = listenerResult.listeners || []
    outbounds.value = outboundResult.outbounds || []
  } finally {
    loading.value = false
  }
}

async function loadEndpoints(listenerID) {
  const result = await api(`/listeners/${listenerID}/endpoints`).catch(() => ({ endpoints: [] }))
  endpointMap.value = { ...endpointMap.value, [listenerID]: result.endpoints || [] }
}

function securityLabel(listener) {
  if (listener.spec?.reality?.enabled) return 'Reality'
  if (listener.spec?.tls?.enabled) return 'TLS'
  return '无加密'
}

function openCreate() {
  editing.value = null
  copying.value = null
  copySourceID.value = ''
  formOpen.value = true
}

async function openEdit(listener) {
  await loadEndpoints(listener.id)
  editing.value = listener
  copying.value = null
  copySourceID.value = ''
  formOpen.value = true
}

// Copying only fills in the form. Nothing is created until the operator picks
// the target server and saves, so a copy can be adjusted first.
async function openCopy(listener) {
  await loadEndpoints(listener.id)
  editing.value = null
  copySourceID.value = listener.id
  copying.value = {
    ...listener,
    id: '',
    name: `${listener.name} 副本`,
    listen_address: '0.0.0.0',
    backend_port: 0,
  }
  formOpen.value = true
}

async function saveListener(payload) {
  saving.value = true
  try {
    if (editing.value) {
      await put(`/listeners/${editing.value.id}`, payload.listener)
      await syncAccounts(editing.value.id, payload.accounts)
    } else {
      const accounts = payload.accounts.map(({ name, alias, enabled, outbound_id }) => ({ name, alias, enabled, outbound_id }))
      await post('/listeners/quick', { listener: payload.listener, accounts })
    }
    ElMessage.success(editing.value ? '接入服务已保存，正在自动应用' : copying.value ? '接入服务已复制并创建，正在自动应用' : '接入服务已创建，正在自动应用')
    formOpen.value = false
    copying.value = null
    copySourceID.value = ''
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '接入服务保存失败，请检查填写内容后重试')
  } finally {
    saving.value = false
  }
}

async function syncAccounts(listenerID, accounts) {
  const existing = endpointMap.value[listenerID] || []
  const existingByID = new Map(existing.map((endpoint) => [endpoint.id, endpoint]))
  const retained = new Set()
  for (const account of accounts) {
    if (!account.id) {
      const created = await post(`/listeners/${listenerID}/endpoints/quick`, {
        name: account.name,
        alias: account.alias,
        outbound_id: account.outbound_id,
      })
      if (!account.enabled) {
        await put(`/endpoints/${created.id}`, {
          listener_id: listenerID,
          name: account.name,
          alias: account.alias,
          enabled: false,
          outbound_id: account.outbound_id,
        })
      }
      continue
    }
    retained.add(account.id)
    const current = existingByID.get(account.id)
    if (!current) continue
    if (current.name !== account.name || (current.alias || '') !== account.alias || current.enabled !== account.enabled || (current.outbound_id || 'direct') !== account.outbound_id) {
      await put(`/endpoints/${account.id}`, {
        listener_id: listenerID,
        name: account.name,
        alias: account.alias,
        enabled: account.enabled,
        outbound_id: account.outbound_id,
      })
    }
  }
  for (const endpoint of existing) {
    if (!retained.has(endpoint.id)) await del(`/endpoints/${endpoint.id}`)
  }
}

async function toggle(listener) {
  await post(`/listeners/${listener.id}/enabled`, { enabled: !listener.enabled })
  ElMessage.success('状态已更新，系统正在自动应用')
  await load()
}

async function removeListener(listener) {
  await ElMessageBox.confirm(
    `删除“${listener.name}”会同时删除其中的所有用户，且无法恢复。`,
    '删除接入服务',
    { type: 'warning', confirmButtonText: '确认删除' },
  )
  await del(`/listeners/${listener.id}`)
  ElMessage.success('接入服务已删除，正在自动应用')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="接入服务">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建</el-button>
    </PageHeader>

    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索服务、协议或端口" style="width: 260px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 180px"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select>
        <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredListeners" :loading="loading" empty-text="还没有接入服务">
          <el-table-column label="服务器" min-width="150" show-overflow-tooltip><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
          <el-table-column label="服务名称" min-width="200">
            <template #default="{ row }">
              <div class="inbound-name">{{ row.name }}</div>
              <div class="inbound-meta">
                <el-tag size="small">{{ protocolMap[row.spec?.protocol]?.label || row.spec?.protocol }}</el-tag>
                <el-tag size="small" type="info">{{ (row.spec?.network || 'tcp').toUpperCase() }}</el-tag>
                <el-tag size="small" type="info">{{ securityLabel(row) }}</el-tag>
                <el-tag v-if="row.spec?.transport?.type" size="small" type="warning">{{ row.spec.transport.type }}</el-tag>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="端口" width="96">
            <template #default="{ row }"><span class="mono">{{ row.port }}</span></template>
          </el-table-column>
          <el-table-column label="连接域名" min-width="180" show-overflow-tooltip>
            <template #default="{ row }"><span class="mono">{{ row.connection_domain || '使用服务器地址' }}</span></template>
          </el-table-column>
          <el-table-column label="用户" width="84" align="center">
            <template #default="{ row }">{{ row.endpoint_count ?? 0 }}</template>
          </el-table-column>
          <el-table-column label="状态" width="88">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="264" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button v-if="canWrite" link :icon="DocumentCopy" @click="openCopy(row)">复制</el-button>
              <el-button v-if="canWrite" link @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeListener(row)">删除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <ListenerFormDialog
      v-model="formOpen"
      :listener="editing"
      :template="copying"
      :nodes="appState.nodes"
      :outbounds="outbounds"
      :endpoints="formEndpoints"
      :saving="saving"
      @save="saveListener"
    />
  </div>
</template>
