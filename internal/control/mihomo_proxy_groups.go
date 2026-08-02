package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func normalizeMihomoProxyGroupMembers(group *MihomoProxyGroup) error {
	group.ID = strings.TrimSpace(group.ID)
	group.Name = strings.TrimSpace(group.Name)
	if group.ID == "" || len(group.ID) > 128 || strings.ContainsAny(group.ID, "\r\n,") {
		return errors.New("proxy group ID is required and must be at most 128 characters")
	}
	if err := validateMihomoName(group.Name); err != nil {
		return err
	}
	if isReservedMihomoClientName(group.Name) {
		return fmt.Errorf("proxy group name %q is reserved by Mihomo", group.Name)
	}
	switch group.Strategy {
	case "select", "url-test", "fallback":
	default:
		return errors.New("unsupported Mihomo proxy group strategy")
	}
	if len(group.Members) == 0 && len(group.EndpointIDs) != 0 {
		for _, endpointID := range group.EndpointIDs {
			group.Members = append(group.Members, MihomoGroupMember{Kind: "endpoint", ID: endpointID})
		}
	}
	if len(group.Members) == 0 {
		return errors.New("proxy group requires at least one node or group")
	}
	seen := map[string]bool{}
	endpointIDs := []string{}
	for index := range group.Members {
		member := &group.Members[index]
		member.Kind = strings.ToLower(strings.TrimSpace(member.Kind))
		member.ID = strings.TrimSpace(member.ID)
		if (member.Kind != "endpoint" && member.Kind != "group") || member.ID == "" {
			return errors.New("proxy group contains an invalid member")
		}
		if member.Kind == "group" && member.ID == group.ID {
			return errors.New("proxy group cannot contain itself")
		}
		key := member.Kind + ":" + member.ID
		if seen[key] {
			return errors.New("proxy group contains duplicate members")
		}
		seen[key] = true
		if member.Kind == "endpoint" {
			endpointIDs = append(endpointIDs, member.ID)
		}
	}
	group.EndpointIDs = endpointIDs
	if group.Aliases == nil {
		group.Aliases = map[string]string{}
	}
	return nil
}

func (s *Store) validateMihomoProxyGroupGraph(ctx context.Context, candidate MihomoProxyGroup) error {
	groups, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]MihomoProxyGroup, len(groups)+1)
	for _, group := range groups {
		byID[group.ID] = group
	}
	byID[candidate.ID] = candidate
	for _, member := range candidate.Members {
		switch member.Kind {
		case "group":
			if _, exists := byID[member.ID]; !exists {
				return fmt.Errorf("proxy group %q references an unknown group", candidate.Name)
			}
		case "endpoint":
			if _, err := s.mihomoProxy(ctx, member.ID, ""); err != nil {
				if errors.Is(err, ErrNotFound) {
					return fmt.Errorf("access account %s is unavailable: %w", member.ID, ErrNotFound)
				}
				return err
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
		for _, member := range byID[groupID].Members {
			if member.Kind == "group" {
				if _, exists := byID[member.ID]; !exists {
					return fmt.Errorf("proxy group %q references an unknown group", byID[groupID].Name)
				}
				if err := visit(member.ID); err != nil {
					return err
				}
			}
		}
		state[groupID] = 2
		return nil
	}
	for groupID := range byID {
		if err := visit(groupID); err != nil {
			return err
		}
	}
	closure := map[string]bool{}
	var collect func(string)
	collect = func(groupID string) {
		if closure[groupID] {
			return
		}
		closure[groupID] = true
		for _, member := range byID[groupID].Members {
			if member.Kind == "group" {
				collect(member.ID)
			}
		}
	}
	collect(candidate.ID)
	groupNames := map[string]string{}
	for groupID := range closure {
		groupNames[strings.ToUpper(byID[groupID].Name)] = byID[groupID].Name
	}
	endpointNames := map[string]string{}
	endpointIDs := map[string]bool{}
	for groupID := range closure {
		for _, member := range byID[groupID].Members {
			if member.Kind != "endpoint" || endpointIDs[member.ID] {
				continue
			}
			proxy, err := s.mihomoProxy(ctx, member.ID, "")
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
			if previousID, exists := endpointNames[upperName]; exists && previousID != member.ID {
				return fmt.Errorf("client node alias %q is used by more than one selected account", name)
			}
			if _, exists := groupNames[upperName]; exists {
				return fmt.Errorf("client node alias %q conflicts with a proxy group name", name)
			}
			endpointNames[upperName] = member.ID
			endpointIDs[member.ID] = true
		}
	}
	return nil
}

func (s *Store) validateMihomoProxyGroupClients(ctx context.Context, candidate MihomoProxyGroup, oldName string) error {
	groups, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]MihomoProxyGroup, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	byID[candidate.ID] = candidate
	rows, err := s.db.QueryContext(ctx, `SELECT name, groups_json, rules_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return err
	}
	type client struct{ name, groups, rules string }
	clients := []client{}
	for rows.Next() {
		var item client
		if err := rows.Scan(&item.name, &item.groups, &item.rules); err != nil {
			rows.Close()
			return err
		}
		clients = append(clients, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range clients {
		var roots []string
		if err := json.Unmarshal([]byte(item.groups), &roots); err != nil {
			return err
		}
		closure := map[string]bool{}
		var collect func(string) error
		collect = func(groupID string) error {
			if closure[groupID] {
				return nil
			}
			group, exists := byID[groupID]
			if !exists {
				return ErrNotFound
			}
			closure[groupID] = true
			for _, member := range group.Members {
				if member.Kind == "group" {
					if err := collect(member.ID); err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, root := range roots {
			if err := collect(root); err != nil {
				return err
			}
		}
		if !closure[candidate.ID] {
			continue
		}
		actions := map[string]bool{"DIRECT": true, "REJECT": true}
		for groupID := range closure {
			actions[strings.ToUpper(byID[groupID].Name)] = true
		}
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(item.rules), &rules); err != nil {
			return err
		}
		for _, rule := range rules.Rules {
			action := rule.Action
			if strings.EqualFold(action, oldName) {
				action = candidate.Name
			}
			if !actions[strings.ToUpper(action)] {
				return fmt.Errorf("proxy group update would invalidate rule action %q in client config %q", rule.Action, item.name)
			}
		}
	}
	return nil
}

func encodeMihomoProxyGroup(group MihomoProxyGroup) (string, string, string, error) {
	members, err := json.Marshal(group.Members)
	if err != nil {
		return "", "", "", err
	}
	endpointIDs, err := json.Marshal(group.EndpointIDs)
	if err != nil {
		return "", "", "", err
	}
	aliases, err := json.Marshal(group.Aliases)
	if err != nil {
		return "", "", "", err
	}
	return string(members), string(endpointIDs), string(aliases), nil
}

func scanMihomoProxyGroup(scanner interface{ Scan(...any) error }) (MihomoProxyGroup, error) {
	var group MihomoProxyGroup
	var membersJSON, endpointJSON, aliasesJSON string
	var created, updated int64
	if err := scanner.Scan(&group.ID, &group.Name, &group.Strategy, &membersJSON, &endpointJSON, &aliasesJSON, &created, &updated); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := json.Unmarshal([]byte(membersJSON), &group.Members); err != nil {
		return MihomoProxyGroup{}, err
	}
	if len(group.Members) == 0 {
		var endpointIDs []string
		if err := json.Unmarshal([]byte(endpointJSON), &endpointIDs); err != nil {
			return MihomoProxyGroup{}, err
		}
		for _, endpointID := range endpointIDs {
			group.Members = append(group.Members, MihomoGroupMember{Kind: "endpoint", ID: endpointID})
		}
	}
	group.CreatedAt, group.UpdatedAt = unixTimeString(created), unixTimeString(updated)
	return group, nil
}

func (s *Store) CreateMihomoProxyGroup(ctx context.Context, group MihomoProxyGroup) (MihomoProxyGroup, error) {
	var err error
	group.ID, err = newID()
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := normalizeMihomoProxyGroupMembers(&group); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := s.validateMihomoProxyGroupGraph(ctx, group); err != nil {
		return MihomoProxyGroup{}, err
	}
	members, endpoints, aliases, err := encodeMihomoProxyGroup(group)
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	now := nowUnix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO mihomo_proxy_groups (id, name, strategy, endpoint_ids, aliases_json, members_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		group.ID, group.Name, group.Strategy, endpoints, aliases, members, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoProxyGroup{}, ErrConflict
		}
		return MihomoProxyGroup{}, fmt.Errorf("create Mihomo proxy group: %w", err)
	}
	group.EndpointIDs, group.Aliases = nil, nil
	group.CreatedAt, group.UpdatedAt = unixTimeString(now), unixTimeString(now)
	return group, nil
}

func rewriteMihomoClientGroupActions(ctx context.Context, tx *sql.Tx, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, rules_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return err
	}
	type update struct{ id, rules string }
	updates := []update{}
	for rows.Next() {
		var id, encoded string
		if err := rows.Scan(&id, &encoded); err != nil {
			rows.Close()
			return err
		}
		var rules mihomoClientRulesV3
		if err := json.Unmarshal([]byte(encoded), &rules); err != nil {
			rows.Close()
			return err
		}
		changed := false
		for index := range rules.Rules {
			if strings.EqualFold(rules.Rules[index].Action, oldName) {
				rules.Rules[index].Action = newName
				changed = true
			}
		}
		if changed {
			rules.RawRules = formatMihomoRules(rules.Rules)
			value, err := json.Marshal(rules)
			if err != nil {
				rows.Close()
				return err
			}
			updates = append(updates, update{id: id, rules: string(value)})
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET rules_json = ?, updated_at = ? WHERE id = ?`, item.rules, nowUnix(), item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpdateMihomoProxyGroup(ctx context.Context, group MihomoProxyGroup) (MihomoProxyGroup, error) {
	if group.ID == "" {
		return MihomoProxyGroup{}, errors.New("proxy group ID is required")
	}
	if err := normalizeMihomoProxyGroupMembers(&group); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := s.validateMihomoProxyGroupGraph(ctx, group); err != nil {
		return MihomoProxyGroup{}, err
	}
	var oldName string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM mihomo_proxy_groups WHERE id = ?`, group.ID).Scan(&oldName); errors.Is(err, sql.ErrNoRows) {
		return MihomoProxyGroup{}, ErrNotFound
	} else if err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := s.validateMihomoProxyGroupClients(ctx, group, oldName); err != nil {
		return MihomoProxyGroup{}, err
	}
	members, endpoints, aliases, err := encodeMihomoProxyGroup(group)
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MihomoProxyGroup{}, err
	}
	defer tx.Rollback()
	var created int64
	if err := tx.QueryRowContext(ctx, `SELECT name, created_at FROM mihomo_proxy_groups WHERE id = ?`, group.ID).Scan(&oldName, &created); errors.Is(err, sql.ErrNoRows) {
		return MihomoProxyGroup{}, ErrNotFound
	} else if err != nil {
		return MihomoProxyGroup{}, err
	}
	now := nowUnix()
	if _, err := tx.ExecContext(ctx, `UPDATE mihomo_proxy_groups SET name = ?, strategy = ?, endpoint_ids = ?, aliases_json = ?, members_json = ?, updated_at = ? WHERE id = ?`,
		group.Name, group.Strategy, endpoints, aliases, members, now, group.ID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return MihomoProxyGroup{}, ErrConflict
		}
		return MihomoProxyGroup{}, fmt.Errorf("update Mihomo proxy group: %w", err)
	}
	if err := rewriteMihomoClientGroupActions(ctx, tx, oldName, group.Name); err != nil {
		return MihomoProxyGroup{}, err
	}
	if err := tx.Commit(); err != nil {
		return MihomoProxyGroup{}, err
	}
	group.EndpointIDs, group.Aliases = nil, nil
	group.CreatedAt, group.UpdatedAt = unixTimeString(created), unixTimeString(now)
	return group, nil
}

func (s *Store) ListMihomoProxyGroups(ctx context.Context) ([]MihomoProxyGroup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, strategy, members_json, endpoint_ids, aliases_json, created_at, updated_at FROM mihomo_proxy_groups ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list Mihomo proxy groups: %w", err)
	}
	defer rows.Close()
	groups := []MihomoProxyGroup{}
	for rows.Next() {
		group, err := scanMihomoProxyGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) DeleteMihomoProxyGroup(ctx context.Context, id string) error {
	groups, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, member := range group.Members {
			if member.Kind == "group" && member.ID == id {
				return ErrConflict
			}
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT groups_json FROM mihomo_client_configs_v3`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return err
		}
		var groupIDs []string
		if err := json.Unmarshal([]byte(encoded), &groupIDs); err != nil {
			rows.Close()
			return err
		}
		for _, groupID := range groupIDs {
			if groupID == id {
				rows.Close()
				return ErrConflict
			}
		}
	}
	if err := rows.Close(); err != nil {
		return err
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

func (s *Store) resolveMihomoProxyGroups(ctx context.Context, rootIDs []string) ([]MihomoProxyGroup, error) {
	rootIDs, err := normalizeUniqueIDs(rootIDs)
	if err != nil {
		return nil, err
	}
	groups, err := s.ListMihomoProxyGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]MihomoProxyGroup, len(groups))
	for _, group := range groups {
		byID[group.ID] = group
	}
	state := map[string]uint8{}
	ordered := make([]MihomoProxyGroup, 0, len(groups))
	var visit func(string) error
	visit = func(groupID string) error {
		group, exists := byID[groupID]
		if !exists {
			return fmt.Errorf("proxy group %s: %w", groupID, ErrNotFound)
		}
		if state[groupID] == 1 {
			return errors.New("proxy groups contain a circular reference")
		}
		if state[groupID] == 2 {
			return nil
		}
		state[groupID] = 1
		for _, member := range group.Members {
			if member.Kind == "group" {
				if err := visit(member.ID); err != nil {
					return err
				}
			}
		}
		state[groupID] = 2
		ordered = append(ordered, group)
		return nil
	}
	for _, groupID := range rootIDs {
		if err := visit(groupID); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}
