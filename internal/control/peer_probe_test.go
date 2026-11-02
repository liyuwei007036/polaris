package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetNodePublicIPv6(t *testing.T) {
	nodeV4Only := Node{
		ID:          "node-v4",
		IPAddresses: []string{"198.51.100.1", "10.0.0.1"},
	}
	if got := getNodePublicIPv6(nodeV4Only); got != "" {
		t.Errorf("expected empty ipv6 for v4 only node, got %s", got)
	}

	nodeDualStack := Node{
		ID:          "node-dual",
		IPAddresses: []string{"198.51.100.2", "2606:4700:4700::1111", "fe80::1"},
	}
	if got := getNodePublicIPv6(nodeDualStack); got != "2606:4700:4700::1111" {
		t.Errorf("expected 2606:4700:4700::1111, got %s", got)
	}
}

func TestClientInfoHandler(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest("GET", "/api/v1/client-info", nil)
	req.Header.Set("X-Forwarded-For", "114.114.114.114")
	rec := httptest.NewRecorder()

	s.handleClientInfo(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if res["ip"] != "114.114.114.114" {
		t.Errorf("expected ip 114.114.114.114, got %v", res["ip"])
	}
	if res["is_ipv6"] != false {
		t.Errorf("expected is_ipv6=false, got %v", res["is_ipv6"])
	}
}

func TestClientInfoHandlerIPv6(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest("GET", "/api/v1/client-info", nil)
	req.Header.Set("X-Real-IP", "2400:3200::1")
	rec := httptest.NewRecorder()

	s.handleClientInfo(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if res["ip"] != "2400:3200::1" {
		t.Errorf("expected ip 2400:3200::1, got %v", res["ip"])
	}
	if res["is_ipv6"] != true {
		t.Errorf("expected is_ipv6=true, got %v", res["is_ipv6"])
	}
}
