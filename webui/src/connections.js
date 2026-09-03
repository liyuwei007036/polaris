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
