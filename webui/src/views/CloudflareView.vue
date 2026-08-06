<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Promotion, Refresh, Search, Setting } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loading = ref(false)
const settings = ref({})
const records = ref([])
const listeners = ref([])
const settingsOpen = ref(false)
const recordOpen = ref(false)
const config = reactive({ zone_id: '', zone_name: '', api_token: '' })
const record = reactive({ type: 'A', name: '', content: '', ttl: 1, proxied: false, node_id: '', listener_id: '' })
const keyword = ref('')
const selectedType = ref('')
const selectedStatus = ref('')
const selectedNode = ref('')

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((node) => [node.id, node.name])))
// Cloudflare's standard proxy only fronts VLESS over WebSocket or gRPC on a
// fixed port set, so only those listeners can back an orange-cloud record.
const proxyListeners = computed(() => listeners.value.filter((listener) => {
  const transport = listener.spec?.transport?.type
  if (listener.spec?.protocol !== 'vless' || !listener.spec?.tls?.enabled) return false
  if (transport === 'grpc') return listener.port === 443
  return transport === 'ws' && [443, 2053, 2083, 2087, 2096, 8443].includes(listener.port)
}))
const nodeListeners = computed(() => listeners.value.filter((listener) => !record.node_id || listener.node_id === record.node_id))
const filteredRecords = computed(() => records.value.filter((row) => {
  if (selectedType.value && row.type !== selectedType.value) return false
  if (selectedStatus.value && row.status !== selectedStatus.value) return false
  if (selectedNode.value && row.node_id !== selectedNode.value) return false
  return includesText([row.name, row.content, row.type, row.node_name, row.listener_name, checkResult(row)], keyword.value)
}))

async function load() {
  loading.value = true
  try {
    const [settingsResult, recordResult, listenerResult] = await Promise.all([
      api('/cloudflare/settings'),
      api('/cloudflare/records'),
      api('/listeners'),
    ])
    const remoteResult = settingsResult.configured
      ? await api('/cloudflare/remote-records').catch(() => ({ records: [] }))
      : { records: [] }
    settings.value = settingsResult
    records.value = [
      ...(recordResult.records || []).map((row) => ({ ...row, managed: true })),
      ...(remoteResult.records || []).map((row) => ({ ...row, managed: false, status: 'external' })),
    ]
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}

function openSettings() { Object.assign(config, { zone_id: settings.value.zone_id || '', zone_name: settings.value.zone_name || '', api_token: '' }); settingsOpen.value = true }
async function saveSettings() { await put('/cloudflare/settings', config); settingsOpen.value = false; ElMessage.success('域名服务连接设置已保存'); await load() }
function addRecord() { Object.assign(record, { type: 'A', name: '', content: '', ttl: 1, proxied: false, node_id: '', listener_id: '' }); recordOpen.value = true }

// Binding a record to a server is the whole point of managing DNS here: the
// record should point at that server, so its client address is filled in.
function onNodeChange() {
  record.listener_id = ''
  const node = appState.nodes.find((item) => item.id === record.node_id)
  if (node?.client_address) record.content = node.client_address
}

function onListenerChange() {
  const listener = listeners.value.find((item) => item.id === record.listener_id)
  if (!listener) return
  record.node_id = listener.node_id
  const node = appState.nodes.find((item) => item.id === listener.node_id)
  if (node?.client_address && !record.content) record.content = node.client_address
}

async function saveRecord() {
  try {
    await post('/cloudflare/records', record)
    recordOpen.value = false
    ElMessage.success('域名记录已保存，请点击“发布”写入 Cloudflare')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '域名记录保存失败')
  }
}

async function sync() {
  try {
    const result = await post('/cloudflare/sync', {})
    ElMessage.success(`检查完成，发现 ${result.drifted || 0} 条与 Cloudflare 不一致的记录`)
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '检查失败，请确认 Cloudflare 连接设置')
  }
}

async function publish(row) {
  await ElMessageBox.confirm(`将 ${row.type} 记录 ${row.name} 发布到 Cloudflare？`, '发布域名记录')
  try {
    await post(`/cloudflare/records/${row.id}/publish`, { confirm: true })
    ElMessage.success('域名记录已发布')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '发布失败')
  }
}

async function remove(row) {
  await ElMessageBox.confirm(`删除 ${row.type} 记录 ${row.name}？如果该记录已发布，也会同时从 Cloudflare 删除。`, '删除域名记录', { type: 'warning' })
  await del(`/cloudflare/records/${row.id}?confirm=true`)
  await load()
}

function checkResult(row) {
  if (row.last_error) return '检查失败，请确认 Cloudflare 连接设置和记录内容'
  if (row.status === 'synced') return '线上记录与当前设置一致'
  if (row.status === 'drift') return '线上记录与当前设置不一致'
  if (row.status === 'missing') return '线上记录已被删除，需要重新发布'
  if (row.status === 'external') return 'Cloudflare 线上已有，当前未由本平台管理'
  return row.observed ? '已读取线上记录' : '尚未发布'
}

function boundTo(row) {
  if (!row.managed) return '未关联'
  const node = row.node_name || nodeNames.value[row.node_id]
  if (!node) return '未关联'
  return row.listener_name ? `${node} · ${row.listener_name}` : node
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="域名解析">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" :icon="Refresh" :disabled="!settings.configured" @click="sync">检查</el-button>
      <el-button v-if="isAdmin" :icon="Setting" @click="openSettings">设置</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" :disabled="!settings.configured" @click="addRecord">新建</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <el-alert
        :title="settings.configured ? `已连接域名：${settings.zone_name || settings.zone_id}` : '尚未连接 Cloudflare 域名服务，请先在“设置”中填写区域编号和访问令牌'"
        :type="settings.configured ? 'success' : 'warning'" show-icon :closable="false" style="margin-bottom: 16px" />
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索域名、内容或服务器" style="width: 260px" />
        <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 180px">
          <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" />
        </el-select>
        <el-select v-model="selectedType" clearable placeholder="全部类型" style="width: 130px"><el-option v-for="type in ['A','AAAA','CNAME','TXT']" :key="type" :label="type" :value="type" /></el-select>
        <el-select v-model="selectedStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="线上已有" value="external" /><el-option label="已同步" value="synced" /><el-option label="存在差异" value="drift" /><el-option label="未发布" value="pending" /></el-select>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filteredRecords" :loading="loading" empty-text="还没有域名记录">
          <el-table-column label="记录类型" prop="type" width="90" />
          <el-table-column label="域名" prop="name" min-width="200" />
          <el-table-column label="指向地址或内容" prop="content" min-width="190" />
          <el-table-column label="关联服务器 / 接入服务" min-width="200">
            <template #default="{ row }">
              <span :class="{ subtle: !row.managed || !row.node_id }">{{ boundTo(row) }}</span>
              <div v-if="row.listener_port" class="subtle">端口 {{ row.listener_port }}</div>
            </template>
          </el-table-column>
          <el-table-column label="Cloudflare 加速" width="120"><template #default="{ row }">{{ row.proxied ? '已启用' : '未启用' }}</template></el-table-column>
          <el-table-column label="同步状态" width="110">
            <template #default="{ row }">
              <el-tag :type="row.status === 'synced' ? 'success' : row.status === 'drift' || row.status === 'missing' ? 'warning' : 'info'">
                {{ row.status === 'external' ? '线上已有' : row.status === 'synced' ? '已同步' : row.status === 'drift' ? '存在差异' : row.status === 'missing' ? '线上缺失' : '未发布' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="检查结果" min-width="200"><template #default="{ row }">{{ checkResult(row) }}</template></el-table-column>
          <el-table-column label="操作" width="150" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="isAdmin && row.managed" link type="primary" :icon="Promotion" @click="publish(row)">发布</el-button>
              <el-button v-if="isAdmin && row.managed" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="settingsOpen" title="连接 Cloudflare" width="560px">
      <el-form label-position="top">
        <el-form-item label="区域编号（Zone ID）"><el-input v-model="config.zone_id" /></el-form-item>
        <el-form-item label="域名"><el-input v-model="config.zone_name" placeholder="example.com" /></el-form-item>
        <el-form-item label="访问令牌（API Token）"><el-input v-model="config.api_token" type="password" show-password placeholder="请输入有效令牌；留空不会保留原令牌" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="settingsOpen = false">取消</el-button>
        <el-button type="primary" :disabled="!config.zone_id || !config.api_token" @click="saveSettings">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="recordOpen" title="新建域名记录" width="620px">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="8"><el-form-item label="记录类型"><el-select v-model="record.type"><el-option v-for="type in ['A','AAAA','CNAME','TXT']" :key="type" :label="type" :value="type" /></el-select></el-form-item></el-col>
          <el-col :span="16"><el-form-item label="域名"><el-input v-model="record.name" :placeholder="`proxy.${settings.zone_name || 'example.com'}`" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="关联的服务器">
          <el-select v-model="record.node_id" clearable style="width: 100%" placeholder="选择后自动填入该服务器的客户端连接地址" @change="onNodeChange">
            <el-option v-for="node in appState.nodes" :key="node.id" :label="node.client_address ? `${node.name}（${node.client_address}）` : `${node.name}（未配置连接地址）`" :value="node.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="指向地址或内容" required><el-input v-model="record.content" /></el-form-item>
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="缓存时间（TTL）"><el-input-number v-model="record.ttl" :min="1" :disabled="record.proxied" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="Cloudflare 加速"><el-switch v-model="record.proxied" active-text="启用" inactive-text="关闭" /></el-form-item></el-col>
        </el-row>
        <el-form-item :label="record.proxied ? '对应的接入服务（加速必填）' : '对应的接入服务（可选）'" :required="record.proxied">
          <el-select v-model="record.listener_id" clearable style="width: 100%" :placeholder="record.proxied ? '请选择启用 TLS 的 WebSocket 或 gRPC 接入服务' : '关联后可在列表中看到该记录服务于哪个接入服务'" @change="onListenerChange">
            <el-option
              v-for="listener in (record.proxied ? proxyListeners : nodeListeners)"
              :key="listener.id"
              :label="`${nodeNames[listener.node_id] || '服务器'} · ${listener.name} · ${listener.port}`"
              :value="listener.id" />
          </el-select>
        </el-form-item>
        <el-alert v-if="record.proxied" title="启用 Cloudflare 加速时，只有 VLESS + TLS 的 WebSocket 或 gRPC 接入服务，且端口在 Cloudflare 支持范围内才能正常工作。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="recordOpen = false">取消</el-button>
        <el-button type="primary" :disabled="!record.name || !record.content || (record.proxied && !record.listener_id)" @click="saveRecord">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
