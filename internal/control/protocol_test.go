package control_test

import (
	"testing"

	"github.com/sb-control/sb-control/internal/control"
)

func TestVLESSWebSocketAllowsDefaultPath(t *testing.T) {
	spec := control.ProtocolSpec{
		Protocol:  "vless",
		Network:   "tcp",
		Transport: control.TransportOptions{Type: "ws"},
	}
	if err := control.ValidateProtocolSpec(spec); err != nil {
		t.Fatalf("VLESS WebSocket with the sing-box default path was rejected: %v", err)
	}
}

func TestGRPCTransportStillRequiresServiceName(t *testing.T) {
	spec := control.ProtocolSpec{
		Protocol:  "vless",
		Network:   "tcp",
		Transport: control.TransportOptions{Type: "grpc"},
	}
	if err := control.ValidateProtocolSpec(spec); err == nil {
		t.Fatal("gRPC transport without a service name was accepted")
	}
}
