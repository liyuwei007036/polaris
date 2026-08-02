<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { Refresh } from '@element-plus/icons-vue'
import { api } from '../api'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('tasks')
const tasks = ref([])
const events = ref([])
const taskQuery = reactive({ page: 1, pageSize: 20, total: 0, status: '' })
const auditQuery = reactive({ page: 1, pageSize: 20, total: 0 })
const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const taskKinds = {
  'singbox.apply_config': '更新连接服务配置',
  'singbox.install': '安装连接服务',
  'singbox.upgrade': '升级连接服务',
  'firewall.apply': '更新访问限制',
  'fail2ban.apply': '更新自动封禁设置',
  'nginx.apply_config': '更新接入端口分配',
  'outbound.test': '检测上网出口',
}
const taskStatuses = {
  queued: ['等待处理', 'info'], dispatched: ['正在处理', 'warning'],
  succeeded: ['已完成', 'success'], failed: ['未完成', 'danger'], rolled_back: ['已恢复原配置', 'warning'],
}
const targetNames = {
  operator: '管理账户', certificate: '加密证书', reality_key: 'Reality 连接密钥', registration_token: '服务器接入信息',
  subscription: '客户端更新地址', node: '服务器', registration: '服务器接入申请', firewall_rule: '访问限制',
  singbox_release: '连接服务版本', listener: '接入服务', outbound: '上网出口', endpoint: '接入用户',
  ingress_route: '接入端口分配', route_rule: '服务器访问规则', task: '系统操作', cloudflare: '域名解析设置',
  cloudflare_record: '域名解析记录', fail2ban_jail: '自动封禁规则', mihomo_proxy_group: '代理分组',
  mihomo_routing_profile: '客户端访问规则', mihomo_client_config: '客户端配置',
}
const actionNames = {
  created: '已创建', updated: '已修改', deleted: '已删除', replaced: '已更换', state_changed: '状态已修改',
  password_reset: '密码已重置', mfa_reset: '两步验证已重置', token_rotated: '更新地址已更换',
  publish_requested: '已请求应用', install_requested: '已请求安装', test_requested: '已请求检测',
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
  if (row.result_summary && /[\u3400-\u9fff]/.test(row.result_summary)) return row.result_summary
  if (row.status === 'succeeded') return '操作已完成'
  if (row.status === 'rolled_back') return '新设置未能生效，系统已恢复原有配置'
  if (row.status === 'failed') return '操作未完成，请检查服务器状态和相关日志'
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
function resetTaskPage() { taskQuery.page = 1; loadTasks() }
onMounted(() => { loadTasks(); loadAudit() })
</script>

<template>
  <div class="page-shell">
    <PageHeader title="操作记录" description="查看系统自动执行的操作和管理员修改记录">
      <el-button :icon="Refresh" @click="tab === 'tasks' ? loadTasks() : loadAudit()">刷新</el-button>
    </PageHeader>
    <main class="page-content">
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="系统操作" name="tasks">
            <div class="tab-actions tab-actions--start">
              <el-select v-model="taskQuery.status" clearable placeholder="全部状态" style="width: 150px" @change="resetTaskPage">
                <el-option v-for="(item, value) in taskStatuses" :key="value" :label="item[0]" :value="value" />
              </el-select>
            </div>
            <el-table v-loading="loading" :data="tasks">
              <el-table-column label="操作内容" min-width="190"><template #default="{ row }"><strong>{{ taskKinds[row.kind] || '其他系统操作' }}</strong></template></el-table-column>
              <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
              <el-table-column label="状态" width="110"><template #default="{ row }"><el-tag :type="taskStatuses[row.status]?.[1] || 'info'">{{ taskStatuses[row.status]?.[0] || row.status }}</el-tag></template></el-table-column>
              <el-table-column label="执行结果" min-width="300"><template #default="{ row }">{{ taskResult(row) }}</template></el-table-column>
              <el-table-column label="开始时间" prop="created_at" width="190" />
            </el-table>
            <div class="pagination-bar"><el-pagination v-model:current-page="taskQuery.page" v-model:page-size="taskQuery.pageSize" :total="taskQuery.total" :page-sizes="[20, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadTasks" /></div>
          </el-tab-pane>
          <el-tab-pane v-if="isAdmin" label="管理员修改记录" name="audit">
            <el-table v-loading="loading" :data="events">
              <el-table-column label="操作人" prop="operator_username" min-width="180" />
              <el-table-column label="修改内容" min-width="210"><template #default="{ row }">{{ auditAction(row) }}</template></el-table-column>
              <el-table-column label="修改对象" min-width="260"><template #default="{ row }">{{ auditTarget(row) }}</template></el-table-column>
              <el-table-column label="时间" prop="created_at" width="190" />
            </el-table>
            <div class="pagination-bar"><el-pagination v-model:current-page="auditQuery.page" v-model:page-size="auditQuery.pageSize" :total="auditQuery.total" :page-sizes="[20, 50, 100]" layout="total, sizes, prev, pager, next" @change="loadAudit" /></div>
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>
  </div>
</template>
