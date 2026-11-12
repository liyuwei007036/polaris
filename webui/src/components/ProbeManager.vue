<script setup>
import { computed, inject, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Bottom, Delete, Edit, InfoFilled, Plus, Refresh, Search, Top } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'

const canWrite = inject('canWrite')

const tasks = ref([])
const loading = ref(false)
const search = ref('')

const filteredTasks = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return tasks.value
  return tasks.value.filter((t) =>
    [t.name, t.target]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(term),
  )
})

async function load() {
  loading.value = true
  try {
    const taskResult = await api('/ping-tasks')
    tasks.value = taskResult.tasks || []
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '延迟监控加载失败')
  } finally {
    loading.value = false
  }
}

// ── 新建 / 编辑 ───────────────────────────────
const dialogVisible = ref(false)
const saving = ref(false)
const editingID = ref(0)
const form = ref({ name: '', target: '', interval: 60 })

function openCreate() {
  editingID.value = 0
  form.value = { name: '', target: '', interval: 60 }
  dialogVisible.value = true
}

function openEdit(task) {
  editingID.value = task.id
  form.value = {
    name: task.name,
    target: task.target,
    interval: task.interval,
  }
  dialogVisible.value = true
}

async function save() {
  if (!form.value.name.trim() || !form.value.target.trim()) {
    ElMessage.warning('请填写名称和目标地址')
    return
  }
  const interval = Number(form.value.interval)
  if (!Number.isInteger(interval) || interval < 5 || interval > 3600) {
    form.value.interval = 60
    ElMessage.warning('间隔要填 5–3600 之间的整数秒，已回填默认 60')
    return
  }
  saving.value = true
  try {
    const body = {
      name: form.value.name.trim(),
      target: form.value.target.trim(),
      interval,
      auto_join: true,
      node_ids: [],
    }
    if (editingID.value) {
      await put(`/ping-tasks/${editingID.value}`, body)
      ElMessage.success('探针已保存')
    } else {
      await post('/ping-tasks', body)
      ElMessage.success('探针已创建')
    }
    dialogVisible.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '探针保存失败')
  } finally {
    saving.value = false
  }
}

async function remove(task) {
  try {
    await ElMessageBox.confirm(`确认删除探针「${task.name}」？删除后所有服务器将停止该探测。`, '删除探针', {
      type: 'warning',
      confirmButtonText: '删除',
    })
  } catch {
    return
  }
  try {
    await del(`/ping-tasks/${task.id}`)
    ElMessage.success('探针已删除')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '探针删除失败')
  }
}

async function move(task, delta) {
  const index = tasks.value.findIndex((t) => t.id === task.id)
  if (index === -1) return
  const targetIndex = index + delta
  if (targetIndex < 0 || targetIndex >= tasks.value.length) return
  const swapWith = tasks.value[targetIndex]
  const list = [...tasks.value]
  list[index] = swapWith
  list[targetIndex] = task
  tasks.value = list
  try {
    await post('/ping-tasks/reorder', { ids: list.map((t) => t.id) })
  } catch {
    await load()
  }
}

onMounted(load)
</script>

<template>
  <div class="ping-tasks-view">
    <div class="toolbar">
      <el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">添加探针</el-button>
    </div>

    <section class="probes-panel">
      <p class="probe-intro">
        每个探针由集群内所有服务器各自执行 TCP 握手测速，实时测量服务器到目标的网络延迟与丢包率，数据汇总展示于卡片的 24h 链路热力与详情大盘中。
      </p>

      <el-input
        v-model="search"
        class="probe-search"
        placeholder="搜索探针名称或目标地址"
        :prefix-icon="Search"
        clearable
      />

      <el-table :data="filteredTasks" v-loading="loading" class="probe-table">
        <el-table-column label="探针名称" prop="name" min-width="160">
          <template #default="{ row }">
            <span class="probe-name-txt">{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="target" label="目标地址 (host:port)" min-width="180" />
        <el-table-column label="探测间隔" width="110" align="right">
          <template #default="{ row }">
            <span class="tnum">{{ row.interval }}s</span>
          </template>
        </el-table-column>
        <el-table-column v-if="canWrite" label="操作" width="220" align="right" class-name="action-column">
          <template #default="{ row }">
            <div class="probe-actions">
              <el-tooltip content="上移" placement="top">
                <el-button link :icon="Top" aria-label="上移" @click="move(row, -1)" />
              </el-tooltip>
              <el-tooltip content="下移" placement="top">
                <el-button link :icon="Bottom" aria-label="下移" @click="move(row, 1)" />
              </el-tooltip>
              <el-button link :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button link class="danger-btn" :icon="Delete" @click="remove(row)">删除</el-button>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="操作" v-else width="100" align="right" class-name="action-column">
          <template #default="{ row }">
            <el-button link :icon="InfoFilled" @click="openEdit(row)">详情</el-button>
          </template>
        </el-table-column>
        <template #empty>还没有延迟监控，点右上角“添加探针”</template>
      </el-table>
    </section>

    <el-dialog
      v-model="dialogVisible"
      :title="editingID ? '编辑探针' : '添加探针'"
      width="520px"
      class="probe-dialog"
    >
      <el-form label-position="top" class="probe-form">
        <el-form-item label="探针名称" required>
          <el-input v-model="form.name" maxlength="64" placeholder="例如：广西电信、广西移动、Cloudflare" @keyup.enter="save" />
        </el-form-item>

        <el-form-item label="目标地址（host:port）" required>
          <el-input v-model="form.target" placeholder="例如：202.103.224.68:53 或 1.1.1.1:443" @keyup.enter="save" />
          <div class="probe-hint">格式为 host:port，所有服务器将定期向此目标建立 TCP 握手并采集往返延迟与丢包率</div>
        </el-form-item>

        <el-form-item label="探测间隔（秒）">
          <el-input-number v-model="form.interval" :min="5" :max="3600" :step="5" controls-position="right" style="width: 160px" />
        </el-form-item>
      </el-form>

      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.probes-panel {
  border: 1px solid var(--sb-line);
  border-radius: 14px;
  padding: 18px 20px;
  background: var(--sb-surface, transparent);
}

.probe-actions {
  display: inline-flex;
  align-items: center;
  justify-content: flex-end;
  gap: 4px;
  white-space: nowrap;
}

.probe-actions :deep(.el-button) {
  margin: 0 !important;
  padding: 4px 6px !important;
  color: var(--sb-text-2) !important;
}

.probe-actions :deep(.el-button:hover) {
  color: var(--sb-text) !important;
}

.probe-actions :deep(.danger-btn) {
  color: var(--sb-danger, #ef4444) !important;
}

.probe-actions :deep(.danger-btn:hover) {
  color: #f87171 !important;
}

.probe-intro {
  margin: 0 0 14px;
  font-size: 12.5px;
  line-height: 1.7;
  color: var(--sb-muted);
}

.probe-search {
  max-width: 320px;
  margin-bottom: 14px;
}

.toolbar {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-bottom: 14px;
}

.probe-name-txt {
  font-weight: 600;
  color: var(--sb-text);
}

.tnum {
  font-variant-numeric: tabular-nums;
  color: var(--sb-muted);
}

.probe-hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--sb-muted);
  line-height: 1.5;
}

.probe-form :deep(.el-form-item__label) {
  font-weight: 600;
  color: var(--sb-text);
}
</style>
