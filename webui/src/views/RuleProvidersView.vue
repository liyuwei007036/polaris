<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const providers = ref([])
const proxyGroups = ref([])
const dialogOpen = ref(false)
const editing = ref(null)
const keyword = ref('')
const behaviorNames = { domain: '域名', ipcidr: 'IP 网段', classical: '经典规则' }
const form = reactive({ name: '', behavior: 'domain', format: 'mrs', url: '', path: '', interval: 86400, proxy: 'DIRECT' })

// A provider downloads its rules through one of the client's own proxies, so
// the choices are the proxy groups plus a direct download.
const proxyOptions = computed(() => ['DIRECT', ...proxyGroups.value.map((group) => group.name)])
const filtered = computed(() => providers.value.filter(
  (row) => includesText([row.name, behaviorNames[row.behavior], row.format, row.url, row.path, row.proxy], keyword.value),
))
const formValid = computed(() => Boolean(form.name.trim() && form.url.trim() && form.path.trim() && form.interval > 0 && form.proxy))

async function load() {
  loading.value = true
  try {
    const [providerResult, groupResult] = await Promise.all([
      api('/mihomo/rule-providers'),
      api('/mihomo/proxy-groups').catch(() => ({ proxy_groups: [] })),
    ])
    providers.value = providerResult.rule_providers || []
    proxyGroups.value = groupResult.proxy_groups || []
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, { name: '', behavior: 'domain', format: 'mrs', url: '', path: '', interval: 86400, proxy: 'DIRECT' })
  dialogOpen.value = true
}

function openEdit(row) {
  editing.value = row
  Object.assign(form, { name: row.name, behavior: row.behavior, format: row.format, url: row.url, path: row.path, interval: row.interval, proxy: row.proxy })
  dialogOpen.value = true
}

// mrs files only carry domain or IP lists; a classical rule set has to be
// yaml or text, so the format follows the behaviour rather than failing on save.
function setBehavior(behavior) {
  form.behavior = behavior
  if (behavior === 'classical' && form.format === 'mrs') form.format = 'yaml'
}

async function save() {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      behavior: form.behavior,
      format: form.format,
      url: form.url.trim(),
      path: form.path.trim(),
      interval: Number(form.interval),
      proxy: form.proxy,
    }
    if (editing.value) await put(`/mihomo/rule-providers/${editing.value.id}`, payload)
    else await post('/mihomo/rule-providers', payload)
    ElMessage.success(editing.value ? '规则供应商已保存' : '规则供应商已创建')
    dialogOpen.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '规则供应商保存失败')
  } finally {
    saving.value = false
  }
}

async function remove(row) {
  await ElMessageBox.confirm(`确认删除规则供应商“${row.name}”？`, '删除规则供应商', { type: 'warning' })
  try {
    await del(`/mihomo/rule-providers/${row.id}`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '规则供应商删除失败')
    return
  }
  ElMessage.success('规则供应商已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="规则供应商">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="openCreate">新建</el-button>
    </PageHeader>

    <main class="page-content page-content--tight">
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或类型" style="width: 280px" />
        <span class="toolbar__spacer" />
        <span class="subtle">在“客户端配置”里可以选择这里维护的供应商</span>
      </div>
      <div class="table-panel">
        <PagedTable :rows="filtered" :loading="loading" empty-text="还没有规则供应商">
          <el-table-column label="名称" prop="name" min-width="170" />
          <el-table-column label="行为" width="120"><template #default="{ row }">{{ behaviorNames[row.behavior] || row.behavior }}</template></el-table-column>
          <el-table-column label="格式" width="100"><template #default="{ row }">{{ (row.format || '').toUpperCase() }}</template></el-table-column>
          <el-table-column label="规则地址" min-width="280"><template #default="{ row }"><span class="mono">{{ row.url }}</span></template></el-table-column>
          <el-table-column label="保存路径" min-width="190"><template #default="{ row }"><span class="mono">{{ row.path }}</span></template></el-table-column>
          <el-table-column label="更新间隔" width="120"><template #default="{ row }">{{ row.interval }} 秒</template></el-table-column>
          <el-table-column label="下载代理" width="140" prop="proxy" />
          <el-table-column label="操作" width="150" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="dialogOpen" :title="editing ? '编辑规则供应商' : '新建规则供应商'" width="min(680px, 96vw)">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="名称" required><el-input v-model="form.name" aria-label="供应商名称" maxlength="128" /></el-form-item></el-col>
          <el-col :span="6">
            <el-form-item label="行为" required>
              <el-select :model-value="form.behavior" aria-label="供应商行为" style="width: 100%" @update:model-value="setBehavior">
                <el-option label="域名" value="domain" /><el-option label="IP 网段" value="ipcidr" /><el-option label="经典规则" value="classical" />
              </el-select>
            </el-form-item>
          </el-col>
          <el-col :span="6">
            <el-form-item label="格式" required>
              <el-select v-model="form.format" aria-label="供应商格式" style="width: 100%">
                <el-option label="MRS" value="mrs" :disabled="form.behavior === 'classical'" /><el-option label="YAML" value="yaml" /><el-option label="Text" value="text" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <el-form-item label="规则地址" required><el-input v-model="form.url" aria-label="供应商规则地址" placeholder="https://example.com/rules.mrs" /></el-form-item>
        <el-form-item label="保存路径" required><el-input v-model="form.path" aria-label="供应商保存路径" placeholder="./ruleset/rules.mrs" /></el-form-item>
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="更新间隔（秒）" required><el-input-number v-model="form.interval" aria-label="供应商更新间隔" :min="1" :max="31536000" controls-position="right" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="12">
            <el-form-item label="下载代理" required>
              <el-select v-model="form.proxy" aria-label="供应商下载代理" filterable allow-create style="width: 100%">
                <el-option v-for="proxy in proxyOptions" :key="proxy" :label="proxy" :value="proxy" />
              </el-select>
            </el-form-item>
          </el-col>
        </el-row>
        <el-alert title="下载代理必须是 DIRECT，或引用该供应商的客户端配置中存在的代理分组、节点名称。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="dialogOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>
