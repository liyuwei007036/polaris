<script setup>
import { computed, inject, onMounted, reactive, ref, watch } from 'vue'
import { DocumentCopy, Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api } from '../api'
import { formatDateTime, localTimeZoneLabel } from '../format'
import PageHeader from '../components/PageHeader.vue'
import DDNSHistoryTable from '../components/DDNSHistoryTable.vue'
import {
  AUDIT_MODULES, TARGET_NAMES, TASK_KINDS, STATUS_MAP,
  getModuleInfo, formatAuditTitle, parseJsonDetail
} from '../audit_helpers'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const tab = ref('tasks') // 'tasks' | 'audit'

// Tasks State
const tasks = ref([])
const taskKeyword = ref('')
const selectedNodeId = ref('')
const selectedTaskStatus = ref('')
const taskQuery = reactive({ page: 1, pageSize: 15, total: 0 })

// Audit Events State
const events = ref([])
const auditKeyword = ref('')
const selectedModule = ref('')
const selectedAuditStatus = ref('')
const auditQuery = reactive({ page: 1, pageSize: 15, total: 0 })

// Dialog State
const detailDialogVisible = ref(false)
const activeRecord = ref(null)
const activeType = ref('task') // 'task' | 'audit'

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const timeZoneLabel = localTimeZoneLabel()

async function loadTasks() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: taskQuery.page,
      page_size: taskQuery.pageSize,
    })
    if (taskKeyword.value.trim()) params.set('keyword', taskKeyword.value.trim())
    if (selectedNodeId.value) params.set('node_id', selectedNodeId.value)
    if (selectedTaskStatus.value) params.set('status', selectedTaskStatus.value)

    const result = await api(`/tasks?${params.toString()}`)
    tasks.value = result.tasks || []
    taskQuery.total = result.total || 0
  } finally {
    loading.value = false
  }
}

async function loadAudit() {
  if (isAdmin.value === false && appState.role && appState.role !== 'admin') return
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: auditQuery.page,
      page_size: auditQuery.pageSize,
    })
    if (auditKeyword.value.trim()) params.set('keyword', auditKeyword.value.trim())
    if (selectedModule.value) params.set('target_type', selectedModule.value)
    if (selectedAuditStatus.value) params.set('status', selectedAuditStatus.value)

    const result = await api(`/audit-events?${params.toString()}`)
    events.value = result.audit_events || []
    auditQuery.total = result.total || 0
  } catch {
    events.value = []
  } finally {
    loading.value = false
  }
}

const ddnsHistoryRef = ref(null)

function refreshCurrent() {
  if (tab.value === 'tasks') loadTasks()
  else if (tab.value === 'audit') loadAudit()
  else if (ddnsHistoryRef.value) ddnsHistoryRef.value.refresh()
}

// 搜索防抖
let debounceTimer = null
watch([taskKeyword, selectedNodeId, selectedTaskStatus], () => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    taskQuery.page = 1
    loadTasks()
  }, 250)
})

watch([auditKeyword, selectedModule, selectedAuditStatus], () => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    auditQuery.page = 1
    loadAudit()
  }, 250)
})

watch(tab, () => {
  refreshCurrent()
})

watch(() => appState.role, (newRole) => {
  if (newRole) refreshCurrent()
})

watch(() => isAdmin.value, (val) => {
  if (val) refreshCurrent()
})

function copyText(text) {
  if (!text) return
  navigator.clipboard?.writeText(text).then(() => {
    ElMessage.success('已复制到剪贴板')
  }).catch(() => {
    ElMessage.info('复制失败，请手动选取复制')
  })
}

function openDetail(row, type) {
  activeRecord.value = row
  activeType.value = type
  detailDialogVisible.value = true
}

onMounted(async () => {
  await loadNodes?.().catch(() => {})
  refreshCurrent()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="操作记录">
      <span class="subtle" style="margin-right: 12px">保留最近 7 天 · {{ timeZoneLabel }}</span>
      <el-button :icon="Refresh" :loading="loading" @click="refreshCurrent">刷新</el-button>
    </PageHeader>

    <main class="page-content page-content--tight">
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <!-- 1. 系统操作选项卡 -->
          <el-tab-pane :label="`系统操作（${taskQuery.total}）`" name="tasks">
            <div class="tab-actions tab-actions--start">
              <el-input
                v-model="taskKeyword"
                clearable
                :prefix-icon="Search"
                placeholder="搜索操作、服务器或执行结果"
                style="width: 260px"
              />
              <el-select
                v-model="selectedNodeId"
                clearable
                placeholder="全部服务器"
                style="width: 180px"
                @change="taskQuery.page = 1; loadTasks()"
              >
                <el-option
                  v-for="node in appState.nodes"
                  :key="node.id"
                  :label="node.name"
                  :value="node.id"
                />
              </el-select>
              <el-select
                v-model="selectedTaskStatus"
                clearable
                placeholder="全部状态"
                style="width: 140px"
                @change="taskQuery.page = 1; loadTasks()"
              >
                <el-option label="执行成功" value="succeeded" />
                <el-option label="执行失败" value="failed" />
                <el-option label="已恢复原配置" value="rolled_back" />
                <el-option label="正在处理" value="dispatched" />
                <el-option label="等待处理" value="queued" />
              </el-select>
            </div>

            <el-table
              v-loading="loading"
              :data="tasks"
              :empty-text="loading ? '' : '还没有系统操作记录'"
            >
              <!-- 服务器 -->
              <el-table-column label="服务器" width="160" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="node-name-text">{{ row.node_name || nodeNames[row.node_id] || row.node_id }}</span>
                </template>
              </el-table-column>

              <!-- 操作内容 -->
              <el-table-column label="操作内容" width="190">
                <template #default="{ row }">
                  <div class="operation-title">
                    <strong>{{ row.kind_name || TASK_KINDS[row.kind] || row.kind }}</strong>
                  </div>
                  <div class="subtle mono font-mono">{{ row.kind }}</div>
                </template>
              </el-table-column>

              <!-- 状态与耗时 -->
              <el-table-column label="状态" width="120">
                <template #default="{ row }">
                  <el-tag :type="STATUS_MAP[row.status]?.type || 'info'" size="small">
                    {{ STATUS_MAP[row.status]?.label || row.status }}
                  </el-tag>
                  <div v-if="row.duration_ms" class="subtle mono tnum">{{ row.duration_ms }} ms</div>
                </template>
              </el-table-column>

              <!-- 执行结果与说明 -->
              <el-table-column label="执行结果与说明" min-width="260" show-overflow-tooltip>
                <template #default="{ row }">
                  <span
                    :class="{
                      'text-danger font-semibold': row.status === 'failed',
                      'text-warning font-semibold': row.status === 'rolled_back'
                    }"
                  >
                    {{ row.diagnostic || row.result_summary || (row.status === 'succeeded' ? '配置已生效' : '等待处理') }}
                  </span>
                </template>
              </el-table-column>

              <!-- 日期 (放在最后，不显示 xx 前) -->
              <el-table-column label="日期" width="170">
                <template #default="{ row }">
                  <span class="mono font-mono">{{ formatDateTime(row.created_at) }}</span>
                </template>
              </el-table-column>

              <!-- 操作 -->
              <el-table-column label="操作" width="84" class-name="action-column">
                <template #default="{ row }">
                  <el-button link @click="openDetail(row, 'task')">详情</el-button>
                </template>
              </el-table-column>
            </el-table>

            <div class="pagination-bar">
              <el-pagination
                v-model:current-page="taskQuery.page"
                v-model:page-size="taskQuery.pageSize"
                :total="taskQuery.total"
                :page-sizes="[15, 30, 50, 100]"
                background
                layout="total, sizes, prev, pager, next"
                @change="loadTasks"
              />
            </div>
          </el-tab-pane>

          <!-- 2. 管理员修改记录选项卡 -->
          <el-tab-pane v-if="isAdmin" :label="`管理员修改记录（${auditQuery.total}）`" name="audit">
            <div class="tab-actions tab-actions--start">
              <el-input
                v-model="auditKeyword"
                clearable
                :prefix-icon="Search"
                placeholder="搜索操作人、内容或对象"
                style="width: 260px"
              />
              <el-select
                v-model="selectedModule"
                clearable
                placeholder="全部业务模块"
                style="width: 160px"
                @change="auditQuery.page = 1; loadAudit()"
              >
                <el-option
                  v-for="mod in AUDIT_MODULES.filter(m => m.value)"
                  :key="mod.value"
                  :label="mod.label"
                  :value="mod.value"
                />
              </el-select>
              <el-select
                v-model="selectedAuditStatus"
                clearable
                placeholder="全部状态"
                style="width: 130px"
                @change="auditQuery.page = 1; loadAudit()"
              >
                <el-option label="执行成功" value="succeeded" />
                <el-option label="执行失败" value="failed" />
                <el-option label="已自动回滚" value="rolled_back" />
              </el-select>
            </div>

            <el-table
              v-loading="loading"
              :data="events"
              :empty-text="loading ? '' : '还没有管理员修改记录'"
            >
              <!-- IP / 归属地 (不需要操作人，只需要记录IP和归属地) -->
              <el-table-column label="IP / 归属地" width="200" show-overflow-tooltip>
                <template #default="{ row }">
                  <div class="mono font-mono">{{ row.ip_address || '127.0.0.1' }}</div>
                  <div class="subtle">{{ row.ip_location || (row.ip_address === '127.0.0.1' ? '本机控制台' : '未知') }}</div>
                </template>
              </el-table-column>

              <!-- 模块标签 -->
              <el-table-column label="模块" width="105">
                <template #default="{ row }">
                  <el-tag
                    size="small"
                    :type="getModuleInfo(row.target_type, row.action).type"
                  >
                    {{ getModuleInfo(row.target_type, row.action).name }}
                  </el-tag>
                </template>
              </el-table-column>

              <!-- 修改内容 -->
              <el-table-column label="修改内容" min-width="240">
                <template #default="{ row }">
                  <div class="action-summary-title">
                    {{ formatAuditTitle(row) }}
                  </div>
                </template>
              </el-table-column>

              <!-- 修改对象 (不需要 ID，不需要复制功能) -->
              <el-table-column label="修改对象" width="220" show-overflow-tooltip>
                <template #default="{ row }">
                  <span class="target-name">
                    {{ row.target_name || TARGET_NAMES[row.target_type] || row.target_type }}
                  </span>
                </template>
              </el-table-column>

              <!-- 状态 -->
              <el-table-column label="状态" width="90" align="center">
                <template #default="{ row }">
                  <el-tag :type="STATUS_MAP[row.status]?.type || 'success'" size="small">
                    {{ STATUS_MAP[row.status]?.label || '已完成' }}
                  </el-tag>
                </template>
              </el-table-column>

              <!-- 日期 (放在最后，不显示 xx 前) -->
              <el-table-column label="日期" width="170">
                <template #default="{ row }">
                  <span class="mono font-mono">{{ formatDateTime(row.created_at) }}</span>
                </template>
              </el-table-column>

              <!-- 操作 -->
              <el-table-column label="操作" width="84" class-name="action-column">
                <template #default="{ row }">
                  <el-button link @click="openDetail(row, 'audit')">详情</el-button>
                </template>
              </el-table-column>
            </el-table>

            <div class="pagination-bar">
              <el-pagination
                v-model:current-page="auditQuery.page"
                v-model:page-size="auditQuery.pageSize"
                :total="auditQuery.total"
                :page-sizes="[15, 30, 50, 100]"
                background
                layout="total, sizes, prev, pager, next"
                @change="loadAudit"
              />
            </div>
          </el-tab-pane>
          <el-tab-pane label="DDNS 变更" name="ddns">
            <DDNSHistoryTable ref="ddnsHistoryRef" />
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>

    <!-- 规整素雅的标准详情弹窗 -->
    <el-dialog
      v-model="detailDialogVisible"
      :title="activeType === 'audit' ? '管理员操作记录' : '系统操作详情'"
      width="540px"
      destroy-on-close
    >
      <div v-if="activeRecord" class="detail-dialog-body">
        <!-- 简明轻量提示条 (失败/回滚时呈现) -->
        <div
          v-if="activeRecord.status === 'failed' || activeRecord.status === 'rolled_back'"
          class="issue-banner"
          :class="activeRecord.status === 'rolled_back' ? 'is-warn' : 'is-err'"
        >
          <span class="issue-dot" />
          <span class="issue-text">{{ activeRecord.diagnostic || activeRecord.result_summary }}</span>
        </div>

        <!-- 极简无边框 Key-Value 清单 -->
        <div class="prop-list">
          <div class="prop-row">
            <span class="prop-label">日期</span>
            <span class="prop-value mono font-mono">{{ formatDateTime(activeRecord.created_at) }}</span>
          </div>

          <div class="prop-row">
            <span class="prop-label">操作 IP</span>
            <span class="prop-value">
              <span class="mono font-mono">{{ activeRecord.ip_address || '127.0.0.1' }}</span>
              <span class="subtle" style="margin-left: 8px">{{ activeRecord.ip_location || (activeRecord.ip_address === '127.0.0.1' ? '本机控制台' : '未知归属地') }}</span>
            </span>
          </div>

          <div class="prop-row">
            <span class="prop-label">执行状态</span>
            <span class="prop-value">
              <el-tag :type="STATUS_MAP[activeRecord.status]?.type || 'info'" size="small">
                {{ STATUS_MAP[activeRecord.status]?.label || activeRecord.status }}
              </el-tag>
              <span v-if="activeRecord.duration_ms" class="subtle mono font-mono tnum" style="margin-left: 8px">
                耗时 {{ activeRecord.duration_ms }} ms
              </span>
            </span>
          </div>

          <div v-if="activeRecord.node_name || activeRecord.node_id" class="prop-row">
            <span class="prop-label">所属服务器</span>
            <span class="prop-value">{{ activeRecord.node_name || nodeNames[activeRecord.node_id] || activeRecord.node_id }}</span>
          </div>

          <div v-if="activeRecord.target_name" class="prop-row">
            <span class="prop-label">操作对象</span>
            <span class="prop-value">
              <strong>{{ activeRecord.target_name }}</strong>
              <span v-if="activeRecord.target_type" class="subtle" style="margin-left: 6px">
                ({{ TARGET_NAMES[activeRecord.target_type] || activeRecord.target_type }})
              </span>
            </span>
          </div>

          <div class="prop-row">
            <span class="prop-label">业务说明</span>
            <span class="prop-value">{{ activeRecord.summary || activeRecord.result_summary || '操作已执行' }}</span>
          </div>
        </div>

        <!-- 变更参数明细 -->
        <div v-if="parseJsonDetail(activeRecord.detail)" class="params-box">
          <div class="box-title">变更参数明细</div>
          <div class="params-grid">
            <div
              v-for="(val, key) in parseJsonDetail(activeRecord.detail)"
              :key="key"
              class="param-item"
            >
              <span class="param-k">{{ key }}:</span>
              <span class="param-v mono font-mono">{{ typeof val === 'object' ? JSON.stringify(val) : val }}</span>
            </div>
          </div>
        </div>

        <!-- 底层执行输出日志 -->
        <div
          v-if="activeRecord.result_summary && activeRecord.result_summary !== activeRecord.summary"
          class="log-box"
        >
          <div class="box-title">底层执行输出</div>
          <pre class="log-content mono font-mono">{{ activeRecord.result_summary }}</pre>
        </div>
      </div>

      <template #footer>
        <el-button @click="detailDialogVisible = false">关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.node-name-text {
  font-weight: 500;
  color: var(--sb-text, #f1f3f7);
}

.operation-title {
  font-size: 13px;
  line-height: 1.4;
  color: var(--sb-text, #f1f3f7);
}

.action-summary-title {
  font-size: 13px;
  color: var(--sb-text, #f1f3f7);
  line-height: 1.5;
}

.target-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--sb-text, #f1f3f7);
}

.copy-target-id {
  font-size: 11px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  transition: color 0.15s;
}

.copy-target-id:hover {
  color: var(--sb-accent, #38bdf8);
}

.copy-icon {
  font-size: 11px;
}

.operator-name {
  font-size: 13px;
  color: var(--sb-text, #f1f3f7);
}

.text-danger {
  color: var(--sb-danger, #ef4444);
}

.text-warning {
  color: var(--sb-warning, #f59e0b);
}

.font-semibold {
  font-weight: 600;
}

.pagination-bar {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}

/* ── 详情弹窗 ──── */
.detail-dialog-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.issue-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-radius: var(--sb-radius-sm, 8px);
  font-size: 13px;
  font-weight: 500;
  line-height: 1.4;
}

.issue-banner.is-err {
  background: rgba(239, 68, 68, 0.08);
  border: 1px solid rgba(239, 68, 68, 0.2);
  color: var(--sb-danger, #ef4444);
}

.issue-banner.is-warn {
  background: rgba(245, 158, 11, 0.08);
  border: 1px solid rgba(245, 158, 11, 0.2);
  color: var(--sb-warning, #f59e0b);
}

.issue-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
  background: currentColor;
}

.prop-list {
  display: flex;
  flex-direction: column;
}

.prop-row {
  display: flex;
  align-items: flex-start;
  padding: 9px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--sb-line, rgba(255, 255, 255, 0.08));
}

.prop-row:last-child {
  border-bottom: none;
}

.prop-label {
  width: 80px;
  flex-shrink: 0;
  color: var(--sb-muted, #8e93a6);
  line-height: 1.5;
}

.prop-value {
  flex: 1;
  color: var(--sb-text, #f1f3f7);
  line-height: 1.5;
  word-break: break-all;
}

.ip-chip {
  font-size: 11px;
  padding: 1px 6px;
  margin-left: 8px;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.06);
  color: var(--sb-muted, #8e93a6);
}

.params-box,
.log-box {
  background: var(--sb-surface-2, rgba(255, 255, 255, 0.02));
  border: 1px solid var(--sb-line, rgba(255, 255, 255, 0.08));
  border-radius: var(--sb-radius-sm, 8px);
  padding: 12px 14px;
}

.box-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--sb-muted, #8e93a6);
  margin-bottom: 8px;
}

.params-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px 16px;
}

.param-item {
  font-size: 12px;
  display: flex;
  align-items: baseline;
  gap: 6px;
}

.param-k {
  color: var(--sb-muted, #8e93a6);
}

.param-v {
  color: var(--sb-text, #f1f3f7);
  font-weight: 500;
}

.log-content {
  margin: 0;
  padding: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--sb-text-2, #b8bccb);
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 120px;
  overflow-y: auto;
}

.copyable {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.copyable:hover {
  color: var(--sb-accent, #38bdf8);
}
</style>
