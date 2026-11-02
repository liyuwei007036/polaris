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
	"strings"
	"time"
)

func (s *Server) getAlertSettings(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	settings, err := s.store.GetAlertSettings(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (s *Server) updateAlertSettings(w http.ResponseWriter, r *http.Request) {
	op, err := s.operator(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	if op.Role != "admin" && op.Role != "operator" {
		writeError(w, ErrForbidden)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		writeError(w, userErrorf("读取告警配置失败: %v", err))
		return
	}

	var in AlertSettings
	if err := json.Unmarshal(bodyBytes, &in); err != nil {
		writeError(w, userErrorf("提交的告警配置格式无效: %v", err))
		return
	}

	// Also support alternate / legacy frontend field names
	var raw map[string]any
	if err := json.Unmarshal(bodyBytes, &raw); err == nil {
		if v, ok := raw["bandwidth_mbps"].(float64); ok && in.TrafficThresholdMbps == 0 {
			in.TrafficThresholdMbps = v
		}
		if v, ok := raw["traffic_window_seconds"].(float64); ok && in.TrafficDurationSec == 0 {
			in.TrafficDurationSec = int(v)
		}
		if v, ok := raw["conn_count_max"].(float64); ok && in.ConnThresholdCount == 0 {
			in.ConnThresholdCount = int(v)
		}
		if v, ok := raw["single_ip_conn_max"].(float64); ok && in.SingleIPThresholdCount == 0 {
			in.SingleIPThresholdCount = int(v)
		}
		if v, ok := raw["thresholds_enabled"].(bool); ok {
			in.TrafficAlertEnabled = v
			in.ConnAlertEnabled = v
			in.SingleIPAlertEnabled = v
		}
		if v, ok := raw["probing_enabled"].(bool); ok {
			in.AutoProbeGFWEnabled = v
			in.AutoProbeSpeedtestEnabled = v
		}
		if v, ok := raw["gfw_probe_interval_minutes"].(float64); ok && in.ProbeGFWIntervalMinutes == 0 {
			in.ProbeGFWIntervalMinutes = int(v)
		}
		if v, ok := raw["speedtest_interval_hours"].(float64); ok && in.ProbeSpeedtestIntervalHours == 0 {
			in.ProbeSpeedtestIntervalHours = int(v)
		}
		if v, ok := raw["speedtest_skip_if_busy"].(bool); ok {
			in.ProbeSkipWhenBusy = v
		}
	}

	updated, err := s.store.UpdateAlertSettings(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	if s.alertEngine != nil {
		s.alertEngine.InvalidateCache()
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": updated})
}

func (s *Server) testAlert(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	var in struct {
		Server    string `json:"server"`
		DeviceKey string `json:"device_key"`
		Sound     string `json:"sound"`
		Group     string `json:"group"`
		URL       string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)

	server := strings.TrimSpace(in.Server)
	deviceKey := strings.TrimSpace(in.DeviceKey)
	sound := strings.TrimSpace(in.Sound)
	group := strings.TrimSpace(in.Group)
	url := strings.TrimSpace(in.URL)

	if deviceKey == "" {
		settings, err := s.store.GetAlertSettings(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if server == "" {
			server = settings.BarkServer
		}
		deviceKey = settings.BarkDeviceKey
		if sound == "" {
			sound = settings.BarkSound
		}
		if group == "" {
			group = settings.BarkGroup
		}
	}
	if deviceKey == "" {
		writeError(w, userErrorf("请先填写 Bark Device Key"))
		return
	}
	if server == "" {
		server = "https://api.day.app"
	}
	if sound == "" {
		sound = "minuet.caf"
	}
	if group == "" {
		group = "Polaris"
	}

	testCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	client := NewBarkClient()
	testBody := fmt.Sprintf("• 系统状态: 正常在线\n• 推送通道: Apple APNs (Bark)\n• 提示铃声: %s\n• 分组标识: %s\n🕒 发送时间: %s",
		sound, group, NowAlertTime())
	msg := BarkMessage{
		Title: "🔔 [Polaris] 测试推送成功",
		Body:  testBody,
		Group: group,
		Sound: sound,
		Level: "active",
		URL:   url,
	}

	if err := client.Send(testCtx, server, deviceKey, msg); err != nil {
		writeError(w, userErrorf("Bark 推送失败: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "测试通知已成功推送到你的 Bark 设备！"})
}

func (s *Server) handleRunGFWCheck(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	results, err := s.runGFWCheckAll(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "nodes": results})
}

func (s *Server) handleRunNodeSpeedtest(w http.ResponseWriter, r *http.Request) {
	op, err := s.operator(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	if op.Role != "admin" && op.Role != "operator" {
		writeError(w, ErrForbidden)
		return
	}

	nodeID := r.PathValue("id")
	if nodeID == "" {
		writeError(w, errors.New("缺少节点 ID"))
		return
	}

	node, err := s.store.GetNode(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !node.Online {
		writeError(w, errors.New("服务器当前处于离线状态，无法进行检测"))
		return
	}

	s.speedtestMu.Lock()
	if s.speedtestingNodes[nodeID] {
		s.speedtestMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "running",
			"running": true,
			"message": "该节点已在进行 TcpQuality 检测中，请稍候...",
		})
		return
	}
	s.speedtestingNodes[nodeID] = true
	s.speedtestMu.Unlock()

	// Launch autonomous speedtest asynchronously in background:
	// Decoupled from HTTP request context so reverse proxies (Nginx 60s/Cloudflare 100s) never abort it.
	go func(targetNodeID string) {
		defer func() {
			s.speedtestMu.Lock()
			delete(s.speedtestingNodes, targetNodeID)
			s.speedtestMu.Unlock()
		}()
		testCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute+30*time.Second)
		defer cancel()

		st, err := s.ExecuteNodeSpeedtest(testCtx, targetNodeID)
		if err != nil {
			log.Printf("node %s speedtest failed: %v", targetNodeID, err)
		}
		s.liveHub.publish(liveEvent{Kind: "speedtest", NodeID: targetNodeID})
		s.liveHub.publish(liveEvent{Kind: "node", NodeID: targetNodeID})
		_ = st
	}(nodeID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "started",
		"running": true,
		"message": "TcpQuality 综合质量检测已在后台启动（预计耗时 1~2 分钟）",
	})
}

func (s *Server) handleGetNodeSpeedtestLatest(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	nodeID := r.PathValue("id")
	s.speedtestMu.Lock()
	running := s.speedtestingNodes[nodeID]
	s.speedtestMu.Unlock()

	st, err := s.store.GetLatestNodeSpeedtest(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"has_test": false, "running": running})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"has_test": true, "running": running, "speedtest": st, "result": st})
}

func (s *Server) handleListNodeSpeedtestsLatest(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	list, err := s.store.ListLatestNodeSpeedtests(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]NodeSpeedtest, 0, len(list))
	for _, st := range list {
		items = append(items, st)
	}

	s.speedtestMu.Lock()
	runningMap := make(map[string]bool, len(s.speedtestingNodes))
	for k, v := range s.speedtestingNodes {
		runningMap[k] = v
	}
	s.speedtestMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"speedtests":     items,
		"speedtests_map": list,
		"running":        runningMap,
	})
}

func (s *Server) handleCheckRealityCandidate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	var req struct {
		Domain string `json:"domain"`
		Target string `json:"target"`
		Port   uint16 `json:"port"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	targetDomain := strings.TrimSpace(req.Domain)
	if targetDomain == "" {
		targetDomain = strings.TrimSpace(req.Target)
	}
	if targetDomain == "" {
		writeError(w, userErrorf("请输入有效的待检测伪装域名"))
		return
	}

	report, err := CheckRealityTarget(r.Context(), targetDomain, req.Port)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reality_check": report,
		"report":        report,
	})
}

func (s *Server) handleClientInfo(w http.ResponseWriter, r *http.Request) {
	clientIP := ""
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		clientIP = strings.TrimSpace(parts[0])
	}
	if clientIP == "" {
		clientIP = strings.TrimSpace(r.Header.Get("X-Real-IP"))
	}
	if clientIP == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			clientIP = host
		} else {
			clientIP = r.RemoteAddr
		}
	}

	info := LocationInfo{IP: clientIP}
	if s.ipLocator != nil && clientIP != "" {
		info = s.ipLocator.Lookup(clientIP)
	}

	isIPv6 := false
	if parsed := net.ParseIP(clientIP); parsed != nil && parsed.To4() == nil {
		isIPv6 = true
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ip":          clientIP,
		"is_ipv6":     isIPv6,
		"location":    info.Location,
		"city":        info.City,
		"country":     info.Country,
		"province":    info.Province,
		"isp":         info.ISP,
		"asn":         info.ASN,
		"is_domestic": info.IsDomestic,
	})
}

func (s *Server) handleProbeNodePeers(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	targetNodeID := r.URL.Query().Get("target_node_id")

	res, err := s.ProbeNodePeers(r.Context(), nodeID, targetNodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleProbeNodeClient(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")

	var req struct {
		ClientIP string `json:"client_ip"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	clientIP := strings.TrimSpace(req.ClientIP)
	if clientIP == "" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			clientIP = strings.TrimSpace(parts[0])
		}
		if clientIP == "" {
			clientIP = strings.TrimSpace(r.Header.Get("X-Real-IP"))
		}
		if clientIP == "" {
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				clientIP = host
			} else {
				clientIP = r.RemoteAddr
			}
		}
	}

	res, err := s.ProbeNodeClient(r.Context(), nodeID, clientIP)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
