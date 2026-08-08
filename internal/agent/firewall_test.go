package agent

import (
	"strings"
	"testing"
)

// The console shows what a server enforces, so the parser has to cope with the
// way nft actually prints rules — counters, comments, match expressions this
// platform never wrote — rather than only the shapes it writes itself.

// A listing in the shape `nft -a list ruleset` produces on a real host: the
// platform's own table, the distribution's, one Docker leftover, and a set
// (whose members are addresses, not rules).
const sampleRuleset = `table inet polaris {
	chain input {
		type filter hook input priority filter; policy accept;
		ct state established,related accept # handle 4
		iif "lo" accept # handle 5
		ip saddr 172.64.0.0/13 tcp dport 443 counter packets 258481 bytes 35305964 accept # handle 7
		ip6 saddr 2001:db8::1 udp dport 443 accept # handle 8
		tcp dport 443 drop # handle 9
		tcp dport 22 counter packets 3 bytes 180 drop comment "keep everyone out" # handle 11
	}
}
table inet filter {
	set blocked {
		type ipv4_addr
		elements = { 203.0.113.9, 198.51.100.4 }
	}
	chain forward {
		type filter hook forward priority filter; policy drop;
		meta l4proto tcp ip saddr @blocked drop # handle 21
	}
}
table ip nat {
	chain PREROUTING {
		type nat hook prerouting priority dstnat; policy accept;
		iifname "docker0" counter packets 0 bytes 0 return # handle 33
	}
}
`

func TestEveryRuleOnTheHostIsReportedWithItsHandle(t *testing.T) {
	rules, truncated := parseNftablesRuleset(sampleRuleset)
	if truncated {
		t.Fatal("a small ruleset should not reach the cap")
	}
	// Eight rules across three tables. A set's members and the chain headers
	// match no traffic and are not rules.
	if len(rules) != 8 {
		for _, rule := range rules {
			t.Logf("%s %s %s handle=%s raw=%q", rule.Family, rule.Table, rule.Chain, rule.Handle, rule.Raw)
		}
		t.Fatalf("unexpected rule count: %d", len(rules))
	}
	for _, rule := range rules {
		if rule.Handle == "" {
			t.Fatalf("a rule without a handle cannot be deleted: %#v", rule)
		}
		if rule.Family == "" || rule.Table == "" || rule.Chain == "" {
			t.Fatalf("a rule was not attributed to its place in the ruleset: %#v", rule)
		}
		if strings.Contains(rule.Raw, "handle") {
			t.Fatalf("the handle comment leaked into the rule text: %#v", rule)
		}
	}
	// Order is what a firewall runs on: the first match wins, so the list has
	// to stay in the order the kernel evaluates it.
	if rules[0].Handle != "4" || rules[len(rules)-1].Handle != "33" {
		t.Fatalf("rules were reordered: %#v", rules)
	}
}

// This is the rule that used to be reported as unreadable. nft prints a
// counter in the middle of it, which an exact-word-order parser could not get
// past, so a perfectly ordinary CDN allowance showed up as raw text.
func TestCountersAndCommentsDoNotHideARule(t *testing.T) {
	rules, _ := parseNftablesRuleset(sampleRuleset)
	counted := findByHandle(rules, "7")
	if counted == nil {
		t.Fatal("the counted rule is missing")
	}
	if counted.Action != "accept" || counted.Protocol != "tcp" || counted.Port != 443 || counted.CIDR != "172.64.0.0/13" {
		t.Fatalf("a counted rule was not read correctly: %#v", counted)
	}
	// A comment is the operator's own text and must never be mistaken for part
	// of the rule — this one contains the word "out", next to a real verdict.
	commented := findByHandle(rules, "11")
	if commented == nil || commented.Action != "drop" || commented.Port != 22 {
		t.Fatalf("a commented rule was not read correctly: %#v", commented)
	}
	// nft prints a single address without its prefix length; the console has to
	// show the range the operator entered.
	if v6 := findByHandle(rules, "8"); v6 == nil || v6.CIDR != "2001:db8::1/128" {
		t.Fatalf("an IPv6 address lost its prefix length: %#v", v6)
	}
	// Whatever cannot be made out still gets listed, in the host's own words,
	// so nothing a server enforces is hidden from the operator.
	unparsed := findByHandle(rules, "21")
	if unparsed == nil || unparsed.Raw == "" {
		t.Fatalf("an unrecognized rule was dropped: %#v", unparsed)
	}
	if unparsed.Action != "drop" || unparsed.Port != 0 {
		t.Fatalf("an unrecognized rule was over-interpreted: %#v", unparsed)
	}
}

func TestTheReportedRuleCountIsCapped(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("table inet polaris {\n\tchain input {\n")
	for index := 0; index < maximumReportedFirewallRules+50; index++ {
		builder.WriteString("\t\ttcp dport 443 accept # handle 100\n")
	}
	builder.WriteString("\t}\n}\n")
	rules, truncated := parseNftablesRuleset(builder.String())
	if !truncated || len(rules) != maximumReportedFirewallRules {
		t.Fatalf("the cap was not applied: truncated=%v count=%d", truncated, len(rules))
	}
}

func TestIptablesRulesAreReadTheSameWay(t *testing.T) {
	rule := parseIptablesRule("-A INPUT -s 172.64.0.0/13 -p tcp -m tcp --dport 443 -j ACCEPT")
	if rule.Chain != "INPUT" || rule.CIDR != "172.64.0.0/13" || rule.Protocol != "tcp" || rule.Port != 443 || rule.Action != "accept" {
		t.Fatalf("an iptables rule was not read correctly: %#v", rule)
	}
	if rule.Raw == "" {
		t.Fatal("an iptables rule lost the host's own wording")
	}
}

func TestClosingDenialIsFoundSoAnAllowanceStaysReachable(t *testing.T) {
	rules, _ := parseNftablesRuleset(sampleRuleset)
	// The denial that refuses everything else on 443 names no source; the one
	// on 22 does the same for its port.
	if handle := closingDenial(rules, "tcp", 443); handle != "9" {
		t.Fatalf("closing denial for 443 = %q, want 9", handle)
	}
	// A port with no blanket denial has none to find, so an allowance for it
	// gets one written alongside.
	if handle := closingDenial(rules, "udp", 443); handle != "" {
		t.Fatalf("closing denial for udp/443 = %q, want none", handle)
	}
}

func TestAddedRulesAreValidatedBeforeReachingTheHost(t *testing.T) {
	valid := LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"}
	if err := validateManagedRule(valid); err != nil {
		t.Fatal(err)
	}
	for name, rule := range map[string]LiveFirewallRule{
		"unknown action":   {Action: "log", Protocol: "tcp", Port: 443},
		"unknown protocol": {Action: "accept", Protocol: "icmp", Port: 443},
		"missing port":     {Action: "accept", Protocol: "tcp"},
		"bad source":       {Action: "accept", Protocol: "tcp", Port: 443, CIDR: "not-a-range"},
	} {
		if err := validateManagedRule(rule); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	expression, err := nftablesExpression(valid)
	if err != nil {
		t.Fatal(err)
	}
	if expression != "ip saddr 10.0.0.0/8 tcp dport 443" {
		t.Fatalf("unexpected expression: %q", expression)
	}
	if expression, err := nftablesExpression(LiveFirewallRule{Action: "drop", Protocol: "udp", Port: 53, CIDR: "2001:db8::/32"}); err != nil || expression != "ip6 saddr 2001:db8::/32 udp dport 53" {
		t.Fatalf("unexpected IPv6 expression %q (err=%v)", expression, err)
	}
}

func TestManagedJailsSurviveBeingReadBack(t *testing.T) {
	jails := []LiveFail2BanJail{{
		Name: "ssh-bruteforce", FilterName: "ssh-bruteforce", LogPath: "/var/log/auth.log",
		FailRegex: "^.*sshd.*from <HOST>.*$", MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 86400,
	}}
	configuration, filters, err := CompileManagedFail2Ban(jails)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 {
		t.Fatalf("unexpected filters: %#v", filters)
	}
	if !strings.Contains(configuration, "[polaris-ssh-bruteforce]") {
		t.Fatalf("jail section is missing: %s", configuration)
	}
	// A jail without a port list must ban the address outright, on every
	// protocol, or a "blocked" address keeps reaching UDP services.
	if !strings.Contains(configuration, "banaction = nftables-allports") || !strings.Contains(configuration, "protocol = tcp,udp") {
		t.Fatalf("ban action does not cover every port and protocol: %s", configuration)
	}
	if err := ValidateManagedJail(LiveFail2BanJail{
		Name: "bad name", FilterName: "f", LogPath: "/var/log/auth.log", FailRegex: "x",
		MaxRetry: 1, FindTimeSeconds: 1, BanTimeSeconds: 1,
	}); err == nil {
		t.Fatal("an unsafe jail name was accepted")
	}
	if err := ValidateManagedJail(LiveFail2BanJail{
		Name: "ok", FilterName: "ok", LogPath: "relative/path", FailRegex: "x",
		MaxRetry: 1, FindTimeSeconds: 1, BanTimeSeconds: 1,
	}); err == nil {
		t.Fatal("a relative log path was accepted")
	}
}

func TestSavingAJailReplacesTheOneWithTheSameName(t *testing.T) {
	existing := []LiveFail2BanJail{
		{Managed: true, Name: "ssh", FilterName: "ssh", LogPath: "/var/log/auth.log", FailRegex: "a", MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 600},
		{Managed: true, Name: "proxy", FilterName: "proxy", LogPath: "/var/log/sing-box/sing-box.log", FailRegex: "b", MaxRetry: 5, FindTimeSeconds: 600, BanTimeSeconds: 600},
	}
	saved, err := applyJailMutation(existing, Fail2BanMutation{
		Operation: "save",
		Jail:      LiveFail2BanJail{Name: "ssh", FilterName: "ssh", LogPath: "/var/log/auth.log", FailRegex: "changed", MaxRetry: 9, FindTimeSeconds: 60, BanTimeSeconds: 60},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("saving an existing jail should not add one: %#v", saved)
	}
	if saved[0].FailRegex != "changed" || saved[0].MaxRetry != 9 {
		t.Fatalf("the jail was not replaced: %#v", saved[0])
	}
	deleted, err := applyJailMutation(saved, Fail2BanMutation{Operation: "delete", Jail: LiveFail2BanJail{Name: "proxy"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0].Name != "ssh" {
		t.Fatalf("unexpected jails after delete: %#v", deleted)
	}
	if _, err := applyJailMutation(deleted, Fail2BanMutation{Operation: "delete", Jail: LiveFail2BanJail{Name: "proxy"}}); err == nil {
		t.Fatal("deleting an absent jail was reported as done")
	}
}

func findByHandle(rules []LiveFirewallRule, handle string) *LiveFirewallRule {
	for index := range rules {
		if rules[index].Handle == handle {
			return &rules[index]
		}
	}
	return nil
}
