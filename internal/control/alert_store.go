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
	BarkServer                string  `json:"bark_server"`
	BarkDeviceKey             string  `json:"bark_device_key"`
	BarkSound                 string  `json:"bark_sound"`
	BarkGroup                 string  `json:"bark_group"`
	BarkURL                   string  `json:"bark_url"`
	TrafficAlertEnabled       bool    `json:"traffic_alert_enabled"`
	TrafficThresholdMbps      float64 `json:"traffic_threshold_mbps"`
	TrafficDurationSec        int     `json:"traffic_duration_sec"`
	ConnAlertEnabled          bool    `json:"conn_alert_enabled"`
	ConnThresholdCount        int     `json:"conn_threshold_count"`
	SingleIPAlertEnabled      bool    `json:"single_ip_alert_enabled"`
	SingleIPThresholdCount    int     `json:"single_ip_threshold_count"`
	GFWAlertEnabled           bool    `json:"gfw_alert_enabled"`
	OfflineAlertEnabled       bool    `json:"offline_alert_enabled"`
	CooldownMinutes           int     `json:"cooldown_minutes"`
	AutoProbeGFWEnabled       bool    `json:"auto_probe_gfw_enabled"`
	ProbeGFWIntervalMinutes   int     `json:"probe_gfw_interval_minutes"`
	AutoProbeSpeedtestEnabled bool    `json:"auto_probe_speedtest_enabled"`
	ProbeSpeedtestIntervalHours int   `json:"probe_speedtest_interval_hours"`
	ProbeSkipWhenBusy         bool    `json:"probe_skip_when_busy"`
	ProbeNotifyAlways         bool    `json:"probe_notify_always"`
	ScanAlertEnabled          bool    `json:"scan_alert_enabled"`
	UpdatedAt                 string  `json:"updated_at,omitempty"`
}

type NodeSpeedtest struct {
	ID                      string  `json:"id"`
	NodeID                  string  `json:"node_id"`
	// 返程 (Outbound)
	TelecomLatencyMs        int     `json:"telecom_latency_ms"`
	TelecomSpeedMbps        float64 `json:"telecom_speed_mbps"`
	TelecomRoute            string  `json:"telecom_route"`
	TelecomNodeLatencies    string  `json:"telecom_node_latencies,omitempty"`
	TelecomTracePath        string  `json:"telecom_trace_path,omitempty"`
	UnicomLatencyMs         int     `json:"unicom_latency_ms"`
	UnicomSpeedMbps         float64 `json:"unicom_speed_mbps"`
	UnicomRoute             string  `json:"unicom_route"`
	UnicomNodeLatencies     string  `json:"unicom_node_latencies,omitempty"`
	UnicomTracePath         string  `json:"unicom_trace_path,omitempty"`
	MobileLatencyMs         int     `json:"mobile_latency_ms"`
	MobileSpeedMbps         float64 `json:"mobile_speed_mbps"`
	MobileRoute             string  `json:"mobile_route"`
	MobileNodeLatencies     string  `json:"mobile_node_latencies,omitempty"`
	MobileTracePath         string  `json:"mobile_trace_path,omitempty"`
	// 去程 (Inbound)
	InboundTelecomRoute         string `json:"inbound_telecom_route"`
	InboundTelecomLatencyMs     int    `json:"inbound_telecom_latency_ms"`
	InboundTelecomNodeLatencies string `json:"inbound_telecom_node_latencies,omitempty"`
	InboundTelecomTracePath     string `json:"inbound_telecom_trace_path,omitempty"`
	InboundUnicomRoute          string `json:"inbound_unicom_route"`
	InboundUnicomLatencyMs      int    `json:"inbound_unicom_latency_ms"`
	InboundUnicomNodeLatencies  string `json:"inbound_unicom_node_latencies,omitempty"`
	InboundUnicomTracePath      string `json:"inbound_unicom_trace_path,omitempty"`
	InboundMobileRoute          string `json:"inbound_mobile_route"`
	InboundMobileLatencyMs      int    `json:"inbound_mobile_latency_ms"`
	InboundMobileNodeLatencies  string `json:"inbound_mobile_node_latencies,omitempty"`
	InboundMobileTracePath      string `json:"inbound_mobile_trace_path,omitempty"`
	// GFW 连通性
	GFWBlocked              bool    `json:"gfw_blocked"`
	GFWStatus               string  `json:"gfw_status"`
	DomesticLatency         int     `json:"domestic_latency_ms"`
	DomesticSource          string  `json:"domestic_source"`
	TestedAt                string  `json:"tested_at"`
}

func (s *Store) GetAlertSettings(ctx context.Context) (AlertSettings, error) {
	var settings AlertSettings
	var updatedAt int64
	var (
		trafficAlert, connAlert, singleIPAlert, gfwAlert, offlineAlert int
		autoGFW, autoSpeed, skipBusy, notifyAlways                     int
		scanAlert                                                      int
	)

	row := s.db.QueryRowContext(ctx, `SELECT 
		bark_server, bark_device_key, bark_sound, bark_group, bark_url,
		traffic_alert_enabled, traffic_threshold_mbps, traffic_duration_sec,
		conn_alert_enabled, conn_threshold_count,
		single_ip_alert_enabled, single_ip_threshold_count,
		gfw_alert_enabled, offline_alert_enabled, cooldown_minutes,
		auto_probe_gfw_enabled, probe_gfw_interval_minutes,
		auto_probe_speedtest_enabled, probe_speedtest_interval_hours,
		probe_skip_when_busy, probe_notify_always, scan_alert_enabled, updated_at
		FROM alert_settings WHERE id = 1`)

	err := row.Scan(
		&settings.BarkServer, &settings.BarkDeviceKey, &settings.BarkSound, &settings.BarkGroup, &settings.BarkURL,
		&trafficAlert, &settings.TrafficThresholdMbps, &settings.TrafficDurationSec,
		&connAlert, &settings.ConnThresholdCount,
		&singleIPAlert, &settings.SingleIPThresholdCount,
		&gfwAlert, &offlineAlert, &settings.CooldownMinutes,
		&autoGFW, &settings.ProbeGFWIntervalMinutes,
		&autoSpeed, &settings.ProbeSpeedtestIntervalHours,
		&skipBusy, &notifyAlways, &scanAlert, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// Default settings
		return AlertSettings{
			BarkServer:                  "https://api.day.app",
			BarkSound:                   "minuet",
			BarkGroup:                   "Polaris",
			BarkURL:                     "",
			TrafficAlertEnabled:         true,
			TrafficThresholdMbps:        50.0,
			TrafficDurationSec:          10,
			ConnAlertEnabled:            true,
			ConnThresholdCount:          200,
			SingleIPAlertEnabled:        true,
			SingleIPThresholdCount:      60,
			GFWAlertEnabled:             true,
			OfflineAlertEnabled:         true,
			CooldownMinutes:             15,
			AutoProbeGFWEnabled:         true,
			ProbeGFWIntervalMinutes:     30,
			AutoProbeSpeedtestEnabled:   true,
			ProbeSpeedtestIntervalHours: 6,
			ProbeSkipWhenBusy:           true,
			ProbeNotifyAlways:           true,
			ScanAlertEnabled:            false,
		}, nil
	}
	if err != nil {
		return AlertSettings{}, fmt.Errorf("load alert settings: %w", err)
	}

	settings.TrafficAlertEnabled = trafficAlert != 0
	settings.ConnAlertEnabled = connAlert != 0
	settings.SingleIPAlertEnabled = singleIPAlert != 0
	settings.GFWAlertEnabled = gfwAlert != 0
	settings.OfflineAlertEnabled = offlineAlert != 0
	settings.AutoProbeGFWEnabled = autoGFW != 0
	settings.AutoProbeSpeedtestEnabled = autoSpeed != 0
	settings.ProbeSkipWhenBusy = skipBusy != 0
	settings.ProbeNotifyAlways = notifyAlways != 0
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
	url := strings.TrimSpace(in.BarkURL)
	cooldown := in.CooldownMinutes
	if cooldown < 1 {
		cooldown = 1
	}
	gfwInterval := in.ProbeGFWIntervalMinutes
	if gfwInterval < 1 {
		gfwInterval = 1
	}
	speedInterval := in.ProbeSpeedtestIntervalHours
	if speedInterval < 1 {
		speedInterval = 1
	}

	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_settings (
		id, bark_server, bark_device_key, bark_sound, bark_group, bark_url,
		traffic_alert_enabled, traffic_threshold_mbps, traffic_duration_sec,
		conn_alert_enabled, conn_threshold_count,
		single_ip_alert_enabled, single_ip_threshold_count,
		gfw_alert_enabled, offline_alert_enabled, cooldown_minutes,
		auto_probe_gfw_enabled, probe_gfw_interval_minutes,
		auto_probe_speedtest_enabled, probe_speedtest_interval_hours,
		probe_skip_when_busy, probe_notify_always, scan_alert_enabled, updated_at
	) VALUES (
		1, ?, ?, ?, ?, ?,
		?, ?, ?,
		?, ?,
		?, ?,
		?, ?, ?,
		?, ?,
		?, ?,
		?, ?, ?, ?
	) ON CONFLICT(id) DO UPDATE SET
		bark_server=excluded.bark_server,
		bark_device_key=excluded.bark_device_key,
		bark_sound=excluded.bark_sound,
		bark_group=excluded.bark_group,
		bark_url=excluded.bark_url,
		traffic_alert_enabled=excluded.traffic_alert_enabled,
		traffic_threshold_mbps=excluded.traffic_threshold_mbps,
		traffic_duration_sec=excluded.traffic_duration_sec,
		conn_alert_enabled=excluded.conn_alert_enabled,
		conn_threshold_count=excluded.conn_threshold_count,
		single_ip_alert_enabled=excluded.single_ip_alert_enabled,
		single_ip_threshold_count=excluded.single_ip_threshold_count,
		gfw_alert_enabled=excluded.gfw_alert_enabled,
		offline_alert_enabled=excluded.offline_alert_enabled,
		cooldown_minutes=excluded.cooldown_minutes,
		auto_probe_gfw_enabled=excluded.auto_probe_gfw_enabled,
		probe_gfw_interval_minutes=excluded.probe_gfw_interval_minutes,
		auto_probe_speedtest_enabled=excluded.auto_probe_speedtest_enabled,
		probe_speedtest_interval_hours=excluded.probe_speedtest_interval_hours,
		probe_skip_when_busy=excluded.probe_skip_when_busy,
		probe_notify_always=excluded.probe_notify_always,
		scan_alert_enabled=excluded.scan_alert_enabled,
		updated_at=excluded.updated_at`,
		server, strings.TrimSpace(in.BarkDeviceKey), sound, group, url,
		boolToInt(in.TrafficAlertEnabled), in.TrafficThresholdMbps, in.TrafficDurationSec,
		boolToInt(in.ConnAlertEnabled), in.ConnThresholdCount,
		boolToInt(in.SingleIPAlertEnabled), in.SingleIPThresholdCount,
		boolToInt(in.GFWAlertEnabled), boolToInt(in.OfflineAlertEnabled), cooldown,
		boolToInt(in.AutoProbeGFWEnabled), gfwInterval,
		boolToInt(in.AutoProbeSpeedtestEnabled), speedInterval,
		boolToInt(in.ProbeSkipWhenBusy), boolToInt(in.ProbeNotifyAlways), boolToInt(in.ScanAlertEnabled), now,
	)
	if err != nil {
		return AlertSettings{}, fmt.Errorf("update alert settings: %w", err)
	}
	return s.GetAlertSettings(ctx)
}

func cleanLegacyRoute(r string) string {
	return strings.TrimSpace(r)
}

func (s *Store) SaveNodeSpeedtest(ctx context.Context, st NodeSpeedtest) error {
	if st.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		st.ID = id
	}
	st.TelecomRoute = cleanLegacyRoute(st.TelecomRoute)
	st.UnicomRoute = cleanLegacyRoute(st.UnicomRoute)
	st.MobileRoute = cleanLegacyRoute(st.MobileRoute)
	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_speedtests (
		id, node_id, telecom_latency_ms, telecom_speed_mbps, telecom_route,
		unicom_latency_ms, unicom_speed_mbps, unicom_route,
		mobile_latency_ms, mobile_speed_mbps, mobile_route,
		gfw_blocked, gfw_status, domestic_latency_ms, domestic_source,
		inbound_telecom_route, inbound_telecom_latency_ms,
		inbound_unicom_route, inbound_unicom_latency_ms,
		inbound_mobile_route, inbound_mobile_latency_ms,
		telecom_node_latencies, telecom_trace_path,
		unicom_node_latencies, unicom_trace_path,
		mobile_node_latencies, mobile_trace_path,
		inbound_telecom_node_latencies, inbound_telecom_trace_path,
		inbound_unicom_node_latencies, inbound_unicom_trace_path,
		inbound_mobile_node_latencies, inbound_mobile_trace_path,
		tested_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		st.ID, st.NodeID, st.TelecomLatencyMs, st.TelecomSpeedMbps, st.TelecomRoute,
		st.UnicomLatencyMs, st.UnicomSpeedMbps, st.UnicomRoute,
		st.MobileLatencyMs, st.MobileSpeedMbps, st.MobileRoute,
		boolToInt(st.GFWBlocked), st.GFWStatus, st.DomesticLatency, st.DomesticSource,
		st.InboundTelecomRoute, st.InboundTelecomLatencyMs,
		st.InboundUnicomRoute, st.InboundUnicomLatencyMs,
		st.InboundMobileRoute, st.InboundMobileLatencyMs,
		st.TelecomNodeLatencies, st.TelecomTracePath,
		st.UnicomNodeLatencies, st.UnicomTracePath,
		st.MobileNodeLatencies, st.MobileTracePath,
		st.InboundTelecomNodeLatencies, st.InboundTelecomTracePath,
		st.InboundUnicomNodeLatencies, st.InboundUnicomTracePath,
		st.InboundMobileNodeLatencies, st.InboundMobileTracePath,
		now)
	if err != nil {
		return fmt.Errorf("save node speedtest: %w", err)
	}
	return nil
}

func (s *Store) GetLatestNodeSpeedtest(ctx context.Context, nodeID string) (*NodeSpeedtest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, node_id, telecom_latency_ms, telecom_speed_mbps, telecom_route,
		unicom_latency_ms, unicom_speed_mbps, unicom_route, mobile_latency_ms, mobile_speed_mbps, mobile_route,
		COALESCE(gfw_blocked, 0), COALESCE(gfw_status, ''), COALESCE(domestic_latency_ms, 0), COALESCE(domestic_source, ''),
		COALESCE(inbound_telecom_route, ''), COALESCE(inbound_telecom_latency_ms, 0),
		COALESCE(inbound_unicom_route, ''), COALESCE(inbound_unicom_latency_ms, 0),
		COALESCE(inbound_mobile_route, ''), COALESCE(inbound_mobile_latency_ms, 0),
		COALESCE(telecom_node_latencies, ''), COALESCE(telecom_trace_path, ''),
		COALESCE(unicom_node_latencies, ''), COALESCE(unicom_trace_path, ''),
		COALESCE(mobile_node_latencies, ''), COALESCE(mobile_trace_path, ''),
		COALESCE(inbound_telecom_node_latencies, ''), COALESCE(inbound_telecom_trace_path, ''),
		COALESCE(inbound_unicom_node_latencies, ''), COALESCE(inbound_unicom_trace_path, ''),
		COALESCE(inbound_mobile_node_latencies, ''), COALESCE(inbound_mobile_trace_path, ''),
		tested_at
		FROM node_speedtests WHERE node_id = ? ORDER BY tested_at DESC LIMIT 1`, nodeID)
	var st NodeSpeedtest
	var testedAt int64
	var gfwBlockedInt int
	err := row.Scan(&st.ID, &st.NodeID, &st.TelecomLatencyMs, &st.TelecomSpeedMbps, &st.TelecomRoute,
		&st.UnicomLatencyMs, &st.UnicomSpeedMbps, &st.UnicomRoute,
		&st.MobileLatencyMs, &st.MobileSpeedMbps, &st.MobileRoute,
		&gfwBlockedInt, &st.GFWStatus, &st.DomesticLatency, &st.DomesticSource,
		&st.InboundTelecomRoute, &st.InboundTelecomLatencyMs,
		&st.InboundUnicomRoute, &st.InboundUnicomLatencyMs,
		&st.InboundMobileRoute, &st.InboundMobileLatencyMs,
		&st.TelecomNodeLatencies, &st.TelecomTracePath,
		&st.UnicomNodeLatencies, &st.UnicomTracePath,
		&st.MobileNodeLatencies, &st.MobileTracePath,
		&st.InboundTelecomNodeLatencies, &st.InboundTelecomTracePath,
		&st.InboundUnicomNodeLatencies, &st.InboundUnicomTracePath,
		&st.InboundMobileNodeLatencies, &st.InboundMobileTracePath,
		&testedAt)
	st.GFWBlocked = gfwBlockedInt != 0
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load latest speedtest: %w", err)
	}
	st.TestedAt = time.Unix(testedAt, 0).UTC().Format(time.RFC3339)
	return &st, nil
}

func (s *Store) ListLatestNodeSpeedtests(ctx context.Context) (map[string]NodeSpeedtest, error) {
	// Query the most recent test for each node using window function or group by
	rows, err := s.db.QueryContext(ctx, `SELECT s1.id, s1.node_id, s1.telecom_latency_ms, s1.telecom_speed_mbps, s1.telecom_route,
		s1.unicom_latency_ms, s1.unicom_speed_mbps, s1.unicom_route, s1.mobile_latency_ms, s1.mobile_speed_mbps, s1.mobile_route,
		COALESCE(s1.gfw_blocked, 0), COALESCE(s1.gfw_status, ''), COALESCE(s1.domestic_latency_ms, 0), COALESCE(s1.domestic_source, ''),
		COALESCE(s1.inbound_telecom_route, ''), COALESCE(s1.inbound_telecom_latency_ms, 0),
		COALESCE(s1.inbound_unicom_route, ''), COALESCE(s1.inbound_unicom_latency_ms, 0),
		COALESCE(s1.inbound_mobile_route, ''), COALESCE(s1.inbound_mobile_latency_ms, 0),
		COALESCE(s1.telecom_node_latencies, ''), COALESCE(s1.telecom_trace_path, ''),
		COALESCE(s1.unicom_node_latencies, ''), COALESCE(s1.unicom_trace_path, ''),
		COALESCE(s1.mobile_node_latencies, ''), COALESCE(s1.mobile_trace_path, ''),
		COALESCE(s1.inbound_telecom_node_latencies, ''), COALESCE(s1.inbound_telecom_trace_path, ''),
		COALESCE(s1.inbound_unicom_node_latencies, ''), COALESCE(s1.inbound_unicom_trace_path, ''),
		COALESCE(s1.inbound_mobile_node_latencies, ''), COALESCE(s1.inbound_mobile_trace_path, ''),
		s1.tested_at
		FROM node_speedtests s1
		JOIN (
			SELECT node_id, MAX(tested_at) as max_tested FROM node_speedtests GROUP BY node_id
		) s2 ON s1.node_id = s2.node_id AND s1.tested_at = s2.max_tested`)
	if err != nil {
		return nil, fmt.Errorf("list latest speedtests: %w", err)
	}
	defer rows.Close()

	out := make(map[string]NodeSpeedtest)
	for rows.Next() {
		var st NodeSpeedtest
		var testedAt int64
		var gfwBlockedInt int
		if err := rows.Scan(&st.ID, &st.NodeID, &st.TelecomLatencyMs, &st.TelecomSpeedMbps, &st.TelecomRoute,
			&st.UnicomLatencyMs, &st.UnicomSpeedMbps, &st.UnicomRoute,
			&st.MobileLatencyMs, &st.MobileSpeedMbps, &st.MobileRoute,
			&gfwBlockedInt, &st.GFWStatus, &st.DomesticLatency, &st.DomesticSource,
			&st.InboundTelecomRoute, &st.InboundTelecomLatencyMs,
			&st.InboundUnicomRoute, &st.InboundUnicomLatencyMs,
			&st.InboundMobileRoute, &st.InboundMobileLatencyMs,
			&st.TelecomNodeLatencies, &st.TelecomTracePath,
			&st.UnicomNodeLatencies, &st.UnicomTracePath,
			&st.MobileNodeLatencies, &st.MobileTracePath,
			&st.InboundTelecomNodeLatencies, &st.InboundTelecomTracePath,
			&st.InboundUnicomNodeLatencies, &st.InboundUnicomTracePath,
			&st.InboundMobileNodeLatencies, &st.InboundMobileTracePath,
			&testedAt); err != nil {
			return nil, err
		}
		st.GFWBlocked = gfwBlockedInt != 0
		st.TestedAt = time.Unix(testedAt, 0).UTC().Format(time.RFC3339)
		out[st.NodeID] = st
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
