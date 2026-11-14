<template>
  <div class="node-detail">
    <!-- 头部：ref NodeDetail 同款，返回 + 名称 + 地区 + 状态 + agent 徽标 + 带宽 -->
    <div class="detail-head">
      <button type="button" class="back-btn neu-track neu-press" @click="$emit('back')">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-4">
          <polyline points="15 18 9 12 15 6" />
        </svg>
        返回
      </button>
      <h2 class="detail-name truncate">{{ node.name }}</h2>
      <span class="country-label">
        <span class="country-flag">{{ flag }}</span>
        <span :title="fullLocationTip">{{ locationName }}</span>
      </span>
      <span class="status-pill" :class="node.online ? 'is-online' : 'is-offline'">
        <span class="status-dot-pulse" />
        <span>{{ node.online ? '在线' : '离线' }}</span>
        <span v-if="node.online && agentUptimeText" class="status-uptime">{{ agentUptimeText }}</span>
      </span>
      <span v-if="node.bandwidth" class="bw-badge font-mono">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
          <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
        </svg>
        <span>带宽 {{ formatBandwidth(node.bandwidth) }}</span>
      </span>
      <span v-if="node.agent_version" class="agent-badge font-mono" :class="{ 'has-upgrade': agentUpdateAvailable }">
        agent {{ node.agent_version }}
        <span v-if="agentUpdateAvailable" class="agent-upgrade-hint">可升级至 v{{ appState?.systemUpdate?.latest_version }}</span>
      </span>

      <div class="detail-actions">
        <button
          v-if="isAdmin && agentUpdateAvailable"
          type="button"
          class="action-btn action-btn--upgrade neu-track neu-press"
          :title="`升级 Agent 到 v${appState?.systemUpdate?.latest_version}`"
          @click="$emit('upgrade-agent', node)"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
            <polyline points="18 15 12 9 6 15" />
          </svg>
          升级 Agent
        </button>
        <button
          v-if="isAdmin && singBoxUpdateAvailable"
          type="button"
          class="action-btn action-btn--upgrade neu-track neu-press"
          :title="`升级 sing-box 到 ${singBoxLatest?.[node.architecture]}`"
          @click="$emit('upgrade-singbox', node)"
        >
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
            <polyline points="18 15 12 9 6 15" />
          </svg>
          升级 sing-box
        </button>
        <button v-if="canWrite" type="button" class="action-btn neu-track neu-press" @click="$emit('edit', node)">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
            <path d="M12 20h9" />
            <path d="M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4L16.5 3.5z" />
          </svg>
          配置
        </button>
        <button v-if="canWrite && node.online" type="button" class="action-btn neu-track neu-press" @click="$emit('probe', node)">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
            <circle cx="12" cy="12" r="10" />
            <path d="m4.93 4.93 4.24 4.24" />
            <path d="m14.83 9.17 4.24-4.24" />
            <path d="m14.83 14.83 4.24 4.24" />
            <path d="m9.17 14.83-4.24 4.24" />
          </svg>
          排查
        </button>
      </div>
    </div>

    <!-- 四个概览指标 -->
    <div class="stat-grid">
      <div class="stat-card neu">
        <span class="stat-label">CPU 使用率</span>
        <strong class="stat-value">{{ host.cpu_percent != null ? `${Number(host.cpu_percent).toFixed(1)}%` : '—' }}</strong>
        <span class="stat-sub">{{ host.cpu_percent != null ? `负载 ${loadText}` : '等待上报' }}</span>
      </div>
      <div class="stat-card neu">
        <span class="stat-label">内存 / 硬盘</span>
        <strong class="stat-value">{{ memoryPct }} / {{ diskPct }}</strong>
        <span class="stat-sub">{{ formatBytes(host.memory_used) }} / {{ formatBytes(host.disk_used) }}</span>
      </div>
      <div class="stat-card neu">
        <span class="stat-label">活跃连接</span>
        <strong class="stat-value text-primary font-mono">{{ activeConnections }}</strong>
        <span class="stat-sub">系统 TCP {{ host.tcp ?? 0 }} · UDP {{ host.udp ?? 0 }}</span>
      </div>
      <div class="stat-card neu">
        <span class="stat-label">当前延迟</span>
        <strong class="stat-value" :class="latencyTone">{{ latencyText }}</strong>
        <span class="stat-sub">{{ latencySub }}</span>
      </div>
    </div>

    <!-- 标签与时间范围 -->
    <div class="detail-tabs">
      <div class="seg neu-inset">
        <button
          v-for="item in TABS"
          :key="item.key"
          type="button"
          class="seg-btn"
          :class="{ active: tab === item.key }"
          @click="tab = item.key"
        >
          {{ item.label }}
        </button>
      </div>
      <div v-if="tab !== 'facts'" class="seg neu-inset">
        <button
          v-for="range in ranges"
          :key="range.hours"
          type="button"
          class="seg-btn"
          :class="{ active: hours === range.hours }"
          @click="hours = range.hours"
        >
          {{ range.label }}
        </button>
      </div>
      <span class="tabs-spacer" />
      <span class="tabs-note">{{ tabsNote }}</span>
    </div>

    <!-- 资源 -->
    <div v-if="tab === 'resources'" class="chart-grid">
      <section class="chart-panel neu">
        <div class="chart-panel-head">
          <h4>CPU 使用率</h4>
        </div>
        <TrendChart :points="series.cpu" name="CPU 使用率" color="var(--sb-chart-1)" :min="0" :max="100" :format="formatCpuTick" />
      </section>
      <section class="chart-panel neu">
        <div class="chart-panel-head">
          <h4>内存 · {{ formatBytes(host.memory_total) }}</h4>
        </div>
        <TrendChart :points="series.mem" name="内存使用" color="var(--sb-chart-2)" :min="0" :max="host.memory_total || undefined" :format="(v) => formatBytes(v)" />
      </section>
      <section class="chart-panel neu">
        <div class="chart-panel-head">
          <h4>网络速率</h4>
          <div class="chart-legend">
            <span class="legend-chip"><span class="legend-swatch" style="background: var(--sb-info)" />下行</span>
            <span class="legend-chip"><span class="legend-swatch" style="background: var(--sb-chart-4)" />上行</span>
          </div>
        </div>
        <TrendChart
          :series="[
            { name: '下行', points: series.rx, color: 'var(--sb-info)' },
            { name: '上行', points: series.tx, color: 'var(--sb-chart-4)' },
          ]"
          :min="0"
          :format="(v) => formatBytes(v, '/s')"
        />
      </section>
      <section class="chart-panel neu">
        <div class="chart-panel-head">
          <h4>硬盘 · {{ formatBytes(host.disk_total) }}</h4>
        </div>
        <TrendChart :points="series.disk" name="硬盘使用" color="var(--sb-chart-3)" :min="0" :max="host.disk_total || undefined" :format="(v) => formatBytes(v)" />
      </section>
    </div>

    <!-- 网络延迟 -->
    <div v-else-if="tab === 'latency'" class="latency-tab">
      <LatencyPanel v-if="effectiveLatency" :latency="effectiveLatency" />
      <p v-if="latencyFailed" class="detail-empty">读取历史数据失败：{{ latencyFailed }}</p>
      <p v-else-if="latencyLoading && !probes.length" class="detail-empty">正在读取延迟历史…</p>
      <p v-else-if="!probes.length" class="detail-empty">
        {{ latency ? '这段时间没有延迟数据' : '离线服务器暂无探针数据' }}
      </p>

      <!-- 多探测链路曲线：与 ref NodeDetail 的 ComposedChart 对应，
           行内“仅看/隐藏”直接作用于这里的曲线集合。 -->
      <section v-if="probes.length" class="chart-panel neu latency-chart">
        <div class="probe-panel-head">
          <h4>链路延迟</h4>
          <span>{{ shownProbes.length === 1 ? shownProbes[0].name : `显示 ${shownProbes.length} / ${probes.length} 个探测` }}</span>
        </div>
        <TrendChart
          v-if="shownProbes.length"
          :series="latencySeries"
          :height="230"
          :area="false"
          :min="0"
          :format="(v) => `${Math.round(v)} ms`"
        />
        <p v-else class="detail-empty">已隐藏全部探测曲线</p>
      </section>

      <section v-if="probes.length" class="probe-panel neu">
        <div class="probe-panel-head">
          <h4>多目标延迟</h4>
          <span>点击整行显示 / 隐藏曲线</span>
        </div>
        <div class="probe-row probe-row--head">
          <span>探测目标</span>
          <span class="right">当前</span>
          <span class="right">平均</span>
          <span class="right">波动</span>
          <span class="right">丢包</span>
          <span class="right">仅看</span>
        </div>
        <div
          v-for="probe in probes"
          :key="probe.id"
          class="probe-row"
          :class="{ 'is-hidden': only === null ? hiddenProbes.includes(probe.id) : only !== probe.id, 'is-active': only === probe.id }"
          role="button"
          tabindex="0"
          @click="toggleProbe(probe.id)"
          @keydown.enter.prevent="toggleProbe(probe.id)"
        >
          <span class="probe-name">
            <svg width="16" height="6" aria-hidden><line x1="0" y1="3" x2="16" y2="3" :stroke="probeColor(probe.id)" stroke-width="2" /></svg>
            <span class="truncate">{{ probe.name || `探测 ${probe.id}` }}</span>
          </span>
          <span class="right tnum" :class="toneOf(probe.latency, probe.loss)">{{ probe.latency == null ? '超时' : `${Math.round(probe.latency)} ms` }}</span>
          <span class="right tnum" :class="toneOf(probe.avgLatency, probe.loss)">{{ probe.avgLatency == null ? '—' : `${Math.round(probe.avgLatency)} ms` }}</span>
          <span class="right tnum" :class="toneOf(probe.jitter)">{{ probe.jitter == null ? '—' : `${Math.round(probe.jitter)} ms` }}</span>
          <span class="right tnum" :class="toneOf(probe.loss)">{{ lossText(probe.loss) }}</span>
          <span class="right">
            <button
              type="button"
              class="only-btn"
              :class="{ active: only === probe.id }"
              @click.stop="only = only === probe.id ? null : probe.id"
            >
              {{ only === probe.id ? '取消' : '仅看' }}
            </button>
          </span>
        </div>
      </section>
    </div>

    <!-- 详情：硬件 / 运行 / 网络 / 服务商与套餐 / DDNS / 出口别名 -->
    <div v-else class="fact-grid">
      <section v-for="group in factGroups" :key="group.label" class="fact-card neu">
        <div class="fact-card-head">
          <h4 class="fact-title">
            <span class="fact-icon-badge">
              <svg v-if="group.icon === 'cpu'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <rect x="4" y="4" width="16" height="16" rx="2" />
                <rect x="9" y="9" width="6" height="6" />
                <path d="M15 2v2M9 2v2M15 20v2M9 20v2M20 15h2M20 9h2M2 15h2M2 9h2" />
              </svg>
              <svg v-else-if="group.icon === 'activity'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
              </svg>
              <svg v-else-if="group.icon === 'network'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <polyline points="7 10 12 15 17 10" />
                <line x1="12" y1="15" x2="12" y2="3" />
                <polyline points="17 14 12 9 7 14" />
                <line x1="12" y1="9" x2="12" y2="21" />
              </svg>
              <svg v-else-if="group.icon === 'wallet'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <path d="M21 12V7H5a2 2 0 0 1 0-4h14v4" />
                <path d="M3 5v14a2 2 0 0 0 2 2h16v-5" />
                <path d="M18 12a2 2 0 0 0 0 4h4v-4Z" />
              </svg>
              <svg v-else-if="group.icon === 'dns'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <circle cx="12" cy="12" r="10" />
                <line x1="2" y1="12" x2="22" y2="12" />
                <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
              </svg>
              <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5">
                <rect x="2" y="2" width="20" height="8" rx="2" />
                <rect x="2" y="14" width="20" height="8" rx="2" />
                <line x1="6" y1="6" x2="6.01" y2="6" />
                <line x1="6" y1="18" x2="6.01" y2="18" />
              </svg>
            </span>
            <span>{{ group.label }}</span>
          </h4>
          <button
            v-if="canWrite && group.action"
            type="button"
            class="fact-action-btn neu-track neu-press"
            :disabled="group.actionLoading"
            :title="group.actionText"
            :aria-label="group.actionText"
            @click="group.action"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3.5" :class="{ 'spin': group.actionLoading }">
              <path d="M21.5 2v6h-6M21.34 15.57a10 10 0 1 1-.57-8.38l5.67-5.67" />
            </svg>
          </button>
        </div>
        <dl>
          <template v-for="fact in group.facts" :key="fact.label">
            <dt>{{ fact.label }}</dt>
            <dd>
              <div v-if="fact.ip" class="egress-ip-item">
                <span class="egress-ip font-mono">{{ fact.ip }}</span>
                <span v-if="fact.alias" class="egress-alias">{{ fact.alias }}</span>
              </div>
              <div v-else-if="fact.domains" class="domain-list">
                <span v-for="domain in fact.domains" :key="domain" class="domain-chip font-mono">{{ domain }}</span>
              </div>
              <a
                v-else-if="fact.url"
                :href="fact.url"
                target="_blank"
                rel="noopener noreferrer"
                class="fact-link"
                :title="fact.value"
              >
                <span>{{ fact.value }}</span>
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="size-3 inline-icon">
                  <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
                  <polyline points="15 3 21 3 21 9" />
                  <line x1="10" y1="14" x2="21" y2="3" />
                </svg>
              </a>
              <span v-else-if="fact.badge" class="fact-badge" :class="fact.badgeClass">
                <span class="badge-dot" />
                {{ fact.value }}
              </span>
              <span v-else :title="fact.value" :class="fact.cls">{{ fact.value }}</span>
            </dd>
          </template>
        </dl>
      </section>
    </div>
  </div>
</template>

<script setup>
import { computed, inject, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import LatencyPanel from './LatencyPanel.vue'
import TrendChart from './TrendChart.vue'
import { api, post } from '../api'
import { formatDDNSBindSummary } from '../ddns'
import { formatBandwidth, formatBytes, formatUptime } from '../format'
import { summarizePing } from '../latency'
import { isShareMode } from '../share'
import { isNewerVersion } from '../version'

const props = defineProps({
  node: { type: Object, required: true },
  metrics: { type: Object, default: () => ({}) },
  live: { type: Object, default: null },
  singBoxLatest: { type: Object, default: () => ({}) },
})
defineEmits(['back', 'edit', 'probe', 'upgrade-agent', 'upgrade-singbox'])

const appState = inject('appState', null)
const isAdmin = inject('isAdmin', { value: false })
const canWrite = inject('canWrite', ref(false))

const agentUpdateAvailable = computed(() =>
  isNewerVersion(appState?.systemUpdate?.latest_version, props.node.agent_version),
)

const singBoxUpdateAvailable = computed(() => {
  const latest = props.singBoxLatest?.[props.node.architecture]
  return Boolean(latest && props.node.sing_box_version && props.node.sing_box_version !== latest)
})

const TABS = [
  { key: 'resources', label: '资源' },
  { key: 'latency', label: '网络延迟' },
  { key: 'facts', label: '详情' },
]
const RESOURCE_RANGES = [
  { hours: 1, label: '1 小时' },
  { hours: 6, label: '6 小时' },
  { hours: 24, label: '24 小时' },
  { hours: 168, label: '7 天' },
]
const LATENCY_RANGES = [
  { hours: 1, label: '1 小时' },
  { hours: 6, label: '6 小时' },
  { hours: 24, label: '24 小时' },
]

const tab = ref('resources')
const hours = ref(6)
const only = ref(null)
const hiddenProbes = ref([])
const history = ref([])
const pingHistory = ref(null)
const latencyLoading = ref(false)
const latencyFailed = ref('')
const ranges = computed(() => (tab.value === 'latency' ? LATENCY_RANGES : RESOURCE_RANGES))
const tabsNote = computed(() => {
  if (tab.value === 'latency') return `${probes.value.length} 个探测`
  if (tab.value === 'facts') return '硬件 · 网络 · 计费 · DDNS'
  return `最近 ${hours.value} 小时`
})

const host = computed(() => props.live?.host || props.metrics.host || {})
const latency = computed(() => props.live?.latency || props.metrics.latency || null)

const activeConnections = computed(() => {
  if (!props.node.online) return 0
  if (props.live?.connection_count !== undefined) return props.live.connection_count
  if (Array.isArray(props.metrics?.connections)) return props.metrics.connections.length
  return 0
})
// 与 ref 一致：延迟页的探测统计来自按所选时间范围拉取的原始 ping 历史，
// 列表页的 24 小时概览仍用实时上报里的汇总。
const pingSummary = computed(() => (pingHistory.value ? summarizePing(pingHistory.value) : null))
const probes = computed(() => {
  if (pingSummary.value) return pingSummary.value.probes
  return latency.value?.probes || []
})

const selectedProbe = computed(() => {
  if (only.value === null) return null
  return probes.value.find((p) => p.id === only.value) || null
})

const effectiveLatency = computed(() => {
  if (selectedProbe.value) {
    const p = selectedProbe.value
    return {
      latency: p.latency,
      loss: p.loss,
      probe: p.name,
      jitter: p.jitter,
      avgLatency: p.avgLatency ?? p.latency,
      avgLoss: p.avgLoss ?? p.loss,
      probes: [p],
      hours: p.hours || [],
    }
  }
  if (shownProbes.value.length === 1) {
    const p = shownProbes.value[0]
    return {
      latency: p.latency,
      loss: p.loss,
      probe: p.name,
      jitter: p.jitter,
      avgLatency: p.avgLatency ?? p.latency,
      avgLoss: p.avgLoss ?? p.loss,
      probes: [p],
      hours: p.hours || [],
    }
  }
  if (pingSummary.value) return pingSummary.value
  return latency.value
})

function getNodeFlag(name) {
  if (!name) return '🌐'
  const upper = name.toUpperCase()
  if (upper.includes('TW') || upper.includes('台湾')) return '🇹🇼'
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
    upper.includes('西雅图')
  )
    return '🇺🇸'
  if (upper.includes('UK') || upper.includes('英国') || upper.includes('伦敦')) return '🇬🇧'
  if (upper.includes('DE') || upper.includes('德国') || upper.includes('法兰克福')) return '🇩🇪'
  if (upper.includes('FR') || upper.includes('法国') || upper.includes('巴黎')) return '🇫🇷'
  if (upper.includes('CA') || upper.includes('加拿大')) return '🇨🇦'
  if (upper.includes('AU') || upper.includes('澳大利亚') || upper.includes('悉尼')) return '🇦🇺'
  return '🌐'
}

const flag = computed(() => {
  const code = props.node.country_code || props.node.country
  if (code && /^[A-Za-z]{2}$/.test(code)) {
    const f = countryFlag(code)
    if (f && f !== '🌐') return f
  }
  return getNodeFlag(props.node.name)
})

const locationName = computed(() => {
  if (props.node.city && props.node.city.trim()) {
    return props.node.city.trim()
  }
  if (props.node.location && props.node.location.trim()) {
    const loc = props.node.location.trim()
    if (loc !== '未知' && loc !== '本机' && loc !== '内网') {
      const parts = loc.split(/\s+/)
      if (parts.length >= 2) {
        return parts[parts.length - 1]
      }
      return loc
    }
  }
  const code = props.node.country || props.node.country_code
  if (code && /^[A-Za-z]{2}$/.test(code)) {
    try {
      const dn = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
      return dn.of(code.toUpperCase()) || code
    } catch {
      return code
    }
  }
  return '未知位置'
})

const fullLocationTip = computed(() => {
  return props.node.location || props.node.city || locationName.value
})
// The header's "在线" duration is how long this agent process has been running.
// The host's boot uptime is a different number (a VPS can be up for months
// while its agent was restarted by an upgrade) and belongs on the facts card.
const agentUptimeText = computed(() =>
  host.value.agent_uptime_seconds ? formatUptime(host.value.agent_uptime_seconds) : '',
)
const loadText = computed(() => {
  const load = host.value.load_avg
  return Array.isArray(load) ? load.map((v) => Number(v).toFixed(2)).join(' ') : '—'
})
const memoryPct = computed(() => percent(host.value.memory_used, host.value.memory_total))
const diskPct = computed(() => percent(host.value.disk_used, host.value.disk_total))

const latencyText = computed(() => {
  const lat = effectiveLatency.value
  const value = lat?.latency ?? lat?.avgLatency ?? lat?.average_latency_ms
  return value == null ? '—' : `${Math.round(value)} ms`
})
const latencySub = computed(() => {
  const lat = effectiveLatency.value
  if (!lat || lat.none) return '无探测任务'
  const probeName = lat.probe || (Array.isArray(lat.targets) && lat.targets[0]?.target) || ''
  const jitterVal = lat.jitter ?? lat.jitter_ms
  const parts = []
  if (probeName) parts.push(probeName)
  if (jitterVal != null) parts.push(`波动 ${Math.round(jitterVal)} ms`)
  if (selectedProbe.value && selectedProbe.value.loss) parts.push(`丢包 ${Math.round(selectedProbe.value.loss)}%`)
  return parts.join(' · ') || '正常'
})
const latencyTone = computed(() => {
  const lat = effectiveLatency.value
  const val = lat?.latency ?? lat?.avgLatency ?? lat?.average_latency_ms
  const loss = lat?.loss ?? lat?.avgLoss ?? lat?.average_loss_percent
  return toneOf(val, loss)
})

function formatCpuTick(v) {
  return `${Number(v).toFixed(v % 1 === 0 ? 0 : 1)}%`
}

const series = computed(() => ({
  cpu: history.value.map((p) => ({ ts: p.ts, v: p.cpu })),
  mem: history.value.map((p) => ({ ts: p.ts, v: p.mem_used })),
  disk: history.value.map((p) => ({ ts: p.ts, v: p.disk_used })),
  rx: history.value.map((p) => ({ ts: p.ts, v: p.net_rx })),
  tx: history.value.map((p) => ({ ts: p.ts, v: p.net_tx })),
}))

function percent(used, total) {
  return total > 0 ? `${Math.round((used / total) * 100)}%` : '—'
}

function countryFlag(code) {
  const value = String(code || '').toUpperCase()
  if (value.length !== 2) return '🌐'
  return String.fromCodePoint(...[...value].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65))
}

// 与 ref 的 latencyTone/lossTone 阈值一致：延迟与丢包共用同一档位表。
function toneOf(value, loss) {
  if (loss != null && loss >= 10) return 'tone-bad'
  if (value == null) return 'tone-muted'
  if (value < 60) return 'tone-ok'
  if (value < 110) return 'tone-mild'
  if (value < 180) return 'tone-warn'
  if (value < 280) return 'tone-hot'
  return 'tone-bad'
}

function lossText(loss) {
  if (loss == null) return '—'
  if (loss > 0 && loss < 1) return '<1%'
  return `${Math.round(loss)}%`
}

const PROBE_COLORS = ['var(--sb-chart-1)', 'var(--sb-chart-2)', 'var(--sb-chart-3)', 'var(--sb-chart-4)', 'var(--sb-chart-5)']
function probeColor(id) {
  const index = probes.value.findIndex((p) => p.id === id)
  return PROBE_COLORS[(index < 0 ? 0 : index) % PROBE_COLORS.length]
}

// 与 ref 的 shownProbes 一致：选了“仅看”就只保留那一条，否则去掉被隐藏的探测。
const shownProbes = computed(() => {
  if (only.value !== null) return probes.value.filter((p) => p.id === only.value)
  return probes.value.filter((p) => !hiddenProbes.value.includes(p.id))
})

const latencySeries = computed(() =>
  shownProbes.value.map((probe) => ({
    name: probe.name,
    color: probeColor(probe.id),
    points: (probe.points || probe.hours || [])
      .map((slot) => ({ ts: slot.ts, v: slot.latency }))
      .filter((point) => Number.isFinite(point.v)),
  })),
)

function toggleProbe(id) {
  if (only.value !== null) return
  hiddenProbes.value = hiddenProbes.value.includes(id)
    ? hiddenProbes.value.filter((item) => item !== id)
    : [...hiddenProbes.value, id]
}

const PERIOD_LABELS = { day: '天', month: '月', quarter: '季', year: '年' }
const CURRENCY_SYMBOLS = { USD: '$', CNY: '¥', EUR: '€', CAD: 'C$' }

function getPriceText(node) {
  if (isShareMode) return ''
  if (!node.price_period) return ''
  if (node.price_period === 'free' || node.price === '0' || node.price === '0.00') return '免费'
  const symbol = CURRENCY_SYMBOLS[node.currency] || `${node.currency} `
  return `${symbol}${node.price || 0} / ${PERIOD_LABELS[node.price_period] || node.price_period}`
}

function getExpiryInfo(expiresOn) {
  if (isShareMode) return { text: '已隐藏', cls: '' }
  if (!expiresOn) return { text: '永不到期', cls: 'is-ok' }
  if (expiresOn === '2035-01-01') return { text: '永久免费', cls: 'is-ok' }
  const end = new Date(`${expiresOn}T00:00:00`)
  if (Number.isNaN(end.getTime())) return { text: expiresOn, cls: '' }
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const days = Math.round((end - today) / 86400000)
  if (days < 0) return { text: `${expiresOn} · 已过期 ${-days} 天`, cls: 'is-danger' }
  if (days === 0) return { text: `${expiresOn} · 今日到期`, cls: 'is-danger' }
  if (days <= 15) return { text: `${expiresOn} · 剩余 ${days} 天`, cls: 'is-danger' }
  if (days <= 60) return { text: `${expiresOn} · 剩余 ${days} 天`, cls: 'is-warn' }
  return { text: `${expiresOn} · 剩余 ${days} 天`, cls: 'is-ok' }
}

const syncingDDNS = ref(false)
async function triggerSyncDDNS() {
  syncingDDNS.value = true
  try {
    const res = await post(`/nodes/${props.node.id}/ddns/sync`, {})
    ElMessage.success(`DDNS 同步触发成功${res.ip ? '，当前解析 IP: ' + res.ip : ''}`)
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : 'DDNS 同步失败')
  } finally {
    syncingDDNS.value = false
  }
}

const factGroups = computed(() => {
  const node = props.node
  const m = host.value
  const groups = []

  // 1. 硬件配置
  groups.push({
    label: '硬件配置',
    icon: 'cpu',
    facts: [
      { label: '系统', value: [node.os_name || m.os_name || node.os, m.kernel].filter(Boolean).join(' · ') },
      { label: 'CPU', value: m.cpu_name ? `${m.cpu_name} × ${m.cpu_cores || 0}` : m.cpu_cores ? `${m.cpu_cores} 核` : '' },
      { label: '内存 / 硬盘', value: m.memory_total || m.disk_total ? `${formatBytes(m.memory_total)} / ${formatBytes(m.disk_total)}` : '' },
      { label: '交换分区', value: m.swap_total > 0 ? `${formatBytes(m.swap_used)} / ${formatBytes(m.swap_total)}` : '无' },
      { label: '架构', value: [node.architecture, m.virt && m.virt !== 'none' ? m.virt : ''].filter(Boolean).join(' · ') },
    ].filter((f) => f.value),
  })

  // 2. 运行状态
  groups.push({
    label: '运行状态',
    icon: 'activity',
    facts: [
      { label: '负载 (1/5/15 分)', value: Array.isArray(m.load_avg) ? m.load_avg.map((v) => Number(v).toFixed(2)).join('  ') : '' },
      { label: '系统套接字 / 进程', value: m.procs != null ? `TCP ${m.tcp} · UDP ${m.udp} · ${m.procs} 进程` : '' },
      { label: '开机时长', value: m.uptime_seconds ? formatUptime(m.uptime_seconds) : '' },
      {
        label: 'Agent',
        value: node.agent_version
          ? `v${String(node.agent_version).replace(/^v/, '')}${agentUpdateAvailable.value ? ` (可升级至 v${appState?.systemUpdate?.latest_version})` : ''}`
          : '',
      },
      {
        label: 'sing-box',
        value: node.sing_box_version
          ? `${node.sing_box_version}${singBoxUpdateAvailable.value ? ` (可升级至 ${props.singBoxLatest?.[node.architecture]})` : ''}`
          : '',
      },
    ].filter((f) => f.value),
  })

  // 3. 网络与流量
  const totals = props.metrics.proxy || props.metrics.node || {}
  const monthlyRx = totals.monthly_received_bytes || 0
  const monthlyTx = totals.monthly_sent_bytes || 0
  const netFacts = [
    { label: '本月流量', value: `↓ ${formatBytes(monthlyRx)} · ↑ ${formatBytes(monthlyTx)}` },
    { label: '累计流量', value: `↓ ${formatBytes(totals.received_bytes || 0)} · ↑ ${formatBytes(totals.sent_bytes || 0)}` },
    { label: '机器带宽', value: isShareMode ? '已隐藏' : formatBandwidth(node.bandwidth || '1 Gbps') },
    { label: '主机地址', value: node.client_address },
    { label: '地理位置', value: node.location || node.city },
  ]
  groups.push({
    label: '网络与流量',
    icon: 'network',
    facts: netFacts.filter((f) => f.value),
  })

  // 4. 服务商与计费套餐 (Provider & Billing)
  const billingFacts = []
  if (node.provider) {
    billingFacts.push({
      label: '服务商 / 厂商',
      value: node.provider,
      url: node.provider_url || '',
    })
  }
  const price = getPriceText(node)
  if (price) {
    billingFacts.push({ label: '续费价格', value: price })
  }
  const expiry = getExpiryInfo(node.expires_on)
  billingFacts.push({
    label: '到期时间',
    value: expiry.text,
    cls: expiry.cls,
  })
  if (node.traffic_plan) {
    billingFacts.push({ label: '流量配额', value: `${node.traffic_plan} / 月` })
  }
  if (node.bandwidth) {
    billingFacts.push({ label: '带宽规格', value: formatBandwidth(node.bandwidth) })
  }
  if (billingFacts.length) {
    groups.push({
      label: '服务商与套餐',
      icon: 'wallet',
      facts: billingFacts,
    })
  }

  // 5. 动态域名解析 (Cloudflare DDNS)
  const domains = (node.ddns_domains && node.ddns_domains.length ? node.ddns_domains : [node.ddns_domain]).filter(Boolean)
  const ddnsFacts = [
    {
      label: 'DDNS 状态',
      value: node.ddns_enabled ? '已开启自动同步' : '未开启',
      badge: true,
      badgeClass: node.ddns_enabled ? 'is-ok' : 'is-muted',
    },
    // Domains are rendered as separate chips so several records stay readable
    // instead of running together into one long line.
    domains.length
      ? { label: '绑定域名', domains }
      : { label: '绑定域名', value: node.ddns_enabled ? '等待绑定记录' : '未配置', cls: 'font-mono' },
    {
      label: '当前解析 IP',
      value: node.last_observed_ip || '等待首次检测同步',
      cls: 'font-mono',
    },
  ]
  if (node.ddns_enabled) {
    ddnsFacts.push({
      label: '绑定出口策略',
      value: formatDDNSBindSummary(node),
      cls: 'font-mono',
    })
  }
  groups.push({
    label: '动态域名解析',
    icon: 'dns',
    actionText: syncingDDNS.value ? '同步中…' : '立即同步',
    actionLoading: syncingDDNS.value,
    // Nothing to sync when DDNS is off, so the refresh control stays hidden.
    action: node.ddns_enabled ? triggerSyncDDNS : null,
    facts: ddnsFacts,
  })

  // 6. 出口 IP 与别名 (如果有别名或多 IP)
  // 出口 IP 只在这张卡片里出现一次；若节点还没有网卡上报列表，用最近观测到
  // 的公网 IP 兜底，避免「网络与流量」里删掉重复行后完全看不到出口地址。
  const observedFallback = (node.ip_addresses || []).length ? [] : [node.last_observed_ip].filter(Boolean)
  const allIPs = Array.from(new Set([...(node.ip_addresses || []), ...Object.keys(node.ip_aliases || {}), ...observedFallback]))
  if (allIPs.length > 0) {
    const aliasFacts = allIPs.map((ip) => {
      const alias = node.ip_aliases?.[ip]
      return {
        label: ip.includes(':') ? 'IPv6' : 'IPv4',
        ip,
        alias: alias ? alias.trim() : '',
      }
    })
    groups.push({
      label: '多出口 IP 与别名',
      icon: 'egress',
      facts: aliasFacts,
    })
  }

  return groups
})

async function loadHistory() {
  if (tab.value === 'facts') return
  if (tab.value === 'latency') {
    latencyLoading.value = true
    latencyFailed.value = ''
    try {
      const result = await api(`/nodes/${props.node.id}/history?hours=${hours.value}&series=ping&points=240`)
      pingHistory.value = result
    } catch (error) {
      pingHistory.value = null
      latencyFailed.value = error.message || '网络错误'
    } finally {
      latencyLoading.value = false
    }
    return
  }
  try {
    const [resResult, pingResult] = await Promise.allSettled([
      api(`/nodes/${props.node.id}/history?hours=${hours.value}`),
      api(`/nodes/${props.node.id}/history?hours=${hours.value}&series=ping&points=240`),
    ])
    if (resResult.status === 'fulfilled') {
      history.value = resResult.value.metrics || []
    } else {
      history.value = []
    }
    if (pingResult.status === 'fulfilled') {
      pingHistory.value = pingResult.value
    }
  } catch {
    history.value = []
  }
}

watch([hours, tab], loadHistory)
watch(
  () => props.node.id,
  () => {
    pingHistory.value = null
    only.value = null
    hiddenProbes.value = []
    history.value = []
    loadHistory()
  },
)
onMounted(loadHistory)
</script>

<style scoped>
.node-detail { display: flex; flex-direction: column; gap: 16px; }
.detail-head { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.back-btn { display: inline-flex; align-items: center; gap: 4px; height: 32px; padding: 0 12px; border: 1px solid var(--sb-line); border-radius: 10px; background: var(--sb-surface-2); color: var(--sb-muted); font-size: 13px; cursor: pointer; }
.back-btn:hover { color: var(--sb-text); }
.detail-name { margin: 0; font-size: 18px; font-weight: 650; color: var(--sb-text); }
.country-label { display: inline-flex; align-items: center; gap: 5px; font-size: 13px; color: var(--sb-muted); }
.status-uptime { margin-left: 4px; opacity: .8; font-weight: 500; }
.agent-badge { padding: 2px 8px; border: 1px solid var(--sb-line); border-radius: 999px; font-size: 11px; color: var(--sb-muted); }
.agent-badge.has-upgrade {
  border-color: color-mix(in oklab, var(--sb-warning, #f59e0b) 40%, transparent);
  color: var(--sb-warning, #f59e0b);
  background: color-mix(in oklab, var(--sb-warning, #f59e0b) 8%, transparent);
}
.agent-upgrade-hint {
  margin-left: 4px;
  font-weight: 600;
}
.bw-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  color: var(--sb-accent);
  background: color-mix(in oklab, var(--sb-accent) 12%, transparent);
  border: 1px solid color-mix(in oklab, var(--sb-accent) 30%, transparent);
  font-weight: 550;
}
.detail-actions { margin-left: auto; display: flex; align-items: center; gap: 8px; }
.action-btn { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 1px solid var(--sb-line); border-radius: 10px; background: var(--sb-surface-2); color: var(--sb-text); font-size: 13px; cursor: pointer; font-weight: 500; transition: border-color 0.15s ease, color 0.15s ease; }
.action-btn:hover { border-color: var(--sb-accent); color: var(--sb-accent); }
.action-btn--upgrade {
  border-color: color-mix(in oklab, var(--sb-warning, #f59e0b) 40%, transparent);
  color: var(--sb-warning, #f59e0b);
  background: color-mix(in oklab, var(--sb-warning, #f59e0b) 8%, transparent);
}
.action-btn--upgrade:hover {
  border-color: var(--sb-warning, #f59e0b) !important;
  color: var(--sb-warning, #f59e0b) !important;
  background: color-mix(in oklab, var(--sb-warning, #f59e0b) 16%, transparent) !important;
}

.stat-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.stat-card { display: flex; flex-direction: column; gap: 3px; padding: 14px 16px; border-radius: 14px; border: 1px solid var(--sb-line); }
.stat-label { font-size: 11.5px; color: var(--sb-muted); }
.stat-value { font-size: 19px; font-weight: 650; color: var(--sb-text); font-variant-numeric: tabular-nums; }
.stat-sub { font-size: 11.5px; color: var(--sb-muted); }

.detail-tabs { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; border-top: 1px solid var(--sb-line); padding-top: 14px; }
.seg { display: inline-flex; gap: 2px; padding: 2px; border-radius: 10px; }
.seg-btn { padding: 4px 10px; border: 0; border-radius: 7px; background: transparent; color: var(--sb-muted); font-size: 12px; cursor: pointer; }
.seg-btn.active { background: var(--sb-surface); color: var(--sb-accent); font-weight: 600; box-shadow: var(--sb-shadow-sm); }
.tabs-spacer { flex: 1; }
.tabs-note { font-size: 11.5px; color: var(--sb-muted); }

.chart-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.chart-panel { padding: 14px 16px; border-radius: 16px; border: 1px solid var(--sb-line); }
.chart-panel-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.chart-panel-head h4 { margin: 0; font-size: 12px; font-weight: 500; color: var(--sb-muted); }
.chart-panel h4 { margin: 0 0 10px; font-size: 12px; font-weight: 500; color: var(--sb-muted); }
.chart-legend { display: flex; align-items: center; gap: 10px; font-size: 11px; color: var(--sb-muted); }
.legend-chip { display: inline-flex; align-items: center; gap: 5px; }
.legend-swatch { width: 7px; height: 7px; border-radius: 2px; }

.latency-tab { display: flex; flex-direction: column; gap: 12px; }
.latency-chart { padding: 0; overflow: hidden; }
.latency-chart :deep(.trend-chart) { padding: 12px 16px 14px; }
.latency-chart .probe-panel-head { border-bottom: 1px solid var(--sb-line); }
.probe-panel { border: 1px solid var(--sb-line); border-radius: 16px; overflow: hidden; }
.probe-panel-head { display: flex; align-items: center; justify-content: space-between; padding: 10px 16px; border-bottom: 1px solid var(--sb-line); }
.probe-panel-head h4 { margin: 0; font-size: 12px; font-weight: 500; color: var(--sb-muted); }
.probe-panel-head span { font-size: 11px; color: var(--sb-muted); }
.probe-row { display: grid; grid-template-columns: minmax(6rem, 1fr) 3.5rem 3.5rem 4.5rem 3.25rem 4.5rem; align-items: center; gap: 12px; padding: 8px 16px; font-size: 12px; border-top: 1px solid var(--sb-line); cursor: pointer; transition: opacity .15s ease, background .15s ease; }
.probe-row--head { border-top: 0; padding-top: 6px; padding-bottom: 6px; font-size: 11px; color: var(--sb-muted); cursor: default; }
.probe-row.is-hidden { opacity: .4; }
.probe-row.is-active { background: color-mix(in oklab, var(--sb-accent) 8%, transparent); }
.probe-name { display: inline-flex; align-items: center; gap: 8px; min-width: 0; font-weight: 500; color: var(--sb-text); }
.right { text-align: right; }
.only-btn { padding: 1px 6px; border: 1px solid var(--sb-line); border-radius: 6px; background: transparent; color: var(--sb-muted); font-size: 11px; cursor: pointer; }
.only-btn:hover { border-color: var(--sb-accent); color: var(--sb-text); }
.only-btn.active { border-color: var(--sb-accent); background: color-mix(in oklab, var(--sb-accent) 12%, transparent); color: var(--sb-accent); }
.detail-empty { margin: 0; padding: 24px; text-align: center; color: var(--sb-muted); font-size: 12.5px; }

.fact-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.fact-card { padding: 14px 16px; border: 1px solid var(--sb-line); border-radius: 16px; }
.fact-card-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 10px; }
.fact-title {
  margin: 0;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--sb-text);
}
.fact-icon-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: 8px;
  background: color-mix(in oklab, var(--sb-accent) 14%, transparent);
  color: var(--sb-accent);
}
.egress-ip-item {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.egress-ip {
  color: var(--sb-text);
  font-weight: 600;
}
.egress-alias {
  font-size: 11.5px;
  padding: 1.5px 8px;
  border-radius: 6px;
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  font-weight: 500;
}
/* One domain per chip; with several records they stack and wrap instead of
   forming an unreadable comma-joined string. */
.domain-list {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.domain-chip {
  padding: 1px 7px;
  border-radius: 6px;
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
  color: var(--sb-text);
  font-size: 11.5px;
  font-weight: 500;
  word-break: break-all;
}
.fact-action-btn {
  width: 26px;
  height: 26px;
  display: inline-grid;
  place-items: center;
  padding: 0;
  border-radius: 6px;
  border: 1px solid var(--sb-line);
  color: var(--sb-text-2);
  cursor: pointer;
  background: var(--sb-surface-2);
  transition: all 0.15s ease;
}
.fact-action-btn:hover:not(:disabled) {
  border-color: var(--sb-muted);
  color: var(--sb-text);
  background: var(--sb-surface-3);
}
.fact-action-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.fact-link {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--sb-info);
  text-decoration: none;
  font-weight: 600;
}
.fact-link:hover {
  text-decoration: underline;
}
.fact-link .inline-icon {
  opacity: 0.75;
}

.fact-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 1.5px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}
.fact-badge.is-ok {
  background: color-mix(in oklab, var(--sb-success) 14%, transparent);
  color: var(--sb-success);
}
.fact-badge.is-muted {
  background: rgba(100, 104, 123, 0.12);
  color: var(--sb-muted);
}
.fact-badge .badge-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}

.fact-card dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 14px; margin: 0; }
.fact-card dt { font-size: 12px; color: var(--sb-muted); }
.fact-card dd { margin: 0; font-size: 13px; color: var(--sb-text); font-variant-numeric: tabular-nums; word-break: break-word; }

.is-ok { color: var(--sb-success); font-weight: 550; }
.is-warn { color: var(--sb-warning); font-weight: 550; }
.is-danger { color: var(--sb-danger); font-weight: 550; }

.tone-ok { color: var(--sb-success); }
.tone-mild { color: var(--sb-success); }
.tone-warn { color: var(--sb-warning); }
.tone-hot { color: var(--sb-warning); }
.tone-bad { color: var(--sb-danger); }
.tone-muted { color: var(--sb-muted); }

@media (max-width: 900px) {
  .stat-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .chart-grid, .fact-grid { grid-template-columns: 1fr; }
}
</style>
