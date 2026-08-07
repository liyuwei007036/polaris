package control

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
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
	// The address the agent dialled in from is the one clients can reach it
	// on, so a node is usable as soon as it is approved. It is only used when
	// nobody has entered an address by hand.
	if address := connectingAddress(raw); address != "" {
		if err := s.store.SetNodeObservedAddress(ctx, node.ID, address); err != nil {
			log.Printf("record observed address for node %s: %v", node.ID, err)
		}
	}
	_ = raw.SetDeadline(time.Time{})
	s.runAgentSession(ctx, conn, node)
}

// connectingAddress reports the agent's source IP, skipping loopback and
// unspecified addresses, which tell a client nothing about how to reach it.
func connectingAddress(raw net.Conn) string {
	host, _, err := net.SplitHostPort(raw.RemoteAddr().String())
	if err != nil {
		return ""
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return ""
	}
	return ip.String()
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
		if st.Capabilities["configuration_hashes"] == "true" {
			s.reconcileNodeDesiredState(ctx, node.ID, st.SingBoxVersion, st.SingBoxConfigHash, st.NginxConfigHash)
		}
		if err := s.mergeNodeMetrics(ctx, node.ID, func(m *storedMetrics) {
			m.CollectedAt = st.CollectedAt
			// Connections are real-time only and live in connectionsHub; drop
			// any list an older build persisted so nothing stale is served.
			m.Connections = nil
			if st.HasNodeTotals {
				m.Node = map[string]uint64{"received_bytes": st.NodeReceivedBytes, "sent_bytes": st.NodeSentBytes}
			}
			if st.HasProxyTotals {
				m.Proxy = map[string]uint64{"received_bytes": st.ProxyReceivedBytes, "sent_bytes": st.ProxySentBytes}
			}
			m.Health = &storedHealth{
				Status: st.HealthStatus, Message: st.HealthMessage, SingBoxService: st.SingBoxService,
				ClashAPIAvailable: st.ClashAPIAvailable, TrafficAvailable: st.TrafficAvailable,
			}
			if st.Fail2BanAvailable || len(st.Fail2BanJails) > 0 {
				m.Fail2Ban = &storedFail2Ban{Available: st.Fail2BanAvailable, Jails: convertFail2BanJails(st.Fail2BanJails)}
			}
			if st.FirewallAvailable {
				m.Firewall = &storedFirewall{
					Available: st.FirewallAvailable, Tool: st.FirewallTool, Truncated: st.FirewallTruncated,
					Error: st.FirewallError, Rules: convertFirewallRules(st.FirewallRules),
				}
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
		connections := s.convertConnections(ctx, node.ID, push.Connections)
		connJSON, err := json.Marshal(connections)
		if err != nil {
			return false
		}
		// Real-time state stays in memory and reaches browsers over SSE. It is
		// deliberately not persisted: this arrives every couple of seconds per
		// node, and a SQLite read-modify-write of the whole metrics blob at
		// that cadence was the single largest source of load on the master.
		s.connHub.update(nodeConnectionsSnapshot{
			NodeID: node.ID, CollectedAt: push.CollectedAt, Connections: connJSON, ConnectionCount: len(connections),
			HasTotals: push.HasNodeTotals, ReceivedBytes: push.NodeReceivedBytes, SentBytes: push.NodeSentBytes,
			HasRates: push.HasNodeRates, ReceivedRate: push.ReceivedBytesRate, SentRate: push.SentBytesRate,
		})
	case wire.MsgKeepalive:
		// no-op
	default:
		return false
	}
	return true
}

// reconcileBackoff spaces out repeated attempts to push the same desired
// state at a node that keeps reporting something else. Every entry doubles
// the wait, capped at the last value.
var reconcileBackoff = []time.Duration{0, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

type reconcileAttempt struct {
	hash     string
	attempts int
	lastAt   time.Time
}

// shouldReconcile reports whether a desired-state task for kind should be
// dispatched now. A node that never converges — a permanently failing apply,
// or a hash the agent can never reproduce — would otherwise produce a brand
// new task on every single heartbeat, which floods the task table, the live
// event stream and every open browser. A genuinely new desired hash always
// dispatches immediately; a repeat of the same hash backs off.
func (s *Server) shouldReconcile(nodeID, kind, desiredHash string) bool {
	key := nodeID + "/" + kind
	now := time.Now()
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	if s.reconcileState == nil {
		s.reconcileState = map[string]reconcileAttempt{}
	}
	previous, seen := s.reconcileState[key]
	if !seen || !strings.EqualFold(previous.hash, desiredHash) {
		s.reconcileState[key] = reconcileAttempt{hash: desiredHash, attempts: 1, lastAt: now}
		return true
	}
	wait := reconcileBackoff[min(previous.attempts, len(reconcileBackoff)-1)]
	if now.Sub(previous.lastAt) < wait {
		return false
	}
	s.reconcileState[key] = reconcileAttempt{hash: desiredHash, attempts: previous.attempts + 1, lastAt: now}
	return true
}

// clearReconcileState forgets a node's backoff so the next desired-state
// change is pushed without delay. Called whenever an operator edits
// something, since that is a deliberate action and must feel immediate.
func (s *Server) clearReconcileState(nodeID string) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	delete(s.reconcileState, nodeID+"/singbox")
	delete(s.reconcileState, nodeID+"/nginx")
}

func (s *Server) reconcileNodeDesiredState(ctx context.Context, nodeID, singBoxVersion, singBoxConfigHash, nginxConfigHash string) {
	if singBoxVersion != "" {
		_, desiredHash, err := s.store.CompileNodeConfig(ctx, nodeID)
		if err != nil {
			log.Printf("compile desired sing-box configuration for node %s: %v", nodeID, err)
		} else if !strings.EqualFold(desiredHash, singBoxConfigHash) && s.shouldReconcile(nodeID, "singbox", desiredHash) {
			if _, err := s.dispatchNodeConfiguration(ctx, nodeID, ""); err != nil {
				log.Printf("reconcile sing-box configuration for node %s: %v", nodeID, err)
			}
		}
	}
	_, desiredNginxHash, err := s.store.CompileNodeNginx(ctx, nodeID)
	if err != nil {
		log.Printf("compile desired Nginx configuration for node %s: %v", nodeID, err)
	} else if !strings.EqualFold(desiredNginxHash, nginxConfigHash) && s.shouldReconcile(nodeID, "nginx", desiredNginxHash) {
		if _, err := s.dispatchNodeNginx(ctx, nodeID, ""); err != nil {
			log.Printf("reconcile Nginx configuration for node %s: %v", nodeID, err)
		}
	}
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
	CollectedAt string            `json:"collected_at"`
	Node        map[string]uint64 `json:"node,omitempty"`
	// Proxy is sing-box's own cumulative byte count — the figure an operator
	// means by "traffic". Node is the host interface total, which also counts
	// SSH, package updates and everything else on the machine.
	Proxy       map[string]uint64  `json:"proxy,omitempty"`
	Connections []storedConnection `json:"connections,omitempty"`
	Fail2Ban    *storedFail2Ban    `json:"fail2ban,omitempty"`
	Firewall    *storedFirewall    `json:"firewall,omitempty"`
	Health      *storedHealth      `json:"health,omitempty"`
}

// storedFirewall carries the host's own firewall rules — the ones polaris
// did not write — so the console can show what a server already enforces.
type storedFirewall struct {
	Available bool                 `json:"available"`
	Tool      string               `json:"tool,omitempty"`
	Rules     []storedFirewallRule `json:"rules,omitempty"`
	Truncated bool                 `json:"truncated,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type storedFirewallRule struct {
	Table string `json:"table,omitempty"`
	Chain string `json:"chain,omitempty"`
	Rule  string `json:"rule"`
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
	// ListenerName is the human-readable inbound the connection arrived on,
	// resolved from the agent's sing-box inbound tag.
	ListenerName string `json:"listener_name,omitempty"`
	// User is the access user sing-box authenticated the connection as, which
	// is what the overview counts when it ranks the busiest nodes.
	User string `json:"user,omitempty"`
}

type storedFail2BanJail struct {
	Name            string              `json:"name"`
	CurrentlyBanned string              `json:"currently_banned,omitempty"`
	TotalBanned     string              `json:"total_banned,omitempty"`
	BannedIPs       []string            `json:"banned_ips,omitempty"`
	Banned          []storedFail2BanBan `json:"banned,omitempty"`
	Error           string              `json:"error,omitempty"`
	Managed         bool                `json:"managed"`
}

type storedFail2BanBan struct {
	IP       string `json:"ip"`
	BannedAt string `json:"banned_at,omitempty"`
	UnbanAt  string `json:"unban_at,omitempty"`
}

type storedFail2Ban struct {
	Available bool                 `json:"available"`
	Jails     []storedFail2BanJail `json:"jails"`
}

func (s *Server) convertConnections(ctx context.Context, nodeID string, in []wire.ConnectionInfo) []storedConnection {
	names := s.listenerNames(ctx, nodeID)
	aliases := s.endpointAliases(ctx, nodeID)
	out := make([]storedConnection, 0, len(in))
	for _, c := range in {
		ip := addressIP(c.Source)
		listenerID := strings.TrimPrefix(c.InboundTag, "listener-")
		// sing-box reports the account name it authenticated; the console
		// shows the alias an operator gave that node where there is one.
		user := c.User
		if alias, ok := aliases[listenerID+"\x00"+c.User]; ok && alias != "" {
			user = alias
		}
		out = append(out, storedConnection{
			ID: c.ID, Inbound: c.Inbound, Network: c.Network, Source: c.Source, Destination: c.Destination,
			SourceIP: ip, SourceLocation: s.ipLocator.Locate(ip), Host: c.Host, Upload: c.Upload, Download: c.Download,
			StartedAt: c.StartedAt, Outbound: c.Outbound, Rule: c.Rule, RulePayload: c.RulePayload, Chains: c.Chains,
			ListenerName: names[listenerID], User: user,
		})
	}
	return out
}

// endpointAliases maps a node's "listener ID + account name" to the alias an
// operator gave that access node, sharing the listener name cache's lifetime
// for the same reason: pushes are frequent, these names are not.
func (s *Server) endpointAliases(ctx context.Context, nodeID string) map[string]string {
	s.listenerNameMu.Lock()
	cached, ok := s.listenerNameCache[nodeID]
	s.listenerNameMu.Unlock()
	if ok && time.Since(cached.at) < listenerNameCacheTTL && cached.aliases != nil {
		return cached.aliases
	}
	aliases := map[string]string{}
	rows, err := s.store.db.QueryContext(ctx, `SELECT e.listener_id, e.name, e.alias FROM endpoints e JOIN listeners l ON l.id = e.listener_id WHERE l.node_id = ?`, nodeID)
	if err != nil {
		return aliases
	}
	defer rows.Close()
	for rows.Next() {
		var listenerID, name, alias string
		if err := rows.Scan(&listenerID, &name, &alias); err != nil {
			return aliases
		}
		aliases[listenerID+"\x00"+name] = alias
	}
	if rows.Err() != nil {
		return aliases
	}
	s.listenerNameMu.Lock()
	if s.listenerNameCache == nil {
		s.listenerNameCache = map[string]listenerNameEntry{}
	}
	entry := s.listenerNameCache[nodeID]
	entry.aliases = aliases
	s.listenerNameCache[nodeID] = entry
	s.listenerNameMu.Unlock()
	return aliases
}

// listenerNames maps a node's listener IDs to their display names so a
// sing-box inbound tag can be shown as the service an operator configured.
// Results are cached briefly: connection pushes arrive every few seconds per
// node, and listener names change far more rarely than that.
func (s *Server) listenerNames(ctx context.Context, nodeID string) map[string]string {
	s.listenerNameMu.Lock()
	cached, ok := s.listenerNameCache[nodeID]
	s.listenerNameMu.Unlock()
	if ok && time.Since(cached.at) < listenerNameCacheTTL {
		return cached.names
	}
	names := map[string]string{}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, name FROM listeners WHERE node_id = ?`, nodeID)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return names
		}
		names[id] = name
	}
	if rows.Err() != nil {
		return names
	}
	s.listenerNameMu.Lock()
	if s.listenerNameCache == nil {
		s.listenerNameCache = map[string]listenerNameEntry{}
	}
	s.listenerNameCache[nodeID] = listenerNameEntry{names: names, at: time.Now()}
	s.listenerNameMu.Unlock()
	return names
}

func convertFail2BanJails(in []wire.Fail2BanJailStatus) []storedFail2BanJail {
	out := make([]storedFail2BanJail, 0, len(in))
	for _, j := range in {
		bans := make([]storedFail2BanBan, 0, len(j.Banned))
		for _, ban := range j.Banned {
			bans = append(bans, storedFail2BanBan{IP: ban.IP, BannedAt: ban.BannedAt, UnbanAt: ban.UnbanAt})
		}
		out = append(out, storedFail2BanJail{Name: j.Name, CurrentlyBanned: j.CurrentlyBanned, TotalBanned: j.TotalBanned, BannedIPs: j.BannedIPs, Banned: bans, Error: j.Error, Managed: j.Managed})
	}
	return out
}

func convertFirewallRules(in []wire.FirewallRuleEntry) []storedFirewallRule {
	out := make([]storedFirewallRule, 0, len(in))
	for _, rule := range in {
		out = append(out, storedFirewallRule{Table: rule.Table, Chain: rule.Chain, Rule: rule.Rule})
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
