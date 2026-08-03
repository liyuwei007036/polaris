<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { CircleCheck, Delete, Plus } from '@element-plus/icons-vue'
import {
  createListenerModel,
  listenerProfileMap,
  listenerProfiles,
  listenerPayload,
  protocolMap,
  securityOptions,
} from '../protocols'

const props = defineProps({
  modelValue: { type: Boolean, required: true },
  listener: { type: Object, default: null },
  nodes: { type: Array, default: () => [] },
  outbounds: { type: Array, default: () => [] },
  endpoints: { type: Array, default: () => [] },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'save'])

const formRef = ref()
const model = ref(createListenerModel(null, ''))
const accounts = ref([])
const selectedProfile = computed(() => listenerProfileMap[model.value.profile])
const showReality = computed(() => model.value.security === 'reality')
const portManagedBySystem = computed(() => Boolean(props.listener && ['127.0.0.1', '::1'].includes(props.listener.listen_address) && props.listener.backend_port !== props.listener.port))

const rules = computed(() => ({
  profile: [{ required: true, message: '请选择接入模式', trigger: 'change' }],
  node_id: [{ required: true, message: '请选择服务器', trigger: 'change' }],
  name: [{ required: true, message: '请输入服务名称', trigger: 'blur' }],
  connection_domain: [{ required: true, message: '请输入连接域名', trigger: 'blur' }],
  port: [{ required: true, message: '请输入服务端口', trigger: 'blur' }],
  reality_handshake_server: showReality.value ? [{ required: true, message: '请输入 Reality 目标网站', trigger: 'blur' }] : [],
}))

function randomHex(byteLength) {
  const bytes = new Uint8Array(byteLength)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
}

function randomAccountName() {
  return `user_${randomHex(4)}`
}

function setProfile(value) {
  model.value.profile = value
  normalizeProfile()
  if (listenerProfileMap[value]?.transport === 'ws' && !model.value.transport_path) {
    model.value.transport_path = `/${randomHex(12)}`
  }
}

watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    model.value = createListenerModel(props.listener, props.nodes[0]?.id || '')
    accounts.value = props.listener
      ? props.endpoints.map((endpoint) => ({
          id: endpoint.id,
          name: endpoint.name,
          alias: endpoint.alias || '',
          enabled: endpoint.enabled,
          outbound_id: endpoint.outbound_id || 'direct',
        }))
      : [{ id: '', name: randomAccountName(), alias: '', enabled: true, outbound_id: 'direct' }]
    normalizeProfile()
    await nextTick()
    formRef.value?.clearValidate()
  },
)

watch(
  () => model.value.profile,
  () => {
    normalizeProfile()
  },
)

function normalizeProfile() {
  const profile = listenerProfileMap[model.value.profile]
  if (!profile) return
  const protocol = protocolMap[profile.protocol]
  model.value.protocol = profile.protocol
  model.value.network = protocol.network
  model.value.security = profile.security
  model.value.transport_type = profile.transport
  if (!props.listener) {
    model.value.name = `${profile.label} 接入服务`
    model.value.port = protocol.defaultPort || 443
  }
}

function close() {
  emit('update:modelValue', false)
}

function addAccount() {
  accounts.value.push({ id: '', name: randomAccountName(), alias: '', enabled: true, outbound_id: 'direct' })
}

function removeAccount(index) {
  if (accounts.value.length > 1) accounts.value.splice(index, 1)
}

async function save() {
  try {
    await formRef.value.validate()
    if (!selectedProfile.value) throw new Error('请选择接入模式')
    if (selectedProfile.value.transport === 'grpc' && !model.value.transport_service_name) {
      throw new Error('请填写 gRPC 服务名称')
    }
    const names = accounts.value.map((item) => item.name.trim())
    const aliases = accounts.value.map((item) => item.alias.trim())
    if (!names.length || names.some((name) => !name)) throw new Error('请至少保留一个已填写名称的用户')
    if (new Set(names).size !== names.length) throw new Error('同一接入服务中的用户名称不能重复')
    if (aliases.some((alias) => !alias)) throw new Error('请为每个用户填写客户端节点别名')
    if (new Set(aliases).size !== aliases.length) throw new Error('同一接入服务中的客户端节点别名不能重复')
    emit('save', {
      listener: listenerPayload(model.value),
      accounts: accounts.value.map((item) => ({
            id: item.id,
            name: item.name.trim(),
            alias: item.alias.trim(),
            enabled: item.enabled,
            outbound_id: item.outbound_id || 'direct',
          })),
    })
  } catch (error) {
    if (error instanceof Error) ElMessage.error(error.message)
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="listener ? '修改接入服务' : '新建接入服务'"
    width="min(1040px, 96vw)"
    destroy-on-close
    @close="close"
  >
    <div class="listener-dialog-body">
      <div class="form-section">
        <div class="form-section__head">选择连接协议</div>
        <div class="protocol-select">
          <label>接入模式</label>
          <el-select :model-value="model.profile" style="width: 100%" placeholder="请选择接入模式" @update:model-value="setProfile">
            <el-option v-for="profile in listenerProfiles" :key="profile.value" :label="profile.label" :value="profile.value">
              <span>{{ profile.label }}</span>
              <span class="option-meta">{{ profile.summary }}</span>
            </el-option>
          </el-select>
        </div>
        <div v-if="selectedProfile" class="protocol-summary">
          <el-icon color="#2563eb" :size="24"><CircleCheck /></el-icon>
          <div class="protocol-summary__main">
            <strong>{{ selectedProfile.label }}</strong>
            <span>{{ selectedProfile.summary }}</span>
          </div>
          <el-tag effect="plain">{{ securityOptions[model.security]?.label }}</el-tag>
        </div>
      </div>

      <el-form ref="formRef" :model="model" :rules="rules" label-position="top">
        <div class="form-section">
          <div class="form-section__head">基础配置</div>
          <el-row :gutter="16">
            <el-col :span="8">
              <el-form-item label="服务器" prop="node_id">
                <el-select v-model="model.node_id" style="width: 100%">
                  <el-option v-for="node in nodes" :key="node.id" :label="node.name" :value="node.id" />
                </el-select>
              </el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="服务名称" prop="name">
                <el-input v-model="model.name" placeholder="例如：VLESS gRPC" />
              </el-form-item>
            </el-col>
            <el-col :span="8">
              <el-form-item label="服务端口" prop="port">
                <el-input-number v-model="model.port" :min="1" :max="65535" :disabled="portManagedBySystem" controls-position="right" style="width: 100%" />
              </el-form-item>
            </el-col>
          </el-row>
          <el-row :gutter="16">
            <el-col :span="24">
              <el-form-item label="连接域名" prop="connection_domain">
                <el-input v-model="model.connection_domain" placeholder="proxy.example.com" />
                <div class="form-hint">客户端连接这个域名；它独立于 Reality 目标网站、WebSocket Host 和 gRPC 服务名称。</div>
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div v-if="showReality" class="form-section">
          <div class="form-section__head">连接安全</div>
          <el-row v-if="showReality" :gutter="16">
            <el-col :span="14">
              <el-form-item label="目标网站" prop="reality_handshake_server">
                <el-input v-model="model.reality_handshake_server" placeholder="www.microsoft.com" />
              </el-form-item>
            </el-col>
            <el-col :span="10">
              <el-form-item label="网站端口">
                <el-input-number v-model="model.reality_handshake_port" :min="1" :max="65535" style="width: 100%" />
              </el-form-item>
            </el-col>
          </el-row>
          <el-alert v-if="showReality" title="Reality 密钥和 Short ID 会在创建接入服务时自动生成，无需手动配置。" type="success" :closable="false" />
        </div>

        <div v-if="['ws', 'grpc'].includes(model.transport_type)" class="form-section">
          <div class="form-section__head">传输配置</div>
          <el-row :gutter="16">
            <el-col v-if="model.transport_type === 'ws'" :span="12">
              <el-form-item label="请求路径">
                <el-input v-model="model.transport_path" placeholder="/proxy" />
              </el-form-item>
            </el-col>
            <el-col v-if="model.transport_type === 'ws'" :span="12">
              <el-form-item label="请求域名（Host）">
                <el-input v-model="model.transport_host" placeholder="可选" />
              </el-form-item>
            </el-col>
            <el-col v-if="model.transport_type === 'grpc'" :span="14">
              <el-form-item label="gRPC 服务名称">
                <el-input v-model="model.transport_service_name" placeholder="grpc-service" />
                <div class="form-hint">标识这条 gRPC 传输通道；客户端必须填写完全相同的值。它不是域名，也不会创建系统服务。</div>
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div class="form-section">
          <div class="account-section-head">
            <div><div class="form-section__head">用户与客户端节点</div><span>每个用户只属于当前服务器；客户端节点别名会直接写入该用户的订阅配置。</span></div>
            <el-button :icon="Plus" @click="addAccount">添加</el-button>
          </div>
          <div class="account-list">
            <div v-for="(account, index) in accounts" :key="account.id || `new-${index}`" class="account-row">
              <span class="account-index">{{ index + 1 }}</span>
              <el-input v-model="account.name" aria-label="用户名称" placeholder="用户名称" />
              <el-input v-model="account.alias" aria-label="客户端节点别名" maxlength="128" placeholder="客户端节点别名" />
              <el-select v-model="account.outbound_id" style="width: 100%">
                <el-option label="服务器直连" value="direct" />
                <el-option v-for="outbound in outbounds.filter((item) => item.type !== 'direct')" :key="outbound.id" :label="outbound.name" :value="outbound.id" />
              </el-select>
              <el-switch v-model="account.enabled" inline-prompt active-text="启用" inactive-text="停用" />
              <el-button text type="danger" :icon="Delete" :disabled="accounts.length === 1" aria-label="删除用户" @click="removeAccount(index)" />
            </div>
          </div>
        </div>

      </el-form>
    </div>

    <template #footer>
      <div class="dialog-footer">
        <el-button @click="close">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">
          {{ listener ? '保存' : '创建' }}
        </el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.protocol-select { margin-bottom: 14px; }
.protocol-select label { display: block; margin-bottom: 8px; color: var(--sb-text); font-size: 13px; font-weight: 550; }
.option-meta { float: right; max-width: 520px; margin-left: 20px; overflow: hidden; color: var(--sb-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.listener-dialog-body { max-width: 100%; max-height: min(72vh, 760px); padding-right: 4px; overflow-x: hidden; overflow-y: auto; scrollbar-width: none; }
.listener-dialog-body::-webkit-scrollbar { display: none; }
.account-section-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; margin-bottom: 14px; }
.account-section-head span { color: var(--sb-muted); font-size: 12px; }
.account-list { border-top: 1px solid var(--sb-border); }
.account-row { display: grid; grid-template-columns: 32px minmax(140px, 1fr) minmax(160px, 1fr) minmax(190px, 1.2fr) 62px 38px; gap: 10px; align-items: center; padding: 10px 0; border-bottom: 1px solid var(--sb-border); }
.account-index { color: var(--sb-muted); text-align: center; font-variant-numeric: tabular-nums; }
.form-hint { margin-top: 6px; color: var(--sb-muted); font-size: 12px; }
.dialog-footer { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 680px) {
  .listener-dialog-body :deep(.el-col) { max-width: 100%; flex: 0 0 100%; }
  .account-row { grid-template-columns: 28px 1fr 62px 36px; }
  .account-row :deep(.el-input), .account-row :deep(.el-select) { grid-column: 2 / -1; }
}
</style>
