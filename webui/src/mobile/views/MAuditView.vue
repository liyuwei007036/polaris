<script setup>
import { computed, inject, onMounted, reactive, ref, watch } from 'vue'
import { DocumentCopy, Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api } from '../../api'
import { formatDateTime, formatRelativeTime, localTimeZoneLabel } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MPicker from '../components/MPicker.vue'
import MSheet from '../components/MSheet.vue'
import {
  AUDIT_MODULES, TARGET_NAMES, TASK_KINDS, STATUS_MAP,
  getModuleInfo, formatAuditTitle, parseJsonDetail
} from '../../audit_helpers'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const tab = ref('tasks') // 'tasks' | 'audit'

// Tasks State
const tasks = ref([])
const taskKeyword = ref('')
const selectedTaskStatus = ref('')
const taskQuery = reactive({ page: 1, pageSize: 20, total: 0 })

// Audit Events State
const events = ref([])
const auditKeyword = ref('')
const selectedModule = ref('')
const selectedAuditStatus = ref('')
const auditQuery = reactive({ page: 1, pageSize: 20, total: 0 })

// Sheet State
const sheetOpen = ref(false)
const activeRecord = ref(null)
const activeType = ref('task')

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((n) => [n.id, n.name])))
const timeZoneLabel = localTimeZoneLabel()

const taskStatusOptions = [
  { value: '', label: '全部状态' },
  { value: 'succeeded', label: '已完成' },
  { value: 'failed', label: '未完成' },
  { value: 'rolled_back', label: '已恢复原配置' },
  { value: 'dispatched', label: '正在处理' },
  { value: 'queued', label: '等待处理' },
]

const auditModuleOptions = [
  { value: '', label: '全部模块' },
  ...AUDIT_MODULES.filter(m => m.value).map(m => ({ value: m.value, label: m.label }))
]

async function loadTasks() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: taskQuery.page,
      page_size: taskQuery.pageSize,
    })
    if (taskKeyword.value.trim()) params.set('keyword', taskKeyword.value.trim())
    if (selectedTaskStatus.value) params.set('status', selectedTaskStatus.value)

    const res = await api(`/tasks?${params.toString()}`)
    tasks.value = res.tasks || []
    taskQuery.total = res.total || 0
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

    const res = await api(`/audit-events?${params.toString()}`)
    events.value = res.audit_events || []
    auditQuery.total = res.total || 0
  } catch {
    events.value = []
  } finally {
    loading.value = false
  }
}

function refreshCurrent() {
  if (tab.value === 'tasks') loadTasks()
  else loadAudit()
}

let debounceTimer = null
watch([taskKeyword, selectedTaskStatus], () => {
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

function turnPage(query, loader, offset) {
  query.page += offset
  loader()
}

function copyText(text) {
  if (!text) return
  navigator.clipboard?.writeText(text).then(() => {
    ElMessage.success('已复制到剪贴板')
  }).catch(() => {
    ElMessage.info('复制失败，请手动选取复制')
  })
}

function openSheet(row, type) {
  activeRecord.value = row
  activeType.value = type
  sheetOpen.value = true
}

const tabs = computed(() => (isAdmin.value
  ? [{ value: 'tasks', label: '系统操作' }, { value: 'audit', label: '管理员修改记录' }]
  : [{ value: 'tasks', label: '系统操作' }]))

onMounted(async () => {
  await loadNodes?.().catch(() => {})
  refreshCurrent()
})
</script>

<template>
  <MPage title="操作审计与任务" :loading="loading">
    <template #actions>
      <button type="button" class="m-header__btn" :disabled="loading" aria-label="刷新" @click="refreshCurrent">
        <el-icon><Refresh /></el-icon>
      </button>
    </template>
    <MSegmented v-model="tab" :options="tabs" />
    <div class="m-count">保留最近 7 天 · 时间按 {{ timeZoneLabel }} 显示</div>

    <!-- 1. 系统操作 -->
    <template v-if="tab === 'tasks'">
      <el-input
        v-model="taskKeyword"
        clearable
        :prefix-icon="Search"
        placeholder="搜索操作、服务器或结果"
      />
      <div class="m-filters">
        <MPicker
          :model-value="selectedTaskStatus"
          chip
          :options="taskStatusOptions"
          title="按状态筛选"
          placeholder="全部状态"
          @update:model-value="selectedTaskStatus = $event"
        />
      </div>

      <article
        v-for="row in tasks"
        :key="row.id"
        class="m-item"
        @click="openSheet(row, 'task')"
      >
        <div class="m-item__hit">
          <div class="m-item__head">
            <span class="m-item__title">
              {{ row.kind_name || TASK_KINDS[row.kind] || row.kind }}
            </span>
            <span class="m-pill" :class="STATUS_MAP[row.status]?.badge || 'm-pill--info'">
              {{ STATUS_MAP[row.status]?.label || row.status }}
            </span>
          </div>

          <div class="m-item__meta">
            {{ row.node_name || nodeNames[row.node_id] || row.node_id || '控制端' }}
            · {{ formatDateTime(row.created_at) }}
            <span v-if="row.duration_ms">· 耗时 {{ row.duration_ms }}ms</span>
          </div>

          <div
            class="m-item__note"
            :class="{ 'm-note--err': row.status === 'failed', 'm-note--warn': row.status === 'rolled_back' }"
          >
            {{ row.diagnostic || row.result_summary || (row.status === 'succeeded' ? '配置已生效' : '等待处理') }}
          </div>
        </div>
      </article>

      <div v-if="!tasks.length && !loading" class="m-empty">没有系统操作记录</div>

      <div v-if="taskQuery.total > 0" class="pager">
        <el-button :disabled="taskQuery.page <= 1" @click="turnPage(taskQuery, loadTasks, -1)">上一页</el-button>
        <span class="pager__label">第 {{ taskQuery.page }} 页 / 共 {{ taskQuery.total }} 条</span>
        <el-button :disabled="taskQuery.page * taskQuery.pageSize >= taskQuery.total" @click="turnPage(taskQuery, loadTasks, 1)">下一页</el-button>
      </div>
    </template>

    <!-- 2. 管理员修改记录 -->
    <template v-else>
      <el-input
        v-model="auditKeyword"
        clearable
        :prefix-icon="Search"
        placeholder="搜索操作人、内容或对象"
      />
      <div class="m-filters">
        <MPicker
          :model-value="selectedModule"
          chip
          :options="auditModuleOptions"
          title="按业务模块筛选"
          placeholder="全部模块"
          @update:model-value="selectedModule = $event"
        />
      </div>

      <article
        v-for="row in events"
        :key="row.id"
        class="m-item"
        @click="openSheet(row, 'audit')"
      >
        <div class="m-item__hit">
          <div class="m-item__head">
            <span class="m-item__title">
              {{ formatAuditTitle(row) }}
            </span>
            <span class="m-pill" :class="STATUS_MAP[row.status]?.badge || 'm-pill--success'">
              {{ STATUS_MAP[row.status]?.label || '已完成' }}
            </span>
          </div>

          <div class="m-item__meta">
            {{ row.ip_address || '127.0.0.1' }}
            <span v-if="row.ip_location">({{ row.ip_location }})</span>
            · {{ formatDateTime(row.created_at) }}
          </div>

          <div class="m-item__note">
            对象：{{ row.target_name || TARGET_NAMES[row.target_type] || row.target_type }}
          </div>
        </div>
      </article>

      <div v-if="!events.length && !loading" class="m-empty">没有管理员修改记录</div>

      <div v-if="auditQuery.total > 0" class="pager">
        <el-button :disabled="auditQuery.page <= 1" @click="turnPage(auditQuery, loadAudit, -1)">上一页</el-button>
        <span class="pager__label">第 {{ auditQuery.page }} 页 / 共 {{ auditQuery.total }} 条</span>
        <el-button :disabled="auditQuery.page * auditQuery.pageSize >= auditQuery.total" @click="turnPage(auditQuery, loadAudit, 1)">下一页</el-button>
      </div>
    </template>

    <!-- 原生风格移动端详情抽屉 (MSheet) -->
    <MSheet
      v-model="sheetOpen"
      :title="activeType === 'audit' ? '管理员操作记录' : '系统任务详情'"
    >
      <div v-if="activeRecord" class="m-sheet-content">
        <!-- 轻量提示条 (失败/回滚时呈现) -->
        <div
          v-if="activeRecord.status === 'failed' || activeRecord.status === 'rolled_back'"
          class="issue-banner"
          :class="activeRecord.status === 'rolled_back' ? 'is-warn' : 'is-err'"
        >
          <span class="issue-dot" />
          <span class="issue-text">{{ activeRecord.diagnostic || activeRecord.result_summary }}</span>
        </div>

        <div class="m-kv-group">
          <div class="m-kv-row">
            <span class="m-kv-lbl">日期</span>
            <span class="m-kv-val mono">{{ formatDateTime(activeRecord.created_at) }}</span>
          </div>

          <div class="m-kv-row">
            <span class="m-kv-lbl">操作 IP</span>
            <span class="m-kv-val mono">
              {{ activeRecord.ip_address || '127.0.0.1' }}
              <span v-if="activeRecord.ip_location" class="subtle">({{ activeRecord.ip_location }})</span>
            </span>
          </div>

          <div class="m-kv-row">
            <span class="m-kv-lbl">状态</span>
            <span class="m-kv-val">
              <span class="m-pill" :class="STATUS_MAP[activeRecord.status]?.badge || 'm-pill--info'">
                {{ STATUS_MAP[activeRecord.status]?.label || activeRecord.status }}
              </span>
              <span v-if="activeRecord.duration_ms" class="subtle" style="margin-left: 6px">({{ activeRecord.duration_ms }}ms)</span>
            </span>
          </div>

          <div v-if="activeRecord.node_name || activeRecord.node_id" class="m-kv-row">
            <span class="m-kv-lbl">服务器</span>
            <span class="m-kv-val">{{ activeRecord.node_name || nodeNames[activeRecord.node_id] || activeRecord.node_id }}</span>
          </div>

          <div v-if="activeRecord.target_name" class="m-kv-row">
            <span class="m-kv-lbl">操作对象</span>
            <span class="m-kv-val font-semibold">{{ activeRecord.target_name }}</span>
          </div>

          <div class="m-kv-row">
            <span class="m-kv-lbl">业务说明</span>
            <span class="m-kv-val">{{ activeRecord.summary || activeRecord.result_summary || '操作已执行' }}</span>
          </div>
        </div>

        <!-- 参数详情 -->
        <div v-if="parseJsonDetail(activeRecord.detail)" class="m-param-group">
          <div class="group-title">变更参数</div>
          <div
            v-for="(val, key) in parseJsonDetail(activeRecord.detail)"
            :key="key"
            class="m-kv-row"
          >
            <span class="m-kv-lbl">{{ key }}</span>
            <span class="m-kv-val mono">{{ typeof val === 'object' ? JSON.stringify(val) : val }}</span>
          </div>
        </div>

        <!-- 原始输出 -->
        <div v-if="activeRecord.result_summary && activeRecord.result_summary !== activeRecord.summary" class="m-param-group">
          <div class="group-title">底层执行输出</div>
          <div class="m-raw-box mono">{{ activeRecord.result_summary }}</div>
        </div>
      </div>
    </MSheet>
  </MPage>
</template>

<style scoped>
.m-note--err {
  color: var(--sb-danger);
}

.m-note--warn {
  color: var(--sb-warning);
}

.pager {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 14px;
}

.pager :deep(.el-button) {
  flex: none;
  height: var(--m-tap, 36px);
  margin: 0;
}

.pager__label {
  flex: 1;
  color: var(--sb-muted);
  font-size: 12px;
  text-align: center;
}

.m-sheet-content {
  display: flex;
  flex-direction: column;
  gap: 14px;
  padding: 6px 0 24px;
}

.issue-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  border-radius: 8px;
  font-size: 13px;
  font-weight: 500;
  line-height: 1.4;
}

.issue-banner.is-err {
  background: rgba(239, 68, 68, 0.08);
  border: 1px solid rgba(239, 68, 68, 0.2);
  color: var(--sb-danger);
}

.issue-banner.is-warn {
  background: rgba(245, 158, 11, 0.08);
  border: 1px solid rgba(245, 158, 11, 0.2);
  color: var(--sb-warning);
}

.issue-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex: none;
  background: currentColor;
}

.m-kv-group,
.m-param-group {
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line-strong);
  border-radius: 8px;
  padding: 8px 12px;
}

.group-title {
  font-size: 12px;
  font-weight: 600;
  color: var(--sb-muted);
  margin-bottom: 6px;
  padding-bottom: 4px;
  border-bottom: 1px solid var(--sb-line);
}

.m-kv-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 12px;
  padding: 6px 0;
  border-bottom: 1px solid var(--sb-line);
}

.m-kv-row:last-child {
  border-bottom: none;
}

.m-kv-lbl {
  color: var(--sb-muted);
  flex: none;
  width: 90px;
}

.m-kv-val {
  color: var(--sb-text);
  text-align: right;
  word-break: break-all;
}

.m-raw-box {
  background: var(--neu-inset-bg);
  color: var(--sb-text);
  border: 1px solid var(--sb-line);
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 11px;
  line-height: 1.4;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 140px;
  overflow-y: auto;
}

.copyable {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
