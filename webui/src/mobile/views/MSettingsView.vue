<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Search } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { api, post, put } from '../../api'
import { formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const appState = inject('appState')
const isAdmin = inject('isAdmin')
const loading = ref(false)

const initialTab = () => {
  const hash = location.hash || ''
  if (hash.includes('alerts')) return 'alerts'
  if (hash.includes('operators')) return 'operators'
  return 'account'
}
const tab = ref(initialTab())
const operators = ref([])
const operatorSheet = ref(false)
const totpSetup = reactive({ open: false, loading: false, secret: '', qr: '', code: '' })
const operator = reactive({ username: '', password: '', role: 'operator' })
const operatorKeyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const roleSheet = ref(false)
const roleTarget = ref(null)
const roleDraft = ref('operator')

// 告警配置相关状态
const alertSettings = reactive({
  bark_server: 'https://api.day.app',
  bark_device_key: '',
  bark_sound: 'minuet.caf',
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
  scan_alert_enabled: false,
})
const alertSaving = ref(false)
const testSending = ref(false)
const gfwProbing = ref(false)
const gfwResults = ref([])
const gfwSheet = ref(false)

const soundOptions = [
  { value: 'minuet.caf', label: 'minuet (清脆小步舞曲 - 推荐)', desc: '轻快清爽，推荐日常使用' },
  { value: 'anticipate.caf', label: 'anticipate (轻快期待)', desc: '轻柔提示' },
  { value: 'bell.caf', label: 'bell (经典铃声)', desc: '清脆经典' },
  { value: 'glass.caf', label: 'glass (清澈水滴)', desc: '温和水滴音' },
  { value: 'horn.caf', label: 'horn (警示号角)', desc: '突出警示' },
  { value: 'calypso.caf', label: 'calypso (欢快击鼓)', desc: '节奏欢快' },
  { value: 'telegraph.caf', label: 'telegraph (电报码)', desc: '极简电报滴答声' },
  { value: 'silence.caf', label: 'silence (静音仅震动)', desc: '不发出声音，仅系统震动' },
]

const roles = [
  { value: 'admin', label: '管理员' },
  { value: 'operator', label: '运维人员' },
  { value: 'viewer', label: '只读用户' },
]
const roleNames = Object.fromEntries(roles.map((role) => [role.value, role.label]))
const filteredOperators = computed(() => operators.value.filter((row) => includesText([row.username, roleNames[row.role]], operatorKeyword.value)))
const tabs = computed(() => (isAdmin.value
  ? [{ value: 'account', label: '登录安全' }, { value: 'operators', label: '管理账户' }, { value: 'alerts', label: '告警配置' }]
  : [{ value: 'account', label: '登录安全' }]))

function syncTabFromHash() {
  const hash = location.hash || ''
  if (hash.includes('alerts')) {
    tab.value = 'alerts'
  } else if (hash.includes('operators')) {
    tab.value = 'operators'
  }
}

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
      if (alertSettings.bark_sound && !alertSettings.bark_sound.endsWith('.caf')) {
        alertSettings.bark_sound = alertSettings.bark_sound + '.caf'
      }
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
    ElMessage.success('告警配置已保存')
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
    gfwSheet.value = true
    ElMessage.success('国内探测完成')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '触发 GFW 探测失败')
  } finally {
    gfwProbing.value = false
  }
}

function openOperator() {
  Object.assign(operator, { username: '', password: '', role: 'operator' })
  operatorSheet.value = true
}

async function saveOperator() {
  await post('/operators', operator)
  operatorSheet.value = false
  ElMessage.success('账户已创建，用户首次登录时需要修改初始密码')
  await load()
}

const details = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  return [
    { label: '权限', value: roleNames[row.role] || row.role },
    { label: '状态', value: row.enabled ? '启用' : '停用' },
    { label: '登录方式', value: row.totp_enabled ? '密码 + 两步验证' : '仅密码' },
    { label: '密码', value: row.must_change_password ? '等待本人修改' : '已设置' },
    { label: '最后登录', value: formatDateTime(row.last_login_at, '从未登录') },
  ]
})

function openActions(row) {
  actionTarget.value = row
  actionsOpen.value = true
}

const actions = computed(() => {
  const row = actionTarget.value
  if (!row) return []
  const list = [
    { key: 'role', label: '修改权限', hint: `当前：${roleNames[row.role]}` },
    { key: 'state', label: row.enabled ? '停用账户' : '启用账户' },
    { key: 'password', label: '重置密码' },
  ]
  if (row.totp_enabled) list.push({ key: 'totp', label: '关闭两步验证', danger: true })
  return list
})

function runAction(key) {
  const row = actionTarget.value
  if (key === 'role') return changeRole(row)
  if (key === 'state') return setOperatorState(row)
  if (key === 'password') return resetPassword(row)
  if (key === 'totp') return disableOperatorTOTP(row)
}

function changeRole(row) {
  roleTarget.value = row
  roleDraft.value = row.role
  roleSheet.value = true
}

async function saveRole() {
  await put(`/operators/${roleTarget.value.id}`, { role: roleDraft.value, enabled: roleTarget.value.enabled })
  roleSheet.value = false
  ElMessage.success('权限已修改')
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
    totpSetup.qr = await QRCode.toDataURL(result.otpauth_uri, { width: 440, margin: 1 })
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

onMounted(() => {
  syncTabFromHash()
  window.addEventListener('hashchange', syncTabFromHash)
  load()
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', syncTabFromHash)
})
</script>

<template>
  <MPage :loading="loading">
    <MSegmented v-model="tab" :options="tabs" />

    <template v-if="tab === 'account'">
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">两步验证</span>
          <span class="m-pill" :class="appState.totp_enabled ? 'm-pill--success' : 'm-pill--info'">{{ appState.totp_enabled ? '已启用' : '未启用' }}</span>
        </div>
        <div class="m-card__note">{{ appState.totp_enabled ? '登录时需要输入验证器生成的动态验证码。' : '为账户增加一层动态验证码保护。' }}</div>
        <el-button v-if="appState.totp_enabled" type="danger" plain class="wide" @click="disableOwnTOTP">关闭两步验证</el-button>
        <el-button v-else type="primary" class="wide" :loading="totpSetup.loading" @click="beginTOTPSetup">启用两步验证</el-button>
      </section>
    </template>

    <template v-else-if="tab === 'operators'">
      <el-input v-model="operatorKeyword" clearable :prefix-icon="Search" placeholder="搜索用户名或权限" />
      <div class="m-count">共 {{ filteredOperators.length }} 个账户</div>
      <article v-for="row in filteredOperators" :key="row.id" class="m-item" :class="{ 'is-off': !row.enabled }">
        <button type="button" class="m-item__hit" @click="openActions(row)">
          <div class="m-item__head">
            <span class="m-item__title">{{ row.username }}</span>
            <span v-if="row.must_change_password" class="m-pill m-pill--warning">待改密码</span>
            <span v-if="!row.enabled" class="m-pill m-pill--info">停用</span>
          </div>
          <div class="m-item__stats">
            <span class="m-stat"><b>{{ roleNames[row.role] || row.role }}</b><small>权限</small></span>
            <span class="m-stat"><b>{{ row.totp_enabled ? '两步验证' : '仅密码' }}</b><small>登录方式</small></span>
          </div>
          <div class="m-item__meta">最后登录 {{ formatDateTime(row.last_login_at, '从未登录') }}</div>
        </button>
      </article>
      <div v-if="!filteredOperators.length && !loading" class="m-empty">还没有管理账户</div>
    </template>

    <template v-else-if="tab === 'alerts'">
      <!-- Bark 推送配置 -->
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">Bark 消息推送</span>
          <el-switch v-model="alertSettings.offline_alert_enabled" active-text="离线警报" />
        </div>
        <div class="m-card__note">
          通过 iOS 原生 APNs 推送实时通知。严格遵循 iPhone 物理静音开关，仅在非静音模式出声。
        </div>

        <div class="m-field" style="margin-top: 14px">
          <label class="m-field__label">Bark 服务器地址</label>
          <el-input v-model="alertSettings.bark_server" placeholder="https://api.day.app" clearable />
        </div>

        <div class="m-field">
          <label class="m-field__label">Device Key (设备密钥) <em>*</em></label>
          <el-input v-model="alertSettings.bark_device_key" show-password placeholder="Bark App 中的专属 Key" clearable />
          <div class="m-field__hint">打开手机 Bark App 查看主页生成的 Device Key。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">默认提示铃声</label>
          <MPicker v-model="alertSettings.bark_sound" :options="soundOptions" title="选择默认提示铃声" />
          <div class="m-field__hint">阻断、离线、爆破等事件会自动使用专属音效（alarm、horn 等）加以区分。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">推送分组 (Group)</label>
          <el-input v-model="alertSettings.bark_group" placeholder="Polaris" clearable />
        </div>

        <div class="m-field">
          <label class="m-field__label">点击跳转 URL (可选)</label>
          <el-input v-model="alertSettings.bark_url" placeholder="例如控制台网址" clearable />
        </div>

        <div class="m-card-actions">
          <el-button :loading="testSending" @click="sendTestAlert">测试推送</el-button>
          <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存配置</el-button>
        </div>
      </section>

      <!-- 瞬时指标与阈值预警 -->
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">瞬时流量与连接数预警</span>
          <el-switch v-model="alertSettings.traffic_alert_enabled" active-text="监控开启" />
        </div>
        <div class="m-card__note">
          实时监控节点瞬时带宽突发、连接数激增及单一 IP 突发高并发。
        </div>

        <div class="m-field" style="margin-top: 14px">
          <label class="m-field__label">瞬时带宽突发预警 (Mbps)</label>
          <el-input-number v-model="alertSettings.traffic_threshold_mbps" :min="0" :max="10000" :step="10" style="width: 100%" />
          <div class="m-field__hint">0 表示不限制；持续达到观察窗口即触发告警。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">带宽突发判定窗口 (秒)</label>
          <el-input-number v-model="alertSettings.traffic_duration_sec" :min="5" :max="300" :step="5" style="width: 100%" />
          <div class="m-field__hint">防止网络瞬间波动误报，推荐 10 ~ 60 秒。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">节点瞬时连接总数上限</label>
          <el-input-number v-model="alertSettings.conn_threshold_count" :min="10" :max="50000" :step="50" style="width: 100%" />
          <div class="m-field__hint">全节点活跃连接总数激增预警。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">单个 IP 突发并发上限</label>
          <el-input-number v-model="alertSettings.single_ip_threshold_count" :min="5" :max="5000" :step="10" style="width: 100%" />
          <div class="m-field__hint">防范单一来源 IP 大量恶意占用并发。</div>
        </div>

        <div class="m-field m-field--inline" style="padding: 6px 0">
          <label class="m-field__label">
            异常网络扫描预警
            <span class="m-field__hint">探测多端口嗅探、高危服务端口探测与纯 IP 扫段，已彻底豁免正常网页浏览</span>
          </label>
          <el-switch v-model="alertSettings.scan_alert_enabled" />
        </div>

        <div class="m-field">
          <label class="m-field__label">重复报警静默冷却 (分钟)</label>
          <el-input-number v-model="alertSettings.cooldown_minutes" :min="1" :max="1440" :step="5" style="width: 100%" />
          <div class="m-field__hint">同一节点同一类型的告警在此期间不重复轰炸。</div>
        </div>

        <div class="m-card-actions">
          <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存配置</el-button>
        </div>
      </section>

      <!-- 定时探测与三网巡检 -->
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">定时阻断探测与三网巡检</span>
          <el-switch v-model="alertSettings.auto_probe_gfw_enabled" active-text="定时探测" />
        </div>
        <div class="m-card__note">
          定时从国内真实探针探测境外节点连通性、三网延迟与回国路由线路。
        </div>

        <div class="m-field" style="margin-top: 14px">
          <label class="m-field__label">GFW 阻断探测周期 (分钟)</label>
          <el-input-number v-model="alertSettings.probe_gfw_interval_minutes" :min="1" :max="1440" :step="1" style="width: 100%" />
          <div class="m-field__hint">定时探测节点客户端连接端口是否被阻断。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">三网线路检测周期 (小时)</label>
          <el-input-number v-model="alertSettings.probe_speedtest_interval_hours" :min="1" :max="168" :step="1" style="width: 100%" />
          <div class="m-field__hint">定期检测电信、联通、移动 TCP 延迟及回国线路。</div>
        </div>

        <div class="m-field m-field--inline" style="padding: 6px 0">
          <label class="m-field__label">
            每次探测必推通知
            <span class="m-field__hint">全绿推健康日报，异常推报警，随时掌握节点状态</span>
          </label>
          <el-switch v-model="alertSettings.probe_notify_always" />
        </div>

        <div class="m-field m-field--inline" style="padding: 6px 0">
          <label class="m-field__label">
            业务繁忙时避让检测
            <span class="m-field__hint">节点带宽 > 5MB/s 时自动跳过探测，避免抢占带宽</span>
          </label>
          <el-switch v-model="alertSettings.probe_skip_when_busy" />
        </div>

        <div class="m-card-actions">
          <el-button :loading="gfwProbing" @click="triggerGFWCheck">立即运行探测</el-button>
          <el-button type="primary" :loading="alertSaving" @click="saveAlertSettings">保存配置</el-button>
        </div>
      </section>

      <!-- GFW 探测结果展示 Sheet -->
      <MSheet v-model="gfwSheet" title="GFW 连通性探测结果">
        <div class="m-notice m-notice--info">
          由国内真实探针向境外节点发起 TCP 握手跨越防火墙核验连通性与时延。
        </div>
        <div v-for="item in gfwResults" :key="item.node_id || item.node_name" class="m-item">
          <div class="m-item__hit is-static">
            <div class="m-item__head">
              <span class="m-item__title">{{ item.node_name }}</span>
              <span v-if="item.overseas_ok && !item.domestic_ok" class="m-pill m-pill--danger">疑似阻断</span>
              <span v-else-if="item.domestic_ok" class="m-pill m-pill--success">正常</span>
              <span v-else class="m-pill m-pill--info">离线</span>
            </div>
            <div class="m-item__stats">
              <span class="m-stat"><b>{{ item.target_port || 443 }}</b><small>端口</small></span>
              <span class="m-stat"><b>{{ item.domestic_ok ? `${item.latency_ms} ms` : '—' }}</b><small>国内延迟</small></span>
              <span class="m-stat"><b>{{ item.packet_loss !== undefined ? `${item.packet_loss}%` : '0%' }}</b><small>丢包率</small></span>
            </div>
            <div v-if="item.probe_source || item.status_desc" class="m-item__note">
              {{ item.probe_source ? `探针来源：${item.probe_source}` : '' }} {{ item.status_desc || '' }}
            </div>
          </div>
        </div>
        <div v-if="!gfwResults.length" class="m-empty">暂无探测结果</div>
      </MSheet>
    </template>

    <MActionSheet v-model="actionsOpen" :title="actionTarget?.username" :details="details" :actions="actions" @select="runAction" />

    <MSheet v-model="roleSheet" :title="`修改 ${roleTarget?.username || ''} 的权限`">
      <div class="m-field">
        <label class="m-field__label">权限</label>
        <MPicker v-model="roleDraft" :options="roles" title="选择权限" />
      </div>
      <template #footer>
        <el-button @click="roleSheet = false">取消</el-button>
        <el-button type="primary" @click="saveRole">保存</el-button>
      </template>
    </MSheet>

    <MSheet v-model="totpSetup.open" title="启用两步验证">
      <div class="totp">
        <p>用验证器扫描下面的二维码，或复制密钥手动添加，然后输入 6 位动态码完成绑定。</p>
        <div class="totp__frame"><img :src="totpSetup.qr" alt="两步验证绑定二维码" data-testid="totp-qr" /></div>
        <div class="m-field">
          <label class="m-field__label">无法扫码时，可手动输入此密钥</label>
          <el-input :model-value="totpSetup.secret" readonly class="m-mono" aria-label="无法扫码时，可手动输入此密钥" data-testid="totp-secret" />
        </div>
        <div class="m-field">
          <label class="m-field__label">动态验证码</label>
          <el-input v-model="totpSetup.code" maxlength="6" inputmode="numeric" aria-label="动态验证码" placeholder="000000" />
        </div>
      </div>
      <template #footer>
        <el-button @click="totpSetup.open = false">取消</el-button>
        <el-button type="primary" :loading="totpSetup.loading" @click="enableTOTP">启用</el-button>
      </template>
    </MSheet>

    <MSheet v-model="operatorSheet" title="新建管理账户">
      <div class="m-field">
        <label class="m-field__label">用户名</label>
        <el-input v-model="operator.username" aria-label="用户名" placeholder="3 至 64 位，可用字母、数字、点、下划线和短横线" />
      </div>
      <div class="m-field">
        <label class="m-field__label">初始密码</label>
        <el-input v-model="operator.password" type="password" show-password aria-label="初始密码" />
        <div class="m-field__hint">用户首次登录时必须修改此密码，至少 12 位。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">权限</label>
        <MPicker v-model="operator.role" :options="roles" title="选择权限" />
      </div>
      <template #footer>
        <el-button @click="operatorSheet = false">取消</el-button>
        <el-button type="primary" :disabled="!operator.username || operator.password.length < 12" @click="saveOperator">创建</el-button>
      </template>
    </MSheet>
    <template v-if="isAdmin && tab === 'operators'" #fab>
      <button type="button" class="m-fab" aria-label="新建管理账户" @click="openOperator">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
.wide { width: 100%; height: var(--m-tap); margin: 12px 0 0; }
.totp { text-align: center; }
.totp > p { margin: 0 0 16px; color: var(--sb-muted); font-size: 13px; line-height: 1.7; text-align: left; }
/* 二维码必须黑白，深色底会让验证器扫不出来。 */
.totp__frame { display: inline-block; padding: 10px; margin-bottom: 18px; background: #fff; border-radius: var(--m-radius); line-height: 0; }
.totp__frame img { display: block; width: min(220px, 60vw); height: min(220px, 60vw); }
.totp .m-field { text-align: left; }
.m-card-actions {
  display: flex;
  gap: 10px;
  margin-top: 14px;
}
.m-card-actions .el-button {
  flex: 1;
  height: var(--m-tap);
  margin: 0;
}
</style>
