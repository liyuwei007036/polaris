package control

import (
	"crypto/x509"
	"encoding/pem"
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
