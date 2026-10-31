<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Aim,
  Clock,
  Connection,
  CopyDocument,
  Cpu,
  Delete,
  Edit,
  Grid,
  InfoFilled,
  Loading,
  Management,
  Monitor,
  MoreFilled,
  Plus,
  Refresh,
  RemoveFilled,
  Search,
  Tickets,
  Top,
} from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { formatBytes, formatDateTime, includesText } from '../format'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const MASTER_HOST_KEY = 'polaris_master_host'
const VIEW_MODE_KEY = 'polaris_server_view_mode'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const canWrite = inject('canWrite')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const refreshing = ref(false)
const pending = ref([])
const metrics = ref({})
const speedtests = ref({})
const speedtesting = reactive({})

// 视图模式：cards (默认精美卡片) 或 table (传统表格)
const viewMode = ref(window.localStorage.getItem(VIEW_MODE_KEY) || 'cards')
function switchViewMode(mode) {
  viewMode.value = mode
  window.localStorage.setItem(VIEW_MODE_KEY, mode)
}

const keyword = ref('')
const statusFilter = ref('') // '' | 'online' | 'offline' | 'blocked'
const sortBy = ref('name')

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
  expires_on: '', price: '', price_period: '', currency: 'USD', provider: '', provider_url: '', traffic_plan: '', bandwidth: '',
})
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
const syncingDDNS = ref(false)
const editSaving = ref(false)

const mtrDialogVisible = ref(false)
const mtrTargetNode = ref(null)
const mtrLoading = ref(false)
const mtrTargetType = ref('')
const mtrCustomTarget = ref('')
const mtrCount = ref(10)
const mtrReport = ref(null)
const mtrShowRaw = ref(false)
const mtrTargetOptions = ref([])

async function loadMTRTargets(nodeId) {
  try {
    const url = nodeId ? `/diagnose/mtr/targets?node_id=${nodeId}` : '/diagnose/mtr/targets'
    const res = await api(url)
    if (res?.targets?.length) {
      mtrTargetOptions.value = res.targets
      if (!mtrTargetType.value || !res.targets.some((t) => t.value === mtrTargetType.value)) {
        mtrTargetType.value = res.targets[0]?.value || 'custom'
      }
    }
  } catch (_) {
    // Keep empty or user custom
  }
}

// 架构 -> 官方最新 sing-box 版本号
const singBoxLatest = ref({})
let stopLive
let stopConnections
let lastSingBoxCheckTime = 0

// 卡片分页
const currentPage = ref(1)
const pageSize = ref(12)

const filteredNodes = computed(() => {
  let list = appState.nodes.filter((node) => {
    if (statusFilter.value === 'online' && !node.online) return false
    if (statusFilter.value === 'offline' && node.online) return false
    if (statusFilter.value === 'blocked' && !isNodeGFWBlocked(speedtests.value[node.id])) return false
    return includesText(
      [node.name, node.client_address, node.os, node.architecture, node.agent_version, node.sing_box_version],
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

const pagedNodes = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return filteredNodes.value.slice(start, start + pageSize.value)
})

watch([keyword, statusFilter, sortBy], () => {
  currentPage.value = 1
})

const filteredPending = computed(() =>
  pending.value.filter((row) => includesText([row.node_name, row.capabilities], keyword.value)),
)

// 实时快照
const live = computed(() => connectionSnapshots.value)

// 状态筛选计数
const stats = computed(() => {
  const total = appState.nodes.length
  const online = appState.nodes.filter((n) => n.online).length
  let gfwBlockedCount = 0
  for (const node of appState.nodes) if (isNodeGFWBlocked(speedtests.value[node.id])) gfwBlockedCount++
  return {
    total,
    online,
    offline: total - online,
    gfwBlockedCount,
  }
})

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    const [, registrations, metricResult, speedtestResult] = await Promise.all([
      loadNodes(),
      isAdmin.value ? api('/registrations').catch(() => ({ registrations: [] })) : Promise.resolve({ registrations: [] }),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
      api('/nodes/speedtests/latest').catch(() => ({ speedtests: [] })),
    ])
    pending.value = registrations.registrations || []
    metrics.value = Object.fromEntries((metricResult.nodes || []).map((entry) => [entry.node_id, entry.report]))
    speedtests.value = Object.fromEntries((speedtestResult.speedtests || []).map((s) => [s.node_id, s]))
  } finally {
    loading.value = false
    refreshing.value = false
  }
  loadSingBoxLatest().catch(() => {})
}

async function runSpeedtest(node) {
  speedtesting[node.id] = true
  try {
    const res = await post(`/nodes/${node.id}/speedtest`, {})
    if (res?.result) {
      speedtests.value = { ...speedtests.value, [node.id]: res.result }
    }
    ElMessage.success(`“${node.name}”三网延迟与线路检测完成`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '线路检测执行失败')
  } finally {
    speedtesting[node.id] = false
  }
}

function getPingStr(val) {
  if (val == null || val <= 0) return '超时'
  return `${val}ms`
}

function getPingClass(val) {
  if (val == null || val <= 0) return 'ping-status-timeout'
  if (val < 90) return 'ping-status-fast'
  if (val < 200) return 'ping-status-medium'
  return 'ping-status-slow'
}

function isNodeGFWBlocked(st) {
  if (!st) return false
  return (
    Boolean(st.gfw_blocked) ||
    st.gfw_status === 'blocked'
  )
}

function formatNodeLatencies(raw) {
  if (!raw) return ''
  return raw
    .split('|')
    .map(s => s.trim())
    .filter(Boolean)
    .map(s => `• ${s.replace(/电信|联通|移动/g, '')}`)
    .join('<br/>')
}

function getNodeLocationLabel(node) {
  if (!node) return '本地节点'
  if (node.city) return node.city
  if (node.location) return node.location

  const name = (node.name || '').trim()
  if (name && !name.includes('http') && name.length <= 20) {
    return name
  }
  return '本地节点'
}

function getCarrierFallbackTrace(route, isp, node) {
  const origin = getNodeLocationLabel(node)
  const targetMap = { telecom: '电信网络', unicom: '联通网络', mobile: '移动网络' }
  const target = targetMap[isp] || '目标网络'
  return `${origin} ➔ 骨干直连 (骨干跃点未回显) ➔ ${target}`
}

function getInboundFallbackTrace(route, isp, node) {
  const target = getNodeLocationLabel(node)
  const srcMap = { telecom: '电信探针', unicom: '联通探针', mobile: '移动探针' }
  const src = srcMap[isp] || '分布式探针'
  return `${src} ➔ 骨干网 ➔ ${target}`
}

function cleanTracePath(trace, node) {
  if (!trace) return ''
  const loc = getNodeLocationLabel(node)
  let t = trace
  if (t.startsWith('节点 ➔') || t.startsWith('本地节点 ➔') || t.startsWith('服务器节点 ➔')) {
    t = t.replace(/^(节点|本地节点|服务器节点)\s*➔\s*/, `${loc} ➔ `)
  } else if (!t.includes(loc) && !t.includes('探针')) {
    t = `${loc} ➔ ${t}`
  }
  t = t.replace(/➔\s*(目标节点|服务器节点|本地节点|目标机房)$/, `➔ ${loc}`)

  // Remove any legacy erroneous AS174 string if present
  t = t.replace(/AS174 \(Cogent·美西洛杉矶\)\s*➔\s*/g, '')

  // Clean duplicate consecutive hops
  const parts = t.split('➔').map(s => s.trim()).filter(Boolean)
  const unique = []
  for (let i = 0; i < parts.length; i++) {
    if (unique.length > 0 && unique[unique.length - 1] === parts[i]) {
      continue
    }
    unique.push(parts[i])
  }
  return unique.join(' ➔ ')
}

function getInboundCarrierTooltip(st, isp, node) {
  if (!st) return '暂无探测数据'
  let lats = ''
  let trace = ''
  let ping = 0
  if (isp === 'telecom') {
    lats = st.inbound_telecom_node_latencies
    trace = st.inbound_telecom_trace_path || getInboundFallbackTrace(st.inbound_telecom_route, 'telecom', node)
    ping = st.inbound_telecom_latency_ms
  } else if (isp === 'unicom') {
    lats = st.inbound_unicom_node_latencies
    trace = st.inbound_unicom_trace_path || getInboundFallbackTrace(st.inbound_unicom_route, 'unicom', node)
    ping = st.inbound_unicom_latency_ms
  } else if (isp === 'mobile') {
    lats = st.inbound_mobile_node_latencies
    trace = st.inbound_mobile_trace_path || getInboundFallbackTrace(st.inbound_mobile_route, 'mobile', node)
    ping = st.inbound_mobile_latency_ms
  }

  const sections = []
  const formattedLats = formatNodeLatencies(lats)
  if (formattedLats) {
    sections.push(`<b>【去程节点延迟】</b><br/>${formattedLats}`)
  } else if (ping > 0) {
    sections.push(`<b>【去程延迟】</b><br/>• 探针实测: ${ping}ms`)
  }

  if (trace) {
    sections.push(`<b>【去程线路轨迹】</b><br/>${cleanTracePath(trace, node)}`)
  }
  return sections.join('<br/><br/>')
}

function getISPTooltip(st, isp, node) {
  if (!st) return '暂无测速数据'
  let lats = ''
  let trace = ''
  let ping = 0
  if (isp === 'telecom') {
    lats = st.telecom_node_latencies
    trace = st.telecom_trace_path || getCarrierFallbackTrace(st.telecom_route, 'telecom', node)
    ping = st.telecom_latency_ms ?? st.telecom_ping_ms
  } else if (isp === 'unicom') {
    lats = st.unicom_node_latencies
    trace = st.unicom_trace_path || getCarrierFallbackTrace(st.unicom_route, 'unicom', node)
    ping = st.unicom_latency_ms ?? st.unicom_ping_ms
  } else if (isp === 'mobile') {
    lats = st.mobile_node_latencies
    trace = st.mobile_trace_path || getCarrierFallbackTrace(st.mobile_route, 'mobile', node)
    ping = st.mobile_latency_ms ?? st.mobile_ping_ms
  }

  const sections = []
  const formattedLats = formatNodeLatencies(lats)
  if (formattedLats) {
    sections.push(`<b>【返程节点延迟】</b><br/>${formattedLats}`)
  } else if (ping > 0) {
    sections.push(`<b>【返程延迟】</b><br/>• 实测延迟: ${ping}ms`)
  }

  if (trace) {
    sections.push(`<b>【返程线路轨迹】</b><br/>${cleanTracePath(trace, node)}`)
  }
  return sections.join('<br/><br/>')
}

function getInboundSummary(st) {
  if (!st) return '待探测'
  if (isNodeGFWBlocked(st)) return '探针全超时 · 端口阻断'
  if (st.domestic_latency_ms) {
    return `探针畅通 · 均值 ${st.domestic_latency_ms}ms`
  }
  return '探针畅通'
}

function getInboundProbeTooltip(st) {
  if (!st) return ''
  if (isNodeGFWBlocked(st)) {
    return '🚨 <b>端口阻断</b><br/>探针发起的 TCP 端口握手全部超时丢弃'
  }
  return `网络通畅<br/>探针 TCP 延迟: ${st.domestic_latency_ms ? st.domestic_latency_ms + 'ms' : '畅通'}`
}

function formatRouteSimple(route) {
  if (!route) return '待测'
  if (route.includes('CN2 GIA')) return 'CN2 GIA'
  if (route.includes('CN2 GT')) return 'CN2 GT'
  if (route.includes('9929')) return '9929'
  if (route.includes('4837')) return '4837'
  if (route.includes('CMIN2')) return 'CMIN2'
  if (route.includes('CMI')) return 'CMI'
  if (route.includes('CTGNet')) return 'CTGNet'
  if (route.includes('163')) return '163'
  if (route.includes('BGP')) return 'BGP'
  if (route.includes('GFW')) return 'GFW 阻断'
  if (route.includes('超时')) return '超时'
  // 若包含旧的“电信普通直连”等历史字段，映射为骨干网
  if (route.includes('电信')) return '163'
  if (route.includes('联通')) return '4837'
  if (route.includes('移动')) return 'CMI'
  const clean = route.replace(/骨干|普通/g, '').trim()
  return clean || '163'
}

function formatCarrierDisplay(route, latencyMs) {
  const rName = formatRouteSimple(route)
  if (rName === 'GFW 阻断') return '阻断'
  if (rName === '超时') return '超时'
  if (latencyMs != null && latencyMs <= 0 && (!rName || rName === '待测')) {
    return '超时'
  }
  return rName || '待测'
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
  if (percent >= 85) return '#f43f5e' // 红色警告
  if (percent >= 65) return '#f59e0b' // 琥珀黄中载
  return '#10b981' // 翠绿正常
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
    provider: node.provider || '', provider_url: node.provider_url || '', traffic_plan: node.traffic_plan || '', bandwidth: node.bandwidth || '',
  }
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
      provider: editForm.value.provider, provider_url: editForm.value.provider_url, traffic_plan: editForm.value.traffic_plan, bandwidth: editForm.value.bandwidth,
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

async function openMTRDialog(node) {
  mtrTargetNode.value = node
  mtrReport.value = null
  mtrShowRaw.value = false
  mtrDialogVisible.value = true
  await loadMTRTargets(node?.id)
}

async function startMTRDiagnose() {
  if (!mtrTargetNode.value) return
  let target = ''
  let label = ''
  if (mtrTargetType.value === 'custom') {
    target = mtrCustomTarget.value.trim()
    if (!target) {
      ElMessage.warning('请输入自定义目标 IP 或域名')
      return
    }
    label = target
  } else {
    const opt = mtrTargetOptions.value.find((o) => o.value === mtrTargetType.value)
    if (!opt || !opt.ip) {
      ElMessage.warning('请选择有效的诊断目标')
      return
    }
    target = opt.ip
    label = opt.label || target
  }

  mtrLoading.value = true
  mtrReport.value = null
  try {
    const res = await post(`/nodes/${mtrTargetNode.value.id}/diagnose/mtr`, {
      target,
      label,
      count: Number(mtrCount.value) || 10,
    })
    const data = res?.report || res?.result || res
    mtrReport.value = data
    if (!data?.hops || data.hops.length === 0) {
      mtrShowRaw.value = true
    }
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '链路诊断失败，请检查节点网络连接')
  } finally {
    mtrLoading.value = false
  }
}

function getLossColor(loss) {
  if (!loss || loss <= 0) return 'var(--sb-success, #10b981)'
  if (loss < 10) return '#f59e0b'
  if (loss < 50) return '#ea580c'
  return 'var(--sb-danger, #ef4444)'
}

function handleCardCommand(node, command) {
  if (command === 'upgradeAgent') upgradeAgent(node)
  else if (command === 'upgradeSingBox') upgradeSingBox(node)
  else if (command === 'revoke') revoke(node)
  else if (command === 'mtr') openMTRDialog(node)
  else if (command === 'syncDDNS') triggerManualDDNS(node)
}

onMounted(() => {
  load()
  loadMTRTargets().catch(() => {})
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
      <el-button :icon="Refresh" :loading="refreshing" @click="load">刷新</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="generateToken">接入服务器</el-button>
    </PageHeader>

    <main v-loading="loading" class="page-content page-content--tight">
      <div class="nodes-toolbar">
        <div class="toolbar-left">
          <el-input
            v-model="keyword"
            clearable
            :prefix-icon="Search"
            placeholder="搜索节点名称、IP地址、系统或架构..."
            style="width: 280px"
          />

          <!-- 快速状态分段筛选胶囊 -->
          <div class="status-segmented">
            <button
              class="seg-btn"
              :class="{ active: statusFilter === '' }"
              @click="statusFilter = ''"
            >
              全部 ({{ stats.total }})
            </button>
            <button
              class="seg-btn"
              :class="{ active: statusFilter === 'online' }"
              @click="statusFilter = 'online'"
            >
              在线 ({{ stats.online }})
            </button>
            <button
              v-if="stats.offline > 0"
              class="seg-btn"
              :class="{ active: statusFilter === 'offline' }"
              @click="statusFilter = 'offline'"
            >
              离线 ({{ stats.offline }})
            </button>
            <button
              v-if="stats.gfwBlockedCount > 0"
              class="seg-btn seg-blocked"
              :class="{ active: statusFilter === 'blocked' }"
              @click="statusFilter = 'blocked'"
            >
              🚨 被墙 ({{ stats.gfwBlockedCount }})
            </button>
          </div>

          <el-select v-model="sortBy" placeholder="排序方式" style="width: 140px">
            <el-option label="服务器名称 (默认)" value="name" />
            <el-option label="活跃时间优先" value="last_seen" />
            <el-option label="连接数最高" value="conn" />
            <el-option label="CPU 负载最高" value="cpu" />
            <el-option label="内存占用最高" value="mem" />
          </el-select>
        </div>

        <div class="toolbar-right">
          <!-- 视图模式切换 -->
          <div class="view-mode-toggle">
            <button
              class="toggle-btn"
              :class="{ active: viewMode === 'cards' }"
              title="卡片视图"
              @click="switchViewMode('cards')"
            >
              <el-icon><Grid /></el-icon>
              <span>卡片</span>
            </button>
            <button
              class="toggle-btn"
              :class="{ active: viewMode === 'table' }"
              title="表格视图"
              @click="switchViewMode('table')"
            >
              <el-icon><Tickets /></el-icon>
              <span>列表</span>
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
          <div v-for="item in filteredPending" :key="item.id" class="pending-card">
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

      <!-- 4. 视图 1：顶级暗黑工业美学卡片 (Card View) -->
      <template v-if="viewMode === 'cards'">
        <div v-if="filteredNodes.length === 0" class="empty-cards-box">
          <el-empty description="未找到符合条件的服务器" />
        </div>

        <div v-else class="node-cards-grid">
          <div
            v-for="node in pagedNodes"
            :key="node.id"
            class="node-card"
            :class="{
              'is-offline': !node.online,
              'has-gfw-block': isNodeGFWBlocked(speedtests[node.id]),
            }"
          >
            <!-- 顶边高光线 -->
            <div
              class="node-card-glow"
              :class="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? 'glow-blocked' : 'glow-online') : 'glow-offline'"
            />

            <!-- 1. 头部：标题、国旗、IP 与快捷操作 -->
            <div class="card-header-clean">
              <div class="header-main">
                <div class="title-row">
                  <span v-if="getNodeFlag(node.name)" class="flag-icon">{{ getNodeFlag(node.name) }}</span>
                  <span class="server-title" :title="node.name">{{ node.name }}</span>
                  <span
                    class="status-indicator-dot"
                    :class="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? 'blocked' : 'online') : 'offline'"
                    :title="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? '探针全超时 (端口阻断)' : '在线运行中') : '节点已离线'"
                  />
                  <span v-if="isNodeGFWBlocked(speedtests[node.id])" class="pill-gfw-warn">
                    端口阻断
                  </span>
                </div>
                <div class="subtitle-row">
                  <span class="sub-os-arch">{{ node.os || 'linux' }}/{{ node.architecture || 'amd64' }}</span>
                  <span class="sub-ver">v{{ node.agent_version || '—' }}</span>
                  <span v-if="node.city || node.location" class="sub-os-arch" :title="node.location || node.city">
                    {{ node.city || node.location }}
                  </span>
                  <span v-if="node.client_address" class="sub-ip font-mono" :title="'点击复制: ' + node.client_address" @click="copyAddress(node.client_address)">
                    {{ node.client_address }}
                    <el-icon class="copy-icon"><CopyDocument /></el-icon>
                  </span>
                  <el-tooltip
                    v-if="node.ddns_enabled"
                    raw-content
                    :content="`DDNS 域名: ${(node.ddns_domains && node.ddns_domains.length ? node.ddns_domains : [node.ddns_domain || node.client_address]).join('、')}<br/>当前解析 IP: ${node.last_observed_ip || '同步中'}`"
                    placement="top"
                  >
                    <span class="chip-ddns">
                      DDNS: {{ node.last_observed_ip || '同步中' }}
                    </span>
                  </el-tooltip>
                  <el-tooltip
                    v-if="node.ip_addresses && node.ip_addresses.length"
                    raw-content
                    :content="`本机网卡检测到的出口 IP (${node.ip_addresses.length} 个):<br/>${node.ip_addresses.map((ip) => ip + (node.ip_aliases && node.ip_aliases[ip] ? ' (' + node.ip_aliases[ip] + ')' : '')).join('<br/>')}`"
                    placement="top"
                  >
                    <span class="chip-ddns" style="cursor: default">
                      {{ node.ip_addresses.length }} 个出口 IP
                    </span>
                  </el-tooltip>
                </div>
                <div v-if="node.expires_on || node.provider || node.price_period || node.traffic_plan || node.bandwidth" class="hosting-meta">
                  <a v-if="node.provider && node.provider_url" :href="node.provider_url" target="_blank" rel="noopener noreferrer" class="hosting-provider">{{ node.provider }}</a>
                  <span v-else-if="node.provider" class="hosting-provider">{{ node.provider }}</span>
                  <span v-if="node.expires_on" class="sub-os-arch">到期 {{ node.expires_on }}</span>
                  <span v-if="node.price_period" class="sub-os-arch">{{ node.price_period === 'free' ? '免费' : `${node.currency || 'USD'} ${node.price}/${({day:'天',month:'月',quarter:'季',year:'年'})[node.price_period] || node.price_period}` }}</span>
                  <span v-if="node.traffic_plan" class="sub-os-arch">流量 {{ node.traffic_plan }}</span>
                  <span v-if="node.bandwidth" class="sub-os-arch">带宽 {{ node.bandwidth }}</span>
                </div>
              </div>

              <div class="header-actions">
                <button
                  v-if="canWrite && node.online"
                  class="btn-speedtest-act"
                  :disabled="Boolean(speedtesting[node.id])"
                  title="测速"
                  @click="runSpeedtest(node)"
                >
                  <el-icon :class="{ 'is-loading': speedtesting[node.id] }"><Connection /></el-icon>
                  <span>{{ speedtesting[node.id] ? '测速中' : '测速' }}</span>
                </button>
                <el-dropdown trigger="click" @command="handleCardCommand(node, $event)">
                  <button class="icon-more-btn" title="更多操作">
                    <el-icon><MoreFilled /></el-icon>
                  </button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item v-if="canWrite && node.online" command="mtr">
                        <el-icon><Aim /></el-icon>逐跳丢包排障 (MTR)
                      </el-dropdown-item>
                      <el-dropdown-item v-if="canWrite && node.ddns_enabled" command="syncDDNS">
                        <el-icon><Refresh /></el-icon>同步 DDNS
                      </el-dropdown-item>
                      <el-dropdown-item v-if="canWrite" command="edit" @click="openEdit(node)">
                        <el-icon><Edit /></el-icon>配置节点
                      </el-dropdown-item>
                      <el-dropdown-item v-if="isAdmin && agentUpdateAvailable(node)" command="upgradeAgent">
                        <el-icon><Top /></el-icon>升级 Agent
                      </el-dropdown-item>
                      <el-dropdown-item v-if="isAdmin && singBoxUpdateAvailable(node)" command="upgradeSingBox">
                        <el-icon><Top /></el-icon>升级 sing-box
                      </el-dropdown-item>
                      <el-dropdown-item v-if="isAdmin" command="revoke" divided class="danger-item">
                        <el-icon><RemoveFilled /></el-icon>移除服务器
                      </el-dropdown-item>
                    </el-dropdown-menu>
                  </template>
                </el-dropdown>
              </div>
            </div>

            <!-- 2. 硬件遥测与流量聚合 (一体化呈现，无生硬分块) -->
            <div class="card-telemetry-clean">
              <!-- CPU / 内存 / 硬盘 三列紧凑进度 -->
              <div class="hw-grid">
                <el-tooltip :content="getCPUTooltip(node)" placement="top">
                  <div class="hw-item">
                    <div class="hw-head font-mono">
                      <span class="hw-lbl">CPU</span>
                      <span class="hw-val" :style="{ color: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0) }">
                        {{ node.online && metrics[node.id]?.host ? `${metrics[node.id].host.cpu_percent.toFixed(0)}%` : '—' }}
                      </span>
                    </div>
                    <div class="hw-track">
                      <div
                        class="hw-bar"
                        :style="{
                          width: `${node.online && metrics[node.id]?.host ? Math.min(100, metrics[node.id].host.cpu_percent) : 0}%`,
                          backgroundColor: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0)
                        }"
                      />
                    </div>
                  </div>
                </el-tooltip>

                <el-tooltip :content="getMemTooltip(node)" placement="top">
                  <div class="hw-item">
                    <div class="hw-head font-mono">
                      <span class="hw-lbl">内存</span>
                      <span class="hw-val" :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total)) }">
                        {{ node.online && metrics[node.id]?.host?.memory_total ? `${getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total)}%` : '—' }}
                      </span>
                    </div>
                    <div class="hw-track">
                      <div
                        class="hw-bar"
                        :style="{
                          width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total) : 0}%`,
                          backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total))
                        }"
                      />
                    </div>
                  </div>
                </el-tooltip>

                <el-tooltip :content="getDiskTooltip(node)" placement="top">
                  <div class="hw-item">
                    <div class="hw-head font-mono">
                      <span class="hw-lbl">硬盘</span>
                      <span class="hw-val" :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total)) }">
                        {{ node.online && metrics[node.id]?.host?.disk_total ? `${getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total)}%` : '—' }}
                      </span>
                    </div>
                    <div class="hw-track">
                      <div
                        class="hw-bar"
                        :style="{
                          width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total) : 0}%`,
                          backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total))
                        }"
                      />
                    </div>
                  </div>
                </el-tooltip>
              </div>

              <!-- 实时网速、总流量与活跃连接 -->
              <div class="network-stream-row font-mono">
                <div class="rate-group">
                  <span class="rate-down">↓ {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).received_rate, '/s') : '0 B/s' }}</span>
                  <span class="rate-up">↑ {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).sent_rate, '/s') : '0 B/s' }}</span>
                </div>
                <div class="traffic-conn-group">
                  <el-tooltip
                    v-if="metrics[node.id]?.proxy"
                    :content="`累计下行: ${formatBytes(metrics[node.id].proxy.received_bytes)} · 累计上行: ${formatBytes(metrics[node.id].proxy.sent_bytes)}`"
                    placement="top"
                  >
                    <span class="total-stream">总计 {{ formatBytes((metrics[node.id].proxy.received_bytes || 0) + (metrics[node.id].proxy.sent_bytes || 0)) }}</span>
                  </el-tooltip>
                  <span v-else class="total-stream subtle">等待流量</span>
                  <span class="conn-pill" :class="{ 'has-conn': (live.get(node.id)?.connection_count || 0) > 0 }">
                    {{ live.get(node.id)?.connection_count ?? 0 }} 连
                  </span>
                </div>
              </div>
            </div>

            <!-- 3. 网络线路矩阵 (去程与返程完整三网类型) -->
            <div class="route-matrix-box" :class="{ 'is-blocked': isNodeGFWBlocked(speedtests[node.id]) }">
              <!-- 去程 Inbound -->
              <div class="route-line">
                <span class="dir-badge in">去程</span>
                <!-- GFW 阻断提示 (悬停显示探针诊断详情，无需明细按钮) -->
                <el-tooltip v-if="isNodeGFWBlocked(speedtests[node.id])" raw-content :content="getInboundProbeTooltip(speedtests[node.id])" placement="top">
                  <div class="gfw-alert-capsule">
                    <span class="gfw-alert-dot" />
                    <span class="gfw-alert-txt font-mono">端口阻断 · 探针全超时</span>
                  </div>
                </el-tooltip>
                <!-- 去程正常线路 -->
                <div v-else-if="speedtests[node.id]" class="carrier-tags-flow">
                  <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[node.id], 'telecom', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].inbound_telecom_latency_ms)">
                      <span class="dot-carrier ct" />电: {{ formatCarrierDisplay(speedtests[node.id].inbound_telecom_route, speedtests[node.id].inbound_telecom_latency_ms) }}
                    </span>
                  </el-tooltip>
                  <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[node.id], 'unicom', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].inbound_unicom_latency_ms)">
                      <span class="dot-carrier cu" />联: {{ formatCarrierDisplay(speedtests[node.id].inbound_unicom_route, speedtests[node.id].inbound_unicom_latency_ms) }}
                    </span>
                  </el-tooltip>
                  <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[node.id], 'mobile', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].inbound_mobile_latency_ms)">
                      <span class="dot-carrier cm" />移: {{ formatCarrierDisplay(speedtests[node.id].inbound_mobile_route, speedtests[node.id].inbound_mobile_latency_ms) }}
                    </span>
                  </el-tooltip>
                </div>
                <span v-else class="route-empty-hint subtle font-mono">去程待测</span>
              </div>

              <!-- 返程 Outbound -->
              <div class="route-line">
                <span class="dir-badge out">返程</span>
                <div v-if="speedtests[node.id]" class="carrier-tags-flow">
                  <el-tooltip raw-content :content="getISPTooltip(speedtests[node.id], 'telecom', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].telecom_latency_ms)">
                      <span class="dot-carrier ct" />电: {{ formatCarrierDisplay(speedtests[node.id].telecom_route, speedtests[node.id].telecom_latency_ms) }}
                    </span>
                  </el-tooltip>
                  <el-tooltip raw-content :content="getISPTooltip(speedtests[node.id], 'unicom', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].unicom_latency_ms)">
                      <span class="dot-carrier cu" />联: {{ formatCarrierDisplay(speedtests[node.id].unicom_route, speedtests[node.id].unicom_latency_ms) }}
                    </span>
                  </el-tooltip>
                  <el-tooltip raw-content :content="getISPTooltip(speedtests[node.id], 'mobile', node)" placement="top">
                    <span class="carrier-tag font-mono" :class="getPingClass(speedtests[node.id].mobile_latency_ms)">
                      <span class="dot-carrier cm" />移: {{ formatCarrierDisplay(speedtests[node.id].mobile_route, speedtests[node.id].mobile_latency_ms) }}
                    </span>
                  </el-tooltip>
                </div>
                <span v-else class="route-empty-hint subtle font-mono">返程待测</span>
              </div>
            </div>

            <!-- 4. 底栏：活跃时间、运行天数 -->
            <div class="card-footer-clean">
              <div class="footer-time-info font-mono">
                <el-icon :size="12"><Clock /></el-icon>
                <span>{{ formatFriendlyTime(node.last_seen_at) }}</span>
                <span class="time-sep">·</span>
                <span>{{ formatUptimeText(metrics[node.id]?.host?.uptime_seconds) }}</span>
              </div>
            </div>
          </div>
        </div>
      </template>

      <!-- 5. 视图 2：传统列表表格 (Table View) -->
      <template v-else>
        <div class="table-panel">
          <PagedTable :rows="filteredNodes" empty-text="尚未接入任何服务器">
            <el-table-column label="服务器 / 系统" min-width="190" show-overflow-tooltip>
              <template #default="{ row }">
                <div class="cell-main">
                  <span class="status-indicator" :class="row.online ? (isNodeGFWBlocked(speedtests[row.id]) ? 'blocked' : 'online') : 'offline'" />
                  <span v-if="getNodeFlag(row.name)" class="flag-icon" style="margin-left: 6px">{{ getNodeFlag(row.name) }}</span>
                  <strong style="margin-left: 4px">{{ row.name }}</strong>
                  <span v-if="isNodeGFWBlocked(speedtests[row.id])" class="badge-gfw-pill" style="margin-left: 6px">GFW 阻断</span>
                </div>
                <div class="cell-sub font-mono">{{ row.os || '—' }} · {{ row.architecture || '—' }} · v{{ row.agent_version || '—' }}</div>
              </template>
            </el-table-column>

            <el-table-column label="硬件负载" min-width="170">
              <template #default="{ row }">
                <div v-if="row.online && metrics[row.id]?.host" class="table-hw-stack font-mono">
                  <div class="hw-mini-row">
                    <span class="hw-mini-name">CPU:</span>
                    <span class="hw-mini-val" :style="{ color: getUsageStatusColor(metrics[row.id].host.cpu_percent) }">
                      {{ metrics[row.id].host.cpu_percent.toFixed(1) }}%
                    </span>
                  </div>
                  <div class="hw-mini-row">
                    <span class="hw-mini-name">内存:</span>
                    <span class="hw-mini-val">
                      {{ getResourcePercent(metrics[row.id].host.memory_used, metrics[row.id].host.memory_total) }}%
                    </span>
                  </div>
                  <div class="hw-mini-row">
                    <span class="hw-mini-name">磁盘:</span>
                    <span class="hw-mini-val">
                      {{ getResourcePercent(metrics[row.id].host.disk_used, metrics[row.id].host.disk_total) }}%
                    </span>
                  </div>
                </div>
                <span v-else class="subtle font-mono">{{ row.online ? '待上报' : '离线' }}</span>
              </template>
            </el-table-column>

            <el-table-column label="客户端连接地址" min-width="190" show-overflow-tooltip>
              <template #default="{ row }">
                <div v-if="row.client_address" class="mono" style="cursor: pointer" @click="copyAddress(row.client_address)">
                  {{ row.client_address }}
                  <span v-if="row.city || row.location" class="sub-os-arch" style="margin-left: 6px" :title="row.location || row.city">
                    {{ row.city || row.location }}
                  </span>
                  <el-tooltip
                    v-if="row.ddns_enabled"
                    raw-content
                    :content="`DDNS 域名: ${(row.ddns_domains && row.ddns_domains.length ? row.ddns_domains : [row.ddns_domain || row.client_address]).join('、')}<br/>当前解析 IP: ${row.last_observed_ip || '同步中'}`"
                    placement="top"
                  >
                    <span class="chip-ddns" style="margin-left: 4px">
                      DDNS: {{ row.last_observed_ip || '同步中' }}
                    </span>
                  </el-tooltip>
                </div>
                <span v-else class="danger-text">未配置连接地址</span>
              </template>
            </el-table-column>

            <el-table-column label="实时速率" min-width="140">
              <template #default="{ row }">
                <template v-if="live.get(row.id)?.has_rates">
                  <div class="rate-stack font-mono">
                    <span class="rate-item down">↓ {{ formatBytes(live.get(row.id).received_rate, '/s') }}</span>
                    <span class="rate-item up">↑ {{ formatBytes(live.get(row.id).sent_rate, '/s') }}</span>
                  </div>
                </template>
                <span v-else class="subtle font-mono">0 B/s</span>
              </template>
            </el-table-column>

                        <el-table-column label="网络路由 (去程 / 返程)" min-width="280">
              <template #default="{ row }">
                <div v-if="speedtests[row.id]" class="table-net-stack">
                  <!-- 去程行 -->
                  <div class="table-net-row">
                    <span class="table-dir-badge in">去程</span>
                    <div v-if="isNodeGFWBlocked(speedtests[row.id])" class="table-gfw-text font-mono">
                      🚨 端口阻断 (探针超时)
                    </div>
                    <div v-else class="table-isp-pills">
                      <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[row.id], 'telecom', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].inbound_telecom_latency_ms)">
                          电: {{ formatCarrierDisplay(speedtests[row.id].inbound_telecom_route, speedtests[row.id].inbound_telecom_latency_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[row.id], 'unicom', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].inbound_unicom_latency_ms)">
                          联: {{ formatCarrierDisplay(speedtests[row.id].inbound_unicom_route, speedtests[row.id].inbound_unicom_latency_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip raw-content :content="getInboundCarrierTooltip(speedtests[row.id], 'mobile', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].inbound_mobile_latency_ms)">
                          移: {{ formatCarrierDisplay(speedtests[row.id].inbound_mobile_route, speedtests[row.id].inbound_mobile_latency_ms) }}
                        </span>
                      </el-tooltip>
                    </div>
                  </div>
                  <!-- 返程行 -->
                  <div class="table-net-row">
                    <span class="table-dir-badge out">返程</span>
                    <div class="table-isp-pills">
                      <el-tooltip raw-content :content="getISPTooltip(speedtests[row.id], 'telecom', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].telecom_latency_ms)">
                          电: {{ formatCarrierDisplay(speedtests[row.id].telecom_route, speedtests[row.id].telecom_latency_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip raw-content :content="getISPTooltip(speedtests[row.id], 'unicom', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].unicom_latency_ms)">
                          联: {{ formatCarrierDisplay(speedtests[row.id].unicom_route, speedtests[row.id].unicom_latency_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip raw-content :content="getISPTooltip(speedtests[row.id], 'mobile', row)" placement="top">
                        <span class="table-isp-pill font-mono" :class="getPingClass(speedtests[row.id].mobile_latency_ms)">
                          移: {{ formatCarrierDisplay(speedtests[row.id].mobile_route, speedtests[row.id].mobile_latency_ms) }}
                        </span>
                      </el-tooltip>
                    </div>
                  </div>
                </div>
                <div v-else class="table-untested font-mono">
                  <span class="subtle">未测速</span>
                  <button
                    v-if="canWrite && row.online"
                    class="btn-probe-quick"
                    :disabled="Boolean(speedtesting[row.id])"
                    @click="runSpeedtest(row)"
                  >
                    测速
                  </button>
                </div>
              </template>
            </el-table-column>

            <el-table-column label="活跃时间" min-width="140">
              <template #default="{ row }">
                <span class="mono" :title="formatDateTime(row.last_seen_at)">
                  {{ formatFriendlyTime(row.last_seen_at) }}
                </span>
              </template>
            </el-table-column>

            <el-table-column label="操作" width="220" fixed="right">
              <template #default="{ row }">
                <el-button v-if="canWrite && row.online" link type="primary" size="small" @click="runSpeedtest(row)">
                  测速
                </el-button>
                <el-button v-if="canWrite && row.online" link type="primary" size="small" @click="openMTRDialog(row)">
                  排障
                </el-button>
                <el-button v-if="canWrite && row.ddns_enabled" link type="primary" size="small" :loading="syncingDDNS" @click="triggerManualDDNS(row)">
                  DDNS
                </el-button>
                <el-button v-if="canWrite" link type="primary" size="small" @click="openEdit(row)">
                  配置
                </el-button>
                <el-button v-if="isAdmin" link type="danger" size="small" @click="revoke(row)">
                  移除
                </el-button>
              </template>
            </el-table-column>
          </PagedTable>
        </div>
      </template>
    </main>

    <!-- 添加节点 Token 弹窗 -->
    <el-dialog v-model="tokenDialog" title="接入新服务器" width="560px">
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
        <el-button type="primary" :icon="CopyDocument" @click="copyInstallCommand">复制命令</el-button>
      </div>
    </el-dialog>

    <!-- 配置服务器弹窗 -->
    <el-dialog v-model="editDialog" title="配置服务器" width="520px">
      <el-form label-position="top">
        <el-form-item label="服务器名称">
          <el-input v-model="editForm.name" placeholder="请输入服务器名称" />
        </el-form-item>
        <el-form-item label="客户端连接域名或 IP">
          <el-input v-model="editForm.client_address" placeholder="例如：hk.example.com 或 1.2.3.4" />
          <div v-if="editNodeAllIPs.length" style="margin-top: 6px; display: flex; flex-wrap: wrap; gap: 6px; align-items: center">
            <span class="subtle" style="font-size: 12px">本机出口 IP：</span>
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
        </el-form-item>

        <el-divider content-position="left">套餐与到期信息</el-divider>
        <el-form-item label="到期日期"><el-date-picker v-model="editForm.expires_on" type="date" value-format="YYYY-MM-DD" placeholder="选择到期日期" clearable style="width:100%" /></el-form-item>
        <el-form-item label="价格">
          <div style="display:flex;gap:8px;width:100%">
            <el-input v-model="editForm.price" :disabled="editForm.price_period === 'free'" placeholder="价格" />
            <el-select v-model="editForm.currency" style="width:115px"><el-option label="美元 USD" value="USD"/><el-option label="人民币 CNY" value="CNY"/><el-option label="欧元 EUR" value="EUR"/><el-option label="加元 CAD" value="CAD"/></el-select>
          </div>
          <el-select v-model="editForm.price_period" placeholder="计价周期" style="width:100%;margin-top:8px"><el-option label="免费" value="free"/><el-option label="按天" value="day"/><el-option label="按月" value="month"/><el-option label="按季" value="quarter"/><el-option label="按年" value="year"/></el-select>
        </el-form-item>
        <el-form-item label="厂商"><el-input v-model="editForm.provider" placeholder="例如：DigitalOcean" /></el-form-item>
        <el-form-item label="厂商访问 URL"><el-input v-model="editForm.provider_url" placeholder="https://..." /></el-form-item>
        <el-form-item label="流量套餐"><el-input v-model="editForm.traffic_plan" placeholder="例如：每月 2 TB" /></el-form-item>
        <el-form-item label="带宽大小"><el-input v-model="editForm.bandwidth" placeholder="例如：1 Gbps" /></el-form-item>

        <el-form-item label="本机出口 IP 与别名设置">
          <div class="subtle" style="font-size: 12px; margin-bottom: 8px">
            自动从服务器网卡获取或手动配置的出口 IP 地址。在此为出口 IP 设置别名，配置用户出口与节点分流时将优先显示该别名。
          </div>
          <div v-if="editNodeAllIPs.length" style="display: flex; flex-direction: column; gap: 8px; width: 100%">
            <div
              v-for="ip in editNodeAllIPs"
              :key="ip"
              style="display: flex; align-items: center; gap: 10px; background: var(--bg-subtle, rgba(0,0,0,0.03)); padding: 6px 10px; border-radius: 6px; border: 1px solid var(--border-color, #e4e7ed);"
            >
              <el-tag size="small" :type="ip.includes(':') ? 'warning' : 'success'" style="font-family: monospace; font-weight: bold">
                {{ ip.includes(':') ? 'IPv6' : 'IPv4' }}
              </el-tag>
              <span style="font-family: monospace; font-size: 13px; min-width: 130px; word-break: break-all;">
                {{ ip }}
              </span>
              <el-input
                v-model="editForm.ip_aliases[ip]"
                size="small"
                placeholder="出口别名（例如：香港原生、电信出口）"
                style="flex: 1"
                clearable
              />
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
          <div v-else class="subtle" style="font-size: 12px; margin-bottom: 8px">
            暂未检测到出口 IP。您可手动输入本服务器的出口 IP 添加管理。
          </div>
          <div style="display: flex; gap: 8px; width: 100%; margin-top: 8px">
            <el-input
              v-model="newCustomEgressIP"
              size="small"
              placeholder="手动添加出口 IP（如：198.51.100.2 或 2001:db8::1）"
              clearable
              @keydown.enter.prevent="addCustomEgressIP"
            />
            <el-button size="small" :icon="Plus" @click="addCustomEgressIP">添加出口 IP</el-button>
          </div>
        </el-form-item>

        <el-form-item label="Cloudflare 动态域名解析 (DDNS)">
          <div class="setting-edit-box">
            <div class="setting-edit-text">
              <div class="setting-edit-heading">出口 IP 变动自动同步解析</div>
              <div class="subtle">当服务器公网 IP 发生变更时，自动更新绑定的 Cloudflare A / AAAA 记录。</div>
            </div>
            <el-switch
              v-model="editForm.ddns_enabled"
              size="small"
            />
          </div>
          <div v-if="editForm.ddns_enabled" style="margin-top: 10px; width: 100%">
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
              <el-option
                v-for="rec in cfRecords"
                :key="rec.id"
                :label="`${rec.name} (${rec.content})`"
                :value="rec.id"
              />
            </el-select>
            <div v-if="(editForm.ddns_domains || []).length" style="margin-top: 8px; display: flex; flex-wrap: wrap; gap: 4px">
              <el-tag v-for="d in editForm.ddns_domains" :key="d" size="small" type="info">
                {{ d }}
              </el-tag>
            </div>
          </div>
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="editDialog = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" @click="saveEdit">保存配置</el-button>
      </template>
    </el-dialog>

    <!-- 逐跳丢包排障 (MTR) 弹窗 -->
    <el-dialog
      v-model="mtrDialogVisible"
      :title="`链路逐跳丢包排障 - ${mtrTargetNode?.name || ''}`"
      width="840px"
      class="mtr-dialog"
      destroy-on-close
    >
      <div class="mtr-dialog-body">
        <!-- 控制表单栏 -->
        <div class="mtr-ctrl-bar">
          <div class="mtr-ctrl-item">
            <span class="ctrl-label">诊断目标：</span>
            <el-select v-model="mtrTargetType" style="width: 270px">
              <el-option
                v-for="opt in mtrTargetOptions"
                :key="opt.value"
                :label="opt.label"
                :value="opt.value"
              />
            </el-select>
          </div>
          <div v-if="mtrTargetType === 'custom'" class="mtr-ctrl-item" style="flex: 1">
            <el-input v-model="mtrCustomTarget" placeholder="输入目标 IP 或域名 (如 1.1.1.1)" clearable />
          </div>
          <div class="mtr-ctrl-item">
            <span class="ctrl-label">采样次数：</span>
            <el-select v-model="mtrCount" style="width: 95px">
              <el-option :value="10" label="10 次" />
              <el-option :value="15" label="15 次" />
              <el-option :value="20" label="20 次" />
            </el-select>
          </div>
          <el-button
            type="primary"
            :loading="mtrLoading"
            :icon="Aim"
            @click="startMTRDiagnose"
          >
            {{ mtrLoading ? '逐跳采样中...' : '开始诊断' }}
          </el-button>
        </div>

        <div v-if="mtrLoading" class="mtr-loading-box">
          <el-icon class="is-loading" :size="32"><Loading /></el-icon>
          <div class="loading-tip">正在从节点发起多轮 ICMP/UDP 探测，分析各骨干跃点丢包与抖动，请稍候...</div>
        </div>

        <!-- 诊断结果区 -->
        <div v-else-if="mtrReport" class="mtr-result-box">
          <!-- 诊断概览摘要卡片 -->
          <div class="mtr-summary-card" :class="{ 'has-real-drop': (mtrReport.real_drop_hops || 0) > 0 || (mtrReport.summary_loss || mtrReport.end_to_end_loss || 0) > 0 }">
            <div class="summary-header">
              <div class="summary-target">
                <span class="summary-label">探测目标：</span>
                <span class="summary-val font-mono">{{ mtrReport.target || mtrReport.target_host || '-' }}</span>
                <span v-if="mtrReport.target_label" class="summary-label-sub" style="margin-left: 6px; color: #38bdf8; font-size: 12px">
                  ({{ mtrReport.target_label }})
                </span>
                <span class="summary-tool font-mono">({{ mtrReport.tool || 'mtr' }})</span>
              </div>
              <div class="summary-badges">
                <span class="metric-pill">
                  目标端丢包：<b :style="{ color: getLossColor(mtrReport.summary_loss ?? mtrReport.end_to_end_loss ?? 0) }">{{ mtrReport.summary_loss ?? mtrReport.end_to_end_loss ?? 0 }}%</b>
                </span>
                <span class="metric-pill">
                  平均时延：<b>{{ (mtrReport.summary_avg_ms ?? mtrReport.end_to_end_avg_ms) != null ? (mtrReport.summary_avg_ms ?? mtrReport.end_to_end_avg_ms).toFixed(1) + ' ms' : '-' }}</b>
                </span>
              </div>
            </div>
            <div class="summary-diagnosis">
              <el-icon :size="16"><InfoFilled /></el-icon>
              <span>{{ mtrReport.diagnosis || mtrReport.summary || '链路排查完成' }}</span>
            </div>
          </div>

          <!-- 跃点明细表 -->
          <div class="mtr-table-wrap">
            <el-table :data="mtrReport.hops || []" size="small" stripe border style="width: 100%">
              <el-table-column label="#" width="48" align="center">
                <template #default="{ row }">
                  <span class="font-mono">{{ row.count ?? row.hop ?? '' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="跃点路由与归属" min-width="210">
                <template #default="{ row }">
                  <div class="hop-host-cell">
                    <span class="hop-ip font-mono">{{ row.ip || row.host || '???' }}</span>
                    <span v-if="row.host && row.host !== row.ip" class="hop-hostname">{{ row.host }}</span>
                    <span v-if="row.location || row.node_name || row.city" class="hop-loc">
                      {{ row.location || row.node_name || (row.city ? (row.asn ? row.asn + ' ' + row.city : row.city) : '') }}
                    </span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="丢包率 (Loss%)" width="200">
                <template #default="{ row }">
                  <div class="loss-cell">
                    <span class="loss-val font-mono" :style="{ color: getLossColor(row.loss_percent ?? row.loss_pct ?? 0) }">
                      {{ row.loss_percent ?? row.loss_pct ?? 0 }}%
                    </span>
                    <el-tooltip
                      v-if="row.is_rate_limit || row.is_rate_limited"
                      content="该骨干路由 CPU 限制了 ICMP 响应频率，但后续跃点通信正常，数据包已完整穿透，非真实业务丢包。"
                      placement="top"
                    >
                      <span class="pill-rate-limit">ICMP限速(正常)</span>
                    </el-tooltip>
                    <el-tooltip
                      v-else-if="(row.loss_percent ?? row.loss_pct ?? 0) > 0"
                      content="丢包已向下游跃点持续传递，属于真实链路拥塞或物理丢包，会影响业务流量！"
                      placement="top"
                    >
                      <span class="pill-real-drop">真实丢包</span>
                    </el-tooltip>
                    <span v-else class="pill-perfect">0%</span>
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="平均时延" width="95" align="right">
                <template #default="{ row }">
                  <span class="font-mono">{{ (row.avg_rtt_ms ?? row.avg_ms) != null ? (row.avg_rtt_ms ?? row.avg_ms).toFixed(1) + ' ms' : '-' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="最优 / 最差" width="130" align="right">
                <template #default="{ row }">
                  <span class="font-mono text-muted">
                    {{ (row.best_rtt_ms ?? row.best_ms) != null ? (row.best_rtt_ms ?? row.best_ms).toFixed(1) : '-' }} / {{ (row.worst_rtt_ms ?? row.worst_ms) != null ? (row.worst_rtt_ms ?? row.worst_ms).toFixed(1) : '-' }} ms
                  </span>
                </template>
              </el-table-column>
              <el-table-column label="抖动 (StDev)" width="105" align="right">
                <template #default="{ row }">
                  <span class="font-mono text-muted">{{ row.stdev_ms != null ? (row.stdev_ms).toFixed(1) + ' ms' : '-' }}</span>
                </template>
              </el-table-column>
            </el-table>
          </div>

          <!-- 无跃点告警提示 -->
          <div v-if="!mtrReport.hops || mtrReport.hops.length === 0" style="margin-top: 12px; padding: 10px 14px; background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.3); border-radius: 6px; color: #fb7185; font-size: 12px;">
            <el-icon style="margin-right: 6px; vertical-align: -2px"><InfoFilled /></el-icon>
            {{ mtrReport.summary || mtrReport.diagnosis || '未能获取到跃点数据，已自动展开下方终端回显供排查' }}
          </div>

          <!-- 原始输出折叠 -->
          <div v-if="mtrReport.raw_output" class="mtr-raw-toggle">
            <el-button link type="primary" size="small" @click="mtrShowRaw = !mtrShowRaw">
              {{ mtrShowRaw ? '收起终端原始输出' : '展开查看终端原始输出 (Raw Output)' }}
            </el-button>
            <pre v-if="mtrShowRaw" class="mtr-raw-terminal font-mono"><code>{{ mtrReport.raw_output }}</code></pre>
          </div>
        </div>

        <!-- 初始空状态 -->
        <div v-else class="mtr-empty-placeholder">
          <el-icon :size="48" style="color: var(--sb-border)"><Aim /></el-icon>
          <div class="empty-text">选择目标线路或输入自定义目标，点击「开始诊断」发起链路逐跳丢包排查</div>
          <div class="empty-sub">支持智能识别骨干路由 CPU 的 ICMP 限速丢包与下游真实丢包</div>
        </div>
      </div>

      <template #footer>
        <el-button @click="mtrDialogVisible = false">关闭</el-button>
        <el-button v-if="mtrReport" type="primary" :loading="mtrLoading" @click="startMTRDiagnose">重新测试</el-button>
      </template>
    </el-dialog>
  </div>
</template>


<style scoped>
:global(.el-popper) {
  line-height: 1.6 !important;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif !important;
  font-size: 12px !important;
}
:global(.el-popper b) {
  color: #38bdf8;
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
  background: #10b981;
  box-shadow: 0 0 8px rgba(16, 185, 129, 0.85);
}
.status-indicator.online::after {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  border: 1.5px solid #10b981;
  animation: pulseLight 2s cubic-bezier(0.24, 0, 0.38, 1) infinite;
}
.status-indicator.offline {
  background: #64748b;
  box-shadow: 0 0 4px rgba(100, 116, 139, 0.4);
}
.status-indicator.blocked {
  background: #f43f5e;
  box-shadow: 0 0 8px rgba(244, 63, 94, 0.9);
}
.status-indicator.blocked::after {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 50%;
  border: 1.5px solid #f43f5e;
  animation: pulseLight 1.5s cubic-bezier(0.24, 0, 0.38, 1) infinite;
}
.status-indicator.pending {
  background: #f59e0b;
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.8);
}
@keyframes pulseLight {
  0% { transform: scale(0.9); opacity: 0.9; }
  60% { transform: scale(1.7); opacity: 0; }
  100% { transform: scale(0.9); opacity: 0; }
}

/* ── 工具栏 ─────────────────────────────────────────────── */
.nodes-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 18px;
  padding: 10px 14px;
  background: rgba(14, 21, 36, 0.7);
  border: 1px solid var(--sb-line);
  border-radius: 10px;
  backdrop-filter: blur(10px);
}
.toolbar-left {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.status-segmented {
  display: inline-flex;
  align-items: center;
  padding: 2px;
  background: rgba(0, 0, 0, 0.35);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
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
  background: rgba(56, 189, 248, 0.18);
  color: #38bdf8;
  font-weight: 600;
}
.seg-btn.seg-blocked.active {
  background: rgba(244, 63, 94, 0.2);
  color: #fb7185;
}

.view-mode-toggle {
  display: inline-flex;
  background: rgba(0, 0, 0, 0.35);
  padding: 2px;
  border-radius: 8px;
  border: 1px solid rgba(255, 255, 255, 0.08);
}
.toggle-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  border: none;
  background: transparent;
  color: var(--sb-muted);
  border-radius: 6px;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.toggle-btn:hover {
  color: var(--sb-text);
}
.toggle-btn.active {
  background: rgba(56, 189, 248, 0.18);
  color: #38bdf8;
  font-weight: 600;
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
  color: #fbbf24;
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
  color: #fbbf24;
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

/* ── 4. 顶级服务器卡片网格 (Server Cards Grid - 极简现代化架构) ──── */
.node-cards-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 20px;
  margin-bottom: 24px;
}
@media (max-width: 1180px) {
  .node-cards-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 700px) {
  .node-cards-grid { grid-template-columns: minmax(0, 1fr); gap: 14px; }
}

.node-card {
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 12px;
  background: linear-gradient(155deg, rgba(17, 30, 49, 0.98) 0%, rgba(10, 18, 32, 0.99) 100%);
  border: 1px solid rgba(120, 153, 190, 0.2);
  border-radius: 16px;
  padding: 17px 18px 15px;
  box-shadow: 0 10px 26px rgba(0, 0, 0, 0.25), inset 0 1px 0 rgba(255, 255, 255, 0.035);
  overflow: hidden;
  transition: transform 0.22s cubic-bezier(0.4, 0, 0.2, 1),
              border-color 0.22s cubic-bezier(0.4, 0, 0.2, 1),
              box-shadow 0.22s cubic-bezier(0.4, 0, 0.2, 1);
}

.node-card:hover {
  transform: translateY(-2px);
  border-color: rgba(56, 189, 248, 0.48);
  box-shadow: 0 14px 32px rgba(0, 0, 0, 0.34), 0 0 22px rgba(56, 189, 248, 0.08);
}

.node-card.is-offline {
  opacity: 0.78;
}
.node-card.has-gfw-block {
  border-color: rgba(244, 63, 94, 0.35);
  box-shadow: 0 8px 24px -4px rgba(244, 63, 94, 0.2);
}

.node-card-glow {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: transparent;
}
.glow-online {
  background: linear-gradient(90deg, transparent, #10b981, transparent);
}
.glow-offline {
  background: linear-gradient(90deg, transparent, #64748b, transparent);
}
.glow-blocked {
  background: linear-gradient(90deg, transparent, #f43f5e, transparent);
}

/* 1. 头部 */
.card-header-clean {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}
.header-main {
  flex: 1;
  min-width: 0;
}
.title-row {
  display: flex;
  align-items: center;
  gap: 9px;
  flex-wrap: wrap;
}
.flag-icon {
  font-size: 18px;
  line-height: 1;
  flex-shrink: 0;
}
.server-title {
  font-size: 15px;
  font-weight: 700;
  color: #f8fafc;
  letter-spacing: -0.2px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.status-indicator-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.status-indicator-dot.online {
  background: #10b981;
  box-shadow: 0 0 8px #10b981;
}
.status-indicator-dot.offline {
  background: #64748b;
}
.status-indicator-dot.blocked {
  background: #f43f5e;
  box-shadow: 0 0 8px #f43f5e;
  animation: pulseLight 1.5s infinite;
}
.pill-gfw-warn {
  font-size: 10px;
  font-weight: 700;
  padding: 1px 6px;
  border-radius: 4px;
  background: rgba(244, 63, 94, 0.16);
  color: #fb7185;
  border: 1px solid rgba(244, 63, 94, 0.35);
}

.subtitle-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 7px;
  font-size: 12px;
  color: var(--sb-muted);
  flex-wrap: wrap;
}
.hosting-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 7px 12px;
  min-height: 22px;
  margin-top: 6px;
  color: var(--sb-muted, #94a3b8);
  font-size: 12px;
  line-height: 1.6;
}
.hosting-provider { color: #7dd3fc; font-weight: 600; text-decoration: none; }
.hosting-provider[href]:hover { color: #bae6fd; text-decoration: underline; }
.sub-os-arch {
  background: rgba(148, 163, 184, 0.09);
  padding: 3px 7px;
  border-radius: 5px;
}
.sub-ver {
  color: #94a3b8;
}
.sub-ip {
  color: #38bdf8;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 3px;
  padding: 1px 5px;
  border-radius: 3px;
  background: rgba(56, 189, 248, 0.08);
  transition: all 0.15s;
}
.sub-ip:hover {
  background: rgba(56, 189, 248, 0.18);
  color: #7dd3fc;
}
.copy-icon {
  font-size: 11px;
}
.chip-ddns {
  font-size: 9px;
  font-weight: 700;
  background: rgba(168, 85, 247, 0.15);
  color: #c084fc;
  border: 1px solid rgba(168, 85, 247, 0.3);
  padding: 1px 4px;
  border-radius: 3px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.btn-speedtest-act {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 3px 8px;
  border-radius: 5px;
  font-size: 11px;
  font-weight: 600;
  background: rgba(56, 189, 248, 0.12);
  color: #38bdf8;
  border: 1px solid rgba(56, 189, 248, 0.28);
  cursor: pointer;
  transition: all 0.15s;
}
.btn-speedtest-act:hover:not(:disabled) {
  background: rgba(56, 189, 248, 0.24);
  color: #fff;
}
.icon-more-btn {
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid rgba(255, 255, 255, 0.08);
  color: var(--sb-muted);
  width: 26px;
  height: 26px;
  border-radius: 5px;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  transition: all 0.15s;
}
.icon-more-btn:hover {
  background: rgba(255, 255, 255, 0.1);
  color: #f8fafc;
}

/* 2. 遥测与带宽聚合 */
.card-telemetry-clean {
  background: rgba(0, 0, 0, 0.18);
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.hw-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
}
.hw-item {
  cursor: default;
}
.hw-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
  margin-bottom: 7px;
}
.hw-lbl {
  color: var(--sb-muted);
  font-size: 10px;
  text-transform: uppercase;
}
.hw-val {
  font-weight: 600;
}
.hw-track {
  width: 100%;
  height: 5px;
  background: rgba(148, 163, 184, 0.14);
  border-radius: 2px;
  overflow: hidden;
}
.hw-bar {
  height: 100%;
  border-radius: 2px;
  transition: width 0.3s ease;
}

.network-stream-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
  padding-top: 10px;
  border-top: 1px solid rgba(148, 163, 184, 0.12);
}
.rate-group {
  display: flex;
  gap: 14px;
}
.rate-down {
  color: #34d399;
  font-weight: 600;
}
.rate-up {
  color: #38bdf8;
  font-weight: 600;
}
.traffic-conn-group {
  display: flex;
  align-items: center;
  gap: 8px;
}
.total-stream {
  color: var(--sb-muted);
}
.conn-pill {
  padding: 1px 5px;
  border-radius: 3px;
  background: rgba(255, 255, 255, 0.05);
  color: var(--sb-muted);
  font-size: 10px;
}
.conn-pill.has-conn {
  background: rgba(16, 185, 129, 0.12);
  color: #34d399;
  border: 1px solid rgba(16, 185, 129, 0.25);
}

/* 3. 网络线路矩阵 (去程 & 返程) */
.route-matrix-box {
  background: rgba(0, 0, 0, 0.18);
  border-radius: 8px;
  padding: 10px 12px;
  display: flex;
  flex-direction: column;
  gap: 9px;
  border: 1px solid rgba(120, 153, 190, 0.13);
}
.route-matrix-box.is-blocked {
  border-color: rgba(244, 63, 94, 0.25);
  background: rgba(244, 63, 94, 0.04);
}

.route-line {
  display: flex;
  align-items: center;
  gap: 11px;
}
.dir-badge {
  font-size: 9px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 3px;
  letter-spacing: 0.5px;
  flex-shrink: 0;
}
.dir-badge.in {
  background: rgba(56, 189, 248, 0.15);
  color: #38bdf8;
  border: 1px solid rgba(56, 189, 248, 0.3);
}
.dir-badge.out {
  background: rgba(168, 85, 247, 0.15);
  color: #c084fc;
  border: 1px solid rgba(168, 85, 247, 0.3);
}

.gfw-alert-capsule {
  display: flex;
  align-items: center;
  gap: 6px;
}
.gfw-alert-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #f43f5e;
  box-shadow: 0 0 8px #f43f5e;
  animation: pulseLight 1.5s infinite;
}
.gfw-alert-txt {
  font-size: 11px;
  font-weight: 700;
  color: #fb7185;
}
.gfw-detail-btn {
  background: rgba(244, 63, 94, 0.18);
  border: 1px solid rgba(244, 63, 94, 0.35);
  color: #fecdd3;
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  cursor: pointer;
  transition: all 0.15s;
}
.gfw-detail-btn:hover {
  background: rgba(244, 63, 94, 0.3);
  color: #fff;
}

.carrier-tags-flow {
  display: flex;
  align-items: center;
  gap: 5px;
  flex-wrap: wrap;
  flex: 1;
}
.carrier-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 8px;
  border-radius: 6px;
  font-size: 12px;
  background: rgba(30, 48, 70, 0.62);
  border: 1px solid rgba(120, 153, 190, 0.16);
  white-space: nowrap;
  cursor: default;
}
.dot-carrier {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot-carrier.ct { background: #3b82f6; }
.dot-carrier.cu { background: #f59e0b; }
.dot-carrier.cm { background: #10b981; }

.route-empty-hint {
  font-size: 11px;
}

/* 4. 底栏 */
.card-footer-clean {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11px;
  color: var(--sb-muted);
  padding-top: 4px;
}
.footer-time-info {
  display: flex;
  align-items: center;
  gap: 4px;
}
.time-sep {
  opacity: 0.5;
}

/* ── 列表表格与辅助 ─────────────────────────────────────── */
.cards-pagination-bar {
  display: flex;
  justify-content: center;
  margin-top: 16px;
  margin-bottom: 30px;
}
.rate-stack {
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 11px;
}
.rate-item.down { color: #34d399; }
.rate-item.up { color: #38bdf8; }

.table-hw-stack {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 11px;
}
.hw-mini-row {
  display: flex;
  align-items: center;
  gap: 4px;
}
.hw-mini-name {
  color: var(--sb-muted);
  width: 32px;
}
.hw-mini-val {
  font-weight: 600;
}
.gfw-table-alert {
  color: #fb7185;
  font-weight: 700;
  font-size: 12px;
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
  padding: 12px 14px;
  background: rgba(0, 0, 0, 0.6);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
  color: #7dd3fc;
  font-size: 12px;
  line-height: 1.5;
  word-break: break-all;
  white-space: pre-wrap;
  user-select: all;
}

.setting-edit-box {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 10px 14px;
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm);
  background: var(--sb-surface-muted, rgba(148, 163, 184, 0.05));
  box-sizing: border-box;
}
.setting-edit-text {
  flex: 1;
  margin-right: 16px;
}
.setting-edit-heading {
  font-weight: 600;
  color: var(--sb-text-1);
  margin-bottom: 2px;
}

/* ── 逐跳丢包排障 (MTR) 样式 ───────────────────────── */
.mtr-ctrl-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  background: var(--sb-surface-muted, rgba(148, 163, 184, 0.05));
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm, 8px);
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.mtr-ctrl-item {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ctrl-label {
  font-size: 13px;
  color: var(--sb-text-2);
  white-space: nowrap;
}
.mtr-loading-box {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 16px;
  gap: 14px;
  color: var(--sb-accent, #38bdf8);
}
.loading-tip {
  font-size: 13px;
  color: var(--sb-text-2);
}
.mtr-summary-card {
  padding: 14px 16px;
  background: var(--sb-panel, rgba(23, 33, 54, 0.5));
  border: 1px solid var(--sb-line);
  border-left: 4px solid var(--sb-success, #10b981);
  border-radius: var(--sb-radius-sm, 8px);
  margin-bottom: 16px;
}
.mtr-summary-card.has-real-drop {
  border-left-color: var(--sb-danger, #ef4444);
}
.summary-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  flex-wrap: wrap;
  gap: 8px;
}
.summary-target {
  font-size: 13px;
}
.summary-label {
  color: var(--sb-text-2);
}
.summary-val {
  font-weight: 600;
  color: var(--sb-text-1);
}
.summary-tool {
  color: var(--sb-muted);
  font-size: 11px;
  margin-left: 6px;
}
.summary-badges {
  display: flex;
  gap: 8px;
}
.metric-pill {
  font-size: 12px;
  padding: 2px 8px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid var(--sb-line);
  border-radius: 4px;
  color: var(--sb-text-2);
}
.metric-pill b {
  color: var(--sb-text-1);
}
.summary-diagnosis {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--sb-text-2);
  line-height: 1.5;
}
.hop-host-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.hop-ip {
  font-weight: 600;
  color: var(--sb-text-1);
  font-size: 12px;
}
.hop-hostname {
  font-size: 11px;
  color: var(--sb-muted);
}
.hop-loc {
  font-size: 11px;
  color: #38bdf8;
}
.loss-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}
.loss-val {
  font-weight: 700;
  font-size: 13px;
  min-width: 44px;
}
.pill-rate-limit {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: rgba(245, 158, 11, 0.12);
  color: #f59e0b;
  border: 1px solid rgba(245, 158, 11, 0.3);
  cursor: help;
  white-space: nowrap;
}
.pill-real-drop {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: rgba(239, 68, 68, 0.15);
  color: #ef4444;
  border: 1px solid rgba(239, 68, 68, 0.35);
  cursor: help;
  font-weight: 600;
  white-space: nowrap;
}
.pill-perfect {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 4px;
  background: rgba(16, 185, 129, 0.1);
  color: #10b981;
}
.text-muted {
  color: var(--sb-text-2);
}
.mtr-raw-toggle {
  margin-top: 12px;
}
.mtr-raw-terminal {
  margin-top: 8px;
  padding: 12px;
  background: #090d16;
  border: 1px solid var(--sb-line);
  border-radius: 6px;
  color: #7dd3fc;
  font-size: 11px;
  line-height: 1.5;
  max-height: 240px;
  overflow-y: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.mtr-empty-placeholder {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 48px 16px;
  gap: 10px;
  text-align: center;
}
.empty-text {
  font-size: 14px;
  font-weight: 500;
  color: var(--sb-text-1);
}
.empty-sub {
  font-size: 12px;
  color: var(--sb-muted);
}

:deep(.el-popper) {
  max-width: 280px !important;
  white-space: pre-line !important;
  line-height: 1.45 !important;
  font-size: 11px !important;
}
</style>
