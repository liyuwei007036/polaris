<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Delete, Edit, Plus, Refresh, Search, Setting } from '@element-plus/icons-vue'
import { api, del, post, put } from '../api'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const isAdmin = inject('isAdmin')
const appState = inject('appState')
const loading = ref(false)
const tab = ref('records')
const settings = ref({})
// The table is the Cloudflare zone itself: every save and every delete goes
// straight upstream, so there is no local copy that could disagree with it.
const records = ref([])
const certificates = ref([])
const listeners = ref([])
const settingsOpen = ref(false)
const recordOpen = ref(false)
const certificateOpen = ref(false)
const config = reactive({ zone_id: '', zone_name: '', api_token: '' })
const record = reactive({ id: '', type: 'A', name: '', content: '', ttl: 1, proxied: false, zone_name: '' })
const saving = ref(false)
const remoteError = ref('')
// An origin certificate is matched to an access service by its connection
// domain, so the operator only pastes the plain "*.example.com" text and the
// PEM pair Cloudflare issued for it.
const certificate = reactive({ id: '', domain: '', certificate: '', private_key: '' })
const keyword = ref('')
const selectedType = ref('')
const selectedNode = ref('')

const nodeAddresses = computed(() => appState.nodes.filter((node) => node.client_address))
// One console can manage several Cloudflare zones. Each has its own token, so
// each reports its own connection state.
const zones = computed(() => settings.value.zones || [])
const connectedLabel = computed(() => {
  if (!settings.value.connected) return settings.value.configured ? '连接异常' : '未连接 Cloudflare'
  const names = zones.value.length
    ? zones.value.filter((zone) => zone.connected).map((zone) => zone.zone_name)
    : [settings.value.zone_name || settings.value.zone_id]
  return `已连接 ${names.filter(Boolean).join('、')}`
})
const recordsAvailable = computed(() => zones.value.length
  ? zones.value.some((zone) => zone.connected)
  : Boolean(settings.value.connected))
const filteredRecords = computed(() => records.value.filter((row) => {
  if (selectedType.value && row.type !== selectedType.value) return false
  if (selectedNode.value && !(row.bindings || []).some((binding) => binding.node_name === selectedNode.value)) return false
  return includesText([row.name, row.content, row.type, row.zone_name, boundTo(row)], keyword.value)
}))
const filteredCertificates = computed(() => certificates.value.filter((row) =>
  includesText([row.domain, row.subject, row.issuer, (row.dns_names || []).join(' ')], keyword.value)))
// Only VLESS httpupgrade and gRPC access services pull through Cloudflare, so
// those are the ones whose connection domain a certificate has to cover.
const certificateUsedBy = computed(() => Object.fromEntries(certificates.value.map((row) => [
  row.id,
  listeners.value.filter((listener) => listener.spec?.protocol === 'vless'
    && listener.spec?.tls?.enabled
    && !listener.spec?.reality?.enabled
    && ['httpupgrade', 'grpc'].includes(listener.spec?.transport?.type)
    && domainCovered(row.domain, listener.connection_domain)),
])))

// A "*." pattern spans exactly one label, the way TLS treats wildcards.
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
    let recordResult = { records: [], zone_errors: [] }
    if (settingsResult.configured) {
      // A failure here means some zone could not be read at all; hiding it
      // behind an empty table is what made this page look broken.
      recordResult = await api('/cloudflare/records').catch((error) => {
        remoteError.value = error instanceof Error ? error.message : '读取 Cloudflare 域名记录失败'
        return { records: [], zone_errors: [] }
      })
      if (!remoteError.value && (recordResult.zone_errors || []).length) {
        remoteError.value = recordResult.zone_errors.map((zone) => `${zone.zone_name}：${zone.error}`).join('；')
      }
    }
    settings.value = settingsResult
    records.value = recordResult.records || []
    certificates.value = certificateResult.certificates || []
    listeners.value = listenerResult.listeners || []
  } finally { loading.value = false }
}

function openSettings() {
  const first = zones.value[0]
  Object.assign(config, {
    zone_id: first?.zone_id || settings.value.zone_id || '',
    zone_name: first?.zone_name || settings.value.zone_name || '',
    api_token: '',
  })
  settingsOpen.value = true
}
function newZone() {
  Object.assign(config, { zone_id: '', zone_name: '', api_token: '' })
}
function editZone(zone) {
  Object.assign(config, { zone_id: zone.zone_id, zone_name: zone.zone_name, api_token: '' })
}
async function removeZone(zone) {
  await ElMessageBox.confirm(`停止管理区域 ${zone.zone_name}？该区域下的 DNS 记录不会被删除。`, '删除区域', { type: 'warning' })
  try {
    await del(`/cloudflare/settings/${encodeURIComponent(zone.zone_id)}`)
    ElMessage.success('区域已移除')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '移除区域失败')
  }
}
// Saving verifies the zone and token upstream, so a rejection has to reach the
// operator with its reason instead of closing the dialog as if it worked.
async function saveSettings() {
  if (!config.zone_id || !config.zone_name || !config.api_token) {
    ElMessage.warning('请填写区域编号（Zone ID）、域名与访问令牌（API Token）')
    return
  }
  try {
    await put('/cloudflare/settings', config)
    config.api_token = ''
    ElMessage.success('域名服务连接设置已保存')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '连接 Cloudflare 失败，请检查区域编号与访问令牌')
  }
}
function addRecord() {
  if (!zones.value.length && !settings.value.zone_id) {
    ElMessage.warning('请先点击右上角「设置」配置 Cloudflare 区域与访问令牌')
    openSettings()
    return
  }
  Object.assign(record, { id: '', type: 'A', name: '', content: '', ttl: 1, proxied: false, zone_name: zones.value[0]?.zone_name || '' })
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
    zone_name: row.zone_name || '',
  })
  recordOpen.value = true
}

// A bare label such as "proxy" becomes the full name under the chosen zone, so
// nobody has to remember which suffix a record belongs to.
function qualifyRecordName() {
  const name = String(record.name || '').trim().replace(/\.$/, '').toLowerCase()
  if (name && !name.includes('.') && record.zone_name) record.name = `${name}.${record.zone_name}`
}

async function saveRecord() {
  qualifyRecordName()
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
  try {
    if (certificate.id) {
      await put(`/cloudflare/origin-certificates/${certificate.id}`, certificate)
    } else {
      await post('/cloudflare/origin-certificates', certificate)
    }
    certificateOpen.value = false
    ElMessage.success('源证书已保存，相关服务器的配置已自动下发')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '源证书保存失败')
  }
}

async function removeCertificate(row) {
  await ElMessageBox.confirm(`删除 ${row.domain} 的源证书？相关接入服务将改回平台自签证书。`, '删除源证书', { type: 'warning' })
  await del(`/cloudflare/origin-certificates/${row.id}`)
  ElMessage.success('源证书已删除，相关服务器的配置已自动下发')
  await load()
}

async function remove(row) {
  await ElMessageBox.confirm(`从 Cloudflare 删除 ${row.type} 记录 ${row.name}？`, '删除域名记录', { type: 'warning' })
  try {
    await del(`/cloudflare/records/${row.id}`)
    ElMessage.success('域名记录已从 Cloudflare 删除')
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '从 Cloudflare 删除失败')
  }
}

// The association is read off the access services themselves: a record whose
// name is a service's connection domain serves that service, and otherwise a
// content that matches a server's address still names that server.
function boundTo(row) {
  const bindings = row.bindings || []
  if (!bindings.length) return '未关联'
  return bindings.map((binding) => (binding.listener_name ? `${binding.node_name} · ${binding.listener_name}` : binding.node_name)).join('、')
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="域名解析">
      <!-- 连接状态放在页头，不占用列表上方的位置。 -->
      <el-tag :type="settings.connected ? 'success' : settings.configured ? 'danger' : 'warning'" effect="plain">
        {{ connectedLabel }}
      </el-tag>
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="isAdmin && tab === 'records'" :icon="Setting" @click="openSettings">设置</el-button>
      <el-button v-if="isAdmin && tab === 'records'" type="primary" :icon="Plus" :disabled="!recordsAvailable" @click="addRecord">新建</el-button>
      <el-button v-if="isAdmin && tab === 'certificates'" type="primary" :icon="Plus" @click="addCertificate">新建</el-button>
    </PageHeader>
    <main class="page-content page-content--tight">
      <el-alert
        v-if="tab === 'records' && remoteError"
        :title="`读取 Cloudflare 失败：${remoteError}`"
        description="请点击「设置」检查区域编号与访问令牌，并确认这台控制机可以访问 api.cloudflare.com。"
        type="error" show-icon :closable="false" style="margin-bottom: 12px" />
      <div class="search-toolbar">
        <el-input v-model="keyword" clearable :prefix-icon="Search" :placeholder="tab === 'records' ? '搜索域名、内容或服务器' : '搜索域名或证书'" style="width: 260px" />
        <template v-if="tab === 'records'">
          <el-select v-model="selectedNode" clearable placeholder="全部服务器" style="width: 180px">
            <el-option v-for="node in appState.nodes" :key="node.id" :label="node.name" :value="node.name" />
          </el-select>
          <el-select v-model="selectedType" clearable placeholder="全部类型" style="width: 130px"><el-option v-for="type in ['A','AAAA','CNAME','TXT']" :key="type" :label="type" :value="type" /></el-select>
        </template>
      </div>
      <div class="table-panel">
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="域名记录" name="records">
            <PagedTable :rows="filteredRecords" :loading="loading" empty-text="还没有域名记录">
              <el-table-column label="类型" prop="type" width="88" />
              <el-table-column label="域名" prop="name" min-width="180" show-overflow-tooltip />
              <el-table-column label="指向地址或内容" prop="content" min-width="170" show-overflow-tooltip />
              <el-table-column label="区域" prop="zone_name" min-width="150" show-overflow-tooltip />
              <el-table-column label="关联服务器 / 接入服务" min-width="200" show-overflow-tooltip>
                <template #default="{ row }">
                  <span :class="{ subtle: !(row.bindings || []).length }">{{ boundTo(row) }}</span>
                  <div v-if="(row.bindings || []).some((binding) => binding.listener_port)" class="subtle">
                    端口 {{ [...new Set(row.bindings.filter((binding) => binding.listener_port).map((binding) => binding.listener_port))].join('、') }}
                  </div>
                </template>
              </el-table-column>
              <el-table-column label="CDN 加速" width="106" align="center"><template #default="{ row }">{{ row.proxied ? '已启用' : '未启用' }}</template></el-table-column>
              <el-table-column label="缓存时间" width="106" align="center"><template #default="{ row }">{{ row.ttl === 1 ? '自动' : `${row.ttl} 秒` }}</template></el-table-column>
              <el-table-column label="操作" width="156" fixed="right" class-name="action-column">
                <template #default="{ row }">
                  <el-button v-if="isAdmin" link :icon="Edit" @click="editRecord(row)">编辑</el-button>
                  <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="remove(row)">删除</el-button>
                </template>
              </el-table-column>
            </PagedTable>
          </el-tab-pane>
          <el-tab-pane :label="`源证书（${certificates.length}）`" name="certificates">
            <el-alert
              title="源证书用于 VLESS + httpupgrade / gRPC 经 Cloudflare 加速时的回源：接入服务的连接域名命中下表中的域名后，会改用对应源证书对外提供 TLS，Cloudflare 的 SSL 模式即可使用“完全（严格）”。未配置时仍使用平台自签证书。"
              type="info" show-icon :closable="false" style="margin-bottom: 12px" />
            <PagedTable :rows="filteredCertificates" :loading="loading" empty-text="还没有源证书">
              <el-table-column label="域名" prop="domain" min-width="170" show-overflow-tooltip />
              <el-table-column label="证书覆盖的域名" min-width="190" show-overflow-tooltip>
                <template #default="{ row }">
                  <div>{{ (row.dns_names || []).join('、') || '—' }}</div>
                  <div class="subtle">{{ row.issuer ? `签发者 ${row.issuer}` : '' }}</div>
                </template>
              </el-table-column>
              <el-table-column label="有效期至" width="164"><template #default="{ row }">{{ formatDateTime(row.not_after) }}</template></el-table-column>
              <el-table-column label="状态" width="88"><template #default="{ row }"><el-tag :type="row.expired ? 'danger' : 'success'">{{ row.expired ? '已过期' : '有效' }}</el-tag></template></el-table-column>
              <el-table-column label="使用中的接入服务" min-width="190" show-overflow-tooltip>
                <template #default="{ row }">
                  <span :class="{ subtle: !certificateUsedBy[row.id]?.length }">
                    {{ certificateUsedBy[row.id]?.length ? certificateUsedBy[row.id].map((listener) => listener.name).join('、') : '暂无匹配的接入服务' }}
                  </span>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="156" fixed="right" class-name="action-column">
                <template #default="{ row }">
                  <el-button v-if="isAdmin" link :icon="Edit" @click="editCertificate(row)">编辑</el-button>
                  <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeCertificate(row)">删除</el-button>
                </template>
              </el-table-column>
            </PagedTable>
          </el-tab-pane>
        </el-tabs>
      </div>
    </main>

    <el-dialog v-model="settingsOpen" title="连接 Cloudflare" width="620px">
      <div class="zone-list">
        <div v-for="zone in zones" :key="zone.zone_id" class="zone-row">
          <div class="zone-main">
            <div class="zone-name">{{ zone.zone_name }}</div>
            <div class="zone-meta">{{ zone.zone_id }} · {{ zone.token_masked || '—' }}</div>
            <div v-if="zone.error" class="zone-error">{{ zone.error }}</div>
          </div>
          <el-tag :type="zone.connected ? 'success' : 'danger'" effect="plain">{{ zone.connected ? '已连接' : '连接异常' }}</el-tag>
          <el-button v-if="isAdmin" link :icon="Edit" @click="editZone(zone)">更新令牌</el-button>
          <el-button v-if="isAdmin" link type="danger" :icon="Delete" @click="removeZone(zone)">删除</el-button>
        </div>
        <div v-if="!zones.length" class="form-hint">还没有绑定区域。填写区域编号、域名和访问令牌后点击「保存」。</div>
        <div v-else class="zone-add"><el-button v-if="isAdmin" link :icon="Plus" @click="newZone">添加新区域</el-button></div>
      </div>
      <el-divider />
      <div class="form-hint" style="margin-bottom: 8px">同一个访问令牌可以授权多个区域；每个区域单独添加一次即可。</div>
      <el-form label-position="top">
        <el-form-item label="区域编号（Zone ID）"><el-input v-model="config.zone_id" /></el-form-item>
        <el-form-item label="域名"><el-input v-model="config.zone_name" placeholder="example.com" /></el-form-item>
        <el-form-item label="访问令牌（API Token）"><el-input v-model="config.api_token" type="password" show-password placeholder="请输入有效令牌；留空不会保留原令牌" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="settingsOpen = false">取消</el-button>
        <el-button type="primary" @click="saveSettings">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="recordOpen" :title="record.id ? '修改域名记录' : '新建域名记录'" width="620px">
      <el-form label-position="top">
        <el-row :gutter="16">
          <el-col :span="8"><el-form-item label="记录类型"><el-select v-model="record.type"><el-option v-for="type in ['A','AAAA','CNAME','TXT']" :key="type" :label="type" :value="type" /></el-select></el-form-item></el-col>
          <el-col :span="16"><el-form-item label="所属区域"><el-select v-model="record.zone_name" placeholder="选择区域" style="width: 100%"><el-option v-for="zone in zones" :key="zone.zone_id" :label="zone.zone_name" :value="zone.zone_name" /></el-select></el-form-item></el-col>
        </el-row>
        <el-form-item label="域名"><el-input v-model="record.name" :placeholder="`proxy.${record.zone_name || settings.zone_name || 'example.com'}`" /></el-form-item>
        <el-form-item label="指向地址或内容" required>
          <el-input v-model="record.content" />
          <!-- 关联关系由平台自动识别，这里只是省去手抄服务器地址。 -->
          <div v-if="nodeAddresses.length" class="address-picks">
            <span class="form-hint">填入服务器地址：</span>
            <el-tag v-for="node in nodeAddresses" :key="node.id" class="address-pick" @click="record.content = node.client_address">
              {{ node.name }} · {{ node.client_address }}
            </el-tag>
          </div>
        </el-form-item>
        <el-row :gutter="16">
          <el-col :span="12"><el-form-item label="缓存时间（TTL）"><el-input-number v-model="record.ttl" :min="1" :disabled="record.proxied" style="width: 100%" /></el-form-item></el-col>
          <el-col :span="12"><el-form-item label="Cloudflare 加速"><el-switch v-model="record.proxied" active-text="启用" inactive-text="关闭" /></el-form-item></el-col>
        </el-row>
        <el-alert title="保存后立即写入 Cloudflare，无需再发布。若该域名下已有接入服务，平台会自动识别并在列表中标注。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="recordOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" :disabled="!record.name || !record.content" @click="saveRecord">保存</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="certificateOpen" :title="certificate.id ? '修改源证书' : '新建源证书'" width="min(680px, 96vw)">
      <el-form label-position="top">
        <el-form-item label="域名" required>
          <el-input v-model="certificate.domain" placeholder="*.example.com" />
          <div class="form-hint">填写纯文本域名，支持 *.example.com 通配一级子域，或直接填写完整域名。</div>
        </el-form-item>
        <el-form-item :label="certificate.id ? '证书内容（留空表示不修改）' : '证书内容'" :required="!certificate.id">
          <el-input v-model="certificate.certificate" type="textarea" :rows="6" placeholder="-----BEGIN CERTIFICATE-----" />
        </el-form-item>
        <el-form-item :label="certificate.id ? '私钥内容（留空表示不修改）' : '私钥内容'" :required="!certificate.id">
          <el-input v-model="certificate.private_key" type="textarea" :rows="6" placeholder="-----BEGIN PRIVATE KEY-----" />
          <div class="form-hint">在 Cloudflare 的「SSL/TLS → 源服务器 → 创建证书」中生成后原样粘贴；私钥保存后不再回显。</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="certificateOpen = false">取消</el-button>
        <el-button
          type="primary"
          :disabled="!certificate.domain || (!certificate.id && (!certificate.certificate || !certificate.private_key))"
          @click="saveCertificate">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.form-hint { margin-top: 6px; color: var(--sb-muted); font-size: 12px; line-height: 1.5; }
.address-picks { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.address-pick { cursor: pointer; }
.zone-list { display: flex; flex-direction: column; gap: 8px; }
.zone-row { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border: 1px solid var(--el-border-color-lighter); border-radius: 8px; }
.zone-main { flex: 1; min-width: 0; }
.zone-name { font-weight: 600; }
.zone-meta { font-size: 12px; color: var(--sb-muted); word-break: break-all; }
.zone-error { margin-top: 4px; font-size: 12px; color: var(--el-color-danger); }
.zone-add { margin-top: 4px; }
</style>
