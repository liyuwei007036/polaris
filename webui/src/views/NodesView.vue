<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Close,
  Connection,
  CopyDocument,
  Delete,
  Edit,
  Grid,
  InfoFilled,
  Loading,
  MoreFilled,
  Plus,
  Refresh,
  RemoveFilled,
  Search,
  Share,
  Tickets,
  Top,
} from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { formatBytes, formatDateTime, includesText } from '../format'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'
import StatusDot from '../components/StatusDot.vue'
import OsIcon from '../components/OsIcon.vue'
import LatencyPanel from '../components/LatencyPanel.vue'
import SummaryCards from '../components/SummaryCards.vue'
import NodeCard from '../components/NodeCard.vue'
import NodeRow from '../components/NodeRow.vue'
import NodeDetail from '../components/NodeDetail.vue'
import ProbeManager from '../components/ProbeManager.vue'
import ShareDialog from '../components/ShareDialog.vue'

const MASTER_HOST_KEY = 'polaris_master_host'
const PERIOD_LABELS = { day: '天', month: '月', quarter: '季', year: '年' }
const CURRENCY_SYMBOLS = { USD: '$', CNY: '¥', EUR: '€', CAD: 'C$' }

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const canWrite = inject('canWrite')
const loadNodes = inject('loadNodes')
const loadSystemUpdate = inject('loadSystemUpdate', null)

const selectedNodeId = ref(null)
const shareOpen = ref(false)
const selectedNode = computed(() => appState.nodes.find((n) => n.id === selectedNodeId.value) || null)

function handleCardAction({ command, node }) {
  if (command === 'detail') {
    selectedNodeId.value = node.id
  } else {
    handleCardCommand(node, command)
  }
}

const loading = ref(false)
const refreshing = ref(false)
const pending = ref([])
const metrics = ref({})
const probesDialog = ref(false)

const keyword = ref('')
const statusFilter = ref('') // '' | 'online' | 'offline'
const sortBy = ref('name')
const viewMode = ref(window.localStorage.getItem('polaris_nodes_view_mode') || 'card')
watch(viewMode, (v) => window.localStorage.setItem('polaris_nodes_view_mode', v))

const tokenDialog = ref(false)
const token = ref('')
const expiresAt = ref('')
const lifetime = ref(900)
const masterPublicKey = ref('')
const masterHost = ref('')
const agentPort = ref(19994)

const editDialog = ref(false)
const editNode = ref(null)
const editForm = ref({
  name: '',
  client_address: '',
  ddns_enabled: false,
  ddns_record_id: '',
  ddns_domain: '',
  ddns_record_ids: [],
  ddns_domains: [],
  ip_aliases: {},
  expires_on: '', price: '', price_period: '', currency: 'USD', provider: '', provider_url: '', traffic_value: '', traffic_unit: 'MB', bandwidth: '', bandwidth_value: '', bandwidth_unit: 'Gbps',
})
const editTab = ref('basic')
const editTabs = computed(() => [
  { key: 'basic', label: '基本信息' },
  { key: 'plan', label: '套餐与到期' },
  { key: 'network', label: '网络与 DDNS', badge: editNodeAllIPs.value.length || '' },
])
const editHeader = computed(() => buildDialogHeader(editNode.value, editForm.value.name))

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

// 从既有 traffic_plan（如 "每月 2 TB" / "2 GB"）解析数值与单位
function parseTrafficPlan(plan) {
  const m = String(plan || '').match(/([\d.]+)\s*(TB|GB|MB)/i)
  if (!m) return { traffic_value: '', traffic_unit: 'MB' }
  return { traffic_value: m[1], traffic_unit: m[2].toUpperCase() }
}

// 从既有 bandwidth（如 "1 Gbps" / "500 Mbps"）解析数值与单位
function parseBandwidth(bw) {
  const m = String(bw || '').match(/([\d.]+)\s*(Gbps|Mbps|G|M)/i)
  if (!m) {
    const rawNum = String(bw || '').match(/([\d.]+)/)
    return { bandwidth_value: rawNum ? rawNum[1] : '', bandwidth_unit: 'Gbps' }
  }
  const unit = m[2].toUpperCase().startsWith('G') ? 'Gbps' : 'Mbps'
  return { bandwidth_value: m[1], bandwidth_unit: unit }
}

function removeCustomEgressIP(ip) {
  if (editForm.value.ip_aliases && ip in editForm.value.ip_aliases) {
    delete editForm.value.ip_aliases[ip]
  }
}
const cfRecords = ref([])
const cfLoading = ref(false)
const syncingDDNS = ref(false)
const editSaving = ref(false)

// 架构 -> 官方最新 sing-box 版本号
const singBoxLatest = ref({})
let stopLive
let stopConnections
let lastSingBoxCheckTime = 0

const filteredNodes = computed(() => {
  let list = appState.nodes.filter((node) => {
    if (statusFilter.value === 'online' && !node.online) return false
    if (statusFilter.value === 'offline' && node.online) return false
    return includesText(
      [node.name, node.client_address, node.os_name, node.os, node.architecture, node.agent_version, node.sing_box_version],
      keyword.value,
    )
  })

  // 排序 (默认按服务器名称自然序)
  list = [...list].sort((a, b) => {
    if (sortBy.value === 'name') {
      return (a.name || '').localeCompare(b.name || '', 'zh-Hans-CN', { numeric: true, sensitivity: 'base' })
    }
    if (sortBy.value === 'conn') {
      const connA = live.value.get(a.id)?.connection_count || 0
      const connB = live.value.get(b.id)?.connection_count || 0
      return connB - connA
    }
    if (sortBy.value === 'cpu') {
      const cpuA = metrics.value[a.id]?.host?.cpu_percent || 0
      const cpuB = metrics.value[b.id]?.host?.cpu_percent || 0
      return cpuB - cpuA
    }
    if (sortBy.value === 'mem') {
      const memA = getResourcePercent(metrics.value[a.id]?.host?.memory_used, metrics.value[a.id]?.host?.memory_total)
      const memB = getResourcePercent(metrics.value[b.id]?.host?.memory_used, metrics.value[b.id]?.host?.memory_total)
      return memB - memA
    }
    // 活跃优先：在线优先及最后活跃时间倒序
    if (a.online !== b.online) return a.online ? -1 : 1
    return (b.last_seen_at || '').localeCompare(a.last_seen_at || '')
  })

  return list
})

const filteredPending = computed(() =>
  pending.value.filter((row) => includesText([row.node_name, row.capabilities], keyword.value)),
)

// 实时快照
const live = computed(() => connectionSnapshots.value)

function buildGauge(key, label, percent, available, detail, tip) {
  const value = available ? Math.min(100, Math.max(0, Math.round(percent || 0))) : 0
  return {
    key,
    label,
    available,
    text: available ? String(value) : '—',
    p: value,
    detail,
    tip,
    color: available ? getUsageStatusColor(value) : 'var(--sb-muted)',
  }
}

function getExpiry(value) {
  if (!value) return null
  const end = new Date(`${value}T00:00:00`)
  if (Number.isNaN(end.getTime())) return { text: `到期 ${value}`, level: '' }
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const days = Math.round((end - today) / 86400000)
  if (days < 0) return { text: `已过期 ${-days} 天`, level: 'danger', title: `到期日 ${value}` }
  if (days === 0) return { text: '今日到期', level: 'danger', title: `到期日 ${value}` }
  return { text: `剩余 ${days} 天`, level: days <= 7 ? 'warning' : '', title: `到期日 ${value}` }
}

function getAvatar(name) {
  const text = (name || '').trim()
  const lead = text.match(/^(\p{Regional_Indicator}{2}|\p{Extended_Pictographic}\uFE0F?)/u)
  if (lead) return { text: lead[0], isFlag: true, rest: text.slice(lead[0].length).trim() }
  const flag = getNodeFlag(text)
  if (flag && flag !== '🌐') return { text: flag, isFlag: true, rest: text }
  return { text: (Array.from(text)[0] || '?').toUpperCase(), isFlag: false, rest: text }
}

function getNodeStatus(node) {
  const status = !node.online ? 'offline' : 'online'
  return { status, statusText: { online: '在线', offline: '离线' }[status] }
}

function buildDialogHeader(node, name) {
  if (!node) return null
  const avatar = getAvatar(name || node.name)
  return {
    ...getNodeStatus(node),
    avatar: avatar.text,
    isFlag: avatar.isFlag,
    name: avatar.rest || name || node.name,
    sub: [[node.os_name || node.os || 'linux', node.architecture].filter(Boolean).join(' · '), node.city || node.location, node.client_address]
      .filter(Boolean)
      .join(' · '),
  }
}

function getPriceText(node) {
  if (!node.price_period) return ''
  if (node.price_period === 'free') return '免费'
  const symbol = CURRENCY_SYMBOLS[node.currency] || `${node.currency} `
  return `${symbol}${node.price || 0}/${PERIOD_LABELS[node.price_period] || node.price_period}`
}

const cards = computed(() => filteredNodes.value.map((node) => {
  const host = node.online ? metrics.value[node.id]?.host : null
  const proxy = metrics.value[node.id]?.proxy
  const latency = metrics.value[node.id]?.latency
  const snap = live.value.get(node.id)
  const { status, statusText } = getNodeStatus(node)
  const avatar = getAvatar(node.name)
  const used = (proxy?.received_bytes || 0) + (proxy?.sent_bytes || 0)

  let trafficLimit = 0
  if (node.traffic_plan) {
    const m = String(node.traffic_plan).match(/([\d.]+)\s*(TB|GB|MB)/i)
    if (m) {
      const val = parseFloat(m[1])
      const unit = m[2].toUpperCase()
      if (unit === 'TB') trafficLimit = val * 1024 * 1024 * 1024 * 1024
      else if (unit === 'GB') trafficLimit = val * 1024 * 1024 * 1024
      else if (unit === 'MB') trafficLimit = val * 1024 * 1024
    }
  }
  const trafficPct = trafficLimit > 0 ? getResourcePercent(used, trafficLimit) : null

  return {
    node,
    status,
    statusText,
    avatar: avatar.text,
    isFlag: avatar.isFlag,
    displayName: avatar.rest || node.name,
    platform: [node.os_name || node.os || 'linux', host?.virt && host?.virt !== 'none' ? host?.virt : '', node.architecture].filter(Boolean).join(' · '),
    region: node.city || node.location || '',
    latency,
    host,
    cpuPercent: host?.cpu_percent ?? null,
    cpuCores: host?.cpu_cores || 0,
    cpuLoad: host?.load_avg || [],
    memUsed: host?.memory_used || 0,
    memTotal: host?.memory_total || 0,
    memPercent: getResourcePercent(host?.memory_used, host?.memory_total),
    diskUsed: host?.disk_used || 0,
    diskTotal: host?.disk_total || 0,
    diskPercent: getResourcePercent(host?.disk_used, host?.disk_total),
    trafficUsed: used,
    trafficLimit,
    trafficPct,
    tcp: host?.tcp || 0,
    udp: host?.udp || 0,
    gauges: [
      buildGauge('cpu', 'CPU', host?.cpu_percent, Boolean(host), host ? `${host.cpu_percent.toFixed(1)}%` : '待上报', getCPUTooltip(node)),
      buildGauge('mem', '内存', getResourcePercent(host?.memory_used, host?.memory_total), Boolean(host?.memory_total),
        host?.memory_total ? `${formatResourceBytes(host.memory_used)} / ${formatResourceBytes(host.memory_total)}` : '待上报', getMemTooltip(node)),
      buildGauge('disk', '硬盘', getResourcePercent(host?.disk_used, host?.disk_total), Boolean(host?.disk_total),
        host?.disk_total ? `${formatResourceBytes(host.disk_used)} / ${formatResourceBytes(host.disk_total)}` : '待上报', getDiskTooltip(node)),
    ],
    rx: snap?.has_rates ? formatBytes(snap.received_rate, '/s') : (host?.net_rx_rate ? formatBytes(host.net_rx_rate, '/s') : '0 B/s'),
    tx: snap?.has_rates ? formatBytes(snap.sent_rate, '/s') : (host?.net_tx_rate ? formatBytes(host.net_tx_rate, '/s') : '0 B/s'),
    total: proxy ? formatBytes(used) : '',
    totalTip: proxy ? `累计下行 ${formatBytes(proxy.received_bytes)} · 累计上行 ${formatBytes(proxy.sent_bytes)}` : '',
    conn: snap?.connection_count ?? 0,
    price: getPriceText(node),
    expiry: getExpiry(node.expires_on),
    ddnsTip: node.ddns_enabled
      ? `${(node.ddns_domains?.length ? node.ddns_domains : [node.ddns_domain || node.client_address]).join('、')}<br/>解析: ${node.last_observed_ip || '同步中'}`
      : '',
    ipTip: node.ip_addresses?.length
      ? node.ip_addresses.map((ip) => ip + (node.ip_aliases?.[ip] ? ` (${node.ip_aliases[ip]})` : '')).join('<br/>')
      : '',
  }
}))

// 状态筛选计数
const stats = computed(() => {
  const total = appState.nodes.length
  const online = appState.nodes.filter((n) => n.online).length
  return {
    total,
    online,
    offline: total - online,
  }
})

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  const isColdLoad = (!appState.nodes || appState.nodes.length === 0) || Object.keys(metrics.value).length === 0
  if (!silent && isColdLoad) loading.value = true
  try {
    const [, registrations, metricResult] = await Promise.all([
      loadNodes(),
      isAdmin.value ? api('/registrations').catch(() => ({ registrations: [] })) : Promise.resolve({ registrations: [] }),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
    ])
    pending.value = registrations.registrations || []
    metrics.value = Object.fromEntries((metricResult.nodes || []).map((entry) => [entry.node_id, entry.report]))
    if (metricResult.nodes && metricResult.nodes.length) {
      for (const entry of metricResult.nodes) {
        if (entry.live && entry.node_id) {
          connectionSnapshots.value.set(entry.node_id, entry.live)
        }
      }
      connectionSnapshots.value = new Map(connectionSnapshots.value)
    }
  } finally {
    loading.value = false
    refreshing.value = false
  }
  if (loadSystemUpdate) loadSystemUpdate().catch(() => {})
  loadSingBoxLatest().catch(() => {})
}

function getResourcePercent(used, total) {
  if (!total || total <= 0) return 0
  const p = Math.round((Number(used) / Number(total)) * 100)
  return Math.min(100, Math.max(0, p))
}

function formatResourceBytes(bytes) {
  if (!bytes) return '0 B'
  const gb = Number(bytes) / (1024 * 1024 * 1024)
  if (gb >= 1) return `${gb.toFixed(1)} GB`
  const mb = Number(bytes) / (1024 * 1024)
  return `${mb.toFixed(0)} MB`
}

function formatCompactBytes(bytes) {
  if (!bytes) return '0 B'
  const gb = Number(bytes) / (1024 * 1024 * 1024)
  if (gb >= 1) return `${gb.toFixed(1)}G`
  const mb = Number(bytes) / (1024 * 1024)
  if (mb >= 1) return `${mb.toFixed(0)}M`
  return `${Number(bytes)}B`
}

function formatUptimeText(seconds) {
  if (!seconds) return '待上报'
  const days = Math.floor(seconds / 86400)
  if (days >= 1) return `运行 ${days}d`
  const hours = Math.floor(seconds / 3600)
  return `运行 ${hours}h`
}

function formatFriendlyTime(value) {
  if (!value) return '从未'
  const text = String(value).trim()
  const naive = /^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?$/.test(text)
  const d = new Date(naive ? `${text.replace(' ', 'T')}Z` : text)
  if (Number.isNaN(d.getTime())) return value
  const now = Date.now()
  const diffSec = Math.floor((now - d.getTime()) / 1000)
  if (diffSec < 45) return '刚刚活跃'
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} 分钟前`
  const pad = (n) => String(n).padStart(2, '0')
  if (diffSec < 86400) {
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function getCPUTooltip(node) {
  const host = metrics.value[node.id]?.host
  if (!host) return node.online ? '等待 CPU 指标上报' : '节点离线'
  let tip = `CPU 使用率: ${host.cpu_percent.toFixed(1)}%`
  if (host.uptime_seconds) {
    const d = Math.floor(host.uptime_seconds / 86400)
    const h = Math.floor((host.uptime_seconds % 86400) / 3600)
    tip += ` · 已连续运行 ${d > 0 ? `${d}天${h}小时` : `${h}小时`}`
  }
  return tip
}

function getMemTooltip(node) {
  const host = metrics.value[node.id]?.host
  if (!host?.memory_total) return node.online ? '等待内存指标上报' : '节点离线'
  const pct = getResourcePercent(host.memory_used, host.memory_total)
  return `内存: ${formatBytes(host.memory_used)} / ${formatBytes(host.memory_total)} (${pct}%)`
}

function getDiskTooltip(node) {
  const host = metrics.value[node.id]?.host
  if (!host?.disk_total) return node.online ? '等待硬盘指标上报' : '节点离线'
  const pct = getResourcePercent(host.disk_used, host.disk_total)
  return `硬盘: ${formatBytes(host.disk_used)} / ${formatBytes(host.disk_total)} (${pct}%)`
}

function getGFWDetailTooltip(st) {
  if (!st) return ''
  const probe = st.domestic_source || '分布式多线探针'
  return `探针来源: ${probe}\n诊断结果: 探针发起的 TCP SYN 握手均超时或被丢弃\n状态: 端口无响应或阻断，无法建立连接`
}

function getUsageStatusColor(percent) {
  if (percent >= 85) return 'var(--sb-danger)' // 红色警告
  if (percent >= 65) return 'var(--sb-warning)' // 琥珀黄中载
  return 'var(--sb-success)' // 翠绿正常
}

// 智能国旗推导
function getNodeFlag(name) {
  if (!name) return '🌐'
  if (/^(\p{Extended_Pictographic})/u.test(name)) return ''
  const upper = name.toUpperCase()
  if (upper.includes('TW') || upper.includes('台湾') || upper.includes('HINET')) return '🇹🇼'
  if (upper.includes('HK') || upper.includes('香港')) return '🇭🇰'
  if (upper.includes('SG') || upper.includes('新加坡') || upper.includes('狮城')) return '🇸🇬'
  if (upper.includes('JP') || upper.includes('日本') || upper.includes('东京') || upper.includes('大阪')) return '🇯🇵'
  if (upper.includes('KR') || upper.includes('韩国') || upper.includes('首尔')) return '🇰🇷'
  if (
    upper.includes('US') ||
    upper.includes('美国') ||
    upper.includes('圣何塞') ||
    upper.includes('洛杉矶') ||
    upper.includes('纽约') ||
    upper.includes('西雅图') ||
    upper.includes('硅谷') ||
    upper.includes('芝加哥')
  )
    return '🇺🇸'
  if (upper.includes('UK') || upper.includes('英国') || upper.includes('伦敦')) return '🇬🇧'
  if (upper.includes('DE') || upper.includes('德国') || upper.includes('法兰克福')) return '🇩🇪'
  if (upper.includes('FR') || upper.includes('法国') || upper.includes('巴黎')) return '🇫🇷'
  if (upper.includes('CA') || upper.includes('加拿大')) return '🇨🇦'
  if (upper.includes('AU') || upper.includes('澳大利亚') || upper.includes('悉尼')) return '🇦🇺'
  if (upper.includes('RU') || upper.includes('俄罗斯') || upper.includes('莫斯科')) return '🇷🇺'
  return '🌐'
}

async function copyAddress(addr) {
  if (!addr) return
  try {
    await navigator.clipboard.writeText(addr)
    ElMessage.success(`已复制: ${addr}`)
  } catch (_) {
    ElMessage.error('复制失败，请手动选择复制')
  }
}

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
  const result = await post('/nodes/registration-tokens', { lifetime_seconds: Number(lifetime.value) })
  token.value = result.token
  expiresAt.value = result.expires_at
  masterPublicKey.value = result.master_public_key || ''
  agentPort.value = result.agent_port || 19994
  masterHost.value = window.localStorage.getItem(MASTER_HOST_KEY) || window.location.hostname
  tokenDialog.value = true
}

async function copyInstallCommand() {
  await navigator.clipboard.writeText(installCommand.value)
  ElMessage.success('安装命令已复制')
}

async function approve(registration) {
  await ElMessageBox.confirm(
    `允许服务器“${registration.node_name}”接入管理平台？批准后即可远程管理该服务器。`,
    '确认服务器接入',
  )
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

async function loadSingBoxLatest() {
  const now = Date.now()
  if (now - lastSingBoxCheckTime < 10 * 60 * 1000 && Object.keys(singBoxLatest.value).length > 0) {
    return
  }
  const architectures = [...new Set(appState.nodes.map((node) => node.architecture).filter(Boolean))]
  if (!architectures.length) return
  lastSingBoxCheckTime = now
  await Promise.all(
    architectures.map(async (architecture) => {
      if (singBoxLatest.value[architecture]) return
      const release = await api(`/sing-box/latest?architecture=${architecture}`).catch(() => null)
      if (release?.version) singBoxLatest.value[architecture] = release.version
    }),
  )
}

function singBoxUpdateAvailable(node) {
  const latest = singBoxLatest.value[node.architecture]
  return Boolean(latest && node.sing_box_version && node.sing_box_version !== latest)
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

async function loadCloudflareRecords() {
  cfLoading.value = true
  try {
    const res = await api('/cloudflare/records').catch(() => ({ records: [] }))
    cfRecords.value = (res.records || []).filter((r) => r.type === 'A' || r.type === 'AAAA')
  } finally {
    cfLoading.value = false
  }
}

function onRecordsSelected(selectedIds) {
  editForm.value.ddns_record_ids = selectedIds
  const selectedRecords = cfRecords.value.filter((r) => selectedIds.includes(r.id))
  editForm.value.ddns_domains = selectedRecords.map((r) => r.name)
  editForm.value.ddns_record_id = selectedIds[0] || ''
  editForm.value.ddns_domain = editForm.value.ddns_domains[0] || ''
  if (!editForm.value.client_address || /^[0-9.:]+$/.test(editForm.value.client_address)) {
    if (editForm.value.ddns_domain) {
      editForm.value.client_address = editForm.value.ddns_domain
    }
  }
}

async function triggerManualDDNS(node) {
  syncingDDNS.value = true
  try {
    const res = await post(`/nodes/${node.id}/ddns/sync`, {})
    ElMessage.success(`DDNS 同步触发成功${res.ip ? '，当前解析 IP: ' + res.ip : ''}`)
    await load(true)
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : 'DDNS 同步失败')
  } finally {
    syncingDDNS.value = false
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
    expires_on: node.expires_on || '', price: node.price || '', price_period: node.price_period || '', currency: node.currency || 'USD',
    provider: node.provider || '', provider_url: node.provider_url || '',
    ...parseTrafficPlan(node.traffic_plan),
    ...parseBandwidth(node.bandwidth),
    bandwidth: node.bandwidth || '',
  }
  editTab.value = 'basic'
  loadCloudflareRecords()
  editDialog.value = true
}

async function saveEdit() {
  if (!editNode.value) return
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
      provider: editForm.value.provider, provider_url: editForm.value.provider_url,
      traffic_plan: [editForm.value.traffic_value, editForm.value.traffic_unit].filter(Boolean).join(' '),
      bandwidth: editForm.value.bandwidth_value ? `${editForm.value.bandwidth_value} ${editForm.value.bandwidth_unit}` : '',
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

// ── 线路与丢包排查状态 ────────────────────────────────────
const probeDialog = ref(false)
const probeNode = ref(null)
const probeActiveTab = ref('client')
const clientInfo = ref({ ip: '', is_ipv6: false, location: '', isp: '', asn: '' })
const loadingClientInfo = ref(false)
const clientTargetIP = ref('')
const clientProbing = ref(false)
const clientProbeResult = ref(null)

const peerProbing = ref(false)
const peerProbeResults = ref([])
const peerProbingSingle = reactive({})

const clientProbeStats = computed(() => {
  const r = clientProbeResult.value
  if (!r) return null
  const isV6 = Boolean(r.ipv6_target)
  const target = isV6 ? r.ipv6_target : r.ipv4_target
  const latency = isV6 ? r.ipv6_latency_ms : r.ipv4_latency_ms
  const minLatency = isV6 ? r.ipv6_min_latency_ms : r.ipv4_min_latency_ms
  const maxLatency = isV6 ? r.ipv6_max_latency_ms : r.ipv4_max_latency_ms
  const sent = isV6 ? r.ipv6_sent : r.ipv4_sent
  const received = isV6 ? r.ipv6_received : r.ipv4_received
  const lossPercent = isV6 ? r.ipv6_loss_percent : r.ipv4_loss_percent
  const status = isV6 ? r.ipv6_status : r.ipv4_status
  const method = (isV6 ? r.ipv6_method : r.ipv4_method) || 'ICMP'
  const latencies = (isV6 ? r.ipv6_latencies : r.ipv4_latencies) || []
  return {
    isV6,
    target,
    latency,
    minLatency,
    maxLatency,
    sent: sent || 4,
    received: received || 0,
    lossPercent: typeof lossPercent === 'number' ? lossPercent : (received > 0 ? 0 : 100),
    status,
    method: method.toUpperCase(),
    latencies,
    hasResponse: (received || 0) > 0,
  }
})

const getNodePublicIPv4 = (node) => {
  if (!node) return ''
  for (const ip of (node.ip_addresses || [])) {
    if (ip && !ip.includes(':') && !ip.startsWith('10.') && !ip.startsWith('192.168.') && !ip.startsWith('127.') && !/^172\.(1[6-9]|2\d|3[01])\./.test(ip)) {
      return ip
    }
  }
  if (node.last_observed_ip && !node.last_observed_ip.includes(':') && !node.last_observed_ip.startsWith('10.') && !node.last_observed_ip.startsWith('192.168.') && !node.last_observed_ip.startsWith('127.')) {
    return node.last_observed_ip
  }
  let host = node.client_address || ''
  if (host.startsWith('[')) {
    return '' // IPv6 address with port
  }
  if (host.includes(':')) {
    host = host.split(':')[0]
  }
  if (host && !host.includes(':') && !host.startsWith('10.') && !host.startsWith('192.168.') && !host.startsWith('127.')) {
    return host
  }
  return ''
}

const getNodePublicIPv6 = (node) => {
  if (!node) return ''
  for (const ip of (node.ip_addresses || [])) {
    if (ip && ip.includes(':') && !ip.startsWith('fe80:') && !ip.startsWith('::1') && !ip.startsWith('fc') && !ip.startsWith('fd')) {
      return ip
    }
  }
  if (node.last_observed_ip && node.last_observed_ip.includes(':') && !node.last_observed_ip.startsWith('fe80:') && !node.last_observed_ip.startsWith('::1')) {
    return node.last_observed_ip
  }
  let host = node.client_address || ''
  if (host.startsWith('[')) {
    const end = host.indexOf(']')
    if (end > 1) {
      host = host.substring(1, end)
    }
  } else if (host.includes(':') && (host.match(/:/g) || []).length === 1) {
    return ''
  }
  if (host && host.includes(':') && !host.startsWith('fe80:') && !host.startsWith('::1')) {
    return host
  }
  return ''
}

const openNetworkProbe = async (node) => {
  probeNode.value = node
  probeDialog.value = true
  probeActiveTab.value = 'client'
  clientProbeResult.value = null
  peerProbeResults.value = []

  loadingClientInfo.value = true
  try {
    const info = await api('/client-info')
    clientInfo.value = info || {}
    clientTargetIP.value = info?.ip || ''
  } catch (err) {
    clientInfo.value = { ip: '', location: '本地客户端' }
    clientTargetIP.value = ''
  } finally {
    loadingClientInfo.value = false
  }

  runClientProbe()
}

const runClientProbe = async () => {
  if (!probeNode.value) return
  clientProbing.value = true
  clientProbeResult.value = null
  const target = (clientTargetIP.value || clientInfo.value.ip || '').trim()
  try {
    const res = await post(`/nodes/${probeNode.value.id}/probe-client`, {
      client_ip: target
    })
    clientProbeResult.value = res
  } catch (err) {
    ElMessage.error(err.message || '链路探测失败')
  } finally {
    clientProbing.value = false
  }
}

const runPeerProbes = async (targetNodeId = '') => {
  if (!probeNode.value) return
  if (targetNodeId) {
    peerProbingSingle[targetNodeId] = true
  } else {
    peerProbing.value = true
  }
  try {
    const url = targetNodeId
      ? `/nodes/${probeNode.value.id}/probe-peers?target_node_id=${targetNodeId}`
      : `/nodes/${probeNode.value.id}/probe-peers`
    const res = await post(url, {})
    if (targetNodeId && res.peers?.length > 0) {
      const idx = peerProbeResults.value.findIndex(p => p.target_node_id === targetNodeId)
      if (idx >= 0) {
        peerProbeResults.value[idx] = res.peers[0]
      } else {
        peerProbeResults.value.push(res.peers[0])
      }
    } else if (!targetNodeId) {
      peerProbeResults.value = res.peers || []
    }
  } catch (err) {
    ElMessage.error(err.message || '节点互联探测失败')
  } finally {
    if (targetNodeId) {
      peerProbingSingle[targetNodeId] = false
    } else {
      peerProbing.value = false
    }
  }
}

function handleCardCommand(node, command) {
  if (command === 'edit') openEdit(node)
  else if (command === 'networkProbe') openNetworkProbe(node)
  else if (command === 'upgradeAgent') upgradeAgent(node)
  else if (command === 'upgradeSingBox') upgradeSingBox(node)
  else if (command === 'revoke') revoke(node)
  else if (command === 'syncDDNS') triggerManualDDNS(node)
}

function onAuthSuccess() {
  load(true)
}

watch(
  () => appState.nodes,
  (nodes) => {
    if (nodes && nodes.length > 0 && Object.keys(metrics.value).length === 0) {
      load(true)
    }
  },
  { deep: true },
)

onMounted(() => {
  load()
  window.addEventListener('sb:authenticated', onAuthSuccess)
  stopConnections = subscribeConnections(() => {})
  stopLive = subscribeLive((event) => {
    // 节点状态变更通过 App.vue 增量 patch，性能指标通过 connections SSE 增量推流
    // 禁止全量调用 load() 重复查询数据库，仅当注册审批变动时轻量同步审批列表
    if (event.kind === 'registration' && isAdmin.value) {
      api('/registrations').then((res) => {
        pending.value = res.registrations || []
      }).catch(() => {})
    }
  })
})

onBeforeUnmount(() => {
  window.removeEventListener('sb:authenticated', onAuthSuccess)
  stopLive?.()
  stopConnections?.()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="服务器">
      <el-button v-if="canWrite" :icon="Connection" @click="probesDialog = true">探针</el-button>
      <el-button :icon="Refresh" :loading="refreshing" @click="load">刷新</el-button>
      <el-button v-if="canWrite" :icon="Share" @click="shareOpen = true">分享</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="generateToken">新增</el-button>
    </PageHeader>

    <ShareDialog v-model="shareOpen" page="nodes" />

    <el-dialog v-model="probesDialog" title="探针" width="960px" destroy-on-close>
      <ProbeManager v-if="probesDialog" />
    </el-dialog>

    <main v-loading.lock="loading" class="page-content page-content--tight">
      <!-- 0. 节点详情视图 (点击任意卡片或列表行进入) -->
      <NodeDetail
        v-if="selectedNode"
        :node="selectedNode"
        :metrics="metrics[selectedNode.id] || {}"
        :live="live.get(selectedNode.id) || null"
        :sing-box-latest="singBoxLatest"
        @back="selectedNodeId = null"
        @edit="openEdit"
        @probe="openNetworkProbe"
        @upgrade-agent="upgradeAgent"
        @upgrade-singbox="upgradeSingBox"
      />

      <!-- 主视图：概览 + 工具栏 + 服务器网格/列表 -->
      <template v-else>
        <!-- 1. 顶部监控大盘概览卡片 (SummaryCards + NodeMap) -->
        <SummaryCards :nodes="appState.nodes" :metrics="metrics" :live="live" @open="(id) => selectedNodeId = id" />

        <!-- 2. 工具栏与视图切换器 -->
        <div class="nodes-toolbar neu-track">
          <div class="toolbar-left">
            <el-input
              v-model="keyword"
              clearable
              :prefix-icon="Search"
              placeholder="搜索节点名称、IP地址、系统或架构..."
              style="width: 280px"
            />

            <!-- 快速状态分段筛选胶囊 -->
            <div class="status-segmented neu-track">
              <button
                class="seg-btn neu-press"
                :class="{ active: statusFilter === '' }"
                @click="statusFilter = ''"
              >
                全部 ({{ stats.total }})
              </button>
              <button
                class="seg-btn neu-press"
                :class="{ active: statusFilter === 'online' }"
                @click="statusFilter = 'online'"
              >
                在线 ({{ stats.online }})
              </button>
              <button
                v-if="stats.offline > 0"
                class="seg-btn neu-press"
                :class="{ active: statusFilter === 'offline' }"
                @click="statusFilter = 'offline'"
              >
                离线 ({{ stats.offline }})
              </button>
            </div>

            <el-select v-model="sortBy" placeholder="排序方式" style="width: 140px">
              <el-option label="服务器名称" value="name" />
              <el-option label="活跃时间优先" value="last_seen" />
              <el-option label="连接数最高" value="conn" />
              <el-option label="CPU 负载最高" value="cpu" />
              <el-option label="内存占用最高" value="mem" />
            </el-select>
          </div>

          <!-- 卡片 / 列表视图切换器 与 主题切换 -->
          <div class="toolbar-right">
            <div class="view-switcher neu-track">
              <button
                class="view-btn neu-press"
                :class="{ active: viewMode === 'card' }"
                title="卡片视图"
                @click="viewMode = 'card'"
              >
                <el-icon><Grid /></el-icon>
              </button>
              <button
                class="view-btn neu-press"
                :class="{ active: viewMode === 'list' }"
                title="列表视图"
                @click="viewMode = 'list'"
              >
                <el-icon><Tickets /></el-icon>
              </button>
            </div>
          </div>
        </div>

        <!-- 3. 等待接入审核卡片 (Pending) -->
        <template v-if="pending.length">
          <div class="pending-section-banner">
            <div class="banner-title">
              <span class="status-indicator pending pulse-sm" />
              <span>待批准接入的服务器</span>
              <span class="badge-pending-count">{{ pending.length }}</span>
            </div>
          </div>
          <div class="pending-grid">
            <div v-for="item in filteredPending" :key="item.id" class="pending-card neu">
              <div class="pending-header">
                <div class="pending-title">
                  <span class="flag-icon">⚡</span>
                  <strong>{{ item.node_name }}</strong>
                </div>
                <el-button type="primary" size="small" @click="approve(item)">批准接入</el-button>
              </div>
              <div class="pending-body">
                <div class="pending-line">
                  <span class="pending-label">支持特性:</span>
                  <span class="pending-val">{{ item.capabilities || '基础代理转发' }}</span>
                </div>
                <div class="pending-line">
                  <span class="pending-label">接入申请:</span>
                  <span class="pending-val font-mono">{{ formatDateTime(item.created_at) }}</span>
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 4. 节点列表：卡片视图 (Card View) 与 列表视图 (List View) -->
        <div v-if="filteredNodes.length === 0" class="empty-cards-box">
          <el-empty description="未找到符合条件的服务器" />
        </div>

        <!-- 4A. 卡片视图 (Card View - 100% 对齐 Openlat Micro-neomorphic uniform grid) -->
        <div v-else-if="viewMode === 'card'" class="openlat-cards-grid">
          <NodeCard
            v-for="node in filteredNodes"
            :key="node.id"
            :node="node"
            :metrics="metrics[node.id] || {}"
            :live="live.get(node.id) || null"
            :can-manage="canWrite || isAdmin"
            :sing-box-latest="singBoxLatest"
            @open="selectedNodeId = node.id"
            @action="handleCardAction"
          />
        </div>

        <!-- 4B. 列表视图 (List View - 100% 对齐 Openlat Micro-neomorphic stacked row) -->
        <div v-else class="openlat-list-rows">
          <NodeRow
            v-for="node in filteredNodes"
            :key="node.id"
            :node="node"
            :metrics="metrics[node.id] || {}"
            :live="live.get(node.id) || null"
            :sing-box-latest="singBoxLatest"
            @open="selectedNodeId = node.id"
          />
        </div>
      </template>
    </main>

    <!-- 添加节点 Token 弹窗 -->
    <el-dialog v-model="tokenDialog" title="新增服务器" width="560px" append-to-body>
      <div style="margin-bottom: 14px">
        <div style="font-size: 13px; font-weight: 500; margin-bottom: 6px; color: var(--sb-text-1)">
          Master 地址（Agent 拨号连接的主机或 IP）
        </div>
        <el-input v-model="masterHost" placeholder="例如：161.118.209.69 或 polaris.example.com">
          <template #append>:{{ agentPort }}</template>
        </el-input>
        <div class="subtle" style="font-size: 12px; margin-top: 4px">
          默认按当前控制台访问地址填入。若 Agent 需通过其他公网 IP 或域名回连，请在此修改，安装命令将自动联动更新并记住。
        </div>
      </div>

      <p style="margin-top: 0; color: var(--sb-text-2)">
        在目标 Linux 服务器上执行以下安装命令，即可自动配置并建立双向安全加密隧道接入平台：
      </p>
      <div class="terminal-box font-mono">
        <code>{{ installCommand }}</code>
      </div>
      <div style="margin-top: 14px; display: flex; justify-content: flex-end; gap: 8px">
        <el-button @click="tokenDialog = false">关闭</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyInstallCommand">复制</el-button>
      </div>
    </el-dialog>

    <!-- 配置服务器弹窗 -->
    <el-dialog v-model="editDialog" width="680px" class="srv-dialog" :show-close="false" align-center append-to-body>
      <template #header="{ close }">
        <div v-if="editHeader" class="dlg-hero" :class="`is-${editHeader.status}`">
          <div class="srv-avatar" :class="{ 'is-flag': editHeader.isFlag }">{{ editHeader.avatar }}</div>
          <div class="dlg-hero-text">
            <div class="dlg-hero-kicker">配置服务器</div>
            <div class="dlg-hero-name">
              <span>{{ editHeader.name || '未命名服务器' }}</span>
              <span class="srv-status" :class="`is-${editHeader.status}`"><i class="srv-status-dot" />{{ editHeader.statusText }}</span>
            </div>
            <div class="dlg-hero-sub font-mono">{{ editHeader.sub }}</div>
          </div>
          <button class="srv-icon-btn" title="关闭" @click="close"><el-icon><Close /></el-icon></button>
        </div>
      </template>

      <div class="dlg-tabs" role="tablist">
        <button
          v-for="t in editTabs"
          :key="t.key"
          class="dlg-tab"
          :class="{ active: editTab === t.key }"
          role="tab"
          :aria-selected="editTab === t.key"
          @click="editTab = t.key"
        >
          {{ t.label }}<span v-if="t.badge" class="dlg-tab-badge">{{ t.badge }}</span>
        </button>
      </div>

      <el-form label-position="top" class="dlg-form">
        <section v-show="editTab === 'basic'" class="dlg-pane">
          <el-form-item label="服务器名称">
            <el-input v-model="editForm.name" placeholder="请输入服务器名称" />
            <div class="dlg-help">名称开头写国旗（如 🇭🇰 香港 01），卡片会把它显示成大号头像。</div>
          </el-form-item>
          <el-form-item label="客户端连接域名或 IP">
            <el-input v-model="editForm.client_address" placeholder="例如：hk.example.com 或 1.2.3.4" />
            <div v-if="editNodeAllIPs.length" class="dlg-pick">
              <span class="dlg-pick-label">点选本机出口 IP</span>
              <button
                v-for="ip in editNodeAllIPs"
                :key="ip"
                type="button"
                class="srv-chip dlg-pick-chip font-mono"
                :class="{ 'is-address': editForm.client_address === ip }"
                @click="editForm.client_address = ip"
              >
                {{ editForm.ip_aliases?.[ip] ? `${editForm.ip_aliases[ip]} · ${ip}` : ip }}
              </button>
            </div>
          </el-form-item>
          <div class="dlg-facts">
            <div><span>系统</span><b class="font-mono">{{ editNode?.os_name || editNode?.os || '—' }} / {{ editNode?.architecture || '—' }}</b></div>
            <div><span>Agent</span><b class="font-mono">v{{ editNode?.agent_version || '—' }}</b></div>
            <div><span>sing-box</span><b class="font-mono">{{ editNode?.sing_box_version || '—' }}</b></div>
            <div><span>最后活跃</span><b class="font-mono">{{ formatFriendlyTime(editNode?.last_seen_at) }}</b></div>
          </div>
        </section>

        <section v-show="editTab === 'plan'" class="dlg-pane">
          <div class="dlg-grid">
            <el-form-item label="厂商">
              <el-input v-model="editForm.provider" placeholder="例如：DigitalOcean" />
            </el-form-item>
            <el-form-item label="厂商访问 URL">
              <el-input v-model="editForm.provider_url" placeholder="https://..." />
            </el-form-item>
            <el-form-item label="计价周期">
              <el-select v-model="editForm.price_period" placeholder="选择计价周期" clearable style="width: 100%">
                <el-option label="免费" value="free" />
                <el-option label="按天" value="day" />
                <el-option label="按月" value="month" />
                <el-option label="按季" value="quarter" />
                <el-option label="按年" value="year" />
              </el-select>
            </el-form-item>
            <el-form-item label="价格">
              <div class="dlg-inline">
                <el-input v-model="editForm.price" :disabled="editForm.price_period === 'free'" placeholder="价格" />
                <el-select v-model="editForm.currency" :disabled="editForm.price_period === 'free'" style="width: 120px">
                  <el-option label="美元 USD" value="USD" />
                  <el-option label="人民币 CNY" value="CNY" />
                  <el-option label="欧元 EUR" value="EUR" />
                  <el-option label="加元 CAD" value="CAD" />
                </el-select>
              </div>
            </el-form-item>
            <el-form-item label="流量套餐（按月）">
              <div class="dlg-inline">
                <el-input v-model="editForm.traffic_value" placeholder="例如：2" />
                <el-select v-model="editForm.traffic_unit" style="width: 110px">
                  <el-option label="MB" value="MB" />
                  <el-option label="GB" value="GB" />
                  <el-option label="TB" value="TB" />
                </el-select>
              </div>
            </el-form-item>
            <el-form-item label="带宽大小">
              <div class="dlg-inline">
                <el-input v-model="editForm.bandwidth_value" placeholder="例如：1" />
                <el-select v-model="editForm.bandwidth_unit" style="width: 110px">
                  <el-option label="Mbps" value="Mbps" />
                  <el-option label="Gbps" value="Gbps" />
                </el-select>
              </div>
            </el-form-item>
            <el-form-item label="到期日期" class="dlg-span-2">
              <el-date-picker
                v-model="editForm.expires_on"
                type="date"
                value-format="YYYY-MM-DD"
                placeholder="选择到期日期"
                clearable
                style="width: 100%"
              />
            </el-form-item>
          </div>
        </section>

        <section v-show="editTab === 'network'" class="dlg-pane">
          <div class="dlg-block">
            <div class="dlg-block-head">
              <div>
                <div class="dlg-block-title">出口 IP 与别名</div>
                <div class="dlg-help">以下出口 IP 由本机网卡自动检测。配置用户出口与节点分流时可使用别名。</div>
              </div>
            </div>
            <div v-if="editNodeAllIPs.length" class="dlg-ip-list">
              <div v-for="ip in editNodeAllIPs" :key="ip" class="dlg-ip-row">
                <span class="dlg-ip-kind" :class="ip.includes(':') ? 'is-v6' : 'is-v4'">{{ ip.includes(':') ? 'IPv6' : 'IPv4' }}</span>
                <span class="dlg-ip-addr font-mono">{{ ip }}</span>
                <el-input v-model="editForm.ip_aliases[ip]" size="small" placeholder="别名，例如：香港原生" clearable class="dlg-ip-alias" />
                <el-button
                  v-if="!editNode?.ip_addresses?.includes(ip)"
                  text
                  type="danger"
                  size="small"
                  :icon="Delete"
                  title="删除该出口 IP"
                  @click="removeCustomEgressIP(ip)"
                />
              </div>
            </div>
            <div v-else class="dlg-empty">暂未检测到出口 IP。</div>
          </div>

          <div class="dlg-block" :class="{ 'is-on': editForm.ddns_enabled }">
            <div class="dlg-block-head">
              <div>
                <div class="dlg-block-title">Cloudflare 动态域名解析 (DDNS)</div>
                <div class="dlg-help">服务器公网 IP 变化时，自动更新绑定的 Cloudflare A / AAAA 记录。</div>
              </div>
              <el-switch v-model="editForm.ddns_enabled" />
            </div>
            <template v-if="editForm.ddns_enabled">
              <el-select
                v-model="editForm.ddns_record_ids"
                multiple
                filterable
                collapse-tags
                collapse-tags-tooltip
                placeholder="选择绑定的 Cloudflare A 记录（支持多选）"
                style="width: 100%"
                :loading="cfLoading"
                @change="onRecordsSelected"
              >
                <el-option v-for="rec in cfRecords" :key="rec.id" :label="`${rec.name} (${rec.content})`" :value="rec.id" />
              </el-select>
              <div v-if="(editForm.ddns_domains || []).length" class="dlg-pick">
                <span v-for="d in editForm.ddns_domains" :key="d" class="srv-chip is-violet font-mono">{{ d }}</span>
              </div>
            </template>
          </div>
        </section>
      </el-form>

      <template #footer>
        <div class="dlg-foot">
          <el-button @click="editDialog = false">取消</el-button>
          <el-button type="primary" :loading="editSaving" @click="saveEdit">保存</el-button>
        </div>
      </template>
    </el-dialog>

    <!-- 线路与丢包排查弹窗 -->
    <el-dialog v-model="probeDialog" width="820px" class="probe-dialog" align-center append-to-body>
      <template #header>
        <div v-if="probeNode" class="probe-hero">
          <div class="probe-hero-title">
            <span class="probe-main-title">线路与丢包排查</span>
            <span class="probe-hero-node">{{ probeNode.name }}</span>
            <span v-if="getNodePublicIPv4(probeNode)" class="probe-ip-tag v4">IPv4</span>
            <span v-if="getNodePublicIPv6(probeNode)" class="probe-ip-tag v6">IPv6</span>
          </div>
          <div class="probe-hero-sub">
            <span>目标服务器位置: {{ probeNode.city || probeNode.location || '全球节点' }}</span>
            <span v-if="getNodePublicIPv4(probeNode)"> · {{ getNodePublicIPv4(probeNode) }}</span>
            <span v-if="getNodePublicIPv6(probeNode)"> · {{ getNodePublicIPv6(probeNode) }}</span>
          </div>
        </div>
      </template>

      <el-tabs v-model="probeActiveTab" class="probe-tabs">
        <!-- 标签页 1: 客户端至该节点 -->
        <el-tab-pane label="客户端至该节点" name="client">
          <div class="probe-client-panel">
            <!-- 链路概览条 -->
            <div class="probe-route-card">
              <div class="probe-route-endpoint is-source">
                <span class="ep-badge">本地端</span>
                <div class="ep-info">
                  <div class="ep-title">{{ clientInfo.location || '本地客户端网络' }}</div>
                  <div class="ep-ip font-mono">{{ clientTargetIP || clientInfo.ip || '获取中...' }} <span v-if="clientInfo.isp">({{ clientInfo.isp }})</span></div>
                </div>
              </div>
              <div class="probe-route-connector">
                <span class="connector-line"></span>
                <span class="connector-label">端到端双向互联</span>
                <span class="connector-line"></span>
              </div>
              <div class="probe-route-endpoint is-target">
                <span class="ep-badge">服务器端</span>
                <div class="ep-info">
                  <div class="ep-title">{{ probeNode?.name }}</div>
                  <div class="ep-ip font-mono">{{ getNodePublicIPv4(probeNode) || getNodePublicIPv6(probeNode) || probeNode?.city || '远程服务器' }}</div>
                </div>
              </div>
            </div>

            <!-- 控制区 / 探测配置 -->
            <div class="probe-control-bar">
              <div class="probe-target-ip-wrap">
                <span class="ctrl-label">目标客户端 IP:</span>
                <el-input
                  v-model="clientTargetIP"
                  size="small"
                  placeholder="请输入探测的目标客户端 IP"
                  style="width: 220px"
                  :disabled="clientProbing"
                  @keyup.enter="runClientProbe"
                />
              </div>

              <div class="probe-proto-indicator">
                <span class="ctrl-label">网络栈:</span>
                <el-tag size="small" :type="clientTargetIP?.includes(':') ? 'warning' : 'success'">
                  {{ clientTargetIP?.includes(':') ? 'IPv6 链路' : 'IPv4 链路' }}
                </el-tag>
              </div>

              <el-button
                type="primary"
                size="small"
                :icon="Refresh"
                :loading="clientProbing"
                @click="runClientProbe"
              >
                {{ clientProbing ? '正在高精度采样中...' : '重新检测链路' }}
              </el-button>
            </div>

            <!-- 采样中动画骨架 -->
            <div v-if="clientProbing" class="probe-loading-state">
              <el-icon class="is-loading probe-spin-icon"><Loading /></el-icon>
              <div class="loading-text">
                正在由远程服务器对公网 IP「{{ clientTargetIP || clientInfo.ip }}」发送 4 轮高精度 ICMP / TCP 探测...
              </div>
            </div>

            <!-- 探测结果卡片区 -->
            <template v-else-if="clientProbeStats">
              <!-- 场景一：正常收到回包 -->
              <template v-if="clientProbeStats.hasResponse">
                <div class="probe-stats-grid">
                  <div class="probe-stat-box" :class="{ 'is-loss': clientProbeStats.lossPercent > 0 }">
                    <div class="stat-num font-mono">{{ clientProbeStats.lossPercent }}%</div>
                    <div class="stat-name">丢包率</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono">
                      {{ clientProbeStats.latency?.toFixed(1) }}<span class="unit">ms</span>
                    </div>
                    <div class="stat-name">往返延迟 (RTT)</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono">
                      {{ clientProbeStats.minLatency?.toFixed(0) }} ~ {{ clientProbeStats.maxLatency?.toFixed(0) }}<span class="unit">ms</span>
                    </div>
                    <div class="stat-name">极值波动 (Min ~ Max)</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono" style="font-size: 15px; line-height: 28px;">
                      {{ clientProbeStats.method }} · {{ clientProbeStats.received }}/{{ clientProbeStats.sent }} 包
                    </div>
                    <div class="stat-name">发包统计与协议</div>
                  </div>
                </div>

                <!-- 采样脉冲明细 -->
                <div class="probe-pulses-row" v-if="clientProbeStats.latencies?.length > 0">
                  <span class="pulses-title">高频往返脉冲记录 ({{ clientProbeStats.latencies.length }}次采样):</span>
                  <div class="pulses-list">
                    <span
                      v-for="(lat, idx) in clientProbeStats.latencies"
                      :key="idx"
                      class="pulse-pill is-ok"
                    >
                      <i class="pulse-dot" />
                      #{{ idx + 1 }}: {{ lat.toFixed(1) }}ms
                    </span>
                    <span
                      v-for="lostIdx in (clientProbeStats.sent - clientProbeStats.received)"
                      :key="'lost-' + lostIdx"
                      class="pulse-pill is-fail"
                    >
                      <i class="pulse-dot" />
                      #{{ clientProbeStats.received + lostIdx }}: 丢包超时
                    </span>
                  </div>
                </div>
              </template>

              <!-- 场景二：未收到回包（家庭宽带光猫/本地防火墙拦截） -->
              <template v-else>
                <div class="probe-stats-grid">
                  <div class="probe-stat-box is-warning">
                    <div class="stat-num font-mono" style="font-size: 16px; color: var(--sb-warning);">本地路由拦截</div>
                    <div class="stat-name">链路状态</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono">{{ clientProbeStats.sent }} 次已发出</div>
                    <div class="stat-name">发包记录</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono">0 次响应</div>
                    <div class="stat-name">回包数量</div>
                  </div>
                  <div class="probe-stat-box">
                    <div class="stat-num font-mono" style="font-size: 15px; line-height: 28px;">
                      {{ clientProbeStats.method }} 探测
                    </div>
                    <div class="stat-name">探测协议</div>
                  </div>
                </div>

                <div class="probe-firewall-notice">
                  <div class="notice-title">说明：服务器未收到来自客户端公网 IP 的回包</div>
                  <div class="notice-desc">
                    服务器已向目标 IP「{{ clientTargetIP || clientInfo.ip }}」发送 4 轮探测，但未收到回应。多数家庭宽带光猫或本地路由器默认启用了外网防探测防火墙（禁止 WAN 口 ICMP Ping 及主动入站连接），导致探测包被本地网络设备丢弃。这是家用网络的正常安全保护机制。若需测试链路，可在上方输入已开放 Ping 响应的公网 IP。
                  </div>
                </div>
              </template>
            </template>
          </div>
        </el-tab-pane>

        <!-- 标签页 2: 各节点间互联线路与丢包 -->
        <el-tab-pane label="各节点间互联线路与丢包" name="mesh">
          <div class="probe-mesh-panel">
            <div class="mesh-topbar">
              <span class="mesh-hint">从当前节点服务器探测至集群内其他各 VPS 节点的网络互联、丢包与延迟</span>
              <el-button type="primary" size="small" :icon="Refresh" :loading="peerProbing" @click="runPeerProbes('')">
                一键检测所有互联节点
              </el-button>
            </div>

            <div v-if="peerProbeResults.length === 0 && !peerProbing" class="mesh-empty">
              <span>暂无互联探测数据，点击上方按钮开始探测</span>
            </div>

            <div v-else class="mesh-table-wrap">
              <table class="mesh-table">
                <thead>
                  <tr>
                    <th>目标服务器</th>
                    <th>所在区域/线路</th>
                    <th>IPv4 互联丢包与延迟</th>
                    <th>IPv6 互联丢包与延迟</th>
                    <th style="text-align: right">操作</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="peer in peerProbeResults" :key="peer.target_node_id">
                    <td>
                      <div class="peer-name font-mono">{{ peer.target_node_name }}</div>
                    </td>
                    <td>
                      <div class="peer-loc">{{ peer.target_location }}</div>
                    </td>
                    <td>
                      <div v-if="peer.ipv4_target" class="peer-probe-cell">
                        <span class="cell-tag v4">IPv4</span>
                        <span v-if="peer.ipv4_status === 'ok'" class="cell-val is-ok">
                          <span class="cell-method">{{ peer.ipv4_method ? peer.ipv4_method.toUpperCase() : 'ICMP' }}</span>
                          <b>{{ peer.ipv4_latency_ms.toFixed(1) }}ms</b> · 0% 丢包
                          <small class="cell-details" v-if="peer.ipv4_sent">({{ peer.ipv4_received }}/{{ peer.ipv4_sent }}包<template v-if="peer.ipv4_min_latency">, {{ peer.ipv4_min_latency.toFixed(0) }}~{{ peer.ipv4_max_latency.toFixed(0) }}ms</template>)</small>
                        </span>
                        <span v-else-if="peer.ipv4_status === 'loss'" class="cell-val is-loss">
                          <span class="cell-method">{{ peer.ipv4_method ? peer.ipv4_method.toUpperCase() : 'ICMP' }}</span>
                          <b>{{ peer.ipv4_latency_ms.toFixed(1) }}ms</b> · {{ peer.ipv4_loss }}% 丢包
                          <small class="cell-details" v-if="peer.ipv4_sent">({{ peer.ipv4_received }}/{{ peer.ipv4_sent }}包)</small>
                        </span>
                        <span v-else class="cell-val is-fail">不可达 / 超时 (4/4 丢包)</span>
                      </div>
                      <div v-else class="cell-val is-dim">无 IPv4</div>
                    </td>
                    <td>
                      <div v-if="peer.ipv6_target" class="peer-probe-cell">
                        <span class="cell-tag v6">IPv6</span>
                        <span v-if="peer.ipv6_status === 'ok'" class="cell-val is-ok">
                          <span class="cell-method">{{ peer.ipv6_method ? peer.ipv6_method.toUpperCase() : 'ICMP' }}</span>
                          <b>{{ peer.ipv6_latency_ms.toFixed(1) }}ms</b> · 0% 丢包
                          <small class="cell-details" v-if="peer.ipv6_sent">({{ peer.ipv6_received }}/{{ peer.ipv6_sent }}包<template v-if="peer.ipv6_min_latency">, {{ peer.ipv6_min_latency.toFixed(0) }}~{{ peer.ipv6_max_latency.toFixed(0) }}ms</template>)</small>
                        </span>
                        <span v-else-if="peer.ipv6_status === 'loss'" class="cell-val is-loss">
                          <span class="cell-method">{{ peer.ipv6_method ? peer.ipv6_method.toUpperCase() : 'ICMP' }}</span>
                          <b>{{ peer.ipv6_latency_ms.toFixed(1) }}ms</b> · {{ peer.ipv6_loss }}% 丢包
                          <small class="cell-details" v-if="peer.ipv6_sent">({{ peer.ipv6_received }}/{{ peer.ipv6_sent }}包)</small>
                        </span>
                        <span v-else class="cell-val is-fail">不可达 / 超时 (4/4 丢包)</span>
                      </div>
                      <div v-else class="cell-val is-dim">未配置 IPv6</div>
                    </td>
                    <td style="text-align: right">
                      <el-button
                        size="small"
                        link
                        :loading="Boolean(peerProbingSingle[peer.target_node_id])"
                        @click="runPeerProbes(peer.target_node_id)"
                      >
                        重测
                      </el-button>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </el-tab-pane>
      </el-tabs>
    </el-dialog>
  </div>
</template>


<style scoped>
:global(.el-popper) {
  line-height: 1.6;
  font-size: 12px;
}
/* 悬浮提示融入系统风格（--sb-* token） */
:global(.el-popper.is-dark) {
  background: var(--sb-surface-3) !important;
  border: 1px solid var(--sb-line-strong);
  color: var(--sb-text-2) !important;
  border-radius: var(--sb-radius-sm) !important;
  box-shadow: var(--sb-shadow);
  padding: 8px 12px;
}
:global(.el-popper.is-dark .el-popper__arrow::before) {
  background: var(--sb-surface-3) !important;
  border: 1px solid var(--sb-line-strong) !important;
}
:global(.el-popper b) {
  color: var(--sb-accent);
  font-weight: 600;
}

/* ── 呼吸指示灯 ─────────────────────────────────────────── */
.status-indicator {
  position: relative;
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.status-indicator.online {
  background: var(--sb-success);
  box-shadow: 0 0 8px rgba(16, 185, 129, 0.85);
}
.status-indicator.online::after {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  border: 1.5px solid var(--sb-success);
  animation: pulseLight 2s cubic-bezier(0.24, 0, 0.38, 1) infinite;
}
.status-indicator.offline {
  background: var(--sb-muted);
  box-shadow: 0 0 4px rgba(100, 116, 139, 0.4);
}
.status-indicator.blocked {
  background: var(--sb-danger);
  box-shadow: 0 0 8px rgba(244, 63, 94, 0.9);
}
.status-indicator.blocked::after {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  border: 1.5px solid var(--sb-danger);
  animation: pulseLight 1.5s cubic-bezier(0.24, 0, 0.38, 1) infinite;
}
.status-indicator.pending {
  background: var(--sb-warning);
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.8);
}
@keyframes pulseLight {
  0% { transform: scale(0.9); opacity: 0.9; }
  60% { transform: scale(1.7); opacity: 0; }
  100% { transform: scale(0.9); opacity: 0; }
}

/* ── 工具栏与视图切换 ───────────────────────────────────── */
.nodes-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 18px;
  padding: 10px 14px;
  border-radius: 12px;
}
.toolbar-left {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.toolbar-right {
  display: flex;
  align-items: center;
}
.status-segmented {
  display: inline-flex;
  align-items: center;
  padding: 3px;
  border-radius: 9px;
}
.seg-btn {
  padding: 4px 10px;
  border: none;
  background: transparent;
  color: var(--sb-muted);
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.seg-btn:hover {
  color: var(--sb-text);
}
.seg-btn.active {
  background: rgba(56, 189, 248, 0.2);
  color: var(--sb-accent);
  font-weight: 600;
  box-shadow: 0 0 10px rgba(56, 189, 248, 0.2);
}
.seg-btn.seg-blocked.active {
  background: rgba(244, 63, 94, 0.22);
  color: var(--sb-danger);
  box-shadow: 0 0 10px rgba(244, 63, 94, 0.25);
}

.toolbar-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.theme-btn {
  display: grid;
  place-items: center;
  width: 34px;
  height: 34px;
  border-radius: 9px;
  border: 1px solid var(--sb-line);
  color: var(--sb-muted);
  cursor: pointer;
  font-size: 15px;
  transition: all 0.2s ease;
}
.theme-btn:hover {
  color: var(--sb-accent);
  border-color: var(--sb-accent);
}
.view-switcher {
  display: inline-flex;
  align-items: center;
  padding: 3px;
  border-radius: 9px;
  gap: 3px;
}
.view-btn {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border: none;
  background: transparent;
  color: var(--sb-muted);
  border-radius: 6px;
  cursor: pointer;
  font-size: 14px;
}
.view-btn:hover {
  color: var(--sb-text);
}
.view-btn.active {
  background: rgba(56, 189, 248, 0.22);
  color: var(--sb-accent);
  box-shadow: 0 0 10px rgba(56, 189, 248, 0.2);
}

/* ── 3. 待接入横幅 (Pending Section) ────────────────────── */
.pending-section-banner {
  margin-bottom: 12px;
}
.banner-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--sb-text-2);
}
.badge-pending-count {
  font-size: 11px;
  font-weight: 700;
  padding: 1px 6px;
  border-radius: 10px;
  background: rgba(245, 158, 11, 0.2);
  color: var(--sb-warning);
  border: 1px solid rgba(245, 158, 11, 0.4);
}
.pending-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
  margin-bottom: 24px;
}
.pending-card {
  padding: 14px 16px;
  background: rgba(245, 158, 11, 0.04);
  border: 1px solid rgba(245, 158, 11, 0.25);
  border-radius: 10px;
}
.pending-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 10px;
}
.pending-title {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: var(--sb-warning);
}
.pending-body {
  font-size: 12px;
  color: var(--sb-muted);
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.pending-line {
  display: flex;
  justify-content: space-between;
}
.pending-val {
  color: var(--sb-text-2);
}

/* ── 服务器卡片与列表视图 (Openlat micro-neomorphic) ─────── */
.openlat-cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(350px, 1fr));
  gap: 16px;
}
.openlat-list-rows {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.n-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 14px;
}
.n-card {
  --tone: var(--sb-success);
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-width: 0;
  padding: 14px;
  border-radius: 16px;
  overflow: hidden;
  transition: transform .2s ease, border-color .2s ease, box-shadow .2s ease;
}
.n-card:hover {
  transform: translateY(-2px);
  border-color: rgba(56, 189, 248, .45);
  box-shadow: var(--sh-neu), 0 0 24px rgba(56, 189, 248, .12);
}
.n-card-blob {
  position: absolute;
  top: -30px;
  right: -30px;
  width: 130px;
  height: 130px;
  border-radius: 50%;
  filter: blur(40px);
  pointer-events: none;
  opacity: 0.16;
}
.n-card-blob.is-online { background: #34d399; }
.n-card-blob.is-offline { background: #94a3b8; }
.n-card-blob.is-blocked { background: #fb7185; }

.n-card.is-offline {
  --tone: var(--sb-muted);
}
.n-card.is-offline .n-gauges,
.n-card.is-offline .n-net-pill,
.n-card.is-offline .n-card-latency {
  opacity: .65;
}
.n-card.is-blocked {
  --tone: var(--sb-danger);
}

.n-card-latency {
  margin-top: 2px;
}

.n-head {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.n-avatar {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 32px;
  height: 32px;
  border-radius: 8px;
  background: var(--sb-surface-3);
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  font-size: 13px;
  font-weight: 700;
}
.n-avatar.is-flag {
  font-size: 16px;
  font-weight: 400;
  background: transparent;
  border-color: transparent;
  color: var(--sb-text);
}
.n-title {
  flex: 1;
  min-width: 0;
}
.n-name-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.n-name {
  margin: 0;
  min-width: 0;
  color: var(--sb-text);
  font-size: 14.5px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.n-meta {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--sb-muted);
  font-size: 11px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  margin-top: 2px;
}
.n-os-icon {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
}
.n-meta .n-region {
  flex-shrink: 0;
}

.n-ops {
  display: flex;
  align-self: flex-start;
  gap: 5px;
  flex-shrink: 0;
}
.n-ops .srv-icon-btn {
  width: 28px;
  height: 28px;
  border-radius: 8px;
}

.n-chips {
  display: flex;
  flex-wrap: nowrap;
  gap: 5px;
  min-width: 0;
}
.n-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  min-width: 0;
  overflow: hidden;
  padding: 3px 8px;
  border-radius: 7px;
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  font-size: 11px;
  line-height: 1.5;
}
.n-chip-txt {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.n-chip .el-icon {
  flex-shrink: 0;
  font-size: 11px;
  opacity: .7;
}
.n-chip.is-address {
  color: var(--sb-accent);
  cursor: pointer;
  text-align: left;
  transition: border-color .15s;
}
.n-chip.is-address:hover { border-color: var(--sb-accent); }
.n-chip.is-violet {
  border-color: rgba(168, 85, 247, .3);
  background: rgba(168, 85, 247, .12);
  color: #d8b4fe;
}
.n-chip.is-danger {
  border-color: rgba(251, 113, 133, .35);
  background: rgba(251, 113, 133, .12);
  color: var(--sb-danger);
}

.n-gauges {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}
.n-gauge {
  min-width: 0;
  cursor: default;
}
.n-g-top {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  color: var(--sb-muted);
  font-size: 10.5px;
}
.n-g-top b {
  color: var(--sb-text-2);
  font-size: 11px;
  font-weight: 600;
}
.n-g-top b i {
  margin-left: 1px;
  color: var(--sb-muted);
  font-size: 9px;
  font-style: normal;
}
.n-g-track {
  height: 4px;
  margin-top: 4px;
  overflow: hidden;
  border-radius: 999px;
}
.n-g-track i {
  display: block;
  height: 100%;
  border-radius: 999px;
  transition: width .4s ease, background-color .3s ease;
}

.n-net-pill {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 8px;
  font-size: 11px;
}
.net-pill-left {
  display: flex;
  align-items: center;
  gap: 8px;
}
.net-pill-right {
  display: flex;
  align-items: center;
  gap: 8px;
}
.is-down { color: #38bdf8; font-weight: 600; }
.is-up { color: #f59e0b; font-weight: 600; }
.is-total { color: var(--sb-muted); font-size: 10.5px; }
.n-conn {
  padding: 0 6px;
  border-radius: 999px;
  background: rgba(148, 163, 184, .12);
  color: var(--sb-muted);
  font-size: 10px;
  line-height: 16px;
}
.n-conn.is-active {
  background: rgba(52, 211, 153, .16);
  color: var(--sb-success);
}

/* ── 列表视图样式 (List View Grid) ──────────────────────── */
.n-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.n-row {
  display: grid;
  grid-template-columns: minmax(0, 1.3fr) minmax(0, 1.5fr) minmax(0, 0.85fr) minmax(0, 0.8fr) minmax(0, 1fr) minmax(0, 0.7fr) auto;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
  border-radius: 12px;
}
.n-row.is-offline {
  opacity: 0.7;
}

.n-row-node {
  min-width: 0;
}
.n-row-head {
  display: flex;
  align-items: center;
  gap: 7px;
}
.n-row-avatar {
  font-size: 14px;
}
.n-row-name {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--sb-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.n-row-sub {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 2px;
  padding-left: 14px;
  font-size: 10px;
  color: var(--sb-muted);
}
.n-row-os-icon {
  width: 13px;
  height: 13px;
  flex-shrink: 0;
}
.n-row-plat {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.n-row-hard {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.hard-row {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 10px;
  line-height: 1;
}
.hard-label {
  width: 26px;
  flex-shrink: 0;
  color: var(--sb-muted);
}
.hard-track {
  flex: 1;
  min-width: 0;
  height: 5px;
  overflow: hidden;
  border-radius: 999px;
}
.hard-fill {
  display: block;
  height: 100%;
  border-radius: 999px;
  transition: width 0.4s ease;
}
.hard-pct {
  width: 28px;
  flex-shrink: 0;
  text-align: right;
  font-weight: 600;
}
.hard-detail {
  width: 90px;
  flex-shrink: 0;
  text-align: right;
  color: var(--sb-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.n-row-traffic {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}
.row-meta-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 10px;
  line-height: 1;
}

.n-row-net {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  font-size: 10px;
}
.net-line {
  display: flex;
  align-items: center;
  justify-content: space-between;
  line-height: 1;
  white-space: nowrap;
}
.net-line.is-down { color: #38bdf8; }
.net-line.is-up { color: #f59e0b; }
.net-line.is-sockets { color: var(--sb-muted); }

.n-row-latency {
  min-width: 0;
}

.n-row-expiry {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  font-size: 10.5px;
  text-align: right;
}
.expiry-line {
  font-weight: 550;
  white-space: nowrap;
}
.price-line {
  white-space: nowrap;
}

.n-row-ops {
  display: flex;
  align-items: center;
  gap: 5px;
  flex-shrink: 0;
}
.n-row-ops .srv-icon-btn {
  width: 28px;
  height: 28px;
  border-radius: 8px;
}

@media (max-width: 1200px) {
  .n-row {
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1.4fr) minmax(0, 0.9fr) minmax(0, 1fr) auto;
  }
  .n-row-traffic,
  .n-row-expiry {
    display: none;
  }
}
@media (max-width: 800px) {
  .n-row {
    grid-template-columns: minmax(0, 1.2fr) minmax(0, 1.4fr) auto;
  }
  .n-row-net,
  .n-row-latency {
    display: none;
  }
}

.n-link {
  flex-shrink: 0;
  margin-left: auto;
  padding: 0;
  border: none;
  background: none;
  color: var(--sb-accent);
  font-size: 11px;
  cursor: pointer;
  white-space: nowrap;
}
.n-link:disabled {
  cursor: progress;
  opacity: .7;
}

.n-foot {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin-top: auto;
  padding-top: 6px;
  border-top: 1px dashed var(--sb-line);
  font-size: 10.5px;
  color: var(--sb-muted);
}
.n-plan {
  display: flex;
  align-items: baseline;
  flex: 1;
  min-width: 0;
  gap: 8px;
  overflow: hidden;
  white-space: nowrap;
}
.n-plan > span,
.n-provider {
  overflow: hidden;
  text-overflow: ellipsis;
}
.n-plan .is-warning { color: var(--sb-warning); }
.n-plan .is-danger { color: var(--sb-danger); }
.n-provider {
  color: var(--sb-accent);
  font-weight: 600;
  text-decoration: none;
}
.n-provider[href]:hover { text-decoration: underline; }
.n-time {
  flex-shrink: 0;
  white-space: nowrap;
}

.empty-cards-box {
  display: flex;
  justify-content: center;
  padding: 40px 0;
}

.srv-avatar {
  display: grid;
  place-items: center;
  flex-shrink: 0;
  width: 42px;
  height: 42px;
  border-radius: 12px;
  background: var(--sb-accent-soft);
  color: var(--sb-accent);
  font-size: 15px;
  font-weight: 700;
}
.srv-avatar.is-flag {
  font-size: 20px;
  font-weight: 400;
  background: transparent;
}
.srv-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
}
.srv-status.is-online {
  color: var(--sb-success);
}
.srv-status.is-offline {
  color: var(--sb-muted);
}
.srv-status.is-blocked {
  color: var(--sb-danger);
}
.srv-status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}
.srv-status.is-online .srv-status-dot,
.srv-status.is-blocked .srv-status-dot {
  box-shadow: 0 0 0 0 currentColor;
  animation: srvPulse 2s ease-out infinite;
}
.srv-status.is-blocked .srv-status-dot { animation-duration: 1.4s; }
@keyframes srvPulse {
  0% { box-shadow: 0 0 0 0 currentColor; }
  70%, 100% { box-shadow: 0 0 0 6px transparent; }
}
.srv-icon-btn {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
  background: transparent;
  color: var(--sb-text-2);
  cursor: pointer;
  transition: color .15s, border-color .15s, background-color .15s;
}
.srv-icon-btn:hover:not(:disabled) {
  border-color: rgba(56, 189, 248, .4);
  background: var(--sb-accent-soft);
  color: var(--sb-accent);
}
.srv-icon-btn:disabled {
  cursor: progress;
  opacity: .6;
}
.srv-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  padding: 3px 8px;
  border-radius: 7px;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
  color: var(--sb-text-2);
  font-size: 11.5px;
  line-height: 1.5;
}
.srv-chip.is-address {
  border-color: rgba(56, 189, 248, .26);
  background: var(--sb-accent-soft);
  color: var(--sb-accent);
  cursor: pointer;
}
.srv-chip.is-violet {
  border-color: rgba(168, 85, 247, .3);
  background: rgba(168, 85, 247, .1);
  color: #d8b4fe;
}
.srv-chip.is-danger {
  border-color: rgba(251, 113, 133, .35);
  background: rgba(251, 113, 133, .1);
  color: var(--sb-danger);
}

.empty-cards-box {
  padding: 40px 0;
  display: flex;
  justify-content: center;
}
.danger-item {
  color: var(--sb-danger) !important;
}

.terminal-box {
  padding: 14px 16px;
  background: var(--neu-inset-bg, rgba(23, 27, 44, 0.9));
  border: 1px solid var(--sb-line-strong);
  border-radius: var(--sb-radius-sm);
  color: var(--sb-text-2, #E2E8F0);
  font-family: "Cascadia Code", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  line-height: 1.6;
  word-break: break-all;
  white-space: pre-wrap;
  user-select: all;
  box-shadow: var(--sh-inset);
}

/* ── 弹窗通用外壳 ───────────────────────────────────────── */
/* align-center 依赖 overlay 的 flex 布局；显式声明一次，避免被其他样式冲掉 */
:global(.el-overlay:has(.el-dialog.srv-dialog) .el-overlay-dialog) {
  display: flex;
  align-items: center;
  justify-content: center;
}
:global(.el-dialog.srv-dialog) {
  margin: auto;
  max-width: calc(100vw - 32px);
  padding: 0;
  overflow: hidden;
  border: 1px solid var(--sb-line-strong);
  border-radius: var(--sb-radius);
  background: var(--sb-surface);
  box-shadow: var(--sb-shadow);
}
:global(.el-dialog.srv-dialog .el-dialog__header) {
  margin: 0;
  padding: 0;
}
:global(.el-dialog.srv-dialog .el-dialog__body) {
  max-height: min(62vh, 600px);
  padding: 0 24px 4px;
  overflow-y: auto;
}
:global(.el-dialog.srv-dialog .el-dialog__footer) {
  padding: 12px 24px 16px;
  border-top: 1px solid var(--sb-line);
  background: var(--sb-surface);
}

.dlg-hero {
  --srv-tone: var(--sb-success);
  --srv-tone-soft: rgba(52, 211, 153, .1);
  position: relative;
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 20px 24px 16px;
  border-bottom: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
}
.dlg-hero::before {
  content: '';
  position: absolute;
  inset: 0 0 auto;
  height: 2px;
  background: var(--srv-tone);
  opacity: .55;
}
.dlg-hero.is-offline { --srv-tone: var(--sb-muted); --srv-tone-soft: rgba(148, 163, 184, .1); }
.dlg-hero.is-blocked { --srv-tone: var(--sb-danger); --srv-tone-soft: rgba(251, 113, 133, .12); }
.dlg-hero .srv-status { background: var(--srv-tone-soft); color: var(--srv-tone); }
.dlg-hero > .srv-icon-btn { align-self: flex-start; }
.dlg-hero-text { flex: 1; min-width: 0; }
.dlg-hero-kicker {
  color: var(--sb-muted);
  font-size: 11px;
  letter-spacing: .08em;
}
.dlg-hero-name {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 2px;
  color: var(--sb-text);
  font-size: 18px;
  font-weight: 700;
}
.dlg-hero-name > span:first-child {
  min-width: 0;
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.dlg-hero-sub {
  margin-top: 4px;
  overflow: hidden;
  color: var(--sb-muted);
  font-size: 12px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
/* 编辑弹窗：标签页 */
.dlg-tabs {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  gap: 4px;
  margin: 0 -24px 18px;
  padding: 0 24px;
  border-bottom: 1px solid rgba(148, 163, 184, .12);
  background: var(--sb-surface-2);
}
.dlg-tab {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 11px 14px;
  border: none;
  background: none;
  color: var(--sb-muted);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: color .15s;
}
.dlg-tab:hover { color: var(--sb-text); }
.dlg-tab.active { color: var(--sb-accent); }
.dlg-tab.active::after {
  content: '';
  position: absolute;
  inset: auto 10px -1px;
  height: 2px;
  border-radius: 2px;
  background: var(--sb-accent);
}
.dlg-tab-badge {
  min-width: 18px;
  padding: 0 5px;
  border-radius: 999px;
  background: rgba(56, 189, 248, .16);
  color: var(--sb-accent);
  font-size: 10.5px;
  line-height: 17px;
  text-align: center;
}

/* 编辑弹窗：表单内容 */
.dlg-form :deep(.el-form-item) { margin-bottom: 16px; }
.dlg-form :deep(.el-form-item__label) {
  color: var(--sb-text-2);
  font-size: 12.5px;
  font-weight: 600;
}
.dlg-pane { min-height: 300px; padding-bottom: 6px; }
.dlg-help {
  margin-top: 6px;
  color: var(--sb-muted);
  font-size: 12px;
  line-height: 1.55;
}
.dlg-pick {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  width: 100%;
  margin-top: 8px;
}
.dlg-pick-label {
  margin-right: 2px;
  color: var(--sb-muted);
  font-size: 12px;
}
.dlg-pick-chip { cursor: pointer; }
.dlg-pick-chip:hover { border-color: rgba(56, 189, 248, .4); }
.dlg-facts {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid rgba(148, 163, 184, .1);
  border-radius: 12px;
  background: rgba(148, 163, 184, .04);
}
.dlg-facts > div {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  padding: 10px 12px;
}
.dlg-facts > div + div { border-left: 1px solid rgba(148, 163, 184, .1); }
.dlg-facts span { color: var(--sb-muted); font-size: 11px; }
.dlg-facts b {
  overflow: hidden;
  color: var(--sb-text);
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.dlg-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  column-gap: 16px;
}
.dlg-span-2 { grid-column: 1 / -1; }
.dlg-inline {
  display: flex;
  gap: 8px;
  width: 100%;
}
.dlg-preview {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  border: 1px dashed rgba(56, 189, 248, .28);
  border-radius: 12px;
  background: rgba(56, 189, 248, .05);
  color: var(--sb-muted);
  font-size: 12px;
}
.dlg-preview b { color: var(--sb-accent); font-weight: 600; }
/* 编辑弹窗：网络分区 */
.dlg-block {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 14px;
  padding: 14px 16px;
  border: 1px solid var(--sb-line);
  border-radius: 12px;
  background: var(--sb-surface-2);
  transition: border-color .2s;
}
.dlg-block.is-on {
  border-color: color-mix(in oklab, var(--sb-accent) 35%, transparent);
}
.dlg-block-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.dlg-block-head .dlg-help {
  margin-top: 3px;
  color: var(--sb-muted);
  font-size: 12px;
  line-height: 1.4;
}
.dlg-block-title {
  color: var(--sb-text);
  font-size: 13px;
  font-weight: 600;
}
.dlg-ip-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.dlg-ip-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 10px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-3);
}
.dlg-ip-kind {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}
.dlg-ip-kind.is-v4 {
  background: color-mix(in oklab, var(--sb-success) 14%, transparent);
  color: var(--sb-success);
}
.dlg-ip-kind.is-v6 {
  background: color-mix(in oklab, var(--sb-warning) 14%, transparent);
  color: var(--sb-warning);
}
.dlg-ip-addr {
  min-width: 140px;
  color: var(--sb-text);
  font-size: 12.5px;
  word-break: break-all;
}
.dlg-ip-alias { flex: 1; }
.dlg-empty {
  padding: 14px;
  border-radius: 8px;
  border: 1px dashed var(--sb-line);
  background: var(--sb-surface-2);
  color: var(--sb-muted);
  font-size: 12px;
  text-align: center;
}
.dlg-foot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  width: 100%;
}
@media (max-width: 700px) {
  .dlg-grid, .dlg-facts { grid-template-columns: minmax(0, 1fr); }
  .dlg-facts > div + div { border-left: none; border-top: 1px solid rgba(148, 163, 184, .1); }
  .dlg-foot-hint { display: none; }
}

:global(.el-popper) {
  max-width: min(420px, 90vw) !important;
  white-space: pre-line !important;
}

/* ── 线路与丢包排查弹窗 ───────────────────────────────── */
:global(.el-overlay:has(.el-dialog.probe-dialog) .el-overlay-dialog),
:global(.el-overlay-dialog:has(.el-dialog.probe-dialog)) {
  display: flex !important;
  align-items: center !important;
  justify-content: center !important;
}

:global(.el-dialog.probe-dialog) {
  margin: auto !important;
  max-width: calc(100vw - 32px);
  border-radius: 16px;
  background: var(--sb-surface);
  border: 1px solid var(--sb-line-strong);
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.45);
  overflow: hidden;
}
:global(.el-dialog.probe-dialog .el-dialog__header) {
  padding: 18px 24px 14px;
  border-bottom: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
}
:global(.el-dialog.probe-dialog .el-dialog__body) {
  padding: 16px 24px 24px;
}
.probe-hero-title {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 17px;
  font-weight: 700;
  color: var(--sb-text);
}
.probe-hero-node {
  color: var(--sb-accent);
}
.probe-ip-tag {
  font-size: 11px;
  padding: 1px 7px;
  border-radius: 6px;
  font-weight: 600;
}
.probe-ip-tag.v4 { background: rgba(52, 211, 153, .16); color: #34d399; }
.probe-ip-tag.v6 { background: rgba(168, 85, 247, .16); color: #c084fc; }
.probe-hero-sub {
  margin-top: 4px;
  font-size: 12px;
  color: var(--sb-muted);
}
.probe-client-panel, .probe-mesh-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 6px;
}
.probe-route-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 18px;
  border-radius: 12px;
  background: rgba(15, 23, 42, 0.4);
  border: 1px solid var(--sb-line);
  gap: 12px;
}
.probe-route-endpoint {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
}
.probe-route-endpoint.is-target {
  justify-content: flex-end;
  text-align: right;
}
.ep-badge {
  font-size: 11px;
  font-weight: 700;
  padding: 3px 8px;
  border-radius: 6px;
  background: var(--sb-surface-3);
  color: var(--sb-text-2);
  flex-shrink: 0;
}
.is-source .ep-badge { background: rgba(56, 189, 248, .15); color: #38bdf8; }
.is-target .ep-badge { background: rgba(168, 85, 247, .15); color: #c084fc; }
.ep-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--sb-text);
}
.ep-ip {
  font-size: 12px;
  color: var(--sb-muted);
}
.probe-route-connector {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.connector-line {
  width: 24px;
  height: 1px;
  background: var(--sb-line-strong);
}
.connector-label {
  font-size: 11px;
  color: var(--sb-muted);
  padding: 2px 8px;
  border-radius: 999px;
  background: var(--sb-surface-3);
}
.probe-control-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  padding: 12px 16px;
  border-radius: 10px;
  background: var(--sb-surface-2);
}
.ctrl-label {
  font-size: 12px;
  color: var(--sb-muted);
  margin-right: 6px;
}
.probe-target-ip-wrap, .probe-proto-indicator {
  display: flex;
  align-items: center;
}
.probe-loading-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 36px 16px;
  background: var(--sb-surface-2);
  border: 1px dashed var(--sb-line-strong);
  border-radius: 12px;
  gap: 12px;
}
.probe-spin-icon {
  font-size: 28px;
  color: var(--sb-accent);
}
.loading-text {
  font-size: 13px;
  color: var(--sb-text-2);
}
.probe-firewall-notice {
  font-size: 12px;
  line-height: 1.6;
  padding: 14px 16px;
  border-radius: 10px;
  background: rgba(245, 158, 11, 0.08);
  border: 1px solid rgba(245, 158, 11, 0.25);
}
.notice-title {
  font-weight: 600;
  color: var(--sb-warning);
  margin-bottom: 4px;
}
.notice-desc {
  color: var(--sb-text-2);
  font-size: 12px;
}
.probe-stats-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.probe-stat-box {
  padding: 14px 12px;
  border-radius: 10px;
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
  text-align: center;
}
.probe-stat-box.is-loss .stat-num {
  color: var(--sb-danger);
}
.probe-stat-box .stat-num {
  font-size: 20px;
  font-weight: 700;
  color: var(--sb-success);
}
.probe-stat-box .unit {
  font-size: 12px;
  font-weight: normal;
  margin-left: 2px;
  color: var(--sb-muted);
}
.probe-stat-box .stat-name {
  margin-top: 4px;
  font-size: 11.5px;
  color: var(--sb-muted);
}
.probe-pulses-row {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
  font-size: 12px;
}
.pulses-title {
  color: var(--sb-muted);
  font-size: 12px;
}
.pulses-list {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.pulse-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  border-radius: 6px;
  font-family: monospace;
  font-size: 11px;
}
.pulse-pill.is-ok {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
}
.pulse-pill.is-fail {
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}
.pulse-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}

.mesh-topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.mesh-hint {
  font-size: 12px;
  color: var(--sb-muted);
}
.mesh-empty {
  padding: 40px;
  text-align: center;
  color: var(--sb-muted);
  font-size: 13px;
  border: 1px dashed var(--sb-line);
  border-radius: 10px;
}
.mesh-table-wrap {
  border: 1px solid var(--sb-line);
  border-radius: 10px;
  overflow: hidden;
}
.mesh-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
}
.mesh-table th {
  background: var(--sb-surface-2);
  color: var(--sb-muted);
  font-weight: 600;
  text-align: left;
  padding: 10px 14px;
  border-bottom: 1px solid var(--sb-line);
}
.mesh-table td {
  padding: 10px 14px;
  border-bottom: 1px solid var(--sb-line);
}
.mesh-table tr:last-child td {
  border-bottom: none;
}
.peer-name {
  font-weight: 600;
  color: var(--sb-text);
}
.peer-loc {
  font-size: 12px;
  color: var(--sb-muted);
}
.peer-probe-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.cell-tag {
  font-size: 10px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 4px;
}
.cell-tag.v4 { background: rgba(52, 211, 153, 0.15); color: #34d399; }
.cell-tag.v6 { background: rgba(168, 85, 247, 0.15); color: #c084fc; }
.cell-method {
  font-size: 10px;
  font-family: monospace;
  background: var(--sb-surface-3);
  padding: 1px 4px;
  border-radius: 3px;
  color: var(--sb-muted);
}
.cell-details {
  font-size: 11px;
  color: var(--sb-muted);
  font-weight: normal;
  margin-left: 2px;
}

.cell-val.is-ok { color: var(--sb-success); }
.cell-val.is-loss { color: var(--sb-warning); font-weight: 600; }
.cell-val.is-fail { color: var(--sb-danger); }
.cell-val.is-dim { color: var(--sb-muted); font-size: 11.5px; }
</style>
