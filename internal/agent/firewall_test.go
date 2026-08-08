package agent

import (
	"strings"
	"testing"
)

// The console shows what a server enforces, so what the platform writes has to
// survive being read back off that server unchanged. These tests take the
// round trip that the console depends on: compile rules, list them the way nft
// prints them, parse them again.

func TestCompiledRulesSurviveBeingReadBack(t *testing.T) {
	rules := []LiveFirewallRule{
		{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"},
		{Action: "drop", Protocol: "udp", Port: 53, CIDR: "203.0.113.7/32"},
		{Action: "drop", Protocol: "tcp", Port: 22},
	}
	script, err := CompileManagedNftables(rules)
	if err != nil {
		t.Fatal(err)
	}
	live := &LiveFirewall{}
	appendNftablesTableRules(live, "inet polaris", nftPrints(script), true)

	// The two base rules are in force but are not access limits, so they stay
	// out of what an operator is shown.
	for _, rule := range live.Rules {
		if strings.Contains(rule.Raw, baseRuleComment) {
			t.Fatalf("a base rule reached the console: %#v", rule)
		}
	}
	for _, want := range rules {
		if !containsRule(live.Rules, want) {
			t.Fatalf("rule %#v did not survive the round trip: %#v", want, live.Rules)
		}
	}
	// An allowance turns its port into a whitelist, and the closing denial that
	// does so is reported as the platform's own so nobody deletes half of it.
	closing := findRule(live.Rules, LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 443})
	if closing == nil {
		t.Fatalf("the whitelist's closing denial is missing: %#v", live.Rules)
	}
	if !closing.Automatic || !closing.Managed {
		t.Fatalf("the closing denial was not marked automatic: %#v", closing)
	}
	// An operator's own "deny everyone" rule is worded identically, so it must
	// not be mistaken for the generated one.
	own := findRule(live.Rules, LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 22})
	if own == nil || own.Automatic {
		t.Fatalf("an operator's denial was reported as automatic: %#v", own)
	}
}

func TestReadingBackRestoresThePrefixLengthNftDrops(t *testing.T) {
	// nft prints a single address without /32, and the console has to show the
	// operator the range they entered.
	rule, ok := parseManagedNftablesRule("ip saddr 203.0.113.7 tcp dport 443 accept")
	if !ok {
		t.Fatal("a rule this platform writes was not recognized")
	}
	if rule.CIDR != "203.0.113.7/32" {
		t.Fatalf("CIDR = %q, want 203.0.113.7/32", rule.CIDR)
	}
	if rule, ok := parseManagedNftablesRule("ip6 saddr 2001:db8::1 udp dport 443 drop"); !ok || rule.CIDR != "2001:db8::1/128" {
		t.Fatalf("IPv6 rule read back as %#v (ok=%v)", rule, ok)
	}
	// Anything the platform did not write stays raw rather than being guessed at.
	if _, ok := parseManagedNftablesRule("meta l4proto tcp counter accept"); ok {
		t.Fatal("an unrecognized rule was reported as structured")
	}
}

func TestMutationsApplyToWhatTheServerAlreadyHas(t *testing.T) {
	existing := []LiveFirewallRule{{Managed: true, Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"}}
	added, err := applyMutation(existing, FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 22, CIDR: "203.0.113.0/24"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("unexpected rules after add: %#v", added)
	}
	// Adding what the server already enforces is reported rather than silently
	// duplicated.
	if _, err := applyMutation(added, FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"},
	}); err == nil {
		t.Fatal("a duplicate rule was accepted")
	}
	removed, err := applyMutation(added, FirewallMutation{
		Operation: "delete",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Port != 22 {
		t.Fatalf("unexpected rules after delete: %#v", removed)
	}
	// Deleting a rule the server no longer has must say so rather than report
	// a change that did not happen.
	if _, err := applyMutation(removed, FirewallMutation{
		Operation: "delete",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "10.0.0.0/8"},
	}); err == nil {
		t.Fatal("deleting an absent rule was reported as done")
	}
	if _, err := applyMutation(removed, FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 443, CIDR: "not-a-range"},
	}); err == nil {
		t.Fatal("an invalid source range was accepted")
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

// nftPrints turns a loaded script into the shape `nft list table` prints it in:
// tab indentation and a quoted loopback interface.
func nftPrints(script string) string {
	listing := strings.ReplaceAll(script, "    ", "\t\t")
	return strings.ReplaceAll(listing, "iif lo accept", `iif "lo" accept`)
}

func containsRule(rules []LiveFirewallRule, want LiveFirewallRule) bool {
	return findRule(rules, want) != nil
}

func findRule(rules []LiveFirewallRule, want LiveFirewallRule) *LiveFirewallRule {
	for index, rule := range rules {
		if rule.Action == want.Action && rule.Protocol == want.Protocol && rule.Port == want.Port && rule.CIDR == want.CIDR {
			return &rules[index]
		}
	}
	return nil
}
