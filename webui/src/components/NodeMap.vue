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

// 记录每条链路的上一次发弹时间戳 (ms)
const linkSpawnTimestamps = new Map()

// 提取当前全部真实连接链路：从 sing-box 上报的所有连接 1:1 完整提取
function extractActiveLinks() {
  const nodeMap = new Map((props.nodes || []).map((n) => [n.id, n]))
  const rawSnapshots = props.live && props.live.size > 0 ? props.live : connectionSnapshots.value
  if (!rawSnapshots || rawSnapshots.size === 0) return []

  // 集群节点 IP 映射表（用于解析节点之间的中继或互联流量来源）
  const fleetByIP = new Map()
  for (const n of props.nodes || []) {
    if (n.ip) fleetByIP.set(n.ip, n)
    if (n.last_observed_ip) fleetByIP.set(n.last_observed_ip, n)
    if (n.client_address) fleetByIP.set(n.client_address.split(':')[0], n)
  }

  const linkMap = new Map()

  for (const snapshot of rawSnapshots.values()) {
    const node = nodeMap.get(snapshot.node_id)
    if (!node) continue

    const nLat = Number(node.latitude)
    const nLon = Number(node.longitude)
    if (isNaN(nLat) || isNaN(nLon) || (nLat === 0 && nLon === 0)) continue
    const targetXY = project(nLat, nLon)

    // 100% 忠实遍历 sing-box 上报的每条连接，严禁以链式代理为由丢弃任何连接
    for (const c of snapshot.connections || []) {
      let sLat = Number(c.source_lat)
      let sLon = Number(c.source_lon)
      let hasSrc = Boolean(c.has_source_coords || (!isNaN(sLat) && !isNaN(sLon) && (sLat !== 0 || sLon !== 0)))

      // 若来源无经纬度，检查是否来自集群内其他节点（中继/级联）
      if (!hasSrc && c.source_ip) {
        const fromNode = fleetByIP.get(c.source_ip)
        if (fromNode && fromNode.has_coords) {
          sLat = Number(fromNode.latitude)
          sLon = Number(fromNode.longitude)
          hasSrc = !isNaN(sLat) && !isNaN(sLon) && (sLat !== 0 || sLon !== 0)
        }
      }

      if (!hasSrc) continue

      const sourceXY = project(sLat, sLon)
      // 过滤完全重合的同物理点
      if (Math.hypot(targetXY[0] - sourceXY[0], targetXY[1] - sourceXY[1]) < 2) continue

      const linkKey = `${sourceXY[0].toFixed(1)},${sourceXY[1].toFixed(1)}->${targetXY[0].toFixed(1)},${targetXY[1].toFixed(1)}`
      const rate = (c.download_rate || 0) + (c.upload_rate || 0)

      let link = linkMap.get(linkKey)
      if (!link) {
        link = {
          key: linkKey,
          sourceXY,
          targetXY,
          totalRate: 0,
          count: 0,
        }
        linkMap.set(linkKey, link)
      }
      link.totalRate += rate
      link.count++
    }
  }

  return [...linkMap.values()]
}

// 动态发射粒子：流星发射频率严格由该航线上的真实连接数与网速共同驱动
function updateAndSpawnParticles(links, now) {
  if (links.length === 0) return

  for (const link of links) {
    // 发弹间隔由该链路聚合的真实连接数量(count)与网速综合决定：
    // 1条连接：约 1200ms 发送 1 颗流星
    // 10条连接：约 400ms 发送 1 颗
    // 50条以上连接（如大流量并发）：约 80ms~150ms 密集发射粒子流，真实呈现高并发流量
    const countScale = Math.max(1, Math.min(30, link.count))
    const baseInterval = Math.max(80, Math.floor(1200 / countScale))
    const speedBoost = link.totalRate > 1024 * 1024 ? 0.7 : 1
    const interval = Math.max(60, Math.floor(baseInterval * speedBoost))

    const lastSpawn = linkSpawnTimestamps.get(link.key) || 0
    if (now - lastSpawn >= interval) {
      linkSpawnTimestamps.set(link.key, now)

      // 粒子的飞翔速度与线宽
      const speed = Math.min(0.024, 0.012 + (link.totalRate / (5 * 1024 * 1024)) * 0.008)
      const lineWidth = Math.min(2.5, 1.0 + Math.min(link.count / 20, 1.0) + (link.totalRate / (2 * 1024 * 1024)) * 0.5)

      // 多连接并发时引入微妙的侧向微扰（±2px），呈现真实的多车道粒子束，而非死板的一根细线
      const jitter = link.count > 1 ? (Math.random() - 0.5) * Math.min(4, link.count * 0.2) : 0

      activeParticles.push({
        sourceXY: link.sourceXY,
        targetXY: link.targetXY,
        t: 0,
        speed,
        lineWidth,
        jitter,
        trail: [],
      })
    }
  }

  // 释放过期链路的时间戳防止内存泄漏
  if (linkSpawnTimestamps.size > 100) {
    const activeKeys = new Set(links.map((l) => l.key))
    for (const k of linkSpawnTimestamps.keys()) {
      if (!activeKeys.has(k)) linkSpawnTimestamps.delete(k)
    }
  }
}

// Canvas 60 FPS 渲染循环 (严禁任何蓝色，采用翠绿 #10b981 与白亮微光)
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

  const now = performance.now()
  const links = extractActiveLinks()
  updateAndSpawnParticles(links, now)

  // 1. 更新并绘制所有在途流星粒子 (严禁常驻死线，严禁蓝色，采用高雅翠绿 #10b981)
  for (let i = activeParticles.length - 1; i >= 0; i--) {
    const p = activeParticles[i]
    p.t += p.speed

    // 到达终点
    if (p.t >= 1) {
      // 抵达目标服务器节点，触发翠绿能量吸收波纹
      targetRipples.push({
        x: p.targetXY[0],
        y: p.targetXY[1],
        r: 1.5,
        alpha: 0.85,
      })
      // 粒子完成单次飞行使命，彻底销毁（不留常驻死线）
      activeParticles.splice(i, 1)
      continue
    }

    const [x1, y1] = p.sourceXY
    const [x2, y2] = p.targetXY
    const mx = (x1 + x2) / 2
    const dist = Math.hypot(x2 - x1, y2 - y1)
    const arcHeight = Math.min(38, Math.max(10, dist * 0.26))
    const cx = mx
    const cy = Math.min(y1, y2) - arcHeight + (p.jitter || 0)

    // 贝塞尔当前飞行坐标
    const it = 1 - p.t
    const px = it * it * x1 + 2 * it * p.t * cx + p.t * p.t * x2
    const py = it * it * y1 + 2 * it * p.t * cy + p.t * p.t * y2

    p.trail.push({ x: px, y: py })
    if (p.trail.length > 12) p.trail.shift()

    // 绘制流星动态渐变拖尾（翠绿色系 #10b981，坚决不含任何蓝色）
    if (p.trail.length > 1) {
      const grad = ctx.createLinearGradient(p.trail[0].x, p.trail[0].y, px, py)
      grad.addColorStop(0, 'rgba(16, 185, 129, 0)')
      grad.addColorStop(1, 'rgba(16, 185, 129, 0.95)')

      ctx.beginPath()
      ctx.moveTo(p.trail[0].x, p.trail[0].y)
      for (let j = 1; j < p.trail.length; j++) {
        ctx.lineTo(p.trail[j].x, p.trail[j].y)
      }
      ctx.strokeStyle = grad
      ctx.lineWidth = p.lineWidth
      ctx.lineCap = 'round'
      ctx.stroke()
    }

    // 绘制流星头部微光晕与白亮核
    ctx.beginPath()
    ctx.arc(px, py, 2.2, 0, Math.PI * 2)
    ctx.fillStyle = 'rgba(16, 185, 129, 0.45)'
    ctx.fill()

    ctx.beginPath()
    ctx.arc(px, py, 1.25, 0, Math.PI * 2)
    ctx.fillStyle = '#ffffff'
    ctx.fill()
  }

  // 2. 绘制终点服务器波纹扩散（翠绿色系）
  for (let i = targetRipples.length - 1; i >= 0; i--) {
    const rip = targetRipples[i]
    rip.r += 0.22
    rip.alpha -= 0.035

    if (rip.alpha <= 0 || rip.r > 6.5) {
      targetRipples.splice(i, 1)
      continue
    }

    ctx.beginPath()
    ctx.arc(rip.x, rip.y, rip.r, 0, Math.PI * 2)
    ctx.strokeStyle = `rgba(16, 185, 129, ${rip.alpha})`
    ctx.lineWidth = 0.85
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
