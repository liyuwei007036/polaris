package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

var ddnsReconcileBackoff = []time.Duration{0, 15 * time.Second, 30 * time.Second, 1 * time.Minute, 2 * time.Minute, 5 * time.Minute}

const ddnsPeriodicCheckInterval = 5 * time.Minute

type ddnsReconcileAttempt struct {
	inFlight      bool
	lastCheckedAt time.Time
	lastIP        string
	lastSignature string
	failures      int
	nextRetryAt   time.Time
}

func ipMatchesFamily(ip net.IP, wantIPv4 bool) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	if wantIPv4 {
		return ip.To4() != nil
	}
	return ip.To4() == nil
}

func isPublicRoutableIP(ip net.IP, wantIPv4 bool) bool {
	return ipMatchesFamily(ip, wantIPv4) && !ip.IsPrivate()
}

// fetchMasterPublicIP queries external public endpoints to get the outbound IPv4 of Master.
func (s *Server) fetchMasterPublicIP(ctx context.Context) (string, error) {
	return s.fetchMasterPublicIPFamily(ctx, true, "")
}

// fetchMasterPublicIPFamily queries external public endpoints for either IPv4
// or IPv6, optionally binding to a specific local interface IP on Master.
func (s *Server) fetchMasterPublicIPFamily(ctx context.Context, wantIPv4 bool, localBindIP string) (string, error) {
	network := "tcp4"
	if !wantIPv4 {
		network = "tcp6"
	}
	dialer := &net.Dialer{Timeout: 4 * time.Second}
	if parsed := net.ParseIP(strings.TrimSpace(localBindIP)); parsed != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: parsed}
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(dialCtx context.Context, _, addr string) (net.Conn, error) {
				return dialer.DialContext(dialCtx, network, addr)
			},
		},
	}

	endpoints := []string{
		"https://cloudflare.com/cdn-cgi/trace",
		"https://api.ipify.org",
		"https://icanhazip.com",
	}
	if !wantIPv4 {
		endpoints = []string{
			"https://cloudflare.com/cdn-cgi/trace",
			"https://api6.ipify.org",
			"https://icanhazip.com",
		}
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

		if parsed := net.ParseIP(candidate); isPublicRoutableIP(parsed, wantIPv4) {
			return parsed.String(), nil
		}
	}
	return "", errors.New("unable to determine master public IP from external endpoints")
}

func resolveMappedPublicIP(node Node, localBindIP string, wantIPv4 bool) string {
	targetBind := net.ParseIP(strings.TrimSpace(localBindIP))
	if targetBind == nil || len(node.IPMappings) == 0 {
		return ""
	}
	pubIPs := make([]string, 0, len(node.IPMappings))
	for pubIP, mappedLocal := range node.IPMappings {
		if localParsed := net.ParseIP(strings.TrimSpace(mappedLocal)); localParsed != nil && localParsed.Equal(targetBind) {
			pubIPs = append(pubIPs, pubIP)
		}
	}
	sort.Strings(pubIPs)
	for _, pubIP := range pubIPs {
		if parsed := net.ParseIP(strings.TrimSpace(pubIP)); isPublicRoutableIP(parsed, wantIPv4) {
			return parsed.String()
		}
	}
	return ""
}

func (s *Server) resolveNodeDDNSRecordIP(ctx context.Context, node Node, explicitAutoIP, recordID, recordType string, allowMasterFallback bool) string {
	if recordType != "A" && recordType != "AAAA" {
		return ""
	}
	wantIPv4 := recordType == "A"
	spec := firstNonEmpty(node.DDNSRecordIPs[recordID], node.DDNSBindIP)

	if spec != "" {
		parsedSpec := net.ParseIP(spec)
		if parsedSpec == nil || !ipMatchesFamily(parsedSpec, parsedSpec.To4() != nil) {
			return ""
		}
		// Prefer NAT mapping lookup so a bound local NIC IP tracks its rotating public IP.
		if mapped := resolveMappedPublicIP(node, spec, wantIPv4); mapped != "" {
			return mapped
		}
		if parsedSpec.IsPrivate() {
			// Local NIC bind IP with no NAT mapping from agent: probe on Master if local.
			if allowMasterFallback {
				if detected, err := s.fetchMasterPublicIPFamily(ctx, wantIPv4, parsedSpec.String()); err == nil && detected != "" {
					return detected
				}
			}
			return ""
		}
		if isPublicRoutableIP(parsedSpec, wantIPv4) {
			return parsedSpec.String()
		}
		return ""
	}

	// Auto mode: prefer explicit caller IP (when valid public), active agent session IP,
	// agent-reported IPAddresses matching record family, NAT IPMappings, or Master fallback.
	if p := net.ParseIP(strings.TrimSpace(explicitAutoIP)); isPublicRoutableIP(p, wantIPv4) {
		return p.String()
	}
	activeAddr := strings.TrimSpace(s.nodeActiveAddress(node.ID))
	if p := net.ParseIP(activeAddr); isPublicRoutableIP(p, wantIPv4) {
		return p.String()
	}
	for _, rawIP := range node.IPAddresses {
		if p := net.ParseIP(strings.TrimSpace(rawIP)); isPublicRoutableIP(p, wantIPv4) {
			return p.String()
		}
	}
	if len(node.IPMappings) > 0 {
		pubKeys := make([]string, 0, len(node.IPMappings))
		for k := range node.IPMappings {
			pubKeys = append(pubKeys, k)
		}
		sort.Strings(pubKeys)
		for _, k := range pubKeys {
			if p := net.ParseIP(strings.TrimSpace(k)); isPublicRoutableIP(p, wantIPv4) {
				return p.String()
			}
		}
	}
	if p := net.ParseIP(strings.TrimSpace(explicitAutoIP)); ipMatchesFamily(p, wantIPv4) {
		// Accept non-private custom/test IPs passed directly if valid routable family.
		if !p.IsPrivate() {
			return p.String()
		}
	}
	if allowMasterFallback {
		activeParsed := net.ParseIP(activeAddr)
		if activeParsed == nil || activeParsed.IsLoopback() || activeParsed.IsUnspecified() || activeParsed.IsLinkLocalUnicast() || activeParsed.IsPrivate() {
			if detected, err := s.fetchMasterPublicIPFamily(ctx, wantIPv4, ""); err == nil && detected != "" {
				return detected
			}
		}
	}
	return ""
}

func (s *Server) computeDDNSTargetDigest(ctx context.Context, node Node, explicitAutoIP string) (primaryIP, digest string) {
	recordIDs := node.DDNSRecordIDs
	if len(recordIDs) == 0 && strings.TrimSpace(node.DDNSRecordID) != "" {
		recordIDs = []string{strings.TrimSpace(node.DDNSRecordID)}
	}
	parts := make([]string, 0, len(recordIDs)+2)
	parts = append(parts, "bind="+node.DDNSBindIP)

	for _, recID := range recordIDs {
		recID = strings.TrimSpace(recID)
		if recID == "" {
			continue
		}
		v4 := s.resolveNodeDDNSRecordIP(ctx, node, explicitAutoIP, recID, "A", false)
		v6 := s.resolveNodeDDNSRecordIP(ctx, node, explicitAutoIP, recID, "AAAA", false)
		parts = append(parts, fmt.Sprintf("%s=%s/%s", recID, v4, v6))
		if primaryIP == "" {
			if v4 != "" {
				primaryIP = v4
			} else if v6 != "" {
				primaryIP = v6
			}
		}
	}
	if primaryIP == "" {
		v4 := s.resolveNodeDDNSRecordIP(ctx, node, explicitAutoIP, "", "A", false)
		if v4 != "" {
			primaryIP = v4
		} else {
			primaryIP = s.resolveNodeDDNSRecordIP(ctx, node, explicitAutoIP, "", "AAAA", false)
		}
	}
	return primaryIP, strings.Join(parts, "|")
}

// executeNodeDDNS performs the actual DNS record lookup and update with Cloudflare for all bound records of the node.
func (s *Server) executeNodeDDNS(ctx context.Context, nodeID, currentIP string) error {
	_, err := s.executeNodeDDNSWithResult(ctx, nodeID, currentIP)
	return err
}

func (s *Server) executeNodeDDNSWithResult(ctx context.Context, nodeID, currentIP string) (string, error) {
	s.ddnsMu.Lock()
	defer s.ddnsMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if !node.DDNSEnabled {
		return "", nil
	}

	recordIDs := node.DDNSRecordIDs
	if len(recordIDs) == 0 && strings.TrimSpace(node.DDNSRecordID) != "" {
		recordIDs = []string{strings.TrimSpace(node.DDNSRecordID)}
	}
	if len(recordIDs) == 0 {
		return "", nil
	}

	zones, err := s.store.CloudflareZones(ctx)
	if err != nil {
		return "", fmt.Errorf("cloudflare zone not configured: %w", err)
	}
	if len(zones) == 0 {
		return "", errors.New("cloudflare zone not configured")
	}

	var (
		updatedDomains []string
		primaryIP      string
		oldIP          string
		fallbackOldIP  string
		anyResolved    bool
		lastErr        error
	)

	for _, recID := range recordIDs {
		recID = strings.TrimSpace(recID)
		if recID == "" {
			continue
		}
		record, zone, err := findCloudflareRecordInZones(ctx, zones, recID)
		if err != nil {
			log.Printf("ddns: get cloudflare record %s for node %s failed: %v", recID, node.Name, err)
			lastErr = err
			continue
		}
		targetIP := s.resolveNodeDDNSRecordIP(ctx, node, currentIP, recID, record.Type, true)
		if targetIP == "" {
			continue
		}
		anyResolved = true
		if primaryIP == "" || (record.Type == "A" && net.ParseIP(primaryIP).To4() == nil) {
			primaryIP = targetIP
		}
		if fallbackOldIP == "" {
			fallbackOldIP = record.Content
		}
		if record.Content != targetIP {
			prevContent := record.Content
			record.Content = targetIP
			if record.TTL > 120 {
				record.TTL = 60
			}
			updated, err := zone.Client.UpdateRecord(ctx, zone.ID, record)
			if err != nil {
				log.Printf("ddns: update cloudflare record %s (%s) in %s failed: %v", record.Name, recID, zone.Name, err)
				lastErr = err
				continue
			}
			if oldIP == "" {
				oldIP = prevContent
			}
			updatedDomains = append(updatedDomains, updated.Name)
		}
	}
	if oldIP == "" {
		oldIP = fallbackOldIP
	}

	if lastErr != nil && len(updatedDomains) == 0 {
		return "", lastErr
	}
	if !anyResolved || primaryIP == "" {
		return "", errors.New("无效的公网 IP 地址")
	}

	ipChanged := len(updatedDomains) > 0 || node.LastObservedIP != primaryIP

	if len(updatedDomains) > 0 {
		operatorID := s.store.SystemOperatorID(ctx)
		if operatorID != "" {
			summary := fmt.Sprintf("节点「%s」公网 IP 变更为 %s（原 %s），已自动更新 Cloudflare 解析：%s",
				node.Name, primaryIP, oldIP, strings.Join(updatedDomains, "、"))
			_ = s.store.AppendAudit(ctx, operatorID, "cloudflare_record.ddns_updated", "node", node.ID, summary)
		}
		if s.alertEngine != nil {
			s.alertEngine.NotifyDDNSUpdated(node.ID, node.Name, oldIP, primaryIP, strings.Join(updatedDomains, "、"))
		}
	}

	_ = s.store.SetNodeLastObservedIP(ctx, node.ID, primaryIP)
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
			if _, dispatchErr := s.dispatchNodeDNSUpdate(ctx, chainedNodeIDs, domains, primaryIP, operatorID); dispatchErr != nil {
				log.Printf("ddns: dispatch dns.update for node %s (%v) failed: %v", node.ID, chainedNodeIDs, dispatchErr)
			} else if s.alertEngine != nil {
				s.alertEngine.NotifyChainedDNSHealing(node.Name, len(chainedNodeIDs), domains, primaryIP)
			}
		}
	}

	s.liveHub.publish(liveEvent{Kind: "node", NodeID: node.ID})
	s.liveHub.publish(liveEvent{Kind: "cloudflare"})

	_, signature := s.computeDDNSTargetDigest(ctx, node, primaryIP)
	s.ddnsReconcileMu.Lock()
	if s.ddnsReconcileState != nil {
		if st := s.ddnsReconcileState[node.ID]; st != nil {
			st.failures = 0
			st.lastIP = primaryIP
			st.lastSignature = signature
			st.lastCheckedAt = time.Now()
			st.nextRetryAt = time.Time{}
		}
	}
	s.ddnsReconcileMu.Unlock()
	return primaryIP, nil
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

	activeAddr := s.nodeActiveAddress(nodeID)
	primaryIP, signature := s.computeDDNSTargetDigest(ctx, node, activeAddr)
	if primaryIP == "" {
		detected, err := s.fetchMasterPublicIP(ctx)
		if err != nil || detected == "" {
			return
		}
		activeAddr = detected
		primaryIP, signature = s.computeDDNSTargetDigest(ctx, node, activeAddr)
	}
	if primaryIP == "" {
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
	hasBinding := node.DDNSBindIP != "" || len(node.DDNSRecordIPs) > 0
	if node.LastObservedIP != primaryIP || state.lastIP != primaryIP || (hasBinding && state.lastSignature != signature) {
		if state.nextRetryAt.IsZero() || now.After(state.nextRetryAt) {
			needsSync = true
		}
	} else if state.lastSignature != "" && state.lastSignature != signature {
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
		resolvedPrimary, err := s.executeNodeDDNSWithResult(context.Background(), nodeID, activeAddr)
		finishTime := time.Now()

		s.ddnsReconcileMu.Lock()
		defer s.ddnsReconcileMu.Unlock()
		state.inFlight = false
		if err == nil {
			if resolvedPrimary == "" {
				resolvedPrimary = primaryIP
			}
			state.failures = 0
			state.lastIP = resolvedPrimary
			state.lastSignature = signature
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
	resolvedIP, err := s.executeNodeDDNSWithResult(r.Context(), node.ID, currentIP)
	if err != nil {
		if strings.Contains(err.Error(), "无效的公网 IP 地址") {
			writeError(w, userErrorf("当前无法获取该服务器的公网 IP，请确认服务器已上线或检查 DDNS 绑定 IP"))
			return
		}
		writeError(w, err)
		return
	}
	updatedNode, _ := s.store.GetNode(r.Context(), node.ID)
	if operator.ID != "" {
		domainsStr := strings.Join(updatedNode.DDNSDomains, "、")
		if domainsStr == "" {
			domainsStr = updatedNode.DDNSDomain
		}
		summary := fmt.Sprintf("手动同步：节点「%s」公网 IP 更新为 %s，已更新 Cloudflare 解析 %s", updatedNode.Name, resolvedIP, domainsStr)
		_ = s.store.AppendAudit(r.Context(), operator.ID, "cloudflare_record.ddns_synced", "node", updatedNode.ID, summary)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "ip": resolvedIP, "domains": updatedNode.DDNSDomains})
}
