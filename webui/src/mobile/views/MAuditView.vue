<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { Refresh, Search } from '@element-plus/icons-vue'
import { api } from '../../api'
import { formatDateTime, includesText, localTimeZoneLabel } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MPicker from '../components/MPicker.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('tasks')
const tasks = ref([])
const events = ref([])
const taskQuery = reactive({ page: 1, pageSize: 20, total: 0, status: '' })
const auditQuery = reactive({ page: 1, pageSize: 20, total: 0 })
const taskKeyword = ref('')
const auditKeyword = ref('')
const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const timeZoneLabel = localTimeZoneLabel()
const taskKinds = {
  'singbox.apply_config': '更新连接服务配置',
  'singbox.install': '安装连接服务',
  'singbox.upgrade': '升级连接服务',
  'firewall.query': '读取访问限制',
  'firewall.mutate': '修改访问限制',
  'fail2ban.query': '读取自动封禁设置',
  'fail2ban.mutate': '修改自动封禁设置',
  'nginx.apply_config': '更新接入端口分配',
  'outbound.test': '检测上网出口',
  'fail2ban.unban': '解封 IP',
}
const taskStatuses = {
  queued: ['等待处理', 'm-pill--info'], dispatched: ['正在处理', 'm-pill--warning'],
  succeeded: ['已完成', 'm-pill--success'], failed: ['未完成', 'm-pill--danger'], rolled_back: ['已恢复原配置', 'm-pill--warning'],
}
const statusOptions = [{ value: '', label: '全部状态' }, ...Object.entries(taskStatuses).map(([value, item]) => ({ value, label: item[0] }))]
const filteredTasks = computed(() => tasks.value.filter((row) => includesText([taskKinds[row.kind], nodeNames.value[row.node_id], taskStatuses[row.status]?.[0], taskResult(row)], taskKeyword.value)))
const filteredEvents = computed(() => events.value.filter((row) => includesText([row.operator_username, auditAction(row), auditTarget(row)], auditKeyword.value)))
const targetNames = {
  operator: '管理账户', reality_key: 'Reality 连接密钥', registration_token: '服务器接入信息',
  subscription: '客户端更新地址', node: '服务器', registration: '服务器接入申请', firewall_rule: '访问限制',
  singbox_release: 'sing-box 版本', listener: '接入服务', outbound: '上网出口', endpoint: '接入用户',
  ingress_route: '接入端口分配', route_rule: '服务器访问规则', task: '系统操作', cloudflare: '域名解析设置',
  cloudflare_record: '域名解析记录', fail2ban_jail: '自动封禁规则', mihomo_proxy_group: '代理分组',
  mihomo_routing_profile: '客户端访问规则', mihomo_client_config: '客户端配置',
  mihomo_rule_provider: '规则供应商',
}
const actionNames = {
  created: '已创建', updated: '已修改', deleted: '已删除', replaced: '已更换', state_changed: '状态已修改',
  password_reset: '密码已重置', mfa_reset: '两步验证已重置', token_rotated: '更新地址已更换',
  publish_requested: '已请求应用', install_requested: '已请求安装', test_requested: '已请求检测',
  rule_add: '已在服务器上添加', rule_delete: '已从服务器上删除',
  jail_save: '已在服务器上保存', jail_delete: '已从服务器上删除', unbanned: '已解封',
  priority_changed: '优先级已修改', registration_approved: '已允许接入', certificate_revoked: '已移除',
  published: '已发布', synced: '已同步', settings_updated: '设置已修改',
}

function auditAction(row) {
  const key = Object.keys(actionNames).sort((a, b) => b.length - a.length).find((item) => row.action?.endsWith(item))
  if (row.action?.startsWith('task.completed.')) return row.action.endsWith('succeeded') ? '系统操作已完成' : '系统操作未完成'
  return `${targetNames[row.target_type] || '系统设置'}${actionNames[key] || '已变更'}`
}

function auditTarget(row) {
  const name = targetNames[row.target_type] || '系统对象'
  return row.target_id ? `${name} · ${row.target_id}` : name
}

function taskResult(row) {
  if (row.result_summary && /[㐀-鿿]/.test(row.result_summary)) return row.result_summary
  if (row.status === 'succeeded') return '操作已完成'
  // 失败摘要里带的是 systemd 和 sing-box 的原文，所以是英文。丢掉它，
  // 端口被占用、证书被拒、服务单元缺失就会显示成同一句话。
  const detail = (row.result_summary || '').trim()
  if (row.status === 'rolled_back') {
    return detail ? `新设置未能生效，系统已恢复原有配置：${detail}` : '新设置未能生效，系统已恢复原有配置'
  }
  if (row.status === 'failed') {
    return detail ? `操作未完成：${detail}` : '操作未完成，请检查服务器状态和相关日志'
  }
  return '等待处理'
}

async function loadTasks() {
  loading.value = true
  try {
    const query = new URLSearchParams({ page: taskQuery.page, page_size: taskQuery.pageSize })
    if (taskQuery.status) query.set('status', taskQuery.status)
    const result = await api(`/tasks?${query}`)
    tasks.value = result.tasks || []
    taskQuery.total = result.total || 0
  } finally { loading.value = false }
}

async function loadAudit() {
  if (!isAdmin.value) return
  loading.value = true
  try {
    const result = await api(`/audit-events?page=${auditQuery.page}&page_size=${auditQuery.pageSize}`)
    events.value = result.audit_events || []
    auditQuery.total = result.total || 0
  } finally { loading.value = false }
}

function changeStatus() {
  taskQuery.page = 1
  loadTasks()
}

function turnPage(query, loader, offset) {
  query.page += offset
  loader()
}

const tabs = computed(() => (isAdmin.value
  ? [{ value: 'tasks', label: '系统操作' }, { value: 'audit', label: '管理员修改记录' }]
  : [{ value: 'tasks', label: '系统操作' }]))

onMounted(() => { loadTasks(); loadAudit() })
</script>

<template>
  <MPage title="操作记录" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="tab === 'tasks' ? loadTasks() : loadAudit()" />
    </template>

    <MSegmented v-model="tab" :options="tabs" />
    <div class="m-count">保留最近 7 天 · 时间按 {{ timeZoneLabel }} 显示</div>

    <template v-if="tab === 'tasks'">
      <el-input v-model="taskKeyword" clearable :prefix-icon="Search" placeholder="搜索操作、服务器或结果" />
      <div class="m-filters">
        <MPicker :model-value="taskQuery.status" chip :options="statusOptions" title="按状态筛选" placeholder="全部状态" @update:model-value="taskQuery.status = $event; changeStatus()" />
      </div>

      <article v-for="row in filteredTasks" :key="row.id" class="m-item">
        <div class="m-item__hit is-static">
          <div class="m-item__head">
            <span class="m-item__title">{{ taskKinds[row.kind] || '其他系统操作' }}</span>
            <span class="m-pill" :class="taskStatuses[row.status]?.[1] || 'm-pill--info'">{{ taskStatuses[row.status]?.[0] || row.status }}</span>
          </div>
          <div class="m-item__meta">{{ nodeNames[row.node_id] || row.node_id || '控制端' }} · {{ formatDateTime(row.created_at) }}</div>
          <div class="m-item__note">{{ taskResult(row) }}</div>
        </div>
      </article>
      <div v-if="!filteredTasks.length && !loading" class="m-empty">没有系统操作记录</div>

      <div class="pager">
        <el-button :disabled="taskQuery.page <= 1" @click="turnPage(taskQuery, loadTasks, -1)">上一页</el-button>
        <span class="pager__label">第 {{ taskQuery.page }} 页 / 共 {{ taskQuery.total }} 条</span>
        <el-button :disabled="taskQuery.page * taskQuery.pageSize >= taskQuery.total" @click="turnPage(taskQuery, loadTasks, 1)">下一页</el-button>
      </div>
    </template>

    <template v-else>
      <el-input v-model="auditKeyword" clearable :prefix-icon="Search" placeholder="搜索操作人、内容或对象" />
      <article v-for="row in filteredEvents" :key="row.id" class="m-item">
        <div class="m-item__hit is-static">
          <div class="m-item__head">
            <span class="m-item__title">{{ auditAction(row) }}</span>
          </div>
          <div class="m-item__meta">{{ row.operator_username }} · {{ formatDateTime(row.created_at) }}</div>
          <div class="m-item__note">{{ auditTarget(row) }}</div>
        </div>
      </article>
      <div v-if="!filteredEvents.length && !loading" class="m-empty">没有修改记录</div>

      <div class="pager">
        <el-button :disabled="auditQuery.page <= 1" @click="turnPage(auditQuery, loadAudit, -1)">上一页</el-button>
        <span class="pager__label">第 {{ auditQuery.page }} 页 / 共 {{ auditQuery.total }} 条</span>
        <el-button :disabled="auditQuery.page * auditQuery.pageSize >= auditQuery.total" @click="turnPage(auditQuery, loadAudit, 1)">下一页</el-button>
      </div>
    </template>
  </MPage>
</template>

<style scoped>
.pager { display: flex; align-items: center; gap: 10px; margin-top: 14px; }
.pager :deep(.el-button) { flex: none; height: var(--m-tap); margin: 0; }
.pager__label { flex: 1; color: var(--sb-muted); font-size: 12px; text-align: center; }
</style>
