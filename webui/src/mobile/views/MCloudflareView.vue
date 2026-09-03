<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search, Setting } from '@element-plus/icons-vue'
import { api, del, post, put } from '../../api'
import { formatDate, formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loading = ref(false)
const tab = ref('records')
const settings = ref({})
// 这张表就是 Cloudflare 区域本身：每次保存和删除都直达上游，
// 本地不留副本，也就不会和上游对不上。
const records = ref([])
const certificates = ref([])
const listeners = ref([])
const settingsOpen = ref(false)
const recordOpen = ref(false)
const certificateOpen = ref(false)
const config = reactive({ zone_id: '', zone_name: '', api_token: '' })
const record = reactive({ id: '', type: 'A', name: '', content: '', ttl: 1, proxied: false })
const saving = ref(false)
const remoteError = ref('')
const certificate = reactive({ id: '', domain: '', certificate: '', private_key: '' })
const keyword = ref('')
const selectedType = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const actionKind = ref('record')

const recordTypes = ['A', 'AAAA', 'CNAME', 'TXT']
const nodeAddresses = computed(() => appState.nodes.filter((node) => node.client_address))
const filteredRecords = computed(() => records.value.filter((row) => {
  if (selectedType.value && row.type !== selectedType.value) return false
  return includesText([row.name, row.content, row.type, boundTo(row)], keyword.value)
}))
const filteredCertificates = computed(() => certificates.value.filter((row) =>
  includesText([row.domain, row.subject, row.issuer, (row.dns_names || []).join(' ')], keyword.value)))
// 只有 VLESS 的 WebSocket 和 gRPC 经 Cloudflare 回源，所以要覆盖的
// 就是这些接入服务的连接域名。
const certificateUsedBy = computed(() => Object.fromEntries(certificates.value.map((row) => [
  row.id,
  listeners.value.filter((listener) => listener.spec?.protocol === 'vless'
    && listener.spec?.tls?.enabled
    && !listener.spec?.reality?.enabled
    && ['ws', 'grpc'].includes(listener.spec?.transport?.type)
    && domainCovered(row.domain, listener.connection_domain)),
])))

// "*." 只跨一级标签，和 TLS 对通配符的处理一致。
function domainCovered(pattern, domain) {
  if (!pattern || !domain) return false
  if (pattern === domain) return true
  if (!pattern.startsWith('*.')) return false
  const suffix = pattern.slice(1)
  if (!domain.endsWith(suffix)) return false
  const label = domain.slice(0, domain.length - suffix.length)
  return Boolean(label) && !label.includes('.')
}

async function load() {
  loading.value = true
  try {
    const [settingsResult, certificateResult, listenerResult] = await Promise.all([
      api('/cloudflare/settings'),
      api('/cloudflare/origin-certificates'),
      api('/listeners'),
    ])
    remoteError.value = settingsResult.error || ''
    let recordResult = { records: [] }
    if (settingsResult.connected) {
      // 这里失败意味着整个区域都读不到；用一张空表把它藏起来，
      // 正是这个页面看着像坏了的原因。
      recordResult = await api('/cloudflare/records').catch((error) => {
        remoteError.value = error instanceof Error ? error.message : '读取 Cloudflare 域名记录失败'
        return { records: [] }
      })
    }
    settings.value = settingsResult
    records.value = recordResult.records || []
    certificates.value = certificateResult.certificates || []
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}

function openSettings() {
  Object.assign(config, { zone_id: settings.value.zone_id || '', zone_name: settings.value.zone_name || '', api_token: '' })
  settingsOpen.value = true
}

// 保存会到上游校验区域和令牌，被拒绝时必须带原因告诉操作者，
// 而不是像成功了一样把弹窗关掉。
async function saveSettings() {
  try {
    await put('/cloudflare/settings', config)
    settingsOpen.value = false
    ElMessage.success('域名服务连接设置已保存')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '连接 Cloudflare 失败，请检查区域编号与访问令牌')
  }
}

function addRecord() {
  Object.assign(record, { id: '', type: 'A', name: '', content: '', ttl: 1, proxied: false })
  recordOpen.value = true
}

function editRecord(row) {
  Object.assign(record, {
    id: row.id,
    type: row.type,
    name: String(row.name || '').replace(/\.$/, ''),
    content: row.content,
    ttl: row.ttl || 1,
    proxied: Boolean(row.proxied),
  })
  recordOpen.value = true
}

async function saveRecord() {
  const body = { type: record.type, name: record.name, content: record.content, ttl: record.ttl, proxied: record.proxied }
  saving.value = true
  try {
    if (record.id) await put(`/cloudflare/records/${record.id}`, body)
    else await post('/cloudflare/records', body)
    recordOpen.value = false
    ElMessage.success('域名记录已写入 Cloudflare')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '写入 Cloudflare 失败')
  } finally { saving.value = false }
}

function addCertificate() {
  Object.assign(certificate, { id: '', domain: '', certificate: '', private_key: '' })
  certificateOpen.value = true
}

function editCertificate(row) {
  Object.assign(certificate, { id: row.id, domain: row.domain, certificate: '', private_key: '' })
  certificateOpen.value = true
}

async function saveCertificate() {
  saving.value = true
  try {
    if (certificate.id) await put(`/cloudflare/origin-certificates/${certificate.id}`, certificate)
    else await post('/cloudflare/origin-certificates', certificate)
    certificateOpen.value = false
    ElMessage.success('源证书已保存，相关服务器的配置已自动下发')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '源证书保存失败')
  } finally { saving.value = false }
}

async function removeCertificate(row) {
  await ElMessageBox.confirm(`删除 ${row.domain} 的源证书？相关接入服务将改回平台自签证书。`, '删除源证书', { type: 'warning' })
  await del(`/cloudflare/origin-certificates/${row.id}`)
  ElMessage.success('源证书已删除，相关服务器的配置已自动下发')
  await load()
}

async function removeRecord(row) {
  await ElMessageBox.confirm(`从 Cloudflare 删除 ${row.type} 记录 ${row.name}？`, '删除域名记录', { type: 'warning' })
  try {
    await del(`/cloudflare/records/${row.id}`)
    ElMessage.success('域名记录已从 Cloudflare 删除')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '从 Cloudflare 删除失败')
  }
}

// 关联关系是从接入服务本身读出来的：名字正好是某个服务连接域名的记录
// 就服务于那个服务，否则内容对上某台服务器地址的仍然指向那台服务器。
function boundTo(row) {
  const bindings = row.bindings || []
  if (!bindings.length) return '未关联'
  return bindings.map((binding) => (binding.listener_name ? `${binding.node_name} · ${binding.listener_name}` : binding.node_name)).join('、')
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  if (actionKind.value === 'record') {
    return [
      { label: '类型', value: row.type },
      { label: '记录值', value: row.content, mono: true },
      { label: 'CDN 代理', value: row.proxied ? '已启用' : '未启用' },
      { label: '缓存时间', value: row.ttl === 1 ? '自动' : `${row.ttl} 秒` },
      { label: '关联', value: boundTo(row) },
    ]
  }
  return [
    { label: '覆盖域名', value: (row.dns_names || []).join('、') || '—' },
    { label: '签发者', value: row.issuer || '—' },
    { label: '有效期至', value: formatDateTime(row.not_after) },
    {
      label: '使用中',
      value: certificateUsedBy.value[row.id]?.length
        ? certificateUsedBy.value[row.id].map((listener) => listener.name).join('、')
        : '暂无匹配的接入服务',
    },
  ]
})

function openActions(row, kind) {
  actionTarget.value = row
  actionKind.value = kind
  actionsOpen.value = true
}

const actions = computed(() => (isAdmin.value ? [{ key: 'edit', label: '修改' }, { key: 'delete', label: '删除', danger: true }] : []))

function runAction(key) {
  const row = actionTarget.value
  if (actionKind.value === 'record') {
    if (key === 'edit') return editRecord(row)
    if (key === 'delete') return removeRecord(row)
    return
  }
  if (key === 'edit') return editCertificate(row)
  if (key === 'delete') return removeCertificate(row)
}

onMounted(load)
</script>

<template>
  <MPage :loading="loading">
    <!-- 连接状态和「设置」入口连在一起：改的就是这条状态里说的那件事。 -->
    <div class="m-notice conn" :class="settings.connected ? 'm-notice--info' : settings.configured ? 'm-notice--danger' : 'm-notice--warning'">
      <span class="conn__text">
        {{ settings.connected ? `已连接 ${settings.zone_name || settings.zone_id}` : settings.configured ? '连接异常' : '未连接 Cloudflare' }}
      </span>
      <el-button v-if="isAdmin" size="small" :icon="Setting" @click="openSettings">设置</el-button>
    </div>
    <div v-if="tab === 'records' && remoteError" class="m-notice m-notice--danger">
      读取 Cloudflare 失败：{{ remoteError }}。请点上面的「设置」检查区域编号与访问令牌，并确认这台控制机可以访问 api.cloudflare.com。
    </div>

    <MSegmented v-model="tab" :options="[{ value: 'records', label: '域名记录' }, { value: 'certificates', label: '源证书', badge: certificates.length }]" />

    <el-input v-model="keyword" clearable :prefix-icon="Search" :placeholder="tab === 'records' ? '搜索域名、内容或服务器' : '搜索域名或证书'" />
    <div v-if="tab === 'records'" class="m-filters">
      <MSegmented v-model="selectedType" :options="[{ value: '', label: '全部类型' }, ...recordTypes.map((type) => ({ value: type, label: type }))]" />
    </div>

    <template v-if="tab === 'records'">
      <article v-for="row in filteredRecords" :key="row.id" class="m-item">
        <button
          type="button"
          class="m-item__hit"
          :class="{ 'is-static': !isAdmin }"
          @click="isAdmin && openActions(row, 'record')"
        >
          <div class="m-item__head">
            <span class="m-pill m-pill--accent">{{ row.type }}</span>
            <span class="m-item__title">{{ row.name }}</span>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ row.proxied ? '已启用' : '未启用' }}</b><small>CDN 代理</small></span>
            <span class="m-stat"><b>{{ row.ttl === 1 ? '自动' : `${row.ttl} 秒` }}</b><small>缓存时间</small></span>
          </div>
          <div class="m-item__meta m-item__meta--mono">{{ row.content }}</div>
          <div class="m-item__meta" :class="{ 'm-muted': !(row.bindings || []).length }">{{ boundTo(row) }}</div>
        </button>
      </article>
      <div v-if="!filteredRecords.length && !loading" class="m-empty">还没有域名记录</div>
    </template>

    <template v-else>
      <div class="m-notice m-notice--info">
        源证书用于 VLESS + WebSocket / gRPC 经 Cloudflare 加速时的回源：接入服务的连接域名命中下面的域名后，会改用对应源证书对外提供 TLS，Cloudflare 的 SSL 模式即可使用「完全（严格）」。未配置时仍使用平台自签证书。
      </div>
      <article v-for="row in filteredCertificates" :key="row.id" class="m-item">
        <button
          type="button"
          class="m-item__hit"
          :class="{ 'is-static': !isAdmin }"
          @click="isAdmin && openActions(row, 'certificate')"
        >
          <div class="m-item__head">
            <span class="m-item__title">{{ row.domain }}</span>
            <span v-if="row.expired" class="m-pill m-pill--danger">已过期</span>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ formatDate(row.not_after) }}</b><small>有效期至</small></span>
            <span class="m-stat"><b>{{ certificateUsedBy[row.id]?.length || 0 }}</b><small>使用中的服务</small></span>
          </div>
          <div class="m-item__meta">覆盖 {{ (row.dns_names || []).join('、') || '—' }}</div>
          <div class="m-item__meta" :class="{ 'm-muted': !certificateUsedBy[row.id]?.length }">
            {{ certificateUsedBy[row.id]?.length ? `使用中：${certificateUsedBy[row.id].map((listener) => listener.name).join('、')}` : '暂无匹配的接入服务' }}
          </div>
        </button>
      </article>
      <div v-if="!filteredCertificates.length && !loading" class="m-empty">还没有源证书</div>
    </template>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.name || actionTarget?.domain" :details="details" :actions="actions" @select="runAction" />

    <MSheet v-model="settingsOpen" title="连接 Cloudflare">
      <div class="m-field">
        <label class="m-field__label">区域编号（Zone ID）</label>
        <el-input v-model="config.zone_id" aria-label="区域编号" />
      </div>
      <div class="m-field">
        <label class="m-field__label">域名</label>
        <el-input v-model="config.zone_name" aria-label="域名" placeholder="example.com" />
      </div>
      <div class="m-field">
        <label class="m-field__label">访问令牌（API Token）</label>
        <el-input v-model="config.api_token" type="password" show-password aria-label="访问令牌" placeholder="请输入有效令牌；留空不会保留原令牌" />
      </div>
      <template #footer>
        <el-button @click="settingsOpen = false">取消</el-button>
        <el-button type="primary" :disabled="!config.zone_id || !config.api_token" @click="saveSettings">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="recordOpen" :title="record.id ? '修改域名记录' : '新建域名记录'" full>
      <div class="m-field">
        <label class="m-field__label">记录类型</label>
        <MSegmented v-model="record.type" :options="recordTypes.map((type) => ({ value: type, label: type }))" />
      </div>
      <div class="m-field">
        <label class="m-field__label">域名</label>
        <el-input v-model="record.name" aria-label="域名" :placeholder="`proxy.${settings.zone_name || 'example.com'}`" />
      </div>
      <div class="m-field">
        <label class="m-field__label">指向地址或内容 <em>*</em></label>
        <el-input v-model="record.content" aria-label="指向地址或内容" />
        <!-- 关联关系由平台自动识别，这几个标签只是省去手抄服务器地址。 -->
        <div v-if="nodeAddresses.length" class="m-chips">
          <button v-for="node in nodeAddresses" :key="node.id" type="button" class="m-chip pick" @click="record.content = node.client_address">
            {{ node.name }} · {{ node.client_address }}
          </button>
        </div>
      </div>
      <div class="m-field">
        <label class="m-field__label">缓存时间（TTL）</label>
        <el-input-number v-model="record.ttl" :min="1" :disabled="record.proxied" controls-position="right" aria-label="缓存时间" style="width: 100%" />
      </div>
      <div class="m-field m-field--inline">
        <label class="m-field__label">Cloudflare 加速</label>
        <el-switch v-model="record.proxied" aria-label="Cloudflare 加速" />
      </div>
      <div class="m-notice m-notice--info">保存后立即写入 Cloudflare，无需再发布。若该域名下已有接入服务，平台会自动识别并在列表中标注。</div>
      <template #footer>
        <el-button @click="recordOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!record.name || !record.content" @click="saveRecord">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="certificateOpen" :title="certificate.id ? '修改源证书' : '新建源证书'" full>
      <div class="m-field">
        <label class="m-field__label">域名 <em>*</em></label>
        <el-input v-model="certificate.domain" aria-label="证书域名" placeholder="*.example.com" />
        <div class="m-field__hint">填写纯文本域名，支持 *.example.com 通配一级子域，或直接填写完整域名。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">{{ certificate.id ? '证书内容（留空表示不修改）' : '证书内容' }}</label>
        <el-input v-model="certificate.certificate" type="textarea" :rows="7" aria-label="证书内容" class="m-mono" placeholder="-----BEGIN CERTIFICATE-----" />
      </div>
      <div class="m-field">
        <label class="m-field__label">{{ certificate.id ? '私钥内容（留空表示不修改）' : '私钥内容' }}</label>
        <el-input v-model="certificate.private_key" type="textarea" :rows="7" aria-label="私钥内容" class="m-mono" placeholder="-----BEGIN PRIVATE KEY-----" />
        <div class="m-field__hint">在 Cloudflare 的「SSL/TLS → 源服务器 → 创建证书」中生成后原样粘贴；私钥保存后不再回显。</div>
      </div>
      <template #footer>
        <el-button @click="certificateOpen = false">取消</el-button>
        <el-button
          type="primary" :loading="saving"
          :disabled="!certificate.domain || (!certificate.id && (!certificate.certificate || !certificate.private_key))"
          @click="saveCertificate"
        >保存</el-button>
      </template>
    </MSheet>

    <template v-if="isAdmin" #fab>
      <button
        type="button"
        class="m-fab"
        :aria-label="tab === 'records' ? '新建域名记录' : '新建源证书'"
        :disabled="tab === 'records' && !settings.connected"
        @click="tab === 'records' ? addRecord() : addCertificate()"
      >
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
.conn { display: flex; align-items: center; gap: 10px; }
.conn__text { flex: 1; min-width: 0; }
.pick { border-style: dashed; cursor: pointer; }
</style>
