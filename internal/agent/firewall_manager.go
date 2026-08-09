package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The console answers one question about a server: which ports its firewall
// opens and which it refuses. That answer has to come from whatever the host
// itself runs. ufw and firewalld keep their own rule stores and rebuild the
// kernel ruleset from them, so a rule written straight into a private nftables
// table beside them is missing from their listings and gone at their next
// reload — it reads as saved and enforces nothing. Reading and writing
// therefore go through the host's own tool wherever there is one, and fall back
// to raw nftables only on hosts that manage their firewall that way.

const (
	managerUFW       = "ufw"
	managerFirewalld = "firewalld"
	managerIptables  = "iptables"
)

// PortRule is one verdict the host firewall holds on a port: Action is
// "accept" for a port it admits traffic on and "drop" or "reject" for one it
// refuses. Sources lists the address ranges the verdict applies to; empty means
// any source. A port opened by a named service or application profile keeps
// that name, because that is how the operator will find it again in the host's
// own tooling.
//
// Family through Raw say where in the kernel ruleset the rule sits and how the
// host itself words it. They are filled in only on a host with no firewall
// manager, where they are the only way to name the rule again when removing it:
// nft deletes by handle and iptables by the rule's own wording. ufw and
// firewalld are told the port instead and have no use for either.
type PortRule struct {
	Action   string   `json:"action,omitempty"`
	Protocol string   `json:"protocol,omitempty"`
	Port     uint16   `json:"port,omitempty"`
	PortEnd  uint16   `json:"port_end,omitempty"`
	Sources  []string `json:"sources,omitempty"`
	Service  string   `json:"service,omitempty"`
	Family   string   `json:"family,omitempty"`
	Table    string   `json:"table,omitempty"`
	Chain    string   `json:"chain,omitempty"`
	Handle   string   `json:"handle,omitempty"`
	Raw      string   `json:"raw,omitempty"`
}

// detectFirewallManager reports what is deciding this host's incoming traffic
// right now. ufw and firewalld only count while they are actually running: an
// installed but inactive ufw enforces nothing. Everything else is iptables,
// which is where this agent writes and which needs no detecting — the filter
// table and its INPUT chain are part of the kernel, present on every host.
//
// A host found running ufw or firewalld is taken over rather than written
// through. Both of them rebuild the kernel ruleset from their own stores, so
// anything written past them is discarded at their next reload, and anything
// written through them lands in a second base chain on the input hook where a
// refusal of theirs can still override an allowance of ours.
func detectFirewallManager(ctx context.Context) string {
	if commandExists("ufw") {
		output, err := exec.CommandContext(ctx, "ufw", "status").CombinedOutput()
		if err == nil && strings.Contains(strings.ToLower(string(output)), "status: active") {
			return managerUFW
		}
	}
	if commandExists("firewall-cmd") {
		output, err := exec.CommandContext(ctx, "firewall-cmd", "--state").CombinedOutput()
		if err == nil && strings.Contains(string(output), "running") {
			return managerFirewalld
		}
	}
	return managerIptables
}

// describeHostFirewall fills in what the host's own firewall tool reports: the
// manager in charge, its default policy for incoming traffic, and the verdict
// it holds on each port. Hosts with no manager get their port rules worked out
// from the raw rules already collected.
func describeHostFirewall(ctx context.Context, live *LiveFirewall) {
	live.Manager = detectFirewallManager(ctx)
	switch live.Manager {
	case managerUFW:
		output, err := exec.CommandContext(ctx, "ufw", "status", "verbose").CombinedOutput()
		if err != nil {
			live.Error = commandSummary("ufw status verbose", output, err)
			return
		}
		live.DefaultIncoming, live.PortRules = parseUFWStatus(string(output))
	case managerFirewalld:
		output, err := exec.CommandContext(ctx, "firewall-cmd", "--list-all").CombinedOutput()
		if err != nil {
			live.Error = commandSummary("firewall-cmd --list-all", output, err)
			return
		}
		live.DefaultIncoming = "drop"
		live.PortRules = parseFirewalldZone(string(output), func(service string) []PortRule {
			return firewalldServicePorts(ctx, service)
		})
	default:
		live.DefaultIncoming, live.PortRules = portRulesFromRules(live.Rules, live.DefaultIncoming)
	}
}

// parseUFWStatus reads `ufw status verbose`: its default incoming policy and
// one entry per rule, whether it allows or denies. The IPv6 duplicates ufw
// prints for the same rule are dropped so a port is not listed twice.
func parseUFWStatus(listing string) (string, []PortRule) {
	defaultIncoming := ""
	ports := []PortRule{}
	inRules := false
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Default:") {
			// "deny (incoming), allow (outgoing), disabled (routed)"
			for _, part := range strings.Split(strings.TrimPrefix(line, "Default:"), ",") {
				if !strings.Contains(part, "(incoming)") {
					continue
				}
				switch strings.TrimSpace(strings.Split(strings.TrimSpace(part), " ")[0]) {
				case "allow":
					defaultIncoming = "accept"
				case "deny", "reject":
					defaultIncoming = "drop"
				}
			}
			continue
		}
		if strings.HasPrefix(line, "To") && strings.Contains(line, "Action") {
			inRules = true
			continue
		}
		if !inRules || strings.HasPrefix(line, "--") {
			continue
		}
		verdict, index, end := ufwVerdict(line)
		if index < 0 {
			continue
		}
		target := strings.TrimSpace(line[:index])
		source := strings.TrimSpace(line[end:])
		// ufw repeats every rule for IPv6; the v4 line already names the port.
		if strings.Contains(target, "(v6)") {
			continue
		}
		sources := []string{}
		if source != "" && !strings.EqualFold(source, "Anywhere") {
			sources = append(sources, source)
		}
		for _, port := range parsePortSpec(target) {
			port.Action = verdict
			port.Sources = sources
			ports = append(ports, port)
		}
	}
	return defaultIncoming, ports
}

// ufwVerdict finds the verdict column of a ufw rule line: what it decides,
// where it starts — which is where the target specification ends — and where
// it ends, which is where the source begins.
func ufwVerdict(line string) (string, int, int) {
	for _, candidate := range []struct {
		token   string
		verdict string
	}{
		{"ALLOW IN", "accept"},
		{"LIMIT IN", "accept"},
		{"DENY IN", "drop"},
		{"REJECT IN", "reject"},
		{"ALLOW", "accept"},
		{"LIMIT", "accept"},
		{"DENY", "drop"},
		{"REJECT", "reject"},
	} {
		if index := strings.Index(line, candidate.token); index >= 0 {
			return candidate.verdict, index, index + len(candidate.token)
		}
	}
	return "", -1, -1
}

// parseFirewalldZone reads `firewall-cmd --list-all`: the zone's own port
// list, the ports behind each named service, and rich rules, which are the one
// place firewalld states a port it refuses as well as ones it opens.
// resolveService reports the ports a named service covers, which only
// firewalld itself knows.
func parseFirewalldZone(listing string, resolveService func(string) []PortRule) []PortRule {
	ports := []PortRule{}
	inRichRules := false
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if inRichRules && strings.HasPrefix(line, "rule ") {
			if port, ok := parseFirewalldRichRule(line); ok {
				ports = append(ports, port)
			}
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		inRichRules = strings.TrimSpace(key) == "rich rules"
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		// A zone's ports and services are what it lets in; firewalld states a
		// refusal only as a rich rule.
		switch strings.TrimSpace(key) {
		case "ports":
			for _, field := range strings.Fields(value) {
				for _, port := range parsePortSpec(field) {
					port.Action = "accept"
					ports = append(ports, port)
				}
			}
		case "services":
			for _, service := range strings.Fields(value) {
				resolved := resolveService(service)
				if len(resolved) == 0 {
					ports = append(ports, PortRule{Action: "accept", Service: service})
					continue
				}
				for _, port := range resolved {
					port.Action, port.Service = "accept", service
					ports = append(ports, port)
				}
			}
		}
	}
	return ports
}

// parseFirewalldRichRule picks out the port a rich rule decides on, what it
// decides, and the source the decision applies to. A rule that neither opens
// nor refuses a port — one that only logs, say — states no verdict and is not
// one of these.
func parseFirewalldRichRule(line string) (PortRule, bool) {
	verdict := firewalldVerdict(line)
	if verdict == "" {
		return PortRule{}, false
	}
	port := PortRule{Action: verdict}
	if value := richRuleValue(line, `port port=`); value != "" {
		start, end := parsePortRange(value)
		port.Port, port.PortEnd = start, end
	}
	port.Protocol = richRuleValue(line, `protocol=`)
	if source := richRuleValue(line, `source address=`); source != "" {
		port.Sources = []string{source}
	}
	if port.Port == 0 {
		if service := richRuleValue(line, `service name=`); service != "" {
			port.Service = service
			return port, true
		}
		return PortRule{}, false
	}
	return port, true
}

// firewalldVerdict reads what a rich rule decides. The verdict closes the rule,
// except for a rejection, which may go on to name the message it sends back.
func firewalldVerdict(line string) string {
	fields := strings.Fields(strings.TrimSpace(line))
	for index := len(fields) - 1; index >= 0; index-- {
		switch {
		case fields[index] == "accept":
			return "accept"
		case fields[index] == "drop":
			return "drop"
		case strings.HasPrefix(fields[index], "reject"):
			return "reject"
		}
	}
	return ""
}

// richRuleValue reads one quoted attribute out of a rich rule.
func richRuleValue(line, key string) string {
	index := strings.Index(line, key)
	if index < 0 {
		return ""
	}
	rest := line[index+len(key):]
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// firewalldServicePorts asks firewalld what a named service covers.
func firewalldServicePorts(ctx context.Context, service string) []PortRule {
	output, err := exec.CommandContext(ctx, "firewall-cmd", "--info-service="+service).CombinedOutput()
	if err != nil {
		return nil
	}
	ports := []PortRule{}
	for _, raw := range strings.Split(string(output), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(raw), ":")
		if !found || strings.TrimSpace(key) != "ports" {
			continue
		}
		for _, field := range strings.Fields(strings.TrimSpace(value)) {
			ports = append(ports, parsePortSpec(field)...)
		}
	}
	return ports
}

// portRulesFromRules works out which ports a host with no firewall manager
// opens and which it refuses, from the rules already read out of the kernel. A
// chain that accepts by default admits everything, so the list of openings is
// not the list of reachable ports — saying so is the honest answer, and the
// caller reports the policy alongside.
//
// Only a rule naming a port belongs here. A rule that names a source and no
// port is an address ban — which is what Fail2Ban writes, hundreds at a time —
// and blocking one address says nothing about which ports the server offers.
// Each rule carries where it sits and how the host words it, because on this
// kind of host that is the only way to name it again when removing it.
func portRulesFromRules(rules []LiveFirewallRule, defaultIncoming string) (string, []PortRule) {
	if defaultIncoming == "" {
		defaultIncoming = "accept"
	}
	ports := []PortRule{}
	for _, rule := range rules {
		if rule.Protocol == "" || rule.Port == 0 {
			continue
		}
		if rule.Action != "accept" && rule.Action != "drop" && rule.Action != "reject" {
			continue
		}
		port := PortRule{
			Action: rule.Action, Protocol: rule.Protocol, Port: rule.Port,
			Family: rule.Family, Table: rule.Table, Chain: rule.Chain,
			Handle: rule.Handle, Raw: rule.Raw,
		}
		if rule.CIDR != "" {
			port.Sources = []string{rule.CIDR}
		}
		ports = append(ports, port)
	}
	return defaultIncoming, ports
}

// parsePortSpec reads a port specification as ufw and firewalld write them:
// "8443/tcp", "80,443/tcp", "6000:6007/tcp", "9000-9010/udp", or a bare port.
// A specification naming no protocol covers both, which is how both tools
// treat it.
func parsePortSpec(spec string) []PortRule {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	// An application profile name ("Nginx Full") has no port to read.
	if strings.ContainsAny(spec, " (") {
		if index := strings.IndexAny(spec, " ("); index > 0 {
			spec = strings.TrimSpace(spec[:index])
		}
	}
	protocol := ""
	if base, proto, found := strings.Cut(spec, "/"); found {
		spec = base
		switch proto {
		case "tcp", "udp":
			protocol = proto
		default:
			return nil
		}
	}
	ports := []PortRule{}
	for _, part := range strings.Split(spec, ",") {
		start, end := parsePortRange(part)
		if start == 0 {
			continue
		}
		ports = append(ports, PortRule{Protocol: protocol, Port: start, PortEnd: end})
	}
	return ports
}

// parsePortRange reads "8443", "6000:6007" or "9000-9010". The end is zero for
// a single port.
func parsePortRange(value string) (uint16, uint16) {
	value = strings.TrimSpace(value)
	separator := strings.IndexAny(value, ":-")
	if separator < 0 {
		port, err := strconv.ParseUint(value, 10, 16)
		if err != nil {
			return 0, 0
		}
		return uint16(port), 0
	}
	start, err := strconv.ParseUint(strings.TrimSpace(value[:separator]), 10, 16)
	if err != nil {
		return 0, 0
	}
	end, err := strconv.ParseUint(strings.TrimSpace(value[separator+1:]), 10, 16)
	if err != nil {
		return uint16(start), 0
	}
	return uint16(start), uint16(end)
}

// maximumAdoptedPorts bounds how wide a port range is carried across during a
// takeover. iptables states a range in one rule, but the rule shape this agent
// works in names a single port, so a range becomes one rule per port. A handful
// is ordinary; a rule spanning thousands of ports is one an operator has to
// decide about themselves.
const maximumAdoptedPorts = 256

// takeOverFromUFW moves what ufw is enforcing into iptables and stops it.
//
// The order is forced by ufw itself: `ufw disable` rebuilds the filter table
// from its own store and wipes anything written into INPUT beforehand. So the
// rules are read and translated first — while ufw is still protecting the host,
// which is what makes it safe to fail — then ufw is stopped and they are written
// straight back. No ordering removes the gap between those two commands; this
// one keeps it to the shortest it can be.
func takeOverFromUFW(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "ufw", "status", "verbose").CombinedOutput()
	if err != nil {
		return errors.New(commandSummary("ufw status verbose", output, err))
	}
	defaultIncoming, ports := parseUFWStatus(string(output))
	rules, err := translatePortRules(ports)
	if err != nil {
		return err
	}
	if output, err := exec.CommandContext(ctx, "ufw", "--force", "disable").CombinedOutput(); err != nil {
		return errors.New(commandSummary("ufw --force disable", output, err))
	}
	return adoptRules(ctx, rules, defaultIncoming)
}

// takeOverFromFirewalld does the same for firewalld, whose default for incoming
// traffic in any zone this agent can read is to refuse it.
func takeOverFromFirewalld(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "firewall-cmd", "--list-all").CombinedOutput()
	if err != nil {
		return errors.New(commandSummary("firewall-cmd --list-all", output, err))
	}
	rules, err := translatePortRules(parseFirewalldZone(string(output), func(service string) []PortRule {
		return firewalldServicePorts(ctx, service)
	}))
	if err != nil {
		return err
	}
	for _, command := range [][]string{
		{"systemctl", "stop", "firewalld"},
		{"systemctl", "disable", "firewalld"},
	} {
		if output, err := exec.CommandContext(ctx, command[0], command[1:]...).CombinedOutput(); err != nil {
			return errors.New(commandSummary(strings.Join(command, " "), output, err))
		}
	}
	return adoptRules(ctx, rules, "drop")
}

// translatePortRules turns what another firewall reported into rules iptables
// can be given, and fails rather than dropping anything it cannot express.
// This runs while taking over a host's firewall: a rule quietly lost here is a
// port that silently stops being reachable, or stops being refused. Failing is
// safe because it happens before the old firewall is stopped.
func translatePortRules(ports []PortRule) ([]LiveFirewallRule, error) {
	rules := []LiveFirewallRule{}
	for _, port := range ports {
		if port.Port == 0 {
			name := port.Service
			if name == "" {
				name = "未命名"
			}
			return nil, errors.New("原有防火墙的「" + name + "」规则没有具体端口，无法自动接管，请先在服务器上手工处理后重试")
		}
		end := port.PortEnd
		if end == 0 {
			end = port.Port
		}
		if end < port.Port {
			return nil, errors.New("原有防火墙有一条端口范围无效的规则，无法自动接管")
		}
		if int(end)-int(port.Port)+1 > maximumAdoptedPorts {
			return nil, errors.New("原有防火墙有一条覆盖 " + strconv.Itoa(int(port.Port)) + "-" + strconv.Itoa(int(end)) +
				" 的规则，端口过多，无法自动接管，请先在服务器上手工处理后重试")
		}
		// A specification naming no protocol covers both, which is how ufw and
		// firewalld mean it.
		protocols := []string{port.Protocol}
		if port.Protocol == "" {
			protocols = []string{"tcp", "udp"}
		}
		sources := port.Sources
		if len(sources) == 0 {
			sources = []string{""}
		}
		for number := port.Port; ; number++ {
			for _, protocol := range protocols {
				for _, source := range sources {
					rules = append(rules, LiveFirewallRule{
						Action: port.Action, Protocol: protocol, Port: number, CIDR: source,
					})
				}
			}
			// Compared at the end so a range reaching 65535 cannot wrap around.
			if number == end {
				break
			}
		}
	}
	return rules, nil
}

// adoptRules rebuilds INPUT out of what was taken over. Stopping ufw or
// firewalld leaves the chain empty, so appending in order is what puts these
// rules in the order they have to be in.
//
// The conntrack allowance goes first and matters more than it looks: it is what
// keeps the connection the operator is working over — and every established
// proxy session — alive across the takeover. The closing refusal is written
// only when the firewall being replaced was refusing by default, because
// writing one where none was wanted would close ports that had been open.
func adoptRules(ctx context.Context, rules []LiveFirewallRule, defaultIncoming string) error {
	for _, command := range iptablesCommands(LiveFirewallRule{}) {
		invocations := [][]string{
			{"-A", "INPUT", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
			{"-A", "INPUT", "-i", "lo", "-j", "ACCEPT"},
		}
		for _, rule := range rules {
			if !ruleAppliesToCommand(rule, command) {
				continue
			}
			arguments, err := iptablesRuleArguments(rule)
			if err != nil {
				return err
			}
			invocations = append(invocations, append([]string{"-A", "INPUT"}, arguments...))
		}
		if defaultIncoming == "drop" {
			invocations = append(invocations, []string{"-A", "INPUT", "-j", "REJECT", "--reject-with", rejectMessage(command)})
		}
		for _, invocation := range invocations {
			if output, err := exec.CommandContext(ctx, command, invocation...).CombinedOutput(); err != nil {
				return errors.New(commandSummary(command+" "+strings.Join(invocation, " "), output, err))
			}
		}
	}
	// The host has just been left with a firewall nothing else knows how to
	// restore; without this the next reboot would come up with none at all.
	return persistIptables(ctx)
}

// adoptRuleInOrder writes one rule taken over from somewhere else, behind
// everything already in the chain and ahead of whatever closes it.
//
// A rule written fresh gets placed by what it decides — refusals to the head so
// they outrank existing allowances. A migrated set cannot be placed that way:
// its order is what made it mean anything, an allowance ahead of the refusal
// that narrows it to one source, and reordering it would either open the port
// to everybody or close it to everybody. Saving is left to the caller, which
// has a whole set to move and should write the file once.
func adoptRuleInOrder(ctx context.Context, rule LiveFirewallRule) error {
	arguments, err := iptablesRuleArguments(rule)
	if err != nil {
		return err
	}
	for _, command := range iptablesCommands(rule) {
		if !ruleAppliesToCommand(rule, command) {
			continue
		}
		listing := ""
		if output, err := exec.CommandContext(ctx, command, "-S", "INPUT").CombinedOutput(); err == nil {
			listing = string(output)
		}
		invocation := []string{"-A", "INPUT"}
		if position := iptablesClosingRulePosition(listing); position > 0 {
			invocation = []string{"-I", "INPUT", strconv.Itoa(position)}
		}
		invocation = append(invocation, arguments...)
		if output, err := exec.CommandContext(ctx, command, invocation...).CombinedOutput(); err != nil {
			return errors.New(commandSummary(command+" "+strings.Join(invocation, " "), output, err))
		}
	}
	return nil
}

// ruleAppliesToCommand reports whether a rule belongs in this command's table.
// A rule naming a source belongs only to that address family; one naming none
// belongs to both.
func ruleAppliesToCommand(rule LiveFirewallRule, command string) bool {
	if rule.CIDR == "" {
		return true
	}
	return (command == "ip6tables") == strings.Contains(rule.CIDR, ":")
}

// rejectMessage names the ICMP message each family refuses with.
func rejectMessage(command string) string {
	if command == "ip6tables" {
		return "icmp6-adm-prohibited"
	}
	return "icmp-host-prohibited"
}

// applyIptablesMutation changes a rule through iptables itself, so it lands in
// the chain the host is already enforcing and in the file iptables restores
// from at boot. A rule is inserted rather than appended: a chain decides on the
// first match, and a rule appended after the refusal such a chain ends with
// would never run at all.
func applyIptablesMutation(ctx context.Context, mutation FirewallMutation) error {
	arguments, err := iptablesRuleArguments(mutation.Rule)
	if err != nil {
		return err
	}
	for _, command := range iptablesCommands(mutation.Rule) {
		if mutation.Operation == "delete" {
			invocation := append([]string{"-D", "INPUT"}, arguments...)
			if output, err := exec.CommandContext(ctx, command, invocation...).CombinedOutput(); err != nil {
				return errors.New(commandSummary(command+" "+strings.Join(invocation, " "), output, err))
			}
			continue
		}
		listing := ""
		if output, err := exec.CommandContext(ctx, command, "-S", "INPUT").CombinedOutput(); err == nil {
			listing = string(output)
		}
		position := iptablesInsertPosition(listing, mutation.Rule)
		invocations := [][]string{append([]string{"-I", "INPUT", strconv.Itoa(position)}, arguments...)}
		if iptablesNeedsClosingDenial(listing, mutation.Rule) {
			// Straight after the allowance, so the allowance is what its own
			// source meets and this is what everybody else meets.
			denial, err := iptablesRuleArguments(LiveFirewallRule{
				Action: "drop", Protocol: mutation.Rule.Protocol, Port: mutation.Rule.Port,
			})
			if err != nil {
				return err
			}
			invocations = append(invocations, append([]string{"-I", "INPUT", strconv.Itoa(position + 1)}, denial...))
		}
		for _, invocation := range invocations {
			if output, err := exec.CommandContext(ctx, command, invocation...).CombinedOutput(); err != nil {
				return errors.New(commandSummary(command+" "+strings.Join(invocation, " "), output, err))
			}
		}
	}
	return persistIptables(ctx)
}

// iptablesInsertPosition reports where in INPUT a new rule belongs, as the
// one-based position `iptables -I` takes.
//
// The two directions want opposite ends of the chain. A refusal goes to the
// head, ahead of any allowance already there, or it decides nothing. An
// allowance goes as late as it still runs — immediately before the rule that
// closes the chain — because everything in between is there to refuse
// particular traffic. Putting an allowance at the head instead would jump it
// ahead of the address bans a host accumulates, opening the newly allowed port
// to exactly the addresses somebody had blocked.
func iptablesInsertPosition(listing string, rule LiveFirewallRule) int {
	if rule.Action != "accept" {
		return 1
	}
	if position := iptablesClosingRulePosition(listing); position > 0 {
		return position
	}
	// Nothing closes this chain, so nothing can leave an allowance unreached and
	// the head is as good as anywhere.
	return 1
}

// iptablesNeedsClosingDenial reports whether an allowance limited to a source
// has to be followed by a refusal of everything else on its port.
//
// Only a source-limited allowance needs one: it means "this source and nobody
// else", and without the refusal everybody else still gets in. An allowance
// naming no source says the opposite — the port is open — and a refusal written
// beside it would seal the port off the moment the allowance was removed.
//
// A chain that already refuses what it has not decided on, or that already
// refuses this particular port, has said it: a second refusal would be dead
// weight.
func iptablesNeedsClosingDenial(listing string, rule LiveFirewallRule) bool {
	if rule.Action != "accept" || rule.CIDR == "" {
		return false
	}
	if iptablesClosingRulePosition(listing) > 0 {
		return false
	}
	for _, raw := range strings.Split(listing, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "-A INPUT ") {
			continue
		}
		existing := parseIptablesRule(line)
		if existing.CIDR != "" || existing.Protocol != rule.Protocol || existing.Port != rule.Port {
			continue
		}
		if existing.Action == "drop" || existing.Action == "reject" {
			return false
		}
	}
	return true
}

// iptablesClosingRulePosition finds the first rule in INPUT that refuses
// everything reaching it, and reports its one-based position among the chain's
// rules. Zero means the chain has no such rule.
func iptablesClosingRulePosition(listing string) int {
	position := 0
	for _, raw := range strings.Split(listing, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) < 2 || fields[0] != "-A" || fields[1] != "INPUT" {
			continue
		}
		position++
		switch unconditionalIptablesVerdict(fields[2:]) {
		case "drop", "reject":
			return position
		}
	}
	return 0
}

// iptablesCommands reports which commands a rule has to be written with. A
// verdict naming no source applies to both address families, and iptables only
// covers IPv4 — leaving IPv6 out would open a port on one family while the
// operator believes it closed on both, or the reverse. ufw and firewalld do the
// same thing internally.
func iptablesCommands(rule LiveFirewallRule) []string {
	if strings.Contains(rule.CIDR, ":") {
		return []string{"ip6tables"}
	}
	if rule.CIDR != "" {
		return []string{"iptables"}
	}
	if commandExists("ip6tables") {
		return []string{"iptables", "ip6tables"}
	}
	return []string{"iptables"}
}

// iptablesRuleArguments spells one rule the way iptables takes it, leaving out
// the chain and the operation, which differ between adding and removing.
func iptablesRuleArguments(rule LiveFirewallRule) ([]string, error) {
	if err := validateManagedRule(rule); err != nil {
		return nil, err
	}
	target := "ACCEPT"
	switch rule.Action {
	case "drop":
		target = "DROP"
	case "reject":
		target = "REJECT"
	}
	arguments := []string{"-p", rule.Protocol}
	if rule.CIDR != "" {
		// iptables needs the prefix length a host leaves off a single address.
		cidr, ok := normalizeCIDR(rule.CIDR)
		if !ok {
			return nil, errors.New("来源地址范围无效")
		}
		arguments = append(arguments, "-s", cidr)
	}
	return append(arguments, "--dport", strconv.Itoa(int(rule.Port)), "-j", target), nil
}

// persistIptables saves the running rules where the host restores them from at
// boot. Changing the running kernel alone leaves the saved copy behind, and the
// next reboot brings back exactly the rule that was just removed — a host whose
// saved rules and running rules disagree is one nobody can reason about.
func persistIptables(ctx context.Context) error {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" {
		return nil
	}
	// Where netfilter-persistent is installed it owns those files, and running
	// it keeps whatever layout it was configured with.
	if commandExists("netfilter-persistent") {
		if output, err := exec.CommandContext(ctx, "netfilter-persistent", "save").CombinedOutput(); err != nil {
			return errors.New(commandSummary("netfilter-persistent save", output, err))
		}
		return nil
	}
	for _, save := range []struct{ command, path string }{
		{"iptables-save", "/etc/iptables/rules.v4"},
		{"ip6tables-save", "/etc/iptables/rules.v6"},
	} {
		if !commandExists(save.command) {
			continue
		}
		// The saved ruleset is this command's standard output, so stderr has to
		// stay out of it.
		output, err := exec.CommandContext(ctx, save.command).Output()
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return errors.New(commandSummary(save.command, exit.Stderr, err))
			}
			return errors.New(commandSummary(save.command, nil, err))
		}
		// A filter table holding even one rule that only nft can express — a
		// chain written straight through nft, which WSL and some appliances do —
		// makes this command skip the whole table, report it in a comment and
		// still exit zero. Writing that out would replace a working saved ruleset
		// with an empty one and the host would come up with no firewall at all,
		// so an export that lost the table is treated as the failure it is.
		if !strings.Contains(string(output), "*filter") {
			return errors.New(save.command + " 无法导出 filter 表：服务器上存在 iptables 语法无法表达的 nftables 规则。" +
				"规则已在运行中的防火墙上生效，但没能保存，重启后会丢失；请先在服务器上清理这类规则")
		}
		path := managedSystemPath(save.path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return errors.New("创建防火墙配置目录失败：" + err.Error() + permissionHint(err))
		}
		if err := writeFileAtomic(path, output, 0o640); err != nil {
			return errors.New("保存防火墙规则失败：" + err.Error() + permissionHint(err))
		}
	}
	return nil
}
