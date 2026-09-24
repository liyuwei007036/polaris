package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBarkClientAndAlertEngine(t *testing.T) {
	ctx := context.Background()

	// 1. Mock Bark Server
	var receivedMessages []BarkMessage
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg BarkMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		receivedMessages = append(receivedMessages, msg)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success"})
	}))
	defer server.Close()

	// 2. Setup Store and AlertSettings
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		BarkServer:                server.URL,
		BarkDeviceKey:             "test_device_key",
		BarkSound:                 "minuet",
		BarkGroup:                 "Polaris",
		TrafficAlertEnabled:       true,
		TrafficThresholdMbps:      10.0, // 10 Mbps
		TrafficDurationSec:        0,    // immediate for testing
		ConnAlertEnabled:          true,
		ConnThresholdCount:        5,    // 5 connections
		SingleIPAlertEnabled:      true,
		SingleIPThresholdCount:    3,    // 3 from single IP
		GFWAlertEnabled:           true,
		OfflineAlertEnabled:       true,
		CooldownMinutes:           1,
		ProbeNotifyAlways:         true,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)

	// 3. Test Connection Telemetry Checks:
	// A. Traffic Spike Check
	connections := []storedConnection{
		{ID: "c1", SourceIP: "1.2.3.4:1234", Host: "example.com", Upload: 1000000, Download: 1000000},
		{ID: "c2", SourceIP: "1.2.3.4:1235", Host: "example.com", Upload: 1000000, Download: 1000000},
		{ID: "c3", SourceIP: "1.2.3.4:1236", Host: "example.com", Upload: 1000000, Download: 1000000},
		{ID: "c4", SourceIP: "5.6.7.8:1237", Host: "test.com", Upload: 1000, Download: 1000},
		{ID: "c5", SourceIP: "9.9.9.9:1238", Host: "other.com", Upload: 1000, Download: 1000},
	}
	// Download rate: 20 MB/s (160 Mbps, well above 10 Mbps threshold)
	engine.CheckConnectionsTelemetry("node-1", "Tokyo-01", 20*1024*1024, 0, connections)

	// Allow goroutine to send Bark push
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(receivedMessages)
	mu.Unlock()

	if count < 3 {
		t.Fatalf("expected at least 3 alerts (traffic, conn count, single IP), got %d", count)
	}

	// 4. Test Probe Summary (GFW check result)
	engine.NotifyProbeSummary([]ProbeResultItem{
		{NodeID: "node-1", NodeName: "Tokyo-01", OverseasOK: true, DomesticOK: false}, // Blocked!
		{NodeID: "node-2", NodeName: "HK-01", OverseasOK: true, DomesticOK: true, LatencyMs: 35},
	})

	// 5. Test Subscription Pull Success & Failure
	engine.NotifySubscriptionPullSuccess("常用主力节点", "120.24.56.78", "Clash-Verge/v1.7.7")
	engine.NotifySubscriptionPullFailed("abcdef123456", "凭据无效", "222.186.42.11", "curl/7.88.1")

	// 6. Test Login Failure
	engine.NotifyLoginFailed("admin", "114.114.114.114", "密码错误", "Mozilla/5.0")

	// 7. Test Abnormal Port/Host Scanning Detection
	scanConns := make([]storedConnection, 20)
	for i := 0; i < 20; i++ {
		scanConns[i] = storedConnection{
			ID:          "s",
			SourceIP:    "198.51.100.22:5000",
			Host:        "host-" + string(rune('a'+i)) + ".com",
			Destination: "1.1.1.1:80",
		}
	}
	engine.CheckConnectionsTelemetry("node-scan", "US-West", 100, 100, scanConns)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	var gfwAlertFound, subOkFound, subFailFound, loginFailFound, scanFound bool
	for _, m := range receivedMessages {
		if m.Level == "passive" {
			t.Errorf("found alert with passive level which causes iOS to silence the sound: %s", m.Title)
		}
		if m.Title == "🚨 [Polaris] 发现节点被阻断" {
			gfwAlertFound = true
			if m.Sound != "alarm.caf" {
				t.Errorf("expected GFW alert sound to be 'alarm.caf', got '%s'", m.Sound)
			}
		}
		if m.Title == "📥 [Polaris] 订阅下载成功" {
			subOkFound = true
			if m.Sound != "glass.caf" {
				t.Errorf("expected sub success sound to be 'glass.caf', got '%s'", m.Sound)
			}
		}
		if m.Title == "❌ [Polaris] 订阅下载失败" {
			subFailFound = true
			if m.Sound != "horn.caf" {
				t.Errorf("expected sub fail sound to be 'horn.caf', got '%s'", m.Sound)
			}
		}
		if m.Title == "⚠️ [Polaris] 控制台登录失败" {
			loginFailFound = true
			if m.Sound != "horn.caf" {
				t.Errorf("expected login fail sound to be 'horn.caf', got '%s'", m.Sound)
			}
		}
		if m.Title == "🚨 [Polaris] 检测到异常网络扫描" {
			scanFound = true
			if m.Sound != "alarm.caf" {
				t.Errorf("expected abnormal scan sound to be 'alarm.caf', got '%s'", m.Sound)
			}
		}
	}
	mu.Unlock()

	if !gfwAlertFound {
		t.Errorf("expected critical GFW blocked alert to be sent")
	}
	if !subOkFound {
		t.Errorf("expected subscription pull success alert")
	}
	if !subFailFound {
		t.Errorf("expected subscription pull failed alert")
	}
	if !loginFailFound {
		t.Errorf("expected login failed alert")
	}
	if !scanFound {
		t.Errorf("expected abnormal scan alert")
	}
}
