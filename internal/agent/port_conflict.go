package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// singBoxInboundBinding is the part of a compiled inbound that decides which
// socket sing-box binds.
type singBoxInboundBinding struct {
	Type       string `json:"type"`
	Listen     string `json:"listen"`
	ListenPort uint16 `json:"listen_port"`
}

// listeningSocket is one row of `ss -lntup`.
type listeningSocket struct {
	network string
	address string
	port    uint16
	process string
}

var listeningProcessPattern = regexp.MustCompile(`users:\(\("([^"]+)"`)

// singBoxPortConflicts names the inbound ports of a compiled configuration that
// a process other than sing-box already listens on.
//
// The control plane only knows about the ports it manages itself, so a port
// held by anything else on the node — an Nginx installed before polaris is the
// common case — surfaces only when sing-box fails to start. The operator then
// sees a generic rollback while systemd retries the service every two seconds.
func singBoxPortConflicts(ctx context.Context, configuration string) []string {
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) != "" {
		return nil
	}
	sockets, ok := listeningSockets(ctx)
	if !ok {
		return nil
	}
	return singBoxBindingConflicts(configuration, sockets)
}

// singBoxBindingConflicts is the decision itself, kept apart from reading the
// system so it can be exercised without a live host.
func singBoxBindingConflicts(configuration string, sockets []listeningSocket) []string {
	var parsed struct {
		Inbounds []singBoxInboundBinding `json:"inbounds"`
	}
	if err := json.Unmarshal([]byte(configuration), &parsed); err != nil {
		return nil
	}
	conflicts := []string{}
	reported := map[string]bool{}
	for _, inbound := range parsed.Inbounds {
		if inbound.ListenPort == 0 {
			continue
		}
		network := inboundNetwork(inbound.Type)
		for _, socket := range sockets {
			if socket.network != network || socket.port != inbound.ListenPort {
				continue
			}
			// sing-box holds the ports of the configuration being replaced and
			// gives them up when it restarts, so its own sockets never conflict.
			if socket.process == "sing-box" || !bindAddressesOverlap(socket.address, inbound.Listen) {
				continue
			}
			conflict := fmt.Sprintf("%s/%d 已被 %s 占用", strings.ToUpper(network), inbound.ListenPort, socket.process)
			if reported[conflict] {
				continue
			}
			reported[conflict] = true
			conflicts = append(conflicts, conflict)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return conflicts
}

func inboundNetwork(inboundType string) string {
	switch inboundType {
	case "hysteria2", "tuic":
		return "udp"
	}
	return "tcp"
}

// bindAddressesOverlap reports whether two listeners on the same port would
// fight over it. A wildcard address covers every other one, so only two
// different concrete addresses can coexist.
func bindAddressesOverlap(a, b string) bool {
	a, b = normalizeBindAddress(a), normalizeBindAddress(b)
	return a == b || a == "*" || b == "*"
}

func normalizeBindAddress(value string) string {
	value = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(value), "["), "]")
	switch value {
	case "", "*", "0.0.0.0", "::":
		return "*"
	}
	return value
}

// listeningSockets reads the listening TCP and UDP sockets together with the
// process owning each. The boolean reports whether the list could be read at
// all: without it nothing can be concluded, and the deploy has to proceed as
// it did before this check existed.
func listeningSockets(ctx context.Context) ([]listeningSocket, bool) {
	if !commandExists("ss") {
		return nil, false
	}
	output, err := exec.CommandContext(ctx, "ss", "-lntup").CombinedOutput()
	if err != nil {
		return nil, false
	}
	return parseListeningSockets(string(output)), true
}

func parseListeningSockets(output string) []listeningSocket {
	var sockets []listeningSocket
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[0] != "tcp" && fields[0] != "udp" {
			continue
		}
		address, port, ok := splitListenAddress(fields[4])
		if !ok {
			continue
		}
		// Without root ss omits the process column; the port is still known to
		// be taken, so the conflict is reported without naming the owner.
		process := "其他程序"
		if match := listeningProcessPattern.FindStringSubmatch(line); match != nil {
			process = match[1]
		}
		sockets = append(sockets, listeningSocket{network: fields[0], address: address, port: port, process: process})
	}
	return sockets
}

// splitListenAddress parses the "Local Address:Port" column, which carries
// forms such as 0.0.0.0:443, [::]:443, *:443 and 127.0.0.1:443.
func splitListenAddress(value string) (string, uint16, bool) {
	index := strings.LastIndex(value, ":")
	if index < 0 {
		return "", 0, false
	}
	port, err := strconv.ParseUint(value[index+1:], 10, 16)
	if err != nil {
		return "", 0, false
	}
	return value[:index], uint16(port), true
}
