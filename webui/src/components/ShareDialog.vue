<script setup>
import { computed, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { CopyDocument, Delete, Link } from '@element-plus/icons-vue'
import { api, del, post } from '../api'
import { writeClipboard } from '../clipboard'
import { formatDateTime } from '../format'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  page: { type: String, required: true },
})
const emit = defineEmits(['update:modelValue'])

const pageLabel = computed(() => (props.page === 'dashboard' ? '运行概览' : '服务器'))
const expiryOptions = [
  { label: '1 小时', value: 3600 },
  { label: '24 小时', value: 86400 },
  { label: '7 天', value: 604800 },
  { label: '30 天', value: 2592000 },
]
const expiresIn = ref(86400)
const creating = ref(false)
const loading = ref(false)
const created = ref(null)
const shares = ref([])

const visible = computed({
  get: () => props.modelValue,
  set: (value) => emit('update:modelValue', value),
})

const activeShares = computed(() => shares.value.filter((item) => item.page === props.page))

async function loadShares() {
  loading.value = true
  try {
    const result = await api('/shares')
    shares.value = result.shares || []
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    loading.value = false
  }
}

async function create() {
  creating.value = true
  try {
    const result = await post('/shares', { page: props.page, expires_in: expiresIn.value })
    created.value = result
    shares.value = [result.share, ...shares.value]
    ElMessage.success('分享链接已生成')
  } catch (error) {
    ElMessage.error(error.message)
  } finally {
    creating.value = false
  }
}

async function copyLink() {
  if (!created.value?.url) return
  try {
    await writeClipboard(created.value.url)
    ElMessage.success('分享链接已复制')
  } catch (error) {
    ElMessage.error(error.message)
  }
}

async function revoke(link) {
  await ElMessageBox.confirm(
    `撤销后该链接会立即失效，已拿到链接的人将无法再查看${pageLabel.value}页面。`,
    '撤销分享链接',
    { type: 'warning', confirmButtonText: '撤销链接' },
  )
  try {
    await del(`/shares/${link.id}`)
    shares.value = shares.value.filter((item) => item.id !== link.id)
    if (created.value?.share?.id === link.id) created.value = null
    ElMessage.success('分享链接已撤销')
  } catch (error) {
    ElMessage.error(error.message)
  }
}

function onOpen() {
  created.value = null
  loadShares()
}
</script>

<template>
  <el-dialog
    v-model="visible"
    title="分享页面"
    width="600px"
    destroy-on-close
    @open="onOpen"
  >
    <div class="share-dialog">
      <div class="share-dialog__notice">
        <el-icon><Link /></el-icon>
        <div>
          <strong>生成只读分享链接</strong>
          <p>访客无需登录即可查看{{ pageLabel }}，但不能进行任何操作。服务器 IP、域名、服务商和费用等核心信息会自动脱敏。</p>
        </div>
      </div>

      <div class="share-dialog__expiry">
        <span>失效时间</span>
        <el-radio-group v-model="expiresIn">
          <el-radio-button v-for="option in expiryOptions" :key="option.value" :value="option.value">
            {{ option.label }}
          </el-radio-button>
        </el-radio-group>
      </div>

      <el-button type="primary" :loading="creating" @click="create">
        生成分享链接
      </el-button>

      <div v-if="created" class="share-dialog__result">
        <label>分享链接（只显示这一次，请立即复制）</label>
        <el-input :model-value="created.url" readonly>
          <template #append>
            <el-button :icon="CopyDocument" @click="copyLink">复制</el-button>
          </template>
        </el-input>
        <p>有效期至 {{ formatDateTime(created.share.expires_at) }}</p>
      </div>

      <div class="share-dialog__list">
        <div class="share-dialog__list-head">
          <h4>已生效的链接</h4>
          <el-button text size="small" :loading="loading" @click="loadShares">刷新</el-button>
        </div>
        <p v-if="!activeShares.length && !loading" class="share-dialog__empty">还没有生效中的分享链接</p>
        <div v-for="link in activeShares" :key="link.id" class="share-dialog__item">
          <div>
            <strong>{{ pageLabel }}</strong>
            <small>有效期至 {{ formatDateTime(link.expires_at) }}</small>
          </div>
          <el-button text type="danger" :icon="Delete" @click="revoke(link)">撤销</el-button>
        </div>
      </div>
    </div>
  </el-dialog>
</template>

<style scoped>
.share-dialog { display: flex; flex-direction: column; gap: 16px; }
.share-dialog__notice {
  display: flex; gap: 11px; align-items: flex-start;
  padding: 13px 14px; border-radius: 11px;
  background: color-mix(in oklab, var(--sb-accent, #0ea5e9) 8%, transparent);
  border: 1px solid color-mix(in oklab, var(--sb-accent, #0ea5e9) 18%, transparent);
}
.share-dialog__notice .el-icon { color: #0ea5e9; margin-top: 2px; }
.share-dialog__notice strong { font-size: 13px; }
.share-dialog__notice p { margin: 5px 0 0; font-size: 12px; line-height: 1.6; color: var(--sb-muted, #64748b); }
.share-dialog__expiry { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
.share-dialog__expiry > span { font-size: 13px; color: var(--sb-muted, #64748b); }
.share-dialog__result {
  padding: 13px 14px; border-radius: 11px;
  background: color-mix(in oklab, #10b981 8%, transparent);
  border: 1px solid color-mix(in oklab, #10b981 22%, transparent);
}
.share-dialog__result label { display: block; font-size: 12px; font-weight: 600; margin-bottom: 8px; }
.share-dialog__result p { margin: 8px 0 0; font-size: 11px; color: var(--sb-muted, #64748b); }
.share-dialog__list { border-top: 1px solid color-mix(in oklab, var(--sb-muted, #64748b) 16%, transparent); padding-top: 12px; }
.share-dialog__list-head { display: flex; align-items: center; justify-content: space-between; }
.share-dialog__list-head h4 { margin: 0; font-size: 13px; }
.share-dialog__empty { margin: 10px 0 0; font-size: 12px; color: var(--sb-muted, #64748b); }
.share-dialog__item {
  display: flex; align-items: center; justify-content: space-between; gap: 12px;
  padding: 9px 0; border-bottom: 1px dashed color-mix(in oklab, var(--sb-muted, #64748b) 18%, transparent);
}
.share-dialog__item:last-child { border-bottom: none; }
.share-dialog__item div { display: flex; flex-direction: column; gap: 3px; }
.share-dialog__item strong { font-size: 12px; }
.share-dialog__item small { font-size: 11px; color: var(--sb-muted, #64748b); }
</style>
