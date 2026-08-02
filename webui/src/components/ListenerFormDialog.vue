<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { CircleCheck, Delete, Plus } from '@element-plus/icons-vue'
import {
  createListenerModel,
  listenerPayload,
  protocolMap,
  protocols,
  securityOptions,
  transportOptions,
} from '../protocols'

const props = defineProps({
  modelValue: { type: Boolean, required: true },
  listener: { type: Object, default: null },
  nodes: { type: Array, default: () => [] },
  certificates: { type: Array, default: () => [] },
  realityKeys: { type: Array, default: () => [] },
  outbounds: { type: Array, default: () => [] },
  ingressRoute: { type: Object, default: null },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'save'])

const formRef = ref()
const model = ref(createListenerModel(null, ''))
const accounts = ref([])
const definition = computed(() => protocolMap[model.value.protocol] || protocols[0])
const visibleProtocols = computed(() => protocols.filter((item) => item.stable))
const allowedSecurity = computed(() => definition.value.security.map((value) => securityOptions[value]))
const showTLS = computed(() => model.value.security === 'tls')
const showReality = computed(() => model.value.security === 'reality')
const sharedPortEligible = computed(() => model.value.network === 'tcp' && (showTLS.value || showReality.value))
const securityHelp = computed(() => ({
  none: definition.value.value === 'shadowsocks'
    ? 'Shadowsocks 已自带加密，无需另行配置证书。'
    : '不使用证书时，连接不会获得额外保护。VLESS、SOCKS5 和 HTTP 仅建议在可信内网使用。',
  tls: definition.value.security.length === 1 && definition.value.security[0] === 'tls'
    ? `使用与客户端连接域名匹配的加密证书。${definition.value.label} 必须选择此方式。`
    : '使用与客户端连接域名匹配的加密证书，可保护客户端与服务器之间的连接。',
  reality: '无需准备域名证书，由 Reality 提供连接保护。仅支持部分协议。',
})[model.value.security])

const rules = computed(() => ({
  node_id: [{ required: true, message: '请选择服务器', trigger: 'change' }],
  name: [{ required: true, message: '请输入服务名称', trigger: 'blur' }],
  port: [{ required: true, message: '请输入服务端口', trigger: 'blur' }],
  certificate_id: showTLS.value ? [{ required: true, message: '请选择加密证书', trigger: 'change' }] : [],
  tls_server_name: showTLS.value ? [{ required: true, message: '请输入证书域名', trigger: 'blur' }] : [],
  reality_key_id: showReality.value ? [{ required: true, message: '请选择 Reality 连接密钥', trigger: 'change' }] : [],
  reality_handshake_server: showReality.value ? [{ required: true, message: '请输入 Reality 目标网站', trigger: 'blur' }] : [],
}))

watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    model.value = createListenerModel(props.listener, props.nodes[0]?.id || '')
    model.value.shared_port = Boolean(props.ingressRoute || model.value.shared_port)
    model.value.ingress_sni = props.ingressRoute?.sni || (model.value.security === 'reality' ? model.value.reality_handshake_server : model.value.tls_server_name) || ''
    accounts.value = [{ name: '用户 1', outbound_id: 'direct' }]
    normalizeProtocol()
    await nextTick()
    formRef.value?.clearValidate()
  },
)

watch(
  () => model.value.protocol,
  () => {
    normalizeProtocol()
  },
)

watch(sharedPortEligible, (eligible) => {
  if (!eligible && !props.listener) model.value.shared_port = false
})

watch(() => model.value.shared_port, (enabled) => {
  if (enabled && !model.value.ingress_sni) {
    model.value.ingress_sni = model.value.security === 'reality' ? model.value.reality_handshake_server : model.value.tls_server_name
  }
})

function normalizeProtocol() {
  const protocol = definition.value
  if (!protocol) return
  if (!protocol.security.includes(model.value.security)) model.value.security = protocol.security[0]
  if (protocol.networks && !protocol.networks.includes(model.value.network)) model.value.network = protocol.networks[0]
  if (!protocol.networks) model.value.network = protocol.network
  if (!protocol.transports) model.value.transport_type = ''
  if (!props.listener) {
    model.value.name = `${protocol.label} 接入服务`
    model.value.port = protocol.defaultPort || 443
  }
}

function close() {
  emit('update:modelValue', false)
}

function addAccount() {
  accounts.value.push({ name: `用户 ${accounts.value.length + 1}`, outbound_id: 'direct' })
}

function removeAccount(index) {
  if (accounts.value.length > 1) accounts.value.splice(index, 1)
}

async function save() {
  try {
    await formRef.value.validate()
    if (
      ['ws', 'httpupgrade'].includes(model.value.transport_type) &&
      !model.value.transport_path
    ) {
      throw new Error('请填写客户端请求路径')
    }
    if (model.value.transport_type === 'grpc' && !model.value.transport_service_name) {
      throw new Error('请填写 gRPC 服务名称')
    }
	if (!props.listener) {
	  const names = accounts.value.map((item) => item.name.trim())
	  if (!names.length || names.some((name) => !name)) throw new Error('请至少添加一个已填写名称的用户')
	  if (new Set(names).size !== names.length) throw new Error('同一接入服务中的用户名称不能重复')
	}
	if (model.value.shared_port && !model.value.ingress_sni.trim()) throw new Error('启用端口共享后，请填写客户端访问域名')
    emit('save', {
      listener: listenerPayload(model.value),
	  accounts: props.listener ? [] : accounts.value.map((item) => ({ name: item.name.trim(), outbound_id: item.outbound_id || 'direct' })),
	  ingress_route: model.value.shared_port ? {
	    listen_address: '0.0.0.0', port: Number(model.value.port), sni: model.value.ingress_sni.trim(), enabled: true,
	  } : null,
    })
  } catch (error) {
    if (error instanceof Error) ElMessage.error(error.message)
  }
}
</script>

<template>
  <el-dialog
    :model-value="modelValue"
    :title="listener ? '编辑接入服务' : '新建接入服务'"
    width="min(1040px, 96vw)"
    destroy-on-close
    @close="close"
  >
    <div class="listener-dialog-body">
      <div class="form-section">
        <div class="form-section__head">选择连接协议</div>
        <div class="protocol-select">
          <label>客户端使用的协议</label>
          <el-select v-model="model.protocol" style="width: 100%" placeholder="请选择连接协议">
            <el-option v-for="protocol in visibleProtocols" :key="protocol.value" :label="protocol.recommended ? `${protocol.label}（推荐）` : protocol.label" :value="protocol.value">
              <span>{{ protocol.label }}</span>
              <span class="option-meta">{{ protocol.summary }}</span>
            </el-option>
          </el-select>
        </div>
        <div class="protocol-summary">
          <el-icon color="#2563eb" :size="24"><CircleCheck /></el-icon>
          <div class="protocol-summary__main">
            <strong>{{ definition.label }}</strong>
            <span>{{ definition.summary }}</span>
          </div>
          <el-tag effect="plain">{{ model.network.toUpperCase() }}</el-tag>
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
                <el-input-number v-model="model.port" :min="1" :max="65535" :disabled="Boolean(listener && model.shared_port)" controls-position="right" style="width: 100%" />
              </el-form-item>
            </el-col>
            <el-col v-if="definition.networks" :span="12">
              <el-form-item label="连接类型">
                <el-segmented v-model="model.network" :options="definition.networks.map((value) => ({ label: value.toUpperCase(), value }))" />
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div class="form-section">
          <div class="form-section__head">连接安全</div>
          <el-form-item v-if="allowedSecurity.length > 1" label="保护方式">
            <el-segmented v-model="model.security" :options="allowedSecurity" />
          </el-form-item>
          <el-alert :title="securityHelp" type="info" show-icon :closable="false" style="margin-bottom: 16px" />

          <div v-if="model.protocol === 'socks'" class="warning-strip">
            SOCKS5 不会加密连接，仅建议在可信内网使用，或限制允许连接的来源地址。
          </div>

          <el-row v-if="showTLS" :gutter="16">
            <el-col :span="12">
              <el-form-item label="证书对应域名" prop="tls_server_name">
                <el-input v-model="model.tls_server_name" placeholder="proxy.example.com" />
              </el-form-item>
            </el-col>
            <el-col :span="12">
              <el-form-item label="加密证书" prop="certificate_id">
                <el-select v-model="model.certificate_id" style="width: 100%" placeholder="请选择已导入的证书">
                  <el-option v-for="certificate in certificates" :key="certificate.id" :label="certificate.name" :value="certificate.id" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>

          <el-row v-if="showReality" :gutter="16">
            <el-col :span="8">
              <el-form-item label="目标网站" prop="reality_handshake_server">
                <el-input v-model="model.reality_handshake_server" placeholder="www.microsoft.com" />
              </el-form-item>
            </el-col>
            <el-col :span="6">
              <el-form-item label="网站端口">
                <el-input-number v-model="model.reality_handshake_port" :min="1" :max="65535" style="width: 100%" />
              </el-form-item>
            </el-col>
            <el-col :span="10">
              <el-form-item label="Reality 连接密钥" prop="reality_key_id">
                <el-select v-model="model.reality_key_id" style="width: 100%" placeholder="请选择已生成的密钥">
                  <el-option v-for="key in realityKeys" :key="key.id" :label="key.name" :value="key.id" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div v-if="sharedPortEligible && (!listener || model.shared_port)" class="form-section shared-port-section">
          <div class="form-section__head">端口共享</div>
          <el-switch v-model="model.shared_port" :disabled="Boolean(listener)" active-text="允许多个加密接入服务共用当前公网端口" />
          <el-alert
            v-if="model.shared_port"
            title="客户端仍连接当前端口。系统会根据客户端访问域名，将连接转发到对应服务。内部端口会自动分配。"
            type="info"
            show-icon
            :closable="false"
            style="margin-top: 14px"
          />
          <el-form-item v-if="model.shared_port" label="客户端访问域名" style="margin-top: 14px">
            <el-input v-model="model.ingress_sni" placeholder="例如 grpc.example.com；同一端口下不能重复" />
          </el-form-item>
        </div>

        <el-collapse v-if="definition.transports">
          <el-collapse-item title="高级设置：连接传输方式" name="optional">
        <div v-if="definition.transports" class="form-section">
          <div class="form-section__head">传输方式</div>
          <el-row :gutter="16">
            <el-col :span="10">
              <el-form-item label="传输方式">
                <el-select v-model="model.transport_type" style="width: 100%">
                  <el-option v-for="option in transportOptions" :key="option.value" :label="option.label" :value="option.value" />
                </el-select>
              </el-form-item>
            </el-col>
            <el-col v-if="['ws', 'httpupgrade', 'http'].includes(model.transport_type)" :span="14">
              <el-form-item label="请求路径">
                <el-input v-model="model.transport_path" placeholder="/proxy" />
              </el-form-item>
            </el-col>
            <el-col v-if="['ws', 'httpupgrade', 'http'].includes(model.transport_type)" :span="12">
              <el-form-item label="请求域名（Host）">
                <el-input v-model="model.transport_host" placeholder="可选" />
              </el-form-item>
            </el-col>
            <el-col v-if="model.transport_type === 'grpc'" :span="14">
              <el-form-item label="gRPC 服务名称">
                <el-input v-model="model.transport_service_name" placeholder="grpc-service" />
              </el-form-item>
            </el-col>
          </el-row>
        </div>

          </el-collapse-item>
        </el-collapse>

        <div v-if="!listener" class="form-section">
          <div class="account-section-head">
            <div><div class="form-section__head">接入用户与上网出口</div><span>每个用户都会获得独立连接信息，并可使用不同的上网出口。</span></div>
            <el-button :icon="Plus" @click="addAccount">添加用户</el-button>
          </div>
          <div class="account-list">
            <div v-for="(account, index) in accounts" :key="index" class="account-row">
              <span class="account-index">{{ index + 1 }}</span>
              <el-input v-model="account.name" placeholder="用户名称" />
              <el-select v-model="account.outbound_id" style="width: 100%">
                <el-option label="服务器直连" value="direct" />
                <el-option v-for="outbound in outbounds.filter((item) => item.type !== 'direct')" :key="outbound.id" :label="outbound.name" :value="outbound.id" />
              </el-select>
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
          {{ listener ? '保存并应用' : '创建并应用' }}
        </el-button>
      </div>
    </template>
  </el-dialog>
</template>

<style scoped>
.protocol-select { margin-bottom: 14px; }
.protocol-select label { display: block; margin-bottom: 8px; color: var(--sb-text); font-size: 13px; font-weight: 550; }
.option-meta { float: right; max-width: 520px; margin-left: 20px; overflow: hidden; color: var(--sb-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.listener-dialog-body { max-height: min(72vh, 760px); padding-right: 4px; overflow-y: auto; }
.account-section-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 20px; margin-bottom: 14px; }
.account-section-head span { color: var(--sb-muted); font-size: 12px; }
.account-list { border-top: 1px solid var(--sb-border); }
.account-row { display: grid; grid-template-columns: 32px minmax(160px, 1fr) minmax(220px, 1.25fr) 38px; gap: 10px; align-items: center; padding: 10px 0; border-bottom: 1px solid var(--sb-border); }
.account-index { color: var(--sb-muted); text-align: center; font-variant-numeric: tabular-nums; }
.dialog-footer { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 680px) {
  .listener-dialog-body :deep(.el-col) { max-width: 100%; flex: 0 0 100%; }
  .account-row { grid-template-columns: 28px 1fr 36px; }
  .account-row :deep(.el-select) { grid-column: 2; }
}
</style>
