package control_test

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
	gopkgyaml "gopkg.in/yaml.v3"
)

// newMihomoDNSFixture builds the smallest configuration that can produce a
// subscription: one node with one account, referenced by one proxy group.
func newMihomoDNSFixture(t *testing.T) (*control.Store, string) {
	t.Helper()
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "dns-node")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "dns.example.com"); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "入站", Domain: "dns-listener.example.com", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "默认账号", Alias: "DNS 节点", Enabled: true},
		control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "DNS 分组", Strategy: "select", Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpoint.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, group.ID
}

func mihomoDNSConfig(groupID string) control.MihomoClientConfig {
	return control.MihomoClientConfig{
		Name: "DNS 配置", ProxyGroupIDs: []string{groupID}, RuleMode: "table",
		Rules: []control.MihomoRule{{Type: "MATCH", Action: "DNS 分组"}},
	}
}

// An operator who does not fill the DNS section gets a subscription without a
// dns block, so the client keeps whatever DNS settings it already has.
func TestMihomoSubscriptionOmitsUnconfiguredDNS(t *testing.T) {
	store, groupID := newMihomoDNSFixture(t)
	config, err := store.CreateMihomoClientConfig(t.Context(), mihomoDNSConfig(groupID))
	if err != nil {
		t.Fatal(err)
	}
	_, yaml, err := store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	var generated map[string]any
	if err := gopkgyaml.Unmarshal([]byte(yaml), &generated); err != nil {
		t.Fatalf("generated YAML cannot be parsed: %v\n%s", err, yaml)
	}
	if _, exists := generated["dns"]; exists {
		t.Fatalf("subscription carries a dns block that was never configured:\n%s", yaml)
	}
	if _, exists := generated["proxies"]; !exists {
		t.Fatalf("subscription is missing its proxies:\n%s", yaml)
	}
}

func TestMihomoSubscriptionCarriesConfiguredDNS(t *testing.T) {
	store, groupID := newMihomoDNSFixture(t)
	input := mihomoDNSConfig(groupID)
	input.DNSMode = "form"
	input.DNS = control.MihomoClientDNS{
		Enable: true, IPv6: true, EnhancedMode: "fake-ip", FakeIPRange: "28.0.0.1/8",
		FakeIPFilterMode: "blacklist", FakeIPFilter: []string{"*.lan"},
		DefaultNameserver:     []string{"119.29.29.29"},
		Nameserver:            []string{"https://dns.example.net/dns-query"},
		ProxyServerNameserver: []string{"https://dns.example.net/dns-query"},
		DirectNameserver:      []string{"system"},
		RespectRules:          true,
	}
	config, err := store.CreateMihomoClientConfig(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, yaml, err := store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	var generated struct {
		DNS struct {
			Enable                bool     `yaml:"enable"`
			IPv6                  bool     `yaml:"ipv6"`
			EnhancedMode          string   `yaml:"enhanced-mode"`
			FakeIPRange           string   `yaml:"fake-ip-range"`
			FakeIPFilterMode      string   `yaml:"fake-ip-filter-mode"`
			FakeIPFilter          []string `yaml:"fake-ip-filter"`
			RespectRules          bool     `yaml:"respect-rules"`
			DefaultNameserver     []string `yaml:"default-nameserver"`
			Nameserver            []string `yaml:"nameserver"`
			ProxyServerNameserver []string `yaml:"proxy-server-nameserver"`
			DirectNameserver      []string `yaml:"direct-nameserver"`
		} `yaml:"dns"`
	}
	if err := gopkgyaml.Unmarshal([]byte(yaml), &generated); err != nil {
		t.Fatalf("generated YAML cannot be parsed: %v\n%s", err, yaml)
	}
	dns := generated.DNS
	if !dns.Enable || !dns.IPv6 || dns.EnhancedMode != "fake-ip" || dns.FakeIPRange != "28.0.0.1/8" || dns.FakeIPFilterMode != "blacklist" || !dns.RespectRules {
		t.Fatalf("configured DNS was not carried through: %#v\n%s", dns, yaml)
	}
	if len(dns.FakeIPFilter) != 1 || dns.FakeIPFilter[0] != "*.lan" || len(dns.DirectNameserver) != 1 || dns.DirectNameserver[0] != "system" {
		t.Fatalf("configured DNS lists were not carried through: %#v", dns)
	}
	if len(dns.Nameserver) != 1 || dns.Nameserver[0] != "https://dns.example.net/dns-query" || len(dns.DefaultNameserver) != 1 || dns.DefaultNameserver[0] != "119.29.29.29" {
		t.Fatalf("configured resolvers were not carried through: %#v", dns)
	}
}

// The text editor exists so an operator can use dns keys the form does not
// model; the section is written through unchanged.
func TestMihomoSubscriptionWritesRawDNSUnchanged(t *testing.T) {
	store, groupID := newMihomoDNSFixture(t)
	input := mihomoDNSConfig(groupID)
	input.DNSMode = "text"
	input.RawDNS = "enable: true\nenhanced-mode: fake-ip\nnameserver:\n  - https://dns.example.net/dns-query\nnameserver-policy:\n  \"+.lan\": system\n"
	config, err := store.CreateMihomoClientConfig(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	_, yaml, err := store.GenerateStoredMihomoYAML(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	var generated struct {
		DNS struct {
			EnhancedMode     string            `yaml:"enhanced-mode"`
			Nameserver       []string          `yaml:"nameserver"`
			NameserverPolicy map[string]string `yaml:"nameserver-policy"`
		} `yaml:"dns"`
	}
	if err := gopkgyaml.Unmarshal([]byte(yaml), &generated); err != nil {
		t.Fatalf("generated YAML cannot be parsed: %v\n%s", err, yaml)
	}
	if generated.DNS.EnhancedMode != "fake-ip" || generated.DNS.NameserverPolicy["+.lan"] != "system" {
		t.Fatalf("raw DNS section was not written through: %#v\n%s", generated.DNS, yaml)
	}
}

func TestMihomoClientDNSRejectsSettingsMihomoWouldRefuse(t *testing.T) {
	store, groupID := newMihomoDNSFixture(t)
	for name, mutate := range map[string]func(*control.MihomoClientConfig){
		"nameserver 为空": func(config *control.MihomoClientConfig) {
			config.DNS.Nameserver = nil
		},
		"default-nameserver 为空": func(config *control.MihomoClientConfig) {
			config.DNS.DefaultNameserver = nil
		},
		"default-nameserver 不是 IP": func(config *control.MihomoClientConfig) {
			config.DNS.DefaultNameserver = []string{"https://dns.example.net/dns-query"}
		},
		"respect-rules 缺少节点解析器": func(config *control.MihomoClientConfig) {
			config.DNS.RespectRules, config.DNS.ProxyServerNameserver = true, nil
		},
		"fake-ip-range 不是 IPv4 网段": func(config *control.MihomoClientConfig) {
			config.DNS.FakeIPRange = "fdfe:dcba:9876::1/64"
		},
		"解析模式无效": func(config *control.MihomoClientConfig) {
			config.DNS.EnhancedMode = "turbo"
		},
		"高级文本不是 YAML": func(config *control.MihomoClientConfig) {
			config.DNSMode, config.RawDNS = "text", "enable: true\n  nameserver: ["
		},
		"高级文本启用后缺少 nameserver": func(config *control.MihomoClientConfig) {
			config.DNSMode, config.RawDNS = "text", "enable: true\nenhanced-mode: fake-ip\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := mihomoDNSConfig(groupID)
			input.Name = "DNS " + name
			input.DNSMode = "form"
			input.DNS = control.MihomoClientDNS{
				Enable: true, EnhancedMode: "fake-ip", FakeIPRange: "198.18.0.1/16",
				FakeIPFilterMode: "rule", FakeIPFilter: []string{"MATCH,fake-ip"},
				DefaultNameserver:     []string{"223.5.5.5"},
				Nameserver:            []string{"https://223.5.5.5/dns-query"},
				ProxyServerNameserver: []string{"https://223.5.5.5/dns-query"},
			}
			mutate(&input)
			if _, err := store.CreateMihomoClientConfig(t.Context(), input); err == nil {
				t.Fatal("configuration was accepted although Mihomo would refuse it")
			} else if strings.TrimSpace(err.Error()) == "" {
				t.Fatal("rejection did not explain itself")
			}
		})
	}
}
