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
	if len(active) == 0 {
		return "", "", errors.New("node has no enabled ingress routes")
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
	if route.Port == listener.BackendPort {
		return errors.New("ingress route public port must differ from listener backend port")
	}
	return nil
}
