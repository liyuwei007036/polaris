<script setup>
import { computed, inject, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Collection, Connection, Document, Lock, Monitor, Operation, Refresh, Setting, Share, Tickets, User,
} from '@element-plus/icons-vue'
import { post } from '../../api'
import MPage from '../components/MPage.vue'
import MSheet from '../components/MSheet.vue'

// 底部四个入口之外的页面都在这里，按「改哪一端」分组：
// 上面一组改服务器那一侧，中间一组改下发给客户端的东西，最后一组是平台自身。
const appState = inject('appState')
const isAdmin = inject('isAdmin')
const navigate = inject('navigate')
const logout = inject('logout')

const groups = [
  {
    label: '服务器',
    items: [
      { id: 'nodes', label: '服务器', icon: Monitor },
      { id: 'security', label: '网络防护', icon: Lock },
      { id: 'routes', label: '服务器访问规则', icon: Operation },
      { id: 'outbounds', label: '上网出口', icon: Connection },
    ],
  },
  {
    label: '连接配置',
    items: [
      { id: 'proxy-groups', label: '代理分组', icon: Share },
      { id: 'rule-providers', label: '规则供应商', icon: Collection },
    ],
  },
  {
    label: '系统管理',
    items: [
      { id: 'audit', label: '操作记录', icon: Tickets },
      { id: 'cloudflare', label: '域名解析', icon: Document },
      { id: 'settings', label: '系统设置', icon: Setting },
    ],
  },
]

const roleLabel = computed(() => ({ admin: '管理员', operator: '运维人员', viewer: '只读用户' })[appState.role] || appState.role)

// 版本与更新从系统设置搬到这里：手机上没有侧栏品牌区，「更多」页顶部是
// 跟桌面版品牌区对应的位置。
const loadSystemUpdate = inject('loadSystemUpdate')
const systemUpdate = computed(() => appState.systemUpdate)
const updateOpen = ref(false)
const updateChecking = ref(false)
const updateApplying = ref(false)

async function checkSystemUpdate() {
  updateChecking.value = true
  try {
    await loadSystemUpdate(true)
    if (systemUpdate.value?.check_error) ElMessage.warning('检查更新失败：' + systemUpdate.value.check_error)
    else if (systemUpdate.value?.update_available) ElMessage.success(`发现新版本 v${systemUpdate.value.latest_version}`)
    else ElMessage.success('当前已是最新版本')
  } finally { updateChecking.value = false }
}

async function applySystemUpdate() {
  await ElMessageBox.confirm(
    `将 master 更新到 v${systemUpdate.value.latest_version}？更新过程中管理平台会重启，约需十几秒，期间控制台短暂不可用。已接入的服务器不受影响。`,
    '更新管理平台',
    { type: 'warning', confirmButtonText: '立即更新' },
  )
  updateApplying.value = true
  try {
    await post('/system/update/apply', {})
    ElMessage.success('更新已开始，管理平台正在重启，页面将在 20 秒后自动刷新')
    setTimeout(() => location.reload(), 20000)
  } finally { updateApplying.value = false }
}

async function confirmLogout() {
  try {
    await ElMessageBox.confirm('退出后需要重新输入用户名和密码。', '退出登录', { type: 'warning', confirmButtonText: '退出' })
  } catch {
    return
  }
  await logout()
}
</script>

<template>
  <MPage>
    <section class="m-card who">
      <span class="who__avatar"><el-icon :size="20"><User /></el-icon></span>
      <span class="who__info">
        <strong>{{ appState.username }}</strong>
        <small>{{ roleLabel }}{{ isAdmin ? '' : ' · 部分操作不可用' }}</small>
      </span>
    </section>

    <template v-for="group in groups" :key="group.label">
      <div class="m-section">{{ group.label }}</div>
      <div class="entry-group">
        <button
          v-for="item in group.items"
          :key="item.id"
          type="button"
          class="entry"
          @click="navigate(item.id)"
        >
          <el-icon :size="17"><component :is="item.icon" /></el-icon>
          <span>{{ item.label }}</span>
          <i aria-hidden="true">›</i>
        </button>
      </div>
    </template>

    <div class="m-section">关于</div>
    <div class="entry-group">
      <button type="button" class="entry" @click="updateOpen = true">
        <el-icon :size="17"><Refresh /></el-icon>
        <span>版本与更新</span>
        <em class="entry__value">
          v{{ systemUpdate?.current_version || '—' }}
          <i v-if="systemUpdate?.update_available" class="entry__dot" aria-hidden="true" />
        </em>
        <i aria-hidden="true">›</i>
      </button>
    </div>

    <el-button class="sign-out" @click="confirmLogout">退出登录</el-button>

    <MSheet v-model="updateOpen" title="版本与更新">
      <div class="m-field"><span>当前版本 v{{ systemUpdate?.current_version || '未知' }}</span></div>
      <div class="m-field__hint" :class="{ 'm-danger': systemUpdate?.check_error }">
        <template v-if="systemUpdate?.check_error">检查更新失败：{{ systemUpdate.check_error }}</template>
        <template v-else-if="systemUpdate?.update_available">
          最新版本 v{{ systemUpdate.latest_version }}。更新控制端后，可在「服务器」页面逐台升级 agent。
        </template>
        <template v-else>已是最新版本。打开控制台时会自动检查。</template>
      </div>
      <el-button class="wide" :loading="updateChecking" @click="checkSystemUpdate">重新检查</el-button>
      <el-button
        v-if="isAdmin && systemUpdate?.update_available"
        type="primary" class="wide" :loading="updateApplying"
        @click="applySystemUpdate"
      >更新管理平台</el-button>
    </MSheet>
  </MPage>
</template>

<style scoped>
.who { display: flex; align-items: center; gap: 12px; }
.who__avatar {
  flex: none;
  width: 40px;
  height: 40px;
  display: grid;
  place-items: center;
  color: var(--sb-accent);
  background: rgba(56, 189, 248, .12);
  border: 1px solid rgba(56, 189, 248, .22);
  border-radius: 11px;
}
.who__info { min-width: 0; }
.who__info strong { display: block; color: var(--sb-text); font-size: 15px; }
.who__info small { display: block; margin-top: 3px; color: var(--sb-muted); font-size: 12.5px; }

.entry-group {
  overflow: hidden;
  background: var(--sb-panel-solid);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
}
.entry {
  width: 100%;
  min-height: var(--m-tap);
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 12px 14px;
  color: var(--sb-text);
  background: transparent;
  border: 0;
  font: inherit;
  font-size: 14.5px;
  cursor: pointer;
}
.entry + .entry { border-top: 1px solid var(--sb-line); }
.entry:active { background: rgba(148, 163, 184, .10); }
.entry .el-icon { flex: none; color: var(--sb-accent); }
.entry span { flex: 1; min-width: 0; text-align: left; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entry i { flex: none; color: var(--sb-muted); font-style: normal; font-size: 18px; }
.entry__value { flex: none; display: inline-flex; align-items: center; gap: 6px; color: var(--sb-muted); font-style: normal; font-size: 13px; }
.entry__dot { width: 6px; height: 6px; border-radius: 50%; background: #fbbf24; }

.sign-out { width: 100%; height: var(--m-tap); margin-top: 22px; }
.wide { width: 100%; height: var(--m-tap); margin: 12px 0 0; }
</style>
