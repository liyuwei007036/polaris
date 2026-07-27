package control

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// ProtocolSpec is a closed, typed representation of the common listener
// parameters. It is persisted as JSON internally, but the public API never
// accepts an arbitrary sing-box configuration fragment.
type ProtocolSpec struct {
	Protocol  string           `json:"protocol"`
	Network   string           `json:"network"`
	TLS       TLSOptions       `json:"tls"`
	Reality   RealityOptions   `json:"reality"`
	Transport TransportOptions `json:"transport"`
	Hysteria  HysteriaOptions  `json:"hysteria"`
	TUIC      TUICOptions      `json:"tuic"`
	Snell     SnellOptions     `json:"snell"`
	ShadowTLS ShadowTLSOptions `json:"shadowtls"`
}

type TLSOptions struct {
	Enabled       bool     `json:"enabled"`
	ServerName    string   `json:"server_name"`
	ALPN          []string `json:"alpn"`
	MinVersion    string   `json:"min_version"`
	MaxVersion    string   `json:"max_version"`
	CipherSuites  []string `json:"cipher_suites"`
	CertificateID string   `json:"certificate_id"`
}
type RealityOptions struct {
	Enabled         bool     `json:"enabled"`
	HandshakeServer string   `json:"handshake_server"`
	HandshakePort   uint16   `json:"handshake_port"`
	ShortIDs        []string `json:"short_ids"`
	KeyID           string   `json:"key_id"`
}
type TransportOptions struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Host        string `json:"host"`
	ServiceName string `json:"service_name"`
}
type HysteriaOptions struct {
	UpMbps      uint32 `json:"up_mbps"`
	DownMbps    uint32 `json:"down_mbps"`
	Obfuscation string `json:"obfuscation"`
}
type TUICOptions struct {
	CongestionControl string `json:"congestion_control"`
}
type SnellOptions struct {
	Version uint8  `json:"version"`
	Mode    string `json:"mode"`
}
type ShadowTLSOptions struct {
	Version         uint8  `json:"version"`
	HandshakeServer string `json:"handshake_server"`
	HandshakePort   uint16 `json:"handshake_port"`
}

type EndpointCredentials struct {
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	Username string `json:"username,omitempty"`
	Method   string `json:"method,omitempty"`
	PSK      string `json:"psk,omitempty"`
	Flow     string `json:"flow,omitempty"`
	AlterID  uint32 `json:"alter_id,omitempty"`
}

type protocolDefinition struct {
	networks  []string
	endpoints bool
}

var protocolDefinitions = map[string]protocolDefinition{
	"direct": {networks: []string{"tcp", "udp"}}, "mixed": {networks: []string{"tcp"}}, "socks": {networks: []string{"tcp"}, endpoints: true},
	"http": {networks: []string{"tcp"}, endpoints: true}, "shadowsocks": {networks: []string{"tcp", "udp"}, endpoints: true},
	"vmess": {networks: []string{"tcp"}, endpoints: true}, "trojan": {networks: []string{"tcp"}, endpoints: true},
	"naive": {networks: []string{"tcp"}, endpoints: true}, "hysteria": {networks: []string{"udp"}, endpoints: true},
	"shadowtls": {networks: []string{"tcp"}, endpoints: true}, "tuic": {networks: []string{"udp"}, endpoints: true},
	"hysteria2": {networks: []string{"udp"}, endpoints: true}, "vless": {networks: []string{"tcp"}, endpoints: true},
	"anytls": {networks: []string{"tcp"}, endpoints: true}, "snell": {networks: []string{"tcp"}, endpoints: true},
	"tun": {networks: []string{"tcp", "udp"}}, "redirect": {networks: []string{"tcp"}}, "tproxy": {networks: []string{"tcp", "udp"}},
	"cloudflared": {networks: []string{"tcp"}},
}

func SupportedProtocols() []string {
	protocols := make([]string, 0, len(protocolDefinitions))
	for protocol := range protocolDefinitions {
		protocols = append(protocols, protocol)
	}
	sort.Strings(protocols)
	return protocols
}

func ValidateProtocolSpec(spec ProtocolSpec) error {
	definition, ok := protocolDefinitions[spec.Protocol]
	if !ok {
		return fmt.Errorf("unsupported sing-box inbound protocol %q", spec.Protocol)
	}
	if spec.Network != "tcp" && spec.Network != "udp" {
		return errors.New("listener network must be tcp or udp")
	}
	validNetwork := false
	for _, network := range definition.networks {
		if network == spec.Network {
			validNetwork = true
			break
		}
	}
	if !validNetwork {
		return fmt.Errorf("protocol %s does not support %s", spec.Protocol, spec.Network)
	}
	if spec.Reality.Enabled && spec.Protocol != "vless" {
		return errors.New("Reality is only supported by VLESS listeners")
	}
	if spec.Reality.Enabled && (!spec.TLS.Enabled || spec.Reality.HandshakeServer == "" || spec.Reality.HandshakePort == 0) {
		return errors.New("Reality requires TLS, handshake server, and handshake port")
	}
	if spec.Reality.Enabled && spec.Reality.KeyID == "" {
		return errors.New("Reality requires a managed Reality key")
	}
	if spec.TLS.Enabled && !spec.Reality.Enabled && spec.TLS.CertificateID == "" {
		return errors.New("TLS listener requires a managed certificate")
	}
	for _, version := range []string{spec.TLS.MinVersion, spec.TLS.MaxVersion} {
		if version != "" && version != "1.0" && version != "1.1" && version != "1.2" && version != "1.3" {
			return errors.New("TLS version must be 1.0, 1.1, 1.2, or 1.3")
		}
	}
	if spec.Transport.Type != "" && spec.Transport.Type != "http" && spec.Transport.Type != "ws" && spec.Transport.Type != "httpupgrade" && spec.Transport.Type != "grpc" && spec.Transport.Type != "quic" {
		return errors.New("unsupported listener transport")
	}
	if spec.Transport.Type != "" {
		switch spec.Protocol {
		case "vless", "vmess", "trojan", "http":
		default:
			return errors.New("transport is only supported by VLESS, VMess, Trojan, or HTTP listeners")
		}
	}
	if (spec.Transport.Type == "ws" || spec.Transport.Type == "httpupgrade" || spec.Transport.Type == "grpc") && spec.Transport.Path == "" && spec.Transport.ServiceName == "" {
		return errors.New("selected transport requires a path or service name")
	}
	if spec.Protocol == "snell" && spec.Snell.Version != 0 && spec.Snell.Version != 5 && spec.Snell.Version != 6 {
		return errors.New("Snell version must be 5 or 6")
	}
	if spec.Protocol == "shadowtls" && spec.ShadowTLS.Version != 0 && spec.ShadowTLS.Version != 2 && spec.ShadowTLS.Version != 3 {
		return errors.New("ShadowTLS version must be 2 or 3")
	}
	return nil
}

func ProtocolSupportsEndpoints(protocol string) bool { return protocolDefinitions[protocol].endpoints }

func ValidateListenerAddress(address string, port uint16) error {
	if port == 0 {
		return errors.New("listener port is required")
	}
	if address == "" {
		return errors.New("listener address is required")
	}
	if address != "0.0.0.0" && address != "::" && net.ParseIP(address) == nil {
		return errors.New("listener address must be an IP address")
	}
	return nil
}

func ValidateEndpointCredentials(protocol string, credentials EndpointCredentials) error {
	if !ProtocolSupportsEndpoints(protocol) {
		return fmt.Errorf("protocol %s does not accept endpoints", protocol)
	}
	switch protocol {
	case "vless", "vmess", "tuic":
		if credentials.UUID == "" {
			return fmt.Errorf("protocol %s requires a UUID", protocol)
		}
	case "trojan", "hysteria", "hysteria2", "anytls", "shadowtls", "snell":
		if credentials.Password == "" && credentials.PSK == "" {
			return fmt.Errorf("protocol %s requires a password or PSK", protocol)
		}
	case "shadowsocks":
		if credentials.Password == "" || credentials.Method == "" {
			return errors.New("Shadowsocks requires method and password")
		}
	case "socks", "http", "naive":
		if credentials.Username == "" || credentials.Password == "" {
			return fmt.Errorf("protocol %s requires username and password", protocol)
		}
	}
	if strings.ContainsAny(credentials.Username, "\r\n") {
		return errors.New("endpoint username contains a line break")
	}
	return nil
}
