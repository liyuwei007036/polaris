<script setup>
import { computed, inject, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, UserFilled } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { protocolMap } from '../protocols'
import PageHeader from '../components/PageHeader.vue'
import ListenerFormDialog from '../components/ListenerFormDialog.vue'
import CredentialDialog from '../components/CredentialDialog.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const listeners = ref([])
const outbounds = ref([])
const certificates = ref([])
const realityKeys = ref([])
const endpointMap = ref({})
const ingressRoutes = ref([])
const formOpen = ref(false)
const credentialOpen = ref(false)
const editing = ref(null)
const credentialTarget = ref(null)

const outboundNames = computed(() => Object.fromEntries(outbounds.value.map((item) => [item.id, item.name])))
const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((item) => [item.id, item.name])))
const ingressMap = computed(() => Object.fromEntries(ingressRoutes.value.map((item) => [item.listener_id, item])))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [listenerResult, outboundResult, certificateResult, realityResult, ingressResult] = await Promise.all([
      api('/listeners'),
      api('/outbounds').catch(() => ({ outbounds: [] })),
      api('/certificates').catch(() => ({ certificates: [] })),
      api('/reality-keys').catch(() => ({ reality_keys: [] })),
	  api('/ingress-routes').catch(() => ({ ingress_routes: [] })),
    ])
    listeners.value = listenerResult.listeners || []
    outbounds.value = outboundResult.outbounds || []
    certificates.value = (certificateResult.certificates || []).filter((item) => item.enabled)
    realityKeys.value = (realityResult.reality_keys || []).filter((item) => item.enabled)
	ingressRoutes.value = ingressResult.ingress_routes || []
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
	  const route = ingressMap.value[editing.value.id]
	  if (route && payload.ingress_route) await put(`/ingress-routes/${route.id}`, { ...payload.ingress_route, node_id: editing.value.node_id, listener_id: editing.value.id })
    } else {
	  await post('/listeners/quick', { listener: payload.listener, accounts: payload.accounts, ingress_route: payload.ingress_route })
    }
    ElMessage.success(editing.value ? '接入服务已保存，正在自动应用' : '接入服务已创建，正在自动应用')
    formOpen.value = false
    await load()
  } finally {
    saving.value = false
  }
}

function openCredential(listener) {
  credentialTarget.value = listener
  credentialOpen.value = true
}

async function saveCredential(payload) {
  saving.value = true
  try {
    await post(`/listeners/${credentialTarget.value.id}/endpoints/quick`, payload)
    ElMessage.success('接入用户已添加，正在自动应用')
    credentialOpen.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function changeEndpointOutbound(listener, endpoint, outboundID) {
  saving.value = true
  try {
    await put(`/endpoints/${endpoint.id}`, {
      listener_id: listener.id,
      name: endpoint.name,
      enabled: endpoint.enabled,
      outbound_id: outboundID,
    })
    ElMessage.success(`用户“${endpoint.name}”的上网出口已更新，正在自动应用`)
    await load()
  } finally {
    saving.value = false
  }
}

function endpointOutboundLabel(endpoint) {
  if (endpoint.outbound_id === 'direct') return '服务器直连'
  if (endpoint.outbound_id) return outboundNames.value[endpoint.outbound_id] || '出口已失效'
  return '服务器直连'
}

async function toggle(listener) {
  await post(`/listeners/${listener.id}/enabled`, { enabled: !listener.enabled })
  ElMessage.success('状态已更新，系统正在自动应用')
  await load()
}

async function removeListener(listener) {
  await ElMessageBox.confirm(
    `删除“${listener.name}”会同时删除其中的所有用户和端口共享设置，且无法恢复。`,
    '删除接入服务',
    { type: 'warning', confirmButtonText: '确认删除' },
  )
  await del(`/listeners/${listener.id}`)
  ElMessage.success('接入服务已删除，正在自动应用')
  await load()
}

async function removeEndpoint(listener, endpoint) {
  await ElMessageBox.confirm(`删除用户“${endpoint.name}”后，客户端将无法继续连接。`, '删除用户', {
    type: 'warning',
    confirmButtonText: '确认删除',
  })
  await del(`/endpoints/${endpoint.id}`)
  ElMessage.success('接入用户已删除，正在自动应用')
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
          <el-table-column type="expand">
            <template #default="{ row }">
              <div style="padding: 8px 24px 18px 58px">
                <div class="toolbar" style="margin-bottom: 8px">
                  <strong>接入用户</strong>
                  <span class="subtle">共 {{ endpointMap[row.id]?.length || 0 }} 个</span>
                  <span class="toolbar__spacer" />
                  <el-button v-if="canWrite" size="small" :icon="UserFilled" @click="openCredential(row)">添加用户</el-button>
                </div>
                <p class="subtle">每个用户都有独立的连接信息，并可单独选择上网出口。</p>
                <div v-if="endpointMap[row.id]?.length" class="credential-list">
                  <div v-for="endpoint in endpointMap[row.id]" :key="endpoint.id" class="credential-row">
                    <span class="credential-row__name">{{ endpoint.name }}</span>
                    <el-tag :type="endpoint.enabled ? 'success' : 'info'" size="small">{{ endpoint.enabled ? '启用' : '停用' }}</el-tag>
                    <el-select
                      :model-value="endpoint.outbound_id || 'direct'"
                      size="small"
                      style="width: 190px"
                      :placeholder="endpointOutboundLabel(endpoint)"
                      @change="changeEndpointOutbound(row, endpoint, $event)"
                    >
                      <el-option label="服务器直连" value="direct" />
                      <el-option
                        v-for="outbound in outbounds.filter((item) => item.enabled && item.type !== 'direct')"
                        :key="outbound.id"
                        :label="outbound.name"
                        :value="outbound.id"
                      />
                    </el-select>
                    <el-button
                      v-if="canWrite"
                      text
                      type="danger"
                      :icon="Delete"
                      @click="removeEndpoint(row, endpoint)"
                    >
                      删除
                    </el-button>
                  </div>
                </div>
                <el-empty v-else :image-size="48" description="还没有用户，客户端暂时无法连接" />
              </div>
            </template>
          </el-table-column>
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
            <template #default="{ row }"><span class="mono">{{ row.port }}</span><el-tag v-if="ingressMap[row.id]" size="small" type="warning" style="margin-left: 6px">共享端口</el-tag></template>
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
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
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
	  :ingress-route="editing ? ingressMap[editing.id] : null"
      :nodes="appState.nodes"
      :certificates="certificates"
      :reality-keys="realityKeys"
      :outbounds="outbounds"
      :saving="saving"
      @save="saveListener"
    />

    <CredentialDialog
      v-if="credentialTarget"
      v-model="credentialOpen"
      :saving="saving"
      :outbounds="outbounds"
      @save="saveCredential"
    />
  </div>
</template>
