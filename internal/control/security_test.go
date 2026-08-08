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

func TestFirewallAnswersCarryTheSourceLocation(t *testing.T) {
	server := newSecurityServer(t)
	answer, err := json.Marshal(map[string]any{
		"available": true, "tool": "nftables",
		"rules": []map[string]any{
			{"managed": true, "action": "accept", "protocol": "tcp", "port": 443, "cidr": "8.8.8.8/32", "table": "inet polaris", "chain": "input"},
			{"managed": false, "table": "inet filter", "chain": "input", "raw": "ct state established accept"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	node := server.decodeNodeFirewall("node-1", liveAnswer{data: string(answer)})
	if !node.Available || len(node.Rules) != 2 {
		t.Fatalf("unexpected answer: %#v", node)
	}
	if node.Rules[0].NodeID != "node-1" || !strings.Contains(node.Rules[0].Location, "United States") {
		t.Fatalf("a managed rule lost its source location: %#v", node.Rules[0])
	}
	// A rule the platform did not write keeps the host's own wording and is
	// never presented as something the console may rewrite.
	if node.Rules[1].Managed || node.Rules[1].Raw == "" {
		t.Fatalf("an external rule was misreported: %#v", node.Rules[1])
	}
}

func TestUnreadableFirewallAnswersAreReportedNotIgnored(t *testing.T) {
	server := newSecurityServer(t)
	node := server.decodeNodeFirewall("node-1", liveAnswer{err: ErrNodeOffline})
	if node.Available || node.Error == "" {
		t.Fatalf("an offline server was not reported as such: %#v", node)
	}
	if len(node.Rules) != 0 {
		t.Fatalf("an offline server must not report rules: %#v", node.Rules)
	}
	// Nonsense from a node is a failure to read the firewall, not an empty
	// firewall — the difference decides whether an operator thinks a port is
	// open or closed.
	garbled := server.decodeNodeFirewall("node-1", liveAnswer{data: "{not json"})
	if garbled.Available || garbled.Error == "" {
		t.Fatalf("an unreadable answer was treated as an empty firewall: %#v", garbled)
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
