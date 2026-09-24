<script setup>
import { computed, inject, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Bell, Check, Plus, Refresh, Search, Warning } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { api, post, put } from '../api'
import { formatDateTime, includesText } from '../format'
import PageHeader from '../components/PageHeader.vue'
import PagedTable from '../components/PagedTable.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)
const tab = ref('account')
const operators = ref([])
const dialog = ref('')
const totpSetup = reactive({ open: false, loading: false, secret: '', qr: '', code: '' })
const operator = reactive({ username: '', password: '', role: 'operator' })
const operatorKeyword = ref('')
const operatorStatus = ref('')

const alertSettings = reactive({
  bark_server: 'https://api.day.app',
  bark_device_key: '',
  bark_sound: 'minuet',
  bark_group: 'Polaris',
  bark_url: '',
  traffic_alert_enabled: true,
  traffic_threshold_mbps: 50,
  traffic_duration_sec: 10,
  conn_alert_enabled: true,
  conn_threshold_count: 200,
  single_ip_alert_enabled: true,
  single_ip_threshold_count: 60,
  gfw_alert_enabled: true,
  offline_alert_enabled: true,
  cooldown_minutes: 15,
  auto_probe_gfw_enabled: true,
  probe_gfw_interval_minutes: 30,
  auto_probe_speedtest_enabled: true,
  probe_speedtest_interval_hours: 6,
  probe_skip_when_busy: true,
  probe_notify_always: true,
})
const alertSaving = ref(false)
const testSending = ref(false)
const gfwProbing = ref(false)
const gfwResultDialogVisible = ref(false)
const gfwResults = ref([])
const realityForm = reactive({ target: 'gateway.icloud.com', port: 443 })
const realityChecking = ref(false)
const realityResult = ref(null)

const filteredOperators = computed(() => operators.value.filter((row) => {
  if (operatorStatus.value && String(row.enabled) !== operatorStatus.value) return false
  return includesText([row.username, row.role], operatorKeyword.value)
}))

async function load() {
  loading.value = true
  try {
    const [operatorResult, alertsResult] = await Promise.all([
      isAdmin.value ? api('/operators').catch(() => ({ operators: [] })) : Promise.resolve({ operators: [] }),
      isAdmin.value ? api('/alerts/settings').catch(() => ({ settings: null })) : Promise.resolve({ settings: null }),
    ])
    operators.value = operatorResult.operators || []
    if (alertsResult?.settings) {
      Object.assign(alertSettings, alertsResult.settings)
    }
  } finally { loading.value = false }
}

async function saveAlertSettings() {
  alertSaving.value = true
  try {
    const res = await put('/alerts/settings', alertSettings)
    if (res?.settings) {
      Object.assign(alertSettings, res.settings)
    }
    ElMessage.success('配置已保存')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '保存失败')
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
    // 自动先保存配置，避免用户刷新页面后数据丢失
    await put('/alerts/settings', alertSettings).catch(() => {})
    await post('/alerts/test', {
      server: alertSettings.bark_server,
      device_key: alertSettings.bark_device_key,
      sound: alertSettings.bark_sound,
      group: alertSettings.bark_group,
      url: alertSettings.bark_url,
    })
    ElMessage.success('测试通知已发出')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '发送测试通知失败')
  } finally {
    testSending.value = false
  }
}

async function triggerGFWCheck() {
  gfwProbing.value = true
  try {
    const res = await post('/probes/run-gfw-check', {})
    const items = res.nodes || res.results || []
    gfwResults.value = items
    gfwResultDialogVisible.value = true
    ElMessage.success('国内探测完成')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '触发 GFW 探测失败')
  } finally {
    gfwProbing.value = false
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

async function beginTOTPSetup() {
  totpSetup.loading = true
  try {
    const result = await post('/auth/2fa/setup', {})
    totpSetup.secret = result.secret
    totpSetup.qr = await QRCode.toDataURL(result.otpauth_uri, { width: 220, margin: 1 })
    totpSetup.code = ''
    totpSetup.open = true
  } finally { totpSetup.loading = false }
}

async function enableTOTP() {
  if (totpSetup.loading) return
  if (!/^\d{6}$/.test(totpSetup.code)) return ElMessage.error('请输入验证器显示的 6 位数字')
  totpSetup.loading = true
  try {
    await api('/auth/2fa/enable', { method: 'POST', body: { code: totpSetup.code }, silentUnauthorized: true })
    appState.totp_enabled = true
    totpSetup.open = false
    ElMessage.success('两步验证已启用，后续登录需要输入动态验证码')
    await load()
  } catch (error) {
    ElMessage.error(error.code === 'authentication failed'
      ? '验证码不正确，请输入验证器当前显示的 6 位动态码'
      : error.message)
  } finally { totpSetup.loading = false }
}

async function disableOwnTOTP() {
  const result = await ElMessageBox.prompt('请输入当前登录密码以确认关闭', '关闭两步验证', {
    inputType: 'password',
    inputValidator: (value) => Boolean(value) || '请输入当前密码',
  })
  await post('/auth/2fa/disable', { password: result.value })
  appState.totp_enabled = false
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
        <el-tabs v-model="tab" class="panel-tabs">
          <el-tab-pane label="登录安全" name="account">
            <section class="security-card">
              <div>
                <h3>两步验证</h3>
                <p v-if="appState.totp_enabled">登录时需要输入验证器生成的动态验证码。</p>
                <p v-else>为账户增加一层动态验证码保护。</p>
              </div>
              <el-tag :type="appState.totp_enabled ? 'success' : 'info'">{{ appState.totp_enabled ? '已启用' : '未启用' }}</el-tag>
              <el-button v-if="appState.totp_enabled" type="danger" plain @click="disableOwnTOTP">关闭</el-button>
              <el-button v-else type="primary" :loading="totpSetup.loading" @click="beginTOTPSetup">启用</el-button>
            </section>
          </el-tab-pane>

          <el-tab-pane v-if="isAdmin" label="管理账户" name="operators">
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

          <el-tab-pane v-if="isAdmin" label="Bark 告警与探测" name="alerts">
            <div class="alerts-scroll-container">
              <!-- 顶部常驻操作栏 -->
              <div class="alerts-top-toolbar">
                <div class="alerts-top-status">
                  <span class="alerts-status-indicator" :class="{ 'is-active': alertSettings.offline_alert_enabled }"></span>
                  <span class="alerts-status-text">Bark 告警推送与定时网络探测</span>
                  <el-tag :type="alertSettings.offline_alert_enabled ? 'success' : 'info'" size="small">
                    {{ alertSettings.offline_alert_enabled ? '告警已启用' : '告警未启用' }}
                  </el-tag>
                </div>
                <div class="alerts-top-actions">
                  <el-button :loading="testSending" @click="sendTestAlert">测试推送</el-button>
                  <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存所有配置</el-button>
                </div>
              </div>

              <div class="settings-grid">
              <!-- Bark 推送配置 -->
              <div class="settings-card">
                <div class="card-header">
                  <div class="card-title">
                    <el-icon class="card-icon"><Bell /></el-icon>
                    <div>
                      <h3>Bark 消息推送 (Apple 原生 APNs)</h3>
                      <p>零国内第三方 SDK、零依赖，通过 iOS 原生 Bark 客户端即时推送通知。</p>
                    </div>
                  </div>
                  <el-switch v-model="alertSettings.offline_alert_enabled" active-text="离线警报开启" inactive-text="离线警报关闭" />
                </div>
                <el-form label-position="top" class="card-body">
                  <el-row :gutter="16">
                    <el-col :span="12">
                      <el-form-item label="Bark 服务器地址">
                        <el-input v-model="alertSettings.bark_server" placeholder="https://api.day.app" />
                      </el-form-item>
                    </el-col>
                    <el-col :span="12">
                      <el-form-item label="Device Key (设备密钥)" required>
                        <el-input v-model="alertSettings.bark_device_key" show-password placeholder="Bark App 中的专属 Key" />
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <el-row :gutter="16">
                    <el-col :span="8">
                      <el-form-item label="提示铃声">
                        <el-select v-model="alertSettings.bark_sound" style="width: 100%">
                          <el-option label="minuet (清脆小步舞曲 - 推荐)" value="minuet" />
                          <el-option label="anticipate (轻快期待)" value="anticipate" />
                          <el-option label="bell (经典铃声)" value="bell" />
                          <el-option label="glass (清澈水滴)" value="glass" />
                          <el-option label="horn (警示号角)" value="horn" />
                          <el-option label="telegraph (电报码)" value="telegraph" />
                          <el-option label="silence (静音仅震动)" value="silence" />
                        </el-select>
                      </el-form-item>
                    </el-col>
                    <el-col :span="8">
                      <el-form-item label="推送分组 (Group)">
                        <el-input v-model="alertSettings.bark_group" placeholder="Polaris" />
                      </el-form-item>
                    </el-col>
                    <el-col :span="8">
                      <el-form-item label="点击跳转 URL (可选)">
                        <el-input v-model="alertSettings.bark_url" placeholder="例如控制台网址" />
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <div class="card-footer-action">
                    <el-button :loading="testSending" @click="sendTestAlert">发送测试通知</el-button>
                    <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存配置</el-button>
                  </div>
                </el-form>
              </div>

              <!-- 实时指标与阈值预警 -->
              <div class="settings-card">
                <div class="card-header">
                  <div class="card-title">
                    <el-icon class="card-icon"><Warning /></el-icon>
                    <div>
                      <h3>瞬时流量与连接数阈值预警</h3>
                      <p>实时监控节点的流量突发、瞬时并发激增或单一 IP 暴力刷量行为并即时报警。</p>
                    </div>
                  </div>
                  <el-switch v-model="alertSettings.traffic_alert_enabled" active-text="监控开启" inactive-text="监控关闭" />
                </div>
                <el-form label-position="top" class="card-body">
                  <el-row :gutter="16">
                    <el-col :span="12">
                      <el-form-item label="瞬时带宽突发预警 (Mbps)">
                        <el-input-number v-model="alertSettings.traffic_threshold_mbps" :min="0" :max="10000" :step="10" style="width: 100%" />
                        <div class="form-tip">0 表示不限制；超过此速率持续达到观察时长即触发告警。</div>
                      </el-form-item>
                    </el-col>
                    <el-col :span="12">
                      <el-form-item label="带宽突发判定窗口 (秒)">
                        <el-input-number v-model="alertSettings.traffic_duration_sec" :min="5" :max="300" :step="5" style="width: 100%" />
                        <div class="form-tip">防止网络瞬间波动误报，推荐 10 ~ 60 秒。</div>
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <el-row :gutter="16">
                    <el-col :span="8">
                      <el-form-item label="节点瞬时连接总数上限">
                        <el-input-number v-model="alertSettings.conn_threshold_count" :min="10" :max="50000" :step="50" style="width: 100%" />
                        <div class="form-tip">个人使用通常低于 500 个并发。</div>
                      </el-form-item>
                    </el-col>
                    <el-col :span="8">
                      <el-form-item label="单个 IP 突发并发上限">
                        <el-input-number v-model="alertSettings.single_ip_threshold_count" :min="5" :max="5000" :step="10" style="width: 100%" />
                        <div class="form-tip">防范单一来源 IP 大量恶意扫描或抓包。</div>
                      </el-form-item>
                    </el-col>
                    <el-col :span="8">
                      <el-form-item label="重复报警静默冷却 (分钟)">
                        <el-input-number v-model="alertSettings.cooldown_minutes" :min="1" :max="1440" :step="5" style="width: 100%" />
                        <div class="form-tip">同一节点同一类型的告警在此期间不重复轰炸。</div>
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <div class="card-footer-action">
                    <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存配置</el-button>
                  </div>
                </el-form>
              </div>

              <!-- 定时探测与三网测速 -->
              <div class="settings-card">
                <div class="card-header">
                  <div class="card-title">
                    <el-icon class="card-icon"><Check /></el-icon>
                    <div>
                      <h3>定时 GFW 阻断探测与三网测速</h3>
                      <p>定时从国内视角探测各节点连通性与三网延迟，每逢探测必推通知汇报状态。</p>
                    </div>
                  </div>
                  <el-switch v-model="alertSettings.auto_probe_gfw_enabled" active-text="定时探测开启" inactive-text="定时探测关闭" />
                </div>
                <el-form label-position="top" class="card-body">
                  <el-row :gutter="16">
                    <el-col :span="12">
                      <el-form-item label="GFW 阻断探测周期 (分钟)">
                        <el-input-number v-model="alertSettings.probe_gfw_interval_minutes" :min="1" :max="1440" :step="1" style="width: 100%" />
                        <div class="form-tip">定时探测客户端连接端口是否被阻断。</div>
                      </el-form-item>
                    </el-col>
                    <el-col :span="12">
                      <el-form-item label="三网定时测速周期 (小时)">
                        <el-input-number v-model="alertSettings.probe_speedtest_interval_hours" :min="1" :max="168" :step="1" style="width: 100%" />
                        <div class="form-tip">测试电信、联通、移动 TCP 延迟及测速。</div>
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <el-row :gutter="16">
                    <el-col :span="12">
                      <el-form-item label="每次探测必推通知 (核心要求)">
                        <el-switch v-model="alertSettings.probe_notify_always" active-text="开启 (全绿推健康日报，异常推警报)" inactive-text="仅在发生异常时推送" />
                        <div class="form-tip">开启后，哪怕所有节点正常运行，每次探测也发送简报让您时刻心中有数。</div>
                      </el-form-item>
                    </el-col>
                    <el-col :span="12">
                      <el-form-item label="业务繁忙时避让测速">
                        <el-switch v-model="alertSettings.probe_skip_when_busy" active-text="避让 (节点带宽 > 5MB/s 时跳过)" inactive-text="强制执行" />
                        <div class="form-tip">避免在您观看高清视频或下载文件时测速造成卡顿。</div>
                      </el-form-item>
                    </el-col>
                  </el-row>
                  <div class="card-footer-action">
                    <el-button :loading="gfwProbing" @click="triggerGFWCheck">立即运行 GFW 探测</el-button>
                    <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存所有告警配置</el-button>
                  </div>
                </el-form>
              </div>

              <!-- Reality 目标域名合格度探测工具 -->
              <div class="settings-card">
                <div class="card-header">
                  <div class="card-title">
                    <el-icon class="card-icon"><Search /></el-icon>
                    <div>
                      <h3>VLESS Reality 伪装域名检测工具</h3>
                      <p>在配置 Reality 偷窥 SNI 之前，快速检测目标网站是否支持 TLS 1.3、h2 ALPN、证书有效期及网络延迟。</p>
                    </div>
                  </div>
                </div>
                <div class="card-body">
                  <el-row :gutter="16" align="bottom">
                    <el-col :span="14">
                      <el-form-item label="待检测目标域名 (例如 gateway.icloud.com, dl.google.com)">
                        <el-input v-model="realityForm.target" placeholder="gateway.icloud.com" clearable />
                      </el-form-item>
                    </el-col>
                    <el-col :span="5">
                      <el-form-item label="端口">
                        <el-input-number v-model="realityForm.port" :min="1" :max="65535" style="width: 100%" />
                      </el-form-item>
                    </el-col>
                    <el-col :span="5">
                      <el-form-item label="&nbsp;">
                        <el-button type="primary" style="width: 100%" :loading="realityChecking" @click="checkReality">开始检测</el-button>
                      </el-form-item>
                    </el-col>
                  </el-row>

                  <div v-if="realityResult" class="reality-result-panel">
                    <div class="result-header">
                      <div class="result-title">
                        <strong>检测结果：{{ realityResult.target }}</strong>
                        <el-tag :type="realityResult.compatible ? 'success' : 'danger'" size="large">
                          {{ realityResult.compatible ? '合格 (推荐作为 Reality 偷窥域名)' : '不合格 (不建议使用)' }}
                        </el-tag>
                      </div>
                      <div class="result-latency">延迟: <strong>{{ realityResult.latency_ms }} ms</strong></div>
                    </div>
                    <div class="result-grid">
                      <div class="result-item">
                        <span class="label">TLS 1.3:</span>
                        <el-tag :type="realityResult.tls_13 ? 'success' : 'danger'">{{ realityResult.tls_13 ? '支持 (TLS 1.3)' : '不支持' }}</el-tag>
                      </div>
                      <div class="result-item">
                        <span class="label">ALPN 协商:</span>
                        <el-tag :type="realityResult.alpn?.includes('h2') ? 'success' : 'warning'">{{ realityResult.alpn || '无' }}</el-tag>
                      </div>
                      <div class="result-item">
                        <span class="label">证书颁发者:</span>
                        <span class="mono">{{ realityResult.cert_issuer || '—' }}</span>
                      </div>
                      <div class="result-item">
                        <span class="label">证书到期天数:</span>
                        <span>{{ realityResult.cert_days_left }} 天 ({{ formatDateTime(realityResult.cert_expires_at) }})</span>
                      </div>
                    </div>
                    <el-alert v-if="realityResult.recommendation" :title="realityResult.recommendation" :type="realityResult.compatible ? 'success' : 'warning'" show-icon :closable="false" style="margin-top: 12px" />
                  </div>
                </div>
              </div>
            </div>
          </div>
        </el-tab-pane>

        </el-tabs>
      </div>
    </main>

    <el-dialog v-model="totpSetup.open" title="启用两步验证" width="500px" :close-on-click-modal="false">
      <div class="totp-setup">
        <p>扫描二维码后，输入验证器显示的 6 位动态码完成绑定。</p>
        <img :src="totpSetup.qr" alt="两步验证绑定二维码" data-testid="totp-qr" />
        <el-form label-position="top" @submit.prevent="enableTOTP">
          <el-form-item label="无法扫码时，可手动输入此密钥"><el-input :model-value="totpSetup.secret" readonly class="mono" data-testid="totp-secret" /></el-form-item>
          <el-form-item label="动态验证码"><el-input v-model="totpSetup.code" maxlength="6" inputmode="numeric" placeholder="000000" /></el-form-item>
        </el-form>
      </div>
      <template #footer><el-button @click="totpSetup.open = false">取消</el-button><el-button type="primary" :loading="totpSetup.loading" @click="enableTOTP">启用</el-button></template>
    </el-dialog>
    <el-dialog :model-value="dialog === 'operator'" title="新建管理账户" width="540px" @close="dialog = ''"><el-form label-position="top"><el-form-item label="用户名"><el-input v-model="operator.username" placeholder="3 至 64 位，可使用字母、数字、点、下划线和短横线" /></el-form-item><el-form-item label="初始密码"><el-input v-model="operator.password" type="password" show-password /><div class="form-tip">用户首次登录时必须修改此密码。</div></el-form-item><el-form-item label="权限"><el-select v-model="operator.role" style="width: 100%"><el-option label="管理员" value="admin" /><el-option label="运维人员" value="operator" /><el-option label="只读用户" value="viewer" /></el-select></el-form-item></el-form><template #footer><el-button @click="dialog = ''">取消</el-button><el-button type="primary" :disabled="!operator.username || operator.password.length < 12" @click="saveOperator">创建</el-button></template></el-dialog>

    <!-- GFW 真实国内探针检测结果对话框 -->
    <el-dialog v-model="gfwResultDialogVisible" title="GFW 阻断探测详细报告 (国内真机视角)" width="780px">
      <div style="margin-bottom: 16px">
        <el-alert
          type="info"
          :closable="false"
          show-icon
          title="真机国内视角跨墙探测"
          description="探测请求由国内真实探针（北京/深圳/上海等国内核心 BGP 机房）向境外目标端口发起 TCP 握手，跨越 GFW 防火墙检测连通性与时延，杜绝境外控制机误判。"
        />
      </div>
      <el-table :data="gfwResults" style="width: 100%" stripe empty-text="未获取到探测数据">
        <el-table-column prop="node_name" label="节点名称" min-width="120" />
        <el-table-column prop="target_port" label="端口" width="75" align="center" />
        <el-table-column label="境外公网" width="95" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.overseas_ok" type="success" size="small">正常</el-tag>
            <el-tag v-else type="danger" size="small">离线</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="国内真机连通" width="145" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.domestic_ok" type="success" size="small">🟢 通畅 ({{ row.latency_ms }}ms)</el-tag>
            <el-tag v-else-if="row.overseas_ok" type="danger" size="small">🚨 疑似被墙</el-tag>
            <el-tag v-else type="info" size="small">⚪ 未监听</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="probe_source" label="国内探针来源" min-width="170" show-overflow-tooltip />
        <el-table-column prop="status_desc" label="判定结论" min-width="150" />
      </el-table>
      <template #footer>
        <el-button type="primary" @click="gfwResultDialogVisible = false">确定</el-button>
      </template>
    </el-dialog>
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
.totp-setup { text-align: center; }
.totp-setup > p { margin: 0 0 16px; color: var(--sb-muted); line-height: 1.7; }
.totp-setup img { width: 220px; height: 220px; margin-bottom: 14px; padding: 8px; background: #fff; border-radius: var(--sb-radius-sm); }
.totp-setup :deep(.el-form-item) { text-align: left; }
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

.alerts-scroll-container {
  flex: 1;
  min-height: 0;
  height: 100%;
  overflow-y: auto !important;
  padding: 16px 20px 80px;
  box-sizing: border-box;
  scrollbar-width: thin;
  scrollbar-color: rgba(148, 163, 184, 0.3) transparent;
}
.alerts-scroll-container::-webkit-scrollbar {
  width: 6px;
}
.alerts-scroll-container::-webkit-scrollbar-thumb {
  background: rgba(148, 163, 184, 0.3);
  border-radius: 4px;
}

.alerts-top-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 18px;
  margin-bottom: 18px;
  background: var(--sb-surface-2, #141d30);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  box-shadow: 0 4px 20px -8px rgba(0, 0, 0, 0.5);
}
.alerts-top-status {
  display: flex;
  align-items: center;
  gap: 12px;
}
.alerts-status-text {
  font-size: 14px;
  font-weight: 600;
  color: var(--sb-text);
}
.alerts-status-indicator {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--sb-muted);
}
.alerts-status-indicator.is-active {
  background: var(--sb-success, #34d399);
  box-shadow: 0 0 10px rgba(52, 211, 153, 0.7);
}
.alerts-top-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.settings-grid {
  display: flex;
  flex-direction: column;
  gap: 20px;
}
.settings-card {
  background: var(--sb-surface-2, #141d30);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius);
  overflow: hidden;
  box-shadow: 0 4px 24px -10px rgba(0, 0, 0, 0.5);
}
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  background: rgba(255, 255, 255, 0.02);
  border-bottom: 1px solid var(--sb-line);
}
.card-title {
  display: flex;
  align-items: center;
  gap: 12px;
}
.card-icon {
  font-size: 22px;
  color: var(--sb-accent, #38bdf8);
}
.card-title h3 {
  margin: 0 0 4px;
  font-size: 15px;
  font-weight: 600;
  color: var(--sb-text);
}
.card-title p {
  margin: 0;
  font-size: 13px;
  color: var(--sb-muted);
}
.card-body {
  padding: 20px;
}
.card-body :deep(.el-form-item__label) {
  color: var(--sb-text-2) !important;
  font-weight: 500;
  font-size: 13px;
}
.card-body :deep(.el-input__wrapper),
.card-body :deep(.el-select__wrapper) {
  background-color: var(--sb-surface) !important;
  box-shadow: 0 0 0 1px var(--sb-line) inset !important;
}
.card-body :deep(.el-input__inner) {
  color: var(--sb-text) !important;
}
.card-body :deep(.el-input-number .el-input-number__decrease),
.card-body :deep(.el-input-number .el-input-number__increase) {
  background-color: rgba(148, 163, 184, 0.08) !important;
  color: var(--sb-text) !important;
  border-color: var(--sb-line) !important;
}
.card-footer-action {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 12px;
  padding-top: 14px;
  border-top: 1px solid var(--sb-line);
}

.reality-result-panel {
  margin-top: 16px;
  padding: 16px;
  background: var(--sb-surface, #0e1524);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm);
}
.result-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.result-title {
  display: flex;
  align-items: center;
  gap: 12px;
}
.result-latency {
  font-size: 13px;
  color: var(--sb-muted);
}
.result-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
}
.result-item {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.result-item .label {
  color: var(--sb-muted);
}

@media (max-width: 700px) {
  .security-card { align-items: flex-start; flex-wrap: wrap; }
  .security-card > div { flex-basis: 100%; }
  .result-grid { grid-template-columns: 1fr; }
}
</style>
