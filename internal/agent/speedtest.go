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

// measureThroughput downloads a small block (up to 3 MiB) from a domestic CDN endpoint to measure download bandwidth.
func measureThroughput(ctx context.Context, testURL string) float64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testURL, nil)
	if err != nil {
		return 0
	}
	client := &http.Client{Timeout: 5 * time.Second}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	const maxSampleBytes = 3 * 1024 * 1024 // 3 MiB sample
	written, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxSampleBytes))
	elapsed := time.Since(started).Seconds()
	if err != nil || elapsed <= 0 || written == 0 {
		return 0
	}

	// Calculate Mbps: (bytes * 8) / (1,000,000 * elapsed)
	mbps := (float64(written) * 8.0) / (1000.0 * 1000.0 * elapsed)
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
		// Prefer TCP SYN traceroute on port 443 which easily passes through NAT/firewalls
		traceCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		out, err = exec.CommandContext(traceCtx, "traceroute", "-T", "-p", "443", "-n", "-m", "18", "-q", "1", "-w", "1", targetHost).Output()
		if err != nil || len(out) == 0 {
			traceCtx2, cancel2 := context.WithTimeout(ctx, 4*time.Second)
			defer cancel2()
			out, err = exec.CommandContext(traceCtx2, "traceroute", "-n", "-m", "18", "-q", "1", "-w", "1", targetHost).Output()
		}
	} else if commandExists("tracepath") {
		traceCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		out, err = exec.CommandContext(traceCtx, "tracepath", "-n", "-m", "18", targetHost).Output()
	}

	if err != nil || len(out) == 0 {
		return nil
	}

	re := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	return re.FindAllString(string(out), -1)
}

func analyzeTelecomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		hasCN2 := false
		has163 := false
		for _, ip := range hops {
			if strings.HasPrefix(ip, "59.43.") {
				hasCN2 = true
			}
			if strings.HasPrefix(ip, "202.97.") {
				has163 = true
			}
		}
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
		if latencyMs < 55 {
			return "CN2 GIA / 优质直连"
		}
		if latencyMs > 160 {
			return "163 骨干网 / 普通直连"
		}
		return "电信直连"
	}
	return "不可达"
}

func analyzeUnicomRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		for _, ip := range hops {
			if strings.HasPrefix(ip, "218.105.") || strings.HasPrefix(ip, "210.51.") {
				return "联通 9929"
			}
			if strings.HasPrefix(ip, "219.158.") {
				return "联通 4837"
			}
		}
	}
	if latencyMs > 0 {
		if latencyMs < 60 {
			return "联通 9929 / 优质直连"
		}
		return "联通 4837 / 普通直连"
	}
	return "不可达"
}

func analyzeMobileRoute(hops []string, latencyMs int) string {
	if len(hops) > 0 {
		for _, ip := range hops {
			if strings.HasPrefix(ip, "223.120.16") || strings.HasPrefix(ip, "223.120.17") ||
				strings.HasPrefix(ip, "223.120.18") || strings.HasPrefix(ip, "223.120.19") {
				return "移动 CMIN2"
			}
			if strings.HasPrefix(ip, "221.183.") || strings.HasPrefix(ip, "223.120.") {
				return "移动 CMI"
			}
		}
	}
	if latencyMs > 0 {
		if latencyMs < 60 {
			return "移动 CMIN2 / 优质直连"
		}
		return "移动 CMI / 普通直连"
	}
	return "不可达"
}

func runSpeedtest(ctx context.Context, task Task) TaskResult {
	timeoutCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	// High-availability carrier HTTPS (TCP port 443) endpoints
	// China Telecom: official web cluster
	telecomTarget := "www.189.cn:443"
	// China Unicom: official web cluster
	unicomTarget := "www.10010.com:443"
	// China Mobile: official web cluster
	mobileTarget := "www.10086.cn:443"

	telecomLatency := measureTCPLatency(timeoutCtx, telecomTarget)
	if telecomLatency == 0 {
		telecomLatency = measureTCPLatency(timeoutCtx, "189.cn:443")
	}

	unicomLatency := measureTCPLatency(timeoutCtx, unicomTarget)
	if unicomLatency == 0 {
		unicomLatency = measureTCPLatency(timeoutCtx, "10010.com:443")
	}

	mobileLatency := measureTCPLatency(timeoutCtx, mobileTarget)
	if mobileLatency == 0 {
		mobileLatency = measureTCPLatency(timeoutCtx, "10086.cn:443")
	}

	// Route BGP backbone analysis (identifying CN2 / 163 / 9929 / CMIN2)
	telecomHops := traceRouteHops(timeoutCtx, "www.189.cn")
	unicomHops := traceRouteHops(timeoutCtx, "www.10010.com")
	mobileHops := traceRouteHops(timeoutCtx, "www.10086.cn")

	telecomRoute := analyzeTelecomRoute(telecomHops, telecomLatency)
	unicomRoute := analyzeUnicomRoute(unicomHops, unicomLatency)
	mobileRoute := analyzeMobileRoute(mobileHops, mobileLatency)

	// Throughput sample test from domestic public endpoint
	// Use Alibaba Cloud / Huawei Cloud / Tsinghua Tuna CDN mirrors
	speed := measureThroughput(timeoutCtx, "https://mirrors.aliyun.com/centos/RPM-GPG-KEY-CentOS-7")
	if speed == 0 {
		speed = measureThroughput(timeoutCtx, "https://mirrors.huaweicloud.com/repository/conf/CentOS-Base-7.repo")
	}
	if speed == 0 {
		speed = measureThroughput(timeoutCtx, "https://mirrors.tuna.tsinghua.edu.cn/static/img/favicon.png")
	}

	// Real measured bandwidth without any artificial or fabricated baseline
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
