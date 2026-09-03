package control

import (
	"testing"
	"time"
)

// The ranking counts connections made, not connections open, so a connection
// that shows up in ten consecutive pushes still counts once.
func TestNodeActivityCountsAConnectionOnceAtFirstSight(t *testing.T) {
	activity := newNodeActivity()
	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	connections := []storedConnection{{ID: "a", User: "alice"}}
	for round := range 10 {
		activity.record("node-1", connections, start.Add(time.Duration(round)*time.Second))
	}

	popular := activity.popular(start.Add(10*time.Second), popularNodeLimit)
	if len(popular) != 1 || popular[0].Name != "alice" || popular[0].Count != 1 {
		t.Fatalf("ranking = %+v, want alice counted once", popular)
	}
}

// A short-lived connection stops counting once it ages past the window, which
// is what makes the ranking "recently" rather than "ever".
func TestNodeActivityDropsSightingsPastTheWindow(t *testing.T) {
	activity := newNodeActivity()
	start := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	activity.record("node-1", []storedConnection{{ID: "old", User: "alice"}}, start)
	activity.record("node-1", []storedConnection{{ID: "new", User: "bob"}}, start.Add(nodeActivityWindow))

	popular := activity.popular(start.Add(nodeActivityWindow+time.Second), popularNodeLimit)
	if len(popular) != 1 || popular[0].Name != "bob" {
		t.Fatalf("ranking = %+v, want only the sighting inside the window", popular)
	}
}

// A connection that matched no account rule carries no account name, so the
// inbound service it arrived on stands in for it.
func TestNodeActivityFallsBackToTheInboundServiceWithoutAnAccount(t *testing.T) {
	activity := newNodeActivity()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	activity.record("node-1", []storedConnection{
		{ID: "a", User: "alice", ListenerName: "香港入口"},
		{ID: "b", ListenerName: "香港入口"},
		{ID: "c"},
	}, now)

	counts := map[string]int{}
	for _, entry := range activity.popular(now, popularNodeLimit) {
		counts[entry.Name] = entry.Count
	}
	if counts["alice"] != 1 || counts["香港入口"] != 1 || counts["未知"] != 1 {
		t.Fatalf("ranking = %+v, want the account, then the service, then 未知", counts)
	}
}

// Busiest first, and ties broken by name so the bars do not swap places from
// one round to the next.
func TestNodeActivityRanksBusiestFirstAndBreaksTiesByName(t *testing.T) {
	activity := newNodeActivity()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	activity.record("node-1", []storedConnection{
		{ID: "1", User: "carol"}, {ID: "2", User: "carol"}, {ID: "3", User: "carol"},
		{ID: "4", User: "bob"},
		{ID: "5", User: "alice"},
	}, now)

	popular := activity.popular(now, popularNodeLimit)
	if len(popular) != 3 {
		t.Fatalf("ranking = %+v, want three entries", popular)
	}
	if popular[0].Name != "carol" || popular[0].Count != 3 {
		t.Fatalf("first = %+v, want carol with 3", popular[0])
	}
	if popular[1].Name != "alice" || popular[2].Name != "bob" {
		t.Fatalf("ties = %v then %v, want alphabetical", popular[1].Name, popular[2].Name)
	}
}

// The ranking carries only the top few, so a fleet with many accounts does not
// push an unbounded list into every console.
func TestNodeActivityRankingIsCappedAtTheLimit(t *testing.T) {
	activity := newNodeActivity()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	connections := make([]storedConnection, 0, popularNodeLimit+3)
	for index := range popularNodeLimit + 3 {
		connections = append(connections, storedConnection{ID: string(rune('a' + index)), User: string(rune('a' + index))})
	}
	activity.record("node-1", connections, now)

	if popular := activity.popular(now, popularNodeLimit); len(popular) != popularNodeLimit {
		t.Fatalf("ranking holds %d entries, want %d", len(popular), popularNodeLimit)
	}
}

// sing-box numbers connections per node, so the same ID on two nodes is two
// connections rather than one seen twice.
func TestNodeActivityKeepsNodesApart(t *testing.T) {
	activity := newNodeActivity()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	activity.record("node-1", []storedConnection{{ID: "shared", User: "alice"}}, now)
	activity.record("node-2", []storedConnection{{ID: "shared", User: "alice"}}, now)

	popular := activity.popular(now, popularNodeLimit)
	if len(popular) != 1 || popular[0].Count != 2 {
		t.Fatalf("ranking = %+v, want alice credited with both nodes' connections", popular)
	}
}

// The transport split is counted where every other push-derived figure is, so
// the console draws what it is handed.
func TestProtocolCountsGroupByTransport(t *testing.T) {
	counts := protocolCounts([]storedConnection{
		{ID: "a", Network: "tcp"}, {ID: "b", Network: "TCP"},
		{ID: "c", Network: "udp"},
		{ID: "d", Network: "icmp"}, {ID: "e"},
	})
	if counts["TCP"] != 2 || counts["UDP"] != 1 || counts["其他"] != 2 {
		t.Fatalf("protocol counts = %v, want 2 TCP, 1 UDP and 2 其他", counts)
	}
}
