<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, ArrowLeft, ArrowUp, CopyDocument, Delete, DocumentCopy, Download, Edit, Grid, Plus, Refresh, RefreshRight, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { writeClipboard } from '../clipboard'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'
import QrCodeDialog from '../components/QrCodeDialog.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const changingState = ref('')
// 配置项太多，塞进一个浮层要一路滚到底。改成整页编辑：列表和表单是这个
// 视图的两种形态，表单再按左侧导航分节，一次只面对一节。
const mode = ref('list')
const editing = ref(null)
const configs = ref([])
const proxyGroups = ref([])
const ruleProviders = ref([])
const listeners = ref([])
const endpoints = ref([])
const tab = ref('configs')
const accessLogs = ref([])
const accessLoading = ref(false)
const keyword = ref('')
const selectedStatus = ref('')
const accessQuery = reactive({ page: 1, pageSize: 10, total: 0, config_id: '', ip: '', location: '', user_agent: '' })
const qrOpen = ref(false)
const qrTitle = ref('')
const qrItems = ref([])
// A new subscription starts from the settings that used to be generated
// automatically, so it works out of the box and stays fully editable.
function defaultDNS() {
  return {
    enable: true,
    ipv6: false,
    enhanced_mode: 'fake-ip',
    fake_ip_range: '198.18.0.1/16',
    fake_ip_filter_mode: 'rule',
    fake_ip_filter: ['MATCH,fake-ip'],
    respect_rules: false,
    default_nameserver: ['https://223.5.5.5/dns-query'],
    nameserver: ['https://223.5.5.5/dns-query'],
    proxy_server_nameserver: ['https://223.5.5.5/dns-query'],
    direct_nameserver: ['https://223.5.5.5/dns-query'],
    direct_nameserver_follow_policy: false,
  }
}
function cloneDNS(dns) {
  const base = defaultDNS()
  if (!dns) return base
  return {
    enable: Boolean(dns.enable),
    ipv6: Boolean(dns.ipv6),
    enhanced_mode: dns.enhanced_mode || base.enhanced_mode,
    fake_ip_range: dns.fake_ip_range || base.fake_ip_range,
    fake_ip_filter_mode: dns.fake_ip_filter_mode || base.fake_ip_filter_mode,
    fake_ip_filter: [...(dns.fake_ip_filter || [])],
    respect_rules: Boolean(dns.respect_rules),
    default_nameserver: [...(dns.default_nameserver || [])],
    nameserver: [...(dns.nameserver || [])],
    proxy_server_nameserver: [...(dns.proxy_server_nameserver || [])],
    direct_nameserver: [...(dns.direct_nameserver || [])],
    direct_nameserver_follow_policy: Boolean(dns.direct_nameserver_follow_policy),
  }
}
const form = reactive({ name: '', proxy_group_ids: [], rule_provider_ids: [], rule_mode: 'table', rules: [], raw_rules: '', dns_mode: 'form', dns: defaultDNS(), raw_dns: '', access_secret: '', access_window_start: '', access_window_end: '', access_expires_at: '' })
function emptyForm() {
  return { name: '', proxy_group_ids: [], rule_provider_ids: [], rule_mode: 'table', rules: [], raw_rules: '', dns_mode: 'form', dns: defaultDNS(), raw_dns: '', access_secret: '', access_window_start: '', access_window_end: '', access_expires_at: '' }
}
const sections = [
  { key: 'basic', label: '基础信息', hint: '名称 · 代理分组 · 规则供应商' },
  { key: 'rules', label: '访问规则', hint: '决定流量走哪个分组' },
  { key: 'dns', label: 'DNS', hint: '下发给客户端的解析设置' },
  { key: 'access', label: '订阅限制', hint: '密钥 · 时间段 · 有效期' },
]
const activeSection = ref('basic')
const formBody = ref(null)
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }
const enhancedModes = [
  { value: 'fake-ip', label: 'fake-ip · 域名解析推迟到代理端' },
  { value: 'redir-host', label: 'redir-host · 本地解析真实 IP' },
  { value: 'normal', label: 'normal · 仅作普通 DNS' },
]
const fakeIPFilterModes = ['blacklist', 'whitelist', 'rule']
const nameserverFields = [
  { key: 'default_nameserver', label: '引导解析 default-nameserver', hint: '用于解析其它 DNS 服务器的域名，必须填 IP，或主机名已是 IP 的加密 DNS。' },
  { key: 'nameserver', label: '默认解析 nameserver', hint: '需要真实 IP 时使用，例如按目标 IP 匹配的规则。不能留空。' },
  { key: 'proxy_server_nameserver', label: '节点域名解析 proxy-server-nameserver', hint: '仅用于解析代理节点自身的域名。' },
  { key: 'direct_nameserver', label: '直连解析 direct-nameserver', hint: '直连出口的域名解析，填 system 表示交给系统解析器。' },
]
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
// 分组下挂多少个节点，直接标在选项上，不用挨个点开看。
const groupSummaries = computed(() => Object.fromEntries(proxyGroups.value.map(
  (group) => [group.id, { count: (group.members || []).length }],
)))
// 表格里成员多的分组标签会撑满整列，先显示前几个。
const groupPreviewCount = 4
// Providers are maintained under their own menu; a configuration only picks
// which of them it uses.
const selectedProviders = computed(() => form.rule_provider_ids
  .map((id) => ruleProviders.value.find((provider) => provider.id === id))
  .filter(Boolean))
const providerNames = computed(() => new Set(selectedProviders.value.map((provider) => provider.name)))
// 校验按分节拆开，导航上就能标出是哪一节还没填完，不用逐节翻找。
const basicValid = computed(() => Boolean(form.name.trim() && form.proxy_group_ids.length))
const rulesValid = computed(() => {
  if (form.rule_mode === 'text') return Boolean(form.raw_rules.trim())
  return Boolean(form.rules.length && form.rules.at(-1)?.type === 'MATCH' && form.rules.every((rule) => rule.action && (rule.type === 'MATCH' || (rule.value.trim() && (rule.type !== 'RULE-SET' || providerNames.value.has(rule.value))))))
})
// Mihomo refuses to start without these two lists once dns is enabled.
const dnsValid = computed(() => !(form.dns_mode === 'form' && form.dns.enable) || Boolean(form.dns.nameserver.length && form.dns.default_nameserver.length))
// 订阅限制整节都是可选项，留空即不限制，所以没有「待填」这一说。
const sectionValid = computed(() => ({ basic: basicValid.value, rules: rulesValid.value, dns: dnsValid.value, access: true }))
const formValid = computed(() => basicValid.value && rulesValid.value && dnsValid.value)

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
    const [configResult, groupResult, providerResult, listenerResult] = await Promise.all([
      api('/mihomo/client-configs'),
      api('/mihomo/proxy-groups'),
      api('/mihomo/rule-providers').catch(() => ({ rule_providers: [] })),
      api('/listeners'),
    ])
    configs.value = configResult.client_configs || []
    proxyGroups.value = groupResult.proxy_groups || []
    ruleProviders.value = providerResult.rule_providers || []
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

function openForm(config = null) {
  editing.value = config
  activeSection.value = 'basic'
  Object.assign(form, config ? {
    name: config.name,
    proxy_group_ids: [...(config.proxy_group_ids || [])],
    rule_provider_ids: [...(config.rule_provider_ids || [])],
    rule_mode: config.rule_mode || 'table',
    rules: (config.rules || []).map((rule) => ({
      type: rule.type,
      value: rule.value || '',
      action: rule.action,
      no_resolve: Boolean(rule.no_resolve),
    })),
    raw_rules: config.raw_rules || '',
    dns_mode: config.dns_mode || 'form',
    dns: cloneDNS(config.dns),
    raw_dns: config.raw_dns || '',
    access_secret: config.access_secret || '',
    access_window_start: config.access_window_start || '',
    access_window_end: config.access_window_end || '',
    access_expires_at: config.access_expires_at || '',
  } : emptyForm())
  mode.value = 'form'
}

// 每节独立滚动，切节时回到顶部，否则从长长的规则表底部切到 DNS 会停在半空。
function selectSection(key) {
  activeSection.value = key
  if (formBody.value) formBody.value.scrollTop = 0
}

// Switching to the text editor starts from what the form currently says, so
// the advanced mode is an extension of the form rather than a blank page.
function setDNSMode(mode) {
  if (mode === 'text' && !form.raw_dns.trim()) form.raw_dns = renderDNSYAML(form.dns)
  form.dns_mode = mode
}

function renderDNSYAML(dns) {
  const list = (key, values) => (values.length ? [`${key}:`, ...values.map((value) => `  - ${value}`)] : [])
  const lines = [`enable: ${dns.enable}`, `ipv6: ${dns.ipv6}`, `enhanced-mode: ${dns.enhanced_mode}`]
  if (dns.enhanced_mode === 'fake-ip') {
    lines.push(`fake-ip-range: ${dns.fake_ip_range}`, `fake-ip-filter-mode: ${dns.fake_ip_filter_mode}`, ...list('fake-ip-filter', dns.fake_ip_filter))
  }
  lines.push(`respect-rules: ${dns.respect_rules}`)
  for (const field of nameserverFields) lines.push(...list(field.key.replace(/_/g, '-'), dns[field.key]))
  lines.push(`direct-nameserver-follow-policy: ${dns.direct_nameserver_follow_policy}`)
  return lines.join('\n')
}

function addRule() {
  form.rules.push({ type: 'DOMAIN-SUFFIX', value: '', action: '', no_resolve: false })
}

function setRuleType(rule, type) {
  rule.type = type
  if (type === 'MATCH') rule.value = ''
  if (type === 'RULE-SET' && !providerNames.value.has(rule.value)) rule.value = selectedProviders.value[0]?.name || ''
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
      rule_provider_ids: [...form.rule_provider_ids],
      rule_mode: form.rule_mode,
      rules: form.rule_mode === 'table' ? form.rules.map((rule) => ({
        type: rule.type,
        value: rule.type === 'MATCH' ? '' : rule.value.trim(),
        action: rule.action,
        no_resolve: Boolean(rule.no_resolve),
      })) : [],
      raw_rules: form.rule_mode === 'text' ? form.raw_rules : '',
      dns_mode: form.dns_mode,
      dns: form.dns,
      raw_dns: form.raw_dns,
      access_secret: form.access_secret.trim(),
      access_window_start: form.access_window_start || '',
      access_window_end: form.access_window_end || '',
      access_expires_at: form.access_expires_at || '',
    }
    if (editing.value) await put(`/mihomo/client-configs/${editing.value.id}`, payload)
    else await post('/mihomo/client-configs', payload)
    ElMessage.success(editing.value ? '客户端配置已保存' : '客户端配置已创建')
    mode.value = 'list'
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '客户端配置保存失败')
  } finally {
    saving.value = false
  }
}

// Copying produces an independent configuration with its own update address:
// the copy is meant to be edited, not to mirror the original.
async function copyConfig(config) {
  const result = await ElMessageBox.prompt('新配置的名称', `复制“${config.name}”`, {
    inputValue: config.name,
    inputPattern: /\S/,
    inputErrorMessage: '请填写配置名称',
    confirmButtonText: '复制',
  }).catch(() => null)
  if (!result) return
  try {
    await post(`/mihomo/client-configs/${config.id}/copy`, { name: result.value.trim() })
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '客户端配置复制失败')
    return
  }
  ElMessage.success('客户端配置已复制')
  await load()
}

function absoluteSubscription(config) {
  return new URL(config.subscription_path, window.location.origin).toString()
}

function expired(config) {
  return Boolean(config.access_expires_at) && new Date(config.access_expires_at).getTime() <= Date.now()
}

function showQrCode(config) {
  qrTitle.value = `${config.name} · 更新地址`
  qrItems.value = [{ key: config.id, label: config.name, value: absoluteSubscription(config) }]
  qrOpen.value = true
}

// 密钥要能从 User-Agent 里原样取回，所以只用服务端认得的字母表。24 位在这个
// 字母表下约合 142 位熵，远超挡住误转发所需要的强度。
function generateAccessSecret() {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  const bytes = new Uint8Array(24)
  crypto.getRandomValues(bytes)
  form.access_secret = Array.from(bytes, (byte) => alphabet[byte % alphabet.length]).join('')
}

async function copyAccessUserAgent() {
  try {
    await writeClipboard(`polaris/${form.access_secret.trim()}`)
    ElMessage.success('User-Agent 已复制')
  } catch {
    ElMessage.error('自动复制失败，请使用 HTTPS 访问后重试')
  }
}

// Clash 系客户端都认这个 scheme，点一下就把订阅装进本机客户端。
function clashImportURL(config) {
  return `clash://install-config?url=${encodeURIComponent(absoluteSubscription(config))}&name=${encodeURIComponent(config.name)}`
}

async function importToClash(config) {
  // 一键导入带不上 User-Agent，设了密钥就必须让人知道还得手工补一步，
  // 否则客户端装上了却每次更新都失败，比直接失败更难查。
  if (config.access_user_agent) {
    const confirmed = await ElMessageBox.confirm(
      `这份配置设了访问密钥，一键导入带不上它。导入后请在客户端把 User-Agent 改成 ${config.access_user_agent}，否则更新会失败。`,
      '导入到 Clash',
      { confirmButtonText: '仍然导入', type: 'warning' },
    ).catch(() => null)
    if (!confirmed) return
  }
  window.location.href = clashImportURL(config)
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
    <PageHeader v-if="mode === 'list'" title="客户端配置">
      <el-button :icon="Refresh" @click="load(); loadAccess()">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openForm()">新建</el-button>
    </PageHeader>
    <main v-if="mode === 'list'" v-loading="loading" class="page-content">
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="客户端配置" name="configs">
            <div class="tab-actions tab-actions--start">
              <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索配置或分组" style="width: 260px" />
              <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select>
            </div>
        <PagedTable :rows="filteredConfigs" empty-text="还没有客户端配置">
          <el-table-column label="配置名称" min-width="165">
            <template #default="{ row }">
              {{ row.name }}
              <el-tag v-if="row.access_user_agent" size="small" type="success" effect="plain">密钥</el-tag>
              <el-tag v-if="row.access_window_start" size="small" type="warning" effect="plain">{{ row.access_window_start }}-{{ row.access_window_end }}</el-tag>
              <el-tag v-if="row.access_expires_at" size="small" :type="expired(row) ? 'danger' : 'info'" effect="plain">
                {{ expired(row) ? '已过期' : `${formatDateTime(row.access_expires_at)} 到期` }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="引用代理分组" min-width="195">
            <template #default="{ row }">
              <el-tag v-for="id in (row.proxy_group_ids || []).slice(0, groupPreviewCount)" :key="id" type="info" class="group-tag">
                {{ groupByID[id]?.name || '分组已失效' }}
              </el-tag>
              <el-tooltip v-if="(row.proxy_group_ids || []).length > groupPreviewCount" placement="top">
                <template #content>
                  <div v-for="id in row.proxy_group_ids.slice(groupPreviewCount)" :key="id">{{ groupByID[id]?.name || '分组已失效' }}</div>
                </template>
                <el-tag class="group-tag" type="info" effect="plain">+{{ row.proxy_group_ids.length - groupPreviewCount }}</el-tag>
              </el-tooltip>
            </template>
          </el-table-column>
          <el-table-column label="分流规则" min-width="150" show-overflow-tooltip>
            <template #default="{ row }">{{ row.rule_mode === 'text' ? '高级文本' : '表格配置' }} · {{ row.rules?.length || 0 }} 条 · {{ row.rule_providers?.length || 0 }} 个供应商</template>
          </el-table-column>
          <el-table-column label="状态" width="82">
            <template #default="{ row }">
              <el-switch :model-value="row.enabled" inline-prompt active-text="启用" inactive-text="停用" :loading="changingState === row.id" :disabled="changingState === row.id || !canWrite" @change="setEnabled(row, $event)" />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="538" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button link :icon="CopyDocument" @click="copySubscription(row)">复制</el-button>
              <el-button link :icon="Grid" @click="showQrCode(row)">二维码</el-button>
              <el-button link :icon="Download" @click="importToClash(row)">导入 Clash</el-button>
              <el-button v-if="canWrite" link :icon="DocumentCopy" @click="copyConfig(row)">复制配置</el-button>
              <el-button v-if="canWrite" link :icon="RefreshRight" @click="rotateSubscription(row)">更换</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="openForm(row)">编辑</el-button>
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

    <PageHeader v-if="mode === 'form'" :title="editing ? '编辑客户端配置' : '新建客户端配置'">
      <el-button :icon="ArrowLeft" @click="mode = 'list'">返回</el-button>
      <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
    </PageHeader>
    <main v-if="mode === 'form'" class="page-content form-page">
      <nav class="form-nav" aria-label="配置分节">
        <button
          v-for="section in sections"
          :key="section.key"
          type="button"
          class="form-nav__item"
          :class="{ active: activeSection === section.key }"
          :aria-current="activeSection === section.key || undefined"
          @click="selectSection(section.key)"
        >
          <span class="form-nav__label">
            {{ section.label }}
            <el-tag v-if="!sectionValid[section.key]" size="small" type="warning" effect="plain" disable-transitions>待填</el-tag>
          </span>
          <span class="form-nav__hint">{{ section.hint }}</span>
        </button>
      </nav>
      <div ref="formBody" class="form-body">
        <el-form label-position="top" class="form-body__inner">
          <template v-if="activeSection === 'basic'">
            <el-form-item label="配置名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
            <el-form-item label="引用代理分组" required>
              <el-select
                v-model="form.proxy_group_ids"
                multiple
                filterable
                collapse-tags
                collapse-tags-tooltip
                :max-collapse-tags="6"
                style="width: 100%"
                placeholder="选择代理分组"
              >
                <!-- 图标与成员数量对读屏无意义，隐藏起来，选项名保持为分组名。 -->
                <el-option v-for="group in proxyGroups" :key="group.id" :label="`${group.name} · ${strategyNames[group.strategy]}`" :value="group.id">
                  <span>{{ group.name }} · {{ strategyNames[group.strategy] }}</span>
                  <span class="option-meta" aria-hidden="true">
                    {{ groupSummaries[group.id]?.count || 0 }} 个成员
                  </span>
                </el-option>
              </el-select>
            </el-form-item>

            <el-form-item label="规则供应商">
              <el-select v-model="form.rule_provider_ids" multiple filterable collapse-tags collapse-tags-tooltip :max-collapse-tags="6" style="width: 100%" aria-label="引用规则供应商" placeholder="选择规则供应商">
                <el-option v-for="provider in ruleProviders" :key="provider.id" :label="`${provider.name} · ${provider.url}`" :value="provider.id" />
              </el-select>
              <div class="subtle" style="margin-top: 6px">选择后可在「访问规则」的 RULE-SET 规则中引用。</div>
            </el-form-item>
          </template>

          <section v-else-if="activeSection === 'rules'">
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
                        <el-select v-if="rule.type === 'RULE-SET'" v-model="rule.value" aria-label="规则供应商" filterable><el-option v-for="provider in selectedProviders" :key="provider.id" :label="provider.name" :value="provider.name" /></el-select>
                        <el-input v-else v-model="rule.value" aria-label="规则匹配值" :disabled="rule.type === 'MATCH'" />
                      </td>
                      <td data-label="动作">
                        <el-select v-model="rule.action" aria-label="规则动作" filterable>
                          <el-option v-for="action in ruleActions" :key="action" :label="action" :value="action">
                            {{ action }}
                          </el-option>
                        </el-select>
                      </td>
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

          <section v-else-if="activeSection === 'access'">
            <div class="section-heading">
              <div><h3>订阅限制</h3></div>
            </div>
            <div class="subtle" style="margin-bottom: 16px">
              三项都可以留空，留空即不限制。它们管的是「拿到更新地址之后还能不能拉到配置」；
              已经下载过的配置照样能连节点，要断掉某个人请去停用他名下的端点。
            </div>

            <el-form-item label="访问密钥">
              <div class="access-row">
                <el-input v-model="form.access_secret" maxlength="64" aria-label="访问密钥" placeholder="留空表示不校验" />
                <el-button :icon="RefreshRight" @click="generateAccessSecret">随机生成</el-button>
                <el-button :icon="CopyDocument" :disabled="!form.access_secret.trim()" @click="copyAccessUserAgent">复制 UA</el-button>
              </div>
              <div class="subtle" style="margin-top: 6px">
                填写后，客户端订阅设置里的 User-Agent 必须包含
                <code>polaris/{{ form.access_secret.trim() || '密钥' }}</code>
                才能拉到配置。只能用字母、数字、下划线和短横线，长度 8-64 位。转达时请和更新地址分开发送。
              </div>
            </el-form-item>

            <el-form-item label="允许访问的时间段">
              <div class="access-row">
                <el-time-select v-model="form.access_window_start" start="00:00" step="00:30" end="23:30" placeholder="开始时间" aria-label="时间段开始" style="width: 150px" />
                <span class="subtle">至</span>
                <el-time-select v-model="form.access_window_end" start="00:00" step="00:30" end="23:30" placeholder="结束时间" aria-label="时间段结束" style="width: 150px" />
                <el-button link @click="form.access_window_start = ''; form.access_window_end = ''">清除</el-button>
              </div>
              <div class="subtle" style="margin-top: 6px">
                按控制台所在服务器的本地时间判断。两端都留空表示任意时间都能拉；
                开始晚于结束表示跨过午夜，例如 22:00 至 06:00 指的是整个夜间。
              </div>
            </el-form-item>

            <el-form-item label="有效期">
              <el-date-picker
                v-model="form.access_expires_at"
                type="datetime"
                value-format="YYYY-MM-DDTHH:mm:ssZ"
                aria-label="有效期"
                placeholder="留空表示长期有效"
                style="width: 260px"
              />
              <div class="subtle" style="margin-top: 6px">到期后这个更新地址不再返回配置。需要续期就回来往后调，或者清空。</div>
            </el-form-item>
          </section>

          <section v-else>
            <div class="section-heading">
              <div><h3>DNS</h3></div>
              <el-radio-group :model-value="form.dns_mode" aria-label="DNS 配置模式" @update:model-value="setDNSMode">
                <el-radio-button value="form">表单配置</el-radio-button>
                <el-radio-button value="text">高级纯文本</el-radio-button>
              </el-radio-group>
            </div>
            <template v-if="form.dns_mode === 'form'">
              <el-form-item label="下发 DNS 配置">
                <el-switch v-model="form.dns.enable" aria-label="下发 DNS 配置" />
                <div class="subtle" style="margin-left: 12px">关闭后订阅不含 dns 段，由客户端自己的 DNS 设置决定。</div>
              </el-form-item>
              <template v-if="form.dns.enable">
                <el-form-item label="解析模式">
                  <el-select v-model="form.dns.enhanced_mode" aria-label="DNS 解析模式" style="width: 100%">
                    <el-option v-for="mode in enhancedModes" :key="mode.value" :label="mode.label" :value="mode.value" />
                  </el-select>
                </el-form-item>
                <template v-if="form.dns.enhanced_mode === 'fake-ip'">
                  <el-form-item label="Fake-IP 网段">
                    <el-input v-model="form.dns.fake_ip_range" aria-label="Fake-IP 网段" placeholder="198.18.0.1/16" />
                  </el-form-item>
                  <el-form-item label="Fake-IP 过滤模式">
                    <el-radio-group v-model="form.dns.fake_ip_filter_mode" aria-label="Fake-IP 过滤模式">
                      <el-radio-button v-for="mode in fakeIPFilterModes" :key="mode" :value="mode">{{ mode }}</el-radio-button>
                    </el-radio-group>
                  </el-form-item>
                  <el-form-item label="Fake-IP 过滤">
                    <el-select v-model="form.dns.fake_ip_filter" multiple filterable allow-create default-first-option :reserve-keyword="false" aria-label="Fake-IP 过滤" style="width: 100%" placeholder="rule 模式写规则，如 MATCH,fake-ip；其它模式写域名，如 *.lan" />
                    <div class="subtle">rule 模式下每条是一条规则，动作只能是 fake-ip 或 real-ip，自上而下匹配。</div>
                  </el-form-item>
                </template>
                <el-form-item v-for="field in nameserverFields" :key="field.key" :label="field.label">
                  <el-select v-model="form.dns[field.key]" multiple filterable allow-create default-first-option :reserve-keyword="false" :aria-label="field.label" style="width: 100%" placeholder="回车添加，如 https://223.5.5.5/dns-query 或 system" />
                  <div class="subtle">{{ field.hint }}</div>
                </el-form-item>
                <el-form-item label="其它开关">
                  <el-checkbox v-model="form.dns.ipv6">解析 IPv6（ipv6）</el-checkbox>
                  <el-checkbox v-model="form.dns.respect_rules">DNS 连接遵守路由规则（respect-rules）</el-checkbox>
                  <el-checkbox v-model="form.dns.direct_nameserver_follow_policy">直连解析遵守 nameserver-policy</el-checkbox>
                </el-form-item>
              </template>
            </template>
            <template v-else>
              <el-input v-model="form.raw_dns" type="textarea" :rows="12" aria-label="高级 DNS 文本" placeholder="enable: true&#10;enhanced-mode: fake-ip&#10;nameserver:&#10;  - https://223.5.5.5/dns-query" />
              <div class="subtle" style="margin-top: 6px">按 Mihomo 的 dns 段格式书写，内容会原样写入订阅；留空则订阅不含 dns 段。</div>
            </template>
          </section>
        </el-form>
      </div>
    </main>

    <QrCodeDialog v-model="qrOpen" :title="qrTitle" :items="qrItems" />

  </div>
</template>

<style scoped>
.group-tag { margin: 3px 6px 3px 0; }
.access-row { display: flex; align-items: center; gap: 8px; width: 100%; }
.option-meta { float: right; margin-left: 18px; color: var(--sb-muted); font-size: 12px; }

/* 整页编辑：左侧导航固定，右侧只渲染当前一节并独立滚动，
   这样无论规则有多少条，导航和顶部的保存按钮都不会被推走。 */
.form-page { flex-direction: row; gap: 20px; align-items: stretch; overflow: hidden; }
.form-nav { flex: 0 0 200px; display: flex; flex-direction: column; gap: 6px; }
.form-nav__item {
  display: flex;
  flex-direction: column;
  gap: 3px;
  padding: 11px 14px;
  border: 1px solid transparent;
  border-radius: var(--sb-radius);
  background: transparent;
  color: var(--sb-muted);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: background .15s ease, color .15s ease, border-color .15s ease;
}
.form-nav__item:hover { background: var(--sb-panel-solid); color: var(--el-text-color-primary); }
.form-nav__item.active { border-color: var(--sb-line); background: var(--sb-panel-solid); color: #fff; }
.form-nav__label { display: flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 550; }
.form-nav__hint { font-size: 12px; line-height: 1.4; opacity: .75; }
.form-body { flex: 1 1 auto; min-width: 0; overflow: auto; }
.form-body__inner { max-width: 1120px; }

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
/* 窄屏放不下侧栏，分节导航改为顶部一行，提示文字让位。 */
@media (max-width: 900px) {
  .form-page { flex-direction: column; gap: 14px; overflow: auto; }
  .form-nav { flex: none; flex-direction: row; overflow-x: auto; }
  .form-nav__item { flex: 1 0 auto; padding: 9px 14px; }
  .form-nav__hint { display: none; }
  .form-body { overflow: visible; }
}
@media (max-width: 640px) { .section-heading { align-items: flex-start; flex-direction: column; } }
@media (max-width: 720px) {
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
