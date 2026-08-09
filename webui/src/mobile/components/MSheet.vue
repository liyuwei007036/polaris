<script setup>
// 手机上代替弹窗的抽屉：默认从底部升起，full 时占满整屏用于长表单。
// 遮罩点击关闭；正在保存的表单可以把 dismissible 关掉。
defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '' },
  full: { type: Boolean, default: false },
  dismissible: { type: Boolean, default: true },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <Teleport to="body">
    <!-- 进入和离开两半都要定义：只写进入动画时离开阶段等不到结束事件，
         抽屉会卡在屏幕上关不掉。 -->
    <transition name="m-sheet">
      <div v-if="modelValue" class="m-sheet" :class="{ 'is-full': full }">
        <div class="m-sheet__mask" @click="dismissible && emit('update:modelValue', false)" />
        <section class="m-sheet__panel" :class="{ 'has-foot': Boolean($slots.footer) }" role="dialog" :aria-label="title">
          <header class="m-sheet__head">
            <strong>{{ title }}</strong>
            <button type="button" class="m-sheet__close" aria-label="关闭" @click="emit('update:modelValue', false)">✕</button>
          </header>
          <div class="m-sheet__body"><slot /></div>
          <footer v-if="$slots.footer" class="m-sheet__foot"><slot name="footer" /></footer>
        </section>
      </div>
    </transition>
  </Teleport>
</template>

<style scoped>
.m-sheet { position: fixed; inset: 0; z-index: 2100; display: flex; flex-direction: column; justify-content: flex-end; }
.m-sheet__mask { position: absolute; inset: 0; background: rgba(4, 8, 16, .68); }
.m-sheet__panel {
  position: relative;
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  background: var(--sb-surface);
  border-top: 1px solid var(--sb-line-strong);
  border-radius: 18px 18px 0 0;
  box-shadow: 0 -20px 50px -30px rgba(0, 0, 0, .95);
}
.m-sheet.is-full .m-sheet__panel {
  max-height: none;
  height: 100%;
  padding-top: var(--m-safe-top);
  border-radius: 0;
}
.m-sheet__head {
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 15px 14px 13px 18px;
  border-bottom: 1px solid var(--sb-line);
}
.m-sheet__head strong { flex: 1; min-width: 0; color: #fff; font-size: 15.5px; font-weight: 620; }
.m-sheet__close {
  flex: none;
  width: 34px;
  height: 34px;
  color: var(--sb-muted);
  background: rgba(148, 163, 184, .10);
  border: 0;
  border-radius: 50%;
  font-size: 14px;
  cursor: pointer;
}
.m-sheet__body {
  flex: 1;
  min-height: 0;
  padding: 16px 16px calc(16px + var(--m-safe-bottom));
  overflow-y: auto;
  -webkit-overflow-scrolling: touch;
  scrollbar-width: none;
}
.m-sheet__body::-webkit-scrollbar { display: none; }
.m-sheet__foot {
  flex: none;
  display: flex;
  gap: 10px;
  padding: 10px 14px calc(10px + var(--m-safe-bottom));
  border-top: 1px solid var(--sb-line);
}
.m-sheet__foot :deep(.el-button) { flex: 1; height: var(--m-tap); margin: 0; }
/* 安全区只垫在最下面那一层：有操作栏时内容区再垫一次就会多出一段空白。 */
.m-sheet__panel.has-foot .m-sheet__body { padding-bottom: 16px; }

.m-sheet-enter-active, .m-sheet-leave-active { transition: opacity .18s ease; }
.m-sheet-enter-active .m-sheet__panel, .m-sheet-leave-active .m-sheet__panel { transition: transform .2s ease; }
.m-sheet-enter-from, .m-sheet-leave-to { opacity: 0; }
.m-sheet-enter-from .m-sheet__panel, .m-sheet-leave-to .m-sheet__panel { transform: translateY(16px); }
</style>
