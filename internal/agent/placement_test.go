package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func testAllocator(start uint16) func() (uint16, bool) {
	next := start
	return func() (uint16, bool) {
		port := next
		next++
		return port, true
	}
}

func inboundListen(t *testing.T, configuration, tag string) (string, uint16) {
	t.Helper()
	var parsed struct {
		Inbounds []struct {
			Tag        string `json:"tag"`
			Listen     string `json:"listen"`
			ListenPort uint16 `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configuration), &parsed); err != nil {
		t.Fatalf("rewritten configuration is not valid JSON: %v\n%s", err, configuration)
	}
	for _, inbound := range parsed.Inbounds {
		if inbound.Tag == tag {
			return inbound.Listen, inbound.ListenPort
		}
	}
	t.Fatalf("inbound %q is missing from the rewritten configuration:\n%s", tag, configuration)
	return "", 0
}

const twoOn443 = `{
  "inbounds": [
    {"type": "vless", "tag": "a", "listen": "0.0.0.0", "listen_port": 443, "tls": {"enabled": true}},
    {"type": "vless", "tag": "b", "listen": "0.0.0.0", "listen_port": 443, "tls": {"enabled": true}},
    {"type": "vless", "tag": "alone", "listen": "0.0.0.0", "listen_port": 8443, "tls": {"enabled": true}},
    {"type": "hysteria2", "tag": "udp", "listen": "0.0.0.0", "listen_port": 443, "tls": {"enabled": true}}
  ]
}`

var twoOn443Names = map[string]string{
	"a": "a.example.com", "b": "b.example.com", "alone": "alone.example.com", "udp": "hy2.example.com",
}

// Two services on one port share it through the router; the one alone on 8443
// keeps binding it so sing-box still sees client addresses; the Hysteria2
// service is UDP and never competes with the TCP router on the same number.
func TestPlanRoutesOnlyTheContendedTCPPort(t *testing.T) {
	plan, err := planListenerPlacement(twoOn443, twoOn443Names, map[uint16]bool{}, testAllocator(20000))
	if err != nil {
		t.Fatal(err)
	}
	if address, port := inboundListen(t, plan.configuration, "a"); address != "127.0.0.1" || port != 20000 {
		t.Fatalf("first service on the shared port = %s:%d", address, port)
	}
	if address, port := inboundListen(t, plan.configuration, "b"); address != "127.0.0.1" || port != 20001 {
		t.Fatalf("second service on the shared port = %s:%d", address, port)
	}
	if address, port := inboundListen(t, plan.configuration, "alone"); address != "0.0.0.0" || port != 8443 {
		t.Fatalf("service alone on its port was moved: %s:%d", address, port)
	}
	if address, port := inboundListen(t, plan.configuration, "udp"); address != "0.0.0.0" || port != 443 {
		t.Fatalf("UDP service was pulled behind the TCP router: %s:%d", address, port)
	}
	if len(plan.routes) != 2 {
		t.Fatalf("routes = %#v", plan.routes)
	}
	if !plan.directPorts[8443] || plan.directPorts[443] {
		t.Fatalf("direct ports = %#v", plan.directPorts)
	}
}

const soleOn443 = `{"inbounds": [{"type": "vless", "tag": "sole", "listen": "0.0.0.0", "listen_port": 443,
	"tls": {"enabled": true}}]}`

var soleNames = map[string]string{"sole": "ws.example.com"}

// A port another process already holds — an Nginx installed before polaris is
// the common case — is exactly the situation the control plane cannot see. The
// lone service on it has to be routed, or sing-box could never bind it.
func TestPlanRoutesASoleServiceOffAnOccupiedPort(t *testing.T) {
	plan, err := planListenerPlacement(soleOn443, soleNames, map[uint16]bool{443: true}, testAllocator(20000))
	if err != nil {
		t.Fatal(err)
	}
	if address, port := inboundListen(t, plan.configuration, "sole"); address != "127.0.0.1" || port != 20000 {
		t.Fatalf("service on an occupied port = %s:%d", address, port)
	}
	if len(plan.routes) != 1 || plan.routes[0].Port != 443 || plan.routes[0].SNI != "ws.example.com" || plan.routes[0].BackendPort != 20000 {
		t.Fatalf("routes = %#v", plan.routes)
	}
	if plan.directPorts[443] {
		t.Fatal("an occupied port was still marked as directly bound")
	}
}

// The same service alone on a free port is left exactly where it is, so the
// common single-service node never grows an Nginx hop it does not need.
func TestPlanLeavesASoleServiceOnAFreePortAlone(t *testing.T) {
	plan, err := planListenerPlacement(soleOn443, soleNames, map[uint16]bool{}, testAllocator(20000))
	if err != nil {
		t.Fatal(err)
	}
	if address, port := inboundListen(t, plan.configuration, "sole"); address != "0.0.0.0" || port != 443 {
		t.Fatalf("service alone on a free port = %s:%d", address, port)
	}
	if len(plan.routes) != 0 {
		t.Fatalf("routes = %#v", plan.routes)
	}
}

// Without a name to match a ClientHello against there is nothing to route by,
// so the operator is told what to change rather than shown a service that
// never starts.
func TestPlanRefusesToRouteAServiceWithoutATLSName(t *testing.T) {
	configuration := `{"inbounds": [
		{"type": "vless", "tag": "listener-plain", "listen": "0.0.0.0", "listen_port": 443},
		{"type": "vless", "tag": "listener-tls", "listen": "0.0.0.0", "listen_port": 443, "tls": {"enabled": true}}]}`
	_, err := planListenerPlacement(configuration, map[string]string{"listener-tls": "a.example.com"}, map[uint16]bool{}, testAllocator(20000))
	if err == nil {
		t.Fatal("a service with no SNI was routed anyway")
	}
	if !strings.Contains(err.Error(), "listener-plain") || !strings.Contains(err.Error(), "443") {
		t.Fatalf("refusal does not name the service and port: %v", err)
	}
}

func TestPlanRefusesTwoServicesWithTheSameName(t *testing.T) {
	names := map[string]string{"a": "same.example.com", "b": "same.example.com"}
	_, err := planListenerPlacement(twoOn443, names, map[uint16]bool{}, testAllocator(20000))
	if err == nil || !strings.Contains(err.Error(), "same.example.com") {
		t.Fatalf("duplicate SNI on one port = %v", err)
	}
}

// Everything the control plane wrote has to survive the rewrite untouched —
// users and credentials above all.
func TestPlanPreservesTheRestOfTheConfiguration(t *testing.T) {
	configuration := `{"log": {"level": "info"},
		"inbounds": [{"type": "vless", "tag": "a", "listen": "0.0.0.0", "listen_port": 443,
		  "users": [{"name": "alice", "uuid": "0f4a1b2c-0000-0000-0000-000000000001"}],
		  "tls": {"enabled": true}}],
		"outbounds": [{"type": "direct", "tag": "direct"}],
		"experimental": {"clash_api": {"external_controller": "127.0.0.1:9090"}}}`
	plan, err := planListenerPlacement(configuration, map[string]string{"a": "a.example.com"}, map[uint16]bool{443: true}, testAllocator(20000))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"alice", "0f4a1b2c-0000-0000-0000-000000000001", "127.0.0.1:9090", "\"level\": \"info\""} {
		if !strings.Contains(plan.configuration, expected) {
			t.Fatalf("rewritten configuration lost %q:\n%s", expected, plan.configuration)
		}
	}
}

func TestOccupiedTCPPortsIgnoresSingBoxItself(t *testing.T) {
	occupied := occupiedTCPPorts([]listeningSocket{
		{network: "tcp", port: 443, process: "nginx"},
		{network: "tcp", port: 8443, process: "sing-box"},
		{network: "udp", port: 443, process: "sing-box"},
		{network: "udp", port: 5353, process: "systemd-resolved"},
	})
	if !occupied[443] || occupied[8443] || occupied[5353] {
		t.Fatalf("occupied TCP ports = %#v", occupied)
	}
}
