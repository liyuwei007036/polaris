package control

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
)

func TestGeneratedHysteria2CertificateUsesConnectionDomain(t *testing.T) {
	certificatePEM, _, err := generateHysteria2Certificate("hy2.example.com")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil {
		t.Fatal("generated certificate is not valid PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Subject.CommonName != "hy2.example.com" || len(certificate.DNSNames) != 1 || certificate.DNSNames[0] != "hy2.example.com" {
		t.Fatalf("generated certificate names = CN %q, DNS %#v", certificate.Subject.CommonName, certificate.DNSNames)
	}
}

func TestMigrationRemovesManagedCertificatesTable(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE managed_certificates (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'managed_certificates'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("managed certificates table still exists after migration")
	}
}

func TestCompileHysteria2ConfigurationIsStable(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	nodeID := strings.Repeat("a", 32)
	if _, err := store.db.Exec(`INSERT INTO nodes (id, name, public_key, created_at) VALUES (?, ?, ?, ?)`, nodeID, "hysteria-node", []byte(strings.Repeat("k", 32)), nowUnix()); err != nil {
		t.Fatal(err)
	}
	listener, err := store.CreateListener(t.Context(), Listener{
		NodeID: nodeID, Name: "hysteria2", Domain: "hy2.example.com", ListenAddr: "0.0.0.0", Port: 443, Enabled: true,
		Spec: ProtocolSpec{Protocol: "hysteria2", Network: "udp", TLS: TLSOptions{Enabled: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateEndpoint(t.Context(), Endpoint{ListenerID: listener.ID, Name: "client", Enabled: true}, EndpointCredentials{Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	firstConfig, firstHash, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	secondConfig, secondHash, err := store.CompileNodeConfig(t.Context(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if firstConfig != secondConfig || firstHash != secondHash {
		t.Fatalf("unchanged Hysteria2 listener compiled differently: first=%s second=%s", firstHash, secondHash)
	}
}
