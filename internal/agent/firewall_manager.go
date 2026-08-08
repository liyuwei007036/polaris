package agent

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// The console answers one question about a server: which ports its firewall
// lets in. That answer has to come from whatever the host itself runs. ufw and
// firewalld keep their own rule stores and rebuild the kernel ruleset from
// them, so a rule written straight into a private nftables table beside them
// is missing from their listings and gone at their next reload — it reads as
// saved and enforces nothing. Reading and writing therefore go through the
// host's own tool wherever there is one, and fall back to raw nftables only on
// hosts that manage their firewall that way.

const (
	managerUFW       = "ufw"
	managerFirewalld = "firewalld"
	managerNftables  = "nftables"
	managerIptables  = "iptables"
)

// OpenPort is one port the host firewall admits traffic on. Sources lists the
// address ranges allowed to reach it; empty means any source. A port opened by
// a named service or application profile keeps that name, because that is how
// the operator will find it again in the host's own tooling.
type OpenPort struct {
	Protocol string   `json:"protocol,omitempty"`
	Port     uint16   `json:"port,omitempty"`
	PortEnd  uint16   `json:"port_end,omitempty"`
	Sources  []string `json:"sources,omitempty"`
	Service  string   `json:"service,omitempty"`
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
// manager in charge, its default policy for incoming traffic, and the ports it
// admits. Hosts with no manager get the open ports worked out from the raw
// rules already collected.
func describeHostFirewall(ctx context.Context, live *LiveFirewall) {
	live.Manager = detectFirewallManager(ctx)
	switch live.Manager {
	case managerUFW:
		output, err := exec.CommandContext(ctx, "ufw", "status", "verbose").CombinedOutput()
		if err != nil {
			live.Error = commandSummary("ufw status verbose", output, err)
			return
		}
		live.DefaultIncoming, live.OpenPorts = parseUFWStatus(string(output))
	case managerFirewalld:
		output, err := exec.CommandContext(ctx, "firewall-cmd", "--list-all").CombinedOutput()
		if err != nil {
			live.Error = commandSummary("firewall-cmd --list-all", output, err)
			return
		}
		live.DefaultIncoming = "drop"
		live.OpenPorts = parseFirewalldZone(string(output), func(service string) []OpenPort {
			return firewalldServicePorts(ctx, service)
		})
	default:
		live.DefaultIncoming, live.OpenPorts = openPortsFromRules(live.Rules, live.DefaultIncoming)
	}
	if live.OpenPorts == nil {
		live.OpenPorts = []OpenPort{}
	}
}

// parseUFWStatus reads `ufw status verbose`: its default incoming policy and
// one entry per allowance. The IPv6 duplicates ufw prints for the same rule
// are dropped so a port is not listed twice.
func parseUFWStatus(listing string) (string, []OpenPort) {
	defaultIncoming := ""
	ports := []OpenPort{}
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
		if index < 0 || verdict != "accept" {
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
// list, the ports behind each named service, and rich rules that open a port
// to specific sources. resolveService reports the ports a named service
// covers, which only firewalld itself knows.
func parseFirewalldZone(listing string, resolveService func(string) []OpenPort) []OpenPort {
	ports := []OpenPort{}
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
		switch strings.TrimSpace(key) {
		case "ports":
			for _, field := range strings.Fields(value) {
				ports = append(ports, parsePortSpec(field)...)
			}
		case "services":
			for _, service := range strings.Fields(value) {
				resolved := resolveService(service)
				if len(resolved) == 0 {
					ports = append(ports, OpenPort{Service: service})
					continue
				}
				for _, port := range resolved {
					port.Service = service
					ports = append(ports, port)
				}
			}
		}
	}
	return ports
}

// parseFirewalldRichRule picks out the port a rich rule opens and the source
// it opens it to. Only accepting rules matter here: the list is of what gets
// in, not of every rule in the zone.
func parseFirewalldRichRule(line string) (OpenPort, bool) {
	if !strings.HasSuffix(strings.TrimSpace(line), "accept") {
		return OpenPort{}, false
	}
	port := OpenPort{}
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
		return OpenPort{}, false
	}
	return port, true
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
func firewalldServicePorts(ctx context.Context, service string) []OpenPort {
	output, err := exec.CommandContext(ctx, "firewall-cmd", "--info-service="+service).CombinedOutput()
	if err != nil {
		return nil
	}
	ports := []OpenPort{}
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

// openPortsFromRules works out which ports get in on a host with no firewall
// manager, from the rules already read out of the kernel. A chain that accepts
// by default admits everything, so there is no port list to give — saying so
// is the honest answer, and the caller reports the policy alongside.
func openPortsFromRules(rules []LiveFirewallRule, defaultIncoming string) (string, []OpenPort) {
	if defaultIncoming == "" {
		defaultIncoming = "accept"
	}
	ports := []OpenPort{}
	for _, rule := range rules {
		if rule.Action != "accept" || rule.Protocol == "" || rule.Port == 0 {
			continue
		}
		port := OpenPort{Protocol: rule.Protocol, Port: rule.Port}
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
func parsePortSpec(spec string) []OpenPort {
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
	ports := []OpenPort{}
	for _, part := range strings.Split(spec, ",") {
		start, end := parsePortRange(part)
		if start == 0 {
			continue
		}
		ports = append(ports, OpenPort{Protocol: protocol, Port: start, PortEnd: end})
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

// ufwRuleArguments spells one rule the way ufw takes it.
func ufwRuleArguments(rule LiveFirewallRule) ([]string, error) {
	if err := validateManagedRule(rule); err != nil {
		return nil, err
	}
	verb := "allow"
	if rule.Action == "drop" {
		verb = "deny"
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
	if mutation.Rule.CIDR == "" {
		verb := "--add-port="
		if mutation.Operation == "delete" {
			verb = "--remove-port="
		}
		specification = verb + strconv.Itoa(int(mutation.Rule.Port)) + "/" + mutation.Rule.Protocol
	} else {
		verdict := "accept"
		if mutation.Rule.Action == "drop" {
			verdict = "drop"
		}
		family := "ipv4"
		if strings.Contains(mutation.Rule.CIDR, ":") {
			family = "ipv6"
		}
		verb := "--add-rich-rule="
		if mutation.Operation == "delete" {
			verb = "--remove-rich-rule="
		}
		specification = verb + `rule family="` + family + `" source address="` + mutation.Rule.CIDR +
			`" port port="` + strconv.Itoa(int(mutation.Rule.Port)) + `" protocol="` + mutation.Rule.Protocol + `" ` + verdict
	}
	if output, err := exec.CommandContext(ctx, "firewall-cmd", "--permanent", specification).CombinedOutput(); err != nil {
		return errors.New(commandSummary("firewall-cmd --permanent", output, err))
	}
	if output, err := exec.CommandContext(ctx, "firewall-cmd", "--reload").CombinedOutput(); err != nil {
		return errors.New(commandSummary("firewall-cmd --reload", output, err))
	}
	return nil
}
