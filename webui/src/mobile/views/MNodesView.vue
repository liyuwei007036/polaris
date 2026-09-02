<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Check, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, post, put } from '../../api'
import { writeClipboard } from '../../clipboard'
import { formatBytes, formatDateTime, includesText } from '../../format'
import { regionFlag } from '../../flags'
import { subscribeLive } from '../../live'
import { connectionSnapshots, subscribeConnections } from '../../connections'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MActionSheet from '../components/MActionSheet.vue'

const MASTER_HOST_KEY = 'polaris_master_host'
const appState = inject('appState')
const isAdmin = inject('isAdmin')
const canWrite = inject('canWrite')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const refreshing = ref(false)
const pending = ref([])
const metrics = ref({})
const keyword = ref('')
// 从看板的「在线服务器」「N 台离线」点进来时直接落在筛好的那一档上，
// 否则用户得再点一次分段控件才看到自己刚才点的那批。
const statusFilter = ref(new URLSearchParams(location.hash.split('?')[1] || '').get('status') || '')
const visible = ref(20)

const tokenSheet = ref(false)
const token = ref('')
const expiresAt = ref('')
const lifetime = 900
const masterPublicKey = ref('')
const masterHost = ref('')
const agentPort = ref(19994)

const editSheet = ref(false)
const editNode = ref(null)
const editForm = ref({ name: '', client_address: '' })
const editSaving = ref(false)

const actionsOpen = ref(false)
const actionTarget = ref(null)
// 架构 -> 官方最新 sing-box 版本号
const singBoxLatest = ref({})

let stopLive
let stopConnections

const filteredNodes = computed(() => appState.nodes.filter((node) => {
  if (statusFilter.value === 'online' && !node.online) return false
  if (statusFilter.value === 'offline' && node.online) return false
  return includesText([node.name, node.client_address, node.os, node.architecture, node.agent_version, node.sing_box_version], keyword.value)
}))
const shownNodes = computed(() => filteredNodes.value.slice(0, visible.value))
const filteredPending = computed(() => pending.value.filter((row) => includesText([row.node_name, row.capabilities], keyword.value)))
const live = computed(() => connectionSnapshots.value)

watch([keyword, statusFilter], () => { visible.value = 20 })

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
    await loadSingBoxLatest()
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

// 控制台的域名只是 agent 拨号地址的一个猜测：控制台常在反向代理后面，
// 和 Noise 端口未必同名，所以这里可改，命令跟着改。
const masterAddress = computed(() => {
  const host = masterHost.value.trim().replace(/[^A-Za-z0-9.:_[\]-]/g, '')
  return `${host}:${agentPort.value}`
})
watch(masterHost, (value) => {
  const host = value.trim()
  if (host) window.localStorage.setItem(MASTER_HOST_KEY, host)
})
const installCommand = computed(() => [
  'curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh',
  ` && sudo env POLARIS_MASTER_ADDRESS='${masterAddress.value}'`,
  ` POLARIS_MASTER_PUBKEY='${masterPublicKey.value}'`,
  ` POLARIS_REGISTRATION_TOKEN='${token.value}'`,
  ' bash install.sh agent',
].join(''))

async function generateToken() {
  const result = await post('/nodes/registration-tokens', { lifetime_seconds: lifetime })
  token.value = result.token
  expiresAt.value = result.expires_at
  masterPublicKey.value = result.master_public_key || ''
  agentPort.value = result.agent_port || 19994
  masterHost.value = window.localStorage.getItem(MASTER_HOST_KEY) || window.location.hostname
  tokenSheet.value = true
}

async function copyInstallCommand() {
  try {
    await writeClipboard(installCommand.value)
    ElMessage.success('安装命令已复制')
  } catch {
    ElMessage.error('自动复制失败，请长按上面的命令手动复制')
  }
}

async function approve(registration) {
  await ElMessageBox.confirm(`允许服务器“${registration.node_name}”接入管理平台？批准后即可远程管理该服务器。`, '确认服务器接入')
  await post(`/nodes/${registration.id}/approve`, {})
  ElMessage.success('服务器已接入')
  await load()
}

function agentUpdateAvailable(node) {
  const latest = appState.systemUpdate?.latest_version
  return Boolean(latest && node.agent_version && node.agent_version !== latest)
}

// 同一架构的服务器共用一个官方版本号，按架构查一次即可；已经拿到的不再重复请求。
async function loadSingBoxLatest() {
  const architectures = [...new Set(appState.nodes.map((node) => node.architecture).filter(Boolean))]
  await Promise.all(architectures.map(async (architecture) => {
    if (singBoxLatest.value[architecture]) return
    const release = await api(`/sing-box/latest?architecture=${architecture}`).catch(() => null)
    if (release?.version) singBoxLatest.value[architecture] = release.version
  }))
}

function singBoxUpdateAvailable(node) {
  const latest = singBoxLatest.value[node.architecture]
  return Boolean(latest && node.sing_box_version && node.sing_box_version !== latest)
}

function openActions(node) {
  actionTarget.value = node
  actionsOpen.value = true
}

// 卡片上只留在线状态、实时速率和活动连接；系统、版本、累计流量这些
// 看一眼就够的字段留到详情里，否则一台服务器要占掉大半屏。
function statOf(node, pick) {
  const snapshot = live.value.get(node.id)
  if (!node.online) return '—'
  if (pick === 'connections') return String(snapshot?.connection_count ?? '—')
  if (!snapshot?.has_rates) return '—'
  return formatBytes(pick === 'down' ? snapshot.received_rate : snapshot.sent_rate, '/s')
}

function metaLine(node) {
  if (!node.online) return `离线 · 最后在线 ${formatDateTime(node.last_seen_at, '从未在线')}`
  return [node.os, node.architecture, node.agent_version && `agent ${node.agent_version}`].filter(Boolean).join(' · ') || '等待上报系统信息'
}

const details = computed(() => {
  const node = actionTarget.value
  if (!node) return []
  const proxy = metrics.value[node.id]?.proxy
  return [
    { label: '运行状态', value: node.online ? '在线' : '离线' },
    { label: '连接地址', value: node.client_address || '未配置，客户端拿不到节点链接', mono: Boolean(node.client_address) },
    { label: '系统', value: [node.os, node.architecture].filter(Boolean).join(' · ') || '—' },
    { label: '版本', value: `agent ${node.agent_version || '—'} · sing-box ${node.sing_box_version || '—'}` },
    { label: '累计流量', value: proxy ? `↓ ${formatBytes(proxy.received_bytes)} · ↑ ${formatBytes(proxy.sent_bytes)}` : '等待上报' },
    { label: '最后在线', value: formatDateTime(node.last_seen_at, '从未在线') },
  ]
})

const actions = computed(() => {
  const node = actionTarget.value
  if (!node) return []
  const list = []
  if (canWrite.value) list.push({ key: 'edit', label: '编辑', hint: '名称与客户端连接地址' })
  if (isAdmin.value && agentUpdateAvailable(node)) list.push({ key: 'upgrade', label: '升级 Agent', hint: `升级到 v${appState.systemUpdate?.latest_version}` })
  if (isAdmin.value && singBoxUpdateAvailable(node)) list.push({ key: 'upgrade-sing-box', label: '升级 sing-box', hint: `升级到 ${singBoxLatest.value[node.architecture]}` })
  if (isAdmin.value) list.push({ key: 'revoke', label: '移除服务器', danger: true })
  return list
})

function runAction(key) {
  const node = actionTarget.value
  if (key === 'edit') return openEdit(node)
  if (key === 'upgrade') return upgradeAgent(node)
  if (key === 'upgrade-sing-box') return upgradeSingBox(node)
  if (key === 'revoke') return revoke(node)
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

async function upgradeSingBox(node) {
  await ElMessageBox.confirm(
    `将“${node.name}”的 sing-box 从 ${node.sing_box_version} 升级到 ${singBoxLatest.value[node.architecture]}？升级时 sing-box 会重启，正在通过该服务器的连接会短暂中断；新版本启动失败会自动回滚到当前版本。`,
    '升级 sing-box',
    { type: 'warning', confirmButtonText: '开始升级' },
  )
  await post(`/nodes/${node.id}/sing-box/install`, {})
  ElMessage.success('升级任务已下发，完成后服务器会上报新的 sing-box 版本')
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

function openEdit(node) {
  editNode.value = node
  editForm.value = { name: node.name, client_address: node.client_address || '' }
  editSheet.value = true
}

async function saveNode() {
  editSaving.value = true
  try {
    await put(`/nodes/${editNode.value.id}`, {
      name: editForm.value.name.trim(),
      client_address: editForm.value.client_address.trim(),
    })
    ElMessage.success('服务器信息已保存')
    editSheet.value = false
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
  <MPage title="服务器" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
    </template>

    <div class="m-listbar">
      <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或版本" />
      <div class="m-filters">
        <MSegmented
          v-model="statusFilter"
          :options="[{ value: '', label: '全部' }, { value: 'online', label: '在线' }, { value: 'offline', label: '离线' }]"
        />
      </div>
    </div>

    <template v-if="filteredPending.length">
      <div class="m-section">等待确认的服务器</div>
      <article v-for="row in filteredPending" :key="row.id" class="m-item">
        <div class="m-item__hit is-static">
          <div class="m-item__head"><span class="m-item__title">{{ row.node_name }}</span></div>
          <div class="m-item__meta">{{ row.capabilities || '未上报支持的功能' }}</div>
          <div class="m-item__meta">申请于 {{ formatDateTime(row.created_at) }}</div>
        </div>
        <button type="button" class="m-item__act" :aria-label="`接入 ${row.node_name}`" @click="approve(row)">
          <el-icon :size="18"><Check /></el-icon><small>接入</small>
        </button>
      </article>
    </template>

    <div class="m-section">已接入服务器（{{ filteredNodes.length }}）</div>
    <!-- 整条就是详情入口：完整字段和编辑、升级、移除都在弹出的抽屉里。 -->
    <article v-for="node in shownNodes" :key="node.id" class="m-item">
      <button type="button" class="m-item__hit" @click="openActions(node)">
        <div class="m-item__head">
          <span class="m-item__dot" :class="{ 'is-on': node.online }" />
          <span class="m-item__title">
            <span v-if="regionFlag(node.name)" class="region-flag">{{ regionFlag(node.name) }}</span>{{ node.name }}
          </span>
          <span v-if="!node.client_address" class="m-pill m-pill--warning">缺地址</span>
          <span v-if="agentUpdateAvailable(node)" class="m-pill m-pill--warning">可升级</span>
          <i class="m-item__chevron" aria-hidden="true">›</i>
        </div>
        <div class="m-item__stats">
          <span class="m-stat"><b :class="{ 'is-muted': !node.online }">↓ {{ statOf(node, 'down') }}</b><small>实时下行</small></span>
          <span class="m-stat"><b :class="{ 'is-muted': !node.online }">↑ {{ statOf(node, 'up') }}</b><small>实时上行</small></span>
          <span class="m-stat"><b :class="{ 'is-muted': !node.online }">{{ statOf(node, 'connections') }}</b><small>活动连接</small></span>
        </div>
        <div class="m-item__meta">{{ metaLine(node) }}</div>
      </button>
    </article>

    <div v-if="!filteredNodes.length" class="m-empty">尚未接入任何服务器</div>
    <button v-if="filteredNodes.length > visible" type="button" class="m-load-more" @click="visible += 20">
      加载更多（还有 {{ filteredNodes.length - visible }} 台）
    </button>

    <MActionSheet
      v-model="actionsOpen"
      :title="actionTarget?.name"
      :details="details"
      :actions="actions"
      @select="runAction"
    />

    <MSheet v-model="editSheet" title="编辑服务器">
      <div class="m-field">
        <label class="m-field__label">服务器名称 <em>*</em></label>
        <el-input v-model="editForm.name" maxlength="128" aria-label="服务器名称" placeholder="例如：香港节点 01" />
      </div>
      <div class="m-field">
        <label class="m-field__label">客户端连接域名或 IP 地址</label>
        <el-input v-model="editForm.client_address" aria-label="客户端连接域名或 IP 地址" placeholder="例如：proxy.example.com" />
        <div class="m-field__hint">接入时会自动填入来源 IP。仅填域名或 IP，不含 http://、端口与路径。</div>
      </div>
      <template #footer>
        <el-button @click="editSheet = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" :disabled="!editForm.name.trim()" @click="saveNode">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="tokenSheet" title="服务器接入信息" full>
      <div class="m-notice m-notice--warning">此命令只显示一次，请立即复制。令牌一次性使用，过期后需重新生成。</div>
      <div class="m-field">
        <label class="m-field__label">Master 地址（agent 拨号用的主机）</label>
        <el-input v-model="masterHost" aria-label="Master 地址" placeholder="control.example.com">
          <template #append>:{{ agentPort }}</template>
        </el-input>
        <div class="m-field__hint">默认按当前控制台域名填入。若 agent 需通过其他域名或公网 IP 连接，请改成正确的主机名，本浏览器会记住。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">在目标服务器上以 root 执行</label>
        <pre class="command">{{ installCommand }}</pre>
      </div>
      <p class="m-field__hint">令牌有效期至：{{ formatDateTime(expiresAt) }}。命令执行后该服务器会出现在「等待确认的服务器」，需在本页点「接入」批准。</p>
      <template #footer>
        <el-button @click="tokenSheet = false">完成</el-button>
        <el-button type="primary" @click="copyInstallCommand">复制命令</el-button>
      </template>
    </MSheet>

    <template v-if="isAdmin" #fab>
      <button type="button" class="m-fab" aria-label="添加服务器" @click="generateToken">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
/* 命令很长，横向滚动比换行成六七行好读，也方便长按整段选中。 */
.command {
  margin: 0;
  padding: 12px;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .07);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm);
  font-family: "Cascadia Code", Consolas, monospace;
  font-size: 12px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-all;
  user-select: all;
}
</style>
