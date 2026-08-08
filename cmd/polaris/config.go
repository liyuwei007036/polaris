package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/agent"
	"github.com/liyuwei007036/polaris/internal/control"
	"github.com/liyuwei007036/polaris/internal/nginxroute"
	"gopkg.in/yaml.v3"
)

type masterConfig struct {
	DataDir           string `yaml:"data_dir"`
	DatabasePath      string `yaml:"database_path"`
	AgentPort         int    `yaml:"agent_port"`
	WebPort           int    `yaml:"web_port"`
	AllowInsecureHTTP bool   `yaml:"allow_insecure_http,omitempty"`
	// ConnectionsInterval paces the real-time connection push on every node.
	// It is set here rather than per node: the master hands it to each agent
	// during the handshake, so changing it never means editing a file on a
	// node or restarting one. Empty keeps the built-in default.
	ConnectionsInterval string `yaml:"connections_interval,omitempty"`
}

type agentConfig struct {
	DataDir                string                        `yaml:"data_dir"`
	MasterAddress          string                        `yaml:"master_address"`
	MasterPublicKey        string                        `yaml:"master_public_key"`
	HeartbeatInterval      string                        `yaml:"heartbeat_interval,omitempty"`
	ConnectionsInterval    string                        `yaml:"connections_interval,omitempty"`
	NginxPassthroughRoutes []agent.NginxPassthroughRoute `yaml:"nginx_passthrough_routes,omitempty"`
}

type masterFlagValues struct {
	configPath        *string
	dataDir           *string
	databasePath      *string
	agentPort         *int
	webPort           *int
	allowInsecureHTTP *bool
}

type agentFlagValues struct {
	configPath          *string
	dataDir             *string
	masterAddress       *string
	masterPublicKey     *string
	heartbeatInterval   *string
	connectionsInterval *string
}

func defaultMasterConfig() masterConfig {
	return masterConfig{DataDir: "data", AgentPort: 19994, WebPort: 19670}
}

func defaultAgentConfig() agentConfig {
	// Ten seconds is frequent enough for a live view and a fifth of the
	// traffic the old two-second cadence generated per node.
	return agentConfig{DataDir: "agent-data", HeartbeatInterval: "30s", ConnectionsInterval: "10s"}
}

func addMasterFlags(flags *flag.FlagSet) masterFlagValues {
	defaults := defaultMasterConfig()
	return masterFlagValues{
		configPath:        flags.String("config", "", "master YAML configuration file"),
		dataDir:           flags.String("data-dir", defaults.DataDir, "master data directory"),
		databasePath:      flags.String("database-path", "", "SQLite database file"),
		agentPort:         flags.Int("agent-port", defaults.AgentPort, "Noise TCP port for agents"),
		webPort:           flags.Int("web-port", defaults.WebPort, "HTTP port for the web console"),
		allowInsecureHTTP: flags.Bool("allow-insecure-http", false, "allow login cookies over plain HTTP"),
	}
}

func addAgentFlags(flags *flag.FlagSet) agentFlagValues {
	defaults := defaultAgentConfig()
	return agentFlagValues{
		configPath:          flags.String("config", "", "agent YAML configuration file"),
		dataDir:             flags.String("data-dir", defaults.DataDir, "agent data directory"),
		masterAddress:       flags.String("master", "", "master address, host:port"),
		masterPublicKey:     flags.String("master-pubkey", "", "master Noise public key, base64"),
		heartbeatInterval:   flags.String("heartbeat-interval", defaults.HeartbeatInterval, "agent heartbeat interval"),
		connectionsInterval: flags.String("connections-interval", defaults.ConnectionsInterval, "connection details push interval"),
	}
}

func resolveMasterFlags(flags *flag.FlagSet, values masterFlagValues) (masterConfig, error) {
	configuration := defaultMasterConfig()
	base, err := os.Getwd()
	if err != nil {
		return masterConfig{}, err
	}
	if *values.configPath != "" {
		configuration, base, err = loadMasterConfig(*values.configPath)
		if err != nil {
			return masterConfig{}, err
		}
	}
	visited := visitedFlags(flags)
	if *values.configPath == "" || visited["data-dir"] {
		configuration.DataDir = *values.dataDir
	}
	if *values.configPath == "" || visited["database-path"] {
		configuration.DatabasePath = *values.databasePath
	}
	if *values.configPath == "" || visited["agent-port"] {
		configuration.AgentPort = *values.agentPort
	}
	if *values.configPath == "" || visited["web-port"] {
		configuration.WebPort = *values.webPort
	}
	if *values.configPath == "" || visited["allow-insecure-http"] {
		configuration.AllowInsecureHTTP = *values.allowInsecureHTTP
	}
	return normalizeMasterConfig(configuration, base)
}

func resolveAgentFlags(flags *flag.FlagSet, values agentFlagValues) (agentConfig, error) {
	configuration := defaultAgentConfig()
	base, err := os.Getwd()
	if err != nil {
		return agentConfig{}, err
	}
	if *values.configPath != "" {
		configuration, base, err = loadAgentConfig(*values.configPath)
		if err != nil {
			return agentConfig{}, err
		}
	}
	visited := visitedFlags(flags)
	if *values.configPath == "" || visited["data-dir"] {
		configuration.DataDir = *values.dataDir
	}
	if *values.configPath == "" || visited["master"] {
		configuration.MasterAddress = *values.masterAddress
	}
	if *values.configPath == "" || visited["master-pubkey"] {
		configuration.MasterPublicKey = *values.masterPublicKey
	}
	if *values.configPath == "" || visited["heartbeat-interval"] {
		configuration.HeartbeatInterval = *values.heartbeatInterval
	}
	if *values.configPath == "" || visited["connections-interval"] {
		configuration.ConnectionsInterval = *values.connectionsInterval
	}
	return normalizeAgentConfig(configuration, base)
}

func loadMasterConfig(path string) (masterConfig, string, error) {
	configuration := defaultMasterConfig()
	base, err := loadYAMLConfig(path, &configuration)
	if err != nil {
		return masterConfig{}, "", fmt.Errorf("load master configuration: %w", err)
	}
	configuration, err = normalizeMasterConfig(configuration, base)
	return configuration, base, err
}

func loadAgentConfig(path string) (agentConfig, string, error) {
	configuration := defaultAgentConfig()
	base, err := loadYAMLConfig(path, &configuration)
	if err != nil {
		return agentConfig{}, "", fmt.Errorf("load agent configuration: %w", err)
	}
	configuration, err = normalizeAgentConfig(configuration, base)
	return configuration, base, err
}

func loadYAMLConfig(path string, target any) (string, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".yaml" && extension != ".yml" {
		return "", errors.New("configuration file must use .yaml or .yml")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return "", err
	}
	if len(data) > 1024*1024 {
		return "", errors.New("configuration file exceeds 1 MiB")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "", errors.New("configuration file is empty")
	}
	if trimmed[0] == '{' || trimmed[0] == '[' {
		return "", errors.New("JSON configuration is not supported; use YAML syntax")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return "", err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("configuration must contain exactly one YAML document")
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return base, nil
}

func normalizeMasterConfig(configuration masterConfig, base string) (masterConfig, error) {
	configuration.DataDir = resolveConfiguredPath(base, configuration.DataDir)
	if strings.TrimSpace(configuration.DatabasePath) == "" {
		configuration.DatabasePath = filepath.Join(configuration.DataDir, "polaris.db")
	} else {
		configuration.DatabasePath = resolveConfiguredPath(base, configuration.DatabasePath)
	}
	if configuration.AgentPort < 1 || configuration.AgentPort > 65535 {
		return masterConfig{}, errors.New("agent_port must be between 1 and 65535")
	}
	if configuration.WebPort < 1 || configuration.WebPort > 65535 {
		return masterConfig{}, errors.New("web_port must be between 1 and 65535")
	}
	if configuration.AgentPort == configuration.WebPort {
		return masterConfig{}, errors.New("agent_port and web_port must be different")
	}
	return configuration, nil
}

func normalizeAgentConfig(configuration agentConfig, base string) (agentConfig, error) {
	configuration.DataDir = resolveConfiguredPath(base, configuration.DataDir)
	configuration.MasterAddress = strings.TrimSpace(configuration.MasterAddress)
	configuration.MasterPublicKey = strings.TrimSpace(configuration.MasterPublicKey)
	if configuration.MasterAddress == "" {
		return agentConfig{}, errors.New("master_address is required")
	}
	host, _, err := net.SplitHostPort(configuration.MasterAddress)
	if err != nil || strings.TrimSpace(host) == "" {
		return agentConfig{}, errors.New("master_address must include a host and port")
	}
	if _, err := net.ResolveTCPAddr("tcp", configuration.MasterAddress); err != nil {
		return agentConfig{}, fmt.Errorf("invalid master_address: %w", err)
	}
	if configuration.MasterPublicKey == "" {
		return agentConfig{}, errors.New("master_public_key is required")
	}
	if _, err := parseMasterPubKey(configuration.MasterPublicKey); err != nil {
		return agentConfig{}, err
	}
	if _, _, err := agentDurations(configuration); err != nil {
		return agentConfig{}, err
	}
	for _, route := range configuration.NginxPassthroughRoutes {
		if err := nginxroute.Validate(nginxroute.Route{
			ListenAddress: route.ListenAddress, Port: route.Port, SNI: route.SNI,
			BackendAddress: route.BackendAddress, BackendPort: route.BackendPort,
		}); err != nil {
			return agentConfig{}, fmt.Errorf("invalid nginx_passthrough_routes entry: %w", err)
		}
	}
	return configuration, nil
}

// applyConnectionsInterval hands the master's configured push cadence to the
// server. An unset or unparseable value leaves the built-in default in place:
// the cadence only affects how often a node reports, so refusing to start the
// master over it would cost far more than it saves.
func applyConnectionsInterval(server *control.Server, value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	interval, err := time.ParseDuration(trimmed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ignoring connections_interval %q: %v\n", trimmed, err)
		return
	}
	server.SetConnectionsInterval(interval)
}

// sessionConnectionsInterval decides how often this session pushes its
// connection list. The master paces the whole fleet, so what it asks for on
// the handshake wins over this node's own configuration file — that is what
// makes the cadence changeable centrally rather than one SSH session per node.
// A master that asks for nothing, or for something outside the range the agent
// accepts, leaves the local value alone.
func sessionConnectionsInterval(configured time.Duration, requestedSeconds uint32) time.Duration {
	requested := time.Duration(requestedSeconds) * time.Second
	if requested < time.Second || requested > 30*time.Second {
		return configured
	}
	return requested
}

func agentDurations(configuration agentConfig) (time.Duration, time.Duration, error) {
	heartbeat, err := time.ParseDuration(configuration.HeartbeatInterval)
	if err != nil || heartbeat < 5*time.Second || heartbeat > 5*time.Minute {
		return 0, 0, errors.New("heartbeat_interval must be between 5s and 5m")
	}
	connections, err := time.ParseDuration(configuration.ConnectionsInterval)
	if err != nil || connections < time.Second || connections > 30*time.Second {
		return 0, 0, errors.New("connections_interval must be between 1s and 30s")
	}
	return heartbeat, connections, nil
}

func resolveConfiguredPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(base, filepath.Clean(value))
}

func visitedFlags(flags *flag.FlagSet) map[string]bool {
	visited := map[string]bool{}
	flags.Visit(func(item *flag.Flag) { visited[item.Name] = true })
	return visited
}

func portAddress(port int) string {
	return fmt.Sprintf(":%d", port)
}
