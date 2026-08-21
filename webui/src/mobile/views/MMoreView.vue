<script setup>
import { computed, inject } from 'vue'
import { ElMessageBox } from 'element-plus'
import {
  Collection, Connection, Document, Lock, Monitor, Operation, Setting, Share, Tickets, User,
} from '@element-plus/icons-vue'
import MPage from '../components/MPage.vue'

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
  <MPage title="更多">
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

    <el-button class="sign-out" @click="confirmLogout">退出登录</el-button>
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

.sign-out { width: 100%; height: var(--m-tap); margin-top: 22px; }
</style>
