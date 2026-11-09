<script setup>
// 全屏选择器，代替 el-select。手机上下拉浮层只有半屏高、选项 32px、
// 多选标签还会把输入框撑成三行，几十个节点根本挑不动。
// 这里改成：一行触发器 + 打开后占满屏的搜索列表。
import { computed, ref, watch } from 'vue'
import MSheet from './MSheet.vue'

const props = defineProps({
  // 单选是字符串，多选是数组
  modelValue: { type: [String, Number, Array], default: '' },
  // [{ value, label, desc, disabled, group }]
  options: { type: Array, default: () => [] },
  title: { type: String, default: '请选择' },
  placeholder: { type: String, default: '请选择' },
  multiple: { type: Boolean, default: false },
  clearable: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  // 筛选条里用的窄版触发器：按内容宽度排在一行，不再独占整行。
  chip: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue'])

const open = ref(false)
const keyword = ref('')
const draft = ref([])

const selected = computed(() => (props.multiple ? [...(props.modelValue || [])] : props.modelValue === '' || props.modelValue === undefined || props.modelValue === null ? [] : [props.modelValue]))
const labels = computed(() => selected.value.map((value) => {
  const option = props.options.find((item) => item.value === value)
  return option ? option.label : String(value)
}))
// 分组保持传入顺序，没有 group 的都归到最前面的空组。
const groups = computed(() => {
  const text = keyword.value.trim().toLocaleLowerCase()
  const matched = text
    ? props.options.filter((option) => `${option.label} ${option.desc || ''} ${option.value}`.toLocaleLowerCase().includes(text))
    : props.options
  const result = []
  for (const option of matched) {
    const name = option.group || ''
    let bucket = result.find((item) => item.name === name)
    if (!bucket) {
      bucket = { name, items: [] }
      result.push(bucket)
    }
    bucket.items.push(option)
  }
  return result
})

watch(open, (value) => {
  if (!value) return
  keyword.value = ''
  draft.value = [...selected.value]
})

function toggle(option) {
  if (option.disabled) return
  if (!props.multiple) {
    emit('update:modelValue', option.value)
    open.value = false
    return
  }
  draft.value = draft.value.includes(option.value)
    ? draft.value.filter((value) => value !== option.value)
    : [...draft.value, option.value]
}

function confirm() {
  emit('update:modelValue', [...draft.value])
  open.value = false
}

function clear() {
  emit('update:modelValue', props.multiple ? [] : '')
  open.value = false
}

function chosen(option) {
  return props.multiple ? draft.value.includes(option.value) : selected.value.includes(option.value)
}
</script>

<template>
  <button
    type="button"
    class="m-picker"
    :class="{ 'is-empty': !labels.length, 'is-disabled': disabled, 'is-chip': chip }"
    :disabled="disabled"
    @click="open = true"
  >
    <span class="m-picker__text">{{ labels.length ? labels.join('、') : placeholder }}</span>
    <span v-if="multiple && labels.length > 1" class="m-picker__count">{{ labels.length }}</span>
    <svg class="m-picker__chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
      <path stroke-linecap="round" stroke-linejoin="round" d="M19 9l-7 7-7-7" />
    </svg>
  </button>

  <!-- 底部只有「清空 / 确定」，两个都不是放弃：右上角的 ✕ 是唯一的退路。 -->
  <MSheet v-model="open" :title="title" full keep-close>
    <div class="m-picker__search">
      <el-input v-model="keyword" clearable placeholder="搜索" />
    </div>
    <template v-for="group in groups" :key="group.name">
      <div v-if="group.name" class="m-section">{{ group.name }}</div>
      <button
        v-for="option in group.items"
        :key="option.value"
        type="button"
        class="m-picker__option"
        :class="{ 'is-chosen': chosen(option), 'is-disabled': option.disabled }"
        :disabled="option.disabled"
        @click="toggle(option)"
      >
        <span class="m-picker__label">{{ option.label }}</span>
        <small v-if="option.desc">{{ option.desc }}</small>
        <i v-if="chosen(option)" class="m-picker__tick" aria-hidden="true">✓</i>
      </button>
    </template>
    <div v-if="!groups.length" class="m-empty">没有匹配的选项</div>

    <template v-if="multiple || clearable" #footer>
      <el-button v-if="clearable" @click="clear">清空</el-button>
      <el-button v-if="multiple" type="primary" @click="confirm">确定（{{ draft.length }}）</el-button>
    </template>
  </MSheet>
</template>

<style scoped>
.m-picker {
  width: 100%;
  min-height: var(--m-tap);
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 9px 12px;
  color: var(--sb-text);
  text-align: left;
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line-strong);
  border-radius: var(--sb-radius-sm);
  font: inherit;
  font-size: 15px;
  cursor: pointer;
  transition: all 0.15s ease;
}
.m-picker.is-empty .m-picker__text { color: var(--sb-muted); }
.m-picker.is-disabled { opacity: .55; cursor: not-allowed; }
.m-picker.is-chip {
  width: auto;
  max-width: 100%;
  min-height: 38px;
  height: 38px;
  box-sizing: border-box;
  padding: 0 12px;
  border-radius: 10px;
  font-size: 13px;
  background: var(--neu-inset-bg);
  border: 1px solid var(--sb-line);
  box-shadow: var(--sh-inset);
  color: var(--sb-muted);
  display: inline-flex;
  align-items: center;
  justify-content: space-between;
  gap: 6px;
  cursor: pointer;
  transition: all 0.15s ease;
}
.m-picker.is-chip:hover {
  color: var(--sb-text);
}
html.dark .m-picker.is-chip:not(.is-empty) {
  color: #F8FAFC;
  background: rgba(255, 255, 255, 0.14);
  border-color: var(--sb-line-strong);
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.35);
}
html.light .m-picker.is-chip:not(.is-empty),
html:not(.dark) .m-picker.is-chip:not(.is-empty) {
  color: #0F172A;
  background: #FFFFFF;
  border-color: var(--sb-line-strong);
  font-weight: 600;
  box-shadow: 0 1px 4px rgba(100, 104, 123, 0.2);
}
.m-picker__chevron {
  width: 11px;
  height: 11px;
  flex-shrink: 0;
  opacity: 0.65;
  transition: transform 0.2s ease, opacity 0.2s ease;
}
.m-picker:hover .m-picker__chevron {
  opacity: 0.9;
}
.m-picker__text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.m-picker__count {
  flex: none;
  padding: 1px 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}
html.dark .m-picker__count {
  color: #0E1015;
  background: #F8FAFC;
}
html.light .m-picker__count,
html:not(.dark) .m-picker__count {
  color: #FFFFFF;
  background: #0F172A;
}
.m-picker__arrow { flex: none; color: var(--sb-muted); font-size: 18px; line-height: 1; }
.m-picker__search {
  position: sticky;
  top: -16px;
  z-index: 1;
  margin: -16px -16px 12px;
  padding: 12px 16px;
  background: var(--sb-surface);
  border-bottom: 1px solid var(--sb-line);
}
.m-picker__option {
  position: relative;
  width: 100%;
  min-height: var(--m-tap);
  padding: 11px 36px 11px 14px;
  color: var(--sb-text);
  text-align: left;
  background: var(--sb-surface-2);
  border: 1px solid var(--sb-line);
  border-radius: var(--sb-radius-sm);
  font: inherit;
  cursor: pointer;
  transition: all 0.15s ease;
}
.m-picker__option + .m-picker__option { margin-top: 7px; }
.m-picker__option.is-chosen {
  background: var(--sb-accent-soft);
  border-color: var(--sb-line-strong);
  box-shadow: 0 0 0 1px var(--sb-text);
}
.m-picker__option.is-disabled { color: var(--sb-muted); opacity: .5; cursor: not-allowed; }
.m-picker__label { display: block; font-size: 14.5px; font-weight: 550; line-height: 1.4; word-break: break-all; }
.m-picker__option small { display: block; margin-top: 3px; color: var(--sb-muted); font-size: 12px; line-height: 1.45; word-break: break-all; }
.m-picker__tick { position: absolute; top: 50%; right: 14px; transform: translateY(-50%); color: var(--sb-text); font-style: normal; font-weight: 700; font-size: 16px; }
</style>
