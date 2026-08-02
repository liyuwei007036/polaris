package control_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sb-control/sb-control/internal/control"
)

func TestStoredMihomoCompositionSupportsMultipleGroupsAndProfiles(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)

	var endpointIDs []string
	for index, name := range []string{"美国节点", "日本节点"} {
		nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, name)
		if err := store.SetNodeClientAddress(t.Context(), nodeID, []string{"us.example.com", "jp.example.com"}[index]); err != nil {
			t.Fatal(err)
		}
		listener, err := store.CreateListener(t.Context(), control.Listener{
			NodeID: nodeID, Name: "SOCKS 入站", ListenAddr: "0.0.0.0", Port: uint16(1080 + index), Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "socks", Network: "tcp"},
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "默认账号", Enabled: true},
			control.EndpointCredentials{Username: "user", Password: "secret"})
		if err != nil {
			t.Fatal(err)
		}
		endpointIDs = append(endpointIDs, endpoint.ID)
	}

	manual, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "美国与日本", Strategy: "select", EndpointIDs: endpointIDs,
		Aliases: map[string]string{endpointIDs[0]: "洛杉矶 01", endpointIDs[1]: "东京 01"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "自动备用", Strategy: "fallback", EndpointIDs: endpointIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	routing, err := store.CreateMihomoRoutingProfile(t.Context(), control.MihomoRoutingProfile{
		Name: "国内直连", RulePreset: "china-direct", DefaultAction: "PROXY",
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name: "手机配置", ProxyGroupIDs: []string{manual.ID, fallback.ID}, RoutingProfileID: routing.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	name, yaml, err := store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if name != "手机配置" {
		t.Fatalf("unexpected config name %q", name)
	}
	if config.SubscriptionPath == "" {
		t.Fatal("new Mihomo client config did not receive a subscription path")
	}
	for _, expected := range []string{`"name":"美国与日本"`, `"name":"自动备用"`, `"name":"PROXY"`, `"name":"洛杉矶 01"`, `"name":"东京 01"`, `"server":"us.example.com"`, `"server":"jp.example.com"`, "GEOSITE,CN,DIRECT"} {
		if !strings.Contains(yaml, expected) {
			t.Fatalf("stored YAML does not contain %q:\n%s", expected, yaml)
		}
	}
	response := request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Mihomo subscription returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(body), `"name":"洛杉矶 01"`) {
		t.Fatalf("subscription content does not contain alias: %v\n%s", err, body)
	}
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/mihomo/client-configs/"+config.ID+"/subscription/rotate", map[string]any{}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rotate Mihomo subscription: got %d", response.StatusCode)
	}
	var rotated struct {
		Path string `json:"subscription_path"`
	}
	decodeBody(t, response, &rotated)
	if rotated.Path == "" || rotated.Path == config.SubscriptionPath {
		t.Fatalf("subscription path was not rotated: %#v", rotated)
	}
	response = request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("old Mihomo subscription path remained valid: got %d", response.StatusCode)
	}
	if binary := os.Getenv("MIHOMO_BIN"); binary != "" {
		configPath := filepath.Join(t.TempDir(), "stored-mihomo.yaml")
		if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "-t", "-f", configPath).CombinedOutput(); err != nil {
			t.Fatalf("official Mihomo validation of stored composition failed: %v\n%s", err, output)
		}
	}
	if err := store.DeleteMihomoProxyGroup(t.Context(), manual.ID); err != control.ErrConflict {
		t.Fatalf("expected referenced group conflict, got %v", err)
	}
	if err := store.DeleteMihomoRoutingProfile(t.Context(), routing.ID); err != control.ErrConflict {
		t.Fatalf("expected referenced profile conflict, got %v", err)
	}
}

func TestEndpointOutboundCompilesAuthenticatedUserRoutes(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "account-routing-node")
	outbound, err := store.CreateOutbound(t.Context(), control.Outbound{
		Name: "日本 SOCKS5", Type: "socks", Server: "127.0.0.1", ServerPort: 1088, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "SOCKS 入站", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "socks", Network: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []control.Endpoint{
		{ListenerID: listener.ID, Name: "用户 A", Enabled: true, OutboundID: "direct"},
		{ListenerID: listener.ID, Name: "用户 B", Enabled: true, OutboundID: outbound.ID},
	} {
		if _, err := store.CreateEndpoint(t.Context(), endpoint, control.EndpointCredentials{Username: endpoint.Name, Password: "secret"}); err != nil {
			t.Fatal(err)
		}
	}
	compiled, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(compiled), &config); err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, rule := range config.Route.Rules {
		users, _ := rule["auth_user"].([]any)
		if len(users) == 1 {
			found[users[0].(string)], _ = rule["outbound"].(string)
		}
	}
	if found["用户 A"] != "direct" {
		t.Fatalf("user A route = %q, want direct", found["用户 A"])
	}
	if found["用户 B"] != "outbound-"+outbound.ID {
		t.Fatalf("user B route = %q, want managed outbound", found["用户 B"])
	}
}

func TestGenerateMihomoYAMLUsesClientServerAddressAndRuleOrder(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "mihomo-node")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "proxy.example.com"); err != nil {
		t.Fatal(err)
	}

	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "SOCKS 入站", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "socks", Network: "tcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "用户 A", Enabled: true}, control.EndpointCredentials{Username: "alice", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}

	yaml, err := store.GenerateMihomoYAML(t.Context(), control.MihomoProfileInput{
		Name: "客户端", EndpointIDs: []string{endpoint.ID}, Strategy: "select",
		RejectDomains: []string{"ads.example.com"}, DirectDomains: []string{"example.cn"},
		ProxyDomains: []string{"github.com"}, ProxyCIDRs: []string{"1.1.1.0/24"}, DefaultAction: "DIRECT",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"server":"proxy.example.com"`, `"username":"alice"`, "DOMAIN-SUFFIX,github.com,PROXY", "IP-CIDR,1.1.1.0/24,PROXY,no-resolve", "MATCH,DIRECT"} {
		if !strings.Contains(yaml, expected) {
			t.Fatalf("generated YAML does not contain %q:\n%s", expected, yaml)
		}
	}
	reject := strings.Index(yaml, "ads.example.com,REJECT")
	direct := strings.Index(yaml, "example.cn,DIRECT")
	proxy := strings.Index(yaml, "github.com,PROXY")
	if reject < 0 || direct <= reject || proxy <= direct {
		t.Fatalf("unexpected rule order:\n%s", yaml)
	}

	presetYAML, err := store.GenerateMihomoYAML(t.Context(), control.MihomoProfileInput{
		Name: "国内直连", EndpointIDs: []string{endpoint.ID}, Strategy: "url-test", RulePreset: "china-direct",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"GEOSITE,CN,DIRECT", "GEOIP,CN,DIRECT,no-resolve", "MATCH,PROXY", `"type":"url-test"`} {
		if !strings.Contains(presetYAML, expected) {
			t.Fatalf("preset YAML does not contain %q:\n%s", expected, presetYAML)
		}
	}
	if binary := os.Getenv("MIHOMO_BIN"); binary != "" {
		configPath := filepath.Join(t.TempDir(), "mihomo.yaml")
		if err := os.WriteFile(configPath, []byte(presetYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "-t", "-f", configPath)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("official Mihomo validation failed: %v\n%s", err, output)
		}
	}
}

func TestMihomoRoutingProfileSupportsRawAndStructuredRules(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateMihomoRoutingProfile(t.Context(), control.MihomoRoutingProfile{
		Name: "双模式规则", RulePreset: "custom", DefaultAction: "DIRECT",
		RawRules: "# 原始文本可以保留注释\nDOMAIN-SUFFIX,example.com,PROXY\nIP-CIDR,1.1.1.0/24,REJECT,no-resolve\nAND,((DOMAIN,internal.example.com),(NETWORK,TCP)),DIRECT\nMATCH,DIRECT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Rules) != 3 || created.Rules[0].Type != "DOMAIN-SUFFIX" || created.Rules[1].Action != "REJECT" || created.Rules[2].Type != "AND" {
		t.Fatalf("parsed rules = %#v", created.Rules)
	}
	if created.Rules[2].Value != "((DOMAIN,internal.example.com),(NETWORK,TCP))" {
		t.Fatalf("logical rule value = %q", created.Rules[2].Value)
	}
	if created.DefaultAction != "DIRECT" || !strings.Contains(created.RawRules, "# 原始文本") {
		t.Fatalf("normalized profile = %#v", created)
	}

	profiles, err := store.ListMihomoRoutingProfiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || len(profiles[0].Rules) != 3 || !strings.Contains(profiles[0].RawRules, "MATCH,DIRECT") {
		t.Fatalf("stored profiles = %#v", profiles)
	}

	_, err = store.CreateMihomoRoutingProfile(t.Context(), control.MihomoRoutingProfile{
		Name: "非法规则", RulePreset: "custom", RawRules: "DOMAIN-SUFFIX,example.com,UNKNOWN",
	})
	if err == nil {
		t.Fatal("invalid raw Mihomo action accepted")
	}
}

func TestProtocolsThatRequireTLSRejectPlainInbound(t *testing.T) {
	for _, protocol := range []string{"naive", "hysteria", "tuic", "hysteria2", "anytls"} {
		network := "tcp"
		if protocol == "hysteria" || protocol == "tuic" || protocol == "hysteria2" {
			network = "udp"
		}
		err := control.ValidateProtocolSpec(control.ProtocolSpec{Protocol: protocol, Network: network})
		if err == nil || !strings.Contains(err.Error(), "requires TLS") {
			t.Fatalf("%s without TLS error = %v", protocol, err)
		}
	}
	if err := control.ValidateProtocolSpec(control.ProtocolSpec{Protocol: "shadowsocks", Network: "tcp"}); err != nil {
		t.Fatalf("Shadowsocks should not require TLS: %v", err)
	}
}
