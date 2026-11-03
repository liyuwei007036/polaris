<template>
  <div class="summary-deck">
    <div class="summary-grid">
      <!-- Tile 1: 本月流量 -->
    <div class="summary-tile tone-primary neu">
      <span class="tile-blob blob-primary" />
      <div class="tile-head">
        <span class="tile-bubble bubble-primary">
          <svg class="tile-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <ellipse cx="12" cy="5" rx="9" ry="3" />
            <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" />
            <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
          </svg>
        </span>
        <span class="tile-label">本月流量</span>
      </div>
      <div class="tile-body">
        <div class="tile-main font-mono">
          <span>{{ formattedMonthTotal }}</span>
          <span v-if="monthPercent !== null" class="tile-pct" :class="monthPctClass">
            {{ monthPercent.toFixed(0) }}%
          </span>
        </div>
        <div class="tile-sub font-mono" :title="`累计下行 ${formatBytes(totalRx)} · 累计上行 ${formatBytes(totalTx)}`">
          <span>↓ {{ formatBytes(totalRx) }}</span>
          <span class="sub-sep">·</span>
          <span>↑ {{ formatBytes(totalTx) }}</span>
        </div>
      </div>
    </div>

    <!-- Tile 2: 实时网速 -->
    <div class="summary-tile tone-info neu">
      <span class="tile-blob blob-info" />
      <div class="tile-head">
        <span class="tile-bubble bubble-info">
          <svg class="tile-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <polyline points="7 10 12 15 17 10" />
            <line x1="12" y1="15" x2="12" y2="3" />
            <polyline points="17 14 12 9 7 14" />
            <line x1="12" y1="9" x2="12" y2="21" />
          </svg>
        </span>
        <span class="tile-label">实时网速</span>
      </div>
      <div class="tile-body">
        <div class="speed-flow font-mono">
          <span class="speed-item is-down">
            <svg class="flow-icon down" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
              <line x1="12" y1="5" x2="12" y2="19" />
              <polyline points="19 12 12 19 5 12" />
            </svg>
            {{ formatBytes(currentRxRate, '/s') }}
          </span>
          <span class="speed-item is-up">
            <svg class="flow-icon up" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
              <line x1="12" y1="19" x2="12" y2="5" />
              <polyline points="5 12 12 5 19 12" />
            </svg>
            {{ formatBytes(currentTxRate, '/s') }}
          </span>
        </div>
        <div class="sparkline-wrap">
          <svg viewBox="0 0 100 24" preserveAspectRatio="none" class="sparkline-svg">
            <polyline
              v-if="rxPoints"
              :points="rxPoints"
              class="spark-line is-rx"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              vector-effect="non-scaling-stroke"
            />
            <polyline
              v-if="txPoints"
              :points="txPoints"
              class="spark-line is-tx"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              vector-effect="non-scaling-stroke"
            />
          </svg>
        </div>
      </div>
    </div>

    <!-- Tile 3: 平均延迟 -->
    <div class="summary-tile tone-ok neu">
      <span class="tile-blob blob-ok" />
      <div class="tile-head">
        <span class="tile-bubble bubble-ok">
          <svg class="tile-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
          </svg>
        </span>
        <span class="tile-label">平均延迟</span>
      </div>
      <div class="tile-body">
        <div class="tile-main font-mono">
          <span>{{ avgFleetLatency !== null ? `${Math.round(avgFleetLatency)} ms` : '—' }}</span>
          <span v-if="avgFleetJitter !== null" class="tile-sub-inline font-mono">
            ±{{ Math.round(avgFleetJitter) }}ms
          </span>
        </div>
        <div class="tile-sub font-mono">
          <span v-if="activeProbingNodeCount > 0">
            来自 {{ activeProbingNodeCount }} 个探测节点
          </span>
          <span v-else>等待探测数据</span>
        </div>
      </div>
    </div>

    <!-- Tile 4: 节点概况 / 费用 -->
    <div class="summary-tile tone-warn neu">
      <span class="tile-blob blob-warn" />
      <div class="tile-head">
        <span class="tile-bubble bubble-warn">
          <svg class="tile-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="2" y="2" width="20" height="8" rx="2" ry="2" />
            <rect x="2" y="14" width="20" height="8" rx="2" ry="2" />
            <line x1="6" y1="6" x2="6.01" y2="6" />
            <line x1="6" y1="18" x2="6.01" y2="18" />
          </svg>
        </span>
        <span class="tile-label">节点状态</span>
      </div>
      <div class="tile-body">
        <div class="tile-main font-mono">
          <span class="text-ok">{{ onlineCount }}</span>
          <span class="tile-divider">/</span>
          <span>{{ totalCount }}</span>
          <span class="tile-unit">在线</span>
        </div>
        <div class="tile-sub font-mono">
          <span v-if="monthlySpendText" :title="`月均续费折合换算（按参考汇率 1 USD ≈ 7.23 CNY）`">{{ monthlySpendText }}</span>
          <span v-else>{{ offlineCount }} 离线</span>
        </div>
      </div>
    </div>
  </div>

  <!-- Right Side: Openlat World Node Distribution Map -->
  <NodeMap :nodes="nodes" class="summary-map" @open="(id) => $emit('open', id)" />
</div>
</template>

<script setup>
import { computed, ref, onMounted, onBeforeUnmount } from 'vue'
import NodeMap from './NodeMap.vue'
import { formatBytes } from '../format'

const props = defineProps({
  nodes: {
    type: Array,
    default: () => [],
  },
  metrics: {
    type: Object,
    default: () => ({}),
  },
  live: {
    type: Object,
    default: () => new Map(),
  },
})

defineEmits(['open'])

// Speed history ring buffer for sparkline (12 points)
const historyLimit = 16
const speedHistory = ref([])

let historyTimer = null

onMounted(() => {
  historyTimer = setInterval(() => {
    speedHistory.value.push({
      rx: currentRxRate.value,
      tx: currentTxRate.value,
    })
    if (speedHistory.value.length > historyLimit) {
      speedHistory.value.shift()
    }
  }, 2500)
})

onBeforeUnmount(() => {
  if (historyTimer) clearInterval(historyTimer)
})

const totalRx = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    const p = props.metrics[n.id]?.proxy
    if (p && p.monthly_received_bytes !== undefined) {
      sum += Number(p.monthly_received_bytes)
    }
  }
  return sum
})

const totalTx = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    const p = props.metrics[n.id]?.proxy
    if (p && p.monthly_sent_bytes !== undefined) {
      sum += Number(p.monthly_sent_bytes)
    }
  }
  return sum
})

const formattedMonthTotal = computed(() => {
  const sum = totalRx.value + totalTx.value
  return sum > 0 ? formatBytes(sum) : '—'
})

const monthLimitBytes = computed(() => {
  let limit = 0
  for (const n of props.nodes) {
    if (n.traffic_plan) {
      const m = String(n.traffic_plan).match(/([\d.]+)\s*(TB|GB|MB)/i)
      if (m) {
        const val = parseFloat(m[1])
        const unit = m[2].toUpperCase()
        if (unit === 'TB') limit += val * 1024 * 1024 * 1024 * 1024
        else if (unit === 'GB') limit += val * 1024 * 1024 * 1024
        else if (unit === 'MB') limit += val * 1024 * 1024
      }
    }
  }
  return limit
})

const monthPercent = computed(() => {
  const used = totalRx.value + totalTx.value
  const limit = monthLimitBytes.value
  if (limit <= 0 || used <= 0) return null
  return Math.min(100, (used / limit) * 100)
})

const monthPctClass = computed(() => {
  const p = monthPercent.value
  if (p === null) return ''
  if (p >= 90) return 'is-danger'
  if (p >= 75) return 'is-warn'
  return 'is-muted'
})

const currentRxRate = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    // 优先取 live snap，其次取 metrics.host.net_rx_rate
    const snap = props.live?.get ? props.live.get(n.id) : null
    if (snap?.has_rates) {
      sum += snap.received_rate || 0
    } else {
      const rx = props.metrics[n.id]?.host?.net_rx_rate
      if (rx) sum += rx
    }
  }
  return sum
})

const currentTxRate = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    const snap = props.live?.get ? props.live.get(n.id) : null
    if (snap?.has_rates) {
      sum += snap.sent_rate || 0
    } else {
      const tx = props.metrics[n.id]?.host?.net_tx_rate
      if (tx) sum += tx
    }
  }
  return sum
})

const rxPoints = computed(() => {
  const data = speedHistory.value
  if (data.length < 2) return ''
  const max = Math.max(...data.map((d) => d.rx), ...data.map((d) => d.tx), 1024)
  const len = data.length - 1
  return data.map((d, i) => `${(i / len) * 100},${22 - (d.rx / max) * 20}`).join(' ')
})

const txPoints = computed(() => {
  const data = speedHistory.value
  if (data.length < 2) return ''
  const max = Math.max(...data.map((d) => d.rx), ...data.map((d) => d.tx), 1024)
  const len = data.length - 1
  return data.map((d, i) => `${(i / len) * 100},${22 - (d.tx / max) * 20}`).join(' ')
})

// Fleet latency stats
const latencyList = computed(() => {
  const list = []
  for (const n of props.nodes) {
    const lat = props.metrics[n.id]?.latency
    const avg = lat?.average_latency_ms ?? lat?.avgLatency
    if (typeof avg === 'number' && avg > 0) {
      list.push({ avg, jitter: lat?.jitter_ms ?? lat?.jitter ?? 0 })
    }
  }
  return list
})

const avgFleetLatency = computed(() => {
  if (!latencyList.value.length) return null
  const sum = latencyList.value.reduce((acc, item) => acc + item.avg, 0)
  return sum / latencyList.value.length
})

const avgFleetJitter = computed(() => {
  if (!latencyList.value.length) return null
  const sum = latencyList.value.reduce((acc, item) => acc + item.jitter, 0)
  return sum / latencyList.value.length
})

const activeProbingNodeCount = computed(() => latencyList.value.length)

// Node count
const totalCount = computed(() => props.nodes.length)
const onlineCount = computed(() => props.nodes.filter((n) => n.online).length)
const offlineCount = computed(() => totalCount.value - onlineCount.value)

// Monthly billing spend
const monthlySpendText = computed(() => {
  let usd = 0
  let cny = 0
  for (const n of props.nodes) {
    const p = parseFloat(n.price)
    if (!p || p <= 0) continue
    const period = n.price_period || 'month'
    let factor = 1
    if (period === 'year') factor = 1 / 12
    else if (period === 'quarter') factor = 1 / 3
    else if (period === 'day') factor = 30
    if (n.currency === 'CNY') cny += p * factor
    else usd += p * factor
  }
  if (usd === 0 && cny === 0) return ''
  const USD_TO_CNY = 7.23
  if (usd > 0 && cny > 0) {
    const totalCny = cny + usd * USD_TO_CNY
    return `折合 ¥${totalCny.toFixed(1)}/月 ($${usd.toFixed(1)} + ¥${cny.toFixed(1)})`
  }
  if (usd > 0) {
    const inCny = usd * USD_TO_CNY
    return `$${usd.toFixed(1)}/月 (折合 ¥${inCny.toFixed(1)})`
  }
  return `¥${cny.toFixed(1)}/月`
})
</script>

<style scoped>
.summary-deck {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(0, 1fr);
  gap: 12px;
  align-items: stretch;
  margin-bottom: 16px;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.summary-map {
  min-height: 220px;
}

@media (max-width: 1100px) {
  .summary-deck {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 600px) {
  .summary-grid {
    grid-template-columns: 1fr;
  }
}

.summary-tile {
  position: relative;
  display: flex;
  flex-direction: column;
  justify-content: center;
  align-items: center;
  overflow: hidden;
  border-radius: 16px;
  padding: 14px 16px;
  text-align: center;
  border: 1px solid var(--sb-line);
  transition: transform 0.2s ease, box-shadow 0.2s ease;
}

.summary-tile:hover {
  transform: translateY(-2px);
}

.tile-blob {
  position: absolute;
  top: -24px;
  right: -24px;
  width: 96px;
  height: 96px;
  border-radius: 50%;
  filter: blur(32px);
  pointer-events: none;
}

.blob-primary { background: color-mix(in oklab, var(--sb-accent) 16%, transparent); }
.blob-info { background: color-mix(in oklab, var(--sb-accent-2) 18%, transparent); }
.blob-ok { background: rgba(52, 211, 153, 0.18); }
.blob-warn { background: rgba(251, 191, 36, 0.18); }

.tile-head {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--sb-muted, #94a3b8);
  margin-bottom: 8px;
}

.tile-bubble {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border-radius: 8px;
}

.bubble-primary { background: color-mix(in oklab, var(--sb-accent) 14%, transparent); color: var(--sb-accent); }
.bubble-info { background: color-mix(in oklab, var(--sb-accent-2) 14%, transparent); color: var(--sb-accent-2); }
.bubble-ok { background: color-mix(in oklab, var(--sb-success) 14%, transparent); color: var(--sb-success); }
.bubble-warn { background: color-mix(in oklab, var(--sb-warning) 14%, transparent); color: var(--sb-warning); }

.tile-icon {
  width: 14px;
  height: 14px;
}

.tile-body {
  position: relative;
  width: 100%;
}

.tile-main {
  font-size: 22px;
  font-weight: 650;
  line-height: 1.2;
  color: var(--sb-text);
  display: flex;
  align-items: baseline;
  justify-content: center;
  gap: 6px;
}

.tile-pct {
  font-size: 13px;
  font-weight: 500;
}
.tile-pct.is-danger { color: #fb7185; }
.tile-pct.is-warn { color: var(--sb-warning); }
.tile-pct.is-muted { color: var(--sb-muted); }

.tile-sub-inline {
  font-size: 12px;
  font-weight: 500;
  color: var(--sb-muted);
}

.tile-divider {
  opacity: 0.4;
  margin: 0 1px;
}

.tile-unit {
  font-size: 12px;
  font-weight: normal;
  color: var(--sb-muted);
}

.tile-sub {
  margin-top: 4px;
  font-size: 11px;
  color: var(--sb-muted, #94a3b8);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.sub-sep {
  margin: 0 4px;
  opacity: 0.5;
}

.speed-flow {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  font-size: 13px;
  font-weight: 600;
}

.speed-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.speed-item.is-down { color: var(--sb-info); }
.speed-item.is-up { color: var(--sb-warning); }

.flow-icon {
  width: 12px;
  height: 12px;
}
.flow-icon.down { color: var(--sb-info); }
.flow-icon.up { color: var(--sb-warning); }

.sparkline-wrap {
  margin-top: 6px;
  height: 22px;
  width: 100%;
  overflow: hidden;
}

.sparkline-svg {
  width: 100%;
  height: 100%;
}

.spark-line.is-rx {
  color: var(--sb-info);
  opacity: 0.85;
}

.spark-line.is-tx {
  color: var(--sb-warning);
  opacity: 0.85;
}
</style>
