package control

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SubscriptionAccess struct {
	ID         string `json:"id"`
	ConfigID   string `json:"config_id"`
	ConfigName string `json:"config_name"`
	IP         string `json:"ip"`
	Location   string `json:"location"`
	UserAgent  string `json:"user_agent"`
	AccessedAt string `json:"accessed_at"`
}

type SubscriptionAccessFilter struct {
	ConfigID  string
	IP        string
	Location  string
	UserAgent string
}

func (s *Store) RecordSubscriptionAccess(ctx context.Context, configID, configName, ip, location, userAgent string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO subscription_access_logs
		(id, config_id, config_name, ip, location, user_agent, accessed_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, configID, configName, ip, location, userAgent, nowUnix())
	if err != nil {
		return fmt.Errorf("record subscription access: %w", err)
	}
	return nil
}

func (s *Store) ListSubscriptionAccess(ctx context.Context, filter SubscriptionAccessFilter, page, pageSize int) ([]SubscriptionAccess, int, error) {
	where := " WHERE 1 = 1"
	args := []any{}
	for _, item := range []struct {
		column string
		value  string
	}{
		{"config_id", filter.ConfigID},
		{"ip", filter.IP},
		{"location", filter.Location},
		{"user_agent", filter.UserAgent},
	} {
		if value := strings.TrimSpace(item.value); value != "" {
			where += " AND " + item.column + " LIKE ?"
			args = append(args, "%"+value+"%")
		}
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM subscription_access_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id, config_id, config_name, ip, location, user_agent, accessed_at
		FROM subscription_access_logs`+where+` ORDER BY accessed_at DESC, id DESC LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []SubscriptionAccess{}
	for rows.Next() {
		var item SubscriptionAccess
		var accessedAt int64
		if err := rows.Scan(&item.ID, &item.ConfigID, &item.ConfigName, &item.IP, &item.Location, &item.UserAgent, &accessedAt); err != nil {
			return nil, 0, err
		}
		item.AccessedAt = time.Unix(accessedAt, 0).UTC().Format(time.RFC3339)
		items = append(items, item)
	}
	return items, total, rows.Err()
}
