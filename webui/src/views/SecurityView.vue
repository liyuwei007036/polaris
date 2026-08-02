<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import { Delete, Plus, Refresh } from '@element-plus/icons-vue'
import { api, del, post } from '../api'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('firewall')
const firewallRules = ref([])
const jails = ref([])
const selectedNode = ref('')
const filteredFirewallRules = computed(() => selectedNode.value
  ? firewallRules.value.filter((row) => row.node_id === selectedNode.value)
  : firewallRules.value)
const filteredJails = computed(() => selectedNode.value
  ? jails.value.filter((row) => row.node_id === selectedNode.value)
  : jails.value)
const firewallOpen = ref(false)
const jailOpen = ref(false)
const firewall = reactive({ node_id: '', action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443, enabled: true })
const jail = reactive({ node_id: '', name: '', log_path: '/var/log/sing-box/access.log', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, enabled: true })

async function load() {
  loading.value = true
  try {
    const results = await Promise.all(appState.nodes.map(async (node) => {
      const [firewallResult, jailResult] = await Promise.all([
        api(`/nodes/${node.id}/firewall/rules`),
        api(`/nodes/${node.id}/fail2ban/jails`),
      ])
      return { node, firewallResult, jailResult }
    }))
    firewallRules.value = results.flatMap(({ node, firewallResult }) => (firewallResult.rules || []).map((row) => ({ ...row, node_name: node.name })))
    jails.value = results.flatMap(({ node, jailResult }) => (jailResult.jails || []).map((row) => ({ ...row, node_name: node.name })))
  } finally { loading.value = false }
}
async function apply(nodeID, kind) { await post(`/nodes/${nodeID}/${kind}/publish`, {}) }
function defaultNode() { return appState.nodes[0]?.id || '' }
function addFirewall() { Object.assign(firewall, { node_id: defaultNode(), action: 'accept', protocol: 'tcp', cidr: '0.0.0.0/0', port: 443, enabled: true }); firewallOpen.value = true }
async function saveFirewall() { await post(`/nodes/${firewall.node_id}/firewall/rules`, { ...firewall, port: Number(firewall.port) }); await apply(firewall.node_id, 'firewall'); firewallOpen.value = false; await load() }
async function toggleFirewall(row) { await post(`/firewall/rules/${row.id}/enabled`, { enabled: !row.enabled }); await apply(row.node_id, 'firewall'); await load() }
async function removeFirewall(row) { await ElMessageBox.confirm('确认删除这条访问限制？', '删除访问限制', { type: 'warning' }); await del(`/firewall/rules/${row.id}`); await apply(row.node_id, 'firewall'); await load() }
function addJail() { Object.assign(jail, { node_id: defaultNode(), name: '', log_path: '/var/log/sing-box/access.log', filter_name: '', fail_regex: '', max_retry: 5, find_time_seconds: 600, ban_time_seconds: 3600, enabled: true }); jailOpen.value = true }
async function saveJail() { await post(`/nodes/${jail.node_id}/fail2ban/jails`, { ...jail }); await apply(jail.node_id, 'fail2ban'); jailOpen.value = false; await load() }
async function toggleJail(row) { await post(`/fail2ban/jails/${row.id}/enabled`, { enabled: !row.enabled }); await apply(row.node_id, 'fail2ban'); await load() }
async function removeJail(row) { await ElMessageBox.confirm(`确认删除自动封禁规则“${row.name}”？`, '删除自动封禁规则', { type: 'warning' }); await del(`/fail2ban/jails/${row.id}`); await apply(row.node_id, 'fail2ban'); await load() }
onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="网络防护" description="统一管理全部服务器的访问限制和异常连接自动封禁规则"><el-button :icon="Refresh" @click="load">刷新</el-button></PageHeader>
    <main class="page-content page-content--tight">
      <p class="subtle">这里显示全部服务器的防护设置，保存后会自动应用到对应服务器。</p>
      <div class="toolbar">
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 220px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
      </div>
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="访问限制" name="firewall">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" @click="addFirewall">添加规则</el-button></div>
            <el-table v-loading="loading" :data="filteredFirewallRules">
              <el-table-column label="服务器" prop="node_name" min-width="150" /><el-table-column label="处理方式" width="100"><template #default="{ row }"><el-tag :type="row.action === 'accept' ? 'success' : 'danger'">{{ row.action === 'accept' ? '允许' : '拒绝' }}</el-tag></template></el-table-column><el-table-column label="连接类型" prop="protocol" width="100" /><el-table-column label="来源地址范围" prop="cidr" min-width="200" /><el-table-column label="端口" prop="port" width="100" /><el-table-column label="状态" width="90"><template #default="{ row }">{{ row.enabled ? '启用' : '停用' }}</template></el-table-column><el-table-column label="操作" width="150"><template #default="{ row }"><el-button v-if="isAdmin" link @click="toggleFirewall(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeFirewall(row)">删除</el-button></template></el-table-column>
            </el-table>
          </el-tab-pane>
          <el-tab-pane label="自动封禁" name="fail2ban">
            <div class="tab-actions"><el-button v-if="isAdmin" type="primary" :icon="Plus" @click="addJail">添加自动封禁规则</el-button></div>
            <el-table v-loading="loading" :data="filteredJails">
              <el-table-column label="服务器" prop="node_name" min-width="150" /><el-table-column label="规则名称" prop="name" min-width="150" /><el-table-column label="检查的日志文件" prop="log_path" min-width="240" /><el-table-column label="允许失败次数" prop="max_retry" width="120" /><el-table-column label="封禁时间" width="120"><template #default="{ row }">{{ row.ban_time_seconds }} 秒</template></el-table-column><el-table-column label="状态" width="90"><template #default="{ row }">{{ row.enabled ? '启用' : '停用' }}</template></el-table-column><el-table-column label="操作" width="150"><template #default="{ row }"><el-button v-if="isAdmin" link @click="toggleJail(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeJail(row)">删除</el-button></template></el-table-column>
            </el-table>
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>
    <el-dialog v-model="firewallOpen" title="添加访问限制" width="580px"><el-form label-position="top"><el-form-item label="服务器"><el-select v-model="firewall.node_id" style="width: 100%"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item><el-row :gutter="16"><el-col :span="8"><el-form-item label="处理方式"><el-select v-model="firewall.action"><el-option label="允许" value="accept" /><el-option label="拒绝" value="drop" /></el-select></el-form-item></el-col><el-col :span="8"><el-form-item label="连接类型"><el-select v-model="firewall.protocol"><el-option label="TCP" value="tcp" /><el-option label="UDP" value="udp" /></el-select></el-form-item></el-col><el-col :span="8"><el-form-item label="端口"><el-input-number v-model="firewall.port" :min="1" :max="65535" style="width: 100%" /></el-form-item></el-col></el-row><el-form-item label="允许或拒绝的来源地址范围"><el-input v-model="firewall.cidr" placeholder="例如 192.168.1.0/24" /></el-form-item></el-form><template #footer><el-button @click="firewallOpen = false">取消</el-button><el-button type="primary" @click="saveFirewall">保存并应用</el-button></template></el-dialog>
    <el-dialog v-model="jailOpen" title="添加自动封禁规则" width="700px"><el-form label-position="top"><el-form-item label="服务器"><el-select v-model="jail.node_id" style="width: 100%"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item><el-row :gutter="16"><el-col :span="12"><el-form-item label="规则名称"><el-input v-model="jail.name" /></el-form-item></el-col><el-col :span="12"><el-form-item label="检测器名称"><el-input v-model="jail.filter_name" /></el-form-item></el-col></el-row><el-form-item label="检查的日志文件"><el-input v-model="jail.log_path" /></el-form-item><el-form-item label="失败记录匹配规则"><el-input v-model="jail.fail_regex" type="textarea" :rows="3" /></el-form-item><el-row :gutter="16"><el-col :span="8"><el-form-item label="允许失败次数"><el-input-number v-model="jail.max_retry" :min="1" style="width: 100%" /></el-form-item></el-col><el-col :span="8"><el-form-item label="统计时间范围（秒）"><el-input-number v-model="jail.find_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col><el-col :span="8"><el-form-item label="封禁时长（秒）"><el-input-number v-model="jail.ban_time_seconds" :min="1" style="width: 100%" /></el-form-item></el-col></el-row></el-form><template #footer><el-button @click="jailOpen = false">取消</el-button><el-button type="primary" :disabled="!jail.name || !jail.fail_regex" @click="saveJail">保存并应用</el-button></template></el-dialog>
  </div>
</template>
