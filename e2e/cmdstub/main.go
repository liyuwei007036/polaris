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
	case "nginx", "fail2ban-client", "nft":
		// Successful deterministic replacements used only inside the E2E agent
		// process. Every invocation is recorded for later assertions.
	default:
		fail("unsupported command name " + name)
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
