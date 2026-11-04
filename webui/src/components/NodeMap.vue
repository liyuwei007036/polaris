<template>
  <div ref="cardRef" class="node-map-card neu" :class="[className, { 'is-fullscreen': isFullscreen }]">
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
        <span class="tnum head-stat" title="事件驱动：只有新建立的连接才会发射一颗流星，已存在的长连接不会重复飞线">
          {{ mappedCount }} 个节点 · {{ places.length }} 个地区 · {{ liveConnectionCount }} 条实时连接
        </span>
      </div>
    </div>

    <div class="map-sub">
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

      <!-- 全屏切换 -->
      <button
        class="map-fullscreen-btn"
        :title="isFullscreen ? '退出全屏' : '全屏'"
        :aria-label="isFullscreen ? '退出全屏' : '全屏'"
        @click.stop="toggleFullscreen"
      >
        <svg v-if="!isFullscreen" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M8 3H5a2 2 0 0 0-2 2v3" />
          <path d="M21 8V5a2 2 0 0 0-2-2h-3" />
          <path d="M3 16v3a2 2 0 0 0 2 2h3" />
          <path d="M16 21h3a2 2 0 0 0 2-2v-3" />
        </svg>
        <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <path d="M8 3v3a2 2 0 0 1-2 2H3" />
          <path d="M21 8h-3a2 2 0 0 1-2-2V3" />
          <path d="M3 16h3a2 2 0 0 1 2 2v3" />
          <path d="M16 21v-3a2 2 0 0 1 2-2h3" />
        </svg>
      </button>

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

const cardRef = ref(null)
const wrapRef = ref(null)
const svgRef = ref(null)
const canvasRef = ref(null)
const selectedKey = ref(null)
const isFullscreen = ref(false)

let unsubConnections = null
let animationFrameId = null
let resizeObserver = null
let isVisible = true

// 终点波纹：流星抵达目标服务器时触发
const targetRipples = []

// 已登记过的连接：key -> 最后一次在快照中出现的时间。
// 用 node_id + connection.id 组合，避免不同节点之间的 ID 冲突。
const emittedConnections = new Map()
// 连接从快照消失后仍保留一段时间，防止两次推送之间的抖动导致重复发射。
const EMITTED_RETENTION_MS = 60_000
// 每个节点的首轮快照只登记已有连接、不发射，避免打开页面或新增节点时爆出流星。
const seededNodes = new Set()
// 数据同步节流：SSE 约每秒推送一次，没必要每帧重新解析整张连接表
const CONNECTION_SYNC_INTERVAL_MS = 300
let lastConnectionSyncAt = 0

// 在途流星：每条“新连接”一颗，飞完即删
const meteors = []
const liveConnectionCount = ref(0)

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

// 全屏切换：整张地图卡片进入全屏，SVG 与 Canvas 由渲染循环自动适配新尺寸
function onFullscreenChange() {
  isFullscreen.value = Boolean(
    document.fullscreenElement === cardRef.value ||
    document.webkitFullscreenElement === cardRef.value
  )
}

async function toggleFullscreen() {
  const card = cardRef.value
  if (!card) return

  const active = document.fullscreenElement || document.webkitFullscreenElement
  try {
    if (active) {
      if (document.exitFullscreen) await document.exitFullscreen()
      else if (document.webkitExitFullscreen) document.webkitExitFullscreen()
    } else if (card.requestFullscreen) {
      await card.requestFullscreen()
    } else if (card.webkitRequestFullscreen) {
      card.webkitRequestFullscreen()
    }
  } catch {
    // 浏览器拒绝全屏（如 iframe 权限不足）时保持原状
  }
}

// ─────────────────────────────────────────────────────────────
// 事件驱动飞线 (Event-Driven Connection Arcs)
//
// 只在一条真实连接“第一次出现”时发射一颗流星，流星飞完即销毁。
// 同一个 connection.id 只发射一次；长连接建立后不会持续刷屏。
//   · 新连接出现   →  发射一颗流星
//   · 连接保持打开 →  不再重复发射
//   · 连接关闭     →  流星早已飞完，画布自然静默
// 首轮快照只登记已有连接、不发射，避免打开页面时一次爆出上百颗流星。
// 链式中继段 (is_chain_relay) 是服务器之间的内部跳转，不是用户连接，
// 与页面上的“活动连接”计数口径保持一致，予以排除。
// ─────────────────────────────────────────────────────────────

// 连接的唯一标识：node_id + connection.id。
// 极少数旧版 agent 可能没有 id，用连接五元组兜底；仍然拿不到就放弃，
// 宁可少画一颗，也不凭空重复发射。
function connectionKey(nodeID, c) {
  if (c.id) return `${nodeID}:${c.id}`
  const fingerprint = [c.source_ip, c.source, c.destination, c.network, c.started_at]
    .filter(Boolean)
    .join('|')
  return fingerprint ? `${nodeID}:${fingerprint}` : ''
}

// 扫描最新快照，返回本轮新出现的、且能定位画线的连接。
function collectNewConnections(now) {
  const rawSnapshots = props.live && props.live.size > 0 ? props.live : connectionSnapshots.value
  if (!rawSnapshots || rawSnapshots.size === 0) return { fresh: [], connectionCount: 0 }

  const nodeMap = new Map((props.nodes || []).map((n) => [n.id, n]))
  // 集群节点 IP 映射表（用于解析节点之间的中继或互联流量来源）
  const fleetByIP = new Map()
  for (const n of props.nodes || []) {
    if (n.ip) fleetByIP.set(n.ip, n)
    if (n.last_observed_ip) fleetByIP.set(n.last_observed_ip, n)
    if (n.client_address) fleetByIP.set(n.client_address.split(':')[0], n)
  }

  const present = new Set()
  const fresh = []
  let connectionCount = 0

  for (const snapshot of rawSnapshots.values()) {
    const node = nodeMap.get(snapshot.node_id)
    if (!node) continue

    // 该节点是否是第一次出现在快照里：首轮只登记，不发射
    const nodeIsNew = !seededNodes.has(snapshot.node_id)

    const nLat = Number(node.latitude)
    const nLon = Number(node.longitude)
    const hasTarget = !(isNaN(nLat) || isNaN(nLon) || (nLat === 0 && nLon === 0))
    const targetXY = hasTarget ? project(nLat, nLon) : null

    for (const c of snapshot.connections || []) {
      if (c.is_chain_relay) continue
      // 真实连接数按页面“活动连接”的口径统计：非中继段全部计入，
      // 即使某条连接暂时没有来源经纬度、画不出流星，也仍然是一条真实连接。
      connectionCount++

      const key = connectionKey(snapshot.node_id, c)
      if (!key) continue

      present.add(key)
      const alreadyKnown = emittedConnections.has(key)
      emittedConnections.set(key, now)
      if (alreadyKnown || nodeIsNew) continue

      // 新连接：节点或来源没有经纬度就画不出流星，只登记不发射
      if (!hasTarget) continue

      let sLat = Number(c.source_lat)
      let sLon = Number(c.source_lon)
      let hasSrc = Boolean(c.has_source_coords || (!isNaN(sLat) && !isNaN(sLon) && (sLat !== 0 || sLon !== 0)))

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

      fresh.push({ sourceXY, targetXY })
    }

    // 该节点本轮已登记完毕，后续快照里再出现的连接才算“新连接”
    seededNodes.add(snapshot.node_id)
  }

  // 清理已经离开快照很久的登记，防止 Map 无限增长
  for (const [key, at] of emittedConnections) {
    if (!present.has(key) && now - at > EMITTED_RETENTION_MS) emittedConnections.delete(key)
  }

  return { fresh, connectionCount }
}

// 二次贝塞尔曲线参数，保证与旧版弧线形状一致
function arcGeometry(arc) {
  const [x1, y1] = arc.sourceXY
  const [x2, y2] = arc.targetXY
  const dist = Math.hypot(x2 - x1, y2 - y1)
  const arcHeight = Math.min(38, Math.max(10, dist * 0.26))
  return { x1, y1, x2, y2, cx: (x1 + x2) / 2, cy: Math.min(y1, y2) - arcHeight }
}

function bezierPoint(g, t) {
  const it = 1 - t
  return [
    it * it * g.x1 + 2 * it * t * g.cx + t * t * g.x2,
    it * it * g.y1 + 2 * it * t * g.cy + t * t * g.y2,
  ]
}

// 沿真实连接弧线绘制一颗流星（翠绿拖尾 + 白亮核）
function drawMeteor(ctx, g, t) {
  const TRAIL_STEPS = 10
  const STEP = 0.012
  const pts = []
  for (let i = TRAIL_STEPS; i >= 0; i--) {
    const tt = t - i * STEP
    if (tt < 0) continue
    pts.push(bezierPoint(g, tt))
  }

  if (pts.length > 1) {
    const tail = pts[0]
    const head = pts[pts.length - 1]
    const grad = ctx.createLinearGradient(tail[0], tail[1], head[0], head[1])
    grad.addColorStop(0, 'rgba(16, 185, 129, 0)')
    grad.addColorStop(1, 'rgba(16, 185, 129, 0.9)')

    ctx.beginPath()
    ctx.moveTo(tail[0], tail[1])
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i][0], pts[i][1])
    ctx.strokeStyle = grad
    ctx.lineWidth = 1.5
    ctx.lineCap = 'round'
    ctx.stroke()
  }

  const [hx, hy] = bezierPoint(g, t)
  ctx.beginPath()
  ctx.arc(hx, hy, 2.1, 0, Math.PI * 2)
  ctx.fillStyle = 'rgba(16, 185, 129, 0.45)'
  ctx.fill()

  ctx.beginPath()
  ctx.arc(hx, hy, 1.2, 0, Math.PI * 2)
  ctx.fillStyle = '#ffffff'
  ctx.fill()
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

  // 节流扫描真实连接：每 300ms 一次，足够跟上约 1Hz 的 SSE 推送
  if (now - lastConnectionSyncAt >= CONNECTION_SYNC_INTERVAL_MS) {
    const { fresh, connectionCount } = collectNewConnections(now)
    for (const conn of fresh) {
      meteors.push({
        sourceXY: conn.sourceXY,
        targetXY: conn.targetXY,
        // 同一批新连接稍微错开出发，避免完全重叠成一条粗线
        bornAt: now + Math.random() * 300,
        duration: 1400,
      })
    }
    if (liveConnectionCount.value !== connectionCount) liveConnectionCount.value = connectionCount
    lastConnectionSyncAt = now
  }

  // 1. 绘制在途流星：每颗流星对应一条件“刚刚建立”的真实连接，飞完即销毁
  for (let i = meteors.length - 1; i >= 0; i--) {
    const m = meteors[i]
    const t = (now - m.bornAt) / m.duration
    if (t < 0) continue
    if (t >= 1) {
      // 抵达目标服务器节点，触发翠绿能量吸收波纹
      targetRipples.push({ x: m.targetXY[0], y: m.targetXY[1], r: 1.5, alpha: 0.85 })
      meteors.splice(i, 1)
      continue
    }
    drawMeteor(ctx, arcGeometry(m), t)
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
  document.addEventListener('fullscreenchange', onFullscreenChange)
  document.addEventListener('webkitfullscreenchange', onFullscreenChange)

  // 自动订阅全局实时连接 SSE 流
  unsubConnections = subscribeConnections(() => {})

  // 启动 Canvas 60 FPS 渲染循环
  animationFrameId = requestAnimationFrame(renderFrame)
})

onUnmounted(() => {
  window.removeEventListener('click', onGlobalClick)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
  document.removeEventListener('fullscreenchange', onFullscreenChange)
  document.removeEventListener('webkitfullscreenchange', onFullscreenChange)

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
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  font-size: 11px;
}

.text-ok { color: var(--sb-success, #10b981); }

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

.map-fullscreen-btn {
  position: absolute;
  right: 8px;
  bottom: 8px;
  z-index: 6;
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  padding: 0;
  border: 1px solid var(--sb-line);
  border-radius: 8px;
  background: color-mix(in oklab, var(--sb-surface-2, #1b1e26) 78%, transparent);
  color: var(--sb-muted);
  cursor: pointer;
  backdrop-filter: blur(6px);
  transition: color 0.15s ease, background-color 0.15s ease, border-color 0.15s ease;
}

.map-fullscreen-btn:hover {
  color: var(--sb-success, #10b981);
  border-color: color-mix(in oklab, var(--sb-success, #10b981) 45%, transparent);
  background: color-mix(in oklab, var(--sb-success, #10b981) 14%, transparent);
}

.map-fullscreen-btn svg {
  width: 14px;
  height: 14px;
}

/* 全屏时让地图铺满整个视口 */
.node-map-card:fullscreen,
.node-map-card.is-fullscreen {
  width: 100vw;
  height: 100vh;
  max-width: none;
  max-height: none;
  padding: 22px 26px;
  border-radius: 0;
  background: var(--sb-bg, #0e1015);
}

.node-map-card:fullscreen .map-canvas-wrap,
.node-map-card.is-fullscreen .map-canvas-wrap {
  flex: 1;
  min-height: 0;
}

.node-map-card:fullscreen .map-svg,
.node-map-card.is-fullscreen .map-svg {
  width: 100%;
  height: 100%;
  max-height: none;
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
