package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
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

	// 去程及 GFW 连通性指标 (由 Agent 离散发起分布式国内真机视角检测)
	InboundTelecomRoute         string `json:"inbound_telecom_route,omitempty"`
	InboundTelecomLatencyMs     int    `json:"inbound_telecom_latency_ms,omitempty"`
	InboundTelecomNodeLatencies string `json:"inbound_telecom_node_latencies,omitempty"`
	InboundTelecomTracePath     string `json:"inbound_telecom_trace_path,omitempty"`
	InboundUnicomRoute          string `json:"inbound_unicom_route,omitempty"`
	InboundUnicomLatencyMs      int    `json:"inbound_unicom_latency_ms,omitempty"`
	InboundUnicomNodeLatencies  string `json:"inbound_unicom_node_latencies,omitempty"`
	InboundUnicomTracePath      string `json:"inbound_unicom_trace_path,omitempty"`
	InboundMobileRoute          string `json:"inbound_mobile_route,omitempty"`
	InboundMobileLatencyMs      int    `json:"inbound_mobile_latency_ms,omitempty"`
	InboundMobileNodeLatencies  string `json:"inbound_mobile_node_latencies,omitempty"`
	InboundMobileTracePath      string `json:"inbound_mobile_trace_path,omitempty"`
	GFWBlocked                  bool   `json:"gfw_blocked"`
	GFWStatus                   string `json:"gfw_status,omitempty"`
	DomesticLatency             int    `json:"domestic_latency_ms,omitempty"`
	DomesticSource              string `json:"domestic_source,omitempty"`
}

type TargetEndpoint struct {
	City string `json:"city"`
	Host string `json:"host"`
}

type SpeedtestTaskPayload struct {
	TargetHost       string           `json:"target_host,omitempty"`
	TargetPort       uint16           `json:"target_port,omitempty"`
	TelecomEndpoints []TargetEndpoint `json:"telecom_endpoints,omitempty"`
	UnicomEndpoints  []TargetEndpoint `json:"unicom_endpoints,omitempty"`
	MobileEndpoints  []TargetEndpoint `json:"mobile_endpoints,omitempty"`
}

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

			// Check for carrier ASN tokens in line (AS9929, CUII, CN2, CMIN2, CUG)
			lower := strings.ToLower(hopContent)
			if strings.Contains(lower, "as9929") || strings.Contains(lower, "cuii") {
				hops = append(hops, "AS9929")
			}
			if strings.Contains(lower, "as10099") || strings.Contains(lower, "cug") {
				hops = append(hops, "AS10099")
			}
			if strings.Contains(lower, "cn2") || strings.Contains(lower, "as4809") {
				hops = append(hops, "AS4809")
			}
			if strings.Contains(lower, "cmin2") || strings.Contains(lower, "as58807") {
				hops = append(hops, "AS58807")
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

// EnsureNetworkTools is a no-op; network diagnosis uses existing host utilities or native Go probes without modifying the system.
func EnsureNetworkTools(ctx context.Context) {}

// traceRoute extracts intermediate IP hops towards the target using traceroute or tracepath.
// It uses CombinedOutput and never discards captured hops due to non-zero exit codes.
func traceRoute(ctx context.Context, targetHost string) traceRouteResult {
	var outStr string

	if runtime.GOOS == "windows" {
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracert", "-d", "-h", "18", "-w", "300", targetHost).CombinedOutput()
		cancel()
		outStr = string(out)
	} else if commandExists("traceroute") {
		// 1. Try ICMP traceroute (-I) with 18 max hops (-m 18 -q 1 -w 1)
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, _ := exec.CommandContext(traceCtx, "traceroute", "-I", "-n", "-m", "18", "-q", "1", "-w", "1", targetHost).CombinedOutput()
		cancel()

		hops, lat := extractHopsFromOutput(targetHost, string(out))
		if len(hops) > 0 {
			outStr = string(out)
		} else {
			// 2. If ICMP produced 0 hops (e.g. non-root or firewall drop), try standard UDP traceroute
			traceCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
			out2, _ := exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "18", "-q", "1", "-w", "1", targetHost).CombinedOutput()
			cancel2()

			hops2, _ := extractHopsFromOutput(targetHost, string(out2))
			if len(hops2) > 0 {
				outStr = string(out2)
			} else {
				// 3. Fallback to TCP SYN traceroute (-T -p 80)
				traceCtx3, cancel3 := context.WithTimeout(ctx, 5*time.Second)
				out3, _ := exec.CommandContext(traceCtx3, "traceroute", "-T", "-p", "80", "-n", "-m", "18", "-q", "1", "-w", "1", targetHost).CombinedOutput()
				cancel3()
				outStr = string(out3)
			}
		}
		_ = lat
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		out, _ := exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "18", targetHost).CombinedOutput()
		cancel()
		outStr = string(out)
	}

	hops, latency := extractHopsFromOutput(targetHost, outStr)

	// Prepend local kernel routing table evidence (first hops leaving the machine)
	if localHops := inspectLocalRouteQuery(ctx, targetHost); len(localHops) > 0 {
		hops = append(localHops, hops...)
	}

	lowerOut := strings.ToLower(outStr)
	if strings.Contains(lowerOut, "as9929") || strings.Contains(lowerOut, "cuii") {
		hops = append(hops, "AS9929")
	}
	if strings.Contains(lowerOut, "as10099") || strings.Contains(lowerOut, "cug") {
		hops = append(hops, "AS10099")
	}
	if strings.Contains(lowerOut, "cn2") || strings.Contains(lowerOut, "as4809") {
		hops = append(hops, "AS4809")
	}
	if strings.Contains(lowerOut, "cmin2") || strings.Contains(lowerOut, "as58807") {
		hops = append(hops, "AS58807")
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

func defaultCarrierEndpoints() (telecom, unicom, mobile []TargetEndpoint) {
	telecom = []TargetEndpoint{
		{Host: "ipv4.sha-4134.endpoint.nxtrace.org"},
		{Host: "ipv4.pek-4134.endpoint.nxtrace.org"},
		{Host: "ipv4.can-4134.endpoint.nxtrace.org"},
	}
	unicom = []TargetEndpoint{
		{Host: "ipv4.sha-4837.endpoint.nxtrace.org"},
		{Host: "ipv4.pek-4837.endpoint.nxtrace.org"},
		{Host: "ipv4.can-4837.endpoint.nxtrace.org"},
	}
	mobile = []TargetEndpoint{
		{Host: "ipv4.sha-9808.endpoint.nxtrace.org"},
		{Host: "ipv4.pek-9808.endpoint.nxtrace.org"},
		{Host: "ipv4.can-9808.endpoint.nxtrace.org"},
	}
	return
}

func runSpeedtest(ctx context.Context, task Task) TaskResult {
	timeoutCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var payload SpeedtestTaskPayload
	if task.Payload != "" && task.Payload != "{}" {
		_ = json.Unmarshal([]byte(task.Payload), &payload)
	}

	telecomEps := payload.TelecomEndpoints
	unicomEps := payload.UnicomEndpoints
	mobileEps := payload.MobileEndpoints
	if len(telecomEps) == 0 && len(unicomEps) == 0 && len(mobileEps) == 0 {
		telecomEps, unicomEps, mobileEps = defaultCarrierEndpoints()
	}

	// 1. 并发执行去程与 GFW 连通性检测 (由 Agent 离散发起，Master 0 探测介入)
	targetHost := strings.TrimSpace(payload.TargetHost)
	targetPort := payload.TargetPort
	if targetPort == 0 {
		targetPort = 443
	}
	var inboundRes inboundProbeResult
	var inboundWg sync.WaitGroup
	inboundWg.Add(1)
	go func() {
		defer inboundWg.Done()
		inboundRes = agentProbeInboundAndGFW(timeoutCtx, targetHost, targetPort)
	}()

	measureEndpoints := func(endpoints []TargetEndpoint) ([]string, int, string, string) {
		if len(endpoints) == 0 {
			return nil, 0, "", ""
		}
		type epRes struct {
			index      int
			city       string
			host       string
			resolvedIP string
			latency    int
		}
		ch := make(chan epRes, len(endpoints))
		for i, ep := range endpoints {
			go func(idx int, target TargetEndpoint) {
				host := target.Host
				resolvedIP := host
				if ips, err := net.LookupHost(host); err == nil && len(ips) > 0 {
					resolvedIP = ips[0]
				}

				city := target.City
				if city == "" || strings.HasSuffix(city, ".org") || strings.HasPrefix(city, "ipv4.") {
					c := wire.IPv4ToCity(resolvedIP)
					asn := wire.IPv4ToASN(resolvedIP)
					carrier := wire.ASNToName(asn)
					if c != "" && carrier != "" {
						city = fmt.Sprintf("%s%s", c, carrier)
					} else if c != "" {
						city = c
					} else if carrier != "" {
						city = carrier
					} else {
						city = host
					}
				}

				l := measureLatency(timeoutCtx, resolvedIP)
				ch <- epRes{index: idx, city: city, host: host, resolvedIP: resolvedIP, latency: l}
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
			targetAddr := r.resolvedIP
			if targetAddr == "" {
				targetAddr = r.host
			}
			if r.latency > 0 {
				list = append(list, fmt.Sprintf("%s: %dms", r.city, r.latency))
				if minLat <= 0 || r.latency < minLat {
					minLat = r.latency
					bestCity = r.city
					bestHost = targetAddr
				}
			} else {
				list = append(list, fmt.Sprintf("%s: 超时", r.city))
			}
		}
		if bestCity == "" && len(items) > 0 {
			bestCity = items[0].city
			bestHost = items[0].resolvedIP
			if bestHost == "" {
				bestHost = items[0].host
			}
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
	inboundWg.Wait()

	telecomRoute := analyzeTelecomRoute(telecomHops, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, mobileLatency)

	if telecomBestCity == "" {
		if len(telecomEps) > 0 {
			telecomBestCity = telecomEps[0].City
		} else {
			telecomBestCity = "电信网络"
		}
	}
	if unicomBestCity == "" {
		if len(unicomEps) > 0 {
			unicomBestCity = unicomEps[0].City
		} else {
			unicomBestCity = "联通网络"
		}
	}
	if mobileBestCity == "" {
		if len(mobileEps) > 0 {
			mobileBestCity = mobileEps[0].City
		} else {
			mobileBestCity = "移动网络"
		}
	}

	var telecomTracePath string
	if telecomLatency > 0 || len(telecomHops) > 0 {
		telecomTracePath = wire.BuildTracePath(telecomHops, telecomBestCity)
		if telecomTracePath == "" && telecomLatency > 0 {
			telecomTracePath = wire.BuildTracePathFallback(telecomRoute, telecomBestCity)
		}
	}
	var unicomTracePath string
	if unicomLatency > 0 || len(unicomHops) > 0 {
		unicomTracePath = wire.BuildTracePath(unicomHops, unicomBestCity)
		if unicomTracePath == "" && unicomLatency > 0 {
			unicomTracePath = wire.BuildTracePathFallback(unicomRoute, unicomBestCity)
		}
	}
	var mobileTracePath string
	if mobileLatency > 0 || len(mobileHops) > 0 {
		mobileTracePath = wire.BuildTracePath(mobileHops, mobileBestCity)
		if mobileTracePath == "" && mobileLatency > 0 {
			mobileTracePath = wire.BuildTracePathFallback(mobileRoute, mobileBestCity)
		}
	}

	result := SpeedtestResult{
		TelecomLatencyMs:            telecomLatency,
		TelecomRoute:                telecomRoute,
		TelecomNodeLatencies:        strings.Join(telecomLatList, " | "),
		TelecomTracePath:            telecomTracePath,
		UnicomLatencyMs:             unicomLatency,
		UnicomRoute:                 unicomRoute,
		UnicomNodeLatencies:         strings.Join(unicomLatList, " | "),
		UnicomTracePath:             unicomTracePath,
		MobileLatencyMs:             mobileLatency,
		MobileRoute:                 mobileRoute,
		MobileNodeLatencies:         strings.Join(mobileLatList, " | "),
		MobileTracePath:             mobileTracePath,
		InboundTelecomRoute:         inboundRes.InboundTelecomRoute,
		InboundTelecomLatencyMs:     inboundRes.InboundTelecomLatencyMs,
		InboundTelecomNodeLatencies: inboundRes.InboundTelecomNodeLatencies,
		InboundTelecomTracePath:     inboundRes.InboundTelecomTracePath,
		InboundUnicomRoute:          inboundRes.InboundUnicomRoute,
		InboundUnicomLatencyMs:      inboundRes.InboundUnicomLatencyMs,
		InboundUnicomNodeLatencies:  inboundRes.InboundUnicomNodeLatencies,
		InboundUnicomTracePath:      inboundRes.InboundUnicomTracePath,
		InboundMobileRoute:          inboundRes.InboundMobileRoute,
		InboundMobileLatencyMs:      inboundRes.InboundMobileLatencyMs,
		InboundMobileNodeLatencies:  inboundRes.InboundMobileNodeLatencies,
		InboundMobileTracePath:      inboundRes.InboundMobileTracePath,
		GFWBlocked:                  inboundRes.GFWBlocked,
		GFWStatus:                   inboundRes.GFWStatus,
		DomesticLatency:             inboundRes.LatencyMs,
		DomesticSource:              inboundRes.Source,
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码检测数据失败：" + err.Error()}
	}

	return TaskResult{
		Status:  "succeeded",
		Summary: "三网测速与连通性检测完成",
		Data:    string(encoded),
	}
}

type inboundProbeResult struct {
	OK                          bool
	Executed                    bool
	LatencyMs                   int
	PacketLoss                  int
	Source                      string
	Error                       string
	GFWBlocked                  bool
	GFWStatus                   string
	InboundTelecomRoute         string
	InboundTelecomLatencyMs     int
	InboundTelecomNodeLatencies string
	InboundTelecomTracePath     string
	InboundUnicomRoute          string
	InboundUnicomLatencyMs      int
	InboundUnicomNodeLatencies  string
	InboundUnicomTracePath      string
	InboundMobileRoute          string
	InboundMobileLatencyMs      int
	InboundMobileNodeLatencies  string
	InboundMobileTracePath      string
}

func formatProbeCity(city string) string {
	return strings.TrimSpace(city)
}

func formatProbeNetwork(asn int, netName string) string {
	if asn > 0 {
		if name := wire.ASNToName(fmt.Sprintf("AS%d", asn)); name != "" {
			return name
		}
	}
	netName = strings.TrimSpace(netName)
	if netName != "" {
		return netName
	}
	return "多线"
}

func agentProbeInboundAndGFW(ctx context.Context, host string, port uint16) inboundProbeResult {
	host = strings.TrimSpace(host)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}

	isPrivateOrInvalid := func(h string) bool {
		if h == "" {
			return true
		}
		ip := net.ParseIP(h)
		if ip == nil {
			return false
		}
		return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
	}

	// 若未指定公网目标或目标为本地/私有地址（如 Master 处于反向代理后），自动调用出口探测获取本机的公网 IPv4
	if isPrivateOrInvalid(host) {
		for _, ip := range CollectLocalIPs() {
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil && !isPrivateOrInvalid(ip) {
				host = ip
				break
			}
		}
	}

	if isPrivateOrInvalid(host) {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "本地或私有内网节点，无公网 IP"}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if port == 0 {
		port = 443
	}

	payload := map[string]any{
		"target": host,
		"type":   "ping",
		"measurementOptions": map[string]any{
			"protocol": "TCP",
			"port":     int(port),
			"packets":  2,
		},
		"locations": []map[string]any{
			{"country": "CN"},
		},
		"limit": 6,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "构造探测请求失败: " + err.Error(), Error: err.Error()}
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, "https://api.globalping.io/v1/measurements", bytes.NewReader(bodyBytes))
	if err != nil {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "创建探测请求失败: " + err.Error(), Error: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polaris-Agent/1.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "连接探测平台失败: " + err.Error(), Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: fmt.Sprintf("探测平台响应错误 (%d): %s", resp.StatusCode, string(respBody)), Error: string(respBody)}
	}

	var initResp struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil || initResp.ID == "" {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "解析探测任务 ID 失败"}
	}

	resultURL := fmt.Sprintf("https://api.globalping.io/v1/measurements/%s", initResp.ID)

	type probeItem struct {
		Probe struct {
			City    string `json:"city"`
			Network string `json:"network"`
			Country string `json:"country"`
			ASN     int    `json:"asn"`
			IP      string `json:"ip"`
		} `json:"probe"`
		Result struct {
			Status    string `json:"status"`
			RawOutput string `json:"rawOutput"`
			Stats     struct {
				Total int      `json:"total"`
				Rcv   int      `json:"rcv"`
				Drop  int      `json:"drop"`
				Loss  float64  `json:"loss"`
				Avg   *float64 `json:"avg"`
			} `json:"stats"`
			Timings []struct {
				RTT float64 `json:"rtt"`
			} `json:"timings"`
		} `json:"result"`
	}

	type resultRespType struct {
		ID      string      `json:"id"`
		Status  string      `json:"status"`
		Results []probeItem `json:"results"`
	}

	var lastRes resultRespType
	pollStart := time.Now()
	for {
		select {
		case <-probeCtx.Done():
			return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "等待探针结果超时"}
		default:
		}

		time.Sleep(1200 * time.Millisecond)

		pollReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, resultURL, nil)
		if err != nil {
			break
		}
		pollReq.Header.Set("User-Agent", "Polaris-Agent/1.0")
		pollResp, err := client.Do(pollReq)
		if err != nil {
			continue
		}

		err = json.NewDecoder(pollResp.Body).Decode(&lastRes)
		_ = pollResp.Body.Close()
		if err != nil {
			continue
		}

		if lastRes.Status == "finished" {
			break
		}
		if len(lastRes.Results) > 0 {
			allDone := true
			for _, r := range lastRes.Results {
				if r.Result.Status == "in-progress" {
					allDone = false
					break
				}
			}
			if allDone {
				break
			}
		}

		if time.Since(pollStart) >= 15*time.Second {
			break
		}
	}

	if len(lastRes.Results) == 0 {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "未获取到探针返回数据"}
	}

	var res inboundProbeResult
	var successCount, failCount int
	var totalRTT float64
	var validRTTCount int

	var telecomCities, unicomCities, mobileCities, bgpCities []string
	var telecomProbeCity, unicomProbeCity, mobileProbeCity string
	var telecomProbeASN, unicomProbeASN, mobileProbeASN int
	var bestBgpCity string
	var bestBgpASN, bestBgpRTT int

	for _, item := range lastRes.Results {
		if item.Result.Status == "in-progress" {
			continue
		}
		probeRTT := 0
		if item.Result.Stats.Avg != nil && *item.Result.Stats.Avg > 0 {
			probeRTT = int(*item.Result.Stats.Avg)
		} else if len(item.Result.Timings) > 0 && item.Result.Timings[0].RTT > 0 {
			probeRTT = int(item.Result.Timings[0].RTT)
		}

		if item.Result.Stats.Rcv > 0 {
			successCount++
			if probeRTT > 0 {
				totalRTT += float64(probeRTT)
				validRTTCount++
			}
		} else {
			failCount++
		}

		asn := item.Probe.ASN
		upperNet := strings.ToUpper(item.Probe.Network)
		cityName := formatProbeCity(item.Probe.City)
		if item.Probe.IP != "" {
			if c := wire.IPv4ToCity(item.Probe.IP); c != "" {
				cityName = c
			}
		}
		if cityName == "" {
			cityName = item.Probe.City
		}

		if asn == 4134 || strings.Contains(upperNet, "CHINANET") || strings.Contains(upperNet, "TELECOM") {
			if probeRTT > 0 {
				telecomCities = append(telecomCities, fmt.Sprintf("%s: %dms", cityName, probeRTT))
			}
			if (res.InboundTelecomLatencyMs == 0 || (probeRTT > 0 && probeRTT < res.InboundTelecomLatencyMs)) && probeRTT > 0 {
				res.InboundTelecomLatencyMs = probeRTT
				res.InboundTelecomRoute = wire.AnalyzeTelecomRoute([]string{host}, probeRTT)
				telecomProbeCity = cityName
				telecomProbeASN = asn
			}
		} else if asn == 4837 || asn == 9929 || strings.Contains(upperNet, "UNICOM") {
			if probeRTT > 0 {
				unicomCities = append(unicomCities, fmt.Sprintf("%s: %dms", cityName, probeRTT))
			}
			if (res.InboundUnicomLatencyMs == 0 || (probeRTT > 0 && probeRTT < res.InboundUnicomLatencyMs)) && probeRTT > 0 {
				res.InboundUnicomLatencyMs = probeRTT
				res.InboundUnicomRoute = wire.AnalyzeUnicomRoute([]string{host}, probeRTT)
				unicomProbeCity = cityName
				unicomProbeASN = asn
			}
		} else if asn == 9808 || asn == 58807 || strings.Contains(upperNet, "MOBILE") {
			if probeRTT > 0 {
				mobileCities = append(mobileCities, fmt.Sprintf("%s: %dms", cityName, probeRTT))
			}
			if (res.InboundMobileLatencyMs == 0 || (probeRTT > 0 && probeRTT < res.InboundMobileLatencyMs)) && probeRTT > 0 {
				res.InboundMobileLatencyMs = probeRTT
				res.InboundMobileRoute = wire.AnalyzeMobileRoute([]string{host}, probeRTT)
				mobileProbeCity = cityName
				mobileProbeASN = asn
			}
		} else {
			netName := formatProbeNetwork(item.Probe.ASN, item.Probe.Network)
			if probeRTT > 0 {
				bgpCities = append(bgpCities, fmt.Sprintf("%s%s: %dms", cityName, netName, probeRTT))
				if bestBgpRTT == 0 || probeRTT < bestBgpRTT {
					bestBgpRTT = probeRTT
					bestBgpCity = cityName
					bestBgpASN = asn
				}
			}
		}
	}

	totalEvaluated := successCount + failCount
	if totalEvaluated == 0 {
		return inboundProbeResult{OK: true, GFWStatus: "normal", Source: "探针未在超时前完成测量"}
	}

	res.Executed = true
	avgRTT := 0
	if validRTTCount > 0 {
		avgRTT = int(totalRTT / float64(validRTTCount))
	}

	if res.InboundTelecomLatencyMs == 0 && bestBgpRTT > 0 {
		res.InboundTelecomLatencyMs = bestBgpRTT
		res.InboundTelecomRoute = wire.AnalyzeTelecomRoute([]string{host}, bestBgpRTT)
	}
	if res.InboundUnicomLatencyMs == 0 && bestBgpRTT > 0 {
		res.InboundUnicomLatencyMs = bestBgpRTT
		res.InboundUnicomRoute = wire.AnalyzeUnicomRoute([]string{host}, bestBgpRTT)
	}
	if res.InboundMobileLatencyMs == 0 && bestBgpRTT > 0 {
		res.InboundMobileLatencyMs = bestBgpRTT
		res.InboundMobileRoute = wire.AnalyzeMobileRoute([]string{host}, bestBgpRTT)
	}

	destASN := wire.IPv4ToASN(host)
	city := wire.IPv4ToCity(host)
	destName := ""
	if destASN != "" {
		destName = wire.ASNToNameWithLocation(destASN, city)
	}
	if destName == "" {
		if city != "" {
			destName = fmt.Sprintf("节点 (%s)", city)
		} else {
			destName = host
		}
	}

	if successCount > 0 {
		res.OK = true
		res.GFWBlocked = false
		res.GFWStatus = "normal"
		res.LatencyMs = avgRTT
		res.PacketLoss = int(float64(failCount) / float64(totalEvaluated) * 100)

		var cityList []string
		citySet := make(map[string]bool)
		for _, item := range lastRes.Results {
			c := formatProbeCity(item.Probe.City)
			if c != "" && !citySet[c] {
				citySet[c] = true
				cityList = append(cityList, c)
			}
		}
		summaryCity := strings.Join(cityList, "/")
		if len(cityList) > 3 {
			summaryCity = strings.Join(cityList[:3], "/") + "等"
		}
		if summaryCity != "" {
			res.Source = fmt.Sprintf("分布式探针 (%s %d/%d 畅通 · %dms)", summaryCity, successCount, totalEvaluated, avgRTT)
		} else {
			res.Source = fmt.Sprintf("分布式探针 (%d/%d 畅通 · %dms)", successCount, totalEvaluated, avgRTT)
		}

		if res.InboundTelecomRoute == "" {
			res.InboundTelecomRoute = "电信直连"
		}
		if res.InboundUnicomRoute == "" {
			res.InboundUnicomRoute = "联通直连"
		}
		if res.InboundMobileRoute == "" {
			res.InboundMobileRoute = "移动直连"
		}

		if len(telecomCities) == 0 && len(bgpCities) > 0 {
			telecomCities = append(telecomCities, bgpCities...)
		}
		if len(unicomCities) == 0 && len(bgpCities) > 0 {
			unicomCities = append(unicomCities, bgpCities...)
		}
		if len(mobileCities) == 0 && len(bgpCities) > 0 {
			mobileCities = append(mobileCities, bgpCities...)
		}

		res.InboundTelecomNodeLatencies = strings.Join(telecomCities, " | ")
		res.InboundUnicomNodeLatencies = strings.Join(unicomCities, " | ")
		res.InboundMobileNodeLatencies = strings.Join(mobileCities, " | ")

		if telecomProbeCity == "" {
			telecomProbeCity = bestBgpCity
			telecomProbeASN = bestBgpASN
		}
		if unicomProbeCity == "" {
			unicomProbeCity = bestBgpCity
			unicomProbeASN = bestBgpASN
		}
		if mobileProbeCity == "" {
			mobileProbeCity = bestBgpCity
			mobileProbeASN = bestBgpASN
		}

		res.InboundTelecomTracePath = wire.BuildRealInboundTracePath(telecomProbeCity, telecomProbeASN, "电信", destName, nil)
		res.InboundUnicomTracePath = wire.BuildRealInboundTracePath(unicomProbeCity, unicomProbeASN, "联通", destName, nil)
		res.InboundMobileTracePath = wire.BuildRealInboundTracePath(mobileProbeCity, mobileProbeASN, "移动", destName, nil)
		return res
	}

	res.OK = false
	res.GFWBlocked = true
	res.GFWStatus = "blocked"
	res.LatencyMs = 0
	res.PacketLoss = 100
	res.Source = fmt.Sprintf("探针全超时 (0/%d 丢弃 · 端口无响应)", totalEvaluated)
	res.InboundTelecomRoute = "端口阻断"
	res.InboundUnicomRoute = "端口阻断"
	res.InboundMobileRoute = "端口阻断"
	res.Error = "分布式探针 TCP 端口握手均超时或被丢弃"
	return res
}
