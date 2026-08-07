<script setup>
// 北极星 + 轨道环：用 currentColor 着色，放在渐变底块或纯色背景上都能用。
defineProps({ size: { type: Number, default: 22 } })
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 32 32" fill="none" aria-hidden="true">
    <path
      d="M16 2.5 18.9 12.3 28.5 16 18.9 19.7 16 29.5 13.1 19.7 3.5 16 13.1 12.3Z"
      fill="currentColor"
    />
    <circle
      cx="16" cy="16" r="13"
      stroke="currentColor" stroke-opacity=".5" stroke-width="1.4" stroke-dasharray="3.6 5.4"
    />
  </svg>
</template>
