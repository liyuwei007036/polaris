<script setup>
// 卡片右上角「⋯」弹出的操作面板。桌面版把这些操作平铺成一列按钮，
// 手机上一行放不下，也点不准。
import MSheet from './MSheet.vue'

defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '选择操作' },
  // [{ key, label, hint, danger, disabled }]
  actions: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'select'])

function choose(action) {
  if (action.disabled) return
  emit('update:modelValue', false)
  emit('select', action.key)
}
</script>

<template>
  <MSheet :model-value="modelValue" :title="title" @update:model-value="emit('update:modelValue', $event)">
    <button
      v-for="action in actions"
      :key="action.key"
      type="button"
      class="m-action"
      :class="{ 'is-danger': action.danger, 'is-disabled': action.disabled }"
      :disabled="action.disabled"
      @click="choose(action)"
    >
      <span>{{ action.label }}</span>
      <small v-if="action.hint">{{ action.hint }}</small>
    </button>
    <div v-if="!actions.length" class="m-empty">没有可用的操作</div>
  </MSheet>
</template>

<style scoped>
.m-action {
  width: 100%;
  min-height: var(--m-tap);
  padding: 11px 14px;
  color: var(--sb-text);
  text-align: left;
  background: rgba(148, 163, 184, .06);
  border: 1px solid var(--sb-line);
  border-radius: var(--m-radius);
  font: inherit;
  cursor: pointer;
}
.m-action + .m-action { margin-top: 8px; }
.m-action:active { background: rgba(148, 163, 184, .14); }
.m-action span { display: block; font-size: 14.5px; }
.m-action small { display: block; margin-top: 3px; color: var(--sb-muted); font-size: 12px; }
.m-action.is-danger { color: var(--sb-danger); }
.m-action.is-disabled { color: var(--sb-muted); opacity: .6; }
</style>
