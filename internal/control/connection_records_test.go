package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConnectionRecordsCRUDAndFilters(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	records := []ConnectionRecord{
		{
			ID:             "node-1:c-1",
			NodeID:         "node-1",
			NodeName:       "HK-01",
			ConnectionID:   "c-1",
			SourceIP:       "192.168.1.100",
			SourcePort:     "51234",
			SourceLocation: "China Guangdong Shenzhen",
			Destination:    "1.1.1.1:443",
			Host:           "one.one.one.one",
			Network:        "tcp",
			User:           "alice-iphone",
			ListenerName:   "vless-in",
			OutboundName:   "DIRECT",
			Upload:         1000,
			Download:       5000,
			StartedAt:      now.Add(-10 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:             "node-1:c-2",
			NodeID:         "node-1",
			NodeName:       "HK-01",
			ConnectionID:   "c-2",
			SourceIP:       "192.168.1.101",
			SourcePort:     "51235",
			SourceLocation: "China Beijing",
			Destination:    "8.8.8.8:53",
			Host:           "dns.google",
			Network:        "udp",
			User:           "bob-macbook",
			ListenerName:   "trojan-in",
			OutboundName:   "PROXY-WARP",
			Upload:         200,
			Download:       800,
			StartedAt:      now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		{
			ID:             "node-2:c-3",
			NodeID:         "node-2",
			NodeName:       "JP-01",
			ConnectionID:   "c-3",
			SourceIP:       "10.0.0.50",
			SourcePort:     "49100",
			SourceLocation: "Japan Tokyo",
			Destination:    "142.250.190.46:443",
			Host:           "google.com",
			Network:        "tcp",
			User:           "alice-iphone",
			ListenerName:   "hysteria2-in",
			OutboundName:   "DIRECT",
			Upload:         3000,
			Download:       15000,
			StartedAt:      now.Add(-2 * time.Minute).Format(time.RFC3339),
		},
	}

	if err := store.SaveConnectionRecords(ctx, records); err != nil {
		t.Fatalf("SaveConnectionRecords failed: %v", err)
	}

	// 1. List all records
	all, total, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords failed: %v", err)
	}
	if total != 3 || len(all) != 3 {
		t.Fatalf("expected 3 records, got total=%d len=%d", total, len(all))
	}

	// 2. Filter by IP
	ipFiltered, totalIP, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{SourceIP: "192.168.1.100"}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords by IP failed: %v", err)
	}
	if totalIP != 1 || len(ipFiltered) != 1 || ipFiltered[0].SourceIP != "192.168.1.100" {
		t.Fatalf("expected 1 record for IP 192.168.1.100, got total=%d", totalIP)
	}

	// 3. Filter by User
	userFiltered, totalUser, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{User: "alice"}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords by User failed: %v", err)
	}
	if totalUser != 2 || len(userFiltered) != 2 {
		t.Fatalf("expected 2 records for user alice, got total=%d", totalUser)
	}

	// 4. Filter by NodeID
	nodeFiltered, totalNode, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{NodeID: "node-2"}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords by NodeID failed: %v", err)
	}
	if totalNode != 1 || len(nodeFiltered) != 1 || nodeFiltered[0].NodeID != "node-2" {
		t.Fatalf("expected 1 record for node-2, got total=%d", totalNode)
	}

	// 5. Filter by Network
	udpFiltered, totalUDP, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{Network: "udp"}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords by Network failed: %v", err)
	}
	if totalUDP != 1 || len(udpFiltered) != 1 || udpFiltered[0].Network != "udp" {
		t.Fatalf("expected 1 udp record, got total=%d", totalUDP)
	}

	// 6. Keyword search
	_, totalKW, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{Keyword: "google"}, 1, 10)
	if err != nil {
		t.Fatalf("ListConnectionRecords by Keyword failed: %v", err)
	}
	if totalKW != 2 {
		t.Fatalf("expected 2 records matching google, got total=%d", totalKW)
	}

	// 7. Update existing connection with increased traffic and closed_at
	closedRecord := records[0]
	closedRecord.Upload = 12000
	closedRecord.Download = 80000
	closedRecord.ClosedAt = now.Format(time.RFC3339)
	if err := store.SaveConnectionRecords(ctx, []ConnectionRecord{closedRecord}); err != nil {
		t.Fatalf("update closed record failed: %v", err)
	}

	updated, _, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{SourceIP: "192.168.1.100"}, 1, 10)
	if err != nil {
		t.Fatalf("fetch updated record failed: %v", err)
	}
	if len(updated) != 1 {
		t.Fatalf("expected 1 updated record, got %d", len(updated))
	}
	if updated[0].Upload != 12000 || updated[0].Download != 80000 {
		t.Fatalf("traffic not updated: upload=%d download=%d", updated[0].Upload, updated[0].Download)
	}
	if updated[0].ClosedAt == "" || updated[0].DurationSeconds <= 0 {
		t.Fatalf("closed_at or duration not set: closedAt=%s duration=%d", updated[0].ClosedAt, updated[0].DurationSeconds)
	}
}

func TestPopularDevicesAndSummary(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	records := []ConnectionRecord{
		{
			ID:           "node-1:c-1",
			NodeID:       "node-1",
			SourceIP:     "1.1.1.1",
			User:         "user-a",
			Upload:       100,
			Download:     500,
			StartedAt:    now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:           "node-1:c-2",
			NodeID:       "node-1",
			SourceIP:     "1.1.1.1",
			User:         "user-a",
			Upload:       200,
			Download:     800,
			StartedAt:    now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:           "node-1:c-3",
			NodeID:       "node-1",
			SourceIP:     "2.2.2.2",
			User:         "user-b",
			Upload:       50,
			Download:     150,
			StartedAt:    now.Add(-30 * time.Minute).Format(time.RFC3339),
		},
	}

	if err := store.SaveConnectionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	since := now.Add(-24 * time.Hour)
	devices, err := store.PopularDevices(ctx, since, 10)
	if err != nil {
		t.Fatalf("PopularDevices failed: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected 2 popular devices, got %d", len(devices))
	}
	if devices[0].User != "user-a" || devices[0].ConnectionCount != 2 {
		t.Fatalf("expected user-a to be rank 1 with 2 connections, got %s with %d", devices[0].User, devices[0].ConnectionCount)
	}
	if devices[0].Upload != 300 || devices[0].Download != 1300 {
		t.Fatalf("user-a bytes mismatch: upload=%d download=%d", devices[0].Upload, devices[0].Download)
	}

	summary, err := store.DeviceSummaryStats(ctx, since)
	if err != nil {
		t.Fatalf("DeviceSummaryStats failed: %v", err)
	}
	if summary.TotalConnections != 3 {
		t.Fatalf("expected 3 total connections, got %d", summary.TotalConnections)
	}
	if summary.UniqueIPs != 2 {
		t.Fatalf("expected 2 unique IPs, got %d", summary.UniqueIPs)
	}
	if summary.TotalUpload != 350 || summary.TotalDownload != 1450 {
		t.Fatalf("summary bytes mismatch: upload=%d download=%d", summary.TotalUpload, summary.TotalDownload)
	}
}

func TestPurgeExpiredConnectionRecords(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()

	// 1 old record (35 days ago, beyond 30 days retention)
	// 1 recent record (1 day ago)
	records := []ConnectionRecord{
		{
			ID:        "node-1:old",
			NodeID:    "node-1",
			SourceIP:  "10.0.0.1",
			StartedAt: now.Add(-35 * 24 * time.Hour).Format(time.RFC3339),
		},
		{
			ID:        "node-1:new",
			NodeID:    "node-1",
			SourceIP:  "10.0.0.2",
			StartedAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
		},
	}

	if err := store.SaveConnectionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.PurgeExpiredConnectionRecords(ctx, ConnectionRecordsRetention)
	if err != nil {
		t.Fatalf("PurgeExpiredConnectionRecords failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 record deleted, got %d", deleted)
	}

	remaining, total, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(remaining) != 1 || remaining[0].ID != "node-1:new" {
		t.Fatalf("expected only node-1:new to remain, got %v", remaining)
	}
}

func TestConnectionRecorderLifecycle(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	recorder := newConnectionRecorder(store)

	// Push 1: two connections
	push1 := []storedConnection{
		{
			ID:             "conn-101",
			Source:         "192.168.1.50:50001",
			SourceIP:       "192.168.1.50",
			SourceLocation: "China",
			Destination:    "1.1.1.1:443",
			Host:           "cloudflare.com",
			Network:        "tcp",
			User:           "client-x",
			Upload:         100,
			Download:       200,
			StartedAt:      time.Now().UTC().Format(time.RFC3339),
		},
		{
			ID:             "conn-102",
			Source:         "192.168.1.51:50002",
			SourceIP:       "192.168.1.51",
			SourceLocation: "China",
			Destination:    "8.8.8.8:53",
			Host:           "dns.google",
			Network:        "udp",
			User:           "client-y",
			Upload:         50,
			Download:       50,
			StartedAt:      time.Now().UTC().Format(time.RFC3339),
		},
	}

	recorder.RecordPush("node-1", "Node-A", push1)
	recorder.Flush(ctx)

	records, total, err := store.ListConnectionRecords(ctx, ConnectionRecordFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("expected 2 records after push 1, got %d", total)
	}

	// Push 2: conn-101 has increased traffic, conn-102 is missing (closed)
	push2 := []storedConnection{
		{
			ID:             "conn-101",
			Source:         "192.168.1.50:50001",
			SourceIP:       "192.168.1.50",
			SourceLocation: "China",
			Destination:    "1.1.1.1:443",
			Host:           "cloudflare.com",
			Network:        "tcp",
			User:           "client-x",
			Upload:         500,
			Download:       1000,
			StartedAt:      push1[0].StartedAt,
		},
	}

	recorder.RecordPush("node-1", "Node-A", push2)
	recorder.Flush(ctx)

	records, total, err = store.ListConnectionRecords(ctx, ConnectionRecordFilter{}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("expected still 2 records in total, got %d", total)
	}

	for _, r := range records {
		if r.ConnectionID == "conn-101" {
			if r.Upload != 500 || r.Download != 1000 {
				t.Fatalf("expected conn-101 bytes updated to 500/1000, got %d/%d", r.Upload, r.Download)
			}
		}
		if r.ConnectionID == "conn-102" {
			if r.ClosedAt == "" {
				t.Fatalf("expected conn-102 to be marked closed, got empty ClosedAt")
			}
		}
	}
}

func TestDeviceAPIs(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	admin, _, err := store.createOperator(ctx, "admin-test", "password123456", "admin", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.createSession(ctx, admin.ID, admin.Role, false)
	if err != nil {
		t.Fatal(err)
	}

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	// Insert sample records
	records := []ConnectionRecord{
		{
			ID:             "node-1:test-1",
			NodeID:         "node-1",
			NodeName:       "Node 1",
			SourceIP:       "203.0.113.195",
			SourceLocation: "Test Country",
			Destination:    "1.1.1.1:443",
			Host:           "one.one.one.one",
			Network:        "tcp",
			User:           "test-user",
			ListenerName:   "vless",
			OutboundName:   "direct",
			Upload:         1000,
			Download:       5000,
			StartedAt:      time.Now().UTC().Format(time.RFC3339),
		},
	}
	if err := store.SaveConnectionRecords(ctx, records); err != nil {
		t.Fatal(err)
	}

	handler := server.BrowserHandler()

	// Test 1: GET /api/v1/devices/connections
	req := httptest.NewRequest("GET", "/api/v1/devices/connections?ip=203.0.113.195", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if total, ok := res["total"].(float64); !ok || total != 1 {
		t.Fatalf("expected total 1, got %v", res["total"])
	}

	// Test 2: GET /api/v1/devices/popular
	req2 := httptest.NewRequest("GET", "/api/v1/devices/popular?range=24h", nil)
	req2.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	var res2 map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil {
		t.Fatal(err)
	}
	devices, ok := res2["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("expected 1 popular device, got %v", res2["devices"])
	}
}

