<script setup>
// 手机上代替弹窗的抽屉：默认从底部升起，full 时占满整屏用于长表单。
// 关掉它有三条路，够不到哪条都还有别的：往下拖顶部那道横条、点遮罩、
// 点右上角的 ✕。正在保存的表单可以把 dismissible 关掉。
// 底部操作栏里已经有「取消 / 关闭 / 完成」时不再画右上角的 ✕：同一件事
// 给两个按钮，拇指够得到的那个反而更远。底部只有「确定」这种提交类按钮
// 的抽屉（多选选择器）必须显式传 keep-close，否则就没有放弃的路了。
import { ref, useSlots } from 'vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '' },
  full: { type: Boolean, default: false },
  dismissible: { type: Boolean, default: true },
  keepClose: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue'])
const slots = useSlots()

// 拖动只跟着手指走，松手时拖过 70px 才算关，否则弹回去。
const dragOffset = ref(0)
const dragging = ref(false)
let startY = 0

function onDragStart(event) {
  // 整屏的那种是一个页面不是抽屉，往下拖关掉它会把填了一半的表单丢掉。
  if (!props.dismissible || props.full) return
  dragging.value = true
  startY = event.touches[0].clientY
}

function onDragMove(event) {
  if (!dragging.value) return
  dragOffset.value = Math.max(0, event.touches[0].clientY - startY)
}

function onDragEnd() {
  if (!dragging.value) return
  dragging.value = false
  if (dragOffset.value > 70) emit('update:modelValue', false)
  dragOffset.value = 0
}
</script>

<template>
  <Teleport to="body">
    <!-- 进入和离开两半都要定义：只写进入动画时离开阶段等不到结束事件，
         抽屉会卡在屏幕上关不掉。 -->
    <transition name="m-sheet">
      <div v-if="modelValue" class="m-sheet" :class="{ 'is-full': full }">
        <div class="m-sheet__mask" @click="dismissible && emit('update:modelValue', false)" />
        <section
          class="m-sheet__panel"
          :class="{ 'has-foot': Boolean($slots.footer), 'is-dragging': dragging }"
          :style="dragOffset ? { transform: `translateY(${dragOffset}px)` } : undefined"
          role="dialog"
          :aria-label="title"
        >
          <header
            class="m-sheet__head"
            @touchstart.passive="onDragStart"
            @touchmove.passive="onDragMove"
            @touchend="onDragEnd"
            @touchcancel="onDragEnd"
          >
            <span v-if="!full" class="m-sheet__grab" aria-hidden="true" />
            <strong>{{ title }}</strong>
            <button
              v-if="keepClose || !slots.footer"
              type="button"
              class="m-sheet__close"
              aria-label="关闭"
              @click="emit('update:modelValue', false)"
            >✕</button>
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
  position: relative;
  flex: none;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 20px 12px 13px 18px;
  border-bottom: 1px solid var(--sb-line);
  touch-action: none;
}
.m-sheet.is-full .m-sheet__head { padding-top: 15px; touch-action: auto; }
/* 顶部这道横条既是「能往下拖关掉」的提示，也是拖动的把手。 */
.m-sheet__grab {
  position: absolute;
  top: 8px;
  left: 50%;
  width: 38px;
  height: 4px;
  transform: translateX(-50%);
  background: rgba(148, 163, 184, .38);
  border-radius: 2px;
}
.m-sheet__head strong { flex: 1; min-width: 0; color: #fff; font-size: 15.5px; font-weight: 620; }
.m-sheet__close {
  flex: none;
  width: 40px;
  height: 40px;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .12);
  border: 0;
  border-radius: 50%;
  font-size: 15px;
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

/* 跟手拖动时不能有过渡，否则手指走一段、面板慢一拍。松手后靠这条弹回去。 */
.m-sheet__panel { transition: transform .18s ease; }
.m-sheet__panel.is-dragging { transition: none; }

.m-sheet-enter-active, .m-sheet-leave-active { transition: opacity .18s ease; }
.m-sheet-enter-active .m-sheet__panel, .m-sheet-leave-active .m-sheet__panel { transition: transform .2s ease; }
.m-sheet-enter-from, .m-sheet-leave-to { opacity: 0; }
.m-sheet-enter-from .m-sheet__panel, .m-sheet-leave-to .m-sheet__panel { transform: translateY(16px); }
</style>
