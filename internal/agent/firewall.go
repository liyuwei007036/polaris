package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// The console shows and edits what the host is actually enforcing, so the
// host — not a record kept somewhere else — is the only source of truth here.
// Every rule on the machine is listed and every rule can be removed, whoever
// wrote it; a firewall does not distinguish between rules by origin and
// neither does this.

// managedTableFamily and managedTableName identify the table new rules are
// added to. Rules anywhere else are listed and can be deleted, they are just
// not where an addition goes.
const managedTableFamily = "inet"
const managedTableName = "polaris"

// maximumReportedFirewallRules bounds how much of a host's firewall one answer
// carries. A gateway fronted by a CDN routinely holds hundreds of source
// ranges, so this has to be generous enough to show them all.
const maximumReportedFirewallRules = 500

// LiveFirewallRule is one rule in force on the host at the moment it was read.
// Raw is always the host's own wording; the other fields are what could be made
// out of it, and stay empty for a rule shaped in a way this parser does not
// recognize. Nothing is hidden because it could not be parsed.
type LiveFirewallRule struct {
	Family string `json:"family,omitempty"`
	Table  string `json:"table,omitempty"`
	Chain  string `json:"chain,omitempty"`
	// Handle is what nft deletes a rule by. A rule without one cannot be
	// removed, which is why the console only offers deletion where it is set.
	Handle   string `json:"handle,omitempty"`
	Action   string `json:"action,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     uint16 `json:"port,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	Raw      string `json:"raw"`
}

// LiveFirewall is everything a host enforces on traffic right now.
type LiveFirewall struct {
	Available bool               `json:"available"`
	Tool      string             `json:"tool,omitempty"`
	Rules     []LiveFirewallRule `json:"rules"`
	Truncated bool               `json:"truncated,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// FirewallMutation is one change to the host's rules. An addition carries the
// rule to write; a deletion carries the location of the rule to remove, which
// is how any rule on the machine can be taken out regardless of who wrote it.
type FirewallMutation struct {
	Operation string           `json:"operation"` // "add" | "delete"
	Rule      LiveFirewallRule `json:"rule"`
}

// CollectLiveFirewall reads the host's rules back.
func CollectLiveFirewall(ctx context.Context) LiveFirewall {
	switch {
	case commandExists("nft"):
		return collectLiveNftables(ctx)
	case commandExists("iptables"):
		return collectLiveIptables(ctx)
	default:
		return LiveFirewall{Rules: []LiveFirewallRule{}, Error: "服务器上没有安装 nftables 或 iptables，无法读取防火墙规则"}
	}
}

func collectLiveNftables(ctx context.Context) LiveFirewall {
	live := LiveFirewall{Available: true, Tool: "nftables", Rules: []LiveFirewallRule{}}
	// -a is what makes deletion possible: without the handles nft prints here
	// there is no way to name a rule when removing it.
	output, err := exec.CommandContext(ctx, "nft", "-a", "list", "ruleset").CombinedOutput()
	if err != nil {
		live.Error = commandSummary("nft -a list ruleset", output, err)
		return live
	}
	live.Rules, live.Truncated = parseNftablesRuleset(string(output))
	return live
}

// parseNftablesRuleset walks a whole ruleset listing and returns every rule in
// it, in the order the kernel evaluates them. Order is preserved deliberately:
// in a firewall the first match wins, so a list sorted by anything else would
// misrepresent what the server does.
func parseNftablesRuleset(listing string) ([]LiveFirewallRule, bool) {
	rules := []LiveFirewallRule{}
	var family, table, chain string
	// skipDepth marks a block whose contents are not rules — a set, a map, or
	// anything else nft nests inside a table.
	skipDepth := 0
	depth := 0
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")
		switch {
		case opens > closes:
			depth += opens - closes
			fields := strings.Fields(line)
			switch {
			case skipDepth > 0:
				// Already inside something that holds no rules.
			case fields[0] == "table" && len(fields) >= 3:
				family, table, chain = fields[1], fields[2], ""
			case fields[0] == "chain" && len(fields) >= 2:
				chain = fields[1]
			default:
				skipDepth = depth
			}
			continue
		case closes > opens:
			depth -= closes - opens
			if skipDepth > depth {
				skipDepth = 0
			}
			if depth < 2 {
				chain = ""
			}
			if depth < 1 {
				family, table = "", ""
			}
			continue
		}
		if skipDepth > 0 || chain == "" {
			continue
		}
		// A set or map written on one line, and a chain's own type/hook/policy
		// header, match no traffic.
		if opens > 0 && (strings.HasPrefix(line, "set ") || strings.HasPrefix(line, "map ") || strings.HasPrefix(line, "elements")) {
			continue
		}
		if strings.HasPrefix(line, "type ") && strings.Contains(line, " hook ") {
			continue
		}
		if strings.HasPrefix(line, "policy ") {
			continue
		}
		if len(rules) >= maximumReportedFirewallRules {
			return rules, true
		}
		rule := parseNftablesRule(line)
		rule.Family, rule.Table, rule.Chain = family, table, chain
		rules = append(rules, rule)
	}
	return rules, false
}

// parseNftablesRule makes out what it can of one rule. It scans for the parts
// it understands rather than expecting a fixed shape, because nft prints
// counters, comments and match expressions this platform never wrote — an
// earlier version demanded an exact word order and so reported a perfectly
// ordinary `ip saddr … counter packets … accept` as unreadable.
func parseNftablesRule(line string) LiveFirewallRule {
	rule := LiveFirewallRule{}
	if index := strings.LastIndex(line, "# handle "); index >= 0 {
		rule.Handle = strings.TrimSpace(line[index+len("# handle "):])
		line = strings.TrimSpace(line[:index])
	}
	rule.Raw = line
	fields := strings.Fields(stripRuleComment(line))
	for index, field := range fields {
		switch field {
		case "saddr":
			if index > 0 && (fields[index-1] == "ip" || fields[index-1] == "ip6") && index+1 < len(fields) {
				if cidr, ok := normalizeCIDR(fields[index+1]); ok {
					rule.CIDR = cidr
				}
			}
		case "dport":
			if index > 0 && (fields[index-1] == "tcp" || fields[index-1] == "udp") && index+1 < len(fields) {
				if port, err := strconv.ParseUint(fields[index+1], 10, 16); err == nil && port > 0 {
					rule.Protocol, rule.Port = fields[index-1], uint16(port)
				}
			}
		// The verdict is whatever the rule ends with, so the last one seen
		// wins: `counter packets 1 bytes 2 accept` decides on accept.
		case "accept", "drop", "reject":
			rule.Action = field
		}
	}
	return rule
}

// stripRuleComment removes an operator's comment text so words inside it are
// never mistaken for parts of the rule.
func stripRuleComment(line string) string {
	index := strings.Index(line, `comment "`)
	if index < 0 {
		return line
	}
	rest := line[index+len(`comment "`):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return line[:index]
	}
	return line[:index] + rest[end+1:]
}

// normalizeCIDR restores the prefix length nft drops when printing a single
// address, so a rule reads back as the operator entered it.
func normalizeCIDR(value string) (string, bool) {
	if strings.Contains(value, "/") {
		if _, network, err := net.ParseCIDR(value); err == nil {
			return network.String(), true
		}
		return "", false
	}
	address := net.ParseIP(value)
	if address == nil {
		return "", false
	}
	if address.To4() != nil {
		return address.String() + "/32", true
	}
	return address.String() + "/128", true
}

func collectLiveIptables(ctx context.Context) LiveFirewall {
	live := LiveFirewall{Available: true, Tool: "iptables", Rules: []LiveFirewallRule{}}
	output, err := exec.CommandContext(ctx, "iptables", "-S").CombinedOutput()
	if err != nil {
		live.Error = commandSummary("iptables -S", output, err)
		return live
	}
	for _, raw := range strings.Split(string(output), "\n") {
		line := strings.TrimSpace(raw)
		// A chain policy (-P) or a bare chain declaration (-N) is not a rule.
		if line == "" || strings.HasPrefix(line, "-P ") || strings.HasPrefix(line, "-N ") {
			continue
		}
		if len(live.Rules) >= maximumReportedFirewallRules {
			live.Truncated = true
			break
		}
		rule := parseIptablesRule(line)
		rule.Family, rule.Table = "ip", "filter"
		live.Rules = append(live.Rules, rule)
	}
	return live
}

// parseIptablesRule reads one `iptables -S` line the same way: take what is
// recognizable, keep the whole line either way.
func parseIptablesRule(line string) LiveFirewallRule {
	rule := LiveFirewallRule{Raw: line}
	fields := strings.Fields(line)
	for index, field := range fields {
		if index+1 >= len(fields) {
			break
		}
		switch field {
		case "-A", "--append":
			rule.Chain = fields[index+1]
		case "-s", "--source":
			if cidr, ok := normalizeCIDR(fields[index+1]); ok {
				rule.CIDR = cidr
			}
		case "-p", "--protocol":
			if fields[index+1] == "tcp" || fields[index+1] == "udp" {
				rule.Protocol = fields[index+1]
			}
		case "--dport", "--destination-port":
			if port, err := strconv.ParseUint(fields[index+1], 10, 16); err == nil && port > 0 {
				rule.Port = uint16(port)
			}
		case "-j", "--jump":
			switch fields[index+1] {
			case "ACCEPT":
				rule.Action = "accept"
			case "DROP":
				rule.Action = "drop"
			case "REJECT":
				rule.Action = "reject"
			}
		}
	}
	return rule
}

// ApplyFirewallMutation changes the host's firewall and returns what it
// enforces afterwards.
func ApplyFirewallMutation(ctx context.Context, mutation FirewallMutation) (LiveFirewall, error) {
	if err := ensureNftablesReady(ctx); err != nil {
		return LiveFirewall{}, errors.New("准备防火墙失败：" + err.Error())
	}
	var err error
	switch mutation.Operation {
	case "add":
		err = addFirewallRule(ctx, mutation.Rule)
	case "delete":
		err = deleteFirewallRule(ctx, mutation.Rule)
	default:
		err = errors.New("不支持的访问限制操作")
	}
	if err != nil {
		return LiveFirewall{}, err
	}
	return CollectLiveFirewall(ctx), nil
}

// deleteFirewallRule removes one rule wherever it lives. nft deletes by handle,
// which is the only way to name a rule unambiguously — two rules can otherwise
// read identically.
func deleteFirewallRule(ctx context.Context, rule LiveFirewallRule) error {
	if commandExists("nft") && rule.Handle != "" {
		if rule.Family == "" || rule.Table == "" || rule.Chain == "" {
			return errors.New("这条规则缺少位置信息，无法删除")
		}
		output, err := exec.CommandContext(ctx, "nft", "delete", "rule", rule.Family, rule.Table, rule.Chain, "handle", rule.Handle).CombinedOutput()
		if err != nil {
			return errors.New(commandSummary("nft delete rule", output, err))
		}
		return persistManagedTable(ctx)
	}
	if commandExists("iptables") && strings.HasPrefix(rule.Raw, "-A ") {
		arguments := append([]string{"-D"}, strings.Fields(rule.Raw)[1:]...)
		output, err := exec.CommandContext(ctx, "iptables", arguments...).CombinedOutput()
		if err != nil {
			return errors.New(commandSummary("iptables -D", output, err))
		}
		return nil
	}
	return errors.New("这条规则无法从服务器上删除")
}

// addFirewallRule writes a new rule into the managed table. An allowance is
// placed before the closing denial for its port so it is actually reached: the
// first matching rule decides, so appending it after the denial would leave it
// dead.
func addFirewallRule(ctx context.Context, rule LiveFirewallRule) error {
	if err := validateManagedRule(rule); err != nil {
		return err
	}
	if err := ensureManagedChain(ctx); err != nil {
		return err
	}
	expression, err := nftablesExpression(rule)
	if err != nil {
		return err
	}
	current := managedTableRules(ctx)
	for _, existing := range current {
		if existing.Action == rule.Action && existing.Protocol == rule.Protocol && existing.Port == rule.Port && existing.CIDR == rule.CIDR {
			return errors.New("这条访问限制已经在服务器上生效了")
		}
	}
	arguments := []string{"add", "rule", managedTableFamily, managedTableName, managedChainName}
	if rule.Action == "accept" {
		if closing := closingDenial(current, rule.Protocol, rule.Port); closing != "" {
			arguments = []string{"insert", "rule", managedTableFamily, managedTableName, managedChainName, "position", closing}
		}
	}
	arguments = append(arguments, strings.Fields(expression+" "+rule.Action)...)
	if output, err := exec.CommandContext(ctx, "nft", arguments...).CombinedOutput(); err != nil {
		return errors.New(commandSummary("nft add rule", output, err))
	}
	// An allowance only means something once everything else on that port is
	// refused; the chain accepts by default.
	if rule.Action == "accept" && closingDenial(current, rule.Protocol, rule.Port) == "" {
		closing := []string{"add", "rule", managedTableFamily, managedTableName, managedChainName, rule.Protocol, "dport", strconv.Itoa(int(rule.Port)), "drop"}
		if output, err := exec.CommandContext(ctx, "nft", closing...).CombinedOutput(); err != nil {
			return errors.New(commandSummary("nft add rule", output, err))
		}
	}
	return persistManagedTable(ctx)
}

// closingDenial finds the handle of the rule that refuses everything else on a
// port — a denial naming no source.
func closingDenial(rules []LiveFirewallRule, protocol string, port uint16) string {
	for _, rule := range rules {
		if rule.Action == "drop" && rule.Protocol == protocol && rule.Port == port && rule.CIDR == "" {
			return rule.Handle
		}
	}
	return ""
}

const managedChainName = "input"

// ensureManagedChain creates the table and chain if they are not there yet, and
// seeds the chain with the two rules the operator's own rules depend on:
// without them a denial would also cut the node's replies and local services.
func ensureManagedChain(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "nft", "add", "table", managedTableFamily, managedTableName).CombinedOutput(); err != nil {
		return errors.New(commandSummary("nft add table", output, err))
	}
	created := len(managedTableRules(ctx)) == 0
	chain := []string{"add", "chain", managedTableFamily, managedTableName, managedChainName, "{ type filter hook input priority filter; policy accept; }"}
	if output, err := exec.CommandContext(ctx, "nft", chain...).CombinedOutput(); err != nil {
		return errors.New(commandSummary("nft add chain", output, err))
	}
	if !created {
		return nil
	}
	for _, base := range [][]string{
		{"add", "rule", managedTableFamily, managedTableName, managedChainName, "ct", "state", "established,related", "accept"},
		{"add", "rule", managedTableFamily, managedTableName, managedChainName, "iif", "lo", "accept"},
	} {
		if output, err := exec.CommandContext(ctx, "nft", base...).CombinedOutput(); err != nil {
			return errors.New(commandSummary("nft add rule", output, err))
		}
	}
	return nil
}

// managedTableRules reads back just the table additions go into.
func managedTableRules(ctx context.Context) []LiveFirewallRule {
	output, err := exec.CommandContext(ctx, "nft", "-a", "list", "table", managedTableFamily, managedTableName).CombinedOutput()
	if err != nil {
		return nil
	}
	rules, _ := parseNftablesRuleset(string(output))
	return rules
}

func validateManagedRule(rule LiveFirewallRule) error {
	if rule.Action != "accept" && rule.Action != "drop" {
		return errors.New("访问限制的处理方式必须是允许或拒绝")
	}
	if rule.Protocol != "tcp" && rule.Protocol != "udp" {
		return errors.New("访问限制的协议必须是 TCP 或 UDP")
	}
	if rule.Port == 0 {
		return errors.New("访问限制需要指定端口")
	}
	if rule.CIDR == "" {
		return nil
	}
	if _, _, err := net.ParseCIDR(rule.CIDR); err != nil {
		return fmt.Errorf("来源地址范围无效：%w", err)
	}
	return nil
}

func nftablesExpression(rule LiveFirewallRule) (string, error) {
	match := rule.Protocol + " dport " + strconv.Itoa(int(rule.Port))
	if rule.CIDR == "" {
		return match, nil
	}
	address, _, err := net.ParseCIDR(rule.CIDR)
	if err != nil {
		return "", fmt.Errorf("来源地址范围无效：%w", err)
	}
	if address.To4() != nil {
		return "ip saddr " + rule.CIDR + " " + match, nil
	}
	return "ip6 saddr " + rule.CIDR + " " + match, nil
}

// persistManagedTable records the managed table so its rules come back after a
// reboot. Rules loaded into the running kernel alone live only until the next
// restart, which used to drop the whole firewall silently.
//
// Only this platform's own table is persisted. Rules in a host's other tables
// are removed from the running kernel when deleted here, and whatever put them
// there — the distribution's own firewall service, Docker, a hand-written
// script — remains responsible for whether they come back.
func persistManagedTable(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "nft", "list", "table", managedTableFamily, managedTableName).CombinedOutput()
	if err != nil {
		// Nothing left to persist: the table is gone.
		return nil
	}
	return persistNftables(ctx, replaceableNftablesScript(string(output)))
}
