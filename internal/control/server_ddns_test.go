package control

import (
	"bytes"
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

func waitForDDNS(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for DDNS reconciliation")
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
	waitForDDNS(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return recordContent == "203.0.113.200" && patchCalls == 5
	})

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
	waitForDDNS(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return recordContent == "203.0.113.200" && patchCalls == 6
	})

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
	waitForDDNS(t, func() bool {
		server.ddnsReconcileMu.Lock()
		defer server.ddnsReconcileMu.Unlock()
		state := server.ddnsReconcileState[nodeID]
		return state != nil && state.failures == 1 && !state.nextRetryAt.IsZero()
	})

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
	waitForDDNS(t, func() bool {
		server.ddnsReconcileMu.Lock()
		defer server.ddnsReconcileMu.Unlock()
		return state.failures == 0 && state.lastIP == "203.0.113.77"
	})

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

func TestNodeDDNSMultiDomainAndMasterDetection(t *testing.T) {
	var mu sync.Mutex
	rec1Content := "1.1.1.1"
	rec2Content := "1.1.1.1"
	patchedRecords := make(map[string]string)

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-multi":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "zone-multi", "name": "example.com", "status": "active"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-multi/dns_records/rec-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "rec-1", "type": "A", "name": "dom1.example.com", "content": rec1Content, "ttl": 60, "proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-multi/dns_records/rec-1":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			rec1Content = req["content"].(string)
			patchedRecords["rec-1"] = rec1Content
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "rec-1", "type": "A", "name": "dom1.example.com", "content": rec1Content, "ttl": 60, "proxied": false,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-multi/dns_records/rec-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "rec-2", "type": "A", "name": "dom2.example.com", "content": rec2Content, "ttl": 60, "proxied": false,
				},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-multi/dns_records/rec-2":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			rec2Content = req["content"].(string)
			patchedRecords["rec-2"] = rec2Content
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result": map[string]any{
					"id": "rec-2", "type": "A", "name": "dom2.example.com", "content": rec2Content, "ttl": 60, "proxied": false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockCF.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	oldAPI := cloudflareAPI
	cloudflareAPI = mockCF.URL
	defer func() { cloudflareAPI = oldAPI }()

	if err := store.SetCloudflareSettings(t.Context(), "zone-multi", "example.com", "mock-token-xyz"); err != nil {
		t.Fatalf("SetCloudflareSettings failed: %v", err)
	}

	nodeID := newTestNode(t, store, "multi-ddns-node", "d")
	enabled := true
	recIDs := []string{"rec-1", "rec-2"}
	domains := []string{"dom1.example.com", "dom2.example.com"}

	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "Master Node", "dom1.example.com", &enabled, nil, nil, recIDs, domains); err != nil {
		t.Fatalf("UpdateNodeWithDDNS failed: %v", err)
	}

	node, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.DDNSRecordIDs) != 2 || node.DDNSRecordIDs[0] != "rec-1" || node.DDNSRecordIDs[1] != "rec-2" {
		t.Fatalf("unexpected DDNSRecordIDs: %v", node.DDNSRecordIDs)
	}
	if len(node.DDNSDomains) != 2 || node.DDNSDomains[0] != "dom1.example.com" || node.DDNSDomains[1] != "dom2.example.com" {
		t.Fatalf("unexpected DDNSDomains: %v", node.DDNSDomains)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	// Execute DDNS for node with new IP 203.0.113.88
	if err := server.executeNodeDDNS(t.Context(), nodeID, "203.0.113.88"); err != nil {
		t.Fatalf("executeNodeDDNS failed: %v", err)
	}

	mu.Lock()
	p1 := patchedRecords["rec-1"]
	p2 := patchedRecords["rec-2"]
	mu.Unlock()

	if p1 != "203.0.113.88" || p2 != "203.0.113.88" {
		t.Fatalf("expected both records to be patched with 203.0.113.88, got rec-1: %s, rec-2: %s", p1, p2)
	}

	// Verify node last observed IP is updated
	updatedNode, _ := store.GetNode(t.Context(), nodeID)
	if updatedNode.LastObservedIP != "203.0.113.88" {
		t.Fatalf("expected LastObservedIP 203.0.113.88, got %s", updatedNode.LastObservedIP)
	}
}

// A node may bind records from different Cloudflare zones. Every record must be
// found in its own zone and updated there.
func TestExecuteNodeDDNSAcrossZones(t *testing.T) {
	var mu sync.Mutex
	contents := map[string]string{"rec-a": "192.0.2.1", "rec-b": "192.0.2.2"}
	patched := map[string]string{}

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		zoneID := strings.Split(strings.TrimPrefix(r.URL.Path, "/zones/"), "/")[0]
		var recordID string
		switch r.URL.Path {
		case "/zones/zone-a/dns_records/rec-a":
			recordID = "rec-a"
		case "/zones/zone-b/dns_records/rec-b":
			recordID = "rec-b"
		}
		if recordID == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if content, ok := body["content"].(string); ok {
				contents[recordID] = content
				patched[recordID] = content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"result": map[string]any{
				"id": recordID, "type": "A", "name": recordID + "." + zoneID + ".com",
				"content": contents[recordID], "ttl": 60, "proxied": false,
			},
		})
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

	if err := store.SetCloudflareSettings(t.Context(), "zone-a", "example.com", "token-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCloudflareSettings(t.Context(), "zone-b", "example.net", "token-b"); err != nil {
		t.Fatal(err)
	}

	nodeID := newTestNode(t, store, "cross-zone-node", "c")
	enabled := true
	recordIDs := []string{"rec-a", "rec-b"}
	domains := []string{"a.example.com", "b.example.net"}
	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "Cross Zone", domains[0], &enabled, nil, nil, recordIDs, domains); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.executeNodeDDNS(t.Context(), nodeID, "203.0.113.77"); err != nil {
		t.Fatalf("executeNodeDDNS failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if patched["rec-a"] != "203.0.113.77" || patched["rec-b"] != "203.0.113.77" {
		t.Fatalf("expected both zones patched, got %#v", patched)
	}
}

func TestServerDDNSMultiIPBindingAndPerRecordOverrides(t *testing.T) {
	var (
		mu      sync.Mutex
		patched = map[string]string{}
	)
	recordDefs := map[string]map[string]any{
		"rec-a1":   {"id": "rec-a1", "type": "A", "name": "a1.example.com", "content": "198.51.100.1", "ttl": 60, "proxied": false},
		"rec-a2":   {"id": "rec-a2", "type": "A", "name": "a2.example.com", "content": "198.51.100.1", "ttl": 60, "proxied": false},
		"rec-aaaa": {"id": "rec-aaaa", "type": "AAAA", "name": "v6.example.com", "content": "2001:db8::1", "ttl": 60, "proxied": false},
	}

	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-multi":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "zone-multi", "name": "example.com"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/zones/zone-multi/dns_records/"):
			recID := strings.TrimPrefix(r.URL.Path, "/zones/zone-multi/dns_records/")
			rec, ok := recordDefs[recID]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rec})
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/zones/zone-multi/dns_records/"):
			recID := strings.TrimPrefix(r.URL.Path, "/zones/zone-multi/dns_records/")
			rec, ok := recordDefs[recID]
			if !ok {
				http.NotFound(w, r)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			content, _ := body["content"].(string)
			patched[recID] = content
			rec["content"] = content
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": rec})
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

	if err := store.SetCloudflareSettings(t.Context(), "zone-multi", "example.com", "token"); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := store.createOperator(t.Context(), "admin_ddns", "AdminPass123456!", "admin", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.createSession(t.Context(), admin.ID, admin.Role, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.BrowserHandler()
	doRequest := func(method, path string, body any) *httptest.ResponseRecorder {
		var reqBody []byte
		if body != nil {
			reqBody, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", session.CSRFToken)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	nodeID := newTestNode(t, store, "multi-ip-node", "m")

	// Simulate multi-IP + dual-stack agent status update where primary control IP is 198.51.100.1.
	if err := store.UpdateNodeIdentityWithIPs(t.Context(), nodeID, "v1", "linux", "debian", "amd64", "", "",
		[]string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "2001:db8::2"},
		map[string]string{}); err != nil {
		t.Fatal(err)
	}
	server.controlMu.Lock()
	server.controls[nodeID] = &controlSession{remoteAddr: "198.51.100.1"}
	server.controlMu.Unlock()

	// Reject invalid bind IP via PUT /api/v1/nodes/{id}.
	badResp := doRequest(http.MethodPut, "/api/v1/nodes/"+nodeID, map[string]any{
		"name":         "multi-ip-node",
		"ddns_enabled": true,
		"ddns_bind_ip": "not-an-ip",
	})
	if badResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid ddns_bind_ip, got %d: %s", badResp.Code, badResp.Body.String())
	}

	// Set node-level ddns_bind_ip=198.51.100.2 plus per-record overrides for rec-a2 and rec-aaaa.
	updResp := doRequest(http.MethodPut, "/api/v1/nodes/"+nodeID, map[string]any{
		"name":            "multi-ip-node",
		"ddns_enabled":    true,
		"ddns_record_ids": []string{"rec-a1", "rec-a2", "rec-aaaa"},
		"ddns_domains":    []string{"a1.example.com", "a2.example.com", "v6.example.com"},
		"ddns_bind_ip":    "198.51.100.2",
		"ddns_record_ips": map[string]string{
			"rec-a2":   "198.51.100.3",
			"rec-aaaa": "2001:db8::2",
		},
	})
	if updResp.Code != http.StatusOK {
		t.Fatalf("expected 200 updating multi-IP DDNS settings, got %d: %s", updResp.Code, updResp.Body.String())
	}

	syncResp := doRequest(http.MethodPost, "/api/v1/nodes/"+nodeID+"/ddns/sync", map[string]any{})
	if syncResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on manual multi-IP ddns sync, got %d: %s", syncResp.Code, syncResp.Body.String())
	}

	mu.Lock()
	gotA1, gotA2, gotAAAA := patched["rec-a1"], patched["rec-a2"], patched["rec-aaaa"]
	mu.Unlock()
	if gotA1 != "198.51.100.2" {
		t.Errorf("expected rec-a1 bound to node ddns_bind_ip 198.51.100.2 (not active IP 198.51.100.1), got %q", gotA1)
	}
	if gotA2 != "198.51.100.3" {
		t.Errorf("expected rec-a2 override 198.51.100.3, got %q", gotA2)
	}
	if gotAAAA != "2001:db8::2" {
		t.Errorf("expected rec-aaaa IPv6 override 2001:db8::2, got %q", gotAAAA)
	}

	persisted, err := store.GetNode(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.DDNSBindIP != "198.51.100.2" || persisted.DDNSRecordIPs["rec-aaaa"] != "2001:db8::2" {
		t.Errorf("unexpected persisted DDNS binding: bind=%q map=%#v", persisted.DDNSBindIP, persisted.DDNSRecordIPs)
	}
}

func TestServerDDNSLocalBindIPTracksDynamicNATMapping(t *testing.T) {
	var (
		mu            sync.Mutex
		recordContent = "222.1.2.1"
	)
	mockCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-nat":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "zone-nat", "name": "example.com"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-nat/dns_records/rec-nat":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "rec-nat", "type": "A", "name": "nat2.example.com", "content": recordContent, "ttl": 60, "proxied": false},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone-nat/dns_records/rec-nat":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			recordContent, _ = body["content"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"result":  map[string]any{"id": "rec-nat", "type": "A", "name": "nat2.example.com", "content": recordContent, "ttl": 60, "proxied": false},
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

	if err := store.SetCloudflareSettings(t.Context(), "zone-nat", "example.com", "token"); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	nodeID := newTestNode(t, store, "multi-nic-nat", "n")
	enabled := true
	recID := "rec-nat"
	domain := "nat2.example.com"
	if err := store.UpdateNodeWithDDNS(t.Context(), nodeID, "Multi NIC NAT", "nat2.example.com", &enabled, &recID, &domain); err != nil {
		t.Fatal(err)
	}
	// Operator binds DDNS to local NIC2 IP (10.0.0.2) rather than control session NIC1 (198.51.100.1).
	bindLocal := "10.0.0.2"
	if err := store.UpdateNodeDDNSBinding(t.Context(), nodeID, &bindLocal, nil); err != nil {
		t.Fatal(err)
	}

	// Primary control socket stays constant on 198.51.100.1; NIC2 public NAT IP is 222.1.2.20.
	server.controlMu.Lock()
	server.controls[nodeID] = &controlSession{remoteAddr: "198.51.100.1"}
	server.controlMu.Unlock()
	if err := store.UpdateNodeIdentityWithIPs(t.Context(), nodeID, "v1", "linux", "debian", "amd64", "", "",
		[]string{"222.1.2.10", "222.1.2.20"},
		map[string]string{"222.1.2.10": "10.0.0.1", "222.1.2.20": "10.0.0.2"}); err != nil {
		t.Fatal(err)
	}

	server.reconcileNodeDDNS(t.Context(), nodeID)
	waitForDDNS(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return recordContent == "222.1.2.20"
	})

	// Later heartbeat: NIC2 NAT public IP rotates to 222.1.2.99 while control IP 198.51.100.1 is unchanged.
	if err := store.UpdateNodeIdentityWithIPs(t.Context(), nodeID, "v1", "linux", "debian", "amd64", "", "",
		[]string{"222.1.2.10", "222.1.2.99"},
		map[string]string{"222.1.2.10": "10.0.0.1", "222.1.2.99": "10.0.0.2"}); err != nil {
		t.Fatal(err)
	}
	server.reconcileNodeDDNS(t.Context(), nodeID)
	waitForDDNS(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return recordContent == "222.1.2.99"
	})
}
