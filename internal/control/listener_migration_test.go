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

// Startup puts every TCP listener on the side of the router its port calls
// for: contended ports get SNI routing, ports with a single listener are bound
// directly so sing-box keeps seeing client addresses. A node routed under the
// old unconditional rule has to be taken back out, or it stays on loopback
// forever and every connection through it reports 127.0.0.1.
func TestStartupReconcilesListenerPortRouting(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := strings.Repeat("e", 32)
	sole := strings.Repeat("f", 32)
	unroutable := strings.Repeat("a", 32)
	sharedFirst := strings.Repeat("b", 32)
	sharedSecond := strings.Repeat("c", 32)
	stranded := strings.Repeat("1", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "direct-node", []byte(strings.Repeat("d", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	insert := func(id, name, domain, listenAddr string, port, backendPort uint16, spec ProtocolSpec) {
		t.Helper()
		encoded, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO listeners (id,node_id,name,protocol,network,connection_domain,listen_address,port,backend_port,enabled,spec,outbound_id,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, nodeID, name, spec.Protocol, spec.Network, domain, listenAddr, port, backendPort, 1, string(encoded), "", nowUnix(), nowUnix()); err != nil {
			t.Fatal(err)
		}
	}
	tlsWebSocket := func(path string) ProtocolSpec {
		return ProtocolSpec{
			Protocol: "vless", Network: "tcp",
			TLS:       TLSOptions{Enabled: true, ALPN: []string{"http/1.1"}},
			Transport: TransportOptions{Type: "ws", Path: path},
		}
	}
	insert(sole, "唯一接入", "sole.example.com", "0.0.0.0", 443, 443, tlsWebSocket("/sole"))
	// No TLS means no ClientHello to route by, so this one has to be left alone.
	insert(unroutable, "明文接入", "plain.example.com", "0.0.0.0", 8080, 8080, ProtocolSpec{
		Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "ws", Path: "/plain"},
	})
	insert(sharedFirst, "共用甲", "first.example.com", "0.0.0.0", 8443, 8443, tlsWebSocket("/first"))
	insert(sharedSecond, "共用乙", "second.example.com", "0.0.0.0", 8443, 8443, tlsWebSocket("/second"))
	// Routed under the old rule despite owning its port outright.
	insert(stranded, "历史遗留", "stranded.example.com", "127.0.0.1", 9443, 20000, tlsWebSocket("/stranded"))
	if _, err := store.db.Exec(`INSERT INTO ingress_routes (id, node_id, listener_id, listen_address, port, sni, enabled, created_at, updated_at)
		VALUES (?,?,?,?,?,?,1,?,?)`,
		strings.Repeat("2", 32), nodeID, stranded, "0.0.0.0", 9443, "stranded.example.com", nowUnix(), nowUnix()); err != nil {
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
	byID := map[string]Listener{}
	for _, listener := range listeners {
		byID[listener.ID] = listener
	}
	if got := byID[sole]; got.ListenAddr != "0.0.0.0" || got.BackendPort != 443 {
		t.Fatalf("sole listener was routed: %#v", got)
	}
	if got := byID[unroutable]; got.ListenAddr != "0.0.0.0" || got.BackendPort != 8080 {
		t.Fatalf("listener without a usable SNI was moved: %#v", got)
	}
	first, second := byID[sharedFirst], byID[sharedSecond]
	if first.ListenAddr != "127.0.0.1" || second.ListenAddr != "127.0.0.1" || first.BackendPort == second.BackendPort {
		t.Fatalf("contended port was not routed: %#v / %#v", first, second)
	}
	if got := byID[stranded]; got.ListenAddr != "0.0.0.0" || got.BackendPort != 9443 {
		t.Fatalf("listener alone on its port was not taken back off loopback: %#v", got)
	}
	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	routeByListener := map[string]IngressRoute{}
	for _, route := range routes {
		routeByListener[route.ListenerID] = route
	}
	if len(routes) != 2 {
		t.Fatalf("routes = %#v, want one per listener on the contended port", routes)
	}
	for id, listener := range map[string]Listener{sharedFirst: first, sharedSecond: second} {
		route, ok := routeByListener[id]
		if !ok || route.Port != 8443 || route.BackendPort != listener.BackendPort {
			t.Fatalf("route for %s does not front its listener: %#v", listener.Name, route)
		}
	}
}
