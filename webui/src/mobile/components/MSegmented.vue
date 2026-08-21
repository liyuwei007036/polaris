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
  background: rgba(148, 163, 184, .07);
  border: 1px solid var(--sb-line);
  border-radius: 11px;
  scrollbar-width: none;
}
.m-seg::-webkit-scrollbar { display: none; }
/* 40×44 是手指按得准的下限；原来的 34px 高在手机上要瞄。 */
.m-seg__item {
  flex: 1 0 auto;
  min-width: 44px;
  min-height: 40px;
  padding: 0 13px;
  color: var(--sb-muted);
  background: transparent;
  border: 0;
  border-radius: 8px;
  font: inherit;
  font-size: 13.5px;
  white-space: nowrap;
  cursor: pointer;
}
.m-seg__item.is-active { color: #04121f; background: var(--sb-accent); font-weight: 600; }
.m-seg__item i { margin-left: 5px; font-style: normal; opacity: .75; font-size: 12px; }
</style>
