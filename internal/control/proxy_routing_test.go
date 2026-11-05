package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyRoutingFlagsDomesticOrLANThroughProxy(t *testing.T) {
	ctx := context.Background()
	var (
		mu       sync.Mutex
		received []BarkMessage
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg BarkMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success"})
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if _, err := store.UpdateAlertSettings(ctx, AlertSettings{
		BarkServer:           server.URL,
		BarkDeviceKey:        "test_device_key",
		TrafficAlertEnabled:  false,
		ConnAlertEnabled:     false,
		SingleIPAlertEnabled: false,
		ScanAlertEnabled:     false,
		OfflineAlertEnabled:  true,
		CooldownMinutes:      1,
	}); err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	direct := []storedConnection{{
		SourceIP:     "203.0.113.5:1234",
		Destination:  "192.168.1.10:80",
		Host:         "router.local",
		OutboundName: "DIRECT",
	}}
	engine.CheckConnectionsTelemetry("node-1", "香港节点", 0, 0, direct)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(received) != 0 {
		t.Errorf("direct LAN traffic should not trigger a routing alert, got %d messages", len(received))
	}
	mu.Unlock()

	proxied := []storedConnection{{
		SourceIP:     "203.0.113.5:1234",
		Destination:  "192.168.1.10:80",
		Host:         "router.local",
		OutboundName: "proxy-a",
	}}
	engine.CheckConnectionsTelemetry("node-1", "香港节点", 0, 0, proxied)
	waitForMessages(t, &mu, &received, 1)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(received[0].Title, "分流异常") {
		t.Errorf("unexpected title: %s", received[0].Title)
	}
	if !strings.Contains(received[0].Body, "192.168.1.10") || !strings.Contains(received[0].Body, "proxy-a") {
		t.Errorf("expected target and outbound in body, got: %s", received[0].Body)
	}
}
