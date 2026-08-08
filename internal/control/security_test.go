package control

import (
	"encoding/json"
	"strings"
	"testing"
)

// newSecurityServer builds a master with nothing in it: these tests exercise
// how a node's answer is turned into what the console shows, which is now the
// only path network protection data travels.
func newSecurityServer(t *testing.T) *Server {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestFirewallAnswersCarryOpenAndBlockedPortsWithTheirSourceLocation(t *testing.T) {
	server := newSecurityServer(t)
	answer, err := json.Marshal(map[string]any{
		"available": true, "tool": "nftables",
		"port_rules": []map[string]any{
			{"action": "accept", "protocol": "tcp", "port": 443, "sources": []string{"8.8.8.8/32"}, "family": "inet", "table": "polaris", "chain": "input", "handle": "7"},
			{"action": "drop", "protocol": "tcp", "port": 25, "family": "inet", "table": "polaris", "chain": "input", "handle": "9"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	node := server.decodeNodeFirewall("node-1", liveAnswer{data: string(answer)})
	// A refused port belongs on this list as much as an open one: the operator
	// is asking what the firewall does with each port, not only what it lets in.
	if !node.Available || len(node.PortRules) != 2 {
		t.Fatalf("unexpected answer: %#v", node)
	}
	if node.PortRules[0].NodeID != "node-1" || len(node.PortRules[0].Locations) != 1 ||
		!strings.Contains(node.PortRules[0].Locations[0], "United States") {
		t.Fatalf("a port rule lost its source location: %#v", node.PortRules[0])
	}
	// The place in the ruleset has to survive the trip, because on a server with
	// no firewall manager it is the only way to name the rule when deleting it.
	if node.PortRules[1].Action != "drop" || node.PortRules[1].Handle != "9" {
		t.Fatalf("a refused port was misreported: %#v", node.PortRules[1])
	}
}

func TestUnreadableFirewallAnswersAreReportedNotIgnored(t *testing.T) {
	server := newSecurityServer(t)
	node := server.decodeNodeFirewall("node-1", liveAnswer{err: ErrNodeOffline})
	if node.Available || node.Error == "" {
		t.Fatalf("an offline server was not reported as such: %#v", node)
	}
	if len(node.PortRules) != 0 {
		t.Fatalf("an offline server must not report ports: %#v", node.PortRules)
	}
	// Nonsense from a node is a failure to read the firewall, not an empty
	// firewall — the difference decides whether an operator thinks a port is
	// open or closed.
	garbled := server.decodeNodeFirewall("node-1", liveAnswer{data: "{not json"})
	if garbled.Available || garbled.Error == "" {
		t.Fatalf("an unreadable answer was treated as an empty firewall: %#v", garbled)
	}
	// An agent too old to report port rules says nothing about them at all. That
	// is not a server with an empty firewall, and reporting it as one would tell
	// an operator every port is closed on a machine where none of them are.
	old := server.decodeNodeFirewall("node-1", liveAnswer{data: `{"available":true,"tool":"nftables","rules":[]}`})
	if old.Available || old.Error == "" || len(old.PortRules) != 0 {
		t.Fatalf("an outdated agent was treated as a firewall with no rules: %#v", old)
	}
}

func TestBannedAddressesResolveBackToTheRuleThatBannedThem(t *testing.T) {
	server := newSecurityServer(t)
	answer, err := json.Marshal(map[string]any{
		"available": true,
		"jails": []map[string]any{
			{
				"name": "ssh-bruteforce", "managed": true, "running": true, "currently_banned": "1",
				"banned": []map[string]string{{"ip": "8.8.8.8", "banned_at": "2026-08-06T02:00:00Z", "unban_at": "2026-08-06T03:00:00Z"}},
			},
			{"name": "operator-own-jail", "managed": false, "running": true, "banned": []map[string]string{{"ip": "198.51.100.9"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	node, banned := server.decodeNodeFail2Ban("node-1", liveAnswer{data: string(answer)})
	if len(node.Jails) != 2 || !node.Jails[0].Running {
		t.Fatalf("unexpected jails: %#v", node.Jails)
	}
	if len(banned) != 2 {
		t.Fatalf("expected both managed and unmanaged bans: %#v", banned)
	}
	// Releasing an address needs the name Fail2Ban knows, which for a managed
	// rule carries the platform's prefix — the console shows it without.
	if banned[0].Jail != "polaris-ssh-bruteforce" || banned[0].RuleName != "ssh-bruteforce" || !banned[0].Managed {
		t.Fatalf("a managed ban was not resolved back to its rule: %#v", banned[0])
	}
	if banned[0].BannedAt != "2026-08-06T02:00:00Z" || banned[0].UnbanAt != "2026-08-06T03:00:00Z" {
		t.Fatalf("ban times were not carried through: %#v", banned[0])
	}
	if !strings.Contains(banned[0].Location, "United States") {
		t.Fatalf("a banned address lost its location: %#v", banned[0])
	}
	if banned[1].Jail != "operator-own-jail" || banned[1].Managed {
		t.Fatalf("an unmanaged ban claimed a platform rule: %#v", banned[1])
	}
}
