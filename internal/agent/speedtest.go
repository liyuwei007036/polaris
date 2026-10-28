package agent

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

type SpeedtestResult struct {
	TelecomLatencyMs int     `json:"telecom_latency_ms"`
	TelecomSpeedMbps float64 `json:"telecom_speed_mbps,omitempty"`
	TelecomRoute     string  `json:"telecom_route"`
	UnicomLatencyMs  int     `json:"unicom_latency_ms"`
	UnicomSpeedMbps  float64 `json:"unicom_speed_mbps,omitempty"`
	UnicomRoute      string  `json:"unicom_route"`
	MobileLatencyMs  int     `json:"mobile_latency_ms"`
	MobileSpeedMbps  float64 `json:"mobile_speed_mbps,omitempty"`
	MobileRoute      string  `json:"mobile_route"`
}

// 权威三网骨干测试节点 (与开源工具 backtrace / nexttrace 标准对齐)
// 电信: 上海电信核心 (202.96.209.133), 广州电信 (202.96.128.86), 成都电信 (61.139.2.69)
// 联通: 上海联通核心 (210.22.97.1), 广州联通 (210.21.196.6), 北京联通 (202.106.50.1)
// 移动: 广州移动核心 (120.196.165.24), 北京移动 (221.179.155.161), 成都移动 (211.137.96.205)
type TargetEndpoint struct {
	City string
	Host string
}

var (
	TelecomEndpoints = []TargetEndpoint{
		{City: "上海电信", Host: "202.96.209.133"},
		{City: "广州电信", Host: "202.96.128.86"},
		{City: "成都电信", Host: "61.139.2.69"},
	}
	UnicomEndpoints = []TargetEndpoint{
		{City: "上海联通", Host: "210.22.97.1"},
		{City: "广州联通", Host: "210.21.196.6"},
		{City: "北京联通", Host: "202.106.50.1"},
	}
	MobileEndpoints = []TargetEndpoint{
		{City: "广州移动", Host: "120.196.165.24"},
		{City: "北京移动", Host: "221.179.155.161"},
		{City: "成都移动", Host: "211.137.96.205"},
	}
)

func parsePingLatency(output string) int {
	// 匹配 Linux rtt 统计: rtt min/avg/max/mdev = 141.800/142.150/142.500/0.350 ms
	reRtt := regexp.MustCompile(`(?i)rtt\s+(?:min/avg/max/mdev|rtt)\s*=\s*[0-9.]+/([0-9.]+)/`)
	if match := reRtt.FindStringSubmatch(output); len(match) > 1 {
		if f, err := strconv.ParseFloat(match[1], 64); err == nil && f > 0 {
			return int(math.Round(f))
		}
	}

	// 匹配 Windows 统计: Average = 137ms 或 平均 = 11ms
	reWinAvg := regexp.MustCompile(`(?i)(?:average|平均)\s*=\s*([0-9]+(?:\.[0-9]+)?)`)
	if match := reWinAvg.FindStringSubmatch(output); len(match) > 1 {
		if f, err := strconv.ParseFloat(match[1], 64); err == nil && f > 0 {
			return int(math.Round(f))
		}
	}

	// 匹配单包响应耗时: time=210.4ms 或 time<1ms
	reTime := regexp.MustCompile(`(?i)time[=<]([0-9]+(?:\.[0-9]+)?)\s*ms`)
	matches := reTime.FindAllStringSubmatch(output, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		if f, err := strconv.ParseFloat(last[1], 64); err == nil && f > 0 {
			return int(math.Round(f))
		}
	}
	return 0
}

func measurePingLatency(ctx context.Context, host string) int {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "ping", "-n", "2", "-w", "1500", host)
	} else {
		cmd = exec.CommandContext(ctx, "ping", "-c", "2", "-W", "2", host)
	}
	out, err := cmd.CombinedOutput()
	if err == nil || len(out) > 0 {
		if ms := parsePingLatency(string(out)); ms > 0 {
			return ms
		}
	}
	return 0
}

func measureTCPLatency(ctx context.Context, host string) int {
	ports := []int{53, 80}
	for _, port := range ports {
		dialer := net.Dialer{Timeout: 1500 * time.Millisecond}
		started := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			_ = conn.Close()
			ms := int(time.Since(started).Milliseconds())
			if ms <= 0 {
				return 1
			}
			return ms
		}
	}
	return 0
}

// measureLatency 优先使用标准 ICMP Ping，无法 Ping 时回退 TCP 探测
func measureLatency(ctx context.Context, host string) int {
	if pingMs := measurePingLatency(ctx, host); pingMs > 0 {
		return pingMs
	}
	return measureTCPLatency(ctx, host)
}

type traceRouteResult struct {
	Hops      []string
	LatencyMs int
}

// traceRouteHops extracts intermediate IP hops towards the target using traceroute or tracepath.
func traceRoute(ctx context.Context, targetHost string) traceRouteResult {
	if !commandExists("traceroute") && !commandExists("tracepath") {
		_ = installPackages(ctx, "traceroute")
	}

	var out []byte
	var err error

	if commandExists("traceroute") {
		// 1. Try ICMP traceroute (-I) with fast options (-n -m 20 -q 1 -w 1)
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, err = exec.CommandContext(traceCtx, "traceroute", "-I", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).Output()
		cancel()

		// 2. If ICMP failed (e.g. non-root or firewall), try standard UDP traceroute
		if err != nil || len(out) == 0 {
			traceCtx2, cancel2 := context.WithTimeout(ctx, 6*time.Second)
			out, err = exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).Output()
			cancel2()
		}

		// 3. If standard UDP failed, try TCP SYN traceroute (-T -p 80)
		if err != nil || len(out) == 0 {
			traceCtx3, cancel3 := context.WithTimeout(ctx, 6*time.Second)
			out, err = exec.CommandContext(traceCtx3, "traceroute", "-T", "-p", "80", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).Output()
			cancel3()
		}
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		out, err = exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "20", targetHost).Output()
		cancel()
	}

	if err != nil || len(out) == 0 {
		return traceRouteResult{}
	}

	outStr := string(out)
	reIP := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	hops := reIP.FindAllString(outStr, -1)

	// 提取 traceroute 响应中末跳 RTT 耗时
	reHopRTT := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*ms`)
	rttMatches := reHopRTT.FindAllStringSubmatch(outStr, -1)
	latency := 0
	if len(rttMatches) > 0 {
		lastMatch := rttMatches[len(rttMatches)-1]
		if f, err := strconv.ParseFloat(lastMatch[1], 64); err == nil && f > 0 {
			latency = int(f)
		}
	}

	return traceRouteResult{
		Hops:      hops,
		LatencyMs: latency,
	}
}

func analyzeTelecomRoute(hops []string, latencyMs int) string {
	return wire.AnalyzeTelecomRoute(hops, latencyMs)
}

func analyzeUnicomRoute(hops []string, latencyMs int) string {
	return wire.AnalyzeUnicomRoute(hops, latencyMs)
}

func analyzeMobileRoute(hops []string, latencyMs int) string {
	return wire.AnalyzeMobileRoute(hops, latencyMs)
}

func runSpeedtest(ctx context.Context, task Task) TaskResult {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var telecomLatency, unicomLatency, mobileLatency int
	var latencyWg sync.WaitGroup
	latencyWg.Add(3)

	go func() {
		defer latencyWg.Done()
		for _, ep := range TelecomEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				telecomLatency = l
				return
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range UnicomEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				unicomLatency = l
				return
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range MobileEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				mobileLatency = l
				return
			}
		}
	}()

	latencyWg.Wait()

	// 并发执行三大运营商路由追踪
	var telecomHops, unicomHops, mobileHops []string
	var traceWg sync.WaitGroup
	traceWg.Add(3)

	go func() {
		defer traceWg.Done()
		for _, ep := range TelecomEndpoints {
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				telecomHops = res.Hops
				if telecomLatency <= 0 && res.LatencyMs > 0 {
					telecomLatency = res.LatencyMs
				}
				return
			}
		}
	}()

	go func() {
		defer traceWg.Done()
		for _, ep := range UnicomEndpoints {
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				unicomHops = res.Hops
				if unicomLatency <= 0 && res.LatencyMs > 0 {
					unicomLatency = res.LatencyMs
				}
				return
			}
		}
	}()

	go func() {
		defer traceWg.Done()
		for _, ep := range MobileEndpoints {
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				mobileHops = res.Hops
				if mobileLatency <= 0 && res.LatencyMs > 0 {
					mobileLatency = res.LatencyMs
				}
				return
			}
		}
	}()

	traceWg.Wait()

	telecomRoute := analyzeTelecomRoute(telecomHops, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, mobileLatency)

	result := SpeedtestResult{
		TelecomLatencyMs: telecomLatency,
		TelecomRoute:     telecomRoute,
		UnicomLatencyMs:  unicomLatency,
		UnicomRoute:      unicomRoute,
		MobileLatencyMs:  mobileLatency,
		MobileRoute:      mobileRoute,
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码检测数据失败：" + err.Error()}
	}

	return TaskResult{
		Status:  "succeeded",
		Summary: "三网回程延迟与线路检测完成",
		Data:    string(encoded),
	}
}
