package control

import (
	"encoding/json"
	"sync"
	"time"
)

// connectionsStaleAfter bounds how long a node's last-pushed connection list
// is trusted. Agents push every few seconds while online; a node that has
// gone silent for longer than this is presumed offline and is cleared
// instead of leaving stale entries in the aggregated view forever.
const connectionsStaleAfter = 15 * time.Second

// nodeConnectionsSnapshot is the latest connection list an agent has reported
// for one node. It lives in memory only — nothing here is persisted, since
// only the current state matters for the real-time connections view.
type nodeConnectionsSnapshot struct {
	NodeID      string          `json:"node_id"`
	CollectedAt string          `json:"collected_at"`
	Connections json.RawMessage `json:"connections"`
}

// connectionsHub fans out real-time connection snapshots from agents to every
// connected browser over Server-Sent Events. Agents push proactively as
// connections change (see (*Server).agentPushConnections); the master never
// polls an agent for this data.
type connectionsHub struct {
	mu         sync.Mutex
	latest     map[string]nodeConnectionsSnapshot
	receivedAt map[string]time.Time
	clients    map[chan nodeConnectionsSnapshot]struct{}
}

func newConnectionsHub() *connectionsHub {
	return &connectionsHub{
		latest:     make(map[string]nodeConnectionsSnapshot),
		receivedAt: make(map[string]time.Time),
		clients:    make(map[chan nodeConnectionsSnapshot]struct{}),
	}
}

// pruneLocked drops nodes that have not pushed recently and returns their
// IDs so callers can notify subscribers the entries are gone. Caller must
// hold h.mu.
func (h *connectionsHub) pruneLocked(now time.Time) []string {
	var stale []string
	for id, at := range h.receivedAt {
		if now.Sub(at) > connectionsStaleAfter {
			stale = append(stale, id)
		}
	}
	for _, id := range stale {
		delete(h.latest, id)
		delete(h.receivedAt, id)
	}
	return stale
}

// snapshot returns every currently-fresh node's last reported connections,
// used to seed a browser client that has just connected.
func (h *connectionsHub) snapshot() []nodeConnectionsSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(time.Now())
	out := make([]nodeConnectionsSnapshot, 0, len(h.latest))
	for _, v := range h.latest {
		out = append(out, v)
	}
	return out
}

// update records a freshly pushed snapshot and broadcasts it, plus clears any
// other node that has gone stale in the meantime.
func (h *connectionsHub) update(snap nodeConnectionsSnapshot) {
	now := time.Now()
	h.mu.Lock()
	h.latest[snap.NodeID] = snap
	h.receivedAt[snap.NodeID] = now
	stale := h.pruneLocked(now)
	clients := make([]chan nodeConnectionsSnapshot, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	broadcast := func(s nodeConnectionsSnapshot) {
		for _, c := range clients {
			select {
			case c <- s:
			default:
			}
		}
	}
	broadcast(snap)
	for _, id := range stale {
		broadcast(nodeConnectionsSnapshot{NodeID: id, Connections: json.RawMessage("[]")})
	}
}

func (h *connectionsHub) subscribe() chan nodeConnectionsSnapshot {
	c := make(chan nodeConnectionsSnapshot, 8)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *connectionsHub) unsubscribe(c chan nodeConnectionsSnapshot) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}
