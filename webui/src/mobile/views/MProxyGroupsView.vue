<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { includesText } from '../../format'
import { regionFlag } from '../../flags'
import { protocolMap } from '../../protocols'
import MPage from '../components/MPage.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const sheetOpen = ref(false)
const editing = ref(null)
const groups = ref([])
const listeners = ref([])
const endpoints = ref([])
const form = reactive({ name: '', strategy: 'select', members: [] })
const keyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const supported = new Set(['vless', 'hysteria2'])
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }

const clientNodes = computed(() => listeners.value
  .filter((listener) => listener.enabled && supported.has(listener.spec?.protocol))
  .flatMap((listener) => endpoints.value
    .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
    .map((endpoint) => {
      const node = appState.nodes.find((item) => item.id === listener.node_id)
      const address = listener.connection_domain || node?.client_address || ''
      const name = endpoint.alias || `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`
      return {
        id: endpoint.id,
        // 地区图标先从节点别名认，认不出再看服务器名。
        flag: regionFlag(name) || regionFlag(node?.name),
        label: name,
        desc: `${protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol} · ${address ? `${address}:${listener.port}` : '未填写连接地址'}`,
        disabled: !address,
      }
    })))
const nodeNames = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item.label])))
const nodeFlags = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item.flag])))
const groupNames = computed(() => Object.fromEntries(groups.value.map((item) => [item.id, item.name])))
const memberOptions = computed(() => [
  ...clientNodes.value.map((node) => ({ value: `endpoint:${node.id}`, label: node.label, desc: node.desc, flag: node.flag, disabled: node.disabled, group: '接入节点' })),
  ...groups.value.filter((item) => item.id !== editing.value?.id).map((group) => ({ value: `group:${group.id}`, label: group.name, desc: strategyNames[group.strategy], group: '代理分组' })),
])
const memberKeys = computed({
  get: () => form.members.map((member) => `${member.kind}:${member.id}`),
  set: (keys) => {
    form.members = keys.map((key) => {
      const separator = key.indexOf(':')
      return { kind: key.slice(0, separator), id: key.slice(separator + 1) }
    })
  },
})
const formValid = computed(() => Boolean(form.name.trim() && form.members.length))
const filteredGroups = computed(() => groups.value.filter((group) => includesText(
  [group.name, strategyNames[group.strategy], ...(group.members || []).map(memberLabel)],
  keyword.value,
)))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [groupResult, listenerResult, endpointResult] = await Promise.all([
      api('/mihomo/proxy-groups'),
      api('/listeners'),
      api('/endpoints').catch(() => ({ endpoints: [] })),
    ])
    groups.value = groupResult.proxy_groups || []
    listeners.value = listenerResult.listeners || []
    endpoints.value = endpointResult.endpoints || []
  } finally {
    loading.value = false
  }
}

function memberLabel(member) {
  return member.kind === 'group' ? groupNames.value[member.id] || '分组已失效' : nodeNames.value[member.id] || '节点已失效'
}

function memberFlag(member) {
  return member.kind === 'group' ? '' : nodeFlags.value[member.id] || ''
}

function openForm(group = null) {
  editing.value = group
  Object.assign(form, group ? {
    name: group.name,
    strategy: group.strategy,
    members: (group.members || []).map((member) => ({ kind: member.kind, id: member.id })),
  } : { name: '', strategy: 'select', members: [] })
  sheetOpen.value = true
}

function openActions(group) {
  actionTarget.value = group
  actionsOpen.value = true
}

const actions = computed(() => {
  if (!actionTarget.value) return []
  const list = []
  if (canWrite.value) list.push({ key: 'edit', label: '编辑' })
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  if (key === 'edit') return openForm(actionTarget.value)
  if (key === 'delete') return remove(actionTarget.value)
}

async function save() {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      strategy: form.strategy,
      members: form.members.map((member) => ({ kind: member.kind, id: member.id })),
    }
    if (editing.value) await put(`/mihomo/proxy-groups/${editing.value.id}`, payload)
    else await post('/mihomo/proxy-groups', payload)
    ElMessage.success(editing.value ? '代理分组已保存' : '代理分组已创建')
    sheetOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '代理分组保存失败，请检查填写内容后重试')
  } finally {
    saving.value = false
  }
}

async function remove(group) {
  await ElMessageBox.confirm(`确认删除代理分组“${group.name}”？`, '删除代理分组', { type: 'warning' })
  await del(`/mihomo/proxy-groups/${group.id}`)
  ElMessage.success('代理分组已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <MPage title="代理分组" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" @click="load" />
      <el-button v-if="canWrite" type="primary" :icon="Plus" circle aria-label="新建代理分组" @click="openForm()" />
    </template>

    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索分组或成员" />
    <div class="m-count">共 {{ filteredGroups.length }} 个分组</div>

    <article v-for="group in filteredGroups" :key="group.id" class="m-card">
      <div class="m-card__top">
        <span class="m-card__title">{{ group.name }}</span>
        <span class="m-pill m-pill--accent">{{ strategyNames[group.strategy] }}</span>
        <button v-if="actions.length || canWrite || isAdmin" type="button" class="m-more-btn" :aria-label="`${group.name} 的操作`" @click="openActions(group)">⋯</button>
      </div>
      <div class="m-card__row"><span>{{ (group.members || []).length }} 个成员</span></div>
      <div class="m-tags">
        <span v-for="member in group.members || []" :key="`${member.kind}:${member.id}`" class="m-pill" :class="member.kind === 'group' ? 'm-pill--accent' : 'm-pill--info'">
          <span v-if="memberFlag(member)" class="region-flag">{{ memberFlag(member) }}</span>{{ memberLabel(member) }}
        </span>
      </div>
    </article>

    <div v-if="!filteredGroups.length && !loading" class="m-empty">还没有代理分组</div>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name" :actions="actions" @select="runAction" />

    <MSheet v-model="sheetOpen" :title="editing ? '编辑代理分组' : '新建代理分组'" full>
      <div class="m-field">
        <label class="m-field__label">分组名称 <em>*</em></label>
        <el-input v-model="form.name" maxlength="128" aria-label="分组名称" />
      </div>
      <div class="m-field">
        <label class="m-field__label">策略 <em>*</em></label>
        <MPicker v-model="form.strategy" :options="Object.entries(strategyNames).map(([value, label]) => ({ value, label }))" title="选择策略" />
      </div>
      <div class="m-field">
        <label class="m-field__label">分组成员 <em>*</em></label>
        <MPicker v-model="memberKeys" :options="memberOptions" multiple title="选择分组成员" placeholder="选择节点或代理分组" />
        <div class="m-field__hint">没有填写连接地址的节点无法加入分组。</div>
      </div>
      <template #footer>
        <el-button @click="sheetOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </MSheet>
  </MPage>
</template>
