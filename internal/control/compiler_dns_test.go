package control

import (
	"encoding/json"
	"testing"
)

func TestCompiledConfigurationIncludesInboundDNSHijacking(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := newTestNode(t, store, "dns-node", "d")
	compiled, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatalf("CompileNodeConfig failed: %v", err)
	}

	var parsed struct {
		DNS struct {
			Servers []map[string]any `json:"servers"`
		} `json:"dns"`
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(compiled), &parsed); err != nil {
		t.Fatalf("Unmarshal compiled config failed: %v", err)
	}

	// 1. Verify DNS servers contains local-dns
	if len(parsed.DNS.Servers) == 0 || parsed.DNS.Servers[0]["tag"] != "local-dns" {
		t.Fatalf("expected local-dns in parsed.DNS.Servers, got %#v", parsed.DNS.Servers)
	}

	// 2. Verify first route rule is hijack-dns on port 53
	if len(parsed.Route.Rules) == 0 {
		t.Fatalf("expected at least 1 route rule for hijack-dns, got none")
	}
	firstRule := parsed.Route.Rules[0]
	if firstRule["action"] != "hijack-dns" {
		t.Fatalf("expected first route rule to have action 'hijack-dns', got %#v", firstRule)
	}
	portVal, ok := firstRule["port"].(float64)
	if !ok || int(portVal) != 53 {
		t.Fatalf("expected hijack-dns port to be 53, got %#v", firstRule["port"])
	}
}

func TestUserDefinedRejectRulePrecedesAccountCatchAll(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := newTestNode(t, store, "reject-node", "r")
	listener, err := store.CreateListener(t.Context(), tlsListener(nodeID, "服务", "reject.example.com", 8443, true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateEndpoint(t.Context(), Endpoint{ListenerID: listener.ID, Name: "用户A", Enabled: true, OutboundID: "direct"}, EndpointCredentials{UUID: "11111111-2222-3333-4444-555555555555"})
	if err != nil {
		t.Fatal(err)
	}

	// Add a user-defined route rule that rejects chrome.cloudflare-dns.com
	_, err = store.CreateRouteRule(t.Context(), RouteRule{
		NodeID:  nodeID,
		Action:  "reject",
		Domains: []string{"chrome.cloudflare-dns.com"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	compiled, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatalf("CompileNodeConfig failed: %v", err)
	}

	var parsed struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(compiled), &parsed); err != nil {
		t.Fatalf("Unmarshal compiled config failed: %v", err)
	}

	// Expected order:
	// Rules[0]: hijack-dns
	// Rules[1]: reject rule for chrome.cloudflare-dns.com
	// Rules[2]: account rule for 用户A (outbound: direct)
	if len(parsed.Route.Rules) < 3 {
		t.Fatalf("expected at least 3 rules, got %d: %#v", len(parsed.Route.Rules), parsed.Route.Rules)
	}
	if parsed.Route.Rules[0]["action"] != "hijack-dns" {
		t.Fatalf("expected Rules[0] to be hijack-dns, got %#v", parsed.Route.Rules[0])
	}
	if parsed.Route.Rules[1]["action"] != "reject" {
		t.Fatalf("expected Rules[1] to be reject rule, got %#v", parsed.Route.Rules[1])
	}
	if parsed.Route.Rules[2]["action"] != "route" || parsed.Route.Rules[2]["auth_user"] == nil {
		t.Fatalf("expected Rules[2] to be account catch-all rule, got %#v", parsed.Route.Rules[2])
	}
}
