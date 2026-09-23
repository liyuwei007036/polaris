<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { Filter, Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../../api'
import { formatBytes, formatDateTime, formatDuration } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MPicker from '../components/MPicker.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')

const tab = ref('popular')
const loading = ref(false)
const popularLoading = ref(false)

// 热门设备概览
const popularRange = ref('7d')
const summary = reactive({
  total_connections: 0,
  unique_devices: 0,
  unique_ips: 0,
  total_upload: 0,
  total_download: 0,
})
const popularDevices = ref([])

// 历史连接查询
const filter = reactive({
  ip: '',
  node_id: '',
  keyword: '',
})
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const records = ref([])

const tabOptions = [
  { value: 'popular', label: '热门排行' },
  { value: 'history', label: '历史记录' },
]

const rangeOptions = [
  { value: '24h', label: '近 24 小时' },
  { value: '7d', label: '近 7 天' },
  { value: '30d', label: '近 30 天' },
]

const nodeOptions = computed(() => [
  { value: '', label: '全部服务器' },
  ...appState.nodes.map((n) => ({ value: n.id, label: n.name })),
])

async function loadPopular() {
  popularLoading.value = true
  try {
    const data = await api(`/devices/popular?range=${popularRange.value}&limit=20`)
    Object.assign(summary, data.summary || {})
    popularDevices.value = data.devices || []
  } catch (err) {
    console.error('Failed to load popular devices', err)
  } finally {
    popularLoading.value = false
  }
}

async function loadRecords(append = false) {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: String(page.value),
      page_size: String(pageSize.value),
    })
    if (filter.ip) params.set('ip', filter.ip.trim())
    if (filter.node_id) params.set('node_id', filter.node_id)
    if (filter.keyword) params.set('keyword', filter.keyword.trim())

    const data = await api(`/devices/connections?${params}`)
    const list = data.records || []
    total.value = data.total || 0
    if (append) {
      records.value = [...records.value, ...list]
    } else {
      records.value = list
    }
  } catch (err) {
    console.error('Failed to load connection records', err)
  } finally {
    loading.value = false
  }
}

function handleSearch() {
  page.value = 1
  loadRecords(false)
}

function loadMore() {
  if (records.value.length < total.value) {
    page.value += 1
    loadRecords(true)
  }
}

function filterByDeviceIP(ip) {
  filter.ip = ip
  tab.value = 'history'
  handleSearch()
}

onMounted(async () => {
  await loadNodes()
  loadPopular()
  loadRecords()
})
</script>

<template>
  <MPage :loading="loading && popularLoading">
    <MSegmented v-model="tab" :options="tabOptions" />

    <!-- 热门排行 TAB -->
    <div v-if="tab === 'popular'">
      <div class="m-filters" style="margin-bottom: 12px">
        <MPicker v-model="popularRange" chip :options="rangeOptions" title="时间范围" @update:model-value="loadPopular" />
      </div>

      <!-- 概览指标卡 -->
      <div class="m-stats-card">
        <div class="m-stat-row">
          <div class="m-stat-col">
            <span class="m-stat-col__val">{{ summary.total_connections.toLocaleString() }}</span>
            <span class="m-stat-col__lbl">连接记录</span>
          </div>
          <div class="m-stat-col">
            <span class="m-stat-col__val">{{ summary.unique_devices }}</span>
            <span class="m-stat-col__lbl">活跃设备</span>
          </div>
          <div class="m-stat-col">
            <span class="m-stat-col__val">{{ summary.unique_ips }}</span>
            <span class="m-stat-col__lbl">来源 IP</span>
          </div>
          <div class="m-stat-col">
            <span class="m-stat-col__val">{{ formatBytes(summary.total_download + summary.total_upload) }}</span>
            <span class="m-stat-col__lbl">总流量</span>
          </div>
        </div>
      </div>

      <div class="m-count">{{ popularDevices.length }} 个设备 · 最多保留 30 天数据</div>

      <article v-for="(row, index) in popularDevices" :key="index" class="m-item">
        <div class="m-item__hit" style="cursor: default">
          <div class="m-item__head">
            <div class="m-rank-title">
              <span class="m-pill" :class="index < 3 ? 'm-pill--warning' : 'm-pill--info'">#{{ index + 1 }}</span>
              <span class="m-item__title" style="margin-left: 6px">{{ row.user || '未知设备' }}</span>
            </div>
            <el-button size="small" link type="primary" :icon="Filter" @click="filterByDeviceIP(row.source_ip)">
              查连接
            </el-button>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ row.connection_count.toLocaleString() }} 次</b><small>连接次数</small></span>
            <span class="m-stat"><b>{{ formatBytes(row.total_bytes) }}</b><small>累计流量</small></span>
          </div>
          <div class="m-item__meta">
            {{ row.source_ip }} {{ row.source_location ? `· ${row.source_location}` : '' }} · {{ formatDateTime(row.last_seen_at) }}
          </div>
        </div>
      </article>
      <div v-if="popularDevices.length === 0" class="m-empty">暂无热门设备数据</div>
    </div>

    <!-- 历史记录 TAB -->
    <div v-if="tab === 'history'">
      <div class="m-listbar">
        <el-input v-model="filter.ip" clearable :prefix-icon="Search" placeholder="按来源 IP 筛选" @change="handleSearch" />
        <div class="m-filters">
          <MPicker v-model="filter.node_id" chip :options="nodeOptions" title="按服务器筛选" placeholder="全部服务器" @update:model-value="handleSearch" />
          <el-input v-model="filter.keyword" clearable placeholder="搜索域名/目标" style="flex: 1" @change="handleSearch" />
        </div>
      </div>

      <div class="m-count">{{ total }} 条历史记录</div>

      <article v-for="row in records" :key="row.id" class="m-item">
        <div class="m-item__hit" style="cursor: default">
          <div class="m-item__head">
            <span class="m-item__title m-item__title--mono">{{ row.host || row.destination }}</span>
            <span class="m-pill m-pill--info">{{ (row.network || 'TCP').toUpperCase() }}</span>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>↓ {{ formatBytes(row.download) }}</b><small>下行流量</small></span>
            <span class="m-stat"><b>↑ {{ formatBytes(row.upload) }}</b><small>上行流量</small></span>
          </div>
          <div class="m-item__meta">
            {{ row.user ? `${row.user} · ` : '' }}{{ row.source_ip }} · {{ row.node_name || row.node_id }} · {{ formatDateTime(row.started_at) }}
            <span v-if="row.closed_at">({{ formatDuration(row.duration_seconds) }})</span>
          </div>
        </div>
      </article>

      <div v-if="records.length === 0 && !loading" class="m-empty">未找到匹配的历史连接记录</div>

      <div v-if="records.length < total" style="padding: 16px 0; text-align: center">
        <el-button :loading="loading" round @click="loadMore">加载更多 ({{ records.length }}/{{ total }})</el-button>
      </div>
    </div>
  </MPage>
</template>

<style scoped>
.m-stats-card {
  padding: 14px 16px;
  margin-bottom: 12px;
  background: var(--sb-panel);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
}
.m-stat-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  text-align: center;
}
.m-stat-col {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.m-stat-col__val {
  font-size: 15px;
  font-weight: 700;
  color: var(--sb-text-title, inherit);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.m-stat-col__lbl {
  font-size: 11px;
  color: var(--sb-text-subtle, #8496b0);
}
.m-rank-title {
  display: flex;
  align-items: center;
}
.m-empty {
  padding: 40px 0;
  text-align: center;
  color: var(--sb-text-subtle, #8496b0);
}
</style>
