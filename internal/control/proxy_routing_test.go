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

func TestProxyRoutingSourceTrackingTelegram(t *testing.T) {
	ctx := context.Background()
	var (
		mu      sync.Mutex
		gotText []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ChatID    string `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotText = append(gotText, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		TelegramEnabled:  true,
		TelegramServer:   server.URL,
		TelegramBotToken: "123456:ABC-DEF",
		TelegramChatID:   "-1009876543210",
		CooldownMinutes:  1,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	proxied := []storedConnection{{
		Destination:    "10.0.0.1:443",
		User:           "alice",
		SourceIP:       "112.86.163.226",
		SourceLocation: "江苏省 南京市",
		ListenerName:   "节点入口-VLESS",
		OutboundName:   "Taiwan-HY2",
	}}
	engine.CheckConnectionsTelemetry("node-1", "洛杉矶-搬瓦工", 0, 0, proxied)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(gotText)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for telegram notification")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	msg := gotText[0]
	if !strings.Contains(msg, "10.0.0.1:443") {
		t.Errorf("expected destination with port, got: %s", msg)
	}
	if !strings.Contains(msg, "来源追溯") {
		t.Errorf("expected source tracking line, got: %s", msg)
	}
	if !strings.Contains(msg, "alice") || !strings.Contains(msg, "112.86.163.226") || !strings.Contains(msg, "江苏省 南京市") || !strings.Contains(msg, "节点入口-VLESS") {
		t.Errorf("expected user, source IP, location and listener in source tracking, got: %s", msg)
	}
	if !strings.Contains(msg, "直接 IP 请求（无域名 SNI）") {
		t.Errorf("expected direct IP notice, got: %s", msg)
	}
}

func TestConnectionSurgeAlertTelegram(t *testing.T) {
	ctx := context.Background()
	var (
		mu      sync.Mutex
		gotText []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ChatID    string `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotText = append(gotText, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		TelegramEnabled:    true,
		TelegramServer:     server.URL,
		TelegramBotToken:   "123456:ABC-DEF",
		TelegramChatID:     "-1009876543210",
		ConnAlertEnabled:   true,
		ConnThresholdCount: 3,
		CooldownMinutes:    1,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	conns := []storedConnection{
		{ID: "1", Host: "www.youtube.com", Destination: "142.250.72.206:443", User: "alice", SourceIP: "1.1.1.1"},
		{ID: "2", Host: "www.youtube.com", Destination: "142.250.72.206:443", User: "alice", SourceIP: "1.1.1.1"},
		{ID: "3", Host: "api.openai.com", Destination: "104.18.6.192:443", User: "alice", SourceIP: "1.1.1.1"},
		{ID: "4", Host: "github.com", Destination: "140.82.121.3:443", User: "bob", SourceIP: "2.2.2.2"},
	}
	engine.CheckConnectionsTelemetry("node-tw", "台湾-HiNet-动态", 0, 0, conns)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(gotText)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for telegram notification")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	msg := gotText[0]
	if !strings.Contains(msg, "活跃连接数激增") {
		t.Errorf("expected connection surge title, got: %s", msg)
	}
	if strings.Contains(msg, "建议操作") {
		t.Errorf("expected NO action advice in alert, got: %s", msg)
	}
	if !strings.Contains(msg, "主要访问目标") {
		t.Errorf("expected top destinations section, got: %s", msg)
	}
	if !strings.Contains(msg, "www.youtube.com（2 条）") {
		t.Errorf("expected youtube count in msg, got: %s", msg)
	}
	if !strings.Contains(msg, "主要请求来源") {
		t.Errorf("expected top sources section, got: %s", msg)
	}
	if !strings.Contains(msg, "alice") || !strings.Contains(msg, "bob") {
		t.Errorf("expected users alice and bob in msg, got: %s", msg)
	}
}

func TestInternalUserIDFilteredFromAlert(t *testing.T) {
	ctx := context.Background()
	var (
		mu      sync.Mutex
		gotText []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		gotText = append(gotText, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		TelegramEnabled:    true,
		TelegramServer:     server.URL,
		TelegramBotToken:   "123456:ABC-DEF",
		TelegramChatID:     "-1009876543210",
		ConnAlertEnabled:   true,
		ConnThresholdCount: 1,
		CooldownMinutes:    1,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	conns := []storedConnection{
		{
			ID:             "1",
			Host:           "www.youtube.com",
			Destination:    "142.250.72.206:443",
			User:           "user_d4553bb7",
			SourceIP:       "112.86.163.226",
			SourceLocation: "江苏省 南京市",
		},
	}
	engine.CheckConnectionsTelemetry("node-tw", "台湾-HiNet-动态", 0, 0, conns)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(gotText)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for telegram notification")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	msg := gotText[0]
	if strings.Contains(msg, "user_d4553bb7") {
		t.Errorf("expected internal user id user_d4553bb7 to be omitted, got: %s", msg)
	}
	if !strings.Contains(msg, "112.86.163.226") {
		t.Errorf("expected client source IP in alert, got: %s", msg)
	}
}
