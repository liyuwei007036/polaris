<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, ArrowUp, CopyDocument, Delete, Edit, Plus, Refresh, RefreshRight, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const changingState = ref('')
const dialogVisible = ref(false)
const editing = ref(null)
const configs = ref([])
const proxyGroups = ref([])
const listeners = ref([])
const endpoints = ref([])
const tab = ref('configs')
const accessLogs = ref([])
const accessLoading = ref(false)
const keyword = ref('')
const selectedStatus = ref('')
const accessQuery = reactive({ page: 1, pageSize: 10, total: 0, config_id: '', ip: '', location: '', user_agent: '' })
const form = reactive({ name: '', proxy_group_ids: [], rule_providers: [], rule_mode: 'table', rules: [], raw_rules: '' })
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }
const supportedProtocols = new Set(['vless', 'hysteria2'])
const ruleTypes = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD', 'DOMAIN-WILDCARD', 'DOMAIN-REGEX', 'GEOSITE',
  'IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP', 'SRC-GEOIP', 'SRC-IP-ASN',
  'SRC-IP-CIDR', 'SRC-IP-SUFFIX', 'DST-PORT', 'SRC-PORT', 'IN-PORT', 'IN-TYPE',
  'IN-USER', 'IN-NAME', 'REMATCH-NAME', 'PROCESS-NAME', 'PROCESS-NAME-WILDCARD',
  'PROCESS-NAME-REGEX', 'PROCESS-PATH', 'PROCESS-PATH-WILDCARD', 'PROCESS-PATH-REGEX',
  'UID', 'NETWORK', 'DSCP', 'AND', 'OR', 'NOT', 'RULE-SET', 'MATCH',
]
const noResolveTypes = new Set(['IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP', 'RULE-SET'])
const groupByID = computed(() => Object.fromEntries(proxyGroups.value.map((group) => [group.id, group])))
const filteredConfigs = computed(() => configs.value.filter((config) => {
  if (selectedStatus.value && String(config.enabled) !== selectedStatus.value) return false
  return includesText([config.name, config.rule_mode, ...(config.proxy_group_ids || []).map((id) => groupByID.value[id]?.name), ...(config.rule_providers || []).map((provider) => provider.name)], keyword.value)
}))
const selectedGroups = computed(() => resolveGroups(form.proxy_group_ids))
const selectedEndpointIDs = computed(() => new Set(selectedGroups.value.flatMap((group) => (group.members || []).filter((member) => member.kind === 'endpoint').map((member) => member.id))))
const selectedNodeNames = computed(() => endpoints.value
  .filter((endpoint) => endpoint.enabled && selectedEndpointIDs.value.has(endpoint.id))
  .map((endpoint) => {
    const listener = listeners.value.find((item) => item.id === endpoint.listener_id)
    const node = appState.nodes.find((item) => item.id === listener?.node_id)
    if (!listener?.enabled || !supportedProtocols.has(listener.spec?.protocol) || !(listener.connection_domain || node?.client_address)) return ''
    return endpoint.alias || `${node.name || listener.node_id} · ${listener.name} · ${endpoint.name}`
  })
  .filter(Boolean))
const ruleActions = computed(() => [...selectedNodeNames.value, ...selectedGroups.value.map((group) => group.name), 'DIRECT', 'REJECT'])
const providerProxies = computed(() => [...selectedNodeNames.value, ...selectedGroups.value.map((group) => group.name), 'DIRECT'])
const providerNames = computed(() => new Set(form.rule_providers.map((provider) => provider.name.trim()).filter(Boolean)))
const providerNameKeys = computed(() => new Set([...providerNames.value].map((name) => name.toLocaleUpperCase())))
const formValid = computed(() => {
  if (!form.name.trim() || !form.proxy_group_ids.length) return false
  if (!form.rule_providers.every((provider) => provider.name.trim() && provider.behavior && provider.format && provider.url.trim() && provider.path.trim() && provider.interval > 0 && provider.proxy)) return false
  if (providerNameKeys.value.size !== form.rule_providers.length) return false
  if (form.rule_mode === 'text') return Boolean(form.raw_rules.trim())
  return Boolean(form.rules.length && form.rules.at(-1)?.type === 'MATCH' && form.rules.every((rule) => rule.action && (rule.type === 'MATCH' || (rule.value.trim() && (rule.type !== 'RULE-SET' || providerNames.value.has(rule.value))))))
})

function resolveGroups(ids) {
  const result = []
  const seen = new Set()
  function visit(id) {
    if (seen.has(id)) return
    seen.add(id)
    const group = groupByID.value[id]
    if (!group) return
    for (const member of group.members || []) if (member.kind === 'group') visit(member.id)
    result.push(group)
  }
  for (const id of ids || []) visit(id)
  return result
}

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [configResult, groupResult, listenerResult] = await Promise.all([api('/mihomo/client-configs'), api('/mihomo/proxy-groups'), api('/listeners')])
    configs.value = configResult.client_configs || []
    proxyGroups.value = groupResult.proxy_groups || []
    listeners.value = listenerResult.listeners || []
    const endpointResults = await Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = endpointResults.flatMap((result) => result.endpoints || [])
  } finally {
    loading.value = false
  }
}

async function loadAccess() {
  accessLoading.value = true
  try {
    const query = new URLSearchParams({ page: accessQuery.page, page_size: accessQuery.pageSize })
    for (const key of ['config_id', 'ip', 'location', 'user_agent']) if (accessQuery[key]) query.set(key, accessQuery[key])
    const result = await api(`/mihomo/subscription-access?${query}`)
    accessLogs.value = result.access_logs || []
    accessQuery.total = result.total || 0
  } finally {
    accessLoading.value = false
  }
}

function searchAccess() {
  accessQuery.page = 1
  loadAccess()
}

function resetForm(config = null) {
  editing.value = config
  Object.assign(form, config ? {
    name: config.name,
    proxy_group_ids: [...(config.proxy_group_ids || [])],
    rule_providers: (config.rule_providers || []).map((provider) => ({ ...provider })),
    rule_mode: config.rule_mode || 'table',
    rules: (config.rules || []).map((rule) => ({
      type: rule.type,
      value: rule.value || '',
      action: rule.action,
      no_resolve: Boolean(rule.no_resolve),
    })),
    raw_rules: config.raw_rules || '',
  } : { name: '', proxy_group_ids: [], rule_providers: [], rule_mode: 'table', rules: [], raw_rules: '' })
  dialogVisible.value = true
}

function addRule() {
  form.rules.push({ type: 'DOMAIN-SUFFIX', value: '', action: '', no_resolve: false })
}

function addRuleProvider() {
  form.rule_providers.push({ name: '', behavior: 'domain', format: 'mrs', url: '', path: '', interval: 86400, proxy: 'DIRECT' })
}

function setProviderBehavior(provider, behavior) {
  provider.behavior = behavior
  if (behavior === 'classical' && provider.format === 'mrs') provider.format = 'yaml'
}

function setRuleType(rule, type) {
  rule.type = type
  if (type === 'MATCH') rule.value = ''
  if (type === 'RULE-SET' && !providerNames.value.has(rule.value)) rule.value = form.rule_providers[0]?.name || ''
  if (!noResolveTypes.has(type)) rule.no_resolve = false
}

function moveRule(index, offset) {
  const target = index + offset
  if (target < 0 || target >= form.rules.length) return
  const [rule] = form.rules.splice(index, 1)
  form.rules.splice(target, 0, rule)
}

async function save() {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      proxy_group_ids: [...form.proxy_group_ids],
      rule_providers: form.rule_providers.map((provider) => ({
        name: provider.name.trim(),
        behavior: provider.behavior,
        format: provider.format,
        url: provider.url.trim(),
        path: provider.path.trim(),
        interval: Number(provider.interval),
        proxy: provider.proxy,
      })),
      rule_mode: form.rule_mode,
      rules: form.rule_mode === 'table' ? form.rules.map((rule) => ({
        type: rule.type,
        value: rule.type === 'MATCH' ? '' : rule.value.trim(),
        action: rule.action,
        no_resolve: Boolean(rule.no_resolve),
      })) : [],
      raw_rules: form.rule_mode === 'text' ? form.raw_rules : '',
    }
    if (editing.value) await put(`/mihomo/client-configs/${editing.value.id}`, payload)
    else await post('/mihomo/client-configs', payload)
    ElMessage.success(editing.value ? '客户端配置已保存' : '客户端配置已创建')
    dialogVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

function absoluteSubscription(config) {
  return new URL(config.subscription_path, window.location.origin).toString()
}

async function writeClipboard(text) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.readOnly = true
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.select()
  const copied = document.execCommand('copy')
  textarea.remove()
  if (!copied) throw new Error('浏览器不支持自动复制')
}

async function setEnabled(config, enabled) {
  changingState.value = config.id
  try {
    await post(`/mihomo/client-configs/${config.id}/enabled`, { enabled })
    ElMessage.success(enabled ? '客户端配置已启用' : '客户端配置已停用')
    await load()
  } finally {
    changingState.value = ''
  }
}

async function copySubscription(config) {
  try {
    await writeClipboard(absoluteSubscription(config))
    ElMessage.success('更新地址已复制')
  } catch {
    ElMessage.error('自动复制失败，请使用 HTTPS 访问后重试')
  }
}

async function rotateSubscription(config) {
  await ElMessageBox.confirm('更换后，旧更新地址会立即失效。', '更换更新地址', { type: 'warning' })
  await post(`/mihomo/client-configs/${config.id}/subscription/rotate`, {})
  ElMessage.success('更新地址已更换')
  await load()
}

async function remove(config) {
  await ElMessageBox.confirm(`确认删除客户端配置“${config.name}”？`, '删除客户端配置', { type: 'warning' })
  await del(`/mihomo/client-configs/${config.id}`)
  ElMessage.success('客户端配置已删除')
  await load()
}

onMounted(() => { load(); loadAccess() })
</script>

<template>
  <div class="page-shell">
    <PageHeader title="客户端配置">
      <el-button :icon="Refresh" @click="load(); loadAccess()">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="resetForm()">新建</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="客户端配置" name="configs">
            <div class="tab-actions tab-actions--start">
              <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索配置或分组" style="width: 260px" />
              <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select>
            </div>
        <PagedTable :rows="filteredConfigs" empty-text="还没有客户端配置">
          <el-table-column label="配置名称" min-width="180" prop="name" />
          <el-table-column label="引用代理分组" min-width="300">
            <template #default="{ row }">
              <el-tag v-for="id in row.proxy_group_ids" :key="id" type="info" class="group-tag">
                {{ groupByID[id]?.name || '分组已失效' }}<template v-if="groupByID[id]"> · {{ strategyNames[groupByID[id].strategy] }}</template>
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="分流规则" min-width="170">
            <template #default="{ row }">{{ row.rule_mode === 'text' ? '高级文本' : '表格配置' }} · {{ row.rules?.length || 0 }} 条 · {{ row.rule_providers?.length || 0 }} 个供应商</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-switch :model-value="row.enabled" inline-prompt active-text="启用" inactive-text="停用" :loading="changingState === row.id" :disabled="changingState === row.id || !canWrite" @change="setEnabled(row, $event)" />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="230" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button link :icon="CopyDocument" @click="copySubscription(row)">复制</el-button>
              <el-button v-if="canWrite" link :icon="RefreshRight" @click="rotateSubscription(row)">更换</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="resetForm(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
          </el-tab-pane>
          <el-tab-pane label="访问记录" name="access">
            <div class="tab-actions tab-actions--start access-filters">
              <el-select v-model="accessQuery.config_id" clearable placeholder="全部配置" style="width: 180px"><el-option v-for="config in configs" :key="config.id" :label="config.name" :value="config.id" /></el-select>
              <el-input v-model="accessQuery.ip" clearable placeholder="IP" style="width: 170px" @keyup.enter="searchAccess" />
              <el-input v-model="accessQuery.location" clearable placeholder="归属地" style="width: 170px" @keyup.enter="searchAccess" />
              <el-input v-model="accessQuery.user_agent" clearable placeholder="User-Agent" style="width: 220px" @keyup.enter="searchAccess" />
              <el-button :icon="Search" @click="searchAccess">查询</el-button>
            </div>
            <el-table v-loading="accessLoading" :data="accessLogs">
              <el-table-column label="客户端配置" prop="config_name" min-width="170" />
              <el-table-column label="IP" prop="ip" min-width="170"><template #default="{ row }"><span class="mono">{{ row.ip || '—' }}</span></template></el-table-column>
              <el-table-column label="IP 归属地" prop="location" min-width="190" />
              <el-table-column label="User-Agent" prop="user_agent" min-width="320" show-overflow-tooltip />
              <el-table-column label="访问时间" width="180"><template #default="{ row }">{{ formatDateTime(row.accessed_at) }}</template></el-table-column>
            </el-table>
            <div class="pagination-bar"><el-pagination v-model:current-page="accessQuery.page" v-model:page-size="accessQuery.pageSize" :total="accessQuery.total" :page-sizes="[10, 20, 50, 100]" background layout="total, sizes, prev, pager, next" @change="loadAccess" /></div>
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑客户端配置' : '新建客户端配置'" width="min(980px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="配置名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="引用代理分组" required>
          <el-select v-model="form.proxy_group_ids" multiple filterable style="width: 100%" placeholder="选择一个或多个已有代理分组">
            <el-option v-for="group in proxyGroups" :key="group.id" :label="`${group.name} · ${strategyNames[group.strategy]}`" :value="group.id" />
          </el-select>
        </el-form-item>

        <section class="provider-section">
          <div class="section-heading">
            <div><h3>规则供应商</h3></div>
            <el-button :icon="Plus" @click="addRuleProvider">添加供应商</el-button>
          </div>
          <div v-for="(provider, index) in form.rule_providers" :key="index" class="provider-row">
            <el-form-item label="名称" required><el-input v-model="provider.name" aria-label="供应商名称" maxlength="128" /></el-form-item>
            <el-form-item label="行为" required>
              <el-select :model-value="provider.behavior" aria-label="供应商行为" @update:model-value="setProviderBehavior(provider, $event)">
                <el-option label="域名" value="domain" /><el-option label="IP 网段" value="ipcidr" /><el-option label="经典规则" value="classical" />
              </el-select>
            </el-form-item>
            <el-form-item label="格式" required>
              <el-select v-model="provider.format" aria-label="供应商格式">
                <el-option label="MRS" value="mrs" :disabled="provider.behavior === 'classical'" /><el-option label="YAML" value="yaml" /><el-option label="Text" value="text" />
              </el-select>
            </el-form-item>
            <el-form-item label="更新间隔（秒）" required><el-input-number v-model="provider.interval" aria-label="供应商更新间隔" :min="1" :max="31536000" controls-position="right" /></el-form-item>
            <el-form-item label="下载代理" required>
              <el-select v-model="provider.proxy" aria-label="供应商下载代理" filterable><el-option v-for="proxy in providerProxies" :key="proxy" :label="proxy" :value="proxy" /></el-select>
            </el-form-item>
            <el-form-item class="provider-url" label="规则地址" required><el-input v-model="provider.url" aria-label="供应商规则地址" placeholder="https://example.com/rules.mrs" /></el-form-item>
            <el-form-item class="provider-path" label="保存路径" required><el-input v-model="provider.path" aria-label="供应商保存路径" placeholder="./ruleset/rules.mrs" /></el-form-item>
            <div class="provider-remove"><el-tooltip content="删除供应商"><el-button :icon="Delete" text type="danger" aria-label="删除供应商" @click="form.rule_providers.splice(index, 1)" /></el-tooltip></div>
          </div>
        </section>

        <section class="rules-section">
          <div class="section-heading">
            <div><h3>访问规则</h3></div>
            <el-radio-group v-model="form.rule_mode" aria-label="规则配置模式">
              <el-radio-button value="table">表格配置</el-radio-button>
              <el-radio-button value="text">高级纯文本</el-radio-button>
            </el-radio-group>
          </div>
          <template v-if="form.rule_mode === 'table'">
            <div class="rule-table-wrap">
              <table class="rule-table">
                <thead><tr><th>类型</th><th>匹配值</th><th>动作</th><th>no-resolve</th><th>顺序</th></tr></thead>
                <tbody>
                  <tr v-for="(rule, index) in form.rules" :key="index">
                    <td data-label="类型"><el-select :model-value="rule.type" aria-label="规则类型" filterable @update:model-value="setRuleType(rule, $event)"><el-option v-for="type in ruleTypes" :key="type" :label="type" :value="type" /></el-select></td>
                    <td data-label="匹配值">
                      <el-select v-if="rule.type === 'RULE-SET'" v-model="rule.value" aria-label="规则供应商" filterable><el-option v-for="provider in form.rule_providers" :key="provider.name" :label="provider.name || '未命名供应商'" :value="provider.name" :disabled="!provider.name" /></el-select>
                      <el-input v-else v-model="rule.value" aria-label="规则匹配值" :disabled="rule.type === 'MATCH'" />
                    </td>
                    <td data-label="动作"><el-select v-model="rule.action" aria-label="规则动作" filterable><el-option v-for="action in ruleActions" :key="action" :label="action" :value="action" /></el-select></td>
                    <td class="check-cell" data-label="no-resolve"><el-checkbox v-model="rule.no_resolve" aria-label="no-resolve" :disabled="!noResolveTypes.has(rule.type)" /></td>
                    <td class="rule-actions" data-label="顺序">
                      <el-tooltip content="上移"><el-button :icon="ArrowUp" text aria-label="上移规则" :disabled="index === 0" @click="moveRule(index, -1)" /></el-tooltip>
                      <el-tooltip content="下移"><el-button :icon="ArrowDown" text aria-label="下移规则" :disabled="index === form.rules.length - 1" @click="moveRule(index, 1)" /></el-tooltip>
                      <el-tooltip content="删除"><el-button :icon="Delete" text type="danger" aria-label="删除规则" @click="form.rules.splice(index, 1)" /></el-tooltip>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <el-button :icon="Plus" class="add-rule" @click="addRule">添加</el-button>
          </template>
          <el-input v-else v-model="form.raw_rules" type="textarea" :rows="10" aria-label="高级规则文本" placeholder="RULE-SET,供应商名称,代理组&#10;DOMAIN-SUFFIX,example.com,代理组&#10;MATCH,DIRECT" />
        </section>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.group-tag { margin: 3px 6px 3px 0; }
.provider-section, .rules-section { padding-top: 20px; border-top: 1px solid var(--el-border-color-lighter); }
.provider-section { margin-top: 4px; }
.provider-row { display: grid; grid-template-columns: 1.4fr 1fr 0.9fr 1.25fr 1.2fr 40px; gap: 0 12px; padding: 14px 0 2px; border-top: 1px solid var(--el-border-color-lighter); }
.provider-row:first-of-type { border-top: 0; }
.provider-row :deep(.el-form-item) { margin-bottom: 12px; }
.provider-row :deep(.el-select), .provider-row :deep(.el-input-number) { width: 100%; }
.provider-url { grid-column: span 3; }
.provider-path { grid-column: span 2; }
.provider-remove { display: flex; align-items: center; justify-content: center; padding-top: 18px; }
.rules-section { margin-top: 8px; }
.section-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
.section-heading h3 { margin: 0 0 3px; font-size: 16px; }
.section-heading span { color: var(--sb-muted); font-size: 12px; }
.rule-table-wrap { max-width: 100%; overflow-x: hidden; }
.rule-table { width: 100%; border-collapse: collapse; table-layout: fixed; }
.rule-table th { padding: 0 8px 8px; color: var(--sb-muted); font-size: 12px; font-weight: 500; text-align: left; }
.rule-table th:nth-child(1) { width: 21%; }
.rule-table th:nth-child(2) { width: 27%; }
.rule-table th:nth-child(3) { width: 22%; }
.rule-table th:nth-child(4) { width: 12%; text-align: center; }
.rule-table th:nth-child(5) { width: 18%; }
.rule-table td { padding: 5px 8px; border-top: 1px solid var(--el-border-color-lighter); }
.check-cell { text-align: center; }
.rule-actions { white-space: nowrap; }
.add-rule { margin-top: 12px; }
@media (max-width: 640px) { .section-heading { align-items: flex-start; flex-direction: column; } }
@media (max-width: 720px) {
  .provider-row { grid-template-columns: 1fr 1fr; }
  .provider-url, .provider-path { grid-column: 1 / -1; }
  .provider-remove { grid-column: 2; justify-content: flex-end; padding-top: 0; }
  .rule-table, .rule-table tbody, .rule-table tr, .rule-table td { display: block; width: 100%; }
  .rule-table thead { display: none; }
  .rule-table tr { padding: 10px 0; border-top: 1px solid var(--el-border-color-lighter); }
  .rule-table td { display: grid; grid-template-columns: 92px minmax(0, 1fr); align-items: center; gap: 10px; padding: 5px 0; border: 0; }
  .rule-table td::before { content: attr(data-label); color: var(--sb-muted); font-size: 12px; }
  .rule-table .check-cell { text-align: left; }
  .rule-table .rule-actions { display: flex; justify-content: flex-end; }
  .rule-table .rule-actions::before { margin-right: auto; }
}
</style>
