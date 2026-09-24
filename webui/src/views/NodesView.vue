<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Connection, CopyDocument, Edit, Lock, Plus, Refresh, RemoveFilled, Search, Top } from '@element-plus/icons-vue'
import { api, post, put } from '../api'
import { formatBytes, formatDateTime, includesText } from '../format'
import { subscribeLive } from '../live'
import { connectionSnapshots, subscribeConnections } from '../connections'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const MASTER_HOST_KEY = 'polaris_master_host'
const appState = inject('appState')
const isAdmin = inject('isAdmin')
const canWrite = inject('canWrite')
const loadNodes = inject('loadNodes')
const loading = ref(false)
const pending = ref([])
const metrics = ref({})
const speedtests = ref({})
const speedtesting = reactive({})
const scanners = ref({})
const togglingScanner = reactive({})
const tokenDialog = ref(false)
const token = ref('')
const expiresAt = ref('')
const lifetime = ref(900)
const masterPublicKey = ref('')
const masterHost = ref('')
const agentPort = ref(19994)
const editDialog = ref(false)
const editNode = ref(null)
const editForm = ref({ name: '', client_address: '', scanner_protection: false })
const editSaving = ref(false)
const refreshing = ref(false)
const keyword = ref('')
const statusFilter = ref('')
// 架构 -> 官方最新 sing-box 版本号
const singBoxLatest = ref({})
let stopLive
let stopConnections

const filteredNodes = computed(() => appState.nodes.filter((node) => {
  if (statusFilter.value === 'online' && !node.online) return false
  if (statusFilter.value === 'offline' && node.online) return false
  return includesText([node.name, node.client_address, node.os, node.architecture, node.agent_version, node.sing_box_version], keyword.value)
}))
const filteredPending = computed(() => pending.value.filter((row) => includesText([row.node_name, row.capabilities], keyword.value)))
// Rates and connection counts arrive over SSE as the agents measure them, so
// they update in place instead of being recomputed on every page visit.
const live = computed(() => connectionSnapshots.value)

let lastSingBoxCheckTime = 0

async function load(silent = false) {
  if (refreshing.value) return
  refreshing.value = true
  if (!silent) loading.value = true
  try {
    const [, registrations, metricResult, speedtestResult] = await Promise.all([
      loadNodes(),
      isAdmin.value ? api('/registrations').catch(() => ({ registrations: [] })) : Promise.resolve({ registrations: [] }),
      api('/nodes/metrics').catch(() => ({ nodes: [] })),
      api('/nodes/speedtests/latest').catch(() => ({ speedtests: [] })),
    ])
    pending.value = registrations.registrations || []
    metrics.value = Object.fromEntries((metricResult.nodes || []).map((entry) => [entry.node_id, entry.report]))
    speedtests.value = Object.fromEntries((speedtestResult.speedtests || []).map((s) => [s.node_id, s]))
  } finally {
    loading.value = false
    refreshing.value = false
  }
  // sing-box 官方版本属于可选更新提示，后台静默请求，不阻塞页面表格渲染
  loadSingBoxLatest().catch(() => {})
  loadScanners().catch(() => {})
}

async function loadScanners() {
  if (!isAdmin.value) return
  try {
    const res = await api('/firewall/rules').catch(() => ({ nodes: [] }))
    if (res?.nodes) {
      const map = {}
      for (const n of res.nodes) {
        if (n && n.node_id) {
          map[n.node_id] = Boolean(n.scanner_protection)
        }
      }
      scanners.value = map
    }
  } catch (_) {}
}

async function toggleScannerDirect(node) {
  const current = Boolean(scanners.value[node.id])
  togglingScanner[node.id] = true
  try {
    const updated = await post(`/nodes/${node.id}/firewall/scanners`, { enabled: !current })
    scanners.value[node.id] = Boolean(updated.scanner_protection)
    ElMessage.success(`“${node.name}”测绘防御已${!current ? '开启' : '关闭'}`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '切换失败')
  } finally {
    togglingScanner[node.id] = false
  }
}

async function runSpeedtest(node) {
  speedtesting[node.id] = true
  try {
    const res = await post(`/nodes/${node.id}/speedtest`, {})
    if (res?.result) {
      speedtests.value = { ...speedtests.value, [node.id]: res.result }
    }
    ElMessage.success(`“${node.name}”三网延迟与线路检测完成`)
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '线路检测执行失败')
  } finally {
    speedtesting[node.id] = false
  }
}

function getPingStr(val) {
  if (val == null || val <= 0) return '超时'
  return `${val}ms`
}

// The console host is only a guess at the address agents should dial: the
// console is often behind a reverse proxy on a different name than the raw
// Noise port, so the field stays editable and the command follows it.
const masterAddress = computed(() => {
  const host = masterHost.value.trim().replace(/[^A-Za-z0-9.:_[\]-]/g, '')
  return `${host}:${agentPort.value}`
})
// 改过一次就记住：下次生成令牌直接用上次填的主机名，不必重复修改。
watch(masterHost, (value) => {
  const host = value.trim()
  if (host) window.localStorage.setItem(MASTER_HOST_KEY, host)
})
const installCommand = computed(() => [
  'curl -fsSLo install.sh https://raw.githubusercontent.com/liyuwei007036/polaris/main/install.sh',
  ` && sudo env POLARIS_MASTER_ADDRESS='${masterAddress.value}'`,
  ` POLARIS_MASTER_PUBKEY='${masterPublicKey.value}'`,
  ` POLARIS_REGISTRATION_TOKEN='${token.value}'`,
  ' bash install.sh agent',
].join(''))

async function generateToken() {
  const result = await post('/nodes/registration-tokens', { lifetime_seconds: Number(lifetime.value) })
  token.value = result.token
  expiresAt.value = result.expires_at
  masterPublicKey.value = result.master_public_key || ''
  agentPort.value = result.agent_port || 19994
  masterHost.value = window.localStorage.getItem(MASTER_HOST_KEY) || window.location.hostname
  tokenDialog.value = true
}

async function copyInstallCommand() {
  await navigator.clipboard.writeText(installCommand.value)
  ElMessage.success('安装命令已复制')
}

async function approve(registration) {
  await ElMessageBox.confirm(`允许服务器“${registration.node_name}”接入管理平台？批准后即可远程管理该服务器。`, '确认服务器接入')
  await post(`/nodes/${registration.id}/approve`, {})
  ElMessage.success('服务器已接入')
  await load()
}

async function revoke(node) {
  await ElMessageBox.confirm(`移除“${node.name}”后，该服务器将无法继续连接管理平台。`, '移除服务器', {
    type: 'warning',
    confirmButtonText: '确认移除',
  })
  await post(`/nodes/${node.id}/revoke`, {})
  ElMessage.success('服务器已移除')
  await load()
}

function agentUpdateAvailable(node) {
  const latest = appState.systemUpdate?.latest_version
  return Boolean(latest && node.agent_version && node.agent_version !== latest)
}

async function upgradeAgent(node) {
  await ElMessageBox.confirm(
    `将“${node.name}”的 agent 升级到最新版本？升级完成后 agent 会自动重启并重新连接，运行中的代理服务不受影响。`,
    '升级 Agent',
    { type: 'warning', confirmButtonText: '开始升级' },
  )
  await post(`/nodes/${node.id}/agent/upgrade`, {})
  ElMessage.success('升级任务已下发，agent 更新完成后会自动重新上线')
}

// 同一架构的服务器共用一个官方版本号，按架构查一次即可；10分钟内不重复请求。
async function loadSingBoxLatest() {
  const now = Date.now()
  if (now - lastSingBoxCheckTime < 10 * 60 * 1000 && Object.keys(singBoxLatest.value).length > 0) {
    return
  }
  const architectures = [...new Set(appState.nodes.map((node) => node.architecture).filter(Boolean))]
  if (!architectures.length) return
  lastSingBoxCheckTime = now
  await Promise.all(architectures.map(async (architecture) => {
    if (singBoxLatest.value[architecture]) return
    const release = await api(`/sing-box/latest?architecture=${architecture}`).catch(() => null)
    if (release?.version) singBoxLatest.value[architecture] = release.version
  }))
}

function singBoxUpdateAvailable(node) {
  const latest = singBoxLatest.value[node.architecture]
  return Boolean(latest && node.sing_box_version && node.sing_box_version !== latest)
}

async function upgradeSingBox(node) {
  await ElMessageBox.confirm(
    `将“${node.name}”的 sing-box 从 ${node.sing_box_version} 升级到 ${singBoxLatest.value[node.architecture]}？升级时 sing-box 会重启，正在通过该服务器的连接会短暂中断；新版本启动失败会自动回滚到当前版本。`,
    '升级 sing-box',
    { type: 'warning', confirmButtonText: '开始升级' },
  )
  await post(`/nodes/${node.id}/sing-box/install`, {})
  ElMessage.success('升级任务已下发，完成后服务器会上报新的 sing-box 版本')
}

async function openEdit(node) {
  editNode.value = node
  let isProtected = Boolean(scanners.value[node.id])
  if (scanners.value[node.id] === undefined && node.online && isAdmin.value) {
    const res = await api(`/nodes/${node.id}/firewall/rules`).catch(() => null)
    if (res?.scanner_protection !== undefined) {
      isProtected = Boolean(res.scanner_protection)
      scanners.value[node.id] = isProtected
    }
  }
  editForm.value = {
    name: node.name,
    client_address: node.client_address || '',
    scanner_protection: isProtected,
  }
  editDialog.value = true
}

async function saveNode() {
  editSaving.value = true
  try {
    await put(`/nodes/${editNode.value.id}`, {
      name: editForm.value.name.trim(),
      client_address: editForm.value.client_address.trim(),
    })
    if (isAdmin.value && editNode.value.online && editForm.value.scanner_protection !== Boolean(scanners.value[editNode.value.id])) {
      const updated = await post(`/nodes/${editNode.value.id}/firewall/scanners`, { enabled: editForm.value.scanner_protection })
      scanners.value[editNode.value.id] = Boolean(updated.scanner_protection)
    }
    ElMessage.success('服务器信息已保存')
    editDialog.value = false
    await load()
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '服务器信息保存失败')
  } finally {
    editSaving.value = false
  }
}


function isValidRoute(route) {
  if (!route) return false
  const trimmed = route.trim()
  return trimmed !== '' && trimmed !== '未知' && trimmed !== '不可达'
}

function hasRecognizedRoutes(st) {
  if (!st) return false
  return isValidRoute(st.telecom_route) || isValidRoute(st.unicom_route) || isValidRoute(st.mobile_route)
}

onMounted(() => {
  load()
  stopConnections = subscribeConnections(() => {})
  stopLive = subscribeLive((event) => {
    if (event.kind === 'node') load(true).catch(() => {})
  })
})

onBeforeUnmount(() => {
  stopLive?.()
  stopConnections?.()
})
</script>

<template>
  <div class="page-shell">
    <PageHeader title="服务器">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
      <el-button v-if="isAdmin" type="primary" :icon="Plus" @click="generateToken">添加</el-button>
    </PageHeader>
    <main v-loading="loading" class="page-content page-content--tight">
      <div class="search-toolbar search-toolbar--standalone">
        <el-input v-model="keyword" clearable :prefix-icon="Search" placeholder="搜索名称、地址或版本" style="width: 280px" />
        <el-select v-model="statusFilter" clearable placeholder="全部状态" style="width: 150px"><el-option label="在线" value="online" /><el-option label="离线" value="offline" /></el-select>
      </div>
      <template v-if="pending.length">
        <div class="section-title">等待确认的服务器</div>
        <div class="table-panel table-panel--fixed" style="margin-bottom: 24px">
          <PagedTable :rows="filteredPending" :page-size="5" empty-text="没有等待确认的服务器">
            <el-table-column label="服务器名称" min-width="180" prop="node_name" />
            <el-table-column label="支持的功能" min-width="260" prop="capabilities" show-overflow-tooltip />
            <el-table-column label="接入时间" width="180"><template #default="{ row }">{{ formatDateTime(row.created_at) }}</template></el-table-column>
            <el-table-column label="操作" width="90" class-name="action-column">
              <template #default="{ row }"><el-button type="primary" link @click="approve(row)">接入</el-button></template>
            </el-table-column>
          </PagedTable>
        </div>
      </template>

      <div class="section-title">已接入服务器</div>
      <div class="table-panel">
        <PagedTable :rows="filteredNodes" empty-text="尚未接入任何服务器">
          <!-- 同一类信息合并成两行，整张表就能在常见宽度里放下，
               不必靠横向滚动去看被固定列挡住的列。 -->
          <el-table-column label="服务器 / 系统" min-width="190" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="cell-main">
                <span class="status-dot" :class="row.online ? 'online' : 'offline'" />
                <strong>{{ row.name }}</strong>
              </div>
              <div class="cell-sub">{{ row.os || '—' }} · {{ row.architecture || '—' }}</div>
            </template>
          </el-table-column>
          <el-table-column label="版本" width="190">
            <template #default="{ row }">
              <div class="cell-main">
                {{ row.agent_version || '—' }}
                <el-tag v-if="agentUpdateAvailable(row)" type="warning" size="small" style="margin-left: 6px">可升级</el-tag>
              </div>
              <!-- sing-box 只在服务器首次接入时自动装一次，之后要靠这里升级；
                   入口贴着版本号放，操作列就不必再挤进第四个按钮。 -->
              <div class="cell-sub">
                sing-box {{ row.sing_box_version || '—' }}
                <el-button v-if="isAdmin && singBoxUpdateAvailable(row)" link type="primary" style="margin-left: 6px" @click="upgradeSingBox(row)">升级</el-button>
                <el-tag v-else-if="singBoxUpdateAvailable(row)" type="warning" size="small" style="margin-left: 6px">可升级</el-tag>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="客户端连接地址" min-width="170" show-overflow-tooltip>
            <template #default="{ row }"><span v-if="row.client_address" class="mono">{{ row.client_address }}</span><el-tag v-else type="warning">未配置</el-tag></template>
          </el-table-column>
          <el-table-column label="主动测绘防御" width="130" align="center">
            <template #default="{ row }">
              <el-tooltip :content="scanners[row.id] ? '已阻断 Shodan、Censys 等公网扫描引擎探测' : '未开启测绘防御，点击快速开启'">
                <el-switch
                  :model-value="Boolean(scanners[row.id])"
                  :loading="Boolean(togglingScanner[row.id])"
                  :disabled="!isAdmin || !row.online"
                  active-text="已开启"
                  inactive-text="未开启"
                  inline-prompt
                  @change="toggleScannerDirect(row)"
                />
              </el-tooltip>
            </template>
          </el-table-column>
          <el-table-column label="三网延迟 / 线路" min-width="260">
            <template #default="{ row }">
              <template v-if="speedtests[row.id]">
                <div class="cell-main speedtest-badges">
                  <el-tooltip :content="'中国移动: ' + (speedtests[row.id].mobile_route || '标准直连')">
                    <span class="ping-badge ping-mobile">移 {{ getPingStr(speedtests[row.id].mobile_latency_ms ?? speedtests[row.id].mobile_ping_ms) }}</span>
                  </el-tooltip>
                  <el-tooltip :content="'中国联通: ' + (speedtests[row.id].unicom_route || '标准直连')">
                    <span class="ping-badge ping-unicom">联 {{ getPingStr(speedtests[row.id].unicom_latency_ms ?? speedtests[row.id].unicom_ping_ms) }}</span>
                  </el-tooltip>
                  <el-tooltip :content="'中国电信: ' + (speedtests[row.id].telecom_route || '标准直连')">
                    <span class="ping-badge ping-telecom">电 {{ getPingStr(speedtests[row.id].telecom_latency_ms ?? speedtests[row.id].telecom_ping_ms) }}</span>
                  </el-tooltip>
                </div>
                <div v-if="hasRecognizedRoutes(speedtests[row.id])" class="route-tags">
                  <span v-if="isValidRoute(speedtests[row.id].telecom_route)" :class="['route-tag', speedtests[row.id].telecom_route.includes('CN2') ? 'route-premium' : 'route-normal']">
                    {{ speedtests[row.id].telecom_route }}
                  </span>
                  <span v-if="isValidRoute(speedtests[row.id].unicom_route)" :class="['route-tag', speedtests[row.id].unicom_route.includes('9929') || speedtests[row.id].unicom_route.includes('CN2') ? 'route-premium' : 'route-normal']">
                    {{ speedtests[row.id].unicom_route }}
                  </span>
                  <span v-if="isValidRoute(speedtests[row.id].mobile_route)" :class="['route-tag', speedtests[row.id].mobile_route.includes('CMIN2') || speedtests[row.id].mobile_route.includes('CN2') ? 'route-premium' : 'route-normal']">
                    {{ speedtests[row.id].mobile_route }}
                  </span>
                </div>
              </template>
              <span v-else class="subtle">未检测</span>
            </template>
          </el-table-column>
          <el-table-column label="实时 / 累计流量" min-width="196" show-overflow-tooltip>
            <template #default="{ row }">
              <div class="cell-main mono">
                <template v-if="live.get(row.id)?.has_rates">↓ {{ formatBytes(live.get(row.id).received_rate, '/s') }} · ↑ {{ formatBytes(live.get(row.id).sent_rate, '/s') }}</template>
                <span v-else class="subtle">{{ row.online ? '等待上报' : '离线' }}</span>
              </div>
              <div class="cell-sub mono">
                <template v-if="metrics[row.id]?.proxy">↓ {{ formatBytes(metrics[row.id].proxy.received_bytes) }} · ↑ {{ formatBytes(metrics[row.id].proxy.sent_bytes) }}</template>
                <template v-else>等待上报</template>
              </div>
            </template>
          </el-table-column>
          <el-table-column label="连接数" width="82" align="center">
            <template #default="{ row }">{{ live.get(row.id)?.connection_count ?? '—' }}</template>
          </el-table-column>
          <el-table-column label="最后在线" width="152"><template #default="{ row }">{{ formatDateTime(row.last_seen_at, '从未') }}</template></el-table-column>
          <el-table-column label="操作" width="240" fixed="right" class-name="action-column">
            <template #default="{ row }">
              <el-button v-if="canWrite && row.online" link :icon="Connection" :loading="Boolean(speedtesting[row.id])" @click="runSpeedtest(row)">线路检测</el-button>
              <el-button v-if="canWrite" link :icon="Edit" @click="openEdit(row)">编辑</el-button>
              <el-button v-if="isAdmin && agentUpdateAvailable(row)" link type="primary" :icon="Top" @click="upgradeAgent(row)">升级</el-button>
              <el-button v-if="isAdmin" link type="danger" :icon="RemoveFilled" @click="revoke(row)">移除</el-button>
            </template>
          </el-table-column>
        </PagedTable>
      </div>
    </main>

    <el-dialog v-model="tokenDialog" title="服务器接入信息" width="720px">
      <el-alert title="此命令只显示一次，请立即复制。令牌一次性使用，过期后需重新生成。" type="warning" show-icon :closable="false" />
      <el-form label-position="top" style="margin-top: 16px">
        <el-form-item label="Master 地址（agent 拨号用的 主机:端口）">
          <el-input v-model="masterHost" placeholder="control.example.com">
            <template #append>:{{ agentPort }}</template>
          </el-input>
          <p class="subtle">默认按当前控制台域名填入。若 agent 需通过其他域名或公网 IP 连接，请在此改成正确的主机名，本浏览器会记住该地址，下次生成令牌自动带入。</p>
        </el-form-item>
        <el-form-item label="在目标服务器上以 root 执行">
          <el-input :model-value="installCommand" readonly type="textarea" :rows="6" class="mono" />
        </el-form-item>
      </el-form>
      <p class="subtle">令牌有效期至：{{ formatDateTime(expiresAt) }}。命令执行后该服务器会出现在“等待确认的服务器”，需在此页面点“接入”批准。</p>
      <template #footer>
        <el-button @click="tokenDialog = false">完成</el-button>
        <el-button type="primary" :icon="CopyDocument" @click="copyInstallCommand">复制命令</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="editDialog" title="编辑服务器" width="540px">
      <el-form label-position="top">
        <el-form-item label="服务器名称" required>
          <el-input v-model="editForm.name" maxlength="128" placeholder="例如：香港节点 01" />
        </el-form-item>
        <el-form-item label="客户端连接域名或 IP 地址">
          <el-input v-model="editForm.client_address" placeholder="例如：proxy.example.com 或 203.0.113.10" @keyup.enter="saveNode" />
        </el-form-item>
        <el-form-item label="主动网络测绘防御">
          <div class="scanner-edit-box">
            <div class="scanner-edit-text">
              <div class="scanner-edit-heading">阻断公网扫描与特征探测</div>
              <div class="subtle">自动屏蔽 Shodan、Censys 等公网搜索引擎的测绘扫描，保护代理端口不被嗅探发现</div>
            </div>
            <el-switch
              v-model="editForm.scanner_protection"
              :disabled="!isAdmin || !editNode?.online"
              active-text="已开启"
              inactive-text="未开启"
              inline-prompt
            />
          </div>
        </el-form-item>
        <el-alert title="接入时会自动填入来源 IP。仅填域名或 IP，不含 http://、端口与路径。" type="info" show-icon :closable="false" />
      </el-form>
      <template #footer>
        <el-button @click="editDialog = false">取消</el-button>
        <el-button type="primary" :loading="editSaving" :disabled="!editForm.name.trim()" @click="saveNode">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.scanner-edit-box {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 10px 14px;
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm);
  background: var(--sb-surface-muted, rgba(148, 163, 184, 0.05));
  box-sizing: border-box;
}
.scanner-edit-text {
  flex: 1;
  margin-right: 16px;
}
.scanner-edit-heading {
  font-weight: 600;
  color: var(--sb-text-1);
  margin-bottom: 2px;
}
.speedtest-badges {
  display: flex;
  gap: 5px;
  font-size: 11px;
  margin-bottom: 2px;
}
.ping-badge {
  padding: 1px 5px;
  border-radius: 4px;
  font-family: var(--sb-font-mono, monospace);
  font-weight: 500;
  white-space: nowrap;
}
.ping-mobile {
  background: rgba(16, 185, 129, 0.12);
  color: #059669;
  border: 1px solid rgba(16, 185, 129, 0.25);
}
.ping-unicom {
  background: rgba(245, 158, 11, 0.12);
  color: #d97706;
  border: 1px solid rgba(245, 158, 11, 0.25);
}
.ping-telecom {
  background: rgba(59, 130, 246, 0.12);
  color: #2563eb;
  border: 1px solid rgba(59, 130, 246, 0.25);
}
.route-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin: 3px 0 2px;
}
.route-tag {
  font-size: 10px;
  padding: 1px 5px;
  border-radius: 3px;
  line-height: 14px;
  font-weight: 600;
  white-space: nowrap;
}
.route-premium {
  background: rgba(245, 158, 11, 0.15);
  color: #fbbf24;
  border: 1px solid rgba(245, 158, 11, 0.35);
}
.route-normal {
  background: rgba(148, 163, 184, 0.12);
  color: #94a3b8;
  border: 1px solid rgba(148, 163, 184, 0.25);
}
</style>

