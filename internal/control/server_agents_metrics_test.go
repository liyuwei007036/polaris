package control

import (
	"testing"
)

func TestAccumulateMetricNeverResetsOnAgentUpgrade(t *testing.T) {
	var totals map[string]uint64
	var rawTotals map[string]uint64

	// 1. Initial report: 100 MB rx, 50 MB tx
	totals, rawTotals = accumulateMetric(totals, rawTotals, 100*1024*1024, 50*1024*1024)
	if totals["received_bytes"] != 100*1024*1024 || totals["sent_bytes"] != 50*1024*1024 {
		t.Fatalf("unexpected initial totals: %v", totals)
	}

	// 2. Normal traffic growth: +20 MB rx, +10 MB tx
	totals, rawTotals = accumulateMetric(totals, rawTotals, 120*1024*1024, 60*1024*1024)
	if totals["received_bytes"] != 120*1024*1024 || totals["sent_bytes"] != 60*1024*1024 {
		t.Fatalf("unexpected grown totals: %v", totals)
	}

	// 3. Agent upgrades / sing-box restarts! Counter resets to 1 MB rx, 500 KB tx
	totals, rawTotals = accumulateMetric(totals, rawTotals, 1*1024*1024, 512*1024)
	expectedRx := uint64(120*1024*1024 + 1*1024*1024)
	expectedTx := uint64(60*1024*1024 + 512*1024)
	if totals["received_bytes"] != expectedRx {
		t.Fatalf("expected monotonic rx %d after upgrade restart, got %d", expectedRx, totals["received_bytes"])
	}
	if totals["sent_bytes"] != expectedTx {
		t.Fatalf("expected monotonic tx %d after upgrade restart, got %d", expectedTx, totals["sent_bytes"])
	}

	// 4. Continued traffic after upgrade: +5 MB rx, +2 MB tx (reported raw: 6 MB rx, 2.5 MB tx)
	totals, rawTotals = accumulateMetric(totals, rawTotals, 6*1024*1024, 2560*1024)
	expectedRx += 5 * 1024 * 1024
	expectedTx += (2560 - 512) * 1024
	if totals["received_bytes"] != expectedRx {
		t.Fatalf("expected continued growth rx %d, got %d", expectedRx, totals["received_bytes"])
	}
	if totals["sent_bytes"] != expectedTx {
		t.Fatalf("expected continued growth tx %d, got %d", expectedTx, totals["sent_bytes"])
	}
}

func TestAccumulateMetricLegacyMigration(t *testing.T) {
	// Historical database has totals = 50 MB, rawTotals = nil
	totals := map[string]uint64{"received_bytes": 50 * 1024 * 1024, "sent_bytes": 25 * 1024 * 1024}
	var rawTotals map[string]uint64

	// Node reports 60 MB rx (has not restarted)
	totals, rawTotals = accumulateMetric(totals, rawTotals, 60*1024*1024, 30*1024*1024)
	if totals["received_bytes"] != 60*1024*1024 || totals["sent_bytes"] != 30*1024*1024 {
		t.Fatalf("expected legacy migration to adjust delta correctly: %v", totals)
	}

	// Now node restarts with raw 5 MB
	totals, rawTotals = accumulateMetric(totals, rawTotals, 5*1024*1024, 2*1024*1024)
	if totals["received_bytes"] != 65*1024*1024 || totals["sent_bytes"] != 32*1024*1024 {
		t.Fatalf("expected monotonic addition after restart: %v", totals)
	}
}
