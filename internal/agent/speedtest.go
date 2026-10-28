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
	"strings"
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

// 权威三网骨干测试节点 (与开源工具 backtrace / nexttrace / zhanghanyun 行业标杆对齐)
// 电信: 上海电信核心 (202.96.209.133), 广州电信 (58.60.188.222), 北京电信 (219.141.140.10), 成都电信 (61.139.2.69)
// 联通: 上海联通核心 (210.22.97.1), 广州联通 (210.21.196.6), 北京联通 (202.106.195.68), 成都联通 (119.6.6.6)
// 移动: 广州移动核心 (120.196.165.24), 上海移动 (211.136.112.200), 北京移动 (221.179.155.161), 成都移动 (211.137.96.205)
type TargetEndpoint struct {
	City string
	Host string
}

var (
	TelecomEndpoints = []TargetEndpoint{
		{City: "上海电信", Host: "202.96.209.133"},
		{City: "广州电信", Host: "58.60.188.222"},
		{City: "北京电信", Host: "219.141.140.10"},
		{City: "成都电信", Host: "61.139.2.69"},
	}
	UnicomEndpoints = []TargetEndpoint{
		{City: "上海联通", Host: "210.22.97.1"},
		{City: "广州联通", Host: "210.21.196.6"},
		{City: "北京联通", Host: "202.106.195.68"},
		{City: "成都联通", Host: "119.6.6.6"},
	}
	MobileEndpoints = []TargetEndpoint{
		{City: "广州移动", Host: "120.196.165.24"},
		{City: "上海移动", Host: "211.136.112.200"},
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

// inspectLocalRouteQuery uses `ip route get <target>` on Linux to discover
// the direct next-hop gateway and source interface IP without relying on external traceroute.
func inspectLocalRouteQuery(ctx context.Context, targetHost string) []string {
	if runtime.GOOS != "linux" || !commandExists("ip") {
		return nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	out, _ := exec.CommandContext(cmdCtx, "ip", "route", "get", targetHost).CombinedOutput()
	if len(out) == 0 {
		return nil
	}
	var hops []string
	reVia := regexp.MustCompile(`via\s+(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})`)
	if m := reVia.FindStringSubmatch(string(out)); len(m) > 1 {
		hops = append(hops, m[1])
	}
	reSrc := regexp.MustCompile(`src\s+(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})`)
	if m := reSrc.FindStringSubmatch(string(out)); len(m) > 1 {
		hops = append(hops, m[1])
	}
	return hops
}

// extractHopsFromOutput parses traceroute / tracert output line-by-line.
// It strictly ignores the header line (preventing target IP from polluting hop 1)
// and extracts intermediate IP hops and the final RTT latency.
func extractHopsFromOutput(targetHost, output string) ([]string, int) {
	var hops []string
	latency := 0
	reHopLine := regexp.MustCompile(`^\s*(\d{1,2})\s+(.+)`)
	reIP := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reHopRTT := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*ms`)

	lines := strings.Split(output, "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		// Skip header line (e.g. "traceroute to 210.22.97.1 ...", "通过最多 30 个跃点跟踪到 ...")
		if strings.HasPrefix(strings.ToLower(line), "traceroute to") ||
			strings.Contains(line, "跃点跟踪到") ||
			strings.HasPrefix(strings.ToLower(line), "tracing route to") {
			continue
		}

		match := reHopLine.FindStringSubmatch(rawLine)
		if len(match) > 2 {
			hopContent := match[2]
			ips := reIP.FindAllString(hopContent, -1)
			for _, ip := range ips {
				// Don't append if it's identical to the target destination as hop 1
				if len(hops) == 0 && ip == targetHost {
					continue
				}
				hops = append(hops, ip)
			}

			// Check for text tokens in line (AS9929, CUII, CN2, CMIN2, CUG)
			lower := strings.ToLower(hopContent)
			if strings.Contains(lower, "9929") || strings.Contains(lower, "cuii") {
				hops = append(hops, "9929")
			}
			if strings.Contains(lower, "10099") || strings.Contains(lower, "cug") {
				hops = append(hops, "10099")
			}
			if strings.Contains(lower, "59.43.") || strings.Contains(lower, "cn2") {
				hops = append(hops, "59.43.0.1")
			}
			if strings.Contains(lower, "cmin2") || strings.Contains(lower, "58807") {
				hops = append(hops, "58807")
			}

			// Extract RTT from this hop
			if rtts := reHopRTT.FindAllStringSubmatch(hopContent, -1); len(rtts) > 0 {
				last := rtts[len(rtts)-1]
				if f, err := strconv.ParseFloat(last[1], 64); err == nil && f > 0 {
					latency = int(f)
				}
			}
		}
	}

	return hops, latency
}

// traceRoute extracts intermediate IP hops towards the target using traceroute or tracepath.
// It uses CombinedOutput and never discards captured hops due to non-zero exit codes.
func traceRoute(ctx context.Context, targetHost string) traceRouteResult {
	if runtime.GOOS == "linux" && !commandExists("traceroute") && !commandExists("tracepath") {
		if commandExists("apt-get") {
			installCtx, installCancel := context.WithTimeout(ctx, 4*time.Second)
			_ = exec.CommandContext(installCtx, "apt-get", "install", "-y", "--no-install-recommends", "traceroute").Run()
			installCancel()
		}
	}

	var outStr string

	if runtime.GOOS == "windows" {
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracert", "-d", "-h", "20", "-w", "500", targetHost).CombinedOutput()
		cancel()
		outStr = string(out)
	} else if commandExists("traceroute") {
		// 1. Try ICMP traceroute (-I) with 20 max hops (-m 20 -q 1 -w 1)
		traceCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		out, _ := exec.CommandContext(traceCtx, "traceroute", "-I", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).CombinedOutput()
		cancel()

		hops, lat := extractHopsFromOutput(targetHost, string(out))
		if len(hops) > 0 {
			outStr = string(out)
		} else {
			// 2. If ICMP produced 0 hops (e.g. non-root or firewall drop), try standard UDP traceroute
			traceCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
			out2, _ := exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).CombinedOutput()
			cancel2()

			hops2, _ := extractHopsFromOutput(targetHost, string(out2))
			if len(hops2) > 0 {
				outStr = string(out2)
			} else {
				// 3. Fallback to TCP SYN traceroute (-T -p 80)
				traceCtx3, cancel3 := context.WithTimeout(ctx, 5*time.Second)
				out3, _ := exec.CommandContext(traceCtx3, "traceroute", "-T", "-p", "80", "-n", "-m", "20", "-q", "1", "-w", "1", targetHost).CombinedOutput()
				cancel3()
				outStr = string(out3)
			}
		}
		_ = lat
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "20", targetHost).CombinedOutput()
		cancel()
		outStr = string(out)
	}

	hops, latency := extractHopsFromOutput(targetHost, outStr)

	// Combine local kernel routing table evidence
	if localHops := inspectLocalRouteQuery(ctx, targetHost); len(localHops) > 0 {
		hops = append(hops, localHops...)
	}

	lowerOut := strings.ToLower(outStr)
	if strings.Contains(lowerOut, "9929") || strings.Contains(lowerOut, "cuii") {
		hops = append(hops, "9929")
	}
	if strings.Contains(lowerOut, "10099") || strings.Contains(lowerOut, "cug") {
		hops = append(hops, "10099")
	}
	if strings.Contains(lowerOut, "59.43.") || strings.Contains(lowerOut, "cn2") {
		hops = append(hops, "59.43.0.1")
	}
	if strings.Contains(lowerOut, "cmin2") || strings.Contains(lowerOut, "58807") {
		hops = append(hops, "58807")
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
	timeoutCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	var telecomLatency, unicomLatency, mobileLatency int
	var latencyWg sync.WaitGroup
	latencyWg.Add(3)

	go func() {
		defer latencyWg.Done()
		for _, ep := range TelecomEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				if telecomLatency <= 0 || l < telecomLatency {
					telecomLatency = l
				}
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range UnicomEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				if unicomLatency <= 0 || l < unicomLatency {
					unicomLatency = l
				}
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range MobileEndpoints {
			l := measureLatency(timeoutCtx, ep.Host)
			if l > 0 {
				if mobileLatency <= 0 || l < mobileLatency {
					mobileLatency = l
				}
			}
		}
	}()

	latencyWg.Wait()

	// 并发执行三大运营商路由追踪 (采用多端点联合证据判定，杜绝单点超时漏判 9929 / CN2)
	var telecomHops, unicomHops, mobileHops []string
	var traceWg sync.WaitGroup
	traceWg.Add(3)

	// 电信路由探测 (先测核心上海节点，若已确认精品线路则提前结束，否则测试广州、北京节点)
	go func() {
		defer traceWg.Done()
		for i, ep := range TelecomEndpoints {
			if i >= 3 && len(telecomHops) > 0 {
				break
			}
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				telecomHops = append(telecomHops, res.Hops...)
				if telecomLatency <= 0 && res.LatencyMs > 0 {
					telecomLatency = res.LatencyMs
				}
				rName := wire.AnalyzeTelecomRoute(telecomHops, telecomLatency)
				if rName == "电信 CN2 GIA" || rName == "电信 CN2 GT" || rName == "电信 CTGNet" {
					return
				}
			}
		}
	}()

	// 联通路由探测 (先测核心上海节点，若已确认 9929/CUG 精品线路则提前结束，否则测试广州、北京节点)
	go func() {
		defer traceWg.Done()
		for i, ep := range UnicomEndpoints {
			if i >= 3 && len(unicomHops) > 0 {
				break
			}
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				unicomHops = append(unicomHops, res.Hops...)
				if unicomLatency <= 0 && res.LatencyMs > 0 {
					unicomLatency = res.LatencyMs
				}
				if wire.AnalyzeUnicomRoute(unicomHops, unicomLatency) == "联通 9929" {
					return
				}
			}
		}
	}()

	// 移动路由探测 (先测广州核心节点，若已确认 CMIN2 精品线路则提前结束，否则测试上海、北京节点)
	go func() {
		defer traceWg.Done()
		for i, ep := range MobileEndpoints {
			if i >= 3 && len(mobileHops) > 0 {
				break
			}
			res := traceRoute(timeoutCtx, ep.Host)
			if len(res.Hops) > 0 {
				mobileHops = append(mobileHops, res.Hops...)
				if mobileLatency <= 0 && res.LatencyMs > 0 {
					mobileLatency = res.LatencyMs
				}
				if wire.AnalyzeMobileRoute(mobileHops, mobileLatency) == "移动 CMIN2" {
					return
				}
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
