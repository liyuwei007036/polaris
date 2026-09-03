package agent

import (
	"testing"
	"time"
)

// The whole point of aligning pushes to the wall clock: two nodes that
// connected at unrelated moments still report at the same instant, so the
// master can add their readings together.
func TestUntilNextTickPutsEveryNodeOnTheSameInstant(t *testing.T) {
	const interval = 10 * time.Second
	grid := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	// One node connected three seconds into the round, another seven.
	early := grid.Add(3 * time.Second)
	late := grid.Add(7 * time.Second)
	if got := early.Add(untilNextTick(early, interval)); !got.Equal(grid.Add(interval)) {
		t.Fatalf("node that connected at +3s reports at %v, want %v", got, grid.Add(interval))
	}
	if got := late.Add(untilNextTick(late, interval)); !got.Equal(grid.Add(interval)) {
		t.Fatalf("node that connected at +7s reports at %v, want %v", got, grid.Add(interval))
	}
}

// Sitting exactly on the grid means the round that just fired is done, not
// that another one is due immediately.
func TestUntilNextTickWaitsAWholeIntervalOnTheGrid(t *testing.T) {
	const interval = 10 * time.Second
	grid := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	if got := untilNextTick(grid, interval); got != interval {
		t.Fatalf("wait on the grid = %v, want a full %v", got, interval)
	}
}

// Sub-second cadences are configurable down to one second; anything at or
// below zero would spin.
func TestUntilNextTickRefusesToSpinOnAnUnsetInterval(t *testing.T) {
	if got := untilNextTick(time.Now(), 0); got <= 0 {
		t.Fatalf("wait with no interval = %v, want a positive fallback", got)
	}
}
