<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh } from '@element-plus/icons-vue'
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
const groups = ref([])
const listeners = ref([])
const endpoints = ref([])
const form = reactive({ name: '', strategy: 'select', endpoint_ids: [], aliases: {} })
const supported = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'socks', 'http'])

const clientNodes = computed(() => listeners.value
  .filter((listener) => listener.enabled && supported.has(listener.spec?.protocol))
  .flatMap((listener) => endpoints.value
    .filter((endpoint) => endpoint.listener_id === listener.id && endpoint.enabled)
    .map((endpoint) => {
      const node = appState.nodes.find((item) => item.id === listener.node_id)
      return {
        id: endpoint.id,
        label: `${node?.name || listener.node_id} · ${listener.name} · ${endpoint.name}`,
        protocol: protocolMap[listener.spec?.protocol]?.label || listener.spec?.protocol,
        address: node?.client_address ? `${node.client_address}:${listener.port}` : '未填写连接地址',
      }
    })))

const nodeNames = computed(() => Object.fromEntries(clientNodes.value.map((node) => [node.id, node.label])))
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [groupResult, listenerResult] = await Promise.all([
      api('/mihomo/proxy-groups'),
      api('/listeners'),
    ])
    groups.value = groupResult.proxy_groups || []
    listeners.value = listenerResult.listeners || []
    const results = await Promise.all(listeners.value.map((listener) =>
      api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = results.flatMap((result) => result.endpoints || [])
  } finally {
    loading.value = false
  }
}

function createGroup() {
  editing.value = null
  Object.assign(form, { name: '', strategy: 'select', endpoint_ids: [], aliases: {} })
  dialogVisible.value = true
}

function editGroup(group) {
  editing.value = group
  Object.assign(form, {
    name: group.name,
    strategy: group.strategy,
    endpoint_ids: [...group.endpoint_ids],
    aliases: { ...(group.aliases || {}) },
  })
  dialogVisible.value = true
}

async function save() {
  saving.value = true
  try {
    const aliases = Object.fromEntries(form.endpoint_ids
      .map((id) => [id, (form.aliases[id] || '').trim()])
      .filter(([, alias]) => alias))
    const payload = { name: form.name, strategy: form.strategy, endpoint_ids: form.endpoint_ids, aliases }
    if (editing.value) await put(`/mihomo/proxy-groups/${editing.value.id}`, payload)
    else await post('/mihomo/proxy-groups', payload)
    ElMessage.success(editing.value ? '客户端节点组已保存' : '客户端节点组已创建')
    dialogVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function remove(group) {
  await ElMessageBox.confirm(`确认删除客户端节点组“${group.name}”？正在被客户端配置使用的节点组不能删除。`, '删除客户端节点组', { type: 'warning' })
  await del(`/mihomo/proxy-groups/${group.id}`)
  ElMessage.success('客户端节点组已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="客户端节点组" description="把多个可用连接放在一组，供客户端手动选择、自动测速或故障切换">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="createGroup">新建节点组</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <el-alert title="组内每一项都是客户端可使用的连接。例如，可以把不同地区的连接放在同一组中供用户选择。" type="info" show-icon :closable="false" style="margin-bottom: 16px" />
      <div class="table-panel">
        <el-table :data="groups">
          <el-table-column label="节点组名称" min-width="180" prop="name" />
          <el-table-column label="选择方式" width="130"><template #default="{ row }"><el-tag>{{ strategyNames[row.strategy] }}</el-tag></template></el-table-column>
          <el-table-column label="包含的客户端连接" min-width="420">
            <template #default="{ row }">
              <div class="node-tags">
                <el-tag v-for="id in row.endpoint_ids" :key="id" type="info">{{ row.aliases?.[id] || nodeNames[id] || `连接已失效 ${id.slice(0, 8)}` }}</el-tag>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="连接数" width="90" align="center"><template #default="{ row }">{{ row.endpoint_ids.length }}</template></el-table-column>
          <el-table-column label="操作" width="160" fixed="right">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="editGroup(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑客户端节点组' : '新建客户端节点组'" width="720px">
      <el-form label-position="top">
        <el-form-item label="节点组名称" required>
          <el-input v-model="form.name" placeholder="例如：美国 + 日本手动选择" />
        </el-form-item>
        <el-form-item label="连接选择方式" required>
          <el-radio-group v-model="form.strategy">
            <el-radio value="select">手动选择</el-radio>
            <el-radio value="url-test">自动测速</el-radio>
            <el-radio value="fallback">故障自动切换</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="包含的客户端连接" required>
          <el-select v-model="form.endpoint_ids" multiple filterable collapse-tags :max-collapse-tags="3" style="width: 100%" placeholder="请选择一个或多个接入用户">
            <el-option v-for="node in clientNodes" :key="node.id" :value="node.id" :label="node.label">
              <span>{{ node.label }}</span>
              <span class="option-meta">{{ node.protocol }} · {{ node.address }}</span>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item v-if="form.endpoint_ids.length" label="客户端显示名称">
          <div class="alias-list">
            <div v-for="id in form.endpoint_ids" :key="id" class="alias-row">
              <span>{{ nodeNames[id] || id }}</span>
              <el-input v-model="form.aliases[id]" maxlength="128" placeholder="例如：东京 IPLC 01" />
            </div>
          </div>
          <div class="form-hint">这里的名称只用于客户端显示；留空时使用系统生成的名称。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!form.name.trim() || !form.endpoint_ids.length" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.node-tags { display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 0; }
.option-meta { float: right; margin-left: 18px; color: var(--sb-muted); font-size: 12px; }
.alias-list { width: 100%; border: 1px solid var(--sb-border); border-radius: 8px; overflow: hidden; }
.alias-row { display: grid; grid-template-columns: minmax(220px, 1fr) 240px; align-items: center; gap: 16px; padding: 10px 12px; border-bottom: 1px solid var(--sb-border); }
.alias-row:last-child { border-bottom: 0; }
.alias-row > span { overflow: hidden; color: var(--sb-text); text-overflow: ellipsis; white-space: nowrap; }
</style>
