package control

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

type ManagedRealityKey struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at,omitempty"`
}

func (s *Store) CreateRealityKey(ctx context.Context, name string) (ManagedRealityKey, string, error) {
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n") {
		return ManagedRealityKey{}, "", errors.New("Reality key name up to 128 characters is required")
	}
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return ManagedRealityKey{}, "", fmt.Errorf("generate Reality key: %w", err)
	}
	privateValue := base64.RawURLEncoding.EncodeToString(private.Bytes())
	publicValue := base64.RawURLEncoding.EncodeToString(private.PublicKey().Bytes())
	encrypted, err := security.Encrypt(s.masterKey, []byte(privateValue))
	if err != nil {
		return ManagedRealityKey{}, "", err
	}
	identifier, err := newID()
	if err != nil {
		return ManagedRealityKey{}, "", err
	}
	createdAt := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO managed_reality_keys (id, name, public_key, private_key, enabled, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`, identifier, name, publicValue, encrypted, createdAt)
	if err != nil {
		return ManagedRealityKey{}, "", fmt.Errorf("create Reality key: %w", err)
	}
	key := ManagedRealityKey{ID: identifier, Name: name, PublicKey: publicValue, Enabled: true, CreatedAt: time.Unix(createdAt, 0).UTC().Format(time.RFC3339)}
	return key, privateValue, nil
}

func (s *Store) ListRealityKeys(ctx context.Context) ([]ManagedRealityKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, public_key, enabled, created_at FROM managed_reality_keys ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Reality keys: %w", err)
	}
	defer rows.Close()
	var keys []ManagedRealityKey
	for rows.Next() {
		var key ManagedRealityKey
		var createdAt int64
		if err := rows.Scan(&key.ID, &key.Name, &key.PublicKey, &key.Enabled, &createdAt); err != nil {
			return nil, fmt.Errorf("read Reality key: %w", err)
		}
		key.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *Store) SetRealityKeyEnabled(ctx context.Context, keyID string, enabled bool) error {
	if keyID == "" {
		return errors.New("Reality key ID is required")
	}
	if !enabled {
		var references int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listeners WHERE json_extract(spec, '$.reality.key_id') = ? AND enabled = 1`, keyID).Scan(&references); err != nil {
			return fmt.Errorf("check Reality key references: %w", err)
		}
		if references != 0 {
			return ErrConflict
		}
	}
	result, err := s.db.ExecContext(ctx, `UPDATE managed_reality_keys SET enabled = ? WHERE id = ?`, enabled, keyID)
	if err != nil {
		return fmt.Errorf("set Reality key state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Reality key state update: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRealityKeyIfUnused(ctx context.Context, keyID string) error {
	if keyID == "" {
		return nil
	}
	var references int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listeners WHERE json_extract(spec, '$.reality.key_id') = ?`, keyID).Scan(&references); err != nil {
		return fmt.Errorf("check Reality key references: %w", err)
	}
	if references != 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM managed_reality_keys WHERE id = ?`, keyID); err != nil {
		return fmt.Errorf("delete unused Reality key: %w", err)
	}
	return nil
}

func (s *Store) loadRealityPrivateKey(ctx context.Context, keyID string) (string, error) {
	var encrypted []byte
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT private_key, enabled FROM managed_reality_keys WHERE id = ?`, keyID).Scan(&encrypted, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load Reality key: %w", err)
	}
	if !enabled {
		return "", errors.New("managed Reality key is disabled")
	}
	privateValue, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt Reality key: %w", err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(string(privateValue)); err != nil {
		return "", errors.New("managed Reality key has invalid format")
	}
	return string(privateValue), nil
}
