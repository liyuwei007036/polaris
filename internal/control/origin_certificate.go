package control

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

// OriginCertificate is an operator-supplied origin certificate, normally the
// one Cloudflare issues in the Origin Server tab. A VLESS WebSocket or gRPC
// listener whose connection domain the certificate covers presents it to
// Cloudflare, so the zone can pull from the origin with full (strict) TLS
// instead of the self-signed certificate this platform generates by default.
type OriginCertificate struct {
	ID          string   `json:"id"`
	Domain      string   `json:"domain"`
	Issuer      string   `json:"issuer,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	DNSNames    []string `json:"dns_names,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	NotBefore   string   `json:"not_before,omitempty"`
	NotAfter    string   `json:"not_after,omitempty"`
	Expired     bool     `json:"expired"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	// Certificate and PrivateKey carry the pasted PEM text on create and
	// update only. Responses always clear them: the private key never leaves
	// the master process once stored.
	Certificate string `json:"certificate,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
}

type originCertificateMaterial struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
}

func normalizeOriginCertificateDomain(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

// validOriginCertificateDomain accepts either a plain host name or a leading
// "*." wildcard, which is what Cloudflare issues origin certificates for.
func validOriginCertificateDomain(value string) bool {
	if rest, wildcard := strings.CutPrefix(value, "*."); wildcard {
		return validSNI(rest)
	}
	return validSNI(value)
}

// originCertificateMatches reports whether a stored domain pattern covers a
// listener's connection domain. A "*." wildcard spans exactly one label, the
// same way TLS itself treats wildcard certificates.
func originCertificateMatches(pattern, domain string) bool {
	if pattern == "" || domain == "" {
		return false
	}
	if pattern == domain {
		return true
	}
	suffix, wildcard := strings.CutPrefix(pattern, "*")
	if !wildcard {
		return false
	}
	label, ok := strings.CutSuffix(domain, suffix)
	return ok && label != "" && !strings.Contains(label, ".")
}

// parseOriginCertificate validates that the pasted PEM pair belongs together
// and that the certificate actually covers the configured domain. Catching a
// mismatched paste here is the difference between a clear error and a
// Cloudflare 526 nobody can explain later.
func parseOriginCertificate(certificatePEM, privateKeyPEM, domain string) (*x509.Certificate, error) {
	if strings.TrimSpace(certificatePEM) == "" || strings.TrimSpace(privateKeyPEM) == "" {
		return nil, errors.New("origin certificate and private key PEM text are required")
	}
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(privateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("origin certificate and private key do not match: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse origin certificate: %w", err)
	}
	if !certificateCoversDomain(leaf, domain) {
		return nil, fmt.Errorf("origin certificate does not cover %s", domain)
	}
	return leaf, nil
}

func certificateCoversDomain(certificate *x509.Certificate, domain string) bool {
	for _, name := range certificate.DNSNames {
		name = normalizeOriginCertificateDomain(name)
		if name == domain || originCertificateMatches(name, domain) {
			return true
		}
	}
	return false
}

func (s *Store) storeOriginCertificate(ctx context.Context, id, domain, certificatePEM, privateKeyPEM string, leaf *x509.Certificate, create bool) error {
	plain, err := json.Marshal(originCertificateMaterial{Certificate: certificatePEM, PrivateKey: privateKeyPEM})
	if err != nil {
		return err
	}
	encrypted, err := security.Encrypt(s.masterKey, plain)
	if err != nil {
		return fmt.Errorf("encrypt origin certificate: %w", err)
	}
	digest := sha256.Sum256(leaf.Raw)
	dnsNames, err := json.Marshal(leaf.DNSNames)
	if err != nil {
		return err
	}
	if create {
		_, err = s.db.ExecContext(ctx, `INSERT INTO origin_certificates (id,domain,material,subject,issuer,dns_names,fingerprint,not_before,not_after,created_at,updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, domain, encrypted, leaf.Subject.CommonName, leaf.Issuer.CommonName, string(dnsNames), hex.EncodeToString(digest[:]),
			leaf.NotBefore.Unix(), leaf.NotAfter.Unix(), nowUnix(), nowUnix())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return fmt.Errorf("create origin certificate: %w", err)
		}
		return nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE origin_certificates SET domain=?,material=?,subject=?,issuer=?,dns_names=?,fingerprint=?,not_before=?,not_after=?,updated_at=? WHERE id=?`,
		domain, encrypted, leaf.Subject.CommonName, leaf.Issuer.CommonName, string(dnsNames), hex.EncodeToString(digest[:]),
		leaf.NotBefore.Unix(), leaf.NotAfter.Unix(), nowUnix(), id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return fmt.Errorf("update origin certificate: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateOriginCertificate(ctx context.Context, input OriginCertificate) (OriginCertificate, error) {
	domain := normalizeOriginCertificateDomain(input.Domain)
	if !validOriginCertificateDomain(domain) {
		return OriginCertificate{}, errors.New("domain must be a host name or a *.example.com wildcard")
	}
	leaf, err := parseOriginCertificate(input.Certificate, input.PrivateKey, domain)
	if err != nil {
		return OriginCertificate{}, err
	}
	id, err := newID()
	if err != nil {
		return OriginCertificate{}, err
	}
	if err := s.storeOriginCertificate(ctx, id, domain, input.Certificate, input.PrivateKey, leaf, true); err != nil {
		return OriginCertificate{}, err
	}
	return s.OriginCertificateByID(ctx, id)
}

// UpdateOriginCertificate replaces the PEM material only when new text was
// pasted; an empty pair means the operator is only renaming the domain, so
// the stored certificate is re-validated against it instead of being lost.
func (s *Store) UpdateOriginCertificate(ctx context.Context, input OriginCertificate) (OriginCertificate, error) {
	if input.ID == "" {
		return OriginCertificate{}, errors.New("origin certificate ID is required")
	}
	domain := normalizeOriginCertificateDomain(input.Domain)
	if !validOriginCertificateDomain(domain) {
		return OriginCertificate{}, errors.New("domain must be a host name or a *.example.com wildcard")
	}
	certificatePEM, privateKeyPEM := input.Certificate, input.PrivateKey
	if strings.TrimSpace(certificatePEM) == "" && strings.TrimSpace(privateKeyPEM) == "" {
		material, err := s.originCertificateMaterial(ctx, input.ID)
		if err != nil {
			return OriginCertificate{}, err
		}
		certificatePEM, privateKeyPEM = material.Certificate, material.PrivateKey
	}
	leaf, err := parseOriginCertificate(certificatePEM, privateKeyPEM, domain)
	if err != nil {
		return OriginCertificate{}, err
	}
	if err := s.storeOriginCertificate(ctx, input.ID, domain, certificatePEM, privateKeyPEM, leaf, false); err != nil {
		return OriginCertificate{}, err
	}
	return s.OriginCertificateByID(ctx, input.ID)
}

func (s *Store) originCertificateMaterial(ctx context.Context, id string) (originCertificateMaterial, error) {
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT material FROM origin_certificates WHERE id=?`, id).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return originCertificateMaterial{}, ErrNotFound
	}
	if err != nil {
		return originCertificateMaterial{}, fmt.Errorf("load origin certificate: %w", err)
	}
	plain, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return originCertificateMaterial{}, fmt.Errorf("decrypt origin certificate: %w", err)
	}
	var material originCertificateMaterial
	if err := json.Unmarshal(plain, &material); err != nil {
		return originCertificateMaterial{}, fmt.Errorf("decode origin certificate: %w", err)
	}
	return material, nil
}

func (s *Store) ListOriginCertificates(ctx context.Context) ([]OriginCertificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,domain,subject,issuer,dns_names,fingerprint,not_before,not_after,created_at,updated_at
		FROM origin_certificates ORDER BY domain,id`)
	if err != nil {
		return nil, fmt.Errorf("list origin certificates: %w", err)
	}
	defer rows.Close()
	certificates := []OriginCertificate{}
	for rows.Next() {
		certificate, err := scanOriginCertificate(rows.Scan)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, certificate)
	}
	return certificates, rows.Err()
}

func (s *Store) OriginCertificateByID(ctx context.Context, id string) (OriginCertificate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,domain,subject,issuer,dns_names,fingerprint,not_before,not_after,created_at,updated_at
		FROM origin_certificates WHERE id=?`, id)
	certificate, err := scanOriginCertificate(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return OriginCertificate{}, ErrNotFound
	}
	if err != nil {
		return OriginCertificate{}, fmt.Errorf("load origin certificate: %w", err)
	}
	return certificate, nil
}

func scanOriginCertificate(scan func(...any) error) (OriginCertificate, error) {
	var certificate OriginCertificate
	var dnsNames string
	var notBefore, notAfter, createdAt, updatedAt int64
	if err := scan(&certificate.ID, &certificate.Domain, &certificate.Subject, &certificate.Issuer, &dnsNames,
		&certificate.Fingerprint, &notBefore, &notAfter, &createdAt, &updatedAt); err != nil {
		return OriginCertificate{}, err
	}
	if dnsNames != "" {
		if err := json.Unmarshal([]byte(dnsNames), &certificate.DNSNames); err != nil {
			return OriginCertificate{}, fmt.Errorf("decode origin certificate names: %w", err)
		}
	}
	certificate.NotBefore = time.Unix(notBefore, 0).UTC().Format(time.RFC3339)
	certificate.NotAfter = time.Unix(notAfter, 0).UTC().Format(time.RFC3339)
	certificate.Expired = time.Now().After(time.Unix(notAfter, 0))
	certificate.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	certificate.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	return certificate, nil
}

func (s *Store) DeleteOriginCertificate(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM origin_certificates WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete origin certificate: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

// loadOriginCertificateFor returns the PEM pair that covers a connection
// domain. An exact domain wins over a wildcard, and a longer wildcard wins
// over a shorter one, so a certificate for a specific host is never shadowed
// by a broader one.
func (s *Store) loadOriginCertificateFor(ctx context.Context, domain string) (string, string, bool, error) {
	domain = normalizeOriginCertificateDomain(domain)
	if domain == "" {
		return "", "", false, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,domain FROM origin_certificates`)
	if err != nil {
		return "", "", false, fmt.Errorf("match origin certificate: %w", err)
	}
	defer rows.Close()
	bestID, bestPattern := "", ""
	for rows.Next() {
		var id, pattern string
		if err := rows.Scan(&id, &pattern); err != nil {
			return "", "", false, err
		}
		if !originCertificateMatches(pattern, domain) {
			continue
		}
		if bestPattern == "" || betterOriginCertificateMatch(pattern, bestPattern) {
			bestID, bestPattern = id, pattern
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", false, err
	}
	if bestID == "" {
		return "", "", false, nil
	}
	material, err := s.originCertificateMaterial(ctx, bestID)
	if err != nil {
		return "", "", false, err
	}
	return material.Certificate, material.PrivateKey, true, nil
}

// OriginCertificateNodeIDs lists the nodes carrying an enabled VLESS WebSocket
// or gRPC listener whose connection domain the pattern covers. Storing or
// removing a certificate changes exactly those listeners' compiled TLS
// material, so those nodes are the ones that need the new configuration.
func (s *Store) OriginCertificateNodeIDs(ctx context.Context, domain string) ([]string, error) {
	domain = normalizeOriginCertificateDomain(domain)
	if domain == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT node_id, connection_domain, spec FROM listeners WHERE enabled = 1 ORDER BY node_id, id`)
	if err != nil {
		return nil, fmt.Errorf("list origin certificate nodes: %w", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	var nodeIDs []string
	for rows.Next() {
		var nodeID, connectionDomain, encoded string
		if err := rows.Scan(&nodeID, &connectionDomain, &encoded); err != nil {
			return nil, fmt.Errorf("read origin certificate node: %w", err)
		}
		if seen[nodeID] || !originCertificateMatches(domain, normalizeOriginCertificateDomain(connectionDomain)) {
			continue
		}
		var spec ProtocolSpec
		if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
			return nil, fmt.Errorf("decode listener spec: %w", err)
		}
		if !listenerUsesOriginCertificate(spec) {
			continue
		}
		seen[nodeID] = true
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, rows.Err()
}

func betterOriginCertificateMatch(candidate, current string) bool {
	candidateWildcard := strings.HasPrefix(candidate, "*.")
	currentWildcard := strings.HasPrefix(current, "*.")
	if candidateWildcard != currentWildcard {
		return !candidateWildcard
	}
	return len(candidate) > len(current)
}

// listenerUsesOriginCertificate reports whether a listener is one of the
// Cloudflare-frontable VLESS transports, which are the only ones where an
// origin certificate is what Cloudflare validates on the pull.
func listenerUsesOriginCertificate(spec ProtocolSpec) bool {
	if spec.Protocol != "vless" || spec.Reality.Enabled || !spec.TLS.Enabled {
		return false
	}
	return spec.Transport.Type == "ws" || spec.Transport.Type == "grpc"
}
