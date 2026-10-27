package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}
