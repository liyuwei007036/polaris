<script setup>
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { api } from '../api'
import { formatBytes } from '../format'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'

use([BarChart, LineChart, PieChart, GridComponent, LegendComponent, TitleComponent, TooltipComponent, CanvasRenderer])

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const endpointCount = ref(0)
const cumulative = ref({ download: 0, upload: 0 })
const trafficHistory = shallowRef([])
const connectionHistory = shallowRef([])
const dismissedOffline = ref(readDismissed('sb_dashboard_offline'))
const refreshing = ref(false)
const trafficChart = ref()
const totalChart = ref()
const connectionChart = ref()
const networkChart = ref()
const proxyChart = ref()
const charts = []
const protocolColors = { TCP: '#38b2ac', UDP: '#93b25f', 其他: '#667085' }
// How far back "recently" reaches when ranking the busiest nodes.
const nodeWindowMinutes = 10
const nodeActivity = new Map()
let stopLive
let stopConnections
let refreshTimer
let resizeObserver
let renderQueued = false

const online = computed(() => appState.nodes.filter((node) => node.online).length)
const offline = computed(() => appState.nodes.length - online.value)
const connections = computed(() => [...connectionSnapshots.value.values()].flatMap(
  (item) => (item.connections || []).map((connection) => ({ ...connection, node_id: item.node_id })),
))
const activeConnections = computed(() => connections.value.length)
// Rates come from the agents, which are the only side sampling the counters
// on a fixed interval. The browser never derives a rate from two page loads.
const liveRates = computed(() => [...connectionSnapshots.value.values()].reduce(
  (total, item) => item.has_rates
    ? { download: total.download + Number(item.received_rate || 0), upload: total.upload + Number(item.sent_rate || 0) }
    : total,
  { download: 0, upload: 0 },
))

function readDismissed(key) {
  const stored = Number(window.localStorage.getItem(key))
  return Number.isFinite(stored) ? stored : -1
}

function dismiss(key, reference, value) {
  reference.value = value
  window.localStorage.setItem(key, String(value))
}

function chartBase(title) {
  return {
    animation: false,
    title: { text: title, left: 16, top: 14, textStyle: { color: '#344054', fontSize: 14, fontWeight: 600 } },
    tooltip: { trigger: 'axis', borderColor: '#d0d5dd', textStyle: { color: '#344054', fontSize: 12 } },
    grid: { left: 56, right: 22, top: 58, bottom: 66 },
    textStyle: { fontFamily: 'Inter, Segoe UI, Microsoft YaHei, sans-serif' },
  }
}

function timeLabel(value) {
  return new Date(value).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function initCharts() {
  for (const element of [trafficChart.value, totalChart.value, connectionChart.value, networkChart.value, proxyChart.value]) {
    if (!element) continue
    charts.push(init(element))
  }
  resizeObserver = new ResizeObserver(() => charts.forEach((chart) => chart.resize()))
  charts.forEach((chart) => resizeObserver.observe(chart.getDom()))
  renderCharts()
}

// Agents push several times a second across all nodes. Redrawing five charts
// on every push is what made this page stutter, so redraws are coalesced into
// one animation frame.
function scheduleRender() {
  if (renderQueued) return
  renderQueued = true
  window.requestAnimationFrame(() => {
    renderQueued = false
    renderCharts()
  })
}

function renderCharts() {
  if (charts.length !== 5) return
  const axis = {
    axisLine: { lineStyle: { color: '#d0d5dd' } },
    axisTick: { show: false },
    axisLabel: { color: '#667085', fontSize: 11 },
    splitLine: { lineStyle: { color: '#eef1f5' } },
  }
  charts[0].setOption({
    ...chartBase('实时流量'),
    legend: { bottom: 10, data: ['下载', '上传'] },
    xAxis: { ...axis, type: 'category', data: trafficHistory.value.map((item) => timeLabel(item.time)), boundaryGap: false },
    yAxis: { ...axis, type: 'value', minInterval: 1, axisLabel: { ...axis.axisLabel, formatter: (value) => formatBytes(value, '/s') } },
    series: [
      { name: '下载', type: 'line', data: trafficHistory.value.map((item) => item.download), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#0e9384' }, areaStyle: { color: 'rgba(14,147,132,.08)' } },
      { name: '上传', type: 'line', data: trafficHistory.value.map((item) => item.upload), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#7a9b48' }, areaStyle: { color: 'rgba(122,155,72,.07)' } },
    ],
  }, true)
  const totals = cumulative.value.download + cumulative.value.upload
  charts[1].setOption({
    animation: false,
    title: chartBase('累计流量').title,
    tooltip: { trigger: 'item', formatter: ({ name, value }) => `${name}<br>${formatBytes(value)}` },
    legend: { bottom: 10, show: totals > 0 },
    series: [{ type: 'pie', radius: ['48%', '70%'], center: ['50%', '49%'], label: { show: false }, data: totals > 0 ? [
      { name: '下载', value: cumulative.value.download, itemStyle: { color: '#38b2ac' } },
      { name: '上传', value: cumulative.value.upload, itemStyle: { color: '#93b25f' } },
    ] : [{ name: '暂无流量', value: 1, itemStyle: { color: '#e4e7ec' } }] }],
  }, true)
  charts[2].setOption({
    ...chartBase('活动连接'),
    legend: { bottom: 10, data: ['连接数'] },
    xAxis: { ...axis, type: 'category', data: connectionHistory.value.map((item) => timeLabel(item.time)), boundaryGap: false },
    yAxis: { ...axis, type: 'value', minInterval: 1 },
    series: [{ name: '连接数', type: 'line', data: connectionHistory.value.map((item) => item.count), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#d46b5f' }, areaStyle: { color: 'rgba(212,107,95,.09)' } }],
  }, true)
  // sing-box reports the transport protocol a connection uses, so this counts
  // TCP against UDP rather than inventing categories of its own.
  const protocolCounts = Object.entries(connections.value.reduce((result, row) => {
    const network = String(row.network || '').toLowerCase()
    const key = network === 'tcp' || network === 'udp' ? network.toUpperCase() : '其他'
    result[key] = (result[key] || 0) + 1
    return result
  }, {}))
  charts[3].setOption({
    animation: false,
    title: chartBase('传输协议').title,
    tooltip: {
      trigger: 'item',
      formatter: ({ name, value, percent }) => (protocolCounts.length ? `${name}<br>${value} 条连接（${percent}%）` : name),
    },
    legend: { bottom: 10, show: protocolCounts.length > 0 },
    series: [{ type: 'pie', radius: ['48%', '70%'], center: ['50%', '49%'], label: { show: false }, data: protocolCounts.length
      ? protocolCounts.map(([name, value]) => ({ name, value, itemStyle: { color: protocolColors[name] || '#667085' } }))
      : [{ name: '暂无连接', value: 1, itemStyle: { color: '#e4e7ec' } }] }],
  }, true)
  const popular = popularNodes()
  charts[4].setOption({
    ...chartBase(`热门节点（最近 ${nodeWindowMinutes} 分钟）`),
    grid: { left: 112, right: 28, top: 58, bottom: 24 },
    tooltip: { trigger: 'axis', formatter: (items) => `${items[0].name}<br>${items[0].value} 次连接` },
    xAxis: { ...axis, type: 'value', minInterval: 1 },
    yAxis: { ...axis, type: 'category', inverse: true, data: popular.map(([name]) => name), axisLabel: { ...axis.axisLabel, width: 92, overflow: 'truncate' } },
    series: [{ type: 'bar', data: popular.map(([, value]) => value), barMaxWidth: 18, itemStyle: { color: '#4d90a6', borderRadius: 2 } }],
  }, true)
}

// The charts advance when the agents actually report, not on a timer of their
// own: sampling faster than the push interval just repeated the last value
// and made the line look like data that was never measured.
function appendSamples() {
  const now = Date.now()
  trafficHistory.value = [...trafficHistory.value, { time: now, ...liveRates.value }].slice(-30)
  connectionHistory.value = [...connectionHistory.value, { time: now, count: activeConnections.value }].slice(-30)
  recordNodeActivity(now)
  scheduleRender()
}

// Nodes are ranked by how often clients connected through them recently, not
// by how many connections happen to be open at this instant: a node that
// carried a hundred short requests a minute ago matters more than one holding
// a single idle connection. Each connection is counted once, when it is first
// seen, and drops out of the ranking when it ages past the window.
function recordNodeActivity(now) {
  for (const row of connections.value) {
    const key = `${row.node_id}/${row.id}`
    if (!row.id || nodeActivity.has(key)) continue
    nodeActivity.set(key, { label: row.user || row.listener_name || '未知节点', at: now })
  }
  for (const [key, entry] of nodeActivity) {
    if (now - entry.at > nodeWindowMinutes * 60_000) nodeActivity.delete(key)
  }
}

function popularNodes() {
  const counts = {}
  for (const entry of nodeActivity.values()) counts[entry.label] = (counts[entry.label] || 0) + 1
  return Object.entries(counts).sort((left, right) => right[1] - left[1]).slice(0, 5)
}

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    await loadNodes()
    const [listenerResult, metricResult] = await Promise.all([
      api('/listeners').catch(() => ({ listeners: [] })),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
    ])
    // A node is one client-facing account, so a service with two users is two
    // nodes. The listener list carries the per-service count already.
    endpointCount.value = (listenerResult.listeners || []).reduce((total, listener) => total + Number(listener.endpoint_count || 0), 0)
    // Proxied traffic, not host interface totals, so the figure matches what
    // the connection list and the live rate are describing.
    cumulative.value = (metricResult.nodes || []).reduce((total, entry) => ({
      download: total.download + Number(entry.report?.proxy?.received_bytes || 0),
      upload: total.upload + Number(entry.report?.proxy?.sent_bytes || 0),
    }), { download: 0, upload: 0 })
    scheduleRender()
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function scheduleLoad() {
  if (refreshTimer) return
  refreshTimer = window.setTimeout(() => {
    refreshTimer = undefined
    load(true).catch(() => {})
  }, 1500)
}

onMounted(async () => {
  await load()
  await nextTick()
  initCharts()
  stopConnections = subscribeConnections(appendSamples)
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node' || event.kind === 'task') scheduleLoad()
  })
})

onBeforeUnmount(() => {
  stopLive?.()
  stopConnections?.()
  window.clearTimeout(refreshTimer)
  resizeObserver?.disconnect()
  charts.forEach((chart) => chart.dispose())
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="运行概览">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content dashboard-content">
      <el-alert v-if="offline > 0 && dismissedOffline !== offline" :title="`${offline} 台服务器离线，恢复连接前无法接收新配置`" type="warning" show-icon closable @close="dismiss('sb_dashboard_offline', dismissedOffline, offline)" />

      <section class="metric-strip">
        <div class="metric"><div class="metric__label">服务器总数</div><div class="metric__value">{{ appState.nodes.length }}</div></div>
        <div class="metric"><div class="metric__label">在线服务器</div><div class="metric__value">{{ online }}</div></div>
        <div class="metric"><div class="metric__label">活动连接</div><div class="metric__value">{{ activeConnections }}</div></div>
        <div class="metric"><div class="metric__label">节点数量</div><div class="metric__value">{{ endpointCount }}</div></div>
      </section>

      <section class="chart-grid">
        <div ref="trafficChart" class="chart-panel chart-panel--wide" />
        <div ref="totalChart" class="chart-panel" />
        <div ref="connectionChart" class="chart-panel" />
        <div ref="networkChart" class="chart-panel" />
        <div ref="proxyChart" class="chart-panel" />
      </section>
    </main>
  </div>
</template>

<style scoped>
.dashboard-content { display: flex; flex-direction: column; gap: 16px; }
.dashboard-content :deep(.el-alert) { flex: none; }
.metric-strip { flex: none; margin-bottom: 0; }
/* The chart rows share whatever height is left rather than each claiming a
   fixed one, so the overview fits the window instead of scrolling. */
.chart-grid { flex: 1; min-height: 0; display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(190px, 1fr)); gap: 14px; }
.chart-panel { min-width: 0; min-height: 0; background: #fff; border: 1px solid var(--sb-border); border-radius: 7px; }
.chart-panel--wide { grid-column: span 2; }
@media (max-width: 1180px) { .chart-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(3, minmax(190px, 1fr)); } .chart-panel--wide { grid-column: span 2; } }
@media (max-width: 720px) { .chart-grid { grid-template-columns: 1fr; grid-template-rows: repeat(5, minmax(220px, 1fr)); } .chart-panel--wide { grid-column: auto; } }
</style>
