package control

import (
	"strings"
	"testing"
)

func compile(t *testing.T, rules ...FirewallRule) string {
	t.Helper()
	configuration, err := CompileNftables(rules)
	if err != nil {
		t.Fatal(err)
	}
	return configuration
}

func indexOf(t *testing.T, configuration, needle string) int {
	t.Helper()
	at := strings.Index(configuration, needle)
	if at < 0 {
		t.Fatalf("configuration is missing %q:\n%s", needle, configuration)
	}
	return at
}

// The chain policy is accept, so an allow rule only means something if the
// port it names is closed to everything else. Without that closing rule an
// "allow 192.168.1.0/24 on 443" changed nothing at all.
func TestAllowRuleTurnsItsPortIntoAWhitelist(t *testing.T) {
	configuration := compile(t, FirewallRule{Action: "accept", Protocol: "tcp", CIDR: "192.168.1.0/24", Port: 443, Enabled: true})
	allow := indexOf(t, configuration, "ip saddr 192.168.1.0/24 tcp dport 443 accept")
	closing := indexOf(t, configuration, "    tcp dport 443 drop")
	if closing < allow {
		t.Fatalf("the closing deny must come after the allowance:\n%s", configuration)
	}
}

// A port with only denials stays open to everyone else.
func TestDenyOnlyPortIsNotClosedToEveryoneElse(t *testing.T) {
	configuration := compile(t, FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "10.0.0.0/8", Port: 443, Enabled: true})
	indexOf(t, configuration, "ip saddr 10.0.0.0/8 tcp dport 443 drop")
	if strings.Contains(configuration, "    tcp dport 443 drop\n") {
		t.Fatalf("a blacklist-only port must not be closed to everyone:\n%s", configuration)
	}
}

// Ordering used to come from sorting on the source address, so whether a deny
// beat an allow depended on how the two CIDRs happened to compare as strings.
func TestDenyBeatsAllowRegardlessOfAddressOrdering(t *testing.T) {
	configuration := compile(t,
		FirewallRule{Action: "accept", Protocol: "tcp", CIDR: "10.0.0.0/8", Port: 443, Enabled: true},
		FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "10.1.2.0/24", Port: 443, Enabled: true},
	)
	deny := indexOf(t, configuration, "ip saddr 10.1.2.0/24 tcp dport 443 drop")
	allow := indexOf(t, configuration, "ip saddr 10.0.0.0/8 tcp dport 443 accept")
	if deny > allow {
		t.Fatalf("a deny must be evaluated before a broader allow:\n%s", configuration)
	}
}

// Rules on one port must not close a different port.
func TestPortsAreIndependent(t *testing.T) {
	configuration := compile(t,
		FirewallRule{Action: "accept", Protocol: "tcp", CIDR: "192.168.1.0/24", Port: 443, Enabled: true},
		FirewallRule{Action: "drop", Protocol: "udp", CIDR: "10.0.0.0/8", Port: 8443, Enabled: true},
	)
	indexOf(t, configuration, "    tcp dport 443 drop")
	if strings.Contains(configuration, "    udp dport 8443 drop\n  }") {
		t.Fatalf("a deny-only UDP port must stay open to others:\n%s", configuration)
	}
}

// Denying a port must not also kill the node's own outbound traffic or its
// local services, which is what happens without these two exemptions.
func TestManagedChainAlwaysExemptsEstablishedAndLoopback(t *testing.T) {
	configuration := compile(t, FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "0.0.0.0/0", Port: 443, Enabled: true})
	established := indexOf(t, configuration, "ct state established,related accept")
	loopback := indexOf(t, configuration, "iif lo accept")
	rule := indexOf(t, configuration, "tcp dport 443 drop")
	if established > rule || loopback > rule {
		t.Fatalf("exemptions must precede the managed rules:\n%s", configuration)
	}
}

func TestDisabledAndExpiredRulesAreOmitted(t *testing.T) {
	configuration := compile(t,
		FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "10.0.0.0/8", Port: 443, Enabled: false},
		FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "10.1.0.0/16", Port: 443, Enabled: true, ExpiresAt: 1},
	)
	if strings.Contains(configuration, "10.0.0.0/8") || strings.Contains(configuration, "10.1.0.0/16") {
		t.Fatalf("disabled or expired rules reached the configuration:\n%s", configuration)
	}
}

func TestCompilationIsDeterministic(t *testing.T) {
	rules := []FirewallRule{
		{Action: "accept", Protocol: "udp", CIDR: "10.0.0.0/8", Port: 8443, Enabled: true},
		{Action: "drop", Protocol: "tcp", CIDR: "192.168.0.0/16", Port: 443, Enabled: true},
		{Action: "accept", Protocol: "tcp", CIDR: "203.0.113.0/24", Port: 443, Enabled: true},
	}
	first := compile(t, rules...)
	reordered := []FirewallRule{rules[2], rules[0], rules[1]}
	if second := compile(t, reordered...); first != second {
		t.Fatalf("compilation depends on input order:\n%s\n---\n%s", first, second)
	}
}

func TestIPv6RuleUsesIP6Matcher(t *testing.T) {
	configuration := compile(t, FirewallRule{Action: "drop", Protocol: "tcp", CIDR: "2001:db8::/32", Port: 443, Enabled: true})
	indexOf(t, configuration, "ip6 saddr 2001:db8::/32 tcp dport 443 drop")
}

func TestInvalidRuleIsRejected(t *testing.T) {
	for name, rule := range map[string]FirewallRule{
		"bad action":   {Action: "reject", Protocol: "tcp", CIDR: "10.0.0.0/8", Port: 443, Enabled: true},
		"bad protocol": {Action: "drop", Protocol: "icmp", CIDR: "10.0.0.0/8", Port: 443, Enabled: true},
		"no port":      {Action: "drop", Protocol: "tcp", CIDR: "10.0.0.0/8", Enabled: true},
		"bad cidr":     {Action: "drop", Protocol: "tcp", CIDR: "not-a-cidr", Port: 443, Enabled: true},
	} {
		if _, err := CompileNftables([]FirewallRule{rule}); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
