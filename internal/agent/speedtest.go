package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
	TelecomLatencyMs     int     `json:"telecom_latency_ms"`
	TelecomSpeedMbps     float64 `json:"telecom_speed_mbps,omitempty"`
	TelecomRoute         string  `json:"telecom_route"`
	TelecomNodeLatencies string  `json:"telecom_node_latencies,omitempty"`
	TelecomTracePath     string  `json:"telecom_trace_path,omitempty"`
	UnicomLatencyMs      int     `json:"unicom_latency_ms"`
	UnicomSpeedMbps      float64 `json:"unicom_speed_mbps,omitempty"`
	UnicomRoute          string  `json:"unicom_route"`
	UnicomNodeLatencies  string  `json:"unicom_node_latencies,omitempty"`
	UnicomTracePath      string  `json:"unicom_trace_path,omitempty"`
	MobileLatencyMs      int     `json:"mobile_latency_ms"`
	MobileSpeedMbps      float64 `json:"mobile_speed_mbps,omitempty"`
	MobileRoute          string  `json:"mobile_route"`
	MobileNodeLatencies  string  `json:"mobile_node_latencies,omitempty"`
	MobileTracePath      string  `json:"mobile_trace_path,omitempty"`
}

// 权威三网骨干测试节点 (南京核心节点与备用端点)
// 电信: 南京电信核心 (218.2.135.1), 南京电信备用 (58.213.1.1), 南京电信汇聚 (221.226.1.1)
// 联通: 南京联通核心 (58.240.23.1), 南京联通备用 (221.6.4.66), 南京联通核心 (112.80.248.75)
// 移动: 南京移动核心 (112.4.0.1), 南京移动备用 (36.152.44.95), 南京移动汇聚 (183.207.95.1)
type TargetEndpoint struct {
	City string
	Host string
}

var (
	TelecomEndpoints = []TargetEndpoint{
		{City: "南京电信", Host: "218.2.135.1"},
		{City: "南京电信备用", Host: "58.213.1.1"},
		{City: "南京电信核心", Host: "221.226.1.1"},
	}
	UnicomEndpoints = []TargetEndpoint{
		{City: "南京联通", Host: "58.240.23.1"},
		{City: "南京联通备用", Host: "221.6.4.66"},
		{City: "南京联通核心", Host: "112.80.248.75"},
	}
	MobileEndpoints = []TargetEndpoint{
		{City: "南京移动", Host: "112.4.0.1"},
		{City: "南京移动备用", Host: "36.152.44.95"},
		{City: "南京移动核心", Host: "183.207.95.1"},
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

var (
	toolsCheckOnce sync.Once
)

// EnsureNetworkTools checks and automatically installs missing network diagnosis tools (e.g. traceroute).
func EnsureNetworkTools(ctx context.Context) {
	toolsCheckOnce.Do(func() {
		if runtime.GOOS != "linux" {
			return
		}
		if commandExists("traceroute") || commandExists("tracepath") {
			return
		}
		installCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		_ = installPackages(installCtx, "traceroute")
	})
}

// traceRoute extracts intermediate IP hops towards the target using traceroute or tracepath.
// It uses CombinedOutput and never discards captured hops due to non-zero exit codes.
func traceRoute(ctx context.Context, targetHost string) traceRouteResult {
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

	// Prepend local kernel routing table evidence (first hops leaving the machine)
	if localHops := inspectLocalRouteQuery(ctx, targetHost); len(localHops) > 0 {
		hops = append(localHops, hops...)
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

	// 确保基础网络测试工具已就绪，缺失时自动安装
	EnsureNetworkTools(timeoutCtx)

	measureEndpoints := func(endpoints []TargetEndpoint) ([]string, int) {
		type epRes struct {
			index   int
			city    string
			latency int
		}
		ch := make(chan epRes, len(endpoints))
		for i, ep := range endpoints {
			go func(idx int, target TargetEndpoint) {
				l := measureLatency(timeoutCtx, target.Host)
				ch <- epRes{index: idx, city: target.City, latency: l}
			}(i, ep)
		}
		items := make([]epRes, len(endpoints))
		for range endpoints {
			r := <-ch
			items[r.index] = r
		}
		var list []string
		minLat := 0
		for _, r := range items {
			if r.latency > 0 {
				list = append(list, fmt.Sprintf("%s: %dms", r.city, r.latency))
				if minLat <= 0 || r.latency < minLat {
					minLat = r.latency
				}
			} else {
				list = append(list, fmt.Sprintf("%s: 超时", r.city))
			}
		}
		return list, minLat
	}

	var telecomLatency, unicomLatency, mobileLatency int
	var telecomLatList, unicomLatList, mobileLatList []string
	var latencyWg sync.WaitGroup
	latencyWg.Add(3)

	go func() {
		defer latencyWg.Done()
		telecomLatList, telecomLatency = measureEndpoints(TelecomEndpoints)
	}()

	go func() {
		defer latencyWg.Done()
		unicomLatList, unicomLatency = measureEndpoints(UnicomEndpoints)
	}()

	go func() {
		defer latencyWg.Done()
		mobileLatList, mobileLatency = measureEndpoints(MobileEndpoints)
	}()

	latencyWg.Wait()

	// 并发执行三大运营商路由追踪 (采用多端点联合证据判定，杜绝单点超时漏判 9929 / CN2)
	var telecomHops, unicomHops, mobileHops []string
	var traceWg sync.WaitGroup
	traceWg.Add(3)

	// 电信路由探测 (先测核心南京节点，若已确认精品线路则提前结束，否则测试备用节点)
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
				if rName == "电信 CN2 GIA" || rName == "电信 CN2 GT" || rName == "电信 CTGNet" || rName == "电信 9929" || rName == "电信 CMIN2" {
					return
				}
			}
		}
	}()

	// 联通路由探测 (先测核心南京节点，若已确认 9929/CUG 精品线路则提前结束，否则测试备用节点)
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
				uName := wire.AnalyzeUnicomRoute(unicomHops, unicomLatency)
				if uName == "联通 9929" || uName == "联通 CN2 GIA" || uName == "联通 CMIN2" {
					return
				}
			}
		}
	}()

	// 移动路由探测 (先测核心南京节点，若已确认 CMIN2 精品线路则提前结束，否则测试备用节点)
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
				mName := wire.AnalyzeMobileRoute(mobileHops, mobileLatency)
				if mName == "移动 CMIN2" || mName == "移动 CN2 GIA" || mName == "移动 9929" {
					return
				}
			}
		}
	}()

	traceWg.Wait()

	telecomRoute := analyzeTelecomRoute(telecomHops, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, mobileLatency)

	telecomTracePath := wire.BuildTracePath(telecomHops, "南京电信")
	if telecomTracePath == "" && telecomLatency > 0 {
		telecomTracePath = wire.BuildTracePathFallback(telecomRoute, "南京电信")
	}
	unicomTracePath := wire.BuildTracePath(unicomHops, "南京联通")
	if unicomTracePath == "" && unicomLatency > 0 {
		unicomTracePath = wire.BuildTracePathFallback(unicomRoute, "南京联通")
	}
	mobileTracePath := wire.BuildTracePath(mobileHops, "南京移动")
	if mobileTracePath == "" && mobileLatency > 0 {
		mobileTracePath = wire.BuildTracePathFallback(mobileRoute, "南京移动")
	}

	result := SpeedtestResult{
		TelecomLatencyMs:     telecomLatency,
		TelecomRoute:         telecomRoute,
		TelecomNodeLatencies: strings.Join(telecomLatList, " | "),
		TelecomTracePath:     telecomTracePath,
		UnicomLatencyMs:      unicomLatency,
		UnicomRoute:          unicomRoute,
		UnicomNodeLatencies:  strings.Join(unicomLatList, " | "),
		UnicomTracePath:      unicomTracePath,
		MobileLatencyMs:      mobileLatency,
		MobileRoute:          mobileRoute,
		MobileNodeLatencies:  strings.Join(mobileLatList, " | "),
		MobileTracePath:      mobileTracePath,
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
