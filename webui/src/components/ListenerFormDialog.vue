<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { Delete, Plus } from '@element-plus/icons-vue'
import {
  createListenerModel,
  defaultALPNFor,
  listenerProfileMap,
  listenerProfiles,
  listenerPayload,
  protocolMap,
} from '../protocols'

const props = defineProps({
  modelValue: { type: Boolean, required: true },
  listener: { type: Object, default: null },
  // template prefills the form for a new service copied from an existing one.
  // Nothing is written until the operator saves, so the target server, name
  // and users can all still be changed first.
  template: { type: Object, default: null },
  nodes: { type: Array, default: () => [] },
  listeners: { type: Array, default: () => [] },
  outbounds: { type: Array, default: () => [] },
  allEndpoints: { type: Array, default: () => [] },
  // DNS records of the configured zone, used to offer the domains that already
  // resolve to the selected server.
  dnsRecords: { type: Array, default: () => [] },
  endpoints: { type: Array, default: () => [] },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['update:modelValue', 'save'])

const formRef = ref()
const model = ref(createListenerModel(null, ''))
const accounts = ref([])
const aliasFlags = [['🇭🇰','香港'],['🇹🇼','台湾'],['🇸🇬','新加坡'],['🇯🇵','日本'],['🇰🇷','韩国'],['🇺🇸','美国'],['🇬🇧','英国'],['🇩🇪','德国'],['🇫🇷','法国'],['🇳🇱','荷兰'],['🇨🇦','加拿大'],['🇦🇺','澳大利亚'],['🇪🇪','爱沙尼亚'],['🇮🇳','印度'],['🇹🇷','土耳其']]
function addAliasFlag(account, flag) {
  const alias = (account.alias || '').trim()
  account.alias = alias.startsWith(flag) ? alias : `${flag}${alias ? ` ${alias}` : ''}`
}
const selectedProfile = computed(() => listenerProfileMap[model.value.profile])
const showReality = computed(() => model.value.security === 'reality')

const currentNode = computed(() => props.nodes.find((node) => node.id === model.value.node_id))
const nodeAddress = computed(() => currentNode.value?.client_address || '')

function getNodeEgressOptions(node) {
  if (!node) return []
  const ips = []
  const seen = new Set()

  const addIP = (ip) => {
    if (ip && typeof ip === 'string') {
      const trimmed = ip.trim()
      if (trimmed && !seen.has(trimmed) && (/^[0-9.]+$/.test(trimmed) || trimmed.includes(':'))) {
        seen.add(trimmed)
        ips.push(trimmed)
      }
    }
  }

  // 1. IP addresses detected by probe
  if (Array.isArray(node.ip_addresses)) {
    for (const ip of node.ip_addresses) addIP(ip)
  }
  // 2. IP aliases configured on this node
  if (node.ip_aliases && typeof node.ip_aliases === 'object') {
    for (const ip of Object.keys(node.ip_aliases)) addIP(ip)
  }
  // 3. IP mappings on this node
  if (node.ip_mappings && typeof node.ip_mappings === 'object') {
    for (const ip of Object.keys(node.ip_mappings)) addIP(ip)
  }
  // 4. Node client address
  addIP(node.client_address)
  // 5. Last observed IP
  addIP(node.last_observed_ip)
  const aliases = node.ip_aliases || {}
  return ips.map((ip) => {
    const alias = aliases[ip]
    const ipType = ip.includes(':') ? 'IPv6' : 'IPv4'
    const label = alias ? `${alias} (${ip})` : `${ip} (${ipType})`
    return {
      value: `direct:${ip}`,
      label,
      ip,
      alias,
      type: ipType,
    }
  })
}

const nodeEgressOptions = computed(() => getNodeEgressOptions(currentNode.value))

function egressOptionsForAccount() {
  return nodeEgressOptions.value
}

function defaultEgressValue() {
  return 'direct'
}

const configuredOutbounds = computed(() => {
  return (props.outbounds || []).filter((item) => {
    if (!item || item.id === 'direct') return false
    if (!item.expires_at) return true
    const expiry = new Date(item.expires_at).getTime()
    return Number.isNaN(expiry) || expiry >= Date.now()
  })
})

function wouldFormCycle(targetEndpointID, currentNodeID, currentListenerID) {
  const visited = new Set([targetEndpointID])
  let currentID = targetEndpointID
  for (let hop = 0; hop < 16; hop++) {
    const ep = (props.allEndpoints || []).find((e) => e.id === currentID)
    if (!ep || !ep.outbound_id || !ep.outbound_id.startsWith('chain:')) break
    const nextID = ep.outbound_id.slice(6)
    if (visited.has(nextID)) return true
    visited.add(nextID)
    const nextEp = (props.allEndpoints || []).find((e) => e.id === nextID)
    if (!nextEp) break
    const nextListener = (props.listeners || []).find((l) => l.id === nextEp.listener_id)
    if (nextListener) {
      if (currentListenerID && nextListener.id === currentListenerID) return true
      if (currentNodeID && nextListener.node_id === currentNodeID) return true
    }
    currentID = nextID
  }
  return false
}

const chainTargets = computed(() => {
  const currentNodeID = model.value.node_id
  const currentListenerID = props.listener?.id || ''
  const supported = new Set(['vless', 'hysteria2'])
  return (props.allEndpoints || []).flatMap((endpoint) => {
    if (currentListenerID && endpoint.listener_id === currentListenerID) return []
    const listener = (props.listeners || []).find((item) => item.id === endpoint.listener_id)
    if (!listener || !supported.has(listener.spec?.protocol)) return []
    if (currentNodeID && listener.node_id === currentNodeID) return []
    if (!listener.enabled || !endpoint.enabled) return []
    if (wouldFormCycle(endpoint.id, currentNodeID, currentListenerID)) return []
    const node = (props.nodes || []).find((item) => item.id === listener.node_id)
    const nodeName = node?.name || listener.node_id
    return [{
      value: `chain:${endpoint.id}`,
      nodeName,
      label: endpoint.alias || endpoint.name,
    }]
  })
})

const chainTargetGroups = computed(() => {
  const groups = new Map()
  for (const target of chainTargets.value) {
    if (!groups.has(target.nodeName)) groups.set(target.nodeName, [])
    groups.get(target.nodeName).push(target)
  }
  return Array.from(groups, ([nodeName, targets]) => ({ nodeName, targets }))
})

function isOrphanChain(outboundID) {
  if (!outboundID || !outboundID.startsWith('chain:')) return false
  return !chainTargets.value.some((target) => target.value === outboundID)
}

function orphanChainLabel(outboundID) {
  if (!outboundID || !outboundID.startsWith('chain:')) return ''
  const targetID = outboundID.slice(6)
  const targetEndpoint = (props.allEndpoints || []).find((e) => e.id === targetID)
  if (targetEndpoint) {
    const listener = (props.listeners || []).find((item) => item.id === targetEndpoint.listener_id)
    const node = (props.nodes || []).find((item) => item.id === listener?.node_id)
    const nodeName = node?.name || listener?.node_id || '其他服务器'
    const listName = listener?.name || '未知服务'
    const name = targetEndpoint.alias || targetEndpoint.name
    return `${nodeName} · ${listName} · ${name} (已停用或同服务器)`
  }
  return `已删除的节点 (${targetID.slice(0, 8)})`
}

function recordName(record) {
  return String(record.name || '').replace(/\.$/, '')
}

// A domain belongs to this server when an A/AAAA record carries the server's
// address, or a CNAME leads to such a domain. When the server is reached by a
// domain instead of an IP, that domain counts as well. Each one is kept with
// its CDN state, which decides whether traffic reaches the server directly.
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
    proxied,
    note: proxied === null ? '' : proxied ? '已开启橙云加速' : '未开启橙云加速',
  }))
})

// Only domains that resolve to the selected server are offered; any other
// domain can still be typed in.
function suggestDomains(query, callback) {
  const keyword = query.trim().toLowerCase()
  callback(keyword ? matchedDomains.value.filter((item) => item.value.toLowerCase().includes(keyword)) : matchedDomains.value)
}

const rules = computed(() => ({
  profile: [{ required: true, message: '请选择接入模式', trigger: 'change' }],
  node_id: [{ required: true, message: '请选择服务器', trigger: 'change' }],
  name: [{ required: true, message: '请输入服务名称', trigger: 'blur' }],
  connection_domain: [{ required: true, message: '请选择或输入连接域名', trigger: ['blur', 'change'] }],
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

// A guessable httpupgrade path is what makes an access service findable by
// probing, so the path is always generated rather than typed.
function randomPath() {
  return `/${randomHex(12)}`
}

function setProfile(value) {
  model.value.profile = value
  model.value.tls_alpn = defaultALPNFor(value)
  normalizeProfile()
  if (listenerProfileMap[value]?.transport === 'httpupgrade' && !model.value.transport_path) {
    model.value.transport_path = randomPath()
  }
}

watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    model.value = createListenerModel(props.listener || props.template, props.nodes[0]?.id || '')
    // Editing keeps the path its clients are already using; a new service and
    // a copy each get their own.
    if (!props.listener) model.value.transport_path = randomPath()
    // A copy reuses the source's users as new ones: their IDs are dropped so
    // saving creates fresh accounts with their own credentials.
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
    normalizeProfile()
    await nextTick()
    formRef.value?.clearValidate()
  },
)

watch(
  () => model.value.node_id,
  (newNodeID) => {
    const targetNode = props.nodes.find((n) => n.id === newNodeID)
    if (!targetNode) return
    const options = getNodeEgressOptions(targetNode)
    for (const account of accounts.value) {
      if (!account.outbound_id) {
        account.outbound_id = 'direct'
      } else if (account.outbound_id.startsWith('direct:') && !options.some((o) => o.value === account.outbound_id)) {
        account.outbound_id = 'direct'
      }
    }
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
  if (!props.listener && !props.template) {
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
    if (!names.length || names.some((name) => !name)) throw new Error('请至少保留一个已填写名称的节点')
    if (new Set(names).size !== names.length) throw new Error('同一接入服务中的节点名称不能重复')
    if (aliases.some((alias) => !alias)) throw new Error('请为每个节点填写客户端节点别名')
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
    :title="listener ? '修改接入服务' : template ? '复制接入服务' : '新建接入服务'"
    width="min(1040px, 96vw)"
    destroy-on-close
    @close="close"
  >
    <div class="listener-dialog-body">
      <p v-if="template" class="copy-note">已复制原服务配置，保存前请确认服务器、端口与域名。</p>
      <div class="form-section">
        <div class="form-section__head">接入模式</div>
        <el-select :model-value="model.profile" style="width: 100%" placeholder="选择协议与加密方式" @update:model-value="setProfile">
          <el-option v-for="profile in listenerProfiles" :key="profile.value" :label="profile.label" :value="profile.value">
            <span>{{ profile.label }}</span>
            <span class="option-meta">{{ profile.summary }}</span>
          </el-option>
        </el-select>
        <p v-if="selectedProfile" class="form-note">{{ selectedProfile.summary }}</p>
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
                <el-input-number v-model="model.port" :min="1" :max="65535" controls-position="right" style="width: 100%" />
                <div v-if="listener" class="form-hint">端口变更后需同步更新客户端。</div>
              </el-form-item>
            </el-col>
          </el-row>
          <el-row :gutter="16">
            <el-col :span="24">
              <el-form-item label="连接域名" prop="connection_domain">
                <el-autocomplete
                  v-model="model.connection_domain"
                  :fetch-suggestions="suggestDomains"
                  clearable
                  style="width: 100%"
                  placeholder="proxy.example.com">
                  <template #default="{ item }">
                    <span>{{ item.value }}</span>
                    <span :class="['domain-note', { 'domain-note--proxied': item.proxied }]">{{ item.note }}</span>
                  </template>
                </el-autocomplete>
                <div class="form-hint">
                  <template v-if="!nodeAddress">选择服务器后可选域名。</template>
                  <template v-else-if="matchedDomains.length">可选解析到该服务器的域名，也可手动输入。</template>
                  <template v-else>没有解析到该服务器的域名，可直接输入。</template>
                </div>
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div v-if="showReality" class="form-section">
          <div class="form-section__head">Reality</div>
          <el-row :gutter="16">
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
        </div>

        <div v-if="['httpupgrade', 'grpc'].includes(model.transport_type)" class="form-section">
          <div class="form-section__head">传输配置</div>
          <el-row :gutter="16">
            <el-col v-if="model.transport_type === 'httpupgrade'" :span="14">
              <el-form-item label="请求路径">
                <el-input v-model="model.transport_path" readonly>
                  <template #append>
                    <el-button @click="model.transport_path = randomPath()">重新生成</el-button>
                  </template>
                </el-input>
                <div class="form-hint">系统随机生成；重新生成后需同步更新客户端。</div>
              </el-form-item>
            </el-col>
            <el-col v-if="model.transport_type === 'grpc'" :span="14">
              <el-form-item label="gRPC 服务名称">
                <el-input v-model="model.transport_service_name" placeholder="grpc-service" />
                <div class="form-hint">客户端需填写相同名称。</div>
              </el-form-item>
            </el-col>
          </el-row>
        </div>

        <div class="form-section">
          <div class="account-section-head">
            <div class="form-section__head">节点</div>
            <el-button :icon="Plus" @click="addAccount">添加节点</el-button>
          </div>
          <div class="account-head-row">
            <span />
            <span>节点名称</span>
            <span>客户端别名</span>
            <span>出口</span>
            <span>状态</span>
            <span />
          </div>
          <div class="account-list">
            <div v-for="(account, index) in accounts" :key="account.id || `new-${index}`" class="account-row">
              <span class="account-index">{{ index + 1 }}</span>
              <el-input v-model="account.name" aria-label="节点名称" placeholder="节点名称" />
              <div class="alias-input">
                <el-input v-model="account.alias" aria-label="客户端节点别名" maxlength="128" placeholder="节点别名" />
                <el-popover trigger="click" placement="bottom-end" :width="244">
                  <template #reference><el-button class="alias-flag-trigger" aria-label="选择国旗" title="选择国旗">🏳️</el-button></template>
                  <div class="alias-flag-grid">
                    <el-tooltip v-for="[flag, country] in aliasFlags" :key="flag" :content="country">
                      <button type="button" class="alias-flag-option" :aria-label="country" @click="addAliasFlag(account, flag)">{{ flag }}</button>
                    </el-tooltip>
                  </div>
                </el-popover>
              </div>
              <el-select
                v-model="account.outbound_id"
                filterable
                style="width: 100%"
                placeholder="选择出口"
              >
                <el-option-group v-if="nodeEgressOptions.length" label="出口 IP">
                  <el-option
                    v-for="opt in egressOptionsForAccount(account)"
                    :key="opt.value"
                    :label="opt.label"
                    :value="opt.value"
                  />
                </el-option-group>
                <el-option-group v-if="configuredOutbounds.length" label="上网出口（配置的出口）">
                  <el-option
                    v-for="outbound in configuredOutbounds"
                    :key="outbound.id"
                    :label="outbound.name + (outbound.type ? ` (${outbound.type.toUpperCase()})` : '') + (!outbound.enabled ? ' [已停用]' : '')"
                    :value="outbound.id"
                  >
                    {{ outbound.name }}{{ outbound.type ? ` (${outbound.type.toUpperCase()})` : '' }}{{ !outbound.enabled ? ' [已停用]' : '' }}
                  </el-option>
                </el-option-group>
                <el-option-group
                  v-for="group in chainTargetGroups"
                  :key="group.nodeName"
                  :label="group.nodeName"
                >
                  <el-option v-for="target in group.targets" :key="target.value" :label="target.label" :value="target.value" />
                </el-option-group>
                <el-option-group v-if="isOrphanChain(account.outbound_id)" label="已失效的链式代理">
                  <el-option :label="orphanChainLabel(account.outbound_id)" :value="account.outbound_id" />
                </el-option-group>
              </el-select>
              <el-switch v-model="account.enabled" inline-prompt active-text="启用" inactive-text="停用" />
              <el-button text type="danger" :icon="Delete" :disabled="accounts.length === 1" aria-label="删除节点" @click="removeAccount(index)" />
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
.copy-note { margin: 0 0 12px; padding: 8px 12px; border-left: 2px solid var(--sb-accent); border-radius: 6px; background: var(--sb-surface-2); color: var(--sb-text-2); font-size: 12px; }
.form-note { margin: 8px 0 0; color: var(--sb-muted); font-size: 12px; }
.option-meta { float: right; max-width: 520px; margin-left: 20px; overflow: hidden; color: var(--sb-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.listener-dialog-body { max-width: 100%; max-height: min(72vh, 760px); box-sizing: border-box; overflow-x: hidden; overflow-y: auto; scrollbar-width: none; }
.listener-dialog-body::-webkit-scrollbar { display: none; }
.account-section-head { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 8px; }
.account-section-head .form-section__head { flex: 1; margin-bottom: 0; }
.account-head-row { display: grid; grid-template-columns: 32px minmax(140px, 1fr) minmax(160px, 1fr) minmax(190px, 1.2fr) 62px 38px; gap: 10px; padding: 10px 0 6px; color: var(--sb-muted); font-size: 11px; }
.account-list { border-top: 1px solid var(--sb-border); }
.account-row { display: grid; grid-template-columns: 32px minmax(140px, 1fr) minmax(160px, 1fr) minmax(190px, 1.2fr) 62px 38px; gap: 10px; align-items: center; padding: 8px 0; border-bottom: 1px solid var(--sb-border); }
.alias-input { display: flex; gap: 6px; min-width: 0; }
.alias-input :deep(.el-input) { flex: 1; min-width: 0; }
.alias-flag-trigger { padding: 0 9px; }
.alias-flag-grid { display: grid; grid-template-columns: repeat(5, 1fr); gap: 6px; }
.alias-flag-option { height: 36px; border: 1px solid var(--sb-border); border-radius: 6px; background: var(--sb-bg); font-size: 18px; cursor: pointer; }
.alias-flag-option:hover { border-color: var(--el-color-primary); }
.account-index { color: var(--sb-muted); text-align: center; font-variant-numeric: tabular-nums; }
.form-hint { margin-top: 6px; color: var(--sb-muted); font-size: 12px; }
.domain-note { float: right; margin-left: 20px; color: var(--sb-muted); font-size: 12px; }
.domain-note--proxied { color: var(--el-color-warning); }
.dialog-footer { display: flex; justify-content: flex-end; gap: 8px; }
@media (max-width: 680px) {
  .listener-dialog-body :deep(.el-col) { max-width: 100%; flex: 0 0 100%; }
  .account-head-row { display: none; }
  .account-row { grid-template-columns: 28px 1fr 62px 36px; }
  .account-row :deep(.el-input), .account-row :deep(.el-select) { grid-column: 2 / -1; }
}
</style>
