import { shallowRef, triggerRef } from 'vue'

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
  triggerRef(connectionSnapshots)
  subscribers.forEach((subscriber) => subscriber())
}

function applyNode(item) {
  if (!item?.node_id) return
  connectionSnapshots.value.set(item.node_id, item)
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
  connectionSnapshots.value.clear()
  triggerRef(connectionSnapshots)
}

// mergeChainedConnections combines the hops of a chained proxy into a single
// logical connection. Without merging, each leg of a chain (ingress proxy on
// node A, relay on node B, and so forth) appears as a separate connection with
// intermediate server IPs shown as client devices, multiplying the fleet count.
export function mergeChainedConnections(connections, nodes = []) {
  if (!connections || connections.length === 0) return []

  const nodeMap = new Map((nodes || []).map((n) => [n.id, n]))
  const nodeIPs = new Set()
  for (const n of nodes || []) {
    if (n.client_address) {
      const clean = n.client_address.split(':')[0].trim().toLowerCase()
      if (clean) nodeIPs.add(clean)
    }
    if (n.last_observed_ip) {
      const clean = n.last_observed_ip.split(':')[0].trim().toLowerCase()
      if (clean) nodeIPs.add(clean)
    }
  }

  const consumed = new Set()

  const isChainEntry = (c) => Boolean(
    c.is_chain_entry ||
    (c.outbound && c.outbound.startsWith('chain-')) ||
    (Array.isArray(c.chains) && c.chains[0] && c.chains[0].startsWith('chain-'))
  )

  const isRelay = (c) => Boolean(
    c.is_chain_relay ||
    (c.source_ip && nodeIPs.has(c.source_ip.toLowerCase()))
  )

  for (const entry of connections) {
    if (!isChainEntry(entry) || consumed.has(entry)) continue

    let currentLeg = entry
    while (true) {
      const match = connections.find((candidate) => {
        if (candidate === entry || candidate === currentLeg || consumed.has(candidate)) return false
        if (candidate.node_id === currentLeg.node_id) return false

        const destMatch = (
          (currentLeg.target && candidate.target && currentLeg.target === candidate.target) ||
          (currentLeg.destination && candidate.destination && currentLeg.destination === candidate.destination) ||
          (currentLeg.host && candidate.host && currentLeg.host === candidate.host)
        )
        if (!destMatch) return false

        if (currentLeg.network && candidate.network &&
            currentLeg.network.toLowerCase() !== candidate.network.toLowerCase()) {
          return false
        }

        const sourceNode = nodeMap.get(currentLeg.node_id)
        const sourceIPMatch = sourceNode && (
          (candidate.source_ip && (candidate.source_ip === sourceNode.client_address || candidate.source_ip === sourceNode.last_observed_ip)) ||
          (sourceNode.client_address && candidate.source && candidate.source.includes(sourceNode.client_address))
        )
        const userMatch = candidate.user && (
          (currentLeg.outbound_name && currentLeg.outbound_name.includes(candidate.user)) ||
          (currentLeg.exit && currentLeg.exit.includes(candidate.user))
        )

        return candidate.is_chain_relay || sourceIPMatch || userMatch || isRelay(candidate) || destMatch
      })

      if (!match) break

      consumed.add(match)

      if (!entry.chain_node_ids) entry.chain_node_ids = [entry.node_id]
      entry.chain_node_ids.push(match.node_id)

      if (!entry.chain_node_names) entry.chain_node_names = [entry.node_name]
      entry.chain_node_names.push(match.node_name)

      entry.node_name = entry.chain_node_names.join(' → ')

      if (match.exit) {
        entry.chain_exit = entry.exit ? `${entry.exit} → ${match.exit}` : match.exit
        entry.exit = match.exit
      }

      if (match.has_rates && !entry.has_rates) {
        entry.has_rates = true
        entry.download_rate = match.download_rate
        entry.upload_rate = match.upload_rate
      }

      currentLeg = match
      if (!isChainEntry(match)) break
    }
  }

  return connections.filter((c) => !consumed.has(c) && !c.is_chain_relay)
}
