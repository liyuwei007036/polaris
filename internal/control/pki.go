package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/sb-control/sb-control/internal/security"
)

const (
	caCertificateFile = "agent-ca.crt"
	caPrivateKeyFile  = "agent-ca.key.enc"
)

type CertificateAuthority struct {
	certificate *x509.Certificate
	privateKey  ed25519.PrivateKey
}

func LoadOrCreateCA(dataDir string, masterKey []byte) (*CertificateAuthority, error) {
	certPath := filepath.Join(dataDir, caCertificateFile)
	keyPath := filepath.Join(dataDir, caPrivateKeyFile)
	certificatePEM, certErr := os.ReadFile(certPath)
	encryptedKey, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		certificate, err := parseCertificatePEM(certificatePEM)
		if err != nil {
			return nil, fmt.Errorf("read agent CA certificate: %w", err)
		}
		plainKey, err := security.Decrypt(masterKey, encryptedKey)
		if err != nil {
			return nil, fmt.Errorf("decrypt agent CA key: %w", err)
		}
		privateKey, err := parseEd25519PrivateKey(plainKey)
		if err != nil {
			return nil, fmt.Errorf("read agent CA key: %w", err)
		}
		return &CertificateAuthority{certificate: certificate, privateKey: privateKey}, nil
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read agent CA files: certificate=%v key=%v", certErr, keyErr)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate agent CA key: %w", err)
	}
	serial, err := certificateSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	certificateTemplate := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "sb-control agent CA"},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, certificateTemplate, certificateTemplate, publicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create agent CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse agent CA certificate: %w", err)
	}
	certificatePEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal agent CA key: %w", err)
	}
	encryptedKey, err = security.Encrypt(masterKey, privateDER)
	if err != nil {
		return nil, fmt.Errorf("encrypt agent CA key: %w", err)
	}
	if err := writePrivateFile(keyPath, encryptedKey); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, certificatePEM, 0o644); err != nil {
		return nil, fmt.Errorf("write agent CA certificate: %w", err)
	}
	return &CertificateAuthority{certificate: certificate, privateKey: privateKey}, nil
}

func (ca *CertificateAuthority) CertificatePEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.certificate.Raw})
}

func (ca *CertificateAuthority) SignNodeCSR(csrPEM []byte, nodeID, nodeName string) (certificatePEM []byte, serial string, err error) {
	csr, err := parseCSR(csrPEM)
	if err != nil {
		return nil, "", err
	}
	serialNumber, err := certificateSerial()
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject:      pkix.Name{CommonName: nodeName},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{nodeID},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, csr.PublicKey, ca.privateKey)
	if err != nil {
		return nil, "", fmt.Errorf("sign node certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), serialNumber.Text(16), nil
}

func ValidateCSR(csrPEM []byte) error {
	_, err := parseCSR(csrPEM)
	return err
}

func parseCSR(csrPEM []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("CSR must be a PEM CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("verify CSR: %w", err)
	}
	return csr, nil
}

func certificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return serial, nil
}

func parseCertificatePEM(value []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("missing PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseEd25519PrivateKey(value []byte) (ed25519.PrivateKey, error) {
	key, err := x509.ParsePKCS8PrivateKey(value)
	if err != nil {
		return nil, err
	}
	privateKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("agent CA key is not Ed25519")
	}
	return privateKey, nil
}

func writePrivateFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private file %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	if _, err := file.Write(content); err != nil {
		return fmt.Errorf("write private file %s: %w", filepath.Base(path), err)
	}
	return nil
}
