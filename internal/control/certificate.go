package control

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/security"
)

// ManagedCertificate keeps PEM material encrypted at rest. API responses only
// expose metadata; neither certificate private keys nor their PEM chains are
// returned after creation.
type ManagedCertificate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type ManagedCertificateInput struct {
	Name           string `json:"name"`
	CertificatePEM string `json:"certificate_pem"`
	PrivateKeyPEM  string `json:"private_key_pem"`
	Enabled        bool   `json:"enabled"`
}

func validateCertificateInput(input ManagedCertificateInput) error {
	if input.Name == "" || len(input.Name) > 128 || strings.ContainsAny(input.Name, "\r\n") {
		return errors.New("certificate name up to 128 characters is required")
	}
	if len(input.CertificatePEM) == 0 || len(input.PrivateKeyPEM) == 0 || len(input.CertificatePEM) > 256*1024 || len(input.PrivateKeyPEM) > 64*1024 {
		return errors.New("certificate or private key is missing or exceeds allowed size")
	}
	if _, err := tls.X509KeyPair([]byte(input.CertificatePEM), []byte(input.PrivateKeyPEM)); err != nil {
		return fmt.Errorf("certificate and private key do not form a valid pair: %w", err)
	}
	return nil
}

func (s *Store) CreateManagedCertificate(ctx context.Context, input ManagedCertificateInput) (ManagedCertificate, error) {
	if err := validateCertificateInput(input); err != nil {
		return ManagedCertificate{}, err
	}
	certificate, err := security.Encrypt(s.masterKey, []byte(input.CertificatePEM))
	if err != nil {
		return ManagedCertificate{}, err
	}
	privateKey, err := security.Encrypt(s.masterKey, []byte(input.PrivateKeyPEM))
	if err != nil {
		return ManagedCertificate{}, err
	}
	identifier, err := newID()
	if err != nil {
		return ManagedCertificate{}, err
	}
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_certificates (id, name, certificate_pem, private_key_pem, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, identifier, input.Name, certificate, privateKey, input.Enabled, createdAt, createdAt)
	if err != nil {
		return ManagedCertificate{}, fmt.Errorf("create managed certificate: %w", err)
	}
	return ManagedCertificate{ID: identifier, Name: input.Name, Enabled: input.Enabled, CreatedAt: time.Unix(createdAt, 0).UTC().Format(time.RFC3339), UpdatedAt: time.Unix(createdAt, 0).UTC().Format(time.RFC3339)}, nil
}

func (s *Store) ListManagedCertificates(ctx context.Context) ([]ManagedCertificate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, enabled, created_at, updated_at FROM managed_certificates ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list managed certificates: %w", err)
	}
	defer rows.Close()
	var certificates []ManagedCertificate
	for rows.Next() {
		var certificate ManagedCertificate
		var createdAt, updatedAt int64
		if err := rows.Scan(&certificate.ID, &certificate.Name, &certificate.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("read managed certificate: %w", err)
		}
		certificate.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		certificate.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
		certificates = append(certificates, certificate)
	}
	return certificates, rows.Err()
}

func (s *Store) ReplaceManagedCertificate(ctx context.Context, certificateID string, input ManagedCertificateInput) (ManagedCertificate, error) {
	if certificateID == "" {
		return ManagedCertificate{}, errors.New("certificate ID is required")
	}
	if err := validateCertificateInput(input); err != nil {
		return ManagedCertificate{}, err
	}
	certificate, err := security.Encrypt(s.masterKey, []byte(input.CertificatePEM))
	if err != nil {
		return ManagedCertificate{}, err
	}
	privateKey, err := security.Encrypt(s.masterKey, []byte(input.PrivateKeyPEM))
	if err != nil {
		return ManagedCertificate{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_certificates SET name = ?, certificate_pem = ?, private_key_pem = ?, enabled = ?, updated_at = ? WHERE id = ?`, input.Name, certificate, privateKey, input.Enabled, nowUnix(), certificateID)
	if err != nil {
		return ManagedCertificate{}, fmt.Errorf("replace managed certificate: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ManagedCertificate{}, fmt.Errorf("read managed certificate replacement: %w", err)
	}
	if changed != 1 {
		return ManagedCertificate{}, ErrNotFound
	}
	return s.managedCertificate(ctx, certificateID)
}

func (s *Store) DeleteManagedCertificate(ctx context.Context, certificateID string) error {
	if certificateID == "" {
		return errors.New("certificate ID is required")
	}
	var references int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listeners WHERE json_extract(spec, '$.tls.certificate_id') = ?`, certificateID).Scan(&references); err != nil {
		return fmt.Errorf("check managed certificate references: %w", err)
	}
	if references != 0 {
		return ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM managed_certificates WHERE id = ?`, certificateID)
	if err != nil {
		return fmt.Errorf("delete managed certificate: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read managed certificate deletion: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) managedCertificate(ctx context.Context, certificateID string) (ManagedCertificate, error) {
	var certificate ManagedCertificate
	var createdAt, updatedAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, enabled, created_at, updated_at FROM managed_certificates WHERE id = ?`, certificateID).
		Scan(&certificate.ID, &certificate.Name, &certificate.Enabled, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedCertificate{}, ErrNotFound
	}
	if err != nil {
		return ManagedCertificate{}, fmt.Errorf("load managed certificate: %w", err)
	}
	certificate.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	certificate.UpdatedAt = time.Unix(updatedAt, 0).UTC().Format(time.RFC3339)
	return certificate, nil
}

func (s *Store) loadManagedCertificatePEM(ctx context.Context, certificateID string) (string, string, error) {
	var certificate, privateKey []byte
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT certificate_pem, private_key_pem, enabled FROM managed_certificates WHERE id = ?`, certificateID).Scan(&certificate, &privateKey, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("load managed certificate material: %w", err)
	}
	if !enabled {
		return "", "", errors.New("managed certificate is disabled")
	}
	plainCertificate, err := security.Decrypt(s.masterKey, certificate)
	if err != nil {
		return "", "", fmt.Errorf("decrypt managed certificate: %w", err)
	}
	plainPrivateKey, err := security.Decrypt(s.masterKey, privateKey)
	if err != nil {
		return "", "", fmt.Errorf("decrypt managed certificate key: %w", err)
	}
	return string(plainCertificate), string(plainPrivateKey), nil
}
