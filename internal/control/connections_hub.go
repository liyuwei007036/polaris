package control

import (
	"encoding/json"
	"sync"
	"time"
)

// connectionsStaleAfter bounds how long a node's last-pushed connection list
// is trusted. Agents push every few seconds while online; a node that has
// gone silent for longer than this is presumed offline and is cleared
// instead of leaving stale entries in the aggregated view forever. It allows
// for several missed pushes at the default five-second cadence.
const connectionsStaleAfter = 30 * time.Second

// connectionRates turns the cumulative byte counts sing-box reports per
// connection into a rate. sing-box only ever reports totals for a connection,
// so the master — the first place that sees the same connection in two
// consecutive pushes — is where a per-connection rate can be measured at all.
// It exists so the connection list is denominated the same way the overview's
// live traffic is: one is a rate per connection, the other the node counters,
// and an operator can now read one against the other.
type connectionRates struct {
	mu       sync.Mutex
	previous map[string]connectionRateSample
}

// connectionRateSample holds one node's byte counts as of the push that
// carried them. Each push replaces the node's previous sample wholesale, so
// closed connections drop out on their own and the map never grows past the
// node's open connection list.
type connectionRateSample struct {
	at    time.Time
	bytes map[string]connectionBytes
}

type connectionBytes struct {
	upload   int64
	download int64
}

func newConnectionRates() *connectionRates {
	return &connectionRates{previous: make(map[string]connectionRateSample)}
}

// measure fills in each connection's rate from the previous push for the same
// node, then records the current counts for the next one. Rates are always
// divided by the interval between the two pushes rather than by how long a
// connection has been open, which is what lets the column be added up and
// compared against the node counters the overview charts.
func (r *connectionRates) measure(nodeID string, connections []storedConnection, now time.Time) {
	current := connectionRateSample{at: now, bytes: make(map[string]connectionBytes, len(connections))}
	for _, connection := range connections {
		if connection.ID == "" {
			continue
		}
		current.bytes[connection.ID] = connectionBytes{upload: connection.Upload, download: connection.Download}
	}
	r.mu.Lock()
	previous, seen := r.previous[nodeID]
	r.previous[nodeID] = current
	r.mu.Unlock()
	// A sample older than the staleness bound spans a gap the node was offline
	// for; the counters either restarted or kept running unobserved, and
	// spreading the difference over that gap would describe neither.
	if !seen || !now.After(previous.at) || now.Sub(previous.at) > connectionsStaleAfter {
		return
	}
	seconds := now.Sub(previous.at).Seconds()
	for index := range connections {
		connection := &connections[index]
		if connection.ID == "" {
			continue
		}
		was, ok := previous.bytes[connection.ID]
		switch {
		case ok && connection.Upload >= was.upload && connection.Download >= was.download:
			connection.HasRates = true
			connection.UploadRate = float64(connection.Upload-was.upload) / seconds
			connection.DownloadRate = float64(connection.Download-was.download) / seconds
		case !ok && openedAfter(connection.StartedAt, previous.at):
			// The master is seeing this connection for the first time, but
			// sing-box says it opened inside this interval, so every byte on
			// it moved inside it. Leaving these at nothing is what would make
			// the column add up to far less than the node counters whenever
			// traffic is made of short-lived connections.
			connection.HasRates = true
			connection.UploadRate = float64(connection.Upload) / seconds
			connection.DownloadRate = float64(connection.Download) / seconds
		}
	}
}

// openedAfter reports whether sing-box says the connection started after the
// given moment. An unparseable or absent timestamp reports false, so a
// connection is never credited with traffic that may predate the interval.
func openedAfter(startedAt string, reference time.Time) bool {
	if startedAt == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, startedAt)
	if err != nil {
		return false
	}
	return parsed.After(reference)
}

// nodeConnectionsSnapshot is the latest real-time report an agent has pushed
// for one node: its open connections plus the host traffic counters and the
// rate the agent measured between its own two most recent samples. It lives
// in memory only — nothing here is persisted, since only the current state
// matters for the real-time views, and writing it to SQLite on every push
// would mean a full read-modify-write of the metrics blob every few seconds
// per node.
type nodeConnectionsSnapshot struct {
	NodeID          string          `json:"node_id"`
	CollectedAt     string          `json:"collected_at"`
	Connections     json.RawMessage `json:"connections"`
	ConnectionCount int             `json:"connection_count"`
	HasTotals       bool            `json:"has_totals"`
	ReceivedBytes   uint64          `json:"received_bytes"`
	SentBytes       uint64          `json:"sent_bytes"`
	HasRates        bool            `json:"has_rates"`
	ReceivedRate    float64         `json:"received_rate"`
	SentRate        float64         `json:"sent_rate"`
}

// connectionsHub fans out real-time snapshots from agents to every connected
// browser over Server-Sent Events. Agents push proactively as connections
// change; the master never polls an agent for this data.
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

// node returns one node's last reported snapshot, if it is still fresh.
func (h *connectionsHub) node(nodeID string) (nodeConnectionsSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(time.Now())
	snap, ok := h.latest[nodeID]
	return snap, ok
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
