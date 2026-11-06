package control

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSanitizeASCIIFilename(t *testing.T) {
	tests := []struct {
		input    string
		fallback string
		want     string
	}{
		{"my-config", "sub", "my-config"},
		{"E2E 客户端", "sub", "E2E"},
		{"默认配置", "sub", "sub"},
		{"---", "sub", "sub"},
		{"a--b--c", "sub", "a-b-c"},
		{"", "default", "default"},
	}
	for _, tc := range tests {
		got := sanitizeASCIIFilename(tc.input, tc.fallback)
		if got != tc.want {
			t.Errorf("sanitizeASCIIFilename(%q, %q) = %q, want %q", tc.input, tc.fallback, got, tc.want)
		}
	}
}

func TestMihomoSubscriptionMetadataHeaders(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := "node-sub-1"
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "香港服务器", make([]byte, 32), "hk.example.com", nowUnix()); err != nil {
		t.Fatalf("insert node failed: %v", err)
	}

	listener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "vless-in",
		Domain:     "hk.example.com",
		ListenAddr: "0.0.0.0",
		Port:       443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "httpupgrade"},
		},
	})
	if err != nil {
		t.Fatalf("CreateListener failed: %v", err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener.ID,
		Name:       "user_sub_001",
		Alias:      "香港高速节点",
		Enabled:    true,
	}, EndpointCredentials{
		UUID: "a0000000-0000-0000-0000-000000000001",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint failed: %v", err)
	}

	group, err := store.CreateMihomoProxyGroup(ctx, MihomoProxyGroup{
		Name:     "香港代理",
		Strategy: "select",
		Members: []MihomoGroupMember{
			{Kind: "endpoint", ID: endpoint.ID},
		},
	})
	if err != nil {
		t.Fatalf("CreateMihomoProxyGroup failed: %v", err)
	}

	expiryTime := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	expiryUnix := expiryTime.Unix()
	config, err := store.CreateMihomoClientConfig(ctx, MihomoClientConfig{
		Name:            "高级香港订阅",
		ProxyGroupIDs:   []string{group.ID},
		AccessExpiresAt: expiryTime.Format(time.RFC3339),
		Rules: []MihomoRule{
			{Type: "MATCH", Action: group.Name},
		},
	})
	if err != nil {
		t.Fatalf("CreateMihomoClientConfig failed: %v", err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, config.SubscriptionPath, nil)
	req.Host = "panel.polaris.example.com"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("subscription pull returned status=%d, body=%s", rec.Code, rec.Body.String())
	}

	// 1. Profile-Title: base64 encoded UTF-8 name
	profileTitleHdr := rec.Header().Get("Profile-Title")
	wantTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte(config.Name))
	if profileTitleHdr != wantTitle {
		t.Errorf("Profile-Title header = %q, want %q", profileTitleHdr, wantTitle)
	}

	// 2. Profile-Update-Interval: 24h
	updateIntervalHdr := rec.Header().Get("Profile-Update-Interval")
	if updateIntervalHdr != "24" {
		t.Errorf("Profile-Update-Interval header = %q, want 24", updateIntervalHdr)
	}

	// 3. Profile-Web-Page-Url
	webUrlHdr := rec.Header().Get("Profile-Web-Page-Url")
	if webUrlHdr != "http://panel.polaris.example.com/" {
		t.Errorf("Profile-Web-Page-Url header = %q, want http://panel.polaris.example.com/", webUrlHdr)
	}

	// 4. Subscription-Userinfo with expire timestamp
	subUserInfoHdr := rec.Header().Get("Subscription-Userinfo")
	wantExpire := fmt.Sprintf("expire=%d", expiryUnix)
	if subUserInfoHdr != wantExpire {
		t.Errorf("Subscription-Userinfo header = %q, want %q", subUserInfoHdr, wantExpire)
	}

	// 5. Content-Disposition with UTF-8 and sanitized ASCII fallback
	contentDisposition := rec.Header().Get("Content-Disposition")
	if !strings.Contains(contentDisposition, `filename*=UTF-8''`) {
		t.Errorf("Content-Disposition missing UTF-8 filename: %q", contentDisposition)
	}

	// Test 304 Not Modified also includes metadata headers
	etag := rec.Header().Get("ETag")
	req304 := httptest.NewRequest(http.MethodGet, config.SubscriptionPath, nil)
	req304.Host = "panel.polaris.example.com"
	req304.Header.Set("If-None-Match", etag)
	rec304 := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec304, req304)

	if rec304.Code != http.StatusNotModified {
		t.Fatalf("subscription pull 304 returned status=%d", rec304.Code)
	}
	if rec304.Header().Get("Profile-Title") != wantTitle {
		t.Errorf("304 response missing Profile-Title header: %q", rec304.Header().Get("Profile-Title"))
	}
	if rec304.Header().Get("Subscription-Userinfo") != wantExpire {
		t.Errorf("304 response missing Subscription-Userinfo header: %q", rec304.Header().Get("Subscription-Userinfo"))
	}
}

func TestLegacyRuleModeAndDNSModeNormalization(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := "node-sub-legacy"
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "演示节点", make([]byte, 32), "node.example.com", nowUnix()); err != nil {
		t.Fatal(err)
	}

	listener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "hy2-in",
		Domain:     "node.example.com",
		ListenAddr: "0.0.0.0",
		Port:       8443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "hysteria2",
			Network:  "udp",
			TLS:      TLSOptions{Enabled: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener.ID,
		Name:       "user_legacy",
		Alias:      "演示端点",
		Enabled:    true,
	}, EndpointCredentials{
		Password: "pass",
	})
	if err != nil {
		t.Fatal(err)
	}

	group, err := store.CreateMihomoProxyGroup(ctx, MihomoProxyGroup{
		Name:     "默认分组",
		Strategy: "select",
		Members:  []MihomoGroupMember{{Kind: "endpoint", ID: endpoint.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Create with rule_mode="preset" and dns_mode="structured" (legacy/demo values)
	cfg := MihomoClientConfig{
		Name:          "兼容测试配置",
		ProxyGroupIDs: []string{group.ID},
		RuleMode:      "preset",
		DNSMode:       "structured",
		Rules: []MihomoRule{
			{Type: "MATCH", Action: group.Name},
		},
	}
	saved, err := store.CreateMihomoClientConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("CreateMihomoClientConfig failed on legacy modes: %v", err)
	}

	// Generating YAML should succeed without "rule mode must be table or text" error
	name, yaml, err := store.GenerateStoredMihomoYAML(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GenerateStoredMihomoYAML failed: %v", err)
	}
	if name != "兼容测试配置" || !strings.Contains(yaml, "proxies:") {
		t.Fatalf("GenerateStoredMihomoYAML output invalid: %s", yaml)
	}
}

func TestGeneralSubscriptionMetadataHeaders(t *testing.T) {
	ctx := t.Context()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := "node-sub-2"
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "日本服务器", make([]byte, 32), "jp.example.com", nowUnix()); err != nil {
		t.Fatalf("insert node failed: %v", err)
	}

	listener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "vless-jp",
		Domain:     "jp.example.com",
		ListenAddr: "0.0.0.0",
		Port:       443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "httpupgrade"},
		},
	})
	if err != nil {
		t.Fatalf("CreateListener failed: %v", err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener.ID,
		Name:       "user_sub_002",
		Alias:      "日本节点",
		Enabled:    true,
	}, EndpointCredentials{
		UUID: "b0000000-0000-0000-0000-000000000002",
	})
	if err != nil {
		t.Fatalf("CreateEndpoint failed: %v", err)
	}

	sub, token, err := store.CreateSubscription(ctx, SubscriptionInput{
		Kind:        "client",
		Name:        "通用测试订阅",
		EndpointIDs: []string{endpoint.ID},
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("CreateSubscription failed: %v", err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/access/"+token, nil)
	req.Host = "sub.polaris.example.com"
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("general subscription returned code=%d body=%s", rec.Code, rec.Body.String())
	}

	title := rec.Header().Get("Profile-Title")
	wantTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte(sub.Name))
	if title != wantTitle {
		t.Errorf("general subscription title = %q, want %q", title, wantTitle)
	}

	if rec.Header().Get("Profile-Update-Interval") != "24" {
		t.Errorf("general subscription update interval = %q, want 24", rec.Header().Get("Profile-Update-Interval"))
	}

	if rec.Header().Get("Profile-Web-Page-Url") != "http://sub.polaris.example.com/" {
		t.Errorf("general subscription web page url = %q, want http://sub.polaris.example.com/", rec.Header().Get("Profile-Web-Page-Url"))
	}
}
