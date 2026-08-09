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

func TestAddedRulesAreValidatedBeforeReachingTheHost(t *testing.T) {
	valid := LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"}
	if err := validateManagedRule(valid); err != nil {
		t.Fatal(err)
	}
	// A rule read back off a host has to be actionable in the shape the host
	// wrote it: a REJECT verdict, and a single address printed without its
	// prefix length. Turning either away leaves a rule on screen that no button
	// can remove.
	for name, rule := range map[string]LiveFirewallRule{
		"host-written rejection": {Action: "reject", Protocol: "tcp", Port: 25},
		"single address source":  {Action: "accept", Protocol: "tcp", Port: 22, CIDR: "192.168.1.5"},
	} {
		if err := validateManagedRule(rule); err != nil {
			t.Fatalf("%s was refused: %v", name, err)
		}
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
	if !strings.Contains(configuration, "banaction = iptables-allports") || !strings.Contains(configuration, "protocol = tcp,udp") {
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

// This is the shape a host in the field actually had: a stock ruleset restored
// by iptables through its nftables backend — policy accept, closing reject —
// with this platform's own table beside it. Both are input chains, both are
// evaluated, and the reject is what a port nobody opened meets.
const sampleIptablesBackedRuleset = `table ip filter {
	chain INPUT {
		type filter hook input priority filter; policy accept;
		ct state related,established counter packets 100 bytes 6000 accept # handle 1
		iifname "lo" counter packets 0 bytes 0 accept # handle 2
		meta l4proto tcp tcp dport 22 counter packets 3 bytes 180 accept # handle 3
		counter packets 5 bytes 300 reject with icmp type admin-prohibited # handle 4
	}
}
table inet polaris {
	chain input {
		type filter hook input priority filter; policy accept;
		tcp dport 19994 accept # handle 7
	}
}
`

// A chain that ends by refusing whatever is left refuses it whatever the chain
// declares as its policy. Reading the policy alone reported such a host as
// letting everything in, which is the opposite of what it does — and it is the
// answer the console puts in front of the operator.
func TestAClosingRefusalIsTheHostsRealDefault(t *testing.T) {
	if policy := defaultIncomingFromRuleset(sampleIptablesBackedRuleset); policy != "drop" {
		t.Fatalf("default incoming = %q, want drop", policy)
	}
	// Without such a rule an accepting policy means what it says. The forward
	// chain in this ruleset drops by default and decides nothing about incoming
	// traffic, so it must not count.
	if policy := defaultIncomingFromRuleset(sampleRuleset); policy != "accept" {
		t.Fatalf("default incoming = %q, want accept", policy)
	}
	if policy := defaultIncomingFromRuleset("table inet f {\n\tchain in {\n\t\ttype filter hook input priority filter; policy drop;\n\t}\n}\n"); policy != "drop" {
		t.Fatalf("a declared drop policy was not reported: %q", policy)
	}
	// A host with no input chain at all has no policy to report, and the caller
	// is what decides that an unfirewalled host lets everything in.
	if policy := defaultIncomingFromRuleset(""); policy != "" {
		t.Fatalf("an empty ruleset reported %q", policy)
	}
}

func TestOnlyARuleMatchingEverythingCountsAsADefault(t *testing.T) {
	for line, want := range map[string]string{
		"counter packets 5 bytes 300 reject with icmp type admin-prohibited # handle 4": "reject",
		"drop":                             "drop",
		"counter packets 1 bytes 2 accept": "accept",
		// Every one of these narrows what it applies to, so none of them decides
		// what happens to traffic in general.
		"iif \"lo\" accept":                       "",
		"ct state established,related accept":     "",
		"tcp dport 443 drop # handle 9":           "",
		"ip saddr 198.51.100.4/32 drop":           "",
		"meta l4proto tcp ip saddr @blocked drop": "",
	} {
		if got := unconditionalNftVerdict(line); got != want {
			t.Fatalf("unconditionalNftVerdict(%q) = %q, want %q", line, got, want)
		}
	}
	// An operator's comment is their own text and must not be read as part of
	// the rule, even when it contains words that look like one.
	if verdict := unconditionalNftVerdict(`counter packets 2 bytes 3 drop comment "tcp dport 22"`); verdict != "drop" {
		t.Fatalf("a commented closing rule was misread: %q", verdict)
	}
}

// The same question on a host with no nft command, read out of iptables' own
// wording.
func TestIptablesDefaultIncomingReadsPolicyAndClosingRule(t *testing.T) {
	closing := `-P INPUT ACCEPT
-P FORWARD DROP
-A INPUT -i lo -j ACCEPT
-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT
-A INPUT -j REJECT --reject-with icmp-host-prohibited
`
	if policy := defaultIncomingFromIptables(closing); policy != "drop" {
		t.Fatalf("default incoming = %q, want drop", policy)
	}
	// The FORWARD policy above decides nothing about incoming traffic; without
	// the closing rule this host really does accept by default.
	open := "-P INPUT ACCEPT\n-P FORWARD DROP\n-A INPUT -p tcp -m tcp --dport 22 -j ACCEPT\n"
	if policy := defaultIncomingFromIptables(open); policy != "accept" {
		t.Fatalf("default incoming = %q, want accept", policy)
	}
	if policy := defaultIncomingFromIptables("-P INPUT DROP\n"); policy != "drop" {
		t.Fatalf("a dropping policy was not reported: %q", policy)
	}
}

// A rule is saved by whatever owns it, and the tables iptables maintains
// through its nftables backend are read back alongside this platform's own.
func TestIptablesOwnedTablesAreToldApart(t *testing.T) {
	for _, owned := range [][2]string{{"ip", "filter"}, {"ip6", "filter"}, {"ip", "nat"}, {"ip", "mangle"}} {
		if !iptablesOwnsTable(owned[0], owned[1]) {
			t.Fatalf("table %s %s was not recognized as iptables'", owned[0], owned[1])
		}
	}
	// An inet table is one nft itself holds, whatever it is called — including a
	// hand-written `inet filter`, which is not iptables' filter table.
	for _, other := range [][2]string{{"inet", "polaris"}, {"inet", "filter"}, {"ip", "polaris"}} {
		if iptablesOwnsTable(other[0], other[1]) {
			t.Fatalf("table %s %s was mistaken for iptables'", other[0], other[1])
		}
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
