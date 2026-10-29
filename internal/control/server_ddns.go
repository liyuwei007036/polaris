package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

var ddnsReconcileBackoff = []time.Duration{0, 15 * time.Second, 30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute}

const ddnsPeriodicCheckInterval = 5 * time.Minute

type ddnsReconcileAttempt struct {
	inFlight      bool
	lastCheckedAt time.Time
	lastIP        string
	failures      int
	nextRetryAt   time.Time
}

// executeNodeDDNS performs the actual DNS record lookup and update with Cloudflare.
func (s *Server) executeNodeDDNS(ctx context.Context, nodeID, currentIP string) error {
	currentIP = strings.TrimSpace(currentIP)
	if currentIP == "" {
		return nil
	}
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return errors.New("无效的公网 IP 地址")
	}

	s.ddnsMu.Lock()
	defer s.ddnsMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return err
	}
	if !node.DDNSEnabled || strings.TrimSpace(node.DDNSRecordID) == "" {
		return nil
	}

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		return fmt.Errorf("cloudflare zone not configured: %w", err)
	}

	record, err := client.GetRecord(ctx, zoneID, node.DDNSRecordID)
	if err != nil {
		return fmt.Errorf("get cloudflare record failed: %w", err)
	}

	isIPv4 := ip.To4() != nil
	if isIPv4 && record.Type != "A" {
		return fmt.Errorf("record %s type is %s, expected A for IPv4 %s", record.Name, record.Type, currentIP)
	}
	if !isIPv4 && record.Type != "AAAA" {
		return fmt.Errorf("record %s type is %s, expected AAAA for IPv6 %s", record.Name, record.Type, currentIP)
	}

	oldIP := record.Content
	ipChanged := record.Content != currentIP || node.LastObservedIP != currentIP
	if record.Content != currentIP {
		record.Content = currentIP
		if record.TTL > 120 {
			record.TTL = 60
		}
		updated, err := s.store.UpdateCloudflareRecord(ctx, record)
		if err != nil {
			return fmt.Errorf("update cloudflare record failed: %w", err)
		}

		operatorID := s.store.SystemOperatorID(ctx)
		if operatorID != "" {
			summary := fmt.Sprintf("NAT 节点「%s」公网 IP 变更为 %s（原 %s），已自动更新 Cloudflare 解析 %s", node.Name, currentIP, oldIP, updated.Name)
			_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_updated", "node", node.ID, summary)
		}
		if s.alertEngine != nil {
			s.alertEngine.NotifyDDNSUpdated(node.ID, node.Name, oldIP, currentIP, updated.Name)
		}
	}

	_ = s.store.SetNodeLastObservedIP(ctx, node.ID, currentIP)
	if node.DDNSDomain != "" && (node.ClientAddress == "" || node.ClientAddress == oldIP || net.ParseIP(node.ClientAddress) != nil) {
		_ = s.store.SetNodeClientAddress(ctx, node.ID, node.DDNSDomain)
	}

	if ipChanged {
		chainedNodeIDs, err := s.store.NodeChainedNodeIDs(ctx, node.ID)
		if err == nil && len(chainedNodeIDs) > 0 {
			var domains []string
			if node.DDNSDomain != "" {
				domains = append(domains, node.DDNSDomain)
			}
			listenerDomains, _ := s.store.NodeConnectionDomains(ctx, node.ID)
			domains = append(domains, listenerDomains...)
			operatorID := s.store.SystemOperatorID(ctx)
			if _, dispatchErr := s.dispatchNodeDNSUpdate(ctx, chainedNodeIDs, domains, currentIP, operatorID); dispatchErr != nil {
				log.Printf("ddns: dispatch dns.update for node %s (%v) failed: %v", node.ID, chainedNodeIDs, dispatchErr)
			} else if s.alertEngine != nil {
				s.alertEngine.NotifyChainedDNSHealing(node.Name, len(chainedNodeIDs), domains, currentIP)
			}
		}
	}

	s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	s.liveHub.publish(liveEvent{Kind: "cloudflare"})

	s.ddnsReconcileMu.Lock()
	if s.ddnsReconcileState != nil {
		if st := s.ddnsReconcileState[node.ID]; st != nil {
			st.failures = 0
			st.lastIP = currentIP
			st.lastCheckedAt = time.Now()
			st.nextRetryAt = time.Time{}
		}
	}
	s.ddnsReconcileMu.Unlock()
	return nil
}

// triggerNodeDDNS is invoked whenever an agent connects or reconnects, or when
// an operator edits DDNS settings. It actively checks and updates the bound Cloudflare DNS record.
func (s *Server) triggerNodeDDNS(ctx context.Context, nodeID, currentIP string) {
	if err := s.executeNodeDDNS(ctx, nodeID, currentIP); err != nil {
		log.Printf("ddns: node %s update failed: %v", nodeID, err)
	}
}

// reconcileNodeDDNS is invoked whenever an agent reports status (e.g. heartbeat wire.MsgStatus).
// It ensures continuous drift correction and retry on transient failures during ongoing reporting,
// instead of relying exclusively on the single reconnect instant.
func (s *Server) reconcileNodeDDNS(ctx context.Context, nodeID string) {
	currentIP := s.nodeActiveAddress(nodeID)
	if currentIP == "" {
		return
	}
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return
	}

	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil || !node.DDNSEnabled || strings.TrimSpace(node.DDNSRecordID) == "" {
		return
	}

	now := time.Now()
	s.ddnsReconcileMu.Lock()
	if s.ddnsReconcileState == nil {
		s.ddnsReconcileState = make(map[string]*ddnsReconcileAttempt)
	}
	state := s.ddnsReconcileState[nodeID]
	if state == nil {
		state = &ddnsReconcileAttempt{}
		s.ddnsReconcileState[nodeID] = state
	}
	if state.inFlight {
		s.ddnsReconcileMu.Unlock()
		return
	}

	needsSync := false
	if node.LastObservedIP != currentIP || state.lastIP != currentIP {
		if state.nextRetryAt.IsZero() || now.After(state.nextRetryAt) {
			needsSync = true
		}
	} else if now.Sub(state.lastCheckedAt) >= ddnsPeriodicCheckInterval {
		needsSync = true
	}

	if !needsSync {
		s.ddnsReconcileMu.Unlock()
		return
	}

	state.inFlight = true
	s.ddnsReconcileMu.Unlock()

	go func() {
		err := s.executeNodeDDNS(context.Background(), nodeID, currentIP)
		finishTime := time.Now()

		s.ddnsReconcileMu.Lock()
		defer s.ddnsReconcileMu.Unlock()
		state.inFlight = false
		if err == nil {
			state.failures = 0
			state.lastIP = currentIP
			state.lastCheckedAt = finishTime
			state.nextRetryAt = time.Time{}
		} else {
			state.failures++
			idx := state.failures
			if idx >= len(ddnsReconcileBackoff) {
				idx = len(ddnsReconcileBackoff) - 1
			}
			state.nextRetryAt = finishTime.Add(ddnsReconcileBackoff[idx])
			log.Printf("ddns: continuous reconciliation for node %s (%s) failed (attempt %d, next retry in %v): %v",
				nodeID, node.Name, state.failures, ddnsReconcileBackoff[idx], err)
		}
	}()
}

// syncNodeDDNSNow forces an immediate Cloudflare DNS record update for the specified node and IP.
func (s *Server) syncNodeDDNSNow(ctx context.Context, node Node, currentIP, operatorID string) error {
	currentIP = strings.TrimSpace(currentIP)
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		return errors.New("无效的公网 IP 地址")
	}

	s.ddnsMu.Lock()
	defer s.ddnsMu.Unlock()

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		return fmt.Errorf("Cloudflare 未配置或连接不可用: %w", err)
	}

	record, err := client.GetRecord(ctx, zoneID, node.DDNSRecordID)
	if err != nil {
		return fmt.Errorf("获取 Cloudflare 解析记录失败: %w", err)
	}

	isIPv4 := ip.To4() != nil
	if isIPv4 && record.Type != "A" {
		return fmt.Errorf("记录类型为 %s，与当前 IPv4 地址不匹配", record.Type)
	}
	if !isIPv4 && record.Type != "AAAA" {
		return fmt.Errorf("记录类型为 %s，与当前 IPv6 地址不匹配", record.Type)
	}

	oldIP := record.Content
	record.Content = currentIP
	if record.TTL > 120 {
		record.TTL = 60
	}

	updated, err := s.store.UpdateCloudflareRecord(ctx, record)
	if err != nil {
		return fmt.Errorf("更新 Cloudflare 记录失败: %w", err)
	}

	_ = s.store.SetNodeLastObservedIP(ctx, node.ID, currentIP)
	if node.DDNSDomain != "" && (node.ClientAddress == "" || node.ClientAddress == oldIP || net.ParseIP(node.ClientAddress) != nil) {
		_ = s.store.SetNodeClientAddress(ctx, node.ID, node.DDNSDomain)
	}

	chainedNodeIDs, err := s.store.NodeChainedNodeIDs(ctx, node.ID)
	if err == nil && len(chainedNodeIDs) > 0 {
		var domains []string
		if node.DDNSDomain != "" {
			domains = append(domains, node.DDNSDomain)
		}
		listenerDomains, _ := s.store.NodeConnectionDomains(ctx, node.ID)
		domains = append(domains, listenerDomains...)
		if _, dispatchErr := s.dispatchNodeDNSUpdate(ctx, chainedNodeIDs, domains, currentIP, operatorID); dispatchErr != nil {
			log.Printf("ddns: sync dns.update for node %s (%v) failed: %v", node.ID, chainedNodeIDs, dispatchErr)
		} else if s.alertEngine != nil {
			s.alertEngine.NotifyChainedDNSHealing(node.Name, len(chainedNodeIDs), domains, currentIP)
		}
	}

	if s.alertEngine != nil && oldIP != currentIP {
		s.alertEngine.NotifyDDNSUpdated(node.ID, node.Name, oldIP, currentIP, updated.Name)
	}

	if operatorID != "" {
		summary := fmt.Sprintf("手动同步：NAT 节点「%s」公网 IP 更新为 %s，已更新 Cloudflare 解析 %s", node.Name, currentIP, updated.Name)
		_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_synced", "node", node.ID, summary)
	}

	s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	s.liveHub.publish(liveEvent{Kind: "cloudflare"})

	s.ddnsReconcileMu.Lock()
	if s.ddnsReconcileState != nil {
		if st := s.ddnsReconcileState[node.ID]; st != nil {
			st.failures = 0
			st.lastIP = currentIP
			st.lastCheckedAt = time.Now()
			st.nextRetryAt = time.Time{}
		}
	}
	s.ddnsReconcileMu.Unlock()
	return nil
}

// syncNodeDDNS is the HTTP handler for POST /api/v1/nodes/{id}/ddns/sync.
func (s *Server) syncNodeDDNS(w http.ResponseWriter, r *http.Request) {
	operator, err := s.writer(r)
	if err != nil {
		writeError(w, err)
		return
	}
	nodeID := r.PathValue("id")
	node, err := s.store.GetNode(r.Context(), nodeID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !node.DDNSEnabled || strings.TrimSpace(node.DDNSRecordID) == "" {
		writeError(w, userErrorf("该服务器未启用 DDNS 或未关联解析记录"))
		return
	}
	currentIP := s.nodeActiveAddress(node.ID)
	if currentIP == "" {
		currentIP = node.LastObservedIP
	}
	if currentIP == "" {
		writeError(w, userErrorf("当前无法获取该服务器的公网 IP，请确认服务器已上线"))
		return
	}
	if err := s.syncNodeDDNSNow(r.Context(), node, currentIP, operator.ID); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ip": currentIP})
}
