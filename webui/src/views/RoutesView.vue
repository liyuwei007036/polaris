<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const rows = ref([])
const outbounds = ref([])
const listeners = ref([])
const endpoints = ref([])
const dialogOpen = ref(false)
const editing = ref(null)
const matchKind = ref('domain_suffix')
const values = ref([])
const form = reactive({ node_id: '', node_ids: [], priority: 100, enabled: true, network: '', protocol: '', inbound_tag: '', endpoint_name: '', port: 0, action: 'route', outbound_tag: 'direct' })
const keyword = ref('')
const selectedNode = ref('')
const selectedAction = ref('')
const outboundOptions = computed(() => [{ label: '服务器直连', value: 'direct' }, { label: '阻断连接', value: 'block' }, ...outbounds.value.filter((item) => item.enabled && item.type !== 'direct').map((item) => ({ label: item.name, value: item.id }))])
const accountOptions = computed(() => {
  const ids = form.node_ids || []
  return listeners.value
    .filter((listener) => ids.includes(listener.node_id))
    .flatMap((listener) => endpoints.value
      .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
      .map((endpoint) => ({
        value: endpoint.name,
        label: `${endpoint.name} · ${nodeNames.value[listener.node_id] || ''} (${listener.name})`,
      })))
})
const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const isAllNodesSelected = computed(() => appState.nodes.length > 0 && form.node_ids.length === appState.nodes.length)
const isIndeterminateNodes = computed(() => form.node_ids.length > 0 && form.node_ids.length < appState.nodes.length)

function getRowNodeIDs(row) {
  if (Array.isArray(row.node_ids) && row.node_ids.length > 0) return row.node_ids
  if (row.node_id) return [row.node_id]
  return []
}

function formatNodesText(row) {
  const ids = getRowNodeIDs(row)
  if (!ids.length) return '未指定服务器'
  if (appState.nodes.length > 0 && ids.length === appState.nodes.length) {
    return '全部服务器'
  }
  const names = ids.map((id) => nodeNames.value[id] || id).filter(Boolean)
  if (names.length <= 2) {
    return names.join('、')
  }
  return `${names.slice(0, 2).join('、')} 等 ${names.length} 台`
}

function formatNodeTooltip(row) {
  const ids = getRowNodeIDs(row)
  return ids.map((id) => nodeNames.value[id] || id).join('、')
}

function toggleSelectAllNodes(val) {
  if (val) {
    form.node_ids = appState.nodes.map((n) => n.id)
  } else {
    form.node_ids = []
  }
  loadAccounts(form.node_ids)
}

function onSelectedNodesChange() {
  loadAccounts(form.node_ids)
}
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value) {
    const ids = getRowNodeIDs(row)
    if (!ids.includes(selectedNode.value)) return false
  }
  if (selectedAction.value && row.action !== selectedAction.value) return false
  return includesText([formatNodeTooltip(row), matchText(row), actionText(row), row.priority], keyword.value)
}))

async function load() {
  loading.value = true
  try {
    const [ruleResult, outboundResult, listenerResult] = await Promise.all([
      api('/rules').catch(() => ({ rules: [] })),
      api('/outbounds').catch(() => ({ outbounds: [] })),
      api('/listeners').catch(() => ({ listeners: [] })),
    ])
    rows.value = ruleResult.rules || []
    outbounds.value = outboundResult.outbounds || []
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}

// Account names are only needed by the advanced matcher in the dialog, so
// they are fetched when it opens rather than on every list refresh.
async function loadAccounts(nodeIDs) {
  const ids = Array.isArray(nodeIDs) ? nodeIDs : [nodeIDs].filter(Boolean)
  if (!ids.length) {
    endpoints.value = []
    return
  }
  const forNodes = listeners.value.filter((listener) => ids.includes(listener.node_id) && listener.endpoint_count > 0)
  const results = await Promise.all(forNodes.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
  endpoints.value = results.flatMap((result) => result.endpoints || [])
}
function resetForm() {
  Object.assign(form, {
    node_id: appState.nodes[0]?.id || '',
    node_ids: selectedNode.value ? [selectedNode.value] : appState.nodes.map((n) => n.id),
    priority: 100,
    enabled: true,
    network: '',
    protocol: '',
    inbound_tag: '',
    endpoint_name: '',
    port: 0,
    action: 'route',
    outbound_tag: 'direct',
  })
  matchKind.value = 'domain_suffix'
  values.value = []
}
function openCreate() {
  editing.value = null
  resetForm()
  loadAccounts(form.node_ids)
  dialogOpen.value = true
}
function openEdit(row) {
  editing.value = row
  Object.assign(form, row)
  form.node_ids = getRowNodeIDs(row)
  loadAccounts(form.node_ids)
  form.outbound_tag = row.action === 'reject' ? 'block' : row.action === 'direct' ? 'direct' : row.outbound_tag
  const candidates = [['domains', 'domains'], ['domain_suffix', 'domain_suffix'], ['cidrs', 'cidrs']]
  const selected = candidates.find(([field]) => row[field]?.length)
  matchKind.value = selected?.[1] || (row.port ? 'port' : 'advanced')
  values.value = selected ? [...row[selected[0]]] : []
  dialogOpen.value = true
}
function payload() {
  return {
    node_ids: form.node_ids,
    priority: Number(form.priority), enabled: form.enabled,
    domains: matchKind.value === 'domains' ? values.value : [],
    domain_suffix: matchKind.value === 'domain_suffix' ? values.value : [],
    cidrs: matchKind.value === 'cidrs' ? values.value : [],
    port: matchKind.value === 'port' ? Number(form.port) : 0,
    network: form.network, protocol: form.protocol, inbound_tag: form.inbound_tag,
    endpoint_name: form.endpoint_name,
    action: form.outbound_tag === 'block' ? 'reject' : form.outbound_tag === 'direct' ? 'direct' : 'outbound',
    outbound_tag: ['block', 'direct'].includes(form.outbound_tag) ? '' : form.outbound_tag,
  }
}
async function save() {
  if (!form.node_ids || form.node_ids.length === 0) {
    ElMessage.warning('请选择至少一台服务器')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await put(`/rules/${editing.value.id}`, payload())
      ElMessage.success('访问规则已保存')
    } else {
      await post('/rules', payload())
      ElMessage.success('访问规则已保存')
    }
    dialogOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '保存访问规则失败')
  } finally { saving.value = false }
}
async function toggle(row) { await post(`/rules/${row.id}/enabled`, { enabled: !row.enabled }); ElMessage.success('状态已更新'); await load() }
async function remove(row) {
  await ElMessageBox.confirm('确认删除这条服务器访问规则？', '删除访问规则', { type: 'warning' })
  await del(`/rules/${row.id}`); ElMessage.success('访问规则已删除'); await load()
}
function matchText(row) {
  if (row.domains?.length) return `完整域名：${row.domains.join('、')}`
  if (row.domain_suffix?.length) return `域名后缀：${row.domain_suffix.join('、')}`
  if (row.cidrs?.length) return `目标网段：${row.cidrs.join('、')}`
  if (row.port) return `目标端口：${row.port}`
  return [row.network, row.protocol, row.inbound_tag, row.endpoint_name].filter(Boolean).join(' · ') || '全部流量'
}
function actionText(row) { return row.action === 'reject' ? '阻断连接' : (outboundOptions.value.find((item) => item.value === row.outbound_tag)?.label || row.outbound_tag || '服务器直连') }
onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="服务器访问规则">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索匹配条件或处理方式" style="width: 280px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 180px"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select>
        <el-select v-model="selectedAction" clearable placeholder="全部处理方式" style="width: 160px"><el-option label="服务器直连" value="direct" /><el-option label="使用出口" value="outbound" /><el-option label="阻断连接" value="reject" /></el-select>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredRows" :loading="loading" empty-text="还没有访问规则">
          <el-table-column label="服务器" min-width="160"><template #default="{ row }"><span :title="formatNodeTooltip(row)">{{ formatNodesText(row) }}</span></template></el-table-column>
          <el-table-column label="优先级" prop="priority" width="90" />
          <el-table-column label="匹配条件" min-width="340"><template #default="{ row }"><strong>{{ matchText(row) }}</strong></template></el-table-column>
          <el-table-column label="处理方式" min-width="180"><template #default="{ row }"><el-tag :type="row.action === 'reject' ? 'danger' : 'primary'">{{ actionText(row) }}</el-tag></template></el-table-column>
          <el-table-column label="状态" width="90"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
          <el-table-column label="操作" width="200" fixed="right" class-name="action-column"><template #default="{ row }"><el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button><el-button v-if="canWrite" link @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="dialogOpen" :title="editing ? '编辑服务器访问规则' : '新建服务器访问规则'" width="680px">
      <el-form label-position="top">
        <el-form-item label="服务器" required>
          <div class="node-select-bar">
            <el-checkbox
              :model-value="isAllNodesSelected"
              :indeterminate="isIndeterminateNodes"
              @change="toggleSelectAllNodes"
            >
              全部服务器
            </el-checkbox>
            <span class="node-select-badge">已选择 {{ form.node_ids.length }} / {{ appState.nodes.length }} 台</span>
          </div>
          <el-select
            v-model="form.node_ids"
            multiple
            filterable
            collapse-tags
            collapse-tags-tooltip
            :max-collapse-tags="4"
            placeholder="请选择要应用规则的服务器（可多选）"
            style="width: 100%"
            @change="onSelectedNodesChange"
          >
            <el-option
              v-for="node in appState.nodes"
              :key="node.id"
              :label="node.name"
              :value="node.id"
            >
              <div class="node-option-row">
                <span>{{ node.name }}</span>
                <span class="node-option-sub">{{ node.endpoint || node.ip || '' }}</span>
              </div>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item label="匹配方式"><el-radio-group v-model="matchKind"><el-radio-button value="domain_suffix">域名后缀</el-radio-button><el-radio-button value="domains">完整域名</el-radio-button><el-radio-button value="cidrs">目标网段</el-radio-button><el-radio-button value="port">目标端口</el-radio-button><el-radio-button value="advanced">高级条件</el-radio-button></el-radio-group></el-form-item>
        <el-form-item v-if="['domain_suffix','domains','cidrs'].includes(matchKind)" :label="matchKind === 'cidrs' ? '网段列表' : '域名列表'"><el-select v-model="values" multiple filterable allow-create default-first-option style="width: 100%" :placeholder="matchKind === 'cidrs' ? '例如 10.0.0.0/8' : '输入后按回车添加'" /></el-form-item>
        <el-form-item v-if="matchKind === 'port'" label="目标端口"><el-input-number v-model="form.port" :min="1" :max="65535" /></el-form-item>
        <el-row v-if="matchKind === 'advanced'" :gutter="16"><el-col :span="12"><el-form-item label="网络协议"><el-select v-model="form.network" clearable style="width: 100%"><el-option label="TCP" value="tcp" /><el-option label="UDP" value="udp" /></el-select></el-form-item></el-col><el-col :span="12"><el-form-item label="应用协议"><el-input v-model="form.protocol" placeholder="例如 DNS" /></el-form-item></el-col><el-col :span="12"><el-form-item label="接入服务标签"><el-input v-model="form.inbound_tag" /></el-form-item></el-col><el-col :span="12"><el-form-item label="接入节点"><el-select v-model="form.endpoint_name" clearable filterable style="width: 100%" placeholder="适用于全部节点"><el-option v-for="account in accountOptions" :key="account.label" :label="account.label" :value="account.value" /></el-select></el-form-item></el-col></el-row>
        <el-row :gutter="16"><el-col :span="8"><el-form-item label="优先级"><el-input-number v-model="form.priority" :min="0" style="width: 100%" /></el-form-item></el-col><el-col :span="16"><el-form-item label="处理方式"><el-select v-model="form.outbound_tag" style="width: 100%"><el-option v-for="option in outboundOptions" :key="option.value" :label="option.label" :value="option.value" /></el-select></el-form-item></el-col></el-row>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="save">保存</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.node-select-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 6px;
  width: 100%;
}
.node-select-badge {
  font-size: 12px;
  color: var(--sb-muted);
}
.node-option-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
}
.node-option-sub {
  font-size: 11px;
  color: var(--sb-muted);
  font-family: var(--sb-mono);
}
</style>
