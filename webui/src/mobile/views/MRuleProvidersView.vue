<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const providers = ref([])
const proxyGroups = ref([])
const sheetOpen = ref(false)
const editing = ref(null)
const keyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const behaviorNames = { domain: '域名', ipcidr: 'IP 网段', classical: '经典规则' }
const form = reactive({ name: '', behavior: 'domain', format: 'mrs', url: '', path: '', interval: 86400, proxy: 'DIRECT' })

// 供应商通过客户端自己的代理下载规则，所以可选的就是代理分组加直连。
const proxyOptions = computed(() => ['DIRECT', ...proxyGroups.value.map((group) => group.name)].map((value) => ({ value, label: value })))
const behaviorOptions = Object.entries(behaviorNames).map(([value, label]) => ({ value, label }))
const formatOptions = computed(() => [
  { value: 'mrs', label: 'MRS', disabled: form.behavior === 'classical', desc: form.behavior === 'classical' ? '经典规则不支持 MRS' : '' },
  { value: 'yaml', label: 'YAML' },
  { value: 'text', label: 'Text' },
])
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

function openForm(row = null) {
  editing.value = row
  Object.assign(form, row
    ? { name: row.name, behavior: row.behavior, format: row.format, url: row.url, path: row.path, interval: row.interval, proxy: row.proxy }
    : { name: '', behavior: 'domain', format: 'mrs', url: '', path: '', interval: 86400, proxy: 'DIRECT' })
  sheetOpen.value = true
}

// mrs 文件只装域名或 IP 列表；经典规则集必须是 yaml 或 text，
// 所以格式跟着行为走，而不是等保存时报错。
function setBehavior(behavior) {
  form.behavior = behavior
  if (behavior === 'classical' && form.format === 'mrs') form.format = 'yaml'
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '规则类型', value: behaviorNames[row.behavior] || row.behavior },
    { label: '格式', value: (row.format || '').toUpperCase() },
    { label: '规则地址', value: row.url, mono: true },
    { label: '保存路径', value: row.path, mono: true },
    { label: '更新间隔', value: `${row.interval} 秒` },
    { label: '下载出口', value: row.proxy || '直连' },
  ]
})

function openActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const actions = computed(() => {
  if (!actionTarget.value) return []
  const list = []
  if (canWrite.value) list.push({ key: 'edit', label: '编辑' })
  if (isAdmin.value) list.push({ key: 'delete', label: '删除', danger: true })
  return list
})

function runAction(key) {
  if (key === 'edit') return openForm(actionTarget.value)
  if (key === 'delete') return remove(actionTarget.value)
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
    sheetOpen.value = false
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
  <MPage title="规则供应商" :loading="loading">
    <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或类型" />
    <div class="m-count">共 {{ filtered.length }} 个供应商</div>

    <article v-for="row in filtered" :key="row.id" class="m-item">
      <button type="button" class="m-item__hit" @click="openActions(row)">
        <div class="m-item__head">
          <span class="m-item__title">{{ row.name }}</span>
          <i class="m-item__chevron" aria-hidden="true">›</i>
        </div>
        <div class="m-item__stats">
          <span class="m-stat"><b>{{ behaviorNames[row.behavior] || row.behavior }}</b><small>规则类型</small></span>
          <span class="m-stat"><b>{{ (row.format || '').toUpperCase() }}</b><small>格式</small></span>
          <span class="m-stat"><b>{{ row.interval }} 秒</b><small>更新间隔</small></span>
        </div>
        <div class="m-item__meta m-item__meta--mono">{{ row.url }}</div>
      </button>
    </article>

    <div v-if="!filtered.length && !loading" class="m-empty">还没有规则供应商</div>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name" :details="details" :actions="actions" @select="runAction" />

    <MSheet v-model="sheetOpen" :title="editing ? '编辑规则供应商' : '新建规则供应商'" full>
      <div class="m-field">
        <label class="m-field__label">名称 <em>*</em></label>
        <el-input v-model="form.name" maxlength="128" aria-label="供应商名称" />
      </div>
      <div class="m-field">
        <label class="m-field__label">行为 <em>*</em></label>
        <MPicker :model-value="form.behavior" :options="behaviorOptions" title="选择行为" @update:model-value="setBehavior" />
      </div>
      <div class="m-field">
        <label class="m-field__label">格式 <em>*</em></label>
        <MPicker v-model="form.format" :options="formatOptions" title="选择格式" />
      </div>
      <div class="m-field">
        <label class="m-field__label">规则地址 <em>*</em></label>
        <el-input v-model="form.url" aria-label="供应商规则地址" placeholder="https://example.com/rules.mrs" />
      </div>
      <div class="m-field">
        <label class="m-field__label">保存路径 <em>*</em></label>
        <el-input v-model="form.path" aria-label="供应商保存路径" placeholder="./ruleset/rules.mrs" />
      </div>
      <div class="m-field">
        <label class="m-field__label">更新间隔（秒） <em>*</em></label>
        <el-input-number v-model="form.interval" :min="1" :max="31536000" controls-position="right" aria-label="供应商更新间隔" style="width: 100%" />
      </div>
      <div class="m-field">
        <label class="m-field__label">下载代理 <em>*</em></label>
        <MPicker v-model="form.proxy" :options="proxyOptions" title="选择下载代理" />
        <div class="m-field__hint">需为 DIRECT，或客户端配置中已存在的代理分组 / 节点名称。</div>
      </div>
      <template #footer>
        <el-button @click="sheetOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </MSheet>

    <template v-if="canWrite" #fab>
      <button type="button" class="m-fab" aria-label="新建规则供应商" @click="openForm()">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>
