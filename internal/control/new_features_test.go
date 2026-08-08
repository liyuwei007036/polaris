package control_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/control"
)

// newFeatureServer builds the usual store/server/session trio the tests below
// share.
func newFeatureServer(t *testing.T) (*control.Store, *control.Server, string, string, string) {
	t.Helper()
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	session, csrfToken := login(t, httpServer.URL, secret)
	return store, server, httpServer.URL, session, csrfToken
}

func TestOperationRecordsAreTrimmedToTheRetentionWindow(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "retention-node")

	operators, err := store.ListOperators(t.Context())
	if err != nil || len(operators) == 0 {
		t.Fatalf("list operators: %v", err)
	}
	// Two records well outside the window and two inside it.
	stale := time.Now().UTC().Add(-8 * 24 * time.Hour).Unix()
	fresh := time.Now().UTC().Add(-time.Hour).Unix()
	for _, target := range []string{"old", "new"} {
		if err := store.AppendAudit(t.Context(), operators[0].ID, "listener.created", "listener", target, target); err != nil {
			t.Fatal(err)
		}
	}
	execForTest(t, store, `UPDATE audit_events SET created_at = ? WHERE target_id = 'old'`, stale)
	execForTest(t, store, `UPDATE audit_events SET created_at = ? WHERE target_id = 'new'`, fresh)
	execForTest(t, store, `INSERT INTO tasks (id, node_id, kind, idempotency_key, payload, expected_hash, status, created_at) VALUES ('t-old', ?, 'firewall.apply', 'k-old', '{}', '', 'succeeded', ?)`, nodeID, stale)
	execForTest(t, store, `INSERT INTO tasks (id, node_id, kind, idempotency_key, payload, expected_hash, status, created_at) VALUES ('t-queued', ?, 'firewall.apply', 'k-queued', '{}', '', 'queued', ?)`, nodeID, stale)
	execForTest(t, store, `INSERT INTO tasks (id, node_id, kind, idempotency_key, payload, expected_hash, status, created_at) VALUES ('t-new', ?, 'firewall.apply', 'k-new', '{}', '', 'succeeded', ?)`, nodeID, fresh)

	if err := store.PurgeExpiredOperationRecords(t.Context()); err != nil {
		t.Fatal(err)
	}

	events, _, err := store.ListAudit(t.Context(), 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, event := range events {
		kept[event.TargetID] = true
	}
	if kept["old"] {
		t.Fatal("audit event older than the retention window was kept")
	}
	if !kept["new"] {
		t.Fatal("audit event inside the retention window was removed")
	}
	// An unfinished task stays regardless of age: its result may still arrive.
	for id, expected := range map[string]bool{"t-old": false, "t-queued": true, "t-new": true} {
		if got := taskExistsForTest(t, store, id); got != expected {
			t.Fatalf("task %s present = %v, want %v", id, got, expected)
		}
	}
}

func TestSharedRuleProvidersAreReferencedByClientConfigs(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)

	var provider struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/mihomo/rule-providers", map[string]any{
		"name": "远程规则", "behavior": "domain", "format": "mrs",
		"url": "https://rules.example.com/proxy.mrs", "path": "./ruleset/proxy.mrs",
		"interval": 86400, "proxy": "DIRECT",
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create rule provider: got %d", response.StatusCode)
	}
	decodeBody(t, response, &provider)

	// A name already in use is rejected rather than silently shadowing.
	response = request(t, http.MethodPost, baseURL+"/api/v1/mihomo/rule-providers", map[string]any{
		"name": "远程规则", "behavior": "domain", "format": "mrs",
		"url": "https://rules.example.com/other.mrs", "path": "./ruleset/other.mrs",
		"interval": 3600, "proxy": "DIRECT",
	}, session, csrfToken)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate rule provider name: got %d", response.StatusCode)
	}

	configID := createRuleSetClientConfig(t, store, server, baseURL, session, csrfToken, provider.ID, provider.Name)

	// A provider in use cannot be deleted out from under a configuration.
	response = request(t, http.MethodDelete, baseURL+"/api/v1/mihomo/rule-providers/"+provider.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("delete referenced rule provider: got %d", response.StatusCode)
	}

	// Renaming follows through into the RULE-SET rules that name it.
	response = request(t, http.MethodPut, baseURL+"/api/v1/mihomo/rule-providers/"+provider.ID, map[string]any{
		"name": "远程规则改名", "behavior": "domain", "format": "mrs",
		"url": "https://rules.example.com/proxy.mrs", "path": "./ruleset/proxy.mrs",
		"interval": 86400, "proxy": "DIRECT",
	}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rename rule provider: got %d", response.StatusCode)
	}
	var configs struct {
		ClientConfigs []struct {
			ID              string   `json:"id"`
			RuleProviderIDs []string `json:"rule_provider_ids"`
			RawRules        string   `json:"raw_rules"`
			Subscription    string   `json:"subscription_path"`
		} `json:"client_configs"`
	}
	response = request(t, http.MethodGet, baseURL+"/api/v1/mihomo/client-configs", nil, session, csrfToken)
	decodeBody(t, response, &configs)
	if len(configs.ClientConfigs) != 1 || configs.ClientConfigs[0].ID != configID {
		t.Fatalf("unexpected client configs: %#v", configs.ClientConfigs)
	}
	if !strings.Contains(configs.ClientConfigs[0].RawRules, "RULE-SET,远程规则改名") {
		t.Fatalf("rename was not applied to the rules: %q", configs.ClientConfigs[0].RawRules)
	}
	if len(configs.ClientConfigs[0].RuleProviderIDs) != 1 || configs.ClientConfigs[0].RuleProviderIDs[0] != provider.ID {
		t.Fatalf("client config lost its provider reference: %#v", configs.ClientConfigs[0].RuleProviderIDs)
	}
	// The generated profile carries the provider under its new name.
	response = request(t, http.MethodGet, baseURL+configs.ClientConfigs[0].Subscription, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("subscription: got %d", response.StatusCode)
	}
	yaml := readBodyForTest(t, response)
	for _, expected := range []string{"远程规则改名:", "RULE-SET,远程规则改名,DIRECT"} {
		if !strings.Contains(yaml, expected) {
			t.Fatalf("generated profile is missing %q:\n%s", expected, yaml)
		}
	}
}

func TestClientConfigCopyIsIndependent(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	var provider struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/mihomo/rule-providers", map[string]any{
		"name": "复制用规则", "behavior": "domain", "format": "mrs",
		"url": "https://rules.example.com/copy.mrs", "path": "./ruleset/copy.mrs",
		"interval": 86400, "proxy": "DIRECT",
	}, session, csrfToken)
	decodeBody(t, response, &provider)
	configID := createRuleSetClientConfig(t, store, server, baseURL, session, csrfToken, provider.ID, provider.Name)

	var copied struct {
		ID               string   `json:"id"`
		Name             string   `json:"name"`
		RuleProviderIDs  []string `json:"rule_provider_ids"`
		SubscriptionPath string   `json:"subscription_path"`
	}
	response = request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs/"+configID+"/copy", map[string]any{"name": "副本配置"}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("copy client config: got %d", response.StatusCode)
	}
	decodeBody(t, response, &copied)
	if copied.ID == configID || copied.Name != "副本配置" {
		t.Fatalf("copy did not become its own configuration: %#v", copied)
	}
	if len(copied.RuleProviderIDs) != 1 || copied.RuleProviderIDs[0] != provider.ID {
		t.Fatalf("copy lost the rule provider reference: %#v", copied.RuleProviderIDs)
	}
	// Each configuration owns its update address, so revoking one leaves the
	// other's link working.
	var original struct {
		ClientConfigs []struct {
			ID               string `json:"id"`
			SubscriptionPath string `json:"subscription_path"`
		} `json:"client_configs"`
	}
	response = request(t, http.MethodGet, baseURL+"/api/v1/mihomo/client-configs", nil, session, csrfToken)
	decodeBody(t, response, &original)
	for _, config := range original.ClientConfigs {
		if config.ID == configID && config.SubscriptionPath == copied.SubscriptionPath {
			t.Fatal("copy shares the original's update address")
		}
	}
}

func TestOutboundExpiryRoundTrips(t *testing.T) {
	_, _, baseURL, session, csrfToken := newFeatureServer(t)
	expiry := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	var created struct {
		ID        string `json:"id"`
		ExpiresAt string `json:"expires_at"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/outbounds", map[string]any{
		"name": "到期出口", "type": "socks", "server": "127.0.0.1", "server_port": 1080,
		"enabled": true, "expires_at": expiry.Format(time.RFC3339),
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create outbound: got %d", response.StatusCode)
	}
	decodeBody(t, response, &created)

	var listed struct {
		Outbounds []struct {
			ID        string `json:"id"`
			ExpiresAt string `json:"expires_at"`
		} `json:"outbounds"`
	}
	response = request(t, http.MethodGet, baseURL+"/api/v1/outbounds", nil, session, csrfToken)
	decodeBody(t, response, &listed)
	if len(listed.Outbounds) != 1 || listed.Outbounds[0].ExpiresAt != expiry.Format(time.RFC3339) {
		t.Fatalf("outbound expiry did not round-trip: %#v", listed.Outbounds)
	}

	// Clearing the date is allowed; it is a reminder, not a constraint.
	response = request(t, http.MethodPut, baseURL+"/api/v1/outbounds/"+created.ID, map[string]any{
		"name": "到期出口", "type": "socks", "server": "127.0.0.1", "server_port": 1080,
		"enabled": true, "expires_at": "",
	}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update outbound: got %d", response.StatusCode)
	}
	response = request(t, http.MethodGet, baseURL+"/api/v1/outbounds", nil, session, csrfToken)
	listed.Outbounds = nil
	decodeBody(t, response, &listed)
	if len(listed.Outbounds) != 1 || listed.Outbounds[0].ExpiresAt != "" {
		t.Fatalf("outbound expiry was not cleared: %#v", listed.Outbounds)
	}

	// A value that is not a timestamp is refused rather than stored as zero.
	response = request(t, http.MethodPut, baseURL+"/api/v1/outbounds/"+created.ID, map[string]any{
		"name": "到期出口", "type": "socks", "server": "127.0.0.1", "server_port": 1080,
		"enabled": true, "expires_at": "下个月",
	}, session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid outbound expiry: got %d", response.StatusCode)
	}
}

func TestReleasingABannedAddressNeedsAValidAddressAndAReachableServer(t *testing.T) {
	_, server, baseURL, session, csrfToken := newFeatureServer(t)
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "banned-node")

	// A value that is not an address never reaches the node.
	response := request(t, http.MethodPost, baseURL+"/api/v1/nodes/"+nodeID+"/fail2ban/unban", map[string]any{
		"jail": "polaris-ssh-bruteforce", "ip": "not-an-ip",
	}, session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid unban address: got %d", response.StatusCode)
	}
	response.Body.Close()

	// Releasing an address is something the server does. An unreachable server
	// releases nobody, so the request must fail rather than report success.
	response = request(t, http.MethodPost, baseURL+"/api/v1/nodes/"+nodeID+"/fail2ban/unban", map[string]any{
		"jail": "polaris-ssh-bruteforce", "ip": "203.0.113.7",
	}, session, csrfToken)
	if response.StatusCode == http.StatusNoContent {
		t.Fatal("releasing an address on an offline server was reported as done")
	}
	response.Body.Close()

	// The banned list is read from the servers, so an unreachable one is
	// reported as unreadable rather than as holding nobody.
	response = request(t, http.MethodGet, baseURL+"/api/v1/fail2ban/banned", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list banned addresses: got %d", response.StatusCode)
	}
	var banned struct {
		Banned []struct {
			IP string `json:"ip"`
		} `json:"banned"`
	}
	decodeBody(t, response, &banned)
	if len(banned.Banned) != 0 {
		t.Fatalf("an offline server reported bans it cannot know about: %#v", banned.Banned)
	}
}
// createRuleSetClientConfig builds the smallest configuration that can carry a
// RULE-SET rule: one access account, one proxy group holding it, and a
// configuration referencing the shared provider.
func createRuleSetClientConfig(t *testing.T, store *control.Store, server *control.Server, baseURL, session, csrfToken, providerID, providerName string) string {
	t.Helper()
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "rules-node-"+providerID[:6])
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "rules.example.com"); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID: nodeID, Name: "规则接入", Domain: "rules.example.com", ListenAddr: "0.0.0.0", Port: 8443, Enabled: true,
		Spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp", Transport: control.TransportOptions{Type: "ws"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{
		ListenerID: listener.ID, Name: "默认账号", Alias: "规则节点 " + providerID[:6], Enabled: true,
	}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := store.CreateMihomoProxyGroup(t.Context(), control.MihomoProxyGroup{
		Name: "规则分组 " + providerID[:6], Strategy: "select",
		Members: []control.MihomoGroupMember{{Kind: "endpoint", ID: endpoint.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		ID string `json:"id"`
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/mihomo/client-configs", map[string]any{
		"name": "规则配置 " + providerID[:6], "proxy_group_ids": []string{group.ID},
		"rule_mode": "table", "rule_provider_ids": []string{providerID},
		"rules": []map[string]any{
			{"type": "RULE-SET", "value": providerName, "action": "DIRECT"},
			{"type": "MATCH", "action": "DIRECT"},
		},
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create client config: got %d", response.StatusCode)
	}
	decodeBody(t, response, &config)
	return config.ID
}

func execForTest(t *testing.T, store *control.Store, query string, args ...any) {
	t.Helper()
	if err := store.ExecForTest(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func taskExistsForTest(t *testing.T, store *control.Store, taskID string) bool {
	t.Helper()
	count, err := store.CountForTest(t.Context(), `SELECT COUNT(*) FROM tasks WHERE id = ?`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return count > 0
}

func readBodyForTest(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
