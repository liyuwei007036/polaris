<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  CircleClose,
  Connection,
  Download,
  Odometer,
  Plus,
  Refresh,
  Search,
  Timer,
  TrendCharts,
  Upload,
} from '@element-plus/icons-vue'
import { api, post, put, uploadFile } from '../api'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('operators')
const operators = ref([])
const dialog = ref('')
const operator = reactive({ username: '', password: '', role: 'operator' })
const operatorKeyword = ref('')
const operatorStatus = ref('')

const alertSettings = reactive({
  bark_server: 'https://api.day.app',
  bark_device_key: '',
  bark_sound: 'minuet.caf',
  bark_group: 'Polaris',
  bark_url: '',
  traffic_alert_enabled: true,
  traffic_threshold_mbps: 150,
  traffic_duration_sec: 10,
  conn_alert_enabled: true,
  conn_threshold_count: 200,
  single_ip_alert_enabled: true,
  single_ip_threshold_count: 60,
  // 定时探测与节点阻断已下线，以下字段仅为兼容后端设置结构而保留，保存时原样回传。
  gfw_alert_enabled: true,
  offline_alert_enabled: true,
  cooldown_minutes: 15,
  auto_probe_gfw_enabled: true,
  probe_gfw_interval_minutes: 30,
  auto_probe_speedtest_enabled: true,
  probe_speedtest_interval_hours: 6,
  probe_skip_when_busy: true,
  probe_notify_always: true,
  scan_alert_enabled: false,
})
const alertSaving = ref(false)
const testSending = ref(false)
const realityForm = reactive({ target: 'gateway.icloud.com', port: 443 })
const realityChecking = ref(false)
const realityResult = ref(null)

// ── 通知设置：未保存状态与通道摘要 ──────────────────────────
const soundOptions = [
  { value: 'minuet.caf', label: 'minuet (清脆小步舞曲 - 推荐)' },
  { value: 'anticipate.caf', label: 'anticipate (轻快期待)' },
  { value: 'bell.caf', label: 'bell (经典铃声)' },
  { value: 'glass.caf', label: 'glass (清澈水滴)' },
  { value: 'horn.caf', label: 'horn (警示号角)' },
  { value: 'calypso.caf', label: 'calypso (欢快击鼓)' },
  { value: 'telegraph.caf', label: 'telegraph (电报码)' },
  { value: 'silence.caf', label: 'silence (静音仅震动)' },
]

const savedState = ref(null)
const barkReady = computed(() => Boolean(alertSettings.bark_device_key.trim()))
const enabledRuleCount = computed(() => [
  alertSettings.traffic_alert_enabled,
  alertSettings.conn_alert_enabled,
  alertSettings.single_ip_alert_enabled,
  alertSettings.offline_alert_enabled,
  alertSettings.scan_alert_enabled,
].filter(Boolean).length)

// 用快照判断是否有未保存的修改，避免用户误以为改动已经生效。
function alertSnapshot() {
  const data = {}
  for (const [key, value] of Object.entries(alertSettings)) {
    if (key !== 'updated_at') data[key] = value
  }
  return JSON.stringify(data)
}
const isAlertDirty = computed(() => savedState.value !== null && alertSnapshot() !== savedState.value)
function markAlertSaved() {
  savedState.value = alertSnapshot()
}
function revertAlertSettings() {
  if (!savedState.value) return
  Object.assign(alertSettings, JSON.parse(savedState.value))
  ElMessage.info('已撤销未保存的修改')
}

const filteredOperators = computed(() => operators.value.filter((row) => {
  if (operatorStatus.value && String(row.enabled) !== operatorStatus.value) return false
  return includesText([row.username, row.role], operatorKeyword.value)
}))

const ipdbStatus = reactive({
  mmdb_loaded: false,
  mmdb_path: '',
  mmdb_type: '',
  ip2region_loaded: true,
})
const ipdbLoading = ref(false)
const ipDownloading = ref(false)
const ipReloading = ref(false)
const ipTestInput = ref('1.1.1.1')
const ipTestResult = ref(null)
const ipTesting = ref(false)
const mmdbUploadInput = ref(null)

async function loadIPDBStatus() {
  if (!isAdmin.value) return
  ipdbLoading.value = true
  try {
    const res = await api('/ip/database')
    Object.assign(ipdbStatus, res)
  } catch {
    // ignore
  } finally {
    ipdbLoading.value = false
  }
}

async function reloadIPDB() {
  ipReloading.value = true
  try {
    const res = await post('/ip/database/reload')
    if (res?.success) {
      Object.assign(ipdbStatus, res)
      ElMessage.success('IP 离线库重载完成')
    } else {
      ElMessage.warning(res?.error || '未找到新的 MMDB 离线库')
    }
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '重载失败')
  } finally {
    ipReloading.value = false
  }
}

async function downloadIPDB() {
  try {
    await ElMessageBox.confirm('确定要从高精度开源镜像源下载最新 GeoLite2-City.mmdb 数据库吗？下载通常需要数十秒。', '下载高精度 IP 库', { type: 'info' })
  } catch {
    return
  }
  ipDownloading.value = true
  try {
    const res = await post('/ip/database/download', {})
    if (res?.success) {
      Object.assign(ipdbStatus, res)
      ElMessage.success('高精度 MMDB 离线库下载并加载成功！')
    } else {
      ElMessage.error(res?.error || '下载失败')
    }
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '下载失败，请检查服务器网络或手动上传')
  } finally {
    ipDownloading.value = false
  }
}

function triggerUploadMMDB() {
  mmdbUploadInput.value?.click()
}

async function onMMDBFileSelected(event) {
  const file = event.target.files?.[0]
  if (!file) return
  const formData = new FormData()
  formData.append('file', file)
  ipdbLoading.value = true
  try {
    const res = await uploadFile('/ip/database/upload', formData)
    if (res?.success) {
      Object.assign(ipdbStatus, res)
      ElMessage.success('MMDB 数据库上传并成功生效！')
    }
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '上传失败')
  } finally {
    ipdbLoading.value = false
    event.target.value = ''
  }
}

async function testIPLookup() {
  const ip = ipTestInput.value.trim()
  if (!ip) {
    return ElMessage.warning('请输入待测试的 IP 地址')
  }
  ipTesting.value = true
  try {
    const res = await api(`/ip/lookup?ip=${encodeURIComponent(ip)}`)
    ipTestResult.value = res
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '查询失败')
  } finally {
    ipTesting.value = false
  }
}

async function load() {
  loading.value = true
  try {
    const [operatorResult, alertsResult] = await Promise.all([
      isAdmin.value ? api('/operators').catch(() => ({ operators: [] })) : Promise.resolve({ operators: [] }),
      isAdmin.value ? api('/alerts/settings').catch(() => ({ settings: null })) : Promise.resolve({ settings: null }),
      loadIPDBStatus(),
    ])
    operators.value = operatorResult.operators || []
    if (alertsResult?.settings) {
      Object.assign(alertSettings, alertsResult.settings)
      if (alertSettings.bark_sound && !alertSettings.bark_sound.endsWith('.caf')) {
        alertSettings.bark_sound = alertSettings.bark_sound + '.caf'
      }
    }
    markAlertSaved()
  } finally { loading.value = false }
}

async function saveAlertSettings(silent = false) {
  alertSaving.value = true
  try {
    const res = await put('/alerts/settings', alertSettings)
    if (res?.settings) {
      Object.assign(alertSettings, res.settings)
      if (alertSettings.bark_sound && !alertSettings.bark_sound.endsWith('.caf')) {
        alertSettings.bark_sound = alertSettings.bark_sound + '.caf'
      }
    }
    markAlertSaved()
    if (!silent) ElMessage.success('通知与探测配置已保存')
    return true
  } catch (error) {
    if (!silent) ElMessage.error(error instanceof Error ? error.message : '保存失败')
    return false
  } finally {
    alertSaving.value = false
  }
}

async function sendTestAlert() {
  if (!alertSettings.bark_device_key.trim()) {
    return ElMessage.warning('请先填写 Bark Device Key')
  }
  testSending.value = true
  try {
    // 先保存尚未落盘的修改，避免测试用的是旧配置。
    if (isAlertDirty.value) {
      const saved = await saveAlertSettings(true)
      if (!saved) return
    }
    await post('/alerts/test', {
      server: alertSettings.bark_server,
      device_key: alertSettings.bark_device_key,
      sound: alertSettings.bark_sound,
      group: alertSettings.bark_group,
      url: alertSettings.bark_url,
    })
    ElMessage.success('测试通知已发出，请查看 Bark 客户端')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '发送测试通知失败')
  } finally {
    testSending.value = false
  }
}

async function checkReality() {
  if (!realityForm.target.trim()) {
    return ElMessage.warning('请输入待检测的目标域名')
  }
  realityChecking.value = true
  try {
    const res = await post('/tools/reality-check', {
      target: realityForm.target.trim(),
      port: Number(realityForm.port) || 443,
    })
    realityResult.value = res.reality_check
    ElMessage.success('目标检测完成')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : 'Reality 目标检测失败')
  } finally {
    realityChecking.value = false
  }
}

function open(kind) {
  dialog.value = kind
  if (kind === 'operator') Object.assign(operator, { username: '', password: '', role: 'operator' })
}

async function saveOperator() {
  await post('/operators', operator)
  dialog.value = ''
  ElMessage.success('账户已创建，用户首次登录时需要修改初始密码')
  await load()
}

async function setOperatorState(row) {
  await put(`/operators/${row.id}`, { role: row.role, enabled: !row.enabled })
  await load()
}

async function resetPassword(row) {
  const result = await ElMessageBox.prompt(`为 ${row.username} 设置新的初始密码`, '重置密码', {
    inputType: 'password',
    inputValidator: (value) => value.length >= 12 || '密码至少 12 位',
  })
  await post(`/operators/${row.id}/password`, { password: result.value })
  ElMessage.success('密码已重置，用户下次登录时需要修改密码')
  await load()
}

async function disableOperatorTOTP(row) {
  await ElMessageBox.confirm(`关闭 ${row.username} 的两步验证？`, '关闭两步验证', { type: 'warning' })
  await post(`/operators/${row.id}/totp/reset`, {})
  ElMessage.success('两步验证已关闭')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-shell">
    <PageHeader title="系统设置">
      <el-button :icon="Refresh" @click="load">刷新</el-button>
    </PageHeader>
    <main class="page-content">
      <div class="table-panel">
        <el-tabs v-if="isAdmin" v-model="tab" class="panel-tabs">
          <el-tab-pane label="管理账户" name="operators">
            <div class="tab-actions tab-actions--start"><el-input v-model="operatorKeyword" clearable :prefix-icon="Search" placeholder="搜索用户名或权限" style="width: 250px" /><el-select v-model="operatorStatus" clearable placeholder="全部状态" style="width: 140px"><el-option label="启用" value="true" /><el-option label="停用" value="false" /></el-select><span class="toolbar__spacer" /><el-button type="primary" :icon="Plus" @click="open('operator')">新建</el-button></div>
            <PagedTable :rows="filteredOperators" :loading="loading" empty-text="还没有管理账户">
              <el-table-column label="用户名" prop="username" min-width="180" />
              <el-table-column label="权限" width="130"><template #default="{ row }"><el-select v-model="row.role" size="small" @change="put(`/operators/${row.id}`, { role: row.role, enabled: row.enabled })"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></template></el-table-column>
              <el-table-column label="登录安全" width="150"><template #default="{ row }"><el-tag v-if="row.must_change_password" type="warning">等待修改密码</el-tag><el-tag v-else-if="row.totp_enabled" type="success">两步验证已启用</el-tag><el-tag v-else type="info">密码登录</el-tag></template></el-table-column>
              <el-table-column label="状态" width="100"><template #default="{ row }"><el-tag :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '启用' : '停用' }}</el-tag></template></el-table-column>
              <el-table-column label="最近登录" min-width="180"><template #default="{ row }">{{ formatDateTime(row.last_login_at) }}</template></el-table-column>
              <el-table-column label="操作" width="220" class-name="action-column"><template #default="{ row }"><el-button link @click="setOperatorState(row)">{{ row.enabled ? '停用' : '启用' }}</el-button><el-button link @click="resetPassword(row)">改密</el-button><el-button v-if="row.totp_enabled" link @click="disableOperatorTOTP(row)">关 2FA</el-button></template></el-table-column>
            </PagedTable>
          </el-tab-pane>

          <el-tab-pane v-if="isAdmin" label="通知设置" name="alerts">
            <div class="notify-page">
              <header class="notify-bar">
                <div class="notify-bar__status" :class="{ 'is-ready': barkReady }">
                  <span class="notify-bar__dot"></span>
                  <span>{{ barkReady ? 'Bark 通道已就绪' : '尚未填写 Device Key' }}</span>
                  <span class="notify-bar__sep"></span>
                  <span>{{ enabledRuleCount }} / 5 项告警启用</span>
                </div>
                <div class="notify-bar__actions">
                  <span v-if="isAlertDirty" class="notify-unsaved">未保存</span>
                  <el-button v-if="isAlertDirty" text @click="revertAlertSettings">撤销</el-button>
                  <el-button :loading="testSending" :disabled="!barkReady" @click="sendTestAlert">发送测试</el-button>
                  <el-button type="primary" :disabled="!isAlertDirty" :loading="alertSaving" @click="saveAlertSettings()">保存</el-button>
                </div>
              </header>

              <div class="notify-scroll">
                <section class="notify-section">
                  <div class="notify-section__head">
                    <h3>Bark 推送</h3>
                    <p>用于发送测试与告警通知。</p>
                  </div>
                  <div class="notify-card">
                    <div class="notify-fields">
                      <label class="notify-field">
                        <span class="notify-field__label">服务器地址</span>
                        <el-input v-model="alertSettings.bark_server" placeholder="https://api.day.app" clearable />
                        <span class="notify-field__hint">使用官方 Bark 服务保持默认；自建服务填写完整地址。</span>
                      </label>
                      <label class="notify-field">
                        <span class="notify-field__label">Device Key <em>*</em></span>
                        <el-input v-model="alertSettings.bark_device_key" show-password placeholder="Bark App 首页显示的专属 Key" clearable />
                        <span class="notify-field__hint">在手机 Bark App 首页复制。</span>
                      </label>
                      <label class="notify-field">
                        <span class="notify-field__label">推送分组</span>
                        <el-input v-model="alertSettings.bark_group" placeholder="Polaris" clearable />
                        <span class="notify-field__hint">不同分组的通知会在 Bark 中分别归档。</span>
                      </label>
                      <label class="notify-field">
                        <span class="notify-field__label">点击跳转</span>
                        <el-input v-model="alertSettings.bark_url" placeholder="https://控制台地址" clearable />
                        <span class="notify-field__hint">点击通知时打开的地址，可留空。</span>
                      </label>
                      <label class="notify-field notify-field--full">
                        <span class="notify-field__label">默认铃声</span>
                        <el-select v-model="alertSettings.bark_sound" style="width: 100%">
                          <el-option v-for="option in soundOptions" :key="option.value" :label="option.label" :value="option.value" />
                        </el-select>
                        <span class="notify-field__hint">不同类型的告警会使用专属音效（bell、horn、alarm 等）。</span>
                      </label>
                    </div>
                  </div>
                </section>

                <section class="notify-section">
                  <div class="notify-section__head">
                    <h3>告警规则</h3>
                    <p>达到条件时通过 Bark 推送。</p>
                  </div>
                  <div class="notify-card">
                    <div class="rule-list">
                      <div class="rule-row" :class="{ 'is-off': !alertSettings.traffic_alert_enabled }">
                        <span class="rule-row__icon"><el-icon><TrendCharts /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">瞬时流量突发</span>
                          <span class="rule-row__desc">带宽超过阈值并持续到判定窗口</span>
                        </div>
                        <div class="rule-row__fields">
                          <label class="rule-row__field"><span>阈值</span><el-input-number v-model="alertSettings.traffic_threshold_mbps" :min="0" :max="10000" :step="10" controls-position="right" /><span>Mbps</span></label>
                          <label class="rule-row__field"><span>窗口</span><el-input-number v-model="alertSettings.traffic_duration_sec" :min="5" :max="300" :step="5" controls-position="right" /><span>秒</span></label>
                        </div>
                        <el-switch v-model="alertSettings.traffic_alert_enabled" />
                      </div>

                      <div class="rule-row" :class="{ 'is-off': !alertSettings.conn_alert_enabled }">
                        <span class="rule-row__icon"><el-icon><Connection /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">连接数激增</span>
                          <span class="rule-row__desc">单节点活跃连接数超过上限</span>
                        </div>
                        <div class="rule-row__fields">
                          <label class="rule-row__field"><span>上限</span><el-input-number v-model="alertSettings.conn_threshold_count" :min="10" :max="50000" :step="50" controls-position="right" /><span>条</span></label>
                        </div>
                        <el-switch v-model="alertSettings.conn_alert_enabled" />
                      </div>

                      <div class="rule-row" :class="{ 'is-off': !alertSettings.single_ip_alert_enabled }">
                        <span class="rule-row__icon"><el-icon><Odometer /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">单一 IP 高并发</span>
                          <span class="rule-row__desc">同一来源 IP 的并发连接超过上限</span>
                        </div>
                        <div class="rule-row__fields">
                          <label class="rule-row__field"><span>单 IP 上限</span><el-input-number v-model="alertSettings.single_ip_threshold_count" :min="5" :max="5000" :step="10" controls-position="right" /><span>条</span></label>
                        </div>
                        <el-switch v-model="alertSettings.single_ip_alert_enabled" />
                      </div>

                      <div class="rule-row" :class="{ 'is-off': !alertSettings.offline_alert_enabled }">
                        <span class="rule-row__icon"><el-icon><CircleClose /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">节点离线</span>
                          <span class="rule-row__desc">节点与控制端失联时提醒，恢复后同步通知</span>
                        </div>
                        <div class="rule-row__fields"></div>
                        <el-switch v-model="alertSettings.offline_alert_enabled" />
                      </div>

                      <div class="rule-row" :class="{ 'is-off': !alertSettings.scan_alert_enabled }">
                        <span class="rule-row__icon"><el-icon><Search /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">异常网络扫描</span>
                          <span class="rule-row__desc">多端口嗅探与 SSH、RDP、Redis 等高危探测</span>
                        </div>
                        <div class="rule-row__fields"></div>
                        <el-switch v-model="alertSettings.scan_alert_enabled" />
                      </div>

                      <div class="rule-row">
                        <span class="rule-row__icon"><el-icon><Timer /></el-icon></span>
                        <div class="rule-row__text">
                          <span class="rule-row__title">重复报警冷却</span>
                          <span class="rule-row__desc">同类告警在冷却期内只推送一次</span>
                        </div>
                        <div class="rule-row__fields">
                          <label class="rule-row__field"><span>冷却时长</span><el-input-number v-model="alertSettings.cooldown_minutes" :min="1" :max="1440" :step="5" controls-position="right" /><span>分钟</span></label>
                        </div>
                      </div>
                    </div>
                  </div>
                </section>
              </div>
            </div>
          </el-tab-pane>

          <el-tab-pane v-if="isAdmin" label="网络工具" name="tools">
            <div class="tools-scroll">
              <section class="notify-section">
                <div class="notify-section__head">
                  <h3>Reality 域名检测</h3>
                  <p>检查目标域名是否支持 TLS 1.3、h2 ALPN 及有效证书。</p>
                </div>
                <div class="notify-card">
                  <div class="reality-form">
                    <el-form label-position="top" @submit.prevent="checkReality">
                      <el-row :gutter="14" align="bottom">
                        <el-col :xs="24" :sm="14">
                          <el-form-item label="目标域名">
                            <el-input v-model="realityForm.target" placeholder="gateway.icloud.com" clearable @keyup.enter="checkReality" />
                          </el-form-item>
                        </el-col>
                        <el-col :xs="12" :sm="5">
                          <el-form-item label="端口">
                            <el-input-number v-model="realityForm.port" :min="1" :max="65535" style="width: 100%" />
                          </el-form-item>
                        </el-col>
                        <el-col :xs="12" :sm="5">
                          <el-form-item label="&nbsp;">
                            <el-button type="primary" style="width: 100%" :loading="realityChecking" @click="checkReality">开始检测</el-button>
                          </el-form-item>
                        </el-col>
                      </el-row>
                    </el-form>
                  </div>

                  <div v-if="realityResult" class="reality-result-panel">
                    <div class="result-header">
                      <div class="result-title">
                        <strong>{{ realityResult.target }}</strong>
                        <el-tag :type="realityResult.compatible ? 'success' : 'danger'" size="small" effect="plain">
                          {{ realityResult.compatible ? '可用' : '不可用' }}
                        </el-tag>
                      </div>
                      <span class="result-latency">{{ realityResult.latency_ms }} ms</span>
                    </div>
                    <div class="result-grid">
                      <div class="result-item">
                        <span class="label">TLS 1.3</span>
                        <el-tag :type="realityResult.tls_13 ? 'success' : 'danger'" size="small" effect="plain">{{ realityResult.tls_13 ? '支持' : '不支持' }}</el-tag>
                      </div>
                      <div class="result-item">
                        <span class="label">ALPN</span>
                        <el-tag :type="realityResult.alpn?.includes('h2') ? 'success' : 'warning'" size="small" effect="plain">{{ realityResult.alpn || '无' }}</el-tag>
                      </div>
                      <div class="result-item">
                        <span class="label">证书颁发者</span>
                        <span class="mono">{{ realityResult.cert_issuer || '—' }}</span>
                      </div>
                      <div class="result-item">
                        <span class="label">剩余有效期</span>
                        <span>{{ realityResult.cert_days_left }} 天</span>
                      </div>
                    </div>
                    <el-alert v-if="realityResult.recommendation" :title="realityResult.recommendation" :type="realityResult.compatible ? 'success' : 'warning'" show-icon :closable="false" style="margin-top: 12px" />
                  </div>
                </div>
              </section>
            </div>
          </el-tab-pane>

          <el-tab-pane v-if="isAdmin" label="高精度 IP 库 (MMDB)" name="ipdb">
            <div class="tools-scroll">
              <input
                ref="mmdbUploadInput"
                type="file"
                accept=".mmdb"
                style="display: none"
                @change="onMMDBFileSelected"
              />

              <section class="notify-section">
                <div class="notify-section__head">
                  <h3>IP 地理位置库</h3>
                  <p>用于识别 IP 归属地、机房、ASN 与 Anycast 广播。</p>
                </div>
                <div class="notify-card">
                  <div class="info-list">
                    <div class="info-row">
                      <span class="info-row__label">引擎状态</span>
                      <el-tag :type="ipdbStatus.mmdb_loaded ? 'success' : 'warning'" size="small" effect="plain">
                        {{ ipdbStatus.mmdb_loaded ? 'MMDB 已加载' : '内置离线库' }}
                      </el-tag>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">数据库类型</span>
                      <span class="info-row__value mono">{{ ipdbStatus.mmdb_type || '—' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">文件路径</span>
                      <span class="info-row__value mono" :title="ipdbStatus.mmdb_path || ''">{{ ipdbStatus.mmdb_path || '未配置' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">内置引擎</span>
                      <span class="info-row__value">ip2region · 国内省市与运营商</span>
                    </div>
                  </div>
                  <div class="ipdb-actions">
                    <el-button type="primary" :icon="Download" :loading="ipDownloading" @click="downloadIPDB">下载更新</el-button>
                    <el-button :icon="Upload" :loading="ipdbLoading" @click="triggerUploadMMDB">上传 MMDB</el-button>
                    <el-button :icon="Refresh" :loading="ipReloading" @click="reloadIPDB">重新加载</el-button>
                  </div>
                  <p class="ipdb-tip">启动时自动获取离线库，也可上传商业版数据库覆盖。</p>
                </div>
              </section>

              <section class="notify-section">
                <div class="notify-section__head">
                  <h3>IP 归属地查询</h3>
                  <p>验证系统识别出的落地城市、运营商与 ASN。</p>
                </div>
                <div class="notify-card">
                  <div class="ipdb-query">
                    <el-input
                      v-model="ipTestInput"
                      placeholder="输入 IPv4 / IPv6 地址，如 1.1.1.1"
                      clearable
                      @keyup.enter="testIPLookup"
                    />
                    <el-button type="primary" :loading="ipTesting" @click="testIPLookup">查询</el-button>
                  </div>

                  <div v-if="ipTestResult" class="info-list info-list--bordered">
                    <div class="info-row">
                      <span class="info-row__label">IP</span>
                      <span class="info-row__value mono">{{ ipTestResult.ip }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">归属地</span>
                      <span class="info-row__value">{{ ipTestResult.location || '未知' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">城市</span>
                      <span class="info-row__value">{{ ipTestResult.city || '—' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">国家 / 地区</span>
                      <span class="info-row__value">{{ ipTestResult.country || '—' }} ({{ ipTestResult.country_code || '—' }})</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">运营商</span>
                      <span class="info-row__value">{{ ipTestResult.isp || '—' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">ASN</span>
                      <span class="info-row__value mono">{{ ipTestResult.asn || '—' }}</span>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">数据来源</span>
                      <el-tag size="small" effect="plain" :type="ipTestResult.source === 'mmdb' ? 'success' : 'info'">
                        {{ ipTestResult.source === 'mmdb' ? 'MMDB' : (ipTestResult.source === 'ip2region' ? 'ip2region' : ipTestResult.source) }}
                      </el-tag>
                    </div>
                    <div class="info-row">
                      <span class="info-row__label">广播类型</span>
                      <el-tag size="small" effect="plain" :type="ipTestResult.is_anycast ? 'warning' : 'info'">
                        {{ ipTestResult.is_anycast ? 'Anycast' : '单播' }}
                      </el-tag>
                    </div>
                  </div>
                </div>
              </section>
            </div>
          </el-tab-pane>
        </el-tabs>
        <section v-else class="security-card">
          <div>
            <h3>系统设置仅管理员可访问</h3>
            <p>当前账户的两步验证请在左下角账户菜单中管理。</p>
          </div>
        </section>
      </div>
    </main>

    <el-dialog :model-value="dialog === 'operator'" title="新建管理账户" width="540px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="用户名"><el-input v-model="operator.username" placeholder="3 至 64 位，可使用字母、数字、点、下划线和短横线" /></el-form-item><el-form-item label="初始密码"><el-input v-model="operator.password" type="password" show-password /><div class="form-tip">用户首次登录时必须修改此密码。</div></el-form-item><el-form-item label="权限"><el-select v-model="operator.role" style="width: 100%"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!operator.username || operator.password.length < 12" @click="saveOperator">创建</el-button></template></el-dialog>
  </div>
</template>

<style scoped>
.security-card {
  display: flex;
  align-items: center;
  gap: 18px;
  margin: 18px;
  padding: 22px;
  background: rgba(148, 163, 184, .05);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
}
.security-card > div { flex: 1; }
.security-card h3 { margin: 0 0 7px; color: var(--sb-text); font-size: 15px; font-weight: 620; }
.security-card p { margin: 0; color: var(--sb-muted); line-height: 1.7; }
.form-tip { margin-top: 6px; color: var(--sb-muted); font-size: 12px; }

:deep(.el-tabs__content) {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
:deep(.el-tab-pane) {
  height: 100%;
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
}

/* ── 通知设置 ───────────────────────────────────────────── */
.notify-page {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.notify-bar {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 22px;
  border-bottom: 1px solid var(--sb-line);
  background: color-mix(in oklab, var(--sb-surface) 82%, transparent);
  backdrop-filter: blur(12px);
}
.notify-bar__status {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  color: var(--sb-muted);
  font-size: 12px;
}
.notify-bar__dot {
  width: 7px;
  height: 7px;
  flex: none;
  background: var(--sb-muted);
  border-radius: 50%;
}
.notify-bar__status.is-ready .notify-bar__dot {
  background: var(--sb-success);
  box-shadow: 0 0 7px color-mix(in oklab, var(--sb-success) 70%, transparent);
}
.notify-bar__sep { width: 1px; height: 11px; flex: none; background: var(--sb-line-strong); }
.notify-bar__actions { flex: none; display: flex; align-items: center; gap: 8px; }
.notify-unsaved { color: var(--sb-warning); font-size: 12px; }
.notify-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 22px 24px 32px;
  scrollbar-width: thin;
  scrollbar-color: color-mix(in oklab, var(--sb-muted) 35%, transparent) transparent;
}
.notify-scroll::-webkit-scrollbar { width: 6px; }
.notify-scroll::-webkit-scrollbar-thumb {
  background: color-mix(in oklab, var(--sb-muted) 30%, transparent);
  border-radius: 4px;
}
.notify-section + .notify-section { margin-top: 24px; }
.notify-section__head { margin-bottom: 10px; }
.notify-section__head h3 {
  margin: 0;
  color: var(--sb-text);
  font-size: 13px;
  font-weight: 650;
}
.notify-section__head p {
  margin: 4px 0 0;
  color: var(--sb-muted);
  font-size: 12px;
  line-height: 1.6;
}
.notify-card {
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  box-shadow: var(--sb-shadow-sm);
  overflow: hidden;
}
.notify-fields {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px 20px;
  padding: 18px 20px;
}
.notify-field { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.notify-field--full { grid-column: 1 / -1; }
.notify-field__label { color: var(--sb-text-2); font-size: 12px; font-weight: 600; }
.notify-field__label em { color: var(--sb-danger); font-style: normal; }
.notify-field__hint { color: var(--sb-muted); font-size: 11px; line-height: 1.55; }
.notify-field :deep(.el-input__wrapper),
.notify-field :deep(.el-select__wrapper) {
  background-color: color-mix(in oklab, var(--sb-surface-2) 55%, transparent);
  box-shadow: 0 0 0 1px var(--sb-line-strong) inset;
}

/* 告警规则：单列行式布局，所有行天然对齐 */
.rule-list { display: flex; flex-direction: column; }
.rule-row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 62px;
  padding: 10px 18px;
  border-bottom: 1px solid var(--sb-line);
  transition: opacity .16s ease;
}
.rule-row:last-child { border-bottom: 0; }
.rule-row.is-off { opacity: .55; }
.rule-row__icon {
  width: 28px;
  height: 28px;
  flex: none;
  display: grid;
  place-items: center;
  color: var(--sb-muted);
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
  border-radius: 8px;
  font-size: 14px;
}
.rule-row__text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.rule-row__title { color: var(--sb-text); font-size: 13px; font-weight: 600; }
.rule-row__desc {
  overflow: hidden;
  color: var(--sb-muted);
  font-size: 11px;
  line-height: 1.5;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rule-row__fields {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  flex-wrap: wrap;
  gap: 8px 18px;
  margin-left: auto;
}
.rule-row__field { display: flex; align-items: center; gap: 7px; color: var(--sb-muted); font-size: 11px; }
.rule-row__field :deep(.el-input-number) { width: 104px; }
.rule-row > :deep(.el-switch) { flex: none; margin-left: 8px; }

.tools-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 20px 24px 32px;
  scrollbar-width: thin;
}
.tools-scroll::-webkit-scrollbar { width: 6px; }
.tools-scroll::-webkit-scrollbar-thumb { background: color-mix(in oklab, var(--sb-muted) 30%, transparent); border-radius: 4px; }

.reality-form { padding: 18px 20px; }
.reality-result-panel {
  padding: 16px 20px 18px;
  background: color-mix(in oklab, var(--sb-surface-2) 35%, transparent);
  border-top: 1px solid var(--sb-line);
}
.result-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.result-title {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}
.result-title strong {
  overflow: hidden;
  color: var(--sb-text);
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.result-latency {
  flex: none;
  color: var(--sb-muted);
  font-size: 12px;
}
.result-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px 24px;
}
.result-item {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 12px;
}
.result-item .label {
  flex: none;
  color: var(--sb-muted);
}
.result-item .mono {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* IP 库状态与查询结果 */
.info-list { display: flex; flex-direction: column; }
.info-list--bordered { border-top: 1px solid var(--sb-line); }
.info-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  min-height: 48px;
  padding: 9px 20px;
  border-bottom: 1px solid var(--sb-line);
}
.info-row:last-child { border-bottom: 0; }
.info-row__label { flex: none; color: var(--sb-muted); font-size: 12px; }
.info-row__value {
  min-width: 0;
  overflow: hidden;
  color: var(--sb-text);
  font-size: 12.5px;
  text-align: right;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.info-row > .el-tag { flex: none; }
.ipdb-actions { display: flex; flex-wrap: wrap; gap: 10px; padding: 14px 20px 0; }
.ipdb-tip { margin: 0; padding: 12px 20px 18px; color: var(--sb-muted); font-size: 11px; line-height: 1.6; }
.ipdb-query { display: flex; gap: 10px; padding: 16px 20px; }
.ipdb-query .el-input { max-width: 420px; }

@media (max-width: 1080px) {
  .notify-scroll { padding: 18px 18px 32px; }
  .tools-scroll { padding: 18px 18px 28px; }
}

@media (max-width: 820px) {
  .notify-fields { grid-template-columns: minmax(0, 1fr); }
  .notify-field--full { grid-column: auto; }
  .notify-bar { align-items: flex-start; flex-direction: column; gap: 10px; padding: 12px 18px; }
  .notify-bar__actions { width: 100%; justify-content: flex-end; }
  .rule-row { flex-wrap: wrap; }
  .rule-row__fields { margin-left: 40px; }
  .rule-row__field :deep(.el-input-number) { width: 96px; }
  .ipdb-query { flex-wrap: wrap; }
  .ipdb-query .el-input { max-width: none; flex: 1; }
}

@media (max-width: 700px) {
  .security-card { align-items: flex-start; flex-wrap: wrap; }
  .security-card > div { flex-basis: 100%; }
  .result-grid { grid-template-columns: 1fr; }
}
</style>
