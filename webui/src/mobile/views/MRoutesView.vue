<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MValueList from '../components/MValueList.vue'
import MActionSheet from '../components/MActionSheet.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')

const loading = ref(false)
const saving = ref(false)
const rows = ref([])
const outbounds = ref([])
const listeners = ref([])
const endpoints = ref([])
const sheetOpen = ref(false)
const editing = ref(null)
const matchKind = ref('domain_suffix')
const values = ref([])
const form = reactive({ node_id: '', priority: 100, enabled: true, network: '', protocol: '', inbound_tag: '', endpoint_name: '', port: 0, action: 'route', outbound_tag: 'direct' })
const keyword = ref('')
const selectedNode = ref('')
const selectedAction = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)

const matchKinds = [
  { value: 'domain_suffix', label: '域名后缀' },
  { value: 'domains', label: '完整域名' },
  { value: 'cidrs', label: '目标网段' },
  { value: 'port', label: '目标端口' },
  { value: 'advanced', label: '高级条件' },
]
const nodeOptions = computed(() => appState.nodes.map((node) => ({ value: node.id, label: node.name })))
const nodeFilterOptions = computed(() => [{ value: '', label: '全部服务器' }, ...nodeOptions.value])
const outboundOptions = computed(() => [
  { value: 'direct', label: '服务器直连' },
  { value: 'block', label: '阻断连接' },
  ...outbounds.value.filter((item) => item.enabled && item.type !== 'direct').map((item) => ({ value: item.id, label: item.name })),
])
const accountOptions = computed(() => [
  { value: '', label: '适用于全部用户' },
  ...listeners.value
    .filter((listener) => listener.node_id === form.node_id)
    .flatMap((listener) => endpoints.value
      .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
      .map((endpoint) => ({ value: endpoint.name, label: endpoint.name, desc: listener.name }))),
])
const networkOptions = [{ value: '', label: '不限' }, { value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]
const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const filteredRows = computed(() => rows.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedAction.value && row.action !== selectedAction.value) return false
  return includesText([nodeNames.value[row.node_id], matchText(row), actionText(row), row.priority], keyword.value)
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

// 用户名单只有弹窗里的高级条件用得到，打开时再取，不拖慢列表刷新。
async function loadAccounts(nodeID) {
  const forNode = listeners.value.filter((listener) => listener.node_id === nodeID && listener.endpoint_count > 0)
  const results = await Promise.all(forNode.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
  endpoints.value = results.flatMap((result) => result.endpoints || [])
}

function resetForm() {
  Object.assign(form, { node_id: appState.nodes[0]?.id || '', priority: 100, enabled: true, network: '', protocol: '', inbound_tag: '', endpoint_name: '', port: 0, action: 'route', outbound_tag: 'direct' })
  matchKind.value = 'domain_suffix'
  values.value = []
}

function openCreate() {
  editing.value = null
  resetForm()
  loadAccounts(form.node_id)
  sheetOpen.value = true
}

function openEdit(row) {
  editing.value = row
  Object.assign(form, row)
  loadAccounts(row.node_id)
  form.outbound_tag = row.action === 'reject' ? 'block' : row.action === 'direct' ? 'direct' : row.outbound_tag
  const candidates = [['domains', 'domains'], ['domain_suffix', 'domain_suffix'], ['cidrs', 'cidrs']]
  const selected = candidates.find(([field]) => row[field]?.length)
  matchKind.value = selected?.[1] || (row.port ? 'port' : 'advanced')
  values.value = selected ? [...row[selected[0]]] : []
  sheetOpen.value = true
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
    sheetOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器访问规则保存失败')
  } finally { saving.value = false }
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '服务器', value: nodeNames.value[row.node_id] || row.node_id },
    { label: '匹配条件', value: matchText(row) },
    { label: '处理方式', value: actionText(row) },
    { label: '优先级', value: String(row.priority) },
    { label: '状态', value: row.enabled ? '启用' : '停用' },
  ]
})

function openActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const actions = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  const list = []
  if (canWrite.value) {
    list.push({ key: 'edit', label: '编辑' })
    list.push({ key: 'toggle', label: row.enabled ? '停用' : '启用' })
  }
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'edit') return openEdit(row)
  if (key === 'toggle') return toggle(row)
  if (key === 'delete') return remove(row)
}

async function toggle(row) {
  await post(`/rules/${row.id}/enabled`, { enabled: !row.enabled })
  ElMessage.success('状态已更新，系统正在自动应用')
  await load()
}

async function remove(row) {
  await ElMessageBox.confirm('确认删除这条服务器访问规则？', '删除访问规则', { type: 'warning' })
  await del(`/rules/${row.id}`)
  ElMessage.success('服务器访问规则已删除，正在自动应用')
  await load()
}

function matchText(row) {
  if (row.domains?.length) return `完整域名：${row.domains.join('、')}`
  if (row.domain_suffix?.length) return `域名后缀：${row.domain_suffix.join('、')}`
  if (row.cidrs?.length) return `目标网段：${row.cidrs.join('、')}`
  if (row.port) return `目标端口：${row.port}`
  return [row.network, row.protocol, row.inbound_tag, row.endpoint_name].filter(Boolean).join(' · ') || '全部流量'
}

function actionText(row) {
  return row.action === 'reject' ? '阻断连接' : (outboundOptions.value.find((item) => item.value === row.outbound_tag)?.label || row.outbound_tag || '服务器直连')
}

onMounted(load)
</script>

<template>
  <MPage title="服务器访问规则" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
    </template>

    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索匹配条件或处理方式" />
    <div class="m-filters">
      <MPicker v-model="selectedNode" chip :options="nodeFilterOptions" title="按服务器筛选" placeholder="全部服务器" />
      <MSegmented
        v-model="selectedAction"
        :options="[{ value: '', label: '全部' }, { value: 'direct', label: '直连' }, { value: 'outbound', label: '出口' }, { value: 'reject', label: '阻断' }]"
      />
    </div>
    <div class="m-count">共 {{ filteredRows.length }} 条规则</div>

    <article v-for="row in filteredRows" :key="row.id" class="m-item" :class="{ 'is-off': !row.enabled }">
      <button type="button" class="m-item__hit" @click="openActions(row)">
        <div class="m-item__head">
          <span class="m-item__title">{{ matchText(row) }}</span>
          <span v-if="!row.enabled" class="m-pill m-pill--info">停用</span>
          <i class="m-item__chevron" aria-hidden="true">›</i>
        </div>
        <div class="m-item__stats">
          <span class="m-stat">
            <b :class="{ 'm-danger': row.action === 'reject' }">{{ actionText(row) }}</b>
            <small>处理方式</small>
          </span>
          <span class="m-stat"><b>{{ row.priority }}</b><small>优先级</small></span>
        </div>
        <div class="m-item__meta">{{ nodeNames[row.node_id] || row.node_id }}</div>
      </button>
    </article>

    <div v-if="!filteredRows.length && !loading" class="m-empty">还没有访问规则</div>

    <MActionSheet v-model="actionsOpen" title="访问规则" :details="details" :actions="actions" @select="runAction" />

    <MSheet v-model="sheetOpen" :title="editing ? '编辑服务器访问规则' : '新建服务器访问规则'" full>
      <div class="m-field">
        <label class="m-field__label">服务器 <em>*</em></label>
        <MPicker :model-value="form.node_id" :options="nodeOptions" :disabled="Boolean(editing)" title="选择服务器" @update:model-value="form.node_id = $event; form.endpoint_name = ''; loadAccounts($event)" />
      </div>
      <div class="m-field">
        <label class="m-field__label">匹配方式</label>
        <MSegmented v-model="matchKind" :options="matchKinds" />
      </div>
      <div v-if="['domain_suffix', 'domains', 'cidrs'].includes(matchKind)" class="m-field">
        <label class="m-field__label">{{ matchKind === 'cidrs' ? '网段列表' : '域名列表' }}</label>
        <MValueList v-model="values" :placeholder="matchKind === 'cidrs' ? '例如 10.0.0.0/8' : '例如 example.com'" />
      </div>
      <div v-if="matchKind === 'port'" class="m-field">
        <label class="m-field__label">目标端口</label>
        <el-input-number v-model="form.port" :min="1" :max="65535" controls-position="right" aria-label="目标端口" style="width: 100%" />
      </div>
      <template v-if="matchKind === 'advanced'">
        <div class="m-field">
          <label class="m-field__label">网络协议</label>
          <MPicker v-model="form.network" :options="networkOptions" title="选择网络协议" placeholder="不限" />
        </div>
        <div class="m-field">
          <label class="m-field__label">应用协议</label>
          <el-input v-model="form.protocol" aria-label="应用协议" placeholder="例如 DNS" />
        </div>
        <div class="m-field">
          <label class="m-field__label">接入服务标签</label>
          <el-input v-model="form.inbound_tag" aria-label="接入服务标签" />
        </div>
        <div class="m-field">
          <label class="m-field__label">接入用户</label>
          <MPicker v-model="form.endpoint_name" :options="accountOptions" title="选择接入用户" placeholder="适用于全部用户" />
        </div>
      </template>
      <div class="m-field">
        <label class="m-field__label">优先级</label>
        <el-input-number v-model="form.priority" :min="0" controls-position="right" aria-label="优先级" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">处理方式</label>
        <MPicker v-model="form.outbound_tag" :options="outboundOptions" title="选择处理方式" />
      </div>
      <template #footer>
        <el-button @click="sheetOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </MSheet>

    <template v-if="canWrite" #fab>
      <button type="button" class="m-fab" aria-label="新建访问规则" @click="openCreate">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>
