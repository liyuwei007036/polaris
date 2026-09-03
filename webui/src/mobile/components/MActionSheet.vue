<script setup>
// 点整张卡片弹出的操作面板，分「操作」和「详情」两页。
// 原来两者叠在一张长纸上：抽屉从下往上升，越靠上离手指越远，字段表一长，
// 操作按钮就被顶到够不着的地方，而想查一个字段又要先滑过一列按钮。
// 分页之后各自从抽屉最上沿开始，谁都不必先滑过对方。
// 默认停在「操作」：这个抽屉多数时候是为了做事才打开的。
import { computed, ref, watch } from 'vue'
import MSheet from './MSheet.vue'
import MSegmented from './MSegmented.vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '选择操作' },
  // [{ label, value, mono }]，或 { label, list: [...], empty } —— 后者一行一个，
  // 用于成组的名字（分组里的节点），挤成一段就数不清有几个了。
  // list 里可以放字符串，也可以放 { text, tone } 给某一条上色（停用、失效）。
  details: { type: Array, default: () => [] },
  // [{ key, label, hint, danger, disabled }]
  actions: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'select'])

const tab = ref('actions')
const paged = computed(() => Boolean(props.actions.length && props.details.length))

// 每次重新打开都回到「操作」，上一条留下的页码不该跟到下一条身上。
watch(() => props.modelValue, (open) => {
  if (open) tab.value = props.actions.length ? 'actions' : 'details'
})

const listText = (item) => (typeof item === 'string' ? item : item.text)
const listTone = (item) => (typeof item === 'string' ? '' : `is-${item.tone}`)

function choose(action) {
  if (action.disabled) return
  emit('update:modelValue', false)
  emit('select', action.key)
}
</script>

<template>
  <MSheet :model-value="modelValue" :title="title" @update:model-value="emit('update:modelValue', $event)">
    <MSegmented
      v-if="paged"
      v-model="tab"
      :options="[{ value: 'actions', label: '操作' }, { value: 'details', label: '详情' }]"
    />

    <template v-if="!paged || tab === 'actions'">
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
    </template>

    <template v-if="!paged || tab === 'details'">
      <dl v-if="details.length" class="m-detail">
        <div
          v-for="detail in details"
          :key="detail.label"
          class="m-detail__row"
          :class="{ 'is-stacked': Boolean(detail.list) }"
        >
          <dt>{{ detail.label }}</dt>
          <dd v-if="!detail.list" :class="{ 'm-mono': detail.mono }">{{ detail.value }}</dd>
          <dd v-else class="detail-list">
            <span v-for="(item, index) in detail.list" :key="index" :class="listTone(item)">{{ listText(item) }}</span>
            <em v-if="!detail.list.length">{{ detail.empty || '无' }}</em>
          </dd>
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

/* 成组的名字：标签单独一行，下面一行一个。挤在 92px 标签右边那一栏里时，
   十几个名字会流成一大段，数不出有几个，也认不出具体是哪几个。 */
.m-detail__row.is-stacked { display: block; }
.detail-list { margin-top: 8px; display: flex; flex-direction: column; gap: 6px; }
.detail-list span {
  padding: 7px 10px;
  background: rgba(148, 163, 184, .07);
  border-radius: 8px;
  font-size: 12.5px;
  line-height: 1.45;
  word-break: break-word;
}
.detail-list em { color: var(--sb-muted); font-style: normal; font-size: 12.5px; }
/* 停用的照常写名字，只换颜色；真的指不到东西了才是红的。 */
.detail-list span.is-warning { color: #fcd34d; }
.detail-list span.is-danger { color: var(--sb-danger); }
</style>
