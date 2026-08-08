package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	case "nft":
		// The firewall is now read back from the host rather than from any
		// stored copy, so the stub has to behave like a ruleset: what a load
		// writes is what a later listing returns.
		nftables(os.Args[1:])
	case "nginx", "fail2ban-client":
		// Successful deterministic replacements used only inside the E2E agent
		// process. Every invocation is recorded for later assertions.
	default:
		fail("unsupported command name " + name)
	}
}

// nftables emulates just enough of nft for the round trip the console depends
// on: rules are added, listed back with handles, and deleted by handle. The
// state is one rule per line so the stub stays as simple as what it stands in
// for.
func nftables(args []string) {
	if contains(args, "-c") {
		return
	}
	// -a only changes how a listing is printed, and this stub always prints
	// handles because that is what the console needs.
	if len(args) > 0 && args[0] == "-a" {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	rules := readStubRules()
	switch args[0] {
	case "add", "insert":
		if len(args) < 2 {
			return
		}
		switch args[1] {
		case "table", "chain":
			// Creating these is idempotent and needs no state of its own: a
			// rule's presence is the only thing the console reads back.
			return
		case "rule":
			// add rule <family> <table> <chain> <expression...>
			// insert rule <family> <table> <chain> position <handle> <expression...>
			if len(args) < 6 {
				return
			}
			rest := args[5:]
			position := -1
			if args[0] == "insert" && len(rest) >= 2 && rest[0] == "position" {
				position = indexOfHandle(rules, rest[1])
				rest = rest[2:]
			}
			entry := strings.Join(args[2:5], " ") + "\t" + strings.Join(rest, " ")
			if position < 0 || position > len(rules) {
				rules = append(rules, entry)
			} else {
				rules = append(rules[:position], append([]string{entry}, rules[position:]...)...)
			}
			writeStubRules(rules)
		}
	case "delete":
		// delete rule <family> <table> <chain> handle <n>
		if len(args) != 7 || args[1] != "rule" || args[5] != "handle" {
			return
		}
		index := indexOfHandle(rules, args[6])
		if index < 0 {
			fail("Could not process rule: No such file or directory")
		}
		writeStubRules(append(rules[:index], rules[index+1:]...))
	case "list":
		if len(args) >= 2 && args[1] == "ruleset" {
			printStubRuleset(rules, "")
			return
		}
		// list table <family> <name>
		if len(args) == 4 && args[1] == "table" {
			scope := args[2] + " " + args[3]
			if !hasScope(rules, scope) {
				fail("No such file or directory")
			}
			printStubRuleset(rules, scope)
		}
	}
}

func stubRulePath() string {
	return filepath.Join(os.Getenv("POLARIS_E2E_ROOT"), "nftables-rules.txt")
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

// Handles are the rule's one-based position here. Real nft never reuses one,
// but the console only ever deletes a handle it has just been told about.
func indexOfHandle(rules []string, handle string) int {
	for index := range rules {
		if fmt.Sprint(index+1) == handle {
			return index
		}
	}
	return -1
}

func hasScope(rules []string, scope string) bool {
	for _, rule := range rules {
		if strings.HasPrefix(rule, scope+" ") {
			return true
		}
	}
	return false
}

func printStubRuleset(rules []string, scope string) {
	type chainKey struct{ table, chain string }
	var order []chainKey
	grouped := map[chainKey][]string{}
	for index, rule := range rules {
		location, expression, found := strings.Cut(rule, "\t")
		if !found {
			continue
		}
		fields := strings.Fields(location)
		if len(fields) != 3 {
			continue
		}
		table := fields[0] + " " + fields[1]
		if scope != "" && table != scope {
			continue
		}
		key := chainKey{table: table, chain: fields[2]}
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], fmt.Sprintf("\t\t%s # handle %d", expression, index+1))
	}
	for _, key := range order {
		fmt.Printf("table %s {\n\tchain %s {\n", key.table, key.chain)
		fmt.Println("\t\ttype filter hook input priority filter; policy accept;")
		for _, line := range grouped[key] {
			fmt.Println(line)
		}
		fmt.Println("\t}\n}")
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
