package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
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

// migrateListenersBehindRouter moves TCP listeners that still bind a public
// port behind the managed SNI router. Creating a listener only did this when
// the port was already contended, so a node whose services were created one at
// a time left sing-box holding the public socket — and losing it to anything
// else on the host that wanted the same port.
//
// A listener is left alone when it cannot be routed: no usable SNI to match a
// ClientHello against, or an SNI already serving that port. Neither is worth
// refusing to start the control plane over, so both are skipped silently and
// keep working exactly as before.
func (s *Store) migrateListenersBehindRouter(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, name, connection_domain, listen_address, port, backend_port, enabled, spec, outbound_id
		FROM listeners WHERE network = 'tcp' AND listen_address NOT IN ('127.0.0.1', '::1') ORDER BY node_id, port, id`)
	if err != nil {
		return fmt.Errorf("list directly bound TCP listeners: %w", err)
	}
	var pending []Listener
	for rows.Next() {
		var listener Listener
		var spec string
		if err := rows.Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr,
			&listener.Port, &listener.BackendPort, &listener.Enabled, &spec, &listener.OutboundID); err != nil {
			rows.Close()
			return fmt.Errorf("read directly bound TCP listener: %w", err)
		}
		if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
			rows.Close()
			return fmt.Errorf("decode listener %s: %w", listener.ID, err)
		}
		pending = append(pending, listener)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	usedPortsByNode := map[string]map[uint16]bool{}
	for _, listener := range pending {
		name, nameErr := automaticRouteName(listener)
		if nameErr != nil {
			continue
		}
		_, routedSNIs, err := ingressRoutesOnPort(ctx, tx, listener.NodeID, listener.Port)
		if err != nil {
			return err
		}
		taken := false
		for listenerID, sni := range routedSNIs {
			if listenerID != listener.ID && strings.EqualFold(sni, name) {
				taken = true
				break
			}
		}
		if taken {
			continue
		}
		usedPorts, ok := usedPortsByNode[listener.NodeID]
		if !ok {
			usedPorts, err = occupiedListenerPorts(ctx, tx, listener.NodeID, "tcp")
			if err != nil {
				return err
			}
			usedPortsByNode[listener.NodeID] = usedPorts
		}
		backendPort, err := takeBackendPort(usedPorts)
		if err != nil {
			return err
		}
		publicAddress := listener.ListenAddr
		if net.ParseIP(publicAddress) == nil {
			publicAddress = "0.0.0.0"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE listeners SET listen_address = '127.0.0.1', backend_port = ?, updated_at = ? WHERE id = ?`,
			backendPort, nowUnix(), listener.ID); err != nil {
			return fmt.Errorf("move listener %s to an internal port: %w", listener.ID, err)
		}
		listener.ListenAddr, listener.BackendPort = "127.0.0.1", backendPort
		if err := upsertAutomaticRoute(ctx, tx, listener, publicAddress, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
