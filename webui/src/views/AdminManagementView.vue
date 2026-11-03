<script setup>
import { computed, inject, onMounted, ref } from 'vue'
import { Bell, DataAnalysis, Download, Collection, Refresh, Check, Lock, Setting, Upload } from '@element-plus/icons-vue'
import { api, post, put, uploadFile } from '../api'
import { isDark, applyTheme } from '../theme'
import PageHeader from '../components/PageHeader.vue'

const canWrite = inject('canWrite')
const section = computed(() => location.hash.match(/^#\/admin-(notify|data|themes|security|settings|update)/)?.[1] || 'settings')
const loading = ref(false)
const saving = ref(false)
const message = ref('')
const settings = ref({})
const themes = ref([])
const update = ref(null)
const account = ref(null)
const totpSecret = ref('')
const totpCode = ref('')
const disablePassword = ref('')

const meta = computed(() => ({
  notify: { title: '通知', icon: Bell, intro: '配置通知渠道与节点事件提醒，布局对应 ref/web-admin 的通知页。' },
  data: { title: '数据', icon: DataAnalysis, intro: '管理历史数据、数据库备份与恢复，危险操作均需要明确确认。' },
  themes: { title: '主题', icon: Collection, intro: '选择公开页主题并管理主题配置，保持 ref 的主题卡片交互。' },
  security: { title: '安全', icon: Lock, intro: '管理登录保护、GitHub 登录和访问安全设置。' },
  settings: { title: '设置', icon: Setting, intro: '管理站点信息、历史保留周期与站点图标。' },
  update: { title: '更新', icon: Download, intro: '检查控制端版本并向节点下发 Agent 更新。' },
}[section.value]))

async function load() {
  loading.value = true
  try {
    if (section.value === 'themes') themes.value = [
      { short: 'dark', name: '深色主题', description: '科技黑夜间模式，对比鲜明', selected: isDark.value },
      { short: 'light', name: '浅色主题', description: '清新日间模式，高对比易读', selected: !isDark.value },
    ]
    if (section.value === 'notify') settings.value = (await api('/alerts/settings')).settings || {}
    if (section.value === 'security') account.value = await api('/auth/me')
    if (section.value === 'settings') settings.value = { history_days: 30 }
    if (section.value === 'update') update.value = await api('/system/update')
  } finally { loading.value = false }
}
async function save() {
  saving.value = true
  try {
    if (section.value === 'notify') await put('/alerts/settings', settings.value)
    else message.value = '该页面的后端配置接口尚未启用'
    message.value = message.value || '设置已保存'
  } finally { saving.value = false }
}
async function refresh() { await load() }
async function chooseTheme(short) {
  if (!canWrite.value) return
  applyTheme(short)
  await load()
}
async function checkUpdate() { update.value = await api('/system/update'); message.value = '更新状态已刷新' }
async function vacuum() { await post('/db/vacuum', {}); message.value = '历史数据已清理并完成数据库回收' }
async function restore(event) { const file = event.target.files?.[0]; event.target.value = ''; if (!file) return; const form = new FormData(); form.append('backup', file); await uploadFile('/db/restore', form); message.value = '数据库已恢复，请重新加载页面' }
async function applyUpdate() { await post('/system/update/apply', {}); message.value = '已开始应用控制端更新' }
async function beginTotp() { const res = await post('/auth/2fa/setup', {}); totpSecret.value = res.secret || ''; message.value = '已生成验证器密钥，请输入验证码确认启用' }
async function enableTotp() { await post('/auth/2fa/enable', { code: totpCode.value }); message.value = '两步验证已启用'; await load() }
async function disableTotp() { await post('/auth/2fa/disable', { password: disablePassword.value }); message.value = '两步验证已停用'; await load() }
onMounted(load)
</script>

<template>
  <div class="page-shell admin-management-view">
    <PageHeader :title="meta.title">
      <el-button :icon="Refresh" :loading="loading" @click="refresh">刷新</el-button>
    </PageHeader>
    <main class="page-content">
      <section class="admin-hero neu">
        <div class="admin-hero__icon"><el-icon :size="22"><component :is="meta.icon" /></el-icon></div>
        <div><h2>{{ meta.title }}</h2><p>{{ meta.intro }}</p></div>
      </section>

      <section v-if="section === 'themes'" class="admin-card-grid">
        <article v-for="theme in themes" :key="theme.short" class="admin-card neu">
          <div class="admin-card__head"><div><h3>{{ theme.name || theme.short }}</h3><p>{{ theme.description || '可用主题' }}</p></div><el-tag v-if="theme.selected" type="success">当前</el-tag></div>
          <div class="admin-card__actions"><el-button :disabled="theme.selected || !canWrite" @click="chooseTheme(theme.short)">{{ theme.selected ? '使用中' : '使用' }}</el-button><el-button text>配置</el-button></div>
        </article>
        <article v-if="!themes.length" class="admin-card neu"><h3>主题管理接口未启用</h3><p>当前控制端提供 openlat/admin 两套内置风格，运行时切换入口位于侧栏底部。</p></article>
      </section>

      <section v-else-if="section === 'data'" class="admin-card neu admin-form-card">
        <h3>数据库</h3><p class="subtle">清理过期历史、导出数据库或恢复备份。</p>
        <div class="admin-action-row"><el-button tag="a" native-type="button" :icon="Download" href="/api/v1/db/backup">导出数据库</el-button><el-button :icon="Upload" @click="$refs.backupInput?.click()">导入备份</el-button><input ref="backupInput" type="file" accept=".db,application/octet-stream" hidden @change="restore" /><el-button type="danger" @click="vacuum">清理历史数据</el-button></div>
      </section>

      <section v-else-if="section === 'update'" class="admin-card neu admin-form-card">
        <h3>版本与节点更新</h3><p class="subtle">控制端更新检查与 Agent 分批更新。</p>
        <div class="admin-action-row"><el-button type="primary" :icon="Refresh" @click="checkUpdate">检查更新</el-button><el-button :icon="Download" :disabled="!update?.update_available" @click="applyUpdate">应用控制端更新</el-button></div>
        <p v-if="update" class="subtle">当前版本：{{ update.current_version || '未知' }}；最新版本：{{ update.latest_version || '未知' }}</p>
      </section>

      <section v-else-if="section === 'security'" class="admin-card neu admin-form-card">
        <div class="admin-card__head"><div><h3>两步验证</h3><p class="subtle">使用验证器保护管理台登录。</p></div><el-tag :type="account?.totp_enabled ? 'success' : 'info'">{{ account?.totp_enabled ? '已启用' : '未启用' }}</el-tag></div>
        <div v-if="!account?.totp_enabled" class="admin-form">
          <el-button v-if="!totpSecret" type="primary" @click="beginTotp">生成验证器密钥</el-button>
          <template v-else><el-alert :title="`密钥：${totpSecret}`" type="info" show-icon /><el-input v-model="totpCode" class="mt-3" placeholder="输入验证器验证码" /><el-button class="mt-3" type="primary" @click="enableTotp">确认启用</el-button></template>
        </div>
        <div v-else class="admin-form"><el-input v-model="disablePassword" type="password" show-password placeholder="输入当前密码以停用" /><el-button class="mt-3" type="danger" @click="disableTotp">停用两步验证</el-button></div>
      </section>

      <section v-else class="admin-card neu admin-form-card">
        <div class="admin-card__head"><div><h3>{{ section === 'settings' ? '站点设置' : `${meta.title}设置` }}</h3><p class="subtle">表单布局与 ref 管理台保持分区卡片结构。</p></div><el-button v-if="canWrite" type="primary" :icon="Check" :loading="saving" @click="save">保存</el-button></div>
        <el-form label-position="top" class="admin-form">
          <el-form-item label="启用配置"><el-switch v-model="settings.enabled" :disabled="!canWrite" /></el-form-item>
          <el-form-item label="说明"><el-input v-model="settings.description" :disabled="!canWrite" placeholder="请输入说明" /></el-form-item>
          <el-form-item v-if="section === 'notify'" label="通知地址"><el-input v-model="settings.notify_webhook" :disabled="!canWrite" placeholder="Webhook URL" /></el-form-item>
          <el-form-item v-if="section === 'security'" label="登录保护"><el-switch v-model="settings.totp_required" :disabled="!canWrite" /></el-form-item>
          <el-form-item v-if="section === 'settings'" label="历史数据保留天数"><el-input-number v-model="settings.history_days" :min="1" :max="3650" :disabled="!canWrite" /></el-form-item>
        </el-form>
      </section>
      <el-alert v-if="message" :title="message" type="success" show-icon closable @close="message = ''" />
    </main>
  </div>
</template>

<style scoped>
.admin-management-view .page-content { gap: 16px; }
.admin-hero { display:flex; align-items:center; gap:14px; padding:20px; border-radius:16px; }
.admin-hero__icon { display:grid; place-items:center; width:44px; height:44px; color:var(--sb-accent); background:var(--sb-accent-soft); border-radius:12px; }
.admin-hero h2,.admin-card h3 { margin:0; color:var(--sb-text); font-size:16px; font-weight:650; }
.admin-hero p,.admin-card p { margin:5px 0 0; color:var(--sb-muted); font-size:13px; line-height:1.6; }
.admin-card-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px; }
.admin-card { padding:20px; border-radius:16px; }
.admin-card__head { display:flex; align-items:flex-start; justify-content:space-between; gap:14px; }
.admin-card__actions,.admin-action-row { display:flex; flex-wrap:wrap; gap:10px; margin-top:18px; }
.admin-form-card { max-width:900px; }
.admin-form { margin-top:20px; max-width:620px; }
@media (max-width:700px) { .admin-card-grid { grid-template-columns:1fr; } }
</style>
