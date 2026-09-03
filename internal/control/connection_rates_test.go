package control

import (
	"math"
	"testing"
	"time"
)

func rateOf(t *testing.T, connections []storedConnection, id string) storedConnection {
	t.Helper()
	for _, connection := range connections {
		if connection.ID == id {
			return connection
		}
	}
	t.Fatalf("connection %q missing from the measured list", id)
	return storedConnection{}
}

func closeEnough(got, want float64) bool {
	return math.Abs(got-want) < 0.001
}

// The first push a node sends says nothing about speed: a connection's totals
// are the whole of its history, not what moved just now.
func TestConnectionRatesReportNothingOnFirstPush(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	connections := []storedConnection{{ID: "a", Upload: 1000, Download: 5000}}
	rates.measure("node-1", connections, start)
	if connections[0].HasRates {
		t.Fatalf("first push reported a rate of ↓%v ↑%v, want none", connections[0].DownloadRate, connections[0].UploadRate)
	}
}

// A connection present in both pushes is measured from the difference, divided
// by the interval between them.
func TestConnectionRatesMeasureAcrossTwoPushes(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a", Upload: 1000, Download: 5000}}, start)

	second := []storedConnection{{ID: "a", Upload: 3000, Download: 25000}}
	rates.measure("node-1", second, start.Add(10*time.Second))
	measured := rateOf(t, second, "a")
	if !measured.HasRates {
		t.Fatal("second push reported no rate, want one measured against the first")
	}
	if !closeEnough(measured.UploadRate, 200) || !closeEnough(measured.DownloadRate, 2000) {
		t.Fatalf("rate = ↓%v ↑%v, want ↓2000 ↑200 bytes/s", measured.DownloadRate, measured.UploadRate)
	}
}

// The point of the column is that it can be added up and read against the node
// counters the overview charts, so a connection that opened inside the
// interval has to contribute the bytes it carried there — reporting nothing
// for it would make a list of short-lived connections add up to almost zero.
func TestConnectionRatesCreditConnectionsOpenedInsideTheInterval(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a", Upload: 1000, Download: 5000}}, start)

	now := start.Add(10 * time.Second)
	second := []storedConnection{
		{ID: "a", Upload: 1000, Download: 5000},
		{ID: "b", Upload: 500, Download: 40000, StartedAt: start.Add(4 * time.Second).Format(time.RFC3339)},
	}
	rates.measure("node-1", second, now)
	fresh := rateOf(t, second, "b")
	if !fresh.HasRates {
		t.Fatal("a connection opened inside the interval reported no rate, want its bytes credited to it")
	}
	// Every byte on it moved inside the ten seconds, so the whole total is
	// spread across the interval, not across its own four-second lifetime.
	if !closeEnough(fresh.DownloadRate, 4000) || !closeEnough(fresh.UploadRate, 50) {
		t.Fatalf("rate = ↓%v ↑%v, want ↓4000 ↑50 bytes/s", fresh.DownloadRate, fresh.UploadRate)
	}
	// An idle connection carried over from the previous push stays at zero, so
	// the totals do not double count it.
	idle := rateOf(t, second, "a")
	if !idle.HasRates || !closeEnough(idle.DownloadRate, 0) || !closeEnough(idle.UploadRate, 0) {
		t.Fatalf("idle connection = %+v, want a measured rate of zero", idle)
	}
}

// A connection the master has never seen and that sing-box says predates the
// interval carries bytes from before it. Crediting those to this interval
// would report a spike that never happened.
func TestConnectionRatesIgnoreUnseenConnectionsOlderThanTheInterval(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a"}}, start)

	second := []storedConnection{{ID: "b", Upload: 10_000_000, Download: 90_000_000, StartedAt: start.Add(-time.Hour).Format(time.RFC3339)}}
	rates.measure("node-1", second, start.Add(10*time.Second))
	if second[0].HasRates {
		t.Fatalf("an hour-old connection reported ↓%v, want no rate", second[0].DownloadRate)
	}
}

// Counters that went backwards mean the ID was reused after a restart, not
// that traffic flowed in reverse.
func TestConnectionRatesIgnoreCountersThatWentBackwards(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a", Upload: 9000, Download: 9000}}, start)

	second := []storedConnection{{ID: "a", Upload: 10, Download: 10}}
	rates.measure("node-1", second, start.Add(10*time.Second))
	if second[0].HasRates {
		t.Fatalf("reset counters reported ↓%v, want no rate", second[0].DownloadRate)
	}
}

// A node that went silent long enough to be dropped from the live view comes
// back with counters that ran unobserved. Spreading that difference over the
// silence would describe neither period.
func TestConnectionRatesIgnoreSamplesOlderThanTheStalenessBound(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a", Upload: 1000, Download: 1000}}, start)

	second := []storedConnection{{ID: "a", Upload: 5000, Download: 5000}}
	rates.measure("node-1", second, start.Add(connectionsStaleAfter+time.Second))
	if second[0].HasRates {
		t.Fatalf("a sample across the staleness bound reported ↓%v, want no rate", second[0].DownloadRate)
	}
}

// The node's throughput is these connection rates added up — the overview
// charts that sum, and the connection list shows a subtotal of it — so the
// figure measure returns has to be exactly what the list adds up to. The only
// traffic that may go missing is what a connection carried before closing
// inside the interval, which nothing reports per connection.
func TestConnectionRatesSumToTheNodeThroughput(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	now := start.Add(10 * time.Second)
	rates.measure("node-1", []storedConnection{
		{ID: "a", Upload: 100, Download: 1000},
		{ID: "b", Upload: 50, Download: 500},
	}, start)

	// Between the pushes: "a" moved 10000 down and 1000 up, "b" sat idle, and
	// "c" opened inside the interval carrying 2000 down and 200 up. The node
	// therefore moved 12000 down and 1200 up over the ten seconds.
	second := []storedConnection{
		{ID: "a", Upload: 1100, Download: 11000},
		{ID: "b", Upload: 50, Download: 500},
		{ID: "c", Upload: 200, Download: 2000, StartedAt: start.Add(3 * time.Second).Format(time.RFC3339)},
	}
	nodeDownload, nodeUpload, measured := rates.measure("node-1", second, now)
	if !measured {
		t.Fatal("a second push against a fresh sample reported no rate, want one")
	}

	var download, upload float64
	for _, connection := range second {
		if !connection.HasRates {
			t.Fatalf("connection %q reported no rate, so the list cannot add up", connection.ID)
		}
		download += connection.DownloadRate
		upload += connection.UploadRate
	}
	if !closeEnough(nodeDownload, download) || !closeEnough(nodeUpload, upload) {
		t.Fatalf("node throughput ↓%v ↑%v is not what its connections add up to (↓%v ↑%v)", nodeDownload, nodeUpload, download, upload)
	}
	const wantDownload, wantUpload = 12000.0 / 10, 1200.0 / 10
	if !closeEnough(nodeDownload, wantDownload) || !closeEnough(nodeUpload, wantUpload) {
		t.Fatalf("node throughput = ↓%v ↑%v, want ↓%v ↑%v", nodeDownload, nodeUpload, wantDownload, wantUpload)
	}
}

// An idle node has measured zero, which is a reading rather than an absence:
// two pushes were compared and they found nothing open. Reporting it as "not
// measured" would leave the node out of the fleet total's reporting count and
// make an idle fleet look like one that has not started reporting.
func TestConnectionRatesReportMeasuredZeroForAnIdleNode(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "a", Upload: 1000, Download: 5000}}, start)

	download, upload, measured := rates.measure("node-1", nil, start.Add(10*time.Second))
	if !measured || download != 0 || upload != 0 {
		t.Fatalf("idle node measured ↓%v ↑%v (measured=%v), want a measured zero", download, upload, measured)
	}
}

// The first push says nothing about speed, so the node has no throughput to
// report either — not a zero, which would draw a dip that never happened.
func TestConnectionRatesReportNoThroughputOnFirstPush(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if _, _, measured := rates.measure("node-1", []storedConnection{{ID: "a", Upload: 1000, Download: 5000}}, start); measured {
		t.Fatal("first push reported a node throughput, want none")
	}
}

// Each node is measured against its own previous push; one node's connections
// never stand in for another's.
func TestConnectionRatesKeepNodesApart(t *testing.T) {
	rates := newConnectionRates()
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	rates.measure("node-1", []storedConnection{{ID: "shared", Upload: 1000, Download: 1000}}, start)

	other := []storedConnection{{ID: "shared", Upload: 8000, Download: 8000}}
	rates.measure("node-2", other, start.Add(10*time.Second))
	if other[0].HasRates {
		t.Fatalf("node-2 borrowed node-1's sample and reported ↓%v, want no rate", other[0].DownloadRate)
	}
}
