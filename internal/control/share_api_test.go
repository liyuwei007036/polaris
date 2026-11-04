package control_test

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/control"
)

func TestShareLinkIsReadOnlyMaskedAndExpires(t *testing.T) {
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
	nodeID := approveTestNode(t, server, httpServer.URL, session, csrfToken, "share-node")
	if err := store.SetNodeClientAddress(t.Context(), nodeID, "203.0.113.25"); err != nil {
		t.Fatal(err)
	}

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/shares", map[string]any{
		"page": "nodes", "expires_in": 3600,
	}, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous share creation: got %d", response.StatusCode)
	}
	response.Body.Close()

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/shares", map[string]any{
		"page": "nodes", "expires_in": 3600,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create share: got %d", response.StatusCode)
	}
	var created struct {
		Share control.ShareLink `json:"share"`
		Token string            `json:"token"`
		URL   string            `json:"url"`
	}
	decodeBody(t, response, &created)
	if created.Token == "" || !strings.HasPrefix(created.URL, httpServer.URL+"/share/") {
		t.Fatalf("share link is not usable: %#v", created)
	}
	if created.Share.Page != "nodes" || created.Share.ExpiresAt == "" {
		t.Fatalf("share metadata is incomplete: %#v", created.Share)
	}

	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/shares/access/"+created.Token, nil, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("public share read: got %d", response.StatusCode)
	}
	var payload struct {
		Page   string `json:"page"`
		Masked bool   `json:"masked"`
		Nodes  []struct {
			Name          string `json:"name"`
			ClientAddress string `json:"client_address"`
		} `json:"nodes"`
	}
	decodeBody(t, response, &payload)
	if payload.Page != "nodes" || !payload.Masked {
		t.Fatalf("public payload is not a masked node share: %#v", payload)
	}
	if len(payload.Nodes) != 1 || payload.Nodes[0].Name != "share-node" {
		t.Fatalf("public payload has the wrong node: %#v", payload.Nodes)
	}
	if payload.Nodes[0].ClientAddress != "203.0.*.*" {
		t.Fatalf("client address was not masked: %q", payload.Nodes[0].ClientAddress)
	}

	response = request(t, http.MethodDelete, httpServer.URL+"/api/v1/shares/"+created.Share.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke share: got %d", response.StatusCode)
	}
	response.Body.Close()
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/shares/access/"+created.Token, nil, "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked share must stop working: got %d", response.StatusCode)
	}
	response.Body.Close()

	expiredHash := sha256.Sum256([]byte("expired-share-token"))
	if _, err := store.CreateShareLink(t.Context(), created.Share.CreatedBy, "dashboard", expiredHash[:], time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	response = request(t, http.MethodGet, httpServer.URL+"/api/v1/shares/access/expired-share-token", nil, "", "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expired share must stop working: got %d", response.StatusCode)
	}
	response.Body.Close()
}
