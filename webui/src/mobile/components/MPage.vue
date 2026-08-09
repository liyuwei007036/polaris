<script setup>
// 手机页面骨架：吸顶标题栏 + 独立滚动的内容区。
// 内容区自己滚动，标题栏和外层的底部标签栏都不会被列表推走。
defineProps({
  title: { type: String, required: true },
  loading: { type: Boolean, default: false },
  back: { type: Boolean, default: false },
})
const emit = defineEmits(['back'])
</script>

<template>
  <section class="m-page">
    <header class="m-page__bar">
      <button v-if="back" type="button" class="m-page__back" aria-label="返回上一页" @click="emit('back')">‹</button>
      <h1 class="m-page__title">{{ title }}</h1>
      <div class="m-page__actions"><slot name="actions" /></div>
    </header>
    <div v-loading="loading" class="m-page__body">
      <slot />
    </div>
    <slot name="foot" />
  </section>
</template>

<style scoped>
.m-page { height: 100%; display: flex; flex-direction: column; min-height: 0; }
.m-page__bar {
  flex: none;
  height: var(--m-bar);
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 12px 0 14px;
  background: linear-gradient(180deg, rgba(20, 29, 48, .92), rgba(10, 16, 29, .82));
  border-bottom: 1px solid var(--sb-line);
}
.m-page__back {
  flex: none;
  width: 32px;
  height: 32px;
  margin-left: -8px;
  color: var(--sb-text);
  background: none;
  border: 0;
  font-size: 26px;
  line-height: 1;
  cursor: pointer;
}
.m-page__title {
  flex: 1;
  min-width: 0;
  margin: 0;
  color: #fff;
  font-size: 17px;
  font-weight: 620;
  letter-spacing: .3px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.m-page__actions { flex: none; display: flex; align-items: center; gap: 6px; }
.m-page__actions :deep(.el-button) { height: 32px; padding: 0 11px; margin: 0; }
.m-page__actions :deep(.el-button + .el-button) { margin-left: 0; }
.m-page__body {
  flex: 1;
  min-height: 0;
  padding: 12px 12px 20px;
  overflow-y: auto;
  overflow-x: hidden;
  -webkit-overflow-scrolling: touch;
  scrollbar-width: none;
}
.m-page__body::-webkit-scrollbar { display: none; }
</style>
