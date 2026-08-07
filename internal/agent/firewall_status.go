package agent

import (
	"context"
	"os/exec"
	"strings"
)

// maximumReportedFirewallRules bounds how much of a host's existing firewall
// a report carries. A busy gateway can hold thousands of rules and the console
// only needs enough of them to show what a server is already protected by.
const maximumReportedFirewallRules = 200

// CollectFirewallStatus reports the firewall rules already present on the host
// that polaris did not write. The managed `inet polaris` table is left
// out: the console already shows those as its own access limits, and repeating
// them here would read as duplicate protection.
func CollectFirewallStatus(ctx context.Context) *FirewallReport {
	if commandExists("nft") {
		return collectNftablesRules(ctx)
	}
	if commandExists("iptables") {
		return collectIptablesRules(ctx)
	}
	return nil
}

func collectNftablesRules(ctx context.Context) *FirewallReport {
	report := &FirewallReport{Available: true, Tool: "nftables"}
	output, err := exec.CommandContext(ctx, "nft", "list", "tables").CombinedOutput()
	if err != nil {
		report.Error = commandSummary("nft list tables", output, err)
		return report
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		// `table inet filter`
		if len(fields) != 3 || fields[0] != "table" {
			continue
		}
		family, name := fields[1], fields[2]
		if family == "inet" && name == "polaris" {
			continue
		}
		tableOutput, err := exec.CommandContext(ctx, "nft", "list", "table", family, name).CombinedOutput()
		if err != nil {
			continue
		}
		if appendNftablesTableRules(report, family+" "+name, string(tableOutput)) {
			report.Truncated = true
			return report
		}
	}
	return report
}

// appendNftablesTableRules turns one table's listing into rule entries and
// reports whether the cap was reached.
func appendNftablesTableRules(report *FirewallReport, table, listing string) bool {
	chain := ""
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "" || line == "}" || strings.HasPrefix(line, "table "):
			continue
		case strings.HasPrefix(line, "chain "):
			chain = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "chain "), "{"))
			continue
		case strings.HasPrefix(line, "set ") || strings.HasPrefix(line, "map "):
			chain = ""
			continue
		// Comments nft emits about a table's owner, and a chain's own
		// type/hook/policy header, match no traffic. Listing them beside real
		// rules made the console read as if the host had protection it does
		// not have.
		case strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "type ") && strings.Contains(line, " hook "):
			continue
		case strings.HasPrefix(line, "policy "):
			continue
		}
		if len(report.Rules) >= maximumReportedFirewallRules {
			return true
		}
		report.Rules = append(report.Rules, FirewallRuleEntry{Table: table, Chain: chain, Rule: line})
	}
	return false
}

func collectIptablesRules(ctx context.Context) *FirewallReport {
	report := &FirewallReport{Available: true, Tool: "iptables"}
	output, err := exec.CommandContext(ctx, "iptables", "-S").CombinedOutput()
	if err != nil {
		report.Error = commandSummary("iptables -S", output, err)
		return report
	}
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		// A chain policy (-P) or a bare chain declaration (-N) is not a rule
		// either, for the same reason the nftables listing skips its headers.
		if line == "" || strings.HasPrefix(line, "-P ") || strings.HasPrefix(line, "-N ") {
			continue
		}
		if len(report.Rules) >= maximumReportedFirewallRules {
			report.Truncated = true
			break
		}
		chain := ""
		if fields := strings.Fields(line); len(fields) >= 2 {
			chain = fields[1]
		}
		report.Rules = append(report.Rules, FirewallRuleEntry{Table: "filter", Chain: chain, Rule: line})
	}
	return report
}
