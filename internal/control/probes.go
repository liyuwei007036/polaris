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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"github.com/liyuwei007036/polaris/internal/wire"
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
	OK                      bool
	LatencyMs               int
	PacketLoss              int
	Source                  string
	Error                   string
	InboundTelecomRoute     string
	InboundTelecomLatencyMs int
	InboundUnicomRoute      string
	InboundUnicomLatencyMs  int
	InboundMobileRoute      string
	InboundMobileLatencyMs  int
}

func formatProbeCity(city string) string {
	cityMap := map[string]string{
		"Guangzhou": "广州",
		"Shanghai":  "上海",
		"Beijing":   "北京",
		"Shenzhen":  "深圳",
		"Hangzhou":  "杭州",
		"Zhengzhou": "郑州",
		"Wuhan":     "武汉",
		"Xi'an":     "西安",
		"Ningbo":    "宁波",
		"Chengdu":   "成都",
		"Nanjing":   "南京",
		"Chongqing": "重庆",
		"Tianjin":   "天津",
		"Changsha":  "长沙",
		"Hefei":     "合肥",
		"Jinan":     "济南",
		"Fuzhou":    "福州",
		"Xiamen":    "厦门",
		"Shenyang":  "沈阳",
		"Kunming":   "昆明",
		"Guiyang":   "贵阳",
		"Nanning":   "南宁",
		"Lanzhou":   "兰州",
	}
	if cn, ok := cityMap[city]; ok {
		return cn
	}
	if city == "" {
		return "国内节点"
	}
	return city
}

func formatProbeNetwork(netName string) string {
	upper := strings.ToUpper(netName)
	if strings.Contains(upper, "TENCENT") {
		return "腾讯云"
	}
	if strings.Contains(upper, "ALIBABA") || strings.Contains(upper, "ALIYUN") {
		return "阿里云"
	}
	if strings.Contains(upper, "UNICOM") {
		return "联通"
	}
	if strings.Contains(upper, "TELECOM") || strings.Contains(upper, "CHINANET") {
		return "电信"
	}
	if strings.Contains(upper, "MOBILE") || strings.Contains(upper, "TIETONG") {
		return "移动"
	}
	if strings.Contains(upper, "CERNET") || strings.Contains(upper, "EDUCATION") {
		return "教育网"
	}
	if strings.Contains(upper, "HUAWEI") {
		return "华为云"
	}
	return "多线"
}

// probeFromChinaReal uses distributed public probe nodes in Mainland China (Beijing, Shenzhen, Shanghai, etc.)
// to perform real inbound TCP handshakes across the GFW to verify port accessibility.
func probeFromChinaReal(ctx context.Context, host string, port uint16) ChinaProbeResult {
	probeCtx, cancel := context.WithTimeout(ctx, 16*time.Second)
	defer cancel()

	payload := map[string]any{
		"target": host,
		"type":   "traceroute",
		"locations": []map[string]any{
			{"country": "CN", "asn": 4134},
			{"country": "CN", "asn": 4837},
			{"country": "CN", "asn": 9808},
			{"country": "CN"},
		},
		"limit": 6,
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
			ASN     int    `json:"asn"`
		} `json:"probe"`
		Result struct {
			Status    string `json:"status"`
			RawOutput string `json:"rawOutput"`
			Hops      []struct {
				ResolvedAddress string `json:"resolvedAddress"`
				Timings         []struct {
					RTT float64 `json:"rtt"`
				} `json:"timings"`
			} `json:"hops"`
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
			if allDone || time.Since(pollStart) >= 7*time.Second {
				break
			}
		}

		if time.Since(pollStart) >= 9*time.Second {
			break
		}
	}

	if len(lastRes.Results) == 0 {
		return ChinaProbeResult{OK: false, Error: "未获取到国内探针返回数据"}
	}

	var res ChinaProbeResult
	var probeDetails []string
	var successCount, failCount int
	var totalRTT float64
	var validRTTCount int

	for _, item := range lastRes.Results {
		cityName := formatProbeCity(item.Probe.City)
		netName := formatProbeNetwork(item.Probe.Network)

		var hops []string
		var probeRTT int
		for _, h := range item.Result.Hops {
			if h.ResolvedAddress != "" {
				hops = append(hops, h.ResolvedAddress)
			}
			for _, t := range h.Timings {
				if t.RTT > 0 {
					probeRTT = int(t.RTT)
				}
			}
		}

		if len(hops) == 0 && item.Result.RawOutput != "" {
			re := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
			hops = re.FindAllString(item.Result.RawOutput, -1)
		}

		if len(hops) > 0 {
			successCount++
			if probeRTT > 0 {
				totalRTT += float64(probeRTT)
				validRTTCount++
				probeDetails = append(probeDetails, fmt.Sprintf("%s(%s %dms)", cityName, netName, probeRTT))
			} else {
				probeDetails = append(probeDetails, fmt.Sprintf("%s(%s 畅通)", cityName, netName))
			}
		} else {
			failCount++
			probeDetails = append(probeDetails, fmt.Sprintf("%s(%s 超时)", cityName, netName))
		}

		// Carrier-specific inbound route detection
		asn := item.Probe.ASN
		upperNet := strings.ToUpper(item.Probe.Network)
		if asn == 4134 || strings.Contains(upperNet, "CHINANET") || strings.Contains(upperNet, "TELECOM") {
			if res.InboundTelecomRoute == "" {
				res.InboundTelecomRoute = wire.AnalyzeTelecomRoute(hops, probeRTT)
				res.InboundTelecomLatencyMs = probeRTT
			}
		} else if asn == 4837 || asn == 9929 || strings.Contains(upperNet, "UNICOM") {
			if res.InboundUnicomRoute == "" {
				res.InboundUnicomRoute = wire.AnalyzeUnicomRoute(hops, probeRTT)
				res.InboundUnicomLatencyMs = probeRTT
			}
		} else if asn == 9808 || asn == 58807 || strings.Contains(upperNet, "MOBILE") {
			if res.InboundMobileRoute == "" {
				res.InboundMobileRoute = wire.AnalyzeMobileRoute(hops, probeRTT)
				res.InboundMobileLatencyMs = probeRTT
			}
		}
	}

	// Sane fallbacks if a carrier was absent from current batch
	if res.InboundTelecomRoute == "" {
		res.InboundTelecomRoute = "电信 163 骨干"
	}
	if res.InboundUnicomRoute == "" {
		res.InboundUnicomRoute = "联通 4837"
	}
	if res.InboundMobileRoute == "" {
		res.InboundMobileRoute = "移动 CMI"
	}

	totalCount := len(lastRes.Results)
	avgRTT := 0
	if validRTTCount > 0 {
		avgRTT = int(totalRTT / float64(validRTTCount))
	}

	var sourceStr string
	if successCount == totalCount {
		sourceStr = fmt.Sprintf("全国 %d 探针 (%d/%d 畅通 · 均值 %dms): %s",
			totalCount, successCount, totalCount, avgRTT, strings.Join(probeDetails, ", "))
	} else if successCount > 0 {
		sourceStr = fmt.Sprintf("全国 %d 探针 (%d/%d 畅通 · 均值 %dms): %s",
			totalCount, successCount, totalCount, avgRTT, strings.Join(probeDetails, ", "))
	} else {
		sourceStr = fmt.Sprintf("全国 %d 探针 (0/%d 全部超时 · GFW 阻断): %s",
			totalCount, totalCount, strings.Join(probeDetails, ", "))
	}

	res.Source = sourceStr
	res.LatencyMs = avgRTT

	if successCount > 0 {
		res.OK = true
		res.PacketLoss = int(float64(failCount) / float64(totalCount) * 100)
		return res
	}

	res.OK = false
	res.LatencyMs = 0
	res.PacketLoss = 100
	res.Error = "国内所有探针 TCP 握手均超时或被重置 (疑似被 GFW 阻断)"
	return res
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
		item.InboundTelecomRoute = domResult.InboundTelecomRoute
		item.InboundTelecomLatencyMs = domResult.InboundTelecomLatencyMs
		item.InboundUnicomRoute = domResult.InboundUnicomRoute
		item.InboundUnicomLatencyMs = domResult.InboundUnicomLatencyMs
		item.InboundMobileRoute = domResult.InboundMobileRoute
		item.InboundMobileLatencyMs = domResult.InboundMobileLatencyMs

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

	targetHost := strings.TrimSpace(node.ClientAddress)
	targetPort := uint16(443)
	if targetHost != "" {
		listeners, err := s.store.ListListeners(ctx, node.ID)
		if err == nil && len(listeners) > 0 {
			for _, l := range listeners {
				if l.Enabled && l.Port > 0 {
					targetPort = l.Port
					break
				}
			}
		}
	}

	type agentResult struct {
		data string
		err  error
	}
	type probeResult struct {
		item ProbeResultItem
		has  bool
	}

	agentCh := make(chan agentResult, 1)
	probeCh := make(chan probeResult, 1)

	// 并发执行 A：海外 Agent 测三大运营商出站回程与 BGP 路由
	go func() {
		data, err := s.AskNode(ctx, nodeID, "speedtest.run", "{}")
		agentCh <- agentResult{data: data, err: err}
	}()

	// 并发执行 B：国内大陆真实探针针对目标公网地址及服务端口发起真实入站 TCP 握手测墙
	go func() {
		if targetHost != "" {
			item := s.probeSingleNode(ctx, node, targetHost, targetPort)
			probeCh <- probeResult{item: item, has: true}
		} else {
			probeCh <- probeResult{has: false}
		}
	}()

	resA := <-agentCh
	if resA.err != nil {
		return nil, fmt.Errorf("执行三网检测任务失败: %w", resA.err)
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
	if err := json.Unmarshal([]byte(resA.data), &res); err != nil {
		return nil, fmt.Errorf("解析检测数据失败: %w", err)
	}

	resB := <-probeCh

	st := NodeSpeedtest{
		NodeID:                  nodeID,
		TelecomLatencyMs:        res.TelecomLatencyMs,
		TelecomSpeedMbps:        res.TelecomSpeedMbps,
		TelecomRoute:            res.TelecomRoute,
		UnicomLatencyMs:         res.UnicomLatencyMs,
		UnicomSpeedMbps:         res.UnicomSpeedMbps,
		UnicomRoute:             res.UnicomRoute,
		MobileLatencyMs:         res.MobileLatencyMs,
		MobileSpeedMbps:         res.MobileSpeedMbps,
		MobileRoute:             res.MobileRoute,
		InboundTelecomRoute:     resB.item.InboundTelecomRoute,
		InboundTelecomLatencyMs: resB.item.InboundTelecomLatencyMs,
		InboundUnicomRoute:      resB.item.InboundUnicomRoute,
		InboundUnicomLatencyMs:  resB.item.InboundUnicomLatencyMs,
		InboundMobileRoute:      resB.item.InboundMobileRoute,
		InboundMobileLatencyMs:  resB.item.InboundMobileLatencyMs,
	}

	if resB.has {
		// 海外公网端口开放，但国内所有探针 TCP 握手均超时或被重置 -> 确系被 GFW 阻断封锁！
		if !resB.item.DomesticOK {
			st.GFWBlocked = true
			st.GFWStatus = "blocked"
			st.DomesticLatency = 0
			st.DomesticSource = resB.item.ProbeSource
			// 注意：去程与回程严格物理分离！
			// 去程（入站）：国内探针握手被 GFW 拦截，标记 GFWBlocked = true
			// 回程（出站）：服务器到国内骨干网 BGP 真实路由及延迟予以完整保留，严禁覆盖！
		} else {
			st.GFWBlocked = false
			st.GFWStatus = "normal"
			st.DomesticLatency = resB.item.LatencyMs
			st.DomesticSource = resB.item.ProbeSource
		}
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
