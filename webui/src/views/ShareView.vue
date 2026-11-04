<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Lock, Refresh, Timer } from '@element-plus/icons-vue'
import { brandName, brandTagline } from '../brand'
import BrandMark from '../components/BrandMark.vue'
import { formatBytes, formatDateTime } from '../format'

const props = defineProps({
  token: { type: String, required: true },
})

const loading = ref(true)
const refreshing = ref(false)
const error = ref('')
const payload = ref(null)
let timer

const pageTitle = computed(() => (payload.value?.page === 'dashboard' ? '运行概览' : '服务器'))
const expiresText = computed(() => formatDateTime(payload.value?.expires_at, '未知'))
const generatedText = computed(() => formatDateTime(payload.value?.generated_at, '—'))
const summary = computed(() => payload.value?.summary || {})
const nodes = computed(() => payload.value?.nodes || [])
const protocols = computed(() => {
  const entries = Object.entries(payload.value?.protocols || {})
  const total = entries.reduce((sum, [, count]) => sum + Number(count || 0), 0)
  return entries
    .map(([name, count]) => ({ name, count: Number(count || 0), percent: total ? (Number(count || 0) / total) * 100 : 0 }))
    .sort((left, right) => right.count - left.count)
})
const popularNodes = computed(() => {
  const entries = payload.value?.popular_nodes || []
  const max = entries.reduce((value, item) => Math.max(value, Number(item.count || 0)), 0)
  return entries.map((item) => ({ ...item, percent: max ? (Number(item.count || 0) / max) * 100 : 0 }))
})
const totalTraffic = computed(() => Number(summary.value.received_bytes || 0) + Number(summary.value.sent_bytes || 0))

function percent(used, total) {
  if (!total) return 0
  return Math.max(0, Math.min(100, (Number(used || 0) / Number(total)) * 100))
}

function nodeMeta(node) {
  return [node.os || 'linux', node.architecture, node.city || node.location].filter(Boolean).join(' · ')
}

function nodeAddress(node) {
  return node.ip_addresses?.[0] || node.client_address || node.last_observed_ip || '—'
}

async function load() {
  if (refreshing.value) return
  refreshing.value = true
  if (!payload.value) loading.value = true
  try {
    const response = await fetch(`/api/v1/shares/access/${encodeURIComponent(props.token)}`, {
      credentials: 'omit',
      headers: { Accept: 'application/json' },
    })
    const body = await response.json().catch(() => null)
    if (!response.ok) throw new Error(body?.error || '分享链接不存在或已过期')
    payload.value = body
    error.value = ''
  } catch (cause) {
    error.value = /[\u3400-\u9fff]/.test(cause?.message || '')
      ? cause.message
      : '暂时无法载入分享内容，请稍后重试'
    payload.value = null
  } finally {
    loading.value = false
    refreshing.value = false
  }
}

onMounted(async () => {
  await load()
  timer = window.setInterval(() => load(), 30000)
})

onBeforeUnmount(() => window.clearInterval(timer))
</script>

<template>
  <div class="share-shell">
    <header class="share-header">
      <div class="share-brand">
        <span class="share-brand__mark"><BrandMark :size="25" /></span>
        <span class="share-brand__text">
          <strong>{{ brandName }}</strong>
          <small>{{ brandTagline }}</small>
        </span>
      </div>
      <div class="share-badges">
        <span class="share-badge"><el-icon><Lock /></el-icon>只读分享</span>
        <span class="share-badge share-badge--muted">核心数据已脱敏</span>
      </div>
    </header>

    <main class="share-main">
      <div v-if="loading" class="share-state">
        <el-icon class="is-loading" :size="26"><Refresh /></el-icon>
        <h1>正在载入分享内容</h1>
      </div>

      <div v-else-if="error" class="share-state share-state--error">
        <el-icon :size="30"><Lock /></el-icon>
        <h1>分享链接不可用</h1>
        <p>{{ error }}</p>
        <p class="share-state__hint">链接可能已过期或被创建者撤销，请联系分享者重新生成。</p>
      </div>

      <template v-else>
        <section class="share-heading">
          <div>
            <h1>{{ pageTitle }}</h1>
            <p>此页面仅用于查看，无法进行任何修改操作。</p>
          </div>
          <div class="share-heading__actions">
            <span class="share-expiry"><el-icon><Timer /></el-icon>有效期至 {{ expiresText }}</span>
            <el-button :icon="Refresh" :loading="refreshing" circle title="刷新数据" @click="load" />
          </div>
        </section>

        <section class="share-metrics">
          <article class="share-metric neu">
            <span>服务器总数</span>
            <strong>{{ summary.nodes_total || 0 }}</strong>
          </article>
          <article class="share-metric neu">
            <span>在线服务器</span>
            <strong class="is-online">{{ summary.online || 0 }}</strong>
          </article>
          <article class="share-metric neu">
            <span>活动连接</span>
            <strong>{{ summary.active_connections || 0 }}</strong>
          </article>
          <article class="share-metric neu">
            <span>累计流量</span>
            <strong>{{ formatBytes(totalTraffic) }}</strong>
          </article>
        </section>

        <template v-if="payload.page === 'dashboard'">
          <section class="share-rate-row">
            <article class="share-rate neu">
              <span>实时下行</span>
              <strong>{{ summary.has_rates ? formatBytes(summary.download_rate, '/s') : '等待上报' }}</strong>
            </article>
            <article class="share-rate neu">
              <span>实时上行</span>
              <strong>{{ summary.has_rates ? formatBytes(summary.upload_rate, '/s') : '等待上报' }}</strong>
            </article>
            <article class="share-rate neu">
              <span>本月下行</span>
              <strong>{{ formatBytes(summary.monthly_received_bytes) }}</strong>
            </article>
            <article class="share-rate neu">
              <span>本月上行</span>
              <strong>{{ formatBytes(summary.monthly_sent_bytes) }}</strong>
            </article>
          </section>

          <section class="share-panels">
            <article class="share-panel neu">
              <h2>传输协议</h2>
              <div v-if="protocols.length" class="share-rank">
                <div v-for="item in protocols" :key="item.name" class="share-rank__item">
                  <div class="share-rank__line">
                    <span>{{ item.name }}</span>
                    <em>{{ item.count }} 条</em>
                  </div>
                  <div class="share-rank__track"><i :style="{ width: `${item.percent}%` }" /></div>
                </div>
              </div>
              <p v-else class="share-empty">暂无连接</p>
            </article>

            <article class="share-panel neu">
              <h2>热门节点<small>最近 {{ payload.popular_window_minutes || 10 }} 分钟</small></h2>
              <div v-if="popularNodes.length" class="share-rank">
                <div v-for="item in popularNodes" :key="item.name" class="share-rank__item">
                  <div class="share-rank__line">
                    <span>{{ item.name }}</span>
                    <em>{{ item.count }} 次</em>
                  </div>
                  <div class="share-rank__track share-rank__track--popular"><i :style="{ width: `${item.percent}%` }" /></div>
                </div>
              </div>
              <p v-else class="share-empty">暂无连接</p>
            </article>
          </section>
        </template>

        <template v-else>
          <section class="share-node-grid">
            <article v-for="(node, index) in nodes" :key="`${node.name}-${index}`" class="share-node neu">
              <header class="share-node__head">
                <span class="share-node__status" :class="{ 'is-online': node.online }" />
                <div class="share-node__identity">
                  <h2>{{ node.name }}</h2>
                  <p>{{ nodeMeta(node) || '暂无系统信息' }}</p>
                </div>
                <span class="share-node__tag" :class="{ 'is-online': node.online }">{{ node.online ? '在线' : '离线' }}</span>
              </header>

              <div class="share-node__stats">
                <div>
                  <span>CPU</span>
                  <strong>{{ node.online && node.host ? `${Number(node.host.cpu_percent || 0).toFixed(1)}%` : '—' }}</strong>
                </div>
                <div>
                  <span>内存</span>
                  <strong>{{ node.online && node.host?.memory_total ? `${percent(node.host.memory_used, node.host.memory_total).toFixed(0)}%` : '—' }}</strong>
                </div>
                <div>
                  <span>硬盘</span>
                  <strong>{{ node.online && node.host?.disk_total ? `${percent(node.host.disk_used, node.host.disk_total).toFixed(0)}%` : '—' }}</strong>
                </div>
                <div>
                  <span>连接</span>
                  <strong>{{ node.connection_count || 0 }}</strong>
                </div>
              </div>

              <div v-if="node.online && node.host" class="share-node__gauges">
                <div class="share-gauge">
                  <span>内存 {{ percent(node.host.memory_used, node.host.memory_total).toFixed(0) }}%</span>
                  <div class="share-gauge__track"><i :style="{ width: `${percent(node.host.memory_used, node.host.memory_total)}%` }" /></div>
                </div>
                <div class="share-gauge">
                  <span>硬盘 {{ percent(node.host.disk_used, node.host.disk_total).toFixed(0) }}%</span>
                  <div class="share-gauge__track"><i :style="{ width: `${percent(node.host.disk_used, node.host.disk_total)}%` }" /></div>
                </div>
              </div>

              <footer class="share-node__foot">
                <span>{{ nodeAddress(node) }}</span>
                <span v-if="node.proxy">
                  累计 {{ formatBytes(Number(node.proxy.received_bytes || 0) + Number(node.proxy.sent_bytes || 0)) }}
                </span>
                <span v-else>暂无流量数据</span>
              </footer>
            </article>
          </section>
          <p v-if="!nodes.length" class="share-empty share-empty--page">还没有已接入的服务器</p>
        </template>

        <footer class="share-foot">
          数据生成于 {{ generatedText }} · 由 {{ brandName }} 提供 · IP 地址、域名与费用信息已脱敏
        </footer>
      </template>
    </main>
  </div>
</template>

<style scoped>
.share-shell {
  min-height: 100vh;
  padding: 22px clamp(16px, 4vw, 48px) 30px;
  background:
    radial-gradient(circle at 12% -10%, color-mix(in oklab, var(--sb-accent) 14%, transparent), transparent 34%),
    var(--sb-bg, #eef1f7);
  color: var(--sb-text, #1e293b);
}
.share-header {
  max-width: 1180px;
  margin: 0 auto 22px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  flex-wrap: wrap;
}
.share-brand { display: flex; align-items: center; gap: 11px; }
.share-brand__mark {
  width: 42px; height: 42px; border-radius: 12px;
  display: grid; place-items: center;
  background: var(--sb-surface, #fff);
  box-shadow: var(--sh-neu, 0 8px 24px rgba(15, 23, 42, .08));
}
.share-brand__text { display: flex; flex-direction: column; line-height: 1.25; }
.share-brand__text strong { font-size: 17px; letter-spacing: .01em; }
.share-brand__text small { color: var(--sb-muted, #64748b); font-size: 12px; }
.share-badges { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.share-badge {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 6px 11px; border-radius: 999px;
  font-size: 12px; font-weight: 600;
  color: var(--sb-accent, #0ea5e9);
  background: color-mix(in oklab, var(--sb-accent, #0ea5e9) 12%, transparent);
  border: 1px solid color-mix(in oklab, var(--sb-accent, #0ea5e9) 26%, transparent);
}
.share-badge--muted {
  color: var(--sb-muted, #64748b);
  background: color-mix(in oklab, var(--sb-muted, #64748b) 8%, transparent);
  border-color: color-mix(in oklab, var(--sb-muted, #64748b) 18%, transparent);
}
.share-main { max-width: 1180px; margin: 0 auto; display: flex; flex-direction: column; gap: 16px; }
.share-heading { display: flex; align-items: flex-end; justify-content: space-between; gap: 18px; flex-wrap: wrap; }
.share-heading h1 { margin: 0; font-size: clamp(24px, 3vw, 34px); letter-spacing: -.02em; }
.share-heading p { margin: 6px 0 0; color: var(--sb-muted, #64748b); font-size: 13px; }
.share-heading__actions { display: flex; align-items: center; gap: 10px; }
.share-expiry {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 7px 11px; border-radius: 9px;
  font-size: 12px; color: var(--sb-muted, #64748b);
  background: color-mix(in oklab, var(--sb-surface, #fff) 76%, transparent);
}
.share-metrics { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.share-metric, .share-rate {
  padding: 16px 18px; border-radius: 14px;
  display: flex; flex-direction: column; gap: 8px;
  background: var(--sb-surface, #fff);
  box-shadow: var(--sh-neu, 0 8px 24px rgba(15, 23, 42, .07));
}
.share-metric span, .share-rate span { font-size: 12px; color: var(--sb-muted, #64748b); }
.share-metric strong { font-size: clamp(22px, 2.4vw, 30px); line-height: 1.05; letter-spacing: -.02em; }
.share-metric strong.is-online { color: #10b981; }
.share-rate-row { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 12px; }
.share-rate strong { font-size: 18px; }
.share-panels { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
.share-panel {
  padding: 18px; border-radius: 14px;
  background: var(--sb-surface, #fff);
  box-shadow: var(--sh-neu, 0 8px 24px rgba(15, 23, 42, .07));
}
.share-panel h2 { margin: 0 0 14px; font-size: 14px; display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
.share-panel h2 small { color: var(--sb-muted, #64748b); font-size: 11px; font-weight: 400; }
.share-rank { display: flex; flex-direction: column; gap: 12px; }
.share-rank__line { display: flex; justify-content: space-between; gap: 12px; font-size: 12px; margin-bottom: 6px; }
.share-rank__line em { font-style: normal; color: var(--sb-muted, #64748b); }
.share-rank__track, .share-gauge__track {
  height: 7px; border-radius: 999px; overflow: hidden;
  background: color-mix(in oklab, var(--sb-muted, #64748b) 13%, transparent);
}
.share-rank__track i, .share-gauge__track i {
  display: block; height: 100%; border-radius: inherit;
  background: linear-gradient(90deg, #0ea5e9, #6366f1);
  transition: width .4s ease;
}
.share-rank__track--popular i { background: linear-gradient(90deg, #22d3ee, #0ea5e9); }
.share-empty { margin: 0; color: var(--sb-muted, #64748b); font-size: 12px; }
.share-empty--page { text-align: center; padding: 36px 0; }
.share-node-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
.share-node {
  padding: 16px; border-radius: 14px;
  background: var(--sb-surface, #fff);
  box-shadow: var(--sh-neu, 0 8px 24px rgba(15, 23, 42, .07));
  display: flex; flex-direction: column; gap: 13px;
}
.share-node__head { display: flex; align-items: flex-start; gap: 10px; }
.share-node__status {
  width: 9px; height: 9px; border-radius: 50%; margin-top: 6px; flex: none;
  background: #94a3b8; box-shadow: 0 0 0 4px color-mix(in oklab, #94a3b8 18%, transparent);
}
.share-node__status.is-online { background: #10b981; box-shadow: 0 0 0 4px color-mix(in oklab, #10b981 18%, transparent); }
.share-node__identity { min-width: 0; flex: 1; }
.share-node__identity h2 { margin: 0; font-size: 15px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.share-node__identity p { margin: 4px 0 0; font-size: 11px; color: var(--sb-muted, #64748b); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.share-node__tag {
  flex: none; font-size: 11px; font-weight: 600; padding: 3px 8px; border-radius: 999px;
  color: #64748b; background: color-mix(in oklab, #64748b 10%, transparent);
}
.share-node__tag.is-online { color: #059669; background: color-mix(in oklab, #10b981 13%, transparent); }
.share-node__stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 8px; }
.share-node__stats div { display: flex; flex-direction: column; gap: 4px; }
.share-node__stats span { font-size: 10px; color: var(--sb-muted, #64748b); }
.share-node__stats strong { font-size: 14px; }
.share-node__gauges { display: flex; flex-direction: column; gap: 9px; }
.share-gauge span { display: block; font-size: 10px; color: var(--sb-muted, #64748b); margin-bottom: 5px; }
.share-node__foot {
  display: flex; justify-content: space-between; gap: 10px;
  padding-top: 10px; border-top: 1px solid color-mix(in oklab, var(--sb-muted, #64748b) 14%, transparent);
  font-size: 11px; color: var(--sb-muted, #64748b);
}
.share-foot { text-align: center; color: var(--sb-muted, #64748b); font-size: 11px; padding: 10px 0 4px; }
.share-state {
  min-height: 58vh; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 10px; text-align: center; color: var(--sb-muted, #64748b);
}
.share-state h1 { margin: 4px 0 0; font-size: 22px; color: var(--sb-text, #1e293b); }
.share-state p { margin: 0; font-size: 13px; }
.share-state__hint { opacity: .8; }
.share-state--error .el-icon { color: #f59e0b; }
@media (max-width: 900px) {
  .share-metrics, .share-rate-row { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .share-panels { grid-template-columns: 1fr; }
}
@media (max-width: 560px) {
  .share-shell { padding: 16px 12px 24px; }
  .share-metrics, .share-rate-row { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 9px; }
  .share-metric, .share-rate { padding: 13px 14px; }
  .share-heading__actions { width: 100%; justify-content: space-between; }
  .share-node-grid { grid-template-columns: 1fr; }
}
</style>
