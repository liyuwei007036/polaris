package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// MTRHop represents the metrics and network identity of a single router hop in the MTR path.
type MTRHop struct {
	Hop           int     `json:"hop"`
	Count         int     `json:"count"` // alias for Hop
	IP            string  `json:"ip"`
	Host          string  `json:"host"`
	ASN           string  `json:"asn"`
	City          string  `json:"city"`
	NodeName      string  `json:"node_name"`
	Location      string  `json:"location"` // alias for NodeName
	LossPct       float64 `json:"loss_pct"`
	LossPercent   float64 `json:"loss_percent"` // alias for LossPct
	Sent          int     `json:"sent"`
	Snt           int     `json:"snt"` // alias for Sent
	Received      int     `json:"received"`
	AvgMs         float64 `json:"avg_ms"`
	AvgRTT        float64 `json:"avg_rtt_ms"` // alias for AvgMs
	BestMs        float64 `json:"best_ms"`
	BestRTT       float64 `json:"best_rtt_ms"` // alias for BestMs
	WorstMs       float64 `json:"worst_ms"`
	WorstRTT      float64 `json:"worst_rtt_ms"` // alias for WorstMs
	StDevMs       float64 `json:"stdev_ms"`
	IsRateLimited bool    `json:"is_rate_limited"`
	IsRateLimit   bool    `json:"is_rate_limit"` // alias for IsRateLimited
}

// MTRReport contains the complete hop-by-hop loss and latency diagnosis report.
type MTRReport struct {
	TargetHost    string   `json:"target_host"`
	Target        string   `json:"target"` // alias for TargetHost
	TargetLabel   string   `json:"target_label,omitempty"`
	Tool          string   `json:"tool"`
	TotalHops     int      `json:"total_hops"`
	EndToEndLoss  float64  `json:"end_to_end_loss"`
	SummaryLoss   float64  `json:"summary_loss"` // alias for EndToEndLoss
	EndToEndAvgMs float64  `json:"end_to_end_avg_ms"`
	SummaryAvgMs  float64  `json:"summary_avg_ms"` // alias for EndToEndAvgMs
	Summary       string   `json:"summary"`
	Diagnosis     string   `json:"diagnosis"` // alias for Summary
	RateLimitHops int      `json:"rate_limit_hops"`
	RealDropHops  int      `json:"real_drop_hops"`
	Hops          []MTRHop `json:"hops"`
	RawOutput     string   `json:"raw_output,omitempty"`
	CreatedAt     string   `json:"created_at"`
}

var mtrInstallOnce sync.Once

// ensureMTRTool attempts to install the mtr package if not available.
func ensureMTRTool(ctx context.Context) {
	mtrInstallOnce.Do(func() {
		if runtime.GOOS != "linux" {
			return
		}
		if commandExists("mtr") || commandExists("traceroute") || commandExists("tracepath") {
			return
		}
		installCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		if commandExists("apt-get") {
			_ = installPackages(installCtx, "mtr-tiny")
		} else {
			_ = installPackages(installCtx, "mtr")
		}
	})
}

// runMTRDiagnose executes a deep hop-by-hop packet loss and jitter diagnosis towards target host.
func runMTRDiagnose(ctx context.Context, task Task) TaskResult {
	var payload struct {
		Target string `json:"target"`
		Count  int    `json:"count"`
		Label  string `json:"label"`
	}
	_ = json.Unmarshal([]byte(task.Payload), &payload)

	target := strings.TrimSpace(payload.Target)
	if target == "" {
		target = "202.96.209.133" // default to Shanghai Telecom core
	}
	count := payload.Count
	if count <= 0 || count > 30 {
		count = 10
	}
	label := strings.TrimSpace(payload.Label)

	timeoutDuration := time.Duration(count+15) * time.Second
	diagCtx, cancel := context.WithTimeout(ctx, timeoutDuration)
	defer cancel()

	ensureMTRTool(diagCtx)

	var hops []MTRHop
	var rawOut string
	toolName := "mtr"

	if runtime.GOOS == "linux" && commandExists("mtr") {
		hops, rawOut = runLinuxMTR(diagCtx, target, count)
	}

	if len(hops) == 0 {
		toolName = "traceroute"
		// Fallback to traceroute / tracepath with multi-probes if mtr is unavailable
		hops, rawOut = runFallbackTracerouteLoss(diagCtx, target, count)
	}

	// Enrich hops with real ASN, GeoIP city, and analyze ICMP rate-limiting
	enrichMTRHops(hops)

	report := buildMTRReport(target, label, hops, rawOut, toolName)
	resJSON, err := json.Marshal(report)
	if err != nil {
		return TaskResult{Status: "failed", Summary: "序列化诊断报告失败: " + err.Error()}
	}

	return TaskResult{
		Status:  "succeeded",
		Summary: report.Summary,
		Data:    string(resJSON),
	}
}

// runLinuxMTR runs the native mtr command with JSON or report formatting.
func runLinuxMTR(ctx context.Context, target string, count int) ([]MTRHop, string) {
	countStr := strconv.Itoa(count)
	// 1. Try ICMP JSON output mode first (-i 0.3 to accelerate sampling)
	cmdJSON := exec.CommandContext(ctx, "mtr", "-i", "0.3", "-n", "-c", countStr, "--report-json", target)
	outJSON, errJSON := cmdJSON.CombinedOutput()
	if errJSON == nil && len(outJSON) > 0 {
		if hops := parseMTRJSONOutput(outJSON, count); len(hops) > 0 {
			return hops, string(outJSON)
		}
	}

	// 2. Try UDP JSON mode (avoids raw socket requirement on some VPS kernels)
	cmdUDPJSON := exec.CommandContext(ctx, "mtr", "-u", "-i", "0.3", "-n", "-c", countStr, "--report-json", target)
	outUDPJSON, errUDPJSON := cmdUDPJSON.CombinedOutput()
	if errUDPJSON == nil && len(outUDPJSON) > 0 {
		if hops := parseMTRJSONOutput(outUDPJSON, count); len(hops) > 0 {
			return hops, string(outUDPJSON)
		}
	}

	// 3. Try standard text report mode
	cmdText := exec.CommandContext(ctx, "mtr", "-n", "-c", countStr, "--report", target)
	outText, errText := cmdText.CombinedOutput()
	if errText == nil || len(outText) > 0 {
		if hops := parseMTRTextOutput(string(outText), count); len(hops) > 0 {
			return hops, string(outText)
		}
	}

	// 4. Try standard text report mode with UDP
	cmdUDPText := exec.CommandContext(ctx, "mtr", "-u", "-n", "-c", countStr, "--report", target)
	outUDPText, errUDPText := cmdUDPText.CombinedOutput()
	if errUDPText == nil || len(outUDPText) > 0 {
		if hops := parseMTRTextOutput(string(outUDPText), count); len(hops) > 0 {
			return hops, string(outUDPText)
		}
	}

	combined := strings.TrimSpace(string(outJSON) + "\n" + string(outUDPJSON) + "\n" + string(outText) + "\n" + string(outUDPText))
	return nil, combined
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(val, "%")), 64)
		return f
	default:
		return 0
	}
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case json.Number:
		i, _ := val.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(val))
		return i
	default:
		return 0
	}
}

// parseMTRJSONOutput decodes the JSON structure produced by mtr --report-json.
func parseMTRJSONOutput(raw []byte, expectedSent int) []MTRHop {
	var mtrData struct {
		Report struct {
			Hubs []map[string]any `json:"hubs"`
		} `json:"report"`
	}

	if err := json.Unmarshal(raw, &mtrData); err != nil || len(mtrData.Report.Hubs) == 0 {
		return nil
	}

	var hops []MTRHop
	for i, hub := range mtrData.Report.Hubs {
		host, _ := hub["host"].(string)
		if host == "" {
			host = "???"
		}

		sent := toInt(hub["Snt"])
		if sent <= 0 {
			sent = expectedSent
		}

		loss := toFloat(hub["Loss%"])
		if loss < 0 {
			loss = 0
		} else if loss > 100 {
			loss = 100
		}

		rcv := int(math.Round(float64(sent) * (1.0 - loss/100.0)))
		if rcv < 0 {
			rcv = 0
		}

		hopNum := toInt(hub["count"])
		if hopNum <= 0 {
			hopNum = i + 1
		}

		roundedLoss := math.Round(loss*10) / 10
		roundedAvg := math.Round(toFloat(hub["Avg"])*10) / 10
		roundedBest := math.Round(toFloat(hub["Best"])*10) / 10
		roundedWorst := math.Round(toFloat(hub["Wrst"])*10) / 10
		roundedStDev := math.Round(toFloat(hub["StDev"])*10) / 10

		hops = append(hops, MTRHop{
			Hop:         hopNum,
			Count:       hopNum,
			IP:          host,
			Host:        host,
			LossPct:     roundedLoss,
			LossPercent: roundedLoss,
			Sent:        sent,
			Snt:         sent,
			Received:    rcv,
			AvgMs:       roundedAvg,
			AvgRTT:      roundedAvg,
			BestMs:      roundedBest,
			BestRTT:     roundedBest,
			WorstMs:     roundedWorst,
			WorstRTT:    roundedWorst,
			StDevMs:     roundedStDev,
		})
	}
	return hops
}

// parseMTRTextOutput extracts hop statistics from mtr's standard column report.
func parseMTRTextOutput(output string, defaultSent int) []MTRHop {
	var hops []MTRHop
	lines := strings.Split(output, "\n")
	reRow := regexp.MustCompile(`^\s*(\d+)\.[\s|]*--\s+(\S+)\s+([0-9.]+)%\s+(\d+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)`)

	for _, line := range lines {
		m := reRow.FindStringSubmatch(line)
		if len(m) >= 10 {
			hopNum, _ := strconv.Atoi(m[1])
			host := m[2]
			loss, _ := strconv.ParseFloat(m[3], 64)
			snt, _ := strconv.Atoi(m[4])
			if snt <= 0 {
				snt = defaultSent
			}
			avg, _ := strconv.ParseFloat(m[6], 64)
			best, _ := strconv.ParseFloat(m[7], 64)
			wrst, _ := strconv.ParseFloat(m[8], 64)
			stdev, _ := strconv.ParseFloat(m[9], 64)

			rcv := int(math.Round(float64(snt) * (1.0 - loss/100.0)))
			if rcv < 0 {
				rcv = 0
			}

			roundedLoss := math.Round(loss*10) / 10
			roundedAvg := math.Round(avg*10) / 10
			roundedBest := math.Round(best*10) / 10
			roundedWorst := math.Round(wrst*10) / 10
			roundedStDev := math.Round(stdev*10) / 10

			hops = append(hops, MTRHop{
				Hop:         hopNum,
				Count:       hopNum,
				IP:          host,
				Host:        host,
				LossPct:     roundedLoss,
				LossPercent: roundedLoss,
				Sent:        snt,
				Snt:         snt,
				Received:    rcv,
				AvgMs:       roundedAvg,
				AvgRTT:      roundedAvg,
				BestMs:      roundedBest,
				BestRTT:     roundedBest,
				WorstMs:     roundedWorst,
				WorstRTT:    roundedWorst,
				StDevMs:     roundedStDev,
			})
		}
	}
	return hops
}

// runFallbackTracerouteLoss uses traceroute, tracepath, or tracert with multi-probes per hop as fallback.
func runFallbackTracerouteLoss(ctx context.Context, target string, count int) ([]MTRHop, string) {
	var outStr string
	probeCount := 3
	if count < 3 {
		probeCount = count
	}

	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "tracert", "-d", "-h", "20", "-w", "500", target)
		out, _ := cmd.CombinedOutput()
		outStr = string(out)
	} else if commandExists("traceroute") {
		// 1. Try standard UDP/ICMP traceroute
		cmd := exec.CommandContext(ctx, "traceroute", "-n", "-m", "20", "-q", strconv.Itoa(probeCount), "-w", "1", target)
		out, _ := cmd.CombinedOutput()
		outStr = string(out)

		// 2. If 0 hops, try ICMP mode
		if !strings.Contains(outStr, "ms") && !strings.Contains(outStr, "*") {
			cmdICMP := exec.CommandContext(ctx, "traceroute", "-I", "-n", "-m", "20", "-q", strconv.Itoa(probeCount), "-w", "1", target)
			outICMP, _ := cmdICMP.CombinedOutput()
			if len(outICMP) > 0 {
				outStr = string(outICMP)
			}
		}
	} else if commandExists("tracepath") {
		// tracepath is preinstalled in iputils across virtually all minimal Linux distributions
		cmd := exec.CommandContext(ctx, "tracepath", "-n", "-m", "20", target)
		out, _ := cmd.CombinedOutput()
		outStr = string(out)
	} else if commandExists("ping") {
		// Universal fallback: ping with incremental TTL probe
		return runPingTTLLoop(ctx, target, probeCount)
	} else {
		return nil, "系统未安装 mtr, traceroute, tracepath 或 ping，无法进行逐跳诊断"
	}

	var hops []MTRHop
	lines := strings.Split(outStr, "\n")
	reLine := regexp.MustCompile(`^\s*(\d{1,2})[:.]?\s+(.+)`)
	reIP := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reRTT := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*ms`)

	for _, rawLine := range lines {
		m := reLine.FindStringSubmatch(rawLine)
		if len(m) > 2 {
			hopNum, _ := strconv.Atoi(m[1])
			content := m[2]
			if strings.Contains(content, "[LOCALHOST]") {
				continue
			}
			ipMatches := reIP.FindAllString(content, -1)
			hopIP := "???"
			if len(ipMatches) > 0 {
				hopIP = ipMatches[0]
			}

			rtts := reRTT.FindAllStringSubmatch(content, -1)
			received := len(rtts)
			asterisks := strings.Count(content, "*")

			actualSent := received + asterisks
			if actualSent < probeCount && (strings.Contains(content, "no reply") || strings.Contains(content, "timed out")) {
				actualSent = probeCount
			}
			if actualSent == 0 {
				if received > 0 {
					actualSent = received
				} else {
					actualSent = probeCount
				}
			}

			loss := 0.0
			if actualSent > 0 {
				loss = float64(actualSent-received) / float64(actualSent) * 100.0
			}

			var sum, minVal, maxVal float64
			for idx, r := range rtts {
				val, _ := strconv.ParseFloat(r[1], 64)
				sum += val
				if idx == 0 || val < minVal {
					minVal = val
				}
				if idx == 0 || val > maxVal {
					maxVal = val
				}
			}
			avg := 0.0
			if received > 0 {
				avg = sum / float64(received)
			}

			roundedLoss := math.Round(loss*10) / 10
			roundedAvg := math.Round(avg*10) / 10
			roundedBest := math.Round(minVal*10) / 10
			roundedWorst := math.Round(maxVal*10) / 10

			hops = append(hops, MTRHop{
				Hop:         hopNum,
				Count:       hopNum,
				IP:          hopIP,
				Host:        hopIP,
				LossPct:     roundedLoss,
				LossPercent: roundedLoss,
				Sent:        actualSent,
				Snt:         actualSent,
				Received:    received,
				AvgMs:       roundedAvg,
				AvgRTT:      roundedAvg,
				BestMs:      roundedBest,
				BestRTT:     roundedBest,
				WorstMs:     roundedWorst,
				WorstRTT:    roundedWorst,
				StDevMs:     0,
			})
		}
	}
	return hops, outStr
}

// runPingTTLLoop probes each hop using ping with incremental TTL (universal Linux/Unix fallback).
func runPingTTLLoop(ctx context.Context, target string, probeCount int) ([]MTRHop, string) {
	var hops []MTRHop
	var logBuilder strings.Builder
	reIP := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reRTT := regexp.MustCompile(`(?i)(?:time|时间)[=<]\s*([0-9.]+)\s*ms`)

	for ttl := 1; ttl <= 20; ttl++ {
		select {
		case <-ctx.Done():
			return hops, logBuilder.String()
		default:
		}

		ttlCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ttlCtx, "ping", "-n", "1", "-i", strconv.Itoa(ttl), "-w", "800", target)
		} else {
			cmd = exec.CommandContext(ttlCtx, "ping", "-c", "1", "-t", strconv.Itoa(ttl), "-W", "1", target)
		}
		out, _ := cmd.CombinedOutput()
		cancel()

		outText := string(out)
		logBuilder.WriteString(fmt.Sprintf("[%d] %s\n", ttl, strings.TrimSpace(outText)))

		hopIP := "???"
		rtt := 0.0
		isReached := false

		if strings.Contains(outText, "TTL expired") || strings.Contains(outText, "Time to live exceeded") ||
			strings.Contains(outText, "传输中过期") || strings.Contains(outText, "传输过期") ||
			strings.Contains(outText, "Exceeded") {
			ips := reIP.FindAllString(outText, -1)
			for _, ip := range ips {
				if ip != target {
					hopIP = ip
					break
				}
			}
			if hopIP == "???" && len(ips) > 0 {
				hopIP = ips[0]
			}
		} else if strings.Contains(outText, "bytes from") || strings.Contains(outText, "字节来自") {
			hopIP = target
			isReached = true
		}

		if m := reRTT.FindStringSubmatch(outText); len(m) > 1 {
			rtt, _ = strconv.ParseFloat(m[1], 64)
		}

		loss := 0.0
		sent := 1
		rcv := 1
		if hopIP == "???" {
			loss = 100.0
			rcv = 0
		}

		hops = append(hops, MTRHop{
			Hop:         ttl,
			Count:       ttl,
			IP:          hopIP,
			Host:        hopIP,
			LossPct:     loss,
			LossPercent: loss,
			Sent:        sent,
			Snt:         sent,
			Received:    rcv,
			AvgMs:       rtt,
			AvgRTT:      rtt,
			BestMs:      rtt,
			BestRTT:     rtt,
			WorstMs:     rtt,
			WorstRTT:    rtt,
			StDevMs:     0,
		})

		if isReached {
			break
		}
	}
	return hops, logBuilder.String()
}

// enrichMTRHops maps IP to real ASN and City, and differentiates ICMP rate-limiting from genuine packet loss.
func enrichMTRHops(hops []MTRHop) {
	for i := range hops {
		ip := hops[i].IP
		if ip == "" || ip == "???" || ip == "*" {
			hops[i].NodeName = "跃点未回显 (ICMP屏蔽)"
			hops[i].Location = hops[i].NodeName
			hops[i].IsRateLimited = true
			hops[i].IsRateLimit = true
			continue
		}

		asn := wire.IPv4ToASN(ip)
		city := wire.IPv4ToCity(ip)

		hops[i].ASN = asn
		hops[i].City = city

		if asn != "" {
			hops[i].NodeName = wire.ASNToNameWithLocation(asn, city)
		} else if city != "" {
			hops[i].NodeName = city
		} else {
			hops[i].NodeName = ip
		}
		hops[i].Location = hops[i].NodeName
	}

	// Analyze rate-limiting:
	// If a hop shows loss > 0, but a subsequent hop has lower or zero loss,
	// the loss on this intermediate hop is overwhelmingly due to router CPU ICMP rate-limiting,
	// because packets clearly traversed through it without being dropped!
	lastIndex := len(hops) - 1
	for i := 0; i < lastIndex; i++ {
		if hops[i].LossPct > 0 {
			for j := i + 1; j < len(hops); j++ {
				if hops[j].LossPct < hops[i].LossPct && hops[j].IP != "???" && hops[j].IP != "*" {
					hops[i].IsRateLimited = true
					hops[i].IsRateLimit = true
					break
				}
			}
		}
	}
}

// buildMTRReport aggregates metrics into a human-readable diagnosis conclusion.
func buildMTRReport(target, label string, hops []MTRHop, raw string, tool ...string) MTRReport {
	toolName := "mtr"
	if len(tool) > 0 && tool[0] != "" {
		toolName = tool[0]
	}

	rateLimitCount := 0
	realDropCount := 0
	for _, h := range hops {
		if h.IsRateLimited {
			rateLimitCount++
		} else if h.LossPct > 0 {
			realDropCount++
		}
	}

	report := MTRReport{
		TargetHost:    target,
		Target:        target,
		TargetLabel:   label,
		Tool:          toolName,
		TotalHops:     len(hops),
		RateLimitHops: rateLimitCount,
		RealDropHops:  realDropCount,
		Hops:          hops,
		RawOutput:     raw,
		CreatedAt:     time.Now().Format("2006-01-02 15:04:05"),
	}

	if len(hops) == 0 {
		report.Summary = "未能完成链路诊断：未能获取到任何跃点数据"
		report.Diagnosis = report.Summary
		return report
	}

	lastHop := hops[len(hops)-1]
	report.EndToEndLoss = lastHop.LossPct
	report.SummaryLoss = lastHop.LossPct
	report.EndToEndAvgMs = lastHop.AvgMs
	report.SummaryAvgMs = lastHop.AvgMs

	switch {
	case lastHop.LossPct == 0:
		report.Summary = fmt.Sprintf("全链路通畅，实测端到端无真实丢包 (RTT: %.1fms，中间若有高丢包跳点系路由器 ICMP 限速保护)", lastHop.AvgMs)
	case lastHop.LossPct >= 100:
		// Find last reachable hop
		lastReachable := "未知"
		for i := len(hops) - 1; i >= 0; i-- {
			if hops[i].LossPct < 100 && hops[i].IP != "???" && hops[i].IP != "*" {
				lastReachable = fmt.Sprintf("第 %d 跳 %s", hops[i].Hop, hops[i].NodeName)
				break
			}
		}
		report.Summary = fmt.Sprintf("目标节点完全不可达 (100%% 丢包)，最后有效响应跃点：%s", lastReachable)
	case lastHop.LossPct >= 15:
		firstDropHop := ""
		for _, h := range hops {
			if !h.IsRateLimited && h.LossPct >= 15 {
				firstDropHop = fmt.Sprintf("第 %d 跳 %s", h.Hop, h.NodeName)
				break
			}
		}
		if firstDropHop != "" {
			report.Summary = fmt.Sprintf("链路发生严重物理丢包 (端到端丢包率: %.1f%%)，丢包自 %s 开始扩散，疑似国际出口海缆或骨干网拥塞", lastHop.LossPct, firstDropHop)
		} else {
			report.Summary = fmt.Sprintf("链路发生严重丢包 (端到端丢包率: %.1f%%，平均时延: %.1fms)", lastHop.LossPct, lastHop.AvgMs)
		}
	default:
		report.Summary = fmt.Sprintf("链路存在轻微网络抖动 (端到端丢包率: %.1f%%，平均时延: %.1fms)", lastHop.LossPct, lastHop.AvgMs)
	}

	report.Diagnosis = report.Summary
	return report
}
