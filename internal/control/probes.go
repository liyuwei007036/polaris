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
				probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	OK                          bool
	Executed                    bool
	LatencyMs                   int
	PacketLoss                  int
	Source                      string
	Error                       string
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
	host = strings.TrimSpace(host)
	if host == "" {
		return ChinaProbeResult{Executed: false, Error: "目标主机地址为空"}
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			return ChinaProbeResult{Executed: false, Error: "目标为本地或私有内网 IP，跳过国内探针检测"}
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 22*time.Second)
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
		return ChinaProbeResult{Executed: false, Error: "构造探测请求失败: " + err.Error()}
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, "https://api.globalping.io/v1/measurements", bytes.NewReader(bodyBytes))
	if err != nil {
		return ChinaProbeResult{Executed: false, Error: "创建探测请求失败: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polaris-Probe/1.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ChinaProbeResult{Executed: false, Error: "连接国内探测平台失败: " + err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return ChinaProbeResult{Executed: false, Error: fmt.Sprintf("探测平台响应错误 (%d): %s", resp.StatusCode, string(respBody))}
	}

	var initResp struct {
		ID          string `json:"id"`
		ProbesCount int    `json:"probesCount"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil || initResp.ID == "" {
		return ChinaProbeResult{Executed: false, Error: "解析探测任务 ID 失败"}
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
			return ChinaProbeResult{Executed: false, Error: "等待国内探针结果超时"}
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

		if time.Since(pollStart) >= 16*time.Second {
			break
		}
	}

	if len(lastRes.Results) == 0 {
		return ChinaProbeResult{Executed: false, Error: "未获取到国内探针返回数据"}
	}

	var res ChinaProbeResult
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

		// Carrier-specific inbound latency and route
		asn := item.Probe.ASN
		upperNet := strings.ToUpper(item.Probe.Network)
		cityName := formatProbeCity(item.Probe.City)
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
			// 国内腾讯云、阿里云等高质量多线探针
			netName := formatProbeNetwork(item.Probe.Network)
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
		return ChinaProbeResult{Executed: false, Error: "国内探针未在超时前完成测量"}
	}

	res.Executed = true
	avgRTT := 0
	if validRTTCount > 0 {
		avgRTT = int(totalRTT / float64(validRTTCount))
	}

	// 若某运营商缺乏单线探针，采用国内最佳多线探针进行基准兜底，避免去程延迟报 0
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
		if loc, err := newIPLocator(); err == nil {
			if l := loc.Locate(host); l != "" && l != "未知" {
				destName = l
			}
		}
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
			res.Source = fmt.Sprintf("国内探针 (%s %d/%d 畅通 · %dms)", summaryCity, successCount, totalEvaluated, avgRTT)
		} else {
			res.Source = fmt.Sprintf("国内探针 (%d/%d 畅通 · %dms)", successCount, totalEvaluated, avgRTT)
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
			if bestBgpCity != "" {
				telecomProbeCity = bestBgpCity
				telecomProbeASN = bestBgpASN
			} else {
				telecomProbeCity = "国内节点"
			}
		}
		if unicomProbeCity == "" {
			if bestBgpCity != "" {
				unicomProbeCity = bestBgpCity
				unicomProbeASN = bestBgpASN
			} else {
				unicomProbeCity = "国内节点"
			}
		}
		if mobileProbeCity == "" {
			if bestBgpCity != "" {
				mobileProbeCity = bestBgpCity
				mobileProbeASN = bestBgpASN
			} else {
				mobileProbeCity = "国内节点"
			}
		}

		res.InboundTelecomTracePath = wire.BuildRealInboundTracePath(telecomProbeCity, telecomProbeASN, "电信", destName, nil)
		res.InboundUnicomTracePath = wire.BuildRealInboundTracePath(unicomProbeCity, unicomProbeASN, "联通", destName, nil)
		res.InboundMobileTracePath = wire.BuildRealInboundTracePath(mobileProbeCity, mobileProbeASN, "移动", destName, nil)
		return res
	}

	res.OK = false
	res.LatencyMs = 0
	res.PacketLoss = 100
	res.Source = fmt.Sprintf("国内探针全超时 (0/%d 丢弃 · GFW 阻断)", totalEvaluated)
	res.InboundTelecomRoute = "GFW 阻断"
	res.InboundUnicomRoute = "GFW 阻断"
	res.InboundMobileRoute = "GFW 阻断"
	res.Error = "国内所有探针 TCP 端口握手均超时或被丢弃 (疑似被 GFW 阻断)"
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
		targetHost := strings.TrimSpace(node.ClientAddress)
		if targetHost == "" {
			targetHost = strings.TrimSpace(node.LastObservedIP)
		}
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

		// 同步持久化 GFW 阻断检测结果到 node_speedtests 数据库，让前端列表实时准确获知阻断状态
		st, _ := s.store.GetLatestNodeSpeedtest(ctx, node.ID)
		if st == nil {
			st = &NodeSpeedtest{NodeID: node.ID}
		}
		if item.GFWBlocked {
			st.GFWBlocked = true
			st.GFWStatus = "blocked"
			st.DomesticLatency = 0
			st.DomesticSource = item.ProbeSource
			st.InboundTelecomRoute = "GFW 阻断"
			st.InboundUnicomRoute = "GFW 阻断"
			st.InboundMobileRoute = "GFW 阻断"
		} else if item.DomesticOK {
			st.GFWBlocked = false
			st.GFWStatus = "normal"
			st.DomesticLatency = item.LatencyMs
			st.DomesticSource = item.ProbeSource
			if item.InboundTelecomRoute != "" && item.InboundTelecomRoute != "GFW 阻断" {
				st.InboundTelecomRoute = item.InboundTelecomRoute
				st.InboundTelecomLatencyMs = item.InboundTelecomLatencyMs
				st.InboundTelecomNodeLatencies = item.InboundTelecomNodeLatencies
				st.InboundTelecomTracePath = item.InboundTelecomTracePath
			}
			if item.InboundUnicomRoute != "" && item.InboundUnicomRoute != "GFW 阻断" {
				st.InboundUnicomRoute = item.InboundUnicomRoute
				st.InboundUnicomLatencyMs = item.InboundUnicomLatencyMs
				st.InboundUnicomNodeLatencies = item.InboundUnicomNodeLatencies
				st.InboundUnicomTracePath = item.InboundUnicomTracePath
			}
			if item.InboundMobileRoute != "" && item.InboundMobileRoute != "GFW 阻断" {
				st.InboundMobileRoute = item.InboundMobileRoute
				st.InboundMobileLatencyMs = item.InboundMobileLatencyMs
				st.InboundMobileNodeLatencies = item.InboundMobileNodeLatencies
				st.InboundMobileTracePath = item.InboundMobileTracePath
			}
		} else {
			// 未阻断（可能是离线、探测平台异常或无需跨墙）
			st.GFWBlocked = false
			if !node.Online {
				st.GFWStatus = "offline"
			} else if !item.OverseasOK {
				st.GFWStatus = "unreachable"
			} else {
				st.GFWStatus = "error"
			}
			if item.ProbeSource != "" {
				st.DomesticSource = item.ProbeSource
			} else if item.Error != "" {
				st.DomesticSource = item.Error
			}
			// 清理因历史错误写入的“GFW 阻断”路由标记
			if st.InboundTelecomRoute == "GFW 阻断" {
				st.InboundTelecomRoute = ""
			}
			if st.InboundUnicomRoute == "GFW 阻断" {
				st.InboundUnicomRoute = ""
			}
			if st.InboundMobileRoute == "GFW 阻断" {
				st.InboundMobileRoute = ""
			}
		}
		_ = s.store.SaveNodeSpeedtest(ctx, *st)
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

	// 0. 节点离线时直接返回离线状态，不归为 GFW 阻断
	if !node.Online {
		item.OverseasOK = false
		item.DomesticOK = false
		item.GFWBlocked = false
		item.ProbeExecuted = false
		item.StatusDesc = "服务器当前处于离线状态"
		item.Error = "节点已离线"
		return item
	}

	// 1. 国内节点无需过 GFW，不作阻断误判
	isDomesticNode := false
	if s.ipLocator != nil {
		loc := s.ipLocator.Locate(host)
		if strings.Contains(loc, "中国") && !strings.Contains(loc, "香港") && !strings.Contains(loc, "澳门") && !strings.Contains(loc, "台湾") {
			isDomesticNode = true
		}
	}
	if !isDomesticNode {
		asn := wire.IPv4ToASN(host)
		if asn == "4134" || asn == "4837" || asn == "9808" || asn == "9929" || asn == "58807" {
			isDomesticNode = true
		}
	}
	if isDomesticNode {
		item.OverseasOK = true
		item.DomesticOK = true
		item.GFWBlocked = false
		item.ProbeExecuted = true
		item.StatusDesc = "国内节点 (无需跨墙)"
		return item
	}

	masterOverseas := isMasterOverseas()

	if masterOverseas {
		// 1. Overseas check (Direct TCP dial from master outside GFW)
		dialer := net.Dialer{Timeout: 2 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil && port != 22 {
			sshAddr := net.JoinHostPort(host, "22")
			if connSSH, errSSH := dialer.DialContext(ctx, "tcp", sshAddr); errSSH == nil {
				_ = connSSH.Close()
				port = 22
				item.TargetPort = 22
				address = sshAddr
				conn = nil
				err = nil
			}
		}
		if err == nil {
			if conn != nil {
				_ = conn.Close()
			}
			item.OverseasOK = true
		} else {
			item.OverseasOK = false
			item.DomesticOK = false
			item.GFWBlocked = false
			item.ProbeExecuted = false
			item.Error = "境外公网连接失败: " + err.Error()
			item.StatusDesc = "服务离线/端口未开放"
			return item
		}

		// 2. Domestic check (Initiated genuinely from inside Mainland China)
		domResult := probeFromChinaReal(ctx, host, port)
		item.ProbeExecuted = domResult.Executed
		item.DomesticOK = domResult.OK
		item.ProbeSource = domResult.Source
		item.LatencyMs = domResult.LatencyMs
		item.PacketLoss = domResult.PacketLoss
		item.InboundTelecomRoute = domResult.InboundTelecomRoute
		item.InboundTelecomLatencyMs = domResult.InboundTelecomLatencyMs
		item.InboundTelecomNodeLatencies = domResult.InboundTelecomNodeLatencies
		item.InboundTelecomTracePath = domResult.InboundTelecomTracePath
		item.InboundUnicomRoute = domResult.InboundUnicomRoute
		item.InboundUnicomLatencyMs = domResult.InboundUnicomLatencyMs
		item.InboundUnicomNodeLatencies = domResult.InboundUnicomNodeLatencies
		item.InboundUnicomTracePath = domResult.InboundUnicomTracePath
		item.InboundMobileRoute = domResult.InboundMobileRoute
		item.InboundMobileLatencyMs = domResult.InboundMobileLatencyMs
		item.InboundMobileNodeLatencies = domResult.InboundMobileNodeLatencies
		item.InboundMobileTracePath = domResult.InboundMobileTracePath

		if !domResult.Executed {
			item.GFWBlocked = false
			item.Error = domResult.Error
			item.StatusDesc = "国内探针异常: " + domResult.Error
		} else if item.DomesticOK {
			item.GFWBlocked = false
			item.StatusDesc = "正常通行"
		} else {
			item.GFWBlocked = true
			item.StatusDesc = "疑似被 GFW 阻断 (国内探针全超时)"
			if domResult.Error != "" {
				item.Error = domResult.Error
			}
		}
	} else {
		// Master is inside Mainland China:
		// 1. 控制机国内直连探测目标端口
		dialer := net.Dialer{Timeout: 2 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			item.OverseasOK = true
			item.DomesticOK = true
			item.GFWBlocked = false
			item.ProbeExecuted = true
			item.StatusDesc = "正常通行 (控制机直连通畅)"
			return item
		}

		// 2. 控制机本地不通时，通过全国多地公共探针交叉核验
		item.OverseasOK = node.Online
		domResult := probeFromChinaReal(ctx, host, port)
		item.ProbeExecuted = domResult.Executed
		item.DomesticOK = domResult.OK
		item.ProbeSource = domResult.Source
		item.LatencyMs = domResult.LatencyMs
		item.PacketLoss = domResult.PacketLoss
		item.InboundTelecomRoute = domResult.InboundTelecomRoute
		item.InboundTelecomLatencyMs = domResult.InboundTelecomLatencyMs
		item.InboundTelecomNodeLatencies = domResult.InboundTelecomNodeLatencies
		item.InboundTelecomTracePath = domResult.InboundTelecomTracePath
		item.InboundUnicomRoute = domResult.InboundUnicomRoute
		item.InboundUnicomLatencyMs = domResult.InboundUnicomLatencyMs
		item.InboundUnicomNodeLatencies = domResult.InboundUnicomNodeLatencies
		item.InboundUnicomTracePath = domResult.InboundUnicomTracePath
		item.InboundMobileRoute = domResult.InboundMobileRoute
		item.InboundMobileLatencyMs = domResult.InboundMobileLatencyMs
		item.InboundMobileNodeLatencies = domResult.InboundMobileNodeLatencies
		item.InboundMobileTracePath = domResult.InboundMobileTracePath

		if !domResult.Executed {
			item.GFWBlocked = false
			item.Error = domResult.Error
			item.StatusDesc = "国内探针异常: " + domResult.Error
		} else if item.DomesticOK {
			item.GFWBlocked = false
			item.StatusDesc = "正常通行"
		} else if item.OverseasOK {
			item.GFWBlocked = true
			item.StatusDesc = "🚨 疑似被 GFW 阻断 (国内探针全超时)"
			if domResult.Error != "" {
				item.Error = domResult.Error
			}
		} else {
			item.GFWBlocked = false
			item.StatusDesc = "服务离线/端口未开放"
		}
	}

	return item
}

// getNodeLocationLabel resolves a human-readable location label for a node based on its observed IP and ASN.
func (s *Server) getNodeLocationLabel(node Node) string {
	ip := strings.TrimSpace(node.LastObservedIP)
	if ip == "" {
		ip = strings.TrimSpace(node.ClientAddress)
	}
	if ip != "" {
		asn := wire.IPv4ToASN(ip)
		city := wire.IPv4ToCity(ip)
		if asn != "" && city != "" {
			return wire.ASNToNameWithLocation(asn, city)
		}
		if s.ipLocator != nil {
			if loc := s.ipLocator.Locate(ip); loc != "" && loc != "未知" && loc != "本机" && loc != "内网" {
				if asn != "" {
					return fmt.Sprintf("%s (%s)", wire.ASNToName(asn), loc)
				}
				return loc
			}
		}
		if city != "" {
			return city
		}
	}
	if node.Name != "" {
		return node.Name
	}
	return "本地节点"
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

	nodeLoc := s.getNodeLocationLabel(node)

	targetHost := strings.TrimSpace(node.ClientAddress)
	if targetHost == "" {
		targetHost = strings.TrimSpace(node.LastObservedIP)
	}
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
		payloadBytes, _ := json.Marshal(s.GetSpeedtestEndpoints())
		data, err := s.AskNode(ctx, nodeID, "speedtest.run", string(payloadBytes))
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
		TelecomLatencyMs     int     `json:"telecom_latency_ms"`
		TelecomSpeedMbps     float64 `json:"telecom_speed_mbps"`
		TelecomRoute         string  `json:"telecom_route"`
		TelecomNodeLatencies string  `json:"telecom_node_latencies"`
		TelecomTracePath     string  `json:"telecom_trace_path"`
		UnicomLatencyMs      int     `json:"unicom_latency_ms"`
		UnicomSpeedMbps      float64 `json:"unicom_speed_mbps"`
		UnicomRoute          string  `json:"unicom_route"`
		UnicomNodeLatencies  string  `json:"unicom_node_latencies"`
		UnicomTracePath      string  `json:"unicom_trace_path"`
		MobileLatencyMs      int     `json:"mobile_latency_ms"`
		MobileSpeedMbps      float64 `json:"mobile_speed_mbps"`
		MobileRoute          string  `json:"mobile_route"`
		MobileNodeLatencies  string  `json:"mobile_node_latencies"`
		MobileTracePath      string  `json:"mobile_trace_path"`
	}
	if err := json.Unmarshal([]byte(resA.data), &res); err != nil {
		return nil, fmt.Errorf("解析检测数据失败: %w", err)
	}

	resB := <-probeCh

	enrichOutboundTrace := func(trace, defaultTarget string, latency int) string {
		if trace == "" {
			if latency <= 0 {
				return ""
			}
			return fmt.Sprintf("%s ➔ %s (骨干跃点未回显)", nodeLoc, defaultTarget)
		}
		t := strings.Replace(trace, "节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "本地节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "服务器节点 ➔", nodeLoc+" ➔", 1)
		t = strings.Replace(t, "➔ 目标", "➔ "+defaultTarget, 1)
		if !strings.HasPrefix(t, nodeLoc) && !strings.Contains(t, nodeLoc) {
			t = fmt.Sprintf("%s ➔ %s", nodeLoc, t)
		}
		return t
	}

	st := NodeSpeedtest{
		NodeID:                      nodeID,
		TelecomLatencyMs:            res.TelecomLatencyMs,
		TelecomSpeedMbps:            res.TelecomSpeedMbps,
		TelecomRoute:                res.TelecomRoute,
		TelecomNodeLatencies:        res.TelecomNodeLatencies,
		TelecomTracePath:            enrichOutboundTrace(res.TelecomTracePath, "上海电信", res.TelecomLatencyMs),
		UnicomLatencyMs:             res.UnicomLatencyMs,
		UnicomSpeedMbps:             res.UnicomSpeedMbps,
		UnicomRoute:                 res.UnicomRoute,
		UnicomNodeLatencies:         res.UnicomNodeLatencies,
		UnicomTracePath:             enrichOutboundTrace(res.UnicomTracePath, "上海联通", res.UnicomLatencyMs),
		MobileLatencyMs:             res.MobileLatencyMs,
		MobileSpeedMbps:             res.MobileSpeedMbps,
		MobileRoute:                 res.MobileRoute,
		MobileNodeLatencies:         res.MobileNodeLatencies,
		MobileTracePath:             enrichOutboundTrace(res.MobileTracePath, "广州移动", res.MobileLatencyMs),
		InboundTelecomRoute:         resB.item.InboundTelecomRoute,
		InboundTelecomLatencyMs:     resB.item.InboundTelecomLatencyMs,
		InboundTelecomNodeLatencies: resB.item.InboundTelecomNodeLatencies,
		InboundTelecomTracePath:     resB.item.InboundTelecomTracePath,
		InboundUnicomRoute:          resB.item.InboundUnicomRoute,
		InboundUnicomLatencyMs:      resB.item.InboundUnicomLatencyMs,
		InboundUnicomNodeLatencies:  resB.item.InboundUnicomNodeLatencies,
		InboundUnicomTracePath:      resB.item.InboundUnicomTracePath,
		InboundMobileRoute:          resB.item.InboundMobileRoute,
		InboundMobileLatencyMs:      resB.item.InboundMobileLatencyMs,
		InboundMobileNodeLatencies:  resB.item.InboundMobileNodeLatencies,
		InboundMobileTracePath:      resB.item.InboundMobileTracePath,
	}

	if resB.has {
		if resB.item.GFWBlocked {
			st.GFWBlocked = true
			st.GFWStatus = "blocked"
			st.DomesticLatency = 0
			st.DomesticSource = resB.item.ProbeSource
			st.InboundTelecomRoute = "GFW 阻断"
			st.InboundUnicomRoute = "GFW 阻断"
			st.InboundMobileRoute = "GFW 阻断"
		} else if resB.item.DomesticOK {
			st.GFWBlocked = false
			st.GFWStatus = "normal"
			st.DomesticLatency = resB.item.LatencyMs
			st.DomesticSource = resB.item.ProbeSource
		} else {
			st.GFWBlocked = false
			st.GFWStatus = "normal"
			if resB.item.Error != "" {
				st.DomesticSource = resB.item.Error
			}
			if st.InboundTelecomRoute == "GFW 阻断" {
				st.InboundTelecomRoute = ""
			}
			if st.InboundUnicomRoute == "GFW 阻断" {
				st.InboundUnicomRoute = ""
			}
			if st.InboundMobileRoute == "GFW 阻断" {
				st.InboundMobileRoute = ""
			}
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

type SpeedtestEndpoint struct {
	City string `json:"city"`
	Host string `json:"host"`
}

type SpeedtestEndpointsPayload struct {
	TelecomEndpoints []SpeedtestEndpoint `json:"telecom_endpoints"`
	UnicomEndpoints  []SpeedtestEndpoint `json:"unicom_endpoints"`
	MobileEndpoints  []SpeedtestEndpoint `json:"mobile_endpoints"`
}

func DefaultSpeedtestEndpoints() SpeedtestEndpointsPayload {
	return SpeedtestEndpointsPayload{
		TelecomEndpoints: []SpeedtestEndpoint{
			{City: "上海电信", Host: "202.96.209.133"},
			{City: "广州电信", Host: "58.60.188.222"},
			{City: "北京电信", Host: "219.141.140.10"},
			{City: "成都电信", Host: "61.139.2.69"},
			{City: "全国电信DNS", Host: "114.114.114.114"},
			{City: "武汉电信", Host: "202.103.24.68"},
		},
		UnicomEndpoints: []SpeedtestEndpoint{
			{City: "上海联通", Host: "210.22.97.1"},
			{City: "广州联通", Host: "210.21.196.6"},
			{City: "北京联通", Host: "202.106.195.68"},
			{City: "成都联通", Host: "119.6.6.6"},
			{City: "全国联通DNS", Host: "116.116.116.116"},
			{City: "武汉联通", Host: "58.19.190.1"},
		},
		MobileEndpoints: []SpeedtestEndpoint{
			{City: "广州移动", Host: "120.196.165.24"},
			{City: "上海移动", Host: "211.136.112.200"},
			{City: "北京移动", Host: "221.179.155.161"},
			{City: "成都移动", Host: "211.137.96.205"},
			{City: "全国移动网关", Host: "120.196.165.1"},
			{City: "武汉移动", Host: "111.175.220.1"},
		},
	}
}

func (s *Server) GetSpeedtestEndpoints() SpeedtestEndpointsPayload {
	return DefaultSpeedtestEndpoints()
}
