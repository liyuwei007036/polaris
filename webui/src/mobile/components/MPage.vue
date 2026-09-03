<script setup>
import { useSlots } from 'vue'
// 手机页面骨架：独立滚动的内容区 + 右下角浮钮。
// 顶部不再有标题栏：底部标签栏已经说明了自己在哪一页，再顶一条只写页名的
// 横条等于把每屏最贵的那 52px 花在一个已知的事实上。
const slots = useSlots()
defineProps({
  loading: { type: Boolean, default: false },
})
</script>

<template>
  <section class="m-page" :class="{ 'has-fab': Boolean(slots.fab) }">
    <div v-loading="loading" class="m-page__body">
      <slot />
    </div>
    <slot name="fab" />
    <slot name="foot" />
  </section>
</template>

<style scoped>
.m-page { position: relative; height: 100%; display: flex; flex-direction: column; min-height: 0; }
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
/* 浮钮压在内容上面，列表末尾要多留一段，最后一条才不会被它盖住。 */
.m-page.has-fab .m-page__body { padding-bottom: 84px; }
</style>
