package control

import (
	"testing"
	"time"
)

// The master adds up the round, so a browser never has to. Nodes that have not
// measured a rate yet are present in the count but contribute nothing.
func TestFleetTotalsSumOnlyNodesThatMeasuredARate(t *testing.T) {
	hub := newConnectionsHub()
	hub.update(nodeConnectionsSnapshot{NodeID: "a", ConnectionCount: 3, HasRates: true, ReceivedRate: 1000, SentRate: 100})
	hub.update(nodeConnectionsSnapshot{NodeID: "b", ConnectionCount: 5, HasRates: true, ReceivedRate: 2500, SentRate: 250})
	hub.update(nodeConnectionsSnapshot{NodeID: "c", ConnectionCount: 2})

	totals := hub.totals(time.Now(), nil)
	if totals.DownloadRate != 3500 || totals.UploadRate != 350 {
		t.Fatalf("totals = ↓%v ↑%v, want ↓3500 ↑350", totals.DownloadRate, totals.UploadRate)
	}
	if totals.ConnectionCount != 10 {
		t.Fatalf("connection count = %d, want all 10 counted", totals.ConnectionCount)
	}
	if totals.Nodes != 3 || totals.Reporting != 2 {
		t.Fatalf("nodes = %d reporting = %d, want 3 present and 2 measuring", totals.Nodes, totals.Reporting)
	}
	if !totals.HasRates {
		t.Fatal("totals claim no rate, want one measured from the two nodes that reported")
	}
}

// The transport split is a fleet figure like any other: each node counts its
// own connections and the master adds the groups up.
func TestFleetTotalsAddUpProtocolCountsAcrossNodes(t *testing.T) {
	hub := newConnectionsHub()
	hub.update(nodeConnectionsSnapshot{NodeID: "a", ConnectionCount: 3, Protocols: map[string]int{"TCP": 2, "UDP": 1}})
	hub.update(nodeConnectionsSnapshot{NodeID: "b", ConnectionCount: 2, Protocols: map[string]int{"TCP": 1, "其他": 1}})

	totals := hub.totals(time.Now(), []popularNode{{Name: "alice", Count: 4}})
	if totals.Protocols["TCP"] != 3 || totals.Protocols["UDP"] != 1 || totals.Protocols["其他"] != 1 {
		t.Fatalf("protocols = %v, want 3 TCP, 1 UDP and 1 其他", totals.Protocols)
	}
	if len(totals.PopularNodes) != 1 || totals.PopularNodes[0].Name != "alice" {
		t.Fatalf("popular nodes = %+v, want the ranking passed in", totals.PopularNodes)
	}
	if totals.PopularWindowMinutes != int(nodeActivityWindow/time.Minute) {
		t.Fatalf("window = %d minutes, want the master's own %v", totals.PopularWindowMinutes, nodeActivityWindow)
	}
}

// A node that stopped reporting must drop out of the total rather than hold
// its last reading there forever.
func TestFleetTotalsDropNodesThatWentSilent(t *testing.T) {
	hub := newConnectionsHub()
	hub.update(nodeConnectionsSnapshot{NodeID: "a", ConnectionCount: 3, HasRates: true, ReceivedRate: 1000, SentRate: 100})
	hub.update(nodeConnectionsSnapshot{NodeID: "gone", ConnectionCount: 9, HasRates: true, ReceivedRate: 9000, SentRate: 900})
	hub.mu.Lock()
	hub.receivedAt["gone"] = time.Now().Add(-connectionsStaleAfter - time.Second)
	hub.mu.Unlock()

	totals := hub.totals(time.Now(), nil)
	if totals.DownloadRate != 1000 || totals.Nodes != 1 || totals.ConnectionCount != 3 {
		t.Fatalf("totals = ↓%v across %d nodes / %d connections, want only the live node", totals.DownloadRate, totals.Nodes, totals.ConnectionCount)
	}
}

// An empty fleet reports nothing rather than a measured zero, so the console
// can tell "no servers" apart from "servers that are idle".
func TestFleetTotalsReportNoRateWithNothingConnected(t *testing.T) {
	if totals := newConnectionsHub().totals(time.Now(), nil); totals.HasRates || totals.Nodes != 0 {
		t.Fatalf("empty fleet totals = %+v, want no nodes and no measured rate", totals)
	}
}

// The master has to add the round up after it lands, never before: agents push
// on the grid instant, so aggregating on that same instant would race them.
func TestUntilNextRoundLandsAfterTheNodesPush(t *testing.T) {
	for _, interval := range []time.Duration{time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second} {
		now := time.Date(2026, 9, 3, 12, 0, 3, 0, time.UTC)
		nodesPushAt := now.Add(interval - time.Duration(now.UnixNano())%interval)
		masterSumsAt := now.Add(untilNextRound(now, interval))
		if !masterSumsAt.After(nodesPushAt) {
			t.Fatalf("interval %v: master sums at %v, not after the nodes' push at %v", interval, masterSumsAt, nodesPushAt)
		}
		// The grace has to stay small enough that the reading is still current.
		if gap := masterSumsAt.Sub(nodesPushAt); gap > 2*time.Second {
			t.Fatalf("interval %v: master waits %v after the push, want at most 2s", interval, gap)
		}
	}
}

// The fleet's push is switched by the first console to open the live stream
// and the last one to close it — not by every console in between, which would
// have the nodes stopping while somebody else was still watching.
func TestConnectionsHubAnnouncesOnlyTheFirstAndLastWatcher(t *testing.T) {
	hub := newConnectionsHub()
	var announced []bool
	hub.onWatchers = func(watching bool) { announced = append(announced, watching) }

	first := hub.subscribe()
	second := hub.subscribe()
	if !hub.watched() {
		t.Fatal("hub reports nobody watching with two streams open")
	}
	hub.unsubscribe(first)
	if !hub.watched() {
		t.Fatal("hub stopped counting the second stream as a watcher")
	}
	hub.unsubscribe(second)
	if hub.watched() {
		t.Fatal("hub still reports a watcher with every stream closed")
	}
	if len(announced) != 2 || !announced[0] || announced[1] {
		t.Fatalf("announcements = %v, want exactly [true false]", announced)
	}
}

// Sessions are nudged rather than handed the value, so a burst of opens and
// closes can collapse into one wake-up without the last one being lost: what a
// session reads when it wakes is the state as it stands.
func TestSetFleetStreamingNudgesSessionsAndKeepsTheStateCurrent(t *testing.T) {
	server := &Server{controls: map[string]*controlSession{
		"node-1": {watch: make(chan struct{}, 1)},
		"node-2": {watch: make(chan struct{}, 1)},
	}}
	server.setFleetStreaming(true)
	if !server.fleetStreamingState() {
		t.Fatal("streaming state was not recorded")
	}
	for id, session := range server.controls {
		select {
		case <-session.watch:
		default:
			t.Fatalf("session %s was never nudged", id)
		}
	}
	// Flip repeatedly without anyone reading in between: the nudges coalesce,
	// but the state a session would read must be the final one.
	server.setFleetStreaming(false)
	server.setFleetStreaming(true)
	server.setFleetStreaming(false)
	if server.fleetStreamingState() {
		t.Fatal("state after the last flip is on, want off")
	}
}

// The whole chain, as a server actually assembles it: a browser opening the
// live stream has to reach the connected nodes and start them reporting, and
// closing it has to stop them. Testing the pieces separately would not catch
// the hub and the switch never being wired together.
func TestOpeningTheLiveStreamSwitchesTheFleetOnAndOff(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if server.fleetStreamingState() {
		t.Fatal("a master with no console open starts with the fleet reporting, want it quiet")
	}

	session := &controlSession{watch: make(chan struct{}, 1)}
	server.controls["node-1"] = session

	stream := server.connHub.subscribe()
	if !server.fleetStreamingState() {
		t.Fatal("opening the live stream did not switch the fleet on")
	}
	select {
	case <-session.watch:
	default:
		t.Fatal("the connected node was never told to start reporting")
	}

	server.connHub.unsubscribe(stream)
	if server.fleetStreamingState() {
		t.Fatal("closing the last live stream left the fleet reporting")
	}
	select {
	case <-session.watch:
	default:
		t.Fatal("the connected node was never told to stop reporting")
	}
}

// A cadence that was never configured must not turn into a busy loop.
func TestUntilNextRoundFallsBackWithoutAnInterval(t *testing.T) {
	if got := untilNextRound(time.Now(), 0); got <= 0 || got > DefaultConnectionsInterval+2*time.Second {
		t.Fatalf("wait with no interval = %v, want one bounded round", got)
	}
}
