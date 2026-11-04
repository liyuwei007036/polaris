import { shallowRef } from 'vue'

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
  source = new EventSource('/api/v1/events/connections', { withCredentials: true })
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

export function closeConnectionEvents() {
  source?.close()
  source = undefined
  fleetTotals.value = null
  connectionSnapshots.value = new Map()
}

// mergeChainedConnections combines the hops of a chained proxy into a single
// logical connection. Without merging, each leg of a chain (ingress proxy on
// node A, relay on node B, and so forth) appears as a separate connection with
// intermediate server IPs shown as client devices, multiplying the fleet count.
export function mergeChainedConnections(connections, nodes = []) {
  if (!connections || connections.length === 0) return []

  const nodeMap = new Map((nodes || []).map((n) => [n.id, n]))

  const consumed = new Set()

  const isChainEntry = (c) => Boolean(
    c.is_chain_entry ||
    (c.outbound && c.outbound.startsWith('chain-')) ||
    (Array.isArray(c.chains) && c.chains[0] && c.chains[0].startsWith('chain-'))
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

  for (const entry of connections) {
    if (!isChainEntry(entry) || consumed.has(entry)) continue

    let currentLeg = entry
    while (true) {
      // 在整张连接表里扫一遍，挑证据最强的一条候选段（并列取先出现的）。
      // 证据强度：
      //   3 —— 主控标记的中继段，并明确指认来源就是上一跳节点；
      //   2 —— 来源 IP、“IP:端口” 来源串、或链路账户与上一跳完全对上；
      //   1 —— 主控标记的中继段，但来源无法归到具体节点（兜底）；
      //   0 —— 没有中继证据。绝不因目标相同就合并。
      // 之所以要“挑最强”而不是“见一条就用”：两条并发链路汇聚在同一目标
      // 时，先扫到的入口不能抢走属于别人的中继段。
      let best = null
      let bestEvidence = 0
      for (const candidate of connections) {
        if (candidate === entry || candidate === currentLeg || consumed.has(candidate)) continue
        if (candidate.node_id === currentLeg.node_id) continue

        const destMatch = (
          (currentLeg.target && candidate.target && currentLeg.target === candidate.target) ||
          (currentLeg.destination && candidate.destination && currentLeg.destination === candidate.destination) ||
          (currentLeg.host && candidate.host && currentLeg.host === candidate.host)
        )
        if (!destMatch) continue

        if (currentLeg.network && candidate.network &&
            currentLeg.network.toLowerCase() !== candidate.network.toLowerCase()) {
          continue
        }

        // 主控标记的中继段：能指认来源时按来源判定（精确证据），指认不出
        // 具体节点时才降级为“只说明来自另一台服务器”的兜底证据。来源明确
        // 不是上一跳的，可证明它属于别的链路，证据为零。
        let evidence = candidate.is_chain_relay
          ? (candidate.relay_upstream_node_id
              ? (candidate.relay_upstream_node_id === currentLeg.node_id ? 3 : 0)
              : 1)
          : 0

        const sourceNode = nodeMap.get(currentLeg.node_id)
        // 中继段的来源必须能对回“上一跳”那台服务器：来源 IP 与该节点的注册
        // 地址完全一致，或完整 “IP:端口” 来源串里带着该节点地址。对不上
        // fleet 里其他节点的、或来源是本节点自己的，都不算中继段。
        if (sourceNode) {
          const sourceIPMatch = Boolean(candidate.source_ip && (
            candidate.source_ip.trim().toLowerCase() === bare(sourceNode.client_address) ||
            candidate.source_ip.trim().toLowerCase() === bare(sourceNode.last_observed_ip)
          ))
          const sourceAliasMatch = Boolean(candidate.source &&
            sourceNode.client_address && candidate.source.includes(sourceNode.client_address))
          // 账户匹配取出口标签尾部括号里的完整账户名，而不是任意子串匹配：
          // 账户别名片段撞进某个出口名字里只是一次巧合，不能当作链路证据。
          const label = currentLeg.outbound_name || currentLeg.exit || ''
          const labelAccount = (label.match(/\(([^()]+)\)\s*$/) || [])[1]
          const userMatch = Boolean(labelAccount && candidate.user && labelAccount === candidate.user)
          if (evidence < 2 && (sourceIPMatch || sourceAliasMatch || userMatch)) evidence = 2
        }

        if (evidence > bestEvidence) {
          best = candidate
          bestEvidence = evidence
          if (evidence === 3) break
        }
      }

      if (!best) break

      consumed.add(best)

      if (!entry.chain_node_ids) entry.chain_node_ids = [entry.node_id]
      entry.chain_node_ids.push(best.node_id)

      if (!entry.chain_node_names) entry.chain_node_names = [entry.node_name]
      entry.chain_node_names.push(best.node_name)

      entry.node_name = entry.chain_node_names.join(' → ')

      if (best.exit) {
        entry.chain_exit = entry.exit ? `${entry.exit} → ${best.exit}` : best.exit
        entry.exit = best.exit
      }

      if (best.has_rates && !entry.has_rates) {
        entry.has_rates = true
        entry.download_rate = best.download_rate
        entry.upload_rate = best.upload_rate
      }

      currentLeg = best
      if (!isChainEntry(best)) break
    }
  }

  return connections.filter((c) => !consumed.has(c) && !c.is_chain_relay)
}
