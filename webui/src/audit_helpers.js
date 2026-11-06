// Audit helpers for operation records

export const AUDIT_MODULES = [
  { value: '', label: '全部操作' },
  { value: 'listener', label: '接入服务', tagType: 'primary' },
  { value: 'node', label: '服务器节点', tagType: 'warning' },
  { value: 'endpoint', label: '接入用户', tagType: 'success' },
  { value: 'outbound', label: '上网出口', tagType: 'danger' },
  { value: 'security', label: '安全防护', tagType: 'danger' },
  { value: 'cloudflare', label: '域名与证书', tagType: 'info' },
  { value: 'mihomo', label: '客户端订阅', tagType: 'warning' },
  { value: 'operator', label: '管理账户', tagType: 'info' },
  { value: 'system', label: '系统设置', tagType: '' },
]

export const TARGET_NAMES = {
  listener: '接入服务',
  node: '服务器',
  endpoint: '接入用户',
  outbound: '上网出口',
  route_rule: '分流规则',
  firewall_rule: '防火墙策略',
  fail2ban_jail: '自动封禁规则',
  cloudflare: '域名解析',
  cloudflare_record: 'DNS解析记录',
  origin_certificate: 'TLS证书',
  reality_key: 'Reality密钥',
  subscription: '客户端更新地址',
  mihomo_proxy_group: '代理策略组',
  mihomo_routing_profile: '客户端分流方案',
  mihomo_client_config: '客户端配置',
  mihomo_rule_provider: '规则供应商',
  operator: '管理账户',
  share: '只读分享',
  system: '系统设置',
  task: '系统操作',
}

export const TASK_KINDS = {
  'singbox.apply_config': '更新连接配置',
  'singbox.install': '安装服务核心',
  'singbox.upgrade': '升级服务核心',
  'dns.update': '刷新域名解析',
  'firewall.query': '读取防火墙',
  'firewall.mutate': '同步防火墙策略',
  'fail2ban.query': '读取封禁设置',
  'fail2ban.mutate': '修改封禁策略',
  'fail2ban.unban': '解封 IP 地址',
  'nginx.apply_config': '更新端口分流',
  'outbound.test': '检测出口连通',
  'agent.upgrade': '升级 Agent 探针',
}

export const STATUS_MAP = {
  succeeded: { label: '已完成', type: 'success' },
  failed: { label: '执行失败', type: 'danger' },
  rolled_back: { label: '已恢复原配置', type: 'warning' },
  dispatched: { label: '正在处理', type: 'warning' },
  queued: { label: '等待处理', type: 'info' },
}

export function getModuleInfo(targetType, action = '') {
  if (targetType === 'firewall_rule' || targetType === 'fail2ban_jail' || action.startsWith('firewall.') || action.startsWith('fail2ban.')) {
    return { name: '安全防护', type: 'danger' }
  }
  if (targetType?.startsWith('mihomo_') || targetType === 'subscription' || action.startsWith('mihomo.')) {
    return { name: '客户端订阅', type: 'warning' }
  }
  if (targetType === 'cloudflare' || targetType === 'cloudflare_record' || targetType === 'origin_certificate') {
    return { name: '域名与证书', type: 'info' }
  }
  if (targetType === 'listener') return { name: '接入服务', type: 'primary' }
  if (targetType === 'node') return { name: '服务器', type: 'warning' }
  if (targetType === 'endpoint') return { name: '接入用户', type: 'success' }
  if (targetType === 'outbound') return { name: '上网出口', type: 'danger' }
  if (targetType === 'route_rule') return { name: '分流规则', type: 'primary' }
  if (targetType === 'operator') return { name: '管理账户', type: 'info' }
  if (targetType === 'share') return { name: '只读分享', type: 'info' }
  return { name: TARGET_NAMES[targetType] || '系统设置', type: 'info' }
}

export function formatAuditTitle(row) {
  if (row.summary && /[\u4e00-\u9fa5]/.test(row.summary)) {
    return row.summary
  }
  const targetLabel = TARGET_NAMES[row.target_type] || '系统设置'
  const name = row.target_name || row.target_id
  if (name) {
    return `变更${targetLabel}「${name}」`
  }
  return `变更${targetLabel}`
}

export function parseJsonDetail(detailStr) {
  if (!detailStr || detailStr === '{}') return null
  try {
    const parsed = JSON.parse(detailStr)
    if (typeof parsed === 'object' && parsed !== null && Object.keys(parsed).length > 0) {
      return parsed
    }
  } catch {
    return { '详情备注': detailStr }
  }
  return null
}

export function extractParamSummary(detailStr) {
  const detail = parseJsonDetail(detailStr)
  if (!detail) return ''
  const parts = []
  if (detail.protocol) parts.push(`协议 ${detail.protocol.toUpperCase()}`)
  if (detail.port) parts.push(`端口 ${detail.port}`)
  if (detail.network) parts.push(`网络 ${detail.network}`)
  if (detail.domain) parts.push(`域名 ${detail.domain}`)
  if (detail.outbound) parts.push(`出口 ${detail.outbound}`)
  if (detail.type) parts.push(`类型 ${detail.type}`)
  if (detail.action) parts.push(`策略 ${detail.action}`)
  if (detail.ip) parts.push(`IP ${detail.ip}`)
  return parts.slice(0, 3).join(' · ')
}
