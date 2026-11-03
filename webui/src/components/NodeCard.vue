<template>
  <div
    class="openlat-node-card neu rise"
    :class="{ 'is-offline': !node.online }"
    role="button"
    tabindex="0"
    @click="$emit('open', node.id)"
  >
    <!-- Background Glow -->
    <span class="corner-glow" :class="node.online ? 'glow-online' : 'glow-offline'" />

    <!-- Card Header Row 1: Title & Status -->
    <div class="card-head">
      <h3 class="node-title truncate min-w-0 flex-1" :title="node.name">
        {{ node.name }}
      </h3>

      <!-- Right: Status Pill & Action button -->
      <div class="head-right shrink-0" @click.stop>
        <div class="status-pill" :class="node.online ? 'is-online' : 'is-offline'">
          <span class="status-dot-pulse" />
          <span>{{ node.online ? '在线' : '离线' }}</span>
        </div>
        <el-dropdown
          v-if="canManage"
          trigger="click"
          @command="(cmd) => $emit('action', { command: cmd, node })"
        >
          <button
            class="card-action-btn neu-track neu-press"
            title="更多操作"
            type="button"
            @click.stop
          >
            <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="12" cy="12" r="1.5" />
              <circle cx="19" cy="12" r="1.5" />
              <circle cx="5" cy="12" r="1.5" />
            </svg>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="detail"><el-icon><InfoFilled /></el-icon>详情</el-dropdown-item>
              <el-dropdown-item command="edit"><el-icon><Edit /></el-icon>配置</el-dropdown-item>
              <el-dropdown-item v-if="node.online" command="networkProbe"><el-icon><Connection /></el-icon>线路排查</el-dropdown-item>
              <el-dropdown-item v-if="node.ddns_enabled" command="syncDDNS"><el-icon><Refresh /></el-icon>同步 DDNS</el-dropdown-item>
              <el-dropdown-item command="revoke" divided class="danger-item"><el-icon><RemoveFilled /></el-icon>移除</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </div>

    <!-- Card Header Row 2: Full-width Meta & Badges -->
    <div class="node-meta">
      <span class="country-flag shrink-0">{{ countryFlag }}</span>
      <span class="country-name shrink-0">{{ countryName }}</span>
      <span class="meta-divider shrink-0">&middot;</span>
      <OsIcon :os="node.os_name || node.os" class="meta-os-icon shrink-0" />
      <span class="node-system-text truncate min-w-0 flex-1">{{ systemInfoText }}</span>
      <span
        v-if="node.ddns_enabled"
        class="node-tag ddns-tag shrink-0"
        :title="ddnsTooltip"
      >
        <span class="ddns-dot" />
        <span>DDNS</span>
      </span>
      <span v-if="node.bandwidth" class="node-tag bw-tag shrink-0" title="机器规格带宽">
        <svg class="size-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2" />
        </svg>
        <span>{{ node.bandwidth }}</span>
      </span>
    </div>

    <!-- Meters Grid (CPU, RAM, DISK, TRAFFIC) -->
    <div class="meters-grid">
      <Meter
        label="CPU"
        tone="cpu"
        :pct="cpuPercent"
        :foot="cpuFootText"
      >
        <template #icon>
          <svg class="size-3.5 text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="4" y="4" width="16" height="16" rx="2" />
            <rect x="9" y="9" width="6" height="6" />
            <path d="M15 2v2M9 2v2M15 20v2M9 20v2M20 15h2M20 9h2M2 15h2M2 9h2" />
          </svg>
        </template>
      </Meter>
      <Meter
        label="内存"
        tone="mem"
        :pct="memPercent"
        :foot="memFootText"
      >
        <template #icon>
          <svg class="size-3.5 text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M6 19v-3M10 19v-3M14 19v-3M18 19v-3M6 5v1M18 5v1M6 12V8a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2Z" />
          </svg>
        </template>
      </Meter>
      <Meter
        label="硬盘"
        tone="disk"
        :pct="diskPercent"
        :foot="diskFootText"
      >
        <template #icon>
          <svg class="size-3.5 text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <line x1="22" x2="2" y1="12" y2="12" />
            <path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" />
            <line x1="6" x2="6.01" y1="16" y2="16" />
            <line x1="10" x2="10.01" y1="16" y2="16" />
          </svg>
        </template>
      </Meter>
      <Meter
        label="流量"
        tone="net"
        :pct="trafficPercent"
        :foot="trafficFootText"
        empty="∞"
      >
        <template #icon>
          <svg class="size-3.5 text-muted" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m3 16 4 4 4-4" />
            <path d="M7 20V4" />
            <path d="m21 8-4-4-4 4" />
            <path d="M17 4v16" />
          </svg>
        </template>
      </Meter>
    </div>

    <!-- Latency & 24h Heat Tracks -->
    <div class="latency-section">
      <LatencyPanel :latency="latencyData" />
    </div>

    <!-- 到期 (Renewal track) -->
    <div class="expiry-track neu-track">
      <span class="expiry-label">
        <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <rect x="3" y="4" width="18" height="18" rx="2" ry="2" />
          <line x1="16" y1="2" x2="16" y2="6" />
          <line x1="8" y1="2" x2="8" y2="6" />
          <line x1="3" y1="10" x2="21" y2="10" />
        </svg>
        到期
      </span>
      <span class="expiry-val font-mono" :class="expiryToneClass">{{ expiryText }}</span>
    </div>

    <!-- 连接 & 续费 & 厂商 -->
    <dl class="card-meta-dl font-mono" :class="{ 'has-provider': Boolean(node.provider) }">
      <div v-if="node.provider" class="dl-row">
        <dt>厂商</dt>
        <dd class="truncate">
          <a
            v-if="node.provider_url"
            :href="node.provider_url"
            target="_blank"
            rel="noopener noreferrer"
            class="provider-link"
            @click.stop
          >
            {{ node.provider }}
          </a>
          <span v-else>{{ node.provider }}</span>
        </dd>
      </div>
      <div class="dl-row">
        <dt>连接</dt>
        <dd>{{ tcpCount }} / {{ udpCount }}</dd>
      </div>
      <div class="dl-row">
        <dt>续费</dt>
        <dd>{{ priceText }}</dd>
      </div>
    </dl>

    <!-- Network live rate tracks -->
    <div class="rate-tracks-grid">
      <div class="neu-track rate-pill">
        <span class="rate-label">
          <svg class="size-3.5 text-info" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
            <polyline points="7 10 12 15 17 10" />
            <line x1="12" y1="15" x2="12" y2="3" />
          </svg>
          下行
        </span>
        <span class="tnum rate-num font-mono">{{ formatRate(netRxRate) }}</span>
      </div>
      <div class="neu-track rate-pill">
        <span class="rate-label">
          <svg class="size-3.5 text-warn" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
            <polyline points="17 14 12 9 7 14" />
            <line x1="12" y1="9" x2="12" y2="21" />
          </svg>
          上行
        </span>
        <span class="tnum rate-num font-mono">{{ formatRate(netTxRate) }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import {
  Connection,
  Edit,
  InfoFilled,
  Refresh,
  RemoveFilled,
} from '@element-plus/icons-vue'
import OsIcon from './OsIcon.vue'
import Meter from './Meter.vue'
import LatencyPanel from './LatencyPanel.vue'

const props = defineProps({
  node: {
    type: Object,
    required: true,
  },
  metrics: {
    type: Object,
    default: () => ({}),
  },
  canManage: {
    type: Boolean,
    default: false,
  },
})

defineEmits(['open', 'action'])

function countryCodeToFlag(code) {
  if (!code || code.length !== 2) return ''
  const offset = 127397
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => c.charCodeAt(0) + offset))
}

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

const countryFlag = computed(() => {
  const code = props.node.country || props.node.country_code
  if (code && /^[A-Za-z]{2}$/.test(code)) {
    const f = countryCodeToFlag(code)
    if (f) return f
  }
  return getNodeFlag(props.node.name)
})

const countryName = computed(() => {
  const code = props.node.country || props.node.country_code
  if (code && /^[A-Za-z]{2}$/.test(code)) {
    try {
      const dn = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
      return dn.of(code.toUpperCase()) || code
    } catch {
      return code
    }
  }
  return props.node.city || '全球节点'
})

const systemInfoText = computed(() => {
  const parts = []
  if (props.node.os_name || props.node.os) {
    let os = props.node.os_name || props.node.os
    os = os.replace(/\s*\([^)]*\)/g, '').replace(/\s+LTS/i, '').trim()
    parts.push(os)
  }
  if (props.node.virt && props.node.virt !== 'none') parts.push(props.node.virt)
  if (props.node.arch || props.node.architecture) parts.push(props.node.arch || props.node.architecture)
  return parts.length ? parts.join(' · ') : 'Linux'
})

const hostMetrics = computed(() => {
  return props.node.online ? props.metrics?.host : null
})

const latencyData = computed(() => {
  return props.metrics?.latency || null
})

const cpuPercent = computed(() => {
  if (!props.node.online || !hostMetrics.value) return null
  return hostMetrics.value.cpu_percent || 0
})

const cpuFootText = computed(() => {
  if (!props.node.online || !hostMetrics.value) return '—'
  const cores = props.node.cpu_cores || hostMetrics.value.cpu_cores || 1
  const load = hostMetrics.value.load_avg
  if (Array.isArray(load) && load.length >= 3) {
    return `${load[0].toFixed(2)} ${load[1].toFixed(2)} ${load[2].toFixed(2)}`
  }
  return `${cores} 核 · 负载 ${((hostMetrics.value.cpu_percent || 0) / 100 * cores).toFixed(2)}`
})

const memPercent = computed(() => {
  if (!props.node.online || !hostMetrics.value) return null
  const total = hostMetrics.value.memory_total || 0
  const used = hostMetrics.value.memory_used || 0
  if (!total) return null
  return (used / total) * 100
})

const memFootText = computed(() => {
  if (!props.node.online || !hostMetrics.value) return '—'
  const total = hostMetrics.value.memory_total || 0
  const used = hostMetrics.value.memory_used || 0
  return `${formatBytes(used)} / ${formatBytes(total)}`
})

const diskPercent = computed(() => {
  if (!props.node.online || !hostMetrics.value) return null
  const total = hostMetrics.value.disk_total || 0
  const used = hostMetrics.value.disk_used || 0
  if (!total) return null
  return (used / total) * 100
})

const diskFootText = computed(() => {
  if (!props.node.online || !hostMetrics.value) return '—'
  const total = hostMetrics.value.disk_total || 0
  const used = hostMetrics.value.disk_used || 0
  return `${formatBytes(used)} / ${formatBytes(total)}`
})

function parseTrafficLimitBytes(plan) {
  if (!plan) return 0
  const m = String(plan).match(/([\d.]+)\s*(TB|GB|MB)?/i)
  if (!m) return 0
  const val = parseFloat(m[1])
  if (isNaN(val) || val <= 0) return 0
  const unit = (m[2] || 'GB').toUpperCase()
  if (unit === 'TB') return val * 1024 * 1024 * 1024 * 1024
  if (unit === 'GB') return val * 1024 * 1024 * 1024
  if (unit === 'MB') return val * 1024 * 1024
  return val * 1024 * 1024 * 1024
}

const trafficLimitBytes = computed(() => {
  return parseTrafficLimitBytes(props.node.traffic_plan)
})

const trafficPercent = computed(() => {
  const limit = trafficLimitBytes.value
  if (!limit || limit <= 0) return null
  const totals = props.metrics?.proxy || props.metrics?.node || {}
  const used = (totals.received_bytes || 0) + (totals.sent_bytes || 0)
  return Math.min(100, Math.round((used / limit) * 100))
})

const trafficFootText = computed(() => {
  const totals = props.metrics?.proxy || props.metrics?.node || {}
  const used = (totals.received_bytes || 0) + (totals.sent_bytes || 0)
  const limit = trafficLimitBytes.value
  if (limit > 0) {
    return `${formatBytes(used)} / ${formatBytes(limit)}`
  }
  return `${formatBytes(used)} / ∞`
})

const netRxRate = computed(() => {
  if (!props.node.online || !hostMetrics.value) return 0
  return hostMetrics.value.net_rx_rate || 0
})

const netTxRate = computed(() => {
  if (!props.node.online || !hostMetrics.value) return 0
  return hostMetrics.value.net_tx_rate || 0
})

const tcpCount = computed(() => {
  return hostMetrics.value?.tcp || 0
})

const udpCount = computed(() => {
  return hostMetrics.value?.udp || 0
})

// 到期天数计算
const expiryDays = computed(() => {
  if (!props.node.expires_on) return null
  const d = new Date(props.node.expires_on)
  if (isNaN(d.getTime())) return null
  const diff = Math.ceil((d.getTime() - Date.now()) / (1000 * 3600 * 24))
  return diff
})

const expiryText = computed(() => {
  if (props.node.expires_on === '2035-01-01' || !props.node.expires_on || props.node.price === '0.00' || props.node.price === '0') {
    return '永久免费'
  }
  if (expiryDays.value === null) return '—'
  if (expiryDays.value < 0) return '已到期'
  return `${expiryDays.value} 天后到期`
})

const expiryToneClass = computed(() => {
  if (expiryDays.value === null) return ''
  if (expiryDays.value <= 15) return 'is-danger'
  if (expiryDays.value <= 60) return 'is-warn'
  return 'is-ok'
})

const priceText = computed(() => {
  const p = props.node.price
  if (!p || p === '0.00' || p === '0') return '免费'
  const sym = props.node.currency === 'CNY' ? '¥' : props.node.currency === 'EUR' ? '€' : '$'
  const per = props.node.price_period === 'year' ? '年' : props.node.price_period === 'quarter' ? '季' : '月'
  return `${sym}${p} / ${per}`
})

const ddnsTooltip = computed(() => {
  if (!props.node.ddns_enabled) return ''
  const domains = (props.node.ddns_domains && props.node.ddns_domains.length ? props.node.ddns_domains : [props.node.ddns_domain]).filter(Boolean)
  const domainText = domains.length ? domains.join(', ') : '未指定域名'
  const ipText = props.node.last_observed_ip ? ` (解析 IP: ${props.node.last_observed_ip})` : ''
  return `Cloudflare DDNS: ${domainText}${ipText}`
})

function formatBytes(bytes) {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i > 1 ? 1 : 0)} ${units[i]}`
}

function formatRate(bps) {
  if (!bps || bps <= 0) return '0 B/s'
  return `${formatBytes(bps)}/s`
}
</script>

<style scoped>
.openlat-node-card {
  position: relative;
  overflow: hidden;
  border-radius: 16px;
  padding: 16px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  border: 1px solid var(--sb-line);
  transition: transform 0.2s ease, border-color 0.2s ease, box-shadow 0.2s ease;
  user-select: none;
}

.openlat-node-card:hover {
  transform: translateY(-2px);
  border-color: var(--sb-accent);
  box-shadow: var(--sh-neu), 0 0 20px color-mix(in oklab, var(--sb-accent) 15%, transparent);
}

.corner-glow {
  pointer-events: none;
  position: absolute;
  top: -48px;
  right: -48px;
  width: 140px;
  height: 140px;
  border-radius: 50%;
  filter: blur(52px);
  opacity: 0.25;
}

.glow-online { background: var(--sb-success, #10b981); }
.glow-offline { background: var(--sb-danger, #ef4444); }

.card-head {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.node-title {
  margin: 0;
  font-size: 14.5px;
  font-weight: 650;
  color: var(--sb-text);
  line-height: 1.3;
}

.head-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.node-meta {
  margin: 6px 0 0;
  display: flex;
  align-items: center;
  flex-wrap: nowrap;
  gap: 6px;
  font-size: 11px;
  color: var(--sb-muted);
  width: 100%;
}

.node-system-text {
  min-width: 0;
  color: var(--sb-muted);
}

.node-tag {
  display: inline-flex;
  align-items: center;
  gap: 3.5px;
  padding: 1px 6px;
  border-radius: 6px;
  font-size: 10.5px;
  line-height: 1.3;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
  color: var(--sb-text-2);
  flex-shrink: 0;
}

.provider-tag {
  color: var(--sb-text);
  font-weight: 500;
}

.provider-link {
  color: inherit;
  text-decoration: none;
}
.provider-link:hover {
  text-decoration: underline;
  color: var(--sb-accent);
}

.ddns-tag {
  color: var(--sb-info);
  border-color: color-mix(in oklab, var(--sb-info) 30%, transparent);
  background: color-mix(in oklab, var(--sb-info) 8%, transparent);
  font-weight: 550;
}

.ddns-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--sb-info);
}

.bw-tag {
  color: var(--sb-muted);
  font-family: var(--font-mono, monospace);
  font-size: 10px;
}

.country-flag {
  font-size: 13px;
  line-height: 1;
}

.country-name {
  max-width: 90px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.meta-divider {
  opacity: 0.5;
}

.meta-os-icon {
  width: 12px;
  height: 12px;
  flex-shrink: 0;
}

.head-right {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.card-action-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
  background: var(--sb-surface-2);
  color: var(--sb-muted);
  cursor: pointer;
  padding: 0;
}

.card-action-btn:hover {
  color: var(--sb-text);
  border-color: var(--sb-accent);
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 550;
  flex-shrink: 0;
}

.status-pill.is-online {
  background: rgba(16, 185, 129, 0.14);
  color: var(--sb-success, #10b981);
}

.status-pill.is-offline {
  background: rgba(239, 68, 68, 0.14);
  color: var(--sb-danger, #ef4444);
}

.status-dot-pulse {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

.status-pill.is-online .status-dot-pulse {
  box-shadow: 0 0 6px var(--sb-success, #10b981);
}

.meters-grid {
  margin-top: 14px;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.latency-section {
  margin-top: 14px;
}

.expiry-track {
  margin-top: 10px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 10px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
}

.expiry-label {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 11px;
  color: var(--sb-muted);
}

.expiry-val {
  font-size: 12px;
  font-weight: 600;
}
.expiry-val.is-ok { color: var(--sb-success); }
.expiry-val.is-warn { color: var(--sb-warning); }
.expiry-val.is-danger { color: var(--sb-danger); }

.card-meta-dl {
  margin: 8px 0 0;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
  font-size: 11px;
}

.card-meta-dl.has-provider {
  grid-template-columns: repeat(3, minmax(0, 1fr));
}

.dl-row {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 6px;
}

.dl-row dt {
  color: var(--sb-muted);
  flex-shrink: 0;
}

.dl-row dd {
  margin: 0;
  color: var(--sb-text);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rate-tracks-grid {
  margin-top: 8px;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.rate-pill {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 6px 10px;
  border-radius: 8px;
  border: 1px solid var(--sb-line);
}

.rate-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--sb-muted);
}

.rate-num {
  font-size: 11px;
  font-weight: 600;
  color: var(--sb-text);
}
</style>
