package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func validateFirewallRule(rule FirewallRule) error {
	if rule.NodeID == "" {
		return errors.New("firewall rule node is required")
	}
	if rule.Action != "accept" && rule.Action != "drop" {
		return errors.New("firewall action must be accept or drop")
	}
	if rule.Protocol != "tcp" && rule.Protocol != "udp" {
		return errors.New("firewall protocol must be tcp or udp")
	}
	if rule.Port == 0 {
		return errors.New("firewall port is required")
	}
	if rule.ExpiresAt != 0 && rule.ExpiresAt <= time.Now().UTC().Unix() {
		return errors.New("firewall expiration must be in the future")
	}
	if rule.CIDR != "" {
		if _, err := CompileNftables([]FirewallRule{{Action: rule.Action, Protocol: rule.Protocol, CIDR: rule.CIDR, Port: rule.Port, Enabled: true}}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateFirewallRule(ctx context.Context, rule FirewallRule) (FirewallRule, error) {
	if err := validateFirewallRule(rule); err != nil {
		return FirewallRule{}, err
	}
	rule.Location = ""
	var node int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id=? AND revoked_at IS NULL`, rule.NodeID).Scan(&node); errors.Is(err, sql.ErrNoRows) {
		return FirewallRule{}, ErrNotFound
	} else if err != nil {
		return FirewallRule{}, err
	}
	id, err := newID()
	if err != nil {
		return FirewallRule{}, err
	}
	rule.ID = id
	_, err = s.db.ExecContext(ctx, `INSERT INTO firewall_rules (id,node_id,action,protocol,cidr,port,expires_at,enabled,created_at,updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, rule.ID, rule.NodeID, rule.Action, rule.Protocol, rule.CIDR, rule.Port, nullableInt64(rule.ExpiresAt), rule.Enabled, nowUnix(), nowUnix())
	if err != nil {
		return FirewallRule{}, fmt.Errorf("create firewall rule: %w", err)
	}
	return rule, nil
}

func (s *Store) ListFirewallRules(ctx context.Context, nodeID string) ([]FirewallRule, error) {
	query := `SELECT id,node_id,action,protocol,cidr,port,COALESCE(expires_at,0),enabled FROM firewall_rules`
	args := []any{}
	if nodeID != "" {
		query += " WHERE node_id=?"
		args = append(args, nodeID)
	}
	query += " ORDER BY node_id,protocol,port,cidr,id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list firewall rules: %w", err)
	}
	defer rows.Close()
	var rules []FirewallRule
	for rows.Next() {
		var rule FirewallRule
		if err := rows.Scan(&rule.ID, &rule.NodeID, &rule.Action, &rule.Protocol, &rule.CIDR, &rule.Port, &rule.ExpiresAt, &rule.Enabled); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) SetFirewallRuleEnabled(ctx context.Context, ruleID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE firewall_rules SET enabled=?,updated_at=? WHERE id=?`, enabled, nowUnix(), ruleID)
	if err != nil {
		return fmt.Errorf("set firewall rule state: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) DeleteFirewallRule(ctx context.Context, ruleID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM firewall_rules WHERE id=?`, ruleID)
	if err != nil {
		return fmt.Errorf("delete firewall rule: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) CompileNodeFirewall(ctx context.Context, nodeID string) (string, error) {
	rules, err := s.ListFirewallRules(ctx, nodeID)
	if err != nil {
		return "", err
	}
	return CompileNftables(rules)
}
