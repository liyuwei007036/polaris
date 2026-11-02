package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type NetworkProbeRequest struct {
	Host   string `json:"host"`
	Port   uint16 `json:"port,omitempty"`
	Count  int    `json:"count,omitempty"`  // default 5, min 1, max 10
	Method string `json:"method,omitempty"` // "auto", "icmp", "tcp"
}

type NetworkProbeResponse struct {
	OK           bool    `json:"ok"`
	Target       string  `json:"target"`
	Port         uint16  `json:"port,omitempty"`
	IPVersion    int     `json:"ip_version"`
	Method       string  `json:"method"` // "icmp" or "tcp"
	Sent         int     `json:"sent"`
	Received     int     `json:"received"`
	LossPercent  float64 `json:"loss_percent"`
	LatencyMs    int     `json:"latency_ms"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	MinLatencyMs float64 `json:"min_latency_ms"`
	MaxLatencyMs float64 `json:"max_latency_ms"`
	Status       string    `json:"status"` // "ok", "loss", "unreachable"
	Summary      string    `json:"summary"`
	Latencies    []float64 `json:"latencies,omitempty"`
}

var (
	linuxPingStatsRegex  = regexp.MustCompile(`(\d+)\s+packets transmitted,\s+(\d+)\s+(?:packets\s+)?received,.*?(\d+(?:\.\d+)?)%\s+packet loss`)
	linuxPingRttRegex    = regexp.MustCompile(`(?:rtt|round-trip)\s+min/avg/max/(?:mdev|stddev)\s*=\s*([0-9.]+)/([0-9.]+)/([0-9.]+)`)
	winPingStatsRegex    = regexp.MustCompile(`Sent = (\d+),\s*Received = (\d+),\s*Lost = \d+\s*\(([0-9.]+)%\s*loss\)`)
	winPingRttRegex      = regexp.MustCompile(`Minimum = (\d+)ms,\s*Maximum = (\d+)ms,\s*Average = (\d+)ms`)
	pingLineLatencyRegex = regexp.MustCompile(`time[<=]([0-9.]+)\s*ms`)
)

func runNetworkProbe(ctx context.Context, task Task) TaskResult {
	var req NetworkProbeRequest
	if err := json.Unmarshal([]byte(task.Payload), &req); err != nil || strings.TrimSpace(req.Host) == "" {
		return TaskResult{Status: "failed", Summary: "探针参数无效: host 不能为空"}
	}
	req.Host = strings.TrimSpace(req.Host)
	if req.Count <= 0 {
		req.Count = 5
	}
	if req.Count > 10 {
		req.Count = 10
	}

	ipVersion := 4
	if strings.Contains(req.Host, ":") {
		ipVersion = 6
	} else if parsed := net.ParseIP(req.Host); parsed != nil && parsed.To4() == nil {
		ipVersion = 6
	}

	res := NetworkProbeResponse{
		Target:    req.Host,
		Port:      req.Port,
		IPVersion: ipVersion,
		Sent:      req.Count,
	}

	// 1. Unless explicitly requested "tcp", attempt ICMP ping first
	if req.Method != "tcp" {
		icmpRes, ok := executeICMPPing(ctx, req.Host, ipVersion, req.Count)
		if ok && icmpRes.Received > 0 {
			res.OK = true
			res.Method = "icmp"
			res.Sent = icmpRes.Sent
			res.Received = icmpRes.Received
			res.LossPercent = icmpRes.LossPercent
			res.AvgLatencyMs = icmpRes.AvgLatencyMs
			res.MinLatencyMs = icmpRes.MinLatencyMs
			res.MaxLatencyMs = icmpRes.MaxLatencyMs
			res.LatencyMs = int(res.AvgLatencyMs + 0.5)
			res.Latencies = icmpRes.Latencies
			if res.LossPercent == 0 {
				res.Status = "ok"
				res.Summary = fmt.Sprintf("ICMP 连通良好 (延迟: %.1fms, 零丢包)", res.AvgLatencyMs)
			} else {
				res.Status = "loss"
				res.Summary = fmt.Sprintf("ICMP 存在丢包 (延迟: %.1fms, 丢包率: %.1f%%)", res.AvgLatencyMs, res.LossPercent)
			}
			resJSON, _ := json.Marshal(res)
			return TaskResult{Status: "succeeded", Summary: res.Summary, Data: string(resJSON)}
		}
	}

	// 2. If ICMP receives 0 packets or method == "tcp", use multi-packet TCP dial probe
	targetPort := req.Port
	if targetPort == 0 {
		targetPort = 443
	}
	res.Port = targetPort
	res.Method = "tcp"

	tcpRes := executeTCPProbe(ctx, req.Host, targetPort, ipVersion, req.Count)
	res.Sent = tcpRes.Sent
	res.Received = tcpRes.Received
	res.LossPercent = tcpRes.LossPercent
	res.AvgLatencyMs = tcpRes.AvgLatencyMs
	res.MinLatencyMs = tcpRes.MinLatencyMs
	res.MaxLatencyMs = tcpRes.MaxLatencyMs
	res.LatencyMs = int(res.AvgLatencyMs + 0.5)
	res.Latencies = tcpRes.Latencies

	if res.Received > 0 {
		res.OK = true
		if res.LossPercent == 0 {
			res.Status = "ok"
			res.Summary = fmt.Sprintf("TCP 连通良好 (端口:%d, 延迟: %.1fms, 零丢包)", targetPort, res.AvgLatencyMs)
		} else {
			res.Status = "loss"
			res.Summary = fmt.Sprintf("TCP 存在丢包 (端口:%d, 延迟: %.1fms, 丢包率: %.1f%%)", targetPort, res.AvgLatencyMs, res.LossPercent)
		}
		resJSON, _ := json.Marshal(res)
		return TaskResult{Status: "succeeded", Summary: res.Summary, Data: string(resJSON)}
	}

	res.OK = false
	res.Status = "unreachable"
	res.LossPercent = 100.0
	res.Summary = fmt.Sprintf("目标不可达 (已发送 %d 次探测均超时或无响应)", req.Count)
	resJSON, _ := json.Marshal(res)
	return TaskResult{Status: "succeeded", Summary: res.Summary, Data: string(resJSON)}
}

type probeStats struct {
	Sent         int
	Received     int
	LossPercent  float64
	AvgLatencyMs float64
	MinLatencyMs float64
	MaxLatencyMs float64
	Latencies    []float64
}

func executeICMPPing(ctx context.Context, host string, ipVersion int, count int) (probeStats, bool) {
	var stats probeStats
	timeout := time.Duration(count+1) * time.Second
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		args := []string{"-n", strconv.Itoa(count), "-w", "1000", host}
		if ipVersion == 6 {
			args = append([]string{"-6"}, args...)
		} else {
			args = append([]string{"-4"}, args...)
		}
		cmd = exec.CommandContext(pingCtx, "ping", args...)
	} else {
		// Linux / macOS
		countStr := strconv.Itoa(count)
		if ipVersion == 6 {
			cmd = exec.CommandContext(pingCtx, "ping", "-6", "-c", countStr, "-i", "0.2", "-W", "1", host)
		} else {
			cmd = exec.CommandContext(pingCtx, "ping", "-c", countStr, "-i", "0.2", "-W", "1", host)
		}
	}

	outBytes, err := cmd.CombinedOutput()
	output := string(outBytes)
	if err != nil && len(output) == 0 {
		return stats, false
	}

	return parsePingOutput(output, count)
}

func parsePingOutput(output string, expectedCount int) (probeStats, bool) {
	var stats probeStats
	stats.Sent = expectedCount

	for _, m := range pingLineLatencyRegex.FindAllStringSubmatch(output, -1) {
		if len(m) == 2 {
			if lat, err := strconv.ParseFloat(m[1], 64); err == nil {
				stats.Latencies = append(stats.Latencies, lat)
			}
		}
	}

	// Check Linux format
	if match := linuxPingStatsRegex.FindStringSubmatch(output); len(match) == 4 {
		stats.Sent, _ = strconv.Atoi(match[1])
		stats.Received, _ = strconv.Atoi(match[2])
		stats.LossPercent, _ = strconv.ParseFloat(match[3], 64)

		if rttMatch := linuxPingRttRegex.FindStringSubmatch(output); len(rttMatch) == 4 {
			stats.MinLatencyMs, _ = strconv.ParseFloat(rttMatch[1], 64)
			stats.AvgLatencyMs, _ = strconv.ParseFloat(rttMatch[2], 64)
			stats.MaxLatencyMs, _ = strconv.ParseFloat(rttMatch[3], 64)
		}
		return stats, stats.Received > 0
	}

	// Check Windows format
	if match := winPingStatsRegex.FindStringSubmatch(output); len(match) == 4 {
		stats.Sent, _ = strconv.Atoi(match[1])
		stats.Received, _ = strconv.Atoi(match[2])
		stats.LossPercent, _ = strconv.ParseFloat(match[3], 64)

		if rttMatch := winPingRttRegex.FindStringSubmatch(output); len(rttMatch) == 4 {
			stats.MinLatencyMs, _ = strconv.ParseFloat(rttMatch[1], 64)
			stats.MaxLatencyMs, _ = strconv.ParseFloat(rttMatch[2], 64)
			stats.AvgLatencyMs, _ = strconv.ParseFloat(rttMatch[3], 64)
		}
		return stats, stats.Received > 0
	}

	return stats, false
}

func isTCPRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refused") || strings.Contains(msg, "reset by peer")
}

func executeTCPProbe(ctx context.Context, host string, port uint16, ipVersion int, count int) probeStats {
	var stats probeStats
	stats.Sent = count
	if count <= 0 {
		stats.Sent = 5
		count = 5
	}

	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	network := "tcp"
	if ipVersion == 6 {
		network = "tcp6"
	} else if ipVersion == 4 {
		network = "tcp4"
	}

	dialer := net.Dialer{Timeout: 800 * time.Millisecond}
	var latencies []float64

	for i := 0; i < count; i++ {
		if ctx.Err() != nil {
			break
		}

		start := time.Now()
		conn, err := dialer.DialContext(ctx, network, address)
		elapsedMs := float64(time.Since(start).Microseconds()) / 1000.0
		if err == nil {
			_ = conn.Close()
			latencies = append(latencies, elapsedMs)
		} else if isTCPRefused(err) {
			// Remote TCP stack responded with RST (connection refused), meaning the packet
			// reached the target host and returned within 1 round-trip time.
			latencies = append(latencies, elapsedMs)
		}
		if i < count-1 {
			time.Sleep(20 * time.Millisecond)
		}
	}

	stats.Received = len(latencies)
	stats.LossPercent = float64(stats.Sent-stats.Received) * 100.0 / float64(stats.Sent)
	stats.Latencies = latencies

	if len(latencies) > 0 {
		minLat := latencies[0]
		maxLat := latencies[0]
		sumLat := 0.0
		for _, lat := range latencies {
			if lat < minLat {
				minLat = lat
			}
			if lat > maxLat {
				maxLat = lat
			}
			sumLat += lat
		}
		stats.MinLatencyMs = minLat
		stats.MaxLatencyMs = maxLat
		stats.AvgLatencyMs = sumLat / float64(len(latencies))
	}

	return stats
}
