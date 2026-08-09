<script setup>
import { computed, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Delete, Plus } from '@element-plus/icons-vue'
import {
  createListenerModel,
  defaultALPNFor,
  listenerProfileMap,
  listenerProfiles,
  listenerPayload,
  protocolMap,
  securityOptions,
} from '../../protocols'
import MSheet from './MSheet.vue'
import MPicker from './MPicker.vue'

// 接入服务表单。桌面版是一个 1040px 宽的三列弹窗，手机上换成整屏单列，
// 一节一节往下填；模式改成可点选的方块，比下拉好按。
const props = defineProps({
  modelValue: { type: Boolean, required: true },
  listener: { type: Object, default: null },
  template: { type: Object, default: null },
  nodes: { type: Array, default: () => [] },
  outbounds: { type: Array, default: () => [] },
  dnsRecords: { type: Array, default: () => [] },
  endpoints: { type: Array, default: () => [] },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'save'])

const model = ref(createListenerModel(null, ''))
const accounts = ref([])
const expanded = ref(0)

const selectedProfile = computed(() => listenerProfileMap[model.value.profile])
const showReality = computed(() => model.value.security === 'reality')
const nodeAddress = computed(() => props.nodes.find((node) => node.id === model.value.node_id)?.client_address || '')
const nodeOptions = computed(() => props.nodes.map((node) => ({ value: node.id, label: node.name, desc: node.client_address || '未填写客户端连接地址' })))
const outboundOptions = computed(() => [
  { value: 'direct', label: '服务器直连' },
  ...props.outbounds.filter((item) => item.type !== 'direct').map((item) => ({ value: item.id, label: item.name, desc: `${item.type.toUpperCase()} ${item.server}:${item.server_port}` })),
])

function recordName(record) {
  return String(record.name || '').replace(/\.$/, '')
}

// 解析到这台服务器的域名：A/AAAA 直接指向它的、CNAME 指到那些名字的，
// 以及服务器本身就用域名接入时的那个域名。
const matchedDomains = computed(() => {
  const address = nodeAddress.value
  if (!address) return []
  const found = new Map()
  const add = (name, proxied) => { if (name && !found.has(name)) found.set(name, proxied) }
  for (const record of props.dnsRecords) {
    if (['A', 'AAAA'].includes(record.type) && record.content === address) add(recordName(record), Boolean(record.proxied))
  }
  if (!/^[0-9.]+$/.test(address) && !address.includes(':')) {
    const own = props.dnsRecords.find((record) => recordName(record) === address)
    add(address, own ? Boolean(own.proxied) : null)
  }
  let grew = true
  while (grew) {
    grew = false
    for (const record of props.dnsRecords) {
      if (record.type !== 'CNAME' || found.has(recordName(record))) continue
      const target = String(record.content || '').replace(/\.$/, '')
      if (target === address || found.has(target)) {
        add(recordName(record), Boolean(record.proxied))
        grew = true
      }
    }
  }
  return [...found].map(([value, proxied]) => ({
    value,
    note: proxied === null ? '' : proxied ? '橙云' : '直连',
  }))
})

function randomHex(byteLength) {
  const bytes = new Uint8Array(byteLength)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
}

function randomAccountName() {
  return `user_${randomHex(4)}`
}

// 能猜到的 WebSocket 路径就是被探测出来的入口，所以路径一律生成而不是手填。
function randomPath() {
  return `/${randomHex(12)}`
}

function setProfile(value) {
  model.value.profile = value
  model.value.tls_alpn = defaultALPNFor(value)
  normalizeProfile()
  if (listenerProfileMap[value]?.transport === 'ws' && !model.value.transport_path) {
    model.value.transport_path = randomPath()
  }
}

function normalizeProfile() {
  const profile = listenerProfileMap[model.value.profile]
  if (!profile) return
  const protocol = protocolMap[profile.protocol]
  model.value.protocol = profile.protocol
  model.value.network = protocol.network
  model.value.security = profile.security
  model.value.transport_type = profile.transport
  if (!props.listener && !props.template) {
    model.value.name = `${profile.label} 接入服务`
    model.value.port = protocol.defaultPort || 443
  }
}

watch(() => props.modelValue, (open) => {
  if (!open) return
  model.value = createListenerModel(props.listener || props.template, props.nodes[0]?.id || '')
  // 编辑保留客户端正在用的路径；新建和复制各自生成新的。
  if (!props.listener) model.value.transport_path = randomPath()
  // 复制时把原有用户当成新用户带入：去掉 id，保存时会生成各自的新凭据。
  accounts.value = props.listener || props.template
    ? props.endpoints.map((endpoint) => ({
        id: props.listener ? endpoint.id : '',
        name: endpoint.name,
        alias: endpoint.alias || endpoint.name,
        enabled: endpoint.enabled,
        outbound_id: endpoint.outbound_id || 'direct',
      }))
    : []
  if (!accounts.value.length) {
    accounts.value = [{ id: '', name: randomAccountName(), alias: '', enabled: true, outbound_id: 'direct' }]
  }
  expanded.value = 0
  normalizeProfile()
})

function addAccount() {
  accounts.value.push({ id: '', name: randomAccountName(), alias: '', enabled: true, outbound_id: 'direct' })
  expanded.value = accounts.value.length - 1
}

function removeAccount(index) {
  if (accounts.value.length === 1) return
  accounts.value.splice(index, 1)
  expanded.value = -1
}

function save() {
  try {
    if (!selectedProfile.value) throw new Error('请选择接入模式')
    if (!model.value.node_id) throw new Error('请选择服务器')
    if (!model.value.name.trim()) throw new Error('请输入服务名称')
    if (!model.value.connection_domain.trim()) throw new Error('请填写连接域名')
    if (!model.value.port) throw new Error('请输入服务端口')
    if (showReality.value && !model.value.reality_handshake_server) throw new Error('请输入 Reality 目标网站')
    if (selectedProfile.value.transport === 'grpc' && !model.value.transport_service_name) throw new Error('请填写 gRPC 服务名称')
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
    ElMessage.error(error.message)
  }
}
</script>

<template>
  <MSheet
    :model-value="modelValue"
    :title="listener ? '修改接入服务' : template ? '复制接入服务' : '新建接入服务'"
    full
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div v-if="template" class="m-notice m-notice--info">
      已带入原配置，保存前请确认服务器、端口与域名，并修改客户端节点别名。连接凭据和 Reality 密钥会重新生成。
    </div>

    <div class="m-section">接入模式</div>
    <button
      v-for="profile in listenerProfiles"
      :key="profile.value"
      type="button"
      class="m-choice"
      :class="{ 'is-active': model.profile === profile.value }"
      @click="setProfile(profile.value)"
    >
      <strong>{{ profile.label }}</strong>
      <span>{{ profile.summary }} · {{ securityOptions[profile.security]?.label }}</span>
    </button>

    <div class="m-section">基础配置</div>
    <div class="m-field">
      <label class="m-field__label">服务器 <em>*</em></label>
      <MPicker v-model="model.node_id" :options="nodeOptions" title="选择服务器" placeholder="选择服务器" />
    </div>
    <div class="m-field">
      <label class="m-field__label">服务名称 <em>*</em></label>
      <el-input v-model="model.name" aria-label="服务名称" placeholder="例如：VLESS gRPC" />
    </div>
    <div class="m-field">
      <label class="m-field__label">服务端口 <em>*</em></label>
      <el-input-number v-model="model.port" :min="1" :max="65535" controls-position="right" aria-label="服务端口" style="width: 100%" />
      <div v-if="listener" class="m-field__hint">改端口后会自动重新下发 sing-box 与 Nginx 配置，客户端需按新端口更新。</div>
    </div>
    <div class="m-field">
      <label class="m-field__label">连接域名 <em>*</em></label>
      <el-input v-model="model.connection_domain" aria-label="连接域名" placeholder="proxy.example.com" />
      <div v-if="matchedDomains.length" class="m-chips">
        <button
          v-for="item in matchedDomains"
          :key="item.value"
          type="button"
          class="m-chip domain"
          @click="model.connection_domain = item.value"
        >{{ item.value }}<small v-if="item.note">{{ item.note }}</small></button>
      </div>
      <div class="m-field__hint">
        客户端连接的域名；经 Cloudflare 的 WS/gRPC 同时作为回源 SNI。
        <template v-if="!nodeAddress">请先选择服务器再挑域名，也可直接输入。</template>
        <template v-else-if="matchedDomains.length">上面是解析到 {{ nodeAddress }} 的域名，点一下即可填入。</template>
        <template v-else>没有找到解析到 {{ nodeAddress }} 的域名，可直接输入，或先到「域名解析」添加记录。</template>
      </div>
    </div>

    <template v-if="showReality">
      <div class="m-section">连接安全</div>
      <div class="m-field">
        <label class="m-field__label">目标网站 <em>*</em></label>
        <el-input v-model="model.reality_handshake_server" aria-label="Reality 目标网站" placeholder="www.microsoft.com" />
      </div>
      <div class="m-field">
        <label class="m-field__label">网站端口</label>
        <el-input-number v-model="model.reality_handshake_port" :min="1" :max="65535" controls-position="right" aria-label="Reality 网站端口" style="width: 100%" />
      </div>
      <div class="m-notice m-notice--info">Reality 密钥和 Short ID 会在创建时自动生成，无需手动配置。</div>
    </template>

    <template v-if="model.transport_type === 'ws'">
      <div class="m-section">传输配置</div>
      <div class="m-field">
        <label class="m-field__label">请求路径</label>
        <el-input v-model="model.transport_path" readonly aria-label="WebSocket 请求路径">
          <template #append><el-button @click="model.transport_path = randomPath()">重新生成</el-button></template>
        </el-input>
        <div class="m-field__hint">由系统随机生成，避免被探测。重新生成后客户端需要同步更新。</div>
      </div>
      <div class="m-field">
        <label class="m-field__label">请求域名（Host）</label>
        <el-input v-model="model.transport_host" aria-label="WebSocket 请求域名" placeholder="可选，通常与连接域名相同" />
      </div>
    </template>

    <template v-if="model.transport_type === 'grpc'">
      <div class="m-section">传输配置</div>
      <div class="m-field">
        <label class="m-field__label">gRPC 服务名称 <em>*</em></label>
        <el-input v-model="model.transport_service_name" aria-label="gRPC 服务名称" placeholder="grpc-service" />
        <div class="m-field__hint">客户端必须填写完全相同的值。</div>
      </div>
    </template>

    <div class="m-section">用户与客户端节点</div>
    <div class="accounts">
      <!-- 一次只展开一个用户，否则三四个用户的表单会把页面拉得很长。 -->
      <div v-for="(account, index) in accounts" :key="account.id || `new-${index}`" class="account">
        <button type="button" class="account__head" @click="expanded = expanded === index ? -1 : index">
          <span>{{ account.alias || account.name || `用户 ${index + 1}` }}</span>
          <span v-if="!account.enabled" class="m-pill m-pill--info">停用</span>
          <i aria-hidden="true">{{ expanded === index ? '⌃' : '⌄' }}</i>
        </button>
        <div v-if="expanded === index" class="account__body">
          <div class="m-field">
            <label class="m-field__label">用户名称 <em>*</em></label>
            <el-input v-model="account.name" aria-label="用户名称" placeholder="用户名称" />
          </div>
          <div class="m-field">
            <label class="m-field__label">客户端节点别名 <em>*</em></label>
            <el-input v-model="account.alias" maxlength="128" aria-label="客户端节点别名" placeholder="会写入该用户的订阅配置" />
          </div>
          <div class="m-field">
            <label class="m-field__label">上网出口</label>
            <MPicker v-model="account.outbound_id" :options="outboundOptions" title="选择上网出口" />
          </div>
          <div class="m-field m-field--inline">
            <label class="m-field__label">启用该用户</label>
            <el-switch v-model="account.enabled" aria-label="启用该用户" />
          </div>
          <el-button text type="danger" :icon="Delete" :disabled="accounts.length === 1" @click="removeAccount(index)">删除该用户</el-button>
        </div>
      </div>
    </div>
    <el-button :icon="Plus" class="add-account" @click="addAccount">添加用户</el-button>

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :loading="saving" @click="save">{{ listener ? '保存' : '创建' }}</el-button>
    </template>
  </MSheet>
</template>

<style scoped>
.domain { border-style: dashed; cursor: pointer; }
.domain small { color: var(--sb-muted); font-size: 11px; }
.accounts { border: 1px solid var(--sb-line); border-radius: var(--m-radius); overflow: hidden; }
.account + .account { border-top: 1px solid var(--sb-line); }
.account__head {
  width: 100%;
  min-height: var(--m-tap);
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 11px 13px;
  color: var(--sb-text);
  background: rgba(148, 163, 184, .05);
  border: 0;
  font: inherit;
  font-size: 14.5px;
  text-align: left;
  cursor: pointer;
}
.account__head > span:first-child { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.account__head i { flex: none; color: var(--sb-muted); font-style: normal; }
.account__body { padding: 14px 13px 6px; border-top: 1px solid var(--sb-line); }
.add-account { width: 100%; height: var(--m-tap); margin-top: 10px; }
</style>
