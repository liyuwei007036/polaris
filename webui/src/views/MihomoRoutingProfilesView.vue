<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import PageHeader from '../components/PageHeader.vue'

const canWrite = inject('canWrite')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const saving = ref(false)
const dialogVisible = ref(false)
const editing = ref(null)
const profiles = ref([])

const typeOptions = [
  ['DOMAIN-SUFFIX', '域名后缀', '例如 google.com'],
  ['DOMAIN', '完整域名', '例如 api.example.com'],
  ['DOMAIN-KEYWORD', '域名关键词', '例如 google'],
  ['DOMAIN-WILDCARD', '域名通配符', '例如 *.example.com'],
  ['DOMAIN-REGEX', '域名正则', '例如 ^api\\..*'],
  ['GEOSITE', '域名分类', '例如 CN 或 geolocation-!cn'],
  ['IP-CIDR', 'IPv4 网段', '例如 1.1.1.0/24'],
  ['IP-CIDR6', 'IPv6 网段', '例如 2001:db8::/32'],
  ['IP-SUFFIX', '目标 IP 后缀', '例如 8.8.8.8/24'],
  ['IP-ASN', '目标 IP ASN', '例如 13335'],
  ['GEOIP', 'IP 地区', '例如 CN'],
  ['SRC-GEOIP', '来源 IP 地区', '例如 CN'],
  ['SRC-IP-ASN', '来源 IP ASN', '例如 9808'],
  ['SRC-IP-CIDR', '来源 IP 网段', '例如 192.168.1.0/24'],
  ['SRC-IP-SUFFIX', '来源 IP 后缀', '例如 192.168.1.1/24'],
  ['DST-PORT', '目标端口', '例如 443 或 8000-9000'],
  ['SRC-PORT', '来源端口', '例如 5353'],
  ['IN-PORT', '客户端监听端口', '例如 7890'],
  ['IN-TYPE', '客户端监听类型', '例如 SOCKS/HTTP'],
  ['IN-USER', '客户端连接用户', '例如 mihomo'],
  ['IN-NAME', '客户端监听名称', '例如 mixed-in'],
  ['REMATCH-NAME', '重匹配名称', '例如 rematch1'],
  ['PROCESS-NAME', '进程名称', '例如 chrome.exe'],
  ['PROCESS-NAME-WILDCARD', '进程名通配符', '例如 *telegram*'],
  ['PROCESS-NAME-REGEX', '进程名正则', '例如 chrome$'],
  ['PROCESS-PATH', '进程路径', '例如 C:\\Apps\\app.exe'],
  ['PROCESS-PATH-WILDCARD', '进程路径通配符', '例如 /usr/*/wget'],
  ['PROCESS-PATH-REGEX', '进程路径正则', '例如 .*bin/wget'],
  ['UID', 'Linux 用户 ID', '例如 1001'],
  ['NETWORK', '网络类型', 'tcp 或 udp'],
  ['DSCP', 'DSCP 标记', '例如 4'],
  ['AND', '逻辑与', '例如 ((DOMAIN,a.com),(NETWORK,UDP))'],
  ['OR', '逻辑或', '例如 ((DOMAIN,a.com),(NETWORK,UDP))'],
  ['NOT', '逻辑非', '例如 ((DOMAIN,a.com))'],
].map(([value, label, placeholder]) => ({ value, label, placeholder }))
const typeMap = Object.fromEntries(typeOptions.map((item) => [item.value, item]))
const actionOptions = [
  { value: 'PROXY', label: '使用节点组' },
  { value: 'DIRECT', label: '直连' },
  { value: 'REJECT', label: '拦截' },
]
const actionMap = Object.fromEntries(actionOptions.map((item) => [item.value, item.label]))
const presetNames = {
  'china-direct': '国内网站直接访问，其他网站使用节点组',
  'proxy-all': '全部使用节点组',
  'direct-all': '全部直连',
  custom: '自定义规则',
}

const form = reactive({
  name: '', rule_preset: 'china-direct', default_action: 'PROXY', editor_mode: 'table',
  rules: [], raw_rules: '',
})
const rawRuleCount = computed(() => form.raw_rules.split(/\r?\n/).filter((line) => line.trim() && !line.trim().startsWith('#')).length)

function blankRule() {
  return { type: 'DOMAIN-SUFFIX', value: '', action: 'PROXY', no_resolve: false }
}

function legacyRules(profile) {
  return [
    ...(profile.reject_domains || []).map((value) => ({ type: 'DOMAIN-SUFFIX', value, action: 'REJECT', no_resolve: false })),
    ...(profile.direct_domains || []).map((value) => ({ type: 'DOMAIN-SUFFIX', value, action: 'DIRECT', no_resolve: false })),
    ...(profile.proxy_domains || []).map((value) => ({ type: 'DOMAIN-SUFFIX', value, action: 'PROXY', no_resolve: false })),
    ...(profile.proxy_cidrs || []).map((value) => ({ type: 'IP-CIDR', value, action: 'PROXY', no_resolve: true })),
  ]
}

function formatRules(rules, includeMatch = true) {
  const lines = rules.map((rule) => `${rule.type},${rule.value},${rule.action}${rule.no_resolve ? ',no-resolve' : ''}`)
  if (includeMatch && form.rule_preset === 'custom') lines.push(`MATCH,${form.default_action}`)
  return lines.join('\n')
}

function splitRuleFields(line) {
  const parts = []
  let start = 0
  let depth = 0
  for (let index = 0; index < line.length; index++) {
    if (line[index] === '(') depth++
    else if (line[index] === ')') depth--
    else if (line[index] === ',' && depth === 0) {
      parts.push(line.slice(start, index).trim())
      start = index + 1
    }
    if (depth < 0) throw new Error('括号不匹配')
  }
  if (depth !== 0) throw new Error('括号不匹配')
  parts.push(line.slice(start).trim())
  return parts
}

function parseRules(raw) {
  const parsed = []
  const lines = raw.replace(/\r\n/g, '\n').split('\n')
  for (let index = 0; index < lines.length; index++) {
    let line = lines[index].trim()
    if (!line || line.startsWith('#')) continue
    if (line.startsWith('-')) line = line.slice(1).trim()
    let parts
    try { parts = splitRuleFields(line) }
    catch (error) { throw new Error(`第 ${index + 1} 行：${error.message}`) }
    if (parts[0]?.toUpperCase() === 'MATCH' && parts.length >= 2) {
      if (index !== lines.length - 1 && lines.slice(index + 1).some((item) => item.trim() && !item.trim().startsWith('#'))) throw new Error(`第 ${index + 1} 行：MATCH 必须是最后一条规则`)
      const action = parts[1].toUpperCase()
      if (!actionMap[action]) throw new Error(`第 ${index + 1} 行：不支持的执行动作`)
      form.rule_preset = 'custom'
      form.default_action = action
      continue
    }
    const type = parts[0]?.toUpperCase()
    const action = parts[2]?.toUpperCase()
    if (!typeMap[type]) throw new Error(`第 ${index + 1} 行：不支持的规则类型 ${parts[0] || ''}`)
    if (!parts[1]) throw new Error(`第 ${index + 1} 行：缺少匹配值`)
    if (!actionMap[action]) throw new Error(`第 ${index + 1} 行：不支持的执行动作`)
    const options = parts.slice(3).filter(Boolean)
    if (options.some((item) => item.toLowerCase() !== 'no-resolve')) throw new Error(`第 ${index + 1} 行：只支持 no-resolve 选项`)
    const noResolve = options.some((item) => item.toLowerCase() === 'no-resolve')
    if (noResolve && !['IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP'].includes(type)) throw new Error(`第 ${index + 1} 行：该规则不能使用 no-resolve`)
    parsed.push({ type, value: parts[1], action, no_resolve: noResolve })
  }
  return parsed
}

function changeMode(mode) {
  if (mode === form.editor_mode) return
  try {
    if (mode === 'raw') form.raw_rules = formatRules(form.rules)
    else form.rules = parseRules(form.raw_rules)
    form.editor_mode = mode
  } catch (error) {
    ElMessage.error(error.message)
  }
}

async function load() {
  loading.value = true
  try { profiles.value = (await api('/mihomo/routing-profiles')).routing_profiles || [] }
  finally { loading.value = false }
}

function createProfile() {
  editing.value = null
  Object.assign(form, { name: '', rule_preset: 'china-direct', default_action: 'PROXY', editor_mode: 'table', rules: [], raw_rules: '' })
  dialogVisible.value = true
}

function editProfile(profile) {
  editing.value = profile
  const rules = profile.rules?.length ? profile.rules.map((rule) => ({ ...rule })) : legacyRules(profile)
  Object.assign(form, {
    name: profile.name, rule_preset: profile.rule_preset, default_action: profile.default_action || 'PROXY',
    editor_mode: 'table', rules, raw_rules: profile.raw_rules || '',
  })
  dialogVisible.value = true
}

function addRule() { form.rules.push(blankRule()) }
function removeRule(index) { form.rules.splice(index, 1) }
function supportsNoResolve(type) { return ['IP-CIDR', 'IP-CIDR6', 'IP-SUFFIX', 'IP-ASN', 'GEOIP'].includes(type) }

async function save() {
  saving.value = true
  try {
    const rules = form.editor_mode === 'raw' ? parseRules(form.raw_rules) : form.rules.map((rule) => ({ ...rule, value: rule.value.trim() }))
    if (rules.some((rule) => !rule.value)) throw new Error('每条表格规则都必须填写匹配值')
    const rawRules = form.editor_mode === 'raw' ? form.raw_rules : formatRules(rules)
    const payload = {
      name: form.name, rule_preset: form.rule_preset, default_action: form.default_action,
      proxy_domains: [], direct_domains: [], reject_domains: [], proxy_cidrs: [],
      rules, raw_rules: rawRules,
    }
    if (editing.value) await put(`/mihomo/routing-profiles/${editing.value.id}`, payload)
    else await post('/mihomo/routing-profiles', payload)
    ElMessage.success(editing.value ? '客户端访问规则已保存' : '客户端访问规则已创建')
    dialogVisible.value = false
    await load()
  } catch (error) {
    ElMessage.error(error.message)
  } finally { saving.value = false }
}

async function remove(profile) {
  await ElMessageBox.confirm(`确认删除客户端访问规则“${profile.name}”？正在被客户端配置使用的规则不能删除。`, '删除客户端访问规则', { type: 'warning' })
  await del(`/mihomo/routing-profiles/${profile.id}`)
  ElMessage.success('客户端访问规则已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="客户端访问规则" description="决定客户端访问不同网站时直接连接、使用节点组或阻止访问">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="canWrite" type="primary" :icon="Plus" @click="createProfile">新建访问规则</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content">
      <div class="context-strip">
        <strong>只影响客户端配置</strong>
        <span>这里的规则不会修改服务器设置，也不会改变接入用户的连接信息。</span>
      </div>
      <div class="table-panel">
        <el-table :data="profiles">
          <el-table-column label="规则名称" min-width="200" prop="name" />
          <el-table-column label="默认方式" min-width="210"><template #default="{ row }"><el-tag>{{ presetNames[row.rule_preset] }}</el-tag></template></el-table-column>
          <el-table-column label="自定义规则" min-width="150"><template #default="{ row }">{{ row.rules?.length || legacyRules(row).length }} 条</template></el-table-column>
          <el-table-column label="其他网站" width="130"><template #default="{ row }">{{ row.rule_preset === 'direct-all' || row.default_action === 'DIRECT' ? '直接访问' : '使用节点组' }}</template></el-table-column>
          <el-table-column label="操作" width="160" fixed="right"><template #default="{ row }"><el-button v-if="canWrite" link :icon="Edit" @click="editProfile(row)">编辑</el-button><el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button></template></el-table-column>
        </el-table>
      </div>
    </main>

    <el-dialog v-model="dialogVisible" :title="editing ? '编辑客户端访问规则' : '新建客户端访问规则'" width="min(980px, 94vw)" destroy-on-close>
      <el-form label-position="top">
        <div class="profile-basics">
          <el-form-item label="规则名称" required><el-input v-model="form.name" placeholder="例如：国内网站直接访问" /></el-form-item>
          <el-form-item label="默认访问方式" required><el-select v-model="form.rule_preset" style="width: 100%"><el-option v-for="(label, value) in presetNames" :key="value" :label="label" :value="value" /></el-select></el-form-item>
          <el-form-item v-if="form.rule_preset === 'custom'" label="其他网站"><el-select v-model="form.default_action" style="width: 100%"><el-option label="使用节点组" value="PROXY" /><el-option label="直接访问" value="DIRECT" /></el-select></el-form-item>
        </div>

        <div class="editor-head">
          <div><strong>详细规则</strong><span>系统按从上到下的顺序检查，排在前面的规则优先生效。</span></div>
          <el-segmented :model-value="form.editor_mode" :options="[{ label: '表格设置', value: 'table' }, { label: '高级文本设置', value: 'raw' }]" @change="changeMode" />
        </div>

        <div v-if="form.editor_mode === 'table'" class="rule-editor">
          <div v-if="!form.rules.length" class="rule-empty">尚未添加详细规则，默认访问方式仍会正常生效。</div>
          <div v-for="(rule, index) in form.rules" :key="index" class="rule-row">
            <span class="rule-index">{{ index + 1 }}</span>
            <el-select v-model="rule.type" filterable class="rule-type" @change="!supportsNoResolve(rule.type) && (rule.no_resolve = false)"><el-option v-for="option in typeOptions" :key="option.value" :label="option.label" :value="option.value"><span>{{ option.label }}</span><span class="option-code">{{ option.value }}</span></el-option></el-select>
            <el-input v-model="rule.value" class="rule-value" :placeholder="typeMap[rule.type]?.placeholder" />
            <el-select v-model="rule.action" class="rule-action"><el-option v-for="option in actionOptions" :key="option.value" :label="option.label" :value="option.value" /></el-select>
            <el-checkbox v-model="rule.no_resolve" :disabled="!supportsNoResolve(rule.type)">不解析域名</el-checkbox>
            <el-button text type="danger" :icon="Delete" aria-label="删除规则" @click="removeRule(index)" />
          </div>
          <el-button :icon="Plus" @click="addRule">添加规则</el-button>
        </div>

        <div v-else class="raw-editor">
          <el-input v-model="form.raw_rules" type="textarea" :rows="14" class="raw-rules" placeholder="# 每行一条 Mihomo 规则&#10;DOMAIN-SUFFIX,google.com,PROXY&#10;IP-CIDR,1.1.1.0/24,PROXY,no-resolve&#10;MATCH,PROXY" />
          <div class="raw-help"><span>此处面向熟悉 Mihomo 规则格式的用户；切回表格时会自动检查内容。</span><strong>{{ rawRuleCount }} 条规则</strong></div>
        </div>
      </el-form>
      <template #footer><el-button @click="dialogVisible = false">取消</el-button><el-button type="primary" :loading="saving" :disabled="!form.name.trim()" @click="save">保存访问规则</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
.context-strip { display: flex; gap: 12px; align-items: baseline; margin-bottom: 16px; color: var(--sb-muted); font-size: 13px; }
.context-strip strong { color: var(--sb-text); }
.profile-basics { display: grid; grid-template-columns: 1.3fr 1fr 1fr; gap: 16px; }
.editor-head { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin: 4px 0 14px; padding-top: 16px; border-top: 1px solid var(--sb-border); }
.editor-head strong, .editor-head span { display: block; }
.editor-head span { margin-top: 4px; color: var(--sb-muted); font-size: 12px; }
.rule-editor { min-height: 220px; }
.rule-row { display: grid; grid-template-columns: 28px minmax(145px, .9fr) minmax(220px, 1.5fr) 110px 110px 36px; gap: 10px; align-items: center; padding: 9px 0; border-bottom: 1px solid #eef1f5; }
.rule-index { color: var(--sb-muted); text-align: center; font-variant-numeric: tabular-nums; }
.rule-empty { padding: 48px 20px; margin-bottom: 12px; color: var(--sb-muted); text-align: center; background: #f8fafc; border: 1px dashed var(--sb-border); border-radius: 8px; }
.option-code { float: right; margin-left: 18px; color: var(--sb-muted); font: 11px "Cascadia Code", monospace; }
.raw-rules :deep(textarea) { font: 13px/1.7 "Cascadia Code", Consolas, monospace; }
.raw-help { display: flex; justify-content: space-between; margin-top: 8px; color: var(--sb-muted); font-size: 12px; }
.raw-help strong { color: var(--sb-text); }
@media (max-width: 760px) {
  .profile-basics { grid-template-columns: 1fr; gap: 0; }
  .editor-head { align-items: flex-start; flex-direction: column; }
  .rule-row { grid-template-columns: 28px 1fr 36px; }
  .rule-value, .rule-action, .rule-row :deep(.el-checkbox) { grid-column: 2; }
}
</style>
