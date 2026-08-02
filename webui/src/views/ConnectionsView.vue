<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const rows = ref([])
const collectedAt = ref('')
const loading = ref(false)
const selectedNode = ref('')
const filteredRows = computed(() => selectedNode.value
  ? rows.value.filter((row) => row.node_id === selectedNode.value)
  : rows.value)
let source
const snapshots = new Map()
function bytes(value) {
  if (!Number.isFinite(Number(value))) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']; let number = Number(value); let index = 0
  while (number >= 1024 && index < units.length - 1) { number /= 1024; index++ }
  return `${number.toFixed(index ? 1 : 0)} ${units[index]}`
}
function rebuildRows() {
  const values = [...snapshots.values()]
  rows.value = values.flatMap((result) => {
    const node = appState.nodes.find((item) => item.id === result.node_id)
    return (result.connections || []).map((connection) => ({
      ...connection,
      node_id: result.node_id,
      node_name: node?.name || result.node_id,
    }))
  })
  collectedAt.value = values.map((result) => result.collected_at).filter(Boolean).sort().at(-1) || ''
}
function connect() {
  source?.close()
  source = new EventSource('/api/v1/events/connections', { withCredentials: true })
  source.addEventListener('snapshot', (event) => {
    snapshots.clear()
    const payload = JSON.parse(event.data)
    ;(payload.nodes || []).forEach((item) => snapshots.set(item.node_id, item))
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
  } catch { loading.value = false }
}
onMounted(load)
onBeforeUnmount(() => source?.close())
</script>

<template>
  <div class="page-shell">
    <PageHeader title="当前连接" description="查看全部服务器上正在使用的客户端连接">
      <el-button :icon="Refresh" @click="load">立即刷新</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <div class="toolbar">
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 220px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
        <span class="subtle">共 {{ filteredRows.length }} 条连接，数据会自动更新。最近更新时间：{{ collectedAt || '暂无数据' }}</span>
      </div>
      <div class="table-panel">
        <el-table v-loading="loading" :data="filteredRows" row-key="id">
          <el-table-column label="服务器" prop="node_name" min-width="150" />
          <el-table-column label="客户端地址" prop="source" min-width="180" />
          <el-table-column label="访问目标" min-width="230"><template #default="{ row }"><strong>{{ row.host || row.destination }}</strong><div v-if="row.host" class="subtle">{{ row.destination }}</div></template></el-table-column>
          <el-table-column label="连接类型" width="90"><template #default="{ row }">{{ (row.network || '—').toUpperCase() }}</template></el-table-column>
          <el-table-column label="接入服务 / 上网出口" min-width="170"><template #default="{ row }">{{ row.inbound || '—' }} → {{ row.outbound || '—' }}</template></el-table-column>
          <el-table-column label="使用的规则" prop="rule" min-width="140"><template #default="{ row }">{{ row.rule || '—' }}</template></el-table-column>
          <el-table-column label="上传" width="100"><template #default="{ row }">{{ bytes(row.upload) }}</template></el-table-column>
          <el-table-column label="下载" width="100"><template #default="{ row }">{{ bytes(row.download) }}</template></el-table-column>
          <el-table-column label="开始时间" prop="started_at" width="190" />
        </el-table>
      </div>
    </main>
  </div>
</template>
