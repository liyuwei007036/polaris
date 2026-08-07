<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Edit, Plus, Refresh, RemoveFilled, Search, Top } from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { formatBytes, formatDateTime, includesText } from '../format'
import { regionFlag } from '../flags'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const canWrite = inject('canWrite')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const pending = ref([])
const metrics = ref({})
const tokenDialog = ref(false)
const token = ref('')
const expiresAt = ref('')
const lifetime = ref(900)
const editDialog = ref(false)
const editNode = ref(null)
const editForm = ref({ name: '', client_address: '' })
const editSaving = ref(false)
const refreshing = ref(false)
const keyword = ref('')
const statusFilter = ref('')
let stopLive
let stopConnections

const filteredNodes = computed(() => appState.nodes.filter((node) => {
  if (statusFilter.value === 'online' && !node.online) return false
  if (statusFilter.value === 'offline' && node.online) return false
  return includesText([node.name, node.client_address, node.os, node.architecture, node.agent_version, node.sing_box_version], keyword.value)
}))
const filteredPending = computed(() => pending.value.filter((row) => includesText([row.node_name, row.capabilities], keyword.value)))
// Rates and connection counts arrive over SSE as the agents measure them, so
// they update in place instead of being recomputed on every page visit.
const live = computed(() => connectionSnapshots.value)

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    await loadNodes()
    const [registrations, metricResult] = await Promise.all([
      isAdmin.value ? api('/registrations').catch(() => ({ registrations: [] })) : Promise.resolve({ registrations: [] }),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
    ])
    pending.value = registrations.registrations || []
    metrics.value = Object.fromEntries((metricResult.nodes || []).map((entry) => [entry.node_id, entry.report]))
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

async function generateToken() {
  const result = await post('/nodes/registration-tokens', { lifetime_seconds: Number(lifetime.value) })
  token.value = result.token
  expiresAt.value = result.expires_at
  tokenDialog.value = true
}

async function copyToken() {
  await navigator.clipboard.writeText(token.value)
  ElMessage.success('服务器接入信息已复制')
}

async function approve(registration) {
  await ElMessageBox.confirm(`允许服务器“${registration.node_name}”接入管理平台？批准后即可远程管理该服务器。`, '确认服务器接入')
  await post(`/nodes/${registration.id}/approve`, {})
  ElMessage.success('服务器已接入')
  await load()
}

async function revoke(node) {
  await ElMessageBox.confirm(`移除“${node.name}”后，该服务器将无法继续连接管理平台。`, '移除服务器', {
    type: 'warning',
    confirmButtonText: '确认移除',
  })
  await post(`/nodes/${node.id}/revoke`, {})
  ElMessage.success('服务器已移除')
  await load()
}

function agentUpdateAvailable(node) {
  const latest = appState.systemUpdate?.latest_version
  return Boolean(latest && node.agent_version && node.agent_version !== latest)
}

async function upgradeAgent(node) {
  await ElMessageBox.confirm(
    `将“${node.name}”的 agent 升级到最新版本？升级完成后 agent 会自动重启并重新连接，运行中的代理服务不受影响。`,
    '升级 Agent',
    { type: 'warning', confirmButtonText: '开始升级' },
  )
  await post(`/nodes/${node.id}/agent/upgrade`, {})
  ElMessage.success('升级任务已下发，agent 更新完成后会自动重新上线')
}

function openEdit(node) {
  editNode.value = node
  editForm.value = { name: node.name, client_address: node.client_address || '' }
  editDialog.value = true
}

async function saveNode() {
  editSaving.value = true
  try {
    await put(`/nodes/${editNode.value.id}`, {
      name: editForm.value.name.trim(),
      client_address: editForm.value.client_address.trim(),
    })
    ElMessage.success('服务器信息已保存')
    editDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器信息保存失败')
  } finally {
    editSaving.value = false
  }
}

onMounted(() => {
  load()
  stopConnections = subscribeConnections(() => {})
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node') load(true).catch(() => {})
  })
})

onBeforeUnmount(() => {
  stopLive?.()
  stopConnections?.()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="服务器">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="generateToken">添加</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content page-content--tight">
      <div class="search-toolbar search-toolbar--standalone">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或版本" style="width: 280px" />
        <el-select v-model="statusFilter" clearable placeholder="全部状态" style="width: 150px"><el-option label="在线" value="online" /><el-option label="离线" value="offline" /></el-select>
      </div>
      <template v-if="pending.length">
        <div class="section-title">等待确认的服务器</div>
        <div class="table-panel table-panel--fixed" style="margin-bottom: 24px">
          <PagedTable :rows="filteredPending" :page-size="5" empty-text="没有等待确认的服务器">
            <el-table-column label="服务器名称" min-width="180" prop="node_name" />
            <el-table-column label="支持的功能" min-width="260" prop="capabilities" show-overflow-tooltip />
            <el-table-column label="接入时间" width="180"><template #default="{ row }">{{ formatDateTime(row.created_at) }}</template></el-table-column>
            <el-table-column label="操作" width="90" class-name="action-column">
              <template #default="{ row }"><el-button type="primary" link @click="approve(row)">接入</el-button></template>
            </el-table-column>
          </PagedTable>
        </div>
      </template>

      <div class="section-title">已接入服务器</div>
      <div class="table-panel">
        <PagedTable :rows="filteredNodes" empty-text="尚未接入任何服务器">
          <!-- 同一类信息合并成两行，整张表就能在常见宽度里放下，
               不必靠横向滚动去看被固定列挡住的列。 -->
          <el-table-column label="服务器 / 系统" min-width="190" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="cell-main">
                <span class="status-dot" :class="row.online ? 'online' : 'offline'" />
                <span v-if="regionFlag(row.name)" class="region-flag">{{ regionFlag(row.name) }}</span>
                <strong>{{ row.name }}</strong>
              </div>
              <div class="cell-sub">{{ row.os || '—' }} · {{ row.architecture || '—' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="150">
            <template #default="{ row }">
              <div class="cell-main">
                {{ row.agent_version || '—' }}
                <el-tag v-if="agentUpdateAvailable(row)" type="warning" size="small" style="margin-left: 6px">可升级</el-tag>
              </div>
              <div class="cell-sub">sing-box {{ row.sing_box_version || '—' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="客户端连接地址" min-width="170" show-overflow-tooltip>
            <template #default="{ row }"><span v-if="row.client_address" class="mono">{{ row.client_address }}</span><el-tag v-else type="warning">未配置</el-tag></template>
          </el-table-column>
          <el-table-column label="实时 / 累计流量" min-width="196" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="cell-main mono">
                <template v-if="live.get(row.id)?.has_rates">↓ {{ formatBytes(live.get(row.id).received_rate, '/s') }} · ↑ {{ formatBytes(live.get(row.id).sent_rate, '/s') }}</template>
                <span v-else class="subtle">{{ row.online ? '等待上报' : '离线' }}</span>
              </div>
              <div class="cell-sub mono">
                <template v-if="metrics[row.id]?.proxy">↓ {{ formatBytes(metrics[row.id].proxy.received_bytes) }} · ↑ {{ formatBytes(metrics[row.id].proxy.sent_bytes) }}</template>
                <template v-else>等待上报</template>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="连接数" width="82" align="center">
            <template #default="{ row }">{{ live.get(row.id)?.connection_count ?? '—' }}</template>
          </el-table-column>
          <el-table-column label="最后在线" width="152"><template #default="{ row }">{{ formatDateTime(row.last_seen_at, '从未') }}</template></el-table-column>
          <el-table-column label="操作" width="200" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button v-if="isAdmin && agentUpdateAvailable(row)" link type="primary" :icon="Top" @click="upgradeAgent(row)">升级</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="RemoveFilled" @click="revoke(row)">移除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="tokenDialog" title="服务器接入信息" width="580px">
      <el-alert title="此信息只显示一次，请立即复制。" type="warning" show-icon :closable="false" />
      <el-input v-model="token" readonly type="textarea" :rows="4" class="mono" style="margin-top: 16px" />
      <p class="subtle">有效期至：{{ formatDateTime(expiresAt) }}</p>
      <template #footer>
        <el-button @click="tokenDialog = false">完成</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyToken">复制</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editDialog" title="编辑服务器" width="520px">
      <el-form label-position="top">
        <el-form-item label="服务器名称" required>
          <el-input v-model="editForm.name" maxlength="128" placeholder="例如：香港节点 01" />
        </el-form-item>
        <el-form-item label="客户端连接域名或 IP 地址">
          <el-input v-model="editForm.client_address" placeholder="例如：proxy.example.com 或 203.0.113.10" @keyup.enter="saveNode" />
        </el-form-item>
        <el-alert title="接入时会自动填入来源 IP。仅填域名或 IP，不含 http://、端口与路径。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="editDialog = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" :disabled="!editForm.name.trim()" @click="saveNode">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
