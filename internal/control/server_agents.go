package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/liyuwei007036/polaris/internal/wire"
)

// ServeAgents accepts KCP/UDP connections from agents and runs the Noise_XK
// handshake + binary session protocol on each — there is no HTTP anywhere in
// this path.
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

// handleAgentConn processes one raw KCP/UDP connection end to end. The protocol
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
	_ = raw.SetDeadline(time.Now().Add(60 * time.Second))
	conn, peerPub, err := wire.AcceptXK(raw, s.noiseKeypair)
	if err != nil {
		log.Printf("[AGENT_HANDSHAKE] Handshake failed from %s: %v", raw.RemoteAddr(), err)
		return
	}
	msgType, body, err := conn.ReadMessage()
	if err != nil {
		log.Printf("[AGENT_HANDSHAKE] Read initial message failed from %s: err=%v", raw.RemoteAddr(), err)
		return
	}
	if msgType == wire.MsgArtifactRequest {
		node, nodeErr := s.store.NodeForPublicKey(ctx, peerPub[:])
		if nodeErr != nil {
			log.Printf("[AGENT_ARTIFACT] Rejected artifact request from unrecognized key %s", raw.RemoteAddr())
			return
		}
		_ = raw.SetDeadline(time.Time{})
		s.handleArtifactRequest(ctx, conn, node, body)
		return
	}
	if msgType != wire.MsgRegister {
		log.Printf("[AGENT_HANDSHAKE] Read register message failed from %s: type=%d, err=%v", raw.RemoteAddr(), msgType, err)
		return
	}
	var req wire.RegisterRequest
	if err := wire.Decode(body, &req); err != nil {
		log.Printf("[AGENT_HANDSHAKE] Decode register failed from %s: %v", raw.RemoteAddr(), err)
		return
	}

	// Dynamic Path MTU Negotiation: reconcile agent's detected MTU with master's local detection.
	agentMTU := req.DetectedMTU
	if agentMTU <= 0 {
		if pmtuStr, ok := req.Capabilities["pmtu"]; ok {
			agentMTU, _ = strconv.Atoi(pmtuStr)
		}
	}
	var effectiveMTU int
	if masterUDP, err := wire.DetectUDPMTU(raw.RemoteAddr()); err == nil && masterUDP > 0 {
		if agentMTU > 0 {
			effectiveMTU = min(masterUDP, agentMTU)
		} else {
			effectiveMTU = masterUDP
		}
	} else if agentMTU > 0 {
		effectiveMTU = agentMTU
	}
	if effectiveMTU > 0 {
		conn.SetMtu(effectiveMTU)
	}

	node, err := s.store.NodeForPublicKey(ctx, peerPub[:])
	if err != nil {
		log.Printf("[AGENT_HANDSHAKE] Unrecognized node key from %s, handling registration attempt", raw.RemoteAddr())
		s.handleRegistrationAttempt(ctx, conn, peerPub, req)
		return
	}
	releaseKeyPEM, err := s.store.ReleaseSigningPublicKeyPEM()
	if err != nil {
		log.Printf("[AGENT_HANDSHAKE] Release signing key error for node %s (%s): %v", node.ID, node.Name, err)
		return
	}
	ackBody, err := wire.Encode(wire.RegisterAck{
		NodeID: node.ID, Status: "approved", ReleaseSigningPublicKeyPEM: releaseKeyPEM,
		ConnectionsIntervalSeconds: uint32(s.connectionsInterval / time.Second),
		EffectiveMTU:               effectiveMTU,
	})
	if err != nil {
		return
	}
	if err := conn.WriteMessage(wire.MsgRegisterAck, ackBody); err != nil {
		log.Printf("[AGENT_HANDSHAKE] Write register ack failed for node %s (%s): %v", node.ID, node.Name, err)
		return
	}
	// The address the agent dialled in from is the one clients can reach it
	// on, so a node is usable as soon as it is approved. It is only used when
	// nobody has entered an address by hand.
	address := connectingAddress(raw)
	if address != "" {
		if err := s.store.SetNodeObservedAddress(ctx, node.ID, address); err != nil {
			log.Printf("record observed address for node %s: %v", node.ID, err)
		}
	}
	_ = raw.SetDeadline(time.Time{})
	log.Printf("[AGENT_HANDSHAKE] SUCCESS: node %s (%s) connected via %s from %s (observed address=%s)", node.ID, node.Name, raw.RemoteAddr().Network(), raw.RemoteAddr(), address)
	s.runAgentSession(ctx, conn, node, address)
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
	defer time.Sleep(100 * time.Millisecond)
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

func (s *Server) handleArtifactRequest(ctx context.Context, conn *wire.Conn, node Node, body []byte) {
	sendError := func(message string) {
		chunkBody, err := wire.Encode(wire.ArtifactChunk{Done: true, Error: message})
		if err != nil {
			return
		}
		_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		if conn.WriteMessage(wire.MsgArtifactChunk, chunkBody) == nil {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _, _ = conn.ReadMessage()
		}
	}

	var req wire.ArtifactRequest
	if err := wire.Decode(body, &req); err != nil {
		sendError("invalid artifact request")
		return
	}
	if req.Kind != "polaris" {
		sendError("unsupported artifact kind")
		return
	}
	log.Printf("[AGENT_ARTIFACT] Node %s (%s) requesting relayed polaris artifact %s (%s)", node.ID, node.Name, req.Version, req.Architecture)
	type prepResult struct {
		path string
		err  error
	}
	prepDone := make(chan prepResult, 1)
	go func() {
		p, pErr := s.ensurePolarisArtifact(ctx, req.Architecture, req.Version, req.SHA256)
		prepDone <- prepResult{path: p, err: pErr}
	}()
	var archivePath string
	keepaliveTicker := time.NewTicker(15 * time.Second)
	defer keepaliveTicker.Stop()
waitPrep:
	for {
		select {
		case res := <-prepDone:
			if res.err != nil {
				log.Printf("[AGENT_ARTIFACT] Failed to prepare artifact for node %s (%s): %v", node.ID, node.Name, res.err)
				sendError(res.err.Error())
				return
			}
			archivePath = res.path
			break waitPrep
		case <-keepaliveTicker.C:
			kaBody, kaErr := wire.Encode(wire.ArtifactChunk{})
			if kaErr != nil {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if wErr := conn.WriteMessage(wire.MsgArtifactChunk, kaBody); wErr != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
	file, err := os.Open(archivePath)
	if err != nil {
		sendError(err.Error())
		return
	}
	defer file.Close()

	buf := make([]byte, 256*1024)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			chunkBody, encErr := wire.Encode(wire.ArtifactChunk{Data: buf[:n]})
			if encErr != nil {
				return
			}
			_ = conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
			if err := conn.WriteMessage(wire.MsgArtifactChunk, chunkBody); err != nil {
				return
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			sendError(readErr.Error())
			return
		}
	}
	eofBody, err := wire.Encode(wire.ArtifactChunk{Done: true})
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	if err := conn.WriteMessage(wire.MsgArtifactChunk, eofBody); err != nil {
		return
	}
	// Wait briefly for the agent to close after reading the final chunk so KCP
	// delivers and ACKs the EOF frame before raw.Close() tears down the socket.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, _ = conn.ReadMessage()
}

// runAgentSession is the long-lived per-connection loop for an approved
// node: it multiplexes incoming Status/TaskResult/Connections messages with
// outgoing Task dispatches and periodic keepalives, all over the one Noise
// connection. This replaces the old controlSession NDJSON stream plus the
// three separate HTTP endpoints (heartbeat, connections push, task result).
func (s *Server) runAgentSession(ctx context.Context, conn *wire.Conn, node Node, remoteAddr string) {
	session := &controlSession{
		done:       make(chan struct{}),
		tasks:      make(chan Task, 64),
		questions:  make(chan wire.Task, 16),
		watch:      make(chan struct{}, 1),
		remoteAddr: remoteAddr,
	}
	s.controlMu.Lock()
	if previous := s.controls[node.ID]; previous != nil {
		log.Printf("[AGENT_SESSION] Node %s (%s) superseding previous session from %s (new session from %s)",
			node.ID, node.Name, previous.remoteAddr, remoteAddr)
		close(previous.done)
	}
	s.controls[node.ID] = session
	s.controlMu.Unlock()
	isOnline := true
	s.liveHub.publish(liveEvent{
		Kind:           "node",
		NodeID:         node.ID,
		Action:         "online",
		Online:         &isOnline,
		ClientAddress:  remoteAddr,
		LastObservedIP: remoteAddr,
	})
	if remoteAddr != "" {
		go s.triggerNodeDDNS(context.Background(), node.ID, remoteAddr)
	}
	if s.alertEngine != nil && ctx.Err() == nil {
		s.alertEngine.NotifyNodeOnline(node.ID, node.Name)
	}
	if probeTasks, probeErr := s.store.ListPingTasks(ctx); probeErr == nil {
		_ = s.reconcilePingTasksNode(ctx, node.ID, probeTasks, "")
	}
	defer func() {
		s.controlMu.Lock()
		isCurrent := (s.controls[node.ID] == session)
		if isCurrent {
			delete(s.controls, node.ID)
		}
		s.controlMu.Unlock()
		if !isCurrent {
			log.Printf("[AGENT_SESSION] Node %s (%s) superseded session from %s completed, NOT marking offline (newer session active)",
				node.ID, node.Name, remoteAddr)
			return
		}
		log.Printf("[AGENT_SESSION] Node %s (%s) session from %s genuinely closed, marking offline", node.ID, node.Name, remoteAddr)
		isOffline := false
		s.liveHub.publish(liveEvent{
			Kind:   "node",
			NodeID: node.ID,
			Action: "offline",
			Online: &isOffline,
		})
		_ = s.store.FailInterruptedTasks(context.Background(), node.ID)
		if s.alertEngine != nil && ctx.Err() == nil {
			s.alertEngine.NotifyNodeOffline(node.ID, node.Name)
		}
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

	// A node that just connected assumes it should be reporting, which is the
	// right default against an older master but wrong here whenever no console
	// is open. Say so before anything else, so it goes quiet after the one
	// push it always sends on connect.
	if !s.sendWatchState(conn, s.fleetStreamingState()) {
		return
	}

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
			log.Printf("[AGENT_EXIT] Node %s (%s) ctx.Done", node.ID, node.Name)
			return
		case <-session.done:
			log.Printf("[AGENT_EXIT] Node %s (%s) session.done (superseded by newer session)", node.ID, node.Name)
			return
		case err := <-readErr:
			log.Printf("[AGENT_EXIT] Node %s (%s) readErr from %s: %v", node.ID, node.Name, remoteAddr, err)
			return
		case msg := <-incoming:
			if !s.handleAgentMessage(ctx, &node, msg.msgType, msg.body) {
				log.Printf("[AGENT_EXIT] Node %s (%s) handleAgentMessage returned false, closing session", node.ID, node.Name)
				return
			}
		case <-session.watch:
			if !s.sendWatchState(conn, s.fleetStreamingState()) {
				log.Printf("[AGENT_EXIT] Node %s (%s) sendWatchState failed", node.ID, node.Name)
				return
			}
		case task := <-session.tasks:
			if !s.sendTask(conn, node.ID, task) {
				log.Printf("[AGENT_EXIT] Node %s (%s) sendTask failed", node.ID, node.Name)
				return
			}
		case question := <-session.questions:
			body, err := wire.Encode(question)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(wire.MsgTask, body); err != nil {
				log.Printf("[AGENT_EXIT] Node %s (%s) sendQuestion failed: %v", node.ID, node.Name, err)
				return
			}
		case <-ticker.C:
			if err := conn.WriteMessage(wire.MsgKeepalive, nil); err != nil {
				log.Printf("[AGENT_EXIT] Node %s (%s) keepalive write failed: %v", node.ID, node.Name, err)
				return
			}
		}
	}
}

// sendWatchState tells one node whether to run its real-time push. Agents too
// old to know the message ignore it and keep reporting, which is why nothing
// here treats a node that keeps pushing as an error.
func (s *Server) sendWatchState(conn *wire.Conn, streaming bool) bool {
	body, err := wire.Encode(wire.WatchState{Streaming: streaming})
	if err != nil {
		return false
	}
	return conn.WriteMessage(wire.MsgWatch, body) == nil
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

func (s *Server) handleAgentMessage(ctx context.Context, node *Node, msgType byte, body []byte) bool {
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
		if err := s.store.UpdateNodeIdentityWithIPs(ctx, node.ID, st.AgentVersion, st.OS, st.OSName, st.Architecture, st.SingBoxVersion, string(capsJSON), st.IPAddresses, st.IPMappings); err != nil {
			log.Printf("UpdateNodeIdentityWithIPs error for node %s (%s): %v", node.ID, node.Name, err)
		}
		// The agent reports its new build right after an upgrade restart. Push
		// the change so an open console stops offering the upgrade it just ran.
		if st.AgentVersion != "" && st.AgentVersion != node.AgentVersion {
			node.AgentVersion = st.AgentVersion
			s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID, AgentVersion: st.AgentVersion})
		}
		s.maybeInstallSingBox(ctx, node.ID, st.OS, st.Architecture, st.SingBoxVersion)
		if st.Capabilities["configuration_hashes"] == "true" {
			s.reconcileNodeDesiredState(ctx, node.ID, st.SingBoxVersion, st.SingBoxConfigHash, st.NginxConfigHash)
		}
		s.reconcileNodeDDNS(ctx, node.ID)
		var currentLatency *storedLatency
		if err := s.mergeNodeMetrics(ctx, node.ID, func(m *storedMetrics) {
			m.CollectedAt = st.CollectedAt
			// Check if calendar month has rolled over, resetting monthly counters on the 1st
			currentMonth := time.Now().UTC().Format("2006-01")
			if m.CurrentMonth != currentMonth {
				m.CurrentMonth = currentMonth
				if m.Proxy != nil {
					m.Proxy["monthly_received_bytes"] = 0
					m.Proxy["monthly_sent_bytes"] = 0
				}
				if m.Node != nil {
					m.Node["monthly_received_bytes"] = 0
					m.Node["monthly_sent_bytes"] = 0
				}
			}
			// Connections are real-time only and live in connectionsHub; drop
			// any list an older build persisted so nothing stale is served.
			m.Connections = nil
			if st.HasNodeTotals {
				m.Node, m.RawNode = accumulateMetric(m.Node, m.RawNode, st.NodeReceivedBytes, st.NodeSentBytes)
			}
			if st.HasProxyTotals {
				m.Proxy, m.RawProxy = accumulateMetric(m.Proxy, m.RawProxy, st.ProxyReceivedBytes, st.ProxySentBytes)
			}
			if st.HasHostTotals {
				m.Host = &HostResources{
					CPUPercent:         st.CPUPercent,
					LoadAvg:            st.LoadAvg,
					MemoryTotal:        st.MemoryTotal,
					MemoryUsed:         st.MemoryUsed,
					SwapTotal:          st.SwapTotal,
					SwapUsed:           st.SwapUsed,
					DiskTotal:          st.DiskTotal,
					DiskUsed:           st.DiskUsed,
					UptimeSeconds:      st.UptimeSeconds,
					AgentUptimeSeconds: st.AgentUptimeSeconds,
					TCP:                st.TCP,
					UDP:                st.UDP,
					Procs:              st.Procs,
					NetRxRate:          st.NetRxRate,
					NetTxRate:          st.NetTxRate,
					OSName:             st.OSName,
					Kernel:             st.Kernel,
					Virt:               st.Virt,
					CPUName:            st.CPUName,
					CPUCores:           st.CPUCores,
				}
			}
			m.Latency = s.recordNodePingResults(node.ID, st.PingResults, m.Latency)
			currentLatency = m.Latency
			m.Health = &storedHealth{
				Status: st.HealthStatus, Message: st.HealthMessage, SingBoxService: st.SingBoxService,
				ClashAPIAvailable: st.ClashAPIAvailable, TrafficAvailable: st.TrafficAvailable,
			}
		}); err != nil {
			log.Printf("mergeNodeMetrics error for node %s (%s): %v", node.ID, node.Name, err)
		}
		if len(st.PingResults) == 0 {
			s.controlMu.Lock()
			session := s.controls[node.ID]
			shouldReconcile := false
			if session != nil && time.Since(session.lastProbeReconcile) > 60*time.Second {
				session.lastProbeReconcile = time.Now()
				shouldReconcile = true
			}
			s.controlMu.Unlock()
			if shouldReconcile {
				go func(nid string) {
					if probeTasks, err := s.store.ListPingTasks(context.Background()); err == nil && len(probeTasks) > 0 {
						_ = s.reconcilePingTasksNode(context.Background(), nid, probeTasks, "")
					}
				}(node.ID)
			}
		}
		var hostRes *HostResources
		if st.HasHostTotals {
			hostRes = &HostResources{
				CPUPercent:         st.CPUPercent,
				LoadAvg:            st.LoadAvg,
				MemoryTotal:        st.MemoryTotal,
				MemoryUsed:         st.MemoryUsed,
				SwapTotal:          st.SwapTotal,
				SwapUsed:           st.SwapUsed,
				DiskTotal:          st.DiskTotal,
				DiskUsed:           st.DiskUsed,
				UptimeSeconds:      st.UptimeSeconds,
				AgentUptimeSeconds: st.AgentUptimeSeconds,
				TCP:                st.TCP,
				UDP:                st.UDP,
				Procs:              st.Procs,
				NetRxRate:          st.NetRxRate,
				NetTxRate:          st.NetTxRate,
				OSName:             st.OSName,
				Kernel:             st.Kernel,
				Virt:               st.Virt,
				CPUName:            st.CPUName,
				CPUCores:           st.CPUCores,
			}
			s.recordNodeMetricPoint(node.ID, hostRes)
		}
		healthRes := &storedHealth{
			Status: st.HealthStatus, Message: st.HealthMessage, SingBoxService: st.SingBoxService,
			ClashAPIAvailable: st.ClashAPIAvailable, TrafficAvailable: st.TrafficAvailable,
		}
		if s.connHub != nil {
			s.connHub.updateHostMetrics(node.ID, hostRes, currentLatency, healthRes)
		}
	case wire.MsgTaskResult:
		var res wire.TaskResult
		if err := wire.Decode(body, &res); err != nil {
			return false
		}
		// A question asked on demand is never stored, so its answer goes
		// straight to whoever is waiting and there is nothing to record.
		if s.deliverTaskAnswer(res) {
			return true
		}
		task, err := s.store.TaskByID(ctx, res.TaskID)
		if err != nil {
			// A task not in the database and having no active live waiter is an
			// expired/timed-out live task answer. Deliberately ignore it and keep
			// the agent session alive. Never drop the connection because of late answers.
			return true
		}
		if err := s.store.CompleteTask(ctx, task.ID, node.ID, res.Status, res.Summary); err != nil {
			log.Printf("CompleteTask error for task %s (node %s): %v, keeping session alive", task.ID, node.ID, err)
		}
		s.liveHub.publish(liveEvent{Kind: "task", NodeID: node.ID, TaskID: task.ID})
		if res.Status == "succeeded" && res.SingBoxVersion != "" {
			if err := s.store.SetNodeSingBoxVersion(ctx, node.ID, res.SingBoxVersion); err != nil {
				log.Printf("SetNodeSingBoxVersion error for node %s: %v", node.ID, err)
			}
		}
		if task.OperatorID != "" {
			_ = s.store.AppendAudit(ctx, task.OperatorID, "task.completed."+res.Status, "task", task.ID, "agent task completed: "+task.Kind)
		}
		if task.Kind == "dns.update" {
			summary := fmt.Sprintf("前置节点「%s」完成动态 DNS 刷新与链路自愈：%s", node.Name, res.Summary)
			_ = s.store.AppendAudit(ctx, task.OperatorID, "node.dns_update_completed", "node", node.ID, summary)
			if s.alertEngine != nil && res.Status == "succeeded" {
				s.alertEngine.NotifyChainedDNSCompleted(node.ID, node.Name, res.Summary)
			}
		}
	case wire.MsgConnections:
		var push wire.ConnectionsPush
		if err := wire.Decode(body, &push); err != nil {
			return false
		}
		connections := s.convertConnections(ctx, node.ID, push.Connections)
		// Rates are measured here, against the node's previous push, because
		// sing-box reports each connection's totals and nothing else. The
		// node's own throughput is those per-connection rates added up, so
		// every real-time figure in the console — the overview's live traffic
		// and any subtotal the connection list shows for a filter — is one
		// measurement read at different scopes.
		now := time.Now()
		downloadRate, uploadRate, measured := s.connRates.measure(node.ID, connections, now)
		// The busiest-nodes ranking counts a connection once, when it first
		// arrives, so it has to see every push rather than the round the
		// console happens to aggregate.
		s.connActivity.record(node.ID, connections, now)
		connJSON, err := json.Marshal(connections)
		if err != nil {
			return false
		}
		// Real-time state stays in memory and reaches browsers over SSE. It is
		// deliberately not persisted: this arrives every couple of seconds per
		// node, and a SQLite read-modify-write of the whole metrics blob at
		// that cadence was the single largest source of load on the master.
		s.connHub.update(nodeConnectionsSnapshot{
			NodeID: node.ID, CollectedAt: push.CollectedAt, Connections: connJSON, RawConns: connections,
			ConnectionCount: len(connections),
			Protocols:       protocolCounts(connections),
			HasRates:        measured, ReceivedRate: downloadRate, SentRate: uploadRate,
		})
		if s.connRecorder != nil {
			s.connRecorder.RecordPush(node.ID, node.Name, connections)
		}
		if s.alertEngine != nil {
			s.alertEngine.CheckConnectionsTelemetry(node.ID, node.Name, downloadRate, uploadRate, connections)
		}
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
	delete(s.reconcileState, nodeID+"/singbox")
	delete(s.reconcileState, nodeID+"/nginx")
	s.reconcileMu.Unlock()

	s.ddnsReconcileMu.Lock()
	delete(s.ddnsReconcileState, nodeID)
	s.ddnsReconcileMu.Unlock()
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
	// The router configuration is no longer a desired state of its own: the
	// node derives it from the services it was asked to run, so converging the
	// sing-box configuration converges the router with it.
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
type HostResources struct {
	CPUPercent    float64    `json:"cpu_percent"`
	LoadAvg       [3]float64 `json:"load_avg"`
	MemoryTotal   uint64     `json:"memory_total"`
	MemoryUsed    uint64     `json:"memory_used"`
	SwapTotal     uint64     `json:"swap_total"`
	SwapUsed      uint64     `json:"swap_used"`
	DiskTotal     uint64     `json:"disk_total"`
	DiskUsed      uint64     `json:"disk_used"`
	UptimeSeconds uint64     `json:"uptime_seconds,omitempty"`
	// AgentUptimeSeconds is the agent process's run time, used for the node's
	// "online" badge; UptimeSeconds stays the host boot uptime.
	AgentUptimeSeconds uint64 `json:"agent_uptime_seconds,omitempty"`
	TCP                uint32 `json:"tcp"`
	UDP                uint32 `json:"udp"`
	Procs              uint32 `json:"procs"`
	NetRxRate          uint64 `json:"net_rx_rate"`
	NetTxRate          uint64 `json:"net_tx_rate"`
	OSName             string `json:"os_name,omitempty"`
	Kernel             string `json:"kernel,omitempty"`
	Virt               string `json:"virt,omitempty"`
	CPUName            string `json:"cpu_name,omitempty"`
	CPUCores           uint32 `json:"cpu_cores,omitempty"`
}

type storedMetrics struct {
	CollectedAt  string             `json:"collected_at"`
	CurrentMonth string             `json:"current_month,omitempty"`
	Node         map[string]uint64  `json:"node,omitempty"`
	Proxy        map[string]uint64  `json:"proxy,omitempty"`
	RawNode      map[string]uint64  `json:"raw_node,omitempty"`
	RawProxy     map[string]uint64  `json:"raw_proxy,omitempty"`
	Connections  []storedConnection `json:"connections,omitempty"`
	Health       *storedHealth      `json:"health,omitempty"`
	Host         *HostResources     `json:"host,omitempty"`
	Latency      *storedLatency     `json:"latency,omitempty"`
}

func accumulateMetric(totals map[string]uint64, rawTotals map[string]uint64, curRx, curTx uint64) (map[string]uint64, map[string]uint64) {
	if totals == nil {
		totals = make(map[string]uint64)
	}
	if rawTotals == nil {
		rawTotals = make(map[string]uint64)
	}

	lastRawRx := rawTotals["received_bytes"]
	lastRawTx := rawTotals["sent_bytes"]

	var deltaRx, deltaTx uint64
	if lastRawRx == 0 && lastRawTx == 0 && totals["received_bytes"] == 0 && totals["sent_bytes"] == 0 {
		deltaRx = curRx
		deltaTx = curTx
	} else if lastRawRx == 0 && lastRawTx == 0 && (totals["received_bytes"] > 0 || totals["sent_bytes"] > 0) {
		// Existing historical database without raw counters
		if curRx >= totals["received_bytes"] {
			deltaRx = curRx - totals["received_bytes"]
		} else {
			deltaRx = curRx
		}
		if curTx >= totals["sent_bytes"] {
			deltaTx = curTx - totals["sent_bytes"]
		} else {
			deltaTx = curTx
		}
	} else {
		// Standard monotonic accumulation across reboots and upgrades
		if curRx >= lastRawRx {
			deltaRx = curRx - lastRawRx
		} else {
			// Restart / upgrade detected
			deltaRx = curRx
		}
		if curTx >= lastRawTx {
			deltaTx = curTx - lastRawTx
		} else {
			// Restart / upgrade detected
			deltaTx = curTx
		}
	}

	totals["received_bytes"] += deltaRx
	totals["sent_bytes"] += deltaTx
	totals["monthly_received_bytes"] += deltaRx
	totals["monthly_sent_bytes"] += deltaTx
	rawTotals["received_bytes"] = curRx
	rawTotals["sent_bytes"] = curTx

	return totals, rawTotals
}

type storedHealth struct {
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	SingBoxService    string `json:"sing_box_service"`
	ClashAPIAvailable bool   `json:"clash_api_available"`
	TrafficAvailable  bool   `json:"traffic_available"`
}

type storedConnection struct {
	ID                  string   `json:"id,omitempty"`
	Inbound             string   `json:"inbound,omitempty"`
	Network             string   `json:"network,omitempty"`
	Source              string   `json:"source,omitempty"`
	SourceIP            string   `json:"source_ip,omitempty"`
	SourceLocation      string   `json:"source_location,omitempty"`
	SourceLat           float64  `json:"source_lat,omitempty"`
	SourceLon           float64  `json:"source_lon,omitempty"`
	HasSourceCoords     bool     `json:"has_source_coords,omitempty"`
	Destination         string   `json:"destination,omitempty"`
	DestinationLocation string   `json:"destination_location,omitempty"`
	Host                string   `json:"host,omitempty"`
	Upload              int64    `json:"upload"`
	Download            int64    `json:"download"`
	StartedAt           string   `json:"started_at,omitempty"`
	Outbound            string   `json:"outbound,omitempty"`
	Rule                string   `json:"rule,omitempty"`
	RulePayload         string   `json:"rule_payload,omitempty"`
	Chains              []string `json:"chains,omitempty"`
	// ListenerName is the human-readable inbound the connection arrived on,
	// resolved from the agent's sing-box inbound tag.
	ListenerName string `json:"listener_name,omitempty"`
	// User is the access user sing-box authenticated the connection as, which
	// is what the overview counts when it ranks the busiest nodes.
	User string `json:"user,omitempty"`
	// OutboundName is the egress the operator configured, resolved from the
	// sing-box outbound tag; "直连" for the built-in direct outbound.
	OutboundName   string `json:"outbound_name,omitempty"`
	OutboundDirect bool   `json:"outbound_direct,omitempty"`
	// HasRates says whether the rates below were measured. It is false for a
	// connection the master has only seen once, whose totals say nothing about
	// how fast it is moving right now.
	HasRates bool `json:"has_rates,omitempty"`
	// UploadRate and DownloadRate are bytes per second across the interval
	// between the two most recent pushes from this connection's node.
	UploadRate   float64 `json:"upload_rate,omitempty"`
	DownloadRate float64 `json:"download_rate,omitempty"`
	// IsChainRelay indicates whether this connection is an internal downstream relay
	// leg of a chained proxy originating from another server in our fleet.
	IsChainRelay  bool `json:"is_chain_relay,omitempty"`
	OnChainTarget bool `json:"on_chain_target,omitempty"`
	// RelayUpstreamNodeID identities the fleet node the relay leg came in from
	// — the node whose egress IP the source address resolved to. Without it a
	// flagged leg only says "from some other server", which is not enough to
	// pair two chains that converge on the same destination.
	RelayUpstreamNodeID  string   `json:"relay_upstream_node_id,omitempty"`
	ChainedTarget        string   `json:"chained_target,omitempty"`
	ResolvedOutboundName string   `json:"resolved_outbound_name,omitempty"`
	ChainPath            string   `json:"chain_path,omitempty"`
	ChainNodeIDs         []string `json:"chain_node_ids,omitempty"`
}

func (s *Server) convertConnections(ctx context.Context, nodeID string, in []wire.ConnectionInfo) []storedConnection {
	names := s.listenerNames(ctx, nodeID)
	aliases := s.endpointAliases(ctx, nodeID)
	egress, directOutbounds := s.outboundMeta(ctx)
	fleetIPs := s.fleetNodeIPs(ctx)
	chainTargets, chainUpstreams := s.chainTargetUsersWithUpstreams(ctx, nodeID)

	var activeSnapshots map[string][]storedConnection
	getActiveSnapshots := func() map[string][]storedConnection {
		if activeSnapshots == nil && s.connHub != nil {
			activeSnapshots = s.connHub.activeNodeConnections()
		}
		return activeSnapshots
	}
	var cachedNodeNames map[string]string
	getNodeNames := func() map[string]string {
		if cachedNodeNames != nil {
			return cachedNodeNames
		}
		cachedNodeNames = make(map[string]string)
		if nodes, err := s.store.ListNodes(ctx); err == nil {
			for _, n := range nodes {
				cachedNodeNames[n.ID] = n.Name
			}
		}
		return cachedNodeNames
	}
	resolvedChainCache := make(map[string]resolvedChainRoute)

	out := make([]storedConnection, 0, len(in))
	for _, c := range in {
		ip := addressIP(c.Source)
		listenerID := connectionListenerID(c)
		// The console shows the alias an operator gave the access node where
		// there is one, falling back to the account name sing-box matched.
		account := connectionAccount(c)
		user := account
		if alias, ok := aliases[listenerID+"\x00"+account]; ok && alias != "" {
			user = alias
		}

		var isChainRelay bool
		var chainedTarget string
		if id, ok := strings.CutPrefix(strings.TrimSpace(c.Outbound), "chain-"); ok && id != "" {
			chainedTarget = id
		} else {
			for _, tag := range c.Chains {
				if id, ok := strings.CutPrefix(strings.TrimSpace(tag), "chain-"); ok && id != "" {
					chainedTarget = id
					break
				}
			}
		}

		// Detect if this connection arrived on a chain target endpoint from another node in the fleet.
		// Important: only fall back to listenerID-level matching when account == "" so normal users sharing
		// the same listener are never misclassified as internal chain relays.
		var relayUpstreamNodeID string
		keySpecific := listenerID + "\x00" + account
		isKnownChainAccount := chainTargets[keySpecific]
		isUnauthenticatedListenerTarget := account == "" && chainTargets[listenerID]
		var targetUpstreams []string
		if isKnownChainAccount {
			targetUpstreams = chainUpstreams[keySpecific]
		} else if isUnauthenticatedListenerTarget {
			targetUpstreams = chainUpstreams[listenerID]
		}
		if isKnownChainAccount || isUnauthenticatedListenerTarget {
			upstreams := targetUpstreams
			upstreamSet := make(map[string]bool, len(upstreams))
			for _, u := range upstreams {
				upstreamSet[u] = true
			}
			isLoopbackPeer := false
			if parsedIP := net.ParseIP(ip); parsedIP != nil && parsedIP.IsLoopback() {
				isLoopbackPeer = true
			}
			if candidates := fleetIPs[ip]; len(candidates) > 0 {
				var fallbackNodeID string
				for _, cand := range candidates {
					if cand == nodeID {
						continue
					}
					if upstreamSet[cand] {
						relayUpstreamNodeID = cand
						break
					}
					if fallbackNodeID == "" {
						fallbackNodeID = cand
					}
				}
				if relayUpstreamNodeID == "" && !isLoopbackPeer {
					relayUpstreamNodeID = fallbackNodeID
				}
				if relayUpstreamNodeID != "" {
					isChainRelay = true
				}
			}
			// Fallback 1: unlisted IPv6/ephemeral SNAT egress when the active (or recent) upstream entry leg is known.
			if !isChainRelay && len(upstreams) > 0 {
				probe := storedConnection{Destination: c.Destination, Host: c.Host, Network: c.Network}
				if active := getActiveSnapshots(); len(active) > 0 {
					for _, upID := range upstreams {
						if upID == nodeID {
							continue
						}
						for _, oc := range active[upID] {
							if connectionChainEndpointID(oc) != "" && connectionDestMatches(oc, probe) {
								isChainRelay = true
								relayUpstreamNodeID = upID
								break
							}
						}
						if isChainRelay {
							break
						}
					}
				}
				if !isChainRelay {
					if matchedUpID, ok := s.matchRecentChainEntry(nodeID, upstreams, probe); ok {
						isChainRelay = true
						relayUpstreamNodeID = matchedUpID
					}
				}
			}
			// Fallback 2: loopback peer (e.g. co-hosted / WSL local proxy) on a chain target account.
			if !isChainRelay && isLoopbackPeer && len(upstreams) > 0 {
				for _, upID := range upstreams {
					if upID != "" && upID != nodeID {
						isChainRelay = true
						relayUpstreamNodeID = upID
						break
					}
				}
			}
		}
		onChainTarget := !isChainRelay && (isKnownChainAccount || isUnauthenticatedListenerTarget) && len(targetUpstreams) > 0

		destIP := addressIP(c.Destination)
		if destIP == "" && c.Host != "" && net.ParseIP(c.Host) != nil {
			destIP = c.Host
		}
		var destLoc string
		if destIP != "" && s.ipLocator != nil {
			destLoc = s.ipLocator.Locate(destIP)
		}

		var srcLoc string
		var srcLat, srcLon float64
		var hasSrcCoords bool
		if s.ipLocator != nil && ip != "" {
			srcInfo := s.ipLocator.Lookup(ip)
			srcLoc = srcInfo.Location
			srcLat = srcInfo.Latitude
			srcLon = srcInfo.Longitude
			hasSrcCoords = srcInfo.HasCoords
		}

		egressTag := c.Outbound
		if egressTag == "" && len(c.Chains) > 0 {
			egressTag = c.Chains[0]
		}

		sc := storedConnection{
			ID: c.ID, Inbound: c.Inbound, Network: c.Network, Source: c.Source, Destination: c.Destination,
			SourceIP: ip, SourceLocation: srcLoc, SourceLat: srcLat, SourceLon: srcLon, HasSourceCoords: hasSrcCoords,
			DestinationLocation: destLoc, Host: c.Host, Upload: c.Upload, Download: c.Download,
			StartedAt: c.StartedAt, Outbound: c.Outbound, Rule: c.Rule, RulePayload: c.RulePayload, Chains: c.Chains,
			ListenerName: names[listenerID], User: user, OutboundName: outboundDisplayName(egressTag, egress),
			OutboundDirect: isDirectOutboundTag(egressTag, directOutbounds),
			IsChainRelay:   isChainRelay, OnChainTarget: onChainTarget, RelayUpstreamNodeID: relayUpstreamNodeID, ChainedTarget: chainedTarget,
		}
		if chainedTarget != "" && !isChainRelay {
			cacheKey := chainedTarget + "\x00" + c.Destination + "\x00" + c.Host + "\x00" + c.Network
			resolved, cached := resolvedChainCache[cacheKey]
			if !cached {
				nodeNames := getNodeNames()
				resolved = resolveChainRoute(ctx, s.store, s.connHub, nil, nodeNames, nodeID, nodeNames[nodeID], sc)
				resolvedChainCache[cacheKey] = resolved
			}
			if resolved.HopResolved && resolved.FinalOutbound != "" {
				if resolved.FinalIsDirect && resolved.FinalNodeName != "" && resolved.FinalNodeName != getNodeNames()[nodeID] {
					sc.ResolvedOutboundName = fmt.Sprintf("%s（经 %s 落地）", resolved.FinalOutbound, resolved.FinalNodeName)
				} else {
					sc.ResolvedOutboundName = resolved.FinalOutbound
				}
				if len(resolved.Path) > 0 {
					sc.ChainPath = strings.Join(resolved.Path, " → ")
				}
				if len(resolved.NodeIDs) > 0 {
					sc.ChainNodeIDs = resolved.NodeIDs
				}
			}
		}
		out = append(out, sc)
	}
	s.recordRecentChainEntries(nodeID, out)
	return out
}

const recentChainEntryTTL = 30 * time.Second

func (s *Server) recordRecentChainEntries(nodeID string, conns []storedConnection) {
	if nodeID == "" {
		return
	}
	now := time.Now()
	s.recentChainMu.Lock()
	defer s.recentChainMu.Unlock()
	if s.recentChainEntries == nil {
		s.recentChainEntries = make(map[string][]recentChainEntry)
	}
	kept := s.recentChainEntries[nodeID][:0]
	for _, entry := range s.recentChainEntries[nodeID] {
		if now.Sub(entry.seenAt) <= recentChainEntryTTL {
			kept = append(kept, entry)
		}
	}
	for _, c := range conns {
		epID := connectionChainEndpointID(c)
		if epID == "" {
			continue
		}
		kept = append(kept, recentChainEntry{
			endpointID: epID,
			conn: storedConnection{
				ID:          c.ID,
				Network:     c.Network,
				Destination: c.Destination,
				Host:        c.Host,
				Outbound:    c.Outbound,
				Chains:      c.Chains,
			},
			seenAt: now,
		})
	}
	if len(kept) == 0 {
		delete(s.recentChainEntries, nodeID)
	} else {
		if len(kept) > 2000 {
			kept = kept[len(kept)-2000:]
		}
		s.recentChainEntries[nodeID] = kept
	}
}

func (s *Server) matchRecentChainEntry(currentNodeID string, upstreams []string, probe storedConnection) (string, bool) {
	now := time.Now()
	s.recentChainMu.Lock()
	defer s.recentChainMu.Unlock()
	if len(s.recentChainEntries) == 0 {
		return "", false
	}
	for _, upID := range upstreams {
		if upID == "" || upID == currentNodeID {
			continue
		}
		entries := s.recentChainEntries[upID]
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if now.Sub(entry.seenAt) > recentChainEntryTTL {
				continue
			}
			if connectionDestMatches(entry.conn, probe) {
				return upID, true
			}
		}
	}
	return "", false
}

// connectionListenerID resolves the listener a connection arrived on. sing-box
// reports the inbound in the Clash API's metadata.type, written as
// "<inbound type>/<inbound tag>"; the separate tag field is only populated by
// agents new enough to read it from there, so the metadata is the fallback for
// nodes whose agent has not been upgraded yet.
func connectionListenerID(c wire.ConnectionInfo) string {
	if id, found := strings.CutPrefix(c.InboundTag, "listener-"); found {
		return id
	}
	if _, tag, found := strings.Cut(c.Inbound, "/"); found {
		if id, ok := strings.CutPrefix(tag, "listener-"); ok {
			return id
		}
	}
	return ""
}

// connectionAccount resolves which access account a connection authenticated
// as. The Clash API leaves the user out of its connection metadata, but every
// account this project compiles gets an auth_user route rule, and the rule the
// connection matched is reported verbatim — so the account name arrives inside
// a string that reads like "inbound=listener-x auth_user=user_y => route(z)".
func connectionAccount(c wire.ConnectionInfo) string {
	if c.User != "" {
		return c.User
	}
	_, rest, found := strings.Cut(c.Rule, "auth_user=")
	if !found {
		return ""
	}
	if end := strings.IndexAny(rest, " \t"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// outboundDisplayName turns a sing-box outbound tag into the egress an
// operator would recognise. Unknown tags are shown as they are rather than
// hidden, so a connection is never left without an egress.
func outboundDisplayName(tag string, names map[string]string) string {
	switch {
	case tag == "":
		return ""
	case tag == "direct":
		return "直连"
	case strings.HasPrefix(tag, "direct-"):
		if ip := strings.TrimSpace(strings.TrimPrefix(tag, "direct-")); ip != "" {
			return fmt.Sprintf("直连 (%s)", ip)
		}
		return "直连"
	case strings.HasPrefix(tag, "direct:"):
		if ip := strings.TrimSpace(strings.TrimPrefix(tag, "direct:")); ip != "" {
			return fmt.Sprintf("直连 (%s)", ip)
		}
		return "直连"
	}
	if id, found := strings.CutPrefix(tag, "outbound-"); found {
		if name := names[id]; name != "" {
			return name
		}
	}
	if id, found := strings.CutPrefix(tag, "chain-"); found {
		if name := names["chain-"+id]; name != "" {
			return name
		}
		return "链式代理"
	}
	return tag
}

func isDirectOutboundTag(tag string, directOutbounds map[string]bool) bool {
	lower := strings.ToLower(strings.TrimSpace(tag))
	if lower == "direct" || strings.HasPrefix(lower, "direct-") || strings.HasPrefix(lower, "direct:") {
		return true
	}
	if id, found := strings.CutPrefix(strings.TrimSpace(tag), "outbound-"); found {
		return directOutbounds[id]
	}
	return false
}

// outboundNames maps outbound IDs to their configured names. Outbounds are
// global rather than per node, and they change far more rarely than the
// connection pushes that read them, so one short-lived cache serves them all.
func (s *Server) outboundNames(ctx context.Context) map[string]string {
	names, _ := s.outboundMeta(ctx)
	return names
}

func (s *Server) outboundMeta(ctx context.Context) (map[string]string, map[string]bool) {
	s.listenerNameMu.Lock()
	cached, directCached, at := s.outboundNameCache, s.outboundDirectCache, s.outboundNameCachedAt
	s.listenerNameMu.Unlock()
	if cached != nil && directCached != nil && time.Since(at) < listenerNameCacheTTL {
		return cached, directCached
	}
	names := map[string]string{}
	direct := map[string]bool{}
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, name, type FROM outbounds`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, name, obType string
			if err := rows.Scan(&id, &name, &obType); err == nil {
				names[id] = name
				if strings.EqualFold(strings.TrimSpace(obType), "direct") {
					direct[id] = true
				}
			}
		}
	}
	chainRows, err := s.store.db.QueryContext(ctx, `SELECT e.id, e.name, e.alias, l.name, n.name
		FROM endpoints e
		JOIN listeners l ON l.id = e.listener_id
		JOIN nodes n ON n.id = l.node_id`)
	if err == nil {
		defer chainRows.Close()
		for chainRows.Next() {
			var epID, epName, epAlias, listName, nName string
			if err := chainRows.Scan(&epID, &epName, &epAlias, &listName, &nName); err == nil {
				label := epAlias
				if label == "" {
					label = epName
				}
				names["chain-"+epID] = fmt.Sprintf("%s · %s (%s)", nName, listName, label)
			}
		}
	}
	s.listenerNameMu.Lock()
	s.outboundNameCache, s.outboundDirectCache, s.outboundNameCachedAt = names, direct, time.Now()
	s.listenerNameMu.Unlock()
	return names, direct
}

// endpointAliases maps a node's "listener ID + account name" to the alias an
// operator gave that access node, sharing the listener name cache's lifetime
// for the same reason: pushes are frequent, these names are not.
func (s *Server) endpointAliases(ctx context.Context, nodeID string) map[string]string {
	s.listenerNameMu.Lock()
	cached, ok := s.listenerNameCache[nodeID]
	s.listenerNameMu.Unlock()
	if ok && time.Since(cached.aliasesAt) < listenerNameCacheTTL && cached.aliases != nil {
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
	entry.aliasesAt = time.Now()
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
	if ok && time.Since(cached.namesAt) < listenerNameCacheTTL && cached.names != nil {
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
	entry := s.listenerNameCache[nodeID]
	entry.names = names
	entry.namesAt = time.Now()
	s.listenerNameCache[nodeID] = entry
	s.listenerNameMu.Unlock()
	return names
}

func appendUniqueString(list []string, item string) []string {
	if item == "" {
		return list
	}
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}

// fleetNodeIPs returns a set of known IP addresses of all servers in the fleet,
// mapped to all node IDs known to use each address (handling shared NAT or same-host loops).
func (s *Server) fleetNodeIPs(ctx context.Context) map[string][]string {
	s.listenerNameMu.Lock()
	cached, at := s.fleetIPCache, s.fleetIPCachedAt
	s.listenerNameMu.Unlock()
	if cached != nil && time.Since(at) < listenerNameCacheTTL {
		return cached
	}
	ips := make(map[string][]string)
	addIP := func(raw, nodeID string) {
		addr := strings.TrimSpace(raw)
		if addr == "" || nodeID == "" {
			return
		}
		if h, _, err := net.SplitHostPort(addr); err == nil {
			addr = h
		}
		addr = strings.Trim(addr, "[]")
		if parsed := net.ParseIP(addr); parsed != nil {
			ips[addr] = appendUniqueString(ips[addr], nodeID)
			canon := parsed.String()
			if canon != addr {
				ips[canon] = appendUniqueString(ips[canon], nodeID)
			}
		}
	}
	nodes, err := s.store.ListNodes(ctx)
	if err == nil {
		for _, n := range nodes {
			addIP(n.ClientAddress, n.ID)
			addIP(n.LastObservedIP, n.ID)
			for _, ipAddr := range n.IPAddresses {
				addIP(ipAddr, n.ID)
			}
			for pubIP := range n.IPMappings {
				addIP(pubIP, n.ID)
			}
		}
	}
	s.controlMu.Lock()
	for id, session := range s.controls {
		if session != nil && session.remoteAddr != "" {
			addIP(session.remoteAddr, id)
		}
	}
	s.controlMu.Unlock()

	s.listenerNameMu.Lock()
	s.fleetIPCache, s.fleetIPCachedAt = ips, time.Now()
	s.listenerNameMu.Unlock()
	return ips
}

// chainTargetUsers returns endpoint keys (listenerID+"\x00"+accountName and listenerID)
// on nodeID that are targeted by other nodes' chain outbounds.
func (s *Server) chainTargetUsers(ctx context.Context, nodeID string) map[string]bool {
	targets, _ := s.chainTargetUsersWithUpstreams(ctx, nodeID)
	return targets
}

func (s *Server) chainTargetUsersWithUpstreams(ctx context.Context, nodeID string) (map[string]bool, map[string][]string) {
	s.listenerNameMu.Lock()
	cached, ok := s.chainTargetCache[nodeID]
	s.listenerNameMu.Unlock()
	if ok && cached.targets != nil && time.Since(cached.at) < listenerNameCacheTTL {
		return cached.targets, cached.upstreams
	}
	targets := make(map[string]bool)
	upstreams := make(map[string][]string)
	rows, err := s.store.db.QueryContext(ctx, `
		SELECT e.listener_id, e.name, COALESCE(up.upstream_node_id, '')
		FROM endpoints e
		JOIN listeners l ON l.id = e.listener_id
		JOIN (
			SELECT SUBSTR(e2.outbound_id, 7) AS target_ep_id, l2.node_id AS upstream_node_id
			FROM endpoints e2
			JOIN listeners l2 ON l2.id = e2.listener_id
			JOIN nodes n2 ON n2.id = l2.node_id
			WHERE e2.outbound_id LIKE 'chain:%' AND e2.enabled = 1 AND l2.enabled = 1 AND n2.revoked_at IS NULL
			UNION
			SELECT SUBSTR(l2.outbound_id, 7) AS target_ep_id, l2.node_id AS upstream_node_id
			FROM listeners l2
			JOIN nodes n2 ON n2.id = l2.node_id
			WHERE l2.outbound_id LIKE 'chain:%' AND l2.enabled = 1 AND n2.revoked_at IS NULL
		) up ON up.target_ep_id = e.id
		WHERE l.node_id = ? AND e.enabled = 1 AND l.enabled = 1`, nodeID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var listenerID, name, upstreamNodeID string
			if err := rows.Scan(&listenerID, &name, &upstreamNodeID); err == nil {
				keySpecific := listenerID + "\x00" + name
				targets[keySpecific] = true
				targets[listenerID] = true
				if upstreamNodeID != "" && upstreamNodeID != nodeID {
					upstreams[keySpecific] = appendUniqueString(upstreams[keySpecific], upstreamNodeID)
					upstreams[listenerID] = appendUniqueString(upstreams[listenerID], upstreamNodeID)
				}
			}
		}
	}
	s.listenerNameMu.Lock()
	if s.chainTargetCache == nil {
		s.chainTargetCache = make(map[string]chainTargetEntry)
	}
	s.chainTargetCache[nodeID] = chainTargetEntry{targets: targets, upstreams: upstreams, at: time.Now()}
	s.listenerNameMu.Unlock()
	return targets, upstreams
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
