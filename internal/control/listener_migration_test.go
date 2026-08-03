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
