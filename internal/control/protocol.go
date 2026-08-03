package control

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
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
}

type TLSOptions struct {
	Enabled      bool     `json:"enabled"`
	ALPN         []string `json:"alpn"`
	MinVersion   string   `json:"min_version"`
	MaxVersion   string   `json:"max_version"`
	CipherSuites []string `json:"cipher_suites"`
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

type EndpointCredentials struct {
	UUID     string `json:"uuid,omitempty"`
	Password string `json:"password,omitempty"`
	Flow     string `json:"flow,omitempty"`
}

type protocolDefinition struct {
	network string
}

var protocolDefinitions = map[string]protocolDefinition{
	"hysteria2": {network: "udp"},
	"vless":     {network: "tcp"},
}

func SupportedProtocols() []string {
	return []string{"hysteria2", "vless"}
}

func ValidateProtocolSpec(spec ProtocolSpec) error {
	definition, ok := protocolDefinitions[spec.Protocol]
	if !ok {
		return fmt.Errorf("unsupported sing-box inbound protocol %q", spec.Protocol)
	}
	if spec.Network != definition.network {
		return fmt.Errorf("protocol %s does not support %s", spec.Protocol, spec.Network)
	}
	if spec.Protocol == "hysteria2" {
		if !spec.TLS.Enabled {
			return errors.New("Hysteria2 requires TLS")
		}
		if spec.Reality.Enabled || spec.Transport.Type != "" {
			return errors.New("Hysteria2 does not support Reality or VLESS transports")
		}
	}
	if spec.Protocol == "vless" {
		if spec.Reality.Enabled {
			if !spec.TLS.Enabled || spec.Reality.HandshakeServer == "" || spec.Reality.HandshakePort == 0 {
				return errors.New("VLESS Reality requires TLS, handshake server, and handshake port")
			}
			if spec.Reality.KeyID == "" {
				return errors.New("VLESS Reality requires a managed Reality key")
			}
			if spec.Transport.Type != "" {
				return errors.New("VLESS Reality cannot be combined with WebSocket or gRPC")
			}
		} else {
			if spec.TLS.Enabled {
				return errors.New("VLESS with TLS is not supported; use Reality, WebSocket, or gRPC")
			}
			if spec.Transport.Type != "ws" && spec.Transport.Type != "grpc" {
				return errors.New("VLESS requires Reality, WebSocket, or gRPC")
			}
		}
	}
	for _, version := range []string{spec.TLS.MinVersion, spec.TLS.MaxVersion} {
		if version != "" && version != "1.0" && version != "1.1" && version != "1.2" && version != "1.3" {
			return errors.New("TLS version must be 1.0, 1.1, 1.2, or 1.3")
		}
	}
	if spec.Transport.Type != "" && spec.Transport.Type != "ws" && spec.Transport.Type != "grpc" {
		return errors.New("unsupported listener transport")
	}
	if spec.Transport.Type == "grpc" && spec.Transport.ServiceName == "" {
		return errors.New("gRPC transport requires a service name")
	}
	return nil
}

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
	switch protocol {
	case "vless":
		if credentials.UUID == "" {
			return errors.New("VLESS requires a UUID")
		}
	case "hysteria2":
		if credentials.Password == "" {
			return errors.New("Hysteria2 requires a password")
		}
	default:
		return fmt.Errorf("unsupported inbound protocol %q", protocol)
	}
	return nil
}

func GenerateEndpointCredentials(protocol string) (EndpointCredentials, error) {
	randomBytes := func(size int) ([]byte, error) {
		value := make([]byte, size)
		if _, err := rand.Read(value); err != nil {
			return nil, err
		}
		return value, nil
	}
	randomPassword := func(size int) (string, error) {
		value, err := randomBytes(size)
		if err != nil {
			return "", err
		}
		return base64.RawURLEncoding.EncodeToString(value), nil
	}
	randomUUID := func() (string, error) {
		value, err := randomBytes(16)
		if err != nil {
			return "", err
		}
		value[6] = (value[6] & 0x0f) | 0x40
		value[8] = (value[8] & 0x3f) | 0x80
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
			value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
	}

	switch protocol {
	case "vless":
		uuid, err := randomUUID()
		return EndpointCredentials{UUID: uuid}, err
	case "hysteria2":
		password, err := randomPassword(24)
		return EndpointCredentials{Password: password}, err
	default:
		return EndpointCredentials{}, fmt.Errorf("unsupported inbound protocol %q", protocol)
	}
}

func GenerateRealityShortID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate Reality short ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
