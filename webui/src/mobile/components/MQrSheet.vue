<script setup>
// 二维码抽屉：手机自己就是扫码的那一端，所以链接文本和复制按钮
// 和二维码同等重要 —— 同一台手机上多半是复制到客户端里粘贴。
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import QRCode from 'qrcode'
import { writeClipboard } from '../../clipboard'
import MSheet from './MSheet.vue'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, default: '二维码' },
  // [{ key, label, value }]
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

watch(current, async (item) => {
  if (!item) {
    image.value = ''
    return
  }
  try {
    image.value = await QRCode.toDataURL(item.value, { width: 480, margin: 1 })
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
  <MSheet :model-value="modelValue" :title="title" @update:model-value="emit('update:modelValue', $event)">
    <div v-loading="loading" class="qr">
      <div v-if="items.length > 1" class="qr__picks">
        <button
          v-for="item in items"
          :key="item.key"
          type="button"
          class="qr__pick"
          :class="{ 'is-active': current?.key === item.key }"
          @click="selected = item.key"
        >{{ item.label }}</button>
      </div>
      <template v-if="current">
        <!-- 二维码必须黑白，深色底会让相机读不出来。 -->
        <div class="qr__frame"><img v-if="image" :src="image" :alt="`${current.label} 的二维码`" data-testid="qr-image" /></div>
        <p class="qr__value m-mono" data-testid="qr-value">{{ current.value }}</p>
        <el-button type="primary" class="qr__copy" @click="copy">复制地址</el-button>
      </template>
      <div v-else-if="!loading" class="m-empty">{{ emptyText }}</div>
    </div>
  </MSheet>
</template>

<style scoped>
.qr { display: flex; flex-direction: column; align-items: center; gap: 14px; min-height: 140px; }
.qr__picks { display: flex; flex-wrap: wrap; gap: 6px; align-self: stretch; }
.qr__pick {
  padding: 7px 11px;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .08);
  border: 1px solid var(--sb-line);
  border-radius: 999px;
  font: inherit;
  font-size: 12.5px;
  cursor: pointer;
}
.qr__pick.is-active { color: #04121f; background: var(--sb-accent); border-color: var(--sb-accent); font-weight: 600; }
.qr__frame { padding: 10px; background: #fff; border-radius: var(--m-radius); line-height: 0; }
.qr__frame img { display: block; width: min(240px, 62vw); height: min(240px, 62vw); }
.qr__value {
  align-self: stretch;
  max-height: 110px;
  padding: 10px 12px;
  color: var(--sb-text-2);
  background: rgba(148, 163, 184, .06);
  border-radius: var(--sb-radius-sm);
  overflow-y: auto;
  line-height: 1.6;
  word-break: break-all;
}
.qr__copy { align-self: stretch; height: var(--m-tap); }
</style>
