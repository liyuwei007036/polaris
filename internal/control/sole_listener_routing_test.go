package control

import (
	"strings"
	"testing"
)

// A node with a single TLS-terminating TCP listener binds the public port
// itself. Routing it through Nginx would gain nothing — there is no second
// listener to tell apart — and would cost every connection its client address,
// because Nginx forwards from loopback and a Reality connection has no layer
// left that could carry the original address across.
func TestSoleTCPListenerBindsThePublicPortItself(t *testing.T) {
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
	if managed || route != nil {
		t.Fatalf("sole listener was routed: managed=%v route=%#v", managed, route)
	}
	if listener.ListenAddr != "0.0.0.0" || listener.BackendPort != 443 || listener.Port != 443 {
		t.Fatalf("sole listener did not keep the public socket: %#v", listener)
	}
	// With nothing to route, Nginx has no managed configuration at all — the
	// listener owns the port, so a stream block would only fight it for the
	// socket.
	configuration, _, err := store.CompileNodeNginx(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(configuration) != "" {
		t.Fatalf("compiled Nginx configuration should be empty:\n%s", configuration)
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
