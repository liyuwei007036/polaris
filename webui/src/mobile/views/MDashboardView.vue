<script setup>
import { computed, inject, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TitleComponent, TooltipComponent } from 'echarts/components'
import { init, use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { api } from '../../api'
import { formatBytes } from '../../format'
import { subscribeLive } from '../../live'
import { fleetTotals, subscribeFleetTotals } from '../../connections'
import { FLAG_FONT_FAMILY } from '../../flags'
import { isDark } from '../../theme'
import MPage from '../components/MPage.vue'

use([BarChart, LineChart, PieChart, GridComponent, LegendComponent, TitleComponent, TooltipComponent, CanvasRenderer])

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const navigate = inject('navigate')
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

const protocolColors = { TCP: '#0284C7', UDP: '#059669', 其他: '#64748B' }

const chartInk = computed(() => (isDark.value ? {
  title: '#F1F3F7',
  label: '#8E93A6',
  line: 'rgba(255, 255, 255, 0.12)',
  split: 'rgba(255, 255, 255, 0.06)',
  empty: 'rgba(255, 255, 255, 0.1)',
  pieBorder: '#15181F',
  down: '#38BDF8',
  downArea: 'rgba(56, 189, 248, 0.14)',
  up: '#818CF8',
  upArea: 'rgba(129, 140, 248, 0.14)',
  conn: '#A78BFA',
  connArea: 'rgba(167, 139, 250, 0.14)',
} : {
  title: '#0F172A',
  label: '#64748B',
  line: 'rgba(0, 0, 0, 0.1)',
  split: 'rgba(0, 0, 0, 0.05)',
  empty: 'rgba(0, 0, 0, 0.1)',
  pieBorder: '#FFFFFF',
  down: '#0284C7',
  downArea: 'rgba(2, 132, 199, 0.12)',
  up: '#6366F1',
  upArea: 'rgba(99, 102, 241, 0.12)',
  conn: '#8B5CF6',
  connArea: 'rgba(139, 92, 246, 0.12)',
}))

const chartTooltip = computed(() => (isDark.value ? {
  backgroundColor: '#1B1E26',
  borderColor: 'rgba(255, 255, 255, 0.14)',
  textStyle: { color: chartInk.value.title, fontSize: 12 },
} : {
  backgroundColor: '#FFFFFF',
  borderColor: 'rgba(0, 0, 0, 0.12)',
  textStyle: { color: chartInk.value.title, fontSize: 12 },
}))

const chartLegend = computed(() => ({
  bottom: 4,
  itemWidth: 14,
  itemHeight: 8,
  textStyle: { color: chartInk.value.label, fontSize: 11 },
}))

function donutLabels(formatter, show) {
  return {
    label: show
      ? { show: true, color: chartInk.value.title, fontSize: 11, formatter }
      : { show: false },
    labelLine: show
      ? { show: true, length: 12, length2: 12, lineStyle: { color: chartInk.value.line } }
      : { show: false },
  }
}

const historyLength = 60
let stopLive
let stopTotals
let refreshTimer
let resizeObserver
let renderQueued = false

const online = computed(() => appState.nodes.filter((node) => node.online).length)
const offline = computed(() => appState.nodes.length - online.value)
const activeConnections = computed(() => Number(fleetTotals.value?.connection_count || 0))

const metrics = computed(() => [
  { label: '服务器总数', value: appState.nodes.length, view: 'nodes', query: '', tone: 'primary' },
  { label: '在线服务器', value: online.value, view: 'nodes', query: 'status=online', tone: 'ok' },
  { label: '活动连接', value: activeConnections.value, view: 'connections', query: '', tone: 'info' },
  { label: '节点数量', value: endpointCount.value, view: 'inbounds', query: '', tone: 'accent' },
])

function readDismissed(key) {
  const stored = Number(window.localStorage.getItem(key))
  return Number.isFinite(stored) ? stored : -1
}

function dismissOffline() {
  dismissedOffline.value = offline.value
  window.localStorage.setItem('sb_dashboard_offline', String(offline.value))
}

function chartBase(title) {
  return {
    animation: false,
    title: {
      text: title,
      left: 14,
      top: 12,
      textStyle: { color: chartInk.value.title, fontSize: 13, fontWeight: 600 },
    },
    tooltip: { ...chartTooltip.value, trigger: 'axis', confine: true },
    grid: { left: 46, right: 14, top: 46, bottom: 44 },
    textStyle: { fontFamily: `"${FLAG_FONT_FAMILY}", Inter, Segoe UI, Microsoft YaHei, sans-serif` },
  }
}

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
  const ink = chartInk.value
  const tip = chartTooltip.value
  const leg = chartLegend.value
  const axis = {
    axisLine: { lineStyle: { color: ink.line } },
    axisTick: { show: false },
    axisLabel: { color: ink.label, fontSize: 10 },
    splitLine: { lineStyle: { color: ink.split } },
  }

  charts[0].setOption({
    ...chartBase('实时流量速率'),
    tooltip: {
      ...tip,
      trigger: 'axis',
      confine: true,
      formatter: (items) => [timeLabel(items[0].axisValue), ...items.map((item) => `${item.marker}${item.seriesName} ${formatBytes(item.value[1], '/s')}`)].join('<br>'),
    },
    legend: { ...leg, data: ['下载', '上传'] },
    xAxis: { ...axis, type: 'time', splitNumber: 3, axisLabel: { ...axis.axisLabel, hideOverlap: true, formatter: (value) => timeLabel(value).slice(3) } },
    yAxis: { ...axis, type: 'value', minInterval: 1, axisLabel: { ...axis.axisLabel, formatter: compactRate } },
    series: [
      { name: '下载', type: 'line', data: trafficHistory.value.map((item) => [item.time, item.download]), showSymbol: false, smooth: 0.25, itemStyle: { color: ink.down }, lineStyle: { width: 2, color: ink.down }, areaStyle: { color: ink.downArea } },
      { name: '上传', type: 'line', data: trafficHistory.value.map((item) => [item.time, item.up || item.upload]), showSymbol: false, smooth: 0.25, itemStyle: { color: ink.up }, lineStyle: { width: 2, color: ink.up }, areaStyle: { color: ink.upArea } },
    ],
  }, true)

  const totals = cumulative.value.download + cumulative.value.upload
  charts[1].setOption({
    animation: false,
    title: chartBase('累计流量').title,
    tooltip: { ...tip, trigger: 'item', confine: true, formatter: ({ name, value }) => (totals > 0 ? `${name}<br>${formatBytes(value)}` : name) },
    legend: { ...leg, show: totals > 0 },
    series: [{
      type: 'pie',
      silent: totals === 0,
      radius: ['40%', '58%'],
      center: ['50%', '48%'],
      avoidLabelOverlap: true,
      ...donutLabels(({ value }) => formatBytes(value), totals > 0),
      itemStyle: { borderColor: ink.pieBorder, borderWidth: 2 },
      data: totals > 0 ? [
        { name: '下载', value: cumulative.value.download, itemStyle: { color: ink.down } },
        { name: '上传', value: cumulative.value.upload, itemStyle: { color: ink.up } },
      ] : [{ name: '暂无流量', value: 1, itemStyle: { color: ink.empty } }],
    }],
  }, true)

  charts[2].setOption({
    ...chartBase('活动连接趋势'),
    tooltip: { ...tip, trigger: 'axis', confine: true, formatter: (items) => `${timeLabel(items[0].axisValue)}<br>${items[0].marker}${items[0].seriesName} ${items[0].value[1]}` },
    legend: { ...leg, data: ['连接数'] },
    xAxis: { ...axis, type: 'time', splitNumber: 3, axisLabel: { ...axis.axisLabel, hideOverlap: true, formatter: (value) => timeLabel(value).slice(3) } },
    yAxis: { ...axis, type: 'value', minInterval: 1 },
    series: [{ name: '连接数', type: 'line', data: connectionHistory.value.map((item) => [item.time, item.count]), showSymbol: false, smooth: 0.25, itemStyle: { color: ink.conn }, lineStyle: { width: 2, color: ink.conn }, areaStyle: { color: ink.connArea } }],
  }, true)

  const protocolCounts = Object.entries(fleetTotals.value?.protocols || {})
  charts[3].setOption({
    animation: false,
    title: chartBase('传输协议分布').title,
    tooltip: {
      ...tip,
      trigger: 'item',
      confine: true,
      formatter: ({ name, value, percent }) => (protocolCounts.length ? `${name}<br>${value} 条连接（${percent}%）` : name),
    },
    legend: { ...leg, show: protocolCounts.length > 0 },
    series: [{
      type: 'pie',
      silent: protocolCounts.length === 0,
      radius: ['40%', '58%'],
      center: ['50%', '48%'],
      avoidLabelOverlap: true,
      ...donutLabels(({ value }) => `${value} 条`, protocolCounts.length > 0),
      itemStyle: { borderColor: ink.pieBorder, borderWidth: 2 },
      data: protocolCounts.length
        ? protocolCounts.map(([name, value]) => ({ name, value, itemStyle: { color: protocolColors[name] || '#64748b' } }))
        : [{ name: '暂无连接', value: 1, itemStyle: { color: ink.empty } }],
    }],
  }, true)

  const popular = fleetTotals.value?.popular_nodes || []
  charts[4].setOption({
    ...chartBase(`热门节点（近 ${Number(fleetTotals.value?.popular_window_minutes) || 10} 分钟）`),
    grid: { left: 110, right: 44, top: 46, bottom: 18 },
    tooltip: { ...tip, trigger: 'axis', confine: true, formatter: (items) => `${items[0].name}<br>${items[0].value} 次连接` },
    xAxis: { ...axis, type: 'value', minInterval: 1 },
    yAxis: { ...axis, type: 'category', inverse: true, data: popular.map((item) => item.name), axisLabel: { ...axis.axisLabel, color: ink.title, width: 94, overflow: 'truncate' } },
    series: [{
      type: 'bar',
      data: popular.map((item) => item.count),
      barMaxWidth: 14,
      label: { show: true, position: 'right', color: ink.title, fontSize: 10, formatter: '{c}' },
      itemStyle: { color: ink.down, borderRadius: [0, 3, 3, 0] },
    }],
  }, true)
}

watch(isDark, () => {
  scheduleRender()
})

function appendTotals(totals) {
  if (totals.has_rates || Number(totals.nodes) === 0) {
    const at = Date.parse(totals.at) || Date.now()
    trafficHistory.value = [...trafficHistory.value, {
      time: at,
      download: Number(totals.download_rate || 0),
      upload: Number(totals.upload_rate || 0),
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
    endpointCount.value = (listenerResult.listeners || []).length
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
  document.fonts?.ready.then(scheduleRender)
  stopTotals = subscribeFleetTotals(appendTotals)
  stopLive = subscribeLive((event) => {
    if (event.kind === 'task') scheduleLoad()
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
  <MPage title="运行概览" :loading="loading">
    <template #actions>
      <el-button
        circle
        size="small"
        :icon="Refresh"
        :loading="refreshing"
        aria-label="刷新数据"
        @click="load(false)"
      />
    </template>

    <div v-if="offline > 0 && dismissedOffline !== offline" class="m-notice m-notice--warning offline">
      <button type="button" class="offline__go" @click="navigate('nodes', 'status=offline')">
        {{ offline }} 台服务器离线，点击查看排障 ›
      </button>
      <button type="button" class="offline__skip" @click="dismissOffline">忽略</button>
    </div>

    <!-- 顶部四格指标卡 -->
    <section class="metrics">
      <button
        v-for="metric in metrics"
        :key="metric.label"
        type="button"
        class="metric neu"
        :class="`tone-${metric.tone}`"
        @click="navigate(metric.view, metric.query)"
      >
        <span class="metric__label">{{ metric.label }}</span>
        <strong class="metric__val font-mono">{{ metric.value }}</strong>
      </button>
    </section>

    <!-- 图表容器 -->
    <div ref="trafficChart" class="chart chart--tall neu-sm" />
    <div class="chart-pair">
      <div ref="totalChart" class="chart neu-sm" />
      <div ref="networkChart" class="chart neu-sm" />
    </div>
    <div ref="connectionChart" class="chart neu-sm" />
    <div ref="proxyChart" class="chart chart--bars neu-sm" />
  </MPage>
</template>

<style scoped>
.offline {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 6px 12px;
  margin-bottom: 12px;
}
.offline button {
  min-height: 36px;
  color: inherit;
  background: none;
  border: 0;
  font: inherit;
  cursor: pointer;
}
.offline__go {
  flex: 1;
  min-width: 0;
  padding: 0;
  font-size: 13px;
  font-weight: 550;
  text-align: left;
}
.offline__skip {
  flex: none;
  min-width: 40px;
  padding: 0 4px;
  font-size: 12px;
  text-decoration: underline;
  opacity: 0.8;
}

/* 四格指标卡：优雅微拟态 + 响应式布局 */
.metrics {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 12px;
}
.metric {
  position: relative;
  padding: 12px 14px;
  overflow: hidden;
  text-align: left;
  border-radius: var(--m-radius);
  font: inherit;
  cursor: pointer;
  transition: transform 0.12s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.metric:active {
  transform: scale(0.985);
}
.metric::before {
  content: "";
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  height: 2px;
  background: linear-gradient(90deg, var(--sb-accent), color-mix(in oklab, var(--sb-accent-2) 55%, transparent) 45%, transparent);
}
.metric.tone-ok::before {
  background: linear-gradient(90deg, var(--sb-success), transparent);
}
.metric.tone-info::before {
  background: linear-gradient(90deg, var(--sb-info), transparent);
}
.metric__label {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--sb-muted);
  font-size: 11.5px;
  font-weight: 550;
  letter-spacing: .3px;
}
.metric__label::after {
  content: "›";
  font-size: 14px;
  color: var(--sb-muted);
  opacity: 0.7;
}
.metric__val {
  display: block;
  margin-top: 4px;
  color: var(--sb-text);
  font-size: 22px;
  font-weight: 700;
  line-height: 1.2;
}

.chart {
  height: 195px;
  margin-bottom: 10px;
  border-radius: var(--m-radius);
  overflow: hidden;
}
.chart--tall { height: 215px; }
.chart--bars { height: 225px; }
.chart-pair .chart { height: 190px; }
</style>
