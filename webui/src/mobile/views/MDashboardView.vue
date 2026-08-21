<script setup>
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { api } from '../../api'
import { formatBytes } from '../../format'
import { subscribeLive } from '../../live'
import { connectionSnapshots, subscribeConnections } from '../../connections'
import MPage from '../components/MPage.vue'

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
const protocolColors = { TCP: '#38bdf8', UDP: '#34d399', 其他: '#64748b' }
const chartInk = { title: '#e8eef8', label: '#8496b0', line: 'rgba(148,163,184,.22)', split: 'rgba(148,163,184,.10)', empty: 'rgba(148,163,184,.16)' }
const chartTooltip = { backgroundColor: '#101a2c', borderColor: 'rgba(148,163,184,.28)', textStyle: { color: chartInk.title, fontSize: 12 } }
const chartLegend = { bottom: 4, itemWidth: 14, itemHeight: 8, textStyle: { color: chartInk.label, fontSize: 11 } }
// 手机上只保留最近十个采样点，点再密也看不出差别，还更省电。
const historyLength = 10
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

function dismissOffline() {
  dismissedOffline.value = offline.value
  window.localStorage.setItem('sb_dashboard_offline', String(offline.value))
}

// 窄屏上标题和图例都要往里收，否则轴标签会被裁掉半个字。
function chartBase(title) {
  return {
    animation: false,
    title: { text: title, left: 12, top: 10, textStyle: { color: chartInk.title, fontSize: 12.5, fontWeight: 600 } },
    tooltip: { ...chartTooltip, trigger: 'axis', confine: true },
    grid: { left: 44, right: 14, top: 44, bottom: 44 },
    textStyle: { fontFamily: 'Inter, Segoe UI, Microsoft YaHei, sans-serif' },
  }
}

// 纵轴上「878.91 KB/s」要占掉 130px 宽，画布只剩三分之二。轴上一位小数够看趋势，
// 准确数字看 tooltip。
function compactRate(value) {
  const number = Number(value || 0)
  const units = ['B', 'K', 'M', 'G', 'T']
  let index = 0
  let scaled = number
  while (scaled >= 1024 && index < units.length - 1) {
    scaled /= 1024
    index += 1
  }
  return `${index && scaled < 10 ? scaled.toFixed(1) : Math.round(scaled)}${units[index]}/s`
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

// 各节点每秒推送多次，五张图逐次重绘会让页面明显发烫。合并到一帧里画。
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
    axisLine: { lineStyle: { color: chartInk.line } },
    axisTick: { show: false },
    axisLabel: { color: chartInk.label, fontSize: 10 },
    splitLine: { lineStyle: { color: chartInk.split } },
  }
  charts[0].setOption({
    ...chartBase('实时流量'),
    tooltip: {
      ...chartTooltip,
      trigger: 'axis',
      confine: true,
      formatter: (items) => [items[0].axisValue, ...items.map((item) => `${item.marker}${item.seriesName} ${formatBytes(item.value, '/s')}`)].join('<br>'),
    },
    legend: { ...chartLegend, data: ['下载', '上传'] },
    xAxis: { ...axis, type: 'category', data: trafficHistory.value.map((item) => timeLabel(item.time)), boundaryGap: false, axisLabel: { ...axis.axisLabel, interval: 'auto', formatter: (value) => value.slice(3) } },
    yAxis: { ...axis, type: 'value', minInterval: 1, axisLabel: { ...axis.axisLabel, formatter: compactRate } },
    series: [
      { name: '下载', type: 'line', data: trafficHistory.value.map((item) => item.download), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#38bdf8' }, areaStyle: { color: 'rgba(56,189,248,.14)' } },
      { name: '上传', type: 'line', data: trafficHistory.value.map((item) => item.upload), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#34d399' }, areaStyle: { color: 'rgba(52,211,153,.12)' } },
    ],
  }, true)
  const totals = cumulative.value.download + cumulative.value.upload
  charts[1].setOption({
    animation: false,
    title: chartBase('累计流量').title,
    // 没有流量时只画占位圆环，不要把占位用的 1 当成流量报出来。
    tooltip: { ...chartTooltip, trigger: 'item', confine: true, formatter: ({ name, value }) => (totals > 0 ? `${name}<br>${formatBytes(value)}` : name) },
    legend: { ...chartLegend, show: totals > 0 },
    series: [{ type: 'pie', silent: totals === 0, radius: ['46%', '68%'], center: ['50%', '48%'], label: { show: false }, itemStyle: { borderColor: '#0e1524', borderWidth: 2 }, data: totals > 0 ? [
      { name: '下载', value: cumulative.value.download, itemStyle: { color: '#38bdf8' } },
      { name: '上传', value: cumulative.value.upload, itemStyle: { color: '#6366f1' } },
    ] : [{ name: '暂无流量', value: 1, itemStyle: { color: chartInk.empty } }] }],
  }, true)
  charts[2].setOption({
    ...chartBase('活动连接'),
    legend: { ...chartLegend, data: ['连接数'] },
    xAxis: { ...axis, type: 'category', data: connectionHistory.value.map((item) => timeLabel(item.time)), boundaryGap: false, axisLabel: { ...axis.axisLabel, formatter: (value) => value.slice(3) } },
    yAxis: { ...axis, type: 'value', minInterval: 1 },
    series: [{ name: '连接数', type: 'line', data: connectionHistory.value.map((item) => item.count), showSymbol: false, smooth: 0.25, lineStyle: { width: 2, color: '#a78bfa' }, areaStyle: { color: 'rgba(167,139,250,.14)' } }],
  }, true)
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
      ...chartTooltip,
      trigger: 'item',
      confine: true,
      formatter: ({ name, value, percent }) => (protocolCounts.length ? `${name}<br>${value} 条连接（${percent}%）` : name),
    },
    legend: { ...chartLegend, show: protocolCounts.length > 0 },
    series: [{ type: 'pie', silent: protocolCounts.length === 0, radius: ['46%', '68%'], center: ['50%', '48%'], label: { show: false }, itemStyle: { borderColor: '#0e1524', borderWidth: 2 }, data: protocolCounts.length
      ? protocolCounts.map(([name, value]) => ({ name, value, itemStyle: { color: protocolColors[name] || '#64748b' } }))
      : [{ name: '暂无连接', value: 1, itemStyle: { color: chartInk.empty } }] }],
  }, true)
  const popular = popularNodes()
  charts[4].setOption({
    ...chartBase(`热门节点（最近 ${nodeWindowMinutes} 分钟）`),
    grid: { left: 92, right: 20, top: 44, bottom: 16 },
    tooltip: { ...chartTooltip, trigger: 'axis', confine: true, formatter: (items) => `${items[0].name}<br>${items[0].value} 次连接` },
    xAxis: { ...axis, type: 'value', minInterval: 1 },
    yAxis: { ...axis, type: 'category', inverse: true, data: popular.map(([name]) => name), axisLabel: { ...axis.axisLabel, width: 78, overflow: 'truncate' } },
    series: [{ type: 'bar', data: popular.map(([, value]) => value), barMaxWidth: 14, itemStyle: { color: '#22d3ee', borderRadius: [0, 3, 3, 0] } }],
  }, true)
}

// 采样跟着各节点的上报走，不另起定时器：比上报还快地取样只会把
// 上一个读数重复画一遍，看着像测过其实没有。
function appendSamples() {
  const now = Date.now()
  trafficHistory.value = [...trafficHistory.value, { time: now, ...liveRates.value }].slice(-historyLength)
  connectionHistory.value = [...connectionHistory.value, { time: now, count: activeConnections.value }].slice(-historyLength)
  recordNodeActivity(now)
  scheduleRender()
}

function recordNodeActivity(now) {
  for (const row of connections.value) {
    const key = `${row.node_id}/${row.id}`
    if (!row.id || nodeActivity.has(key)) continue
    nodeActivity.set(key, { label: row.user || row.listener_name || '未知', at: now })
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
    endpointCount.value = (listenerResult.listeners || []).reduce((total, listener) => total + Number(listener.endpoint_count || 0), 0)
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
  <MPage title="运行概览" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
    </template>

    <div v-if="offline > 0 && dismissedOffline !== offline" class="m-notice m-notice--warning offline">
      <span>{{ offline }} 台服务器离线</span>
      <button type="button" @click="dismissOffline">忽略</button>
    </div>

    <section class="metrics">
      <div class="metric"><span>服务器总数</span><strong>{{ appState.nodes.length }}</strong></div>
      <div class="metric"><span>在线服务器</span><strong>{{ online }}</strong></div>
      <div class="metric"><span>活动连接</span><strong>{{ activeConnections }}</strong></div>
      <div class="metric"><span>节点数量</span><strong>{{ endpointCount }}</strong></div>
    </section>

    <div ref="trafficChart" class="chart chart--tall" />
    <div class="chart-pair">
      <div ref="totalChart" class="chart" />
      <div ref="networkChart" class="chart" />
    </div>
    <div ref="connectionChart" class="chart" />
    <div ref="proxyChart" class="chart chart--bars" />
  </MPage>
</template>

<style scoped>
.offline { display: flex; align-items: center; gap: 10px; }
.offline > span { flex: 1; }
.offline button {
  flex: none;
  min-width: 44px;
  min-height: 40px;
  margin: -10px -4px;
  padding: 0 6px;
  color: inherit;
  background: none;
  border: 0;
  font: inherit;
  font-size: 12px;
  text-decoration: underline;
  cursor: pointer;
}

/* 四个数字占掉大半屏就没地方放图了：一格压到 62px，四格连同间距不到 140px。 */
.metrics { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin-bottom: 12px; }
.metric {
  position: relative;
  padding: 9px 12px;
  overflow: hidden;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
}
.metric::before {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  height: 2px;
  background: linear-gradient(90deg, var(--sb-accent), rgba(99, 102, 241, .55) 45%, transparent);
}
.metric span { display: block; color: var(--sb-muted); font-size: 11.5px; }
.metric strong { display: block; margin-top: 2px; color: #fff; font-size: 21px; font-weight: 650; font-variant-numeric: tabular-nums; }

.chart {
  height: 190px;
  margin-bottom: 10px;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  overflow: hidden;
}
.chart--tall { height: 210px; }
.chart--bars { height: 220px; }
.chart-pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.chart-pair .chart { height: 170px; }
</style>
