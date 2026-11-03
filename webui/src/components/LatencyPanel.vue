<template>
  <div v-if="compact" class="latency-mini">
    <HeatTrack :hours="formattedHours" kind="latency" :probes="targetCount" :compact="true" class="mini-track" />
    <HeatTrack :hours="formattedHours" kind="loss" :probes="targetCount" :compact="true" class="mini-track" />
    <div class="mini-meta font-mono">
      <span class="mini-latency" :class="latencyToneClass">{{ latencySummaryText }}</span>
      <span class="mini-loss" :class="lossToneClass">{{ lossSummaryText }}</span>
    </div>
  </div>

  <div v-else class="latency-panel neu-inset">
    <div class="panel-head">
      <span class="head-left">
        <svg class="head-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <polyline points="22 12 18 12 15 21 9 3 6 12 2 12" />
        </svg>
        <span>24 小时</span>
      </span>
      <span class="head-right font-mono" :title="targetTitles">
        <template v-if="failed">
          <span class="text-danger">读取失败</span>
        </template>
        <template v-else>
          <span v-if="jitter !== null" :class="jitterToneClass">波动 {{ Math.round(jitter) }}ms</span>
          <span v-if="jitter !== null && targetCount > 0" class="subtle-divider"> · </span>
          <span v-if="targetCount > 0" class="subtle-text">{{ targetCount }} 个探测</span>
          <span v-else class="subtle-text">未配置探针</span>
        </template>
      </span>
    </div>

    <div class="panel-tracks">
      <div class="track-line">
        <span class="track-label">延迟</span>
        <div class="track-bar">
          <HeatTrack :hours="formattedHours" kind="latency" :probes="targetCount" />
        </div>
        <span class="track-val font-mono" :class="latencyToneClass">
          {{ avgLatency === null ? '—' : `${Math.round(avgLatency)}ms` }}
        </span>
      </div>

      <div class="track-line">
        <span class="track-label">丢包</span>
        <div class="track-bar">
          <HeatTrack :hours="formattedHours" kind="loss" :probes="targetCount" />
        </div>
        <span class="track-val font-mono" :class="lossToneClass">
          {{ avgLoss === null ? '—' : `${Number(avgLoss).toFixed(1)}%` }}
        </span>
      </div>
    </div>

    <div class="panel-foot">
      <span>24 小时前</span>
      <span>现在</span>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import HeatTrack from './HeatTrack.vue'

const props = defineProps({
  latency: {
    type: Object,
    default: null,
  },
  compact: {
    type: Boolean,
    default: false,
  },
})

const failed = computed(() => props.latency?.failed === true)

const avgLatency = computed(() => {
  if (props.latency?.average_latency_ms !== undefined && props.latency?.average_latency_ms !== null) {
    return props.latency.average_latency_ms
  }
  if (props.latency?.avgLatency !== undefined && props.latency?.avgLatency !== null) {
    return props.latency.avgLatency
  }
  return null
})

const avgLoss = computed(() => {
  if (props.latency?.average_loss_percent !== undefined && props.latency?.average_loss_percent !== null) {
    return props.latency.average_loss_percent
  }
  if (props.latency?.avgLoss !== undefined && props.latency?.avgLoss !== null) {
    return props.latency.avgLoss
  }
  return null
})

const jitter = computed(() => {
  if (props.latency?.jitter_ms !== undefined && props.latency?.jitter_ms !== null) {
    return props.latency.jitter_ms
  }
  if (props.latency?.jitter !== undefined && props.latency?.jitter !== null) {
    return props.latency.jitter
  }
  return null
})

const targets = computed(() => {
  if (Array.isArray(props.latency?.targets)) return props.latency.targets
  if (Array.isArray(props.latency?.probes)) return props.latency.probes
  return []
})

const targetCount = computed(() => targets.value.length)

const targetTitles = computed(() => {
  if (!targets.value.length) return ''
  return targets.value.map((t) => t.target || t.name || '').filter(Boolean).join('、')
})

const formattedHours = computed(() => {
  const raw = props.latency?.hours
  if (!Array.isArray(raw)) return []
  return raw.map((item) => ({
    ts: item.timestamp ?? item.ts,
    latency: item.latency_ms ?? item.latency,
    loss: item.loss_percent ?? item.loss,
  }))
})

const latencyToneClass = computed(() => {
  const v = avgLatency.value
  if (v === null || v === undefined) return 'tone-muted'
  if (v < 60) return 'tone-ok'
  if (v < 110) return 'tone-mild'
  if (v < 180) return 'tone-warn'
  if (v < 280) return 'tone-hot'
  return 'tone-bad'
})

const lossToneClass = computed(() => {
  const v = avgLoss.value
  if (v === null || v === undefined) return 'tone-muted'
  if (v < 0.5) return 'tone-ok'
  if (v < 2) return 'tone-mild'
  if (v < 5) return 'tone-warn'
  if (v < 10) return 'tone-hot'
  return 'tone-bad'
})

const jitterToneClass = computed(() => {
  const j = jitter.value
  if (j === null || j === undefined) return 'tone-muted'
  if (j < 15) return 'tone-ok'
  if (j < 40) return 'tone-mild'
  if (j < 80) return 'tone-warn'
  return 'tone-bad'
})

const latencySummaryText = computed(() => {
  if (avgLatency.value === null) return '—'
  const min = props.latency?.min_latency_ms
  const max = props.latency?.max_latency_ms
  if (min !== undefined && max !== undefined && min !== null && max !== null && min < max) {
    const spread = Math.round((max - min) / 2)
    return `${Math.round(min)}–${Math.round(max)}ms${spread > 0 ? ` ±${spread}` : ''}`
  }
  return `${Math.round(avgLatency.value)} ms`
})

const lossSummaryText = computed(() => {
  if (avgLoss.value === null) return '丢 —'
  return `丢 ${avgLoss.value.toFixed(1)}%`
})
</script>

<style scoped>
.latency-panel {
  position: relative;
  overflow: hidden;
  border-radius: 12px;
  padding: 10px 12px;
  background: var(--neu-inset-bg, rgba(15, 23, 42, 0.45));
  border: 1px solid var(--sb-line);
}

.panel-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}

.head-left {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 550;
  color: var(--sb-muted, #94a3b8);
}

.head-icon {
  width: 14px;
  height: 14px;
  color: var(--sb-accent, #38bdf8);
}

.head-right {
  font-size: 11px;
  color: var(--sb-muted, #94a3b8);
  white-space: nowrap;
}

.subtle-divider {
  opacity: 0.6;
}

.subtle-text {
  opacity: 0.85;
}

.panel-tracks {
  display: flex;
  flex-direction: column;
  gap: 7px;
}

.track-line {
  display: flex;
  align-items: center;
  gap: 8px;
}

.track-label {
  width: 28px;
  flex-shrink: 0;
  font-size: 11px;
  color: var(--sb-muted, #94a3b8);
}

.track-bar {
  flex: 1;
  min-width: 0;
}

.track-val {
  width: 48px;
  flex-shrink: 0;
  text-align: right;
  font-size: 11px;
  font-weight: 600;
}

.panel-foot {
  display: flex;
  justify-content: space-between;
  margin-top: 5px;
  padding: 0 54px 0 36px;
  font-size: 9px;
  color: rgba(148, 163, 184, 0.5);
}

/* Compact (Mini) for Table / List view */
.latency-mini {
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 4px;
  min-width: 0;
}

.mini-track {
  height: 7px;
}

.mini-meta {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  font-size: 10px;
  line-height: 1;
  white-space: nowrap;
}

.mini-latency {
  font-weight: 550;
  overflow: hidden;
  text-overflow: ellipsis;
}

.mini-loss {
  flex-shrink: 0;
}

/* Tones aligned with openlat */
.tone-ok { color: var(--heat-ok, #10b981); }
.tone-mild { color: var(--heat-mild, #84cc16); }
.tone-warn { color: var(--heat-warn, #f59e0b); }
.tone-hot { color: var(--heat-hot, #f97316); }
.tone-bad { color: var(--heat-bad, #ef4444); }
.tone-muted { color: var(--sb-muted, #64748b); }
.text-danger { color: var(--sb-danger); }
</style>
