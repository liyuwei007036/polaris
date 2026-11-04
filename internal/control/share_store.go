package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ShareLink is a read-only, expiring link to one console page. The raw token
// is never stored: only its SHA-256 hash lives in the database, so a leaked
// database copy cannot be turned into a working share URL.
type ShareLink struct {
	ID        string `json:"id"`
	Page      string `json:"page"`
	CreatedBy string `json:"created_by"`
	ExpiresAt string `json:"expires_at"`
	CreatedAt string `json:"created_at"`
	Expired   bool   `json:"expired"`
}

const (
	sharePageNodes     = "nodes"
	sharePageDashboard = "dashboard"
)

func (s *Store) initShareLinksTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS share_links (
  id TEXT PRIMARY KEY,
  token_hash BLOB NOT NULL UNIQUE,
  page TEXT NOT NULL,
  created_by TEXT NOT NULL REFERENCES operators(id),
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_share_links_expires ON share_links(expires_at);
`)
	return err
}

// CreateShareLink persists a hashed share token. The caller keeps the raw
// token in memory just long enough to show it to the operator once.
func (s *Store) CreateShareLink(ctx context.Context, operatorID, page string, tokenHash []byte, expiresAt time.Time) (ShareLink, error) {
	if err := s.initShareLinksTable(ctx); err != nil {
		return ShareLink{}, fmt.Errorf("init share links: %w", err)
	}
	id, err := newID()
	if err != nil {
		return ShareLink{}, err
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO share_links (id, token_hash, page, created_by, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, tokenHash, page, operatorID, expiresAt.UTC().Unix(), now.Unix()); err != nil {
		return ShareLink{}, fmt.Errorf("create share link: %w", err)
	}
	return ShareLink{
		ID: id, Page: page, CreatedBy: operatorID,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		CreatedAt: now.Format(time.RFC3339),
	}, nil
}

// FindShareLink resolves a live share token. Expired and revoked rows are
// deliberately indistinguishable from missing ones to the public caller.
func (s *Store) FindShareLink(ctx context.Context, tokenHash []byte) (ShareLink, error) {
	if err := s.initShareLinksTable(ctx); err != nil {
		return ShareLink{}, fmt.Errorf("init share links: %w", err)
	}
	var link ShareLink
	var expiresAt, createdAt int64
	err := s.db.QueryRowContext(ctx, `SELECT id, page, created_by, expires_at, created_at FROM share_links
		WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?`, tokenHash, time.Now().UTC().Unix()).
		Scan(&link.ID, &link.Page, &link.CreatedBy, &expiresAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ShareLink{}, ErrNotFound
	}
	if err != nil {
		return ShareLink{}, fmt.Errorf("find share link: %w", err)
	}
	link.ExpiresAt = time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
	link.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
	return link, nil
}

// ListShareLinks returns links that have neither expired nor been revoked.
func (s *Store) ListShareLinks(ctx context.Context) ([]ShareLink, error) {
	if err := s.initShareLinksTable(ctx); err != nil {
		return nil, fmt.Errorf("init share links: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, page, created_by, expires_at, created_at FROM share_links
		WHERE revoked_at IS NULL AND expires_at > ? ORDER BY created_at DESC`, time.Now().UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("list share links: %w", err)
	}
	defer rows.Close()
	links := []ShareLink{}
	for rows.Next() {
		var link ShareLink
		var expiresAt, createdAt int64
		if err := rows.Scan(&link.ID, &link.Page, &link.CreatedBy, &expiresAt, &createdAt); err != nil {
			return nil, fmt.Errorf("read share link: %w", err)
		}
		link.ExpiresAt = time.Unix(expiresAt, 0).UTC().Format(time.RFC3339)
		link.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *Store) RevokeShareLink(ctx context.Context, id string) error {
	if err := s.initShareLinksTable(ctx); err != nil {
		return fmt.Errorf("init share links: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE share_links SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("revoke share link: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
