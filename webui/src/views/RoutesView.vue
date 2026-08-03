<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'

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
const form = reactive({ node_id: '', priority: 100, enabled: true, network: '', protocol: '', inbound_tag: '', endpoint_name: '', port: 0, action: 'route', outbound_tag: 'direct' })
const keyword = ref('')
const selectedNode = ref('')
const selectedAction = ref('')
const outboundOptions = computed(() => [{ label: '服务器直连', value: 'direct' }, { label: '阻断连接', value: 'block' }, ...outbounds.value.filter((item) => item.enabled && item.type !== 'direct').map((item) => ({ label: item.name, value: item.id }))])
const accountOptions = computed(() => listeners.value
  .filter((listener) => listener.node_id === form.node_id)
  .flatMap((listener) => endpoints.value
    .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
    .map((endpoint) => ({ value: endpoint.name, label: `${endpoint.name} · ${listener.name}` }))))
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedAction.value && row.action !== selectedAction.value) return false
  return includesText([row.node_name, matchText(row), actionText(row), row.priority], keyword.value)
}))

async function load() {
  loading.value = true
  try {
    const [ruleResults, outboundResult, listenerResult] = await Promise.all([
      Promise.all(appState.nodes.map(async (node) => ({ node, result: await api(`/nodes/${node.id}/rules`).catch(() => ({ rules: [] })) }))),
      api('/outbounds').catch(() => ({ outbounds: [] })),
      api('/listeners').catch(() => ({ listeners: [] })),
    ])
    rows.value = ruleResults.flatMap(({ node, result }) => (result.rules || []).map((rule) => ({ ...rule, node_name: node.name })))
    outbounds.value = outboundResult.outbounds || []
    listeners.value = listenerResult.listeners || []
    const endpointResults = await Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = endpointResults.flatMap((result) => result.endpoints || [])
  } finally { loading.value = false }
}
function resetForm() {
  Object.assign(form, { node_id: appState.nodes[0]?.id || '', priority: 100, enabled: true, network: '', protocol: '', inbound_tag: '', endpoint_name: '', port: 0, action: 'route', outbound_tag: 'direct' })
  matchKind.value = 'domain_suffix'
  values.value = []
}
function openCreate() { editing.value = null; resetForm(); dialogOpen.value = true }
function openEdit(row) {
  editing.value = row
  Object.assign(form, row)
  form.outbound_tag = row.action === 'reject' ? 'block' : row.action === 'direct' ? 'direct' : row.outbound_tag
  const candidates = [['domains', 'domains'], ['domain_suffix', 'domain_suffix'], ['cidrs', 'cidrs']]
  const selected = candidates.find(([field]) => row[field]?.length)
  matchKind.value = selected?.[1] || (row.port ? 'port' : 'advanced')
  values.value = selected ? [...row[selected[0]]] : []
  dialogOpen.value = true
}
function payload() {
  return {
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
  saving.value = true
  try {
    if (editing.value) await put(`/rules/${editing.value.id}`, payload())
    else await post(`/nodes/${form.node_id}/rules`, payload())
    ElMessage.success(editing.value ? '服务器访问规则已保存，正在自动应用' : '服务器访问规则已创建，正在自动应用')
    dialogOpen.value = false
    await load()
  } finally { saving.value = false }
}
async function toggle(row) { await post(`/rules/${row.id}/enabled`, { enabled: !row.enabled }); ElMessage.success('状态已更新，系统正在自动应用'); await load() }
async function remove(row) {
  await ElMessageBox.confirm('确认删除这条服务器访问规则？', '删除访问规则', { type: 'warning' })
  await del(`/rules/${row.id}`); ElMessage.success('服务器访问规则已删除，正在自动应用'); await load()
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
        <el-table v-loading="loading" :data="filteredRows">
          <el-table-column label="服务器" prop="node_name" min-width="150" />
          <el-table-column label="优先级" prop="priority" width="90" />
          <el-table-column label="匹配条件" min-width="340"><template #default="{ row }"><strong>{{ matchText(row) }}</strong></template></el-table-column>
          <el-table-column label="处理方式" min-width="180"><template #default="{ row }"><el-tag :type="row.action === 'reject' ? 'danger' : 'primary'">{{ actionText(row) }}</el-tag></template></el-table-column>
          <el-table-column label="状态" width="90"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
          <el-table-column label="操作" width="200" fixed="right" class-name="action-column"><template #default="{ row }"><el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button><el-button v-if="canWrite" link @click="toggle(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogOpen" :title="editing ? '编辑服务器访问规则' : '新建服务器访问规则'" width="680px">
      <el-form label-position="top">
        <el-form-item label="服务器" required><el-select v-model="form.node_id" :disabled="Boolean(editing)" style="width: 100%" @change="form.endpoint_name = ''"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item>
        <el-form-item label="匹配方式"><el-radio-group v-model="matchKind"><el-radio-button value="domain_suffix">域名后缀</el-radio-button><el-radio-button value="domains">完整域名</el-radio-button><el-radio-button value="cidrs">目标网段</el-radio-button><el-radio-button value="port">目标端口</el-radio-button><el-radio-button value="advanced">高级条件</el-radio-button></el-radio-group></el-form-item>
        <el-form-item v-if="['domain_suffix','domains','cidrs'].includes(matchKind)" :label="matchKind === 'cidrs' ? '网段列表' : '域名列表'"><el-select v-model="values" multiple filterable allow-create default-first-option style="width: 100%" :placeholder="matchKind === 'cidrs' ? '例如 10.0.0.0/8' : '输入后按回车添加'" /></el-form-item>
        <el-form-item v-if="matchKind === 'port'" label="目标端口"><el-input-number v-model="form.port" :min="1" :max="65535" /></el-form-item>
        <el-row v-if="matchKind === 'advanced'" :gutter="16"><el-col :span="12"><el-form-item label="网络协议"><el-select v-model="form.network" clearable style="width: 100%"><el-option label="TCP" value="tcp" /><el-option label="UDP" value="udp" /></el-select></el-form-item></el-col><el-col :span="12"><el-form-item label="应用协议"><el-input v-model="form.protocol" placeholder="例如 DNS" /></el-form-item></el-col><el-col :span="12"><el-form-item label="接入服务标签"><el-input v-model="form.inbound_tag" /></el-form-item></el-col><el-col :span="12"><el-form-item label="接入用户"><el-select v-model="form.endpoint_name" clearable filterable style="width: 100%" placeholder="适用于全部用户"><el-option v-for="account in accountOptions" :key="account.label" :label="account.label" :value="account.value" /></el-select></el-form-item></el-col></el-row>
        <el-row :gutter="16"><el-col :span="8"><el-form-item label="优先级"><el-input-number v-model="form.priority" :min="0" style="width: 100%" /></el-form-item></el-col><el-col :span="16"><el-form-item label="处理方式"><el-select v-model="form.outbound_tag" style="width: 100%"><el-option v-for="option in outboundOptions" :key="option.value" :label="option.label" :value="option.value" /></el-select></el-form-item></el-col></el-row>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="save">保存</el-button></template>
    </el-dialog>
  </div>
</template>
