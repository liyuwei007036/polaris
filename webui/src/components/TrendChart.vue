<template>
  <div ref="root" class="trend-chart" :style="{ height: `${height}px` }" @mousemove="onMove" @mouseleave="hover = null">
    <svg :viewBox="`0 0 ${W} ${H}`" preserveAspectRatio="none" class="trend-svg">
      <defs>
        <linearGradient v-for="(line, index) in lines" :id="`${gradientId}-${index}`" :key="index" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" :stop-color="line.color" stop-opacity="0.36" />
          <stop offset="100%" :stop-color="line.color" stop-opacity="0.02" />
        </linearGradient>
      </defs>
      <g v-for="tick in yTicks" :key="tick.value">
        <line :x1="PAD_LEFT" :x2="W - PAD_RIGHT" :y1="tick.y" :y2="tick.y" class="trend-grid" />
        <text :x="0" :y="tick.y" class="trend-axis" dominant-baseline="middle">{{ tick.label }}</text>
      </g>
      <template v-for="(line, index) in lines" :key="index">
        <path v-if="line.area" :d="line.area" :fill="`url(#${gradientId}-${index})`" stroke="none" />
        <path :d="line.path" fill="none" :stroke="line.color" stroke-width="2" vector-effect="non-scaling-stroke" />
      </template>
      <template v-if="hover">
        <line :x1="hover.x" :x2="hover.x" :y1="PAD_TOP" :y2="H - PAD_BOTTOM" class="trend-cursor" />
        <circle v-for="(dot, index) in hover.dots" :key="index" :cx="dot.x" :cy="dot.y" r="3" :fill="dot.color" />
      </template>
    </svg>
    <div class="trend-x">
      <span>{{ xLabels[0] }}</span>
      <span>{{ xLabels[1] }}</span>
    </div>
    <div v-if="hover" class="trend-tip" :style="{ left: `${(hover.x / W) * 100}%` }">
      <strong v-for="(item, index) in hover.values" :key="index">
        <span v-if="lines.length > 1" class="trend-swatch" :style="{ background: item.color }" />{{ item.text }}
      </strong>
      <small>{{ hover.time }}</small>
    </div>
    <p v-if="!hasData" class="trend-empty">这段时间没有历史数据</p>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps({
  // 单序列简写：points + color。多序列用 series。
  points: { type: Array, default: () => [] },
  color: { type: String, default: 'var(--sb-accent)' },
  series: { type: Array, default: () => [] },
  height: { type: Number, default: 176 },
  area: { type: Boolean, default: true },
  format: { type: Function, default: (v) => String(Math.round(v * 10) / 10) },
})

const root = ref(null)
const W = ref(600)
const H = computed(() => Math.max(80, props.height - 16))
const PAD_LEFT = 38
const PAD_RIGHT = 6
const PAD_TOP = 10
const PAD_BOTTOM = 6
const hover = ref(null)
const gradientId = `trend-${Math.random().toString(36).slice(2, 9)}`

let observer
onMounted(() => {
  if (!root.value || typeof ResizeObserver === 'undefined') return
  observer = new ResizeObserver((entries) => {
    const width = entries[0]?.contentRect?.width || 0
    if (width > 0) W.value = Math.max(240, width)
  })
  observer.observe(root.value)
})
onBeforeUnmount(() => observer?.disconnect())

const sources = computed(() =>
  props.series.length ? props.series : [{ points: props.points, color: props.color }],
)

const linesData = computed(() =>
  sources.value.map((source) => ({
    color: source.color || props.color,
    points: (source.points || [])
      .filter((p) => Number.isFinite(Number(p.ts)))
      .slice()
      .sort((a, b) => Number(a.ts) - Number(b.ts)),
  })),
)

const allPoints = computed(() => linesData.value.flatMap((line) => line.points))
// 丢包点是 null：它们不参与数值范围，但保留在横轴里，曲线在缺口处断开，
// 与 ref 的 recharts Line（connectNulls 默认关闭）一致。
const validPoints = computed(() => allPoints.value.filter((p) => Number.isFinite(p.v)))
const hasData = computed(() => validPoints.value.length > 0)

const bounds = computed(() => {
  const values = validPoints.value.map((p) => p.v)
  if (!values.length) return { min: 0, max: 1 }
  const max = Math.max(...values)
  const min = Math.min(...values)
  return { min: min === max ? 0 : min, max: max === min ? (max || 1) * 1.2 : max }
})

// 以时间戳作为横轴：不同探测的缺失点不会把曲线挤错位。
const domain = computed(() => {
  const stamps = allPoints.value.map((p) => Number(p.ts)).filter(Number.isFinite)
  if (!stamps.length) return { min: 0, max: 1 }
  const min = Math.min(...stamps)
  const max = Math.max(...stamps)
  return { min, max: max === min ? min + 1 : max }
})

const plotWidth = computed(() => W.value - PAD_LEFT - PAD_RIGHT)
const plotHeight = computed(() => H.value - PAD_TOP - PAD_BOTTOM)

function project(point) {
  const { min, max } = bounds.value
  const { min: minTs, max: maxTs } = domain.value
  const ts = Number(point.ts)
  const ratio = Number.isFinite(ts) ? (ts - minTs) / (maxTs - minTs || 1) : 0
  const x = PAD_LEFT + Math.min(Math.max(ratio, 0), 1) * plotWidth.value
  const y = PAD_TOP + (1 - (point.v - min) / (max - min || 1)) * plotHeight.value
  return { x, y }
}

const lines = computed(() =>
  linesData.value.map((line) => {
    let path = ''
    let pen = false
    for (const point of line.points) {
      if (!Number.isFinite(point.v)) {
        pen = false
        continue
      }
      const { x, y } = project(point)
      path += `${pen ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`
      pen = true
    }
    let area = ''
    if (props.area && line.points.length && line.points.every((p) => Number.isFinite(p.v))) {
      const first = project(line.points[0])
      const last = project(line.points[line.points.length - 1])
      area = `${path} L${last.x.toFixed(1)},${H.value - PAD_BOTTOM} L${first.x.toFixed(1)},${H.value - PAD_BOTTOM} Z`
    }
    return { ...line, path, area }
  }),
)

const yTicks = computed(() => {
  const { min, max } = bounds.value
  return [0, 0.5, 1].map((ratio) => {
    const value = max - (max - min) * ratio
    return { value, label: props.format(value), y: PAD_TOP + ratio * plotHeight.value }
  })
})

const xLabels = computed(() => {
  if (!hasData.value) return ['', '']
  const clock = (ts) => new Date(Number(ts) * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  return [clock(domain.value.min), clock(domain.value.max)]
})

function onMove(event) {
  if (!hasData.value) return
  const rect = event.currentTarget.getBoundingClientRect()
  const ratio = Math.min(Math.max((event.clientX - rect.left) / rect.width, 0), 1)
  const targetTs = domain.value.min + ratio * (domain.value.max - domain.value.min)
  const dots = []
  const values = []
  let reference = null
  for (const line of linesData.value) {
    if (!line.points.length) continue
    const nearest = line.points.reduce((best, point) =>
      Math.abs(Number(point.ts) - targetTs) < Math.abs(Number(best.ts) - targetTs) ? point : best,
    )
    const { x, y } = project(nearest)
    dots.push({ x, y, color: line.color })
    values.push({ text: props.format(nearest.v), color: line.color })
    if (!reference) reference = nearest
  }
  if (!reference) return
  hover.value = {
    x: dots[0]?.x ?? 0,
    dots,
    values,
    time: new Date(Number(reference.ts) * 1000).toLocaleString('zh-CN'),
  }
}
</script>

<style scoped>
.trend-chart { position: relative; width: 100%; }
.trend-svg { width: 100%; height: calc(100% - 16px); overflow: visible; }
.trend-grid { stroke: var(--sb-line); stroke-dasharray: 3 3; stroke-width: 1; }
.trend-axis { fill: var(--sb-muted); font-size: 10px; }
.trend-cursor { stroke: var(--sb-muted); stroke-dasharray: 3 3; stroke-width: 1; }
.trend-x { display: flex; justify-content: space-between; margin-top: 4px; padding: 0 1% 0 6.3%; color: var(--sb-muted); font-size: 10.5px; }
.trend-tip { position: absolute; top: -6px; transform: translateX(-50%); padding: 4px 8px; border-radius: 8px; background: var(--sb-surface); border: 1px solid var(--sb-line); box-shadow: var(--sb-shadow); display: grid; gap: 1px; pointer-events: none; }
.trend-tip strong { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--sb-text); font-variant-numeric: tabular-nums; font-weight: 600; }
.trend-swatch { width: 7px; height: 7px; border-radius: 2px; }
.trend-tip small { font-size: 10px; color: var(--sb-muted); }
.trend-empty { position: absolute; inset: 0; display: grid; place-items: center; margin: 0; font-size: 12.5px; color: var(--sb-muted); }
</style>
