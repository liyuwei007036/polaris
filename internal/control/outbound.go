package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sb-control/sb-control/internal/security"
)

// Outbound is a managed egress definition. A node's compiled sing-box config
// always contains a built-in "direct" outbound; managed outbounds add proxy
// egress (SOCKS5/HTTP) that listeners can select as their default route.
type Outbound struct {
	ID         string `json:"id"`
	NodeID     string `json:"node_id"`
	Name       string `json:"name"`
	Type       string `json:"type"` // direct | socks | http
	Server     string `json:"server,omitempty"`
	ServerPort uint16 `json:"server_port,omitempty"`
	Username   string `json:"username,omitempty"`
	Password   string `json:"password,omitempty"` // write-only: accepted on create/update, never returned
	Enabled    bool   `json:"enabled"`
}

type outboundSecret struct {
	Password string `json:"password"`
}

func validateOutbound(o Outbound) error {
	if o.NodeID == "" || o.Name == "" || len(o.Name) > 128 {
		return errors.New("outbound node and a name up to 128 characters are required")
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
}

func (s *Store) CreateOutbound(ctx context.Context, o Outbound) (Outbound, error) {
	normalizeOutbound(&o)
	if err := validateOutbound(o); err != nil {
		return Outbound{}, err
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, o.NodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Outbound{}, ErrNotFound
		}
		return Outbound{}, err
	}
	var conflict string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM outbounds WHERE node_id = ? AND name = ?`, o.NodeID, o.Name).Scan(&conflict); err == nil {
		return Outbound{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, fmt.Errorf("check outbound name conflict: %w", err)
	}
	encrypted, err := s.encryptOutboundSecret(o.Password)
	if err != nil {
		return Outbound{}, err
	}
	o.ID, err = newID()
	if err != nil {
		return Outbound{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO outbounds (id, node_id, name, type, server, server_port, username, credentials, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, o.ID, o.NodeID, o.Name, o.Type, o.Server, o.ServerPort, o.Username, encrypted, o.Enabled, nowUnix(), nowUnix())
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
	var existingNode string
	err := s.db.QueryRowContext(ctx, `SELECT node_id FROM outbounds WHERE id = ?`, o.ID).Scan(&existingNode)
	if errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, ErrNotFound
	}
	if err != nil {
		return Outbound{}, fmt.Errorf("load outbound: %w", err)
	}
	if existingNode != o.NodeID {
		return Outbound{}, ErrForbidden
	}
	var conflict string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM outbounds WHERE node_id = ? AND name = ? AND id <> ?`, o.NodeID, o.Name, o.ID).Scan(&conflict)
	if err == nil {
		return Outbound{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Outbound{}, fmt.Errorf("check outbound name conflict: %w", err)
	}
	// A blank password on update keeps the stored secret; supplying one replaces it.
	if o.Password == "" && o.Type != "direct" {
		_, err = s.db.ExecContext(ctx, `UPDATE outbounds SET name = ?, type = ?, server = ?, server_port = ?, username = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			o.Name, o.Type, o.Server, o.ServerPort, o.Username, o.Enabled, nowUnix(), o.ID)
	} else {
		encrypted, encErr := s.encryptOutboundSecret(o.Password)
		if encErr != nil {
			return Outbound{}, encErr
		}
		_, err = s.db.ExecContext(ctx, `UPDATE outbounds SET name = ?, type = ?, server = ?, server_port = ?, username = ?, credentials = ?, enabled = ?, updated_at = ? WHERE id = ?`,
			o.Name, o.Type, o.Server, o.ServerPort, o.Username, encrypted, o.Enabled, nowUnix(), o.ID)
	}
	if err != nil {
		return Outbound{}, fmt.Errorf("update outbound: %w", err)
	}
	o.Password = ""
	return o, nil
}

// ListOutbounds returns managed outbounds without decrypting secrets; passwords
// are never exposed through the API.
func (s *Store) ListOutbounds(ctx context.Context, nodeID string) ([]Outbound, error) {
	query := `SELECT id, node_id, name, type, server, server_port, username, enabled FROM outbounds`
	args := []any{}
	if nodeID != "" {
		query += " WHERE node_id = ?"
		args = append(args, nodeID)
	}
	query += " ORDER BY node_id, name, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list outbounds: %w", err)
	}
	defer rows.Close()
	var outbounds []Outbound
	for rows.Next() {
		var o Outbound
		if err := rows.Scan(&o.ID, &o.NodeID, &o.Name, &o.Type, &o.Server, &o.ServerPort, &o.Username, &o.Enabled); err != nil {
			return nil, fmt.Errorf("read outbound: %w", err)
		}
		outbounds = append(outbounds, o)
	}
	return outbounds, rows.Err()
}

// loadEnabledOutbounds returns enabled outbounds with decrypted passwords for
// configuration compilation. It never leaves the compiler.
func (s *Store) loadEnabledOutbounds(ctx context.Context, nodeID string) ([]Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, name, type, server, server_port, username, credentials FROM outbounds WHERE node_id = ? AND enabled = 1 ORDER BY name, id`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("load outbounds: %w", err)
	}
	defer rows.Close()
	var outbounds []Outbound
	for rows.Next() {
		var o Outbound
		var encrypted []byte
		if err := rows.Scan(&o.ID, &o.NodeID, &o.Name, &o.Type, &o.Server, &o.ServerPort, &o.Username, &encrypted); err != nil {
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

// ensureOutboundOnNode verifies a listener's selected outbound exists on the
// same node, preventing listeners from referencing a dangling outbound tag.
func (s *Store) ensureOutboundOnNode(ctx context.Context, nodeID, outboundID string) error {
	if outboundID == "" {
		return nil
	}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM outbounds WHERE id = ? AND node_id = ?`, outboundID, nodeID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("selected outbound does not exist on this node")
	}
	return err
}
