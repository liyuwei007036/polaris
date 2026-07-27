package control_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- test reproduces RFC 6238 TOTP client output.
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sb-control/sb-control/internal/agent"
	"github.com/sb-control/sb-control/internal/control"
	"github.com/sb-control/sb-control/internal/wire"
)

func TestSecureRegistrationLifecycle(t *testing.T) {
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
	agentAddr := startAgentListener(t, server)

	session, csrfToken := login(t, httpServer.URL, secret)
	keypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	registrationToken := createRegistrationToken(t, httpServer.URL, session, csrfToken)
	registrationID := registerAgent(t, agentAddr, server.NoisePublicKey(), keypair, registrationToken, "test-node")

	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+registrationID+"/approve", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approve registration: got %d", response.StatusCode)
	}
	var approved struct {
		NodeID string `json:"node_id"`
	}
	decodeBody(t, response, &approved)
	if approved.NodeID == "" {
		t.Fatal("approval did not return a node ID")
	}

	// Reconnecting with the same keypair after approval must be recognized
	// directly (no certificate to fetch — the public key itself is the
	// identity) and proceed straight into a normal session.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, agentAddr, keypair, server.NoisePublicKey())
	if err != nil {
		t.Fatal(err)
	}
	ack, err := agent.Register(conn, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "approved" || ack.NodeID != approved.NodeID {
		t.Fatalf("unexpected post-approval ack: %#v", ack)
	}
	conn.Close()

	response = request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+approved.NodeID+"/revoke", nil, session, csrfToken)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke node: got %d", response.StatusCode)
	}
}

func TestRegistrationTokenCanOnlyBeUsedOnce(t *testing.T) {
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
	agentAddr := startAgentListener(t, server)

	session, csrfToken := login(t, httpServer.URL, secret)
	token := createRegistrationToken(t, httpServer.URL, session, csrfToken)
	firstKeypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	registerAgent(t, agentAddr, server.NoisePublicKey(), firstKeypair, token, "test-node")

	// A second, different node trying to reuse the same (now-consumed) token
	// must be rejected. Idempotency in RegisterAgent is keyed by public key,
	// so this must use a fresh keypair to actually exercise that path
	// instead of just re-observing the first node's pending registration.
	secondKeypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, agentAddr, secondKeypair, server.NoisePublicKey())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ack, err := agent.Register(conn, token, "another-node", map[string]string{"os": "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "rejected" {
		t.Fatalf("reused registration token: got status %q, want rejected", ack.Status)
	}
}

// startAgentListener runs the given server's agent-facing Noise/TCP accept
// loop on an ephemeral local port for the duration of the test.
func startAgentListener(t *testing.T, server *control.Server) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.ServeAgents(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
	})
	return listener.Addr().String()
}

func TestOperatorManagementPreservesAnEnabledAdministrator(t *testing.T) {
	store, err := control.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secret, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple")
	if err != nil || secret == "" {
		t.Fatalf("create initial administrator: %v", err)
	}
	operators, err := store.ListOperators(t.Context())
	if err != nil || len(operators) != 1 {
		t.Fatalf("list initial administrator: %#v, %v", operators, err)
	}
	if _, err := store.UpdateOperator(t.Context(), control.Operator{ID: operators[0].ID, Role: "viewer", Enabled: true}); err == nil {
		t.Fatal("allowed the last enabled administrator to be demoted")
	}
	created, memberSecret, err := store.CreateOperator(t.Context(), "operator@example.com", "another correct horse battery staple", "admin")
	if err != nil || memberSecret == "" || created.Role != "admin" {
		t.Fatalf("create replacement administrator: %#v, %v", created, err)
	}
	updated, err := store.UpdateOperator(t.Context(), control.Operator{ID: operators[0].ID, Role: "viewer", Enabled: true})
	if err != nil || updated.Role != "viewer" {
		t.Fatalf("demote administrator with a replacement: %#v, %v", updated, err)
	}
	if _, err := store.ResetOperatorTOTP(t.Context(), created.ID); err != nil {
		t.Fatalf("reset operator TOTP: %v", err)
	}
}

func TestCompileTLSAndRealityListeners(t *testing.T) {
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
	agentAddr := startAgentListener(t, server)
	session, csrfToken := login(t, httpServer.URL, secret)
	keypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	registrationID := registerAgent(t, agentAddr, server.NoisePublicKey(), keypair, createRegistrationToken(t, httpServer.URL, session, csrfToken), "config-node")
	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+registrationID+"/approve", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approve registration: got %d", response.StatusCode)
	}
	var approved struct {
		NodeID string `json:"node_id"`
	}
	decodeBody(t, response, &approved)
	certificatePEM, privateKeyPEM := testCertificate(t)
	certificate, err := store.CreateManagedCertificate(t.Context(), control.ManagedCertificateInput{Name: "test-cert", CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tlsListener, err := store.CreateListener(t.Context(), control.Listener{NodeID: approved.NodeID, Name: "trojan", ListenAddr: "0.0.0.0", Port: 4443, Enabled: true, Spec: control.ProtocolSpec{Protocol: "trojan", Network: "tcp", TLS: control.TLSOptions{Enabled: true, CertificateID: certificate.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	tlsEndpoint, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: tlsListener.ID, Name: "alice", Enabled: true}, control.EndpointCredentials{Password: "password"})
	if err != nil {
		t.Fatal(err)
	}
	realityKey, privateRealityKey, err := store.CreateRealityKey(t.Context(), "test-reality")
	if err != nil {
		t.Fatal(err)
	}
	realityListener, err := store.CreateListener(t.Context(), control.Listener{
		NodeID:     approved.NodeID,
		Name:       "reality",
		ListenAddr: "0.0.0.0",
		Port:       4444,
		Enabled:    true,
		Spec: control.ProtocolSpec{
			Protocol: "vless",
			Network:  "tcp",
			TLS:      control.TLSOptions{Enabled: true},
			Reality:  control.RealityOptions{Enabled: true, KeyID: realityKey.ID, HandshakeServer: "www.example.com", HandshakePort: 443, ShortIDs: []string{"0123456789abcdef"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEndpoint(t.Context(), control.Endpoint{ListenerID: realityListener.ID, Name: "bob", Enabled: true}, control.EndpointCredentials{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}); err != nil {
		t.Fatal(err)
	}
	configuration, _, err := store.CompileNodeConfig(t.Context(), approved.NodeID)
	if err != nil {
		t.Fatalf("compile TLS and Reality configuration: %v", err)
	}
	if !strings.Contains(configuration, "BEGIN CERTIFICATE") || !strings.Contains(configuration, privateRealityKey) {
		t.Fatal("compiled configuration omitted managed TLS or Reality material")
	}
	subscription, accessToken, err := store.CreateSubscription(t.Context(), control.SubscriptionInput{Kind: control.ClientSubscription, Name: "clients", EndpointIDs: []string{tlsEndpoint.ID}, Enabled: true})
	if err != nil || subscription.ID == "" || accessToken == "" {
		t.Fatalf("create client subscription: %#v, %v", subscription, err)
	}
	content, err := store.GenerateClientSubscription(t.Context(), accessToken)
	decoded, decodeErr := base64.StdEncoding.DecodeString(content)
	if err != nil || decodeErr != nil || !strings.Contains(string(decoded), "trojan://") {
		t.Fatalf("generate client subscription: %q, %v, %v", content, err, decodeErr)
	}
	if err := store.SetEndpointEnabled(t.Context(), tlsEndpoint.ID, false); err != nil {
		t.Fatal(err)
	}
	content, err = store.GenerateClientSubscription(t.Context(), accessToken)
	decoded, decodeErr = base64.StdEncoding.DecodeString(content)
	if err != nil || decodeErr != nil || strings.Contains(string(decoded), "trojan://") {
		t.Fatalf("disabled endpoint remained in client subscription: %q, %v, %v", content, err, decodeErr)
	}
}

func testCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"example.com"}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// TestApprovedAgentSessionUpdatesNodeStatus exercises the whole Noise-based
// session end to end: an unapproved key is rejected from the normal session,
// approval unblocks it, and a Status message sent over the session updates
// the node's stored identity — the replacement for the old mTLS-certificate
// heartbeat test (there is no certificate anymore; the public key itself,
// verified during the Noise_XK handshake, is the identity).
func TestApprovedAgentSessionUpdatesNodeStatus(t *testing.T) {
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
	agentAddr := startAgentListener(t, server)
	session, csrfToken := login(t, httpServer.URL, secret)

	keypair, err := wire.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	registrationID := registerAgent(t, agentAddr, server.NoisePublicKey(), keypair, createRegistrationToken(t, httpServer.URL, session, csrfToken), "status-node")
	response := request(t, http.MethodPost, httpServer.URL+"/api/v1/nodes/"+registrationID+"/approve", nil, session, csrfToken)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("approve registration: got %d", response.StatusCode)
	}
	response.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, agentAddr, keypair, server.NoisePublicKey())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ack, err := agent.Register(conn, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "approved" {
		t.Fatalf("unexpected ack after approval: %#v", ack)
	}
	statusBody, err := wire.Encode(wire.Status{AgentVersion: "test", OS: "linux", Architecture: "amd64", SingBoxVersion: "1.0", Capabilities: map[string]string{"systemd": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(wire.MsgStatus, statusBody); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		nodes, err := store.ListNodes(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 1 && nodes[0].AgentVersion == "test" && nodes[0].SingBox == "1.0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("node status was not updated in time: %#v", nodes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func login(t *testing.T, baseURL, secret string) (string, string) {
	t.Helper()
	response := request(t, http.MethodPost, baseURL+"/api/v1/auth/login", map[string]string{
		"email": "admin@example.com", "password": "correct horse battery staple",
	}, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("password login: got %d", response.StatusCode)
	}
	var challenge struct {
		ID string `json:"challenge_id"`
	}
	decodeBody(t, response, &challenge)
	response = request(t, http.MethodPost, baseURL+"/api/v1/auth/mfa", map[string]string{
		"challenge_id": challenge.ID, "code": totp(secret, time.Now().UTC()),
	}, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("MFA login: got %d", response.StatusCode)
	}
	defer response.Body.Close()
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Value == "" {
		t.Fatal("MFA response did not set a session cookie")
	}
	return cookies[0].Value, session.CSRF
}

func createRegistrationToken(t *testing.T, baseURL, session, csrfToken string) string {
	t.Helper()
	response := request(t, http.MethodPost, baseURL+"/api/v1/nodes/registration-tokens", map[string]int{"lifetime_seconds": 60}, session, csrfToken)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create registration token: got %d", response.StatusCode)
	}
	var result struct {
		Token string `json:"token"`
	}
	decodeBody(t, response, &result)
	if result.Token == "" {
		t.Fatal("registration token is empty")
	}
	return result.Token
}

// registerAgent dials the agent Noise listener with keypair and submits a
// registration; it fails the test unless the result is "pending" (the
// expected outcome for a fresh, not-yet-approved key), returning the new
// registration's ID for the caller to approve via the admin API.
func registerAgent(t *testing.T, agentAddr string, masterPub [wire.KeySize]byte, keypair wire.Keypair, token, nodeName string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, agentAddr, keypair, masterPub)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ack, err := agent.Register(conn, token, nodeName, map[string]string{"os": "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if ack.Status != "pending" {
		t.Fatalf("register agent: unexpected status %q", ack.Status)
	}
	return ack.RegistrationID
}

func request(t *testing.T, method, url string, value any, session, csrfToken string) *http.Response {
	t.Helper()
	var body *bytes.Reader
	if value == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "sb_control_session", Value: session})
	}
	if csrfToken != "" {
		req.Header.Set("X-CSRF-Token", csrfToken)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeBody(t *testing.T, response *http.Response, target any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}

func totp(secret string, now time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		panic(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, key) // #nosec G401 -- RFC 6238 test client compatibility.
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}
