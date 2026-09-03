<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { writeClipboard } from '../../clipboard'
import { formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MValueList from '../components/MValueList.vue'
import MActionSheet from '../components/MActionSheet.vue'
import MQrSheet from '../components/MQrSheet.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loadNodes = inject('loadNodes')

const loading = ref(false)
const saving = ref(false)
// 列表和表单是这个页面的两种形态，手机上不叠弹窗，直接换整页。
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
const accessFilterOpen = ref(false)
const keyword = ref('')
const selectedStatus = ref('')
const accessQuery = reactive({ page: 1, pageSize: 20, total: 0, config_id: '', ip: '', location: '', user_agent: '' })
const qrOpen = ref(false)
const qrTitle = ref('')
const qrItems = ref([])
const actionsOpen = ref(false)
const actionTarget = ref(null)
const activeSection = ref('basic')
const ruleSheet = ref(false)
const ruleIndex = ref(-1)

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
  { value: 'basic', label: '基础信息' },
  { value: 'rules', label: '访问规则' },
  { value: 'dns', label: 'DNS' },
  { value: 'access', label: '订阅限制' },
]
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }
const enhancedModes = [
  { value: 'fake-ip', label: 'fake-ip', desc: '域名解析推迟到代理端' },
  { value: 'redir-host', label: 'redir-host', desc: '本地解析真实 IP' },
  { value: 'normal', label: 'normal', desc: '仅作普通 DNS' },
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
const ruleActionOptions = computed(() => [
  ...selectedNodeNames.value.map((name) => ({ value: name, label: name, group: '节点' })),
  ...selectedGroups.value.map((group) => ({ value: group.name, label: group.name, group: '代理分组' })),
  { value: 'DIRECT', label: 'DIRECT', desc: '直连', group: '内置' },
  { value: 'REJECT', label: 'REJECT', desc: '阻断', group: '内置' },
])
const groupOptions = computed(() => proxyGroups.value.map((group) => ({
  value: group.id,
  label: group.name,
  desc: `${strategyNames[group.strategy]} · ${(group.members || []).length} 个成员`,
})))
const providerOptions = computed(() => ruleProviders.value.map((provider) => ({ value: provider.id, label: provider.name, desc: provider.url })))
const selectedProviders = computed(() => form.rule_provider_ids
  .map((id) => ruleProviders.value.find((provider) => provider.id === id))
  .filter(Boolean))
const providerNames = computed(() => new Set(selectedProviders.value.map((provider) => provider.name)))
const providerValueOptions = computed(() => selectedProviders.value.map((provider) => ({ value: provider.name, label: provider.name })))

const basicValid = computed(() => Boolean(form.name.trim() && form.proxy_group_ids.length))
const rulesValid = computed(() => {
  if (form.rule_mode === 'text') return Boolean(form.raw_rules.trim())
  return Boolean(form.rules.length && form.rules.at(-1)?.type === 'MATCH' && form.rules.every((rule) => rule.action && (rule.type === 'MATCH' || (rule.value.trim() && (rule.type !== 'RULE-SET' || providerNames.value.has(rule.value))))))
})
// dns 打开后 Mihomo 缺了这两个列表就起不来。
const dnsValid = computed(() => !(form.dns_mode === 'form' && form.dns.enable) || Boolean(form.dns.nameserver.length && form.dns.default_nameserver.length))
// 订阅限制整节都是可选项，留空即不限制，所以没有「待填」这一说。
const sectionValid = computed(() => ({ basic: basicValid.value, rules: rulesValid.value, dns: dnsValid.value, access: true }))
const formValid = computed(() => basicValid.value && rulesValid.value && dnsValid.value)
const sectionOptions = computed(() => sections.map((section) => ({ ...section, badge: sectionValid.value[section.value] ? '' : '待填' })))
const editingRule = computed(() => form.rules[ruleIndex.value] || null)

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
  accessFilterOpen.value = false
  loadAccess()
}

function turnAccessPage(offset) {
  accessQuery.page += offset
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

// 切到文本模式时从表单当前内容起手，高级模式是表单的延续而不是空白页。
function setDNSMode(value) {
  if (value === 'text' && !form.raw_dns.trim()) form.raw_dns = renderDNSYAML(form.dns)
  form.dns_mode = value
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
  ruleIndex.value = form.rules.length - 1
  ruleSheet.value = true
}

function editRule(index) {
  ruleIndex.value = index
  ruleSheet.value = true
}

function setRuleType(type) {
  const rule = editingRule.value
  if (!rule) return
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

function removeRule(index) {
  form.rules.splice(index, 1)
  ruleSheet.value = false
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

function openActions(config) {
  actionTarget.value = config
  actionsOpen.value = true
}

function groupNames(config) {
  const names = (config.proxy_group_ids || []).map((id) => groupByID.value[id]?.name || '分组已失效')
  return names.length ? names.join(' · ') : '未引用分组，客户端会拿到空配置'
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '状态', value: row.enabled ? '启用' : '停用' },
    { label: '代理分组', value: groupNames(row) },
    { label: '规则供应商', value: (row.rule_providers || []).map((provider) => provider.name).join(' · ') || '未引用' },
    { label: '规则', value: `${row.rule_mode === 'text' ? '高级文本' : '表格配置'} · ${row.rules?.length || 0} 条` },
    { label: '更新地址', value: absoluteSubscription(row), mono: true },
    { label: '访问密钥', value: row.access_user_agent || '未设置', mono: Boolean(row.access_user_agent) },
    { label: '访问时间段', value: row.access_window_start ? `${row.access_window_start} - ${row.access_window_end}` : '不限' },
    { label: '有效期', value: row.access_expires_at ? formatDateTime(row.access_expires_at) : '长期有效' },
  ]
})

const actions = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  const list = [
    { key: 'copy-url', label: '复制更新地址' },
    { key: 'qr', label: '更新地址二维码' },
    { key: 'clash', label: '导入到 Clash', hint: '交给本机 Clash 客户端安装' },
  ]
  if (row.access_user_agent) list.push({ key: 'copy-ua', label: '复制访问密钥 User-Agent' })
  if (canWrite.value) {
    list.push({ key: 'edit', label: '编辑' })
    list.push({ key: 'duplicate', label: '复制配置', hint: '生成一份独立的新配置' })
    list.push({ key: 'rotate', label: '更换更新地址', hint: '旧地址立即失效' })
    list.push({ key: 'toggle', label: row.enabled ? '停用' : '启用' })
  }
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'copy-url') return copySubscription(row)
  if (key === 'qr') return showQrCode(row)
  if (key === 'edit') return openForm(row)
  if (key === 'duplicate') return copyConfig(row)
  if (key === 'rotate') return rotateSubscription(row)
  if (key === 'copy-ua') return copyAccessUserAgent(row)
  if (key === 'clash') return importToClash(row)
  if (key === 'toggle') return setEnabled(row, !row.enabled)
  if (key === 'delete') return remove(row)
}

// 复制出来的是一份独立配置，有自己的更新地址：复制是为了改，不是为了镜像。
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

function showQrCode(config) {
  qrTitle.value = `${config.name} · 更新地址`
  qrItems.value = [{ key: config.id, label: config.name, value: absoluteSubscription(config) }]
  qrOpen.value = true
}

async function setEnabled(config, enabled) {
  await post(`/mihomo/client-configs/${config.id}/enabled`, { enabled })
  ElMessage.success(enabled ? '客户端配置已启用' : '客户端配置已停用')
  await load()
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

// 传 config 时复制它已存的 UA，不传时复制表单里正在编辑的那个。
async function copyAccessUserAgent(config = null) {
  const value = config ? (config.access_user_agent || '') : `polaris/${form.access_secret.trim()}`
  try {
    await writeClipboard(value)
    ElMessage.success('User-Agent 已复制')
  } catch {
    ElMessage.error('自动复制失败，请使用 HTTPS 访问后重试')
  }
}

// 密钥要能从 User-Agent 里原样取回，所以只用服务端认得的字母表。
function generateAccessSecret() {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
  const bytes = new Uint8Array(24)
  crypto.getRandomValues(bytes)
  form.access_secret = Array.from(bytes, (byte) => alphabet[byte % alphabet.length]).join('')
}

// Clash 系客户端都认这个 scheme，点一下就把订阅装进本机客户端。
function clashImportURL(config) {
  return `clash://install-config?url=${encodeURIComponent(absoluteSubscription(config))}&name=${encodeURIComponent(config.name)}`
}

async function importToClash(config) {
  // 一键导入带不上 User-Agent，设了密钥就必须让人知道还得手工补一步。
  if (config.access_user_agent) {
    await ElMessageBox.confirm(
      `这份配置设了访问密钥，一键导入带不上它。导入后请在客户端把 User-Agent 改成 ${config.access_user_agent}，否则更新会失败。`,
      '导入到 Clash',
      { confirmButtonText: '仍然导入', type: 'warning' },
    )
  }
  window.location.href = clashImportURL(config)
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
  <MPage v-if="mode === 'list'" title="客户端配置" :loading="loading">
    <MSegmented v-model="tab" :options="[{ value: 'configs', label: '客户端配置' }, { value: 'access', label: '访问记录' }]" />

    <template v-if="tab === 'configs'">
      <div class="m-listbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索配置或分组" />
        <div class="m-filters">
          <MSegmented
            v-model="selectedStatus"
            :options="[{ value: '', label: '全部' }, { value: 'true', label: '启用' }, { value: 'false', label: '停用' }]"
          />
        </div>
      </div>

      <article v-for="row in filteredConfigs" :key="row.id" class="m-item" :class="{ 'is-off': !row.enabled }">
        <button type="button" class="m-item__hit" @click="openActions(row)">
          <div class="m-item__head">
            <span class="m-item__title">{{ row.name }}</span>
            <span v-if="!row.enabled" class="m-pill m-pill--info">停用</span>
            <i class="m-item__chevron" aria-hidden="true">›</i>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ (row.proxy_group_ids || []).length }}</b><small>代理分组</small></span>
            <span class="m-stat"><b>{{ row.rules?.length || 0 }}</b><small>访问规则</small></span>
            <span class="m-stat"><b>{{ row.rule_providers?.length || 0 }}</b><small>规则供应商</small></span>
          </div>
          <div class="m-item__meta">{{ groupNames(row) }}</div>
        </button>
      </article>
      <div v-if="!filteredConfigs.length && !loading" class="m-empty">还没有客户端配置</div>
    </template>

    <template v-else>
      <div class="access-bar">
        <span class="m-count">共 {{ accessQuery.total }} 条 · 第 {{ accessQuery.page }} 页</span>
        <el-button size="small" @click="accessFilterOpen = true">筛选</el-button>
      </div>
      <div v-loading="accessLoading">
        <article v-for="(row, index) in accessLogs" :key="`${row.ip}-${row.accessed_at}-${index}`" class="m-item">
          <div class="m-item__hit is-static">
            <div class="m-item__head">
              <span class="m-item__title m-item__title--mono">{{ row.ip || '—' }}</span>
              <span class="m-pill m-pill--info">{{ row.config_name }}</span>
            </div>
            <div class="m-item__meta">{{ row.location || '归属地未知' }} · {{ formatDateTime(row.accessed_at) }}</div>
            <div class="m-item__note">{{ row.user_agent || '未上报 User-Agent' }}</div>
          </div>
        </article>
        <div v-if="!accessLogs.length && !accessLoading" class="m-empty">没有访问记录</div>
      </div>
      <div class="pager">
        <el-button :disabled="accessQuery.page <= 1" @click="turnAccessPage(-1)">上一页</el-button>
        <el-button :disabled="accessQuery.page * accessQuery.pageSize >= accessQuery.total" @click="turnAccessPage(1)">下一页</el-button>
      </div>
    </template>

    <MActionSheet
      v-model="actionsOpen"
      :title="actionTarget?.name"
      :details="details"
      :actions="actions"
      @select="runAction"
    />
    <MQrSheet v-model="qrOpen" :title="qrTitle" :items="qrItems" />

    <MSheet v-model="accessFilterOpen" title="筛选访问记录">
      <div class="m-field">
        <label class="m-field__label">客户端配置</label>
        <MPicker
          v-model="accessQuery.config_id"
          :options="[{ value: '', label: '全部配置' }, ...configs.map((config) => ({ value: config.id, label: config.name }))]"
          title="选择客户端配置"
          placeholder="全部配置"
        />
      </div>
      <div class="m-field"><label class="m-field__label">IP</label><el-input v-model="accessQuery.ip" clearable aria-label="IP" /></div>
      <div class="m-field"><label class="m-field__label">归属地</label><el-input v-model="accessQuery.location" clearable aria-label="归属地" /></div>
      <div class="m-field"><label class="m-field__label">User-Agent</label><el-input v-model="accessQuery.user_agent" clearable aria-label="User-Agent" /></div>
      <template #footer>
        <el-button @click="accessFilterOpen = false">取消</el-button>
        <el-button type="primary" @click="searchAccess">查询</el-button>
      </template>
    </MSheet>

    <template v-if="canWrite && tab === 'configs'" #fab>
      <button type="button" class="m-fab" aria-label="新建客户端配置" @click="openForm()">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>

  <MPage v-else :title="editing ? '编辑客户端配置' : '新建客户端配置'" back @back="mode = 'list'">
    <MSegmented v-model="activeSection" :options="sectionOptions" />

    <template v-if="activeSection === 'basic'">
      <div class="m-field">
        <label class="m-field__label">配置名称 <em>*</em></label>
        <el-input v-model="form.name" maxlength="128" aria-label="配置名称" />
      </div>
      <div class="m-field">
        <label class="m-field__label">引用代理分组 <em>*</em></label>
        <MPicker v-model="form.proxy_group_ids" :options="groupOptions" multiple title="选择代理分组" placeholder="选择代理分组" />
      </div>
      <div class="m-field">
        <label class="m-field__label">规则供应商</label>
        <MPicker v-model="form.rule_provider_ids" :options="providerOptions" multiple title="选择规则供应商" placeholder="选择规则供应商" />
        <div class="m-field__hint">选择后可在「访问规则」的 RULE-SET 规则中引用。</div>
      </div>
    </template>

    <template v-else-if="activeSection === 'rules'">
      <MSegmented v-model="form.rule_mode" :options="[{ value: 'table', label: '表格配置' }, { value: 'text', label: '高级纯文本' }]" />
      <template v-if="form.rule_mode === 'table'">
        <!-- 桌面版是一张五列表格，手机上一条规则一张卡：点卡片改内容，
             卡片右侧留出上下移动，顺序就是匹配顺序。 -->
        <div v-for="(rule, index) in form.rules" :key="index" class="rule">
          <button type="button" class="rule__body" @click="editRule(index)">
            <span class="rule__type">{{ rule.type }}</span>
            <span class="rule__value">{{ rule.type === 'MATCH' ? '全部流量' : (rule.value || '未填写匹配值') }}</span>
            <span class="rule__action">→ {{ rule.action || '未选择动作' }}<i v-if="rule.no_resolve">no-resolve</i></span>
          </button>
          <div class="rule__order">
            <button type="button" aria-label="上移" :disabled="index === 0" @click="moveRule(index, -1)">↑</button>
            <button type="button" aria-label="下移" :disabled="index === form.rules.length - 1" @click="moveRule(index, 1)">↓</button>
          </div>
        </div>
        <div v-if="!form.rules.length" class="m-empty">还没有规则。最后一条必须是 MATCH。</div>
        <el-button :icon="Plus" class="wide" @click="addRule">添加规则</el-button>
        <div v-if="form.rules.length && form.rules.at(-1)?.type !== 'MATCH'" class="m-notice m-notice--warning">最后一条规则必须是 MATCH，否则没有兜底动作。</div>
      </template>
      <template v-else>
        <el-input v-model="form.raw_rules" type="textarea" :rows="14" aria-label="高级规则文本" class="m-mono" placeholder="RULE-SET,供应商名称,代理组&#10;DOMAIN-SUFFIX,example.com,代理组&#10;MATCH,DIRECT" />
      </template>
    </template>

    <template v-else-if="activeSection === 'access'">
      <div class="m-field">
        <div class="m-field__hint" style="margin-bottom: 4px">
          三项都可以留空，留空即不限制。它们管的是「拿到更新地址之后还能不能拉到配置」；
          已经下载过的配置照样能连节点，要断掉某个人请去停用他名下的端点。
        </div>
      </div>
      <div class="m-field">
        <label class="m-field__label">访问密钥</label>
        <el-input v-model="form.access_secret" maxlength="64" aria-label="访问密钥" placeholder="留空表示不校验" />
        <div class="access-actions">
          <el-button size="small" @click="generateAccessSecret">随机生成</el-button>
          <el-button size="small" :disabled="!form.access_secret.trim()" @click="copyAccessUserAgent">复制 UA</el-button>
        </div>
        <div class="m-field__hint">
          填写后，客户端订阅设置里的 User-Agent 必须包含 polaris/{{ form.access_secret.trim() || '密钥' }} 才能拉到配置。
          只能用字母、数字、下划线和短横线，长度 8-64 位。转达时请和更新地址分开发送。
        </div>
      </div>
      <div class="m-field">
        <label class="m-field__label">允许访问的时间段</label>
        <div class="access-actions">
          <el-time-select v-model="form.access_window_start" start="00:00" step="00:30" end="23:30" placeholder="开始" aria-label="时间段开始" style="flex: 1" />
          <el-time-select v-model="form.access_window_end" start="00:00" step="00:30" end="23:30" placeholder="结束" aria-label="时间段结束" style="flex: 1" />
          <el-button size="small" @click="form.access_window_start = ''; form.access_window_end = ''">清除</el-button>
        </div>
        <div class="m-field__hint">
          按控制台所在服务器的本地时间判断。两端都留空表示任意时间都能拉；
          开始晚于结束表示跨过午夜，例如 22:00 至 06:00 指的是整个夜间。
        </div>
      </div>
      <div class="m-field">
        <label class="m-field__label">有效期</label>
        <el-date-picker v-model="form.access_expires_at" type="datetime" value-format="YYYY-MM-DDTHH:mm:ssZ" aria-label="有效期" placeholder="留空表示长期有效" style="width: 100%" />
        <div class="m-field__hint">到期后这个更新地址不再返回配置。需要续期就回来往后调，或者清空。</div>
      </div>
    </template>

    <template v-else>
      <MSegmented :model-value="form.dns_mode" :options="[{ value: 'form', label: '表单配置' }, { value: 'text', label: '高级纯文本' }]" @update:model-value="setDNSMode" />
      <template v-if="form.dns_mode === 'form'">
        <div class="m-field m-field--inline">
          <label class="m-field__label">下发 DNS 配置</label>
          <el-switch v-model="form.dns.enable" aria-label="下发 DNS 配置" />
        </div>
        <div class="m-field__hint" style="margin: -10px 0 16px">关闭后订阅不含 dns 段，由客户端自己的 DNS 设置决定。</div>
        <template v-if="form.dns.enable">
          <div class="m-field">
            <label class="m-field__label">解析模式</label>
            <MPicker v-model="form.dns.enhanced_mode" :options="enhancedModes" title="选择解析模式" />
          </div>
          <template v-if="form.dns.enhanced_mode === 'fake-ip'">
            <div class="m-field">
              <label class="m-field__label">Fake-IP 网段</label>
              <el-input v-model="form.dns.fake_ip_range" aria-label="Fake-IP 网段" placeholder="198.18.0.1/16" />
            </div>
            <div class="m-field">
              <label class="m-field__label">Fake-IP 过滤模式</label>
              <MSegmented v-model="form.dns.fake_ip_filter_mode" :options="fakeIPFilterModes.map((value) => ({ value, label: value }))" />
            </div>
            <div class="m-field">
              <label class="m-field__label">Fake-IP 过滤</label>
              <MValueList v-model="form.dns.fake_ip_filter" placeholder="rule 模式写规则，如 MATCH,fake-ip" />
              <div class="m-field__hint">rule 模式下每条是一条规则，动作只能是 fake-ip 或 real-ip，自上而下匹配。</div>
            </div>
          </template>
          <div v-for="field in nameserverFields" :key="field.key" class="m-field">
            <label class="m-field__label">{{ field.label }}</label>
            <MValueList v-model="form.dns[field.key]" placeholder="如 https://223.5.5.5/dns-query 或 system" />
            <div class="m-field__hint">{{ field.hint }}</div>
          </div>
          <div class="m-field m-field--inline"><label class="m-field__label">解析 IPv6</label><el-switch v-model="form.dns.ipv6" aria-label="解析 IPv6" /></div>
          <div class="m-field m-field--inline"><label class="m-field__label">DNS 连接遵守路由规则</label><el-switch v-model="form.dns.respect_rules" aria-label="DNS 连接遵守路由规则" /></div>
          <div class="m-field m-field--inline"><label class="m-field__label">直连解析遵守 nameserver-policy</label><el-switch v-model="form.dns.direct_nameserver_follow_policy" aria-label="直连解析遵守 nameserver-policy" /></div>
        </template>
      </template>
      <template v-else>
        <el-input v-model="form.raw_dns" type="textarea" :rows="14" aria-label="高级 DNS 文本" class="m-mono" placeholder="enable: true&#10;enhanced-mode: fake-ip&#10;nameserver:&#10;  - https://223.5.5.5/dns-query" />
        <div class="m-field__hint">按 Mihomo 的 dns 段格式书写，内容会原样写入订阅；留空则订阅不含 dns 段。</div>
      </template>
    </template>

    <!-- 抽屉本身会 Teleport 到 body，放在页面里只是为了让这个组件保持
         单个根节点：外层的页面过渡对多根组件会失效，切走之后就再也切不回来。 -->
    <MSheet v-model="ruleSheet" title="编辑规则">
      <template v-if="editingRule">
        <div class="m-field">
          <label class="m-field__label">类型</label>
          <MPicker :model-value="editingRule.type" :options="ruleTypes.map((type) => ({ value: type, label: type }))" title="选择规则类型" @update:model-value="setRuleType" />
        </div>
        <div class="m-field">
          <label class="m-field__label">匹配值</label>
          <MPicker v-if="editingRule.type === 'RULE-SET'" v-model="editingRule.value" :options="providerValueOptions" title="选择规则供应商" placeholder="先在「基础信息」里引用供应商" />
          <el-input v-else v-model="editingRule.value" :disabled="editingRule.type === 'MATCH'" aria-label="规则匹配值" :placeholder="editingRule.type === 'MATCH' ? '兜底规则，无需填写' : '例如 example.com'" />
        </div>
        <div class="m-field">
          <label class="m-field__label">动作</label>
          <MPicker v-model="editingRule.action" :options="ruleActionOptions" title="选择规则动作" placeholder="选择节点、分组或 DIRECT / REJECT" />
        </div>
        <div class="m-field m-field--inline">
          <label class="m-field__label">no-resolve</label>
          <el-switch v-model="editingRule.no_resolve" :disabled="!noResolveTypes.has(editingRule.type)" aria-label="no-resolve" />
        </div>
        <el-button text type="danger" @click="removeRule(ruleIndex)">删除这条规则</el-button>
      </template>
      <template #footer>
        <el-button type="primary" @click="ruleSheet = false">完成</el-button>
      </template>
    </MSheet>

    <template #foot>
      <div class="m-form-foot">
        <el-button @click="mode = 'list'">返回</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </div>
    </template>
  </MPage>
</template>

<style scoped>
.filter { margin-top: 10px; }
.access-actions { display: flex; align-items: center; gap: 8px; margin-top: 8px; }
.access-bar { display: flex; align-items: center; gap: 10px; }
.access-bar .m-count { flex: 1; padding-bottom: 0; }
.pager { display: flex; gap: 10px; margin-top: 14px; }
.pager :deep(.el-button) { flex: 1; height: var(--m-tap); margin: 0; }
.wide { width: 100%; height: var(--m-tap); margin-top: 10px; }

.rule { display: flex; align-items: stretch; gap: 8px; margin-bottom: 8px; }
.rule__body {
  flex: 1;
  min-width: 0;
  padding: 10px 12px;
  color: var(--sb-text);
  text-align: left;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  font: inherit;
  cursor: pointer;
}
.rule__type { display: inline-block; color: var(--sb-accent); font-size: 11.5px; font-weight: 600; letter-spacing: .4px; }
.rule__value { display: block; margin-top: 3px; font-size: 14px; word-break: break-all; }
.rule__action { display: block; margin-top: 4px; color: var(--sb-muted); font-size: 12.5px; word-break: break-all; }
.rule__action i { margin-left: 6px; padding: 1px 5px; background: rgba(148, 163, 184, .14); border-radius: 5px; font-style: normal; font-size: 11px; }
.rule__order { flex: none; display: flex; flex-direction: column; gap: 4px; }
.rule__order button {
  width: 38px;
  flex: 1;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .07);
  border: 1px solid var(--sb-line);
  border-radius: 9px;
  font-size: 14px;
  cursor: pointer;
}
.rule__order button:disabled { color: #475569; }
</style>
