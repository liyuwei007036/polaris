package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

type userFacingError struct {
	message string
}

func (e *userFacingError) Error() string { return e.message }

func userErrorf(format string, arguments ...any) error {
	return &userFacingError{message: fmt.Sprintf(format, arguments...)}
}

// CreateListenerWithAutomaticPortRouting creates a listener on its requested
// public port. If another enabled TCP listener already uses that port, both
// listeners are moved behind the managed SNI router in one transaction.
func (s *Store) CreateListenerWithAutomaticPortRouting(ctx context.Context, listener Listener) (Listener, *IngressRoute, bool, error) {
	if listener.NodeID == "" || listener.Name == "" || len(listener.Name) > 128 {
		return Listener{}, nil, false, errors.New("listener node and a name up to 128 characters are required")
	}
	if err := ValidateProtocolSpec(listener.Spec); err != nil {
		return Listener{}, nil, false, err
	}
	if listener.ListenAddr == "" {
		listener.ListenAddr = "0.0.0.0"
	}
	if err := ValidateListenerAddress(listener.ListenAddr, listener.Port); err != nil {
		return Listener{}, nil, false, err
	}
	if err := s.ensureOutboundExists(ctx, listener.OutboundID); err != nil {
		return Listener{}, nil, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Listener{}, nil, false, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM nodes WHERE id = ? AND revoked_at IS NULL`, listener.NodeID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Listener{}, nil, false, ErrNotFound
		}
		return Listener{}, nil, false, err
	}
	var conflict string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM listeners WHERE node_id = ? AND name = ?`, listener.NodeID, listener.Name).Scan(&conflict); err == nil {
		return Listener{}, nil, false, fmt.Errorf("%w: %w", ErrConflict, userErrorf("该服务器已存在名为“%s”的接入服务，请修改服务名称", listener.Name))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Listener{}, nil, false, fmt.Errorf("check listener name conflict: %w", err)
	}

	existing, err := listenersOnPublicPort(ctx, tx, listener.NodeID, listener.Spec.Network, listener.Port)
	if err != nil {
		return Listener{}, nil, false, err
	}
	managed := listener.Enabled && len(existing) > 0
	publicAddress := listener.ListenAddr
	usedPorts, err := occupiedListenerPorts(ctx, tx, listener.NodeID, listener.Spec.Network)
	if err != nil {
		return Listener{}, nil, false, err
	}
	seenNames := map[string]struct{}{}
	if managed {
		if listener.Spec.Network != "tcp" {
			return Listener{}, nil, false, userErrorf("端口 %d 已被该服务器上的另一个 UDP 接入服务使用，请选择其他端口", listener.Port)
		}
		newName, err := automaticRouteName(listener)
		if err != nil {
			return Listener{}, nil, false, userErrorf("端口 %d 已被使用；如需自动使用同一端口，两个接入服务都必须启用加密并填写不同的连接域名", listener.Port)
		}
		seenNames[newName] = struct{}{}
		for index := range existing {
			name, nameErr := automaticRouteName(existing[index])
			if nameErr != nil {
				return Listener{}, nil, false, userErrorf("端口 %d 已被“%s”使用，且该服务无法按连接域名自动区分，请选择其他端口", listener.Port, existing[index].Name)
			}
			if _, duplicate := seenNames[name]; duplicate {
				return Listener{}, nil, false, userErrorf("连接域名 %s 已用于端口 %d，请为每个接入服务填写不同的连接域名", name, listener.Port)
			}
			seenNames[name] = struct{}{}
		}

		for index := range existing {
			var routeAddress string
			routeErr := tx.QueryRowContext(ctx, `SELECT listen_address FROM ingress_routes WHERE listener_id = ?`, existing[index].ID).Scan(&routeAddress)
			if routeErr == nil {
				publicAddress = routeAddress
			} else if errors.Is(routeErr, sql.ErrNoRows) && index == 0 {
				publicAddress = existing[index].ListenAddr
			} else if routeErr != nil && !errors.Is(routeErr, sql.ErrNoRows) {
				return Listener{}, nil, false, fmt.Errorf("load automatic port route: %w", routeErr)
			}
		}
		if publicAddress == "127.0.0.1" || publicAddress == "::1" || net.ParseIP(publicAddress) == nil {
			publicAddress = "0.0.0.0"
		}

		for index := range existing {
			if existing[index].BackendPort == existing[index].Port || (existing[index].ListenAddr != "127.0.0.1" && existing[index].ListenAddr != "::1") {
				backendPort, portErr := takeBackendPort(usedPorts)
				if portErr != nil {
					return Listener{}, nil, false, portErr
				}
				existing[index].ListenAddr = "127.0.0.1"
				existing[index].BackendPort = backendPort
				if _, updateErr := tx.ExecContext(ctx, `UPDATE listeners SET listen_address = '127.0.0.1', backend_port = ?, updated_at = ? WHERE id = ?`, backendPort, nowUnix(), existing[index].ID); updateErr != nil {
					return Listener{}, nil, false, fmt.Errorf("move existing listener to an internal port: %w", updateErr)
				}
			}
			name, _ := automaticRouteName(existing[index])
			if err := upsertAutomaticRoute(ctx, tx, existing[index], publicAddress, name); err != nil {
				return Listener{}, nil, false, err
			}
		}
		listener.ListenAddr = "127.0.0.1"
		listener.BackendPort, err = takeBackendPort(usedPorts)
		if err != nil {
			return Listener{}, nil, false, err
		}
	} else if listener.BackendPort == 0 {
		listener.BackendPort = listener.Port
	}

	spec, err := json.Marshal(listener.Spec)
	if err != nil {
		return Listener{}, nil, false, fmt.Errorf("encode listener spec: %w", err)
	}
	listener.ID, err = newID()
	if err != nil {
		return Listener{}, nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO listeners (id, node_id, name, protocol, network, listen_address, port, backend_port, enabled, spec, outbound_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, listener.ID, listener.NodeID, listener.Name, listener.Spec.Protocol, listener.Spec.Network, listener.ListenAddr, listener.Port, listener.BackendPort, listener.Enabled, string(spec), listener.OutboundID, nowUnix(), nowUnix()); err != nil {
		return Listener{}, nil, false, fmt.Errorf("create listener: %w", err)
	}
	var route *IngressRoute
	if managed {
		name, _ := automaticRouteName(listener)
		createdRoute, routeErr := insertAutomaticRoute(ctx, tx, listener, publicAddress, name)
		if routeErr != nil {
			return Listener{}, nil, false, routeErr
		}
		route = &createdRoute
	}
	if err := tx.Commit(); err != nil {
		return Listener{}, nil, false, err
	}
	return listener, route, managed, nil
}

func listenersOnPublicPort(ctx context.Context, tx *sql.Tx, nodeID, network string, port uint16) ([]Listener, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, node_id, name, listen_address, port, backend_port, enabled, spec, outbound_id
		FROM listeners WHERE node_id = ? AND network = ? AND port = ? AND enabled = 1 ORDER BY id`, nodeID, network, port)
	if err != nil {
		return nil, fmt.Errorf("find listeners on public port: %w", err)
	}
	defer rows.Close()
	var listeners []Listener
	for rows.Next() {
		var listener Listener
		var spec string
		if err := rows.Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec, &listener.OutboundID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
			return nil, fmt.Errorf("decode existing listener: %w", err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, rows.Err()
}

func occupiedListenerPorts(ctx context.Context, tx *sql.Tx, nodeID, network string) (map[uint16]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT backend_port FROM listeners WHERE node_id = ? AND network = ?
		UNION SELECT port FROM ingress_routes WHERE node_id = ?`, nodeID, network, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list occupied listener ports: %w", err)
	}
	defer rows.Close()
	occupied := map[uint16]bool{}
	for rows.Next() {
		var port uint16
		if err := rows.Scan(&port); err != nil {
			return nil, err
		}
		occupied[port] = true
	}
	return occupied, rows.Err()
}

func takeBackendPort(occupied map[uint16]bool) (uint16, error) {
	for port := uint16(20000); port < 40000; port++ {
		if !occupied[port] {
			occupied[port] = true
			return port, nil
		}
	}
	return 0, userErrorf("没有可用的内部端口，请清理未使用的接入服务后重试")
}

func automaticRouteName(listener Listener) (string, error) {
	if listener.Spec.Network != "tcp" || !listener.Spec.TLS.Enabled {
		return "", errors.New("listener does not expose TLS SNI")
	}
	name := listener.Spec.TLS.ServerName
	if name == "" && listener.Spec.Reality.Enabled {
		name = listener.Spec.Reality.HandshakeServer
	}
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if !validSNI(name) {
		return "", errors.New("listener TLS server name is invalid")
	}
	return name, nil
}

func (s *Store) prepareAutomaticRouteUpdate(ctx context.Context, listener Listener) (*IngressRoute, error) {
	routes, err := s.ListIngressRoutes(ctx, listener.NodeID)
	if err != nil {
		return nil, err
	}
	var route *IngressRoute
	for index := range routes {
		if routes[index].ListenerID == listener.ID {
			copy := routes[index]
			route = &copy
			break
		}
	}
	if route == nil {
		return nil, nil
	}
	if listener.Port != route.Port || listener.BackendPort != route.BackendPort || (listener.ListenAddr != "127.0.0.1" && listener.ListenAddr != "::1") {
		return nil, userErrorf("该接入服务的端口由系统自动分配，不能直接更改；如需更换端口，请新建接入服务")
	}
	name, err := automaticRouteName(listener)
	if err != nil {
		return nil, userErrorf("该接入服务正在自动使用同一端口，必须保留有效的加密连接域名")
	}
	for _, existing := range routes {
		if existing.ID != route.ID && existing.ListenAddress == route.ListenAddress && existing.Port == route.Port && strings.EqualFold(existing.SNI, name) && existing.Enabled {
			return nil, userErrorf("连接域名 %s 已用于端口 %d，请填写其他连接域名", name, route.Port)
		}
	}
	route.SNI = name
	route.Enabled = listener.Enabled
	return route, nil
}

func upsertAutomaticRoute(ctx context.Context, tx *sql.Tx, listener Listener, publicAddress, name string) error {
	var routeID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM ingress_routes WHERE listener_id = ?`, listener.ID).Scan(&routeID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE ingress_routes SET listen_address = ?, port = ?, sni = ?, enabled = 1, updated_at = ? WHERE id = ?`, publicAddress, listener.Port, name, nowUnix(), routeID)
		if err != nil {
			return fmt.Errorf("update automatic port route: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load automatic port route: %w", err)
	}
	_, err = insertAutomaticRoute(ctx, tx, listener, publicAddress, name)
	return err
}

func insertAutomaticRoute(ctx context.Context, tx *sql.Tx, listener Listener, publicAddress, name string) (IngressRoute, error) {
	id, err := newID()
	if err != nil {
		return IngressRoute{}, err
	}
	route := IngressRoute{ID: id, NodeID: listener.NodeID, ListenerID: listener.ID, ListenAddress: publicAddress, Port: listener.Port, SNI: name, BackendAddress: listener.ListenAddr, BackendPort: listener.BackendPort, Enabled: true}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ingress_routes (id, node_id, listener_id, listen_address, port, sni, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, route.ID, route.NodeID, route.ListenerID, route.ListenAddress, route.Port, route.SNI, nowUnix(), nowUnix()); err != nil {
		return IngressRoute{}, fmt.Errorf("create automatic port route: %w", err)
	}
	return route, nil
}

func validateIngressRoute(route IngressRoute) error {
	if route.NodeID == "" || route.ListenerID == "" {
		return errors.New("ingress route node and listener are required")
	}
	if net.ParseIP(route.ListenAddress) == nil || route.Port == 0 || !validSNI(route.SNI) {
		return errors.New("ingress route address, port and SNI are invalid")
	}
	return nil
}

func (s *Store) CreateIngressRoute(ctx context.Context, route IngressRoute) (IngressRoute, error) {
	if err := validateIngressRoute(route); err != nil {
		return IngressRoute{}, err
	}
	listener, err := s.listenerForIngress(ctx, route.ListenerID, route.NodeID)
	if err != nil {
		return IngressRoute{}, err
	}
	if err := validateIngressListener(listener, route); err != nil {
		return IngressRoute{}, err
	}
	route.SNI = strings.TrimSuffix(strings.ToLower(route.SNI), ".")
	var duplicate string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM ingress_routes WHERE node_id = ? AND listen_address = ? AND port = ? AND sni = ? AND enabled = 1`, route.NodeID, route.ListenAddress, route.Port, route.SNI).Scan(&duplicate)
	if err == nil {
		return IngressRoute{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IngressRoute{}, fmt.Errorf("check ingress SNI conflict: %w", err)
	}
	route.ID, err = newID()
	if err != nil {
		return IngressRoute{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ingress_routes (id, node_id, listener_id, listen_address, port, sni, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, route.ID, route.NodeID, route.ListenerID, route.ListenAddress, route.Port, route.SNI, route.Enabled, nowUnix(), nowUnix())
	if err != nil {
		return IngressRoute{}, fmt.Errorf("create ingress route: %w", err)
	}
	route.BackendAddress = listener.ListenAddr
	route.BackendPort = listener.BackendPort
	return route, nil
}

func (s *Store) UpdateIngressRoute(ctx context.Context, route IngressRoute) (IngressRoute, error) {
	if route.ID == "" {
		return IngressRoute{}, errors.New("ingress route ID is required")
	}
	if err := validateIngressRoute(route); err != nil {
		return IngressRoute{}, err
	}
	var existingNode string
	err := s.db.QueryRowContext(ctx, `SELECT node_id FROM ingress_routes WHERE id = ?`, route.ID).Scan(&existingNode)
	if errors.Is(err, sql.ErrNoRows) {
		return IngressRoute{}, ErrNotFound
	}
	if err != nil {
		return IngressRoute{}, fmt.Errorf("load ingress route: %w", err)
	}
	if existingNode != route.NodeID {
		return IngressRoute{}, ErrForbidden
	}
	listener, err := s.listenerForIngress(ctx, route.ListenerID, route.NodeID)
	if err != nil {
		return IngressRoute{}, err
	}
	if err := validateIngressListener(listener, route); err != nil {
		return IngressRoute{}, err
	}
	route.SNI = strings.TrimSuffix(strings.ToLower(route.SNI), ".")
	var duplicate string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM ingress_routes WHERE node_id = ? AND listen_address = ? AND port = ? AND sni = ? AND enabled = 1 AND id <> ?`, route.NodeID, route.ListenAddress, route.Port, route.SNI, route.ID).Scan(&duplicate)
	if err == nil {
		return IngressRoute{}, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IngressRoute{}, fmt.Errorf("check ingress SNI conflict: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE ingress_routes SET listener_id = ?, listen_address = ?, port = ?, sni = ?, enabled = ?, updated_at = ? WHERE id = ?`, route.ListenerID, route.ListenAddress, route.Port, route.SNI, route.Enabled, nowUnix(), route.ID)
	if err != nil {
		return IngressRoute{}, fmt.Errorf("update ingress route: %w", err)
	}
	route.BackendAddress = listener.ListenAddr
	route.BackendPort = listener.BackendPort
	return route, nil
}

func (s *Store) ListIngressRoutes(ctx context.Context, nodeID string) ([]IngressRoute, error) {
	query := `SELECT r.id, r.node_id, r.listener_id, r.listen_address, r.port, r.sni, r.enabled, l.listen_address, l.backend_port
		FROM ingress_routes r JOIN listeners l ON l.id = r.listener_id`
	var arguments []any
	if nodeID != "" {
		query += " WHERE r.node_id = ?"
		arguments = append(arguments, nodeID)
	}
	query += " ORDER BY r.node_id, r.listen_address, r.port, r.sni, r.id"
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list ingress routes: %w", err)
	}
	defer rows.Close()
	var routes []IngressRoute
	for rows.Next() {
		var route IngressRoute
		if err := rows.Scan(&route.ID, &route.NodeID, &route.ListenerID, &route.ListenAddress, &route.Port, &route.SNI, &route.Enabled, &route.BackendAddress, &route.BackendPort); err != nil {
			return nil, fmt.Errorf("read ingress route: %w", err)
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s *Store) DeleteIngressRoute(ctx context.Context, routeID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM ingress_routes WHERE id = ?`, routeID)
	if err != nil {
		return fmt.Errorf("delete ingress route: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read ingress route deletion: %w", err)
	}
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CompileNodeNginx(ctx context.Context, nodeID string) (string, string, error) {
	routes, err := s.ListIngressRoutes(ctx, nodeID)
	if err != nil {
		return "", "", err
	}
	active := routes[:0]
	for _, route := range routes {
		if route.Enabled {
			active = append(active, route)
		}
	}
	return CompileNginxStream(active)
}

func (s *Store) listenerForIngress(ctx context.Context, listenerID, nodeID string) (Listener, error) {
	var listener Listener
	var spec string
	err := s.db.QueryRowContext(ctx, `SELECT id, node_id, name, listen_address, port, backend_port, enabled, spec FROM listeners WHERE id = ? AND node_id = ?`, listenerID, nodeID).
		Scan(&listener.ID, &listener.NodeID, &listener.Name, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return Listener{}, ErrNotFound
	}
	if err != nil {
		return Listener{}, fmt.Errorf("load ingress listener: %w", err)
	}
	if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
		return Listener{}, fmt.Errorf("decode ingress listener: %w", err)
	}
	return listener, nil
}

func validateIngressListener(listener Listener, route IngressRoute) error {
	if listener.Spec.Network != "tcp" || listener.BackendPort == listener.Port {
		return errors.New("ingress route requires a TCP listener with a distinct backend port")
	}
	if listener.ListenAddr != "127.0.0.1" && listener.ListenAddr != "::1" {
		return errors.New("ingress-routed listener must bind to a loopback address")
	}
	if !listener.Spec.TLS.Enabled {
		return errors.New("ingress route requires a TLS or Reality listener")
	}
	if route.Port != listener.Port {
		return errors.New("ingress route public port must match the listener service port")
	}
	if route.Port == listener.BackendPort {
		return errors.New("ingress route public port must differ from listener backend port")
	}
	return nil
}
