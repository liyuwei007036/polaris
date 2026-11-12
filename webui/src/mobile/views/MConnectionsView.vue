<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { formatBytes, formatDateTime, includesText } from '../../format'
import { connectionSnapshots, mergeChainedConnections, subscribeConnections } from '../../connections'
import MPage from '../components/MPage.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(true)
const selectedNode = ref('')
const selectedOutbound = ref('')
const keyword = ref('')
// 连接可能有上千条，全画出来手机会卡住；先给一页，看得完再要下一页。
const visible = ref(30)
let stopConnections

// sing-box 把目标地址和嗅探到的域名分开报，这里合成操作者会写的那一种：
// 有域名就用域名，永远带端口。
function splitAddress(address) {
  const text = String(address || '')
  const separator = text.lastIndexOf(':')
  if (separator < 0) return { host: text, port: '' }
  return { host: text.slice(0, separator).replace(/^\[|\]$/g, ''), port: text.slice(separator + 1) }
}

function targetLabel(connection) {
  const address = splitAddress(connection.destination)
  const host = connection.host || address.host
  if (!host) return address.port ? `:${address.port}` : '—'
  return address.port ? `${host}:${address.port}` : host
}

// 认一条连接靠的是用户别名；没有别名可显示时，才退回它进来的那个入站服务。
function entryLabel(connection) {
  return connection.user || connection.listener_name || '—'
}

// 卡上的脚注：从哪个入站节点进来、从哪个地址来的。服务器名不在这里 ——
// 一条连接落在哪台机器上是筛选用的条件，不是认出这条连接靠的东西。
function sourceLine(row) {
  return [row.entry !== '—' ? row.entry : '', row.source_ip || row.source, row.source_location]
    .filter(Boolean).join(' · ') || '未知来源'
}

function isDirect(exit) {
  if (!exit) return true
  const lower = String(exit).toLowerCase()
  return lower === 'direct' || lower === '直连'
}

const rows = computed(() => {
  const flat = [...connectionSnapshots.value.values()].flatMap((result) => {
    const node = appState.nodes.find((item) => item.id === result.node_id)
    return (result.connections || []).map((connection) => ({
      ...connection,
      node_id: result.node_id,
      node_name: node?.name || result.node_id,
      clock_offset_ms: result.clock_offset_ms || 0,
      // master 会把 sing-box 的出站标签解析成操作者配置的出口，解析不到才显示原始标签。
      exit: connection.outbound_name || connection.outbound || connection.chains?.[0] || 'DIRECT',
      entry: entryLabel(connection),
      target: targetLabel(connection),
    }))
  })
  return mergeChainedConnections(flat, appState.nodes)
})
const collectedAt = computed(() => [...connectionSnapshots.value.values()].map((result) => result.collected_at).filter(Boolean).sort().at(-1) || '')
const nodeOptions = computed(() => [{ value: '', label: '全部服务器' }, ...appState.nodes.map((node) => ({ value: node.id, label: node.name }))])
const outboundOptions = computed(() => [
  { value: '', label: '全部出口' },
  ...[...new Set(rows.value.map((row) => row.exit).filter(Boolean))].sort().map((exit) => ({ value: exit, label: exit })),
])
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value && !row.chain_node_ids?.includes(selectedNode.value)) return false
  if (selectedOutbound.value && row.exit !== selectedOutbound.value && !row.chain_exit?.includes(selectedOutbound.value) && !row.exit?.includes(selectedOutbound.value)) return false
  return includesText([
    row.node_name, row.source, row.source_ip, row.source_location, row.target,
    row.entry, row.user, row.listener_name, row.exit, row.network,
  ], keyword.value)
}))
const shown = computed(() => filteredRows.value.slice(0, visible.value))

watch([keyword, selectedNode, selectedOutbound], () => { visible.value = 30 })

// 卡上只放目标、上下行和来源；入站服务、开始时间这些点开这条再看。
const detailOpen = ref(false)
const detailTarget = ref(null)
const details = computed(() => {
  const row = detailTarget.value
  if (!row) return []
  return [
    { label: '目标', value: [row.target, row.destination_location].filter(Boolean).join(' · '), mono: true },
    { label: '服务器', value: row.node_name },
    { label: '入站节点', value: row.entry },
    { label: '出口', value: row.exit },
    { label: '来源', value: [row.source_ip || row.source, row.source_location].filter(Boolean).join(' · ') || '未知', mono: true },
    { label: '网络', value: (row.network || '').toUpperCase() || '—' },
    { label: '实时速率', value: row.has_rates ? `↓ ${formatBytes(row.download_rate, '/s')} · ↑ ${formatBytes(row.upload_rate, '/s')}` : '等待上报' },
    { label: '累计流量', value: `↓ ${formatBytes(row.download)} · ↑ ${formatBytes(row.upload)}` },
    { label: '开始于', value: formatDateTime(row.started_at) },
  ]
})

function openDetail(row) {
  detailTarget.value = row
  detailOpen.value = true
}

async function load() {
  loading.value = true
  try {
    await loadNodes()
  } finally {
    loading.value = false
  }
}

// 建流时服务端立刻回一帧快照，但那时 hub 通常还是空的：各节点的连接要再过
// 几百毫秒才到。等到真有数据、或等满兜底时间再收起加载态，否则会先闪一下
// 「当前没有活动连接」再出列表。
let settleTimer
function settle() {
  clearTimeout(settleTimer)
  loading.value = false
}

onMounted(async () => {
  try {
    await loadNodes()
  } finally {
    stopConnections = subscribeConnections(() => { if (rows.value.length) settle() })
    settleTimer = setTimeout(settle, 1500)
  }
})
onBeforeUnmount(() => {
  clearTimeout(settleTimer)
  stopConnections?.()
})
</script>

<template>
  <MPage title="当前连接" :loading="loading">
    <div class="m-listbar">
      <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索 IP、目标、入站节点或出口" />
      <div class="m-filters">
        <MPicker v-model="selectedNode" chip :options="nodeOptions" title="按服务器筛选" placeholder="全部服务器" />
        <MPicker v-model="selectedOutbound" chip :options="outboundOptions" title="按出口筛选" placeholder="全部出口" />
      </div>
    </div>
    <div class="m-count">{{ filteredRows.length }} 条 · 采集于 {{ formatDateTime(collectedAt, '暂无数据') }}</div>

    <article
      v-for="row in shown"
      :key="`${row.node_id}/${row.id}`"
      class="m-conn-card neu rise"
      role="button"
      tabindex="0"
      @click="openDetail(row)"
    >
      <!-- Top: Host & Badges -->
      <div class="m-conn-head">
        <div class="m-conn-target">
          <div class="m-conn-host font-mono" :title="row.target">{{ row.target }}</div>
          <div v-if="row.destination_location" class="m-conn-geo">
            <span class="geo-dot" />
            <span>{{ row.destination_location }}</span>
          </div>
        </div>
        <div class="m-conn-badges">
          <span class="m-conn-tag font-mono">{{ (row.network || 'TCP').toUpperCase() }}</span>
          <span class="m-conn-tag m-conn-tag--exit" :title="row.exit">{{ row.exit }}</span>
        </div>
      </div>

      <!-- Middle: Micro-neomorphic Inset Rates Grid -->
      <div class="m-conn-rates neu-inset">
        <div class="rate-col">
          <span class="rate-lbl">实时下行</span>
          <div class="rate-val font-mono">
            ↓ {{ row.has_rates ? formatBytes(row.download_rate, '/s') : '—' }}
          </div>
        </div>
        <div class="rate-divider" />
        <div class="rate-col">
          <span class="rate-lbl">实时上行</span>
          <div class="rate-val font-mono">
            ↑ {{ row.has_rates ? formatBytes(row.upload_rate, '/s') : '—' }}
          </div>
        </div>
        <div class="rate-divider" />
        <div class="rate-col">
          <span class="rate-lbl">累计传输</span>
          <div class="rate-val font-mono">
            {{ formatBytes((row.download || 0) + (row.upload || 0)) }}
          </div>
        </div>
      </div>

      <!-- Bottom: Client source & Node -->
      <div class="m-conn-footer">
        <div class="m-conn-footer__top">
          <div class="source-client-group">
            <span class="source-client">{{ row.entry !== '—' ? row.entry : '未知客户端' }}</span>
            <span class="source-sep">·</span>
            <span class="source-ip font-mono">{{ row.source_ip || row.source }}</span>
          </div>
          <span class="m-conn-node-tag">{{ row.node_name }}</span>
        </div>
        <div v-if="row.source_location" class="m-conn-footer__geo">
          <span class="source-loc">{{ row.source_location }}</span>
        </div>
      </div>
    </article>

    <div v-if="!filteredRows.length && !loading" class="m-empty">当前没有活动连接</div>
    <button v-if="filteredRows.length > visible" type="button" class="m-load-more" @click="visible += 30">
      加载更多（还有 {{ filteredRows.length - visible }} 条）
    </button>

    <MActionSheet v-model="detailOpen" :title="detailTarget?.target" :details="details" />
  </MPage>
</template>
<style scoped>
.m-conn-card {
  display: block;
  width: 100%;
  padding: 13px 14px 11px;
  margin-bottom: 10px;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  box-shadow: var(--sb-shadow-sm);
  cursor: pointer;
  text-align: left;
  user-select: none;
  transition: transform 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.m-conn-card:active {
  transform: scale(0.992);
}
.m-conn-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 10px;
}
.m-conn-target {
  flex: 1;
  min-width: 0;
}
.m-conn-host {
  color: var(--sb-text);
  font-size: 14.5px;
  font-weight: 650;
  line-height: 1.35;
  word-break: break-all;
}
.m-conn-geo {
  display: flex;
  align-items: center;
  gap: 5px;
  margin-top: 3px;
  font-size: 11.5px;
  color: var(--sb-muted);
}
.geo-dot {
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--sb-muted);
  flex-shrink: 0;
}
.m-conn-badges {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.m-conn-tag {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 2px 7px;
  border-radius: var(--sb-radius-sm);
  font-size: 11px;
  font-weight: 550;
  line-height: 18px;
  background: var(--sb-surface-2);
  color: var(--sb-text-2);
  border: 1px solid var(--sb-line-strong);
  white-space: nowrap;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.m-conn-tag--exit {
  background: var(--sb-accent-soft);
  color: var(--sb-text);
  border-color: var(--sb-line-strong);
  font-weight: 600;
}

/* Inset rates row */
.m-conn-rates {
  display: flex;
  align-items: center;
  margin-top: 10px;
  padding: 8px 10px;
  border-radius: 10px;
  background: var(--neu-inset-bg);
  border: 1px solid var(--sb-line);
  box-shadow: var(--sh-inset);
}
.rate-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.rate-lbl {
  font-size: 10.5px;
  color: var(--sb-muted);
}
.rate-val {
  font-size: 11.5px;
  font-weight: 650;
  letter-spacing: -0.02em;
  color: var(--sb-text);
  white-space: nowrap;
}
.rate-divider {
  width: 1px;
  height: 22px;
  background: var(--sb-line);
  margin: 0 6px;
  flex-shrink: 0;
}

/* Footer row */
.m-conn-footer {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-top: 9px;
  padding-top: 8px;
  border-top: 1px solid var(--sb-line);
  font-size: 12px;
}
.m-conn-footer__top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.source-client-group {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.source-client {
  color: var(--sb-text-2);
  font-weight: 600;
}
.source-ip {
  color: var(--sb-muted);
  font-size: 11.5px;
}
.source-sep {
  opacity: 0.5;
}
.m-conn-node-tag {
  flex-shrink: 0;
  padding: 1px 7px;
  border-radius: 5px;
  font-size: 11px;
  font-weight: 500;
  color: var(--sb-muted);
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
}
.m-conn-footer__geo {
  display: flex;
  align-items: flex-start;
  font-size: 11.5px;
  line-height: 1.45;
  color: var(--sb-muted);
  word-break: break-all;
  white-space: normal;
}
.source-loc {
  flex: 1;
  min-width: 0;
}
</style>
