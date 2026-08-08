package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMigrationEnablesTLSForLegacyVLESSStreamListener(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := strings.Repeat("b", 32)
	listenerID := strings.Repeat("c", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "legacy-node", []byte(strings.Repeat("l", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "invalid-vless", ListenAddr: "127.0.0.1", Port: 443, Enabled: true,
		Spec: ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "ws", Path: "/invalid"}},
	}); err == nil {
		t.Fatal("accepted a VLESS WebSocket listener on TCP/443 without origin TLS")
	}
	spec, err := json.Marshal(ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "ws", Path: "/legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO listeners (id,node_id,name,protocol,network,connection_domain,listen_address,port,backend_port,enabled,spec,outbound_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		listenerID, nodeID, "legacy-vless", "vless", "tcp", "legacy.example.com", "127.0.0.1", 443, 20000, 1, string(spec), "", nowUnix(), nowUnix()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || !listeners[0].Spec.TLS.Enabled || len(listeners[0].Spec.TLS.ALPN) != 1 || listeners[0].Spec.TLS.ALPN[0] != "http/1.1" {
		t.Fatalf("legacy VLESS listener was not migrated: %#v", listeners)
	}
}

func TestMigrationFillsHysteria2ALPN(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := strings.Repeat("d", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "hysteria2-node", []byte(strings.Repeat("h", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "hysteria2-legacy", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || len(listeners[0].Spec.TLS.ALPN) != 1 || listeners[0].Spec.TLS.ALPN[0] != "h3" {
		t.Fatalf("Hysteria2 listener ALPN was not migrated: %#v", listeners)
	}
}

// A node set up before routing became unconditional keeps sing-box on the
// public port, which is exactly the state that loses the socket to an Nginx
// already installed on the host.
func TestMigrationMovesDirectlyBoundTCPListenerBehindRouter(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := strings.Repeat("e", 32)
	routed := strings.Repeat("f", 32)
	unroutable := strings.Repeat("a", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "direct-node", []byte(strings.Repeat("d", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	insert := func(id, name, domain string, port uint16, spec ProtocolSpec) {
		t.Helper()
		encoded, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO listeners (id,node_id,name,protocol,network,connection_domain,listen_address,port,backend_port,enabled,spec,outbound_id,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, nodeID, name, spec.Protocol, spec.Network, domain, "0.0.0.0", port, port, 1, string(encoded), "", nowUnix(), nowUnix()); err != nil {
			t.Fatal(err)
		}
	}
	insert(routed, "唯一接入", "sole.example.com", 443, ProtocolSpec{
		Protocol: "vless", Network: "tcp",
		TLS:       TLSOptions{Enabled: true, ALPN: []string{"http/1.1"}},
		Transport: TransportOptions{Type: "ws", Path: "/sole"},
	})
	// No TLS means no ClientHello to route by, so this one has to be left alone.
	insert(unroutable, "明文接入", "plain.example.com", 8080, ProtocolSpec{
		Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "ws", Path: "/plain"},
	})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Listener{}
	for _, listener := range listeners {
		byID[listener.ID] = listener
	}
	if got := byID[routed]; got.ListenAddr != "127.0.0.1" || got.Port != 443 || got.BackendPort == 443 {
		t.Fatalf("TLS listener was not moved behind the router: %#v", got)
	}
	if got := byID[unroutable]; got.ListenAddr != "0.0.0.0" || got.BackendPort != 8080 {
		t.Fatalf("listener without a usable SNI was moved: %#v", got)
	}
	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].ListenerID != routed || routes[0].Port != 443 || routes[0].SNI != "sole.example.com" {
		t.Fatalf("migrated routes = %#v", routes)
	}
	if routes[0].BackendPort != byID[routed].BackendPort {
		t.Fatalf("route backend %d does not match listener %#v", routes[0].BackendPort, byID[routed])
	}
}
