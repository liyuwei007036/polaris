<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { Clock, Filter, Refresh } from '@element-plus/icons-vue'
import { api } from '../api'
import { formatBytes, formatDateTime, localTimeZoneLabel } from '../format'
import PageHeader from '../components/PageHeader.vue'

const loadNodes = inject('loadNodes')
const navigate = inject('navigate')

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

function filterByIP(ip) {
  navigate?.('connection-history', `ip=${encodeURIComponent(ip)}`)
}

function filterByUser(userName) {
  navigate?.('connection-history', `user=${encodeURIComponent(userName)}`)
}

function rankTagType(index) {
  if (index === 0) return 'danger'
  if (index === 1) return 'warning'
  if (index === 2) return 'success'
  return 'info'
}

async function refreshAll() {
  await Promise.all([loadNodes?.().catch(() => {}), loadPopular()])
}

onMounted(() => {
  refreshAll()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="热门设备">
      <span class="subtle" style="margin-right: 12px">数据保留最近 30 天 · {{ timeZoneLabel }}</span>
      <el-button :icon="Clock" @click="navigate('connection-history')">历史连接明细</el-button>
      <el-button :icon="Refresh" :loading="popularLoading" @click="refreshAll">刷新</el-button>
    </PageHeader>

    <main class="page-content devices-content">
      <!-- 汇总指标卡片 -->
      <section class="metric-strip">
        <div class="metric">
          <div class="metric__label">历史连接数</div>
          <div class="metric__value">{{ summary.total_connections.toLocaleString() }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">热门设备数 (独立来源 IP)</div>
          <div class="metric__value">{{ summary.unique_ips }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">累计下行流量</div>
          <div class="metric__value">{{ formatBytes(summary.total_download) }}</div>
        </div>
        <div class="metric">
          <div class="metric__label">累计上行流量</div>
          <div class="metric__value">{{ formatBytes(summary.total_upload) }}</div>
        </div>
      </section>

      <!-- 热门设备排行（按来源 IP） -->
      <div class="table-panel">
        <div class="panel-header">
          <div class="panel-title">
            <strong>热门设备排行（来源 IP）</strong>
            <span class="subtle" style="margin-left: 8px">按客户端来源 IP 聚合连接频次与流量</span>
          </div>
          <div class="panel-actions">
            <el-radio-group v-model="popularRange" size="small" @change="loadPopular">
              <el-radio-button label="24h">最近 24 小时</el-radio-button>
              <el-radio-button label="7d">最近 7 天</el-radio-button>
              <el-radio-button label="30d">最近 30 天</el-radio-button>
            </el-radio-group>
            <el-button link type="primary" :icon="Clock" style="margin-left: 14px" @click="navigate('connection-history')">
              查看全部历史明细 &rarr;
            </el-button>
          </div>
        </div>

        <el-table v-loading="popularLoading" :data="popularDevices" empty-text="当前周期内暂无热门设备记录">
          <el-table-column label="排名" width="70" align="center">
            <template #default="{ $index }">
              <el-tag :type="rankTagType($index)" size="small" effect="dark" round>
                #{{ $index + 1 }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="来源 IP（设备）" min-width="180" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="mono font-bold clickable device-ip" @click="filterByIP(row.source_ip)">
                {{ row.source_ip }}
              </div>
            </template>
          </el-table-column>
          <el-table-column label="归属地" min-width="160" show-overflow-tooltip>
            <template #default="{ row }">
              <span>{{ row.source_location || '未知归属地' }}</span>
            </template>
          </el-table-column>
          <el-table-column label="接入账号" min-width="130" show-overflow-tooltip>
            <template #default="{ row }">
              <span v-if="row.user" class="clickable" @click="filterByUser(row.user)">{{ row.user }}</span>
              <span v-else class="subtle">—</span>
            </template>
          </el-table-column>
          <el-table-column label="连接次数" min-width="110" align="right">
            <template #default="{ row }">
              <strong>{{ row.connection_count.toLocaleString() }}</strong> 次
            </template>
          </el-table-column>
          <el-table-column label="总流量" min-width="150" align="right">
            <template #default="{ row }">
              <div class="mono font-bold">{{ formatBytes(row.total_bytes) }}</div>
              <div class="mono subtle" style="font-size: 11px">↓{{ formatBytes(row.download) }} ↑{{ formatBytes(row.upload) }}</div>
            </template>
          </el-table-column>
          <el-table-column label="最近活跃" min-width="170" show-overflow-tooltip>
            <template #default="{ row }">{{ formatDateTime(row.last_seen_at) }}</template>
          </el-table-column>
          <el-table-column label="操作" width="130" align="center">
            <template #default="{ row }">
              <el-button link type="primary" size="small" :icon="Filter" @click="filterByIP(row.source_ip)">
                查此 IP 连接
              </el-button>
            </template>
          </el-table-column>
        </el-table>
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
.panel-actions {
  display: flex;
  align-items: center;
}
.device-ip {
  color: var(--el-color-primary);
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
</style>
