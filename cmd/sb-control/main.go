package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/agent"
	"github.com/sb-control/sb-control/internal/control"
	"github.com/sb-control/sb-control/internal/wire"
)

func main() {
	if len(os.Args) < 3 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "master":
		err = runMaster(os.Args[2:])
	case "agent":
		err = runAgent(os.Args[2:])
	case "combined":
		err = runCombined(os.Args[2:])
	default:
		usage()
		err = errors.New("role must be master, agent, or combined")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runMaster(args []string) error {
	if len(args) == 0 {
		return errors.New("master command is required")
	}
	switch args[0] {
	case "init-admin":
		flags := flag.NewFlagSet("master init-admin", flag.ContinueOnError)
		configurationFlags := addMasterFlags(flags)
		email := flags.String("email", "", "administrator email")
		passwordStdin := flags.Bool("password-stdin", false, "read password from standard input")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		configuration, err := resolveMasterFlags(flags, configurationFlags)
		if err != nil {
			return err
		}
		if *email == "" || !*passwordStdin {
			return errors.New("--email and --password-stdin are required")
		}
		password, err := readPassword(os.Stdin)
		if err != nil {
			return err
		}
		store, err := control.OpenWithDatabase(configuration.DataDir, configuration.DatabasePath)
		if err != nil {
			return err
		}
		defer store.Close()
		secret, err := store.CreateInitialAdmin(context.Background(), *email, password)
		if err != nil {
			return err
		}
		fmt.Println("Administrator created. Add this TOTP secret to an authenticator now; it is displayed only once:")
		fmt.Println(secret)
		return nil
	case "reset-mfa":
		flags := flag.NewFlagSet("master reset-mfa", flag.ContinueOnError)
		configurationFlags := addMasterFlags(flags)
		email := flags.String("email", "", "operator email")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		configuration, err := resolveMasterFlags(flags, configurationFlags)
		if err != nil {
			return err
		}
		if *email == "" {
			return errors.New("--email is required")
		}
		store, err := control.OpenWithDatabase(configuration.DataDir, configuration.DatabasePath)
		if err != nil {
			return err
		}
		defer store.Close()
		operators, err := store.ListOperators(context.Background())
		if err != nil {
			return err
		}
		for _, operator := range operators {
			if strings.EqualFold(operator.Email, *email) {
				secret, err := store.ResetOperatorTOTP(context.Background(), operator.ID)
				if err != nil {
					return err
				}
				fmt.Println("MFA reset. Add this TOTP secret to an authenticator now; it is displayed only once:")
				fmt.Println(secret)
				return nil
			}
		}
		return errors.New("operator not found")
	case "show-pubkey":
		flags := flag.NewFlagSet("master show-pubkey", flag.ContinueOnError)
		configurationFlags := addMasterFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		configuration, err := resolveMasterFlags(flags, configurationFlags)
		if err != nil {
			return err
		}
		store, err := control.OpenWithDatabase(configuration.DataDir, configuration.DatabasePath)
		if err != nil {
			return err
		}
		defer store.Close()
		server, err := control.NewServer(store, true)
		if err != nil {
			return err
		}
		public := server.NoisePublicKey()
		fmt.Println(base64.StdEncoding.EncodeToString(public[:]))
		return nil
	case "serve":
		flags := flag.NewFlagSet("master serve", flag.ContinueOnError)
		configurationFlags := addMasterFlags(flags)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		configuration, err := resolveMasterFlags(flags, configurationFlags)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return serveMaster(ctx, configuration)
	default:
		return fmt.Errorf("unknown master command %q", args[0])
	}
}

func serveMaster(ctx context.Context, configuration masterConfig) error {
	store, err := control.OpenWithDatabase(configuration.DataDir, configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	server, err := control.NewServer(store, !configuration.AllowInsecureHTTP)
	if err != nil {
		return err
	}
	agentAddress := portAddress(configuration.AgentPort)
	agentListener, err := net.Listen("tcp", agentAddress)
	if err != nil {
		return fmt.Errorf("listen for agents: %w", err)
	}
	defer agentListener.Close()
	browserAddress := portAddress(configuration.WebPort)
	browserServer := &http.Server{
		Addr:              browserAddress,
		Handler:           server.BrowserHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       60 * time.Second,
	}
	defer browserServer.Close()
	go func() {
		<-ctx.Done()
		_ = agentListener.Close()
		_ = browserServer.Close()
	}()
	errorsChannel := make(chan error, 2)
	go func() {
		fmt.Fprintln(os.Stdout, "sb-control master (agent, Noise-encrypted TCP) listening on", agentAddress)
		errorsChannel <- server.ServeAgents(ctx, agentListener)
	}()
	go func() {
		fmt.Fprintln(os.Stdout, "sb-control master (browser, plain HTTP) listening on", browserAddress)
		errorsChannel <- browserServer.ListenAndServe()
	}()
	runErr := <-errorsChannel
	if errors.Is(runErr, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return runErr
}

func runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("agent command is required")
	}
	switch args[0] {
	case "register":
		return agentRegister(args[1:])
	case "run", "serve":
		return runAgentControl(args[1:])
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func parseMasterPubKey(value string) ([wire.KeySize]byte, error) {
	var out [wire.KeySize]byte
	if value == "" {
		return out, errors.New("--master-pubkey is required (see \"sb-control master show-pubkey\")")
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return out, fmt.Errorf("decode --master-pubkey: %w", err)
	}
	if len(decoded) != wire.KeySize {
		return out, errors.New("--master-pubkey must decode to 32 bytes")
	}
	copy(out[:], decoded)
	return out, nil
}

func agentRegister(args []string) error {
	flags := flag.NewFlagSet("agent register", flag.ContinueOnError)
	configurationFlags := addAgentFlags(flags)
	token := flags.String("token", "", "one-time registration token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	configuration, err := resolveAgentFlags(flags, configurationFlags)
	if err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	masterPub, err := parseMasterPubKey(configuration.MasterPublicKey)
	if err != nil {
		return err
	}
	keypair, err := agent.LoadOrCreateKeypair(configuration.DataDir)
	if err != nil {
		return err
	}
	nodeName, err := os.Hostname()
	if err != nil || strings.TrimSpace(nodeName) == "" {
		return errors.New("read local hostname for agent registration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, configuration.MasterAddress, keypair, masterPub)
	if err != nil {
		return err
	}
	defer conn.Close()
	ack, err := agent.Register(conn, *token, nodeName, map[string]string{"os": runtime.GOOS, "architecture": runtime.GOARCH, "agent_version": "dev"})
	if err != nil {
		return err
	}
	switch ack.Status {
	case "approved":
		if err := agent.SaveReleaseSigningPublicKey(configuration.DataDir, ack.ReleaseSigningPublicKeyPEM); err != nil {
			return err
		}
		fmt.Printf("Node already approved (node_id=%s). Ready to run \"agent run\".\n", ack.NodeID)
	case "pending":
		fmt.Printf("Registration pending approval (registration_id=%s). Ask an administrator to approve it, then run \"agent run\" — it retries until approved.\n", ack.RegistrationID)
	default:
		return fmt.Errorf("registration %s", ack.Status)
	}
	return nil
}

func runAgentControl(args []string) error {
	flags := flag.NewFlagSet("agent serve", flag.ContinueOnError)
	configurationFlags := addAgentFlags(flags)
	if err := flags.Parse(args); err != nil {
		return err
	}
	configuration, err := resolveAgentFlags(flags, configurationFlags)
	if err != nil {
		return err
	}
	interval, connInterval, err := agentDurations(configuration)
	if err != nil {
		return err
	}
	masterPub, err := parseMasterPubKey(configuration.MasterPublicKey)
	if err != nil {
		return err
	}
	warnIfNotRoot()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return runAgentLoop(ctx, configuration.DataDir, configuration.MasterAddress, masterPub, interval, connInterval, "")
}

func runAgentLoop(ctx context.Context, dataDir, masterAddr string, masterPub [wire.KeySize]byte, interval, connInterval time.Duration, singBoxVersion string) error {
	keypair, err := agent.LoadOrCreateKeypair(dataDir)
	if err != nil {
		return err
	}
	handler := agent.NewTaskHandler(dataDir)
	backoff := 5 * time.Second
	const maxBackoff = 60 * time.Second
	for ctx.Err() == nil {
		conn, err := agent.Connect(ctx, masterAddr, keypair, masterPub)
		if err != nil {
			fmt.Fprintln(os.Stderr, "connect failed:", err)
			if !sleepContext(ctx, backoff) {
				return nil
			}
			continue
		}
		ack, err := agent.Register(conn, "", "", nil)
		if err != nil {
			conn.Close()
			fmt.Fprintln(os.Stderr, "registration check failed:", err)
			if !sleepContext(ctx, backoff) {
				return nil
			}
			continue
		}
		if ack.Status != "approved" {
			conn.Close()
			fmt.Fprintf(os.Stderr, "node not approved yet (status=%s); run \"agent register\" if you haven't, then keep this running — it retries automatically\n", ack.Status)
			if !sleepContext(ctx, backoff) {
				return nil
			}
			continue
		}
		if err := agent.SaveReleaseSigningPublicKey(dataDir, ack.ReleaseSigningPublicKeyPEM); err != nil {
			conn.Close()
			return err
		}
		backoff = 5 * time.Second
		sessionErr := agent.RunSession(ctx, conn, handler, interval, connInterval, singBoxVersion)
		conn.Close()
		if ctx.Err() != nil {
			return nil
		}
		if sessionErr != nil {
			fmt.Fprintln(os.Stderr, "session ended:", sessionErr)
		}
		if !sleepContext(ctx, backoff) {
			return nil
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
	return nil
}

// sleepContext waits for d or ctx cancellation, reporting which happened
// first so callers can stop their retry loop cleanly on shutdown.
func sleepContext(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func readPassword(reader io.Reader) (string, error) {
	value, err := io.ReadAll(bufio.NewReader(reader))
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(value), "\n"), "\r")
	if password == "" {
		return "", errors.New("password is empty")
	}
	return password, nil
}

// warnIfNotRoot logs a clear, upfront warning instead of leaving the operator
// to decode a bare "permission denied" the first time a task tries to write
// to /etc/sing-box, /etc/nginx, or /etc/fail2ban.
func warnIfNotRoot() {
	if runtime.GOOS == "linux" && os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "warning: agent is not running as root; tasks that write to /etc/sing-box, /etc/nginx, or /etc/fail2ban will fail with permission denied. Run via systemd with User=root (see deploy/sb-control-agent.service), or as root directly.")
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sb-control master <init-admin|reset-mfa|serve|show-pubkey> ... | sb-control agent <register|run> ... | sb-control combined <init-admin|reset-mfa|serve|show-pubkey> --config FILE ...")
}
