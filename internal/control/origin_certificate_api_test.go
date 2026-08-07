package control_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

// TestOriginCertificateAPIKeepsThePrivateKeyOnTheServer walks the whole
// lifecycle an operator sees: paste the PEM pair, read the list back, rename
// the domain without re-pasting, and delete it.
func TestOriginCertificateAPIKeepsThePrivateKeyOnTheServer(t *testing.T) {
	_, _, baseURL, session, csrfToken := newFeatureServer(t)
	certificatePEM, privateKeyPEM, err := control.NewOriginCertificatePEMForTest("example.com", "*.example.com")
	if err != nil {
		t.Fatal(err)
	}

	response := request(t, http.MethodPost, baseURL+"/api/v1/cloudflare/origin-certificates", map[string]any{
		"domain": "*.example.com", "certificate": certificatePEM, "private_key": privateKeyPEM,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create origin certificate: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
	var created control.OriginCertificate
	decodeBody(t, response, &created)
	if created.ID == "" || created.Domain != "*.example.com" {
		t.Fatalf("unexpected created origin certificate: %#v", created)
	}
	if created.PrivateKey != "" || created.Certificate != "" {
		t.Fatal("the API echoed the stored PEM material back to the browser")
	}

	response = request(t, http.MethodGet, baseURL+"/api/v1/cloudflare/origin-certificates", nil, session, csrfToken)
	var listed struct {
		Certificates []control.OriginCertificate `json:"certificates"`
	}
	decodeBody(t, response, &listed)
	if len(listed.Certificates) != 1 || listed.Certificates[0].PrivateKey != "" {
		t.Fatalf("unexpected origin certificate list: %#v", listed.Certificates)
	}

	// Renaming the domain without re-pasting keeps the stored material, so an
	// edit cannot silently empty the certificate.
	response = request(t, http.MethodPut, baseURL+"/api/v1/cloudflare/origin-certificates/"+created.ID,
		map[string]any{"domain": "proxy.example.com"}, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rename origin certificate domain: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
	var renamed control.OriginCertificate
	decodeBody(t, response, &renamed)
	if renamed.Domain != "proxy.example.com" || renamed.Fingerprint != created.Fingerprint {
		t.Fatalf("rename replaced the stored certificate: %#v", renamed)
	}

	// A domain the stored certificate does not cover has to be refused.
	response = request(t, http.MethodPut, baseURL+"/api/v1/cloudflare/origin-certificates/"+created.ID,
		map[string]any{"domain": "proxy.other.com"}, session, csrfToken)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("uncovered domain accepted: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}

	response = request(t, http.MethodDelete, baseURL+"/api/v1/cloudflare/origin-certificates/"+created.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete origin certificate: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
}

// TestOriginCertificateSaveDispatchesAffectedNodes proves the operator does not
// have to publish anything by hand: storing and removing a certificate queues
// the recompiled configuration for the nodes whose listeners it covers.
func TestOriginCertificateSaveDispatchesAffectedNodes(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "origin-node")
	listenerResponse := request(t, http.MethodPost, baseURL+"/api/v1/listeners", map[string]any{
		"node_id": nodeID, "name": "vless-ws", "connection_domain": "proxy.example.com",
		"listen_address": "0.0.0.0", "port": 443, "enabled": true,
		"spec": map[string]any{
			"protocol": "vless", "network": "tcp",
			"tls":       map[string]any{"enabled": true},
			"transport": map[string]any{"type": "ws", "path": "/proxy"},
		},
	}, session, csrfToken)
	if listenerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create listener: got %d, body=%s", listenerResponse.StatusCode, readBodyForTest(t, listenerResponse))
	}
	listenerResponse.Body.Close()

	certificatePEM, privateKeyPEM, err := control.NewOriginCertificatePEMForTest("example.com", "*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/cloudflare/origin-certificates", map[string]any{
		"domain": "*.example.com", "certificate": certificatePEM, "private_key": privateKeyPEM,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create origin certificate: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
	var created control.OriginCertificate
	decodeBody(t, response, &created)
	if response.Header.Get("X-SB-Auto-Apply-Task") == "" {
		t.Fatal("the response did not report the automatically dispatched task")
	}
	// The compiled configuration must now carry the pasted certificate, and the
	// node must have that exact desired state waiting for it.
	if got := compiledInboundCertificate(t, store, nodeID); got != certificatePEM {
		t.Fatalf("the recompiled configuration does not use the origin certificate, got:\n%s", got)
	}
	assertPendingConfiguration(t, store, nodeID)

	response = request(t, http.MethodDelete, baseURL+"/api/v1/cloudflare/origin-certificates/"+created.ID, nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete origin certificate: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
	if got := compiledInboundCertificate(t, store, nodeID); got == certificatePEM {
		t.Fatal("the configuration still carries a deleted origin certificate")
	}
	assertPendingConfiguration(t, store, nodeID)
}

// assertPendingConfiguration checks the node has an undelivered configuration
// task carrying exactly the current desired state. Counting tasks would be
// wrong: returning to an already queued state reuses that task by design.
func assertPendingConfiguration(t *testing.T, store *control.Store, nodeID string) {
	t.Helper()
	_, desiredHash, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.CountForTest(t.Context(),
		`SELECT COUNT(*) FROM tasks WHERE node_id = ? AND kind = 'singbox.apply_config' AND expected_hash = ? AND status IN ('queued', 'dispatched')`,
		nodeID, desiredHash)
	if err != nil {
		t.Fatal(err)
	}
	if pending == 0 {
		t.Fatalf("no configuration task carries the current desired state %s", desiredHash)
	}
}

// compiledInboundCertificate returns the certificate the node's single inbound
// presents.
func compiledInboundCertificate(t *testing.T, store *control.Store, nodeID string) string {
	t.Helper()
	encoded, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		Inbounds []struct {
			TLS struct {
				Certificate []string `json:"certificate"`
			} `json:"tls"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(encoded), &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Inbounds) != 1 || len(configuration.Inbounds[0].TLS.Certificate) != 1 {
		t.Fatalf("unexpected compiled inbounds: %s", encoded)
	}
	return configuration.Inbounds[0].TLS.Certificate[0]
}

// TestOriginCertificateSkipsListenersItDoesNotCover keeps an unrelated edit
// from restarting sing-box on every node in the fleet.
func TestOriginCertificateSkipsListenersItDoesNotCover(t *testing.T) {
	store, server, baseURL, session, csrfToken := newFeatureServer(t)
	nodeID := approveTestNode(t, server, baseURL, session, csrfToken, "reality-node")
	listenerResponse := request(t, http.MethodPost, baseURL+"/api/v1/listeners", map[string]any{
		"node_id": nodeID, "name": "vless-reality", "connection_domain": "proxy.example.com",
		"listen_address": "0.0.0.0", "port": 443, "enabled": true,
		"spec": map[string]any{
			"protocol": "vless", "network": "tcp",
			"tls":     map[string]any{"enabled": true},
			"reality": map[string]any{"enabled": true, "handshake_server": "www.microsoft.com", "handshake_port": 443},
		},
	}, session, csrfToken)
	if listenerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create Reality listener: got %d, body=%s", listenerResponse.StatusCode, readBodyForTest(t, listenerResponse))
	}
	listenerResponse.Body.Close()

	before := configurationTaskCount(t, store, nodeID)
	certificatePEM, privateKeyPEM, err := control.NewOriginCertificatePEMForTest("example.com", "*.example.com")
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, http.MethodPost, baseURL+"/api/v1/cloudflare/origin-certificates", map[string]any{
		"domain": "*.example.com", "certificate": certificatePEM, "private_key": privateKeyPEM,
	}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create origin certificate: got %d, body=%s", response.StatusCode, readBodyForTest(t, response))
	}
	response.Body.Close()
	if got := configurationTaskCount(t, store, nodeID); got != before {
		t.Fatalf("a Reality-only node was redeployed for an origin certificate: %d extra tasks", got-before)
	}
	if response.Header.Get("X-SB-Auto-Apply-Task") != "" {
		t.Fatal("a Reality-only node was reported as redeployed")
	}
}

func configurationTaskCount(t *testing.T, store *control.Store, nodeID string) int {
	t.Helper()
	count, err := store.CountForTest(t.Context(), `SELECT COUNT(*) FROM tasks WHERE node_id = ? AND kind = 'singbox.apply_config'`, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOriginCertificateAPIRejectsUnauthenticatedAccess(t *testing.T) {
	_, _, baseURL, _, _ := newFeatureServer(t)
	response := request(t, http.MethodGet, baseURL+"/api/v1/cloudflare/origin-certificates", nil, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated origin certificate query returned HTTP %d", response.StatusCode)
	}
}
