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

// Taking over another firewall means carrying every verdict it held across.
// What cannot be carried across has to stop the takeover rather than go
// missing: this runs before the old firewall is stopped, so failing here leaves
// the host exactly as protected as it was.
func TestTakeoverCarriesEveryVerdictAcrossOrRefusesToStart(t *testing.T) {
	rules, err := translatePortRules([]PortRule{
		{Action: "accept", Protocol: "tcp", Port: 8443},
		// A range becomes one rule per port, because a rule here names one port.
		{Action: "accept", Protocol: "udp", Port: 9000, PortEnd: 9002},
		// No protocol means both, which is how ufw and firewalld mean it.
		{Action: "accept", Port: 443},
		// Two sources are two rules; a refusal has to survive as a refusal.
		{Action: "drop", Protocol: "tcp", Port: 25, Sources: []string{"203.0.113.0/24", "198.51.100.7/32"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1+3+2+2 {
		t.Fatalf("translated rules = %#v", rules)
	}
	if rules[0].Port != 8443 || rules[0].Action != "accept" || rules[0].Protocol != "tcp" {
		t.Fatalf("a plain allowance was mistranslated: %#v", rules[0])
	}
	if rules[1].Port != 9000 || rules[3].Port != 9002 || rules[3].Protocol != "udp" {
		t.Fatalf("a port range was not expanded: %#v", rules[1:4])
	}
	if rules[4].Protocol != "tcp" || rules[5].Protocol != "udp" || rules[4].Port != 443 {
		t.Fatalf("a rule naming no protocol did not cover both: %#v", rules[4:6])
	}
	if rules[6].Action != "drop" || rules[6].CIDR != "203.0.113.0/24" || rules[7].CIDR != "198.51.100.7/32" {
		t.Fatalf("a source-limited refusal was mistranslated: %#v", rules[6:8])
	}
	// A service whose ports the old firewall could not resolve, and a range too
	// wide to state one port at a time. Both have to stop the takeover.
	for name, port := range map[string]PortRule{
		"unresolved service": {Action: "accept", Service: "custom-app"},
		"very wide range":    {Action: "accept", Protocol: "tcp", Port: 1, PortEnd: 65535},
	} {
		if _, err := translatePortRules([]PortRule{port}); err == nil {
			t.Fatalf("%s was silently dropped instead of stopping the takeover", name)
		}
	}
	// A range ending at the last port must terminate rather than wrap around.
	wide, err := translatePortRules([]PortRule{{Action: "accept", Protocol: "tcp", Port: 65534, PortEnd: 65535}})
	if err != nil || len(wide) != 2 || wide[1].Port != 65535 {
		t.Fatalf("a range reaching the last port = %#v (err=%v)", wide, err)
	}
}

// A rule limited to one address family belongs only in that family's table;
// one naming no source belongs in both.
func TestAdoptedRulesGoToTheRightAddressFamily(t *testing.T) {
	for _, testCase := range []struct {
		rule    LiveFirewallRule
		command string
		belongs bool
	}{
		{LiveFirewallRule{}, "iptables", true},
		{LiveFirewallRule{}, "ip6tables", true},
		{LiveFirewallRule{CIDR: "203.0.113.0/24"}, "iptables", true},
		{LiveFirewallRule{CIDR: "203.0.113.0/24"}, "ip6tables", false},
		{LiveFirewallRule{CIDR: "2001:db8::/32"}, "ip6tables", true},
		{LiveFirewallRule{CIDR: "2001:db8::/32"}, "iptables", false},
	} {
		if got := ruleAppliesToCommand(testCase.rule, testCase.command); got != testCase.belongs {
			t.Fatalf("%q with %s = %v", testCase.rule.CIDR, testCase.command, got)
		}
	}
	// Each family refuses with its own ICMP message; the v4 name is not valid
	// for ip6tables and the rule would be rejected outright.
	if rejectMessage("iptables") == rejectMessage("ip6tables") {
		t.Fatal("both families were given the same reject message")
	}
}

// iptables takes a rule in its own wording, and the same wording has to remove
// it again.
func TestIptablesRuleArguments(t *testing.T) {
	arguments, err := iptablesRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(arguments, " ") != "-p tcp --dport 19994 -j ACCEPT" {
		t.Fatalf("iptables arguments = %v", arguments)
	}
	limited, err := iptablesRuleArguments(LiveFirewallRule{Action: "drop", Protocol: "udp", Port: 53, CIDR: "203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(limited, " ") != "-p udp -s 203.0.113.0/24 --dport 53 -j DROP" {
		t.Fatalf("source-limited iptables arguments = %v", limited)
	}
	rejected, err := iptablesRuleArguments(LiveFirewallRule{Action: "reject", Protocol: "tcp", Port: 25})
	if err != nil {
		t.Fatal(err)
	}
	if rejected[len(rejected)-1] != "REJECT" {
		t.Fatalf("rejecting iptables arguments = %v", rejected)
	}
	// A host prints a single address without a prefix length; iptables needs it.
	host, err := iptablesRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 22, CIDR: "192.168.1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(host, " ") != "-p tcp -s 192.168.1.5/32 --dport 22 -j ACCEPT" {
		t.Fatalf("single-address iptables arguments = %v", host)
	}
	if _, err := iptablesRuleArguments(LiveFirewallRule{Action: "accept", Protocol: "tcp"}); err == nil {
		t.Fatal("a rule without a port was accepted")
	}
}

// A real host's INPUT chain: ports opened at the top, a pile of banned
// addresses, Fail2Ban's jumps, the usual conntrack and loopback allowances, and
// a closing rejection.
const sampleHostInputChain = `-P INPUT ACCEPT
-A INPUT -p tcp -m tcp --dport 8443 -j ACCEPT
-A INPUT -p tcp -m tcp --dport 443 -j ACCEPT
-A INPUT -p udp -m udp --dport 443 -j ACCEPT
-A INPUT -s 49.51.180.2/32 -j DROP
-A INPUT -s 43.131.39.179/32 -j DROP
-A INPUT -s 45.63.4.69/32 -j DROP
-A INPUT -p tcp -m multiport --dports 22 -j f2b-sshd
-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT
-A INPUT -p icmp -j ACCEPT
-A INPUT -i lo -j ACCEPT
-A INPUT -p tcp -m state --state NEW -m tcp --dport 22 -j ACCEPT
-A INPUT -j REJECT --reject-with icmp-host-prohibited
`

// An allowance has to go late enough to leave the address bans above it in
// force, and early enough to still be reached. Inserting it at the head instead
// would open the newly allowed port to precisely the addresses somebody banned.
func TestAnAllowanceIsPlacedAheadOfTheClosingRuleAndNothingElse(t *testing.T) {
	// Twelve rules, the last of which closes the chain.
	if position := iptablesClosingRulePosition(sampleHostInputChain); position != 12 {
		t.Fatalf("closing rule position = %d, want 12", position)
	}
	// A chain nothing closes cannot leave an allowance unreached.
	if position := iptablesClosingRulePosition("-P INPUT ACCEPT\n-A INPUT -i lo -j ACCEPT\n"); position != 0 {
		t.Fatalf("a chain with no closing rule reported position %d", position)
	}
	// A dropping rule that names a source closes nothing: it decides only about
	// that source, and rules below it still get their turn.
	if position := iptablesClosingRulePosition("-P INPUT ACCEPT\n-A INPUT -s 49.51.180.2/32 -j DROP\n-A INPUT -j DROP\n"); position != 2 {
		t.Fatalf("an address ban was mistaken for the rule that closes the chain: %d", position)
	}
	// An allowance lands on the closing rule's position, pushing it down; a
	// refusal goes to the head so it outranks any allowance already there.
	if position := iptablesInsertPosition(sampleHostInputChain, LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994}); position != 12 {
		t.Fatalf("an allowance was placed at %d", position)
	}
	if position := iptablesInsertPosition(sampleHostInputChain, LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 25}); position != 1 {
		t.Fatalf("a refusal was placed at %d", position)
	}
	if position := iptablesInsertPosition("-P INPUT ACCEPT\n", LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994}); position != 1 {
		t.Fatalf("an allowance in an open chain was placed at %d", position)
	}
}

// An allowance limited to a source only means "this source and nobody else"
// once everything else on that port is refused. On a chain that already refuses
// what it has not decided on, that has been said already.
func TestAWhitelistGetsTheRefusalThatMakesItOneOnlyWhenItNeedsIt(t *testing.T) {
	whitelisted := LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24"}
	// This chain ends in a rejection, so the allowance alone is already a
	// whitelist and a second refusal would be dead weight.
	if iptablesNeedsClosingDenial(sampleHostInputChain, whitelisted) {
		t.Fatal("a chain that already closes was given another closing refusal")
	}
	// A wide-open chain has nothing to make the allowance mean anything.
	if !iptablesNeedsClosingDenial("-P INPUT ACCEPT\n", whitelisted) {
		t.Fatal("a whitelist was left without the refusal that makes it one")
	}
	// The port already carries its own refusal.
	closed := "-P INPUT ACCEPT\n-A INPUT -p tcp -m tcp --dport 19994 -j DROP\n"
	if iptablesNeedsClosingDenial(closed, whitelisted) {
		t.Fatal("a second refusal was written for a port that already has one")
	}
	// A refusal on a different port says nothing about this one.
	elsewhere := "-P INPUT ACCEPT\n-A INPUT -p tcp -m tcp --dport 25 -j DROP\n"
	if !iptablesNeedsClosingDenial(elsewhere, whitelisted) {
		t.Fatal("a refusal on another port was taken to cover this one")
	}
	// An allowance open to everybody is not a whitelist and must not get one.
	if iptablesNeedsClosingDenial("-P INPUT ACCEPT\n", LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994}) {
		t.Fatal("an allowance open to every source was given a closing refusal")
	}
	if iptablesNeedsClosingDenial("-P INPUT ACCEPT\n", LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 19994}) {
		t.Fatal("a refusal was given a closing refusal of its own")
	}
}

// iptables only covers IPv4. A verdict naming no source is meant for both
// families, and leaving IPv6 out would close a port on one family while the
// operator believes it closed on both.
func TestIptablesCommandsFollowTheAddressFamily(t *testing.T) {
	if commands := iptablesCommands(LiveFirewallRule{CIDR: "2001:db8::/32"}); len(commands) != 1 || commands[0] != "ip6tables" {
		t.Fatalf("an IPv6 rule = %v, want ip6tables alone", commands)
	}
	if commands := iptablesCommands(LiveFirewallRule{CIDR: "203.0.113.0/24"}); len(commands) != 1 || commands[0] != "iptables" {
		t.Fatalf("an IPv4 rule = %v, want iptables alone", commands)
	}
	// Whether ip6tables joins it depends on the host having the command, so only
	// the IPv4 half is certain everywhere this test runs.
	commands := iptablesCommands(LiveFirewallRule{})
	if len(commands) == 0 || commands[0] != "iptables" {
		t.Fatalf("a rule for every source = %v, want iptables first", commands)
	}
}

// A rich rule states the one thing a firewalld zone's port list cannot — a
// refusal, or an opening limited to a source — so a takeover that misread one
// would carry the wrong verdict across.
func TestFirewalldRichRulesAreReadForTakeover(t *testing.T) {
	refusal, ok := parseFirewalldRichRule(`rule family="ipv4" source address="198.51.100.7/32" port port="25" protocol="tcp" drop`)
	if !ok || refusal.Action != "drop" || refusal.Port != 25 || refusal.Sources[0] != "198.51.100.7/32" {
		t.Fatalf("a dropping rich rule read back as %#v (ok=%v)", refusal, ok)
	}
	// A rejection may go on to name the message it sends back, and still decides
	// the same thing.
	rejection, ok := parseFirewalldRichRule(`rule family="ipv6" source address="2001:db8::/32" port port="53" protocol="udp" reject type="icmp-admin-prohibited"`)
	if !ok || rejection.Action != "reject" || rejection.Protocol != "udp" {
		t.Fatalf("a rejecting rich rule read back as %#v (ok=%v)", rejection, ok)
	}
	// A rule that neither opens nor refuses a port states no verdict to carry.
	if _, ok := parseFirewalldRichRule(`rule family="ipv4" source address="203.0.113.0/24" log prefix="probe"`); ok {
		t.Fatal("a logging rule was read as a verdict")
	}
}
