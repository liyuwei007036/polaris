package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) CreateFail2BanJail(ctx context.Context, jail Fail2BanJail) (Fail2BanJail, error) {
	if jail.NodeID == "" {
		return Fail2BanJail{}, errors.New("fail2ban jail node is required")
	}
	if err := validateFail2BanJail(jail); err != nil {
		return Fail2BanJail{}, err
	}
	var node int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id=? AND revoked_at IS NULL`, jail.NodeID).Scan(&node); errors.Is(err, sql.ErrNoRows) {
		return Fail2BanJail{}, ErrNotFound
	} else if err != nil {
		return Fail2BanJail{}, err
	}
	id, err := newID()
	if err != nil {
		return Fail2BanJail{}, err
	}
	jail.ID = id
	_, err = s.db.ExecContext(ctx, `INSERT INTO fail2ban_jails (id,node_id,name,log_path,filter_name,fail_regex,max_retry,find_time_seconds,ban_time_seconds,enabled,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		jail.ID, jail.NodeID, jail.Name, jail.LogPath, jail.FilterName, jail.FailRegex, jail.MaxRetry, jail.FindTimeSeconds, jail.BanTimeSeconds, jail.Enabled, nowUnix(), nowUnix())
	if err != nil {
		return Fail2BanJail{}, fmt.Errorf("create fail2ban jail: %w", err)
	}
	return jail, nil
}

func (s *Store) UpdateFail2BanJail(ctx context.Context, jail Fail2BanJail) (Fail2BanJail, error) {
	if jail.ID == "" {
		return Fail2BanJail{}, errors.New("fail2ban jail ID is required")
	}
	if err := validateFail2BanJail(jail); err != nil {
		return Fail2BanJail{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE fail2ban_jails SET name=?,log_path=?,filter_name=?,fail_regex=?,max_retry=?,find_time_seconds=?,ban_time_seconds=?,enabled=?,updated_at=? WHERE id=?`,
		jail.Name, jail.LogPath, jail.FilterName, jail.FailRegex, jail.MaxRetry, jail.FindTimeSeconds, jail.BanTimeSeconds, jail.Enabled, nowUnix(), jail.ID)
	if err != nil {
		return Fail2BanJail{}, fmt.Errorf("update fail2ban jail: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Fail2BanJail{}, ErrNotFound
	}
	var nodeID string
	if err := s.db.QueryRowContext(ctx, `SELECT node_id FROM fail2ban_jails WHERE id=?`, jail.ID).Scan(&nodeID); err != nil {
		return Fail2BanJail{}, fmt.Errorf("load fail2ban jail node: %w", err)
	}
	jail.NodeID = nodeID
	return jail, nil
}

func (s *Store) ListFail2BanJails(ctx context.Context, nodeID string) ([]Fail2BanJail, error) {
	query := `SELECT id,node_id,name,log_path,filter_name,fail_regex,max_retry,find_time_seconds,ban_time_seconds,enabled FROM fail2ban_jails`
	args := []any{}
	if nodeID != "" {
		query += " WHERE node_id=?"
		args = append(args, nodeID)
	}
	query += " ORDER BY node_id,name,id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list fail2ban jails: %w", err)
	}
	defer rows.Close()
	var jails []Fail2BanJail
	for rows.Next() {
		var jail Fail2BanJail
		if err := rows.Scan(&jail.ID, &jail.NodeID, &jail.Name, &jail.LogPath, &jail.FilterName, &jail.FailRegex, &jail.MaxRetry, &jail.FindTimeSeconds, &jail.BanTimeSeconds, &jail.Enabled); err != nil {
			return nil, err
		}
		jails = append(jails, jail)
	}
	return jails, rows.Err()
}

func (s *Store) SetFail2BanJailEnabled(ctx context.Context, jailID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE fail2ban_jails SET enabled=?,updated_at=? WHERE id=?`, enabled, nowUnix(), jailID)
	if err != nil {
		return fmt.Errorf("set fail2ban jail state: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteFail2BanJail(ctx context.Context, jailID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM fail2ban_jails WHERE id=?`, jailID)
	if err != nil {
		return fmt.Errorf("delete fail2ban jail: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

// CompileNodeFail2Ban renders the jail file and filter files for one node.
// Publishing an empty set is valid: it removes every managed jail on the node.
func (s *Store) CompileNodeFail2Ban(ctx context.Context, nodeID string) (string, map[string]string, error) {
	jails, err := s.ListFail2BanJails(ctx, nodeID)
	if err != nil {
		return "", nil, err
	}
	return CompileFail2Ban(jails)
}
