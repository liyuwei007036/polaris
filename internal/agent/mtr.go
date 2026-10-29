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
	IP            string  `json:"ip"`
	Host          string  `json:"host"`
	ASN           string  `json:"asn"`
	City          string  `json:"city"`
	NodeName      string  `json:"node_name"`
	LossPct       float64 `json:"loss_pct"`
	Sent          int     `json:"sent"`
	Received      int     `json:"received"`
	AvgMs         float64 `json:"avg_ms"`
	BestMs        float64 `json:"best_ms"`
	WorstMs       float64 `json:"worst_ms"`
	StDevMs       float64 `json:"stdev_ms"`
	IsRateLimited bool    `json:"is_rate_limited"`
}

// MTRReport contains the complete hop-by-hop loss and latency diagnosis report.
type MTRReport struct {
	TargetHost    string   `json:"target_host"`
	TargetLabel   string   `json:"target_label,omitempty"`
	TotalHops     int      `json:"total_hops"`
	EndToEndLoss  float64  `json:"end_to_end_loss"`
	EndToEndAvgMs float64  `json:"end_to_end_avg_ms"`
	Summary       string   `json:"summary"`
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
		if commandExists("mtr") {
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

	if runtime.GOOS == "linux" && commandExists("mtr") {
		hops, rawOut = runLinuxMTR(diagCtx, target, count)
	}

	if len(hops) == 0 {
		// Fallback to traceroute with multi-probes if mtr is unavailable
		hops, rawOut = runFallbackTracerouteLoss(diagCtx, target, count)
	}

	// Enrich hops with real ASN, GeoIP city, and analyze ICMP rate-limiting
	enrichMTRHops(hops)

	report := buildMTRReport(target, label, hops, rawOut)
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
	// 1. Try JSON output mode first
	cmdJSON := exec.CommandContext(ctx, "mtr", "-n", "-c", countStr, "--report-json", target)
	outJSON, errJSON := cmdJSON.CombinedOutput()
	if errJSON == nil && len(outJSON) > 0 {
		if hops := parseMTRJSONOutput(outJSON, count); len(hops) > 0 {
			return hops, string(outJSON)
		}
	}

	// 2. Try standard text report mode
	cmdText := exec.CommandContext(ctx, "mtr", "-n", "-c", countStr, "--report", target)
	outText, errText := cmdText.CombinedOutput()
	if errText == nil || len(outText) > 0 {
		if hops := parseMTRTextOutput(string(outText), count); len(hops) > 0 {
			return hops, string(outText)
		}
	}

	return nil, string(outJSON) + "\n" + string(outText)
}

// parseMTRJSONOutput decodes the JSON structure produced by mtr --report-json.
func parseMTRJSONOutput(raw []byte, expectedSent int) []MTRHop {
	var mtrData struct {
		Report struct {
			Hubs []struct {
				Count   int     `json:"count"`
				Host    string  `json:"host"`
				LossPct float64 `json:"Loss%"`
				Snt     int     `json:"Snt"`
				Last    float64 `json:"Last"`
				Avg     float64 `json:"Avg"`
				Best    float64 `json:"Best"`
				Wrst    float64 `json:"Wrst"`
				StDev   float64 `json:"StDev"`
			} `json:"hubs"`
		} `json:"report"`
	}

	if err := json.Unmarshal(raw, &mtrData); err != nil || len(mtrData.Report.Hubs) == 0 {
		return nil
	}

	var hops []MTRHop
	for i, hub := range mtrData.Report.Hubs {
		sent := hub.Snt
		if sent <= 0 {
			sent = expectedSent
		}
		loss := hub.LossPct
		if loss < 0 {
			loss = 0
		} else if loss > 100 {
			loss = 100
		}
		rcv := int(math.Round(float64(sent) * (1.0 - loss/100.0)))
		if rcv < 0 {
			rcv = 0
		}

		hopNum := hub.Count
		if hopNum <= 0 {
			hopNum = i + 1
		}

		hops = append(hops, MTRHop{
			Hop:      hopNum,
			IP:       hub.Host,
			Host:     hub.Host,
			LossPct:  math.Round(loss*10) / 10,
			Sent:     sent,
			Received: rcv,
			AvgMs:    math.Round(hub.Avg*10) / 10,
			BestMs:   math.Round(hub.Best*10) / 10,
			WorstMs:  math.Round(hub.Wrst*10) / 10,
			StDevMs:  math.Round(hub.StDev*10) / 10,
		})
	}
	return hops
}

// parseMTRTextOutput extracts hop statistics from mtr's standard column report.
func parseMTRTextOutput(output string, defaultSent int) []MTRHop {
	var hops []MTRHop
	lines := strings.Split(output, "\n")
	reRow := regexp.MustCompile(`^\s*(\d+)\.\|--\s+(\S+)\s+([0-9.]+)%\s+(\d+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)\s+([0-9.]+)`)

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

			hops = append(hops, MTRHop{
				Hop:      hopNum,
				IP:       host,
				Host:     host,
				LossPct:  math.Round(loss*10) / 10,
				Sent:     snt,
				Received: rcv,
				AvgMs:    math.Round(avg*10) / 10,
				BestMs:   math.Round(best*10) / 10,
				WorstMs:  math.Round(wrst*10) / 10,
				StDevMs:  math.Round(stdev*10) / 10,
			})
		}
	}
	return hops
}

// runFallbackTracerouteLoss uses traceroute/tracert with multi-probes per hop as fallback.
func runFallbackTracerouteLoss(ctx context.Context, target string, count int) ([]MTRHop, string) {
	var outStr string
	probeCount := 5
	if count < 5 {
		probeCount = count
	}

	if runtime.GOOS == "windows" {
		cmd := exec.CommandContext(ctx, "tracert", "-d", "-h", "20", "-w", "500", target)
		out, _ := cmd.CombinedOutput()
		outStr = string(out)
	} else if commandExists("traceroute") {
		cmd := exec.CommandContext(ctx, "traceroute", "-n", "-m", "20", "-q", strconv.Itoa(probeCount), "-w", "1", target)
		out, _ := cmd.CombinedOutput()
		outStr = string(out)
	} else {
		return nil, "系统未安装 mtr 或 traceroute，无法进行逐跳诊断"
	}

	var hops []MTRHop
	lines := strings.Split(outStr, "\n")
	reLine := regexp.MustCompile(`^\s*(\d{1,2})\s+(.+)`)
	reIP := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	reRTT := regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*ms`)

	for _, rawLine := range lines {
		m := reLine.FindStringSubmatch(rawLine)
		if len(m) > 2 {
			hopNum, _ := strconv.Atoi(m[1])
			content := m[2]
			ipMatches := reIP.FindAllString(content, -1)
			hopIP := "???"
			if len(ipMatches) > 0 {
				hopIP = ipMatches[0]
			}

			rtts := reRTT.FindAllStringSubmatch(content, -1)
			received := len(rtts)
			sent := probeCount
			if received > sent {
				sent = received
			}

			loss := 0.0
			if sent > 0 {
				loss = float64(sent-received) / float64(sent) * 100.0
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

			hops = append(hops, MTRHop{
				Hop:      hopNum,
				IP:       hopIP,
				Host:     hopIP,
				LossPct:  math.Round(loss*10) / 10,
				Sent:     sent,
				Received: received,
				AvgMs:    math.Round(avg*10) / 10,
				BestMs:   math.Round(minVal*10) / 10,
				WorstMs:  math.Round(maxVal*10) / 10,
				StDevMs:  0,
			})
		}
	}
	return hops, outStr
}

// enrichMTRHops maps IP to real ASN and City, and differentiates ICMP rate-limiting from genuine packet loss.
func enrichMTRHops(hops []MTRHop) {
	for i := range hops {
		ip := hops[i].IP
		if ip == "" || ip == "???" || ip == "*" {
			hops[i].NodeName = "跃点未回显 (ICMP屏蔽)"
			hops[i].IsRateLimited = true
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
					break
				}
			}
		}
	}
}

// buildMTRReport aggregates metrics into a human-readable diagnosis conclusion.
func buildMTRReport(target, label string, hops []MTRHop, raw string) MTRReport {
	report := MTRReport{
		TargetHost: target,
		TargetLabel: label,
		TotalHops:   len(hops),
		Hops:        hops,
		RawOutput:   raw,
		CreatedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}

	if len(hops) == 0 {
		report.Summary = "未能完成链路诊断：未能获取到任何跃点数据"
		return report
	}

	lastHop := hops[len(hops)-1]
	report.EndToEndLoss = lastHop.LossPct
	report.EndToEndAvgMs = lastHop.AvgMs

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

	return report
}
