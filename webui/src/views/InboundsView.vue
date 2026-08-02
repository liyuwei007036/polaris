<script setup>
import { computed, inject, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { protocolMap } from '../protocols'
import PageHeader from '../components/PageHeader.vue'
import ListenerFormDialog from '../components/ListenerFormDialog.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const listeners = ref([])
const outbounds = ref([])
const certificates = ref([])
const endpointMap = ref({})
const formOpen = ref(false)
const editing = ref(null)

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((item) => [item.id, item.name])))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [listenerResult, outboundResult, certificateResult] = await Promise.all([
      api('/listeners'),
      api('/outbounds').catch(() => ({ outbounds: [] })),
      api('/certificates').catch(() => ({ certificates: [] })),
    ])
    listeners.value = listenerResult.listeners || []
    outbounds.value = outboundResult.outbounds || []
    certificates.value = (certificateResult.certificates || []).filter((item) => item.enabled)
    const pairs = await Promise.all(
      listeners.value.map(async (listener) => {
        const result = await api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))
        return [listener.id, result.endpoints || []]
      }),
    )
    endpointMap.value = Object.fromEntries(pairs)
  } finally {
    loading.value = false
  }
}

function securityLabel(listener) {
  if (listener.spec?.reality?.enabled) return 'Reality'
  if (listener.spec?.tls?.enabled) return 'TLS'
  return '无加密'
}

function openCreate() {
  editing.value = null
  formOpen.value = true
}

function openEdit(listener) {
  editing.value = listener
  formOpen.value = true
}

async function saveListener(payload) {
  saving.value = true
  try {
    if (editing.value) {
      await put(`/listeners/${editing.value.id}`, payload.listener)
      await syncAccounts(editing.value.id, payload.accounts)
    } else {
      await post('/listeners/quick', { listener: payload.listener, accounts: payload.accounts })
    }
    ElMessage.success(editing.value ? '接入服务已保存，正在自动应用' : '接入服务已创建，正在自动应用')
    formOpen.value = false
    await load()
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
    <PageHeader title="接入服务" description="创建客户端用于连接服务器的服务，并为不同用户指定上网出口">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建接入服务</el-button>
    </PageHeader>

    <main class="page-content page-content--tight">
      <p class="subtle">这里显示全部服务器的接入服务。保存、停用或删除后，系统会自动检查并应用。</p>

      <div class="table-panel">
        <el-table v-loading="loading" :data="listeners" row-key="id">
          <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
          <el-table-column label="服务名称" min-width="190">
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
          <el-table-column label="端口" width="110">
            <template #default="{ row }"><span class="mono">{{ row.port }}</span></template>
          </el-table-column>
          <el-table-column label="用户" width="90" align="center">
            <template #default="{ row }">{{ endpointMap[row.id]?.length || 0 }}</template>
          </el-table-column>
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="220" fixed="right">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">修改</el-button>
              <el-button v-if="canWrite" link @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeListener(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </main>

    <ListenerFormDialog
      v-model="formOpen"
      :listener="editing"
      :nodes="appState.nodes"
      :certificates="certificates"
      :outbounds="outbounds"
      :endpoints="editing ? endpointMap[editing.id] || [] : []"
      :saving="saving"
      @save="saveListener"
    />
  </div>
</template>
