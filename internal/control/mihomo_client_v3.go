package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sb-control/sb-control/internal/security"
)

type mihomoClientRulesV3 struct {
	Rules    []MihomoRule `json:"rules"`
	RawRules string       `json:"raw_rules"`
}

func isReservedMihomoClientName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "PROXY", "DIRECT", "REJECT":
		return true
	default:
		return false
	}
}

func normalizeMihomoClientRule(rule MihomoRule, actions map[string]string) (MihomoRule, error) {
	rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
	rule.Value = strings.TrimSpace(rule.Value)
	rule.Action = strings.TrimSpace(rule.Action)
	if !supportedMihomoRuleTypes[rule.Type] {
		return MihomoRule{}, fmt.Errorf("unsupported Mihomo rule type %q", rule.Type)
	}
	if rule.Type == "MATCH" {
		rule.Value = ""
	} else if rule.Value == "" || strings.ContainsAny(rule.Value, "\r\n") {
		return MihomoRule{}, errors.New("Mihomo rule value is required and cannot contain line breaks")
	}
	if normalized, ok := actions[strings.ToUpper(rule.Action)]; ok {
		rule.Action = normalized
	} else {
		return MihomoRule{}, fmt.Errorf("Mihomo rule action %q does not name a proxy group, DIRECT, or REJECT", rule.Action)
	}
	if rule.NoResolve && !mihomoNoResolveRuleTypes[rule.Type] {
		return MihomoRule{}, errors.New("no-resolve is only valid for target IP rules")
	}
	return rule, nil
}

func parseMihomoClientRawRules(raw string, actions map[string]string) ([]MihomoRule, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	rules := make([]MihomoRule, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		parts, err := splitMihomoRuleFields(line)
		if err != nil {
			return nil, fmt.Errorf("Mihomo rule line %d: %w", index+1, err)
		}
		var rule MihomoRule
		if len(parts) == 2 && strings.EqualFold(parts[0], "MATCH") {
			rule = MihomoRule{Type: "MATCH", Action: parts[1]}
		} else if len(parts) >= 3 {
			rule = MihomoRule{Type: parts[0], Value: parts[1], Action: parts[2]}
			for _, option := range parts[3:] {
				if strings.EqualFold(option, "no-resolve") {
					rule.NoResolve = true
				} else if option != "" {
					return nil, fmt.Errorf("Mihomo rule line %d has unsupported option %q", index+1, option)
				}
			}
		} else {
			return nil, fmt.Errorf("Mihomo rule line %d is incomplete", index+1)
		}
		normalized, err := normalizeMihomoClientRule(rule, actions)
		if err != nil {
			return nil, fmt.Errorf("Mihomo rule line %d: %w", index+1, err)
		}
		rules = append(rules, normalized)
	}
	return rules, nil
}

func validateMihomoClientTerminalRule(rules []MihomoRule) error {
	if len(rules) == 0 {
		return errors.New("at least one manually configured routing rule is required")
	}
	for index, rule := range rules {
		if rule.Type == "MATCH" && index != len(rules)-1 {
			return errors.New("MATCH must be the last Mihomo rule")
		}
	}
	if rules[len(rules)-1].Type != "MATCH" {
		return errors.New("the last manually configured routing rule must be MATCH")
	}
	return nil
}

func normalizeMihomoClientGroups(groups []MihomoClientGroup) ([]MihomoClientGroup, map[string]string, error) {
	if len(groups) == 0 {
		return nil, nil, errors.New("at least one proxy group is required")
	}
	ids := make(map[string]MihomoClientGroup, len(groups))
	names := make(map[string]string, len(groups))
	for index := range groups {
		group := &groups[index]
		group.ID = strings.TrimSpace(group.ID)
		group.Name = strings.TrimSpace(group.Name)
		if group.ID == "" || len(group.ID) > 128 || strings.ContainsAny(group.ID, "\r\n,") {
			return nil, nil, errors.New("proxy group ID is required and must be at most 128 characters")
		}
		if _, exists := ids[group.ID]; exists {
			return nil, nil, fmt.Errorf("proxy group ID %q is duplicated", group.ID)
		}
		if err := validateMihomoName(group.Name); err != nil {
			return nil, nil, fmt.Errorf("proxy group %s: %w", group.ID, err)
		}
		if isReservedMihomoClientName(group.Name) {
			return nil, nil, fmt.Errorf("proxy group name %q is reserved by Mihomo", group.Name)
		}
		upperName := strings.ToUpper(group.Name)
		if _, exists := names[upperName]; exists {
			return nil, nil, fmt.Errorf("proxy group name %q is duplicated", group.Name)
		}
		switch group.Strategy {
		case "select", "url-test", "fallback":
		default:
			return nil, nil, fmt.Errorf("proxy group %q has an unsupported strategy", group.Name)
		}
		if len(group.Members) == 0 {
			return nil, nil, fmt.Errorf("proxy group %q requires at least one node or group", group.Name)
		}
		seenMembers := map[string]bool{}
		for memberIndex := range group.Members {
			member := &group.Members[memberIndex]
			member.Kind = strings.ToLower(strings.TrimSpace(member.Kind))
			member.ID = strings.TrimSpace(member.ID)
			if (member.Kind != "endpoint" && member.Kind != "group") || member.ID == "" {
				return nil, nil, fmt.Errorf("proxy group %q contains an invalid member", group.Name)
			}
			key := member.Kind + ":" + member.ID
			if seenMembers[key] {
				return nil, nil, fmt.Errorf("proxy group %q contains duplicate members", group.Name)
			}
			seenMembers[key] = true
		}
		ids[group.ID] = *group
		names[upperName] = group.Name
	}
	for _, group := range groups {
		for _, member := range group.Members {
			if member.Kind != "group" {
				continue
			}
			if member.ID == group.ID {
				return nil, nil, fmt.Errorf("proxy group %q cannot contain itself", group.Name)
			}
			if _, exists := ids[member.ID]; !exists {
				return nil, nil, fmt.Errorf("proxy group %q references an unknown group", group.Name)
			}
		}
	}
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(groupID string) error {
		if state[groupID] == 1 {
			return errors.New("proxy groups contain a circular reference")
		}
		if state[groupID] == 2 {
			return nil
		}
		state[groupID] = 1
		for _, member := range ids[groupID].Members {
			if member.Kind == "group" {
				if err := visit(member.ID); err != nil {
					return err
				}
			}
		}
		state[groupID] = 2
		return nil
	}
	for groupID := range ids {
		if err := visit(groupID); err != nil {
			return nil, nil, err
		}
	}
	return groups, names, nil
}

func (s *Store) normalizeMihomoClientConfigV3(ctx context.Context, config *MihomoClientConfig) error {
	config.Name = strings.TrimSpace(config.Name)
	if err := validateMihomoName(config.Name); err != nil {
		return err
	}
	if len(config.EndpointIDs) != 0 || config.Strategy != "" || config.RulePreset != "" || config.DefaultAction != "" || len(config.Groups) != 0 || config.RoutingProfileID != "" {
		return errors.New("legacy client configuration fields are no longer supported")
	}
	groupIDs, err := normalizeUniqueIDs(config.ProxyGroupIDs)
	if err != nil {
		return err
	}
	config.ProxyGroupIDs = groupIDs
	groups, err := s.resolveMihomoProxyGroups(ctx, groupIDs)
	if err != nil {
		return err
	}
	groupNames := make(map[string]string, len(groups))
	for _, group := range groups {
		groupNames[strings.ToUpper(group.Name)] = group.Name
	}
	endpointNamesByID := map[string]string{}
	endpointIDsByName := map[string]string{}
	for _, group := range groups {
		for _, member := range group.Members {
			if member.Kind != "endpoint" {
				continue
			}
			if _, exists := endpointNamesByID[member.ID]; exists {
				continue
			}
			proxy, err := s.mihomoProxy(ctx, member.ID, "")
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("access account %s is unavailable: %w", member.ID, ErrNotFound)
			}
			if err != nil {
				return err
			}
			name, ok := proxy["name"].(string)
			if !ok || strings.TrimSpace(name) == "" {
				return errors.New("generated Mihomo node name is invalid")
			}
			if isReservedMihomoClientName(name) {
				return fmt.Errorf("client node alias %q is reserved by Mihomo", name)
			}
			upperName := strings.ToUpper(name)
			if previousID, exists := endpointIDsByName[upperName]; exists && previousID != member.ID {
				return fmt.Errorf("client node alias %q is used by more than one selected account", name)
			}
			if _, exists := groupNames[upperName]; exists {
				return fmt.Errorf("client node alias %q conflicts with a proxy group name", name)
			}
			endpointNamesByID[member.ID] = name
			endpointIDsByName[upperName] = member.ID
		}
	}
	actions := map[string]string{"DIRECT": "DIRECT", "REJECT": "REJECT"}
	for upper, name := range groupNames {
		actions[upper] = name
	}
	config.RuleMode = strings.ToLower(strings.TrimSpace(config.RuleMode))
	if config.RuleMode == "" {
		config.RuleMode = "table"
	}
	switch config.RuleMode {
	case "table":
		normalized := make([]MihomoRule, 0, len(config.Rules))
		for _, rule := range config.Rules {
			rule, err = normalizeMihomoClientRule(rule, actions)
			if err != nil {
				return err
			}
			normalized = append(normalized, rule)
		}
		config.Rules = normalized
		config.RawRules = formatMihomoRules(normalized)
	case "text":
		config.RawRules = strings.TrimSpace(strings.ReplaceAll(config.RawRules, "\r\n", "\n"))
		config.Rules, err = parseMihomoClientRawRules(config.RawRules, actions)
		if err != nil {
			return err
		}
	default:
		return errors.New("rule mode must be table or text")
	}
	return validateMihomoClientTerminalRule(config.Rules)
}

func encodeMihomoClientConfigV3(config MihomoClientConfig) (string, string, error) {
	groups, err := json.Marshal(config.ProxyGroupIDs)
	if err != nil {
		return "", "", err
	}
	rules, err := json.Marshal(mihomoClientRulesV3{Rules: config.Rules, RawRules: config.RawRules})
	if err != nil {
		return "", "", err
	}
	return string(groups), string(rules), nil
}

func scanMihomoClientConfig(scanner interface{ Scan(...any) error }, masterKey []byte) (MihomoClientConfig, error) {
	var config MihomoClientConfig
	var groupsJSON, rulesJSON string
	var encryptedToken []byte
	var created, updated int64
	if err := scanner.Scan(&config.ID, &config.Name, &groupsJSON, &config.RuleMode, &rulesJSON, &encryptedToken, &config.Enabled, &created, &updated); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := json.Unmarshal([]byte(groupsJSON), &config.ProxyGroupIDs); err != nil {
		return MihomoClientConfig{}, err
	}
	var rules mihomoClientRulesV3
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
	if err := s.normalizeMihomoClientConfigV3(ctx, &config); err != nil {
		return MihomoClientConfig{}, err
	}
	groupsJSON, rulesJSON, err := encodeMihomoClientConfigV3(config)
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
	config.Enabled = true
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_client_configs_v3
		(id, name, groups_json, rule_mode, rules_json, subscription_token_hash, subscription_token_encrypted, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name, groupsJSON, config.RuleMode, rulesJSON, security.TokenHash(token), encrypted, config.Enabled, now, now)
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
	if err := s.normalizeMihomoClientConfigV3(ctx, &config); err != nil {
		return MihomoClientConfig{}, err
	}
	groupsJSON, rulesJSON, err := encodeMihomoClientConfigV3(config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET name = ?, groups_json = ?, rule_mode = ?, rules_json = ?, updated_at = ? WHERE id = ?`,
		config.Name, groupsJSON, config.RuleMode, rulesJSON, nowUnix(), config.ID)
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, groups_json, rule_mode, rules_json, subscription_token_encrypted, enabled, created_at, updated_at FROM mihomo_client_configs_v3 WHERE id = ?`, id)
	config, err := scanMihomoClientConfig(row, s.masterKey)
	if errors.Is(err, sql.ErrNoRows) {
		return MihomoClientConfig{}, ErrNotFound
	}
	return config, err
}

func (s *Store) ListMihomoClientConfigs(ctx context.Context) ([]MihomoClientConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, groups_json, rule_mode, rules_json, subscription_token_encrypted, enabled, created_at, updated_at FROM mihomo_client_configs_v3 ORDER BY name, id`)
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

func (s *Store) SetMihomoClientConfigEnabled(ctx context.Context, configID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, nowUnix(), configID)
	if err != nil {
		return fmt.Errorf("set Mihomo client config state: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
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
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET subscription_token_hash = ?, subscription_token_encrypted = ?, updated_at = ? WHERE id = ?`, security.TokenHash(token), encrypted, nowUnix(), configID)
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
	err := s.db.QueryRowContext(ctx, `SELECT id FROM mihomo_client_configs_v3 WHERE subscription_token_hash = ? AND enabled = 1`, security.TokenHash(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (s *Store) DeleteMihomoClientConfig(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_client_configs_v3 WHERE id = ?`, id)
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
	if err := s.normalizeMihomoClientConfigV3(ctx, &config); err != nil {
		return "", "", err
	}
	yaml, err := s.generateMihomoClientYAML(ctx, config)
	return config.Name, yaml, err
}

func migrateLegacyMihomoAction(action, groupName string) string {
	if strings.EqualFold(action, "PROXY") {
		return groupName
	}
	return strings.ToUpper(strings.TrimSpace(action))
}

func (s *Store) migrateMihomoClientConfigsV2(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, endpoint_ids, strategy, rule_preset, rules_json, default_action, subscription_token_hash, subscription_token_encrypted, created_at, updated_at FROM mihomo_client_configs_v2`)
	if err != nil {
		return fmt.Errorf("list Mihomo v2 client configs: %w", err)
	}
	type legacyRow struct {
		config                    MihomoClientConfig
		endpointJSON, rulesJSON   string
		tokenHash, encryptedToken []byte
		created, updated          int64
	}
	legacyRows := []legacyRow{}
	for rows.Next() {
		var row legacyRow
		if err := rows.Scan(&row.config.ID, &row.config.Name, &row.endpointJSON, &row.config.Strategy, &row.config.RulePreset, &row.rulesJSON, &row.config.DefaultAction, &row.tokenHash, &row.encryptedToken, &row.created, &row.updated); err != nil {
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
		if err := json.Unmarshal([]byte(row.endpointJSON), &row.config.EndpointIDs); err != nil {
			return err
		}
		var oldRules mihomoClientRules
		if err := json.Unmarshal([]byte(row.rulesJSON), &oldRules); err != nil {
			return err
		}
		groupName := "节点选择"
		groupID := "group-default"
		config := MihomoClientConfig{
			ID: row.config.ID, Name: row.config.Name, RuleMode: "table",
			Groups: []MihomoClientGroup{{ID: groupID, Name: groupName, Strategy: row.config.Strategy}},
		}
		for _, endpointID := range row.config.EndpointIDs {
			config.Groups[0].Members = append(config.Groups[0].Members, MihomoGroupMember{Kind: "endpoint", ID: endpointID})
		}
		switch row.config.RulePreset {
		case "china-direct":
			config.Rules = append(config.Rules,
				MihomoRule{Type: "GEOSITE", Value: "CN", Action: "DIRECT"},
				MihomoRule{Type: "GEOIP", Value: "CN", Action: "DIRECT", NoResolve: true})
		case "direct-all":
			config.Rules = append(config.Rules, MihomoRule{Type: "MATCH", Action: "DIRECT"})
		case "custom":
			for _, rule := range oldRules.Rules {
				rule.Action = migrateLegacyMihomoAction(rule.Action, groupName)
				config.Rules = append(config.Rules, rule)
			}
		}
		if len(config.Rules) == 0 || config.Rules[len(config.Rules)-1].Type != "MATCH" {
			config.Rules = append(config.Rules, MihomoRule{Type: "MATCH", Action: migrateLegacyMihomoAction(row.config.DefaultAction, groupName)})
		}
		config.RawRules = formatMihomoRules(config.Rules)
		groups, err := json.Marshal(config.Groups)
		if err != nil {
			return err
		}
		rules, err := json.Marshal(mihomoClientRulesV3{Rules: config.Rules, RawRules: config.RawRules})
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mihomo_client_configs_v3
			(id, name, groups_json, rule_mode, rules_json, subscription_token_hash, subscription_token_encrypted, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name, string(groups), config.RuleMode, string(rules), row.tokenHash, row.encryptedToken, row.created, row.updated); err != nil {
			return fmt.Errorf("migrate Mihomo v2 client config %s: %w", config.Name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_client_configs_v2`); err != nil {
		return fmt.Errorf("finish Mihomo v2 client config migration: %w", err)
	}
	return nil
}

func migratedMihomoGroupID(configID, embeddedID string) string {
	sum := sha256.Sum256([]byte(configID + "\x00" + embeddedID))
	return fmt.Sprintf("%x", sum[:16])
}

func (s *Store) migrateEmbeddedMihomoClientGroups(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, groups_json, rules_json, created_at, updated_at FROM mihomo_client_configs_v3`)
	if err != nil {
		return fmt.Errorf("list embedded Mihomo client groups: %w", err)
	}
	type row struct {
		id, name, groupsJSON, rulesJSON string
		created, updated                int64
	}
	items := []row{}
	for rows.Next() {
		var item row
		if err := rows.Scan(&item.id, &item.name, &item.groupsJSON, &item.rulesJSON, &item.created, &item.updated); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	existing, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return err
	}
	byID := map[string]MihomoProxyGroup{}
	usedNames := map[string]string{}
	for _, group := range existing {
		byID[group.ID] = group
		usedNames[strings.ToUpper(group.Name)] = group.ID
	}
	for _, item := range items {
		var groupIDs []string
		if err := json.Unmarshal([]byte(item.groupsJSON), &groupIDs); err == nil && groupIDs != nil {
			continue
		}
		groupIDs = nil
		var embedded []MihomoClientGroup
		if err := json.Unmarshal([]byte(item.groupsJSON), &embedded); err != nil {
			return fmt.Errorf("decode embedded groups for client config %s: %w", item.name, err)
		}
		idMap := make(map[string]string, len(embedded))
		nameMap := make(map[string]string, len(embedded))
		for _, group := range embedded {
			stableID := migratedMihomoGroupID(item.id, group.ID)
			idMap[group.ID] = stableID
			if current, exists := byID[stableID]; exists {
				nameMap[strings.ToUpper(group.Name)] = current.Name
				continue
			}
			name := strings.TrimSpace(group.Name)
			if owner, exists := usedNames[strings.ToUpper(name)]; exists && owner != stableID {
				name = "迁移分组 " + stableID[:12]
			}
			nameMap[strings.ToUpper(group.Name)] = name
			usedNames[strings.ToUpper(name)] = stableID
		}
		for _, group := range embedded {
			stableID := idMap[group.ID]
			if _, exists := byID[stableID]; exists {
				groupIDs = append(groupIDs, stableID)
				continue
			}
			members := make([]MihomoGroupMember, len(group.Members))
			copy(members, group.Members)
			endpointIDs := []string{}
			for index := range members {
				if members[index].Kind == "group" {
					members[index].ID = idMap[members[index].ID]
				} else if members[index].Kind == "endpoint" {
					endpointIDs = append(endpointIDs, members[index].ID)
				}
			}
			membersJSON, _ := json.Marshal(members)
			endpointsJSON, _ := json.Marshal(endpointIDs)
			name := nameMap[strings.ToUpper(group.Name)]
			if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mihomo_proxy_groups (id, name, strategy, endpoint_ids, aliases_json, members_json, created_at, updated_at) VALUES (?, ?, ?, ?, '{}', ?, ?, ?)`,
				stableID, name, group.Strategy, string(endpointsJSON), string(membersJSON), item.created, item.updated); err != nil {
				return fmt.Errorf("migrate embedded proxy group %s: %w", group.Name, err)
			}
			migrated := MihomoProxyGroup{ID: stableID, Name: name, Strategy: group.Strategy, Members: members}
			byID[stableID] = migrated
			groupIDs = append(groupIDs, stableID)
		}
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(item.rulesJSON), &rules); err != nil {
			return err
		}
		for index := range rules.Rules {
			if name, exists := nameMap[strings.ToUpper(rules.Rules[index].Action)]; exists {
				rules.Rules[index].Action = name
			}
		}
		rules.RawRules = formatMihomoRules(rules.Rules)
		groupsJSON, _ := json.Marshal(groupIDs)
		rulesJSON, _ := json.Marshal(rules)
		if _, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET groups_json = ?, rules_json = ? WHERE id = ?`, string(groupsJSON), string(rulesJSON), item.id); err != nil {
			return fmt.Errorf("reference migrated proxy groups from client config %s: %w", item.name, err)
		}
	}
	return nil
}

func (s *Store) clientConfigReferencesAnyEndpoint(ctx context.Context, endpointIDs map[string]bool) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT members_json, endpoint_ids FROM mihomo_proxy_groups`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var membersJSON, endpointJSON string
		if err := rows.Scan(&membersJSON, &endpointJSON); err != nil {
			return false, err
		}
		var members []MihomoGroupMember
		if err := json.Unmarshal([]byte(membersJSON), &members); err != nil {
			return false, err
		}
		if len(members) == 0 {
			var legacyIDs []string
			if err := json.Unmarshal([]byte(endpointJSON), &legacyIDs); err != nil {
				return false, err
			}
			for _, endpointID := range legacyIDs {
				members = append(members, MihomoGroupMember{Kind: "endpoint", ID: endpointID})
			}
		}
		for _, member := range members {
			if member.Kind == "endpoint" && endpointIDs[member.ID] {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func (s *Store) generateMihomoClientYAML(ctx context.Context, config MihomoClientConfig) (string, error) {
	groups, err := s.resolveMihomoProxyGroups(ctx, config.ProxyGroupIDs)
	if err != nil {
		return "", err
	}
	groupNames := make(map[string]string, len(groups))
	endpointNames := map[string]string{}
	proxies := []map[string]any{}
	for _, group := range groups {
		groupNames[group.ID] = group.Name
		for _, member := range group.Members {
			if member.Kind != "endpoint" {
				continue
			}
			if _, exists := endpointNames[member.ID]; exists {
				continue
			}
			proxy, err := s.mihomoProxy(ctx, member.ID, "")
			if err != nil {
				return "", err
			}
			endpointNames[member.ID] = proxy["name"].(string)
			proxies = append(proxies, proxy)
		}
	}
	var builder strings.Builder
	builder.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\n")
	builder.WriteString("dns:\n  enable: true\n  ipv6: true\n  enhanced-mode: fake-ip\n")
	builder.WriteString("  default-nameserver:\n    - https://223.5.5.5/dns-query\n")
	builder.WriteString("  nameserver:\n    - https://dns.alidns.com/dns-query\n    - https://doh.pub/dns-query\n")
	builder.WriteString("  proxy-server-nameserver:\n    - https://223.5.5.5/dns-query\n")
	builder.WriteString("proxies:\n")
	for _, proxy := range proxies {
		encoded, _ := json.Marshal(proxy)
		builder.WriteString("  - " + string(encoded) + "\n")
	}
	builder.WriteString("proxy-groups:\n")
	for _, group := range groups {
		members := make([]string, 0, len(group.Members))
		for _, member := range group.Members {
			if member.Kind == "endpoint" {
				members = append(members, endpointNames[member.ID])
			} else {
				members = append(members, groupNames[member.ID])
			}
		}
		object := map[string]any{"name": group.Name, "type": group.Strategy, "proxies": members}
		if group.Strategy != "select" {
			object["url"] = "https://www.gstatic.com/generate_204"
			object["interval"] = 300
		}
		encoded, _ := json.Marshal(object)
		builder.WriteString("  - " + string(encoded) + "\n")
	}
	builder.WriteString("rules:\n")
	for _, rule := range config.Rules {
		line := rule.Type
		if rule.Type != "MATCH" {
			line += "," + rule.Value
		}
		line += "," + rule.Action
		if rule.NoResolve {
			line += ",no-resolve"
		}
		builder.WriteString("  - " + line + "\n")
	}
	return builder.String(), nil
}
