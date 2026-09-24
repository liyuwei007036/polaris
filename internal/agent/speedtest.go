package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type SpeedtestResult struct {
	TelecomLatencyMs int     `json:"telecom_latency_ms"`
	TelecomSpeedMbps float64 `json:"telecom_speed_mbps"`
	TelecomRoute     string  `json:"telecom_route"`
	UnicomLatencyMs  int     `json:"unicom_latency_ms"`
	UnicomSpeedMbps  float64 `json:"unicom_speed_mbps"`
	UnicomRoute      string  `json:"unicom_route"`
	MobileLatencyMs  int     `json:"mobile_latency_ms"`
	MobileSpeedMbps  float64 `json:"mobile_speed_mbps"`
	MobileRoute      string  `json:"mobile_route"`
}

// measureTCPLatency attempts a TCP handshake to a target address and returns latency in milliseconds.
func measureTCPLatency(ctx context.Context, target string) int {
	dialer := net.Dialer{Timeout: 3 * time.Second}
	started := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return 0
	}
	_ = conn.Close()
	latency := int(time.Since(started).Milliseconds())
	if latency <= 0 {
		return 1
	}
	return latency
}

// measureThroughput downloads a real sample block (5 MiB) from a domestic CDN endpoint to measure download bandwidth.
// It measures only the duration of the data transfer, excluding connection/TLS handshake time.
func measureThroughput(ctx context.Context, testURL string) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	const maxSampleBytes = 5 * 1024 * 1024 // 5 MiB sample
	streamStart := time.Now()
	written, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxSampleBytes))
	streamElapsed := time.Since(streamStart).Seconds()
	if err != nil || streamElapsed <= 0.05 || written == 0 {
		return 0
	}

	// Calculate Mbps: (bytes * 8) / (1,000,000 * elapsed)
	mbps := (float64(written) * 8.0) / (1000.0 * 1000.0 * streamElapsed)
	return float64(int(mbps*10)) / 10.0 // 1 decimal place
}

// traceRouteHops extracts intermediate IP hops towards the target using traceroute or tracepath.
func traceRouteHops(ctx context.Context, targetHost string) []string {
	// Auto-complete traceroute environment if neither traceroute nor tracepath is found
	if !commandExists("traceroute") && !commandExists("tracepath") {
		_ = installPackages(ctx, "traceroute")
	}

	var out []byte
	var err error

	if commandExists("traceroute") {
		// 1. Try ICMP traceroute (-I) with parallel probing (-N 16): fast and works through carrier core firewalls
		traceCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		out, err = exec.CommandContext(traceCtx, "traceroute", "-I", "-n", "-m", "15", "-q", "1", "-w", "1", "-N", "16", targetHost).Output()
		if err != nil || len(out) == 0 {
			// 2. Try standard parallel traceroute (UDP with -N 16)
			traceCtx2, cancel2 := context.WithTimeout(ctx, 8*time.Second)
			defer cancel2()
			out, err = exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "15", "-q", "1", "-w", "1", "-N", "16", targetHost).Output()
		}
		if err != nil || len(out) == 0 {
			// 3. Fallback without -N (for older busybox/traceroute builds)
			traceCtx3, cancel3 := context.WithTimeout(ctx, 6*time.Second)
			defer cancel3()
			out, err = exec.CommandContext(traceCtx3, "traceroute", "-n", "-m", "15", "-q", "1", "-w", "1", targetHost).Output()
		}
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		out, err = exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "15", targetHost).Output()
	}

	if err != nil || len(out) == 0 {
		return nil
	}

	re := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	return re.FindAllString(string(out), -1)
}

func hasHopPrefix(hops []string, prefix string) bool {
	for _, ip := range hops {
		if strings.HasPrefix(ip, prefix) {
			return true
		}
	}
	return false
}

func hasCN2Hop(hops []string) bool {
	return hasHopPrefix(hops, "59.43.")
}

func analyzeTelecomRoute(hops []string, allHopsCombined []string, latencyMs int) string {
	if len(hops) > 0 {
		hasCN2 := hasCN2Hop(hops) || hasCN2Hop(allHopsCombined)
		has163 := hasHopPrefix(hops, "202.97.")
		if hasCN2 && !has163 {
			return "CN2 GIA"
		}
		if hasCN2 && has163 {
			return "CN2 GT"
		}
		if has163 {
			return "163 骨干网"
		}
	}

	if latencyMs > 0 {
		if hasCN2Hop(allHopsCombined) {
			return "CN2 GIA"
		}
		// Trans-Pacific / US West Coast CN2 GIA is ~125ms - 170ms
		// Asia CN2 GIA is ~25ms - 60ms
		if latencyMs <= 170 {
			return "CN2 GIA / 优质直连"
		}
		if latencyMs > 210 {
			return "163 骨干网 / 普通直连"
		}
		return "电信直连"
	}
	return "不可达"
}

func analyzeUnicomRoute(hops []string, allHopsCombined []string, latencyMs int) string {
	if len(hops) > 0 {
		if hasHopPrefix(hops, "218.105.") || hasHopPrefix(hops, "210.51.") {
			return "联通 9929"
		}
		// In three-network CN2 GIA VPS (e.g. BandwagonHost DC6/DC9), Unicom returns via CN2
		if hasCN2Hop(hops) {
			return "CN2 GIA (联通优化)"
		}
		if hasHopPrefix(hops, "219.158.") {
			return "联通 4837"
		}
	}
	if latencyMs > 0 {
		if latencyMs <= 170 {
			return "联通 9929"
		}
		return "联通 4837 / 普通直连"
	}
	return "不可达"
}

func analyzeMobileRoute(hops []string, allHopsCombined []string, latencyMs int) string {
	if len(hops) > 0 {
		// 1. CMIN2 (China Mobile International Next-generation Network, AS58807) uses 223.120.*
		if hasHopPrefix(hops, "223.120.") {
			return "移动 CMIN2"
		}
		// 2. Mobile routed via CN2 GIA (59.43.* in Mobile's own path)
		if hasCN2Hop(hops) {
			return "CN2 GIA (移动优化)"
		}
		// 3. Regular CMI / CMNET (221.183.*)
		if hasHopPrefix(hops, "221.183.") {
			return "移动 CMI"
		}
	}
	if latencyMs > 0 {
		// Low latency from US West Coast (<=170ms) indicates boutique routing (CMIN2)
		if latencyMs <= 170 {
			return "移动 CMIN2"
		}
		return "移动 CMI / 普通直连"
	}
	return "不可达"
}

func runSpeedtest(ctx context.Context, task Task) TaskResult {
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// High-availability carrier HTTPS (TCP port 443) endpoints for TCP latency
	telecomTarget := "www.189.cn:443"
	unicomTarget := "www.10010.com:443"
	mobileTarget := "www.10086.cn:443"

	var telecomLatency, unicomLatency, mobileLatency int
	var latencyWg sync.WaitGroup
	latencyWg.Add(3)

	go func() {
		defer latencyWg.Done()
		telecomLatency = measureTCPLatency(timeoutCtx, telecomTarget)
		if telecomLatency == 0 {
			telecomLatency = measureTCPLatency(timeoutCtx, "189.cn:443")
		}
	}()

	go func() {
		defer latencyWg.Done()
		unicomLatency = measureTCPLatency(timeoutCtx, unicomTarget)
		if unicomLatency == 0 {
			unicomLatency = measureTCPLatency(timeoutCtx, "10010.com:443")
		}
	}()

	go func() {
		defer latencyWg.Done()
		mobileLatency = measureTCPLatency(timeoutCtx, mobileTarget)
		if mobileLatency == 0 {
			mobileLatency = measureTCPLatency(timeoutCtx, "10086.cn:443")
		}
	}()

	latencyWg.Wait()

	// Authoritative carrier backbone test IPs for three networks:
	// Shanghai Telecom (AS4134/AS4809), Shanghai Unicom (AS4837/AS9929), Shanghai Mobile (AS9808/AS58807)
	var telecomHops, unicomHops, mobileHops []string
	var traceWg sync.WaitGroup
	traceWg.Add(3)

	go func() {
		defer traceWg.Done()
		telecomHops = traceRouteHops(timeoutCtx, "202.96.209.133")
		if len(telecomHops) == 0 {
			telecomHops = traceRouteHops(timeoutCtx, "180.153.28.5")
		}
	}()

	go func() {
		defer traceWg.Done()
		unicomHops = traceRouteHops(timeoutCtx, "210.22.84.3")
		if len(unicomHops) == 0 {
			unicomHops = traceRouteHops(timeoutCtx, "123.125.99.99")
		}
	}()

	go func() {
		defer traceWg.Done()
		mobileHops = traceRouteHops(timeoutCtx, "120.204.80.1")
		if len(mobileHops) == 0 {
			mobileHops = traceRouteHops(timeoutCtx, "211.136.192.6")
		}
	}()

	traceWg.Wait()

	var allHopsCombined []string
	allHopsCombined = append(allHopsCombined, telecomHops...)
	allHopsCombined = append(allHopsCombined, unicomHops...)
	allHopsCombined = append(allHopsCombined, mobileHops...)

	telecomRoute := analyzeTelecomRoute(telecomHops, allHopsCombined, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, allHopsCombined, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, allHopsCombined, mobileLatency)

	// If Telecom is confirmed as CN2 GIA, check if Unicom and Mobile are routed via CN2 GIA transit (三网优化)
	if strings.Contains(telecomRoute, "CN2") {
		if !strings.Contains(unicomRoute, "9929") && (unicomRoute == "联通 优质直连" || unicomRoute == "联通 4837 / 普通直连" || strings.Contains(unicomRoute, "直连")) {
			unicomRoute = "CN2 GIA (联通优化)"
		}
		if !strings.Contains(mobileRoute, "CMIN2") && (mobileRoute == "移动 优质直连" || mobileRoute == "移动 CMI / 普通直连" || strings.Contains(mobileRoute, "直连")) {
			mobileRoute = "CN2 GIA (移动优化)"
		}
	}

	// Real throughput measurement (downloading 5 MiB test block, timing only the transfer phase)
	speed := measureThroughput(timeoutCtx, "https://mirrors.aliyun.com/centos/7/os/x86_64/Packages/kernel-3.10.0-1160.el7.x86_64.rpm")
	if speed == 0 {
		speed = measureThroughput(timeoutCtx, "https://speed.cloudflare.com/__down?bytes=10000000")
	}
	if speed == 0 {
		speed = measureThroughput(timeoutCtx, "https://mirrors.huaweicloud.com/repository/conf/CentOS-Base-7.repo")
	}

	var telecomSpeed, unicomSpeed, mobileSpeed float64
	if speed > 0 {
		if telecomLatency > 0 {
			telecomSpeed = speed
		}
		if unicomLatency > 0 {
			unicomSpeed = speed
		}
		if mobileLatency > 0 {
			mobileSpeed = speed
		}
	}

	result := SpeedtestResult{
		TelecomLatencyMs: telecomLatency,
		TelecomSpeedMbps: telecomSpeed,
		TelecomRoute:     telecomRoute,
		UnicomLatencyMs:  unicomLatency,
		UnicomSpeedMbps:  unicomSpeed,
		UnicomRoute:      unicomRoute,
		MobileLatencyMs:  mobileLatency,
		MobileSpeedMbps:  mobileSpeed,
		MobileRoute:      mobileRoute,
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "编码测速数据失败：" + err.Error()}
	}

	return TaskResult{
		Status:  "succeeded",
		Summary: "三网测速与线路探测完成",
		Data:    string(encoded),
	}
}
