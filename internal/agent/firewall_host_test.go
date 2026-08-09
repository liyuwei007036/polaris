//go:build hostfirewall

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// These tests change the firewall of the machine they run on, so they are built
// only with the `hostfirewall` tag and are meant to be run as root on a
// throwaway container or virtual machine. Everything else about this package can
// be tested from parsed text; whether a takeover actually leaves a host
// protected cannot, and that is what these are for.
//
//	GOOS=linux go test -c -tags hostfirewall ./internal/agent
//	sudo ./agent.test -test.v

func requireHostFirewall(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("changing the host firewall needs root")
	}
	if !commandExists("iptables") {
		t.Skip("host has no iptables")
	}
}

// hostState is the INPUT chain of both address families as a test found it.
// Both are needed: a rule naming no source is written to each family, so a
// snapshot covering only IPv4 restores half the machine and leaves the rest to
// pile up across runs.
type hostState struct {
	v4, v6 []string
}

// snapshotFirewall reads the chains rather than using iptables-save, which
// refuses to export a filter table holding rules only nft can express — the
// very situation these tests have to survive, and one where a save-based
// snapshot silently restores nothing.
func snapshotFirewall(t *testing.T) hostState {
	t.Helper()
	return hostState{v4: inputChainRules(t, "iptables"), v6: inputChainRules(t, "ip6tables")}
}

func restoreFirewall(t *testing.T, saved hostState) {
	t.Helper()
	restoreInputChain(t, "iptables", saved.v4)
	restoreInputChain(t, "ip6tables", saved.v6)
	// The agent writes these while persisting; a test must not leave them behind.
	_ = os.Remove("/etc/iptables/rules.v4")
	_ = os.Remove("/etc/iptables/rules.v6")
	// A test that restored badly must not be allowed to poison the next one.
	for _, family := range []struct {
		command string
		want    []string
	}{{"iptables", saved.v4}, {"ip6tables", saved.v6}} {
		if got := inputChainRules(t, family.command); strings.Join(got, "\n") != strings.Join(family.want, "\n") {
			t.Fatalf("%s INPUT was not restored:\ngot:\n%s\nwant:\n%s",
				family.command, strings.Join(got, "\n"), strings.Join(family.want, "\n"))
		}
	}
}

func inputChainRules(t *testing.T, command string) []string {
	t.Helper()
	if !commandExists(command) {
		return nil
	}
	output, err := exec.Command(command, "-S", "INPUT").Output()
	if err != nil {
		t.Fatalf("%s -S INPUT: %v", command, err)
	}
	rules := []string{}
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "-A INPUT ") {
			rules = append(rules, strings.TrimPrefix(line, "-A INPUT "))
		}
	}
	return rules
}

func restoreInputChain(t *testing.T, command string, saved []string) {
	t.Helper()
	if !commandExists(command) {
		return
	}
	if output, err := exec.Command(command, "-F", "INPUT").CombinedOutput(); err != nil {
		t.Fatalf("clearing %s INPUT: %v: %s", command, err, output)
	}
	if output, err := exec.Command(command, "-P", "INPUT", "ACCEPT").CombinedOutput(); err != nil {
		t.Fatalf("resetting %s INPUT policy: %v: %s", command, err, output)
	}
	for _, rule := range saved {
		arguments := append([]string{"-A", "INPUT"}, strings.Fields(rule)...)
		if output, err := exec.Command(command, arguments...).CombinedOutput(); err != nil {
			t.Fatalf("restoring %q into %s: %v: %s", rule, command, err, output)
		}
	}
}

// clearInput empties both families so a test starts from a wide-open host.
func clearInput(t *testing.T) {
	t.Helper()
	for _, command := range []string{"iptables", "ip6tables"} {
		if !commandExists(command) {
			continue
		}
		if output, err := exec.Command(command, "-F", "INPUT").CombinedOutput(); err != nil {
			t.Fatalf("clearing %s INPUT: %v: %s", command, err, output)
		}
	}
}

// removeUFWBackups clears the copies ufw makes of its own rule files. It stamps
// them to the second and refuses to overwrite one, so two resets inside the same
// second collide — which is exactly what a run of these tests does.
func removeUFWBackups() {
	matches, _ := filepath.Glob("/etc/ufw/*.rules.2*")
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

// resetUFW empties ufw's own rule store, leaving nothing of this test behind.
func resetUFW(t *testing.T) {
	t.Helper()
	removeUFWBackups()
	runHostCommand(t, "ufw", "--force", "reset")
	removeUFWBackups()
}

// releaseUFW puts ufw back to stopped and empty however a test ended.
func releaseUFW() {
	_, _ = exec.Command("ufw", "--force", "disable").CombinedOutput()
	removeUFWBackups()
	_, _ = exec.Command("ufw", "--force", "reset").CombinedOutput()
	removeUFWBackups()
}

func runHostCommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	if output, err := exec.Command(name, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(arguments, " "), err, output)
	}
}

func hostInputChain(t *testing.T) string {
	t.Helper()
	return chainListing(t, "iptables")
}

func chainListing(t *testing.T, command string) string {
	t.Helper()
	output, err := exec.Command(command, "-S", "INPUT").Output()
	if err != nil {
		t.Fatalf("%s -S INPUT: %v", command, err)
	}
	return string(output)
}

// persistenceRefused reports whether an error is this host telling us it cannot
// export its own filter table. The rule still reached the running kernel, which
// is what the tests around this are checking.
func persistenceRefused(err error) bool {
	return err != nil && strings.Contains(err.Error(), "无法导出 filter 表")
}

func countRules(listing string, fragments ...string) int {
	count := 0
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "-A INPUT ") {
			continue
		}
		if containsAll(line, fragments) {
			count++
		}
	}
	return count
}

// ruleIndex reports the one-based position of the first rule containing every
// fragment, and zero when there is none.
func ruleIndex(listing string, fragments ...string) int {
	position := 0
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "-A INPUT ") {
			continue
		}
		position++
		if containsAll(line, fragments) {
			return position
		}
	}
	return 0
}

func containsAll(line string, fragments []string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(line, fragment) {
			return false
		}
	}
	return true
}

// addRule runs one mutation the way the console would, tolerating only the one
// failure this host is entitled to: being unable to save its own ruleset.
func addRule(t *testing.T, rule LiveFirewallRule) {
	t.Helper()
	if err := applyIptablesMutation(context.Background(), FirewallMutation{Operation: "add", Rule: rule}); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
}

// A source-limited allowance on an otherwise open chain has to arrive with the
// refusal that makes it a whitelist, and the refusal has to sit behind it.
func TestHostWhitelistLandsWithItsClosingRefusal(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)

	rule := LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24"}
	err := applyIptablesMutation(context.Background(), FirewallMutation{Operation: "add", Rule: rule})
	if err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
	listing := hostInputChain(t)
	allowance := ruleIndex(listing, "192.0.2.0/24", "19994", "ACCEPT")
	refusal := ruleIndex(listing, "19994", "DROP")
	if allowance == 0 || refusal == 0 {
		t.Fatalf("whitelist did not reach the kernel:\n%s", listing)
	}
	if refusal != allowance+1 {
		t.Fatalf("the refusal is at %d and the allowance at %d:\n%s", refusal, allowance, listing)
	}
	// An IPv4 source is an IPv4 rule and has no business in the IPv6 chain.
	if ip6 := chainListing(t, "ip6tables"); countRules(ip6, "19994") != 0 {
		t.Fatalf("an IPv4 rule reached the IPv6 chain:\n%s", ip6)
	}
	// A host that cannot export its own filter table has to say so rather than
	// write an empty ruleset over a working one — checked by there being no file
	// at all rather than a truncated one.
	saved4, readErr := os.ReadFile("/etc/iptables/rules.v4")
	switch {
	case persistenceRefused(err):
		if readErr == nil {
			t.Fatalf("saving was refused yet a ruleset was written:\n%s", saved4)
		}
		t.Logf("this host cannot export its filter table; persistence correctly refused")
	case readErr != nil:
		t.Fatalf("the change was not persisted: %v", readErr)
	case !strings.Contains(string(saved4), "19994"):
		t.Fatalf("the persisted ruleset does not carry the new rule:\n%s", saved4)
	}
}

// An allowance open to everybody is not a whitelist: it must get no closing
// refusal, and it must reach both address families.
func TestHostOpenAllowanceCoversBothFamiliesAndClosesNothing(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)

	addRule(t, LiveFirewallRule{Action: "accept", Protocol: "udp", Port: 19995})
	for _, command := range []string{"iptables", "ip6tables"} {
		listing := chainListing(t, command)
		if countRules(listing, "19995", "ACCEPT") != 1 {
			t.Fatalf("%s did not get the allowance:\n%s", command, listing)
		}
		// Writing a refusal beside it would seal the port off the moment the
		// allowance was removed.
		if countRules(listing, "19995", "DROP") != 0 {
			t.Fatalf("%s got a needless refusal:\n%s", command, listing)
		}
		if countRules(listing, "udp") != 1 {
			t.Fatalf("%s did not carry the protocol through:\n%s", command, listing)
		}
	}
}

// A refusal goes to the head of the chain, ahead of an allowance already there,
// or it decides nothing at all.
func TestHostRefusalOutranksAnExistingAllowance(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)
	runHostCommand(t, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", "19996", "-j", "ACCEPT")

	addRule(t, LiveFirewallRule{Action: "drop", Protocol: "tcp", Port: 19996, CIDR: "198.51.100.7/32"})
	listing := hostInputChain(t)
	refusal := ruleIndex(listing, "198.51.100.7/32", "19996", "DROP")
	allowance := ruleIndex(listing, "19996", "ACCEPT")
	if refusal == 0 || allowance == 0 {
		t.Fatalf("chain is not what it should be:\n%s", listing)
	}
	if refusal > allowance {
		t.Fatalf("the refusal at %d never runs, the allowance at %d decides first:\n%s", refusal, allowance, listing)
	}
}

// A rule naming an IPv6 source belongs only to the IPv6 chain.
func TestHostIPv6SourceStaysOutOfTheIPv4Chain(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("ip6tables") {
		t.Skip("host has no ip6tables")
	}
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)

	addRule(t, LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19997, CIDR: "2001:db8::/32"})
	if ip6 := chainListing(t, "ip6tables"); ruleIndex(ip6, "2001:db8::/32", "19997", "ACCEPT") == 0 {
		t.Fatalf("the IPv6 rule did not reach the IPv6 chain:\n%s", ip6)
	}
	if ip4 := hostInputChain(t); countRules(ip4, "19997") != 0 {
		t.Fatalf("an IPv6 rule reached the IPv4 chain:\n%s", ip4)
	}
}

// On a chain that ends in a refusal, an allowance has to land ahead of that
// refusal and behind the address bans — putting it at the head would open the
// newly allowed port to exactly the addresses somebody had blocked.
func TestHostAllowanceDoesNotJumpAheadOfAddressBans(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)
	for _, arguments := range [][]string{
		{"-A", "INPUT", "-s", "198.51.100.7/32", "-j", "DROP"},
		{"-A", "INPUT", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-A", "INPUT", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"},
	} {
		runHostCommand(t, "iptables", arguments...)
	}

	addRule(t, LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994})
	listing := hostInputChain(t)
	ban := ruleIndex(listing, "198.51.100.7/32", "DROP")
	allowance := ruleIndex(listing, "19994", "ACCEPT")
	closing := ruleIndex(listing, "-j REJECT")
	if ban == 0 || allowance == 0 || closing == 0 {
		t.Fatalf("the prepared chain is not what it should be:\n%s", listing)
	}
	if !(ban < allowance && allowance < closing) {
		t.Fatalf("ban=%d allowance=%d closing=%d:\n%s", ban, allowance, closing, listing)
	}
	// The chain already refuses whatever it has not decided on, so a
	// source-limited allowance needs no refusal of its own either.
	addRule(t, LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19998, CIDR: "192.0.2.0/24"})
	if listing := hostInputChain(t); countRules(listing, "19998", "DROP") != 0 {
		t.Fatalf("a needless refusal was written into a chain that already closes:\n%s", listing)
	}
	// And the console has to report that this host refuses by default.
	live := CollectLiveFirewall(context.Background())
	if live.DefaultIncoming != "drop" {
		t.Fatalf("default incoming reported as %q on a chain that ends in REJECT", live.DefaultIncoming)
	}
	if live.Manager != managerIptables {
		t.Fatalf("manager reported as %q", live.Manager)
	}
}

// What the console says about a host with nothing in its way, and about one
// whose policy alone refuses.
func TestHostDefaultIncomingIsReadFromTheRunningChain(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)

	if live := CollectLiveFirewall(context.Background()); live.DefaultIncoming != "accept" {
		t.Fatalf("an empty accepting chain was reported as %q", live.DefaultIncoming)
	}
	runHostCommand(t, "iptables", "-P", "INPUT", "DROP")
	if live := CollectLiveFirewall(context.Background()); live.DefaultIncoming != "drop" {
		t.Fatalf("a chain whose policy is DROP was reported as %q", live.DefaultIncoming)
	}
	runHostCommand(t, "iptables", "-P", "INPUT", "ACCEPT")
}

// Any rule on the machine can be withdrawn, whoever wrote it and whichever
// table it sits in — by handle where the console was given one, and in the
// host's own wording where it was not.
func TestHostRemovesRulesInEveryTableAndReportsAMissingOne(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("nft") {
		t.Skip("host has no nft")
	}
	saved := snapshotFirewall(t)
	defer func() {
		_, _ = exec.Command("nft", "delete", "table", "inet", "polaristest").CombinedOutput()
		restoreFirewall(t, saved)
	}()
	clearInput(t)

	// A rule in somebody else's table, of the shape Fail2Ban and hand-written
	// rulesets leave behind.
	for _, arguments := range [][]string{
		{"add", "table", "inet", "polaristest"},
		{"add", "chain", "inet", "polaristest", "input", "{ type filter hook input priority filter; policy accept; }"},
		{"add", "rule", "inet", "polaristest", "input", "tcp", "dport", "19999", "accept"},
	} {
		runHostCommand(t, "nft", arguments...)
	}
	live := CollectLiveFirewall(context.Background())
	var foreign *PortRule
	for index := range live.PortRules {
		if live.PortRules[index].Port == 19999 && live.PortRules[index].Table == "polaristest" {
			foreign = &live.PortRules[index]
		}
	}
	if foreign == nil {
		t.Fatalf("a rule in another table was not reported at all: %#v", live.PortRules)
	}
	if foreign.Handle == "" {
		t.Fatalf("a rule that can only be deleted by handle was reported without one: %#v", foreign)
	}
	if _, err := ApplyFirewallMutation(context.Background(), FirewallMutation{
		Operation: "delete",
		Rule: LiveFirewallRule{
			Action: foreign.Action, Protocol: foreign.Protocol, Port: foreign.Port,
			Family: foreign.Family, Table: foreign.Table, Chain: foreign.Chain,
			Handle: foreign.Handle, Raw: foreign.Raw,
		},
	}); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
	if output, _ := exec.Command("nft", "list", "table", "inet", "polaristest").CombinedOutput(); strings.Contains(string(output), "19999") {
		t.Fatalf("the rule in another table survived deletion:\n%s", output)
	}

	// A rule written by iptables and withdrawn in iptables' own wording.
	addRule(t, LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24"})
	raw := ""
	for _, line := range strings.Split(hostInputChain(t), "\n") {
		if strings.Contains(line, "192.0.2.0/24") && strings.Contains(line, "19994") {
			raw = strings.TrimSpace(line)
		}
	}
	if raw == "" {
		t.Fatal("the rule just written is not in the chain")
	}
	if _, err := ApplyFirewallMutation(context.Background(), FirewallMutation{
		Operation: "delete",
		Rule: LiveFirewallRule{
			Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24", Raw: raw,
		},
	}); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
	if listing := hostInputChain(t); countRules(listing, "192.0.2.0/24", "19994") != 0 {
		t.Fatalf("the rule was not withdrawn:\n%s", listing)
	}

	// Withdrawing something that is not there has to be reported, not reported
	// as done.
	if _, err := ApplyFirewallMutation(context.Background(), FirewallMutation{
		Operation: "delete",
		Rule: LiveFirewallRule{
			Action: "accept", Protocol: "tcp", Port: 12345, CIDR: "203.0.113.9/32",
			Raw: "-A INPUT -s 203.0.113.9/32 -p tcp -m tcp --dport 12345 -j ACCEPT",
		},
	}); err == nil {
		t.Fatal("withdrawing a rule that does not exist was reported as done")
	}
}

// The takeover this agent performs on a host running ufw: everything ufw was
// enforcing has to still be enforced afterwards, by iptables, with ufw stopped.
func TestHostTakeoverFromUFW(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("ufw") {
		t.Skip("host has no ufw")
	}
	saved := snapshotFirewall(t)
	defer func() {
		releaseUFW()
		restoreFirewall(t, saved)
	}()

	resetUFW(t)
	// `--force` belongs to reset and enable; ufw rejects it on a rule command.
	for _, arguments := range [][]string{
		{"default", "deny", "incoming"},
		{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "8443"},
		{"allow", "proto", "tcp", "from", "203.0.113.0/24", "to", "any", "port", "9443"},
		{"allow", "9000:9002/udp"},
		{"deny", "proto", "tcp", "from", "any", "to", "any", "port", "25"},
		{"--force", "enable"},
	} {
		runHostCommand(t, "ufw", arguments...)
	}
	if detected := detectFirewallManager(context.Background()); detected != managerUFW {
		t.Fatalf("a running ufw was detected as %q", detected)
	}

	if err := consolidateHostFirewall(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}

	status, _ := exec.Command("ufw", "status").CombinedOutput()
	if !strings.Contains(strings.ToLower(string(status)), "inactive") {
		t.Fatalf("ufw is still running after the takeover: %s", status)
	}
	if detected := detectFirewallManager(context.Background()); detected != managerIptables {
		t.Fatalf("after the takeover the manager is %q", detected)
	}
	listing := hostInputChain(t)
	closing := ruleIndex(listing, "-j REJECT")
	if closing == 0 {
		t.Fatalf("the takeover left the host open:\n%s", listing)
	}
	for _, carried := range [][]string{
		{"8443", "ACCEPT"},
		{"203.0.113.0/24", "9443", "ACCEPT"},
		{"25", "DROP"},
	} {
		position := ruleIndex(listing, carried...)
		if position == 0 {
			t.Fatalf("%v was lost in the takeover:\n%s", carried, listing)
		}
		if position > closing {
			t.Fatalf("%v landed behind the closing refusal:\n%s", carried, listing)
		}
	}
	// A port range ufw stated in one rule becomes one rule per port here.
	for _, port := range []string{"9000", "9001", "9002"} {
		if ruleIndex(listing, port, "ACCEPT") == 0 {
			t.Fatalf("port %s of the range was lost:\n%s", port, listing)
		}
	}
	// The conntrack allowance is what keeps established connections — including
	// the operator's own session — alive across the takeover.
	conntrack := ruleIndex(listing, "ESTABLISHED", "ACCEPT")
	if conntrack == 0 || conntrack > closing {
		t.Fatalf("established connections were not carried across:\n%s", listing)
	}
	if live := CollectLiveFirewall(context.Background()); live.DefaultIncoming != "drop" {
		t.Fatalf("after taking over a deny-by-default ufw the host reports %q", live.DefaultIncoming)
	}
}

// A ufw that was letting everything in must not come out of the takeover
// refusing by default: that would close ports the operator had open.
func TestHostTakeoverKeepsAnAllowingDefault(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("ufw") {
		t.Skip("host has no ufw")
	}
	saved := snapshotFirewall(t)
	defer func() {
		releaseUFW()
		restoreFirewall(t, saved)
	}()

	resetUFW(t)
	for _, arguments := range [][]string{
		{"default", "allow", "incoming"},
		{"deny", "proto", "tcp", "from", "any", "to", "any", "port", "25"},
		{"--force", "enable"},
	} {
		runHostCommand(t, "ufw", arguments...)
	}
	if err := consolidateHostFirewall(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
	listing := hostInputChain(t)
	if ruleIndex(listing, "-j REJECT") != 0 {
		t.Fatalf("an allowing default came out of the takeover refusing:\n%s", listing)
	}
	if ruleIndex(listing, "25", "DROP") == 0 {
		t.Fatalf("the one refusal ufw held was lost:\n%s", listing)
	}
	if live := CollectLiveFirewall(context.Background()); live.DefaultIncoming != "accept" {
		t.Fatalf("after taking over an allow-by-default ufw the host reports %q", live.DefaultIncoming)
	}
}

// A takeover that cannot carry every verdict across has to stop before the old
// firewall is touched, leaving the host exactly as protected as it was.
func TestHostTakeoverAbortsWithTheOldFirewallStillRunning(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("ufw") {
		t.Skip("host has no ufw")
	}
	saved := snapshotFirewall(t)
	defer func() {
		releaseUFW()
		restoreFirewall(t, saved)
	}()

	resetUFW(t)
	for _, arguments := range [][]string{
		{"default", "deny", "incoming"},
		{"allow", "8443/tcp"},
		// A range far too wide to state one port at a time.
		{"allow", "1:65535/tcp"},
		{"--force", "enable"},
	} {
		runHostCommand(t, "ufw", arguments...)
	}

	err := consolidateHostFirewall(context.Background())
	if err == nil {
		t.Fatal("a takeover that could not carry a rule across reported success")
	}
	if persistenceRefused(err) {
		t.Fatalf("the takeover failed for the wrong reason: %v", err)
	}
	// ufw has to still be running and still be enforcing what it was.
	status, _ := exec.Command("ufw", "status").CombinedOutput()
	if !strings.Contains(strings.ToLower(string(status)), "active") {
		t.Fatalf("the host was left unprotected by a failed takeover: %s", status)
	}
	if detected := detectFirewallManager(context.Background()); detected != managerUFW {
		t.Fatalf("after a failed takeover the manager is %q", detected)
	}
	t.Logf("takeover correctly refused: %v", err)
}

// A table left behind by an older version of this agent has to be emptied into
// iptables and removed, or it keeps competing on the input hook.
func TestHostRetiresTheOldManagedTable(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("nft") {
		t.Skip("host has no nft")
	}
	saved := snapshotFirewall(t)
	defer func() {
		_, _ = exec.Command("nft", "delete", "table", "inet", "polaris").CombinedOutput()
		restoreFirewall(t, saved)
	}()
	clearInput(t)
	for _, arguments := range [][]string{
		{"add", "table", "inet", "polaris"},
		{"add", "chain", "inet", "polaris", "input", "{ type filter hook input priority filter; policy accept; }"},
		{"add", "rule", "inet", "polaris", "input", "ct", "state", "established,related", "accept"},
		{"add", "rule", "inet", "polaris", "input", "ip", "saddr", "192.0.2.0/24", "tcp", "dport", "19994", "accept"},
		{"add", "rule", "inet", "polaris", "input", "tcp", "dport", "19994", "drop"},
	} {
		runHostCommand(t, "nft", arguments...)
	}

	if err := retireManagedTable(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}

	if output, err := exec.Command("nft", "list", "table", "inet", "polaris").CombinedOutput(); err == nil {
		t.Fatalf("the retired table is still in the ruleset:\n%s", output)
	}
	listing := hostInputChain(t)
	allowance := ruleIndex(listing, "192.0.2.0/24", "19994", "ACCEPT")
	refusal := ruleIndex(listing, "19994", "DROP")
	if allowance == 0 || refusal == 0 {
		t.Fatalf("the retired table's rules did not reach iptables:\n%s", listing)
	}
	// Their order is what made them mean anything: the allowance first, then the
	// refusal that narrows it to that source.
	if allowance > refusal {
		t.Fatalf("allowance at %d, refusal at %d:\n%s", allowance, refusal, listing)
	}
	// The conntrack rule was the old table's own scaffolding and does not belong
	// in a chain that already has its own.
	if countRules(listing, "ESTABLISHED") > 1 {
		t.Fatalf("the old table's scaffolding was copied across:\n%s", listing)
	}
	if _, err := os.Stat("/etc/polaris/nftables.conf"); !os.IsNotExist(err) {
		t.Fatal("the retired table's configuration file is still on disk")
	}
}

// Retiring on a host that never had the table must do nothing and say nothing.
func TestHostRetiringWithoutTheOldTableIsHarmless(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	before := hostInputChain(t)
	if err := retireManagedTable(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}
	if after := hostInputChain(t); after != before {
		t.Fatalf("retiring a table that was never there changed the chain:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// hostCanExportFilterTable reports whether this machine's own tooling can save
// its ruleset. A filter table holding rules only nft can express cannot be
// exported at all, and the tests that check a successful save have nothing to
// check on such a host.
func hostCanExportFilterTable() bool {
	output, err := exec.Command("iptables-save").Output()
	return err == nil && strings.Contains(string(output), "*filter")
}

// The saved ruleset has to match the running one, or the next reboot undoes
// every change the console made.
func TestHostPersistsWhatItJustChanged(t *testing.T) {
	requireHostFirewall(t)
	if !hostCanExportFilterTable() {
		t.Skip("this host cannot export its filter table")
	}
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)
	_ = os.Remove("/etc/iptables/rules.v4")
	_ = os.Remove("/etc/iptables/rules.v6")

	// No tolerance here: on a host that can save, a save that fails is a bug.
	if err := applyIptablesMutation(context.Background(), FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24"},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("/etc/iptables/rules.v4")
	if err != nil {
		t.Fatalf("the change was not persisted: %v", err)
	}
	for _, expected := range []string{"*filter", "19994", "192.0.2.0/24", "COMMIT"} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("the persisted ruleset is missing %q:\n%s", expected, contents)
		}
	}
	// A rule naming no source is written to both families, so both saved copies
	// have to carry it.
	if err := applyIptablesMutation(context.Background(), FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19995},
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/etc/iptables/rules.v4", "/etc/iptables/rules.v6"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s was not written: %v", path, err)
		}
		if !strings.Contains(string(contents), "19995") {
			t.Fatalf("%s does not carry the rule:\n%s", path, contents)
		}
	}
	// And a withdrawal has to reach the saved copy too, or the reboot brings the
	// rule back — which is the bug that started all of this.
	raw := ""
	for _, line := range strings.Split(hostInputChain(t), "\n") {
		if strings.Contains(line, "192.0.2.0/24") && strings.Contains(line, "19994") {
			raw = strings.TrimSpace(line)
		}
	}
	if _, err := ApplyFirewallMutation(context.Background(), FirewallMutation{
		Operation: "delete",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24", Raw: raw},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile("/etc/iptables/rules.v4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "192.0.2.0/24") {
		t.Fatalf("the withdrawal never reached the saved ruleset:\n%s", contents)
	}
}

// The takeover on a host running firewalld: its zone ports, the services it
// names and its rich rules all have to survive as iptables rules, with
// firewalld stopped and kept from coming back at boot.
func TestHostTakeoverFromFirewalld(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("firewall-cmd") {
		t.Skip("host has no firewalld")
	}
	if output, err := exec.Command("firewall-cmd", "--state").CombinedOutput(); err != nil || !strings.Contains(string(output), "running") {
		t.Skip("firewalld is not running on this host")
	}
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)

	for _, arguments := range [][]string{
		{"--permanent", "--add-port=8443/tcp"},
		{"--permanent", "--add-port=9000-9002/udp"},
		{"--permanent", `--add-rich-rule=rule family="ipv4" source address="203.0.113.0/24" port port="9443" protocol="tcp" accept`},
		{"--permanent", `--add-rich-rule=rule family="ipv4" source address="198.51.100.7/32" port port="25" protocol="tcp" drop`},
		{"--reload"},
	} {
		runHostCommand(t, "firewall-cmd", arguments...)
	}
	if detected := detectFirewallManager(context.Background()); detected != managerFirewalld {
		t.Fatalf("a running firewalld was detected as %q", detected)
	}

	if err := consolidateHostFirewall(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}

	if output, _ := exec.Command("firewall-cmd", "--state").CombinedOutput(); strings.Contains(string(output), "running") {
		t.Fatalf("firewalld is still running after the takeover: %s", output)
	}
	// Stopped is not enough: it must not come back at the next boot and start
	// competing again.
	if output, _ := exec.Command("systemctl", "is-enabled", "firewalld").CombinedOutput(); strings.TrimSpace(string(output)) == "enabled" {
		t.Fatal("firewalld was left enabled and would return at boot")
	}
	if detected := detectFirewallManager(context.Background()); detected != managerIptables {
		t.Fatalf("after the takeover the manager is %q", detected)
	}
	listing := hostInputChain(t)
	closing := ruleIndex(listing, "-j REJECT")
	if closing == 0 {
		t.Fatalf("firewalld refuses by default and the takeover left the host open:\n%s", listing)
	}
	for _, carried := range [][]string{
		{"8443", "ACCEPT"},
		{"203.0.113.0/24", "9443", "ACCEPT"},
		{"198.51.100.7/32", "25", "DROP"},
		// A zone's port range becomes one rule per port.
		{"9000", "ACCEPT"},
		{"9002", "ACCEPT"},
	} {
		position := ruleIndex(listing, carried...)
		if position == 0 {
			t.Fatalf("%v was lost in the takeover:\n%s", carried, listing)
		}
		if position > closing {
			t.Fatalf("%v landed behind the closing refusal:\n%s", carried, listing)
		}
	}
	// firewalld's default zone names ssh as a service, and losing it is how an
	// operator locks themselves out.
	if ruleIndex(listing, "22", "ACCEPT") == 0 {
		t.Logf("no ssh allowance was carried across; the zone may not have named one:\n%s", listing)
	}
	if conntrack := ruleIndex(listing, "ESTABLISHED", "ACCEPT"); conntrack == 0 || conntrack > closing {
		t.Fatalf("established connections were not carried across:\n%s", listing)
	}
	if live := CollectLiveFirewall(context.Background()); live.DefaultIncoming != "drop" {
		t.Fatalf("after taking over firewalld the host reports %q", live.DefaultIncoming)
	}
}

// Where netfilter-persistent is installed it owns the saved ruleset, and this
// agent hands the job to it rather than writing those files itself — otherwise
// the two would take turns overwriting each other with different opinions of
// what the layout should be.
func TestHostPersistsThroughNetfilterPersistent(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("netfilter-persistent") {
		t.Skip("host has no netfilter-persistent")
	}
	if !hostCanExportFilterTable() {
		t.Skip("this host cannot export its filter table")
	}
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)
	_ = os.Remove("/etc/iptables/rules.v4")

	if err := applyIptablesMutation(context.Background(), FirewallMutation{
		Operation: "add",
		Rule:      LiveFirewallRule{Action: "accept", Protocol: "tcp", Port: 19994, CIDR: "192.0.2.0/24"},
	}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("/etc/iptables/rules.v4")
	if err != nil {
		t.Fatalf("netfilter-persistent did not save the ruleset: %v", err)
	}
	if !strings.Contains(string(contents), "19994") {
		t.Fatalf("the saved ruleset does not carry the new rule:\n%s", contents)
	}
}

// writeStub puts an executable script on a directory that will be put ahead of
// PATH, so a command this agent runs can be stood in for.
func writeStub(t *testing.T, directory, name, script string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The firewalld takeover, with the daemon's own two commands stood in for.
//
// firewalld cannot start on every kernel — it wants bridge netfilter, which
// WSL2 does not carry — so where the real daemon will not run this covers
// everything except the daemon itself: the zone is read and translated for
// real, the stop and disable are observed, and the rules that come out of it
// are written into the real kernel and checked there.
func TestHostTakeoverFromFirewalldWithStubbedDaemon(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)

	directory := t.TempDir()
	journal := filepath.Join(directory, "systemctl.log")
	writeStub(t, directory, "firewall-cmd", `
case "$1" in
  --state) echo running; exit 0;;
  --list-all)
    cat <<'EOF'
public (active)
  target: default
  services: ssh
  ports: 8443/tcp 9000-9002/udp
  protocols:
  rich rules:
	rule family="ipv4" source address="203.0.113.0/24" port port="9443" protocol="tcp" accept
	rule family="ipv4" source address="198.51.100.7/32" port port="25" protocol="tcp" drop
EOF
    exit 0;;
  --info-service=ssh) echo "  ports: 22/tcp"; exit 0;;
esac
exit 0
`)
	writeStub(t, directory, "systemctl", `echo "$@" >> `+journal+`
exit 0`)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	if detected := detectFirewallManager(context.Background()); detected != managerFirewalld {
		t.Fatalf("a running firewalld was detected as %q", detected)
	}
	if err := consolidateHostFirewall(context.Background()); err != nil && !persistenceRefused(err) {
		t.Fatal(err)
	}

	// The daemon has to be stopped and kept from returning at the next boot.
	recorded, err := os.ReadFile(journal)
	if err != nil {
		t.Fatalf("systemctl was never called: %v", err)
	}
	for _, expected := range []string{"stop firewalld", "disable firewalld"} {
		if !strings.Contains(string(recorded), expected) {
			t.Fatalf("systemctl %q was not run; log:\n%s", expected, recorded)
		}
	}

	// Everything the zone was enforcing has to be in the real chain now.
	listing := hostInputChain(t)
	closing := ruleIndex(listing, "-j REJECT")
	if closing == 0 {
		t.Fatalf("firewalld refuses by default and the takeover left the host open:\n%s", listing)
	}
	for _, carried := range [][]string{
		{"8443", "ACCEPT"},                     // a zone port
		{"22", "ACCEPT"},                       // a port behind a named service
		{"9000", "ACCEPT"}, {"9002", "ACCEPT"}, // a port range, one rule per port
		{"203.0.113.0/24", "9443", "ACCEPT"}, // a rich rule that opens
		{"198.51.100.7/32", "25", "DROP"},    // a rich rule that refuses
	} {
		position := ruleIndex(listing, carried...)
		if position == 0 {
			t.Fatalf("%v was lost in the takeover:\n%s", carried, listing)
		}
		if position > closing {
			t.Fatalf("%v landed behind the closing refusal:\n%s", carried, listing)
		}
	}
	if conntrack := ruleIndex(listing, "ESTABLISHED", "ACCEPT"); conntrack == 0 || conntrack > closing {
		t.Fatalf("established connections were not carried across:\n%s", listing)
	}
}

// What the access-limit page ends up showing, checked against a chain shaped
// like a real server's: ports opened, addresses banned, Fail2Ban's jumps, the
// usual conntrack and loopback allowances, and a closing rejection.
//
// The page answers one question — which ports this server opens and which it
// refuses — so a rule that names no port answers nothing and must stay out,
// however many of them the host has.
func TestHostAccessLimitListShowsPortsAndOnlyPorts(t *testing.T) {
	requireHostFirewall(t)
	if !commandExists("nft") {
		t.Skip("host has no nft")
	}
	saved := snapshotFirewall(t)
	defer func() {
		_, _ = exec.Command("nft", "delete", "table", "inet", "f2b-table").CombinedOutput()
		// INPUT has to lose its jump before the chain it points at can go.
		restoreFirewall(t, saved)
		_, _ = exec.Command("iptables", "-F", "f2b-sshd-stub").CombinedOutput()
		_, _ = exec.Command("iptables", "-X", "f2b-sshd-stub").CombinedOutput()
	}()
	clearInput(t)
	// Left over from an earlier run that ended badly; a chain cannot be created
	// twice.
	_, _ = exec.Command("iptables", "-F", "f2b-sshd-stub").CombinedOutput()
	_, _ = exec.Command("iptables", "-X", "f2b-sshd-stub").CombinedOutput()
	runHostCommand(t, "iptables", "-N", "f2b-sshd-stub")

	for _, arguments := range [][]string{
		{"-A", "INPUT", "-p", "tcp", "--dport", "8443", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "tcp", "--dport", "443", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "udp", "--dport", "443", "-j", "ACCEPT"},
		// Address bans: what Fail2Ban and hand-written scripts leave by the
		// hundred. They say nothing about which ports are offered.
		{"-A", "INPUT", "-s", "49.51.180.2/32", "-j", "DROP"},
		{"-A", "INPUT", "-s", "43.131.39.179/32", "-j", "DROP"},
		// A jump decides nothing by itself.
		{"-A", "INPUT", "-p", "tcp", "-m", "multiport", "--dports", "22", "-j", "f2b-sshd-stub"},
		{"-A", "INPUT", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "icmp", "-j", "ACCEPT"},
		{"-A", "INPUT", "-i", "lo", "-j", "ACCEPT"},
		{"-A", "INPUT", "-p", "tcp", "-s", "203.0.113.0/24", "--dport", "9443", "-j", "ACCEPT"},
		{"-A", "INPUT", "-j", "REJECT", "--reject-with", "icmp-host-prohibited"},
	} {
		runHostCommand(t, "iptables", arguments...)
	}
	// Fail2Ban's own nftables table, which this agent reads back alongside
	// everything else. Its bans name addresses, not ports.
	for _, arguments := range [][]string{
		{"add", "table", "inet", "f2b-table"},
		{"add", "chain", "inet", "f2b-table", "f2b-chain", "{ type filter hook input priority filter - 1; policy accept; }"},
		{"add", "rule", "inet", "f2b-table", "f2b-chain", "ip", "saddr", "198.51.100.4", "drop"},
	} {
		runHostCommand(t, "nft", arguments...)
	}
	// A jail given a port list bans through multiport instead, and that ban does
	// name a port. This is the one address ban the list cannot tell apart from
	// an access limit an operator wrote by hand.
	runHostCommand(t, "iptables", "-A", "f2b-sshd-stub", "-s", "203.0.113.99/32", "-p", "tcp", "--dport", "22", "-j", "REJECT")

	live := CollectLiveFirewall(context.Background())
	rendered, _ := json.MarshalIndent(live.PortRules, "", "  ")
	t.Logf("default_incoming=%q manager=%q truncated=%v\nport_rules=%s",
		live.DefaultIncoming, live.Manager, live.Truncated, rendered)

	if live.DefaultIncoming != "drop" {
		t.Fatalf("the page would say this server admits everything: default_incoming=%q", live.DefaultIncoming)
	}
	// Four openings and one source-limited opening; nothing else names a port.
	type shown struct {
		action, protocol string
		port             uint16
		source           string
	}
	got := map[shown]int{}
	for _, rule := range live.PortRules {
		source := ""
		if len(rule.Sources) > 0 {
			source = rule.Sources[0]
		}
		got[shown{rule.Action, rule.Protocol, rule.Port, source}]++
		// Every row the page offers a delete button for has to be removable, and
		// every row has to carry enough to name it back to the server.
		if rule.Port != 0 && rule.PortEnd == 0 && rule.Protocol != "" && rule.Service == "" {
			if rule.Raw == "" && rule.Handle == "" {
				t.Fatalf("a removable row cannot be named back to the server: %#v", rule)
			}
		}
	}
	for _, want := range []shown{
		{"accept", "tcp", 8443, ""},
		{"accept", "tcp", 443, ""},
		{"accept", "udp", 443, ""},
		{"accept", "tcp", 9443, "203.0.113.0/24"},
		// A ban that names a port is, from the kernel's side, indistinguishable
		// from an access limit somebody wrote by hand: same shape, same fields.
		// So it shows up here too, saying what the automatic banning tab already
		// says. Only a jail given a port list produces one — the default jails
		// ban on every port and stay out of this list. Recorded rather than
		// filtered: telling the two apart means going by chain name, which is a
		// product decision and not a parsing one.
		{"reject", "tcp", 22, "203.0.113.99/32"},
	} {
		if got[want] != 1 {
			t.Fatalf("the page shows %d rows for %+v, want exactly 1:\n%s", got[want], want, rendered)
		}
	}
	if len(live.PortRules) != 5 {
		t.Fatalf("the page shows %d rows, want only the ones that name a port:\n%s", len(live.PortRules), rendered)
	}
	// Named specifically: an address ban must never reach this list, whichever
	// table it lives in.
	for _, rule := range live.PortRules {
		for _, banned := range []string{"49.51.180.2", "43.131.39.179", "198.51.100.4"} {
			if strings.Contains(strings.Join(rule.Sources, " "), banned) || strings.Contains(rule.Raw, banned) {
				t.Fatalf("an address ban reached the access-limit list: %#v", rule)
			}
		}
	}
}

// A sanity check on the numbers the other tests lean on: a chain read back has
// to be the chain that was written.
func TestHostChainPositionsAreReadBackAsWritten(t *testing.T) {
	requireHostFirewall(t)
	saved := snapshotFirewall(t)
	defer restoreFirewall(t, saved)
	clearInput(t)
	for port := 1; port <= 3; port++ {
		runHostCommand(t, "iptables", "-A", "INPUT", "-p", "tcp", "--dport", strconv.Itoa(9000+port), "-j", "ACCEPT")
	}
	runHostCommand(t, "iptables", "-A", "INPUT", "-j", "REJECT", "--reject-with", "icmp-host-prohibited")
	listing := hostInputChain(t)
	if position := iptablesClosingRulePosition(listing); position != 4 {
		t.Fatalf("closing rule position = %d:\n%s", position, listing)
	}
}
