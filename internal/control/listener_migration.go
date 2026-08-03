package control

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Store) migrateLegacyVLESSStreamTLS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, port, spec FROM listeners WHERE protocol = 'vless'`)
	if err != nil {
		return fmt.Errorf("list legacy VLESS listeners: %w", err)
	}
	type update struct {
		id   string
		spec string
	}
	var updates []update
	for rows.Next() {
		var id, encoded string
		var port uint16
		if err := rows.Scan(&id, &port, &encoded); err != nil {
			rows.Close()
			return fmt.Errorf("read legacy VLESS listener: %w", err)
		}
		var spec ProtocolSpec
		if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
			rows.Close()
			return fmt.Errorf("decode legacy VLESS listener %s: %w", id, err)
		}
		if port != 443 || spec.Reality.Enabled || spec.TLS.Enabled || (spec.Transport.Type != "ws" && spec.Transport.Type != "grpc") {
			continue
		}
		spec.TLS.Enabled = true
		if len(spec.TLS.ALPN) == 0 {
			if spec.Transport.Type == "grpc" {
				spec.TLS.ALPN = []string{"h2"}
			} else {
				spec.TLS.ALPN = []string{"http/1.1"}
			}
		}
		migrated, err := json.Marshal(spec)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id: id, spec: string(migrated)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err := s.db.ExecContext(ctx, `UPDATE listeners SET spec = ?, updated_at = ? WHERE id = ?`, item.spec, nowUnix(), item.id); err != nil {
			return fmt.Errorf("migrate legacy VLESS listener %s: %w", item.id, err)
		}
	}
	return nil
}
