package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
		NodeID:   node.ID,
		NodeName: node.Name,
	}

	// 1. Overseas check (Direct TCP dial from master)
	dialer := net.Dialer{Timeout: 3 * time.Second}
	started := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err == nil {
		_ = conn.Close()
		item.OverseasOK = true
		item.LatencyMs = int(time.Since(started).Milliseconds())
	} else {
		item.OverseasOK = false
		item.Error = err.Error()
	}

	// 2. Domestic check
	// If overseas failed, domestic check is not GFW blocked, just offline.
	if !item.OverseasOK {
		item.DomesticOK = false
		return item
	}

	// Probe from domestic perspective
	domesticOK := probeFromChina(ctx, host, port)
	item.DomesticOK = domesticOK
	return item
}

// probeFromChina uses public lightweight TCP/HTTP reachability checks from domestic points.
func probeFromChina(ctx context.Context, host string, port uint16) bool {
	// Attempt check-host.net TCP check API
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	apiURL := fmt.Sprintf("https://check-host.net/check-tcp?host=%s&max_nodes=3", url.QueryEscape(net.JoinHostPort(host, strconv.Itoa(int(port)))))
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, apiURL, nil)
	if err != nil {
		return true // Fallback to healthy if prober API fails
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return true // Fallback to assuming reachable if third-party is unreachable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return true
	}

	var initResp struct {
		OK        int               `json:"ok"`
		RequestID string            `json:"request_id"`
		Nodes     map[string]any    `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&initResp); err != nil || initResp.RequestID == "" {
		return true
	}

	// Wait 2 seconds for node probes to collect results
	select {
	case <-time.After(2 * time.Second):
	case <-probeCtx.Done():
		return true
	}

	resultURL := fmt.Sprintf("https://check-host.net/check-result/%s", initResp.RequestID)
	resultReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, resultURL, nil)
	if err != nil {
		return true
	}
	resultReq.Header.Set("Accept", "application/json")

	resultResp, err := client.Do(resultReq)
	if err != nil {
		return true
	}
	defer resultResp.Body.Close()

	if resultResp.StatusCode != http.StatusOK {
		return true
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(resultResp.Body, 10240))
	if err != nil {
		return true
	}

	// Parse check result: looking for China nodes e.g. "cn" or overall verdicts
	var rawMap map[string]any
	if err := json.Unmarshal(bodyBytes, &rawMap); err != nil {
		return true
	}

	hasCNNode := false
	cnFailed := false
	for nodeKey, nodeResult := range rawMap {
		if strings.Contains(strings.ToLower(nodeKey), ".cn") || strings.Contains(strings.ToLower(nodeKey), "china") {
			hasCNNode = true
			// If result is null or contains error, consider failed
			if nodeResult == nil {
				cnFailed = true
				continue
			}
			resArr, ok := nodeResult.([]any)
			if ok && len(resArr) > 0 {
				first, isMap := resArr[0].(map[string]any)
				if isMap {
					if _, hasErr := first["error"]; hasErr {
						cnFailed = true
					}
				}
			}
		}
	}

	if hasCNNode && cnFailed {
		return false // China node explicitly failed to connect
	}

	return true
}

// ExecuteNodeSpeedtest runs a three-network speedtest task on an agent and records the result.
func (s *Server) ExecuteNodeSpeedtest(ctx context.Context, nodeID string) (*NodeSpeedtest, error) {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if !node.Online {
		return nil, errors.New("服务器当前处于离线状态，无法进行测速")
	}

	data, err := s.AskNode(ctx, nodeID, "speedtest.run", "{}")
	if err != nil {
		return nil, fmt.Errorf("执行测速任务失败: %w", err)
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
		return nil, fmt.Errorf("解析测速数据失败: %w", err)
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
