<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide, reactive, ref } from 'vue'
import {
  Aim,
  Collection,
  Connection,
  DataAnalysis,
  Document,
  Link,
  List,
  Lock,
  Monitor,
  Operation,
  Setting,
  Share,
  SwitchButton,
  Tickets,
  User,
} from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, post, setCsrfToken } from './api'
import { closeLiveEvents, subscribeLive } from './live'
import { closeConnectionEvents } from './connections'
import { brandName, brandTagline } from './brand'
import BrandMark from './components/BrandMark.vue'
import LoginView from './views/LoginView.vue'
import PasswordChangeView from './views/PasswordChangeView.vue'

const authenticated = ref(false)
const checking = ref(true)
const viewAliases = { 'ingress-routes': 'inbounds' }
const requestedView = location.hash.replace(/^#\/?/, '') || 'dashboard'
const currentView = ref(viewAliases[requestedView] || requestedView)
const appState = reactive({
  username: '',
  role: '',
  totp_enabled: false,
  must_change_password: false,
  nodes: [],
  systemUpdate: null,
})
// 版本号和更新都挂在侧栏品牌区：谁在跑哪个版本是每次打开控制台都想扫一眼的
// 事，藏进系统设置反而要多点两下。
const updateChecking = ref(false)
const updateApplying = ref(false)

const groups = [
  {
    label: '工作台',
    items: [
      { id: 'dashboard', label: '运行概览', icon: DataAnalysis },
      { id: 'nodes', label: '服务器', icon: Monitor },
    ],
  },
  {
    label: '连接配置',
    items: [
      { id: 'inbounds', label: '接入服务', icon: Aim },
      { id: 'proxy-groups', label: '代理分组', icon: Share },
      { id: 'rule-providers', label: '规则供应商', icon: Collection },
      { id: 'subscriptions', label: '客户端配置', icon: Link },
      { id: 'routes', label: '服务器访问规则', icon: Operation },
      { id: 'outbounds', label: '上网出口', icon: Connection },
    ],
  },
  {
    label: '状态与记录',
    items: [
      { id: 'connections', label: '当前连接', icon: List },
      { id: 'audit', label: '操作记录', icon: Tickets },
    ],
  },
  {
    label: '系统管理',
    items: [
      { id: 'security', label: '网络防护', icon: Lock },
      { id: 'cloudflare', label: '域名解析', icon: Document },
      { id: 'settings', label: '系统设置', icon: Setting },
    ],
  },
]

// Views load on demand. Bundling them together meant every visitor
// downloaded the charting and QR-code libraries before the first page could
// render, which is most of what made the console slow to open.
const views = {
  dashboard: defineAsyncComponent(() => import('./views/DashboardView.vue')),
  nodes: defineAsyncComponent(() => import('./views/NodesView.vue')),
  inbounds: defineAsyncComponent(() => import('./views/InboundsView.vue')),
  'proxy-groups': defineAsyncComponent(() => import('./views/ProxyGroupsView.vue')),
  'rule-providers': defineAsyncComponent(() => import('./views/RuleProvidersView.vue')),
  subscriptions: defineAsyncComponent(() => import('./views/SubscriptionsView.vue')),
  routes: defineAsyncComponent(() => import('./views/RoutesView.vue')),
  outbounds: defineAsyncComponent(() => import('./views/OutboundsView.vue')),
  connections: defineAsyncComponent(() => import('./views/ConnectionsView.vue')),
  security: defineAsyncComponent(() => import('./views/SecurityView.vue')),
  cloudflare: defineAsyncComponent(() => import('./views/CloudflareView.vue')),
  audit: defineAsyncComponent(() => import('./views/AuditView.vue')),
  settings: defineAsyncComponent(() => import('./views/SettingsView.vue')),
}

const activeComponent = computed(() => views[currentView.value] || views.dashboard)
const roleLabel = computed(() => ({ admin: '管理员', operator: '运维人员', viewer: '只读用户' })[appState.role] || appState.role)
const canWrite = computed(() => appState.role === 'admin' || appState.role === 'operator')
const isAdmin = computed(() => appState.role === 'admin')
let stopAppLive

async function loadNodes() {
  if (!authenticated.value) return []
  const result = await api('/nodes')
  appState.nodes = result.nodes || []
  return appState.nodes
}

async function loadSystemUpdate(refresh = false) {
  if (!authenticated.value) return null
  appState.systemUpdate = await api(refresh ? '/system/update?refresh=1' : '/system/update')
  return appState.systemUpdate
}

async function checkSystemUpdate() {
  updateChecking.value = true
  try {
    await loadSystemUpdate(true)
    if (appState.systemUpdate?.check_error) ElMessage.warning('检查更新失败：' + appState.systemUpdate.check_error)
    else if (appState.systemUpdate?.update_available) ElMessage.success(`发现新版本 v${appState.systemUpdate.latest_version}`)
    else ElMessage.success('当前已是最新版本')
  } finally { updateChecking.value = false }
}

async function applySystemUpdate() {
  await ElMessageBox.confirm(
    `将 master 更新到 v${appState.systemUpdate.latest_version}？更新过程中管理平台会重启，约需十几秒，期间控制台短暂不可用。已接入的服务器不受影响。`,
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

async function checkSession() {
  checking.value = true
  try {
    const session = await api('/auth/me')
    setAuthenticated(session)
  } catch {
    clearSession()
  } finally {
    checking.value = false
  }
}

function setAuthenticated(session) {
  setCsrfToken(session.csrf_token)
  appState.username = session.username
  appState.role = session.role
  appState.totp_enabled = Boolean(session.totp_enabled)
  appState.must_change_password = Boolean(session.must_change_password)
  authenticated.value = true
  if (appState.must_change_password) return
  loadNodes().catch(() => {})
  loadSystemUpdate().catch(() => {})
  stopAppLive?.()
  stopAppLive = subscribeLive((event) => {
    if (event.kind === 'node') loadNodes().catch(() => {})
  })
}

function clearSession() {
  setCsrfToken('')
  authenticated.value = false
  appState.username = ''
  appState.role = ''
  appState.totp_enabled = false
  appState.must_change_password = false
  appState.nodes = []
  stopAppLive?.()
  stopAppLive = undefined
  closeLiveEvents()
  closeConnectionEvents()
}

async function logout() {
  try {
    await post('/auth/logout', {})
  } finally {
    clearSession()
  }
}

function navigate(view) {
  currentView.value = views[view] ? view : 'dashboard'
  location.hash = `#/${currentView.value}`
}

function onHashChange() {
	const view = location.hash.replace(/^#\/?/, '') || 'dashboard'
	const resolved = viewAliases[view] || view
	currentView.value = views[resolved] ? resolved : 'dashboard'
}

function onUnauthorized() {
  clearSession()
}

provide('appState', appState)
provide('canWrite', canWrite)
provide('isAdmin', isAdmin)
provide('loadNodes', loadNodes)
provide('loadSystemUpdate', loadSystemUpdate)
provide('navigate', navigate)

onMounted(() => {
  window.addEventListener('hashchange', onHashChange)
  window.addEventListener('sb:unauthorized', onUnauthorized)
  checkSession()
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', onHashChange)
  window.removeEventListener('sb:unauthorized', onUnauthorized)
})
</script>

<template>
  <div v-if="checking" class="boot-screen">
    <el-icon class="is-loading" :size="28"><Operation /></el-icon>
    <span>正在载入</span>
  </div>

  <LoginView v-else-if="!authenticated" @authenticated="setAuthenticated" />

  <PasswordChangeView
    v-else-if="appState.must_change_password"
    :username="appState.username"
    @changed="setAuthenticated"
  />

  <el-container v-else class="app-layout">
    <el-aside width="232px" class="app-sidebar">
      <el-popover placement="right-start" :width="308" trigger="click" popper-class="update-popover">
        <template #reference>
          <button type="button" class="app-brand" :aria-label="`${brandName} ${brandTagline}，查看版本与更新`">
            <span class="app-brand__mark"><BrandMark :size="21" /></span>
            <span class="app-brand__text">
              <strong>{{ brandName }}</strong>
              <small>
                {{ brandTagline }}
                <em class="app-brand__version">
                  v{{ appState.systemUpdate?.current_version || '—' }}
                  <i v-if="appState.systemUpdate?.update_available" class="app-brand__dot" aria-hidden="true" />
                </em>
              </small>
            </span>
          </button>
        </template>
        <div class="update-pop">
          <h4>软件版本</h4>
          <p>当前版本 <strong>v{{ appState.systemUpdate?.current_version || '未知' }}</strong></p>
          <p v-if="appState.systemUpdate?.check_error" class="update-pop__warn">检查更新失败：{{ appState.systemUpdate.check_error }}</p>
          <p v-else-if="appState.systemUpdate?.update_available">
            最新版本 <strong>v{{ appState.systemUpdate.latest_version }}</strong>。更新控制端后，可在「服务器」页面逐台升级 agent。
          </p>
          <p v-else>已是最新版本。打开控制台时会自动检查。</p>
          <div class="update-pop__actions">
            <el-button size="small" :loading="updateChecking" @click="checkSystemUpdate">重新检查</el-button>
            <el-button
              v-if="isAdmin && appState.systemUpdate?.update_available"
              size="small" type="primary" :loading="updateApplying"
              @click="applySystemUpdate"
            >更新管理平台</el-button>
          </div>
        </div>
      </el-popover>

      <el-scrollbar class="app-menu-scroll">
        <div v-for="group in groups" :key="group.label" class="menu-group">
          <div class="menu-group__label">{{ group.label }}</div>
          <button
            v-for="item in group.items"
            :key="item.id"
            type="button"
            class="menu-item"
            :class="{ active: currentView === item.id }"
            @click="navigate(item.id)"
          >
            <el-icon><component :is="item.icon" /></el-icon>
            <span>{{ item.label }}</span>
          </button>
        </div>
      </el-scrollbar>

      <div class="operator-panel">
        <span class="operator-avatar"><User /></span>
        <span class="operator-info">
          <strong>{{ appState.username }}</strong>
          <small>{{ roleLabel }}</small>
        </span>
        <el-tooltip content="退出登录" placement="top">
          <el-button text circle :icon="SwitchButton" @click="logout" />
        </el-tooltip>
      </div>
    </el-aside>

    <el-main class="app-main">
      <!-- out-in keeps exactly one page mounted at a time. It only works
           because the leave half of the transition is defined; without it
           Vue waits for an end event that never arrives and navigation
           freezes on the page you were leaving. -->
      <transition name="page" mode="out-in">
        <component :is="activeComponent" :key="currentView" />
      </transition>
    </el-main>
  </el-container>
</template>

<style scoped>
.boot-screen {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: var(--sb-muted);
  letter-spacing: 1px;
}
.app-layout { width: 100%; height: 100%; }
.app-sidebar {
  position: relative;
  display: flex;
  flex-direction: column;
  /* el-aside 默认 overflow: auto，内容稍宽就会在侧边栏底部出现横向滚动条。 */
  overflow: hidden;
  color: var(--sb-text-2);
  background: linear-gradient(180deg, #0b1120, var(--sb-sidebar));
  border-right: 1px solid var(--sb-line);
}
/* 侧边栏右缘的一道渐隐高光，与页头的强调线呼应。
   注意贴着 right: 0 画，越界哪怕 1px 都会让侧边栏多出一条横向滚动条。 */
.app-sidebar::after {
  content: "";
  position: absolute;
  top: 0;
  right: 0;
  width: 1px;
  height: 240px;
  background: linear-gradient(180deg, var(--sb-accent), transparent);
  opacity: .5;
  pointer-events: none;
}
.app-brand {
  width: 100%;
  height: 64px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 0 20px;
  color: inherit;
  background: transparent;
  border: 0;
  border-bottom: 1px solid var(--sb-line);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.app-brand:hover { background: rgba(148, 163, 184, .08); }
.app-brand__mark {
  width: 36px;
  height: 36px;
  flex: none;
  display: grid;
  place-items: center;
  color: #04121f;
  background: linear-gradient(135deg, var(--sb-accent), var(--sb-accent-2));
  border-radius: 10px;
  box-shadow: 0 0 20px -4px rgba(56, 189, 248, .85);
}
.app-brand__text { min-width: 0; }
.app-brand strong { display: block; color: #fff; font-size: 16px; line-height: 1.2; letter-spacing: .6px; }
.app-brand small { display: flex; align-items: center; margin-top: 2px; color: var(--sb-muted); font-size: 10px; letter-spacing: 3px; }
/* 版本号自己取消字距，否则会跟着“控 制 台”一起被拉散。 */
.app-brand__version { display: inline-flex; align-items: center; gap: 4px; margin-left: 7px; font-style: normal; letter-spacing: .2px; }
.app-brand__dot { width: 6px; height: 6px; border-radius: 50%; background: #fbbf24; box-shadow: 0 0 6px rgba(251, 191, 36, .9); }
.app-menu-scroll { flex: 1; min-height: 0; padding: 10px 0; }
/* 菜单只纵向滚动：窄屏折叠时文字被裁掉会撑出一条横向滚动条。 */
.app-menu-scroll :deep(.el-scrollbar__wrap) { overflow-x: hidden; }
.app-menu-scroll :deep(.el-scrollbar__view) { min-width: 0; }
.app-menu-scroll :deep(.el-scrollbar__bar.is-horizontal) { display: none; }
.menu-group { padding: 4px 9px 7px; }
.menu-group__label { padding: 7px 10px; color: #5f6d85; font-size: 10px; font-weight: 600; letter-spacing: 1.4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.menu-item {
  position: relative;
  width: 100%;
  height: 38px;
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 0 12px;
  color: #94a3b8;
  background: transparent;
  border: none;
  border-radius: 8px;
  cursor: pointer;
  transition: color .14s ease, background .14s ease;
}
.menu-item:hover { color: #fff; background: rgba(148, 163, 184, .08); }
.menu-item.active {
  color: #fff;
  background: linear-gradient(90deg, rgba(56, 189, 248, .18), rgba(99, 102, 241, .06));
  box-shadow: inset 0 0 0 1px rgba(56, 189, 248, .22);
}
.menu-item.active::before {
  content: "";
  position: absolute;
  left: -9px;
  top: 50%;
  width: 3px;
  height: 20px;
  transform: translateY(-50%);
  border-radius: 0 3px 3px 0;
  background: var(--sb-accent);
  box-shadow: 0 0 12px rgba(56, 189, 248, .9);
}
.menu-item.active .el-icon { color: var(--sb-accent); }
.menu-item .el-icon { flex: none; font-size: 17px; }
.menu-item span { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.operator-panel {
  height: 62px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 14px;
  border-top: 1px solid var(--sb-line);
}
.operator-avatar {
  width: 34px;
  height: 34px;
  flex: none;
  display: grid;
  place-items: center;
  color: var(--sb-accent);
  background: rgba(56, 189, 248, .12);
  border: 1px solid rgba(56, 189, 248, .22);
  border-radius: 9px;
}
.operator-info { flex: 1; min-width: 0; }
.operator-info strong, .operator-info small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.operator-info strong { color: var(--sb-text); font-size: 12px; }
.operator-info small { color: var(--sb-muted); font-size: 11px; margin-top: 2px; }
.operator-panel :deep(.el-button) { color: var(--sb-muted); }
.operator-panel :deep(.el-button:hover) { color: var(--sb-danger); }
.app-main { --el-main-padding: 0; position: relative; min-width: 0; height: 100%; overflow: hidden; display: flex; flex-direction: column; background: transparent; }
/* 工作区背景的网格纹理，纯装饰。 */
.app-main::before {
  content: "";
  position: absolute;
  inset: 0;
  background-image:
    linear-gradient(rgba(148, 163, 184, .045) 1px, transparent 1px),
    linear-gradient(90deg, rgba(148, 163, 184, .045) 1px, transparent 1px);
  background-size: 48px 48px;
  mask-image: radial-gradient(120% 90% at 50% 0%, #000 20%, transparent 85%);
  pointer-events: none;
}
.app-main > * { position: relative; }
.app-main > :last-child { flex: 1; min-height: 0; }
@media (max-width: 900px) {
  .app-sidebar { width: 72px !important; }
  .app-brand { justify-content: center; padding: 0; }
  .app-brand__text, .menu-group__label, .menu-item span, .operator-info { display: none; }
  .menu-group { padding: 4px 8px 7px; }
  .menu-item { justify-content: center; padding: 0; }
  .menu-item.active::before { left: -8px; }
  .operator-panel { justify-content: center; padding: 0; }
  .operator-panel :deep(.el-button) { display: none; }
}

@media (max-width: 520px) {
  .app-sidebar { width: 60px !important; }
  .app-brand__mark { width: 32px; height: 32px; }
}
</style>
