package agent

import (
	"strings"
	"testing"
)

func TestParseMemInfo_ServerTotalMemory(t *testing.T) {
	// A representative /proc/meminfo from a 2GB KVM VPS running Linux 5.15+
	sample := `
MemTotal:        2006764 kB
MemFree:          401352 kB
MemAvailable:    1234567 kB
Buffers:           87654 kB
Cached:           765432 kB
SwapCached:            0 kB
Active:           654321 kB
Inactive:         432109 kB
Shmem:             12345 kB
Slab:              87654 kB
SReclaimable:      65432 kB
SUnreclaim:        22222 kB
`
	total, used, ok := parseMemInfo(strings.NewReader(sample))
	if !ok {
		t.Fatalf("expected ok=true")
	}

	expectedTotal := uint64(2006764 * 1024)
	if total != expectedTotal {
		t.Fatalf("expected total=%d, got=%d", expectedTotal, total)
	}

	// Server actual memory used = MemTotal - MemAvailable
	// 2006764 - 1234567 = 772197 kB (~38.5%)
	expectedUsed := uint64(772197 * 1024)
	if used != expectedUsed {
		t.Fatalf("expected used=%d, got=%d", expectedUsed, used)
	}
}

func TestParseMemInfo_FallbackWithoutMemAvailable(t *testing.T) {
	sample := `
MemTotal:        1000000 kB
MemFree:          200000 kB
Buffers:          100000 kB
Cached:           300000 kB
`
	total, used, ok := parseMemInfo(strings.NewReader(sample))
	if !ok {
		t.Fatalf("expected ok=true")
	}

	expectedTotal := uint64(1000000 * 1024)
	// Fallback used = MemTotal - MemFree - Buffers - Cached
	// 1000000 - 200000 - 100000 - 300000 = 400000 kB
	expectedUsed := uint64(400000 * 1024)
	if total != expectedTotal || used != expectedUsed {
		t.Fatalf("unexpected values: total=%d (exp %d), used=%d (exp %d)", total, expectedTotal, used, expectedUsed)
	}
}

func TestParseMemInfo_ZeroFree(t *testing.T) {
	sample := `
MemTotal:        1024000 kB
MemFree:               0 kB
`
	total, used, ok := parseMemInfo(strings.NewReader(sample))
	if !ok {
		t.Fatalf("expected ok=true")
	}

	expectedTotal := uint64(1024000 * 1024)
	expectedUsed := uint64(1024000 * 1024)
	if total != expectedTotal || used != expectedUsed {
		t.Fatalf("unexpected values: total=%d (exp %d), used=%d (exp %d)", total, expectedTotal, used, expectedUsed)
	}
}

func TestParseMemInfo_OverflowProtection(t *testing.T) {
	// MemFree unexpectedly larger than MemTotal
	sample := `
MemTotal:        1000 kB
MemFree:         2000 kB
`
	total, used, ok := parseMemInfo(strings.NewReader(sample))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if total != 1000*1024 {
		t.Fatalf("expected total=1000*1024, got=%d", total)
	}
	if used != 0 {
		t.Fatalf("expected used=0 on overflow, got=%d", used)
	}
}

func TestParseMemInfo_Empty(t *testing.T) {
	_, _, ok := parseMemInfo(strings.NewReader(""))
	if ok {
		t.Fatalf("expected ok=false for empty input")
	}
}

func TestParseMemAndSwapInfo(t *testing.T) {
	sample := `
MemTotal:        4000000 kB
MemFree:         1000000 kB
MemAvailable:    2500000 kB
SwapTotal:       2000000 kB
SwapFree:        1500000 kB
`
	memTotal, memUsed, swapTotal, swapUsed, ok := parseMemAndSwapInfo(strings.NewReader(sample))
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if memTotal != 4000000*1024 || memUsed != 1500000*1024 {
		t.Fatalf("unexpected mem values: total=%d, used=%d", memTotal, memUsed)
	}
	if swapTotal != 2000000*1024 || swapUsed != 500000*1024 {
		t.Fatalf("unexpected swap values: total=%d, used=%d", swapTotal, swapUsed)
	}
}
