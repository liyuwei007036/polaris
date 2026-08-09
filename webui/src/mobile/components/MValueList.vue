<script setup>
// 多值输入：域名列表、网段列表、DNS 服务器列表都用它。
// 桌面版用的是 el-select 的 allow-create，手机上那个下拉浮层会挡住输入框。
import { ref } from 'vue'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
  placeholder: { type: String, default: '输入后点添加' },
})
const emit = defineEmits(['update:modelValue'])

const draft = ref('')

function add() {
  const value = draft.value.trim()
  if (!value) return
  if (!props.modelValue.includes(value)) emit('update:modelValue', [...props.modelValue, value])
  draft.value = ''
}

function remove(value) {
  emit('update:modelValue', props.modelValue.filter((item) => item !== value))
}
</script>

<template>
  <div class="m-values">
    <div class="m-values__input">
      <el-input v-model="draft" :placeholder="placeholder" @keyup.enter="add" />
      <el-button :disabled="!draft.trim()" @click="add">添加</el-button>
    </div>
    <div v-if="modelValue.length" class="m-chips">
      <span v-for="value in modelValue" :key="value" class="m-chip">
        {{ value }}
        <button type="button" :aria-label="`删除 ${value}`" @click="remove(value)">✕</button>
      </span>
    </div>
  </div>
</template>

<style scoped>
.m-values__input { display: flex; gap: 8px; }
.m-values__input :deep(.el-input) { flex: 1; min-width: 0; }
.m-values__input :deep(.el-button) { flex: none; height: auto; }
</style>
