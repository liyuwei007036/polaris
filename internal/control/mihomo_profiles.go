package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/security"
)

type MihomoProxyGroup struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Strategy    string              `json:"strategy"`
	Members     []MihomoGroupMember `json:"members"`
	EndpointIDs []string            `json:"endpoint_ids,omitempty"`
	Aliases     map[string]string   `json:"aliases,omitempty"`
	CreatedAt   string              `json:"created_at,omitempty"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
}

type MihomoRoutingProfile struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	RulePreset    string       `json:"rule_preset"`
	ProxyDomains  []string     `json:"proxy_domains"`
	DirectDomains []string     `json:"direct_domains"`
	RejectDomains []string     `json:"reject_domains"`
	ProxyCIDRs    []string     `json:"proxy_cidrs"`
	DefaultAction string       `json:"default_action"`
	Rules         []MihomoRule `json:"rules"`
	RawRules      string       `json:"raw_rules"`
	CreatedAt     string       `json:"created_at,omitempty"`
	UpdatedAt     string       `json:"updated_at,omitempty"`
}

type MihomoRule struct {
	Type      string `json:"type"`
	Value     string `json:"value,omitempty"`
	Action    string `json:"action"`
	NoResolve bool   `json:"no_resolve,omitempty"`
}

type MihomoRuleProvider struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Behavior  string `json:"behavior"`
	Format    string `json:"format"`
	URL       string `json:"url"`
	Path      string `json:"path"`
	Interval  int    `json:"interval"`
	Proxy     string `json:"proxy"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type MihomoClientConfig struct {
	ID               string               `json:"id"`
	Name             string               `json:"name"`
	Enabled          bool                 `json:"enabled"`
	ProxyGroupIDs    []string             `json:"proxy_group_ids"`
	Groups           []MihomoClientGroup  `json:"groups,omitempty"`
	RuleMode         string               `json:"rule_mode,omitempty"`
	EndpointIDs      []string             `json:"endpoint_ids,omitempty"`
	Strategy         string               `json:"strategy,omitempty"`
	RulePreset       string               `json:"rule_preset,omitempty"`
	RuleProviderIDs  []string             `json:"rule_provider_ids"`
	// RuleProviders is resolved from RuleProviderIDs for display and
	// compilation; it is never accepted as input.
	RuleProviders    []MihomoRuleProvider `json:"rule_providers,omitempty"`
	Rules            []MihomoRule         `json:"rules,omitempty"`
	RawRules         string               `json:"raw_rules,omitempty"`
	// DNSMode selects which of the two editors below the subscription is
	// generated from, mirroring RuleMode.
	DNSMode          string          `json:"dns_mode,omitempty"`
	DNS              MihomoClientDNS `json:"dns"`
	RawDNS           string          `json:"raw_dns,omitempty"`
	DefaultAction    string          `json:"default_action,omitempty"`
	CreatedAt        string               `json:"created_at,omitempty"`
	UpdatedAt        string               `json:"updated_at,omitempty"`
	SubscriptionPath string               `json:"subscription_path,omitempty"`
	// Legacy fields are retained only while existing rows are migrated.
	RoutingProfileID string `json:"routing_profile_id,omitempty"`
}

// MihomoClientGroup is self-contained in one client configuration. Members
// may be access endpoints or other groups in the same configuration.
type MihomoClientGroup struct {
	ID       string              `json:"id"`
	Name     string              `json:"name"`
	Strategy string              `json:"strategy"`
	Members  []MihomoGroupMember `json:"members"`
}

type MihomoGroupMember struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type mihomoRoutingRules struct {
	ProxyDomains  []string     `json:"proxy_domains"`
	DirectDomains []string     `json:"direct_domains"`
	RejectDomains []string     `json:"reject_domains"`
	ProxyCIDRs    []string     `json:"proxy_cidrs"`
	DefaultAction string       `json:"default_action"`
	Rules         []MihomoRule `json:"rules,omitempty"`
	RawRules      string       `json:"raw_rules,omitempty"`
}

var supportedMihomoRuleTypes = map[string]bool{
	"DOMAIN": true, "DOMAIN-SUFFIX": true, "DOMAIN-KEYWORD": true,
	"DOMAIN-WILDCARD": true, "DOMAIN-REGEX": true, "GEOSITE": true,
	"IP-CIDR": true, "IP-CIDR6": true, "IP-SUFFIX": true, "IP-ASN": true, "GEOIP": true,
	"SRC-GEOIP": true, "SRC-IP-ASN": true, "SRC-IP-CIDR": true, "SRC-IP-SUFFIX": true,
	"DST-PORT": true, "SRC-PORT": true, "IN-PORT": true, "IN-TYPE": true,
	"IN-USER": true, "IN-NAME": true, "REMATCH-NAME": true,
	"PROCESS-NAME": true, "PROCESS-NAME-WILDCARD": true, "PROCESS-NAME-REGEX": true,
	"PROCESS-PATH": true, "PROCESS-PATH-WILDCARD": true, "PROCESS-PATH-REGEX": true,
	"UID": true, "NETWORK": true, "DSCP": true,
	"AND": true, "OR": true, "NOT": true, "MATCH": true,
}

var mihomoNoResolveRuleTypes = map[string]bool{
	"IP-CIDR": true, "IP-CIDR6": true, "IP-SUFFIX": true, "IP-ASN": true, "GEOIP": true,
}

func normalizeMihomoRule(rule MihomoRule) (MihomoRule, error) {
	rule.Type = strings.ToUpper(strings.TrimSpace(rule.Type))
	rule.Value = strings.TrimSpace(rule.Value)
	rule.Action = strings.ToUpper(strings.TrimSpace(rule.Action))
	if !supportedMihomoRuleTypes[rule.Type] {
		return MihomoRule{}, fmt.Errorf("unsupported Mihomo rule type %q", rule.Type)
	}
	if rule.Type == "MATCH" {
		rule.Value = ""
	} else if rule.Value == "" || strings.ContainsAny(rule.Value, "\r\n") {
		return MihomoRule{}, errors.New("Mihomo rule value is required and cannot contain line breaks")
	}
	if rule.Action != "PROXY" && rule.Action != "DIRECT" && rule.Action != "REJECT" {
		return MihomoRule{}, errors.New("Mihomo rule action must be PROXY, DIRECT, or REJECT")
	}
	if rule.NoResolve && !mihomoNoResolveRuleTypes[rule.Type] {
		return MihomoRule{}, errors.New("no-resolve is only valid for target IP rules")
	}
	return rule, nil
}

func splitMihomoRuleFields(line string) ([]string, error) {
	fields := make([]string, 0, 4)
	start, depth := 0, 0
	for index, character := range line {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				return nil, errors.New("unbalanced parentheses")
			}
		case ',':
			if depth == 0 {
				fields = append(fields, strings.TrimSpace(line[start:index]))
				start = index + 1
			}
		}
	}
	if depth != 0 {
		return nil, errors.New("unbalanced parentheses")
	}
	fields = append(fields, strings.TrimSpace(line[start:]))
	return fields, nil
}

func parseMihomoRawRules(raw string) ([]MihomoRule, error) {
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
		normalized, err := normalizeMihomoRule(rule)
		if err != nil {
			return nil, fmt.Errorf("Mihomo rule line %d: %w", index+1, err)
		}
		rules = append(rules, normalized)
	}
	return rules, nil
}

func formatMihomoRule(rule MihomoRule) string {
	if rule.Type == "MATCH" {
		return "MATCH," + rule.Action
	}
	line := rule.Type + "," + rule.Value + "," + rule.Action
	if rule.NoResolve {
		line += ",no-resolve"
	}
	return line
}

func formatMihomoRules(rules []MihomoRule) string {
	lines := make([]string, 0, len(rules))
	for _, rule := range rules {
		lines = append(lines, formatMihomoRule(rule))
	}
	return strings.Join(lines, "\n")
}

func normalizeUniqueIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one selection is required")
	}
	return result, nil
}

func validateMihomoName(name string) error {
	if name == "" || len(name) > 128 {
		return errors.New("a name up to 128 characters is required")
	}
	if name == "PROXY" || name == "DIRECT" || name == "REJECT" {
		return errors.New("this name is reserved by Mihomo")
	}
	if strings.ContainsAny(name, "\r\n,") {
		return errors.New("name contains unsupported characters")
	}
	return nil
}

func normalizeMihomoProxyGroup(group *MihomoProxyGroup) error {
	group.Name = strings.TrimSpace(group.Name)
	if err := validateMihomoName(group.Name); err != nil {
		return err
	}
	switch group.Strategy {
	case "select", "url-test", "fallback":
	default:
		return errors.New("unsupported Mihomo proxy group strategy")
	}
	ids, err := normalizeUniqueIDs(group.EndpointIDs)
	if err != nil {
		return err
	}
	group.EndpointIDs = ids
	normalizedAliases := make(map[string]string)
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	for endpointID, alias := range group.Aliases {
		alias = strings.TrimSpace(alias)
		if !selected[endpointID] || alias == "" {
			continue
		}
		if err := validateMihomoName(alias); err != nil {
			return fmt.Errorf("invalid node alias: %w", err)
		}
		normalizedAliases[endpointID] = alias
	}
	group.Aliases = normalizedAliases
	return nil
}

func normalizeMihomoRoutingProfile(profile *MihomoRoutingProfile) error {
	profile.Name = strings.TrimSpace(profile.Name)
	if err := validateMihomoName(profile.Name); err != nil {
		return err
	}
	switch profile.RulePreset {
	case "china-direct", "proxy-all", "direct-all", "custom":
	default:
		return errors.New("unsupported Mihomo routing preset")
	}
	if profile.DefaultAction == "" {
		profile.DefaultAction = "PROXY"
	}
	if profile.DefaultAction != "PROXY" && profile.DefaultAction != "DIRECT" {
		return errors.New("Mihomo default action must be PROXY or DIRECT")
	}
	for _, values := range [][]string{profile.ProxyDomains, profile.DirectDomains, profile.RejectDomains, profile.ProxyCIDRs} {
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n,") {
				return errors.New("routing values cannot contain commas or line breaks")
			}
		}
	}
	if strings.TrimSpace(profile.RawRules) != "" {
		rules, err := parseMihomoRawRules(profile.RawRules)
		if err != nil {
			return err
		}
		profile.Rules = rules
		profile.RawRules = strings.TrimSpace(strings.ReplaceAll(profile.RawRules, "\r\n", "\n"))
	} else {
		normalized := make([]MihomoRule, 0, len(profile.Rules))
		for _, rule := range profile.Rules {
			item, err := normalizeMihomoRule(rule)
			if err != nil {
				return err
			}
			normalized = append(normalized, item)
		}
		profile.Rules = normalized
		profile.RawRules = formatMihomoRules(normalized)
	}
	for index, rule := range profile.Rules {
		if rule.Type != "MATCH" {
			continue
		}
		if index != len(profile.Rules)-1 {
			return errors.New("MATCH must be the last Mihomo rule")
		}
		profile.RulePreset = "custom"
		profile.DefaultAction = rule.Action
		profile.Rules = profile.Rules[:index]
		break
	}
	return nil
}

func normalizeMihomoClientConfig(config *MihomoClientConfig) error {
	config.Name = strings.TrimSpace(config.Name)
	if err := validateMihomoName(config.Name); err != nil {
		return err
	}
	ids, err := normalizeUniqueIDs(config.EndpointIDs)
	if err != nil {
		return err
	}
	config.EndpointIDs = ids
	if config.Strategy == "" {
		config.Strategy = "select"
	}
	if config.Strategy != "select" && config.Strategy != "url-test" && config.Strategy != "fallback" {
		return errors.New("unsupported Mihomo proxy strategy")
	}
	if config.RulePreset == "" {
		config.RulePreset = "china-direct"
	}
	if config.RulePreset != "china-direct" && config.RulePreset != "proxy-all" && config.RulePreset != "direct-all" && config.RulePreset != "custom" {
		return errors.New("unsupported Mihomo rule preset")
	}
	if config.RulePreset != "custom" {
		config.Rules = nil
		config.RawRules = ""
		if config.RulePreset == "direct-all" {
			config.DefaultAction = "DIRECT"
		} else {
			config.DefaultAction = "PROXY"
		}
		return nil
	}
	config.DefaultAction = strings.ToUpper(strings.TrimSpace(config.DefaultAction))
	if config.DefaultAction != "PROXY" && config.DefaultAction != "DIRECT" {
		return errors.New("custom Mihomo default action must be PROXY or DIRECT")
	}
	if strings.TrimSpace(config.RawRules) != "" {
		config.Rules, err = parseMihomoRawRules(config.RawRules)
		if err != nil {
			return err
		}
	} else {
		normalized := make([]MihomoRule, 0, len(config.Rules))
		for _, rule := range config.Rules {
			rule, err = normalizeMihomoRule(rule)
			if err != nil {
				return err
			}
			normalized = append(normalized, rule)
		}
		config.Rules = normalized
	}
	for _, rule := range config.Rules {
		if rule.Type == "MATCH" {
			return errors.New("MATCH is generated from the default action and must not appear in custom rules")
		}
	}
	config.RawRules = formatMihomoRules(config.Rules)
	return nil
}

func (s *Store) ensureMihomoEndpointsExist(ctx context.Context, endpointIDs []string) error {
	for _, endpointID := range endpointIDs {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM endpoints WHERE id = ?`, endpointID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("access account %s: %w", endpointID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("load access account: %w", err)
		}
	}
	return nil
}

func encodeStringList(values []string) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func decodeStringList(encoded string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func unixTimeString(value int64) string {
	return time.Unix(value, 0).UTC().Format(time.RFC3339)
}

func (s *Store) legacyCreateMihomoProxyGroup(ctx context.Context, group MihomoProxyGroup) (MihomoProxyGroup, error) {
	if err := normalizeMihomoProxyGroup(&group); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := s.ensureMihomoEndpointsExist(ctx, group.EndpointIDs); err != nil {
		return MihomoProxyGroup{}, err
	}
	encoded, err := encodeStringList(group.EndpointIDs)
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	group.ID, err = newID()
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	now := nowUnix()
	aliases, _ := json.Marshal(group.Aliases)
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_proxy_groups (id, name, strategy, endpoint_ids, aliases_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		group.ID, group.Name, group.Strategy, encoded, string(aliases), now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoProxyGroup{}, ErrConflict
		}
		return MihomoProxyGroup{}, fmt.Errorf("create Mihomo proxy group: %w", err)
	}
	group.CreatedAt, group.UpdatedAt = unixTimeString(now), unixTimeString(now)
	return group, nil
}

func (s *Store) legacyUpdateMihomoProxyGroup(ctx context.Context, group MihomoProxyGroup) (MihomoProxyGroup, error) {
	if group.ID == "" {
		return MihomoProxyGroup{}, errors.New("proxy group ID is required")
	}
	if err := normalizeMihomoProxyGroup(&group); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := s.ensureMihomoEndpointsExist(ctx, group.EndpointIDs); err != nil {
		return MihomoProxyGroup{}, err
	}
	encoded, err := encodeStringList(group.EndpointIDs)
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	now := nowUnix()
	aliases, _ := json.Marshal(group.Aliases)
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_proxy_groups SET name = ?, strategy = ?, endpoint_ids = ?, aliases_json = ?, updated_at = ? WHERE id = ?`,
		group.Name, group.Strategy, encoded, string(aliases), now, group.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoProxyGroup{}, ErrConflict
		}
		return MihomoProxyGroup{}, fmt.Errorf("update Mihomo proxy group: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return MihomoProxyGroup{}, ErrNotFound
	}
	var created int64
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM mihomo_proxy_groups WHERE id = ?`, group.ID).Scan(&created); err != nil {
		return MihomoProxyGroup{}, err
	}
	group.CreatedAt, group.UpdatedAt = unixTimeString(created), unixTimeString(now)
	return group, nil
}

func (s *Store) legacyListMihomoProxyGroups(ctx context.Context) ([]MihomoProxyGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, strategy, endpoint_ids, aliases_json, created_at, updated_at FROM mihomo_proxy_groups ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Mihomo proxy groups: %w", err)
	}
	defer rows.Close()
	groups := []MihomoProxyGroup{}
	for rows.Next() {
		var group MihomoProxyGroup
		var encoded, aliases string
		var created, updated int64
		if err := rows.Scan(&group.ID, &group.Name, &group.Strategy, &encoded, &aliases, &created, &updated); err != nil {
			return nil, err
		}
		group.EndpointIDs, err = decodeStringList(encoded)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(aliases), &group.Aliases); err != nil {
			return nil, err
		}
		group.CreatedAt, group.UpdatedAt = unixTimeString(created), unixTimeString(updated)
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) legacyDeleteMihomoProxyGroup(ctx context.Context, id string) error {
	configs, err := s.ListMihomoClientConfigs(ctx)
	if err != nil {
		return err
	}
	for _, config := range configs {
		for _, groupID := range config.ProxyGroupIDs {
			if groupID == id {
				return ErrConflict
			}
		}
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_proxy_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Mihomo proxy group: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateMihomoRoutingProfile(ctx context.Context, profile MihomoRoutingProfile) (MihomoRoutingProfile, error) {
	if err := normalizeMihomoRoutingProfile(&profile); err != nil {
		return MihomoRoutingProfile{}, err
	}
	rules, err := json.Marshal(mihomoRoutingRules{
		ProxyDomains: profile.ProxyDomains, DirectDomains: profile.DirectDomains,
		RejectDomains: profile.RejectDomains, ProxyCIDRs: profile.ProxyCIDRs, DefaultAction: profile.DefaultAction,
		Rules: profile.Rules, RawRules: profile.RawRules,
	})
	if err != nil {
		return MihomoRoutingProfile{}, err
	}
	profile.ID, err = newID()
	if err != nil {
		return MihomoRoutingProfile{}, err
	}
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_routing_profiles (id, name, rule_preset, rules_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		profile.ID, profile.Name, profile.RulePreset, string(rules), now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoRoutingProfile{}, ErrConflict
		}
		return MihomoRoutingProfile{}, fmt.Errorf("create Mihomo routing profile: %w", err)
	}
	profile.CreatedAt, profile.UpdatedAt = unixTimeString(now), unixTimeString(now)
	return profile, nil
}

func (s *Store) UpdateMihomoRoutingProfile(ctx context.Context, profile MihomoRoutingProfile) (MihomoRoutingProfile, error) {
	if profile.ID == "" {
		return MihomoRoutingProfile{}, errors.New("routing profile ID is required")
	}
	if err := normalizeMihomoRoutingProfile(&profile); err != nil {
		return MihomoRoutingProfile{}, err
	}
	rules, err := json.Marshal(mihomoRoutingRules{
		ProxyDomains: profile.ProxyDomains, DirectDomains: profile.DirectDomains,
		RejectDomains: profile.RejectDomains, ProxyCIDRs: profile.ProxyCIDRs, DefaultAction: profile.DefaultAction,
		Rules: profile.Rules, RawRules: profile.RawRules,
	})
	if err != nil {
		return MihomoRoutingProfile{}, err
	}
	now := nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_routing_profiles SET name = ?, rule_preset = ?, rules_json = ?, updated_at = ? WHERE id = ?`,
		profile.Name, profile.RulePreset, string(rules), now, profile.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoRoutingProfile{}, ErrConflict
		}
		return MihomoRoutingProfile{}, fmt.Errorf("update Mihomo routing profile: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return MihomoRoutingProfile{}, ErrNotFound
	}
	var created int64
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM mihomo_routing_profiles WHERE id = ?`, profile.ID).Scan(&created); err != nil {
		return MihomoRoutingProfile{}, err
	}
	profile.CreatedAt, profile.UpdatedAt = unixTimeString(created), unixTimeString(now)
	return profile, nil
}

func scanMihomoRoutingProfile(scanner interface{ Scan(...any) error }) (MihomoRoutingProfile, error) {
	var profile MihomoRoutingProfile
	var encoded string
	var created, updated int64
	if err := scanner.Scan(&profile.ID, &profile.Name, &profile.RulePreset, &encoded, &created, &updated); err != nil {
		return MihomoRoutingProfile{}, err
	}
	var rules mihomoRoutingRules
	if err := json.Unmarshal([]byte(encoded), &rules); err != nil {
		return MihomoRoutingProfile{}, err
	}
	profile.ProxyDomains, profile.DirectDomains = rules.ProxyDomains, rules.DirectDomains
	profile.RejectDomains, profile.ProxyCIDRs = rules.RejectDomains, rules.ProxyCIDRs
	profile.DefaultAction = rules.DefaultAction
	profile.Rules, profile.RawRules = rules.Rules, rules.RawRules
	if profile.RawRules == "" {
		profile.RawRules = formatMihomoRules(profile.Rules)
	}
	profile.CreatedAt, profile.UpdatedAt = unixTimeString(created), unixTimeString(updated)
	return profile, nil
}

func (s *Store) ListMihomoRoutingProfiles(ctx context.Context) ([]MihomoRoutingProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, rule_preset, rules_json, created_at, updated_at FROM mihomo_routing_profiles ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Mihomo routing profiles: %w", err)
	}
	defer rows.Close()
	profiles := []MihomoRoutingProfile{}
	for rows.Next() {
		profile, err := scanMihomoRoutingProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) DeleteMihomoRoutingProfile(ctx context.Context, id string) error {
	var references int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mihomo_client_configs WHERE routing_profile_id = ?`, id).Scan(&references); err != nil {
		return err
	}
	if references > 0 {
		return ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_routing_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Mihomo routing profile: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) legacyMihomoCompositionExists(ctx context.Context, config MihomoClientConfig) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM mihomo_routing_profiles WHERE id = ?`, config.RoutingProfileID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("routing profile: %w", ErrNotFound)
	} else if err != nil {
		return err
	}
	for _, groupID := range config.ProxyGroupIDs {
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM mihomo_proxy_groups WHERE id = ?`, groupID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("proxy group %s: %w", groupID, ErrNotFound)
		} else if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) legacyCreateMihomoClientConfig(ctx context.Context, config MihomoClientConfig) (MihomoClientConfig, error) {
	if err := normalizeMihomoClientConfig(&config); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := s.legacyMihomoCompositionExists(ctx, config); err != nil {
		return MihomoClientConfig{}, err
	}
	groups, err := encodeStringList(config.ProxyGroupIDs)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	config.ID, err = newID()
	if err != nil {
		return MihomoClientConfig{}, err
	}
	now := nowUnix()
	token, err := security.RandomToken(32)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	encryptedToken, err := security.Encrypt(s.masterKey, []byte(token))
	if err != nil {
		return MihomoClientConfig{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_client_configs (id, name, proxy_group_ids, routing_profile_id, subscription_token_hash, subscription_token_encrypted, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		config.ID, config.Name, groups, config.RoutingProfileID, security.TokenHash(token), encryptedToken, now, now)
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

func (s *Store) legacyUpdateMihomoClientConfig(ctx context.Context, config MihomoClientConfig) (MihomoClientConfig, error) {
	if config.ID == "" {
		return MihomoClientConfig{}, errors.New("client config ID is required")
	}
	if err := normalizeMihomoClientConfig(&config); err != nil {
		return MihomoClientConfig{}, err
	}
	if err := s.legacyMihomoCompositionExists(ctx, config); err != nil {
		return MihomoClientConfig{}, err
	}
	groups, err := encodeStringList(config.ProxyGroupIDs)
	if err != nil {
		return MihomoClientConfig{}, err
	}
	now := nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs SET name = ?, proxy_group_ids = ?, routing_profile_id = ?, updated_at = ? WHERE id = ?`,
		config.Name, groups, config.RoutingProfileID, now, config.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoClientConfig{}, ErrConflict
		}
		return MihomoClientConfig{}, fmt.Errorf("update Mihomo client config: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return MihomoClientConfig{}, ErrNotFound
	}
	var created int64
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM mihomo_client_configs WHERE id = ?`, config.ID).Scan(&created); err != nil {
		return MihomoClientConfig{}, err
	}
	config.CreatedAt, config.UpdatedAt = unixTimeString(created), unixTimeString(now)
	return config, nil
}

func (s *Store) legacyListMihomoClientConfigs(ctx context.Context) ([]MihomoClientConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, proxy_group_ids, routing_profile_id, subscription_token_encrypted, created_at, updated_at FROM mihomo_client_configs ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Mihomo client configs: %w", err)
	}
	defer rows.Close()
	configs := []MihomoClientConfig{}
	for rows.Next() {
		var config MihomoClientConfig
		var encoded string
		var encryptedToken []byte
		var created, updated int64
		if err := rows.Scan(&config.ID, &config.Name, &encoded, &config.RoutingProfileID, &encryptedToken, &created, &updated); err != nil {
			return nil, err
		}
		config.ProxyGroupIDs, err = decodeStringList(encoded)
		if err != nil {
			return nil, err
		}
		config.CreatedAt, config.UpdatedAt = unixTimeString(created), unixTimeString(updated)
		if len(encryptedToken) > 0 {
			token, err := security.Decrypt(s.masterKey, encryptedToken)
			if err != nil {
				return nil, fmt.Errorf("decrypt Mihomo subscription token: %w", err)
			}
			config.SubscriptionPath = "/api/v1/mihomo/subscriptions/" + string(token)
		}
		configs = append(configs, config)
	}
	return configs, rows.Err()
}

func (s *Store) legacyRotateMihomoClientSubscription(ctx context.Context, configID string) (string, error) {
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	encrypted, err := security.Encrypt(s.masterKey, []byte(token))
	if err != nil {
		return "", err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs SET subscription_token_hash = ?, subscription_token_encrypted = ?, updated_at = ? WHERE id = ?`,
		security.TokenHash(token), encrypted, nowUnix(), configID)
	if err != nil {
		return "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return "", ErrNotFound
	}
	return "/api/v1/mihomo/subscriptions/" + token, nil
}

func (s *Store) legacyMihomoClientConfigIDByToken(ctx context.Context, token string) (string, error) {
	var configID string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM mihomo_client_configs WHERE subscription_token_hash = ?`, security.TokenHash(token)).Scan(&configID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return configID, err
}

func (s *Store) legacyDeleteMihomoClientConfig(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_client_configs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Mihomo client config: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) legacyGenerateStoredMihomoYAML(ctx context.Context, configID string) (string, string, error) {
	var config MihomoClientConfig
	var groupIDs string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, proxy_group_ids, routing_profile_id FROM mihomo_client_configs WHERE id = ?`, configID).
		Scan(&config.ID, &config.Name, &groupIDs, &config.RoutingProfileID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	config.ProxyGroupIDs, err = decodeStringList(groupIDs)
	if err != nil {
		return "", "", err
	}
	groups := make([]mihomoGroupDefinition, 0, len(config.ProxyGroupIDs))
	for _, groupID := range config.ProxyGroupIDs {
		var group MihomoProxyGroup
		var endpointIDs, aliases string
		err := s.db.QueryRowContext(ctx, `SELECT name, strategy, endpoint_ids, aliases_json FROM mihomo_proxy_groups WHERE id = ?`, groupID).
			Scan(&group.Name, &group.Strategy, &endpointIDs, &aliases)
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("proxy group %s: %w", groupID, ErrNotFound)
		}
		if err != nil {
			return "", "", err
		}
		group.EndpointIDs, err = decodeStringList(endpointIDs)
		if err != nil {
			return "", "", err
		}
		if err := json.Unmarshal([]byte(aliases), &group.Aliases); err != nil {
			return "", "", err
		}
		groups = append(groups, mihomoGroupDefinition{Name: group.Name, Strategy: group.Strategy, EndpointIDs: group.EndpointIDs, Aliases: group.Aliases})
	}
	var routing MihomoRoutingProfile
	var rulesJSON string
	err = s.db.QueryRowContext(ctx, `SELECT name, rule_preset, rules_json FROM mihomo_routing_profiles WHERE id = ?`, config.RoutingProfileID).
		Scan(&routing.Name, &routing.RulePreset, &rulesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("routing profile: %w", ErrNotFound)
	}
	if err != nil {
		return "", "", err
	}
	var rules mihomoRoutingRules
	if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
		return "", "", err
	}
	yaml, err := s.generateMihomoYAML(ctx, config.Name, "", groups, mihomoRuleDefinition{
		RulePreset: routing.RulePreset, ProxyDomains: rules.ProxyDomains, DirectDomains: rules.DirectDomains,
		RejectDomains: rules.RejectDomains, ProxyCIDRs: rules.ProxyCIDRs, DefaultAction: rules.DefaultAction,
		Rules: rules.Rules,
	})
	return config.Name, yaml, err
}
