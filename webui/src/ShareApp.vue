<script setup>
import { computed } from 'vue'
import ShareView from './views/ShareView.vue'

// The share URL is /share/<token>. main.js routes that path to this entry so
// the normal console (and its login check) is never loaded for a viewer.
const token = computed(() => {
  const match = window.location.pathname.match(/^\/share\/([^/?#]+)\/?$/)
  return match ? decodeURIComponent(match[1]) : ''
})
</script>

<template>
  <ShareView v-if="token" :token="token" />
  <div v-else class="share-shell">
    <div class="share-state">
      <h1>分享链接无效</h1>
      <p>请向分享者确认链接是否完整。</p>
    </div>
  </div>
</template>

<style scoped>
.share-shell { min-height: 100vh; display: grid; place-items: center; background: var(--sb-bg); color: var(--sb-text); }
.share-state { text-align: center; color: var(--sb-muted); }
</style>
