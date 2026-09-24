package control

import (
	"context"
	"encoding/json"
	"errors"
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
		sound = "minuet"
	}
	if group == "" {
		group = "Polaris"
	}

	testCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	client := NewBarkClient()
	msg := BarkMessage{
		Title: "🔔 [Polaris] 测试推送成功",
		Body:  "这是一条来自 Polaris 代理管理平台的测试通知。你的 Bark 告警链路已正常连通！\n发送时间: " + time.Now().Format("15:04:05"),
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
