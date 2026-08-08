export const securityOptions = {
  none: { label: '不加密（仅限可信网络）', value: 'none' },
  tls: { label: 'TLS 加密（自动证书）', value: 'tls' },
  reality: { label: 'Reality 免证书加密', value: 'reality' },
  auto: { label: '自动生成加密', value: 'auto' },
}

export const listenerProfiles = [
  { value: 'vless-reality', label: 'VLESS + Reality', summary: '免证书加密连接', protocol: 'vless', security: 'reality', transport: '' },
  { value: 'vless-ws', label: 'VLESS + WebSocket', summary: 'TLS 回源，适合通过 Cloudflare 转发', protocol: 'vless', security: 'tls', transport: 'ws' },
  { value: 'vless-grpc', label: 'VLESS + gRPC', summary: 'TLS 回源，使用 gRPC 传输', protocol: 'vless', security: 'tls', transport: 'grpc' },
  { value: 'hysteria2', label: 'Hysteria2', summary: '自动生成加密配置', protocol: 'hysteria2', security: 'auto', transport: '' },
]

export const listenerProfileMap = Object.fromEntries(listenerProfiles.map((profile) => [profile.value, profile]))

// Hysteria2 runs over QUIC, whose ALPN is h3. The VLESS transports run over
// TCP, where h3 cannot be negotiated at all: WebSocket upgrades over HTTP/1.1
// and gRPC needs HTTP/2.
export function defaultALPNFor(profileValue) {
  const profile = listenerProfileMap[profileValue]
  if (!profile) return []
  if (profile.protocol === 'hysteria2') return ['h3']
  if (profile.transport === 'grpc') return ['h2']
  if (profile.transport === 'ws') return ['http/1.1']
  return []
}

export const protocols = [
  {
    value: 'vless',
    label: 'VLESS',
    summary: '支持 Reality、WebSocket 和 gRPC',
    network: 'tcp',
    security: ['reality', 'tls', 'none'],
    transports: true,
    recommended: true,
    defaultPort: 443,
  },
  {
    value: 'hysteria2',
    label: 'Hysteria2',
    summary: '基于 QUIC 的 UDP 入站',
    network: 'udp',
    security: ['auto'],
    transports: false,
    defaultPort: 443,
  },
]

export const protocolMap = Object.fromEntries(protocols.map((protocol) => [protocol.value, protocol]))

function profileValue(spec) {
  if (spec.protocol === 'hysteria2') return 'hysteria2'
  if (spec.protocol !== 'vless') return ''
  if (spec.reality?.enabled) return 'vless-reality'
  if (spec.transport?.type === 'ws') return 'vless-ws'
  if (spec.transport?.type === 'grpc') return 'vless-grpc'
  return ''
}

export function createListenerModel(existing, nodeID = '') {
  const spec = existing?.spec || {}
  const security = spec.reality?.enabled ? 'reality' : spec.tls?.enabled ? 'tls' : protocolMap[spec.protocol]?.security?.[0] || 'none'
  return {
    id: existing?.id || '',
    node_id: existing?.node_id || nodeID,
    name: existing?.name || '',
    connection_domain: existing?.connection_domain || '',
    listen_address: existing?.listen_address || '0.0.0.0',
    port: existing?.port || 443,
    original_port: existing?.port || 0,
    backend_port: existing?.backend_port || 0,
    enabled: existing?.enabled ?? true,
    outbound_id: existing?.outbound_id || '',
    profile: existing ? profileValue(spec) : 'vless-reality',
    protocol: spec.protocol || 'vless',
    network: spec.network || 'tcp',
    security,
    tls_alpn: spec.tls?.alpn || [],
    tls_min_version: spec.tls?.min_version || '',
    tls_max_version: spec.tls?.max_version || '',
    reality_handshake_server: spec.reality?.handshake_server || 'www.microsoft.com',
    reality_handshake_port: spec.reality?.handshake_port || 443,
    reality_short_ids: spec.reality?.short_ids || [],
    reality_key_id: spec.reality?.key_id || '',
    transport_type: spec.transport?.type || '',
    transport_path: spec.transport?.path || '',
    transport_host: spec.transport?.host || '',
    transport_service_name: spec.transport?.service_name || '',
  }
}

export function listenerPayload(model) {
  const profile = listenerProfileMap[model.profile]
  const definition = protocolMap[profile.protocol]
  const security = profile.security
  const defaultALPN = defaultALPNFor(model.profile)
  const automaticallyManaged = ['127.0.0.1', '::1'].includes(model.listen_address) && model.backend_port && model.backend_port !== model.original_port
  return {
    node_id: model.node_id,
    name: model.name,
    connection_domain: model.connection_domain.trim(),
    listen_address: model.listen_address || '0.0.0.0',
    port: Number(model.port),
    backend_port: automaticallyManaged ? Number(model.backend_port) : Number(model.port),
    enabled: Boolean(model.enabled),
    outbound_id: model.outbound_id || '',
    spec: {
      protocol: profile.protocol,
      network: definition.network,
      tls: {
        enabled: security === 'auto' || security === 'tls' || security === 'reality',
        alpn: model.tls_alpn?.length ? model.tls_alpn : defaultALPN,
        min_version: model.tls_min_version || '',
        max_version: model.tls_max_version || '',
        cipher_suites: [],
      },
      reality: {
        enabled: security === 'reality',
        handshake_server: security === 'reality' ? model.reality_handshake_server : '',
        handshake_port: security === 'reality' ? Number(model.reality_handshake_port || 443) : 0,
        short_ids: security === 'reality' ? model.reality_short_ids || [] : [],
        key_id: security === 'reality' ? model.reality_key_id : '',
      },
      transport: {
        type: profile.transport,
        path: profile.transport === 'ws' ? model.transport_path || '' : '',
        host: profile.transport === 'ws' ? model.transport_host || '' : '',
        service_name: profile.transport === 'grpc' ? model.transport_service_name || '' : '',
      },
    },
  }
}
