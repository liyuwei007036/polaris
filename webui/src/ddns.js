/**
 * Build selectable IP options for node-level or per-record DDNS binding.
 * Includes detected public IPs, custom alias IPs, and NAT local bind IPs
 * (from ip_mappings) so operators on multi-IP / NAT / dual-stack hosts can
 * bind DDNS either to a static public IP or to a specific NIC's local IP.
 */
export function buildDDNSIPOptions(node, ipAliases = {}) {
  const options = [{ value: '', label: '自动识别（默认出口 IP）' }]
  const seen = new Set([''])
  const mappings = node?.ip_mappings && typeof node.ip_mappings === 'object' ? node.ip_mappings : {}

  // Build reverse index: localBindIP -> [publicIP, ...]
  const localToPublic = {}
  for (const [pubIP, localIP] of Object.entries(mappings)) {
    const l = String(localIP || '').trim()
    const p = String(pubIP || '').trim()
    if (!l || !p) continue
    if (!localToPublic[l]) localToPublic[l] = []
    if (!localToPublic[l].includes(p)) localToPublic[l].push(p)
  }

  for (const [localIP, pubIPs] of Object.entries(localToPublic)) {
    if (seen.has(localIP)) continue
    seen.add(localIP)
    const alias = ipAliases[localIP] || pubIPs.map((p) => ipAliases[p]).find(Boolean)
    const suffix = alias ? ` · ${alias}` : ''
    options.push({
      value: localIP,
      label: `${localIP}（本地网卡 NAT 追踪 → 当前 ${pubIPs.join(', ')}${suffix}）`,
    })
  }

  const candidateIPs = []
  if (Array.isArray(node?.ip_addresses)) candidateIPs.push(...node.ip_addresses)
  if (ipAliases && typeof ipAliases === 'object') candidateIPs.push(...Object.keys(ipAliases))
  if (node?.last_observed_ip) candidateIPs.push(node.last_observed_ip)
  if (node?.ddns_bind_ip) candidateIPs.push(node.ddns_bind_ip)
  if (node?.ddns_record_ips && typeof node.ddns_record_ips === 'object') {
    candidateIPs.push(...Object.values(node.ddns_record_ips))
  }

  for (const raw of candidateIPs) {
    const ip = String(raw || '').trim()
    if (!ip || seen.has(ip)) continue
    seen.add(ip)
    const alias = ipAliases[ip]
    const natLocal = mappings[ip]
    const notes = []
    if (alias) notes.push(alias)
    if (natLocal && natLocal !== ip) notes.push(`NAT 本地 ${natLocal}`)
    options.push({
      value: ip,
      label: notes.length ? `${ip}（${notes.join(' · ')}）` : ip,
    })
  }
  return options
}

export function formatDDNSBindSummary(node) {
  if (!node) return '自动识别'
  const bindIP = String(node.ddns_bind_ip || '').trim()
  const mappings = node.ip_mappings || {}
  const aliases = node.ip_aliases || {}
  if (!bindIP) {
    const perRecCount = Object.values(node.ddns_record_ips || {}).filter(Boolean).length
    return perRecCount ? `自动识别（已独立指定 ${perRecCount} 条记录）` : '自动识别（跟随主出口）'
  }
  const natMatches = Object.entries(mappings)
    .filter(([, local]) => String(local || '').trim() === bindIP)
    .map(([pub]) => pub)
  if (natMatches.length) {
    return `${bindIP}（NAT → ${natMatches.join(', ')}）`
  }
  if (aliases[bindIP]) {
    return `${bindIP}（${aliases[bindIP]}）`
  }
  return bindIP
}
