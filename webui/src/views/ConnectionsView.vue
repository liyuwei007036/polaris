<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { formatBytes, formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const rows = ref([])
const collectedAt = ref('')
const loading = ref(false)
const selectedNode = ref('')
const selectedRule = ref('')
const keyword = ref('')
const ruleOptions = computed(() => [...new Set(rows.value.map((row) => row.rule).filter(Boolean))].sort())
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedRule.value && row.rule !== selectedRule.value) return false
  return includesText([
    row.node_name, row.source, row.source_ip, row.source_location, row.host, row.destination,
    row.inbound, row.outbound, row.rule, row.rule_payload, ...(row.chains || []),
  ], keyword.value)
}))
let source
const snapshots = new Map()

function rebuildRows() {
  const values = [...snapshots.values()]
  rows.value = values.flatMap((result) => {
    const node = appState.nodes.find((item) => item.id === result.node_id)
    return (result.connections || []).map((connection) => ({ ...connection, node_id: result.node_id, node_name: node?.name || result.node_id }))
  })
  collectedAt.value = values.map((result) => result.collected_at).filter(Boolean).sort().at(-1) || ''
}

function connect() {
  source?.close()
  source = new EventSource('/api/v1/events/connections', { withCredentials: true })
  source.addEventListener('snapshot', (event) => {
    snapshots.clear()
    for (const item of JSON.parse(event.data).nodes || []) snapshots.set(item.node_id, item)
    rebuildRows()
    loading.value = false
  })
  source.addEventListener('node', (event) => {
    const item = JSON.parse(event.data)
    snapshots.set(item.node_id, item)
    rebuildRows()
  })
}

async function load() {
  loading.value = true
  try {
    await loadNodes()
    connect()
  } catch {
    loading.value = false
  }
}

onMounted(load)
onBeforeUnmount(() => source?.close())
</script>

<template>
  <div class="page-shell">
    <PageHeader title="当前连接">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索 IP、目标、规则或策略" style="width: 280px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 190px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
        <el-select v-model="selectedRule" clearable placeholder="全部规则" style="width: 160px">
          <el-option v-for="rule in ruleOptions" :key="rule" :label="rule" :value="rule" />
        </el-select>
        <span class="toolbar__spacer" />
        <span class="subtle">{{ filteredRows.length }} 条 · {{ formatDateTime(collectedAt, '暂无数据') }}</span>
      </div>
      <div class="table-panel">
        <el-table v-loading="loading" :data="filteredRows" row-key="id">
          <el-table-column label="服务器" prop="node_name" min-width="140" />
          <el-table-column label="客户端 IP" min-width="210">
            <template #default="{ row }">
              <div class="mono">{{ row.source_ip || row.source || '—' }}</div>
              <div class="subtle">{{ row.source_location || '未知' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="访问目标" min-width="230">
            <template #default="{ row }"><strong>{{ row.host || row.destination || '—' }}</strong><div v-if="row.host" class="subtle">{{ row.destination }}</div></template>
          </el-table-column>
          <el-table-column label="命中规则" min-width="190">
            <template #default="{ row }"><el-tag size="small" effect="plain">{{ row.rule || 'MATCH' }}</el-tag><span class="rule-payload">{{ row.rule_payload || row.host || '全部流量' }}</span></template>
          </el-table-column>
          <el-table-column label="策略链路" min-width="190">
            <template #default="{ row }">{{ row.chains?.length ? row.chains.join(' → ') : row.outbound || 'DIRECT' }}</template>
          </el-table-column>
          <el-table-column label="流量" width="150"><template #default="{ row }"><span class="mono">↓ {{ formatBytes(row.download) }} · ↑ {{ formatBytes(row.upload) }}</span></template></el-table-column>
          <el-table-column label="开始时间" width="180"><template #default="{ row }">{{ formatDateTime(row.started_at) }}</template></el-table-column>
        </el-table>
      </div>
    </main>
  </div>
</template>

<style scoped>
.rule-payload { display: block; max-width: 180px; margin-top: 5px; overflow: hidden; color: var(--sb-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
</style>
