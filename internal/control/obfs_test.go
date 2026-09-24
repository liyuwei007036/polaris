package control

import (
	"context"
	"strings"
	"testing"
)

func TestHysteria2SalamanderObfuscation(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// 1. Validation test
	invalidSpec := ProtocolSpec{
		Protocol: "vless",
		Network:  "tcp",
		Obfs:     ObfsOptions{Type: "salamander", Password: "test"},
	}
	if err := ValidateProtocolSpec(invalidSpec); err == nil {
		t.Errorf("expected error when setting obfs on vless, got nil")
	}

	validSpec := ProtocolSpec{
		Protocol: "hysteria2",
		Network:  "udp",
		TLS:      TLSOptions{Enabled: true},
		Obfs:     ObfsOptions{Type: "salamander", Password: "my-obfs-password"},
	}
	if err := ValidateProtocolSpec(validSpec); err != nil {
		t.Fatalf("expected valid obfs spec, got: %v", err)
	}

	// 2. Compiler test
	nodeID := "node-obfs"
	_, err = store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "Node Obfs", []byte("12345678901234567890123456789012"), nowUnix())
	if err != nil {
		t.Fatalf("create node: %v", err)
	}

	listener, err := store.CreateListener(ctx, Listener{
		NodeID:     nodeID,
		Name:       "hy2-obfs",
		Domain:     "hy2.example.com",
		ListenAddr: "0.0.0.0",
		Port:       4443,
		Enabled:    true,
		Spec:       validSpec,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}

	endpoint, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener.ID,
		Name:       "user1",
		Enabled:    true,
	}, EndpointCredentials{Password: "user-pass"})
	if err != nil {
		t.Fatalf("create endpoint: %v", err)
	}

	compiledJSON, _, err := store.CompileNodeConfig(ctx, nodeID)
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}
	if !strings.Contains(compiledJSON, `"type": "salamander"`) || !strings.Contains(compiledJSON, `"password": "my-obfs-password"`) {
		t.Errorf("compiled singbox config missing obfs: %s", compiledJSON)
	}

	// 3. Mihomo export test
	yaml, err := store.GenerateMihomoYAML(ctx, MihomoProfileInput{
		Name:        "test-profile",
		EndpointIDs: []string{endpoint.ID},
		Strategy:    "select",
	})
	if err != nil {
		t.Fatalf("render mihomo yaml: %v", err)
	}
	if (!strings.Contains(yaml, `"obfs":"salamander"`) && !strings.Contains(yaml, "obfs: salamander")) ||
		(!strings.Contains(yaml, `"obfs-password":"my-obfs-password"`) && !strings.Contains(yaml, "obfs-password: my-obfs-password")) {
		t.Errorf("mihomo yaml missing obfs: %s", yaml)
	}

	// 4. Share link test
	links, err := store.ListEndpointShareLinks(ctx, listener.ID)
	if err != nil {
		t.Fatalf("generate share link: %v", err)
	}
	if len(links) == 0 {
		t.Fatalf("expected at least 1 share link")
	}
	link := links[0].Link
	if !strings.Contains(link, "obfs=salamander") || !strings.Contains(link, "obfs-password=my-obfs-password") {
		t.Errorf("share link missing obfs params: %s", link)
	}
}
