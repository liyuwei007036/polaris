<script setup>
defineProps({
  title: { type: String, required: true },
})
</script>

<template>
  <header class="page-header">
    <div>
      <h1 class="page-header__title">{{ title }}</h1>
    </div>
    <div class="page-header__actions">
      <slot />
    </div>
  </header>
</template>
