package control_test

import (
	"reflect"
	"testing"

	"github.com/liyuwei007036/polaris/internal/control"
)

func TestSupportedInboundProtocols(t *testing.T) {
	want := []string{"hysteria2", "vless"}
	if got := control.SupportedProtocols(); !reflect.DeepEqual(got, want) {
		t.Fatalf("supported protocols = %v, want %v", got, want)
	}
}

func TestSupportedInboundModes(t *testing.T) {
	tests := []struct {
		name string
		spec control.ProtocolSpec
	}{
		{
			name: "Hysteria2",
			spec: control.ProtocolSpec{
				Protocol: "hysteria2",
				Network:  "udp",
				TLS:      control.TLSOptions{Enabled: true},
			},
		},
		{
			name: "VLESS Reality",
			spec: control.ProtocolSpec{
				Protocol: "vless",
				Network:  "tcp",
				TLS:      control.TLSOptions{Enabled: true},
				Reality: control.RealityOptions{
					Enabled:         true,
					HandshakeServer: "www.example.com",
					HandshakePort:   443,
					KeyID:           "reality-key-id",
				},
			},
		},
		{
			name: "VLESS WebSocket",
			spec: control.ProtocolSpec{
				Protocol:  "vless",
				Network:   "tcp",
				TLS:       control.TLSOptions{Enabled: true, ALPN: []string{"http/1.1"}},
				Transport: control.TransportOptions{Type: "ws"},
			},
		},
		{
			name: "VLESS gRPC",
			spec: control.ProtocolSpec{
				Protocol:  "vless",
				Network:   "tcp",
				TLS:       control.TLSOptions{Enabled: true, ALPN: []string{"h2"}},
				Transport: control.TransportOptions{Type: "grpc", ServiceName: "proxy"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := control.ValidateProtocolSpec(test.spec); err != nil {
				t.Fatalf("supported inbound mode was rejected: %v", err)
			}
		})
	}
}

func TestProtocolValidationAndCredentialGenerationRejectRemovedInbounds(t *testing.T) {
	removed := []string{
		"anytls", "cloudflared", "direct", "http", "hysteria", "mixed",
		"naive", "redirect", "shadowsocks", "shadowtls", "snell", "socks",
		"tproxy", "trojan", "tuic", "tun", "vmess",
	}
	for _, protocol := range removed {
		t.Run(protocol, func(t *testing.T) {
			spec := control.ProtocolSpec{Protocol: protocol, Network: "tcp"}
			if err := control.ValidateProtocolSpec(spec); err == nil {
				t.Fatalf("removed inbound protocol %q was accepted", protocol)
			}
			if _, err := control.GenerateEndpointCredentials(protocol); err == nil {
				t.Fatalf("generated credentials for removed inbound protocol %q", protocol)
			}
		})
	}
}

func TestUnsupportedVLESSCombinationsAreRejected(t *testing.T) {
	tests := []struct {
		name string
		spec control.ProtocolSpec
	}{
		{
			name: "plain VLESS",
			spec: control.ProtocolSpec{Protocol: "vless", Network: "tcp"},
		},
		{
			name: "removed HTTPUpgrade transport",
			spec: control.ProtocolSpec{
				Protocol:  "vless",
				Network:   "tcp",
				Transport: control.TransportOptions{Type: "httpupgrade"},
			},
		},
		{
			name: "gRPC without service name",
			spec: control.ProtocolSpec{
				Protocol:  "vless",
				Network:   "tcp",
				Transport: control.TransportOptions{Type: "grpc"},
			},
		},
		{
			name: "Reality with WebSocket",
			spec: control.ProtocolSpec{
				Protocol:  "vless",
				Network:   "tcp",
				TLS:       control.TLSOptions{Enabled: true},
				Reality:   control.RealityOptions{Enabled: true, HandshakeServer: "www.example.com", HandshakePort: 443, KeyID: "reality-key-id"},
				Transport: control.TransportOptions{Type: "ws"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := control.ValidateProtocolSpec(test.spec); err == nil {
				t.Fatal("unsupported VLESS combination was accepted")
			}
		})
	}
}
