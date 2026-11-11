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

	"github.com/liyuwei007036/polaris/internal/wire"
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

func TestProxyRoutingChainedProxyResolvesTrueFinalExit(t *testing.T) {
	ctx := context.Background()
	var (
		mu       sync.Mutex
		barks    []BarkMessage
		gotTexts []string
	)
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg BarkMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		barks = append(barks, msg)
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
		gotTexts = append(gotTexts, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer tgServer.Close()

	store := createTestStore(t)
	nodeAID := createTestNode(t, store, "香港入口", "198.51.100.11")
	nodeBID := createTestNode(t, store, "台湾中转", "203.0.113.22")
	nodeCID := createTestNode(t, store, "日本落地", "203.0.113.33")

	warpOB, err := store.CreateOutbound(ctx, Outbound{
		Name: "WARP-JP", Type: "socks", Server: "127.0.0.1", ServerPort: 1080, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create warp outbound: %v", err)
	}
	directManagedOB, err := store.CreateOutbound(ctx, Outbound{
		Name: "MyCustomDirect", Type: "direct", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create direct outbound: %v", err)
	}

	spec := ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/vless"}}
	lC, err := store.CreateListener(ctx, Listener{
		NodeID: nodeCID, Name: "jp-in", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true, OutboundID: warpOB.ID, Spec: spec,
	})
	if err != nil {
		t.Fatalf("create lC: %v", err)
	}
	epC, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lC.ID, Name: "relayC", Enabled: true, OutboundID: warpOB.ID,
	}, EndpointCredentials{UUID: "33333333-3333-3333-3333-333333333333"})
	if err != nil {
		t.Fatalf("create epC: %v", err)
	}

	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "tw-in", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatalf("create lB: %v", err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relayB", Enabled: true, OutboundID: "chain:" + epC.ID,
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create epB: %v", err)
	}

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

	hub := newConnectionsHub()
	engine := NewAlertEngine(store, nil)
	engine.SetConnectionsHub(hub)

	nowStr := time.Now().UTC().Format(time.RFC3339)
	connsA := []storedConnection{{
		ID:             "conn-a-1",
		Network:        "tcp",
		Destination:    "10.0.0.8:443",
		User:           "alice",
		SourceIP:       "112.86.163.226",
		SourceLocation: "江苏省 南京市",
		ListenerName:   "入口VLESS",
		Outbound:       "chain-" + epB.ID,
		OutboundName:   "台湾中转 · tw-in (relayB)",
		ChainedTarget:  epB.ID,
	}}
	connsB := []storedConnection{{
		ID:                  "conn-b-1",
		Network:             "tcp",
		Destination:         "10.0.0.8:443",
		SourceIP:            "198.51.100.11",
		ListenerName:        "tw-in",
		Outbound:            "chain-" + epC.ID,
		OutboundName:        "日本落地 · jp-in (relayC)",
		ChainedTarget:       epC.ID,
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}}
	connsC := []storedConnection{{
		ID:                  "conn-c-1",
		Network:             "tcp",
		Destination:         "10.0.0.8:443",
		SourceIP:            "203.0.113.22",
		ListenerName:        "jp-in",
		Outbound:            "outbound-" + warpOB.ID,
		OutboundName:        "WARP-JP",
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeBID,
	}}

	hub.update(nodeConnectionsSnapshot{NodeID: nodeAID, CollectedAt: nowStr, RawConns: connsA, ConnectionCount: 1})
	hub.update(nodeConnectionsSnapshot{NodeID: nodeBID, CollectedAt: nowStr, RawConns: connsB, ConnectionCount: 1})
	hub.update(nodeConnectionsSnapshot{NodeID: nodeCID, CollectedAt: nowStr, RawConns: connsC, ConnectionCount: 1})

	// Evaluate all three nodes; relay hops must NOT fire duplicate alerts.
	engine.CheckConnectionsTelemetry(nodeCID, "日本落地", 0, 0, connsC)
	engine.CheckConnectionsTelemetry(nodeBID, "台湾中转", 0, 0, connsB)
	engine.CheckConnectionsTelemetry(nodeAID, "香港入口", 0, 0, connsA)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		bCount, tCount := len(barks), len(gotTexts)
		mu.Unlock()
		if bCount >= 1 && tCount >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for chained alerts (bark=%d tg=%d)", bCount, tCount)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(80 * time.Millisecond)

	mu.Lock()
	if len(barks) != 1 || len(gotTexts) != 1 {
		mu.Unlock()
		t.Fatalf("expected exactly 1 entry alert (no relay duplicates), got bark=%d tg=%d", len(barks), len(gotTexts))
	}
	if !strings.Contains(gotTexts[0], "WARP-JP") {
		t.Errorf("expected true final exit WARP-JP in Telegram, got: %s", gotTexts[0])
	}
	if !strings.Contains(gotTexts[0], "香港入口 → 台湾中转 → 日本落地 → WARP-JP") {
		t.Errorf("expected full 3-hop chain path in Telegram, got: %s", gotTexts[0])
	}
	if strings.Contains(gotTexts[0], "台湾中转 · tw-in") || strings.Contains(gotTexts[0], "日本落地 · jp-in") {
		t.Errorf("expected intermediate hop listener strings NOT to appear as final exit, got: %s", gotTexts[0])
	}
	if !strings.Contains(gotTexts[0], "alice") || !strings.Contains(gotTexts[0], "112.86.163.226") {
		t.Errorf("expected original client source preserved, got: %s", gotTexts[0])
	}
	if !strings.Contains(barks[0].Body, "WARP-JP") || !strings.Contains(barks[0].Body, "香港入口 → 台湾中转 → 日本落地 → WARP-JP") {
		t.Errorf("expected Bark to display final exit and chain clearly, got: %s", barks[0].Body)
	}
	mu.Unlock()

	// Single-node direct-IP and managed type=direct outbounds must NOT alert.
	engine.CheckConnectionsTelemetry("node-direct-test", "直连测试", 0, 0, []storedConnection{
		{Destination: "192.168.1.50:80", Outbound: "direct-198.51.100.11", OutboundName: "直连 (198.51.100.11)", OutboundDirect: true},
		{Destination: "192.168.1.51:80", Outbound: "outbound-" + directManagedOB.ID, OutboundName: "MyCustomDirect", OutboundDirect: true},
	})
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	if len(barks) != 1 {
		mu.Unlock()
		t.Errorf("expected direct-<ip> and managed type=direct outbounds not to trigger alert, got %d bark msgs", len(barks))
	} else {
		mu.Unlock()
	}

	// Verify static DB chain resolution when downstream live hub snapshots are absent.
	emptyHub := newConnectionsHub()
	staticEngine := NewAlertEngine(store, nil)
	staticEngine.SetConnectionsHub(emptyHub)
	staticEngine.CheckConnectionsTelemetry(nodeAID, "香港入口", 0, 0, []storedConnection{{
		ID:            "static-1",
		Destination:   "192.168.100.5:80",
		Host:          "nas.internal",
		Outbound:      "chain-" + epB.ID,
		OutboundName:  "台湾中转 · tw-in (relayB)",
		ChainedTarget: epB.ID,
	}})
	deadline2 := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		bc, tc := len(barks), len(gotTexts)
		mu.Unlock()
		if bc >= 2 && tc >= 2 {
			break
		}
		if time.Now().After(deadline2) {
			t.Fatalf("timed out waiting for static DB chain alert (bark=%d tg=%d)", bc, tc)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(gotTexts[1], "WARP-JP") || !strings.Contains(gotTexts[1], "香港入口 → 台湾中转 → 日本落地 → WARP-JP") {
		t.Errorf("expected static DB trace to resolve WARP-JP & 3-hop path, got: %s", gotTexts[1])
	}
}

func TestChainTargetUsersCachePerNodeIsolationAndMultiIPFleet(t *testing.T) {
	ctx := context.Background()
	store := createTestStore(t)

	// NodeA has secondary multi-IP 198.51.100.77 in IPAddresses (not its primary ClientAddress).
	nodeAID := createTestNode(t, store, "NodeA", "198.51.100.1")
	if err := store.UpdateNodeIdentityWithIPs(ctx, nodeAID, "v1", "linux", "debian", "amd64", "", "",
		[]string{"198.51.100.1", "198.51.100.77"}, map[string]string{"198.51.100.77": "10.10.0.2"}); err != nil {
		t.Fatalf("update nodeA IPs: %v", err)
	}
	nodeBID := createTestNode(t, store, "NodeB", "203.0.113.2")

	spec := ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/vless"}}
	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "tw-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatalf("create listener B: %v", err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relay-user", Alias: "中转账号", Enabled: true,
	}, EndpointCredentials{UUID: "44444444-4444-4444-4444-444444444444"})
	if err != nil {
		t.Fatalf("create endpoint B: %v", err)
	}
	lA, err := store.CreateListener(ctx, Listener{
		NodeID: nodeAID, Name: "hk-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatalf("create listener A: %v", err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lA.ID, Name: "alice", Enabled: true, OutboundID: "chain:" + epB.ID,
	}, EndpointCredentials{UUID: "55555555-5555-5555-5555-555555555555"})
	if err != nil {
		t.Fatalf("create endpoint A: %v", err)
	}

	srv, err := NewServer(store, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	// Populate cache on NodeA FIRST, then convert connections for NodeB immediately after.
	srv.convertConnections(ctx, nodeAID, []wire.ConnectionInfo{{
		ID: "c1", InboundTag: "listener-" + lA.ID, Source: "112.86.163.226:50000", Destination: "1.1.1.1:443", Outbound: "chain-" + epB.ID,
	}})
	connsB := srv.convertConnections(ctx, nodeBID, []wire.ConnectionInfo{{
		ID: "c2", InboundTag: "listener-" + lB.ID, User: "relay-user", Source: "198.51.100.77:60000", Destination: "1.1.1.1:443", Outbound: "direct-198.51.100.99",
	}})
	if len(connsB) != 1 {
		t.Fatalf("expected 1 connection on nodeB, got %d", len(connsB))
	}
	if !connsB[0].IsChainRelay || connsB[0].RelayUpstreamNodeID != nodeAID {
		t.Errorf("expected IsChainRelay=true and RelayUpstreamNodeID=%s via multi-IP match despite NodeA cache warmup, got %+v", nodeAID, connsB[0])
	}
	if !connsB[0].OutboundDirect {
		t.Errorf("expected OutboundDirect=true for direct-<ip> tag, got false (%+v)", connsB[0])
	}
	if connsB[0].User != "中转账号" {
		t.Errorf("expected alias cache not clobbered by listenerNames, got User=%q", connsB[0].User)
	}
}

func TestTrafficSpikeOnRelayNodeRecoversUpstreamClient(t *testing.T) {
	ctx := context.Background()
	var (
		mu    sync.Mutex
		barks []BarkMessage
		tgs   []string
	)
	barkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg BarkMessage
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		barks = append(barks, msg)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200})
	}))
	defer barkServer.Close()

	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		tgs = append(tgs, msg.Text)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer tgServer.Close()

	store := createTestStore(t)
	nodeAID := createTestNode(t, store, "广州入口", "198.51.100.10")
	nodeBID := createTestNode(t, store, "日本落地", "203.0.113.20")

	if _, err := store.UpdateAlertSettings(ctx, AlertSettings{
		BarkServer:           barkServer.URL,
		BarkDeviceKey:        "test_key",
		TelegramEnabled:      true,
		TelegramServer:       tgServer.URL,
		TelegramBotToken:     "12345:token",
		TelegramChatID:       "1001",
		TrafficAlertEnabled:  true,
		TrafficThresholdMbps: 10,
		TrafficDurationSec:   0,
		CooldownMinutes:      1,
	}); err != nil {
		t.Fatalf("update alert settings: %v", err)
	}

	hub := newConnectionsHub()
	engine := NewAlertEngine(store, nil)
	engine.SetConnectionsHub(hub)

	entryConns := []storedConnection{{
		ID:             "e1",
		Network:        "tcp",
		Host:           "speed.cloudflare.com",
		Destination:    "104.16.132.229:443",
		User:           "alice",
		SourceIP:       "112.86.163.226",
		SourceLocation: "江苏省 南京市",
		ChainedTarget:  "ep-b",
	}}
	hub.update(nodeConnectionsSnapshot{NodeID: nodeAID, CollectedAt: time.Now().UTC().Format(time.RFC3339), RawConns: entryConns, ConnectionCount: 1})

	relayConns := []storedConnection{{
		ID:                  "r1",
		Network:             "tcp",
		Destination:         "104.16.132.229:443",
		SourceIP:            "198.51.100.10",
		Download:            50 * 1024 * 1024,
		IsChainRelay:        true,
		RelayUpstreamNodeID: nodeAID,
	}}
	// Trigger twice with 0s duration exceeded so alert fires on landing node.
	engine.CheckConnectionsTelemetry(nodeBID, "日本落地", 5_000_000, 0, relayConns)

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		bc, tc := len(barks), len(tgs)
		mu.Unlock()
		if bc >= 1 && tc >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for spike alerts (bark=%d tg=%d)", bc, tc)
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(tgs[0], "未知来源") || strings.Contains(tgs[0], "未指定域名/IP") {
		t.Errorf("expected relay traffic spike to recover real client & host, got: %s", tgs[0])
	}
	if !strings.Contains(tgs[0], "alice") || !strings.Contains(tgs[0], "speed.cloudflare.com") || !strings.Contains(tgs[0], "广州入口") {
		t.Errorf("expected client alice, host speed.cloudflare.com, and upstream name 广州入口 in alert, got: %s", tgs[0])
	}
	if !strings.Contains(barks[0].Body, "alice") || !strings.Contains(barks[0].Body, "广州入口") {
		t.Errorf("expected recovered source and upstream name in Bark body, got: %s", barks[0].Body)
	}
}

func TestSharedIPSameHostChainRelayAndLiveExitResolution(t *testing.T) {
	ctx := context.Background()
	store := createTestStore(t)

	// Two nodes sharing the exact same IP (127.0.0.1, e.g. local WSL/loopback or shared NAT)
	// plus a third node connecting via an unlisted SNAT egress IP.
	nodeAID := createTestNode(t, store, "SameHost-Entry", "127.0.0.1")
	nodeBID := createTestNode(t, store, "SameHost-Relay", "127.0.0.1")
	nodeCID := createTestNode(t, store, "Landing-Exit", "203.0.113.55")

	exitOB, err := store.CreateOutbound(ctx, Outbound{
		Name: "Residential-Exit", Type: "socks", Server: "127.0.0.1", ServerPort: 1081, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create exit outbound: %v", err)
	}

	spec := ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/ws"}}
	lC, err := store.CreateListener(ctx, Listener{
		NodeID: nodeCID, Name: "c-in", Port: 9443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	epC, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lC.ID, Name: "relay-c", Enabled: true, OutboundID: exitOB.ID,
	}, EndpointCredentials{UUID: "66666666-6666-6666-6666-666666666666"})
	if err != nil {
		t.Fatal(err)
	}

	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeBID, Name: "b-in", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relay-b", Alias: "共享IP中继", Enabled: true, OutboundID: "chain:" + epC.ID,
	}, EndpointCredentials{UUID: "77777777-7777-7777-7777-777777777777"})
	if err != nil {
		t.Fatal(err)
	}

	lA, err := store.CreateListener(ctx, Listener{
		NodeID: nodeAID, Name: "a-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lA.ID, Name: "client-alice", Enabled: true, OutboundID: "chain:" + epB.ID,
	}, EndpointCredentials{UUID: "88888888-8888-8888-8888-888888888888"})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	nowStr := time.Now().UTC().Format(time.RFC3339)

	// 1. Node A pushes an entry connection targeting 223.5.5.5:443 via chain-epB (placed in chains[1] too).
	rawA := []wire.ConnectionInfo{{
		ID: "entry-1", InboundTag: "listener-" + lA.ID, Network: "tcp",
		User: "client-alice", Source: "112.86.163.226:55555", Destination: "223.5.5.5:443",
		Chains: []string{"proxy-wrap", "chain-" + epB.ID},
	}}
	connsA := srv.convertConnections(ctx, nodeAID, rawA)
	if len(connsA) != 1 || connsA[0].ChainedTarget != epB.ID {
		t.Fatalf("expected ChainedTarget=%s parsed from chains slice, got %+v", epB.ID, connsA)
	}
	if connsA[0].ResolvedOutboundName != "Residential-Exit" {
		t.Errorf("expected static DB resolved final exit Residential-Exit on entry leg, got %q", connsA[0].ResolvedOutboundName)
	}
	if connsA[0].ChainPath != "SameHost-Entry → SameHost-Relay → Landing-Exit" {
		t.Errorf("expected full 3-hop chain_path, got %q", connsA[0].ChainPath)
	}
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeAID, CollectedAt: nowStr, RawConns: connsA, ConnectionCount: 1})

	// 2. Node B (sharing 127.0.0.1 with Node A!) receives relay connection from 127.0.0.1.
	connsB := srv.convertConnections(ctx, nodeBID, []wire.ConnectionInfo{{
		ID: "relay-1", InboundTag: "listener-" + lB.ID, Network: "tcp",
		User: "relay-b", Source: "127.0.0.1:41000", Destination: "223.5.5.5:443",
		Outbound: "chain-" + epC.ID,
	}})
	if !connsB[0].IsChainRelay || connsB[0].RelayUpstreamNodeID != nodeAID {
		t.Fatalf("expected same-IP loopback chain relay detected with upstream %s, got %+v", nodeAID, connsB[0])
	}
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeBID, CollectedAt: nowStr, RawConns: connsB, ConnectionCount: 1})

	// 3. Node C receives relay connection from an unlisted ephemeral IP 198.18.0.99 (fallback via active hub match!).
	connsC := srv.convertConnections(ctx, nodeCID, []wire.ConnectionInfo{{
		ID: "landing-1", InboundTag: "listener-" + lC.ID, Network: "tcp",
		User: "relay-c", Source: "198.18.0.99:42000", Destination: "223.5.5.5:443",
		Outbound: "outbound-" + exitOB.ID,
	}})
	if !connsC[0].IsChainRelay || connsC[0].RelayUpstreamNodeID != nodeBID {
		t.Fatalf("expected unlisted-IP relay detected via active upstream hub match %s, got %+v", nodeBID, connsC[0])
	}
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeCID, CollectedAt: nowStr, RawConns: connsC, ConnectionCount: 1})

	// 4. Verify fleet totals deduplicates all 3 hops down to 1 active connection.
	totals := srv.connHub.totals(time.Now(), nil)
	if totals.ConnectionCount != 1 {
		t.Errorf("expected fleet totals ConnectionCount=1 across 3-hop chain, got %d", totals.ConnectionCount)
	}

	// 5. Verify loopback/same-host downstream push arriving BEFORE upstream push is still
	// immediately marked IsChainRelay=true via chainTargetUpstreams fallback.
	nodeD := createTestNode(t, store, "LoopbackLanding", "203.0.113.88")
	lD, err := store.CreateListener(ctx, Listener{
		NodeID: nodeD, Name: "d-in", Port: 9444, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	epD, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lD.ID, Name: "relay-d", Enabled: true, OutboundID: exitOB.ID,
	}, EndpointCredentials{UUID: "99999999-9999-9999-9999-999999999999"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lC.ID, Name: "forward-to-d", Enabled: true, OutboundID: "chain:" + epD.ID,
	}, EndpointCredentials{UUID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	srv.invalidateConnMetadataCache()
	connsD := srv.convertConnections(ctx, nodeD, []wire.ConnectionInfo{{
		ID: "loop-d1", InboundTag: "listener-" + lD.ID, Network: "tcp",
		User: "relay-d", Source: "127.0.0.1:49999", Destination: "10.0.0.99:443", Outbound: "outbound-" + exitOB.ID,
	}})
	if len(connsD) != 1 || !connsD[0].IsChainRelay || connsD[0].RelayUpstreamNodeID != nodeCID {
		t.Errorf("expected loopback connection on chain target to be tagged IsChainRelay upstream=%s before upstream push, got %+v", nodeCID, connsD)
	}
}

func TestChainEmptyHostDestinationPortDoesNotCrossMatch(t *testing.T) {
	a := storedConnection{
		Network: "tcp", Destination: ":443", Host: "youtube.com",
	}
	b := storedConnection{
		Network: "tcp", Destination: ":443", Host: "github.com",
	}
	if connectionDestMatches(a, b) {
		t.Fatalf("expected :443 connections with distinct hosts (youtube.com vs github.com) NOT to match")
	}

	c := storedConnection{
		Network: "tcp", Destination: "142.250.1.1:443", Host: "youtube.com",
	}
	if !connectionDestMatches(a, c) {
		t.Fatalf("expected :443 domain connection to match resolved destination with same host youtube.com")
	}

	// Same CDN IP on port 443 but explicitly different sniffed SNI/hosts must NOT match.
	d1 := storedConnection{Network: "tcp", Destination: "104.16.1.1:443", Host: "a.example.com"}
	d2 := storedConnection{Network: "tcp", Destination: "104.16.1.1:443", Host: "b.example.com"}
	if connectionDestMatches(d1, d2) {
		t.Fatalf("expected same IP:port with differing Hosts NOT to match")
	}
}

func TestSharedListenerNonChainAccountNotMisclassified(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeA := createTestNode(t, store, "Entry-A", "198.51.100.10")
	nodeB := createTestNode(t, store, "Shared-B", "198.51.100.20")

	spec := ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/ws"}}
	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeB, Name: "shared-in", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	epRelay, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relay-account", Enabled: true, OutboundID: "direct",
	}, EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "normal-client", Enabled: true, OutboundID: "direct",
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatal(err)
	}

	lA, err := store.CreateListener(ctx, Listener{
		NodeID: nodeA, Name: "entry-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lA.ID, Name: "entry-user", Enabled: true, OutboundID: "chain:" + epRelay.ID,
	}, EndpointCredentials{UUID: "33333333-3333-3333-3333-333333333333"})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	// Node A has an active connection to 1.2.3.4:443.
	connsA := srv.convertConnections(ctx, nodeA, []wire.ConnectionInfo{{
		ID: "a1", InboundTag: "listener-" + lA.ID, Network: "tcp", User: "entry-user",
		Source: "112.86.163.226:50000", Destination: "1.2.3.4:443", Outbound: "chain-" + epRelay.ID,
	}})
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeA, CollectedAt: time.Now().UTC().Format(time.RFC3339), RawConns: connsA, ConnectionCount: 1})

	// normal-client on the same listener connects via loopback AND to the same 1.2.3.4:443!
	// Because normal-client is NOT the chain target account, it MUST NOT be flagged as IsChainRelay.
	connsB := srv.convertConnections(ctx, nodeB, []wire.ConnectionInfo{
		{
			ID: "normal-1", InboundTag: "listener-" + lB.ID, Network: "tcp", User: "normal-client",
			Source: "127.0.0.1:41000", Destination: "1.2.3.4:443", Outbound: "direct",
		},
		{
			ID: "relay-1", InboundTag: "listener-" + lB.ID, Network: "tcp", User: "relay-account",
			Source: "127.0.0.1:41001", Destination: "1.2.3.4:443", Outbound: "direct",
		},
	})
	if len(connsB) != 2 {
		t.Fatalf("expected 2 connections on Node B, got %d", len(connsB))
	}
	if connsB[0].IsChainRelay || connsB[0].OnChainTarget {
		t.Errorf("expected normal-client NOT to be marked IsChainRelay or OnChainTarget on shared listener, got %+v", connsB[0])
	}
	if !connsB[1].IsChainRelay || connsB[1].RelayUpstreamNodeID != nodeA {
		t.Errorf("expected relay-account to be marked IsChainRelay with upstream=%s, got %+v", nodeA, connsB[1])
	}
}

func TestShortLivedAndOutOfOrderRelayCorrelationAndRecorder(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeA := createTestNode(t, store, "FastCloseEntry", "198.51.100.11")
	nodeB := createTestNode(t, store, "UnlistedSNATRelay", "198.51.100.22")

	defaultOB, err := store.CreateOutbound(ctx, Outbound{
		Name: "Fallback-Exit", Type: "socks", Server: "198.51.100.50", ServerPort: 1080, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	liveOB, err := store.CreateOutbound(ctx, Outbound{
		Name: "Rule-WARP", Type: "socks", Server: "198.51.100.51", ServerPort: 1080, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	spec := ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "httpupgrade", Path: "/ws"}}
	lB, err := store.CreateListener(ctx, Listener{
		NodeID: nodeB, Name: "b-in", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	epB, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lB.ID, Name: "relay-b", Enabled: true, OutboundID: defaultOB.ID,
	}, EndpointCredentials{UUID: "44444444-4444-4444-4444-444444444444"})
	if err != nil {
		t.Fatal(err)
	}

	lA, err := store.CreateListener(ctx, Listener{
		NodeID: nodeA, Name: "a-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: lA.ID, Name: "client-a", Enabled: true, OutboundID: "chain:" + epB.ID,
	}, EndpointCredentials{UUID: "55555555-5555-5555-5555-555555555555"})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	// Part A: Node B pushes BEFORE Node A over an unlisted SNAT IP -> OnChainTarget=true, NOT persisted to SQLite!
	preConnsB := srv.convertConnections(ctx, nodeB, []wire.ConnectionInfo{{
		ID: "early-b", InboundTag: "listener-" + lB.ID, Network: "tcp", User: "relay-b",
		Source: "198.19.1.77:51000", Host: "api.short.example", Destination: ":443", Outbound: "outbound-" + liveOB.ID,
	}})
	if !preConnsB[0].OnChainTarget || preConnsB[0].IsChainRelay {
		t.Fatalf("expected early push on Node B before A to have OnChainTarget=true, IsChainRelay=false, got %+v", preConnsB[0])
	}
	srv.connRecorder.RecordPush(nodeB, "UnlistedSNATRelay", preConnsB)
	srv.connRecorder.Flush(ctx)

	// Part B: Node A pushes entry leg, records initial DB fallback ("UnlistedSNATRelay → Fallback-Exit"), then closes immediately.
	connsA := srv.convertConnections(ctx, nodeA, []wire.ConnectionInfo{{
		ID: "a-short", InboundTag: "listener-" + lA.ID, Network: "tcp", User: "client-a",
		Source: "112.86.163.226:52000", Host: "api.short.example", Destination: ":443", Outbound: "chain-" + epB.ID,
	}})
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeA, CollectedAt: time.Now().UTC().Format(time.RFC3339), RawConns: connsA, ConnectionCount: 1})
	srv.connRecorder.RecordPush(nodeA, "FastCloseEntry", connsA)
	// Node A's connection closes immediately before Node B pushes again!
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeA, CollectedAt: time.Now().UTC().Format(time.RFC3339), RawConns: nil, ConnectionCount: 0})

	// Now Node B pushes even after Node A closed the active connection: recentChainEntries correlates it!
	postConnsB := srv.convertConnections(ctx, nodeB, []wire.ConnectionInfo{{
		ID: "early-b", InboundTag: "listener-" + lB.ID, Network: "tcp", User: "relay-b",
		Source: "198.19.1.77:51000", Host: "api.short.example", Destination: ":443", Outbound: "outbound-" + liveOB.ID,
	}})
	if !postConnsB[0].IsChainRelay || postConnsB[0].RelayUpstreamNodeID != nodeA {
		t.Fatalf("expected recentChainEntries to correlate Node B relay leg even after Node A closed, got %+v", postConnsB[0])
	}
	srv.connRecorder.RecordPush(nodeB, "UnlistedSNATRelay", postConnsB)

	// Verify live route update also refreshes Node A's recorded OutboundName when Node B IS active!
	srv.connHub.update(nodeConnectionsSnapshot{NodeID: nodeB, CollectedAt: time.Now().UTC().Format(time.RFC3339), RawConns: postConnsB, ConnectionCount: 1})
	connsA[0].Download += 1024
	srv.connRecorder.RecordPush(nodeA, "FastCloseEntry", connsA)
	srv.connRecorder.Flush(ctx)

	recs, total, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{}, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("expected ONLY 1 non-relay ConnectionRecord (relay leg suppressed), got %d (%+v)", total, recs)
	}
	if !strings.Contains(recs[0].OutboundName, "Rule-WARP") {
		t.Errorf("expected recorded OutboundName to refresh to live route Rule-WARP, got %q", recs[0].OutboundName)
	}
}

func TestTrafficSpikeOnRelayNodeWithMinorDirectConnection(t *testing.T) {
	ctx := context.Background()
	var (
		mu       sync.Mutex
		tgTexts  []string
	)
	tgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		mu.Lock()
		tgTexts = append(tgTexts, msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer tgServer.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeA := createTestNode(t, store, "Edge-Entry", "198.51.100.1")
	nodeB := createTestNode(t, store, "Landing-Relay", "198.51.100.2")

	if _, err := store.UpdateAlertSettings(ctx, AlertSettings{
		TrafficAlertEnabled:  true,
		TrafficThresholdMbps: 50,
		TrafficDurationSec:   0,
		CooldownMinutes:      1,
		TelegramEnabled:      true,
		TelegramServer:       tgServer.URL,
		TelegramBotToken:     "123:tok",
		TelegramChatID:       "100",
	}); err != nil {
		t.Fatal(err)
	}

	engine := NewAlertEngine(store, nil)
	engine.SetConnectionsHub(newConnectionsHub())

	// Upstream Edge-Entry holds the real origin client 112.86.163.226 (user alice) below nodeA threshold.
	engine.CheckConnectionsTelemetry(nodeA, "Edge-Entry", 1024, 1024, []storedConnection{{
		ID: "entry-c1", SourceIP: "112.86.163.226", Source: "112.86.163.226:55555",
		Destination: "198.18.20.20:443", Host: "heavy-cdn.example.com", Network: "tcp",
		User: "alice", Outbound: "chain-epB", ChainedTarget: "epB", HasRates: true, DownloadRate: 25 * 1000 * 1000,
	}})

	// Landing-Relay has BOTH a tiny 1 KB/s direct connection AND a 25 MB/s (200 Mbps) relay leg!
	engine.CheckConnectionsTelemetry(nodeB, "Landing-Relay", 25*1000*1000+1024, 1024, []storedConnection{
		{
			ID: "minor-direct", SourceIP: "10.0.0.5", Destination: "1.1.1.1:443",
			Host: "idle.example.com", Network: "tcp", Outbound: "direct",
			HasRates: true, DownloadRate: 1024, Download: 4096,
		},
		{
			ID: "relay-heavy", SourceIP: "198.51.100.1", Destination: "198.18.20.20:443",
			Host: "heavy-cdn.example.com", Network: "tcp", IsChainRelay: true,
			RelayUpstreamNodeID: nodeA, HasRates: true, DownloadRate: 25 * 1000 * 1000, Download: 100 * 1024 * 1024,
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(tgTexts)
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for traffic spike alert")
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	text := tgTexts[len(tgTexts)-1]
	if !strings.Contains(text, "112.86.163.226") || !strings.Contains(text, "alice") || !strings.Contains(text, "中转：Edge-Entry") || !strings.Contains(text, "heavy-cdn.example.com") {
		t.Errorf("expected traffic spike alert on relay node with minor direct conn to attribute spike to heavy-cdn + 112.86.163.226 + Edge-Entry, got:\n%s", text)
	}
}
