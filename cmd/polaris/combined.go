package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/liyuwei007036/polaris/internal/control"
)

type combinedFlagValues struct {
	masterConfigPath   *string
	agentConfigPath    *string
	masterDataDir      *string
	databasePath       *string
	agentPort          *int
	webPort            *int
	allowInsecureHTTP  *bool
	agentDataDir       *string
	masterAddress      *string
	masterPublicKey    *string
	heartbeatInterval  *string
	connectionInterval *string
}

func runCombined(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return errors.New("combined command must be serve")
	}
	flags := flag.NewFlagSet("combined serve", flag.ContinueOnError)
	values := addCombinedFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	masterConfiguration, agentConfiguration, err := resolveCombinedFlags(flags, values)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return serveCombined(ctx, masterConfiguration, agentConfiguration)
}

func addCombinedFlags(flags *flag.FlagSet) combinedFlagValues {
	masterDefaults := defaultMasterConfig()
	agentDefaults := defaultAgentConfig()
	return combinedFlagValues{
		masterConfigPath:   flags.String("master-config", "", "master YAML configuration file"),
		agentConfigPath:    flags.String("agent-config", "", "agent YAML configuration file"),
		masterDataDir:      flags.String("master-data-dir", masterDefaults.DataDir, "master data directory"),
		databasePath:       flags.String("database-path", "", "SQLite database file"),
		agentPort:          flags.Int("agent-port", masterDefaults.AgentPort, "Noise TCP port for agents"),
		webPort:            flags.Int("web-port", masterDefaults.WebPort, "HTTP port for the web console"),
		allowInsecureHTTP:  flags.Bool("allow-insecure-http", false, "allow login cookies over plain HTTP"),
		agentDataDir:       flags.String("agent-data-dir", agentDefaults.DataDir, "agent data directory"),
		masterAddress:      flags.String("master", "", "master address used by the embedded agent, host:port"),
		masterPublicKey:    flags.String("master-pubkey", "", "master Noise public key pinned by the embedded agent"),
		heartbeatInterval:  flags.String("heartbeat-interval", agentDefaults.HeartbeatInterval, "agent heartbeat interval"),
		connectionInterval: flags.String("connections-interval", agentDefaults.ConnectionsInterval, "connection details push interval"),
	}
}

func resolveCombinedFlags(flags *flag.FlagSet, values combinedFlagValues) (masterConfig, agentConfig, error) {
	if (*values.masterConfigPath == "") != (*values.agentConfigPath == "") {
		return masterConfig{}, agentConfig{}, errors.New("--master-config and --agent-config must be provided together")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return masterConfig{}, agentConfig{}, err
	}
	masterConfiguration := defaultMasterConfig()
	agentConfiguration := defaultAgentConfig()
	if *values.masterConfigPath != "" {
		masterConfiguration, _, err = loadMasterConfig(*values.masterConfigPath)
		if err != nil {
			return masterConfig{}, agentConfig{}, err
		}
		agentConfiguration, _, err = loadAgentConfig(*values.agentConfigPath)
		if err != nil {
			return masterConfig{}, agentConfig{}, err
		}
	}
	visited := visitedFlags(flags)
	if *values.masterConfigPath == "" || visited["master-data-dir"] {
		masterConfiguration.DataDir = *values.masterDataDir
	}
	if *values.masterConfigPath == "" || visited["database-path"] {
		masterConfiguration.DatabasePath = *values.databasePath
	}
	if *values.masterConfigPath == "" || visited["agent-port"] {
		masterConfiguration.AgentPort = *values.agentPort
	}
	if *values.masterConfigPath == "" || visited["web-port"] {
		masterConfiguration.WebPort = *values.webPort
	}
	if *values.masterConfigPath == "" || visited["allow-insecure-http"] {
		masterConfiguration.AllowInsecureHTTP = *values.allowInsecureHTTP
	}
	if *values.agentConfigPath == "" || visited["agent-data-dir"] {
		agentConfiguration.DataDir = *values.agentDataDir
	}
	if *values.agentConfigPath == "" || visited["master"] {
		agentConfiguration.MasterAddress = *values.masterAddress
	}
	if *values.agentConfigPath == "" || visited["master-pubkey"] {
		agentConfiguration.MasterPublicKey = *values.masterPublicKey
	}
	if *values.agentConfigPath == "" || visited["heartbeat-interval"] {
		agentConfiguration.HeartbeatInterval = *values.heartbeatInterval
	}
	if *values.agentConfigPath == "" || visited["connections-interval"] {
		agentConfiguration.ConnectionsInterval = *values.connectionInterval
	}
	masterConfiguration, err = normalizeMasterConfig(masterConfiguration, cwd)
	if err != nil {
		return masterConfig{}, agentConfig{}, err
	}
	agentConfiguration, err = normalizeAgentConfig(agentConfiguration, cwd)
	if err != nil {
		return masterConfig{}, agentConfig{}, err
	}
	return masterConfiguration, agentConfiguration, nil
}

func serveCombined(ctx context.Context, masterConfiguration masterConfig, agentConfiguration agentConfig) error {
	heartbeat, connections, err := agentDurations(agentConfiguration)
	if err != nil {
		return err
	}
	pinnedMasterKey, err := parseMasterPubKey(agentConfiguration.MasterPublicKey)
	if err != nil {
		return err
	}
	store, err := control.OpenWithDatabase(masterConfiguration.DataDir, masterConfiguration.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, created, err := store.EnsureDefaultAdmin(ctx); err != nil {
		return err
	} else if created {
		fmt.Printf("Default administrator %s created; change the initial password after login.\n", control.DefaultAdminUsername)
	}
	server, err := control.NewServer(store, !masterConfiguration.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	if pinnedMasterKey != server.NoisePublicKey() {
		return errors.New("agent master_public_key does not match this master")
	}
	agentAddress := portAddress(masterConfiguration.AgentPort)
	agentListener, err := net.Listen("tcp", agentAddress)
	if err != nil {
		return fmt.Errorf("listen for agents: %w", err)
	}
	defer agentListener.Close()
	browserAddress := portAddress(masterConfiguration.WebPort)
	browserServer := &http.Server{
		Addr:              browserAddress,
		Handler:           server.BrowserHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	defer browserServer.Close()
	combinedContext, cancel := context.WithCancel(ctx)
	defer cancel()
	server.StartMaintenance(combinedContext)
	go func() {
		<-combinedContext.Done()
		_ = agentListener.Close()
		_ = browserServer.Close()
	}()
	warnIfNotRoot()
	errorsChannel := make(chan error, 3)
	go func() {
		fmt.Fprintln(os.Stdout, "polaris combined (master agent port) listening on", agentAddress)
		errorsChannel <- server.ServeAgents(combinedContext, agentListener)
	}()
	go func() {
		fmt.Fprintln(os.Stdout, "polaris combined (master web port) listening on", browserAddress)
		errorsChannel <- browserServer.ListenAndServe()
	}()
	go func() {
		errorsChannel <- runAgentLoop(combinedContext, agentConfiguration.DataDir, agentConfiguration.MasterAddress, pinnedMasterKey, heartbeat, connections, "", agentConfiguration.NginxPassthroughRoutes)
	}()
	select {
	case <-combinedContext.Done():
		return nil
	case runErr := <-errorsChannel:
		cancel()
		if errors.Is(runErr, http.ErrServerClosed) || combinedContext.Err() != nil {
			return nil
		}
		return runErr
	}
}
