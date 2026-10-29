package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUpdateNodeWithDDNSStore(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	nodeID := newTestNode(t, store, "ddns-test-node", "d")

	enabled := true
	recordID := "rec-test-123"
	domain := "nat.example.com"

	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "NAT Node", "proxy.example.com", &enabled, &recordID, &domain); err != nil {
		t.Fatalf("UpdateNodeWithDDNS failed: %v", err)
	}

	node, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatalf("GetNode failed: %v", err)
	}
	if !node.DDNSEnabled {
		t.Fatal("expected DDNSEnabled to be true")
	}
	if node.DDNSRecordID != recordID {
		t.Fatalf("expected DDNSRecordID=%q, got %q", recordID, node.DDNSRecordID)
	}
	if node.DDNSDomain != domain {
		t.Fatalf("expected DDNSDomain=%q, got %q", domain, node.DDNSDomain)
	}

	// Verify SetNodeLastObservedIP
	if err := store.SetNodeLastObservedIP(t.Context(), nodeID, "1.2.3.4"); err != nil {
		t.Fatalf("SetNodeLastObservedIP failed: %v", err)
	}
	node, err = store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.LastObservedIP != "1.2.3.4" {
		t.Fatalf("expected LastObservedIP=1.2.3.4, got %q", node.LastObservedIP)
	}

	// Changing record ID should reset last_observed_ip so an immediate sync is forced
	newRecordID := "rec-test-456"
	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "NAT Node", "proxy.example.com", &enabled, &newRecordID, &domain); err != nil {
		t.Fatal(err)
	}
	node, err = store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.LastObservedIP != "" {
		t.Fatalf("expected LastObservedIP to be cleared, got %q", node.LastObservedIP)
	}
}

func TestServerDDNSAutoUpdateAndManualSync(t *testing.T) {
	var mu sync.Mutex
	recordContent := "10.0.0.1"
	patchCalls := 0

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "zone-123", "name": "example.com", "status": "active"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123/dns_records/rec-abc":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-abc",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
					"proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-123/dns_records/rec-abc":
			patchCalls++
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if c, ok := req["content"].(string); ok {
				recordContent = c
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-abc",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
					"proxied": false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCF.Close()

	oldAPI := cloudflareAPI
	cloudflareAPI = mockCF.URL
	defer func() { cloudflareAPI = oldAPI }()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Configure Cloudflare credentials
	if err := store.SetCloudflareSettings(t.Context(), "zone-123", "example.com", "mock-token-xyz"); err != nil {
		t.Fatalf("SetCloudflareSettings failed: %v", err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	nodeID := newTestNode(t, store, "nat-box", "k")

	// Enable DDNS on node
	enabled := true
	recID := "rec-abc"
	domain := "nat.example.com"
	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "NAT Box", "nat.example.com", &enabled, &recID, &domain); err != nil {
		t.Fatal(err)
	}

	// 1. Initial IP observed (e.g. 198.51.100.10)
	server.triggerNodeDDNS(t.Context(), nodeID, "198.51.100.10")

	// Wait briefly for goroutine/updates
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "198.51.100.10" {
		t.Fatalf("expected CF record content to be 198.51.100.10, got %s", recordContent)
	}
	if patchCalls != 1 {
		t.Fatalf("expected 1 patch call, got %d", patchCalls)
	}
	mu.Unlock()

	// Check node last observed IP
	node, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.LastObservedIP != "198.51.100.10" {
		t.Fatalf("expected node LastObservedIP=198.51.100.10, got %s", node.LastObservedIP)
	}

	// 2. Same IP observed again -> should NOT trigger another PATCH call
	server.triggerNodeDDNS(t.Context(), nodeID, "198.51.100.10")
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if patchCalls != 1 {
		t.Fatalf("expected still 1 patch call, got %d", patchCalls)
	}
	mu.Unlock()

	// 3. IP changes to 203.0.113.88 -> should trigger CF update
	server.triggerNodeDDNS(t.Context(), nodeID, "203.0.113.88")
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "203.0.113.88" {
		t.Fatalf("expected CF record content to be 203.0.113.88, got %s", recordContent)
	}
	if patchCalls != 2 {
		t.Fatalf("expected 2 patch calls, got %d", patchCalls)
	}
	mu.Unlock()

	// 4. Test manual syncNow
	err = server.syncNodeDDNSNow(t.Context(), node, "203.0.113.99", store.SystemOperatorID(t.Context()))
	if err != nil {
		t.Fatalf("syncNodeDDNSNow failed: %v", err)
	}

	mu.Lock()
	if recordContent != "203.0.113.99" {
		t.Fatalf("expected CF record content to be 203.0.113.99, got %s", recordContent)
	}
	if patchCalls != 3 {
		t.Fatalf("expected 3 patch calls, got %d", patchCalls)
	}
	mu.Unlock()

	// 5. Test self-healing when database already has LastObservedIP matching currentIP
	// (e.g. from a past premature write), but Cloudflare record content is out of date.
	mu.Lock()
	recordContent = "198.51.100.1" // Stale IP in Cloudflare
	mu.Unlock()

	// Corrupt local store to match currentIP ahead of time
	if err := store.SetNodeLastObservedIP(t.Context(), nodeID, "203.0.113.150"); err != nil {
		t.Fatal(err)
	}

	server.triggerNodeDDNS(t.Context(), nodeID, "203.0.113.150")
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "203.0.113.150" {
		t.Fatalf("expected CF record content to self-heal to 203.0.113.150, got %s", recordContent)
	}
	if patchCalls != 4 {
		t.Fatalf("expected 4 patch calls after self-healing, got %d", patchCalls)
	}
	mu.Unlock()

	// 6. Test continuous DDNS reconciliation during ongoing status reports
	// Attach an active session for the node with IP 203.0.113.200
	server.controlMu.Lock()
	server.controls[nodeID] = &controlSession{remoteAddr: "203.0.113.200"}
	server.controlMu.Unlock()

	// Agent reports status -> reconcileNodeDDNS runs
	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "203.0.113.200" {
		t.Fatalf("expected CF record content to be 203.0.113.200 from continuous reconciliation, got %s", recordContent)
	}
	if patchCalls != 5 {
		t.Fatalf("expected 5 patch calls after continuous reconciliation, got %d", patchCalls)
	}
	mu.Unlock()

	// Consecutive report with same IP -> no redundant PATCH
	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	if patchCalls != 5 {
		t.Fatalf("expected still 5 patch calls on repeated reporting, got %d", patchCalls)
	}
	mu.Unlock()

	// Cloudflare drifts externally -> periodic check corrects it during reporting
	mu.Lock()
	recordContent = "1.2.3.4"
	mu.Unlock()

	server.ddnsReconcileMu.Lock()
	if st := server.ddnsReconcileState[nodeID]; st != nil {
		st.lastCheckedAt = time.Now().Add(-10 * time.Minute) // simulate 10 minutes elapsed
	}
	server.ddnsReconcileMu.Unlock()

	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "203.0.113.200" {
		t.Fatalf("expected CF record content to drift-correct back to 203.0.113.200, got %s", recordContent)
	}
	if patchCalls != 6 {
		t.Fatalf("expected 6 patch calls after drift correction, got %d", patchCalls)
	}
	mu.Unlock()
}

func TestServerDDNSContinuousReconciliationBackoffRetry(t *testing.T) {
	var mu sync.Mutex
	failCloudflare := true
	recordContent := "198.51.100.1"

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if failCloudflare {
			http.Error(w, `{"success":false,"errors":[{"message":"temporary cf error"}]}`, http.StatusInternalServerError)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "zone-1", "name": "example.com", "status": "active"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records/rec-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-1",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-1/dns_records/rec-1":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if c, ok := req["content"].(string); ok {
				recordContent = c
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-1",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCF.Close()

	oldAPI := cloudflareAPI
	cloudflareAPI = mockCF.URL
	defer func() { cloudflareAPI = oldAPI }()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SetCloudflareSettings(t.Context(), "zone-1", "example.com", "token"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	nodeID := newTestNode(t, store, "nat-node-retry", "k")
	enabled := true
	recID := "rec-1"
	domain := "nat.example.com"
	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "NAT Node Retry", "nat.example.com", &enabled, &recID, &domain); err != nil {
		t.Fatal(err)
	}

	server.controlMu.Lock()
	server.controls[nodeID] = &controlSession{remoteAddr: "203.0.113.77"}
	server.controlMu.Unlock()

	// 1. First status report arrives while Cloudflare is failing
	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(100 * time.Millisecond)

	server.ddnsReconcileMu.Lock()
	state := server.ddnsReconcileState[nodeID]
	if state == nil || state.failures != 1 {
		t.Fatalf("expected state with 1 failure, got %#v", state)
	}
	if state.nextRetryAt.IsZero() {
		t.Fatal("expected nextRetryAt to be set after failure")
	}
	server.ddnsReconcileMu.Unlock()

	// 2. Immediate second report during backoff window must not hammer Cloudflare
	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(50 * time.Millisecond)

	server.ddnsReconcileMu.Lock()
	if state.failures != 1 {
		t.Fatalf("expected failures still 1 during backoff, got %d", state.failures)
	}
	server.ddnsReconcileMu.Unlock()

	// 3. Cloudflare recovers! Advance past backoff window to simulate next retry
	mu.Lock()
	failCloudflare = false
	mu.Unlock()

	server.ddnsReconcileMu.Lock()
	state.nextRetryAt = time.Now().Add(-1 * time.Second)
	server.ddnsReconcileMu.Unlock()

	// Agent's next status report arrives -> automatically retries and heals!
	server.reconcileNodeDDNS(t.Context(), nodeID)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if recordContent != "203.0.113.77" {
		t.Fatalf("expected CF record to recover to 203.0.113.77, got %s", recordContent)
	}
	mu.Unlock()

	server.ddnsReconcileMu.Lock()
	if state.failures != 0 {
		t.Fatalf("expected failures reset to 0 after recovery, got %d", state.failures)
	}
	if state.lastIP != "203.0.113.77" {
		t.Fatalf("expected lastIP=203.0.113.77, got %s", state.lastIP)
	}
	server.ddnsReconcileMu.Unlock()
}

func TestServerDDNSChainedNodesAutoDispatch(t *testing.T) {
	var mu sync.Mutex
	recordContent := "198.51.100.10"

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "zone-123", "name": "example.com", "status": "active"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-123/dns_records/rec-nat":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-nat",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
					"proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-123/dns_records/rec-nat":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if c, ok := req["content"].(string); ok {
				recordContent = c
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id":      "rec-nat",
					"type":    "A",
					"name":    "nat.example.com",
					"content": recordContent,
					"ttl":     60,
					"proxied": false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCF.Close()

	oldAPI := cloudflareAPI
	cloudflareAPI = mockCF.URL
	defer func() { cloudflareAPI = oldAPI }()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.SetCloudflareSettings(t.Context(), "zone-123", "example.com", "mock-token-xyz"); err != nil {
		t.Fatalf("SetCloudflareSettings failed: %v", err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	node1ID := createTestNode(t, store, "entry-node-a", "198.51.100.1")
	node2ID := createTestNode(t, store, "nat-node-b", "198.51.100.10")

	// Enable DDNS on Node 2 (NAT Node B)
	enabled := true
	recID := "rec-nat"
	domain := "nat.example.com"
	if err := store.UpdateNodeWithDDNS(ctx, node2ID, "NAT Node B", "nat.example.com", &enabled, &recID, &domain); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeLastObservedIP(ctx, node2ID, "198.51.100.10"); err != nil {
		t.Fatal(err)
	}

	// Listener on Node 2 (NAT Node B)
	listener2, err := store.CreateListener(ctx, Listener{
		NodeID:     node2ID,
		Name:       "node2-service",
		Domain:     "nat.example.com",
		Port:       443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "httpupgrade", Path: "/ws"},
		},
	})
	if err != nil {
		t.Fatalf("create listener 2: %v", err)
	}

	user2, err := store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener2.ID,
		Name:       "user2",
		Enabled:    true,
	}, EndpointCredentials{
		UUID: "22222222-2222-2222-2222-222222222222",
	})
	if err != nil {
		t.Fatalf("create endpoint 2: %v", err)
	}

	// Listener on Node 1 (Entry Node A)
	listener1, err := store.CreateListener(ctx, Listener{
		NodeID:     node1ID,
		Name:       "node1-entry",
		Domain:     "entry.example.com",
		Port:       8443,
		ListenAddr: "0.0.0.0",
		Enabled:    true,
		Spec: ProtocolSpec{
			Protocol:  "vless",
			Network:   "tcp",
			TLS:       TLSOptions{Enabled: true},
			Transport: TransportOptions{Type: "httpupgrade", Path: "/in"},
		},
	})
	if err != nil {
		t.Fatalf("create listener 1: %v", err)
	}

	// Endpoint on Node 1 chaining to user2 on Node 2
	_, err = store.CreateEndpoint(ctx, Endpoint{
		ListenerID: listener1.ID,
		Name:       "user1",
		Enabled:    true,
		OutboundID: "chain:" + user2.ID,
	}, EndpointCredentials{
		UUID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("create chained endpoint 1: %v", err)
	}

	// Initial compile of Node 1 MUST use the domain name nat.example.com
	config1, hash1, err := store.CompileNodeConfig(ctx, node1ID)
	if err != nil {
		t.Fatalf("CompileNodeConfig node 1: %v", err)
	}
	if !strings.Contains(config1, "nat.example.com") {
		t.Fatalf("expected node1 config to contain domain nat.example.com: %s", config1)
	}
	if strings.Contains(config1, "198.51.100.10") {
		t.Fatalf("prohibited from containing raw IP in node1 config: %s", config1)
	}

	// Now Node 2's NAT IP changes to 198.51.100.99!
	// Execute DDNS update
	if err := server.executeNodeDDNS(ctx, node2ID, "198.51.100.99"); err != nil {
		t.Fatalf("executeNodeDDNS failed: %v", err)
	}

	// Check that Cloudflare was updated
	mu.Lock()
	if recordContent != "198.51.100.99" {
		t.Fatalf("expected CF record updated to 198.51.100.99, got %s", recordContent)
	}
	mu.Unlock()

	// Verify that Node 1's sing-box configuration did NOT change!
	config2, hash2, err := store.CompileNodeConfig(ctx, node1ID)
	if err != nil {
		t.Fatalf("CompileNodeConfig node 1 after ip change: %v", err)
	}
	if hash1 != hash2 || config1 != config2 {
		t.Fatalf("Node 1 configuration must not change! hash1=%s, hash2=%s", hash1, hash2)
	}

	// Check that Node 1 (chained node) was automatically dispatched a "dns.update" instruction!
	tasks, _, err := store.ListTasks(ctx, node1ID, "", 1, 20)
	if err != nil {
		t.Fatalf("ListTasks for node1 failed: %v", err)
	}
	var dnsUpdateTask *Task
	for i := range tasks {
		if tasks[i].Kind == "singbox.apply_config" {
			t.Fatalf("unexpected singbox.apply_config task dispatched for node1; config must not change")
		}
		if tasks[i].Kind == "dns.update" {
			dnsUpdateTask = &tasks[i]
		}
	}
	if dnsUpdateTask == nil {
		t.Fatalf("expected a dns.update task dispatched for node1, got %d tasks", len(tasks))
	}

	// Verify task payload contains Node 2's new NAT IP 198.51.100.99 and target domain
	fullTask, err := store.TaskByID(ctx, dnsUpdateTask.ID)
	if err != nil {
		t.Fatalf("TaskByID failed: %v", err)
	}
	var payload struct {
		IP      string   `json:"ip"`
		Domains []string `json:"domains"`
	}
	if err := json.Unmarshal([]byte(fullTask.Payload), &payload); err != nil {
		t.Fatalf("unmarshal dns.update payload: %v", err)
	}
	if payload.IP != "198.51.100.99" {
		t.Errorf("expected payload IP 198.51.100.99, got: %s", payload.IP)
	}
	foundDomain := false
	for _, d := range payload.Domains {
		if d == "nat.example.com" {
			foundDomain = true
			break
		}
	}
	if !foundDomain {
		t.Errorf("expected payload domains to include nat.example.com, got: %v", payload.Domains)
	}
}

