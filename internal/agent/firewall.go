package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// The console shows and edits what the host is actually enforcing, so the
// host — not a record kept somewhere else — is the only source of truth here.
// Everything below either reads the live ruleset back or rewrites it.

// managedTableFamily and managedTableName identify the one table this platform
// writes. Every other table on the host is reported as it stands and never
// touched.
const managedTableFamily = "inet"
const managedTableName = "polaris"

// automaticRuleComment marks the closing denial that turns a port into a
// whitelist. It is written into the rule itself because the console has to be
// able to tell it apart from an operator's own "deny everyone" rule, which is
// otherwise worded identically.
const automaticRuleComment = "polaris-auto"

// baseRuleComment marks the two rules every managed chain needs in order for
// the operator's rules to mean what they say: replies to connections this host
// opened, and loopback traffic. They are not access limits and are left out of
// what the console lists.
const baseRuleComment = "polaris-base"

// maximumReportedFirewallRules bounds how much of a host's existing firewall
// one answer carries. A busy gateway can hold thousands of rules and the
// console only needs enough of them to show what a server is protected by.
const maximumReportedFirewallRules = 200

// LiveFirewallRule is one rule in force on the host at the moment it was read.
type LiveFirewallRule struct {
	// Managed marks a rule this platform wrote. Those can be removed from the
	// console; everything else is reported exactly as the host words it.
	Managed bool   `json:"managed"`
	Table   string `json:"table,omitempty"`
	Chain   string `json:"chain,omitempty"`
	// The structured fields are filled in for managed rules only — this
	// platform wrote them, so it can read them back with confidence.
	Action   string `json:"action,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Port     uint16 `json:"port,omitempty"`
	CIDR     string `json:"cidr,omitempty"`
	// Automatic marks the closing denial described at automaticRuleComment. It
	// is in force like any other rule, but it belongs to the allowance above it
	// rather than to an operator, so the console will not offer to delete it.
	Automatic bool `json:"automatic,omitempty"`
	// Raw is the host's own wording, kept for rules this platform did not write.
	Raw string `json:"raw,omitempty"`
}

// LiveFirewall is everything a host enforces on inbound traffic right now.
type LiveFirewall struct {
	Available bool               `json:"available"`
	Tool      string             `json:"tool,omitempty"`
	Rules     []LiveFirewallRule `json:"rules"`
	Truncated bool               `json:"truncated,omitempty"`
	Error     string             `json:"error,omitempty"`
}

// FirewallMutation is one change to the managed rules. The console never sends
// a whole ruleset: the node reads its own current rules, applies this change to
// them, and writes the result back, so two operators working at once cannot
// silently discard each other's rule.
type FirewallMutation struct {
	Operation string           `json:"operation"` // "add" | "delete"
	Rule      LiveFirewallRule `json:"rule"`
}

// CollectLiveFirewall reads the host's inbound rules back. Rules this platform
// wrote come back structured; the rest come back as the host words them.
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
	output, err := exec.CommandContext(ctx, "nft", "list", "tables").CombinedOutput()
	if err != nil {
		live.Error = commandSummary("nft list tables", output, err)
		return live
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		// `table inet filter`
		if len(fields) != 3 || fields[0] != "table" {
			continue
		}
		family, name := fields[1], fields[2]
		listing, err := exec.CommandContext(ctx, "nft", "list", "table", family, name).CombinedOutput()
		if err != nil {
			continue
		}
		managed := family == managedTableFamily && name == managedTableName
		if appendNftablesTableRules(&live, family+" "+name, string(listing), managed) {
			live.Truncated = true
			break
		}
	}
	sortLiveFirewallRules(live.Rules)
	return live
}

// appendNftablesTableRules turns one table's listing into rule entries and
// reports whether the cap was reached.
func appendNftablesTableRules(live *LiveFirewall, table, listing string, managed bool) bool {
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
		if managed {
			// The two rules described at baseRuleComment are in force but are
			// not access limits; listing them would only make an operator
			// wonder which of their rules they are.
			if strings.Contains(line, baseRuleComment) {
				continue
			}
			rule, ok := parseManagedNftablesRule(line)
			if !ok {
				// A rule inside the managed table that this platform does not
				// recognize is still in force, so it is reported — just not as
				// something the console may rewrite.
				rule = LiveFirewallRule{Raw: line}
			}
			rule.Table, rule.Chain = table, chain
			if len(live.Rules) >= maximumReportedFirewallRules {
				return true
			}
			live.Rules = append(live.Rules, rule)
			continue
		}
		if len(live.Rules) >= maximumReportedFirewallRules {
			return true
		}
		live.Rules = append(live.Rules, LiveFirewallRule{Table: table, Chain: chain, Raw: line})
	}
	return false
}

// parseManagedNftablesRule reads back a rule this platform wrote. nft
// normalizes what it prints, so the parser accepts the forms nft emits rather
// than only the exact text that was loaded.
func parseManagedNftablesRule(line string) (LiveFirewallRule, bool) {
	rule := LiveFirewallRule{Managed: true}
	fields := strings.Fields(line)
	index := 0
	if index+2 < len(fields) && (fields[index] == "ip" || fields[index] == "ip6") && fields[index+1] == "saddr" {
		cidr, ok := normalizeCIDR(fields[index+2])
		if !ok {
			return LiveFirewallRule{}, false
		}
		rule.CIDR = cidr
		index += 3
	}
	if index+2 >= len(fields) || (fields[index] != "tcp" && fields[index] != "udp") || fields[index+1] != "dport" {
		return LiveFirewallRule{}, false
	}
	rule.Protocol = fields[index]
	port, err := strconv.ParseUint(fields[index+2], 10, 16)
	if err != nil || port == 0 {
		return LiveFirewallRule{}, false
	}
	rule.Port = uint16(port)
	index += 3
	if index >= len(fields) || (fields[index] != "accept" && fields[index] != "drop") {
		return LiveFirewallRule{}, false
	}
	rule.Action = fields[index]
	rule.Automatic = strings.Contains(line, automaticRuleComment)
	return rule, true
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
		// A chain policy (-P) or a bare chain declaration (-N) is not a rule
		// either, for the same reason the nftables listing skips its headers.
		if line == "" || strings.HasPrefix(line, "-P ") || strings.HasPrefix(line, "-N ") {
			continue
		}
		if len(live.Rules) >= maximumReportedFirewallRules {
			live.Truncated = true
			break
		}
		chain := ""
		if fields := strings.Fields(line); len(fields) >= 2 {
			chain = fields[1]
		}
		live.Rules = append(live.Rules, LiveFirewallRule{Table: "filter", Chain: chain, Raw: line})
	}
	return live
}

func sortLiveFirewallRules(rules []LiveFirewallRule) {
	sort.SliceStable(rules, func(left, right int) bool {
		if rules[left].Managed != rules[right].Managed {
			return rules[left].Managed
		}
		if !rules[left].Managed {
			return false
		}
		if rules[left].Protocol != rules[right].Protocol {
			return rules[left].Protocol < rules[right].Protocol
		}
		if rules[left].Port != rules[right].Port {
			return rules[left].Port < rules[right].Port
		}
		return rules[left].CIDR < rules[right].CIDR
	})
}

// ApplyFirewallMutation changes one managed rule on the host and returns what
// the host enforces afterwards. The node reads its own rules first, so the
// change is applied to reality rather than to whatever the console last saw.
func ApplyFirewallMutation(ctx context.Context, mutation FirewallMutation) (LiveFirewall, error) {
	if err := ensureNftablesReady(ctx); err != nil {
		return LiveFirewall{}, errors.New("准备防火墙失败：" + err.Error())
	}
	current, err := readManagedRules(ctx)
	if err != nil {
		return LiveFirewall{}, err
	}
	next, err := applyMutation(current, mutation)
	if err != nil {
		return LiveFirewall{}, err
	}
	script, err := CompileManagedNftables(next)
	if err != nil {
		return LiveFirewall{}, err
	}
	if err := loadManagedNftables(ctx, script); err != nil {
		return LiveFirewall{}, err
	}
	return CollectLiveFirewall(ctx), nil
}

// readManagedRules returns the operator-authored rules currently in the managed
// table, without the closing denials the platform derives from them — those are
// regenerated on every write.
func readManagedRules(ctx context.Context) ([]LiveFirewallRule, error) {
	output, err := exec.CommandContext(ctx, "nft", "list", "table", managedTableFamily, managedTableName).CombinedOutput()
	if err != nil {
		// No managed table yet is the normal state of a server that has never
		// had an access limit, not a failure.
		if strings.Contains(strings.ToLower(string(output)), "no such file or directory") {
			return nil, nil
		}
		return nil, errors.New(commandSummary("nft list table inet polaris", output, err))
	}
	var live LiveFirewall
	appendNftablesTableRules(&live, managedTableFamily+" "+managedTableName, string(output), true)
	rules := make([]LiveFirewallRule, 0, len(live.Rules))
	for _, rule := range live.Rules {
		if rule.Managed && !rule.Automatic {
			rules = append(rules, rule)
		}
	}
	return rules, nil
}

func applyMutation(rules []LiveFirewallRule, mutation FirewallMutation) ([]LiveFirewallRule, error) {
	target := mutation.Rule
	if err := validateManagedRule(target); err != nil {
		return nil, err
	}
	switch mutation.Operation {
	case "add":
		for _, rule := range rules {
			if sameManagedRule(rule, target) {
				return nil, errors.New("这条访问限制已经在服务器上生效了")
			}
		}
		return append(rules, target), nil
	case "delete":
		kept := make([]LiveFirewallRule, 0, len(rules))
		found := false
		for _, rule := range rules {
			if sameManagedRule(rule, target) {
				found = true
				continue
			}
			kept = append(kept, rule)
		}
		if !found {
			return nil, errors.New("服务器上已经没有这条访问限制了")
		}
		return kept, nil
	default:
		return nil, errors.New("不支持的访问限制操作")
	}
}

func sameManagedRule(left, right LiveFirewallRule) bool {
	return left.Action == right.Action && left.Protocol == right.Protocol &&
		left.Port == right.Port && left.CIDR == right.CIDR
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

// loadManagedNftables replaces the managed table in one atomic script and
// records it so the rules survive a reboot. Loading a table definition on its
// own merges into what is already there, which would stack another copy of
// every rule on each write.
func loadManagedNftables(ctx context.Context, script string) error {
	full := replaceableNftablesScript(script)
	temporaryDirectory := managedSystemPath("/tmp")
	if err := os.MkdirAll(temporaryDirectory, 0o700); err != nil {
		return errors.New("创建临时目录失败：" + err.Error() + permissionHint(err))
	}
	path, err := writeTemporaryNftablesScript(temporaryDirectory, full)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	// Checking before loading matters more here than anywhere else: the script
	// deletes the managed table before recreating it, so a script that fails
	// halfway would leave the server with no access limits at all.
	if output, err := exec.CommandContext(ctx, "nft", "-c", "-f", path).CombinedOutput(); err != nil {
		return errors.New(commandSummary("nft -c", output, err))
	}
	snapshot, _ := exec.CommandContext(ctx, "nft", "list", "table", managedTableFamily, managedTableName).CombinedOutput()
	if output, err := exec.CommandContext(ctx, "nft", "-f", path).CombinedOutput(); err != nil {
		restoreManagedNftables(ctx, temporaryDirectory, string(snapshot))
		return errors.New(commandSummary("nft -f", output, err))
	}
	return persistNftables(ctx, full)
}

func writeTemporaryNftablesScript(directory, script string) (string, error) {
	file, err := os.CreateTemp(directory, "polaris-nft-*.nft")
	if err != nil {
		return "", errors.New("创建临时文件失败：" + err.Error() + permissionHint(err))
	}
	path := file.Name()
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		os.Remove(path)
		return "", errors.New("写入防火墙脚本失败：" + err.Error())
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", errors.New("写入防火墙脚本失败：" + err.Error())
	}
	return path, nil
}

// restoreManagedNftables puts back what the table held before a failed load,
// so a rejected change leaves the server exactly as protected as it was.
func restoreManagedNftables(ctx context.Context, directory, snapshot string) {
	if strings.TrimSpace(snapshot) == "" {
		return
	}
	path, err := writeTemporaryNftablesScript(directory, replaceableNftablesScript(snapshot))
	if err != nil {
		return
	}
	defer os.Remove(path)
	_, _ = exec.CommandContext(ctx, "nft", "-f", path).CombinedOutput()
}

// CompileManagedNftables renders the managed input chain from the rules that
// should be in force.
//
// Rules are grouped per protocol and port, and each group is emitted in a
// fixed order — denials first, then allowances, then a closing denial when
// the group has any allowance at all. That last part is what makes an
// "allow" rule mean something: the chain policy is accept, so an allow rule
// on its own matched traffic that was already going to be accepted, and a
// deny rule's effect depended on where the source address happened to sort.
// An allowance turns its port into a whitelist; a group with only denials
// stays a blacklist.
func CompileManagedNftables(rules []LiveFirewallRule) (string, error) {
	type portKey struct {
		protocol string
		port     uint16
	}
	groups := map[portKey][]LiveFirewallRule{}
	var order []portKey
	for _, rule := range rules {
		if err := validateManagedRule(rule); err != nil {
			return "", err
		}
		key := portKey{protocol: rule.Protocol, port: rule.Port}
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
	lines = append(lines, `    ct state established,related accept comment "`+baseRuleComment+`"`)
	lines = append(lines, `    iif lo accept comment "`+baseRuleComment+`"`)
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
			lines = append(lines, "    "+key.protocol+" dport "+strconv.Itoa(int(key.port))+` drop comment "`+automaticRuleComment+`"`)
		}
	}
	return "table inet polaris {\n  chain input {\n    type filter hook input priority filter; policy accept;\n" +
		strings.Join(lines, "\n") + "\n  }\n}\n", nil
}

func nftablesMatch(rule LiveFirewallRule) (string, error) {
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
