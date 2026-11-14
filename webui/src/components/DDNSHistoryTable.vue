<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Delete, Refresh, Search } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, del } from '../api'
import { formatDateTime } from '../format'
import { subscribeLive } from '../live'

const props = defineProps({
  nodeId: { type: String, default: '' },
  hideNodeFilter: { type: Boolean, default: false },
  inDialog: { type: Boolean, default: false },
})

const appState = inject('appState', { nodes: [] })
const isAdmin = inject('isAdmin', ref(true))
const canWrite = inject('canWrite', ref(true))

const loading = ref(false)
const records = ref([])
const keyword = ref('')
const selectedNodeId = ref(props.nodeId || '')
const selectedStatus = ref('')
const selectedTrigger = ref('')
const query = reactive({ page: 1, pageSize: 15, total: 0 })

let unsubscribeLive = null
let debounceTimer = null

watch(
  () => props.nodeId,
  (newVal) => {
    selectedNodeId.value = newVal || ''
    query.page = 1
    loadRecords()
  },
)

watch([keyword, selectedStatus, selectedTrigger, selectedNodeId], () => {
  clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    query.page = 1
    loadRecords()
  }, 250)
})

async function loadRecords() {
  loading.value = true
  try {
    const params = new URLSearchParams({
      page: query.page,
      page_size: query.pageSize,
    })
    const nid = props.nodeId || selectedNodeId.value
    if (nid) params.set('node_id', nid)
    if (keyword.value.trim()) params.set('keyword', keyword.value.trim())
    if (selectedStatus.value) params.set('status', selectedStatus.value)
    if (selectedTrigger.value) params.set('trigger_type', selectedTrigger.value)

    const res = await api(`/ddns/history?${params.toString()}`)
    records.value = res.records || []
    query.total = res.total || 0
  } catch (err) {
    ElMessage.error(err instanceof Error ? err.message : '获取 DDNS 变更记录失败')
    records.value = []
    query.total = 0
  } finally {
    loading.value = false
  }
}

async function removeRecord(row) {
  try {
    await ElMessageBox.confirm('确定要删除这条 DDNS 变更记录吗？', '提示', {
      type: 'warning',
      confirmButtonText: '删除',
      cancelButtonText: '取消',
    })
    await del(`/ddns/history/${row.id}`)
    ElMessage.success('已删除记录')
    loadRecords()
  } catch (err) {
    if (err !== 'cancel') {
      ElMessage.error(err instanceof Error ? err.message : '删除记录失败')
    }
  }
}

async function clearAllRecords() {
  const nid = props.nodeId || selectedNodeId.value
  const targetDesc = nid ? '当前服务器的所有' : '全部服务器的'
  try {
    await ElMessageBox.confirm(`确定要清空${targetDesc} DDNS 变更记录吗？此操作不可撤销。`, '危险操作', {
      type: 'warning',
      confirmButtonText: '确认清空',
      cancelButtonText: '取消',
      confirmButtonClass: 'el-button--danger',
    })
    const path = nid ? `/ddns/history?node_id=${encodeURIComponent(nid)}` : '/ddns/history'
    await del(path)
    ElMessage.success('已清空变更记录')
    query.page = 1
    loadRecords()
  } catch (err) {
    if (err !== 'cancel') {
      ElMessage.error(err instanceof Error ? err.message : '清空失败')
    }
  }
}

onMounted(() => {
  loadRecords()
  unsubscribeLive = subscribeLive((event) => {
    if (event.kind === 'ddns' || event.kind === 'cloudflare') {
      loadRecords()
    }
  })
})

onBeforeUnmount(() => {
  if (unsubscribeLive) unsubscribeLive()
  clearTimeout(debounceTimer)
})

defineExpose({ refresh: loadRecords })
</script>

<template>
  <div class="ddns-history-container" :class="{ 'in-dialog': inDialog }">
    <div class="search-toolbar ddns-history-toolbar">
      <el-input
        v-model="keyword"
        clearable
        :prefix-icon="Search"
        placeholder="搜索域名、服务器、IP"
        style="width: 230px"
      />
      <el-select
        v-if="!props.hideNodeFilter && !props.nodeId"
        v-model="selectedNodeId"
        clearable
        placeholder="全部服务器"
        style="width: 170px"
      >
        <el-option
          v-for="node in appState.nodes"
          :key="node.id"
          :label="node.name"
          :value="node.id"
        />
      </el-select>
      <el-select
        v-model="selectedStatus"
        clearable
        placeholder="全部状态"
        style="width: 110px"
      >
        <el-option label="成功" value="succeeded" />
        <el-option label="失败" value="failed" />
      </el-select>
      <el-select
        v-model="selectedTrigger"
        clearable
        placeholder="全部方式"
        style="width: 110px"
      >
        <el-option label="自动同步" value="auto" />
        <el-option label="手动同步" value="manual" />
      </el-select>
      <div class="toolbar-spacer" />
      <el-button :icon="Refresh" :loading="loading" @click="loadRecords">刷新</el-button>
      <el-button
        v-if="(isAdmin || canWrite) && query.total > 0"
        type="danger"
        plain
        :icon="Delete"
        @click="clearAllRecords"
      >
        清空记录
      </el-button>
    </div>

    <div class="ddns-history-table-wrapper">
      <el-table
        v-loading="loading"
        :data="records"
        empty-text="暂无 DDNS 变更记录"
        stripe
        style="width: 100%"
      >
        <el-table-column label="变更时间" width="168">
          <template #default="{ row }">
            <span class="mono font-mono text-xs">{{ formatDateTime(row.created_at) }}</span>
          </template>
        </el-table-column>

        <el-table-column
          v-if="!props.nodeId"
          label="服务器"
          min-width="140"
          show-overflow-tooltip
        >
          <template #default="{ row }">
            <span class="font-medium">{{ row.node_name || row.node_id }}</span>
          </template>
        </el-table-column>

        <el-table-column label="域名" min-width="180" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono font-mono font-medium">{{ row.domain }}</span>
          </template>
        </el-table-column>

        <el-table-column label="类型" width="76" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="row.record_type === 'AAAA' ? 'warning' : 'primary'" effect="plain">
              {{ row.record_type }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column label="变更前 IP" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono font-mono text-muted">{{ row.old_ip || '—' }}</span>
          </template>
        </el-table-column>

        <el-table-column label="变更后 IP" min-width="140" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="mono font-mono font-semibold text-primary">{{ row.new_ip }}</span>
          </template>
        </el-table-column>

        <el-table-column label="触发方式" width="96" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="row.trigger_type === 'manual' ? 'warning' : 'info'">
              {{ row.trigger_type === 'manual' ? '手动同步' : '自动同步' }}
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tooltip
              v-if="row.status === 'failed' && row.error_message"
              :content="row.error_message"
              placement="top"
            >
              <el-tag size="small" type="danger" effect="dark" class="cursor-pointer">
                失败
              </el-tag>
            </el-tooltip>
            <el-tag v-else-if="row.status === 'failed'" size="small" type="danger">
              失败
            </el-tag>
            <el-tag v-else size="small" type="success">
              成功
            </el-tag>
          </template>
        </el-table-column>

        <el-table-column
          v-if="isAdmin || canWrite"
          label="操作"
          width="68"
          align="center"
          fixed="right"
        >
          <template #default="{ row }">
            <el-button
              link
              type="danger"
              :icon="Delete"
              title="删除此记录"
              @click="removeRecord(row)"
            />
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div class="pagination-bar ddns-history-pagination">
      <el-pagination
        v-model:current-page="query.page"
        v-model:page-size="query.pageSize"
        :total="query.total"
        :page-sizes="[15, 30, 50, 100]"
        background
        layout="total, sizes, prev, pager, next"
        @change="loadRecords"
      />
    </div>
  </div>
</template>

<style scoped>
.ddns-history-container {
  display: flex;
  flex-direction: column;
  min-height: 0;
  width: 100%;
}
.ddns-history-toolbar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  align-items: center;
  margin-bottom: 14px;
}
.toolbar-spacer {
  flex: 1;
}
.ddns-history-table-wrapper {
  flex: 1 1 auto;
  min-height: 0;
  overflow: auto;
}
.ddns-history-pagination {
  margin-top: 14px;
  display: flex;
  justify-content: flex-end;
}
.text-muted {
  color: var(--sb-muted, #94a3b8);
}
.text-primary {
  color: var(--sb-text, #f8fafc);
  font-weight: 600;
}
.cursor-pointer {
  cursor: pointer;
}
.text-xs {
  font-size: 12px;
}
</style>
