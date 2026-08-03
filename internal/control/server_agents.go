package control

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"time"

	"github.com/sb-control/sb-control/internal/wire"
)

// ServeAgents accepts raw TCP connections from agents and runs the Noise_XK
// handshake + binary session protocol on each — there is no HTTP anywhere in
// this path. It returns when ln.Accept fails permanently (including on
// context cancellation via the listener being closed by the caller).
func (s *Server) ServeAgents(ctx context.Context, ln net.Listener) error {
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleAgentConn(ctx, raw)
	}
}

// handleAgentConn processes one raw TCP connection end to end. The protocol
// after the handshake is always the same from the agent's point of view: it
// sends exactly one Register message (Token is blank once already approved —
// see the agent side), and gets back exactly one RegisterAck. If that ack
// says "approved", both sides fall straight through into the same
// connection's normal session loop; any other status ends the connection
// and the agent's retry loop reconnects later.
func (s *Server) handleAgentConn(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	// Bound the handshake+register step so a slow or malicious peer can't
	// hold a goroutine open indefinitely; cleared once the session starts.
	_ = raw.SetDeadline(time.Now().Add(15 * time.Second))
	conn, peerPub, err := wire.AcceptXK(raw, s.noiseKeypair)
	if err != nil {
		// Handshake failed (unknown/wrong key, tampered message, etc). There
		// is nothing more to do — and nothing more to send, since the peer
		// isn't authenticated — so just drop the connection.
		return
	}
	msgType, body, err := conn.ReadMessage()
	if err != nil || msgType != wire.MsgRegister {
		return
	}
	var req wire.RegisterRequest
	if err := wire.Decode(body, &req); err != nil {
		return
	}

	node, err := s.store.NodeForPublicKey(ctx, peerPub[:])
	if err != nil {
		s.handleRegistrationAttempt(ctx, conn, peerPub, req)
		return
	}
	releaseKeyPEM, err := s.store.ReleaseSigningPublicKeyPEM()
	if err != nil {
		return
	}
	ackBody, err := wire.Encode(wire.RegisterAck{NodeID: node.ID, Status: "approved", ReleaseSigningPublicKeyPEM: releaseKeyPEM})
	if err != nil {
		return
	}
	if err := conn.WriteMessage(wire.MsgRegisterAck, ackBody); err != nil {
		return
	}
	_ = raw.SetDeadline(time.Time{})
	s.runAgentSession(ctx, conn, node)
}

// handleRegistrationAttempt handles a connection from a public key that is
// not (yet) an approved node: its Register message either creates a new
// pending registration or reports the status of one already created by an
// earlier retry (see Store.RegisterAgent's idempotency-by-public-key).
// Either way the connection ends here — the agent's retry loop reconnects
// later, and once approved that reconnect is recognized directly above.
func (s *Server) handleRegistrationAttempt(ctx context.Context, conn *wire.Conn, peerPub [wire.KeySize]byte, req wire.RegisterRequest) {
	capsJSON, err := json.Marshal(req.Capabilities)
	if err != nil {
		return
	}
	reg, err := s.store.RegisterAgent(ctx, RegistrationInput{
		Token: req.Token, NodeName: req.NodeName, PublicKey: peerPub[:], Capabilities: string(capsJSON),
	})
	if err != nil {
		ackBody, encErr := wire.Encode(wire.RegisterAck{Status: "rejected"})
		if encErr == nil {
			_ = conn.WriteMessage(wire.MsgRegisterAck, ackBody)
		}
		return
	}
	ackBody, err := wire.Encode(wire.RegisterAck{RegistrationID: reg.ID, NodeID: reg.NodeID, Status: reg.Status})
	if err != nil {
		return
	}
	_ = conn.WriteMessage(wire.MsgRegisterAck, ackBody)
}

type agentInboundMessage struct {
	msgType byte
	body    []byte
}

// runAgentSession is the long-lived per-connection loop for an approved
// node: it multiplexes incoming Status/TaskResult/Connections messages with
// outgoing Task dispatches and periodic keepalives, all over the one Noise
// connection. This replaces the old controlSession NDJSON stream plus the
// three separate HTTP endpoints (heartbeat, connections push, task result).
func (s *Server) runAgentSession(ctx context.Context, conn *wire.Conn, node Node) {
	session := &controlSession{done: make(chan struct{}), tasks: make(chan Task, 16)}
	s.controlMu.Lock()
	if previous := s.controls[node.ID]; previous != nil {
		close(previous.done)
	}
	s.controls[node.ID] = session
	s.controlMu.Unlock()
	defer func() {
		s.controlMu.Lock()
		if s.controls[node.ID] == session {
			delete(s.controls, node.ID)
		}
		s.controlMu.Unlock()
	}()

	incoming := make(chan agentInboundMessage, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			msgType, body, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case incoming <- agentInboundMessage{msgType, body}:
			case <-ctx.Done():
				return
			}
		}
	}()

	pending, err := s.store.PendingTasks(ctx, node.ID)
	if err == nil {
		for _, task := range pending {
			if !s.sendTask(conn, node.ID, task) {
				return
			}
		}
	}

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-session.done:
			return
		case <-readErr:
			return
		case msg := <-incoming:
			if !s.handleAgentMessage(ctx, node, msg.msgType, msg.body) {
				return
			}
		case task := <-session.tasks:
			if !s.sendTask(conn, node.ID, task) {
				return
			}
		case <-ticker.C:
			if err := conn.WriteMessage(wire.MsgKeepalive, nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) sendTask(conn *wire.Conn, nodeID string, task Task) bool {
	payload := task.Payload
	if task.Kind == "outbound.test" {
		var reference struct {
			OutboundID string `json:"outbound_id"`
		}
		if json.Unmarshal([]byte(task.Payload), &reference) != nil {
			return false
		}
		outbound, err := s.store.outboundForTest(context.Background(), reference.OutboundID)
		if err != nil {
			_ = s.store.CompleteTask(context.Background(), task.ID, nodeID, "failed", err.Error())
			s.liveHub.publish(liveEvent{Kind: "task", NodeID: nodeID, TaskID: task.ID})
			return true
		}
		encoded, err := json.Marshal(map[string]any{
			"type": outbound.Type, "server": outbound.Server, "server_port": outbound.ServerPort,
			"username": outbound.Username, "password": outbound.Password,
		})
		if err != nil {
			return false
		}
		payload = string(encoded)
	}
	body, err := wire.Encode(wire.Task{ID: task.ID, Kind: task.Kind, IdempotencyKey: task.IdempotencyKey, Payload: payload, ExpectedHash: task.ExpectedHash})
	if err != nil {
		return false
	}
	if err := conn.WriteMessage(wire.MsgTask, body); err != nil {
		return false
	}
	_ = s.store.MarkTaskDispatched(context.Background(), task.ID, nodeID)
	return true
}

func (s *Server) handleAgentMessage(ctx context.Context, node Node, msgType byte, body []byte) bool {
	switch msgType {
	case wire.MsgStatus:
		var st wire.Status
		if err := wire.Decode(body, &st); err != nil {
			return false
		}
		capsJSON, err := json.Marshal(st.Capabilities)
		if err != nil {
			return false
		}
		if err := s.store.UpdateNodeIdentity(ctx, node.ID, st.AgentVersion, st.OS, st.Architecture, st.SingBoxVersion, string(capsJSON)); err != nil {
			return false
		}
		s.maybeInstallSingBox(ctx, node.ID, st.OS, st.Architecture, st.SingBoxVersion)
		if err := s.mergeNodeMetrics(ctx, node.ID, func(m *storedMetrics) {
			m.CollectedAt = st.CollectedAt
			if st.HasNodeTotals {
				m.Node = map[string]uint64{"received_bytes": st.NodeReceivedBytes, "sent_bytes": st.NodeSentBytes}
			}
			m.Health = &storedHealth{
				Status: st.HealthStatus, Message: st.HealthMessage, SingBoxService: st.SingBoxService,
				ClashAPIAvailable: st.ClashAPIAvailable, TrafficAvailable: st.TrafficAvailable,
			}
			if st.Fail2BanAvailable || len(st.Fail2BanJails) > 0 {
				m.Fail2Ban = &storedFail2Ban{Available: st.Fail2BanAvailable, Jails: convertFail2BanJails(st.Fail2BanJails)}
			}
		}); err != nil {
			return false
		}
		s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	case wire.MsgTaskResult:
		var res wire.TaskResult
		if err := wire.Decode(body, &res); err != nil {
			return false
		}
		task, err := s.store.TaskByID(ctx, res.TaskID)
		if err != nil {
			return false
		}
		if err := s.store.CompleteTask(ctx, task.ID, node.ID, res.Status, res.Summary); err != nil {
			return false
		}
		s.liveHub.publish(liveEvent{Kind: "task", NodeID: node.ID, TaskID: task.ID})
		if res.Status == "succeeded" && res.SingBoxVersion != "" {
			if err := s.store.SetNodeSingBoxVersion(ctx, node.ID, res.SingBoxVersion); err != nil {
				return false
			}
		}
		if task.OperatorID != "" {
			_ = s.store.AppendAudit(ctx, task.OperatorID, "task.completed."+res.Status, "task", task.ID, "agent task completed: "+task.Kind)
		}
	case wire.MsgConnections:
		var push wire.ConnectionsPush
		if err := wire.Decode(body, &push); err != nil {
			return false
		}
		connections := s.convertConnections(push.Connections)
		connJSON, err := json.Marshal(connections)
		if err != nil {
			return false
		}
		s.connHub.update(nodeConnectionsSnapshot{NodeID: node.ID, CollectedAt: push.CollectedAt, Connections: connJSON})
		if err := s.mergeNodeMetrics(ctx, node.ID, func(m *storedMetrics) {
			m.Connections = connections
		}); err != nil {
			return false
		}
		s.liveHub.publish(liveEvent{Kind: "connections", NodeID: node.ID})
	case wire.MsgKeepalive:
		// no-op
	default:
		return false
	}
	return true
}

func (s *Server) maybeInstallSingBox(ctx context.Context, nodeID, operatingSystem, architecture, version string) {
	if version != "" || operatingSystem != "linux" || (architecture != "amd64" && architecture != "arm64") {
		return
	}
	s.autoInstallMu.Lock()
	if s.autoInstallChecked == nil {
		s.autoInstallChecked = make(map[string]bool)
	}
	if s.autoInstallChecked[nodeID] {
		s.autoInstallMu.Unlock()
		return
	}
	s.autoInstallChecked[nodeID] = true
	s.autoInstallMu.Unlock()

	go func() {
		installContext, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := s.scheduleAutomaticSingBoxInstall(installContext, nodeID, architecture); err != nil {
			log.Printf("automatic sing-box installation for node %s was not queued: %v", nodeID, err)
		}
	}()
}

func (s *Server) scheduleAutomaticSingBoxInstall(ctx context.Context, nodeID, architecture string) (*Task, error) {
	attempted, err := s.store.HasSingBoxInstallAttempt(ctx, nodeID)
	if err != nil || attempted {
		return nil, err
	}
	resolver := s.latestSingBoxReleaseFn
	if resolver == nil {
		resolver = LatestOfficialSingBoxRelease
	}
	release, err := resolver(ctx, architecture)
	if err != nil {
		return nil, err
	}
	payload, err := s.store.SignedSingBoxReleasePayload(release)
	if err != nil {
		return nil, err
	}
	task, err := s.DispatchTask(ctx, Task{
		NodeID: nodeID, Kind: "singbox.install", IdempotencyKey: "singbox-" + release.Version + "-" + release.SHA256,
		Payload: payload, ExpectedHash: release.SHA256,
	})
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// storedMetrics mirrors the JSON shape the dashboard/connections API already
// expect from node_metrics.report (previously produced by the agent package
// directly); it's assembled here from whichever wire message just arrived,
// merged with whatever was already stored so a Status update doesn't erase
// the last-known connections and vice versa.
type storedMetrics struct {
	CollectedAt string             `json:"collected_at"`
	Node        map[string]uint64  `json:"node,omitempty"`
	Connections []storedConnection `json:"connections,omitempty"`
	Fail2Ban    *storedFail2Ban    `json:"fail2ban,omitempty"`
	Health      *storedHealth      `json:"health,omitempty"`
}

type storedHealth struct {
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	SingBoxService    string `json:"sing_box_service"`
	ClashAPIAvailable bool   `json:"clash_api_available"`
	TrafficAvailable  bool   `json:"traffic_available"`
}

type storedConnection struct {
	ID             string   `json:"id,omitempty"`
	Inbound        string   `json:"inbound,omitempty"`
	Network        string   `json:"network,omitempty"`
	Source         string   `json:"source,omitempty"`
	SourceIP       string   `json:"source_ip,omitempty"`
	SourceLocation string   `json:"source_location,omitempty"`
	Destination    string   `json:"destination,omitempty"`
	Host           string   `json:"host,omitempty"`
	Upload         int64    `json:"upload"`
	Download       int64    `json:"download"`
	StartedAt      string   `json:"started_at,omitempty"`
	Outbound       string   `json:"outbound,omitempty"`
	Rule           string   `json:"rule,omitempty"`
	RulePayload    string   `json:"rule_payload,omitempty"`
	Chains         []string `json:"chains,omitempty"`
}

type storedFail2BanJail struct {
	Name            string   `json:"name"`
	CurrentlyBanned string   `json:"currently_banned,omitempty"`
	TotalBanned     string   `json:"total_banned,omitempty"`
	BannedIPs       []string `json:"banned_ips,omitempty"`
	Error           string   `json:"error,omitempty"`
}

type storedFail2Ban struct {
	Available bool                 `json:"available"`
	Jails     []storedFail2BanJail `json:"jails"`
}

func (s *Server) convertConnections(in []wire.ConnectionInfo) []storedConnection {
	out := make([]storedConnection, 0, len(in))
	for _, c := range in {
		ip := addressIP(c.Source)
		out = append(out, storedConnection{
			ID: c.ID, Inbound: c.Inbound, Network: c.Network, Source: c.Source, Destination: c.Destination,
			SourceIP: ip, SourceLocation: s.ipLocator.Locate(ip), Host: c.Host, Upload: c.Upload, Download: c.Download,
			StartedAt: c.StartedAt, Outbound: c.Outbound, Rule: c.Rule, RulePayload: c.RulePayload, Chains: c.Chains,
		})
	}
	return out
}

func convertFail2BanJails(in []wire.Fail2BanJailStatus) []storedFail2BanJail {
	out := make([]storedFail2BanJail, 0, len(in))
	for _, j := range in {
		out = append(out, storedFail2BanJail{Name: j.Name, CurrentlyBanned: j.CurrentlyBanned, TotalBanned: j.TotalBanned, BannedIPs: j.BannedIPs, Error: j.Error})
	}
	return out
}

func (s *Server) mergeNodeMetrics(ctx context.Context, nodeID string, mutate func(*storedMetrics)) error {
	var current storedMetrics
	if existing, _, err := s.store.NodeMetrics(ctx, nodeID); err == nil {
		_ = json.Unmarshal(existing, &current)
	}
	mutate(&current)
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	return s.store.UpdateNodeMetrics(ctx, nodeID, string(encoded))
}
