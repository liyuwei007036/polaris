<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

const appState = inject('appState')
const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const saving = ref(false)
const dialogOpen = ref(false)
const editing = ref(null)
const routes = ref([])
const listeners = ref([])
const form = reactive({ node_id: '', listener_id: '', listen_address: '0.0.0.0', port: 443, sni: '', enabled: true })

const nodeNames = computed(() => Object.fromEntries(appState.nodes.map((item) => [item.id, item.name])))
const listenerMap = computed(() => Object.fromEntries(listeners.value.map((item) => [item.id, item])))
const eligibleListeners = computed(() => listeners.value.filter((listener) => (
  listener.node_id === form.node_id && listener.spec?.network === 'tcp' && listener.spec?.tls?.enabled &&
  ['127.0.0.1', '::1'].includes(listener.listen_address) && listener.backend_port !== listener.port
)))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [routeResult, listenerResult] = await Promise.all([api('/ingress-routes'), api('/listeners')])
    routes.value = routeResult.ingress_routes || []
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}

function openCreate() {
  editing.value = null
  Object.assign(form, { node_id: appState.nodes[0]?.id || '', listener_id: '', listen_address: '0.0.0.0', port: 443, sni: '', enabled: true })
  dialogOpen.value = true
}

function openEdit(route) {
  editing.value = route
  Object.assign(form, route)
  dialogOpen.value = true
}

function selectListener(listenerID) {
  const listener = listenerMap.value[listenerID]
  if (listener) form.port = listener.port
}

async function save() {
  if (!form.node_id || !form.listener_id || !form.sni.trim()) return
  saving.value = true
  try {
    const payload = { ...form, sni: form.sni.trim().toLowerCase(), listen_address: '0.0.0.0', port: Number(form.port) }
    if (editing.value) await put(`/ingress-routes/${editing.value.id}`, payload)
    else await post('/ingress-routes', payload)
    ElMessage.success(editing.value ? '端口共享设置已保存，正在自动应用' : '端口共享设置已创建，正在自动应用')
    dialogOpen.value = false
    await load()
  } finally { saving.value = false }
}

async function remove(route) {
  await ElMessageBox.confirm(`确认删除域名 ${route.sni} 在端口 ${route.port} 上的共享设置？`, '删除端口共享设置', { type: 'warning' })
  await del(`/ingress-routes/${route.id}`)
  ElMessage.success('端口共享设置已删除，正在自动应用')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="端口共享" description="让多个加密接入服务使用不同访问域名，共用同一个公网端口">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建共享设置</el-button>
    </PageHeader>

    <main class="page-content">
      <div class="context-strip">
        <strong>使用说明</strong>
        <span>客户端访问不同域名时，系统会自动把连接转发到对应的接入服务。保存后会自动检查并应用。</span>
      </div>
      <el-alert title="使用前需要在服务器上启用端口共享组件。应用新设置前会自动检查；检查失败时会保留原有配置。" type="info" show-icon :closable="false" style="margin-bottom: 14px" />
      <div class="table-panel">
        <el-table v-loading="loading" :data="routes" row-key="id">
          <el-table-column label="服务器" min-width="150"><template #default="{ row }">{{ nodeNames[row.node_id] || row.node_id }}</template></el-table-column>
          <el-table-column label="公网端口" min-width="150"><template #default="{ row }"><span class="mono">{{ row.listen_address }}:{{ row.port }}</span></template></el-table-column>
          <el-table-column label="客户端访问域名" min-width="220" prop="sni" />
          <el-table-column label="接入服务" min-width="190"><template #default="{ row }">{{ listenerMap[row.listener_id]?.name || row.listener_id }}</template></el-table-column>
          <el-table-column label="内部端口" min-width="160"><template #default="{ row }"><span class="mono">{{ row.backend_address }}:{{ row.backend_port }}</span></template></el-table-column>
          <el-table-column label="状态" width="90"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
          <el-table-column label="操作" width="150" fixed="right"><template #default="{ row }"><el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogOpen" :title="editing ? '编辑端口共享设置' : '新建端口共享设置'" width="min(680px, 94vw)" destroy-on-close>
      <el-form label-position="top">
        <el-form-item label="服务器" required><el-select v-model="form.node_id" :disabled="Boolean(editing)" style="width: 100%" @change="form.listener_id = ''"><el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.id" /></el-select></el-form-item>
        <el-form-item label="接入服务" required>
          <el-select v-model="form.listener_id" :disabled="Boolean(editing)" style="width: 100%" placeholder="请选择已启用端口共享的加密接入服务" @change="selectListener">
            <el-option v-for="listener in eligibleListeners" :key="listener.id" :label="`${listener.name} · 内部端口 ${listener.backend_port}`" :value="listener.id" />
          </el-select>
          <span v-if="!eligibleListeners.length" class="form-hint">请先新建或编辑接入服务，并启用“端口共享”。</span>
        </el-form-item>
        <el-row :gutter="16">
          <el-col :span="10"><el-form-item label="公网端口"><el-input-number v-model="form.port" disabled style="width: 100%" /></el-form-item></el-col>
          <el-col :span="14"><el-form-item label="客户端访问域名" required><el-input v-model="form.sni" placeholder="例如 grpc.example.com" /></el-form-item></el-col>
        </el-row>
        <el-form-item label="状态"><el-switch v-model="form.enabled" active-text="启用" inactive-text="停用" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="dialogOpen = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!form.node_id || !form.listener_id || !form.sni.trim()" @click="save">保存并应用</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.context-strip { display: flex; gap: 12px; align-items: baseline; margin-bottom: 14px; color: var(--sb-muted); font-size: 13px; }
.context-strip strong { flex: 0 0 auto; color: var(--sb-text); }
.form-hint { display: block; margin-top: 6px; color: var(--sb-muted); font-size: 12px; }
@media (max-width: 620px) {
  .context-strip { align-items: flex-start; flex-direction: column; }
  :deep(.el-dialog .el-col) { max-width: 100%; flex: 0 0 100%; }
}
</style>
