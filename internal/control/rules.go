package control

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
)

// RouteRule is intentionally a closed model of fields documented for the
// control plane. It is not an arbitrary sing-box route JSON fragment.
type RouteRule struct {
	ID           string   `json:"id"`
	NodeID       string   `json:"node_id"`
	Priority     int      `json:"priority"`
	Enabled      bool     `json:"enabled"`
	Domains      []string `json:"domains"`
	DomainSuffix []string `json:"domain_suffix"`
	CIDRs        []string `json:"cidrs"`
	Port         uint16   `json:"port"`
	Network      string   `json:"network"`
	Protocol     string   `json:"protocol"`
	InboundTag   string   `json:"inbound_tag"`
	EndpointName string   `json:"endpoint_name"`
	Action       string   `json:"action"`
	OutboundTag  string   `json:"outbound_tag"`
}

func ValidateRouteRule(rule RouteRule) error {
	if rule.NodeID == "" {
		return errors.New("rule node is required")
	}
	if rule.Priority < 0 || rule.Priority > 1_000_000 {
		return errors.New("rule priority is out of range")
	}
	if rule.Network != "" && rule.Network != "tcp" && rule.Network != "udp" {
		return errors.New("rule network must be tcp or udp")
	}
	if rule.Action != "direct" && rule.Action != "reject" && rule.Action != "outbound" {
		return errors.New("rule action must be direct, reject, or outbound")
	}
	if rule.Action == "outbound" && rule.OutboundTag == "" {
		return errors.New("outbound action requires an outbound tag")
	}
	if len(rule.Domains)+len(rule.DomainSuffix)+len(rule.CIDRs) == 0 && rule.Port == 0 && rule.Network == "" && rule.Protocol == "" && rule.InboundTag == "" && rule.EndpointName == "" {
		return errors.New("rule requires at least one match condition")
	}
	for _, domain := range append(append([]string{}, rule.Domains...), rule.DomainSuffix...) {
		if domain == "" || strings.ContainsAny(domain, " \t\r\n") {
			return fmt.Errorf("invalid domain match %q", domain)
		}
	}
	for _, cidr := range rule.CIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q", cidr)
		}
	}
	return nil
}

func sortRouteRules(rules []RouteRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Priority == rules[j].Priority {
			return rules[i].ID < rules[j].ID
		}
		return rules[i].Priority < rules[j].Priority
	})
}
