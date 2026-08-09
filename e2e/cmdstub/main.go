package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), strings.ToLower(filepath.Ext(os.Args[0])))
	appendInvocation(name, os.Args[1:])
	switch name {
	case "sing-box":
		if len(os.Args) == 2 && os.Args[1] == "version" {
			fmt.Println("sing-box version e2e-stub")
			return
		}
		if len(os.Args) < 4 || os.Args[1] != "check" || os.Args[2] != "-c" {
			fail("unsupported sing-box invocation")
		}
		content, err := os.ReadFile(os.Args[3])
		if err != nil {
			fail(err.Error())
		}
		var value any
		if err := json.Unmarshal(content, &value); err != nil {
			fail("invalid JSON configuration: " + err.Error())
		}
	case "systemctl":
		if len(os.Args) > 1 && os.Args[1] == "is-active" && !contains(os.Args[2:], "--quiet") {
			fmt.Println("active")
		}
	case "iptables":
		// The firewall is read back from the host rather than from any stored
		// copy, so the stub has to behave like a chain: what an insert writes is
		// what a later listing returns.
		iptables(os.Args[1:])
	case "nginx", "fail2ban-client":
		// Successful deterministic replacements used only inside the E2E agent
		// process. Every invocation is recorded for later assertions.
	default:
		fail("unsupported command name " + name)
	}
}

// iptables emulates just enough of iptables for the round trip the console
// depends on: rules are inserted into INPUT, listed back in the wording they
// were written in, and deleted by that same wording. The state is one rule per
// line so the stub stays as simple as what it stands in for.
func iptables(args []string) {
	if len(args) == 0 {
		return
	}
	rules := readStubRules()
	switch args[0] {
	case "-S":
		// -S, and -S INPUT, both answer with the chain policy and one line per
		// rule — which is the wording a rule has to be deleted in later.
		fmt.Println("-P INPUT ACCEPT")
		for _, rule := range rules {
			fmt.Println("-A INPUT " + rule)
		}
	case "-I":
		// -I INPUT <position> <rule...>, where the position is one-based.
		if len(args) < 4 || args[1] != "INPUT" {
			fail("unsupported iptables insert")
		}
		position, err := strconv.Atoi(args[2])
		if err != nil || position < 1 {
			fail("invalid rule position " + args[2])
		}
		index := position - 1
		if index > len(rules) {
			index = len(rules)
		}
		updated := make([]string, 0, len(rules)+1)
		updated = append(updated, rules[:index]...)
		updated = append(updated, strings.Join(args[3:], " "))
		updated = append(updated, rules[index:]...)
		writeStubRules(updated)
	case "-A":
		if len(args) < 3 || args[1] != "INPUT" {
			fail("unsupported iptables append")
		}
		writeStubRules(append(rules, strings.Join(args[2:], " ")))
	case "-D":
		if len(args) < 3 || args[1] != "INPUT" {
			fail("unsupported iptables delete")
		}
		entry := strings.Join(args[2:], " ")
		for index, rule := range rules {
			if rule == entry {
				writeStubRules(append(rules[:index], rules[index+1:]...))
				return
			}
		}
		fail("iptables: Bad rule (does a matching rule exist in that chain?)")
	}
}

func stubRulePath() string {
	return filepath.Join(os.Getenv("POLARIS_E2E_ROOT"), "iptables-rules.txt")
}

func readStubRules() []string {
	content, err := os.ReadFile(stubRulePath())
	if err != nil {
		return nil
	}
	var rules []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) != "" {
			rules = append(rules, line)
		}
	}
	return rules
}

func writeStubRules(rules []string) {
	if err := os.WriteFile(stubRulePath(), []byte(strings.Join(rules, "\n")), 0o600); err != nil {
		fail(err.Error())
	}
}

func appendInvocation(name string, args []string) {
	path := os.Getenv("POLARIS_E2E_COMMAND_LOG")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fail(err.Error())
	}
	defer file.Close()
	_, _ = fmt.Fprintln(file, name, strings.Join(args, " "))
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
