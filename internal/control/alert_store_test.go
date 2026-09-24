package control

import (
	"context"
	"testing"
)

func TestAlertSettingsLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Initial fetch should return defaults
	settings, err := store.GetAlertSettings(ctx)
	if err != nil {
		t.Fatalf("get alert settings: %v", err)
	}
	if settings.BarkServer != "https://api.day.app" {
		t.Errorf("expected default bark server https://api.day.app, got %s", settings.BarkServer)
	}
	if !settings.ProbeNotifyAlways {
		t.Errorf("expected default probe notify always true, got %v", settings.ProbeNotifyAlways)
	}

	// Update settings
	settings.BarkDeviceKey = "test_key_12345"
	settings.TrafficThresholdMbps = 80.0
	settings.ConnThresholdCount = 350
	settings.SingleIPThresholdCount = 80
	settings.ProbeGFWIntervalMinutes = 15
	settings.ProbeNotifyAlways = true

	updated, err := store.UpdateAlertSettings(ctx, settings)
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}
	if updated.BarkDeviceKey != "test_key_12345" {
		t.Errorf("expected test_key_12345, got %s", updated.BarkDeviceKey)
	}
	if updated.TrafficThresholdMbps != 80.0 {
		t.Errorf("expected 80.0, got %f", updated.TrafficThresholdMbps)
	}
	if updated.ProbeGFWIntervalMinutes != 15 {
		t.Errorf("expected 15, got %d", updated.ProbeGFWIntervalMinutes)
	}
	if !updated.ProbeNotifyAlways {
		t.Errorf("expected probe notify always true, got %v", updated.ProbeNotifyAlways)
	}

	// Speedtest lifecycle
	nodeID := "node-test-1"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "test-node", []byte("12345678901234567890123456789012"), nowUnix()); err != nil {
		t.Fatalf("create test node: %v", err)
	}
	st := NodeSpeedtest{
		NodeID:           nodeID,
		TelecomLatencyMs: 45,
		TelecomSpeedMbps: 120.5,
		UnicomLatencyMs:  52,
		UnicomSpeedMbps:  180.0,
		MobileLatencyMs:  38,
		MobileSpeedMbps:  220.0,
	}
	if err := store.SaveNodeSpeedtest(ctx, st); err != nil {
		t.Fatalf("save node speedtest: %v", err)
	}

	latest, err := store.GetLatestNodeSpeedtest(ctx, nodeID)
	if err != nil {
		t.Fatalf("get latest speedtest: %v", err)
	}
	if latest == nil {
		t.Fatalf("expected speedtest record, got nil")
	}
	if latest.MobileLatencyMs != 38 {
		t.Errorf("expected mobile latency 38, got %d", latest.MobileLatencyMs)
	}

	list, err := store.ListLatestNodeSpeedtests(ctx)
	if err != nil {
		t.Fatalf("list latest speedtests: %v", err)
	}
	if len(list) != 1 || list[nodeID].TelecomLatencyMs != 45 {
		t.Errorf("expected 1 record with telecom latency 45, got %+v", list)
	}
}
