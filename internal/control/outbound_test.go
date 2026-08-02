package control_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sb-control/sb-control/internal/control"
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
		NodeID: nodeID, Name: "socks-in", ListenAddr: "0.0.0.0", Port: 1081, Enabled: true,
		OutboundID: outbound.ID,
		Spec:       control.ProtocolSpec{Protocol: "socks", Network: "tcp"},
	})
	if err != nil {
		t.Fatalf("create listener with outbound: %v", err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{
		ListenerID: listener.ID, Name: "account-a", Enabled: true, OutboundID: outbound.ID,
	}, control.EndpointCredentials{Username: "account-a", Password: "account-password"})
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
		Spec:       control.ProtocolSpec{Protocol: "socks", Network: "tcp"},
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
