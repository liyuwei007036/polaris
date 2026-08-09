<script setup>
import { computed, defineAsyncComponent, onBeforeUnmount, onMounted, provide, reactive, ref } from 'vue'
import { Aim, DataAnalysis, Link, Menu, Monitor, Operation } from '@element-plus/icons-vue'
import { api, post, setCsrfToken } from '../api'
import { closeLiveEvents, subscribeLive } from '../live'
import { closeConnectionEvents } from '../connections'
import './styles.css'
import MLoginView from './views/MLoginView.vue'
import MPasswordChangeView from './views/MPasswordChangeView.vue'

// 手机版外壳：与桌面版共用会话、SSE 和 hash 路由，界面另起一套。
// 页面标识与桌面版保持一致，收藏的链接在两端都能打开。
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
  audit: defineAsyncComponent(() => import('./views/MAuditView.vue')),
  security: defineAsyncComponent(() => import('./views/MSecurityView.vue')),
  cloudflare: defineAsyncComponent(() => import('./views/MCloudflareView.vue')),
  settings: defineAsyncComponent(() => import('./views/MSettingsView.vue')),
}

// 底部只放四个最常走的入口，其余九项收进「更多」。
const tabs = [
  { id: 'dashboard', label: '概览', icon: DataAnalysis },
  { id: 'nodes', label: '服务器', icon: Monitor },
  { id: 'inbounds', label: '接入', icon: Aim },
  { id: 'subscriptions', label: '配置', icon: Link },
  { id: 'more', label: '更多', icon: Menu },
]

const activeComponent = computed(() => views[currentView.value] || views.dashboard)
// 二级页面停留时高亮「更多」，否则底部四个入口全灭，看不出自己在哪。
const activeTab = computed(() => (tabs.some((tab) => tab.id === currentView.value) ? currentView.value : 'more'))
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
  checkSession()
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

  <MLoginView v-else-if="!authenticated" @authenticated="setAuthenticated" />

  <MPasswordChangeView
    v-else-if="appState.must_change_password"
    :username="appState.username"
    @changed="setAuthenticated"
  />

  <div v-else class="m-app">
    <div v-if="appState.systemUpdate?.update_available && !updateBannerDismissed" class="m-app__banner">
      <span>新版本 v{{ appState.systemUpdate.latest_version }} 可用</span>
      <button type="button" @click="navigate('settings')">前往更新</button>
      <button type="button" class="is-plain" @click="updateBannerDismissed = true">忽略</button>
    </div>

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
.m-app {
  width: 100%;
  height: 100%;
  display: flex;
  flex-direction: column;
  padding-top: var(--m-safe-top);
}
.m-app__banner {
  flex: none;
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  color: #fcd34d;
  background: rgba(251, 191, 36, .12);
  border-bottom: 1px solid rgba(251, 191, 36, .28);
  font-size: 12.5px;
}
.m-app__banner > span { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.m-app__banner button {
  flex: none;
  padding: 5px 10px;
  color: #04121f;
  background: #fcd34d;
  border: 0;
  border-radius: 7px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.m-app__banner button.is-plain { color: #fcd34d; background: transparent; }
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
