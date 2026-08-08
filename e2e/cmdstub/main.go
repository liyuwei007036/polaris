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
// on: `nft -f` records the managed table, `nft list` reads it back. `-c` only
// checks, so it records nothing.
func nftables(args []string) {
	state := filepath.Join(os.Getenv("POLARIS_E2E_ROOT"), "nftables-state.conf")
	switch {
	case contains(args, "-c"):
		return
	case len(args) == 2 && args[0] == "-f":
		script, err := os.ReadFile(args[1])
		if err != nil {
			fail(err.Error())
		}
		// A load replaces the table; the delete lines that make it replaceable
		// are not part of what a listing returns.
		var kept []string
		for _, line := range strings.Split(string(script), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "table inet polaris" || trimmed == "delete table inet polaris" {
				continue
			}
			kept = append(kept, line)
		}
		if err := os.WriteFile(state, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
			fail(err.Error())
		}
	case len(args) == 2 && args[0] == "list" && args[1] == "tables":
		if _, err := os.Stat(state); err == nil {
			fmt.Println("table inet polaris")
		}
	case len(args) == 4 && args[0] == "list" && args[1] == "table":
		content, err := os.ReadFile(state)
		if err != nil {
			fail("No such file or directory")
		}
		fmt.Print(string(content))
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
