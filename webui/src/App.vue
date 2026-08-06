<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide, reactive, ref } from 'vue'
import {
  Aim,
  Connection,
  DataAnalysis,
  Document,
  Key,
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
import { api, post, setCsrfToken } from './api'
import { closeLiveEvents, liveStatus, subscribeLive } from './live'
import { closeConnectionEvents } from './connections'
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
const updateBannerDismissed = ref(false)

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
const liveLabel = computed(() => liveStatus.value === 'connected' ? '数据连接正常' : '正在重新连接')
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
    <span>正在打开管理平台</span>
  </div>

  <LoginView v-else-if="!authenticated" @authenticated="setAuthenticated" />

  <PasswordChangeView
    v-else-if="appState.must_change_password"
    :username="appState.username"
    @changed="setAuthenticated"
  />

  <el-container v-else class="app-layout">
    <el-aside width="232px" class="app-sidebar">
      <div class="app-brand">
        <span class="app-brand__mark"><Key /></span>
        <span>
          <strong>sb-control</strong>
          <small>服务器连接管理</small>
        </span>
      </div>

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

      <div class="live-panel">
        <span class="status-dot" :class="liveStatus === 'connected' ? 'online' : 'offline'" />
        <span>{{ liveLabel }}</span>
      </div>
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
      <div v-if="appState.systemUpdate?.update_available && !updateBannerDismissed" class="update-banner">
        <span>发现新版本 v{{ appState.systemUpdate.latest_version }}（当前 v{{ appState.systemUpdate.current_version }}），可前往系统设置执行更新。</span>
        <el-button size="small" type="primary" @click="navigate('settings')">前往系统设置</el-button>
        <el-button size="small" text @click="updateBannerDismissed = true">暂不提醒</el-button>
      </div>
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
  color: #667085;
  background: #f4f6f9;
}
.app-layout { width: 100%; height: 100%; }
.app-sidebar {
  display: flex;
  flex-direction: column;
  color: #d5d9e2;
  background: var(--sb-sidebar);
  border-right: 1px solid #273244;
}
.app-brand {
  height: 64px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 0 20px;
  border-bottom: 1px solid rgba(255,255,255,.08);
}
.app-brand__mark {
  width: 36px;
  height: 36px;
  display: grid;
  place-items: center;
  color: #fff;
  background: var(--sb-accent);
  border-radius: 8px;
}
.app-brand strong { display: block; color: #fff; font-size: 16px; line-height: 1.2; }
.app-brand small { color: #778197; font-size: 9px; letter-spacing: 0; }
.app-menu-scroll { flex: 1; min-height: 0; padding: 10px 0; }
.menu-group { padding: 4px 9px 7px; }
.menu-group__label { padding: 7px 10px; color: #687386; font-size: 11px; letter-spacing: 0; }
.menu-item {
  width: 100%;
  height: 38px;
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 0 12px;
  color: #aab3c2;
  background: transparent;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  transition: color .14s ease, background .14s ease;
}
.menu-item:hover { color: #fff; background: rgba(255,255,255,.055); }
.menu-item.active { color: #fff; background: #263449; box-shadow: inset 3px 0 0 var(--sb-accent); }
.menu-item .el-icon { font-size: 17px; }
.live-panel {
  height: 36px;
  display: flex;
  align-items: center;
  padding: 0 16px;
  color: #8f9bad;
  border-top: 1px solid rgba(255,255,255,.06);
  font-size: 11px;
}
.live-panel .status-dot { flex: none; }
.operator-panel {
  height: 62px;
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 14px;
  border-top: 1px solid rgba(255,255,255,.08);
}
.operator-avatar {
  width: 34px;
  height: 34px;
  display: grid;
  place-items: center;
  color: #fff;
  background: #334155;
  border-radius: 7px;
}
.operator-info { flex: 1; min-width: 0; }
.operator-info strong, .operator-info small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.operator-info strong { color: #e5e7eb; font-size: 12px; }
.operator-info small { color: #7f8a9e; font-size: 11px; margin-top: 2px; }
.operator-panel :deep(.el-button) { color: #8c96a8; }
.app-main { --el-main-padding: 0; min-width: 0; height: 100%; overflow: hidden; background: #f4f6f9; display: flex; flex-direction: column; }
.app-main > :last-child { flex: 1; min-height: 0; }
.update-banner {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 20px;
  color: #92400e;
  background: #fef3c7;
  border-bottom: 1px solid #fcd34d;
  font-size: 13px;
}
.update-banner > span { flex: 1; min-width: 0; }

@media (max-width: 900px) {
  .app-sidebar { width: 72px !important; }
  .app-brand { justify-content: center; padding: 0; }
  .app-brand > span:last-child, .menu-group__label, .menu-item span, .operator-info, .live-panel span:last-child { display: none; }
  .menu-group { padding: 4px 8px 7px; }
  .menu-item { justify-content: center; padding: 0; }
  .menu-item.active { box-shadow: inset 2px 0 0 var(--sb-accent); }
  .live-panel, .operator-panel { justify-content: center; padding: 0; }
  .operator-panel :deep(.el-button) { display: none; }
}

@media (max-width: 520px) {
  .app-sidebar { width: 60px !important; }
  .app-brand__mark { width: 32px; height: 32px; }
}
</style>
