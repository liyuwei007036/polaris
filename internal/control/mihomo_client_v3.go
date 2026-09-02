package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/liyuwei007036/polaris/internal/security"
)

type mihomoClientRulesV3 struct {
	RuleProviderIDs []string `json:"rule_provider_ids,omitempty"`
	// RuleProviders is only still decoded so rows written before providers
	// became standalone records can be migrated; new rows never carry it.
	RuleProviders []MihomoRuleProvider `json:"rule_providers,omitempty"`
	Rules         []MihomoRule         `json:"rules"`
	RawRules      string               `json:"raw_rules"`
}

func isReservedMihomoClientName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "PROXY", "DIRECT", "REJECT":
		return true
	default:
		return false
	}
}

func normalizeMihomoClientRule(rule MihomoRule, actions, providers map[string]string) (MihomoRule, error) {
	rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
	rule.Value = strings.TrimSpace(rule.Value)
	rule.Action = strings.TrimSpace(rule.Action)
	if rule.Type != "RULE-SET" && !supportedMihomoRuleTypes[rule.Type] {
		return MihomoRule{}, fmt.Errorf("unsupported Mihomo rule type %q", rule.Type)
	}
	if rule.Type == "MATCH" {
		rule.Value = ""
	} else if rule.Value == "" || strings.ContainsAny(rule.Value, "\r\n") {
		return MihomoRule{}, errors.New("Mihomo rule value is required and cannot contain line breaks")
	}
	if rule.Type == "RULE-SET" {
		name, exists := providers[strings.ToUpper(rule.Value)]
		if !exists {
			return MihomoRule{}, fmt.Errorf("Mihomo rule provider %q is not configured", rule.Value)
		}
		rule.Value = name
	}
	if normalized, ok := actions[strings.ToUpper(rule.Action)]; ok {
		rule.Action = normalized
	} else {
		return MihomoRule{}, fmt.Errorf("Mihomo rule action %q does not name a node, proxy group, DIRECT, or REJECT", rule.Action)
	}
	if rule.NoResolve && rule.Type != "RULE-SET" && !mihomoNoResolveRuleTypes[rule.Type] {
		return MihomoRule{}, errors.New("no-resolve is only valid for target IP rules")
	}
	return rule, nil
}

func parseMihomoClientRawRules(raw string, actions, providers map[string]string) ([]MihomoRule, error) {
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
		normalized, err := normalizeMihomoClientRule(rule, actions, providers)
		if err != nil {
			return nil, fmt.Errorf("Mihomo rule line %d: %w", index+1, err)
		}
		rules = append(rules, normalized)
	}
	return rules, nil
}

// validateMihomoRuleProviderFields checks everything about a provider that can
// be judged on its own. The download proxy is only checked for shape here:
// whether the name resolves depends on which client configuration is using it,
// which is decided in normalizeMihomoRuleProviders.
func validateMihomoRuleProviderFields(provider *MihomoRuleProvider) error {
	provider.Name = strings.TrimSpace(provider.Name)
	if err := validateMihomoName(provider.Name); err != nil {
		return fmt.Errorf("rule provider: %w", err)
	}
	provider.Behavior = strings.ToLower(strings.TrimSpace(provider.Behavior))
	switch provider.Behavior {
	case "domain", "ipcidr", "classical":
	default:
		return fmt.Errorf("rule provider %q has an unsupported behavior", provider.Name)
	}
	provider.Format = strings.ToLower(strings.TrimSpace(provider.Format))
	switch provider.Format {
	case "yaml", "text", "mrs":
	default:
		return fmt.Errorf("rule provider %q has an unsupported format", provider.Name)
	}
	if provider.Format == "mrs" && provider.Behavior == "classical" {
		return fmt.Errorf("rule provider %q cannot use mrs format with classical behavior", provider.Name)
	}
	provider.URL = strings.TrimSpace(provider.URL)
	parsedURL, err := url.ParseRequestURI(provider.URL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return fmt.Errorf("rule provider %q requires a valid HTTP or HTTPS URL", provider.Name)
	}
	provider.Path = strings.TrimSpace(provider.Path)
	if provider.Path == "" || strings.ContainsAny(provider.Path, "\r\n") {
		return fmt.Errorf("rule provider %q requires a path without line breaks", provider.Name)
	}
	if provider.Interval <= 0 {
		return fmt.Errorf("rule provider %q requires a positive update interval", provider.Name)
	}
	provider.Proxy = strings.TrimSpace(provider.Proxy)
	if provider.Proxy == "" {
		provider.Proxy = "DIRECT"
	}
	if strings.EqualFold(provider.Proxy, "REJECT") || strings.ContainsAny(provider.Proxy, "\r\n") {
		return fmt.Errorf("rule provider %q download proxy must be DIRECT or a proxy group name", provider.Name)
	}
	return nil
}

func normalizeMihomoRuleProviders(ruleProviders []MihomoRuleProvider, actions map[string]string) ([]MihomoRuleProvider, map[string]string, error) {
	providers := make(map[string]string, len(ruleProviders))
	for index := range ruleProviders {
		provider := &ruleProviders[index]
		if err := validateMihomoRuleProviderFields(provider); err != nil {
			return nil, nil, err
		}
		upperName := strings.ToUpper(provider.Name)
		if _, exists := providers[upperName]; exists {
			return nil, nil, fmt.Errorf("rule provider name %q is duplicated", provider.Name)
		}
		providers[upperName] = provider.Name
		proxy, exists := actions[strings.ToUpper(provider.Proxy)]
		if !exists || proxy == "REJECT" {
			return nil, nil, fmt.Errorf("rule provider %q download proxy must be DIRECT or a configured node or proxy group", provider.Name)
		}
		provider.Proxy = proxy
	}
	return ruleProviders, providers, nil
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
			// A user that is switched off keeps its name here so the rules
			// naming it still resolve; only a deleted one is dropped, and
			// deleting one already prunes it from the groups.
			name, err := s.mihomoEndpointName(ctx, member.ID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(name) == "" {
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
	for upper, endpointID := range endpointIDsByName {
		actions[upper] = endpointNamesByID[endpointID]
	}
	// Rule providers are optional, so an empty selection is normal here.
	providerIDs := make([]string, 0, len(config.RuleProviderIDs))
	seenProviders := map[string]struct{}{}
	for _, id := range config.RuleProviderIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seenProviders[id]; exists {
			continue
		}
		seenProviders[id] = struct{}{}
		providerIDs = append(providerIDs, id)
	}
	config.RuleProviderIDs = providerIDs
	resolvedProviders, err := s.resolveMihomoRuleProviders(ctx, providerIDs)
	if err != nil {
		return err
	}
	var providers map[string]string
	config.RuleProviders, providers, err = normalizeMihomoRuleProviders(resolvedProviders, actions)
	if err != nil {
		return err
	}
	config.RuleMode = strings.ToLower(strings.TrimSpace(config.RuleMode))
	if config.RuleMode == "" {
		config.RuleMode = "table"
	}
	switch config.RuleMode {
	case "table":
		normalized := make([]MihomoRule, 0, len(config.Rules))
		for _, rule := range config.Rules {
			rule, err = normalizeMihomoClientRule(rule, actions, providers)
			if err != nil {
				return err
			}
			normalized = append(normalized, rule)
		}
		config.Rules = normalized
		config.RawRules = formatMihomoRules(normalized)
	case "text":
		config.RawRules = strings.TrimSpace(strings.ReplaceAll(config.RawRules, "\r\n", "\n"))
		config.Rules, err = parseMihomoClientRawRules(config.RawRules, actions, providers)
		if err != nil {
			return err
		}
	default:
		return errors.New("rule mode must be table or text")
	}
	if err := normalizeMihomoClientDNS(config); err != nil {
		return err
	}
	return validateMihomoClientTerminalRule(config.Rules)
}

type mihomoClientDNSV3 struct {
	Mode string          `json:"mode"`
	DNS  MihomoClientDNS `json:"dns"`
	Raw  string          `json:"raw,omitempty"`
}

func encodeMihomoClientDNS(config MihomoClientConfig) (string, error) {
	encoded, err := json.Marshal(mihomoClientDNSV3{Mode: config.DNSMode, DNS: config.DNS, Raw: config.RawDNS})
	if err != nil {
		return "", fmt.Errorf("encode Mihomo client DNS: %w", err)
	}
	return string(encoded), nil
}

func encodeMihomoClientConfigV3(config MihomoClientConfig) (string, string, error) {
	groups, err := json.Marshal(config.ProxyGroupIDs)
	if err != nil {
		return "", "", err
	}
	rules, err := json.Marshal(mihomoClientRulesV3{RuleProviderIDs: config.RuleProviderIDs, Rules: config.Rules, RawRules: config.RawRules})
	if err != nil {
		return "", "", err
	}
	return string(groups), string(rules), nil
}

func scanMihomoClientConfig(scanner interface{ Scan(...any) error }, masterKey []byte) (MihomoClientConfig, error) {
	var config MihomoClientConfig
	var groupsJSON, rulesJSON, dnsJSON string
	var encryptedToken, encryptedSecret []byte
	var windowStart, windowEnd, expiresAt sql.NullInt64
	var created, updated int64
	if err := scanner.Scan(&config.ID, &config.Name, &groupsJSON, &config.RuleMode, &rulesJSON, &dnsJSON, &encryptedToken, &encryptedSecret,
		&windowStart, &windowEnd, &expiresAt, &config.Enabled, &created, &updated); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := json.Unmarshal([]byte(groupsJSON), &config.ProxyGroupIDs); err != nil {
		return MihomoClientConfig{}, err
	}
	var dns mihomoClientDNSV3
	if err := json.Unmarshal([]byte(dnsJSON), &dns); err != nil {
		return MihomoClientConfig{}, err
	}
	config.DNSMode, config.DNS, config.RawDNS = dns.Mode, dns.DNS, dns.Raw
	var rules mihomoClientRulesV3
	if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
		return MihomoClientConfig{}, err
	}
	config.RuleProviderIDs, config.Rules, config.RawRules = rules.RuleProviderIDs, rules.Rules, rules.RawRules
	if config.RuleProviderIDs == nil {
		config.RuleProviderIDs = []string{}
	}
	config.CreatedAt, config.UpdatedAt = unixTimeString(created), unixTimeString(updated)
	token, err := security.Decrypt(masterKey, encryptedToken)
	if err != nil {
		return MihomoClientConfig{}, fmt.Errorf("decrypt Mihomo subscription token: %w", err)
	}
	config.SubscriptionPath = "/api/v1/mihomo/subscriptions/" + string(token)
	if len(encryptedSecret) != 0 {
		secret, err := security.Decrypt(masterKey, encryptedSecret)
		if err != nil {
			return MihomoClientConfig{}, fmt.Errorf("decrypt Mihomo access secret: %w", err)
		}
		config.AccessSecret = string(secret)
		config.AccessUserAgent = subscriptionUserAgent(config.AccessSecret)
	}
	// Only a complete window means anything, so a half-written row reads back
	// as no window at all rather than as a bound that silently applies.
	if windowStart.Valid && windowEnd.Valid {
		config.AccessWindowStart = formatAccessWindowBound(int(windowStart.Int64))
		config.AccessWindowEnd = formatAccessWindowBound(int(windowEnd.Int64))
	}
	if expiresAt.Valid {
		config.AccessExpiresAt = formatAccessExpiry(expiresAt.Int64)
	}
	return config, nil
}

// mihomoAccessColumns is the stored form of the three access fences.
type mihomoAccessColumns struct {
	secretHash      []byte
	secretEncrypted []byte
	windowStart     any
	windowEnd       any
	expiresAt       any
}

// encodeMihomoAccess validates the operator-facing access fields and returns
// the values the row stores, rewriting config with the normalized text so the
// response shows exactly what was kept.
func (s *Store) encodeMihomoAccess(config *MihomoClientConfig) (mihomoAccessColumns, error) {
	secret, err := normalizeAccessSecret(config.AccessSecret)
	if err != nil {
		return mihomoAccessColumns{}, err
	}
	config.AccessSecret, config.AccessUserAgent = secret, subscriptionUserAgent(secret)

	start, err := parseAccessWindowBound(config.AccessWindowStart)
	if err != nil {
		return mihomoAccessColumns{}, err
	}
	end, err := parseAccessWindowBound(config.AccessWindowEnd)
	if err != nil {
		return mihomoAccessColumns{}, err
	}
	if (start == unsetAccessWindowBound) != (end == unsetAccessWindowBound) {
		return mihomoAccessColumns{}, userErrorf("访问时间段要么两端都填，要么都留空")
	}
	config.AccessWindowStart, config.AccessWindowEnd = formatAccessWindowBound(start), formatAccessWindowBound(end)

	expires, err := parseAccessExpiry(config.AccessExpiresAt)
	if err != nil {
		return mihomoAccessColumns{}, err
	}
	config.AccessExpiresAt = formatAccessExpiry(expires)

	columns := mihomoAccessColumns{
		windowStart: nullableAccessValue(int64(start), int64(unsetAccessWindowBound)),
		windowEnd:   nullableAccessValue(int64(end), int64(unsetAccessWindowBound)),
		expiresAt:   nullableAccessValue(expires, 0),
	}
	if secret != "" {
		encrypted, err := security.Encrypt(s.masterKey, []byte(secret))
		if err != nil {
			return mihomoAccessColumns{}, err
		}
		columns.secretHash, columns.secretEncrypted = security.TokenHash(secret), encrypted
	}
	return columns, nil
}

// nullableAccessValue stores NULL for the sentinel that means "not set", so an
// unset fence is absent from the row rather than encoded as a magic number.
func nullableAccessValue(value, unset int64) any {
	if value == unset {
		return nil
	}
	return value
}

func (s *Store) CreateMihomoClientConfig(ctx context.Context, config MihomoClientConfig) (MihomoClientConfig, error) {
	if err := s.normalizeMihomoClientConfigV3(ctx, &config); err != nil {
		return MihomoClientConfig{}, err
	}
	groupsJSON, rulesJSON, err := encodeMihomoClientConfigV3(config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	dnsJSON, err := encodeMihomoClientDNS(config)
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
	access, err := s.encodeMihomoAccess(&config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	now := nowUnix()
	config.Enabled = true
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_client_configs_v3
		(id, name, groups_json, rule_mode, rules_json, dns_json, subscription_token_hash, subscription_token_encrypted,
		access_secret_hash, access_secret_encrypted, access_window_start, access_window_end, access_expires_at, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, config.ID, config.Name, groupsJSON, config.RuleMode, rulesJSON, dnsJSON, security.TokenHash(token), encrypted,
		access.secretHash, access.secretEncrypted, access.windowStart, access.windowEnd, access.expiresAt, config.Enabled, now, now)
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

// CopyMihomoClientConfig creates an independent duplicate of a configuration:
// same groups, rule providers and rules, its own update address.
func (s *Store) CopyMihomoClientConfig(ctx context.Context, configID, name string) (MihomoClientConfig, error) {
	source, err := s.mihomoClientConfigByID(ctx, configID)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = source.Name + " 副本"
	}
	copied := MihomoClientConfig{
		Name:            name,
		ProxyGroupIDs:   append([]string(nil), source.ProxyGroupIDs...),
		RuleProviderIDs: append([]string(nil), source.RuleProviderIDs...),
		RuleMode:        source.RuleMode,
		Rules:           append([]MihomoRule(nil), source.Rules...),
		RawRules:        source.RawRules,
		DNSMode:         source.DNSMode,
		DNS:             source.DNS,
		RawDNS:          source.RawDNS,
	}
	return s.CreateMihomoClientConfig(ctx, copied)
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
	dnsJSON, err := encodeMihomoClientDNS(config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	access, err := s.encodeMihomoAccess(&config)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET name = ?, groups_json = ?, rule_mode = ?, rules_json = ?, dns_json = ?,
		access_secret_hash = ?, access_secret_encrypted = ?, access_window_start = ?, access_window_end = ?, access_expires_at = ?, updated_at = ? WHERE id = ?`,
		config.Name, groupsJSON, config.RuleMode, rulesJSON, dnsJSON,
		access.secretHash, access.secretEncrypted, access.windowStart, access.windowEnd, access.expiresAt, nowUnix(), config.ID)
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
	row := s.db.QueryRowContext(ctx, `SELECT id, name, groups_json, rule_mode, rules_json, dns_json, subscription_token_encrypted, access_secret_encrypted,
		access_window_start, access_window_end, access_expires_at, enabled, created_at, updated_at FROM mihomo_client_configs_v3 WHERE id = ?`, id)
	config, err := scanMihomoClientConfig(row, s.masterKey)
	if errors.Is(err, sql.ErrNoRows) {
		return MihomoClientConfig{}, ErrNotFound
	}
	if err != nil {
		return MihomoClientConfig{}, err
	}
	config.RuleProviders = s.describeMihomoRuleProviders(ctx, config.RuleProviderIDs)
	return config, nil
}

// describeMihomoRuleProviders fills in the referenced providers for display.
// A reference that no longer resolves is skipped rather than failing the read:
// the console still has to be able to show and repair such a configuration.
func (s *Store) describeMihomoRuleProviders(ctx context.Context, ids []string) []MihomoRuleProvider {
	providers := make([]MihomoRuleProvider, 0, len(ids))
	for _, id := range ids {
		provider, err := s.mihomoRuleProviderByID(ctx, id)
		if err != nil {
			continue
		}
		providers = append(providers, provider)
	}
	return providers
}

func (s *Store) ListMihomoClientConfigs(ctx context.Context) ([]MihomoClientConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, groups_json, rule_mode, rules_json, dns_json, subscription_token_encrypted, access_secret_encrypted,
		access_window_start, access_window_end, access_expires_at, enabled, created_at, updated_at FROM mihomo_client_configs_v3 ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	configs := []MihomoClientConfig{}
	for rows.Next() {
		config, err := scanMihomoClientConfig(rows, s.masterKey)
		if err != nil {
			rows.Close()
			return nil, err
		}
		configs = append(configs, config)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The connection has to be released before the per-config provider lookups
	// run: the store keeps a single SQLite connection, so querying while these
	// rows are still open would wait on itself.
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range configs {
		configs[index].RuleProviders = s.describeMihomoRuleProviders(ctx, configs[index].RuleProviderIDs)
	}
	return configs, nil
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

// mihomoSubscriptionAccessByToken resolves a subscription address to the
// configuration it renders together with every fence a pull has to clear.
func (s *Store) mihomoSubscriptionAccessByToken(ctx context.Context, token string) (mihomoSubscriptionAccess, error) {
	access := mihomoSubscriptionAccess{WindowStart: unsetAccessWindowBound, WindowEnd: unsetAccessWindowBound}
	var windowStart, windowEnd, expiresAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, access_secret_hash, access_window_start, access_window_end, access_expires_at
		FROM mihomo_client_configs_v3 WHERE subscription_token_hash = ? AND enabled = 1`, security.TokenHash(token)).
		Scan(&access.ConfigID, &access.SecretHash, &windowStart, &windowEnd, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return mihomoSubscriptionAccess{}, ErrNotFound
	}
	if err != nil {
		return mihomoSubscriptionAccess{}, err
	}
	if windowStart.Valid && windowEnd.Valid {
		access.WindowStart, access.WindowEnd = int(windowStart.Int64), int(windowEnd.Int64)
	}
	if expiresAt.Valid {
		access.ExpiresAt = expiresAt.Int64
	}
	return access, nil
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

// removeEndpointReferences drops endpoints from every proxy group and client
// subscription that lists them.
//
// Deleting a service deletes its users, and the references have to go with
// them: a group or subscription still naming a user that no longer exists
// renders a member pointing at nothing. Refusing the delete instead — which is
// what this replaced — left the operator to hunt down every reference by hand
// before the console would let them remove anything.
func removeEndpointReferences(ctx context.Context, tx *sql.Tx, endpointIDs map[string]bool) error {
	if len(endpointIDs) == 0 {
		return nil
	}
	groups, err := tx.QueryContext(ctx, `SELECT id, members_json, endpoint_ids FROM mihomo_proxy_groups`)
	if err != nil {
		return fmt.Errorf("load proxy groups for endpoint removal: %w", err)
	}
	type groupUpdate struct{ id, members, legacy string }
	var updates []groupUpdate
	for groups.Next() {
		var id, membersJSON, legacyJSON string
		if err := groups.Scan(&id, &membersJSON, &legacyJSON); err != nil {
			groups.Close()
			return err
		}
		var members []MihomoGroupMember
		if err := json.Unmarshal([]byte(membersJSON), &members); err != nil {
			groups.Close()
			return err
		}
		var legacy []string
		if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
			groups.Close()
			return err
		}
		keptMembers := make([]MihomoGroupMember, 0, len(members))
		for _, member := range members {
			if member.Kind == "endpoint" && endpointIDs[member.ID] {
				continue
			}
			keptMembers = append(keptMembers, member)
		}
		keptLegacy := make([]string, 0, len(legacy))
		for _, endpointID := range legacy {
			if endpointIDs[endpointID] {
				continue
			}
			keptLegacy = append(keptLegacy, endpointID)
		}
		if len(keptMembers) == len(members) && len(keptLegacy) == len(legacy) {
			continue
		}
		encodedMembers, err := json.Marshal(keptMembers)
		if err != nil {
			groups.Close()
			return err
		}
		encodedLegacy, err := json.Marshal(keptLegacy)
		if err != nil {
			groups.Close()
			return err
		}
		updates = append(updates, groupUpdate{id: id, members: string(encodedMembers), legacy: string(encodedLegacy)})
	}
	if err := groups.Close(); err != nil {
		return err
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE mihomo_proxy_groups SET members_json = ?, endpoint_ids = ?, updated_at = ? WHERE id = ?`,
			update.members, update.legacy, nowUnix(), update.id); err != nil {
			return fmt.Errorf("remove deleted users from proxy group: %w", err)
		}
	}

	subscriptions, err := tx.QueryContext(ctx, `SELECT id, endpoint_ids FROM subscriptions WHERE kind = 'client'`)
	if err != nil {
		return fmt.Errorf("load subscriptions for endpoint removal: %w", err)
	}
	type subscriptionUpdate struct{ id, endpoints string }
	var subscriptionUpdates []subscriptionUpdate
	for subscriptions.Next() {
		var id, endpointJSON string
		if err := subscriptions.Scan(&id, &endpointJSON); err != nil {
			subscriptions.Close()
			return err
		}
		var listed []string
		if err := json.Unmarshal([]byte(endpointJSON), &listed); err != nil {
			subscriptions.Close()
			return err
		}
		kept := make([]string, 0, len(listed))
		for _, endpointID := range listed {
			if endpointIDs[endpointID] {
				continue
			}
			kept = append(kept, endpointID)
		}
		if len(kept) == len(listed) {
			continue
		}
		encoded, err := json.Marshal(kept)
		if err != nil {
			subscriptions.Close()
			return err
		}
		subscriptionUpdates = append(subscriptionUpdates, subscriptionUpdate{id: id, endpoints: string(encoded)})
	}
	if err := subscriptions.Close(); err != nil {
		return err
	}
	for _, update := range subscriptionUpdates {
		if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET endpoint_ids = ?, updated_at = ? WHERE id = ?`,
			update.endpoints, nowUnix(), update.id); err != nil {
			return fmt.Errorf("remove deleted users from subscription: %w", err)
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

func (s *Store) mihomoClientConfigIDsReferencingEndpoint(ctx context.Context, endpointID string) (map[string]bool, error) {
	groups, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]MihomoProxyGroup, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	contains := map[string]bool{}
	checked := map[string]bool{}
	var referencesEndpoint func(string) bool
	referencesEndpoint = func(groupID string) bool {
		if checked[groupID] {
			return contains[groupID]
		}
		checked[groupID] = true
		group, exists := byID[groupID]
		if !exists {
			return false
		}
		for _, member := range group.Members {
			if member.Kind == "endpoint" && member.ID == endpointID || member.Kind == "group" && referencesEndpoint(member.ID) {
				contains[groupID] = true
				break
			}
		}
		return contains[groupID]
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id, groups_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	configIDs := map[string]bool{}
	for rows.Next() {
		var configID, groupsJSON string
		if err := rows.Scan(&configID, &groupsJSON); err != nil {
			return nil, err
		}
		var roots []string
		if err := json.Unmarshal([]byte(groupsJSON), &roots); err != nil {
			return nil, err
		}
		for _, root := range roots {
			if referencesEndpoint(root) {
				configIDs[configID] = true
				break
			}
		}
	}
	return configIDs, rows.Err()
}

// The subscription is a plain Mihomo YAML document, so it is built from
// ordered structs and handed to the YAML encoder rather than assembled as
// text: the encoder quotes any node name, rule line or provider name that
// would otherwise change the meaning of the document.
type mihomoYAMLProfile struct {
	StoreSelected bool `yaml:"store-selected"`
}

type mihomoYAMLGeoxURL struct {
	GeoIP   string `yaml:"geoip"`
	Mmdb    string `yaml:"mmdb"`
	ASN     string `yaml:"asn"`
	GeoSite string `yaml:"geosite"`
}

// Mihomo downloads its geo databases from github.com release assets by
// default, which a client on a restricted network cannot reach: the download
// times out, every GEOSITE and GEOIP rule fails to build and Mihomo rejects
// the whole subscription. All four databases are pointed at the same mirror.
const mihomoGeoDataMirror = "https://testingcf.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/"

func defaultMihomoGeoxURL() mihomoYAMLGeoxURL {
	return mihomoYAMLGeoxURL{
		GeoIP:   mihomoGeoDataMirror + "geoip.dat",
		Mmdb:    mihomoGeoDataMirror + "geoip.metadb",
		ASN:     mihomoGeoDataMirror + "GeoLite2-ASN.mmdb",
		GeoSite: mihomoGeoDataMirror + "geosite.dat",
	}
}

type mihomoYAMLTun struct {
	Enable              bool     `yaml:"enable"`
	Stack               string   `yaml:"stack"`
	AutoRoute           bool     `yaml:"auto-route"`
	AutoDetectInterface bool     `yaml:"auto-detect-interface"`
	StrictRoute         bool     `yaml:"strict-route"`
	DNSHijack           []string `yaml:"dns-hijack"`
}

type mihomoYAMLGroup struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Proxies  []string `yaml:"proxies"`
	URL      string   `yaml:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty"`
}

type mihomoYAMLRuleProvider struct {
	Type     string `yaml:"type"`
	Behavior string `yaml:"behavior"`
	Format   string `yaml:"format"`
	URL      string `yaml:"url"`
	Path     string `yaml:"path"`
	Interval int    `yaml:"interval"`
	Proxy    string `yaml:"proxy"`
}

type mihomoYAMLConfig struct {
	MixedPort     int                               `yaml:"mixed-port"`
	AllowLAN      bool                              `yaml:"allow-lan"`
	Mode          string                            `yaml:"mode"`
	LogLevel      string                            `yaml:"log-level"`
	Profile       mihomoYAMLProfile                 `yaml:"profile"`
	GeoxURL       mihomoYAMLGeoxURL                 `yaml:"geox-url"`
	Tun           mihomoYAMLTun                     `yaml:"tun"`
	DNS           *yaml.Node                        `yaml:"dns,omitempty"`
	Proxies       []map[string]any                  `yaml:"proxies"`
	ProxyGroups   []mihomoYAMLGroup                 `yaml:"proxy-groups"`
	RuleProviders map[string]mihomoYAMLRuleProvider `yaml:"rule-providers,omitempty"`
	Rules         []string                          `yaml:"rules"`
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
			// A user that is switched off, or whose service or server is, has
			// nothing to connect to and is left out rather than failing the
			// whole profile — the same way the node links are built.
			proxy, err := s.mihomoProxy(ctx, member.ID, "")
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return "", err
			}
			endpointNames[member.ID] = proxy["name"].(string)
			proxies = append(proxies, proxy)
		}
	}
	dns, err := mihomoClientDNSNode(config)
	if err != nil {
		return "", err
	}
	document := mihomoYAMLConfig{
		MixedPort: 7890,
		Mode:      "rule",
		LogLevel:  "info",
		Profile:   mihomoYAMLProfile{StoreSelected: true},
		GeoxURL:   defaultMihomoGeoxURL(),
		Tun: mihomoYAMLTun{
			Enable: true, Stack: "mixed", AutoRoute: true, AutoDetectInterface: true,
			StrictRoute: true, DNSHijack: []string{"any:53", "tcp://any:53"},
		},
		DNS:     dns,
		Proxies: proxies,
	}
	document.ProxyGroups = make([]mihomoYAMLGroup, 0, len(groups))
	for _, group := range groups {
		members := make([]string, 0, len(group.Members))
		for _, member := range group.Members {
			if member.Kind != "endpoint" {
				members = append(members, groupNames[member.ID])
				continue
			}
			if name, exists := endpointNames[member.ID]; exists {
				members = append(members, name)
			}
		}
		if len(members) == 0 {
			// Every user in this group is switched off. Mihomo rejects a group
			// with no entries, and REJECT says so without letting the traffic
			// out unproxied.
			members = append(members, "REJECT")
		}
		object := mihomoYAMLGroup{Name: group.Name, Type: group.Strategy, Proxies: members}
		if group.Strategy != "select" {
			object.URL = "https://www.gstatic.com/generate_204"
			object.Interval = 300
		}
		document.ProxyGroups = append(document.ProxyGroups, object)
	}
	if len(config.RuleProviders) != 0 {
		document.RuleProviders = make(map[string]mihomoYAMLRuleProvider, len(config.RuleProviders))
		for _, provider := range config.RuleProviders {
			document.RuleProviders[provider.Name] = mihomoYAMLRuleProvider{
				Type: "http", Behavior: provider.Behavior, Format: provider.Format,
				URL: provider.URL, Path: provider.Path, Interval: provider.Interval, Proxy: provider.Proxy,
			}
		}
	}
	document.Rules = make([]string, 0, len(config.Rules))
	for _, rule := range config.Rules {
		document.Rules = append(document.Rules, formatMihomoRule(rule))
	}
	var builder strings.Builder
	encoder := yaml.NewEncoder(&builder)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return "", fmt.Errorf("encode Mihomo subscription: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return "", fmt.Errorf("encode Mihomo subscription: %w", err)
	}
	return builder.String(), nil
}
