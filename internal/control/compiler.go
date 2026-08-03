package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/sb-control/sb-control/internal/security"
)

// CompileNodeConfig translates only typed Listener and Endpoint objects into a
// sing-box configuration. No caller-supplied configuration JSON is merged.
func (s *Store) CompileNodeConfig(ctx context.Context, nodeID string) (string, string, error) {
	listeners, err := s.ListListeners(ctx, nodeID)
	if err != nil {
		return "", "", err
	}
	inbounds := make([]map[string]any, 0, len(listeners))
	type endpointRouteSelection struct {
		ListenerID string
		Name       string
		OutboundID string
	}
	var endpointSelections []endpointRouteSelection
	for _, listener := range listeners {
		if !listener.Enabled {
			continue
		}
		endpoints, err := s.loadEndpointCredentials(ctx, listener.ID)
		if err != nil {
			return "", "", err
		}
		inbound, err := s.compileInbound(ctx, listener, endpoints)
		if err != nil {
			return "", "", err
		}
		inbounds = append(inbounds, inbound)
		for _, endpoint := range endpoints {
			if endpoint.OutboundID != "" {
				endpointSelections = append(endpointSelections, endpointRouteSelection{
					ListenerID: listener.ID,
					Name:       endpoint.Name,
					OutboundID: endpoint.OutboundID,
				})
			}
		}
	}
	rules, err := s.ListEffectiveRouteRules(ctx, nodeID)
	if err != nil {
		return "", "", err
	}
	sortRouteRules(rules)
	// Managed outbounds: a built-in "direct" plus any enabled SOCKS5/HTTP proxies.
	// Outbounds are global, so every node's config carries the same egress set.
	managedOutbounds, err := s.loadEnabledOutbounds(ctx)
	if err != nil {
		return "", "", err
	}
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	outboundTags := make(map[string]string, len(managedOutbounds))
	for _, ob := range managedOutbounds {
		tag := "outbound-" + ob.ID
		outbounds = append(outbounds, compileOutbound(ob, tag))
		outboundTags[ob.ID] = tag
	}
	compiledRules := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		compiled := compileRouteRule(rule)
		if rule.Action == "outbound" {
			tag, ok := outboundTags[rule.OutboundTag]
			if !ok {
				return "", "", fmt.Errorf("route rule %s references a missing or disabled outbound", rule.ID)
			}
			compiled["outbound"] = tag
		}
		compiledRules = append(compiledRules, compiled)
	}
	// An account-level outbound is authoritative for that authenticated user.
	// Put these rules before general server rules so the per-account selection
	// made in the inbound form cannot be overridden by a broader rule.
	accountRules := make([]map[string]any, 0, len(endpointSelections))
	for _, selection := range endpointSelections {
		tag := "direct"
		if selection.OutboundID != "direct" {
			var ok bool
			tag, ok = outboundTags[selection.OutboundID]
			if !ok {
				continue
			}
		}
		accountRules = append(accountRules, map[string]any{
			"inbound":   []string{"listener-" + selection.ListenerID},
			"auth_user": []string{selection.Name},
			"action":    "route",
			"outbound":  tag,
		})
	}
	compiledRules = append(accountRules, compiledRules...)
	// Each listener with a selected outbound gets a fallback rule routing its
	// inbound to that outbound; user-defined rules above still take precedence.
	for _, listener := range listeners {
		if !listener.Enabled || listener.OutboundID == "" {
			continue
		}
		tag, ok := outboundTags[listener.OutboundID]
		if !ok {
			continue
		}
		compiledRules = append(compiledRules, map[string]any{
			"inbound":  []string{"listener-" + listener.ID},
			"action":   "route",
			"outbound": tag,
		})
	}
	configuration := map[string]any{
		"log":       map[string]any{"level": "info", "timestamp": true},
		"inbounds":  inbounds,
		"outbounds": outbounds,
		// Loopback-only Clash API: the agent reads current connections from it
		// for F-12; it is never exposed beyond the node itself.
		"experimental": map[string]any{"clash_api": map[string]any{"external_controller": "127.0.0.1:9090"}},
	}
	if len(compiledRules) > 0 {
		configuration["route"] = map[string]any{"rules": compiledRules}
	}
	encoded, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode compiled configuration: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

// PreviewNodeRules exposes only static rule ordering and affected Listener
// tags. It intentionally does not include endpoint credentials or runtime
// claims such as DNS/SNI matches that cannot be proven before traffic arrives.
func (s *Store) PreviewNodeRules(ctx context.Context, nodeID string) (map[string]any, error) {
	rules, err := s.ListEffectiveRouteRules(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	sortRouteRules(rules)
	listeners, err := s.ListListeners(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	tags := make([]map[string]string, 0, len(listeners))
	for _, listener := range listeners {
		tags = append(tags, map[string]string{"id": listener.ID, "name": listener.Name, "tag": "listener-" + listener.ID})
	}
	compiled := make([]map[string]any, 0, len(rules))
	warnings := []string{"预览仅展示静态规则顺序和已知 Listener；DNS 解析、协议嗅探与 TLS/QUIC SNI 的运行时命中无法静态保证。"}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		compiled = append(compiled, map[string]any{"id": rule.ID, "priority": rule.Priority, "rule": compileRouteRule(rule)})
		if rule.Action == "outbound" {
			warnings = append(warnings, "规则 "+rule.ID+" 依赖出站标签 "+rule.OutboundTag+"；发布前必须存在对应的受管出站。")
		}
	}
	return map[string]any{"node_id": nodeID, "rules": compiled, "listeners": tags, "warnings": warnings}, nil
}

func compileRouteRule(rule RouteRule) map[string]any {
	compiled := map[string]any{}
	if len(rule.Domains) > 0 {
		compiled["domain"] = rule.Domains
	}
	if len(rule.DomainSuffix) > 0 {
		compiled["domain_suffix"] = rule.DomainSuffix
	}
	if len(rule.CIDRs) > 0 {
		compiled["ip_cidr"] = rule.CIDRs
	}
	if rule.Port != 0 {
		compiled["port"] = rule.Port
	}
	if rule.Network != "" {
		compiled["network"] = rule.Network
	}
	if rule.Protocol != "" {
		compiled["protocol"] = rule.Protocol
	}
	if rule.InboundTag != "" {
		compiled["inbound"] = []string{rule.InboundTag}
	}
	if rule.EndpointName != "" {
		compiled["auth_user"] = []string{rule.EndpointName}
	}
	switch rule.Action {
	case "direct":
		compiled["action"] = "route"
		compiled["outbound"] = "direct"
	case "reject":
		compiled["action"] = "reject"
	case "outbound":
		compiled["action"] = "route"
		compiled["outbound"] = rule.OutboundTag
	}
	return compiled
}

// compileOutbound renders a managed outbound as a sing-box outbound object.
func compileOutbound(ob Outbound, tag string) map[string]any {
	if ob.Type == "direct" {
		return map[string]any{"type": "direct", "tag": tag}
	}
	outbound := map[string]any{"type": ob.Type, "tag": tag, "server": ob.Server, "server_port": ob.ServerPort}
	if ob.Type == "socks" {
		outbound["version"] = "5"
	}
	if ob.Username != "" {
		outbound["username"] = ob.Username
		outbound["password"] = ob.Password
	}
	return outbound
}

// compileTransport renders the supported VLESS transports.
func compileTransport(t TransportOptions) map[string]any {
	switch t.Type {
	case "ws":
		transport := map[string]any{"type": "ws", "path": t.Path}
		if t.Host != "" {
			transport["headers"] = map[string]any{"Host": t.Host}
		}
		return transport
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": t.ServiceName}
	}
	return nil
}

type endpointWithCredentials struct {
	Endpoint
	Credentials EndpointCredentials
}

func (s *Store) loadEndpointCredentials(ctx context.Context, listenerID string) ([]endpointWithCredentials, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, listener_id, name, credentials, enabled, outbound_id FROM endpoints WHERE listener_id = ? AND enabled = 1 ORDER BY name, id`, listenerID)
	if err != nil {
		return nil, fmt.Errorf("load endpoint credentials: %w", err)
	}
	defer rows.Close()
	var endpoints []endpointWithCredentials
	for rows.Next() {
		var endpoint endpointWithCredentials
		var encrypted []byte
		if err := rows.Scan(&endpoint.ID, &endpoint.ListenerID, &endpoint.Name, &encrypted, &endpoint.Enabled, &endpoint.OutboundID); err != nil {
			return nil, err
		}
		plain, err := security.Decrypt(s.masterKey, encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt endpoint credentials: %w", err)
		}
		if err := json.Unmarshal(plain, &endpoint.Credentials); err != nil {
			return nil, fmt.Errorf("decode endpoint credentials: %w", err)
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}

func (s *Store) compileInbound(ctx context.Context, listener Listener, endpoints []endpointWithCredentials) (map[string]any, error) {
	if err := ValidateProtocolSpec(listener.Spec); err != nil {
		return nil, err
	}
	inbound := map[string]any{"type": listener.Spec.Protocol, "tag": "listener-" + listener.ID, "listen": listener.ListenAddr, "listen_port": listener.BackendPort}
	if listener.Spec.TLS.Enabled {
		tls, err := s.compileTLS(ctx, listener)
		if err != nil {
			return nil, err
		}
		inbound["tls"] = tls
	}
	if transport := compileTransport(listener.Spec.Transport); transport != nil {
		inbound["transport"] = transport
	}
	users := make([]map[string]any, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if err := ValidateEndpointCredentials(listener.Spec.Protocol, endpoint.Credentials); err != nil {
			return nil, err
		}
		user, err := compileUser(listener.Spec.Protocol, endpoint)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	inbound["users"] = users
	return inbound, nil
}

func (s *Store) compileTLS(ctx context.Context, listener Listener) (map[string]any, error) {
	spec := listener.Spec
	configuration := map[string]any{"enabled": true}
	if len(spec.TLS.ALPN) > 0 {
		configuration["alpn"] = spec.TLS.ALPN
	}
	if spec.TLS.MinVersion != "" {
		configuration["min_version"] = spec.TLS.MinVersion
	}
	if spec.TLS.MaxVersion != "" {
		configuration["max_version"] = spec.TLS.MaxVersion
	}
	if len(spec.TLS.CipherSuites) > 0 {
		configuration["cipher_suites"] = spec.TLS.CipherSuites
	}
	if spec.Reality.Enabled {
		privateKey, err := s.loadRealityPrivateKey(ctx, spec.Reality.KeyID)
		if err != nil {
			return nil, err
		}
		configuration["server_name"] = spec.Reality.HandshakeServer
		configuration["reality"] = map[string]any{
			"enabled":     true,
			"handshake":   map[string]any{"server": spec.Reality.HandshakeServer, "server_port": spec.Reality.HandshakePort},
			"private_key": privateKey,
			"short_id":    spec.Reality.ShortIDs,
		}
		return configuration, nil
	}
	certificate, privateKey, err := s.loadOrCreateHysteria2Certificate(ctx, listener.ID, listener.Domain)
	if err != nil {
		return nil, err
	}
	configuration["certificate"] = []string{certificate}
	configuration["key"] = []string{privateKey}
	return configuration, nil
}

func compileUser(protocol string, endpoint endpointWithCredentials) (map[string]any, error) {
	c := endpoint.Credentials
	user := map[string]any{"name": endpoint.Name}
	switch protocol {
	case "vless":
		user["uuid"] = c.UUID
		if c.Flow != "" {
			user["flow"] = c.Flow
		}
	case "hysteria2":
		user["password"] = c.Password
	default:
		return nil, fmt.Errorf("unsupported inbound protocol %q", protocol)
	}
	return user, nil
}
