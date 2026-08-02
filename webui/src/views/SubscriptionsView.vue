<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { ArrowDown, ArrowUp, CopyDocument, Delete, Edit, Plus, Refresh, RefreshRight } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const changingState = ref('')
const dialogVisible = ref(false)
const editing = ref(null)
const configs = ref([])
const proxyGroups = ref([])
const form = reactive({ name: '', proxy_group_ids: [], rule_mode: 'table', rules: [], raw_rules: '' })
const strategyNames = { select: '手动选择', 'url-test': '自动测速', fallback: '故障切换' }
const ruleTypes = [
  'DOMAIN', 'DOMAIN-SUFFIX', 'DOMAIN-KEYWORD', 'DOMAIN-WILDCARD', 'DOMAIN-REGEX', 'GEOSITE',
  'IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP', 'SRC-GEOIP', 'SRC-IP-ASN',
  'SRC-IP-CIDR', 'SRC-IP-SUFFIX', 'DST-PORT', 'SRC-PORT', 'IN-PORT', 'IN-TYPE',
  'IN-USER', 'IN-NAME', 'REMATCH-NAME', 'PROCESS-NAME', 'PROCESS-NAME-WILDCARD',
  'PROCESS-NAME-REGEX', 'PROCESS-PATH', 'PROCESS-PATH-WILDCARD', 'PROCESS-PATH-REGEX',
  'UID', 'NETWORK', 'DSCP', 'AND', 'OR', 'NOT', 'MATCH',
]
const noResolveTypes = new Set(['IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP'])
const groupByID = computed(() => Object.fromEntries(proxyGroups.value.map((group) => [group.id, group])))
const ruleActions = computed(() => [...resolveGroups(form.proxy_group_ids).map((group) => group.name), 'DIRECT', 'REJECT'])
const formValid = computed(() => {
  if (!form.name.trim() || !form.proxy_group_ids.length) return false
  if (form.rule_mode === 'text') return Boolean(form.raw_rules.trim())
  return Boolean(form.rules.length && form.rules.at(-1)?.type === 'MATCH' && form.rules.every((rule) => rule.action && (rule.type === 'MATCH' || rule.value.trim())))
})

function resolveGroups(ids) {
  const result = []
  const seen = new Set()
  function visit(id) {
    if (seen.has(id)) return
    seen.add(id)
    const group = groupByID.value[id]
    if (!group) return
    for (const member of group.members || []) if (member.kind === 'group') visit(member.id)
    result.push(group)
  }
  for (const id of ids || []) visit(id)
  return result
}

async function load() {
  loading.value = true
  try {
    const [configResult, groupResult] = await Promise.all([api('/mihomo/client-configs'), api('/mihomo/proxy-groups')])
    configs.value = configResult.client_configs || []
    proxyGroups.value = groupResult.proxy_groups || []
  } finally {
    loading.value = false
  }
}

function resetForm(config = null) {
  editing.value = config
  Object.assign(form, config ? {
    name: config.name,
    proxy_group_ids: [...(config.proxy_group_ids || [])],
    rule_mode: config.rule_mode || 'table',
    rules: (config.rules || []).map((rule) => ({
      type: rule.type,
      value: rule.value || '',
      action: rule.action,
      no_resolve: Boolean(rule.no_resolve),
    })),
    raw_rules: config.raw_rules || '',
  } : { name: '', proxy_group_ids: [], rule_mode: 'table', rules: [], raw_rules: '' })
  dialogVisible.value = true
}

function addRule() {
  form.rules.push({ type: 'DOMAIN-SUFFIX', value: '', action: '', no_resolve: false })
}

function setRuleType(rule, type) {
  rule.type = type
  if (type === 'MATCH') rule.value = ''
  if (!noResolveTypes.has(type)) rule.no_resolve = false
}

function moveRule(index, offset) {
  const target = index + offset
  if (target < 0 || target >= form.rules.length) return
  const [rule] = form.rules.splice(index, 1)
  form.rules.splice(target, 0, rule)
}

async function save() {
  saving.value = true
  try {
    const payload = {
      name: form.name.trim(),
      proxy_group_ids: [...form.proxy_group_ids],
      rule_mode: form.rule_mode,
      rules: form.rule_mode === 'table' ? form.rules.map((rule) => ({
        type: rule.type,
        value: rule.type === 'MATCH' ? '' : rule.value.trim(),
        action: rule.action,
        no_resolve: Boolean(rule.no_resolve),
      })) : [],
      raw_rules: form.rule_mode === 'text' ? form.raw_rules : '',
    }
    if (editing.value) await put(`/mihomo/client-configs/${editing.value.id}`, payload)
    else await post('/mihomo/client-configs', payload)
    ElMessage.success(editing.value ? '客户端配置已保存' : '客户端配置已创建')
    dialogVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

function absoluteSubscription(config) {
  return new URL(config.subscription_path, window.location.origin).toString()
}

async function writeClipboard(text) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.readOnly = true
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  textarea.select()
  const copied = document.execCommand('copy')
  textarea.remove()
  if (!copied) throw new Error('浏览器不支持自动复制')
}

async function setEnabled(config, enabled) {
  changingState.value = config.id
  try {
    await post(`/mihomo/client-configs/${config.id}/enabled`, { enabled })
    ElMessage.success(enabled ? '客户端配置已启用' : '客户端配置已停用')
    await load()
  } finally {
    changingState.value = ''
  }
}

async function copySubscription(config) {
  try {
    await writeClipboard(absoluteSubscription(config))
    ElMessage.success('更新地址已复制')
  } catch {
    ElMessage.error('自动复制失败，请使用 HTTPS 访问后重试')
  }
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
    <PageHeader title="客户端配置" description="引用已有代理分组并配置客户端分流规则，生成可持续更新的 Mihomo 配置">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="resetForm()">新建客户端配置</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <div class="table-panel">
        <el-table :data="configs">
          <el-table-column label="配置名称" min-width="180" prop="name" />
          <el-table-column label="引用代理分组" min-width="300">
            <template #default="{ row }">
              <el-tag v-for="id in row.proxy_group_ids" :key="id" type="info" class="group-tag">
                {{ groupByID[id]?.name || '分组已失效' }}<template v-if="groupByID[id]"> · {{ strategyNames[groupByID[id].strategy] }}</template>
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="分流规则" min-width="170">
            <template #default="{ row }">{{ row.rule_mode === 'text' ? '高级文本' : '表格配置' }} · {{ row.rules?.length || 0 }} 条</template>
          </el-table-column>
          <el-table-column label="状态" width="110">
            <template #default="{ row }">
              <el-switch :model-value="row.enabled" inline-prompt active-text="启用" inactive-text="停用" :loading="changingState === row.id" :disabled="changingState === row.id || !canWrite" @change="setEnabled(row, $event)" />
            </template>
          </el-table-column>
          <el-table-column label="操作" width="330" fixed="right">
            <template #default="{ row }">
              <el-button link :icon="CopyDocument" @click="copySubscription(row)">复制更新地址</el-button>
              <el-button v-if="canWrite" link :icon="RefreshRight" @click="rotateSubscription(row)">更换地址</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="resetForm(row)">编辑</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
            </template>
          </el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑客户端配置' : '新建客户端配置'" width="min(980px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="配置名称" required><el-input v-model="form.name" maxlength="128" /></el-form-item>
        <el-form-item label="引用代理分组" required>
          <el-select v-model="form.proxy_group_ids" multiple filterable style="width: 100%" placeholder="选择一个或多个已有代理分组">
            <el-option v-for="group in proxyGroups" :key="group.id" :label="`${group.name} · ${strategyNames[group.strategy]}`" :value="group.id" />
          </el-select>
        </el-form-item>

        <section class="rules-section">
          <div class="section-heading">
            <div><h3>访问规则</h3><span>规则按从上到下的顺序执行</span></div>
            <el-radio-group v-model="form.rule_mode" aria-label="规则配置模式">
              <el-radio-button value="table">表格配置</el-radio-button>
              <el-radio-button value="text">高级纯文本</el-radio-button>
            </el-radio-group>
          </div>
          <template v-if="form.rule_mode === 'table'">
            <div class="rule-table-wrap">
              <table class="rule-table">
                <thead><tr><th>类型</th><th>匹配值</th><th>动作</th><th>no-resolve</th><th>顺序</th></tr></thead>
                <tbody>
                  <tr v-for="(rule, index) in form.rules" :key="index">
                    <td data-label="类型"><el-select :model-value="rule.type" aria-label="规则类型" filterable @update:model-value="setRuleType(rule, $event)"><el-option v-for="type in ruleTypes" :key="type" :label="type" :value="type" /></el-select></td>
                    <td data-label="匹配值"><el-input v-model="rule.value" aria-label="规则匹配值" :disabled="rule.type === 'MATCH'" /></td>
                    <td data-label="动作"><el-select v-model="rule.action" aria-label="规则动作" filterable><el-option v-for="action in ruleActions" :key="action" :label="action" :value="action" /></el-select></td>
                    <td class="check-cell" data-label="no-resolve"><el-checkbox v-model="rule.no_resolve" aria-label="no-resolve" :disabled="!noResolveTypes.has(rule.type)" /></td>
                    <td class="rule-actions" data-label="顺序">
                      <el-tooltip content="上移"><el-button :icon="ArrowUp" text aria-label="上移规则" :disabled="index === 0" @click="moveRule(index, -1)" /></el-tooltip>
                      <el-tooltip content="下移"><el-button :icon="ArrowDown" text aria-label="下移规则" :disabled="index === form.rules.length - 1" @click="moveRule(index, 1)" /></el-tooltip>
                      <el-tooltip content="删除"><el-button :icon="Delete" text type="danger" aria-label="删除规则" @click="form.rules.splice(index, 1)" /></el-tooltip>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <el-button :icon="Plus" class="add-rule" @click="addRule">添加访问规则</el-button>
          </template>
          <el-input v-else v-model="form.raw_rules" type="textarea" :rows="10" aria-label="高级规则文本" placeholder="DOMAIN-SUFFIX,example.com,代理组&#10;MATCH,DIRECT" />
        </section>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!formValid" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.group-tag { margin: 3px 6px 3px 0; }
.rules-section { padding-top: 20px; border-top: 1px solid var(--el-border-color-lighter); }
.section-heading { display: flex; align-items: center; justify-content: space-between; gap: 16px; margin-bottom: 14px; }
.section-heading h3 { margin: 0 0 3px; font-size: 16px; }
.section-heading span { color: var(--sb-muted); font-size: 12px; }
.rule-table-wrap { max-width: 100%; overflow-x: hidden; }
.rule-table { width: 100%; border-collapse: collapse; table-layout: fixed; }
.rule-table th { padding: 0 8px 8px; color: var(--sb-muted); font-size: 12px; font-weight: 500; text-align: left; }
.rule-table th:nth-child(1) { width: 21%; }
.rule-table th:nth-child(2) { width: 27%; }
.rule-table th:nth-child(3) { width: 22%; }
.rule-table th:nth-child(4) { width: 12%; text-align: center; }
.rule-table th:nth-child(5) { width: 18%; }
.rule-table td { padding: 5px 8px; border-top: 1px solid var(--el-border-color-lighter); }
.check-cell { text-align: center; }
.rule-actions { white-space: nowrap; }
.add-rule { margin-top: 12px; }
@media (max-width: 640px) { .section-heading { align-items: flex-start; flex-direction: column; } }
@media (max-width: 720px) {
  .rule-table, .rule-table tbody, .rule-table tr, .rule-table td { display: block; width: 100%; }
  .rule-table thead { display: none; }
  .rule-table tr { padding: 10px 0; border-top: 1px solid var(--el-border-color-lighter); }
  .rule-table td { display: grid; grid-template-columns: 92px minmax(0, 1fr); align-items: center; gap: 10px; padding: 5px 0; border: 0; }
  .rule-table td::before { content: attr(data-label); color: var(--sb-muted); font-size: 12px; }
  .rule-table .check-cell { text-align: left; }
  .rule-table .rule-actions { display: flex; justify-content: flex-end; }
  .rule-table .rule-actions::before { margin-right: auto; }
}
</style>
