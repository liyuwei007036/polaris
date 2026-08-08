package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// clashAPIBase is the loopback-only sing-box Clash API published by compiled
// configurations. It is never reachable from outside the node. The E2E suite
// redirects only its spawned agent to a deterministic local test endpoint.
var clashAPIBase = func() string {
	if value := strings.TrimRight(strings.TrimSpace(os.Getenv("POLARIS_E2E_CLASH_API_URL")), "/"); value != "" {
		return value
	}
	return "http://127.0.0.1:9090"
}()

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
		Health: NodeHealth{Status: "degraded", SingBoxService: "unavailable"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	report.Health.SingBoxService = collectSingBoxServiceHealth(ctx)
	if connections, traffic, err := collectConnectionsAndTraffic(ctx); err == nil {
		report.Connections = connections
		report.Capabilities["connection"] = MetricCapability{CumulativeTraffic: true, InstantRate: false, ConnectionCount: true, Source: "sing-box clash_api " + clashAPIBase, Precision: "per_connection"}
		report.Health.ClashAPIAvailable = true
		if traffic.Available {
			// Proxied traffic, not host interface totals: this is the number
			// that stays at zero while nothing is being proxied.
			report.Proxy = map[string]uint64{"received_bytes": traffic.ReceivedBytes, "sent_bytes": traffic.SentBytes}
		}
	}
	report.Fail2Ban = CollectFail2BanStatus(ctx)
	report.Firewall = CollectFirewallStatus(ctx)
	received, sent, ok := NodeTrafficCounters()
	if !ok {
		return report
	}
	report.Node = map[string]uint64{"received_bytes": received, "sent_bytes": sent}
	report.Capabilities["node"] = MetricCapability{CumulativeTraffic: true, InstantRate: true, ConnectionCount: false, Source: "/proc/net/dev non-loopback aggregate", Precision: "host_total"}
	report.Health.TrafficAvailable = true
	switch {
	case report.Health.SingBoxService == "active" && report.Health.ClashAPIAvailable:
		report.Health.Status = "healthy"
	case report.Health.SingBoxService == "inactive":
		report.Health.Status = "stopped"
		report.Health.Message = "连接服务未运行"
	case report.Health.SingBoxService == "active" && !report.Health.ClashAPIAvailable:
		report.Health.Status = "degraded"
		report.Health.Message = "连接数据接口异常"
	default:
		report.Health.Status = "degraded"
		report.Health.Message = "部分运行数据暂时无法获取"
	}
	return report
}

// NodeTrafficCounters reads the host's cumulative non-loopback interface
// byte counters. ok is false when /proc/net/dev cannot be read at all.
func NodeTrafficCounters() (received, sent uint64, ok bool) {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == "lo" {
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
	return received, sent, true
}

// TrafficSampler turns the host's cumulative counters into an instantaneous
// rate. The agent samples on its own fixed push interval, so it is the only
// side that can measure a real rate; the master and browser just display it.
type TrafficSampler struct {
	previousReceived uint64
	previousSent     uint64
	previousAt       time.Time
	seeded           bool
}

// Sample records the counters observed at now and reports the rate since the
// previous sample. hasRate is false for the first sample, and whenever the
// counters went backwards (interface reset) so a bogus spike is never shown.
func (s *TrafficSampler) Sample(received, sent uint64, now time.Time) (receivedRate, sentRate float64, hasRate bool) {
	previousReceived, previousSent, previousAt, seeded := s.previousReceived, s.previousSent, s.previousAt, s.seeded
	s.previousReceived, s.previousSent, s.previousAt, s.seeded = received, sent, now, true
	if !seeded || !now.After(previousAt) || received < previousReceived || sent < previousSent {
		return 0, 0, false
	}
	seconds := now.Sub(previousAt).Seconds()
	return float64(received-previousReceived) / seconds, float64(sent-previousSent) / seconds, true
}

func collectSingBoxServiceHealth(ctx context.Context) string {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "unavailable"
	}
	output, err := exec.CommandContext(ctx, "systemctl", "is-active", "sing-box").Output()
	if err != nil {
		return "inactive"
	}
	if strings.TrimSpace(string(output)) == "active" {
		return "active"
	}
	return "inactive"
}

// ProxyTraffic is sing-box's own cumulative byte count for traffic it
// carried. It is what an operator means by "traffic": the host interface
// counters also include SSH, package updates and everything else on the box,
// which is why a node with no connections at all still appeared to be
// pushing data.
type ProxyTraffic struct {
	ReceivedBytes uint64
	SentBytes     uint64
	Available     bool
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// CollectConnections independently polls the local Clash API for the current
// connection list. It is used by the fast real-time push loop, which runs on
// its own short interval decoupled from the slower heartbeat.
func CollectConnections(ctx context.Context) ([]ConnectionInfo, error) {
	connections, _, err := collectConnectionsAndTraffic(ctx)
	return connections, err
}

// CollectConnectionsAndTraffic returns the open connections together with
// sing-box's cumulative proxied byte counts from the same single request.
func CollectConnectionsAndTraffic(ctx context.Context) ([]ConnectionInfo, ProxyTraffic, error) {
	return collectConnectionsAndTraffic(ctx)
}

// collectConnectionsAndTraffic reads the current connections and sing-box's
// cumulative proxied byte counts from the loopback Clash API. Every value is
// copied verbatim; absent fields stay empty.
func collectConnectionsAndTraffic(ctx context.Context) ([]ConnectionInfo, ProxyTraffic, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, clashAPIBase+"/connections", nil)
	if err != nil {
		return nil, ProxyTraffic{}, err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return nil, ProxyTraffic{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ProxyTraffic{}, fmt.Errorf("clash API returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		DownloadTotal uint64 `json:"downloadTotal"`
		UploadTotal   uint64 `json:"uploadTotal"`
		Connections   []struct {
			ID       string `json:"id"`
			Metadata struct {
				Network         string `json:"network"`
				Type            string `json:"type"`
				SourceIP        string `json:"sourceIP"`
				SourcePort      string `json:"sourcePort"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Host            string `json:"host"`
				// sing-box names the authenticated inbound account
				// inboundUser; Clash-compatible builds call it user.
				InboundUser string `json:"inboundUser"`
				User        string `json:"user"`
			} `json:"metadata"`
			Upload      int64    `json:"upload"`
			Download    int64    `json:"download"`
			Start       string   `json:"start"`
			Chains      []string `json:"chains"`
			Rule        string   `json:"rule"`
			RulePayload string   `json:"rulePayload"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8*1024*1024)).Decode(&payload); err != nil {
		return nil, ProxyTraffic{}, err
	}
	traffic := ProxyTraffic{ReceivedBytes: payload.DownloadTotal, SentBytes: payload.UploadTotal, Available: true}
	const maximumConnections = 1000
	connections := make([]ConnectionInfo, 0, len(payload.Connections))
	for index, connection := range payload.Connections {
		if index == maximumConnections {
			break
		}
		info := ConnectionInfo{
			ID:          connection.ID,
			Inbound:     connection.Metadata.Type,
			Network:     connection.Metadata.Network,
			Host:        connection.Metadata.Host,
			Upload:      connection.Upload,
			Download:    connection.Download,
			StartedAt:   connection.Start,
			Rule:        connection.Rule,
			RulePayload: connection.RulePayload,
			Chains:      append([]string(nil), connection.Chains...),
			User:        firstNonEmpty(connection.Metadata.InboundUser, connection.Metadata.User),
		}
		if connection.Metadata.SourceIP != "" {
			info.Source = net.JoinHostPort(connection.Metadata.SourceIP, connection.Metadata.SourcePort)
		}
		if connection.Metadata.DestinationIP != "" || connection.Metadata.DestinationPort != "" {
			info.Destination = net.JoinHostPort(connection.Metadata.DestinationIP, connection.Metadata.DestinationPort)
		}
		// chains only ever lists outbounds, with index 0 the one that actually
		// carried the traffic. The inbound is reported separately, inside
		// metadata.type, written as "<inbound type>/<inbound tag>".
		if len(connection.Chains) > 0 {
			info.Outbound = connection.Chains[0]
		}
		if _, tag, found := strings.Cut(connection.Metadata.Type, "/"); found {
			info.InboundTag = tag
		}
		connections = append(connections, info)
	}
	return connections, traffic, nil
}
