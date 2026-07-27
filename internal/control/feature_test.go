package control_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sb-control/sb-control/internal/agent"
	"github.com/sb-control/sb-control/internal/control"
)

func approveTestNode(t *testing.T, baseURL, session, csrfToken, nodeName string) string {
	t.Helper()
	csr, err := agent.CreateCSR(t.TempDir(), nodeName)
	if err != nil {
		t.Fatal(err)
	}
	registrationID, _ := registerAgent(t, baseURL, createRegistrationToken(t, baseURL, session, csrfToken), csr)
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
	nodeID := approveTestNode(t, httpServer.URL, session, csrfToken, "fail2ban-node")

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
	nodeID := approveTestNode(t, httpServer.URL, session, csrfToken, "cdn-node")
	if err := store.SetCloudflareSettings(t.Context(), "zone1", "example.com", "test-token-1234567890"); err != nil {
		t.Fatal(err)
	}

	certificatePEM, privateKeyPEM := testCertificate(t)
	certificate, err := store.CreateManagedCertificate(t.Context(), control.ManagedCertificateInput{Name: "cdn-cert", CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	wsListener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "vless-ws", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true, CertificateID: certificate.ID}, Transport: control.TransportOptions{Type: "ws", Path: "/ws"}},
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
		t.Fatalf("rejected orange cloud for WebSocket+TLS listener on 8443: %v", err)
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
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", TLS: control.TLSOptions{Enabled: true, CertificateID: certificate.ID}, Transport: control.TransportOptions{Type: "ws", Path: "/ws"}},
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
	nodeID := approveTestNode(t, httpServer.URL, session, csrfToken, "metrics-node")

	certificatePEM, privateKeyPEM := testCertificate(t)
	certificate, err := store.CreateManagedCertificate(t.Context(), control.ManagedCertificateInput{Name: "metrics-cert", CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "trojan", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "trojan", Network: "tcp", TLS: control.TLSOptions{Enabled: true, CertificateID: certificate.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: listener.ID, Name: "alice", Enabled: true}, control.EndpointCredentials{Password: "password"}); err != nil {
		t.Fatal(err)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configuration, `"clash_api"`) || !strings.Contains(configuration, "127.0.0.1:9090") {
		t.Fatal("compiled configuration does not enable the loopback clash API")
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
