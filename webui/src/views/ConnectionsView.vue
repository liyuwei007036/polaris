<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { formatBytes, formatDateTime, includesText } from '../format'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(true)
const selectedNode = ref('')
const selectedOutbound = ref('')
const keyword = ref('')
let stopConnections

const rows = computed(() => [...connectionSnapshots.value.values()].flatMap((result) => {
  const node = appState.nodes.find((item) => item.id === result.node_id)
  return (result.connections || []).map((connection) => ({
    ...connection,
    node_id: result.node_id,
    node_name: node?.name || result.node_id,
    // The master resolves the sing-box outbound tag to the egress an operator
    // configured; the raw tag is only shown when that lookup found nothing.
    exit: connection.outbound_name || connection.outbound || connection.chains?.[0] || 'DIRECT',
    entry: connection.listener_name || '—',
  }))
}))
const collectedAt = computed(() => [...connectionSnapshots.value.values()].map((result) => result.collected_at).filter(Boolean).sort().at(-1) || '')
const outboundOptions = computed(() => [...new Set(rows.value.map((row) => row.exit).filter(Boolean))].sort())
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedOutbound.value && row.exit !== selectedOutbound.value) return false
  return includesText([
    row.node_name, row.source, row.source_ip, row.source_location, row.host, row.destination,
    row.entry, row.user, row.exit, row.network,
  ], keyword.value)
}))

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
  <div class="page-shell">
    <PageHeader title="当前连接">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索 IP、目标、客户端节点或出口" style="width: 280px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 190px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
        <el-select v-model="selectedOutbound" clearable placeholder="全部出口" style="width: 180px">
          <el-option v-for="exit in outboundOptions" :key="exit" :label="exit" :value="exit" />
        </el-select>
        <span class="toolbar__spacer" />
        <span class="subtle">{{ filteredRows.length }} 条 · {{ formatDateTime(collectedAt, '暂无数据') }}</span>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredRows" :loading="loading" empty-text="当前没有活动连接">
          <el-table-column label="服务器" prop="node_name" min-width="130" show-overflow-tooltip />
          <el-table-column label="客户端节点" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">
              <div>{{ row.user || '—' }}</div>
              <div class="subtle">{{ [row.entry, (row.network || '').toUpperCase()].filter((part) => part && part !== '—').join(' · ') || '—' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="客户端 IP" min-width="180" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono">{{ row.source_ip || row.source || '—' }}</div>
              <div class="subtle">{{ row.source_location || '未知' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="访问目标" min-width="200" show-overflow-tooltip>
            <template #default="{ row }"><strong>{{ row.host || row.destination || '—' }}</strong><div v-if="row.host" class="subtle">{{ row.destination }}</div></template>
          </el-table-column>
          <el-table-column label="出口" min-width="150" show-overflow-tooltip>
            <template #default="{ row }"><el-tag size="small" effect="plain">{{ row.exit }}</el-tag></template>
          </el-table-column>
          <el-table-column label="流量" min-width="156"><template #default="{ row }"><span class="mono">↓ {{ formatBytes(row.download) }} · ↑ {{ formatBytes(row.upload) }}</span></template></el-table-column>
          <el-table-column label="开始时间" width="152"><template #default="{ row }">{{ formatDateTime(row.started_at) }}</template></el-table-column>
        </PagedTable>
      </div>
    </main>
  </div>
</template>
