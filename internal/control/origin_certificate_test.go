package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func testOriginCertificatePEM(t *testing.T, names ...string) (string, string) {
	t.Helper()
	certificatePEM, privateKeyPEM, err := NewOriginCertificatePEMForTest(names...)
	if err != nil {
		t.Fatal(err)
	}
	return certificatePEM, privateKeyPEM
}

func TestOriginCertificateWildcardCoversOneLabel(t *testing.T) {
	for _, testCase := range []struct {
		pattern string
		domain  string
		want    bool
	}{
		{"*.example.com", "proxy.example.com", true},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "example.com", false},
		{"proxy.example.com", "proxy.example.com", true},
		{"proxy.example.com", "other.example.com", false},
	} {
		if got := originCertificateMatches(testCase.pattern, testCase.domain); got != testCase.want {
			t.Errorf("originCertificateMatches(%q, %q) = %v, want %v", testCase.pattern, testCase.domain, got, testCase.want)
		}
	}
}

func TestCreateOriginCertificateRejectsUnusablePEM(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificatePEM, privateKeyPEM := testOriginCertificatePEM(t, "example.com", "*.example.com")
	_, otherKeyPEM := testOriginCertificatePEM(t, "example.com", "*.example.com")

	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.other.com", Certificate: certificatePEM, PrivateKey: privateKeyPEM,
	}); err == nil {
		t.Fatal("a certificate that does not cover the domain was accepted")
	}
	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.example.com", Certificate: certificatePEM, PrivateKey: otherKeyPEM,
	}); err == nil {
		t.Fatal("a certificate paired with the wrong private key was accepted")
	}
	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.exa mple.com", Certificate: certificatePEM, PrivateKey: privateKeyPEM,
	}); err == nil {
		t.Fatal("an invalid domain pattern was accepted")
	}
}

func TestOriginCertificateResponseNeverCarriesPrivateKey(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificatePEM, privateKeyPEM := testOriginCertificatePEM(t, "example.com", "*.example.com")
	created, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.example.com", Certificate: certificatePEM, PrivateKey: privateKeyPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PrivateKey != "" || created.Certificate != "" {
		t.Fatal("stored origin certificate returned its PEM material")
	}
	if created.NotAfter == "" || created.Fingerprint == "" || len(created.DNSNames) != 2 {
		t.Fatalf("stored origin certificate lost its details: %#v", created)
	}
}

// vlessOriginListenerConfig compiles one node carrying a single VLESS listener
// and returns its inbound TLS block.
func vlessOriginListenerConfig(t *testing.T, store *Store, spec ProtocolSpec, domain string) map[string]any {
	t.Helper()
	nodeID := strings.Repeat("b", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`,
		nodeID, "origin-node", []byte(strings.Repeat("k", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "vless", Domain: domain, ListenAddr: "0.0.0.0", Port: 443, Enabled: true, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEndpoint(t.Context(), Endpoint{ListenerID: listener.ID, Name: "client", Enabled: true},
		EndpointCredentials{UUID: "4b8e9c1a-0000-4000-8000-000000000000"}); err != nil {
		t.Fatal(err)
	}
	encoded, _, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		Inbounds []struct {
			TLS map[string]any `json:"tls"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(encoded), &configuration); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Inbounds) != 1 {
		t.Fatalf("expected one compiled inbound, got %d", len(configuration.Inbounds))
	}
	return configuration.Inbounds[0].TLS
}

func compiledCertificate(t *testing.T, tlsBlock map[string]any) string {
	t.Helper()
	values, ok := tlsBlock["certificate"].([]any)
	if !ok || len(values) != 1 {
		t.Fatalf("compiled TLS block has no certificate: %#v", tlsBlock)
	}
	certificate, _ := values[0].(string)
	return certificate
}

func TestVLESSWebSocketAndGRPCUseMatchingOriginCertificate(t *testing.T) {
	for _, transport := range []TransportOptions{
		{Type: "ws", Path: "/proxy"},
		{Type: "grpc", ServiceName: "grpc-service"},
	} {
		t.Run(transport.Type, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			certificatePEM, privateKeyPEM := testOriginCertificatePEM(t, "example.com", "*.example.com")
			if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
				Domain: "*.example.com", Certificate: certificatePEM, PrivateKey: privateKeyPEM,
			}); err != nil {
				t.Fatal(err)
			}
			tlsBlock := vlessOriginListenerConfig(t, store, ProtocolSpec{
				Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: transport,
			}, "proxy.example.com")
			if compiledCertificate(t, tlsBlock) != certificatePEM {
				t.Fatal("compiled listener did not use the stored origin certificate")
			}
		})
	}
}

func TestVLESSListenerWithoutMatchingOriginCertificateKeepsSelfSigned(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificatePEM, privateKeyPEM := testOriginCertificatePEM(t, "other.com", "*.other.com")
	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.other.com", Certificate: certificatePEM, PrivateKey: privateKeyPEM,
	}); err != nil {
		t.Fatal(err)
	}
	tlsBlock := vlessOriginListenerConfig(t, store, ProtocolSpec{
		Protocol: "vless", Network: "tcp", TLS: TLSOptions{Enabled: true}, Transport: TransportOptions{Type: "ws", Path: "/proxy"},
	}, "proxy.example.com")
	if compiledCertificate(t, tlsBlock) == certificatePEM {
		t.Fatal("a listener outside the certificate's domain used the origin certificate")
	}
}

func TestExactOriginCertificateWinsOverWildcard(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wildcardPEM, wildcardKeyPEM := testOriginCertificatePEM(t, "example.com", "*.example.com")
	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "*.example.com", Certificate: wildcardPEM, PrivateKey: wildcardKeyPEM,
	}); err != nil {
		t.Fatal(err)
	}
	exactPEM, exactKeyPEM := testOriginCertificatePEM(t, "proxy.example.com")
	if _, err := store.CreateOriginCertificate(t.Context(), OriginCertificate{
		Domain: "proxy.example.com", Certificate: exactPEM, PrivateKey: exactKeyPEM,
	}); err != nil {
		t.Fatal(err)
	}
	certificate, _, found, err := store.loadOriginCertificateFor(t.Context(), "proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !found || certificate != exactPEM {
		t.Fatal("the wildcard certificate shadowed the exact one")
	}
}

func TestRealityListenerIgnoresOriginCertificate(t *testing.T) {
	if listenerUsesOriginCertificate(ProtocolSpec{
		Protocol: "vless", TLS: TLSOptions{Enabled: true}, Reality: RealityOptions{Enabled: true},
	}) {
		t.Fatal("a Reality listener asked for an origin certificate")
	}
	if listenerUsesOriginCertificate(ProtocolSpec{Protocol: "hysteria2", TLS: TLSOptions{Enabled: true}}) {
		t.Fatal("a Hysteria2 listener asked for an origin certificate")
	}
}
