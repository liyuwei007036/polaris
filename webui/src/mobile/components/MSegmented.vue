<script setup>
// 分段控件，代替桌面版的标签页和状态筛选下拉。
// 选项多于三四个时整行横向滑动，不换行。
defineProps({
  modelValue: { type: [String, Number], default: '' },
  // [{ value, label, badge }]
  options: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue'])
</script>

<template>
  <div class="m-seg" role="tablist">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      role="tab"
      class="m-seg__item"
      :class="{ 'is-active': modelValue === option.value }"
      :aria-selected="modelValue === option.value"
      @click="emit('update:modelValue', option.value)"
    >
      {{ option.label }}<i v-if="option.badge !== undefined && option.badge !== ''">{{ option.badge }}</i>
    </button>
  </div>
</template>

<style scoped>
.m-seg {
  display: flex;
  gap: 4px;
  margin-bottom: 12px;
  padding: 3px;
  overflow-x: auto;
  background: var(--neu-inset-bg);
  border: 1px solid var(--sb-line);
  box-shadow: var(--sh-inset);
  border-radius: 10px;
  scrollbar-width: none;
}
.m-seg::-webkit-scrollbar { display: none; }
/* 40×44 是手指按得准的触控尺寸 */
.m-seg__item {
  flex: 1 0 auto;
  min-width: 44px;
  min-height: 38px;
  padding: 0 14px;
  color: var(--sb-muted);
  background: transparent;
  border: 0;
  border-radius: 7px;
  font: inherit;
  font-size: 13.5px;
  white-space: nowrap;
  cursor: pointer;
  transition: all 0.15s ease;
}
.m-seg__item:hover {
  color: var(--sb-text);
}
html.dark .m-seg__item.is-active {
  background: rgba(255, 255, 255, 0.14);
  color: #F8FAFC;
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.35);
}
html.light .m-seg__item.is-active,
html:not(.dark) .m-seg__item.is-active {
  background: #FFFFFF;
  color: #0F172A;
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(100, 104, 123, 0.2);
}
.m-seg__item i { margin-left: 5px; font-style: normal; opacity: .75; font-size: 12px; }
</style>
