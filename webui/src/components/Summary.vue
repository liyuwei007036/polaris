<template>
  <div class="summary-grid">
    <div class="cards-subgrid">
      <!-- 1. 本月流量 -->
      <div class="summary-tile neu">
        <span class="tile-blob blob-primary" />
        <div class="tile-head">
          <span class="tile-bubble bubble-primary">
            <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <ellipse cx="12" cy="5" rx="9" ry="3" />
              <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" />
              <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
            </svg>
          </span>
          <span>本月流量</span>
        </div>
        <div class="tile-body">
          <div class="tnum main-val">
            {{ formatBytes(totalMonthTraffic) }}
            <span v-if="monthPct !== null" class="pct-tag" :class="pctClass">
              {{ monthPct.toFixed(0) }}%
            </span>
          </div>
          <div class="sub-text truncate">
            &darr; {{ formatBytes(totalRx) }} &middot; &uarr; {{ formatBytes(totalTx) }}
          </div>
        </div>
      </div>

      <!-- 2. 实时网速 -->
      <div class="summary-tile neu">
        <span class="tile-blob blob-info" />
        <div class="tile-head">
          <span class="tile-bubble bubble-info">
            <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <path d="m3 16 4 4 4-4" />
              <path d="M7 20V4" />
              <path d="m21 8-4-4-4 4" />
              <path d="M17 4v16" />
            </svg>
          </span>
          <span>实时网速</span>
        </div>
        <div class="tile-body">
          <div class="tnum speed-flow">
            <span class="flow-down">&darr; {{ formatRate(liveRx) }}</span>
            <span class="flow-up">&uarr; {{ formatRate(liveTx) }}</span>
          </div>
          <div class="spark-wrap">
            <svg viewBox="0 0 100 24" preserveAspectRatio="none" class="spark-svg">
              <path v-if="rxSpark" :d="rxSpark" class="spark-line is-rx" />
              <path v-if="txSpark" :d="txSpark" class="spark-line is-tx" />
            </svg>
          </div>
        </div>
      </div>

      <!-- 3. 平均延迟 -->
      <div class="summary-tile neu">
        <span class="tile-blob blob-ok" />
        <div class="tile-head">
          <span class="tile-bubble bubble-ok">
            <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
            </svg>
          </span>
          <span>平均延迟</span>
        </div>
        <div class="tile-body">
          <div class="tnum main-val" :class="avgLatencyTone">
            {{ avgLatency === null ? '—' : `${Math.round(avgLatency)} ms` }}
          </div>
          <div class="sub-text truncate">
            {{ activeProbeSummary }}
          </div>
        </div>
      </div>

      <!-- 4. 节点概览与月支出 -->
      <div class="summary-tile neu">
        <span class="tile-blob blob-warn" />
        <div class="tile-head">
          <span class="tile-bubble bubble-warn">
            <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <rect x="2" y="4" width="20" height="16" rx="2" />
              <line x1="2" y1="10" x2="22" y2="10" />
            </svg>
          </span>
          <span>服务器状态</span>
        </div>
        <div class="tile-body">
          <div class="tnum main-val">
            {{ onlineCount }} <span class="total-nodes">/ {{ nodes.length }} 在线</span>
          </div>
          <div class="sub-text truncate">
            {{ nodes.length > 0 ? `${((onlineCount / nodes.length) * 100).toFixed(0)}% 可用率` : '暂无服务器' }}
          </div>
        </div>
      </div>
    </div>

    <!-- World Dot Matrix Map -->
    <NodeMap :nodes="nodes" class="summary-map" @open="$emit('open', $event)" />
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import NodeMap from './NodeMap.vue'
import { sparklinePath } from '../sparkline'

const props = defineProps({
  nodes: {
    type: Array,
    default: () => [],
  },
  metrics: {
    type: Object,
    default: () => ({}),
  },
})

defineEmits(['open'])

// Live speed tracking history (keep up to 30 points)
const speedHistory = ref([])
const maxPoints = 30

function sampleSpeed() {
  let rx = 0
  let tx = 0
  for (const n of props.nodes) {
    if (!n.online) continue
    const m = props.metrics[n.id]
    if (m?.host) {
      rx += m.host.net_rx_rate || 0
      tx += m.host.net_tx_rate || 0
    }
  }
  speedHistory.value.push({ rx, tx })
  if (speedHistory.value.length > maxPoints) {
    speedHistory.value.shift()
  }
}

let stopWatch
onMounted(() => {
  sampleSpeed()
  stopWatch = watch(() => props.metrics, () => sampleSpeed(), { deep: false })
})

onUnmounted(() => {
  if (stopWatch) stopWatch()
})

const liveRx = computed(() => {
  const last = speedHistory.value[speedHistory.value.length - 1]
  return last ? last.rx : 0
})

const liveTx = computed(() => {
  const last = speedHistory.value[speedHistory.value.length - 1]
  return last ? last.tx : 0
})

// Both series share one scale so the two lines stay comparable, and the curve
// is fitted rather than polyline-joined so a noisy sample does not draw a kink.
const rxSpark = computed(() => {
  const data = speedHistory.value
  const max = Math.max(...data.map((s) => s.rx), ...data.map((s) => s.tx), 1024)
  return sparklinePath(data.map((s) => s.rx), { max })
})

const txSpark = computed(() => {
  const data = speedHistory.value
  const max = Math.max(...data.map((s) => s.rx), ...data.map((s) => s.tx), 1024)
  return sparklinePath(data.map((s) => s.tx), { max })
})

const totalRx = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    const p = props.metrics[n.id]?.proxy || props.metrics[n.id]?.node
    if (p && p.monthly_received_bytes !== undefined) {
      sum += Number(p.monthly_received_bytes)
    }
  }
  return sum
})

const totalTx = computed(() => {
  let sum = 0
  for (const n of props.nodes) {
    const p = props.metrics[n.id]?.proxy || props.metrics[n.id]?.node
    if (p && p.monthly_sent_bytes !== undefined) {
      sum += Number(p.monthly_sent_bytes)
    }
  }
  return sum
})

const totalMonthTraffic = computed(() => {
  return totalRx.value + totalTx.value
})

const monthPct = computed(() => {
  return null
})

const pctClass = computed(() => {
  if (!monthPct.value) return ''
  if (monthPct.value >= 90) return 'text-danger'
  if (monthPct.value >= 75) return 'text-warn'
  return 'text-muted'
})

const onlineCount = computed(() => {
  return props.nodes.filter((n) => n.online).length
})

const avgLatency = computed(() => {
  const lats = []
  for (const n of props.nodes) {
    if (!n.online) continue
    const lat = props.metrics[n.id]?.latency?.latency ?? props.metrics[n.id]?.latency?.avgLatency
    if (lat !== undefined && lat !== null && lat > 0) {
      lats.push(lat)
    }
  }
  if (lats.length === 0) return null
  return lats.reduce((a, b) => a + b, 0) / lats.length
})

const avgLatencyTone = computed(() => {
  const v = avgLatency.value
  if (v === null) return 'tone-muted'
  if (v < 60) return 'tone-ok'
  if (v < 110) return 'tone-mild'
  if (v < 180) return 'tone-warn'
  if (v < 280) return 'tone-hot'
  return 'tone-bad'
})

const activeProbeSummary = computed(() => {
  const online = onlineCount.value
  if (online === 0) return '等待探测数据'
  return `来自 ${online} 台在线服务器的探测均值`
})

function formatBytes(bytes) {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(i > 1 ? 2 : 0)} ${units[i]}`
}

function formatRate(bps) {
  if (!bps || bps <= 0) return '0 B/s'
  return `${formatBytes(bps)}/s`
}
</script>

<style scoped>
.summary-grid {
  display: grid;
  gap: 12px;
  grid-template-columns: 1fr;
}

@media (min-width: 768px) {
  .summary-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1280px) {
  .summary-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 1536px) {
  .summary-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

.cards-subgrid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

@media (min-width: 768px) {
  .cards-subgrid {
    grid-column: span 2 / span 2;
  }
}

@media (min-width: 1280px) {
  .cards-subgrid {
    grid-column: span 2 / span 2;
  }
}

@media (min-width: 1536px) {
  .cards-subgrid {
    grid-column: span 2 / span 2;
  }
}

.summary-map {
  grid-column: span 1 / span 1;
}

@media (min-width: 768px) {
  .summary-map {
    grid-column: span 2 / span 2;
  }
}

@media (min-width: 1280px) {
  .summary-map {
    grid-column: span 1 / span 1;
  }
}

@media (min-width: 1536px) {
  .summary-map {
    grid-column: span 2 / span 2;
  }
}

.summary-tile {
  position: relative;
  overflow: hidden;
  border-radius: 16px;
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  text-align: center;
}

.tile-blob {
  pointer-events: none;
  position: absolute;
  top: -32px;
  right: -32px;
  width: 96px;
  height: 96px;
  border-radius: 50%;
  filter: blur(48px);
}

.blob-primary { background: color-mix(in oklab, var(--sb-accent) 15%, transparent); }
.blob-info { background: color-mix(in oklab, var(--sb-accent-2) 15%, transparent); }
.blob-ok { background: rgba(16, 185, 129, 0.15); }
.blob-warn { background: rgba(245, 158, 11, 0.15); }

.tile-head {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-size: 12px;
  color: var(--sb-muted);
}

.tile-bubble {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 8px;
}

.bubble-primary { background: color-mix(in oklab, var(--sb-accent) 12%, transparent); color: var(--sb-accent); }
.bubble-info { background: color-mix(in oklab, var(--sb-accent-2) 12%, transparent); color: var(--sb-accent-2); }
.bubble-ok { background: rgba(16, 185, 129, 0.12); color: var(--sb-success); }
.bubble-warn { background: rgba(245, 158, 11, 0.12); color: var(--sb-warning); }

.tile-body {
  position: relative;
  margin-top: 8px;
}

.main-val {
  font-size: 20px;
  font-weight: 650;
  color: var(--sb-text);
  line-height: 1.2;
}

.pct-tag {
  font-size: 13px;
  font-weight: 400;
  margin-left: 4px;
}

.total-nodes {
  font-size: 13px;
  font-weight: 400;
  color: var(--sb-muted);
}

.sub-text {
  margin-top: 4px;
  font-size: 11px;
  color: var(--sb-muted);
}

.speed-flow {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  font-size: 13px;
  font-weight: 600;
}

.flow-down { color: var(--sb-accent); }
.flow-up { color: var(--sb-warning); }

.spark-wrap {
  margin-top: 4px;
  height: 24px;
  width: 100%;
}

.spark-svg {
  width: 100%;
  height: 100%;
  display: block;
  overflow: visible;
}

.spark-line {
  fill: none;
  stroke: currentColor;
  stroke-width: 1.6;
  stroke-linecap: round;
  stroke-linejoin: round;
  vector-effect: non-scaling-stroke;
}

.spark-line.is-rx {
  color: var(--sb-accent);
}

.spark-line.is-tx {
  color: var(--sb-warning);
}
</style>
