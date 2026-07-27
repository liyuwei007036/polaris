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
	default:
		usage()
		err = errors.New("role must be master or agent")
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
		dataDir := flags.String("data-dir", "./data", "master data directory")
		email := flags.String("email", "", "administrator email")
		passwordStdin := flags.Bool("password-stdin", false, "read password from standard input")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *email == "" || !*passwordStdin {
			return errors.New("--email and --password-stdin are required")
		}
		password, err := readPassword(os.Stdin)
		if err != nil {
			return err
		}
		store, err := control.Open(*dataDir)
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
	case "show-pubkey":
		flags := flag.NewFlagSet("master show-pubkey", flag.ContinueOnError)
		dataDir := flags.String("data-dir", "./data", "master data directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		store, err := control.Open(*dataDir)
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
		dataDir := flags.String("data-dir", "./data", "master data directory")
		agentListen := flags.String("agent-listen", ":8443", "TCP listen address for agents (Noise-encrypted, no certificate needed)")
		browserListen := flags.String("browser-listen", ":8080", "plain HTTP listen address for the operator web UI/API; put a reverse proxy in front for public HTTPS")
		insecureCookies := flags.Bool("insecure-dev-cookies", false, "allow non-Secure cookies; only needed if the browser listener is reached over plain HTTP with no TLS-terminating proxy in front")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		store, err := control.Open(*dataDir)
		if err != nil {
			return err
		}
		defer store.Close()
		server, err := control.NewServer(store, !*insecureCookies)
		if err != nil {
			return err
		}
		agentListener, err := net.Listen("tcp", *agentListen)
		if err != nil {
			return fmt.Errorf("listen for agents: %w", err)
		}
		defer agentListener.Close()
		// Agent traffic (status, task dispatch, task results, connection
		// pushes) is Noise-encrypted raw TCP, never HTTP — see ServeAgents.
		// The browser listener is plain HTTP by design; put nginx (or any
		// reverse proxy) in front of it for public HTTPS, or leave it plain
		// for LAN-only access.
		browserServer := &http.Server{
			Addr:              *browserListen,
			Handler:           server.BrowserHandler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      0, // the real-time connections SSE stream is held open indefinitely
			IdleTimeout:       60 * time.Second,
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		go func() {
			<-ctx.Done()
			_ = agentListener.Close()
			_ = browserServer.Close()
		}()
		errs := make(chan error, 2)
		go func() {
			fmt.Fprintln(os.Stdout, "sb-control master (agent, Noise-encrypted TCP) listening on", *agentListen)
			errs <- server.ServeAgents(ctx, agentListener)
		}()
		go func() {
			fmt.Fprintln(os.Stdout, "sb-control master (browser, plain HTTP) listening on", *browserListen)
			errs <- browserServer.ListenAndServe()
		}()
		return <-errs
	default:
		return fmt.Errorf("unknown master command %q", args[0])
	}
}

func runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("agent command is required")
	}
	switch args[0] {
	case "register":
		return agentRegister(args[1:])
	case "run":
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
	dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
	masterAddr := flags.String("master", "", "master agent-listen address, host:port")
	masterPubKeyStr := flags.String("master-pubkey", "", "master's Noise public key, base64")
	token := flags.String("token", "", "one-time registration token")
	nodeName := flags.String("node-name", "", "node display name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *masterAddr == "" || *token == "" || *nodeName == "" {
		return errors.New("--master, --token and --node-name are required")
	}
	masterPub, err := parseMasterPubKey(*masterPubKeyStr)
	if err != nil {
		return err
	}
	keypair, err := agent.LoadOrCreateKeypair(*dataDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := agent.Connect(ctx, *masterAddr, keypair, masterPub)
	if err != nil {
		return err
	}
	defer conn.Close()
	ack, err := agent.Register(conn, *token, *nodeName, map[string]string{"os": runtime.GOOS, "architecture": runtime.GOARCH, "agent_version": "dev"})
	if err != nil {
		return err
	}
	switch ack.Status {
	case "approved":
		if err := agent.SaveReleaseSigningPublicKey(*dataDir, ack.ReleaseSigningPublicKeyPEM); err != nil {
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
	flags := flag.NewFlagSet("agent run", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
	masterAddr := flags.String("master", "", "master agent-listen address, host:port")
	masterPubKeyStr := flags.String("master-pubkey", "", "master's Noise public key, base64")
	interval := flags.Duration("heartbeat-interval", 30*time.Second, "agent heartbeat interval")
	connInterval := flags.Duration("connections-interval", 2*time.Second, "real-time connections push interval")
	singBoxVersion := flags.String("sing-box-version", "", "detected sing-box version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *masterAddr == "" {
		return errors.New("--master is required")
	}
	if *interval < 5*time.Second || *interval > 5*time.Minute {
		return errors.New("--heartbeat-interval must be between 5s and 5m")
	}
	if *connInterval < time.Second || *connInterval > 30*time.Second {
		return errors.New("--connections-interval must be between 1s and 30s")
	}
	masterPub, err := parseMasterPubKey(*masterPubKeyStr)
	if err != nil {
		return err
	}
	warnIfNotRoot()
	keypair, err := agent.LoadOrCreateKeypair(*dataDir)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	handler := agent.NewTaskHandler(*dataDir)

	backoff := 5 * time.Second
	const maxBackoff = 60 * time.Second
	for ctx.Err() == nil {
		conn, err := agent.Connect(ctx, *masterAddr, keypair, masterPub)
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
		if err := agent.SaveReleaseSigningPublicKey(*dataDir, ack.ReleaseSigningPublicKeyPEM); err != nil {
			conn.Close()
			return err
		}
		backoff = 5 * time.Second
		sessionErr := agent.RunSession(ctx, conn, handler, *interval, *connInterval, *singBoxVersion)
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
	fmt.Fprintln(os.Stderr, "usage: sb-control master <init-admin|serve|show-pubkey> ... | sb-control agent <register|run> ...")
}
