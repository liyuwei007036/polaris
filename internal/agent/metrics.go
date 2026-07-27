package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// clashAPIBase is the loopback-only sing-box Clash API published by compiled
// configurations. It is never reachable from outside the node.
const clashAPIBase = "http://127.0.0.1:9090"

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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if connections, err := collectConnections(ctx); err == nil {
		report.Connections = connections
		report.Capabilities["connection"] = MetricCapability{CumulativeTraffic: true, InstantRate: false, ConnectionCount: true, Source: "sing-box clash_api " + clashAPIBase, Precision: "per_connection"}
	}
	report.Fail2Ban = CollectFail2BanStatus(ctx)
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

// CollectConnections independently polls the local Clash API for the current
// connection list. It is used by the fast real-time push loop, which runs on
// its own short interval decoupled from the slower heartbeat.
func CollectConnections(ctx context.Context) ([]ConnectionInfo, error) {
	return collectConnections(ctx)
}

// collectConnections reads current connections from the loopback sing-box
// Clash API. Every value is copied verbatim; absent fields stay empty.
func collectConnections(ctx context.Context) ([]ConnectionInfo, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clashAPIBase+"/connections", nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash API returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				Network         string `json:"network"`
				Type            string `json:"type"`
				SourceIP        string `json:"sourceIP"`
				SourcePort      string `json:"sourcePort"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Host            string `json:"host"`
			} `json:"metadata"`
			Upload   int64    `json:"upload"`
			Download int64    `json:"download"`
			Start    string   `json:"start"`
			Chains   []string `json:"chains"`
			Rule     string   `json:"rule"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8*1024*1024)).Decode(&payload); err != nil {
		return nil, err
	}
	const maximumConnections = 1000
	connections := make([]ConnectionInfo, 0, len(payload.Connections))
	for index, connection := range payload.Connections {
		if index == maximumConnections {
			break
		}
		info := ConnectionInfo{
			ID:       connection.ID,
			Inbound:  connection.Metadata.Type,
			Network:  connection.Metadata.Network,
			Host:     connection.Metadata.Host,
			Upload:   connection.Upload,
			Download: connection.Download,
			StartedAt: connection.Start,
			Rule:     connection.Rule,
		}
		if connection.Metadata.SourceIP != "" {
			info.Source = connection.Metadata.SourceIP + ":" + connection.Metadata.SourcePort
		}
		if connection.Metadata.DestinationIP != "" || connection.Metadata.DestinationPort != "" {
			info.Destination = connection.Metadata.DestinationIP + ":" + connection.Metadata.DestinationPort
		}
		if len(connection.Chains) > 0 {
			info.Outbound = connection.Chains[len(connection.Chains)-1]
		}
		connections = append(connections, info)
	}
	return connections, nil
}
