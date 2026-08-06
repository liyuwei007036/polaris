<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const publishing = ref(false)
const tab = ref('firewall')
const firewallRules = ref([])
const jails = ref([])
const jailStatus = ref({})
const selectedNode = ref('')
const selectedStatus = ref('')
const keyword = ref('')

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
const filteredFirewallRules = computed(() => firewallRules.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedStatus.value && String(row.enabled) !== selectedStatus.value) return false
  return includesText([nodeNames.value[row.node_id], row.action, row.protocol, row.cidr, row.location, row.port], keyword.value)
}))
const filteredJails = computed(() => jails.value.filter((row) => {
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  if (selectedStatus.value && String(row.enabled) !== selectedStatus.value) return false
  return includesText([nodeNames.value[row.node_id], row.name, row.log_path, row.filter_name], keyword.value)
}))

const firewallOpen = ref(false)
const jailOpen = ref(false)
const firewall = reactive({ node_id: '', action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443, enabled: true })
const jail = reactive({ node_id: '', name: '', log_path: '', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, ports: '', enabled: true })
const selectedTemplate = ref('sshd')

// sing-box writes this log because the compiled configuration points it here;
// a jail watching any other path will never match anything.
const singBoxLogPath = '/var/log/sing-box/sing-box.log'

// Automatic banning is a system-level control: a matching IP is blocked by
// the server's firewall and cannot connect at all. The presets cover the
// usual sources of attack traffic so nobody has to hand-write a regex.
const templates = {
  sshd: {
    label: 'SSH 暴力破解（推荐）',
    description: '反复尝试登录服务器 SSH 的来源 IP 会被系统防火墙封禁，无法再连接本服务器的任何端口。',
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
    description: '使用错误的密码或 UUID 反复尝试连接代理服务的来源 IP 会被系统防火墙封禁。',
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
    description: '短时间内产生大量失败连接的来源 IP 会被系统防火墙封禁，用于抵御扫描和探测。',
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

async function load() {
  loading.value = true
  try {
    const [firewallResult, jailResult, metricResult] = await Promise.all([
      api('/firewall/rules').catch(() => ({ rules: [] })),
      api('/fail2ban/jails').catch(() => ({ jails: [] })),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
    ])
    firewallRules.value = firewallResult.rules || []
    jails.value = jailResult.jails || []
    // Agents report every jail a host runs, including ones the operator set
    // up outside this console. Showing them prevents configuring a duplicate
    // rule for protection that is already in place.
    jailStatus.value = Object.fromEntries((metricResult.nodes || []).map(
      (entry) => [entry.node_id, entry.report?.fail2ban || null],
    ))
  } finally { loading.value = false }
}

function unmanagedJails(nodeID) {
  return (jailStatus.value[nodeID]?.jails || []).filter((jail) => !jail.managed)
}

function jailRuntime(row) {
  const jail = (jailStatus.value[row.node_id]?.jails || []).find((item) => item.name === 'sb-control-' + row.name)
  if (!jail) return { text: '等待服务器上报', type: 'info' }
  if (jail.error) return { text: '未生效：' + jail.error, type: 'danger' }
  return { text: `运行中 · 当前封禁 ${jail.currently_banned || 0} · 累计 ${jail.total_banned || 0}`, type: 'success' }
}

// Publishing is what actually changes the server, so a failure here must be
// visible rather than swallowed: previously a rejected apply looked identical
// to a successful one.
async function apply(nodeID, kind, successMessage) {
  publishing.value = true
  try {
    await post(`/nodes/${nodeID}/${kind}/publish`, {})
    ElMessage.success(successMessage)
    return true
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '下发失败，请在“操作记录”中查看详情')
    return false
  } finally {
    publishing.value = false
  }
}

function defaultNode() { return appState.nodes[0]?.id || '' }

function addFirewall() {
  Object.assign(firewall, { node_id: defaultNode(), action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443, enabled: true })
  firewallOpen.value = true
}

async function saveFirewall() {
  try {
    await post(`/nodes/${firewall.node_id}/firewall/rules`, { ...firewall, port: Number(firewall.port) })
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '访问限制保存失败')
    return
  }
  firewallOpen.value = false
  await apply(firewall.node_id, 'firewall', '访问限制已下发')
  await load()
}

async function toggleFirewall(row) {
  await post(`/firewall/rules/${row.id}/enabled`, { enabled: !row.enabled })
  await apply(row.node_id, 'firewall', '访问限制已下发')
  await load()
}

async function removeFirewall(row) {
  await ElMessageBox.confirm('确认删除这条访问限制？', '删除访问限制', { type: 'warning' })
  await del(`/firewall/rules/${row.id}`)
  await apply(row.node_id, 'firewall', '访问限制已下发')
  await load()
}

function addJail() {
  Object.assign(jail, { node_id: defaultNode(), name: '', log_path: '', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, ports: '', enabled: true })
  selectedTemplate.value = 'sshd'
  applyTemplate('sshd')
  jailOpen.value = true
}

async function saveJail() {
  try {
    await post(`/nodes/${jail.node_id}/fail2ban/jails`, { ...jail })
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '自动封禁规则保存失败')
    return
  }
  jailOpen.value = false
  await apply(jail.node_id, 'fail2ban', '自动封禁规则已下发，服务器会自动安装并启动 Fail2Ban')
  await load()
}

async function toggleJail(row) {
  await post(`/fail2ban/jails/${row.id}/enabled`, { enabled: !row.enabled })
  await apply(row.node_id, 'fail2ban', '自动封禁规则已下发')
  await load()
}

async function removeJail(row) {
  await ElMessageBox.confirm(`确认删除自动封禁规则“${row.name}”？`, '删除自动封禁规则', { type: 'warning' })
  await del(`/fail2ban/jails/${row.id}`)
  await apply(row.node_id, 'fail2ban', '自动封禁规则已下发')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="网络防护"><el-button :icon="Refresh" :loading="publishing" @click="load">刷新</el-button></PageHeader>
    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索服务器、地址或规则" style="width: 270px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 220px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
        <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select>
      </div>
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="访问限制" name="firewall">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" :disabled="!appState.nodes.length" @click="addFirewall">添加</el-button></div>
            <PagedTable :rows="filteredFirewallRules" :loading="loading" empty-text="还没有访问限制">
              <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
              <el-table-column label="处理方式" width="100"><template #default="{ row }"><el-tag :type="row.action === 'accept' ? 'success' : 'danger'">{{ row.action === 'accept' ? '允许' : '拒绝' }}</el-tag></template></el-table-column>
              <el-table-column label="协议" prop="protocol" width="100" />
              <el-table-column label="来源地址范围 / IP 归属地" min-width="240"><template #default="{ row }"><div class="mono">{{ row.cidr || '所有来源' }}</div><div class="subtle">{{ row.location || '未知' }}</div></template></el-table-column>
              <el-table-column label="端口" prop="port" width="100" />
              <el-table-column label="状态" width="90"><template #default="{ row }">{{ row.enabled ? '启用' : '停用' }}</template></el-table-column>
              <el-table-column label="操作" width="140" class-name="action-column"><template #default="{ row }"><el-button v-if="isAdmin" link @click="toggleFirewall(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeFirewall(row)">删除</el-button></template></el-table-column>
            </PagedTable>
          </el-tab-pane>
          <el-tab-pane label="自动封禁" name="fail2ban">
            <div v-for="node in appState.nodes" :key="node.id">
              <el-alert
                v-if="unmanagedJails(node.id).length"
                :title="`${node.name} 上已有 ${unmanagedJails(node.id).length} 条本平台之外的封禁规则：${unmanagedJails(node.id).map((jail) => jail.name).join('、')}。这些规则不会被本平台修改或删除。`"
                type="info" show-icon :closable="false" style="margin: 12px 16px 0" />
            </div>
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" :disabled="!appState.nodes.length" @click="addJail">添加</el-button></div>
            <PagedTable :rows="filteredJails" :loading="loading" empty-text="还没有自动封禁规则">
              <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
              <el-table-column label="规则名称" prop="name" min-width="150" />
              <el-table-column label="检查的日志文件" prop="log_path" min-width="220" />
              <el-table-column label="允许失败次数" prop="max_retry" width="120" />
              <el-table-column label="封禁范围" width="140"><template #default="{ row }">{{ row.ports || '全部端口' }}</template></el-table-column>
              <el-table-column label="封禁时间" width="120"><template #default="{ row }">{{ row.ban_time_seconds }} 秒</template></el-table-column>
              <el-table-column label="状态" width="90"><template #default="{ row }">{{ row.enabled ? '启用' : '停用' }}</template></el-table-column>
              <el-table-column label="服务器上的运行情况" min-width="230">
                <template #default="{ row }"><el-tag :type="jailRuntime(row).type" effect="plain">{{ jailRuntime(row).text }}</el-tag></template>
              </el-table-column>
              <el-table-column label="操作" width="140" class-name="action-column"><template #default="{ row }"><el-button v-if="isAdmin" link @click="toggleJail(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeJail(row)">删除</el-button></template></el-table-column>
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
        <el-form-item label="允许或拒绝的来源地址范围"><el-input v-model="firewall.cidr" placeholder="例如 192.168.1.0/24" /></el-form-item>
        <el-alert
          :title="firewall.action === 'accept'
            ? `添加“允许”规则后，${firewall.protocol.toUpperCase()} ${firewall.port} 端口将只接受这里列出的来源，其余来源一律拒绝。`
            : `“拒绝”规则只挡住这里列出的来源，${firewall.protocol.toUpperCase()} ${firewall.port} 端口对其他来源仍然开放。`"
          type="info" show-icon :closable="false" />
      </el-form>
      <template #footer><el-button @click="firewallOpen = false">取消</el-button><el-button type="primary" :loading="publishing" @click="saveFirewall">保存</el-button></template>
    </el-dialog>

    <el-dialog v-model="jailOpen" title="添加自动封禁规则" width="720px">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="服务器"><el-select v-model="jail.node_id" style="width: 100%"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item></el-col>
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
          <el-col :span="12"><el-form-item label="规则名称" required><el-input v-model="jail.name" placeholder="只能使用字母、数字、下划线和短横线" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="检测器名称" required><el-input v-model="jail.filter_name" placeholder="只能使用字母、数字、下划线和短横线" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="检查的日志文件" required><el-input v-model="jail.log_path" /></el-form-item>
        <el-form-item label="失败记录匹配规则" required>
          <el-input v-model="jail.fail_regex" type="textarea" :rows="3" />
          <div class="subtle" style="margin-top: 6px">其中 &lt;HOST&gt; 代表要封禁的来源 IP，必须保留。</div>
        </el-form-item>
        <el-row :gutter="16">
          <el-col :span="8"><el-form-item label="允许失败次数"><el-input-number v-model="jail.max_retry" :min="1" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="统计时间范围（秒）"><el-input-number v-model="jail.find_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="8"><el-form-item label="封禁时长（秒）"><el-input-number v-model="jail.ban_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="封禁范围">
          <el-input v-model="jail.ports" placeholder="留空表示禁止该 IP 连接本服务器的所有端口" />
          <div class="subtle" style="margin-top: 6px">如只想封禁特定端口，可填写如 22 或 443,8443，多个端口用逗号分隔。</div>
        </el-form-item>
        <el-alert title="触发条件后，服务器防火墙会直接拒绝该 IP 的连接。未安装 Fail2Ban 或 nftables 的服务器会自动安装，缺少的日志文件也会自动创建。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer><el-button @click="jailOpen = false">取消</el-button><el-button type="primary" :loading="publishing" :disabled="!jail.name || !jail.filter_name || !jail.fail_regex || !jail.log_path" @click="saveJail">保存</el-button></template>
    </el-dialog>
  </div>
</template>
