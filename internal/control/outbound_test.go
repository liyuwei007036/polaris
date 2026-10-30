package control_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

// TestManagedOutboundCompilation exercises the managed-outbound surface end to
// end: an encrypted SOCKS5 proxy is created over the API (outbounds are global,
// not tied to any node), a listener selects it as its default egress, and the
// compiled sing-box config gains both the outbound object and a fallback route
// rule. It also verifies the password is never returned by the API yet is
// present (decrypted) in the compiled config, that a listener cannot reference
// a nonexistent outbound, and that deleting an outbound detaches any listener
// that used it.
func TestManagedOutboundCompilation(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "outbound-node")

	// Create an authenticated SOCKS5 outbound over the API.
	const proxyPassword = "s3cr3t-pass-9f2a"
	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/outbounds", map[string]any{
		"name": "upstream-hk", "type": "socks",
		"server": "10.9.8.7", "server_port": 1080,
		"username": "proxyuser", "password": proxyPassword, "enabled": true,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create outbound: got %d", response.StatusCode)
	}
	var outbound control.Outbound
	decodeBody(t, response, &outbound)
	if outbound.ID == "" {
		t.Fatalf("unexpected outbound: %#v", outbound)
	}
	if outbound.Password != "" {
		t.Fatal("create response leaked the proxy password")
	}

	// The list endpoint must never expose the stored secret.
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/outbounds", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list outbounds: got %d", response.StatusCode)
	}
	var listed struct {
		Outbounds []control.Outbound `json:"outbounds"`
	}
	decodeBody(t, response, &listed)
	if len(listed.Outbounds) != 1 || listed.Outbounds[0].Password != "" {
		t.Fatalf("list outbounds leaked secret or wrong count: %#v", listed.Outbounds)
	}

	// A listener selecting the outbound compiles into an outbound object plus a
	// fallback route rule tying its inbound to that outbound tag.
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-httpupgrade-in", ListenAddr: "0.0.0.0", Port: 1081, Enabled: true,
		OutboundID: outbound.ID,
		Spec:       control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "httpupgrade"}},
	})
	if err != nil {
		t.Fatalf("create listener with outbound: %v", err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{
		ListenerID: listener.ID, Name: "account-a", Enabled: true, OutboundID: outbound.ID,
	}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatalf("create endpoint with outbound: %v", err)
	}
	rule, err := store.CreateRouteRule(t.Context(), control.RouteRule{
		NodeID: nodeID, Priority: 10, Enabled: true, DomainSuffix: []string{"example.com"},
		Action: "outbound", OutboundTag: outbound.ID,
	})
	if err != nil {
		t.Fatalf("create route rule with outbound: %v", err)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	for _, want := range []string{
		"outbound-" + outbound.ID, // outbound tag
		"listener-" + listener.ID, // inbound referenced by the fallback rule
		"10.9.8.7",                // proxy server address
		proxyPassword,             // decrypted secret reaches the node config
	} {
		if !strings.Contains(configuration, want) {
			t.Fatalf("compiled config missing %q\n%s", want, configuration)
		}
	}
	var compiled struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(configuration), &compiled); err != nil {
		t.Fatal(err)
	}
	if len(compiled.Route.Rules) == 0 || compiled.Route.Rules[0]["outbound"] != "outbound-"+outbound.ID {
		t.Fatalf("managed route rule did not use the compiled outbound tag: %#v", compiled.Route.Rules)
	}

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/outbounds/"+outbound.ID+"/enabled", map[string]bool{"enabled": false}, session, csrfToken)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("disable referenced outbound: got %d", response.StatusCode)
	}

	// A listener cannot reference an outbound that does not exist.
	if _, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "bad-out", ListenAddr: "0.0.0.0", Port: 1082, Enabled: true,
		OutboundID: "nonexistent-outbound",
		Spec:       control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "httpupgrade"}},
	}); err == nil {
		t.Fatal("accepted listener referencing a dangling outbound")
	}

	// Deleting the outbound detaches the listener back to direct egress.
	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/outbounds/"+outbound.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete outbound: got %d", response.StatusCode)
	}
	if response.Header.Get("X-SB-Auto-Apply-Task") == "" {
		t.Fatal("deleting a referenced outbound did not queue configuration apply")
	}
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, l := range listeners {
		if l.ID == listener.ID {
			found = true
			if l.OutboundID != "" {
				t.Fatalf("listener still references deleted outbound: %q", l.OutboundID)
			}
		}
	}
	if !found {
		t.Fatal("listener disappeared after outbound deletion")
	}
	endpoints, err := store.ListEndpoints(t.Context(), listener.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].ID != endpoint.ID || endpoints[0].OutboundID != "direct" {
		t.Fatalf("endpoint did not fall back to direct after outbound deletion: %#v", endpoints)
	}
	rules, err := store.ListRouteRules(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID || rules[0].Action != "direct" || rules[0].OutboundTag != "" {
		t.Fatalf("route rule did not fall back to direct after outbound deletion: %#v", rules)
	}
}

func TestDirectIPOutboundCompilation(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := t.Context()

	nodeID := "node-direct-test"
	if err := store.ExecForTest(ctx, `INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "test-node", make([]byte, 32), "198.51.100.1", 1000); err != nil {
		t.Fatal(err)
	}

	listener, _, _, err := store.CreateListenerWithAutomaticPortRouting(ctx, control.Listener{
		NodeID:     nodeID,
		Name:       "vless-direct",
		Domain:     "vless.example.com",
		Port:       8443,
		Enabled:    true,
		Spec:       control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "httpupgrade"}},
		OutboundID: "direct",
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	// Verify invalid direct IP is rejected
	_, err = store.CreateEndpoint(ctx, control.Endpoint{
		ListenerID: listener.ID,
		Name:       "invalid-user",
		Alias:      "Invalid User",
		Enabled:    true,
		OutboundID: "direct:invalid-ip-addr",
	}, control.EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err == nil {
		t.Fatal("expected error for invalid direct IP, got nil")
	}

	// Create user with IPv4 direct outbound
	ipv4User, err := store.CreateEndpoint(ctx, control.Endpoint{
		ListenerID: listener.ID,
		Name:       "u-ipv4",
		Alias:      "IPv4 Node",
		Enabled:    true,
		OutboundID: "direct:198.51.100.55",
	}, control.EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create IPv4 endpoint: %v", err)
	}

	// Create user with IPv6 direct outbound
	ipv6User, err := store.CreateEndpoint(ctx, control.Endpoint{
		ListenerID: listener.ID,
		Name:       "u-ipv6",
		Alias:      "IPv6 Node",
		Enabled:    true,
		OutboundID: "direct:2001:db8::88",
	}, control.EndpointCredentials{UUID: "33333333-3333-3333-3333-333333333333"})
	if err != nil {
		t.Fatalf("create IPv6 endpoint: %v", err)
	}

	configJSON, _, err := store.CompileNodeConfig(ctx, nodeID)
	if err != nil {
		t.Fatalf("compile node config: %v", err)
	}

	var parsed struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	var foundIPv4, foundIPv6 bool
	for _, ob := range parsed.Outbounds {
		tag, _ := ob["tag"].(string)
		if tag == "direct-198.51.100.55" {
			foundIPv4 = true
			if ob["type"] != "direct" {
				t.Errorf("expected type direct, got %v", ob["type"])
			}
			if ob["inet4_bind_address"] != "198.51.100.55" {
				t.Errorf("expected inet4_bind_address 198.51.100.55, got %v", ob["inet4_bind_address"])
			}
			if ob["domain_strategy"] != "prefer_ipv4" {
				t.Errorf("expected domain_strategy prefer_ipv4, got %v", ob["domain_strategy"])
			}
		}
		if tag == "direct-2001:db8::88" {
			foundIPv6 = true
			if ob["type"] != "direct" {
				t.Errorf("expected type direct, got %v", ob["type"])
			}
			if ob["inet6_bind_address"] != "2001:db8::88" {
				t.Errorf("expected inet6_bind_address 2001:db8::88, got %v", ob["inet6_bind_address"])
			}
			if ob["domain_strategy"] != "prefer_ipv6" {
				t.Errorf("expected domain_strategy prefer_ipv6, got %v", ob["domain_strategy"])
			}
		}
	}
	if !foundIPv4 {
		t.Error("outbound direct-198.51.100.55 not found in config")
	}
	if !foundIPv6 {
		t.Error("outbound direct-2001:db8::88 not found in config")
	}

	var routedIPv4, routedIPv6 bool
	for _, rule := range parsed.Route.Rules {
		users, _ := rule["auth_user"].([]any)
		outbound, _ := rule["outbound"].(string)
		for _, u := range users {
			if u == ipv4User.Name && outbound == "direct-198.51.100.55" {
				routedIPv4 = true
			}
			if u == ipv6User.Name && outbound == "direct-2001:db8::88" {
				routedIPv6 = true
			}
		}
	}
	if !routedIPv4 {
		t.Error("route rule for u-ipv4 targeting direct-198.51.100.55 not found")
	}
	if !routedIPv6 {
		t.Error("route rule for u-ipv6 targeting direct-2001:db8::88 not found")
	}
}
