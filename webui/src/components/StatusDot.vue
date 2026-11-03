<template>
  <span
    class="status-dot-wrap"
    :class="[statusClass, { 'pulse-ok': status === 'online' }]"
    :title="titleText"
  />
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  online: {
    type: Boolean,
    default: false,
  },
  status: {
    type: String,
    default: '', // 'online' | 'offline' | 'blocked' | 'warning'
  },
})

const effectiveStatus = computed(() => {
  if (props.status) return props.status
  return props.online ? 'online' : 'offline'
})

const statusClass = computed(() => {
  switch (effectiveStatus.value) {
    case 'online':
      return 'is-online'
    case 'blocked':
      return 'is-blocked'
    case 'warning':
      return 'is-warning'
    default:
      return 'is-offline'
  }
})

const titleText = computed(() => {
  switch (effectiveStatus.value) {
    case 'online':
      return '在线运行'
    case 'blocked':
      return '已停用 / 隔离'
    case 'warning':
      return '异常告警'
    default:
      return '离线'
  }
})
</script>

<style scoped>
.status-dot-wrap {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
  display: inline-block;
  transition: background-color 0.2s ease;
}
.is-online {
  background-color: var(--sb-success, #10b981);
  box-shadow: 0 0 8px rgba(16, 185, 129, 0.7);
}
.is-offline {
  background-color: #64748b;
  opacity: 0.6;
}
.is-blocked {
  background-color: var(--sb-danger, #f43f5e);
  box-shadow: 0 0 6px rgba(244, 63, 94, 0.6);
}
.is-warning {
  background-color: var(--sb-warning, #f59e0b);
  box-shadow: 0 0 6px rgba(245, 158, 11, 0.6);
}
</style>
