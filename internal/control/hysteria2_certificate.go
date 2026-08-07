package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

type hysteria2Certificate struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

func (s *Store) loadOrCreateHysteria2Certificate(ctx context.Context, listenerID, domain string) (string, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var encrypted []byte
	var storedDomain string
	err = tx.QueryRowContext(ctx, `SELECT domain, certificate FROM listener_certificates WHERE listener_id = ?`, listenerID).Scan(&storedDomain, &encrypted)
	if err == nil && storedDomain == domain {
		plain, decryptErr := security.Decrypt(s.masterKey, encrypted)
		if decryptErr != nil {
			return "", "", fmt.Errorf("decrypt Hysteria2 certificate: %w", decryptErr)
		}
		var certificate hysteria2Certificate
		if unmarshalErr := json.Unmarshal(plain, &certificate); unmarshalErr != nil {
			return "", "", fmt.Errorf("decode Hysteria2 certificate: %w", unmarshalErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return "", "", commitErr
		}
		return certificate.Certificate, certificate.PrivateKey, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("load Hysteria2 certificate: %w", err)
	}
	certificatePEM, privateKeyPEM, err := generateHysteria2Certificate(domain)
	if err != nil {
		return "", "", err
	}
	plain, err := json.Marshal(hysteria2Certificate{Certificate: certificatePEM, PrivateKey: privateKeyPEM})
	if err != nil {
		return "", "", err
	}
	encrypted, err = security.Encrypt(s.masterKey, plain)
	if err != nil {
		return "", "", fmt.Errorf("encrypt Hysteria2 certificate: %w", err)
	}
	now := nowUnix()
	_, err = tx.ExecContext(ctx, `INSERT INTO listener_certificates (listener_id, domain, certificate, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(listener_id) DO UPDATE SET domain=excluded.domain, certificate=excluded.certificate, updated_at=excluded.updated_at`,
		listenerID, domain, encrypted, now, now)
	if err != nil {
		return "", "", fmt.Errorf("store Hysteria2 certificate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return certificatePEM, privateKeyPEM, nil
}

func generateHysteria2Certificate(domain string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	now := time.Now()
	commonName := domain
	if commonName == "" {
		commonName = "polaris-hysteria2"
	}
	certificate := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName}, DNSNames: []string{commonName}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}
