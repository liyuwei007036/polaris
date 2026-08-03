<script setup>
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { api } from '../api'
import { formatBytes } from '../format'
import { subscribeLive } from '../live'
import PageHeader from '../components/PageHeader.vue'

use([BarChart, LineChart, PieChart, GridComponent, LegendComponent, TitleComponent, TooltipComponent, CanvasRenderer])

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const listeners = ref([])
const endpointCount = ref(0)
const tasks = ref([])
const metrics = ref({})
const rates = ref({})
const trafficHistory = ref([])
const connectionHistory = ref([])
const connectionSnapshots = new Map()
const previousCounters = new Map()
const dismissedOffline = ref(-1)
const dismissedFailed = ref(-1)
const refreshing = ref(false)
const trafficChart = ref()
const totalChart = ref()
const connectionChart = ref()
const networkChart = ref()
const proxyChart = ref()
const charts = []
let stopLive
let connectionSource
let refreshTimer
let resizeObserver

const online = computed(() => appState.nodes.filter((node) => node.online).length)
const offline = computed(() => appState.nodes.length - online.value)
const failed = computed(() => tasks.value.filter((task) => task.status === 'failed').length)
const connections = computed(() => [...connectionSnapshots.values()].flatMap((item) => item.connections || []))
const activeConnections = computed(() => connections.value.length)
const cumulative = computed(() => Object.values(metrics.value).reduce((total, report) => {
  total.download += Number(report?.node?.received_bytes || 0)
  total.upload += Number(report?.node?.sent_bytes || 0)
  return total
}, { download: 0, upload: 0 }))

function chartBase(title) {
  return {
    animationDuration: 240,
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
  charts[1].setOption({
    title: chartBase('累计流量').title,
    tooltip: { trigger: 'item', formatter: ({ name, value }) => `${name}<br>${formatBytes(value)}` },
    legend: { bottom: 10, show: cumulative.value.download + cumulative.value.upload > 0 },
    series: [{ type: 'pie', radius: ['48%', '70%'], center: ['50%', '49%'], label: { show: false }, data: cumulative.value.download + cumulative.value.upload > 0 ? [
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
  const networkCounts = Object.entries(connections.value.reduce((result, row) => {
    const key = (row.network || '未知').toUpperCase()
    result[key] = (result[key] || 0) + 1
    return result
  }, {}))
  charts[3].setOption({
    title: chartBase('网络类型').title,
    tooltip: { trigger: 'item' },
    legend: { bottom: 10 },
    series: [{ type: 'pie', radius: ['48%', '70%'], center: ['50%', '49%'], label: { show: false }, data: networkCounts.map(([name, value], index) => ({ name, value, itemStyle: { color: ['#38b2ac', '#93b25f', '#d46b5f', '#667085'][index % 4] } })) }],
  }, true)
  const popular = Object.entries(connections.value.reduce((result, row) => {
    const name = row.chains?.[0] || row.outbound || 'DIRECT'
    result[name] = (result[name] || 0) + Number(row.upload || 0) + Number(row.download || 0)
    return result
  }, {})).sort((left, right) => right[1] - left[1]).slice(0, 5)
  charts[4].setOption({
    ...chartBase('热门代理'),
    grid: { left: 112, right: 28, top: 58, bottom: 24 },
    tooltip: { trigger: 'axis', formatter: (items) => `${items[0].name}<br>${formatBytes(items[0].value)}` },
    xAxis: { ...axis, type: 'value', axisLabel: { ...axis.axisLabel, formatter: (value) => formatBytes(value) } },
    yAxis: { ...axis, type: 'category', inverse: true, data: popular.map(([name]) => name), axisLabel: { ...axis.axisLabel, width: 92, overflow: 'truncate' } },
    series: [{ type: 'bar', data: popular.map(([, value]) => value), barMaxWidth: 18, itemStyle: { color: '#4d90a6', borderRadius: 2 } }],
  }, true)
}

function updateRates(nodeID, report) {
  if (!report?.node || !report.collected_at) return
  const current = { received: Number(report.node.received_bytes || 0), sent: Number(report.node.sent_bytes || 0), time: Date.parse(report.collected_at) }
  const previous = previousCounters.get(nodeID)
  if (previous && current.time > previous.time) {
    const seconds = (current.time - previous.time) / 1000
    rates.value[nodeID] = { received: Math.max(0, current.received - previous.received) / seconds, sent: Math.max(0, current.sent - previous.sent) / seconds }
  }
  previousCounters.set(nodeID, current)
}

function appendTrafficSample() {
  const total = Object.values(rates.value).reduce((result, rate) => ({ download: result.download + Number(rate.received || 0), upload: result.upload + Number(rate.sent || 0) }), { download: 0, upload: 0 })
  trafficHistory.value = [...trafficHistory.value, { time: Date.now(), ...total }].slice(-30)
}

function appendConnectionSample() {
  connectionHistory.value = [...connectionHistory.value, { time: Date.now(), count: activeConnections.value }].slice(-30)
}

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    await loadNodes()
    const [listenerResult, taskResult] = await Promise.all([api('/listeners').catch(() => ({ listeners: [] })), api('/tasks?page_size=100').catch(() => ({ tasks: [] }))])
    listeners.value = listenerResult.listeners || []
    tasks.value = taskResult.tasks || []
    const [metricPairs, endpointResults] = await Promise.all([
      Promise.all(appState.nodes.map(async (node) => [node.id, (await api(`/nodes/${node.id}/metrics`).catch(() => null))?.report || null])),
      Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] })))),
    ])
    metrics.value = Object.fromEntries(metricPairs)
    endpointCount.value = endpointResults.reduce((total, result) => total + (result.endpoints?.length || 0), 0)
    metricPairs.forEach(([nodeID, report]) => updateRates(nodeID, report))
    appendTrafficSample()
    renderCharts()
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

function connectConnections() {
  connectionSource?.close()
  connectionSource = new EventSource('/api/v1/events/connections', { withCredentials: true })
  connectionSource.addEventListener('snapshot', (event) => {
    connectionSnapshots.clear()
    for (const item of JSON.parse(event.data).nodes || []) connectionSnapshots.set(item.node_id, item)
    appendConnectionSample()
    renderCharts()
  })
  connectionSource.addEventListener('node', (event) => {
    const item = JSON.parse(event.data)
    connectionSnapshots.set(item.node_id, item)
    appendConnectionSample()
    renderCharts()
  })
}

function scheduleLoad() {
  if (refreshTimer) return
  refreshTimer = window.setTimeout(() => {
    refreshTimer = undefined
    load(true).catch(() => {})
  }, 1000)
}

onMounted(async () => {
  await load()
  await nextTick()
  initCharts()
  connectConnections()
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node' || event.kind === 'task') scheduleLoad()
  })
})

onBeforeUnmount(() => {
  stopLive?.()
  window.clearTimeout(refreshTimer)
  connectionSource?.close()
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
      <el-alert v-if="offline > 0 && dismissedOffline !== offline" :title="`${offline} 台服务器离线，恢复连接前无法接收新配置`" type="warning" show-icon closable @close="dismissedOffline = offline" />
      <el-alert v-if="failed > 0 && dismissedFailed !== failed" :title="`最近有 ${failed} 项操作未完成`" type="error" show-icon closable @close="dismissedFailed = failed" />

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
.metric-strip { margin-bottom: 0; }
.chart-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
.chart-panel { height: 285px; min-width: 0; background: #fff; border: 1px solid var(--sb-border); border-radius: 7px; }
.chart-panel--wide { grid-column: span 2; }
@media (max-width: 1180px) { .chart-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .chart-panel--wide { grid-column: span 2; } }
@media (max-width: 720px) { .chart-grid { grid-template-columns: 1fr; } .chart-panel--wide { grid-column: auto; } .chart-panel { height: 260px; } }
</style>
