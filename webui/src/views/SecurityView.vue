<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, post } from '../api'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

// Nothing on this page is stored by the platform. Every list below is what the
// servers themselves reported a moment ago, and every change is applied to a
// server's own firewall or Fail2Ban and confirmed before it is shown as done.
// A rule that exists only in a console is a rule that protects nobody.

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

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const nodeName = (nodeID) => nodeNames.value[nodeID] || nodeID

// What each server's own firewall decides about each port it names — opened or
// refused. That is the whole of access restriction: a rule that blocks one
// address says nothing about which ports a server offers, so address bans are
// not here. They belong to automatic banning, one tab over.
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

// Each source is shown with where it is, in the order the server reported them.
function sourceLabel(row) {
  const sources = row.sources || []
  if (!sources.length) return ''
  return sources.map((source, index) => {
    const location = (row.locations || [])[index]
    return location ? `${source} · ${location}` : source
  }).join('、')
}

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

// sing-box writes this log because the compiled configuration points it here;
// a jail watching any other path will never match anything.
const singBoxLogPath = '/var/log/sing-box/sing-box.log'

// Automatic banning is a system-level control: a matching IP is blocked by
// the server's firewall and cannot connect at all. The presets cover the
// usual sources of attack traffic so nobody has to hand-write a regex.
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

function applyTemplate(key) {
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

// load asks every server what it is enforcing. It is slower than reading a
// table would be, and that is the point: the answer is current.
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

// Every change here goes straight to the server's firewall and the server's own
// answer replaces what is on screen, so nothing is reported as applied unless
// the server confirmed it.
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

// Removing a rule states the port to withdraw, which is how ufw and firewalld
// name it, and where the rule sits, which is what a server without either of
// them deletes by — two rules can read identically, so the server has to remove
// the one on screen.
async function removePortRule(row) {
  const sources = (row.sources || []).length ? row.sources.join('、') : '所有来源'
  const verb = statusLabel(row).text
  await ElMessageBox.confirm(
    `确认在 ${nodeName(row.node_id)} 上删除“${verb} ${row.protocol.toUpperCase()} ${row.port}（来源：${sources}）”这条规则？删除后立即生效。`,
    '删除访问限制', { type: 'warning' },
  )
  await changeFirewall(row.node_id, {
    operation: 'delete', action: row.action, protocol: row.protocol,
    port: Number(row.port), cidr: (row.sources || [])[0] || '',
    family: row.family, table: row.table, chain: row.chain, handle: row.handle, raw: row.raw,
  }, '访问限制已从服务器防火墙移除')
}

// A port a rule can be withdrawn for. A port range and a port that comes from a
// named service are both left alone: neither ufw nor firewalld can withdraw one
// port out of them, and offering a button that cannot work is worse than
// offering none — firewalld answers such a request with a warning and success.
function removable(row) {
  return Boolean(row.port) && !row.port_end && Boolean(row.protocol) && !row.service
}

function addJail() {
  Object.assign(jail, { node_id: defaultNode(), name: '', log_path: '', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, ports: '' })
  selectedTemplate.value = 'sshd'
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

// Releasing an address is a runtime action on the server; the server performs
// it before the console reports it.
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
  accept: { text: '开放', type: 'success' },
  drop: { text: '禁止', type: 'danger' },
  reject: { text: '禁止', type: 'danger' },
}
function statusLabel(row) { return statusLabels[row.action] || { text: '未知', type: 'info' } }

function jailRuntime(row) {
  if (row.error) return { text: '未生效：' + row.error, type: 'danger' }
  if (!row.running) return { text: '已配置但未运行', type: 'danger' }
  return { text: `运行中 · 当前封禁 ${row.currently_banned || 0} · 累计 ${row.total_banned || 0}`, type: 'success' }
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="网络防护"><el-button :icon="Refresh" :loading="loading" @click="load">刷新</el-button></PageHeader>
    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索服务器、地址或规则" style="width: 270px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 220px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
      </div>
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane :label="`访问限制（${portRules.length}）`" name="ports">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" :disabled="!appState.nodes.length" @click="addFirewall">添加</el-button></div>
            <PagedTable :rows="portRules" :loading="loading" empty-text="服务器防火墙没有针对任何端口的规则">
              <el-table-column label="服务器" min-width="130" show-overflow-tooltip><template #default="{ row }">{{ nodeName(row.node_id) }}</template></el-table-column>
              <el-table-column label="状态" width="88" align="center">
                <template #default="{ row }"><el-tag :type="statusLabel(row).type">{{ statusLabel(row).text }}</el-tag></template>
              </el-table-column>
              <el-table-column label="端口" width="110" align="center"><template #default="{ row }"><span class="mono">{{ portLabel(row) }}</span></template></el-table-column>
              <el-table-column label="协议" width="88" align="center">
                <template #default="{ row }">{{ row.protocol ? row.protocol.toUpperCase() : '全部' }}</template>
              </el-table-column>
              <el-table-column label="来源 / IP 归属地" min-width="240" show-overflow-tooltip>
                <template #default="{ row }">
                  <span v-if="!(row.sources || []).length" class="subtle">所有来源</span>
                  <span v-else class="mono">{{ sourceLabel(row) }}</span>
                </template>
              </el-table-column>
              <el-table-column label="来自" min-width="150" show-overflow-tooltip>
                <template #default="{ row }">
                  <span v-if="row.service">服务 {{ row.service }}</span>
                  <span v-else class="subtle">{{ row.manager || '防火墙规则' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="84" class-name="action-column">
                <template #default="{ row }">
                  <el-button
                    v-if="isAdmin && removable(row)" link type="danger" :icon="Delete"
                    :loading="saving" @click="removePortRule(row)"
                  >删除</el-button>
                  <span v-else class="subtle">—</span>
                </template>
              </el-table-column>
            </PagedTable>
          </el-tab-pane>
          <el-tab-pane :label="`自动封禁（${jails.length}）`" name="fail2ban">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" :disabled="!appState.nodes.length" @click="addJail">添加</el-button></div>
            <PagedTable :rows="jails" :loading="loading" empty-text="服务器上没有自动封禁规则">
              <el-table-column label="服务器" min-width="130" show-overflow-tooltip><template #default="{ row }">{{ nodeName(row.node_id) }}</template></el-table-column>
              <el-table-column label="规则来源" width="110" align="center">
                <template #default="{ row }"><el-tag :type="row.managed ? 'success' : 'info'" effect="plain">{{ row.managed ? '本平台' : '系统已有' }}</el-tag></template>
              </el-table-column>
              <el-table-column label="规则名称" prop="name" min-width="140" show-overflow-tooltip />
              <el-table-column label="检查的日志文件" min-width="180" show-overflow-tooltip><template #default="{ row }">{{ row.log_path || '—' }}</template></el-table-column>
              <el-table-column label="失败次数" width="96" align="center"><template #default="{ row }">{{ row.max_retry || '—' }}</template></el-table-column>
              <el-table-column label="封禁范围" width="110"><template #default="{ row }">{{ row.managed ? (row.ports || '全部端口') : '—' }}</template></el-table-column>
              <el-table-column label="封禁时间" width="100"><template #default="{ row }">{{ row.ban_time_seconds ? row.ban_time_seconds + ' 秒' : '—' }}</template></el-table-column>
              <el-table-column label="运行情况" min-width="180" show-overflow-tooltip>
                <template #default="{ row }"><el-tag :type="jailRuntime(row).type" effect="plain">{{ jailRuntime(row).text }}</el-tag></template>
              </el-table-column>
              <el-table-column label="操作" width="130" class-name="action-column">
                <template #default="{ row }">
                  <template v-if="isAdmin && row.managed">
                    <el-button link :loading="saving" @click="editJail(row)">修改</el-button>
                    <el-button link type="danger" :icon="Delete" :loading="saving" @click="removeJail(row)">删除</el-button>
                  </template>
                  <span v-else class="subtle">仅查看</span>
                </template>
              </el-table-column>
            </PagedTable>
          </el-tab-pane>
          <el-tab-pane :label="`已封禁 IP（${filteredBanned.length}）`" name="banned">
            <PagedTable :rows="filteredBanned" :loading="loading" empty-text="当前没有被封禁的 IP">
              <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeName(row.node_id) }}</template></el-table-column>
              <el-table-column label="IP 地址 / IP 归属地" min-width="200"><template #default="{ row }"><div class="mono">{{ row.ip }}</div><div class="subtle">{{ row.location || '未知' }}</div></template></el-table-column>
              <el-table-column label="触发的规则" min-width="200">
                <template #default="{ row }">
                  <div>{{ row.rule_name || row.jail }}</div>
                  <div class="subtle">{{ row.managed ? '本平台规则' : '服务器已有规则' }}</div>
                </template>
              </el-table-column>
              <el-table-column label="封禁时间" width="180"><template #default="{ row }">{{ formatDateTime(row.banned_at, '未知') }}</template></el-table-column>
              <el-table-column label="操作" width="110" class-name="action-column">
                <template #default="{ row }">
                  <el-button
                    v-if="isAdmin" link type="primary"
                    :loading="unbanning === `${row.node_id}-${row.jail}-${row.ip}`"
                    :disabled="Boolean(unbanning)"
                    @click="unban(row)"
                  >解封</el-button>
                </template>
              </el-table-column>
            </PagedTable>
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>

    <el-dialog v-model="firewallOpen" title="添加访问限制" width="580px">
      <el-form label-position="top">
        <el-form-item label="服务器"><el-select v-model="firewall.node_id" style="width: 100%"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item>
        <el-row :gutter="16">
          <el-col :span="8"><el-form-item label="处理方式"><el-select v-model="firewall.action"><el-option label="允许" value="accept" /><el-option label="拒绝" value="drop" /></el-select></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="协议"><el-select v-model="firewall.protocol"><el-option label="TCP" value="tcp" /><el-option label="UDP" value="udp" /></el-select></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="端口"><el-input-number v-model="firewall.port" :min="1" :max="65535" style="width: 100%" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="允许或拒绝的来源地址范围"><el-input v-model="firewall.cidr" placeholder="例如 192.168.1.0/24，留空表示所有来源" /></el-form-item>
        <el-alert
          :title="`规则写入服务器自己的防火墙（ufw / firewalld，都没有时写入 nftables），保存后可在“访问限制”中看到结果。${
            firewall.action === 'accept'
              ? `${firewall.protocol.toUpperCase()} ${firewall.port} 端口将对列出的来源放行。`
              : `${firewall.protocol.toUpperCase()} ${firewall.port} 端口将拒绝列出的来源。`}`"
          type="info" show-icon :closable="false" />
        <el-alert
          title="云服务器还有厂商侧的安全组，平台无法读取或修改；端口在这里放行后仍连不上，请到云控制台放行同一端口。"
          type="warning" show-icon :closable="false" style="margin-top: 8px" />
      </el-form>
      <template #footer><el-button @click="firewallOpen = false">取消</el-button><el-button type="primary" :loading="saving" @click="saveFirewall">保存并生效</el-button></template>
    </el-dialog>

    <el-dialog v-model="jailOpen" :title="editingJail ? '修改自动封禁规则' : '添加自动封禁规则'" width="720px">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="服务器"><el-select v-model="jail.node_id" :disabled="editingJail" style="width: 100%"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item></el-col>
          <el-col :span="12">
            <el-form-item label="规则类型">
              <el-select v-model="selectedTemplate" style="width: 100%" @change="applyTemplate">
                <el-option v-for="(template, key) in templates" :key="key" :label="template.label" :value="key" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <el-alert :title="templates[selectedTemplate].description" type="info" show-icon :closable="false" style="margin-bottom: 16px" />
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="规则名称" required><el-input v-model="jail.name" :disabled="editingJail" placeholder="只能使用字母、数字、下划线和短横线" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="检测器名称" required><el-input v-model="jail.filter_name" placeholder="只能使用字母、数字、下划线和短横线" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="检查的日志文件" required><el-input v-model="jail.log_path" /></el-form-item>
        <el-form-item label="失败记录匹配规则" required>
          <el-input v-model="jail.fail_regex" type="textarea" :rows="3" />
          <div class="subtle" style="margin-top: 6px">&lt;HOST&gt; 代表要封禁的来源 IP，必须保留。</div>
        </el-form-item>
        <el-row :gutter="16">
          <el-col :span="8"><el-form-item label="允许失败次数"><el-input-number v-model="jail.max_retry" :min="1" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="统计时间范围（秒）"><el-input-number v-model="jail.find_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="封禁时长（秒）"><el-input-number v-model="jail.ban_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="封禁范围">
          <el-input v-model="jail.ports" placeholder="留空表示禁止该 IP 连接本服务器的所有端口" />
          <div class="subtle" style="margin-top: 6px">多个端口用逗号分隔，如 443,8443。</div>
        </el-form-item>
        <el-alert title="缺少的 Fail2Ban、nftables 与日志文件会自动安装或创建。保存后立即在服务器上生效。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer><el-button @click="jailOpen = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!jail.name || !jail.filter_name || !jail.fail_regex || !jail.log_path" @click="saveJail">保存并生效</el-button></template>
    </el-dialog>
  </div>
</template>
