package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/liyuwei007036/polaris/internal/security"
)

type MihomoProfileInput struct {
	Name          string   `json:"name"`
	Server        string   `json:"server,omitempty"`
	EndpointIDs   []string `json:"endpoint_ids"`
	Strategy      string   `json:"strategy"`
	RulePreset    string   `json:"rule_preset"`
	ProxyDomains  []string `json:"proxy_domains"`
	DirectDomains []string `json:"direct_domains"`
	RejectDomains []string `json:"reject_domains"`
	ProxyCIDRs    []string `json:"proxy_cidrs"`
	DefaultAction string   `json:"default_action"`
}

type mihomoGroupDefinition struct {
	Name        string
	Strategy    string
	EndpointIDs []string
	Aliases     map[string]string
}

type mihomoRuleDefinition struct {
	RulePreset    string
	ProxyDomains  []string
	DirectDomains []string
	RejectDomains []string
	ProxyCIDRs    []string
	DefaultAction string
	Rules         []MihomoRule
}

func (s *Store) GenerateMihomoYAML(ctx context.Context, input MihomoProfileInput) (string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Server = strings.TrimSpace(input.Server)
	if input.Name == "" || len(input.EndpointIDs) == 0 {
		return "", errors.New("Mihomo profile name and at least one access account are required")
	}
	if strings.ContainsAny(input.Server, "\r\n,/:") {
		return "", errors.New("invalid Mihomo client server address")
	}
	return s.generateMihomoYAML(ctx, input.Name, input.Server, []mihomoGroupDefinition{{
		Name: "默认代理组", Strategy: input.Strategy, EndpointIDs: input.EndpointIDs,
	}}, mihomoRuleDefinition{
		RulePreset: input.RulePreset, ProxyDomains: input.ProxyDomains, DirectDomains: input.DirectDomains,
		RejectDomains: input.RejectDomains, ProxyCIDRs: input.ProxyCIDRs, DefaultAction: input.DefaultAction,
	})
}

func (s *Store) generateMihomoYAML(ctx context.Context, name, fallbackServer string, groups []mihomoGroupDefinition, rules mihomoRuleDefinition) (string, error) {
	if strings.TrimSpace(name) == "" || len(groups) == 0 {
		return "", errors.New("Mihomo profile name and at least one proxy group are required")
	}
	if rules.RulePreset == "" {
		rules.RulePreset = "custom"
	}
	if rules.RulePreset != "china-direct" && rules.RulePreset != "proxy-all" && rules.RulePreset != "direct-all" && rules.RulePreset != "custom" {
		return "", errors.New("unsupported Mihomo rule preset")
	}
	if rules.DefaultAction == "" {
		rules.DefaultAction = "PROXY"
	}
	if rules.DefaultAction != "DIRECT" && rules.DefaultAction != "PROXY" {
		return "", errors.New("Mihomo default action must be DIRECT or PROXY")
	}
	proxies := make([]map[string]any, 0)
	proxyNames := make(map[string]string)
	usedProxyNames := make(map[string]string)
	groupObjects := make([]map[string]any, 0, len(groups)+1)
	groupNames := make([]string, 0, len(groups))
	for _, group := range groups {
		if group.Strategy != "select" && group.Strategy != "url-test" && group.Strategy != "fallback" {
			return "", errors.New("unsupported Mihomo proxy group strategy")
		}
		if strings.TrimSpace(group.Name) == "" || len(group.EndpointIDs) == 0 {
			return "", errors.New("Mihomo proxy group name and nodes are required")
		}
		names := make([]string, 0, len(group.EndpointIDs))
		for _, endpointID := range group.EndpointIDs {
			proxyKey := endpointID
			proxyName, exists := proxyNames[proxyKey]
			if !exists {
				proxy, err := s.mihomoProxy(ctx, endpointID, fallbackServer)
				if err != nil {
					return "", err
				}
				proxyName = proxy["name"].(string)
				if previousKey, used := usedProxyNames[proxyName]; used && previousKey != proxyKey {
					return "", fmt.Errorf("Mihomo node alias %q is used by more than one node", proxyName)
				}
				usedProxyNames[proxyName] = proxyKey
				proxyNames[proxyKey] = proxyName
				proxies = append(proxies, proxy)
			}
			names = append(names, proxyName)
		}
		groupObject := map[string]any{"name": group.Name, "type": group.Strategy, "proxies": names}
		if group.Strategy != "select" {
			groupObject["url"] = "https://www.gstatic.com/generate_204"
			groupObject["interval"] = 300
		}
		groupObjects = append(groupObjects, groupObject)
		groupNames = append(groupNames, group.Name)
	}
	groupObjects = append(groupObjects, map[string]any{"name": "PROXY", "type": "select", "proxies": groupNames})

	var builder strings.Builder
	builder.WriteString("mixed-port: 7890\nallow-lan: false\nmode: rule\nlog-level: info\nproxies:\n")
	for _, proxy := range proxies {
		encoded, _ := json.Marshal(proxy)
		builder.WriteString("  - " + string(encoded) + "\n")
	}
	builder.WriteString("proxy-groups:\n")
	for _, group := range groupObjects {
		encoded, _ := json.Marshal(group)
		builder.WriteString("  - " + string(encoded) + "\n")
	}
	builder.WriteString("rules:\n")
	appendDomainRules := func(values []string, action string) {
		for _, value := range values {
			value = strings.TrimSpace(strings.TrimPrefix(value, "."))
			if value != "" && !strings.ContainsAny(value, "\r\n,") {
				builder.WriteString("  - DOMAIN-SUFFIX," + value + "," + action + "\n")
			}
		}
	}
	appendDomainRules(rules.RejectDomains, "REJECT")
	appendDomainRules(rules.DirectDomains, "DIRECT")
	if rules.RulePreset == "china-direct" {
		builder.WriteString("  - GEOSITE,CN,DIRECT\n")
		builder.WriteString("  - GEOIP,CN,DIRECT,no-resolve\n")
	}
	appendDomainRules(rules.ProxyDomains, "PROXY")
	for _, cidr := range rules.ProxyCIDRs {
		cidr = strings.TrimSpace(cidr)
		if cidr != "" && !strings.ContainsAny(cidr, "\r\n,") {
			builder.WriteString("  - IP-CIDR," + cidr + ",PROXY,no-resolve\n")
		}
	}
	for _, rule := range rules.Rules {
		line := rule.Type
		if rule.Type != "MATCH" {
			line += "," + rule.Value
		}
		line += "," + rule.Action
		if rule.NoResolve {
			line += ",no-resolve"
		}
		builder.WriteString("  - " + line + "\n")
	}
	switch rules.RulePreset {
	case "proxy-all", "china-direct":
		builder.WriteString("  - MATCH,PROXY\n")
	case "direct-all":
		builder.WriteString("  - MATCH,DIRECT\n")
	default:
		builder.WriteString("  - MATCH," + rules.DefaultAction + "\n")
	}
	return builder.String(), nil
}

func (s *Store) mihomoProxy(ctx context.Context, endpointID, fallbackServer string) (map[string]any, error) {
	var endpoint endpointWithCredentials
	var encrypted []byte
	var listener Listener
	var spec string
	var clientAddress string
	var nodeName string
	err := s.db.QueryRowContext(ctx, `SELECT e.id, e.listener_id, e.name, e.alias, e.credentials, e.enabled, l.id, l.node_id, l.name, l.connection_domain, l.listen_address, l.port, l.backend_port, l.enabled, l.spec
		, n.client_address, n.name FROM endpoints e JOIN listeners l ON l.id=e.listener_id JOIN nodes n ON n.id=l.node_id
		WHERE e.id=? AND e.enabled=1 AND l.enabled=1 AND n.revoked_at IS NULL`, endpointID).
		Scan(&endpoint.ID, &endpoint.ListenerID, &endpoint.Name, &endpoint.Alias, &encrypted, &endpoint.Enabled, &listener.ID, &listener.NodeID, &listener.Name, &listener.Domain, &listener.ListenAddr, &listener.Port, &listener.BackendPort, &listener.Enabled, &spec, &clientAddress, &nodeName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(spec), &listener.Spec); err != nil {
		return nil, err
	}
	plain, err := security.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(plain, &endpoint.Credentials); err != nil {
		return nil, err
	}
	server := strings.TrimSpace(listener.Domain)
	if server == "" {
		server = strings.TrimSpace(clientAddress)
	}
	if server == "" {
		server = fallbackServer
	}
	if server == "" {
		return nil, fmt.Errorf("node for listener %s has no client connection address", listener.Name)
	}
	displayName := mihomoEndpointDisplayName(endpoint.Alias, nodeName, listener.Name, endpoint.Name)
	proxy := map[string]any{"name": displayName, "server": server, "port": listener.Port}
	switch listener.Spec.Protocol {
	case "vless":
		proxy["type"], proxy["uuid"] = "vless", endpoint.Credentials.UUID
		proxy["udp"] = true
		if endpoint.Credentials.Flow != "" {
			proxy["flow"] = endpoint.Credentials.Flow
		}
	case "hysteria2":
		proxy["type"], proxy["password"] = "hysteria2", endpoint.Credentials.Password
	default:
		return nil, fmt.Errorf("protocol %s is not supported by Mihomo YAML export", listener.Spec.Protocol)
	}
	if listener.Spec.TLS.Enabled {
		proxy["tls"] = true
		if len(listener.Spec.TLS.ALPN) > 0 {
			proxy["alpn"] = listener.Spec.TLS.ALPN
		}
	}
	if listener.Spec.Protocol == "hysteria2" {
		proxy["skip-cert-verify"] = true
		if listener.Domain != "" {
			proxy["sni"] = listener.Domain
		}
	}
	if listener.Spec.Reality.Enabled {
		publicKey, err := s.realityPublicKey(ctx, listener.Spec.Reality.KeyID)
		if err != nil {
			return nil, fmt.Errorf("load Reality public key for listener %s: %w", listener.Name, err)
		}
		reality := map[string]any{"public-key": publicKey}
		if len(listener.Spec.Reality.ShortIDs) > 0 {
			reality["short-id"] = listener.Spec.Reality.ShortIDs[0]
		}
		proxy["tls"] = true
		proxy["servername"] = listener.Spec.Reality.HandshakeServer
		proxy["client-fingerprint"] = "chrome"
		proxy["reality-opts"] = reality
	}
	switch listener.Spec.Transport.Type {
	case "":
	case "ws":
		proxy["network"] = "ws"
		options := map[string]any{"path": listener.Spec.Transport.Path}
		if listener.Spec.Transport.Host != "" {
			options["headers"] = map[string]string{"Host": listener.Spec.Transport.Host}
		}
		proxy["ws-opts"] = options
	case "grpc":
		proxy["network"] = "grpc"
		proxy["grpc-opts"] = map[string]any{"grpc-service-name": listener.Spec.Transport.ServiceName}
	default:
		return nil, fmt.Errorf("transport %s on listener %s is not supported by Mihomo YAML export", listener.Spec.Transport.Type, listener.Name)
	}
	return proxy, nil
}

// mihomoEndpointName resolves the name a client sees for a user, whether or not
// that user can be connected to right now. Switching a user, its service or its
// server off is reversible, so the name has to stay resolvable: the client
// configurations naming it must remain editable while it is off.
func (s *Store) mihomoEndpointName(ctx context.Context, endpointID string) (string, error) {
	var name, alias, listenerName, nodeName string
	err := s.db.QueryRowContext(ctx, `SELECT e.name, e.alias, l.name, n.name FROM endpoints e
		JOIN listeners l ON l.id = e.listener_id JOIN nodes n ON n.id = l.node_id WHERE e.id = ?`, endpointID).
		Scan(&name, &alias, &listenerName, &nodeName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load client node name: %w", err)
	}
	return mihomoEndpointDisplayName(alias, nodeName, listenerName, name), nil
}

func mihomoEndpointDisplayName(alias, nodeName, listenerName, endpointName string) string {
	if displayName := strings.TrimSpace(alias); displayName != "" {
		return displayName
	}
	return nodeName + " · " + listenerName + " · " + endpointName
}
