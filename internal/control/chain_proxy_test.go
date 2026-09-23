package control

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
)

func createTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createTestNode(t *testing.T, store *Store, name, clientAddress string) string {
	t.Helper()
	nodeID, err := newID()
	if err != nil {
		t.Fatalf("newID: %v", err)
	}
	pubKey := sha256.Sum256([]byte(name))
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, client_address, revoked_at, created_at) VALUES (?, ?, ?, ?, NULL, ?)`,
		nodeID, name, pubKey[:], clientAddress, nowUnix()); err != nil {
		t.Fatalf("insert node %s: %v", name, err)
	}
	return nodeID
}

func TestChainProxyCompileVLESSReality(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)

	node1ID := createTestNode(t, store, "node1", "198.51.100.1")
	node2ID := createTestNode(t, store, "node2", "203.0.113.2")

	key, _, err := store.CreateRealityKey(ctx, "reality-key")
	if err != nil {
		t.Fatalf("create reality key: %v", err)
	}

	listener2, err := store.CreateListener(ctx, Listener{
		NodeID:     node2ID,
		Name:       "node2-reality",
		Domain:     "hk.example.com",
		Port:       443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "vless",
			Network:  "tcp",
			TLS:      TLSOptions{Enabled: true},
			Reality: RealityOptions{
				Enabled:         true,
				HandshakeServer: "www.apple.com",
				HandshakePort:   443,
				KeyID:           key.ID,
				ShortIDs:        []string{"0123456789abcdef"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create listener on node2: %v", err)
	}

	user2, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener2.ID,
		Name:       "user2",
		Alias:      "香港落地 01",
		Enabled:    true,
	}, EndpointCredentials{
		UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Flow: "xtls-rprx-vision",
	})
	if err != nil {
		t.Fatalf("create endpoint on node2: %v", err)
	}

	listener1, err := store.CreateListener(ctx, Listener{
		NodeID:     node1ID,
		Name:       "node1-inbound",
		Domain:     "in.example.com",
		Port:       8443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/vless-ws"},
		},
	})
	if err != nil {
		t.Fatalf("create listener on node1: %v", err)
	}

	user1, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener1.ID,
		Name:       "user1",
		Alias:      "中转入口 01",
		Enabled:    true,
		OutboundID: "chain:" + user2.ID,
	}, EndpointCredentials{
		UUID: "11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatalf("create chained endpoint on node1: %v", err)
	}

	configJSON, _, err := store.CompileNodeConfig(ctx, node1ID)
	if err != nil {
		t.Fatalf("compile config for node1: %v", err)
	}

	var parsed struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("unmarshal compiled config: %v", err)
	}

	var chainOutbound map[string]any
	expectedTag := "chain-" + user2.ID
	for _, ob := range parsed.Outbounds {
		if ob["tag"] == expectedTag {
			chainOutbound = ob
			break
		}
	}
	if chainOutbound == nil {
		t.Fatalf("outbounds does not contain expected chain outbound with tag %q", expectedTag)
	}

	if chainOutbound["type"] != "vless" {
		t.Errorf("expected type vless, got %v", chainOutbound["type"])
	}
	if chainOutbound["server"] != "hk.example.com" {
		t.Errorf("expected server hk.example.com, got %v", chainOutbound["server"])
	}
	if chainOutbound["server_port"] != float64(443) {
		t.Errorf("expected server_port 443, got %v", chainOutbound["server_port"])
	}
	if chainOutbound["uuid"] != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("expected uuid, got %v", chainOutbound["uuid"])
	}
	if chainOutbound["flow"] != "xtls-rprx-vision" {
		t.Errorf("expected flow, got %v", chainOutbound["flow"])
	}

	tls, ok := chainOutbound["tls"].(map[string]any)
	if !ok || tls["enabled"] != true {
		t.Fatalf("expected tls enabled in chain outbound: %#v", chainOutbound)
	}
	if tls["server_name"] != "www.apple.com" {
		t.Errorf("expected server_name www.apple.com, got %v", tls["server_name"])
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok || reality["enabled"] != true {
		t.Fatalf("expected reality enabled: %#v", tls)
	}
	if reality["public_key"] != key.PublicKey {
		t.Errorf("expected public_key %s, got %v", key.PublicKey, reality["public_key"])
	}
	if reality["short_id"] != "0123456789abcdef" {
		t.Errorf("expected short_id 0123456789abcdef, got %v", reality["short_id"])
	}

	var foundRule bool
	for _, rule := range parsed.Route.Rules {
		inbounds, _ := rule["inbound"].([]any)
		authUsers, _ := rule["auth_user"].([]any)
		if len(inbounds) > 0 && inbounds[0] == "listener-"+listener1.ID && len(authUsers) > 0 && authUsers[0] == user1.Name {
			if rule["outbound"] != expectedTag {
				t.Errorf("expected rule outbound %q, got %v", expectedTag, rule["outbound"])
			}
			foundRule = true
			break
		}
	}
	if !foundRule {
		t.Fatalf("route rule for user1 not found in compiled config")
	}
}

func TestChainProxyCompileHysteria2(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)

	node1ID := createTestNode(t, store, "node1", "198.51.100.1")
	node2ID := createTestNode(t, store, "node2", "203.0.113.2")

	listener2, err := store.CreateListener(ctx, Listener{
		NodeID:     node2ID,
		Name:       "node2-hy2",
		Domain:     "hy2.example.com",
		Port:       443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol: "hysteria2",
			Network:  "udp",
			TLS:      TLSOptions{Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("create hysteria2 listener on node2: %v", err)
	}

	user2, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener2.ID,
		Name:       "hy2user",
		Alias:      "日本 Hy2",
		Enabled:    true,
	}, EndpointCredentials{
		Password: "secret-hy2-password",
	})
	if err != nil {
		t.Fatalf("create endpoint on node2: %v", err)
	}

	listener1, err := store.CreateListener(ctx, Listener{
		NodeID:     node1ID,
		Name:       "node1-inbound",
		Port:       8443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/node1-ws"},
		},
	})
	if err != nil {
		t.Fatalf("create listener on node1: %v", err)
	}

	user1, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener1.ID,
		Name:       "user1",
		Enabled:    true,
		OutboundID: "chain:" + user2.ID,
	}, EndpointCredentials{
		UUID: "11111111-2222-3333-4444-555555555555",
	})
	if err != nil {
		t.Fatalf("create user1: %v", err)
	}

	configJSON, _, err := store.CompileNodeConfig(ctx, node1ID)
	if err != nil {
		t.Fatalf("compile config for node1: %v", err)
	}

	var parsed struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	expectedTag := "chain-" + user2.ID
	var chainOutbound map[string]any
	for _, ob := range parsed.Outbounds {
		if ob["tag"] == expectedTag {
			chainOutbound = ob
			break
		}
	}
	if chainOutbound == nil {
		t.Fatalf("outbounds does not contain hysteria2 chain outbound %q", expectedTag)
	}

	if chainOutbound["type"] != "hysteria2" {
		t.Errorf("expected hysteria2, got %v", chainOutbound["type"])
	}
	if chainOutbound["password"] != "secret-hy2-password" {
		t.Errorf("expected password, got %v", chainOutbound["password"])
	}
	tls, _ := chainOutbound["tls"].(map[string]any)
	if tls["insecure"] != true {
		t.Errorf("expected insecure true for hysteria2 tls")
	}
	_ = user1
}

func TestChainProxyValidation(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)

	node1ID := createTestNode(t, store, "node1", "198.51.100.1")
	node2ID := createTestNode(t, store, "node2", "203.0.113.2")

	l1, err := store.CreateListener(ctx, Listener{
		NodeID: node1ID, Name: "l1", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/l1"},
		},
	})
	if err != nil {
		t.Fatalf("create l1: %v", err)
	}
	u1, err := store.CreateEndpoint(ctx, Endpoint{ListenerID: l1.ID, Name: "u1", Enabled: true}, EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("create u1: %v", err)
	}

	// 1. Same-node rejection: creating u2 on node1 targeting u1 on node1
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: l1.ID,
		Name:       "u2",
		Enabled:    true,
		OutboundID: "chain:" + u1.ID,
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err == nil || !strings.Contains(err.Error(), "只能指定其他服务器上的节点用户") {
		t.Fatalf("expected same-server rejection, got: %v", err)
	}

	// 2. Non-existent target
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: l1.ID,
		Name:       "u2",
		Enabled:    true,
		OutboundID: "chain:non-existent-id",
	}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err == nil || !strings.Contains(err.Error(), "指定的链式代理节点用户不存在") {
		t.Fatalf("expected non-existent rejection, got: %v", err)
	}

	// 3. Self-chaining on update
	_, err = store.UpdateEndpoint(ctx, Endpoint{
		ID:         u1.ID,
		ListenerID: l1.ID,
		Name:       "u1",
		Enabled:    true,
		OutboundID: "chain:" + u1.ID,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "不能将链式代理指向用户自身") {
		t.Fatalf("expected self-chaining rejection, got: %v", err)
	}

	// 4. Cycle detection: u1 (node1) -> u_b (node2) -> u1 (node1)
	l2, err := store.CreateListener(ctx, Listener{
		NodeID: node2ID, Name: "l2", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/l2"},
		},
	})
	if err != nil {
		t.Fatalf("create l2: %v", err)
	}

	ub, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: l2.ID,
		Name:       "ub",
		Enabled:    true,
		OutboundID: "chain:" + u1.ID,
	}, EndpointCredentials{UUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"})
	if err != nil {
		t.Fatalf("create ub on node2 chaining to u1: %v", err)
	}

	// Now try to update u1 to chain to ub (forming a cycle u1 -> ub -> u1)
	_, err = store.UpdateEndpoint(ctx, Endpoint{
		ID:         u1.ID,
		ListenerID: l1.ID,
		Name:       "u1",
		Enabled:    true,
		OutboundID: "chain:" + ub.ID,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "循环引用") {
		t.Fatalf("expected circular cycle rejection, got: %v", err)
	}
}

func TestChainProxyCascadeDeletion(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)

	node1ID := createTestNode(t, store, "node1", "198.51.100.1")
	node2ID := createTestNode(t, store, "node2", "203.0.113.2")

	l2, err := store.CreateListener(ctx, Listener{
		NodeID: node2ID, Name: "l2", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/l2"},
		},
	})
	if err != nil {
		t.Fatalf("create l2: %v", err)
	}
	u2, err := store.CreateEndpoint(ctx, Endpoint{ListenerID: l2.ID, Name: "u2", Enabled: true}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create u2: %v", err)
	}

	l1, err := store.CreateListener(ctx, Listener{
		NodeID: node1ID, Name: "l1", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/l1"},
		},
	})
	if err != nil {
		t.Fatalf("create l1: %v", err)
	}
	u1, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: l1.ID,
		Name:       "u1",
		Enabled:    true,
		OutboundID: "chain:" + u2.ID,
	}, EndpointCredentials{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("create u1: %v", err)
	}

	// Verify ChainedEndpointNodeIDs finds node1
	nodes, err := store.ChainedEndpointNodeIDs(ctx, u2.ID)
	if err != nil {
		t.Fatalf("ChainedEndpointNodeIDs: %v", err)
	}
	if len(nodes) != 1 || nodes[0] != node1ID {
		t.Fatalf("expected node1, got %#v", nodes)
	}

	// Delete target u2
	if err := store.DeleteEndpoint(ctx, u2.ID); err != nil {
		t.Fatalf("delete u2: %v", err)
	}

	// u1 should automatically fallback to 'direct'
	endpoints, err := store.ListEndpoints(ctx, l1.ID)
	if err != nil {
		t.Fatalf("list endpoints: %v", err)
	}
	if len(endpoints) != 1 || endpoints[0].OutboundID != "direct" {
		t.Fatalf("expected u1 outbound_id to be direct, got %q", endpoints[0].OutboundID)
	}
	_ = u1
}

func TestChainProxyCompileVLESSWebSocketAndGRPC(t *testing.T) {
	ctx := t.Context()
	store := createTestStore(t)

	node1ID := createTestNode(t, store, "node1", "198.51.100.1")
	node2ID := createTestNode(t, store, "node2", "203.0.113.2")
	node3ID := createTestNode(t, store, "node3", "192.0.2.3")

	// Target 1 on Node 2: VLESS WS
	lWS, err := store.CreateListener(ctx, Listener{
		NodeID: node2ID, Name: "node2-ws", Port: 8443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true, ALPN: []string{"http/1.1"}},
			Transport: TransportOptions{Type: "ws", Path: "/my-ws", Host: "ws.example.com"},
		},
	})
	if err != nil {
		t.Fatalf("create lWS: %v", err)
	}
	uWS, err := store.CreateEndpoint(ctx, Endpoint{ListenerID: lWS.ID, Name: "uWS", Enabled: true}, EndpointCredentials{UUID: "22222222-2222-2222-2222-222222222222"})
	if err != nil {
		t.Fatalf("create uWS: %v", err)
	}

	// Target 2 on Node 3: VLESS gRPC
	lGRPC, err := store.CreateListener(ctx, Listener{
		NodeID: node3ID, Name: "node3-grpc", Port: 9443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true, ALPN: []string{"h2"}},
			Transport: TransportOptions{Type: "grpc", ServiceName: "my-grpc-service"},
		},
	})
	if err != nil {
		t.Fatalf("create lGRPC: %v", err)
	}
	uGRPC, err := store.CreateEndpoint(ctx, Endpoint{ListenerID: lGRPC.ID, Name: "uGRPC", Enabled: true}, EndpointCredentials{UUID: "33333333-3333-3333-3333-333333333333"})
	if err != nil {
		t.Fatalf("create uGRPC: %v", err)
	}

	// Inbound on Node 1 with two users chaining to uWS and uGRPC
	l1, err := store.CreateListener(ctx, Listener{
		NodeID: node1ID, Name: "node1-in", Port: 443, ListenAddr: "0.0.0.0", Enabled: true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/in"},
		},
	})
	if err != nil {
		t.Fatalf("create l1: %v", err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{ListenerID: l1.ID, Name: "clientWS", Enabled: true, OutboundID: "chain:" + uWS.ID}, EndpointCredentials{UUID: "aaaaaaaa-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("create clientWS: %v", err)
	}
	_, err = store.CreateEndpoint(ctx, Endpoint{ListenerID: l1.ID, Name: "clientGRPC", Enabled: true, OutboundID: "chain:" + uGRPC.ID}, EndpointCredentials{UUID: "bbbbbbbb-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatalf("create clientGRPC: %v", err)
	}

	configJSON, _, err := store.CompileNodeConfig(ctx, node1ID)
	if err != nil {
		t.Fatalf("compile config: %v", err)
	}

	var parsed struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	var foundWS, foundGRPC bool
	for _, ob := range parsed.Outbounds {
		if ob["tag"] == "chain-"+uWS.ID {
			foundWS = true
			trans, _ := ob["transport"].(map[string]any)
			if trans["type"] != "ws" || trans["path"] != "/my-ws" {
				t.Errorf("expected ws transport, got %#v", trans)
			}
			headers, _ := trans["headers"].(map[string]any)
			if headers["Host"] != "ws.example.com" {
				t.Errorf("expected Host header ws.example.com, got %#v", headers)
			}
		}
		if ob["tag"] == "chain-"+uGRPC.ID {
			foundGRPC = true
			trans, _ := ob["transport"].(map[string]any)
			if trans["type"] != "grpc" || trans["service_name"] != "my-grpc-service" {
				t.Errorf("expected grpc transport, got %#v", trans)
			}
		}
	}
	if !foundWS || !foundGRPC {
		t.Fatalf("expected both WS and GRPC chain outbounds, foundWS=%v foundGRPC=%v", foundWS, foundGRPC)
	}
}

