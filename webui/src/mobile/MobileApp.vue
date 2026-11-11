<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide, reactive, ref } from 'vue'
import { Aim, Connection, DataAnalysis, Link, Lock, Menu, Operation } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { api, post, setCsrfToken } from '../api'
import { closeLiveEvents, subscribeLive } from '../live'
import { closeConnectionEvents } from '../connections'
import { isShareMode } from '../share'
import './styles.css'
import MLoginView from './views/MLoginView.vue'
import MPasswordChangeView from './views/MPasswordChangeView.vue'

// 手机版外壳：与桌面版共用会话、SSE 和 hash 路由，界面移动端专属优化。
// 页面标识与桌面版保持一致，收藏的链接在两端都能打开。
const authenticated = ref(false)
const checking = ref(true)
const shareError = ref('')
const sharePage = ref('')
const viewAliases = {
  'ingress-routes': 'inbounds',
  history: 'connection-history',
  'history-connections': 'connection-history',
  'security-center': 'security',
  firewall: 'security',
  alerts: 'settings',
  'alert-settings': 'settings',
}

const viewOf = (hash) => hash.replace(/^#\/?/, '').split('?')[0] || 'dashboard'
const requestedView = viewOf(location.hash)
const currentView = ref(viewAliases[requestedView] || requestedView)
const appState = reactive({
  username: '',
  role: '',
  totp_enabled: false,
  must_change_password: false,
  nodes: [],
  systemUpdate: null,
})

const views = {
  dashboard: defineAsyncComponent(() => import('./views/MDashboardView.vue')),
  nodes: defineAsyncComponent(() => import('./views/MNodesView.vue')),
  inbounds: defineAsyncComponent(() => import('./views/MInboundsView.vue')),
  subscriptions: defineAsyncComponent(() => import('./views/MSubscriptionsView.vue')),
  more: defineAsyncComponent(() => import('./views/MMoreView.vue')),
  'proxy-groups': defineAsyncComponent(() => import('./views/MProxyGroupsView.vue')),
  'rule-providers': defineAsyncComponent(() => import('./views/MRuleProvidersView.vue')),
  routes: defineAsyncComponent(() => import('./views/MRoutesView.vue')),
  outbounds: defineAsyncComponent(() => import('./views/MOutboundsView.vue')),
  connections: defineAsyncComponent(() => import('./views/MConnectionsView.vue')),
  devices: defineAsyncComponent(() => import('./views/MDevicesView.vue')),
  'connection-history': defineAsyncComponent(() => import('./views/MDevicesView.vue')),
  audit: defineAsyncComponent(() => import('./views/MAuditView.vue')),
  security: defineAsyncComponent(() => import('./views/MSecurityView.vue')),
  cloudflare: defineAsyncComponent(() => import('./views/MCloudflareView.vue')),
  settings: defineAsyncComponent(() => import('./views/MSettingsView.vue')),
}

const tabs = [
  { id: 'dashboard', label: '概览', icon: DataAnalysis },
  { id: 'connections', label: '连接', icon: Connection },
  { id: 'subscriptions', label: '配置', icon: Link },
  { id: 'inbounds', label: '接入', icon: Aim },
  { id: 'more', label: '更多', icon: Menu },
]

const activeComponent = computed(() => views[currentView.value] || views.dashboard)
const activeTab = computed(() => (tabs.some((tab) => tab.id === currentView.value) ? currentView.value : 'more'))
const canWrite = computed(() => appState.role === 'admin' || appState.role === 'operator')
const isAdmin = computed(() => appState.role === 'admin')
const historyStack = ref([])

let stopAppLive
let inFlightNodesPromise = null

function navigate(view, query = '') {
  const resolved = viewAliases[view] || view
  if (isShareMode && resolved !== sharePage.value) {
    ElMessage.warning('这是只读分享页面，仅包含分享的页面')
    return
  }
  if (currentView.value && currentView.value !== resolved) {
    historyStack.value.push(currentView.value)
    if (historyStack.value.length > 20) historyStack.value.shift()
  }
  currentView.value = views[resolved] ? resolved : 'dashboard'
  location.hash = `#/${view}${query ? `?${query}` : ''}`
}

function goBack(fallback = 'more') {
  if (historyStack.value.length > 0) {
    const prev = historyStack.value.pop()
    currentView.value = views[prev] ? prev : fallback
    location.hash = `#/${currentView.value}`
  } else {
    navigate(fallback)
  }
}

function onHashChange() {
  if (isShareMode) {
    const page = sharePage.value || 'nodes'
    currentView.value = page
    if (location.hash !== `#/${page}`) location.hash = `#/${page}`
    return
  }
  const view = viewOf(location.hash)
  const resolved = viewAliases[view] || view
  currentView.value = views[resolved] ? resolved : 'dashboard'
}

async function loadNodes() {
  if (!authenticated.value) return []
  if (inFlightNodesPromise) return inFlightNodesPromise
  inFlightNodesPromise = (async () => {
    try {
      const result = await api('/nodes')
      appState.nodes = result.nodes || []
      return appState.nodes
    } finally {
      inFlightNodesPromise = null
    }
  })()
  return inFlightNodesPromise
}

async function loadSystemUpdate(refresh = false) {
  if (!authenticated.value) return null
  if (isShareMode) {
    appState.systemUpdate = null
    return null
  }
  appState.systemUpdate = await api(refresh ? '/system/update?refresh=1' : '/system/update')
  return appState.systemUpdate
}

async function checkSession() {
  checking.value = true
  try {
    setAuthenticated(await api('/auth/me'))
  } catch {
    clearSession()
  } finally {
    checking.value = false
  }
}

function startLiveSubscription() {
  stopAppLive?.()
  stopAppLive = subscribeLive((event) => {
    if (event.kind === 'node' && event.node_id) {
      const node = appState.nodes.find((n) => n.id === event.node_id)
      if (node) {
        if (event.online !== undefined) node.online = event.online
        if (event.client_address !== undefined) node.client_address = event.client_address
        if (event.last_observed_ip !== undefined) node.last_observed_ip = event.last_observed_ip
        if (event.agent_version !== undefined) node.agent_version = event.agent_version
      }
    }
  })
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
  startLiveSubscription()
}

async function initShareMode() {
  checking.value = true
  try {
    const meta = await api('')
    sharePage.value = meta?.page === 'dashboard' ? 'dashboard' : 'nodes'
    appState.username = '只读分享'
    appState.role = 'viewer'
    appState.totp_enabled = false
    appState.must_change_password = false
    authenticated.value = true
    currentView.value = sharePage.value
    location.hash = `#/${sharePage.value}`
    await loadNodes()
    startLiveSubscription()
    shareError.value = ''
  } catch (error) {
    shareError.value = error?.message || '分享链接不存在或已过期'
    authenticated.value = false
  } finally {
    checking.value = false
  }
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

provide('appState', appState)
provide('canWrite', canWrite)
provide('isAdmin', isAdmin)
provide('loadNodes', loadNodes)
provide('loadSystemUpdate', loadSystemUpdate)
provide('navigate', navigate)
provide('goBack', goBack)
provide('currentView', currentView)
provide('logout', logout)

onMounted(() => {
  window.addEventListener('hashchange', onHashChange)
  window.addEventListener('sb:unauthorized', clearSession)
  if (isShareMode) initShareMode()
  else checkSession()
})

onBeforeUnmount(() => {
  window.removeEventListener('hashchange', onHashChange)
  window.removeEventListener('sb:unauthorized', clearSession)
})
</script>

<template>
  <div v-if="checking" class="m-boot">
    <el-icon class="is-loading" :size="26"><Operation /></el-icon>
    <span>正在载入</span>
  </div>

  <div v-else-if="shareError" class="m-boot m-share-error">
    <el-icon :size="26"><Lock /></el-icon>
    <strong>{{ shareError }}</strong>
    <span>请向分享者确认链接是否完整，或让分享者重新生成。</span>
  </div>

  <MLoginView v-else-if="!authenticated" @authenticated="setAuthenticated" />

  <MPasswordChangeView
    v-else-if="appState.must_change_password"
    :username="appState.username"
    @changed="setAuthenticated"
  />

  <div v-else class="m-app">
    <main class="m-app__view">
      <transition name="m-view" mode="out-in">
        <component :is="activeComponent" :key="currentView" />
      </transition>
    </main>

    <nav class="m-app__tabs" aria-label="主导航">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        class="m-app__tab"
        :class="{ 'is-active': activeTab === tab.id }"
        :aria-current="activeTab === tab.id || undefined"
        @click="navigate(tab.id)"
      >
        <span class="tab-icon-wrap">
          <el-icon :size="19"><component :is="tab.icon" /></el-icon>
        </span>
        <span>{{ tab.label }}</span>
      </button>
    </nav>
  </div>
</template>

<style scoped>
.m-boot {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  color: var(--sb-muted);
  letter-spacing: 1px;
}
.m-share-error {
  flex-direction: column;
  text-align: center;
  padding: 24px;
}
.m-share-error strong { color: var(--sb-text); font-size: 15px; }
.m-share-error span { max-width: 280px; font-size: 12.5px; line-height: 1.6; }
.m-app {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding-top: var(--m-safe-top);
  background: var(--sb-bg);
}
.m-app__view {
  flex: 1;
  min-height: 0;
  position: relative;
  overflow: hidden;
}
.m-app__tabs {
  flex: none;
  height: calc(var(--m-tabbar) + var(--m-safe-bottom));
  display: flex;
  align-items: center;
  padding-bottom: var(--m-safe-bottom);
  background: color-mix(in oklab, var(--sb-sidebar) 85%, transparent);
  backdrop-filter: blur(20px) saturate(1.4);
  -webkit-backdrop-filter: blur(20px) saturate(1.4);
  border-top: 1px solid var(--sb-line);
}
.m-app__tab {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  padding: 4px 0;
  color: var(--sb-muted);
  background: transparent;
  border: 0;
  font: inherit;
  cursor: pointer;
  transition: color 0.15s ease, transform 0.1s ease;
}
.m-app__tab:active {
  transform: scale(0.94);
}
.tab-icon-wrap {
  display: grid;
  place-items: center;
  width: 36px;
  height: 24px;
  border-radius: 12px;
  transition: background-color 0.15s ease, color 0.15s ease;
}
.m-app__tab span:last-child {
  font-size: 10.5px;
  font-weight: 500;
  letter-spacing: .2px;
}
.m-app__tab.is-active {
  color: var(--sb-text);
}
.m-app__tab.is-active span:last-child {
  font-weight: 600;
}
.m-app__tab.is-active .tab-icon-wrap {
  background: var(--sb-accent-soft);
  color: var(--sb-text);
}

.m-view-enter-active, .m-view-leave-active { transition: opacity .14s ease, transform .14s ease; }
.m-view-enter-from { opacity: 0; transform: translateY(4px); }
.m-view-leave-to { opacity: 0; transform: translateY(-4px); }
</style>
