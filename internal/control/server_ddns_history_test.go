package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestDDNSHistoryStoreAndAPI(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := t.Context()
	nodeID := newTestNode(t, store, "ddns-test-node", "k")

	// 1. Initially no records
	records, total, err := store.ListDDNSHistory(ctx, DDNSHistoryFilter{}, 1, 20)
	if err != nil {
		t.Fatalf("ListDDNSHistory failed: %v", err)
	}
	if total != 0 || len(records) != 0 {
		t.Fatalf("expected 0 records, got %d (total: %d)", len(records), total)
	}

	// 2. Insert records
	rec1 := DDNSChangeRecord{
		NodeID:      nodeID,
		NodeName:    "ddns-test-node",
		Domain:      "node1.example.com",
		RecordType:  "A",
		RecordID:    "rec-1",
		OldIP:       "1.1.1.1",
		NewIP:       "2.2.2.2",
		TriggerType: "auto",
		Status:      "succeeded",
	}
	if err := store.RecordDDNSChange(ctx, rec1); err != nil {
		t.Fatalf("RecordDDNSChange failed: %v", err)
	}

	rec2 := DDNSChangeRecord{
		NodeID:       nodeID,
		NodeName:     "ddns-test-node",
		Domain:       "node1.example.com",
		RecordType:   "A",
		RecordID:     "rec-1",
		OldIP:        "2.2.2.2",
		NewIP:        "3.3.3.3",
		TriggerType:  "manual",
		Status:       "failed",
		ErrorMessage: "cloudflare api timeout",
	}
	if err := store.RecordDDNSChange(ctx, rec2); err != nil {
		t.Fatalf("RecordDDNSChange rec2 failed: %v", err)
	}

	node2ID := newTestNode(t, store, "other-node", "k2")
	rec3 := DDNSChangeRecord{
		NodeID:      node2ID,
		NodeName:    "other-node",
		Domain:      "node2.example.com",
		RecordType:  "AAAA",
		RecordID:    "rec-2",
		OldIP:       "2001:db8::1",
		NewIP:       "2001:db8::2",
		TriggerType: "auto",
		Status:      "succeeded",
	}
	if err := store.RecordDDNSChange(ctx, rec3); err != nil {
		t.Fatalf("RecordDDNSChange rec3 failed: %v", err)
	}

	// 3. Test listing all records
	records, total, err = store.ListDDNSHistory(ctx, DDNSHistoryFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("ListDDNSHistory failed: %v", err)
	}
	if total != 3 || len(records) != 3 {
		t.Fatalf("expected 3 records, got %d (total: %d)", len(records), total)
	}

	// 4. Test filtering by node_id
	records, total, err = store.ListDDNSHistory(ctx, DDNSHistoryFilter{NodeID: nodeID}, 1, 10)
	if err != nil {
		t.Fatalf("ListDDNSHistory by nodeID failed: %v", err)
	}
	if total != 2 || len(records) != 2 {
		t.Fatalf("expected 2 records for nodeID, got %d", total)
	}

	// 5. Test filtering by status
	records, total, err = store.ListDDNSHistory(ctx, DDNSHistoryFilter{Status: "failed"}, 1, 10)
	if err != nil {
		t.Fatalf("ListDDNSHistory by status failed: %v", err)
	}
	if total != 1 || records[0].ErrorMessage != "cloudflare api timeout" {
		t.Fatalf("expected 1 failed record with error message, got %d (%+v)", total, records)
	}

	// 6. Test filtering by keyword
	records, total, err = store.ListDDNSHistory(ctx, DDNSHistoryFilter{Keyword: "2001:db8"}, 1, 10)
	if err != nil {
		t.Fatalf("ListDDNSHistory by keyword failed: %v", err)
	}
	if total != 1 || records[0].Domain != "node2.example.com" {
		t.Fatalf("expected 1 record matching keyword 2001:db8, got %d", total)
	}

	// 7. Test HTTP API
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	admin, _, err := store.CreateOperator(ctx, "admin@example.com", "Password123!", "admin")
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.createSession(ctx, admin.ID, admin.Role, false)
	if err != nil {
		t.Fatal(err)
	}

	handler := server.BrowserHandler()

	// GET /api/v1/ddns/history
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ddns/history?keyword=example.com", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	req.Header.Set("X-CSRF-Token", session.CSRFToken)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/ddns/history returned %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Records []DDNSChangeRecord `json:"records"`
		Total   int                `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("expected total 3, got %d", resp.Total)
	}

	// DELETE /api/v1/ddns/history/{id}
	targetID := resp.Records[0].ID
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/ddns/history/"+targetID, nil)
	delReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	delReq.Header.Set("X-CSRF-Token", session.CSRFToken)
	wDel := httptest.NewRecorder()
	handler.ServeHTTP(wDel, delReq)
	if wDel.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/ddns/history/%s returned %d: %s", targetID, wDel.Code, wDel.Body.String())
	}

	records, total, _ = store.ListDDNSHistory(ctx, DDNSHistoryFilter{}, 1, 10)
	if total != 2 {
		t.Fatalf("expected 2 records after single delete, got %d", total)
	}

	// DELETE /api/v1/ddns/history (clear all)
	clearReq := httptest.NewRequest(http.MethodDelete, "/api/v1/ddns/history", nil)
	clearReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	clearReq.Header.Set("X-CSRF-Token", session.CSRFToken)
	wClear := httptest.NewRecorder()
	handler.ServeHTTP(wClear, clearReq)
	if wClear.Code != http.StatusOK {
		t.Fatalf("DELETE /api/v1/ddns/history returned %d: %s", wClear.Code, wClear.Body.String())
	}

	records, total, _ = store.ListDDNSHistory(ctx, DDNSHistoryFilter{}, 1, 10)
	if total != 0 {
		t.Fatalf("expected 0 records after clear all, got %d", total)
	}
}

func TestServerDDNSLiveRecordCreation(t *testing.T) {
	var mu sync.Mutex
	recordContent := "1.1.1.1"

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

	ctx := t.Context()
	if err := store.SetCloudflareSettings(ctx, "zone-123", "example.com", "mock-token-xyz"); err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	nodeID := newTestNode(t, store, "nat-test-box", "k")
	enabled := true
	recID := "rec-abc"
	domain := "nat.example.com"
	if err := store.UpdateNodeWithDDNS(ctx, nodeID, "NAT Test Box", "nat.example.com", &enabled, &recID, &domain); err != nil {
		t.Fatal(err)
	}

	// 1. Initial IP change: 1.1.1.1 -> 198.51.100.10
	if err := server.executeNodeDDNS(ctx, nodeID, "198.51.100.10"); err != nil {
		t.Fatalf("executeNodeDDNS failed: %v", err)
	}

	records, total, err := store.ListDDNSHistory(ctx, DDNSHistoryFilter{NodeID: nodeID}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("expected 1 history record, got %d", total)
	}
	if records[0].OldIP != "1.1.1.1" || records[0].NewIP != "198.51.100.10" {
		t.Fatalf("unexpected record: %+v", records[0])
	}
	if records[0].Status != "succeeded" || records[0].TriggerType != "auto" {
		t.Fatalf("unexpected status/trigger: %+v", records[0])
	}

	// 2. Same IP -> should NOT create duplicate history record
	if err := server.executeNodeDDNS(ctx, nodeID, "198.51.100.10"); err != nil {
		t.Fatalf("executeNodeDDNS 2 failed: %v", err)
	}
	_, total, _ = store.ListDDNSHistory(ctx, DDNSHistoryFilter{NodeID: nodeID}, 1, 10)
	if total != 1 {
		t.Fatalf("expected still 1 history record when IP unchanged, got %d", total)
	}

	// 3. Manual sync with new IP -> should record trigger_type="manual"
	node, _ := store.GetNode(ctx, nodeID)
	if err := server.syncNodeDDNSNow(ctx, node, "203.0.113.88", "op-1"); err != nil {
		t.Fatalf("syncNodeDDNSNow failed: %v", err)
	}

	records, total, _ = store.ListDDNSHistory(ctx, DDNSHistoryFilter{NodeID: nodeID}, 1, 10)
	if total != 2 {
		t.Fatalf("expected 2 history records, got %d", total)
	}
	if records[0].NewIP != "203.0.113.88" || records[0].TriggerType != "manual" {
		t.Fatalf("expected newest record with 203.0.113.88 manual, got %+v", records[0])
	}
}
