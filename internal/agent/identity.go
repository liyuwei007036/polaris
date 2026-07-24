package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	privateKeyFile              = "agent.key.pem"
	releaseSigningPublicKeyFile = "release-manifest.pub"
)

func CreateCSR(dataDir, nodeName string) ([]byte, error) {
	if nodeName == "" {
		return nil, errors.New("node name is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create agent data directory: %w", err)
	}
	privateKey, err := loadOrCreatePrivateKey(filepath.Join(dataDir, privateKeyFile))
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeName}}, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create agent CSR: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

func SaveCertificate(dataDir string, certificatePEM, caPEM []byte, releaseSigningPublicKeyPEM ...[]byte) error {
	if len(certificatePEM) == 0 || len(caPEM) == 0 {
		return errors.New("certificate and CA certificate are required")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent.crt"), certificatePEM, 0o644); err != nil {
		return fmt.Errorf("write agent certificate: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "agent-ca.crt"), caPEM, 0o644); err != nil {
		return fmt.Errorf("write agent CA certificate: %w", err)
	}
	if len(releaseSigningPublicKeyPEM) > 0 && len(releaseSigningPublicKeyPEM[0]) > 0 {
		if err := os.WriteFile(filepath.Join(dataDir, releaseSigningPublicKeyFile), releaseSigningPublicKeyPEM[0], 0o644); err != nil {
			return fmt.Errorf("write release signing public key: %w", err)
		}
	}
	return nil
}

func loadOrCreatePrivateKey(path string) (ed25519.PrivateKey, error) {
	value, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(value)
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("agent private key has invalid PEM format")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse agent private key: %w", err)
		}
		privateKey, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("agent private key is not Ed25519")
		}
		return privateKey, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read agent private key: %w", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate agent private key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("marshal agent private key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create agent private key: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, fmt.Errorf("write agent private key: %w", err)
	}
	return privateKey, nil
}
