package control

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FirewallRule struct {
	ID        string `json:"id,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	Action    string `json:"action"`
	Protocol  string `json:"protocol"`
	CIDR      string `json:"cidr"`
	Location  string `json:"location,omitempty"`
	Port      uint16 `json:"port"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Enabled   bool   `json:"enabled"`
}

type firewallPort struct {
	protocol string
	port     uint16
}

// CompileNftables renders the managed input chain.
//
// Rules are grouped per protocol and port, and each group is emitted in a
// fixed order — denials first, then allowances, then a closing denial when
// the group has any allowance at all. That last part is what makes an
// "allow" rule mean something: the chain policy is accept, so an allow rule
// on its own matched traffic that was already going to be accepted, and a
// deny rule's effect depended on where the source address happened to sort.
// An allowance now turns its port into a whitelist; a group with only
// denials stays a blacklist.
func CompileNftables(rules []FirewallRule) (string, error) {
	groups := map[firewallPort][]FirewallRule{}
	var order []firewallPort
	now := time.Now().UTC().Unix()
	for _, rule := range rules {
		if !rule.Enabled || (rule.ExpiresAt != 0 && rule.ExpiresAt <= now) {
			continue
		}
		if err := validateFirewallRuleShape(rule); err != nil {
			return "", err
		}
		key := firewallPort{protocol: rule.Protocol, port: rule.Port}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], rule)
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].protocol != order[j].protocol {
			return order[i].protocol < order[j].protocol
		}
		return order[i].port < order[j].port
	})

	var lines []string
	// Replies to connections this host opened, and anything on loopback, are
	// never subject to the managed rules: without this a deny rule would also
	// break the node's own outbound traffic and local services.
	lines = append(lines, "    ct state established,related accept")
	lines = append(lines, "    iif lo accept")
	for _, key := range order {
		group := groups[key]
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].Action != group[j].Action {
				return group[i].Action == "drop"
			}
			return group[i].CIDR < group[j].CIDR
		})
		allows := false
		for _, rule := range group {
			expression, err := nftablesMatch(rule)
			if err != nil {
				return "", err
			}
			lines = append(lines, "    "+expression+" "+rule.Action)
			if rule.Action == "accept" {
				allows = true
			}
		}
		if allows {
			lines = append(lines, "    "+key.protocol+" dport "+strconv.Itoa(int(key.port))+" drop")
		}
	}
	return "table inet sb_control {\n  chain input {\n    type filter hook input priority filter; policy accept;\n" +
		strings.Join(lines, "\n") + "\n  }\n}\n", nil
}

func validateFirewallRuleShape(rule FirewallRule) error {
	if rule.Action != "accept" && rule.Action != "drop" {
		return errors.New("firewall action must be accept or drop")
	}
	if rule.Protocol != "tcp" && rule.Protocol != "udp" {
		return errors.New("firewall protocol must be tcp or udp")
	}
	if rule.Port == 0 {
		return errors.New("firewall port is required")
	}
	if rule.CIDR == "" {
		return nil
	}
	if _, _, err := net.ParseCIDR(rule.CIDR); err != nil {
		return fmt.Errorf("invalid firewall CIDR: %w", err)
	}
	return nil
}

func nftablesMatch(rule FirewallRule) (string, error) {
	match := rule.Protocol + " dport " + strconv.Itoa(int(rule.Port))
	if rule.CIDR == "" {
		return match, nil
	}
	ip, _, err := net.ParseCIDR(rule.CIDR)
	if err != nil {
		return "", fmt.Errorf("invalid firewall CIDR: %w", err)
	}
	if ip.To4() != nil {
		return "ip saddr " + rule.CIDR + " " + match, nil
	}
	return "ip6 saddr " + rule.CIDR + " " + match, nil
}
