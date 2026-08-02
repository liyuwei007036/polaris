<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Delete, Download, Edit, Plus, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'
import { protocolMap } from '../protocols'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const editing = ref(null)
const configs = ref([])
const listeners = ref([])
const endpoints = ref([])
const form = reactive({ name: '', endpoint_ids: [], strategy: 'select', rule_preset: 'china-direct', default_action: 'PROXY', raw_rules: '' })
const supported = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'socks', 'http'])
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }
const presetNames = { 'china-direct': '国内直连，其余代理', 'proxy-all': '全部代理', 'direct-all': '全部直连', custom: '自定义规则' }

const clientNodes = computed(() => listeners.value
  .filter((listener) => listener.enabled && supported.has(listener.spec?.protocol))
  .flatMap((listener) => endpoints.value
    .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
    .map((endpoint) => {
      const node = appState.nodes.find((item) => item.id === listener.node_id)
      return {
        id: endpoint.id,
        label: endpoint.alias || `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`,
        detail: `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`,
        protocol: protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol,
        address: node?.client_address ? `${node.client_address}:${listener.port}` : '未填写连接地址',
        disabled: !node?.client_address,
      }
    })))
const clientNodeNames = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item.label])))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [configResult, listenerResult] = await Promise.all([api('/mihomo/client-configs'), api('/listeners')])
    configs.value = configResult.client_configs || []
    listeners.value = listenerResult.listeners || []
    const results = await Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = results.flatMap((result) => result.endpoints || [])
  } finally {
    loading.value = false
  }
}

function resetForm(config = null) {
  editing.value = config
  Object.assign(form, config ? {
    name: config.name,
    endpoint_ids: [...config.endpoint_ids],
    strategy: config.strategy,
    rule_preset: config.rule_preset,
    default_action: config.default_action,
    raw_rules: config.raw_rules || '',
  } : { name: '', endpoint_ids: [], strategy: 'select', rule_preset: 'china-direct', default_action: 'PROXY', raw_rules: '' })
  dialogVisible.value = true
}

async function save(downloadAfterCreate = false) {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(), endpoint_ids: form.endpoint_ids, strategy: form.strategy,
      rule_preset: form.rule_preset, default_action: form.default_action, raw_rules: form.rule_preset === 'custom' ? form.raw_rules : '',
    }
    let saved
    if (editing.value) saved = await put(`/mihomo/client-configs/${editing.value.id}`, payload)
    else saved = await post('/mihomo/client-configs', payload)
    ElMessage.success(editing.value ? '客户端配置已保存' : '客户端配置已创建')
    dialogVisible.value = false
    await load()
    if (downloadAfterCreate) triggerDownload(saved)
  } finally {
    saving.value = false
  }
}

function absoluteSubscription(config) {
  return new URL(config.subscription_path, window.location.origin).toString()
}

function triggerDownload(config) {
  const anchor = document.createElement('a')
  anchor.href = absoluteSubscription(config)
  anchor.download = `${config.name}.yaml`
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}

async function copySubscription(config) {
  await navigator.clipboard.writeText(absoluteSubscription(config))
  ElMessage.success('更新地址已复制')
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

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="客户端配置" description="直接选择接入用户和访问策略，生成可持续更新的 Mihomo 配置">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="resetForm()">新建客户端配置</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <el-alert title="客户端节点名称来自接入服务中的“客户端节点别名”。配置保存时会校验用户可用性、服务器连接地址和别名唯一性。" type="info" show-icon :closable="false" style="margin-bottom: 16px" />
      <div class="table-panel">
        <el-table :data="configs">
          <el-table-column label="配置名称" min-width="180" prop="name" />
          <el-table-column label="客户端节点" min-width="300">
            <template #default="{ row }"><el-tag v-for="id in row.endpoint_ids" :key="id" type="info" class="node-tag">{{ clientNodeNames[id] || '节点已失效' }}</el-tag></template>
          </el-table-column>
          <el-table-column label="节点策略" width="130"><template #default="{ row }">{{ strategyNames[row.strategy] }}</template></el-table-column>
          <el-table-column label="访问规则" min-width="170"><template #default="{ row }">{{ presetNames[row.rule_preset] }}</template></el-table-column>
          <el-table-column label="操作" width="390" fixed="right">
            <template #default="{ row }">
              <el-button type="primary" link :icon="Download" @click="triggerDownload(row)">下载</el-button>
              <el-button link :icon="CopyDocument" @click="copySubscription(row)">复制更新地址</el-button>
              <el-button v-if="canWrite" link :icon="RefreshRight" @click="rotateSubscription(row)">更换地址</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="resetForm(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑客户端配置' : '新建客户端配置'" width="min(760px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="配置名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="接入用户与客户端节点" required>
          <el-select v-model="form.endpoint_ids" multiple filterable style="width: 100%" placeholder="请选择一个或多个接入用户">
            <el-option v-for="node in clientNodes" :key="node.id" :value="node.id" :label="node.label" :disabled="node.disabled">
              <span>{{ node.label }}</span><span class="option-meta">{{ node.detail }} · {{ node.protocol }} · {{ node.address }}</span>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item label="多个节点的使用方式" required>
          <el-radio-group v-model="form.strategy"><el-radio value="select">手动选择</el-radio><el-radio value="url-test">自动测速</el-radio><el-radio value="fallback">故障切换</el-radio></el-radio-group>
        </el-form-item>
        <el-form-item label="访问规则" required>
          <el-select v-model="form.rule_preset" style="width: 100%"><el-option v-for="(label, value) in presetNames" :key="value" :value="value" :label="label" /></el-select>
        </el-form-item>
        <template v-if="form.rule_preset === 'custom'">
          <el-form-item label="自定义规则">
            <el-input v-model="form.raw_rules" type="textarea" :rows="7" placeholder="DOMAIN-SUFFIX,example.com,PROXY&#10;IP-CIDR,10.0.0.0/8,DIRECT,no-resolve" />
            <div class="form-hint">按顺序每行一条；不要填写 MATCH，系统会根据下方默认动作生成且只生成一条终结规则。</div>
          </el-form-item>
          <el-form-item label="没有命中规则时" required><el-radio-group v-model="form.default_action"><el-radio value="PROXY">使用代理</el-radio><el-radio value="DIRECT">直接访问</el-radio></el-radio-group></el-form-item>
        </template>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!form.name.trim() || !form.endpoint_ids.length" @click="save(!editing)">{{ editing ? '保存' : '创建并下载' }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.node-tag { margin: 3px 6px 3px 0; }
.option-meta { float: right; margin-left: 18px; color: var(--sb-muted); font-size: 12px; }
.form-hint { margin-top: 6px; color: var(--sb-muted); font-size: 12px; }
</style>
