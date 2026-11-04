<template>
  <div class="node-map-card neu" :class="className">
    <div class="map-head">
      <div class="head-left">
        <span class="globe-badge">
          <svg class="size-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="12" cy="12" r="10" />
            <line x1="2" y1="12" x2="22" y2="12" />
            <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
          </svg>
        </span>
        <span class="head-title">节点分布</span>
      </div>
      <div class="head-right">
        <span class="tnum head-stat">
          {{ mappedCount }} 个节点 · {{ places.length }} 个地区
        </span>
        <span v-if="activeLinkCount > 0" class="flow-badge font-mono">
          <span class="flow-badge-dot" />
          <span>{{ activeLinkCount }} 条流量飞线</span>
          <span v-if="totalFlowRate > 0" class="flow-rate">({{ formatBytes(totalFlowRate, '/s') }})</span>
        </span>
      </div>
    </div>

    <div class="map-sub">
      <span class="tnum status-count">
        在线 <span class="text-ok font-semibold">{{ onlineCount }}</span>
        <template v-if="offlineCount > 0">
          · 离线 <span class="text-danger font-semibold">{{ offlineCount }}</span>
        </template>
        <template v-if="unmappedCount > 0">
          · 未定位 <span class="text-muted font-medium">{{ unmappedCount }}</span>
        </template>
      </span>
      <div class="legend-wrap">
        <span v-for="item in LEGEND" :key="item.tone" class="legend-item" :class="`is-${item.tone}`">
          <span class="legend-dot" :class="item.dotClass" />
          <span :class="item.tone === 'all' ? 'text-ok font-medium' : ''">{{ item.label }}</span>
        </span>
      </div>
    </div>

    <div ref="wrapRef" class="map-canvas-wrap">
      <!-- 下层：矢量静态底图与精准节点打点 (保持无损矢量与交互) -->
      <svg
        ref="svgRef"
        :viewBox="`0 0 ${MAP_W} ${MAP_H}`"
        preserveAspectRatio="xMidYMid meet"
        class="map-svg"
      >
        <!-- Static Land Dot Matrix -->
        <g class="land-dots">
          <circle
            v-for="(pos, i) in LAND_POSITIONS"
            :key="i"
            :cx="pos[0]"
            :cy="pos[1]"
            r="1.05"
          />
        </g>

        <!-- Place Pins / Highlights (精准独立经纬度打点) -->
        <g v-for="p in places" :key="p.key" class="place-pin-group">
          <!-- Pulse halo -->
          <circle
            :cx="p.x"
            :cy="p.y"
            r="4.2"
            class="pin-halo"
            :class="p.toneClass"
          />
          <!-- Center dot -->
          <circle
            :cx="p.x"
            :cy="p.y"
            r="2.2"
            class="pin-core cursor-pointer"
            :class="p.toneClass"
            @click.stop="toggleSelect(p.key)"
          >
            <title>{{ p.label }}: {{ p.online }}/{{ p.nodes.length }} 在线</title>
          </circle>
        </g>
      </svg>

      <!-- 上层：高性能动态流星飞线画布 (鼠标穿透，稳锁 60FPS) -->
      <canvas
        ref="canvasRef"
        class="map-fly-canvas"
      />

      <!-- Region Popup on marker click -->
      <transition name="pop-fade">
        <div v-if="activePlace" class="place-popup neu" @click.stop>
          <div class="popup-head">
            <span class="popup-flag">{{ activePlace.flag }}</span>
            <span class="popup-title">{{ activePlace.label }}</span>
            <span class="popup-stat">{{ activePlace.online }}/{{ activePlace.nodes.length }} 在线</span>
            <button class="popup-close" @click="selectedKey = null">&times;</button>
          </div>
          <div class="popup-nodes-list">
            <div
              v-for="node in activePlace.nodes"
              :key="node.id"
              class="popup-node-item"
              @click="handleNodeClick(node.id)"
            >
              <span class="popup-node-status" :class="node.online ? 'is-online' : 'is-offline'" />
              <span class="popup-node-name truncate">{{ node.name }}</span>
              <span class="popup-node-arrow">&rsaquo;</span>
            </div>
          </div>
        </div>
      </transition>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { LAND_POSITIONS, MAP_H, MAP_W, project } from '../geo'
import { connectionSnapshots, subscribeConnections } from '../connections'
import { formatBytes } from '../format'

const props = defineProps({
  nodes: {
    type: Array,
    default: () => [],
  },
  live: {
    type: Object,
    default: null,
  },
  className: {
    type: String,
    default: '',
  },
})

const emit = defineEmits(['open'])

const LEGEND = [
  { tone: 'all', label: '全部在线', dotClass: 'tone-all' },
  { tone: 'some', label: '部分离线', dotClass: 'tone-some' },
  { tone: 'none', label: '全部离线', dotClass: 'tone-none' },
]

const wrapRef = ref(null)
const svgRef = ref(null)
const canvasRef = ref(null)
const selectedKey = ref(null)

let unsubConnections = null
let animationFrameId = null
let resizeObserver = null
let isVisible = true

// 内部活跃粒子队列与终点波纹
const activeParticles = []
const targetRipples = []

function countryCodeToFlag(code) {
  if (!code || code.length !== 2) return '🌐'
  const offset = 127397
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => c.charCodeAt(0) + offset))
}

// 纯数据驱动：根据真实下发的物理经纬度进行城市/机房级别的打点，零硬编码！
const places = computed(() => {
  const map = new Map()

  for (const node of props.nodes) {
    const lat = Number(node.latitude)
    const lon = Number(node.longitude)
    const hasValidCoords = Boolean(node.has_coords || (!isNaN(lat) && !isNaN(lon) && (lat !== 0 || lon !== 0)))
    if (!hasValidCoords) continue

    const [x, y] = project(lat, lon)
    // 以保留 3 位小数的坐标指纹作为聚合键（约 100 米范围，同机房聚合，不同城市自然独立）
    const key = `${lat.toFixed(3)},${lon.toFixed(3)}`

    let place = map.get(key)
    if (!place) {
      let regionName = node.city || node.location
      if (!regionName) {
        try {
          const dn = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
          regionName = dn.of(node.country_code) || node.country_code || '未知区域'
        } catch {
          regionName = node.country_code || '未知区域'
        }
      }
      place = {
        key,
        code: key,
        label: regionName,
        flag: countryCodeToFlag(node.country_code),
        x,
        y,
        lat,
        lon,
        nodes: [],
        online: 0,
      }
      map.set(key, place)
    }
    place.nodes.push(node)
    if (node.online) place.online++
  }

  return [...map.values()].map((p) => {
    let toneClass = 'tone-all'
    if (p.online === 0) toneClass = 'tone-none'
    else if (p.online < p.nodes.length) toneClass = 'tone-some'
    return { ...p, toneClass }
  })
})

const activePlace = computed(() => {
  if (!selectedKey.value) return null
  return places.value.find((p) => p.key === selectedKey.value) || null
})

const mappedCount = computed(() => {
  return places.value.reduce((sum, p) => sum + p.nodes.length, 0)
})

const onlineCount = computed(() => {
  return places.value.reduce((sum, p) => sum + p.online, 0)
})

const offlineCount = computed(() => {
  return mappedCount.value - onlineCount.value
})

const unmappedCount = computed(() => {
  return Math.max(0, props.nodes.length - mappedCount.value)
})

function toggleSelect(key) {
  selectedKey.value = selectedKey.value === key ? null : key
}

function handleNodeClick(nodeId) {
  emit('open', nodeId)
  selectedKey.value = null
}

function onGlobalClick(e) {
  if (!e.target.closest('.place-pin-group') && !e.target.closest('.place-popup')) {
    selectedKey.value = null
  }
}

// ─────────────────────────────────────────────────────────────
// 流量与飞线引擎 (Flow & Flying Lines System)
// ─────────────────────────────────────────────────────────────

const activeLinkCount = ref(0)
const totalFlowRate = ref(0)

// 提取当前全部活跃流量链路：(客户端经纬度 -> 服务器节点经纬度)
function extractActiveLinks() {
  const nodeMap = new Map((props.nodes || []).map((n) => [n.id, n]))
  const rawSnapshots = props.live || connectionSnapshots.value
  if (!rawSnapshots || rawSnapshots.size === 0) return []

  const linkMap = new Map()
  let sumRate = 0

  for (const snapshot of rawSnapshots.values()) {
    const node = nodeMap.get(snapshot.node_id)
    if (!node) continue

    const nLat = Number(node.latitude)
    const nLon = Number(node.longitude)
    if (isNaN(nLat) || isNaN(nLon) || (nLat === 0 && nLon === 0)) continue
    const targetXY = project(nLat, nLon)

    for (const c of snapshot.connections || []) {
      const sLat = Number(c.source_lat)
      const sLon = Number(c.source_lon)
      const hasSrc = Boolean(c.has_source_coords || (!isNaN(sLat) && !isNaN(sLon) && (sLat !== 0 || sLon !== 0)))
      if (!hasSrc) continue

      const sourceXY = project(sLat, sLon)
      // 过滤客户端与服务端完全重合的点
      if (Math.hypot(targetXY[0] - sourceXY[0], targetXY[1] - sourceXY[1]) < 2) continue

      const linkKey = `${sourceXY[0].toFixed(1)},${sourceXY[1].toFixed(1)}->${targetXY[0].toFixed(1)},${targetXY[1].toFixed(1)}`
      const rate = (c.download_rate || 0) + (c.upload_rate || 0)
      sumRate += rate

      let link = linkMap.get(linkKey)
      if (!link) {
        link = {
          key: linkKey,
          sourceXY,
          targetXY,
          totalRate: 0,
          count: 0,
          sourceCity: c.source_location || '客户端',
          nodeName: node.name,
        }
        linkMap.set(linkKey, link)
      }
      link.totalRate += rate
      link.count++
    }
  }

  activeLinkCount.value = linkMap.size
  totalFlowRate.value = sumRate
  return [...linkMap.values()]
}

// 动态控制粒子池数量以匹配活跃链路
function syncParticles(links) {
  if (links.length === 0) {
    activeParticles.length = 0
    return
  }

  const linkMap = new Map(links.map((l) => [l.key, l]))

  // 清除失效链路的粒子
  for (let i = activeParticles.length - 1; i >= 0; i--) {
    if (!linkMap.has(activeParticles[i].linkKey)) {
      activeParticles.splice(i, 1)
    }
  }

  // 为每条活跃链路维护粒子 (按网速分配 1 ~ 3 个粒子)
  for (const link of links) {
    const desired = link.totalRate > 1024 * 1024 ? 3 : (link.totalRate > 50 * 1024 ? 2 : 1)
    const existing = activeParticles.filter((p) => p.linkKey === link.key)

    if (existing.length < desired) {
      for (let i = existing.length; i < desired; i++) {
        activeParticles.push({
          linkKey: link.key,
          link,
          t: Math.random(),
          trail: [],
        })
      }
    }
    // 更新引用
    for (const p of existing) {
      p.link = link
    }
  }
}

// Canvas 60 FPS 渲染循环
function renderFrame() {
  if (!isVisible) {
    animationFrameId = requestAnimationFrame(renderFrame)
    return
  }

  const canvas = canvasRef.value
  const svgEl = svgRef.value
  if (!canvas || !svgEl) {
    animationFrameId = requestAnimationFrame(renderFrame)
    return
  }

  const ctx = canvas.getContext('2d')
  if (!ctx) return

  // 计算 SVG 的实际渲染区域，实现像素级完美重合
  const rect = svgEl.getBoundingClientRect()
  const dpr = window.devicePixelRatio || 1
  const displayW = Math.round(rect.width * dpr)
  const displayH = Math.round(rect.height * dpr)

  if (canvas.width !== displayW || canvas.height !== displayH) {
    canvas.width = displayW
    canvas.height = displayH
  }

  ctx.clearRect(0, 0, canvas.width, canvas.height)

  const svgAspect = MAP_W / MAP_H
  const boxAspect = rect.width / rect.height
  let scale, offsetX = 0, offsetY = 0

  if (boxAspect > svgAspect) {
    scale = rect.height / MAP_H
    offsetX = (rect.width - MAP_W * scale) / 2
  } else {
    scale = rect.width / MAP_W
    offsetY = (rect.height - MAP_H * scale) / 2
  }

  ctx.save()
  ctx.setTransform(scale * dpr, 0, 0, scale * dpr, offsetX * dpr, offsetY * dpr)

  const links = extractActiveLinks()
  syncParticles(links)

  // 1. 绘制背景微弱飞线轨道与起点脉冲
  for (const link of links) {
    const [x1, y1] = link.sourceXY
    const [x2, y2] = link.targetXY

    const mx = (x1 + x2) / 2
    const dist = Math.hypot(x2 - x1, y2 - y1)
    const arcHeight = Math.min(38, Math.max(10, dist * 0.26))
    const cx = mx
    const cy = Math.min(y1, y2) - arcHeight

    // 绘制飞线轨道弧线
    ctx.beginPath()
    ctx.moveTo(x1, y1)
    ctx.quadraticCurveTo(cx, cy, x2, y2)
    ctx.strokeStyle = 'rgba(56, 189, 248, 0.16)'
    ctx.lineWidth = 0.65
    ctx.stroke()

    // 绘制客户端发射点小微光
    ctx.beginPath()
    ctx.arc(x1, y1, 1.2, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(56, 189, 248, 0.7)'
    ctx.fill()
  }

  // 2. 绘制彗星粒子及发光残影拖尾
  for (const p of activeParticles) {
    const link = p.link
    if (!link) continue

    const [x1, y1] = link.sourceXY
    const [x2, y2] = link.targetXY
    const mx = (x1 + x2) / 2
    const dist = Math.hypot(x2 - x1, y2 - y1)
    const arcHeight = Math.min(38, Math.max(10, dist * 0.26))
    const cx = mx
    const cy = Math.min(y1, y2) - arcHeight

    // 步进速度随实时网速自适应
    const speed = 0.007 + Math.min(0.022, (link.totalRate / (2 * 1024 * 1024)) * 0.01)
    p.t += speed

    if (p.t > 1) {
      p.t = 0
      p.trail = []
      // 触发终点波纹
      targetRipples.push({
        x: x2,
        y: y2,
        r: 1.5,
        alpha: 0.9,
      })
    }

    // 计算贝塞尔当前坐标
    const it = 1 - p.t
    const px = it * it * x1 + 2 * it * p.t * cx + p.t * p.t * x2
    const py = it * it * y1 + 2 * it * p.t * cy + p.t * p.t * y2

    p.trail.push({ x: px, y: py })
    if (p.trail.length > 9) p.trail.shift()

    // 绘制彗星渐变拖尾
    if (p.trail.length > 1) {
      ctx.beginPath()
      ctx.moveTo(p.trail[0].x, p.trail[0].y)
      for (let i = 1; i < p.trail.length; i++) {
        ctx.lineTo(p.trail[i].x, p.trail[i].y)
      }
      ctx.strokeStyle = 'rgba(56, 189, 248, 0.85)'
      ctx.lineWidth = Math.min(2.5, 0.9 + (link.totalRate / (1024 * 1024)) * 0.8)
      ctx.stroke()
    }

    // 绘制流星高亮头部粒子
    ctx.beginPath()
    ctx.arc(px, py, 1.25, 0, Math.PI * 2)
    ctx.fillStyle = '#ffffff'
    ctx.fill()
  }

  // 3. 绘制终点服务器波纹扩散
  for (let i = targetRipples.length - 1; i >= 0; i--) {
    const rip = targetRipples[i]
    rip.r += 0.22
    rip.alpha -= 0.03

    if (rip.alpha <= 0 || rip.r > 6.5) {
      targetRipples.splice(i, 1)
      continue
    }

    ctx.beginPath()
    ctx.arc(rip.x, rip.y, rip.r, 0, Math.PI * 2)
    ctx.strokeStyle = `rgba(56, 189, 248, ${rip.alpha})`
    ctx.lineWidth = 0.8
    ctx.stroke()
  }

  ctx.restore()
  animationFrameId = requestAnimationFrame(renderFrame)
}

function handleVisibilityChange() {
  isVisible = !document.hidden
}

onMounted(() => {
  window.addEventListener('click', onGlobalClick)
  document.addEventListener('visibilitychange', handleVisibilityChange)

  // 自动订阅全局实时连接 SSE 流
  unsubConnections = subscribeConnections(() => {})

  // 启动 Canvas 60 FPS 渲染循环
  animationFrameId = requestAnimationFrame(renderFrame)
})

onUnmounted(() => {
  window.removeEventListener('click', onGlobalClick)
  document.removeEventListener('visibilitychange', handleVisibilityChange)

  if (animationFrameId) {
    cancelAnimationFrame(animationFrameId)
    animationFrameId = null
  }
  if (unsubConnections) {
    unsubConnections()
    unsubConnections = null
  }
  if (resizeObserver) {
    resizeObserver.disconnect()
    resizeObserver = null
  }
})
</script>

<style scoped>
.node-map-card {
  position: relative;
  overflow: hidden;
  border-radius: 16px;
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
}

.map-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.head-left {
  display: flex;
  align-items: center;
  gap: 8px;
}

.head-right {
  display: flex;
  align-items: center;
  gap: 10px;
}

.globe-badge {
  display: grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 8px;
  background: color-mix(in oklab, var(--sb-accent) 15%, transparent);
  color: var(--sb-accent);
}

.head-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--sb-text);
}

.head-stat {
  font-size: 11px;
  color: var(--sb-muted);
}

.flow-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 10.5px;
  color: #38bdf8;
  background: rgba(56, 189, 248, 0.1);
  border: 1px solid rgba(56, 189, 248, 0.25);
  padding: 1px 7px;
  border-radius: 9999px;
}

.flow-badge-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #38bdf8;
  box-shadow: 0 0 6px #38bdf8;
  animation: flow-pulse 1.8s ease-in-out infinite;
}

.flow-rate {
  font-size: 10px;
  opacity: 0.85;
}

@keyframes flow-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50% { opacity: 0.4; transform: scale(0.85); }
}

.map-sub {
  margin-top: 6px;
  padding-left: 32px;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  font-size: 11px;
}

.status-count {
  color: var(--sb-muted);
}

.text-ok { color: var(--sb-success, #10b981); }
.text-danger { color: var(--sb-danger, #ef4444); }
.text-muted { color: var(--sb-muted); }

.legend-wrap {
  display: flex;
  align-items: center;
  gap: 10px;
}

.legend-item {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--sb-muted);
  font-size: 11px;
}

.legend-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.legend-dot.tone-all {
  background: var(--sb-success, #10b981);
  box-shadow: 0 0 6px color-mix(in oklab, var(--sb-success, #10b981) 40%, transparent);
}
.legend-dot.tone-some { background: var(--sb-warning, #f59e0b); }
.legend-dot.tone-none { background: var(--sb-danger, #ef4444); }

.map-canvas-wrap {
  position: relative;
  margin-top: 10px;
  width: 100%;
}

.map-svg {
  display: block;
  width: 100%;
  height: auto;
  max-height: 180px;
}

.map-fly-canvas {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  pointer-events: none;
}

.land-dots circle {
  fill: var(--sb-muted);
  opacity: 0.3;
}

.pin-halo {
  animation: pulse-ring 2.4s ease-out infinite;
}

.pin-halo.tone-all { fill: var(--sb-success, #10b981); }
.pin-halo.tone-some { fill: var(--sb-warning, #f59e0b); }
.pin-halo.tone-none { fill: var(--sb-danger, #ef4444); }

.pin-core {
  transition: transform 0.15s ease;
}

.pin-core:hover {
  transform: scale(1.3);
}

.pin-core.tone-all { fill: var(--sb-success, #10b981); }
.pin-core.tone-some { fill: var(--sb-warning, #f59e0b); }
.pin-core.tone-none { fill: var(--sb-danger, #ef4444); }

@keyframes pulse-ring {
  0% { r: 2.5; opacity: 0.7; }
  100% { r: 5.5; opacity: 0; }
}

.place-popup {
  position: absolute;
  top: 12px;
  right: 12px;
  z-index: 10;
  width: 220px;
  border-radius: 12px;
  padding: 10px 12px;
  box-shadow: var(--sh-neu);
}

.popup-head {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  font-weight: 600;
  border-bottom: 1px solid var(--sb-line);
  padding-bottom: 6px;
}

.popup-flag { font-size: 14px; }
.popup-stat { font-size: 10px; color: var(--sb-muted); margin-left: auto; }
.popup-close {
  background: none;
  border: none;
  color: var(--sb-muted);
  font-size: 16px;
  cursor: pointer;
}

.popup-nodes-list {
  margin-top: 6px;
  max-height: 140px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.popup-node-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 6px;
  border-radius: 6px;
  font-size: 11px;
  cursor: pointer;
  transition: background-color 0.15s ease;
}

.popup-node-item:hover {
  background: var(--neu-inset-bg);
}

.popup-node-status {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.popup-node-status.is-online { background: var(--sb-success, #10b981); }
.popup-node-status.is-offline { background: var(--sb-danger, #ef4444); }

.popup-node-arrow {
  margin-left: auto;
  color: var(--sb-muted);
  font-size: 14px;
}

.pop-fade-enter-active, .pop-fade-leave-active { transition: opacity 0.15s ease, transform 0.15s ease; }
.pop-fade-enter-from, .pop-fade-leave-to { opacity: 0; transform: scale(0.95); }
</style>
