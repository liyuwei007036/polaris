package agent

import (
	"strings"
	"testing"
)

// A host running ufw keeps its own rule store; the console has to read the
// ports out of ufw's own listing — the refused ones as well as the allowed
// ones, and including the ones limited to a source.
func TestUFWStatusReportsDefaultPolicyAndEveryPortVerdict(t *testing.T) {
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
	if len(ports) != 5 {
		t.Fatalf("port rules = %#v, want the five rules without the v6 duplicates", ports)
	}
	if ports[0].Port != 22 || ports[0].Protocol != "tcp" || ports[0].Action != "accept" || len(ports[0].Sources) != 0 {
		t.Fatalf("first port rule = %#v", ports[0])
	}
	if ports[2].Port != 8443 || len(ports[2].Sources) != 1 || ports[2].Sources[0] != "203.0.113.0/24" {
		t.Fatalf("source-limited port = %#v", ports[2])
	}
	if ports[3].Port != 9000 || ports[3].PortEnd != 9010 || ports[3].Protocol != "udp" {
		t.Fatalf("port range = %#v", ports[3])
	}
	// A denied port is half of what the operator came to see: without it the
	// page reads as though the port were simply never mentioned.
	if ports[4].Port != 25 || ports[4].Action != "drop" {
		t.Fatalf("denied port = %#v, want tcp/25 reported as refused", ports[4])
	}
}

// An inactive ufw enforces nothing, so its listing decides nothing.
func TestUFWStatusOfAnInactiveFirewallReportsNothing(t *testing.T) {
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
	ports := parseFirewalldZone(listing, func(service string) []PortRule {
		switch service {
		case "ssh":
			return []PortRule{{Protocol: "tcp", Port: 22}}
		case "https":
			return []PortRule{{Protocol: "tcp", Port: 443}}
		}
		return nil
	})
	found := map[uint16]PortRule{}
	for _, port := range ports {
		found[port.Port] = port
	}
	for _, port := range []uint16{22, 443, 8443, 9000, 9443} {
		if _, ok := found[port]; !ok {
			t.Fatalf("port rules = %#v, want %d listed", ports, port)
		}
	}
	if found[443].Action != "accept" || found[8443].Action != "accept" || found[9443].Action != "accept" {
		t.Fatalf("an opened port was not reported as such: %#v", ports)
	}
	// A rich rule that drops traffic is listed too, as a refusal — hiding it
	// leaves the port looking unmentioned rather than closed.
	if found[25].Action != "drop" {
		t.Fatalf("a dropping rich rule was not reported as a refusal: %#v", ports)
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
	ports := parseFirewalldZone("public\n  services: custom-app\n", func(string) []PortRule { return nil })
	if len(ports) != 1 || ports[0].Service != "custom-app" || ports[0].Action != "accept" || ports[0].Port != 0 {
		t.Fatalf("unresolved service = %#v", ports)
	}
}

// Without ufw or firewalld the ports come from the kernel rules themselves.
func TestPortRulesDerivedFromRawRules(t *testing.T) {
	policy, ports := portRulesFromRules([]LiveFirewallRule{
		{Action: "accept", Protocol: "tcp", Port: 443, Family: "inet", Table: "polaris", Chain: "input", Handle: "7", Raw: "tcp dport 443 accept"},
		{Action: "accept", Protocol: "tcp", Port: 8443, CIDR: "203.0.113.0/24"},
		{Action: "drop", Protocol: "tcp", Port: 25},
		{Action: "accept", Raw: "ct state established,related accept"},
		// This is what Fail2Ban writes, once per banned address. It says nothing
		// about which ports the server offers and must not reach this list.
		{Action: "drop", CIDR: "198.51.100.4/32", Raw: "ip saddr 198.51.100.4 drop"},
	}, "drop")
	if policy != "drop" {
		t.Fatalf("policy = %q", policy)
	}
	if len(ports) != 3 {
		t.Fatalf("derived port rules = %#v, want only the three rules naming a port", ports)
	}
	// The place in the ruleset and the host's own wording both have to survive:
	// nft removes a rule by handle, iptables by the words it was written in, and
	// this list is the only place either is offered from.
	if ports[0].Port != 443 || ports[0].Handle != "7" || ports[0].Table != "polaris" || ports[0].Raw == "" {
		t.Fatalf("a derived rule lost its place in the ruleset: %#v", ports[0])
	}
	if ports[1].Port != 8443 || ports[1].Sources[0] != "203.0.113.0/24" {
		t.Fatalf("a source-limited rule was misread: %#v", ports[1])
	}
	if ports[2].Port != 25 || ports[2].Action != "drop" {
		t.Fatalf("a refused port was not carried through: %#v", ports[2])
	}
}

func TestPortSpecParsing(t *testing.T) {
	for _, testCase := range []struct {
		spec  string
		ports []PortRule
	}{
		{"8443/tcp", []PortRule{{Protocol: "tcp", Port: 8443}}},
		{"80,443/tcp", []PortRule{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 443}}},
		{"6000:6007/tcp", []PortRule{{Protocol: "tcp", Port: 6000, PortEnd: 6007}}},
		{"9000-9010/udp", []PortRule{{Protocol: "udp", Port: 9000, PortEnd: 9010}}},
		{"443", []PortRule{{Port: 443}}},
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
	// A rule ufw itself wrote as REJECT has to go back to it as reject. Told
	// "deny", ufw looks for a rule it does not have and removes nothing, so the
	// console would report a removal that never happened.
	rejected, err := ufwRuleArguments(LiveFirewallRule{Action: "reject", Protocol: "tcp", Port: 25})
	if err != nil {
		t.Fatal(err)
	}
	if rejected[0] != "reject" {
		t.Fatalf("rejecting ufw arguments = %v", rejected)
	}
	// ufw prints a single address without a prefix length, and that is the
	// wording a rule read back off it has to be removed in.
	host, err := ufwRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 22, CIDR: "192.168.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if host[4] != "192.168.1.5" {
		t.Fatalf("single-address ufw arguments = %v", host)
	}
	if _, err := ufwRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp"}); err == nil {
		t.Fatal("a rule without a port was accepted")
	}
}

// A rich rule is removed by naming it exactly as firewalld prints it, and the
// rule being removed was read back off that very listing — so what this writes
// and what the listing parser reads have to be the same rule.
func TestFirewalldRichRulesSurviveTheRoundTrip(t *testing.T) {
	for _, rule := range []LiveFirewallRule{
		{Action: "accept", Protocol: "tcp", Port: 9443, CIDR: "203.0.113.0/24"},
		{Action: "drop", Protocol: "tcp", Port: 25},
		{Action: "reject", Protocol: "udp", Port: 53, CIDR: "2001:db8::/32"},
	} {
		written := firewalldRichRule(rule)
		read, ok := parseFirewalldRichRule(written)
		if !ok {
			t.Fatalf("firewalld would not read back its own rule: %q", written)
		}
		source := ""
		if len(read.Sources) > 0 {
			source = read.Sources[0]
		}
		if read.Action != rule.Action || read.Protocol != rule.Protocol || read.Port != rule.Port || source != rule.CIDR {
			t.Fatalf("%q read back as %#v", written, read)
		}
	}
	// A refusal is a rich rule and nothing else. firewalld's zone port list only
	// says what gets in, so putting a refusal there would open the very port the
	// operator asked to close.
	if !strings.HasSuffix(firewalldRichRule(LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 25}), " drop") {
		t.Fatal("a refusal was not stated as a refusal")
	}
	// Without a source there is no family to state, and the rule covers IPv4 and
	// IPv6 both — which is what a verdict aimed at everybody means.
	if strings.Contains(firewalldRichRule(LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 25}), "family=") {
		t.Fatal("a rule aimed at everybody was pinned to one address family")
	}
}
