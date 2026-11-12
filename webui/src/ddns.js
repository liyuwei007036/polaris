function isIPv6(ip) {
  return String(ip || '').includes(':')
}

function isPrivateOrLocalIP(ip) {
  const s = String(ip || '').trim().toLowerCase()
  if (!s) return true
  if (s === '::1' || s.startsWith('fe80:') || s.startsWith('fc') || s.startsWith('fd')) return true
  if (s.includes(':')) return false
  const parts = s.split('.').map(Number)
  if (parts.length !== 4 || parts.some((n) => Number.isNaN(n) || n < 0 || n > 255)) return true
  const [a, b] = parts
  if (a === 10 || a === 127 || a === 0) return true
  if (a === 192 && b === 168) return true
  if (a === 172 && b >= 16 && b <= 31) return true
  if (a === 169 && b === 254) return true
  if (a === 100 && b >= 64 && b <= 127) return true
  return false
}

/**
 * Build deduplicated, human-readable IP options for node-level or per-record DDNS binding.
 * For NAT interfaces (e.g. 129.221.174.61 <- 10.0.0.121), emit ONE entry with the public IP
 * shown first while binding the underlying local NIC IP so DDNS follows public IP changes.
 */
export function buildDDNSIPOptions(node, ipAliases = {}, optionsConfig = {}) {
  const { recordType = '', selectedValues } = optionsConfig || {}
  const wantFamily = recordType === 'A' ? 'v4' : recordType === 'AAAA' ? 'v6' : ''
  const rawSelected = Array.isArray(selectedValues)
    ? selectedValues
    : [node?.ddns_bind_ip, ...Object.values(node?.ddns_record_ips || {})]
  const selectedSet = new Set(rawSelected.map((v) => String(v || '').trim()).filter(Boolean))

  const matchesFamily = (ip) => {
    if (!wantFamily) return true
    return wantFamily === 'v6' ? isIPv6(ip) : !isIPv6(ip)
  }

  const options = [{ value: '', label: '自动识别（默认出口 IP）' }]
  const seen = new Set([''])
  const mappings = node?.ip_mappings && typeof node.ip_mappings === 'object' ? node.ip_mappings : {}

  // Group NAT mappings (where pubIP != localIP): localBindIP -> [publicIP, ...]
  const localToPublic = {}
  for (const [rawPub, rawLocal] of Object.entries(mappings)) {
    const l = String(rawLocal || '').trim()
    const p = String(rawPub || '').trim()
    if (!l || !p || l === p) continue
    if (!localToPublic[l]) localToPublic[l] = []
    if (!localToPublic[l].includes(p)) localToPublic[l].push(p)
  }

  for (const [localIP, pubIPs] of Object.entries(localToPublic)) {
    const matchingPubs = pubIPs.filter(matchesFamily)
    if (wantFamily && matchingPubs.length === 0) continue
    const shownPubs = matchingPubs.length ? matchingPubs : pubIPs
    if (!seen.has(localIP)) {
      seen.add(localIP)
      const alias = ipAliases[localIP] || shownPubs.map((p) => ipAliases[p]).find(Boolean)
      const details = []
      if (alias) details.push(alias)
      details.push(`网卡 ${localIP}`)
      options.push({
        value: localIP,
        label: `${shownPubs.join(', ')}（${details.join(' · ')}）`,
      })
    }
    // Deduplicate the mapped public IP itself unless user explicitly pinned it earlier.
    for (const pubIP of pubIPs) {
      if (!selectedSet.has(pubIP)) {
        seen.add(pubIP)
      }
    }
  }

  const candidateIPs = []
  if (Array.isArray(node?.ip_addresses)) candidateIPs.push(...node.ip_addresses)
  if (ipAliases && typeof ipAliases === 'object') candidateIPs.push(...Object.keys(ipAliases))
  if (node?.last_observed_ip) candidateIPs.push(node.last_observed_ip)
  for (const val of selectedSet) candidateIPs.push(val)

  const aliasedSet = new Set(Object.keys(ipAliases || {}).map((k) => String(k || '').trim()))
  for (const raw of candidateIPs) {
    const ip = String(raw || '').trim()
    if (!ip || seen.has(ip)) continue
    if (!matchesFamily(ip)) continue
    if (isPrivateOrLocalIP(ip) && !selectedSet.has(ip) && !aliasedSet.has(ip)) continue
    seen.add(ip)

    const alias = ipAliases[ip]
    const natLocal = mappings[ip]
    const notes = []
    if (alias) notes.push(alias)
    if (natLocal && natLocal !== ip && selectedSet.has(ip)) {
      notes.push('已固定公网 IP')
    }
    options.push({
      value: ip,
      label: notes.length ? `${ip}（${notes.join(' · ')}）` : ip,
    })
  }
  return options
}

export function formatDDNSBindLabel(node) {
  if (!node) return ''
  const bindIP = String(node.ddns_bind_ip || '').trim()
  if (!bindIP) return ''
  const mappings = node.ip_mappings || {}
  const aliases = node.ip_aliases || {}
  const natMatches = Object.entries(mappings)
    .filter(([, local]) => String(local || '').trim() === bindIP)
    .map(([pub]) => pub)
  if (natMatches.length) {
    return `${natMatches.join(', ')}（网卡 ${bindIP}）`
  }
  if (aliases[bindIP]) {
    return `${bindIP}（${aliases[bindIP]}）`
  }
  return bindIP
}

export function formatDDNSBindSummary(node) {
  if (!node) return '自动识别'
  const label = formatDDNSBindLabel(node)
  if (label) return label
  const perRecCount = Object.values(node.ddns_record_ips || {}).filter(Boolean).length
  return perRecCount ? `自动识别（已单独指定 ${perRecCount} 条记录）` : '自动识别（默认出口）'
}
