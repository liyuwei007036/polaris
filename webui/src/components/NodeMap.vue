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
        <span class="head-title">服务器分布</span>
      </div>
      <div class="head-right">
        <span
          class="tnum head-stat"
          :title="unmappedCount > 0
            ? `已定位 ${mappedCount} / ${nodes.length} 台服务器，另有 ${unmappedCount} 台服务器缺少经纬度，暂未显示在地图上。事件驱动：只有新建立的连接才会发射一颗流星。`
            : '事件驱动：只有新建立的连接才会发射一颗流星，已存在的长连接不会重复飞线'"
        >
          {{ nodes.length }} 台服务器 · {{ places.length }} 个地区 · {{ liveConnectionCount }} 条实时连接<template v-if="unmappedCount > 0">（{{ unmappedCount }} 台未定位）</template>
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
        :class="{ 'is-pin-hover': hoveredKey }"
        @mousemove="onMapMove"
        @mouseleave="onMapLeave"
        @click="onMapClick"
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
        <g
          v-for="p in places"
          :key="p.key"
          class="place-pin-group"
          :class="[p.toneClass, { 'is-active': selectedKey === p.key, 'is-hover': hoveredKey === p.key }]"
          role="button"
          tabindex="0"
          :aria-label="`${p.label}，${p.online}/${p.nodes.length} 台在线`"
          @keydown.enter.prevent="toggleSelect(p.key, $event)"
          @keydown.space.prevent="toggleSelect(p.key, $event)"
        >
          <title>{{ pinTitle(p) }}</title>
          <!-- 防重叠展开后的引线：从真实坐标指向显示位置 -->
          <line
            v-if="p.spread"
            :x1="p.trueX"
            :y1="p.trueY"
            :x2="p.x"
            :y2="p.y"
            class="pin-leader"
          />
          <!-- Pulse halo -->
          <circle
            :cx="p.x"
            :cy="p.y"
            r="4.2"
            class="pin-halo"
            :class="p.toneClass"
          />
          <!-- 位置近似标记：只有国家级定位，没有精确经纬度 -->
          <circle
            v-if="p.approximate"
            :cx="p.x"
            :cy="p.y"
            :r="pinRadius(p) + 1.6"
            class="pin-approx-ring"
          />
          <!-- 选中环 -->
          <circle
            v-if="selectedKey === p.key"
            :cx="p.x"
            :cy="p.y"
            :r="pinRadius(p) + 2.4"
            class="pin-select-ring"
            :class="p.toneClass"
          />
          <!-- Center dot：同一地点 VPS 越多，圆点略大，便于识别聚合规模 -->
          <circle
            :cx="p.x"
            :cy="p.y"
            :r="pinRadius(p)"
            class="pin-core"
            :class="p.toneClass"
          />
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

      <!-- Region Popup on marker click：跟随点击位置显示，而不是固定在角落 -->
      <Teleport :to="popupTarget">
        <transition name="pop-fade">
          <div
            v-if="activePlace"
            ref="popupRef"
            class="place-popup neu"
            :style="{ left: `${popupPos.x}px`, top: `${popupPos.y}px` }"
            @click.stop
          >
            <div class="popup-head">
              <span class="popup-flag">{{ activePlace.flag }}</span>
              <span class="popup-title truncate">{{ activePlace.label }}</span>
              <span class="popup-stat">{{ activePlace.online }}/{{ activePlace.nodes.length }} 在线</span>
              <button class="popup-close" title="关闭" @click="selectedKey = null">&times;</button>
            </div>
            <div class="popup-sub tnum">
              {{ activePlace.nodes.length }} 台 VPS · {{ activePlace.approximate ? '位置近似（按国家中心显示）' : formatCoords(activePlace.lat, activePlace.lon) }}
            </div>
            <div class="popup-nodes-list">
              <button
                v-for="node in activePlace.nodes"
                :key="node.id"
                type="button"
                class="popup-node-item"
                :title="node.name"
                @click="handleNodeClick(node.id)"
              >
                <span class="popup-node-status" :class="node.online ? 'is-online' : 'is-offline'" />
                <span class="popup-node-main">
                  <span class="popup-node-name truncate">{{ node.name }}</span>
                  <span class="popup-node-meta truncate">{{ nodeMeta(node) }}</span>
                </span>
                <span class="popup-node-tag" :class="node.online ? 'is-online' : 'is-offline'">
                  {{ node.online ? '在线' : '离线' }}
                </span>
                <span class="popup-node-arrow">&rsaquo;</span>
              </button>
            </div>
          </div>
        </transition>
      </Teleport>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { countryCoords, LAND_POSITIONS, MAP_H, MAP_W, project } from '../geo'
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

const cardRef = ref(null)
const wrapRef = ref(null)
const svgRef = ref(null)
const canvasRef = ref(null)
const selectedKey = ref(null)
const hoveredKey = ref(null)
const popupRef = ref(null)
const popupPos = ref({ x: 0, y: 0 })
const isFullscreen = ref(false)
// 全屏时 popup 必须挂在全屏元素内部，否则浏览器不会渲染 body 下的兄弟节点。
const popupTarget = computed(() => (isFullscreen.value && cardRef.value ? cardRef.value : 'body'))

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

// 把同一座城市/地区的 VPS 聚合到一起。
// 之前按经纬度保留 3 位小数做键，同一城市的服务器只要定位结果有几米差异，
// 就会变成多个互相重叠的小点，点开只看到其中 1 台。
function localityLabel(node) {
  const city = String(node.city || '').trim()
  if (city && city !== '未知' && city !== '本机' && city !== '内网') return city

  const location = String(node.location || '').trim()
  if (location && location !== '未知') {
    const parts = location.split(/\s+/).filter(Boolean)
    if (parts.length) return parts[parts.length - 1]
  }
  return ''
}

function regionLabel(node) {
  const code = String(node.country_code || '').trim().toUpperCase()
  if (/^[A-Z]{2}$/.test(code)) {
    try {
      const dn = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
      const name = dn.of(code)
      if (name) return name
    } catch {
      // 浏览器不支持 Intl.DisplayNames 时退回国家代码
    }
  }
  return String(node.country || code || '未知区域').trim()
}

function toneOf(place) {
  if (place.online === 0) return 'tone-none'
  if (place.online < place.nodes.length) return 'tone-some'
  return 'tone-all'
}

// 视图坐标系中的合并距离：地图总宽只有 360 单位，小于 2 单位的两座城市
// 在屏幕上几乎重合，合并成一个点并列出全部 VPS，避免“藏在下面点不到”。
const PLACE_MERGE_DISTANCE = 2
// 两个标记在视图坐标系里的最小显示间距，避免洛杉矶/圣何塞这种相邻城市叠成一个点。
const PLACE_MIN_DISTANCE = 9

// 纯数据驱动：同一城市/地区的 VPS 聚合为一个标记，节点全部保留在弹窗里。
const places = computed(() => {
  const map = new Map()
  const unlocated = []

  for (const node of props.nodes) {
    const lat = Number(node.latitude)
    const lon = Number(node.longitude)
    const hasValidCoords = Boolean(node.has_coords || (!isNaN(lat) && !isNaN(lon) && (lat !== 0 || lon !== 0)))
    if (!hasValidCoords) {
      unlocated.push(node)
      continue
    }

    const locality = localityLabel(node)
    const country = String(node.country_code || node.country || '').trim().toUpperCase()
    const key = locality ? `${country}|${locality.toLowerCase()}` : `${lat.toFixed(3)},${lon.toFixed(3)}`

    let place = map.get(key)
    if (!place) {
      place = {
        key,
        code: key,
        label: locality || regionLabel(node),
        flag: countryCodeToFlag(node.country_code),
        latSum: 0,
        lonSum: 0,
        coordCount: 0,
        nodes: [],
        online: 0,
      }
      map.set(key, place)
    }
    place.nodes.push(node)
    place.latSum += lat
    place.lonSum += lon
    place.coordCount++
    if (node.online) place.online++
  }

  const normalized = [...map.values()].map((place) => {
    const lat = place.latSum / place.coordCount
    const lon = place.lonSum / place.coordCount
    const [x, y] = project(lat, lon)
    return {
      ...place,
      lat,
      lon,
      x,
      y,
      toneClass: toneOf(place),
    }
  })

  // 没有经纬度的节点：先尝试并入同城市的精确标记；城市也不确定时退到国家中心点，
  // 保证 VPS 不会从地图上凭空消失，同时用“位置近似”明确标注。
  const approximate = new Map()
  for (const node of unlocated) {
    const locality = localityLabel(node)
    const country = String(node.country_code || node.country || '').trim().toUpperCase()
    const cityKey = locality ? `${country}|${locality.toLowerCase()}` : ''
    const host = cityKey ? normalized.find((place) => place.key === cityKey) : null
    if (host) {
      host.nodes.push(node)
      if (node.online) host.online++
      host.toneClass = toneOf(host)
      continue
    }

    const coords = countryCoords(node.country_code)
    if (!coords) continue

    const key = `~${country || 'unknown'}`
    let place = approximate.get(key)
    if (!place) {
      const [x, y] = project(coords.lat, coords.lon)
      place = {
        key,
        code: key,
        label: `${regionLabel(node)}（位置近似）`,
        flag: countryCodeToFlag(node.country_code),
        lat: coords.lat,
        lon: coords.lon,
        x,
        y,
        nodes: [],
        online: 0,
        approximate: true,
      }
      approximate.set(key, place)
    }
    place.nodes.push(node)
    if (node.online) place.online++
  }
  for (const place of approximate.values()) {
    place.toneClass = toneOf(place)
  }

  // 不同城市如果定位坐标几乎重合，也合并成一个标记，保证点开后能看到全部节点。
  const merged = []
  for (const place of [...normalized, ...approximate.values()]) {
    const host = merged.find((item) => Math.hypot(item.x - place.x, item.y - place.y) < PLACE_MERGE_DISTANCE)
    if (!host) {
      merged.push({ ...place })
      continue
    }
    const hostCount = host.nodes.length
    const total = hostCount + place.nodes.length
    host.lat = (host.lat * hostCount + place.lat * place.nodes.length) / total
    host.lon = (host.lon * hostCount + place.lon * place.nodes.length) / total
    ;[host.x, host.y] = project(host.lat, host.lon)
    host.nodes.push(...place.nodes)
    host.online += place.online
    const labels = host.label.split(' / ')
    if (!labels.includes(place.label)) labels.push(place.label)
    host.label = labels.join(' / ')
    if (host.flag !== place.flag) host.flag = '🌐'
    host.toneClass = toneOf(host)
  }

  // 同一区域的两个城市（例如洛杉矶和圣何塞）即使各自只剩一个标记，
  // 在总览地图上也可能只差几个像素。这里做一次轻量的“防重叠展开”：
  // 显示位置至少间隔 9 个视图单位，并保留真实坐标用于画回引线和弹窗展示。
  const laidOut = merged
    .slice()
    .sort((a, b) => a.key.localeCompare(b.key))
    .map((place) => ({ ...place, trueX: place.x, trueY: place.y }))

  for (let iteration = 0; iteration < 24; iteration++) {
    let moved = false
    for (let i = 0; i < laidOut.length; i++) {
      for (let j = i + 1; j < laidOut.length; j++) {
        const a = laidOut[i]
        const b = laidOut[j]
        let dx = b.x - a.x
        let dy = b.y - a.y
        let distance = Math.hypot(dx, dy)
        if (distance >= PLACE_MIN_DISTANCE) continue
        if (distance < 0.001) {
          dx = 1
          dy = 0
          distance = 1
        }
        const ux = dx / distance
        const uy = dy / distance
        const push = (PLACE_MIN_DISTANCE - distance) / 2 + 0.01
        a.x -= ux * push
        a.y -= uy * push
        b.x += ux * push
        b.y += uy * push
        moved = true
      }
    }
    if (!moved) break
  }

  for (const place of laidOut) {
    place.x = Math.max(5, Math.min(MAP_W - 5, place.x))
    place.y = Math.max(4, Math.min(MAP_H - 4, place.y))
    place.spread = Math.hypot(place.x - place.trueX, place.y - place.trueY) > 0.2
  }

  return laidOut
})

const activePlace = computed(() => {
  if (!selectedKey.value) return null
  return places.value.find((p) => p.key === selectedKey.value) || null
})

const mappedCount = computed(() => {
  return places.value.reduce((sum, p) => sum + p.nodes.length, 0)
})

// 缺少经纬度、无法落到地图上的节点，避免统计口径让人误以为地图漏画了 VPS
const unmappedCount = computed(() => Math.max(0, props.nodes.length - mappedCount.value))

function pinTitle(place) {
  const names = place.nodes.map((n) => n.name).filter(Boolean)
  const shown = names.length ? names.slice(0, 3).join('、') : '未命名节点'
  const rest = names.length > 3 ? ` 等 ${names.length} 台` : ''
  return `${place.label} · ${shown}${rest}（${place.online}/${place.nodes.length} 在线）`
}

function nodeMeta(node) {
  const location = String(node.city || node.location || '').trim()
  const address = String(node.last_observed_ip || node.client_address || '').trim()
  return [location, address].filter(Boolean).join(' · ') || '位置未知'
}

function formatCoords(lat, lon) {
  const la = Number(lat)
  const lo = Number(lon)
  if (!Number.isFinite(la) || !Number.isFinite(lo)) return ''
  const ns = la >= 0 ? 'N' : 'S'
  const ew = lo >= 0 ? 'E' : 'W'
  return `${Math.abs(la).toFixed(2)}°${ns}, ${Math.abs(lo).toFixed(2)}°${ew}`
}

function pinRadius(place) {
  return 2.2 + Math.min(1.2, Math.max(0, place.nodes.length - 1) * 0.35)
}

function toggleSelect(key, event) {
  if (selectedKey.value === key) {
    selectedKey.value = null
    return
  }
  const place = places.value.find((p) => p.key === key)
  if (place) openPlacePopup(place, event)
}

// 弹窗跟随点击位置：点击地图上的任意标记，弹窗都出现在鼠标附近，
// 靠近视口边缘时自动翻到另一侧，避免被裁掉。
async function openPlacePopup(place, event) {
  let clientX = Number(event?.clientX)
  let clientY = Number(event?.clientY)
  if (!Number.isFinite(clientX) || !Number.isFinite(clientY)) {
    const svg = svgRef.value
    const ctm = svg?.getScreenCTM()
    if (svg && ctm) {
      const point = svg.createSVGPoint()
      point.x = place.x
      point.y = place.y
      const screen = point.matrixTransform(ctm)
      clientX = screen.x
      clientY = screen.y
    }
  }
  if (!Number.isFinite(clientX) || !Number.isFinite(clientY)) {
    const rect = wrapRef.value?.getBoundingClientRect()
    clientX = rect ? rect.left + rect.width / 2 : window.innerWidth / 2
    clientY = rect ? rect.top + rect.height / 2 : window.innerHeight / 2
  }
  selectedKey.value = place.key
  popupPos.value = { x: clientX + 12, y: clientY + 12 }
  await nextTick()
  fitPopupToViewport(clientX, clientY)
}

function fitPopupToViewport(clientX, clientY) {
  const el = popupRef.value
  const width = el?.offsetWidth || 260
  const height = el?.offsetHeight || 200
  const gap = 12
  const margin = 10
  let x = clientX + gap
  let y = clientY + gap
  if (x + width > window.innerWidth - margin) x = clientX - width - gap
  if (y + height > window.innerHeight - margin) y = clientY - height - gap
  x = Math.max(margin, Math.min(x, window.innerWidth - width - margin))
  y = Math.max(margin, Math.min(y, window.innerHeight - height - margin))
  popupPos.value = { x, y }
}

// 地图上两个城市可能只差几个像素（例如洛杉矶和圣何塞）。
// 不用固定半径的透明热区互相遮挡，而是按鼠标到各标记中心的实际距离取最近的一个，
// 这样挨得很近的 VPS 也都能各自选中。
function mapPointFromEvent(event) {
  const svg = svgRef.value
  if (!svg || typeof event.clientX !== 'number') return null
  const ctm = svg.getScreenCTM()
  if (!ctm) return null
  const point = svg.createSVGPoint()
  point.x = event.clientX
  point.y = event.clientY
  const local = point.matrixTransform(ctm.inverse())
  return [local.x, local.y]
}

function nearestPlaceAt(event, maxScreenPx) {
  const point = mapPointFromEvent(event)
  const svg = svgRef.value
  if (!point || !svg) return null

  const rect = svg.getBoundingClientRect()
  const scale = Math.min(rect.width / MAP_W, rect.height / MAP_H) || 1
  const maxDistance = maxScreenPx / scale

  let nearest = null
  let nearestDistance = Infinity
  for (const place of places.value) {
    const distance = Math.hypot(place.x - point[0], place.y - point[1])
    if (distance < nearestDistance) {
      nearestDistance = distance
      nearest = place
    }
  }
  return nearest && nearestDistance <= maxDistance ? nearest : null
}

function onMapMove(event) {
  const place = nearestPlaceAt(event, 16)
  const key = place ? place.key : null
  if (hoveredKey.value !== key) hoveredKey.value = key
}

function onMapLeave() {
  hoveredKey.value = null
}

function onMapClick(event) {
  const place = nearestPlaceAt(event, 20)
  if (!place) return
  // 阻止全局 click 把刚刚选中的弹窗立刻关掉
  event.stopPropagation()
  toggleSelect(place.key, event)
}

function handleNodeClick(nodeId) {
  emit('open', nodeId)
  selectedKey.value = null
}

function onGlobalClick(e) {
  const target = e.target
  if (!(target instanceof Element)) return
  if (!target.closest('.place-pin-group') && !target.closest('.place-popup')) {
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
  // 标记的实际显示坐标（含防止相邻城市重叠的轻微展开），
  // 让流星终点落在用户看到的圆点上，而不是原始经纬度对应的位置。
  const displayByNode = new Map()
  for (const place of places.value) {
    for (const n of place.nodes) displayByNode.set(n.id, [place.x, place.y])
  }
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
    const targetXY = hasTarget ? (displayByNode.get(node.id) || project(nLat, nLon)) : null

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
      let sourceXY = null

      if (!hasSrc && c.source_ip) {
        const fromNode = fleetByIP.get(c.source_ip)
        if (fromNode) {
          sourceXY = displayByNode.get(fromNode.id) || null
          if (!sourceXY && fromNode.has_coords) {
            sLat = Number(fromNode.latitude)
            sLon = Number(fromNode.longitude)
            hasSrc = !isNaN(sLat) && !isNaN(sLon) && (sLat !== 0 || sLon !== 0)
          }
        }
      }
      if (!sourceXY) {
        if (!hasSrc) continue
        sourceXY = project(sLat, sLon)
      }
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

.place-pin-group {
  cursor: pointer;
  outline: none;
}

.map-svg.is-pin-hover {
  cursor: pointer;
}

.pin-leader {
  stroke: var(--sb-muted, #8b93a7);
  stroke-width: 0.45;
  opacity: 0.45;
  pointer-events: none;
}

.pin-halo {
  animation: pulse-ring 2.4s ease-out infinite;
}

.pin-halo.tone-all { fill: var(--sb-success, #10b981); }
.pin-halo.tone-some { fill: var(--sb-warning, #f59e0b); }
.pin-halo.tone-none { fill: var(--sb-danger, #ef4444); }

.pin-core {
  transform-box: fill-box;
  transform-origin: center;
  transition: transform 0.15s ease;
  /* 用页面底色描边，两个靠得很近的城市不会糊成一团 */
  stroke: var(--sb-bg, #0e1015);
  stroke-width: 0.7;
  pointer-events: none;
}

/*
 * 关键修复：SVG 元素默认以 viewBox 原点作为 transform-origin，
 * 直接 scale 会让圆点瞬移到别处，鼠标随之脱离 -> 再缩回，形成“跳来跳去”。
 * 这里用 fill-box + center 保证只以圆点自身中心放大。
 * 悬停/选中状态由固定的地图坐标判定，不依赖缩放后的元素边界。
 */
.place-pin-group.is-hover .pin-core,
.place-pin-group:focus-visible .pin-core,
.place-pin-group.is-active .pin-core {
  transform: scale(1.5);
}

.pin-approx-ring {
  fill: none;
  stroke: var(--sb-muted, #8b93a7);
  stroke-width: 0.6;
  stroke-dasharray: 1.2 1;
  opacity: 0.8;
  pointer-events: none;
}

.pin-core.tone-all { fill: var(--sb-success, #10b981); }
.pin-core.tone-some { fill: var(--sb-warning, #f59e0b); }
.pin-core.tone-none { fill: var(--sb-danger, #ef4444); }

.pin-select-ring {
  fill: none;
  stroke-width: 1.2;
  pointer-events: none;
}

.pin-select-ring.tone-all { stroke: var(--sb-success, #10b981); }
.pin-select-ring.tone-some { stroke: var(--sb-warning, #f59e0b); }
.pin-select-ring.tone-none { stroke: var(--sb-danger, #ef4444); }

@keyframes pulse-ring {
  0% { r: 2.5; opacity: 0.55; }
  100% { r: 4.6; opacity: 0; }
}

.place-popup {
  position: fixed;
  z-index: 2200;
  width: 248px;
  border-radius: 12px;
  padding: 10px 12px;
  box-shadow: var(--sh-neu);
  background: var(--sb-panel-solid, var(--sb-surface));
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

.popup-flag { font-size: 14px; flex-shrink: 0; }
.popup-title { min-width: 0; }
.popup-stat { font-size: 10px; color: var(--sb-muted); margin-left: auto; white-space: nowrap; }
.popup-close {
  flex-shrink: 0;
  padding: 0 2px;
  background: none;
  border: none;
  color: var(--sb-muted);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}

.popup-sub {
  margin-top: 6px;
  font-size: 10px;
  color: var(--sb-muted);
}

.popup-nodes-list {
  margin-top: 6px;
  max-height: 150px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.popup-node-item {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 5px 6px;
  border: none;
  border-radius: 6px;
  background: transparent;
  color: var(--sb-text);
  font: inherit;
  font-size: 11px;
  text-align: left;
  cursor: pointer;
  transition: background-color 0.15s ease;
}

.popup-node-item:hover {
  background: var(--neu-inset-bg);
}

.popup-node-status {
  flex-shrink: 0;
  width: 6px;
  height: 6px;
  border-radius: 50%;
}

.popup-node-status.is-online { background: var(--sb-success, #10b981); }
.popup-node-status.is-offline { background: var(--sb-danger, #ef4444); }

.popup-node-main {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}

.popup-node-name {
  min-width: 0;
}

.popup-node-meta {
  min-width: 0;
  font-size: 9px;
  color: var(--sb-muted);
}

.popup-node-tag {
  flex-shrink: 0;
  padding: 1px 5px;
  border-radius: 999px;
  font-size: 9px;
  line-height: 1.4;
}

.popup-node-tag.is-online {
  color: var(--sb-success, #10b981);
  background: color-mix(in oklab, var(--sb-success, #10b981) 14%, transparent);
}

.popup-node-tag.is-offline {
  color: var(--sb-danger, #ef4444);
  background: color-mix(in oklab, var(--sb-danger, #ef4444) 14%, transparent);
}

.popup-node-arrow {
  flex-shrink: 0;
  color: var(--sb-muted);
  font-size: 14px;
}

.pop-fade-enter-active, .pop-fade-leave-active { transition: opacity 0.15s ease, transform 0.15s ease; }
.pop-fade-enter-from, .pop-fade-leave-to { opacity: 0; transform: scale(0.95); }
</style>
