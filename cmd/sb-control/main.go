package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/agent"
	"github.com/sb-control/sb-control/internal/control"
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
	case "serve":
		flags := flag.NewFlagSet("master serve", flag.ContinueOnError)
		dataDir := flags.String("data-dir", "./data", "master data directory")
		listen := flags.String("listen", ":8443", "HTTPS listen address")
		certFile := flags.String("tls-cert", "", "HTTPS certificate PEM")
		keyFile := flags.String("tls-key", "", "HTTPS private key PEM")
		insecureCookies := flags.Bool("insecure-dev-cookies", false, "allow non-Secure cookies for local HTTP development only")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *certFile == "" || *keyFile == "" {
			return errors.New("--tls-cert and --tls-key are required; master does not start HTTP in production mode")
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
		tlsConfig, err := server.TLSConfig()
		if err != nil {
			return err
		}
		httpServer := &http.Server{
			Addr:              *listen,
			Handler:           server.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       60 * time.Second,
			TLSConfig:         tlsConfig,
		}
		fmt.Fprintln(os.Stdout, "sb-control master listening on", *listen)
		return httpServer.ListenAndServeTLS(*certFile, *keyFile)
	default:
		return fmt.Errorf("unknown master command %q", args[0])
	}
}

func runAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("agent command is required")
	}
	switch args[0] {
	case "create-csr":
		flags := flag.NewFlagSet("agent create-csr", flag.ContinueOnError)
		dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
		nodeName := flags.String("node-name", "", "node display name")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		csr, err := agent.CreateCSR(*dataDir, *nodeName)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(csr)
		return err
	case "register":
		return agentRegister(args[1:])
	case "fetch-certificate":
		return agentFetchCertificate(args[1:])
	case "run":
		return runAgentControl(args[1:])
	default:
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func runAgentControl(args []string) error {
	flags := flag.NewFlagSet("agent run", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
	masterURL := flags.String("master", "", "master HTTPS base URL")
	masterCA := flags.String("master-ca", "", "optional PEM CA for the master HTTPS certificate")
	interval := flags.Duration("heartbeat-interval", 30*time.Second, "agent heartbeat interval")
	singBoxVersion := flags.String("sing-box-version", "", "detected sing-box version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireHTTPS(*masterURL); err != nil {
		return err
	}
	if *interval < 5*time.Second || *interval > 5*time.Minute {
		return errors.New("--heartbeat-interval must be between 5s and 5m")
	}
	client, err := agent.NewMTLSClient(*dataDir, *masterCA)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	status := agent.DefaultStatus(*singBoxVersion)
	status.Capabilities["goos"] = runtime.GOOS
	if err := agent.SendHeartbeat(ctx, client, *masterURL, status); err != nil {
		return err
	}
	go func() { _ = agent.KeepControlChannel(ctx, client, *masterURL, agent.NewTaskHandler(*dataDir)) }()
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			status.Metrics = agent.CollectMetrics()
			if err := agent.SendHeartbeat(ctx, client, *masterURL, status); err != nil {
				fmt.Fprintln(os.Stderr, "heartbeat failed:", err)
			}
		}
	}
}

func agentRegister(args []string) error {
	flags := flag.NewFlagSet("agent register", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
	masterURL := flags.String("master", "", "master HTTPS base URL")
	token := flags.String("token", "", "one-time registration token")
	nodeName := flags.String("node-name", "", "node display name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireHTTPS(*masterURL); err != nil {
		return err
	}
	if *token == "" || *nodeName == "" {
		return errors.New("--token and --node-name are required")
	}
	csr, err := agent.CreateCSR(*dataDir, *nodeName)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{
		"token": *token, "node_name": *nodeName, "csr_pem": string(csr),
		"capabilities": map[string]any{"os": "unknown", "architecture": "unknown", "agent_version": "dev"},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(*masterURL, "/")+"/api/v1/agent/registrations", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("submit registration: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return readAPIError(response)
	}
	var result struct {
		RegistrationID string `json:"registration_id"`
		PollToken      string `json:"poll_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode registration response: %w", err)
	}
	fmt.Printf("Registration pending approval. Keep these values secret until the certificate is fetched:\nregistration_id=%s\npoll_token=%s\n", result.RegistrationID, result.PollToken)
	return nil
}

func agentFetchCertificate(args []string) error {
	flags := flag.NewFlagSet("agent fetch-certificate", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "./agent-data", "agent data directory")
	masterURL := flags.String("master", "", "master HTTPS base URL")
	registrationID := flags.String("registration-id", "", "registration ID")
	pollToken := flags.String("poll-token", "", "registration poll token")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if err := requireHTTPS(*masterURL); err != nil {
		return err
	}
	if *registrationID == "" || *pollToken == "" {
		return errors.New("--registration-id and --poll-token are required")
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(*masterURL, "/")+"/api/v1/agent/registrations/"+*registrationID, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Registration-Poll-Token", *pollToken)
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("fetch registration status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return readAPIError(response)
	}
	var result struct {
		Status                     string `json:"status"`
		CertificatePEM             string `json:"certificate_pem"`
		CAPEM                      string `json:"ca_pem"`
		ReleaseSigningPublicKeyPEM string `json:"release_signing_public_key_pem"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode registration status: %w", err)
	}
	if result.Status != "approved" {
		return fmt.Errorf("registration status is %s", result.Status)
	}
	if err := agent.SaveCertificate(*dataDir, []byte(result.CertificatePEM), []byte(result.CAPEM), []byte(result.ReleaseSigningPublicKeyPEM)); err != nil {
		return err
	}
	fmt.Println("Agent certificate saved.")
	return nil
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

func requireHTTPS(rawURL string) error {
	if !strings.HasPrefix(strings.ToLower(rawURL), "https://") {
		return errors.New("--master must use an https:// URL")
	}
	return nil
}

func readAPIError(response *http.Response) error {
	limited := io.LimitReader(response.Body, 4096)
	content, _ := io.ReadAll(limited)
	return fmt.Errorf("master returned %s: %s", response.Status, strings.TrimSpace(string(content)))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sb-control master <init-admin|serve> ... | sb-control agent <create-csr|register|fetch-certificate|run> ...")
}
