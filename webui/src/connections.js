import { shallowRef, triggerRef } from 'vue'

// One shared connections stream for the whole console. Every page that shows
// real-time data reads the same snapshot map instead of opening its own
// EventSource, which previously meant several streams — and several full
// re-renders per push — whenever more than one such page had been visited.
export const connectionSnapshots = shallowRef(new Map())

let source
let subscribers = new Set()

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
}

// subscribeConnections registers a callback fired whenever a snapshot lands.
// The stream stays open while at least one page is subscribed.
export function subscribeConnections(subscriber) {
  subscribers.add(subscriber)
  ensureSource()
  return () => {
    subscribers.delete(subscriber)
    if (subscribers.size === 0) closeConnectionEvents()
  }
}

export function closeConnectionEvents() {
  source?.close()
  source = undefined
  connectionSnapshots.value.clear()
  triggerRef(connectionSnapshots)
}
