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

func CompileNftables(rules []FirewallRule) (string, error) {
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].CIDR < rules[j].CIDR })
	var lines []string
	for _, rule := range rules {
		if !rule.Enabled || (rule.ExpiresAt != 0 && rule.ExpiresAt <= time.Now().UTC().Unix()) {
			continue
		}
		if rule.Action != "accept" && rule.Action != "drop" {
			return "", errors.New("firewall action must be accept or drop")
		}
		if rule.Protocol != "tcp" && rule.Protocol != "udp" {
			return "", errors.New("firewall protocol must be tcp or udp")
		}
		if rule.Port == 0 {
			return "", errors.New("firewall port is required")
		}
		prefix := rule.Protocol + " dport " + strconv.Itoa(int(rule.Port))
		if rule.CIDR != "" {
			ip, _, err := net.ParseCIDR(rule.CIDR)
			if err != nil {
				return "", fmt.Errorf("invalid firewall CIDR: %w", err)
			}
			if ip.To4() != nil {
				prefix = "ip saddr " + rule.CIDR + " " + prefix
			} else {
				prefix = "ip6 saddr " + rule.CIDR + " " + prefix
			}
		}
		lines = append(lines, "    "+prefix+" "+rule.Action)
	}
	configuration := "table inet sb_control {\n  chain input {\n    type filter hook input priority filter; policy accept;\n" + strings.Join(lines, "\n") + "\n  }\n}\n"
	return configuration, nil
}
