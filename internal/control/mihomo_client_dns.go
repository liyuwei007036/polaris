package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

// MihomoClientDNS is the form editor for the subscription's DNS section. The
// generator no longer decides any of this: a client configuration carries
// whatever the operator entered, and leaving it disabled emits no dns block at
// all so the client keeps its own settings.
type MihomoClientDNS struct {
	Enable                       bool     `json:"enable"`
	IPv6                         bool     `json:"ipv6"`
	EnhancedMode                 string   `json:"enhanced_mode"`
	FakeIPRange                  string   `json:"fake_ip_range"`
	FakeIPFilterMode             string   `json:"fake_ip_filter_mode"`
	FakeIPFilter                 []string `json:"fake_ip_filter"`
	RespectRules                 bool     `json:"respect_rules"`
	DefaultNameserver            []string `json:"default_nameserver"`
	Nameserver                   []string `json:"nameserver"`
	ProxyServerNameserver        []string `json:"proxy_server_nameserver"`
	DirectNameserver             []string `json:"direct_nameserver"`
	DirectNameserverFollowPolicy bool     `json:"direct_nameserver_follow_policy"`
}

// mihomoYAMLDNS is the emitted shape; field order is the document order.
type mihomoYAMLDNS struct {
	Enable                       bool     `yaml:"enable"`
	IPv6                         bool     `yaml:"ipv6"`
	EnhancedMode                 string   `yaml:"enhanced-mode"`
	FakeIPRange                  string   `yaml:"fake-ip-range,omitempty"`
	FakeIPFilterMode             string   `yaml:"fake-ip-filter-mode,omitempty"`
	FakeIPFilter                 []string `yaml:"fake-ip-filter,omitempty"`
	RespectRules                 bool     `yaml:"respect-rules"`
	DefaultNameserver            []string `yaml:"default-nameserver"`
	Nameserver                   []string `yaml:"nameserver"`
	ProxyServerNameserver        []string `yaml:"proxy-server-nameserver,omitempty"`
	DirectNameserver             []string `yaml:"direct-nameserver,omitempty"`
	DirectNameserverFollowPolicy bool     `yaml:"direct-nameserver-follow-policy"`
}

// defaultMihomoClientDNS is what the generator used to hard-code. It is the
// starting point for a new client configuration and what existing rows are
// migrated to, so their subscriptions keep working unchanged.
func defaultMihomoClientDNS() MihomoClientDNS {
	return MihomoClientDNS{
		Enable:                true,
		EnhancedMode:          "fake-ip",
		FakeIPRange:           "198.18.0.1/16",
		FakeIPFilterMode:      "rule",
		FakeIPFilter:          []string{"MATCH,fake-ip"},
		DefaultNameserver:     []string{"https://223.5.5.5/dns-query"},
		Nameserver:            []string{"https://223.5.5.5/dns-query"},
		ProxyServerNameserver: []string{"https://223.5.5.5/dns-query"},
		DirectNameserver:      []string{"https://223.5.5.5/dns-query"},
	}
}

// adoptHardCodedMihomoClientDNS gives rows written before the DNS section
// became editable exactly the settings the generator used to write, so their
// subscriptions do not change under the operator.
func (s *Store) adoptHardCodedMihomoClientDNS(ctx context.Context) error {
	encoded, err := json.Marshal(mihomoClientDNSV3{Mode: "form", DNS: defaultMihomoClientDNS()})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE mihomo_client_configs_v3 SET dns_json = ? WHERE dns_json = ''`, string(encoded)); err != nil {
		return fmt.Errorf("migrate Mihomo client DNS settings: %w", err)
	}
	return nil
}

func normalizeMihomoDNSServers(servers []string, field string) ([]string, error) {
	result := make([]string, 0, len(servers))
	for _, server := range servers {
		server = strings.TrimSpace(server)
		if server == "" {
			continue
		}
		if strings.ContainsAny(server, "\r\n") {
			return nil, fmt.Errorf("%s entries cannot contain line breaks", field)
		}
		result = append(result, server)
	}
	return result, nil
}

// Mihomo rejects a default-nameserver that is not a plain IP, because that
// list is what resolves the other resolvers' host names. A URL is accepted as
// long as its host is already an address.
func validateMihomoDefaultNameserver(server string) error {
	if server == "system" || net.ParseIP(server) != nil {
		return nil
	}
	host := server
	if parsed, err := url.Parse(server); err == nil && parsed.Host != "" {
		host = parsed.Host
	}
	if bare, _, err := net.SplitHostPort(host); err == nil {
		host = bare
	}
	if net.ParseIP(host) == nil {
		return fmt.Errorf("default-nameserver %q must be a plain IP address", server)
	}
	return nil
}

func normalizeMihomoClientDNS(config *MihomoClientConfig) error {
	config.DNSMode = strings.ToLower(strings.TrimSpace(config.DNSMode))
	if config.DNSMode == "" {
		config.DNSMode = "form"
	}
	// Only the active editor is validated, and both are kept: switching modes
	// back and forth must not throw away what was entered on the other side.
	config.RawDNS = strings.TrimSpace(strings.ReplaceAll(config.RawDNS, "\r\n", "\n"))
	switch config.DNSMode {
	case "form":
		return normalizeMihomoClientDNSForm(config)
	case "text":
		return validateMihomoClientRawDNS(config.RawDNS)
	default:
		return errors.New("DNS mode must be form or text")
	}
}

func normalizeMihomoClientDNSForm(config *MihomoClientConfig) error {
	dns := &config.DNS
	dns.EnhancedMode = strings.ToLower(strings.TrimSpace(dns.EnhancedMode))
	dns.FakeIPFilterMode = strings.ToLower(strings.TrimSpace(dns.FakeIPFilterMode))
	dns.FakeIPRange = strings.TrimSpace(dns.FakeIPRange)
	if !dns.Enable {
		// The section is off, so nothing is emitted and the half-filled
		// values are kept as they are for when it is switched back on.
		return nil
	}
	if dns.EnhancedMode == "" {
		dns.EnhancedMode = "fake-ip"
	}
	switch dns.EnhancedMode {
	case "normal", "fake-ip", "redir-host":
	default:
		return fmt.Errorf("DNS enhanced mode %q is not supported", dns.EnhancedMode)
	}
	var err error
	if dns.FakeIPFilter, err = normalizeMihomoDNSServers(dns.FakeIPFilter, "fake-ip-filter"); err != nil {
		return err
	}
	if dns.EnhancedMode == "fake-ip" {
		if dns.FakeIPFilterMode == "" {
			dns.FakeIPFilterMode = "blacklist"
		}
		switch dns.FakeIPFilterMode {
		case "blacklist", "whitelist", "rule":
		default:
			return fmt.Errorf("fake-ip filter mode %q is not supported", dns.FakeIPFilterMode)
		}
		prefix, err := netip.ParsePrefix(dns.FakeIPRange)
		if err != nil || !prefix.Addr().Is4() {
			return errors.New("fake-ip-range must be an IPv4 CIDR such as 198.18.0.1/16")
		}
	} else {
		dns.FakeIPRange, dns.FakeIPFilterMode, dns.FakeIPFilter = "", "", nil
	}
	for _, servers := range []struct {
		values *[]string
		field  string
	}{
		{&dns.DefaultNameserver, "default-nameserver"},
		{&dns.Nameserver, "nameserver"},
		{&dns.ProxyServerNameserver, "proxy-server-nameserver"},
		{&dns.DirectNameserver, "direct-nameserver"},
	} {
		if *servers.values, err = normalizeMihomoDNSServers(*servers.values, servers.field); err != nil {
			return err
		}
	}
	// The three checks below are the ones Mihomo itself refuses to start on.
	if len(dns.Nameserver) == 0 {
		return errors.New("nameserver is required when the DNS section is enabled")
	}
	if len(dns.DefaultNameserver) == 0 {
		return errors.New("default-nameserver needs at least one entry")
	}
	for _, server := range dns.DefaultNameserver {
		if err := validateMihomoDefaultNameserver(server); err != nil {
			return err
		}
	}
	if dns.RespectRules && len(dns.ProxyServerNameserver) == 0 {
		return errors.New("proxy-server-nameserver is required when DNS follows the routing rules")
	}
	return nil
}

// validateMihomoClientRawDNS only checks what can be judged without running
// Mihomo: that the text is a YAML mapping, plus the two conditions Mihomo
// refuses to start on.
func validateMihomoClientRawDNS(raw string) error {
	if raw == "" {
		return nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		return fmt.Errorf("DNS text is not valid YAML: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("DNS text must be a YAML mapping of dns options")
	}
	var section struct {
		Enable                bool     `yaml:"enable"`
		Nameserver            []string `yaml:"nameserver"`
		RespectRules          bool     `yaml:"respect-rules"`
		ProxyServerNameserver []string `yaml:"proxy-server-nameserver"`
	}
	if err := document.Content[0].Decode(&section); err != nil {
		return fmt.Errorf("DNS text is not a valid dns section: %w", err)
	}
	if section.Enable && len(section.Nameserver) == 0 {
		return errors.New("nameserver is required when the DNS section is enabled")
	}
	if section.RespectRules && len(section.ProxyServerNameserver) == 0 {
		return errors.New("proxy-server-nameserver is required when DNS follows the routing rules")
	}
	return nil
}

// mihomoClientDNSNode renders the configured section, or nil when nothing
// should be written and the client is left to its own DNS settings.
func mihomoClientDNSNode(config MihomoClientConfig) (*yaml.Node, error) {
	var node yaml.Node
	if config.DNSMode == "text" {
		if config.RawDNS == "" {
			return nil, nil
		}
		if err := yaml.Unmarshal([]byte(config.RawDNS), &node); err != nil {
			return nil, fmt.Errorf("encode DNS section: %w", err)
		}
		if len(node.Content) == 0 {
			return nil, nil
		}
		return node.Content[0], nil
	}
	if !config.DNS.Enable {
		return nil, nil
	}
	section := mihomoYAMLDNS{
		Enable:                       true,
		IPv6:                         config.DNS.IPv6,
		EnhancedMode:                 config.DNS.EnhancedMode,
		FakeIPRange:                  config.DNS.FakeIPRange,
		FakeIPFilterMode:             config.DNS.FakeIPFilterMode,
		FakeIPFilter:                 config.DNS.FakeIPFilter,
		RespectRules:                 config.DNS.RespectRules,
		DefaultNameserver:            config.DNS.DefaultNameserver,
		Nameserver:                   config.DNS.Nameserver,
		ProxyServerNameserver:        config.DNS.ProxyServerNameserver,
		DirectNameserver:             config.DNS.DirectNameserver,
		DirectNameserverFollowPolicy: config.DNS.DirectNameserverFollowPolicy,
	}
	if err := node.Encode(section); err != nil {
		return nil, fmt.Errorf("encode DNS section: %w", err)
	}
	return &node, nil
}
