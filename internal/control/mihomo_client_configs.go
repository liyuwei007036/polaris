package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sb-control/sb-control/internal/security"
)

type mihomoClientRules struct {
	Rules    []MihomoRule `json:"rules"`
	RawRules string       `json:"raw_rules"`
}

func (s *Store) upgradeLegacyMihomoClientInput(ctx context.Context, config *MihomoClientConfig) error {
	if len(config.EndpointIDs) != 0 || len(config.ProxyGroupIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, groupID := range config.ProxyGroupIDs {
		var encoded, strategy string
		if err := s.db.QueryRowContext(ctx, `SELECT endpoint_ids, strategy FROM mihomo_proxy_groups WHERE id = ?`, groupID).Scan(&encoded, &strategy); err != nil {
			return fmt.Errorf("legacy proxy group %s: %w", groupID, err)
		}
		ids, err := decodeStringList(encoded)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				config.EndpointIDs = append(config.EndpointIDs, id)
			}
		}
		if config.Strategy == "" {
			config.Strategy = strategy
		} else if config.Strategy != strategy {
			config.Strategy = "select"
		}
	}
	if config.RoutingProfileID != "" {
		var encoded string
		if err := s.db.QueryRowContext(ctx, `SELECT rule_preset, rules_json FROM mihomo_routing_profiles WHERE id = ?`, config.RoutingProfileID).Scan(&config.RulePreset, &encoded); err != nil {
			return fmt.Errorf("legacy routing profile: %w", err)
		}
		var rules mihomoRoutingRules
		if err := json.Unmarshal([]byte(encoded), &rules); err != nil {
			return err
		}
		config.Rules, config.RawRules, config.DefaultAction = rules.Rules, formatMihomoRules(rules.Rules), rules.DefaultAction
	}
	return nil
}

func (s *Store) ensureMihomoClientEndpoints(ctx context.Context, endpointIDs []string) error {
	usedNames := map[string]string{}
	for _, endpointID := range endpointIDs {
		var alias, endpointName, listenerName, nodeName, clientAddress, protocol string
		err := s.db.QueryRowContext(ctx, `SELECT e.alias, e.name, l.name, n.name, n.client_address, l.protocol
			FROM endpoints e JOIN listeners l ON l.id = e.listener_id JOIN nodes n ON n.id = l.node_id
			WHERE e.id = ? AND e.enabled = 1 AND l.enabled = 1 AND n.revoked_at IS NULL`, endpointID).
			Scan(&alias, &endpointName, &listenerName, &nodeName, &clientAddress, &protocol)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("access account %s is unavailable: %w", endpointID, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(clientAddress) == "" {
			return fmt.Errorf("node %s has no client connection address", nodeName)
		}
		switch protocol {
		case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "socks", "http":
		default:
			return fmt.Errorf("protocol %s is not supported by Mihomo YAML export", protocol)
		}
		displayName := strings.TrimSpace(alias)
		if displayName == "" {
			displayName = nodeName + " · " + listenerName + " · " + endpointName
		}
		if previous, exists := usedNames[displayName]; exists && previous != endpointID {
			return fmt.Errorf("client node alias %q is used by more than one selected account", displayName)
		}
		usedNames[displayName] = endpointID
	}
	return nil
}

func encodeMihomoClientRules(config MihomoClientConfig) (string, error) {
	encoded, err := json.Marshal(mihomoClientRules{Rules: config.Rules, RawRules: config.RawRules})
	if err != nil {
		return "", fmt.Errorf("encode Mihomo client rules: %w", err)
	}
	return string(encoded), nil
}

func scanMihomoClientConfig(scanner interface{ Scan(...any) error }, masterKey []byte) (MihomoClientConfig, error) {
	var config MihomoClientConfig
	var endpointJSON, rulesJSON string
	var encryptedToken []byte
	var created, updated int64
	if err := scanner.Scan(&config.ID, &config.Name, &endpointJSON, &config.Strategy, &config.RulePreset, &rulesJSON, &config.DefaultAction, &encryptedToken, &created, &updated); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := json.Unmarshal([]byte(endpointJSON), &config.EndpointIDs); err != nil {
		return MihomoClientConfig{}, err
	}
	var rules mihomoClientRules
	if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
		return MihomoClientConfig{}, err
	}
	config.Rules, config.RawRules = rules.Rules, rules.RawRules
	config.CreatedAt, config.UpdatedAt = unixTimeString(created), unixTimeString(updated)
	token, err := security.Decrypt(masterKey, encryptedToken)
	if err != nil {
		return MihomoClientConfig{}, fmt.Errorf("decrypt Mihomo subscription token: %w", err)
	}
	config.SubscriptionPath = "/api/v1/mihomo/subscriptions/" + string(token)
	return config, nil
}

func (s *Store) CreateMihomoClientConfig(ctx context.Context, config MihomoClientConfig) (MihomoClientConfig, error) {
	if err := s.upgradeLegacyMihomoClientInput(ctx, &config); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := normalizeMihomoClientConfig(&config); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := s.ensureMihomoClientEndpoints(ctx, config.EndpointIDs); err != nil {
		return MihomoClientConfig{}, err
	}
	endpointJSON, _ := json.Marshal(config.EndpointIDs)
	rulesJSON, err := encodeMihomoClientRules(config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	config.ID, err = newID()
	if err != nil {
		return MihomoClientConfig{}, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	encrypted, err := security.Encrypt(s.masterKey, []byte(token))
	if err != nil {
		return MihomoClientConfig{}, err
	}
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_client_configs_v2
		(id, name, endpoint_ids, strategy, rule_preset, rules_json, default_action, subscription_token_hash, subscription_token_encrypted, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name, string(endpointJSON), config.Strategy, config.RulePreset, rulesJSON, config.DefaultAction, security.TokenHash(token), encrypted, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoClientConfig{}, ErrConflict
		}
		return MihomoClientConfig{}, fmt.Errorf("create Mihomo client config: %w", err)
	}
	config.CreatedAt, config.UpdatedAt = unixTimeString(now), unixTimeString(now)
	config.SubscriptionPath = "/api/v1/mihomo/subscriptions/" + token
	return config, nil
}

func (s *Store) UpdateMihomoClientConfig(ctx context.Context, config MihomoClientConfig) (MihomoClientConfig, error) {
	if config.ID == "" {
		return MihomoClientConfig{}, errors.New("client config ID is required")
	}
	if err := normalizeMihomoClientConfig(&config); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := s.ensureMihomoClientEndpoints(ctx, config.EndpointIDs); err != nil {
		return MihomoClientConfig{}, err
	}
	endpointJSON, _ := json.Marshal(config.EndpointIDs)
	rulesJSON, err := encodeMihomoClientRules(config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	now := nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v2 SET name = ?, endpoint_ids = ?, strategy = ?, rule_preset = ?, rules_json = ?, default_action = ?, updated_at = ? WHERE id = ?`,
		config.Name, string(endpointJSON), config.Strategy, config.RulePreset, rulesJSON, config.DefaultAction, now, config.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoClientConfig{}, ErrConflict
		}
		return MihomoClientConfig{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MihomoClientConfig{}, ErrNotFound
	}
	return s.mihomoClientConfigByID(ctx, config.ID)
}

func (s *Store) mihomoClientConfigByID(ctx context.Context, id string) (MihomoClientConfig, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, endpoint_ids, strategy, rule_preset, rules_json, default_action, subscription_token_encrypted, created_at, updated_at FROM mihomo_client_configs_v2 WHERE id = ?`, id)
	config, err := scanMihomoClientConfig(row, s.masterKey)
	if errors.Is(err, sql.ErrNoRows) {
		return MihomoClientConfig{}, ErrNotFound
	}
	return config, err
}

func (s *Store) ListMihomoClientConfigs(ctx context.Context) ([]MihomoClientConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, endpoint_ids, strategy, rule_preset, rules_json, default_action, subscription_token_encrypted, created_at, updated_at FROM mihomo_client_configs_v2 ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configs := []MihomoClientConfig{}
	for rows.Next() {
		config, err := scanMihomoClientConfig(rows, s.masterKey)
		if err != nil {
			return nil, err
		}
		configs = append(configs, config)
	}
	return configs, rows.Err()
}

func (s *Store) RotateMihomoClientSubscription(ctx context.Context, configID string) (string, error) {
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	encrypted, err := security.Encrypt(s.masterKey, []byte(token))
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v2 SET subscription_token_hash = ?, subscription_token_encrypted = ?, updated_at = ? WHERE id = ?`, security.TokenHash(token), encrypted, nowUnix(), configID)
	if err != nil {
		return "", err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", ErrNotFound
	}
	return "/api/v1/mihomo/subscriptions/" + token, nil
}

func (s *Store) MihomoClientConfigIDByToken(ctx context.Context, token string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM mihomo_client_configs_v2 WHERE subscription_token_hash = ?`, security.TokenHash(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Store) DeleteMihomoClientConfig(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_client_configs_v2 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GenerateStoredMihomoYAML(ctx context.Context, configID string) (string, string, error) {
	config, err := s.mihomoClientConfigByID(ctx, configID)
	if err != nil {
		return "", "", err
	}
	if err := s.ensureMihomoClientEndpoints(ctx, config.EndpointIDs); err != nil {
		return "", "", err
	}
	yaml, err := s.generateMihomoYAML(ctx, config.Name, "", []mihomoGroupDefinition{{
		Name: "节点选择", Strategy: config.Strategy, EndpointIDs: config.EndpointIDs,
	}}, mihomoRuleDefinition{RulePreset: config.RulePreset, DefaultAction: config.DefaultAction, Rules: config.Rules})
	return config.Name, yaml, err
}

func (s *Store) migrateLegacyMihomoClientConfigs(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, proxy_group_ids, routing_profile_id, subscription_token_hash, subscription_token_encrypted, created_at, updated_at FROM mihomo_client_configs`)
	if err != nil {
		return fmt.Errorf("list legacy Mihomo client configs: %w", err)
	}
	type legacyRow struct {
		config                    MihomoClientConfig
		groupJSON                 string
		tokenHash, encryptedToken []byte
		created, updated          int64
	}
	legacyRows := []legacyRow{}
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.config.ID, &row.config.Name, &row.groupJSON, &row.config.RoutingProfileID, &row.tokenHash, &row.encryptedToken, &row.created, &row.updated); err != nil {
			rows.Close()
			return err
		}
		legacyRows = append(legacyRows, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range legacyRows {
		config := row.config
		if err := json.Unmarshal([]byte(row.groupJSON), &config.ProxyGroupIDs); err != nil {
			return err
		}
		for _, groupID := range config.ProxyGroupIDs {
			var aliasJSON string
			if err := s.db.QueryRowContext(ctx, `SELECT aliases_json FROM mihomo_proxy_groups WHERE id = ?`, groupID).Scan(&aliasJSON); err != nil {
				return err
			}
			var aliases map[string]string
			if err := json.Unmarshal([]byte(aliasJSON), &aliases); err != nil {
				return err
			}
			for endpointID, alias := range aliases {
				alias = strings.TrimSpace(alias)
				if alias != "" {
					if _, err := s.db.ExecContext(ctx, `UPDATE endpoints SET alias = ? WHERE id = ? AND alias = ''`, alias, endpointID); err != nil {
						return err
					}
				}
			}
		}
		if err := s.upgradeLegacyMihomoClientInput(ctx, &config); err != nil {
			return fmt.Errorf("migrate legacy Mihomo config %s: %w", config.Name, err)
		}
		if err := normalizeMihomoClientConfig(&config); err != nil {
			return fmt.Errorf("normalize legacy Mihomo config %s: %w", config.Name, err)
		}
		endpointJSON, _ := json.Marshal(config.EndpointIDs)
		rulesJSON, _ := encodeMihomoClientRules(config)
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mihomo_client_configs_v2
			(id, name, endpoint_ids, strategy, rule_preset, rules_json, default_action, subscription_token_hash, subscription_token_encrypted, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name, string(endpointJSON), config.Strategy, config.RulePreset, rulesJSON, config.DefaultAction, row.tokenHash, row.encryptedToken, row.created, row.updated); err != nil {
			return fmt.Errorf("migrate legacy Mihomo config %s: %w", config.Name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_client_configs`); err != nil {
		return fmt.Errorf("finish legacy Mihomo client config migration: %w", err)
	}
	return nil
}
