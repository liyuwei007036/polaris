package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/liyuwei007036/polaris/internal/agent"
	"github.com/liyuwei007036/polaris/internal/control"
)

func TestCombinedFileAndCommandLineConfiguration(t *testing.T) {
	directory := t.TempDir()
	masterPath := filepath.Join(directory, "master.yaml")
	agentPath := filepath.Join(directory, "agent.yaml")
	masterYAML := "data_dir: master\ndatabase_path: database/control.db\nagent_port: 18443\nweb_port: 18080\n"
	agentYAML := "data_dir: agent\nmaster_address: 127.0.0.1:18443\nmaster_public_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"
	if err := os.WriteFile(masterPath, []byte(masterYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, []byte(agentYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	fileFlags := flagSetForTest(t, "combined-file")
	fileValues := addCombinedFlags(fileFlags)
	if err := fileFlags.Parse([]string{"--master-config", masterPath, "--agent-config", agentPath}); err != nil {
		t.Fatal(err)
	}
	masterFromFile, agentFromFile, err := resolveCombinedFlags(fileFlags, fileValues)
	if err != nil {
		t.Fatal(err)
	}
	if masterFromFile.DataDir != filepath.Join(directory, "master") || agentFromFile.DataDir != filepath.Join(directory, "agent") {
		t.Fatalf("combined file paths = %q / %q", masterFromFile.DataDir, agentFromFile.DataDir)
	}

	commandFlags := flagSetForTest(t, "combined-command")
	commandValues := addCombinedFlags(commandFlags)
	if err := commandFlags.Parse([]string{
		"--master-data-dir", filepath.Join(directory, "cli-master"),
		"--database-path", filepath.Join(directory, "cli.db"),
		"--agent-port", "19443", "--web-port", "19080",
		"--agent-data-dir", filepath.Join(directory, "cli-agent"),
		"--master", "127.0.0.1:19443",
		"--master-pubkey", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}); err != nil {
		t.Fatal(err)
	}
	masterFromCLI, agentFromCLI, err := resolveCombinedFlags(commandFlags, commandValues)
	if err != nil {
		t.Fatal(err)
	}
	if masterFromCLI.AgentPort != 19443 || agentFromCLI.MasterAddress != "127.0.0.1:19443" {
		t.Fatalf("combined command configuration = %#v / %#v", masterFromCLI, agentFromCLI)
	}
}

func TestCombinedRequiresBothConfigurationFiles(t *testing.T) {
	flags := flagSetForTest(t, "combined-pair")
	values := addCombinedFlags(flags)
	if err := flags.Parse([]string{"--master-config", "master.yaml"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveCombinedFlags(flags, values); err == nil {
		t.Fatal("combined accepted only one configuration file")
	}
}

func TestCombinedDoesNotAutomaticallyTrustLocalAgent(t *testing.T) {
	directory := t.TempDir()
	masterConfiguration, agentConfiguration := combinedTestConfigurations(t, directory)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveCombined(ctx, masterConfiguration, agentConfiguration) }()
	time.Sleep(400 * time.Millisecond)
	stopCombinedTest(t, cancel, done)
	store, err := control.OpenWithDatabase(masterConfiguration.DataDir, masterConfiguration.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.ListNodes(t.Context())
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("combined automatically trusted local agent: %#v", nodes)
	}
}

func TestCombinedRunsPreviouslyApprovedAgentInOneProcess(t *testing.T) {
	directory := t.TempDir()
	masterConfiguration, agentConfiguration := combinedTestConfigurations(t, directory)
	store, err := control.OpenWithDatabase(masterConfiguration.DataDir, masterConfiguration.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateInitialAdmin(t.Context(), "admin@example.com", "correct horse battery staple"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	operators, err := store.ListOperators(t.Context())
	if err != nil || len(operators) != 1 {
		store.Close()
		t.Fatalf("operators = %#v, %v", operators, err)
	}
	token, err := store.CreateRegistrationToken(t.Context(), operators[0].ID, time.Minute)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	keypair, err := agent.LoadOrCreateKeypair(agentConfiguration.DataDir)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	registration, err := store.RegisterAgent(t.Context(), control.RegistrationInput{
		Token: token.Token, NodeName: "approved-host", PublicKey: keypair.Public[:], Capabilities: `{}`,
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	approved, err := store.ApproveRegistration(t.Context(), registration.ID)
	store.Close()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- serveCombined(ctx, masterConfiguration, agentConfiguration) }()
	time.Sleep(800 * time.Millisecond)
	stopCombinedTest(t, cancel, done)
	store, err = control.OpenWithDatabase(masterConfiguration.DataDir, masterConfiguration.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.GetNode(t.Context(), approved.NodeID)
	store.Close()
	if err != nil || !node.Online {
		t.Fatalf("approved embedded agent was not online: %#v, %v", node, err)
	}
}

func combinedTestConfigurations(t *testing.T, directory string) (masterConfig, agentConfig) {
	t.Helper()
	agentPort := unusedTCPPort(t)
	webPort := unusedTCPPort(t)
	for webPort == agentPort {
		webPort = unusedTCPPort(t)
	}
	masterConfiguration := masterConfig{
		DataDir: filepath.Join(directory, "master"), DatabasePath: filepath.Join(directory, "control.db"),
		AgentPort: agentPort, WebPort: webPort, AllowInsecureHTTP: true,
	}
	store, err := control.OpenWithDatabase(masterConfiguration.DataDir, masterConfiguration.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := control.NewServer(store, false)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	publicKey := server.NoisePublicKey()
	store.Close()
	agentConfiguration := agentConfig{
		DataDir:           filepath.Join(directory, "agent"),
		MasterAddress:     fmt.Sprintf("127.0.0.1:%d", agentPort),
		MasterPublicKey:   base64.StdEncoding.EncodeToString(publicKey[:]),
		HeartbeatInterval: "5s", ConnectionsInterval: "1s",
	}
	return masterConfiguration, agentConfiguration
}

func unusedTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func stopCombinedTest(t *testing.T, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("combined service did not stop")
	}
}

func flagSetForTest(t *testing.T, name string) *flag.FlagSet {
	t.Helper()
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}
