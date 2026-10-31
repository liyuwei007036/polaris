<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Aim,
  Close,
  Connection,
  CopyDocument,
  Delete,
  Edit,
  InfoFilled,
  Loading,
  MoreFilled,
  Plus,
  Refresh,
  RemoveFilled,
  Search,
  Top,
} from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { formatBytes, formatDateTime, includesText } from '../format'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'

const MASTER_HOST_KEY = 'polaris_master_host'
const CARRIERS = [
  { isp: 'telecom', short: '电信' },
  { isp: 'unicom', short: '联通' },
  { isp: 'mobile', short: '移动' },
]
const PERIOD_LABELS = { day: '天', month: '月', quarter: '季', year: '年' }

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
const editTab = ref('basic')
const editTabs = computed(() => [
  { key: 'basic', label: '基本信息' },
  { key: 'plan', label: '套餐与到期' },
  { key: 'network', label: '网络与 DDNS', badge: editNodeAllIPs.value.length || '' },
])
const editHeader = computed(() => buildDialogHeader(editNode.value, editForm.value.name))
const editPlanPreview = computed(() => {
  const parts = [getPriceText(editForm.value), getExpiry(editForm.value.expires_on)?.text].filter(Boolean)
  return parts.join(' · ')
})

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

function buildRoute(st, isp, node, direction) {
  const prefix = direction === 'in' ? `inbound_${isp}` : isp
  const latency = direction === 'in' ? st[`${prefix}_latency_ms`] : (st[`${prefix}_latency_ms`] ?? st[`${prefix}_ping_ms`])
  const blocked = direction === 'in' && isNodeGFWBlocked(st)
  return {
    label: blocked ? '阻断' : formatCarrierDisplay(st[`${prefix}_route`], latency),
    latency: blocked ? '' : (latency > 0 ? `${latency}ms` : ''),
    cls: blocked ? 'ping-status-timeout' : getPingClass(latency),
    tip: direction === 'in'
      ? (blocked ? getInboundProbeTooltip(st) : getInboundCarrierTooltip(st, isp, node))
      : getISPTooltip(st, isp, node),
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
  const blocked = isNodeGFWBlocked(speedtests.value[node.id])
  const status = !node.online ? 'offline' : blocked ? 'blocked' : 'online'
  return { status, blocked, statusText: { online: '在线', offline: '离线', blocked: '端口阻断' }[status] }
}

function buildDialogHeader(node, name) {
  if (!node) return null
  const avatar = getAvatar(name || node.name)
  return {
    ...getNodeStatus(node),
    avatar: avatar.text,
    isFlag: avatar.isFlag,
    name: avatar.rest || name || node.name,
    sub: [[node.os || 'linux', node.architecture].filter(Boolean).join(' · '), node.city || node.location, node.client_address]
      .filter(Boolean)
      .join(' · '),
  }
}

function getPriceText(node) {
  if (!node.price_period) return ''
  if (node.price_period === 'free') return '免费'
  return `${node.currency || 'USD'} ${node.price || 0}/${PERIOD_LABELS[node.price_period] || node.price_period}`
}

const cards = computed(() => filteredNodes.value.map((node) => {
  const host = node.online ? metrics.value[node.id]?.host : null
  const proxy = metrics.value[node.id]?.proxy
  const st = speedtests.value[node.id]
  const snap = live.value.get(node.id)
  const { status, statusText, blocked } = getNodeStatus(node)
  const avatar = getAvatar(node.name)
  return {
    node,
    status,
    statusText,
    blocked,
    avatar: avatar.text,
    isFlag: avatar.isFlag,
    displayName: avatar.rest || node.name,
    platform: [node.os || 'linux', node.architecture].filter(Boolean).join(' · '),
    region: node.city || node.location || '',
    gauges: [
      buildGauge('cpu', 'CPU', host?.cpu_percent, Boolean(host), host ? `${host.cpu_percent.toFixed(1)}%` : '待上报', getCPUTooltip(node)),
      buildGauge('mem', '内存', getResourcePercent(host?.memory_used, host?.memory_total), Boolean(host?.memory_total),
        host?.memory_total ? `${formatResourceBytes(host.memory_used)} / ${formatResourceBytes(host.memory_total)}` : '待上报', getMemTooltip(node)),
      buildGauge('disk', '硬盘', getResourcePercent(host?.disk_used, host?.disk_total), Boolean(host?.disk_total),
        host?.disk_total ? `${formatResourceBytes(host.disk_used)} / ${formatResourceBytes(host.disk_total)}` : '待上报', getDiskTooltip(node)),
    ],
    rx: snap?.has_rates ? formatBytes(snap.received_rate, '/s') : '0 B/s',
    tx: snap?.has_rates ? formatBytes(snap.sent_rate, '/s') : '0 B/s',
    total: proxy ? formatBytes((proxy.received_bytes || 0) + (proxy.sent_bytes || 0)) : '',
    totalTip: proxy ? `累计下行 ${formatBytes(proxy.received_bytes)} · 累计上行 ${formatBytes(proxy.sent_bytes)}` : '',
    conn: snap?.connection_count ?? 0,
    testedAt: st?.tested_at ? formatFriendlyTime(st.tested_at).replace('刚刚活跃', '刚刚') : '',
    routes: st
      ? CARRIERS.map((c) => ({ ...c, in: buildRoute(st, c.isp, node, 'in'), out: buildRoute(st, c.isp, node, 'out') }))
      : null,
    price: getPriceText(node),
    expiry: getExpiry(node.expires_on),
    ddnsTip: node.ddns_enabled
      ? `DDNS 域名: ${(node.ddns_domains?.length ? node.ddns_domains : [node.ddns_domain || node.client_address]).join('、')}<br/>当前解析 IP: ${node.last_observed_ip || '同步中'}`
      : '',
    ipTip: node.ip_addresses?.length
      ? `本机网卡检测到的出口 IP (${node.ip_addresses.length} 个):<br/>${node.ip_addresses.map((ip) => ip + (node.ip_aliases?.[ip] ? ` (${node.ip_aliases[ip]})` : '')).join('<br/>')}`
      : '',
  }
}))

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
    provider: node.provider || '', provider_url: node.provider_url || '', traffic_plan: node.traffic_plan || '', bandwidth: node.bandwidth || '',
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
  if (!loss || loss <= 0) return 'var(--sb-success, var(--sb-success))'
  if (loss < 10) return 'var(--sb-warning)'
  if (loss < 50) return '#ea580c'
  return 'var(--sb-danger, #ef4444)'
}

function formatMs(value) {
  return value != null && value !== '' ? Number(value).toFixed(1) : '-'
}

function formatLoss(value) {
  return Number.isInteger(value) ? String(value) : value.toFixed(1)
}

const mtrHeader = computed(() => buildDialogHeader(mtrTargetNode.value))

const mtrHops = computed(() => (mtrReport.value?.hops || []).map((h, i) => {
  const loss = Number(h.loss_percent ?? h.loss_pct ?? 0) || 0
  return {
    key: i,
    no: h.count ?? h.hop ?? i + 1,
    ip: h.ip || h.host || '???',
    host: h.host && h.host !== h.ip ? h.host : '',
    loc: h.location || h.node_name || (h.city ? (h.asn ? `${h.asn} ${h.city}` : h.city) : ''),
    loss,
    lossText: formatLoss(loss),
    lossColor: getLossColor(loss),
    kind: h.is_rate_limit || h.is_rate_limited ? 'limit' : loss > 0 ? 'drop' : 'ok',
    avg: formatMs(h.avg_rtt_ms ?? h.avg_ms),
    best: formatMs(h.best_rtt_ms ?? h.best_ms),
    worst: formatMs(h.worst_rtt_ms ?? h.worst_ms),
    stdev: formatMs(h.stdev_ms),
  }
}))

const mtrSummary = computed(() => {
  const r = mtrReport.value
  if (!r) return null
  const loss = Number(r.summary_loss ?? r.end_to_end_loss ?? 0) || 0
  const realDrops = r.real_drop_hops ?? mtrHops.value.filter((h) => h.kind === 'drop').length
  return {
    target: r.target || r.target_host || '-',
    label: r.target_label || '',
    tool: r.tool || 'mtr',
    loss: formatLoss(loss),
    lossColor: getLossColor(loss),
    avg: formatMs(r.summary_avg_ms ?? r.end_to_end_avg_ms),
    hopCount: mtrHops.value.length,
    realDrops,
    bad: realDrops > 0 || loss > 0,
    diagnosis: r.diagnosis || r.summary || '链路排查完成',
  }
})

function handleCardCommand(node, command) {
  if (command === 'edit') openEdit(node)
  else if (command === 'upgradeAgent') upgradeAgent(node)
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

      <div v-if="cards.length === 0" class="empty-cards-box">
        <el-empty description="未找到符合条件的服务器" />
      </div>

      <div v-else class="n-grid">
        <article v-for="card in cards" :key="card.node.id" class="n-card" :class="`is-${card.status}`">
          <header class="n-head">
            <span class="n-avatar" :class="{ 'is-flag': card.isFlag }">{{ card.avatar }}</span>
            <div class="n-title">
              <div class="n-name-row">
                <h3 class="n-name" :title="card.node.name">{{ card.displayName }}</h3>
                <span class="srv-status" :class="`is-${card.status}`"><i class="srv-status-dot" />{{ card.statusText }}</span>
              </div>
              <div class="n-meta font-mono">
                <span>{{ card.platform }}</span>
                <span v-if="card.region" class="n-region" :title="card.region"> · {{ card.region }}</span>
                <span> · v{{ card.node.agent_version || '—' }}</span>
              </div>
            </div>
            <div class="n-ops">
              <el-tooltip v-if="canWrite && card.node.online" content="三网线路检测" placement="top">
                <button class="srv-icon-btn" :disabled="Boolean(speedtesting[card.node.id])" @click="runSpeedtest(card.node)">
                  <el-icon :class="{ 'is-loading': speedtesting[card.node.id] }"><Connection /></el-icon>
                </button>
              </el-tooltip>
              <el-dropdown v-if="canWrite || isAdmin" trigger="click" @command="handleCardCommand(card.node, $event)">
                <button class="srv-icon-btn" title="更多操作"><el-icon><MoreFilled /></el-icon></button>
                <template #dropdown>
                  <el-dropdown-menu>
                    <el-dropdown-item v-if="canWrite" command="edit"><el-icon><Edit /></el-icon>配置节点</el-dropdown-item>
                    <el-dropdown-item v-if="canWrite && card.node.online" command="mtr"><el-icon><Aim /></el-icon>逐跳丢包排障 (MTR)</el-dropdown-item>
                    <el-dropdown-item v-if="canWrite && card.node.ddns_enabled" command="syncDDNS"><el-icon><Refresh /></el-icon>同步 DDNS</el-dropdown-item>
                    <el-dropdown-item v-if="isAdmin && agentUpdateAvailable(card.node)" command="upgradeAgent"><el-icon><Top /></el-icon>升级 Agent</el-dropdown-item>
                    <el-dropdown-item v-if="isAdmin && singBoxUpdateAvailable(card.node)" command="upgradeSingBox"><el-icon><Top /></el-icon>升级 sing-box</el-dropdown-item>
                    <el-dropdown-item v-if="isAdmin" command="revoke" divided class="danger-item"><el-icon><RemoveFilled /></el-icon>移除服务器</el-dropdown-item>
                  </el-dropdown-menu>
                </template>
              </el-dropdown>
            </div>
          </header>

          <div class="n-chips font-mono">
            <button v-if="card.node.client_address" class="n-chip is-address" :title="`点击复制: ${card.node.client_address}`" @click="copyAddress(card.node.client_address)">
              <span class="n-chip-txt">{{ card.node.client_address }}</span><el-icon><CopyDocument /></el-icon>
            </button>
            <span v-else class="n-chip is-danger">未配置连接地址</span>
            <el-tooltip v-if="card.ddnsTip" raw-content :content="card.ddnsTip" placement="top">
              <span class="n-chip is-violet">{{ card.node.last_observed_ip || 'DDNS 同步中' }}</span>
            </el-tooltip>
            <el-tooltip v-if="card.ipTip" raw-content :content="card.ipTip" placement="top">
              <span class="n-chip">{{ card.node.ip_addresses.length }} 出口</span>
            </el-tooltip>
          </div>

          <div class="n-gauges">
            <el-tooltip v-for="g in card.gauges" :key="g.key" :content="g.tip" placement="top">
              <div class="n-gauge" :class="{ 'is-empty': !g.available }">
                <div class="n-g-top font-mono"><span>{{ g.label }}</span><b :style="{ color: g.color }">{{ g.text }}<i v-if="g.available">%</i></b></div>
                <div class="n-g-track"><i :style="{ width: `${g.p}%`, background: g.color }" /></div>
              </div>
            </el-tooltip>
          </div>

          <div class="n-net font-mono">
            <span class="is-down">↓{{ card.rx }}</span>
            <span class="is-up">↑{{ card.tx }}</span>
            <el-tooltip v-if="card.totalTip" :content="card.totalTip" placement="top">
              <span class="is-total">{{ card.total }}</span>
            </el-tooltip>
            <span v-else class="is-total">累计 —</span>
            <span class="n-conn" :class="{ 'is-active': card.conn > 0 }">{{ card.conn }} 连</span>
          </div>

          <div class="n-routes" :class="{ 'is-blocked': card.blocked }">
            <template v-if="card.routes">
              <div class="n-route-line font-mono">
                <span class="n-dir is-in">去</span>
                <el-tooltip v-for="r in card.routes" :key="r.isp" raw-content :content="r.in.tip" placement="top">
                  <span class="n-lat" :class="r.in.cls"><b :class="`is-${r.isp}`">{{ r.short[0] }}</b>{{ r.in.label }}<em v-if="r.in.latency"> {{ r.in.latency }}</em></span>
                </el-tooltip>
              </div>
              <div class="n-route-line font-mono">
                <span class="n-dir is-out">返</span>
                <el-tooltip v-for="r in card.routes" :key="`out-${r.isp}`" raw-content :content="r.out.tip" placement="top">
                  <span class="n-lat" :class="r.out.cls"><b :class="`is-${r.isp}`">{{ r.short[0] }}</b>{{ r.out.label }}<em v-if="r.out.latency"> {{ r.out.latency }}</em></span>
                </el-tooltip>
              </div>
            </template>
            <div v-else class="n-route-line is-empty font-mono">
              <span class="n-dir is-in">去</span><span class="n-lat is-idle">待检测</span>
              <span class="n-dir is-out">返</span><span class="n-lat is-idle">待检测</span>
              <button
                v-if="canWrite && card.node.online"
                class="n-link"
                :disabled="Boolean(speedtesting[card.node.id])"
                @click="runSpeedtest(card.node)"
              >
                {{ speedtesting[card.node.id] ? '检测中…' : '立即检测' }}
              </button>
            </div>
          </div>

          <footer class="n-foot">
            <span class="n-plan">
              <a v-if="card.node.provider && card.node.provider_url" :href="card.node.provider_url" target="_blank" rel="noopener noreferrer" class="n-provider" :title="card.node.provider">{{ card.node.provider }}</a>
              <span v-else-if="card.node.provider" class="n-provider" :title="card.node.provider">{{ card.node.provider }}</span>
              <span v-if="card.price">{{ card.price }}</span>
              <span v-if="card.expiry" :class="card.expiry.level && `is-${card.expiry.level}`" :title="card.expiry.title">{{ card.expiry.text }}</span>
            </span>
            <span class="n-time font-mono" :title="formatDateTime(card.node.last_seen_at)">
              {{ formatFriendlyTime(card.node.last_seen_at) }}<template v-if="card.node.online"> · {{ formatUptimeText(metrics[card.node.id]?.host?.uptime_seconds) }}</template>
            </span>
          </footer>
        </article>
      </div>
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
    <el-dialog v-model="editDialog" width="680px" class="srv-dialog" :show-close="false" align-center>
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
            <div><span>系统</span><b class="font-mono">{{ editNode?.os || '—' }} / {{ editNode?.architecture || '—' }}</b></div>
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
            <el-form-item label="流量套餐">
              <el-input v-model="editForm.traffic_plan" placeholder="例如：每月 2 TB" />
            </el-form-item>
            <el-form-item label="带宽大小">
              <el-input v-model="editForm.bandwidth" placeholder="例如：1 Gbps" />
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
          <div v-if="editPlanPreview" class="dlg-preview">
            <span>卡片底栏将显示</span>
            <b>{{ editPlanPreview }}</b>
          </div>
        </section>

        <section v-show="editTab === 'network'" class="dlg-pane">
          <div class="dlg-block">
            <div class="dlg-block-head">
              <div>
                <div class="dlg-block-title">出口 IP 与别名</div>
                <div class="dlg-help">网卡自动检测或手动添加的出口 IP。配置用户出口与节点分流时会优先显示别名。</div>
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
                  title="删除手动添加的出口 IP"
                  @click="removeCustomEgressIP(ip)"
                />
              </div>
            </div>
            <div v-else class="dlg-empty">暂未检测到出口 IP，可在下方手动添加。</div>
            <div class="dlg-inline">
              <el-input
                v-model="newCustomEgressIP"
                size="small"
                placeholder="手动添加出口 IP（如 198.51.100.2 或 2001:db8::1）"
                clearable
                @keydown.enter.prevent="addCustomEgressIP"
              />
              <el-button size="small" :icon="Plus" @click="addCustomEgressIP">添加</el-button>
            </div>
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
          <span class="dlg-foot-hint">三个分页的修改会一起保存</span>
          <el-button @click="editDialog = false">取消</el-button>
          <el-button type="primary" :loading="editSaving" @click="saveEdit">保存配置</el-button>
        </div>
      </template>
    </el-dialog>

    <!-- 逐跳丢包排障 (MTR) 弹窗 -->
    <el-dialog v-model="mtrDialogVisible" width="880px" class="srv-dialog" :show-close="false" align-center destroy-on-close>
      <template #header="{ close }">
        <div v-if="mtrHeader" class="dlg-hero" :class="`is-${mtrHeader.status}`">
          <div class="srv-avatar" :class="{ 'is-flag': mtrHeader.isFlag }">{{ mtrHeader.avatar }}</div>
          <div class="dlg-hero-text">
            <div class="dlg-hero-kicker">链路逐跳丢包排障</div>
            <div class="dlg-hero-name">
              <span>{{ mtrHeader.name }}</span>
              <span class="srv-status" :class="`is-${mtrHeader.status}`"><i class="srv-status-dot" />{{ mtrHeader.statusText }}</span>
            </div>
            <div class="dlg-hero-sub font-mono">{{ mtrHeader.sub }}</div>
          </div>
          <button class="srv-icon-btn" title="关闭" @click="close"><el-icon><Close /></el-icon></button>
        </div>
      </template>

      <div class="mtr-ctrl">
        <div class="mtr-ctrl-field is-grow">
          <span class="mtr-ctrl-label">诊断目标</span>
          <el-select v-model="mtrTargetType" placeholder="选择目标线路" style="width: 100%">
            <el-option v-for="opt in mtrTargetOptions" :key="opt.value" :label="opt.label" :value="opt.value" />
          </el-select>
        </div>
        <div v-if="mtrTargetType === 'custom'" class="mtr-ctrl-field is-grow">
          <span class="mtr-ctrl-label">自定义目标</span>
          <el-input v-model="mtrCustomTarget" placeholder="IP 或域名，如 1.1.1.1" clearable @keydown.enter.prevent="startMTRDiagnose" />
        </div>
        <div class="mtr-ctrl-field">
          <span class="mtr-ctrl-label">采样次数</span>
          <el-select v-model="mtrCount" style="width: 100px">
            <el-option :value="10" label="10 次" />
            <el-option :value="15" label="15 次" />
            <el-option :value="20" label="20 次" />
          </el-select>
        </div>
        <el-button type="primary" :loading="mtrLoading" :icon="Aim" @click="startMTRDiagnose">
          {{ mtrLoading ? '采样中…' : mtrReport ? '重新诊断' : '开始诊断' }}
        </el-button>
      </div>

      <div v-if="mtrLoading" class="mtr-state">
        <div class="mtr-radar"><el-icon class="is-loading" :size="26"><Loading /></el-icon></div>
        <div class="mtr-state-title">正在逐跳采样</div>
        <div class="mtr-state-sub">节点正在发起多轮 ICMP/UDP 探测，分析各骨干跃点的丢包与抖动，请稍候。</div>
      </div>

      <div v-else-if="mtrReport && mtrSummary" class="mtr-result">
        <div class="mtr-stats">
          <div class="mtr-stat is-target">
            <span>探测目标 · {{ mtrSummary.tool }}</span>
            <b class="font-mono">{{ mtrSummary.target }}</b>
            <small v-if="mtrSummary.label">{{ mtrSummary.label }}</small>
          </div>
          <div class="mtr-stat">
            <span>目标端丢包</span>
            <b class="font-mono" :style="{ color: mtrSummary.lossColor }">{{ mtrSummary.loss }}<small>%</small></b>
          </div>
          <div class="mtr-stat">
            <span>平均时延</span>
            <b class="font-mono">{{ mtrSummary.avg }}<small v-if="mtrSummary.avg !== '-'">ms</small></b>
          </div>
          <div class="mtr-stat">
            <span>跃点 / 真实丢包</span>
            <b class="font-mono">{{ mtrSummary.hopCount }}<small> / </small><em :class="{ 'is-bad': mtrSummary.realDrops > 0 }">{{ mtrSummary.realDrops }}</em></b>
          </div>
        </div>

        <div class="mtr-verdict" :class="{ 'is-bad': mtrSummary.bad }">
          <el-icon :size="16"><InfoFilled /></el-icon>
          <span>{{ mtrSummary.diagnosis }}</span>
        </div>

        <div v-if="mtrHops.length" class="mtr-hops">
          <div class="mtr-hop is-head">
            <span>#</span><span>跃点路由与归属</span><span>丢包率</span>
            <span class="is-num">平均</span><span class="is-num">最优 / 最差</span><span class="is-num">抖动</span>
          </div>
          <div v-for="h in mtrHops" :key="h.key" class="mtr-hop" :class="`is-${h.kind}`">
            <span class="mtr-hop-no font-mono">{{ h.no }}</span>
            <div class="mtr-hop-host">
              <span class="font-mono">{{ h.ip }}</span>
              <small v-if="h.host">{{ h.host }}</small>
              <small v-if="h.loc" class="is-loc">{{ h.loc }}</small>
            </div>
            <div class="mtr-hop-loss">
              <div class="mtr-loss-top">
                <b class="font-mono" :style="{ color: h.lossColor }">{{ h.lossText }}%</b>
                <el-tooltip v-if="h.kind === 'limit'" content="该骨干路由限制了 ICMP 响应频率，但后续跃点正常，数据包已完整穿透，不是真实业务丢包。" placement="top">
                  <span class="mtr-tag is-limit">ICMP 限速</span>
                </el-tooltip>
                <el-tooltip v-else-if="h.kind === 'drop'" content="丢包持续传递到下游跃点，属于真实链路拥塞或物理丢包，会影响业务流量。" placement="top">
                  <span class="mtr-tag is-drop">真实丢包</span>
                </el-tooltip>
              </div>
              <div class="mtr-loss-track"><i :style="{ width: `${Math.min(100, h.loss)}%`, background: h.lossColor }" /></div>
            </div>
            <span class="is-num font-mono">{{ h.avg }}</span>
            <span class="is-num font-mono is-muted">{{ h.best }} / {{ h.worst }}</span>
            <span class="is-num font-mono is-muted">{{ h.stdev }}</span>
          </div>
        </div>
        <div v-else class="mtr-verdict is-bad">
          <el-icon :size="16"><InfoFilled /></el-icon>
          <span>未能获取到跃点数据，已自动展开下方终端原始输出供排查。</span>
        </div>

        <div v-if="mtrReport.raw_output" class="mtr-raw">
          <button class="srv-link-btn" @click="mtrShowRaw = !mtrShowRaw">
            {{ mtrShowRaw ? '收起终端原始输出' : '查看终端原始输出' }}
          </button>
          <pre v-if="mtrShowRaw" class="mtr-raw-terminal font-mono"><code>{{ mtrReport.raw_output }}</code></pre>
        </div>
      </div>

      <div v-else class="mtr-state">
        <div class="mtr-radar is-idle"><el-icon :size="26"><Aim /></el-icon></div>
        <div class="mtr-state-title">选择目标线路，开始逐跳排查</div>
        <div class="mtr-state-sub">能区分骨干路由的 ICMP 限速丢包和影响业务的真实丢包。</div>
      </div>

      <template #footer>
        <el-button @click="mtrDialogVisible = false">关闭</el-button>
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
  color: var(--sb-accent);
  font-weight: 600;
}
.seg-btn.seg-blocked.active {
  background: rgba(244, 63, 94, 0.2);
  color: var(--sb-danger);
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

/* ── 服务器卡片 ─────────────────────────────────────────── */
/* ── 服务器卡片 ─────────────────────────────────────────── */
.n-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 12px;
}
.n-card {
  --tone: var(--sb-success);
  --tone-soft: rgba(52, 211, 153, .1);
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 9px;
  min-width: 0;
  padding: 13px 14px 11px;
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  background: var(--sb-surface);
  overflow: hidden;
  transition: transform .18s ease, border-color .18s ease, box-shadow .18s ease;
}
.n-card::before {
  content: '';
  position: absolute;
  inset: 0 0 auto;
  height: 2px;
  background: var(--tone);
  opacity: .55;
}
.n-card:hover {
  transform: translateY(-2px);
  border-color: rgba(56, 189, 248, .45);
  box-shadow: var(--sb-shadow);
}
.n-card.is-offline {
  --tone: var(--sb-muted);
  --tone-soft: rgba(148, 163, 184, .08);
}
.n-card.is-offline .n-gauges,
.n-card.is-offline .n-net,
.n-card.is-offline .n-routes {
  opacity: .6;
}
.n-card.is-blocked {
  --tone: var(--sb-danger);
  --tone-soft: rgba(251, 113, 133, .12);
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
  width: 30px;
  height: 30px;
  border-radius: var(--sb-radius-sm);
  background: var(--sb-surface-3);
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  font-size: 12px;
  font-weight: 700;
}
.n-avatar.is-flag {
  font-size: 16px;
  font-weight: 400;
  background: rgba(148, 163, 184, .06);
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
  font-size: 13.5px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.n-meta {
  display: block;
  margin-top: 2px;
  color: var(--sb-muted);
  font-size: 10.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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
  width: 26px;
  height: 26px;
  border-radius: 7px;
}

.n-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
}
.n-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 100%;
  min-width: 0;
  padding: 1.5px 7px;
  border-radius: 6px;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
  color: var(--sb-text-2);
  font-size: 10.5px;
  line-height: 1.6;
}
.n-chip-txt {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.n-chip .el-icon {
  flex-shrink: 0;
  font-size: 10px;
  opacity: .7;
}
.n-chip.is-address {
  border-color: rgba(56, 189, 248, .26);
  background: var(--sb-accent-soft);
  color: var(--sb-accent);
  cursor: pointer;
  text-align: left;
  transition: border-color .15s;
}
.n-chip.is-address:hover { border-color: var(--sb-accent); }
.n-chip.is-violet {
  border-color: rgba(168, 85, 247, .3);
  background: rgba(168, 85, 247, .1);
  color: #d8b4fe;
}
.n-chip.is-danger {
  border-color: rgba(251, 113, 133, .35);
  background: rgba(251, 113, 133, .1);
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
  font-size: 10px;
}
.n-g-top b {
  color: var(--sb-text-2);
  font-size: 10.5px;
  font-weight: 600;
}
.n-g-top b i {
  margin-left: 1px;
  color: var(--sb-muted);
  font-size: 9px;
  font-style: normal;
}
.n-g-track {
  height: 3px;
  margin-top: 4px;
  overflow: hidden;
  border-radius: 2px;
  background: rgba(148, 163, 184, .16);
}
.n-g-track i {
  display: block;
  height: 100%;
  border-radius: 2px;
  transition: width .4s ease, background-color .3s ease;
}

.n-net {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 6px 10px;
  border-radius: var(--sb-radius-sm);
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
  font-size: 11px;
}
.n-net > span {
  flex-shrink: 0;
  white-space: nowrap;
}
.n-net .is-down {
  color: var(--sb-success);
  font-weight: 600;
}
.n-net .is-up {
  color: var(--sb-accent);
  font-weight: 600;
}
.n-net .is-total {
  color: var(--sb-muted);
  font-size: 10.5px;
  cursor: default;
}
.n-conn {
  margin-left: auto;
  padding: 0 7px;
  border-radius: 999px;
  background: rgba(148, 163, 184, .1);
  color: var(--sb-muted);
  font-size: 10px;
  line-height: 16px;
}
.n-conn.is-active {
  background: rgba(52, 211, 153, .12);
  color: var(--sb-success);
}

.n-routes {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 7px 10px;
  border-radius: var(--sb-radius-sm);
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
}
.n-routes.is-blocked {
  border-color: rgba(251, 113, 133, .3);
  background: rgba(251, 113, 133, .06);
}
.n-route-line {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 10.5px;
}
.n-dir {
  flex-shrink: 0;
  padding: 0 4px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 700;
  line-height: 15px;
}
.n-dir.is-in {
  background: rgba(56, 189, 248, .1);
  color: var(--sb-accent);
}
.n-dir.is-out {
  background: rgba(168, 85, 247, .1);
  color: #c084fc;
}
.n-lat {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  min-width: 0;
  max-width: 100%;
  padding: 1px 6px;
  border-radius: 6px;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-3);
  color: var(--sb-text-2);
  white-space: nowrap;
  overflow: hidden;
  cursor: default;
}
.n-lat b.is-telecom { color: #60a5fa; }
.n-lat b.is-unicom { color: var(--sb-warning); }
.n-lat b.is-mobile { color: var(--sb-success); }
.n-lat em {
  font-size: 9.5px;
  font-style: normal;
  opacity: .75;
}
.n-lat.is-idle {
  border-style: dashed;
  color: var(--sb-muted);
  background: transparent;
}
.n-lat.ping-status-fast {
  border-color: rgba(52, 211, 153, .28);
  color: var(--sb-success);
}
.n-lat.ping-status-medium {
  border-color: rgba(251, 191, 36, .3);
  color: var(--sb-warning);
}
.n-lat.ping-status-slow {
  border-color: rgba(251, 146, 60, .32);
  color: #fb923c;
}
.n-lat.ping-status-timeout {
  border-color: rgba(251, 113, 133, .4);
  background: rgba(251, 113, 133, .08);
  color: var(--sb-danger);
}
.n-link {
  flex-shrink: 0;
  margin-left: auto;
  padding: 0;
  border: none;
  background: none;
  color: var(--sb-accent);
  font-size: 10.5px;
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
  padding-top: 8px;
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
  font-size: 24px;
  font-weight: 400;
  background: var(--sb-surface-3);
}
.srv-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  line-height: 16px;
}
.srv-status.is-online {
  background: rgba(52, 211, 153, .12);
  color: var(--sb-success);
}
.srv-status.is-offline {
  background: rgba(148, 163, 184, .1);
  color: var(--sb-muted);
}
.srv-status.is-blocked {
  background: rgba(251, 113, 133, .14);
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
  padding: 12px 14px;
  background: rgba(0, 0, 0, 0.6);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
  color: var(--sb-accent);
  font-size: 12px;
  line-height: 1.5;
  word-break: break-all;
  white-space: pre-wrap;
  user-select: all;
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
  border: 1px solid rgba(148, 163, 184, .12);
  border-radius: 14px;
  background: rgba(2, 6, 15, .24);
  transition: border-color .2s;
}
.dlg-block.is-on { border-color: rgba(168, 85, 247, .3); }
.dlg-block-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}
.dlg-block-head .dlg-help { margin-top: 3px; }
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
  padding: 6px 8px 6px 10px;
  border-radius: 10px;
  background: rgba(148, 163, 184, .05);
}
.dlg-ip-kind {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 5px;
  font-size: 10.5px;
  font-weight: 700;
}
.dlg-ip-kind.is-v4 { background: rgba(52, 211, 153, .14); color: #6ee7b7; }
.dlg-ip-kind.is-v6 { background: rgba(251, 191, 36, .14); color: #fcd34d; }
.dlg-ip-addr {
  min-width: 140px;
  color: var(--sb-text);
  font-size: 12.5px;
  word-break: break-all;
}
.dlg-ip-alias { flex: 1; }
.dlg-empty {
  padding: 12px;
  border-radius: 10px;
  background: rgba(148, 163, 184, .05);
  color: var(--sb-muted);
  font-size: 12px;
  text-align: center;
}
.dlg-foot {
  display: flex;
  align-items: center;
  gap: 8px;
}
.dlg-foot-hint {
  margin-right: auto;
  color: var(--sb-muted);
  font-size: 12px;
}
@media (max-width: 700px) {
  .dlg-grid, .dlg-facts { grid-template-columns: minmax(0, 1fr); }
  .dlg-facts > div + div { border-left: none; border-top: 1px solid rgba(148, 163, 184, .1); }
  .dlg-foot-hint { display: none; }
}

/* ── MTR 排障弹窗 ───────────────────────────────────────── */
.mtr-ctrl {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 12px;
  margin-bottom: 16px;
  padding: 14px 16px;
  border: 1px solid rgba(148, 163, 184, .12);
  border-radius: 14px;
  background: rgba(2, 6, 15, .24);
}
.mtr-ctrl-field {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.mtr-ctrl-field.is-grow { flex: 1; min-width: 200px; }
.mtr-ctrl-label {
  color: var(--sb-muted);
  font-size: 11.5px;
  font-weight: 600;
}
.mtr-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 44px 16px 40px;
  text-align: center;
}
.mtr-radar {
  position: relative;
  display: grid;
  place-items: center;
  width: 64px;
  height: 64px;
  margin-bottom: 6px;
  border-radius: 50%;
  background: rgba(56, 189, 248, .1);
  color: var(--sb-accent);
}
.mtr-radar::after {
  content: '';
  position: absolute;
  inset: -6px;
  border: 1px solid rgba(56, 189, 248, .35);
  border-radius: 50%;
  animation: mtrRipple 1.8s ease-out infinite;
}
.mtr-radar.is-idle { background: rgba(148, 163, 184, .08); color: var(--sb-muted); }
.mtr-radar.is-idle::after { animation: none; border-color: rgba(148, 163, 184, .15); }
@keyframes mtrRipple {
  0% { transform: scale(.85); opacity: 1; }
  100% { transform: scale(1.35); opacity: 0; }
}
.mtr-state-title {
  color: var(--sb-text);
  font-size: 14px;
  font-weight: 600;
}
.mtr-state-sub {
  max-width: 420px;
  color: var(--sb-muted);
  font-size: 12px;
  line-height: 1.6;
}
.mtr-stats {
  display: grid;
  grid-template-columns: 1.6fr repeat(3, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.mtr-stat {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  padding: 12px 14px;
  border: 1px solid rgba(148, 163, 184, .1);
  border-radius: 12px;
  background: rgba(148, 163, 184, .04);
}
.mtr-stat > span { color: var(--sb-muted); font-size: 11px; }
.mtr-stat b {
  overflow: hidden;
  color: var(--sb-text);
  font-size: 20px;
  font-weight: 700;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.mtr-stat.is-target b { font-size: 15px; line-height: 27px; }
.mtr-stat b small { margin-left: 2px; color: var(--sb-muted); font-size: 11px; font-weight: 500; }
.mtr-stat b em { font-style: normal; color: var(--sb-success); }
.mtr-stat b em.is-bad { color: var(--sb-danger); }
.mtr-stat > small { color: var(--sb-accent); font-size: 11.5px; }
.mtr-verdict {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  margin-bottom: 14px;
  padding: 10px 14px;
  border: 1px solid rgba(52, 211, 153, .25);
  border-radius: 12px;
  background: rgba(52, 211, 153, .06);
  color: var(--sb-success);
  font-size: 12.5px;
  line-height: 1.6;
}
.mtr-verdict .el-icon { margin-top: 2px; flex-shrink: 0; }
.mtr-verdict.is-bad {
  border-color: rgba(244, 63, 94, .3);
  background: rgba(244, 63, 94, .07);
  color: var(--sb-danger);
}
.mtr-hops {
  overflow: hidden;
  border: 1px solid rgba(148, 163, 184, .12);
  border-radius: 14px;
}
.mtr-hop {
  display: grid;
  grid-template-columns: 40px minmax(0, 2.2fr) minmax(130px, 1.3fr) 70px 110px 64px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
  font-size: 12.5px;
}
.mtr-hop + .mtr-hop { border-top: 1px solid rgba(148, 163, 184, .08); }
.mtr-hop.is-head {
  background: rgba(148, 163, 184, .06);
  color: var(--sb-muted);
  font-size: 11px;
  font-weight: 600;
}
.mtr-hop.is-drop { background: rgba(244, 63, 94, .05); box-shadow: inset 3px 0 0 var(--sb-danger); }
.mtr-hop.is-limit { box-shadow: inset 3px 0 0 rgba(251, 191, 36, .7); }
.mtr-hop .is-num { text-align: right; }
.mtr-hop .is-muted { color: var(--sb-muted); }
.mtr-hop-no {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 8px;
  background: rgba(148, 163, 184, .08);
  color: var(--sb-text-2);
  font-size: 11px;
}
.mtr-hop-host {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.mtr-hop-host > span { color: var(--sb-text); font-weight: 600; }
.mtr-hop-host small {
  overflow: hidden;
  color: var(--sb-muted);
  font-size: 11px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.mtr-hop-host small.is-loc { color: var(--sb-accent); }
.mtr-hop-loss { display: flex; flex-direction: column; gap: 5px; }
.mtr-loss-top { display: flex; align-items: center; gap: 6px; }
.mtr-loss-track {
  height: 4px;
  overflow: hidden;
  border-radius: 2px;
  background: rgba(148, 163, 184, .12);
}
.mtr-loss-track i { display: block; height: 100%; border-radius: 2px; }
.mtr-tag {
  padding: 0 6px;
  border-radius: 5px;
  font-size: 10.5px;
  font-weight: 600;
  line-height: 17px;
  white-space: nowrap;
  cursor: help;
}
.mtr-tag.is-limit { background: rgba(251, 191, 36, .14); color: #fcd34d; }
.mtr-tag.is-drop { background: rgba(244, 63, 94, .16); color: #fda4af; }
.mtr-raw { margin-top: 12px; }
.mtr-raw-terminal {
  margin: 8px 0 0;
  max-height: 240px;
  padding: 12px 14px;
  overflow-y: auto;
  border: 1px solid rgba(148, 163, 184, .12);
  border-radius: 12px;
  background: var(--sb-sidebar);
  color: var(--sb-accent);
  font-size: 11px;
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-all;
}
@media (max-width: 860px) {
  .mtr-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .mtr-hop { grid-template-columns: 32px minmax(0, 1fr) 120px 64px; }
  .mtr-hop > :nth-child(5), .mtr-hop > :nth-child(6) { display: none; }
}

:deep(.el-popper) {
  max-width: 280px !important;
  white-space: pre-line !important;
  line-height: 1.45 !important;
  font-size: 11px !important;
}
</style>
