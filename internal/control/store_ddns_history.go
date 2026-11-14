package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DDNSChangeRecord struct {
	ID           string `json:"id"`
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	Domain       string `json:"domain"`
	RecordType   string `json:"record_type"`
	RecordID     string `json:"record_id"`
	OldIP        string `json:"old_ip"`
	NewIP        string `json:"new_ip"`
	TriggerType  string `json:"trigger_type"` // "auto" | "manual"
	Status       string `json:"status"`       // "succeeded" | "failed"
	ErrorMessage string `json:"error_message,omitempty"`
	CreatedAt    string `json:"created_at"`
}

type DDNSHistoryFilter struct {
	NodeID      string
	Domain      string
	Keyword     string
	Status      string
	TriggerType string
}

// RecordDDNSChange saves a DDNS IP update event (either success or failure).
func (s *Store) RecordDDNSChange(ctx context.Context, record DDNSChangeRecord) error {
	if record.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		record.ID = id
	}
	if record.RecordType == "" {
		record.RecordType = "A"
	}
	if record.TriggerType == "" {
		record.TriggerType = "auto"
	}
	if record.Status == "" {
		record.Status = "succeeded"
	}
	if record.NodeName == "" && record.NodeID != "" {
		if node, err := s.GetNode(ctx, record.NodeID); err == nil && node.Name != "" {
			record.NodeName = node.Name
		}
	}

	query := `INSERT INTO ddns_change_records (
		id, node_id, node_name, domain, record_type, record_id,
		old_ip, new_ip, trigger_type, status, error_message, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	now := nowUnix()
	_, err := s.db.ExecContext(ctx, query,
		record.ID, record.NodeID, record.NodeName, record.Domain, record.RecordType, record.RecordID,
		record.OldIP, record.NewIP, record.TriggerType, record.Status, record.ErrorMessage, now,
	)
	if err != nil {
		return fmt.Errorf("record ddns change: %w", err)
	}
	return nil
}

// ListDDNSHistory retrieves paginated DDNS change records with filtering.
func (s *Store) ListDDNSHistory(ctx context.Context, filter DDNSHistoryFilter, page, pageSize int) ([]DDNSChangeRecord, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	where := " WHERE 1 = 1"
	var args []any

	if filter.NodeID != "" {
		where += " AND r.node_id = ?"
		args = append(args, filter.NodeID)
	}
	if filter.Domain != "" {
		where += " AND r.domain = ?"
		args = append(args, filter.Domain)
	}
	if filter.Status != "" {
		where += " AND r.status = ?"
		args = append(args, filter.Status)
	}
	if filter.TriggerType != "" {
		where += " AND r.trigger_type = ?"
		args = append(args, filter.TriggerType)
	}
	if filter.Keyword != "" {
		kw := "%" + strings.TrimSpace(filter.Keyword) + "%"
		where += " AND (r.domain LIKE ? OR COALESCE(NULLIF(n.name, ''), r.node_name) LIKE ? OR r.old_ip LIKE ? OR r.new_ip LIKE ? OR r.error_message LIKE ?)"
		args = append(args, kw, kw, kw, kw, kw)
	}

	countQuery := `SELECT COUNT(*) FROM ddns_change_records r LEFT JOIN nodes n ON n.id = r.node_id` + where
	var total int
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count ddns history: %w", err)
	}

	query := `SELECT r.id, r.node_id, COALESCE(NULLIF(n.name, ''), r.node_name, r.node_id),
		r.domain, r.record_type, r.record_id, r.old_ip, r.new_ip,
		r.trigger_type, r.status, r.error_message, r.created_at
		FROM ddns_change_records r
		LEFT JOIN nodes n ON n.id = r.node_id` + where + ` ORDER BY r.created_at DESC, r.id DESC LIMIT ? OFFSET ?`

	pagedArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, query, pagedArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list ddns history: %w", err)
	}
	defer rows.Close()

	records := make([]DDNSChangeRecord, 0)
	for rows.Next() {
		var rec DDNSChangeRecord
		var createdAt int64
		if err := rows.Scan(
			&rec.ID, &rec.NodeID, &rec.NodeName,
			&rec.Domain, &rec.RecordType, &rec.RecordID,
			&rec.OldIP, &rec.NewIP,
			&rec.TriggerType, &rec.Status, &rec.ErrorMessage,
			&createdAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan ddns history record: %w", err)
		}
		if createdAt > 0 {
			rec.CreatedAt = time.Unix(createdAt, 0).UTC().Format(time.RFC3339)
		}
		records = append(records, rec)
	}
	return records, total, rows.Err()
}

// DeleteDDNSHistory deletes a single DDNS change record by ID.
func (s *Store) DeleteDDNSHistory(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("id is required")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM ddns_change_records WHERE id = ?`, id)
	return err
}

// ClearDDNSHistory removes all DDNS change records, or only those belonging to a specific node.
func (s *Store) ClearDDNSHistory(ctx context.Context, nodeID string) error {
	if strings.TrimSpace(nodeID) != "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM ddns_change_records WHERE node_id = ?`, nodeID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM ddns_change_records`)
	return err
}
