<script setup>
import { computed, inject, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Search } from '@element-plus/icons-vue'
import { api, post, put } from '../../api'
import { formatDateTime, includesText } from '../../format'
import MPage from '../components/MPage.vue'
import MSegmented from '../components/MSegmented.vue'
import MSheet from '../components/MSheet.vue'
import MPicker from '../components/MPicker.vue'
import MActionSheet from '../components/MActionSheet.vue'

const isAdmin = inject('isAdmin')
const loading = ref(false)

const initialTab = () => {
  const hash = location.hash || ''
  if (hash.includes('alerts')) return 'alerts'
  return 'operators'
}
const tab = ref(initialTab())
const operators = ref([])
const operatorSheet = ref(false)
const operator = reactive({ username: '', password: '', role: 'operator' })
const operatorKeyword = ref('')
const actionsOpen = ref(false)
const actionTarget = ref(null)
const roleSheet = ref(false)
const roleTarget = ref(null)
const roleDraft = ref('operator')

// 通知设置相关状态
const alertSettings = reactive({
  bark_server: 'https://api.day.app',
  bark_device_key: '',
  bark_sound: 'minuet.caf',
  bark_group: 'Polaris',
  bark_url: '',
  telegram_enabled: false,
  telegram_server: 'https://api.telegram.org',
  telegram_bot_token: '',
  telegram_chat_id: '',
  traffic_alert_enabled: true,
  traffic_threshold_mbps: 50,
  traffic_duration_sec: 10,
  conn_alert_enabled: true,
  conn_threshold_count: 200,
  single_ip_alert_enabled: true,
  single_ip_threshold_count: 60,
  offline_alert_enabled: true,
  cooldown_minutes: 15,
  scan_alert_enabled: false,
})
const alertSaving = ref(false)
const testSending = ref(false)
const telegramReady = computed(() => Boolean(
  alertSettings.telegram_enabled
  && alertSettings.telegram_bot_token.trim()
  && alertSettings.telegram_chat_id.trim(),
))
const notifierReady = computed(() => Boolean(alertSettings.bark_device_key.trim()) || telegramReady.value)

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
const tabs = computed(() => [
  { value: 'operators', label: '管理账户' },
  { value: 'alerts', label: '通知设置' },
])

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
    ElMessage.success('通知设置已保存')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '保存失败')
  } finally {
    alertSaving.value = false
  }
}

async function sendTestAlert() {
  if (!notifierReady.value) {
    return ElMessage.warning('请先填写 Bark Device Key，或启用并配置 Telegram')
  }
  testSending.value = true
  try {
    await put('/alerts/settings', alertSettings).catch(() => {})
    const res = await post('/alerts/test', {
      server: alertSettings.bark_server,
      device_key: alertSettings.bark_device_key,
      sound: alertSettings.bark_sound,
      group: alertSettings.bark_group,
      url: alertSettings.bark_url,
      telegram_server: alertSettings.telegram_server,
      telegram_bot_token: alertSettings.telegram_bot_token,
      telegram_chat_id: alertSettings.telegram_chat_id,
    })
    ElMessage.success(res?.message || '测试通知已发出')
  } catch (error) {
    ElMessage.error(error instanceof Error ? error.message : '发送测试通知失败')
  } finally {
    testSending.value = false
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
  <MPage title="系统设置" :loading="loading">
    <template #actions>
      <button type="button" class="m-header__btn" :disabled="loading" aria-label="刷新" @click="load">
        <el-icon><Refresh /></el-icon>
      </button>
    </template>
    <template v-if="isAdmin">
    <MSegmented v-model="tab" :options="tabs" />

    <template v-if="tab === 'operators'">
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
          以简洁的 iOS 系统通知推送关键信息，按告警类型自动分组，并严格遵循 iPhone 物理静音开关。
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

      <!-- Telegram 推送配置 -->
      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">Telegram 消息推送</span>
          <el-switch v-model="alertSettings.telegram_enabled" />
        </div>
        <div class="m-card__note">
          通过 Telegram 机器人发送富文本告警，包含字段化详情与处理建议，可与 Bark 同时启用。
        </div>

        <div class="m-field" style="margin-top: 14px">
          <label class="m-field__label">Bot API 地址</label>
          <el-input v-model="alertSettings.telegram_server" placeholder="https://api.telegram.org" clearable />
          <div class="m-field__hint">使用官方服务保持默认；自建 Bot API 或反向代理时填写完整地址。</div>
        </div>

        <div class="m-field">
          <label class="m-field__label">Bot Token <em>*</em></label>
          <el-input v-model="alertSettings.telegram_bot_token" show-password placeholder="由 @BotFather 生成的 Token" clearable />
        </div>

        <div class="m-field">
          <label class="m-field__label">Chat ID <em>*</em></label>
          <el-input v-model="alertSettings.telegram_chat_id" placeholder="例如 123456789 或 -1001234567890" clearable />
          <div class="m-field__hint">私聊为数字 ID，群组通常以 -100 开头；公开频道也可填 @频道名。</div>
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

      <section class="m-card">
        <div class="m-card__top">
          <span class="m-card__title">每日简报</span>
        </div>
        <div class="m-card__note">
          每天 09:00 自动发送昨日简报，包含各节点网络质量、活跃设备排行、热门访问目标、节点使用排行与流量汇总。
        </div>
      </section>
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
    </template>
    <div v-else class="m-empty">系统设置仅管理员可访问</div>
    <template v-if="isAdmin && tab === 'operators'" #fab>
      <button type="button" class="m-fab" aria-label="新建管理账户" @click="openOperator">
        <el-icon :size="24"><Plus /></el-icon>
      </button>
    </template>
  </MPage>
</template>

<style scoped>
.wide { width: 100%; height: var(--m-tap); margin: 12px 0 0; }
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
