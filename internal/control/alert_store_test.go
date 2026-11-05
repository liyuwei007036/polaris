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
	if settings.ScanAlertEnabled {
		t.Errorf("expected default scan alert enabled false, got %v", settings.ScanAlertEnabled)
	}

	// Update settings
	settings.BarkDeviceKey = "test_key_12345"
	settings.TrafficThresholdMbps = 80.0
	settings.ConnThresholdCount = 350
	settings.SingleIPThresholdCount = 80
	settings.ScanAlertEnabled = true

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
	if !updated.ScanAlertEnabled {
		t.Errorf("expected scan alert enabled true, got %v", updated.ScanAlertEnabled)
	}
}
