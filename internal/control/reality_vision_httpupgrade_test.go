package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/security"
)

func TestVLESSRealityVisionAndHTTPUpgrade(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)
	nodeID := createTestNode(t, store, "test-node", "198.51.100.1")

	// 1. Create a VLESS Reality listener
	realityKey, _, err := store.CreateRealityKey(ctx, "reality-key")
	if err != nil {
		t.Fatalf("create Reality key: %v", err)
	}

	realityListener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "reality-in",
		ListenAddr: "0.0.0.0",
		Port:       443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "vless",
			Network:  "tcp",
			TLS: TLSOptions{
				Enabled: true,
				ALPN:    []string{"h2", "http/1.1"},
			},
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
		t.Fatalf("create reality listener: %v", err)
	}

	// 2. Create endpoint for Reality listener without specifying Flow (should default to xtls-rprx-vision)
	realityEndpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: realityListener.ID,
		Name:       "reality-user",
		Alias:      "Reality 节点",
		Enabled:    true,
	}, EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("create reality endpoint: %v", err)
	}

	// Verify DB stored Flow
	loadedCreds, err := store.loadEndpointCredentials(ctx, realityListener.ID)
	if err != nil {
		t.Fatalf("load endpoint credentials: %v", err)
	}
	if len(loadedCreds) != 1 || loadedCreds[0].Credentials.Flow != "xtls-rprx-vision" {
		t.Fatalf("expected flow xtls-rprx-vision on Reality endpoint, got %#v", loadedCreds)
	}

	// Verify inbound compilation includes flow
	compiledInbound, err := store.compileInbound(ctx, realityListener, loadedCreds)
	if err != nil {
		t.Fatalf("compile reality inbound: %v", err)
	}
	users, ok := compiledInbound["users"].([]map[string]any)
	if !ok || len(users) != 1 {
		t.Fatalf("expected 1 user in compiled inbound: %#v", compiledInbound)
	}
	if users[0]["flow"] != "xtls-rprx-vision" {
		t.Fatalf("expected user flow xtls-rprx-vision, got %v", users[0]["flow"])
	}

	// Verify Reality share link includes security=reality, fp=chrome, flow=xtls-rprx-vision
	shareLink, err := store.clientSubscriptionLine(ctx, realityEndpoint.ID)
	if err != nil {
		t.Fatalf("generate reality share link: %v", err)
	}
	for _, expected := range []string{"security=reality", "fp=chrome", "flow=xtls-rprx-vision", "sni=www.microsoft.com", "sid=0123456789abcdef"} {
		if !strings.Contains(shareLink, expected) {
			t.Fatalf("share link %q is missing expected param %q", shareLink, expected)
		}
	}

	// Verify Mihomo YAML export includes flow and client-fingerprint
	mihomoProxy, err := store.mihomoProxy(ctx, realityEndpoint.ID, "198.51.100.1")
	if err != nil {
		t.Fatalf("build mihomo proxy for reality: %v", err)
	}
	if mihomoProxy["flow"] != "xtls-rprx-vision" {
		t.Fatalf("expected mihomo proxy flow xtls-rprx-vision, got %v", mihomoProxy["flow"])
	}
	if mihomoProxy["client-fingerprint"] != "chrome" {
		t.Fatalf("expected mihomo proxy client-fingerprint chrome, got %v", mihomoProxy["client-fingerprint"])
	}

	// 3. Create a VLESS HTTPUpgrade listener
	upgradeListener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "httpupgrade-in",
		ListenAddr: "0.0.0.0",
		Port:       8443,
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "vless",
			Network:  "tcp",
			TLS: TLSOptions{
				Enabled: true,
				ALPN:    []string{"http/1.1"},
			},
			Transport: TransportOptions{
				Type: "httpupgrade",
				Path: "/ray",
				Host: "upgrade.example.com",
			},
		},
	})
	if err != nil {
		t.Fatalf("create httpupgrade listener: %v", err)
	}

	upgradeEndpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: upgradeListener.ID,
		Name:       "upgrade-user",
		Alias:      "HTTPUpgrade 节点",
		Enabled:    true,
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create upgrade endpoint: %v", err)
	}

	// Verify DB stored Flow is empty for HTTPUpgrade (no vision)
	loadedUpgradeCreds, err := store.loadEndpointCredentials(ctx, upgradeListener.ID)
	if err != nil {
		t.Fatalf("load upgrade credentials: %v", err)
	}
	if len(loadedUpgradeCreds) != 1 || loadedUpgradeCreds[0].Credentials.Flow != "" {
		t.Fatalf("expected empty flow on HTTPUpgrade endpoint, got %q", loadedUpgradeCreds[0].Credentials.Flow)
	}

	// Verify inbound compilation has httpupgrade transport
	compiledUpgradeInbound, err := store.compileInbound(ctx, upgradeListener, loadedUpgradeCreds)
	if err != nil {
		t.Fatalf("compile httpupgrade inbound: %v", err)
	}
	trans, ok := compiledUpgradeInbound["transport"].(map[string]any)
	if !ok || trans["type"] != "httpupgrade" || trans["path"] != "/ray" {
		t.Fatalf("expected transport type httpupgrade, got %#v", trans)
	}

	// Verify share link contains type=httpupgrade
	upgradeShareLink, err := store.clientSubscriptionLine(ctx, upgradeEndpoint.ID)
	if err != nil {
		t.Fatalf("generate upgrade share link: %v", err)
	}
	if !strings.Contains(upgradeShareLink, "type=httpupgrade") || !strings.Contains(upgradeShareLink, "path=%2Fray") {
		t.Fatalf("upgrade share link is missing httpupgrade parameters: %s", upgradeShareLink)
	}

	// Verify Mihomo proxy has network: httpupgrade
	upgradeMihomo, err := store.mihomoProxy(ctx, upgradeEndpoint.ID, "198.51.100.1")
	if err != nil {
		t.Fatalf("build mihomo proxy for upgrade: %v", err)
	}
	if upgradeMihomo["network"] != "httpupgrade" {
		t.Fatalf("expected mihomo network httpupgrade, got %v", upgradeMihomo["network"])
	}
	opts, ok := upgradeMihomo["httpupgrade-opts"].(map[string]any)
	if !ok || opts["path"] != "/ray" {
		t.Fatalf("expected httpupgrade-opts path /ray, got %#v", opts)
	}
}

func TestMigrationAutoAssignsRealityFlowAndUpgradesXHTTP(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	nodeID := createTestNode(t, store, "node-mig", "198.51.100.2")
	realityKey, _, err := store.CreateRealityKey(ctx, "mig-reality-key")
	if err != nil {
		t.Fatal(err)
	}

	// Directly insert a legacy Reality listener and a legacy xhttp listener into raw db
	realitySpecID := "listener-reality-mig"
	realitySpecJSON, _ := json.Marshal(ProtocolSpec{
		Protocol: "vless",
		Network:  "tcp",
		TLS:      TLSOptions{Enabled: true},
		Reality: RealityOptions{
			Enabled:         true,
			HandshakeServer: "www.apple.com",
			HandshakePort:   443,
			KeyID:           realityKey.ID,
		},
	})
	_, err = store.db.Exec(`INSERT INTO listeners (id, node_id, name, protocol, network, connection_domain, listen_address, port, backend_port, enabled, outbound_id, spec, created_at, updated_at)
		VALUES (?, ?, 'legacy-reality', 'vless', 'tcp', 'apple.com', '0.0.0.0', 443, 443, 1, 'direct', ?, 1, 1)`, realitySpecID, nodeID, string(realitySpecJSON))
	if err != nil {
		t.Fatal(err)
	}

	// Insert endpoint with empty flow
	rawCreds, _ := json.Marshal(EndpointCredentials{UUID: "33333333-3333-3333-3333-333333333333", Flow: ""})
	encCreds, err := security.Encrypt(store.masterKey, rawCreds)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.Exec(`INSERT INTO endpoints (id, listener_id, name, alias, enabled, outbound_id, credentials, created_at, updated_at)
		VALUES ('ep-mig-1', ?, 'user-empty-flow', '别名', 1, 'direct', ?, 1, 1)`, realitySpecID, encCreds)
	if err != nil {
		t.Fatal(err)
	}

	// Insert legacy xhttp listener
	xhttpSpecID := "listener-xhttp-mig"
	xhttpSpecJSON, _ := json.Marshal(ProtocolSpec{
		Protocol:  "vless",
		Network:   "tcp",
		TLS:       TLSOptions{Enabled: true, ALPN: []string{"h2", "http/1.1"}},
		Transport: TransportOptions{Type: "xhttp", Path: "/legacy-xhttp"},
	})
	_, err = store.db.Exec(`INSERT INTO listeners (id, node_id, name, protocol, network, connection_domain, listen_address, port, backend_port, enabled, outbound_id, spec, created_at, updated_at)
		VALUES (?, ?, 'legacy-xhttp', 'vless', 'tcp', 'xhttp.com', '0.0.0.0', 8443, 8443, 1, 'direct', ?, 1, 1)`, xhttpSpecID, nodeID, string(xhttpSpecJSON))
	if err != nil {
		t.Fatal(err)
	}

	store.Close()

	// Re-open store to trigger migrations
	reopened, err := Open(directory)
	if err != nil {
		t.Fatalf("re-open store: %v", err)
	}
	defer reopened.Close()

	// Verify Reality endpoint got migrated to flow: xtls-rprx-vision
	migratedCreds, err := reopened.loadEndpointCredentials(ctx, realitySpecID)
	if err != nil {
		t.Fatalf("load migrated reality credentials: %v", err)
	}
	if len(migratedCreds) != 1 || migratedCreds[0].Credentials.Flow != "xtls-rprx-vision" {
		t.Fatalf("expected flow xtls-rprx-vision after migration, got %#v", migratedCreds)
	}

	// Verify xhttp listener was migrated to httpupgrade
	listeners, err := reopened.ListListeners(ctx, nodeID)
	if err != nil {
		t.Fatalf("list listeners: %v", err)
	}
	var foundUpgrade bool
	for _, l := range listeners {
		if l.ID == xhttpSpecID {
			foundUpgrade = true
			if l.Spec.Transport.Type != "httpupgrade" {
				t.Fatalf("expected transport type httpupgrade after migration, got %q", l.Spec.Transport.Type)
			}
			if len(l.Spec.TLS.ALPN) != 1 || l.Spec.TLS.ALPN[0] != "http/1.1" {
				t.Fatalf("expected ALPN [http/1.1], got %v", l.Spec.TLS.ALPN)
			}
		}
	}
	if !foundUpgrade {
		t.Fatal("xhttp listener not found after migration")
	}
}
