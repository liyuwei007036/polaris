<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Clock, Download, Filter, Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api, csrfToken } from '../api'
import { formatBytes, formatDateTime, formatDuration, localTimeZoneLabel } from '../format'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const exporting = ref(false)
const timeZoneLabel = localTimeZoneLabel()

// 历史连接查询参数
const filter = reactive({
  ip: '',
  node_id: '',
  user: '',
  network: '',
  outbound: '',
  keyword: '',
  dateRange: null,
})

// 排序参数
const sortState = reactive({
  prop: 'started_at',
  order: 'descending',
})

// 分页参数
const query = reactive({
  page: 1,
  pageSize: 20,
  total: 0,
})

const records = ref([])

const dateShortcuts = [
  { text: '今天', value: () => [new Date(new Date().setHours(0, 0, 0, 0)), new Date()] },
  { text: '近 3 天', value: () => [new Date(Date.now() - 3 * 24 * 3600 * 1000), new Date()] },
  { text: '近 7 天', value: () => [new Date(Date.now() - 7 * 24 * 3600 * 1000), new Date()] },
  { text: '近 30 天', value: () => [new Date(Date.now() - 30 * 24 * 3600 * 1000), new Date()] },
]

function buildQueryParams() {
  const params = new URLSearchParams()
  if (filter.ip) params.set('ip', filter.ip.trim())
  if (filter.node_id) params.set('node_id', filter.node_id)
  if (filter.user) params.set('user', filter.user.trim())
  if (filter.network) params.set('network', filter.network)
  if (filter.outbound) params.set('outbound', filter.outbound.trim())
  if (filter.keyword) params.set('keyword', filter.keyword.trim())
  if (filter.dateRange && filter.dateRange.length === 2) {
    params.set('start_time', filter.dateRange[0].toISOString())
    params.set('end_time', filter.dateRange[1].toISOString())
  }
  if (sortState.prop) {
    params.set('order_by', sortState.prop)
    params.set('order_dir', sortState.order === 'ascending' ? 'asc' : 'desc')
  }
  return params
}

async function loadRecords() {
  loading.value = true
  try {
    const params = buildQueryParams()
    params.set('page', String(query.page))
    params.set('page_size', String(query.pageSize))

    const data = await api(`/devices/connections?${params}`)
    records.value = data.records || []
    query.total = data.total || 0
  } catch (err) {
    console.error('Failed to load connection records', err)
    ElMessage.error(err.message || '加载历史连接记录失败')
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  query.page = 1
  loadRecords()
}

function resetFilter() {
  filter.ip = ''
  filter.node_id = ''
  filter.user = ''
  filter.network = ''
  filter.outbound = ''
  filter.keyword = ''
  filter.dateRange = null
  sortState.prop = 'started_at'
  sortState.order = 'descending'
  query.page = 1
  loadRecords()
}

function handleSortChange({ prop, order }) {
  if (!order) {
    sortState.prop = 'started_at'
    sortState.order = 'descending'
  } else {
    sortState.prop = prop
    sortState.order = order
  }
  query.page = 1
  loadRecords()
}

async function exportCSV() {
  exporting.value = true
  try {
    const params = buildQueryParams()

    const headers = {}
    if (csrfToken.value) headers['X-CSRF-Token'] = csrfToken.value

    const res = await fetch(`/api/v1/devices/connections/export?${params}`, {
      method: 'GET',
      credentials: 'same-origin',
      headers,
    })
    if (!res.ok) {
      throw new Error(`导出失败（HTTP ${res.status}）`)
    }

    const blob = await res.blob()
    const disposition = res.headers.get('content-disposition') || ''
    const match = disposition.match(/filename="?([^"]+)"?/)
    const filename = match ? match[1] : `connections_${Date.now()}.csv`

    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = filename
    document.body.appendChild(link)
    link.click()
    document.body.removeChild(link)
    URL.revokeObjectURL(url)

    ElMessage.success('历史连接记录已导出')
  } catch (err) {
    console.error('Export error', err)
    ElMessage.error(err.message || '导出连接明细失败')
  } finally {
    exporting.value = false
  }
}

function parseHashParams() {
  const hash = location.hash || ''
  const qIndex = hash.indexOf('?')
  if (qIndex === -1) return
  const queryStr = hash.slice(qIndex + 1)
  const params = new URLSearchParams(queryStr)
  let changed = false
  if (params.has('ip')) {
    filter.ip = params.get('ip')
    changed = true
  }
  if (params.has('node_id')) {
    filter.node_id = params.get('node_id')
    changed = true
  }
  if (params.has('user')) {
    filter.user = params.get('user')
    changed = true
  }
  return changed
}

function onHashChange() {
  if (parseHashParams()) {
    handleSearch()
  }
}

onMounted(() => {
  loadNodes?.().catch(() => {})
  parseHashParams()
  loadRecords()
  window.addEventListener('hashchange', onHashChange)
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', onHashChange)
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="历史连接明细">
      <span class="subtle" style="margin-right: 12px">数据保留最近 30 天 · {{ timeZoneLabel }}</span>
      <el-button :icon="Download" :loading="exporting" @click="exportCSV">导出 CSV</el-button>
      <el-button :icon="Refresh" :loading="loading" @click="loadRecords">刷新</el-button>
    </PageHeader>

    <main class="page-content history-content">
      <div class="table-panel">
        <!-- 搜索与筛选工具栏 -->
        <div class="search-toolbar history-toolbar">
          <el-input
            v-model="filter.ip"
            clearable
            :prefix-icon="Search"
            placeholder="来源 IP"
            style="width: 170px"
            @keyup.enter="handleSearch"
          />
          <el-select v-model="filter.node_id" clearable placeholder="全部服务器" style="width: 150px" @change="handleSearch">
            <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
          </el-select>
          <el-select v-model="filter.network" clearable placeholder="协议" style="width: 100px" @change="handleSearch">
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
          </el-select>
          <el-input
            v-model="filter.user"
            clearable
            placeholder="认证用户 / 设备"
            style="width: 160px"
            @keyup.enter="handleSearch"
          />
          <el-input
            v-model="filter.keyword"
            clearable
            :prefix-icon="Search"
            placeholder="目标域名、端口或出口"
            style="width: 220px"
            @keyup.enter="handleSearch"
          />
          <el-date-picker
            v-model="filter.dateRange"
            type="datetimerange"
            range-separator="至"
            start-placeholder="开始时间"
            end-placeholder="结束时间"
            :shortcuts="dateShortcuts"
            style="width: 320px"
            @change="handleSearch"
          />
          <div class="toolbar-actions">
            <el-button type="primary" :icon="Search" @click="handleSearch">查询</el-button>
            <el-button @click="resetFilter">重置</el-button>
          </div>
        </div>

        <!-- 历史连接明细数据表格 -->
        <el-table
          v-loading="loading"
          :data="records"
          :default-sort="{ prop: 'started_at', order: 'descending' }"
          empty-text="未找到匹配的历史连接记录"
          class="history-table"
          @sort-change="handleSortChange"
        >
          <el-table-column label="来源 IP（设备）" prop="source_ip" sortable="custom" min-width="190" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono font-bold">{{ row.source_ip }}<span class="subtle">{{ row.source_port ? `:${row.source_port}` : '' }}</span></div>
              <div class="subtle" style="font-size: 12px">{{ row.source_location || '未知归属地' }}</div>
            </template>
          </el-table-column>

          <el-table-column label="目标" prop="host" sortable="custom" min-width="210" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono font-medium">{{ row.host || row.destination }}</div>
              <div v-if="row.host && row.destination && row.host !== row.destination" class="subtle mono" style="font-size: 11px">
                {{ row.destination }}
              </div>
            </template>
          </el-table-column>

          <el-table-column label="服务器" prop="node_name" min-width="110" show-overflow-tooltip>
            <template #default="{ row }">
              <el-tag size="small" effect="plain">{{ row.node_name || row.node_id }}</el-tag>
            </template>
          </el-table-column>

          <el-table-column label="认证账号" prop="user" sortable="custom" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.user">{{ row.user }}</span>
              <span v-else class="subtle">—</span>
            </template>
          </el-table-column>

          <el-table-column label="网络 / 出口" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <div><el-tag size="small" effect="plain">{{ (row.network || 'TCP').toUpperCase() }}</el-tag></div>
              <div class="subtle" style="font-size: 11px; margin-top: 2px">{{ row.outbound_name || '—' }}</div>
            </template>
          </el-table-column>

          <el-table-column label="传输流量" prop="total_bytes" sortable="custom" min-width="140" align="right">
            <template #default="{ row }">
              <div class="mono font-bold">{{ formatBytes(row.upload + row.download) }}</div>
              <div class="mono subtle" style="font-size: 11px">↓{{ formatBytes(row.download) }} ↑{{ formatBytes(row.upload) }}</div>
            </template>
          </el-table-column>

          <el-table-column label="开始时间" prop="started_at" sortable="custom" min-width="170" show-overflow-tooltip>
            <template #default="{ row }">{{ formatDateTime(row.started_at) }}</template>
          </el-table-column>

          <el-table-column label="持续时间" prop="duration_seconds" sortable="custom" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.closed_at">{{ formatDuration(row.duration_seconds) }}</span>
              <el-tag v-else size="small" type="success" effect="light">连接中</el-tag>
            </template>
          </el-table-column>
        </el-table>

        <div class="pagination-bar">
          <el-pagination
            v-model:current-page="query.page"
            v-model:page-size="query.pageSize"
            :total="query.total"
            :page-sizes="[20, 50, 100, 200]"
            background
            layout="total, sizes, prev, pager, next"
            @change="loadRecords"
          />
        </div>
      </div>
    </main>
  </div>
</template>

<style scoped>
.history-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.table-panel {
  background: var(--sb-bg-panel, #121826);
  border-radius: 8px;
  overflow: hidden;
}

.history-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--sb-line, rgba(255, 255, 255, 0.08));
}

.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}

.history-table {
  width: 100%;
}

.pagination-bar {
  display: flex;
  justify-content: flex-end;
  padding: 14px 20px;
  border-top: 1px solid var(--sb-line, rgba(255, 255, 255, 0.08));
}

.clickable {
  cursor: pointer;
}

.clickable:hover {
  text-decoration: underline;
}

.font-bold {
  font-weight: 600;
}

.font-medium {
  font-weight: 500;
}
</style>
