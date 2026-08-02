<script setup>
import { inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Edit, Plus, Refresh, RemoveFilled, Upload } from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { subscribeLive } from '../live'
import PageHeader from '../components/PageHeader.vue'

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
const releaseDialog = ref(false)
const latestRelease = ref(null)
const releaseNode = ref(null)
const addressDialog = ref(false)
const addressNode = ref(null)
const clientAddress = ref('')
const addressSaving = ref(false)
const nameDialog = ref(false)
const nameNode = ref(null)
const nodeName = ref('')
const nameSaving = ref(false)
const rates = ref({})
const refreshing = ref(false)
const previousCounters = new Map()
const now = ref(Date.now())
let stopLive

function formatBytes(value, suffix = '') {
  let number = Number(value || 0)
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let index = 0
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024
    index += 1
  }
  return `${index ? number.toFixed(number < 10 ? 2 : 1) : Math.round(number)} ${units[index]}${suffix}`
}

function relativeTime(value) {
  const seconds = Math.max(0, Math.floor((now.value - Date.parse(value)) / 1000))
  if (!Number.isFinite(seconds)) return '从未'
  if (seconds < 5) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  return `${Math.floor(seconds / 3600)} 小时前`
}

function healthInfo(node) {
  if (!node.online) return ['离线', 'info']
  const health = metrics.value[node.id]?.health
  if (health?.status === 'degraded') {
    if (health.sing_box_service === 'active' && !health.clash_api_available) return ['连接数据异常', 'warning']
    if (!health.traffic_available) return ['流量统计不可用', 'warning']
    return ['检测数据不完整', 'warning']
  }
  return {
    healthy: ['正常', 'success'],
    stopped: ['连接服务已停止', 'danger'],
  }[health?.status] || ['等待检测', 'info']
}

function updateRates(nodeID, report) {
  if (!report?.node || !report.collected_at) return
  const current = {
    received: Number(report.node.received_bytes || 0),
    sent: Number(report.node.sent_bytes || 0),
    time: Date.parse(report.collected_at),
  }
  const previous = previousCounters.get(nodeID)
  if (previous && current.time > previous.time) {
    const seconds = (current.time - previous.time) / 1000
    rates.value[nodeID] = {
      received: Math.max(0, current.received - previous.received) / seconds,
      sent: Math.max(0, current.sent - previous.sent) / seconds,
    }
  }
  previousCounters.set(nodeID, current)
}

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  now.value = Date.now()
  try {
    await loadNodes()
    if (isAdmin.value) {
      pending.value = (await api('/registrations').catch(() => ({ registrations: [] }))).registrations || []
    }
    const metricPairs = await Promise.all(
      appState.nodes.map(async (node) => {
        const result = await api(`/nodes/${node.id}/metrics`).catch(() => null)
        return [node.id, result?.report || null]
      }),
    )
    metrics.value = Object.fromEntries(metricPairs)
    metricPairs.forEach(([nodeID, report]) => updateRates(nodeID, report))
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

async function openInstall(node) {
  releaseNode.value = node
  latestRelease.value = await api(`/sing-box/latest?architecture=${encodeURIComponent(node.architecture)}`)
  releaseDialog.value = true
}

async function install() {
  await post(`/nodes/${releaseNode.value.id}/sing-box/install`, { version: '' })
  ElMessage.success('安装或升级操作已开始')
  releaseDialog.value = false
}

function openClientAddress(node) {
  addressNode.value = node
  clientAddress.value = node.client_address || ''
  addressDialog.value = true
}

function openNodeName(node) {
  nameNode.value = node
  nodeName.value = node.name
  nameDialog.value = true
}

async function saveNodeName() {
  nameSaving.value = true
  try {
    await put(`/nodes/${nameNode.value.id}/name`, { name: nodeName.value.trim() })
    ElMessage.success('服务器名称已保存')
    nameDialog.value = false
    await load()
  } finally {
    nameSaving.value = false
  }
}

async function saveClientAddress() {
  addressSaving.value = true
  try {
    await put(`/nodes/${addressNode.value.id}/client-address`, { address: clientAddress.value })
    ElMessage.success('客户端连接地址已保存')
    addressDialog.value = false
    await load()
  } finally {
    addressSaving.value = false
  }
}

onMounted(() => {
  load()
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node' || event.kind === 'connections') load(true).catch(() => {})
  })
})

onBeforeUnmount(() => {
  stopLive?.()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="服务器" description="添加和管理运行连接服务的服务器">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="generateToken">添加服务器</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <template v-if="pending.length">
        <div class="section-title">等待确认的服务器</div>
        <div class="table-panel" style="margin-bottom: 24px">
          <el-table :data="pending">
            <el-table-column label="服务器名称" min-width="180" prop="node_name" />
            <el-table-column label="支持的功能" min-width="260" prop="capabilities" show-overflow-tooltip />
            <el-table-column label="接入时间" width="200" prop="created_at" />
            <el-table-column label="操作" width="120">
              <template #default="{ row }"><el-button type="primary" link @click="approve(row)">允许接入</el-button></template>
            </el-table-column>
          </el-table>
        </div>
      </template>

      <div class="section-title">已接入服务器</div>
      <div class="table-panel">
        <el-table :data="appState.nodes">
          <el-table-column label="服务器" min-width="180">
            <template #default="{ row }">
              <span class="status-dot" :class="row.online ? 'online' : 'offline'" />
              <strong>{{ row.name }}</strong>
            </template>
          </el-table-column>
          <el-table-column label="系统" min-width="150">
            <template #default="{ row }">{{ row.os || '—' }} · {{ row.architecture || '—' }}</template>
          </el-table-column>
          <el-table-column label="管理组件版本" width="130" prop="agent_version" />
          <el-table-column label="连接服务版本" width="130" prop="sing_box_version" />
          <el-table-column label="客户端连接地址" min-width="190">
            <template #default="{ row }"><span v-if="row.client_address" class="mono">{{ row.client_address }}</span><el-tag v-else type="warning">未配置</el-tag></template>
          </el-table-column>
          <el-table-column label="运行状态" width="135">
            <template #default="{ row }"><el-tag :type="healthInfo(row)[1]">{{ healthInfo(row)[0] }}</el-tag></template>
          </el-table-column>
          <el-table-column label="当前下载 / 上传" min-width="180">
            <template #default="{ row }">
              <span v-if="rates[row.id]" class="mono">↓ {{ formatBytes(rates[row.id].received, '/s') }} · ↑ {{ formatBytes(rates[row.id].sent, '/s') }}</span>
              <span v-else class="subtle">正在计算</span>
            </template>
          </el-table-column>
          <el-table-column label="累计下载 / 上传" min-width="190">
            <template #default="{ row }">
              <span v-if="metrics[row.id]?.node" class="mono">↓ {{ formatBytes(metrics[row.id].node.received_bytes) }} · ↑ {{ formatBytes(metrics[row.id].node.sent_bytes) }}</span>
              <span v-else class="subtle">等待上报</span>
            </template>
          </el-table-column>
          <el-table-column label="连接数" width="90">
            <template #default="{ row }">{{ metrics[row.id]?.connections?.length ?? '—' }}</template>
          </el-table-column>
          <el-table-column label="最后在线" width="125"><template #default="{ row }">{{ relativeTime(row.last_seen_at) }}</template></el-table-column>
          <el-table-column label="操作" width="420" fixed="right">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="openNodeName(row)">修改名称</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="openClientAddress(row)">连接地址</el-button>
              <el-button v-if="isAdmin" link :icon="Upload" @click="openInstall(row)">安装或升级</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="RemoveFilled" @click="revoke(row)">移除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
      <p class="subtle">网络速度和累计用量是服务器全部网络接口的总计，不代表某个接入服务或用户的单独用量。</p>
    </main>

    <el-dialog v-model="tokenDialog" title="服务器接入信息" width="580px">
      <el-alert title="此信息只显示一次，请立即复制并在要添加的服务器上使用。" type="warning" show-icon :closable="false" />
      <el-input v-model="token" readonly type="textarea" :rows="4" class="mono" style="margin-top: 16px" />
      <p class="subtle">有效期至：{{ expiresAt }}</p>
      <template #footer>
        <el-button @click="tokenDialog = false">完成</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyToken">复制接入信息</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="nameDialog" title="修改服务器名称" width="460px">
      <el-form label-position="top">
        <el-form-item label="服务器名称" required>
          <el-input v-model="nodeName" maxlength="128" @keyup.enter="saveNodeName" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="nameDialog = false">取消</el-button>
        <el-button type="primary" :loading="nameSaving" :disabled="!nodeName.trim()" @click="saveNodeName">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="releaseDialog" title="安装或升级连接服务" width="500px">
      <el-form label-position="top">
        <el-form-item label="目标服务器"><el-input :model-value="releaseNode?.name" disabled /></el-form-item>
        <el-form-item label="最新稳定版本"><el-input :model-value="latestRelease ? `${latestRelease.version} · ${latestRelease.architecture}` : '正在获取'" disabled /></el-form-item>
        <el-alert title="系统会从 sing-box 官方来源获取最新稳定版本，完成安全校验后再安装。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="releaseDialog = false">取消</el-button>
        <el-button type="primary" :disabled="!latestRelease" @click="install">开始安装</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="addressDialog" title="客户端连接地址" width="500px">
      <el-form label-position="top">
        <el-form-item label="服务器"><el-input :model-value="addressNode?.name" disabled /></el-form-item>
        <el-form-item label="客户端连接域名或 IP 地址" required>
          <el-input v-model="clientAddress" placeholder="例如：proxy.example.com 或 203.0.113.10" />
        </el-form-item>
        <el-alert title="只填写域名或 IP 地址，不要添加 http://、端口或路径。生成客户端配置时会使用此地址。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="addressDialog = false">取消</el-button>
        <el-button type="primary" :loading="addressSaving" :disabled="!clientAddress.trim()" @click="saveClientAddress">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
