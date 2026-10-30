package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGFWProbeLogic(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.prober.Stop()

	// 1. Test offline node: Must NEVER be flagged as GFW blocked
	offlineNode := Node{
		ID:            "node-offline-test",
		Name:          "Offline Node",
		Online:        false,
		ClientAddress: "198.51.100.1",
	}
	itemOffline := server.probeSingleNode(ctx, offlineNode, offlineNode.ClientAddress, 443)
	if itemOffline.GFWBlocked {
		t.Errorf("offline node was incorrectly marked as GFW blocked!")
	}
	if itemOffline.OverseasOK {
		t.Errorf("offline node should have OverseasOK = false")
	}

	// 2. Test healthy online node: Must NEVER be flagged as GFW blocked
	_, err = store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		"node-online-test", "Online Transit Node", []byte("11111111111111111111111111111111"), nowUnix())
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}
	_ = store.SaveNodeSpeedtest(ctx, NodeSpeedtest{
		NodeID:          "node-online-test",
		GFWBlocked:      false,
		GFWStatus:       "normal",
		DomesticLatency: 45,
		DomesticSource:  "分布式探针",
		TestedAt:        time.Now().UTC().Format(time.RFC3339),
	})
	onlineNode := Node{
		ID:            "node-online-test",
		Name:          "Online Transit Node",
		Online:        true,
		ClientAddress: "198.51.100.2",
	}
	itemOnline := server.probeSingleNode(ctx, onlineNode, onlineNode.ClientAddress, 443)
	if itemOnline.GFWBlocked {
		t.Errorf("healthy online node was marked as GFW blocked!")
	}
	if !itemOnline.DomesticOK {
		t.Errorf("healthy online node should have DomesticOK = true")
	}

	// 3. Test historical DB cleanup migration
	// Insert node first to satisfy foreign key
	_, err = store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		"node-corrupt", "Corrupt Node", []byte("12345678901234567890123456789012"), nowUnix())
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}

	// Insert a corrupt speedtest record into DB
	_ = store.SaveNodeSpeedtest(ctx, NodeSpeedtest{
		NodeID:              "node-corrupt",
		GFWBlocked:          true,
		GFWStatus:           "blocked",
		DomesticLatency:     0,
		DomesticSource:      "国内探针全超时 (GFW 端口阻断)",
		InboundTelecomRoute: "GFW 阻断",
		InboundUnicomRoute:  "GFW 阻断",
		InboundMobileRoute:  "GFW 阻断",
		TestedAt:            time.Now().UTC().Format(time.RFC3339),
	})

	latestBefore, _ := store.GetLatestNodeSpeedtest(ctx, "node-corrupt")
	if latestBefore == nil || !latestBefore.GFWBlocked {
		t.Fatalf("failed to insert corrupt test record")
	}

	// Run migration
	if err := store.migrate(ctx); err != nil {
		t.Fatalf("store migration failed: %v", err)
	}

	latestAfter, _ := store.GetLatestNodeSpeedtest(ctx, "node-corrupt")
	if latestAfter == nil {
		t.Fatalf("latest record not found")
	}
	if latestAfter.GFWBlocked {
		t.Errorf("migration should have cleared false-positive GFWBlocked, but got true")
	}
	if latestAfter.GFWStatus == "blocked" {
		t.Errorf("migration should have reset GFWStatus, got %s", latestAfter.GFWStatus)
	}
	if latestAfter.InboundTelecomRoute == "GFW 阻断" {
		t.Errorf("migration should have cleared InboundTelecomRoute, got %s", latestAfter.InboundTelecomRoute)
	}

	// 4. Test AlertEngine: Probe error should NOT trigger GFW blocked alarm
	var receivedAlerts []struct {
		Title string
		Body  string
	}
	mockBark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAlerts = append(receivedAlerts, struct {
			Title string
			Body  string
		}{
			Title: r.URL.Query().Get("title"),
			Body:  r.URL.Query().Get("body"),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code": 200, "message": "success"}`))
	}))
	defer mockBark.Close()

	alertEngine := NewAlertEngine(store, nil)
	_, _ = store.UpdateAlertSettings(ctx, AlertSettings{
		BarkDeviceKey:   "test_key",
		BarkServer:      mockBark.URL,
		GFWAlertEnabled: true,
	})

	// When probe failed due to platform error (Error != ""), no GFW alert should fire!
	alertEngine.NotifyProbeSummary([]ProbeResultItem{
		{
			NodeID:        "node-err",
			NodeName:      "Node With API Error",
			OverseasOK:    true,
			DomesticOK:    false,
			GFWBlocked:    false,
			ProbeExecuted: false,
			Error:         "探测平台响应错误 (400)",
			StatusDesc:    "国内探针异常: 探测平台响应错误 (400)",
		},
	})

	time.Sleep(100 * time.Millisecond)

	for _, a := range receivedAlerts {
		if strings.Contains(a.Title, "发现节点被阻断") || strings.Contains(a.Body, "疑似被 GFW 阻断") {
			t.Errorf("unexpected GFW alert triggered for probe error: %+v", a)
		}
	}
}
