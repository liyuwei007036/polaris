<template>
  <div class="openlat-console">
    <!-- Sticky glass header, ref App.tsx 同款结构 -->
    <header v-if="!embedded" class="console-header glass">
      <div class="header-inner">
        <button type="button" class="brand-btn" @click="closeDetail">
          <span class="brand-tile">
            <BrandMark class="brand-icon" />
          </span>
          <span class="brand-text">
            <span class="brand-name">{{ brandName }}</span>
            <span class="brand-sub">服务器在线状态</span>
          </span>
        </button>

        <div class="flex-1" />

        <div class="header-actions">
          <button
            type="button"
            class="header-btn header-icon-btn neu-track neu-press"
            :title="isDark ? '切换到浅色模式' : '切换到深色模式'"
            @click="toggleTheme()"
          >
            <svg v-if="isDark" class="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="5" />
              <line x1="12" y1="1" x2="12" y2="3" />
              <line x1="12" y1="21" x2="12" y2="23" />
              <line x1="4.22" y1="4.22" x2="5.64" y2="5.64" />
              <line x1="18.36" y1="18.36" x2="19.78" y2="19.78" />
              <line x1="1" y1="12" x2="3" y2="12" />
              <line x1="21" y1="12" x2="23" y2="12" />
              <line x1="4.22" y1="19.78" x2="5.64" y2="18.36" />
              <line x1="18.36" y1="5.64" x2="19.78" y2="4.22" />
            </svg>
            <svg v-else class="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
            </svg>
          </button>

          <button
            type="button"
            class="header-btn header-icon-btn neu-track neu-press"
            title="退出登录"
            @click="$emit('logout')"
          >
            <svg class="size-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
              <polyline points="16 17 21 12 16 7" />
              <line x1="21" y1="12" x2="9" y2="12" />
            </svg>
          </button>
        </div>
      </div>
    </header>

    <main class="console-main">
      <p v-if="error" class="console-error">
        {{ error }}
      </p>

      <!-- 节点详情（点击卡片/行进入），结构对齐 ref NodeDetail -->
      <NodeDetail
        v-if="selectedNode"
        :node="selectedNode"
        :metrics="selectedMetrics"
        :live="connectionSnapshots.get(selectedNode.id) || null"
        @back="closeDetail"
      />

      <template v-else>
        <Summary :nodes="openlatNodes(filteredNodes)" :metrics="metrics" @open="openNode" />

        <Toolbar
          v-model:query="query"
          v-model:group="group"
          v-model:sort="sort"
          v-model:view="view"
        />

        <p v-if="openlatNodes.length === 0" class="console-empty">还没有服务器</p>
        <div v-else-if="filteredNodes.length === 0" class="console-empty console-empty--search">
          <svg class="size-8" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="11" cy="11" r="8" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
            <line x1="8" y1="11" x2="14" y2="11" />
          </svg>
          没有匹配「{{ query }}」的服务器
          <button type="button" class="header-btn neu-track neu-press clear-query-btn" @click="query = ''">清空搜索</button>
        </div>

        <template v-else>
          <section
            v-for="groupItem in groups"
            :key="groupItem.key"
            class="group-section"
          >
            <div v-if="groupItem.label" class="group-head">
              <h2 class="group-title">
                <span v-if="groupItem.code" class="country-flag">{{ flagOf(groupItem.code) }}</span>
                {{ groupItem.label }}
              </h2>
              <span class="neu-inset group-badge">{{ groupItem.nodes.length }}</span>
              <span class="tnum group-online">{{ groupOnline(groupItem) }} 在线</span>
              <span class="group-rule" />
            </div>

            <div v-if="view === 'grid'" class="node-grid">
              <NodeCard
                v-for="node in groupItem.nodes"
                :key="node.id"
                :node="node"
                :metrics="metrics[node.id] || {}"
                @open="openNode"
              />
            </div>
            <div v-else class="node-list">
              <NodeRow
                v-for="node in groupItem.nodes"
                :key="node.id"
                :node="node"
                :metrics="metrics[node.id] || {}"
                @open="openNode"
              />
            </div>
          </section>
        </template>
      </template>

      <footer v-if="!embedded" class="console-footer neu rise">
        <div class="footer-main">
          <span class="footer-name">
            {{ brandName }}
            <span class="footer-muted">控制台</span>
          </span>
          <span class="footer-sub">服务器状态与链路质量监控</span>
          <span class="flex-1" />
          <span class="footer-live">
            <span class="live-dot" :class="liveStatus === 'connected' ? 'is-on' : 'is-off'" />
            {{ liveStatus === 'connected' ? '实时推送中' : '实时连接建立中' }}
          </span>
        </div>
        <div class="footer-meta">
          <span>WebSocket 实时推送 · 探针/24h 链路热力随数据自动刷新</span>
          <span>Powered by Polaris · © {{ year }}</span>
        </div>
      </footer>
    </main>
  </div>
</template>

<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import BrandMark from './BrandMark.vue'
import NodeCard from './NodeCard.vue'
import NodeDetail from './NodeDetail.vue'
import NodeRow from './NodeRow.vue'
import Summary from './Summary.vue'
import Toolbar from './Toolbar.vue'
import { api } from '../api'
import { subscribeLive, liveStatus } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import { isDark, toggleTheme } from '../theme'
import { brandName } from '../brand'

defineEmits(['logout'])

// 作为应用内页面（“节点状态”）渲染时，隐藏自带页头页脚，
// 避免与控制台外壳的侧边栏/页头重复。
defineProps({ embedded: { type: Boolean, default: false } })

const appState = inject('appState', { nodes: [] })
const loadNodes = inject('loadNodes', async () => [])

const VIEWS = ['grid', 'list']
const GROUP_KEYS = ['none', 'country', 'status', 'os']
const SORT_KEYS = ['default', 'name', 'cpu', 'mem', 'traffic', 'ping']

function storedChoice(key, allowed, fallback) {
  const saved = localStorage.getItem(key)
  return allowed.includes(saved) ? saved : fallback
}

const query = ref('')
const view = ref(storedChoice('openlat.view', VIEWS, 'grid'))
const group = ref(storedChoice('openlat.group', GROUP_KEYS, 'none'))
const sort = ref(storedChoice('openlat.sort', SORT_KEYS, 'default'))

watch(view, (v) => localStorage.setItem('openlat.view', v))
watch(group, (v) => localStorage.setItem('openlat.group', v))
watch(sort, (v) => localStorage.setItem('openlat.sort', v))

const metrics = ref({})
const error = ref('')
const openNodeId = ref(null)
const mappedNodes = computed(() => openlatNodes(appState.nodes))
const selectedNode = computed(() => mappedNodes.value.find((n) => n.id === openNodeId.value) || null)
const selectedMetrics = computed(() => (selectedNode.value ? metrics.value[selectedNode.value.id] || {} : {}))
const year = new Date().getFullYear()

async function loadMetrics() {
  try {
    const result = await api('/nodes/metrics')
    metrics.value = Object.fromEntries((result.nodes || []).map((entry) => [entry.node_id, entry.report]))
    error.value = ''
  } catch (e) {
    error.value = e?.message || '加载监控数据失败'
  }
}

// 后端节点 → openlat 节点：补 country（2 字母码）与取自 host 指标的 os_name / cpu_cores / virt
function openlatNodes(list) {
  return list.map((node) => {
    const host = metrics.value[node.id]?.host || {}
    return {
      ...node,
      country: node.country_code || node.country || '',
      os_name: host.os_name || node.os_name || node.os,
      cpu_cores: host.cpu_cores || node.cpu_cores || 0,
      virt: host.virt || '',
    }
  })
}

function osNameOf(node) {
  const raw = node.os_name || node.os || ''
  const name = raw.split(' ')[0].toLowerCase()
  if (!name) return ''
  if (name.includes('ubuntu')) return 'Ubuntu'
  if (name.includes('debian')) return 'Debian'
  if (name.includes('centos')) return 'CentOS'
  if (name.includes('almalinux')) return 'AlmaLinux'
  if (name.includes('rocky')) return 'RockyLinux'
  if (name.includes('fedora')) return 'Fedora'
  if (name.includes('arch')) return 'ArchLinux'
  if (name.includes('alpine')) return 'AlpineLinux'
  if (name.includes('suse')) return 'OpenSUSE'
  if (name.includes('gentoo')) return 'Gentoo'
  if (name.includes('openwrt')) return 'OpenWrt'
  if (name.includes('freebsd')) return 'FreeBSD'
  if (name.includes('redhat') || name.includes('rhel')) return 'RedHat'
  if (name.includes('windows')) return 'Windows'
  if (name.includes('darwin') || name.includes('macos')) return 'macOS'
  return raw.charAt(0).toUpperCase() + raw.slice(1)
}

function statusOf(node) {
  if (node.online) return 'online'
  const host = metrics.value[node.id]?.host
  return (host?.cpu_cores || host?.memory_total || node.last_seen_at) ? 'offline' : 'pending'
}

function statusLabel(status) {
  return status === 'online' ? '在线' : status === 'offline' ? '离线' : '未接入'
}

function nodeTraffic(node) {
  const totals = metrics.value[node.id]?.node || {}
  return (totals.received_bytes || 0) + (totals.sent_bytes || 0)
}

function nodeLatencyScore(node) {
  // 排序键：ref 的 storeLatency 顶层 latency 字段（最快探针/DNS 综合值），超大表丢失判定
  const latency = metrics.value[node.id]?.latency
  if (!latency || latency.none) return Number.POSITIVE_INFINITY
  const value = Number(latency.latency ?? latency.avgLatency)
  return Number.isFinite(value) ? value : Number.POSITIVE_INFINITY
}

const sortedNodes = computed(() => {
  const list = [...mappedNodes.value]
  switch (sort.value) {
    case 'name':
      return list.sort((a, b) => (a.name || '').localeCompare(b.name || '', 'zh-Hans-CN'))
    case 'cpu': {
      const cpu = (node) => metrics.value[node.id]?.host?.cpu_percent ?? -1
      return list.sort((a, b) => cpu(b) - cpu(a))
    }
    case 'mem': {
      const ratio = (node) => {
        const host = metrics.value[node.id]?.host
        return host?.memory_total > 0 ? host.memory_used / host.memory_total : -1
      }
      return list.sort((a, b) => ratio(b) - ratio(a))
    }
    case 'traffic':
      return list.sort((a, b) => nodeTraffic(b) - nodeTraffic(a))
    case 'ping':
      return list.sort((a, b) => nodeLatencyScore(a) - nodeLatencyScore(b))
    default:
      return list
  }
})

const filteredNodes = computed(() => {
  const terms = query.value.trim().toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return sortedNodes.value
  return sortedNodes.value.filter((node) => {
    const host = metrics.value[node.id]?.host || {}
    const hay = [
      node.name, node.country, node.country_code, node.city, node.location,
      osNameOf(node), node.os, node.architecture, node.agent_version,
      ...(node.ip_addresses || []), node.last_observed_ip, node.provider, node.client_address,
      host.cpu_name,
    ].filter(Boolean).join(' ').toLowerCase()
    return terms.every((term) => hay.includes(term))
  })
})

const groups = computed(() => {
  const nodes = filteredNodes.value
  if (group.value === 'none') return nodes.length > 0 ? [{ key: 'all', label: '', nodes }] : []

  const buckets = new Map()
  for (const node of nodes) {
    let bucket
    if (group.value === 'country') {
      const code = (node.country_code || node.country || '').trim().toUpperCase()
      bucket = code ? { key: code, label: code, code } : { key: 'ZZ', label: '未知地区', code: '' }
    } else if (group.value === 'status') {
      const status = statusOf(node)
      bucket = { key: status, label: statusLabel(status) }
    } else {
      const name = osNameOf(node)
      bucket = name ? { key: name, label: name } : { key: 'unknown', label: '等待上报' }
    }
    const existing = buckets.get(bucket.key)
    if (existing) existing.nodes.push(node)
    else buckets.set(bucket.key, { ...bucket, nodes: [node] })
  }

  const list = [...buckets.values()]
  if (group.value === 'status') {
    const order = ['online', 'offline', 'pending']
    return list.sort((a, b) => order.indexOf(a.key) - order.indexOf(b.key))
  }
  return list.sort((a, b) => b.nodes.length - a.nodes.length || a.label.localeCompare(b.label, 'zh-Hans-CN'))
})

function groupOnline(groupItem) {
  return groupItem.nodes.filter((n) => n.online).length
}

function flagOf(code) {
  if (!code || code.length !== 2) return '🌐'
  const offset = 127397
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => c.charCodeAt(0) + offset))
}

function openNode(id) {
  openNodeId.value = id
}

function closeDetail() {
  openNodeId.value = null
}

let stopLive, stopConns, pollTimer

onMounted(async () => {
  await loadNodes().catch((e) => { error.value = e?.message || '' })
  await loadMetrics()
  // 节点增删/上线事件由 App 统一拉一次 nodes；监控数字由这里按事件刷新并低频轮询补齐速率曲线
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node') loadMetrics()
  })
  stopConns = subscribeConnections(() => {})
  pollTimer = window.setInterval(loadMetrics, 8000)
})

onBeforeUnmount(() => {
  stopLive?.()
  stopConns?.()
  window.clearInterval(pollTimer)
})
</script>

<style scoped>
.openlat-console {
  min-height: 100vh;
  color: var(--sb-text);
}

.console-header {
  position: sticky;
  top: 0;
  z-index: 20;
  border-bottom: 1px solid var(--sb-line);
}

.header-inner {
  display: flex;
  align-items: center;
  gap: 12px;
  max-width: 1500px;
  margin: 0 auto;
  padding: 12px 24px;
}

@media (max-width: 640px) {
  .header-inner {
    padding: 10px 16px;
  }
}

.brand-btn {
  display: flex;
  align-items: center;
  gap: 10px;
  border: none;
  background: transparent;
  color: inherit;
  cursor: pointer;
  transition: opacity 0.15s ease;
}

.brand-btn:hover {
  opacity: 0.8;
}

.brand-tile {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 12px;
  background: linear-gradient(135deg, var(--sb-accent), var(--sb-accent-2));
  color: #fff;
  box-shadow: var(--sh-neu-sm);
}

.brand-icon {
  width: 16px;
  height: 16px;
}

.brand-text {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
}

.brand-name {
  font-size: 13px;
  font-weight: 600;
  line-height: 1;
}

.brand-sub {
  margin-top: 4px;
  font-size: 10px;
  line-height: 1;
  color: var(--sb-muted);
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.header-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border-radius: 10px;
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  transition: color 0.15s ease;
}

.header-btn:hover {
  color: var(--sb-text);
}

.header-icon-btn {
  width: 32px;
  padding: 0;
  justify-content: center;
}

@media (max-width: 480px) {
  .header-btn-text {
    display: none;
  }
  .header-btn {
    padding: 0;
    width: 32px;
    justify-content: center;
  }
}

.console-main {
  max-width: 1500px;
  margin: 0 auto;
  padding: 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

@media (max-width: 640px) {
  .console-main {
    padding: 16px;
  }
}

.console-error {
  padding: 8px 12px;
  border: 1px solid rgba(244, 63, 94, 0.3);
  background: rgba(244, 63, 94, 0.1);
  border-radius: 12px;
  font-size: 13px;
  color: var(--sb-danger);
}

.console-empty {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  padding: 72px 0;
  color: var(--sb-muted);
  font-size: 13px;
}

.clear-query-btn {
  height: 30px;
}

.group-section {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.group-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 4px 0;
}

.group-title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.group-badge {
  padding: 1px 8px;
  border-radius: 999px;
  font-size: 11px;
  color: var(--sb-muted);
}

.group-online {
  font-size: 11px;
  color: var(--sb-muted);
}

.group-rule {
  height: 1px;
  flex: 1;
  background: var(--sb-line);
}

.node-grid {
  display: grid;
  grid-template-columns: repeat(1, 1fr);
  gap: 12px;
}

@media (min-width: 640px) {
  .node-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}

@media (min-width: 1280px) {
  .node-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}

@media (min-width: 1536px) {
  .node-grid {
    grid-template-columns: repeat(4, 1fr);
  }
}

.node-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.console-footer {
  border-radius: 16px;
  border: 1px solid var(--sb-line);
  padding: 14px 16px;
}

.footer-main {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}

.footer-name {
  font-size: 13px;
  font-weight: 600;
}

.footer-muted {
  margin-left: 6px;
  font-size: 12px;
  font-weight: 400;
  color: var(--sb-muted);
}

.footer-sub {
  font-size: 11px;
  color: var(--sb-muted);
  max-width: 208px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.footer-live {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 11px;
  color: var(--sb-muted);
}

.live-dot {
  width: 8px;
  height: 8px;
  border-radius: 999px;
}

.live-dot.is-on {
  background: var(--sb-success);
  box-shadow: 0 0 8px rgba(52, 211, 153, 0.7);
}

.live-dot.is-off {
  background: var(--sb-muted);
}

.footer-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 6px 12px;
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--sb-line);
  font-size: 10px;
  color: color-mix(in srgb, var(--sb-muted) 80%, transparent);
}

.country-flag {
  font-family: var(--flag-font, inherit);
}

</style>
