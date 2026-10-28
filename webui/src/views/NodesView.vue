<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Connection,
  CopyDocument,
  Cpu,
  Delete,
  Edit,
  Grid,
  Lock,
  Management,
  Monitor,
  MoreFilled,
  Odometer,
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
const scanners = ref({})
const togglingScanner = reactive({})

// 视图模式：cards (默认精美卡片) 或 table (传统表格)
const viewMode = ref(window.localStorage.getItem(VIEW_MODE_KEY) || 'cards')
function switchViewMode(mode) {
  viewMode.value = mode
  window.localStorage.setItem(VIEW_MODE_KEY, mode)
}

// 排序与筛选
const keyword = ref('')
const statusFilter = ref('')
const sortBy = ref('last_seen') // last_seen, name, conn, cpu, mem

// 令牌与接入弹窗
const tokenDialog = ref(false)
const token = ref('')
const expiresAt = ref('')
const lifetime = ref(900)
const masterPublicKey = ref('')
const masterHost = ref('')
const agentPort = ref(19994)

// 编辑弹窗
const editDialog = ref(false)
const editNode = ref(null)
const editForm = ref({
  name: '',
  client_address: '',
  scanner_protection: false,
  ddns_enabled: false,
  ddns_record_id: '',
  ddns_domain: '',
})
const cfRecords = ref([])
const cfLoading = ref(false)
const syncingDDNS = ref(false)
const editSaving = ref(false)

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
    return includesText(
      [node.name, node.client_address, node.os, node.architecture, node.agent_version, node.sing_box_version],
      keyword.value,
    )
  })

  // 排序
  list = [...list].sort((a, b) => {
    if (sortBy.value === 'name') return a.name.localeCompare(b.name)
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
    // 默认按在线优先及最后活跃时间倒序
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

// 统计概览
const stats = computed(() => {
  const total = appState.nodes.length
  const online = appState.nodes.filter((n) => n.online).length
  const offline = total - online
  let activeConns = 0
  let totalDownRate = 0
  let totalUpRate = 0
  for (const node of appState.nodes) {
    const snap = live.value.get(node.id)
    if (snap) {
      activeConns += Number(snap.connection_count || 0)
      if (snap.has_rates) {
        totalDownRate += Number(snap.received_rate || 0)
        totalUpRate += Number(snap.sent_rate || 0)
      }
    }
  }
  return {
    total,
    online,
    offline,
    activeConns,
    totalDownRate,
    totalUpRate,
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
  loadScanners().catch(() => {})
}

async function loadScanners() {
  if (!isAdmin.value) return
  try {
    const res = await api('/firewall/rules').catch(() => ({ nodes: [] }))
    if (res?.nodes) {
      const map = {}
      for (const n of res.nodes) {
        if (n && n.node_id) {
          map[n.node_id] = Boolean(n.scanner_protection)
        }
      }
      scanners.value = map
    }
  } catch (_) {}
}

async function toggleScannerDirect(node) {
  const current = Boolean(scanners.value[node.id])
  togglingScanner[node.id] = true
  try {
    const updated = await post(`/nodes/${node.id}/firewall/scanners`, { enabled: !current })
    scanners.value[node.id] = Boolean(updated.scanner_protection)
    ElMessage.success(`“${node.name}”测绘防御已${!current ? '开启' : '关闭'}`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '切换失败')
  } finally {
    togglingScanner[node.id] = false
  }
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
    st.gfw_status === 'blocked' ||
    st.telecom_route === 'GFW 阻断' ||
    st.unicom_route === 'GFW 阻断' ||
    st.mobile_route === 'GFW 阻断'
  )
}

function getLatencyTooltip(st, isp) {
  if (!st) return ''
  if (isNodeGFWBlocked(st)) {
    return '国内所有探针 TCP 握手均超时或被丢弃 (疑似被 GFW 阻断，公网端口国内不可达)'
  }
  if (isp === 'mobile') return '中国移动: ' + (st.mobile_route || '标准直连')
  if (isp === 'unicom') return '中国联通: ' + (st.unicom_route || '标准直连')
  if (isp === 'telecom') return '中国电信: ' + (st.telecom_route || '标准直连')
  return ''
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

function onRecordSelected(recordId) {
  const rec = cfRecords.value.find((r) => r.id === recordId)
  if (rec) {
    editForm.value.ddns_domain = rec.name
    if (!editForm.value.client_address || /^[0-9.:]+$/.test(editForm.value.client_address)) {
      editForm.value.client_address = rec.name
    }
  }
}

async function syncDDNS(node) {
  syncingDDNS.value = true
  try {
    const res = await post(`/nodes/${node.id}/ddns/sync`, {})
    ElMessage.success(`DDNS 解析已成功同步，当前解析 IP: ${res.ip}`)
    await load(true)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : 'DDNS 同步失败')
  } finally {
    syncingDDNS.value = false
  }
}

async function openEdit(node) {
  editNode.value = node
  let isProtected = Boolean(scanners.value[node.id])
  if (scanners.value[node.id] === undefined && node.online && isAdmin.value) {
    const res = await api(`/nodes/${node.id}/firewall/rules`).catch(() => null)
    if (res?.scanner_protection !== undefined) {
      isProtected = Boolean(res.scanner_protection)
      scanners.value[node.id] = isProtected
    }
  }
  editForm.value = {
    name: node.name,
    client_address: node.client_address || '',
    scanner_protection: isProtected,
    ddns_enabled: Boolean(node.ddns_enabled),
    ddns_record_id: node.ddns_record_id || '',
    ddns_domain: node.ddns_domain || '',
  }
  loadCloudflareRecords()
  editDialog.value = true
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
    })
    if (
      isAdmin.value &&
      editNode.value.online &&
      editForm.value.scanner_protection !== Boolean(scanners.value[editNode.value.id])
    ) {
      const updated = await post(`/nodes/${editNode.value.id}/firewall/scanners`, {
        enabled: editForm.value.scanner_protection,
      })
      scanners.value[editNode.value.id] = Boolean(updated.scanner_protection)
    }
    ElMessage.success('服务器信息已保存')
    editDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器信息保存失败')
  } finally {
    editSaving.value = false
  }
}

function handleCardCommand(node, command) {
  if (command === 'upgradeAgent') upgradeAgent(node)
  else if (command === 'upgradeSingBox') upgradeSingBox(node)
  else if (command === 'revoke') revoke(node)
}

function isValidRoute(route) {
  if (!route) return false
  const trimmed = route.trim()
  return trimmed !== '' && trimmed !== '未知' && trimmed !== '不可达' && trimmed !== 'GFW 阻断'
}

function isPremiumRoute(route) {
  if (!route) return false
  return route.includes('CN2') || route.includes('9929') || route.includes('CMIN2')
}

function hasRecognizedRoutes(st) {
  if (!st) return false
  return isValidRoute(st.telecom_route) || isValidRoute(st.unicom_route) || isValidRoute(st.mobile_route)
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
      <!-- 汇总状态指示栏 -->
      <div class="server-overview-bar">
        <div class="overview-stat-group">
          <div class="overview-pill">
            <span class="pill-label">全部服务器</span>
            <span class="pill-value font-mono">{{ stats.total }}</span>
          </div>
          <div class="overview-pill pill-online">
            <span class="status-indicator online pulse-sm" />
            <span class="pill-label">在线</span>
            <span class="pill-value font-mono">{{ stats.online }}</span>
          </div>
          <div v-if="stats.offline > 0" class="overview-pill pill-offline">
            <span class="status-indicator offline" />
            <span class="pill-label">离线</span>
            <span class="pill-value font-mono">{{ stats.offline }}</span>
          </div>
          <div class="overview-pill">
            <el-icon><Connection /></el-icon>
            <span class="pill-label">活跃连接</span>
            <span class="pill-value font-mono highlight">{{ stats.activeConns }}</span>
          </div>
        </div>

        <div class="overview-rates font-mono">
          <span class="rate-item down">
            <span class="rate-arrow">↓</span>
            {{ formatBytes(stats.totalDownRate, '/s') }}
          </span>
          <span class="rate-sep">/</span>
          <span class="rate-item up">
            <span class="rate-arrow">↑</span>
            {{ formatBytes(stats.totalUpRate, '/s') }}
          </span>
        </div>
      </div>

      <!-- 工具栏：搜索、状态、排序、视图切换 -->
      <div class="search-toolbar modern-toolbar">
        <div class="toolbar-left">
          <el-input
            v-model="keyword"
            clearable
            :prefix-icon="Search"
            placeholder="搜索名称、地址、系统或版本"
            style="width: 270px"
          />
          <el-select v-model="statusFilter" clearable placeholder="全部状态" style="width: 120px">
            <el-option label="在线" value="online" />
            <el-option label="离线" value="offline" />
          </el-select>
          <el-select v-model="sortBy" placeholder="排序方式" style="width: 140px">
            <el-option label="最后活跃" value="last_seen" />
            <el-option label="服务器名称" value="name" />
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

      <!-- 等待确认的服务器（Pending） -->
      <template v-if="pending.length">
        <div class="section-title">
          <span>等待确认的服务器</span>
          <el-tag size="small" type="warning" effect="dark" style="margin-left: 8px">{{ pending.length }}</el-tag>
        </div>
        <div class="pending-grid" style="margin-bottom: 24px">
          <div v-for="item in filteredPending" :key="item.id" class="pending-card">
            <div class="pending-header">
              <div class="pending-title">
                <span class="status-indicator pending pulse-sm" />
                <strong>{{ item.node_name }}</strong>
              </div>
              <el-button type="primary" size="small" @click="approve(item)">批准接入</el-button>
            </div>
            <div class="pending-body">
              <div class="pending-line">
                <span class="pending-label">支持功能:</span>
                <span class="pending-val">{{ item.capabilities || '基础代理转发' }}</span>
              </div>
              <div class="pending-line">
                <span class="pending-label">请求时间:</span>
                <span class="pending-val">{{ formatDateTime(item.created_at) }}</span>
              </div>
            </div>
          </div>
        </div>
      </template>

      <!-- 已接入服务器区域 -->
      <div class="section-header-row">
        <div class="section-title">
          已接入服务器
          <span class="count-tag font-mono">{{ filteredNodes.length }}</span>
        </div>
      </div>

      <!-- 视图 1：精美卡片网格 (Card View) -->
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
            <!-- 卡片顶边流光 -->
            <div
              class="node-card-glow"
              :class="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? 'glow-blocked' : 'glow-online') : 'glow-offline'"
            />

            <!-- 1. 卡片头部：状态、名称、国旗、系统、版本、操作 -->
            <div class="card-head">
              <div class="card-head-left">
                <div class="title-row">
                  <span
                    class="status-indicator"
                    :class="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? 'blocked' : 'online') : 'offline'"
                    :title="node.online ? (isNodeGFWBlocked(speedtests[node.id]) ? '国内探针全超时 (疑似被 GFW 阻断)' : '在线运行中') : '节点已离线'"
                  />
                  <span v-if="getNodeFlag(node.name)" class="flag-icon">{{ getNodeFlag(node.name) }}</span>
                  <span class="node-name" :title="node.name">{{ node.name }}</span>
                  <span v-if="isNodeGFWBlocked(speedtests[node.id])" class="badge-gfw-alert">
                    <span class="gfw-dot" /> 疑似被墙
                  </span>
                </div>
                <div class="sub-badge-row">
                  <span class="os-arch-tag">{{ node.os || 'linux' }} · {{ node.architecture || 'amd64' }}</span>
                  <span class="version-tag">v{{ node.agent_version || '—' }}</span>
                  <button
                    v-if="agentUpdateAvailable(node)"
                    class="upgrade-pill"
                    title="点击立即升级 Agent"
                    @click="upgradeAgent(node)"
                  >
                    ↑ 升级 Agent
                  </button>
                  <button
                    v-if="singBoxUpdateAvailable(node)"
                    class="upgrade-pill upgrade-pill-sub"
                    title="点击立即升级 sing-box"
                    @click="upgradeSingBox(node)"
                  >
                    ↑ sing-box
                  </button>
                </div>
              </div>

              <div class="card-head-right">
                <el-dropdown trigger="click" @command="handleCardCommand(node, $event)">
                  <button class="icon-more-btn" title="更多操作">
                    <el-icon><MoreFilled /></el-icon>
                  </button>
                  <template #dropdown>
                    <el-dropdown-menu>
                      <el-dropdown-item v-if="canWrite && node.online" command="speedtest" @click="runSpeedtest(node)">
                        <el-icon><Connection /></el-icon>线路测速
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

            <!-- 2. 连接地址终端微槽 -->
            <div class="card-address-capsule" @click="copyAddress(node.client_address)">
              <div class="address-prefix font-mono">&gt;</div>
              <span v-if="node.client_address" class="address-text font-mono" :title="'点击复制: ' + node.client_address">
                {{ node.client_address }}
              </span>
              <span v-else class="address-text address-empty">未配置连接地址</span>
              <span v-if="node.ddns_enabled" class="ddns-chip" :title="`DDNS 解析至: ${node.last_observed_ip || '检测中'}`">
                DDNS
              </span>
              <button v-if="node.client_address" class="address-copy-icon" title="复制连接地址">
                <el-icon><CopyDocument /></el-icon>
              </button>
            </div>

            <!-- 3. 核心硬件资源三微舱 (CPU / 内存 / 磁盘) -->
            <div class="hw-dash-container">
              <!-- CPU -->
              <div class="hw-mini-card">
                <div class="hw-card-top">
                  <span class="hw-card-title">
                    <span class="hw-dot cpu" />
                    CPU
                  </span>
                  <span
                    class="hw-card-percent font-mono"
                    :style="{ color: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0) }"
                  >
                    {{ node.online && metrics[node.id]?.host ? `${metrics[node.id].host.cpu_percent.toFixed(1)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-bar-track">
                  <div
                    class="hw-bar-thumb"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? Math.min(100, metrics[node.id].host.cpu_percent) : 0}%`,
                      backgroundColor: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0),
                    }"
                  />
                </div>
                <div class="hw-card-bottom font-mono">
                  <span v-if="metrics[node.id]?.host?.uptime_seconds">
                    已运行 {{ Math.floor(metrics[node.id].host.uptime_seconds / 86400) }} 天
                  </span>
                  <span v-else class="subtle">待上报</span>
                </div>
              </div>

              <!-- 内存 -->
              <div class="hw-mini-card">
                <div class="hw-card-top">
                  <span class="hw-card-title">
                    <span class="hw-dot mem" />
                    内存
                  </span>
                  <span
                    class="hw-card-percent font-mono"
                    :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total)) }"
                  >
                    {{ node.online && metrics[node.id]?.host?.memory_total ? `${getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-bar-track">
                  <div
                    class="hw-bar-thumb"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total) : 0}%`,
                      backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total)),
                    }"
                  />
                </div>
                <div class="hw-card-bottom font-mono">
                  <span v-if="node.online && metrics[node.id]?.host?.memory_total">
                    {{ formatResourceBytes(metrics[node.id].host.memory_used) }} / {{ formatResourceBytes(metrics[node.id].host.memory_total) }}
                  </span>
                  <span v-else class="subtle">待上报</span>
                </div>
              </div>

              <!-- 磁盘 -->
              <div class="hw-mini-card">
                <div class="hw-card-top">
                  <span class="hw-card-title">
                    <span class="hw-dot disk" />
                    硬盘
                  </span>
                  <span
                    class="hw-card-percent font-mono"
                    :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total)) }"
                  >
                    {{ node.online && metrics[node.id]?.host?.disk_total ? `${getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-bar-track">
                  <div
                    class="hw-bar-thumb"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total) : 0}%`,
                      backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total)),
                    }"
                  />
                </div>
                <div class="hw-card-bottom font-mono">
                  <span v-if="node.online && metrics[node.id]?.host?.disk_total">
                    {{ formatResourceBytes(metrics[node.id].host.disk_used) }} / {{ formatResourceBytes(metrics[node.id].host.disk_total) }}
                  </span>
                  <span v-else class="subtle">待上报</span>
                </div>
              </div>
            </div>

            <!-- 4. 实时网络吞吐与连接舱 -->
            <div class="net-telemetry-bay">
              <div class="net-left-stream">
                <div class="stream-speeds font-mono">
                  <div class="speed-unit down">
                    <span class="speed-arrow">↓</span>
                    <span class="speed-num">
                      {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).received_rate, '/s') : '0 B/s' }}
                    </span>
                  </div>
                  <div class="speed-unit up">
                    <span class="speed-arrow">↑</span>
                    <span class="speed-num">
                      {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).sent_rate, '/s') : '0 B/s' }}
                    </span>
                  </div>
                </div>
                <div class="stream-total font-mono">
                  <span class="total-lbl">累计:</span>
                  <span v-if="metrics[node.id]?.proxy" class="total-val">
                    ↓ {{ formatBytes(metrics[node.id].proxy.received_bytes) }} · ↑ {{ formatBytes(metrics[node.id].proxy.sent_bytes) }}
                  </span>
                  <span v-else class="total-val subtle">等待上报</span>
                </div>
              </div>

              <div class="net-right-pod">
                <div class="conn-pod" :class="{ 'has-conn': (live.get(node.id)?.connection_count || 0) > 0 }">
                  <div class="conn-pod-top">
                    <el-icon><Connection /></el-icon>
                    <span class="conn-digit font-mono">{{ live.get(node.id)?.connection_count ?? 0 }}</span>
                  </div>
                  <span class="conn-unit-lbl">活跃连接</span>
                </div>
              </div>
            </div>

            <!-- 5. 三网延迟与线路质量 -->
            <div class="latency-route-bay" :class="{ 'is-blocked': isNodeGFWBlocked(speedtests[node.id]) }">
              <div v-if="speedtests[node.id]" class="speedtest-inner">
                <!-- GFW 阻断醒目标幅 -->
                <div v-if="isNodeGFWBlocked(speedtests[node.id])" class="gfw-banner">
                  <span class="gfw-alert-flare" />
                  <div class="gfw-banner-text">
                    <span class="gfw-banner-main">长城防火墙阻断 (GFW Blocked)</span>
                    <span class="gfw-banner-note">{{ speedtests[node.id].domestic_source || '国内所有探针 TCP 握手全超时' }}</span>
                  </div>
                </div>

                <!-- 延迟药丸 -->
                <div class="latency-grid">
                  <el-tooltip :content="getLatencyTooltip(speedtests[node.id], 'mobile')">
                    <div class="isp-pill" :class="isNodeGFWBlocked(speedtests[node.id]) ? 'is-timeout' : getPingClass(speedtests[node.id].mobile_latency_ms ?? speedtests[node.id].mobile_ping_ms)">
                      <span class="isp-dot mobile-dot" />
                      <span class="isp-title">移动</span>
                      <span class="isp-ms font-mono">{{ isNodeGFWBlocked(speedtests[node.id]) ? '阻断' : getPingStr(speedtests[node.id].mobile_latency_ms ?? speedtests[node.id].mobile_ping_ms) }}</span>
                    </div>
                  </el-tooltip>

                  <el-tooltip :content="getLatencyTooltip(speedtests[node.id], 'unicom')">
                    <div class="isp-pill" :class="isNodeGFWBlocked(speedtests[node.id]) ? 'is-timeout' : getPingClass(speedtests[node.id].unicom_latency_ms ?? speedtests[node.id].unicom_ping_ms)">
                      <span class="isp-dot unicom-dot" />
                      <span class="isp-title">联通</span>
                      <span class="isp-ms font-mono">{{ isNodeGFWBlocked(speedtests[node.id]) ? '阻断' : getPingStr(speedtests[node.id].unicom_latency_ms ?? speedtests[node.id].unicom_ping_ms) }}</span>
                    </div>
                  </el-tooltip>

                  <el-tooltip :content="getLatencyTooltip(speedtests[node.id], 'telecom')">
                    <div class="isp-pill" :class="isNodeGFWBlocked(speedtests[node.id]) ? 'is-timeout' : getPingClass(speedtests[node.id].telecom_latency_ms ?? speedtests[node.id].telecom_ping_ms)">
                      <span class="isp-dot telecom-dot" />
                      <span class="isp-title">电信</span>
                      <span class="isp-ms font-mono">{{ isNodeGFWBlocked(speedtests[node.id]) ? '阻断' : getPingStr(speedtests[node.id].telecom_latency_ms ?? speedtests[node.id].telecom_ping_ms) }}</span>
                    </div>
                  </el-tooltip>
                </div>

                <!-- 线路标签与国内验证 -->
                <div v-if="isNodeGFWBlocked(speedtests[node.id])" class="routes-row">
                  <span class="route-tag tag-danger">🚨 疑似被墙 · 国内客户端不可达</span>
                </div>
                <div v-else-if="hasRecognizedRoutes(speedtests[node.id]) || speedtests[node.id].domestic_latency_ms" class="routes-row">
                  <span
                    v-if="isValidRoute(speedtests[node.id].telecom_route)"
                    :class="['route-tag', isPremiumRoute(speedtests[node.id].telecom_route) ? 'tag-gold' : 'tag-muted']"
                  >
                    {{ speedtests[node.id].telecom_route }}
                  </span>
                  <span
                    v-if="isValidRoute(speedtests[node.id].unicom_route)"
                    :class="['route-tag', isPremiumRoute(speedtests[node.id].unicom_route) ? 'tag-gold' : 'tag-muted']"
                  >
                    {{ speedtests[node.id].unicom_route }}
                  </span>
                  <span
                    v-if="isValidRoute(speedtests[node.id].mobile_route)"
                    :class="['route-tag', isPremiumRoute(speedtests[node.id].mobile_route) ? 'tag-gold' : 'tag-muted']"
                  >
                    {{ speedtests[node.id].mobile_route }}
                  </span>
                  <span
                    v-if="speedtests[node.id].domestic_latency_ms"
                    class="route-tag tag-verified font-mono"
                    :title="speedtests[node.id].domestic_source || '国内探针握手验证正常'"
                  >
                    ✓ 直连 {{ speedtests[node.id].domestic_latency_ms }}ms
                  </span>
                </div>
              </div>

              <div v-else class="speedtest-empty">
                <span class="subtle font-mono">未进行三网与 GFW 线路检测</span>
                <button
                  v-if="canWrite && node.online"
                  class="btn-inline-probe"
                  :disabled="Boolean(speedtesting[node.id])"
                  @click="runSpeedtest(node)"
                >
                  {{ speedtesting[node.id] ? '正在检测...' : '立即检测' }}
                </button>
              </div>
            </div>

            <!-- 6. 主动测绘防御精工条 -->
            <div class="defense-strip">
              <div class="defense-meta">
                <div class="shield-badge" :class="{ 'is-active': Boolean(scanners[node.id]) }">
                  <svg class="shield-svg" viewBox="0 0 24 24" fill="none" stroke="currentColor">
                    <path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>
                  </svg>
                </div>
                <div class="defense-copy">
                  <span class="defense-name">主动测绘防御</span>
                  <span class="defense-subtext">{{ scanners[node.id] ? '拦截 Shodan / Censys 探测' : '未开启公网防护' }}</span>
                </div>
              </div>
              <el-switch
                :model-value="Boolean(scanners[node.id])"
                :loading="Boolean(togglingScanner[node.id])"
                :disabled="!isAdmin || !node.online"
                size="small"
                active-text="开"
                inactive-text="关"
                inline-prompt
                @change="toggleScannerDirect(node)"
              />
            </div>

            <!-- 7. 卡片底栏 -->
            <div class="card-bottom-bar">
              <div class="bottom-last-seen font-mono">
                <span class="seen-label">活跃:</span>
                <span>{{ formatDateTime(node.last_seen_at, '从未') }}</span>
              </div>
              <div class="bottom-actions">
                <button
                  v-if="canWrite && node.online"
                  class="glass-act-btn"
                  :disabled="Boolean(speedtesting[node.id])"
                  @click="runSpeedtest(node)"
                >
                  <el-icon><Connection /></el-icon>
                  <span>{{ speedtesting[node.id] ? '检测中' : '测速' }}</span>
                </button>
                <button
                  v-if="canWrite"
                  class="glass-act-btn"
                  @click="openEdit(node)"
                >
                  <el-icon><Edit /></el-icon>
                  <span>配置</span>
                </button>
              </div>
            </div>
          </div>
        </div>

        <!-- 卡片分页条 -->
        <div v-if="filteredNodes.length > pageSize" class="cards-pagination-bar">
          <el-pagination
            v-model:current-page="currentPage"
            v-model:page-size="pageSize"
            :total="filteredNodes.length"
            :page-sizes="[12, 24, 36, 48]"
            layout="total, sizes, prev, pager, next"
            background
          />
        </div>
      </template>

      <!-- 视图 2：传统列表表格 (Table View) -->
      <template v-else>
        <div class="table-panel">
          <PagedTable :rows="filteredNodes" empty-text="尚未接入任何服务器">
            <el-table-column label="服务器 / 系统" min-width="190" show-overflow-tooltip>
              <template #default="{ row }">
                <div class="cell-main">
                  <span class="status-indicator" :class="row.online ? (isNodeGFWBlocked(speedtests[row.id]) ? 'blocked' : 'online') : 'offline'" />
                  <strong style="margin-left: 6px">{{ row.name }}</strong>
                </div>
                <div class="cell-sub">{{ row.os || '—' }} · {{ row.architecture || '—' }}</div>
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
                    <span class="hw-mini-name">硬盘:</span>
                    <span class="hw-mini-val">
                      {{ getResourcePercent(metrics[row.id].host.disk_used, metrics[row.id].host.disk_total) }}%
                    </span>
                  </div>
                </div>
                <span v-else class="subtle">{{ row.online ? '待上报' : '离线' }}</span>
              </template>
            </el-table-column>

            <el-table-column label="版本" width="180">
              <template #default="{ row }">
                <div class="cell-main">
                  {{ row.agent_version || '—' }}
                  <el-tag v-if="agentUpdateAvailable(row)" type="warning" size="small" style="margin-left: 6px">可升级</el-tag>
                </div>
                <div class="cell-sub">
                  sing-box {{ row.sing_box_version || '—' }}
                  <el-button v-if="isAdmin && singBoxUpdateAvailable(row)" link type="primary" style="margin-left: 6px" @click="upgradeSingBox(row)">升级</el-button>
                  <el-tag v-else-if="singBoxUpdateAvailable(row)" type="warning" size="small" style="margin-left: 6px">可升级</el-tag>
                </div>
              </template>
            </el-table-column>

            <el-table-column label="客户端连接地址" min-width="180" show-overflow-tooltip>
              <template #default="{ row }">
                <div class="cell-main">
                  <span v-if="row.client_address" class="font-mono">{{ row.client_address }}</span>
                  <el-tag v-else type="warning">未配置</el-tag>
                  <el-tooltip v-if="row.ddns_enabled" :content="`Cloudflare DDNS 自动同步 (${row.ddns_domain || row.client_address})，最新IP: ${row.last_observed_ip || '等待检测'}`">
                    <el-tag size="small" type="success" style="margin-left: 6px">DDNS</el-tag>
                  </el-tooltip>
                </div>
              </template>
            </el-table-column>

            <el-table-column label="主动测绘防御" width="130" align="center">
              <template #default="{ row }">
                <el-tooltip :content="scanners[row.id] ? '已阻断 Shodan、Censys 等公网扫描引擎探测' : '未开启测绘防御，点击快速开启'">
                  <el-switch
                    :model-value="Boolean(scanners[row.id])"
                    :loading="Boolean(togglingScanner[row.id])"
                    :disabled="!isAdmin || !row.online"
                    active-text="已开启"
                    inactive-text="未开启"
                    inline-prompt
                    @change="toggleScannerDirect(row)"
                  />
                </el-tooltip>
              </template>
            </el-table-column>

            <el-table-column label="三网延迟 / 线路" min-width="260">
              <template #default="{ row }">
                <template v-if="speedtests[row.id]">
                  <div v-if="isNodeGFWBlocked(speedtests[row.id])" class="cell-main">
                    <el-tag type="danger" size="small" effect="dark" style="margin-bottom: 2px">🚨 疑似被 GFW 阻断 (国内探针全超时)</el-tag>
                    <div class="cell-sub font-mono" style="color: #fb7185">国内入站不可达 · {{ speedtests[row.id].domestic_source || 'TCP握手超时' }}</div>
                  </div>
                  <template v-else>
                    <div class="cell-main speedtest-badges">
                      <el-tooltip :content="'中国移动: ' + (speedtests[row.id].mobile_route || '标准直连')">
                        <span class="ping-pill" :class="getPingClass(speedtests[row.id].mobile_latency_ms ?? speedtests[row.id].mobile_ping_ms)">
                          移 {{ getPingStr(speedtests[row.id].mobile_latency_ms ?? speedtests[row.id].mobile_ping_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip :content="'中国联通: ' + (speedtests[row.id].unicom_route || '标准直连')">
                        <span class="ping-pill" :class="getPingClass(speedtests[row.id].unicom_latency_ms ?? speedtests[row.id].unicom_ping_ms)">
                          联 {{ getPingStr(speedtests[row.id].unicom_latency_ms ?? speedtests[row.id].unicom_ping_ms) }}
                        </span>
                      </el-tooltip>
                      <el-tooltip :content="'中国电信: ' + (speedtests[row.id].telecom_route || '标准直连')">
                        <span class="ping-pill" :class="getPingClass(speedtests[row.id].telecom_latency_ms ?? speedtests[row.id].telecom_ping_ms)">
                          电 {{ getPingStr(speedtests[row.id].telecom_latency_ms ?? speedtests[row.id].telecom_ping_ms) }}
                        </span>
                      </el-tooltip>
                    </div>
                    <div v-if="hasRecognizedRoutes(speedtests[row.id]) || speedtests[row.id].domestic_latency_ms" class="routes-row">
                      <span v-if="isValidRoute(speedtests[row.id].telecom_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].telecom_route) ? 'route-premium' : 'route-normal']">
                        {{ speedtests[row.id].telecom_route }}
                      </span>
                      <span v-if="isValidRoute(speedtests[row.id].unicom_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].unicom_route) ? 'route-premium' : 'route-normal']">
                        {{ speedtests[row.id].unicom_route }}
                      </span>
                      <span v-if="isValidRoute(speedtests[row.id].mobile_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].mobile_route) ? 'route-premium' : 'route-normal']">
                        {{ speedtests[row.id].mobile_route }}
                      </span>
                      <span v-if="speedtests[row.id].domestic_latency_ms" class="route-badge route-verified font-mono">
                        ✓ 直连 {{ speedtests[row.id].domestic_latency_ms }}ms
                      </span>
                    </div>
                  </template>
                </template>
                <span v-else class="subtle">未检测</span>
              </template>
            </el-table-column>

            <el-table-column label="实时 / 累计流量" min-width="196" show-overflow-tooltip>
              <template #default="{ row }">
                <div class="cell-main font-mono">
                  <template v-if="live.get(row.id)?.has_rates">↓ {{ formatBytes(live.get(row.id).received_rate, '/s') }} · ↑ {{ formatBytes(live.get(row.id).sent_rate, '/s') }}</template>
                  <span v-else class="subtle">{{ row.online ? '等待上报' : '离线' }}</span>
                </div>
                <div class="cell-sub font-mono">
                  <template v-if="metrics[row.id]?.proxy">↓ {{ formatBytes(metrics[row.id].proxy.received_bytes) }} · ↑ {{ formatBytes(metrics[row.id].proxy.sent_bytes) }}</template>
                  <template v-else>等待上报</template>
                </div>
              </template>
            </el-table-column>

            <el-table-column label="连接数" width="82" align="center">
              <template #default="{ row }">{{ live.get(row.id)?.connection_count ?? '—' }}</template>
            </el-table-column>

            <el-table-column label="最后在线" width="152">
              <template #default="{ row }">{{ formatDateTime(row.last_seen_at, '从未') }}</template>
            </el-table-column>

            <el-table-column label="操作" width="220" fixed="right" class-name="action-column">
              <template #default="{ row }">
                <el-button v-if="canWrite && row.online" link :icon="Connection" :loading="Boolean(speedtesting[row.id])" @click="runSpeedtest(row)">线路检测</el-button>
                <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
                <el-button v-if="isAdmin && agentUpdateAvailable(row)" link type="primary" :icon="Top" @click="upgradeAgent(row)">升级</el-button>
                <el-button v-if="isAdmin" link type="danger" :icon="RemoveFilled" @click="revoke(row)">移除</el-button>
              </template>
            </el-table-column>
          </PagedTable>
        </div>
      </template>
    </main>

    <!-- 生成令牌 / 接入命令弹窗 -->
    <el-dialog v-model="tokenDialog" title="服务器接入信息" width="720px">
      <el-alert title="此命令只显示一次，请立即复制。令牌一次性使用，过期后需重新生成。" type="warning" show-icon :closable="false" />
      <el-form label-position="top" style="margin-top: 16px">
        <el-form-item label="Master 地址（agent 拨号用的 主机:端口）">
          <el-input v-model="masterHost" placeholder="control.example.com">
            <template #append>:{{ agentPort }}</template>
          </el-input>
          <p class="subtle">默认按当前控制台域名填入。若 agent 需通过其他域名或公网 IP 连接，请在此改成正确的主机名，本浏览器会记住该地址，下次生成令牌自动带入。</p>
        </el-form-item>
        <el-form-item label="在目标服务器上以 root 执行">
          <el-input :model-value="installCommand" readonly type="textarea" :rows="6" class="font-mono" />
        </el-form-item>
      </el-form>
      <p class="subtle">令牌有效期至：{{ formatDateTime(expiresAt) }}。命令执行后该服务器会出现在“等待确认的服务器”，需在此页面点“接入”批准。</p>
      <template #footer>
        <el-button @click="tokenDialog = false">完成</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyInstallCommand">复制命令</el-button>
      </template>
    </el-dialog>

    <!-- 编辑服务器弹窗 -->
    <el-dialog v-model="editDialog" title="编辑服务器" width="540px">
      <el-form label-position="top">
        <el-form-item label="服务器名称" required>
          <el-input v-model="editForm.name" maxlength="128" placeholder="例如：香港节点 01" />
        </el-form-item>
        <el-form-item label="客户端连接域名或 IP 地址">
          <el-input v-model="editForm.client_address" placeholder="例如：proxy.example.com 或 203.0.113.10" @keyup.enter="saveNode" />
        </el-form-item>
        <el-form-item label="主动网络测绘防御">
          <div class="scanner-edit-box">
            <div class="scanner-edit-text">
              <div class="scanner-edit-heading">阻断公网扫描与特征探测</div>
              <div class="subtle">自动屏蔽 Shodan、Censys 等公网搜索引擎的测绘扫描，保护代理端口不被嗅探发现</div>
            </div>
            <el-switch
              v-model="editForm.scanner_protection"
              :disabled="!isAdmin || !editNode?.online"
              active-text="已开启"
              inactive-text="未开启"
              inline-prompt
            />
          </div>
        </el-form-item>
        <el-form-item label="动态域名解析 (Cloudflare DDNS)">
          <div class="scanner-edit-box" style="margin-bottom: 8px;">
            <div class="scanner-edit-text">
              <div class="scanner-edit-heading">NAT 机器出口 IP 变动自动同步</div>
              <div class="subtle">当此服务器 IP 发生变动并重新连接 Master 时，自动更新 Cloudflare 上的域名解析记录</div>
            </div>
            <div style="display: flex; align-items: center; gap: 8px;">
              <el-button
                v-if="editForm.ddns_enabled && editNode?.ddns_enabled && editNode?.online"
                size="small"
                type="primary"
                link
                :loading="syncingDDNS"
                @click="syncDDNS(editNode)"
              >
                立即同步
              </el-button>
              <el-switch
                v-model="editForm.ddns_enabled"
                active-text="已开启"
                inactive-text="未开启"
                inline-prompt
              />
            </div>
          </div>
          <div v-if="editForm.ddns_enabled" style="width: 100%; margin-top: 6px;">
            <el-select
              v-model="editForm.ddns_record_id"
              placeholder="选择绑定的 Cloudflare A / AAAA 记录"
              style="width: 100%;"
              :loading="cfLoading"
              no-data-text="未找到可用记录（请先在「域名解析」中添加 A 记录）"
              @change="onRecordSelected"
            >
              <el-option
                v-if="editForm.ddns_record_id && !cfRecords.some(r => r.id === editForm.ddns_record_id)"
                :key="editForm.ddns_record_id"
                :label="`${editForm.ddns_domain || '已绑定记录'} (当前绑定)`"
                :value="editForm.ddns_record_id"
              />
              <el-option
                v-for="item in cfRecords"
                :key="item.id"
                :label="`${item.name} (${item.type} ➔ ${item.content})`"
                :value="item.id"
              />
            </el-select>
          </div>
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

<style scoped>
/* ── 顶部统计横幅 ───────────────────────────────────────── */
.server-overview-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  padding: 10px 16px;
  background: linear-gradient(180deg, rgba(17, 24, 39, 0.7) 0%, rgba(11, 16, 27, 0.85) 100%);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: var(--sb-radius);
  margin-bottom: 14px;
  backdrop-filter: blur(12px);
  box-shadow: 0 4px 16px -2px rgba(0, 0, 0, 0.4);
}
.overview-stat-group {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.overview-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: rgba(255, 255, 255, 0.035);
  border: 1px solid rgba(255, 255, 255, 0.07);
  border-radius: 20px;
  font-size: 12px;
  color: var(--sb-text-2);
}
.overview-pill .pill-value {
  font-weight: 600;
  color: var(--sb-text);
}
.overview-pill .pill-value.highlight {
  color: var(--sb-accent);
}
.overview-pill.pill-online {
  border-color: rgba(52, 211, 153, 0.25);
  background: rgba(52, 211, 153, 0.08);
}
.overview-pill.pill-offline {
  border-color: rgba(251, 113, 133, 0.25);
  background: rgba(251, 113, 133, 0.08);
}
.overview-rates {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  padding: 4px 10px;
  background: rgba(0, 0, 0, 0.35);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
}
.rate-item.down { color: #34d399; font-weight: 600; }
.rate-item.up { color: #38bdf8; font-weight: 600; }
.rate-sep { color: var(--sb-muted); opacity: 0.5; }
.rate-arrow { font-size: 11px; margin-right: 1px; }

/* ── 工具栏 ─────────────────────────────────────────────── */
.modern-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}
.toolbar-left {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.view-mode-toggle {
  display: inline-flex;
  background: rgba(0, 0, 0, 0.4);
  padding: 3px;
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
  transition: all 0.2s;
}
.toggle-btn:hover {
  color: var(--sb-text);
}
.toggle-btn.active {
  background: var(--sb-accent-soft);
  color: var(--sb-accent);
  font-weight: 600;
}

/* ── 标题与计数 ─────────────────────────────────────────── */
.section-header-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 14px;
}
.section-title {
  display: flex;
  align-items: center;
  font-size: 15px;
  font-weight: 600;
  color: var(--sb-text);
}
.count-tag {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 7px;
  font-size: 11px;
  font-weight: 600;
  border-radius: 10px;
  background: rgba(56, 189, 248, 0.14);
  color: var(--sb-accent);
  border: 1px solid rgba(56, 189, 248, 0.25);
}

/* ── 等待确认卡片 ───────────────────────────────────────── */
.pending-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}
.pending-card {
  padding: 12px 16px;
  background: linear-gradient(145deg, rgba(245, 158, 11, 0.08) 0%, rgba(15, 23, 42, 0.8) 100%);
  border: 1px solid rgba(245, 158, 11, 0.3);
  border-radius: var(--sb-radius);
}
.pending-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}
.pending-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
}
.pending-line {
  font-size: 12px;
  color: var(--sb-text-2);
  margin-top: 4px;
}
.pending-label {
  color: var(--sb-muted);
  margin-right: 6px;
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

/* ── 顶级暗黑工业美学卡片 (Server Cards Grid) ────────────── */
.node-cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(350px, 1fr));
  gap: 16px;
  margin-bottom: 24px;
}

.node-card {
  position: relative;
  display: flex;
  flex-direction: column;
  background: linear-gradient(180deg, rgba(16, 24, 40, 0.82) 0%, rgba(9, 14, 25, 0.95) 100%);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 12px;
  padding: 14px 15px 12px;
  box-shadow: 0 6px 20px -4px rgba(0, 0, 0, 0.6), inset 0 1px 0 rgba(255, 255, 255, 0.08);
  backdrop-filter: blur(14px);
  overflow: hidden;
  transition: transform 0.22s cubic-bezier(0.4, 0, 0.2, 1),
              border-color 0.22s cubic-bezier(0.4, 0, 0.2, 1),
              box-shadow 0.22s cubic-bezier(0.4, 0, 0.2, 1);
}
.node-card:hover {
  transform: translateY(-2px);
  border-color: rgba(56, 189, 248, 0.35);
  box-shadow: 0 14px 32px -4px rgba(0, 0, 0, 0.75), 0 0 18px rgba(56, 189, 248, 0.12), inset 0 1px 0 rgba(255, 255, 255, 0.15);
}
.node-card.has-gfw-block {
  border-color: rgba(244, 63, 94, 0.28);
}
.node-card.has-gfw-block:hover {
  border-color: rgba(244, 63, 94, 0.45);
  box-shadow: 0 14px 32px -4px rgba(0, 0, 0, 0.75), 0 0 20px rgba(244, 63, 94, 0.16), inset 0 1px 0 rgba(255, 255, 255, 0.15);
}
.node-card.is-offline {
  opacity: 0.72;
  border-color: rgba(255, 255, 255, 0.05);
}
.node-card.is-offline:hover {
  opacity: 0.92;
  border-color: rgba(255, 255, 255, 0.15);
}

/* 顶部光晕高光线 */
.node-card-glow {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
}
.node-card-glow.glow-online {
  background: linear-gradient(90deg, transparent, rgba(52, 211, 153, 0.7) 30%, rgba(56, 189, 248, 0.8) 70%, transparent);
}
.node-card-glow.glow-offline {
  background: linear-gradient(90deg, transparent, rgba(148, 163, 184, 0.3) 50%, transparent);
}
.node-card-glow.glow-blocked {
  background: linear-gradient(90deg, transparent, rgba(244, 63, 94, 0.9) 30%, rgba(251, 113, 133, 0.9) 70%, transparent);
}

/* 1. 头部排版 */
.card-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 10px;
}
.card-head-left {
  flex: 1;
  min-width: 0;
}
.title-row {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 5px;
}
.flag-icon {
  font-size: 15px;
  line-height: 1;
}
.node-name {
  font-size: 15px;
  font-weight: 600;
  color: #f8fafc;
  letter-spacing: -0.01em;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.badge-gfw-alert {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 6px;
  font-size: 10px;
  font-weight: 700;
  border-radius: 4px;
  background: rgba(244, 63, 94, 0.16);
  color: #fb7185;
  border: 1px solid rgba(244, 63, 94, 0.35);
  line-height: 14px;
}
.gfw-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #f43f5e;
}

.sub-badge-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 11px;
}
.os-arch-tag {
  color: var(--sb-muted);
  background: rgba(255, 255, 255, 0.04);
  padding: 1px 6px;
  border-radius: 4px;
  border: 1px solid rgba(255, 255, 255, 0.06);
}
.version-tag {
  color: var(--sb-text-2);
  background: rgba(255, 255, 255, 0.03);
  padding: 1px 6px;
  border-radius: 4px;
}
.upgrade-pill {
  border: 1px solid rgba(245, 158, 11, 0.4);
  background: rgba(245, 158, 11, 0.15);
  color: #fbbf24;
  font-size: 10px;
  font-weight: 600;
  padding: 1px 6px;
  border-radius: 4px;
  cursor: pointer;
  transition: all 0.15s;
}
.upgrade-pill:hover {
  background: rgba(245, 158, 11, 0.25);
  transform: scale(1.03);
}
.upgrade-pill-sub {
  border-color: rgba(56, 189, 248, 0.35);
  background: rgba(56, 189, 248, 0.12);
  color: #38bdf8;
}

.icon-more-btn {
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.06);
  color: var(--sb-muted);
  width: 26px;
  height: 26px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.15s;
}
.icon-more-btn:hover {
  color: #f8fafc;
  background: rgba(255, 255, 255, 0.08);
  border-color: rgba(255, 255, 255, 0.15);
}

/* 2. 终端风格地址胶囊 (Terminal Capsule) */
.card-address-capsule {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 10px;
  background: rgba(0, 0, 0, 0.42);
  border: 1px solid rgba(255, 255, 255, 0.06);
  border-radius: 6px;
  margin-bottom: 12px;
  cursor: pointer;
  transition: all 0.15s;
}
.card-address-capsule:hover {
  border-color: rgba(56, 189, 248, 0.3);
  background: rgba(0, 0, 0, 0.55);
}
.address-prefix {
  font-size: 11px;
  color: var(--sb-muted);
  opacity: 0.6;
}
.address-text {
  font-size: 12px;
  color: #7dd3fc;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.address-empty {
  color: var(--sb-warning);
}
.ddns-chip {
  font-size: 9px;
  font-weight: 700;
  padding: 1px 5px;
  border-radius: 3px;
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  border: 1px solid rgba(52, 211, 153, 0.3);
}
.address-copy-icon {
  background: transparent;
  border: none;
  color: var(--sb-muted);
  display: flex;
  align-items: center;
  padding: 0;
  cursor: pointer;
  transition: color 0.15s;
}
.card-address-capsule:hover .address-copy-icon {
  color: #f8fafc;
}

/* 3. 核心硬件资源三微舱 (Hardware Micro Cards) ────────── */
.hw-dash-container {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  margin-bottom: 10px;
}
.hw-mini-card {
  display: flex;
  flex-direction: column;
  padding: 8px 9px 7px;
  background: rgba(255, 255, 255, 0.025);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 8px;
  transition: all 0.15s;
}
.hw-mini-card:hover {
  background: rgba(255, 255, 255, 0.04);
  border-color: rgba(255, 255, 255, 0.08);
}
.hw-card-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 5px;
}
.hw-card-title {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  font-weight: 600;
  color: var(--sb-muted);
}
.hw-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
}
.hw-dot.cpu { background: #38bdf8; }
.hw-dot.mem { background: #a855f7; }
.hw-dot.disk { background: #34d399; }

.hw-card-percent {
  font-size: 13px;
  font-weight: 700;
  line-height: 1;
}
.hw-bar-track {
  width: 100%;
  height: 4px;
  background: rgba(255, 255, 255, 0.07);
  border-radius: 2px;
  overflow: hidden;
  margin-bottom: 4px;
}
.hw-bar-thumb {
  height: 100%;
  border-radius: 2px;
  transition: width 0.3s ease;
}
.hw-card-bottom {
  font-size: 10px;
  color: var(--sb-muted);
  text-align: right;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  opacity: 0.85;
}

/* 4. 网络遥测舱 (Network Telemetry Bay) ───────────────── */
.net-telemetry-bay {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 10px;
  background: rgba(0, 0, 0, 0.28);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 8px;
  margin-bottom: 10px;
}
.net-left-stream {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.stream-speeds {
  display: flex;
  align-items: center;
  gap: 8px;
}
.speed-unit {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 4px;
  line-height: 1;
}
.speed-unit.down {
  background: rgba(16, 185, 129, 0.12);
  color: #34d399;
  border: 1px solid rgba(16, 185, 129, 0.25);
}
.speed-unit.up {
  background: rgba(56, 189, 248, 0.12);
  color: #38bdf8;
  border: 1px solid rgba(56, 189, 248, 0.25);
}
.speed-arrow { font-size: 10px; font-weight: 700; }
.speed-num { font-weight: 600; }

.stream-total {
  font-size: 10px;
  color: var(--sb-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.total-lbl { margin-right: 4px; opacity: 0.7; }
.total-val { color: var(--sb-text-2); }

.net-right-pod {
  flex-shrink: 0;
}
.conn-pod {
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 4px 10px;
  border-radius: 6px;
  background: rgba(255, 255, 255, 0.03);
  border: 1px solid rgba(255, 255, 255, 0.06);
  min-width: 62px;
}
.conn-pod.has-conn {
  background: rgba(99, 102, 241, 0.15);
  border-color: rgba(99, 102, 241, 0.3);
  color: #c7d2fe;
}
.conn-pod-top {
  display: flex;
  align-items: center;
  gap: 4px;
}
.conn-digit {
  font-size: 14px;
  font-weight: 700;
  line-height: 1;
}
.conn-unit-lbl {
  font-size: 9px;
  color: var(--sb-muted);
  margin-top: 1px;
}

/* 5. 三网延迟与线路质量舱 (Latency & Route Bay) ────────── */
.latency-route-bay {
  padding: 8px 10px;
  background: rgba(0, 0, 0, 0.25);
  border: 1px solid rgba(255, 255, 255, 0.05);
  border-radius: 8px;
  margin-bottom: 10px;
  transition: all 0.2s;
}
.latency-route-bay.is-blocked {
  border-color: rgba(244, 63, 94, 0.3);
  background: rgba(244, 63, 94, 0.06);
}

/* GFW 阻断警示横幅 */
.gfw-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 8px;
  border-radius: 6px;
  background: rgba(244, 63, 94, 0.18);
  border: 1px solid rgba(244, 63, 94, 0.4);
  margin-bottom: 8px;
}
.gfw-alert-flare {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #f43f5e;
  box-shadow: 0 0 6px #f43f5e;
  animation: pulseGfw 1.5s infinite;
  flex-shrink: 0;
}
@keyframes pulseGfw {
  0% { transform: scale(0.9); opacity: 1; }
  50% { transform: scale(1.6); opacity: 0.3; }
  100% { transform: scale(0.9); opacity: 1; }
}
.gfw-banner-text {
  display: flex;
  align-items: baseline;
  gap: 6px;
  overflow: hidden;
}
.gfw-banner-main {
  font-size: 11px;
  font-weight: 700;
  color: #fb7185;
  white-space: nowrap;
}
.gfw-banner-note {
  font-size: 10px;
  color: var(--sb-muted);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.latency-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 6px;
  margin-bottom: 6px;
}
.isp-pill {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 3px 5px;
  border-radius: 5px;
  font-size: 11px;
  background: rgba(255, 255, 255, 0.025);
  border: 1px solid rgba(255, 255, 255, 0.06);
  white-space: nowrap;
  transition: all 0.15s;
}
.isp-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  flex-shrink: 0;
}
.isp-dot.mobile-dot { background: #10b981; }
.isp-dot.unicom-dot { background: #f59e0b; }
.isp-dot.telecom-dot { background: #3b82f6; }
.isp-title {
  font-size: 10px;
  color: var(--sb-muted);
}
.isp-ms {
  font-weight: 600;
}

.ping-status-fast { border-color: rgba(16, 185, 129, 0.35); color: #34d399; }
.ping-status-medium { border-color: rgba(245, 158, 11, 0.35); color: #fbbf24; }
.ping-status-slow { border-color: rgba(148, 163, 184, 0.25); color: #94a3b8; }
.ping-status-timeout, .isp-pill.is-timeout { border-color: rgba(244, 63, 94, 0.4); color: #fb7185; }

.routes-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.route-tag {
  font-size: 10px;
  padding: 2px 6px;
  border-radius: 4px;
  font-weight: 600;
  line-height: 1;
  white-space: nowrap;
}
.route-tag.tag-gold {
  background: rgba(245, 158, 11, 0.16);
  color: #fbbf24;
  border: 1px solid rgba(245, 158, 11, 0.38);
  box-shadow: 0 0 8px rgba(245, 158, 11, 0.2);
}
.route-tag.tag-muted {
  background: rgba(255, 255, 255, 0.04);
  color: #94a3b8;
  border: 1px solid rgba(255, 255, 255, 0.08);
}
.route-tag.tag-danger {
  background: rgba(244, 63, 94, 0.18);
  color: #fb7185;
  border: 1px solid rgba(244, 63, 94, 0.4);
  font-weight: 700;
}
.route-tag.tag-verified {
  background: rgba(52, 211, 153, 0.12);
  color: #34d399;
  border: 1px solid rgba(52, 211, 153, 0.25);
}

.speedtest-empty {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 11px;
  padding: 2px 0;
}
.btn-inline-probe {
  background: rgba(56, 189, 248, 0.12);
  border: 1px solid rgba(56, 189, 248, 0.3);
  color: #38bdf8;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s;
}
.btn-inline-probe:hover {
  background: rgba(56, 189, 248, 0.22);
}

/* 6. 主动测绘防御精工条 (Defense Strip) ─────────────── */
.defense-strip {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: rgba(0, 0, 0, 0.2);
  border: 1px solid rgba(255, 255, 255, 0.04);
  border-radius: 6px;
  margin-bottom: 12px;
}
.defense-meta {
  display: flex;
  align-items: center;
  gap: 7px;
}
.shield-badge {
  width: 20px;
  height: 20px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(255, 255, 255, 0.04);
  color: var(--sb-muted);
  border: 1px solid rgba(255, 255, 255, 0.06);
}
.shield-badge.is-active {
  background: rgba(56, 189, 248, 0.15);
  color: #38bdf8;
  border-color: rgba(56, 189, 248, 0.35);
  box-shadow: 0 0 6px rgba(56, 189, 248, 0.25);
}
.shield-svg {
  width: 12px;
  height: 12px;
}
.defense-copy {
  display: flex;
  align-items: baseline;
  gap: 6px;
}
.defense-name {
  font-size: 11px;
  font-weight: 600;
  color: var(--sb-text-2);
}
.defense-subtext {
  font-size: 10px;
  color: var(--sb-muted);
}

/* 7. 卡片底栏 (Card Bottom Bar) ───────────────────────── */
.card-bottom-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-top: 9px;
  border-top: 1px solid rgba(255, 255, 255, 0.05);
  margin-top: auto;
}
.bottom-last-seen {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--sb-muted);
  opacity: 0.8;
}
.seen-label {
  opacity: 0.7;
}
.bottom-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}
.glass-act-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 10px;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 6px;
  color: var(--sb-text-2);
  font-size: 11px;
  cursor: pointer;
  transition: all 0.15s cubic-bezier(0.4, 0, 0.2, 1);
}
.glass-act-btn:hover {
  background: rgba(56, 189, 248, 0.15);
  border-color: rgba(56, 189, 248, 0.4);
  color: #38bdf8;
  transform: translateY(-1px);
}
.glass-act-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* ── 列表表格与辅助 ─────────────────────────────────────── */
.cards-pagination-bar {
  display: flex;
  justify-content: center;
  margin-top: 16px;
  margin-bottom: 30px;
}
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

.empty-cards-box {
  padding: 40px 0;
  display: flex;
  justify-content: center;
}

.danger-item {
  color: var(--sb-danger) !important;
}

.scanner-edit-box {
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
.scanner-edit-text {
  flex: 1;
  margin-right: 16px;
}
.scanner-edit-heading {
  font-weight: 600;
  color: var(--sb-text-1);
  margin-bottom: 2px;
}
</style>
