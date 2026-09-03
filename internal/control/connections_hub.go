package control

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// connectionsStaleAfter bounds how long a node's last-pushed connection list
// is trusted. A node that has gone silent for longer than this is presumed
// offline and is cleared, instead of leaving stale entries in the aggregated
// view forever. It is a wall-clock tolerance rather than a count of missed
// pushes, so it holds whatever cadence is configured — and it is also what
// clears the view out after the last console closes and the fleet stops
// reporting, which is why a console that opens later starts from what the
// nodes say now rather than from whatever was true when it last closed.
const connectionsStaleAfter = 30 * time.Second

// connectionRates turns the cumulative byte counts sing-box reports per
// connection into a rate. sing-box only ever reports totals for a connection,
// so the master — the first place that sees the same connection in two
// consecutive pushes — is where a per-connection rate can be measured at all.
// It is the only place a rate is measured. Every real-time throughput figure
// the console shows is these per-connection rates added up over some set of
// connections — the whole fleet for the overview, whatever a filter leaves
// for the connection list — so the numbers are subtotals of one another
// rather than separate measurements that never quite agree.
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
// connection has been open, which is what lets them be added up.
//
// It also returns those same rates summed for the node, which is the figure
// the overview charts. Both the overview's live traffic and the connection
// list's total are therefore the same arithmetic over the same connections,
// and an operator filtering the list watches a subtotal of the chart rather
// than a second measurement that never quite agrees with it.
func (r *connectionRates) measure(nodeID string, connections []storedConnection, now time.Time) (downloadRate, uploadRate float64, measured bool) {
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
		return 0, 0, false
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
			// a node read as idle whenever its traffic is made of short-lived
			// connections.
			connection.HasRates = true
			connection.UploadRate = float64(connection.Upload) / seconds
			connection.DownloadRate = float64(connection.Download) / seconds
		default:
			continue
		}
		downloadRate += connection.DownloadRate
		uploadRate += connection.UploadRate
	}
	// A node with nothing open measured zero, which is a reading rather than
	// an absence: two pushes have been compared and they found no traffic.
	return downloadRate, uploadRate, true
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

// protocolCounts groups a node's connections by the transport sing-box reports
// for them, so the overview charts TCP against UDP rather than inventing
// categories of its own. It is counted here, next to every other figure
// derived from a push, so the browser only ever draws what it is handed.
func protocolCounts(connections []storedConnection) map[string]int {
	counts := make(map[string]int, 3)
	for _, connection := range connections {
		switch strings.ToLower(connection.Network) {
		case "tcp":
			counts["TCP"]++
		case "udp":
			counts["UDP"]++
		default:
			counts["其他"]++
		}
	}
	return counts
}

// nodeActivityWindow is how far back "recently" reaches when ranking the
// busiest nodes.
const nodeActivityWindow = 10 * time.Minute

// popularNodeLimit is how many entries the ranking carries.
const popularNodeLimit = 5

// popularNode is one entry of the busiest-nodes ranking.
type popularNode struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// nodeActivity ranks client nodes by how often they connected recently rather
// than by how many connections happen to be open at this instant: a node that
// carried a hundred short requests a minute ago matters more than one holding
// a single idle connection.
//
// It lives on the master because the window has to outlast any one browser.
// Counted in the console, a freshly opened page started from an empty ranking
// and took the full window to fill it in, and a reload threw it away again.
type nodeActivity struct {
	mu   sync.Mutex
	seen map[string]nodeActivitySighting
}

// nodeActivitySighting is one connection, remembered from the push that first
// carried it. Recording it once at first sight is what makes the ranking a
// count of connections made rather than of pushes they showed up in.
type nodeActivitySighting struct {
	label string
	at    time.Time
}

func newNodeActivity() *nodeActivity {
	return &nodeActivity{seen: make(map[string]nodeActivitySighting)}
}

// record files every connection in this push that has not been seen before,
// then drops whatever has aged out of the window.
//
// A connection is attributed to the client node that authenticated it; the
// inbound service stands in only for connections that matched no account rule
// and therefore carry no account name.
func (a *nodeActivity) record(nodeID string, connections []storedConnection, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, connection := range connections {
		if connection.ID == "" {
			continue
		}
		key := nodeID + "\x00" + connection.ID
		if _, seen := a.seen[key]; seen {
			continue
		}
		label := connection.User
		if label == "" {
			label = connection.ListenerName
		}
		if label == "" {
			label = "未知"
		}
		a.seen[key] = nodeActivitySighting{label: label, at: now}
	}
	a.pruneLocked(now)
}

// popular returns the busiest nodes in the window, most connections first.
// Ties break by name so the bars do not swap places between rounds.
func (a *nodeActivity) popular(now time.Time, limit int) []popularNode {
	a.mu.Lock()
	a.pruneLocked(now)
	counts := make(map[string]int)
	for _, sighting := range a.seen {
		counts[sighting.label]++
	}
	a.mu.Unlock()
	ranked := make([]popularNode, 0, len(counts))
	for name, count := range counts {
		ranked = append(ranked, popularNode{Name: name, Count: count})
	}
	sort.Slice(ranked, func(left, right int) bool {
		if ranked[left].Count != ranked[right].Count {
			return ranked[left].Count > ranked[right].Count
		}
		return ranked[left].Name < ranked[right].Name
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// pruneLocked drops sightings that have aged out. Caller must hold a.mu.
func (a *nodeActivity) pruneLocked(now time.Time) {
	for key, sighting := range a.seen {
		if now.Sub(sighting.at) > nodeActivityWindow {
			delete(a.seen, key)
		}
	}
}

// nodeConnectionsSnapshot is the latest real-time report an agent has pushed
// for one node: its open connections and the node's throughput — this node's
// connection rates added up, as measured against its previous push. It lives
// in memory only — nothing here is persisted, since only the current state
// matters for the real-time views, and writing it to SQLite on every push
// would mean a full read-modify-write of the metrics blob every few seconds
// per node.
type nodeConnectionsSnapshot struct {
	NodeID          string          `json:"node_id"`
	CollectedAt     string          `json:"collected_at"`
	Connections     json.RawMessage `json:"connections"`
	ConnectionCount int             `json:"connection_count"`
	Protocols       map[string]int  `json:"protocols"`
	HasRates        bool            `json:"has_rates"`
	ReceivedRate    float64         `json:"received_rate"`
	SentRate        float64         `json:"sent_rate"`
}

// connectionsHub fans out real-time snapshots from agents to every connected
// browser over Server-Sent Events. Agents push proactively as connections
// change; the master never polls an agent for this data.
type connectionsHub struct {
	mu           sync.Mutex
	latest       map[string]nodeConnectionsSnapshot
	receivedAt   map[string]time.Time
	clients      map[chan nodeConnectionsSnapshot]struct{}
	totalClients map[chan fleetTotals]struct{}
	// onWatchers fires when the number of open browser streams crosses between
	// none and some. That transition is what starts and stops the fleet's
	// real-time push: nodes sample once a second while a console is open, and
	// stay quiet the rest of the time rather than polling sing-box around the
	// clock for a screen nobody has up.
	onWatchers func(watching bool)
}

func newConnectionsHub() *connectionsHub {
	return &connectionsHub{
		latest:       make(map[string]nodeConnectionsSnapshot),
		receivedAt:   make(map[string]time.Time),
		clients:      make(map[chan nodeConnectionsSnapshot]struct{}),
		totalClients: make(map[chan fleetTotals]struct{}),
	}
}

// fleetTotals is the whole fleet's throughput for one beat of the reporting
// grid: every open connection's rate, added up. Nodes push on the same
// wall-clock grid, so each node's rates were measured over the same window
// and the readings describe one moment — which is what makes adding them up
// mean something. The master sums them once per round rather than leaving each
// browser to add up whatever had arrived by the time it happened to redraw —
// which folded the same node reading into several points in a row and drew the
// line as a staircase built out of reporting phases rather than traffic.
type fleetTotals struct {
	At              string  `json:"at"`
	DownloadRate    float64 `json:"download_rate"`
	UploadRate      float64 `json:"upload_rate"`
	ConnectionCount int     `json:"connection_count"`
	Nodes           int     `json:"nodes"`
	Reporting       int     `json:"reporting"`
	HasRates        bool    `json:"has_rates"`
	// Protocols is the fleet's connections grouped by transport, and
	// PopularNodes the busiest client nodes over the window
	// PopularWindowMinutes names. Both ride this event rather than being
	// counted per browser, so every console shows the same figures and a page
	// that just opened shows them straight away.
	Protocols            map[string]int `json:"protocols"`
	PopularNodes         []popularNode  `json:"popular_nodes"`
	PopularWindowMinutes int            `json:"popular_window_minutes"`
}

// totals adds up what every node still considered live has reported. Nodes
// that have not measured a rate yet are counted as present but contribute
// nothing, so a fleet still warming up reads as slow rather than as fast as
// whichever node happened to report first.
func (h *connectionsHub) totals(now time.Time, popular []popularNode) fleetTotals {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pruneLocked(now)
	if popular == nil {
		popular = []popularNode{}
	}
	out := fleetTotals{
		At: now.UTC().Format(time.RFC3339Nano), Nodes: len(h.latest),
		Protocols: map[string]int{}, PopularNodes: popular,
		PopularWindowMinutes: int(nodeActivityWindow / time.Minute),
	}
	for _, snapshot := range h.latest {
		out.ConnectionCount += snapshot.ConnectionCount
		for name, count := range snapshot.Protocols {
			out.Protocols[name] += count
		}
		if !snapshot.HasRates {
			continue
		}
		out.Reporting++
		out.HasRates = true
		out.DownloadRate += snapshot.ReceivedRate
		out.UploadRate += snapshot.SentRate
	}
	return out
}

func (h *connectionsHub) broadcastTotals(totals fleetTotals) {
	h.mu.Lock()
	clients := make([]chan fleetTotals, 0, len(h.totalClients))
	for c := range h.totalClients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	for _, c := range clients {
		select {
		case c <- totals:
		default:
		}
	}
}

func (h *connectionsHub) subscribeTotals() chan fleetTotals {
	c := make(chan fleetTotals, 4)
	h.mu.Lock()
	h.totalClients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *connectionsHub) unsubscribeTotals(c chan fleetTotals) {
	h.mu.Lock()
	delete(h.totalClients, c)
	h.mu.Unlock()
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
	first, notify := len(h.clients) == 1, h.onWatchers
	h.mu.Unlock()
	if first && notify != nil {
		notify(true)
	}
	return c
}

func (h *connectionsHub) unsubscribe(c chan nodeConnectionsSnapshot) {
	h.mu.Lock()
	delete(h.clients, c)
	last, notify := len(h.clients) == 0, h.onWatchers
	h.mu.Unlock()
	if last && notify != nil {
		notify(false)
	}
}

// watched reports whether any console currently has the live stream open.
func (h *connectionsHub) watched() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}
