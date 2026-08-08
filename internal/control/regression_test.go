package control

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newTestNode(t *testing.T, store *Store, name, key string) string {
	t.Helper()
	operator, _, err := store.EnsureDefaultAdmin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateRegistrationToken(t.Context(), operator.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := store.RegisterAgent(t.Context(), RegistrationInput{
		Token: token.Token, NodeName: name, PublicKey: []byte(strings.Repeat(key, 32)[:32]), Capabilities: `{}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.ApproveRegistration(t.Context(), registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	return approved.NodeID
}

func tlsListener(nodeID, name, domain string, port uint16, enabled bool) Listener {
	return Listener{
		NodeID: nodeID, Name: name, Domain: domain, ListenAddr: "0.0.0.0", Port: port, Enabled: enabled,
		Spec: ProtocolSpec{
			Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "ws", Path: "/ws"},
		},
	}
}

// A node that never converges used to get a brand new task on every single
// heartbeat, because a terminal task is deliberately not reused. That filled
// the task table, flooded the live event stream and hammered every open
// browser — the storm behind both the Nginx churn and the sluggish console.
func TestReconcileBacksOffInsteadOfQueueingATaskPerHeartbeat(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "backoff-node", "b")
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 5; round++ {
		server.reconcileNodeDesiredState(t.Context(), nodeID, "1.13.0", "stale", "stale")
		tasks, _, err := store.ListTasks(t.Context(), nodeID, "", 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		// Fail every outstanding task, exactly as a node stuck in a broken
		// state would report back.
		for _, task := range tasks {
			if task.Status == "queued" || task.Status == "dispatched" {
				if err := store.CompleteTask(t.Context(), task.ID, nodeID, "failed", "still broken"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	tasks, total, err := store.ListTasks(t.Context(), nodeID, "", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total > 2 {
		t.Fatalf("five failed heartbeats queued %d tasks, want at most one per configuration kind: %#v", total, tasks)
	}

	// Counting rows cannot show the wait itself: a desired state that is still
	// queued is reused rather than queued twice. The wait is the thing under
	// test, so it is asked about directly.
	_, desiredHash, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if server.shouldReconcile(nodeID, "singbox", desiredHash) {
		t.Fatal("a node that keeps reporting the same drift was retried without waiting")
	}
	// An operator action must still take effect immediately, however long the
	// node has been failing to converge.
	server.clearReconcileState(nodeID)
	if !server.shouldReconcile(nodeID, "singbox", desiredHash) {
		t.Fatal("an operator action did not clear the backoff")
	}
}

// Services asked to share a public port are recorded on that port, and the
// control plane supplies the name each is reached by. Deciding which of them
// binds the socket and which sits behind the router belongs to the node, which
// is the only side that can see what else is already there.
func TestSharedPortServicesKeepTheirPublicPortAndCarryDistinctNames(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "port-node", "p")

	first, _, managed, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "第一个", "a.example.com", 443, true))
	if err != nil || managed {
		t.Fatalf("first service on 443 = managed:%v err:%v", managed, err)
	}
	second, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "第二个", "b.example.com", 443, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range []Listener{first, second} {
		if listener.ListenAddr != "0.0.0.0" || listener.BackendPort != 443 {
			t.Fatalf("service on the shared port was placed centrally: %#v", listener)
		}
	}
	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil || len(routes) != 0 {
		t.Fatalf("automatic routes were created centrally: %#v err:%v", routes, err)
	}
	names, err := store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if names["listener-"+first.ID] != "a.example.com" || names["listener-"+second.ID] != "b.example.com" {
		t.Fatalf("routing names = %#v", names)
	}

	// Two services on one port that no name can tell apart could not deploy on
	// any host, and that is something the control plane can see for itself.
	if _, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "重名", "a.example.com", 443, true)); err == nil {
		t.Fatal("a second service with the same name on one port was accepted")
	}
}

// UDP shares only the port number with the TCP router, never the socket.
func TestUDPListenerMayReusePortHeldByTheTCPRouter(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "udp-node", "u")
	for _, host := range []string{"first", "second"} {
		if _, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "TCP "+host, host+".example.com", 443, true)); err != nil {
			t.Fatal(err)
		}
	}
	udp := Listener{
		NodeID: nodeID, Name: "udp-443", Domain: "udp.example.com", ListenAddr: "0.0.0.0", Port: 443, Enabled: true,
		Spec: ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true}},
	}
	created, _, managed, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), udp)
	if err != nil {
		t.Fatalf("UDP listener on the same port number was rejected: %v", err)
	}
	if managed || created.Port != 443 || created.BackendPort != 443 {
		t.Fatalf("UDP listener was routed through the TCP router: managed=%v port=%d backend=%d", managed, created.Port, created.BackendPort)
	}
}

// A disabled service's name must stop being published, otherwise the node
// would keep routing that hostname to a backend sing-box no longer binds.
func TestDisabledListenerLeavesThePublishedRoutingNames(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "nginx-node", "n")
	if _, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "保留", "keep.example.com", 443, true)); err != nil {
		t.Fatal(err)
	}
	dropped, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "停用", "drop.example.com", 443, true))
	if err != nil {
		t.Fatal(err)
	}

	names, err := store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if names["listener-"+dropped.ID] != "drop.example.com" {
		t.Fatalf("an enabled service is missing from the published names: %#v", names)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE listeners SET enabled = 0 WHERE id = ?`, dropped.ID); err != nil {
		t.Fatal(err)
	}
	names, err = store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := names["listener-"+dropped.ID]; present {
		t.Fatalf("a disabled service is still published: %#v", names)
	}
	if len(names) != 1 {
		t.Fatalf("the remaining service was dropped too: %#v", names)
	}
}

// An agent knows the address it reaches the master from, so a freshly
// approved node is usable without anyone typing one in. An address an
// operator did choose must never be overwritten by it.
func TestObservedAddressFillsOnlyAnEmptyClientAddress(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "address-node", "a")

	if err := store.SetNodeObservedAddress(t.Context(), nodeID, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.ClientAddress != "203.0.113.10" {
		t.Fatalf("client address = %q, want the address the agent connected from", node.ClientAddress)
	}

	if err := store.UpdateNode(t.Context(), nodeID, "address-node", "proxy.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeObservedAddress(t.Context(), nodeID, "203.0.113.99"); err != nil {
		t.Fatal(err)
	}
	node, err = store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.ClientAddress != "proxy.example.com" {
		t.Fatalf("client address = %q, want the operator's choice preserved", node.ClientAddress)
	}
}

// Name and address describe the same server, so they are saved together.
func TestUpdateNodeSavesNameAndAddressTogether(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "combined-node", "c")
	if err := store.UpdateNode(t.Context(), nodeID, "香港 01", "hk.example.com"); err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Name != "香港 01" || node.ClientAddress != "hk.example.com" {
		t.Fatalf("node = %+v, want both fields updated in one call", node)
	}
	if err := store.UpdateNode(t.Context(), nodeID, "", "hk.example.com"); err == nil {
		t.Fatal("an empty name was accepted")
	}
}

// The console shows how many users a service has; serving that count with the
// list removes one request per listener on every refresh.
func TestListListenersReportsEndpointCount(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "count-node", "c")
	listener, err := store.CreateListener(t.Context(), tlsListener(nodeID, "服务", "count.example.com", 8443, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		if _, err := store.CreateEndpoint(t.Context(), Endpoint{ListenerID: listener.ID, Name: name, Enabled: true},
			EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}); err != nil {
			t.Fatal(err)
		}
	}
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].EndpointCount != 2 {
		t.Fatalf("listener endpoint count = %#v, want 2", listeners)
	}
}

// Compiled configurations must point sing-box at a log file: a Fail2Ban jail
// whose logpath does not exist refuses to start.
func TestCompiledConfigurationWritesTheFail2BanLogPath(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "log-node", "l")
	if _, err := store.CreateListener(t.Context(), tlsListener(nodeID, "服务", "log.example.com", 8443, true)); err != nil {
		t.Fatal(err)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, singBoxLogPath) {
		t.Fatalf("compiled configuration does not log to %s:\n%s", singBoxLogPath, configuration)
	}
}

// The DNS list is only useful if it says which server and service a record
// serves. Nobody should have to declare that by hand: the access service's own
// connection domain already says it, and a bare address still names its server.
func TestCloudflareRecordsBindThemselvesToAccessServices(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "dns-node", "d")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateListener(t.Context(), tlsListener(nodeID, "网页接入", "dns.example.com", 8443, true)); err != nil {
		t.Fatal(err)
	}
	records, err := store.annotateCloudflareRecords(t.Context(), []CloudflareRecord{
		{ID: "r1", Type: "A", Name: "dns.example.com", Content: "203.0.113.10", TTL: 1},
		{ID: "r2", Type: "A", Name: "other.example.com", Content: "203.0.113.10", TTL: 300},
		{ID: "r3", Type: "TXT", Name: "spf.example.com", Content: "v=spf1 -all", TTL: 300},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records[0].Bindings) != 1 || records[0].Bindings[0].NodeName != "dns-node" ||
		records[0].Bindings[0].ListenerName != "网页接入" || records[0].Bindings[0].ListenerPort != 8443 {
		t.Fatalf("record on an access service domain = %+v, want the node and listener named", records[0].Bindings)
	}
	// No access service claims this name, but the address still identifies the
	// server the record points at.
	if len(records[1].Bindings) != 1 || records[1].Bindings[0].NodeName != "dns-node" || records[1].Bindings[0].ListenerName != "" {
		t.Fatalf("record pointing at a server = %+v, want only the node named", records[1].Bindings)
	}
	if len(records[2].Bindings) != 0 {
		t.Fatalf("unrelated record = %+v, want no binding invented", records[2].Bindings)
	}
}

// Editing a listener onto another port must carry the whole chain with it:
// the compiled sing-box inbound, the Nginx SNI group it leaves, and the
// subscription clients download.
func TestPortEditKeepsCompiledConfigAndSubscriptionInSync(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := newTestNode(t, store, "port-edit-node", "p")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "服务 A", "a.example.com", 443, true)); err != nil {
		t.Fatal(err)
	}
	second, _, _, err := store.CreateListenerWithAutomaticPortRouting(t.Context(), tlsListener(nodeID, "服务 B", "b.example.com", 443, true))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), Endpoint{ListenerID: second.ID, Name: "user", Enabled: true},
		EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.CreateSubscription(t.Context(), SubscriptionInput{Kind: ClientSubscription, Name: "订阅", EndpointIDs: []string{endpoint.ID}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	subscription := func() string {
		t.Helper()
		encoded, err := store.GenerateClientSubscription(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		return string(decoded)
	}
	if before := subscription(); !strings.Contains(before, "b.example.com:443") {
		t.Fatalf("subscription before the edit = %q", before)
	}
	moved := second
	moved.Port = 8443
	moved, _, err = store.RelocateListenerPort(t.Context(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Port != 8443 || moved.BackendPort != 8443 || moved.ListenAddr != "0.0.0.0" {
		t.Fatalf("listener after the port edit = %#v", moved)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var compiled struct {
		Inbounds []struct {
			Listen     string `json:"listen"`
			ListenPort uint16 `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configuration), &compiled); err != nil {
		t.Fatal(err)
	}
	listenByPort := map[uint16]string{}
	for _, inbound := range compiled.Inbounds {
		listenByPort[inbound.ListenPort] = inbound.Listen
	}
	if listenByPort[8443] != "0.0.0.0" {
		t.Fatalf("compiled inbounds = %#v, want 8443 bound on 0.0.0.0", compiled.Inbounds)
	}
	// Both services keep publishing their names; which of them the node ends up
	// routing follows from the ports, and 8443 is now free of company.
	names, err := store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if names["listener-"+moved.ID] != "b.example.com" || len(names) != 2 {
		t.Fatalf("published routing names after the edit = %#v", names)
	}
	if after := subscription(); !strings.Contains(after, "b.example.com:8443") {
		t.Fatalf("subscription after the edit = %q", after)
	}
}
