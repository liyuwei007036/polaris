package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/security"
)

// Outbound is a managed egress definition, shared globally across every node:
// it describes where traffic exits to, independent of which node's listener
// uses it. A node's compiled sing-box config always contains a built-in
// "direct" outbound; managed outbounds add proxy egress (SOCKS5/HTTP) that any
// listener on any node can select as its default route.
type Outbound struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // direct | socks | http
	Server     string `json:"server,omitempty"`
	ServerPort uint16 `json:"server_port,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"` // write-only: accepted on create/update, never returned
	Enabled    bool   `json:"enabled"`
	// ExpiresAt records when the upstream account the outbound uses runs out.
	// It is recorded for the operator's benefit only: nothing in the system
	// disables or hides an outbound when the date passes.
	ExpiresAt string `json:"expires_at,omitempty"`
}

type outboundSecret struct {
	Password string `json:"password"`
}

func validateOutbound(o Outbound) error {
	if o.Name == "" || len(o.Name) > 128 {
		return errors.New("a name up to 128 characters is required")
	}
	switch o.Type {
	case "direct":
	case "socks", "http":
		if o.Server == "" || o.ServerPort == 0 {
			return errors.New("proxy outbound requires a server address and port")
		}
	default:
		return fmt.Errorf("unsupported outbound type %q", o.Type)
	}
	return nil
}

// normalizeOutbound blanks proxy-only fields for a direct outbound so stored
// rows stay clean.
func normalizeOutbound(o *Outbound) {
	if o.Type == "direct" {
		o.Server = ""
		o.ServerPort = 0
		o.Username = ""
		o.Password = ""
	}
	o.ExpiresAt = strings.TrimSpace(o.ExpiresAt)
}

// outboundExpiry converts the submitted expiry into the nullable column value.
// An empty value clears the date; anything else must be a full timestamp so
// the console can render it in the visitor's own time zone.
func outboundExpiry(value string) (sql.NullInt64, error) {
	if value == "" {
		return sql.NullInt64{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return sql.NullInt64{}, errors.New("outbound expiry must be an RFC 3339 timestamp")
	}
	return sql.NullInt64{Int64: parsed.UTC().Unix(), Valid: true}, nil
}

func outboundExpiryString(value sql.NullInt64) string {
	if !value.Valid {
		return ""
	}
	return time.Unix(value.Int64, 0).UTC().Format(time.RFC3339)
}

func (s *Store) CreateOutbound(ctx context.Context, o Outbound) (Outbound, error) {
	normalizeOutbound(&o)
	if err := validateOutbound(o); err != nil {
		return Outbound{}, err
	}
	var conflict string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM outbounds WHERE name = ?`, o.Name).Scan(&conflict); err == nil {
		return Outbound{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, fmt.Errorf("check outbound name conflict: %w", err)
	}
	encrypted, err := s.encryptOutboundSecret(o.Password)
	if err != nil {
		return Outbound{}, err
	}
	expiry, err := outboundExpiry(o.ExpiresAt)
	if err != nil {
		return Outbound{}, err
	}
	o.ID, err = newID()
	if err != nil {
		return Outbound{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO outbounds (id, name, type, server, server_port, username, credentials, enabled, expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, o.ID, o.Name, o.Type, o.Server, o.ServerPort, o.Username, encrypted, o.Enabled, expiry, nowUnix(), nowUnix())
	if err != nil {
		return Outbound{}, fmt.Errorf("create outbound: %w", err)
	}
	o.Password = ""
	return o, nil
}

func (s *Store) UpdateOutbound(ctx context.Context, o Outbound) (Outbound, error) {
	if o.ID == "" {
		return Outbound{}, errors.New("outbound ID is required")
	}
	normalizeOutbound(&o)
	if err := validateOutbound(o); err != nil {
		return Outbound{}, err
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM outbounds WHERE id = ?`, o.ID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, ErrNotFound
	}
	if err != nil {
		return Outbound{}, fmt.Errorf("load outbound: %w", err)
	}
	if !o.Enabled {
		referenced, err := s.outboundReferenced(ctx, o.ID)
		if err != nil {
			return Outbound{}, err
		}
		if referenced {
			return Outbound{}, ErrConflict
		}
	}
	var conflict string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM outbounds WHERE name = ? AND id <> ?`, o.Name, o.ID).Scan(&conflict)
	if err == nil {
		return Outbound{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, fmt.Errorf("check outbound name conflict: %w", err)
	}
	expiry, err := outboundExpiry(o.ExpiresAt)
	if err != nil {
		return Outbound{}, err
	}
	// A blank password on update keeps the stored secret; supplying one replaces it.
	if o.Password == "" && o.Type != "direct" {
		_, err = s.db.ExecContext(ctx, `UPDATE outbounds SET name = ?, type = ?, server = ?, server_port = ?, username = ?, enabled = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
			o.Name, o.Type, o.Server, o.ServerPort, o.Username, o.Enabled, expiry, nowUnix(), o.ID)
	} else {
		encrypted, encErr := s.encryptOutboundSecret(o.Password)
		if encErr != nil {
			return Outbound{}, encErr
		}
		_, err = s.db.ExecContext(ctx, `UPDATE outbounds SET name = ?, type = ?, server = ?, server_port = ?, username = ?, credentials = ?, enabled = ?, expires_at = ?, updated_at = ? WHERE id = ?`,
			o.Name, o.Type, o.Server, o.ServerPort, o.Username, encrypted, o.Enabled, expiry, nowUnix(), o.ID)
	}
	if err != nil {
		return Outbound{}, fmt.Errorf("update outbound: %w", err)
	}
	o.Password = ""
	return o, nil
}

// ListOutbounds returns every managed outbound (they are global, not scoped to
// a node) without decrypting secrets; passwords are never exposed through the
// API.
func (s *Store) ListOutbounds(ctx context.Context) ([]Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, server, server_port, username, enabled, expires_at FROM outbounds ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list outbounds: %w", err)
	}
	defer rows.Close()
	var outbounds []Outbound
	for rows.Next() {
		var o Outbound
		var expiry sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Name, &o.Type, &o.Server, &o.ServerPort, &o.Username, &o.Enabled, &expiry); err != nil {
			return nil, fmt.Errorf("read outbound: %w", err)
		}
		o.ExpiresAt = outboundExpiryString(expiry)
		outbounds = append(outbounds, o)
	}
	return outbounds, rows.Err()
}

func (s *Store) outboundForTest(ctx context.Context, outboundID string) (Outbound, error) {
	var outbound Outbound
	var encrypted []byte
	err := s.db.QueryRowContext(ctx, `SELECT id, name, type, server, server_port, username, credentials, enabled FROM outbounds WHERE id = ?`, outboundID).
		Scan(&outbound.ID, &outbound.Name, &outbound.Type, &outbound.Server, &outbound.ServerPort, &outbound.Username, &encrypted, &outbound.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, ErrNotFound
	}
	if err != nil {
		return Outbound{}, fmt.Errorf("load outbound for test: %w", err)
	}
	if outbound.Type == "direct" {
		return Outbound{}, errors.New("the built-in direct outbound does not require testing")
	}
	if len(encrypted) > 0 {
		plain, err := security.Decrypt(s.masterKey, encrypted)
		if err != nil {
			return Outbound{}, fmt.Errorf("decrypt outbound secret: %w", err)
		}
		var secret outboundSecret
		if err := json.Unmarshal(plain, &secret); err != nil {
			return Outbound{}, fmt.Errorf("decode outbound secret: %w", err)
		}
		outbound.Password = secret.Password
	}
	return outbound, nil
}

// loadEnabledOutbounds returns every enabled outbound with decrypted passwords
// for configuration compilation. It never leaves the compiler.
func (s *Store) loadEnabledOutbounds(ctx context.Context) ([]Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, server, server_port, username, credentials FROM outbounds WHERE enabled = 1 ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("load outbounds: %w", err)
	}
	defer rows.Close()
	var outbounds []Outbound
	for rows.Next() {
		var o Outbound
		var encrypted []byte
		if err := rows.Scan(&o.ID, &o.Name, &o.Type, &o.Server, &o.ServerPort, &o.Username, &encrypted); err != nil {
			return nil, fmt.Errorf("read outbound: %w", err)
		}
		if len(encrypted) > 0 {
			plain, err := security.Decrypt(s.masterKey, encrypted)
			if err != nil {
				return nil, fmt.Errorf("decrypt outbound secret: %w", err)
			}
			var secret outboundSecret
			if err := json.Unmarshal(plain, &secret); err != nil {
				return nil, fmt.Errorf("decode outbound secret: %w", err)
			}
			o.Password = secret.Password
		}
		outbounds = append(outbounds, o)
	}
	return outbounds, rows.Err()
}

func (s *Store) SetOutboundEnabled(ctx context.Context, outboundID string, enabled bool) error {
	if !enabled {
		referenced, err := s.outboundReferenced(ctx, outboundID)
		if err != nil {
			return err
		}
		if referenced {
			return ErrConflict
		}
	}
	updated, err := s.db.ExecContext(ctx, `UPDATE outbounds SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), outboundID)
	if err != nil {
		return fmt.Errorf("set outbound state: %w", err)
	}
	count, _ := updated.RowsAffected()
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// DeleteOutbound removes an outbound and detaches any listener that referenced
// it, falling those listeners back to the direct outbound.
func (s *Store) DeleteOutbound(ctx context.Context, outboundID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE listeners SET outbound_id = '', updated_at = ? WHERE outbound_id = ?`, nowUnix(), outboundID); err != nil {
		return fmt.Errorf("detach listeners from outbound: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE endpoints SET outbound_id = 'direct', updated_at = ? WHERE outbound_id = ?`, nowUnix(), outboundID); err != nil {
		return fmt.Errorf("detach endpoints from outbound: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE route_rules SET action = 'direct', outbound_tag = '', updated_at = ? WHERE action = 'outbound' AND outbound_tag = ?`, nowUnix(), outboundID); err != nil {
		return fmt.Errorf("detach route rules from outbound: %w", err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM outbounds WHERE id = ?`, outboundID)
	if err != nil {
		return fmt.Errorf("delete outbound: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbound deletion: %w", err)
	}
	if count != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (s *Store) encryptOutboundSecret(password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	plain, err := json.Marshal(outboundSecret{Password: password})
	if err != nil {
		return nil, err
	}
	encrypted, err := security.Encrypt(s.masterKey, plain)
	if err != nil {
		return nil, err
	}
	return encrypted, nil
}

// ensureOutboundExists verifies a listener's selected outbound exists,
// preventing listeners from referencing a dangling outbound tag. Outbounds
// are global, so any listener on any node may reference any of them.
func (s *Store) ensureOutboundExists(ctx context.Context, outboundID string) error {
	if outboundID == "" {
		return nil
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM outbounds WHERE id = ?`, outboundID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("selected outbound does not exist")
	}
	return err
}

func (s *Store) ensureEndpointOutboundExists(ctx context.Context, outboundID string) error {
	if outboundID == "" || outboundID == "direct" {
		return nil
	}
	return s.ensureOutboundExists(ctx, outboundID)
}

func (s *Store) ensureEnabledOutboundExists(ctx context.Context, outboundID string) error {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM outbounds WHERE id = ? AND enabled = 1`, outboundID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("selected outbound does not exist or is disabled")
	}
	return err
}

func (s *Store) outboundReferenced(ctx context.Context, outboundID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM listeners WHERE outbound_id = ?) +
		(SELECT COUNT(*) FROM endpoints WHERE outbound_id = ?) +
		(SELECT COUNT(*) FROM route_rules WHERE action = 'outbound' AND outbound_tag = ?)`,
		outboundID, outboundID, outboundID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check outbound references: %w", err)
	}
	return count > 0, nil
}

func (s *Store) OutboundNodeIDs(ctx context.Context, outboundID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id FROM listeners WHERE outbound_id = ?
		UNION SELECT l.node_id FROM endpoints e JOIN listeners l ON l.id = e.listener_id WHERE e.outbound_id = ?
		UNION SELECT node_id FROM route_rules WHERE action = 'outbound' AND outbound_tag = ?
		ORDER BY 1`, outboundID, outboundID, outboundID)
	if err != nil {
		return nil, fmt.Errorf("list outbound nodes: %w", err)
	}
	defer rows.Close()
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("read outbound node: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, rows.Err()
}
