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
const form = reactive({ name: '', strategy: 'select', members: [] })
const supported = new Set(['vless', 'vmess', 'trojan', 'shadowsocks', 'hysteria2', 'socks', 'http'])
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }

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
const nodeNames = computed(() => Object.fromEntries(clientNodes.value.map((item) => [item.id, item.label])))
const groupNames = computed(() => Object.fromEntries(groups.value.map((item) => [item.id, item.name])))
const formValid = computed(() => Boolean(form.name.trim() && form.members.length))

async function load() {
  loading.value = true
  try {
    await loadNodes()
    const [groupResult, listenerResult] = await Promise.all([api('/mihomo/proxy-groups'), api('/listeners')])
    groups.value = groupResult.proxy_groups || []
    listeners.value = listenerResult.listeners || []
    const results = await Promise.all(listeners.value.map((listener) => api(`/listeners/${listener.id}/endpoints`).catch(() => ({ endpoints: [] }))))
    endpoints.value = results.flatMap((result) => result.endpoints || [])
  } finally {
    loading.value = false
  }
}

function resetForm(group = null) {
  editing.value = group
  Object.assign(form, group ? {
    name: group.name,
    strategy: group.strategy,
    members: structuredClone(group.members || []),
  } : { name: '', strategy: 'select', members: [] })
  dialogVisible.value = true
}

function memberKeys() {
  return form.members.map((member) => `${member.kind}:${member.id}`)
}

function setMembers(keys) {
  form.members = keys.map((key) => {
    const separator = key.indexOf(':')
    return { kind: key.slice(0, separator), id: key.slice(separator + 1) }
  })
}

function memberLabel(member) {
  return member.kind === 'group' ? groupNames.value[member.id] || '分组已失效' : nodeNames.value[member.id] || '节点已失效'
}

async function save() {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      strategy: form.strategy,
      members: form.members.map((member) => ({ kind: member.kind, id: member.id })),
    }
    if (editing.value) await put(`/mihomo/proxy-groups/${editing.value.id}`, payload)
    else await post('/mihomo/proxy-groups', payload)
    ElMessage.success(editing.value ? '代理分组已保存' : '代理分组已创建')
    dialogVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function remove(group) {
  await ElMessageBox.confirm(`确认删除代理分组“${group.name}”？`, '删除代理分组', { type: 'warning' })
  await del(`/mihomo/proxy-groups/${group.id}`)
  ElMessage.success('代理分组已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="代理分组" description="组合客户端节点和其他代理分组，供客户端配置引用">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="resetForm()">新建代理分组</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <el-alert title="分组成员按顺序写入 Mihomo；被其他分组或客户端配置引用的分组不能删除。" type="info" show-icon :closable="false" class="page-alert" />
      <div class="table-panel">
        <el-table :data="groups">
          <el-table-column label="分组名称" min-width="180" prop="name" />
          <el-table-column label="策略" width="140"><template #default="{ row }">{{ strategyNames[row.strategy] }}</template></el-table-column>
          <el-table-column label="成员" min-width="340">
            <template #default="{ row }">
              <el-tag v-for="member in row.members" :key="`${member.kind}:${member.id}`" :type="member.kind === 'group' ? 'primary' : 'info'" class="member-tag">
                {{ member.kind === 'group' ? '分组' : '节点' }} · {{ memberLabel(member) }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="操作" width="180" fixed="right">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="resetForm(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑代理分组' : '新建代理分组'" width="min(760px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="分组名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="策略" required>
          <el-select v-model="form.strategy" style="width: 100%">
            <el-option v-for="(label, value) in strategyNames" :key="value" :label="label" :value="value" />
          </el-select>
        </el-form-item>
        <el-form-item label="分组成员" required>
          <el-select :model-value="memberKeys()" multiple filterable style="width: 100%" aria-label="分组成员" placeholder="选择节点或代理分组" @update:model-value="setMembers">
            <el-option-group label="接入节点">
              <el-option v-for="node in clientNodes" :key="`endpoint:${node.id}`" :value="`endpoint:${node.id}`" :label="node.label" :disabled="node.disabled">
                <span>{{ node.label }}</span><span class="option-meta">{{ node.detail }} · {{ node.protocol }} · {{ node.address }}</span>
              </el-option>
            </el-option-group>
            <el-option-group label="代理分组">
              <el-option v-for="group in groups.filter((item) => item.id !== editing?.id)" :key="`group:${group.id}`" :value="`group:${group.id}`" :label="group.name" />
            </el-option-group>
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.page-alert { margin-bottom: 16px; }
.member-tag { margin: 3px 6px 3px 0; }
.option-meta { float: right; margin-left: 18px; color: var(--sb-muted); font-size: 12px; }
@media (max-width: 640px) { .option-meta { display: none; } }
</style>
