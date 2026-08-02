<script setup>
import { computed, inject, onBeforeUnmount, onMounted, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { api } from '../api'
import { subscribeLive } from '../live'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const loadNodes = inject('loadNodes')
const navigate = inject('navigate')
const loading = ref(false)
const listeners = ref([])
const tasks = ref([])
const metrics = ref({})
const rates = ref({})
const previousCounters = new Map()
const now = ref(Date.now())
const refreshing = ref(false)
let stopLive

const online = computed(() => appState.nodes.filter((node) => node.online).length)
const failed = computed(() => tasks.value.filter((task) => task.status === 'failed').length)

function formatBytes(value) {
  let number = Number(value || 0)
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let index = 0
  while (number >= 1024 && index < units.length - 1) {
    number /= 1024
    index += 1
  }
  return `${index ? number.toFixed(number < 10 ? 2 : 1) : Math.round(number)} ${units[index]}`
}

function formatRate(value) {
  return `${formatBytes(value)}/s`
}

function relativeTime(value) {
  const seconds = Math.max(0, Math.floor((now.value - Date.parse(value)) / 1000))
  if (!Number.isFinite(seconds)) return '从未'
  if (seconds < 5) return '刚刚'
  if (seconds < 60) return `${seconds} 秒前`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  return `${Math.floor(seconds / 3600)} 小时前`
}

function healthInfo(node) {
  if (!node.online) return ['离线', 'info']
  return {
    healthy: ['正常', 'success'],
    degraded: ['部分不可用', 'warning'],
    stopped: ['连接服务已停止', 'danger'],
  }[metrics.value[node.id]?.health?.status] || ['等待检测', 'info']
}

function updateRates(nodeID, report) {
  if (!report?.node || !report.collected_at) return
  const current = {
    received: Number(report.node.received_bytes || 0),
    sent: Number(report.node.sent_bytes || 0),
    time: Date.parse(report.collected_at),
  }
  const previous = previousCounters.get(nodeID)
  if (previous && current.time > previous.time) {
    const seconds = (current.time - previous.time) / 1000
    rates.value[nodeID] = {
      received: Math.max(0, current.received - previous.received) / seconds,
      sent: Math.max(0, current.sent - previous.sent) / seconds,
    }
  }
  previousCounters.set(nodeID, current)
}

function taskStatus(status) {
  return {
    queued: ['等待处理', 'info'],
    dispatched: ['正在处理', 'warning'],
    succeeded: ['已完成', 'success'],
    failed: ['未完成', 'danger'],
    rolled_back: ['已恢复原配置', 'warning'],
  }[status] || [status, 'info']
}

function taskKind(kind) {
  return {
    'singbox.apply_config': '更新连接服务配置',
    'singbox.install': '安装连接服务',
    'singbox.upgrade': '升级连接服务',
    'firewall.apply': '更新访问限制',
    'fail2ban.apply': '更新自动封禁设置',
    'nginx.apply_config': '更新端口共享配置',
    'outbound.test': '检测上网出口',
  }[kind] || '其他系统操作'
}

function taskResult(task) {
  if (task.result_summary && /[\u3400-\u9fff]/.test(task.result_summary)) return task.result_summary
  if (task.status === 'succeeded') return '操作已完成'
  if (task.status === 'rolled_back') return '已恢复原有配置'
  if (task.status === 'failed') return '操作未完成，请查看操作记录'
  return '等待处理'
}

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  now.value = Date.now()
  try {
    await loadNodes()
    const [listenerResult, taskResult] = await Promise.all([
      api('/listeners').catch(() => ({ listeners: [] })),
      api('/tasks').catch(() => ({ tasks: [] })),
    ])
    listeners.value = listenerResult.listeners || []
    tasks.value = taskResult.tasks || []
    const metricPairs = await Promise.all(
      appState.nodes.map(async (node) => {
        const result = await api(`/nodes/${node.id}/metrics`).catch(() => null)
        return [node.id, result?.report || null]
      }),
    )
    metrics.value = Object.fromEntries(metricPairs)
    metricPairs.forEach(([nodeID, report]) => updateRates(nodeID, report))
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

onMounted(() => {
  load()
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node' || event.kind === 'connections' || event.kind === 'task') load(true).catch(() => {})
  })
})

onBeforeUnmount(() => {
  stopLive?.()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="运行概览" description="查看全部服务器的运行状态、网络用量和最近操作">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <el-alert
        v-if="appState.nodes.length - online > 0"
        :title="`${appState.nodes.length - online} 台服务器离线，恢复连接前无法接收新的配置`"
        type="warning"
        show-icon
        :closable="false"
        style="margin-bottom: 16px"
      />
      <el-alert
        v-if="failed"
        :title="`最近有 ${failed} 项操作未完成，请到“操作记录”查看原因`"
        type="error"
        show-icon
        :closable="false"
        style="margin-bottom: 16px"
      />

      <section class="metric-strip">
        <div class="metric"><div class="metric__label">服务器总数</div><div class="metric__value">{{ appState.nodes.length }}</div></div>
        <div class="metric"><div class="metric__label">在线服务器</div><div class="metric__value">{{ online }} / {{ appState.nodes.length }}</div></div>
        <div class="metric"><div class="metric__label">接入服务</div><div class="metric__value">{{ listeners.length }}</div></div>
        <div class="metric"><div class="metric__label">未完成操作</div><div class="metric__value">{{ failed }}</div></div>
      </section>

      <div class="toolbar">
        <h3 class="section-title" style="margin: 0">服务器状态</h3>
        <span class="toolbar__spacer" />
        <el-button link type="primary" @click="navigate('nodes')">管理服务器</el-button>
      </div>
      <div class="table-panel">
        <el-table :data="appState.nodes">
          <el-table-column label="服务器" min-width="180">
            <template #default="{ row }">
              <span class="status-dot" :class="row.online ? 'online' : 'offline'" />
              <strong>{{ row.name }}</strong>
            </template>
          </el-table-column>
          <el-table-column label="操作系统" min-width="150">
            <template #default="{ row }">{{ row.os || '—' }} · {{ row.architecture || '—' }}</template>
          </el-table-column>
          <el-table-column label="连接服务版本" width="130" prop="sing_box_version" />
          <el-table-column label="运行状态" width="135">
            <template #default="{ row }"><el-tag :type="healthInfo(row)[1]">{{ healthInfo(row)[0] }}</el-tag></template>
          </el-table-column>
          <el-table-column label="当前下载速度" width="125">
            <template #default="{ row }">{{ rates[row.id] ? formatRate(rates[row.id].received) : '正在计算' }}</template>
          </el-table-column>
          <el-table-column label="当前上传速度" width="125">
            <template #default="{ row }">{{ rates[row.id] ? formatRate(rates[row.id].sent) : '正在计算' }}</template>
          </el-table-column>
          <el-table-column label="累计下载" width="140">
            <template #default="{ row }">{{ metrics[row.id]?.node ? formatBytes(metrics[row.id].node.received_bytes) : '等待上报' }}</template>
          </el-table-column>
          <el-table-column label="累计上传" width="140">
            <template #default="{ row }">{{ metrics[row.id]?.node ? formatBytes(metrics[row.id].node.sent_bytes) : '等待上报' }}</template>
          </el-table-column>
          <el-table-column label="当前连接" width="110">
            <template #default="{ row }">{{ metrics[row.id]?.connections?.length ?? '—' }}</template>
          </el-table-column>
          <el-table-column label="最后在线" width="120"><template #default="{ row }">{{ relativeTime(row.last_seen_at) }}</template></el-table-column>
        </el-table>
      </div>
      <p class="subtle">这里显示服务器全部网络接口的总用量，不代表某个接入服务或用户的单独用量。</p>

      <div class="toolbar" style="margin-top: 24px">
        <h3 class="section-title" style="margin: 0">最近操作</h3>
        <span class="toolbar__spacer" />
        <el-button link type="primary" @click="navigate('audit')">查看全部</el-button>
      </div>
      <div class="table-panel">
        <el-table :data="tasks.slice(0, 8)">
          <el-table-column label="操作内容" min-width="180"><template #default="{ row }">{{ taskKind(row.kind) }}</template></el-table-column>
          <el-table-column label="服务器" min-width="150">
            <template #default="{ row }">{{ appState.nodes.find((node) => node.id === row.node_id)?.name || row.node_id?.slice(0, 8) }}</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }"><el-tag :type="taskStatus(row.status)[1]">{{ taskStatus(row.status)[0] }}</el-tag></template>
          </el-table-column>
          <el-table-column label="执行结果" min-width="260" show-overflow-tooltip><template #default="{ row }">{{ taskResult(row) }}</template></el-table-column>
          <el-table-column label="开始时间" width="180" prop="created_at" />
        </el-table>
      </div>
    </main>
  </div>
</template>
