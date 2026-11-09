<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Check, Delete, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, post, put } from '../../api'
import { writeClipboard } from '../../clipboard'
import { formatBandwidth, formatBytes, formatDateTime, includesText } from '../../format'
import { subscribeLive } from '../../live'
import { connectionSnapshots, subscribeConnections } from '../../connections'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MActionSheet from '../components/MActionSheet.vue'
import StatusDot from '../../components/StatusDot.vue'
import OsIcon from '../../components/OsIcon.vue'
import LatencyPanel from '../../components/LatencyPanel.vue'
import { isNewerVersion } from '../../version'

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
const editForm = ref({ name: '', client_address: '', ddns_enabled: false, ddns_record_id: '', ddns_domain: '', ddns_record_ids: [], ddns_domains: [], ip_aliases: {}, expires_on: '', price: '', price_period: '', currency: 'USD', provider: '', provider_url: '', traffic_plan: '', bandwidth: '' })
const newCustomEgressIP = ref('')

const editNodeAllIPs = computed(() => {
  const ips = []
  const seen = new Set()
  const add = (ip) => {
    if (!ip || typeof ip !== 'string') return
    const trimmed = ip.trim()
    if (trimmed && !seen.has(trimmed)) {
      seen.add(trimmed)
      ips.push(trimmed)
    }
  }
  if (Array.isArray(editNode.value?.ip_addresses)) {
    for (const ip of editNode.value.ip_addresses) add(ip)
  }
  if (editForm.value?.ip_aliases) {
    for (const ip of Object.keys(editForm.value.ip_aliases)) add(ip)
  }
  return ips
})

function addCustomEgressIP() {
  const ip = (newCustomEgressIP.value || '').trim()
  if (!ip) return
  const ipv4 = /^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}$/
  const ipv6 = /^([0-9a-fA-F]{0,4}:){2,7}[0-9a-fA-F]{0,4}$/
  if (!ipv4.test(ip) && !ipv6.test(ip)) {
    ElMessage.warning('请输入有效的 IPv4 或 IPv6 地址')
    return
  }
  if (!editForm.value.ip_aliases) {
    editForm.value.ip_aliases = {}
  }
  if (editForm.value.ip_aliases[ip] === undefined) {
    editForm.value.ip_aliases[ip] = ''
  }
  newCustomEgressIP.value = ''
  ElMessage.success(`已添加出口 IP: ${ip}`)
}

function removeCustomEgressIP(ip) {
  if (editForm.value.ip_aliases && ip in editForm.value.ip_aliases) {
    delete editForm.value.ip_aliases[ip]
  }
}
const cfRecords = ref([])
const cfLoading = ref(false)
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
  return includesText([node.name, node.client_address, node.os_name, node.os, node.architecture, node.agent_version, node.sing_box_version], keyword.value)
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
  return isNewerVersion(appState.systemUpdate?.latest_version, node.agent_version)
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

// 脚注答的是「客户端连的是哪个地址」和「这台机器到现在搬了多少数据」。
// 系统、架构、版本号看一眼就够，留在详情里，不和这两条抢同一行。
function totalText(node) {
  const proxy = metrics.value[node.id]?.proxy
  if (!proxy) return '累计流量等待上报'
  return `累计 ↓ ${formatBytes(proxy.received_bytes)} · ↑ ${formatBytes(proxy.sent_bytes)}`
}

function addressText(node) {
  if (!node.client_address) return '未填写连接地址'
  if (node.ddns_enabled && node.last_observed_ip) {
    return `${node.client_address} (DDNS: ${node.last_observed_ip})`
  }
  return node.client_address
}

const details = computed(() => {
  const node = actionTarget.value
  if (!node) return []
  const proxy = metrics.value[node.id]?.proxy
  const host = metrics.value[node.id]?.host
  const addrVal = node.client_address ? (node.ddns_enabled && node.last_observed_ip ? `${node.client_address} (DDNS: ${node.last_observed_ip})` : node.client_address) : '未配置，客户端拿不到节点链接'
  const items = [
    { label: '运行状态', value: node.online ? '在线' : '离线' },
    { label: '连接地址', value: addrVal, mono: Boolean(node.client_address) },
    { label: '系统', value: [node.os_name || host?.os_name || node.os, node.architecture].filter(Boolean).join(' · ') || '—' },
    { label: '版本', value: 'agent ' + (node.agent_version || '—') + ' · sing-box ' + (node.sing_box_version || '—') },
    ...(node.ip_addresses && node.ip_addresses.length ? [{ label: '出口 IP', value: node.ip_addresses.map((ip) => (node.ip_aliases?.[ip] ? `${node.ip_aliases[ip]} (${ip})` : ip)).join('、') }] : []),
  ]
  if (host) {
    items.push(
      { label: 'CPU 负载', value: (host.cpu_percent != null ? host.cpu_percent.toFixed(1) + '%' : '—') },
      { label: '内存占用', value: formatBytes(host.memory_used) + ' / ' + formatBytes(host.memory_total) },
      { label: '硬盘占用', value: formatBytes(host.disk_used) + ' / ' + formatBytes(host.disk_total) },
    )
  }
  items.push(
    { label: '累计流量', value: proxy ? '↓ ' + formatBytes(proxy.received_bytes) + ' · ↑ ' + formatBytes(proxy.sent_bytes) : '等待上报' },
    { label: '最后在线', value: formatDateTime(node.last_seen_at, '从未在线') },
  )
  return items
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

async function loadCloudflareRecords() {
  cfLoading.value = true
  try {
    const res = await api('/cloudflare/records').catch(() => ({ records: [] }))
    cfRecords.value = (res.records || []).filter(r => r.type === 'A' || r.type === 'AAAA')
  } finally {
    cfLoading.value = false
  }
}

function onRecordsSelected(selectedIds) {
  editForm.value.ddns_record_ids = selectedIds
  const selectedRecords = cfRecords.value.filter(r => selectedIds.includes(r.id))
  editForm.value.ddns_domains = selectedRecords.map(r => r.name)
  editForm.value.ddns_record_id = selectedIds[0] || ''
  editForm.value.ddns_domain = editForm.value.ddns_domains[0] || ''
  if (!editForm.value.client_address || /^[0-9.:]+$/.test(editForm.value.client_address)) {
    if (editForm.value.ddns_domain) {
      editForm.value.client_address = editForm.value.ddns_domain
    }
  }
}

async function openEdit(node) {
  editNode.value = node

  const recordIds = (node.ddns_record_ids && node.ddns_record_ids.length)
    ? [...node.ddns_record_ids]
    : (node.ddns_record_id ? [node.ddns_record_id] : [])
  const domains = (node.ddns_domains && node.ddns_domains.length)
    ? [...node.ddns_domains]
    : (node.ddns_domain ? [node.ddns_domain] : [])

  editForm.value = {
    name: node.name,
    client_address: node.client_address || '',
    ddns_enabled: Boolean(node.ddns_enabled),
    ddns_record_id: node.ddns_record_id || '',
    ddns_domain: node.ddns_domain || '',
    ddns_record_ids: recordIds,
    ddns_domains: domains,
    ip_aliases: { ...(node.ip_aliases || {}) },
    expires_on: node.expires_on || '', price: node.price || '', price_period: node.price_period || '', currency: node.currency || 'USD', provider: node.provider || '', provider_url: node.provider_url || '', traffic_plan: node.traffic_plan || '', bandwidth: node.bandwidth || '',
  }
  loadCloudflareRecords()
  editSheet.value = true
}

async function saveNode() {
  editSaving.value = true
  try {
    await put(`/nodes/${editNode.value.id}`, {
      name: editForm.value.name.trim(),
      client_address: editForm.value.client_address.trim(),
      ddns_enabled: editForm.value.ddns_enabled,
      ddns_record_id: editForm.value.ddns_record_id,
      ddns_domain: editForm.value.ddns_domain,
      ddns_record_ids: editForm.value.ddns_record_ids,
      ddns_domains: editForm.value.ddns_domains,
      ip_aliases: editForm.value.ip_aliases || {},
      expires_on: editForm.value.expires_on, price: editForm.value.price, price_period: editForm.value.price_period, currency: editForm.value.currency,
      provider: editForm.value.provider, provider_url: editForm.value.provider_url, traffic_plan: editForm.value.traffic_plan, bandwidth: editForm.value.bandwidth,
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

function openProvider(node) {
  if (node.provider_url) window.open(node.provider_url, '_blank', 'noopener,noreferrer')
}

onMounted(() => {
  load()
  stopConnections = subscribeConnections(() => {})
  stopLive = subscribeLive((event) => {
    // 节点状态变更通过 MobileApp.vue 增量 patch，性能指标通过 connections SSE 增量推流
    if (event.kind === 'registration') load(true).catch(() => {})
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
      <el-button
        circle
        size="small"
        :icon="Refresh"
        :loading="refreshing"
        aria-label="刷新"
        @click="load(false)"
      />
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
    <article
      v-for="node in shownNodes"
      :key="node.id"
      class="m-node-card neu rise"
      :class="{ 'is-offline': !node.online }"
      role="button"
      tabindex="0"
      @click="openActions(node)"
    >
      <span class="m-node-glow" :class="node.online ? 'is-online' : 'is-offline'" />

      <div class="m-node-head">
        <div class="m-node-hero min-w-0 flex-1">
          <OsIcon :os="node.os_name || node.os" class="m-node-os" />
          <h3 class="m-node-title truncate" :title="node.name">{{ node.name }}</h3>
        </div>
        <div class="m-node-status" :class="node.online ? 'is-online' : 'is-offline'">
          <span class="m-node-pulse" />
          <span>{{ node.online ? '在线' : '离线' }}</span>
        </div>
      </div>

      <div class="m-node-tags">
        <span class="m-node-addr font-mono truncate">{{ addressText(node) }}</span>
        <span v-if="!node.client_address" class="m-pill m-pill--warning">缺地址</span>
        <span v-if="agentUpdateAvailable(node) || singBoxUpdateAvailable(node)" class="m-pill m-pill--amber">可升级</span>
        <span v-if="node.ddns_enabled" class="m-pill m-pill--info">DDNS</span>
        <span v-if="node.bandwidth" class="m-pill m-pill--neutral">⚡ {{ formatBandwidth(node.bandwidth) }}</span>
      </div>

      <div class="m-node-gauges neu-inset">
        <div class="gauge-col">
          <div class="gauge-meta">
            <span class="gauge-dot is-down" />
            <span class="gauge-lbl">实时下行</span>
          </div>
          <div class="gauge-val font-mono" :class="{ 'is-muted': !node.online }">
            ↓ {{ statOf(node, 'down') }}
          </div>
        </div>
        <div class="gauge-sep" />
        <div class="gauge-col">
          <div class="gauge-meta">
            <span class="gauge-dot is-up" />
            <span class="gauge-lbl">实时上行</span>
          </div>
          <div class="gauge-val font-mono" :class="{ 'is-muted': !node.online }">
            ↑ {{ statOf(node, 'up') }}
          </div>
        </div>
        <div class="gauge-sep" />
        <div class="gauge-col">
          <div class="gauge-meta">
            <span class="gauge-dot is-conn" />
            <span class="gauge-lbl">活动连接</span>
          </div>
          <div class="gauge-val font-mono" :class="{ 'is-muted': !node.online }">
            {{ statOf(node, 'connections') }}
          </div>
        </div>
      </div>

      <div v-if="node.online && metrics[node.id]?.host" class="m-node-host-meters">
        <div v-if="metrics[node.id].host.cpu_percent != null" class="m-host-meter">
          <span class="m-host-meter__lbl">CPU</span>
          <div class="m-host-meter__track neu-inset">
            <div class="m-host-meter__fill is-cpu" :style="{ width: `${Math.min(100, Math.max(2, metrics[node.id].host.cpu_percent))}%` }" />
          </div>
          <span class="m-host-meter__val font-mono">{{ metrics[node.id].host.cpu_percent.toFixed(0) }}%</span>
        </div>
        <div v-if="metrics[node.id].host.memory_total" class="m-host-meter">
          <span class="m-host-meter__lbl">内存</span>
          <div class="m-host-meter__track neu-inset">
            <div class="m-host-meter__fill is-mem" :style="{ width: `${Math.min(100, Math.max(2, (metrics[node.id].host.memory_used / metrics[node.id].host.memory_total) * 100))}%` }" />
          </div>
          <span class="m-host-meter__val font-mono">{{ formatBytes(metrics[node.id].host.memory_used) }}</span>
        </div>
      </div>

      <div v-if="metrics[node.id]?.latency" class="m-node-latency">
        <LatencyPanel :latency="metrics[node.id]?.latency" :compact="true" />
      </div>

      <div class="m-node-footer">
        <div class="m-node-traffic">{{ totalText(node) }}</div>
        <div v-if="node.expires_on || node.provider || node.price_period" class="m-node-billing">
          <span v-if="node.provider" class="provider-link" :class="{ 'is-clickable': node.provider_url }" @click.stop="openProvider(node)">
            {{ node.provider }}
          </span>
          <span v-if="node.expires_on" class="expire-date">到期 {{ node.expires_on }}</span>
          <span v-if="node.price_period" class="price-chip">
            {{ node.price_period === 'free' ? '免费' : `${node.currency || 'USD'} ${node.price}/${({day:'天',month:'月',quarter:'季',year:'年'})[node.price_period] || node.price_period}` }}
          </span>
        </div>
        <div v-if="!node.online" class="m-node-offline-hint">
          离线 · 最后在线 {{ formatDateTime(node.last_seen_at, '从未在线') }}
        </div>
      </div>
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
        <label class="m-field__label">套餐与到期信息</label>
        <el-date-picker v-model="editForm.expires_on" type="date" value-format="YYYY-MM-DD" placeholder="到期日期" clearable style="width:100%;margin-bottom:8px" />
        <div style="display:flex;gap:8px"><el-input v-model="editForm.price" :disabled="editForm.price_period === 'free'" placeholder="价格"/><el-select v-model="editForm.currency" style="width:110px"><el-option label="USD" value="USD"/><el-option label="CNY" value="CNY"/><el-option label="EUR" value="EUR"/><el-option label="CAD" value="CAD"/></el-select></div>
        <el-select v-model="editForm.price_period" placeholder="计价周期" style="width:100%;margin-top:8px"><el-option label="免费" value="free"/><el-option label="按天" value="day"/><el-option label="按月" value="month"/><el-option label="按季" value="quarter"/><el-option label="按年" value="year"/></el-select>
        <el-input v-model="editForm.provider" placeholder="厂商" style="margin-top:8px"/><el-input v-model="editForm.provider_url" placeholder="厂商访问 URL" style="margin-top:8px"/><el-input v-model="editForm.traffic_plan" placeholder="流量套餐" style="margin-top:8px"/><el-input v-model="editForm.bandwidth" placeholder="带宽大小" style="margin-top:8px"/>
      </div>
      <div class="m-field">
        <label class="m-field__label">服务器名称 <em>*</em></label>
        <el-input v-model="editForm.name" maxlength="128" aria-label="服务器名称" placeholder="例如：香港节点 01" />
      </div>
      <div class="m-field">
        <label class="m-field__label">客户端连接域名或 IP 地址</label>
        <el-input v-model="editForm.client_address" aria-label="客户端连接域名或 IP 地址" placeholder="例如：proxy.example.com" />
        <div class="m-field__hint">接入时会自动填入来源 IP。仅填域名或 IP，不含 http://、端口与路径。</div>
        <div v-if="editNodeAllIPs.length" style="margin-top: 6px; display: flex; flex-wrap: wrap; gap: 6px; align-items: center">
          <span style="font-size: 12px; color: var(--sb-muted)">点击填入：</span>
          <el-tag
            v-for="ip in editNodeAllIPs"
            :key="ip"
            size="small"
            type="info"
            style="cursor: pointer"
            @click="editForm.client_address = ip"
          >
            {{ editForm.ip_aliases?.[ip] ? `${editForm.ip_aliases[ip]} (${ip})` : ip }}
          </el-tag>
        </div>
      </div>
      <div class="m-field">
        <label class="m-field__label">本机出口 IP 与别名设置</label>
        <div class="m-field__hint">自动从服务器网卡获取或手动配置的出口 IP。在此为每个出口 IP 设置别名。</div>
        <div v-if="editNodeAllIPs.length" style="margin-top: 8px; display: flex; flex-direction: column; gap: 8px">
          <div
            v-for="ip in editNodeAllIPs"
            :key="ip"
            style="background: var(--sb-surface-2); padding: 8px 10px; border-radius: 8px; border: 1px solid var(--sb-line)"
          >
            <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px">
              <span style="font-family: monospace; font-size: 12px; font-weight: 600; word-break: break-all">{{ ip }}</span>
              <div style="display: flex; align-items: center; gap: 6px">
                <el-tag size="small" :type="ip.includes(':') ? 'warning' : 'success'">
                  {{ ip.includes(':') ? 'IPv6' : 'IPv4' }}
                </el-tag>
                <el-button
                  v-if="!editNode?.ip_addresses?.includes(ip)"
                  text
                  type="danger"
                  size="small"
                  :icon="Delete"
                  title="删除手动添加的出口 IP"
                  @click="removeCustomEgressIP(ip)"
                />
              </div>
            </div>
            <el-input
              v-model="editForm.ip_aliases[ip]"
              size="small"
              placeholder="出口别名（例如：电信出口、香港主）"
              clearable
            />
          </div>
        </div>
        <div v-else class="m-field__hint" style="margin-top: 6px">
          暂未检测到出口 IP。您可手动输入本服务器的出口 IP 添加管理。
        </div>
        <div style="display: flex; gap: 8px; width: 100%; margin-top: 8px">
          <el-input
            v-model="newCustomEgressIP"
            size="small"
            placeholder="手动添加出口 IP（如：198.51.100.2）"
            clearable
            @keydown.enter.prevent="addCustomEgressIP"
          />
          <el-button size="small" :icon="Plus" @click="addCustomEgressIP">添加</el-button>
        </div>
      </div>
      <div class="m-field">
        <div style="display: flex; align-items: center; justify-content: space-between; margin-bottom: 6px;">
          <label class="m-field__label" style="margin: 0">动态域名解析 (DDNS)</label>
          <el-switch
            v-model="editForm.ddns_enabled"
            active-text="已开启"
            inactive-text="未开启"
            inline-prompt
          />
        </div>
        <div class="m-field__hint">NAT 机器公网出口 IP 变动时自动更新绑定的 Cloudflare A 记录。</div>
        <div v-if="editForm.ddns_enabled" style="margin-top: 8px;">
          <el-select
            v-model="editForm.ddns_record_ids"
            multiple
            filterable
            collapse-tags
            collapse-tags-tooltip
            placeholder="选择关联的 Cloudflare 记录（支持多选）"
            style="width: 100%;"
            :loading="cfLoading"
            no-data-text="未找到可用记录（请先在域名解析中添加）"
            @change="onRecordsSelected"
          >
            <el-option
              v-for="item in cfRecords"
              :key="item.id"
              :label="`${item.name} (${item.type} ➔ ${item.content})`"
              :value="item.id"
            />
          </el-select>
          <div v-if="(editForm.ddns_domains || []).length" style="margin-top: 6px; display: flex; flex-wrap: wrap; gap: 4px">
            <el-tag v-for="d in editForm.ddns_domains" :key="d" size="small" type="info">
              {{ d }}
            </el-tag>
          </div>
        </div>
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
        <el-button type="primary" @click="copyInstallCommand">复制</el-button>
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

.m-node-card {
  position: relative;
  display: block;
  width: 100%;
  padding: 14px 15px 12px;
  margin-bottom: 12px;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  box-shadow: var(--sb-shadow-sm);
  cursor: pointer;
  text-align: left;
  user-select: none;
  overflow: hidden;
  transition: transform 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.m-node-card:active {
  transform: scale(0.992);
}
.m-node-card.is-offline {
  opacity: 0.72;
}

.m-node-glow {
  position: absolute;
  top: 0;
  right: 0;
  width: 70px;
  height: 70px;
  border-radius: 0 var(--m-radius) 0 50px;
  pointer-events: none;
  opacity: 0.35;
  transition: opacity 0.3s ease;
}
.m-node-glow.is-online {
  background: radial-gradient(circle at top right, rgba(16, 185, 129, 0.45) 0%, transparent 70%);
}
.m-node-glow.is-offline {
  background: radial-gradient(circle at top right, rgba(148, 163, 184, 0.25) 0%, transparent 70%);
}

.m-node-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.m-node-hero {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.m-node-os {
  width: 17px;
  height: 17px;
  flex-shrink: 0;
}
.m-node-title {
  margin: 0;
  color: var(--sb-text);
  font-size: 15.5px;
  font-weight: 650;
  letter-spacing: -0.01em;
  line-height: 1.35;
}

.m-node-status {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 9px;
  border-radius: 999px;
  font-size: 11.5px;
  font-weight: 600;
  flex-shrink: 0;
}
.m-node-status.is-online {
  background: rgba(16, 185, 129, 0.12);
  color: #059669;
  border: 1px solid rgba(16, 185, 129, 0.28);
}
html.dark .m-node-status.is-online {
  background: rgba(16, 185, 129, 0.18);
  color: #34D399;
  border-color: rgba(16, 185, 129, 0.35);
}
.m-node-status.is-offline {
  background: var(--sb-surface-3);
  color: var(--sb-muted);
  border: 1px solid var(--sb-line);
}
.m-node-pulse {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.m-node-status.is-online .m-node-pulse {
  background: #10B981;
  box-shadow: 0 0 0 2px rgba(16, 185, 129, 0.35);
  animation: pulse-dot 2s infinite ease-in-out;
}
.m-node-status.is-offline .m-node-pulse {
  background: #94A3B8;
}
@keyframes pulse-dot {
  0%, 100% { transform: scale(1); opacity: 1; }
  50% { transform: scale(1.3); opacity: 0.6; }
}

.m-node-tags {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 8px;
}
.m-node-addr {
  font-size: 12px;
  color: var(--sb-muted);
  max-width: 60%;
}
.m-pill--amber {
  background: rgba(245, 158, 11, 0.12);
  color: #D97706;
  border: 1px solid rgba(245, 158, 11, 0.28);
}
html.dark .m-pill--amber {
  background: rgba(245, 158, 11, 0.2);
  color: #FBBF24;
  border-color: rgba(245, 158, 11, 0.35);
}
.m-pill--neutral {
  background: var(--sb-surface-3);
  color: var(--sb-text-2);
  border: 1px solid var(--sb-line);
}

.m-node-gauges {
  display: flex;
  align-items: center;
  margin-top: 10px;
  padding: 9px 12px;
  border-radius: 10px;
  background: var(--neu-inset-bg);
  border: 1px solid var(--sb-line);
  box-shadow: var(--sh-inset);
}
.gauge-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.gauge-meta {
  display: flex;
  align-items: center;
  gap: 4px;
}
.gauge-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  flex-shrink: 0;
}
.gauge-dot.is-down { background: #0284C7; }
html.dark .gauge-dot.is-down { background: #38BDF8; }
.gauge-dot.is-up { background: #D97706; }
html.dark .gauge-dot.is-up { background: #FBBF24; }
.gauge-dot.is-conn { background: #10B981; }
html.dark .gauge-dot.is-conn { background: #34D399; }

.gauge-lbl {
  font-size: 10.5px;
  color: var(--sb-muted);
}
.gauge-val {
  font-size: 13px;
  font-weight: 650;
  color: var(--sb-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.gauge-val.is-muted {
  color: var(--sb-muted);
}
.gauge-sep {
  width: 1px;
  height: 24px;
  background: var(--sb-line);
  margin: 0 8px;
  flex-shrink: 0;
}

.m-node-host-meters {
  display: flex;
  gap: 12px;
  margin-top: 8px;
  padding: 0 2px;
}
.m-host-meter {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
}
.m-host-meter__lbl {
  color: var(--sb-muted);
  font-weight: 600;
  width: 24px;
  flex-shrink: 0;
}
.m-host-meter__track {
  flex: 1;
  height: 6px;
  background: var(--neu-inset-bg);
  border-radius: 999px;
  border: 1px solid var(--sb-line);
  overflow: hidden;
}
.m-host-meter__fill {
  height: 100%;
  border-radius: 999px;
  transition: width 0.3s ease;
}
.m-host-meter__fill.is-cpu {
  background: linear-gradient(90deg, #38BDF8, #0284C7);
}
.m-host-meter__fill.is-mem {
  background: linear-gradient(90deg, #A78BFA, #7C3AED);
}
.m-host-meter__val {
  color: var(--sb-text-2);
  font-size: 11px;
  min-width: 32px;
  text-align: right;
  flex-shrink: 0;
}

.m-node-latency {
  margin-top: 8px;
}

.m-node-footer {
  margin-top: 9px;
  padding-top: 8px;
  border-top: 1px solid var(--sb-line);
  font-size: 11.5px;
  color: var(--sb-muted);
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.m-node-traffic {
  color: var(--sb-text-2);
}
.m-node-billing {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
.provider-link.is-clickable {
  color: var(--sb-accent);
  text-decoration: underline;
  cursor: pointer;
}
.price-chip {
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--sb-surface-3);
  border: 1px solid var(--sb-line);
}
.m-node-offline-hint {
  color: var(--sb-danger);
  font-weight: 500;
}

</style>
