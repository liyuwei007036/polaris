package control

import (
	"strings"
	"testing"
)

// A node with a single TLS-terminating TCP listener still puts it behind the
// managed SNI router. sing-box binding the public port itself only works while
// nothing else on the host wants that port; an Nginx installed before polaris
// takes it first and leaves sing-box failing to start on every retry.
func TestSoleTCPListenerIsRoutedInsteadOfBindingThePublicPort(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "单服务节点", make([]byte, 32), "sole.example.com", nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, route, managed, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), Listener{
		NodeID: nodeID, Name: "唯一接入", Domain: "sole.example.com", ListenAddr: "0.0.0.0", Port: 443, Enabled: true,
		Spec: ProtocolSpec{
			Protocol: "vless", Network: "tcp",
			TLS:       TLSOptions{Enabled: true, ALPN: []string{"http/1.1"}},
			Transport: TransportOptions{Type: "ws", Path: "/sole"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !managed || route == nil {
		t.Fatalf("sole listener was not routed: managed=%v route=%#v", managed, route)
	}
	if listener.ListenAddr != "127.0.0.1" || listener.BackendPort == 443 || listener.Port != 443 {
		t.Fatalf("sole listener kept the public socket: %#v", listener)
	}
	if route.Port != 443 || route.SNI != "sole.example.com" || route.BackendPort != listener.BackendPort {
		t.Fatalf("route does not front the listener: %#v", route)
	}
	// The compiled Nginx configuration has to actually carry that mapping,
	// otherwise nothing listens on the public port at all.
	configuration, _, err := store.CompileNodeNginx(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"listen 0.0.0.0:443;", "ssl_preread on;", "sole.example.com"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("compiled Nginx configuration is missing %q:\n%s", expected, configuration)
		}
	}
}

// Hysteria2 runs on UDP, which the TCP-only router cannot carry, so it keeps
// the public port.
func TestSoleUDPListenerKeepsThePublicPort(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, "UDP 节点", make([]byte, 32), "udp.example.com", nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, route, managed, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), Listener{
		NodeID: nodeID, Name: "HY2 接入", Domain: "udp.example.com", ListenAddr: "0.0.0.0", Port: 443, Enabled: true,
		Spec:   ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true, ALPN: []string{"h3"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if managed || route != nil {
		t.Fatalf("UDP listener was routed: managed=%v route=%#v", managed, route)
	}
	if listener.ListenAddr != "0.0.0.0" || listener.BackendPort != 443 {
		t.Fatalf("UDP listener did not keep the public port: %#v", listener)
	}
}
