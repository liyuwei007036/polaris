package control

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSanitizeMihomoYAMLEmojisAndUnquoting(t *testing.T) {
	input := `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxies:
  - client-fingerprint: chrome
    flow: xtls-rprx-vision
    name: "\U0001F1F8\U0001F1EC  洛杉矶 40 Reailty ISP-LYW"
    port: 443
proxy-groups:
  - name: "\U0001F310 默认-GD"
    type: select
    proxies:
      - AI-LYW
      - "\U0001F1F8\U0001F1EC 新加坡"
      - "\U0001F1FA\U0001F1F8 美国"
rules:
  - DOMAIN-KEYWORD,dcsoft,DIRECT
  - "DOMAIN-SUFFIX,translate.google.com,\U0001F1FA\U0001F1F8 美国"
  - "MATCH,\U0001F310 默认-GD"
`

	sanitized := sanitizeMihomoYAML(input)

	if strings.Contains(sanitized, `\U`) || strings.Contains(sanitized, `\u`) {
		t.Fatalf("sanitized YAML still contains unicode escape sequences:\n%s", sanitized)
	}

	expectedSnippets := []string{
		"name: 🇸🇬  洛杉矶 40 Reailty ISP-LYW",
		"name: 🌐 默认-GD",
		"- 🇸🇬 新加坡",
		"- 🇺🇸 美国",
		"- DOMAIN-SUFFIX,translate.google.com,🇺🇸 美国",
		"- MATCH,🌐 默认-GD",
	}

	for _, expected := range expectedSnippets {
		if !strings.Contains(sanitized, expected) {
			t.Errorf("expected sanitized YAML to contain %q, but got:\n%s", expected, sanitized)
		}
	}

	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(sanitized), &parsed); err != nil {
		t.Fatalf("sanitized YAML failed to parse as valid YAML: %v\nYAML content:\n%s", err, sanitized)
	}
}

func TestGenerateStoredMihomoYAMLEmitsRawEmojis(t *testing.T) {
	store := createTestStore(t)
	ctx := t.Context()

	nodeID := createTestNode(t, store, "node-emoji", "198.51.100.1")
	realityKey, _, err := store.CreateRealityKey(ctx, "reality-key")
	if err != nil {
		t.Fatal(err)
	}

	listener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "reality-in",
		ListenAddr: "0.0.0.0",
		Port:       443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "vless",
			Network:  "tcp",
			TLS:      TLSOptions{Enabled: true},
			Reality: RealityOptions{
				Enabled:         true,
				HandshakeServer: "www.microsoft.com",
				HandshakePort:   443,
				KeyID:           realityKey.ID,
				ShortIDs:        []string{"0123456789abcdef"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener.ID,
		Name:       "user-sg",
		Alias:      "🇸🇬 新加坡 69 Reality",
		Enabled:    true,
	}, EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}

	group, err := store.CreateMihomoProxyGroup(ctx, MihomoProxyGroup{
		Name:     "🌐 默认-GD",
		Strategy: "select",
		Members: []MihomoGroupMember{
			{Kind: "endpoint", ID: endpoint.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	config, err := store.CreateMihomoClientConfig(ctx, MihomoClientConfig{
		Name:          "客户端配置",
		ProxyGroupIDs: []string{group.ID},
		RuleMode:      "table",
		Rules: []MihomoRule{
			{Type: "MATCH", Action: group.Name},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, generatedYAML, err := store.GenerateStoredMihomoYAML(ctx, config.ID)
	if err != nil {
		t.Fatalf("GenerateStoredMihomoYAML failed: %v", err)
	}

	if strings.Contains(generatedYAML, `\U`) || strings.Contains(generatedYAML, `\u`) {
		t.Fatalf("generated YAML should not contain escape sequences:\n%s", generatedYAML)
	}

	if !strings.Contains(generatedYAML, "name: 🇸🇬 新加坡 69 Reality") {
		t.Errorf("expected unescaped and unquoted node name, got:\n%s", generatedYAML)
	}

	if !strings.Contains(generatedYAML, "name: 🌐 默认-GD") {
		t.Errorf("expected unescaped and unquoted group name, got:\n%s", generatedYAML)
	}

	if !strings.Contains(generatedYAML, "- MATCH,🌐 默认-GD") {
		t.Errorf("expected unescaped and unquoted MATCH rule, got:\n%s", generatedYAML)
	}
}
