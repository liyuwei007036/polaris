package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAlertsAndSpeedtestAPIs(t *testing.T) {
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

	// Create admin operator and session
	admin, _, err := store.createOperator(ctx, "admin_user", "AdminPass123456!", "admin", false, false, false)
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	session, err := store.createSession(ctx, admin.ID, admin.Role, false)
	if err != nil {
		t.Fatalf("create session: %v", err)
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

	// 1. GET /api/v1/alerts/settings
	rec := doRequest("GET", "/api/v1/alerts/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get alert settings failed with status %d: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		Settings AlertSettings `json:"settings"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	settings := res.Settings
	if settings.BarkServer != "https://api.day.app" {
		t.Errorf("expected default bark server, got %s", settings.BarkServer)
	}

	// 2. PUT /api/v1/alerts/settings
	settings.BarkDeviceKey = "mock_device_key"
	settings.TrafficThresholdMbps = 75.0
	settings.ProbeNotifyAlways = true
	rec = doRequest("PUT", "/api/v1/alerts/settings", settings)
	if rec.Code != http.StatusOK {
		t.Fatalf("update alert settings failed with status %d: %s", rec.Code, rec.Body.String())
	}
	var updateRes struct {
		Settings AlertSettings `json:"settings"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&updateRes); err != nil {
		t.Fatalf("decode updated settings: %v", err)
	}
	updated := updateRes.Settings
	if updated.BarkDeviceKey != "mock_device_key" || updated.TrafficThresholdMbps != 75.0 {
		t.Errorf("updated settings mismatch: %+v", updated)
	}

	// 3. POST /api/v1/alerts/test (with mock bark server)
	mockBark := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "message": "success"})
	}))
	defer mockBark.Close()

	updated.BarkServer = mockBark.URL
	_, _ = store.UpdateAlertSettings(ctx, updated)

	rec = doRequest("POST", "/api/v1/alerts/test", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("test alert failed with status %d: %s", rec.Code, rec.Body.String())
	}

	// 4. Node Speedtests APIs: Create a mock node in DB
	nodeID := "node-speedtest-api"
	_, err = store.db.ExecContext(ctx, `INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "Speedtest Node", []byte("12345678901234567890123456789012"), nowUnix())
	if err != nil {
		t.Fatalf("insert node: %v", err)
	}

	// Save test speedtest record directly
	_ = store.SaveNodeSpeedtest(ctx, NodeSpeedtest{
		NodeID:           nodeID,
		TelecomLatencyMs: 40,
		TelecomSpeedMbps: 100.0,
		UnicomLatencyMs:  50,
		UnicomSpeedMbps:  150.0,
		MobileLatencyMs:  35,
		MobileSpeedMbps:  210.0,
	})

	// GET /api/v1/nodes/{id}/speedtest/latest
	rec = doRequest("GET", "/api/v1/nodes/"+nodeID+"/speedtest/latest", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get latest speedtest failed: %d %s", rec.Code, rec.Body.String())
	}
	var nodeLatest map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&nodeLatest)
	if nodeLatest["has_test"] != true {
		t.Errorf("expected has_test true, got %+v", nodeLatest)
	}

	// GET /api/v1/nodes/speedtests/latest
	rec = doRequest("GET", "/api/v1/nodes/speedtests/latest", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list latest speedtests failed: %d %s", rec.Code, rec.Body.String())
	}
	var allLatest map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&allLatest)
	if _, ok := allLatest["speedtests"]; !ok {
		t.Errorf("expected speedtests map, got %+v", allLatest)
	}

	// 5. POST /api/v1/tools/reality-check with invalid or unreachable domain should return error/report gracefully
	rec = doRequest("POST", "/api/v1/tools/reality-check", map[string]any{"domain": "127.0.0.1", "port": 1})
	// Should return error or handled 400
	if rec.Code == http.StatusInternalServerError {
		t.Errorf("expected clean client error or handled response, got 500: %s", rec.Body.String())
	}
}
