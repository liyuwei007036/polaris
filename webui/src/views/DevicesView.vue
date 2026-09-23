<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { Filter, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../api'
import { formatBytes, formatDateTime, formatDuration, localTimeZoneLabel } from '../format'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const popularLoading = ref(false)
const timeZoneLabel = localTimeZoneLabel()

// 热门设备概览与排行
const popularRange = ref('7d')
const summary = reactive({
  total_connections: 0,
  unique_devices: 0,
  unique_ips: 0,
  total_upload: 0,
  total_download: 0,
})
const popularDevices = ref([])

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
const query = reactive({
  page: 1,
  pageSize: 20,
  total: 0,
})
const records = ref([])
const tableSectionRef = ref()

const dateShortcuts = [
  { text: '今天', value: () => [new Date(new Date().setHours(0, 0, 0, 0)), new Date()] },
  { text: '近 3 天', value: () => [new Date(Date.now() - 3 * 24 * 3600 * 1000), new Date()] },
  { text: '近 7 天', value: () => [new Date(Date.now() - 7 * 24 * 3600 * 1000), new Date()] },
  { text: '近 30 天', value: () => [new Date(Date.now() - 30 * 24 * 3600 * 1000), new Date()] },
]

async function loadPopular() {
  popularLoading.value = true
  try {
    const data = await api(`/devices/popular?range=${popularRange.value}&limit=15`)
    Object.assign(summary, data.summary || {})
    popularDevices.value = data.devices || []
  } catch (err) {
    console.error('Failed to load popular devices', err)
  } finally {
    popularLoading.value = false
  }
}

async function loadRecords() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: String(query.page),
      page_size: String(query.pageSize),
    })
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

    const data = await api(`/devices/connections?${params}`)
    records.value = data.records || []
    query.total = data.total || 0
  } catch (err) {
    console.error('Failed to load connection records', err)
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
  query.page = 1
  loadRecords()
}

function filterByIP(ip) {
  filter.ip = ip
  handleSearch()
  tableSectionRef.value?.scrollIntoView({ behavior: 'smooth' })
}

function filterByUser(userName) {
  filter.user = userName
  handleSearch()
  tableSectionRef.value?.scrollIntoView({ behavior: 'smooth' })
}

function rankTagType(index) {
  if (index === 0) return 'danger'
  if (index === 1) return 'warning'
  if (index === 2) return 'success'
  return 'info'
}

async function refreshAll() {
  await Promise.all([loadNodes(), loadPopular(), loadRecords()])
}

onMounted(() => {
  refreshAll()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="热门设备">
      <span class="subtle" style="margin-right: 12px">数据保留最近 30 天 · {{ timeZoneLabel }}</span>
      <el-button :icon="Refresh" @click="refreshAll">刷新</el-button>
    </PageHeader>

    <main class="page-content devices-content">
      <!-- 汇总指标卡片 -->
      <section class="metric-strip">
        <div class="metric">
          <div class="metric__label">历史连接数</div>
          <div class="metric__value">{{ summary.total_connections.toLocaleString() }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">活跃设备数</div>
          <div class="metric__value">{{ summary.unique_devices }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">独立 IP 数</div>
          <div class="metric__value">{{ summary.unique_ips }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">累计传输流量</div>
          <div class="metric__value">{{ formatBytes(summary.total_download + summary.total_upload) }}</div>
        </div>
      </section>

      <!-- 热门设备排行 -->
      <div class="table-panel">
        <div class="panel-header">
          <div class="panel-title">
            <strong>热门设备排行</strong>
            <span class="subtle" style="margin-left: 8px">按连接频次与流量排序</span>
          </div>
          <el-radio-group v-model="popularRange" size="small" @change="loadPopular">
            <el-radio-button label="24h">最近 24 小时</el-radio-button>
            <el-radio-button label="7d">最近 7 天</el-radio-button>
            <el-radio-button label="30d">最近 30 天</el-radio-button>
          </el-radio-group>
        </div>

        <el-table v-loading="popularLoading" :data="popularDevices" empty-text="当前周期内暂无热门设备记录">
          <el-table-column label="排名" width="70" align="center">
            <template #default="{ $index }">
              <el-tag :type="rankTagType($index)" size="small" effect="dark" round>
                #{{ $index + 1 }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="设备 / 账号" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">
              <span class="device-name clickable" @click="filterByUser(row.user || row.source_ip)">
                {{ row.user || '未知设备' }}
              </span>
            </template>
          </el-table-column>
          <el-table-column label="常用来源 IP" min-width="180" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono clickable" @click="filterByIP(row.source_ip)">{{ row.source_ip }}</div>
              <div class="subtle">{{ row.source_location || '未知归属地' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="连接次数" min-width="110" align="right">
            <template #default="{ row }">
              <strong>{{ row.connection_count.toLocaleString() }}</strong> 次
            </template>
          </el-table-column>
          <el-table-column label="总流量" min-width="130" align="right">
            <template #default="{ row }">
              <div class="mono font-bold">{{ formatBytes(row.total_bytes) }}</div>
              <div class="mono subtle" style="font-size: 11px">↓{{ formatBytes(row.download) }} ↑{{ formatBytes(row.upload) }}</div>
            </template>
          </el-table-column>
          <el-table-column label="最近活跃" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">{{ formatDateTime(row.last_seen_at) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="130" align="center">
            <template #default="{ row }">
              <el-button link type="primary" size="small" :icon="Filter" @click="filterByIP(row.source_ip)">
                查连接
              </el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>

      <!-- 历史连接记录 -->
      <div ref="tableSectionRef" class="table-panel">
        <div class="panel-header">
          <div class="panel-title">
            <strong>历史连接明细</strong>
            <span class="subtle" style="margin-left: 8px">支持按 IP 等参数全面检索</span>
          </div>
        </div>

        <div class="search-toolbar history-toolbar">
          <el-input
            v-model="filter.ip"
            clearable
            :prefix-icon="Search"
            placeholder="按来源 IP 筛选"
            style="width: 170px"
            @keyup.enter="handleSearch"
          />
          <el-select v-model="filter.node_id" clearable placeholder="全部服务器" style="width: 150px" @change="handleSearch">
            <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
          </el-select>
          <el-select v-model="filter.network" clearable placeholder="全部协议" style="width: 110px" @change="handleSearch">
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
          </el-select>
          <el-input
            v-model="filter.user"
            clearable
            placeholder="按设备 / 用户筛选"
            style="width: 150px"
            @keyup.enter="handleSearch"
          />
          <el-input
            v-model="filter.keyword"
            clearable
            :prefix-icon="Search"
            placeholder="目标域名、端口或出口"
            style="width: 200px"
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
          <el-button type="primary" :icon="Search" @click="handleSearch">查询</el-button>
          <el-button @click="resetFilter">重置</el-button>
        </div>

        <el-table v-loading="loading" :data="records" empty-text="未找到匹配的历史连接记录">
          <el-table-column label="服务器" prop="node_name" min-width="100" show-overflow-tooltip>
            <template #default="{ row }">
              {{ row.node_name || row.node_id }}
            </template>
          </el-table-column>
          <el-table-column label="设备 / 账号" min-width="130" show-overflow-tooltip>
            <template #default="{ row }">
              <strong>{{ row.user || row.listener_name || '—' }}</strong>
            </template>
          </el-table-column>
          <el-table-column label="来源" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono clickable" @click="filterByIP(row.source_ip)">
                {{ row.source_ip }}{{ row.source_port ? `:${row.source_port}` : '' }}
              </div>
              <div class="subtle">{{ row.source_location || '未知归属地' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="目标" min-width="190" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono font-medium">{{ row.host || row.destination }}</div>
              <div v-if="row.host && row.destination && row.host !== row.destination" class="subtle mono" style="font-size: 11px">
                {{ row.destination }}
              </div>
            </template>
          </el-table-column>
          <el-table-column label="网络 / 出口" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <div><el-tag size="small" effect="plain">{{ (row.network || 'TCP').toUpperCase() }}</el-tag></div>
              <div class="subtle" style="margin-top: 2px">{{ row.outbound_name || '—' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="传输流量" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono">↓ {{ formatBytes(row.download) }}</div>
              <div class="mono subtle">↑ {{ formatBytes(row.upload) }}</div>
            </template>
          </el-table-column>
          <el-table-column label="开始时间" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">{{ formatDateTime(row.started_at) }}</template>
          </el-table-column>
          <el-table-column label="持续时间" min-width="120" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.closed_at">{{ formatDuration(row.duration_seconds) }}</span>
              <el-tag v-else size="small" type="success" effect="plain">连接中</el-tag>
            </template>
          </el-table-column>
        </el-table>

        <div class="pagination-bar">
          <el-pagination
            v-model:current-page="query.page"
            v-model:page-size="query.pageSize"
            :total="query.total"
            :page-sizes="[10, 20, 50, 100]"
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
.devices-content {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.metric-strip {
  flex: none;
  margin-bottom: 0;
}
.panel-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 14px 18px;
  border-bottom: 1px solid var(--sb-line);
}
.panel-title {
  font-size: 15px;
  color: var(--sb-text-title, inherit);
}
.history-toolbar {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  padding: 12px 18px;
  border-bottom: 1px solid var(--sb-line);
}
.device-name {
  font-weight: 600;
  color: var(--el-color-primary);
}
.clickable {
  cursor: pointer;
}
.clickable:hover {
  text-decoration: underline;
}
.font-bold {
  font-weight: 700;
}
.font-medium {
  font-weight: 500;
}
</style>
