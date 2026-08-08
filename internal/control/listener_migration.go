package control

import (
	"context"
	"database/sql"
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

// reconcileListenerPortRouting puts every TCP listener on the side of the SNI
// router its port actually calls for.
//
// A port carrying more than one enabled listener needs SNI routing, so all of
// them move to internal ports behind Nginx. A port carrying exactly one goes
// the other way: that listener binds the public port itself. Routing a lone
// listener costs it the client's address — Nginx forwards from loopback, and
// nothing in a Reality connection can carry the original address across — so
// the router is used only where it earns its place.
//
// A listener that cannot be routed is left where it is: no usable SNI to match
// a ClientHello against, or an SNI already serving that port. Neither is worth
// refusing to start the control plane over.
func (s *Store) reconcileListenerPortRouting(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, name, connection_domain, listen_address, port, backend_port, enabled, spec, outbound_id
		FROM listeners WHERE network = 'tcp' AND enabled = 1 ORDER BY node_id, port, id`)
	if err != nil {
		return fmt.Errorf("list TCP listeners: %w", err)
	}
	type portKey struct {
		nodeID string
		port   uint16
	}
	groups := map[portKey][]Listener{}
	var order []portKey
	for rows.Next() {
		var listener Listener
		var spec string
		if err := rows.Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr,
			&listener.Port, &listener.BackendPort, &listener.Enabled, &spec, &listener.OutboundID); err != nil {
			rows.Close()
			return fmt.Errorf("read TCP listener: %w", err)
		}
		if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
			rows.Close()
			return fmt.Errorf("decode listener %s: %w", listener.ID, err)
		}
		key := portKey{nodeID: listener.NodeID, port: listener.Port}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], listener)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(order) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	usedPortsByNode := map[string]map[uint16]bool{}
	for _, key := range order {
		listeners := groups[key]
		if len(listeners) > 1 {
			for _, listener := range listeners {
				if err := routeListenerBehindNginx(ctx, tx, listener, usedPortsByNode); err != nil {
					return err
				}
			}
			continue
		}
		if err := bindListenerToPublicPort(ctx, tx, listeners[0]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// routeListenerBehindNginx moves one listener to an internal port and points an
// automatic SNI route at it. A listener already behind the router stays put.
func routeListenerBehindNginx(ctx context.Context, tx *sql.Tx, listener Listener, usedPortsByNode map[string]map[uint16]bool) error {
	if listener.ListenAddr == "127.0.0.1" || listener.ListenAddr == "::1" {
		return nil
	}
	name, nameErr := automaticRouteName(listener)
	if nameErr != nil {
		return nil
	}
	_, routedSNIs, err := ingressRoutesOnPort(ctx, tx, listener.NodeID, listener.Port)
	if err != nil {
		return err
	}
	for listenerID, sni := range routedSNIs {
		if listenerID != listener.ID && strings.EqualFold(sni, name) {
			return nil
		}
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
	return upsertAutomaticRoute(ctx, tx, listener, publicAddress, name)
}

// bindListenerToPublicPort takes a listener back out from behind the router so
// sing-box binds the public port itself. The listener keeps waiting behind
// Nginx when Nginx still binds that port for something else, because two
// processes cannot hold the same socket.
func bindListenerToPublicPort(ctx context.Context, tx *sql.Tx, listener Listener) error {
	if listener.ListenAddr != "127.0.0.1" && listener.ListenAddr != "::1" {
		return nil
	}
	publicAddress, routedSNIs, err := ingressRoutesOnPort(ctx, tx, listener.NodeID, listener.Port)
	if err != nil {
		return err
	}
	for listenerID := range routedSNIs {
		if listenerID != listener.ID {
			return nil
		}
	}
	if publicAddress == "" || net.ParseIP(publicAddress) == nil {
		publicAddress = "0.0.0.0"
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ingress_routes WHERE listener_id = ?`, listener.ID); err != nil {
		return fmt.Errorf("drop automatic port route for listener %s: %w", listener.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE listeners SET listen_address = ?, backend_port = ?, updated_at = ? WHERE id = ?`,
		publicAddress, listener.Port, nowUnix(), listener.ID); err != nil {
		return fmt.Errorf("bind listener %s to its public port: %w", listener.ID, err)
	}
	return nil
}
