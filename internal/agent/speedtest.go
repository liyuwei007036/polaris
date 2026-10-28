package agent

import (
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"regexp"
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
// 电信: 上海电信核心 DNS (202.96.209.133), 广州电信 (58.60.188.222), 北京电信 (219.141.136.12)
// 联通: 上海联通核心 DNS (210.22.84.3), 广州联通 (210.21.196.6), 北京联通 (202.106.50.1)
// 移动: 上海移动核心网关 (120.204.80.1), 广州移动 (120.196.165.24), 北京移动 (221.179.155.161)
type TargetEndpoint struct {
	City string
	Host string
}

var (
	TelecomEndpoints = []TargetEndpoint{
		{City: "上海电信", Host: "202.96.209.133"},
		{City: "广州电信", Host: "58.60.188.222"},
		{City: "北京电信", Host: "219.141.136.12"},
	}
	UnicomEndpoints = []TargetEndpoint{
		{City: "上海联通", Host: "210.22.84.3"},
		{City: "广州联通", Host: "210.21.196.6"},
		{City: "北京联通", Host: "202.106.50.1"},
	}
	MobileEndpoints = []TargetEndpoint{
		{City: "上海移动", Host: "120.204.80.1"},
		{City: "广州移动", Host: "120.196.165.24"},
		{City: "北京移动", Host: "221.179.155.161"},
	}
)

// measureTCPLatency attempts a TCP handshake to authoritative carrier target ports (53, 80, 443) and returns latency in milliseconds.
func measureTCPLatency(ctx context.Context, host string) int {
	ports := []int{53, 80, 443}
	for _, port := range ports {
		dialer := net.Dialer{Timeout: 2 * time.Second}
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

// traceRouteHops extracts intermediate IP hops towards the target using traceroute or tracepath.
func traceRouteHops(ctx context.Context, targetHost string) []string {
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
		return nil
	}

	re := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	return re.FindAllString(string(out), -1)
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
			l := measureTCPLatency(timeoutCtx, ep.Host)
			if l > 0 {
				telecomLatency = l
				return
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range UnicomEndpoints {
			l := measureTCPLatency(timeoutCtx, ep.Host)
			if l > 0 {
				unicomLatency = l
				return
			}
		}
	}()

	go func() {
		defer latencyWg.Done()
		for _, ep := range MobileEndpoints {
			l := measureTCPLatency(timeoutCtx, ep.Host)
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
			hops := traceRouteHops(timeoutCtx, ep.Host)
			if len(hops) > 0 {
				telecomHops = hops
				return
			}
		}
	}()

	go func() {
		defer traceWg.Done()
		for _, ep := range UnicomEndpoints {
			hops := traceRouteHops(timeoutCtx, ep.Host)
			if len(hops) > 0 {
				unicomHops = hops
				return
			}
		}
	}()

	go func() {
		defer traceWg.Done()
		for _, ep := range MobileEndpoints {
			hops := traceRouteHops(timeoutCtx, ep.Host)
			if len(hops) > 0 {
				mobileHops = hops
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
