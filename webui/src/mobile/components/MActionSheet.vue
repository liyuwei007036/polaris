<script setup>
// 点整张卡片弹出的操作面板。
// 操作必须排在最前面：抽屉是从下往上升的，越靠上的内容离手指越远，
// 把字段表垫在操作前面等于每次操作都先滑一屏。字段表跟在后面，
// 用来补上卡片为了扫读而省掉的那些信息。
import MSheet from './MSheet.vue'

defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '选择操作' },
  // [{ label, value, mono }]
  details: { type: Array, default: () => [] },
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

    <template v-if="details.length">
      <div v-if="actions.length" class="m-section">详细信息</div>
      <dl class="m-detail">
        <div v-for="detail in details" :key="detail.label" class="m-detail__row">
          <dt>{{ detail.label }}</dt>
          <dd :class="{ 'm-mono': detail.mono }">{{ detail.value }}</dd>
        </div>
      </dl>
    </template>

    <div v-if="!actions.length && !details.length" class="m-empty">没有可用的操作</div>

    <!-- 字段表可能很长，标题栏的 ✕ 会被滚到看不见的地方；这里再给一个
         固定在屏幕下缘、拇指够得到的关闭。 -->
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">关闭</el-button>
    </template>
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
