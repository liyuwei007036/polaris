<script setup>
import { inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Plus, Promotion, Refresh, Setting } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

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

async function load() {
  loading.value = true
  try {
    const [settingsResult, recordResult, listenerResult] = await Promise.all([api('/cloudflare/settings'), api('/cloudflare/records'), api('/listeners')])
    settings.value = settingsResult
    records.value = recordResult.records || []
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}
function openSettings() { Object.assign(config, { zone_id: settings.value.zone_id || '', zone_name: settings.value.zone_name || '', api_token: '' }); settingsOpen.value = true }
async function saveSettings() { await put('/cloudflare/settings', config); settingsOpen.value = false; ElMessage.success('域名服务连接设置已保存'); await load() }
function addRecord() { Object.assign(record, { type: 'A', name: '', content: '', ttl: 1, proxied: false, node_id: '', listener_id: '' }); recordOpen.value = true }
async function saveRecord() { await post('/cloudflare/records', record); recordOpen.value = false; await load() }
async function sync() { const result = await post('/cloudflare/sync', {}); ElMessage.success(`检查完成，发现 ${result.drifted || 0} 条与 Cloudflare 不一致的记录`); await load() }
async function publish(row) { await ElMessageBox.confirm(`将 ${row.type} 记录 ${row.name} 发布到 Cloudflare？`, '发布域名记录'); await post(`/cloudflare/records/${row.id}/publish`, { confirm: true }); await load() }
async function remove(row) { await ElMessageBox.confirm(`删除 ${row.type} 记录 ${row.name}？如果该记录已发布，也会同时从 Cloudflare 删除。`, '删除域名记录', { type: 'warning' }); await del(`/cloudflare/records/${row.id}?confirm=true`); await load() }
function checkResult(row) {
  if (row.last_error) return '检查失败，请确认 Cloudflare 连接设置和记录内容'
  if (row.status === 'synced') return '线上记录与当前设置一致'
  if (row.status === 'drift') return '线上记录与当前设置不一致'
  return row.observed ? '已读取线上记录' : '尚未检查'
}
onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="域名解析" description="管理 Cloudflare 域名记录，并检查平台设置是否与线上记录一致">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" :icon="Refresh" :disabled="!settings.configured" @click="sync">检查线上记录</el-button>
      <el-button v-if="isAdmin" :icon="Setting" @click="openSettings">连接设置</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="addRecord">新建记录</el-button>
    </PageHeader>
    <main class="page-content">
      <el-alert :title="settings.configured ? `已连接域名：${settings.zone_name || settings.zone_id}` : '尚未连接 Cloudflare 域名服务'" :type="settings.configured ? 'success' : 'warning'" show-icon :closable="false" style="margin-bottom: 16px" />
      <div class="table-panel"><el-table v-loading="loading" :data="records"><el-table-column label="记录类型" prop="type" width="90" /><el-table-column label="域名" prop="name" min-width="220" /><el-table-column label="指向地址或内容" prop="content" min-width="220" /><el-table-column label="Cloudflare 加速" width="130"><template #default="{ row }">{{ row.proxied ? '已启用' : '未启用' }}</template></el-table-column><el-table-column label="同步状态" width="110"><template #default="{ row }"><el-tag :type="row.status === 'synced' ? 'success' : row.status === 'drift' ? 'warning' : 'info'">{{ row.status === 'synced' ? '已同步' : row.status === 'drift' ? '存在差异' : '未发布' }}</el-tag></template></el-table-column><el-table-column label="检查结果" min-width="220"><template #default="{ row }">{{ checkResult(row) }}</template></el-table-column><el-table-column label="操作" width="180" fixed="right"><template #default="{ row }"><el-button v-if="isAdmin" link type="primary" :icon="Promotion" @click="publish(row)">发布</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column></el-table></div>
    </main>
    <el-dialog v-model="settingsOpen" title="连接 Cloudflare" width="560px"><el-form label-position="top"><el-form-item label="区域编号（Zone ID）"><el-input v-model="config.zone_id" /></el-form-item><el-form-item label="域名"><el-input v-model="config.zone_name" placeholder="example.com" /></el-form-item><el-form-item label="访问令牌（API Token）"><el-input v-model="config.api_token" type="password" show-password placeholder="请输入有效令牌；留空不会保留原令牌" /></el-form-item></el-form><template #footer><el-button @click="settingsOpen = false">取消</el-button><el-button type="primary" :disabled="!config.zone_id || !config.api_token" @click="saveSettings">保存连接</el-button></template></el-dialog>
    <el-dialog v-model="recordOpen" title="新建域名记录" width="580px"><el-form label-position="top"><el-row :gutter="16"><el-col :span="8"><el-form-item label="记录类型"><el-select v-model="record.type"><el-option v-for="type in ['A','AAAA','CNAME','TXT']" :key="type" :label="type" :value="type" /></el-select></el-form-item></el-col><el-col :span="16"><el-form-item label="域名"><el-input v-model="record.name" placeholder="proxy.example.com" /></el-form-item></el-col></el-row><el-form-item label="指向地址或内容"><el-input v-model="record.content" /></el-form-item><el-row :gutter="16"><el-col :span="12"><el-form-item label="缓存时间（TTL）"><el-input-number v-model="record.ttl" :min="1" style="width: 100%" /></el-form-item></el-col><el-col :span="12"><el-form-item label="Cloudflare 加速"><el-switch v-model="record.proxied" active-text="启用" inactive-text="关闭" /></el-form-item></el-col></el-row><el-form-item v-if="record.proxied" label="对应的接入服务" required><el-select v-model="record.listener_id" style="width: 100%" placeholder="请选择使用证书保护的 TCP 接入服务" @change="record.node_id = listeners.find((item) => item.id === record.listener_id)?.node_id || ''"><el-option v-for="listener in listeners" :key="listener.id" :label="`${appState.nodes.find((node) => node.id === listener.node_id)?.name || '服务器'} · ${listener.name}`" :value="listener.id" /></el-select></el-form-item></el-form><template #footer><el-button @click="recordOpen = false">取消</el-button><el-button type="primary" :disabled="!record.name || !record.content || (record.proxied && !record.listener_id)" @click="saveRecord">保存记录</el-button></template></el-dialog>
  </div>
</template>
