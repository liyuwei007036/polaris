<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { includesText } from '../../format'
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

// 停用的接入服务和用户不筛掉：一个分组里「洛杉矶 40 这条停用了」和「这一条
// 指向的东西已经没了」是两回事，都显示成「节点已失效」等于把前者说成了后者。
// 停用只是换个颜色，名字照写；选项里也留着，否则编辑一次就把它从分组里抹掉了。
const clientNodes = computed(() => endpoints.value.flatMap((endpoint) => {
  const listener = listeners.value.find((item) => item.id === endpoint.listener_id)
  if (!listener || !supported.has(listener.spec?.protocol)) return []
  const node = appState.nodes.find((item) => item.id === listener.node_id)
  const address = listener.connection_domain || node?.client_address || ''
  const enabled = Boolean(listener.enabled && endpoint.enabled)
  const protocol = protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol
  return [{
    id: endpoint.id,
    label: endpoint.alias || `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`,
    desc: `${protocol} · ${address ? `${address}:${listener.port}` : '未填写连接地址'}${enabled ? '' : ' · 已停用'}`,
    enabled,
    disabled: !address,
  }]
}))
const nodeStates = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item])))
const groupNames = computed(() => Object.fromEntries(groups.value.map((item) => [item.id, item.name])))
const memberOptions = computed(() => [
  ...clientNodes.value.map((node) => ({ value: `endpoint:${node.id}`, label: node.label, desc: node.desc, disabled: node.disabled, group: '接入节点' })),
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

// 三种状态要分开：正常、停用（名字照写，换个颜色）、真的指不到东西了。
function resolveMember(member) {
  if (member.kind === 'group') {
    const name = groupNames.value[member.id]
    return { name: name || '分组已失效', missing: !name, off: false }
  }
  const state = nodeStates.value[member.id]
  if (!state) return { name: '节点已失效', missing: true, off: false }
  return { name: state.label, missing: false, off: !state.enabled }
}

// 详情里一行一个，停用的橙、指不到的红。
const toneOf = (item) => (item.missing ? { text: item.name, tone: 'danger' } : item.off ? { text: item.name, tone: 'warning' } : item.name)

function memberLabel(member) {
  return resolveMember(member).name
}

// 一个分组挂着什么，其实是三个不同的问题：里面有几个节点、引用了几个别的
// 分组、有几条已经指不到东西了。原来卡上只有一个「成员」总数，把这三样
// 揉成一个数字，结果既数不出有多少个节点，也看不出里面混着几条坏的。
const summaries = computed(() => Object.fromEntries(groups.value.map((group) => {
  const members = group.members || []
  const nodes = members.filter((member) => member.kind !== 'group').map(resolveMember)
  const nested = members.filter((member) => member.kind === 'group').map(resolveMember)
  return [group.id, {
    nodes,
    nested,
    broken: [...nodes, ...nested].filter((member) => member.missing).length,
  }]
})))

const emptySummary = { nodes: [], nested: [], broken: 0 }
const summaryOf = (group) => summaries.value[group.id] || emptySummary

// 卡上只摆得下前几个名字，剩下的折成一个计数。全部铺开时十几枚标签会把
// 一张卡撑到大半屏，一屏就只剩两个分组。
const PREVIEW = 3
const previewNodes = (group) => summaryOf(group).nodes.slice(0, PREVIEW)
const restCount = (group) => Math.max(0, summaryOf(group).nodes.length - PREVIEW)

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

const details = computed(() => {
  const group = actionTarget.value
  if (!group) return []
  const summary = summaryOf(group)
  // 节点一行一个：挤成一段用「·」隔开时，十几个名字会流成一整块，
  // 既数不清有几个，也认不出具体是哪几个。
  const list = [
    { label: '选点方式', value: strategyNames[group.strategy] || group.strategy },
    { label: `节点（${summary.nodes.length}）`, list: summary.nodes.map(toneOf), empty: '这个分组里还没有节点' },
  ]
  if (summary.nested.length) list.push({ label: `引用分组（${summary.nested.length}）`, list: summary.nested.map(toneOf) })
  return list
})

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
  <MPage :loading="loading">
    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索分组或节点" />
    <div class="m-count">共 {{ filteredGroups.length }} 个分组</div>

    <article v-for="group in filteredGroups" :key="group.id" class="m-item">
      <button type="button" class="m-item__hit" @click="openActions(group)">
        <div class="m-item__head">
          <span class="m-item__title">{{ group.name }}</span>
          <span class="m-pill m-pill--accent">{{ strategyNames[group.strategy] }}</span>
        </div>
        <div class="m-item__stats">
          <span class="m-stat"><b>{{ summaryOf(group).nodes.length }}</b><small>节点</small></span>
          <span class="m-stat">
            <b :class="{ 'is-muted': !summaryOf(group).nested.length }">{{ summaryOf(group).nested.length }}</b>
            <small>引用分组</small>
          </span>
          <span class="m-stat">
            <b :class="summaryOf(group).broken ? 'm-danger' : 'is-muted'">{{ summaryOf(group).broken }}</b>
            <small>已失效</small>
          </span>
        </div>
        <div v-if="previewNodes(group).length" class="picks">
          <span
            v-for="(item, index) in previewNodes(group)"
            :key="index"
            class="pick"
            :class="{ 'is-off': item.off, 'is-broken': item.missing }"
          >{{ item.name }}</span>
          <span v-if="restCount(group)" class="pick pick--more">还有 {{ restCount(group) }} 个</span>
        </div>
        <div v-else class="m-item__meta">还没有节点，客户端会拿到一个空分组</div>
      </button>
    </article>

    <div v-if="!filteredGroups.length && !loading" class="m-empty">还没有代理分组</div>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name" :details="details" :actions="actions" @select="runAction" />

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
        <label class="m-field__label">包含的节点 <em>*</em></label>
        <MPicker v-model="memberKeys" :options="memberOptions" multiple title="选择节点或分组" placeholder="选择节点或代理分组" />
        <div class="m-field__hint">没有填写连接地址的节点无法加入分组。</div>
      </div>
      <template #footer>
        <el-button @click="sheetOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </MSheet>

    <template v-if="canWrite" #fab>
      <button type="button" class="m-fab" aria-label="新建代理分组" @click="openForm()">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
/* 前几个节点名摆成标签，剩下的折成一个计数：一眼能认出这个分组里
   大致是哪一批线路，又不会被十几枚标签撑成大半屏。 */
.picks { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 9px; }
.pick {
  max-width: 100%;
  padding: 4px 9px;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .09);
  border-radius: 8px;
  font-size: 11.5px;
  line-height: 1.5;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pick--more { color: var(--sb-muted); background: transparent; border: 1px dashed var(--sb-line); }
/* 停用的照常写名字，只换颜色；真的指不到东西了才是红的。 */
.pick.is-off { color: #fcd34d; background: rgba(251, 191, 36, .10); }
.pick.is-broken { color: var(--sb-danger); background: rgba(251, 113, 133, .10); }
</style>
