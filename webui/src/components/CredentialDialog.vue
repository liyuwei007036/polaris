<script setup>
import { reactive, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Boolean, required: true },
  saving: { type: Boolean, default: false },
  outbounds: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'save'])
const form = reactive({ name: '', outbound_id: 'direct' })

watch(
  () => props.modelValue,
  (open) => {
    if (open) Object.assign(form, { name: '', outbound_id: 'direct' })
  },
)

function save() {
  if (!form.name.trim()) return
  emit('save', { name: form.name.trim(), outbound_id: form.outbound_id })
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    title="添加接入用户"
    width="520px"
    destroy-on-close
    @close="emit('update:modelValue', false)"
  >
    <el-alert
      title="系统会自动生成客户端需要的连接信息。您可以为这个用户单独选择上网出口。"
      type="info"
      show-icon
      :closable="false"
      style="margin-bottom: 18px"
    />
    <el-form label-position="top">
      <el-form-item label="用户名称" required>
        <el-input v-model="form.name" placeholder="例如：日本线路用户" />
      </el-form-item>
      <el-form-item label="上网出口">
        <el-select v-model="form.outbound_id" style="width: 100%">
          <el-option label="服务器直连" value="direct" />
          <el-option
            v-for="outbound in outbounds.filter((item) => item.enabled && item.type !== 'direct')"
            :key="outbound.id"
            :label="outbound.name"
            :value="outbound.id"
          />
        </el-select>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" :disabled="!form.name.trim()" @click="save">添加用户</el-button>
    </template>
  </el-dialog>
</template>
