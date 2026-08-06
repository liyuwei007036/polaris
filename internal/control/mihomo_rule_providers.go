package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Rule providers used to live inside each client configuration. They are now
// standalone records so one provider can be maintained once and referenced by
// several client configurations. A configuration stores the provider IDs it
// uses; the provider names are what the generated RULE-SET rules reference,
// which is why renaming a provider rewrites those rules.

func (s *Store) CreateMihomoRuleProvider(ctx context.Context, provider MihomoRuleProvider) (MihomoRuleProvider, error) {
	if err := validateMihomoRuleProviderFields(&provider); err != nil {
		return MihomoRuleProvider{}, err
	}
	id, err := newID()
	if err != nil {
		return MihomoRuleProvider{}, err
	}
	provider.ID = id
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_rule_providers (id, name, behavior, format, url, path, interval, proxy, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		provider.ID, provider.Name, provider.Behavior, provider.Format, provider.URL, provider.Path, provider.Interval, provider.Proxy, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoRuleProvider{}, ErrConflict
		}
		return MihomoRuleProvider{}, fmt.Errorf("create Mihomo rule provider: %w", err)
	}
	provider.CreatedAt, provider.UpdatedAt = unixTimeString(now), unixTimeString(now)
	return provider, nil
}

func (s *Store) UpdateMihomoRuleProvider(ctx context.Context, provider MihomoRuleProvider) (MihomoRuleProvider, error) {
	if provider.ID == "" {
		return MihomoRuleProvider{}, errors.New("rule provider ID is required")
	}
	if err := validateMihomoRuleProviderFields(&provider); err != nil {
		return MihomoRuleProvider{}, err
	}
	previous, err := s.mihomoRuleProviderByID(ctx, provider.ID)
	if err != nil {
		return MihomoRuleProvider{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE mihomo_rule_providers SET name = ?, behavior = ?, format = ?, url = ?, path = ?, interval = ?, proxy = ?, updated_at = ? WHERE id = ?`,
		provider.Name, provider.Behavior, provider.Format, provider.URL, provider.Path, provider.Interval, provider.Proxy, nowUnix(), provider.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoRuleProvider{}, ErrConflict
		}
		return MihomoRuleProvider{}, fmt.Errorf("update Mihomo rule provider: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MihomoRuleProvider{}, ErrNotFound
	}
	// RULE-SET rules name their provider, so a rename has to follow through
	// into every configuration that uses it or those rules would point at a
	// provider Mihomo no longer knows.
	if previous.Name != provider.Name {
		if err := s.renameMihomoRuleSetReferences(ctx, provider.ID, previous.Name, provider.Name); err != nil {
			return MihomoRuleProvider{}, err
		}
	}
	return s.mihomoRuleProviderByID(ctx, provider.ID)
}

func (s *Store) ListMihomoRuleProviders(ctx context.Context) ([]MihomoRuleProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, behavior, format, url, path, interval, proxy, created_at, updated_at FROM mihomo_rule_providers ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Mihomo rule providers: %w", err)
	}
	defer rows.Close()
	providers := []MihomoRuleProvider{}
	for rows.Next() {
		provider, err := scanMihomoRuleProvider(rows)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

func (s *Store) DeleteMihomoRuleProvider(ctx context.Context, id string) error {
	names, err := s.mihomoClientConfigNamesUsingRuleProvider(ctx, id)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return fmt.Errorf("%w: %w", ErrConflict, userErrorf("规则供应商正在被客户端配置“%s”使用，请先解除引用", strings.Join(names, "、")))
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM mihomo_rule_providers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete Mihomo rule provider: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) mihomoRuleProviderByID(ctx context.Context, id string) (MihomoRuleProvider, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, behavior, format, url, path, interval, proxy, created_at, updated_at FROM mihomo_rule_providers WHERE id = ?`, id)
	provider, err := scanMihomoRuleProvider(row)
	if errors.Is(err, sql.ErrNoRows) {
		return MihomoRuleProvider{}, ErrNotFound
	}
	return provider, err
}

func scanMihomoRuleProvider(scanner interface{ Scan(...any) error }) (MihomoRuleProvider, error) {
	var provider MihomoRuleProvider
	var created, updated int64
	if err := scanner.Scan(&provider.ID, &provider.Name, &provider.Behavior, &provider.Format, &provider.URL, &provider.Path, &provider.Interval, &provider.Proxy, &created, &updated); err != nil {
		return MihomoRuleProvider{}, err
	}
	provider.CreatedAt, provider.UpdatedAt = unixTimeString(created), unixTimeString(updated)
	return provider, nil
}

// resolveMihomoRuleProviders returns the stored providers for the given IDs in
// the order they were requested. A missing ID is an error rather than a silent
// omission: the configuration's RULE-SET rules would otherwise lose their
// source without anybody being told.
func (s *Store) resolveMihomoRuleProviders(ctx context.Context, ids []string) ([]MihomoRuleProvider, error) {
	providers := make([]MihomoRuleProvider, 0, len(ids))
	for _, id := range ids {
		provider, err := s.mihomoRuleProviderByID(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("rule provider %s is unavailable: %w", id, ErrNotFound)
		}
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

func (s *Store) mihomoClientConfigNamesUsingRuleProvider(ctx context.Context, providerID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, rules_json FROM mihomo_client_configs_v3 ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list client configs using rule provider: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name, rulesJSON string
		if err := rows.Scan(&name, &rulesJSON); err != nil {
			return nil, err
		}
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(rulesJSON), &rules); err != nil {
			return nil, err
		}
		for _, id := range rules.RuleProviderIDs {
			if id == providerID {
				names = append(names, name)
				break
			}
		}
	}
	return names, rows.Err()
}

// renameMihomoRuleSetReferences updates the RULE-SET rule values of every
// configuration referencing a renamed provider.
func (s *Store) renameMihomoRuleSetReferences(ctx context.Context, providerID, previousName, newName string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, rules_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return fmt.Errorf("list client configs for rule provider rename: %w", err)
	}
	type item struct{ id, rulesJSON string }
	items := []item{}
	for rows.Next() {
		var current item
		if err := rows.Scan(&current.id, &current.rulesJSON); err != nil {
			rows.Close()
			return err
		}
		items = append(items, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, current := range items {
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(current.rulesJSON), &rules); err != nil {
			return err
		}
		uses := false
		for _, id := range rules.RuleProviderIDs {
			if id == providerID {
				uses = true
				break
			}
		}
		if !uses {
			continue
		}
		changed := false
		for index := range rules.Rules {
			if rules.Rules[index].Type == "RULE-SET" && strings.EqualFold(rules.Rules[index].Value, previousName) {
				rules.Rules[index].Value = newName
				changed = true
			}
		}
		if !changed {
			continue
		}
		rules.RawRules = formatMihomoRules(rules.Rules)
		encoded, err := json.Marshal(rules)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET rules_json = ?, updated_at = ? WHERE id = ?`, string(encoded), nowUnix(), current.id); err != nil {
			return fmt.Errorf("rewrite RULE-SET references: %w", err)
		}
	}
	return nil
}

// migrateEmbeddedMihomoRuleProviders promotes the providers previously stored
// inside each client configuration into the shared table, leaving the
// configuration holding only the IDs. Providers with identical content are
// shared; a name reused for different content is stored under a suffixed name
// and the owning configuration's RULE-SET rules follow it.
func (s *Store) migrateEmbeddedMihomoRuleProviders(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, rules_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return fmt.Errorf("list embedded Mihomo rule providers: %w", err)
	}
	type item struct{ id, rulesJSON string }
	items := []item{}
	for rows.Next() {
		var current item
		if err := rows.Scan(&current.id, &current.rulesJSON); err != nil {
			rows.Close()
			return err
		}
		items = append(items, current)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	existing, err := s.ListMihomoRuleProviders(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]MihomoRuleProvider, len(existing))
	for _, provider := range existing {
		byName[strings.ToUpper(provider.Name)] = provider
	}
	for _, current := range items {
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(current.rulesJSON), &rules); err != nil {
			return fmt.Errorf("decode embedded rule providers: %w", err)
		}
		if len(rules.RuleProviders) == 0 || len(rules.RuleProviderIDs) != 0 {
			continue
		}
		renamed := map[string]string{}
		for _, embedded := range rules.RuleProviders {
			stored, exists := byName[strings.ToUpper(embedded.Name)]
			if exists && sameMihomoRuleProvider(stored, embedded) {
				rules.RuleProviderIDs = append(rules.RuleProviderIDs, stored.ID)
				continue
			}
			candidate := embedded
			for suffix := 2; exists; suffix++ {
				candidate.Name = fmt.Sprintf("%s-%d", embedded.Name, suffix)
				stored, exists = byName[strings.ToUpper(candidate.Name)]
				if exists && sameMihomoRuleProvider(stored, candidate) {
					break
				}
			}
			if exists {
				rules.RuleProviderIDs = append(rules.RuleProviderIDs, stored.ID)
			} else {
				created, err := s.CreateMihomoRuleProvider(ctx, candidate)
				if err != nil {
					return fmt.Errorf("migrate embedded rule provider %s: %w", embedded.Name, err)
				}
				byName[strings.ToUpper(created.Name)] = created
				rules.RuleProviderIDs = append(rules.RuleProviderIDs, created.ID)
				stored = created
			}
			if stored.Name != embedded.Name {
				renamed[strings.ToUpper(embedded.Name)] = stored.Name
			}
		}
		for index := range rules.Rules {
			if rules.Rules[index].Type != "RULE-SET" {
				continue
			}
			if name, exists := renamed[strings.ToUpper(rules.Rules[index].Value)]; exists {
				rules.Rules[index].Value = name
			}
		}
		rules.RuleProviders = nil
		rules.RawRules = formatMihomoRules(rules.Rules)
		encoded, err := json.Marshal(rules)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET rules_json = ? WHERE id = ?`, string(encoded), current.id); err != nil {
			return fmt.Errorf("reference migrated rule providers: %w", err)
		}
	}
	return nil
}

func sameMihomoRuleProvider(left, right MihomoRuleProvider) bool {
	return strings.EqualFold(left.Name, right.Name) && left.Behavior == right.Behavior && left.Format == right.Format &&
		left.URL == right.URL && left.Path == right.Path && left.Interval == right.Interval && left.Proxy == right.Proxy
}
