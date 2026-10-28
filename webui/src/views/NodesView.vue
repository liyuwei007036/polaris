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
  if (percent >= 85) return '#fb7185' // 红色警告
  if (percent >= 65) return '#fbbf24' // 琥珀黄中载
  return '#34d399' // 翠绿正常
}

// 智能国旗推导
function getNodeFlag(name) {
  if (!name) return '🌐'
  // 若名称开头已包含 Emoji
  if (/^(\p{Extended_Pictographic})/u.test(name)) return ''
  const upper = name.toUpperCase()
  if (upper.includes('TW') || upper.includes('台湾') || upper.includes('HINET')) return '🇹🇼'
  if (upper.includes('HK') || upper.includes('香港')) return '🇭🇰'
  if (upper.includes('SG') || upper.includes('新加坡') || upper.includes('狮城')) return '🇸🇬'
  if (upper.includes('JP') || upper.includes('日本') || upper.includes('东京') || upper.includes('大阪')) return '🇯🇵'
  if (upper.includes('KR') || upper.includes('韩国') || upper.includes('首尔')) return '🇰🇷'
  if (upper.includes('US') || upper.includes('美国') || upper.includes('圣何塞') || upper.includes('洛杉矶') || upper.includes('纽约') || upper.includes('西雅图') || upper.includes('硅谷') || upper.includes('芝加哥')) return '🇺🇸'
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
    ElMessage.success(`已复制地址: ${addr}`)
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
  return trimmed !== '' && trimmed !== '未知' && trimmed !== '不可达'
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
            :class="{ 'is-offline': !node.online }"
          >
            <!-- 卡片状态顶边光条 -->
            <div class="node-card-glow" :class="node.online ? 'glow-online' : 'glow-offline'" />

            <!-- 卡片头部：状态灯、名称、国旗、系统架构、版本 -->
            <div class="card-head">
              <div class="card-head-left">
                <div class="title-row">
                  <span
                    class="status-indicator"
                    :class="node.online ? 'online' : 'offline'"
                    :title="node.online ? '在线运行中' : '节点已离线'"
                  />
                  <span v-if="getNodeFlag(node.name)" class="flag-icon">{{ getNodeFlag(node.name) }}</span>
                  <span class="node-name" :title="node.name">{{ node.name }}</span>
                </div>
                <div class="sub-badge-row">
                  <span class="os-arch-tag">
                    {{ node.os || 'linux' }} · {{ node.architecture || 'amd64' }}
                  </span>
                  <span class="version-tag">v{{ node.agent_version || '—' }}</span>
                  <el-tag
                    v-if="agentUpdateAvailable(node)"
                    type="warning"
                    size="small"
                    effect="dark"
                    class="clickable-tag"
                    @click="upgradeAgent(node)"
                  >
                    Agent可升级
                  </el-tag>
                  <el-tag
                    v-if="singBoxUpdateAvailable(node)"
                    type="warning"
                    size="small"
                    effect="plain"
                    class="clickable-tag"
                    @click="upgradeSingBox(node)"
                  >
                    sing-box可升级
                  </el-tag>
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

            <!-- 连接地址条 -->
            <div class="card-address-bar">
              <div class="address-content" :title="'点击复制: ' + (node.client_address || '未配置')">
                <span class="addr-icon">🌐</span>
                <span v-if="node.client_address" class="addr-text font-mono" @click="copyAddress(node.client_address)">
                  {{ node.client_address }}
                </span>
                <span v-else class="addr-empty">未配置连接地址</span>
                <el-tooltip
                  v-if="node.ddns_enabled"
                  :content="`Cloudflare DDNS 自动同步 (${node.ddns_domain || node.client_address})，最新IP: ${node.last_observed_ip || '等待检测'}`"
                >
                  <span class="ddns-badge">DDNS</span>
                </el-tooltip>
              </div>
              <button
                v-if="node.client_address"
                class="copy-btn"
                title="复制地址"
                @click="copyAddress(node.client_address)"
              >
                <el-icon><CopyDocument /></el-icon>
              </button>
            </div>

            <!-- 服务器硬件监控矩阵：CPU / 内存 / 磁盘 -->
            <div class="hardware-metrics-box">
              <!-- CPU -->
              <div class="hardware-item">
                <div class="hw-head">
                  <span class="hw-label">
                    <svg class="hw-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor">
                      <rect x="4" y="4" width="16" height="16" rx="2" stroke-width="2"/>
                      <rect x="9" y="9" width="6" height="6" stroke-width="2"/>
                      <path d="M9 1v3M15 1v3M9 20v3M15 20v3M20 9h3M20 14h3M1 9h3M1 14h3" stroke-width="2"/>
                    </svg>
                    CPU
                  </span>
                  <span
                    class="hw-val font-mono"
                    :style="{ color: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0) }"
                  >
                    {{ node.online && metrics[node.id]?.host ? `${metrics[node.id].host.cpu_percent.toFixed(1)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-progress-track">
                  <div
                    class="hw-progress-fill"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? Math.min(100, metrics[node.id].host.cpu_percent) : 0}%`,
                      backgroundColor: getUsageStatusColor(metrics[node.id]?.host?.cpu_percent || 0),
                    }"
                  />
                </div>
                <div class="hw-foot">
                  <span v-if="metrics[node.id]?.host?.uptime_seconds" class="hw-sub font-mono">
                    运行 {{ Math.floor(metrics[node.id].host.uptime_seconds / 86400) }} 天
                  </span>
                  <span v-else class="hw-sub font-mono">核数待报</span>
                </div>
              </div>

              <!-- 内存 -->
              <div class="hardware-item">
                <div class="hw-head">
                  <span class="hw-label">
                    <svg class="hw-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor">
                      <rect x="2" y="6" width="20" height="12" rx="2" stroke-width="2"/>
                      <path d="M6 10v4M10 10v4M14 10v4M18 10v4" stroke-width="2"/>
                    </svg>
                    内存
                  </span>
                  <span
                    class="hw-val font-mono"
                    :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total)) }"
                  >
                    {{ node.online && metrics[node.id]?.host?.memory_total ? `${getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-progress-track">
                  <div
                    class="hw-progress-fill"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.memory_used, metrics[node.id].host.memory_total) : 0}%`,
                      backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.memory_used, metrics[node.id]?.host?.memory_total)),
                    }"
                  />
                </div>
                <div class="hw-foot">
                  <span v-if="node.online && metrics[node.id]?.host?.memory_total" class="hw-sub font-mono">
                    {{ formatResourceBytes(metrics[node.id].host.memory_used) }} / {{ formatResourceBytes(metrics[node.id].host.memory_total) }}
                  </span>
                  <span v-else class="hw-sub font-mono">待上报</span>
                </div>
              </div>

              <!-- 硬盘 -->
              <div class="hardware-item">
                <div class="hw-head">
                  <span class="hw-label">
                    <svg class="hw-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor">
                      <rect x="3" y="4" width="18" height="16" rx="2" stroke-width="2"/>
                      <line x1="3" y1="12" x2="21" y2="12" stroke-width="2"/>
                      <circle cx="7" cy="8" r="1" fill="currentColor"/>
                      <circle cx="7" cy="16" r="1" fill="currentColor"/>
                    </svg>
                    硬盘
                  </span>
                  <span
                    class="hw-val font-mono"
                    :style="{ color: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total)) }"
                  >
                    {{ node.online && metrics[node.id]?.host?.disk_total ? `${getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total)}%` : (node.online ? '—' : '离线') }}
                  </span>
                </div>
                <div class="hw-progress-track">
                  <div
                    class="hw-progress-fill"
                    :style="{
                      width: `${node.online && metrics[node.id]?.host ? getResourcePercent(metrics[node.id].host.disk_used, metrics[node.id].host.disk_total) : 0}%`,
                      backgroundColor: getUsageStatusColor(getResourcePercent(metrics[node.id]?.host?.disk_used, metrics[node.id]?.host?.disk_total)),
                    }"
                  />
                </div>
                <div class="hw-foot">
                  <span v-if="node.online && metrics[node.id]?.host?.disk_total" class="hw-sub font-mono">
                    {{ formatResourceBytes(metrics[node.id].host.disk_used) }} / {{ formatResourceBytes(metrics[node.id].host.disk_total) }}
                  </span>
                  <span v-else class="hw-sub font-mono">待上报</span>
                </div>
              </div>
            </div>

            <!-- 网络监控区：实时速率与连接数、累计流量 -->
            <div class="network-section">
              <div class="net-row-top">
                <div class="rates-cluster font-mono">
                  <div class="rate-badge rate-down">
                    <span class="arrow">↓</span>
                    <span class="val">
                      {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).received_rate, '/s') : (node.online ? '0 B/s' : '0 B/s') }}
                    </span>
                  </div>
                  <div class="rate-badge rate-up">
                    <span class="arrow">↑</span>
                    <span class="val">
                      {{ live.get(node.id)?.has_rates ? formatBytes(live.get(node.id).sent_rate, '/s') : (node.online ? '0 B/s' : '0 B/s') }}
                    </span>
                  </div>
                </div>

                <div class="conn-badge" :class="{ 'has-connections': (live.get(node.id)?.connection_count || 0) > 0 }">
                  <el-icon><Connection /></el-icon>
                  <span class="conn-count font-mono">{{ live.get(node.id)?.connection_count ?? 0 }}</span>
                  <span class="conn-txt">连接</span>
                </div>
              </div>

              <div class="net-row-bottom font-mono">
                <span class="traffic-tag-label">累计流量:</span>
                <span v-if="metrics[node.id]?.proxy" class="traffic-tag-val">
                  ↓ {{ formatBytes(metrics[node.id].proxy.received_bytes) }} · ↑ {{ formatBytes(metrics[node.id].proxy.sent_bytes) }}
                </span>
                <span v-else class="traffic-tag-val subtle">等待上报</span>
              </div>
            </div>

            <!-- 三网延迟与线路质量 -->
            <div class="latency-section">
              <div v-if="speedtests[node.id]" class="speedtest-details">
                <div class="latency-badges-row">
                  <el-tooltip :content="'中国移动: ' + (speedtests[node.id].mobile_route || '标准直连')">
                    <div class="ping-pill" :class="getPingClass(speedtests[node.id].mobile_latency_ms ?? speedtests[node.id].mobile_ping_ms)">
                      <span class="isp-dot mobile" />
                      <span class="isp-name">移动</span>
                      <span class="isp-val font-mono">{{ getPingStr(speedtests[node.id].mobile_latency_ms ?? speedtests[node.id].mobile_ping_ms) }}</span>
                    </div>
                  </el-tooltip>

                  <el-tooltip :content="'中国联通: ' + (speedtests[node.id].unicom_route || '标准直连')">
                    <div class="ping-pill" :class="getPingClass(speedtests[node.id].unicom_latency_ms ?? speedtests[node.id].unicom_ping_ms)">
                      <span class="isp-dot unicom" />
                      <span class="isp-name">联通</span>
                      <span class="isp-val font-mono">{{ getPingStr(speedtests[node.id].unicom_latency_ms ?? speedtests[node.id].unicom_ping_ms) }}</span>
                    </div>
                  </el-tooltip>

                  <el-tooltip :content="'中国电信: ' + (speedtests[node.id].telecom_route || '标准直连')">
                    <div class="ping-pill" :class="getPingClass(speedtests[node.id].telecom_latency_ms ?? speedtests[node.id].telecom_ping_ms)">
                      <span class="isp-dot telecom" />
                      <span class="isp-name">电信</span>
                      <span class="isp-val font-mono">{{ getPingStr(speedtests[node.id].telecom_latency_ms ?? speedtests[node.id].telecom_ping_ms) }}</span>
                    </div>
                  </el-tooltip>
                </div>

                <!-- 优质线路识别 -->
                <div v-if="hasRecognizedRoutes(speedtests[node.id])" class="routes-row">
                  <span
                    v-if="isValidRoute(speedtests[node.id].telecom_route)"
                    :class="['route-badge', isPremiumRoute(speedtests[node.id].telecom_route) ? 'route-premium' : 'route-normal']"
                  >
                    {{ speedtests[node.id].telecom_route }}
                  </span>
                  <span
                    v-if="isValidRoute(speedtests[node.id].unicom_route)"
                    :class="['route-badge', isPremiumRoute(speedtests[node.id].unicom_route) ? 'route-premium' : 'route-normal']"
                  >
                    {{ speedtests[node.id].unicom_route }}
                  </span>
                  <span
                    v-if="isValidRoute(speedtests[node.id].mobile_route)"
                    :class="['route-badge', isPremiumRoute(speedtests[node.id].mobile_route) ? 'route-premium' : 'route-normal']"
                  >
                    {{ speedtests[node.id].mobile_route }}
                  </span>
                </div>
              </div>

              <div v-else class="latency-empty-row">
                <span class="subtle">三网延迟与线路尚未检测</span>
                <el-button
                  v-if="canWrite && node.online"
                  link
                  type="primary"
                  size="small"
                  :loading="Boolean(speedtesting[node.id])"
                  @click="runSpeedtest(node)"
                >
                  立即检测
                </el-button>
              </div>
            </div>

            <!-- 防御控制与最后心跳 -->
            <div class="card-defense-bar">
              <div class="defense-label-box">
                <span class="shield-dot" :class="{ active: Boolean(scanners[node.id]) }" />
                <span class="defense-title">测绘防御</span>
                <span class="defense-status-text">
                  {{ scanners[node.id] ? '拦截引擎扫描' : '未开启' }}
                </span>
              </div>
              <el-switch
                :model-value="Boolean(scanners[node.id])"
                :loading="Boolean(togglingScanner[node.id])"
                :disabled="!isAdmin || !node.online"
                active-text="已开"
                inactive-text="关闭"
                inline-prompt
                @change="toggleScannerDirect(node)"
              />
            </div>

            <!-- 卡片底栏操作区 -->
            <div class="card-footer">
              <div class="last-seen-text">
                <span class="time-label">活跃:</span>
                <span class="font-mono">{{ formatDateTime(node.last_seen_at, '从未') }}</span>
              </div>

              <div class="card-footer-actions">
                <el-button
                  v-if="canWrite && node.online"
                  size="small"
                  class="card-act-btn"
                  :loading="Boolean(speedtesting[node.id])"
                  @click="runSpeedtest(node)"
                >
                  <el-icon><Connection /></el-icon>
                  检测
                </el-button>
                <el-button
                  v-if="canWrite"
                  size="small"
                  class="card-act-btn"
                  @click="openEdit(node)"
                >
                  <el-icon><Edit /></el-icon>
                  配置
                </el-button>
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
                  <span class="status-indicator" :class="row.online ? 'online' : 'offline'" />
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
                  <div v-if="hasRecognizedRoutes(speedtests[row.id])" class="routes-row">
                    <span v-if="isValidRoute(speedtests[row.id].telecom_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].telecom_route) ? 'route-premium' : 'route-normal']">
                      {{ speedtests[row.id].telecom_route }}
                    </span>
                    <span v-if="isValidRoute(speedtests[row.id].unicom_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].unicom_route) ? 'route-premium' : 'route-normal']">
                      {{ speedtests[row.id].unicom_route }}
                    </span>
                    <span v-if="isValidRoute(speedtests[row.id].mobile_route)" :class="['route-badge', isPremiumRoute(speedtests[row.id].mobile_route) ? 'route-premium' : 'route-normal']">
                      {{ speedtests[row.id].mobile_route }}
                    </span>
                  </div>
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
/* ── 顶部统计指示栏 ─────────────────────────────────────── */
.server-overview-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 12px;
  padding: 10px 16px;
  background: linear-gradient(180deg, rgba(14, 21, 36, 0.75) 0%, rgba(9, 14, 25, 0.85) 100%);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  margin-bottom: 14px;
  backdrop-filter: blur(8px);
}
.overview-stat-group {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}
.overview-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: rgba(148, 163, 184, 0.06);
  border: 1px solid rgba(148, 163, 184, 0.12);
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
  background: rgba(15, 23, 42, 0.6);
  border: 1px solid rgba(148, 163, 184, 0.15);
  border-radius: 6px;
}
.rate-item.down { color: #34d399; font-weight: 600; }
.rate-item.up { color: #38bdf8; font-weight: 600; }
.rate-sep { color: var(--sb-muted); opacity: 0.5; }
.rate-arrow { font-size: 11px; margin-right: 1px; }

/* ── 现代工具栏 ─────────────────────────────────────────── */
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
  background: rgba(15, 23, 42, 0.7);
  padding: 3px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
}
.toggle-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
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

/* ── 状态指示灯 ─────────────────────────────────────────── */
.status-indicator {
  position: relative;
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  flex-shrink: 0;
}
.status-indicator.online {
  background: #10b981;
  box-shadow: 0 0 8px rgba(16, 185, 129, 0.8);
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
.status-indicator.pending {
  background: #f59e0b;
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.8);
}
@keyframes pulseLight {
  0% { transform: scale(0.9); opacity: 0.9; }
  60% { transform: scale(1.7); opacity: 0; }
  100% { transform: scale(0.9); opacity: 0; }
}

/* ── 卡片网格主体 (Grid) ────────────────────────────────── */
.node-cards-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
  gap: 16px;
  margin-bottom: 24px;
}

.node-card {
  position: relative;
  display: flex;
  flex-direction: column;
  background: linear-gradient(150deg, rgba(17, 26, 44, 0.88) 0%, rgba(10, 15, 27, 0.96) 100%);
  border: 1px solid rgba(148, 163, 184, 0.14);
  border-radius: var(--sb-radius);
  padding: 14px 16px 12px;
  box-shadow: 0 10px 30px -15px rgba(0, 0, 0, 0.7);
  backdrop-filter: blur(12px);
  overflow: hidden;
  transition: transform 0.22s ease, border-color 0.22s ease, box-shadow 0.22s ease;
}
.node-card:hover {
  transform: translateY(-3px);
  border-color: rgba(56, 189, 248, 0.35);
  box-shadow: 0 16px 36px -12px rgba(0, 0, 0, 0.8), 0 0 20px rgba(56, 189, 248, 0.12);
}
.node-card.is-offline {
  opacity: 0.76;
  border-color: rgba(148, 163, 184, 0.08);
}
.node-card.is-offline:hover {
  opacity: 0.95;
  border-color: rgba(148, 163, 184, 0.25);
}

/* 顶部光晕线 */
.node-card-glow {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 2px;
  background: transparent;
}
.node-card-glow.glow-online {
  background: linear-gradient(90deg, transparent, rgba(52, 211, 153, 0.8) 30%, rgba(56, 189, 248, 0.8) 70%, transparent);
}
.node-card-glow.glow-offline {
  background: linear-gradient(90deg, transparent, rgba(148, 163, 184, 0.3) 50%, transparent);
}

/* 头部 */
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
  font-weight: 700;
  color: var(--sb-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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
  background: rgba(148, 163, 184, 0.08);
  padding: 1px 6px;
  border-radius: 4px;
  border: 1px solid rgba(148, 163, 184, 0.12);
}
.version-tag {
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, 0.06);
  padding: 1px 6px;
  border-radius: 4px;
}
.clickable-tag {
  cursor: pointer;
  transition: transform 0.15s;
}
.clickable-tag:hover {
  transform: scale(1.05);
}
.icon-more-btn {
  background: transparent;
  border: none;
  color: var(--sb-muted);
  padding: 4px 6px;
  border-radius: 6px;
  cursor: pointer;
  transition: all 0.2s;
}
.icon-more-btn:hover {
  color: var(--sb-text);
  background: rgba(148, 163, 184, 0.1);
}

/* 地址条 */
.card-address-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 5px 10px;
  background: rgba(8, 13, 23, 0.7);
  border: 1px solid rgba(148, 163, 184, 0.1);
  border-radius: 6px;
  margin-bottom: 12px;
}
.address-content {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 1;
  min-width: 0;
  cursor: pointer;
}
.addr-icon {
  font-size: 12px;
  opacity: 0.7;
}
.addr-text {
  font-size: 12px;
  color: var(--sb-accent);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.addr-text:hover {
  text-decoration: underline;
}
.addr-empty {
  font-size: 12px;
  color: var(--sb-warning);
}
.ddns-badge {
  font-size: 10px;
  font-weight: 700;
  padding: 0 4px;
  line-height: 14px;
  border-radius: 3px;
  background: rgba(52, 211, 153, 0.18);
  color: #34d399;
  border: 1px solid rgba(52, 211, 153, 0.3);
  margin-left: 4px;
}
.copy-btn {
  border: none;
  background: transparent;
  color: var(--sb-muted);
  padding: 2px 4px;
  border-radius: 4px;
  cursor: pointer;
}
.copy-btn:hover {
  color: var(--sb-text);
}

/* ── 硬件监控仪表盒子 (CPU / 内存 / 磁盘) ─────────────────── */
.hardware-metrics-box {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
  padding: 10px;
  background: rgba(11, 18, 32, 0.65);
  border: 1px solid rgba(148, 163, 184, 0.1);
  border-radius: 8px;
  margin-bottom: 12px;
}
.hardware-item {
  display: flex;
  flex-direction: column;
}
.hw-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 4px;
}
.hw-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  font-weight: 600;
  color: var(--sb-muted);
}
.hw-icon {
  width: 12px;
  height: 12px;
  opacity: 0.7;
}
.hw-val {
  font-size: 12px;
  font-weight: 700;
}
.hw-progress-track {
  width: 100%;
  height: 4px;
  background: rgba(148, 163, 184, 0.12);
  border-radius: 3px;
  overflow: hidden;
  margin-bottom: 3px;
}
.hw-progress-fill {
  height: 100%;
  border-radius: 3px;
  transition: width 0.3s ease, background-color 0.3s ease;
}
.hw-foot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
}
.hw-sub {
  font-size: 10px;
  color: var(--sb-muted);
  transform: scale(0.92);
  transform-origin: right center;
  white-space: nowrap;
}

/* ── 网络与流量区域 ─────────────────────────────────────── */
.network-section {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 8px 10px;
  background: rgba(14, 23, 40, 0.5);
  border: 1px solid rgba(148, 163, 184, 0.08);
  border-radius: 8px;
  margin-bottom: 10px;
}
.net-row-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.rates-cluster {
  display: flex;
  align-items: center;
  gap: 6px;
}
.rate-badge {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 4px;
  line-height: 14px;
}
.rate-badge.rate-down {
  background: rgba(52, 211, 153, 0.1);
  color: #34d399;
  border: 1px solid rgba(52, 211, 153, 0.2);
}
.rate-badge.rate-up {
  background: rgba(56, 189, 248, 0.1);
  color: #38bdf8;
  border: 1px solid rgba(56, 189, 248, 0.2);
}
.rate-badge .arrow {
  font-weight: 700;
  font-size: 10px;
}
.rate-badge .val {
  font-weight: 600;
}
.conn-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--sb-muted);
  padding: 2px 7px;
  border-radius: 4px;
  background: rgba(148, 163, 184, 0.08);
}
.conn-badge.has-connections {
  background: rgba(99, 102, 241, 0.14);
  color: #a5b4fc;
  border: 1px solid rgba(99, 102, 241, 0.25);
}
.conn-count {
  font-weight: 700;
}
.net-row-bottom {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
}
.traffic-tag-label {
  color: var(--sb-muted);
}
.traffic-tag-val {
  color: var(--sb-text-2);
}

/* ── 三网延迟与线路质量 ─────────────────────────────────── */
.latency-section {
  padding: 8px 10px;
  background: rgba(14, 21, 37, 0.5);
  border: 1px solid rgba(148, 163, 184, 0.08);
  border-radius: 8px;
  margin-bottom: 10px;
}
.latency-badges-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
}
.ping-pill {
  flex: 1;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 3px 5px;
  border-radius: 5px;
  font-size: 11px;
  white-space: nowrap;
  background: rgba(15, 23, 42, 0.8);
  border: 1px solid rgba(148, 163, 184, 0.15);
  transition: all 0.2s;
}
.isp-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
}
.isp-dot.mobile { background: #10b981; }
.isp-dot.unicom { background: #f59e0b; }
.isp-dot.telecom { background: #3b82f6; }
.isp-name {
  color: var(--sb-muted);
  font-size: 10px;
}
.isp-val {
  font-weight: 600;
}

/* 延迟状态颜色阶梯 */
.ping-status-fast {
  border-color: rgba(16, 185, 129, 0.35);
  color: #34d399;
}
.ping-status-medium {
  border-color: rgba(245, 158, 11, 0.35);
  color: #fbbf24;
}
.ping-status-slow {
  border-color: rgba(148, 163, 184, 0.25);
  color: #94a3b8;
}
.ping-status-timeout {
  border-color: rgba(251, 113, 133, 0.35);
  color: #fb7185;
}

.routes-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 4px;
}
.route-badge {
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 600;
  line-height: 14px;
  white-space: nowrap;
}
.route-premium {
  background: rgba(245, 158, 11, 0.16);
  color: #fbbf24;
  border: 1px solid rgba(245, 158, 11, 0.35);
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.2);
}
.route-normal {
  background: rgba(148, 163, 184, 0.1);
  color: #94a3b8;
  border: 1px solid rgba(148, 163, 184, 0.2);
}
.latency-empty-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  font-size: 12px;
}

/* ── 测绘防御状态条 ─────────────────────────────────────── */
.card-defense-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  background: rgba(8, 13, 23, 0.5);
  border: 1px solid rgba(148, 163, 184, 0.08);
  border-radius: 6px;
  margin-bottom: 12px;
}
.defense-label-box {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
}
.shield-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #64748b;
}
.shield-dot.active {
  background: #38bdf8;
  box-shadow: 0 0 6px rgba(56, 189, 248, 0.8);
}
.defense-title {
  font-weight: 600;
  color: var(--sb-text-2);
}
.defense-status-text {
  font-size: 11px;
  color: var(--sb-muted);
}

/* ── 卡片底栏与动作 ─────────────────────────────────────── */
.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding-top: 8px;
  border-top: 1px solid rgba(148, 163, 184, 0.08);
  margin-top: auto;
}
.last-seen-text {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--sb-muted);
}
.card-footer-actions {
  display: flex;
  align-items: center;
  gap: 6px;
}
.card-act-btn {
  background: rgba(148, 163, 184, 0.08) !important;
  border: 1px solid rgba(148, 163, 184, 0.16) !important;
  color: var(--sb-text-2) !important;
  border-radius: 6px !important;
  padding: 4px 8px !important;
  font-size: 11px !important;
  height: 26px !important;
  transition: all 0.2s !important;
}
.card-act-btn:hover {
  background: var(--sb-accent-soft) !important;
  border-color: rgba(56, 189, 248, 0.4) !important;
  color: var(--sb-accent) !important;
}

/* ── 卡片分页条 ─────────────────────────────────────────── */
.cards-pagination-bar {
  display: flex;
  justify-content: center;
  margin-top: 16px;
  margin-bottom: 30px;
}

/* ── 列表表格模式补充样式 ───────────────────────────────── */
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
