package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ConnectionRecordsRetention bounds how long historical connection records are kept.
// Records older than 30 days are automatically pruned.
const ConnectionRecordsRetention = 30 * 24 * time.Hour

type ConnectionRecord struct {
	ID              string `json:"id"`
	NodeID          string `json:"node_id"`
	NodeName        string `json:"node_name"`
	ConnectionID    string `json:"connection_id"`
	SourceIP        string `json:"source_ip"`
	SourcePort      string `json:"source_port"`
	SourceLocation  string `json:"source_location"`
	Destination     string `json:"destination"`
	Host            string `json:"host"`
	Network         string `json:"network"`
	User            string `json:"user"`
	ListenerName    string `json:"listener_name"`
	OutboundName    string `json:"outbound_name"`
	Upload          int64  `json:"upload"`
	Download        int64  `json:"download"`
	StartedAt       string `json:"started_at"`
	ClosedAt        string `json:"closed_at,omitempty"`
	DurationSeconds int64  `json:"duration_seconds"`
}

type ConnectionRecordFilter struct {
	NodeID       string
	SourceIP     string
	User         string
	Network      string
	OutboundName string
	Keyword      string
	StartTime    int64 // Unix timestamp in seconds
	EndTime      int64 // Unix timestamp in seconds
}

type PopularDevice struct {
	User            string `json:"user"`
	SourceIP        string `json:"source_ip"`
	SourceLocation  string `json:"source_location"`
	ConnectionCount int    `json:"connection_count"`
	Upload          int64  `json:"upload"`
	Download        int64  `json:"download"`
	TotalBytes      int64  `json:"total_bytes"`
	LastSeenAt      string `json:"last_seen_at"`
}

type DeviceSummaryStats struct {
	TotalConnections int   `json:"total_connections"`
	UniqueDevices    int   `json:"unique_devices"`
	UniqueIPs        int   `json:"unique_ips"`
	TotalUpload      int64 `json:"total_upload"`
	TotalDownload    int64 `json:"total_download"`
}

// SaveConnectionRecords inserts or updates connection records in a single batch transaction.
func (s *Store) SaveConnectionRecords(ctx context.Context, records []ConnectionRecord) error {
	if len(records) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for save connection records: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO connection_records (
			id, node_id, node_name, connection_id, source_ip, source_port, source_location,
			destination, host, network, user, listener_name, outbound_name,
			upload, download, started_at, closed_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			upload = MAX(connection_records.upload, excluded.upload),
			download = MAX(connection_records.download, excluded.download),
			closed_at = CASE WHEN excluded.closed_at > 0 THEN excluded.closed_at ELSE connection_records.closed_at END,
			node_name = CASE WHEN excluded.node_name != '' THEN excluded.node_name ELSE connection_records.node_name END
	`)
	if err != nil {
		return fmt.Errorf("prepare save connection records statement: %w", err)
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for _, r := range records {
		var startedAt int64
		if t, err := time.Parse(time.RFC3339, r.StartedAt); err == nil {
			startedAt = t.Unix()
		} else {
			startedAt = now
		}
		var closedAt int64
		if r.ClosedAt != "" {
			if t, err := time.Parse(time.RFC3339, r.ClosedAt); err == nil {
				closedAt = t.Unix()
			}
		}

		_, err = stmt.ExecContext(ctx,
			r.ID, r.NodeID, r.NodeName, r.ConnectionID, r.SourceIP, r.SourcePort, r.SourceLocation,
			r.Destination, r.Host, r.Network, r.User, r.ListenerName, r.OutboundName,
			r.Upload, r.Download, startedAt, closedAt, now,
		)
		if err != nil {
			return fmt.Errorf("exec save connection record %s: %w", r.ID, err)
		}
	}

	return tx.Commit()
}

// ListConnectionRecords returns paginated connection records matching the filter.
func (s *Store) ListConnectionRecords(ctx context.Context, filter ConnectionRecordFilter, page, pageSize int) ([]ConnectionRecord, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 20
	}

	where := " WHERE 1 = 1"
	args := []any{}

	if v := strings.TrimSpace(filter.SourceIP); v != "" {
		where += " AND source_ip LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := strings.TrimSpace(filter.NodeID); v != "" {
		where += " AND node_id = ?"
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.User); v != "" {
		where += " AND user LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := strings.TrimSpace(filter.Network); v != "" {
		where += " AND LOWER(network) = LOWER(?)"
		args = append(args, v)
	}
	if v := strings.TrimSpace(filter.OutboundName); v != "" {
		where += " AND outbound_name LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if filter.StartTime > 0 {
		where += " AND started_at >= ?"
		args = append(args, filter.StartTime)
	}
	if filter.EndTime > 0 {
		where += " AND started_at <= ?"
		args = append(args, filter.EndTime)
	}
	if v := strings.TrimSpace(filter.Keyword); v != "" {
		kw := "%" + v + "%"
		where += " AND (source_ip LIKE ? OR destination LIKE ? OR host LIKE ? OR user LIKE ? OR listener_name LIKE ? OR outbound_name LIKE ? OR node_name LIKE ?)"
		args = append(args, kw, kw, kw, kw, kw, kw, kw)
	}

	var total int
	countSQL := "SELECT COUNT(*) FROM connection_records" + where
	if err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count connection records: %w", err)
	}

	querySQL := `
		SELECT id, node_id, node_name, connection_id, source_ip, source_port, source_location,
		       destination, host, network, user, listener_name, outbound_name,
		       upload, download, started_at, closed_at
		FROM connection_records` + where + `
		ORDER BY started_at DESC, id DESC
		LIMIT ? OFFSET ?
	`
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query connection records: %w", err)
	}
	defer rows.Close()

	records := make([]ConnectionRecord, 0, pageSize)
	for rows.Next() {
		var r ConnectionRecord
		var startedAt, closedAt int64
		err := rows.Scan(
			&r.ID, &r.NodeID, &r.NodeName, &r.ConnectionID, &r.SourceIP, &r.SourcePort, &r.SourceLocation,
			&r.Destination, &r.Host, &r.Network, &r.User, &r.ListenerName, &r.OutboundName,
			&r.Upload, &r.Download, &startedAt, &closedAt,
		)
		if err != nil {
			return nil, 0, fmt.Errorf("scan connection record: %w", err)
		}
		if startedAt > 0 {
			r.StartedAt = time.Unix(startedAt, 0).UTC().Format(time.RFC3339)
		}
		if closedAt > 0 {
			r.ClosedAt = time.Unix(closedAt, 0).UTC().Format(time.RFC3339)
			if closedAt >= startedAt {
				r.DurationSeconds = closedAt - startedAt
			}
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate connection records: %w", err)
	}

	return records, total, nil
}

// PopularDevices returns top active devices/clients within the given time window.
func (s *Store) PopularDevices(ctx context.Context, since time.Time, limit int) ([]PopularDevice, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	sinceUnix := since.Unix()

	querySQL := `
		SELECT
			COALESCE(NULLIF(user, ''), source_ip) AS device_name,
			source_ip,
			source_location,
			COUNT(*) AS conn_count,
			SUM(upload) AS total_upload,
			SUM(download) AS total_download,
			MAX(started_at) AS last_seen
		FROM connection_records
		WHERE started_at >= ?
		GROUP BY device_name, source_ip
		ORDER BY conn_count DESC, total_download DESC
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, querySQL, sinceUnix, limit)
	if err != nil {
		return nil, fmt.Errorf("query popular devices: %w", err)
	}
	defer rows.Close()

	items := make([]PopularDevice, 0, limit)
	for rows.Next() {
		var item PopularDevice
		var lastSeen int64
		var upload, download sql.NullInt64
		if err := rows.Scan(
			&item.User,
			&item.SourceIP,
			&item.SourceLocation,
			&item.ConnectionCount,
			&upload,
			&download,
			&lastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan popular device: %w", err)
		}
		if upload.Valid {
			item.Upload = upload.Int64
		}
		if download.Valid {
			item.Download = download.Int64
		}
		item.TotalBytes = item.Upload + item.Download
		if lastSeen > 0 {
			item.LastSeenAt = time.Unix(lastSeen, 0).UTC().Format(time.RFC3339)
		}
		items = append(items, item)
	}

	return items, rows.Err()
}

// DeviceSummaryStats returns aggregate metrics for connections within the given time window.
func (s *Store) DeviceSummaryStats(ctx context.Context, since time.Time) (DeviceSummaryStats, error) {
	sinceUnix := since.Unix()
	querySQL := `
		SELECT
			COUNT(*),
			COUNT(DISTINCT NULLIF(user, '')),
			COUNT(DISTINCT source_ip),
			COALESCE(SUM(upload), 0),
			COALESCE(SUM(download), 0)
		FROM connection_records
		WHERE started_at >= ?
	`
	var stats DeviceSummaryStats
	err := s.db.QueryRowContext(ctx, querySQL, sinceUnix).Scan(
		&stats.TotalConnections,
		&stats.UniqueDevices,
		&stats.UniqueIPs,
		&stats.TotalUpload,
		&stats.TotalDownload,
	)
	if err != nil {
		return DeviceSummaryStats{}, fmt.Errorf("query device summary stats: %w", err)
	}
	// If unique devices count (named users) is smaller than unique IPs, provide at least unique IPs as devices
	if stats.UniqueDevices == 0 {
		stats.UniqueDevices = stats.UniqueIPs
	}
	return stats, nil
}

// PurgeExpiredConnectionRecords removes connection records older than the retention duration.
func (s *Store) PurgeExpiredConnectionRecords(ctx context.Context, retention time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-retention).Unix()
	res, err := s.db.ExecContext(ctx, "DELETE FROM connection_records WHERE started_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge expired connection records: %w", err)
	}
	return res.RowsAffected()
}
