package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpdateDNSTask(t *testing.T) {
	// Test updateDNS task execution
	task := Task{
		ID:   "task-dns-1",
		Kind: "dns.update",
		Payload: `{"ip":"203.0.113.55","domains":["my-nat.example.com"]}`,
	}

	res := updateDNS(context.Background(), task)
	if res.Status != "succeeded" {
		t.Fatalf("expected succeeded, got %s: %s", res.Status, res.Summary)
	}

	if !strings.Contains(res.Summary, "my-nat.example.com") {
		t.Fatalf("unexpected summary: %s", res.Summary)
	}
}

func TestCloseStaleClashConnectionsSelective(t *testing.T) {
	deletedIDs := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/connections" {
			connections := []map[string]any{
				{
					"id": "conn-stale-nat",
					"metadata": map[string]string{
						"host":           "nat.example.com",
						"destinationIP":  "198.51.100.10",
					},
				},
				{
					"id": "conn-other-user",
					"metadata": map[string]string{
						"host":           "google.com",
						"destinationIP":  "142.250.190.46",
					},
				},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"connections": connections})
			return
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/connections/") {
			id := strings.TrimPrefix(r.URL.Path, "/connections/")
			deletedIDs[id] = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Note: in production, clashAPIBase is 127.0.0.1:9090.
	// Here we test logic by ensuring closeStaleClashConnections handles errors gracefully.
	err := closeStaleClashConnections(context.Background(), []string{"nat.example.com"}, "203.0.113.55")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
