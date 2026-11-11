package control

import (
	"context"
	"encoding/json"
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
		Server           string `json:"server"`
		DeviceKey        string `json:"device_key"`
		Sound            string `json:"sound"`
		Group            string `json:"group"`
		TelegramServer   string `json:"telegram_server"`
		TelegramBotToken string `json:"telegram_bot_token"`
		TelegramChatID   string `json:"telegram_chat_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)

	settings, err := s.store.GetAlertSettings(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}

	pick := func(value, fallback string) string {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(fallback)
	}

	server := strings.TrimRight(pick(in.Server, settings.BarkServer), "/")
	if server == "" {
		server = "https://api.day.app"
	}
	deviceKey := pick(in.DeviceKey, settings.BarkDeviceKey)
	sound := pick(in.Sound, settings.BarkSound)
	group := pick(in.Group, settings.BarkGroup)
	if sound == "" {
		sound = "minuet.caf"
	}
	if group == "" {
		group = "Polaris"
	}

	// Explicit credentials in the request bypass the saved "enabled" switch so
	// operators can validate a token before turning the channel on.
	telegramExplicit := strings.TrimSpace(in.TelegramBotToken) != "" || strings.TrimSpace(in.TelegramChatID) != ""
	telegramServer := strings.TrimRight(pick(in.TelegramServer, settings.TelegramServer), "/")
	if telegramServer == "" {
		telegramServer = telegramDefaultAPIBase
	}
	telegramToken := pick(in.TelegramBotToken, settings.TelegramBotToken)
	telegramChatID := pick(in.TelegramChatID, settings.TelegramChatID)
	telegramReady := telegramToken != "" && telegramChatID != "" && (telegramExplicit || settings.TelegramEnabled)

	if deviceKey == "" && !telegramReady {
		writeError(w, userErrorf("请先填写 Bark Device Key，或启用并配置 Telegram Bot Token 与 Chat ID"))
		return
	}

	testCtx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	var sent, failures []string

	if deviceKey != "" {
		testBody := fmt.Sprintf("通道已连通，告警将按事件类型自动分组。\n铃声：%s\n分组：%s\n时间：%s", sound, group, NowAlertTime())
		msg := BarkMessage{
			Title: "[信息] Bark 测试推送",
			Body:  testBody,
			Group: group,
			Sound: sound,
			Level: "active",
		}
		if err := NewBarkClient().Send(testCtx, server, deviceKey, msg); err != nil {
			failures = append(failures, "Bark: "+err.Error())
		} else {
			sent = append(sent, "Bark")
		}
	}

	if telegramReady {
		text := buildTelegramAlertFor("通道测试", severityInfo, "通道测试", "Telegram Bot 已连通", "", []string{
			tgLine("通道", "Telegram Bot API"),
			tgCodeLine("目标会话", telegramChatID),
			tgLine("消息格式", "HTML 富文本"),
		}, "")
		if err := NewTelegramClient().SendHTML(testCtx, telegramServer, telegramToken, telegramChatID, text); err != nil {
			failures = append(failures, "Telegram: "+err.Error())
		} else {
			sent = append(sent, "Telegram")
		}
	}

	if len(failures) > 0 {
		prefix := ""
		if len(sent) > 0 {
			prefix = "已发送 " + strings.Join(sent, "、") + "；"
		}
		writeError(w, userErrorf("%s发送失败: %s", prefix, strings.Join(failures, "; ")))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"channels": sent,
		"message":  "测试通知已成功推送到 " + strings.Join(sent, "、") + "！",
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

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"pong"}`))
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
