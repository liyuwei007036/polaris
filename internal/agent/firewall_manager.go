package agent

import (
	"context"
	"errors"
	"os/exec"
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
	managerNftables  = "nftables"
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

// detectFirewallManager reports the tool the host manages its firewall with.
// ufw and firewalld only count when they are actually running: an installed
// but inactive ufw enforces nothing, and writing rules into it would leave
// them silently inert.
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
	if commandExists("nft") {
		return managerNftables
	}
	if commandExists("iptables") {
		return managerIptables
	}
	return ""
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

// applyUFWMutation changes a rule through ufw itself, so it lands in ufw's own
// store and survives the reloads that would discard anything written beside it.
func applyUFWMutation(ctx context.Context, mutation FirewallMutation) error {
	arguments, err := ufwRuleArguments(mutation.Rule)
	if err != nil {
		return err
	}
	if mutation.Operation == "delete" {
		arguments = append([]string{"--force", "delete"}, arguments...)
	} else {
		arguments = append([]string{"--force"}, arguments...)
	}
	if output, err := exec.CommandContext(ctx, "ufw", arguments...).CombinedOutput(); err != nil {
		return errors.New(commandSummary("ufw "+strings.Join(arguments, " "), output, err))
	}
	return nil
}

// ufwRuleArguments spells one rule the way ufw takes it. A refusal ufw itself
// wrote as REJECT has to go back to it as reject: told "deny" instead, ufw
// would look for a rule it does not have and remove nothing.
func ufwRuleArguments(rule LiveFirewallRule) ([]string, error) {
	if err := validateManagedRule(rule); err != nil {
		return nil, err
	}
	verb := "allow"
	switch rule.Action {
	case "drop":
		verb = "deny"
	case "reject":
		verb = "reject"
	}
	source := "any"
	if rule.CIDR != "" {
		source = rule.CIDR
	}
	return []string{verb, "proto", rule.Protocol, "from", source, "to", "any", "port", strconv.Itoa(int(rule.Port))}, nil
}

// applyFirewalldMutation changes a rule through firewalld, permanently and in
// the running configuration both: a change made only to the running zone is
// lost on the next reload, and one made only permanently does nothing until
// then.
func applyFirewalldMutation(ctx context.Context, mutation FirewallMutation) error {
	if err := validateManagedRule(mutation.Rule); err != nil {
		return err
	}
	specification := ""
	if mutation.Rule.Action == "accept" && mutation.Rule.CIDR == "" {
		// A zone's port list is how firewalld states a port open to everybody,
		// and where the operator will look for it again.
		verb := "--add-port="
		if mutation.Operation == "delete" {
			verb = "--remove-port="
		}
		specification = verb + strconv.Itoa(int(mutation.Rule.Port)) + "/" + mutation.Rule.Protocol
	} else {
		// Everything else — a refusal, or an opening limited to a source — only
		// a rich rule can state. Putting a refusal in the zone's port list would
		// open the very port the operator asked to close.
		verb := "--add-rich-rule="
		if mutation.Operation == "delete" {
			verb = "--remove-rich-rule="
		}
		specification = verb + firewalldRichRule(mutation.Rule)
	}
	if output, err := exec.CommandContext(ctx, "firewall-cmd", "--permanent", specification).CombinedOutput(); err != nil {
		return errors.New(commandSummary("firewall-cmd --permanent", output, err))
	}
	if output, err := exec.CommandContext(ctx, "firewall-cmd", "--reload").CombinedOutput(); err != nil {
		return errors.New(commandSummary("firewall-cmd --reload", output, err))
	}
	return nil
}

// firewalldRichRule spells one rule the way firewalld prints it in
// `firewall-cmd --list-all`, attribute for attribute: removing a rich rule
// means naming it exactly, and the rule being removed is one read back off
// this very listing. A rule naming no source states no family either, so it
// covers IPv4 and IPv6 both — which is what a verdict aimed at everybody means.
func firewalldRichRule(rule LiveFirewallRule) string {
	specification := "rule"
	if rule.CIDR != "" {
		family := "ipv4"
		if strings.Contains(rule.CIDR, ":") {
			family = "ipv6"
		}
		specification += ` family="` + family + `" source address="` + rule.CIDR + `"`
	}
	return specification + ` port port="` + strconv.Itoa(int(rule.Port)) +
		`" protocol="` + rule.Protocol + `" ` + rule.Action
}
