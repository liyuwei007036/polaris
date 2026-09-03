<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'
import { protocolMap } from '../protocols'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const editing = ref(null)
const groups = ref([])
const listeners = ref([])
const endpoints = ref([])
const form = reactive({ name: '', strategy: 'select', members: [] })
const keyword = ref('')
const selectedStrategy = ref('')
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
  const detail = `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`
  return [{
    id: endpoint.id,
    label: endpoint.alias || detail,
    detail,
    protocol: protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol,
    address: address ? `${address}:${listener.port}` : '未填写连接地址',
    enabled: Boolean(listener.enabled && endpoint.enabled),
    disabled: !address,
  }]
}))
const nodeStates = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item])))
const groupNames = computed(() => Object.fromEntries(groups.value.map((item) => [item.id, item.name])))
// 一列标签挤满整行时数不出有几个，也认不出是哪几个：前几个铺开，其余折成计数。
const memberPreviewCount = 5
const formValid = computed(() => Boolean(form.name.trim() && form.members.length))
const filteredGroups = computed(() => groups.value.filter((group) => {
  if (selectedStrategy.value && group.strategy !== selectedStrategy.value) return false
  return includesText([group.name, strategyNames[group.strategy], ...(group.members || []).map(memberLabel)], keyword.value)
}))

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

function resetForm(group = null) {
  editing.value = group
  Object.assign(form, group ? {
    name: group.name,
    strategy: group.strategy,
    members: (group.members || []).map((member) => ({ kind: member.kind, id: member.id })),
  } : { name: '', strategy: 'select', members: [] })
  dialogVisible.value = true
}

function memberKeys() {
  return form.members.map((member) => `${member.kind}:${member.id}`)
}

function setMembers(keys) {
  form.members = keys.map((key) => {
    const separator = key.indexOf(':')
    return { kind: key.slice(0, separator), id: key.slice(separator + 1) }
  })
}

// 三种状态要分开：正常、停用（名字照写，换个颜色）、真的指不到东西了。
function resolveMember(member) {
  const key = `${member.kind}:${member.id}`
  if (member.kind === 'group') {
    const name = groupNames.value[member.id]
    return { key, kind: 'group', name: name || '分组已失效', missing: !name, off: false }
  }
  const state = nodeStates.value[member.id]
  if (!state) return { key, kind: 'endpoint', name: '节点已失效', missing: true, off: false }
  return { key, kind: 'endpoint', name: state.label, missing: false, off: !state.enabled }
}

function memberTagType(member) {
  if (member.missing) return 'danger'
  if (member.off) return 'warning'
  return member.kind === 'group' ? 'primary' : 'info'
}

function memberLabel(member) {
  return resolveMember(member).name
}

// 一个分组挂着什么，其实是三个不同的问题：里面有几个节点、引用了几个别的
// 分组、有几条已经指不到东西了。原来表格里只有一列铺满的标签，把这三样混在
// 一起，结果既数不出有多少个节点，也看不出里面混着几条坏的。
const summaries = computed(() => Object.fromEntries(groups.value.map((group) => {
  const all = (group.members || []).map(resolveMember)
  return [group.id, {
    all,
    nodes: all.filter((item) => item.kind !== 'group'),
    nested: all.filter((item) => item.kind === 'group'),
    broken: all.filter((item) => item.missing).length,
  }]
})))
const emptySummary = { all: [], nodes: [], nested: [], broken: 0 }
const summaryOf = (group) => summaries.value[group.id] || emptySummary
const previewMembers = (group) => summaryOf(group).all.slice(0, memberPreviewCount)
const restMembers = (group) => summaryOf(group).all.slice(memberPreviewCount)

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
    dialogVisible.value = false
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
  <div class="page-shell">
    <PageHeader title="代理分组">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="resetForm()">新建</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索分组或节点" style="width: 260px" />
        <el-select v-model="selectedStrategy" clearable placeholder="全部策略" style="width: 160px"><el-option v-for="(label, value) in strategyNames" :key="value" :label="label" :value="value" /></el-select>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredGroups" :loading="loading" empty-text="还没有代理分组">
          <el-table-column label="分组名称" min-width="180" prop="name" />
          <el-table-column label="策略" width="120"><template #default="{ row }">{{ strategyNames[row.strategy] }}</template></el-table-column>
          <!-- 数量单独成列：客户端拿到的是这些节点，有几个、坏了几个是这一页
               最先要回答的问题，藏在一列标签里得自己数。 -->
          <el-table-column label="节点" width="112" align="center">
            <template #default="{ row }">
              <span class="count">{{ summaryOf(row).nodes.length }}</span>
              <span v-if="summaryOf(row).broken" class="count__broken">{{ summaryOf(row).broken }} 个已失效</span>
            </template>
          </el-table-column>
          <el-table-column label="引用分组" width="96" align="center">
            <template #default="{ row }">
              <span :class="summaryOf(row).nested.length ? 'count' : 'subtle'">{{ summaryOf(row).nested.length }}</span>
            </template>
          </el-table-column>
          <el-table-column label="包含的节点" min-width="320">
            <template #default="{ row }">
              <template v-if="summaryOf(row).all.length">
                <el-tag
                  v-for="member in previewMembers(row)"
                  :key="member.key"
                  :type="memberTagType(member)"
                  class="member-tag"
                >
                  <span v-if="member.kind === 'group'" class="member-tag__kind">分组</span>
                  {{ member.name }}
                </el-tag>
                <el-tooltip v-if="restMembers(row).length" placement="top">
                  <template #content>
                    <div v-for="member in restMembers(row)" :key="member.key">{{ member.name }}</div>
                  </template>
                  <el-tag class="member-tag" type="info" effect="plain">还有 {{ restMembers(row).length }} 个</el-tag>
                </el-tooltip>
              </template>
              <span v-else class="subtle">还没有节点，客户端会拿到一个空分组</span>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="150" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="resetForm(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑代理分组' : '新建代理分组'" width="min(760px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="分组名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="策略" required>
          <el-select v-model="form.strategy" style="width: 100%">
            <el-option v-for="(label, value) in strategyNames" :key="value" :label="label" :value="value" />
          </el-select>
        </el-form-item>
        <el-form-item label="包含的节点" required>
          <!-- 一个分组可能挂几十个节点，标签折叠起来，选项里带地区图标和线路信息便于分辨。 -->
          <el-select
            :model-value="memberKeys()"
            multiple
            filterable
            collapse-tags
            collapse-tags-tooltip
            :max-collapse-tags="6"
            style="width: 100%"
            aria-label="包含的节点"
            placeholder="选择节点或代理分组"
            @update:model-value="setMembers"
          >
            <el-option-group label="接入节点">
              <el-option v-for="node in clientNodes" :key="`endpoint:${node.id}`" :value="`endpoint:${node.id}`" :label="node.label" :disabled="node.disabled">
                <span class="option-name">{{ node.label }}</span>
                <span class="option-meta">{{ node.detail }} · {{ node.protocol }} · {{ node.address }}{{ node.enabled ? '' : ' · 已停用' }}</span>
              </el-option>
            </el-option-group>
            <el-option-group label="代理分组">
              <el-option v-for="group in groups.filter((item) => item.id !== editing?.id)" :key="`group:${group.id}`" :value="`group:${group.id}`" :label="group.name" />
            </el-option-group>
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-alert { margin-bottom: 16px; }
.member-tag { margin: 3px 6px 3px 0; }
.count { display: block; font-weight: 600; font-variant-numeric: tabular-nums; }
.count__broken { display: block; margin-top: 2px; color: var(--sb-danger); font-size: 12px; }
.member-tag__kind { margin-right: 5px; color: var(--sb-muted); }
.option-name { display: inline-flex; align-items: center; }
.option-meta { float: right; margin-left: 18px; color: var(--sb-muted); font-size: 12px; }
@media (max-width: 640px) { .option-meta { display: none; } }
</style>
