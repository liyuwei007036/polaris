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

	"github.com/liyuwei007036/polaris/internal/selfupdate"
	"github.com/liyuwei007036/polaris/internal/version"
	"github.com/liyuwei007036/polaris/internal/wire"
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
	CollectedAt string            `json:"collected_at"`
	Node        map[string]uint64 `json:"node,omitempty"`
	// Proxy holds sing-box's own cumulative byte counts. Node holds the host
	// interface totals, which include everything else running on the machine.
	Proxy        map[string]uint64           `json:"proxy,omitempty"`
	Capabilities map[string]MetricCapability `json:"capabilities"`
	Connections  []ConnectionInfo            `json:"connections,omitempty"`
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
	InboundTag  string   `json:"inbound_tag,omitempty"`
	// User is the inbound account sing-box authenticated the connection as,
	// which the master resolves back to the access user that owns it.
	User string `json:"user,omitempty"`
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
	// Banned carries the same addresses with the times Fail2Ban recorded for
	// them, which the plain status output does not include.
	Banned []Fail2BanBan `json:"banned,omitempty"`
	Error  string        `json:"error,omitempty"`
	// Managed distinguishes jails polaris wrote from ones already on the
	// host, which are reported for visibility but never modified.
	Managed bool `json:"managed"`
}

// Fail2BanBan is one currently banned address. Times are RFC 3339 in UTC so
// the console can render them in the visitor's own time zone; they are absent
// when the local Fail2Ban is too old to report them.
type Fail2BanBan struct {
	IP       string `json:"ip"`
	BannedAt string `json:"banned_at,omitempty"`
	UnbanAt  string `json:"unban_at,omitempty"`
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
	// Data is the structured answer to a task that asks the agent to read the
	// host back — the live firewall and Fail2Ban state. See wire.TaskResult.
	Data string `json:"data,omitempty"`
	// RestartAgent asks the session loop to re-execute the process after the
	// result has been reported (set by a successful agent.upgrade).
	RestartAgent bool `json:"-"`
}
type TaskHandler func(context.Context, Task) TaskResult

func DefaultStatus(singBoxVersion, dataDir string) Status {
	// The node adapts a configuration to the host before writing it, so the
	// file on disk hashes to something the control plane never sent. What is
	// reported is the hash it did send, and only while the adapted file is
	// still the one this node wrote. Falling back to the file itself covers a
	// node that has not applied anything yet.
	singBoxConfigHash := reportedSingBoxConfigurationHash(dataDir)
	if singBoxConfigHash == "" {
		singBoxConfigHash, _ = configurationFileHash(managedSingBoxConfig)
	}
	return Status{
		AgentVersion:      version.Version,
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
	// Report until told otherwise: a master too old to send WatchState never
	// switches this off, and going quiet on it would leave its console blank.
	streaming := true
	sendConnections := func() error {
		push := wire.ConnectionsPush{CollectedAt: time.Now().UTC().Format(time.RFC3339)}
		connections, _, err := CollectConnectionsAndTraffic(ctx)
		if err == nil {
			push.Connections = toWireConnections(connections)
		}
		body, encodeErr := wire.Encode(push)
		if encodeErr != nil {
			return encodeErr
		}
		return conn.WriteMessage(wire.MsgConnections, body)
	}

	if err := sendStatus(); err != nil {
		return err
	}
	// Report connections once straight away rather than only on the first tick.
	// The master hands out the cadence at handshake time, so a node that has
	// just connected would otherwise show no connections at all for a full
	// interval — reading as an idle server rather than an unreported one.
	if err := sendConnections(); err != nil {
		return err
	}

	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()
	// Connection pushes ride a wall-clock grid rather than a ticker started
	// whenever this session happened to connect. Every node in the fleet
	// therefore reports at the same instants, and the master can add their
	// rates together: readings taken over the same window sum to a fleet
	// total, readings taken over windows that only overlap by chance do not.
	// The timer is re-armed against the clock each round so it cannot drift
	// off the grid the way a free-running ticker would.
	connectionsTimer := time.NewTimer(untilNextTick(time.Now(), connectionsInterval))
	defer connectionsTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case msg := <-incoming:
			if msg.msgType == wire.MsgWatch {
				var state wire.WatchState
				if wire.Decode(msg.body, &state) != nil || state.Streaming == streaming {
					continue
				}
				streaming = state.Streaming
				if !streaming {
					continue
				}
				// Push straight away rather than waiting for the next tick,
				// so a console that just opened is not left blank for one
				// full interval. The master measures nothing from this first
				// push: its own previous sample is as old as the pause, which
				// it refuses to divide by.
				if err := sendConnections(); err != nil {
					return err
				}
				continue
			}
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
			resBody, err := wire.Encode(wire.TaskResult{TaskID: task.ID, Status: result.Status, Summary: result.Summary, SingBoxVersion: result.SingBoxVersion, Data: result.Data})
			if err != nil {
				return err
			}
			if err := conn.WriteMessage(wire.MsgTaskResult, resBody); err != nil {
				return err
			}
			if result.RestartAgent {
				// Give the kernel a moment to flush the result to the master
				// before exec closes the connection.
				time.Sleep(500 * time.Millisecond)
				if err := selfupdate.Restart(); err != nil {
					fmt.Fprintln(os.Stderr, "restart after agent upgrade failed:", err)
				}
			}
		case <-heartbeatTicker.C:
			if err := sendStatus(); err != nil {
				return err
			}
		case <-connectionsTimer.C:
			// The timer keeps running while paused so pushes stay on the grid
			// the moment they resume; skipping the work is what makes a fleet
			// nobody is watching cost nothing.
			connectionsTimer.Reset(untilNextTick(time.Now(), connectionsInterval))
			if !streaming {
				continue
			}
			if err := sendConnections(); err != nil {
				return err
			}
		}
	}
}

// untilNextTick reports how long to wait for the next instant that divides the
// wall clock evenly by interval. Nodes reach the same instants without
// exchanging anything as long as their clocks agree, which is already assumed
// elsewhere in this system. A node whose clock is off reports off the grid and
// simply lands in a neighbouring round; nothing breaks, its reading is just
// added to the total one beat late.
func untilNextTick(now time.Time, interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	remainder := time.Duration(now.UnixNano()) % interval
	if remainder == 0 {
		return interval
	}
	return interval - remainder
}

func detectSingBoxVersion(ctx context.Context) string {
	binaryPath := managedSystemPath("/usr/local/bin/sing-box")
	if strings.TrimSpace(os.Getenv("POLARIS_E2E_ROOT")) == "" {
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
	if local.Metrics.Proxy != nil {
		st.HasProxyTotals = true
		st.ProxyReceivedBytes = local.Metrics.Proxy["received_bytes"]
		st.ProxySentBytes = local.Metrics.Proxy["sent_bytes"]
	}
	return st
}

func toWireConnections(in []ConnectionInfo) []wire.ConnectionInfo {
	out := make([]wire.ConnectionInfo, 0, len(in))
	for _, c := range in {
		out = append(out, wire.ConnectionInfo{
			ID: c.ID, Inbound: c.Inbound, Network: c.Network, Source: c.Source, Destination: c.Destination,
			Host: c.Host, Upload: c.Upload, Download: c.Download, StartedAt: c.StartedAt, Outbound: c.Outbound,
			Rule: c.Rule, RulePayload: c.RulePayload, Chains: c.Chains, InboundTag: c.InboundTag, User: c.User,
		})
	}
	return out
}
