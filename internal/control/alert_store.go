package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AlertSettings struct {
	BarkServer             string  `json:"bark_server"`
	BarkDeviceKey          string  `json:"bark_device_key"`
	BarkSound              string  `json:"bark_sound"`
	BarkGroup              string  `json:"bark_group"`
	TelegramEnabled        bool    `json:"telegram_enabled"`
	TelegramServer         string  `json:"telegram_server"`
	TelegramBotToken       string  `json:"telegram_bot_token"`
	TelegramChatID         string  `json:"telegram_chat_id"`
	TrafficAlertEnabled    bool    `json:"traffic_alert_enabled"`
	TrafficThresholdMbps   float64 `json:"traffic_threshold_mbps"`
	TrafficDurationSec     int     `json:"traffic_duration_sec"`
	ConnAlertEnabled       bool    `json:"conn_alert_enabled"`
	ConnThresholdCount     int     `json:"conn_threshold_count"`
	SingleIPAlertEnabled   bool    `json:"single_ip_alert_enabled"`
	SingleIPThresholdCount int     `json:"single_ip_threshold_count"`
	OfflineAlertEnabled    bool    `json:"offline_alert_enabled"`
	CooldownMinutes        int     `json:"cooldown_minutes"`
	ScanAlertEnabled       bool    `json:"scan_alert_enabled"`
	UpdatedAt              string  `json:"updated_at,omitempty"`
}

// BarkReady reports whether the Bark channel has enough configuration to push.
func (s AlertSettings) BarkReady() bool {
	return strings.TrimSpace(s.BarkDeviceKey) != ""
}

// TelegramReady reports whether the Telegram channel is enabled and configured.
func (s AlertSettings) TelegramReady() bool {
	return s.TelegramEnabled &&
		strings.TrimSpace(s.TelegramBotToken) != "" &&
		strings.TrimSpace(s.TelegramChatID) != ""
}

// HasNotifier reports whether at least one push channel is available.
func (s AlertSettings) HasNotifier() bool {
	return s.BarkReady() || s.TelegramReady()
}

func (s *Store) GetAlertSettings(ctx context.Context) (AlertSettings, error) {
	var settings AlertSettings
	var updatedAt int64
	var (
		trafficAlert, connAlert, singleIPAlert, offlineAlert int
		scanAlert, telegramEnabled                           int
	)

	row := s.db.QueryRowContext(ctx, `SELECT 
		bark_server, bark_device_key, bark_sound, bark_group, bark_url,
		telegram_enabled, telegram_server, telegram_bot_token, telegram_chat_id,
		traffic_alert_enabled, traffic_threshold_mbps, traffic_duration_sec,
		conn_alert_enabled, conn_threshold_count,
		single_ip_alert_enabled, single_ip_threshold_count,
		offline_alert_enabled, cooldown_minutes,
		scan_alert_enabled, updated_at
		FROM alert_settings WHERE id = 1`)

	var ignoredBarkURL string
	err := row.Scan(
		&settings.BarkServer, &settings.BarkDeviceKey, &settings.BarkSound, &settings.BarkGroup, &ignoredBarkURL,
		&telegramEnabled, &settings.TelegramServer, &settings.TelegramBotToken, &settings.TelegramChatID,
		&trafficAlert, &settings.TrafficThresholdMbps, &settings.TrafficDurationSec,
		&connAlert, &settings.ConnThresholdCount,
		&singleIPAlert, &settings.SingleIPThresholdCount,
		&offlineAlert, &settings.CooldownMinutes,
		&scanAlert, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Default settings
		return AlertSettings{
			BarkServer:             "https://api.day.app",
			BarkSound:              "minuet",
			BarkGroup:              "Polaris",
			TelegramEnabled:        false,
			TelegramServer:         telegramDefaultAPIBase,
			TrafficAlertEnabled:    true,
			TrafficThresholdMbps:   50.0,
			TrafficDurationSec:     10,
			ConnAlertEnabled:       true,
			ConnThresholdCount:     200,
			SingleIPAlertEnabled:   true,
			SingleIPThresholdCount: 60,
			OfflineAlertEnabled:    true,
			CooldownMinutes:        15,
			ScanAlertEnabled:       false,
		}, nil
	}
	if err != nil {
		return AlertSettings{}, fmt.Errorf("load alert settings: %w", err)
	}

	settings.TrafficAlertEnabled = trafficAlert != 0
	settings.TelegramEnabled = telegramEnabled != 0
	if strings.TrimSpace(settings.TelegramServer) == "" {
		settings.TelegramServer = telegramDefaultAPIBase
	}
	settings.ConnAlertEnabled = connAlert != 0
	settings.SingleIPAlertEnabled = singleIPAlert != 0
	settings.OfflineAlertEnabled = offlineAlert != 0
	settings.ScanAlertEnabled = scanAlert != 0
	if updatedAt > 0 {
		settings.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	}
	return settings, nil
}

func (s *Store) UpdateAlertSettings(ctx context.Context, in AlertSettings) (AlertSettings, error) {
	server := strings.TrimRight(strings.TrimSpace(in.BarkServer), "/")
	if server == "" {
		server = "https://api.day.app"
	}
	sound := strings.TrimSpace(in.BarkSound)
	if sound == "" {
		sound = "minuet"
	}
	group := strings.TrimSpace(in.BarkGroup)
	if group == "" {
		group = "Polaris"
	}
	url := ""
	_ = url
	telegramServer := strings.TrimRight(strings.TrimSpace(in.TelegramServer), "/")
	if telegramServer == "" {
		telegramServer = telegramDefaultAPIBase
	}
	telegramToken := strings.TrimSpace(in.TelegramBotToken)
	telegramChatID := strings.TrimSpace(in.TelegramChatID)
	cooldown := in.CooldownMinutes
	if cooldown < 1 {
		cooldown = 1
	}

	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_settings (
		id, bark_server, bark_device_key, bark_sound, bark_group, bark_url,
		telegram_enabled, telegram_server, telegram_bot_token, telegram_chat_id,
		traffic_alert_enabled, traffic_threshold_mbps, traffic_duration_sec,
		conn_alert_enabled, conn_threshold_count,
		single_ip_alert_enabled, single_ip_threshold_count,
		offline_alert_enabled, cooldown_minutes,
		scan_alert_enabled, updated_at
	) VALUES (
		1, ?, ?, ?, ?, ?,
		?, ?, ?, ?,
		?, ?, ?,
		?, ?,
		?, ?,
		?, ?,
		?, ?
	) ON CONFLICT(id) DO UPDATE SET
		bark_server=excluded.bark_server,
		bark_device_key=excluded.bark_device_key,
		bark_sound=excluded.bark_sound,
		bark_group=excluded.bark_group,
		bark_url=excluded.bark_url,
		telegram_enabled=excluded.telegram_enabled,
		telegram_server=excluded.telegram_server,
		telegram_bot_token=excluded.telegram_bot_token,
		telegram_chat_id=excluded.telegram_chat_id,
		traffic_alert_enabled=excluded.traffic_alert_enabled,
		traffic_threshold_mbps=excluded.traffic_threshold_mbps,
		traffic_duration_sec=excluded.traffic_duration_sec,
		conn_alert_enabled=excluded.conn_alert_enabled,
		conn_threshold_count=excluded.conn_threshold_count,
		single_ip_alert_enabled=excluded.single_ip_alert_enabled,
		single_ip_threshold_count=excluded.single_ip_threshold_count,
		offline_alert_enabled=excluded.offline_alert_enabled,
		cooldown_minutes=excluded.cooldown_minutes,
		scan_alert_enabled=excluded.scan_alert_enabled,
		updated_at=excluded.updated_at`,
		server, strings.TrimSpace(in.BarkDeviceKey), sound, group, url,
		boolToInt(in.TelegramEnabled), telegramServer, telegramToken, telegramChatID,
		boolToInt(in.TrafficAlertEnabled), in.TrafficThresholdMbps, in.TrafficDurationSec,
		boolToInt(in.ConnAlertEnabled), in.ConnThresholdCount,
		boolToInt(in.SingleIPAlertEnabled), in.SingleIPThresholdCount,
		boolToInt(in.OfflineAlertEnabled), cooldown,
		boolToInt(in.ScanAlertEnabled), now,
	)
	if err != nil {
		return AlertSettings{}, fmt.Errorf("update alert settings: %w", err)
	}
	return s.GetAlertSettings(ctx)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
