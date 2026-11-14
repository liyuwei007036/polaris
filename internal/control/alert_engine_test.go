package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNodeRecoveryRequiresPriorOffline(t *testing.T) {
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
		BarkServer:          server.URL,
		BarkDeviceKey:       "test_device_key",
		OfflineAlertEnabled: true,
		CooldownMinutes:     1,
	}); err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.NotifyNodeOnline("node-1", "Tokyo-01")
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(received) != 0 {
		t.Errorf("expected no recovery without a prior offline alert, got %d", len(received))
	}
	mu.Unlock()

	engine.NotifyNodeOffline("node-1", "Tokyo-01")
	waitForMessages(t, &mu, &received, 1)
	engine.NotifyNodeOnline("node-1", "Tokyo-01")
	waitForMessages(t, &mu, &received, 2)

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(received[0].Title, "服务器离线") {
		t.Errorf("expected offline notification first, got %s", received[0].Title)
	}
	if !strings.Contains(received[1].Title, "服务器恢复") {
		t.Errorf("expected recovery notification second, got %s", received[1].Title)
	}
}

func waitForMessages(t *testing.T, mu *sync.Mutex, messages *[]BarkMessage, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(*messages)
		mu.Unlock()
		if count >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d messages, got %d", want, count)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFormatAlertTimeCleanValue(t *testing.T) {
	got := FormatAlertTime(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(got, "2026-11-05") {
		t.Errorf("expected formatted date, got %s", got)
	}
	if strings.Contains(got, "UTC") || strings.Contains(got, "(") {
		t.Errorf("expected clean datetime without UTC or timezone suffix, got %s", got)
	}
}

func TestFail2BanBlocksAreBatched(t *testing.T) {
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

	_, err = store.UpdateAlertSettings(ctx, AlertSettings{
		BarkServer:      server.URL,
		BarkDeviceKey:   "test_device_key",
		CooldownMinutes: 1,
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.fail2banWait = 50 * time.Millisecond
	engine.NotifyFail2BanBlock("node-1", "Tokyo-01", "1.1.1.1", "sshd")
	engine.NotifyFail2BanBlock("node-1", "Tokyo-01", "2.2.2.2", "sshd")
	engine.NotifyFail2BanBlock("node-1", "Tokyo-01", "3.3.3.3", "sshd")

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(received)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for batched fail2ban notification")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected one merged notification, got %d", len(received))
	}
	if !strings.Contains(received[0].Title, "已拦截 3 个") {
		t.Errorf("expected merged title with count, got %s", received[0].Title)
	}
	for _, ip := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		if !strings.Contains(received[0].Body, ip) {
			t.Errorf("expected merged body to contain %s, got %s", ip, received[0].Body)
		}
	}
}

func TestSubscriptionConfigChangeNotification(t *testing.T) {
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
		BarkServer:      server.URL,
		BarkDeviceKey:   "test_device_key",
		CooldownMinutes: 1,
	}); err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.NotifySubscriptionConfigChanged("代理分组", "修改", "Group-A", "成员发生变化", nil)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if len(received) != 0 {
		t.Errorf("expected no notification without affected clients, got %d", len(received))
	}
	mu.Unlock()

	engine.NotifySubscriptionConfigChanged("代理分组", "修改", "Group-A", "成员发生变化", []string{"Client-B", "Client-A"})
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		count := len(received)
		mu.Unlock()
		if count > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for subscription change notification")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected one notification, got %d", len(received))
	}
	if !strings.Contains(received[0].Title, "订阅配置已更新") {
		t.Errorf("unexpected title: %s", received[0].Title)
	}
	if !strings.Contains(received[0].Body, "Client-A") || !strings.Contains(received[0].Body, "Client-B") {
		t.Errorf("expected affected clients in body, got: %s", received[0].Body)
	}
	if !strings.Contains(received[0].Body, "2 个") {
		t.Errorf("expected affected client count in body, got: %s", received[0].Body)
	}
}

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
		BarkServer:             server.URL,
		BarkDeviceKey:          "test_device_key",
		BarkSound:              "minuet",
		BarkGroup:              "Polaris",
		TrafficAlertEnabled:    true,
		TrafficThresholdMbps:   10.0, // 10 Mbps
		TrafficDurationSec:     0,    // immediate for testing
		ConnAlertEnabled:       true,
		ConnThresholdCount:     5, // 5 connections
		SingleIPAlertEnabled:   true,
		SingleIPThresholdCount: 3, // 3 from single IP
		OfflineAlertEnabled:    true,
		CooldownMinutes:        1,
		ScanAlertEnabled:       true,
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

	// 4. Test Subscription Pull Success & Failure
	engine.NotifySubscriptionPullSuccess("常用主力节点", "120.24.56.78", "Clash-Verge/v1.7.7")
	engine.NotifySubscriptionPullFailed("abcdef123456", "凭据无效", "222.186.42.11", "curl/7.88.1")

	// 6. Test Login Failure
	engine.NotifyLoginFailed("admin", "114.114.114.114", "密码错误", "Mozilla/5.0")

	// 7. Verify Normal Web Browsing (Domain-based) DOES NOT trigger scan alert (anti-false-positive check)
	normalWebConns := make([]storedConnection, 25)
	for i := 0; i < 25; i++ {
		normalWebConns[i] = storedConnection{
			ID:          "w",
			SourceIP:    "203.0.113.50:5000",
			Host:        "web-asset-" + string(rune('a'+i)) + ".bilibili.com",
			Destination: "1.1.1.1:443",
		}
	}
	engine.CheckConnectionsTelemetry("node-web", "Tokyo-02", 100, 100, normalWebConns)

	// 8. Test True Abnormal Port Scanning Detection (Vertical Port Scan across multiple ports on same IP)
	testPorts := []string{"21", "22", "23", "80", "443", "445", "1433", "3306", "3389", "6379", "8080", "27017"}
	scanConns := make([]storedConnection, len(testPorts))
	for i, p := range testPorts {
		scanConns[i] = storedConnection{
			ID:          "s",
			SourceIP:    "198.51.100.22:5000",
			Destination: "192.0.2.1:" + p,
		}
	}
	engine.CheckConnectionsTelemetry("node-scan", "US-West", 100, 100, scanConns)

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	var subOkFound, subFailFound, loginFailFound, scanFound bool
	for _, m := range receivedMessages {
		if m.Level != "active" {
			t.Errorf("expected alert level to be 'active' (strictly obeys iOS silent switch), got '%s' for: %s", m.Level, m.Title)
		}
		if strings.Contains(m.Title, "订阅下载成功") {
			subOkFound = true
			if m.Sound != "glass.caf" {
				t.Errorf("expected sub success sound to be 'glass.caf', got '%s'", m.Sound)
			}
		}
		if strings.Contains(m.Title, "订阅下载失败") {
			subFailFound = true
			if m.Sound != "horn.caf" {
				t.Errorf("expected sub fail sound to be 'horn.caf', got '%s'", m.Sound)
			}
		}
		if strings.Contains(m.Title, "登录失败") {
			loginFailFound = true
			if m.Sound != "horn.caf" {
				t.Errorf("expected login fail sound to be 'horn.caf', got '%s'", m.Sound)
			}
		}
		if strings.Contains(m.Title, "异常扫描") {
			if strings.Contains(m.Body, "203.0.113.50") {
				t.Errorf("false positive: normal web browsing IP 203.0.113.50 was flagged as scan!")
			}
			if strings.Contains(m.Body, "198.51.100.22") {
				scanFound = true
			}
			if m.Sound != "alarm.caf" {
				t.Errorf("expected abnormal scan sound to be 'alarm.caf', got '%s'", m.Sound)
			}
		}
	}
	mu.Unlock()

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

func TestBarkAlertsNeverContainURL(t *testing.T) {
	ctx := context.Background()
	var (
		mu       sync.Mutex
		rawBarks []string
		gotTG    []string
	)
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		rawBarks = append(rawBarks, string(bodyBytes))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success"})
	}))
	defer barkServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		gotTG = append(gotTG, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer tgServer.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	if _, err := store.UpdateAlertSettings(ctx, AlertSettings{
		BarkServer:       barkServer.URL,
		BarkDeviceKey:    "test_key",
		TelegramEnabled:  true,
		TelegramServer:   tgServer.URL,
		TelegramBotToken: "12345:token",
		TelegramChatID:   "1001",
		CooldownMinutes:  1,
	}); err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	// Simulate an existing DB row that previously saved a legacy bark_url.
	if _, err := store.db.ExecContext(ctx, `UPDATE alert_settings SET bark_url = ? WHERE id = 1`, "https://legacy.example.com/admin"); err != nil {
		t.Fatalf("seed legacy bark_url: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.NotifyServerExpiry(Node{
		ID:             "node-exp",
		Name:           "ExpiryNode",
		LastObservedIP: "198.51.100.88",
		ExpiresOn:      "2026-12-01",
		Provider:       "DMIT",
		ProviderURL:    "https://www.dmit.io/clientarea.php",
	}, 3)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		bCount, tCount := len(rawBarks), len(gotTG)
		mu.Unlock()
		if bCount >= 1 && tCount >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for notifications (bark=%d tg=%d)", bCount, tCount)
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(rawBarks[0], `"url"`) || strings.Contains(rawBarks[0], "https://") || strings.Contains(rawBarks[0], "续费入口") {
		t.Errorf("expected Bark payload to contain zero URL fields or links, got: %s", rawBarks[0])
	}
	if !strings.Contains(gotTG[0], "https://www.dmit.io/clientarea.php") {
		t.Errorf("expected Telegram notification to still include provider URL link, got: %s", gotTG[0])
	}
}

func TestConfirmChainTargetTimeoutSurvivesFiveMinutes(t *testing.T) {
	engine := NewAlertEngine(nil, nil)
	t0 := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

	if engine.confirmChainTargetTimeout("node-1:c1", t0) {
		t.Fatal("expected initial sighting to wait for chainRelayConfirmWait")
	}
	if !engine.confirmChainTargetTimeout("node-1:c1", t0.Add(6*time.Second)) {
		t.Fatal("expected sighting after chainRelayConfirmWait to be confirmed")
	}
	// Long-lived active connections refreshed every 2 minutes must stay confirmed past 5 minutes.
	for _, offset := range []time.Duration{2 * time.Minute, 4 * time.Minute, 6 * time.Minute, 8 * time.Minute} {
		if !engine.confirmChainTargetTimeout("node-1:c1", t0.Add(offset)) {
			t.Fatalf("expected active connection at +%v to remain confirmed without re-freezing", offset)
		}
	}
	// Once absent for >5 minutes after last sighting at +8m, it is evicted cleanly.
	if engine.confirmChainTargetTimeout("node-1:c1", t0.Add(14*time.Minute)) {
		t.Fatal("expected entry absent for >5 minutes to be evicted and require fresh confirmation")
	}
}

func TestCleanDNSSummary(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "已执行动态 DNS 刷新与链路自愈：twhi.1x1x1.dpdns.org -> 36.234.152.63（已同步 /etc/hosts，已写入 sing-box 静态解析规则，已重启 sing-box 重置出站连接池）",
			expected: "twhi.1x1x1.dpdns.org → 36.234.152.63",
		},
		{
			input:    "twhi.1x1x1.dpdns.org -> 36.234.152.63",
			expected: "twhi.1x1x1.dpdns.org → 36.234.152.63",
		},
		{
			input:    "twhi.1x1x1.dpdns.org → 36.234.152.63（已同步 /etc/hosts）",
			expected: "twhi.1x1x1.dpdns.org → 36.234.152.63",
		},
		{
			input:    "a.com, b.com -> 1.2.3.4",
			expected: "a.com, b.com → 1.2.3.4",
		},
	}
	for _, tc := range cases {
		got := cleanDNSSummary(tc.input)
		if got != tc.expected {
			t.Errorf("cleanDNSSummary(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestNotifyChainedDNSCompletedFormat(t *testing.T) {
	ctx := context.Background()
	var (
		mu       sync.Mutex
		gotTG    []string
		gotBarks []BarkMessage
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sendMessage") {
			var msg telegramSendMessage
			_ = json.NewDecoder(r.Body).Decode(&msg)
			mu.Lock()
			gotTG = append(gotTG, msg.Text)
			mu.Unlock()
		} else {
			var bark BarkMessage
			_ = json.NewDecoder(r.Body).Decode(&bark)
			mu.Lock()
			gotBarks = append(gotBarks, bark)
			mu.Unlock()
		}
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
		TelegramBotToken: "123:TOKEN",
		TelegramChatID:   "12345",
		BarkServer:       server.URL,
		BarkDeviceKey:    "key",
	})
	if err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	engine := NewAlertEngine(store, nil)
	engine.NotifyChainedDNSCompleted("node-hk", "香港-LowEndCloud-182",
		"已执行动态 DNS 刷新与链路自愈：twhi.1x1x1.dpdns.org -> 36.234.152.63（已同步 /etc/hosts，已写入 sing-box 静态解析规则，已重启 sing-box 重置出站连接池）")

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		tgCount := len(gotTG)
		barkCount := len(gotBarks)
		mu.Unlock()
		if tgCount > 0 && barkCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for alerts")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	tg := gotTG[0]
	bark := gotBarks[0]

	// Check Telegram format
	if !strings.Contains(tg, "<b>DNS 刷新完成</b>") {
		t.Errorf("expected <b>DNS 刷新完成</b> title, got: %s", tg)
	}
	if strings.Contains(tg, "链路自愈 / DNS 刷新与验证完成") {
		t.Errorf("expected no old title, got: %s", tg)
	}
	if strings.Contains(tg, "详细信息") {
		t.Errorf("expected no 详细信息 header, got: %s", tg)
	}
	if strings.Contains(tg, "执行结果") || strings.Contains(tg, "当前状态") || strings.Contains(tg, "客户端影响") {
		t.Errorf("expected no boilerplate fields, got: %s", tg)
	}
	if !strings.Contains(tg, "<code>twhi.1x1x1.dpdns.org</code> → <code>36.234.152.63</code>") {
		t.Errorf("expected clean DNS mapping line, got: %s", tg)
	}
	if !strings.Contains(tg, "香港-LowEndCloud-182") {
		t.Errorf("expected node name, got: %s", tg)
	}
	if strings.Contains(tg, "时间：") {
		t.Errorf("expected no 时间： label, got: %s", tg)
	}
	if strings.Contains(tg, "UTC") {
		t.Errorf("expected no UTC in timestamp, got: %s", tg)
	}

	// Check Bark format
	if !strings.Contains(bark.Title, "DNS 刷新完成：香港-LowEndCloud-182") {
		t.Errorf("expected Bark title to contain 'DNS 刷新完成：香港-LowEndCloud-182', got: %s", bark.Title)
	}
	if !strings.Contains(bark.Body, "twhi.1x1x1.dpdns.org → 36.234.152.63") {
		t.Errorf("expected Bark body to contain clean DNS mapping, got: %s", bark.Body)
	}
	if strings.Contains(bark.Body, "已执行动态 DNS 刷新与链路自愈") || strings.Contains(bark.Body, "已同步 /etc/hosts") {
		t.Errorf("expected clean Bark body without technical noise, got: %s", bark.Body)
	}
}
