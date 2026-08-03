package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/sb-control/sb-control/internal/wire"
)

type Status struct {
	AgentVersion      string
	OS                string
	Architecture      string
	SingBox           string
	SingBoxConfigHash string
	NginxConfigHash   string
	Capabilities      map[string]any
	Metrics           MetricReport
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
	Health       NodeHealth                  `json:"health"`
}

type NodeHealth struct {
	Status            string `json:"status"`
	SingBoxService    string `json:"sing_box_service"`
	ClashAPIAvailable bool   `json:"clash_api_available"`
	TrafficAvailable  bool   `json:"traffic_available"`
	Message           string `json:"message,omitempty"`
}

// ConnectionInfo mirrors what the local sing-box Clash API reports. Fields the
// API does not provide stay empty; nothing is inferred.
type ConnectionInfo struct {
	ID          string   `json:"id,omitempty"`
	Inbound     string   `json:"inbound,omitempty"`
	Network     string   `json:"network,omitempty"`
	Source      string   `json:"source,omitempty"`
	Destination string   `json:"destination,omitempty"`
	Host        string   `json:"host,omitempty"`
	Upload      int64    `json:"upload"`
	Download    int64    `json:"download"`
	StartedAt   string   `json:"started_at,omitempty"`
	Outbound    string   `json:"outbound,omitempty"`
	Rule        string   `json:"rule,omitempty"`
	RulePayload string   `json:"rule_payload,omitempty"`
	Chains      []string `json:"chains,omitempty"`
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

func DefaultStatus(singBoxVersion, dataDir string) Status {
	singBoxConfigHash, _ := configurationFileHash(managedSingBoxConfig)
	return Status{
		AgentVersion:      "dev",
		OS:                runtime.GOOS,
		Architecture:      runtime.GOARCH,
		SingBox:           singBoxVersion,
		SingBoxConfigHash: singBoxConfigHash,
		NginxConfigHash:   reportedNginxConfigurationHash(dataDir),
		Capabilities: map[string]any{
			"systemd": runtime.GOOS == "linux", "control_channel": "noise-tcp", "configuration_hashes": true,
		},
		Metrics: CollectMetrics(),
	}
}

// Connect dials the master's agent port and completes the Noise_XK
// handshake. The agent is the initiator and must already know the master's
// static public key (configured out of band by the operator) — the
// WireGuard-style replacement for pinning a CA certificate.
func Connect(ctx context.Context, masterAddr string, local wire.Keypair, masterPub [wire.KeySize]byte) (*wire.Conn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", masterAddr)
	if err != nil {
		return nil, fmt.Errorf("dial master: %w", err)
	}
	conn, err := wire.DialXK(raw, local, masterPub)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("handshake with master: %w", err)
	}
	return conn, nil
}

// Register sends the one registration/status-check message every connection
// starts with, and returns the master's answer. token is required only the
// first time (before the node has been approved); an already-approved agent
// can pass an empty token — the master recognizes it by its already-pinned
// public key regardless.
func Register(conn *wire.Conn, token, nodeName string, capabilities map[string]string) (wire.RegisterAck, error) {
	body, err := wire.Encode(wire.RegisterRequest{Token: token, NodeName: nodeName, Capabilities: capabilities})
	if err != nil {
		return wire.RegisterAck{}, err
	}
	if err := conn.WriteMessage(wire.MsgRegister, body); err != nil {
		return wire.RegisterAck{}, fmt.Errorf("send registration: %w", err)
	}
	msgType, respBody, err := conn.ReadMessage()
	if err != nil {
		return wire.RegisterAck{}, fmt.Errorf("read registration ack: %w", err)
	}
	if msgType != wire.MsgRegisterAck {
		return wire.RegisterAck{}, errors.New("unexpected response to registration")
	}
	var ack wire.RegisterAck
	if err := wire.Decode(respBody, &ack); err != nil {
		return wire.RegisterAck{}, err
	}
	return ack, nil
}

// RunSession is the long-lived per-connection loop for an approved node: it
// reports Status and pushes Connections on their own independent cadences,
// executes Task messages the master pushes, and reports results back — all
// multiplexed over the one Noise connection. It returns when ctx is
// canceled or the connection fails (the caller reconnects with backoff).
func RunSession(ctx context.Context, conn *wire.Conn, handler TaskHandler, heartbeatInterval, connectionsInterval time.Duration, singBoxVersion, dataDir string) error {
	if singBoxVersion == "" {
		singBoxVersion = detectSingBoxVersion(ctx)
	}
	incoming := make(chan wireInboundMessage, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			msgType, body, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case incoming <- wireInboundMessage{msgType, body}:
			case <-ctx.Done():
				return
			}
		}
	}()

	sendStatus := func() error {
		body, err := wire.Encode(toWireStatus(DefaultStatus(singBoxVersion, dataDir)))
		if err != nil {
			return err
		}
		return conn.WriteMessage(wire.MsgStatus, body)
	}
	sendConnections := func() error {
		connections, err := CollectConnections(ctx)
		if err != nil {
			return nil // best-effort, same as the heartbeat path always was
		}
		body, err := wire.Encode(wire.ConnectionsPush{CollectedAt: time.Now().UTC().Format(time.RFC3339), Connections: toWireConnections(connections)})
		if err != nil {
			return err
		}
		return conn.WriteMessage(wire.MsgConnections, body)
	}

	if err := sendStatus(); err != nil {
		return err
	}

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()
	connectionsTicker := time.NewTicker(connectionsInterval)
	defer connectionsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case msg := <-incoming:
			if msg.msgType != wire.MsgTask {
				continue // keepalive or anything else: nothing to do
			}
			var task wire.Task
			if err := wire.Decode(msg.body, &task); err != nil {
				continue
			}
			result := TaskResult{Status: "failed", Summary: "agent has no handler for this task"}
			if handler != nil {
				result = handler(ctx, Task{ID: task.ID, Kind: task.Kind, IdempotencyKey: task.IdempotencyKey, Payload: task.Payload, ExpectedHash: task.ExpectedHash})
			}
			if result.Status == "succeeded" && result.SingBoxVersion != "" {
				singBoxVersion = result.SingBoxVersion
			}
			resBody, err := wire.Encode(wire.TaskResult{TaskID: task.ID, Status: result.Status, Summary: result.Summary, SingBoxVersion: result.SingBoxVersion})
			if err != nil {
				return err
			}
			if err := conn.WriteMessage(wire.MsgTaskResult, resBody); err != nil {
				return err
			}
		case <-heartbeatTicker.C:
			if err := sendStatus(); err != nil {
				return err
			}
		case <-connectionsTicker.C:
			if err := sendConnections(); err != nil {
				return err
			}
		}
	}
}

func detectSingBoxVersion(ctx context.Context) string {
	binaryPath := managedSystemPath("/usr/local/bin/sing-box")
	if strings.TrimSpace(os.Getenv("SB_CONTROL_E2E_ROOT")) == "" {
		if discovered, err := exec.LookPath("sing-box"); err == nil {
			binaryPath = discovered
		}
	}
	output, err := exec.CommandContext(ctx, binaryPath, "version").CombinedOutput()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(output))
	for index, field := range fields {
		if field == "version" && index+1 < len(fields) {
			return fields[index+1]
		}
	}
	return ""
}

type wireInboundMessage struct {
	msgType byte
	body    []byte
}

func toWireStatus(local Status) wire.Status {
	caps := make(map[string]string, len(local.Capabilities))
	for k, v := range local.Capabilities {
		caps[k] = fmt.Sprint(v)
	}
	st := wire.Status{
		CollectedAt:       local.Metrics.CollectedAt,
		AgentVersion:      local.AgentVersion,
		OS:                local.OS,
		Architecture:      local.Architecture,
		SingBoxVersion:    local.SingBox,
		SingBoxConfigHash: local.SingBoxConfigHash,
		NginxConfigHash:   local.NginxConfigHash,
		Capabilities:      caps,
		HealthStatus:      local.Metrics.Health.Status,
		HealthMessage:     local.Metrics.Health.Message,
		SingBoxService:    local.Metrics.Health.SingBoxService,
		ClashAPIAvailable: local.Metrics.Health.ClashAPIAvailable,
		TrafficAvailable:  local.Metrics.Health.TrafficAvailable,
	}
	if local.Metrics.Node != nil {
		st.HasNodeTotals = true
		st.NodeReceivedBytes = local.Metrics.Node["received_bytes"]
		st.NodeSentBytes = local.Metrics.Node["sent_bytes"]
	}
	if local.Metrics.Fail2Ban != nil {
		st.Fail2BanAvailable = local.Metrics.Fail2Ban.Available
		for _, j := range local.Metrics.Fail2Ban.Jails {
			st.Fail2BanJails = append(st.Fail2BanJails, wire.Fail2BanJailStatus{
				Name: j.Name, CurrentlyBanned: j.CurrentlyBanned, TotalBanned: j.TotalBanned, BannedIPs: j.BannedIPs, Error: j.Error,
			})
		}
	}
	return st
}

func toWireConnections(in []ConnectionInfo) []wire.ConnectionInfo {
	out := make([]wire.ConnectionInfo, 0, len(in))
	for _, c := range in {
		out = append(out, wire.ConnectionInfo{
			ID: c.ID, Inbound: c.Inbound, Network: c.Network, Source: c.Source, Destination: c.Destination,
			Host: c.Host, Upload: c.Upload, Download: c.Download, StartedAt: c.StartedAt, Outbound: c.Outbound,
			Rule: c.Rule, RulePayload: c.RulePayload, Chains: c.Chains,
		})
	}
	return out
}
