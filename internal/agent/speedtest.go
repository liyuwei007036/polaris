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

// 权威三网骨干测试节点池 (覆盖华东、华南、华北、西南、华中等核心国际互联出口与主干交换局)
type TargetEndpoint struct {
	City string `json:"city"`
	Host string `json:"host"`
}

type SpeedtestTaskPayload struct {
	TelecomEndpoints []TargetEndpoint `json:"telecom_endpoints,omitempty"`
	UnicomEndpoints  []TargetEndpoint `json:"unicom_endpoints,omitempty"`
	MobileEndpoints  []TargetEndpoint `json:"mobile_endpoints,omitempty"`
}

var (
	TelecomEndpoints = []TargetEndpoint{
		{City: "上海电信", Host: "202.96.209.133"},
		{City: "广州电信", Host: "58.60.188.222"},
		{City: "北京电信", Host: "219.141.140.10"},
		{City: "成都电信", Host: "61.139.2.69"},
		{City: "全国电信DNS", Host: "114.114.114.114"},
		{City: "武汉电信", Host: "202.103.24.68"},
	}
	UnicomEndpoints = []TargetEndpoint{
		{City: "上海联通", Host: "210.22.97.1"},
		{City: "广州联通", Host: "210.21.196.6"},
		{City: "北京联通", Host: "202.106.195.68"},
		{City: "成都联通", Host: "119.6.6.6"},
		{City: "全国联通DNS", Host: "116.116.116.116"},
		{City: "武汉联通", Host: "58.19.190.1"},
	}
	MobileEndpoints = []TargetEndpoint{
		{City: "广州移动", Host: "120.196.165.24"},
		{City: "上海移动", Host: "211.136.112.200"},
		{City: "北京移动", Host: "221.179.155.161"},
		{City: "成都移动", Host: "211.137.96.205"},
		{City: "全国移动网关", Host: "120.196.165.1"},
		{City: "武汉移动", Host: "111.175.220.1"},
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
		traceCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracert", "-d", "-h", "25", "-w", "500", targetHost).CombinedOutput()
		cancel()
		outStr = string(out)
	} else if commandExists("traceroute") {
		// 1. Try ICMP traceroute (-I) with 25 max hops (-m 25 -q 1 -w 1)
		traceCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		out, _ := exec.CommandContext(traceCtx, "traceroute", "-I", "-n", "-m", "25", "-q", "1", "-w", "1", targetHost).CombinedOutput()
		cancel()

		hops, lat := extractHopsFromOutput(targetHost, string(out))
		if len(hops) > 0 {
			outStr = string(out)
		} else {
			// 2. If ICMP produced 0 hops (e.g. non-root or firewall drop), try standard UDP traceroute
			traceCtx2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
			out2, _ := exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "25", "-q", "1", "-w", "1", targetHost).CombinedOutput()
			cancel2()

			hops2, _ := extractHopsFromOutput(targetHost, string(out2))
			if len(hops2) > 0 {
				outStr = string(out2)
			} else {
				// 3. Fallback to TCP SYN traceroute (-T -p 80)
				traceCtx3, cancel3 := context.WithTimeout(ctx, 10*time.Second)
				out3, _ := exec.CommandContext(traceCtx3, "traceroute", "-T", "-p", "80", "-n", "-m", "25", "-q", "1", "-w", "1", targetHost).CombinedOutput()
				cancel3()
				outStr = string(out3)
			}
		}
		_ = lat
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "25", targetHost).CombinedOutput()
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
	timeoutCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	// 确保基础网络测试工具已就绪，缺失时自动安装
	EnsureNetworkTools(timeoutCtx)

	var payload SpeedtestTaskPayload
	if task.Payload != "" && task.Payload != "{}" {
		_ = json.Unmarshal([]byte(task.Payload), &payload)
	}

	telecomEps := payload.TelecomEndpoints
	if len(telecomEps) == 0 {
		telecomEps = TelecomEndpoints
	}
	unicomEps := payload.UnicomEndpoints
	if len(unicomEps) == 0 {
		unicomEps = UnicomEndpoints
	}
	mobileEps := payload.MobileEndpoints
	if len(mobileEps) == 0 {
		mobileEps = MobileEndpoints
	}

	measureEndpoints := func(endpoints []TargetEndpoint) ([]string, int, string, string) {
		type epRes struct {
			index   int
			city    string
			host    string
			latency int
		}
		ch := make(chan epRes, len(endpoints))
		for i, ep := range endpoints {
			go func(idx int, target TargetEndpoint) {
				l := measureLatency(timeoutCtx, target.Host)
				ch <- epRes{index: idx, city: target.City, host: target.Host, latency: l}
			}(i, ep)
		}
		items := make([]epRes, len(endpoints))
		for range endpoints {
			r := <-ch
			items[r.index] = r
		}
		var list []string
		minLat := 0
		bestCity := ""
		bestHost := ""
		for _, r := range items {
			if r.latency > 0 {
				list = append(list, fmt.Sprintf("%s: %dms", r.city, r.latency))
				if minLat <= 0 || r.latency < minLat {
					minLat = r.latency
					bestCity = r.city
					bestHost = r.host
				}
			} else {
				list = append(list, fmt.Sprintf("%s: 超时", r.city))
			}
		}
		if bestCity == "" && len(endpoints) > 0 {
			bestCity = endpoints[0].City
			bestHost = endpoints[0].Host
		}
		return list, minLat, bestCity, bestHost
	}

	var telecomLatency, unicomLatency, mobileLatency int
	var telecomLatList, unicomLatList, mobileLatList []string
	var telecomBestCity, unicomBestCity, mobileBestCity string
	var telecomBestHost, unicomBestHost, mobileBestHost string
	var latencyWg sync.WaitGroup
	latencyWg.Add(3)

	go func() {
		defer latencyWg.Done()
		telecomLatList, telecomLatency, telecomBestCity, telecomBestHost = measureEndpoints(telecomEps)
	}()

	go func() {
		defer latencyWg.Done()
		unicomLatList, unicomLatency, unicomBestCity, unicomBestHost = measureEndpoints(unicomEps)
	}()

	go func() {
		defer latencyWg.Done()
		mobileLatList, mobileLatency, mobileBestCity, mobileBestHost = measureEndpoints(mobileEps)
	}()

	latencyWg.Wait()

	// 并发执行三大运营商路由追踪 (采用多端点联合证据判定，杜绝单点超时漏判 9929 / CN2)
	var telecomHops, unicomHops, mobileHops []string
	var traceWg sync.WaitGroup
	traceWg.Add(3)

	// 电信路由探测：优先测试测速最优的健康节点，确保真实捕获骨干跃点
	go func() {
		defer traceWg.Done()
		if telecomBestHost != "" {
			res := traceRoute(timeoutCtx, telecomBestHost)
			if len(res.Hops) > 0 {
				telecomHops = append(telecomHops, res.Hops...)
			}
		}
		if len(telecomHops) == 0 {
			attempts := 0
			for _, ep := range telecomEps {
				if timeoutCtx.Err() != nil || attempts >= 2 {
					break
				}
				if ep.Host == telecomBestHost {
					continue
				}
				attempts++
				res := traceRoute(timeoutCtx, ep.Host)
				if len(res.Hops) > 0 {
					telecomHops = append(telecomHops, res.Hops...)
					telecomBestCity = ep.City
					break
				}
			}
		}
	}()

	// 联通路由探测：优先测试测速最优的健康节点，若已确认 9929/CUG 精品线路则提前结束
	go func() {
		defer traceWg.Done()
		if unicomBestHost != "" {
			res := traceRoute(timeoutCtx, unicomBestHost)
			if len(res.Hops) > 0 {
				unicomHops = append(unicomHops, res.Hops...)
			}
		}
		if len(unicomHops) == 0 {
			attempts := 0
			for _, ep := range unicomEps {
				if timeoutCtx.Err() != nil || attempts >= 2 {
					break
				}
				if ep.Host == unicomBestHost {
					continue
				}
				attempts++
				res := traceRoute(timeoutCtx, ep.Host)
				if len(res.Hops) > 0 {
					unicomHops = append(unicomHops, res.Hops...)
					unicomBestCity = ep.City
					break
				}
			}
		}
	}()

	// 移动路由探测：优先测试测速最优的健康节点，若已确认 CMIN2 精品线路则提前结束
	go func() {
		defer traceWg.Done()
		if mobileBestHost != "" {
			res := traceRoute(timeoutCtx, mobileBestHost)
			if len(res.Hops) > 0 {
				mobileHops = append(mobileHops, res.Hops...)
			}
		}
		if len(mobileHops) == 0 {
			attempts := 0
			for _, ep := range mobileEps {
				if timeoutCtx.Err() != nil || attempts >= 2 {
					break
				}
				if ep.Host == mobileBestHost {
					continue
				}
				attempts++
				res := traceRoute(timeoutCtx, ep.Host)
				if len(res.Hops) > 0 {
					mobileHops = append(mobileHops, res.Hops...)
					mobileBestCity = ep.City
					break
				}
			}
		}
	}()

	traceWg.Wait()

	telecomRoute := analyzeTelecomRoute(telecomHops, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, mobileLatency)

	if telecomBestCity == "" {
		telecomBestCity = "上海电信"
	}
	if unicomBestCity == "" {
		unicomBestCity = "上海联通"
	}
	if mobileBestCity == "" {
		mobileBestCity = "广州移动"
	}

	telecomTracePath := wire.BuildTracePath(telecomHops, telecomBestCity)
	if telecomTracePath == "" && telecomLatency > 0 {
		telecomTracePath = wire.BuildTracePathFallback(telecomRoute, telecomBestCity)
	}
	unicomTracePath := wire.BuildTracePath(unicomHops, unicomBestCity)
	if unicomTracePath == "" && unicomLatency > 0 {
		unicomTracePath = wire.BuildTracePathFallback(unicomRoute, unicomBestCity)
	}
	mobileTracePath := wire.BuildTracePath(mobileHops, mobileBestCity)
	if mobileTracePath == "" && mobileLatency > 0 {
		mobileTracePath = wire.BuildTracePathFallback(mobileRoute, mobileBestCity)
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
