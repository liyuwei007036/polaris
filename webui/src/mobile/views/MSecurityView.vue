<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, post } from '../../api'
import { formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

// 这个页面不存任何东西。下面每一份清单都是各台服务器刚刚报上来的，
// 每一次修改都写进服务器自己的防火墙或 Fail2Ban，并等它确认后才显示为完成。
const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const unbanning = ref('')
const tab = ref('ports')
const firewallNodes = ref([])
const fail2banNodes = ref([])
const banned = ref([])
const selectedNode = ref('')
const keyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const nodeName = (nodeID) => nodeNames.value[nodeID] || nodeID
const nodeOptions = computed(() => appState.nodes.map((node) => ({ value: node.id, label: node.name })))
const nodeFilterOptions = computed(() => [{ value: '', label: '全部服务器' }, ...nodeOptions.value])

// 读不到的服务器单独点名。给它们显示一张空表等于说「这台没有限制」，
// 而那正好是相反的意思。
const unreachable = computed(() => {
  const reasons = {}
  for (const node of firewallNodes.value) if (node.error) reasons[node.node_id] = node.error
  for (const node of fail2banNodes.value) if (node.error && !reasons[node.node_id]) reasons[node.node_id] = node.error
  const entries = Object.entries(reasons).filter(([nodeID]) => !selectedNode.value || nodeID === selectedNode.value)
  if (!entries.length) return ''
  return `${entries.map(([nodeID]) => nodeName(nodeID)).join('、')}：${entries[0][1]}`
})

const portRules = computed(() => firewallNodes.value.flatMap((node) => (node.port_rules || [])
  .map((rule, index) => ({ ...rule, key: `${node.node_id}-port-${index}`, manager: node.manager })))
  .filter((row) => {
    if (selectedNode.value && row.node_id !== selectedNode.value) return false
    return includesText(
      [nodeName(row.node_id), row.protocol, row.port, row.service, (row.sources || []).join(' '), (row.locations || []).join(' ')],
      keyword.value,
    )
  }))

function portLabel(row) {
  if (!row.port) return row.service || '—'
  return row.port_end ? `${row.port}-${row.port_end}` : String(row.port)
}

function sourceLabel(row) {
  const sources = row.sources || []
  if (!sources.length) return ''
  return sources.map((source, index) => {
    const location = (row.locations || [])[index]
    return location ? `${source} · ${location}` : source
  }).join('、')
}

// 防火墙默认放行一切的服务器，下面这份端口清单只是「写下来的」，
// 不是「能连上的全部」。不说明就是一份误导人的清单。
const wideOpenNodes = computed(() => firewallNodes.value
  .filter((node) => node.available && node.default_incoming === 'accept')
  .filter((node) => !selectedNode.value || node.node_id === selectedNode.value)
  .map((node) => nodeName(node.node_id)))

const truncatedNodes = computed(() => firewallNodes.value
  .filter((node) => node.truncated)
  .filter((node) => !selectedNode.value || node.node_id === selectedNode.value)
  .map((node) => nodeName(node.node_id)))

const jails = computed(() => fail2banNodes.value.flatMap((node) => (node.jails || [])
  .map((jail) => ({ ...jail, key: `${node.node_id}-${jail.name}` })))
  .filter((row) => {
    if (selectedNode.value && row.node_id !== selectedNode.value) return false
    return includesText([nodeName(row.node_id), row.name, row.log_path, row.filter_name], keyword.value)
  }))

const filteredBanned = computed(() => banned.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  return includesText([nodeName(row.node_id), row.ip, row.location, row.rule_name, row.jail], keyword.value)
}))

const firewallOpen = ref(false)
const jailOpen = ref(false)
const firewall = reactive({ node_id: '', action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443 })
const jail = reactive({ node_id: '', name: '', log_path: '', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, ports: '' })
const selectedTemplate = ref('sshd')
const editingJail = ref(false)

// sing-box 的日志写在这里，因为编译出来的配置就指向这个路径；
// 盯着别的路径的规则永远不会匹配到任何东西。
const singBoxLogPath = '/var/log/sing-box/sing-box.log'

const templates = {
  sshd: {
    label: 'SSH 暴力破解（推荐）',
    description: '反复尝试 SSH 登录的来源 IP 会被防火墙封禁。',
    name: 'ssh-bruteforce',
    filter_name: 'ssh-bruteforce',
    log_path: '/var/log/auth.log',
    fail_regex: '^.*sshd.*(Failed password|Invalid user|Connection closed by authenticating user).* from <HOST>.*$',
    max_retry: 5,
    find_time_seconds: 600,
    ban_time_seconds: 86400,
    ports: '',
  },
  proxyauth: {
    label: '代理认证失败',
    description: '反复使用错误密码或 UUID 连接代理的来源 IP 会被防火墙封禁。',
    name: 'proxy-auth',
    filter_name: 'proxy-auth',
    log_path: singBoxLogPath,
    fail_regex: '^.*inbound/.*: process connection from <HOST>:\\d+:.*(authenticat|password|user).*$',
    max_retry: 5,
    find_time_seconds: 600,
    ban_time_seconds: 3600,
    ports: '',
  },
  scan: {
    label: '端口扫描与探测',
    description: '短时间内大量失败连接的来源 IP 会被防火墙封禁。',
    name: 'port-scan',
    filter_name: 'port-scan',
    log_path: singBoxLogPath,
    fail_regex: '^.*inbound/.*: process connection from <HOST>:\\d+: .*$',
    max_retry: 60,
    find_time_seconds: 60,
    ban_time_seconds: 1800,
    ports: '',
  },
  custom: { label: '自定义规则', description: '自行填写要检查的日志文件和匹配规则。' },
}
const templateOptions = Object.entries(templates).map(([value, template]) => ({ value, label: template.label, desc: template.description }))

function applyTemplate(key) {
  selectedTemplate.value = key
  const template = templates[key]
  if (!template || key === 'custom') return
  Object.assign(jail, {
    name: template.name,
    filter_name: template.filter_name,
    log_path: template.log_path,
    fail_regex: template.fail_regex,
    max_retry: template.max_retry,
    find_time_seconds: template.find_time_seconds,
    ban_time_seconds: template.ban_time_seconds,
    ports: template.ports,
  })
}

// load 是挨个问服务器现在在执行什么。比读一张表慢，这正是重点：答案是当下的。
async function load() {
  loading.value = true
  try {
    const [firewallResult, jailResult, bannedResult] = await Promise.all([
      api('/firewall/rules').catch(() => ({ nodes: [] })),
      api('/fail2ban/jails').catch(() => ({ nodes: [] })),
      api('/fail2ban/banned').catch(() => ({ banned: [] })),
    ])
    firewallNodes.value = firewallResult.nodes || []
    fail2banNodes.value = jailResult.nodes || []
    banned.value = bannedResult.banned || []
  } finally { loading.value = false }
}

function defaultNode() { return appState.nodes[0]?.id || '' }

function addFirewall() {
  Object.assign(firewall, { node_id: defaultNode(), action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443 })
  firewallOpen.value = true
}

// 每次修改都直达服务器防火墙，屏幕上的内容由服务器自己的回答替换，
// 服务器没确认的事不会被报成已生效。
async function changeFirewall(nodeID, body, successMessage) {
  saving.value = true
  try {
    const answer = await post(`/nodes/${nodeID}/firewall/rules`, body)
    firewallNodes.value = firewallNodes.value.map((node) => (node.node_id === nodeID ? answer : node))
    ElMessage.success(successMessage)
    return true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器未能完成这次修改')
    return false
  } finally { saving.value = false }
}

async function saveFirewall() {
  const applied = await changeFirewall(firewall.node_id, {
    operation: 'add', action: firewall.action, protocol: firewall.protocol,
    port: Number(firewall.port), cidr: firewall.cidr,
  }, '访问限制已在服务器防火墙上生效')
  if (applied) firewallOpen.value = false
}

async function removePortRule(row) {
  const sources = (row.sources || []).length ? row.sources.join('、') : '所有来源'
  await ElMessageBox.confirm(
    `确认在 ${nodeName(row.node_id)} 上删除“${statusLabel(row).text} ${row.protocol.toUpperCase()} ${row.port}（来源：${sources}）”这条规则？删除后立即生效。`,
    '删除访问限制', { type: 'warning' },
  )
  await changeFirewall(row.node_id, {
    operation: 'delete', action: row.action, protocol: row.protocol,
    port: Number(row.port), cidr: (row.sources || [])[0] || '',
    family: row.family, table: row.table, chain: row.chain, handle: row.handle, raw: row.raw,
  }, '访问限制已从服务器防火墙移除')
}

// 能撤回的端口。端口范围和来自命名服务的端口都不动：ufw 和 firewalld
// 都无法从中撤掉单独一个端口，给一个按不动的按钮比不给更糟。
function removable(row) {
  return Boolean(row.port) && !row.port_end && Boolean(row.protocol) && !row.service
}

function addJail() {
  Object.assign(jail, { node_id: defaultNode(), name: '', log_path: '', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, ports: '' })
  applyTemplate('sshd')
  editingJail.value = false
  jailOpen.value = true
}

function editJail(row) {
  Object.assign(jail, {
    node_id: row.node_id, name: row.name, log_path: row.log_path, filter_name: row.filter_name,
    fail_regex: row.fail_regex, max_retry: row.max_retry, find_time_seconds: row.find_time_seconds,
    ban_time_seconds: row.ban_time_seconds, ports: row.ports || '',
  })
  selectedTemplate.value = 'custom'
  editingJail.value = true
  jailOpen.value = true
}

async function changeJail(nodeID, body, successMessage) {
  saving.value = true
  try {
    const answer = await post(`/nodes/${nodeID}/fail2ban/jails`, body)
    fail2banNodes.value = fail2banNodes.value.map((node) => (node.node_id === nodeID ? answer : node))
    ElMessage.success(successMessage)
    return true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器未能完成这次修改')
    return false
  } finally { saving.value = false }
}

async function saveJail() {
  const applied = await changeJail(jail.node_id, { ...jail, operation: 'save' }, '自动封禁规则已在服务器上生效')
  if (applied) {
    jailOpen.value = false
    await load()
  }
}

async function removeJail(row) {
  await ElMessageBox.confirm(`确认从 ${nodeName(row.node_id)} 上删除自动封禁规则“${row.name}”？删除后立即生效。`, '删除自动封禁规则', { type: 'warning' })
  await changeJail(row.node_id, { operation: 'delete', name: row.name }, '自动封禁规则已从服务器移除')
}

async function unban(row) {
  await ElMessageBox.confirm(
    `解封后 ${row.ip} 可以立即重新连接 ${nodeName(row.node_id)}。如果它再次触发规则，仍会被自动封禁。`,
    '解封 IP',
    { type: 'warning', confirmButtonText: '确认解封' },
  )
  unbanning.value = `${row.node_id}-${row.jail}-${row.ip}`
  try {
    await post(`/nodes/${row.node_id}/fail2ban/unban`, { jail: row.jail, ip: row.ip })
    ElMessage.success(`已解封 ${row.ip}`)
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '解封未完成，请稍后重试')
  } finally {
    unbanning.value = ''
  }
}

const statusLabels = {
  accept: { text: '开放', pill: 'm-pill--success' },
  drop: { text: '禁止', pill: 'm-pill--danger' },
  reject: { text: '禁止', pill: 'm-pill--danger' },
}
function statusLabel(row) { return statusLabels[row.action] || { text: '未知', pill: 'm-pill--info' } }

function jailRuntime(row) {
  if (row.error) return { text: '未生效：' + row.error, pill: 'm-pill--danger' }
  if (!row.running) return { text: '已配置但未运行', pill: 'm-pill--danger' }
  return { text: `运行中 · 当前封禁 ${row.currently_banned || 0} · 累计 ${row.total_banned || 0}`, pill: 'm-pill--success' }
}

function openJailActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const jailActions = computed(() => {
  const row = actionTarget.value
  if (!row || !isAdmin.value || !row.managed) return []
  return [{ key: 'edit', label: '修改' }, { key: 'delete', label: '删除', danger: true }]
})

function runJailAction(key) {
  if (key === 'edit') return editJail(actionTarget.value)
  if (key === 'delete') return removeJail(actionTarget.value)
}

const tabs = computed(() => [
  { value: 'ports', label: '访问限制', badge: portRules.value.length },
  { value: 'fail2ban', label: '自动封禁', badge: jails.value.length },
  { value: 'banned', label: '已封禁 IP', badge: filteredBanned.value.length },
])

onMounted(load)
</script>

<template>
  <MPage title="网络防护" :loading="loading">
    <template #actions>
      <el-button :icon="Refresh" circle aria-label="刷新" :loading="loading" @click="load" />
      <el-button
        v-if="isAdmin && tab !== 'banned'"
        type="primary" :icon="Plus" circle aria-label="添加规则"
        :disabled="!appState.nodes.length"
        @click="tab === 'ports' ? addFirewall() : addJail()"
      />
    </template>

    <div class="m-notice m-notice--info">
      这里是各台服务器上正在生效的防火墙与封禁规则，每次打开或刷新都会实时读取；添加和删除会立即写入服务器。
    </div>
    <div v-if="unreachable" class="m-notice m-notice--warning">{{ unreachable }}</div>

    <MSegmented v-model="tab" :options="tabs" />

    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索服务器、地址或规则" />
    <div class="filter">
      <MPicker v-model="selectedNode" :options="nodeFilterOptions" title="按服务器筛选" placeholder="全部服务器" />
    </div>

    <template v-if="tab === 'ports'">
      <div v-if="wideOpenNodes.length" class="m-notice m-notice--warning">
        {{ wideOpenNodes.join('、') }} 的防火墙默认放行所有入站流量，下面列出的端口不是全部可访问的端口。
      </div>
      <div v-if="truncatedNodes.length" class="m-notice m-notice--warning">
        {{ truncatedNodes.join('、') }} 的防火墙规则过多，只读取了前面一部分，下面的端口可能不全。
      </div>

      <article v-for="row in portRules" :key="row.key" class="m-card">
        <div class="m-card__top">
          <span class="m-pill" :class="statusLabel(row).pill">{{ statusLabel(row).text }}</span>
          <span class="m-card__title m-mono">{{ row.protocol ? row.protocol.toUpperCase() : '全部' }} {{ portLabel(row) }}</span>
          <el-button v-if="isAdmin && removable(row)" link type="danger" :loading="saving" @click="removePortRule(row)">删除</el-button>
        </div>
        <div class="m-card__row"><span>{{ nodeName(row.node_id) }}</span><span class="m-card__spacer" /><span>{{ row.service ? `服务 ${row.service}` : (row.manager || '防火墙规则') }}</span></div>
        <div class="m-card__note">
          <template v-if="(row.sources || []).length">来源 <span class="m-mono">{{ sourceLabel(row) }}</span></template>
          <template v-else>来源：所有来源</template>
        </div>
      </article>
      <div v-if="!portRules.length && !loading" class="m-empty">服务器防火墙没有针对任何端口的规则</div>
    </template>

    <template v-else-if="tab === 'fail2ban'">
      <article v-for="row in jails" :key="row.key" class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">{{ row.name }}</span>
          <span class="m-pill" :class="row.managed ? 'm-pill--success' : 'm-pill--info'">{{ row.managed ? '本平台' : '系统已有' }}</span>
          <button v-if="isAdmin && row.managed" type="button" class="m-more-btn" :aria-label="`${row.name} 的操作`" @click="openJailActions(row)">⋯</button>
        </div>
        <div class="m-card__row"><span>{{ nodeName(row.node_id) }}</span><span class="m-card__spacer" /><span>失败 {{ row.max_retry || '—' }} 次</span></div>
        <div class="m-card__row"><span class="m-mono">{{ row.log_path || '—' }}</span></div>
        <div class="m-card__row">
          <span>范围 {{ row.managed ? (row.ports || '全部端口') : '—' }}</span>
          <span class="m-card__spacer" />
          <span>封禁 {{ row.ban_time_seconds ? `${row.ban_time_seconds} 秒` : '—' }}</span>
        </div>
        <div class="m-card__row"><span class="m-pill" :class="jailRuntime(row).pill">{{ jailRuntime(row).text }}</span></div>
      </article>
      <div v-if="!jails.length && !loading" class="m-empty">服务器上没有自动封禁规则</div>
    </template>

    <template v-else>
      <article v-for="row in filteredBanned" :key="`${row.node_id}-${row.jail}-${row.ip}`" class="m-card">
        <div class="m-card__top">
          <span class="m-card__title m-mono">{{ row.ip }}</span>
          <el-button
            v-if="isAdmin" link type="primary"
            :loading="unbanning === `${row.node_id}-${row.jail}-${row.ip}`"
            :disabled="Boolean(unbanning)"
            @click="unban(row)"
          >解封</el-button>
        </div>
        <div class="m-card__row"><span>{{ nodeName(row.node_id) }}</span><span class="m-card__spacer" /><span>{{ row.location || '归属地未知' }}</span></div>
        <div class="m-card__row"><span>{{ row.rule_name || row.jail }} · {{ row.managed ? '本平台规则' : '服务器已有规则' }}</span></div>
        <div class="m-card__row">
          <span>封禁 {{ formatDateTime(row.banned_at, '未知') }}</span>
          <span class="m-card__spacer" />
          <span>解封 {{ formatDateTime(row.unban_at, '不自动解封') }}</span>
        </div>
      </article>
      <div v-if="!filteredBanned.length && !loading" class="m-empty">当前没有被封禁的 IP</div>
    </template>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name" :actions="jailActions" @select="runJailAction" />

    <MSheet v-model="firewallOpen" title="添加访问限制" full>
      <div class="m-field">
        <label class="m-field__label">服务器</label>
        <MPicker v-model="firewall.node_id" :options="nodeOptions" title="选择服务器" />
      </div>
      <div class="m-field">
        <label class="m-field__label">处理方式</label>
        <MSegmented v-model="firewall.action" :options="[{ value: 'accept', label: '允许' }, { value: 'drop', label: '拒绝' }]" />
      </div>
      <div class="m-field">
        <label class="m-field__label">协议</label>
        <MSegmented v-model="firewall.protocol" :options="[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]" />
      </div>
      <div class="m-field">
        <label class="m-field__label">端口</label>
        <el-input-number v-model="firewall.port" :min="1" :max="65535" controls-position="right" aria-label="端口" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">允许或拒绝的来源地址范围</label>
        <el-input v-model="firewall.cidr" aria-label="来源地址范围" placeholder="例如 192.168.1.0/24，留空表示所有来源" />
      </div>
      <div class="m-notice m-notice--info">
        规则写入服务器自己的防火墙（ufw / firewalld，都没有时写入 iptables），保存后可在「访问限制」中看到结果。
        {{ firewall.action === 'accept' ? `${firewall.protocol.toUpperCase()} ${firewall.port} 端口将对列出的来源放行。` : `${firewall.protocol.toUpperCase()} ${firewall.port} 端口将拒绝列出的来源。` }}
      </div>
      <div class="m-notice m-notice--warning">
        云服务器还有厂商侧的安全组，平台无法读取或修改；端口在这里放行后仍连不上，请到云控制台放行同一端口。
      </div>
      <template #footer>
        <el-button @click="firewallOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveFirewall">保存并生效</el-button>
      </template>
    </MSheet>

    <MSheet v-model="jailOpen" :title="editingJail ? '修改自动封禁规则' : '添加自动封禁规则'" full>
      <div class="m-field">
        <label class="m-field__label">服务器</label>
        <MPicker v-model="jail.node_id" :options="nodeOptions" :disabled="editingJail" title="选择服务器" />
      </div>
      <div class="m-field">
        <label class="m-field__label">规则类型</label>
        <MPicker :model-value="selectedTemplate" :options="templateOptions" title="选择规则类型" @update:model-value="applyTemplate" />
        <div class="m-field__hint">{{ templates[selectedTemplate].description }}</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">规则名称 <em>*</em></label>
        <el-input v-model="jail.name" :disabled="editingJail" aria-label="规则名称" placeholder="只能使用字母、数字、下划线和短横线" />
      </div>
      <div class="m-field">
        <label class="m-field__label">检测器名称 <em>*</em></label>
        <el-input v-model="jail.filter_name" aria-label="检测器名称" placeholder="只能使用字母、数字、下划线和短横线" />
      </div>
      <div class="m-field">
        <label class="m-field__label">检查的日志文件 <em>*</em></label>
        <el-input v-model="jail.log_path" aria-label="检查的日志文件" />
      </div>
      <div class="m-field">
        <label class="m-field__label">失败记录匹配规则 <em>*</em></label>
        <el-input v-model="jail.fail_regex" type="textarea" :rows="5" aria-label="失败记录匹配规则" class="m-mono" />
        <div class="m-field__hint">&lt;HOST&gt; 代表要封禁的来源 IP，必须保留。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">允许失败次数</label>
        <el-input-number v-model="jail.max_retry" :min="1" controls-position="right" aria-label="允许失败次数" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">统计时间范围（秒）</label>
        <el-input-number v-model="jail.find_time_seconds" :min="1" controls-position="right" aria-label="统计时间范围" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">封禁时长（秒）</label>
        <el-input-number v-model="jail.ban_time_seconds" :min="1" controls-position="right" aria-label="封禁时长" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">封禁范围</label>
        <el-input v-model="jail.ports" aria-label="封禁范围" placeholder="留空表示禁止该 IP 连接所有端口" />
        <div class="m-field__hint">多个端口用逗号分隔，如 443,8443。</div>
      </div>
      <div class="m-notice m-notice--info">缺少的 Fail2Ban、防火墙链与日志文件会自动安装或创建。保存后立即在服务器上生效。</div>
      <template #footer>
        <el-button @click="jailOpen = false">取消</el-button>
        <el-button
          type="primary" :loading="saving"
          :disabled="!jail.name || !jail.filter_name || !jail.fail_regex || !jail.log_path"
          @click="saveJail"
        >保存并生效</el-button>
      </template>
    </MSheet>
  </MPage>
</template>

<style scoped>
.filter { margin: 10px 0 4px; }
</style>
