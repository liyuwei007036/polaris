import { shallowRef } from 'vue'
import { isShareMode, shareStreamPath } from './share'

// One shared connections stream for the whole console. Every page that shows
// real-time data reads the same snapshot map instead of opening its own
// EventSource, which previously meant several streams — and several full
// re-renders per push — whenever more than one such page had been visited.
export const connectionSnapshots = shallowRef(new Map())

// The fleet total the master sums once per reporting round. Every node pushes
// on the same wall-clock grid, so only the master ever sees a complete round —
// a browser adding up whatever had arrived by the time it redrew was folding
// the same node reading into several points in a row. Charts read this series
// instead of doing arithmetic of their own.
export const fleetTotals = shallowRef(null)

let source
let subscribers = new Set()
let totalsSubscribers = new Set()

function notify() {
  // 不能只 triggerRef：shared Map 原地修改后，中间 computed 读到的
  // 还是同一个对象，hasChanged 判定“值没变”，下游(卡片计算属性)
  // 永远不会重算。必须换成新 Map 实例，让响应式链条真正失效重算。
  connectionSnapshots.value = new Map(connectionSnapshots.value)
  subscribers.forEach((subscriber) => subscriber())
}

function applyNode(item) {
  if (!item?.node_id) return
  const prev = connectionSnapshots.value.get(item.node_id) || {}
  connectionSnapshots.value.set(item.node_id, { ...prev, ...item })
}

function ensureSource() {
  if (source) return
  // The public share stream carries the same snapshot / node / totals events
  // as the authenticated one, with per-node connection details stripped.
  source = new EventSource(
    isShareMode ? shareStreamPath('/events/connections') : '/api/v1/events/connections',
    { withCredentials: !isShareMode },
  )
  source.addEventListener('snapshot', (event) => {
    let payload
    try { payload = JSON.parse(event.data) } catch { return }
    connectionSnapshots.value.clear()
    for (const item of payload.nodes || []) applyNode(item)
    notify()
  })
  source.addEventListener('node', (event) => {
    let item
    try { item = JSON.parse(event.data) } catch { return }
    applyNode(item)
    notify()
  })
  source.addEventListener('totals', (event) => {
    let payload
    try { payload = JSON.parse(event.data) } catch { return }
    fleetTotals.value = payload
    totalsSubscribers.forEach((subscriber) => subscriber(payload))
  })
}

// subscribeConnections registers a callback fired whenever a snapshot lands.
// The stream stays open while at least one page is subscribed.
export function subscribeConnections(subscriber) {
  subscribers.add(subscriber)
  ensureSource()
  return () => {
    subscribers.delete(subscriber)
    closeWhenIdle()
  }
}

// subscribeFleetTotals registers a callback fired once per reporting round,
// when the master publishes the summed figure. One call means one beat, so a
// chart can append exactly one point per call.
export function subscribeFleetTotals(subscriber) {
  totalsSubscribers.add(subscriber)
  ensureSource()
  return () => {
    totalsSubscribers.delete(subscriber)
    closeWhenIdle()
  }
}

function closeWhenIdle() {
  if (subscribers.size === 0 && totalsSubscribers.size === 0) closeConnectionEvents()
}

const stickyChainPairs = new Map()
const MAX_CHAIN_HOPS = 16
const STICKY_PAIR_BONUS = 200
const STICKY_DIVERGE_MIN_BYTES = 64 * 1024
const STICKY_DIVERGE_RATIO_THRESH = 0.05

export function resetStickyChainPairs() {
  stickyChainPairs.clear()
}

export function closeConnectionEvents() {
  source?.close()
  source = undefined
  fleetTotals.value = null
  connectionSnapshots.value = new Map()
  stickyChainPairs.clear()
}

function ratioSimilarity(a, b) {
  if (!(a > 0) || !(b > 0)) return 0
  return a > b ? b / a : a / b
}

function parseNormalizedStartMs(conn) {
  if (!conn?.started_at) return NaN
  const ms = Date.parse(conn.started_at)
  if (!Number.isFinite(ms)) return NaN
  const offsetMs = Number(conn.clock_offset_ms) || 0
  return ms - offsetMs
}

function chainTargetKey(c) {
  if (!c) return ''
  if (c.chained_target) return String(c.chained_target).trim()
  if (c.outbound && String(c.outbound).startsWith('chain-')) {
    return String(c.outbound).slice(6).trim()
  }
  if (Array.isArray(c.chains)) {
    for (const tag of c.chains) {
      if (tag && String(tag).startsWith('chain-')) {
        return String(tag).slice(6).trim()
      }
    }
  }
  return ''
}

function needsLandingAttributionFallback(exitLabel, targetKey) {
  const label = String(exitLabel || '').trim()
  if (!label) return true
  const lower = label.toLowerCase()
  if (lower.includes('落地）') || lower.includes('落地)')) return false
  if (lower === 'direct' || lower === '直连' || lower.startsWith('direct-') || lower.startsWith('direct:') || lower.startsWith('直连 (') || lower.startsWith('直连（')) {
    return true
  }
  if (lower === '链式代理' || lower.startsWith('chain-')) return true
  if (targetKey && ((label.includes(' · ') && /\([^()]+\)\s*$/.test(label)) || lower === targetKey.toLowerCase())) return true
  return false
}

// mergeChainedConnections combines the hops of a chained proxy into a single
// logical connection using multi-dimensional scoring (evidence, clock-aligned
// start time, transferred bytes, live rates), cross-snapshot sticky locking,
// global 1-to-1 hop matching, and consistent landing exit fallback.
export function mergeChainedConnections(connections, nodes = [], options = {}) {
  const stickyMap = options?.stickyMap || stickyChainPairs
  if (!connections || connections.length === 0) {
    stickyMap.clear()
    return []
  }

  const nodeMap = new Map((nodes || []).map((n) => [n.id, n]))
  const consumed = new Set()
  const seenStickyKeys = new Set()

  const isChainEntry = (c) => Boolean(
    c.is_chain_entry ||
    c.chained_target ||
    (c.outbound && c.outbound.startsWith('chain-')) ||
    (Array.isArray(c.chains) && c.chains.some((tag) => tag && tag.startsWith('chain-')))
  )

  // 取“IP[:端口]”里的 IP。[v6]:端口 去括号；IPv4:端口 去端口；IPv6 本身
  // 含多个冒号，必须整体保留，不能用取第一个冒号前段的方式切。
  const bare = (addr) => {
    if (!addr) return ''
    let text = addr.trim().toLowerCase()
    if (text.startsWith('[')) return (text.match(/^\[(.+?)]/) || [])[1] || text
    const separator = text.lastIndexOf(':')
    if (separator >= 0 && /^\d+$/.test(text.slice(separator + 1))) text = text.slice(0, separator)
    return text
  }

  const splitDest = (addr) => {
    if (!addr) return { host: '', port: '' }
    const text = addr.trim().toLowerCase()
    if (text.startsWith('[')) {
      const m = text.match(/^\[([^\]]+)\](?::(\d+))?$/)
      return { host: m?.[1] || '', port: m?.[2] || '' }
    }
    const separator = text.lastIndexOf(':')
    if (separator >= 0 && /^\d+$/.test(text.slice(separator + 1))) {
      return { host: text.slice(0, separator), port: text.slice(separator + 1) }
    }
    return { host: text, port: '' }
  }

  const destMatches = (a, b) => {
    const netA = (a.network || '').toLowerCase()
    const netB = (b.network || '').toLowerCase()
    if (netA && netB && netA !== netB) return false

    const da = splitDest(a.destination)
    const db = splitDest(b.destination)
    if (da.port && db.port && da.port !== db.port) return false

    const ha = (a.host || '').trim().toLowerCase()
    const hb = (b.host || '').trim().toLowerCase()
    if (ha && hb) return ha === hb

    if (da.host && db.host && da.host === db.host && (da.port === db.port || !da.port || !db.port)) {
      return true
    }
    if (ha && db.host && ha === db.host) return true
    if (hb && da.host && hb === da.host) return true
    return Boolean(a.target && b.target && a.target === b.target && !String(a.target).startsWith(':'))
  }

  const isDirectExit = (c) => {
    if (!c) return false
    if (c.outbound_direct) return true
    const rawOut = String(c.outbound || '').trim().toLowerCase()
    if (rawOut === 'direct' || rawOut.startsWith('direct-') || rawOut.startsWith('direct:')) return true
    const label = String(c.exit || c.outbound_name || '').trim().toLowerCase()
    return label === 'direct' || label === '直连' || label.startsWith('direct (') || label.startsWith('direct:') || label.startsWith('直连 (') || label.startsWith('直连（')
  }

  const computeEvidence = (currentLeg, candidate) => {
    let evidence = candidate.is_chain_relay
      ? (candidate.relay_upstream_node_id
          ? (candidate.relay_upstream_node_id === currentLeg.node_id ? 3 : 0)
          : 1)
      : 0

    const sourceNode = nodeMap.get(currentLeg.node_id)
    if (sourceNode) {
      const candIP = candidate.source_ip ? candidate.source_ip.trim().toLowerCase() : ''
      const nodeKnownIPs = [
        sourceNode.client_address,
        sourceNode.last_observed_ip,
        ...(Array.isArray(sourceNode.ip_addresses) ? sourceNode.ip_addresses : []),
        ...Object.keys(sourceNode.ip_mappings || {}),
      ].map(bare).filter(Boolean)
      const sourceIPMatch = Boolean(candIP && nodeKnownIPs.includes(candIP))
      const sourceAliasMatch = Boolean(candidate.source &&
        sourceNode.client_address && candidate.source.includes(sourceNode.client_address))
      const label = currentLeg.outbound_name || currentLeg.exit || ''
      const labelAccount = (label.match(/\(([^()]+)\)\s*$/) || [])[1]
      const userMatch = Boolean(labelAccount && candidate.user && labelAccount === candidate.user)
      if (evidence < 2 && (sourceIPMatch || sourceAliasMatch || userMatch)) {
        // Explicit conflicting relay_upstream_node_id already returned evidence 0 above only for is_chain_relay;
        // do not promote a relay leg known to come from another upstream server.
        if (!(candidate.is_chain_relay && candidate.relay_upstream_node_id && candidate.relay_upstream_node_id !== currentLeg.node_id)) {
          evidence = 2
        }
      }
    }
    return evidence
  }

  const scoreCandidate = (leg, candidate, evidence, isSticky) => {
    let score = evidence * 1000
    let absTimeDeltaMs = Number.MAX_VALUE

    const ha = (leg.host || '').trim().toLowerCase()
    const hb = (candidate.host || '').trim().toLowerCase()
    if (ha && hb && ha === hb) score += 20
    const da = splitDest(leg.destination)
    const db = splitDest(candidate.destination)
    if (da.port && db.port && da.port === db.port) score += 10

    const legStartMs = parseNormalizedStartMs(leg)
    const candStartMs = parseNormalizedStartMs(candidate)
    if (Number.isFinite(legStartMs) && Number.isFinite(candStartMs)) {
      const diffSec = (candStartMs - legStartMs) / 1000
      absTimeDeltaMs = Math.abs(candStartMs - legStartMs)
      if (diffSec >= 0 && diffSec <= 2) {
        score += 120 - Math.min(diffSec * 2, 4)
      } else if (diffSec >= -2 && diffSec < 0) {
        score += 90 - Math.min((-diffSec) * 5, 10)
      } else if (diffSec > 2 && diffSec <= 10) {
        score += 50 - Math.min((diffSec - 2) * 2, 16)
      } else if (diffSec > 10 && diffSec <= 60) {
        score += 10 - Math.min((diffSec - 10) * 0.2, 10)
      } else if (diffSec >= -10 && diffSec < -2) {
        score -= 20 + Math.min((-diffSec - 2) * 2.5, 20)
      } else {
        score -= 150 + Math.min(Math.abs(diffSec) * 0.1, 100)
      }
    }

    const legUp = Number(leg.upload) || 0
    const legDown = Number(leg.download) || 0
    const candUp = Number(candidate.upload) || 0
    const candDown = Number(candidate.download) || 0
    const upTot = legUp + legDown
    const downTot = candUp + candDown
    const maxTot = Math.max(upTot, downTot)
    let totRatio = 1
    if (maxTot >= 2048) {
      totRatio = ratioSimilarity(upTot, downTot)
      if (totRatio >= 0.85) score += 100
      else if (totRatio >= 0.60) score += 60
      else if (totRatio >= 0.30) score += 15
      else if (totRatio < 0.10) score -= 140
      else score -= 40

      if (Math.max(legUp, candUp) >= 2048) {
        const ur = ratioSimilarity(legUp, candUp)
        if (ur >= 0.70) score += 25
        else if (ur < 0.10) score -= 20
      }
      if (Math.max(legDown, candDown) >= 2048) {
        const dr = ratioSimilarity(legDown, candDown)
        if (dr >= 0.70) score += 25
        else if (dr < 0.10) score -= 20
      }
    }

    if (leg.has_rates && candidate.has_rates) {
      const upRate = (Number(leg.upload_rate) || 0) + (Number(leg.download_rate) || 0)
      const downRate = (Number(candidate.upload_rate) || 0) + (Number(candidate.download_rate) || 0)
      const maxRate = Math.max(upRate, downRate)
      if (maxRate >= 512) {
        const rr = ratioSimilarity(upRate, downRate)
        if (rr >= 0.70) score += 40
        else if (rr >= 0.40) score += 15
        else if (rr < 0.10) score -= 40
      }
    }

    if (isSticky && !(maxTot >= STICKY_DIVERGE_MIN_BYTES && totRatio < STICKY_DIVERGE_RATIO_THRESH)) {
      score += STICKY_PAIR_BONUS
    }
    return { score, absTimeDeltaMs }
  }

  const applyFallbackOnMissingHop = (entry, leg, initialChainNodeIDs) => {
    if (entry.chain_path && (!entry.chain_node_names ||
        entry.chain_path.split(' → ').length > entry.chain_node_names.length)) {
      entry.node_name = entry.chain_path
      if (initialChainNodeIDs && initialChainNodeIDs.length > (entry.chain_node_ids?.length || 0)) {
        entry.chain_node_ids = initialChainNodeIDs
      }
    }
    const resolvedName = leg?.resolved_outbound_name || entry.resolved_outbound_name
    if (resolvedName) {
      const prevExit = entry.chain_exit || entry.exit
      if (prevExit && !String(prevExit).endsWith(resolvedName)) {
        entry.chain_exit = `${prevExit} → ${resolvedName}`
      }
      entry.exit = resolvedName
    }
  }

  // Start frontier from top-level chain entries first so intermediate relay hops
  // (which are themselves is_chain_relay && isChainEntry) are claimed in hop order
  // rather than stealing downstream legs out of array order.
  let frontier = connections
    .filter((c) => isChainEntry(c) && !c.is_chain_relay)
    .map((entry) => ({
      root: entry,
      leg: entry,
      initialChainNodeIDs: Array.isArray(entry.chain_node_ids) ? [...entry.chain_node_ids] : null,
      liveChainMatched: false,
      visitedNodes: new Set(entry.node_id ? [entry.node_id] : []),
    }))

  const unresolvedForConsensus = []

  for (let hop = 0; hop < MAX_CHAIN_HOPS && frontier.length > 0; hop++) {
    const edges = []
    for (let fIdx = 0; fIdx < frontier.length; fIdx++) {
      const item = frontier[fIdx]
      const { root, leg } = item
      const stickyKey = (leg.node_id && leg.id) ? `${leg.node_id}\0${leg.id}\0${hop}` : ''
      const stickyTarget = stickyKey ? stickyMap.get(stickyKey) : ''

      for (let cIdx = 0; cIdx < connections.length; cIdx++) {
        const cand = connections[cIdx]
        if (cand === root || cand === leg || consumed.has(cand)) continue
        if (cand.node_id === leg.node_id || (cand.node_id && item.visitedNodes.has(cand.node_id))) continue
        if (!destMatches(leg, cand)) continue

        const evidence = computeEvidence(leg, cand)
        if (evidence <= 0) continue

        const candStickyId = (cand.node_id && cand.id) ? `${cand.node_id}\0${cand.id}` : ''
        const isSticky = Boolean(stickyTarget && candStickyId && stickyTarget === candStickyId)
        const { score, absTimeDeltaMs } = scoreCandidate(leg, cand, evidence, isSticky)
        edges.push({
          fIdx,
          cIdx,
          score,
          absTimeDeltaMs,
          stickyKey,
          candStickyId,
          tieA: String(leg.id || fIdx),
          tieB: String(cand.id || cIdx),
        })
      }
    }

    edges.sort((a, b) => {
      if (a.score !== b.score) return b.score - a.score
      if (a.absTimeDeltaMs !== b.absTimeDeltaMs) return a.absTimeDeltaMs - b.absTimeDeltaMs
      if (a.tieA !== b.tieA) return a.tieA < b.tieA ? -1 : 1
      return a.tieB < b.tieB ? -1 : (a.tieB > b.tieB ? 1 : 0)
    })

    const matchedFrontier = new Set()
    const nextFrontier = []

    for (const edge of edges) {
      if (matchedFrontier.has(edge.fIdx)) continue
      const best = connections[edge.cIdx]
      if (consumed.has(best)) continue

      matchedFrontier.add(edge.fIdx)
      consumed.add(best)

      if (edge.stickyKey && edge.candStickyId) {
        stickyMap.set(edge.stickyKey, edge.candStickyId)
        seenStickyKeys.add(edge.stickyKey)
      }

      const item = frontier[edge.fIdx]
      const entry = item.root

      if (!item.liveChainMatched) {
        item.liveChainMatched = true
        entry.chain_node_ids = [entry.node_id]
      }
      if (!entry.chain_node_ids.includes(best.node_id)) {
        entry.chain_node_ids.push(best.node_id)
      }
      if (best.node_id) item.visitedNodes.add(best.node_id)

      if (!entry.chain_node_names) entry.chain_node_names = [entry.node_name]
      entry.chain_node_names.push(best.node_name)
      entry.node_name = entry.chain_node_names.join(' → ')

      if (best.exit) {
        let exitLabel = best.exit
        if (
          !isChainEntry(best) &&
          isDirectExit(best) &&
          best.node_id &&
          best.node_id !== entry.node_id &&
          best.node_name &&
          !String(exitLabel).includes('落地）')
        ) {
          exitLabel = `${exitLabel}（经 ${best.node_name} 落地）`
        }
        entry.chain_exit = entry.exit ? `${entry.exit} → ${exitLabel}` : exitLabel
        entry.exit = exitLabel
      }

      if (best.has_rates && !entry.has_rates) {
        entry.has_rates = true
        entry.download_rate = best.download_rate
        entry.upload_rate = best.upload_rate
      }

      if (isChainEntry(best)) {
        nextFrontier.push({ ...item, leg: best })
      }
    }

    for (let fIdx = 0; fIdx < frontier.length; fIdx++) {
      if (matchedFrontier.has(fIdx)) continue
      const item = frontier[fIdx]
      applyFallbackOnMissingHop(item.root, item.leg, item.initialChainNodeIDs)
      unresolvedForConsensus.push(item.root)
    }

    frontier = nextFrontier
  }

  for (const item of frontier) {
    applyFallbackOnMissingHop(item.root, item.leg, item.initialChainNodeIDs)
    unresolvedForConsensus.push(item.root)
  }

  // Prune sticky pairings for connections no longer active in this snapshot.
  for (const key of stickyMap.keys()) {
    if (!seenStickyKeys.has(key)) stickyMap.delete(key)
  }

  // Plan C: Keep exit labels consistent across sibling connections entering the
  // exact same chained target so temporary timing skew on one leg never regresses
  // its display back to unattributed "直连" or raw chain tag.
  const consensusExitByTarget = new Map()
  for (const c of connections) {
    if (!isChainEntry(c) || c.is_chain_relay) continue
    const tKey = chainTargetKey(c)
    if (!tKey) continue
    const candidateExit = String(c.exit || c.resolved_outbound_name || '').trim()
    if (!needsLandingAttributionFallback(candidateExit, tKey)) {
      if (!consensusExitByTarget.has(tKey) || candidateExit.includes('落地）')) {
        consensusExitByTarget.set(tKey, {
          exit: candidateExit,
          chain_path: c.chain_path || c.node_name,
          chain_node_ids: Array.isArray(c.chain_node_ids) ? [...c.chain_node_ids] : null,
        })
      }
    }
  }
  for (const entry of unresolvedForConsensus) {
    const tKey = chainTargetKey(entry)
    if (!tKey) continue
    if (!needsLandingAttributionFallback(entry.exit, tKey)) continue
    const consensus = consensusExitByTarget.get(tKey)
    if (!consensus) continue
    entry.exit = consensus.exit
    if (consensus.chain_path && (!entry.chain_node_names || consensus.chain_path.split(' → ').length > entry.chain_node_names.length)) {
      entry.node_name = consensus.chain_path
    }
    if (consensus.chain_node_ids && consensus.chain_node_ids.length > (entry.chain_node_ids?.length || 0)) {
      entry.chain_node_ids = [...consensus.chain_node_ids]
    }
  }

  return connections.filter((c) => !consumed.has(c) && !c.is_chain_relay)
}
