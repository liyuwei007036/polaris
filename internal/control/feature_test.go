package control_test

import (
	"crypto/sha256"
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

	"github.com/sb-control/sb-control/internal/control"
	"github.com/sb-control/sb-control/internal/wire"
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

func TestFirewallRulesIncludeIPLocations(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "firewall-location-node")

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/firewall/rules", map[string]any{
		"action": "accept", "protocol": "tcp", "cidr": "8.8.8.8/32", "port": 443, "enabled": true,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create firewall rule: got %d", response.StatusCode)
	}
	response.Body.Close()

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/nodes/"+nodeID+"/firewall/rules", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list firewall rules: got %d", response.StatusCode)
	}
	var result struct {
		Rules []control.FirewallRule `json:"rules"`
	}
	decodeBody(t, response, &result)
	if len(result.Rules) != 1 || !strings.Contains(result.Rules[0].Location, "United States") {
		t.Fatalf("firewall rules missing IP location: %#v", result.Rules)
	}
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

	create := func(name, sni string, expectAutomaticRouting bool) struct {
		Listener  control.Listener      `json:"listener"`
		Endpoints []control.Endpoint    `json:"endpoints"`
		Ingress   *control.IngressRoute `json:"ingress_route"`
	} {
		t.Helper()
		response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
			"listener": map[string]any{
				"node_id": nodeID, "name": name, "connection_domain": sni, "listen_address": "0.0.0.0", "port": 443, "enabled": true,
				"spec": map[string]any{
					"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
					"reality": map[string]any{"enabled": true, "handshake_server": "www.microsoft.com", "handshake_port": 443, "key_id": realityKey.ID},
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
		if got := response.Header.Get("X-SB-Auto-Apply-Nginx-Task") != ""; got != expectAutomaticRouting {
			t.Fatalf("automatic port task header present = %v, want %v", got, expectAutomaticRouting)
		}
		var created struct {
			Listener  control.Listener      `json:"listener"`
			Endpoints []control.Endpoint    `json:"endpoints"`
			Ingress   *control.IngressRoute `json:"ingress_route"`
		}
		decodeBody(t, response, &created)
		return created
	}

	first := create("Reality A", "a.example.com", false)
	second := create("Reality B", "b.example.com", true)
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
	if firstListener.Port != 443 || secondListener.Port != 443 || firstListener.BackendPort == secondListener.BackendPort {
		t.Fatalf("automatically routed listener ports = %#v / %#v", firstListener, secondListener)
	}
	if firstListener.ListenAddr != "127.0.0.1" || secondListener.ListenAddr != "127.0.0.1" || second.Ingress == nil {
		t.Fatalf("automatically routed listeners = %#v / %#v, route %#v", firstListener, secondListener, second.Ingress)
	}
	routes, err := store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("automatic routes = %#v", routes)
	}
	routeByListener := map[string]control.IngressRoute{}
	for _, route := range routes {
		routeByListener[route.ListenerID] = route
	}
	if routeByListener[first.Listener.ID].BackendPort != firstListener.BackendPort || routeByListener[second.Listener.ID].BackendPort != secondListener.BackendPort {
		t.Fatalf("automatic route backends = %#v", routes)
	}
	editedSecond := secondListener
	editedSecond.Domain = "vless-b-new.example.com"
	editResponse := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+secondListener.ID, editedSecond, session, csrfToken)
	if editResponse.StatusCode != http.StatusOK {
		t.Fatalf("edit automatically routed listener: got %d", editResponse.StatusCode)
	}
	editResponse.Body.Close()
	routes, err = store.ListIngressRoutes(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.ListenerID == secondListener.ID && route.SNI != "vless-b-new.example.com" {
			t.Fatalf("automatic route name was not updated: %#v", route)
		}
	}
	editedSecond.Port = 444
	portEdit := request(t, http.MethodPut, httpServer.URL+"/api/v1/listeners/"+secondListener.ID, editedSecond, session, csrfToken)
	if portEdit.StatusCode != http.StatusBadRequest {
		t.Fatalf("managed port edit status = %d", portEdit.StatusCode)
	}
	var portEditError map[string]string
	decodeBody(t, portEdit, &portEditError)
	if !strings.Contains(portEditError["error"], "系统自动分配") {
		t.Fatalf("managed port edit error = %#v", portEditError)
	}

	duplicateDomain := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "重复域名", "connection_domain": "a.example.com", "port": 443, "enabled": true,
			"spec": map[string]any{
				"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
				"reality": map[string]any{"enabled": true, "handshake_server": "www.microsoft.com", "handshake_port": 443, "key_id": realityKey.ID},
			},
		},
		"accounts": []map[string]any{{"name": "用户 C", "outbound_id": "direct"}},
	}, session, csrfToken)
	if duplicateDomain.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate domain status = %d", duplicateDomain.StatusCode)
	}
	var duplicateError map[string]string
	decodeBody(t, duplicateDomain, &duplicateError)
	if !strings.Contains(duplicateError["error"], "不同的连接域名") {
		t.Fatalf("duplicate domain error = %#v", duplicateError)
	}

	plainTCP := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "无加密 TCP", "port": 443, "enabled": true,
			"spec": map[string]any{"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": false}},
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
	udp.Body.Close()
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
	nginx, _, err := store.CompileNodeNginx(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"listen 0.0.0.0:443", "a.example.com", "vless-b-new.example.com"} {
		if !strings.Contains(nginx, expected) {
			t.Fatalf("Nginx configuration missing %q:\n%s", expected, nginx)
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

	response := request(t, http.MethodDelete, httpServer.URL+"/api/v1/ingress-routes/"+routeByListener[first.Listener.ID].ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete first shared ingress route: got %d", response.StatusCode)
	}
	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/ingress-routes/"+routeByListener[second.Listener.ID].ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete last shared ingress route: got %d", response.StatusCode)
	}
	if response.Header.Get("X-SB-Auto-Apply-Nginx-Task") == "" {
		t.Fatal("deleting the last ingress route did not queue the Nginx clear task")
	}
	nginx, hash, err := store.CompileNodeNginx(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(nil)
	if nginx != "" || hash != hex.EncodeToString(digest[:]) {
		t.Fatalf("cleared Nginx configuration = %q, %q", nginx, hash)
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

func TestCompileFail2BanRendersManagedNamespace(t *testing.T) {
	jail := control.Fail2BanJail{Name: "singbox-auth", LogPath: "/var/log/sing-box.log", FilterName: "singbox-auth", FailRegex: "authentication failed from <HOST>", MaxRetry: 3, FindTimeSeconds: 600, BanTimeSeconds: 3600, Enabled: true}
	jailFile, filters, err := control.CompileFail2Ban([]control.Fail2BanJail{jail})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jailFile, "[sb-control-singbox-auth]") || !strings.Contains(jailFile, "filter = sb-control-singbox-auth") {
		t.Fatalf("jail file missing managed namespace: %q", jailFile)
	}
	filter, ok := filters["sb-control-singbox-auth.conf"]
	if !ok || !strings.Contains(filter, "failregex = authentication failed from <HOST>") {
		t.Fatalf("filter file missing: %#v", filters)
	}
	jail.Name = "bad name"
	if _, _, err := control.CompileFail2Ban([]control.Fail2BanJail{jail}); err == nil {
		t.Fatal("accepted jail name with spaces")
	}
	jail.Name = "singbox-auth"
	jail.FailRegex = "line1\nline2"
	if _, _, err := control.CompileFail2Ban([]control.Fail2BanJail{jail}); err == nil {
		t.Fatal("accepted multi-line fail regex")
	}
	conflicting := []control.Fail2BanJail{
		{Name: "a", LogPath: "/var/log/a.log", FilterName: "shared", FailRegex: "x <HOST>", MaxRetry: 1, FindTimeSeconds: 1, BanTimeSeconds: 1, Enabled: true},
		{Name: "b", LogPath: "/var/log/b.log", FilterName: "shared", FailRegex: "y <HOST>", MaxRetry: 1, FindTimeSeconds: 1, BanTimeSeconds: 1, Enabled: true},
	}
	if _, _, err := control.CompileFail2Ban(conflicting); err == nil {
		t.Fatal("accepted one filter name with two different regexes")
	}
}

func TestFail2BanJailLifecycleAndPublish(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "fail2ban-node")

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/fail2ban/jails", map[string]any{
		"name": "singbox-auth", "log_path": "/var/log/sing-box.log", "filter_name": "singbox-auth",
		"fail_regex": "authentication failed from <HOST>", "max_retry": 3, "find_time_seconds": 600, "ban_time_seconds": 3600, "enabled": true,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create fail2ban jail: got %d", response.StatusCode)
	}
	var jail control.Fail2BanJail
	decodeBody(t, response, &jail)
	if jail.ID == "" || jail.NodeID != nodeID {
		t.Fatalf("unexpected jail: %#v", jail)
	}

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/nodes/"+nodeID+"/fail2ban/jails", nil, session, csrfToken)
	var listed struct {
		Jails []control.Fail2BanJail `json:"jails"`
	}
	decodeBody(t, response, &listed)
	if len(listed.Jails) != 1 {
		t.Fatalf("expected one jail, got %#v", listed.Jails)
	}

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+nodeID+"/fail2ban/publish", nil, session, csrfToken)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("publish fail2ban: got %d", response.StatusCode)
	}
	var task control.Task
	decodeBody(t, response, &task)
	if task.Kind != "fail2ban.apply" {
		t.Fatalf("unexpected task kind %q", task.Kind)
	}
	digest := sha256.Sum256([]byte(task.Payload))
	if !strings.EqualFold(hex.EncodeToString(digest[:]), task.ExpectedHash) {
		t.Fatal("task hash does not cover the payload")
	}
	var payload struct {
		Jail    string            `json:"jail"`
		Filters map[string]string `json:"filters"`
	}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Jail, "[sb-control-singbox-auth]") || len(payload.Filters) != 1 {
		t.Fatalf("unexpected payload: %#v", payload)
	}

	response = request(t, http.MethodPut, httpServer.URL+"/api/v1/fail2ban/jails/"+jail.ID, map[string]any{
		"name": "singbox-auth", "log_path": "/var/log/sing-box.log", "filter_name": "singbox-auth",
		"fail_regex": "denied from <HOST>", "max_retry": 5, "find_time_seconds": 300, "ban_time_seconds": 600, "enabled": true,
	}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update jail: got %d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/fail2ban/jails/"+jail.ID+"/enabled", map[string]bool{"enabled": false}, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("disable jail: got %d", response.StatusCode)
	}
	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/fail2ban/jails/"+jail.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete jail: got %d", response.StatusCode)
	}
}

// fakeCloudflare emulates the Cloudflare v4 DNS records API in memory.
type fakeCloudflare struct {
	mu      sync.Mutex
	records map[string]control.CloudflareRecord
	next    int
}

func (f *fakeCloudflare) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /zones/{zone}/dns_records", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		list := []control.CloudflareRecord{}
		for _, record := range f.records {
			list = append(list, record)
		}
		writeCF(w, list)
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
	mux.HandleFunc("PUT /zones/{zone}/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		var record control.CloudflareRecord
		_ = json.NewDecoder(r.Body).Decode(&record)
		record.ID = r.PathValue("id")
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.records[record.ID]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"message":"record not found"}]}`))
			return
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
	return mux
}

func writeCF(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": result})
}

func TestCloudflareRecordsDriftAndPublish(t *testing.T) {
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
	fake.mu.Lock()
	fake.records["external-record"] = control.CloudflareRecord{ID: "external-record", Type: "A", Name: "existing.example.com", Content: "192.0.2.1", TTL: 300, Proxied: true}
	fake.mu.Unlock()
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/cloudflare/remote-records", nil, session, csrfToken)
	var remoteRecords struct {
		Records []control.CloudflareRecord `json:"records"`
	}
	decodeBody(t, response, &remoteRecords)
	if len(remoteRecords.Records) != 1 || remoteRecords.Records[0].ID != "external-record" {
		t.Fatalf("remote records not listed: %#v", remoteRecords.Records)
	}
	fake.mu.Lock()
	delete(fake.records, "external-record")
	fake.mu.Unlock()

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records", map[string]any{
		"name": "test.example.com", "type": "TXT", "content": "hello", "ttl": 300,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create record: got %d", response.StatusCode)
	}
	var record control.ManagedCloudflareRecord
	decodeBody(t, response, &record)
	if record.Status != "pending" {
		t.Fatalf("new record status %q", record.Status)
	}
	outsideZone := request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records", map[string]any{
		"name": "test.other.com", "type": "TXT", "content": "x", "ttl": 300,
	}, session, csrfToken)
	if outsideZone.StatusCode == http.StatusCreated {
		t.Fatal("accepted a record outside the configured zone")
	}
	outsideZone.Body.Close()

	// Preview without confirm must not write upstream.
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records/"+record.ID+"/publish", map[string]bool{"confirm": false}, session, csrfToken)
	var preview struct {
		RequiresConfirm bool `json:"requires_confirm"`
	}
	decodeBody(t, response, &preview)
	if !preview.RequiresConfirm || len(fake.records) != 0 {
		t.Fatalf("publish preview wrote upstream: %#v, %d records", preview, len(fake.records))
	}
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/records/"+record.ID+"/publish", map[string]bool{"confirm": true}, session, csrfToken)
	var published struct {
		Record control.ManagedCloudflareRecord `json:"record"`
	}
	decodeBody(t, response, &published)
	if published.Record.Status != "synced" || published.Record.RemoteID == "" || len(fake.records) != 1 {
		t.Fatalf("publish did not sync: %#v", published.Record)
	}
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/cloudflare/remote-records", nil, session, csrfToken)
	decodeBody(t, response, &remoteRecords)
	if len(remoteRecords.Records) != 0 {
		t.Fatalf("managed record also listed as remote-only: %#v", remoteRecords.Records)
	}

	// Simulate an out-of-band console change, then detect drift without
	// overwriting it.
	fake.mu.Lock()
	changed := fake.records[published.Record.RemoteID]
	changed.Content = "changed-by-console"
	fake.records[published.Record.RemoteID] = changed
	fake.mu.Unlock()
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/cloudflare/sync", nil, session, csrfToken)
	var synced struct {
		Drifted int                               `json:"drifted"`
		Records []control.ManagedCloudflareRecord `json:"records"`
	}
	decodeBody(t, response, &synced)
	if synced.Drifted != 1 || synced.Records[0].Status != "drift" {
		t.Fatalf("drift not detected: %#v", synced)
	}
	fake.mu.Lock()
	if fake.records[published.Record.RemoteID].Content != "changed-by-console" {
		fake.mu.Unlock()
		t.Fatal("sync overwrote the external change")
	}
	fake.mu.Unlock()

	// Deleting a published record needs explicit confirmation.
	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/cloudflare/records/"+record.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("delete without confirm: got %d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/cloudflare/records/"+record.ID+"?confirm=true", nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent || len(fake.records) != 0 {
		t.Fatalf("confirmed delete failed: %d, %d remote records", response.StatusCode, len(fake.records))
	}
}

func TestCloudflareProxyValidationFollowsListenerType(t *testing.T) {
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

	wsListener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-ws", ListenAddr: "0.0.0.0", Port: 8080, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws", Path: "/ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	realityKey, _, err := store.CreateRealityKey(t.Context(), "cdn-reality")
	if err != nil {
		t.Fatal(err)
	}
	realityListener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-reality", ListenAddr: "0.0.0.0", Port: 9443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true}, Reality: control.RealityOptions{Enabled: true, KeyID: realityKey.ID, HandshakeServer: "www.example.com", HandshakePort: 443, ShortIDs: []string{"0123456789abcdef"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "ws.example.com", Type: "A", Content: "192.0.2.10", TTL: 1, Proxied: true, ListenerID: wsListener.ID,
	}); err != nil {
		t.Fatalf("rejected orange cloud for WebSocket listener on 8080: %v", err)
	}
	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "reality.example.com", Type: "A", Content: "192.0.2.11", TTL: 1, Proxied: true, ListenerID: realityListener.ID,
	}); err == nil {
		t.Fatal("allowed orange cloud on a Reality listener")
	}
	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "bare.example.com", Type: "A", Content: "192.0.2.12", TTL: 1, Proxied: true,
	}); err == nil {
		t.Fatal("allowed orange cloud without a listener binding")
	}
	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "txt.example.com", Type: "TXT", Content: "v=spf1", TTL: 1, Proxied: true, ListenerID: wsListener.ID,
	}); err == nil {
		t.Fatal("allowed orange cloud on a TXT record")
	}
	badPort, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-ws-4444", ListenAddr: "0.0.0.0", Port: 4444, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws", Path: "/ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "badport.example.com", Type: "A", Content: "192.0.2.13", TTL: 1, Proxied: true, ListenerID: badPort.ID,
	}); err == nil {
		t.Fatal("allowed orange cloud on a port Cloudflare does not proxy")
	}
	// Grey cloud stays available for every listener type.
	if _, err := store.CreateCloudflareRecord(t.Context(), control.ManagedCloudflareRecord{
		Name: "grey.example.com", Type: "A", Content: "192.0.2.14", TTL: 300, Proxied: false, ListenerID: realityListener.ID,
	}); err != nil {
		t.Fatalf("rejected grey cloud record: %v", err)
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

	metrics := `{"collected_at":"2026-07-26T00:00:00Z","connections":[{"id":"c1","inbound":"vless","network":"tcp","source":"198.51.100.7:52011","destination":"203.0.113.5:443","host":"target.example.com","upload":100,"download":2048,"started_at":"2026-07-26T00:00:00Z","outbound":"direct"}],"capabilities":{"connection":{"cumulative_traffic":true,"instant_rate":false,"connection_count":true,"source":"sing-box clash_api http://127.0.0.1:9090","precision":"per_connection"}}}`
	if err := store.UpdateAgentStatus(t.Context(), nodeID, control.AgentStatus{AgentVersion: "test", OS: "linux", Architecture: "arm64", SingBox: "1.12.0", Capabilities: "{}", Metrics: metrics}); err != nil {
		t.Fatal(err)
	}
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
