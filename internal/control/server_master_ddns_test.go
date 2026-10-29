package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerMasterDDNSEndpoints(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.prober.Stop()

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

	// 1. GET /api/v1/master/ddns
	rec := doRequest(http.MethodGet, "/api/v1/master/ddns", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/master/ddns code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	// 2. PUT /api/v1/master/ddns
	putBody := map[string]any{
		"enabled":    true,
		"record_ids": []string{"rec-1", "rec-2"},
		"domains":    []string{"int.1x1x1.dpdns.org", "master.1x1x1.dpdns.org"},
	}
	rec = doRequest(http.MethodPut, "/api/v1/master/ddns", putBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/v1/master/ddns code = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Settings MasterDDNSSettings `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Settings.Enabled || len(resp.Settings.RecordIDs) != 2 || len(resp.Settings.Domains) != 2 {
		t.Fatalf("unexpected settings response: %+v", resp.Settings)
	}
}
