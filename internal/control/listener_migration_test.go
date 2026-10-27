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
		Spec: ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "httpupgrade", Path: "/invalid"}},
	}); err == nil {
		t.Fatal("accepted a VLESS httpupgrade listener on TCP/443 without origin TLS")
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
	if len(listeners) != 1 || !listeners[0].Spec.TLS.Enabled || len(listeners[0].Spec.TLS.ALPN) != 1 || listeners[0].Spec.TLS.ALPN[0] != "http/1.1" || listeners[0].Spec.Transport.Type != "httpupgrade" {
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
	listenerID := strings.Repeat("e", 32)
	spec, err := json.Marshal(ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO listeners (id,node_id,name,protocol,network,connection_domain,listen_address,port,backend_port,enabled,spec,outbound_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		listenerID, nodeID, "hysteria2", "hysteria2", "udp", "", "0.0.0.0", 443, 443, 1, string(spec), "", nowUnix(), nowUnix()); err != nil {
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
		t.Fatalf("Hysteria2 listener was not migrated: %#v", listeners)
	}
}

// TestReconcileListenerPortRoutingCleansUpCentralizedRouting simulates an
// upgrade from a version that resolved TCP/443 cohabitation inside the control
// plane: it seeds a database with two shared-port listeners on internal loopback
// ports and their ingress routes, lets Store.Open run the reconcile, and asserts
// that every listener now binds 0.0.0.0 on its own port and the ingress table
// is empty.
func TestReconcileListenerPortRoutingCleansUpCentralizedRouting(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := strings.Repeat("a", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "routed-node", []byte(strings.Repeat("k", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	sole := strings.Repeat("1", 32)
	sharedFirst := strings.Repeat("2", 32)
	sharedSecond := strings.Repeat("3", 32)
	unroutable := strings.Repeat("4", 32)
	stranded := strings.Repeat("5", 32)
	insert := func(id, name, domain, listenAddr string, port, backendPort uint16, spec ProtocolSpec) {
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
	tlsHTTPUpgrade := func(path string) ProtocolSpec {
		return ProtocolSpec{
			Protocol: "vless", Network: "tcp",
			TLS:       TLSOptions{Enabled: true, ALPN: []string{"h2", "http/1.1"}},
			Transport: TransportOptions{Type: "httpupgrade", Path: path},
		}
	}
	insert(sole, "唯一接入", "sole.example.com", "0.0.0.0", 443, 443, tlsHTTPUpgrade("/sole"))
	// No TLS means no ClientHello to route by, so this one has to be left alone.
	insert(unroutable, "明文接入", "plain.example.com", "0.0.0.0", 8080, 8080, ProtocolSpec{
		Protocol: "vless", Network: "tcp", Transport: TransportOptions{Type: "httpupgrade", Path: "/plain"},
	})
	insert(sharedFirst, "共用甲", "first.example.com", "0.0.0.0", 8443, 8443, tlsHTTPUpgrade("/first"))
	insert(sharedSecond, "共用乙", "second.example.com", "0.0.0.0", 8443, 8443, tlsHTTPUpgrade("/second"))
	// Routed under the old rule despite owning its port outright.
	insert(stranded, "历史遗留", "stranded.example.com", "127.0.0.1", 9443, 20000, tlsHTTPUpgrade("/stranded"))
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
	// A shared port is no longer resolved here. The node works out that two
	// services want one socket and puts them behind its own router; the control
	// plane records only the public port each was asked for.
	if first.ListenAddr != "0.0.0.0" || first.BackendPort != 8443 || second.ListenAddr != "0.0.0.0" || second.BackendPort != 8443 {
		t.Fatalf("services on a shared port were still placed centrally: %#v / %#v", first, second)
	}
	if got := byID[stranded]; got.ListenAddr != "0.0.0.0" || got.BackendPort != 9443 {
		t.Fatalf("listener alone on its port was not taken back off loopback: %#v", got)
	}
	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("automatic routes survived the migration: %#v", routes)
	}
	// The names the node needs to tell the two apart still come from here.
	names, err := store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if names["listener-"+sharedFirst] != "first.example.com" || names["listener-"+sharedSecond] != "second.example.com" {
		t.Fatalf("routing names for the shared port = %#v", names)
	}
}
