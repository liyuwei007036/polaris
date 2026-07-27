package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Status struct {
	AgentVersion string         `json:"agent_version"`
	OS           string         `json:"os"`
	Architecture string         `json:"architecture"`
	SingBox      string         `json:"sing_box_version"`
	Capabilities map[string]any `json:"capabilities"`
	Metrics      MetricReport   `json:"metrics"`
}

// MetricReport deliberately separates host-interface counters from
// protocol-level measurements. Unavailable fields remain absent instead of
// being estimated from unrelated traffic.
type MetricReport struct {
	CollectedAt  string                      `json:"collected_at"`
	Node         map[string]uint64           `json:"node,omitempty"`
	Capabilities map[string]MetricCapability `json:"capabilities"`
	Connections  []ConnectionInfo            `json:"connections,omitempty"`
	Fail2Ban     *Fail2BanReport             `json:"fail2ban,omitempty"`
}

// ConnectionInfo mirrors what the local sing-box Clash API reports. Fields the
// API does not provide stay empty; nothing is inferred.
type ConnectionInfo struct {
	ID          string `json:"id,omitempty"`
	Inbound     string `json:"inbound,omitempty"`
	Network     string `json:"network,omitempty"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination,omitempty"`
	Host        string `json:"host,omitempty"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
	StartedAt   string `json:"started_at,omitempty"`
	Outbound    string `json:"outbound,omitempty"`
	Rule        string `json:"rule,omitempty"`
}

type Fail2BanReport struct {
	Available bool                 `json:"available"`
	Jails     []Fail2BanJailStatus `json:"jails"`
}

type Fail2BanJailStatus struct {
	Name            string   `json:"name"`
	CurrentlyBanned string   `json:"currently_banned,omitempty"`
	TotalBanned     string   `json:"total_banned,omitempty"`
	BannedIPs       []string `json:"banned_ips,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type MetricCapability struct {
	CumulativeTraffic bool   `json:"cumulative_traffic"`
	InstantRate       bool   `json:"instant_rate"`
	ConnectionCount   bool   `json:"connection_count"`
	Source            string `json:"source"`
	Precision         string `json:"precision"`
}

type Task struct {
	ID             string `json:"id"`
	NodeID         string `json:"node_id"`
	Kind           string `json:"kind"`
	IdempotencyKey string `json:"idempotency_key"`
	Payload        string `json:"payload"`
	ExpectedHash   string `json:"expected_hash"`
}

type TaskResult struct {
	Status         string `json:"status"`
	Summary        string `json:"summary"`
	SingBoxVersion string `json:"sing_box_version,omitempty"`
}
type TaskHandler func(context.Context, Task) TaskResult

func DefaultStatus(singBoxVersion string) Status {
	return Status{
		AgentVersion: "dev",
		OS:           runtime.GOOS,
		Architecture: runtime.GOARCH,
		SingBox:      singBoxVersion,
		Capabilities: map[string]any{"systemd": runtime.GOOS == "linux", "control_channel": "https-ndjson"},
		Metrics:      CollectMetrics(),
	}
}

func NewMTLSClient(dataDir, masterCAPath string) (*http.Client, error) {
	certificate, err := tls.LoadX509KeyPair(filepath.Join(dataDir, "agent.crt"), filepath.Join(dataDir, privateKeyFile))
	if err != nil {
		return nil, fmt.Errorf("load agent certificate: %w", err)
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if masterCAPath != "" {
		masterCAPEM, err := os.ReadFile(masterCAPath)
		if err != nil {
			return nil, fmt.Errorf("read master HTTPS CA: %w", err)
		}
		if ok := roots.AppendCertsFromPEM(masterCAPEM); !ok {
			return nil, errors.New("master HTTPS CA does not contain a certificate")
		}
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
	}}}, nil
}

func SendHeartbeat(ctx context.Context, client *http.Client, masterURL string, status Status) error {
	body, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+"/api/v1/agent/heartbeat", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send agent heartbeat: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiError(response)
	}
	return nil
}

// KeepControlChannel reconnects the agent-owned mTLS stream. The master emits
// only structured events on this stream; no shell input is accepted here.
func KeepControlChannel(ctx context.Context, client *http.Client, masterURL string, handler TaskHandler) error {
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(masterURL, "/")+"/api/v1/agent/control", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			if response.StatusCode == http.StatusOK {
				err = consumeControl(ctx, client, masterURL, response.Body, handler)
			} else {
				err = apiError(response)
			}
			response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
	}
}

func consumeControl(ctx context.Context, client *http.Client, masterURL string, body io.Reader, handler TaskHandler) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
			Task Task   `json:"task"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("decode control event: %w", err)
		}
		if event.Type != "task" {
			continue
		}
		result := TaskResult{Status: "failed", Summary: "agent has no handler for this task"}
		if handler != nil {
			result = handler(ctx, event.Task)
		}
		if err := submitTaskResult(ctx, client, masterURL, event.Task.ID, result); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func submitTaskResult(ctx context.Context, client *http.Client, masterURL, taskID string, result TaskResult) error {
	if result.Status != "succeeded" && result.Status != "failed" && result.Status != "rolled_back" {
		return errors.New("invalid task result status")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+"/api/v1/agent/tasks/"+taskID+"/result", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("submit task result: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return apiError(response)
	}
	return nil
}

func apiError(response *http.Response) error {
	content, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("master returned %s: %s", response.Status, strings.TrimSpace(string(content)))
}
