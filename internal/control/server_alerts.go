package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	st, err := s.ExecuteNodeSpeedtest(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": st, "speedtest": st})
}

func (s *Server) handleRunNodeMTR(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, errors.New("服务器当前处于离线状态，无法进行路由排障诊断"))
		return
	}

	var req struct {
		Target string `json:"target"`
		Count  int    `json:"count"`
		Label  string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Target == "" {
		req.Target = "202.96.209.133"
		if req.Label == "" {
			req.Label = "上海电信"
		}
	}
	if req.Count <= 0 || req.Count > 30 {
		req.Count = 10
	}

	payloadBytes, _ := json.Marshal(req)
	data, err := s.AskNode(r.Context(), nodeID, "diagnose.mtr", string(payloadBytes))
	if err != nil {
		writeError(w, fmt.Errorf("执行 MTR 逐跳丢包排障失败: %w", err))
		return
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(data), &report); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"raw": data, "summary": data, "raw_output": data})
		return
	}

	response := make(map[string]any)
	for k, v := range report {
		response[k] = v
	}
	response["report"] = report
	response["result"] = report
	writeJSON(w, http.StatusOK, response)
}

type MTRTargetOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
	IP    string `json:"ip"`
}

func (s *Server) GetMTRTargets() []MTRTargetOption {
	return []MTRTargetOption{
		{Label: "上海电信 (AS4134 华东核心)", Value: "sh_telecom", IP: "202.96.209.133"},
		{Label: "广州电信 (AS4134 华南核心)", Value: "gz_telecom", IP: "58.60.188.222"},
		{Label: "北京电信 (AS4134 华北核心)", Value: "bj_telecom", IP: "219.141.140.10"},
		{Label: "成都电信 (AS4134 西南核心)", Value: "cd_telecom", IP: "61.139.2.69"},
		{Label: "全国电信公共网关 (114.114.114.114)", Value: "dns_telecom", IP: "114.114.114.114"},
		{Label: "上海联通 (AS4837 华东核心)", Value: "sh_unicom", IP: "210.22.97.1"},
		{Label: "广州联通 (AS4837 华南核心)", Value: "gz_unicom", IP: "210.21.196.6"},
		{Label: "北京联通 (AS4837 华北核心)", Value: "bj_unicom", IP: "202.106.195.68"},
		{Label: "成都联通 (AS4837 西南核心)", Value: "cd_unicom", IP: "119.6.6.6"},
		{Label: "全国联通公共网关 (116.116.116.116)", Value: "dns_unicom", IP: "116.116.116.116"},
		{Label: "广州移动 (AS9808 华南核心)", Value: "gz_mobile", IP: "120.196.165.24"},
		{Label: "上海移动 (AS9808 华东核心)", Value: "sh_mobile", IP: "211.136.112.200"},
		{Label: "北京移动 (AS9808 华北核心)", Value: "bj_mobile", IP: "221.179.155.161"},
		{Label: "成都移动 (AS9808 西南核心)", Value: "cd_mobile", IP: "211.137.96.205"},
		{Label: "全国移动核心网关 (120.196.165.1)", Value: "gw_mobile", IP: "120.196.165.1"},
		{Label: "自定义 IP / 域名", Value: "custom", IP: ""},
	}
}

func (s *Server) handleGetMTRTargets(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": s.GetMTRTargets()})
}

func (s *Server) handleGetNodeSpeedtestLatest(w http.ResponseWriter, r *http.Request) {
	if _, err := s.operator(r, false); err != nil {
		writeError(w, err)
		return
	}

	nodeID := r.PathValue("id")
	st, err := s.store.GetLatestNodeSpeedtest(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"has_test": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"has_test": true, "speedtest": st, "result": st})
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
	writeJSON(w, http.StatusOK, map[string]any{"speedtests": items, "speedtests_map": list})
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
