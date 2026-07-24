package agent

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// CollectMetrics reports only counters that the agent can read directly. The
// /proc/net/dev values are host interface totals, not per-listener values.
func CollectMetrics() MetricReport {
	report := MetricReport{
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Capabilities: map[string]MetricCapability{
			"node":       {CumulativeTraffic: false, InstantRate: false, ConnectionCount: false, Source: "unavailable", Precision: "unavailable"},
			"listener":   {CumulativeTraffic: false, InstantRate: false, ConnectionCount: false, Source: "sing-box API not configured", Precision: "unavailable"},
			"endpoint":   {CumulativeTraffic: false, InstantRate: false, ConnectionCount: false, Source: "sing-box API not configured", Precision: "unavailable"},
			"connection": {CumulativeTraffic: false, InstantRate: false, ConnectionCount: false, Source: "sing-box API not configured", Precision: "unavailable"},
		},
	}
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return report
	}
	defer file.Close()
	var received, sent uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		rx, rxErr := strconv.ParseUint(fields[0], 10, 64)
		tx, txErr := strconv.ParseUint(fields[8], 10, 64)
		if rxErr != nil || txErr != nil {
			continue
		}
		received += rx
		sent += tx
	}
	report.Node = map[string]uint64{"received_bytes": received, "sent_bytes": sent}
	report.Capabilities["node"] = MetricCapability{CumulativeTraffic: true, InstantRate: false, ConnectionCount: false, Source: "/proc/net/dev non-loopback aggregate", Precision: "host_total"}
	return report
}
