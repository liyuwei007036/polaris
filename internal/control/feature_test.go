package control_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
	"github.com/liyuwei007036/polaris/internal/wire"
)

func approveTestNode(t *testing.T, server *control.Server, baseURL, session, csrfToken, nodeName string) string {
	t.Helper()
	agentAddr := startAgentListener(t, server)
	keypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	registrationID := registerAgent(t, agentAddr, server.NoisePublicKey(), keypair, createRegistrationToken(t, baseURL, session, csrfToken), nodeName)
	response := request(t, http.MethodPost, baseURL+"/api/v1/nodes/"+registrationID+"/approve", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approve registration: got %d", response.StatusCode)
	}
	var approved struct {
		NodeID string `json:"node_id"`
	}
	decodeBody(t, response, &approved)
	return approved.NodeID
}

// Network protection reads the servers themselves, so a server that cannot
// answer must say so. Reporting "no rules" for an unreachable server would
// read as "this server has no access limits", which is the opposite of what
// is known.
func TestNetworkProtectionReportsUnreachableServers(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "offline-node")

	for _, path := range []string{"/firewall/rules", "/fail2ban/jails"} {
		response := request(t, http.MethodGet, httpServer.URL+"/api/v1/nodes/"+nodeID+path, nil, session, csrfToken)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("read %s: got %d", path, response.StatusCode)
		}
		var result struct {
			Nodes []struct {
				NodeID    string `json:"node_id"`
				Available bool   `json:"available"`
				Error     string `json:"error"`
			} `json:"nodes"`
		}
		decodeBody(t, response, &result)
		if len(result.Nodes) != 1 || result.Nodes[0].NodeID != nodeID {
			t.Fatalf("unexpected %s answer: %#v", path, result.Nodes)
		}
		if result.Nodes[0].Available || result.Nodes[0].Error == "" {
			t.Fatalf("an offline server was reported as answering for %s: %#v", path, result.Nodes[0])
		}
	}

	// A change to an unreachable server changes nothing, so it must be refused
	// rather than accepted and quietly queued.
	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/firewall/rules", map[string]any{
		"operation": "add", "action": "accept", "protocol": "tcp", "cidr": "8.8.8.8/32", "port": 443,
	}, session, csrfToken)
	if response.StatusCode == http.StatusOK {
		t.Fatal("a firewall change to an offline server was reported as done")
	}
	response.Body.Close()
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/fail2ban/jails", map[string]any{
		"operation": "save", "name": "ssh", "filter_name": "ssh", "log_path": "/var/log/auth.log",
		"fail_regex": "x <HOST>", "max_retry": 5, "find_time_seconds": 600, "ban_time_seconds": 600,
	}, session, csrfToken)
	if response.StatusCode == http.StatusOK {
		t.Fatal("an automatic-banning change to an offline server was reported as done")
	}
	response.Body.Close()
}
func TestInboundMutationCreatesAutomaticApplyTask(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "automatic-apply-node")

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "自动生效入站", "listen_address": "0.0.0.0", "port": 1080,
			"enabled": true, "spec": map[string]any{"protocol": "vless", "network": "tcp", "transport": map[string]any{"type": "ws"}},
		},
		"default_outbound_id": "direct",
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create quick listener: got %d", response.StatusCode)
	}
	if response.Header.Get("X-SB-Auto-Apply-Task") == "" {
		t.Fatal("automatic apply task header missing")
	}
	response.Body.Close()

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/tasks?node_id="+nodeID+"&page=1&page_size=20", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list tasks: got %d", response.StatusCode)
	}
	var tasks struct {
		Tasks []control.Task `json:"tasks"`
	}
	decodeBody(t, response, &tasks)
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].Kind != "singbox.apply_config" {
		t.Fatalf("automatic tasks = %#v", tasks.Tasks)
	}

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "VLESS WebSocket", "port": 18444, "enabled": true,
			"spec": map[string]any{
				"protocol": "vless", "network": "tcp",
				"transport": map[string]any{"type": "ws", "path": ""},
			},
		},
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		var problem map[string]any
		decodeBody(t, response, &problem)
		t.Fatalf("create VLESS WebSocket with default path: got %d, response %#v", response.StatusCode, problem)
	}
	var created struct {
		Endpoints []control.Endpoint `json:"endpoints"`
	}
	decodeBody(t, response, &created)
	if len(created.Endpoints) != 1 || !strings.HasPrefix(created.Endpoints[0].Name, "user_") || len(created.Endpoints[0].Name) != len("user_")+8 {
		t.Fatalf("default generated account = %#v", created.Endpoints)
	}
}

func TestSharedPortInboundCreatesMultipleUsersAndNginxRoutes(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "shared-port-node")
	outbound, err := store.CreateOutbound(t.Context(), control.Outbound{Name: "用户 B 出口", Type: "socks", Server: "127.0.0.1", ServerPort: 1080, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	realityKey, _, err := store.CreateRealityKey(t.Context(), "shared-port-reality")
	if err != nil {
		t.Fatal(err)
	}

	create := func(name, connectionDomain, realitySNI string) struct {
		Listener  control.Listener      `json:"listener"`
		Endpoints []control.Endpoint    `json:"endpoints"`
		Ingress   *control.IngressRoute `json:"ingress_route"`
	} {
		t.Helper()
		response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
			"listener": map[string]any{
				"node_id": nodeID, "name": name, "connection_domain": connectionDomain, "listen_address": "0.0.0.0", "port": 443, "enabled": true,
				"spec": map[string]any{
					"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
					"reality": map[string]any{"enabled": true, "handshake_server": realitySNI, "handshake_port": 443, "key_id": realityKey.ID},
				},
			},
			"accounts": []map[string]any{{"name": "用户 A", "outbound_id": "direct"}, {"name": "用户 B", "outbound_id": outbound.ID}},
		}, session, csrfToken)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create shared listener: got %d", response.StatusCode)
		}
		if response.Header.Get("X-SB-Auto-Apply-Task") == "" {
			t.Fatal("listener automatic task header missing")
		}
		// One task, carrying everything the node needs. There is no separate
		// router task any more: the node derives the router from the services.
		if header := response.Header.Get("X-SB-Auto-Apply-Nginx-Task"); header != "" {
			t.Fatalf("a separate router task was queued: %q", header)
		}
		var created struct {
			Listener  control.Listener      `json:"listener"`
			Endpoints []control.Endpoint    `json:"endpoints"`
			Ingress   *control.IngressRoute `json:"ingress_route"`
		}
		decodeBody(t, response, &created)
		if created.Ingress != nil {
			t.Fatalf("the control plane placed the service itself: %#v", created.Ingress)
		}
		return created
	}

	// Both services are recorded on the public port they asked for. Which of
	// them binds the socket and which sits behind the router is worked out on
	// the node, where what else holds that port is actually visible.
	first := create("Reality A", "a.example.com", "reality-a.example.com")
	second := create("Reality B", "b.example.com", "reality-b.example.com")
	if len(first.Endpoints) != 2 || first.Endpoints[0].OutboundID != "direct" || first.Endpoints[1].OutboundID != outbound.ID {
		t.Fatalf("generated accounts = %#v", first.Endpoints)
	}
	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	listenerByID := map[string]control.Listener{}
	for _, listener := range listeners {
		listenerByID[listener.ID] = listener
	}
	firstListener := listenerByID[first.Listener.ID]
	secondListener := listenerByID[second.Listener.ID]
	for _, listener := range []control.Listener{firstListener, secondListener} {
		if listener.Port != 443 || listener.BackendPort != 443 || listener.ListenAddr != "0.0.0.0" {
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
	if names["listener-"+firstListener.ID] != "reality-a.example.com" || names["listener-"+secondListener.ID] != "reality-b.example.com" {
		t.Fatalf("published routing names = %#v", names)
	}

	// Editing the Reality target changes the name the node routes by.
	editedSecond := secondListener
	editedSecond.Spec.Reality.HandshakeServer = "reality-b-new.example.com"
	editResponse := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+secondListener.ID, editedSecond, session, csrfToken)
	if editResponse.StatusCode != http.StatusOK {
		t.Fatalf("edit service sharing a port: got %d", editResponse.StatusCode)
	}
	editResponse.Body.Close()
	if names, err = store.NodeRoutingNames(t.Context(), nodeID); err != nil {
		t.Fatal(err)
	} else if names["listener-"+secondListener.ID] != "reality-b-new.example.com" {
		t.Fatalf("published name was not updated: %#v", names)
	}

	// Moving to a free port and back is just a port change now; nothing about
	// where it binds is decided here either way.
	for _, port := range []uint16{444, 443} {
		editedSecond.Port = port
		portEdit := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+secondListener.ID, editedSecond, session, csrfToken)
		if portEdit.StatusCode != http.StatusOK {
			t.Fatalf("port edit to %d: got %d", port, portEdit.StatusCode)
		}
		var moved control.Listener
		decodeBody(t, portEdit, &moved)
		if moved.Port != port || moved.BackendPort != port || moved.ListenAddr != "0.0.0.0" {
			t.Fatalf("service after the port edit = %#v", moved)
		}
	}

	duplicateDomain := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "重复 SNI", "connection_domain": "c.example.com", "port": 443, "enabled": true,
			"spec": map[string]any{
				"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
				"reality": map[string]any{"enabled": true, "handshake_server": "reality-a.example.com", "handshake_port": 443, "key_id": realityKey.ID},
			},
		},
		"accounts": []map[string]any{{"name": "用户 C", "outbound_id": "direct"}},
	}, session, csrfToken)
	if duplicateDomain.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate domain status = %d", duplicateDomain.StatusCode)
	}
	var duplicateError map[string]string
	decodeBody(t, duplicateDomain, &duplicateError)
	if !strings.Contains(duplicateError["error"], "实际 SNI") {
		t.Fatalf("duplicate domain error = %#v", duplicateError)
	}

	plainTCP := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "无加密 TCP", "port": 443, "enabled": true,
			"spec": map[string]any{"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": false}, "transport": map[string]any{"type": "ws", "path": "/plain"}},
		},
		"accounts": []map[string]any{{"name": "用户 D", "outbound_id": "direct"}},
	}, session, csrfToken)
	if plainTCP.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain TCP conflict status = %d", plainTCP.StatusCode)
	}

	udp := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "UDP 443", "port": 443, "enabled": true,
			"spec": map[string]any{"protocol": "hysteria2", "network": "udp", "tls": map[string]any{"enabled": true}},
		},
		"accounts": []map[string]any{{"name": "UDP 用户", "outbound_id": "direct"}},
	}, session, csrfToken)
	if udp.StatusCode != http.StatusCreated || udp.Header.Get("X-SB-Auto-Apply-Nginx-Task") != "" {
		t.Fatalf("TCP and UDP coexistence status = %d, automatic route task = %q", udp.StatusCode, udp.Header.Get("X-SB-Auto-Apply-Nginx-Task"))
	}
	var udpCreated struct {
		Listener control.Listener `json:"listener"`
	}
	decodeBody(t, udp, &udpCreated)

	secondUDP := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "重复 UDP 443", "port": 443, "enabled": true,
			"spec": map[string]any{"protocol": "hysteria2", "network": "udp", "tls": map[string]any{"enabled": true}},
		},
		"accounts": []map[string]any{{"name": "UDP 用户 2", "outbound_id": "direct"}},
	}, session, csrfToken)
	if secondUDP.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate UDP status = %d", secondUDP.StatusCode)
	}
	var udpError map[string]string
	decodeBody(t, secondUDP, &udpError)
	if !strings.Contains(udpError["error"], "UDP") || !strings.Contains(udpError["error"], "其他端口") {
		t.Fatalf("duplicate UDP error = %#v", udpError)
	}
	// A listener alone on its port moves ports directly: sing-box is
	// re-applied and no Nginx work is queued because no route is involved.
	renamedUDP := udpCreated.Listener
	renamedUDP.Name = "UDP 443 · 改名"
	renameEdit := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+udpCreated.Listener.ID, renamedUDP, session, csrfToken)
	if renameEdit.StatusCode != http.StatusOK {
		t.Fatalf("rename without port change status = %d", renameEdit.StatusCode)
	}
	renameEdit.Body.Close()
	movedUDP := renamedUDP
	movedUDP.Port = 8443
	movedUDP.BackendPort = 0
	directPortEdit := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+udpCreated.Listener.ID, movedUDP, session, csrfToken)
	if directPortEdit.StatusCode != http.StatusOK {
		t.Fatalf("direct listener port edit status = %d", directPortEdit.StatusCode)
	}
	if directPortEdit.Header.Get("X-SB-Auto-Apply-Nginx-Task") != "" {
		t.Fatal("moving an unrouted listener queued an Nginx task")
	}
	var movedUDPListener control.Listener
	decodeBody(t, directPortEdit, &movedUDPListener)
	if movedUDPListener.Port != 8443 || movedUDPListener.BackendPort != 8443 || movedUDPListener.ListenAddr != "0.0.0.0" {
		t.Fatalf("moved UDP listener = %#v", movedUDPListener)
	}
	// Both names on the shared port stay published, which is what the node uses
	// to build the router it decides it needs.
	if names, err = store.NodeRoutingNames(t.Context(), nodeID); err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, name := range names {
		published[name] = true
	}
	for _, expected := range []string{"reality-a.example.com", "reality-b-new.example.com"} {
		if !published[expected] {
			t.Fatalf("published routing names missing %q: %#v", expected, names)
		}
	}
	compiled, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		Route struct {
			Rules []map[string]any `json:"rules"`
		} `json:"route"`
	}
	if err := json.Unmarshal([]byte(compiled), &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Route.Rules) < 2 || configuration.Route.Rules[0]["auth_user"] == nil || configuration.Route.Rules[1]["auth_user"] == nil {
		t.Fatalf("account rules are not first: %#v", configuration.Route.Rules)
	}

	// Deleting a service stops publishing its name, which is how the node
	// learns to take it out of the router on the next apply.
	response := request(t, http.MethodDelete, httpServer.URL+"/api/v1/listeners/"+second.Listener.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete a service sharing the port: got %d", response.StatusCode)
	}
	if names, err = store.NodeRoutingNames(t.Context(), nodeID); err != nil {
		t.Fatal(err)
	} else if _, present := names["listener-"+second.Listener.ID]; present {
		t.Fatalf("a deleted service is still published: %#v", names)
	}
}

func TestEmptyNginxConfigurationCanClearManagedRoutes(t *testing.T) {
	configuration, hash, err := control.CompileNginxStream(nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(nil)
	if configuration != "" || hash != hex.EncodeToString(digest[:]) {
		t.Fatalf("empty Nginx configuration = %q, %q", configuration, hash)
	}
}

func TestRealityWebSocketGRPCAndHysteria2SharePublicPort443(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "four-protocol-443-node")

	create := func(name, domain string, spec map[string]any) control.Listener {
		t.Helper()
		response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
			"listener": map[string]any{
				"node_id": nodeID, "name": name, "connection_domain": domain,
				"listen_address": "0.0.0.0", "port": 443, "enabled": true, "spec": spec,
			},
			"accounts": []map[string]any{{"name": name + " 用户", "alias": name, "enabled": true, "outbound_id": "direct"}},
		}, session, csrfToken)
		if response.StatusCode != http.StatusCreated {
			var failure map[string]any
			decodeBody(t, response, &failure)
			t.Fatalf("create %s on port 443: status %d, response %#v", name, response.StatusCode, failure)
		}
		var created struct {
			Listener control.Listener      `json:"listener"`
			Ingress  *control.IngressRoute `json:"ingress_route"`
		}
		decodeBody(t, response, &created)
		if created.Ingress != nil {
			t.Fatalf("the control plane placed %s itself: %#v", name, created.Ingress)
		}
		return created.Listener
	}

	// All four are recorded on the public port they were asked for. Separating
	// the three TCP ones is the node's job, and it has the names to do it.
	reality := create("Reality 443", "reality.example.com", map[string]any{
		"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
		"reality": map[string]any{"enabled": true, "handshake_server": "reality-target.example.com", "handshake_port": 443},
	})
	websocket := create("WebSocket 443", "ws.example.com", map[string]any{
		"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true, "alpn": []string{"http/1.1"}},
		"transport": map[string]any{"type": "ws", "path": "/ws", "host": "ws.example.com"},
	})
	grpc := create("gRPC 443", "grpc.example.com", map[string]any{
		"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true, "alpn": []string{"h2"}},
		"transport": map[string]any{"type": "grpc", "service_name": "grpc-service"},
	})
	hysteria := create("HY2 443", "hy2.example.com", map[string]any{
		"protocol": "hysteria2", "network": "udp", "tls": map[string]any{"enabled": true},
	})

	listeners, err := store.ListListeners(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]control.Listener{}
	for _, listener := range listeners {
		byID[listener.ID] = listener
		if listener.Port != 443 {
			t.Fatalf("listener %s public port = %d, want 443", listener.Name, listener.Port)
		}
	}
	for _, id := range []string{reality.ID, websocket.ID, grpc.ID} {
		listener := byID[id]
		if listener.ListenAddr != "0.0.0.0" || listener.BackendPort != 443 {
			t.Fatalf("TCP service on the shared port was placed centrally: %#v", listener)
		}
	}
	if got := byID[hysteria.ID]; got.Spec.Network != "udp" || got.ListenAddr != "0.0.0.0" || got.BackendPort != 443 {
		t.Fatalf("HY2 did not remain on UDP/443: %#v", got)
	}

	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil || len(routes) != 0 {
		t.Fatalf("automatic routes were created centrally: %#v err:%v", routes, err)
	}
	// Each TCP service publishes the name the node separates it by. The UDP one
	// publishes none it needs: nothing on UDP competes for the TCP socket.
	names, err := store.NodeRoutingNames(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := map[string]string{
		reality.ID:   "reality-target.example.com",
		websocket.ID: "ws.example.com",
		grpc.ID:      "grpc.example.com",
	}
	for id, want := range wantNames {
		if names["listener-"+id] != want {
			t.Fatalf("published name for %s = %q, want %q", byID[id].Name, names["listener-"+id], want)
		}
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"\"type\": \"ws\"", "\"type\": \"grpc\"", "\"type\": \"hysteria2\"", "BEGIN CERTIFICATE"} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("four-protocol sing-box configuration missing %q", expected)
		}
	}
	if binary := os.Getenv("SING_BOX_BIN"); binary != "" {
		configPath := filepath.Join(t.TempDir(), "sing-box-four-protocol-443.json")
		if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(binary, "check", "-c", configPath).CombinedOutput(); err != nil {
			t.Fatalf("official sing-box rejected four-protocol port 443 configuration: %v\n%s", err, output)
		}
	}
	wsEndpoints, err := store.ListEndpoints(t.Context(), websocket.ID)
	if err != nil {
		t.Fatal(err)
	}
	grpcEndpoints, err := store.ListEndpoints(t.Context(), grpc.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.CreateSubscription(t.Context(), control.SubscriptionInput{
		Kind: control.ClientSubscription, Name: "WS and gRPC 443", EndpointIDs: []string{wsEndpoints[0].ID, grpcEndpoints[0].ID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := store.GenerateClientSubscription(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	links := string(decoded)
	for _, expected := range []string{
		"ws.example.com:443", "security=tls", "sni=ws.example.com", "type=ws", "path=%2Fws", "host=ws.example.com",
		"grpc.example.com:443", "sni=grpc.example.com", "type=grpc", "serviceName=grpc-service",
	} {
		if !strings.Contains(links, expected) {
			t.Fatalf("shared-port client subscription missing %q:\n%s", expected, links)
		}
	}
}

func TestOfficialSingBoxAcceptsPrimaryInboundVariants(t *testing.T) {
	binary := os.Getenv("SING_BOX_BIN")
	if binary == "" {
		t.Skip("SING_BOX_BIN is not set")
	}
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "official-config-node")
	realityKey, _, err := store.CreateRealityKey(t.Context(), "primary-inbound-reality")
	if err != nil {
		t.Fatal(err)
	}
	tls := control.TLSOptions{Enabled: true}
	definitions := []struct {
		name        string
		port        uint16
		spec        control.ProtocolSpec
		credentials control.EndpointCredentials
	}{
		{"VLESS WebSocket", 10443, control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws", Path: "/ws", Host: "example.com"}}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}},
		{"VLESS gRPC", 10444, control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "grpc", ServiceName: "grpc-service"}}, control.EndpointCredentials{UUID: "4e05f165-94f3-4f54-aac7-0487dcb83011"}},
		{"VLESS Reality", 10445, control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true}, Reality: control.RealityOptions{Enabled: true, KeyID: realityKey.ID, HandshakeServer: "www.microsoft.com", HandshakePort: 443, ShortIDs: []string{"0123456789abcdef"}}}, control.EndpointCredentials{UUID: "7d45d34f-09fc-43e6-b5cf-1ee1fbe3f2ca"}},
		{"Hysteria2", 10443, control.ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: tls}, control.EndpointCredentials{Password: "audit-password"}},
	}
	for _, definition := range definitions {
		listener, err := store.CreateListener(t.Context(), control.Listener{
			NodeID: nodeID, Name: definition.name, ListenAddr: "0.0.0.0", Port: definition.port, Enabled: true, Spec: definition.spec,
		})
		if err != nil {
			t.Fatalf("create %s listener: %v", definition.name, err)
		}
		if _, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: definition.name + " 用户", Enabled: true, OutboundID: "direct"}, definition.credentials); err != nil {
			t.Fatalf("create %s endpoint: %v", definition.name, err)
		}
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"grpc-service", `"type": "hysteria2"`, `"auth_user"`} {
		if !strings.Contains(configuration, expected) {
			t.Fatalf("primary inbound configuration missing %q", expected)
		}
	}
	configPath := filepath.Join(t.TempDir(), "sing-box-primary-inbounds.json")
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "check", "-c", configPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("official sing-box validation failed: %v\n%s\n%s", err, output, configuration)
	}
}

// fakeCloudflare emulates the Cloudflare v4 DNS records API in memory.
type fakeCloudflare struct {
	mu sync.Mutex
	// denied emulates a revoked or under-scoped token: every call is refused
	// the way Cloudflare refuses one.
	denied  bool
	records map[string]control.CloudflareRecord
	next    int
}

func (f *fakeCloudflare) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /zones/{zone}", func(w http.ResponseWriter, r *http.Request) {
		writeCF(w, control.CloudflareZoneInfo{ID: r.PathValue("zone"), Name: "example.com", Status: "active"})
	})
	mux.HandleFunc("GET /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		list := []control.CloudflareRecord{}
		for _, record := range f.records {
			list = append(list, record)
		}
		writeCF(w, list)
	})
	mux.HandleFunc("GET /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		record, ok := f.records[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"record not found"}]}`))
			return
		}
		writeCF(w, record)
	})
	mux.HandleFunc("POST /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var record control.CloudflareRecord
		_ = json.NewDecoder(r.Body).Decode(&record)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.next++
		record.ID = "remote-" + hex.EncodeToString([]byte{byte(f.next)})
		f.records[record.ID] = record
		writeCF(w, record)
	})
	// PATCH keeps whatever the console set and polaris does not manage, which is
	// why the comment below survives an edit.
	mux.HandleFunc("PATCH /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		var patch control.CloudflareRecord
		_ = json.NewDecoder(r.Body).Decode(&patch)
		f.mu.Lock()
		defer f.mu.Unlock()
		record, ok := f.records[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"record not found"}]}`))
			return
		}
		record.Type, record.Name, record.Content = patch.Type, patch.Name, patch.Content
		record.TTL, record.Proxied = patch.TTL, patch.Proxied
		if patch.Comment != "" {
			record.Comment = patch.Comment
		}
		f.records[record.ID] = record
		writeCF(w, record)
	})
	mux.HandleFunc("DELETE /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.records, r.PathValue("id"))
		writeCF(w, map[string]string{"id": r.PathValue("id")})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		denied := f.denied
		f.mu.Unlock()
		if denied {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"Invalid API Token"}]}`))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeCF(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

// DNS records live at Cloudflare and nowhere else. The list is the zone as it
// stands, a save lands upstream in the same request, and a delete needs no
// confirmation step because there is no local copy that could survive it.
func TestCloudflareRecordsWriteStraightToTheZone(t *testing.T) {
	fake := &fakeCloudflare{records: map[string]control.CloudflareRecord{}}
	fakeServer := httptest.NewServer(fake.handler())
	defer fakeServer.Close()
	control.SetCloudflareAPIBaseForTest(fakeServer.URL)

	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)

	// Records cannot exist before the zone is configured.
	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records", map[string]any{
		"name": "test.example.com", "type": "TXT", "content": "hello", "ttl": 300,
	}, session, csrfToken)
	if response.StatusCode == http.StatusCreated {
		t.Fatal("created a record before Cloudflare was configured")
	}
	response.Body.Close()

	response = request(t, http.MethodPut, httpServer.URL+"/api/v1/cloudflare/settings", map[string]string{
		"zone_id": "zone1", "zone_name": "example.com", "api_token": "test-token-1234567890",
	}, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("set Cloudflare settings: got %d", response.StatusCode)
	}
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/cloudflare/settings", nil, session, csrfToken)
	var settings control.CloudflareSettingsView
	decodeBody(t, response, &settings)
	if !settings.Configured || settings.TokenMasked == "test-token-1234567890" || settings.TokenMasked == "" {
		t.Fatalf("settings leak or missing mask: %#v", settings)
	}

	// A record created in the Cloudflare console is just part of the list: there
	// is no "unmanaged" bucket to adopt it out of.
	fake.mu.Lock()
	fake.records["external-record"] = control.CloudflareRecord{ID: "external-record", Type: "A", Name: "existing.example.com", Content: "192.0.2.1", TTL: 300, Proxied: true}
	fake.mu.Unlock()
	var listed struct {
		Records []control.CloudflareRecordView `json:"records"`
	}
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/cloudflare/records", nil, session, csrfToken)
	decodeBody(t, response, &listed)
	if len(listed.Records) != 1 || listed.Records[0].ID != "external-record" {
		t.Fatalf("zone records not listed: %#v", listed.Records)
	}

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records", map[string]any{
		"name": "test.example.com", "type": "TXT", "content": "hello", "ttl": 300,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create record: got %d", response.StatusCode)
	}
	var created control.CloudflareRecordView
	decodeBody(t, response, &created)
	fake.mu.Lock()
	upstream, exists := fake.records[created.ID]
	fake.mu.Unlock()
	if !exists || upstream.Content != "hello" {
		t.Fatalf("create did not reach Cloudflare: %#v", upstream)
	}

	outsideZone := request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records", map[string]any{
		"name": "test.other.com", "type": "TXT", "content": "x", "ttl": 300,
	}, session, csrfToken)
	if outsideZone.StatusCode == http.StatusCreated {
		t.Fatal("accepted a record outside the configured zone")
	}
	outsideZone.Body.Close()

	// A note added in the Cloudflare console must survive an edit made here.
	fake.mu.Lock()
	commented := fake.records[created.ID]
	commented.Comment = "set in the Cloudflare console"
	fake.records[created.ID] = commented
	fake.mu.Unlock()
	response = request(t, http.MethodPut, httpServer.URL+"/api/v1/cloudflare/records/"+created.ID, map[string]any{
		"name": "test.example.com", "type": "TXT", "content": "hello-again", "ttl": 300,
	}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update record: got %d", response.StatusCode)
	}
	response.Body.Close()
	fake.mu.Lock()
	upstream = fake.records[created.ID]
	fake.mu.Unlock()
	if upstream.Content != "hello-again" {
		t.Fatalf("update did not reach Cloudflare: %#v", upstream)
	}
	if upstream.Comment != "set in the Cloudflare console" {
		t.Fatalf("update dropped a field polaris does not manage: %#v", upstream)
	}

	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/cloudflare/records/"+created.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete record: got %d", response.StatusCode)
	}
	response.Body.Close()
	fake.mu.Lock()
	_, survived := fake.records[created.ID]
	fake.mu.Unlock()
	if survived {
		t.Fatal("delete left the record at Cloudflare")
	}
}

// Turning on the orange cloud is refused when it would break an access service
// published under the same name. The binding comes from the access service's
// own connection domain, so nothing is declared twice.
func TestCloudflareProxyValidationFollowsTheAccessServiceOnThatDomain(t *testing.T) {
	fake := &fakeCloudflare{records: map[string]control.CloudflareRecord{}}
	fakeServer := httptest.NewServer(fake.handler())
	defer fakeServer.Close()
	control.SetCloudflareAPIBaseForTest(fakeServer.URL)

	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "cdn-node")
	if err := store.SetCloudflareSettings(t.Context(), "zone1", "example.com", "test-token-1234567890"); err != nil {
		t.Fatal(err)
	}

	realityKey, _, err := store.CreateRealityKey(t.Context(), "cdn-reality")
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range []control.Listener{
		{NodeID: nodeID, Name: "vless-ws", Domain: "ws.example.com", ListenAddr: "0.0.0.0", Port: 8080, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws", Path: "/ws"}}},
		{NodeID: nodeID, Name: "vless-reality", Domain: "reality.example.com", ListenAddr: "0.0.0.0", Port: 9443, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true}, Reality: control.RealityOptions{Enabled: true, KeyID: realityKey.ID, HandshakeServer: "www.example.com", HandshakePort: 443, ShortIDs: []string{"0123456789abcdef"}}}},
		{NodeID: nodeID, Name: "vless-ws-tls", Domain: "ws-tls.example.com", ListenAddr: "127.0.0.1", Port: 443, BackendPort: 20443, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true}, Transport: control.TransportOptions{Type: "ws", Path: "/ws-tls"}}},
		{NodeID: nodeID, Name: "vless-grpc-tls", Domain: "grpc.example.com", ListenAddr: "127.0.0.1", Port: 443, BackendPort: 20444, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true}, Transport: control.TransportOptions{Type: "grpc", ServiceName: "grpc-service"}}},
		{NodeID: nodeID, Name: "vless-ws-4444", Domain: "badport.example.com", ListenAddr: "0.0.0.0", Port: 4444, Enabled: true,
			Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws", Path: "/ws"}}},
	} {
		if _, err := store.CreateListener(t.Context(), listener); err != nil {
			t.Fatalf("create listener %s: %v", listener.Name, err)
		}
	}

	for _, allowed := range []control.CloudflareRecord{
		{Name: "ws.example.com", Type: "A", Content: "192.0.2.10", TTL: 1, Proxied: true},
		{Name: "ws-tls.example.com", Type: "A", Content: "192.0.2.15", TTL: 1, Proxied: true},
		{Name: "grpc.example.com", Type: "A", Content: "192.0.2.16", TTL: 1, Proxied: true},
		// No access service claims this name, so the record may well front
		// something this platform does not host.
		{Name: "bare.example.com", Type: "A", Content: "192.0.2.12", TTL: 1, Proxied: true},
		// Grey cloud stays available for every access service.
		{Name: "reality.example.com", Type: "A", Content: "192.0.2.14", TTL: 300, Proxied: false},
	} {
		if _, err := store.CreateCloudflareRecord(t.Context(), allowed); err != nil {
			t.Fatalf("rejected %s: %v", allowed.Name, err)
		}
	}

	for _, refused := range []struct {
		reason string
		record control.CloudflareRecord
	}{
		{"orange cloud on a Reality access service", control.CloudflareRecord{Name: "reality.example.com", Type: "AAAA", Content: "2001:db8::11", TTL: 1, Proxied: true}},
		{"orange cloud on a TXT record", control.CloudflareRecord{Name: "ws.example.com", Type: "TXT", Content: "v=spf1", TTL: 1, Proxied: true}},
		{"orange cloud on a port Cloudflare does not proxy", control.CloudflareRecord{Name: "badport.example.com", Type: "A", Content: "192.0.2.13", TTL: 1, Proxied: true}},
	} {
		if _, err := store.CreateCloudflareRecord(t.Context(), refused.record); err == nil {
			t.Fatalf("allowed %s", refused.reason)
		}
	}
}

func TestNodeConnectionsAndClashAPICompilation(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "metrics-node")

	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-ws", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "alice", Enabled: true}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}); err != nil {
		t.Fatal(err)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, `"clash_api"`) || !strings.Contains(configuration, "127.0.0.1:9090") {
		t.Fatal("compiled configuration does not enable the loopback clash API")
	}
	if binary := os.Getenv("SING_BOX_BIN"); binary != "" {
		configPath := filepath.Join(t.TempDir(), "sing-box.json")
		if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "check", "-c", configPath)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("official sing-box validation failed: %v\n%s", err, output)
		}
	}

	// Connections are real-time, in-memory state pushed by the agent; nothing
	// about them is persisted, so the push is what the endpoint serves.
	server.PushConnectionsForTest(nodeID, "2026-07-26T00:00:00Z", json.RawMessage(
		`[{"id":"c1","inbound":"vless","network":"tcp","source":"198.51.100.7:52011","destination":"203.0.113.5:443","host":"target.example.com","upload":100,"download":2048,"started_at":"2026-07-26T00:00:00Z","outbound":"direct"}]`))
	response := request(t, http.MethodGet, httpServer.URL+"/api/v1/nodes/"+nodeID+"/connections", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("node connections: got %d", response.StatusCode)
	}
	var result struct {
		Connections []map[string]any `json:"connections"`
		CollectedAt string           `json:"collected_at"`
	}
	decodeBody(t, response, &result)
	if len(result.Connections) != 1 || result.Connections[0]["host"] != "target.example.com" {
		t.Fatalf("unexpected connections: %#v", result)
	}
}

// "已连接" has to mean the token actually reaches the zone. A configuration is
// stored only after it verifies, and one that stops working stops reporting as
// connected instead of leaving a stale label behind.
func TestCloudflareSettingsReportVerifiedConnectionOnly(t *testing.T) {
	fake := &fakeCloudflare{records: map[string]control.CloudflareRecord{}}
	fakeServer := httptest.NewServer(fake.handler())
	defer fakeServer.Close()
	control.SetCloudflareAPIBaseForTest(fakeServer.URL)

	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	settingsURL := httpServer.URL + "/api/v1/cloudflare/settings"
	valid := map[string]string{"zone_id": "zone1", "zone_name": "example.com", "api_token": "test-token-1234567890"}

	fake.mu.Lock()
	fake.denied = true
	fake.mu.Unlock()
	response := request(t, http.MethodPut, settingsURL, valid, session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("stored a token Cloudflare rejects: got %d", response.StatusCode)
	}
	var failure struct {
		Error string `json:"error"`
	}
	decodeBody(t, response, &failure)
	if !strings.Contains(failure.Error, "Invalid API Token") {
		t.Fatalf("upstream reason was swallowed: %q", failure.Error)
	}
	response = request(t, http.MethodGet, settingsURL, nil, session, csrfToken)
	var settings control.CloudflareSettingsView
	decodeBody(t, response, &settings)
	if settings.Configured || settings.Connected {
		t.Fatalf("rejected settings were kept: %#v", settings)
	}

	fake.mu.Lock()
	fake.denied = false
	fake.mu.Unlock()
	mismatch := map[string]string{"zone_id": "zone1", "zone_name": "other.com", "api_token": "test-token-1234567890"}
	response = request(t, http.MethodPut, settingsURL, mismatch, session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("accepted a zone name the zone ID does not serve: got %d", response.StatusCode)
	}
	response.Body.Close()

	response = request(t, http.MethodPut, settingsURL, valid, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("set verified Cloudflare settings: got %d", response.StatusCode)
	}
	response = request(t, http.MethodGet, settingsURL, nil, session, csrfToken)
	decodeBody(t, response, &settings)
	if !settings.Configured || !settings.Connected || settings.Error != "" {
		t.Fatalf("verified settings not reported as connected: %#v", settings)
	}

	// A token revoked after it was stored must surface, not keep claiming a
	// connection that no longer exists.
	fake.mu.Lock()
	fake.denied = true
	fake.mu.Unlock()
	response = request(t, http.MethodGet, settingsURL, nil, session, csrfToken)
	decodeBody(t, response, &settings)
	if !settings.Configured || settings.Connected || !strings.Contains(settings.Error, "Invalid API Token") {
		t.Fatalf("revoked token still reported as connected: %#v", settings)
	}
}
