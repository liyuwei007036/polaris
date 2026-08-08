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

// migrateHysteria2ALPN fills in the ALPN of Hysteria2 listeners created before
// it had a default. Hysteria2 runs over QUIC, which negotiates h3; leaving the
// list empty means the compiled server configuration and every client profile
// omit the field, so the two sides have to agree by accident.
func (s *Store) migrateHysteria2ALPN(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, spec FROM listeners WHERE protocol = 'hysteria2'`)
	if err != nil {
		return fmt.Errorf("list Hysteria2 listeners: %w", err)
	}
	type update struct {
		id   string
		spec string
	}
	var updates []update
	for rows.Next() {
		var id, encoded string
		if err := rows.Scan(&id, &encoded); err != nil {
			rows.Close()
			return fmt.Errorf("read Hysteria2 listener: %w", err)
		}
		var spec ProtocolSpec
		if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
			rows.Close()
			return fmt.Errorf("decode Hysteria2 listener %s: %w", id, err)
		}
		if len(spec.TLS.ALPN) > 0 {
			continue
		}
		spec.TLS.ALPN = []string{"h3"}
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
			return fmt.Errorf("migrate Hysteria2 listener %s: %w", item.id, err)
		}
	}
	return nil
}

// reconcileListenerPortRouting takes every service back out from behind the
// SNI router, so each one is recorded as binding the public port it was asked
// for.
//
// Where a service really listens is no longer the control plane's to decide —
// the node works that out when it applies the configuration, because only the
// node can see what else holds the port. Rows left behind by the versions that
// did decide here would otherwise keep a service pinned to a loopback port the
// node never learns about, and the automatic routes beside them would describe
// a router the node no longer takes its orders from.
func (s *Store) reconcileListenerPortRouting(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE listeners SET listen_address = '0.0.0.0', backend_port = port, updated_at = ?
		WHERE listen_address <> '0.0.0.0' OR backend_port <> port`, nowUnix()); err != nil {
		return fmt.Errorf("take listeners back off their internal ports: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ingress_routes`); err != nil {
		return fmt.Errorf("drop automatic SNI routes: %w", err)
	}
	return tx.Commit()
}

