package agent

import "testing"

// A host running ufw keeps its own rule store; the console has to read the
// ports out of ufw's own listing, including the ones limited to a source.
func TestUFWStatusReportsDefaultPolicyAndOpenPorts(t *testing.T) {
	listing := `Status: active
Logging: on (low)
Default: deny (incoming), allow (outgoing), disabled (routed)
New profiles: skip

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW IN    Anywhere
443/tcp                    ALLOW IN    Anywhere
8443/tcp                   ALLOW IN    203.0.113.0/24
9000:9010/udp              ALLOW IN    Anywhere
25/tcp                     DENY IN     Anywhere
22/tcp (v6)                ALLOW IN    Anywhere (v6)
443/tcp (v6)               ALLOW IN    Anywhere (v6)
`
	policy, ports := parseUFWStatus(listing)
	if policy != "drop" {
		t.Fatalf("default incoming policy = %q, want drop", policy)
	}
	if len(ports) != 4 {
		t.Fatalf("open ports = %#v, want the four allowances without the v6 duplicates", ports)
	}
	if ports[0].Port != 22 || ports[0].Protocol != "tcp" || len(ports[0].Sources) != 0 {
		t.Fatalf("first open port = %#v", ports[0])
	}
	if ports[2].Port != 8443 || len(ports[2].Sources) != 1 || ports[2].Sources[0] != "203.0.113.0/24" {
		t.Fatalf("source-limited port = %#v", ports[2])
	}
	if ports[3].Port != 9000 || ports[3].PortEnd != 9010 || ports[3].Protocol != "udp" {
		t.Fatalf("port range = %#v", ports[3])
	}
}

// An inactive ufw enforces nothing, so its listing opens no ports.
func TestUFWStatusOfAnInactiveFirewallOpensNothing(t *testing.T) {
	policy, ports := parseUFWStatus("Status: inactive\n")
	if policy != "" || len(ports) != 0 {
		t.Fatalf("inactive ufw = %q / %#v", policy, ports)
	}
}

// firewalld states its ports three ways — plain ports, named services, and
// rich rules — and all three have to reach the console.
func TestFirewalldZoneReportsPortsServicesAndRichRules(t *testing.T) {
	listing := `public (active)
  target: default
  icmp-block-inversion: no
  interfaces: eth0
  sources:
  services: ssh https
  ports: 8443/tcp 9000-9010/udp
  protocols:
  forward: yes
  masquerade: no
  forward-ports:
  source-ports:
  icmp-blocks:
  rich rules:
	rule family="ipv4" source address="203.0.113.0/24" port port="9443" protocol="tcp" accept
	rule family="ipv4" source address="198.51.100.7/32" port port="25" protocol="tcp" drop
`
	ports := parseFirewalldZone(listing, func(service string) []OpenPort {
		switch service {
		case "ssh":
			return []OpenPort{{Protocol: "tcp", Port: 22}}
		case "https":
			return []OpenPort{{Protocol: "tcp", Port: 443}}
		}
		return nil
	})
	found := map[uint16]OpenPort{}
	for _, port := range ports {
		found[port.Port] = port
	}
	for _, port := range []uint16{22, 443, 8443, 9000, 9443} {
		if _, ok := found[port]; !ok {
			t.Fatalf("open ports = %#v, want %d listed", ports, port)
		}
	}
	// A rich rule that drops traffic is not an opening.
	if _, ok := found[25]; ok {
		t.Fatalf("a dropping rich rule was reported as open: %#v", ports)
	}
	if found[22].Service != "ssh" {
		t.Fatalf("service-provided port = %#v, want it to name ssh", found[22])
	}
	if found[9000].PortEnd != 9010 {
		t.Fatalf("port range = %#v", found[9000])
	}
	if len(found[9443].Sources) != 1 || found[9443].Sources[0] != "203.0.113.0/24" {
		t.Fatalf("rich rule source = %#v", found[9443])
	}
}

// A service firewalld cannot resolve still has to be shown by name: hiding it
// would understate what the server admits.
func TestFirewalldKeepsUnresolvedServicesByName(t *testing.T) {
	ports := parseFirewalldZone("public\n  services: custom-app\n", func(string) []OpenPort { return nil })
	if len(ports) != 1 || ports[0].Service != "custom-app" || ports[0].Port != 0 {
		t.Fatalf("unresolved service = %#v", ports)
	}
}

// Without ufw or firewalld the ports come from the kernel rules themselves.
func TestOpenPortsDerivedFromRawRules(t *testing.T) {
	policy, ports := openPortsFromRules([]LiveFirewallRule{
		{Action: "accept", Protocol: "tcp", Port: 443},
		{Action: "accept", Protocol: "tcp", Port: 8443, CIDR: "203.0.113.0/24"},
		{Action: "drop", Protocol: "tcp", Port: 25},
		{Action: "accept", Raw: "ct state established,related accept"},
	}, "drop")
	if policy != "drop" {
		t.Fatalf("policy = %q", policy)
	}
	if len(ports) != 2 || ports[0].Port != 443 || ports[1].Port != 8443 || ports[1].Sources[0] != "203.0.113.0/24" {
		t.Fatalf("derived open ports = %#v", ports)
	}
}

func TestPortSpecParsing(t *testing.T) {
	for _, testCase := range []struct {
		spec  string
		ports []OpenPort
	}{
		{"8443/tcp", []OpenPort{{Protocol: "tcp", Port: 8443}}},
		{"80,443/tcp", []OpenPort{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 443}}},
		{"6000:6007/tcp", []OpenPort{{Protocol: "tcp", Port: 6000, PortEnd: 6007}}},
		{"9000-9010/udp", []OpenPort{{Protocol: "udp", Port: 9000, PortEnd: 9010}}},
		{"443", []OpenPort{{Port: 443}}},
		{"Nginx Full", nil},
	} {
		got := parsePortSpec(testCase.spec)
		if len(got) != len(testCase.ports) {
			t.Fatalf("%q parsed to %#v, want %#v", testCase.spec, got, testCase.ports)
		}
		for index := range got {
			if got[index].Protocol != testCase.ports[index].Protocol ||
				got[index].Port != testCase.ports[index].Port ||
				got[index].PortEnd != testCase.ports[index].PortEnd {
				t.Fatalf("%q parsed to %#v, want %#v", testCase.spec, got, testCase.ports)
			}
		}
	}
}

// ufw takes a rule in its own wording; getting that wrong is how a rule ends
// up silently unapplied.
func TestUFWRuleArguments(t *testing.T) {
	arguments, err := ufwRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "8443"}
	for index := range want {
		if arguments[index] != want[index] {
			t.Fatalf("ufw arguments = %v, want %v", arguments, want)
		}
	}
	limited, err := ufwRuleArguments(LiveFirewallRule{Action: "drop", Protocol: "udp", Port: 51820, CIDR: "203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if limited[0] != "deny" || limited[4] != "203.0.113.0/24" {
		t.Fatalf("source-limited ufw arguments = %v", limited)
	}
	if _, err := ufwRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp"}); err == nil {
		t.Fatal("a rule without a port was accepted")
	}
}
