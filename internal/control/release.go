package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const releaseSigningKeyFile = "release-manifest.key.pem"

// SingBoxRelease is a master-approved binary for one Ubuntu CPU architecture.
// The agent only accepts the corresponding master-signed manifest, never an
// arbitrary download URL received on the control stream.
type SingBoxRelease struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Enabled      bool   `json:"enabled"`
	CreatedAt    string `json:"created_at,omitempty"`
}

type releaseManifest struct {
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
}

type signedReleasePayload struct {
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
}

func loadOrCreateReleaseSigningKey(dataDir string) (ed25519.PrivateKey, error) {
	path := filepath.Join(dataDir, releaseSigningKeyFile)
	encoded, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(encoded)
		if block == nil || block.Type != "PRIVATE KEY" {
			return nil, errors.New("release signing key has invalid PEM format")
		}
		value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse release signing key: %w", err)
		}
		key, ok := value.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("release signing key is not Ed25519")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read release signing key: %w", err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate release signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal release signing key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create release signing key: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, fmt.Errorf("write release signing key: %w", err)
	}
	return key, nil
}

func releaseSigningPublicKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		return nil, fmt.Errorf("marshal release signing public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

func validateSingBoxRelease(release SingBoxRelease) error {
	if strings.TrimSpace(release.Version) == "" || len(release.Version) > 128 || strings.ContainsAny(release.Version, "\r\n") {
		return errors.New("sing-box release version is required and must not contain a line break")
	}
	if release.Architecture != "amd64" && release.Architecture != "arm64" {
		return errors.New("sing-box release architecture must be amd64 or arm64")
	}
	parsed, err := url.Parse(release.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("sing-box release URL must be an HTTPS URL without user credentials")
	}
	if len(release.SHA256) != sha256HexLength {
		return errors.New("sing-box release SHA-256 must have 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(release.SHA256); err != nil {
		return errors.New("sing-box release SHA-256 must be hexadecimal")
	}
	return nil
}

const sha256HexLength = 64

func (s *Store) CreateSingBoxRelease(ctx context.Context, release SingBoxRelease) (SingBoxRelease, error) {
	if err := validateSingBoxRelease(release); err != nil {
		return SingBoxRelease{}, err
	}
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM singbox_releases WHERE version = ? AND architecture = ?`, release.Version, release.Architecture).Scan(&existing)
	if err == nil {
		return SingBoxRelease{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SingBoxRelease{}, fmt.Errorf("lookup sing-box release: %w", err)
	}
	identifier, err := newID()
	if err != nil {
		return SingBoxRelease{}, err
	}
	release.ID = identifier
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO singbox_releases (id, version, architecture, url, sha256, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, release.ID, release.Version, release.Architecture, release.URL, strings.ToLower(release.SHA256), release.Enabled, createdAt, createdAt)
	if err != nil {
		return SingBoxRelease{}, fmt.Errorf("create sing-box release: %w", err)
	}
	release.SHA256 = strings.ToLower(release.SHA256)
	release.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	return release, nil
}

func (s *Store) ListSingBoxReleases(ctx context.Context) ([]SingBoxRelease, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, version, architecture, url, sha256, enabled, created_at FROM singbox_releases ORDER BY version DESC, architecture, id`)
	if err != nil {
		return nil, fmt.Errorf("list sing-box releases: %w", err)
	}
	defer rows.Close()
	var releases []SingBoxRelease
	for rows.Next() {
		var release SingBoxRelease
		var createdAt int64
		if err := rows.Scan(&release.ID, &release.Version, &release.Architecture, &release.URL, &release.SHA256, &release.Enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("read sing-box release: %w", err)
		}
		release.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func (s *Store) FindSingBoxRelease(ctx context.Context, version, architecture string) (SingBoxRelease, error) {
	var release SingBoxRelease
	var createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, version, architecture, url, sha256, enabled, created_at
		FROM singbox_releases WHERE version = ? AND architecture = ? AND enabled = 1`, version, architecture).
		Scan(&release.ID, &release.Version, &release.Architecture, &release.URL, &release.SHA256, &release.Enabled, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SingBoxRelease{}, ErrNotFound
	}
	if err != nil {
		return SingBoxRelease{}, fmt.Errorf("load sing-box release: %w", err)
	}
	release.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	return release, nil
}

func (s *Store) SignedSingBoxReleasePayload(release SingBoxRelease) (string, error) {
	if err := validateSingBoxRelease(release); err != nil {
		return "", err
	}
	manifest, err := json.Marshal(releaseManifest{Version: release.Version, Architecture: release.Architecture, URL: release.URL, SHA256: strings.ToLower(release.SHA256)})
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(s.releaseSigningKey, manifest)
	payload, err := json.Marshal(signedReleasePayload{Manifest: string(manifest), Signature: base64.StdEncoding.EncodeToString(signature)})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func (s *Store) ReleaseSigningPublicKeyPEM() ([]byte, error) {
	return releaseSigningPublicKeyPEM(s.releaseSigningKey)
}
