<template>
  <div class="heat-track-container" :class="{ 'is-compact': compact }">
    <span
      v-for="(slot, i) in normalizedSlots"
      :key="i"
      class="heat-cell"
      :class="[
        slot.heatClass,
        { 'slot-gap': i > 0 && i % 6 === 0, 'heat-cell-flat': compact }
      ]"
      :title="slot.tooltip"
    />
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  hours: {
    type: Array,
    default: () => [],
  },
  kind: {
    type: String,
    default: 'latency', // 'latency' | 'loss'
  },
  probes: {
    type: Number,
    default: 1,
  },
  compact: {
    type: Boolean,
    default: false,
  },
})

const TRACK_LABELS = {
  latency: '延迟',
  loss: '丢包',
}

function getHeatClass(value, kind) {
  if (value === null || value === undefined) return 'heat-muted'
  if (kind === 'latency') {
    if (value < 60) return 'heat-ok'
    if (value < 110) return 'heat-mild'
    if (value < 180) return 'heat-warn'
    if (value < 280) return 'heat-hot'
    return 'heat-bad'
  }
  // loss
  if (value < 0.5) return 'heat-ok'
  if (value < 2) return 'heat-mild'
  if (value < 5) return 'heat-warn'
  if (value < 10) return 'heat-hot'
  return 'heat-bad'
}

function formatSlotTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const date = String(d.getDate()).padStart(2, '0')
  const hour = String(d.getHours()).padStart(2, '0')
  return `${month}-${date} ${hour}:00`
}

const normalizedSlots = computed(() => {
  const result = []
  const count = 24
  const source = Array.isArray(props.hours) ? props.hours : []

  // Ensure 24 slots, aligning from right (latest) if fewer
  let items = source
  if (items.length < count) {
    const padCount = count - items.length
    const nowHour = Math.floor(Date.now() / 1000 / 3600) * 3600
    const padding = Array.from({ length: padCount }, (_, idx) => ({
      ts: nowHour - (count - 1 - idx) * 3600,
      latency: null,
      loss: null,
    }))
    items = [...padding, ...items]
  } else if (items.length > count) {
    items = items.slice(items.length - count)
  }

  for (let i = 0; i < count; i++) {
    const item = items[i] || {}
    const value = props.kind === 'latency' ? item.latency : item.loss
    const timeStr = formatSlotTime(item.ts)
    const valStr = value === null || value === undefined
      ? '无数据'
      : props.kind === 'latency'
        ? `${Math.round(value)} ms`
        : `${Number(value).toFixed(1)}%`
    const probeStr = props.probes > 1 ? ` (${props.probes} 个探测)` : ''
    const tooltip = timeStr ? `${timeStr} · ${TRACK_LABELS[props.kind]}: ${valStr}${probeStr}` : '暂无数据'

    result.push({
      value,
      heatClass: getHeatClass(value, props.kind),
      tooltip,
    })
  }

  return result
})
</script>

<style scoped>
.heat-track-container {
  display: flex;
  align-items: stretch;
  gap: 1.5px;
  width: 100%;
  height: 10px;
  min-width: 0;
}
.heat-track-container.is-compact {
  gap: 1px;
  height: 8px;
}
.heat-cell {
  flex: 1;
  min-width: 0;
  border-radius: 3px;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.28), 0 1px 2px rgba(0, 0, 0, 0.45);
  transition: transform 0.15s ease, filter 0.15s ease;
  cursor: pointer;
}
.heat-cell:hover {
  transform: translateY(-1px);
  filter: saturate(1.2) brightness(1.1);
  z-index: 2;
}
.heat-cell-flat {
  border-radius: 2px;
  box-shadow: inset 0 1px 0 rgba(255, 255, 255, 0.18);
}
.slot-gap {
  margin-left: 3px;
}
.is-compact .slot-gap {
  margin-left: 2px;
}
.heat-ok {
  background-color: var(--heat-ok, #10b981);
  background-image: linear-gradient(165deg, rgba(255, 255, 255, 0.3), transparent);
}
.heat-mild {
  background-color: var(--heat-mild, #84cc16);
  background-image: linear-gradient(165deg, rgba(255, 255, 255, 0.3), transparent);
}
.heat-warn {
  background-color: var(--heat-warn, #f59e0b);
  background-image: linear-gradient(165deg, rgba(255, 255, 255, 0.3), transparent);
}
.heat-hot {
  background-color: var(--heat-hot, #f97316);
  background-image: linear-gradient(165deg, rgba(255, 255, 255, 0.3), transparent);
}
.heat-bad {
  background-color: var(--heat-bad, #ef4444);
  background-image: linear-gradient(165deg, rgba(255, 255, 255, 0.3), transparent);
}
.heat-muted {
  background-color: var(--heat-muted, rgba(148, 163, 184, 0.16));
  box-shadow: inset 0 1px 3px rgba(0, 0, 0, 0.4);
}
.heat-muted:hover {
  transform: none;
  filter: none;
}
</style>
