<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { formatBytes, formatDateTime, includesText } from '../../format'
import { connectionSnapshots, subscribeConnections } from '../../connections'
import MPage from '../components/MPage.vue'
import MPicker from '../components/MPicker.vue'

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

const rows = computed(() => [...connectionSnapshots.value.values()].flatMap((result) => {
  const node = appState.nodes.find((item) => item.id === result.node_id)
  return (result.connections || []).map((connection) => ({
    ...connection,
    node_id: result.node_id,
    node_name: node?.name || result.node_id,
    // master 会把 sing-box 的出站标签解析成操作者配置的出口，解析不到才显示原始标签。
    exit: connection.outbound_name || connection.outbound || connection.chains?.[0] || 'DIRECT',
    entry: connection.listener_name || '—',
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
    row.entry, row.user, row.exit, row.network,
  ], keyword.value)
}))
const shown = computed(() => filteredRows.value.slice(0, visible.value))

watch([keyword, selectedNode, selectedOutbound], () => { visible.value = 30 })

async function load() {
  loading.value = true
  try {
    await loadNodes()
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await load()
  stopConnections = subscribeConnections(() => {})
})
onBeforeUnmount(() => stopConnections?.())
</script>

<template>
  <MPage title="当前连接" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
    </template>

    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索 IP、目标、入站节点或出口" />
    <div class="filters">
      <MPicker v-model="selectedNode" :options="nodeOptions" title="按服务器筛选" placeholder="全部服务器" />
      <MPicker v-model="selectedOutbound" :options="outboundOptions" title="按出口筛选" placeholder="全部出口" />
    </div>
    <div class="m-count">{{ filteredRows.length }} 条 · 采集于 {{ formatDateTime(collectedAt, '暂无数据') }}</div>

    <article v-for="row in shown" :key="`${row.node_id}/${row.id}`" class="m-card">
      <div class="m-card__top">
        <span class="m-card__title">{{ row.target }}</span>
        <span class="m-pill m-pill--accent">{{ row.exit }}</span>
      </div>
      <div class="m-card__row">
        <span>{{ row.node_name }}</span>
        <span class="m-card__spacer" />
        <span>{{ [row.user, row.source_location].filter(Boolean).join(' · ') || '未知来源' }}</span>
      </div>
      <div class="m-card__row m-mono">
        <span>{{ row.source_ip || row.source || '—' }}</span>
        <span class="m-card__spacer" />
        <span>{{ (row.network || '').toUpperCase() || '—' }}</span>
      </div>
      <div class="m-card__row m-mono">
        <span>↓ {{ formatBytes(row.download) }} · ↑ {{ formatBytes(row.upload) }}</span>
        <span class="m-card__spacer" />
        <span>{{ formatDateTime(row.started_at) }}</span>
      </div>
      <div class="m-card__row"><span>入站 {{ row.entry }}</span></div>
    </article>

    <div v-if="!filteredRows.length" class="m-empty">当前没有活动连接</div>
    <button v-if="filteredRows.length > visible" type="button" class="m-load-more" @click="visible += 30">
      加载更多（还有 {{ filteredRows.length - visible }} 条）
    </button>
  </MPage>
</template>

<style scoped>
.filters { display: flex; gap: 10px; margin-top: 10px; }
.filters > * { flex: 1; min-width: 0; }
</style>
