package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Prober struct {
	server             *Server
	lastGFWProbe       time.Time
	lastSpeedtestProbe time.Time
	mu                 sync.Mutex
	stopCh             chan struct{}
}

func newProber(s *Server) *Prober {
	return &Prober{
		server: s,
		stopCh: make(chan struct{}),
	}
}

func (p *Prober) Start(ctx context.Context) {
	go p.loop(ctx)
}

func (p *Prober) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}

func (p *Prober) loop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	// Initial delay before background scheduler starts
	select {
	case <-time.After(30 * time.Second):
	case <-ctx.Done():
		return
	case <-p.stopCh:
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

func (p *Prober) tick(parentCtx context.Context) {
	ctx, cancel := context.WithTimeout(parentCtx, 2*time.Minute)
	defer cancel()

	settings, err := p.server.store.GetAlertSettings(ctx)
	if err != nil {
		return
	}

	now := time.Now()

	// 1. GFW Scheduled Probing
	if settings.AutoProbeGFWEnabled && settings.ProbeGFWIntervalMinutes > 0 {
		interval := time.Duration(settings.ProbeGFWIntervalMinutes) * time.Minute
		if p.lastGFWProbe.IsZero() || now.Sub(p.lastGFWProbe) >= interval {
			p.lastGFWProbe = now
			go func() {
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer probeCancel()
				_, _ = p.server.runGFWCheckAll(probeCtx)
			}()
		}
	}

	// 2. Speedtest Scheduled Probing
	if settings.AutoProbeSpeedtestEnabled && settings.ProbeSpeedtestIntervalHours > 0 {
		interval := time.Duration(settings.ProbeSpeedtestIntervalHours) * time.Hour
		if p.lastSpeedtestProbe.IsZero() || now.Sub(p.lastSpeedtestProbe) >= interval {
			// Check if busy
			if settings.ProbeSkipWhenBusy && p.isAnyNodeBusy() {
				// Postpone by 10 minutes
				p.lastSpeedtestProbe = now.Add(-interval + 10*time.Minute)
				return
			}
			p.lastSpeedtestProbe = now
			go func() {
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer probeCancel()
				p.runScheduledSpeedtests(probeCtx)
			}()
		}
	}
}

func (p *Prober) isAnyNodeBusy() bool {
	snapshots := p.server.connHub.snapshot()
	for _, snap := range snapshots {
		// If download rate or upload rate is above 5 MB/s (40 Mbps), consider busy
		if snap.ReceivedRate > 5*1024*1024 || snap.SentRate > 5*1024*1024 {
			return true
		}
	}
	return false
}

func (p *Prober) runScheduledSpeedtests(ctx context.Context) {
	nodes, err := p.server.store.ListNodes(ctx)
	if err != nil {
		return
	}
	for _, node := range nodes {
		if !node.Online {
			continue
		}
		_, _ = p.server.ExecuteNodeSpeedtest(ctx, node.ID)
		time.Sleep(3 * time.Second) // Space out tests
	}
}

var (
	masterLocationCached string
	masterLocationMu     sync.RWMutex
	masterLocationTime   time.Time
)

// isMasterOverseas checks whether the Master control plane is running outside Mainland China.
// If it can reach Google directly within 2 seconds, it is overseas.
func isMasterOverseas() bool {
	masterLocationMu.RLock()
	if time.Since(masterLocationTime) < 30*time.Minute && masterLocationCached != "" {
		isOverseas := masterLocationCached == "overseas"
		masterLocationMu.RUnlock()
		return isOverseas
	}
	masterLocationMu.RUnlock()

	masterLocationMu.Lock()
	defer masterLocationMu.Unlock()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://connectivitycheck.gstatic.com/generate_204")
	if err == nil && resp.StatusCode == http.StatusNoContent {
		masterLocationCached = "overseas"
		masterLocationTime = time.Now()
		return true
	}
	if resp != nil {
		_ = resp.Body.Close()
	}

	masterLocationCached = "domestic"
	masterLocationTime = time.Now()
	return false
}

type ChinaProbeResult struct {
	OK         bool
	LatencyMs  int
	PacketLoss int
	Source     string
	Error      string
}

// probeFromChinaReal uses distributed public probe nodes in Mainland China (Beijing, Shenzhen, Shanghai, etc.)
// to perform real inbound TCP handshakes across the GFW to verify port accessibility.
func probeFromChinaReal(ctx context.Context, host string, port uint16) ChinaProbeResult {
	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	payload := map[string]any{
		"target": host,
		"type":   "ping",
		"measurementOptions": map[string]any{
			"protocol": "TCP",
			"port":     int(port),
		},
		"locations": []map[string]string{
			{"country": "CN"},
		},
		"limit": 2,
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return ChinaProbeResult{OK: false, Error: "构造探测请求失败: " + err.Error()}
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, "https://api.globalping.io/v1/measurements", bytes.NewReader(bodyBytes))
	if err != nil {
		return ChinaProbeResult{OK: false, Error: "创建探测请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polaris-Probe/1.0")

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ChinaProbeResult{OK: false, Error: "连接国内探测平台失败: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ChinaProbeResult{OK: false, Error: fmt.Sprintf("探测平台响应错误 (%d): %s", resp.StatusCode, string(respBody))}
	}

	var initResp struct {
		ID          string `json:"id"`
		ProbesCount int    `json:"probesCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil || initResp.ID == "" {
		return ChinaProbeResult{OK: false, Error: "解析探测任务 ID 失败"}
	}

	resultURL := fmt.Sprintf("https://api.globalping.io/v1/measurements/%s", initResp.ID)

	type probeItem struct {
		Probe struct {
			City    string `json:"city"`
			Network string `json:"network"`
			Country string `json:"country"`
		} `json:"probe"`
		Result struct {
			Status string `json:"status"`
			Stats  struct {
				Loss  float64 `json:"loss"`
				Avg   float64 `json:"avg"`
				Rcv   int     `json:"rcv"`
				Total int     `json:"total"`
				Drop  int     `json:"drop"`
			} `json:"stats"`
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
			return ChinaProbeResult{OK: false, Error: "等待国内探针结果超时"}
		default:
		}

		time.Sleep(1200 * time.Millisecond)

		pollReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, resultURL, nil)
		if err != nil {
			break
		}
		pollReq.Header.Set("User-Agent", "Polaris-Probe/1.0")
		pollResp, err := client.Do(pollReq)
		if err != nil {
			continue
		}

		err = json.NewDecoder(pollResp.Body).Decode(&lastRes)
		_ = pollResp.Body.Close()
		if err != nil {
			continue
		}

		if lastRes.Status == "finished" || len(lastRes.Results) > 0 {
			allDone := true
			for _, r := range lastRes.Results {
				if r.Result.Status == "in-progress" {
					allDone = false
					break
				}
			}
			if allDone || time.Since(pollStart) >= 6*time.Second {
				break
			}
		}

		if time.Since(pollStart) >= 8*time.Second {
			break
		}
	}

	if len(lastRes.Results) == 0 {
		return ChinaProbeResult{OK: false, Error: "未获取到国内探针返回数据"}
	}

	var successCount, failCount int
	var sources []string
	var totalRTT float64
	var validRTTCount int

	for _, item := range lastRes.Results {
		loc := item.Probe.City
		if loc == "" {
			loc = "中国"
		}
		if item.Probe.Network != "" {
			netName := item.Probe.Network
			if strings.Contains(netName, "Tencent") {
				netName = "腾讯云"
			} else if strings.Contains(netName, "Alibaba") {
				netName = "阿里云"
			} else if strings.Contains(netName, "UNICOM") {
				netName = "中国联通"
			} else if strings.Contains(netName, "Chinanet") || strings.Contains(netName, "Telecom") {
				netName = "中国电信"
			} else if strings.Contains(netName, "Mobile") {
				netName = "中国移动"
			}
			loc = fmt.Sprintf("%s (%s)", loc, netName)
		}
		sources = append(sources, loc)

		if item.Result.Stats.Rcv > 0 && item.Result.Stats.Loss < 100 {
			successCount++
			if item.Result.Stats.Avg > 0 {
				totalRTT += item.Result.Stats.Avg
				validRTTCount++
			}
		} else {
			failCount++
		}
	}

	sourceStr := strings.Join(sources, ", ")
	if sourceStr != "" {
		sourceStr = "国内探针: " + sourceStr
	} else {
		sourceStr = "国内探针"
	}

	if successCount > 0 {
		avgRTT := 0
		if validRTTCount > 0 {
			avgRTT = int(totalRTT / float64(validRTTCount))
		}
		return ChinaProbeResult{
			OK:         true,
			LatencyMs:  avgRTT,
			PacketLoss: 0,
			Source:     sourceStr,
		}
	}

	return ChinaProbeResult{
		OK:         false,
		LatencyMs:  0,
		PacketLoss: 100,
		Source:     sourceStr,
		Error:      "国内所有探针 TCP 握手均超时或被重置 (疑似被 GFW 阻断)",
	}
}

// runGFWCheckAll probes connectivity for all active nodes from overseas and domestic perspectives.
func (s *Server) runGFWCheckAll(ctx context.Context) ([]ProbeResultItem, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	var items []ProbeResultItem
	for _, node := range nodes {
		// Determine address and port
		targetHost := node.ClientAddress
		if targetHost == "" {
			continue
		}
		// Default port to probe: 443 or node's primary listener port
		targetPort := uint16(443)
		listeners, err := s.store.ListListeners(ctx, node.ID)
		if err == nil && len(listeners) > 0 {
			for _, l := range listeners {
				if l.Enabled && l.Port > 0 {
					targetPort = l.Port
					break
				}
			}
		}

		item := s.probeSingleNode(ctx, node, targetHost, targetPort)
		items = append(items, item)
	}

	if s.alertEngine != nil && len(items) > 0 {
		s.alertEngine.NotifyProbeSummary(items)
	}

	return items, nil
}

func (s *Server) probeSingleNode(ctx context.Context, node Node, host string, port uint16) ProbeResultItem {
	address := net.JoinHostPort(host, strconv.Itoa(int(port)))
	item := ProbeResultItem{
		NodeID:     node.ID,
		NodeName:   node.Name,
		TargetHost: host,
		TargetPort: port,
	}

	masterOverseas := isMasterOverseas()

	if masterOverseas {
		// 1. Overseas check (Direct TCP dial from master outside GFW)
		dialer := net.Dialer{Timeout: 3 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			item.OverseasOK = true
		} else {
			item.OverseasOK = false
			item.Error = "境外公网连接失败: " + err.Error()
			item.StatusDesc = "服务离线/端口未开放"
			return item
		}

		// 2. Domestic check (Initiated genuinely from inside Mainland China)
		domResult := probeFromChinaReal(ctx, host, port)
		item.DomesticOK = domResult.OK
		item.ProbeSource = domResult.Source
		item.LatencyMs = domResult.LatencyMs
		item.PacketLoss = domResult.PacketLoss

		if !item.DomesticOK {
			item.StatusDesc = "疑似被 GFW 阻断 (国内探针全超时)"
			if domResult.Error != "" {
				item.Error = domResult.Error
			}
		} else {
			item.StatusDesc = "正常通行"
		}
	} else {
		// Master is inside Mainland China:
		// 1. Master direct dial verifies DomesticOK
		dialer := net.Dialer{Timeout: 3 * time.Second}
		started := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			item.DomesticOK = true
			item.LatencyMs = int(time.Since(started).Milliseconds())
			item.ProbeSource = "控制中心本地 (国内网络)"
			item.OverseasOK = true
			item.StatusDesc = "正常通行"
		} else {
			item.DomesticOK = false
			item.ProbeSource = "控制中心本地 (国内网络)"
			item.Error = err.Error()
			if node.Online {
				item.OverseasOK = true
				item.StatusDesc = "疑似被 GFW 阻断 (国内直连超时)"
			} else {
				item.OverseasOK = false
				item.StatusDesc = "服务器离线"
			}
		}
	}

	return item
}

// ExecuteNodeSpeedtest runs a three-network latency and route detection task on an agent and records the result.
func (s *Server) ExecuteNodeSpeedtest(ctx context.Context, nodeID string) (*NodeSpeedtest, error) {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !node.Online {
		return nil, errors.New("服务器当前处于离线状态，无法进行检测")
	}

	data, err := s.AskNode(ctx, nodeID, "speedtest.run", "{}")
	if err != nil {
		return nil, fmt.Errorf("执行三网检测任务失败: %w", err)
	}

	var res struct {
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
	if err := json.Unmarshal([]byte(data), &res); err != nil {
		return nil, fmt.Errorf("解析检测数据失败: %w", err)
	}

	st := NodeSpeedtest{
		NodeID:           nodeID,
		TelecomLatencyMs: res.TelecomLatencyMs,
		TelecomSpeedMbps: res.TelecomSpeedMbps,
		TelecomRoute:     res.TelecomRoute,
		UnicomLatencyMs:  res.UnicomLatencyMs,
		UnicomSpeedMbps:  res.UnicomSpeedMbps,
		UnicomRoute:      res.UnicomRoute,
		MobileLatencyMs:  res.MobileLatencyMs,
		MobileSpeedMbps:  res.MobileSpeedMbps,
		MobileRoute:      res.MobileRoute,
	}

	if err := s.store.SaveNodeSpeedtest(ctx, st); err != nil {
		log.Printf("save node speedtest error: %v", err)
	}

	if s.alertEngine != nil {
		s.alertEngine.NotifySpeedtestReport(node.Name, st)
	}

	latest, _ := s.store.GetLatestNodeSpeedtest(ctx, nodeID)
	if latest != nil {
		return latest, nil
	}
	return &st, nil
}
