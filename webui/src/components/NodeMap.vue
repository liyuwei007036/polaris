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
      <span class="tnum head-stat">
        {{ mappedCount }} 个节点 · {{ places.length }} 个地区
      </span>
    </div>

    <div class="map-sub">
      <span class="tnum status-count">
        在线 <span class="text-ok font-semibold">{{ onlineCount }}</span>
        <template v-if="offlineCount > 0">
          · 离线 <span class="text-danger font-semibold">{{ offlineCount }}</span>
        </template>
      </span>
      <div class="legend-wrap">
        <span v-for="item in LEGEND" :key="item.tone" class="legend-item" :class="`is-${item.tone}`">
          <span class="legend-dot" :class="item.dotClass" />
          <span :class="item.tone === 'all' ? 'text-ok font-medium' : ''">{{ item.label }}</span>
        </span>
      </div>
    </div>

    <div class="map-canvas-wrap">
      <svg
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

        <!-- Place Pins / Highlights -->
        <g v-for="p in places" :key="p.code" class="place-pin-group">
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
            @click.stop="toggleSelect(p.code)"
          >
            <title>{{ p.label }}: {{ p.online }}/{{ p.nodes.length }} 在线</title>
          </circle>
        </g>
      </svg>

      <!-- Region Popup on marker click -->
      <transition name="pop-fade">
        <div v-if="activePlace" class="place-popup neu" @click.stop>
          <div class="popup-head">
            <span class="popup-flag">{{ activePlace.flag }}</span>
            <span class="popup-title">{{ activePlace.label }}</span>
            <span class="popup-stat">{{ activePlace.online }}/{{ activePlace.nodes.length }} 在线</span>
            <button class="popup-close" @click="selectedCode = null">&times;</button>
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
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { LAND_POSITIONS, MAP_H, MAP_W, placeOf } from '../geo'

const props = defineProps({
  nodes: {
    type: Array,
    default: () => [],
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

const selectedCode = ref(null)

function countryCodeToFlag(code) {
  if (!code || code.length !== 2) return '🌐'
  const offset = 127397
  return String.fromCodePoint(...[...code.toUpperCase()].map((c) => c.charCodeAt(0) + offset))
}

const places = computed(() => {
  const map = new Map()
  for (const node of props.nodes) {
    let code = (node.country || node.country_code || '').trim().toUpperCase()
    if (!code || !placeOf(code)) {
      const name = (node.name || '').toUpperCase()
      if (name.includes('DE') || name.includes('德国') || name.includes('法兰克福')) code = 'DE'
      else if (name.includes('UK') || name.includes('GB') || name.includes('英国') || name.includes('伦敦')) code = 'GB'
      else if (name.includes('JP') || name.includes('日本') || name.includes('东京') || name.includes('大阪')) code = 'JP'
      else if (name.includes('HK') || name.includes('香港')) code = 'HK'
      else if (name.includes('US') || name.includes('美国') || name.includes('纽约') || name.includes('洛杉矶')) code = 'US'
      else if (name.includes('SG') || name.includes('新加坡')) code = 'SG'
    }
    const point = code ? placeOf(code) : null
    if (!point) continue
    let place = map.get(code)
    if (!place) {
      let regionName = node.city
      if (!regionName) {
        try {
          const dn = new Intl.DisplayNames(['zh-CN'], { type: 'region' })
          regionName = dn.of(code) || code
        } catch {
          regionName = code
        }
      }
      place = {
        code,
        label: regionName,
        flag: countryCodeToFlag(code),
        x: point[0],
        y: point[1],
        nodes: [],
        online: 0,
      }
      map.set(code, place)
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
  if (!selectedCode.value) return null
  return places.value.find((p) => p.code === selectedCode.value) || null
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

function toggleSelect(code) {
  selectedCode.value = selectedCode.value === code ? null : code
}

function handleNodeClick(nodeId) {
  emit('open', nodeId)
  selectedCode.value = null
}

function onGlobalClick(e) {
  if (!e.target.closest('.place-pin-group') && !e.target.closest('.place-popup')) {
    selectedCode.value = null
  }
}

onMounted(() => {
  window.addEventListener('click', onGlobalClick)
})

onUnmounted(() => {
  window.removeEventListener('click', onGlobalClick)
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
  width: 100%;
  height: auto;
  max-height: 180px;
}

.land-dots circle {
  fill: var(--sb-muted);
  opacity: 0.3;
}

.pin-halo {
  animation: pulse-ring 2.4s ease-out infinite;
  opacity: 0.3;
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
