package main

import (
	"testing"
	"time"
)

// The cadence a node reports at is a fleet-wide decision, so the master's
// value replaces whatever the node's own file says. Editing a file on every
// node and restarting each agent is exactly what this avoids.
func TestSessionConnectionsIntervalPrefersTheMasterCadence(t *testing.T) {
	configured := 2 * time.Second
	if got := sessionConnectionsInterval(configured, 10); got != 10*time.Second {
		t.Fatalf("interval = %v, want the master's 10s", got)
	}
	// A master that says nothing leaves the node running as configured, which
	// is what keeps an older master working against a newer agent.
	if got := sessionConnectionsInterval(configured, 0); got != configured {
		t.Fatalf("interval = %v, want the configured %v", got, configured)
	}
	// Out of range means the agent would reject it anyway; falling back to the
	// local value beats pushing at a cadence neither side agreed on.
	for _, seconds := range []uint32{31, 3600} {
		if got := sessionConnectionsInterval(configured, seconds); got != configured {
			t.Fatalf("interval for %ds = %v, want the configured %v", seconds, got, configured)
		}
	}
}
