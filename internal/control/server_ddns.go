package control

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// fetchMasterPublicIP queries external public endpoints to get the outbound IPv4 of Master.
func (s *Server) fetchMasterPublicIP(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	endpoints := []string{
		"https://cloudflare.com/cdn-cgi/trace",
		"https://api.ipify.org",
		"https://icanhazip.com",
	}

	for _, ep := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}

		text := string(body)
		var candidate string
		if strings.Contains(ep, "cdn-cgi/trace") {
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "ip=") {
					candidate = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
					break
				}
			}
		} else {
			candidate = strings.TrimSpace(text)
		}

		parsed := net.ParseIP(candidate)
		if parsed != nil && parsed.To4() != nil && !parsed.IsLoopback() && !parsed.IsPrivate() && !parsed.IsUnspecified() {
			return candidate, nil
		}
	}
	return "", errors.New("unable to determine master public IP from external endpoints")
}

// executeNodeDDNS performs the actual DNS record lookup and update with Cloudflare for all bound records of the node.
func (s *Server) executeNodeDDNS(ctx context.Context, nodeID, currentIP string) error {
	currentIP = strings.TrimSpace(currentIP)
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		// Detect public IP (especially for Master node / local combined agent)
		detected, err := s.fetchMasterPublicIP(ctx)
		if err == nil && detected != "" {
			currentIP = detected
			ip = net.ParseIP(currentIP)
		}
	}
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
	if !node.DDNSEnabled {
		return nil
	}

	recordIDs := node.DDNSRecordIDs
	if len(recordIDs) == 0 && strings.TrimSpace(node.DDNSRecordID) != "" {
		recordIDs = []string{strings.TrimSpace(node.DDNSRecordID)}
	}
	if len(recordIDs) == 0 {
		return nil
	}

	zoneID, _, client, err := s.store.CloudflareZone(ctx)
	if err != nil {
		return fmt.Errorf("cloudflare zone not configured: %w", err)
	}

	isIPv4 := ip.To4() != nil
	var updatedDomains []string
	var oldIP string
	var lastErr error

	for _, recID := range recordIDs {
		recID = strings.TrimSpace(recID)
		if recID == "" {
			continue
		}
		record, err := client.GetRecord(ctx, zoneID, recID)
		if err != nil {
			log.Printf("ddns: get cloudflare record %s for node %s failed: %v", recID, node.Name, err)
			lastErr = err
			continue
		}
		if isIPv4 && record.Type != "A" {
			continue
		}
		if !isIPv4 && record.Type != "AAAA" {
			continue
		}
		if oldIP == "" {
			oldIP = record.Content
		}
		if record.Content != currentIP {
			record.Content = currentIP
			if record.TTL > 120 {
				record.TTL = 60
			}
			updated, err := s.store.UpdateCloudflareRecord(ctx, record)
			if err != nil {
				log.Printf("ddns: update cloudflare record %s (%s) failed: %v", record.Name, recID, err)
				lastErr = err
				continue
			}
			updatedDomains = append(updatedDomains, updated.Name)
		}
	}

	if lastErr != nil && len(updatedDomains) == 0 {
		return lastErr
	}

	ipChanged := len(updatedDomains) > 0 || node.LastObservedIP != currentIP

	if len(updatedDomains) > 0 {
		operatorID := s.store.SystemOperatorID(ctx)
		if operatorID != "" {
			summary := fmt.Sprintf("节点「%s」公网 IP 变更为 %s（原 %s），已自动更新 Cloudflare 解析：%s",
				node.Name, currentIP, oldIP, strings.Join(updatedDomains, "、"))
			_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_updated", "node", node.ID, summary)
		}
		if s.alertEngine != nil {
			s.alertEngine.NotifyDDNSUpdated(node.ID, node.Name, oldIP, currentIP, strings.Join(updatedDomains, "、"))
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
			if len(node.DDNSDomains) > 0 {
				domains = append(domains, node.DDNSDomains...)
			} else if node.DDNSDomain != "" {
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
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil || !node.DDNSEnabled {
		return
	}
	if len(node.DDNSRecordIDs) == 0 && strings.TrimSpace(node.DDNSRecordID) == "" {
		return
	}

	currentIP := s.nodeActiveAddress(nodeID)
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		detected, err := s.fetchMasterPublicIP(ctx)
		if err == nil && detected != "" {
			currentIP = detected
		} else {
			return
		}
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
	return s.executeNodeDDNS(ctx, node.ID, currentIP)
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
	if !node.DDNSEnabled || (len(node.DDNSRecordIDs) == 0 && strings.TrimSpace(node.DDNSRecordID) == "") {
		writeError(w, userErrorf("该服务器未启用 DDNS 或未关联解析记录"))
		return
	}
	currentIP := s.nodeActiveAddress(node.ID)
	if currentIP == "" {
		currentIP = node.LastObservedIP
	}
	ip := net.ParseIP(currentIP)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
		detected, err := s.fetchMasterPublicIP(r.Context())
		if err == nil && detected != "" {
			currentIP = detected
		}
	}
	if currentIP == "" {
		writeError(w, userErrorf("当前无法获取该服务器的公网 IP，请确认服务器已上线"))
		return
	}
	if err := s.executeNodeDDNS(r.Context(), node.ID, currentIP); err != nil {
		writeError(w, err)
		return
	}
	updatedNode, _ := s.store.GetNode(r.Context(), node.ID)
	if operator.ID != "" {
		domainsStr := strings.Join(updatedNode.DDNSDomains, "、")
		if domainsStr == "" {
			domainsStr = updatedNode.DDNSDomain
		}
		summary := fmt.Sprintf("手动同步：节点「%s」公网 IP 更新为 %s，已更新 Cloudflare 解析 %s", updatedNode.Name, currentIP, domainsStr)
		_ = s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.ddns_synced", "node", updatedNode.ID, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "ip": currentIP, "domains": updatedNode.DDNSDomains})
}
