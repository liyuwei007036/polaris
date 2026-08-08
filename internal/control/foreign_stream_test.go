package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
	"github.com/liyuwei007036/polaris/internal/wire"
)

// A node whose own Nginx already binds a stream socket can never deploy a
// polaris service onto it: `nginx -t` rejects the duplicate listen and the
// agent rolls back. The save has to be refused up front, or the console
// reports success for a configuration the node can never reach.
func TestSaveRefusedOnForeignNginxStreamPort(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	session, csrfToken := login(t, httpServer.URL, secret)
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "foreign-stream-node")
	server.ReportForeignStreamListensForTest(nodeID, []wire.StreamListen{
		{Address: "0.0.0.0", Port: 443, File: "/etc/nginx/nginx.conf"},
	})

	quickListener := func(name string, port int) map[string]any {
		return map[string]any{
			"listener": map[string]any{
				"node_id": nodeID, "name": name, "port": port, "enabled": true,
				"spec": map[string]any{"protocol": "vless", "network": "tcp", "transport": map[string]any{"type": "ws"}},
			},
			"default_outbound_id": "direct",
		}
	}

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", quickListener("撞外部 stream", 443), session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("save onto a foreign stream port: got %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	var problem struct {
		Error string `json:"error"`
	}
	decodeBody(t, response, &problem)
	if !strings.Contains(problem.Error, "/etc/nginx/nginx.conf") || !strings.Contains(problem.Error, "443") {
		t.Fatalf("refusal does not name the conflicting file and port: %q", problem.Error)
	}
	count, err := store.CountForTest(t.Context(), `SELECT COUNT(*) FROM listeners`)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a refused save still stored %d listener(s)", count)
	}

	// A different port on the same node is untouched by the report.
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", quickListener("其他端口", 8443), session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("save on a free port: got %d", response.StatusCode)
	}
	response.Body.Close()

	// The agent reports afresh once the operator removes the stream block (a
	// reconnect clears it too); the same save must then pass.
	server.ReportForeignStreamListensForTest(nodeID, nil)
	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", quickListener("冲突解除", 443), session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("save after the conflict cleared: got %d", response.StatusCode)
	}
	response.Body.Close()
}
