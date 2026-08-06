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

// A ban has to reach the kernel. Fail2Ban still defaults to iptables, which
// on an nftables-only host records the ban and blocks nothing.
func TestFail2BanJailsBanThroughNftables(t *testing.T) {
	jail, _, err := CompileFail2Ban([]Fail2BanJail{{
		Name: "ssh", FilterName: "ssh", LogPath: "/var/log/auth.log", FailRegex: "from <HOST>",
		MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 3600, Enabled: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// An unset port list means "this address may not connect at all", which
	// is the allports action; multiport would only ever cover TCP and UDP.
	if !strings.Contains(jail, "banaction = nftables-allports") {
		t.Fatalf("jail does not block the address on every port:\n%s", jail)
	}
	if strings.Contains(jail, "\nport = ") {
		t.Fatalf("an allports ban must not narrow itself with a port list:\n%s", jail)
	}
	// Fail2Ban's nftables actions cover TCP only unless told otherwise, which
	// would leave every UDP service reachable by a banned address.
	if !strings.Contains(jail, "protocol = tcp,udp") {
		t.Fatalf("jail bans TCP only, leaving UDP open:\n%s", jail)
	}
}

func TestFail2BanJailHonoursAPortList(t *testing.T) {
	jail, _, err := CompileFail2Ban([]Fail2BanJail{{
		Name: "web", FilterName: "web", LogPath: "/var/log/auth.log", FailRegex: "from <HOST>",
		MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 3600, Enabled: true, Ports: "443,8443",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jail, "port = 443,8443") {
		t.Fatalf("jail ignored its port list:\n%s", jail)
	}
	if _, _, err := CompileFail2Ban([]Fail2BanJail{{
		Name: "bad", FilterName: "bad", LogPath: "/var/log/auth.log", FailRegex: "from <HOST>",
		MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 3600, Enabled: true, Ports: "443; rm -rf /",
	}}); err == nil {
		t.Fatal("a port list that could break out of the INI value was accepted")
	}
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
