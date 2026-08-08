package wire

import (
	"bytes"
	"encoding/gob"
	"fmt"
)

// Message types carried in a Conn frame's header byte. All bodies are
// gob-encoded (Go's native binary serialization — not JSON, not text).
const (
	MsgRegister    byte = iota + 1 // agent -> master, pre-approval
	MsgRegisterAck                 // master -> agent, pre-approval or post-handshake status
	MsgStatus                      // agent -> master, heartbeat-cadence status/metrics
	MsgTask                        // master -> agent
	MsgTaskResult                  // agent -> master
	MsgConnections                 // agent -> master, fast-cadence real-time push
	MsgKeepalive                   // either direction
)

// RegisterRequest is sent by an agent whose public key the master does not
// yet recognize as an approved node.
type RegisterRequest struct {
	Token        string
	NodeName     string
	Capabilities map[string]string
}

// RegisterAck answers a RegisterRequest, or is sent unconditionally as the
// first message after every handshake to tell the agent whether it's already
// approved (Status == "approved") and can proceed straight to normal
// operation, or still "pending"/"rejected".
type RegisterAck struct {
	RegistrationID string
	NodeID         string
	Status         string // "approved" | "pending" | "rejected"
	// ReleaseSigningPublicKeyPEM is set only when Status == "approved" — a
	// distinct mechanism from node identity, used to verify signed sing-box
	// release manifests (see VerifyReleaseTask).
	ReleaseSigningPublicKeyPEM []byte
	// ConnectionsIntervalSeconds is how often the master wants this node to
	// push its connection list. Sending it on every handshake is what lets the
	// cadence be changed centrally: the agent adopts it in place of its own
	// configured value, so no node has to be edited or restarted by hand.
	// Zero means the master has no opinion and the agent keeps its own.
	ConnectionsIntervalSeconds uint32
}

// ConnectionInfo mirrors one entry from the local sing-box Clash API.
type ConnectionInfo struct {
	ID          string
	Inbound     string
	Network     string
	Source      string
	Destination string
	Host        string
	Upload      int64
	Download    int64
	StartedAt   string
	Outbound    string
	Rule        string
	RulePayload string
	Chains      []string
	// InboundTag is the sing-box inbound tag the connection entered through,
	// which the master resolves back to the listener that owns it.
	InboundTag string
	// User is the inbound account sing-box authenticated the connection as.
	User string
}

// Status is the heartbeat-cadence identity/build/metrics report (everything
// the old heartbeat bundled together, minus the connections list — that's
// ConnectionsPush now, on its own faster, independent cadence).
type Status struct {
	CollectedAt       string
	AgentVersion      string
	OS                string
	Architecture      string
	SingBoxVersion    string
	SingBoxConfigHash string
	NginxConfigHash   string
	Capabilities      map[string]string
	NodeReceivedBytes uint64
	NodeSentBytes     uint64
	HasNodeTotals     bool
	// Proxy counters come from sing-box itself and stay flat while nothing is
	// being proxied, unlike the host interface totals above.
	ProxyReceivedBytes uint64
	ProxySentBytes     uint64
	HasProxyTotals     bool
	HealthStatus       string
	HealthMessage     string
	SingBoxService    string
	ClashAPIAvailable bool
	TrafficAvailable  bool
	// Network protection is deliberately not part of the heartbeat: the console
	// asks a node for its firewall and jails at the moment it shows them, so it
	// can never present a rule that has since been changed on the server.
}

// ConnectionsPush is the fast-cadence, independent real-time connections
// report (agent pushes proactively; master never polls for it). It also
// carries host traffic counters and the instantaneous rate the agent derived
// from them: the agent is the only side sampling on a fixed interval, so it
// is the only side that can measure a rate rather than guess one.
type ConnectionsPush struct {
	CollectedAt       string
	Connections       []ConnectionInfo
	HasNodeTotals     bool
	NodeReceivedBytes uint64
	NodeSentBytes     uint64
	HasNodeRates      bool
	ReceivedBytesRate float64
	SentBytesRate     float64
}

// Task is the subset of the master's internal task record the agent needs to
// execute an instruction.
type Task struct {
	ID             string
	Kind           string
	IdempotencyKey string
	Payload        string
	ExpectedHash   string
}

// TaskResult is the agent's report of how a Task went.
type TaskResult struct {
	TaskID         string
	Status         string
	Summary        string
	SingBoxVersion string
	// Data carries a structured answer for tasks the master asks in order to
	// read something back — the live firewall and Fail2Ban state. Summary is a
	// short sentence for the operations log and is capped there; a query's
	// answer is far larger than that cap, so it travels separately and is
	// never stored.
	Data string
}

// Encode gob-encodes any of the message body types above.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, fmt.Errorf("wire: encode message: %w", err)
	}
	return buf.Bytes(), nil
}

// Decode gob-decodes into the given pointer.
func Decode(body []byte, v any) error {
	if err := gob.NewDecoder(bytes.NewReader(body)).Decode(v); err != nil {
		return fmt.Errorf("wire: decode message: %w", err)
	}
	return nil
}
