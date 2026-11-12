<script setup>
import { computed, inject, onMounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { waitForTask } from '../../live'
import { includesText } from '../../format'
import { protocolMap } from '../../protocols'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'
import MQrSheet from '../components/MQrSheet.vue'
import MListenerForm from '../components/MListenerForm.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
const listeners = ref([])
const outbounds = ref([])
const allEndpoints = ref([])
const dnsRecords = ref([])
const endpointMap = ref({})
const formOpen = ref(false)
const editing = ref(null)
const copying = ref(null)
const copySourceID = ref('')
const keyword = ref('')
const selectedNode = ref('')
const selectedStatus = ref('')
const visible = ref(20)
const qrOpen = ref(false)
const qrLoading = ref(false)
const qrTitle = ref('')
const qrItems = ref([])
const actionsOpen = ref(false)
const actionTarget = ref(null)

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((item) => [item.id, item.name])))
const nodeOptions = computed(() => [{ value: '', label: '全部服务器' }, ...appState.nodes.map((node) => ({ value: node.id, label: node.name }))])
const formEndpoints = computed(() => {
  const source = editing.value?.id || copySourceID.value
  return source ? endpointMap.value[source] || [] : []
})
const filteredListeners = computed(() => listeners.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedStatus.value && String(row.enabled) !== selectedStatus.value) return false
  return includesText([nodeNames.value[row.node_id], row.name, row.connection_domain, row.port, row.spec?.protocol, row.spec?.transport?.type, securityLabel(row)], keyword.value)
}))
const shown = computed(() => filteredListeners.value.slice(0, visible.value))

watch([keyword, selectedNode, selectedStatus], () => { visible.value = 20 })

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [listenerResult, outboundResult, endpointResult] = await Promise.all([
      api('/listeners'),
      api('/outbounds').catch(() => ({ outbounds: [] })),
      api('/endpoints').catch(() => ({ endpoints: [] })),
    ])
    listeners.value = listenerResult.listeners || []
    outbounds.value = outboundResult.outbounds || []
    allEndpoints.value = endpointResult.endpoints || []
    // 域名候选是次要信息，读取区域可能很慢，让它自己补上，不挡列表。
    loadDomainSuggestions().catch(() => { dnsRecords.value = [] })
  } finally {
    loading.value = false
  }
}

async function loadDomainSuggestions() {
  const settings = await api('/cloudflare/settings')
  if (!settings.connected) {
    dnsRecords.value = []
    return
  }
  const zone = await api('/cloudflare/records')
  dnsRecords.value = zone.records || []
}

async function loadEndpoints(listenerID) {
  const result = await api(`/listeners/${listenerID}/endpoints`).catch(() => ({ endpoints: [] }))
  endpointMap.value = { ...endpointMap.value, [listenerID]: result.endpoints || [] }
}

// 一眼分出协议：同一台服务器上常同时挂着 VLESS 和 Hysteria2，两者的端口
// 行为和排障方向都不一样，用颜色区分比逐条读字快。
const protocolPills = { vless: 'm-pill--accent', hysteria2: 'm-pill--success' }
function protocolPill(listener) {
  return protocolPills[listener.spec?.protocol] || 'm-pill--info'
}

function protocolLabel(listener) {
  return protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol || '未知协议'
}

function transportLabel(listener) {
  const network = (listener.spec?.network || 'tcp').toUpperCase()
  return listener.spec?.transport?.type ? `${network} · ${listener.spec.transport.type}` : network
}

function securityLabel(listener) {
  if (listener.spec?.reality?.enabled) return 'Reality'
  if (listener.spec?.tls?.enabled) return 'TLS'
  return '无加密'
}

function openActions(listener) {
  actionTarget.value = listener
  actionsOpen.value = true
}

function addressText(listener) {
  return `${listener.connection_domain || '服务器地址'}:${listener.port}`
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '所在服务器', value: nodeNames.value[row.node_id] || row.node_id },
    { label: '连接地址', value: addressText(row), mono: true },
    { label: '协议', value: protocolLabel(row) },
    { label: '加密', value: securityLabel(row) },
    { label: '传输', value: transportLabel(row) },
    { label: '节点数', value: `${row.endpoint_count ?? 0} 个` },
    { label: '状态', value: row.enabled ? '启用' : '停用' },
  ]
})

const actions = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  const list = [{ key: 'qr', label: '节点二维码', hint: '每个节点一条链接' }]
  if (canWrite.value) {
    list.push({ key: 'edit', label: '编辑' })
    list.push({ key: 'copy', label: '复制到其它服务器', hint: '带入配置，保存前可改' })
    list.push({ key: 'toggle', label: row.enabled ? '停用' : '启用' })
  }
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'qr') return showShareLinks(row)
  if (key === 'edit') return openEdit(row)
  if (key === 'copy') return openCopy(row)
  if (key === 'toggle') return toggle(row)
  if (key === 'delete') return removeListener(row)
}

// 一个服务下每个用户都有各自的节点链接，全部取回来让操作者挑要扫哪一条。
async function showShareLinks(listener) {
  qrTitle.value = `${listener.name} · 节点链接`
  qrItems.value = []
  qrLoading.value = true
  qrOpen.value = true
  try {
    const result = await api(`/listeners/${listener.id}/share-links`)
    qrItems.value = (result.share_links || []).map((link) => ({
      key: link.endpoint_id,
      label: link.alias || link.name,
      value: link.link,
    }))
  } catch (error) {
    qrOpen.value = false
    ElMessage.error(error instanceof Error ? error.message : '节点链接获取失败，请稍后重试')
  } finally {
    qrLoading.value = false
  }
}

function openCreate() {
  editing.value = null
  copying.value = null
  copySourceID.value = ''
  api('/outbounds').then((res) => { if (res.outbounds) outbounds.value = res.outbounds }).catch(() => {})
  api('/endpoints').then((res) => { if (res.endpoints) allEndpoints.value = res.endpoints }).catch(() => {})
  formOpen.value = true
}

async function openEdit(listener) {
  api('/outbounds').then((res) => { if (res.outbounds) outbounds.value = res.outbounds }).catch(() => {})
  api('/endpoints').then((res) => { if (res.endpoints) allEndpoints.value = res.endpoints }).catch(() => {})
  await loadEndpoints(listener.id)
  editing.value = listener
  copying.value = null
  copySourceID.value = ''
  formOpen.value = true
}

// 复制只是把表单填好，选定目标服务器并保存之前什么都不会创建。
async function openCopy(listener) {
  api('/outbounds').then((res) => { if (res.outbounds) outbounds.value = res.outbounds }).catch(() => {})
  api('/endpoints').then((res) => { if (res.endpoints) allEndpoints.value = res.endpoints }).catch(() => {})
  await loadEndpoints(listener.id)
  editing.value = null
  copySourceID.value = listener.id
  copying.value = { ...listener, id: '', listen_address: '0.0.0.0', backend_port: 0 }
  formOpen.value = true
}

async function saveListener(payload) {
  saving.value = true
  try {
    let applyTaskID = ''
    const trackApply = (id) => { if (id) applyTaskID = id }
    if (editing.value) {
      await put(`/listeners/${editing.value.id}`, payload.listener, { onTask: trackApply })
      await syncAccounts(editing.value.id, payload.accounts)
    } else {
      const accounts = payload.accounts.map(({ name, alias, enabled, outbound_id }) => ({ name, alias, enabled, outbound_id }))
      await post('/listeners/quick', { listener: payload.listener, accounts }, { onTask: trackApply })
    }
    const saved = editing.value ? '接入服务已保存' : copying.value ? '接入服务已复制并创建' : '接入服务已创建'
    formOpen.value = false
    copying.value = null
    copySourceID.value = ''
    await load()
    // 保存只写了数据库，配置是另一个任务下发到服务器的。不等这个任务就
    // 报成功，会把端口被占用之类的下发失败全部藏起来。
    if (!applyTaskID) {
      ElMessage.success(`${saved}，正在自动应用`)
      return
    }
    const result = await waitForTask(applyTaskID, 60000)
    if (result.status !== 'succeeded') {
      ElMessage.error(result.result_summary || '配置未能应用，请到「操作记录」查看原因')
      return
    }
    ElMessage.success(`${saved}并已应用`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '接入服务保存失败，请检查填写内容后重试')
  } finally {
    saving.value = false
  }
}

async function syncAccounts(listenerID, accounts) {
  const existing = endpointMap.value[listenerID] || []
  const existingByID = new Map(existing.map((endpoint) => [endpoint.id, endpoint]))
  const retained = new Set()
  for (const account of accounts) {
    if (!account.id) {
      const created = await post(`/listeners/${listenerID}/endpoints/quick`, {
        name: account.name,
        alias: account.alias,
        outbound_id: account.outbound_id,
      })
      if (!account.enabled) {
        await put(`/endpoints/${created.id}`, {
          listener_id: listenerID,
          name: account.name,
          alias: account.alias,
          enabled: false,
          outbound_id: account.outbound_id,
        })
      }
      continue
    }
    retained.add(account.id)
    const current = existingByID.get(account.id)
    if (!current) continue
    if (current.name !== account.name || (current.alias || '') !== account.alias || current.enabled !== account.enabled || (current.outbound_id || 'direct') !== account.outbound_id) {
      await put(`/endpoints/${account.id}`, {
        listener_id: listenerID,
        name: account.name,
        alias: account.alias,
        enabled: account.enabled,
        outbound_id: account.outbound_id,
      })
    }
  }
  for (const endpoint of existing) {
    if (!retained.has(endpoint.id)) await del(`/endpoints/${endpoint.id}`)
  }
}

async function toggle(listener) {
  try {
    await post(`/listeners/${listener.id}/enabled`, { enabled: !listener.enabled })
    ElMessage.success('状态已更新，系统正在自动应用')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '状态更新失败，请稍后重试')
  }
}

// 服务器拒绝删除时必须让操作者看见原因，否则行还在屏幕上，按钮像是坏的。
async function removeListener(listener) {
  try {
    await ElMessageBox.confirm(
      `删除“${listener.name}”会同时删除其中的所有节点，且无法恢复。`,
      '删除接入服务',
      { type: 'warning', confirmButtonText: '确认删除' },
    )
  } catch {
    return
  }
  try {
    await del(`/listeners/${listener.id}`)
    ElMessage.success('接入服务已删除，正在自动应用')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '接入服务删除失败，请稍后重试')
  }
}

onMounted(load)
</script>

<template>
  <MPage title="接入服务" :loading="loading">
    <template #actions>
      <el-button
        circle
        size="small"
        :icon="Refresh"
        :loading="loading"
        aria-label="刷新"
        @click="load()"
      />
    </template>
    <div class="m-listbar">
      <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索服务、协议或端口" />
      <div class="m-filters">
        <MPicker v-model="selectedNode" chip :options="nodeOptions" title="按服务器筛选" placeholder="全部服务器" />
        <MSegmented
          v-model="selectedStatus"
          :options="[{ value: '', label: '全部' }, { value: 'true', label: '启用' }, { value: 'false', label: '停用' }]"
        />
      </div>
    </div>

    <div class="m-count">共 {{ filteredListeners.length }} 个接入服务</div>

    <article
      v-for="row in shown"
      :key="row.id"
      class="m-inbound-card neu rise"
      :class="{ 'is-off': !row.enabled }"
      role="button"
      tabindex="0"
      @click="openActions(row)"
    >
      <div class="m-inbound-head">
        <div class="m-inbound-hero min-w-0 flex-1">
          <span class="m-inbound-name truncate" :title="row.name">{{ row.name }}</span>
          <span class="m-pill" :class="protocolPill(row)">{{ protocolLabel(row) }}</span>
        </div>
        <span class="m-inbound-status" :class="row.enabled ? 'is-enabled' : 'is-disabled'">
          {{ row.enabled ? '启用' : '停用' }}
        </span>
      </div>

      <div class="m-inbound-stats neu-inset">
        <div class="inbound-stat-col">
          <span class="stat-lbl">加密</span>
          <span class="stat-val font-mono">{{ securityLabel(row) }}</span>
        </div>
        <div class="stat-sep" />
        <div class="inbound-stat-col">
          <span class="stat-lbl">传输协议</span>
          <span class="stat-val font-mono">{{ transportLabel(row) }}</span>
        </div>
        <div class="stat-sep" />
        <div class="inbound-stat-col">
          <span class="stat-lbl">节点数</span>
          <span class="stat-val font-mono">{{ row.endpoint_count ?? 0 }} 个</span>
        </div>
      </div>

      <div class="m-inbound-footer">
        <span class="inbound-addr font-mono truncate">{{ addressText(row) }}</span>
        <span class="inbound-node-tag">{{ nodeNames[row.node_id] || row.node_id }}</span>
      </div>
    </article>

    <div v-if="!filteredListeners.length && !loading" class="m-empty">还没有接入服务</div>
    <button v-if="filteredListeners.length > visible" type="button" class="m-load-more" @click="visible += 20">
      加载更多（还有 {{ filteredListeners.length - visible }} 个）
    </button>

    <MActionSheet
      v-model="actionsOpen"
      :title="actionTarget?.name"
      :details="details"
      :actions="actions"
      @select="runAction"
    />

    <MListenerForm
      v-model="formOpen"
      :listener="editing"
      :template="copying"
      :nodes="appState.nodes"
      :listeners="listeners"
      :outbounds="outbounds"
      :all-endpoints="allEndpoints"
      :dns-records="dnsRecords"
      :endpoints="formEndpoints"
      :saving="saving"
      @save="saveListener"
    />

    <MQrSheet
      v-model="qrOpen"
      :title="qrTitle"
      :items="qrItems"
      :loading="qrLoading"
      empty-text="该服务下还没有可扫描的节点链接：服务或节点已停用，或服务器还没有填写客户端连接地址。"
    />

    <template v-if="canWrite" #fab>
      <button type="button" class="m-fab" aria-label="新建接入服务" @click="openCreate">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>


<style scoped>
.m-inbound-card {
  display: block;
  width: 100%;
  padding: 13px 14px 11px;
  margin-bottom: 10px;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  box-shadow: var(--sb-shadow-sm);
  cursor: pointer;
  text-align: left;
  user-select: none;
  transition: transform 0.15s ease, border-color 0.15s ease, box-shadow 0.15s ease;
}
.m-inbound-card:active {
  transform: scale(0.992);
}
.m-inbound-card.is-off {
  opacity: 0.65;
}
.m-inbound-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}
.m-inbound-hero {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.m-inbound-name {
  color: var(--sb-text);
  font-size: 15px;
  font-weight: 650;
  line-height: 1.35;
}
.m-inbound-status {
  flex-shrink: 0;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}
.m-inbound-status.is-enabled {
  background: var(--sb-success-soft);
  color: var(--sb-success);
  border: 1px solid color-mix(in oklab, var(--sb-success) 30%, transparent);
}
.m-inbound-status.is-disabled {
  background: var(--sb-surface-3);
  color: var(--sb-muted);
  border: 1px solid var(--sb-line);
}

.m-inbound-stats {
  display: flex;
  align-items: center;
  margin-top: 10px;
  padding: 8px 10px;
  border-radius: 10px;
  background: var(--neu-inset-bg);
  border: 1px solid var(--sb-line);
  box-shadow: var(--sh-inset);
}
.inbound-stat-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.stat-lbl {
  font-size: 10.5px;
  color: var(--sb-muted);
}
.stat-val {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--sb-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.stat-sep {
  width: 1px;
  height: 22px;
  background: var(--sb-line);
  margin: 0 8px;
  flex-shrink: 0;
}

.m-inbound-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-top: 9px;
  padding-top: 8px;
  border-top: 1px solid var(--sb-line);
  font-size: 12px;
  color: var(--sb-muted);
}
.inbound-addr {
  flex: 1;
  min-width: 0;
  color: var(--sb-muted);
  font-size: 11.5px;
}
.inbound-node-tag {
  flex-shrink: 0;
  padding: 1px 6px;
  border-radius: 5px;
  font-size: 11px;
  color: var(--sb-muted);
  background: var(--sb-surface-3);
  border: 1px solid var(--sb-line);
}
</style>
