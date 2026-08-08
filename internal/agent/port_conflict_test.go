package agent

import (
	"strings"
	"testing"
)

const sampleListeningSockets = `Netid State  Recv-Q Send-Q Local Address:Port Peer Address:PortProcess
tcp   LISTEN 0      511          0.0.0.0:443        0.0.0.0:*    users:(("nginx",pid=1234,fd=6))
tcp   LISTEN 0      511             [::]:80             [::]:*    users:(("nginx",pid=1234,fd=7))
tcp   LISTEN 0      4096       127.0.0.1:20000      0.0.0.0:*    users:(("sing-box",pid=999,fd=8))
udp   UNCONN 0      0            0.0.0.0:8443       0.0.0.0:*    users:(("sing-box",pid=999,fd=9))
tcp   LISTEN 0      128          0.0.0.0:19670      0.0.0.0:*
tcp   LISTEN 0      128         10.0.0.9:8080       0.0.0.0:*    users:(("caddy",pid=555,fd=3))
`

func TestParseListeningSocketsReadsAddressPortAndOwner(t *testing.T) {
	sockets := parseListeningSockets(sampleListeningSockets)
	if len(sockets) != 6 {
		t.Fatalf("parsed sockets = %#v", sockets)
	}
	if sockets[0] != (listeningSocket{network: "tcp", address: "0.0.0.0", port: 443, process: "nginx"}) {
		t.Fatalf("first socket = %#v", sockets[0])
	}
	if sockets[1] != (listeningSocket{network: "tcp", address: "[::]", port: 80, process: "nginx"}) {
		t.Fatalf("IPv6 socket = %#v", sockets[1])
	}
	if sockets[3] != (listeningSocket{network: "udp", address: "0.0.0.0", port: 8443, process: "sing-box"}) {
		t.Fatalf("UDP socket = %#v", sockets[3])
	}
	// A row without the process column still proves the port is taken.
	if sockets[4].process != "其他程序" {
		t.Fatalf("socket without an owner = %#v", sockets[4])
	}
}

// The node that prompted this check ran an Nginx installed before polaris, so
// sing-box could never bind the 443 the control plane believed was free.
func TestPortConflictReportsForeignProcessOnInboundPort(t *testing.T) {
	configuration := `{"inbounds":[{"type":"vless","tag":"listener-a","listen":"0.0.0.0","listen_port":443}]}`
	conflicts := singBoxBindingConflicts(configuration, parseListeningSockets(sampleListeningSockets))
	if len(conflicts) != 1 || !strings.Contains(conflicts[0], "TCP/443") || !strings.Contains(conflicts[0], "nginx") {
		t.Fatalf("conflicts = %#v", conflicts)
	}
}

func TestPortConflictIgnoresSingBoxItselfAndDistinctSockets(t *testing.T) {
	sockets := parseListeningSockets(sampleListeningSockets)
	for name, configuration := range map[string]string{
		// sing-box holds these ports for the configuration being replaced.
		"internal port held by sing-box": `{"inbounds":[{"type":"vless","listen":"127.0.0.1","listen_port":20000}]}`,
		"UDP port held by sing-box":      `{"inbounds":[{"type":"hysteria2","listen":"0.0.0.0","listen_port":8443}]}`,
		// Hysteria2 is UDP, so Nginx on TCP/443 is not in its way.
		"UDP inbound against a TCP socket": `{"inbounds":[{"type":"hysteria2","listen":"0.0.0.0","listen_port":443}]}`,
		// Two concrete addresses can share a port.
		"distinct concrete address": `{"inbounds":[{"type":"vless","listen":"10.0.0.5","listen_port":8080}]}`,
	} {
		if conflicts := singBoxBindingConflicts(configuration, sockets); conflicts != nil {
			t.Fatalf("%s reported %#v", name, conflicts)
		}
	}
}

func TestPortConflictTreatsWildcardAsCoveringEveryAddress(t *testing.T) {
	sockets := parseListeningSockets(sampleListeningSockets)
	// Nginx listens on 0.0.0.0:443, so even a loopback-only inbound collides.
	configuration := `{"inbounds":[{"type":"vless","listen":"127.0.0.1","listen_port":443}]}`
	if conflicts := singBoxBindingConflicts(configuration, sockets); len(conflicts) != 1 {
		t.Fatalf("conflicts = %#v", conflicts)
	}
}
