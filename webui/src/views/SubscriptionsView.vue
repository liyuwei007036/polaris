<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Delete, Download, Edit, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'
import { protocolMap } from '../protocols'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')
const navigate = inject('navigate')
const loading = ref(false)
const saving = ref(false)
const configs = ref([])
const groups = ref([])
const profiles = ref([])
const listeners = ref([])
const endpoints = ref([])
const editVisible = ref(false)
const editing = ref(null)
const editForm = reactive({ name: '', proxy_group_ids: [], routing_profile_id: '' })
const quick = reactive({ name: '', endpoint_ids: [], group_name: '', strategy: 'select', routing_profile_id: '' })
const supported = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'socks', 'http'])

const groupNames = computed(() => Object.fromEntries(groups.value.map((item) => [item.id, item.name])))
const profileNames = computed(() => Object.fromEntries(profiles.value.map((item) => [item.id, item.name])))
const clientNodes = computed(() => listeners.value
  .filter((listener) => listener.enabled && supported.has(listener.spec?.protocol))
  .flatMap((listener) => endpoints.value
    .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
    .map((endpoint) => {
      const node = appState.nodes.find((item) => item.id === listener.node_id)
      return {
        id: endpoint.id,
        node_name: node?.name || listener.node_id,
        listener_name: listener.name,
        account_name: endpoint.name,
        protocol: protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol,
        address: node?.client_address ? `${node.client_address}:${listener.port}` : '未填写连接地址',
        disabled: !node?.client_address,
      }
    })))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [configResult, groupResult, profileResult, listenerResult] = await Promise.all([
      api('/mihomo/client-configs'), api('/mihomo/proxy-groups'), api('/mihomo/routing-profiles'), api('/listeners'),
    ])
    configs.value = configResult.client_configs || []
    groups.value = groupResult.proxy_groups || []
    profiles.value = profileResult.routing_profiles || []
    listeners.value = listenerResult.listeners || []
    const endpointResults = await Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = endpointResults.flatMap((result) => result.endpoints || [])
    if (!quick.routing_profile_id && profiles.value.length) quick.routing_profile_id = profiles.value[0].id
  } finally { loading.value = false }
}

function subscriptionURL(config) { return config.subscription_path ? `${location.origin}${config.subscription_path}` : '' }
function triggerDownload(config) {
  const url = subscriptionURL(config)
  if (!url) return
  const link = document.createElement('a')
  link.href = url
  link.download = `${config.name || 'mihomo'}.yaml`
  document.body.appendChild(link)
  link.click()
  link.remove()
}

async function copySubscription(config) {
  await navigator.clipboard.writeText(subscriptionURL(config))
  ElMessage.success('客户端更新地址已复制')
}

async function rotateSubscription(config) {
  if (config.subscription_path) await ElMessageBox.confirm('生成新地址后，原地址会立即失效。是否继续？', '更换客户端更新地址', { type: 'warning' })
  const result = await post(`/mihomo/client-configs/${config.id}/subscription/rotate`, {})
  config.subscription_path = result.subscription_path
  await copySubscription(config)
}

async function createAndDownload() {
  if (!quick.name.trim() || !quick.endpoint_ids.length || !quick.routing_profile_id) return
  saving.value = true
  let group
  try {
    group = await post('/mihomo/proxy-groups', {
      name: quick.group_name.trim() || `${quick.name.trim()}节点组`, strategy: quick.strategy,
      endpoint_ids: quick.endpoint_ids, aliases: {},
    })
    const config = await post('/mihomo/client-configs', {
      name: quick.name.trim(), proxy_group_ids: [group.id], routing_profile_id: quick.routing_profile_id,
    })
    const token = await post(`/mihomo/client-configs/${config.id}/subscription/rotate`, {})
    config.subscription_path = token.subscription_path
    configs.value.push(config)
    groups.value.push(group)
    triggerDownload(config)
    Object.assign(quick, { name: '', endpoint_ids: [], group_name: '', strategy: 'select', routing_profile_id: profiles.value[0]?.id || '' })
    ElMessage.success('客户端配置文件已生成并开始下载')
  } catch (error) {
    if (group && isAdmin.value) await del(`/mihomo/proxy-groups/${group.id}`).catch(() => {})
    throw error
  } finally { saving.value = false }
}

function openEdit(config) {
  editing.value = config
  Object.assign(editForm, { name: config.name, proxy_group_ids: [...config.proxy_group_ids], routing_profile_id: config.routing_profile_id })
  editVisible.value = true
}

async function saveEdit() {
  saving.value = true
  try {
    await put(`/mihomo/client-configs/${editing.value.id}`, editForm)
    editVisible.value = false
    ElMessage.success('客户端配置已更新，原更新地址仍可继续使用')
    await load()
  } finally { saving.value = false }
}

async function remove(config) {
  await ElMessageBox.confirm(`确认删除客户端配置“${config.name}”？`, '删除客户端配置', { type: 'warning' })
  await del(`/mihomo/client-configs/${config.id}`)
  ElMessage.success('客户端配置已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="客户端配置" description="选择客户端连接、节点组和访问规则，然后下载 Mihomo 配置文件">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content subscription-workspace">
      <section class="build-flow">
        <div class="flow-heading"><div><strong>生成客户端配置</strong><span>此操作只生成供客户端使用的文件，不会修改服务器配置。</span></div><span class="step-count">3 步完成</span></div>
        <div class="flow-grid">
          <div class="flow-step">
            <span class="step-number">1</span>
            <div class="step-copy"><strong>选择客户端连接</strong><span>可以选择不同服务器上的多个接入用户</span></div>
            <el-select v-model="quick.endpoint_ids" multiple filterable collapse-tags :max-collapse-tags="3" placeholder="请选择一个或多个客户端连接" style="width: 100%">
              <el-option v-for="node in clientNodes" :key="node.id" :value="node.id" :label="`${node.node_name} · ${node.listener_name} · ${node.account_name}`" :disabled="node.disabled"><span>{{ node.node_name }} · {{ node.listener_name }}</span><span class="option-meta">{{ node.protocol }} · {{ node.address }}</span></el-option>
            </el-select>
            <el-alert v-if="!clientNodes.length" title="暂无可用的客户端连接。请先创建接入服务和用户，并填写服务器的客户端连接地址。" type="warning" :closable="false" />
          </div>
          <div class="flow-step">
            <span class="step-number">2</span>
            <div class="step-copy"><strong>设置客户端节点组</strong><span>决定客户端如何选择可用连接</span></div>
            <el-input v-model="quick.group_name" placeholder="节点组名称，留空时自动生成" />
            <el-select v-model="quick.strategy" style="width: 100%"><el-option label="手动选择" value="select" /><el-option label="自动测速" value="url-test" /><el-option label="故障自动切换" value="fallback" /></el-select>
          </div>
          <div class="flow-step">
            <span class="step-number">3</span>
            <div class="step-copy"><strong>选择客户端访问规则</strong><span>决定不同网站直接访问、使用节点组或被阻止</span></div>
            <el-input v-model="quick.name" placeholder="配置名称，例如：手机日常" />
            <el-select v-model="quick.routing_profile_id" placeholder="请选择客户端访问规则" style="width: 100%"><el-option v-for="profile in profiles" :key="profile.id" :value="profile.id" :label="profile.name" /></el-select>
            <el-button v-if="!profiles.length" link type="primary" @click="navigate('routing-profiles')">先创建访问规则</el-button>
          </div>
        </div>
        <div class="flow-action"><span>下载后可直接导入 Mihomo 或 Clash Meta 客户端。</span><el-button v-if="canWrite" type="primary" :icon="Download" :loading="saving" :disabled="!quick.name.trim() || !quick.endpoint_ids.length || !quick.routing_profile_id" @click="createAndDownload">生成并下载配置</el-button></div>
      </section>

      <section class="saved-configs">
        <div class="section-heading"><div><strong>已保存配置</strong><span>客户端通过更新地址获取最新连接信息和访问规则。</span></div></div>
        <div class="table-panel">
          <el-table :data="configs">
            <el-table-column label="配置名称" min-width="180" prop="name" />
            <el-table-column label="客户端节点组" min-width="220"><template #default="{ row }"><el-tag v-for="id in row.proxy_group_ids" :key="id" type="info" style="margin-right: 6px">{{ groupNames[id] || '节点组已失效' }}</el-tag></template></el-table-column>
            <el-table-column label="客户端访问规则" min-width="180"><template #default="{ row }">{{ profileNames[row.routing_profile_id] || '访问规则已失效' }}</template></el-table-column>
            <el-table-column label="更新地址" width="120"><template #default="{ row }"><el-tag :type="row.subscription_path ? 'success' : 'warning'">{{ row.subscription_path ? '可用' : '未生成' }}</el-tag></template></el-table-column>
            <el-table-column label="操作" width="360" fixed="right"><template #default="{ row }"><el-button v-if="row.subscription_path" type="primary" link :icon="Download" @click="triggerDownload(row)">下载配置</el-button><el-button v-if="row.subscription_path" link :icon="CopyDocument" @click="copySubscription(row)">复制更新地址</el-button><el-button v-if="canWrite" link :icon="RefreshRight" @click="rotateSubscription(row)">{{ row.subscription_path ? '更换地址' : '生成地址' }}</el-button><el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column>
          </el-table>
        </div>
      </section>
    </main>

    <el-dialog v-model="editVisible" title="编辑客户端配置" width="min(620px, 94vw)">
      <el-form label-position="top"><el-form-item label="配置名称" required><el-input v-model="editForm.name" /></el-form-item><el-form-item label="客户端节点组" required><el-select v-model="editForm.proxy_group_ids" multiple style="width: 100%"><el-option v-for="group in groups" :key="group.id" :value="group.id" :label="group.name" /></el-select></el-form-item><el-form-item label="客户端访问规则" required><el-select v-model="editForm.routing_profile_id" style="width: 100%"><el-option v-for="profile in profiles" :key="profile.id" :value="profile.id" :label="profile.name" /></el-select></el-form-item></el-form>
      <template #footer><el-button @click="editVisible = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!editForm.name.trim() || !editForm.proxy_group_ids.length || !editForm.routing_profile_id" @click="saveEdit">保存</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.subscription-workspace { display: grid; grid-template-columns: minmax(0, 1fr); gap: 28px; min-width: 0; }
.build-flow, .saved-configs { min-width: 0; }
.build-flow { padding: 22px; background: #fff; border: 1px solid var(--sb-border); border-radius: 9px; }
.flow-heading, .section-heading, .flow-action { display: flex; align-items: center; justify-content: space-between; gap: 20px; }
.flow-heading strong, .flow-heading span, .section-heading strong, .section-heading span { display: block; }
.flow-heading > div > span, .section-heading span { margin-top: 4px; color: var(--sb-muted); font-size: 12px; }
.step-count { color: var(--sb-accent); font-size: 12px; font-weight: 650; }
.flow-grid { display: grid; grid-template-columns: 1.2fr .9fr 1fr; margin: 20px 0; border-top: 1px solid var(--sb-border); border-bottom: 1px solid var(--sb-border); }
.flow-step { display: flex; flex-direction: column; gap: 12px; min-width: 0; padding: 20px; border-right: 1px solid var(--sb-border); }
.flow-step:first-child { padding-left: 0; }
.flow-step:last-child { padding-right: 0; border-right: 0; }
.step-number { width: 26px; height: 26px; display: grid; place-items: center; color: #fff; background: var(--sb-accent); border-radius: 50%; font-size: 12px; font-weight: 700; }
.step-copy strong, .step-copy span { display: block; }
.step-copy span { margin-top: 4px; color: var(--sb-muted); font-size: 12px; }
.flow-action { color: var(--sb-muted); font-size: 12px; }
.option-meta { float: right; margin-left: 16px; color: var(--sb-muted); font-size: 12px; }
.section-heading { margin-bottom: 12px; }
@media (max-width: 980px) {
  .flow-grid { grid-template-columns: 1fr; }
  .flow-step, .flow-step:first-child, .flow-step:last-child { padding: 18px 0; border-right: 0; border-bottom: 1px solid var(--sb-border); }
  .flow-step:last-child { border-bottom: 0; }
}
@media (max-width: 620px) { .flow-action { align-items: stretch; flex-direction: column; } }
</style>
