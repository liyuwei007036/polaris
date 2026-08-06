package control_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sb-control/sb-control/internal/control"
	gopkgyaml "gopkg.in/yaml.v3"
)

func TestStoredMihomoConfigReferencesNestedGroupsRulesAndAliases(t *testing.T) {
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

	var endpointIDs, nodeIDs []string
	var endpoints []control.Endpoint
	for index, name := range []string{"美国节点", "日本节点"} {
		nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, name)
		nodeIDs = append(nodeIDs, nodeID)
		if err := store.SetNodeClientAddress(t.Context(), nodeID, []string{"us.example.com", "jp.example.com"}[index]); err != nil {
			t.Fatal(err)
		}
		listener, err := store.CreateListener(t.Context(), control.Listener{
			NodeID: nodeID, Name: "VLESS WebSocket 入站", Domain: []string{"us-listener.example.com", "jp-listener.example.com"}[index], ListenAddr: "0.0.0.0", Port: uint16(1080 + index), Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "默认账号", Alias: []string{"洛杉矶 01", "东京 01"}[index], Enabled: true},
			control.EndpointCredentials{UUID: []string{"bf000d23-0752-40b4-affe-68f7707a9661", "4e05f165-94f3-4f54-aac7-0487dcb83011"}[index]})
		if err != nil {
			t.Fatal(err)
		}
		endpointIDs = append(endpointIDs, endpoint.ID)
		endpoints = append(endpoints, endpoint)
	}

	usGroup, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "美国节点", Strategy: "url-test", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	jpGroup, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "日本节点", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[1]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	allGroup, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "自动选择", Strategy: "fallback", Members: []control.MihomoGroupMember{{Kind: "group", ID: usGroup.ID}, {Kind: "group", ID: jpGroup.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := store.CreateMihomoRuleProvider(t.Context(), control.MihomoRuleProvider{
		Name: "远程代理规则", Behavior: "domain", Format: "mrs",
		URL: "https://rules.example.com/proxy.mrs", Path: "./ruleset/proxy.mrs",
		Interval: 86400, Proxy: "自动选择",
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name:            "手机配置",
		ProxyGroupIDs:   []string{allGroup.ID},
		RuleMode:        "table",
		RuleProviderIDs: []string{provider.ID},
		Rules: []control.MihomoRule{
			{Type: "RULE-SET", Value: "远程代理规则", Action: "自动选择"},
			{Type: "DOMAIN-SUFFIX", Value: "youtube.com", Action: "洛杉矶 01"},
			{Type: "DOMAIN-SUFFIX", Value: "example.com", Action: "自动选择"},
			{Type: "IP-ASN", Value: "13335", Action: "自动选择"},
			{Type: "MATCH", Action: "DIRECT"},
		},
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
	for _, expected := range []string{`"name":"美国节点"`, `"type":"url-test"`, `"name":"日本节点"`, `"name":"自动选择"`, `"proxies":["美国节点","日本节点"]`, `"name":"洛杉矶 01"`, `"name":"东京 01"`, `"server":"us-listener.example.com"`, `"server":"jp-listener.example.com"`, "geox-url:", "testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/GeoLite2-ASN.mmdb", "rule-providers:", `"远程代理规则": {"behavior":"domain","format":"mrs","interval":86400,"path":"./ruleset/proxy.mrs","proxy":"自动选择","type":"http","url":"https://rules.example.com/proxy.mrs"}`, "RULE-SET,远程代理规则,自动选择", "DOMAIN-SUFFIX,youtube.com,洛杉矶 01", "DOMAIN-SUFFIX,example.com,自动选择", "IP-ASN,13335,自动选择", "MATCH,DIRECT", "fake-ip-range: 198.18.0.1/16", "fake-ip-filter-mode: rule", "- MATCH,fake-ip", "respect-rules: false", "rcode://success", "direct-nameserver-follow-policy: false", "https://223.5.5.5/dns-query"} {
		if !strings.Contains(yaml, expected) {
			t.Fatalf("stored YAML does not contain %q:\n%s", expected, yaml)
		}
	}
	var generated struct {
		GeoxURL struct {
			ASN string `yaml:"asn"`
		} `yaml:"geox-url"`
		Profile struct {
			StoreSelected bool `yaml:"store-selected"`
		} `yaml:"profile"`
		TUN struct {
			Enable      bool     `yaml:"enable"`
			StrictRoute bool     `yaml:"strict-route"`
			DNSHijack   []string `yaml:"dns-hijack"`
		} `yaml:"tun"`
		DNS struct {
			IPv6                         bool     `yaml:"ipv6"`
			EnhancedMode                 string   `yaml:"enhanced-mode"`
			FakeIPRange                  string   `yaml:"fake-ip-range"`
			FakeIPFilterMode             string   `yaml:"fake-ip-filter-mode"`
			FakeIPFilter                 []string `yaml:"fake-ip-filter"`
			RespectRules                 bool     `yaml:"respect-rules"`
			DefaultNameserver            []string `yaml:"default-nameserver"`
			Nameserver                   []string `yaml:"nameserver"`
			DirectNameserver             []string `yaml:"direct-nameserver"`
			DirectNameserverFollowPolicy bool     `yaml:"direct-nameserver-follow-policy"`
			ProxyServerNameserver        []string `yaml:"proxy-server-nameserver"`
		} `yaml:"dns"`
		RuleProviders map[string]struct {
			Type     string `yaml:"type"`
			Behavior string `yaml:"behavior"`
			Format   string `yaml:"format"`
			URL      string `yaml:"url"`
			Path     string `yaml:"path"`
			Interval int    `yaml:"interval"`
			Proxy    string `yaml:"proxy"`
		} `yaml:"rule-providers"`
	}
	if err := gopkgyaml.Unmarshal([]byte(yaml), &generated); err != nil {
		t.Fatalf("generated YAML cannot be parsed: %v\n%s", err, yaml)
	}
	if provider := generated.RuleProviders["远程代理规则"]; provider.Type != "http" || provider.Behavior != "domain" || provider.Format != "mrs" || provider.Interval != 86400 || provider.Proxy != "自动选择" {
		t.Fatalf("generated rule provider = %#v", provider)
	}
	if !strings.Contains(generated.GeoxURL.ASN, "GeoLite2-ASN.mmdb") {
		t.Fatalf("generated ASN data source = %q", generated.GeoxURL.ASN)
	}
	if !generated.Profile.StoreSelected {
		t.Fatal("generated config does not persist the selected proxy node")
	}
	if !generated.TUN.Enable || !generated.TUN.StrictRoute || len(generated.TUN.DNSHijack) != 2 {
		t.Fatalf("generated TUN leak protection = %#v", generated.TUN)
	}
	if generated.DNS.IPv6 || generated.DNS.EnhancedMode != "fake-ip" || generated.DNS.FakeIPRange != "198.18.0.1/16" || generated.DNS.FakeIPFilterMode != "rule" || len(generated.DNS.FakeIPFilter) != 1 || generated.DNS.FakeIPFilter[0] != "MATCH,fake-ip" || generated.DNS.RespectRules {
		t.Fatalf("generated Fake-IP DNS configuration = %#v", generated.DNS)
	}
	if len(generated.DNS.Nameserver) != 1 || generated.DNS.Nameserver[0] != "rcode://success" || generated.DNS.DirectNameserverFollowPolicy {
		t.Fatalf("generated DNS routing configuration = %#v", generated.DNS)
	}
	for _, resolver := range append(append(generated.DNS.DefaultNameserver, generated.DNS.DirectNameserver...), generated.DNS.ProxyServerNameserver...) {
		if !strings.HasPrefix(resolver, "https://") || !strings.Contains(resolver, "/dns-query") {
			t.Fatalf("non-DoH resolver %q in generated YAML", resolver)
		}
	}

	endpoints[0].Alias = "洛杉矶 02"
	if _, err := store.UpdateEndpoint(t.Context(), endpoints[0], nil); err != nil {
		t.Fatal(err)
	}
	_, yaml, err = store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil || !strings.Contains(yaml, "DOMAIN-SUFFIX,youtube.com,洛杉矶 02") {
		t.Fatalf("renamed endpoint was not synchronized to client rules: %v\n%s", err, yaml)
	}

	config.RuleMode = "text"
	config.Rules = nil
	config.RawRules = "# 按顺序分流\nRULE-SET,远程代理规则,自动选择\nDOMAIN-SUFFIX,example.org,自动选择\nMATCH,REJECT"
	updated, err := store.UpdateMihomoClientConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RuleMode != "text" || len(updated.Rules) != 3 || len(updated.RuleProviders) != 1 || !strings.Contains(updated.RawRules, "# 按顺序分流") {
		t.Fatalf("advanced rules were not preserved: %#v", updated)
	}
	_, yaml, err = store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil || !strings.Contains(yaml, "DOMAIN-SUFFIX,example.org,自动选择") || !strings.Contains(yaml, "MATCH,REJECT") {
		t.Fatalf("advanced rules were not generated: %v\n%s", err, yaml)
	}
	allGroup.Name = "自动选择 2"
	if _, err := store.UpdateMihomoProxyGroup(t.Context(), allGroup); err != nil {
		t.Fatal(err)
	}
	_, yaml, err = store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil || !strings.Contains(yaml, "DOMAIN-SUFFIX,example.org,自动选择 2") || !strings.Contains(yaml, `"proxy":"自动选择 2"`) {
		t.Fatalf("renamed group was not synchronized to client rules: %v\n%s", err, yaml)
	}
	response := request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Mihomo subscription returned %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "application/yaml; charset=utf-8" {
		t.Fatalf("unexpected subscription content type %q", contentType)
	}
	if disposition := response.Header.Get("Content-Disposition"); !strings.Contains(disposition, `filename*=UTF-8''%E6%89%8B%E6%9C%BA%E9%85%8D%E7%BD%AE.yaml`) {
		t.Fatalf("subscription filename is not UTF-8 encoded: %q", disposition)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(body), `"name":"洛杉矶 02"`) {
		t.Fatalf("subscription content does not contain alias: %v\n%s", err, body)
	}
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/mihomo/subscription-access?config_id="+config.ID+"&ip=127&location="+url.QueryEscape("本机")+"&user_agent=Go-http-client", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list subscription access: got %d", response.StatusCode)
	}
	var accessPage struct {
		Total int                          `json:"total"`
		Logs  []control.SubscriptionAccess `json:"access_logs"`
	}
	decodeBody(t, response, &accessPage)
	if accessPage.Total != 1 || len(accessPage.Logs) != 1 || accessPage.Logs[0].ConfigName != "手机配置" || accessPage.Logs[0].Location != "本机" || accessPage.Logs[0].AccessedAt == "" {
		t.Fatalf("unexpected subscription access page: %#v", accessPage)
	}
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/mihomo/client-configs/"+config.ID+"/enabled", map[string]bool{"enabled": false}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable Mihomo client config: got %d", response.StatusCode)
	}
	response = request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled Mihomo subscription remained available: got %d", response.StatusCode)
	}
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/mihomo/client-configs/"+config.ID+"/enabled", map[string]bool{"enabled": true}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("enable Mihomo client config: got %d", response.StatusCode)
	}
	response = request(t, http.MethodGet, httpServer.URL+config.SubscriptionPath, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("re-enabled Mihomo subscription returned %d", response.StatusCode)
	}
	response.Body.Close()
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
	if _, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name: "缺少终结规则", RuleMode: "text", ProxyGroupIDs: []string{allGroup.ID},
		RawRules: "DOMAIN-SUFFIX,example.com,自动选择 2",
	}); err == nil || !strings.Contains(err.Error(), "MATCH") {
		t.Fatalf("client config accepted rules without terminal MATCH: %v", err)
	}
	if _, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name: "未知规则供应商", RuleMode: "table", ProxyGroupIDs: []string{allGroup.ID},
		Rules: []control.MihomoRule{{Type: "RULE-SET", Value: "missing", Action: "DIRECT"}, {Type: "MATCH", Action: "DIRECT"}},
	}); err == nil || !strings.Contains(err.Error(), "rule provider") {
		t.Fatalf("client config accepted an unknown rule provider: %v", err)
	}
	multiGroupConfig, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name: "多个代理组", RuleMode: "table", ProxyGroupIDs: []string{usGroup.ID, jpGroup.ID},
		Rules: []control.MihomoRule{{Type: "MATCH", Action: "DIRECT"}},
	})
	if err != nil {
		t.Fatalf("client config rejected multiple proxy groups: %v", err)
	}
	if err := store.DeleteMihomoClientConfig(t.Context(), multiGroupConfig.ID); err != nil {
		t.Fatal(err)
	}
	groupA, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "分组 A", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "分组 B", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "group", ID: groupA.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	groupA.Members = []control.MihomoGroupMember{{Kind: "group", ID: groupB.ID}}
	if _, err := store.UpdateMihomoProxyGroup(t.Context(), groupA); err == nil || !strings.Contains(err.Error(), "循环引用") {
		t.Fatalf("proxy group accepted a circular reference: %v", err)
	}
	if _, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "未知分组", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "group", ID: "missing"}},
	}); err == nil || !strings.Contains(err.Error(), "已不存在") {
		t.Fatalf("proxy group accepted an unknown group: %v", err)
	}
	if _, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "洛杉矶 02", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[0]}},
	}); err == nil || !strings.Contains(err.Error(), "冲突") {
		t.Fatalf("proxy group accepted a node/group name conflict: %v", err)
	}
	if _, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "direct", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpointIDs[0]}},
	}); err == nil || !strings.Contains(err.Error(), "保留名称") {
		t.Fatalf("proxy group accepted a reserved name: %v", err)
	}

	_, err = store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeIDs[0], Name: "HTTPUpgrade 接入", ListenAddr: "0.0.0.0", Port: 2080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "httpupgrade", Path: "/proxy"}},
	})
	if err == nil || !strings.Contains(err.Error(), "Reality, WebSocket, or gRPC") {
		t.Fatalf("accepted removed HTTPUpgrade transport: %v", err)
	}
	if _, err := store.CreateMihomoClientConfig(t.Context(), control.MihomoClientConfig{
		Name: "旧式组合", Groups: []control.MihomoClientGroup{{ID: "legacy-group"}}, RoutingProfileID: "legacy-profile",
	}); err == nil || !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("client config accepted embedded proxy groups: %v", err)
	}
	if err := store.DeleteMihomoProxyGroup(t.Context(), allGroup.ID); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("deleted a group referenced by a client config: %v", err)
	}
	if err := store.DeleteMihomoProxyGroup(t.Context(), usGroup.ID); !errors.Is(err, control.ErrConflict) {
		t.Fatalf("deleted a group referenced by another group: %v", err)
	}
	configs, err := store.ListMihomoClientConfigs(t.Context())
	if err != nil || len(configs) != 1 {
		t.Fatalf("invalid client configurations were persisted: %#v, %v", configs, err)
	}

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/mihomo/proxy-groups", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy group API is unavailable: got %d", response.StatusCode)
	}
	response.Body.Close()
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
		NodeID: nodeID, Name: "VLESS WebSocket 入站", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []control.Endpoint{
		{ListenerID: listener.ID, Name: "用户 A", Enabled: true, OutboundID: "direct"},
		{ListenerID: listener.ID, Name: "用户 B", Enabled: true, OutboundID: outbound.ID},
	} {
		if _, err := store.CreateEndpoint(t.Context(), endpoint, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}); err != nil {
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
		NodeID: nodeID, Name: "VLESS WebSocket 入站", ListenAddr: "0.0.0.0", Port: 1080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "用户 A", Enabled: true}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
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
	for _, expected := range []string{`"server":"proxy.example.com"`, `"uuid":"bf000d23-0752-40b4-affe-68f7707a9661"`, "DOMAIN-SUFFIX,github.com,PROXY", "IP-CIDR,1.1.1.0/24,PROXY,no-resolve", "MATCH,DIRECT"} {
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

func TestRemovedInboundProtocolsAreRejected(t *testing.T) {
	for _, protocol := range []string{"anytls", "http", "hysteria", "naive", "shadowsocks", "shadowtls", "snell", "socks", "trojan", "tuic", "vmess"} {
		err := control.ValidateProtocolSpec(control.ProtocolSpec{Protocol: protocol, Network: "tcp"})
		if err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("removed protocol %s error = %v", protocol, err)
		}
	}
}
