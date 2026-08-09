package agent

import (
	"context"
	"errors"
	"net"
	"os"
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

// LiveFirewall is everything a host enforces on traffic right now. Manager is
// the tool the host manages its firewall with, DefaultIncoming its policy for
// traffic nothing else matches, and PortRules the verdict it holds on each
// port it names — the question the console exists to answer.
//
// Rules is the unabridged kernel listing PortRules is worked out from on a host
// with no firewall manager. It stays on this side of the wire: it is mostly
// Fail2Ban's address bans on any host that runs Fail2Ban, and a list of blocked
// addresses answers no question about which ports a server offers.
type LiveFirewall struct {
	Available       bool               `json:"available"`
	Tool            string             `json:"tool,omitempty"`
	Manager         string             `json:"manager,omitempty"`
	DefaultIncoming string             `json:"default_incoming,omitempty"`
	PortRules       []PortRule         `json:"port_rules"`
	Rules           []LiveFirewallRule `json:"-"`
	Truncated       bool               `json:"truncated,omitempty"`
	Error           string             `json:"error,omitempty"`
}

// FirewallMutation is one change to the host's rules. An addition carries the
// rule to write; a deletion carries the location of the rule to remove, which
// is how any rule on the machine can be taken out regardless of who wrote it.
type FirewallMutation struct {
	Operation string           `json:"operation"` // "add" | "delete"
	Rule      LiveFirewallRule `json:"rule"`
}

// CollectLiveFirewall reads the host's rules back, then has the host's own
// firewall tool say which ports it opens and which it refuses. The kernel
// listing alone cannot answer that on a host running ufw or firewalld: their
// rules reach it through chains and sets whose shape is theirs, and only they
// know the zones and service names behind it.
func CollectLiveFirewall(ctx context.Context) LiveFirewall {
	var live LiveFirewall
	switch {
	case commandExists("nft"):
		live = collectLiveNftables(ctx)
	case commandExists("iptables"):
		live = collectLiveIptables(ctx)
	default:
		live = LiveFirewall{Rules: []LiveFirewallRule{}, Error: "服务器上没有安装 nftables 或 iptables，无法读取防火墙规则"}
	}
	describeHostFirewall(ctx, &live)
	// Always a list, even when reading the host failed. An answer that omits it
	// is an answer from an agent too old to know about port rules, and the
	// master tells that apart from a host that genuinely has none.
	if live.PortRules == nil {
		live.PortRules = []PortRule{}
	}
	return live
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
	live.DefaultIncoming = defaultIncomingFromRuleset(string(output))
	return live
}

// defaultIncomingFromRuleset works out what a host does with incoming traffic
// no rule names. A chain's declared policy is only half of that: a chain ending
// in a rule that refuses whatever is left enforces the refusal whatever its
// policy says, and a stock Debian ruleset is exactly that shape — policy accept
// with a closing reject. Reading the policy alone reported such a host as
// letting everything in while it was in fact refusing every port nobody had
// opened, which is the opposite of what it does.
//
// Every input chain sits on the same hook and all of them are evaluated, so a
// refusal in any one is what the traffic meets: the strictest verdict found is
// the one that counts.
func defaultIncomingFromRuleset(listing string) string {
	policy := ""
	inInputChain := false
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		// A new table or chain begins; what follows belongs to it, not to the
		// chain before it.
		if strings.HasPrefix(line, "table ") || strings.HasPrefix(line, "chain ") {
			inInputChain = false
			continue
		}
		if strings.Contains(line, " hook input ") {
			inInputChain = true
			if verdict := declaredChainPolicy(line); verdict == "drop" || verdict == "reject" {
				policy = "drop"
			} else if policy == "" {
				policy = "accept"
			}
			continue
		}
		if !inInputChain {
			continue
		}
		switch unconditionalNftVerdict(line) {
		case "drop", "reject":
			policy = "drop"
		}
	}
	return policy
}

// declaredChainPolicy reads the verdict a chain falls back to out of its header.
func declaredChainPolicy(header string) string {
	index := strings.Index(header, "policy ")
	if index < 0 {
		return ""
	}
	fields := strings.Fields(header[index+len("policy "):])
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSuffix(fields[0], ";")
}

// unconditionalNftVerdict reports the verdict of a rule that decides on
// everything reaching it, and "" for one that narrows what it applies to. The
// closing `reject` a stock INPUT chain ends with is such a rule, and it — not
// the chain's declared policy — is what that chain really does with traffic
// nothing above it matched. Counters and comments are the only other things nft
// prints inside such a rule.
func unconditionalNftVerdict(line string) string {
	if index := strings.LastIndex(line, "# handle "); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	fields := strings.Fields(stripRuleComment(line))
	verdict := ""
	for index := 0; index < len(fields); index++ {
		switch fields[index] {
		case "counter":
		case "packets", "bytes":
			// The number belongs to the counter, not to the rule.
			index++
		case "accept", "drop":
			verdict = fields[index]
		case "reject":
			// What follows only says what gets sent back to the sender.
			return "reject"
		default:
			return ""
		}
	}
	return verdict
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
	live.DefaultIncoming = defaultIncomingFromIptables(string(output))
	return live
}

// defaultIncomingFromIptables answers the same question for a host with no nft
// command: the INPUT chain's policy, and the closing rule that overrides it.
func defaultIncomingFromIptables(listing string) string {
	policy := ""
	for _, raw := range strings.Split(listing, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) < 2 || fields[1] != "INPUT" {
			continue
		}
		switch fields[0] {
		case "-P":
			if len(fields) >= 3 && fields[2] != "ACCEPT" {
				policy = "drop"
			} else if policy == "" {
				policy = "accept"
			}
		case "-A":
			switch unconditionalIptablesVerdict(fields[2:]) {
			case "drop", "reject":
				policy = "drop"
			}
		}
	}
	return policy
}

// unconditionalIptablesVerdict reports the verdict of a rule that names no
// traffic to match, which in `iptables -S` wording means the jump is all there
// is to it.
func unconditionalIptablesVerdict(fields []string) string {
	if len(fields) < 2 || fields[0] != "-j" {
		return ""
	}
	verdict := ""
	switch fields[1] {
	case "ACCEPT":
		verdict = "accept"
	case "DROP":
		verdict = "drop"
	case "REJECT":
		verdict = "reject"
	default:
		return ""
	}
	rest := fields[2:]
	if len(rest) == 0 {
		return verdict
	}
	// A rejection may go on to name the message it sends back; nothing else
	// trailing a jump leaves the rule unconditional.
	if verdict == "reject" && len(rest) == 2 && rest[0] == "--reject-with" {
		return verdict
	}
	return ""
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
// enforces afterwards. Every change goes into iptables' INPUT chain, on every
// host, and anything else found deciding incoming traffic is folded into it
// first.
//
// One firewall per node is the whole point. Netfilter evaluates every base
// chain on the input hook, and there an accept only ends its own chain while a
// refusal is final — so a second firewall can refuse what the first one allowed,
// and no amount of care on this side changes that. Writing everywhere the host
// happens to keep rules was the earlier design; it meant an opened port that
// stayed shut, with the console reporting success.
func ApplyFirewallMutation(ctx context.Context, mutation FirewallMutation) (LiveFirewall, error) {
	if mutation.Operation != "add" && mutation.Operation != "delete" {
		return LiveFirewall{}, errors.New("不支持的访问限制操作")
	}
	// A deletion names a rule the host has already reported, and that report
	// carries the rule's own place in the ruleset and the host's own wording for
	// it. Removing it exactly as reported takes out the rule the operator is
	// looking at, wherever it lives — including tables this platform no longer
	// writes to. Spelling the rule out again from protocol and port would match
	// some other rule somewhere else, or none at all.
	if mutation.Operation == "delete" && (mutation.Rule.Handle != "" || strings.HasPrefix(mutation.Rule.Raw, "-A ")) {
		if err := deleteFirewallRule(ctx, mutation.Rule); err != nil {
			return LiveFirewall{}, err
		}
		return CollectLiveFirewall(ctx), nil
	}
	if err := ensureIptablesReady(ctx); err != nil {
		return LiveFirewall{}, errors.New("准备防火墙失败：" + err.Error())
	}
	// Whatever else was deciding this host's incoming traffic is folded into
	// iptables first, so the rule written next is not competing with it.
	if err := consolidateHostFirewall(ctx); err != nil {
		return LiveFirewall{}, err
	}
	if err := applyIptablesMutation(ctx, mutation); err != nil {
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
		// A rule is saved by whatever owns it, and iptables keeps its own saved
		// copy. Skipping this left the two disagreeing: the removal held until the
		// next reboot, which brought the rule back.
		if iptablesOwnsTable(rule.Family, rule.Table) {
			return persistIptables(ctx)
		}
		// Any other table belongs to whatever wrote it — Fail2Ban's own, or one
		// left behind by an older version of this agent. The rule is out of the
		// running kernel; this agent keeps no store of its own to update.
		return nil
	}
	if commandExists("iptables") && strings.HasPrefix(rule.Raw, "-A ") {
		arguments := append([]string{"-D"}, strings.Fields(rule.Raw)[1:]...)
		output, err := exec.CommandContext(ctx, "iptables", arguments...).CombinedOutput()
		if err != nil {
			return errors.New(commandSummary("iptables -D", output, err))
		}
		return persistIptables(ctx)
	}
	return errors.New("这条规则无法从服务器上删除")
}

// iptablesOwnsTable reports whether a table in the kernel ruleset is one
// iptables maintains through its nftables backend. Those tables are read back
// alongside this platform's own — they are all one ruleset — but they are saved
// and restored by iptables, so a change to one has to be saved its way.
func iptablesOwnsTable(family, table string) bool {
	if family != "ip" && family != "ip6" {
		return false
	}
	switch table {
	case "filter", "nat", "mangle", "raw", "security":
		return true
	}
	return false
}

// managedTableRules reads back the table an earlier version of this agent wrote
// its rules into. Nothing is written there any more — it is read so its contents
// can be moved into iptables and the table retired.
func managedTableRules(ctx context.Context) []LiveFirewallRule {
	output, err := exec.CommandContext(ctx, "nft", "-a", "list", "table", managedTableFamily, managedTableName).CombinedOutput()
	if err != nil {
		return nil
	}
	rules, _ := parseNftablesRuleset(string(output))
	return rules
}

// validateManagedRule checks a rule this platform is about to write or
// withdraw. "reject" belongs here alongside "drop" because a rule being
// withdrawn was read back off the host, and a host is free to have written one:
// refusing to act on it would leave a rule on screen that no button can remove.
func validateManagedRule(rule LiveFirewallRule) error {
	if rule.Action != "accept" && rule.Action != "drop" && rule.Action != "reject" {
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
	// A host prints a single address without a prefix length — ufw's From column
	// says "192.168.1.5" — and a rule read back off a host has to be removable
	// in the words that host used.
	if _, ok := normalizeCIDR(rule.CIDR); !ok {
		return errors.New("来源地址范围无效")
	}
	return nil
}

// consolidateHostFirewall leaves iptables as the only thing deciding this
// host's incoming traffic, moving across whatever it takes over so nothing that
// was being enforced stops being enforced.
//
// It runs before a rule is written rather than at startup: taking over a
// server's firewall is not something to do behind the operator's back on a
// routine restart, and this is the moment they have asked for the firewall to
// change.
func consolidateHostFirewall(ctx context.Context) error {
	switch detectFirewallManager(ctx) {
	case managerUFW:
		if err := takeOverFromUFW(ctx); err != nil {
			return err
		}
	case managerFirewalld:
		if err := takeOverFromFirewalld(ctx); err != nil {
			return err
		}
	}
	return retireManagedTable(ctx)
}

// retireManagedTable clears out the nftables table earlier versions of this
// agent wrote into. Its rules move to iptables and the table, its configuration
// file and its boot unit all go — left in place it would keep competing on the
// input hook, and a refusal in it would still be final.
func retireManagedTable(ctx context.Context) error {
	if !commandExists("nft") {
		return nil
	}
	rules := managedTableRules(ctx)
	if len(rules) == 0 {
		// The table is gone or never held anything, but the file and unit that
		// reinstate it at boot may still be on disk.
		return removeRetiredNftables(ctx)
	}
	// The rules keep their order, all of them landing ahead of whatever closes
	// the INPUT chain. Their order relative to each other is what made the table
	// mean what it meant — an allowance ahead of the refusal that narrows it.
	for _, rule := range rules {
		if rule.Protocol == "" || rule.Port == 0 || rule.Action == "" {
			// Conntrack and loopback allowances are the table's own scaffolding,
			// and a stock INPUT chain already carries its equivalent.
			continue
		}
		if err := adoptRuleInOrder(ctx, rule); err != nil {
			return errors.New("迁移旧防火墙规则失败：" + err.Error())
		}
	}
	// The table goes before the ruleset is saved. Saving can fail on a host that
	// cannot export its own filter table, and a retirement left half-done — rules
	// in both places, the table still competing on the input hook — is worse than
	// one that completed and could not be written down.
	if err := removeRetiredNftables(ctx); err != nil {
		return err
	}
	return persistIptables(ctx)
}

// removeRetiredNftables takes the retired table out of the kernel and removes
// the file and unit that used to bring it back at boot.
func removeRetiredNftables(ctx context.Context) error {
	// Deleting a table that is not there fails, and a table that is not there is
	// the state being aimed at. A failure that actually matters — no permission
	// to change the ruleset — surfaces on the file removals below.
	_, _ = exec.CommandContext(ctx, "nft", "delete", "table", managedTableFamily, managedTableName).CombinedOutput()
	if commandExists("systemctl") {
		// Failure here means the unit was never installed, which is the state
		// being aimed at anyway.
		_, _ = exec.CommandContext(ctx, "systemctl", "disable", "polaris-nftables.service").CombinedOutput()
	}
	for _, path := range []string{managedNftablesConfig, managedNftablesUnit} {
		if err := os.Remove(managedSystemPath(path)); err != nil && !os.IsNotExist(err) {
			return errors.New("清理旧防火墙配置失败：" + err.Error() + permissionHint(err))
		}
	}
	return nil
}
