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

// 手机版外壳：与桌面版共用会话、SSE 和 hash 路由，界面另起一套。
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
// 页面标识后面可以带查询串，用来从看板直接跳进筛选好的列表（#/nodes?status=offline）。
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

// 底部四个入口按实际打开频率排：看板、当前连接、客户端配置每天都看，
// 接入服务次之。服务器基本是装好就不动的，收进「更多」，概览页的
// 「服务器总数 / 在线服务器」两张指标卡也直接点进去。
const tabs = [
  { id: 'dashboard', label: '概览', icon: DataAnalysis },
  { id: 'connections', label: '连接', icon: Connection },
  { id: 'subscriptions', label: '配置', icon: Link },
  { id: 'inbounds', label: '接入', icon: Aim },
  { id: 'more', label: '更多', icon: Menu },
]

const activeComponent = computed(() => views[currentView.value] || views.dashboard)
// 二级页面停留时高亮「更多」，否则底部四个入口全灭，看不出自己在哪。
const activeTab = computed(() => (tabs.some((tab) => tab.id === currentView.value) ? currentView.value : 'more'))
const canWrite = computed(() => appState.role === 'admin' || appState.role === 'operator')
const isAdmin = computed(() => appState.role === 'admin')
let stopAppLive
let inFlightNodesPromise = null

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

function navigate(view, query = '') {
  const resolved = viewAliases[view] || view
  if (isShareMode && resolved !== sharePage.value) {
    ElMessage.warning('这是只读分享页面，仅包含分享的页面')
    return
  }
  currentView.value = views[resolved] ? resolved : 'dashboard'
  location.hash = `#/${view}${query ? `?${query}` : ''}`
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

provide('appState', appState)
provide('canWrite', canWrite)
provide('isAdmin', isAdmin)
provide('loadNodes', loadNodes)
provide('loadSystemUpdate', loadSystemUpdate)
provide('navigate', navigate)
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
      <!-- 两半过渡都要写：只写进入动画时 out-in 会等一个永远不来的结束事件。 -->
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
        <el-icon :size="20"><component :is="tab.icon" /></el-icon>
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
}
.m-app__view { flex: 1; min-height: 0; position: relative; }
.m-app__tabs {
  flex: none;
  height: calc(var(--m-tabbar) + var(--m-safe-bottom));
  display: flex;
  padding-bottom: var(--m-safe-bottom);
  background: var(--sb-sidebar);
  border-top: 1px solid var(--sb-line);
}
.m-app__tab {
  flex: 1;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 2px;
  color: #64748b;
  background: transparent;
  border: 0;
  font: inherit;
  cursor: pointer;
}
.m-app__tab span { font-size: 10.5px; letter-spacing: .3px; }
.m-app__tab.is-active { color: var(--sb-accent); }

.m-view-enter-active, .m-view-leave-active { transition: opacity .13s ease; }
.m-view-enter-from, .m-view-leave-to { opacity: 0; }
</style>
