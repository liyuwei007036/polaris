<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Search } from '@element-plus/icons-vue'
import { formatBytes, formatDateTime, includesText } from '../../format'
import { connectionSnapshots, subscribeConnections } from '../../connections'
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

const rows = computed(() => [...connectionSnapshots.value.values()].flatMap((result) => {
  const node = appState.nodes.find((item) => item.id === result.node_id)
  return (result.connections || []).map((connection) => ({
    ...connection,
    node_id: result.node_id,
    node_name: node?.name || result.node_id,
    // master 会把 sing-box 的出站标签解析成操作者配置的出口，解析不到才显示原始标签。
    exit: connection.outbound_name || connection.outbound || connection.chains?.[0] || 'DIRECT',
    entry: entryLabel(connection),
    target: targetLabel(connection),
  }))
}))
const collectedAt = computed(() => [...connectionSnapshots.value.values()].map((result) => result.collected_at).filter(Boolean).sort().at(-1) || '')
const nodeOptions = computed(() => [{ value: '', label: '全部服务器' }, ...appState.nodes.map((node) => ({ value: node.id, label: node.name }))])
const outboundOptions = computed(() => [
  { value: '', label: '全部出口' },
  ...[...new Set(rows.value.map((row) => row.exit).filter(Boolean))].sort().map((exit) => ({ value: exit, label: exit })),
])
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedOutbound.value && row.exit !== selectedOutbound.value) return false
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
    { label: '目标', value: row.target, mono: true },
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
  <MPage :loading="loading">
    <div class="m-listbar">
      <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索 IP、目标、入站节点或出口" />
      <div class="m-filters">
        <MPicker v-model="selectedNode" chip :options="nodeOptions" title="按服务器筛选" placeholder="全部服务器" />
        <MPicker v-model="selectedOutbound" chip :options="outboundOptions" title="按出口筛选" placeholder="全部出口" />
      </div>
    </div>
    <div class="m-count">{{ filteredRows.length }} 条 · 采集于 {{ formatDateTime(collectedAt, '暂无数据') }}</div>

    <article v-for="row in shown" :key="`${row.node_id}/${row.id}`" class="m-item">
      <!-- 出口靠右居中：目标、速率、来源三层是这条连接「是什么」，出口是它
           「走哪儿出去」，两件事分列左右比串在标题行里挤更好认。 -->
      <button type="button" class="m-item__hit conn" @click="openDetail(row)">
        <div class="conn__main">
          <div class="m-item__head">
            <span class="m-item__title m-item__title--mono">{{ row.target }}</span>
            <span class="m-pill m-pill--info">{{ (row.network || '').toUpperCase() || '—' }}</span>
          </div>
          <!-- 出口占掉右边一截后，三格速率会把「4.60 MB/s」截成「4.60 M…」。
               TCP/UDP 是个短标签，挪到标题行；留下的两格才装得下完整的数。 -->
          <div class="m-item__stats">
            <span class="m-stat"><b>↓ {{ row.has_rates ? formatBytes(row.download_rate, '/s') : '—' }}</b><small>实时下行</small></span>
            <span class="m-stat"><b>↑ {{ row.has_rates ? formatBytes(row.upload_rate, '/s') : '—' }}</b><small>实时上行</small></span>
          </div>
          <div class="m-item__meta">{{ sourceLine(row) }}</div>
        </div>
        <span class="conn__exit">{{ row.exit }}</span>
      </button>
    </article>

    <div v-if="!filteredRows.length && !loading" class="m-empty">当前没有活动连接</div>
    <button v-if="filteredRows.length > visible" type="button" class="m-load-more" @click="visible += 30">
      加载更多（还有 {{ filteredRows.length - visible }} 条）
    </button>

    <MActionSheet v-model="detailOpen" :title="detailTarget?.target" :details="details" />
  </MPage>
</template>
<style scoped>
.conn { display: flex; align-items: center; gap: 10px; }
.conn__main { flex: 1; min-width: 0; }
/* 出口名长短不一（DIRECT、直连、机场名），宽了就折行而不是截断：
   一条连接走哪儿出去是排障的落点，截成「美国家宽…」等于没写。 */
.conn__exit {
  flex: none;
  max-width: 34%;
  padding: 6px 11px;
  color: #7dd3fc;
  background: rgba(56, 189, 248, .14);
  border-radius: 12px;
  font-size: 12px;
  font-weight: 600;
  line-height: 1.4;
  text-align: center;
  word-break: break-word;
}
</style>
