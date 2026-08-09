package control_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

func TestListenerShareLinksCoverEnabledUsersOnly(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "share-link-node")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "sg.example.com"); err != nil {
		t.Fatal(err)
	}

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/listeners/quick", map[string]any{
		"listener": map[string]any{
			"node_id": nodeID, "name": "扫码入口", "connection_domain": "listener.example.com", "port": 443, "enabled": true,
			"spec": map[string]any{
				"protocol": "vless", "network": "tcp", "tls": map[string]any{"enabled": true},
				"reality": map[string]any{"enabled": true, "handshake_server": "www.microsoft.com", "handshake_port": 443},
			},
		},
		"accounts": []map[string]any{
			{"name": "alice", "alias": "新加坡专线 01", "enabled": true, "outbound_id": "direct"},
			{"name": "bob", "alias": "新加坡专线 02", "enabled": true, "outbound_id": "direct"},
		},
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create listener: got %d", response.StatusCode)
	}
	var created struct {
		Listener  control.Listener   `json:"listener"`
		Endpoints []control.Endpoint `json:"endpoints"`
	}
	decodeBody(t, response, &created)
	if len(created.Endpoints) != 2 {
		t.Fatalf("expected two endpoints, got %#v", created.Endpoints)
	}

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/endpoints/"+created.Endpoints[1].ID+"/enabled", map[string]any{"enabled": false}, session, csrfToken)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		t.Fatalf("disable endpoint: got %d", response.StatusCode)
	}
	response.Body.Close()

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/listeners/"+created.Listener.ID+"/share-links", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list share links: got %d", response.StatusCode)
	}
	var payload struct {
		ShareLinks []control.EndpointShareLink `json:"share_links"`
	}
	decodeBody(t, response, &payload)
	if len(payload.ShareLinks) != 1 {
		t.Fatalf("a disabled user has nothing to scan and must be left out: %#v", payload.ShareLinks)
	}
	link := payload.ShareLinks[0]
	if link.EndpointID != created.Endpoints[0].ID || link.Name != "alice" || link.Alias != "新加坡专线 01" {
		t.Fatalf("share link does not identify its user: %#v", link)
	}
	if !strings.HasPrefix(link.Link, "vless://") || !strings.Contains(link.Link, "listener.example.com:443") || !strings.Contains(link.Link, "security=reality") {
		t.Fatalf("share link is not a scannable node link: %s", link.Link)
	}

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/listeners/"+created.Listener.ID+"/share-links", nil, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("share links carry credentials and must require a session: got %d", response.StatusCode)
	}
	response.Body.Close()
}
