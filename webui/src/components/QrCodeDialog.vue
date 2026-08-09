<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { CopyDocument } from '@element-plus/icons-vue'
import QRCode from 'qrcode'
import { writeClipboard } from '../clipboard'

// Shows one address as a QR code so a phone can scan it instead of the
// operator retyping it. Several addresses are picked from a selector rather
// than stacked, keeping one code on screen at a time.
const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '二维码' },
  items: { type: Array, default: () => [] },
  loading: { type: Boolean, default: false },
  emptyText: { type: String, default: '暂无可扫描的地址' },
})
const emit = defineEmits(['update:modelValue'])

const selected = ref('')
const image = ref('')
const current = computed(() => props.items.find((item) => item.key === selected.value) || props.items[0] || null)

watch(() => props.items, (items) => {
  if (!items.some((item) => item.key === selected.value)) selected.value = items[0]?.key || ''
}, { immediate: true })

// Rendering fails only on input QR cannot encode at all; saying so beats
// leaving an empty frame with no explanation.
watch(current, async (item) => {
  if (!item) {
    image.value = ''
    return
  }
  try {
    image.value = await QRCode.toDataURL(item.value, { width: 240, margin: 1 })
  } catch {
    image.value = ''
    ElMessage.error('地址过长，无法生成二维码')
  }
}, { immediate: true })

async function copy() {
  if (!current.value) return
  try {
    await writeClipboard(current.value.value)
    ElMessage.success('地址已复制')
  } catch {
    ElMessage.error('自动复制失败，请使用 HTTPS 访问后重试')
  }
}
</script>

<template>
  <el-dialog
    :model-value="props.modelValue"
    :title="props.title"
    width="min(440px, 94vw)"
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-loading="props.loading" class="qr-body">
      <el-select
        v-if="props.items.length > 1"
        v-model="selected"
        class="qr-picker"
        aria-label="选择要扫描的地址"
      >
        <el-option v-for="item in props.items" :key="item.key" :label="item.label" :value="item.key" />
      </el-select>
      <template v-if="current">
        <div class="qr-frame">
          <img v-if="image" :src="image" :alt="`${current.label} 的二维码`" data-testid="qr-image" />
        </div>
        <div class="qr-value mono" data-testid="qr-value">{{ current.value }}</div>
        <el-button :icon="CopyDocument" @click="copy">复制地址</el-button>
      </template>
      <div v-else-if="!props.loading" class="qr-empty">{{ props.emptyText }}</div>
    </div>
  </el-dialog>
</template>

<style scoped>
.qr-body { display: flex; flex-direction: column; align-items: center; gap: 14px; min-height: 120px; }
.qr-picker { width: 100%; }
/* The code itself must stay black on white in both themes, or a dark page
   inverts it and phones stop reading it. */
.qr-frame { padding: 10px; border-radius: var(--sb-radius); background: #fff; line-height: 0; }
.qr-frame img { display: block; width: 240px; height: 240px; }
.qr-value { width: 100%; max-height: 96px; overflow-y: auto; color: var(--sb-muted); font-size: 12px; line-height: 1.5; text-align: center; word-break: break-all; }
.qr-empty { color: var(--sb-muted); font-size: 13px; }
</style>
