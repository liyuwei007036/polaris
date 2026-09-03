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
import { fleetTotals, subscribeFleetTotals } from '../connections'
import { FLAG_FONT_FAMILY } from '../flags'
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
const protocolColors = { TCP: '#38bdf8', UDP: '#34d399', 其他: '#64748b' }
// 图表配色跟随控制台的深色令牌，避免出现浅色底残留。
const chartInk = { title: '#e8eef8', label: '#8496b0', line: 'rgba(148,163,184,.22)', split: 'rgba(148,163,184,.10)', empty: 'rgba(148,163,184,.16)' }
const chartTooltip = { backgroundColor: '#101a2c', borderColor: 'rgba(148,163,184,.28)', textStyle: { color: chartInk.title, fontSize: 12 } }
const chartLegend = { bottom: 10, textStyle: { color: chartInk.label } }
// 环形图上的数字：一根引导线牵到环外。数值直接写在图上，不必先悬停出提示，
// 也不必对着图例猜哪一段是哪一个。
function donutLabels(formatter, show) {
  return {
    label: show ? { show: true, color: chartInk.title, fontSize: 12, formatter } : { show: false },
    labelLine: show
      ? { show: true, length: 14, length2: 14, lineStyle: { color: chartInk.line } }
      : { show: false },
  }
}
let stopLive
let stopTotals
let refreshTimer
let resizeObserver
let renderQueued = false

const online = computed(() => appState.nodes.filter((node) => node.online).length)
const offline = computed(() => appState.nodes.length - online.value)
// 这一页的实时数字全部来自 master 每轮汇总的那一条 totals 事件：连接数、流量、
// 传输协议、热门节点。浏览器不再自己遍历连接列表——同一份数据算两遍，只会让
// 卡片和曲线差一拍，也让每个控制台各算各的。
const activeConnections = computed(() => Number(fleetTotals.value?.connection_count || 0))

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
    title: { text: title, left: 16, top: 14, textStyle: { color: chartInk.title, fontSize: 13, fontWeight: 600 } },
    tooltip: { ...chartTooltip, trigger: 'axis' },
    grid: { left: 56, right: 22, top: 58, bottom: 66 },
    textStyle: { fontFamily: `"${FLAG_FONT_FAMILY}", Inter, Segoe UI, Microsoft YaHei, sans-serif` },
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
    axisLine: { lineStyle: { color: chartInk.line } },
    axisTick: { show: false },
    axisLabel: { color: chartInk.label, fontSize: 11 },
    splitLine: { lineStyle: { color: chartInk.split } },
  }
  charts[0].setOption({
    ...chartBase('实时流量'),
    // Rates are sampled counters, so they arrive with a long fractional tail.
    // The default axis tooltip would print that raw number.
    tooltip: {
      ...chartTooltip,
      trigger: 'axis',
      formatter: (items) => [timeLabel(items[0].axisValue), ...items.map((item) => `${item.marker}${item.seriesName} ${formatBytes(item.value[1], '/s')}`)].join('<br>'),
    },
    legend: { ...chartLegend, data: ['下载', '上传'] },
    // A time axis rather than a category one. Samples do not arrive on a fixed
    // beat, and spacing them evenly drew a twenty-second gap exactly as wide as
    // a one-second one — and printed a label per sample, so the same second
    // appeared twice in a row. Points now sit where their clock reading says.
    xAxis: { ...axis, type: 'time', splitNumber: 5, axisLabel: { ...axis.axisLabel, hideOverlap: true, formatter: timeLabel } },
    yAxis: { ...axis, type: 'value', minInterval: 1, axisLabel: { ...axis.axisLabel, formatter: (value) => formatBytes(value, '/s') } },
    series: [
      { name: '下载', type: 'line', data: trafficHistory.value.map((item) => [item.time, item.download]), showSymbol: false, smooth: 0.25, itemStyle: { color: '#38bdf8' }, lineStyle: { width: 2, color: '#38bdf8' }, areaStyle: { color: 'rgba(56,189,248,.14)' } },
      { name: '上传', type: 'line', data: trafficHistory.value.map((item) => [item.time, item.upload]), showSymbol: false, smooth: 0.25, itemStyle: { color: '#34d399' }, lineStyle: { width: 2, color: '#34d399' }, areaStyle: { color: 'rgba(52,211,153,.12)' } },
    ],
  }, true)
  const totals = cumulative.value.download + cumulative.value.upload
  charts[1].setOption({
    animation: false,
    title: chartBase('累计流量').title,
    // 没有流量时只显示占位圆环，不要把占位用的 1 当成流量报出来。
    tooltip: { ...chartTooltip, trigger: 'item', formatter: ({ name, value }) => (totals > 0 ? `${name}<br>${formatBytes(value)}` : name) },
    legend: { ...chartLegend, show: totals > 0 },
    series: [{
      type: 'pie',
      silent: totals === 0,
      // 引导线和标签要占掉环两边各 60px 上下，环得比原来收一档才不会被面板裁掉。
      radius: ['38%', '55%'],
      center: ['50%', '48%'],
      avoidLabelOverlap: true,
      ...donutLabels(({ value }) => formatBytes(value), totals > 0),
      itemStyle: { borderColor: '#0e1524', borderWidth: 2 },
      data: totals > 0 ? [
        { name: '下载', value: cumulative.value.download, itemStyle: { color: '#38bdf8' } },
        { name: '上传', value: cumulative.value.upload, itemStyle: { color: '#34d399' } },
      ] : [{ name: '暂无流量', value: 1, itemStyle: { color: chartInk.empty } }],
    }],
  }, true)
  charts[2].setOption({
    ...chartBase('活动连接'),
    tooltip: { ...chartTooltip, trigger: 'axis', formatter: (items) => `${timeLabel(items[0].axisValue)}<br>${items[0].marker}${items[0].seriesName} ${items[0].value[1]}` },
    legend: { ...chartLegend, data: ['连接数'] },
    xAxis: { ...axis, type: 'time', splitNumber: 5, axisLabel: { ...axis.axisLabel, hideOverlap: true, formatter: timeLabel } },
    yAxis: { ...axis, type: 'value', minInterval: 1 },
    series: [{ name: '连接数', type: 'line', data: connectionHistory.value.map((item) => [item.time, item.count]), showSymbol: false, smooth: 0.25, itemStyle: { color: '#a78bfa' }, lineStyle: { width: 2, color: '#a78bfa' }, areaStyle: { color: 'rgba(167,139,250,.14)' } }],
  }, true)
  const protocolCounts = Object.entries(fleetTotals.value?.protocols || {})
  charts[3].setOption({
    animation: false,
    title: chartBase('传输协议').title,
    tooltip: {
      ...chartTooltip,
      trigger: 'item',
      formatter: ({ name, value, percent }) => (protocolCounts.length ? `${name}<br>${value} 条连接（${percent}%）` : name),
    },
    legend: { ...chartLegend, show: protocolCounts.length > 0 },
    series: [{
      type: 'pie',
      silent: protocolCounts.length === 0,
      radius: ['38%', '55%'],
      center: ['50%', '48%'],
      avoidLabelOverlap: true,
      ...donutLabels(({ value }) => `${value} 条`, protocolCounts.length > 0),
      itemStyle: { borderColor: '#0e1524', borderWidth: 2 },
      data: protocolCounts.length
        ? protocolCounts.map(([name, value]) => ({ name, value, itemStyle: { color: protocolColors[name] || '#64748b' } }))
        : [{ name: '暂无连接', value: 1, itemStyle: { color: chartInk.empty } }],
    }],
  }, true)
  const popular = fleetTotals.value?.popular_nodes || []
  charts[4].setOption({
    ...chartBase(`热门节点（最近 ${Number(fleetTotals.value?.popular_window_minutes) || 10} 分钟）`),
    grid: { left: 148, right: 52, top: 58, bottom: 24 },
    tooltip: { ...chartTooltip, trigger: 'axis', formatter: (items) => `${items[0].name}<br>${items[0].value} 次连接` },
    xAxis: { ...axis, type: 'value', minInterval: 1 },
    // 这一列是名称而不是刻度，用标题的亮度写，暗一档在深底上就糊了。
    yAxis: { ...axis, type: 'category', inverse: true, data: popular.map((item) => item.name), axisLabel: { ...axis.axisLabel, color: chartInk.title, width: 128, overflow: 'truncate' } },
    series: [{
      type: 'bar',
      data: popular.map((item) => item.count),
      barMaxWidth: 16,
      // 数字写在柱子末端：横条上比长短要来回对轴上的刻度，直接写出来省这一步。
      label: { show: true, position: 'right', color: chartInk.title, fontSize: 11, formatter: '{c}' },
      itemStyle: { color: '#22d3ee', borderRadius: [0, 3, 3, 0] },
    }],
  }, true)
}

// One beat of the reporting grid, one point. The master publishes the fleet
// total once per round, after every node's push for that round has landed, so
// the browser has nothing left to add up: it plots what arrives. This is what
// keeps the line an account of traffic rather than of reporting phases — the
// browser used to sum whatever had arrived and append a point per push, which
// meant each node's reading was drawn into several consecutive points.
// Two minutes of history at the one-second cadence the nodes report on.
const historyLength = 120
function appendTotals(totals) {
  // 协议分布和热门节点是当轮的即时数字，每一轮都照画；只有两条曲线要挑，
  // 因为整个舰队都还没测出速率时的那个 0 是"不知道"，不是"没有流量"，把它
  // 画进去会留下一个从未发生过的凹陷。
  if (totals.has_rates || Number(totals.nodes) === 0) {
    const at = Date.parse(totals.at) || Date.now()
    trafficHistory.value = [...trafficHistory.value, {
      time: at, download: Number(totals.download_rate || 0), upload: Number(totals.upload_rate || 0),
    }].slice(-historyLength)
    connectionHistory.value = [...connectionHistory.value, { time: at, count: Number(totals.connection_count || 0) }].slice(-historyLength)
  }
  scheduleRender()
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
  // 国旗字体是异步加载的，画布上已经画好的字不会自己更新，就绪后补画一次。
  document.fonts?.ready.then(scheduleRender)
  stopTotals = subscribeFleetTotals(appendTotals)
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node' || event.kind === 'task') scheduleLoad()
  })
})

onBeforeUnmount(() => {
  stopLive?.()
  stopTotals?.()
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
      <el-alert v-if="offline > 0 && dismissedOffline !== offline" :title="`${offline} 台服务器离线`" type="warning" show-icon closable @close="dismiss('sb_dashboard_offline', dismissedOffline, offline)" />

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
.chart-panel {
  min-width: 0;
  min-height: 0;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  box-shadow: var(--sb-shadow);
  overflow: hidden;
}
.chart-panel--wide { grid-column: span 2; }
@media (max-width: 1180px) { .chart-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); grid-template-rows: repeat(3, minmax(190px, 1fr)); } .chart-panel--wide { grid-column: span 2; } }
@media (max-width: 720px) { .chart-grid { grid-template-columns: 1fr; grid-template-rows: repeat(5, minmax(220px, 1fr)); } .chart-panel--wide { grid-column: auto; } }
</style>
