package control

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"time"
)

// ExecForTest runs one statement against the store's database so a test can
// set up state the API deliberately does not expose, such as records old
// enough to fall outside the retention window. Compiled only for tests.
func (s *Store) ExecForTest(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// CountForTest returns the single integer a counting query produces.
// Compiled only for tests.
func (s *Store) CountForTest(ctx context.Context, query string, args ...any) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
}

// SetCloudflareAPIBaseForTest points the Cloudflare client at a test server.
// Compiled only for tests.
func SetCloudflareAPIBaseForTest(url string) { cloudflareAPI = url }

// NewOriginCertificatePEMForTest issues a self-signed certificate and matching
// private key covering the given names, so a test can supply real PEM material
// the way an operator pastes a Cloudflare origin certificate. Compiled only
// for tests.
func NewOriginCertificatePEMForTest(names ...string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}

// PushConnectionsForTest injects a real-time connections snapshot exactly as
// an agent's push would. Connections are in-memory state, so this is the only
// way a test can populate them. Compiled only for tests.
func (s *Server) PushConnectionsForTest(nodeID, collectedAt string, connections json.RawMessage) {
	s.connHub.update(nodeConnectionsSnapshot{NodeID: nodeID, CollectedAt: collectedAt, Connections: connections})
}
