package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testMasterPublicKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

func TestMasterSupportsYAMLAndCommandLineConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "master.yaml")
	content := "data_dir: state\ndatabase_path: database/control.db\nagent_port: 18443\nweb_port: 18080\nallow_insecure_http: true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fileFlags := flagSetForTest(t, "master-file")
	fileValues := addMasterFlags(fileFlags)
	if err := fileFlags.Parse([]string{"--config", path}); err != nil {
		t.Fatal(err)
	}
	fromFile, err := resolveMasterFlags(fileFlags, fileValues)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.DataDir != filepath.Join(directory, "state") || fromFile.DatabasePath != filepath.Join(directory, "database", "control.db") || fromFile.AgentPort != 18443 {
		t.Fatalf("master YAML configuration = %#v", fromFile)
	}

	commandFlags := flagSetForTest(t, "master-command")
	commandValues := addMasterFlags(commandFlags)
	if err := commandFlags.Parse([]string{
		"--data-dir", filepath.Join(directory, "cli-state"),
		"--database-path", filepath.Join(directory, "cli.db"),
		"--agent-port", "19443", "--web-port", "19080", "--allow-insecure-http",
	}); err != nil {
		t.Fatal(err)
	}
	fromCLI, err := resolveMasterFlags(commandFlags, commandValues)
	if err != nil {
		t.Fatal(err)
	}
	if fromCLI.AgentPort != 19443 || fromCLI.WebPort != 19080 || !fromCLI.AllowInsecureHTTP {
		t.Fatalf("master command configuration = %#v", fromCLI)
	}
}

func TestAgentSupportsYAMLAndCommandLineConfiguration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.yaml")
	content := "data_dir: state\nmaster_address: 127.0.0.1:18443\nmaster_public_key: " + testMasterPublicKey + "\nheartbeat_interval: 15s\nconnections_interval: 3s\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	fileFlags := flagSetForTest(t, "agent-file")
	fileValues := addAgentFlags(fileFlags)
	if err := fileFlags.Parse([]string{"--config", path}); err != nil {
		t.Fatal(err)
	}
	fromFile, err := resolveAgentFlags(fileFlags, fileValues)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.DataDir != filepath.Join(directory, "state") || fromFile.MasterAddress != "127.0.0.1:18443" {
		t.Fatalf("agent YAML configuration = %#v", fromFile)
	}

	commandFlags := flagSetForTest(t, "agent-command")
	commandValues := addAgentFlags(commandFlags)
	if err := commandFlags.Parse([]string{
		"--data-dir", filepath.Join(directory, "cli-state"),
		"--master", "127.0.0.1:19443", "--master-pubkey", testMasterPublicKey,
		"--heartbeat-interval", "20s", "--connections-interval", "4s",
	}); err != nil {
		t.Fatal(err)
	}
	fromCLI, err := resolveAgentFlags(commandFlags, commandValues)
	if err != nil {
		t.Fatal(err)
	}
	if fromCLI.MasterAddress != "127.0.0.1:19443" || fromCLI.HeartbeatInterval != "20s" {
		t.Fatalf("agent command configuration = %#v", fromCLI)
	}
}

func TestAgentSupportsValidatedNginxPassthroughRoutes(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agent.yaml")
	content := "data_dir: state\nmaster_address: 127.0.0.1:18443\nmaster_public_key: " + testMasterPublicKey + `
nginx_passthrough_routes:
  - listen_address: 0.0.0.0
    port: 443
    sni: s2a.example.com
    backend_address: 127.0.0.1
    backend_port: 10444
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration, _, err := loadAgentConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(configuration.NginxPassthroughRoutes) != 1 || configuration.NginxPassthroughRoutes[0].BackendPort != 10444 {
		t.Fatalf("passthrough routes = %#v", configuration.NginxPassthroughRoutes)
	}
	invalid := strings.Replace(content, "s2a.example.com", "not a valid SNI", 1)
	if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAgentConfig(path); err == nil {
		t.Fatal("invalid passthrough SNI was accepted")
	}
}

func TestYAMLConfigurationsAreStrictAndSeparated(t *testing.T) {
	directory := t.TempDir()
	jsonPath := filepath.Join(directory, "master.json")
	if err := os.WriteFile(jsonPath, []byte(`{"agent_port":8443}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMasterConfig(jsonPath); err == nil {
		t.Fatal("JSON master configuration was accepted")
	}
	jsonAsYAMLPath := filepath.Join(directory, "json-as-yaml.yaml")
	if err := os.WriteFile(jsonAsYAMLPath, []byte(`{"agent_port":8443}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMasterConfig(jsonAsYAMLPath); err == nil {
		t.Fatal("JSON syntax in a YAML file was accepted")
	}
	masterPath := filepath.Join(directory, "master.yaml")
	if err := os.WriteFile(masterPath, []byte("master_address: 127.0.0.1:8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadMasterConfig(masterPath); err == nil {
		t.Fatal("agent field was accepted by master configuration")
	}
	agentPath := filepath.Join(directory, "agent.yaml")
	if err := os.WriteFile(agentPath, []byte("agent_port: 8443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAgentConfig(agentPath); err == nil {
		t.Fatal("master field was accepted by agent configuration")
	}
}
